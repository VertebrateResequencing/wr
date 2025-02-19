/*******************************************************************************
 * Copyright (c) 2021, 2025 Genome Research Ltd.
 *
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
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15"
	"github.com/rs/xid"
	"github.com/wtsi-ssg/wr/clog"
)

type Error string

func (e Error) Error() string { return string(e) }

const errDupJobs = Error("some of the added jobs were duplicates")

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

// some consts for the jobs returned by NewJob().
const (
	jobRetries uint8 = 30
	reqRAM           = 100
	reqTime          = 10 * time.Second
	reqCores         = 1
	reqDisk          = 1
)

type SchedulerSettings struct {
	Deployment  string
	Cwd         string
	Queue       string
	QueuesAvoid string
	Timeout     time.Duration
	Logger      log15.Logger
}

type jobqueueClient interface {
	Add(jobs []*jobqueue.Job, envVars []string, ignoreComplete bool) (added int, existed int, err error)
	GetByRepGroup(repgroup string, subStr bool, limit int,
		state jobqueue.JobState, getStd bool, getEnv bool) ([]*jobqueue.Job, error)
	Delete(jes []*jobqueue.JobEssence) (int, error)
	Disconnect() error
}

type pretendJobqueue struct {
	jobBuffer []*jobqueue.Job
	output    io.Writer
}

func newPretendJobqueue() *pretendJobqueue {
	var w io.Writer

	fd, errr := strconv.ParseUint(PretendSubmissions, 10, 64)
	if errr == nil {
		w = os.NewFile(uintptr(fd), "")
	}

	return &pretendJobqueue{output: w}
}

func (p *pretendJobqueue) Add(jobs []*jobqueue.Job, _ []string, _ bool) (int, int, error) {
	p.jobBuffer = append(p.jobBuffer, jobs...)

	if p.output != nil {
		json.NewEncoder(p.output).Encode(jobs) //nolint:errcheck,errchkjson
	}

	return len(jobs), 0, nil
}

func (p *pretendJobqueue) SubmittedJobs() []*jobqueue.Job {
	sj := p.jobBuffer

	return sj
}

// GetByRepGroup behaves like jobqueue.GetByRepGroup, but only repgroup is
// considered (as a substring).
func (p *pretendJobqueue) GetByRepGroup(repgroup string, _ bool, _ int,
	_ jobqueue.JobState, _ bool, _ bool) ([]*jobqueue.Job, error) {
	var jobs []*jobqueue.Job

	for _, job := range p.jobBuffer {
		if strings.Contains(job.RepGroup, repgroup) {
			jobs = append(jobs, job)
		}
	}

	return jobs, nil
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
	return nil
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

// NewJob is a convenience function for creating Jobs. It sets the job's Cwd
// to the current working directory, sets CwdMatters to true, applies the given
// Requirements, and sets Retries to 3.
//
// If this Scheduler had been made with sudo: true, cmd will be prefixed with
// 'sudo '.
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
	inserts, _, err := s.jq.Add(jobs, os.Environ(), false)
	if err != nil {
		return err
	}

	if inserts != len(jobs) {
		return errDupJobs
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
	jobs, err := s.jq.GetByRepGroup(suffix, true, 0, "", true, false)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(jobs, func(job *jobqueue.Job) bool {
		return !strings.HasSuffix(job.RepGroup, suffix)
	}), nil
}

// RemoveJobs removes all of the supplied jobs from the wr queues.
//
// NB: Running jobs will not be removed.
func (s *Scheduler) RemoveJobs(jobs ...*jobqueue.Job) error {
	es := make([]*jobqueue.JobEssence, len(jobs))

	for n, job := range jobs {
		es[n] = &jobqueue.JobEssence{JobKey: job.Key()}
	}

	_, err := s.jq.Delete(es)

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
