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

// This file contains the command line interface code of the server.

import (
	"github.com/go-mangos/mangos"
	"github.com/satori/go.uuid"
	"github.com/sb10/vrpipe/queue"
	"github.com/ugorji/go/codec"
	"time"
)

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

	q := s.getOrCreateQueue(cr.Queue)

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
				for _, job := range cr.Jobs {
					job.EnvKey = envkey
					job.UntilBuried = 3
					job.Queue = cr.Queue
					itemdefs = append(itemdefs, &queue.ItemDef{jobKey(job), job, job.Priority, 0 * time.Second, ServerItemTTR})
				}

				// keep an on-disk record of these new jobs; we sacrifice a lot
				// of speed by waiting on this database write to persist to
				// disk. The alternative would be to return success to the
				// client as soon as the jobs were in the in-memory queue, then
				// lazily persist to disk in a goroutine, but we must guarantee
				// that jobs are never lost or a pipeline could hopelessly break
				// if the server node goes down between returning success and
				// the write to disk succeeding. (If we don't return success to
				// the client, it won't Remove the job that created the new jobs
				// from the queue and when we recover, at worst the creating job
				// will be run again - no jobs get lost.)
				err = s.db.storeNewJobs(cr.Jobs)
				if err != nil {
					srerr = ErrDBError
					qerr = err.Error()
				} else {
					// add the jobs to the in-memory job queue
					added, dups, err := s.enqueueItems(q, itemdefs)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					}

					sr = &serverResponse{Added: added, Existed: dups}
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
				// if this is the first job that the client is trying to
				// reserve, and if we don't actually want any more clients
				// working on this schedulerGroup, we'll just act as if nothing
				// was ready
				skip := false
				if cr.FirstReserve && s.rc != "" {
					s.sgcmutex.Lock()
					if count, existed := s.sgroupcounts[cr.SchedulerGroup]; !existed || count == 0 {
						skip = true
					}
					s.sgcmutex.Unlock()
				}

				if !skip {
					rf = func(data interface{}) bool {
						job := data.(*Job)
						if job.schedulerGroup == cr.SchedulerGroup {
							return true
						}
						return false
					}
					item, err = q.ReserveFiltered(rf)
				}
			} else {
				item, err = q.Reserve()
			}
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

					itemerrch := make(chan *itemErr, 1)
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
										itemerrch <- &itemErr{err: ErrQueueClosed}
									} else {
										itemerrch <- &itemErr{err: ErrInternalError}
									}
									return
								}
								ticker.Stop()
								itemerrch <- &itemErr{item: item}
								return
							case <-stop:
								ticker.Stop()
								// if we time out, we'll return nil job and nil err
								itemerrch <- &itemErr{}
								return
							}
						}
					}()
					itemerr := <-itemerrch
					close(itemerrch)
					item = itemerr.item
					srerr = itemerr.err
				}
			}
			if srerr == "" && item != nil {
				// clean up any past state to have a fresh job ready to run
				sjob := item.Data.(*Job)
				sjob.ReservedBy = cr.ClientID //*** we should unset this on moving out of run state, to save space
				sjob.Exited = false
				sjob.Pid = 0
				sjob.Host = ""
				var tnil time.Time
				sjob.starttime = tnil
				sjob.endtime = tnil
				sjob.Peakmem = 0
				sjob.Exitcode = -1

				// make a copy of the job with some extra stuff filled in (that
				// we don't want taking up memory here) for the client
				job := s.itemToJob(item, false, true)
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
				job.Attempts++
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
			s.db.updateJobAfterExit(job, cr.Job.StdOutC, cr.Job.StdErrC)
		}
	case "jarchive":
		// remove the job from the queue, rpl and live bucket and add to
		// complete bucket
		var job *Job
		_, job, srerr = s.getij(cr, q)
		if srerr == "" {
			if !job.Exited || job.Exitcode != 0 || job.starttime.IsZero() || job.endtime.IsZero() {
				srerr = ErrBadRequest
			} else {
				key := jobKey(job)
				job.State = "complete"
				job.FailReason = ""
				job.Walltime = job.endtime.Sub(job.starttime)
				err := s.db.archiveJob(key, job)
				if err != nil {
					srerr = ErrDBError
					qerr = err.Error()
				} else {
					err = q.Remove(key)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					} else {
						s.rpl.Lock()
						if m, exists := s.rpl.lookup[job.RepGroup]; exists {
							delete(m, key)
						}
						s.rpl.Unlock()

						//log.Println("jarchive will decrement")
						s.decrementGroupCount(job.schedulerGroup, q)
					}
				}
			}
		}
	case "jrelease":
		// move the job from the run queue to the delay queue, unless it has
		// failed too many times, in which case bury
		var item *queue.Item
		var job *Job
		item, job, srerr = s.getij(cr, q)
		if srerr == "" {
			job.FailReason = cr.Job.FailReason
			if job.Exited && job.Exitcode != 0 {
				job.UntilBuried--
			}
			if job.UntilBuried <= 0 {
				err = q.Bury(item.Key)
				if err != nil {
					srerr = ErrInternalError
					qerr = err.Error()
				}
			} else {
				err = q.SetDelay(item.Key, cr.Timeout)
				if err != nil {
					srerr = ErrInternalError
					qerr = err.Error()
				} else {
					err = q.Release(item.Key)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					}
				}
			}
		}
	case "jbury":
		// move the job from the run queue to the bury queue
		var item *queue.Item
		var job *Job
		item, job, srerr = s.getij(cr, q)
		if srerr == "" {
			job.FailReason = cr.Job.FailReason
			err = q.Bury(item.Key)
			if err != nil {
				srerr = ErrInternalError
				qerr = err.Error()
			} else {
				//log.Println("jbury will decrement")
				s.decrementGroupCount(job.schedulerGroup, q)
			}
		}
	case "jkick":
		// move the jobs from the bury queue to the ready queue; unlike the
		// other j* methods, client doesn't have to be the Reserve() owner of
		// these jobs, and we don't want the "in run queue" test
		if cr.Keys == nil {
			srerr = ErrBadRequest
		} else {
			kicked := 0
			for _, jobkey := range cr.Keys {
				item, err := q.Get(jobkey)
				if err != nil || item.Stats().State != "bury" {
					continue
				}
				err = q.Kick(jobkey)
				if err == nil {
					job := item.Data.(*Job)
					job.UntilBuried = 3
					kicked++
				}
			}
			sr = &serverResponse{Existed: kicked}
		}
	case "jdel":
		// remove the jobs from the bury queue and the live bucket
		if cr.Keys == nil {
			srerr = ErrBadRequest
		} else {
			deleted := 0
			for _, jobkey := range cr.Keys {
				item, err := q.Get(jobkey)
				if err != nil || item.Stats().State != "bury" {
					continue
				}
				err = q.Remove(jobkey)
				if err == nil {
					deleted++
					s.db.deleteLiveJob(jobkey) //*** probably want to batch this up to delete many at once
				}
			}
			sr = &serverResponse{Existed: deleted}
		}
	case "getbc":
		// get jobs by their keys (which come from their Cmds & Cwds)
		if cr.Keys == nil {
			srerr = ErrBadRequest
		} else {
			var jobs []*Job
			jobs, srerr, qerr = s.getJobsByKeys(q, cr.Keys, cr.GetStd, cr.GetEnv)
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
			jobs, srerr, qerr = s.getJobsByRepGroup(q, cr.Job.RepGroup, cr.Limit, cr.State, cr.GetStd, cr.GetEnv)
			if len(jobs) > 0 {
				sr = &serverResponse{Jobs: jobs}
			}
		}
	case "getin":
		// get all jobs in the jobqueue
		jobs := s.getJobsCurrent(q, cr.Limit, cr.State, cr.GetStd, cr.GetEnv)
		if len(jobs) > 0 {
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
// the desired item and job.
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
// an item's job from the in-memory queue formulated for the client.
func (s *Server) itemToJob(item *queue.Item, getStd bool, getEnv bool) (job *Job) {
	sjob := item.Data.(*Job)
	stats := item.Stats()

	state := "unknown"
	switch stats.State {
	case "delay":
		state = "delayed"
	case "ready":
		state = "ready"
	case "run":
		state = "reserved"
	case "bury":
		state = "buried"
	}

	// we're going to fill in some properties of the Job and return
	// it to client, but don't want those properties set here for
	// us, so we make a new Job and fill stuff in that
	job = &Job{
		RepGroup:    sjob.RepGroup,
		ReqGroup:    sjob.ReqGroup,
		Cmd:         sjob.Cmd,
		Cwd:         sjob.Cwd,
		Memory:      sjob.Memory,
		Time:        sjob.Time,
		CPUs:        sjob.CPUs,
		Priority:    sjob.Priority,
		Peakmem:     sjob.Peakmem,
		Exited:      sjob.Exited,
		Exitcode:    sjob.Exitcode,
		FailReason:  sjob.FailReason,
		Pid:         sjob.Pid,
		Host:        sjob.Host,
		CPUtime:     sjob.CPUtime,
		State:       state,
		Attempts:    sjob.Attempts,
		UntilBuried: sjob.UntilBuried,
		ReservedBy:  sjob.ReservedBy,
		EnvKey:      sjob.EnvKey,
	}

	if !sjob.starttime.IsZero() {
		if sjob.endtime.IsZero() || state == "reserved" {
			job.Walltime = time.Since(sjob.starttime)
		} else {
			job.Walltime = sjob.endtime.Sub(sjob.starttime)
		}
		state = "running"
	}
	s.jobPopulateStdEnv(job, getStd, getEnv)
	return
}

// jobPopulateStdEnv fills in the StdOutC, StdErrC and EnvC values for a Job,
// extracting them from the database.
func (s *Server) jobPopulateStdEnv(job *Job, getStd bool, getEnv bool) {
	if getStd && job.Exited && job.Exitcode != 0 {
		job.StdOutC, job.StdErrC = s.db.retrieveJobStd(jobKey(job))
	}
	if getEnv {
		job.EnvC = s.db.retrieveEnv(job.EnvKey)
	}
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
