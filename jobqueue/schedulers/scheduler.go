// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

/*
Package schedulers lets the jobqueue server interact with the local job
scheduler (if any) to submit jobqueue clients and have them run on a compute
cluster (or local machine). Examples of job schedulers are things like LSF and
SGE.

It's a psuedo plug-in system in that it is designed so that you can easily add a
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
	"time"
)

const (
	randBytes   = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	randIdxBits = 6                  // 6 bits to represent a rand index
	randIdxMask = 1<<randIdxBits - 1 // All 1-bits, as many as letterIdxBits
	randIdxMax  = 63 / randIdxBits   // # of letter indices fitting in 63 bits
)

var (
	ErrBadScheduler = "unknown scheduler name"
	ErrImpossible   = "scheduler cannot accept the job, since its resource requirements are too high"
)

// Error records an error and the operation, item and queue that caused it.
type Error struct {
	Scheduler string // the scheduler's Name
	Op        string // name of the method
	Err       string // one of our Err* vars
}

func (e Error) Error() string {
	return "scheduler(" + e.Scheduler + ") " + e.Op + "(): " + e.Err
}

// the Requirements type describes the resource requirements of the commands
// you want to run, so that when provided to a scheduler it will be able to
// schedule things appropriately.
type Requirements struct {
	Memory int           // the expected peak memory in MB Cmd will use while running
	Time   time.Duration // the expected time Cmd will take to run
	CPUs   int           // how many processor cores the Cmd will use
	Other  string        // an arbitrary string that will be passed through to the job scheduler, defining further resource requirements
}

// the CmdStatus type lets you describe how many of a given cmd are already in
// the job scheduler, and gives the details of those jobs.
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
}

// the Scheduler struct gives you access to all of the methods you'll need to
// interact with a job scheduler.
type Scheduler struct {
	impl scheduleri
	Name string
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
		err = s.impl.initialize(deployment, shell)
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
	return s.impl.schedule(cmd, req, count)
}

// Busy reports true if there are any Schedule()d cmds still in the job
// scheduler's system. This is useful when testing and other situations where
// you want to avoid shutting down the server while there are still clients
// running/ about to run.
func (s *Scheduler) Busy() bool {
	return s.impl.busy()
}

// jobName could be useful to a scheduleri implementer if it needs a constant-
// width (length 37) string unique to the cmd and deployment, and optionally
// suffixed with a random string (length 9, total length 46).
func jobName(cmd string, deployment string, unique bool) (name string) {
	l, h := farm.Hash128([]byte(cmd))
	name = fmt.Sprintf("vrp%s_%016x%016x", deployment[0:1], l, h)

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
