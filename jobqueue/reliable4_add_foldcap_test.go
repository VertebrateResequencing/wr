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

package jobqueue

// Tests for the BOUND on the add path's group commit (newJobsFoldMaxBytes,
// newJobsFoldMaxPuts). The coalescing newJobsWriter folds concurrent adds into one
// write transaction; bbolt holds every page a transaction dirties in memory until
// it commits, so without a bound the writer's peak memory is a function of how
// deep the add queue got, and production's ~25KB commands make that a real
// gigabyte-scale risk (see the newJobsFoldMaxBytes doc comment).
//
// What has to be true of the bound, and which test covers it:
//
//   - it CUTS a fold that exceeds the budget, and never in the middle of an add
//     (TestReliable4AddFoldSplit);
//   - it always takes at least one add, so an add bigger than the whole budget
//     still makes progress (TestReliable4AddFoldSplit);
//   - a bounded swap re-arms njSignal, so the writer starts the next transaction
//     without waiting for another arrival (TestReliable4AddFoldNonFinalDrain);
//   - the FINAL drain still persists everything, in as many transactions as the
//     budget needs (TestReliable4AddFoldFinalDrain) - a bound must not turn
//     shutdown into a lost add;
//   - and none of it loses an add or fails a caller under real concurrent load
//     through the real storeNewJobs path (TestReliable4AddFoldConcurrentAdds).
//
// The drain tests drive drainNewJobs directly on a db with NO writer goroutine, so
// what each transaction carried is deterministic rather than a race against a
// writer; the concurrency test uses the real initDB db, with its real writer, and
// holds bolt's write lock while the adds arrive so that they genuinely pile up
// into one queue for the bound to cut.
//
// These are tagged reliability_repro (not part of `make test`) because proving a
// 32MB budget engages means pushing a few hundred MB through bolt. The BEHAVIOUR
// the bound must not break - adds coalesce, one bad add stays its own error, a
// panic stays its own error, close() drains everything, and the final drain loops
// until the queue is empty - is covered in the main suite, in
// reliable4_add_coalesce_test.go, whose helpers these reuse.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// afcValueBytes is the size of each encoded job value the fold tests write.
	// Production's real jobs carry ~25KB commands, which is what makes an unbounded
	// fold dangerous, so the tests use the same magnitude.
	afcValueBytes = 25 * 1024

	// afcItemsPerOp is how many items each test add carries. It stays under
	// storeBatchGranularity so that a real add of this size would not be chunked
	// away from the folded path, and makes each op ~5MB, so several fit in one
	// budget and the cut lands between ops rather than at the first one.
	afcItemsPerOp = 200

	// afcOps is how many adds the tests pile up: ~100MB, over three times
	// newJobsFoldMaxBytes, so the budget has to cut the fold at least three times.
	afcOps = 20

	// afcPileUpTimeout bounds the wait for the concurrent adds to reach the pending
	// queue while bolt's write lock is held. It is a hang detector, not a latency
	// budget: the test proceeds either way, it just reports a smaller pile-up.
	afcPileUpTimeout = 60 * time.Second

	// afcPileUpPoll is how often that wait checks the pending queue's depth.
	afcPileUpPoll = 5 * time.Millisecond
)

// TestReliable4AddFoldSplit checks the budget arithmetic itself: where it cuts,
// that it cuts between adds and not inside one, and that it always takes at least
// one add however big that add is.
func TestReliable4AddFoldSplit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	t.Run("a fold within the budget is taken whole", func(t *testing.T) {
		fold, remainder := splitNewJobsFold(afcCostedOps(newJobsFoldMaxBytes/4, 1, 4))
		if len(fold) != 4 || remainder != nil {
			t.Errorf("took %d ops and left %d, want all 4 and none", len(fold), len(remainder))
		}
	})

	t.Run("the byte budget cuts the fold between adds", func(t *testing.T) {
		fold, remainder := splitNewJobsFold(afcCostedOps(newJobsFoldMaxBytes/4, 1, 10))
		if len(fold) != 4 || len(remainder) != 6 {
			t.Errorf("took %d ops and left %d, want 4 and 6", len(fold), len(remainder))
		}
	})

	t.Run("the put budget cuts the fold between adds", func(t *testing.T) {
		fold, remainder := splitNewJobsFold(afcCostedOps(1, newJobsFoldMaxPuts/3, 10))
		if len(fold) != 3 || len(remainder) != 7 {
			t.Errorf("took %d ops and left %d, want 3 and 7", len(fold), len(remainder))
		}
	})

	t.Run("an add bigger than the whole budget is still taken, alone", func(t *testing.T) {
		ops := append(afcCostedOps(newJobsFoldMaxBytes*3, 1, 1),
			afcCostedOps(newJobsFoldMaxBytes/4, 1, 2)...)

		fold, remainder := splitNewJobsFold(ops)
		if len(fold) != 1 || len(remainder) != 2 {
			t.Fatalf("took %d ops and left %d, want 1 and 2", len(fold), len(remainder))
		}

		if fold[0] != ops[0] {
			t.Error("the op taken was not the oversized one at the head of the queue")
		}
	})

	t.Run("the remainder does not alias the pending queue", func(t *testing.T) {
		ops := afcCostedOps(newJobsFoldMaxBytes/2, 1, 4)

		fold, remainder := splitNewJobsFold(ops)
		if len(fold) != 2 || len(remainder) != 2 {
			t.Fatalf("took %d ops and left %d, want 2 and 2", len(fold), len(remainder))
		}

		// a resliced remainder (pending[2:]) shares the pending queue's backing
		// array, whose earlier slots hold the ops the transaction is about to
		// persist: those ops, and the encoded bytes they carry, then stay
		// reachable through the queue for as long as any add is pending, which
		// hands back the memory the fold budget exists to bound. Only a fresh
		// slice drops them, and its first element is at a different address from
		// the pending queue's corresponding one.
		if &remainder[0] == &ops[len(fold)] {
			t.Error("the remainder is a reslice of the pending queue, so the ops it just handed over " +
				"to be persisted stay reachable through the queue")
		}
	})
}

// afcCostedOps returns n ops with the given fold cost and no stores, for testing
// the budget arithmetic without writing anything.
func afcCostedOps(foldBytes, foldPuts, n int) []*newJobsOp {
	ops := make([]*newJobsOp, n)
	for i := range ops {
		ops[i] = &newJobsOp{
			result:    make(chan error, 1),
			foldBytes: foldBytes,
			foldPuts:  foldPuts,
		}
	}

	return ops
}

// TestReliable4AddFoldNonFinalDrain proves that a non-final drain commits ONE
// budget-bounded transaction, leaves the rest pending with njSignal re-armed (so
// the writer's select loop comes straight back for them rather than waiting for
// another add), and that repeating it loses nothing.
func TestReliable4AddFoldNonFinalDrain(t *testing.T) {
	if runnermode || servermode {
		return
	}

	database := reliable4ACAddOpenBareDB(t)
	ops := afcEnqueueOps(t, database, "nonfinal", afcOps)

	if len(database.njSignal) != 1 {
		t.Error("enqueuing adds did not arm the writer's signal")
	}

	txs, drains, biggest := 0, 0, 0

	for len(database.njPending) > 0 {
		pendingBefore := len(database.njPending)
		before := reliable4ACAddWriteTxID(t, database)

		// take the kick, as the writer's select does, so that what the drain leaves
		// behind is what the writer would come back for
		<-database.njSignal

		database.drainNewJobs(false)

		txs += reliable4ACAddWriteTxID(t, database) - before
		drains++

		took := pendingBefore - len(database.njPending)
		if took > biggest {
			biggest = took
		}

		if took == 0 {
			t.Fatal("a drain took no ops at all, so the queue would never empty")
		}

		if len(database.njPending) > 0 && len(database.njSignal) != 1 {
			t.Fatalf("drain %d left %d adds pending without re-arming the signal",
				drains, len(database.njPending))
		}
	}

	if len(database.njSignal) != 0 {
		t.Error("the last drain emptied the queue but left the signal armed, so the writer " +
			"would wake for nothing")
	}

	if drains < 2 {
		t.Errorf("%d adds of ~%dMB drained in %d transactions: the budget never cut the fold",
			afcOps, afcOpBytes()/(1024*1024), drains)
	}

	if biggest*afcOpBytes() > newJobsFoldMaxBytes {
		t.Errorf("a single transaction carried %d adds, %d bytes, over the %d byte budget",
			biggest, biggest*afcOpBytes(), newJobsFoldMaxBytes)
	}

	t.Logf("%d adds (%d bytes total, %d per add) drained in %d transactions "+
		"(%d drain calls, biggest fold %d adds = %d bytes, budget %d)",
		afcOps, afcOps*afcOpBytes(), afcOpBytes(), txs, drains, biggest,
		biggest*afcOpBytes(), newJobsFoldMaxBytes)

	reliable4ACAddAssertReplied(t, ops)
	reliable4ACAddAssertStored(t, database.bolt, ops)
}

// TestReliable4AddFoldFinalDrain proves that the shutdown drain still persists
// EVERYTHING pending, in however many transactions the budget needs, in the ONE
// call stopNewJobsWriter makes: a bounded swap must not turn shutdown into a lost
// add. This is the same property TestReliable4AddFinalDrainLoops guards cheaply in
// the main suite, here against the REAL byte budget and real 25KB values.
func TestReliable4AddFoldFinalDrain(t *testing.T) {
	if runnermode || servermode {
		return
	}

	database := reliable4ACAddOpenBareDB(t)
	ops := afcEnqueueOps(t, database, "final", afcOps)

	before := reliable4ACAddWriteTxID(t, database)

	database.drainNewJobs(true)

	txs := reliable4ACAddWriteTxID(t, database) - before

	if len(database.njPending) != 0 {
		t.Errorf("the final drain returned with %d adds still pending", len(database.njPending))
	}

	if !database.njStopped {
		t.Error("the final drain did not latch the add queue shut")
	}

	if txs < 2 {
		t.Errorf("the final drain of %d adds (%d bytes, budget %d) committed %d transactions: "+
			"it cannot have been bounded", afcOps, afcOps*afcOpBytes(), newJobsFoldMaxBytes, txs)
	}

	t.Logf("one final drain of %d adds (%d bytes) committed %d write transactions",
		afcOps, afcOps*afcOpBytes(), txs)

	reliable4ACAddAssertReplied(t, ops)
	reliable4ACAddAssertStored(t, database.bolt, ops)

	// an add offered after the final drain must be refused, not left waiting
	if database.enqueueNewJobs(&newJobsOp{result: make(chan error, 1)}) {
		t.Error("an add was accepted after the final drain")
	}
}

// afcEnqueueOps queues n adds, each afcItemsPerOp items of afcValueBytes, through
// the real enqueueNewJobs, and returns them. Their stores mirror a real add's: a
// bucketRTK lookup per item (an INDEXED lookup bucket, so each of those is two
// puts) and the encoded job itself in bucketJobsLive.
func afcEnqueueOps(t *testing.T, database *db, label string, n int) []*newJobsOp {
	t.Helper()

	ops := make([]*newJobsOp, n)

	for i := range ops {
		lookups := make(sobsd, afcItemsPerOp)
		encodes := make(sobsd, afcItemsPerOp)
		value := []byte(strings.Repeat("v", afcValueBytes))

		for j := range afcItemsPerOp {
			key := []byte(fmt.Sprintf("%s-%03d-%06d", label, i, j))
			lookups[j] = [2][]byte{[]byte(label + dbDelimiter + string(key)), nil}
			encodes[j] = [2][]byte{key, value}
		}

		sort.Sort(lookups)
		sort.Sort(encodes)

		stores := []newJobStore{
			{bucketRTK, lookups, database.putLookups},
			{bucketJobsLive, encodes, database.putEncodedJobs},
		}

		foldBytes, foldPuts := newJobsFoldCost(stores)

		ops[i] = &newJobsOp{
			stores: stores, result: make(chan error, 1),
			foldBytes: foldBytes, foldPuts: foldPuts,
		}

		if !database.enqueueNewJobs(ops[i]) {
			t.Fatalf("op %d was refused by the add queue", i)
		}
	}

	return ops
}

// afcOpBytes is what afcEnqueueOps' ops cost the byte budget, so a test can say
// how many of them a budget's worth is. It is dominated by the encoded values;
// the keys, and the indexed lookup bucket's doubling of them, are noise beside a
// 25KB value.
func afcOpBytes() int {
	return afcItemsPerOp * afcValueBytes
}

// TestReliable4AddFoldConcurrentAdds pushes several budgets' worth of add data
// through the REAL storeNewJobs path, from many goroutines at once, against the
// real writer - and holds bolt's write lock while they arrive so they genuinely
// pile up into one queue for the budget to cut. Every add must succeed and every
// job must be in the database afterwards.
func TestReliable4AddFoldConcurrentAdds(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)

	defer func() { _ = database.close(ctx) }()

	jobs := afcBigJobs(t, afcOps, afcItemsPerOp)
	before := reliable4ACAddWriteTxID(t, database)

	release := reliable4ACAddHoldWriteLock(t, database)

	defer release()

	errs := make([]error, afcOps)

	var wg sync.WaitGroup

	for i := range afcOps {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			_, _, _, err := database.storeNewJobs(ctx, jobs[i], true)
			errs[i] = err
		}(i)
	}

	piled := afcAwaitPileUp(database, afcOps)

	release()
	wg.Wait()

	txs := reliable4ACAddWriteTxID(t, database) - before

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent add %d failed: %v", i, err)
		}
	}

	missing := 0

	err := database.bolt.View(func(tx *bolt.Tx) error {
		for _, batch := range jobs {
			for _, job := range batch {
				if !checkIfLiveTx(tx, job.Key()) {
					missing++
				}
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("reading the jobs back failed: %v", err)
	}

	if missing > 0 {
		t.Errorf("%d of %d added jobs are not in the database", missing, afcOps*afcItemsPerOp)
	}

	t.Logf("%d concurrent adds of %d jobs each (~%d bytes of commands), %d of them piled up "+
		"behind the held write lock, committed %d write transactions; budget %d bytes / %d puts",
		afcOps, afcItemsPerOp, afcOps*afcItemsPerOp*afcValueBytes, piled, txs,
		newJobsFoldMaxBytes, newJobsFoldMaxPuts)
}

// afcBigJobs returns batches of jobs with production-sized commands, distinct
// across every batch, and few enough per batch that storesNeedChunking does not
// take them off the folded path.
func afcBigJobs(t *testing.T, batches, perBatch int) [][]*Job {
	t.Helper()

	padding := strings.Repeat("x", afcValueBytes)
	jobs := make([][]*Job, batches)

	for i := range jobs {
		jobs[i] = make([]*Job, perBatch)

		for j := range jobs[i] {
			jobs[i][j] = testDBJob(fmt.Sprintf("echo foldcap %d %d %s", i, j, padding),
				"reliable4foldcap")
		}
	}

	return jobs
}

// afcAwaitPileUp waits for the concurrent adds to reach the pending queue, and
// returns the deepest queue it saw. The writer takes one budget-bounded swap
// before it blocks on the held write lock, so the queue settles below want; the
// wait ends when it stops growing, or at afcPileUpTimeout.
func afcAwaitPileUp(database *db, want int) int {
	deepest, unchanged, deadline := 0, 0, time.Now().Add(afcPileUpTimeout)

	for time.Now().Before(deadline) {
		database.njMu.Lock()
		depth := len(database.njPending)
		database.njMu.Unlock()

		if depth > deepest {
			deepest, unchanged = depth, 0
		} else {
			unchanged++
		}

		if deepest >= want || (deepest > 0 && unchanged > 20) {
			break
		}

		time.Sleep(afcPileUpPoll)
	}

	return deepest
}
