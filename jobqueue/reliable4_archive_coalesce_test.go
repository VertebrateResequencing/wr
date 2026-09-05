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

// FAST, deterministic, in-process reproducer for reliable4 FINDING 2 (see
// .docs/reliable4/prod-run-20260817.md): the ARCHIVE path is serialised on the
// single bbolt write lock, ONE TRANSACTION PER JOB.
//
// PROD FAILURE (live block/mutex profiles, 2026-08-17, ~660 concurrent runners):
// Server.archiveCompletedJob was 25.5% then 34.75% of ALL block delay, 100% of it
// inside db.archiveJob, with a mean block of 17.1s rising to 43.0s against the 60s
// ClientMinRequestTimeout floor; 99.93% of mutex hold time was the bbolt db.rwlock.
// The archive queue sat persistently ~600 deep and drained at only ~12/s because
// 234 concurrent bbolt.(*batch).run goroutines were counted, ie. the pending
// archives were spread over HUNDREDS of separate write transactions rather than
// coalesced into a few. bbolt's Batch() cannot fix this: it detaches db.batch the
// instant a batch STARTS and arms a fresh MaxBatchDelay timer, so whenever
// archives arrive further apart than MaxBatchDelay (prod: ~83ms apart, delay 10ms)
// every single archive gets a transaction of its own, and they queue behind each
// other on the one write lock. Runner-side "receive time out" then stops the
// touches, the job is falsely lost, and a successful compress lands in `delayed`
// with Exitcode 0.
//
// These tests reproduce that regime deterministically, with no big DB, no LSF and
// no manager, using the prod-inert archiveTxObserver seam (db.go) to
//
//   - COUNT the distinct write transactions the archives are applied in, and
//   - make each transaction artificially expensive (the seam runs inside the write
//     transaction), standing in for prod's ~80ms freelist-bound commit on a 10GB DB.
//
// The reproducing shape is arrival SPREAD, not arrival volume: arrivals paced
// further apart than bbolt's 10ms MaxBatchDelay but far closer together than a
// commit takes. Pre-fix that is one transaction per archive; post-fix (a
// coalescing archive writer that folds every pending archive into ONE db.Update
// and replies to each waiter individually) it is one transaction per commit
// window, whatever the archive count.
//
// Plain main-suite tests (no build tag), so `make test` runs them: the RED test
// for the coalescing-archive-writer /bugfix and its GREEN regression guard. The
// sustained-rate 10GB-class gate lives in developers/wrdev.sh archive-rate.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// reliable4ACArchives is how many archives the coalescing test drives. It only
	// needs to be well above the number of commit windows the submission spans, so
	// that "one transaction per archive" and "one transaction per commit window"
	// are unmistakably different numbers.
	reliable4ACArchives = 100

	// reliable4ACInterval paces the arrivals. It is deliberately LONGER than
	// bbolt's 10ms MaxBatchDelay (so bbolt's own batching cannot coalesce them -
	// the prod regime) and far SHORTER than reliable4ACCommitCost (so a real queue
	// of pending archives forms, as prod's ~600-deep one did).
	reliable4ACInterval = 20 * time.Millisecond

	// reliable4ACCommitCost is the artificial cost of one archive write
	// transaction, standing in for prod's freelist-bound commit on the 10.3GB DB.
	// Charged once per transaction (not per archive), so batching genuinely pays.
	reliable4ACCommitCost = 200 * time.Millisecond

	// reliable4ACTxBudget is the most write transactions reliable4ACArchives
	// archives may cost. A coalescing writer needs about
	// (reliable4ACArchives*reliable4ACInterval)/reliable4ACCommitCost = 10 of them
	// (plus slack for the trailing drain and for a loaded host), whereas the
	// pre-fix per-job transaction costs reliable4ACArchives. This is a COUNT, not
	// a wall-clock bound, so it is robust on a heavily loaded shared node: extra
	// host load makes commits slower, ie. fewer transactions, never more.
	reliable4ACTxBudget = reliable4ACArchives / 4

	// reliable4ACBatchJobs is how many archives the per-job-error test folds into
	// one transaction alongside the failing one.
	reliable4ACBatchJobs = 20

	// reliable4ACQueueSettle is how long the error/close tests allow for already
	// launched archive goroutines to reach the write path, while a transaction they
	// must queue behind is deliberately held open.
	reliable4ACQueueSettle = 2 * time.Second

	// reliable4ACWaitTimeout bounds every wait in these tests, so a lost reply or
	// an undrained queue fails loudly instead of hanging the suite.
	reliable4ACWaitTimeout = 3 * time.Minute
)

// reliable4ACMaxLatency is a generous absolute ceiling on a single archive's
// latency: well under the 60s ClientMinRequestTimeout floor that, once crossed,
// makes a successful job land in `delayed`. ClientMinRequestTimeout is a var, so
// this cannot be a const.
//
//nolint:gochecknoglobals // derived from the ClientMinRequestTimeout var.
var reliable4ACMaxLatency = ClientMinRequestTimeout / 2

// TestReliable4ArchivesCoalesceIntoOneTransaction proves the archive path costs
// O(1) write transactions per commit window rather than O(1) per archive, and
// that no single archive is starved anywhere near the client timeout floor.
//
// RED on the pre-fix code (one transaction per archive, because each arrival
// lands outside the previous batch's 10ms window); GREEN with the coalescing
// archive writer.
func TestReliable4ArchivesCoalesceIntoOneTransaction(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	rec := newArchiveTxRecorder(t)
	rec.commitCost = reliable4ACCommitCost

	jobs := reliable4ACCompletableJobs(t, ctx, database, "coalesce", reliable4ACArchives)

	latencies, errs := reliable4ACPacedArchives(ctx, database, jobs, reliable4ACInterval)

	txns, folded := rec.transactions()
	mean, maxLat := reliable4ACLatencyStats(latencies)

	t.Logf("ARCHIVE-COALESCE: archives=%d arrivalInterval=%s commitCost=%s transactions=%d "+
		"biggestTransaction=%d archives/tx=%.1f meanLatency=%s maxLatency=%s txBudget=%d",
		len(jobs), reliable4ACInterval, reliable4ACCommitCost, txns, folded,
		float64(len(jobs))/float64(max(txns, 1)), mean.Round(time.Millisecond),
		maxLat.Round(time.Millisecond), reliable4ACTxBudget)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("archive %d failed: %v", i, err)
		}
	}

	if txns > reliable4ACTxBudget {
		t.Errorf("archives are not coalesced: %d archives arriving %s apart cost %d separate bolt write "+
			"transactions (budget %d, biggest transaction held only %d of them). Pending archives must be "+
			"folded into ONE db.Update per commit and each waiter replied to individually; bbolt's Batch() "+
			"cannot do it because it detaches its batch as soon as one starts, so arrivals further apart "+
			"than MaxBatchDelay each get their own transaction and queue on the single write lock (prod: "+
			"~600 deep, ~12/s, mean block 43s of the 60s client budget).",
			len(jobs), reliable4ACInterval, txns, reliable4ACTxBudget, folded)
	}

	if maxLat > reliable4ACMaxLatency {
		t.Errorf("archive starvation: the slowest of %d archives took %s, over the %s ceiling (the %s client "+
			"timeout floor is what turns a successful job into a `delayed` one)",
			len(jobs), maxLat.Round(time.Millisecond), reliable4ACMaxLatency, ClientMinRequestTimeout)
	}

	reliable4ACAssertArchived(t, database, jobs)
}

// TestReliable4ArchiveErrorsStayPerJob proves coalescing preserves per-job error
// semantics: one archive that cannot be applied must not fail its batch-mates.
// This mirrors bbolt.Batch's own behaviour (drop the offender, re-run the rest as
// a batch, and re-run the offender alone so its caller gets its own error).
//
// The bad archive is given an empty key, which fails deterministically inside the
// transaction (bolt's Put rejects an empty key), and is enqueued FIRST so that a
// naive "fold everything into one db.Update" would roll back - and so report the
// bad job's error to - every good archive behind it.
func TestReliable4ArchiveErrorsStayPerJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	rec := newArchiveTxRecorder(t)
	rec.gateFirstTx()

	good := reliable4ACCompletableJobs(t, ctx, database, "perjob", reliable4ACBatchJobs)
	blocker := reliable4ACCompletableJobs(t, ctx, database, "perjob-blocker", 1)
	bad := reliable4ACCompletableJobs(t, ctx, database, "perjob-bad", 1)[0]

	// hold the first archive's transaction open, so everything submitted next
	// queues up and is offered to the write path together.
	blockerDone := reliable4ACArchiveAsync(ctx, database, blocker[0].Key(), blocker[0])

	rec.awaitTx(t)

	var (
		wg      sync.WaitGroup
		goodErr = make([]error, len(good))
		badErr  error
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		badErr = database.archiveJob(ctx, "", bad)
	}()

	time.Sleep(reliable4ACQueueSettle)

	for i, job := range good {
		wg.Add(1)

		go func() {
			defer wg.Done()

			goodErr[i] = database.archiveJob(ctx, job.Key(), job)
		}()
	}

	time.Sleep(reliable4ACQueueSettle)
	rec.releaseGate()

	reliable4ACWait(t, &wg)

	if err := reliable4ACAwait(t, blockerDone); err != nil {
		t.Errorf("the blocking archive failed: %v", err)
	}

	txns, folded := rec.transactions()
	t.Logf("ARCHIVE-PERJOB: goodArchives=%d transactions=%d biggestTransaction=%d badErr=%v",
		len(good), txns, folded, badErr)

	if badErr == nil {
		t.Error("the unarchivable job's caller got no error, so a per-job failure is being hidden")
	}

	for i, err := range goodErr {
		if err != nil {
			t.Fatalf("good archive %d was failed by the unarchivable archive queued ahead of it (%v): one "+
				"bad job must not fail the whole batch - mirror bbolt.Batch, drop the offender, re-run the "+
				"rest, and re-run the offender alone", i, err)
		}
	}

	if folded < len(good) {
		t.Errorf("the good archives were not re-run together after the bad one was dropped: the biggest "+
			"transaction held %d of %d (transactions=%d). Recovering from a per-job error must not degrade "+
			"the batch into one transaction per archive", folded, len(good), txns)
	}

	reliable4ACAssertArchived(t, database, good)
	reliable4ACAssertArchived(t, database, blocker)
}

// TestReliable4ArchivePanicStaysPerJob proves a malformed archive that PANICS
// inside the write transaction fails only its own caller. bbolt.Batch used to give
// this for free (safelyCall turned a panic into that call's error); a plain
// db.Update does not, so without applyArchiveOp's recover one bad job would take
// the whole manager down along with every archive folded in beside it.
func TestReliable4ArchivePanicStaysPerJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	good := reliable4ACCompletableJobs(t, ctx, database, "panic", reliable4ACBatchJobs)
	blocker := reliable4ACCompletableJobs(t, ctx, database, "panic-blocker", 1)
	bad := reliable4ACCompletableJobs(t, ctx, database, "panic-bad", 1)[0]
	badKey := bad.Key()

	// gate the first transaction so everything submitted after it queues up
	// together, and panic when the bad job's archive is applied.
	gate := make(chan struct{})
	gated := make(chan struct{}, 1)

	var once sync.Once

	archiveTxObserver = func(_ int, key []byte) {
		if string(key) == badKey {
			panic("reliable4 archive panic test")
		}

		once.Do(func() {
			gated <- struct{}{}

			<-gate
		})
	}

	t.Cleanup(func() { archiveTxObserver = nil })

	blockerDone := reliable4ACArchiveAsync(ctx, database, blocker[0].Key(), blocker[0])

	select {
	case <-gated:
	case <-time.After(reliable4ACWaitTimeout):
		t.Fatal("no archive write transaction started")
	}

	var (
		wg      sync.WaitGroup
		goodErr = make([]error, len(good))
		badErr  error
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		badErr = database.archiveJob(ctx, badKey, bad)
	}()

	time.Sleep(reliable4ACQueueSettle)

	for i, job := range good {
		wg.Add(1)

		go func() {
			defer wg.Done()

			goodErr[i] = database.archiveJob(ctx, job.Key(), job)
		}()
	}

	time.Sleep(reliable4ACQueueSettle)
	close(gate)

	reliable4ACWait(t, &wg)

	if err := reliable4ACAwait(t, blockerDone); err != nil {
		t.Errorf("the blocking archive failed: %v", err)
	}

	t.Logf("ARCHIVE-PANIC: goodArchives=%d badErr=%v", len(good), badErr)

	if !errors.Is(badErr, errArchivePanic) {
		t.Errorf("a panicking archive gave its caller %v, want an error wrapping errArchivePanic", badErr)
	}

	for i, err := range goodErr {
		if err != nil {
			t.Fatalf("good archive %d was failed by a panicking batch-mate (%v): a panic must fail only "+
				"the archive that caused it", i, err)
		}
	}

	reliable4ACAssertArchived(t, database, good)
	reliable4ACAssertArchived(t, database, blocker)
}

// TestReliable4ArchiveCloseDrains proves close() drains archives that are still
// in flight: every waiter is replied to (no hang, no lost reply), every archive
// that reported success is durably in the complete bucket, and close() itself
// returns.
func TestReliable4ArchiveCloseDrains(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	rec := newArchiveTxRecorder(t)
	rec.gateFirstTx()

	// bolt clears its path on Close, so remember it while the database is open.
	path := database.bolt.Path()

	blocker := reliable4ACCompletableJobs(t, ctx, database, "close-blocker", 1)
	jobs := reliable4ACCompletableJobs(t, ctx, database, "close", reliable4ACBatchJobs)

	blockerDone := reliable4ACArchiveAsync(ctx, database, blocker[0].Key(), blocker[0])

	rec.awaitTx(t)

	var (
		wg   sync.WaitGroup
		errs = make([]error, len(jobs))
	)

	for i, job := range jobs {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = database.archiveJob(ctx, job.Key(), job)
		}()
	}

	time.Sleep(reliable4ACQueueSettle)

	closed := make(chan error, 1)

	go func() { closed <- database.close(ctx) }()

	// let close() reach its drain, then let the held transaction finish.
	time.Sleep(reliable4ACQueueSettle)
	rec.releaseGate()

	reliable4ACWait(t, &wg)

	if err := reliable4ACAwait(t, closed); err != nil {
		t.Errorf("close() with archives in flight failed: %v", err)
	}

	if err := reliable4ACAwait(t, blockerDone); err != nil {
		t.Errorf("the blocking archive failed: %v", err)
	}

	survived := make([]*Job, 0, len(jobs))

	for i, err := range errs {
		if err == nil {
			survived = append(survived, jobs[i])
		}
	}

	t.Logf("ARCHIVE-CLOSE-DRAIN: inFlight=%d archivedBeforeClose=%d", len(jobs), len(survived))

	if len(survived) != len(jobs) {
		t.Errorf("%d/%d archives already queued when close() was called were dropped; close() must drain "+
			"the archive queue (no lost archive, no hung waiter)", len(jobs)-len(survived), len(jobs))
	}

	reliable4ACAssertReopenedArchived(t, database, path, survived)
}

// TestReliable4ArchiveBeatsResurrectingChange guards the archive-vs-change race
// across the two writers. A best-effort change/exit write only rewrites a job that
// is still in the live bucket (beBatch.applyChanges and jobExitData.update), so a
// "started" update cannot resurrect a job the archive already removed. Folding
// archives into a coalescing writer changes WHEN an archive commits, so this
// asserts the guard still holds: whichever of the two transactions commits first,
// the job ends up archived and gone from the live bucket.
func TestReliable4ArchiveBeatsResurrectingChange(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	rec := newArchiveTxRecorder(t)
	rec.gateFirstTx()

	jobs := reliable4ACCompletableJobs(t, ctx, database, "resurrect", 2)
	changed, exited := jobs[0], jobs[1]

	// hold the archives' transaction open, then queue a change for one job and an
	// exit for the other, so both best-effort writes are pending against jobs that
	// are about to be archived.
	archived := make(chan error, 2)

	go func() { archived <- database.archiveJob(ctx, changed.Key(), changed) }()

	rec.awaitTx(t)

	go func() { archived <- database.archiveJob(ctx, exited.Key(), exited) }()

	time.Sleep(reliable4ACQueueSettle)

	changed.Lock()
	changed.State = JobStateReserved
	changed.Unlock()

	database.updateJobAfterChange(ctx, changed)
	database.updateJobAfterExit(ctx, exited, []byte("stdout"), []byte("stderr"), true)

	time.Sleep(reliable4ACQueueSettle)
	rec.releaseGate()

	for range 2 {
		if err := reliable4ACAwait(t, archived); err != nil {
			t.Errorf("archive failed: %v", err)
		}
	}

	database.wg.Wait(reliable4ACWaitTimeout)

	reliable4ACAssertArchived(t, database, jobs)
}

// reliable4ACPacedArchives archives every job in its own goroutine, launching
// them interval apart on an ABSOLUTE schedule (so the submission window is fixed
// even when a loaded host oversleeps: it catches up rather than stretching). It
// returns each archive's latency and error, in job order.
func reliable4ACPacedArchives(ctx context.Context, database *db, jobs []*Job,
	interval time.Duration,
) ([]time.Duration, []error) {
	var wg sync.WaitGroup

	latencies := make([]time.Duration, len(jobs))
	errs := make([]error, len(jobs))
	start := time.Now()

	for i, job := range jobs {
		time.Sleep(time.Until(start.Add(time.Duration(i) * interval)))

		wg.Add(1)

		go func() {
			defer wg.Done()

			t0 := time.Now()
			errs[i] = database.archiveJob(ctx, job.Key(), job)
			latencies[i] = time.Since(t0)
		}()
	}

	wg.Wait()

	return latencies, errs
}

// reliable4ACArchiveAsync archives one job in its own goroutine, returning a
// channel that yields its error.
func reliable4ACArchiveAsync(ctx context.Context, database *db, key string, job *Job) chan error {
	done := make(chan error, 1)

	go func() { done <- database.archiveJob(ctx, key, job) }()

	return done
}

// reliable4ACLatencyStats returns the mean and maximum of the given latencies.
func reliable4ACLatencyStats(latencies []time.Duration) (time.Duration, time.Duration) {
	if len(latencies) == 0 {
		return 0, 0
	}

	var total, maxLat time.Duration

	for _, lat := range latencies {
		total += lat

		if lat > maxLat {
			maxLat = lat
		}
	}

	return total / time.Duration(len(latencies)), maxLat
}

// reliable4ACCompletableJobs stores n fresh live jobs and marks each as
// successfully exited with distinct end times, so archiving them does the full
// end-time-index and stat work the server's archive path does.
func reliable4ACCompletableJobs(t *testing.T, ctx context.Context, database *db, prefix string, n int) []*Job {
	t.Helper()

	jobs := make([]*Job, n)
	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo archive-coalesce %s %d", prefix, i), "reliable4archive")
	}

	if _, _, _, err := database.storeNewJobs(ctx, jobs, false); err != nil {
		t.Fatalf("storeNewJobs failed: %v", err)
	}

	start := time.Now()

	for i, job := range jobs {
		job.Lock()
		job.State = JobStateComplete
		job.Exited = true
		job.StartTime = start
		job.EndTime = start.Add(time.Second).Add(time.Duration(i) * time.Microsecond)
		job.PeakRAM = 100
		job.Unlock()
	}

	database.wg.Wait(reliable4ACWaitTimeout)

	return jobs
}

// reliable4ACAssertArchived checks every job is in the live database's complete
// bucket and gone from its live bucket.
func reliable4ACAssertArchived(t *testing.T, database *db, jobs []*Job) {
	t.Helper()

	reliable4ACAssertArchivedIn(t, database.bolt, jobs)
}

// reliable4ACAssertReopenedArchived closes the database (if it is not already
// closed) and reopens the underlying bolt file, so durability is checked against
// what actually reached disk rather than against a still-open handle.
func reliable4ACAssertReopenedArchived(t *testing.T, database *db, path string, jobs []*Job) {
	t.Helper()

	_ = database.close(context.Background())

	reopened, err := bolt.Open(path, dbFilePermission, nil)
	if err != nil {
		t.Fatalf("failed to reopen the database file: %v", err)
	}

	defer func() { _ = reopened.Close() }()

	reliable4ACAssertArchivedIn(t, reopened, jobs)
}

// reliable4ACAssertArchivedIn checks every job is in bdb's complete bucket and
// gone from its live bucket, reporting a count rather than asserting per job.
func reliable4ACAssertArchivedIn(t *testing.T, bdb *bolt.DB, jobs []*Job) {
	t.Helper()

	var missing, stillLive int

	err := bdb.View(func(tx *bolt.Tx) error {
		complete := tx.Bucket(bucketJobsComplete)
		live := tx.Bucket(bucketJobsLive)

		for _, job := range jobs {
			key := []byte(job.Key())

			if complete.Get(key) == nil {
				missing++
			}

			if live.Get(key) != nil {
				stillLive++
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("failed to read back the archived jobs: %v", err)
	}

	if missing > 0 {
		t.Errorf("%d/%d archived jobs are not in the complete bucket: coalescing must not lose an archive "+
			"whose caller was told it succeeded", missing, len(jobs))
	}

	if stillLive > 0 {
		t.Errorf("%d/%d archived jobs are still in the live bucket", stillLive, len(jobs))
	}
}

// reliable4ACWait waits for wg with a timeout, so a lost reply fails loudly
// instead of hanging.
func reliable4ACWait(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()

	done := make(chan struct{})

	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(reliable4ACWaitTimeout):
		t.Fatalf("archive callers did not all get a reply within %s: a coalescing writer must reply to "+
			"EVERY waiter individually", reliable4ACWaitTimeout)
	}
}

// reliable4ACAwait reads one error from ch with a timeout.
func reliable4ACAwait(t *testing.T, ch chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(reliable4ACWaitTimeout):
		t.Fatalf("timed out after %s waiting for an archive/close reply", reliable4ACWaitTimeout)

		return nil
	}
}

// archiveTxRecorder observes archive transactional work through the prod-inert
// archiveTxObserver seam. It counts the DISTINCT write transactions archives are
// applied in (bolt's Tx.ID is unique per write transaction) and how many archives
// shared each of them, can charge an artificial per-transaction commit cost
// (standing in for prod's freelist-bound commit on a multi-GB DB), and can hold
// the first transaction open so a queue of pending archives forms behind it.
type archiveTxRecorder struct {
	mu sync.Mutex

	// perTx counts the archives each write transaction applied, keyed on its
	// Tx.ID. Only the counts are kept, so a long benchmark retains nothing
	// per-archive.
	perTx map[int]int

	// commitCost is slept once per NEW transaction, inside it, so batching a
	// transaction's worth of archives genuinely pays for itself.
	commitCost time.Duration

	gate     chan struct{}
	gateUsed bool
	started  chan struct{}
}

// newArchiveTxRecorder installs a recorder for the duration of the test or
// benchmark.
func newArchiveTxRecorder(tb testing.TB) *archiveTxRecorder {
	tb.Helper()

	rec := &archiveTxRecorder{
		perTx:   make(map[int]int),
		started: make(chan struct{}, 4096),
	}

	archiveTxObserver = rec.observe

	tb.Cleanup(func() { archiveTxObserver = nil })

	return rec
}

// gateFirstTx makes the first transaction block inside itself until
// releaseGate() is called.
func (r *archiveTxRecorder) gateFirstTx() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.gate = make(chan struct{})
}

// releaseGate lets a gated transaction complete. Safe to call once.
func (r *archiveTxRecorder) releaseGate() {
	r.mu.Lock()
	gate := r.gate
	r.gate = nil
	r.mu.Unlock()

	if gate != nil {
		close(gate)
	}
}

// awaitTx blocks until another transaction has started.
func (r *archiveTxRecorder) awaitTx(tb testing.TB) {
	tb.Helper()

	select {
	case <-r.started:
	case <-time.After(reliable4ACWaitTimeout):
		tb.Fatalf("no archive write transaction started within %s", reliable4ACWaitTimeout)
	}
}

// observe records one archive's transactional work. It runs INSIDE the bolt write
// transaction, on whichever goroutine is applying it.
func (r *archiveTxRecorder) observe(txID int, _ []byte) {
	r.mu.Lock()
	_, seen := r.perTx[txID]
	isNew := !seen
	r.perTx[txID]++

	gate := r.gate
	if !isNew || r.gateUsed {
		gate = nil
	} else if gate != nil {
		r.gateUsed = true
	}
	r.mu.Unlock()

	if !isNew {
		return
	}

	select {
	case r.started <- struct{}{}:
	default:
	}

	if gate != nil {
		<-gate
	}

	if r.commitCost > 0 {
		time.Sleep(r.commitCost)
	}
}

// transactions returns how many distinct write transactions archives were applied
// in, and how many archives the biggest of them held.
func (r *archiveTxRecorder) transactions() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	biggest := 0

	for _, n := range r.perTx {
		if n > biggest {
			biggest = n
		}
	}

	return len(r.perTx), biggest
}
