/*******************************************************************************
 * Copyright (c) 2021,2025 Genome Research Ltd.
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
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	missingSchedulerJobKey = "missing-key"
	testDeployment         = "development"
)

var (
	errSchedulerJobTimeout     = errors.New("timed out waiting for WaitForJobs")
	errSchedulerNoReservedJob  = errors.New("reserve returned no job")
	errSchedulerNotLocalConfig = errors.New("test scheduler config is not local")
)

func TestSchedulerGetJobByKey(t *testing.T) {
	Convey("Given a running test manager and scheduler", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		jq, ok := s.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		schedulerConfig, ok := config.SchedulerConfig.(*jqs.ConfigLocal)
		So(ok, ShouldBeTrue)

		Convey("GetJobByKey returns a submitted ready job by key", func() {
			job := s.NewJob("echo b3 ready", "rg-b3-ready", "req-b3-ready", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			stored, err := s.GetJobByKey(keys[0], false, false)
			So(err, ShouldBeNil)
			So(stored.Key(), ShouldEqual, keys[0])
			So(stored.State, ShouldEqual, jobqueue.JobStateReady)
		})

		Convey("GetJobByKey fetches stdout and stderr for complete jobs when requested", func() {
			job := s.NewJob("printf 'typed stdout'; printf 'typed stderr' >&2",
				"rg-b3-std", "req-b3-std", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			reserved, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(reserved.Key(), ShouldEqual, keys[0])

			So(jq.Execute(ctx, reserved, schedulerConfig.Shell), ShouldBeNil)

			stored, err := s.GetJobByKey(keys[0], true, false)
			So(err, ShouldBeNil)
			So(stored.State, ShouldEqual, jobqueue.JobStateComplete)

			stdout, err := stored.StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, "typed stdout")

			stderr, err := stored.StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "typed stderr")
		})

		Convey("GetJobByKey rejects a blank key", func() {
			stored, err := s.GetJobByKey("", false, false)
			So(stored, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr, ShouldResemble, jobqueue.Error{
				Op:  getJobByKeyOp,
				Err: jobqueue.ErrBadRequest,
			})
		})

		Convey("GetJobByKey reports a missing key as a bad job", func() {
			stored, err := s.GetJobByKey(missingSchedulerJobKey, false, false)
			So(stored, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr, ShouldResemble, jobqueue.Error{
				Op:   getJobByKeyOp,
				Item: missingSchedulerJobKey,
				Err:  jobqueue.ErrBadJob,
			})
		})
	})
}

type schedulerJobStderrError string

func (s schedulerJobStderrError) Error() string {
	return string(s)
}

func TestSchedulerSubmitJobsAndReturnIDs(t *testing.T) {
	Convey("Given a running test manager and one scheduler job", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)

		So(s, ShouldNotBeNil)
		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		job := s.NewJob("echo ok", "rg-a1", "req-a1", "", "", nil)
		_ = &ErrDuplicateJobs

		So(ErrDuplicateJobs.Error(), ShouldEqual, "some of the added jobs were duplicates")

		Convey("SubmitJobsAndReturnIDs returns the submitted job key", func() {
			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			info := server.GetServerStats()
			So(info.Ready, ShouldEqual, 1)

			Convey("submitting the same queued job again returns its key without adding another ready job", func() {
				keys, err = s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
				So(err, ShouldBeNil)
				So(keys, ShouldResemble, []string{job.Key()})

				info = server.GetServerStats()
				So(info.Ready, ShouldEqual, 1)

				Convey("SubmitJobs exposes duplicate failures through ErrDuplicateJobs", func() {
					err = s.SubmitJobs([]*jobqueue.Job{job})
					So(err, ShouldNotBeNil)
					So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
					So(err.Error(), ShouldEqual, "some of the added jobs were duplicates")
				})
			})
		})
	})
}

func TestSchedulerNewJobFromJSON(t *testing.T) {
	Convey("Given a Scheduler configured with JSON job defaults", t, func() {
		cwd := t.TempDir()
		s := &Scheduler{cwd: cwd, queue: "short", queuesAvoid: "slow,big"}

		Convey("JobDefaults maps Scheduler defaults into jobqueue defaults", func() {
			defaults := s.JobDefaults()

			So(defaults.Cwd, ShouldEqual, cwd)
			So(defaults.CwdMatters, ShouldBeTrue)
			So(defaults.SchedulerQueue, ShouldEqual, "short")
			So(defaults.SchedulerQueuesAvoid, ShouldEqual, "slow,big")
			So(defaults.Memory, ShouldEqual, 100)
			So(defaults.Time, ShouldEqual, 10*time.Second)
			So(defaults.CPUs, ShouldEqual, float64(1))
			So(defaults.Disk, ShouldEqual, 1)
			So(defaults.DiskSet, ShouldBeTrue)
			So(defaults.Retries, ShouldEqual, 30)
			So(defaults.Override, ShouldEqual, 0)
		})

		Convey("NewJobFromJSON converts a JobViaJSON using Scheduler defaults", func() {
			retries := 3
			override := 2
			mounts := jobqueue.MountConfigs{{
				Mount:     "mnt",
				CacheBase: "cache-base",
				Targets: []jobqueue.MountTarget{{
					Profile:  "prof",
					Path:     "bucket/path",
					Cache:    true,
					CacheDir: "cache-dir",
					Write:    true,
				}},
			}}
			spec := &jobqueue.JobViaJSON{
				Cmd:          "echo json",
				RepGrp:       "rg-json",
				Retries:      &retries,
				LimitGrps:    []string{"lg1"},
				Memory:       "8G",
				Time:         "8h",
				Override:     &override,
				MountConfigs: mounts,
			}

			job, err := s.NewJobFromJSON(spec)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, "echo json")
			So(job.RepGroup, ShouldEqual, "rg-json")
			So(job.Retries, ShouldEqual, uint8(3))
			So(job.LimitGroups, ShouldResemble, []string{"lg1"})
			So(job.Requirements.RAM, ShouldEqual, 8*1024)
			So(job.Requirements.Time, ShouldEqual, 8*time.Hour)
			So(job.Cwd, ShouldEqual, cwd)
			So(job.CwdMatters, ShouldBeTrue)
			So(job.Requirements.Other, ShouldResemble, map[string]string{
				"scheduler_queue":        "short",
				"scheduler_queues_avoid": "slow,big",
			})
			So(job.Override, ShouldEqual, uint8(2))
			So(job.MountConfigs, ShouldResemble, mounts)
		})

		Convey("NewJobFromJSON returns a typed bad request for a nil spec", func() {
			job, err := s.NewJobFromJSON(nil)
			So(job, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr.Err, ShouldEqual, jobqueue.ErrBadRequest)
		})

		Convey("NewJobFromJSON returns conversion errors from JobViaJSON", func() {
			job, err := s.NewJobFromJSON(&jobqueue.JobViaJSON{RepGrp: "missing-cmd"})
			So(job, ShouldBeNil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "cmd was not specified")
		})
	})
}

type waitForRunningSequenceJobqueue struct {
	*pretendJobqueue
	job    *jobqueue.Job
	states []jobqueue.JobState
	calls  atomic.Int64
}

func newWaitForRunningSequenceScheduler(key string,
	states ...jobqueue.JobState) (*Scheduler, *waitForRunningSequenceJobqueue) {
	if len(states) == 0 {
		states = []jobqueue.JobState{jobqueue.JobStateReady}
	}

	s := &Scheduler{cwd: "/tmp"}
	job := s.NewJob("cmd-"+key, "rg-"+key, "req-"+key, "", "", nil)

	jq := &waitForRunningSequenceJobqueue{
		pretendJobqueue: newPretendJobqueue(),
		job:             job,
		states:          states,
	}
	s.jq = jq

	return s, jq
}

func (w *waitForRunningSequenceJobqueue) GetByEssence(je *jobqueue.JobEssence,
	_ bool, _ bool) (*jobqueue.Job, error) {
	if je == nil || je.Key() == "" {
		return nil, jobqueue.Error{Op: getByEssenceOp, Err: jobqueue.ErrBadRequest}
	}

	key := je.Key()
	if key != w.job.Key() {
		return nil, jobqueue.Error{Op: getByEssenceOp, Item: key, Err: jobqueue.ErrBadJob}
	}

	call := int(w.calls.Add(1)) - 1
	if call >= len(w.states) {
		call = len(w.states) - 1
	}

	w.job.State = w.states[call]

	return w.job, nil
}

func TestSchedulerWaitForRunning(t *testing.T) {
	Convey("Given a running test manager and scheduler", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		runner, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(runner, ShouldNotBeNil)

		defer func() {
			So(runner.Disconnect(), ShouldBeNil)
		}()

		runnerJQ, ok := runner.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		Convey("WaitForRunning returns when a ready job starts running", func() {
			job := s.NewJob("echo c1 running", "rg-c1-running",
				"req-c1-running", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			done := waitForRunningAsync(waitCtx, s, keys[0], 10*time.Millisecond)

			started, err := reserveAndStartSchedulerJob(runnerJQ)
			So(err, ShouldBeNil)

			result := receiveWaitForRunningResult(done, 6*time.Second)
			So(result.err, ShouldBeNil)
			So(result.job, ShouldNotBeNil)
			So(result.job.Key(), ShouldEqual, keys[0])
			So(result.job.State, ShouldEqual, jobqueue.JobStateRunning)

			err = runnerJQ.Archive(started, &jobqueue.JobEndState{
				Exited:   true,
				Exitcode: 0,
				EndTime:  time.Now(),
			})
			So(err, ShouldBeNil)
		})

		Convey("WaitForRunning skips reserved states and returns final started-or-ended states", func() {
			finalStates := []jobqueue.JobState{
				jobqueue.JobStateRunning,
				jobqueue.JobStateLost,
				jobqueue.JobStateComplete,
				jobqueue.JobStateBuried,
				jobqueue.JobStateUnknown,
			}

			for _, finalState := range finalStates {
				label := "c1-reserved-" + string(finalState)
				s, jq := newWaitForRunningSequenceScheduler(label,
					jobqueue.JobStateReserved, finalState)
				key := jq.job.Key()

				got, err := s.WaitForRunning(ctx, key, time.Millisecond)
				So(err, ShouldBeNil)
				So(got, ShouldNotBeNil)
				So(got.Key(), ShouldEqual, key)
				So(got.State, ShouldEqual, finalState)
				So(got.State, ShouldNotEqual, jobqueue.JobStateReserved)
			}

			s, jq := newWaitForRunningSequenceScheduler("c1-reserved-canceled",
				jobqueue.JobStateReserved)
			key := jq.job.Key()
			waitCtx, cancel := context.WithCancel(ctx)

			done := waitForRunningAsync(waitCtx, s, key, time.Millisecond)

			So(waitForRunningCalls(jq, 1, time.Second), ShouldBeTrue)
			cancel()

			result := receiveWaitForRunningResult(done, time.Second)
			So(result.job, ShouldBeNil)
			So(errors.Is(result.err, context.Canceled), ShouldBeTrue)
		})

		Convey("WaitForRunning returns lost before running", func() {
			s, jq := newWaitForRunningSequenceScheduler("c1-lost",
				jobqueue.JobStateLost)
			key := jq.job.Key()

			got, err := s.WaitForRunning(ctx, key, time.Millisecond)
			So(err, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.Key(), ShouldEqual, key)
			So(got.State, ShouldEqual, jobqueue.JobStateLost)
		})

		Convey("WaitForRunning returns complete before running", func() {
			s, jq := newWaitForRunningSequenceScheduler("c1-complete",
				jobqueue.JobStateComplete)
			key := jq.job.Key()

			got, err := s.WaitForRunning(ctx, key, time.Millisecond)
			So(err, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, jobqueue.JobStateComplete)
		})

		Convey("WaitForRunning returns buried before running", func() {
			s, jq := newWaitForRunningSequenceScheduler("c1-buried",
				jobqueue.JobStateBuried)
			key := jq.job.Key()

			got, err := s.WaitForRunning(ctx, key, time.Millisecond)
			So(err, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, jobqueue.JobStateBuried)
		})

		Convey("WaitForRunning returns unknown without retrying", func() {
			s, jq := newWaitForRunningSequenceScheduler("c1-unknown",
				jobqueue.JobStateUnknown)
			key := jq.job.Key()

			got, err := s.WaitForRunning(ctx, key, time.Millisecond)
			So(err, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, jobqueue.JobStateUnknown)
			So(jq.calls.Load(), ShouldEqual, 1)
		})

		Convey("WaitForRunning rejects a blank key", func() {
			s, _ := newWaitForRunningSequenceScheduler("unused",
				jobqueue.JobStateRunning)

			got, err := s.WaitForRunning(ctx, "", time.Millisecond)
			So(got, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr, ShouldResemble, jobqueue.Error{
				Op:  "WaitForRunning",
				Err: jobqueue.ErrBadRequest,
			})
		})

		Convey("WaitForRunning reports a missing key as a bad job", func() {
			s, _ := newWaitForRunningSequenceScheduler("existing-key",
				jobqueue.JobStateRunning)

			got, err := s.WaitForRunning(ctx, missingSchedulerJobKey, time.Millisecond)
			So(got, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr, ShouldResemble, jobqueue.Error{
				Op:   "WaitForRunning",
				Item: missingSchedulerJobKey,
				Err:  jobqueue.ErrBadJob,
			})
		})

		Convey("WaitForRunning returns context deadline before a ready job starts", func() {
			s, jq := newWaitForRunningSequenceScheduler("c1-deadline",
				jobqueue.JobStateReady)
			key := jq.job.Key()

			waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()

			got, err := s.WaitForRunning(waitCtx, key, time.Millisecond)
			So(got, ShouldBeNil)
			So(errors.Is(err, context.DeadlineExceeded), ShouldBeTrue)
		})

		Convey("WaitForRunning accepts a non-positive poll interval", func() {
			s, jq := newWaitForRunningSequenceScheduler("c1-canceled",
				jobqueue.JobStateReady)
			key := jq.job.Key()
			waitCtx, cancel := context.WithCancel(ctx)
			cancel()

			got, err := s.WaitForRunning(waitCtx, key, 0)
			So(got, ShouldBeNil)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
		})
	})
}

func waitForRunningAsync(ctx context.Context, s *Scheduler, key string,
	pollInterval time.Duration) <-chan waitForRunningResult {
	done := make(chan waitForRunningResult, 1)

	go func() {
		job, err := s.WaitForRunning(ctx, key, pollInterval)
		done <- waitForRunningResult{job: job, err: err}
	}()

	return done
}

func receiveWaitForRunningResult(done <-chan waitForRunningResult,
	timeout time.Duration) waitForRunningResult {
	select {
	case result := <-done:
		return result
	case <-time.After(timeout):
		return waitForRunningResult{err: errSchedulerJobTimeout}
	}
}

func waitForRunningCalls(jq *waitForRunningSequenceJobqueue, want int64,
	timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if jq.calls.Load() >= want {
			return true
		}

		select {
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

func TestSchedulerSubmitJobsOptions(t *testing.T) {
	Convey("Given a running test manager and scheduler", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		jq, ok := s.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		Convey("completed jobs are skipped by default and rerun when requested", func() {
			job := s.NewJob("echo a2 complete", "rg-a2-complete", "req-a2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			reserved, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, job.Key())
			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

			err = jq.Archive(reserved, &jobqueue.JobEndState{
				Exited:   true,
				Exitcode: 0,
				EndTime:  time.Now(),
			})
			So(err, ShouldBeNil)

			keys, err = s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldBeEmpty)

			info := server.GetServerStats()
			So(info.Ready, ShouldEqual, 0)

			keys, err = s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{RerunCompleted: true})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			info = server.GetServerStats()
			So(info.Ready, ShouldEqual, 1)
		})

		Convey("explicit environment variables are persisted", func() {
			job := s.NewJob("echo a2 env", "rg-a2-env", "req-a2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{EnvVars: []string{"A=B"}})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			stored, err := jq.GetByEssence(&jobqueue.JobEssence{JobKey: keys[0]}, false, true)
			So(err, ShouldBeNil)

			env, err := stored.Env()
			So(err, ShouldBeNil)
			So(slices.Contains(env, "A=B"), ShouldBeTrue)
		})

		Convey("an explicit empty environment is persisted as empty", func() {
			t.Setenv("WR_A2_EMPTY_ENV_SHOULD_NOT_APPEAR", "present")

			job := s.NewJob("echo a2 empty env", "rg-a2-empty-env", "req-a2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job},
				SubmitJobsOptions{EnvVars: []string{}})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			stored, err := jq.GetByEssence(&jobqueue.JobEssence{JobKey: keys[0]}, false, true)
			So(err, ShouldBeNil)

			env, err := stored.Env()
			So(err, ShouldBeNil)
			So(env, ShouldResemble, []string{})
		})
	})
}

func TestSchedulerSubmitJobsAndWait(t *testing.T) {
	Convey("Given a running test manager and scheduler", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		runner, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(runner, ShouldNotBeNil)

		defer func() {
			So(runner.Disconnect(), ShouldBeNil)
		}()

		runnerJQ, ok := runner.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		Convey("SubmitJobsAndWait returns complete and buried jobs in submitted-key order", func() {
			jobs := []*jobqueue.Job{
				s.NewJob("printf 'a1 stdout'; printf 'a1 stderr' >&2",
					"rg-b1-mixed-1", "req-b1-mixed", "", "", nil),
				s.NewJob("echo b1 mixed 2", "rg-b1-mixed-2", "req-b1-mixed", "", "", nil),
			}

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			done := submitJobsAndWaitAsync(waitCtx, s, jobs, SubmitJobsOptions{})

			So(executeNextSchedulerJob(runnerJQ, config), ShouldBeNil)
			So(buryNextSchedulerJob(runnerJQ, 12, "b1 failed", "b1 stderr"), ShouldBeNil)

			result := receiveWaitForJobsResult(done, 6*time.Second)
			So(result.err, ShouldBeNil)
			So(result.jobs, ShouldHaveLength, 2)
			So(result.jobs[0].Key(), ShouldEqual, jobs[0].Key())
			So(result.jobs[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(result.jobs[0].Exitcode, ShouldEqual, 0)
			So(result.jobs[1].Key(), ShouldEqual, jobs[1].Key())
			So(result.jobs[1].State, ShouldEqual, jobqueue.JobStateBuried)
			So(result.jobs[1].Exitcode, ShouldEqual, 12)
			So(result.jobs[1].FailReason, ShouldEqual, "b1 failed")

			stdout, err := result.jobs[0].StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, "a1 stdout")

			stderr, err := result.jobs[0].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "a1 stderr")

			stderr, err = result.jobs[1].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "b1 stderr")
		})

		Convey("SubmitJobsAndWait returns context cancellation before submission", func() {
			waitCtx, cancel := context.WithCancel(ctx)
			cancel()

			job := s.NewJob("echo b1 canceled", "rg-b1-canceled", "req-b1-canceled",
				"", "", nil)

			got, err := s.SubmitJobsAndWait(waitCtx, []*jobqueue.Job{job},
				SubmitJobsOptions{})
			So(got, ShouldBeNil)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
		})

		Convey("SubmitJobsAndWait returns gathered jobs and unfinished keys on context deadline", func() {
			jobs := []*jobqueue.Job{
				s.NewJob("echo b1 deadline 1", "rg-b1-deadline-1", "req-b1-deadline", "", "", nil),
				s.NewJob("echo b1 deadline 2", "rg-b1-deadline-2", "req-b1-deadline", "", "", nil),
			}

			// generous deadline: it must outlast the (load-sensitive) time to
			// reserve+archive+gather the one completed job below, while the other
			// job stays unfinished until the deadline. A tight value raced the
			// gather under heavy parallel-test load (got 0 gathered, not 1).
			waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			done := submitJobsAndWaitAsync(waitCtx, s, jobs, SubmitJobsOptions{})

			So(archiveNextSchedulerJob(runnerJQ), ShouldBeNil)

			result := receiveWaitForJobsResult(done, 5*time.Second)
			So(result.jobs, ShouldHaveLength, 1)
			So(result.jobs[0].Key(), ShouldEqual, jobs[0].Key())
			So(result.jobs[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(errors.Is(result.err, context.DeadlineExceeded), ShouldBeTrue)
			So(result.err.Error(), ShouldContainSubstring, "unfinished job keys: "+jobs[1].Key())
			So(result.err.Error(), ShouldNotContainSubstring, jobs[0].Key())
		})

		Convey("SubmitJobsAndWait skips already complete matching jobs by default", func() {
			job := s.NewJob("echo b1 skip complete", "rg-b1-skip-complete",
				"req-b1-skip-complete", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			So(archiveNextSchedulerJob(runnerJQ), ShouldBeNil)

			got, err := s.SubmitJobsAndWait(ctx, []*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(got, ShouldResemble, []*jobqueue.Job{})
		})

		Convey("SubmitJobsAndWait reruns already complete matching jobs when requested", func() {
			job := s.NewJob("echo b1 rerun complete", "rg-b1-rerun-complete",
				"req-b1-rerun-complete", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job.Key()})

			So(archiveNextSchedulerJob(runnerJQ), ShouldBeNil)

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			done := submitJobsAndWaitAsync(waitCtx, s, []*jobqueue.Job{job},
				SubmitJobsOptions{RerunCompleted: true})

			So(archiveNextSchedulerJob(runnerJQ), ShouldBeNil)

			result := receiveWaitForJobsResult(done, 6*time.Second)
			So(result.err, ShouldBeNil)
			So(result.jobs, ShouldHaveLength, 1)
			So(result.jobs[0].Key(), ShouldEqual, job.Key())
			So(result.jobs[0].State, ShouldEqual, jobqueue.JobStateComplete)
		})
	})
}

func submitJobsAndWaitAsync(ctx context.Context, s *Scheduler, jobs []*jobqueue.Job,
	opts SubmitJobsOptions) <-chan waitForJobsResult {
	done := make(chan waitForJobsResult, 1)

	go func() {
		got, err := s.SubmitJobsAndWait(ctx, jobs, opts)
		done <- waitForJobsResult{jobs: got, err: err}
	}()

	return done
}

func executeNextSchedulerJob(jq *jobqueue.Client, config jobqueue.ServerConfig) error {
	schedulerConfig, ok := config.SchedulerConfig.(*jqs.ConfigLocal)
	if !ok {
		return errSchedulerNotLocalConfig
	}

	job, err := jq.Reserve(2 * time.Second)
	if err != nil {
		return err
	}

	if job == nil {
		return errSchedulerNoReservedJob
	}

	return jq.Execute(context.Background(), job, schedulerConfig.Shell)
}

func receiveWaitForJobsResult(done <-chan waitForJobsResult,
	timeout time.Duration) waitForJobsResult {
	select {
	case result := <-done:
		return result
	case <-time.After(timeout):
		return waitForJobsResult{err: errSchedulerJobTimeout}
	}
}

func archiveNextSchedulerJob(jq *jobqueue.Client) error {
	job, err := reserveAndStartSchedulerJob(jq)
	if err != nil {
		return err
	}

	return jq.Archive(job, &jobqueue.JobEndState{
		Exited:   true,
		Exitcode: 0,
		EndTime:  time.Now(),
	})
}

func reserveAndStartSchedulerJob(jq *jobqueue.Client) (*jobqueue.Job, error) {
	job, err := jq.Reserve(2 * time.Second)
	if err != nil {
		return nil, err
	}

	if job == nil {
		return nil, errSchedulerNoReservedJob
	}

	if err = jq.Started(job, os.Getpid()); err != nil {
		return nil, err
	}

	return job, nil
}

func TestSchedulerWaitForJobs(t *testing.T) {
	Convey("Given a running test manager and scheduler", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		s, err := New(SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		defer func() {
			So(s.Disconnect(), ShouldBeNil)
		}()

		jq, ok := s.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		schedulerConfig, ok := config.SchedulerConfig.(*jqs.ConfigLocal)
		So(ok, ShouldBeTrue)

		Convey("WaitForJobs returns live jobs after they archive", func() {
			jobs := []*jobqueue.Job{
				s.NewJob("echo b2 live 1", "rg-b2-live-1", "req-b2-live", "", "", nil),
				s.NewJob("echo b2 live 2", "rg-b2-live-2", "req-b2-live", "", "", nil),
			}

			keys, err := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})
			So(err, ShouldBeNil)

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			done := waitForJobsAsync(waitCtx, s, keys...)

			So(archiveNextSchedulerJob(jq), ShouldBeNil)
			So(archiveNextSchedulerJob(jq), ShouldBeNil)

			result := receiveWaitForJobsResult(done, 6*time.Second)
			So(result.err, ShouldBeNil)
			So(result.jobs, ShouldHaveLength, 2)
			So(result.jobs[0].Key(), ShouldEqual, keys[0])
			So(result.jobs[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(result.jobs[1].Key(), ShouldEqual, keys[1])
			So(result.jobs[1].State, ShouldEqual, jobqueue.JobStateComplete)
		})

		Convey("WaitForJobs returns already terminal jobs with stdout and stderr", func() {
			completeJob := s.NewJob("printf 'pre stdout'; printf 'pre stderr' >&2",
				"rg-b2-pre-complete", "req-b2-pre", "", "", nil)
			buriedJob := s.NewJob("echo b2 pre buried", "rg-b2-pre-buried",
				"req-b2-pre", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{completeJob, buriedJob},
				SubmitJobsOptions{})
			So(err, ShouldBeNil)

			reserved, err := jq.Reserve(2 * time.Second)
			So(err, ShouldBeNil)
			So(reserved.Key(), ShouldEqual, keys[0])
			So(jq.Execute(ctx, reserved, schedulerConfig.Shell), ShouldBeNil)
			So(buryNextSchedulerJob(jq, 7, "pre failed", "pre buried stderr"),
				ShouldBeNil)

			got, err := s.WaitForJobs(ctx, keys...)
			So(err, ShouldBeNil)
			So(got, ShouldHaveLength, 2)
			So(got[0].Key(), ShouldEqual, keys[0])
			So(got[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(got[0].Exitcode, ShouldEqual, 0)
			So(got[1].Key(), ShouldEqual, keys[1])
			So(got[1].State, ShouldEqual, jobqueue.JobStateBuried)
			So(got[1].Exitcode, ShouldEqual, 7)
			So(got[1].FailReason, ShouldEqual, "pre failed")

			stdout, err := got[0].StdOut()
			So(err, ShouldBeNil)
			So(stdout, ShouldEqual, "pre stdout")

			stderr, err := got[0].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "pre stderr")

			stderr, err = got[1].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "pre buried stderr")
		})

		Convey("WaitForJobs de-duplicates keys in input order", func() {
			jobs := []*jobqueue.Job{
				s.NewJob("echo b2 dedup 1", "rg-b2-dedup-1", "req-b2-dedup", "", "", nil),
				s.NewJob("echo b2 dedup 2", "rg-b2-dedup-2", "req-b2-dedup", "", "", nil),
			}

			keys, err := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})
			So(err, ShouldBeNil)

			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			done := waitForJobsAsync(waitCtx, s, keys[0], keys[0], keys[1])

			So(archiveNextSchedulerJob(jq), ShouldBeNil)
			So(archiveNextSchedulerJob(jq), ShouldBeNil)

			result := receiveWaitForJobsResult(done, 6*time.Second)
			So(result.err, ShouldBeNil)
			So(result.jobs, ShouldHaveLength, 2)
			So(result.jobs[0].Key(), ShouldEqual, keys[0])
			So(result.jobs[1].Key(), ShouldEqual, keys[1])
		})

		Convey("WaitForJobs returns an empty slice when no keys are supplied", func() {
			got, err := s.WaitForJobs(ctx)
			So(err, ShouldBeNil)
			So(got, ShouldResemble, []*jobqueue.Job{})
		})

		Convey("WaitForJobs rejects a blank key", func() {
			got, err := s.WaitForJobs(ctx, "")
			So(got, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr, ShouldResemble, jobqueue.Error{
				Op:  "WaitForJobs",
				Err: jobqueue.ErrBadRequest,
			})
		})

		Convey("WaitForJobs returns cancellation before looking up jobs", func() {
			waitCtx, cancel := context.WithCancel(ctx)
			cancel()

			got, err := s.WaitForJobs(waitCtx, missingSchedulerJobKey)
			So(got, ShouldHaveLength, 0)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(err.Error(), ShouldContainSubstring,
				"unfinished job keys: "+missingSchedulerJobKey)

			var jqErr jobqueue.Error

			So(errors.As(err, &jqErr), ShouldBeFalse)
		})

		Convey("WaitForJobs returns partial terminal jobs on context deadline", func() {
			jobs := []*jobqueue.Job{
				s.NewJob("echo b2 deadline 1", "rg-b2-deadline-1", "req-b2-deadline", "", "", nil),
				s.NewJob("echo b2 deadline 2", "rg-b2-deadline-2", "req-b2-deadline", "", "", nil),
			}

			keys, err := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})
			So(err, ShouldBeNil)

			// generous deadline: it must outlast the (load-sensitive) time to
			// reserve+archive+gather the one completed job below, while the other
			// job stays unfinished until the deadline. A tight value raced the
			// gather under heavy parallel-test load (got 0 gathered, not 1).
			waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			done := waitForJobsAsync(waitCtx, s, keys...)

			So(archiveNextSchedulerJob(jq), ShouldBeNil)

			result := receiveWaitForJobsResult(done, 5*time.Second)
			So(result.jobs, ShouldHaveLength, 1)
			So(result.jobs[0].Key(), ShouldEqual, keys[0])
			So(result.jobs[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(errors.Is(result.err, context.DeadlineExceeded), ShouldBeTrue)
			So(result.err.Error(), ShouldContainSubstring, "unfinished job keys: "+keys[1])
			So(result.err.Error(), ShouldNotContainSubstring, keys[0])
		})
	})
}

func waitForJobsAsync(ctx context.Context, s *Scheduler, keys ...string) <-chan waitForJobsResult {
	done := make(chan waitForJobsResult, 1)

	go func() {
		jobs, err := s.WaitForJobs(ctx, keys...)
		done <- waitForJobsResult{jobs: jobs, err: err}
	}()

	return done
}

func buryNextSchedulerJob(jq *jobqueue.Client, exitCode int,
	failReason string, stderr string) error {
	job, err := reserveAndStartSchedulerJob(jq)
	if err != nil {
		return err
	}

	return jq.Bury(job, &jobqueue.JobEndState{
		Exited:   true,
		Exitcode: exitCode,
		EndTime:  time.Now(),
	}, failReason, schedulerJobStderrError(stderr))
}

func TestScheduler(t *testing.T) {
	Convey("Given some scheduler settings", t, func() {
		deployment := testDeployment
		timeout := 10 * time.Second
		logger := log15.New()
		ctx := context.Background()

		settings := SchedulerSettings{
			Deployment: deployment,
			Timeout:    timeout,
			Logger:     logger,
		}

		Convey("You can get unique strings", func() {
			str := UniqueString()
			So(len(str), ShouldEqual, 20)

			str2 := UniqueString()
			So(len(str2), ShouldEqual, 20)
			So(str2, ShouldNotEqual, str)
		})

		Convey("When the jobqueue server is up", func() {
			config, d := clienttesting.PrepareWrConfig(t)
			defer d()

			server := clienttesting.Serve(t, config)
			defer server.Stop(ctx, true)

			Convey("You can make a Scheduler", func() {
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				wd, err := os.Getwd()
				So(err, ShouldBeNil)
				So(s.cwd, ShouldEqual, wd)

				exe, err := os.Executable()
				So(err, ShouldBeNil)
				So(s.Executable(), ShouldEqual, exe)

				So(s.jq, ShouldNotBeNil)

				Convey("which lets you create jobs", func() {
					job := s.NewJob("cmd", "rep", "req", "", "", nil)
					So(job.Cmd, ShouldEqual, "cmd")
					So(job.RepGroup, ShouldEqual, "rep")
					So(job.ReqGroup, ShouldEqual, "req")
					So(job.Cwd, ShouldEqual, wd)
					So(job.CwdMatters, ShouldBeTrue)
					So(job.Requirements, ShouldResemble, &jqs.Requirements{RAM: 100, Time: 10 * time.Second, Cores: 1, Disk: 1})
					So(job.Retries, ShouldEqual, 30)
					So(job.DepGroups, ShouldBeNil)
					So(job.Dependencies, ShouldBeNil)
					So(job.Override, ShouldEqual, 0)

					job2 := s.NewJob("cmd2", "rep", "req", "a", "b", nil)
					So(job2.Cmd, ShouldEqual, "cmd2")
					So(job2.DepGroups, ShouldResemble, []string{"a"})
					So(job2.Dependencies, ShouldResemble,
						jobqueue.Dependencies{&jobqueue.Dependency{DepGroup: "b"}})

					Convey("which you can add to the queue", func() {
						err = s.SubmitJobs([]*jobqueue.Job{job, job2})
						So(err, ShouldBeNil)

						info := server.GetServerStats()
						So(info.Ready, ShouldEqual, 2)

						Convey("but you get an error if there are duplicates", func() {
							err = s.SubmitJobs([]*jobqueue.Job{job, job2})
							So(err, ShouldNotBeNil)
							So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
							So(err.Error(), ShouldEqual, "some of the added jobs were duplicates")

							info := server.GetServerStats()
							So(info.Ready, ShouldEqual, 2)
						})
					})

					Convey("which you can't add to the queue if the server is down", func() {
						server.Stop(ctx, true)

						err = s.SubmitJobs([]*jobqueue.Job{job, job2})
						So(err, ShouldNotBeNil)
					})

					Convey("which you can't add to the queue if you disconnected", func() {
						err = s.Disconnect()
						So(err, ShouldBeNil)
						err = s.SubmitJobs([]*jobqueue.Job{job, job2})
						So(err, ShouldNotBeNil)
					})
				})
			})

			Convey("You can make a Scheduler with a specified cwd and it creates jobs in there", func() {
				cwd := t.TempDir()
				settings.Cwd = cwd

				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Cwd, ShouldEqual, cwd)
				So(job.CwdMatters, ShouldBeTrue)
			})

			Convey("You can't create a Scheduler in an invalid dir", func() {
				d := cdNonExistantDir(t)
				defer d()

				s, err := New(settings)
				So(err, ShouldNotBeNil)
				So(s, ShouldBeNil)
			})

			Convey("You can't create a Scheduler if you pass an invalid dir", func() {
				settings.Cwd = "/non_existent"
				s, err := New(settings)
				So(err, ShouldNotBeNil)
				So(s, ShouldBeNil)
			})

			Convey("You can make a Scheduler that creates sudo jobs", func() {
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)
				s.EnableSudo()

				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Cmd, ShouldEqual, "sudo cmd")
			})

			Convey("You can make a Scheduler with a Req override", func() {
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				req := DefaultRequirements()
				req.RAM = 16000

				job := s.NewJob("cmd", "rep", "req", "", "", req)
				So(job.Requirements.RAM, ShouldEqual, 16000)
				So(job.Override, ShouldEqual, 1)
			})

			Convey("You can make a Scheduler with a queue override", func() {
				settings.Queue = "foo"
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				dreq := DefaultRequirements()

				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Requirements.RAM, ShouldEqual, dreq.RAM)
				So(job.Override, ShouldEqual, 0)
				So(job.Requirements.Other, ShouldResemble, map[string]string{"scheduler_queue": "foo"})
			})

			Convey("You can make a Scheduler with queues to avoid", func() {
				settings.QueuesAvoid = "avoid,queue"
				s, err := New(settings)
				So(err, ShouldBeNil)
				So(s, ShouldNotBeNil)

				dreq := DefaultRequirements()
				job := s.NewJob("cmd", "rep", "req", "", "", nil)
				So(job.Requirements.RAM, ShouldEqual, dreq.RAM)
				So(job.Override, ShouldEqual, 0)
				So(job.Requirements.Other, ShouldResemble, map[string]string{"scheduler_queues_avoid": "avoid,queue"})
			})
		})

		Convey("When the jobqueue server is not up, you can't make a Scheduler", func() {
			_, d := clienttesting.PrepareWrConfig(t)
			defer d()

			s, err := New(settings)
			So(err, ShouldNotBeNil)
			So(s, ShouldBeNil)
		})
	})
}

// cdNonExistantDir changes directory to a temp directory, then deletes that
// directory. It returns a function you should defer to change back to your
// original directory.
func cdNonExistantDir(t *testing.T) func() {
	t.Helper()

	tmpDir, d := clienttesting.CDTmpDir(t)

	os.RemoveAll(tmpDir)

	return d
}

func TestFakeScheduler(t *testing.T) {
	Convey("Given scheduler settings configured to store add commands", t, func() {
		PretendSubmissions = " "

		settings := SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		}

		Convey("You can make a Scheduler that records submitted jobs without a real server", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)
			So(s, ShouldNotBeNil)

			job1 := s.NewJob("cmd1", "rep1suffix", "req1", "depg1", "dep1", nil)
			job2 := s.NewJob("cmd2", "rep2suffix", "req2", "depg2", "dep2", nil)

			err = s.SubmitJobs([]*jobqueue.Job{job1, job2})
			So(err, ShouldBeNil)

			submittedJobs := s.SubmittedJobs()
			So(submittedJobs, ShouldResemble, []*jobqueue.Job{job1, job2})

			Convey("You can FindJobsByRepGroupSuffix", func() {
				jobs, err := s.FindJobsByRepGroupSuffix("none")
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupSuffix("p1suffix")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1})

				jobs, err = s.FindJobsByRepGroupSuffix("suffix")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1, job2})
			})

			Convey("You can FindJobsByRepGroupPrefixAndState", func() {
				jobs, err := s.FindJobsByRepGroupPrefixAndState("none", "")
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupPrefixAndState("rep1", jobqueue.JobStateDelayed)
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1})

				jobs, err = s.FindJobsByRepGroupPrefixAndState("ep1", jobqueue.JobStateDelayed)
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupPrefixAndState("rep1", jobqueue.JobStateRunning)
				So(err, ShouldBeNil)
				So(jobs, ShouldBeNil)

				jobs, err = s.FindJobsByRepGroupPrefixAndState("rep", "")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1, job2})
			})

			Convey("You can find only incomplete jobs by repgroup match", func() {
				job3 := s.NewJob("cmd3", "rep1complete", "req3", "", "", nil)
				err = s.SubmitJobs([]*jobqueue.Job{job3})
				So(err, ShouldBeNil)

				job2.State = jobqueue.JobStateReady
				job3.State = jobqueue.JobStateComplete

				jobs, errf := s.FindIncompleteJobsByRepGroup("rep1", jobqueue.RepGroupMatchPrefix)
				So(errf, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job1})
			})

			Convey("You can find only incomplete jobs by repgroup match and state", func() {
				job3 := s.NewJob("cmd3", "rep1running", "req3", "", "", nil)
				err = s.SubmitJobs([]*jobqueue.Job{job3})
				So(err, ShouldBeNil)

				job1.State = jobqueue.JobStateDelayed
				job2.State = jobqueue.JobStateReady
				job3.State = jobqueue.JobStateRunning

				jobs, errf := s.FindIncompleteJobsByRepGroupAndState("rep1",
					jobqueue.RepGroupMatchPrefix, jobqueue.JobStateRunning)
				So(errf, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job3})
			})

			Convey("You can get the latest completion time by repgroup", func() {
				now := time.Now().Truncate(time.Second)

				job1.State = jobqueue.JobStateComplete
				job1.EndTime = now.Add(1 * time.Second)

				job2.State = jobqueue.JobStateComplete
				job2.EndTime = now.Add(2 * time.Second)

				job3 := s.NewJob("cmd3", "rep2suffix", "req3", "", "", nil)
				err = s.SubmitJobs([]*jobqueue.Job{job3})
				So(err, ShouldBeNil)

				job3.State = jobqueue.JobStateComplete
				job3.EndTime = now.Add(3 * time.Second)

				lct, errf := s.GetLastCompletionTimeByRepGroup("rep1suffix",
					jobqueue.RepGroupMatchExact)
				So(errf, ShouldBeNil)
				So(lct, ShouldResemble,
					map[string]time.Time{"rep1suffix": job1.EndTime})

				lct, errf = s.GetLastCompletionTimeByRepGroup("rep",
					jobqueue.RepGroupMatchPrefix)
				So(errf, ShouldBeNil)
				So(lct, ShouldResemble, map[string]time.Time{
					"rep1suffix": job1.EndTime,
					"rep2suffix": job3.EndTime,
				})

				lct, errf = s.GetLastCompletionTimeByRepGroup("missing",
					jobqueue.RepGroupMatchExact)
				So(errf, ShouldBeNil)
				So(lct, ShouldResemble, map[string]time.Time{})
			})

			Convey("You can remove jobs", func() {
				err := s.RemoveJobs(job1)
				So(err, ShouldBeNil)

				jobs, err := s.FindJobsByRepGroupSuffix("suffix")
				So(err, ShouldBeNil)
				So(jobs, ShouldResemble, []*jobqueue.Job{job2})
			})
		})

		Convey("Setting pretendSubmissions to a file description writes new jobs to it", func() {
			pr, pw, err := os.Pipe()
			So(err, ShouldBeNil)

			defer pr.Close()

			restorePretend := setPretendSubmissionsForTest(strconv.FormatUint(uint64(pw.Fd()), 10))
			defer restorePretend()

			var (
				payloads [][]*jobqueue.Job
				jch      = make(chan error)
			)

			go func() {
				var decodeErr error

				payloads, decodeErr = decodePretendJobPayloads(pr)
				jch <- decodeErr
			}()

			s, err := New(settings)
			So(err, ShouldBeNil)
			So(pw.Close(), ShouldBeNil)

			job1 := s.NewJob("cmd1", "rep1suffix", "req1", "depg1", "dep1", nil)
			job2 := s.NewJob("cmd2", "rep2suffix", "req2", "depg2", "dep2", nil)

			err = s.SubmitJobs([]*jobqueue.Job{job1, job2})
			So(err, ShouldBeNil)

			So(s.Disconnect(), ShouldBeNil)

			So(<-jch, ShouldBeNil)
			So(payloads, ShouldHaveLength, 1)
			So(payloads[0], ShouldResemble, []*jobqueue.Job{job1, job2})
		})

		Convey("Setting pretendSubmissions to a file descriptor keeps the duplicate close-on-exec", func() {
			pr, pw, err := os.Pipe()
			So(err, ShouldBeNil)

			defer pr.Close()

			restorePretend := setPretendSubmissionsForTest(strconv.FormatUint(uint64(pw.Fd()), 10))
			defer restorePretend()

			s, err := New(settings)
			So(err, ShouldBeNil)

			defer func() {
				So(s.Disconnect(), ShouldBeNil)
			}()

			So(pw.Close(), ShouldBeNil)

			pjq, ok := s.jq.(*pretendJobqueue)
			So(ok, ShouldBeTrue)

			output, ok := pjq.output.(*os.File)
			So(ok, ShouldBeTrue)

			flags, err := fdFlags(output)
			So(err, ShouldBeNil)
			So(flags&syscall.FD_CLOEXEC, ShouldEqual, syscall.FD_CLOEXEC)
		})
	})
}

func TestPretendGetIncompleteByRepGroupEmptyRepGroup(t *testing.T) {
	Convey("Given a pretend jobqueue with mixed complete and incomplete jobs", t, func() {
		p := newPretendJobqueue()
		p.jobBuffer = []*jobqueue.Job{
			{RepGroup: "rg1", State: jobqueue.JobStateReady},
			{RepGroup: "rg2", State: jobqueue.JobStateRunning},
			{RepGroup: "rg3", State: jobqueue.JobStateComplete},
		}

		Convey("GetIncompleteByRepGroupMatch with empty repgroup returns all incomplete jobs", func() {
			jobs, err := p.GetIncompleteByRepGroupMatch("", jobqueue.RepGroupMatchExact,
				0, "", false, false)
			So(err, ShouldBeNil)
			So(jobs, ShouldResemble, []*jobqueue.Job{p.jobBuffer[0], p.jobBuffer[1]})
		})
	})
}

func TestPretendGetByRepGroupEmptyRepGroup(t *testing.T) {
	Convey("Given a pretend jobqueue", t, func() {
		p := newPretendJobqueue()

		Convey("GetByRepGroupMatch with empty repgroup returns ErrBadRequest", func() {
			jobs, err := p.GetByRepGroupMatch("", jobqueue.RepGroupMatchExact,
				0, "", false, false)
			So(jobs, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: "GetByRepGroupMatch", Err: jobqueue.ErrBadRequest})
		})

		Convey("GetByRepGroup with empty repgroup returns ErrBadRequest", func() {
			jobs, err := p.GetByRepGroup("", false, 0, "", false, false)
			So(jobs, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: "GetByRepGroupMatch", Err: jobqueue.ErrBadRequest})
		})
	})
}

type waitForJobsResult struct {
	jobs []*jobqueue.Job
	err  error
}

type waitForRunningResult struct {
	job *jobqueue.Job
	err error
}

func TestSchedulerPretendNewMethods(t *testing.T) {
	Convey("Given scheduler settings in pretend mode", t, func() {
		restorePretend := setPretendSubmissionsForTest(" ")
		defer restorePretend()

		ctx := context.Background()
		settings := SchedulerSettings{
			Deployment: testDeployment,
			Timeout:    10 * time.Second,
			Logger:     log15.New(),
		}

		Convey("SubmitJobsAndReturnIDs records delayed jobs and returns keys", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)

			job1 := s.NewJob("cmd-e1-ids-1", "rg-e1-ids-1", "req-e1-ids-1", "", "", nil)
			job2 := s.NewJob("cmd-e1-ids-2", "rg-e1-ids-2", "req-e1-ids-2", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job1, job2},
				SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(keys, ShouldResemble, []string{job1.Key(), job2.Key()})
			So(job1.State, ShouldEqual, jobqueue.JobStateDelayed)
			So(job2.State, ShouldEqual, jobqueue.JobStateDelayed)

			submitted := s.SubmittedJobs()
			So(submitted, ShouldHaveLength, 2)
			So(submitted[0], ShouldEqual, job1)
			So(submitted[1], ShouldEqual, job2)
		})

		Convey("SubmitJobsAndWait records complete jobs and returns them", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)

			job1 := s.NewJob("cmd-e1-wait-1", "rg-e1-wait-1", "req-e1-wait-1", "", "", nil)
			job2 := s.NewJob("cmd-e1-wait-2", "rg-e1-wait-2", "req-e1-wait-2", "", "", nil)

			got, err := s.SubmitJobsAndWait(ctx, []*jobqueue.Job{job1, job2},
				SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(got, ShouldHaveLength, 2)
			So(got[0], ShouldEqual, job1)
			So(got[1], ShouldEqual, job2)
			So(got[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(got[0].Exited, ShouldBeTrue)
			So(got[0].Exitcode, ShouldEqual, 0)
			So(got[1].State, ShouldEqual, jobqueue.JobStateComplete)
			So(got[1].Exited, ShouldBeTrue)
			So(got[1].Exitcode, ShouldEqual, 0)

			submitted := s.SubmittedJobs()
			So(submitted, ShouldHaveLength, 2)
			So(submitted[0], ShouldEqual, job1)
			So(submitted[1], ShouldEqual, job2)
		})

		Convey("GetJobByKey returns recorded jobs and typed missing-key errors", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)

			job := s.NewJob("cmd-e1-get", "rg-e1-get", "req-e1-get", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)

			got, err := s.GetJobByKey(keys[0], false, false)
			So(err, ShouldBeNil)
			So(got, ShouldEqual, job)

			got, err = s.GetJobByKey(missingSchedulerJobKey, false, false)
			So(got, ShouldBeNil)

			var jqErr jobqueue.Error

			ok := errors.As(err, &jqErr)
			So(ok, ShouldBeTrue)
			So(jqErr, ShouldResemble, jobqueue.Error{
				Op:   getJobByKeyOp,
				Item: missingSchedulerJobKey,
				Err:  jobqueue.ErrBadJob,
			})
		})

		Convey("WaitForRunning marks a recorded delayed job running", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)

			job := s.NewJob("cmd-e1-running", "rg-e1-running", "req-e1-running", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(job.State, ShouldEqual, jobqueue.JobStateDelayed)

			got, err := s.WaitForRunning(ctx, keys[0], time.Millisecond)
			So(err, ShouldBeNil)
			So(got, ShouldEqual, job)
			So(got.State, ShouldEqual, jobqueue.JobStateRunning)
		})

		Convey("WaitForJobs completes and returns a recorded delayed job", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)

			job := s.NewJob("cmd-e1-wait-for-jobs", "rg-e1-wait-for-jobs",
				"req-e1-wait-for-jobs", "", "", nil)

			keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{job}, SubmitJobsOptions{})
			So(err, ShouldBeNil)
			So(job.State, ShouldEqual, jobqueue.JobStateDelayed)

			got, err := s.WaitForJobs(ctx, keys[0])
			So(err, ShouldBeNil)
			So(got, ShouldHaveLength, 1)
			So(got[0], ShouldEqual, job)
			So(got[0].State, ShouldEqual, jobqueue.JobStateComplete)
			So(got[0].Exited, ShouldBeTrue)
			So(got[0].Exitcode, ShouldEqual, 0)
		})

		Convey("WaitForJobs returns cancellation without completing recorded pending jobs", func() {
			s, err := New(settings)
			So(err, ShouldBeNil)

			jobs := []*jobqueue.Job{
				s.NewJob("cmd-e1-canceled-delayed", "rg-e1-canceled-delayed",
					"req-e1-canceled", "", "", nil),
				s.NewJob("cmd-e1-canceled-ready", "rg-e1-canceled-ready",
					"req-e1-canceled", "", "", nil),
				s.NewJob("cmd-e1-canceled-reserved", "rg-e1-canceled-reserved",
					"req-e1-canceled", "", "", nil),
			}

			keys, err := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})
			So(err, ShouldBeNil)

			jobs[1].State = jobqueue.JobStateReady
			jobs[2].State = jobqueue.JobStateReserved

			waitCtx, cancel := context.WithCancel(ctx)
			cancel()

			got, err := s.WaitForJobs(waitCtx, keys...)
			So(got, ShouldHaveLength, 0)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(err.Error(), ShouldContainSubstring, "unfinished job keys: "+keys[0])
			So(err.Error(), ShouldContainSubstring, keys[1])
			So(err.Error(), ShouldContainSubstring, keys[2])
			So(jobs[0].State, ShouldEqual, jobqueue.JobStateDelayed)
			So(jobs[1].State, ShouldEqual, jobqueue.JobStateReady)
			So(jobs[2].State, ShouldEqual, jobqueue.JobStateReserved)
			So(jobs[0].Exited, ShouldBeFalse)
			So(jobs[1].Exited, ShouldBeFalse)
			So(jobs[2].Exited, ShouldBeFalse)
		})

		Convey("Submit paths write pretend JSON exactly once per call", func() {
			returnIDJobs, returnIDPayloads, err := collectPretendJSONPayloads(settings,
				func(s *Scheduler) ([]*jobqueue.Job, error) {
					jobs := []*jobqueue.Job{
						s.NewJob("cmd-e1-json-ids", "rg-e1-json-ids", "req-e1-json-ids", "", "", nil),
					}

					_, submitErr := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})

					return jobs, submitErr
				})
			So(err, ShouldBeNil)
			So(returnIDPayloads, ShouldHaveLength, 1)
			So(returnIDPayloads[0], ShouldHaveLength, 1)
			So(returnIDPayloads[0][0].Key(), ShouldEqual, returnIDJobs[0].Key())
			So(returnIDPayloads[0][0].State, ShouldEqual, jobqueue.JobStateDelayed)

			waitJobs, waitPayloads, err := collectPretendJSONPayloads(settings,
				func(s *Scheduler) ([]*jobqueue.Job, error) {
					jobs := []*jobqueue.Job{
						s.NewJob("cmd-e1-json-wait", "rg-e1-json-wait", "req-e1-json-wait", "", "", nil),
					}

					_, submitErr := s.SubmitJobsAndWait(ctx, jobs, SubmitJobsOptions{})

					return jobs, submitErr
				})
			So(err, ShouldBeNil)
			So(waitPayloads, ShouldHaveLength, 1)
			So(waitPayloads[0], ShouldHaveLength, 1)
			So(waitPayloads[0][0].Key(), ShouldEqual, waitJobs[0].Key())
			So(waitPayloads[0][0].State, ShouldEqual, jobqueue.JobStateComplete)
		})
	})
}

func collectPretendJSONPayloads(settings SchedulerSettings,
	submit func(*Scheduler) ([]*jobqueue.Job, error)) (
	[]*jobqueue.Job, [][]*jobqueue.Job, error) {
	// Capture the pretend JSON via a temp file rather than a pipe: the scheduler
	// is handed the file's fd to write to, but we read the result back by path
	// (a fresh fd), so no read fd is shared. Under heavy parallel-test load a
	// shared pipe read fd could go bad ("read |0: bad file descriptor") when its
	// number got reused.
	f, err := os.CreateTemp("", "wr_pretend_json")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(f.Name())

	restorePretend := setPretendSubmissionsForTest(strconv.FormatUint(uint64(f.Fd()), 10))
	defer restorePretend()

	s, err := New(settings)
	if err != nil {
		f.Close()

		return nil, nil, err
	}

	jobs, submitErr := submit(s)
	disconnectErr := s.Disconnect()
	closeErr := f.Close()

	r, openErr := os.Open(f.Name())
	if openErr != nil {
		return jobs, nil, errors.Join(submitErr, disconnectErr, closeErr, openErr)
	}
	defer r.Close()

	payloads, decodeErr := decodePretendJobPayloads(r)

	return jobs, payloads, errors.Join(submitErr, disconnectErr, closeErr, decodeErr)
}

func setPretendSubmissionsForTest(value string) func() {
	oldValue := PretendSubmissions
	PretendSubmissions = value

	return func() {
		PretendSubmissions = oldValue
	}
}

func decodePretendJobPayloads(r io.Reader) ([][]*jobqueue.Job, error) {
	decoder := json.NewDecoder(r)
	payloads := make([][]*jobqueue.Job, 0, 1)

	for {
		var jobs []*jobqueue.Job

		err := decoder.Decode(&jobs)
		if errors.Is(err, io.EOF) {
			return payloads, nil
		}

		if err != nil {
			return nil, err
		}

		payloads = append(payloads, jobs)
	}
}

func fdFlags(f *os.File) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFD, 0)
	if errno != 0 {
		return 0, errno
	}

	return int(flags), nil
}
