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

package jobqueue

// This file contains the functions to implement a jobqueue server.

import (
	"fmt"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/rep"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/grafov/bcast" // *** must be commit e9affb593f6c871f9b4c3ee6a3c77d421fe953df or status web page updates break in certain cases
	"github.com/ugorji/go/codec"
	"io"
	"log"
	"net"
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
	ErrWrongUser      = "you did not start this server: permission denied"
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
	ServerCheckRunnerTime = 1 * time.Minute
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
	DB      []byte
}

// ServerInfo holds basic addressing info about the server.
type ServerInfo struct {
	AllowedUsers []string // usernames that are allowed to use the server
	Addr         string   // ip:port
	Host         string   // hostname
	Port         string   // port
	WebPort      string   // port of the web interface
	PID          int      // process id of server
	Deployment   string   // deployment the server is running under
	Scheduler    string   // the name of the scheduler that jobs are being submitted to
	Mode         string   // ServerModeNormal if the server is running normally, or ServerModeDrain if draining
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
	FromState JobState
	ToState   JobState
	Count     int // num in FromState drop by this much, num in ToState rise by this much
}

// badServer is the details of servers that have gone bad that we send to the
// status webpage. Previously bad servers can also be sent if they become good
// again, hence the IsBad boolean.
type badServer struct {
	ID    string
	Name  string
	IP    string
	Date  int64 // seconds since Unix epoch
	IsBad bool
}

// Server represents the server side of the socket that clients Connect() to.
type Server struct {
	ServerInfo   *ServerInfo
	allowedUsers map[string]bool
	sock         mangos.Socket
	ch           codec.Handle
	db           *db
	done         chan error
	stop         chan bool
	up           bool
	drain        bool
	blocking     bool
	sync.Mutex
	qs              map[string]*queue.Queue
	rpl             *rgToKeys
	scheduler       *scheduler.Scheduler
	sgroupcounts    map[string]int
	sgrouptrigs     map[string]int
	sgtr            map[string]*scheduler.Requirements
	sgcmutex        sync.Mutex
	racmutex        sync.RWMutex
	rc              string // runner command string compatible with fmt.Sprintf(..., queueName, schedulerGroup, deployment, serverAddr, reserveTimeout, maxMinsAllowed)
	httpServer      *http.Server
	statusCaster    *bcast.Group
	badServerCaster *bcast.Group
	racCheckTimer   *time.Timer
	racChecking     bool
	racCheckReady   int
	bsmutex         sync.RWMutex
	badServers      map[string]*cloud.Server
}

// ServerConfig is supplied to Serve() to configure your jobqueue server. All
// fields are required with no working default unless otherwise noted.
type ServerConfig struct {
	// AllowedUsers are the usernames that will be allowed access to
	// the user interfaces that will connect to the server. (In cloud situations
	// this isn't necessarily just the username of the account that starts the
	// server, though that user is always allowed access, regardless of this
	// value.)
	AllowedUsers []string

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

	// CIDR is the IP address range of your network. When the server needs to
	// know its own IP address, it uses this CIDR to confirm it got it correct
	// (ie. it picked the correct network interface). You can leave this unset,
	// in which case it will do its best to pick correctly. (This is only a
	// possible issue if you have multiple network interfaces.)
	CIDR string
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
	// for security purposes we need to know who will be allowed to access us
	// in the future
	owner, err := internal.Username()
	if err != nil {
		return
	}
	var allowedUsers []string
	allowedUsersMap := make(map[string]bool)
	for _, user := range config.AllowedUsers {
		allowedUsersMap[user] = true
		allowedUsers = append(allowedUsers, user)
	}
	if _, exists := allowedUsersMap[owner]; !exists {
		allowedUsersMap[owner] = true
		allowedUsers = append(allowedUsers, owner)
	}

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
	ip := CurrentIP(config.CIDR)
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
		ServerInfo:      &ServerInfo{AllowedUsers: allowedUsers, Addr: ip + ":" + config.Port, Host: host, Port: config.Port, WebPort: config.WebPort, PID: os.Getpid(), Deployment: config.Deployment, Scheduler: config.SchedulerName, Mode: ServerModeNormal},
		allowedUsers:    allowedUsersMap,
		sock:            sock,
		ch:              new(codec.BincHandle),
		qs:              make(map[string]*queue.Queue),
		rpl:             &rgToKeys{lookup: make(map[string]map[string]bool)},
		db:              db,
		stop:            stop,
		done:            done,
		up:              true,
		scheduler:       sch,
		sgroupcounts:    make(map[string]int),
		sgrouptrigs:     make(map[string]int),
		sgtr:            make(map[string]*scheduler.Requirements),
		rc:              config.RunnerCmd,
		statusCaster:    bcast.NewGroup(),
		badServerCaster: bcast.NewGroup(),
		badServers:      make(map[string]*cloud.Server),
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
			jobsByQueue[job.Queue] = append(jobsByQueue[job.Queue], &queue.ItemDef{Key: job.key(), ReserveGroup: job.getSchedulerGroup(), Data: job, Priority: job.Priority, Delay: 0 * time.Second, TTR: ServerItemTTR, Dependencies: job.Dependencies.incompleteJobKeys(s.db)})
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
	ready := make(chan bool)
	go func() {
		// log panics and die
		defer s.logPanic("jobqueue web server", true)

		mux := http.NewServeMux()
		mux.HandleFunc("/", webInterfaceStatic)
		mux.HandleFunc("/status_ws", webInterfaceStatusWS(s))
		cmdsQ := s.getOrCreateQueue("cmds")
		mux.HandleFunc(restJobsEndpoint, restJobs(s, cmdsQ))
		srv := &http.Server{Addr: "0.0.0.0:" + config.WebPort, Handler: mux}
		go srv.ListenAndServe() // *** should use ListenAndServeTLS, which needs certs (http package has cert creation)...
		s.httpServer = srv

		go s.statusCaster.Broadcasting(0)
		go s.badServerCaster.Broadcasting(0)

		badServerCB := func(server *cloud.Server) {
			s.bsmutex.Lock()
			s.badServers[server.ID] = server
			s.bsmutex.Unlock()
			s.badServerCaster.Send(&badServer{
				ID:    server.ID,
				Name:  server.Name,
				IP:    server.IP,
				Date:  time.Now().Unix(),
				IsBad: server.IsBad(),
			})
		}
		s.scheduler.SetBadServerCallBack(badServerCB)

		// wait a while for ListenAndServe() to start listening
		<-time.After(10 * time.Millisecond)
		ready <- true
	}()
	<-ready

	return
}

// Block makes you block while the server does the job of serving clients. This
// will return with an error indicating why it stopped blocking, which will
// be due to receiving a signal or because you called Stop()
func (s *Server) Block() (err error) {
	s.racmutex.Lock()
	s.blocking = true
	s.racmutex.Unlock()
	err = <-s.done
	s.racmutex.Lock()
	s.up = false
	s.blocking = false
	s.racmutex.Unlock()
	return
}

// Stop will cause a graceful shut down of the server. Supplying an optional
// bool of true will cause Stop() to wait until all runners have exited and
// the server is truly down before returning.
func (s *Server) Stop(wait ...bool) (err error) {
	s.racmutex.Lock()
	if s.up {
		s.stop <- true // results in shutdown()
		if !s.blocking {
			s.up = false
			s.racmutex.Unlock()
			err = <-s.done
		} else {
			s.racmutex.Unlock()
		}

		if len(wait) == 1 && wait[0] {
			ticker := time.NewTicker(100 * time.Millisecond)
			for {
				select {
				case <-ticker.C:
					if !s.HasRunners() {
						ticker.Stop()
						return
					}
				}
			}
		}
	} else {
		s.racmutex.Unlock()
	}
	return
}

// Drain will stop the server spawning new runners and stop Reserve*() from
// returning any more Jobs. Once all current runners exit, we Stop().
func (s *Server) Drain() (err error) {
	s.racmutex.Lock()
	defer s.racmutex.Unlock()
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
						s.racmutex.Lock()
						for _, q := range s.qs {
							stats := q.Stats()
							if stats.Running > 0 {
								s.racmutex.Unlock()
								continue TICKS
							}
						}
						s.racmutex.Unlock()
						ticker.Stop()

						// now that we think nothing should be running, get
						// Stop() to wait for the runner clients to exit so the
						// job scheduler will be nice and clean
						s.scheduler.Cleanup()
						s.Stop(true)
						return
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
			job.RLock()
			if !job.StartTime.IsZero() && job.Requirements.Time.Seconds() > 0 {
				endTime := job.StartTime.Add(job.Requirements.Time)
				if endTime.After(etc) {
					etc = endTime
				}
			}
			job.RUnlock()
		}
	}

	return &ServerStats{ServerInfo: s.ServerInfo, Delayed: delayed, Ready: ready, Running: running, Buried: buried, ETC: etc.Truncate(time.Minute).Sub(time.Now().Truncate(time.Minute))}
}

// BackupDB lets you do a manual live backup of the server's database to a given
// writer. Note that automatic backups occur to the configured location
// without calling this.
func (s *Server) BackupDB(w io.Writer) error {
	return s.db.backup(w)
}

// HasRunners tells you if there are currently runner clients in the job
// scheduler (either running or pending).
func (s *Server) HasRunners() bool {
	return s.scheduler.Busy()
}

// getOrCreateQueue returns a queue with the given name, creating one if we
// hadn't already done so.
func (s *Server) getOrCreateQueue(qname string) *queue.Queue {
	s.racmutex.Lock()
	defer s.racmutex.Unlock()
	if !s.up {
		return nil
	}

	q, existed := s.qs[qname]
	if !existed {
		q = queue.New(qname)
		s.qs[qname] = q

		// we set a callback for things entering this queue's ready sub-queue.
		// This function will be called in a go routine and receives a slice of
		// all the ready jobs. Based on the requirements, we add to each job a
		// schedulerGroup, which the runners we spawn will be able to pass to
		// Reserve() so that they run the correct jobs for the machine and
		// resource reservations the job scheduler will run them under
		q.SetReadyAddedCallback(func(queuename string, allitemdata []interface{}) {
			// we must only ever run this 1 at a time
			s.racmutex.Lock()
			defer s.racmutex.Unlock()

			if s.drain || !s.up {
				return
			}

			// calculate, set and count jobs by schedulerGroup
			groups := make(map[string]int)
			groupToReqs := make(map[string]*scheduler.Requirements)
			groupsScheduledCounts := make(map[string]int)
			noRecGroups := make(map[string]bool)
			for _, inter := range allitemdata {
				job := inter.(*Job)

				// depending on job.Override, get memory and time
				// recommendations, which are rounded to get fewer larger
				// groups
				noRec := false
				if job.Override != 2 {
					var recommendedReq *scheduler.Requirements
					if rec, existed := groupToReqs[job.ReqGroup]; existed {
						recommendedReq = rec
					} else {
						recm, _ := s.db.recommendedReqGroupMemory(job.ReqGroup)
						recs, _ := s.db.recommendedReqGroupTime(job.ReqGroup)
						if recm == 0 || recs == 0 {
							groupToReqs[job.ReqGroup] = nil
						} else {
							recommendedReq = &scheduler.Requirements{RAM: recm, Time: time.Duration(recs) * time.Second}
							groupToReqs[job.ReqGroup] = recommendedReq
						}
					}

					if recommendedReq != nil {
						if job.Override == 1 {
							if recommendedReq.RAM > job.Requirements.RAM {
								job.Requirements.RAM = recommendedReq.RAM
							}
							if recommendedReq.Time > job.Requirements.Time {
								job.Requirements.Time = recommendedReq.Time
							}
						} else {
							job.Requirements.RAM = recommendedReq.RAM
							job.Requirements.Time = recommendedReq.Time
						}
					} else {
						noRec = true
					}
				}

				var req *scheduler.Requirements
				if job.Requirements.RAM < 924 {
					// our req will be like the jobs but with memory + 100 to
					// allow some leeway in case the job scheduler calculates
					// used memory differently, and for other memory usage
					// vagaries
					req = &scheduler.Requirements{
						RAM:   job.Requirements.RAM + 100,
						Time:  job.Requirements.Time,
						Cores: job.Requirements.Cores,
						Disk:  job.Requirements.Disk,
						Other: job.Requirements.Other,
					}
				} else {
					req = job.Requirements
				}

				prevSchedGroup := job.getSchedulerGroup()
				schedulerGroup := req.Stringify()
				if prevSchedGroup != schedulerGroup {
					job.setSchedulerGroup(schedulerGroup)
					if prevSchedGroup != "" {
						job.setScheduledRunner(false)
					}
					if s.rc != "" {
						q.SetReserveGroup(job.key(), schedulerGroup)
					}
				}

				if s.rc != "" {
					if job.getScheduledRunner() {
						groupsScheduledCounts[schedulerGroup]++
					} else {
						job.setScheduledRunner(true)
					}
					groups[schedulerGroup]++

					if noRec {
						noRecGroups[schedulerGroup] = true
					}

					s.sgcmutex.Lock()
					if _, set := s.sgtr[schedulerGroup]; !set {
						s.sgtr[schedulerGroup] = req
					}
					s.sgcmutex.Unlock()
				}
			}

			if s.rc != "" {
				// clear out groups we no longer need
				s.sgcmutex.Lock()
				stillRunning := make(map[string]bool)
				for _, inter := range q.GetRunningData() {
					job := inter.(*Job)
					stillRunning[job.getSchedulerGroup()] = true
				}
				for group := range s.sgroupcounts {
					if _, needed := groups[group]; !needed && !stillRunning[group] {
						s.sgroupcounts[group] = 0
						go s.clearSchedulerGroup(group, q)
					}
				}
				for group := range s.sgrouptrigs {
					if _, needed := groups[group]; !needed && !stillRunning[group] {
						delete(s.sgrouptrigs, group)
					}
				}
				s.sgcmutex.Unlock()

				// schedule runners for each group in the job scheduler
				for group, count := range groups {
					// we also keep a count of how many we request for this
					// group, so that when we Archive() or Bury() we can
					// decrement the count and re-call Schedule() to get rid
					// of no-longer-needed pending runners in the job
					// scheduler
					s.sgcmutex.Lock()
					countIncRunning := count
					if s.sgroupcounts[group] > 0 {
						countIncRunning += s.sgroupcounts[group]
					}
					if groupsScheduledCounts[group] > 0 {
						countIncRunning -= groupsScheduledCounts[group]
					}
					s.sgroupcounts[group] = countIncRunning

					// if we got no resource requirement recommendations for
					// this group, we'll set up a retrigger of this ready
					// callback after 100 runners have been run
					if _, noRec := noRecGroups[group]; noRec && count > 100 {
						if _, existed := s.sgrouptrigs[group]; !existed {
							s.sgrouptrigs[group] = 0
						}
					}

					s.sgcmutex.Unlock()
					go s.scheduleRunners(q, group)
				}

				// in the event that the runners we spawn can't reach us
				// temporarily and just die, we need to make sure this callback
				// gets triggered again even if no new jobs get added
				s.racCheckReady = len(allitemdata)
				if s.racChecking {
					if !s.racCheckTimer.Stop() {
						<-s.racCheckTimer.C
					}
					s.racCheckTimer.Reset(ServerCheckRunnerTime)
				} else {
					s.racCheckTimer = time.NewTimer(ServerCheckRunnerTime)

					go func() {
						select {
						case <-s.racCheckTimer.C:
							s.racmutex.Lock()
							s.racChecking = false
							stats := q.Stats()
							s.racmutex.Unlock()

							if stats.Ready >= s.racCheckReady {
								q.TriggerReadyAddedCallback()
							}

							return
						}
					}()

					s.racChecking = true
				}
			}
		})

		// we set a callback for things changing in the queue, which lets us
		// update the status webpage with the minimal work and data transfer
		q.SetChangedCallback(func(fromQ, toQ queue.SubQueue, data []interface{}) {
			var from, to JobState
			if toQ == queue.SubQueueRemoved {
				// things are removed from the queue if deleted or completed;
				// disambiguate
				to = JobStateDeleted
				for _, inter := range data {
					job := inter.(*Job)
					job.RLock()
					jState := job.State
					job.RUnlock()
					if jState == JobStateComplete {
						to = JobStateComplete
						break
					}
				}
			} else {
				to = subqueueToJobState[toQ]
			}
			from = subqueueToJobState[fromQ]

			// calculate counts per RepGroup
			groups := make(map[string]int)
			for _, inter := range data {
				job := inter.(*Job)

				// if we change from running, mark that we have not scheduled a
				// runner for the job
				if from == JobStateRunning {
					job.setScheduledRunner(false)
				}

				groups[job.RepGroup]++
			}

			// send out the counts
			s.statusCaster.Send(&jstateCount{"+all+", from, to, len(data)})
			for group, count := range groups {
				s.statusCaster.Send(&jstateCount{group, from, to, count})
			}
		})

		// we set a callback for running items that hit their ttr because the
		// runner died or because of networking issues: we treat it like a
		// normal release, which means it becomes delayed unless it is out of
		// retries, in which case it gets buried
		q.SetTTRCallback(func(data interface{}) queue.SubQueue {
			job := data.(*Job)

			job.Lock()
			defer job.Unlock()
			if !job.StartTime.IsZero() && !job.Exited {
				job.UntilBuried--
				job.Exited = true
				job.Exitcode = -1
				job.FailReason = FailReasonRelease
				job.EndTime = time.Now()

				s.decrementGroupCount(job.schedulerGroup, q)

				if job.UntilBuried <= 0 {
					return queue.SubQueueBury
				}
			}

			return queue.SubQueueDelay
		})
	}

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

// createJobs creates new jobs, adding them to the database and the in-memory
// queue. It returns 2 errors; the first is one of our Err constant strings,
// the second is the actual error with more details.
func (s *Server) createJobs(q *queue.Queue, inputJobs []*Job, envkey string, ignoreComplete bool) (added, dups, alreadyComplete int, srerr string, qerr error) {
	// create itemdefs for the jobs
	for _, job := range inputJobs {
		job.Lock()
		job.EnvKey = envkey
		job.UntilBuried = job.Retries + 1
		job.Queue = q.Name
		if s.rc != "" {
			job.schedulerGroup = job.Requirements.Stringify()
		}
		job.Unlock()

		// in cloud deployments we may bring up a server running an operating
		// system with a different username, which we must allow access to
		// ourselves
		if user, set := job.Requirements.Other["cloud_user"]; set {
			if _, allowed := s.allowedUsers[user]; !allowed {
				s.allowedUsers[user] = true
				s.ServerInfo.AllowedUsers = append(s.ServerInfo.AllowedUsers, user)
			}
		}
	}

	// keep an on-disk record of these new jobs; we sacrifice a lot of speed by
	// waiting on this database write to persist to disk. The alternative would
	// be to return success to the client as soon as the jobs were in the in-
	// memory queue, then lazily persist to disk in a goroutine, but we must
	// guarantee that jobs are never lost or a workflow could hopelessly break
	// if the server node goes down between returning success and the write to
	// disk succeeding. (If we don't return success to the client, it won't
	// Remove the job that created the new jobs from the queue and when we
	// recover, at worst the creating job will be run again - no jobs get lost.)
	jobsToQueue, jobsToUpdate, alreadyComplete, err := s.db.storeNewJobs(inputJobs, ignoreComplete)
	if err != nil {
		srerr = ErrDBError
		qerr = err
	} else {
		// now that jobs are in the db we can get dependencies fully, so now we
		// can build our itemdefs *** we really need to test for cycles, because
		// if the user creates one, we won't let them delete the bad jobs!
		// storeNewJobs() returns jobsToQueue, which is all of cr.Jobs plus any
		// previously Archive()d jobs that were resurrected because of one of
		// their DepGroup dependencies being in cr.Jobs
		var itemdefs []*queue.ItemDef
		for _, job := range jobsToQueue {
			itemdefs = append(itemdefs, &queue.ItemDef{Key: job.key(), ReserveGroup: job.getSchedulerGroup(), Data: job, Priority: job.Priority, Delay: 0 * time.Second, TTR: ServerItemTTR, Dependencies: job.Dependencies.incompleteJobKeys(s.db)})
		}

		// storeNewJobs also returns jobsToUpdate, which are those jobs
		// currently in the queue that need their dependencies updated because
		// they just changed when we stored cr.Jobs
		for _, job := range jobsToUpdate {
			thisErr := q.Update(job.key(), job.getSchedulerGroup(), job, job.Priority, 0*time.Second, ServerItemTTR, job.Dependencies.incompleteJobKeys(s.db))
			if thisErr != nil {
				qerr = thisErr
				break
			}
		}

		if qerr != nil {
			srerr = ErrInternalError
		} else {
			// add the jobs to the in-memory job queue
			added, dups, qerr = s.enqueueItems(q, itemdefs)
			if qerr != nil {
				srerr = ErrInternalError
			}
		}
	}

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
func (s *Server) getJobsByRepGroup(q *queue.Queue, repgroup string, limit int, state JobState, getStd bool, getEnv bool) (jobs []*Job, srerr string, qerr string) {
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
	if state == "" || state == JobStateComplete {
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

	if limit > 0 || state != "" || getStd || getEnv {
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
func (s *Server) getJobsCurrent(q *queue.Queue, limit int, state JobState, getStd bool, getEnv bool) (jobs []*Job) {
	for _, item := range q.AllItems() {
		jobs = append(jobs, s.itemToJob(item, false, false))
	}

	if limit > 0 || state != "" || getStd || getEnv {
		jobs = s.limitJobs(jobs, limit, state, getStd, getEnv)
	}

	return
}

// limitJobs handles the limiting of jobs for getJobsByRepGroup() and
// getJobsCurrent(). States 'reserved' and 'running' are treated as the same
// state.
func (s *Server) limitJobs(jobs []*Job, limit int, state JobState, getStd bool, getEnv bool) (limited []*Job) {
	groups := make(map[string][]*Job)
	for _, job := range jobs {
		job.RLock()
		jState := job.State
		jExitCode := job.Exitcode
		jFailReason := job.FailReason
		job.RUnlock()
		if jState == JobStateRunning {
			jState = JobStateReserved
		}

		if state != "" {
			if state == JobStateRunning {
				state = JobStateReserved
			}
			if jState != state {
				continue
			}
		}

		if limit == 0 {
			limited = append(limited, job)
		} else {
			group := fmt.Sprintf("%s.%d.%s", jState, jExitCode, jFailReason)
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
	}

	if getEnv || getStd {
		for _, job := range limited {
			s.jobPopulateStdEnv(job, getStd, getEnv)
		}
	}

	return
}

func (s *Server) scheduleRunners(q *queue.Queue, group string) {
	s.racmutex.RLock()
	rc := s.rc
	s.racmutex.RUnlock()
	if rc == "" {
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
	if groupCount <= 0 {
		s.sgroupcounts[group] = 0
		doClear = true
	}
	s.sgcmutex.Unlock()

	if !doClear {
		err := s.scheduler.Schedule(fmt.Sprintf(rc, q.Name, group, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.scheduler.ReserveTimeout(), int(s.scheduler.MaxQueueTime(req).Minutes())), req, groupCount)
		if err != nil {
			problem := true
			if serr, ok := err.(scheduler.Error); ok && serr.Err == scheduler.ErrImpossible {
				// bury all jobs in this scheduler group
				problem = false
				s.sgcmutex.Lock()
				for {
					item, err := q.Reserve(group)
					if err != nil {
						problem = true
						break
					}
					if item == nil {
						break
					}
					job := item.Data.(*Job)
					job.Lock()
					job.FailReason = FailReasonResource
					job.Unlock()
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
			if count, set := s.sgrouptrigs[schedulerGroup]; set {
				s.sgrouptrigs[schedulerGroup]++
				if count >= 100 {
					delete(s.sgrouptrigs, schedulerGroup)
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
		s.scheduler.Schedule(fmt.Sprintf(s.rc, q.Name, schedulerGroup, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.scheduler.ReserveTimeout(), int(s.scheduler.MaxQueueTime(req).Minutes())), req, 0)
	}
}

// shutdown stops listening to client connections, close all queues and
// persists them to disk.
func (s *Server) shutdown() {
	s.Lock()
	s.sock.Close()
	s.db.close()
	s.scheduler.Cleanup()
	s.httpServer.Shutdown(nil)

	// wait until the ports are really no longer being listened to (which isn't
	// the same as them being available to be reconnected to, but this is the
	// best we can do?)
	for {
		conn, _ := net.DialTimeout("tcp", net.JoinHostPort("", s.ServerInfo.Port), 10*time.Millisecond)
		if conn != nil {
			conn.Close()
			continue
		}
		conn, _ = net.DialTimeout("tcp", net.JoinHostPort("", s.ServerInfo.WebPort), 10*time.Millisecond)
		if conn != nil {
			conn.Close()
			continue
		}
		break
	}
	s.Unlock()

	// clean up our queues and empty everything out to be garbage collected,
	// in case the same process calls Serve() again after this
	s.racmutex.Lock()
	defer s.racmutex.Unlock()
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
