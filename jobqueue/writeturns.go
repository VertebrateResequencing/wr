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

import (
	"slices"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// writerKind names a kind of write transaction that takes write turns: one of
// the db's coalescing writer goroutines, or a chunk of an add too big for one
// transaction.
type writerKind int

const (
	writerNewJobs writerKind = iota
	writerArchive
	writerBestEffort
	// writerChunkedAdd is an add too big for one transaction: each of its
	// chunks takes a turn of its own (storeNewJobDataChunked).
	writerChunkedAdd
	// writerLargeNewJobs is the new jobs writer when its transaction holds an
	// add too large to go ahead of the other writers (newJobsWriterKind).
	// writeTurns treats it exactly as writerChunkedAdd (no preference, ordered
	// by when it asked, never kept); they are separate kinds only so that each
	// call site names the transaction it runs, since nothing logs by kind.
	writerLargeNewJobs
	// writerKinds is the number of writer kinds.
	writerKinds
)

// coalescing reports whether k is one of the coalescing writers, the archive
// and best-effort writers, which batch the requests queued for them.
func (k writerKind) coalescing() bool {
	return k == writerArchive || k == writerBestEffort
}

// statsWriter is the writer the "db writer" line reports k's transactions
// under (see store_stats.go).
func (k writerKind) statsWriter() dbWriter {
	switch k {
	case writerArchive:
		return dbWriterArchive
	case writerBestEffort:
		return dbWriterBestEffort
	case writerChunkedAdd:
		return dbWriterAddChunk
	default:
		return dbWriterNewJobs
	}
}

// updateInTurn runs fn in a bolt write transaction once it is kind's turn to
// write, and ends the turn even if fn panics. The writer's oldest request is
// taken to be one made now. items is how many of the writer's items the
// transaction holds, for the "db writer" line.
func (db *db) updateInTurn(kind writerKind, items int, fn func(*bolt.Tx) error) error {
	return db.updateInTurnSince(kind, time.Time{}, items, fn)
}

// updateInTurnSince is updateInTurn for a writer whose oldest request was
// queued at oldest (the zero time means now).
func (db *db) updateInTurnSince(kind writerKind, oldest time.Time, items int, fn func(*bolt.Tx) error) error {
	asked := time.Now()

	var begun time.Time

	db.turns.takeSince(kind, oldest)
	defer db.turns.give()

	err := db.bolt.Update(func(tx *bolt.Tx) error {
		begun = time.Now()

		return fn(tx)
	})

	db.writerStats.observe(kind.statsWriter(), items, asked, begun)

	return err
}

// writeTurn is a writer waiting for its turn, woken by closing ready.
type writeTurn struct {
	kind  writerKind
	ready chan struct{}
	// oldest is the time this writer waits with (see the writeTurns doc).
	oldest time.Time
	// addTurnsAtArrival is writeTurns.addTurns when this writer began waiting.
	addTurnsAtArrival uint64
}

// writeTurns orders the write transactions of the new jobs, archive and
// best-effort writers, and of adds too big for one transaction, which otherwise
// queue on bolt's single write lock in arrival order. On a slow filesystem the
// archive writer, which re-queues the moment its transaction ends, fills that
// lock, and an add, whose client waits for it, ends up behind every transaction
// already queued (item 6.3d of .docs/nohold/phase6.md).
//
// The writers are small adds (writerNewJobs: adds small enough to be cheap to
// let through; see newJobsWriterKind), the coalescing writers (the archive and
// best-effort writers, which batch the requests queued for them), and chunks
// and large adds (each chunk of an add too big for one transaction, and the new
// jobs writer with an add too large to go ahead of the others). Each waiting
// writer carries a time. A coalescing writer carries when the oldest request in
// the queue it took was queued, that is the first request queued since it took
// the queue before that (see queued and swapped). Every attempt of a failing
// archive batch carries the batch's. A chunk or large add carries the moment
// it asked. A writer that asks while no turn is in progress takes one at once,
// one that asks for a turn kept for it takes that, and any other waits. When a
// turn ends (give and nextTurn):
//
//   - With a small add waiting, no turn is kept. If a small add has been given
//     a turn since any waiting writer other than a small add began waiting (an
//     add has gone ahead of that writer), the writer nextOther chooses from
//     those writers goes; otherwise the small add that asked first goes.
//   - With no small add waiting, the writer nextOther chooses from all those
//     waiting goes, if any, unless the writer whose turn is ending keeps the
//     turn. It keeps it when it has requests queued since it took its queue (so
//     only a coalescing writer can), nextOther chose no coalescing writer, and
//     either nobody waits or the oldest of those queued requests is older than
//     the time of the chunk or large add nextOther chose.
//   - Of the writers it may choose from, nextOther chooses the coalescing
//     writer that asked first, unless the chunk or large add with the oldest
//     time (of those with the same time, the one that asked first) has one
//     older than every coalescing writer's; then it chooses that chunk or large
//     add.
//
// A kept turn goes to its writer alone, when it next asks; until then no writer
// goes.
//
// Without the first rule an add that arrived during each transaction went
// ahead again every time (item 6.4b of .docs/nohold/phase6.md). The rule is
// judged by when each writer asked, not by its time: judged by time, the
// archive and best-effort writers, which under a steady stream nearly always
// carry a request older than the last add's turn, held every add back until
// each had gone, and adds lost the speed-up this ordering exists for (item
// 6.4d).
//
// The coalescing writers go among themselves by asking, but for a kept turn,
// because, ordered by time, the archive writer, whose batch's first request
// under a steady stream is nearly always the older, went straight after each
// add and kept its turn over a waiting best-effort writer, so the next add
// waited out a whole archive commit rather than a short best-effort one:
// single-job adds were 21% slower in BenchmarkAddDuringWriterStreams (items
// 6.4e and 6.4g). Between those two a request's age gains little, since each
// asks again soon after its turn ends.
//
// Against chunks and large adds the coalescing writers are judged by time
// rather than by asking because the archive and best-effort writers take their
// queues before they ask: requests that arrive while one of them waits behind a
// long transaction (a big add's chunk, megabytes on a production add) ride in
// its next transaction, which asks only after this one commits. Ordered by
// asking, a large add queued in that moment went ahead of them, and they waited
// behind two long transactions (items 6.4c and 6.4d). That is also why a large
// add gets no preference: its transaction can take seconds on a slow
// filesystem, and going ahead it often landed just after a chunk the other
// writers had already waited through.
//
// The first five of these bounds follow from the rules and the definitions
// above them, and the last from applyFolded and drainArchives (item 6.4g of
// .docs/nohold/phase6.md lists, for each, the tests that pin it or why none
// does):
//
//   - Once a small add waits, no turn is kept, and each turn after the one in
//     flight or kept as it asks goes to it, to a small add that asked before
//     it, or to a writer an add has gone ahead of. No add has gone ahead of a
//     writer that asks again after its turn, so while the small add waits it
//     gets no second turn before another small add has had one. So a small add
//     that asks while no other small add waits goes after the transaction in
//     flight (or, if it asks while a turn is kept, that writer's next
//     transaction) and at most one transaction of each writer then waiting that
//     an add has already gone ahead of, however many others wait.
//   - Small adds go among themselves in the order they asked, as do chunks and
//     large adds (whose time is when they asked) and the two coalescing
//     writers, except that a coalescing writer that keeps its turn goes again
//     while the other waits.
//   - While a coalescing writer waits, each writer that goes ahead of it asked
//     before it did, is a small add, or is the other coalescing writer keeping
//     its turn. With no small add waiting, a chunk or large add goes ahead of
//     it only with a time older than its own, and the other coalescing writer
//     keeps its turn only when a chunk or large add with a time older than the
//     waiting writer's would go next and the oldest of the requests queued for
//     the keeper is older still. With a small add waiting, a writer an add has
//     gone ahead of goes before one it has not, whatever their times.
//   - When a turn ends with a chunk or large add waiting and no small add, a
//     coalescing writer gets the turn only if a waiting coalescing writer
//     carries a time no younger than the chunk's or large add's, or by keeping
//     the turn when the oldest of the requests queued for it is older than that
//     time.
//   - The requests a coalescing writer has taken count in the order through the
//     time it carries while it waits; of those queued for it since it took its
//     queue, only the oldest counts, and only in its keep decision as its turn
//     ends. So a writer waiting or asking between the end of its turn and its
//     next ask can go ahead of those, whatever its time.
//   - A failing archive batch is not one wait. applyFolded retries the rest of
//     a batch after each archive that fails in it, then re-runs each failed one
//     alone, so a batch in which k archives fail takes at most 2k+1
//     transactions (2k when every archive in it fails, 1 when its first
//     transaction fails as a whole). Each attempt carries the batch's time but
//     asks afresh, only after the one before it has ended. The bounds above
//     hold for each attempt's own wait, not for the batch: between attempts any
//     waiting writer can go, among them small adds, the best-effort writer, and
//     chunks and large adds whatever their times.
//
// With more than one goroutine taking turns as the new jobs writer, several
// small adds can wait at once, and big and large adds may take turns from
// several goroutines at once.
//
// A big add's chunks take turns too. Each is a full transaction, and one that
// queued on bolt's lock instead would get every other transaction while one
// chunk follows another, since only the turn holder competes with it there: a
// reservation's write then waited behind several chunks per turn of its own,
// past ReserveWriteWait (item 6.4b).
//
// A turn kept for a writer is given to nobody else until that writer takes it.
// That is safe because a writer with requests queued always asks for its next
// turn, and on its way there it takes only locks whose holders never wait for a
// write turn: its own queue's mutex (db.arMu or db.beMu), the mutex inside
// db.wg (waitgroup's own, not db.wgMutex; the best-effort writer's Done calls),
// and each archived job's lock (noteArchivesWritten). The archive fold stats it
// records on the way (observeTx, observeWaits) are atomics and take no lock.
// The durable best-effort waits (updateJobAfterChangeDurable,
// updateJobRunStateDurable, updateJobRunStateDurableWithin and
// updateJobAfterExitDurable) state the same rule for their callers. A writer
// that has made its final drain has no requests queued (queued is not called
// after it), so no turn is kept for it.
//
// Only transactions run through updateInTurn or updateInTurnSince take a turn.
// Any other bolt write transaction queues on bolt's lock alongside whichever
// writer holds one. The zero value is ready to use.
type writeTurns struct {
	mu      sync.Mutex
	busy    bool
	waiting []writeTurn
	// holder is the kind of the writer whose turn is in progress, and kept is
	// true when that turn has ended and is kept for holder's next transaction.
	holder writerKind
	kept   bool
	// queuedSince is, for the archive and best-effort writers, when the first
	// request queued since the writer last took its queue was queued; zero when
	// it has none.
	queuedSince [writerKinds]time.Time
	// addTurns counts the turns give has handed to small adds (writerNewJobs),
	// not those handed to large adds (writerLargeNewJobs). A take
	// that finds no turn in progress is not counted, but then no writer is
	// waiting for an add to overtake.
	addTurns uint64
}

// queued tells w that a request has been queued for kind's writer, which
// becomes the oldest that writer has queued if it has none. The writer must
// call it, and swapped, holding the mutex that guards its queue.
func (w *writeTurns) queued(kind writerKind) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.queuedSince[kind].IsZero() {
		w.queuedSince[kind] = time.Now()
	}
}

// swapped tells w that kind's writer has taken every request queued for it,
// and returns when the oldest was queued, zero if there was none.
func (w *writeTurns) swapped(kind writerKind) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()

	oldest := w.queuedSince[kind]
	w.queuedSince[kind] = time.Time{}

	return oldest
}

// takeSince blocks until it is kind's turn to write, for a transaction whose
// oldest request was queued at oldest; the zero time is taken as now. The
// caller must call give once its transaction has ended.
func (w *writeTurns) takeSince(kind writerKind, oldest time.Time) {
	w.mu.Lock()

	if oldest.IsZero() {
		oldest = time.Now()
	}

	if w.kept && w.holder == kind {
		w.kept = false
		w.mu.Unlock()

		return
	}

	if !w.busy {
		w.busy = true
		w.holder = kind
		w.mu.Unlock()

		return
	}

	turn := writeTurn{kind: kind, ready: make(chan struct{}), oldest: oldest, addTurnsAtArrival: w.addTurns}
	w.waiting = append(w.waiting, turn)
	w.mu.Unlock()

	<-turn.ready
}

// give ends the current turn and hands the next one to the writer whose turn it
// is: a waiting writer, or the writer whose turn just ended, which then keeps
// it. With neither, no turn is in progress until the next take.
func (w *writeTurns) give() {
	w.mu.Lock()
	defer w.mu.Unlock()

	next, keep := w.nextTurn()

	switch {
	case keep:
		w.kept = true
	case next < 0:
		w.busy = false
	default:
		turn := w.waiting[next]

		if turn.kind == writerNewJobs {
			w.addTurns++
		}

		w.holder = turn.kind
		w.waiting = slices.Delete(w.waiting, next, next+1)

		close(turn.ready)
	}
}

// nextTurn returns the index in waiting of the writer to go next, or keep true
// if the writer whose turn is ending should keep it, or neither (-1, false) if
// no writer has a turn to take. With a small add waiting no turn is kept: the
// writer nextOther chooses from those an add has gone ahead of since they began
// waiting goes, or, if there is none, the small add that asked first. With no
// small add waiting, the writer nextOther chooses from all those waiting goes,
// unless the writer whose turn is ending keeps the turn: it does when it has
// requests queued since it took its queue, nextOther chose no coalescing
// writer, and either nobody waits or the oldest of those queued requests is
// older than the chosen chunk's or large add's time. So it can keep the turn
// while the other coalescing writer waits.
func (w *writeTurns) nextTurn() (int, bool) {
	add := slices.IndexFunc(w.waiting, func(turn writeTurn) bool {
		return turn.kind == writerNewJobs
	})

	next, coalescing := w.nextOther(add >= 0)

	if add >= 0 {
		if next < 0 {
			return add, false
		}

		return next, false
	}

	own := w.queuedSince[w.holder]
	if own.IsZero() || coalescing {
		return next, false
	}

	return next, next < 0 || own.Before(w.waiting[next].oldest)
}

// nextOther returns the index in waiting of the writer other than a small add
// to go next, or -1 if there is none, and whether it is a coalescing writer
// (the archive or best-effort writer). Of the writers that count, the
// coalescing writer that asked first goes unless a chunk or large add carries a
// time older than that of every coalescing writer; then the chunk or large add
// with the oldest time goes (of those with the same time, the one that asked
// first). With onlyOvertaken, only the writers a small add has gone ahead of
// since they began waiting count.
func (w *writeTurns) nextOther(onlyOvertaken bool) (int, bool) {
	first, coalescingOldest := w.firstCoalescing(onlyOvertaken)
	other, otherOldest := w.oldestUncoalesced(onlyOvertaken)

	if first >= 0 && (other < 0 || !otherOldest.Before(coalescingOldest)) {
		return first, true
	}

	return other, false
}

// firstCoalescing returns the index in waiting of the coalescing writer that
// asked first, or -1 if none counts, and the oldest time of the coalescing
// writers that count. With onlyOvertaken, only the writers a small add has
// gone ahead of since they began waiting count.
func (w *writeTurns) firstCoalescing(onlyOvertaken bool) (int, time.Time) {
	first := -1

	var oldest time.Time

	for i, turn := range w.waiting {
		if !turn.kind.coalescing() || !w.mayGoNext(turn, onlyOvertaken) {
			continue
		}

		if first < 0 {
			first, oldest = i, turn.oldest
		} else if turn.oldest.Before(oldest) {
			oldest = turn.oldest
		}
	}

	return first, oldest
}

// oldestUncoalesced returns the index in waiting of the chunk or large add
// carrying the oldest request (of those carrying the same, the one that asked
// first), or -1 if none counts, and when that request was made. With
// onlyOvertaken, only the writers a small add has gone ahead of since they
// began waiting count.
func (w *writeTurns) oldestUncoalesced(onlyOvertaken bool) (int, time.Time) {
	next := -1

	var oldest time.Time

	for i, turn := range w.waiting {
		if turn.kind.coalescing() || !w.mayGoNext(turn, onlyOvertaken) {
			continue
		}

		if next < 0 || turn.oldest.Before(oldest) {
			next, oldest = i, turn.oldest
		}
	}

	return next, oldest
}

// mayGoNext reports whether nextOther may choose turn: never a small add, and,
// with onlyOvertaken, only a writer a small add has gone ahead of since it
// began waiting.
func (w *writeTurns) mayGoNext(turn writeTurn, onlyOvertaken bool) bool {
	return turn.kind != writerNewJobs && (!onlyOvertaken || turn.addTurnsAtArrival != w.addTurns)
}

// batchWrite runs fn through bolt.Batch, recording the call under the batch
// writer in the "db writer" line as one transaction of the given number of
// items. bbolt may coalesce several calls into one transaction, and the hold
// then includes the other calls' functions and the shared commit.
func (db *db) batchWrite(items int, fn func(*bolt.Tx) error) error {
	asked := time.Now()

	var begun time.Time

	// bbolt calls fn on its batch goroutine, or again on this one if the batch
	// fails; either call happens before Batch returns, which it does by a
	// channel receive from that goroutine, so begun needs no other
	// synchronisation.
	err := db.bolt.Batch(func(tx *bolt.Tx) error {
		if begun.IsZero() {
			begun = time.Now()
		}

		return fn(tx)
	})

	db.writerStats.observe(dbWriterBatch, items, asked, begun)

	return err
}

// updateUncoalesced runs fn in its own bolt write transaction, outside write
// turns and Batch, recording it under the batch writer in the "db writer" line
// as one transaction of the given number of items.
func (db *db) updateUncoalesced(items int, fn func(*bolt.Tx) error) error {
	asked := time.Now()

	var begun time.Time

	err := db.bolt.Update(func(tx *bolt.Tx) error {
		begun = time.Now()

		return fn(tx)
	})

	db.writerStats.observe(dbWriterBatch, items, asked, begun)

	return err
}
