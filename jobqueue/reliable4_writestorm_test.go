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

// This file is a FAST, deterministic, in-process reproducer for the reliable4
// production FREEZE root cause (see .docs/reliable4/prod-freeze-pprof-diagnosis.md).
//
// PROD FAILURE (measured by live pprof on the real prod manager): un-suspending a
// large batch flips ~100k jobs' state at once. Each state change persists
// best-effort to bolt via updateJobAfterChange -> launchJobChangeUpdate (and each
// exit via updateJobAfterExit -> launchJobExitUpdate), which spawn ONE NEW
// GOROUTINE PER CHANGE doing db.bolt.Batch(...), with no bound and no backpressure.
// At freeze onset the goroutine dump showed 119,698 goroutines, 114,459 of them
// blocked in bbolt.(*DB).Batch. bbolt's batch-coalescing then collapses into
// thousands of tiny fsync'd write-txns serialised on the single write lock, and
// the SYNCHRONOUS archiveJob commit blocks behind them past the client's 60s
// receive floor -> jobs falsely lost -> re-reserved -> archive rejected -> churn.
//
// This test isolates the PRIMARY mechanism (the unbounded goroutine spawn) with no
// big DB, no LSF and no manager, fully deterministically:
//
//   - open a fresh backups-off db (the real initDB path);
//   - seed N jobs into the live bucket so updateJobAfterChange's "only Put if the
//     key is still live" guard (db.go:2425) passes;
//   - HOLD the bbolt write transaction open, so every best-effort commit blocks in
//     beginRWTx exactly as prod's slow freelist/spill commits made them pile up;
//   - fire a simultaneous N-job state-change burst from a helper goroutine, and
//     sample the PEAK goroutine count while the write lock is held.
//
// With the current unbounded code the count climbs by ~N (one goroutine per
// change, all stuck in db.bolt.Batch). The fix (a bounded, coalescing,
// dedup-by-key single writer) enqueues DATA, not goroutines, so the count stays
// O(1) whatever the exact design - and because the burst runs in its own
// goroutine and the lock is released once the count settles, a fix that applies
// backpressure cannot deadlock the test.
//
// It also asserts the fix stays CORRECT: after the write lock is released and the
// writes drain, every job's LATEST state is persisted (best-effort latest-wins),
// so a "fix" that keeps goroutines bounded merely by dropping writes fails too.
//
// These are plain main-suite tests (no build tag) so `make test` runs them: they
// are the RED tests for the primary-fix /bugfix and its GREEN regression guard.
// The faithful full-freeze reproduction (archive latency crossing 60s on the real
// freelist-bloated prod.db) lives in developers/wrdev.sh unsuspend-burst.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
)

const (
	// reliable4WSBurst is how many jobs change state simultaneously in the burst.
	// Prod saw ~100k; a few thousand here is far above any sane bound yet keeps the
	// test well under a second.
	reliable4WSBurst = 5000

	// reliable4WSGoroutineBound is the most extra goroutines the best-effort write
	// path may hold in flight during the burst. The fix routes all best-effort
	// change/exit writes through a bounded writer (or small worker pool), so the
	// added goroutines are O(1); this bound leaves generous headroom for any
	// reasonable bounded design while staying an order of magnitude below the burst
	// size, so the current one-goroutine-per-change code (peak ~= burst) fails it.
	reliable4WSGoroutineBound = 512

	// reliable4WSDrainTimeout bounds the post-release drain of the queued writes.
	reliable4WSDrainTimeout = 60 * time.Second

	// reliable4WSSample is the goroutine-count sampling interval.
	reliable4WSSample = 5 * time.Millisecond

	// reliable4WSSettleChecks is how many consecutive equal samples mean the burst
	// has fully spawned/enqueued and the goroutine count has settled.
	reliable4WSSettleChecks = 8

	// reliable4WSObserveCap hard-caps how long we sample the peak, so the test can
	// never hang even if a (correct, backpressuring) fix blocks the burst goroutine.
	reliable4WSObserveCap = 8 * time.Second
)

// TestReliable4WriteStormGoroutineExplosion proves that a simultaneous job-state
// change burst spawns O(N) concurrent DB-write goroutines (the freeze's primary
// mechanism), and that the writes are nonetheless persisted latest-wins.
//
// RED on current code (peak added goroutines ~= burst size); GREEN after the
// bounded single-writer fix (peak added goroutines <= reliable4WSGoroutineBound).
func TestReliable4WriteStormGoroutineExplosion(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	jobs := reliable4WSSeedLiveJobs(t, ctx, database, reliable4WSBurst)

	added := reliable4WSMeasureBurst(t, database, func() {
		for _, job := range jobs {
			job.Lock()
			job.State = JobStateReserved
			job.StartTime = time.Now()
			job.Unlock()

			database.updateJobAfterChange(ctx, job)
		}
	})

	if added > reliable4WSGoroutineBound {
		t.Errorf("write-storm goroutine explosion: a %d-job state-change burst held %d "+
			"concurrent best-effort DB-write goroutines in flight (bound %d). The best-effort "+
			"change/exit persistence must be bounded (a coalescing single writer), not one "+
			"unbounded `go db.bolt.Batch` per change.", reliable4WSBurst, added, reliable4WSGoroutineBound)
	}

	reliable4WSAssertPersisted(t, database, jobs)
}

// TestReliable4WriteStormExitExplosion is the exit-update twin of the change
// test: updateJobAfterExit -> launchJobExitUpdate (db.go:2276) spawns the same
// unbounded `go db.bolt.Batch` per exiting job (the dump counted 9,076 of these
// alongside the change-updates). The fix must bound this lane too.
func TestReliable4WriteStormExitExplosion(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	jobs := reliable4WSSeedLiveJobs(t, ctx, database, reliable4WSBurst)

	added := reliable4WSMeasureBurst(t, database, func() {
		for _, job := range jobs {
			job.Lock()
			job.State = JobStateComplete
			job.Exited = true
			job.EndTime = time.Now()
			job.Unlock()

			database.updateJobAfterExit(ctx, job, nil, nil, false)
		}
	})

	if added > reliable4WSGoroutineBound {
		t.Errorf("exit-update goroutine explosion: a %d-job exit burst held %d concurrent "+
			"best-effort DB-write goroutines in flight (bound %d); the exit lane must be bounded too.",
			reliable4WSBurst, added, reliable4WSGoroutineBound)
	}
}

// reliable4WSMeasureBurst holds the single bbolt write transaction open, runs
// burst in its own goroutine, samples the peak goroutine count until it settles
// (or a hard cap), then releases the lock and drains. It returns the peak number
// of goroutines added over the pre-burst baseline.
//
// Holding the write tx makes every best-effort commit block in beginRWTx, so the
// current code's per-change goroutines accumulate deterministically (they cannot
// drain). Running the burst in a goroutine and releasing on settle (not on burst
// completion) means a fix that applies backpressure - blocking the burst goroutine
// once its bounded queue fills - is measured correctly (added ~= O(1)) rather than
// hanging the test.
func reliable4WSMeasureBurst(t *testing.T, database *db, burst func()) int {
	t.Helper()

	holdTx, err := database.bolt.Begin(true)
	if err != nil {
		t.Fatalf("failed to open blocking write tx: %v", err)
	}

	baseline := reliable4WSStableGoroutines()

	burstDone := make(chan struct{})

	go func() {
		burst()
		close(burstDone)
	}()

	peak := reliable4WSSamplePeak(baseline)
	added := peak - baseline

	t.Logf("WRITESTORM: burst=%d baselineGoroutines=%d peakGoroutines=%d added=%d bound=%d",
		reliable4WSBurst, baseline, peak, added, reliable4WSGoroutineBound)

	// Release the write lock so the queued writes (and any backpressured burst
	// goroutine) can proceed, then wait for the burst and the drain to finish.
	if errr := holdTx.Rollback(); errr != nil {
		t.Fatalf("failed to roll back blocking write tx: %v", errr)
	}

	<-burstDone
	database.wg.Wait(reliable4WSDrainTimeout)

	return added
}

// openReliable4WriteStormDB opens a fresh, BACKUPS-OFF db in a temp dir via the
// real initDB, so no backup ticker perturbs the goroutine baseline.
func openReliable4WriteStormDB(t *testing.T, ctx context.Context) *db {
	t.Helper()

	tmpdir := t.TempDir()

	database, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}

	if database == nil {
		t.Fatal("initDB returned a nil db")
	}

	return database
}

// reliable4WSSeedLiveJobs stores n jobs with distinct keys into the live bucket
// and waits for the store to quiesce, so the burst's updateJobAfterChange guard
// (only Put if the key is still live) passes for every job.
func reliable4WSSeedLiveJobs(t *testing.T, ctx context.Context, database *db, n int) []*Job {
	t.Helper()

	jobs := make([]*Job, n)
	for i := range jobs {
		job := testDBJob(fmt.Sprintf("echo writestorm %d", i), "reliable4writestorm")
		job.State = JobStateReady
		jobs[i] = job
	}

	if _, _, _, err := database.storeNewJobs(ctx, jobs, false); err != nil {
		t.Fatalf("storeNewJobs failed: %v", err)
	}

	database.wg.Wait(reliable4WSDrainTimeout)

	return jobs
}

// reliable4WSStableGoroutines returns a settled goroutine count (waits for two
// consecutive equal samples), so the baseline is not skewed by transient startup
// goroutines.
func reliable4WSStableGoroutines() int {
	prev := runtime.NumGoroutine()

	for range 200 {
		time.Sleep(reliable4WSSample)

		cur := runtime.NumGoroutine()
		if cur == prev {
			return cur
		}

		prev = cur
	}

	return prev
}

// reliable4WSSamplePeak samples the goroutine count until it has been stable for
// reliable4WSSettleChecks consecutive samples (the burst has fully spawned or
// enqueued and the count is no longer moving), or until the hard observation cap,
// returning the peak seen over the baseline window.
func reliable4WSSamplePeak(baseline int) int {
	peak := baseline
	stable := 0
	prev := -1
	deadline := time.Now().Add(reliable4WSObserveCap)

	for time.Now().Before(deadline) {
		cur := runtime.NumGoroutine()
		if cur > peak {
			peak = cur
		}

		if cur == prev { //nolint:nestif // pre-existing settle-counter; logic unchanged
			stable++
			if stable >= reliable4WSSettleChecks {
				return peak
			}
		} else {
			stable = 0
		}

		prev = cur

		time.Sleep(reliable4WSSample)
	}

	return peak
}

// reliable4WSAssertPersisted checks that every job's latest state reached the live
// bucket (best-effort latest-wins), guarding against a "fix" that keeps goroutines
// bounded merely by dropping writes. It reads the live bucket back via the real
// recovery decode path.
func reliable4WSAssertPersisted(t *testing.T, database *db, jobs []*Job) {
	t.Helper()

	recovered, err := database.recoverIncompleteJobs()
	if err != nil {
		t.Fatalf("recoverIncompleteJobs failed: %v", err)
	}

	states := make(map[string]JobState, len(recovered))
	for _, job := range recovered {
		states[job.Key()] = job.State
	}

	missing := 0

	for _, job := range jobs {
		got, ok := states[job.Key()]
		if !ok {
			missing++

			continue
		}

		if got != job.State {
			t.Errorf("job %s persisted state=%v, want latest state=%v (best-effort must be latest-wins)",
				job.Key(), got, job.State)

			return
		}
	}

	if missing > 0 {
		t.Errorf("%d/%d jobs' latest state was not persisted; best-effort writes must not be silently dropped",
			missing, len(jobs))
	}
}
