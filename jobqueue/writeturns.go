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

// updateInTurn runs fn in a bolt write transaction once it is kind's turn to
// write, and ends the turn even if fn panics. The writer's oldest request is
// taken to be one made now.
func (db *db) updateInTurn(kind writerKind, fn func(*bolt.Tx) error) error {
	return db.updateInTurnSince(kind, time.Time{}, fn)
}

// updateInTurnSince is updateInTurn for a writer whose oldest request was
// queued at oldest (the zero time means now).
func (db *db) updateInTurnSince(kind writerKind, oldest time.Time, fn func(*bolt.Tx) error) error {
	db.turns.takeSince(kind, oldest)
	defer db.turns.give()

	return db.bolt.Update(fn)
}

// writeTurn is a writer waiting for its turn, woken by closing ready.
type writeTurn struct {
	kind  writerKind
	ready chan struct{}
	// oldest is when the oldest request this writer's transaction carries was
	// queued.
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
// Adds small enough to be cheap to let through (writerNewJobs; see
// newJobsWriterKind) go first. A waiting add goes next unless an add has
// already gone ahead of another waiting writer since that writer began waiting;
// then only the writers an add has gone ahead of go before it. So a waiting add
// usually commits right after the transaction in flight or, if it asks while a
// turn is kept, right after that writer's next transaction, and at worst after
// one transaction of each writer an add has already overtaken as well, however
// many others wait. Without the "already gone ahead" rule an add that arrived
// during each of those transactions went ahead again every time (item 6.4b of
// .docs/nohold/phase6.md). The rule is judged by when each writer asked, not by
// when its oldest request was queued: judged by request, the archive and
// best-effort writers, which under a steady stream nearly always carry a
// request older than the last add's turn, held every add back until each had
// gone, and adds lost the speed-up this ordering exists for (item 6.4d).
//
// Every other writer (the archive and best-effort writers, each chunk of a big
// add, and the new jobs writer with a large add) carries the time of the oldest
// request its transaction holds: the archive and best-effort writers that of the
// first request queued since they last took their queue (see queued and
// swapped), the others the moment they asked for the turn. Among them the
// oldest request goes first. When the archive or best-effort writer's turn ends
// with no small add waiting, and the requests it has queued since it took its
// queue are older than every waiting writer's, it keeps the turn for its next
// transaction, which it asks for as soon as it has replied to the requests it
// just wrote; until then no other writer goes.
//
// Ordering them by request rather than by when they asked matters because the
// archive and best-effort writers take their queues before they ask: requests
// that arrive while one of them waits behind a long transaction (a big add's
// chunk, megabytes on a production add) ride in its next transaction, which
// asks only after this one commits. Ordered by asking, a large add queued in
// that moment went ahead of them, and they waited behind two long transactions
// (items 6.4c and 6.4d of .docs/nohold/phase6.md). That is also why a large
// add gets no preference: its transaction can take seconds on a slow
// filesystem, and going ahead it often landed just after a chunk the other
// writers had already waited through.
//
// So, while its writer waits for a turn, a request queued for one of the other
// writers waits behind at most the transaction in flight, transactions carrying
// requests older than it (of each other coalescing writer at most two batches:
// the one it has taken and the one queued behind that; and one chunk of each
// big add in progress, since each chunk asks after the one before it has
// committed), and the small adds that go ahead of its writer: at most one small
// add per wait. While its writer is between its turn ending and asking again
// (the time it takes to reply to the requests it has just written, or a
// transaction it waits for before it takes its queue), the requests it has
// queued are invisible to the ordering: only its own keep decision, as its turn
// ends, sees them. Any writer waiting then, or asking then, can go ahead of
// them, whatever the age of its own request, so a chunk that asked after such a
// request can commit before it; how many go depends on how long that gap lasts.
// A batch is one transaction unless archives in it fail: applyFolded then
// retries the rest after each failing archive and re-runs each failed one
// alone, every attempt carrying the batch's oldest request, so a batch with k
// failing archives takes at most 2k+1 transactions (2k when all of its archives
// fail, 1 when its first transaction fails as a whole), all of which go ahead
// of the request. With more than one goroutine taking turns as the new jobs
// writer, an add also waits behind the adds queued before it, which go in the
// order they asked. Big and large adds, which get no preference, may take turns
// from several goroutines at once, and go in the order they asked.
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
// Only transactions run through updateInTurn or updateInTurnSince take a
// turn. Any other bolt write transaction queues on bolt's lock alongside
// whichever writer holds one. The zero value is ready to use.
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
	// addTurns counts the turns give has handed to the new jobs writer. A take
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
// no writer has a turn to take. With a small add waiting, the first to ask goes
// unless an add has already gone ahead of a waiting writer since that writer
// began waiting; then, of the writers an add has gone ahead of, the one carrying
// the oldest request goes. With no small add waiting, the writer carrying the
// oldest request goes, counting the requests the writer whose turn is ending has
// queued since it took its queue, which keeps the turn if they are the oldest.
func (w *writeTurns) nextTurn() (int, bool) {
	add := slices.IndexFunc(w.waiting, func(turn writeTurn) bool {
		return turn.kind == writerNewJobs
	})

	next, oldest := w.oldestOther(add >= 0)

	if add >= 0 {
		if next < 0 {
			return add, false
		}

		return next, false
	}

	own := w.queuedSince[w.holder]

	return next, !own.IsZero() && (next < 0 || own.Before(oldest))
}

// oldestOther returns the index in waiting of the writer other than a small
// add carrying the oldest request, and when that request was queued, or -1 if
// there is none. With onlyOvertaken, only the writers a small add has gone
// ahead of since they began waiting count.
func (w *writeTurns) oldestOther(onlyOvertaken bool) (int, time.Time) {
	next := -1

	var oldest time.Time

	for i, turn := range w.waiting {
		if turn.kind == writerNewJobs || (onlyOvertaken && turn.addTurnsAtArrival == w.addTurns) {
			continue
		}

		if next < 0 || turn.oldest.Before(oldest) {
			next, oldest = i, turn.oldest
		}
	}

	return next, oldest
}
