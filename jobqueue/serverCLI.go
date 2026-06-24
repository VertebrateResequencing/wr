/*******************************************************************************
 * Copyright (c) 2016-2022, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package jobqueue

// This file contains the command line interface code of the server.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

type subscriptionCatchUpRecord struct {
	job    *Job
	state  JobState
	atTime time.Time
}

func subscriptionCatchUpRecordForJob(job *Job) (string, subscriptionCatchUpRecord, bool) {
	job.RLock()
	defer job.RUnlock()

	if !subscriptionCatchUpTerminalState(job.State) {
		return "", subscriptionCatchUpRecord{}, false
	}

	atTime := job.EndTime
	if atTime.IsZero() {
		atTime = job.StartTime
	}

	return job.Key(), subscriptionCatchUpRecord{job: job, state: job.State, atTime: atTime}, true
}

func subscriptionCatchUpRepGroupRecordForJob(job *Job) (string, subscriptionCatchUpRecord, bool) {
	job.RLock()
	defer job.RUnlock()

	record := subscriptionCatchUpRecord{job: job, state: job.State}
	if !subscriptionCatchUpTerminalState(job.State) {
		return job.Key(), record, false
	}

	record.atTime = job.EndTime
	if record.atTime.IsZero() {
		record.atTime = job.StartTime
	}

	return job.Key(), record, true
}

func (s *Server) subscriptionCatchUpByKeys(ctx context.Context, keys []string) ([]*JobUpdate, error) {
	records := make(map[string]subscriptionCatchUpRecord, len(keys))
	completeKeys := make([]string, 0, len(keys))

	for _, key := range keys {
		if item, err := s.q.Get(key); err == nil && item != nil {
			addSubscriptionCatchUpRecord(records, s.itemToJob(ctx, item, false, false))

			continue
		}

		completeKeys = append(completeKeys, key)
	}

	complete, err := s.db.retrieveCompleteJobsByKeys(completeKeys)
	if err != nil {
		return nil, err
	}

	for _, job := range complete {
		addSubscriptionCatchUpRecord(records, job)
	}

	return subscriptionCatchUpKeyUpdates(keys, records), nil
}

func (s *Server) subscriptionCatchUpRepGroupRecords(ctx context.Context,
	repGroup string,
) (map[string]subscriptionCatchUpRecord, bool, error) {
	records := make(map[string]subscriptionCatchUpRecord)
	queueTerminal := addSubscriptionCatchUpRepGroupRecords(records, s.getQueueJobsByRepGroup(ctx, repGroup))

	complete, err := s.db.retrieveCompleteJobsByRepGroup(repGroup)
	if err != nil {
		return nil, false, err
	}

	completeTerminal := addSubscriptionCatchUpRepGroupRecords(records, complete)

	return records, queueTerminal && completeTerminal, nil
}

func addSubscriptionCatchUpRepGroupRecords(records map[string]subscriptionCatchUpRecord, jobs []*Job) bool {
	allTerminal := true

	for _, job := range jobs {
		if !addSubscriptionCatchUpRepGroupRecord(records, job) {
			allTerminal = false
		}
	}

	return allTerminal
}

func addSubscriptionCatchUpRepGroupRecord(records map[string]subscriptionCatchUpRecord, job *Job) bool {
	key, record, terminal := subscriptionCatchUpRepGroupRecordForJob(job)
	if !terminal {
		records[key] = record

		return false
	}

	previous, exists := records[key]
	if exists && !subscriptionCatchUpTerminalState(previous.state) {
		return true
	}

	if !exists || record.atTime.After(previous.atTime) {
		records[key] = record
	}

	return true
}

func subscriptionCatchUpTerminalState(state JobState) bool {
	return state == JobStateComplete || state == JobStateBuried
}

func addSubscriptionCatchUpRecord(records map[string]subscriptionCatchUpRecord, job *Job) {
	key, record, ok := subscriptionCatchUpRecordForJob(job)
	if !ok {
		return
	}

	previous, exists := records[key]
	if !exists || record.atTime.After(previous.atTime) {
		records[key] = record
	}
}

func subscriptionCatchUpKeyUpdates(keys []string, records map[string]subscriptionCatchUpRecord) []*JobUpdate {
	updates := make([]*JobUpdate, 0, len(records))
	seen := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}

		record, exists := records[key]
		if !exists {
			continue
		}

		updates = append(updates, subscriptionCatchUpJobUpdate(record))
	}

	return updates
}

func subscriptionCatchUpJobUpdate(record subscriptionCatchUpRecord) *JobUpdate {
	job := record.job

	job.RLock()
	defer job.RUnlock()

	return &JobUpdate{
		Started:    jobUnixNano(job.StartTime),
		Ended:      jobUnixNano(job.EndTime),
		Kind:       jobUpdateKind(record.state),
		Key:        job.Key(),
		RepGroup:   job.RepGroup,
		State:      record.state,
		Exitcode:   job.Exitcode,
		FailReason: job.FailReason,
	}
}

func subscriptionCatchUpRepGroupUpdate(repGroup string, records map[string]subscriptionCatchUpRecord) *JobUpdate {
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	update := &JobUpdate{
		Kind:     JobUpdateRepGroupDone,
		RepGroup: repGroup,
		JobKeys:  keys,
		Total:    len(keys),
	}

	for _, key := range keys {
		state := records[key].state
		update.JobStates = append(update.JobStates, state)

		switch state {
		case JobStateComplete:
			update.Complete++
		case JobStateBuried:
			update.Buried++
		default:
		}
	}

	return update
}

type liveJobUpdateData struct {
	update  *JobUpdate
	stdoutC []byte
	stderrC []byte
}

func liveJobUpdateDataFromJob(job *Job) (*liveJobUpdateData, error) {
	job.RLock()
	defer job.RUnlock()

	cwdLeaf, err := liveJobCwdLeaf(job.Cwd, job.ActualCwd)
	if err != nil {
		return nil, err
	}

	return &liveJobUpdateData{
		update: &JobUpdate{
			Kind:       JobUpdateLive,
			Key:        job.Key(),
			RepGroup:   job.RepGroup,
			State:      job.State,
			PeakRAM:    job.PeakRAM,
			PeakDisk:   job.PeakDisk,
			Pid:        job.Pid,
			CPUtime:    job.CPUtime,
			Host:       job.Host,
			HostID:     job.HostID,
			HostIP:     job.HostIP,
			CwdBase:    job.Cwd,
			Cwd:        cwdLeaf,
			SSHCommand: sshCommandForRunningJob(job.State, job.Requirements, job.Host, job.HostIP, job.ActualCwd),
		},
		stdoutC: slices.Clone(job.StdOutC),
		stderrC: slices.Clone(job.StdErrC),
	}, nil
}

func jobUpdateFromLiveJob(job *Job) (*JobUpdate, error) {
	data, err := liveJobUpdateDataFromJob(job)
	if err != nil {
		return nil, err
	}

	stdout, err := compressedStdString(data.stdoutC)
	if err != nil {
		return nil, err
	}

	stderr, err := compressedStdString(data.stderrC)
	if err != nil {
		return nil, err
	}

	data.update.StdOut = stdout
	data.update.StdErr = stderr

	return data.update, nil
}

func compressedStdString(compressed []byte) (string, error) {
	if len(compressed) == 0 {
		return "", nil
	}

	decompressed, err := decompress(compressed)
	if err != nil {
		return "", err
	}

	return string(decompressed), nil
}

func liveSnapshotPresent(jes *JobEndState) bool {
	if jes == nil {
		return false
	}

	return jes.Cwd != "" ||
		jes.PeakRAM != 0 ||
		jes.PeakDisk != 0 ||
		jes.CPUtime != 0 ||
		len(jes.Stdout) != 0 ||
		len(jes.Stderr) != 0
}

func applyLiveSnapshot(job *Job, jes *JobEndState) {
	job.Lock()
	defer job.Unlock()

	if jes.Cwd != "" {
		job.ActualCwd = jes.Cwd
	}

	job.PeakRAM = jes.PeakRAM
	job.PeakDisk = jes.PeakDisk
	job.CPUtime = jes.CPUtime
	job.StdOutC = jes.Stdout
	job.StdErrC = jes.Stderr
}

func liveJobCwdLeaf(cwdBase, cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}

	if cwdBase == "" {
		return cwd, nil
	}

	rel, err := filepath.Rel(cwdBase, cwd)
	if err != nil {
		return "", err
	}

	return "/" + rel, nil
}

func (s *Server) subscriptionCatchUpByRepGroup(ctx context.Context, repGroup string) ([]*JobUpdate, error) {
	records, allTerminal, err := s.subscriptionCatchUpRepGroupRecords(ctx, repGroup)
	if err != nil {
		return nil, err
	}

	if !allTerminal || len(records) == 0 {
		return nil, nil
	}

	return []*JobUpdate{subscriptionCatchUpRepGroupUpdate(repGroup, records)}, nil
}

// handleRequest parses the bytes received from a connected client in to a
// clientRequest, does the requested work, then responds back to the client with
// a serverResponse
func (s *Server) handleRequest(ctx context.Context, m *mangos.Message) error {
	dec := codec.NewDecoderBytes(m.Body, s.ch)
	cr := &clientRequest{}
	errd := dec.Decode(cr)
	if errd != nil {
		m.Free()

		return errd
	}

	var sr *serverResponse
	var srerr string
	var qerr string

	s.ssmutex.RLock()
	up := s.up
	drain := s.drain
	s.ssmutex.RUnlock()

	switch {
	// check that the client making the request has the expected token
	case (len(cr.Token) != tokenLength || !tokenMatches(cr.Token, s.token)) && cr.Method != "ping":
		srerr = ErrPermissionDenied
		qerr = "Client presented the wrong token"
	case s.q == nil || (!up && !drain):
		// the server just got shutdown
		srerr = ErrClosedStop
		qerr = "The server has been stopped"
	default:
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
			clog.Debug(ctx, "backup requested")
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
		case "pause":
			clog.Debug(ctx, "pause requested")
			paused, err := s.Pause()
			if err != nil {
				var jqerr Error
				if errors.As(err, &jqerr) {
					srerr = jqerr.Err
				} else {
					srerr = ErrInternalError
				}
				qerr = err.Error()
			} else {
				if paused {
					clog.Info(ctx, "paused by request")
				} else {
					// clients are allowed to call pause as many times as they
					// like, but a single resume call later should work, so we
					// resume now to keep the internal pause counter at 1
					resumed, err := s.Resume(ctx)
					if err != nil {
						clog.Error(ctx, "resume following an extraneous pause failed", "error", err)
					} else if resumed {
						clog.Error(ctx, "resumed incorrectly succeeded following a pause that did not")
					}
				}
				sr = &serverResponse{SStats: s.GetServerStats()}
			}
		case "resume":
			clog.Debug(ctx, "resume requested")
			resumed, err := s.Resume(ctx)
			if err != nil {
				var jqerr Error
				if errors.As(err, &jqerr) {
					srerr = jqerr.Err
				} else {
					srerr = ErrInternalError
				}
				qerr = err.Error()
			} else if resumed {
				clog.Info(ctx, "resumed on request")
			}
		case "drain":
			clog.Info(ctx, "drain requested")
			err := s.Drain(ctx)
			if err != nil {
				srerr = ErrInternalError
				qerr = err.Error()
			} else {
				sr = &serverResponse{SStats: s.GetServerStats()}
			}
		case "shutdown":
			clog.Debug(ctx, "shutdown requested")
			go s.Stop(ctx, true) // server stop can't complete while this client request is pending
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
					path, err := s.uploadFile(ctx, r, cr.Path)
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
				} else if srerr == "" {
					// create the jobs server-side
					added, dups, alreadyComplete, warnings, thisSrerr, err := s.createJobs(
						ctx,
						cr.Jobs,
						envkey,
						cr.IgnoreComplete,
					)
					if err != nil {
						srerr = thisSrerr
						qerr = err.Error()
					} else {
						clog.Debug(ctx, "added jobs", "new", added, "dups", dups, "complete", alreadyComplete)
						if cr.ReturnIDs {
							jobs := s.inputToQueuedJobs(ctx, cr.Jobs)
							var ids []string
							for _, job := range jobs {
								ids = append(ids, job.Key())
							}

							sr = &serverResponse{
								Added:       added,
								Existed:     dups + alreadyComplete,
								AddedIDs:    ids,
								AddWarnings: warnings,
							}
						} else {
							sr = &serverResponse{Added: added, Existed: dups + alreadyComplete, AddWarnings: warnings}
						}
					}
				}
			}
		case requestMethodSubscribe:
			repGroup := ""
			if cr.Job != nil {
				repGroup = cr.Job.RepGroup
			}

			id, err := s.registerClientSubscription(cr.Keys, repGroup)
			if err != nil {
				srerr = ErrBadRequest
				qerr = err.Error()

				break
			}

			catchUp, catchUpErr := s.subscriptionCatchUpForRegistered(ctx, id, cr.Keys, repGroup)
			if catchUpErr != nil {
				s.unregisterClientSubscription(id)

				srerr = ErrDBError
				qerr = catchUpErr.Error()

				break
			}

			sr = &serverResponse{SubscriptionID: id, JobUpdates: catchUp}
		case requestMethodUnsubscribe:
			if cr.SubscriptionID == "" {
				srerr = ErrBadRequest

				break
			}

			s.unregisterClientSubscription(cr.SubscriptionID)

			sr = &serverResponse{}
		case requestMethodWaitForUpdates:
			if cr.SubscriptionID == "" {
				srerr = ErrBadRequest

				break
			}

			updates, err := s.waitForSubscriptionUpdates(cr.SubscriptionID, cr.Timeout)
			if err != nil {
				srerr = ErrBadRequest
				qerr = err.Error()

				break
			}

			sr = &serverResponse{JobUpdates: updates}
		case "reserve":
			// return the next ready job
			if cr.ClientID.String() == "00000000-0000-0000-0000-000000000000" {
				srerr = ErrBadRequest
			} else if !drain {
				// first just try to Reserve normally
				var item *queue.Item
				var err error

				// don't proceed when we're expecting new/changed items
				s.rpmutex.Lock()
				var wch chan struct{}
				if s.racPending || s.racRunning {
					wch = make(chan struct{})
					s.waitingReserves = append(s.waitingReserves, wch)
				}
				s.rpmutex.Unlock()
				if wch != nil {
					<-wch
				}

				skip := false
				if cr.SchedulerGroup != "" {
					// if this is the first job that the client is trying to
					// reserve, and if we don't actually want any more clients
					// working on this schedulerGroup, we'll just act as if
					// nothing was ready. Likewise if in drain mode.
					if cr.FirstReserve && s.rc != "" {
						s.psgmutex.RLock()
						if group, existed := s.previouslyScheduledGroups[cr.SchedulerGroup]; !existed || group.getCount() == 0 {
							skip = true
						}
						s.psgmutex.RUnlock()
					}
				}

				if !skip {
					item, err = s.reserveWithLimits(ctx, cr.SchedulerGroup, cr.Timeout)
					if err != nil {
						var qerr queue.Error
						if errors.As(err, &qerr) {
							switch {
							case errors.Is(qerr.Err, queue.ErrNothingReady):
								srerr = ""
							case errors.Is(qerr.Err, queue.ErrQueueClosed):
								srerr = ErrQueueClosed
							default:
								srerr = ErrInternalError
							}
						}
					}
				}

				if srerr == "" && item != nil {
					// clean up any past state to have a fresh job ready to run
					sjob := item.Data().(*Job)
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
					sjob.killCalled = false
					sgroup := sjob.schedulerGroup
					retries := sjob.Retries
					ub := sjob.UntilBuried
					sjob.Unlock()

					delay := s.setItemDelay(ctx, item.Key, retries, ub)
					sjob.Lock()
					sjob.DelayTime = delay
					sjob.Unlock()

					// make a copy of the job with some extra stuff filled in (that
					// we don't want taking up memory here) for the client
					job := s.itemToJob(ctx, item, false, true)
					sr = &serverResponse{Job: job}
					clog.Debug(ctx, "reserved job", "cmd", job.Cmd, "schedGrp", sgroup)
				}
			} // else we'll return nothing, as if there were no jobs in the queue
		case requestMethodStart:
			// update the job's cmd-started-related properties
			if cr.Job == nil {
				srerr = ErrBadRequest

				break
			}

			var job *Job
			_, job, srerr = s.getij(cr, true)
			if srerr == "" {
				job.Lock()
				if cr.Job.Pid <= 0 || cr.Job.Host == "" {
					srerr = ErrBadRequest
					job.Unlock()
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
					job.Lost = false
					job.State = JobStateRunning

					job.Unlock()

					// we'll save-to-disk that we started running this job, so
					// recovery is possible after a crash
					s.db.updateJobAfterChange(ctx, job)
				}
			}
		case requestMethodTouch:
			var job *Job
			var item *queue.Item
			item, job, srerr = s.getij(cr, true)
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

					if srerr == "" && s.liveJTouchEnabled() && liveSnapshotPresent(cr.JobEndState) {
						applyLiveSnapshot(job, cr.JobEndState)

						update, err := jobUpdateFromLiveJob(job)
						if err != nil {
							clog.Warn(ctx, "failed to build live subscription update", "err", err)
						} else {
							s.enqueueSubscriptionUpdate(update)
						}
					}
				}
				sr = &serverResponse{KillCalled: killCalled}
			}
		case "jarchive":
			// remove the job from the queue, rpl and live bucket and add to
			// complete bucket
			var item *queue.Item
			var job *Job
			item, job, srerr = s.getij(cr, true)
			if srerr == "" {
				// first check the item is still in the run queue (eg. the job
				// wasn't released by another process; unlike the other methods,
				// queue package does not check we're in the run queue when
				// Remove()ing, since you can remove from any queue)
				job.updateAfterExit(cr.JobEndState, s.limiter)
				job.Lock()
				running := item.Stats().State == queue.ItemStateRun
				switch {
				case !running:
					srerr = ErrBadJob
					job.Unlock()
				case !job.Exited || job.Exitcode != 0 || job.StartTime.IsZero() || job.EndTime.IsZero():
					srerr = ErrBadRequest
					job.Unlock()
				default:
					key := job.Key()
					job.State = JobStateComplete
					job.FailReason = ""

					if cr.JobEndState != nil {
						job.StdOutC = cr.JobEndState.Stdout
						job.StdErrC = cr.JobEndState.Stderr
					}

					sgroup := job.schedulerGroup
					rgroup := job.RepGroup
					job.Unlock()
					err := s.db.archiveJob(ctx, key, job)
					if err != nil {
						srerr = ErrDBError
						qerr = err.Error()
					} else {
						err = s.q.Remove(ctx, key)
						if err != nil {
							srerr = ErrInternalError
							qerr = err.Error()
						} else {
							s.rpl.Lock()
							s.rpl.Delete(rgroup, key)
							s.rpl.Unlock()
							clog.Debug(ctx, "completed job", "cmd", job.Cmd, "schedGrp", sgroup)
							s.decrementGroupCount(ctx, sgroup, 1)
						}
					}
				}
			}
		case "jrelease":
			// move the job from the run queue to the delay queue, unless it has
			// failed too many times, in which case bury
			var job *Job
			_, job, srerr = s.getij(cr, false)
			if srerr == "" {
				if cr.JobEndState == nil {
					cr.JobEndState = &JobEndState{}
				}

				errq := s.releaseJob(ctx, job, cr.JobEndState, cr.failReason(), true, false)
				if errq != nil {
					srerr = ErrInternalError

					clog.Warn(ctx, "releaseJob failed", "err", errq)
					qerr = errq.Error()
				}
			}
		case "jbury":
			// move the job from the run queue to the bury queue
			var job *Job
			_, job, srerr = s.getij(cr, false)
			if srerr == "" {
				if cr.JobEndState == nil {
					cr.JobEndState = &JobEndState{}
				}

				errq := s.releaseJob(ctx, job, cr.JobEndState, cr.failReason(), true, true)
				if errq != nil {
					srerr = ErrInternalError

					clog.Warn(ctx, "releaseJob to bury failed", "err", errq)
					qerr = errq.Error()
				}
			}
		case "jkick":
			// move the jobs from the bury queue to the ready queue; unlike the
			// other j* methods, client doesn't have to be the Reserve() owner
			// of these jobs, and we don't want the "in run queue" test
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				var jobs []*Job
				for _, key := range cr.Keys {
					item, err := s.q.Get(key)
					if err != nil || item.Stats().State != queue.ItemStateBury {
						continue
					}

					jobs = append(jobs, item.Data().(*Job))
				}

				kicked := s.kickJobs(ctx, jobs)
				sr = &serverResponse{Existed: kicked}
			}
		case "jdel":
			// remove the jobs from the bury/delay/dependent/ready queue and the
			// live bucket
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				var jobs []*Job
				for _, key := range cr.Keys {
					item, err := s.q.Get(key)
					if err != nil || item == nil {
						continue
					}

					iState := item.Stats().State
					if iState == queue.ItemStateRun {
						continue
					}

					jobs = append(jobs, item.Data().(*Job))
				}

				deleted := s.deleteJobs(ctx, jobs)
				clog.Debug(ctx, "deleted jobs", "count", len(deleted))
				sr = &serverResponse{Existed: len(deleted)}
			}
		case "jmod":
			// modify jobs in the bury/delay/dependent/ready queue and the
			// live bucket
			if cr.Keys == nil || cr.Modifier == nil {
				srerr = ErrBadRequest
			} else {
				// to avoid race conditions with jobs that are currently
				// pending, but become running in the middle of us trying to
				// modify them, we first pause the server, and resume it
				// afterwards
				paused, err := s.Pause()
				if err != nil {
					var jqerr Error
					if errors.As(err, &jqerr) {
						srerr = jqerr.Err
					} else {
						srerr = ErrInternalError
					}
					qerr = err.Error()
				} else if paused {
					clog.Debug(ctx, "modify requested, paused server")
				} else {
					clog.Debug(ctx, "modify requested")
				}

				if err == nil {
					var toModifyJobs []*Job
					toModifyKeys := make(map[string]*Job)
					for _, jobkey := range cr.Keys {
						item, err := s.q.Get(jobkey)
						if err != nil || item == nil {
							continue
						}
						iState := item.Stats().State
						if iState == queue.ItemStateRun {
							continue
						}
						toModifyJobs = append(toModifyJobs, item.Data().(*Job))
						toModifyKeys[jobkey] = item.Data().(*Job)
					}

					modified, err := cr.Modifier.Modify(toModifyJobs, s)
					if err != nil {
						var jqerr Error
						if errors.As(err, &jqerr) {
							srerr = jqerr.Err
						} else {
							srerr = ErrInternalError
						}
						qerr = err.Error()
					}

					if err == nil && len(modified) > 0 {
						var toModify []*Job
						for _, old := range modified {
							job := toModifyKeys[old]
							if job != nil {
								toModify = append(toModify, job)
							}
						}

						// additional handling of changed limit groups
						if cr.Modifier.LimitGroupsSet {
							limitGroups := make(map[string]*limiter.GroupData)
							for _, job := range toModify {
								s.handleUserSpecifiedJobLimitGroups(job, limitGroups)
							}
							err := s.storeLimitGroups(limitGroups)
							if err != nil {
								clog.Error(ctx, "failed to store limit groups", "err", err)
							}
						}

						// update changed keys in the queue and in our rpl lookup
						keyToRP := make(map[string]string)
						for _, job := range toModify {
							keyToRP[job.Key()] = job.RepGroup
						}
						s.rpl.Lock()
						for new, old := range modified {
							if old == new {
								continue
							}
							errc := s.q.ChangeKey(old, new)
							if errc != nil {
								clog.Error(ctx, "failed to change a job key in the queue", "err", errc)
							}

							rp := keyToRP[new]
							s.rpl.Delete(rp, old)
							s.rpl.Add(rp, new)
						}
						s.rpl.Unlock()

						// update db live bucket and dep lookups
						if len(toModify) > 0 {
							oldKeys := make([]string, len(toModify))
							for i, job := range toModify {
								oldKeys[i] = modified[job.Key()]
							}
							errm := s.db.modifyLiveJobs(ctx, oldKeys, toModify)
							if errm != nil {
								clog.Error(ctx, "job modification in database failed", "err", errm)
							} else if cr.Modifier.DependenciesSet || cr.Modifier.PrioritySet {
								// if we're changing the jobs these jobs are
								// dependant upon or their priority, that must be
								// reflected in the queue as well
								for _, job := range toModify {
									deps, waitingForDepGroups, depErr := job.Dependencies.incompleteJobKeys(s.db)
									if depErr != nil {
										clog.Error(ctx, "failed to get job dependencies", "err", depErr)

										continue
									}

									job.setWaitingForDepGroups(waitingForDepGroups)

									err = s.q.Update(
										ctx, job.Key(), job.getSchedulerGroup(), job, job.Priority,
										0*time.Second, s.itemTTRDuration(), deps,
									)
									if err != nil {
										clog.Error(ctx, "failed to modify a job in the queue", "err", err)
									}
								}
							}
						}
					}

					sr = &serverResponse{Modified: modified}

					// now resume the server again
					resumed, err := s.Resume(ctx)
					if err != nil {
						clog.Error(ctx, err.Error())
					} else if resumed {
						clog.Debug(ctx, "modify completed, resumed server", "count", len(modified))
					} else {
						clog.Debug(ctx, "modify completed", "count", len(modified))
					}
				}
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
					k, err := s.killJob(ctx, jobkey)
					if err != nil {
						continue
					}

					if k {
						killable++
					}
				}
				clog.Debug(ctx, "killed jobs", "count", killable)
				sr = &serverResponse{Existed: killable}
			}
		case "getbc":
			// get jobs by their keys (which come from their Cmds & Cwds)
			if cr.Keys == nil {
				srerr = ErrBadRequest
			} else {
				var jobs []*Job
				jobs, srerr, qerr = s.getJobsByKeys(ctx, cr.Keys, cr.GetStd, cr.GetEnv)
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

				opts := repGroupOptions{
					RepGroup: cr.Job.RepGroup,
					Match:    normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search),
					limitJobsOptions: limitJobsOptions{
						Limit:               cr.Limit,
						State:               cr.State,
						GetStd:              cr.GetStd,
						GetEnv:              cr.GetEnv,
						WaitingForDepGroups: cr.WaitingForDepGroups,
					},
				}

				jobs, srerr, qerr = s.getJobsByRepGroup(ctx, opts)
				if len(jobs) > 0 {
					sr = &serverResponse{Jobs: jobs}
				}
			}
		case "getrs":
			var repGroup string
			if cr.Job != nil {
				repGroup = cr.Job.RepGroup
			}

			match := normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search)
			summaries, serr, qe := s.getStatusByRepGroup(repGroupStatusOptions{
				RepGroup:             repGroup,
				Match:                match,
				States:               cr.States,
				IncludeComplete:      cr.IncludeComplete,
				IncludeStatusDetails: cr.IncludeStatusDetails,
			})
			srerr = serr
			qerr = qe

			if srerr == "" {
				sr = &serverResponse{StatusSummaries: summaries}
			}
		case "getin":
			// get incomplete jobs in the jobqueue, optionally filtered by rep group
			var repGroup string
			if cr.Job != nil {
				repGroup = cr.Job.RepGroup
			}

			match := normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search)

			jobs := s.getJobsCurrent(ctx, repGroup, match, cr.Limit, cr.State,
				cr.GetStd, cr.GetEnv, cr.WaitingForDepGroups)
			if len(jobs) > 0 {
				sr = &serverResponse{Jobs: jobs}
			}
		case "getlct":
			if cr.Job == nil || cr.Job.RepGroup == "" {
				srerr = ErrBadRequest

				break
			}

			match := normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search)
			m, serr, qe := s.getLastCompletionTimeByRepGroup(cr.Job.RepGroup,
				match)
			srerr = serr
			qerr = qe

			if srerr == "" {
				sr = &serverResponse{CompletionTimes: m}
			}
		case "getbcs":
			servers := s.getBadServers()

			if cr.ConfirmDeadCloudServers {
				confirmed, jobs := s.killBadCloudServers(ctx, servers, cr.CloudServerID)
				sr = &serverResponse{BadServers: confirmed, Jobs: jobs}
			} else {
				sr = &serverResponse{BadServers: servers}
			}
		case "dch":
			if cr.DestroyCloudHost != "" {
				server, jobs := s.killCloudServer(ctx, cr.DestroyCloudHost)
				if server != nil {
					sr = &serverResponse{BadServers: []*BadServer{server}, Jobs: jobs}
				}
			} else {
				srerr = ErrBadRequest
			}
		case "getsetlg":
			if cr.LimitGroup == "" {
				srerr = ErrBadRequest
			} else {
				limit, serr, err := s.getSetLimitGroup(ctx, cr.LimitGroup)
				if err != nil {
					srerr = serr
					qerr = err.Error()
				} else {
					sr = &serverResponse{Limit: int(limit.Limit())}
				}
			}
		case "getlgs":
			sr = &serverResponse{LimitGroups: s.limiter.GetLimits()}
		default:
			srerr = ErrUnknownCommand
		}
	}

	// on error, just send the error back to client and return a more detailed
	// error for logging
	if srerr != "" {
		errr := s.reply(m, &serverResponse{Err: srerr})
		if errr != nil {
			clog.Warn(ctx, "reply to client failed", "err", errr)
		}
		if qerr == "" {
			qerr = srerr
		}

		return Error{cr.Method, cr.key(), qerr}
	}

	// some commands don't return anything to the client
	if sr == nil {
		sr = &serverResponse{}
	}

	// send reply to client
	return s.reply(m, sr) // *** log failure to reply?
}

// for the many j* methods in handleRequest, we do this common stuff to get
// the desired item and job. The returned string is one of our Err* constants.
func (s *Server) getij(cr *clientRequest, checkRunning bool) (*queue.Item, *Job, string) {
	key := cr.key()
	if key == "" {
		return nil, nil, ErrBadRequest
	}

	item, err := s.q.Get(key)
	if err != nil || (checkRunning && item.Stats().State != queue.ItemStateRun) {
		return item, nil, ErrBadJob
	}
	job := item.Data().(*Job)

	if cr.ClientID != job.ReservedBy {
		return item, job, ErrMustReserve
	}

	return item, job, ""
}

func (s *Server) liveJTouchEnabled() bool {
	s.ssmutex.RLock()
	defer s.ssmutex.RUnlock()

	return s.ServerInfo != nil && s.ServerInfo.WebPort != ""
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

// setItemDelay is called when a job is reserved, and sets the item's delay to
// a value based on a backoff. Returns the delay that was set.
func (s *Server) setItemDelay(ctx context.Context, key string, maxRetries, untilBuried uint8) time.Duration {
	delay := calculateItemDelay(int(maxRetries)-int(untilBuried)+1, s.timings.ReleaseDelayMin)

	errd := s.q.SetDelay(key, delay)
	if errd != nil {
		clog.Warn(ctx, "reserve queue SetDelay failed", "err", errd)
	}

	return delay
}

// for the many get* methods in handleRequest, we do this common stuff to get
// an item's job from the in-memory queue formulated for the client.
func (s *Server) itemToJob(ctx context.Context, item *queue.Item, getStd bool, getEnv bool) *Job {
	sjob := item.Data().(*Job)
	sjob.RLock()

	stats := item.Stats()

	state := s.itemStateToJobState(stats.State, sjob.Lost)

	// we're going to fill in some properties of the Job and return
	// it to client, but don't want those properties set here for
	// us, so we make a new Job and fill stuff in that
	req := &scheduler.Requirements{}
	*req = *sjob.Requirements // copy reqs since server changes these, avoiding a race condition
	job := &Job{
		RepGroup:              sjob.RepGroup,
		ReqGroup:              sjob.ReqGroup,
		Group:                 sjob.Group,
		LimitGroups:           sjob.LimitGroups,
		LimitGroupsForDisplay: sjob.LimitGroupsForDisplay,
		Modules:               sjob.Modules,
		DepGroups:             sjob.DepGroups,
		Cmd:                   sjob.Cmd,
		Cwd:                   sjob.Cwd,
		CwdMatters:            sjob.CwdMatters,
		ChangeHome:            sjob.ChangeHome,
		ActualCwd:             sjob.ActualCwd,
		Requirements:          req,
		Override:              sjob.Override,
		Priority:              sjob.Priority,
		Retries:               sjob.Retries,
		DelayTime:             sjob.DelayTime,
		NoRetriesOverWalltime: sjob.NoRetriesOverWalltime,
		PeakRAM:               sjob.PeakRAM,
		PeakDisk:              sjob.PeakDisk,
		Exited:                sjob.Exited,
		Exitcode:              sjob.Exitcode,
		FailReason:            sjob.FailReason,
		StartTime:             sjob.StartTime,
		EndTime:               sjob.EndTime,
		Pid:                   sjob.Pid,
		Host:                  sjob.Host,
		HostID:                sjob.HostID,
		HostIP:                sjob.HostIP,
		CPUtime:               sjob.CPUtime,
		State:                 state,
		Attempts:              sjob.Attempts,
		UntilBuried:           sjob.UntilBuried,
		ReservedBy:            sjob.ReservedBy,
		EnvKey:                sjob.EnvKey,
		EnvOverride:           sjob.EnvOverride,
		Dependencies:          sjob.Dependencies,
		WaitingForDepGroups:   sjob.WaitingForDepGroups,
		Behaviours:            sjob.Behaviours,
		MountConfigs:          sjob.MountConfigs,
		MonitorDocker:         sjob.MonitorDocker,
		WithDocker:            sjob.WithDocker,
		WithSingularity:       sjob.WithSingularity,
		ContainerMounts:       sjob.ContainerMounts,
		BsubMode:              sjob.BsubMode,
		BsubID:                sjob.BsubID,
	}

	if state == JobStateReserved && !sjob.StartTime.IsZero() {
		job.State = JobStateRunning
	}
	sjob.RUnlock()
	s.jobPopulateStdEnv(ctx, job, getStd, getEnv)
	return job
}

// jobPopulateStdEnv fills in the StdOutC, StdErrC and EnvC values for a Job,
// extracting them from the database.
func (s *Server) jobPopulateStdEnv(ctx context.Context, job *Job, getStd bool, getEnv bool) {
	if !getStd && !getEnv {
		return
	}

	job.Lock()
	defer job.Unlock()

	if getStd && jobCouldHaveStd(job) {
		job.StdOutC, job.StdErrC = s.db.retrieveJobStd(ctx, job.Key())
	}

	if getEnv {
		job.EnvC = s.db.retrieveEnv(ctx, job.EnvKey)
		job.EnvCRetrieved = true
	}
}

func jobCouldHaveStd(job *Job) bool {
	return (job.Exited && job.Exitcode != 0) || job.State == JobStateBuried
}

func (s *Server) subscriptionCatchUpForRegistered(ctx context.Context, id string,
	keys []string, repGroup string,
) ([]*JobUpdate, error) {
	if repGroup == "" {
		return s.subscriptionCatchUp(ctx, keys, repGroup)
	}

	records, _, err := s.subscriptionCatchUpRepGroupRecords(ctx, repGroup)
	if err != nil {
		return nil, err
	}

	update := s.seedRepGroupSubscription(id, records)
	if update == nil {
		return nil, nil
	}

	return []*JobUpdate{update}, nil
}

func (s *Server) subscriptionCatchUp(ctx context.Context, keys []string, repGroup string) ([]*JobUpdate, error) {
	if len(keys) > 0 {
		return s.subscriptionCatchUpByKeys(ctx, keys)
	}

	if repGroup != "" {
		return s.subscriptionCatchUpByRepGroup(ctx, repGroup)
	}

	return nil, nil
}

// reserveWithLimits reserves the next item in the queue (optionally limited to
// the given scheduler group). If (and only if!) a scheduler group was supplied,
// and it is suffixed with limit groups, those limit groups will be incremented.
// On success we reserve and return as normal. On failure, we act as if the
// queue was empty.
func (s *Server) reserveWithLimits(ctx context.Context, group string, wait time.Duration) (*queue.Item, error) {
	var item *queue.Item
	var err error
	var limitGroups []string
	if group != "" {
		limitGroups = s.schedGroupToLimitGroups(group)
		if len(limitGroups) > 0 {
			// it is better to call Increment before Reserve and possibly use up
			// the limit for up to wait period if there's no item in the queue,
			// than it is to Reserve first and then Release if at the limit,
			// because Releasing causes scheduler churn
			t := time.Now()

			if !s.limiter.Increment(ctx, limitGroups, wait) {
				return nil, queue.Error{Queue: s.q.Name, Op: "Reserve", Item: "", Err: queue.ErrNothingReady}
			}
			wait -= time.Since(t)
		}
	}

	item, err = s.q.Reserve(group, wait)

	if len(limitGroups) > 0 {
		if item == nil {
			s.limiter.Decrement(limitGroups)
		} else {
			item.Data().(*Job).noteIncrementedLimitGroups(limitGroups)
		}
	}

	return item, err
}

// schedGroupToLimitGroups takes a scheduler group that may be suffixed with
// limit groups (by Job.generateSchedulerGroup()), and returns the extracted
// limit groups
func (s *Server) schedGroupToLimitGroups(group string) []string {
	parts := strings.Split(group, jobSchedLimitGroupSeparator)
	if len(parts) == 2 {
		return strings.Split(parts[1], jobLimitGroupSeparator)
	}
	return nil
}

// reply to a client
func (s *Server) reply(m *mangos.Message, sr *serverResponse) error {
	var encoded []byte
	enc := codec.NewEncoderBytes(&encoded, s.ch)
	err := enc.Encode(sr)
	if err != nil {
		m.Free()

		return err
	}
	m.Body = encoded
	err = s.sock.SendMsg(m)
	if err != nil {
		m.Free()
	}

	return err
}
