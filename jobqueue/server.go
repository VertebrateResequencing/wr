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

package jobqueue

// This file contains the functions to implement a jobqueue server.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gorilla/websocket"
	"github.com/grafov/bcast" // *** must be commit e9affb593f6c871f9b4c3ee6a3c77d421fe953df or status web page updates break in certain cases
	"github.com/inconshreveable/log15"
	logext "github.com/inconshreveable/log15/ext"
	"github.com/ugorji/go/codec"
	"nanomsg.org/go-mangos"
	"nanomsg.org/go-mangos/protocol/rep"
	"nanomsg.org/go-mangos/transport/tlstcp"
)

// Err* constants are found in our returned Errors under err.Err, so you can
// cast and check if it's a certain type of error. ServerMode* constants are
// used to report on the status of the server, found inside ServerInfo.
const (
	ErrInternalError    = "internal error"
	ErrUnknownCommand   = "unknown command"
	ErrBadRequest       = "bad request (missing arguments?)"
	ErrBadJob           = "bad job (not in queue or correct sub-queue)"
	ErrMissingJob       = "corresponding job not found"
	ErrUnknown          = "unknown error"
	ErrClosedInt        = "queues closed due to SIGINT"
	ErrClosedTerm       = "queues closed due to SIGTERM"
	ErrClosedStop       = "queues closed due to manual Stop()"
	ErrQueueClosed      = "queue closed"
	ErrNoHost           = "could not determine the non-loopback ip address of this host"
	ErrNoServer         = "could not reach the server"
	ErrMustReserve      = "you must Reserve() a Job before passing it to other methods"
	ErrDBError          = "failed to use database"
	ErrPermissionDenied = "bad token: permission denied"
	ServerModeNormal    = "started"
	ServerModeDrain     = "draining"
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

// BsubID is used to give added jobs a unique (atomically incremented) id when
// pretending to be bsub.
var BsubID uint64

// Error records an error and the operation and item that caused it.
type Error struct {
	Op   string // name of the method
	Item string // the item's key
	Err  string // one of our Err* vars
}

func (e Error) Error() string {
	return "jobqueue " + e.Op + "(" + e.Item + "): " + e.Err
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
	Err        string // string instead of error so we can decode on the client side
	Added      int
	Existed    int
	KillCalled bool
	Job        *Job
	Jobs       []*Job
	SInfo      *ServerInfo
	SStats     *ServerStats
	DB         []byte
	Path       string
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
	Delayed int           // how many jobs are waiting following a possibly transient error
	Ready   int           // how many jobs are ready to begin running
	Running int           // how many jobs are currently running
	Buried  int           // how many jobs are no longer being processed because of seemingly permanent errors
	ETC     time.Duration // how long until the the slowest of the currently running jobs is expected to complete
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
	ID      string
	Name    string
	IP      string
	Date    int64 // seconds since Unix epoch
	IsBad   bool
	Problem string
}

// schedulerIssue is the details of scheduler problems encountered that we send
// to the status webpage.
type schedulerIssue struct {
	Msg       string
	FirstDate int64 // seconds since Unix epoch
	LastDate  int64
	Count     int // the number of identical Msg sent
}

// Server represents the server side of the socket that clients Connect() to.
type Server struct {
	ServerInfo         *ServerInfo
	token              []byte
	uploadDir          string
	sock               mangos.Socket
	ch                 codec.Handle
	db                 *db
	done               chan error
	stopSigHandling    chan bool
	stopClientHandling chan bool
	wg                 *sync.WaitGroup
	up                 bool
	drain              bool
	blocking           bool
	sync.Mutex
	q               *queue.Queue
	rpl             *rgToKeys
	scheduler       *scheduler.Scheduler
	sgroupcounts    map[string]int
	sgrouptrigs     map[string]int
	sgtr            map[string]*scheduler.Requirements
	sgcmutex        sync.Mutex
	racmutex        sync.RWMutex // to protect the readyaddedcallback
	rc              string       // runner command string compatible with fmt.Sprintf(..., schedulerGroup, deployment, serverAddr, reserveTimeout, maxMinsAllowed)
	httpServer      *http.Server
	statusCaster    *bcast.Group
	badServerCaster *bcast.Group
	schedCaster     *bcast.Group
	racCheckTimer   *time.Timer
	racChecking     bool
	racCheckReady   int
	wsmutex         sync.Mutex
	wsconns         map[string]*websocket.Conn
	bsmutex         sync.RWMutex
	badServers      map[string]*cloud.Server
	simutex         sync.RWMutex
	schedIssues     map[string]*schedulerIssue
	krmutex         sync.RWMutex
	killRunners     bool
	timings         map[string]*timingAvg
	tmutex          sync.Mutex
	ssmutex         sync.RWMutex // "server state mutex" to protect up, drain, blocking and ServerInfo.Mode
	log15.Logger
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
	// should contain 6 %s parts which will be replaced with the scheduler
	// group, deployment, ip:host address of the server, domain name that the
	// server's certificate should be valid for, reservation time out and
	// maximum number of minutes allowed, eg. "my_jobqueue_runner_client --group
	// '%s' --deployment %s --server '%s' --domain %s --reserve_timeout %d
	// --max_mins %d". If you supply an empty string (the default), runner
	// clients will not be spawned; for any work to be done you will have to run
	// your runner client yourself manually.
	RunnerCmd string

	// Absolute path to where the database file should be saved. The database is
	// used to ensure no loss of added commands, to keep a permanent history of
	// all jobs completed, and to keep various stats, amongst other things.
	DBFile string

	// Absolute path to where the database file should be backed up to.
	DBFileBackup string

	// Absolute path to where the server will store the authorization token
	// needed by clients to communicate with the server. Storing it in a file
	// could make using any CLI clients more convenient. The file will be
	// read-only by the user starting the server. The default of empty string
	// means the token is not saved to disk.
	TokenFile string

	// Absolute path to where CA PEM file is that will be used for
	// securing access to the web interface. If the given file does not exist,
	// a certificate will be generated for you at this path.
	CAFile string

	// Absolute path to where certificate PEM file is that will be used for
	// securing access to the web interface. If the given file does not exist,
	// a certificate will be generated for you at this path.
	CertFile string

	// Absolute path to where key PEM file is that will be used for securing
	// access to the web interface. If the given file does not exist, a
	// key will be generated for you at this path.
	KeyFile string

	// Domain that a generated CertFile should be valid for. If not supplied,
	// defaults to "localhost".
	//
	// When using your own CertFile, this should be set to a domain that the
	// certifcate is valid for, as when the server spawns clients, those clients
	// will validate the server's certifcate based on this domain. For the web
	// interface and REST API, it is up to you to ensure that your DNS has an
	// entry for this domain that points to the IP address of the machine
	// running your server.
	CertDomain string

	// Name of the deployment ("development" or "production"); development
	// databases are deleted and recreated on start up by default.
	Deployment string

	// CIDR is the IP address range of your network. When the server needs to
	// know its own IP address, it uses this CIDR to confirm it got it correct
	// (ie. it picked the correct network interface). You can leave this unset,
	// in which case it will do its best to pick correctly. (This is only a
	// possible issue if you have multiple network interfaces.)
	CIDR string

	// UploadDir is the directory where files uploaded to the Server will be
	// stored. They get given unique names based on the MD5 checksum of the file
	// uploaded. Defaults to /tmp.
	UploadDir string

	// Logger is a logger object that will be used to log uncaught errors and
	// debug statements. "Uncought" errors are all errors generated during
	// operation that either shouldn't affect the success of operations, and can
	// be ignored (logged at the Warn level, and which is why the errors are not
	// returned by the methods generating them), or errors that could not be
	// returned (logged at the Error level, eg. generated during a go routine,
	// such as errors by the server handling a particular client request).
	// We attempt to recover from panics during server operation and log these
	// at the Crit level.
	//
	// If your logger is levelled and set to the debug level, you will also get
	// information tracking the inner workings of the server.
	//
	// If this is unset, nothing is logged (defaults to a logger using a
	// log15.DiscardHandler()).
	Logger log15.Logger
}

// Serve is for use by a server executable and makes it start listening on
// localhost at the configured port for Connect()ions from clients, and then
// handles those clients.
//
// It returns a *Server that you will typically call Block() on to block until
// until your executable receives a SIGINT or SIGTERM, or you call Stop(), at
// which point the queues will be safely closed (you'd probably just exit at
// that point).
//
// If it creates a db file or recreates one from backup, and if it creates TLS
// certificates, it will say what it did in the returned msg string.
//
// The returned token must be provided by any client to authenticate. The server
// is a single user system, so there is only 1 token kept for its entire
// lifetime. If config.TokenFile has been set, the token will also be written to
// that file, potentially making it easier for any CLI clients to authenticate
// with this returned Server.
//
// The possible errors from Serve() will be related to not being able to start
// up at the supplied address; errors encountered while dealing with clients are
// logged but otherwise ignored.
//
// It also spawns your runner clients as needed, running them via the configured
// job scheduler, using the configured shell. It determines the command line to
// execute for your runner client from the configured RunnerCmd string you
// supplied.
func Serve(config ServerConfig) (s *Server, msg string, token []byte, err error) {
	// if a logger was configured we will log debug statements and "harmless"
	// errors not worth returning (or not possible to return), along with
	// panics. Otherwise we create a default logger that discards all log
	// attempts.
	serverLogger := config.Logger
	if serverLogger == nil {
		serverLogger = log15.New()
		serverLogger.SetHandler(log15.DiscardHandler())
	} else {
		serverLogger = serverLogger.New()
	}
	defer internal.LogPanic(serverLogger, "jobqueue serve", true)

	// generate a secure token for clients to authenticate with
	token, err = generateToken()
	if err != nil {
		return s, msg, token, err
	}

	// check if the cert files are available
	httpAddr := "0.0.0.0:" + config.WebPort
	caFile := config.CAFile
	certFile := config.CertFile
	keyFile := config.KeyFile
	certDomain := config.CertDomain
	if certDomain == "" {
		certDomain = localhost
	}
	err = internal.CheckCerts(certFile, keyFile)
	var certMsg string
	if err != nil {
		// if not, generate our own
		err = internal.GenerateCerts(caFile, certFile, keyFile, certDomain)
		if err != nil {
			serverLogger.Error("GenerateCerts failed", "err", err)
			return s, msg, token, err
		}
		certMsg = "created a new key and certificate for TLS"
		msg = certMsg
	}

	sock, err := rep.NewSocket()
	if err != nil {
		return s, msg, token, err
	}

	// we open ourselves up to possible denial-of-service attack if a client
	// sends us tons of data, but at least the client doesn't silently hang
	// forever when it legitimately wants to Add() a ton of jobs
	// unlimited Recv() length
	if err = sock.SetOption(mangos.OptionMaxRecvSize, 0); err != nil {
		return s, msg, token, err
	}

	// we use raw mode, allowing us to respond to multiple clients in
	// parallel
	if err = sock.SetOption(mangos.OptionRaw, true); err != nil {
		return s, msg, token, err
	}

	// we'll wait ServerInterruptTime to recv from clients before trying again,
	// allowing us to check if signals have been passed
	if err = sock.SetOption(mangos.OptionRecvDeadline, ServerInterruptTime); err != nil {
		return s, msg, token, err
	}

	// have mangos listen using TLS over TCP
	sock.AddTransport(tlstcp.NewTransport())
	cer, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return s, msg, token, err
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cer}}
	listenOpts := make(map[string]interface{})
	caCert, err := ioutil.ReadFile(caFile)
	if err == nil {
		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(caCert)
		tlsConfig.RootCAs = certPool
	}
	listenOpts[mangos.OptionTLSConfig] = tlsConfig
	if err = sock.ListenOptions("tls+tcp://0.0.0.0:"+config.Port, listenOpts); err != nil {
		return s, msg, token, err
	}

	// serving will happen in a goroutine that will stop on SIGINT or SIGTERM,
	// or if something is sent on the stopSigHandling channel. The done channel
	// is used to report back to a user that called Block() when and why we
	// stopped serving. stopClientHandling is used to stop client handling at
	// the right moment during the shutdown process. To know when all the
	// goroutines we start actually finish, the shutdown process will check a
	// waitgroup as well.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	stopSigHandling := make(chan bool, 1)
	stopClientHandling := make(chan bool)
	done := make(chan error, 1)
	wg := &sync.WaitGroup{}

	// if we end up spawning clients on other machines, they'll need to know
	// our non-loopback ip address so they can connect to us
	ip, err := internal.CurrentIP(config.CIDR)
	if err != nil {
		serverLogger.Error("getting current IP failed", "err", err)
	}
	if ip == "" {
		return s, msg, token, Error{"Serve", "", ErrNoHost}
	}

	// we will spawn runner clients via the requested job scheduler
	sch, err := scheduler.New(config.SchedulerName, config.SchedulerConfig, serverLogger)
	if err != nil {
		return s, msg, token, err
	}

	// we need to persist stuff to disk, and we do so using boltdb
	db, msg, err := initDB(config.DBFile, config.DBFileBackup, config.Deployment, serverLogger)
	if certMsg != "" {
		if msg == "" {
			msg = certMsg
		} else {
			msg = certMsg + ". " + msg
		}
	}
	if err != nil {
		return s, msg, token, err
	}

	uploadDir := config.UploadDir
	if uploadDir == "" {
		uploadDir = "/tmp"
	}

	s = &Server{
		ServerInfo:         &ServerInfo{Addr: ip + ":" + config.Port, Host: certDomain, Port: config.Port, WebPort: config.WebPort, PID: os.Getpid(), Deployment: config.Deployment, Scheduler: config.SchedulerName, Mode: ServerModeNormal},
		token:              token,
		uploadDir:          uploadDir,
		sock:               sock,
		ch:                 new(codec.BincHandle),
		rpl:                &rgToKeys{lookup: make(map[string]map[string]bool)},
		db:                 db,
		stopSigHandling:    stopSigHandling,
		stopClientHandling: stopClientHandling,
		done:               done,
		wg:                 wg,
		up:                 true,
		scheduler:          sch,
		sgroupcounts:       make(map[string]int),
		sgrouptrigs:        make(map[string]int),
		sgtr:               make(map[string]*scheduler.Requirements),
		rc:                 config.RunnerCmd,
		wsconns:            make(map[string]*websocket.Conn),
		statusCaster:       bcast.NewGroup(),
		badServerCaster:    bcast.NewGroup(),
		badServers:         make(map[string]*cloud.Server),
		schedCaster:        bcast.NewGroup(),
		schedIssues:        make(map[string]*schedulerIssue),
		timings:            make(map[string]*timingAvg),
		Logger:             serverLogger,
	}

	// if we're restarting from a state where there were incomplete jobs, we
	// need to load those in to our queue now
	s.createQueue()
	priorJobs, err := db.recoverIncompleteJobs()
	if err != nil {
		return nil, msg, token, err
	}
	if len(priorJobs) > 0 {
		var itemdefs []*queue.ItemDef
		for _, job := range priorJobs {
			var deps []string
			deps, err = job.Dependencies.incompleteJobKeys(s.db)
			if err != nil {
				return nil, msg, token, err
			}
			itemdefs = append(itemdefs, &queue.ItemDef{Key: job.Key(), ReserveGroup: job.getSchedulerGroup(), Data: job, Priority: job.Priority, Delay: 0 * time.Second, TTR: ServerItemTTR, Dependencies: deps})
		}
		_, _, err = s.enqueueItems(itemdefs)
		if err != nil {
			return nil, msg, token, err
		}
	}

	// set up responding to command-line clients
	wg.Add(1)
	go func() {
		// log panics and die
		defer internal.LogPanic(s.Logger, "jobqueue serving", true)
		defer wg.Done()

		for {
			select {
			case <-stopClientHandling: // s.shutdown() sends this
				return
			default:
				// receive a clientRequest from a client
				m, rerr := sock.RecvMsg()
				if rerr != nil {
					s.krmutex.RLock()
					inShutdown := s.killRunners
					s.krmutex.RUnlock()
					if !inShutdown && rerr != mangos.ErrRecvTimeout {
						s.Error("Server socket Receive error", "err", rerr)
					}
					continue
				}

				// parse the request, do the desired work and respond to the client
				wg.Add(1)
				go func() {
					// log panics and continue
					defer internal.LogPanic(s.Logger, "jobqueue server client handling", false)
					defer wg.Done()

					herr := s.handleRequest(m)
					if ServerLogClientErrors && herr != nil {
						s.krmutex.RLock()
						inShutdown := s.killRunners
						s.krmutex.RUnlock()
						if !inShutdown {
							s.Error("Server handle client request error", "err", herr)
						}
					}
				}()
			}
		}
	}()

	// wait for signal or s.Stop() and call s.shutdown(). (We don't use the
	// waitgroup here since we call shutdown, which waits on the group)
	go func() {
		// log panics and die
		defer internal.LogPanic(s.Logger, "jobqueue serving", true)

		for {
			select {
			case sig := <-sigs:
				var reason string
				switch sig {
				case os.Interrupt:
					reason = ErrClosedInt
				case syscall.SIGTERM:
					reason = ErrClosedTerm
				}
				signal.Stop(sigs)
				s.shutdown(reason, true, false)
				return
			case <-stopSigHandling: // s.Stop() causes this to be sent during s.shutdown(), which it calls
				signal.Stop(sigs)
				return
			}
		}
	}()

	// set up the web interface
	ready := make(chan bool)
	wg.Add(1)
	go func() {
		// log panics and die
		defer internal.LogPanic(s.Logger, "jobqueue web server", true)
		defer wg.Done()

		mux := http.NewServeMux()
		mux.HandleFunc("/", webInterfaceStatic(s))
		mux.HandleFunc("/status_ws", webInterfaceStatusWS(s))
		mux.HandleFunc(restJobsEndpoint, restJobs(s))
		mux.HandleFunc(restWarningsEndpoint, restWarnings(s))
		mux.HandleFunc(restBadServersEndpoint, restBadServers(s))
		mux.HandleFunc(restFileUploadEndpoint, restFileUpload(s))
		srv := &http.Server{Addr: httpAddr, Handler: mux}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs := srv.ListenAndServeTLS(certFile, keyFile)
			if errs != nil && errs != http.ErrServerClosed {
				s.Error("server web interface had problems", "err", errs)
			}
		}()
		s.httpServer = srv

		wg.Add(1)
		go func() {
			defer wg.Done()
			s.statusCaster.Broadcasting(0)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.badServerCaster.Broadcasting(0)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.schedCaster.Broadcasting(0)
		}()

		badServerCB := func(server *cloud.Server) {
			s.bsmutex.Lock()
			skip := false
			if server.IsBad() {
				// double check that due to timing issues this server hasn't
				// been destroyed, which is not something to warn anyone about
				if server.Destroyed() {
					skip = true
				} else {
					s.badServers[server.ID] = server
				}
			} else {
				delete(s.badServers, server.ID)
			}
			s.bsmutex.Unlock()

			if !skip {
				s.badServerCaster.Send(&badServer{
					ID:      server.ID,
					Name:    server.Name,
					IP:      server.IP,
					Date:    time.Now().Unix(),
					IsBad:   server.IsBad(),
					Problem: server.PermanentProblem(),
				})
			}
		}
		s.scheduler.SetBadServerCallBack(badServerCB)

		messageCB := func(msg string) {
			s.simutex.Lock()
			var si *schedulerIssue
			var existed bool
			if si, existed = s.schedIssues[msg]; existed {
				si.LastDate = time.Now().Unix()
				si.Count = si.Count + 1
			} else {
				si = &schedulerIssue{
					Msg:       msg,
					FirstDate: time.Now().Unix(),
					LastDate:  time.Now().Unix(),
					Count:     1,
				}
				s.schedIssues[msg] = si
			}
			s.simutex.Unlock()
			s.schedCaster.Send(si)
		}
		s.scheduler.SetMessageCallBack(messageCB)

		// wait a while for ListenAndServe() to start listening
		<-time.After(10 * time.Millisecond)
		ready <- true
	}()
	<-ready

	// store token on disk
	if config.TokenFile != "" {
		err = ioutil.WriteFile(config.TokenFile, token, 0600)
		if err != nil {
			return s, msg, token, err
		}
	}

	return s, msg, token, err
}

// Block makes you block while the server does the job of serving clients. This
// will return with an error indicating why it stopped blocking, which will
// be due to receiving a signal or because you called Stop()
func (s *Server) Block() error {
	s.ssmutex.Lock()
	s.blocking = true
	s.ssmutex.Unlock()
	return <-s.done
}

// Stop will cause a graceful shut down of the server. Supplying an optional
// bool of true will cause Stop() to wait until all runners have exited and
// the server is truly down before returning.
func (s *Server) Stop(wait ...bool) {
	s.shutdown(ErrClosedStop, len(wait) == 1 && wait[0], true)
}

// Drain will stop the server spawning new runners and stop Reserve*() from
// returning any more Jobs. Once all current runners exit, we Stop().
func (s *Server) Drain() error {
	s.ssmutex.Lock()
	defer s.ssmutex.Unlock()
	if !s.up {
		return Error{"Drain", "", ErrNoServer}
	}
	if s.drain {
		return nil
	}

	s.drain = true
	s.ServerInfo.Mode = ServerModeDrain
	go func() {
		defer internal.LogPanic(s.Logger, "jobqueue drain", true)

		ticker := time.NewTicker(1 * time.Second)
	TICKS:
		for range ticker.C {
			// check our queue for things running, which is cheap
			stats := s.q.Stats()
			if stats.Running > 0 {
				continue TICKS
			}
			ticker.Stop()

			// now that we think nothing should be running, get
			// Stop() to wait for the runner clients to exit so the
			// job scheduler will be nice and clean
			s.Stop(true)
			break
		}
	}()
	return nil
}

// GetServerStats returns some simple live stats about what's happening in the
// server's queue.
func (s *Server) GetServerStats() *ServerStats {
	var delayed, ready, running, buried int
	var etc time.Time

	stats := s.q.Stats()
	delayed += stats.Delayed
	ready += stats.Ready
	buried += stats.Buried

	for _, inter := range s.q.GetRunningData() {
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

	return &ServerStats{Delayed: delayed, Ready: ready, Running: running, Buried: buried, ETC: etc.Truncate(time.Minute).Sub(time.Now().Truncate(time.Minute))}
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

// uploadFile uploads the given file data to the given path on the machine where
// the server process is running.
//
// If savePath is an empty string, the file is stored at a path based on the MD5
// checksum of the file data, rooted in the server's configured UploadDir. If it
// turns out such a file already exists, no error is generated. savePath can be
// prefixed with ~/ to have it saved relative to the server's home directory.
//
// Files stored will only be readable by the user that started the server.
//
// Note that this is only intended for a a few small files, such as config files
// that need to be passed through to spawned cloud servers, when doing a cloud
// deployment.
//
// Returns the absolute path to the file that now contains the given file data.
func (s *Server) uploadFile(source io.Reader, savePath string) (string, error) {
	var file *os.File
	var err error
	usedTempFile := false
	if savePath == "" {
		if _, err = os.Stat(s.uploadDir); err != nil && os.IsNotExist(err) {
			err = os.MkdirAll(s.uploadDir, os.ModePerm)
			if err != nil {
				s.Error("uploadFile create directory error", "err", err)
				return "", err
			}
		}
		file, err = ioutil.TempFile(s.uploadDir, "file_upload")
		if err != nil {
			s.Error("uploadFile temp file create error", "err", err)
			return "", err
		}
		savePath = file.Name()
		usedTempFile = true
	} else {
		savePath = internal.TildaToHome(savePath)
		err = os.MkdirAll(filepath.Dir(savePath), os.ModePerm)
		if err != nil {
			s.Error("uploadFile create directory error", "err", err)
			return "", err
		}
		file, err = os.OpenFile(savePath, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			s.Error("uploadFile create file error", "err", err)
			return "", err
		}
	}

	_, err = io.Copy(file, source)
	if err != nil {
		s.Error("uploadFile store file error", "err", err)
		return "", err
	}
	err = file.Close()
	if err != nil {
		s.Warn("uploadFile close file error", "err", err)
	}

	if usedTempFile {
		// rename the file to one based on the md5 checksum of the file
		var md5 string
		md5, err = internal.FileMD5(savePath, s.Logger)
		if err != nil {
			s.Error("uploadFile md5 calculation error", "err", err)
			return "", err
		}

		dir, leaf := calculateHashedDir(s.uploadDir, md5)
		err = os.MkdirAll(dir, os.ModePerm)
		if err != nil {
			s.Error("uploadFile create directory error", "err", err)
			return "", err
		}

		finalPath := path.Join(dir, leaf)
		_, err = os.Stat(finalPath)
		if err != nil {
			if os.IsNotExist(err) {
				err = os.Rename(savePath, finalPath)
				if err != nil {
					s.Error("uploadFile rename file error", "err", err)
					return "", err
				}
			} else {
				s.Error("uploadFile stat file error", "err", err)
				return "", err
			}
		} else {
			// already exists, delete the temp file
			err = os.Remove(savePath)
			if err != nil {
				s.Warn("uploadFile file removal error", "err", err)
			}
		}
		savePath = finalPath
	}

	return savePath, nil
}

// createQueue creates and stores a queue.Queue on the Server and sets up its
// callbacks.
func (s *Server) createQueue() {
	q := queue.New("cmds")
	s.q = q

	// we set a callback for things entering this queue's ready sub-queue.
	// This function will be called in a go routine and receives a slice of
	// all the ready jobs. Based on the requirements, we add to each job a
	// schedulerGroup, which the runners we spawn will be able to pass to
	// Reserve() so that they run the correct jobs for the machine and
	// resource reservations the job scheduler will run them under. queue
	// package will only call this once at a time, so we don't need to worry
	// about locking across the whole function.
	q.SetReadyAddedCallback(func(queuename string, allitemdata []interface{}) {
		defer internal.LogPanic(s.Logger, "jobqueue ready added callback", true)

		s.ssmutex.RLock()
		if s.drain || !s.up {
			s.ssmutex.RUnlock()
			return
		}
		s.ssmutex.RUnlock()

		// calculate, set and count jobs by schedulerGroup
		groups := make(map[string]int)
		groupToReqs := make(map[string]*scheduler.Requirements)
		groupsScheduledCounts := make(map[string]int)
		noRecGroups := make(map[string]bool)
		for _, inter := range allitemdata {
			job := inter.(*Job)

			// depending on job.Override, get memory, disk and time
			// recommendations, which are rounded to get fewer larger
			// groups
			noRec := false
			var recommendedReq *scheduler.Requirements
			if rec, existed := groupToReqs[job.ReqGroup]; existed {
				recommendedReq = rec
			} else {
				recm, errm := s.db.recommendedReqGroupMemory(job.ReqGroup)
				recd, errd := s.db.recommendedReqGroupDisk(job.ReqGroup)
				recs, errs := s.db.recommendedReqGroupTime(job.ReqGroup)
				if recm == 0 || recs == 0 || errm != nil || errd != nil || errs != nil {
					groupToReqs[job.ReqGroup] = nil
				} else {
					recdGBs := 0
					if recd > 0 { // not all jobs collect disk usage, so this can be 0
						recdGBs = int(math.Ceil(float64(recd) / float64(1024)))
					}
					recommendedReq = &scheduler.Requirements{RAM: recm, Disk: recdGBs, Time: time.Duration(recs) * time.Second}
					groupToReqs[job.ReqGroup] = recommendedReq
				}
			}

			if recommendedReq != nil {
				job.Lock()
				if job.RequirementsOrig == nil {
					job.RequirementsOrig = &scheduler.Requirements{
						RAM:  job.Requirements.RAM,
						Time: job.Requirements.Time,
						Disk: job.Requirements.Disk,
					}
				}
				if job.RequirementsOrig.RAM > 0 {
					switch job.Override {
					case 0:
						job.Requirements.RAM = recommendedReq.RAM
					case 1:
						if recommendedReq.RAM > job.Requirements.RAM {
							job.Requirements.RAM = recommendedReq.RAM
						}
					}
				} else {
					job.Requirements.RAM = recommendedReq.RAM
				}

				if job.RequirementsOrig.Disk > 0 {
					switch job.Override {
					case 0:
						job.Requirements.Disk = recommendedReq.Disk
					case 1:
						if recommendedReq.Disk > job.Requirements.Disk {
							job.Requirements.Disk = recommendedReq.Disk
						}
					}
				} else {
					job.Requirements.Disk = recommendedReq.Disk
				}

				if job.RequirementsOrig.Time > 0 {
					switch job.Override {
					case 0:
						job.Requirements.Time = recommendedReq.Time
					case 1:
						if recommendedReq.Time > job.Requirements.Time {
							job.Requirements.Time = recommendedReq.Time
						}
					}
				} else {
					job.Requirements.Time = recommendedReq.Time
				}
				job.Unlock()
			} else {
				noRec = true
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
					errs := q.SetReserveGroup(job.Key(), schedulerGroup)
					if errs != nil {
						// we could be trying to set the reserve group after the
						// job has already completed, if they complete
						// ~instantly
						if qerr, ok := errs.(queue.Error); !ok || qerr.Err != queue.ErrNotFound {
							s.Warn("readycallback queue setreservegroup failed", "err", errs)
						}
					}
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
					s.wg.Add(1)
					go func(group string) {
						defer internal.LogPanic(s.Logger, "jobqueue clear scheduler group", true)
						defer s.wg.Done()
						s.clearSchedulerGroup(group)
					}(group)
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
				s.wg.Add(1)
				go func(group string) {
					defer internal.LogPanic(s.Logger, "jobqueue schedule runners", true)
					defer s.wg.Done()
					s.scheduleRunners(group)
				}(group)
			}

			// in the event that the runners we spawn can't reach us
			// temporarily and just die, we need to make sure this callback
			// gets triggered again even if no new jobs get added
			s.racmutex.Lock()
			defer s.racmutex.Unlock()
			s.racCheckReady = len(allitemdata)
			if s.racChecking {
				if !s.racCheckTimer.Stop() {
					<-s.racCheckTimer.C
				}
				s.racCheckTimer.Reset(ServerCheckRunnerTime)
			} else {
				s.racCheckTimer = time.NewTimer(ServerCheckRunnerTime)

				s.wg.Add(1)
				go func() {
					defer internal.LogPanic(s.Logger, "jobqueue rac checking", true)
					defer s.wg.Done()

					select {
					case <-s.racCheckTimer.C:
						break
					case <-s.stopClientHandling:
						return
					}

					s.racmutex.Lock()
					s.racChecking = false
					stats := q.Stats()
					s.racmutex.Unlock()

					if stats.Ready >= s.racCheckReady {
						q.TriggerReadyAddedCallback()
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
		groupsLost := make(map[string]int)
		lost := 0
		for _, inter := range data {
			job := inter.(*Job)

			// if we change from running, mark that we have not scheduled a
			// runner for the job
			if from == JobStateRunning {
				job.setScheduledRunner(false)

				job.RLock()
				l := job.Lost
				job.RUnlock()
				if l {
					lost++
					groupsLost[job.RepGroup]++
					continue
				}
			}

			groups[job.RepGroup]++
		}

		// send out the counts
		s.statusCaster.Send(&jstateCount{"+all+", from, to, len(data) - lost})
		for group, count := range groups {
			s.statusCaster.Send(&jstateCount{group, from, to, count})
		}

		if lost > 0 {
			s.statusCaster.Send(&jstateCount{"+all+", JobStateLost, to, lost})
			for group, count := range groupsLost {
				s.statusCaster.Send(&jstateCount{group, JobStateLost, to, count})
			}
		}
	})

	// we set a callback for running items that hit their ttr because the
	// runner died or because of networking issues: we keep them in the
	// running queue, but mark them up as having possibly failed, leaving it
	// up the user if they want to confirm the jobs are dead by killing
	// them or leaving them to spring back to life if not.
	q.SetTTRCallback(func(data interface{}) queue.SubQueue {
		job := data.(*Job)

		job.Lock()
		defer job.Unlock()
		if !job.StartTime.IsZero() && !job.Exited {
			job.Lost = true
			job.FailReason = FailReasonLost
			job.EndTime = time.Now()

			// since our changed callback won't be called, send out this
			// transition from running to lost state
			defer s.statusCaster.Send(&jstateCount{"+all+", JobStateRunning, JobStateLost, 1})
			defer s.statusCaster.Send(&jstateCount{job.RepGroup, JobStateRunning, JobStateLost, 1})

			return queue.SubQueueRun
		}

		return queue.SubQueueDelay
	})
}

// enqueueItems adds new items to a queue, for when we have new jobs to handle.
func (s *Server) enqueueItems(itemdefs []*queue.ItemDef) (added, dups int, err error) {
	added, dups, err = s.q.AddMany(itemdefs)
	if err != nil {
		return added, dups, err
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

	return added, dups, err
}

// createJobs creates new jobs, adding them to the database and the in-memory
// queue. It returns 2 errors; the first is one of our Err constant strings,
// the second is the actual error with more details.
func (s *Server) createJobs(inputJobs []*Job, envkey string, ignoreComplete bool) (added, dups, alreadyComplete int, srerr string, qerr error) {
	// create itemdefs for the jobs
	for _, job := range inputJobs {
		job.Lock()
		job.EnvKey = envkey
		job.UntilBuried = job.Retries + 1
		if s.rc != "" {
			job.schedulerGroup = job.Requirements.Stringify()
		}
		if job.BsubMode != "" {
			atomic.AddUint64(&BsubID, 1)
			job.BsubID = atomic.LoadUint64(&BsubID)
		}
		job.Unlock()
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
			deps, err := job.Dependencies.incompleteJobKeys(s.db)
			if err != nil {
				srerr = ErrDBError
				qerr = err
				break
			}
			itemdefs = append(itemdefs, &queue.ItemDef{Key: job.Key(), ReserveGroup: job.getSchedulerGroup(), Data: job, Priority: job.Priority, Delay: 0 * time.Second, TTR: ServerItemTTR, Dependencies: deps})
		}

		// storeNewJobs also returns jobsToUpdate, which are those jobs
		// currently in the queue that need their dependencies updated because
		// they just changed when we stored cr.Jobs
		for _, job := range jobsToUpdate {
			deps, err := job.Dependencies.incompleteJobKeys(s.db)
			if err != nil {
				srerr = ErrDBError
				qerr = err
				break
			}
			thisErr := s.q.Update(job.Key(), job.getSchedulerGroup(), job, job.Priority, 0*time.Second, ServerItemTTR, deps)
			if thisErr != nil {
				qerr = thisErr
				break
			}
		}

		if qerr != nil {
			srerr = ErrInternalError
		} else {
			// add the jobs to the in-memory job queue
			added, dups, qerr = s.enqueueItems(itemdefs)
			if qerr != nil {
				srerr = ErrInternalError
			}
		}
	}
	return added, dups, alreadyComplete, srerr, qerr
}

// killJob sets the killCalled property on a job, to change the subsequent
// behaviour of touching, which should result in an executing job killing
// itself.
//
// If we have lost contact with the job, calling killJob is also the way to
// confirm it is definitely dead and won't spring back to life in the future:
// we release or bury it as appropriate.
//
// If the job wasn't running, returned bool will be false and nothing will have
// been done.
func (s *Server) killJob(jobkey string) (bool, error) {
	item, err := s.q.Get(jobkey)
	if err != nil || item.Stats().State != queue.ItemStateRun {
		return false, err
	}

	job := item.Data.(*Job)
	job.Lock()
	job.killCalled = true

	if job.Lost {
		job.UntilBuried--
		ub := job.UntilBuried
		job.Exited = true
		job.Exitcode = -1
		job.EndTime = time.Now()
		job.FailReason = FailReasonLost
		job.Unlock()
		s.db.updateJobAfterExit(job, []byte{}, []byte{}, false)

		if ub <= 0 {
			err = s.q.Bury(item.Key)
			if err != nil {
				return true, err
			}
			s.decrementGroupCount(job.getSchedulerGroup())
			return true, err
		}
		err = s.q.Release(item.Key)
		if err != nil {
			return true, err
		}
		s.decrementGroupCount(job.getSchedulerGroup())
		return true, err
	}

	job.Unlock()
	return true, err
}

// getJobsByKeys gets jobs with the given keys (current and complete).
func (s *Server) getJobsByKeys(keys []string, getStd bool, getEnv bool) (jobs []*Job, srerr string, qerr string) {
	var notfound []string
	for _, jobkey := range keys {
		// try and get the job from the in-memory queue
		item, err := s.q.Get(jobkey)
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
		found, err := s.db.retrieveCompleteJobsByKeys(notfound)
		if err != nil {
			srerr = ErrDBError
			qerr = err.Error()
		} else if len(found) > 0 {
			if getEnv { // complete jobs don't have any std
				for _, job := range found {
					s.jobPopulateStdEnv(job, false, getEnv)
				}
			}
			jobs = append(jobs, found...)
		}
	}

	return jobs, srerr, qerr
}

// searchRepGroups looks up the rep groups of all jobs that have ever been added
// and returns those that contain the given sub string.
func (s *Server) searchRepGroups(partialRepGroup string) ([]string, error) {
	rgs, err := s.db.retrieveRepGroups()
	if err != nil {
		return nil, err
	}

	var matching []string
	for _, rg := range rgs {
		if strings.Contains(rg, partialRepGroup) {
			matching = append(matching, rg)
		}
	}
	return matching, err
}

// getJobsByRepGroup gets jobs in the given group (current and complete).
func (s *Server) getJobsByRepGroup(repgroup string, search bool, limit int, state JobState, getStd bool, getEnv bool) (jobs []*Job, srerr string, qerr string) {
	var rgs []string
	if search {
		var errs error
		rgs, errs = s.searchRepGroups(repgroup)
		if errs != nil {
			return nil, ErrDBError, errs.Error()
		}
	} else {
		rgs = append(rgs, repgroup)
	}

	for _, rg := range rgs {
		// look in the in-memory queue for matching jobs
		s.rpl.RLock()
		for key := range s.rpl.lookup[rg] {
			item, err := s.q.Get(key)
			if err == nil && item != nil {
				job := s.itemToJob(item, false, false)
				jobs = append(jobs, job)
			}
		}
		s.rpl.RUnlock()

		// look in the permanent store for matching jobs
		if state == "" || state == JobStateComplete {
			var complete []*Job
			complete, srerr, qerr = s.getCompleteJobsByRepGroup(rg)
			if len(complete) > 0 {
				// a job is stored in the db with only the single most recent
				// RepGroup it had, but we're able to retrieve jobs based on any of
				// the RepGroups it ever had; set the RepGroup to the one the user
				// requested *** may want to change RepGroup to store a slice of
				// RepGroups? But that could be massive...
				for _, cj := range complete {
					cj.RepGroup = rg
				}
				jobs = append(jobs, complete...)
			}
		}
	}

	if limit > 0 || state != "" || getStd || getEnv {
		jobs = s.limitJobs(jobs, limit, state, getStd, getEnv)
	}
	return jobs, srerr, qerr
}

// getCompleteJobsByRepGroup gets complete jobs in the given group.
func (s *Server) getCompleteJobsByRepGroup(repgroup string) (jobs []*Job, srerr string, qerr string) {
	jobs, err := s.db.retrieveCompleteJobsByRepGroup(repgroup)
	if err != nil {
		srerr = ErrDBError
		qerr = err.Error()
	}
	return jobs, srerr, qerr
}

// getJobsCurrent gets all current (incomplete) jobs.
func (s *Server) getJobsCurrent(limit int, state JobState, getStd bool, getEnv bool) []*Job {
	var jobs []*Job
	for _, item := range s.q.AllItems() {
		jobs = append(jobs, s.itemToJob(item, false, false))
	}

	if limit > 0 || state != "" || getStd || getEnv {
		jobs = s.limitJobs(jobs, limit, state, getStd, getEnv)
	}

	return jobs
}

// limitJobs handles the limiting of jobs for getJobsByRepGroup() and
// getJobsCurrent(). States 'reserved' and 'running' are treated as the same
// state.
func (s *Server) limitJobs(jobs []*Job, limit int, state JobState, getStd bool, getEnv bool) []*Job {
	groups := make(map[string][]*Job)
	var limited []*Job
	for _, job := range jobs {
		job.RLock()
		jState := job.State
		jExitCode := job.Exitcode
		jFailReason := job.FailReason
		jLost := job.Lost
		job.RUnlock()
		if jState == JobStateRunning {
			if jLost {
				jState = JobStateLost
			} else {
				jState = JobStateReserved
			}
		}

		if state != "" {
			if state == JobStateRunning {
				state = JobStateReserved
			}
			if state == JobStateDeletable {
				if jState == JobStateRunning || jState == JobStateComplete {
					continue
				}
			} else if jState != state {
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

	return limited
}

func (s *Server) scheduleRunners(group string) {
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
		err := s.scheduler.Schedule(fmt.Sprintf(rc, group, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.ServerInfo.Host, s.scheduler.ReserveTimeout(req), int(s.scheduler.MaxQueueTime(req).Minutes())), req, groupCount)
		if err != nil {
			problem := true
			if serr, ok := err.(scheduler.Error); ok && serr.Err == scheduler.ErrImpossible {
				// bury all jobs in this scheduler group
				problem = false
				s.sgcmutex.Lock()
				for {
					item, errr := s.q.Reserve(group)
					if errr != nil {
						if qerr, ok := errr.(queue.Error); !ok || qerr.Err != queue.ErrNothingReady {
							s.Warn("scheduleRunners failed to reserve an item", "group", group, "err", errr)
							problem = true
						}
						break
					}
					if item == nil {
						break
					}
					job := item.Data.(*Job)
					job.Lock()
					job.FailReason = FailReasonResource
					job.Unlock()
					errb := s.q.Bury(item.Key)
					if errb != nil {
						s.Warn("scheduleRunners failed to bury an item", "err", errb)
					}
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
				s.Warn("Server scheduling runners error", "err", err)

				// retry the schedule in a while
				s.wg.Add(1)
				go func() {
					defer internal.LogPanic(s.Logger, "jobqueue schedule runners retry", true)
					defer s.wg.Done()

					select {
					case <-time.After(ServerCheckRunnerTime):
						break
					case <-s.stopClientHandling:
						return
					}

					s.scheduleRunners(group)
				}()
				return
			}
		}
	}

	if doClear {
		s.clearSchedulerGroup(group)
	}
}

// adjust our count of how many jobs with this schedulerGroup we need in the job
// scheduler.
func (s *Server) decrementGroupCount(schedulerGroup string) {
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
			s.q.TriggerReadyAddedCallback()
		} else if doSchedule {
			// notify the job scheduler we need less jobs for this job's cmd now;
			// it will remove extraneous ones from its queue
			s.scheduleRunners(schedulerGroup)
		}
	}
}

// when we no longer need a schedulerGroup in the job scheduler, clean up and
// make sure the job scheduler knows we don't need any runners for this group.
func (s *Server) clearSchedulerGroup(schedulerGroup string) {
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
		err := s.scheduler.Schedule(fmt.Sprintf(s.rc, schedulerGroup, s.ServerInfo.Deployment, s.ServerInfo.Addr, s.ServerInfo.Host, s.scheduler.ReserveTimeout(req), int(s.scheduler.MaxQueueTime(req).Minutes())), req, 0)
		if err != nil {
			s.Warn("clearSchedulerGroup failed", "err", err)
		}
	}
}

// getBadServers converts the slice of cloud.Server objects we hold in to a
// slice of badServer structs.
func (s *Server) getBadServers() []*badServer {
	s.bsmutex.RLock()
	var bs []*badServer
	for _, server := range s.badServers {
		bs = append(bs, &badServer{
			ID:      server.ID,
			Name:    server.Name,
			IP:      server.IP,
			Date:    time.Now().Unix(),
			IsBad:   server.IsBad(),
			Problem: server.PermanentProblem(),
		})
	}
	s.bsmutex.RUnlock()
	return bs
}

// storeWebSocketConnection stores a connection and returns a unique identifier
// so that it can be later closed with closeWebSocketConnection(unique) or
// during Server shutdown.
func (s *Server) storeWebSocketConnection(conn *websocket.Conn) string {
	s.wsmutex.Lock()
	defer s.wsmutex.Unlock()
	unique := logext.RandId(8)
	s.wsconns[unique] = conn
	return unique
}

// closeWebSocketConnection closes the connection that was stored with
// storeWebSocketConnection() and that returned the given unique string.
// Closing it this way means that during Server shutdown we won't try and close
// it again.
func (s *Server) closeWebSocketConnection(unique string) {
	s.wsmutex.Lock()
	defer s.wsmutex.Unlock()
	conn, found := s.wsconns[unique]
	if !found {
		return
	}
	err := conn.Close()
	if err != nil {
		s.Warn("websocket close failed", "err", err)
	}
	delete(s.wsconns, unique)
}

// shutdown stops listening to client connections, close all queues and
// persists them to disk.
//
// Does nothing if already shutdown.
//
// For now it also kills all currently running jobs so that their runners don't
// stay alive uselessly. *** This adds 15s to our shutdown time...
func (s *Server) shutdown(reason string, wait bool, stopSigHandling bool) {
	s.ssmutex.Lock()

	if !s.up {
		s.ssmutex.Unlock()
		return
	}

	if stopSigHandling {
		close(s.stopSigHandling)
	}

	// change touch to always return a kill signal
	s.up = false
	s.drain = true
	s.ServerInfo.Mode = ServerModeDrain
	s.ssmutex.Unlock()
	s.krmutex.Lock()
	s.killRunners = true
	s.krmutex.Unlock()
	if s.HasRunners() {
		// wait until everything must have attempted a touch
		<-time.After(ClientTouchInterval)
	}

	// wait for the runners to actually die
	if wait {
		ticker := time.NewTicker(100 * time.Millisecond)
		for range ticker.C {
			if !s.HasRunners() {
				ticker.Stop()
				break
			}
		}
	}

	// stop the scheduler
	s.scheduler.Cleanup()

	// graceful shutdown of all websocket-related goroutines and connections
	s.statusCaster.Close()
	s.badServerCaster.Close()
	s.schedCaster.Close()
	s.wsmutex.Lock()
	for unique, conn := range s.wsconns {
		errc := conn.Close()
		if errc != nil {
			s.Warn("server shutdown failed to close a websocket", "err", errc)
		}
		delete(s.wsconns, unique)
	}
	s.wsmutex.Unlock()

	// graceful shutdown of http server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := s.httpServer.Shutdown(ctx)
	if err != nil {
		s.Warn("server shutdown of web interface failed", "err", err)
	}
	cancel()

	// close our command line interface
	close(s.stopClientHandling)
	err = s.sock.Close()
	if err != nil {
		s.Warn("server shutdown socket close failed", "err", err)
	}

	// close the database
	err = s.db.close()
	if err != nil {
		s.Warn("server shutdown database close failed", "err", err)
	}

	// wait for our goroutines to finish
	s.wg.Wait()

	// wait until the ports are really no longer being listened to (which isn't
	// the same as them being available to be reconnected to, but this is the
	// best we can do?)
	for {
		conn, _ := net.DialTimeout("tcp", net.JoinHostPort("", s.ServerInfo.Port), 10*time.Millisecond)
		if conn != nil {
			errc := conn.Close()
			if errc != nil {
				s.Warn("server shutdown port close failed", "port", s.ServerInfo.WebPort, "err", errc)
			}
			continue
		}
		conn, _ = net.DialTimeout("tcp", net.JoinHostPort("", s.ServerInfo.WebPort), 10*time.Millisecond)
		if conn != nil {
			errc := conn.Close()
			if errc != nil {
				s.Warn("server shutdown port close failed", "port", s.ServerInfo.WebPort, "err", errc)
			}
			continue
		}
		break
	}

	// clean up our queues and empty everything out to be garbage collected,
	// in case the same process calls Serve() again after this
	err = s.q.Destroy()
	if err != nil {
		s.Warn("server shutdown queue destruction failed", "err", err)
	}
	s.q = nil

	s.krmutex.Lock()
	s.killRunners = false
	s.krmutex.Unlock()

	s.ssmutex.Lock()
	s.drain = false
	wasBlocking := s.blocking
	s.blocking = false
	s.ssmutex.Unlock()

	if wasBlocking {
		s.done <- Error{"Serve", "", reason}
	}
}
