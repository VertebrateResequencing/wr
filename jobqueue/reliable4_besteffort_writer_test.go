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

// This file adds behavioural regression tests for the bounded, coalescing,
// single-writer best-effort persistence that fixes the reliable4 prod freeze (see
// reliable4_writestorm_test.go and .docs/reliable4/prod-freeze-pprof-diagnosis.md).
// The writestorm tests prove the goroutine bound; these prove the other two
// properties the fix must have:
//
//   - coalescing: many change-updates to a few keys fold into a handful of write
//     transactions (dedup-by-key, latest-wins) rather than one fsync'd txn per
//     call - the tiny-txn collapse that starved the archive path in prod;
//   - clean shutdown: close() drains all still-pending best-effort writes, so a
//     shutdown mid-burst loses no writes and does not hang.
//
// They reuse the sibling writestorm helpers (same package) and its plain-test
// style: the best-effort writer goroutine mutates db fields, so goconvey's
// whole-struct fmt reflection would race it (see reliable4_backup_coordination_test).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
)

const (
	// reliable4BEKeys is how many distinct live jobs the coalescing test churns.
	reliable4BEKeys = 4

	// reliable4BEUpdatesPerKey is how many change-updates each key receives; with
	// coalescing they must fold into far fewer write transactions than the total
	// number of calls.
	reliable4BEUpdatesPerKey = 1000

	// reliable4BEMaxBoltWrites bounds the bolt write-op delta the coalescing test
	// tolerates. The fix folds all pending changes into ~2-3 transactions (a
	// handful of page writes each) regardless of the call count; the old
	// one-batch-per-change code produced thousands. This sits far above the former
	// and far below the latter.
	reliable4BEMaxBoltWrites = 500

	// reliable4BEReleaseDelay is how long the tests hold the single bbolt write lock
	// so the burst of updates genuinely piles up unpersisted before the writer can
	// drain it.
	reliable4BEReleaseDelay = 100 * time.Millisecond

	// reliable4BECloseTimeout bounds how long close() may take to drain and return.
	reliable4BECloseTimeout = 30 * time.Second
)

// TestReliable4BestEffortCoalesces proves that a storm of change-updates to a few
// keys folds into a handful of write transactions (dedup-by-key, latest-wins)
// instead of amplifying to one fsync'd transaction per call. It holds the single
// bbolt write tx open across the burst so the updates must pile into the coalescing
// map, then measures the bolt write-op count once drained.
func TestReliable4BestEffortCoalesces(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	jobs := reliable4WSSeedLiveJobs(t, ctx, database, reliable4BEKeys)

	writesBefore := boltWrites(database)

	// Hold the write tx so every enqueued change piles into the coalescing map
	// rather than being drained one-at-a-time; this is what forces the fold.
	holdTx, err := database.bolt.Begin(true)
	if err != nil {
		t.Fatalf("failed to open blocking write tx: %v", err)
	}

	for i := range reliable4BEUpdatesPerKey {
		state := JobStateRunning
		if i == reliable4BEUpdatesPerKey-1 {
			state = JobStateReserved // the final, latest-wins state per key
		}

		for _, job := range jobs {
			job.Lock()
			job.State = state
			job.StartTime = time.Now()
			job.Unlock()

			database.updateJobAfterChange(ctx, job)
		}
	}

	time.Sleep(reliable4BEReleaseDelay)

	if errr := holdTx.Rollback(); errr != nil {
		t.Fatalf("failed to roll back blocking write tx: %v", errr)
	}

	database.wg.Wait(reliable4WSDrainTimeout)

	writesDelta := boltWrites(database) - writesBefore
	totalCalls := reliable4BEKeys * reliable4BEUpdatesPerKey

	t.Logf("BE-COALESCE: calls=%d boltWriteOpsDelta=%d bound=%d", totalCalls, writesDelta, reliable4BEMaxBoltWrites)

	if writesDelta >= reliable4BEMaxBoltWrites {
		t.Errorf("best-effort writes did not coalesce: %d change-update calls produced %d bolt write ops "+
			"(bound %d); the writer must fold pending changes into a few transactions, not one per call",
			totalCalls, writesDelta, reliable4BEMaxBoltWrites)
	}

	// latest-wins: every key must be persisted in its final (Reserved) state.
	reliable4WSAssertPersisted(t, database, jobs)
}

// TestReliable4BestEffortCloseDrains proves that close() drains best-effort writes
// that are still pending, so a shutdown mid-burst loses nothing and never hangs. It
// holds the write tx so the writes are genuinely unpersisted (the single writer is
// blocked in beginRWTx) when close() is called, releases it from a goroutine so
// close()'s own drain can commit, then reopens the db and checks every latest state
// survived.
func TestReliable4BestEffortCloseDrains(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	tmpdir := t.TempDir()
	dbFile := filepath.Join(tmpdir, "queue.db")
	dbBkFile := filepath.Join(tmpdir, "queue.db.bak")

	database, _, err := initDB(ctx, dbFile, dbBkFile, internal.Development, false, false)
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}

	if database == nil {
		t.Fatal("initDB returned a nil db")
	}

	jobs := reliable4WSSeedLiveJobs(t, ctx, database, reliable4BEKeys)

	holdTx, err := database.bolt.Begin(true)
	if err != nil {
		t.Fatalf("failed to open blocking write tx: %v", err)
	}

	for _, job := range jobs {
		job.Lock()
		job.State = JobStateReserved
		job.StartTime = time.Now()
		job.Unlock()

		database.updateJobAfterChange(ctx, job)
	}

	// Release the write lock shortly, so close()'s drain (blocked in beginRWTx) can
	// commit; doing it from a goroutine exercises close() waiting for the drain.
	rollbackErr := make(chan error, 1)

	go func() {
		time.Sleep(reliable4BEReleaseDelay)

		rollbackErr <- holdTx.Rollback()
	}()

	done := make(chan error, 1)
	go func() { done <- database.close(ctx) }()

	select {
	case cerr := <-done:
		if cerr != nil {
			t.Fatalf("close returned an error: %v", cerr)
		}
	case <-time.After(reliable4BECloseTimeout):
		t.Fatal("close hung: the best-effort writer's queue was not drained/stopped on close")
	}

	if rerr := <-rollbackErr; rerr != nil {
		t.Fatalf("failed to roll back blocking write tx: %v", rerr)
	}

	// Reopen the (preserved) db file and confirm every job's latest state was
	// persisted by close()'s drain - no best-effort write was lost on shutdown.
	reopened, _, err := initDB(ctx, dbFile, dbBkFile, internal.Development, false, false)
	if err != nil {
		t.Fatalf("reopen initDB failed: %v", err)
	}

	if reopened == nil {
		t.Fatal("reopen initDB returned a nil db")
	}

	defer func() { _ = reopened.close(ctx) }()

	reliable4WSAssertPersisted(t, reopened, jobs)
}
