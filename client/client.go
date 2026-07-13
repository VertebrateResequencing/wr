/*******************************************************************************
 * Copyright (c) 2025-2026 Genome Research Ltd.
 *
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15/v3"
	"github.com/rs/xid"
)

// ErrDuplicateJobs is returned by SubmitJobs when any submitted jobs already
// exist in the queue.
var ErrDuplicateJobs = errors.New("some of the added jobs were duplicates")

var errWaitForJobsJobqueueClient = errors.New("WaitForJobs requires a jobqueue client")

// PretendSubmissions as a non-empty string causes SubmitJobs to only record the
// jobs for retrieval by SubmittedJobs(); no wr manager server is needed or
// used.
//
// This variable can either be set directly in test code or when building by
// adding the following (for example):
//
// -ldflags='-X github.com/VertebrateResequencing/wr/client.PretendSubmissions=Y'
//
// If set to a number, SubmitJobs will print JSON encoded data to that file
// descriptor.
var PretendSubmissions string //nolint:gochecknoglobals

// some consts used by Scheduler.
const (
	getByEssenceOp                 = "GetByEssence"
	getByRepGroupMatchOp           = "GetByRepGroupMatch"
	getJobByKeyOp                  = "GetJobByKey"
	newJobFromJSONOp               = "NewJobFromJSON"
	submitJobsAndReturnIDsOp       = "SubmitJobsAndReturnIDs"
	submitJobsAndWaitOp            = "SubmitJobsAndWait"
	submitJobsOp                   = "SubmitJobs"
	waitForRunningOp               = "WaitForRunning"
	waitForJobsOp                  = "WaitForJobs"
	jobRetries               uint8 = 30
	reqRAM                         = 100
	reqTime                        = 10 * time.Second
	reqCores                       = 1
	reqDisk                        = 1

	waitForRunningDefaultPollInterval = 5 * time.Second
)

type Error string

func (e Error) Error() string { return string(e) }

type SchedulerSettings struct {
	Deployment  string
	Cwd         string
	Queue       string
	QueuesAvoid string
	Timeout     time.Duration
	Logger      log15.Logger
}

// SubmitJobsOptions controls how Scheduler job submission handles environment
// variables and already-completed matching jobs.
type SubmitJobsOptions struct {
	// EnvVars is passed to wr for job execution. nil means os.Environ().
	// A non-nil empty slice means no environment variables.
	EnvVars []string

	// RerunCompleted matches `wr add --rerun`. false skips already complete
	// matching jobs; true re-adds them.
	RerunCompleted bool
}

func (opts SubmitJobsOptions) envVars() []string {
	if opts.EnvVars == nil {
		return os.Environ()
	}

	return opts.EnvVars
}

func (opts SubmitJobsOptions) ignoreComplete() bool {
	return !opts.RerunCompleted
}

//nolint:interfacebloat // mirrors the subset of the jobqueue client API this package uses
type jobqueueClient interface {
	Add(jobs []*jobqueue.Job, envVars []string, ignoreComplete bool) (added int, existed int, err error)
	AddAndReturnIDs(jobs []*jobqueue.Job, envVars []string, ignoreComplete bool) ([]string, error)
	AddAndWait(ctx context.Context, jobs []*jobqueue.Job, envVars []string,
		ignoreComplete bool) ([]*jobqueue.Job, error)
	GetByEssence(je *jobqueue.JobEssence, getStd bool, getEnv bool) (*jobqueue.Job, error)
	GetByRepGroup(repgroup string, subStr bool, limit int,
		state jobqueue.JobState, getStd bool, getEnv bool) ([]*jobqueue.Job, error)
	GetByRepGroupMatch(repgroup string, match jobqueue.RepGroupMatch, limit int,
		state jobqueue.JobState, getStd bool, getEnv bool) ([]*jobqueue.Job, error)
	GetIncompleteByRepGroupMatch(repgroup string, match jobqueue.RepGroupMatch,
		limit int, state jobqueue.JobState, getStd bool, getEnv bool) ([]*jobqueue.Job, error)
	GetLastCompletionTimeByRepGroup(repgroup string,
		match jobqueue.RepGroupMatch) (map[string]time.Time, error)
	GetSchedulerAlerts() (*jobqueue.SchedulerAlerts, error)
	Delete(jes []*jobqueue.JobEssence) (int, error)
	Disconnect() error
}

type pretendJobqueue struct {
	jobBuffer []*jobqueue.Job
	output    io.WriteCloser
}

func newPretendJobqueue() *pretendJobqueue {
	var w io.WriteCloser

	fd, errr := strconv.Atoi(PretendSubmissions)
	if errr == nil {
		dupFD, err := syscall.Dup(fd)
		if err == nil {
			syscall.CloseOnExec(dupFD)
			w = os.NewFile(uintptr(dupFD), "pretend-submissions")
		}
	}

	return &pretendJobqueue{output: w}
}

func (p *pretendJobqueue) Add(jobs []*jobqueue.Job, _ []string, _ bool) (int, int, error) {
	for _, job := range jobs {
		job.State = jobqueue.JobStateDelayed
	}

	p.jobBuffer = append(p.jobBuffer, jobs...)

	if p.output != nil {
		json.NewEncoder(p.output).Encode(jobs) //nolint:errcheck,errchkjson
	}

	return len(jobs), 0, nil
}

func (p *pretendJobqueue) AddAndReturnIDs(jobs []*jobqueue.Job,
	envVars []string, ignoreComplete bool) ([]string, error) {
	_, _, err := p.Add(jobs, envVars, ignoreComplete)
	if err != nil {
		return nil, err
	}

	keys := make([]string, len(jobs))
	for n, job := range jobs {
		keys[n] = job.Key()
	}

	return keys, nil
}

func (p *pretendJobqueue) AddAndWait(ctx context.Context, jobs []*jobqueue.Job,
	_ []string, _ bool) ([]*jobqueue.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for _, job := range jobs {
		job.State = jobqueue.JobStateComplete
		job.Exited = true
		job.Exitcode = 0
		job.EndTime = time.Now()
	}

	p.jobBuffer = append(p.jobBuffer, jobs...)

	if p.output != nil {
		json.NewEncoder(p.output).Encode(jobs) //nolint:errcheck,errchkjson
	}

	return distinctJobsInKeyOrder(jobs), nil
}

func distinctJobsInKeyOrder(jobs []*jobqueue.Job) []*jobqueue.Job {
	if jobs == nil {
		return nil
	}

	seen := make(map[string]struct{}, len(jobs))
	distinct := make([]*jobqueue.Job, 0, len(jobs))

	for _, job := range jobs {
		if job == nil {
			continue
		}

		key := job.Key()
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		distinct = append(distinct, job)
	}

	return distinct
}

func (p *pretendJobqueue) GetByEssence(je *jobqueue.JobEssence, _ bool,
	_ bool) (*jobqueue.Job, error) {
	if je == nil || je.Key() == "" {
		return nil, jobqueue.Error{Op: getByEssenceOp, Err: jobqueue.ErrBadRequest}
	}

	key := je.Key()
	for _, job := range p.jobBuffer {
		if job.Key() == key {
			return job, nil
		}
	}

	return nil, jobqueue.Error{Op: getByEssenceOp, Item: key, Err: jobqueue.ErrBadJob}
}

func (p *pretendJobqueue) SubmittedJobs() []*jobqueue.Job {
	sj := p.jobBuffer

	return sj
}

func (p *pretendJobqueue) GetSchedulerAlerts() (*jobqueue.SchedulerAlerts, error) {
	return &jobqueue.SchedulerAlerts{}, nil
}

// GetByRepGroup behaves like jobqueue.GetByRepGroup, but only repgroup is
// considered (as a substring).
func (p *pretendJobqueue) GetByRepGroup(repgroup string, _ bool, _ int,
	state jobqueue.JobState, _ bool, _ bool) ([]*jobqueue.Job, error) {
	return p.GetByRepGroupMatch(repgroup, jobqueue.RepGroupMatchSubStr, 0,
		state, false, false)
}

// GetByRepGroupMatch behaves like jobqueue.GetByRepGroupMatch, but only
// repgroup, match mode and state are considered.
func (p *pretendJobqueue) GetByRepGroupMatch(repgroup string,
	match jobqueue.RepGroupMatch, _ int, state jobqueue.JobState, _ bool,
	_ bool) ([]*jobqueue.Job, error) {
	if repgroup == "" {
		return nil, jobqueue.Error{Op: getByRepGroupMatchOp, Err: jobqueue.ErrBadRequest}
	}

	var jobs []*jobqueue.Job

	for _, job := range p.jobBuffer {
		if jobqueue.RepGroupMatches(job.RepGroup, repgroup, match) && (state == "" || job.State == state) {
			jobs = append(jobs, job)
		}
	}

	return jobs, nil
}

// GetIncompleteByRepGroupMatch behaves like
// jobqueue.GetIncompleteByRepGroupMatch, but only repgroup/match and state are
// considered; the limit and final boolean arguments (eg. getStd/getEnv) are
// ignored in this pretend implementation.
func (p *pretendJobqueue) GetIncompleteByRepGroupMatch(repgroup string,
	match jobqueue.RepGroupMatch, _ int, state jobqueue.JobState, _ bool,
	_ bool) ([]*jobqueue.Job, error) {
	jobs := make([]*jobqueue.Job, 0, len(p.jobBuffer))

	for _, job := range p.jobBuffer {
		if !matchesIncompleteRepGroup(job.RepGroup, repgroup, match) {
			continue
		}

		if !isIncompleteStateMatch(job.State, state) {
			continue
		}

		jobs = append(jobs, job)
	}

	return jobs, nil
}

// GetLastCompletionTimeByRepGroup behaves like
// jobqueue.GetLastCompletionTimeByRepGroup, but only complete jobs currently
// in memory are considered.
func (p *pretendJobqueue) GetLastCompletionTimeByRepGroup(repgroup string,
	match jobqueue.RepGroupMatch) (map[string]time.Time, error) {
	completionTimes := make(map[string]time.Time)

	for _, job := range p.jobBuffer {
		if job.State != jobqueue.JobStateComplete {
			continue
		}

		if !jobqueue.RepGroupMatches(job.RepGroup, repgroup, match) {
			continue
		}

		current, found := completionTimes[job.RepGroup]
		if !found || current.Before(job.EndTime) {
			completionTimes[job.RepGroup] = job.EndTime
		}
	}

	return completionTimes, nil
}

func matchesIncompleteRepGroup(jobRepGroup, repgroup string,
	match jobqueue.RepGroupMatch) bool {
	if repgroup == "" {
		return true
	}

	return jobqueue.RepGroupMatches(jobRepGroup, repgroup, match)
}

func isIncompleteStateMatch(jobState, state jobqueue.JobState) bool {
	if jobState == jobqueue.JobStateComplete {
		return false
	}

	return state == "" || jobState == state
}

func (p *pretendJobqueue) Delete(jeses []*jobqueue.JobEssence) (int, error) {
	origLen := len(p.jobBuffer)

	p.jobBuffer = slices.DeleteFunc(p.jobBuffer, func(job *jobqueue.Job) bool {
		for _, jes := range jeses {
			if job.Key() == jes.JobKey {
				return true
			}
		}

		return false
	})

	return origLen - len(p.jobBuffer), nil
}

func (p *pretendJobqueue) Disconnect() error {
	if p.output == nil {
		return nil
	}

	output := p.output
	p.output = nil

	return output.Close()
}

// Scheduler can be used to schedule commands to be executed by adding them to
// wr's queue.
type Scheduler struct {
	cwd         string
	exe         string
	jq          jobqueueClient
	sudo        bool
	queue       string
	queuesAvoid string
}

// New returns a Scheduler that is connected to wr manager using the given
// deployment, timeout and logger. Added jobs will have the given cwd, which
// matters. If cwd is blank, the current working dir is used. If queue is not
// blank, that queue will be used during NewJob(). If queuesAvoid is not blank,
// queues including a substring from the list will be avoided during NewJob().
//
// When PretendSubmissions is set, a fake server will be used and no real
// interactions will take place. Methods SubmitJobs, SubmittedJobs, and
// RemoveJobs will all make no changes to any WR state.
func New(settings SchedulerSettings) (*Scheduler, error) {
	cwd, err := pickCWD(settings.Cwd)
	if err != nil {
		return nil, err
	}

	var jq jobqueueClient

	if PretendSubmissions != "" {
		jq = newPretendJobqueue()
	} else if jq, err = jobqueue.ConnectUsingConfig(clog.ContextWithLogHandler(context.Background(),
		settings.Logger.GetHandler()), settings.Deployment, settings.Timeout); err != nil {
		return nil, err
	}

	exe, err := os.Executable()

	return &Scheduler{
		cwd:         cwd,
		exe:         exe,
		queue:       settings.Queue,
		queuesAvoid: settings.QueuesAvoid,
		jq:          jq,
	}, err
}

// DisableSudo is used to disable sudo if it was enabled with EnableSudo.
func (s *Scheduler) DisableSudo() {
	s.sudo = false
}

// EnableSudo causes NewJob() to prefix 'sudo' to commands.
func (s *Scheduler) EnableSudo() {
	s.sudo = true
}

// SubmitJobsAndReturnIDs adds the given jobs to wr's queue and returns their
// stable job keys. Queued duplicate jobs are not an error; their existing keys
// are returned.
func (s *Scheduler) SubmitJobsAndReturnIDs(jobs []*jobqueue.Job,
	opts SubmitJobsOptions) ([]string, error) {
	if err := validateSubmissionJobs(submitJobsAndReturnIDsOp, jobs); err != nil {
		return nil, err
	}

	s.defaultMissingRequirements(jobs)

	return s.jq.AddAndReturnIDs(jobs, opts.envVars(), opts.ignoreComplete())
}

func validateSubmissionJobs(op string, jobs []*jobqueue.Job) error {
	for index, job := range jobs {
		if job != nil {
			continue
		}

		jqErr := jobqueue.Error{
			Op:   op,
			Item: fmt.Sprintf("jobs[%d]", index),
			Err:  jobqueue.ErrBadRequest,
		}

		return fmt.Errorf("%w: job at index %d is nil", jqErr, index)
	}

	return nil
}

// SubmitJobsAndWait adds the given jobs to wr's queue and waits for every
// just-added job to reach a terminal state.
func (s *Scheduler) SubmitJobsAndWait(ctx context.Context, jobs []*jobqueue.Job,
	opts SubmitJobsOptions) ([]*jobqueue.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := validateSubmissionJobs(submitJobsAndWaitOp, jobs); err != nil {
		return nil, err
	}

	s.defaultMissingRequirements(jobs)

	got, err := s.jq.AddAndWait(ctx, jobs, opts.envVars(), opts.ignoreComplete())

	return distinctJobsInKeyOrder(got), err
}

// GetJobByKey returns the job identified by key, optionally including stored
// stdout/stderr and environment data.
func (s *Scheduler) GetJobByKey(key string, getStd bool,
	getEnv bool) (*jobqueue.Job, error) {
	if key == "" {
		return nil, jobqueue.Error{Op: getJobByKeyOp, Err: jobqueue.ErrBadRequest}
	}

	job, err := s.jq.GetByEssence(&jobqueue.JobEssence{JobKey: key}, getStd, getEnv)
	if err != nil {
		var jqErr jobqueue.Error

		ok := errors.As(err, &jqErr)
		if ok && jqErr.Err == jobqueue.ErrBadJob {
			return nil, jobqueue.Error{Op: getJobByKeyOp, Item: key, Err: jobqueue.ErrBadJob}
		}

		return nil, err
	}

	if job == nil {
		return nil, jobqueue.Error{Op: getJobByKeyOp, Item: key, Err: jobqueue.ErrBadJob}
	}

	return job, nil
}

// WaitForRunning waits until the job identified by key has started running or
// has already reached a state that means it will not start in this wait.
func (s *Scheduler) WaitForRunning(ctx context.Context, key string,
	pollInterval time.Duration) (*jobqueue.Job, error) {
	if err := validateWaitForRunningKey(key); err != nil {
		return nil, err
	}

	ticker := time.NewTicker(waitForRunningPollInterval(pollInterval))
	defer ticker.Stop()

	return s.waitForRunning(ctx, key, ticker)
}

func validateWaitForRunningKey(key string) error {
	if key == "" {
		return jobqueue.Error{Op: waitForRunningOp, Err: jobqueue.ErrBadRequest}
	}

	return nil
}

func waitForRunningPollInterval(pollInterval time.Duration) time.Duration {
	if pollInterval <= 0 {
		return waitForRunningDefaultPollInterval
	}

	return pollInterval
}

func (s *Scheduler) waitForRunning(ctx context.Context, key string,
	ticker *time.Ticker) (*jobqueue.Job, error) {
	for ctx.Err() == nil {
		job, done, err := s.pollWaitForRunning(key)
		switch {
		case err != nil:
			return nil, err
		case done:
			return job, nil
		}

		if err = waitForRunningTick(ctx, ticker); err != nil {
			return nil, err
		}
	}

	return nil, ctx.Err()
}

func waitForRunningTick(ctx context.Context, ticker *time.Ticker) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ticker.C:
		return nil
	}
}

func (s *Scheduler) pollWaitForRunning(key string) (*jobqueue.Job, bool, error) {
	job, err := s.GetJobByKey(key, false, false)
	if err != nil {
		return nil, false, waitForRunningError(key, err)
	}

	done := s.returnPretendRunningJob(job) || isWaitForRunningState(job.State)

	return job, done, nil
}

func waitForRunningError(key string, err error) error {
	var jqErr jobqueue.Error

	if !errors.As(err, &jqErr) {
		return err
	}

	switch jqErr.Err {
	case jobqueue.ErrBadRequest:
		return jobqueue.Error{Op: waitForRunningOp, Err: jobqueue.ErrBadRequest}
	case jobqueue.ErrBadJob:
		return jobqueue.Error{Op: waitForRunningOp, Item: key, Err: jobqueue.ErrBadJob}
	default:
		return err
	}
}

func isWaitForRunningState(state jobqueue.JobState) bool {
	switch state {
	case jobqueue.JobStateRunning, jobqueue.JobStateLost, jobqueue.JobStateComplete,
		jobqueue.JobStateBuried, jobqueue.JobStateUnknown:
		return true
	default:
		return false
	}
}

func (s *Scheduler) returnPretendRunningJob(job *jobqueue.Job) bool {
	if _, ok := s.jq.(*pretendJobqueue); !ok {
		return false
	}

	switch job.State {
	case jobqueue.JobStateDelayed, jobqueue.JobStateReady, jobqueue.JobStateReserved:
		job.State = jobqueue.JobStateRunning

		return true
	default:
		return false
	}
}

// WaitForJobs waits for the supplied job keys to reach a terminal state,
// returning complete and buried jobs in de-duplicated key order.
func (s *Scheduler) WaitForJobs(ctx context.Context,
	keys ...string) ([]*jobqueue.Job, error) {
	distinct, err := distinctWaitForJobKeys(keys)
	if err != nil {
		return nil, err
	}

	if len(distinct) == 0 {
		return []*jobqueue.Job{}, nil
	}

	terminal := make(map[string]*jobqueue.Job, len(distinct))

	if err = ctx.Err(); err != nil {
		return jobsInWaitKeyOrder(distinct, terminal),
			waitForJobsContextError(err, distinct, terminal)
	}

	waitKeys, err := s.currentTerminalAndWaitKeys(distinct, terminal)
	if err != nil {
		return nil, err
	}

	if err = s.waitForNonTerminalJobs(ctx, distinct, waitKeys, terminal); err != nil {
		return jobsInWaitKeyOrder(distinct, terminal), err
	}

	return jobsInWaitKeyOrder(distinct, terminal), nil
}

func distinctWaitForJobKeys(keys []string) ([]string, error) {
	seen := make(map[string]struct{}, len(keys))
	distinct := make([]string, 0, len(keys))

	for _, key := range keys {
		if key == "" {
			return nil, jobqueue.Error{Op: waitForJobsOp, Err: jobqueue.ErrBadRequest}
		}

		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}
		distinct = append(distinct, key)
	}

	return distinct, nil
}

func jobsInWaitKeyOrder(keys []string,
	terminal map[string]*jobqueue.Job) []*jobqueue.Job {
	jobs := make([]*jobqueue.Job, 0, len(terminal))

	for _, key := range keys {
		if job, ok := terminal[key]; ok {
			jobs = append(jobs, job)
		}
	}

	return jobs
}

func (s *Scheduler) waitForNonTerminalJobs(ctx context.Context, allKeys []string,
	waitKeys []string, terminal map[string]*jobqueue.Job) error {
	if len(waitKeys) == 0 {
		return nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return waitForJobsContextError(ctxErr, allKeys, terminal)
	}

	if _, ok := s.jq.(*pretendJobqueue); ok {
		return s.completePretendWaitJobs(waitKeys, terminal)
	}

	jq, ok := s.jq.(*jobqueue.Client)
	if !ok {
		return errWaitForJobsJobqueueClient
	}

	return s.waitForSubscribedKeys(ctx, jq, allKeys, waitKeys, terminal)
}

func waitForJobsContextError(ctxErr error, keys []string,
	terminal map[string]*jobqueue.Job) error {
	return fmt.Errorf("%w; unfinished job keys: %s", ctxErr,
		strings.Join(unfinishedWaitForJobKeys(keys, terminal), ", "))
}

func (s *Scheduler) currentTerminalAndWaitKeys(keys []string,
	terminal map[string]*jobqueue.Job) ([]string, error) {
	waitKeys := make([]string, 0, len(keys))

	for _, key := range keys {
		job, err := s.GetJobByKey(key, true, false)
		if err != nil {
			return nil, err
		}

		if isTerminalJobState(job.State) {
			terminal[key] = job

			continue
		}

		waitKeys = append(waitKeys, key)
	}

	return waitKeys, nil
}

func isTerminalJobState(state jobqueue.JobState) bool {
	return state == jobqueue.JobStateComplete || state == jobqueue.JobStateBuried
}

func (s *Scheduler) completePretendWaitJobs(waitKeys []string,
	terminal map[string]*jobqueue.Job) error {
	for _, key := range waitKeys {
		job, err := s.GetJobByKey(key, true, false)
		if err != nil {
			return err
		}

		job.State = jobqueue.JobStateComplete
		job.Exited = true
		job.Exitcode = 0
		job.EndTime = time.Now()
		terminal[key] = job
	}

	return nil
}

func (s *Scheduler) waitForSubscribedKeys(ctx context.Context, jq *jobqueue.Client,
	allKeys []string, waitKeys []string, terminal map[string]*jobqueue.Job) error {
	sub, err := jq.SubscribeToJobKeys(ctx, waitKeys)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return waitForJobsContextError(ctxErr, allKeys, terminal)
		}

		return err
	}
	defer sub.Unsubscribe()

	wanted := waitForJobsKeySet(waitKeys)

	return s.collectSubscribedTerminalJobs(ctx, sub, allKeys, waitKeys, wanted, terminal)
}

func waitForJobsKeySet(keys []string) map[string]struct{} {
	wanted := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		wanted[key] = struct{}{}
	}

	return wanted
}

func (s *Scheduler) collectSubscribedTerminalJobs(ctx context.Context,
	sub *jobqueue.Subscription, allKeys []string, waitKeys []string,
	wanted map[string]struct{}, terminal map[string]*jobqueue.Job) error {
	for !allWaitKeysTerminal(waitKeys, terminal) {
		update, recvErr := receiveWaitForJobsUpdate(ctx, sub)
		if recvErr != nil {
			return waitForJobsReceiveError(ctx, recvErr, allKeys, terminal)
		}

		if !isWantedTerminalUpdate(update, wanted, terminal) {
			continue
		}

		if err := s.recordSubscribedTerminalJob(update, terminal); err != nil {
			return err
		}
	}

	return nil
}

func allWaitKeysTerminal(keys []string, terminal map[string]*jobqueue.Job) bool {
	for _, key := range keys {
		if _, ok := terminal[key]; !ok {
			return false
		}
	}

	return true
}

func receiveWaitForJobsUpdate(ctx context.Context,
	sub *jobqueue.Subscription) (*jobqueue.JobUpdate, error) {
	select {
	case update, ok := <-sub.Updates():
		if !ok {
			if err := sub.Err(); err != nil {
				return nil, err
			}

			return nil, jobqueue.ErrSubscriptionClosed
		}

		return update, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func waitForJobsReceiveError(ctx context.Context, recvErr error, keys []string,
	terminal map[string]*jobqueue.Job) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return waitForJobsContextError(ctxErr, keys, terminal)
	}

	return recvErr
}

func isWantedTerminalUpdate(update *jobqueue.JobUpdate,
	wanted map[string]struct{}, terminal map[string]*jobqueue.Job) bool {
	if update == nil || update.Kind != jobqueue.JobUpdateTerminal {
		return false
	}

	if !isTerminalJobState(update.State) {
		return false
	}

	if _, ok := wanted[update.Key]; !ok {
		return false
	}

	_, alreadyTerminal := terminal[update.Key]

	return !alreadyTerminal
}

func (s *Scheduler) recordSubscribedTerminalJob(update *jobqueue.JobUpdate,
	terminal map[string]*jobqueue.Job) error {
	job, err := s.GetJobByKey(update.Key, true, false)
	if err != nil {
		return err
	}

	terminal[update.Key] = job

	return nil
}

// JobDefaults returns the defaults used when converting a JobViaJSON into a
// Job with this Scheduler.
func (s *Scheduler) JobDefaults() *jobqueue.JobDefaults {
	return &jobqueue.JobDefaults{
		Cwd:                  s.cwd,
		SchedulerQueue:       s.queue,
		SchedulerQueuesAvoid: s.queuesAvoid,
		CPUs:                 reqCores,
		Memory:               reqRAM,
		Time:                 reqTime,
		Disk:                 reqDisk,
		Retries:              int(jobRetries),
		Override:             0,
		CwdMatters:           true,
		DiskSet:              true,
	}
}

// NewJobFromJSON converts a JobViaJSON into a Job using this Scheduler's
// defaults.
func (s *Scheduler) NewJobFromJSON(spec *jobqueue.JobViaJSON) (*jobqueue.Job, error) {
	if spec == nil {
		return nil, jobqueue.Error{Op: newJobFromJSONOp, Err: jobqueue.ErrBadRequest}
	}

	return spec.Convert(s.JobDefaults())
}

// GetSchedulerAlerts returns the scheduler alerts currently shown by wr's web
// UI, including dismissible scheduler issues and bad cloud servers.
func (s *Scheduler) GetSchedulerAlerts() (*jobqueue.SchedulerAlerts, error) {
	return s.jq.GetSchedulerAlerts()
}

func (s *Scheduler) defaultMissingRequirements(jobs []*jobqueue.Job) {
	for _, job := range jobs {
		if job == nil {
			continue
		}

		job.Lock()
		if job.Requirements == nil {
			job.Requirements, job.Override = s.determineOverrideAndReq(nil)
		}
		job.Unlock()
	}
}

func unfinishedWaitForJobKeys(keys []string,
	terminal map[string]*jobqueue.Job) []string {
	unfinished := make([]string, 0, len(keys))

	for _, key := range keys {
		if _, ok := terminal[key]; !ok {
			unfinished = append(unfinished, key)
		}
	}

	return unfinished
}

// pickCWD checks the given directory exists, returns an error. If the given
// dir is blank, returns the current working directory.
func pickCWD(cwd string) (string, error) {
	if cwd == "" {
		return os.Getwd()
	}

	_, err := os.Stat(cwd)

	return cwd, err
}

// Executable is a convenience function that returns the same as
// os.Executable(), but without the error.
func (s *Scheduler) Executable() string {
	if s.exe == "" {
		exe, err := os.Executable()
		if err == nil {
			s.exe = exe
		}
	}

	return s.exe
}

// DefaultRequirements returns a minimal set of requirments, which is what
// NewJob() will use by default.
func DefaultRequirements() *jqs.Requirements {
	return &jqs.Requirements{
		RAM:   reqRAM,
		Time:  reqTime,
		Cores: reqCores,
		Disk:  reqDisk,
	}
}

// NewJob is a convenience function for creating Jobs. It sets the job's Cwd to
// the current working directory, sets CwdMatters to true, applies the given
// Requirements, and sets Retries to 3.
//
// If this Scheduler had been made with sudo: true, cmd will be prefixed with
// 'sudo '.
//
// NB: When running with sudo that is configured to not pass through
// environmental variables, you must have a wr config file, accessible from the
// working directory, with ManagerHost, ManagerPort, and ManagerCertDomain set.
//
// The supplied depGroup and dep can be blank to not set DepGroups and
// Dependencies.
//
// If req is supplied, sets the job override to 1. Otherwise, req will default
// to a minimal set of requirements, and override will be 0. If this Scheduler
// had been made with a queue override, the requirements will be altered to add
// that queue.
func (s *Scheduler) NewJob(cmd, repGroup, reqGroup, depGroup, dep string, req *jqs.Requirements) *jobqueue.Job {
	if s.sudo {
		cmd = "sudo " + cmd
	}

	req, override := s.determineOverrideAndReq(req)

	return &jobqueue.Job{
		Cmd:          cmd,
		Cwd:          s.cwd,
		CwdMatters:   true,
		RepGroup:     repGroup,
		ReqGroup:     reqGroup,
		Requirements: req,
		DepGroups:    createDepGroups(depGroup),
		Dependencies: createDependencies(dep),
		Retries:      jobRetries,
		Override:     override,
	}
}

// createDepGroups returns the given depGroup inside a string slice, unless
// blank, in which case returns nil slice.
func createDepGroups(depGroup string) []string {
	var depGroups []string
	if depGroup != "" {
		depGroups = []string{depGroup}
	}

	return depGroups
}

// createDependencies returns the given dep as a Dependencies if not blank,
// otherwise nil.
func createDependencies(dep string) jobqueue.Dependencies {
	var dependencies jobqueue.Dependencies
	if dep != "" {
		dependencies = jobqueue.Dependencies{{DepGroup: dep}}
	}

	return dependencies
}

// determineOverrideAndReq returns the given req and an override of 1 if req is
// not nil, otherwise returns a default req and override of 0.
func (s *Scheduler) determineOverrideAndReq(req *jqs.Requirements) (*jqs.Requirements, uint8) {
	override := uint8(1)

	if req == nil {
		req = DefaultRequirements()
		override = 0
	}

	if s.queue != "" {
		other := req.Other
		if other == nil {
			other = make(map[string]string)
		}

		other["scheduler_queue"] = s.queue
		req.Other = other
	}

	if s.queuesAvoid != "" {
		other := req.Other
		if other == nil {
			other = make(map[string]string)
		}

		other["scheduler_queues_avoid"] = s.queuesAvoid
		req.Other = other
	}

	return req, override
}

// SubmitJobs adds the given jobs to wr's queue, passing through current
// environment variables.
//
// Previously added identical jobs that have since been archived will get added
// again.
//
// If any duplicate jobs were added, an error will be returned.
//
// If this scheduler was created with PretendSubmissions set none of the above
// happens; the jobs are merely recorded for later retrieval with
// SubmittedJobs().
func (s *Scheduler) SubmitJobs(jobs []*jobqueue.Job) error {
	if err := validateSubmissionJobs(submitJobsOp, jobs); err != nil {
		return err
	}

	s.defaultMissingRequirements(jobs)

	inserts, _, err := s.jq.Add(jobs, os.Environ(), false)
	if err != nil {
		return err
	}

	if inserts != len(jobs) {
		return ErrDuplicateJobs
	}

	return nil
}

// SubmittedJobs returns jobs sent to SubmitJobs() if this Scheduler was created
// with PretendSubmissions unset.
func (s *Scheduler) SubmittedJobs() []*jobqueue.Job {
	pjq, ok := s.jq.(*pretendJobqueue)
	if !ok {
		return nil
	}

	return pjq.SubmittedJobs()
}

// FindJobsByRepGroupSuffix finds all of the jobs in wr whose rep group has the
// supplied suffix.
func (s *Scheduler) FindJobsByRepGroupSuffix(suffix string) ([]*jobqueue.Job, error) {
	return s.jq.GetByRepGroupMatch(suffix, jobqueue.RepGroupMatchSuffix, 0, "",
		true, false)
}

// FindJobsByRepGroupPrefixAndState finds all jobs in wr whose RepGroup starts
// with the supplied prefix, optionally limited to the supplied state.
func (s *Scheduler) FindJobsByRepGroupPrefixAndState(prefix string, state jobqueue.JobState) ([]*jobqueue.Job, error) {
	return s.jq.GetByRepGroupMatch(prefix, jobqueue.RepGroupMatchPrefix, 0,
		state, true, false)
}

// FindIncompleteJobsByRepGroup finds incomplete jobs in wr whose RepGroup
// matches the supplied value according to match.
//
// Unlike FindJobsByRepGroupPrefixAndState(), this method does not request
// stdout, stderr, or env from the server.
func (s *Scheduler) FindIncompleteJobsByRepGroup(repgroup string,
	match jobqueue.RepGroupMatch) ([]*jobqueue.Job, error) {
	return s.jq.GetIncompleteByRepGroupMatch(repgroup, match, 0, "", false,
		false)
}

// FindIncompleteJobsByRepGroupAndState finds incomplete jobs in wr whose
// RepGroup matches the supplied value according to match, optionally limited to
// the supplied state.
//
// Unlike FindJobsByRepGroupPrefixAndState(), this method does not request
// stdout, stderr, or env from the server.
func (s *Scheduler) FindIncompleteJobsByRepGroupAndState(repgroup string,
	match jobqueue.RepGroupMatch,
	state jobqueue.JobState) ([]*jobqueue.Job, error) {
	return s.jq.GetIncompleteByRepGroupMatch(repgroup, match, 0, state, false,
		false)
}

// GetLastCompletionTimeByRepGroup finds the latest completion time among
// complete jobs in each RepGroup that matches repgroup according to match.
func (s *Scheduler) GetLastCompletionTimeByRepGroup(repgroup string,
	match jobqueue.RepGroupMatch) (map[string]time.Time, error) {
	return s.jq.GetLastCompletionTimeByRepGroup(repgroup, match)
}

// Kill asks the server to kill the provided jobs.
func (s *Scheduler) KillJobs(jobs ...*jobqueue.Job) error {
	jq, ok := s.jq.(*jobqueue.Client)
	if !ok {
		return nil
	}

	_, err := jq.Kill(jobsToEssences(jobs))

	return err
}

func jobsToEssences(jobs []*jobqueue.Job) []*jobqueue.JobEssence {
	es := make([]*jobqueue.JobEssence, len(jobs))

	for n, job := range jobs {
		es[n] = &jobqueue.JobEssence{JobKey: job.Key()}
	}

	return es
}

// RemoveJobs removes all of the supplied jobs from the wr queues.
//
// NB: Running jobs will not be removed.
func (s *Scheduler) RemoveJobs(jobs ...*jobqueue.Job) error {
	_, err := s.jq.Delete(jobsToEssences(jobs))

	return err
}

// Disconnect disconnects from the manager. You should defer this after New().
func (s *Scheduler) Disconnect() error {
	return s.jq.Disconnect()
}

// UniqueString returns a unique string that could be useful for supplying as
// depGroup values to NewJob() etc. The length is always 20 characters.
func UniqueString() string {
	return xid.New().String()
}
