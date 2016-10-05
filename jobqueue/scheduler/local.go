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
	"bufio"
	"bytes"
	"fmt"
	"github.com/VertebrateResequencing/wr/queue"
	"math"
	"os"
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

// cmdRunners are functions used by processQueue() to actually run cmds.
// (Their reason for being is the same as for canCounters.)
type cmdRunner func(cmd string, req *Requirements)

// local is our implementer of scheduleri.
type local struct {
	config       *SchedulerConfigLocal
	maxmb        int
	maxcores     int
	mb           int
	cores        int
	rcount       int
	mutex        sync.Mutex
	queue        *queue.Queue
	running      map[string]int
	cleaned      bool
	reqCheckFunc reqChecker
	canCountFunc canCounter
	runCmdFunc   cmdRunner
}

// SchedulerConfigLocal represents the configuration options required by the
// local scheduler. All are required with no usable defaults.
type SchedulerConfigLocal struct {
	// Shell is the shell to use to run the commands to interact with your job
	// scheduler; 'bash' is recommended.
	Shell string
}

// jobs are what we store in our queue.
type job struct {
	cmd   string
	req   *Requirements
	count int
}

// initialize finds out about the local machine. Compatible with linux-like
// systems with /proc/meminfo only!
func (s *local) initialize(config interface{}) (err error) {
	s.config = config.(*SchedulerConfigLocal)
	s.maxcores = runtime.NumCPU()

	// get MemTotal from /proc/meminfo
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	kb := uint64(0)
	r := bufio.NewScanner(f)
	for r.Scan() {
		line := r.Bytes()
		if bytes.HasPrefix(line, mt) {
			_, err = fmt.Sscanf(string(line[9:]), "%d", &kb)
			if err != nil {
				return
			}
			break
		}
	}
	if err = r.Err(); err != nil {
		return
	}

	// convert kB to MB
	s.maxmb = int(kb / 1024)

	// make our queue
	s.queue = queue.New(localPlace)
	s.running = make(map[string]int)

	// set our functions for use in schedule() and processQueue()
	s.reqCheckFunc = s.reqCheck
	s.canCountFunc = s.canCount
	s.runCmdFunc = s.runCmd

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
	// first find out if its at all possible to ever run this cmd
	err := s.reqCheckFunc(req)
	if err != nil {
		return err
	}

	// add to the queue
	key := jobName(cmd, "n/a", false)
	data := &job{cmd, req, count}
	s.mutex.Lock()
	item, err := s.queue.Add(key, data, 0, 0*time.Second, 30*time.Second) // the ttr just has to be long enough for processQueue() to process a job, not actually run the cmds
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

	// try and run the oldest job in the queue
	return s.processQueue()
}

// reqCheck gives an ErrImpossible if the given Requirements can not be met.
func (s *local) reqCheck(req *Requirements) error {
	if req.Memory > s.maxmb || req.CPUs > s.maxcores {
		return Error{"local", "schedule", ErrImpossible}
	}
	return nil
}

// processQueue gets the oldest job in the queue, sees if it's possible to run
// it, does so if it does, otherwise returns the job to the queue.
func (s *local) processQueue() error {
	if s.cleaned {
		return nil
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
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
		shouldCount := count - running
		if shouldCount <= 0 {
			// we're already running everything for this job, try the next most
			// oldest
			continue
		}

		// now see if there's remaining capacity to run the job
		canCount = s.canCountFunc(req)
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
	for i := 0; i < canCount; i++ {
		s.mb += req.Memory
		s.cores += req.CPUs
		s.running[key]++

		go func() {
			s.runCmdFunc(cmd, req)
			s.mutex.Lock()
			s.mb -= req.Memory
			s.cores -= req.CPUs
			s.running[key]--
			if s.running[key] <= 0 {
				delete(s.running, key)
			}
			j.count--
			if j.count <= 0 {
				s.queue.Remove(key)
			}
			s.mutex.Unlock()
			s.processQueue()
		}()
	}

	// the item will now be released, so on the next call to this method we'll
	// try to run the remainder
	return nil
}

// canCount tells you how many jobs with the given mb and cpu requirements it is
// possible to run, given remaining resources.
func (s *local) canCount(req *Requirements) (canCount int) {
	// we don't do any actual checking of current resources on the machine, but
	// instead rely on our simple tracking based on how many cpus and memory
	// prior cmds were /supposed/ to use. This could be bad for misbehaving cmds
	// that use too much memory, but we will end up killing cmds that do this,
	// so it shouldn't be too much of an issue.
	canCount = int(math.Floor(float64(s.maxmb-s.mb) / float64(req.Memory)))
	if canCount >= 1 {
		canCount2 := int(math.Floor(float64(s.maxcores-s.cores) / float64(req.CPUs)))
		if canCount2 < canCount {
			canCount = canCount2
		}
	}
	return
}

// runCmd runs the command, kills it if it goes much over memory or time limits.
// NB: errors are ignored (schedule() only guarantees that the cmds are run
// count times, not that they are /successful/ that many times).
func (s *local) runCmd(cmd string, req *Requirements) {
	ec := exec.Command(s.config.Shell, "-c", cmd)
	err := ec.Start()
	if err != nil {
		fmt.Println(err)
		return
	}

	s.mutex.Lock()
	s.rcount++
	s.mutex.Unlock()

	//*** set up monitoring of memory and time usage and kill if >> than
	// req.Memory or req.Time

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
}

// busy returns true if there's anything in our queue or we are still running
// any cmd
func (s *local) busy() bool {
	if s.queue.Stats().Items == 0 && s.rcount <= 0 {
		return false
	}
	return true
}

// cleanup destroys our internal queue
func (s *local) cleanup() {
	s.cleaned = true
	s.queue.Destroy()
	return
}
