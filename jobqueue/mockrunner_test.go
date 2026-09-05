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

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// fakeRunnerSchedGrp extracts the scheduler group from the command the server
// templated for a runner. The tests set ServerConfig.RunnerCmd to a template
// whose first %s (i.e. the second whitespace field) is the scheduler group.
func fakeRunnerSchedGrp(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}

	return fields[1]
}

// mockRunnerCmd is the RunnerCmd template to use with the mock scheduler: the
// server fills in (group, deployment, addr, domain, rtimeout, maxmins) and the
// fake runner only cares about the group.
const mockRunnerCmd = "fakerunner %s %s %s %s %d %d"

// schedulerNameMock is the SchedulerName that selects the in-process mock
// scheduler in tests.
const schedulerNameMock = "mock"

// fakeReqGroup is the ReqGroup used by mock-runner test jobs.
const fakeReqGroup = "fake"

// newFakeRunnerFunc returns a ConfigMock.RunnerFunc that acts like a wr runner
// without spawning a subprocess or executing any real job command: it connects
// to the server, reserves jobs for its scheduler group, and drives each job
// through its lifecycle using the public client API. The behaviour for each job
// is decided by behave(job), which returns how the simulated execution should
// end. This exercises all of the server-side scheduling and job-state logic
// while doing no real work.
func newFakeRunnerFunc(config interface{ caFile() string }, addr, domain string, tokenFile string,
	behave func(job *Job) fakeOutcome) func(ctx context.Context, cmd string) {
	return func(ctx context.Context, cmd string) {
		grp := fakeRunnerSchedGrp(cmd)
		if grp == "" {
			return
		}

		token, err := os.ReadFile(tokenFile)
		if err != nil {
			return
		}

		jq, err := Connect(addr, config.caFile(), domain, token, 10*time.Second)
		if err != nil {
			return
		}
		defer disconnect(jq)

		for {
			job, errr := jq.ReserveScheduled(time.Second, grp)
			if errr != nil || job == nil {
				return
			}

			if err := runFakeJob(jq, job, behave(job)); err != nil {
				return
			}
		}
	}
}

// fakeOutcome describes how a fake runner should simulate the execution of a
// job: hold it "running" for hold (releasing early if the job is killed), then
// end it as complete, or failed (which the server will retry or bury).
type fakeOutcome struct {
	hold   time.Duration
	fail   bool
	signal bool // simulate the job being killed (like a real runner getting FailReasonSignal)
}

func runFakeJob(jq *Client, job *Job, outcome fakeOutcome) error {
	if err := jq.Started(job, os.Getpid()); err != nil {
		return err
	}

	killed := holdFakeJob(jq, job, outcome.hold)

	jes := &JobEndState{Exited: true, EndTime: time.Now()}

	switch {
	case killed || outcome.signal:
		jes.Exitcode = -1

		return jq.Bury(job, jes, FailReasonSignal)
	case outcome.fail:
		jes.Exitcode = 1

		return jq.Release(job, jes, FailReasonExit)
	default:
		jes.Exitcode = 0

		return jq.Archive(job, jes)
	}
}

// holdFakeJob keeps the job "running" for the given duration by touching it,
// returning early (true) if the server tells us the job has been killed.
func holdFakeJob(jq *Client, job *Job, hold time.Duration) bool {
	if hold <= 0 {
		return false
	}

	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		killed, err := jq.Touch(job)
		if err != nil || killed {
			return killed
		}

		<-time.After(20 * time.Millisecond)
	}

	return false
}

// caFileWrap lets us pass a CA file path to newFakeRunnerFunc via a tiny
// interface (so the helper doesn't need to import internal.Config).
type caFileWrap string

func (c caFileWrap) caFile() string { return string(c) }

// TestJobqueueMockRunner is a proof-of-concept: the server uses the "mock"
// scheduler and an in-process fake runner, so jobs are driven through their
// full lifecycle with no runner subprocess and no real command execution. It
// should be near-instant compared to the real-runner tests.
func TestJobqueueMockRunner(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	// a meaningful (but short) CheckRunnerTime so the scheduling-gap assertion
	// below still detects the old "delayed by a full CheckRunnerTime" regression.
	serverConfig.Timings.CheckRunnerTime = 2 * time.Second
	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.RunnerCmd = mockRunnerCmd
	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		RunnerFunc: newFakeRunnerFunc(caFileWrap(config.ManagerCAFile), addr,
			config.ManagerCertDomain, config.ManagerTokenFile,
			func(job *Job) fakeOutcome {
				if strings.Contains(job.Cmd, "false") {
					return fakeOutcome{fail: true}
				}

				return fakeOutcome{}
			}),
	}

	Convey("With a mock scheduler and in-process fake runner", t, func() {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("jobs get scheduled, run by the fake runner, and complete", func() {
			jobs := make([]*Job, 0, 10)
			for i := range 10 {
				jobs = append(jobs, &Job{
					Cmd: "echo " + string(rune('a'+i)), Cwd: testCwd, ReqGroup: fakeReqGroup,
					Requirements: standardReqs, RepGroup: "mocktest",
				})
			}

			inserts, _, errr := jq.Add(jobs, envVars, true)
			So(errr, ShouldBeNil)
			So(inserts, ShouldEqual, 10)

			complete := waitForJobState(jq, "mocktest", JobStateComplete, 10)
			So(complete, ShouldEqual, 10)
		})

		Convey("a job that fails with no retries is buried immediately", func() {
			jobs := []*Job{{
				Cmd: "echo x && false", Cwd: testCwd, ReqGroup: fakeReqGroup,
				Requirements: standardReqs, Retries: uint8(0), RepGroup: "mockfail0",
			}}

			inserts, _, errr := jq.Add(jobs, envVars, true)
			So(errr, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			buried := waitForJobState(jq, "mockfail0", JobStateBuried, 1)
			So(buried, ShouldEqual, 1)
		})

		Convey("a failing job is retried then buried, and kicking retries it again", func() {
			jobs := []*Job{{
				Cmd: "echo x && false", Cwd: testCwd, ReqGroup: fakeReqGroup,
				Requirements: standardReqs, Retries: uint8(1), RepGroup: "mockfail",
			}}

			inserts, _, errr := jq.Add(jobs, envVars, true)
			So(errr, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			buried := waitForJobState(jq, "mockfail", JobStateBuried, 1)
			So(buried, ShouldEqual, 1)

			// kicking a buried job makes it ready again; the fake runner runs
			// it, it fails again and is re-buried - repeatedly.
			for range 2 {
				buriedJobs, errg := jq.GetByRepGroup("mockfail", false, 0, JobStateBuried, false, false)
				So(errg, ShouldBeNil)
				So(len(buriedJobs), ShouldEqual, 1)

				kicked, errk := jq.Kick([]*JobEssence{buriedJobs[0].ToEssense()})
				So(errk, ShouldBeNil)
				So(kicked, ShouldEqual, 1)

				reburied := waitForJobState(jq, "mockfail", JobStateBuried, 1)
				So(reburied, ShouldEqual, 1)
			}
		})

		Convey("limit groups cap concurrency and the next job starts without delay", func() {
			// limitedA jobs share limit group "b:1" with the limitedB job, so the
			// server lets only one of them be reserved/run at a time; the 1-core
			// limitedB job should become runnable promptly once the limitedA jobs
			// free the shared limit (the old regression delayed it by a full
			// CheckRunnerTime).
			count := 3

			jobs := make([]*Job, 0, count)
			for i := 1; i <= count; i++ {
				jobs = append(jobs, &Job{
					Cmd: "echo " + string(rune('a'+i)), Cwd: testCwd, CwdMatters: true,
					ReqGroup: "limitedA", Requirements: &jqs.Requirements{RAM: 1, Time: time.Second, Cores: 0},
					Override: uint8(2), RepGroup: "limited", LimitGroups: []string{"a:5", "b:1"},
				})
			}

			inserts, _, errr := jq.Add(jobs, envVars, true)
			So(errr, ShouldBeNil)
			So(inserts, ShouldEqual, count)

			So(waitForJobState(jq, "limited", JobStateComplete, 1), ShouldBeGreaterThanOrEqualTo, 1)

			jobs = []*Job{{
				Cmd: "echo z", Cwd: testCwd, CwdMatters: true, ReqGroup: "limitedB",
				Requirements: &jqs.Requirements{RAM: 1, Time: time.Second, Cores: 1},
				Override:     uint8(2), RepGroup: "limited", LimitGroups: []string{"c:5", "b:1"},
			}}

			inserts, _, errr = jq.Add(jobs, envVars, true)
			So(errr, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			So(waitForJobState(jq, "limited", JobStateComplete, count+1), ShouldEqual, count+1)

			completed, errg := jq.GetByRepGroup("limited", false, 0, JobStateComplete, false, false)
			So(errg, ShouldBeNil)

			var (
				zeroCPUComplete int
				zeroCPUEnd      time.Time
				oneCPUComplete  int
				oneCPUStart     time.Time
			)

			for _, job := range completed {
				switch job.ReqGroup {
				case "limitedA":
					zeroCPUComplete++

					if job.EndTime.After(zeroCPUEnd) {
						zeroCPUEnd = job.EndTime
					}
				case "limitedB":
					oneCPUComplete++
					oneCPUStart = job.StartTime
				}
			}

			So(zeroCPUComplete, ShouldEqual, count)
			So(oneCPUComplete, ShouldEqual, 1)
			So(zeroCPUEnd.IsZero(), ShouldBeFalse)
			So(oneCPUStart.IsZero(), ShouldBeFalse)
			// the 1-CPU job should start within a re-check cycle or two of the
			// limited jobs finishing (proving the freed limit gets noticed and
			// rescheduled promptly); allow generous headroom for scheduling
			// jitter under parallel test load rather than asserting a tight gap.
			So(oneCPUStart.Sub(zeroCPUEnd), ShouldBeLessThan, 2*serverConfig.Timings.CheckRunnerTime)
		})
	})
}

// waitForJobState polls until want jobs in the repgroup reach the given state,
// or the deadline passes; returns how many reached it.
func waitForJobState(jq *Client, repGroup string, state JobState, want int) int {
	limit := time.Now().Add(20 * time.Second)

	for {
		jobs, err := jq.GetByRepGroup(repGroup, false, 0, state, false, false)
		if err == nil && len(jobs) >= want {
			return len(jobs)
		}

		if time.Now().After(limit) {
			return len(jobs)
		}

		<-time.After(20 * time.Millisecond)
	}
}
