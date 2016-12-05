// Copyright © 2016 Genome Research Limited
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
	"errors"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/ricochet2200/go-disk-usage/du"
	"github.com/satori/go.uuid"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const gb = uint64(1.07374182e9) // for byte to GB conversion
const unquotadVal = 1000000     // a "large" number for use when we don't have quota

// opst is our implementer of scheduleri. It takes much of its implementation
// from the local scheduler.
type opst struct {
	local
	config             *ConfigOpenStack
	provider           *cloud.Provider
	flavorRegex        string
	quotaMaxInstances  int
	quotaMaxCores      int
	quotaMaxRAM        int
	reservedInstances  int
	reservedCores      int
	reservedRAM        int
	servers            map[string]*cloud.Server
	standins           map[string]*standin
	waitingToSpawn     int
	spawningNow        int
	nextSpawnTime      time.Time
	stopWaitingToSpawn chan bool
}

// ConfigOpenStack represents the configuration options required by the
// OpenStack scheduler. All are required with no usable defaults, unless
// otherwise noted.
type ConfigOpenStack struct {
	// ResourceName is the resource name prefix used to name any resources (such
	// as keys, security groups and servers) that need to be created.
	ResourceName string

	// OSPrefix is the prefix or full name of the Operating System image you
	// wish spawned servers to run.
	OSPrefix string

	// OSUser is the login username of your chosen Operating System.
	OSUser string

	// OSRAM is the minimum RAM in MB needed to bring up a server instance that
	// runs your Operating System image. It defaults to 2048.
	OSRAM int

	// FlavorRegex is a regular expression that you can use to limit what
	// flavors of server will be created to run commands on. The default of an
	// empty string means there is no limit, and any available flavor can be
	// used. (The flavor chosen for a command will be the flavor with the least
	// specifications (RAM, CPUs, Disk) capable of running the command, that
	// also satisfies this regex.)
	FlavorRegex string

	// PostCreationScript is the []byte content of a script you want after a
	// server is Spawn()ed.
	PostCreationScript []byte

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

	// MaxInstances is the maximum number of instances we are allowed to spawn.
	// A 0 value (the default) means we will be limited by your quota, if any.
	MaxInstances int

	// Shell is the shell to use to run your commands with; 'bash' is
	// recommended.
	Shell string
}

// standin describes a server that we're in the middle of spawning, allowing us
// to keep track of command->server allocations while they're still being
// created.
type standin struct {
	id        string
	flavor    cloud.Flavor
	usedRAM   int
	usedCores int
	usedDisk  int
	mutex     sync.RWMutex
	server    *cloud.Server
	fail      bool
	work      bool
}

// newStandin returns a new standin server
func newStandin(id string, flavor cloud.Flavor) *standin {
	return &standin{id: id, flavor: flavor}
}

// allocate is like cloud.Server.Allocate()
func (s *standin) allocate(req *Requirements) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.usedCores += req.Cores
	s.usedRAM += req.RAM
	s.usedDisk += req.Disk
}

// hasSpaceFor is like cloud.Server.HasSpaceFor()
func (s *standin) hasSpaceFor(req *Requirements) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if (s.flavor.Cores-s.usedCores < req.Cores) || (s.flavor.RAM-s.usedRAM < req.RAM) || (s.flavor.Disk-s.usedDisk < req.Disk) {
		return 0
	}
	canDo := (s.flavor.Cores - s.usedCores) / req.Cores
	if canDo > 1 {
		n := (s.flavor.RAM - s.usedRAM) / req.RAM
		if n < canDo {
			canDo = n
		}
		n = (s.flavor.Disk - s.usedDisk) / req.Disk
		if n < canDo {
			canDo = n
		}
	}
	return canDo
}

// failed is what you call if the server that this is a standin for failed to
// start up; anything that is waiting on waitForServer() will then receive nil.
func (s *standin) failed() {
	//*** not yet implemented properly?
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.fail = true
}

// worked is what you call once the server that this is a standin for has
// actually started up successfully. Anything that is waiting on waitForServer()
// will then receive the server you supply here.
func (s *standin) worked(server *cloud.Server) {
	//*** not yet implemented properly?
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.server = server
	s.work = true
}

// waitForServer waits until another goroutine calls failed() or worked(). You
// would use this after checking hasSpaceFor() and doing allocate().
func (s *standin) waitForServer() (server *cloud.Server) {
	//*** not yet implemented properly?
	done := make(chan *cloud.Server)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		for {
			select {
			case <-ticker.C:
				s.mutex.RLock()
				if s.work || s.fail {
					ticker.Stop()
					s.mutex.RUnlock()
					done <- s.server
					return
				}
				s.mutex.RUnlock()
				continue
			}
		}
	}()
	return <-done
}

// initialize sets up an openstack scheduler.
func (s *opst) initialize(config interface{}) (err error) {
	s.config = config.(*ConfigOpenStack)
	if s.config.OSRAM == 0 {
		s.config.OSRAM = 2048
	}

	// create a cloud provider for openstack, that we'll use to interact with
	// openstack
	provider, err := cloud.New("openstack", s.config.ResourceName, s.config.SavePath)
	if err != nil {
		return
	}
	s.provider = provider

	err = provider.Deploy(s.config.ServerPorts)
	if err != nil {
		return
	}

	// query our quota maximums for cpu and memory and total number of
	// instances; 0 will mean unlimited
	quota, err := provider.GetQuota()
	if err != nil {
		return
	}
	s.quotaMaxCores = quota.MaxCores
	s.quotaMaxRAM = quota.MaxRAM
	s.quotaMaxInstances = quota.MaxInstances
	if s.config.MaxInstances > 0 && s.config.MaxInstances < s.quotaMaxInstances {
		s.quotaMaxInstances = s.config.MaxInstances
	}

	// initialize our job queue and other trackers
	s.queue = queue.New(localPlace)
	s.running = make(map[string]int)

	// initialise our servers with details of ourself
	s.servers = make(map[string]*cloud.Server)
	maxRAM, err := s.procMeminfoMBs()
	if err != nil {
		return
	}
	usage := du.NewDiskUsage(".")
	s.servers["localhost"] = &cloud.Server{
		IP: "127.0.0.1",
		Flavor: cloud.Flavor{
			RAM:   maxRAM,
			Cores: runtime.NumCPU(),
			Disk:  int(usage.Size() / gb),
		},
	}

	// set our functions for use in schedule() and processQueue()
	s.reqCheckFunc = s.reqCheck
	s.canCountFunc = s.canCount
	s.runCmdFunc = s.runCmd

	// pass through our shell config to our local embed
	s.local.config = &ConfigLocal{Shell: s.config.Shell}

	s.standins = make(map[string]*standin)
	s.stopWaitingToSpawn = make(chan bool)

	return
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met,
// based on our quota and the available server flavours.
func (s *opst) reqCheck(req *Requirements) error {
	// check if possible vs quota
	if req.RAM > s.quotaMaxRAM || req.Cores > s.quotaMaxCores {
		return Error{"openstack", "schedule", ErrImpossible}
	}

	// check if possible vs flavors
	_, err := s.determineFlavor(req)
	return err
}

// determineFlavor picks a server flavor, preferring the smallest (cheapest)
// amongst those that are capable of running it.
func (s *opst) determineFlavor(req *Requirements) (flavor cloud.Flavor, err error) {
	flavor, err = s.provider.CheapestServerFlavor(req.Cores, req.RAM, req.Disk, s.config.FlavorRegex)
	if err != nil {
		if perr, ok := err.(cloud.Error); ok && perr.Err == cloud.ErrNoFlavor {
			err = Error{"openstack", "determineFlavor", ErrImpossible}
		}
	}
	return
}

// canCount tells you how many jobs with the given RAM and core requirements it
// is possible to run, given remaining resources.
func (s *opst) canCount(req *Requirements) (canCount int) {
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
	for sid, server := range s.servers {
		if server.Destroyed() {
			delete(s.servers, sid)
			continue
		}
		canCount += server.HasSpaceFor(req.Cores, req.RAM, req.Disk)
	}

	// now we get the smallest server type that can run our job, and calculate
	// how many we could spawn before exceeding our quota
	reqForSpawn := req
	if req.RAM < s.config.OSRAM {
		reqForSpawn = &Requirements{
			RAM:   s.config.OSRAM,
			Time:  req.Time,
			Cores: req.Cores,
			Disk:  req.Disk,
			Other: req.Other,
		}
	}
	flavor, err := s.determineFlavor(reqForSpawn)
	if err != nil {
		return
	}
	quota, err := s.provider.GetQuota()
	if err != nil {
		return
	}
	remainingInstances := unquotadVal
	if s.quotaMaxInstances > 0 { // this instead of quota.MaxInstances because our own config may be lower
		remainingInstances = s.quotaMaxInstances - quota.UsedInstances - s.reservedInstances
	}
	remainingRAM := unquotadVal
	if quota.MaxRAM > 0 {
		remainingRAM = quota.MaxRAM - quota.UsedRAM - s.reservedRAM
	}
	remainingCores := unquotadVal
	if quota.MaxCores > 0 {
		remainingCores = quota.MaxCores - quota.UsedCores - s.reservedCores
	}
	if remainingInstances < 1 || remainingRAM < flavor.RAM || remainingCores < flavor.Cores {
		return
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
			n = flavor.Disk / reqForSpawn.Disk
			if n < perServer {
				perServer = n
			}
		}
	}
	canCount += spawnable * perServer
	return
}

// runCmd runs the command on next available server, or creates a new server if
// none are available. NB: we only return an error if we can't start the cmd,
// not if the command fails (schedule() only guarantees that the cmds are run
// count times, not that they are /successful/ that many times).
func (s *opst) runCmd(cmd string, req *Requirements) error {
	// look through space on existing servers to see if we can run cmd on one
	// of them
	s.mutex.Lock()
	var server *cloud.Server
	for sid, thisServer := range s.servers {
		if thisServer.Destroyed() {
			delete(s.servers, sid)
			continue
		}
		if thisServer.HasSpaceFor(req.Cores, req.RAM, req.Disk) > 0 {
			server = thisServer
			break
		}
	}

	// else see if there will be space on a soon-to-be-spawned server
	// *** this is untested
	if server == nil {
		for _, standinServer := range s.standins {
			if standinServer.hasSpaceFor(req) > 0 {
				standinServer.allocate(req)
				s.mutex.Unlock()
				server = standinServer.waitForServer()
				s.mutex.Lock()
			}
		}
	}

	// else spawn the smallest server that can run this cmd, recording our new
	// quota usage.
	if server == nil {
		reqForSpawn := req
		if req.RAM < s.config.OSRAM {
			reqForSpawn = &Requirements{
				RAM:   s.config.OSRAM,
				Time:  req.Time,
				Cores: req.Cores,
				Disk:  req.Disk,
				Other: req.Other,
			}
		}
		flavor, err := s.determineFlavor(reqForSpawn)
		if err != nil {
			s.mutex.Unlock()
			return err
		}

		// because spawning can take a while, we record that we're going to use
		// up some of our quota and unlock so other things can proceed
		numSpawning := s.waitingToSpawn + s.spawningNow
		if numSpawning == 0 {
			s.nextSpawnTime = time.Now().Add(10 * time.Second)
			s.spawningNow++
		} else {
			s.waitingToSpawn++
		}
		s.reservedInstances++
		s.reservedCores += flavor.Cores
		s.reservedRAM += flavor.RAM

		standinID := uuid.NewV4().String()
		standinServer := newStandin(standinID, flavor)
		standinServer.allocate(req)
		s.standins[standinID] = standinServer
		s.mutex.Unlock()

		// now spawn, but don't overload the system by trying to spawn too many
		// at once; wait at least 10 seconds between each spawn
		if numSpawning > 0 {
			done := make(chan error)
			go func() {
				ticker := time.NewTicker(1 * time.Second)
				for {
					select {
					case <-ticker.C:
						s.mutex.Lock()
						if time.Now().After(s.nextSpawnTime) {
							s.nextSpawnTime = time.Now().Add(10 * time.Second)
							s.waitingToSpawn--
							s.spawningNow++
							s.mutex.Unlock()
							ticker.Stop()
							done <- nil
							return
						}
						s.mutex.Unlock()
						continue
					case <-s.stopWaitingToSpawn:
						ticker.Stop()
						s.mutex.Lock()
						s.waitingToSpawn--
						standinServer.failed()
						delete(s.standins, standinID)
						s.mutex.Unlock()
						done <- errors.New("giving up waiting to spawn")
						return
					}
				}
			}()
			err = <-done
			if err != nil {
				return err
			}
		}
		server, err = s.provider.Spawn(s.config.OSPrefix, s.config.OSUser, flavor.ID, s.config.ServerKeepTime, false, s.config.PostCreationScript)
		if err != nil {
			s.mutex.Lock()
			s.spawningNow--
			s.reservedInstances--
			s.reservedCores -= flavor.Cores
			s.reservedRAM -= flavor.RAM
			standinServer.failed()
			delete(s.standins, standinID)
			s.mutex.Unlock()
			return err
		}

		// check that the exe of the cmd we're supposed to run exists on the new
		// server, and if not, copy it over *** this is just a hack to get wr
		// working, need to think of a better way of doing this...
		exe := strings.Split(cmd, " ")[0]
		if _, err = os.Stat(exe); err == nil {
			if stdout, err := server.RunCmd("file "+exe, false); err == nil && strings.Contains(stdout, "No such file") {
				err = server.UploadFile(exe, exe)
				if err == nil {
					server.RunCmd("chmod u+x "+exe, false)
				} else {
					return err
				}
			}
		}

		s.mutex.Lock()
		s.spawningNow--
		s.reservedInstances--
		s.reservedCores -= flavor.Cores
		s.reservedRAM -= flavor.RAM
		s.servers[server.ID] = server
		standinServer.worked(server)
		delete(s.standins, standinID)
	}

	server.Allocate(req.Cores, req.RAM, req.Disk)
	s.mutex.Unlock()

	// now we have a server, ssh over and run the cmd on it
	var err error
	if server.IP == "127.0.0.1" {
		err = s.local.runCmd(cmd, req)
	} else {
		_, err = server.RunCmd(cmd, false)
	}

	// having run a command, this server is now available for another; signal a
	// runCmd call that is waiting its turn to spawn a new server to give up
	// waiting and potentially get scheduled on us instead
	s.mutex.Lock()
	server.Release(req.Cores, req.RAM, req.Disk)
	if s.waitingToSpawn > 0 && server.IP != "127.0.0.1" {
		s.mutex.Unlock()
		s.stopWaitingToSpawn <- true
	} else {
		s.mutex.Unlock()
	}

	return err
}

// cleanup destroys our internal queues and brings down our servers
func (s *opst) cleanup() {
	s.cleaned = true

	// bring down all our servers
	for sid, server := range s.servers {
		if sid == "localhost" {
			continue
		}
		server.Destroy()
		delete(s.servers, sid)
	}

	// destroy our queue
	s.queue.Destroy()

	// teardown any cloud resources created
	s.provider.TearDown()
}
