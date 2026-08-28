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

// FAST, deterministic, in-process reproducer and regression guard for the ADD
// path's write-transaction fragmentation (.docs/bugfixes/260828-1.md, and the
// trial log .docs/reliable4/addstorm-fix-trials.md).
//
// PROD FAILURE (profiled 2026-08-27, /nfs/hgi/wr/sb10-pprof/prof260827/): 28,815
// slow `add` warnings in 23 minutes, 20,524 of them adding a SINGLE job, p50
// 12.8s; 763 goroutines parked in DB.Batch; 85% of the bolt writer-lock delay
// under bbolt.(*batch).run. db.storeNewJobData persisted an add with
// db.bolt.Batch, and bbolt's Batch cannot coalesce this regime: it sets
// db.batch = nil at the TOP of batch.run(), BEFORE calling db.Update, so the
// instant one batch starts committing every later arrival forms a NEW batch on a
// fresh 10ms timer and opens a write transaction of its own, which then queues on
// bolt's single writer lock. bbolt coalesces WITHIN a 10ms window and never
// ACROSS a commit - backwards when a commit on a production-sized database costs
// 50-120ms.
//
// The reproducing shape is therefore arrival SPREAD against a commit that
// outlasts the batch window, not arrival volume. These tests create it without a
// big database, LSF or a manager: bolt's single write lock is HELD while the adds
// arrive (standing in for production's 50-120ms commit), and they are paced
// further apart than bbolt's 10ms MaxBatchDelay. Pre-fix that is one write
// transaction per add; post-fix (a coalescing newJobsWriter that folds every
// pending add into ONE db.Update and replies to each caller individually) it is
// one transaction per commit, whatever the add count.
//
// Write transactions are counted with bbolt's meta transaction id, as
// reliable4_add_tx_test.go does: a read transaction reports the id of the last
// COMMITTED write transaction, so the difference across a call is an exact count
// of the write transactions it committed (DB.Stats().TxN cannot see this - bbolt
// increments it in beginTx, not beginRWTx, so it counts only read transactions).
// A rolled-back transaction does not bump it, which is what lets the per-add
// error tests below say the surviving adds were re-run TOGETHER.
//
// Plain main-suite tests (no build tag), so `make test` runs them. The bound on
// how much one transaction may fold is tested separately, in
// reliable4_add_foldcap_test.go; the 10GB-class scale gate lives in
// developers/wrdev.sh add-storm.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// reliable4ACAdds is how many single-job adds the coalescing test drives. It
	// only needs to be well above the number of commits the arrival window spans,
	// so that "one transaction per add" and "one transaction per commit" are
	// unmistakably different numbers.
	reliable4ACAdds = 100

	// reliable4ACAddInterval paces the arrivals. It is deliberately LONGER than
	// bbolt's 10ms MaxBatchDelay, so bbolt's own batching cannot coalesce them -
	// production's single-job adds arrived while a commit was already in flight,
	// which comes to the same thing. The sleep is RELATIVE rather than on an
	// absolute schedule: a loaded host then stretches the window, keeping every gap
	// over MaxBatchDelay so the pre-fix cost stays one transaction per add, instead
	// of catching up and bunching arrivals into one batch, which would understate
	// the pre-fix cost.
	reliable4ACAddInterval = 20 * time.Millisecond

	// reliable4ACAddTxBudget is the most write transactions reliable4ACAdds adds
	// may cost. A coalescing writer needs two (the one already in flight when the
	// window opened, plus one for everything that arrived during it) plus slack for
	// a loaded host, whereas the pre-fix per-add transaction costs reliable4ACAdds.
	// This is a COUNT, not a wall-clock bound, so it is robust on a heavily loaded
	// shared node: extra host load makes commits slower, ie. fewer transactions,
	// never more.
	reliable4ACAddTxBudget = reliable4ACAdds / 4

	// reliable4ACAddBatchAdds is how many good adds the per-add error tests fold
	// into one transaction alongside the failing one.
	reliable4ACAddBatchAdds = 20

	// reliable4ACAddSettle is how long the tests allow for already launched adds to
	// reach the write path while the transaction they must queue behind is
	// deliberately held open. It is a settling wait, not a latency budget.
	reliable4ACAddSettle = 500 * time.Millisecond

	// reliable4ACAddWaitTimeout bounds every wait in these tests, so a lost reply
	// or an undrained queue fails loudly instead of hanging the suite.
	reliable4ACAddWaitTimeout = 3 * time.Minute

	// reliable4ACAddPoll is how often the tests sample the writer's pending queue
	// while waiting for adds to reach it.
	reliable4ACAddPoll = 2 * time.Millisecond

	// reliable4ACAddPerAddTxBudget is the most write transactions the per-add error
	// tests may cost: the blocking add's own transaction, the folded transaction the
	// failing add rolls back, and the retry of the survivors, plus slack. Recovering
	// from one add's failure must not degrade the fold into a transaction per add,
	// which would cost reliable4ACAddBatchAdds of them.
	reliable4ACAddPerAddTxBudget = 4

	// reliable4ACAddDrainOps is how many adds the bounded-final-drain test queues.
	reliable4ACAddDrainOps = 3
)

// reliable4ACAddPanickingPut stands in for an add whose bucket puts panic (as a
// malformed job's could), so that the panic happens at the same call site
// applyNewJobsOp's recover guards.
func reliable4ACAddPanickingPut(*bolt.Tx, []byte, sobsd) error {
	panic("reliable4 add panic test")
}

// TestReliable4AddsCoalesceIntoOneTransaction proves the add path costs O(1)
// write transactions per commit rather than O(1) per add, and that no single add
// is starved anywhere near the client timeout floor.
//
// RED on the pre-fix code (one transaction per add, because each arrival lands
// outside the previous batch's 10ms window while a commit is in flight); GREEN
// with the coalescing new jobs writer.
func TestReliable4AddsCoalesceIntoOneTransaction(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	jobs := reliable4ACAddJobs("coalesce", reliable4ACAdds)
	before := reliable4ACAddWriteTxID(t, database)

	// hold bolt's single write lock while the adds arrive, standing in for
	// production's 50-120ms commit: every add's own transaction then queues behind
	// it, exactly as production's 573-deep beginRWTx queue did.
	release := reliable4ACAddHoldWriteLock(t, database)
	defer release()

	wg, latencies, errs := reliable4ACAddPaced(ctx, database, jobs, reliable4ACAddInterval)

	time.Sleep(reliable4ACAddSettle)
	release()
	reliable4ACAddWait(t, wg)

	txns := reliable4ACAddWriteTxID(t, database) - before
	mean, maxLat := reliable4ACLatencyStats(latencies)

	t.Logf("ADD-COALESCE: adds=%d arrivalInterval=%s transactions=%d adds/tx=%.1f "+
		"meanLatency=%s maxLatency=%s txBudget=%d",
		len(jobs), reliable4ACAddInterval, txns, float64(len(jobs))/float64(max(txns, 1)),
		mean.Round(time.Millisecond), maxLat.Round(time.Millisecond), reliable4ACAddTxBudget)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("add %d failed: %v", i, err)
		}
	}

	if txns > reliable4ACAddTxBudget {
		t.Errorf("adds are not coalesced: %d single-job adds arriving %s apart, while one commit was in "+
			"flight, cost %d separate bolt write transactions (budget %d). Pending adds must be folded into "+
			"ONE db.Update per commit and each caller replied to individually; bbolt's Batch() cannot do it, "+
			"because it detaches its batch as soon as one starts, so every arrival during a commit gets a "+
			"transaction of its own and queues on the single write lock (prod: 763 goroutines parked in "+
			"DB.Batch, p50 12.8s to add ONE job).",
			len(jobs), reliable4ACAddInterval, txns, reliable4ACAddTxBudget)
	}

	if maxLat > reliable4ACMaxLatency {
		t.Errorf("add starvation: the slowest of %d adds took %s, over the %s ceiling (the %s client timeout "+
			"floor is what makes a client give up on an add altogether)",
			len(jobs), maxLat.Round(time.Millisecond), reliable4ACMaxLatency, ClientMinRequestTimeout)
	}

	reliable4ACAddAssertLive(t, database.bolt, jobs)
}

// TestReliable4AddErrorsStayPerAdd proves coalescing preserves per-add error
// semantics: one add that cannot be applied must not fail the adds folded into the
// same transaction. This mirrors bbolt.Batch's own behaviour (drop the offender,
// re-run the rest as a batch, and re-run the offender alone so its caller gets its
// own error).
//
// The bad add carries an encoded job with an empty key, which fails
// deterministically inside the transaction (bolt's Put rejects an empty key,
// whatever else the transaction holds), and is queued FIRST so
// that a naive "fold everything into one db.Update" would roll back - and so
// report the bad add's error to - every good add behind it.
func TestReliable4AddErrorsStayPerAdd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	jobs := reliable4ACAddJobs("perAdd", reliable4ACAddBatchAdds)
	bad := func() error {
		return database.storeNewJobData(ctx, sobsd{{[]byte{}, []byte("unstorable")}}, nil, nil, nil, nil)
	}

	errs, txns := reliable4ACAddPileUp(t, database, append([]func() error{bad},
		reliable4ACAddFuncs(ctx, database, jobs)...))

	t.Logf("ADD-PERADD: goodAdds=%d transactions=%d badErr=%v txBudget=%d",
		len(jobs), txns, errs[0], reliable4ACAddPerAddTxBudget)

	if errs[0] == nil {
		t.Error("the unstorable add's caller got no error, so a per-add failure is being hidden")
	}

	reliable4ACAddAssertNoErrors(t, errs[1:], "was failed by the unstorable add queued ahead of it")

	if txns > reliable4ACAddPerAddTxBudget {
		t.Errorf("the good adds were not re-run together after the bad one was dropped: %d adds cost %d "+
			"write transactions (budget %d). Recovering from a per-add error must not degrade the fold into "+
			"one transaction per add", len(jobs), txns, reliable4ACAddPerAddTxBudget)
	}

	reliable4ACAddAssertLive(t, database.bolt, jobs)
}

// TestReliable4AddPanicStaysPerAdd proves a malformed add that PANICS inside the
// write transaction fails only its own caller. bbolt.Batch used to give this for
// free (safelyCall turned a panic into that call's error); a plain db.Update does
// not, so without applyNewJobsOp's recover one bad add would take the whole
// manager down along with every add folded in beside it.
func TestReliable4AddPanicStaysPerAdd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	jobs := reliable4ACAddJobs("panic", reliable4ACAddBatchAdds)
	panicking := func() error {
		return reliable4ACAddStore(database, reliable4ACAddPanickingPut,
			sobsd{{[]byte("reliable4-add-panic"), []byte("v")}})
	}

	errs, txns := reliable4ACAddPileUp(t, database, append([]func() error{panicking},
		reliable4ACAddFuncs(ctx, database, jobs)...))

	t.Logf("ADD-PANIC: goodAdds=%d transactions=%d panicErr=%v", len(jobs), txns, errs[0])

	if !errors.Is(errs[0], errNewJobsPanic) {
		t.Errorf("a panicking add gave its caller %v, want an error wrapping errNewJobsPanic", errs[0])
	}

	reliable4ACAddAssertNoErrors(t, errs[1:], "was failed by a panicking transaction-mate")
	reliable4ACAddAssertLive(t, database.bolt, jobs)
}

// TestReliable4AddCloseDrains proves close() drains adds that are still in flight:
// every waiter is replied to (no hang, no lost reply), every add that reported
// success is durably in the live bucket of the file on disk, and close() itself
// returns. It then proves an add offered after that final drain is told the
// database is closed rather than left waiting for a reply that can never come.
func TestReliable4AddCloseDrains(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	// bolt clears its path on Close, so remember it while the database is open.
	path := database.bolt.Path()
	jobs := reliable4ACAddJobs("close", reliable4ACAddBatchAdds)

	release := reliable4ACAddHoldWriteLock(t, database)
	defer release()

	blocker := reliable4ACAddBlockWriter(t, database)
	wg, errs := reliable4ACAddLaunch(reliable4ACAddFuncs(ctx, database, jobs))

	reliable4ACAddAwaitPending(t, database, len(jobs))

	closed := make(chan error, 1)

	go func() { closed <- database.close(ctx) }()

	// let close() reach its drain, then let the held transaction finish.
	time.Sleep(reliable4ACAddSettle)
	release()
	reliable4ACAddWait(t, wg)

	if err := reliable4ACAddAwait(t, closed); err != nil {
		t.Errorf("close() with adds in flight failed: %v", err)
	}

	if err := reliable4ACAddOpErr(t, blocker); err != nil {
		t.Errorf("the blocking add failed: %v", err)
	}

	survived := make([]*Job, 0, len(jobs))

	for i, err := range errs {
		if err == nil {
			survived = append(survived, jobs[i])
		}
	}

	t.Logf("ADD-CLOSE-DRAIN: inFlight=%d addedBeforeClose=%d", len(jobs), len(survived))

	if len(survived) != len(jobs) {
		t.Errorf("%d/%d adds already queued when close() was called were dropped; close() must drain the "+
			"add queue (no lost add, no hung waiter)", len(jobs)-len(survived), len(jobs))
	}

	err := database.storeNewJobData(ctx, sobsd{{[]byte("reliable4-add-after-close"), []byte("v")}},
		nil, nil, nil, nil)
	if !errors.Is(err, errDBClosed) {
		t.Errorf("an add offered after the final drain got %v, want %v: it must be refused, not left "+
			"waiting for a reply that can never come", err, errDBClosed)
	}

	reliable4ACAddAssertReopenedLive(t, path, survived)
}

// reliable4ACAddJobs returns n distinct jobs for a single-job add each.
func reliable4ACAddJobs(prefix string, n int) []*Job {
	jobs := make([]*Job, n)
	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo add-coalesce %s %d", prefix, i), "reliable4add")
	}

	return jobs
}

// reliable4ACAddStore queues one add whose single live-bucket store uses the given
// putter, and returns that add's own outcome, blocking as storeNewJobData does.
func reliable4ACAddStore(database *db, put sobsdPutter, encodes sobsd) error {
	op := &newJobsOp{
		stores: []newJobStore{{bucketJobsLive, encodes, put}},
		result: make(chan error, 1),
	}

	if !database.enqueueNewJobs(op) {
		return errDBClosed
	}

	return <-op.result
}

// reliable4ACAddPileUp gets the writer into a transaction of its own that is
// blocked on bolt's held write lock, then launches adds[0] and waits for it to
// reach the pending queue before launching the rest, so that the offender is at
// the head of the fold and every add is in it. It releases the lock, waits for
// every add, and returns their errors in order plus how many write transactions
// committed (the blocking add's and the fold's).
func reliable4ACAddPileUp(t *testing.T, database *db, adds []func() error) ([]error, int) {
	t.Helper()

	release := reliable4ACAddHoldWriteLock(t, database)
	defer release()

	blocker := reliable4ACAddBlockWriter(t, database)
	before := reliable4ACAddWriteTxID(t, database)

	firstWG, firstErr := reliable4ACAddLaunch(adds[:1])

	reliable4ACAddAwaitPending(t, database, 1)

	restWG, restErrs := reliable4ACAddLaunch(adds[1:])

	reliable4ACAddAwaitPending(t, database, len(adds))

	release()
	reliable4ACAddWait(t, firstWG)
	reliable4ACAddWait(t, restWG)

	if err := reliable4ACAddOpErr(t, blocker); err != nil {
		t.Errorf("the blocking add failed: %v", err)
	}

	return append(firstErr, restErrs...), reliable4ACAddWriteTxID(t, database) - before
}

// TestReliable4AddFinalDrainLoops proves the shutdown drain persists EVERYTHING
// pending even when the fold bound cuts it, in the ONE call stopNewJobsWriter
// makes: a bounded swap that persisted only the first budget's worth would leave
// the rest of the callers blocked on their replies forever, so a bound must not
// turn shutdown into a lost add.
//
// The real bound needs ~100MB of bolt writes to engage (which is why the tests of
// the budget arithmetic itself are tagged reliability_repro, in
// reliable4_add_foldcap_test.go); this sets each queued add's measured cost
// directly so the same cut happens on three tiny adds, because what is being
// proved here is the drain LOOP, not the arithmetic.
func TestReliable4AddFinalDrainLoops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	database := reliable4ACAddOpenBareDB(t)
	ops := make([]*newJobsOp, reliable4ACAddDrainOps)

	for i := range ops {
		ops[i] = reliable4ACAddCostedOp(database, fmt.Sprintf("reliable4-drain-%d", i))

		if !database.enqueueNewJobs(ops[i]) {
			t.Fatalf("add %d was refused by the add queue", i)
		}
	}

	before := reliable4ACAddWriteTxID(t, database)

	database.drainNewJobs(true)

	txns := reliable4ACAddWriteTxID(t, database) - before

	t.Logf("ADD-FINAL-DRAIN: adds=%d transactions=%d pendingAfter=%d",
		len(ops), txns, len(database.njPending))

	if txns != reliable4ACAddDrainOps {
		t.Errorf("the final drain of %d adds, each costing a whole fold budget, committed %d write "+
			"transactions, want %d: it must keep swapping until the queue is empty",
			len(ops), txns, reliable4ACAddDrainOps)
	}

	if len(database.njPending) != 0 {
		t.Errorf("the final drain returned with %d adds still pending, whose callers wait forever",
			len(database.njPending))
	}

	if !database.njStopped {
		t.Error("the final drain did not latch the add queue shut")
	}

	reliable4ACAddAssertReplied(t, ops)
	reliable4ACAddAssertStored(t, database.bolt, ops)
}

// reliable4ACAddOpenBareDB returns a db with a real bolt database and the buckets
// the add path puts into, but NO writer goroutine, so a test can drive
// drainNewJobs itself and what each transaction carried is deterministic rather
// than a race against the writer.
func reliable4ACAddOpenBareDB(t *testing.T) *db {
	t.Helper()

	boltdb, err := bolt.Open(filepath.Join(t.TempDir(), "bare.db"), dbFilePermission, nil)
	if err != nil {
		t.Fatalf("bolt.Open failed: %v", err)
	}

	t.Cleanup(func() { _ = boltdb.Close() })

	err = boltdb.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketJobsLive, bucketRTK, bucketJobLookupEntries} {
			if _, errB := tx.CreateBucketIfNotExists(bucket); errB != nil {
				return errB
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("creating buckets failed: %v", err)
	}

	return &db{bolt: boltdb, njSignal: make(chan struct{}, 1)}
}

// reliable4ACAddHoldWriteLock opens a write transaction and holds bolt's single
// writer lock until the returned function is called, so adds arriving meanwhile
// queue up instead of each committing immediately. The transaction is rolled
// back, not committed, so it does not itself count as a write transaction.
//
// The release is idempotent, so a caller defers it as well as calling it where
// the test wants the lock dropped: a t.Fatalf between the two would otherwise
// leave the lock held, and the deferred database.close() would then block in
// bolt.Close() and hang the suite to the go test timeout instead of failing it.
// It has to be a defer rather than a t.Cleanup, which would run after that
// close.
func reliable4ACAddHoldWriteLock(t *testing.T, database *db) func() {
	t.Helper()

	tx, err := database.bolt.Begin(true)
	if err != nil {
		t.Fatalf("failed to open the blocking write transaction: %v", err)
	}

	var once sync.Once

	return func() {
		once.Do(func() {
			if errR := tx.Rollback(); errR != nil {
				t.Errorf("failed to roll back the blocking write transaction: %v", errR)
			}
		})
	}
}

// reliable4ACAddBlockWriter queues one tiny add SYNCHRONOUSLY and waits for the
// writer to take it off the pending queue, so the writer is inside a transaction
// of its own (blocked on the write lock the caller is holding) and everything
// queued next piles up into the following fold. Queueing it directly, rather than
// from a goroutine, is what makes the wait for the empty queue meaningful: the op
// is definitely pending before the first poll. It returns the op, whose reply the
// caller should check once the lock is released.
func reliable4ACAddBlockWriter(t *testing.T, database *db) *newJobsOp {
	t.Helper()

	op := reliable4ACAddCostedOp(database, "reliable4-add-blocker")

	if !database.enqueueNewJobs(op) {
		t.Fatal("the blocking add was refused by the add queue")
	}

	reliable4ACAddAwaitPending(t, database, 0)

	return op
}

// reliable4ACAddCostedOp returns an op storing one small value in the live bucket,
// declaring a whole fold budget's worth of puts so that no two of them can share a
// write transaction. Its cost is set rather than measured because engaging the real
// budget with real data costs ~100MB of writes (see reliable4_add_foldcap_test.go).
func reliable4ACAddCostedOp(database *db, key string) *newJobsOp {
	stores := []newJobStore{{
		bucketJobsLive,
		sobsd{{[]byte(key), []byte("reliable4 add coalesce")}},
		database.putEncodedJobs,
	}}

	return &newJobsOp{
		stores:    stores,
		result:    make(chan error, 1),
		foldPuts:  newJobsFoldMaxPuts/2 + 1,
		foldBytes: 1,
	}
}

// reliable4ACAddWriteTxID returns the database's current meta transaction id: a
// read transaction reports the id of the last committed write transaction, so the
// difference between two of these is how many write transactions committed in
// between.
func reliable4ACAddWriteTxID(t *testing.T, database *db) int {
	t.Helper()

	var id int

	if err := database.bolt.View(func(tx *bolt.Tx) error {
		id = tx.ID()

		return nil
	}); err != nil {
		t.Fatalf("reading the transaction id failed: %v", err)
	}

	return id
}

// reliable4ACAddAssertReplied checks every op has ALREADY been replied to, with no
// error. The check is non-blocking because the drain it follows returned, so an op
// still without a reply is one whose caller would have waited forever - which is
// exactly what a final drain that stopped after one bounded swap would do.
func reliable4ACAddAssertReplied(t *testing.T, ops []*newJobsOp) {
	t.Helper()

	for i, op := range ops {
		select {
		case err := <-op.result:
			if err != nil {
				t.Errorf("add %d failed: %v", i, err)
			}
		default:
			t.Errorf("add %d was never replied to, so its caller waits forever: a bounded final drain must "+
				"keep swapping until the queue is empty", i)
		}
	}
}

// reliable4ACAddAssertStored checks every key every op carried is in the database,
// so a bounded fold is shown to have lost nothing.
func reliable4ACAddAssertStored(t *testing.T, bdb *bolt.DB, ops []*newJobsOp) {
	t.Helper()

	var missing int

	err := bdb.View(func(tx *bolt.Tx) error {
		for _, op := range ops {
			for _, s := range op.stores {
				for _, doublet := range s.encodes {
					if tx.Bucket(s.bucket).Get(doublet[0]) == nil {
						missing++
					}
				}
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("failed to read back the stored data: %v", err)
	}

	if missing > 0 {
		t.Errorf("%d stored values are missing from the database", missing)
	}
}

// reliable4ACAddLaunch runs every add in its own goroutine, returning the
// WaitGroup to wait on and the slice its goroutines fill with each add's error.
func reliable4ACAddLaunch(adds []func() error) (*sync.WaitGroup, []error) {
	wg := &sync.WaitGroup{}
	errs := make([]error, len(adds))

	for i, add := range adds {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = add()
		}()
	}

	return wg, errs
}

// reliable4ACAddFuncs returns one single-job add call per job.
func reliable4ACAddFuncs(ctx context.Context, database *db, jobs []*Job) []func() error {
	adds := make([]func() error, len(jobs))

	for i, job := range jobs {
		adds[i] = func() error {
			_, _, _, err := database.storeNewJobs(ctx, []*Job{job}, true)

			return err
		}
	}

	return adds
}

// reliable4ACAddAwaitPending waits for the writer's pending queue to hold exactly
// want adds, so a test's setup is deterministic instead of a sleep. It fails
// loudly rather than hanging.
func reliable4ACAddAwaitPending(t *testing.T, database *db, want int) {
	t.Helper()

	deadline := time.Now().Add(reliable4ACAddWaitTimeout)

	for time.Now().Before(deadline) {
		database.njMu.Lock()
		depth := len(database.njPending)
		database.njMu.Unlock()

		if depth == want {
			return
		}

		time.Sleep(reliable4ACAddPoll)
	}

	t.Fatalf("the add writer's pending queue never held %d adds within %s",
		want, reliable4ACAddWaitTimeout)
}

// reliable4ACAddPaced adds each job in its own goroutine, launching them interval
// apart, and returns the WaitGroup to wait on plus the slices its goroutines fill
// with each add's latency and error, in job order.
func reliable4ACAddPaced(ctx context.Context, database *db, jobs []*Job,
	interval time.Duration,
) (*sync.WaitGroup, []time.Duration, []error) {
	wg := &sync.WaitGroup{}
	latencies := make([]time.Duration, len(jobs))
	errs := make([]error, len(jobs))

	for i, job := range jobs {
		time.Sleep(interval)

		wg.Add(1)

		go func() {
			defer wg.Done()

			t0 := time.Now()
			_, _, _, errs[i] = database.storeNewJobs(ctx, []*Job{job}, true)
			latencies[i] = time.Since(t0)
		}()
	}

	return wg, latencies, errs
}

// reliable4ACAddWait waits for wg with a timeout, so a lost reply fails loudly
// instead of hanging.
func reliable4ACAddWait(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(reliable4ACAddWaitTimeout):
		t.Fatalf("add callers did not all get a reply within %s: a coalescing writer must reply to EVERY "+
			"waiter individually", reliable4ACAddWaitTimeout)
	}
}

// reliable4ACAddAssertNoErrors fails the test if any add errored, quoting what it
// means for a folded add to be failed by one of its transaction-mates.
func reliable4ACAddAssertNoErrors(t *testing.T, errs []error, what string) {
	t.Helper()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("good add %d %s (%v): one bad add must not fail the whole fold - mirror bbolt.Batch, "+
				"drop the offender, re-run the rest, and re-run the offender alone", i, what, err)
		}
	}
}

// reliable4ACAddOpErr reads one add op's reply, failing loudly rather than hanging
// if the writer never replied to it.
func reliable4ACAddOpErr(t *testing.T, op *newJobsOp) error {
	t.Helper()

	return reliable4ACAddAwait(t, op.result)
}

// reliable4ACAddAwait reads one error from ch with a timeout.
func reliable4ACAddAwait(t *testing.T, ch chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(reliable4ACAddWaitTimeout):
		t.Fatalf("timed out after %s waiting for an add/close reply", reliable4ACAddWaitTimeout)

		return nil
	}
}

// reliable4ACAddAssertReopenedLive reopens the underlying bolt file, so durability
// is checked against what actually reached disk rather than against a still-open
// handle.
func reliable4ACAddAssertReopenedLive(t *testing.T, path string, jobs []*Job) {
	t.Helper()

	reopened, err := bolt.Open(path, dbFilePermission, nil)
	if err != nil {
		t.Fatalf("failed to reopen the database file: %v", err)
	}

	defer func() { _ = reopened.Close() }()

	reliable4ACAddAssertLive(t, reopened, jobs)
}

// reliable4ACAddAssertLive checks every job is in bdb's live bucket, reporting a
// count rather than asserting per job.
func reliable4ACAddAssertLive(t *testing.T, bdb *bolt.DB, jobs []*Job) {
	t.Helper()

	var missing int

	err := bdb.View(func(tx *bolt.Tx) error {
		for _, job := range jobs {
			if !checkIfLiveTx(tx, job.Key()) {
				missing++
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("failed to read back the added jobs: %v", err)
	}

	if missing > 0 {
		t.Errorf("%d/%d added jobs are not in the live bucket: coalescing must not lose an add whose caller "+
			"was told it succeeded", missing, len(jobs))
	}
}
