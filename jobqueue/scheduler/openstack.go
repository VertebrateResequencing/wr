// Copyright © 2016-2017 Genome Research Limited
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
	"log"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/ricochet2200/go-disk-usage/du"
	"github.com/satori/go.uuid"
)

const (
	gb               = uint64(1.07374182e9) // for byte to GB conversion
	unquotadVal      = 1000000              // a "large" number for use when we don't have quota
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
}

// ConfigOpenStack represents the configuration options required by the
// OpenStack scheduler. All are required with no usable defaults, unless
// otherwise noted.
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

	// Debug turns on debugging mode.
	Debug bool
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
	debugMode      bool
}

// newStandin returns a new standin server
func newStandin(id string, flavor *cloud.Flavor, disk int, osPrefix string, script []byte, debug bool) *standin {
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
		waitingToSpawn: true,
		endWait:        make(chan *cloud.Server),
		readyToSpawn:   make(chan bool),
		noLongerNeeded: make(chan bool),
		debugMode:      debug,
	}
}

// allocate is like cloud.Server.Allocate()
func (s *standin) allocate(req *Requirements) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.usedCores += req.Cores
	s.usedRAM += req.RAM
	s.usedDisk += req.Disk
	s.debug("standin %s allocated(%d, %d, %d), used now (%d, %d, %d)\n", s.id, req.Cores, req.RAM, req.Disk, s.usedCores, s.usedRAM, s.usedDisk)
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
	s.debug("standin %s willBeUsed, waitingToSpawn = false\n", s.id)
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
			s.debug("standin %s isExtraneous, failed = %v\n", s.id, failed)
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
		s.debug("standin %s failed(), will send nil to endWait...\n", s.id)
		s.endWait <- nil
		s.debug("standin %s failed(), nil was sent to endWait\n", s.id)
	} else {
		s.mutex.Unlock()
	}
	if alreadyFailed || !waitingToSpawn {
		s.debug("standin %s failed(), already failed or not waiting to spawn, returning false\n", s.id)
		return false
	}
	s.mutex.Lock()
	s.alreadyFailed = true
	s.mutex.Unlock()
	s.debug("standin %s failed(), returning true.\n", s.id)
	return true
}

// worked is what you call once the server that this is a standin for has
// actually started up successfully. Anything that is waiting on waitForServer()
// will then receive the server you supply here. The server is allocated all
// the resources that were allocated to this standin.
func (s *standin) worked(server *cloud.Server) {
	s.mutex.RLock()
	server.Allocate(s.usedCores, s.usedRAM, s.usedDisk)
	s.debug("standin %s worked(), allocated (%d, %d, %d) to server %s\n", s.id, s.usedCores, s.usedRAM, s.usedDisk, server.ID)
	if s.nowWaiting > 0 {
		s.mutex.RUnlock()
		s.debug("standin %s worked() was waiting, sending server %s to endWait\n", s.id, server.ID)
		s.endWait <- server
		s.debug("standin %s worked() was waiting, sent server %s to endWait\n", s.id, server.ID)
	} else {
		s.mutex.RUnlock()
		s.debug("standin %s worked() was not waiting\n", s.id)
	}
}

// waitForServer waits until another goroutine calls failed() or worked(). You
// would use this after checking hasSpaceFor() and doing allocate().
func (s *standin) waitForServer() (*cloud.Server, error) {
	// *** is this broken if called multiple times?...
	s.mutex.Lock()
	s.nowWaiting++
	s.debug("standin %s waitForServer(), nowWaiting = %d\n", s.id, s.nowWaiting)
	s.mutex.Unlock()
	done := make(chan *cloud.Server)
	go func() {
		server := <-s.endWait
		done <- server
		s.mutex.Lock()
		s.nowWaiting--
		nowWaiting := s.nowWaiting
		s.mutex.Unlock()
		s.debug("standin %s waitForServer(), received on endWait, nowWaiting = %d\n", s.id, s.nowWaiting)

		// multiple goroutines may have called waitForServer(), so we
		// will repeat for the next one if so
		if nowWaiting > 0 {
			s.debug("standin %s waitForServer(), resending on endWait\n", s.id)
			s.endWait <- server
			s.debug("standin %s waitForServer(), resent on endWait\n", s.id)
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
func (s *opst) initialize(config interface{}) error {
	s.config = config.(*ConfigOpenStack)
	if s.config.OSRAM == 0 {
		s.config.OSRAM = 2048
	}
	if s.config.OSDisk == 0 {
		s.config.OSDisk = 1
	}

	if s.config.Debug {
		s.debugMode = true
	}

	// create a cloud provider for openstack, that we'll use to interact with
	// openstack
	provider, err := cloud.New("openstack", s.config.ResourceName, s.config.SavePath)
	if err != nil {
		return err
	}
	provider.Debug = s.debugMode
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
	maxRAM, err := s.procMeminfoMBs()
	if err != nil {
		return err
	}
	usage := du.NewDiskUsage(".")
	diskSize := int(usage.Size() / gb)
	s.servers["localhost"] = &cloud.Server{
		IP:     "127.0.0.1",
		OS:     s.config.OSPrefix,
		Script: s.config.PostCreationScript,
		Flavor: cloud.Flavor{
			RAM:   maxRAM,
			Cores: runtime.NumCPU(),
			Disk:  diskSize,
		},
		Disk: diskSize,
	}

	// set our functions for use in schedule() and processQueue()
	s.reqCheckFunc = s.reqCheck
	s.canCountFunc = s.canCount
	s.runCmdFunc = s.runCmd
	s.cancelRunCmdFunc = s.cancelRun
	s.stateUpdateFunc = s.stateUpdate
	s.stateUpdateFreq = s.config.StateUpdateFrequency
	if s.stateUpdateFreq == 0 {
		s.stateUpdateFreq = 1 * time.Minute
	}

	// pass through our shell config to our local embed
	s.local.config = &ConfigLocal{Shell: s.config.Shell}

	s.standins = make(map[string]*standin)
	s.cmdToStandins = make(map[string]map[string]bool)
	s.standinToCmd = make(map[string]map[string]bool)

	return err
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met,
// based on our quota and the available server flavours.
func (s *opst) reqCheck(req *Requirements) error {
	// check if possible vs quota
	if req.RAM > s.quotaMaxRAM || req.Cores > s.quotaMaxCores || req.Disk > s.quotaMaxVolume {
		return Error{"openstack", "schedule", ErrImpossible}
	}

	// check if possible vs flavors
	_, err := s.determineFlavor(req)
	return err
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

// canCount tells you how many jobs with the given RAM and core requirements it
// is possible to run, given remaining resources.
func (s *opst) canCount(req *Requirements) int {
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
		if !server.IsBad() {
			space := server.HasSpaceFor(req.Cores, req.RAM, req.Disk)
			canCount += space
		}
	}

	// now we get the smallest server type that can run our job, and calculate
	// how many we could spawn before exceeding our quota
	reqForSpawn := s.reqForSpawn(req)
	flavor, err := s.determineFlavor(reqForSpawn)
	if err != nil {
		s.debug("determineFlavor failed: %s\n", err)
		return canCount
	}

	quota, err := s.provider.GetQuota() // this includes resources used by currently spawning servers
	if err != nil {
		s.debug("GetQuota failed: %s\n", err)
		return canCount
	}
	remainingInstances := unquotadVal
	if quota.MaxInstances > 0 {
		remainingInstances = quota.MaxInstances - quota.UsedInstances - s.reservedInstances
		if remainingInstances < 1 {
			s.debug("remainingInstances %d = quota.MaxInstances %d - quota.UsedInstances %d - reservedInstances %d", remainingInstances, quota.MaxInstances, quota.UsedInstances, s.reservedInstances)
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
			s.debug("remainingInstances %d = configuredMaxInstances %d - personallyUsedInstances %d - reservedInstances %d", remainingInstances, s.quotaMaxInstances, len(s.servers), s.reservedInstances)
		}
	}
	remainingRAM := unquotadVal
	if quota.MaxRAM > 0 {
		remainingRAM = quota.MaxRAM - quota.UsedRAM - s.reservedRAM
		if remainingRAM < flavor.RAM {
			s.debug("remainingRAM %d = quota.MaxRAM %d - quota.UsedRAM %d - reservedRAM %d", remainingRAM, quota.MaxRAM, quota.UsedRAM, s.reservedRAM)
			s.notifyMessage(fmt.Sprintf("OpenStack: Not enough RAM quota to create another server (need %d, have %d)", flavor.RAM, remainingRAM))
		}
	}
	remainingCores := unquotadVal
	if quota.MaxCores > 0 {
		remainingCores = quota.MaxCores - quota.UsedCores - s.reservedCores
		if remainingCores < flavor.Cores {
			s.debug("remainingCores %d = quota.MaxCores %d - quota.UsedCores %d - reservedCores %d", remainingCores, quota.MaxCores, quota.UsedCores, s.reservedCores)
			s.notifyMessage(fmt.Sprintf("OpenStack: Not enough cores quota to create another server (need %d, have %d)", flavor.Cores, remainingCores))
		}
	}
	remainingVolume := unquotadVal
	checkVolume := req.Disk > flavor.Disk // we'll only use up volume if we need more than the flavor offers
	if quota.MaxVolume > 0 && checkVolume {
		remainingVolume = quota.MaxVolume - quota.UsedVolume - s.reservedVolume
		if remainingVolume < req.Disk {
			s.debug("remainingVolume %d = quota.MaxVolume %d - quota.UsedVolume %d - reservedVolume %d", remainingVolume, quota.MaxVolume, quota.UsedVolume, s.reservedVolume)
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
	perServer := flavor.Cores / reqForSpawn.Cores
	if perServer > 1 {
		var n int
		if reqForSpawn.RAM > 0 {
			n = flavor.RAM / reqForSpawn.RAM
			if n < perServer {
				perServer = n
			}
		}
		if reqForSpawn.Disk > 0 {
			if checkVolume {
				// we'll be creating volumes to exactly match required disk
				// space
				n = 1
			} else {
				n = flavor.Disk / reqForSpawn.Disk
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

	disk := req.Disk
	if disk == 0 {
		disk = s.config.OSDisk
	}

	if req.RAM < osRAM {
		reqForSpawn = &Requirements{
			RAM:   osRAM,
			Time:  req.Time,
			Cores: req.Cores,
			Disk:  disk,
			Other: req.Other,
		}
	}
	return reqForSpawn
}

// runCmd runs the command on next available server, or creates a new server if
// none are available. NB: we only return an error if we can't start the cmd,
// not if the command fails (schedule() only guarantees that the cmds are run
// count times, not that they are /successful/ that many times). New servers are
// created sequentially to avoid overloading OpenStack's sub-systems.
func (s *opst) runCmd(cmd string, req *Requirements) error {
	// look through space on existing servers to see if we can run cmd on one
	// of them
	var osPrefix string
	if val, defined := req.Other["cloud_os"]; defined {
		osPrefix = val
	} else {
		osPrefix = s.config.OSPrefix
	}

	var osScript []byte
	if val, defined := req.Other["cloud_script"]; defined {
		osScript = []byte(val)
	} else {
		osScript = s.config.PostCreationScript
	}

	u, _ := uuid.NewV4()
	uniqueDebug := u.String()

	s.mutex.Lock()

	// *** we need a better way for our test script to prove the bugs that rely
	// on debugEffect, that doesn't affect non-testing code. Probably have to
	// mock OpenStack instead at some point...
	var thisDebugCount int
	if debugEffect != "" {
		debugCounter++
		thisDebugCount = debugCounter
	}

	if s.cleaned {
		s.mutex.Unlock()
		return nil
	}
	s.debug("a %s lock, %d servers, %d standins\n", uniqueDebug, len(s.servers), len(s.standins))
	var server *cloud.Server
	for sid, thisServer := range s.servers {
		if !thisServer.IsBad() && thisServer.OS == osPrefix && bytes.Equal(thisServer.Script, osScript) && thisServer.HasSpaceFor(req.Cores, req.RAM, req.Disk) > 0 {
			server = thisServer
			server.Allocate(req.Cores, req.RAM, req.Disk)
			s.debug("b %s using existing server %s\n", uniqueDebug, sid)
			break
		}
	}

	// else see if there will be space on a soon-to-be-spawned server
	if server == nil {
		for _, standinServer := range s.standins {
			if standinServer.os == osPrefix && bytes.Equal(standinServer.script, osScript) && standinServer.hasSpaceFor(req) > 0 {
				s.recordStandin(standinServer, cmd)
				standinServer.allocate(req)
				s.mutex.Unlock()
				s.debug("c %s will use standin %s, unlocked\n", uniqueDebug, standinServer.id)
				var err error
				server, err = standinServer.waitForServer()
				if err != nil || server == nil {
					s.debug("d %s giving up waiting on standin %s\n", uniqueDebug, standinServer.id)
					return err
				}
				s.mutex.Lock()
				s.debug("e %s got server %s from standin %s, locked\n", uniqueDebug, server.ID, standinServer.id)
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
				s.debug("f %s over quota, unlocked\n", uniqueDebug)
				return errors.New("over quota")
			}
		}

		flavor, err := s.determineFlavor(s.reqForSpawn(req))
		if err != nil {
			s.mutex.Unlock()
			s.notifyMessage(fmt.Sprintf("OpenStack: Was unable to determine a server flavor to use for requirements %s: %s", req.Stringify(), err))
			return err
		}
		volumeAffected := req.Disk > flavor.Disk

		// because spawning can take a while, we record that we're going to use
		// up some of our quota and unlock so other things can proceed
		s.reservedInstances++
		s.reservedCores += flavor.Cores
		s.reservedRAM += flavor.RAM
		if volumeAffected {
			s.reservedVolume += req.Disk
		}

		u, _ = uuid.NewV4()
		standinID := u.String()
		standinServer := newStandin(standinID, flavor, req.Disk, osPrefix, osScript, s.debugMode)
		standinServer.allocate(req)
		s.recordStandin(standinServer, cmd)
		s.debug("g %s made new standin %s\n", uniqueDebug, standinID)

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
						s.debug("h %s ready to spawn standin %s\n", uniqueDebug, standinID)
						done <- nil
						s.debug("j %s sent nil on done channel\n", uniqueDebug)
						return
					case <-standinServer.noLongerNeeded:
						s.debug("k %s standin %s no longer needed\n", uniqueDebug, standinID)
						done <- errors.New(standinNotNeeded)
						s.debug("m %s sent give up error on done channel\n", uniqueDebug)
						return
					}
				}
			}()
			err = <-done
			if err != nil {
				s.mutex.Lock()
				s.reservedInstances--
				s.reservedCores -= flavor.Cores
				s.reservedRAM -= flavor.RAM
				s.mutex.Unlock()
				return err
			}
			s.mutex.Lock()
		} else {
			s.spawningNow = true
			standinServer.willBeUsed()
			s.debug("n %s will use the standin straightaway, unlocked\n", uniqueDebug)
		}

		var osUser string
		if val, defined := req.Other["cloud_user"]; defined {
			osUser = val
		} else {
			osUser = s.config.OSUser
		}

		s.debug("o %s will spawn\n", uniqueDebug)
		if debugEffect == "slowSecondSpawn" && thisDebugCount == 3 {
			s.mutex.Unlock()
			<-time.After(10 * time.Second)
			s.mutex.Lock()
		}

		// just before actually spawning, drop our reserved values down or
		// we'll end up double-counting resource usage in canCount(), since that
		// takes in to account resources used by an in-progress spawn. It's ok
		// to do this a little early since that just means we underestimate our
		// resource usage and may try to spawn when we can't, instead of
		// overestimating and not trying to spawn when we could
		s.reservedInstances--
		s.reservedCores -= flavor.Cores
		s.reservedRAM -= flavor.RAM
		if volumeAffected {
			s.reservedVolume -= req.Disk
		}

		// unlock just after we issue the spawn request, since we don't want to
		// block here waiting for that to complete
		unlocked := make(chan bool)
		go func() {
			<-time.After(10 * time.Millisecond)
			s.mutex.Unlock()
			unlocked <- true
		}()

		// spawn
		server, err = s.provider.Spawn(osPrefix, osUser, flavor.ID, req.Disk, s.config.ServerKeepTime, false)
		s.debug("p %s spawned\n", uniqueDebug)

		<-unlocked // in case the spawn call takes less than 10ms

		// spawn completed; if we have standins that are waiting to spawn, tell
		// one of them to go ahead
		s.mutex.Lock()
		s.debug("q %s locked\n", uniqueDebug)
		s.spawningNow = false
		if s.waitingToSpawn > 0 {
			for _, otherStandinServer := range s.standins {
				//*** we're not locking otherStandinServer to check
				//    waitingToSpawn... is this going to be a problem?
				if otherStandinServer.waitingToSpawn {
					s.debug("r %s will send true to readyToSpawn on standin %s\n", uniqueDebug, otherStandinServer.id)
					s.waitingToSpawn--
					s.spawningNow = true
					otherStandinServer.willBeUsed()
					otherStandinServer.readyToSpawn <- true
					s.debug("s %s sent true to other standin\n", uniqueDebug)
					break
				}
			}
		}
		s.eraseStandin(standinID)

		// unlock again prior to waiting until the server is ready and trying to
		// check and upload our exe, since that could take quite a long time
		s.mutex.Unlock()
		s.debug("t %s unlocked prior to waiting for server to become ready\n", uniqueDebug)
		if err == nil {
			// wait until boot is finished, ssh is ready, and osScript has
			// completed
			err = server.WaitUntilReady(s.config.ConfigFiles, osScript)

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
		s.debug("u %s locked after waiting for server to become ready\n", uniqueDebug)

		if debugEffect == "failFirstSpawn" && thisDebugCount == 1 {
			err = errors.New("forced fail")
		}

		// handle Spawn() or upload-of-exe errors now, by destroying the server
		// and noting we failed
		if err != nil {
			server.Destroy()
			s.debug("v %s will fail standin due to err %s\n", uniqueDebug, err)
			standinServer.failed(fmt.Sprintf("New server failed to spawn correctly: %s", err))
			s.mutex.Unlock()
			s.debug("w %s unlocked after failing standin\n", uniqueDebug)
			s.notifyMessage(fmt.Sprintf("OpenStack: Failed to create a usable server: %s", err))
			return err
		}

		s.debug("x %s completed new server %s\n", uniqueDebug, server.ID)

		s.servers[server.ID] = server
		standinServer.worked(server) // calls server.Allocate() for everything allocated to the standin
		s.debug("y %s told standin it worked\n", uniqueDebug)
	}

	s.mutex.Unlock()

	// now we have a server, ssh over and run the cmd on it
	var err error
	if server.IP == "127.0.0.1" {
		s.debug("z %s unlocked, will runCmd(%s) locally\n", uniqueDebug, cmd)
		err = s.local.runCmd(cmd, req)
	} else {
		s.debug("z %s unlocked, server %s will runCmd(%s)\n", uniqueDebug, server.ID, cmd)
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
			s.debug("z2 %s server %s has gone bad, won't be used again: %s\n", uniqueDebug, server.ID, err)
			return err
		}
	}
	s.debug("z3 %s server %s ran the command\n", uniqueDebug, server.ID)

	// having run a command, this server is now available for another; signal a
	// runCmd call that is waiting its turn to spawn a new server to give up
	// waiting and potentially get scheduled on us instead
	s.mutex.Lock()
	s.debug("z4 %s locked to release server %s\n", uniqueDebug, server.ID)
	server.Release(req.Cores, req.RAM, req.Disk)
	if s.waitingToSpawn > 0 {
		for _, otherStandinServer := range s.standins {
			if otherStandinServer.isExtraneous(server) {
				s.debug("z5 %s other standin %s is extraneous, will send noLongerNeeded\n", uniqueDebug, otherStandinServer.id)
				s.waitingToSpawn--
				s.eraseStandin(otherStandinServer.id)
				otherStandinServer.noLongerNeeded <- true
				s.debug("z6 %s sent noLongerNeeded to otherstandin\n", uniqueDebug)
				break
			}
		}
	}
	s.mutex.Unlock()
	s.debug("z7 %s unlocked, returning\n", uniqueDebug)

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
		for _, server := range servers {
			if server.Destroyed() {
				continue
			}

			alive := server.Alive(true)
			if server.IsBad() {
				// check if the server is fine now
				if alive && server.PermanentProblem() == "" {
					server.NotBad()
					s.notifyBadServer(server)
					s.debug("existing server %s was bad, now working again\n", server.ID)
				}
			} else if !alive {
				server.GoneBad()
				s.notifyBadServer(server)
				s.debug("existing server %s is no longer alive, marked as bad\n", server.ID)
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
	s.queue.Destroy()

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
		server.Destroy()
		delete(s.servers, sid)
	}

	// teardown any cloud resources created
	s.provider.TearDown()
}

func (s *opst) debug(msg string, a ...interface{}) {
	if s.debugMode {
		log.Printf(msg, a...)
	}
}

func (s *standin) debug(msg string, a ...interface{}) {
	if s.debugMode {
		log.Printf(msg, a...)
	}
}
