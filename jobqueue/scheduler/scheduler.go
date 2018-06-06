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

/*
Package scheduler lets the jobqueue server interact with the configured job
scheduler (if any) to submit jobqueue runner clients and have them run on a
compute cluster (or local machine).

Currently implemented schedulers are local, LSF and OpenStack. The
implementation of each supported scheduler type is in its own .go file.

It's a pseudo plug-in system in that it is designed so that you can easily add a
go file that implements the methods of the scheduleri interface, to support a
new job scheduler. On the other hand, there is no dynamic loading of these go
files; they are all imported (they all belong to the scheduler package), and the
correct one used at run time. To "register" a new scheduleri implementation you
must add a case for it to New() and rebuild.

    import "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
    s, err := scheduler.New("local", &scheduler.ConfigLocal{"bash"})
    req := &scheduler.Requirements{RAM: 300, Time: 2 * time.Hour, Cores: 1}
    err = s.Schedule("myWRRunnerClient -args", req, 24)
    // wait, and when s.Busy() returns false, your command has been run 24 times
*/
package scheduler

import (
	"crypto/md5" // #nosec - not used for cryptographic purposes here
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/dgryski/go-farm"
	"github.com/inconshreveable/log15"
)

const (
	defaultReserveTimeout               = 1 // implementers of reserveTimeout can just return this
	infiniteQueueTime     time.Duration = 0
)

// Err* constants are found in the returned Errors under err.Err, so you can
// cast and check if it's a certain type of error.
var (
	ErrBadScheduler = "unknown scheduler name"
	ErrImpossible   = "scheduler cannot accept the job, since its resource requirements are too high"
	ErrBadFlavor    = "unknown server flavor"
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
	RAM   int               // the expected peak RAM in MB Cmd will use while running
	Time  time.Duration     // the expected time Cmd will take to run
	Cores int               // how many processor cores the Cmd will use
	Disk  int               // the required local disk space in GB the Cmd needs to run
	Other map[string]string // a map that will be passed through to the job scheduler, defining further arbitrary resource requirements
}

// Stringify represents the contents of the Requirements as a string, sorting
// the keys of Other to ensure the same result is returned for the same content
// every time. Note that the data in Other undergoes a 1-way transformation,
// so you cannot recreate the Requirements from the the output of this method.
func (req *Requirements) Stringify() string {
	var other string
	if len(req.Other) > 0 {
		otherKeys := make([]string, 0, len(req.Other))
		for key := range req.Other {
			otherKeys = append(otherKeys, key)
		}
		sort.Strings(otherKeys)
		for _, key := range otherKeys {
			other += ":" + key + "=" + req.Other[key]
		}

		// now convert it all in to an md5sum, to avoid any problems with some
		// key values having line returns etc. *** we might like to use
		// byteKey() from jobqueue package instead, but that isn't exported...
		other = fmt.Sprintf(":%x", md5.Sum([]byte(other))) // #nosec
	}

	return fmt.Sprintf("%d:%.0f:%d:%d%s", req.RAM, req.Time.Minutes(), req.Cores, req.Disk, other)
}

// CmdStatus lets you describe how many of a given cmd are already in the job
// scheduler, and gives the details of those jobs.
type CmdStatus struct {
	Count   int
	Running [][2]int // a slice of [id, index] tuples
	Pending [][2]int // ditto
	Other   [][2]int // ditto, for jobs in some strange state
}

// MessageCallBack functions receive a message that would be good to display to
// end users, so they understand current error conditions related to the
// scheduler.
type MessageCallBack func(msg string)

// BadServerCallBack functions receive a server when a cloud scheduler discovers
// that a server it spawned no longer seems functional. It's possible that this
// was due to a temporary networking issue, in which case the callback will be
// called again with the same server when it is working fine again: check
// server.IsBad(). If it's bad, you'd probably call server.Destroy() after
// confirming the server is definitely unusable (eg. ask the end user to
// manually check).
type BadServerCallBack func(server *cloud.Server)

// scheduleri interface must be satisfied to add support for a particular job
// scheduler.
type scheduleri interface {
	initialize(config interface{}, logger log15.Logger) error // do any initial set up to be able to use the job scheduler
	schedule(cmd string, req *Requirements, count int) error  // achieve the aims of Schedule()
	busy() bool                                               // achieve the aims of Busy()
	reserveTimeout() int                                      // achieve the aims of ReserveTimeout()
	maxQueueTime(req *Requirements) time.Duration             // achieve the aims of MaxQueueTime()
	hostToID(host string) string                              // achieve the aims of HostToID()
	setMessageCallBack(MessageCallBack)                       // achieve the aims of SetMessageCallBack()
	setBadServerCallBack(BadServerCallBack)                   // achieve the aims of SetBadServerCallBack()
	cleanup()                                                 // do any clean up once you've finished using the job scheduler
}

// CloudConfig interface could be satisfied by the config option taken by cloud
// schedulers which have a ConfigFiles property.
type CloudConfig interface {
	// AddConfigFile takes a value like that of the ConfigFiles property of the
	// struct implementing this interface, and appends this value to what is
	// in ConfigFiles, or sets it if unset.
	AddConfigFile(spec string)
}

// Scheduler gives you access to all of the methods you'll need to interact with
// a job scheduler.
type Scheduler struct {
	impl    scheduleri
	Name    string
	limiter map[string]int
	sync.Mutex
	log15.Logger
}

// New creates a new Scheduler to interact with the given job scheduler.
// Possible names so far are "lsf", "local" and "openstack". You must also
// provide a config struct appropriate for your chosen scheduler, eg. for the
// local scheduler you will provide a ConfigLocal.
//
// Providing a logger allows for debug messages to be logged somewhere, along
// with any "harmless" or unreturnable errors. If not supplied, we use a default
// logger that discards all log messages.
func New(name string, config interface{}, logger ...log15.Logger) (*Scheduler, error) {
	var s *Scheduler
	switch name {
	case "lsf":
		s = &Scheduler{impl: new(lsf)}
	case "local":
		s = &Scheduler{impl: new(local)}
	case "openstack":
		s = &Scheduler{impl: new(opst)}
	case "kubernetes":
		s = &Scheduler{impl: new(k8s)}
	default:
		return nil, Error{name, "New", ErrBadScheduler}
	}

	var l log15.Logger
	if len(logger) == 1 {
		l = logger[0].New()
	} else {
		l = log15.New()
		l.SetHandler(log15.DiscardHandler())
	}
	s.Logger = l

	s.Name = name
	s.limiter = make(map[string]int)
	err := s.impl.initialize(config, l)

	return s, err
}

// SetMessageCallBack sets the function that will be called when a scheduler has
// some message that could be informative to end users wondering why something
// is not getting scheduled. The message typically describes an error condition.
func (s *Scheduler) SetMessageCallBack(cb MessageCallBack) {
	s.impl.setMessageCallBack(cb)
}

// SetBadServerCallBack sets the function that will be called when a cloud
// scheduler discovers that one of the servers it spawned seems to no longer be
// functional or reachable. Only relevant for cloud schedulers.
func (s *Scheduler) SetBadServerCallBack(cb BadServerCallBack) {
	s.impl.setBadServerCallBack(cb)
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
// and a lower count. NB: there is no guarantee that the jobs run successfully,
// and no feedback on their success or failure is given.
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
		if newcount != count {
			go func() {
				defer internal.LogPanic(s.Logger, "schedule recall", true)
				errf := s.Schedule(cmd, req, newcount)
				if errf != nil {
					s.Error("schedule recall", "err", err)
				}
			}()
		}
		delete(s.limiter, cmd)
	}
	s.Unlock()

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

// HostToID will return the server id of the server with the given host name, if
// the scheduler is cloud based. Otherwise this just returns an empty string.
func (s *Scheduler) HostToID(host string) string {
	return s.impl.hostToID(host)
}

// Cleanup means you've finished using a scheduler and it can delete any
// remaining jobs in its system and clean up any other used resources.
func (s *Scheduler) Cleanup() {
	s.impl.cleanup()
}

// jobName could be useful to a scheduleri implementer if it needs a constant-
// width (length 36) string unique to the cmd and deployment, and optionally
// suffixed with a random string (length 9, total length 45).
func jobName(cmd string, deployment string, unique bool) string {
	l, h := farm.Hash128([]byte(cmd))
	name := fmt.Sprintf("wr%s_%016x%016x", deployment[0:1], l, h)

	if unique {
		name += "_" + internal.RandomString()
	}

	return name
}
