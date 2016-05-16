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

package jobqueue

// This file contains all the functions to implement a jobqueue server.

import (
	"fmt"
	"github.com/go-mangos/mangos"
	"github.com/go-mangos/mangos/protocol/rep"
	"github.com/go-mangos/mangos/transport/tcp"
	"github.com/satori/go.uuid"
	"github.com/sb10/vrpipe/queue"
	"github.com/ugorji/go/codec"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	ErrInternalError      = "internal error"
	ErrUnknownCommand     = "unknown command"
	ErrBadRequest         = "bad request (missing arguments?)"
	ErrBadJob             = "bad job (not in queue or correct sub-queue)"
	ErrMissingJob         = "corresponding job not found"
	ErrUnknown            = "unknown error"
	ErrClosedInt          = "queues closed due to SIGINT"
	ErrClosedTerm         = "queues closed due to SIGTERM"
	ErrClosedStop         = "queues closed due to manual Stop()"
	ErrQueueClosed        = "queue closed"
	ErrNoHost             = "could not determine the non-loopback ip address of this host"
	ErrNoServer           = "could not reach the server"
	ErrMustReserve        = "you must Reserve() a Job before passing it to other methods"
	ErrDBError            = "failed to use database"
	ServerInterruptTime   = 1 * time.Second
	ServerItemDelay       = 30 * time.Second
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

// jobErr is used internally to implement Reserve(), which needs to send job and
// err over a channel
type jobErr struct {
	job *Job
	err string
}

// serverResponse is the struct that the server sends to clients over the
// network in response to their clientRequest
type serverResponse struct {
	Err     string // string instead of error so we can decode on the client side
	Added   int
	Existed int
	Job     *Job
	Jobs    []*Job
	SStats  *ServerStats
}

// ServerInfo holds basic addressing info about the server
type ServerInfo struct {
	Addr string // ip:port
	Host string // hostname
	Port string // port
	PID  int    // process id of server
}

// ServerStats holds information about the jobqueue server for sending to
// clients
type ServerStats struct {
	ServerInfo *ServerInfo
}

// server represents the server side of the socket that clients Connect() to
type Server struct {
	ServerInfo *ServerInfo
	sock       mangos.Socket
	ch         codec.Handle
	db         *db
	done       chan error
	stop       chan bool
	sync.Mutex
	qs map[string]*queue.Queue
}

// Serve is for use by a server executable and makes it start listening on
// localhost at the supplied port for Connect()ions from clients, and then
// handles those clients. It returns a *Server that you will typically call
// Block() on to block until until your executable receives a SIGINT or SIGTERM,
// or you call Stop(), at which point the queues will be safely closed (you'd
// probably just exit at that point). The possible errors from Serve() will be
// related to not being able to start up at the supplied address; errors
// encountered while dealing with clients are logged but otherwise ignored. If
// it creates a db file or recreates one from backup, it will say what it did
// in the returned msg string.
func Serve(port string, dbFile string, dbBkFile string) (s *Server, msg string, err error) {
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

	if err = sock.Listen("tcp://localhost:" + port); err != nil {
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
	var ip string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ip = ipnet.IP.String()
			}
		}
	}
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

	// we need to persist stuff to disk, and we do so using boltdb
	db, msg, err := initDB(dbFile, dbBkFile)
	if err != nil {
		return
	}

	s = &Server{
		ServerInfo: &ServerInfo{Addr: ip + ":" + port, Host: host, Port: port, PID: os.Getpid()},
		sock:       sock,
		ch:         new(codec.BincHandle),
		qs:         make(map[string]*queue.Queue),
		db:         db,
		stop:       stop,
		done:       done,
	}

	go func() {
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
				done <- serr
				return
			case <-stop:
				s.shutdown()
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
					herr := s.handleRequest(m)
					if ServerLogClientErrors && herr != nil {
						log.Println(herr)
					}
				}()
			}
		}
	}()

	return
}

// Block makes you block while the server does the job of serving clients. This
// will return with an error indicating why it stopped blocking, which will
// be due to receiving a signal or because you called Stop()
func (s *Server) Block() (err error) {
	err = <-s.done
	s.db.close() //*** do one last backup?
	return
}

// Stop will cause a graceful shut down of the server.
func (s *Server) Stop() {
	s.stop <- true
}

// handleRequest parses the bytes received from a connected client in to a
// clientRequest, does the requested work, then responds back to the client with
// a serverResponse
func (s *Server) handleRequest(m *mangos.Message) error {
	dec := codec.NewDecoderBytes(m.Body, s.ch)
	cr := &clientRequest{}
	err := dec.Decode(cr)
	if err != nil {
		return err
	}

	s.Lock()
	q, existed := s.qs[cr.Queue]
	if !existed {
		q = queue.New(cr.Queue)
		s.qs[cr.Queue] = q

		// we set a callback for things entering the this queue's ready
		// sub-queue. This function will be called in a go routine and receives
		// a slice of all the ready jobs. Based on the scheduler, we add to
		// each job a schedulerGroup, which the runners we spawn will be able
		// to pass to ReserveFiltered so that they run the correct jobs for
		// the machine and resource reservations they're running under
		q.SetReadyAddedCallback(func(queuename string, allitemdata []interface{}) {
			//*** for now, scheduler stuff is not implemented, so we just set
			// schedulerGroup to match the reqs provided
			for _, inter := range allitemdata {
				job := inter.(*Job)
				job.schedulerGroup = fmt.Sprintf("%d.%.0f.%d", job.Memory, job.Time.Seconds(), job.CPUs)
			}
		})
	}
	s.Unlock()

	var sr *serverResponse
	var srerr string
	var qerr string

	switch cr.Method {
	case "ping":
		// do nothing - not returning an error to client means ping success
	case "sstats":
		sr = &serverResponse{SStats: &ServerStats{ServerInfo: s.ServerInfo}}
	case "add":
		// add jobs to the queue, and along side keep the environment variables
		// they're supposed to execute under.
		if cr.Env == nil || cr.Jobs == nil {
			srerr = ErrBadRequest
		} else {
			// Store Env
			envkey, err := s.db.storeEnv(cr.Env)
			if err != nil {
				srerr = ErrDBError
				qerr = err.Error()
			} else {
				var itemdefs []*queue.ItemDef
				ok := true
				for _, encjob := range cr.Jobs {
					job := &Job{}
					dec := codec.NewDecoderBytes(encjob[2], s.ch)
					err = dec.Decode(job)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
						ok = false
						break
					}
					if job.EnvKey != envkey {
						srerr = ErrBadRequest
						ok = false
						break
					}
					itemdefs = append(itemdefs, &queue.ItemDef{string(encjob[0]), job, job.Priority, 0 * time.Second, ServerItemTTR})
				}

				if ok {
					// keep an on-disk record of these new jobs; we sacrifice a
					// lot of speed by waiting on this database write to persist
					// to disk. The alternative would be to return success to
					// the client as soon as the jobs were in the in-memory
					// queue, then lazily persist to disk in a goroutine, but we
					// must guarantee that jobs are never lost or a pipeline
					// could hopelessly break if the server node goes down
					// between returning success and the write to disk
					// succeeding. (If we don't return success to the client, it
					// won't Remove the job that created the new jobs from the
					// queue and when we recover, at worst the creating job will
					// be run again - no jobs get lost.)
					err = s.db.storeNewJobs(cr.Jobs)
					if err != nil {
						srerr = ErrDBError
						qerr = err.Error()
					} else {
						// add the jobs to the in-memory job queue
						added, dups, err := q.AddMany(itemdefs)
						if err != nil {
							srerr = ErrInternalError
							qerr = err.Error()
						}
						sr = &serverResponse{Added: added, Existed: dups}
					}
				}
			}
		}
	case "reserve":
		// return the next ready job
		if cr.ClientID.String() == "00000000-0000-0000-0000-000000000000" {
			srerr = ErrBadRequest
		} else {
			// first just try to Reserve normally
			var item *queue.Item
			var err error
			var rf queue.ReserveFilter
			if cr.SchedulerGroup != "" {
				rf = func(data interface{}) bool {
					job := data.(*Job)
					if job.schedulerGroup == cr.SchedulerGroup {
						return true
					}
					return false
				}
				item, err = q.ReserveFiltered(rf)
			} else {
				item, err = q.Reserve()
			}
			var job *Job
			if err != nil {
				if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrNothingReady {
					// there's nothing in the ready sub queue right now, so every
					// second try and Reserve() from the queue until either we get
					// an item, or we exceed the client's timeout
					var stop <-chan time.Time
					if cr.Timeout.Nanoseconds() > 0 {
						stop = time.After(cr.Timeout)
					} else {
						stop = make(chan time.Time)
					}

					joberrch := make(chan *jobErr, 1)
					ticker := time.NewTicker(ServerReserveTicker)
					go func() {
						for {
							select {
							case <-ticker.C:
								var item *queue.Item
								var err error
								if cr.SchedulerGroup != "" {
									item, err = q.ReserveFiltered(rf)
								} else {
									item, err = q.Reserve()
								}
								if err != nil {
									if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrNothingReady {
										continue
									}
									ticker.Stop()
									if qerr, ok := err.(queue.Error); ok && qerr.Err == queue.ErrQueueClosed {
										joberrch <- &jobErr{err: ErrQueueClosed}
									} else {
										joberrch <- &jobErr{err: ErrInternalError}
									}
									return
								}
								ticker.Stop()
								joberrch <- &jobErr{job: item.Data.(*Job)}
								return
							case <-stop:
								ticker.Stop()
								// if we time out, we'll return nil job and nil err
								joberrch <- &jobErr{}
								return
							}
						}
					}()
					joberr := <-joberrch
					close(joberrch)
					job = joberr.job
					srerr = joberr.err
				}
			} else {
				job = item.Data.(*Job)
			}
			if job != nil {
				job.ReservedBy = cr.ClientID //*** we should unset this on moving out of run state, to save space
				job.EnvC = s.db.retrieveEnv(job.EnvKey)
				job.Exited = false
				sr = &serverResponse{Job: job}
			}
		}
	case "jstart":
		// update the job's cmd-started-related properties
		var job *Job
		_, job, srerr = s.getij(cr, q)
		if srerr == "" {
			if cr.Job.Pid <= 0 || cr.Job.Host == "" {
				srerr = ErrBadRequest
			} else {
				job.Pid = cr.Job.Pid
				job.Host = cr.Job.Host
				job.starttime = time.Now()
				var tend time.Time
				job.endtime = tend
			}
		}
	case "jtouch":
		// update the job's ttr
		var item *queue.Item
		item, _, srerr = s.getij(cr, q)
		if srerr == "" {
			err = q.Touch(item.Key)
			if err != nil {
				srerr = ErrInternalError
				qerr = err.Error()
			}
		}
	case "jend":
		// update the job's cmd-ended-related properties
		var job *Job
		_, job, srerr = s.getij(cr, q)
		if srerr == "" {
			job.Exited = true
			job.Exitcode = cr.Job.Exitcode
			job.Peakmem = cr.Job.Peakmem
			job.CPUtime = cr.Job.CPUtime
			job.endtime = time.Now()
			err := s.db.updateJobStd(jobKey(job), cr.Job.Exitcode, cr.Job.StdOutC, cr.Job.StdErrC)
			if err != nil {
				srerr = ErrDBError
				qerr = err.Error()
			}
		}
	case "getbc":
		// get jobs by their Cmds & Cwds
		if cr.CCs == nil {
			srerr = ErrBadRequest
		} else {
			var jobs []*Job
			var notfound []string
			for _, cc := range cr.CCs {
				jobkey := byteKey([]byte(fmt.Sprintf("%s.%s", cc[0], cc[1])))
				var job *Job

				// try and get the job from the in-memory queue
				item, err := q.Get(jobkey)
				if err == nil && item != nil {
					job = s.itemToJob(item, cr.GetStd, cr.GetEnv)
				} else {
					notfound = append(notfound, jobkey)
				}

				if job != nil {
					jobs = append(jobs, job)
				}
			}

			if len(notfound) > 0 {
				// try and get the jobs from the permanent store
				found, err := s.db.retrieveCompleteJobsByKeys(notfound, cr.GetStd, cr.GetEnv)
				if err != nil {
					srerr = ErrDBError
					qerr = err.Error()
				} else if len(found) > 0 {
					jobs = append(jobs, found...)
				}
			}

			if len(jobs) > 0 {
				sr = &serverResponse{Jobs: jobs}
			}
		}
	case "getbr":
		// get jobs by their RepGroup
		if cr.Job == nil || cr.Job.RepGroup == "" {
			srerr = ErrBadRequest
		} else {
			var jobs []*Job
			//***
			sr = &serverResponse{Jobs: jobs}
		}
	default:
		srerr = ErrUnknownCommand
	}

	// on error, just send the error back to client and return a more detailed
	// error for logging
	if srerr != "" {
		s.reply(m, &serverResponse{Err: srerr})
		if qerr == "" {
			qerr = srerr
		}
		key := ""
		if cr.Job != nil {
			key = jobKey(cr.Job)
		}
		return Error{cr.Queue, cr.Method, key, qerr}
	}

	// some commands don't return anything to the client
	if sr == nil {
		sr = &serverResponse{}
	}

	// send reply to client
	err = s.reply(m, sr)
	if err != nil {
		// log failure to reply
		return err
	}
	return nil
}

// for the many j* methods in handleRequest, we do this common stuff to get
// the desired item and job
func (s *Server) getij(cr *clientRequest, q *queue.Queue) (item *queue.Item, job *Job, errs string) {
	// clientRequest must have a Job
	if cr.Job == nil {
		errs = ErrBadRequest
		return
	}

	item, err := q.Get(jobKey(cr.Job))
	if err != nil || item.Stats().State != "run" {
		errs = ErrBadJob
		return
	}
	job = item.Data.(*Job)

	if !uuid.Equal(cr.ClientID, job.ReservedBy) {
		errs = ErrMustReserve
	}

	return
}

// for the many get* methods in handleRequest, we do this common stuff to get
// an item's job from the in-memory queue formulated for the client
func (s *Server) itemToJob(item *queue.Item, getstd bool, getenv bool) (job *Job) {
	sjob := item.Data.(*Job)
	stats := item.Stats()

	state := "unknown"
	switch stats.State {
	case "delay":
		state = "delayed"
	case "ready":
		state = "ready"
	case "run":
		state = "running"
	case "bury":
		state = "buried"
	}

	// we're going to fill in some properties of the Job and return
	// it to client, but don't want those properties set here for
	// us, so we make a new Job and fill stuff in that
	job = &Job{
		RepGroup: sjob.RepGroup,
		ReqGroup: sjob.ReqGroup,
		Cmd:      sjob.Cmd,
		Cwd:      sjob.Cwd,
		Memory:   sjob.Memory,
		Time:     sjob.Time,
		CPUs:     sjob.CPUs,
		Priority: sjob.Priority,
		Peakmem:  sjob.Peakmem,
		Exited:   sjob.Exited,
		Exitcode: sjob.Exitcode,
		Pid:      sjob.Pid,
		Host:     sjob.Host,
		CPUtime:  sjob.CPUtime,
		State:    state,
		Attempts: stats.Reserves,
	}

	if !sjob.starttime.IsZero() {
		if sjob.endtime.IsZero() || state == "running" {
			job.Walltime = time.Since(sjob.starttime)
		} else {
			job.Walltime = sjob.endtime.Sub(sjob.starttime)
		}
	}
	if getenv {
		job.EnvC = s.db.retrieveEnv(sjob.EnvKey)
	}
	if getstd && job.Exited && job.Exitcode != 0 {
		job.StdOutC, job.StdErrC = s.db.retrieveJobStd(jobKey(job))
	}

	return
}

// reply to a client
func (s *Server) reply(m *mangos.Message, sr *serverResponse) (err error) {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, s.ch)
	err = enc.Encode(sr)
	if err != nil {
		return
	}
	m.Body = encoded
	err = s.sock.SendMsg(m)
	return
}

// shutdown stops listening to client connections, close all queues and
// persists them to disk
func (s *Server) shutdown() {
	s.sock.Close()
	s.db.close()

	//*** we want to persist production queues to disk

	// clean up our queues and empty everything out to be garbage collected,
	// in case the same process calls Serve() again after this
	for _, q := range s.qs {
		q.Destroy()
	}
	s.qs = nil
}
