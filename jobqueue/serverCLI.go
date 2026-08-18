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
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

// request method names handled by the server's dispatchMethod. The companion
// constants for the j* and subscription methods live alongside the client in
// client.go (requestMethodStart etc.).
const (
	requestMethodPing          = "ping"
	requestMethodAdd           = "add"
	requestMethodReserve       = "reserve"
	requestMethodGetByCmd      = "getbc"
	requestMethodGetIncomplete = "getin"
	requestMethodGetRecent     = "getrec"
	requestMethodGetBadServers = "getbcs"

	// schedGroupWithLimitParts is the number of parts a scheduler group splits
	// into when it carries a limit-groups suffix (the group name and the limit
	// groups).
	schedGroupWithLimitParts = 2
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
	queueTerminal := addSubscriptionCatchUpRepGroupRecords(records, s.getQueueJobsByRepGroup(ctx, repGroup, false))

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

	leaf, err := cwdLeaf(job.Cwd, job.createdCwd())
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
			Cwd:        leaf,
			SSHCommand: sshCommandForRunningJob(job.State, job.Requirements, job.Host, job.HostIP, job.workingDir()),
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

	job.setActualCwd(jes.Cwd)
	job.PeakRAM = jes.PeakRAM
	job.PeakDisk = jes.PeakDisk
	job.CPUtime = jes.CPUtime

	if len(jes.Stdout) != 0 {
		job.StdOutC = jes.Stdout
	}

	if len(jes.Stderr) != 0 {
		job.StdErrC = jes.Stderr
	}
}

func malformedAddJobMessage(jobs []*Job) string {
	for jobIndex, job := range jobs {
		if job == nil {
			return fmt.Sprintf("job at index %d is nil", jobIndex)
		}

		if dependencyIndex := slices.Index(job.Dependencies, nil); dependencyIndex >= 0 {
			return fmt.Sprintf("jobs[%d].Dependencies[%d] is nil", jobIndex, dependencyIndex)
		}

		if behaviourIndex := slices.Index(job.Behaviours, nil); behaviourIndex >= 0 {
			return fmt.Sprintf("jobs[%d].Behaviours[%d] is nil", jobIndex, behaviourIndex)
		}
	}

	return ""
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
// a serverResponse.
func (s *Server) handleRequest(ctx context.Context, m *mangos.Message) error {
	dec := codec.NewDecoderBytes(m.Body, s.ch)
	cr := &clientRequest{}

	errd := dec.Decode(cr)
	if errd != nil {
		m.Free()

		return errd
	}

	s.ssmutex.RLock()
	up := s.up
	drain := s.drain
	s.ssmutex.RUnlock()

	var (
		sr          *serverResponse
		srerr, qerr string
	)

	if srerr, qerr = s.validateRequest(cr, up, drain); srerr == "" {
		sr, srerr, qerr = s.dispatchMethod(ctx, cr, drain)
	}

	return s.replyToClient(ctx, m, cr, sr, srerr, qerr)
}

// validateRequest checks a request's token and that the server can serve it,
// returning non-empty error strings if it should be rejected.
func (s *Server) validateRequest(cr *clientRequest, up, drain bool) (string, string) {
	// check that the client making the request has the expected token
	if (len(cr.Token) != tokenLength || !tokenMatches(cr.Token, s.token)) && cr.Method != requestMethodPing {
		return ErrPermissionDenied, "Client presented the wrong token"
	}

	if s.q == nil || (!up && !drain) {
		// the server just got shutdown
		return ErrClosedStop, "The server has been stopped"
	}

	// once shutdown has begun (up=false), refuse to register a new client
	// subscription: any subscription created now is torn down moments later when
	// closeClientSubscriptions runs and the command socket closes, so a
	// reconnecting subscriber that bound to this dying server would be forced to
	// reconnect a second time (to the replacement server) and emit a second,
	// spurious JobUpdateResync. Rejecting here makes the subscriber's reconnect
	// retry until the replacement server is up, so it resubscribes exactly once.
	// This matters because concurrent RPC readers (spec B1) let a subscribe be
	// admitted and served during the brief shutdown window that a single reader
	// almost never hit. Pause/drain keep up=true, so graceful draining is
	// unaffected.
	if !up && cr.Method == requestMethodSubscribe {
		return ErrClosedStop, "The server is shutting down"
	}

	return "", ""
}

// replyToClient sends sr (or an error response) back to the client, returning a
// detailed error for logging when srerr is set.
func (s *Server) replyToClient(ctx context.Context, m *mangos.Message, cr *clientRequest,
	sr *serverResponse, srerr, qerr string) error {
	// on error, just send the error back to client and return a more detailed
	// error for logging
	if srerr != "" {
		return s.replyError(ctx, m, cr, srerr, qerr)
	}

	// some commands don't return anything to the client
	if sr == nil {
		sr = &serverResponse{}
	}

	// send reply to client
	return s.reply(m, sr) // *** log failure to reply?
}

// replyError sends an error response to the client and returns a detailed
// jobqueue Error for logging (defaulting qerr to srerr).
func (s *Server) replyError(ctx context.Context, m *mangos.Message, cr *clientRequest, srerr, qerr string) error {
	if errr := s.reply(m, &serverResponse{Err: srerr}); errr != nil {
		clog.Warn(ctx, "reply to client failed", "err", errr)
	}

	if qerr == "" {
		qerr = srerr
	}

	return Error{cr.Method, cr.key(), qerr}
}

// handlePing returns server info for a ping request.
func (s *Server) handlePing() *serverResponse {
	// avoid a later race condition when we try to encode ServerInfo by doing
	// the read here, copying it under read lock
	s.ssmutex.RLock()
	defer s.ssmutex.RUnlock()

	si := &ServerInfo{}
	*si = *s.ServerInfo

	return &serverResponse{SInfo: si}
}

// handleBackup backs the database up into the response.
func (s *Server) handleBackup(ctx context.Context) (*serverResponse, string, string) {
	clog.Debug(ctx, "backup requested")
	// make an io.Writer that writes to a byte slice, so we can return the db as
	// that
	var b bytes.Buffer

	if err := s.BackupDB(&b); err != nil {
		return nil, ErrInternalError, err.Error()
	}

	return &serverResponse{DB: b.Bytes()}, "", ""
}

// handlePause pauses the server, resuming immediately if it was already paused
// so a later single resume works.
func (s *Server) handlePause(ctx context.Context) (*serverResponse, string, string) {
	clog.Debug(ctx, "pause requested")

	paused, err := s.Pause()
	if err != nil {
		return nil, serverErrString(err), err.Error()
	}

	if paused {
		clog.Info(ctx, "paused by request")
	} else {
		s.resumeAfterExtraneousPause(ctx)
	}

	return &serverResponse{SStats: s.GetServerStats()}, "", ""
}

// resumeAfterExtraneousPause resumes immediately after a pause that found the
// server already paused, keeping the internal pause counter at 1 so a later
// single resume works.
func (s *Server) resumeAfterExtraneousPause(ctx context.Context) {
	// clients are allowed to call pause as many times as they like, but a single
	// resume call later should work, so we resume now to keep the internal pause
	// counter at 1
	resumed, err := s.Resume(ctx)
	if err != nil {
		clog.Error(ctx, "resume following an extraneous pause failed", "error", err)
	} else if resumed {
		clog.Error(ctx, "resumed incorrectly succeeded following a pause that did not")
	}
}

// handleResume resumes the server.
func (s *Server) handleResume(ctx context.Context) (*serverResponse, string, string) {
	clog.Debug(ctx, "resume requested")

	resumed, err := s.Resume(ctx)
	if err != nil {
		return nil, serverErrString(err), err.Error()
	}

	if resumed {
		clog.Info(ctx, "resumed on request")
	}

	return nil, "", ""
}

// handleDrain puts the server into drain mode.
func (s *Server) handleDrain(ctx context.Context) (*serverResponse, string, string) {
	clog.Info(ctx, "drain requested")

	if err := s.Drain(ctx); err != nil {
		return nil, ErrInternalError, err.Error()
	}

	return &serverResponse{SStats: s.GetServerStats()}, "", ""
}

// handleUpload stores an uploaded (compressed) file and returns its path.
func (s *Server) handleUpload(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	// upload file to us
	if cr.File == nil {
		return nil, ErrBadRequest, ""
	}

	data, err := decompress(cr.File)
	if err != nil {
		return nil, ErrInternalError, err.Error()
	}

	path, err := s.uploadFile(ctx, bytes.NewReader(data), cr.Path)
	if err != nil {
		return nil, ErrInternalError, err.Error()
	}

	return &serverResponse{Path: path}, "", ""
}

// serverErrString maps an error to its jobqueue Err* string: the Err field of a
// jobqueue Error, otherwise ErrInternalError.
func serverErrString(err error) string {
	var jqerr Error
	if errors.As(err, &jqerr) {
		return jqerr.Err
	}

	return ErrInternalError
}

// handleAdd stores the request's env and creates its jobs in the queue.
func (s *Server) handleAdd(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	// add jobs to the queue, and along side keep the environment variables
	// they're supposed to execute under.
	if malformed := malformedAddJobMessage(cr.Jobs); malformed != "" {
		return nil, ErrBadRequest, malformed
	}

	missingRequirements := slices.ContainsFunc(cr.Jobs, func(job *Job) bool {
		return job != nil && job.Requirements == nil
	})
	if cr.Env == nil || cr.Jobs == nil || missingRequirements {
		return nil, ErrBadRequest, ""
	}

	// Store Env
	envkey, err := s.db.storeEnv(cr.Env)
	if err != nil {
		return nil, ErrDBError, err.Error()
	}

	// create the jobs server-side
	added, dups, alreadyComplete, warnings, thisSrerr, err := s.createJobs(ctx, cr.Jobs, envkey, cr.IgnoreComplete)
	if err != nil {
		return nil, thisSrerr, err.Error()
	}

	clog.Debug(ctx, "added jobs", "new", added, "dups", dups, "complete", alreadyComplete)

	existed := dups + alreadyComplete

	if !cr.ReturnIDs {
		return &serverResponse{Added: added, Existed: existed, AddWarnings: warnings}, "", ""
	}

	jobs := s.inputToQueuedJobs(ctx, cr.Jobs)

	var ids []string
	for _, job := range jobs {
		ids = append(ids, job.Key())
	}

	return &serverResponse{Added: added, Existed: existed, AddedIDs: ids, AddWarnings: warnings}, "", ""
}

// handleSubscribe registers a client subscription and returns its id plus the
// catch-up updates for the already-known state of the subscribed jobs.
func (s *Server) handleSubscribe(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	repGroup := ""
	if cr.Job != nil {
		repGroup = cr.Job.RepGroup
	}

	id, err := s.registerClientSubscription(cr.Keys, repGroup)
	if err != nil {
		if errors.Is(err, errSubscriptionClosed) {
			return nil, ErrClosedStop, err.Error()
		}

		return nil, ErrBadRequest, err.Error()
	}

	catchUp, catchUpErr := s.subscriptionCatchUpForRegistered(ctx, id, cr.Keys, repGroup)
	if catchUpErr != nil {
		s.unregisterClientSubscription(id)

		return nil, ErrDBError, catchUpErr.Error()
	}

	return &serverResponse{SubscriptionID: id, JobUpdates: catchUp}, "", ""
}

// handleUnsubscribe unregisters the request's client subscription.
func (s *Server) handleUnsubscribe(cr *clientRequest) (*serverResponse, string, string) {
	if cr.SubscriptionID == "" {
		return nil, ErrBadRequest, ""
	}

	s.unregisterClientSubscription(cr.SubscriptionID)

	return &serverResponse{}, "", ""
}

// handleWaitForUpdates blocks until the subscription has updates (or times out)
// and returns them.
func (s *Server) handleWaitForUpdates(cr *clientRequest) (*serverResponse, string, string) {
	if cr.SubscriptionID == "" {
		return nil, ErrBadRequest, ""
	}

	updates, err := s.waitForSubscriptionUpdates(cr.SubscriptionID, cr.Timeout)
	if err != nil {
		return nil, ErrBadRequest, err.Error()
	}

	return &serverResponse{JobUpdates: updates}, "", ""
}

// handleReserve returns the next ready job for a client to run, or nothing (as
// if the queue were empty) when draining or no suitable job is available.
func (s *Server) handleReserve(ctx context.Context, cr *clientRequest, drain bool) (*serverResponse, string, string) {
	if cr.ClientID.String() == "00000000-0000-0000-0000-000000000000" {
		return nil, ErrBadRequest, ""
	}

	if drain {
		// return nothing, as if there were no jobs in the queue
		return nil, "", ""
	}

	s.waitForPendingReserves()

	if s.skipReserve(cr) {
		return nil, "", ""
	}

	item, srerr := s.reserveItem(ctx, cr)
	if srerr != "" || item == nil {
		return nil, srerr, ""
	}

	return s.respondWithReservedJob(ctx, cr, item), "", ""
}

// waitForPendingReserves blocks the caller until any in-progress ready-added
// callback has finished, so reserves don't race new/changed items.
func (s *Server) waitForPendingReserves() {
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
}

// skipReserve reports whether a reserve should be treated as if nothing were
// ready because the client's first reserve targets a scheduler group we no
// longer want more clients working on.
func (s *Server) skipReserve(cr *clientRequest) bool {
	if cr.SchedulerGroup == "" || !cr.FirstReserve || s.runnerCommand() == "" {
		return false
	}

	// if this is the first job that the client is trying to reserve, and if we
	// don't actually want any more clients working on this schedulerGroup, we'll
	// just act as if nothing was ready. Likewise if in drain mode.
	s.psgmutex.RLock()
	defer s.psgmutex.RUnlock()

	group, existed := s.previouslyScheduledGroups[cr.SchedulerGroup]

	return !existed || group.getCount() == 0
}

// reserveItem reserves the next item for the client's scheduler group, mapping
// queue errors to our Err* strings (an empty queue is not an error).
func (s *Server) reserveItem(ctx context.Context, cr *clientRequest) (*queue.Item, string) {
	item, err := s.reserveWithLimits(ctx, cr.SchedulerGroup, cr.Timeout)
	if err == nil {
		return item, ""
	}

	var qerr queue.Error
	if !errors.As(err, &qerr) {
		return nil, ""
	}

	switch {
	case errors.Is(qerr.Err, queue.ErrNothingReady):
		return nil, ""
	case errors.Is(qerr.Err, queue.ErrQueueClosed):
		return nil, ErrQueueClosed
	default:
		return nil, ErrInternalError
	}
}

// respondWithReservedJob resets the reserved item's job to a fresh run state and
// returns a client copy of it in a response.
func (s *Server) respondWithReservedJob(ctx context.Context, cr *clientRequest, item *queue.Item) *serverResponse {
	// clean up any past state to have a fresh job ready to run
	sjob := item.Data().(*Job) //nolint:errcheck,forcetypeassert // queue only ever stores *Job

	sgroup, retries, ub := s.resetJobForReservation(sjob, cr.ClientID)

	delay := s.setItemDelay(ctx, item.Key, retries, ub)

	sjob.Lock()
	sjob.DelayTime = delay
	// record which runner holds this reservation (its own host+pid) before the
	// command's own pid is reported at Started, so a reserved-not-started job's
	// liveness can be confirmed independently of the RPC stream. An old client
	// sends no host+pid, leaving Host "" and Pid 0.
	sjob.Host = cr.Host
	sjob.Pid = cr.Pid
	sjob.Unlock()

	// tell the scheduler which of its elements (e.g. an LSF "jobid[index]") holds
	// this reservation, so it is never killed as excess mid-job. An old/non-LSF
	// client sends no SchedulerID.
	if cr.SchedulerID != "" {
		s.scheduler.Reserved(cr.SchedulerID)
	}

	// make a copy of the job with some extra stuff filled in (that we don't want
	// taking up memory here) for the client
	job := s.itemToJob(ctx, item, false, true)
	clog.Debug(ctx, "reserved job", "cmd", job.Cmd, "schedGrp", sgroup)

	return &serverResponse{Job: job}
}

// resetJobForReservation clears a job's past run state ready for a fresh run by
// the reserving client, returning its scheduler group, retries and
// until-buried count (read under the same lock).
//
// A RUN of a job begins here, so this is where the manager mints the run's
// identity (see runToken) and clears the fields that described the run before. The
// runner makes its working directory, mounts filesystems and starts the Cmd on
// the strength of the reservation alone, before its Started reaches us.
func (s *Server) resetJobForReservation(sjob *Job, clientID uuid.UUID) (string, uint8, uint8) {
	sjob.Lock()
	defer sjob.Unlock()

	sjob.ReservedBy = clientID // *** we should unset this on moving out of run state, to save space
	sjob.Exited = false
	// Host/Pid are NOT zeroed here: respondWithReservedJob records the reserving
	// runner's host+pid so a reserved-not-started job's liveness can be confirmed.
	// StartTime stays zeroed - it is set at Started.
	sjob.StartTime = time.Time{}
	sjob.EndTime = time.Time{}
	sjob.PeakRAM = 0
	sjob.PeakDisk = 0
	sjob.Exitcode = -1
	sjob.killCalled = false

	// the identity of the run beginning now, which nothing pinned to an earlier
	// run of this job answers to.
	sjob.runID = s.mintRunToken()

	// this run has not been lost. Lost gates every lost-run decision, and
	// ttrCallback refuses to re-mark an already-lost job, so a Lost carried into a
	// fresh reservation would park the job for ever.
	sjob.Lost = false

	// nor has it made a working directory or landed on a machine yet. ActualCwd is
	// what cleanup deletes and what a `run` behaviour executes in, and HostID is
	// what killJobsOnBadServers matches condemned cloud servers against.
	sjob.ActualCwd = ""
	sjob.HostID = ""

	return sjob.schedulerGroup, sjob.Retries, sjob.UntilBuried
}

// handleStart records that a reserved job's command has started running.
func (s *Server) handleStart(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	// update the job's cmd-started-related properties
	if cr.Job == nil {
		return nil, ErrBadRequest, ""
	}

	_, job, srerr := s.getij(cr, true)
	if srerr != "" {
		return nil, srerr, ""
	}

	if !s.applyJobStart(job, cr.Job) {
		return nil, ErrBadRequest, ""
	}

	// we'll save-to-disk that we started running this job, so recovery is
	// possible after a crash
	s.db.updateJobAfterChange(ctx, job)

	return nil, "", ""
}

// applyJobStart records the host/pid/start-time of a started job under lock,
// returning false (changing nothing) if the request lacked a pid or host.
//
// It is where the manager first learns the working directory the runner made for
// this run, created before the runner calls Started. It does not mint the run's
// identity: the run began at Reserve.
func (s *Server) applyJobStart(job, crJob *Job) bool {
	job.Lock()
	defer job.Unlock()

	if crJob.Pid <= 0 || crJob.Host == "" {
		return false
	}

	// idempotent ack: a DUPLICATE report of the SAME start (e.g. retryStartReport
	// re-sending after a reply was lost) must not re-increment Attempts, which would
	// prematurely erode the retry budget (UntilBuried) and bury the job one real
	// attempt early. A genuinely new attempt is a new process (different pid) or a
	// different host, so Running + same pid + same host uniquely identifies a
	// duplicate of the current start (pids are not reused fast enough to alias). We
	// still clear Lost, because the duplicate report is fresh proof the runner is
	// alive (a spurious TTR-driven markJobLost can set Lost=true while State stays
	// Running); we deliberately do NOT touch Attempts/StartTime/EndTime, which is the
	// whole point of the guard. We DO adopt a first-seen runner pid: a job that went
	// Running with RunnerPid==0 (recovered from the DB, or first-started by an older
	// runner that reported none) needs the current runner's pid recorded so
	// the confirm-dead check keeps the both-pid liveness protection instead of falling
	// back to the command-pid-only verdict; we only fill an unset RunnerPid and never
	// overwrite or clobber an existing one.
	if job.State == JobStateRunning && job.Pid == crJob.Pid && job.Host == crJob.Host {
		job.Lost = false

		if job.RunnerPid == 0 && crJob.RunnerPid > 0 {
			job.RunnerPid = crJob.RunnerPid
		}

		return true
	}

	job.Host = crJob.Host
	if job.Host != "" {
		job.HostID = s.scheduler.HostToID(job.Host)
	}

	job.HostIP = crJob.HostIP
	job.Pid = crJob.Pid
	job.RunnerPid = crJob.RunnerPid
	job.StartTime = time.Now()
	job.EndTime = time.Time{}
	job.Attempts++
	job.setActualCwd(crJob.ActualCwd)

	// a reservation whose reserve-to-Started stretch outlasts the TTR is declared
	// lost while its Cmd starts, under this run's own token, so taking the job off
	// lost here is all that stands between that confirmation and a live Cmd.
	job.Lost = false
	job.State = JobStateRunning

	return true
}

// handleTouch refreshes a running job's TTR, recovering it from lost state and
// applying any live status snapshot, or reports that kill has been called.
func (s *Server) handleTouch(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	item, job, srerr := s.getij(cr, true)
	if srerr != "" {
		return nil, srerr, ""
	}

	// if kill has been called for this job, just return KillCalled
	job.RLock()
	killCalled := job.killCalled
	lost := job.Lost
	job.RUnlock()

	if !killCalled {
		// also just return killCalled if server has been set to kill all jobs
		killCalled = s.inShutdown()
	}

	if killCalled {
		return &serverResponse{KillCalled: true}, "", ""
	}

	srerr, qerr := s.touchJob(ctx, cr, item, job, lost)

	return &serverResponse{KillCalled: false}, srerr, qerr
}

// touchJob updates the job's TTR and routes its lost->running count and any live
// subscription snapshot through the single transition chokepoint.
func (s *Server) touchJob(ctx context.Context, cr *clientRequest, item *queue.Item, job *Job,
	lost bool) (string, string) {
	var srerr, qerr string

	// else, update the job's ttr
	if err := s.q.Touch(item.Key); err != nil {
		srerr = ErrInternalError
		qerr = err.Error()
	}

	var counts []countContribution

	if srerr == "" && lost {
		counts = append(counts, s.recoverLostTouchedJob(job))
	}

	// route both projections through the single chokepoint: the lost -> running
	// count (if recovering a lost job) and the live subscription update (if a
	// snapshot is present). The two are independently conditioned, but pairing
	// them here makes it impossible to record one without considering the other.
	// No lock is held here (q.Touch released queue.mutex), and the emitter
	// helpers manage their own job/subscription locking.
	s.emitJobTransition(counts, func() {
		s.emitLiveTouchSnapshot(ctx, cr, job, srerr)
	})

	return srerr, qerr
}

// emitLiveTouchSnapshot applies any live status snapshot from a touch request to
// job and enqueues a subscription update, unless the touch failed or live touch
// updates are disabled/absent.
func (s *Server) emitLiveTouchSnapshot(ctx context.Context, cr *clientRequest, job *Job, srerr string) {
	if srerr != "" || !s.liveJTouchEnabled() || !liveSnapshotPresent(cr.JobEndState) {
		return
	}

	applyLiveSnapshot(job, cr.JobEndState)

	// Building the subscription update decompresses the job's stdout/stderr and
	// enqueues a per-job update; that is wasted work when no client is subscribed
	// to receive per-job updates (the common case - every touch would otherwise
	// pay it). Skip it via the same idle fast-path the change-callback delivery
	// path uses, so a touch with no subscribers stays cheap. The absolute web UI
	// status counts are maintained separately and are unaffected.
	if !s.hasAnyClientSubscriptions() {
		return
	}

	update, err := jobUpdateFromLiveJob(job)
	if err != nil {
		clog.Warn(ctx, "failed to build live subscription update", "err", err)
	} else {
		s.enqueueSubscriptionUpdate(update, false)
	}
}

// recoverLostTouchedJob clears a lost job's lost state on touch and returns the
// lost->running count contribution to record.
func (s *Server) recoverLostTouchedJob(job *Job) countContribution {
	job.Lock()
	job.Lost = false
	job.EndTime = time.Time{}
	repGroup := job.RepGroup
	job.Unlock()

	// our changed callback won't be called, so this lost -> running transition's
	// count is recorded via the chokepoint, which broadcasts it as a jstateCount
	// delta (statusCaster derives the "+all+" aggregate from the contribution).
	return countContribution{from: JobStateLost, to: JobStateRunning, repGroup: repGroup, n: 1}
}

// handleArchive removes a successfully completed job from the queue, rpl and
// live bucket, and adds it to the complete bucket.
func (s *Server) handleArchive(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	// remove the job from the queue, rpl and live bucket and add to complete
	// bucket. getijForReport accepts the owner's successful archive while the item
	// is in ANY in-flight sub-queue (Run, or Delay/Ready after a busy manager
	// speculatively released it) - not just Run - so a completed job's result is
	// not discarded and re-run. A job that is already gone-and-complete is handled
	// idempotently (jobAlreadyComplete), a new owner yields ErrMustReserve
	// (new-run-wins) and a missing item during recovery yields ErrRecovering.
	job, srerr := s.getijForReport(cr)
	if srerr != "" {
		if srerr == ErrBadJob && s.jobAlreadyComplete(cr.key()) {
			return nil, "", "" // idempotent: the job is already archived/complete
		}

		return nil, srerr, ""
	}

	key, rgroup, sgroup, srerr := markJobComplete(job, cr.JobEndState, s.limiter, cr.ClientID)
	if srerr != "" {
		return nil, srerr, ""
	}

	return s.archiveCompletedJob(ctx, job, key, rgroup, sgroup)
}

// markJobComplete applies a successfully exited job's terminal state and marks it
// complete under lock, returning its key, rep group and scheduler group (or an
// Err* string if it cannot be completed). It does NOT gate on the queue item
// state or job.State - that item/ownership gating is done by getijForReport at
// the call site (handleArchive). It validates the owner (job.ReservedBy must
// match the optional expectedReservedBy, else ErrMustReserve) and the end state
// (canCompleteFromEndState, else ErrBadRequest); on success it applies the end
// state, sets State to JobStateComplete and clears FailReason, but deliberately
// does NOT clear job.Lost (see the inline comment) so a parked-lost job's later
// removal is counted lost->complete.
func markJobComplete(job *Job, endState *JobEndState,
	lim *limiter.Limiter, expectedReservedBy ...uuid.UUID,
) (key, rgroup, sgroup, srerr string) {
	job.Lock()
	defer job.Unlock()

	if len(expectedReservedBy) > 0 && job.ReservedBy != expectedReservedBy[0] {
		return "", "", "", ErrMustReserve
	}

	if !job.canCompleteFromEndState(endState) {
		return "", "", "", ErrBadRequest
	}

	job.applySuccessfulEndStateLocked(endState, lim)
	job.State = JobStateComplete
	job.FailReason = ""
	// deliberately do NOT clear job.Lost here. A job parked Lost (Lost==true in
	// SubQueueRun) is held as `lost` by the web-UI counter; the change-callback
	// chokepoint (changeCallbackCounts) reads job.Lost at removal time to decide
	// whether this exit from the run queue is from the running or the lost bucket.
	// Clearing it before archiveCompletedJob's s.q.Remove would make the removal
	// count running->complete, whose running decrement clamps to nothing and
	// leaves a stale lost:1 that reappears as a phantom lost bar on refresh. We
	// therefore leave Lost set through the removal (exactly as removeDeletableJobs
	// leaves it for a lost job being deleted), so the removal counts lost->complete.
	// Lost is only ever surfaced when State==Running (see buildJStatus), so a
	// Complete job carrying Lost==true is invisible everywhere else.

	if endState != nil {
		job.StdOutC = endState.Stdout
		job.StdErrC = endState.Stderr
	}

	return job.Key(), job.RepGroup, job.schedulerGroup, ""
}

func (j *Job) canCompleteFromEndState(endState *JobEndState) bool {
	return endState != nil && endState.Exited && endState.Exitcode == 0 &&
		!j.StartTime.IsZero() && !endState.EndTime.IsZero()
}

func (j *Job) applySuccessfulEndStateLocked(endState *JobEndState, lim *limiter.Limiter) {
	j.decrementLimitGroupsLocked(lim)
	j.Exited = true
	j.Exitcode = endState.Exitcode
	j.PeakRAM = endState.PeakRAM
	j.PeakDisk = endState.PeakDisk
	j.CPUtime = endState.CPUtime
	j.EndTime = endState.EndTime
	j.setActualCwd(endState.Cwd)
}

// archiveCompletedJob persists a completed job to the complete bucket and
// removes it from the live queue and lookups.
func (s *Server) archiveCompletedJob(ctx context.Context, job *Job, key, rgroup, sgroup string) (
	*serverResponse, string, string,
) {
	if err := s.db.archiveJob(ctx, key, job); err != nil {
		return nil, ErrDBError, err.Error()
	}

	if err := s.q.Remove(ctx, key); err != nil {
		return nil, ErrInternalError, err.Error()
	}

	s.rpl.Lock()
	s.rpl.Delete(rgroup, key)
	s.rpl.Unlock()
	clog.Debug(ctx, "completed job", "cmd", job.Cmd, "schedGrp", sgroup)
	s.decrementGroupCount(ctx, sgroup, 1)

	return nil, "", ""
}

// handleRelease moves a job from the run queue to the delay queue, or buries it
// (if forceBury, or it has failed too many times).
func (s *Server) handleRelease(ctx context.Context, cr *clientRequest, forceBury bool,
	failMsg string) (*serverResponse, string, string) {
	// getijForReport accepts the owner's release/bury report while the item is in
	// ANY in-flight sub-queue (Run, or Delay/Ready after a busy manager
	// speculatively released it) - not just Run - mirroring handleArchive, so a
	// genuine failure report is applied rather than discarded and the job re-run.
	// A job that is already gone-and-complete is handled idempotently
	// (jobAlreadyComplete). A new owner yields ErrMustReserve (new-run-wins). A
	// terminal (e.g. a winning double-reservation runner already buried it) or
	// otherwise-gone item yields ErrBadJob, which lands in the client's give-up
	// set so the losing runner abandons the dead reservation promptly instead of
	// looping for the full 24h retryTime (reliable2 D1); a missing item during
	// recovery yields ErrRecovering.
	job, srerr := s.getijForReport(cr)
	if srerr != "" {
		if srerr == ErrBadJob && s.jobAlreadyComplete(cr.key()) {
			return nil, "", "" // idempotent: the job is already terminal
		}

		return nil, srerr, ""
	}

	if cr.JobEndState == nil {
		cr.JobEndState = &JobEndState{}
	}

	if errq := s.releaseJob(ctx, job, cr.JobEndState, cr.failReason(), true, forceBury); errq != nil {
		clog.Warn(ctx, failMsg, "err", errq)

		return nil, ErrInternalError, errq.Error()
	}

	return nil, "", ""
}

// handleKick moves the keyed jobs from the bury queue to the ready queue. Unlike
// the other j* methods the client need not be the reserver and there is no
// "in run queue" test.
func (s *Server) handleKick(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.Keys == nil {
		return nil, ErrBadRequest, ""
	}

	var jobs []*Job

	for _, key := range cr.Keys {
		item, err := s.q.Get(key)
		if err != nil || item.Stats().State != queue.ItemStateBury {
			continue
		}

		if job, ok := item.Data().(*Job); ok {
			jobs = append(jobs, job)
		}
	}

	return &serverResponse{Existed: s.kickJobs(ctx, jobs)}, "", ""
}

// handleDelete removes the keyed non-running jobs from the queue and live
// bucket.
func (s *Server) handleDelete(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	// remove the jobs from the bury/delay/dependent/ready queue and the live
	// bucket
	if cr.Keys == nil {
		return nil, ErrBadRequest, ""
	}

	jobs := s.nonRunningJobsByKeys(cr.Keys)

	deleted := s.deleteJobs(ctx, jobs)
	clog.Debug(ctx, "deleted jobs", "count", len(deleted))

	return &serverResponse{Existed: len(deleted)}, "", ""
}

// nonRunningJobsByKeys returns the jobs for the given keys that are present in
// the queue and not currently running.
func (s *Server) nonRunningJobsByKeys(keys []string) []*Job {
	var jobs []*Job

	for _, key := range keys {
		item, err := s.q.Get(key)
		if err != nil || item == nil || item.Stats().State == queue.ItemStateRun {
			continue
		}

		if job, ok := item.Data().(*Job); ok {
			jobs = append(jobs, job)
		}
	}

	return jobs
}

// handleKill sets killCalled on the keyed jobs (changing the behaviour of a
// subsequent touch). As per jkick the client need not be the reserver, but the
// "in run queue" test still applies.
func (s *Server) handleKill(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.Keys == nil {
		return nil, ErrBadRequest, ""
	}

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

	return &serverResponse{Existed: killable}, "", ""
}

// handleModify modifies the keyed non-running jobs. The server is paused while
// modifying to avoid racing jobs that become running mid-modification, and
// resumed afterwards.
func (s *Server) handleModify(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	// modify jobs in the bury/delay/dependent/ready queue and the live bucket
	if cr.Keys == nil {
		return nil, ErrBadRequest, ""
	}

	if validationErr, invalid := cr.Modifier.validationError(); invalid {
		return nil, validationErr.Err, validationErr.Item
	}

	// to avoid race conditions with jobs that are currently pending, but become
	// running in the middle of us trying to modify them, we first pause the
	// server, and resume it afterwards
	if srerr, qerr, ok := s.pauseForModify(ctx); !ok {
		return nil, srerr, qerr
	}

	modified, srerr, qerr := s.modifyJobs(ctx, cr)

	// now resume the server again
	resumed, err := s.Resume(ctx)
	switch {
	case err != nil:
		clog.Error(ctx, err.Error())
	case resumed:
		clog.Debug(ctx, "modify completed, resumed server", "count", len(modified))
	default:
		clog.Debug(ctx, "modify completed", "count", len(modified))
	}

	return &serverResponse{Modified: modified}, srerr, qerr
}

// pauseForModify pauses the server before a modify. ok is false (with error
// strings set) only if pausing failed.
func (s *Server) pauseForModify(ctx context.Context) (string, string, bool) {
	paused, err := s.Pause()
	if err != nil {
		return serverErrString(err), err.Error(), false
	}

	if paused {
		clog.Debug(ctx, "modify requested, paused server")
	} else {
		clog.Debug(ctx, "modify requested")
	}

	return "", "", true
}

// modifyJobs applies cr.Modifier to the keyed non-running jobs, persisting the
// changes, and returns the old->new key mapping plus any error strings.
func (s *Server) modifyJobs(ctx context.Context, cr *clientRequest) (map[string]string, string, string) {
	toModifyJobs, toModifyKeys := s.collectModifiableJobs(cr.Keys)

	modified, err := cr.Modifier.Modify(toModifyJobs, s)
	if err != nil {
		return modified, serverErrString(err), err.Error()
	}

	if len(modified) == 0 {
		return modified, "", ""
	}

	var toModify []*Job

	for _, old := range modified {
		if job := toModifyKeys[old]; job != nil {
			toModify = append(toModify, job)
		}
	}

	s.persistModifiedJobs(ctx, cr, modified, toModify)

	return modified, "", ""
}

// collectModifiableJobs returns the non-running jobs for the given keys, along
// with a key->job map for the originally requested keys.
func (s *Server) collectModifiableJobs(keys []string) ([]*Job, map[string]*Job) {
	var toModifyJobs []*Job

	toModifyKeys := make(map[string]*Job)

	for _, jobkey := range keys {
		item, err := s.q.Get(jobkey)
		if err != nil || item == nil || item.Stats().State == queue.ItemStateRun {
			continue
		}

		job, ok := item.Data().(*Job)
		if !ok {
			continue
		}

		toModifyJobs = append(toModifyJobs, job)
		toModifyKeys[jobkey] = job
	}

	return toModifyJobs, toModifyKeys
}

// persistModifiedJobs stores changed limit groups, updates changed keys in the
// queue and rpl lookup, and persists the modifications to the database.
func (s *Server) persistModifiedJobs(ctx context.Context, cr *clientRequest,
	modified map[string]string, toModify []*Job) {
	// additional handling of changed limit groups
	if cr.Modifier.LimitGroupsSet {
		limitGroups := make(map[string]*limiter.GroupData)

		for _, job := range toModify {
			// handleUserSpecifiedJobLimitGroups rewrites job.LimitGroups (and
			// invalidates the job's memoised derived scheduler-group strings), so
			// the job's lock must be held, as its own docs and the equivalent REST
			// path (storeModifiedLimitGroups) require.
			job.Lock()
			s.handleUserSpecifiedJobLimitGroups(job, limitGroups)
			job.Unlock()
		}

		if err := s.storeLimitGroups(limitGroups); err != nil {
			clog.Error(ctx, "failed to store limit groups", "err", err)
		}
	}

	s.changeModifiedJobKeys(ctx, modified, toModify)

	if len(toModify) > 0 {
		s.persistModifiedJobsToDB(ctx, cr, modified, toModify)
	}
}

// changeModifiedJobKeys applies the modified jobs' key changes to the queue and
// the rpl lookup.
func (s *Server) changeModifiedJobKeys(ctx context.Context, modified map[string]string, toModify []*Job) {
	// update changed keys in the queue and in our rpl lookup
	keyToRP := make(map[string]string)
	for _, job := range toModify {
		keyToRP[job.Key()] = job.RepGroup
	}

	s.rpl.Lock()
	defer s.rpl.Unlock()

	for newKey, oldKey := range modified {
		if oldKey == newKey {
			continue
		}

		if errc := s.q.ChangeKey(oldKey, newKey); errc != nil {
			clog.Error(ctx, "failed to change a job key in the queue", "err", errc)
		}

		rp := keyToRP[newKey]
		s.rpl.Delete(rp, oldKey)
		s.rpl.Add(rp, newKey)
	}
}

// persistModifiedJobsToDB writes the modified jobs to the database live bucket
// and, if dependencies or priority changed, reflects that in the queue too.
func (s *Server) persistModifiedJobsToDB(ctx context.Context, cr *clientRequest,
	modified map[string]string, toModify []*Job) {
	oldKeys := make([]string, len(toModify))
	for i, job := range toModify {
		oldKeys[i] = modified[job.Key()]
	}

	if errm := s.db.modifyLiveJobs(ctx, oldKeys, toModify); errm != nil {
		clog.Error(ctx, "job modification in database failed", "err", errm)

		return
	}

	if !cr.Modifier.DependenciesSet && !cr.Modifier.PrioritySet {
		return
	}

	// if we're changing the jobs these jobs are dependant upon or their
	// priority, that must be reflected in the queue as well
	for _, job := range toModify {
		s.reflectModifiedJobInQueue(ctx, job)
	}
}

// reflectModifiedJobInQueue updates a modified job's dependencies in the queue.
func (s *Server) reflectModifiedJobInQueue(ctx context.Context, job *Job) {
	deps, waitingForDepGroups, depErr := job.Dependencies.incompleteJobKeys(s.db)
	if depErr != nil {
		clog.Error(ctx, "failed to get job dependencies", "err", depErr)

		return
	}

	job.setWaitingForDepGroups(waitingForDepGroups)

	err := s.q.Update(
		ctx, job.Key(), job.getSchedulerGroup(), job, job.Priority,
		0*time.Second, s.itemTTRDuration(), deps,
	)
	if err != nil {
		clog.Error(ctx, "failed to modify a job in the queue", "err", err)
	}
}

// handleGetByKeys gets jobs by their keys (which come from their Cmds & Cwds).
func (s *Server) handleGetByKeys(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.Keys == nil {
		return nil, ErrBadRequest, ""
	}

	jobs, srerr, qerr := s.getJobsByKeys(ctx, cr.Keys, cr.GetStd, cr.GetEnv)

	return jobsResponse(jobs), srerr, qerr
}

// handleGetByRepGroup gets jobs by their RepGroup.
func (s *Server) handleGetByRepGroup(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.Job == nil || cr.Job.RepGroup == "" {
		return nil, ErrBadRequest, ""
	}

	// this is the request `wr status -i` makes, and Client.GetByRepGroupMatch's
	// documented contract is to return complete jobs as well as live ones, so it
	// asks for the history. What keeps a caller that can only act on LIVE jobs off
	// that scan is its State filter: `wr suspend`/`wr resume` send
	// JobStateIncomplete, `wr remove`/`wr mod` JobStateDeletable, and any
	// non-complete state stops getDBJobsByRepGroup before it reads the DB.
	opts := repGroupOptions{
		RepGroup:        cr.Job.RepGroup,
		Match:           normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search),
		IncludeComplete: true,
		limitJobsOptions: limitJobsOptions{
			Limit:               cr.Limit,
			State:               cr.State,
			GetStd:              cr.GetStd,
			GetEnv:              cr.GetEnv,
			WaitingForDepGroups: cr.WaitingForDepGroups,
		},
	}

	jobs, srerr, qerr := s.getJobsByRepGroup(ctx, opts)

	return jobsResponse(jobs), srerr, qerr
}

// handleGetRepGroupStatus returns status summaries for a rep group.
func (s *Server) handleGetRepGroupStatus(cr *clientRequest) (*serverResponse, string, string) {
	repGroup := ""
	if cr.Job != nil {
		repGroup = cr.Job.RepGroup
	}

	summaries, srerr, qerr := s.getStatusByRepGroup(repGroupStatusOptions{
		RepGroup:             repGroup,
		Match:                normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search),
		States:               cr.States,
		IncludeComplete:      cr.IncludeComplete,
		IncludeStatusDetails: cr.IncludeStatusDetails,
	})
	if srerr != "" {
		return nil, srerr, qerr
	}

	return &serverResponse{StatusSummaries: summaries}, "", qerr
}

// handleGetIncomplete gets incomplete jobs, optionally filtered by rep group.
func (s *Server) handleGetIncomplete(ctx context.Context, cr *clientRequest) *serverResponse {
	repGroup := ""
	if cr.Job != nil {
		repGroup = cr.Job.RepGroup
	}

	match := normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search)

	jobs := s.getJobsCurrent(ctx, repGroup, match, cr.Limit, cr.State,
		cr.GetStd, cr.GetEnv, cr.WaitingForDepGroups)

	return jobsResponse(jobs)
}

// handleGetRecent gets archived jobs finished within cr.Period.
func (s *Server) handleGetRecent(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.Period <= 0 {
		return nil, ErrBadRequest, ""
	}

	jobs, srerr, qerr := s.getJobsRecent(ctx, cr.Period, cr.Limit, cr.GetStd, cr.GetEnv)

	return jobsResponse(jobs), srerr, qerr
}

// handleGetLastCompletionTime returns the last completion times for a rep group.
func (s *Server) handleGetLastCompletionTime(cr *clientRequest) (*serverResponse, string, string) {
	if cr.Job == nil || cr.Job.RepGroup == "" {
		return nil, ErrBadRequest, ""
	}

	match := normalizeRepGroupMatch(cr.RepGroupMatch, cr.Search)

	m, srerr, qerr := s.getLastCompletionTimeByRepGroup(cr.Job.RepGroup, match)
	if srerr != "" {
		return nil, srerr, qerr
	}

	return &serverResponse{CompletionTimes: m}, "", qerr
}

// handleGetBadServers returns the current bad servers, optionally confirming
// them dead (and killing their jobs) first.
func (s *Server) handleGetBadServers(ctx context.Context, cr *clientRequest) *serverResponse {
	servers := s.getBadServers()

	if cr.ConfirmDeadCloudServers {
		confirmed, jobs := s.killBadCloudServers(ctx, servers, cr.CloudServerID)

		return &serverResponse{BadServers: confirmed, Jobs: jobs}
	}

	return &serverResponse{BadServers: servers}
}

// handleDestroyCloudHost destroys a named cloud host and returns it plus its
// affected jobs.
func (s *Server) handleDestroyCloudHost(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.DestroyCloudHost == "" {
		return nil, ErrBadRequest, ""
	}

	server, jobs := s.killCloudServer(ctx, cr.DestroyCloudHost)
	if server == nil {
		return nil, "", ""
	}

	return &serverResponse{BadServers: []*BadServer{server}, Jobs: jobs}, "", ""
}

// handleGetSetLimitGroup gets or sets a limit group and returns its limit.
func (s *Server) handleGetSetLimitGroup(ctx context.Context, cr *clientRequest) (*serverResponse, string, string) {
	if cr.LimitGroup == "" {
		return nil, ErrBadRequest, ""
	}

	limit, serr, err := s.getSetLimitGroup(ctx, cr.LimitGroup)
	if err != nil {
		return nil, serr, err.Error()
	}

	return &serverResponse{Limit: int(limit.Limit())}, "", ""
}

// jobsResponse returns a response containing jobs, or nil if there are none.
func jobsResponse(jobs []*Job) *serverResponse {
	if len(jobs) == 0 {
		return nil
	}

	return &serverResponse{Jobs: jobs}
}

// dispatchMethod runs the handler for the client request's method, returning the
// response (if any) and any server/detailed error strings.
//
//nolint:cyclop,gocyclo,funlen // a flat command dispatch switch; each case delegates to a handler
func (s *Server) dispatchMethod(ctx context.Context, cr *clientRequest, drain bool) (
	sr *serverResponse, srerr, qerr string,
) {
	switch cr.Method {
	case requestMethodPing:
		return s.handlePing(), "", ""
	case "backup":
		return s.handleBackup(ctx)
	case "pause":
		return s.handlePause(ctx)
	case "resume":
		return s.handleResume(ctx)
	case "drain":
		return s.handleDrain(ctx)
	case "shutdown":
		clog.Debug(ctx, "shutdown requested")
		go s.Stop(ctx, true) // server stop can't complete while this client request is pending

		return nil, "", ""
	case "upload":
		return s.handleUpload(ctx, cr)
	case requestMethodAdd:
		return s.handleAdd(ctx, cr)
	case requestMethodSubscribe:
		return s.handleSubscribe(ctx, cr)
	case requestMethodUnsubscribe:
		return s.handleUnsubscribe(cr)
	case requestMethodWaitForUpdates:
		return s.handleWaitForUpdates(cr)
	case requestMethodReserve:
		// return the next ready job
		return s.handleReserve(ctx, cr, drain)
	case requestMethodStart:
		return s.handleStart(ctx, cr)
	case requestMethodTouch:
		return s.handleTouch(ctx, cr)
	case "jarchive":
		return s.handleArchive(ctx, cr)
	case "jrelease":
		return s.handleRelease(ctx, cr, false, "releaseJob failed")
	case "jbury":
		return s.handleRelease(ctx, cr, true, "releaseJob to bury failed")
	case "jkick":
		return s.handleKick(ctx, cr)
	case "jsuspend":
		// suspend eligible jobs; client doesn't have to be the Reserve() owner
		// and ineligible or missing keys are ignored.
		if cr.Keys == nil {
			return nil, ErrBadRequest, ""
		}

		suspended := s.suspendJobs(ctx, cr.Keys)
		clog.Debug(ctx, "suspended jobs", "count", suspended)

		return &serverResponse{Existed: suspended}, "", ""
	case "jresume":
		// resume suspended jobs; client doesn't have to be the Reserve() owner
		// and ineligible or missing keys are ignored.
		if cr.Keys == nil {
			return nil, ErrBadRequest, ""
		}

		resumed := s.resumeJobs(ctx, cr.Keys)
		clog.Debug(ctx, "resumed suspended jobs", "count", resumed)

		return &serverResponse{Existed: resumed}, "", ""
	case "jdel":
		return s.handleDelete(ctx, cr)
	case requestMethodModify:
		return s.handleModify(ctx, cr)
	case "jkill":
		return s.handleKill(ctx, cr)
	case requestMethodGetByCmd:
		return s.handleGetByKeys(ctx, cr)
	case "getbr":
		return s.handleGetByRepGroup(ctx, cr)
	case "getrs":
		return s.handleGetRepGroupStatus(cr)
	case requestMethodGetIncomplete:
		return s.handleGetIncomplete(ctx, cr), "", ""
	case requestMethodGetRecent:
		return s.handleGetRecent(ctx, cr)
	case "getlct":
		return s.handleGetLastCompletionTime(cr)
	case requestMethodGetBadServers:
		return s.handleGetBadServers(ctx, cr), "", ""
	case "dch":
		return s.handleDestroyCloudHost(ctx, cr)
	case "getsetlg":
		return s.handleGetSetLimitGroup(ctx, cr)
	case "getlgs":
		return &serverResponse{LimitGroups: s.limiter.GetLimits()}, "", ""
	default:
		return nil, ErrUnknownCommand, ""
	}
}

// getijForReport resolves the item and job for a runner's FINAL-state report
// (archive/release/bury). Unlike getij(cr, true) it does NOT require the item to
// still be in the Run sub-queue: an exiting runner is the authority on its
// command's outcome, so while it still holds the reservation (job.ReservedBy ==
// cr.ClientID) its report is applied wherever a busy manager has parked the item
// (Run, or Delay after a speculative lost-release), rather than being rejected as
// ErrBadJob and re-run. A report from a client that no longer holds the
// reservation - a genuinely new runner took the job over - is rejected with
// ErrMustReserve (new-run-wins). A missing item is retryable during recovery
// (ErrRecovering) and otherwise ErrBadJob (the caller may still treat an
// already-completed job idempotently via jobAlreadyComplete).
func (s *Server) getijForReport(cr *clientRequest) (*Job, string) {
	key := cr.key()
	if key == "" {
		return nil, ErrBadRequest
	}

	item, err := s.q.Get(key)
	if err != nil {
		if s.isRecovering() {
			return nil, ErrRecovering
		}

		return nil, ErrBadJob
	}

	// accept a report only for an IN-FLIGHT item: Run (normal, or parked Lost),
	// or Delay/Ready after a busy manager speculatively released it. A TERMINAL
	// item (Bury) or any other state is authoritatively "gone/resolved", so we
	// return ErrBadJob and the runner gives up cleanly (D1) instead of looping on
	// an internal release error - and an already-completed job is handled
	// idempotently by the caller via jobAlreadyComplete.
	switch item.Stats().State {
	case queue.ItemStateRun, queue.ItemStateDelay, queue.ItemStateReady:
	default:
		return nil, ErrBadJob
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return nil, ErrBadJob
	}

	if cr.ClientID != job.ReservedBy {
		return job, ErrMustReserve
	}

	return job, ""
}

// jobAlreadyComplete reports whether the keyed job is already in the completed
// bucket. It makes a runner's archive/release retry idempotent when a busy
// manager processed the first attempt but the response was lost to a client
// timeout: the job is already done, so the caller returns success rather than
// ErrBadJob (which the runner treats as a reason to re-run an already-complete
// job, discarding the work and doubling it up).
func (s *Server) jobAlreadyComplete(key string) bool {
	complete, err := s.db.checkIfComplete(key)

	return err == nil && complete
}

// for the many j* methods in handleRequest, we do this common stuff to get
// the desired item and job. The returned string is one of our Err* constants.
func (s *Server) getij(cr *clientRequest, checkRunning bool) (*queue.Item, *Job, string) {
	key := cr.key()
	if key == "" {
		return nil, nil, ErrBadRequest
	}

	item, err := s.q.Get(key)
	if err != nil {
		// the item is not in the queue. During the recovery window a
		// to-be-restored job legitimately misses; report a retryable error so a
		// reconnecting runner retries rather than treating it as a permanent
		// failure (spec B2). Outside recovery this is a real bad job.
		if s.isRecovering() {
			return item, nil, ErrRecovering
		}

		return item, nil, ErrBadJob
	}

	if checkRunning && item.Stats().State != queue.ItemStateRun {
		// the item exists but is in the wrong sub-queue: a real state error, not
		// a recovery-timing miss, so it stays ErrBadJob even while recovering.
		return item, nil, ErrBadJob
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return item, nil, ErrBadJob
	}

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
	sjob := item.Data().(*Job) //nolint:errcheck,forcetypeassert // queue only ever stores *Job
	sjob.RLock()

	state := s.itemStateToJobState(item.Stats().State, sjob.Lost)
	if state == JobStateReserved && !sjob.StartTime.IsZero() {
		state = JobStateRunning
	}

	// we're going to fill in some properties of the Job and return it to client,
	// but don't want those properties set here for us, so we make a new Job and
	// fill stuff in that
	job := copyJobForClient(sjob, state)

	if getStd && (state == JobStateReserved || state == JobStateRunning || state == JobStateLost) {
		job.StdErrC = sjob.StdErrC
		job.StdOutC = sjob.StdOutC
	}

	sjob.RUnlock()
	s.jobPopulateStdEnv(ctx, job, getStd, getEnv)

	return job
}

// copyJobForClient returns a copy of sjob (which must be read-locked) with the
// given state, suitable for sending to a client. The requirements are deep
// copied because the server mutates the original's.
//
//nolint:funlen // a flat field-by-field copy of the many-fielded Job struct
func copyJobForClient(sjob *Job, state JobState) *Job {
	req := &scheduler.Requirements{}
	*req = *sjob.Requirements // copy reqs since server changes these, avoiding a race condition

	return &Job{
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
	limitGroups, wait, ok := s.incrementReserveLimit(ctx, group, wait)
	if !ok {
		return nil, queue.Error{Queue: s.q.Name, Op: "Reserve", Item: "", Err: queue.ErrNothingReady}
	}

	item, err := s.q.Reserve(group, wait)

	s.noteReserveLimitGroups(item, limitGroups)

	return item, err
}

// incrementReserveLimit determines a scheduler group's limit groups and, if any,
// increments their usage before reserving (reducing wait by the time spent).
// ok is false if the limit was reached, in which case the caller should act as
// if the queue were empty.
func (s *Server) incrementReserveLimit(ctx context.Context, group string,
	wait time.Duration) ([]string, time.Duration, bool) {
	if group == "" {
		return nil, wait, true
	}

	limitGroups := s.schedGroupToLimitGroups(group)
	if len(limitGroups) == 0 {
		return limitGroups, wait, true
	}

	// it is better to call Increment before Reserve and possibly use up the
	// limit for up to wait period if there's no item in the queue, than it is to
	// Reserve first and then Release if at the limit, because Releasing causes
	// scheduler churn
	t := time.Now()

	if !s.limiter.Increment(ctx, limitGroups, wait) {
		return limitGroups, wait, false
	}

	return limitGroups, wait - time.Since(t), true
}

// noteReserveLimitGroups updates limit-group accounting after a reserve: it
// decrements the groups if nothing was reserved, otherwise records the
// increment on the reserved job.
func (s *Server) noteReserveLimitGroups(item *queue.Item, limitGroups []string) {
	if len(limitGroups) == 0 {
		return
	}

	if item == nil {
		s.limiter.Decrement(limitGroups)

		return
	}

	if job, ok := item.Data().(*Job); ok {
		job.noteIncrementedLimitGroups(limitGroups)
	}
}

// schedGroupToLimitGroups takes a scheduler group that may be suffixed with
// limit groups (by Job.generateSchedulerGroup()), and returns the extracted
// limit groups.
func (s *Server) schedGroupToLimitGroups(group string) []string {
	parts := strings.Split(group, jobSchedLimitGroupSeparator)
	if len(parts) == schedGroupWithLimitParts {
		return strings.Split(parts[1], jobLimitGroupSeparator)
	}

	return nil
}

// reply to a client.
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
