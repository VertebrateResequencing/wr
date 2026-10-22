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
)

// updateInTurn runs fn in a bolt write transaction once it is kind's turn to
// write, and ends the turn even if fn panics.
func (db *db) updateInTurn(kind writerKind, fn func(*bolt.Tx) error) error {
	db.turns.take(kind)
	defer db.turns.give()

	return db.bolt.Update(fn)
}

// writeTurn is a writer waiting for its turn, woken by closing ready.
type writeTurn struct {
	kind  writerKind
	ready chan struct{}
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
// The new jobs writer goes next when it is waiting, unless it has already
// gone ahead of the writer that has waited longest; otherwise that writer goes.
// So a waiting add usually commits right after the transaction in flight, and
// at worst after one transaction of each writer it has already overtaken. Each
// of the other writers waits behind at most the transaction in flight, one
// add, and one transaction of each other writer waiting (the archive writer's,
// the best-effort writer's, and a chunk of each big add in progress), however
// fast adds keep arriving. Without the "already gone ahead" rule an add that
// arrived during each of those transactions went ahead again every time, and
// a best-effort write waited behind one add per other writer (item 6.4b of
// .docs/nohold/phase6.md). The other writers' bound holds however many
// goroutines take turns as the new jobs writer, since any add's turn counts as
// having gone ahead; with more than one, an add also waits behind the adds
// queued before it. Big adds, which get no preference, may take turns from
// several goroutines at once.
//
// A big add's chunks take turns too. Each is a full transaction (megabytes on
// a production add), and one that queued on bolt's lock instead would get every
// other transaction while one chunk follows another, since only the turn
// holder competes with it there: a reservation's write then waited behind
// several chunks per turn of its own, past ReserveWriteWait (item 6.4b).
//
// Only transactions run through updateInTurn take a turn. Any other bolt write
// transaction queues on bolt's lock alongside whichever writer holds one. The
// zero value is ready to use.
type writeTurns struct {
	mu      sync.Mutex
	busy    bool
	waiting []writeTurn
	// addTurns counts the turns give has handed to the new jobs writer. A take
	// that finds no turn in progress is not counted, but then no writer is
	// waiting for an add to overtake.
	addTurns uint64
}

// take blocks until it is kind's turn to write. The caller must call give once
// its transaction has ended.
func (w *writeTurns) take(kind writerKind) {
	w.mu.Lock()

	if !w.busy {
		w.busy = true
		w.mu.Unlock()

		return
	}

	turn := writeTurn{kind: kind, ready: make(chan struct{}), addTurnsAtArrival: w.addTurns}
	w.waiting = append(w.waiting, turn)
	w.mu.Unlock()

	<-turn.ready
}

// give ends the current turn and hands the next one to the writer whose turn it
// is, if any is waiting.
func (w *writeTurns) give() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.waiting) == 0 {
		w.busy = false

		return
	}

	next := w.nextTurn()
	turn := w.waiting[next]

	if turn.kind == writerNewJobs {
		w.addTurns++
	}

	w.waiting = slices.Delete(w.waiting, next, next+1)

	close(turn.ready)
}

// nextTurn returns the index in waiting of the writer to go next: the new jobs
// writer if it is waiting and has not gone ahead of the longest-waiting writer
// of another kind since that writer began waiting, else that writer. With no
// new jobs writer waiting, the writer that has waited longest goes. waiting
// must not be empty.
func (w *writeTurns) nextTurn() int {
	add := slices.IndexFunc(w.waiting, func(turn writeTurn) bool {
		return turn.kind == writerNewJobs
	})
	if add < 0 {
		return 0
	}

	// the longest waiting other writer goes before another add if an add has
	// already gone ahead of it.
	other := slices.IndexFunc(w.waiting, func(turn writeTurn) bool {
		return turn.kind != writerNewJobs
	})
	if other >= 0 && w.waiting[other].addTurnsAtArrival != w.addTurns {
		return other
	}

	return add
}
