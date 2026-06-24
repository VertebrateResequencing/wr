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

package jobqueue

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestJobqueueSuspendResume(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	start := func(keepDB bool) (*Server, *Client) {
		serverConfig.dontWipeDevDB = keepDB
		server, _, token, errs := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false

		So(errs, ShouldBeNil)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		return server, jq
	}

	newJob := func(cmd, repGroup string) *Job {
		return &Job{
			Cmd:          cmd,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Retries:      uint8(1),
			RepGroup:     repGroup,
		}
	}

	addJobs := func(jq *Client, jobs ...*Job) {
		inserts, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, len(jobs))
		So(already, ShouldEqual, 0)
	}

	getJob := func(jq *Client, job *Job) *Job {
		got, err := jq.GetByEssence(job.ToEssense(), false, false)
		So(err, ShouldBeNil)
		So(got, ShouldNotBeNil)

		return got
	}

	reserveJob := func(jq *Client, job *Job) *Job {
		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())

		return reserved
	}

	releaseAsDelayed := func(jq *Client, job *Job) {
		reserved := reserveJob(jq, job)
		So(jq.Release(reserved, nil, "delay for suspend test"), ShouldBeNil)
		So(getJob(jq, job).State, ShouldEqual, JobStateDelayed)
	}

	Convey("Suspending and resuming a ready job persists state and controls reservation", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		job := newJob("echo suspend ready b1", "suspend-ready-b1")
		addJobs(jq, job)

		suspended, err := jq.Suspend([]*JobEssence{job.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(getJob(jq, job).State, ShouldEqual, JobStateSuspended)

		summaries, err := jq.GetStatusByRepGroupMatch(job.RepGroup, RepGroupMatchExact,
			[]JobState{JobStateSuspended}, false, false)
		So(err, ShouldBeNil)
		So(summaries[job.RepGroup].Counts[JobStateSuspended], ShouldEqual, 1)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)

		resumed, err := jq.Resume([]*JobEssence{job.ToEssense()})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(getJob(jq, job).State, ShouldEqual, JobStateReady)

		reserved, err = jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())
	})

	Convey("Suspending ignores running, buried, and complete jobs", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		runningJob := newJob("echo suspend running b1", "suspend-ineligible-b1")
		buriedJob := newJob("echo suspend buried b1", "suspend-ineligible-b1")
		completeJob := newJob("echo suspend complete b1", "suspend-ineligible-b1")
		addJobs(jq, runningJob, buriedJob, completeJob)

		runningReserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(runningReserved, ShouldNotBeNil)
		So(runningReserved.Key(), ShouldEqual, runningJob.Key())
		So(jq.Started(runningReserved, os.Getpid()), ShouldBeNil)

		buriedReserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(buriedReserved, ShouldNotBeNil)
		So(buriedReserved.Key(), ShouldEqual, buriedJob.Key())
		So(jq.Bury(buriedReserved, &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()},
			"test bury"), ShouldBeNil)

		completeReserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(completeReserved, ShouldNotBeNil)
		So(completeReserved.Key(), ShouldEqual, completeJob.Key())
		So(jq.Started(completeReserved, os.Getpid()), ShouldBeNil)
		So(jq.Archive(completeReserved, &JobEndState{
			Exited: true, Exitcode: 0, EndTime: time.Now(),
		}), ShouldBeNil)

		suspended, err := jq.Suspend([]*JobEssence{
			runningJob.ToEssense(),
			buriedJob.ToEssense(),
			completeJob.ToEssense(),
		})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 0)
		So(getJob(jq, runningJob).State, ShouldEqual, JobStateRunning)
		So(getJob(jq, buriedJob).State, ShouldEqual, JobStateBuried)
		So(getJob(jq, completeJob).State, ShouldEqual, JobStateComplete)
	})

	Convey("Suspended ready jobs recover as suspended after restart", t, func() {
		server, jq := start(false)
		defer func() {
			disconnect(jq)
			server.Stop(ctx, true)
		}()

		job := newJob("echo suspend restart b1", "suspend-restart-b1")
		addJobs(jq, job)

		suspended, err := jq.Suspend([]*JobEssence{job.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(getJob(jq, job).State, ShouldEqual, JobStateSuspended)

		disconnect(jq)
		server.Stop(ctx, true)
		server, jq = start(true)

		So(getJob(jq, job).State, ShouldEqual, JobStateSuspended)
		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)

		resumed, err := jq.Resume([]*JobEssence{job.ToEssense()})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)

		reserved, err = jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())
	})

	Convey("Suspended dependent jobs recover and resume after dependencies complete", t, func() {
		server, jq := start(false)
		defer func() {
			disconnect(jq)
			server.Stop(ctx, true)
		}()

		parent := newJob("echo suspend parent b1", "suspend-dependent-b1")
		child := newJob("echo suspend child b1", "suspend-dependent-b1")
		child.Dependencies = Dependencies{NewEssenceDependency(parent.Cmd, "")}
		addJobs(jq, parent, child)
		So(getJob(jq, child).State, ShouldEqual, JobStateDependent)

		suspended, err := jq.Suspend([]*JobEssence{child.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(getJob(jq, child).State, ShouldEqual, JobStateSuspended)

		disconnect(jq)
		server.Stop(ctx, true)
		server, jq = start(true)

		So(getJob(jq, child).State, ShouldEqual, JobStateSuspended)

		parentReserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(parentReserved, ShouldNotBeNil)
		So(parentReserved.Key(), ShouldEqual, parent.Key())
		So(jq.Execute(ctx, parentReserved, config.RunnerExecShell), ShouldBeNil)

		resumed, err := jq.Resume([]*JobEssence{child.ToEssense()})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(getJob(jq, child).State, ShouldEqual, JobStateReady)
	})

	Convey("Client suspend handles delayed, dependent, and ready jobs together", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		delayed := newJob("echo suspend delayed b2", "suspend-mixed-b2")
		parent := newJob("echo suspend parent b2", "suspend-mixed-b2")
		dependent := newJob("echo suspend dependent b2", "suspend-mixed-b2")
		dependent.Dependencies = Dependencies{NewEssenceDependency(parent.Cmd, "")}
		ready := newJob("echo suspend ready b2", "suspend-mixed-b2")
		addJobs(jq, delayed, parent, dependent, ready)
		releaseAsDelayed(jq, delayed)

		suspended, err := jq.Suspend([]*JobEssence{
			delayed.ToEssense(),
			dependent.ToEssense(),
			ready.ToEssense(),
		})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 3)
		So(getJob(jq, delayed).State, ShouldEqual, JobStateSuspended)
		So(getJob(jq, dependent).State, ShouldEqual, JobStateSuspended)
		So(getJob(jq, ready).State, ShouldEqual, JobStateSuspended)
	})

	Convey("Client suspend counts only eligible jobs in mixed ready and running input", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		running := newJob("echo suspend running b2", "suspend-ready-running-b2")
		readyA := newJob("echo suspend ready a b2", "suspend-ready-running-b2")
		readyB := newJob("echo suspend ready b b2", "suspend-ready-running-b2")
		readyC := newJob("echo suspend ready c b2", "suspend-ready-running-b2")
		addJobs(jq, running, readyA, readyB, readyC)

		runningReserved := reserveJob(jq, running)
		So(jq.Started(runningReserved, os.Getpid()), ShouldBeNil)

		suspended, err := jq.Suspend([]*JobEssence{
			running.ToEssense(),
			readyA.ToEssense(),
			readyB.ToEssense(),
			readyC.ToEssense(),
		})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 3)
		So(getJob(jq, running).State, ShouldEqual, JobStateRunning)
		So(getJob(jq, readyA).State, ShouldEqual, JobStateSuspended)
		So(getJob(jq, readyB).State, ShouldEqual, JobStateSuspended)
		So(getJob(jq, readyC).State, ShouldEqual, JobStateSuspended)
	})

	Convey("Client suspend ignores reserved and lost jobs", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		reservedJob := newJob("echo suspend reserved b2", "suspend-reserved-lost-b2")
		lostJob := newJob("echo suspend lost b2", "suspend-reserved-lost-b2")
		addJobs(jq, reservedJob, lostJob)

		reserved := reserveJob(jq, reservedJob)
		lost := reserveJob(jq, lostJob)

		item, err := server.q.Get(lost.Key())
		So(err, ShouldBeNil)

		lostServerJob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)
		lostServerJob.Lock()
		lostServerJob.State = JobStateRunning
		lostServerJob.Lost = true
		lostServerJob.StartTime = time.Now()
		lostServerJob.Unlock()

		suspended, err := jq.Suspend([]*JobEssence{reserved.ToEssense(), lost.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 0)
		So(getJob(jq, reservedJob).State, ShouldEqual, JobStateReserved)
		So(getJob(jq, lostJob).State, ShouldEqual, JobStateLost)
	})

	Convey("Client resume counts only suspended jobs and restores dependency-aware states", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		parent := newJob("echo resume parent b2", "resume-mixed-b2")
		readyFromSuspend := newJob("echo resume ready b2", "resume-mixed-b2")
		dependentFromSuspend := newJob("echo resume dependent b2", "resume-mixed-b2")
		dependentFromSuspend.Dependencies = Dependencies{NewEssenceDependency(parent.Cmd, "")}
		alreadyReady := newJob("echo resume already ready b2", "resume-mixed-b2")
		addJobs(jq, parent, readyFromSuspend, dependentFromSuspend, alreadyReady)

		suspended, err := jq.Suspend([]*JobEssence{
			readyFromSuspend.ToEssense(),
			dependentFromSuspend.ToEssense(),
		})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 2)

		resumed, err := jq.Resume([]*JobEssence{
			readyFromSuspend.ToEssense(),
			dependentFromSuspend.ToEssense(),
			alreadyReady.ToEssense(),
		})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 2)
		So(getJob(jq, readyFromSuspend).State, ShouldEqual, JobStateReady)
		So(getJob(jq, dependentFromSuspend).State, ShouldEqual, JobStateDependent)
		So(getJob(jq, alreadyReady).State, ShouldEqual, JobStateReady)
	})

	Convey("Client resume makes an expired delayed suspended job ready", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		delayed := newJob("echo resume delayed ready b2", "resume-delayed-ready-b2")
		addJobs(jq, delayed)
		releaseAsDelayed(jq, delayed)

		suspended, err := jq.Suspend([]*JobEssence{delayed.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		<-time.After(serverConfig.Timings.ReleaseDelayMin + 50*time.Millisecond)

		resumed, err := jq.Resume([]*JobEssence{delayed.ToEssense()})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(getJob(jq, delayed).State, ShouldEqual, JobStateReady)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, delayed.Key())
	})

	Convey("Client resume keeps an expired delayed suspended job dependent after dependency modification", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		delayed := newJob("echo resume delayed dependent b2", "resume-delayed-dependent-b2")
		parent := newJob("echo resume delayed parent b2", "resume-delayed-dependent-b2")
		addJobs(jq, delayed, parent)
		releaseAsDelayed(jq, delayed)
		parentReserved := reserveJob(jq, parent)

		suspended, err := jq.Suspend([]*JobEssence{delayed.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		<-time.After(serverConfig.Timings.ReleaseDelayMin + 50*time.Millisecond)

		modifier := NewJobModifer()
		modifier.SetDependencies(Dependencies{NewEssenceDependency(parent.Cmd, "")})
		modified, err := jq.Modify([]*JobEssence{delayed.ToEssense()}, modifier)
		So(err, ShouldBeNil)
		So(modified[delayed.Key()], ShouldEqual, delayed.Key())

		resumed, err := jq.Resume([]*JobEssence{delayed.ToEssense()})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(getJob(jq, delayed).State, ShouldEqual, JobStateDependent)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)
		touch(jq, parentReserved)
	})

	Convey("Client suspend and resume handle empty and partially missing input", t, func() {
		server, jq := start(false)
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		suspended, err := jq.Suspend([]*JobEssence{})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 0)

		resumed, err := jq.Resume([]*JobEssence{})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 0)

		eligible := newJob("echo suspend missing eligible b2", "suspend-missing-b2")
		addJobs(jq, eligible)

		suspended, err = jq.Suspend([]*JobEssence{
			{JobKey: "missing-suspend-key"},
			eligible.ToEssense(),
		})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(getJob(jq, eligible).State, ShouldEqual, JobStateSuspended)
	})
}

func TestJobqueueSuspendResumeLimitGroups(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	baseGroup := "110:30:1:0"

	start := func() (*Server, *Client) {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		server.rc = serverRC

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		return server, jq
	}

	newJob := func(cmd, repGroup string, limitGroups []string) *Job {
		return &Job{
			Cmd:          cmd,
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			Override:     uint8(2),
			Retries:      uint8(1),
			RepGroup:     repGroup,
			LimitGroups:  limitGroups,
		}
	}

	addJobs := func(jq *Client, jobs ...*Job) {
		inserts, already, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, len(jobs))
		So(already, ShouldEqual, 0)
	}

	getJob := func(jq *Client, job *Job) *Job {
		got, err := jq.GetByEssence(job.ToEssense(), false, false)
		So(err, ShouldBeNil)
		So(got, ShouldNotBeNil)

		return got
	}

	serverJob := func(server *Server, job *Job) *Job {
		item, err := server.q.Get(job.Key())
		So(err, ShouldBeNil)

		got, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		return got
	}

	waitForSchedulerGroup := func(server *Server, job *Job, want string) {
		So(pollUntil(func() bool {
			return serverJob(server, job).getSchedulerGroup() == want
		}), ShouldBeTrue)
	}

	scheduledCounts := func(server *Server, groupName string) (int, int, bool) {
		server.psgmutex.RLock()
		group, ok := server.previouslyScheduledGroups[groupName]
		server.psgmutex.RUnlock()

		if !ok {
			return 0, 0, false
		}

		group.RLock()
		defer group.RUnlock()

		return group.count, group.skipped, true
	}

	Convey("Suspended jobs preserve limit-group metadata but are not reservable", t, func() {
		server, jq := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		limitGroup := "lg-suspend"
		schedulerGroup := baseGroup + jobSchedLimitGroupSeparator + limitGroup
		toSuspend := newJob("echo limit suspend a", "limit-suspend-b3", []string{limitGroup + ":1"})
		toRun := newJob("echo limit suspend b", "limit-suspend-b3", []string{limitGroup + ":1"})
		addJobs(jq, toSuspend, toRun)
		waitForSchedulerGroup(server, toSuspend, schedulerGroup)
		waitForSchedulerGroup(server, toRun, schedulerGroup)

		suspended, err := jq.Suspend([]*JobEssence{toSuspend.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		limits, err := jq.GetLimitGroups()
		So(err, ShouldBeNil)
		So(limits[limitGroup], ShouldEqual, 1)
		So(getJob(jq, toSuspend).LimitGroups, ShouldResemble, []string{limitGroup})
		So(strings.HasSuffix(serverJob(server, toSuspend).getSchedulerGroup(),
			jobSchedLimitGroupSeparator+limitGroup), ShouldBeTrue)

		reserved, err := jq.ReserveScheduled(2*time.Second, schedulerGroup)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, toRun.Key())

		reserved, err = jq.ReserveScheduled(50*time.Millisecond, schedulerGroup)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)
	})

	Convey("Resumed limit-group jobs wait for current running capacity", t, func() {
		server, jq := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		limitGroup := "lg-resume"
		schedulerGroup := baseGroup + jobSchedLimitGroupSeparator + limitGroup
		running := newJob("echo limit resume running", "limit-resume-b3", []string{limitGroup + ":1"})
		suspendedJob := newJob("echo limit resume suspended", "limit-resume-b3", []string{limitGroup + ":1"})
		addJobs(jq, running, suspendedJob)
		waitForSchedulerGroup(server, running, schedulerGroup)
		waitForSchedulerGroup(server, suspendedJob, schedulerGroup)

		runningReserved, err := jq.ReserveScheduled(2*time.Second, schedulerGroup)
		So(err, ShouldBeNil)
		So(runningReserved, ShouldNotBeNil)
		So(runningReserved.Key(), ShouldEqual, running.Key())
		So(jq.Started(runningReserved, os.Getpid()), ShouldBeNil)

		suspended, err := jq.Suspend([]*JobEssence{suspendedJob.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		resumed, err := jq.Resume([]*JobEssence{suspendedJob.ToEssense()})
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 1)
		So(pollUntil(func() bool {
			count, skipped, ok := scheduledCounts(server, schedulerGroup)

			return ok && count == 1 && skipped == 1
		}), ShouldBeTrue)

		reserved, err := jq.ReserveScheduled(50*time.Millisecond, schedulerGroup)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)

		So(jq.Archive(runningReserved, &JobEndState{
			Exited: true, Exitcode: 0, EndTime: time.Now(),
		}), ShouldBeNil)

		reserved, err = jq.ReserveScheduled(2*time.Second, schedulerGroup)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, suspendedJob.Key())
	})

	Convey("Suspended delayed jobs do not re-enter scheduler counts when their original delay elapses", t, func() {
		server, jq := start()
		defer server.Stop(ctx, true)
		defer disconnect(jq)

		delayed := newJob("echo delayed scheduler suspend", "limit-delayed-b3", nil)
		addJobs(jq, delayed)
		waitForSchedulerGroup(server, delayed, baseGroup)

		reserved, err := jq.ReserveScheduled(2*time.Second, baseGroup)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, delayed.Key())
		So(jq.Release(reserved, nil, "delay before suspend"), ShouldBeNil)
		So(getJob(jq, delayed).State, ShouldEqual, JobStateDelayed)

		suspended, err := jq.Suspend([]*JobEssence{delayed.ToEssense()})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		<-time.After(serverConfig.Timings.ReleaseDelayMin + 50*time.Millisecond)

		count, _, ok := scheduledCounts(server, baseGroup)
		So(ok, ShouldBeTrue)
		So(count, ShouldEqual, 0)

		reserved, err = jq.ReserveScheduled(50*time.Millisecond, baseGroup)
		So(err, ShouldBeNil)
		So(reserved, ShouldBeNil)
		So(getJob(jq, delayed).State, ShouldEqual, JobStateSuspended)
	})
}
