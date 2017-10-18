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

// This file contains a scheduleri implementation for 'local': running jobs
// on the local machine directly. It has a very simple strictly fifo queue, so
// may not be very efficient with the machine's resources.

import (
	"fmt"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/shirou/gopsutil/mem"
	"log"
	"math"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

const (
	localPlace          = "localhost"
	localReserveTimeout = 1
)

var mt = []byte("MemTotal:")

// reqCheckers are functions used by schedule() to see if it is at all possible
// to ever run a job with the given resource requirements. (We make use of this
// in the local struct so that other implementers of scheduleri can embed local,
// use local's schedule(), but have their own reqChecker implementation.)
type reqChecker func(req *Requirements) error

// canCounters are functions used by processQueue() to see how many of a job
// can be run. (We make use of this in the local struct so that other
// implementers of scheduleri can embed local, use local's processQueue(), but
// have their own canCounter implementation.)
type canCounter func(req *Requirements) (canCount int)

// stateUpdaters are functions used by processQueue() to update any global state
// that might have become invalid due to changes external to our own actions.
// (We make use of this in the local struct so that other implementers of
// scheduleri can embed local, use local's processQueue(), but have their own
// stateUpdater implementation.)
type stateUpdater func()

// cmdRunners are functions used by processQueue() to actually run cmds.
// (Their reason for being is the same as for canCounters.)
type cmdRunner func(cmd string, req *Requirements) error

// cancelCmdRunner are functions used by processQueue() to cancel the running
// of cmdRunners that started but didn't really start to run the cmd. You give
// the cmd and the number still needed and it will cancel any extras that have
// been created but not started.
// (Their reason for being is the same as for canCounters.)
type cancelCmdRunner func(cmd string, desiredNumber int)

// local is our implementer of scheduleri.
type local struct {
	config           *ConfigLocal
	maxRAM           int
	maxCores         int
	ram              int
	cores            int
	rcount           int
	mutex            sync.Mutex
	queue            *queue.Queue
	running          map[string]int
	cleaned          bool
	reqCheckFunc     reqChecker
	canCountFunc     canCounter
	stateUpdateFunc  stateUpdater
	stateUpdateFreq  time.Duration
	runCmdFunc       cmdRunner
	cancelRunCmdFunc cancelCmdRunner
	autoProcessing   bool
	stopAuto         chan bool
	debugMode        bool
}

// ConfigLocal represents the configuration options required by the local
// scheduler. All are required with no usable defaults.
type ConfigLocal struct {
	// Shell is the shell to use to run your commands with; 'bash' is
	// recommended.
	Shell string

	// StateUpdateFrequency is the frequency at which to re-check the queue to
	// see if anything can now run. 0 (default) is treated as 1 minute.
	StateUpdateFrequency time.Duration
}

// jobs are what we store in our queue.
type job struct {
	cmd   string
	req   *Requirements
	count int
}

// initialize finds out about the local machine. Compatible with amd64 archs
// only!
func (s *local) initialize(config interface{}) (err error) {
	s.config = config.(*ConfigLocal)
	s.maxCores = runtime.NumCPU()
	s.maxRAM, err = s.procMeminfoMBs()
	if err != nil {
		return
	}

	// make our queue
	s.queue = queue.New(localPlace)
	s.running = make(map[string]int)

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

	return
}

// procMeminfoMBs uses gopsutil (amd64 freebsd, linux, windows, darwin, openbds
// only!) to find the total number of MBs of memory physically installed on the
// current system.
func (s *local) procMeminfoMBs() (mbs int, err error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return
	}

	// convert bytes to MB
	mbs = int((v.Total / 1024) / 1024)
	return
}

// reserveTimeout achieves the aims of ReserveTimeout().
func (s *local) reserveTimeout() int {
	return localReserveTimeout
}

// maxQueueTime achieves the aims of MaxQueueTime().
func (s *local) maxQueueTime(req *Requirements) time.Duration {
	return infiniteQueueTime
}

// schedule achieves the aims of Schedule().
func (s *local) schedule(cmd string, req *Requirements, count int) error {
	s.mutex.Lock()
	if s.cleaned {
		s.mutex.Unlock()
		return nil
	}
	s.mutex.Unlock()

	// first find out if its at all possible to ever run this cmd
	err := s.reqCheckFunc(req)
	if err != nil {
		return err
	}

	// add to the queue
	key := jobName(cmd, "n/a", false)
	data := &job{cmd, req, count}
	s.mutex.Lock()
	item, err := s.queue.Add(key, "", data, 0, 0*time.Second, 30*time.Second) // the ttr just has to be long enough for processQueue() to process a job, not actually run the cmds
	if err != nil {
		if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrAlreadyExists {
			// update the job's count (only)
			j := item.Data.(*job)
			j.count = count
		} else {
			s.mutex.Unlock()
			return err
		}
	}
	s.mutex.Unlock()

	s.startAutoProcessing()

	// try and run the oldest job in the queue
	return s.processQueue()
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met.
func (s *local) reqCheck(req *Requirements) error {
	if req.RAM > s.maxRAM || req.Cores > s.maxCores {
		return Error{"local", "schedule", ErrImpossible}
	}
	return nil
}

// processQueue gets the oldest job in the queue, sees if it's possible to run
// it, does so if it is, otherwise returns the job to the queue.
func (s *local) processQueue() error {
	// first perform any global state update needed by the scheduler
	s.stateUpdateFunc()

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cleaned {
		return nil
	}
	var key, cmd string
	var req *Requirements
	var count, canCount int
	var j *job

	// get the oldest job
	var toRelease []string
	defer func() {
		for _, key := range toRelease {
			s.queue.Release(key)
		}
	}()
	for {
		item, err := s.queue.Reserve()
		if err != nil {
			if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrNothingReady {
				return nil
			}
			return err
		}
		key = item.Key
		toRelease = append(toRelease, key)
		j = item.Data.(*job)
		cmd = j.cmd
		req = j.req
		count = j.count

		running := s.running[key]
		s.debug("processQueue() needs %d [%s] running, currently %d\n", count, cmd, running)
		if count < running {
			// "running" things may not actually be running the cmd yet, so tell
			// extraneous ones to cancel and not start running
			s.cancelRunCmdFunc(cmd, count)
		}
		shouldCount := count - running
		if shouldCount <= 0 {
			// we're already running everything for this job, try the next most
			// oldest
			continue
		}

		// now see if there's remaining capacity to run the job
		canCount = s.canCountFunc(req)
		s.debug("processQueue() can run %d of these commands\n", canCount)
		if canCount > shouldCount {
			canCount = shouldCount
		}

		if canCount == 0 {
			// we don't want to go to the next most oldest, but will wait until
			// something calls processQueue() again to get the cmd for this
			// job running: dumb fifo behaviour
			//*** could easily make this less dumb by considering how long we
			// most likely have to wait for a currently running job to finish,
			// and seeing if there are any other jobs in the queue that will
			// finish in less time...
			return nil
		}

		break
	}

	// start running what we can
	s.debug("processQueue() will call runCmdFunc %d times\n", canCount)
	for i := 0; i < canCount; i++ {
		s.ram += req.RAM
		s.cores += req.Cores
		s.running[key]++

		go func() {
			err := s.runCmdFunc(cmd, req)
			s.mutex.Lock()
			s.ram -= req.RAM
			s.cores -= req.Cores
			s.running[key]--
			if s.running[key] <= 0 {
				delete(s.running, key)
			}
			var stopAuto bool
			if err == nil {
				j.count--
				if j.count <= 0 {
					s.queue.Remove(key)
					if s.queue.Stats().Items == 0 {
						stopAuto = true
					}
				}
			} else if err.Error() != standinNotNeeded {
				// *** figure out a better way of always letting users know
				// about these errors
				s.debug("jobqueue scheduler runCmd error: %s\n", err)
			}
			s.mutex.Unlock()
			if stopAuto {
				s.stopAutoProcessing()
			}
			s.processQueue()
		}()
	}

	// the item will now be released, so on the next call to this method we'll
	// try to run the remainder
	return nil
}

// canCount tells you how many jobs with the given RAM and core requirements it
// is possible to run, given remaining resources.
func (s *local) canCount(req *Requirements) (canCount int) {
	// we don't do any actual checking of current resources on the machine, but
	// instead rely on our simple tracking based on how many cores and RAM prior
	// cmds were /supposed/ to use. This could be bad for misbehaving cmds that
	// use too much RAM, but we will end up killing cmds that do this, so it
	// shouldn't be too much of an issue.
	canCount = int(math.Floor(float64(s.maxRAM-s.ram) / float64(req.RAM)))
	if canCount >= 1 {
		canCount2 := int(math.Floor(float64(s.maxCores-s.cores) / float64(req.Cores)))
		if canCount2 < canCount {
			canCount = canCount2
		}
	}
	return
}

// runCmd runs the command, kills it if it goes much over RAM or time limits.
// NB: we only return an error if we can't start the cmd, not if the command
// fails (schedule() only guarantees that the cmds are run count times, not that
// they run /successful/ that many times).
func (s *local) runCmd(cmd string, req *Requirements) error {
	ec := exec.Command(s.config.Shell, "-c", cmd)
	err := ec.Start()
	if err != nil {
		fmt.Println(err)
		return err
	}

	s.mutex.Lock()
	s.rcount++
	s.mutex.Unlock()

	//*** set up monitoring of RAM and time usage and kill if >> than
	// req.RAM or req.Time

	err = ec.Wait()
	if err != nil {
		fmt.Println(err)
	}

	s.mutex.Lock()
	s.rcount--
	if s.rcount < 0 {
		s.rcount = 0
	}
	s.mutex.Unlock()

	return nil
}

// cancelRun in the local scheduler is a no-op, since our runCmd immediately
// starts running the cmd and is never eligible for cancellation.
func (s *local) cancelRun(cmd string, cancelCount int) {
	return
}

// stateUpdate in the local scheduler is a no-op, since there currently isn't
// any state out of our control we worry about.
func (s *local) stateUpdate() {
	return
}

// startAutoProcessing begins periodic running of processQueue(). Normally
// processQueue is only called when cmds are added or complete. Calling it
// periodically as well means we are responsive to external events freeing up
// resources.
func (s *local) startAutoProcessing() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cleaned || s.autoProcessing {
		return
	}

	s.stopAuto = make(chan bool)
	go func() {
		ticker := time.NewTicker(s.stateUpdateFreq)
		for {
			select {
			case <-ticker.C:
				s.processQueue()
				continue
			case <-s.stopAuto:
				ticker.Stop()
				return
			}
		}
	}()

	s.autoProcessing = true
}

// stopAutoProcessing turns off the periodic processQueue() calls initiated by
// startAutoProcessing().
func (s *local) stopAutoProcessing() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.cleaned || !s.autoProcessing {
		return
	}

	s.stopAuto <- true

	s.autoProcessing = false
}

// busy returns true if there's anything in our queue or we are still running
// any cmd.
func (s *local) busy() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.cleaned {
		return false
	}
	if s.queue.Stats().Items == 0 && s.rcount <= 0 {
		return false
	}
	return true
}

// hostToID always returns an empty string, since we're not in the cloud.
func (s *local) hostToID(host string) string {
	return ""
}

// setMessageCallBack does nothing at the moment, since we don't generate any
// messages for the user.
func (s *local) setMessageCallBack(cb MessageCallBack) {
	return
}

// setBadServerCallBack does nothing, since we're not a cloud-based scheduler.
func (s *local) setBadServerCallBack(cb BadServerCallBack) {
	return
}

// cleanup destroys our internal queue.
func (s *local) cleanup() {
	s.stopAutoProcessing()
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.cleaned = true
	s.queue.Destroy()
	return
}

func (s *local) debug(msg string, a ...interface{}) {
	if s.debugMode {
		log.Printf(msg, a...)
	}
}
