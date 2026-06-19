/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
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

package client_test

import (
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/client"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

func compatNewScheduler(settings client.SchedulerSettings) (*client.Scheduler, error) {
	return client.New(settings)
}

func compatWrstatSchedulerCalls(s *client.Scheduler, jobs []*jobqueue.Job) {
	req := client.DefaultRequirements()
	unique := client.UniqueString()

	walkJob := s.NewJob(
		s.Executable()+" walk",
		"wrstat-walk-"+unique,
		"wrstat-walk",
		unique,
		"",
		req,
	)
	combineJob := s.NewJob(
		s.Executable()+" combine",
		"wrstat-combine-"+unique,
		"wrstat-combine",
		"",
		unique,
		req,
	)
	jobs = append(jobs, walkJob, combineJob)

	submitErr := s.SubmitJobs(jobs)
	submittedJobs := s.SubmittedJobs()
	suffixJobs, suffixErr := s.FindJobsByRepGroupSuffix("-" + unique)
	prefixJobs, prefixErr := s.FindJobsByRepGroupPrefixAndState("wrstat-", jobqueue.JobStateDependent)
	killErr := s.KillJobs(jobs...)
	removeErr := s.RemoveJobs(jobs...)
	disconnectErr := s.Disconnect()

	_ = submitErr
	_ = submittedJobs
	_ = suffixJobs
	_ = suffixErr
	_ = prefixJobs
	_ = prefixErr
	_ = killErr
	_ = removeErr
	_ = disconnectErr
}

func compatWrstatUICalls(s *client.Scheduler, inputDir string) {
	req := client.DefaultRequirements()
	job := s.NewJob(
		s.Executable()+" summarise "+inputDir,
		"wrstat-ui-summarise",
		"wrstat-ui-summarise",
		"",
		"",
		req,
	)
	jobs := []*jobqueue.Job{job}

	submitErr := s.SubmitJobs(jobs)
	disconnectErr := s.Disconnect()

	_ = submitErr
	_ = disconnectErr
}

type ibackupJobSubmitter interface {
	NewJob(cmd, repGroup, reqGroup, depGroup, dep string, req *scheduler.Requirements) *jobqueue.Job
	SubmitJobs(jobs []*jobqueue.Job) error
	FindIncompleteJobsByRepGroup(repgroup string, match jobqueue.RepGroupMatch) ([]*jobqueue.Job, error)
	GetLastCompletionTimeByRepGroup(repgroup string, match jobqueue.RepGroupMatch) (map[string]time.Time, error)
	RemoveJobs(jobs ...*jobqueue.Job) error
	Disconnect() error
}

func TestSchedulerCompatibility(t *testing.T) {
	t.Parallel()

	var _ ibackupJobSubmitter = (*client.Scheduler)(nil)

	_ = compatNewScheduler
	_ = compatWrstatSchedulerCalls
	_ = compatWrstatUICalls
}
