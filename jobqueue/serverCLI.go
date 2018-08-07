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

// This file contains the command line interface code of the server.

import (
	"bytes"
	"sync"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/ugorji/go/codec"
	"nanomsg.org/go-mangos"
)

// handleRequest parses the bytes received from a connected client in to a
// clientRequest, does the requested work, then responds back to the client with
// a serverResponse
func (s *Server) handleRequest(m *mangos.Message) error {
	dec := codec.NewDecoderBytes(m.Body, s.ch)
	cr := &clientRequest{}
	errd := dec.Decode(cr)
	if errd != nil {
		return errd
	}

	var sr *serverResponse
	var srerr string
	var qerr string

	s.ssmutex.RLock()
	up := s.up
	drain := s.drain
	s.ssmutex.RUnlock()

	// check that the client making the request has the expected token
	if (len(cr.Token) != tokenLength || !tokenMatches(cr.Token, s.token)) && cr.Method != "ping" {
		srerr = ErrPermissionDenied
		qerr = "Client presented the wrong token"
	} else if s.q == nil || (!up && !drain) {
		// the server just got shutdown
		srerr = ErrClosedStop
		qerr = "The server has been stopped"
	} else {
		switch cr.Method {
		case "ping":
			// avoid a later race condition when we try to encode ServerInfo by
			// doing the read here, copying it under read lock
			s.ssmutex.RLock()
			si := &ServerInfo{}
			*si = *s.ServerInfo
			s.ssmutex.RUnlock()
			sr = &serverResponse{SInfo: si}
		case "backup":
			s.Debug("backup requested")
			// make an io.Writer that writes to a byte slice, so we can return
			// the db as that
			var b bytes.Buffer
			err := s.BackupDB(&b)
			if err != nil {
				srerr = ErrInternalError
				qerr = err.Error()
			} else {
				sr = &serverResponse{DB: b.Bytes()}
			}
		case "drain":
			s.Debug("drain requested")
			err := s.Drain()
			if err != nil {
				srerr = ErrInternalError
				qerr = err.Error()
			} else {
				sr = &serverResponse{SStats: s.GetServerStats()}
			}
		case "shutdown":
			s.Debug("shutdown requested")
			s.Stop(true)
		case "upload":
			// upload file to us
			if cr.File == nil {
				srerr = ErrBadRequest
			} else {
				data, err := decompress(cr.File)
				if err != nil {
					srerr = ErrInternalError
					qerr = err.Error()
				} else {
					r := bytes.NewReader(data)
					path, err := s.uploadFile(r, cr.Path)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					} else {
						sr = &serverResponse{Path: path}
					}
				}
			}
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
					if srerr == "" {
						// create the jobs server-side
						added, dups, alreadyComplete, thisSrerr, err := s.createJobs(cr.Jobs, envkey, cr.IgnoreComplete)
						if err != nil {
							srerr = thisSrerr
							qerr = err.Error()
						} else {
							s.Debug("added jobs", "new", added, "dups", dups, "complete", alreadyComplete)
							sr = &serverResponse{Added: added, Existed: dups + alreadyComplete}
						}
					}
				}
			}
		case "reserve":
			// return the next ready job
			if cr.ClientID.String() == "00000000-0000-0000-0000-000000000000" {
				srerr = ErrBadRequest
			} else if !drain {
				// first just try to Reserve normally
				var item *queue.Item
				var err error
				if cr.SchedulerGroup != "" {
					// if this is the first job that the client is trying to
					// reserve, and if we don't actually want any more clients
					// working on this schedulerGroup, we'll just act as if nothing
					// was ready. Likewise if in drain mode.
					skip := false
					if cr.FirstReserve && s.rc != "" {
						s.sgcmutex.Lock()
						if count, existed := s.sgroupcounts[cr.SchedulerGroup]; !existed || count == 0 {
							skip = true
						}
						s.sgcmutex.Unlock()
					}

					if !skip {
						item, err = s.q.Reserve(cr.SchedulerGroup)
					}
				} else {
					item, err = s.q.Reserve()
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
							defer internal.LogPanic(s.Logger, "reserve", true)

							for {
								select {
								case <-ticker.C:
									itemr, err := s.q.Reserve(cr.SchedulerGroup)
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
									itemerrch <- &itemErr{item: itemr}
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
					sjob.Lock()
					sjob.ReservedBy = cr.ClientID //*** we should unset this on moving out of run state, to save space
					sjob.Exited = false
					sjob.Pid = 0
					sjob.Host = ""
					var tnil time.Time
					sjob.StartTime = tnil
					sjob.EndTime = tnil
					sjob.PeakRAM = 0
					sjob.PeakDisk = 0
					sjob.Exitcode = -1
					sgroup := sjob.schedulerGroup
					sjob.Unlock()

					errd := s.q.SetDelay(item.Key, ClientReleaseDelay)
					if errd != nil {
						s.Warn("reserve queue SetDelay failed", "err", errd)
					}

					// make a copy of the job with some extra stuff filled in (that
					// we don't want taking up memory here) for the client
					job := s.itemToJob(item, false, true)
					sr = &serverResponse{Job: job}
					s.Debug("reserved job", "cmd", job.Cmd, "schedGrp", sgroup)
				}
			} // else we'll return nothing, as if there were no jobs in the queue
		case "jstart":
			// update the job's cmd-started-related properties
			var job *Job
			_, job, srerr = s.getij(cr)
			if srerr == "" {
				job.Lock()
				if cr.Job.Pid <= 0 || cr.Job.Host == "" {
					srerr = ErrBadRequest
				} else {
					job.Host = cr.Job.Host
					if job.Host != "" {
						job.HostID = s.scheduler.HostToID(job.Host)
					}
					job.HostIP = cr.Job.HostIP
					job.Pid = cr.Job.Pid
					job.StartTime = time.Now()
					var tend time.Time
					job.EndTime = tend
					job.Attempts++
					job.killCalled = false
					job.Lost = false
				}
				job.Unlock()
			}
		case "jtouch":
			var job *Job
			var item *queue.Item
			item, job, srerr = s.getij(cr)
			if srerr == "" {
				// if kill has been called for this job, just return KillCalled
				job.RLock()
				killCalled := job.killCalled
				lost := job.Lost
				job.RUnlock()

				if !killCalled {
					// also just return killCalled if server has been set to
					// kill all jobs
					s.krmutex.RLock()
					killCalled = s.killRunners
					s.krmutex.RUnlock()
				}

				if !killCalled {
					// else, update the job's ttr
					err := s.q.Touch(item.Key)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					} else if lost {
						job.Lock()
						job.Lost = false
						job.EndTime = time.Time{}
						job.Unlock()

						// since our changed callback won't be called, send out
						// this transition from lost to running state
						s.statusCaster.Send(&jstateCount{"+all+", JobStateLost, JobStateRunning, 1})
						s.statusCaster.Send(&jstateCount{job.RepGroup, JobStateLost, JobStateRunning, 1})
					}
				}
				sr = &serverResponse{KillCalled: killCalled}
			}
		case "jarchive":
			// remove the job from the queue, rpl and live bucket and add to
			// complete bucket
			var item *queue.Item
			var job *Job
			item, job, srerr = s.getij(cr)
			if srerr == "" {
				// first check the item is still in the run queue (eg. the job
				// wasn't released by another process; unlike the other methods,
				// queue package does not check we're in the run queue when
				// Remove()ing, since you can remove from any queue)
				job.updateAfterExit(cr.JobEndState)
				job.Lock()
				if running := item.Stats().State == queue.ItemStateRun; !running {
					srerr = ErrBadJob
					job.Unlock()
				} else if !job.Exited || job.Exitcode != 0 || job.StartTime.IsZero() || job.EndTime.IsZero() {
					srerr = ErrBadRequest
					job.Unlock()
				} else {
					key := job.Key()
					job.State = JobStateComplete
					job.FailReason = ""
					sgroup := job.schedulerGroup
					rgroup := job.RepGroup
					job.Unlock()
					err := s.db.archiveJob(key, job)
					if err != nil {
						srerr = ErrDBError
						qerr = err.Error()
					} else {
						err = s.q.Remove(key)
						if err != nil {
							srerr = ErrInternalError
							qerr = err.Error()
						} else {
							s.rpl.Lock()
							if m, exists := s.rpl.lookup[rgroup]; exists {
								delete(m, key)
							}
							s.rpl.Unlock()
							s.Debug("completed job", "cmd", job.Cmd, "schedGrp", sgroup)
							go func(group string) {
								defer internal.LogPanic(s.Logger, "jarchive", true)
								s.decrementGroupCount(group)
							}(sgroup)
						}
					}
				}
			}
		case "jrelease":
			// move the job from the run queue to the delay queue, unless it has
			// failed too many times, in which case bury
			var item *queue.Item
			var job *Job
			item, job, srerr = s.getij(cr)
			if srerr == "" {
				job.updateAfterExit(cr.JobEndState)
				job.Lock()
				job.FailReason = cr.Job.FailReason
				if !job.StartTime.IsZero() {
					// obey jobs's Retries count by adjusting UntilBuried if a
					// client reserved this job and started to run the job's cmd
					job.UntilBuried--
				}
				if job.Exited && job.Exitcode != 0 {
					job.updateRecsAfterFailure()
				}
				if job.UntilBuried <= 0 {
					sgroup := job.schedulerGroup
					job.Unlock()
					err := s.q.Bury(item.Key)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					} else {
						s.decrementGroupCount(job.getSchedulerGroup())
						s.db.updateJobAfterExit(job, cr.Job.StdOutC, cr.Job.StdErrC, true)
						s.Debug("buried job", "cmd", job.Cmd, "schedGrp", sgroup)
					}
				} else {
					sgroup := job.schedulerGroup
					job.Unlock()
					err := s.q.Release(item.Key)
					if err != nil {
						srerr = ErrInternalError
						qerr = err.Error()
					} else {
						s.decrementGroupCount(job.getSchedulerGroup())
						s.db.updateJobAfterExit(job, cr.Job.StdOutC, cr.Job.StdErrC, true)
						s.Debug("released job", "cmd", job.Cmd, "schedGrp", sgroup)
					}
				}
			}
		case "jbury":
			// move the job from the run queue to the bury queue
			var item *queue.Item
			var job *Job
			item, job, srerr = s.getij(cr)
			if srerr == "" {
				job.updateAfterExit(cr.JobEndState)
				job.Lock()
				job.FailReason = cr.Job.FailReason
				sgroup := job.schedulerGroup
				job.Unlock()
				err := s.q.Bury(item.Key)
				if err != nil {
					srerr = ErrInternalError
					qerr = err.Error()
				} else {
					s.decrementGroupCount(job.getSchedulerGroup())
					s.db.updateJobAfterExit(job, cr.Job.StdOutC, cr.Job.StdErrC, true)
					s.Debug("buried job", "cmd", job.Cmd, "schedGrp", sgroup)
				}
			}
		case "jkick":
			// move the jobs from the bury queue to the ready queue; unlike the
			// other j* methods, client doesn't have to be the Reserve() owner
			// of these jobs, and we don't want the "in run queue" test
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				kicked := 0
				for _, jobkey := range cr.Keys {
					item, err := s.q.Get(jobkey)
					if err != nil || item.Stats().State != queue.ItemStateBury {
						continue
					}
					err = s.q.Kick(jobkey)
					if err == nil {
						job := item.Data.(*Job)
						job.Lock()
						job.UntilBuried = job.Retries + 1
						s.Debug("unburied job", "cmd", job.Cmd, "schedGrp", job.schedulerGroup)
						job.Unlock()
						kicked++
					}
				}
				sr = &serverResponse{Existed: kicked}
			}
		case "jdel":
			// remove the jobs from the bury/delay/dependent/ready queue and the
			// live bucket
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				deleted := 0
				keys := cr.Keys
				for {
					var skippedDeps []string
					removedJobs := false
					for _, jobkey := range keys {
						item, err := s.q.Get(jobkey)
						if err != nil || item == nil {
							continue
						}
						iState := item.Stats().State
						if iState == queue.ItemStateRun {
							continue
						}

						// we can't allow the removal of jobs that have
						// dependencies, as *queue would regard that as satisfying
						// the dependency and downstream jobs would start
						hasDeps, err := s.q.HasDependents(jobkey)
						if err != nil || hasDeps {
							if hasDeps {
								skippedDeps = append(skippedDeps, jobkey)
							}
							continue
						}
						err = s.q.Remove(jobkey)
						if err == nil {
							deleted++
							removedJobs = true
							s.db.deleteLiveJob(jobkey) //*** probably want to batch this up to delete many at once
						}
					}

					// if we removed at least 1 job, and skipped any due to
					// deps, repeat and see if we can remove everything desired
					// by going down the dependency tree
					if len(skippedDeps) > 0 && removedJobs {
						keys = skippedDeps
						continue
					}
					break
				}
				s.Debug("deleted jobs", "count", deleted)
				sr = &serverResponse{Existed: deleted}
			}
		case "jkill":
			// set the killCalled property on the jobs, to change the subsequent
			// behaviour of jtouch; as per jkick, client doesn't have to be the
			// Reserve() owner of these jobs, though we do want the "in run
			// queue" test
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				killable := 0
				for _, jobkey := range cr.Keys {
					k, err := s.killJob(jobkey)
					if err != nil {
						continue
					}
					if k {
						killable++
					}
				}
				s.Debug("killed jobs", "count", killable)
				sr = &serverResponse{Existed: killable}
			}
		case "getbc":
			// get jobs by their keys (which come from their Cmds & Cwds)
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				var jobs []*Job
				jobs, srerr, qerr = s.getJobsByKeys(cr.Keys, cr.GetStd, cr.GetEnv)
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
				jobs, srerr, qerr = s.getJobsByRepGroup(cr.Job.RepGroup, cr.Search, cr.Limit, cr.State, cr.GetStd, cr.GetEnv)
				if len(jobs) > 0 {
					sr = &serverResponse{Jobs: jobs}
				}
			}
		case "getin":
			// get all jobs in the jobqueue
			jobs := s.getJobsCurrent(cr.Limit, cr.State, cr.GetStd, cr.GetEnv)
			if len(jobs) > 0 {
				sr = &serverResponse{Jobs: jobs}
			}
		default:
			srerr = ErrUnknownCommand
		}
	}

	// on error, just send the error back to client and return a more detailed
	// error for logging
	if srerr != "" {
		errr := s.reply(m, &serverResponse{Err: srerr})
		if errr != nil {
			s.Warn("reply to client failed", "err", errr)
		}
		if qerr == "" {
			qerr = srerr
		}
		key := ""
		if cr.Job != nil {
			key = cr.Job.Key()
		}
		return Error{cr.Method, key, qerr}
	}

	// some commands don't return anything to the client
	if sr == nil {
		sr = &serverResponse{}
	}

	// send reply to client
	return s.reply(m, sr) // *** log failure to reply?
}

// logTimings will log the average took after 1000 calls to this message with
// the same desc.
func (s *Server) logTimings(desc string, took time.Duration) {
	if desc == "" {
		return
	}

	s.tmutex.Lock()
	if _, exists := s.timings[desc]; !exists {
		s.timings[desc] = &timingAvg{}
	}
	avg := s.timings[desc].store(took.Seconds())
	s.tmutex.Unlock()
	if avg > 0 {
		s.Info("timing", "desc", desc, "avg", avg)
	}
}

// timingAvg is used by logTimings to do the averaging
type timingAvg struct {
	timings [1000]float64
	count   int
	sync.Mutex
}

// store stores the supplied float64, and when there are 1000 of them returns
// the average and resets. Otherwise returns 0.
func (a *timingAvg) store(s float64) float64 {
	a.Lock()
	defer a.Unlock()
	a.timings[a.count] = s
	a.count++
	if a.count == 1000 {
		sum := float64(0)
		for i := range &a.timings {
			sum += a.timings[i]
		}
		a.timings = [1000]float64{}
		a.count = 0
		return sum / float64(1000)
	}
	return 0
}

// for the many j* methods in handleRequest, we do this common stuff to get
// the desired item and job. The returned string is one of our Err* constants.
func (s *Server) getij(cr *clientRequest) (*queue.Item, *Job, string) {
	// clientRequest must have a Job
	if cr.Job == nil {
		return nil, nil, ErrBadRequest
	}

	item, err := s.q.Get(cr.Job.Key())
	if err != nil || item.Stats().State != queue.ItemStateRun {
		return item, nil, ErrBadJob
	}
	job := item.Data.(*Job)

	if cr.ClientID != job.ReservedBy {
		return item, job, ErrMustReserve
	}

	return item, job, ""
}

func (s *Server) itemStateToJobState(itemState queue.ItemState, lost bool) JobState {
	state := itemsStateToJobState[itemState]
	if state == "" {
		state = JobStateUnknown
	} else if state == JobStateReserved && lost {
		state = JobStateLost
	}
	return state
}

// for the many get* methods in handleRequest, we do this common stuff to get
// an item's job from the in-memory queue formulated for the client.
func (s *Server) itemToJob(item *queue.Item, getStd bool, getEnv bool) *Job {
	sjob := item.Data.(*Job)
	sjob.RLock()

	stats := item.Stats()

	state := s.itemStateToJobState(stats.State, sjob.Lost)

	// we're going to fill in some properties of the Job and return
	// it to client, but don't want those properties set here for
	// us, so we make a new Job and fill stuff in that
	req := &scheduler.Requirements{}
	*req = *sjob.Requirements // copy reqs since server changes these, avoiding a race condition
	job := &Job{
		RepGroup:      sjob.RepGroup,
		ReqGroup:      sjob.ReqGroup,
		DepGroups:     sjob.DepGroups,
		Cmd:           sjob.Cmd,
		Cwd:           sjob.Cwd,
		CwdMatters:    sjob.CwdMatters,
		ChangeHome:    sjob.ChangeHome,
		ActualCwd:     sjob.ActualCwd,
		Requirements:  req,
		Priority:      sjob.Priority,
		Retries:       sjob.Retries,
		PeakRAM:       sjob.PeakRAM,
		PeakDisk:      sjob.PeakDisk,
		Exited:        sjob.Exited,
		Exitcode:      sjob.Exitcode,
		FailReason:    sjob.FailReason,
		StartTime:     sjob.StartTime,
		EndTime:       sjob.EndTime,
		Pid:           sjob.Pid,
		Host:          sjob.Host,
		HostID:        sjob.HostID,
		HostIP:        sjob.HostIP,
		CPUtime:       sjob.CPUtime,
		State:         state,
		Attempts:      sjob.Attempts,
		UntilBuried:   sjob.UntilBuried,
		ReservedBy:    sjob.ReservedBy,
		EnvKey:        sjob.EnvKey,
		EnvOverride:   sjob.EnvOverride,
		Dependencies:  sjob.Dependencies,
		Behaviours:    sjob.Behaviours,
		MountConfigs:  sjob.MountConfigs,
		MonitorDocker: sjob.MonitorDocker,
		BsubMode:      sjob.BsubMode,
		BsubID:        sjob.BsubID,
	}

	if state == JobStateReserved && !sjob.StartTime.IsZero() {
		job.State = JobStateRunning
	}
	sjob.RUnlock()
	s.jobPopulateStdEnv(job, getStd, getEnv)
	return job
}

// jobPopulateStdEnv fills in the StdOutC, StdErrC and EnvC values for a Job,
// extracting them from the database.
func (s *Server) jobPopulateStdEnv(job *Job, getStd bool, getEnv bool) {
	job.Lock()
	defer job.Unlock()
	if getStd && ((job.Exited && job.Exitcode != 0) || job.State == JobStateBuried) {
		job.StdOutC, job.StdErrC = s.db.retrieveJobStd(job.Key())
	}
	if getEnv {
		job.EnvC = s.db.retrieveEnv(job.EnvKey)
		job.EnvCRetrieved = true
	}
}

// reply to a client
func (s *Server) reply(m *mangos.Message, sr *serverResponse) error {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, s.ch)
	err := enc.Encode(sr)
	if err != nil {
		return err
	}
	m.Body = encoded
	err = s.sock.SendMsg(m)
	return err
}
