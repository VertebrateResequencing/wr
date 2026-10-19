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

// writerKind names one of the db's coalescing writer goroutines.
type writerKind int

const (
	writerNewJobs writerKind = iota
	writerArchive
	writerBestEffort
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
}

// writeTurns orders the write transactions of the new jobs, archive and
// best-effort writers, which otherwise queue on bolt's single write lock in
// arrival order. On a slow filesystem the archive writer, which re-queues the
// moment its transaction ends, fills that lock, and an add, whose client waits
// for it, ends up behind every transaction already queued (item 6.3d of
// .docs/nohold/phase6.md).
//
// The new jobs writer goes next whenever it is waiting; otherwise the writer
// that has waited longest goes. So a waiting add commits right after the
// transaction in flight. Each writer is a single goroutine, so it is never
// waiting when its own turn ends, and the others alternate with it rather than
// wait out an add storm: the archive and best-effort writers each wait behind at
// most three other transactions (the one in flight, the other's and an add's).
// These bounds assume exactly one goroutine takes turns per writerKind; a
// second new jobs goroutine could keep the turn between the two of them and
// starve the others.
//
// Other bolt write transactions do not take a turn and still queue on bolt's
// lock alongside whichever writer holds one. The zero value is ready to use.
type writeTurns struct {
	mu      sync.Mutex
	busy    bool
	waiting []writeTurn
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

	turn := writeTurn{kind: kind, ready: make(chan struct{})}
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
	w.waiting = slices.Delete(w.waiting, next, next+1)

	close(turn.ready)
}

// nextTurn returns the index in waiting of the writer to go next: the new jobs
// writer if it is waiting, else the one that has waited longest. waiting must
// not be empty.
func (w *writeTurns) nextTurn() int {
	if i := slices.IndexFunc(w.waiting, func(turn writeTurn) bool {
		return turn.kind == writerNewJobs
	}); i >= 0 {
		return i
	}

	return 0
}
