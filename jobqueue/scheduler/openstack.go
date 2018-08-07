// Copyright © 2016-2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package scheduler

// This file contains a scheduleri implementation for 'openstack': running jobs
// on servers spawned on demand.

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid"
	"github.com/inconshreveable/log15"
	logext "github.com/inconshreveable/log15/ext"
)

const (
	unquotadVal      = 1000000 // a "large" number for use when we don't have quota
	standinNotNeeded = "standin no longer needed"
)

// debugCounter and debugEffect are used by tests to prove some bugs
var debugCounter int
var debugEffect string

// opst is our implementer of scheduleri. It takes much of its implementation
// from the local scheduler.
type opst struct {
	local
	config            *ConfigOpenStack
	provider          *cloud.Provider
	quotaMaxInstances int
	quotaMaxCores     int
	quotaMaxRAM       int
	quotaMaxVolume    int
	reservedInstances int
	reservedCores     int
	reservedRAM       int
	reservedVolume    int
	servers           map[string]*cloud.Server
	standins          map[string]*standin
	spawningNow       bool
	waitingToSpawn    int
	cmdToStandins     map[string]map[string]bool
	standinToCmd      map[string]map[string]bool
	updatingState     bool
	cbmutex           sync.RWMutex
	msgCB             MessageCallBack
	badServerCB       BadServerCallBack
	log15.Logger
}

// ConfigOpenStack represents the configuration options required by the
// OpenStack scheduler. All are required with no usable defaults, unless
// otherwise noted. This struct implements the CloudConfig interface.
type ConfigOpenStack struct {
	// ResourceName is the resource name prefix used to name any resources (such
	// as keys, security groups and servers) that need to be created.
	ResourceName string

	// OSPrefix is the prefix or full name of the Operating System image you
	// wish spawned servers to run by default (overridden during Schedule() by a
	// Requirements.Other["cloud_os"] value)
	OSPrefix string

	// OSUser is the login username of your chosen Operating System from
	// OSPrefix. (Overridden during Schedule() by a
	// Requirements.Other["cloud_user"] value.)
	OSUser string

	// OSRAM is the minimum RAM in MB needed to bring up a server instance that
	// runs your Operating System image. It defaults to 2048. (Overridden during
	// Schedule() by a Requirements.Other["cloud_os_ram"] value.)
	OSRAM int

	// OSDisk is the minimum disk in GB with which to bring up a server instance
	// that runs your Operating System image. It defaults to 1. (Overridden
	// during Schedule() by a Requirements.Disk value.)
	OSDisk int

	// FlavorRegex is a regular expression that you can use to limit what
	// flavors of server will be created to run commands on. The default of an
	// empty string means there is no limit, and any available flavor can be
	// used. (The flavor chosen for a command will be the flavor with the least
	// specifications (RAM, CPUs, Disk) capable of running the command, that
	// also satisfies this regex.)
	FlavorRegex string

	// PostCreationScript is the []byte content of a script you want executed
	// after a server is Spawn()ed. (Overridden during Schedule() by a
	// Requirements.Other["cloud_script"] value.)
	PostCreationScript []byte

	// ConfigFiles is a comma separated list of paths to config files that
	// should be copied over to all spawned servers. Absolute paths are copied
	// over to the same absolute path on the new server. To handle a config file
	// that should remain relative to the home directory (and where the spawned
	// server may have a different username and thus home directory path
	// compared to the current server), use the prefix ~/ to signify the home
	// directory. It silently ignores files that don't exist locally.
	// (Appended to during Schedule() by a
	// Requirements.Other["cloud_config_files"] value.)
	ConfigFiles string

	// ServerPorts are the TCP port numbers you need to be open for
	// communication with any spawned servers. At a minimum you will need to
	// specify []int{22}.
	ServerPorts []int

	// SavePath is an absolute path to a file on disk where details of any
	// created resources can be read from and written to.
	SavePath string

	// ServerKeepTime is the time to wait before an idle server is destroyed.
	// Zero duration means "never destroy due to being idle".
	ServerKeepTime time.Duration

	// StateUpdateFrequency is the frequency at which to check spawned servers
	// that are being used to run things, to see if they're still alive.
	// 0 (default) is treated as 1 minute.
	StateUpdateFrequency time.Duration

	// MaxInstances is the maximum number of instances we are allowed to spawn.
	// -1 means we will be limited by your quota, if any. 0 (the default) means
	// no additional instances will be spawned (commands will run locally on the
	// same instance the manager is running on).
	MaxInstances int

	// Shell is the shell to use to run your commands with; 'bash' is
	// recommended.
	Shell string

	// CIDR describes the range of network ips that can be used to spawn
	// OpenStack servers on which to run our commands. The default is
	// "192.168.0.0/18", which allows for 16381 servers to be spawned. This
	// range ends at 192.168.63.254.
	CIDR string

	// GatewayIP is the gateway ip address for the subnet that will be created
	// with the given CIDR. It defaults to 192.168.0.1.
	GatewayIP string

	// DNSNameServers is a slice of DNS IP addresses to use for lookups on the
	// created subnet. It defaults to Google's: []string{"8.8.4.4", "8.8.8.8"}
	DNSNameServers []string
}

// AddConfigFile takes a value as per the ConfigFiles property, and appends it
// to the existing ConfigFiles value (or sets it if unset).
func (c *ConfigOpenStack) AddConfigFile(configFile string) {
	if c.ConfigFiles == "" {
		c.ConfigFiles = configFile
	} else {
		c.ConfigFiles += "," + configFile
	}
}

// standin describes a server that we're in the middle of spawning (or intend to
// spawn in the future), allowing us to keep track of command->server
// allocations while they're still being created.
type standin struct {
	id             string
	flavor         *cloud.Flavor
	disk           int
	os             string
	script         []byte
	configFiles    string // in cloud.Server.CopyOver() format
	sharedDisk     bool
	usedRAM        int
	usedCores      int
	usedDisk       int
	mutex          sync.RWMutex
	alreadyFailed  bool
	failReason     string
	nowWaiting     int // for waitForServer()
	endWait        chan *cloud.Server
	waitingToSpawn bool // for isExtraneous() and opst's runCmd()
	readyToSpawn   chan bool
	noLongerNeeded chan bool
	log15.Logger
}

// newStandin returns a new standin server
func newStandin(id string, flavor *cloud.Flavor, disk int, osPrefix string, script []byte, configFiles string, sharedDisk bool, logger log15.Logger) *standin {
	availableDisk := flavor.Disk
	if disk > availableDisk {
		availableDisk = disk
	}
	return &standin{
		id:             id,
		flavor:         flavor,
		disk:           availableDisk,
		os:             osPrefix,
		script:         script,
		configFiles:    configFiles,
		sharedDisk:     sharedDisk,
		waitingToSpawn: true,
		endWait:        make(chan *cloud.Server),
		readyToSpawn:   make(chan bool),
		noLongerNeeded: make(chan bool),
		Logger:         logger.New("standin", id),
	}
}

// matches is like cloud.Server.Matches()
func (s *standin) matches(os string, script []byte, configFiles string, flavor *cloud.Flavor, sharedDisk bool) bool {
	return s.os == os && bytes.Equal(s.script, script) && s.configFiles == configFiles && (flavor == nil || flavor.ID == s.flavor.ID) && s.sharedDisk == sharedDisk
}

// allocate is like cloud.Server.Allocate()
func (s *standin) allocate(req *Requirements) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.usedCores += req.Cores
	s.usedRAM += req.RAM
	s.usedDisk += req.Disk
	s.Debug("allocate", "cores", req.Cores, "RAM", req.RAM, "disk", req.Disk, "usedCores", s.usedCores, "usedRAM", s.usedRAM, "usedDisk", s.usedDisk)
}

// hasSpaceFor is like cloud.Server.HasSpaceFor()
func (s *standin) hasSpaceFor(req *Requirements) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	cores := req.Cores
	if cores == 0 {
		cores = 1
	}
	if (s.flavor.Cores-s.usedCores < cores) || (s.flavor.RAM-s.usedRAM < req.RAM) || (s.disk-s.usedDisk < req.Disk) {
		return 0
	}
	canDo := (s.flavor.Cores - s.usedCores) / cores
	if canDo > 1 {
		var n int
		if req.RAM > 0 {
			n = (s.flavor.RAM - s.usedRAM) / req.RAM
			if n < canDo {
				canDo = n
			}
		}
		if req.Disk > 0 {
			n = (s.disk - s.usedDisk) / req.Disk
			if n < canDo {
				canDo = n
			}
		}
	}
	return canDo
}

// willBeUsed tags this standin to note that we're no longer waiting to spawn,
// and we're about to spawn a real server.
func (s *standin) willBeUsed() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.waitingToSpawn = false
}

// isExtraneous checks if all prior allocate()ions on this standin can fit on
// the given server. If they can, this standin will get failed() and we return
// true.
func (s *standin) isExtraneous(server *cloud.Server) bool {
	s.mutex.RLock()
	var failed bool
	if s.waitingToSpawn {
		if server.OS == s.os && server.HasSpaceFor(s.usedCores, s.usedRAM, s.usedDisk) > 0 {
			s.mutex.RUnlock()
			failed = s.failed(standinNotNeeded)
			s.Debug("isExtraneous", "failed", failed)
		} else {
			s.mutex.RUnlock()
		}
	} else {
		s.mutex.RUnlock()
	}
	return failed
}

// failed is what you call if the server that this is a standin for failed to
// start up; anything that is waiting on waitForServer() will then receive nil.
// Returns true if it hadn't already been failed.
func (s *standin) failed(msg string) bool {
	s.mutex.Lock()
	alreadyFailed := s.alreadyFailed
	waitingToSpawn := s.waitingToSpawn
	s.failReason = msg
	if s.nowWaiting > 0 {
		s.mutex.Unlock()
		s.endWait <- nil
	} else {
		s.mutex.Unlock()
	}
	if alreadyFailed || !waitingToSpawn {
		s.Debug("standin already failed or not waiting to spawn", "msg", msg)
		return false
	}
	s.mutex.Lock()
	s.alreadyFailed = true
	s.mutex.Unlock()
	s.Debug(msg)
	return true
}

// worked is what you call once the server that this is a standin for has
// actually started up successfully. Anything that is waiting on waitForServer()
// will then receive the server you supply here. The server is allocated all
// the resources that were allocated to this standin.
func (s *standin) worked(server *cloud.Server) {
	s.mutex.RLock()
	server.Allocate(s.usedCores, s.usedRAM, s.usedDisk)
	s.Debug("standin worked", "server", server.ID, "cores", s.usedCores, "ram", s.usedRAM, "disk", s.usedDisk)
	if s.nowWaiting > 0 {
		s.mutex.RUnlock()
		s.endWait <- server
	} else {
		s.mutex.RUnlock()
	}
}

// waitForServer waits until another goroutine calls failed() or worked(). You
// would use this after checking hasSpaceFor() and doing allocate().
func (s *standin) waitForServer() (*cloud.Server, error) {
	// *** is this broken if called multiple times?...
	s.mutex.Lock()
	s.nowWaiting++
	s.Debug("waitForServer", "waiting", s.nowWaiting)
	s.mutex.Unlock()
	done := make(chan *cloud.Server)
	go func() {
		server := <-s.endWait
		done <- server
		s.mutex.Lock()
		s.nowWaiting--
		nowWaiting := s.nowWaiting
		s.mutex.Unlock()
		s.Debug("waitForServer endWait", "waiting", nowWaiting)

		// multiple goroutines may have called waitForServer(), so we
		// will repeat for the next one if so
		if nowWaiting > 0 {
			s.endWait <- server
		}
	}()
	server := <-done
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if s.failReason != "" {
		return server, fmt.Errorf(s.failReason)
	}
	return server, nil
}

// initialize sets up an openstack scheduler.
func (s *opst) initialize(config interface{}, logger log15.Logger) error {
	s.config = config.(*ConfigOpenStack)
	if s.config.OSRAM == 0 {
		s.config.OSRAM = 2048
	}
	if s.config.OSDisk == 0 {
		s.config.OSDisk = 1
	}

	s.Logger = logger.New("scheduler", "openstack")

	// create a cloud provider for openstack, that we'll use to interact with
	// openstack
	provider, err := cloud.New("openstack", s.config.ResourceName, s.config.SavePath, logger)
	if err != nil {
		return err
	}
	s.provider = provider

	err = provider.Deploy(&cloud.DeployConfig{
		RequiredPorts:  s.config.ServerPorts,
		GatewayIP:      s.config.GatewayIP,
		CIDR:           s.config.CIDR,
		DNSNameServers: s.config.DNSNameServers,
	})
	if err != nil {
		return err
	}

	// to debug spawned servers that don't work correctly:
	// keyFile := filepath.Join("/tmp", "key")
	// ioutil.WriteFile(keyFile, []byte(provider.PrivateKey()), 0600)

	// query our quota maximums for cpu and memory and total number of
	// instances; 0 will mean unlimited
	quota, err := provider.GetQuota()
	if err != nil {
		return err
	}
	if quota.MaxCores == 0 {
		s.quotaMaxCores = unquotadVal
	} else {
		s.quotaMaxCores = quota.MaxCores
	}
	if quota.MaxRAM == 0 {
		s.quotaMaxRAM = unquotadVal
	} else {
		s.quotaMaxRAM = quota.MaxRAM
	}
	if quota.MaxVolume == 0 {
		s.quotaMaxVolume = unquotadVal
	} else {
		s.quotaMaxVolume = quota.MaxVolume
	}
	if quota.MaxInstances == 0 {
		s.quotaMaxInstances = unquotadVal
	} else {
		s.quotaMaxInstances = quota.MaxInstances
	}
	if s.config.MaxInstances > -1 && s.config.MaxInstances < s.quotaMaxInstances {
		s.quotaMaxInstances = s.config.MaxInstances
		if provider.InCloud() {
			s.quotaMaxInstances++
		}
	}

	// initialize our job queue and other trackers
	s.queue = queue.New(localPlace)
	s.running = make(map[string]int)

	// initialise our servers with details of ourself
	s.servers = make(map[string]*cloud.Server)
	localhost, err := provider.LocalhostServer(s.config.OSPrefix, s.config.PostCreationScript, s.config.ConfigFiles, s.config.CIDR)
	if err != nil {
		return err
	}
	s.servers["localhost"] = localhost

	// set our functions for use in schedule() and processQueue()
	s.reqCheckFunc = s.reqCheck
	s.maxMemFunc = s.maxMem
	s.maxCPUFunc = s.maxCPU
	s.canCountFunc = s.canCount
	s.runCmdFunc = s.runCmd
	s.cancelRunCmdFunc = s.cancelRun
	s.stateUpdateFunc = s.stateUpdate
	s.stateUpdateFreq = s.config.StateUpdateFrequency
	if s.stateUpdateFreq == 0 {
		s.stateUpdateFreq = 1 * time.Minute
	}

	// pass through our shell config and logger to our local embed
	s.local.config = &ConfigLocal{Shell: s.config.Shell}
	s.local.Logger = s.Logger

	s.standins = make(map[string]*standin)
	s.cmdToStandins = make(map[string]map[string]bool)
	s.standinToCmd = make(map[string]map[string]bool)

	return err
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met,
// based on our quota and the available server flavours. Also based on the
// specific flavor the user has specified, if any.
func (s *opst) reqCheck(req *Requirements) error {
	reqForSpawn := s.reqForSpawn(req)

	// check if possible vs quota
	if reqForSpawn.RAM > s.quotaMaxRAM || reqForSpawn.Cores > s.quotaMaxCores || reqForSpawn.Disk > s.quotaMaxVolume {
		s.Warn("Requested resources are greater than max quota", "quotaCores", s.quotaMaxCores, "requiredCores", reqForSpawn.Cores, "quotaRAM", s.quotaMaxRAM, "requiredRAM", reqForSpawn.RAM, "quotaDisk", s.quotaMaxVolume, "requiredDisk", reqForSpawn.Disk)
		s.notifyMessage(fmt.Sprintf("OpenStack: not enough quota for the job needing %d cores, %d RAM and %d Disk", reqForSpawn.Cores, reqForSpawn.RAM, reqForSpawn.Disk))
		return Error{"openstack", "schedule", ErrImpossible}
	}

	if name, defined := req.Other["cloud_flavor"]; defined {
		requestedFlavor, err := s.getFlavor(name)
		if err != nil {
			return err
		}

		// check that the user hasn't requested a flavor that isn't actually big
		// enough to run their job
		if requestedFlavor.Cores < reqForSpawn.Cores || requestedFlavor.RAM < reqForSpawn.RAM {
			s.Warn("Requested flavor is too small for the job", "flavor", requestedFlavor.Name, "flavorCores", requestedFlavor.Cores, "requiredCores", reqForSpawn.Cores, "flavorRAM", requestedFlavor.RAM, "requiredRAM", reqForSpawn.RAM)
			s.notifyMessage(fmt.Sprintf("OpenStack: requested flavor %s is too small for the job needing %d cores and %d RAM", requestedFlavor.Name, reqForSpawn.Cores, reqForSpawn.RAM))
			return Error{"openstack", "schedule", ErrImpossible}
		}
	} else {
		// check if possible vs flavors
		_, err := s.determineFlavor(req)
		return err
	}
	return nil
}

// maxMem returns the maximum memory available in quota.
func (s *opst) maxMem() int {
	return s.quotaMaxRAM
}

// maxCPU returns the maximum number of CPU cores available quota.
func (s *opst) maxCPU() int {
	return s.quotaMaxCores
}

// determineFlavor picks a server flavor, preferring the smallest (cheapest)
// amongst those that are capable of running it.
func (s *opst) determineFlavor(req *Requirements) (*cloud.Flavor, error) {
	flavor, err := s.provider.CheapestServerFlavor(req.Cores, req.RAM, s.config.FlavorRegex)
	if err != nil {
		if perr, ok := err.(cloud.Error); ok && perr.Err == cloud.ErrNoFlavor {
			err = Error{"openstack", "determineFlavor", ErrImpossible}
		}
	}
	return flavor, err
}

// getFlavor returns a flavor with the given name or id. Returns an error
// if no matching flavor exists.
func (s *opst) getFlavor(name string) (*cloud.Flavor, error) {
	flavor, err := s.provider.GetServerFlavor(name)
	if err != nil {
		if perr, ok := err.(cloud.Error); ok && perr.Err == cloud.ErrNoFlavor {
			err = Error{"openstack", "getFlavorByName", ErrBadFlavor}
		}
	}
	return flavor, err
}

// serverReqs checks the given req's Other details to see if a particular kind
// of server has been requested. If not specified, the returned os defaults to
// the configured OSPrefix, script defaults to PostCreationScript, config files
// defaults to ConfigFiles and flavor will be nil.
func (s *opst) serverReqs(req *Requirements) (osPrefix string, osScript []byte, osConfigFiles string, flavor *cloud.Flavor, sharedDisk bool, err error) {
	if val, defined := req.Other["cloud_os"]; defined {
		osPrefix = val
	} else {
		osPrefix = s.config.OSPrefix
	}

	if val, defined := req.Other["cloud_script"]; defined {
		osScript = []byte(val)
	} else {
		osScript = s.config.PostCreationScript
	}

	if val, defined := req.Other["cloud_config_files"]; defined {
		if s.config.ConfigFiles != "" {
			osConfigFiles = s.config.ConfigFiles + "," + val
		} else {
			osConfigFiles = val
		}
	} else {
		osConfigFiles = s.config.ConfigFiles
	}

	if name, defined := req.Other["cloud_flavor"]; defined {
		flavor, err = s.getFlavor(name)
		if err != nil {
			return osPrefix, osScript, osConfigFiles, flavor, sharedDisk, err
		}
	}

	if val, defined := req.Other["cloud_shared"]; defined && val == "true" {
		sharedDisk = true

		// create a shared disk on our "head" node (if not already done)
		err = s.servers["localhost"].CreateSharedDisk()
	}

	return osPrefix, osScript, osConfigFiles, flavor, sharedDisk, err
}

// canCount tells you how many jobs with the given RAM and core requirements it
// is possible to run, given remaining resources.
func (s *opst) canCount(req *Requirements) int {
	s.resourceMutex.RLock()
	defer s.resourceMutex.RUnlock()

	requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk, err := s.serverReqs(req)
	if err != nil {
		s.Warn("Failed to determine server requirements", "err", err)
		return 0
	}

	reqForSpawn := s.reqForSpawn(req)

	// we don't do any actual checking of current resources on the machines, but
	// instead rely on our simple tracking based on how many cores and RAM
	// prior cmds were /supposed/ to use. This could be bad for misbehaving cmds
	// that use too much memory, but we will end up killing cmds that do this,
	// so it shouldn't be too much of an issue.

	// first we see how many of these commands will run on existing servers ***
	// both here and for the similar bit in runCmd, while looping over even
	// thousands of servers shouldn't be a performance issue, perhaps we could
	// do something a bit better, eg bin packing:
	// http://codeincomplete.com/posts/bin-packing/ (implemented in go:
	// https://github.com/azul3d/engine/blob/master/binpack/binpack.go)
	// "Analytical and empirical results suggest that ‘first fit decreasing’ is
	// the best heuristic. Sort the objects in decreasing order of size, so that
	// the biggest object is first and the smallest last. Insert each object one
	// by one in to the first bin that has room for it.”
	var canCount int
	for _, server := range s.servers {
		if !server.IsBad() && server.Matches(requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk) {
			space := server.HasSpaceFor(req.Cores, req.RAM, req.Disk)
			canCount += space
		}
	}

	// if a specific flavor was not requested, get the smallest server type that
	// can run our job, so we can subsequently calculate how many we could spawn
	// before exceeding our quota
	flavor := requestedFlavor
	if flavor == nil {
		flavor, err = s.determineFlavor(reqForSpawn)
		if err != nil {
			s.Warn("Failed to determine a server flavor", "err", err)
			return canCount
		}
	}

	quota, err := s.provider.GetQuota() // this includes resources used by currently spawning servers
	if err != nil {
		s.Warn("Failed to GetQuota", "err", err)
		return canCount
	}
	remainingInstances := unquotadVal
	if quota.MaxInstances > 0 {
		remainingInstances = quota.MaxInstances - quota.UsedInstances - s.reservedInstances
		if remainingInstances < 1 {
			s.Debug("lack of instance quota", "remaining", remainingInstances, "max", quota.MaxInstances, "used", quota.UsedInstances, "reserved", s.reservedInstances)
			s.notifyMessage("OpenStack: Not enough instance quota to create another server")
		}
	}
	if remainingInstances > 0 && s.quotaMaxInstances > -1 && s.quotaMaxInstances < quota.MaxInstances {
		// also check that the users configured max instances hasn't been breached
		used := len(s.servers) + s.reservedInstances
		remaining := s.quotaMaxInstances - used
		if remaining < remainingInstances {
			remainingInstances = remaining
		}
		if remainingInstances < 1 {
			s.Debug("instances over configured max", "remaining", remainingInstances, "configuredMax", s.quotaMaxInstances, "usedPersonally", len(s.servers), "reserved", s.reservedInstances)
		}
	}
	remainingRAM := unquotadVal
	if quota.MaxRAM > 0 {
		remainingRAM = quota.MaxRAM - quota.UsedRAM - s.reservedRAM
		if remainingRAM < flavor.RAM {
			s.Debug("lack of ram quota", "remaining", remainingRAM, "max", quota.MaxRAM, "used", quota.UsedRAM, "reserved", s.reservedRAM)
			s.notifyMessage(fmt.Sprintf("OpenStack: Not enough RAM quota to create another server (need %d, have %d)", flavor.RAM, remainingRAM))
		}
	}
	remainingCores := unquotadVal
	if quota.MaxCores > 0 {
		remainingCores = quota.MaxCores - quota.UsedCores - s.reservedCores
		if remainingCores < flavor.Cores {
			s.Debug("lack of cores quota", "remaining", remainingCores, "max", quota.MaxCores, "used", quota.UsedCores, "reserved", s.reservedCores)
			s.notifyMessage(fmt.Sprintf("OpenStack: Not enough cores quota to create another server (need %d, have %d)", flavor.Cores, remainingCores))
		}
	}
	remainingVolume := unquotadVal
	checkVolume := req.Disk > flavor.Disk // we'll only use up volume if we need more than the flavor offers
	if quota.MaxVolume > 0 && checkVolume {
		remainingVolume = quota.MaxVolume - quota.UsedVolume - s.reservedVolume
		if remainingVolume < req.Disk {
			s.Debug("lack of volume quota", "remaining", remainingVolume, "max", quota.MaxVolume, "used", quota.UsedVolume, "reserved", s.reservedVolume)
			s.notifyMessage(fmt.Sprintf("OpenStack: Not enough volume quota to create another server (need %d, have %d)", flavor.Disk, remainingVolume))
		}
	}
	if remainingInstances < 1 || remainingRAM < flavor.RAM || remainingCores < flavor.Cores || remainingVolume < req.Disk {
		return canCount
	}

	spawnable := remainingInstances
	if spawnable > 1 {
		n := remainingRAM / flavor.RAM // dividing ints == floor
		if n < spawnable {
			spawnable = n
		}
		n = remainingCores / flavor.Cores
		if n < spawnable {
			spawnable = n
		}
		if checkVolume {
			n = remainingVolume / req.Disk
			if n < spawnable {
				spawnable = n
			}
		}
	}

	// finally, calculate how many reqs we can get running on that many servers
	perServer := flavor.Cores / req.Cores
	if perServer > 1 {
		var n int
		if req.RAM > 0 {
			n = flavor.RAM / req.RAM
			if n < perServer {
				perServer = n
			}
		}
		if req.Disk > 0 {
			if checkVolume {
				// we'll be creating volumes to exactly match required disk
				// space
				n = 1
			} else {
				n = flavor.Disk / req.Disk
			}
			if n < perServer {
				perServer = n
			}
		}
	}
	canCount += spawnable * perServer
	return canCount
}

// reqForSpawn checks the input Requirements and if the configured OSRAM (or
// overriding that, the Requirements.Other["cloud_os_ram"]) is higher that the
// Requirements.RAM, or Requirements.Disk is not set and OSDisk is configured,
// returns a new Requirements with the higher RAM/ configured Disk value.
// Otherwise returns the input.
func (s *opst) reqForSpawn(req *Requirements) *Requirements {
	reqForSpawn := req

	var osRAM int
	if val, defined := req.Other["cloud_os_ram"]; defined {
		i, err := strconv.Atoi(val)
		if err == nil {
			osRAM = i
		} else {
			osRAM = s.config.OSRAM
		}
	} else {
		osRAM = s.config.OSRAM
	}

	if req.RAM < osRAM {
		reqForSpawn = &Requirements{
			RAM:   osRAM,
			Time:  req.Time,
			Cores: req.Cores,
			Disk:  req.Disk,
			Other: req.Other,
		}
	}

	disk := req.Disk
	if disk == 0 {
		disk = s.config.OSDisk
	}
	if req.Disk < disk {
		reqForSpawn = &Requirements{
			RAM:   reqForSpawn.RAM,
			Time:  reqForSpawn.Time,
			Cores: reqForSpawn.Cores,
			Disk:  disk,
			Other: reqForSpawn.Other,
		}
	}

	return reqForSpawn
}

// runCmd runs the command on next available server, or creates a new server if
// none are available. NB: we only return an error if we can't start the cmd,
// not if the command fails (schedule() only guarantees that the cmds are run
// count times, not that they are /successful/ that many times). New servers are
// created sequentially to avoid overloading OpenStack's sub-systems.
func (s *opst) runCmd(cmd string, req *Requirements, reservedCh chan bool) error {
	// since we can have many simultaneous calls to this method running at once,
	// we make a new logger with a unique "call" context key to keep track of
	// which runCmd call is doing what
	logger := s.Logger.New("call", logext.RandId(8))

	requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk, err := s.serverReqs(req)
	if err != nil {
		return err
	}
	if requestedFlavor != nil {
		logger.Debug("using requested flavor", "flavor", requestedFlavor.Name)
	}

	s.mutex.Lock()
	if s.cleaned {
		s.mutex.Unlock()
		reservedCh <- false
		return nil
	}

	// *** we need a better way for our test script to prove the bugs that rely
	// on debugEffect, that doesn't affect non-testing code. Probably have to
	// mock OpenStack instead at some point...
	var thisDebugCount int
	if debugEffect != "" {
		debugCounter++
		thisDebugCount = debugCounter
	}

	logger.Debug("runCmd", "servers", len(s.servers), "standins", len(s.standins))

	// look through space on existing servers to see if we can run cmd on one
	// of them
	var server *cloud.Server
	for sid, thisServer := range s.servers {
		if !thisServer.IsBad() && thisServer.Matches(requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk) && thisServer.HasSpaceFor(req.Cores, req.RAM, req.Disk) > 0 {
			server = thisServer
			server.Allocate(req.Cores, req.RAM, req.Disk)
			logger = logger.New("server", sid)
			logger.Debug("using existing server")
			break
		}
	}

	// else see if there will be space on a soon-to-be-spawned server
	if server == nil {
		for _, standinServer := range s.standins {
			if standinServer.matches(requestedOS, requestedScript, requestedConfigFiles, requestedFlavor, needsSharedDisk) && standinServer.hasSpaceFor(req) > 0 {
				s.recordStandin(standinServer, cmd)
				standinServer.allocate(req)
				s.mutex.Unlock()
				logger = logger.New("standin", standinServer.id)
				logger.Debug("using existing standin")
				var errw error
				server, errw = standinServer.waitForServer()
				if errw != nil || server == nil {
					logger.Debug("giving up on standin", "err", errw)
					reservedCh <- false
					return errw
				}
				s.mutex.Lock()
				logger = logger.New("server", server.ID)
				logger.Debug("using server from standin")
				break
			}
		}
	}

	// else spawn the smallest server that can run this cmd, recording our new
	// quota usage.
	if server == nil {
		// *** sometimes, when we're configured to not spawn any servers, we can
		// still manage to get here without a server due to timing issues? Guard
		// against proceeding if we'd spawn more servers than configured
		if s.quotaMaxInstances > -1 {
			numServers := len(s.servers) + len(s.standins)
			if numServers >= s.quotaMaxInstances {
				s.mutex.Unlock()
				logger.Debug("over quota", "servers", numServers, "max", s.quotaMaxInstances)
				reservedCh <- false
				return errors.New("over quota")
			}
		}

		flavor := requestedFlavor
		if flavor == nil {
			var errd error
			flavor, errd = s.determineFlavor(s.reqForSpawn(req))
			if errd != nil {
				s.mutex.Unlock()
				s.notifyMessage(fmt.Sprintf("OpenStack: Was unable to determine a server flavor to use for requirements %s: %s", req.Stringify(), errd))
				reservedCh <- false
				return errd
			}
		}
		volumeAffected := req.Disk > flavor.Disk

		// because spawning can take a while, we record that we're going to use
		// up some of our quota and unlock so other things can proceed
		s.resourceMutex.Lock()
		s.reservedInstances++
		s.reservedCores += flavor.Cores
		s.reservedRAM += flavor.RAM
		if volumeAffected {
			s.reservedVolume += req.Disk
		}
		reservedCh <- true
		s.resourceMutex.Unlock()

		u, _ := uuid.NewV4()
		standinID := u.String()
		standinServer := newStandin(standinID, flavor, req.Disk, requestedOS, requestedScript, requestedConfigFiles, needsSharedDisk, s.Logger)
		standinServer.allocate(req)
		s.recordStandin(standinServer, cmd)
		logger = logger.New("standin", standinID)
		logger.Debug("using new standin")

		// now spawn, but don't overload the system by trying to spawn too many
		// at once; wait until we are no longer in the middle of spawning
		// another
		if s.spawningNow || s.waitingToSpawn > 0 {
			s.waitingToSpawn++
			s.mutex.Unlock()
			done := make(chan error)
			go func() {
				for {
					select {
					case <-standinServer.readyToSpawn:
						done <- nil
						return
					case <-standinServer.noLongerNeeded:
						done <- errors.New(standinNotNeeded)
						return
					}
				}
			}()
			err = <-done
			if err != nil {
				s.resourceMutex.Lock()
				s.reservedInstances--
				s.reservedCores -= flavor.Cores
				s.reservedRAM -= flavor.RAM
				s.resourceMutex.Unlock()
				return err
			}
			s.mutex.Lock()
			logger.Debug("using the standin after waiting")
		} else {
			s.spawningNow = true
			standinServer.willBeUsed()
			logger.Debug("using the standin straightaway")
		}

		var osUser string
		if val, defined := req.Other["cloud_user"]; defined {
			osUser = val
		} else {
			osUser = s.config.OSUser
		}

		logger.Debug("will spawn")
		if debugEffect == "slowSecondSpawn" && thisDebugCount == 3 {
			s.mutex.Unlock()
			<-time.After(10 * time.Second)
			s.mutex.Lock()
		}

		// unlock before spawning, since we don't want to block here waiting for
		// that to complete
		s.mutex.Unlock()

		// immediately after the spawn request goes through (and so presumably is
		// using up quota), but before the new server powers up, drop our
		// reserved values down or we'll end up double-counting resource usage
		// in canCount(), since that takes in to account resources used by an
		// in-progress spawn.
		usingQuotaCB := func() {
			s.resourceMutex.Lock()
			s.reservedInstances--
			s.reservedCores -= flavor.Cores
			s.reservedRAM -= flavor.RAM
			if volumeAffected {
				s.reservedVolume -= req.Disk
			}
			s.resourceMutex.Unlock()
		}

		// spawn
		server, err = s.provider.Spawn(requestedOS, osUser, flavor.ID, req.Disk, s.config.ServerKeepTime, false, usingQuotaCB)
		serverID := "failed"
		if server != nil {
			serverID = server.ID
		}
		logger = logger.New("server", serverID)
		logger.Debug("spawned")

		// spawn completed; if we have standins that are waiting to spawn, tell
		// one of them to go ahead
		s.mutex.Lock()
		s.spawningNow = false
		if s.waitingToSpawn > 0 {
			for _, otherStandinServer := range s.standins {
				//*** we're not locking otherStandinServer to check
				//    waitingToSpawn... is this going to be a problem?
				if otherStandinServer.waitingToSpawn {
					s.waitingToSpawn--
					s.spawningNow = true
					otherStandinServer.willBeUsed()
					otherStandinServer.readyToSpawn <- true
					break
				}
			}
		}
		s.eraseStandin(standinID)

		// unlock again prior to waiting until the server is ready and trying to
		// check and upload our exe, since that could take quite a long time
		s.mutex.Unlock()
		logger.Debug("waiting for server ready")
		if err == nil {
			// wait until boot is finished, ssh is ready and osScript has
			// completed
			err = server.WaitUntilReady(requestedConfigFiles, requestedScript)

			if err == nil && needsSharedDisk {
				err = server.MountSharedDisk(s.servers["localhost"].IP)
			}

			if err == nil {
				// check that the exe of the cmd we're supposed to run exists on the
				// new server, and if not, copy it over *** this is just a hack to
				// get wr working, need to think of a better way of doing this...
				exe := strings.Split(cmd, " ")[0]
				var exePath, stdout string
				if exePath, err = exec.LookPath(exe); err == nil {
					if stdout, _, err = server.RunCmd("file "+exePath, false); stdout != "" {
						if strings.Contains(stdout, "No such file") {
							// *** NB this will fail if exePath is in a dir we can't
							// create on the remote server, eg. if it is in our home
							// dir, but the remote server has a different user, or
							// presumably if it is somewhere requiring root
							// permission
							err = server.UploadFile(exePath, exePath)
							if err == nil {
								_, _, err = server.RunCmd("chmod u+x "+exePath, false)
							} else {
								err = fmt.Errorf("Could not upload exe [%s]: %s (try putting the exe in /tmp?)", exePath, err)
							}
						} else if err != nil {
							err = fmt.Errorf("Could not check exe with [file %s]: %s [%s]", exePath, stdout, err)
						}
					} else {
						// checking for exePath with the file command failed for
						// some reason, and without any stdout... but let's just
						// try the upload anyway, assuming the exe isn't there
						err = server.UploadFile(exePath, exePath)
						if err == nil {
							_, _, err = server.RunCmd("chmod u+x "+exePath, false)
						} else {
							err = fmt.Errorf("Could not upload exe [%s]: %s (try putting the exe in /tmp?)", exePath, err)
						}
					}
				} else {
					err = fmt.Errorf("Could not look for exe [%s]: %s", exePath, err)
				}
			}
		}

		s.mutex.Lock()

		if debugEffect == "failFirstSpawn" && thisDebugCount == 1 {
			err = errors.New("forced fail")
		}

		// handle Spawn() or upload-of-exe errors now, by destroying the server
		// and noting we failed
		if err != nil {
			logger.Warn("server failed ready", "err", err)
			errd := server.Destroy()
			if errd != nil {
				logger.Debug("server also failed to destroy", "err", errd)
			}
			standinServer.failed(fmt.Sprintf("New server failed to spawn correctly: %s", err))
			s.mutex.Unlock()
			s.notifyMessage(fmt.Sprintf("OpenStack: Failed to create a usable server: %s", err))
			return err
		}

		logger.Debug("server ready")

		s.servers[server.ID] = server
		standinServer.worked(server) // calls server.Allocate() for everything allocated to the standin
	} else {
		reservedCh <- true
	}

	s.mutex.Unlock()

	// now we have a server, ssh over and run the cmd on it
	if server.Name == "localhost" {
		logger.Debug("running command locally", "cmd", cmd)
		reserved := make(chan bool)
		go func() {
			<-reserved
		}()
		err = s.local.runCmd(cmd, req, reserved)
	} else {
		logger.Debug("running command remotely", "cmd", cmd)
		_, _, err = server.RunCmd(cmd, false)

		// if we got an error running the command, we won't use this server
		// again
		if err != nil {
			if !server.Destroyed() {
				// tell the user about why we're not using this server, but
				// don't just Destroy it: let them investigate the server
				// manually if they wish, and let them Destroy when they wish.
				server.GoneBad(err.Error())
				s.notifyBadServer(server)
			}
			logger.Warn("server went bad", "err", err)
			return err
		}
	}
	logger.Debug("ran command", "cmd", cmd)

	// having run a command, this server is now available for another; signal a
	// runCmd call that is waiting its turn to spawn a new server to give up
	// waiting and potentially get scheduled on us instead
	s.mutex.Lock()
	server.Release(req.Cores, req.RAM, req.Disk)
	if s.waitingToSpawn > 0 {
		for _, otherStandinServer := range s.standins {
			if otherStandinServer.isExtraneous(server) {
				s.waitingToSpawn--
				s.eraseStandin(otherStandinServer.id)
				otherStandinServer.noLongerNeeded <- true
				break
			}
		}
	}
	s.mutex.Unlock()

	return err
}

// cancelRun fails standins for the given cmd. Only call when you have the lock!
func (s *opst) cancelRun(cmd string, desiredCount int) {
	if lookup, existed := s.cmdToStandins[cmd]; existed {
		numStandins := len(lookup)
		cancelCount := numStandins - desiredCount
		if cancelCount > 0 {
			cancelled := 0
			for standinID := range lookup {
				if standinServer, existed := s.standins[standinID]; existed {
					if standinServer.failed(standinNotNeeded) {
						cancelled++
						s.waitingToSpawn--
						s.eraseStandin(standinServer.id)
						standinServer.noLongerNeeded <- true

						if cancelled >= cancelCount {
							break
						}
					}
				} else {
					// (this should be impossible)
					delete(lookup, standinID)
				}
			}
		}
	}
}

// stateUpdate checks all our servers are really alive.
func (s *opst) stateUpdate() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var servers []*cloud.Server
	for _, server := range s.servers {
		if server.ID != "" {
			if server.Destroyed() {
				delete(s.servers, server.ID)
				continue
			}
			servers = append(servers, server)
		}
	}

	if s.updatingState || s.cleaned {
		return
	}
	s.updatingState = true

	// stateUpdate must return quickly, but checking on the servers with the
	// Alive() call can take too long, so we do the rest in a goroutine
	go func() {
		defer internal.LogPanic(s.Logger, "stateUpdate", true)

		for _, server := range servers {
			if server.Destroyed() {
				continue
			}

			alive := server.Alive(true)
			if server.IsBad() {
				// check if the server is fine now
				if alive && server.PermanentProblem() == "" {
					worked := server.NotBad()
					if worked {
						s.notifyBadServer(server)
						s.Debug("server became good", "server", server.ID)
					}
				}
			} else if !alive {
				server.GoneBad()
				s.notifyBadServer(server)
				s.Debug("server went bad", "server", server.ID)
			}
		}

		s.mutex.Lock()
		defer s.mutex.Unlock()
		s.updatingState = false
	}()
}

// recordStandin stores some lookups for the given standin. Only call when you
// have the lock!
func (s *opst) recordStandin(standinServer *standin, cmd string) {
	s.standins[standinServer.id] = standinServer

	if lookup, existed := s.cmdToStandins[cmd]; existed {
		lookup[standinServer.id] = true
	} else {
		s.cmdToStandins[cmd] = make(map[string]bool)
		s.cmdToStandins[cmd][standinServer.id] = true
	}

	if lookup, existed := s.standinToCmd[standinServer.id]; existed {
		lookup[cmd] = true
	} else {
		s.standinToCmd[standinServer.id] = make(map[string]bool)
		s.standinToCmd[standinServer.id][cmd] = true
	}
}

// eraseStandin deletes the various lookups for the given standin. Only call
// when you have the lock!
func (s *opst) eraseStandin(standinID string) {
	for cmd := range s.standinToCmd[standinID] {
		if lookup, existed := s.cmdToStandins[cmd]; existed {
			delete(lookup, standinID)
			if len(lookup) == 0 {
				delete(s.cmdToStandins, cmd)
			}
		}
	}
	delete(s.standinToCmd, standinID)
	delete(s.standins, standinID)
}

// hostToID does the necessary lookup to convert hostname to instance id.
func (s *opst) hostToID(host string) string {
	server := s.provider.GetServerByName(host)
	if server == nil {
		return ""
	}
	return server.ID
}

// setMessageCallBack sets the given callback.
func (s *opst) setMessageCallBack(cb MessageCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()
	s.msgCB = cb
}

// notifyMessage calls the message callback with the given message in a
// goroutine, if that callback has been set.
func (s *opst) notifyMessage(msg string) {
	s.cbmutex.RLock()
	defer s.cbmutex.RUnlock()
	if s.msgCB != nil {
		go s.msgCB(msg)
	}
}

// setBadServerCallBack sets the given callback.
func (s *opst) setBadServerCallBack(cb BadServerCallBack) {
	s.cbmutex.Lock()
	defer s.cbmutex.Unlock()
	s.badServerCB = cb
}

// notifyBadServer calls the bad server callback with the given server in a
// goroutine, if that callback has been set.
func (s *opst) notifyBadServer(server *cloud.Server) {
	s.cbmutex.RLock()
	defer s.cbmutex.RUnlock()
	if s.badServerCB != nil {
		go s.badServerCB(server)
	}
}

// cleanup destroys our internal queues and brings down our servers.
func (s *opst) cleanup() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// prevent any further scheduling and queue processing, and destroy the
	// queue
	s.cleaned = true
	err := s.queue.Destroy()
	if err != nil {
		s.Warn("cleanup queue destruction failed", "err", err)
	}

	// cancel all standins
	for _, standinServer := range s.standins {
		s.eraseStandin(standinServer.id)
		if standinServer.failed(standinNotNeeded) {
			standinServer.noLongerNeeded <- true
		}
	}
	s.waitingToSpawn = 0
	s.cmdToStandins = nil
	s.standinToCmd = nil

	// wait for any ongoing state update to complete
	for {
		if !s.updatingState {
			break
		}
		s.mutex.Unlock()
		<-time.After(10 * time.Millisecond)
		s.mutex.Lock()
	}

	// bring down all our servers
	for sid, server := range s.servers {
		if sid == "localhost" {
			continue
		}
		errd := server.Destroy()
		if errd != nil {
			s.Warn("cleanup server destruction failed", "server", server.ID, "err", errd)
		}
		delete(s.servers, sid)
	}

	// teardown any cloud resources created
	err = s.provider.TearDown()
	if err != nil {
		s.Warn("cleanup teardown failed", "err", err)
	}
}
