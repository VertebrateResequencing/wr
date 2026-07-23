//go:build reliability

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

// WARNING - THIS TEST UNDER-REPRODUCES THE REAL FAILURE; IT IS A
// NON-AUTHORITATIVE SUPPORT TEST ONLY, NEVER THE SOLE EVIDENCE.
//
// This in-process harness passed with M2 (archive-rejection) == 0 while real
// LSF at scale actually churned. Two structural reasons make it weaker than
// the real system:
//   - it drives Started/Touch/Archive with live in-process runners whose pid is
//     os.Getpid(), so every "owner" is genuinely alive and the liveness checks
//     the manager makes always succeed - it cannot exhibit the confirmed-dead /
//     stale-owner cases that real LSF (with real, exiting scheduler processes)
//     produces, and
//   - it must run with a TTR set comfortably ABOVE the saturated single-reader's
//     per-RPC backlog (see the NOTE on TTR below), so a job's Started/Touch RPC
//     is never itself delayed past the TTR - exactly the delay that, on the real
//     farm, drives the churn.
//
// It is therefore kept only as a SUPPORTING signal and must never be treated as
// the authoritative reproduction or as sufficient evidence on its own. The
// AUTHORITATIVE reproduction is Tier B - real LSF at scale on the isolated dev
// deployment - recorded in .docs/reliable2/phase2/validation.md (see spec.md
// section E3 and phase5.md Item 5.2).
//
// This file is the in-process SATURATION HARNESS for the reliable2 scale/
// throughput validation gate (spec.md section I, phase5.md Item 5.2). It is
// NOT a normal unit test: it is excluded from the default build and the
// `make test` suite by the `reliability` build tag, and is run explicitly as a
// heavy validation gate. Its recorded result lives in
// .docs/reliable2/scale-validation.md.
//
// It scales up the deterministic A1 oracle (TestReliable2HoldingRunnerArchiveAccepted)
// to many concurrent in-process runners driving the real jobqueue server
// through the single-reader command socket under the exact conditions that, on
// the PRE-revert code, produced the churn described in testing.md:
//
//   - a short ItemTTR so a running job's TTR expires easily,
//   - a large cohort of runners that Reserve -> Started(alive pid) -> Touch once
//     (so the server reliably records it Running) -> then hold the job past the
//     TTR WITHOUT further touching (so the TTR callback flags it Lost while its
//     runner is alive) -> Archive(exit 0). On v0.36.5-semantics (this reworked
//     build) an alive owner's successful archive must always be accepted and the
//     job never re-reserved; on the pre-revert strict state machine this archive
//     was rejected ("jarchive: bad job" / ErrMustReserve) and the successful work
//     discarded and re-run,
//   - sheer connection concurrency to saturate the single serveClients reader
//     (there is no separate tunable "reader threshold" in the server; the reader
//     is structurally one goroutine calling sock.RecvMsg() in a loop, so we
//     saturate it with many concurrent connections, as documented in
//     scale-validation.md),
//   - a status-details listener (the same server-side subscription the web UI
//     uses, stateChanges=true, scoped to every job key) counting every
//     JobStateDeleted broadcast for a succeeded job (M4), plus a heavy
//     `wr status` sampler (the GetByRepGroup/AllItems path) measuring M5 idle
//     vs under load.
//
// NOTE on TTR: the TTR must be set comfortably ABOVE the saturated single-reader's
// per-RPC processing latency at the chosen connection count. If the TTR is shorter
// than that backlog (e.g. sub-second TTR at >=2000 connections), a runner's
// Started/Touch RPC can itself be delayed past the TTR, so the manager correctly
// (per the v0.36.5 ttrCallback: zero-start -> SubQueueDelay) requeues a job whose
// start it has not yet recorded, and the job is re-run. That is a harness artefact
// of the tiny TTR (the real farm has a 60s TTR vs multi-minute jobs, so a quick
// Started/Touch RPC is never backlogged that long); see scale-validation.md. Use
// WR_SCALE_TTR_MS >= 2000 for 1000 conns, >= 8000 for 2000-3000 conns.
//
// Metrics measured (definitions in testing.md "Metrics"):
//   M1 forward progress    = complete / executed (target ~1.0)
//   M2 archive-rejection    = archive errors for exit-0 jobs (target 0)
//   M4 web-UI fidelity      = JobStateDeleted broadcasts for succeeded jobs (target 0)
//   M5 status responsiveness = heavy status latency under load / idle (bounded)
//   M7 throughput           = steady-state jobs/s completed (reported)
//
// Run it with (bounded, a few minutes; adjust scale via env):
//
//   env -u OS_AUTH_URL -u OS_USERNAME ...(all OS_ vars)... \
//     WR_SCALE_JOBS=2000 WR_SCALE_RUNNERS=2000 WR_SCALE_TTR_MS=8000 \
//     CGO_ENABLED=1 go test -tags 'netgo reliability' -count=1 -timeout 20m \
//     -run TestReliable2ScaleSaturation ./jobqueue -v
//
// Add -race for the race-detector pass (slower; lower the scale if needed).

package jobqueue

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/queue"
	log15 "github.com/inconshreveable/log15/v3"
)

// scaleParams holds the tunable scale knobs, read from the environment so the
// same harness can be dialled up or down for the node it runs on.
type scaleParams struct {
	jobs         int
	runners      int
	ttr          time.Duration
	lostFraction float64 // fraction of jobs held past TTR (flipped Lost while alive)
	m5Samples    int
}

func scaleParamsFromEnv() scaleParams {
	p := scaleParams{
		jobs:         envInt("WR_SCALE_JOBS", 2000),
		runners:      envInt("WR_SCALE_RUNNERS", 0),
		ttr:          time.Duration(envInt("WR_SCALE_TTR_MS", 2000)) * time.Millisecond,
		lostFraction: envFloat("WR_SCALE_LOST_FRACTION", 0.5),
		m5Samples:    envInt("WR_SCALE_M5_SAMPLES", 8),
	}

	// default to 1000 concurrent connections (a level the default 2s TTR clears
	// comfortably on an 8-core node). Higher connection counts need a larger TTR
	// (see the NOTE at the top of the file), so are opt-in via WR_SCALE_RUNNERS.
	if p.runners <= 0 {
		p.runners = min(1000, p.jobs)
	}

	if p.runners > p.jobs {
		p.runners = p.jobs
	}

	return p
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}

	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}

	return def
}

// scaleCounters are the atomics every runner updates; read once at the end.
type scaleCounters struct {
	reserved     atomic.Int64 // jobs reserved+started (commands "executed")
	completed    atomic.Int64 // successful archives accepted
	archiveErrs  atomic.Int64 // M2: archive rejected for an exit-0 job
	reserveErrs  atomic.Int64 // reserve RPC errors (connection saturation churn)
	heldPastTTR  atomic.Int64 // jobs deliberately held past the TTR (Lost-while-alive)
	observedLost atomic.Int64 // of those, confirmed Lost-in-Run before archiving

	// reserveCounts tracks, per job key, how many distinct times it was
	// reserved+started, so a re-reservation of an alive-owned job (an A1 invariant
	// violation) is counted in doubleReserve.
	reserveCounts sync.Map // key -> *atomic.Int64
	doubleReserve atomic.Int64
}

// TestReliable2ScaleSaturation is the in-process saturation validation gate.
func TestReliable2ScaleSaturation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	p := scaleParamsFromEnv()
	rg := "reliable2_scale_rg"

	t.Logf("SCALE PARAMS: jobs=%d runners(connections)=%d ttr=%v lostFraction=%.2f",
		p.jobs, p.runners, p.ttr, p.lostFraction)

	if os.Getenv("WR_SCALE_DEBUG_LOG") != "" {
		clog.ToHandlerAtLevel(log15.StreamHandler(os.Stderr, log15.LogfmtFormat()), "debug")

		defer clog.ToDefault()
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = p.ttr
	serverConfig.Timings.TouchInterval = p.ttr / 2
	// keep the release backoff tiny so any (unexpected) re-runs churn fast rather
	// than hiding behind a long delay.
	serverConfig.Timings.ReleaseDelayMin = 50 * time.Millisecond

	server, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	defer server.Stop(ctx, true)

	connect := func() *Client {
		c, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		if errc != nil {
			t.Fatalf("connect failed: %v", errc)
		}

		return c
	}

	adder := connect()
	defer disconnect(adder)

	// build p.jobs unique jobs (unique Cmd => unique Key => no dedup) in one rep
	// group so the counts are aggregatable.
	jobs := make([]*Job, p.jobs)
	jobKeys := make([]string, p.jobs)

	for i := range jobs {
		j := &Job{
			Cmd: fmt.Sprintf("%s scale %d", restFormTrue, i), Cwd: testCwdPath,
			RepGroup: rg, ReqGroup: rg, Requirements: standardReqs, Retries: 30,
		}
		jobs[i] = j
		jobKeys[i] = j.Key()
	}

	inserts, _, err := adder.Add(jobs, os.Environ(), true)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if inserts != p.jobs {
		t.Fatalf("expected %d inserts, got %d", p.jobs, inserts)
	}

	t.Logf("added %d jobs", inserts)

	// M4 status-details listener: the exact server-side subscription the web UI
	// uses (stateChanges=true), scoped to every job key, counting JobStateDeleted
	// broadcasts (and, for reference, JobStateComplete). Started before any job
	// runs so no terminal transition is missed.
	statusSubID := server.registerStatusSubscription()
	server.subscribeToJobs(statusSubID, jobKeys)

	var (
		deletedBroadcasts  atomic.Int64
		completeBroadcasts atomic.Int64
	)

	stopStatusSub := make(chan struct{})
	statusSubDone := make(chan struct{})

	go pollStatusSubscription(server, statusSubID, stopStatusSub, statusSubDone,
		&deletedBroadcasts, &completeBroadcasts)

	// M5 idle baseline: heavy status latency with no runner load.
	idleM5 := measureHeavyStatus(connect, rg, p.m5Samples)
	t.Logf("M5 idle heavy-status latency: median=%v", idleM5)

	// launch the saturation: p.runners persistent connections, each looping
	// Reserve -> Started(alive) -> (hold-past-TTR | fast) -> Archive(exit 0)
	// until every job is drained.
	counters := &scaleCounters{}
	remaining := atomic.Int64{}
	remaining.Store(int64(p.jobs))

	var underLoadM5 time.Duration

	var m5wg sync.WaitGroup

	m5wg.Add(1)

	// sampler measures M5 under load while runners churn.
	go func() {
		defer m5wg.Done()

		underLoadM5 = sampleHeavyStatusUnderLoad(connect, rg, p.m5Samples, &remaining)
	}()

	// DECISIVE PROBE: while the churn runs, repeatedly ask the scheduler whether
	// THIS (alive) process is dead - the exact confirm-dead check the lost path
	// uses. Any "dead" verdict here is a false-positive caused by the in-process
	// fork-storm and is the only way an alive-owned Lost job gets killed+re-run.
	var probeFalseDead, probeTotal atomic.Int64

	probeHost, herr := os.Hostname()
	if herr != nil {
		probeHost = localhost
	}

	probePid := os.Getpid()

	go func() {
		for remaining.Load() > 0 {
			if server.scheduler.ProcessNotRunningOnHost(ctx, probePid, probeHost) {
				probeFalseDead.Add(1)
			}

			probeTotal.Add(1)

			time.Sleep(2 * time.Millisecond)
		}
	}()

	start := time.Now()

	var runnerWg sync.WaitGroup

	for r := range p.runners {
		runnerWg.Add(1)

		go func(seed int) {
			defer runnerWg.Done()

			runnerLoop(server, connect, p, seed, &remaining, counters)
		}(r)
	}

	runnerWg.Wait()

	elapsed := time.Since(start)

	m5wg.Wait()

	// let the status subscription drain any trailing terminal broadcasts.
	time.Sleep(2 * time.Second)
	close(stopStatusSub)
	<-statusSubDone

	// authoritative final counts (the heavy GetByRepGroup/AllItems path).
	summary := heavyStatus(connect(), rg)

	completeCount := 0
	deletedCount := 0

	if summary != nil {
		completeCount = summary.Counts[JobStateComplete]
		deletedCount = summary.Counts[JobStateDeleted]
	}

	executed := counters.reserved.Load()
	completed := counters.completed.Load()

	var m1 float64
	if executed > 0 {
		m1 = float64(completed) / float64(executed)
	}

	var m5ratio float64
	if idleM5 > 0 {
		m5ratio = float64(underLoadM5) / float64(idleM5)
	}

	throughput := float64(completed) / elapsed.Seconds()

	t.Logf("========================= SCALE RESULT =========================")
	t.Logf("scale: jobs=%d concurrent-connections=%d ttr=%v", p.jobs, p.runners, p.ttr)
	t.Logf("executed(reserved+started)=%d  completed(archive accepted)=%d", executed, completed)
	t.Logf("held-past-TTR=%d  observed-Lost-in-Run=%d (genuine alive-runner Lost flips under load)",
		counters.heldPastTTR.Load(), counters.observedLost.Load())
	t.Logf("re-reservations of an already-reserved job = %d (A1 invariant: must be 0)",
		counters.doubleReserve.Load())
	t.Logf("reserve RPC errors (saturation)=%d", counters.reserveErrs.Load())
	t.Logf("CONFIRM-DEAD PROBE: false-dead verdicts for THIS alive process = %d/%d",
		probeFalseDead.Load(), probeTotal.Load())
	t.Logf("M1 forward-progress (complete/executed) = %.4f  (target ~1.0)", m1)
	t.Logf("M2 archive-rejections for exit-0 jobs   = %d      (target 0)", counters.archiveErrs.Load())
	t.Logf("M4 deleted broadcasts for succeeded jobs = %d     (target 0)", deletedBroadcasts.Load())
	t.Logf("   (complete broadcasts observed = %d)", completeBroadcasts.Load())
	t.Logf("M5 heavy-status idle=%v underLoad=%v ratio=%.2fx (target bounded, <<15x)",
		idleM5, underLoadM5, m5ratio)
	t.Logf("M7 throughput = %.1f jobs/s over %v", throughput, elapsed.Round(time.Millisecond))
	t.Logf("authoritative rep-group counts: complete=%d deleted=%d", completeCount, deletedCount)
	t.Logf("================================================================")

	// PASS/FAIL assertions against testing.md acceptance thresholds.
	if completed != int64(p.jobs) {
		t.Errorf("FAIL M1: only %d/%d jobs completed", completed, p.jobs)
	}

	if executed != completed {
		t.Errorf("FAIL M1: executed=%d != completed=%d (some successful work not recorded complete)",
			executed, completed)
	}

	if counters.archiveErrs.Load() != 0 {
		t.Errorf("FAIL M2: %d archive rejections for exit-0 jobs", counters.archiveErrs.Load())
	}

	if counters.doubleReserve.Load() != 0 {
		t.Errorf("FAIL A1: %d re-reservations of an already-reserved (alive-owned) job",
			counters.doubleReserve.Load())
	}

	if deletedBroadcasts.Load() != 0 {
		t.Errorf("FAIL M4: %d deleted broadcasts for succeeded jobs", deletedBroadcasts.Load())
	}

	if deletedCount != 0 {
		t.Errorf("FAIL M4: authoritative deleted count = %d", deletedCount)
	}

	if completeCount != p.jobs {
		t.Errorf("FAIL M1/M4: authoritative complete count = %d, want %d", completeCount, p.jobs)
	}

	// M5: the user-facing symptom was heavy `wr status` STALLING for many seconds
	// (manager "unresponsive, needs kill -9"). The genuine acceptance is that
	// status stays RESPONSIVE under saturation - it must not enter the
	// multi-second freeze. We assert an absolute responsiveness bound rather than
	// a ratio: the single-reader socket is architecturally unchanged by Option R
	// (decoupling it is out-of-scope Idea 2), so the ratio off a sub-millisecond
	// idle baseline naturally grows with connection count while absolute latency
	// stays in the low-millisecond range. The ratio is reported above for context.
	const m5ResponsiveCeiling = 500 * time.Millisecond
	if underLoadM5 > m5ResponsiveCeiling {
		t.Errorf("FAIL M5: heavy-status under load = %v exceeds responsive ceiling %v (ratio %.2fx)",
			underLoadM5, m5ResponsiveCeiling, m5ratio)
	}
}

// runnerLoop is one persistent runner connection draining jobs until none
// remain. Each reserved job is Started with our own (alive) pid so the async
// dead-confirmation can never remove it, then either held past the TTR (so the
// TTR callback flags it Lost-while-alive: the churn trigger) or archived fast;
// in both cases the successful (exit 0) archive must be accepted.
func runnerLoop(server *Server, connect func() *Client, p scaleParams, seed int,
	remaining *atomic.Int64, c *scaleCounters) {
	jq := connect()
	defer disconnect(jq)

	pid := os.Getpid()
	processed := 0

	for remaining.Load() > 0 {
		reserved, err := jq.Reserve(500 * time.Millisecond)
		if err != nil {
			c.reserveErrs.Add(1)

			continue
		}

		if reserved == nil {
			// nothing ready right now; if everything is claimed we are done.
			if remaining.Load() <= 0 {
				return
			}

			continue
		}

		if serr := jq.Started(reserved, pid); serr != nil {
			c.reserveErrs.Add(1)

			continue
		}

		c.reserved.Add(1)

		// track how many distinct times each key is reserved+started, so a
		// re-reservation of an alive-owned job (an A1 invariant violation) is
		// counted.
		cntAny, _ := c.reserveCounts.LoadOrStore(reserved.Key(), &atomic.Int64{})
		if n := cntAny.(*atomic.Int64).Add(1); n > 1 { //nolint:errcheck,forcetypeassert
			c.doubleReserve.Add(1)
		}

		// deterministically choose the cohort from a per-runner rotating counter
		// so exactly ~lostFraction of jobs are held past the TTR.
		hold := float64((seed+processed)%100)/100.0 < p.lostFraction
		processed++

		if hold {
			c.heldPastTTR.Add(1)
			holdPastTTR(server, jq, reserved, p.ttr, c)
		} else if _, terr := jq.Touch(reserved); terr != nil {
			// a healthy runner's on-time touch; a failure here is saturation churn.
			c.reserveErrs.Add(1)
		}

		successEnd := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		if aerr := jq.Archive(reserved, successEnd); aerr != nil {
			c.archiveErrs.Add(1)

			continue
		}

		c.completed.Add(1)
		remaining.Add(-1)
	}
}

// holdPastTTR touches the job once (so the server reliably records it Running
// before the TTR fires, matching a real runner that ran for a while), then stops
// touching and sleeps well past the TTR so the TTR callback flags it Lost while
// its runner (this alive process) still holds it - the exact "alive owner, TTR
// expired" churn state whose successful archive the pre-revert code discarded.
// It records whether the job was observed Lost-in-Run before archiving.
func holdPastTTR(server *Server, jq *Client, reserved *Job, ttr time.Duration, c *scaleCounters) {
	// one touch confirms Running server-side and resets the TTR, so the ensuing
	// Lost flip is a genuine "alive runner stopped touching" event and not a race
	// with the initial Started RPC still being processed under saturation.
	if _, terr := jq.Touch(reserved); terr != nil {
		c.reserveErrs.Add(1)
	}

	time.Sleep(ttr + ttr/2)

	item, err := server.q.Get(reserved.Key())
	if err != nil || item == nil {
		return
	}

	inRun := item.Stats().State == queue.ItemStateRun

	lost := false

	if j, ok := item.Data().(*Job); ok {
		j.RLock()
		lost = j.Lost
		j.RUnlock()
	}

	if inRun && lost {
		c.observedLost.Add(1)
	}
}

// pollStatusSubscription drains the web-UI status-details subscription tightly,
// counting JobStateDeleted (M4) and JobStateComplete broadcasts, until stopped.
func pollStatusSubscription(server *Server, subID string, stop <-chan struct{},
	done chan<- struct{}, deleted, complete *atomic.Int64) {
	defer close(done)

	for {
		select {
		case <-stop:
			return
		default:
		}

		updates, err := server.waitForSubscriptionUpdates(subID, 200*time.Millisecond)
		if err != nil {
			select {
			case <-stop:
				return
			default:
				return
			}
		}

		for _, u := range updates {
			switch u.State {
			case JobStateDeleted:
				deleted.Add(1)
			case JobStateComplete:
				complete.Add(1)
			default:
			}
		}
	}
}

// heavyStatus runs the heavy GetByRepGroup/AllItems status path (includeComplete
// + includeStatusDetails), the same work `wr status -i <rg>` does.
func heavyStatus(jq *Client, rg string) *RepGroupStatus {
	summaries, err := jq.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, true)
	if err != nil {
		return nil
	}

	return summaries[rg]
}

// measureHeavyStatus times n heavy-status calls from a fresh connection and
// returns the median latency.
func measureHeavyStatus(connect func() *Client, rg string, n int) time.Duration {
	jq := connect()
	defer disconnect(jq)

	samples := make([]time.Duration, 0, n)

	for range n {
		t0 := time.Now()
		_ = heavyStatus(jq, rg)

		samples = append(samples, time.Since(t0))
	}

	return median(samples)
}

// sampleHeavyStatusUnderLoad repeatedly times heavy-status calls while runners
// are still churning (remaining > 0), returning the median under-load latency.
func sampleHeavyStatusUnderLoad(connect func() *Client, rg string, n int,
	remaining *atomic.Int64) time.Duration {
	jq := connect()
	defer disconnect(jq)

	samples := make([]time.Duration, 0, n)

	for len(samples) < n {
		if remaining.Load() <= 0 {
			break
		}

		t0 := time.Now()
		_ = heavyStatus(jq, rg)

		samples = append(samples, time.Since(t0))

		time.Sleep(100 * time.Millisecond)
	}

	if len(samples) == 0 {
		return 0
	}

	return median(samples)
}

func median(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	return samples[len(samples)/2]
}
