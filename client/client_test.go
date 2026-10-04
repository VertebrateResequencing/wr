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
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
	"go.nanomsg.org/mangos/v3"
)

const (
	missingSchedulerJobKey             = "missing-key"
	schedulerQueueRequirementKey       = "scheduler_queue"
	schedulerQueuesAvoidRequirementKey = "scheduler_queues_avoid"
	testDeployment                     = "development"
	testSchedulerQueue                 = "short"
	testSchedulerQueuesAvoid           = "slow,big"
)

var (
	errSchedulerJobTimeout     = errors.New("timed out waiting for WaitForJobs")
	errSchedulerNoReservedJob  = errors.New("reserve returned no job")
	errSchedulerNotLocalConfig = errors.New("test scheduler config is not local")
)

const (
	restartWaitTimeout = time.Second
	restartWaitPoll    = 100 * time.Millisecond
	restartRetryWait   = 200 * time.Millisecond
	restartLongRetry   = 30 * time.Second
	restartShortRetry  = 3 * time.Second
	restartMediumRetry = 6 * time.Second
	restartOutage      = 3500 * time.Millisecond
	restartDownTime    = 3 * restartWaitTimeout
	restartResultWait  = 30 * time.Second
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

		Convey("GetJobByKey fetches no stdout or stderr for a successful complete job", func() {
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
			So(stdout, ShouldEqual, "")

			stderr, err := stored.StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")
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

func TestSchedulerSubmitJobsDefaultsMissingRequirements(t *testing.T) {
	Convey("Given a running manager that schedules runners", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		config.RunnerCmd = "true '%s' '%s' '%s' '%s' %d %d"

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		// ClientMinRequestTimeout is deliberately left at its package default:
		// it is the floor that stops the real Add below - a DB write plus
		// scheduler processing - being given up on as a dead manager when the
		// box is loaded. Lowering it to a second here cost this test a spurious
		// 'receive time out' (.docs/bugfixes/260828-4.md BUG 2), and nothing
		// here asserts a timeout.
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

		job := &jobqueue.Job{
			Cmd:      "echo default missing requirements",
			RepGroup: "rg-default-missing-requirements",
			ReqGroup: "req-default-missing-requirements",
		}

		err = s.SubmitJobs([]*jobqueue.Job{job})
		So(err, ShouldBeNil)
		So(job.Requirements, ShouldResemble, DefaultRequirements())
		So(job.Override, ShouldEqual, 0)
		So(server.GetServerStats().Ready, ShouldEqual, 1)
	})
}

// restartableManager is a test manager that can be stopped the way `wr manager
// stop` does and started again from the same config, which writes a new token.
type restartableManager struct {
	t      *testing.T
	config jobqueue.ServerConfig
	server *jobqueue.Server
}

// newRestartableManager starts a manager that tells its clients to keep trying
// to reach it for retryTime. Its deployment is production so that a restart
// keeps its database (development wipes it on start); the client side still
// finds it through the development wr config PrepareWrConfig wrote.
func newRestartableManager(t *testing.T, retryTime time.Duration) (*restartableManager, func()) {
	t.Helper()

	config, d := clienttesting.PrepareWrConfig(t)
	config.Deployment = "production"
	config.Timings.RetryWait = restartRetryWait
	config.Timings.RetryTime = retryTime

	m := &restartableManager{t: t, config: config, server: clienttesting.Serve(t, config)}

	return m, func() {
		if m.server != nil {
			m.server.Stop(context.Background(), true)
		}

		d()
	}
}

func (m *restartableManager) token() []byte {
	token, err := os.ReadFile(m.config.TokenFile)
	So(err, ShouldBeNil)

	return token
}

// stop stops the manager cleanly and deletes its token file.
func (m *restartableManager) stop() {
	m.server.Stop(context.Background(), true)
	m.server = nil

	So(os.Remove(m.config.TokenFile), ShouldBeNil)
}

// stopSeenBy stops the manager like stop, then waits until each of clients has
// seen its connection to the manager drop. A request a client sends before
// then goes out on that dropped connection, so it waits for its reply until
// the reply deadline (see the package doc) instead of finding the manager
// down.
func (m *restartableManager) stopSeenBy(clients ...*jobqueue.Client) {
	m.stop()

	seen := make([]bool, len(clients))

	var wg sync.WaitGroup

	for i, jq := range clients {
		wg.Go(func() { seen[i] = waitUntilConnectionDropped(jq) })
	}

	wg.Wait()

	So(seen, ShouldNotContain, false)
}

// waitUntilConnectionDropped pings the stopped manager through jq until a ping
// cannot be sent because jq has no connection left, reporting false if that
// does not happen within restartResultWait.
func waitUntilConnectionDropped(jq *jobqueue.Client) bool {
	limit := time.Now().Add(restartResultWait)

	for time.Now().Before(limit) {
		if _, err := jq.Ping(restartWaitPoll); errors.Is(err, mangos.ErrSendTimeout) {
			return true
		}
	}

	return false
}

func (m *restartableManager) start() {
	m.server = clienttesting.Serve(m.t, m.config)
}

func TestSchedulerWaitForRunningAcrossManagerRestart(t *testing.T) {
	Convey("Given a WaitForRunning in progress on a job that has not started", t, func() {
		ctx := context.Background()

		Convey("it returns the job once it starts after a clean restart with a new token", func() {
			w, cleanup := startRestartedWait(t, restartLongRetry)
			defer cleanup()

			oldToken := w.manager.token()

			w.stopManager()
			So(receiveWaitForRunningResult(w.done, restartDownTime).err, ShouldEqual, errSchedulerJobTimeout)

			w.manager.start()
			So(w.manager.token(), ShouldNotResemble, oldToken)

			driver, err := jobqueue.ConnectUsingConfig(ctx, testDeployment, 10*time.Second)
			So(err, ShouldBeNil)

			defer driver.Disconnect() //nolint:errcheck

			started, err := reserveAndStartSchedulerJob(driver)
			So(err, ShouldBeNil)

			result := receiveWaitForRunningResult(w.done, restartResultWait)
			So(result.err, ShouldBeNil)
			So(result.job, ShouldNotBeNil)
			So(result.job.Key(), ShouldEqual, w.key)
			So(result.job.State, ShouldEqual, jobqueue.JobStateRunning)

			So(driver.Archive(started, &jobqueue.JobEndState{Exited: true, EndTime: time.Now()}), ShouldBeNil)
		})

		Convey("it rides out repeated outages that together, but not each, last longer than RetryTime", func() {
			w, cleanup := startRestartedWait(t, restartMediumRetry)
			defer cleanup()

			for range 2 {
				w.stopManager()
				So(receiveWaitForRunningResult(w.done, restartOutage).err, ShouldEqual, errSchedulerJobTimeout)

				w.manager.start()
				So(receiveWaitForRunningResult(w.done, restartWaitTimeout).err, ShouldEqual, errSchedulerJobTimeout)
			}

			driver, err := jobqueue.ConnectUsingConfig(ctx, testDeployment, 10*time.Second)
			So(err, ShouldBeNil)

			defer driver.Disconnect() //nolint:errcheck

			started, err := reserveAndStartSchedulerJob(driver)
			So(err, ShouldBeNil)

			result := receiveWaitForRunningResult(w.done, restartResultWait)
			So(result.err, ShouldBeNil)
			So(result.job, ShouldNotBeNil)
			So(result.job.State, ShouldEqual, jobqueue.JobStateRunning)

			So(driver.Archive(started, &jobqueue.JobEndState{Exited: true, EndTime: time.Now()}), ShouldBeNil)
		})

		Convey("it returns the last error after about the manager's RetryTime if the manager stays down", func() {
			w, cleanup := startRestartedWait(t, restartShortRetry)
			defer cleanup()

			stoppedAt := time.Now()

			w.stopManager()

			result := receiveWaitForRunningResult(w.done, restartResultWait)
			elapsed := time.Since(stoppedAt)

			So(result.job, ShouldBeNil)
			So(errors.Is(result.err, mangos.ErrSendTimeout), ShouldBeTrue)
			So(elapsed, ShouldBeGreaterThanOrEqualTo, restartShortRetry)
			So(elapsed, ShouldBeLessThan, restartShortRetry+3*restartWaitTimeout)
		})

		Convey("cancelling its context while the manager is down returns the context's error promptly", func() {
			w, cleanup := startRestartedWait(t, restartLongRetry)
			defer cleanup()

			w.stopManager()
			So(receiveWaitForRunningResult(w.done, restartDownTime).err, ShouldEqual, errSchedulerJobTimeout)

			cancelledAt := time.Now()

			w.cancel()

			result := receiveWaitForRunningResult(w.done, restartResultWait)
			So(result.job, ShouldBeNil)
			So(errors.Is(result.err, context.Canceled), ShouldBeTrue)
			So(time.Since(cancelledAt), ShouldBeLessThan, restartWaitTimeout+time.Second)

			Convey("and a WaitForRunning called while it is down rides it out too, until its context is cancelled", func() {
				calledCtx, cancelCalled := context.WithCancel(ctx)
				defer cancelCalled()

				called := waitForRunningAsync(calledCtx, w.scheduler, w.key, restartWaitPoll)
				So(receiveWaitForRunningResult(called, restartDownTime).err, ShouldEqual, errSchedulerJobTimeout)

				cancelledAt = time.Now()

				cancelCalled()

				result = receiveWaitForRunningResult(called, restartResultWait)
				So(result.job, ShouldBeNil)
				So(errors.Is(result.err, context.Canceled), ShouldBeTrue)
				So(time.Since(cancelledAt), ShouldBeLessThan, restartWaitTimeout+time.Second)
			})
		})
	})
}

// startRestartedWait starts a manager with the given RetryTime, submits a job
// and starts a WaitForRunning on it with a Scheduler whose Timeout is
// restartWaitTimeout and whose polls stopManager can hold off, confirming the
// wait is polling before returning it.
func startRestartedWait(t *testing.T, retryTime time.Duration) (*restartedWait, func()) {
	t.Helper()

	m, cleanupManager := newRestartableManager(t, retryTime)

	s, err := New(SchedulerSettings{Deployment: testDeployment, Timeout: restartWaitTimeout, Logger: log15.New()})
	So(err, ShouldBeNil)

	keys, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{
		s.NewJob("echo restart", "rg-restart", "req-restart", "", "", nil),
	}, SubmitJobsOptions{})
	So(err, ShouldBeNil)

	gated := &pollGatedJobqueue{Client: jobqueueClients(s)[0]}
	s.jq = gated

	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan waitForRunningResult, 1)
	returned := make(chan struct{})

	go func() {
		job, errw := s.WaitForRunning(waitCtx, keys[0], restartWaitPoll)
		done <- waitForRunningResult{job: job, err: errw}

		close(returned)
	}()

	So(receiveWaitForRunningResult(done, 3*restartWaitPoll).err, ShouldEqual, errSchedulerJobTimeout)

	return &restartedWait{manager: m, scheduler: s, jq: gated, key: keys[0], cancel: cancel, done: done}, func() {
		cancel()

		select {
		case <-returned:
		case <-time.After(restartResultWait):
		}

		s.Disconnect() //nolint:errcheck
		cleanupManager()
	}
}

func TestSchedulerRequestsAcrossManagerRestart(t *testing.T) {
	Convey("Given a Scheduler connected to a manager that can be restarted", t, func() {
		ctx := context.Background()

		Convey("a SubmitJobs and a GetJobByKey made while it is down return once it restarts with a new token", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			logged := &recordedLogs{}
			logger := log15.New()
			logger.SetHandler(logged)

			s, err := New(SchedulerSettings{Deployment: testDeployment, Timeout: restartWaitTimeout, Logger: logger})
			So(err, ShouldBeNil)

			defer s.Disconnect() //nolint:errcheck

			queued := s.NewJob("echo queued before the outage", "rg-requests-restart", "req-requests-restart", "", "", nil)
			So(s.SubmitJobs([]*jobqueue.Job{queued}), ShouldBeNil)

			oldToken := m.token()

			m.stopSeenBy(jobqueueClients(s)...)

			added := s.NewJob("echo added during the outage", "rg-requests-restart", "req-requests-restart", "", "", nil)
			submitted := callAsync(func() (*jobqueue.Job, error) {
				return nil, s.SubmitJobs([]*jobqueue.Job{added})
			})
			got := callAsync(func() (*jobqueue.Job, error) {
				return s.GetJobByKey(queued.Key(), false, false)
			})

			So(receiveWaitForRunningResult(submitted, restartDownTime).err, ShouldEqual, errSchedulerJobTimeout)
			So(receiveWaitForRunningResult(got, 0).err, ShouldEqual, errSchedulerJobTimeout)

			m.start()
			So(m.token(), ShouldNotResemble, oldToken)

			So(receiveWaitForRunningResult(submitted, restartResultWait).err, ShouldBeNil)

			result := receiveWaitForRunningResult(got, restartResultWait)
			So(result.err, ShouldBeNil)
			So(result.job, ShouldNotBeNil)
			So(result.job.Key(), ShouldEqual, queued.Key())

			So(logged.count(log15.LvlWarn, "manager unreachable; retrying request"), ShouldBeGreaterThanOrEqualTo, 2)
			So(logged.count(log15.LvlInfo, "manager reachable again; retried request answered"), ShouldEqual, 2)

			driver, err := jobqueue.ConnectUsingConfig(ctx, testDeployment, 10*time.Second)
			So(err, ShouldBeNil)

			defer driver.Disconnect() //nolint:errcheck

			jobs, err := driver.GetByRepGroup("rg-requests-restart", false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(jobKeys(jobs), ShouldResemble, map[string]int{queued.Key(): 1, added.Key(): 1})
		})

		Convey("a SubmitJobs made while it stays down fails after about the manager's RetryTime", func() {
			m, cleanup := newRestartableManager(t, restartShortRetry)
			defer cleanup()

			s := newRestartScheduler()
			defer s.Disconnect() //nolint:errcheck

			m.stopSeenBy(jobqueueClients(s)...)

			calledAt := time.Now()
			err := s.SubmitJobs([]*jobqueue.Job{
				s.NewJob("echo never added", "rg-requests-down", "req-requests-down", "", "", nil),
			})
			elapsed := time.Since(calledAt)

			So(errors.Is(err, mangos.ErrSendTimeout), ShouldBeTrue)
			So(elapsed, ShouldBeGreaterThanOrEqualTo, restartShortRetry)
			So(elapsed, ShouldBeLessThan, restartShortRetry+3*restartWaitTimeout)
		})

		Convey("a WaitForJobs and a SubmitJobsAndWait made while it is down end soon after their ctx is cancelled", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			s := newRestartScheduler()
			defer s.Disconnect() //nolint:errcheck

			queued := s.NewJob("echo queued before the outage", "rg-requests-cancel", "req-requests-cancel", "", "", nil)
			So(s.SubmitJobs([]*jobqueue.Job{queued}), ShouldBeNil)

			m.stopSeenBy(jobqueueClients(s)...)

			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			waited := waitForJobsAsync(waitCtx, s, queued.Key())
			added := submitJobsAndWaitAsync(waitCtx, s, []*jobqueue.Job{
				s.NewJob("echo added during the outage", "rg-requests-cancel", "req-requests-cancel", "", "", nil),
			}, SubmitJobsOptions{})

			So(receiveWaitForJobsResult(waited, restartDownTime).err, ShouldEqual, errSchedulerJobTimeout)
			So(receiveWaitForJobsResult(added, 0).err, ShouldEqual, errSchedulerJobTimeout)

			cancelledAt := time.Now()

			cancel()

			So(errors.Is(receiveWaitForJobsResult(waited, restartResultWait).err, context.Canceled), ShouldBeTrue)
			So(errors.Is(receiveWaitForJobsResult(added, restartResultWait).err, context.Canceled), ShouldBeTrue)
			So(time.Since(cancelledAt), ShouldBeLessThan, restartWaitTimeout+time.Second)
		})

		Convey("each context-taking call made while it is down keeps trying until its ctx is cancelled, "+
			"while a plain call keeps trying", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			calls := contextSchedulerCalls()
			schedulers := make(map[string]*Scheduler, len(calls))

			for name := range calls {
				schedulers[name] = newRestartScheduler()
				defer schedulers[name].Disconnect() //nolint:errcheck
			}

			plainScheduler := newRestartScheduler()
			defer plainScheduler.Disconnect() //nolint:errcheck

			m.stopSeenBy(jobqueueClients(append(slices.Collect(maps.Values(schedulers)), plainScheduler)...)...)

			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			results := make(map[string]<-chan waitForRunningResult, len(calls))
			for name, call := range calls {
				results[name] = callAsync(func() (*jobqueue.Job, error) { return nil, call(callCtx, schedulers[name]) })
			}

			plain := callAsync(func() (*jobqueue.Job, error) {
				_, err := plainScheduler.FindJobsByRepGroupSuffix("ctx")

				return nil, err
			})

			time.Sleep(restartWaitTimeout + restartWaitTimeout/2)

			stillTrying := make(map[string]bool, len(calls))
			for name, result := range results {
				stillTrying[name] = errors.Is(receiveWaitForRunningResult(result, 0).err, errSchedulerJobTimeout)
			}

			cancelledAt := time.Now()

			cancel()

			endedWithCtxErr := make(map[string]bool, len(calls))
			for name, result := range results {
				endedWithCtxErr[name] = errors.Is(receiveWaitForRunningResult(result, restartResultWait).err,
					context.Canceled)
			}

			cancelTook := time.Since(cancelledAt)

			allTrue := make(map[string]bool, len(calls))
			for name := range calls {
				allTrue[name] = true
			}

			So(stillTrying, ShouldResemble, allTrue)
			So(endedWithCtxErr, ShouldResemble, allTrue)
			So(cancelTook, ShouldBeLessThan, restartWaitTimeout+time.Second)

			So(receiveWaitForRunningResult(plain, 0).err, ShouldEqual, errSchedulerJobTimeout)

			m.start()

			So(receiveWaitForRunningResult(plain, restartResultWait).err, ShouldBeNil)
		})

		Convey("an error that is the manager's answer is returned at once", func() {
			_, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			s := newRestartScheduler()
			defer s.Disconnect() //nolint:errcheck

			calledAt := time.Now()
			job, err := s.GetJobByKey(missingSchedulerJobKey, false, false)

			So(job, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: getJobByKeyOp, Item: missingSchedulerJobKey, Err: jobqueue.ErrBadJob})
			So(time.Since(calledAt), ShouldBeLessThan, restartWaitTimeout)
		})

		Convey("a plain jobqueue client, as wr's commands use, still fails after its timeout", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			jq, err := jobqueue.ConnectUsingConfig(ctx, testDeployment, restartWaitTimeout)
			So(err, ShouldBeNil)

			defer jq.Disconnect() //nolint:errcheck

			m.stopSeenBy(jq)

			calledAt := time.Now()
			_, _, err = jq.Add([]*jobqueue.Job{{Cmd: "echo plain", Cwd: "/tmp", RepGroup: "rg-plain"}}, nil, true)

			So(errors.Is(err, mangos.ErrSendTimeout), ShouldBeTrue)
			So(time.Since(calledAt), ShouldBeLessThan, restartWaitTimeout+time.Second)
		})
	})
}

// jobqueueClients returns the jobqueue clients schedulers talk to the manager
// with.
func jobqueueClients(schedulers ...*Scheduler) []*jobqueue.Client {
	clients := make([]*jobqueue.Client, 0, len(schedulers))

	for _, s := range schedulers {
		jq, ok := s.jq.(*jobqueue.Client)
		So(ok, ShouldBeTrue)

		clients = append(clients, jq)
	}

	return clients
}

// callAsync runs call in the background, returning a channel that gets what it
// returns.
func callAsync(call func() (*jobqueue.Job, error)) <-chan waitForRunningResult {
	done := make(chan waitForRunningResult, 1)

	go func() {
		job, err := call()
		done <- waitForRunningResult{job: job, err: err}
	}()

	return done
}

// jobKeys counts how many of jobs have each key.
func jobKeys(jobs []*jobqueue.Job) map[string]int {
	counts := make(map[string]int, len(jobs))

	for _, job := range jobs {
		counts[job.Key()]++
	}

	return counts
}

// newRestartScheduler returns a Scheduler for a restartableManager, with a
// Timeout of restartWaitTimeout.
func newRestartScheduler() *Scheduler {
	s, err := New(SchedulerSettings{Deployment: testDeployment, Timeout: restartWaitTimeout, Logger: log15.New()})
	So(err, ShouldBeNil)

	return s
}

// contextSchedulerCalls returns, by name, a call of each Scheduler method that
// takes a ctx and makes a single request of the manager.
func contextSchedulerCalls() map[string]func(context.Context, *Scheduler) error {
	newJob := func(s *Scheduler) *jobqueue.Job {
		return s.NewJob("echo ctx call", "rg-requests-ctx", "req-requests-ctx", "", "", nil)
	}

	return map[string]func(context.Context, *Scheduler) error{
		"SubmitJobsContext": func(ctx context.Context, s *Scheduler) error {
			return s.SubmitJobsContext(ctx, []*jobqueue.Job{newJob(s)})
		},
		"SubmitJobsAndReturnIDsContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.SubmitJobsAndReturnIDsContext(ctx, []*jobqueue.Job{newJob(s)}, SubmitJobsOptions{})

			return err
		},
		"GetJobByKeyContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.GetJobByKeyContext(ctx, newJob(s).Key(), false, false)

			return err
		},
		"FindJobsByRepGroupSuffixContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.FindJobsByRepGroupSuffixContext(ctx, "ctx")

			return err
		},
		"FindJobsByRepGroupPrefixAndStateContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.FindJobsByRepGroupPrefixAndStateContext(ctx, "rg-", jobqueue.JobStateReady)

			return err
		},
		"FindIncompleteJobsByRepGroupContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.FindIncompleteJobsByRepGroupContext(ctx, "rg-requests-ctx", jobqueue.RepGroupMatchExact)

			return err
		},
		"FindIncompleteJobsByRepGroupAndStateContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.FindIncompleteJobsByRepGroupAndStateContext(ctx, "rg-requests-ctx",
				jobqueue.RepGroupMatchExact, jobqueue.JobStateReady)

			return err
		},
		"GetLastCompletionTimeByRepGroupContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.GetLastCompletionTimeByRepGroupContext(ctx, "rg-requests-ctx", jobqueue.RepGroupMatchExact)

			return err
		},
		"GetSchedulerAlertsContext": func(ctx context.Context, s *Scheduler) error {
			_, err := s.GetSchedulerAlertsContext(ctx)

			return err
		},
		"KillJobsContext": func(ctx context.Context, s *Scheduler) error {
			return s.KillJobsContext(ctx, newJob(s))
		},
		"RemoveJobsContext": func(ctx context.Context, s *Scheduler) error {
			return s.RemoveJobsContext(ctx, newJob(s))
		},
	}
}

func TestSchedulerGetSchedulerAlertsAcrossManagerRestart(t *testing.T) {
	Convey("Given a Scheduler connected to a manager that can be restarted", t, func() {
		Convey("GetSchedulerAlerts works after a clean restart with a new token, with no other call between", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			s := newRestartScheduler()
			defer s.Disconnect() //nolint:errcheck

			_, err := s.GetSchedulerAlerts()
			So(err, ShouldBeNil)

			oldToken := m.token()

			m.stop()
			m.start()
			So(m.token(), ShouldNotResemble, oldToken)

			alerts, err := s.GetSchedulerAlerts()
			So(err, ShouldBeNil)
			So(alerts, ShouldNotBeNil)
		})

		Convey("a GetSchedulerAlerts made while it is down returns once it restarts with a new token", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			logged := &recordedLogs{}
			logger := log15.New()
			logger.SetHandler(logged)

			s, err := New(SchedulerSettings{Deployment: testDeployment, Timeout: restartWaitTimeout, Logger: logger})
			So(err, ShouldBeNil)

			defer s.Disconnect() //nolint:errcheck

			_, err = s.GetSchedulerAlerts()
			So(err, ShouldBeNil)

			m.stop()

			got := callAsync(func() (*jobqueue.Job, error) {
				_, errg := s.GetSchedulerAlerts()

				return nil, errg
			})

			So(receiveWaitForRunningResult(got, restartDownTime).err, ShouldEqual, errSchedulerJobTimeout)

			m.start()

			So(receiveWaitForRunningResult(got, restartResultWait).err, ShouldBeNil)
			So(logged.count(log15.LvlWarn, "manager unreachable; retrying request"), ShouldBeGreaterThanOrEqualTo, 1)
			So(logged.count(log15.LvlInfo, "manager reachable again; retried request answered"), ShouldEqual, 1)
		})

		Convey("a GetSchedulerAlerts made while it stays down fails after about the manager's RetryTime", func() {
			m, cleanup := newRestartableManager(t, restartShortRetry)
			defer cleanup()

			s := newRestartScheduler()
			defer s.Disconnect() //nolint:errcheck

			m.stop()

			calledAt := time.Now()
			_, err := s.GetSchedulerAlerts()
			elapsed := time.Since(calledAt)

			So(errors.Is(err, syscall.ECONNREFUSED), ShouldBeTrue)
			So(elapsed, ShouldBeGreaterThanOrEqualTo, restartShortRetry)
			So(elapsed, ShouldBeLessThan, restartShortRetry+3*restartWaitTimeout)
		})

		Convey("a plain jobqueue client, as wr status uses, fails at once while it is down, "+
			"and is refused after it restarts with a new token", func() {
			m, cleanup := newRestartableManager(t, restartLongRetry)
			defer cleanup()

			cfg := internal.ConfigLoadFromCurrentDir(context.Background(), testDeployment)
			jq, err := jobqueue.Connect(cfg.ManagerHost+":"+cfg.ManagerPort, cfg.ManagerCAFile,
				cfg.ManagerCertDomain, m.token(), restartWaitTimeout)
			So(err, ShouldBeNil)

			defer jq.Disconnect() //nolint:errcheck

			m.stop()

			calledAt := time.Now()
			_, err = jq.GetSchedulerAlerts()

			So(errors.Is(err, syscall.ECONNREFUSED), ShouldBeTrue)
			So(time.Since(calledAt), ShouldBeLessThan, restartWaitTimeout)

			m.start()

			_, err = jq.GetSchedulerAlerts()
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "401")
		})
	})
}

// pollGatedJobqueue is a Scheduler's real jobqueue client whose job lookups,
// the requests WaitForRunning polls with, can be held off by locking polls.
// Scheduler methods that need the concrete *jobqueue.Client, such as
// WaitForJobs and Kill, don't work on a Scheduler using it.
type pollGatedJobqueue struct {
	*jobqueue.Client

	polls sync.RWMutex
}

func (g *pollGatedJobqueue) GetByEssenceContext(ctx context.Context, je *jobqueue.JobEssence, getStd bool,
	getEnv bool) (*jobqueue.Job, error) {
	g.polls.RLock()
	defer g.polls.RUnlock()

	return g.Client.GetByEssenceContext(ctx, je, getStd, getEnv)
}

// restartedWait is a WaitForRunning in progress against a restartableManager.
type restartedWait struct {
	manager   *restartableManager
	scheduler *Scheduler
	jq        *pollGatedJobqueue
	key       string
	cancel    context.CancelFunc
	done      <-chan waitForRunningResult
}

// stopManager stops the manager between two of the wait's polls, and lets the
// wait poll again only once its client has seen its connection drop. A poll
// sent before then, or in flight as the manager stops, waits for its reply
// until the reply deadline (see the package doc) instead of finding the manager
// down.
func (w *restartedWait) stopManager() {
	w.jq.polls.Lock()
	defer w.jq.polls.Unlock()

	w.manager.stopSeenBy(w.jq.Client)
}

// recordedLogs is a log15.Handler that keeps the level and message of every
// record it is given.
type recordedLogs struct {
	mu      sync.Mutex
	records []log15.Record
}

func (r *recordedLogs) Log(record log15.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = append(r.records, record)

	return nil
}

// count returns how many records at lvl had msg.
func (r *recordedLogs) count(lvl log15.Lvl, msg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0

	for _, record := range r.records {
		if record.Lvl == lvl && record.Msg == msg {
			n++
		}
	}

	return n
}

func (w *waitForRunningSequenceJobqueue) GetByEssenceContext(_ context.Context, je *jobqueue.JobEssence,
	_ bool, _ bool) (*jobqueue.Job, error) {
	if je == nil || je.Key() == "" {
		return nil, jobqueue.Error{Op: getByEssenceOp, Err: jobqueue.ErrBadRequest}
	}

	key := je.Key()
	if key != w.job.Key() {
		return nil, jobqueue.Error{Op: getByEssenceOp, Item: key, Err: jobqueue.ErrBadJob}
	}

	call := int(w.calls.Add(1)) - 1
	if call < len(w.errs) && w.errs[call] != nil {
		return nil, w.errs[call]
	}

	if call >= len(w.states) {
		call = len(w.states) - 1
	}

	w.job.State = w.states[call]

	return w.job, nil
}

func mapPointer(m map[string]string) uintptr {
	return reflect.ValueOf(m).Pointer()
}

// jobHasConfiguredQueues reports whether job carries both the configured
// scheduler_queue and scheduler_queues_avoid test values in its Requirements.
func jobHasConfiguredQueues(job *jobqueue.Job) bool {
	other := job.Requirements.Other

	return other[schedulerQueuesAvoidRequirementKey] == testSchedulerQueuesAvoid &&
		other[schedulerQueueRequirementKey] == testSchedulerQueue
}

func TestSchedulerNewJobQueuesAvoid(t *testing.T) {
	Convey("Given a Scheduler configured only with queuesAvoid", t, func() {
		s := &Scheduler{queuesAvoid: testSchedulerQueuesAvoid}

		Convey("NewJob with nil req gives every job an independent queues_avoid map", func() {
			const n = 200

			maps := make([]map[string]string, 0, n)
			missing := 0

			for i := range n {
				job := s.NewJob(fmt.Sprintf("echo nil-%d", i), "rg", "results_frontend", "", "", nil)
				So(job.Override, ShouldEqual, 0)

				if job.Requirements.Other[schedulerQueuesAvoidRequirementKey] != testSchedulerQueuesAvoid {
					missing++
				}

				maps = append(maps, job.Requirements.Other)
			}

			So(missing, ShouldEqual, 0)

			// each nil-req job must have its own Other map (no aliasing that a
			// later mutation could corrupt).
			shared := 0

			for i := 1; i < len(maps); i++ {
				if mapPointer(maps[i]) == mapPointer(maps[0]) {
					shared++
				}
			}

			So(shared, ShouldEqual, 0)
		})

		Convey("NewJob reusing one req object keeps queues_avoid on every job", func() {
			const n = 200

			req := &jqs.Requirements{RAM: 1000, Time: time.Minute, Cores: 1, Disk: 1}
			missing := 0

			for i := range n {
				job := s.NewJob(fmt.Sprintf("echo reuse-%d", i), "rg", "results_frontend", "", "", req)
				if job.Requirements.Other[schedulerQueuesAvoidRequirementKey] != testSchedulerQueuesAvoid {
					missing++
				}
			}

			So(missing, ShouldEqual, 0)
		})
	})
}

func TestSchedulerNewJobDoesNotAliasCallerReq(t *testing.T) {
	Convey("Given a Scheduler with both a queue and queuesAvoid configured", t, func() {
		s := &Scheduler{queue: testSchedulerQueue, queuesAvoid: testSchedulerQueuesAvoid}

		Convey("NewJob called twice with one shared req returns independent Requirements", func() {
			req := &jqs.Requirements{RAM: 1000, Time: time.Minute, Cores: 1, Disk: 1}

			job1 := s.NewJob("echo one", "rg", "results_frontend", "", "", req)
			job2 := s.NewJob("echo two", "rg", "results_frontend", "", "", req)

			// the returned jobs must not alias each other or the caller's req:
			// each must get its own Requirements and its own Other map, so a
			// later mutation (or a concurrent NewJob) of one cannot corrupt the
			// other or the caller's shared req.
			So(job1.Requirements, ShouldNotPointTo, job2.Requirements)
			So(job1.Requirements, ShouldNotPointTo, req)
			So(job2.Requirements, ShouldNotPointTo, req)

			So(mapPointer(job1.Requirements.Other), ShouldNotEqual, mapPointer(job2.Requirements.Other))

			So(job1.Requirements.Other[schedulerQueuesAvoidRequirementKey], ShouldEqual, testSchedulerQueuesAvoid)
			So(job2.Requirements.Other[schedulerQueuesAvoidRequirementKey], ShouldEqual, testSchedulerQueuesAvoid)
			So(job1.Requirements.Other[schedulerQueueRequirementKey], ShouldEqual, testSchedulerQueue)
			So(job2.Requirements.Other[schedulerQueueRequirementKey], ShouldEqual, testSchedulerQueue)
		})
	})
}

func TestSchedulerNewJobSharedReqConcurrent(t *testing.T) {
	Convey("Given a Scheduler with both a queue and queuesAvoid configured", t, func() {
		s := &Scheduler{queue: testSchedulerQueue, queuesAvoid: testSchedulerQueuesAvoid}

		Convey("Many goroutines can share one req across concurrent NewJob calls", func() {
			const n = 200

			// a single req pointer, deliberately shared across every goroutine,
			// as a real caller building a batch of similar jobs would do.
			req := &jqs.Requirements{RAM: 1000, Time: time.Minute, Cores: 1, Disk: 1}

			jobs := make([]*jobqueue.Job, n)

			var wg sync.WaitGroup

			for i := range n {
				wg.Add(1)

				go func(i int) {
					defer wg.Done()

					jobs[i] = s.NewJob(fmt.Sprintf("echo conc-%d", i), "rg", "results_frontend", "", "", req)
				}(i)
			}

			wg.Wait()

			missing := 0

			for _, job := range jobs {
				if !jobHasConfiguredQueues(job) {
					missing++
				}
			}

			So(missing, ShouldEqual, 0)
		})
	})
}

func TestSchedulerSubmissionMethodsDefaultMissingRequirements(t *testing.T) {
	Convey("Scheduler submission methods default missing requirements", t, func() {
		s := &Scheduler{
			jq:          &pretendJobqueue{},
			queue:       testSchedulerQueue,
			queuesAvoid: testSchedulerQueuesAvoid,
		}
		configuredQueues := map[string]string{
			schedulerQueueRequirementKey:       testSchedulerQueue,
			schedulerQueuesAvoidRequirementKey: testSchedulerQueuesAvoid,
		}
		expectedRequirements := DefaultRequirements()
		expectedRequirements.Other = configuredQueues
		expectedRequirements.OtherSet = true

		legacyJob := &jobqueue.Job{Cmd: "echo legacy defaults"}
		So(s.SubmitJobs([]*jobqueue.Job{legacyJob}), ShouldBeNil)
		So(legacyJob.Requirements, ShouldResemble, expectedRequirements)
		So(legacyJob.Requirements.Other, ShouldResemble, configuredQueues)
		So(legacyJob.Override, ShouldEqual, 0)

		idJob := &jobqueue.Job{Cmd: "echo id defaults"}
		ids, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{idJob}, SubmitJobsOptions{})
		So(err, ShouldBeNil)
		So(ids, ShouldResemble, []string{idJob.Key()})
		So(idJob.Requirements, ShouldResemble, expectedRequirements)
		So(idJob.Requirements.Other, ShouldResemble, configuredQueues)
		So(idJob.Override, ShouldEqual, 0)

		waitJob := &jobqueue.Job{Cmd: "echo wait defaults"}
		done, err := s.SubmitJobsAndWait(context.Background(), []*jobqueue.Job{waitJob}, SubmitJobsOptions{})
		So(err, ShouldBeNil)
		So(done, ShouldResemble, []*jobqueue.Job{waitJob})
		So(waitJob.Requirements, ShouldResemble, expectedRequirements)
		So(waitJob.Requirements.Other, ShouldResemble, configuredQueues)
		So(waitJob.Override, ShouldEqual, 0)
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
		s := &Scheduler{cwd: cwd, queue: testSchedulerQueue, queuesAvoid: testSchedulerQueuesAvoid}

		Convey("JobDefaults maps Scheduler defaults into jobqueue defaults", func() {
			defaults := s.JobDefaults()

			So(defaults.Cwd, ShouldEqual, cwd)
			So(defaults.CwdMatters, ShouldBeTrue)
			So(defaults.SchedulerQueue, ShouldEqual, testSchedulerQueue)
			So(defaults.SchedulerQueuesAvoid, ShouldEqual, testSchedulerQueuesAvoid)
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
				schedulerQueueRequirementKey:       testSchedulerQueue,
				schedulerQueuesAvoidRequirementKey: testSchedulerQueuesAvoid,
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

func assertNilJobSubmissionError(err error, op string, index int) {
	var jqErr jobqueue.Error

	So(errors.As(err, &jqErr), ShouldBeTrue)
	So(jqErr, ShouldResemble, jobqueue.Error{
		Op:   op,
		Item: fmt.Sprintf("jobs[%d]", index),
		Err:  jobqueue.ErrBadRequest,
	})
	So(err.Error(), ShouldContainSubstring, fmt.Sprintf("job at index %d is nil", index))
}

func assertNilDependencySubmissionError(err error, op string, jobIndex, dependencyIndex int) {
	var jqErr jobqueue.Error

	path := fmt.Sprintf("jobs[%d].Dependencies[%d]", jobIndex, dependencyIndex)

	So(errors.As(err, &jqErr), ShouldBeTrue)
	So(jqErr, ShouldResemble, jobqueue.Error{
		Op:   op,
		Item: path,
		Err:  jobqueue.ErrBadRequest,
	})
	So(err.Error(), ShouldContainSubstring, path+" is nil")
}

func assertNilBehaviourSubmissionError(err error, op string, jobIndex, behaviourIndex int) {
	var jqErr jobqueue.Error

	path := fmt.Sprintf("jobs[%d].Behaviours[%d]", jobIndex, behaviourIndex)

	So(errors.As(err, &jqErr), ShouldBeTrue)
	So(jqErr, ShouldResemble, jobqueue.Error{
		Op:   op,
		Item: path,
		Err:  jobqueue.ErrBadRequest,
	})
	So(err.Error(), ShouldContainSubstring, path+" is nil")
}

type waitForRunningSequenceJobqueue struct {
	*pretendJobqueue
	job    *jobqueue.Job
	states []jobqueue.JobState
	// errs, if set, are returned instead of the job by the calls at the same
	// index that have a non-nil one.
	errs  []error
	calls atomic.Int64
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

		Convey("WaitForRunning returns a poll's error at once, its jobqueue client having ridden out any outage", func() {
			pollErrs := map[string]error{
				mangos.ErrSendTimeout.Error(): mangos.ErrSendTimeout,
				mangos.ErrRecvTimeout.Error(): mangos.ErrRecvTimeout,
				jobqueue.ErrClosedStop:        jobqueue.Error{Op: getByEssenceOp, Err: jobqueue.ErrClosedStop},
				jobqueue.ErrPermissionDenied:  jobqueue.Error{Op: getByEssenceOp, Err: jobqueue.ErrPermissionDenied},
				jobqueue.ErrBadRequest:        jobqueue.Error{Op: getByEssenceOp, Err: jobqueue.ErrBadRequest},
				mangos.ErrClosed.Error():      mangos.ErrClosed,
			}

			for want, pollErr := range pollErrs {
				s, jq := newWaitForRunningSequenceScheduler("c1-poll-error",
					jobqueue.JobStateReady, jobqueue.JobStateRunning)
				jq.errs = []error{nil, pollErr}

				got, err := s.WaitForRunning(ctx, jq.job.Key(), time.Millisecond)
				So(got, ShouldBeNil)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, want)
				So(jq.calls.Load(), ShouldEqual, 2)
			}
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
			So(stdout, ShouldEqual, "")

			stderr, err := result.jobs[0].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")

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

		Convey("WaitForJobs returns already terminal jobs, with output only for the buried one", func() {
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
			So(stdout, ShouldEqual, "")

			stderr, err := got[0].StdErr()
			So(err, ShouldBeNil)
			So(stderr, ShouldEqual, "")

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

			// a Scheduler rides out the manager being down for this long
			config.Timings.RetryTime = restartShortRetry

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
						So(info.Ready, ShouldEqual, 1)

						dependent, errg := s.FindIncompleteJobsByRepGroupAndState("rep",
							jobqueue.RepGroupMatchExact, jobqueue.JobStateDependent)
						So(errg, ShouldBeNil)
						So(dependent, ShouldHaveLength, 1)
						So(dependent[0].Key(), ShouldEqual, job2.Key())
						So(dependent[0].WaitingForDepGroups, ShouldResemble, []string{"b"})

						Convey("but you get an error if there are duplicates", func() {
							err = s.SubmitJobs([]*jobqueue.Job{job, job2})
							So(err, ShouldNotBeNil)
							So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
							So(err.Error(), ShouldEqual, "some of the added jobs were duplicates")

							info := server.GetServerStats()
							So(info.Ready, ShouldEqual, 1)

							dependent, errg = s.FindIncompleteJobsByRepGroupAndState("rep",
								jobqueue.RepGroupMatchExact, jobqueue.JobStateDependent)
							So(errg, ShouldBeNil)
							So(dependent, ShouldHaveLength, 1)
							So(dependent[0].Key(), ShouldEqual, job2.Key())
							So(dependent[0].WaitingForDepGroups, ShouldResemble, []string{"b"})
						})
					})

					Convey("which you can't add to the queue if the server stays down", func() {
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
				So(job.Requirements.Other, ShouldResemble, map[string]string{schedulerQueueRequirementKey: "foo"})
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
				So(job.Requirements.Other, ShouldResemble,
					map[string]string{schedulerQueuesAvoidRequirementKey: "avoid,queue"})
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

func TestSchedulerSubmissionMethodsRejectNilJobs(t *testing.T) {
	Convey("Scheduler submission methods reject a nil job before mutating or forwarding the batch", t, func() {
		Convey("SubmitJobs returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid := &jobqueue.Job{Cmd: "echo valid legacy job"}

			err := s.SubmitJobs([]*jobqueue.Job{valid, nil})

			assertNilJobSubmissionError(err, "SubmitJobs", 1)
			So(valid.Requirements, ShouldBeNil)
			So(valid.State, ShouldEqual, jobqueue.JobState(""))
			So(jq.jobBuffer, ShouldHaveLength, 0)
		})

		Convey("SubmitJobsAndReturnIDs returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid := &jobqueue.Job{Cmd: "echo valid id job"}

			ids, err := s.SubmitJobsAndReturnIDs([]*jobqueue.Job{valid, nil}, SubmitJobsOptions{})

			So(ids, ShouldBeNil)
			assertNilJobSubmissionError(err, "SubmitJobsAndReturnIDs", 1)
			So(valid.Requirements, ShouldBeNil)
			So(valid.State, ShouldEqual, jobqueue.JobState(""))
			So(jq.jobBuffer, ShouldHaveLength, 0)
		})

		Convey("SubmitJobsAndWait returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid := &jobqueue.Job{Cmd: "echo valid wait job"}

			jobs, err := s.SubmitJobsAndWait(context.Background(), []*jobqueue.Job{valid, nil}, SubmitJobsOptions{})

			So(jobs, ShouldBeNil)
			assertNilJobSubmissionError(err, "SubmitJobsAndWait", 1)
			So(valid.Requirements, ShouldBeNil)
			So(valid.State, ShouldEqual, jobqueue.JobState(""))
			So(jq.jobBuffer, ShouldHaveLength, 0)
		})

		Convey("SubmitJobsAndWait preserves pre-cancelled context precedence", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			jobs, err := s.SubmitJobsAndWait(ctx, []*jobqueue.Job{nil}, SubmitJobsOptions{})

			So(jobs, ShouldBeNil)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(jq.jobBuffer, ShouldHaveLength, 0)
		})
	})
}

func TestSchedulerSubmissionMethodsRejectNilDependencies(t *testing.T) {
	Convey("Scheduler submission methods reject a nil dependency before mutating or forwarding the batch", t, func() {
		newJobs := func(prefix string) (*jobqueue.Job, *jobqueue.Job, []*jobqueue.Job) {
			valid := &jobqueue.Job{Cmd: "echo " + prefix + " valid"}
			malformedRequirements := DefaultRequirements()
			malformed := &jobqueue.Job{
				Cmd:          "echo " + prefix + " malformed",
				Requirements: malformedRequirements,
				Dependencies: jobqueue.Dependencies{&jobqueue.Dependency{}, nil},
			}

			return valid, malformed, []*jobqueue.Job{valid, malformed}
		}

		assertUnmodified := func(jq *pretendJobqueue, valid, malformed *jobqueue.Job) {
			So(valid.Requirements, ShouldBeNil)
			So(valid.State, ShouldEqual, jobqueue.JobState(""))
			So(malformed.Requirements, ShouldNotBeNil)
			So(malformed.State, ShouldEqual, jobqueue.JobState(""))
			So(jq.jobBuffer, ShouldHaveLength, 0)
		}

		Convey("SubmitJobs returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid, malformed, jobs := newJobs("legacy")

			err := s.SubmitJobs(jobs)

			assertNilDependencySubmissionError(err, "SubmitJobs", 1, 1)
			assertUnmodified(jq, valid, malformed)
		})

		Convey("SubmitJobsAndReturnIDs returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid, malformed, jobs := newJobs("ids")

			ids, err := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})

			So(ids, ShouldBeNil)
			assertNilDependencySubmissionError(err, "SubmitJobsAndReturnIDs", 1, 1)
			assertUnmodified(jq, valid, malformed)
		})

		Convey("SubmitJobsAndWait returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid, malformed, jobs := newJobs("wait")

			done, err := s.SubmitJobsAndWait(context.Background(), jobs, SubmitJobsOptions{})

			So(done, ShouldBeNil)
			assertNilDependencySubmissionError(err, "SubmitJobsAndWait", 1, 1)
			assertUnmodified(jq, valid, malformed)
		})

		Convey("SubmitJobsAndWait preserves pre-cancelled context precedence", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			done, err := s.SubmitJobsAndWait(ctx, []*jobqueue.Job{{
				Dependencies: jobqueue.Dependencies{nil},
			}}, SubmitJobsOptions{})

			So(done, ShouldBeNil)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(jq.jobBuffer, ShouldHaveLength, 0)
		})

		Convey("nil, empty and zero-valued dependencies remain valid", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			jobs := []*jobqueue.Job{
				{Cmd: "echo nil dependencies", Dependencies: nil},
				{Cmd: "echo empty dependencies", Dependencies: jobqueue.Dependencies{}},
				{Cmd: "echo zero dependency", Dependencies: jobqueue.Dependencies{&jobqueue.Dependency{}}},
			}

			So(s.SubmitJobs(jobs), ShouldBeNil)
			So(jq.jobBuffer, ShouldHaveLength, 3)

			for _, job := range jobs {
				So(job.Requirements, ShouldNotBeNil)
			}
		})
	})
}

func TestSchedulerSubmissionMethodsRejectNilBehaviours(t *testing.T) {
	Convey("Scheduler submission methods reject a nil behaviour before mutating or forwarding the batch", t, func() {
		newJobs := func(prefix string) (*jobqueue.Job, *jobqueue.Job, []*jobqueue.Job) {
			valid := &jobqueue.Job{Cmd: "echo " + prefix + " valid"}
			malformedRequirements := DefaultRequirements()
			malformed := &jobqueue.Job{
				Cmd:          "echo " + prefix + " malformed",
				Requirements: malformedRequirements,
				Behaviours: jobqueue.Behaviours{
					&jobqueue.Behaviour{When: jobqueue.OnSuccess, Do: jobqueue.Nothing},
					nil,
				},
			}

			return valid, malformed, []*jobqueue.Job{valid, malformed}
		}

		assertUnmodified := func(jq *pretendJobqueue, valid, malformed *jobqueue.Job) {
			So(valid.Requirements, ShouldBeNil)
			So(valid.State, ShouldEqual, jobqueue.JobState(""))
			So(malformed.Requirements, ShouldNotBeNil)
			So(malformed.State, ShouldEqual, jobqueue.JobState(""))
			So(jq.jobBuffer, ShouldHaveLength, 0)
		}

		Convey("SubmitJobs returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid, malformed, jobs := newJobs("legacy")

			err := s.SubmitJobs(jobs)

			assertNilBehaviourSubmissionError(err, "SubmitJobs", 1, 1)
			assertUnmodified(jq, valid, malformed)
		})

		Convey("SubmitJobsAndReturnIDs returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid, malformed, jobs := newJobs("ids")

			ids, err := s.SubmitJobsAndReturnIDs(jobs, SubmitJobsOptions{})

			So(ids, ShouldBeNil)
			assertNilBehaviourSubmissionError(err, "SubmitJobsAndReturnIDs", 1, 1)
			assertUnmodified(jq, valid, malformed)
		})

		Convey("SubmitJobsAndWait returns a typed bad request", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			valid, malformed, jobs := newJobs("wait")

			done, err := s.SubmitJobsAndWait(context.Background(), jobs, SubmitJobsOptions{})

			So(done, ShouldBeNil)
			assertNilBehaviourSubmissionError(err, "SubmitJobsAndWait", 1, 1)
			assertUnmodified(jq, valid, malformed)
		})

		Convey("SubmitJobsAndWait preserves pre-cancelled context precedence", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			done, err := s.SubmitJobsAndWait(ctx, []*jobqueue.Job{{
				Behaviours: jobqueue.Behaviours{nil},
			}}, SubmitJobsOptions{})

			So(done, ShouldBeNil)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(jq.jobBuffer, ShouldHaveLength, 0)
		})

		Convey("nil, empty and valid zero or remove behaviours remain valid", func() {
			jq := &pretendJobqueue{}
			s := &Scheduler{jq: jq}
			remove := &jobqueue.Behaviour{When: jobqueue.OnFailure, Do: jobqueue.Remove}
			jobs := []*jobqueue.Job{
				{Cmd: "echo nil behaviours", Behaviours: nil},
				{Cmd: "echo empty behaviours", Behaviours: jobqueue.Behaviours{}},
				{Cmd: "echo zero behaviour", Behaviours: jobqueue.Behaviours{&jobqueue.Behaviour{}}},
				{Cmd: "echo remove behaviour", Behaviours: jobqueue.Behaviours{remove}},
			}

			So(s.SubmitJobs(jobs), ShouldBeNil)
			So(jq.jobBuffer, ShouldHaveLength, 4)
			So(jobs[2].TriggerBehaviours(true), ShouldBeNil)
			So(jobs[2].Behaviours.String(), ShouldEqual, "{}")
			So(jobs[3].RemovalRequested(), ShouldBeTrue)

			for _, job := range jobs {
				So(job.Requirements, ShouldNotBeNil)
			}
		})
	})
}

func TestFakeScheduler(t *testing.T) {
	origPretend := PretendSubmissions

	Convey("Given scheduler settings configured to store add commands", t, func() {
		// restored by a defer in this Convey, not a t.Cleanup, so it is unset
		// again before any later Convey of this test runs.
		restorePretend := setPretendSubmissionsForTest(" ")
		defer restorePretend()

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
				var jobs []*jobqueue.Job

				jobs, err = s.FindJobsByRepGroupSuffix("none")
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
				var jobs []*jobqueue.Job

				jobs, err = s.FindJobsByRepGroupPrefixAndState("none", "")
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

	Convey("After the fake scheduler Conveys, PretendSubmissions is back as it was", t, func() {
		// every later New in this package would otherwise quietly make a fake
		// scheduler that records submissions instead of submitting them.
		So(PretendSubmissions, ShouldEqual, origPretend)
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
			jobs, err := p.GetIncompleteByRepGroupMatchContext(context.Background(), "", jobqueue.RepGroupMatchExact,
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
			jobs, err := p.GetByRepGroupMatchContext(context.Background(), "", jobqueue.RepGroupMatchExact,
				0, "", false, false)
			So(jobs, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: getByRepGroupMatchOp, Err: jobqueue.ErrBadRequest})
		})

		Convey("GetByRepGroup with empty repgroup returns ErrBadRequest", func() {
			jobs, err := p.GetByRepGroup("", false, 0, "", false, false)
			So(jobs, ShouldBeNil)
			So(err, ShouldResemble, jobqueue.Error{Op: getByRepGroupMatchOp, Err: jobqueue.ErrBadRequest})
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
