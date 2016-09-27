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

/*
Package scheduler lets the jobqueue server interact with the local job
scheduler (if any) to submit jobqueue clients and have them run on a compute
cluster (or local machine). Examples of job schedulers are things like LSF and
SGE.

It's a pseudo plug-in system in that it is designed so that you can easily add a
go file that implements the methods of the scheduleri interface, to support a
new job scheduler. On the other hand, there is no dynamic loading of these go
files; they are all imported (they all belong to the scheduler package), and the
correct one used at run time. To "register" a new scheduleri implementation you
must add a case for it to New() and rebuild.
*/
package scheduler

import (
	"fmt"
	"github.com/dgryski/go-farm"
	"math/rand"
	"sync"
	"time"
)

const (
	randBytes                           = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	randIdxBits                         = 6                  // 6 bits to represent a rand index
	randIdxMask                         = 1<<randIdxBits - 1 // All 1-bits, as many as letterIdxBits
	randIdxMax                          = 63 / randIdxBits   // # of letter indices fitting in 63 bits
	defaultReserveTimeout               = 1                  // implementers of reserveTimeout can just return this
	infiniteQueueTime     time.Duration = 0
)

// Err* constants are found in the returned Errors under err.Err, so you can
// cast and check if it's a certain type of error.
var (
	ErrBadScheduler = "unknown scheduler name"
	ErrImpossible   = "scheduler cannot accept the job, since its resource requirements are too high"
)

// Error records an error and the operation and scheduler that caused it.
type Error struct {
	Scheduler string // the scheduler's Name
	Op        string // name of the method
	Err       string // one of our Err* vars
}

func (e Error) Error() string {
	return "scheduler(" + e.Scheduler + ") " + e.Op + "(): " + e.Err
}

// Requirements describes the resource requirements of the commands you want to
// run, so that when provided to a scheduler it will be able to schedule things
// appropriately.
type Requirements struct {
	Memory int           // the expected peak memory in MB Cmd will use while running
	Time   time.Duration // the expected time Cmd will take to run
	CPUs   int           // how many processor cores the Cmd will use
	Disk   int           // the required local disk space in GB the Cmd needs to run
	Other  string        // an arbitrary string that will be passed through to the job scheduler, defining further resource requirements
}

// CmdStatus lets you describe how many of a given cmd are already in the job
// scheduler, and gives the details of those jobs.
type CmdStatus struct {
	Count   int
	Running [][2]int // a slice of [id, index] tuples
	Pending [][2]int // ditto
	Other   [][2]int // ditto, for jobs in some strange state
}

// this interface must be satisfied to add support for a particular job
// scheduler.
type scheduleri interface {
	initialize(deployment string, shell string) error        // do any initial set up to be able to use the job scheduler
	schedule(cmd string, req *Requirements, count int) error // achieve the aims of Schedule()
	busy() bool                                              // achieve the aims of Busy()
	reserveTimeout() int                                     // achieve the aims of ReserveTimeout()
	maxQueueTime(req *Requirements) time.Duration            // achieve the aims of MaxQueueTime()
	cleanup(deployment string, shell string)                 // do any clean up once you've finished using the job scheduler
}

// Scheduler gives you access to all of the methods you'll need to interact with
// a job scheduler.
type Scheduler struct {
	impl    scheduleri
	Name    string
	limiter map[string]int
	sync.Mutex
	deployment string
	shell      string
}

// New creates a new Scheduler to interact with the given job scheduler.
// Possible names so far are "lsf" and "local". You must provide the shell that
// commands to interact with your job scheduler will be run on; 'bash' is
// recommended.
func New(name string, deployment string, shell string) (s *Scheduler, err error) {
	switch name {
	case "lsf":
		s = &Scheduler{impl: new(lsf)}
	case "local":
		s = &Scheduler{impl: new(local)}
	}

	if s == nil {
		err = Error{name, "New", ErrBadScheduler}
	} else {
		s.Name = name
		s.limiter = make(map[string]int)
		err = s.impl.initialize(deployment, shell)
		s.deployment = deployment
		s.shell = shell
	}

	return
}

// Schedule gets your cmd scheduled in the job scheduler. You give it a command
// that you would like `count` identical instances of running via your job
// scheduler. If you already had `count` many scheduled, it will do nothing. If
// you had less than `count`, it will schedule more to run. If you have more
// than `count`, it will remove the appropriate number of scheduled (but not yet
// running) jobs that were previously scheduled for this same cmd (counts of 0
// are legitimate - it will get rid of all non-running jobs for the cmd). If no
// error is returned, you know all `count` of your jobs are now scheduled and
// will eventually run unless you call Schedule() again with the same command
// and a lower count.
func (s *Scheduler) Schedule(cmd string, req *Requirements, count int) error {
	// Schedule may get called many times in different go routines, eg. a
	// succession of calls with the same cmd and req but decrementing count.
	// Here we arrange that impl.schedule is only called once at a time per
	// cmd: if not already running we call as normal; if running we don't run
	// it but return immediately while storing the more recent desired count;
	// when it finishes running, we re-run with the most recent count, if any
	s.Lock()
	if _, limited := s.limiter[cmd]; limited {
		s.limiter[cmd] = count
		s.Unlock()
		return nil
	}
	s.limiter[cmd] = count
	s.Unlock()

	err := s.impl.schedule(cmd, req, count)

	s.Lock()
	if newcount, limited := s.limiter[cmd]; limited {
		delete(s.limiter, cmd)
		s.Unlock()
		if newcount != count {
			return s.Schedule(cmd, req, newcount)
		}
	} else {
		s.Unlock()
	}
	return err
}

// Busy reports true if there are any Schedule()d cmds still in the job
// scheduler's system. This is useful when testing and other situations where
// you want to avoid shutting down the server while there are still clients
// running/ about to run.
func (s *Scheduler) Busy() bool {
	return s.impl.busy()
}

// ReserveTimeout returns the number of seconds that runners spawned in this
// scheduler should wait for new jobs to appear in the manager's queue.
func (s *Scheduler) ReserveTimeout() int {
	return s.impl.reserveTimeout()
}

// MaxQueueTime returns the maximum amount of time that jobs with the given
// resource requirements are allowed to run for in the job scheduler's queue. If
// the job scheduler doesn't have a queue system, or if the queue allows jobs to
// run forever, then this returns a 0 length duration, which should be regarded
// as "infinite" queue time.
func (s *Scheduler) MaxQueueTime(req *Requirements) time.Duration {
	return s.impl.maxQueueTime(req)
}

// Cleanup means you've finished using a scheduler and it can delete any
// remaining jobs in its system and clean up any other used resources.
func (s *Scheduler) Cleanup() {
	s.impl.cleanup(s.deployment, s.shell)
}

// jobName could be useful to a scheduleri implementer if it needs a constant-
// width (length 36) string unique to the cmd and deployment, and optionally
// suffixed with a random string (length 9, total length 45).
func jobName(cmd string, deployment string, unique bool) (name string) {
	l, h := farm.Hash128([]byte(cmd))
	name = fmt.Sprintf("wr%s_%016x%016x", deployment[0:1], l, h)

	if unique {
		// based on http://stackoverflow.com/a/31832326/675083
		b := make([]byte, 8)
		src := rand.NewSource(time.Now().UnixNano())
		for i, cache, remain := 7, src.Int63(), randIdxMax; i >= 0; {
			if remain == 0 {
				cache, remain = src.Int63(), randIdxMax
			}
			if idx := int(cache & randIdxMask); idx < len(randBytes) {
				b[i] = randBytes[idx]
				i--
			}
			cache >>= randIdxBits
			remain--
		}
		name += "_" + string(b)
	}

	return
}
