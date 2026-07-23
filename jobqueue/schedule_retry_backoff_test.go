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
	"strings"
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
			ScheduleError: func() error {
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
			ScheduleError: func() error { return errTestScheduleFail },
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
