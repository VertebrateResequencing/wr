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

package jobqueue

// This file contains the functions to implement a jobqueue server.

import (
	"fmt"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/rep"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/grafov/bcast"
	"github.com/ugorji/go/codec"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"
)

// Err* constants are found in the our returned Errors under err.Err, so you
// can cast and check if it's a certain type of error. ServerMode* constants are
// used to report on the status of the server, found inside ServerInfo.
const (
	ErrInternalError  = "internal error"
	ErrUnknownCommand = "unknown command"
	ErrBadRequest     = "bad request (missing arguments?)"
	ErrBadJob         = "bad job (not in queue or correct sub-queue)"
	ErrMissingJob     = "corresponding job not found"
	ErrUnknown        = "unknown error"
	ErrClosedInt      = "queues closed due to SIGINT"
	ErrClosedTerm     = "queues closed due to SIGTERM"
	ErrClosedStop     = "queues closed due to manual Stop()"
	ErrQueueClosed    = "queue closed"
	ErrNoHost         = "could not determine the non-loopback ip address of this host"
	ErrNoServer       = "could not reach the server"
	ErrMustReserve    = "you must Reserve() a Job before passing it to other methods"
	ErrDBError        = "failed to use database"
	ServerModeNormal  = "started"
	ServerModeDrain   = "draining"
)

// these global variables are primarily exported for testing purposes; you
// probably shouldn't change them (*** and they should probably be re-factored
// as fields of a config struct...)
var (
	ServerInterruptTime   = 1 * time.Second
	ServerItemTTR         = 60 * time.Second
	ServerReserveTicker   = 1 * time.Second
	ServerLogClientErrors = true
)

// Error records an error and the operation, item and queue that caused it.
type Error struct {
	Queue string // the queue's Name
	Op    string // name of the method
	Item  string // the item's key
	Err   string // one of our Err* vars
}

func (e Error) Error() string {
	return "jobqueue(" + e.Queue + ") " + e.Op + "(" + e.Item + "): " + e.Err
}

// itemErr is used internally to implement Reserve(), which needs to send item
// and err over a channel.
type itemErr struct {
	item *queue.Item
	err  string
}

// serverResponse is the struct that the server sends to clients over the
// network in response to their clientRequest.
type serverResponse struct {
	Err     string // string instead of error so we can decode on the client side
	Added   int
	Existed int
	Job     *Job
	Jobs    []*Job
	SStats  *ServerStats
}

// ServerInfo holds basic addressing info about the server.
type ServerInfo struct {
	Addr       string // ip:port
	Host       string // hostname
	Port       string // port
	WebPort    string // port of the web interface
	PID        int    // process id of server
	Deployment string // deployment the server is running under
	Scheduler  string // the name of the scheduler that jobs are being submitted to
	Mode       string // ServerModeNormal if the server is running normally, or ServerModeDrain if draining
}

// ServerStats holds information about the jobqueue server for sending to
// clients.
type ServerStats struct {
	ServerInfo *ServerInfo
	Delayed    int           // how many jobs are waiting following a possibly transient error
	Ready      int           // how many jobs are ready to begin running
	Running    int           // how many jobs are currently running
	Buried     int           // how many jobs are no longer being processed because of seemingly permanent errors
	ETC        time.Duration // how long until the the slowest of the currently running jobs is expected to complete
}

type rgToKeys struct {
	sync.RWMutex
	lookup map[string]map[string]bool
}

// jstateCount is the state count change we send to the status webpage; we are
// representing the jobs moving from one state to another.
type jstateCount struct {
	RepGroup  string // "+all+" is the special group representing all live jobs across all RepGroups
	FromState string // one of 'new', 'delay', 'ready', 'run' or 'bury'
	ToState   string // one of 'delay', 'dependent', 'ready', 'run', 'bury' or 'complete'
	Count     int    // num in FromState drop by this much, num in ToState rise by this much
}

// Server represents the server side of the socket that clients Connect() to.
type Server struct {
	ServerInfo *ServerInfo
	sock       mangos.Socket
	ch         codec.Handle
	db         *db
	done       chan error
	stop       chan bool
	up         bool
	drain      bool
	blocking   bool
	sync.Mutex
	qs           map[string]*queue.Queue
	rpl          *rgToKeys
	scheduler    *scheduler.Scheduler
	sgroupcounts map[string]int
	sgrouptrigs  map[string]int
	sgtr         map[string]*scheduler.Requirements
	sgcmutex     sync.Mutex
	rc           string // runner command string compatible with fmt.Sprintf(..., queueName, schedulerGroup, deployment, serverAddr, reserveTimeout, maxMinsAllowed)
	statusCaster *bcast.Group
}

// ServerConfig is supplied to Serve() to configure your jobqueue server. All
// fields are required with no working default unless otherwise noted.
type ServerConfig struct {
	// Port for client-server communication.
	Port string

	// Port for the web interface.
	WebPort string

	// Name of the desired scheduler (eg. "local" or "lsf" or "openstack") that
	// jobs will be submitted to.
	SchedulerName string

	// SchedulerConfig should define the config options needed by the chosen
	// scheduler, eg. scheduler.ConfigLocal{Deployment: "production", Shell:
	// "bash"} if using the local scheduler.
	SchedulerConfig interface{}

	// The command line needed to bring up a jobqueue runner client, which
	// should contain 6 %s parts which will be replaced with the queue name,
	// scheduler group, deployment ip:host address of the server, reservation
	// time out and maximum number of minutes allowed, eg.
	// "my_jobqueue_runner_client --queue %s --group '%s' --deployment %s
	// --server '%s' --reserve_timeout %d --max_mins %d". If you supply an empty
	// string (the default), runner clients will not be spawned; for any work to
	// be done you will have to run your runner client yourself manually.
	RunnerCmd string

	// Absolute path to where the database file should be saved. The database is
	// used to ensure no loss of added commands, to keep a permanent history of
	// all jobs completed, and to keep various stats, amongst other things.
	DBFile string

	// Absolute path to where the database file should be backed up to.
	DBFileBackup string

	// Name of the deployment ("development" or "production"); development
	// databases are deleted and recreated on start up by default.
	Deployment string
}

// Serve is for use by a server executable and makes it start listening on
// localhost at the configured port for Connect()ions from clients, and then
// handles those clients. It returns a *Server that you will typically call
// Block() on to block until until your executable receives a SIGINT or SIGTERM,
// or you call Stop(), at which point the queues will be safely closed (you'd
// probably just exit at that point). The possible errors from Serve() will be
// related to not being able to start up at the supplied address; errors
// encountered while dealing with clients are logged but otherwise ignored. If
// it creates a db file or recreates one from backup, it will say what it did in
// the returned msg string. It also spawns your runner clients as needed,
// running them via the configured job scheduler, using the configured shell. It
// determines the command line to execute for your runner client from the
// configured RunnerCmd string you supplied.
func Serve(config ServerConfig) (s *Server, msg string, err error) {
	// port string, webPort string, schedulerName string, shell string, runnerCmd string, dbFile string, dbBkFile string, deployment string
	sock, err := rep.NewSocket()
	if err != nil {
		return
	}

	// we open ourselves up to possible denial-of-service attack if a client
	// sends us tons of data, but at least the client doesn't silently hang
	// forever when it legitimately wants to Add() a ton of jobs
	// unlimited Recv() length
	if err = sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return
	}

	// we use raw mode, allowing us to respond to multiple clients in
	// parallel
	if err = sock.SetOption(mangos.OptionRaw, true); err != nil {
		return
	}

	// we'll wait ServerInterruptTime to recv from clients before trying again,
	// allowing us to check if signals have been passed
	if err = sock.SetOption(mangos.OptionRecvDeadline, ServerInterruptTime); err != nil {
		return
	}

	sock.AddTransport(tcp.NewTransport())

	if err = sock.Listen("tcp://0.0.0.0:" + config.Port); err != nil {
		return
	}

	// serving will happen in a goroutine that will stop on SIGINT or SIGTERM,
	// of if something is sent on the quit channel
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	stop := make(chan bool, 1)
	done := make(chan error, 1)

	// if we end up spawning clients on other machines, they'll need to know
	// our non-loopback ip address so they can connect to us
	ip := CurrentIP()
	if ip == "" {
		err = Error{"", "Serve", "", ErrNoHost}
		return
	}

	// to be friendly we also record the hostname, but it's possible this isn't
	// defined, hence we don't rely on it for anything important
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}

	// we will spawn runner clients via the requested job scheduler
	sch, err := scheduler.New(config.SchedulerName, config.SchedulerConfig)
	if err != nil {
		return
	}

	// we need to persist stuff to disk, and we do so using boltdb
	db, msg, err := initDB(config.DBFile, config.DBFileBackup, config.Deployment)
	if err != nil {
		return
	}

	s = &Server{
		ServerInfo:   &ServerInfo{Addr: ip + ":" + config.Port, Host: host, Port: config.Port, WebPort: config.WebPort, PID: os.Getpid(), Deployment: config.Deployment, Scheduler: config.SchedulerName, Mode: ServerModeNormal},
		sock:         sock,
		ch:           new(codec.BincHandle),
		qs:           make(map[string]*queue.Queue),
		rpl:          &rgToKeys{lookup: make(map[string]map[string]bool)},
		db:           db,
		stop:         stop,
		done:         done,
		up:           true,
		scheduler:    sch,
		sgroupcounts: make(map[string]int),
		sgrouptrigs:  make(map[string]int),
		sgtr:         make(map[string]*scheduler.Requirements),
		rc:           config.RunnerCmd,
		statusCaster: bcast.NewGroup(),
	}

	// if we're restarting from a state where there were incomplete jobs, we
	// need to load those in to the appropriate queues now
	priorJobs, err := db.recoverIncompleteJobs()
	if err != nil {
		return
	}
	if len(priorJobs) > 0 {
		jobsByQueue := make(map[string][]*queue.ItemDef)
		for _, job := range priorJobs {
			jobsByQueue[job.Queue] = append(jobsByQueue[job.Queue], &queue.ItemDef{Key: job.key(), Data: job, Priority: job.Priority, Delay: 0 * time.Second, TTR: ServerItemTTR, Dependencies: job.Dependencies.incompleteJobKeys(s.db)})
		}
		for qname, itemdefs := range jobsByQueue {
			q := s.getOrCreateQueue(qname)
			_, _, err = s.enqueueItems(q, itemdefs)
			if err != nil {
				return
			}
		}
	}

	// set up responding to command-line clients and signals
	go func() {
		// log panics and die
		defer s.logPanic("jobqueue serving", true)

		for {
			select {
			case sig := <-sigs:
				s.shutdown()
				var serr error
				switch sig {
				case os.Interrupt:
					serr = Error{"", "Serve", "", ErrClosedInt}
				case syscall.SIGTERM:
					serr = Error{"", "Serve", "", ErrClosedTerm}
				}
				signal.Stop(sigs)
				done <- serr
				return
			case <-stop:
				s.shutdown()
				signal.Stop(sigs)
				done <- Error{"", "Serve", "", ErrClosedStop}
				return
			default:
				// receive a clientRequest from a client
				m, rerr := sock.RecvMsg()
				if rerr != nil {
					if rerr != mangos.ErrRecvTimeout {
						log.Println(rerr)
					}
					continue
				}

				// parse the request, do the desired work and respond to the client
				go func() {
					// log panics and continue
					defer s.logPanic("jobqueue server client handling", false)

					herr := s.handleRequest(m)
					if ServerLogClientErrors && herr != nil {
						log.Println(herr)
					}
				}()
			}
		}
	}()

	// set up the web interface
	go func() {
		// log panics and die
		defer s.logPanic("jobqueue web server", true)

		mux := http.NewServeMux()
		mux.HandleFunc("/", webInterfaceStatic)
		mux.HandleFunc("/status_ws", webInterfaceStatusWS(s))
		go http.ListenAndServe("0.0.0.0:"+config.WebPort, mux) // *** should use ListenAndServeTLS, which needs certs (http package has cert creation)...
		go s.statusCaster.Broadcasting(0)
	}()

	return
}

// Block makes you block while the server does the job of serving clients. This
// will return with an error indicating why it stopped blocking, which will
// be due to receiving a signal or because you called Stop()
func (s *Server) Block() (err error) {
	s.blocking = true
	err = <-s.done
	s.db.close() //*** do one last backup?
	s.up = false
	s.blocking = false
	return
}

// Stop will cause a graceful shut down of the server.
func (s *Server) Stop() (err error) {
	if s.up {
		s.stop <- true // results in shutdown()
		if !s.blocking {
			err = <-s.done
			s.db.close()
			s.up = false
		}
	}
	return
}

// Drain will stop the server spawning new runners and stop Reserve*() from
// returning any more Jobs. Once all current runners exit, we Stop().
func (s *Server) Drain() (err error) {
	if s.up {
		if !s.drain {
			s.drain = true
			s.ServerInfo.Mode = ServerModeDrain
			go func() {
				ticker := time.NewTicker(1 * time.Second)
			TICKS:
				for {
					select {
					case <-ticker.C:
						// check our queues for things running, which is cheap
						for _, q := range s.qs {
							stats := q.Stats()
							if stats.Running > 0 {
								continue TICKS
							}
						}

						// now that we think nothing should be running, wait
						// for the runner clients to exit so the job scheduler
						// will be nice and clean
						if !s.HasRunners() {
							ticker.Stop()
							s.Stop()
							return
						}
					}
				}
			}()
		}
	} else {
		err = Error{"", "Drain", "", ErrNoServer}
	}
	return
}

// GetServerStats returns basic info about the server along with some simple
// live stats about what's happening in the server's queues.
func (s *Server) GetServerStats() *ServerStats {
	var delayed, ready, running, buried int
	var etc time.Time

	for _, q := range s.qs {
		stats := q.Stats()
		delayed += stats.Delayed
		ready += stats.Ready
		buried += stats.Buried

		for _, inter := range q.GetRunningData() {
			running++

			// work out when this Job is going to end, and update etc if later
			job := inter.(*Job)
			if !job.starttime.IsZero() && job.Time.Seconds() > 0 {
				endTime := job.starttime.Add(job.Time)
				if endTime.After(etc) {
					etc = endTime
				}
			}
		}
	}

	return &ServerStats{ServerInfo: s.ServerInfo, Delayed: delayed, Ready: ready, Running: running, Buried: buried, ETC: etc.Truncate(time.Minute).Sub(time.Now().Truncate(time.Minute))}
}

// HasRunners tells you if there are currently runner clients in the job
// scheduler (either running or pending).
func (s *Server) HasRunners() bool {
	return s.scheduler.Busy()
}

// getOrCreateQueue returns a queue with the given name, creating one if we
// hadn't already done so.
func (s *Server) getOrCreateQueue(qname string) *queue.Queue {
	s.Lock()
	q, existed := s.qs[qname]
	if !existed {
		q = queue.New(qname)
		s.qs[qname] = q

		// we set a callback for things entering this queue's ready sub-queue.
		// This function will be called in a go routine and receives a slice of
		// all the ready jobs. Based on the requirements, we add to each job a
		// schedulerGroup, which the runners we spawn will be able to pass to
		// ReserveFiltered so that they run the correct jobs for the machine and
		// resource reservations the job scheduler will run them under
		q.SetReadyAddedCallback(func(queuename string, allitemdata []interface{}) {
			if s.drain {
				return
			}

			// calculate, set and count jobs by schedulerGroup
			groups := make(map[string]int)
			recMBs := make(map[string]int)
			recSecs := make(map[string]int)
			noRecGroups := make(map[string]bool)
			for _, inter := range allitemdata {
				job := inter.(*Job)
				// depending on job.Override, get memory and time
				// recommendations, which are rounded to get fewer larger
				// groups
				noRec := false
				if job.Override != 2 {
					var recm int
					var done bool
					var err error
					if recm, done = recMBs[job.ReqGroup]; !done {
						recm, err = s.db.recommendedReqGroupMemory(job.ReqGroup)
						if err == nil {
							recMBs[job.ReqGroup] = recm
						}
					}
					if recm == 0 {
						recm = job.RAM
						noRec = true
					}

					var recs int
					if recs, done = recSecs[job.ReqGroup]; !done {
						recs, err = s.db.recommendedReqGroupTime(job.ReqGroup)
						if err == nil {
							recSecs[job.ReqGroup] = recs
						}
					}
					if recs == 0 {
						recs = int(job.Time.Seconds())
						noRec = true
					}

					if job.Override == 1 {
						if recm > job.RAM {
							job.RAM = recm
						}
						if recs > int(job.Time.Seconds()) {
							job.Time = time.Duration(recs) * time.Second
						}
					} else {
						job.RAM = recm
						job.Time = time.Duration(recs) * time.Second
					}
				}

				// our req will be job memory + 100 to allow some leeway in
				// case the job scheduler calculates used memory differently,
				// and for other memory usage vagaries
				req := &scheduler.Requirements{
					RAM:   job.RAM + 100,
					Time:  job.Time,
					Cores: job.Cores,
					Disk:  0,
					Other: "", // *** how to pass though scheduler extra args?
				}
				job.schedulerGroup = fmt.Sprintf("%d:%.0f:%d", req.RAM, req.Time.Minutes(), req.Cores)

				if s.rc != "" {
					groups[job.schedulerGroup]++

					if noRec {
						noRecGroups[job.schedulerGroup] = true
					}

					s.sgcmutex.Lock()
					if _, set := s.sgtr[job.schedulerGroup]; !set {
						s.sgtr[job.schedulerGroup] = req
					}
					s.sgcmutex.Unlock()
				}
			}

			if s.rc != "" {
				// schedule runners for each group in the job scheduler
				for group, count := range groups {
					// we also keep a count of how many we request for this
					// group, so that when we Archive() or Bury() we can
					// decrement the count and re-call Schedule() to get rid
					// of no-longer-needed pending runners in the job
					// scheduler
					s.sgcmutex.Lock()
					s.sgroupcounts[group] = count
					s.sgcmutex.Unlock()
					s.scheduleRunners(q, group)

					if _, noRec := noRecGroups[group]; noRec && count > 100 {
						s.sgrouptrigs[group] = 0
					}
				}

				// clear out groups we no longer need
				for group := range s.sgroupcounts {
					if _, needed := groups[group]; !needed {
						s.clearSchedulerGroup(group, q)
					}
				}
				for group := range s.sgrouptrigs {
					if _, needed := groups[group]; !needed {
						delete(s.sgrouptrigs, group)
					}
				}
			}
		})

		// we set a callback for things changing in the queue, which lets us
		// update the status webpage with the minimal work and data transfer
		q.SetChangedCallback(func(from string, to string, data []interface{}) {
			if to == "removed" {
				// things are removed from the queue if deleted or completed;
				// disambiguate
				to = "deleted"
				for _, inter := range data {
					job := inter.(*Job)
					if job.State == "complete" {
						to = "complete"
						break
					}
				}
			}

			// overall count
			s.statusCaster.Send(&jstateCount{"+all+", from, to, len(data)})

			// counts per RepGroup
			groups := make(map[string]int)
			for _, inter := range data {
				job := inter.(*Job)
				groups[job.RepGroup]++
			}
			for group, count := range groups {
				s.statusCaster.Send(&jstateCount{group, from, to, count})
			}
		})
	}
	s.Unlock()

	return q
}

// enqueueItems adds new items to a queue, for when we have new jobs to handle.
func (s *Server) enqueueItems(q *queue.Queue, itemdefs []*queue.ItemDef) (added int, dups int, err error) {
	added, dups, err = q.AddMany(itemdefs)
	if err != nil {
		return
	}

	// add to our lookup of job RepGroup to key
	s.rpl.Lock()
	for _, itemdef := range itemdefs {
		rp := itemdef.Data.(*Job).RepGroup
		if _, exists := s.rpl.lookup[rp]; !exists {
			s.rpl.lookup[rp] = make(map[string]bool)
		}
		s.rpl.lookup[rp][itemdef.Key] = true
	}
	s.rpl.Unlock()

	return
}

// getJobsByKeys gets jobs with the given keys (current and complete)
func (s *Server) getJobsByKeys(q *queue.Queue, keys []string, getStd bool, getEnv bool) (jobs []*Job, srerr string, qerr string) {
	var notfound []string
	for _, jobkey := range keys {
		// try and get the job from the in-memory queue
		item, err := q.Get(jobkey)
		var job *Job
		if err == nil && item != nil {
			job = s.itemToJob(item, getStd, getEnv)
		} else {
			notfound = append(notfound, jobkey)
		}

		if job != nil {
			jobs = append(jobs, job)
		}
	}

	if len(notfound) > 0 {
		// try and get the jobs from the permanent store
		found, err := s.db.retrieveCompleteJobsByKeys(notfound, getStd, getEnv)
		if err != nil {
			srerr = ErrDBError
			qerr = err.Error()
		} else if len(found) > 0 {
			jobs = append(jobs, found...)
		}
	}

	return
}

// getJobsByRepGroup gets jobs in the given group (current and complete)
func (s *Server) getJobsByRepGroup(q *queue.Queue, repgroup string, limit int, state string, getStd bool, getEnv bool) (jobs []*Job, srerr string, qerr string) {
	// look in the in-memory queue for matching jobs
	s.rpl.RLock()
	for key := range s.rpl.lookup[repgroup] {
		item, err := q.Get(key)
		if err == nil && item != nil {
			job := s.itemToJob(item, false, false)
			jobs = append(jobs, job)
		}
	}
	s.rpl.RUnlock()

	// look in the permanent store for matching jobs
	if state == "" || state == "complete" {
		var complete []*Job
		complete, srerr, qerr = s.getCompleteJobsByRepGroup(repgroup)
		if len(complete) > 0 {
			// a job is stored in the db with only the single most recent
			// RepGroup it had, but we're able to retrieve jobs based on any of
			// the RepGroups it ever had; set the RepGroup to the one the user
			// requested *** may want to change RepGroup to store a slice of
			// RepGroups? But that could be massive...
			for _, cj := range complete {
				cj.RepGroup = repgroup
			}
			jobs = append(jobs, complete...)
		}
	}

	if limit > 0 || state != "" {
		jobs = s.limitJobs(jobs, limit, state, getStd, getEnv)
	}

	return
}

// getCompleteJobsByRepGroup gets complete jobs in the given group
func (s *Server) getCompleteJobsByRepGroup(repgroup string) (jobs []*Job, srerr string, qerr string) {
	jobs, err := s.db.retrieveCompleteJobsByRepGroup(repgroup)
	if err != nil {
		srerr = ErrDBError
		qerr = err.Error()
	}
	return
}

// getJobsCurrent gets all current (incomplete) jobs
func (s *Server) getJobsCurrent(q *queue.Queue, limit int, state string, getStd bool, getEnv bool) (jobs []*Job) {
	for _, item := range q.AllItems() {
		jobs = append(jobs, s.itemToJob(item, false, false))
	}

	if limit > 0 || state != "" {
		jobs = s.limitJobs(jobs, limit, state, getStd, getEnv)
	}

	return
}

// limitJobs handles the limiting of jobs for getJobsByRepGroup() and
// getJobsCurrent(). States 'reserved' and 'running' are treated as the same
// state.
func (s *Server) limitJobs(jobs []*Job, limit int, state string, getStd bool, getEnv bool) (limited []*Job) {
	groups := make(map[string][]*Job)
	for _, job := range jobs {
		jState := job.State
		if jState == "running" {
			jState = "reserved"
		}

		if state != "" {
			if state == "running" {
				state = "reserved"
			}
			if jState != state {
				continue
			}
		}

		if limit == 0 {
			limited = append(limited, job)
		} else {
			group := fmt.Sprintf("%s.%d.%s", jState, job.Exitcode, job.FailReason)
			jobs, existed := groups[group]
			if existed {
				lenj := len(jobs)
				if lenj == limit {
					jobs[lenj-1].Similar++
				} else {
					jobs = append(jobs, job)
					groups[group] = jobs
				}
			} else {
				jobs = []*Job{job}
				groups[group] = jobs
			}
		}
	}

	if limit > 0 {
		for _, jobs := range groups {
			limited = append(limited, jobs...)
		}

		if limit <= 5 {
			for _, job := range limited {
				s.jobPopulateStdEnv(job, getStd, getEnv)
			}
		}
	}
	return
}

func (s *Server) scheduleRunners(q *queue.Queue, group string) {
	if s.rc == "" {
		return
	}

	s.sgcmutex.Lock()
	req, hadreq := s.sgtr[group]
	if !hadreq {
		s.sgcmutex.Unlock()
		return
	}

	doClear := false
	groupCount := s.sgroupcounts[group]
	if groupCount < 0 {
		s.sgroupcounts[group] = 0
		groupCount = 0
		doClear = true
	}
	s.sgcmutex.Unlock()

	if !doClear {
		err := s.scheduler.Schedule(fmt.Sprintf(s.rc, q.Name, group, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.scheduler.ReserveTimeout(), int(s.scheduler.MaxQueueTime(req).Minutes())), req, groupCount)
		if err != nil {
			problem := true
			if serr, ok := err.(scheduler.Error); ok && serr.Err == scheduler.ErrImpossible {
				// bury all jobs in this scheduler group
				problem = false
				rf := func(data interface{}) bool {
					job := data.(*Job)
					if job.schedulerGroup == group {
						return true
					}
					return false
				}
				s.sgcmutex.Lock()
				for {
					item, err := q.ReserveFiltered(rf)
					if err != nil {
						problem = true
						break
					}
					if item == nil {
						break
					}
					job := item.Data.(*Job)
					job.FailReason = FailReasonResource
					q.Bury(item.Key)
					s.sgroupcounts[group]--
				}
				s.sgcmutex.Unlock()
				if !problem {
					doClear = true
				}
			}

			if problem {
				// log the error *** and inform (by email) the user about this
				// problem if it's persistent, once per hour (day?)
				log.Println(err)

				// retry the schedule in a while
				go func() {
					<-time.After(1 * time.Minute)
					s.scheduleRunners(q, group)
				}()
				return
			}
		}
	}

	if doClear {
		//log.Printf("group [%s] count dropped to 0, will clear\n", group)
		s.clearSchedulerGroup(group, q)
	}
}

// adjust our count of how many jobs with this schedulerGroup we need in the job
// scheduler.
func (s *Server) decrementGroupCount(schedulerGroup string, q *queue.Queue) {
	if s.rc != "" {
		doSchedule := false
		doTrigger := false
		s.sgcmutex.Lock()
		if _, existed := s.sgroupcounts[schedulerGroup]; existed {
			s.sgroupcounts[schedulerGroup]--
			doSchedule = true
			//log.Printf("decremented group [%s] to %d\n", schedulerGroup, s.sgroupcounts[schedulerGroup])
			if count, set := s.sgrouptrigs[schedulerGroup]; set {
				s.sgrouptrigs[schedulerGroup]++
				if count >= 100 {
					s.sgrouptrigs[schedulerGroup] = 0
					if s.sgroupcounts[schedulerGroup] > 10 {
						doTrigger = true
					}
				}
			}
		}
		s.sgcmutex.Unlock()

		if doTrigger {
			// we most likely have completed 100 more jobs for this group, so
			// we'll trigger our ready callback which will re-calculate the
			// best resource requirements for the remaining jobs in the group
			// and then call scheduleRunners
			q.TriggerReadyAddedCallback()
		} else if doSchedule {
			// notify the job scheduler we need less jobs for this job's cmd now;
			// it will remove extraneous ones from its queue
			s.scheduleRunners(q, schedulerGroup)
		}
	}
}

// when we no longer need a schedulerGroup in the job scheduler, clean up and
// make sure the job scheduler knows we don't need any runners for this group.
func (s *Server) clearSchedulerGroup(schedulerGroup string, q *queue.Queue) {
	if s.rc != "" {
		s.sgcmutex.Lock()
		req, hadreq := s.sgtr[schedulerGroup]
		if !hadreq {
			s.sgcmutex.Unlock()
			return
		}
		delete(s.sgroupcounts, schedulerGroup)
		delete(s.sgrouptrigs, schedulerGroup)
		delete(s.sgtr, schedulerGroup)
		s.sgcmutex.Unlock()
		//log.Printf("telling scheduler we need 0 for group [%s]\n", schedulerGroup)
		s.scheduler.Schedule(fmt.Sprintf(s.rc, q.Name, schedulerGroup, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.scheduler.ReserveTimeout(), int(s.scheduler.MaxQueueTime(req).Minutes())), req, 0)
	}
}

// shutdown stops listening to client connections, close all queues and
// persists them to disk.
func (s *Server) shutdown() {
	s.sock.Close()
	s.db.close()
	s.scheduler.Cleanup()

	//*** we want to persist production queues to disk
	//*** want to do db backup; in cloud mode we want to copy backup to local
	// deploy client that spawned us, and also to s3

	// clean up our queues and empty everything out to be garbage collected,
	// in case the same process calls Serve() again after this
	for _, q := range s.qs {
		q.Destroy()
	}
	s.qs = nil
}

// logPanic is for (ideally temporary) use in a go routine, deferred at the
// start of it, to figure out what is causing runtime panics that are killing
// the server. If the die bool is true, the program exits, otherwise it
// continues, after logging the error message and stack trace (to whatever
// log.SetOutput is set to). Desc string should be used to describe briefly what
// the goroutine you call this in does.
func (s *Server) logPanic(desc string, die bool) {
	if err := recover(); err != nil {
		log.Printf("internal error in %s: %s\n%s\n", desc, err, debug.Stack())
		if die {
			os.Exit(1)
		}
	}
}
