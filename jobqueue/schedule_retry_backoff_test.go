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

// This file tests that the schedule-retry delay is now driven by wr's own
// backoff package (github.com/VertebrateResequencing/wr/backoff): a jittered,
// exponential, capped backoff that Resets on a successful schedule, replacing
// the old hand-rolled doubling loop (scheduleRetryDelay). The failures counter
// is retained only to escalate the log from Warn to Error once scheduling has
// failed persistentScheduleFailures times in a row.

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/backoff"
	"github.com/VertebrateResequencing/wr/backoff/mock"
	backofftime "github.com/VertebrateResequencing/wr/backoff/time"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/sb10/waitgroup"
	. "github.com/smartystreets/goconvey/convey"
)

// errTestScheduleFail is a static sentinel returned by the mock scheduler to
// drive the server's scheduling-failure and retry paths in these tests.
var errTestScheduleFail = errors.New("mock schedule failure")

// TestScheduleRetryBackoff proves the per-sgroup schedule-retry backoff is
// configured and used correctly: it grows exponentially, is capped at
// scheduleRetryBackoffMax, Resets on a successful schedule, and is excluded from
// clone/snapshot; and that the retained failures counter still escalates the log
// to Error at persistentScheduleFailures.
func TestScheduleRetryBackoff(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	const minSleep = time.Minute

	Convey("A fresh sgroup lazily creates a correctly-configured retry backoff", t, func() {
		grp := &sgroup{
			name:     "cfg_rg",
			req:      &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
			failures: 2,
		}

		b := grp.ensureRetryBackoff(minSleep)
		So(b, ShouldNotBeNil)
		So(b.Min, ShouldEqual, minSleep)
		So(b.Max, ShouldEqual, scheduleRetryBackoffMax)
		So(b.Factor, ShouldEqual, float64(scheduleRetryBackoffFactor))
		So(b.Sleeper, ShouldNotBeNil)

		Convey("and returns the same object on subsequent calls (lazy-init once)", func() {
			So(grp.ensureRetryBackoff(minSleep), ShouldEqual, b)
		})

		Convey("but clone and snapshot start with a fresh (nil) backoff and zero failures", func() {
			c := grp.clone(5)
			So(c.retryBackoff, ShouldBeNil)
			So(c.failures, ShouldEqual, 0)

			snap := grp.snapshot()
			So(snap.retryBackoff, ShouldBeNil)
			So(snap.failures, ShouldEqual, 0)
		})
	})

	Convey("The retry backoff grows exponentially, caps at Max, and Resets to Min", t, func() {
		ms := &mock.Sleeper{}
		b := &backoff.Backoff{
			Min:     minSleep,
			Max:     scheduleRetryBackoffMax,
			Factor:  scheduleRetryBackoffFactor,
			Sleeper: ms,
		}

		// helper returning the duration of the next Sleep() as recorded by the
		// deterministic mock Sleeper.
		nextSleep := func() time.Duration {
			before := ms.Elapsed()

			b.Sleep(ctx)

			return ms.Elapsed() - before
		}

		// the first sleep is exactly Min (no jitter is applied to the first).
		So(nextSleep(), ShouldEqual, minSleep)

		// every sleep stays within [Min, Max], and after enough failures the
		// exponential growth is pinned to Max (2^k * Min far exceeds Max).
		var last time.Duration
		for range 12 {
			last = nextSleep()
			So(last, ShouldBeGreaterThanOrEqualTo, minSleep)
			So(last, ShouldBeLessThanOrEqualTo, scheduleRetryBackoffMax)
		}

		So(last, ShouldEqual, scheduleRetryBackoffMax)

		Convey("and Reset() makes the next sleep Min again", func() {
			b.Reset()
			So(nextSleep(), ShouldEqual, minSleep)
		})
	})

	Convey("A persistently failing schedule retries using the backoff and escalates to Error at the threshold", t, func() {
		var calls int32

		// fail the first persistentScheduleFailures calls, then succeed, so the
		// retry chain terminates deterministically.
		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc: func(context.Context, string) {},
			ScheduleError: func(int) error {
				if atomic.AddInt32(&calls, 1) <= persistentScheduleFailures {
					return errTestScheduleFail
				}

				return nil
			},
		})
		So(err, ShouldBeNil)

		s := &Server{
			previouslyScheduledGroups: make(map[string]*sgroup),
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "schedule-retry-runner %s %s %s %s %d %d",
			ServerInfo:                &ServerInfo{},
			stopClientHandling:        make(chan bool),
		}
		s.timings.CheckRunnerTime = minSleep

		ms := &mock.Sleeper{}
		grp := &sgroup{
			name:  "retry_rg",
			count: 1,
			req:   &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
			retryBackoff: &backoff.Backoff{
				Min:     minSleep,
				Max:     scheduleRetryBackoffMax,
				Factor:  scheduleRetryBackoffFactor,
				Sleeper: ms,
			},
		}

		logs := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		s.scheduleRunners(ctx, grp)
		s.wg.Wait(5 * time.Second)

		chainElapsed := ms.Elapsed()

		Convey("the backoff sleeps once per failed retry, with exponential (not flat) delays", func() {
			So(ms.Invoked(), ShouldEqual, persistentScheduleFailures)

			// delays are Min, then jittered within (Min,2*Min] and (2*Min,4*Min],
			// so the total is strictly more than a flat 3*Min and no more than the
			// unjittered maximum of Min+2*Min+4*Min.
			So(chainElapsed, ShouldBeGreaterThan, time.Duration(persistentScheduleFailures)*minSleep)
			So(chainElapsed, ShouldBeLessThanOrEqualTo, 7*minSleep)
		})

		Convey("the failure counter and backoff are reset once scheduling succeeds", func() {
			grp.RLock()
			failures := grp.failures
			grp.RUnlock()
			So(failures, ShouldEqual, 0)

			// after the success Reset, the next backoff sleep is Min again.
			before := ms.Elapsed()

			grp.retryBackoff.Sleep(ctx)
			So(ms.Elapsed()-before, ShouldEqual, minSleep)
		})

		Convey("the log escalates from Warn to Error exactly once, at the threshold", func() {
			out := logs.String()
			So(strings.Count(out, "Server scheduling runners error"), ShouldEqual, persistentScheduleFailures-1)
			So(strings.Count(out, "Server scheduling runners persistently failing"), ShouldEqual, 1)
		})
	})

	Convey("A pending retry sleep is aborted promptly when client handling stops", t, func() {
		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc:    func(context.Context, string) {},
			ScheduleError: func(int) error { return errTestScheduleFail },
		})
		So(err, ShouldBeNil)

		s := &Server{
			previouslyScheduledGroups: make(map[string]*sgroup),
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "schedule-retry-runner %s %s %s %s %d %d",
			ServerInfo:                &ServerInfo{},
			stopClientHandling:        make(chan bool),
		}
		s.timings.CheckRunnerTime = minSleep

		// a real-time Sleeper with a long Min: only a working shutdown-abort can
		// end the sleep before the (5s) drain wait would otherwise expire.
		grp := &sgroup{
			name:  "shutdown_rg",
			count: 1,
			req:   &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
			retryBackoff: &backoff.Backoff{
				Min:     30 * time.Second,
				Max:     scheduleRetryBackoffMax,
				Factor:  scheduleRetryBackoffFactor,
				Sleeper: &backofftime.Sleeper{},
			},
		}

		s.scheduleRunners(ctx, grp)

		// stopping client handling must cancel the in-flight backoff sleep.
		close(s.stopClientHandling)

		drained := make(chan struct{})

		go func() {
			s.wg.Wait(5 * time.Second)
			close(drained)
		}()

		So(closedWithin(drained, 2*time.Second), ShouldBeTrue)
	})
}

// TestScheduleRetryFailureAsLoopEnds proves that a schedule that fails just as
// its group's retry loop succeeds, too late for that loop's attempt, is still
// retried.
func TestScheduleRetryFailureAsLoopEnds(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const minSleep = 50 * time.Millisecond

	ctx := context.Background()

	Convey("Given a retry loop that succeeds while another attempt fails", t, func() {
		var n atomic.Int32

		// calls 1 and 3 fail: the first attempt, and one made after the retry
		// loop's attempt (call 2) succeeded but before the loop ended
		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc: func(context.Context, string) {},
			ScheduleError: func(int) error {
				if c := n.Add(1); c == 1 || c == 3 {
					return errTestScheduleFail
				}

				return nil
			},
		})
		So(err, ShouldBeNil)

		live := &sgroup{
			name:  "ending_rg",
			count: 1,
			req:   &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
		}

		s := &Server{
			previouslyScheduledGroups: map[string]*sgroup{live.name: live},
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "schedule-retry-runner %s %s %s %s %d %d",
			ServerInfo:                &ServerInfo{},
			stopClientHandling:        make(chan bool),
		}
		s.timings.CheckRunnerTime = minSleep

		defer close(s.stopClientHandling)

		var once sync.Once

		scheduleRetryEndingHook = func() {
			once.Do(func() { s.scheduleRunners(ctx, live.snapshot()) })
		}

		defer func() { scheduleRetryEndingHook = nil }()

		s.scheduleRunners(ctx, live.snapshot())

		drained := make(chan struct{})

		go func() {
			s.wg.Wait(5 * time.Second)
			close(drained)
		}()

		So(closedWithin(drained, 4*time.Second), ShouldBeTrue)
		So(n.Load(), ShouldEqual, 4)
	})
}

// scheduleCall is one Schedule() the mock scheduler was asked to make.
type scheduleCall struct {
	count int
	at    time.Time
}

// scheduleCallLog records the counts a mock scheduler is asked for.
type scheduleCallLog struct {
	mu    sync.Mutex
	calls []scheduleCall
}

// record notes a Schedule() for count, and fails it if count is above 0, like a
// queue whose bsub is always refused.
func (l *scheduleCallLog) record(count int) error {
	l.note(count)

	if count > 0 {
		return errTestScheduleFail
	}

	return nil
}

// note notes a Schedule() for count.
func (l *scheduleCallLog) note(count int) {
	l.mu.Lock()
	l.calls = append(l.calls, scheduleCall{count: count, at: time.Now()})
	l.mu.Unlock()
}

// failedSince returns how many Schedule() calls asked for runners at or after
// the given time.
func (l *scheduleCallLog) failedSince(since time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := 0

	for _, c := range l.calls {
		if c.count > 0 && !c.at.Before(since) {
			n++
		}
	}

	return n
}

// total returns how many Schedule() calls there have been.
func (l *scheduleCallLog) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.calls)
}

// lastCount returns the count of the latest Schedule() call, or -1 if none.
func (l *scheduleCallLog) lastCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.calls) == 0 {
		return -1
	}

	return l.calls[len(l.calls)-1].count
}

// TestScheduleRetryStopsWhenGroupEmptied proves that once a scheduler group's
// only job is removed, the manager stops asking the scheduler for runners for
// it, even though every earlier attempt failed and is being retried. In soak9 a
// job sent to a queue whose esub refused every bsub was removed, and the manager
// kept retrying the bsub for its empty group for 16 minutes.
func TestScheduleRetryStopsWhenGroupEmptied(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		rg          = "emptied_retry_rg"
		checkRunner = 50 * time.Millisecond
		settle      = 500 * time.Millisecond
		quiet       = 3 * time.Second
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	calls := &scheduleCallLog{}

	serverConfig.Timings.CheckRunnerTime = checkRunner
	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.RunnerCmd = mockRunnerCmd
	serverConfig.SchedulerConfig = &scheduler.ConfigMock{
		RunnerFunc:    func(context.Context, string) {},
		ScheduleError: calls.record,
	}

	Convey("Given a server whose scheduler fails every request for runners", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: "echo emptied retry", Cwd: testCwdPath, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs,
		}

		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		start := time.Now()

		for calls.failedSince(start) < 10 && time.Since(start) < 10*time.Second {
			<-time.After(checkRunner)
		}

		So(calls.failedSince(start), ShouldBeGreaterThanOrEqualTo, 10)

		Convey("removing the group's only job stops the failing requests and leaves the count at 0", func() {
			deleted, errd := jq.Delete([]*JobEssence{{JobKey: job.Key()}})
			So(errd, ShouldBeNil)
			So(deleted, ShouldEqual, 1)

			<-time.After(settle)

			quietFrom := time.Now()

			<-time.After(quiet)

			So(calls.failedSince(quietFrom), ShouldEqual, 0)
			So(calls.lastCount(), ShouldEqual, 0)
		})
	})
}

// TestScheduleRetryFollowsGroupCount proves that a failing schedule keeps being
// retried while its group still has jobs, by one retry loop however many
// attempts failed, and that the retry asks for the group's current count, so it
// ends once that count is 0.
func TestScheduleRetryFollowsGroupCount(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		minSleep = 50 * time.Millisecond
		attempts = 20
		window   = time.Second
	)

	ctx := context.Background()

	Convey("Given a scheduled group whose schedule attempts all fail", t, func() {
		calls := &scheduleCallLog{}

		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc:    func(context.Context, string) {},
			ScheduleError: calls.record,
		})
		So(err, ShouldBeNil)

		live := &sgroup{
			name:  "follow_rg",
			count: 1,
			req:   &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
		}

		s := &Server{
			previouslyScheduledGroups: map[string]*sgroup{live.name: live},
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "schedule-retry-runner %s %s %s %s %d %d",
			ServerInfo:                &ServerInfo{},
			stopClientHandling:        make(chan bool),
		}
		s.timings.CheckRunnerTime = minSleep

		defer close(s.stopClientHandling)

		start := time.Now()

		for range attempts {
			s.scheduleRunners(ctx, live.snapshot())
		}

		<-time.After(window)

		retries := calls.failedSince(start) - attempts

		Convey("it is retried, by one retry loop, while the group has jobs", func() {
			So(retries, ShouldBeGreaterThanOrEqualTo, 2)
			So(retries, ShouldBeLessThanOrEqualTo, 10)
		})

		Convey("and once the group's count drops to 0 the retry asks for 0 and stops", func() {
			So(live.decrement(1), ShouldEqual, 0)

			emptied := time.Now()

			drained := make(chan struct{})

			go func() {
				s.wg.Wait(5 * time.Second)
				close(drained)
			}()

			So(closedWithin(drained, 4*time.Second), ShouldBeTrue)

			// a retry that read the count just before the decrement may still
			// ask for 1 once
			So(calls.failedSince(emptied), ShouldBeLessThanOrEqualTo, 1)
			So(calls.lastCount(), ShouldEqual, 0)
		})
	})
}

// TestScheduleRetryAgainAfterRecovery proves that once a group's retry loop has
// ended on a successful schedule, a later failing schedule for the same group is
// retried again.
func TestScheduleRetryAgainAfterRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const minSleep = 50 * time.Millisecond

	ctx := context.Background()

	Convey("Given a group whose schedule failed and was then retried successfully", t, func() {
		var failing atomic.Bool

		failing.Store(true)

		calls := &scheduleCallLog{}

		sched, err := scheduler.New(ctx, "mock", &scheduler.ConfigMock{
			RunnerFunc: func(context.Context, string) {},
			ScheduleError: func(count int) error {
				calls.note(count)

				if failing.Load() {
					return errTestScheduleFail
				}

				return nil
			},
		})
		So(err, ShouldBeNil)

		live := &sgroup{
			name:  "recover_rg",
			count: 1,
			req:   &scheduler.Requirements{RAM: 1, Cores: 1, Disk: 1, Time: time.Second},
		}

		s := &Server{
			previouslyScheduledGroups: map[string]*sgroup{live.name: live},
			wg:                        waitgroup.New(),
			scheduler:                 sched,
			rc:                        "schedule-retry-runner %s %s %s %s %d %d",
			ServerInfo:                &ServerInfo{},
			stopClientHandling:        make(chan bool),
		}
		s.timings.CheckRunnerTime = minSleep

		defer close(s.stopClientHandling)

		s.scheduleRunners(ctx, live.snapshot())
		failing.Store(false)

		drained := make(chan struct{})

		go func() {
			s.wg.Wait(5 * time.Second)
			close(drained)
		}()

		So(closedWithin(drained, 4*time.Second), ShouldBeTrue)

		Convey("a later failure for the same group is retried", func() {
			failing.Store(true)

			before := calls.total()

			s.scheduleRunners(ctx, live.snapshot())

			<-time.After(time.Second)

			So(calls.total()-before, ShouldBeGreaterThanOrEqualTo, 3)
		})
	})
}
