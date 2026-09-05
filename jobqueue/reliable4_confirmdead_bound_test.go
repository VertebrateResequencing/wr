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

// Untagged, fast, deterministic behavioural regression test for the reliable4
// "confirm-dead ssh storm" fix (Layer 3, checklist 260727-4; grouping follow-up
// 260729-3): markJobLost spawns a confirmOrReleaseLostJob goroutine per lost job,
// so a mass false-lost event (a freeze firing the TTR on thousands of jobs at
// once) would otherwise fire thousands of concurrent scheduler ssh checks at once
// (the ~852-goroutine ssh storm). The fix routes them through the confirm-dead
// coordinator, which groups a host's checks onto one connection and bounds the
// number of HOSTS ssh-checked at once with a per-Server semaphore sized
// ConfirmDeadConcurrency. This test uses one job per distinct host (so each is its
// own host batch) to exercise that per-host bound directly.

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable4ConfirmDeadConcurrencyBound drives the per-lost-job confirm path
// (exactly what markJobLost spawns: go confirmOrReleaseLostJob) for M jobs at
// once, each on its OWN host, where M >> the bound N, using the mock scheduler's
// RunCmd seam to record the peak number of scheduler ssh checks in flight
// simultaneously. Because each job is on a distinct host it is its own host batch
// and needs a limiter slot, so the peak equals the number of hosts ssh-checked at
// once. It asserts that peak never exceeds N. Without the semaphore the peak
// equals M (unbounded storm); with it the peak is capped at N.
func TestReliable4ConfirmDeadConcurrencyBound(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		bound = 8   // N: the maximum concurrent confirm-dead ssh checks allowed.
		lost  = 100 // M: lost jobs confirmed at once; M >> N so a breach is obvious.
	)

	ctx := context.Background()
	_, serverConfig, _, _, _ := jobqueueTestInit(true) //nolint:dogsled
	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.RunnerCmd = mockRunnerCmd
	serverConfig.Timings.ConfirmDeadConcurrency = bound

	var (
		inflight atomic.Int64 // ProcessNotRunningOnHost checks currently in flight.
		peak     atomic.Int64 // the maximum inflight ever observed.
	)

	release := make(chan struct{})

	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		// never called: we drive the confirm-dead path directly below.
		RunnerFunc: func(context.Context, string) {},
		// stands in for the ssh ProcessNotRunningOnHost performs: record how many
		// are running concurrently, hold the "ssh" open until released so overlap
		// is observable, then report the process as alive.
		RunCmdFunc: func(_ context.Context, _ string, _ bool) (string, string, error) {
			recordPeak(&peak, inflight.Add(1))

			<-release
			inflight.Add(-1)

			return "S", "", nil // a live process => ProcessNotRunningOnHost returns false
		},
	}

	Convey("Confirm-dead ssh checks are bounded under mass false-lost", t, func() {
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		// long durations so the retry/backstop paths do not fire during the test.
		const checkTimeout, checkRetry = 30 * time.Second, 30 * time.Second

		var wg sync.WaitGroup

		wg.Add(lost)

		for i := range lost {
			go func(i int) {
				defer wg.Done()

				// exactly what markJobLost spawns per lost job (killCalled false =>
				// confirm-then-kill path, pid > 0 so the check runs, runnerPid 0 so it
				// is a single pid check per job). A DISTINCT host per job makes each its
				// own host batch, so the peak in-flight equals the number of hosts
				// checked at once -- exactly what the per-host limiter bounds.
				server.confirmOrReleaseLostJob(ctx, lostJobDetails{
					key: "cdkey" + strconv.Itoa(i), host: "mockhost" + strconv.Itoa(i),
					pid: 1000 + i, runnerPid: 0, killCalled: false,
					checkTimeout: checkTimeout, checkRetryTime: checkRetry,
				})
			}(i)
		}

		// wait until as many checks as can run at once are running and the count
		// has stopped changing (the rest block for a slot, or for release).
		waitForInflightToSettle(&inflight, 250*time.Millisecond, 10*time.Second)

		observed := peak.Load()

		// let the held checks complete so all goroutines can finish.
		close(release)
		wg.Wait()

		So(observed, ShouldBeGreaterThan, int64(0))
		So(observed, ShouldBeLessThanOrEqualTo, int64(bound))
	})
}

// recordPeak raises *peak to cur if cur is larger, retrying the atomic
// compare-and-swap until the stored maximum is at least cur.
func recordPeak(peak *atomic.Int64, cur int64) {
	for {
		old := peak.Load()
		if cur <= old || peak.CompareAndSwap(old, cur) {
			return
		}
	}
}

// waitForInflightToSettle returns once inflight has stopped changing for
// stableFor (all reachable checks are running and the rest are blocked), or once
// maxWait elapses, whichever comes first.
func waitForInflightToSettle(inflight *atomic.Int64, stableFor, maxWait time.Duration) {
	deadline := time.Now().Add(maxWait)
	last := inflight.Load()
	stableSince := time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)

		cur := inflight.Load()
		if cur != last {
			last = cur
			stableSince = time.Now()

			continue
		}

		if time.Since(stableSince) >= stableFor {
			return
		}
	}
}
