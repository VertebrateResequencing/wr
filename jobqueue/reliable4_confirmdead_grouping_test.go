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

// Untagged, fast, deterministic behavioural test for the reliable4 confirm-dead
// SSH connection GROUPING (the follow-up to the leak fix, .docs/bugfixes/260729-2.md
// / freeze-fix-plan.md Fix 5). When a whole exec node dies, all its jobs go lost
// at once and each is confirmed dead by ssh'ing to that host to `ps` its pid(s) -
// two pids per job. Today that is one fresh ssh connection PER CHECK (getHost ->
// dial -> close), so K lost jobs on one dead host open ~2K connections. Grouping
// the checks by host so all of a host's pid checks share ONE connection collapses
// that to ~1 connection per host per batch.
//
// This drives the per-lost-job confirm path (exactly what markJobLost spawns) for
// K lost jobs ALL ON ONE HOST, and counts how many times a host connection is
// opened (the mock scheduler's GetHostHook). RED on current code (~2K = one per
// check); GREEN once same-host checks are grouped onto a shared connection.

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

// TestReliable4ConfirmDeadGroupsByHost asserts that confirming K lost jobs on a
// single host opens few ssh connections (grouped per host), not one per pid check.
func TestReliable4ConfirmDeadGroupsByHost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		jobs  = 50 // lost jobs, all on the one dead host; each has 2 pids => 2K checks pre-grouping.
		bound = 10 // max host connections allowed for the whole batch (post-grouping ~= 1; pre ~= 2*jobs).
	)

	ctx := context.Background()
	_, serverConfig, _, _, _ := jobqueueTestInit(true) //nolint:dogsled
	serverConfig.SchedulerName = schedulerNameMock
	serverConfig.RunnerCmd = mockRunnerCmd

	var connections atomic.Int64 // host connections opened (getHost calls).

	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		RunnerFunc:  func(context.Context, string) {},
		GetHostHook: func(string) { connections.Add(1) },
		// empty output => interpretProcessState => processDead => the pid is not
		// running, so each job is promptly confirmed dead (no blocking, no retry).
		RunCmdFunc: func(context.Context, string, bool) (string, string, error) {
			return "", "", nil
		},
	}

	Convey("Confirming many lost jobs on one host groups their checks onto few connections", t, func() {
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		// long durations so the retry/backstop paths do not fire during the test.
		const checkTimeout, checkRetry = 30 * time.Second, 30 * time.Second

		var wg sync.WaitGroup

		wg.Add(jobs)

		for i := range jobs {
			go func(i int) {
				defer wg.Done()

				// exactly what markJobLost spawns per lost job: same host, distinct
				// pids, runnerPid > 0 so confirmJobDead checks BOTH pids (2 checks/job).
				server.confirmOrReleaseLostJob(ctx, lostJobDetails{
					key: "grpkey" + strconv.Itoa(i), host: "deadnode",
					pid: 1000 + i, runnerPid: 5000 + i, killCalled: false,
					checkTimeout: checkTimeout, checkRetryTime: checkRetry,
				})
			}(i)
		}

		wg.Wait()

		// the confirmations may complete asynchronously (a grouping coordinator
		// processes them off the calling goroutine), so wait for the connection
		// count to stop climbing before reading it.
		opened := reliable4WaitConnectionsSettle(&connections, 750*time.Millisecond, 20*time.Second)

		t.Logf("CONFIRMDEAD-GROUPING: %d lost jobs (2 pids each) on one host -> %d host connections opened (bound %d)",
			jobs, opened, bound)

		So(opened, ShouldBeGreaterThan, int64(0))
		So(opened, ShouldBeLessThanOrEqualTo, int64(bound))
	})
}

// reliable4WaitConnectionsSettle waits until count has been non-zero and unchanged
// for stableFor, or until maxWait elapses, returning the final value.
func reliable4WaitConnectionsSettle(count *atomic.Int64, stableFor, maxWait time.Duration) int64 {
	deadline := time.Now().Add(maxWait)
	last := count.Load()
	stableSince := time.Now()

	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)

		cur := count.Load()
		if cur != last {
			last = cur
			stableSince = time.Now()

			continue
		}

		if cur > 0 && time.Since(stableSince) >= stableFor {
			return cur
		}
	}

	return count.Load()
}
