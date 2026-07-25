//go:build reliability_repro

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

// This file holds DETERMINISTIC, IN-PROCESS reproducers for the two reliable4
// LSF-scale stall bugs described in .docs/reliable4/background.md. They are
// gated behind the `reliability_repro` build tag so they are NOT part of
// `make test`; run them with:
//
//	go test -tags reliability_repro ./jobqueue/ -run <TestName>
//
// (or via the developers/wrdev.sh backlog-rescan-check /
// runner-started-timeout-check commands). Unlike the shipped red-until-fixed
// TDD tests, these are written to FAIL on the CURRENT (pre-fix) code: each
// asserts an INVARIANT that only holds once the corresponding bug is fixed. A
// later /bugfix step makes them pass. They exercise the real code paths
// (buildSchedulerGroups/prepareReadyJob for issue #1; Client.Execute's
// post-exec Started() report for issue #3) at the smallest faithful level.
//
// Helpers opEnvInt and newOverProvisionServer are shared with
// reliable3_overprovision_test.go; subscriptionTestConfig, serve, Connect,
// disconnect, pollUntil and the client_payload_test capture-socket harness are
// shared with the untagged tests.

package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kballard/go-shellquote"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

// TestReliable4BacklogRescan reproduces reliable4 ISSUE #1: the ready-added
// callback re-scans the WHOLE ready backlog every cycle. buildSchedulerGroups
// loops over every ready item and runs the per-job scheduling work
// (prepareReadyJob: job locks, requirement lookup, scheduler-group snapshot and,
// on change, q.SetReserveGroup) for ALL of them - including the jobs whose limit
// group is already saturated and so cannot be scheduled this cycle. With ~80k
// ready behind a 2000 limit that is ~40x wasted work every cycle, and the cycle
// runs back-to-back, pinning the manager.
//
// INVARIANT (fails now, passes after the fix): a rac cycle's per-job scheduling
// work is bounded by the SCHEDULABLE count (~limit + a small constant), NOT by
// the ready backlog size. It is observed via the inert Server.racScanWork
// counter (reset + incremented inside buildSchedulerGroups).
//
// Deterministic: we add backlog ready jobs sharing ONE limit group (limit L), so
// exactly L are schedulable and backlog-L are permanently limit-blocked, wait for
// the automatic rac to go idle, then drive ONE real buildSchedulerGroups cycle
// synchronously and read racScanWork. No runner command is configured, so nothing
// re-triggers rac while we measure. Scale knobs: WR_OP_LIMIT (default 2000) and
// WR_BR_BACKLOG (default 50000).
func TestReliable4BacklogRescan(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A rac cycle's per-job work is bounded by the schedulable count, not the ready backlog", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 2000)
		backlog := opEnvInt("WR_BR_BACKLOG", 50000)
		if backlog <= limit {
			backlog = limit + 1000
		}

		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		// NB: we deliberately do NOT configure a runner command (server.rc stays
		// ""), so scheduleGroupRunners never runs and nothing re-triggers rac once
		// the backlog is added: our measurement below then has no concurrent cycle.

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// add `backlog` ready jobs, all sharing ONE count-limited limit group "lg"
		// (limit L). Only L are ever schedulable; the other backlog-L are
		// permanently limit-blocked - yet the current rac still scans them all.
		limitGroup := fmt.Sprintf("lg:%d", limit)
		jobs := make([]*Job, 0, backlog)

		for i := range backlog {
			jobs = append(jobs, &Job{
				Cmd:          fmt.Sprintf("true reliable4 backlog %d", i),
				Cwd:          testCwd,
				ReqGroup:     "reliable4-backlog",
				Requirements: standardReqs,
				RepGroup:     "reliable4-backlog",
				LimitGroups:  []string{limitGroup},
			})
		}

		added, existed, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, backlog)
		So(existed, ShouldEqual, 0)

		// wait for every job to be ready and the automatic rac (fired by Add) to
		// go idle, so our direct buildSchedulerGroups measurement runs with no
		// concurrent cycle mutating racScanWork.
		So(pollUntil(func() bool {
			server.rpmutex.Lock()
			running := server.racRunning
			server.rpmutex.Unlock()

			return !running && len(server.q.AllItems()) == backlog
		}), ShouldBeTrue)

		// gather the ready item data exactly as the queue hands it to the rac
		// callback, then drive ONE real buildSchedulerGroups cycle. We pass a
		// non-empty rc so the full prepareReadyJob path runs, as in production.
		items := server.q.AllItems()
		allitemdata := make([]any, 0, len(items))

		for _, item := range items {
			allitemdata = append(allitemdata, item.Data())
		}

		groups := server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")
		scanWork := int(server.racScanWork.Load())

		// prove the backlog really is limit-blocked: only L are schedulable this
		// cycle, the rest are skipped - which is exactly why scanning all of them
		// is wasted work.
		scheduled, skipped := 0, 0
		for _, g := range groups {
			scheduled += g.count
			skipped += g.skipped
		}

		const margin = 100

		t.Logf("BACKLOG-RESCAN-REPRO: limit=%d backlog=%d scanWork=%d (want <= %d); schedulable=%d limitBlocked=%d",
			limit, backlog, scanWork, limit+margin, scheduled, skipped)

		So(scheduled, ShouldEqual, limit)
		So(skipped, ShouldEqual, backlog-limit)

		Convey("the per-cycle per-job work does not scan the whole ready backlog", func() {
			So(scanWork, ShouldBeLessThanOrEqualTo, limit+margin)
		})
	})
}

// startedTimeoutSocket wraps a captureSocket to make the FIRST post-exec
// Started() report RPC (method "jstart") fail once with a "receive time out"
// error, exactly as a saturated server does in production. Every other request
// (jtouch, jarchive, ...) behaves normally, so only the Started() report is
// disrupted - the command itself is healthy. This is a pure TEST seam: it lives
// entirely in the socket the test injects, with no production change.
type startedTimeoutSocket struct {
	*captureSocket

	mu         sync.Mutex
	failArmed  bool // set when a jstart was just sent, so the next Recv errors
	failedOnce bool // ensures we fail Started at most once
	startSeen  int  // how many jstart requests were observed
}

func (s *startedTimeoutSocket) Send(msg []byte) error {
	req := &clientRequest{}
	dec := codec.NewDecoderBytes(msg, s.ch)
	_ = dec.Decode(req) //nolint:errcheck // best-effort peek at the method

	s.mu.Lock()
	if req.Method == requestMethodStart {
		s.startSeen++

		if !s.failedOnce {
			s.failArmed = true
			s.failedOnce = true
		}
	}
	s.mu.Unlock()

	return s.captureSocket.Send(msg)
}

func (s *startedTimeoutSocket) Recv() ([]byte, error) {
	s.mu.Lock()
	fail := s.failArmed
	s.failArmed = false
	s.mu.Unlock()

	if fail {
		// mimic the production error a saturated server's blocked reply produces.
		return nil, errors.New("receive time out")
	}

	return s.captureSocket.Recv()
}

func (s *startedTimeoutSocket) starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.startSeen
}

// TestReliable4StartedTimeoutKillsHealthyCommand reproduces reliable4 ISSUE #3:
// after exec, the runner calls c.Started(job, pid) to report its PID; if that
// outbound RPC returns an error (e.g. "receive time out" under server
// saturation), Execute KILLS the still-healthy command and returns the "started
// running, but I killed it due to a jobqueue server error" error (client.go
// ~1675-1686). The touch loop already tolerates errors and retries; Started()
// does not, so a transient outbound status-report failure destroys good work.
//
// INVARIANT (fails now, passes after the fix): a transient failure of the
// post-exec Started() report must NOT kill a healthy running command - the
// command runs to completion and its side effect (writing a marker file)
// happens.
//
// Deterministic: the injected socket fails the FIRST jstart immediately (no real
// 60s wait), while the command `sleep 1; echo ran > $marker` would write its
// marker if not killed. Pre-fix the command is killed during the sleep, so the
// marker is never written; the injected error is immediate, so there is no
// timing race (the marker is written a whole second later, only if the command
// survives).
func TestReliable4StartedTimeoutKillsHealthyCommand(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A transient Started() timeout must not kill a healthy running command", t, func() {
		capture := &liveTouchCapture{}
		client, sock := newStartedTimeoutClient(capture)
		cwd := liveExecuteCwd(t)
		marker := filepath.Join(cwd, "ran")

		cmd := fmt.Sprintf("sleep 1; echo ran > %s", shellquote.Join(marker))
		job := liveExecuteJob(client, cwd, cmd)

		execErr := client.Execute(ctx, job, "/bin/sh")

		_, statErr := os.Stat(marker)
		markerWritten := statErr == nil

		t.Logf("STARTED-TIMEOUT-REPRO: markerWritten=%v startedCalls=%d execErr=%q",
			markerWritten, sock.starts(), errString(execErr))

		// sanity: the Started() report path really was exercised (and failed once).
		So(sock.starts(), ShouldBeGreaterThanOrEqualTo, 1)

		Convey("the command runs to completion and its side effect happens", func() {
			So(markerWritten, ShouldBeTrue)
		})
	})
}

// newStartedTimeoutClient builds an in-process capture client (no real server)
// whose socket fails the first Started() report RPC once, mirroring
// newLiveExecuteCaptureClient's timing overrides so Execute runs quickly.
func newStartedTimeoutClient(capture *liveTouchCapture) (*Client, *startedTimeoutSocket) {
	client, base := newCaptureClient()
	sock := &startedTimeoutSocket{captureSocket: base}
	client.sock = sock
	client.touchInterval = liveExecuteTouchInterval
	client.retryWait = liveExecuteRetryWait
	client.retryTime = liveExecuteRetryTime
	client.percentMemoryKill = ClientPercentMemoryKill
	client.liveTouchHook = capture.record

	return client, sock
}

// errString renders an error for a log line, tolerating nil.
func errString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
