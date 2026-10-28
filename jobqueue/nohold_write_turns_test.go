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

// Items 6.3e to 6.4g of .docs/nohold/phase6.md: the new-jobs, archive and
// best-effort writers, and each chunk of an add too big for one transaction,
// take turns at bolt's single write lock, so a pending small add commits
// within a bounded number of other transactions, and archives still get their
// turns while adds keep arriving.
//
// The order of transactions is read from bolt's transaction ids (unique and
// increasing per committed write transaction): archives report theirs through
// the archiveTxObserver seam and adds through a putter of their own. Every
// writer is held at a known point by a gate inside a transaction, so the order
// does not depend on how fast the host is; the settle waits only give a
// goroutine that has been released time to reach the lock it then waits on.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

// noHoldWTQueuedAdds is how many adds, each needing a transaction of its own,
// the starvation test has queued behind the one in flight before an archive
// arrives.
const noHoldWTQueuedAdds = 8

// noHoldWTChunkedChunks is how many write transactions the big add of the
// chunked-add tests needs.
const noHoldWTChunkedChunks = 2

// noHoldWTLargeAddBytes is the size of an add too large to go ahead of the
// other writers, one byte over the limit, and noHoldWTSmallFoldAddBytes that of
// the largest add small enough to, two of which make a fold larger than one add
// may be.
const (
	noHoldWTLargeAddBytes     = newJobsPreferMaxBytes + 1
	noHoldWTSmallFoldAddBytes = newJobsPreferMaxBytes
)

// noHoldWTSizedAddValue is the size of each value a sized add stores.
const noHoldWTSizedAddValue = 8 * 1024

// errNoHoldWTChunk is what a big add's failing putter returns.
var errNoHoldWTChunk = errors.New("nohold write turns chunk failure")

// errNoHoldWTBadOp is what a failing noHoldWTFoldedOp's write returns, and
// errNoHoldWTWholeTx what a transaction that fails as a whole returns.
var (
	errNoHoldWTBadOp   = errors.New("nohold write turns bad op")
	errNoHoldWTWholeTx = errors.New("nohold write turns whole transaction failure")
)

// noHoldWTTxLog records the bolt transaction ids each observed write ran in,
// in the order they ran.
type noHoldWTTxLog struct {
	mu  sync.Mutex
	ids map[string][]int
}

func newNoHoldWTTxLog() *noHoldWTTxLog {
	return &noHoldWTTxLog{ids: make(map[string][]int)}
}

func (l *noHoldWTTxLog) record(name string, id int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.ids[name] = append(l.ids[name], id)
}

// get returns the id of the first transaction name ran in, or 0 if none.
func (l *noHoldWTTxLog) get(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.ids[name]) == 0 {
		return 0
	}

	return l.ids[name][0]
}

// all returns the ids of every transaction name ran in, in the order they ran.
func (l *noHoldWTTxLog) all(name string) []int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.ids[name])
}

func TestNoHoldWriteTurns(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database whose writers are all busy", t, func() {
		database := openReliable4WriteStormDB(t, ctx)
		defer func() { _ = database.close(ctx) }()

		txLog := newNoHoldWTTxLog()

		Convey("an add asking while an archive is in flight and only a best-effort write waits "+
			"commits in the next transaction, ahead of that write and of an archive queued after "+
			"the add asked", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "turns", 2)
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])

			arGate.awaitHeld(t)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			addDone := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "add", nil), "nohold-turns-add")
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			arGate.open()

			So(reliable4ACAwait(t, first), ShouldBeNil)
			So(reliable4ACAwait(t, second), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)
			So(reliable4ACAwait(t, addDone), ShouldBeNil)

			So(txLog.get("add"), ShouldEqual, txLog.get(archives[0].Key())+1)
			So(txLog.get(archives[1].Key()), ShouldBeGreaterThan, txLog.get("add"))
		})

		Convey("with only an add and a best-effort write waiting behind the archive in flight, a "+
			"big add's first transaction asking after them commits after both, so a big add's "+
			"transactions wait their turn too", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "chunked", 1)[0]
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)

			arGate.awaitHeld(t)

			addDone := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "add", nil), "nohold-turns-add")
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "big", nil),
				"nohold-turns-big", noHoldWTChunkedChunks)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 3)

			arGate.open()

			So(reliable4ACAwait(t, arDone), ShouldBeNil)
			So(reliable4ACAwait(t, addDone), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)
			So(reliable4ACAwait(t, bigDone), ShouldBeNil)

			So(txLog.get("add"), ShouldEqual, txLog.get(archive.Key())+1)
			So(txLog.get("big"), ShouldEqual, txLog.get("add")+2)
		})

		Convey("a big add's first transaction asking while only a best-effort write waits behind "+
			"the archive in flight commits after that write", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "chunkedalone", 1)[0]
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)

			arGate.awaitHeld(t)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "big", nil),
				"nohold-turns-big-alone", noHoldWTChunkedChunks)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			arGate.open()

			So(reliable4ACAwait(t, arDone), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)
			So(reliable4ACAwait(t, bigDone), ShouldBeNil)

			So(txLog.get("big"), ShouldEqual, txLog.get(archive.Key())+2)
		})

		Convey("a best-effort write queued while a big add's first transaction is in flight, "+
			"with no other writer waiting, commits between that transaction and the add's next "+
			"one", func() {
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			bigGate := newNoHoldWTGate()
			defer bigGate.open()

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "big", bigGate),
				"nohold-turns-big-held", noHoldWTChunkedChunks)

			bigGate.awaitHeld(t)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			bigGate.open()

			So(reliable4ACAwait(t, bigDone), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)

			chunks := txLog.all("big")
			So(len(chunks), ShouldEqual, noHoldWTChunkedChunks)
			So(chunks[1], ShouldEqual, chunks[0]+2)
		})

		Convey("an archive queued before an add went ahead of its writer commits ahead of a big "+
			"add's next chunk and a large add that asked after it was queued, the only writers "+
			"waiting with it, though the chunk asked before its writer did", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "aged", 3)

			archiveTxObserver = func(txID int, key []byte) { txLog.record(string(key), txID) }

			defer func() { archiveTxObserver = nil }()

			s := noHoldWTStartAgedSequence(t, database, txLog, "aged")
			defer s.open()

			s.add2Size = noHoldWTLargeAddBytes

			// archives[2] joins archives[1] after add1 has gone ahead of it, so
			// their transaction is marked by archives[1], the first queued.
			var third chan error

			s.duringAdd1 = func() {
				third = reliable4ACArchiveAsync(database, archives[2].Key(), archives[2])
				noHoldWTAwaitArchivesPending(t, database, 2)
			}

			// the archive writer holds archives[0] waiting for its turn, so
			// archives[1] stays queued behind it while the first add goes ahead.
			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			s.queueFirstAddAndBigB(t)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			s.releaseBigAThenQueueSecondAdd(t)

			So(reliable4ACAwait(t, first), ShouldBeNil)
			So(reliable4ACAwait(t, second), ShouldBeNil)
			So(reliable4ACAwait(t, third), ShouldBeNil)
			s.await(t)

			So(txLog.get("aged-add1"), ShouldBeLessThan, txLog.get(archives[0].Key()))
			So(txLog.get(archives[1].Key()), ShouldEqual, txLog.all("aged-bigB")[0]+1)
			So(txLog.get(archives[2].Key()), ShouldEqual, txLog.get(archives[1].Key()))
			So(txLog.get("aged-add2"), ShouldBeGreaterThan, txLog.get(archives[1].Key()))
		})

		Convey("with only an archive and a big add's next chunk waiting, both asking after the "+
			"last add's turn, a small add goes first and the archive next, though the archive was "+
			"queued before that add went ahead of its writer", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "agedsmall", 3)

			archiveTxObserver = func(txID int, key []byte) { txLog.record(string(key), txID) }

			defer func() { archiveTxObserver = nil }()

			s := noHoldWTStartAgedSequence(t, database, txLog, "agedsmall")
			defer s.open()

			// archives[2] joins archives[1] after add1 has gone ahead of it, so
			// their transaction is marked by archives[1], the first queued.
			var third chan error

			s.duringAdd1 = func() {
				third = reliable4ACArchiveAsync(database, archives[2].Key(), archives[2])
				noHoldWTAwaitArchivesPending(t, database, 2)
			}

			// the archive writer holds archives[0] waiting for its turn, so
			// archives[1] stays queued behind it while the first add goes ahead.
			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			s.queueFirstAddAndBigB(t)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			s.releaseBigAThenQueueSecondAdd(t)

			So(reliable4ACAwait(t, first), ShouldBeNil)
			So(reliable4ACAwait(t, second), ShouldBeNil)
			So(reliable4ACAwait(t, third), ShouldBeNil)
			s.await(t)

			So(txLog.get("agedsmall-add1"), ShouldBeLessThan, txLog.get(archives[0].Key()))
			// add2 has gone ahead of no writer waiting when bigB's chunk commits
			// (the archive writer asked after add1's turn), so it goes next, as
			// 6.3e's add preference needs; the archives follow it.
			bigB := txLog.all("agedsmall-bigB")[0]
			So(txLog.get("agedsmall-add2"), ShouldEqual, bigB+1)
			So(txLog.get(archives[1].Key()), ShouldEqual, bigB+2)
			So(txLog.get(archives[2].Key()), ShouldEqual, txLog.get(archives[1].Key()))
		})

		// each kind of best-effort write marks the batch it is queued in, so the
		// sequence below is run with changed[1]'s and changed[2]'s writes, the
		// first of their batch, of each kind.
		for _, write := range []struct {
			label, name string
			write       func(*db, *Job) error
		}{
			{"runstate", "a run-state", (*db).updateJobRunStateDurable},
			{"change", "a change", (*db).updateJobAfterChangeDurable},
			{"exit", "an exit", func(database *db, job *Job) error {
				return database.updateJobAfterExitDurable(job, nil, nil, false)
			}},
		} {
			Convey(write.name+" write queued before an add went ahead of its writer commits ahead "+
				"of a big add's next chunk and a large add that asked after it was queued, the "+
				"only writers waiting with it, though the chunk asked before its writer did", func() {
				changed := reliable4WSSeedLiveJobs(t, ctx, database, 3)
				prefix := "agedbe-" + write.label

				s := noHoldWTStartAgedSequence(t, database, txLog, prefix)
				defer s.open()

				s.add2Size = noHoldWTLargeAddBytes

				// changed[2]'s write joins changed[1]'s after add1 has gone ahead of
				// it, so their transaction is marked by changed[1]'s, the first queued.
				third := make(chan error, 1)

				s.duringAdd1 = func() {
					go func() { third <- write.write(database, changed[2]) }()

					noHoldWTAwaitBestEffortWaiters(t, database, 2)
				}

				first := noHoldWTBestEffortAsync(t, database, changed[0])
				noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

				s.queueFirstAddAndBigB(t)

				second := make(chan error, 1)

				go func() { second <- write.write(database, changed[1]) }()

				noHoldWTAwaitBestEffortWaiters(t, database, 1)

				s.releaseBigAThenQueueSecondAdd(t)

				So(reliable4ACAwait(t, first), ShouldBeNil)
				So(reliable4ACAwait(t, second), ShouldBeNil)
				So(reliable4ACAwait(t, third), ShouldBeNil)
				s.await(t)

				// the one transaction between bigB's chunk and bigA's second is the
				// best-effort writes of changed[1] and changed[2]; add2, which gets
				// no preference, asked after bigA's second chunk and goes after it.
				bigB := txLog.all(prefix + "-bigB")[0]
				So(txLog.all(prefix + "-bigA")[1], ShouldEqual, bigB+2)
				So(txLog.get(prefix+"-add2"), ShouldEqual, bigB+3)
			})
		}

		Convey("with only an archive and a big add's next chunk waiting, the archive goes first "+
			"when its request is older than the chunk's, though the chunk asked first", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "oldest", 2)

			archiveTxObserver = func(txID int, key []byte) { txLog.record(string(key), txID) }

			defer func() { archiveTxObserver = nil }()

			s := noHoldWTStartAgedSequence(t, database, txLog, "oldest")
			defer s.open()

			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			s.queueFirstAddAndBigB(t)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			s.releaseBigA(t)
			s.bigBGate.open()

			So(reliable4ACAwait(t, first), ShouldBeNil)
			So(reliable4ACAwait(t, second), ShouldBeNil)
			s.await(t)

			// bigA's second chunk asked for its turn before the archive writer
			// asked for archives[1]'s, but archives[1] was queued first.
			So(txLog.get(archives[1].Key()), ShouldEqual, txLog.all("oldest-bigB")[0]+1)
			So(txLog.all("oldest-bigA")[1], ShouldBeGreaterThan, txLog.get(archives[1].Key()))
		})

		Convey("an archive writer whose turn ends with only a large add waiting keeps it for an "+
			"archive queued before an add went ahead of its writer and before the large add asked", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "kept", 2)

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			bigGate := newNoHoldWTGate()
			defer bigGate.open()

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "kept-big", bigGate),
				"nohold-turns-kept-big", 1)
			bigGate.awaitHeld(t)

			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			add1 := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "kept-add1", nil), "nohold-turns-kept-add1")
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			// the big add's chunk commits, then add1, then archives[0], which is
			// held in its transaction while add2 queues.
			bigGate.open()
			arGate.awaitHeld(t)

			add2 := noHoldWTSizedAdd(t, database, noHoldWTRecordingPutter(txLog, "kept-add2", nil),
				"nohold-turns-kept-add2", noHoldWTLargeAddBytes)
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			arGate.open()

			for _, done := range []chan error{bigDone, first, add1, second, add2} {
				So(reliable4ACAwait(t, done), ShouldBeNil)
			}

			So(txLog.get("kept-add1"), ShouldEqual, txLog.get(archives[0].Key())-1)
			So(txLog.get(archives[1].Key()), ShouldEqual, txLog.get(archives[0].Key())+1)
			So(txLog.get("kept-add2"), ShouldEqual, txLog.get(archives[1].Key())+1)
		})

		Convey("an archive writer whose turn ends with a small add waiting does not keep "+
			"it", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "keptsmall", 2)

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			bigGate := newNoHoldWTGate()
			defer bigGate.open()

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "keptsmall-big", bigGate),
				"nohold-turns-keptsmall-big", 1)
			bigGate.awaitHeld(t)

			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			add1 := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "keptsmall-add1", nil),
				"nohold-turns-keptsmall-add1")
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			// the big add's chunk commits, then add1, then archives[0], which is
			// held in its transaction while add2 queues.
			bigGate.open()
			arGate.awaitHeld(t)

			add2 := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "keptsmall-add2", nil),
				"nohold-turns-keptsmall-add2")
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			arGate.open()

			for _, done := range []chan error{bigDone, first, add1, second, add2} {
				So(reliable4ACAwait(t, done), ShouldBeNil)
			}

			So(txLog.get("keptsmall-add1"), ShouldEqual, txLog.get(archives[0].Key())-1)
			So(txLog.get("keptsmall-add2"), ShouldEqual, txLog.get(archives[0].Key())+1)
			So(txLog.get(archives[1].Key()), ShouldEqual, txLog.get("keptsmall-add2")+1)
		})

		Convey("with no chunk or large add waiting, a best-effort write waiting when an archive "+
			"commits goes ahead of the archive queued for the archive writer's next transaction, "+
			"though that was queued first", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "byasking", 2)
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])

			arGate.awaitHeld(t)

			second := reliable4ACArchiveAsync(database, archives[1].Key(), archives[1])
			noHoldWTAwaitArchivesPending(t, database, 1)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			arGate.open()

			for _, done := range []chan error{first, second, beDone} {
				So(reliable4ACAwait(t, done), ShouldBeNil)
			}

			// the one transaction between the two archives' is the best-effort
			// write's.
			So(txLog.get(archives[1].Key()), ShouldEqual, txLog.get(archives[0].Key())+2)
		})

		Convey("a best-effort write queued after the writer's final drain began keeps no turn "+
			"for it once the database is closed", func() {
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			bestEffortSwappedHook = func() {
				database.beMu.Lock()
				defer database.beMu.Unlock()

				if database.beStopped {
					database.trackBestEffortLocked("nohold-turns-after-final-drain")
				}
			}

			defer func() { bestEffortSwappedHook = nil }()

			// a write queued without waking the writer is still queued at close,
			// so the final drain has a transaction, whose turn ends after the
			// hook's write was queued.
			encoded, err := database.encodeJob(changed)
			So(err, ShouldBeNil)

			database.wgMutex.Lock()
			database.beMu.Lock()
			database.enqueueChangeLocked(changed.Key(), encoded, nil)
			database.beMu.Unlock()
			database.wgMutex.Unlock()

			So(database.close(ctx), ShouldBeNil)

			done := make(chan error, 1)

			go func() { done <- database.updateInTurn(writerChunkedAdd, 1, func(*bolt.Tx) error { return nil }) }()

			So(reliable4ACAwait(t, done), ShouldWrap, berrors.ErrDatabaseNotOpen)
		})

		Convey("an archive offered during the archive writer's final drain is rejected and keeps "+
			"no turn for it once the database is closed", func() {
			archives := reliable4ACCompletableJobs(t, ctx, database, "stopped", 3)
			opB := noHoldWTArchiveOp(t, database, archives[1])
			opC := noHoldWTArchiveOp(t, database, archives[2])

			arGate := newNoHoldWTGate()
			offered := make(chan bool, 1)
			archiveTxObserver = func(_ int, key []byte) {
				switch string(key) {
				case archives[0].Key():
					arGate.wait()
				case archives[1].Key():
					offered <- database.enqueueArchive(opC)
				}
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			first := reliable4ACArchiveAsync(database, archives[0].Key(), archives[0])

			arGate.awaitHeld(t)

			// queued as enqueueArchive queues it, but without waking the writer,
			// so it is still queued when close stops the writer, and the final
			// drain has a transaction, during which opC is offered.
			database.arMu.Lock()
			database.arPending = append(database.arPending, opB)
			database.turns.queued(writerArchive)
			database.arMu.Unlock()

			closed := make(chan error, 1)

			go func() { closed <- database.close(ctx) }()

			select {
			case <-database.arStop:
			case <-time.After(reliable4ACWaitTimeout):
				t.Fatalf("close did not stop the archive writer within %s", reliable4ACWaitTimeout)
			}

			arGate.open()

			So(reliable4ACAwait(t, first), ShouldBeNil)
			So(reliable4ACAwait(t, closed), ShouldBeNil)
			So(reliable4ACAwait(t, opB.result), ShouldBeNil)

			select {
			case ok := <-offered:
				So(ok, ShouldBeFalse)
			case <-time.After(reliable4ACWaitTimeout):
				t.Fatalf("archive B's final-drain transaction never offered archive C within %s",
					reliable4ACWaitTimeout)
			}

			done := make(chan error, 1)

			go func() { done <- database.updateInTurn(writerChunkedAdd, 1, func(*bolt.Tx) error { return nil }) }()

			select {
			case err := <-done:
				So(err, ShouldWrap, berrors.ErrDatabaseNotOpen)
			case <-time.After(reliable4ACWaitTimeout):
				t.Fatalf("a turn is kept for the stopped archive writer: no write turn within %s",
					reliable4ACWaitTimeout)
			}
		})

		Convey("a big add whose put panics fails only itself and gives up its turn", func() {
			err := reliable4ACAwait(t, noHoldWTChunkedAddAsync(database, reliable4ACAddPanickingPut,
				"nohold-turns-panic", 1))
			So(errors.Is(err, errNewJobsPanic), ShouldBeTrue)

			So(reliable4ACAwait(t, noHoldWTChunkedAddAsync(database, putLiveRecordsInBucket,
				"nohold-turns-after-panic", noHoldWTChunkedChunks)), ShouldBeNil)
		})

		Convey("a writer whose transaction panics gives up its turn", func() {
			var recovered any

			func() {
				defer func() { recovered = recover() }()

				// this returns only if fn did not panic, and recovered then ends up nil.
				recovered = database.updateInTurn(writerBestEffort, 1, func(*bolt.Tx) error {
					panic("nohold write turns panic")
				})
			}()

			So(recovered, ShouldNotBeNil)

			done := make(chan error, 1)

			go func() { done <- database.updateInTurn(writerArchive, 1, func(*bolt.Tx) error { return nil }) }()

			So(reliable4ACAwait(t, done), ShouldBeNil)
		})

		Convey("two big adds in progress at once each wait for a turn of their own", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "twobig", 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(int, []byte) { arGate.wait() }

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)

			arGate.awaitHeld(t)

			bigA := noHoldWTChunkedAddAsync(database, putLiveRecordsInBucket, "nohold-turns-big-a",
				noHoldWTChunkedChunks)
			bigB := noHoldWTChunkedAddAsync(database, putLiveRecordsInBucket, "nohold-turns-big-b",
				noHoldWTChunkedChunks)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			arGate.open()

			So(reliable4ACAwait(t, arDone), ShouldBeNil)
			So(reliable4ACAwait(t, bigA), ShouldBeNil)
			So(reliable4ACAwait(t, bigB), ShouldBeNil)
		})

		Convey("a big add whose second transaction fails keeps its first one committed", func() {
			var (
				calls int
				first sobsd
			)

			put := func(tx *bolt.Tx, bucket []byte, encodes sobsd) error {
				calls++
				if calls > 1 {
					return errNoHoldWTChunk
				}

				first = slices.Clone(encodes)

				return putLiveRecordsInBucket(tx, bucket, encodes)
			}

			err := reliable4ACAwait(t, noHoldWTChunkedAddAsync(database, put, "nohold-turns-partial",
				noHoldWTChunkedChunks))
			So(errors.Is(err, errNoHoldWTChunk), ShouldBeTrue)
			So(len(first), ShouldEqual, storeBatchGranularity)

			stored := 0

			So(database.bolt.View(func(tx *bolt.Tx) error {
				for _, kv := range first {
					if tx.Bucket(bucketJobsLive).Get(kv[0]) != nil {
						stored++
					}
				}

				return nil
			}), ShouldBeNil)
			So(stored, ShouldEqual, len(first))
		})

		Convey("a write that takes no turn does not wait for the turn in progress", func() {
			job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			database.turns.takeSince(writerArchive, time.Time{})
			defer database.turns.give()

			done := make(chan error, 1)

			go func() { done <- database.deleteLiveJobs(ctx, []string{job.Key()}) }()

			So(reliable4ACAwait(t, done), ShouldBeNil)
		})

		Convey("with an archive in flight and only a best-effort write waiting, an add too large "+
			"to go ahead of the other writers commits after that write", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "large", 1)[0]
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)

			arGate.awaitHeld(t)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			large := noHoldWTSizedAdd(t, database, noHoldWTRecordingPutter(txLog, "large", nil),
				"nohold-turns-large", noHoldWTLargeAddBytes)
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			arGate.open()

			So(reliable4ACAwait(t, arDone), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)
			So(reliable4ACAwait(t, large), ShouldBeNil)

			So(txLog.get("large"), ShouldEqual, txLog.get(archive.Key())+2)
		})

		Convey("adds each small enough to go ahead still go ahead when folded into a transaction "+
			"larger than one add may be: the fold goes ahead of an archive, the only other writer "+
			"waiting, that asked after the last add's turn began", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "smallfold", 1)[0]
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			archiveTxObserver = func(txID int, key []byte) { txLog.record(string(key), txID) }

			defer func() { archiveTxObserver = nil }()

			njGate := newNoHoldWTGate()
			defer njGate.open()

			blocker := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "blocker", njGate),
				"nohold-turns-fold-blocker")
			njGate.awaitHeld(t)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			put := noHoldWTRecordingPutter(txLog, "folded", nil)
			first := noHoldWTSizedAdd(t, database, put, "nohold-turns-fold-a", noHoldWTSmallFoldAddBytes)
			second := noHoldWTSizedAdd(t, database, put, "nohold-turns-fold-b", noHoldWTSmallFoldAddBytes)
			reliable4ACAddAwaitPending(t, database, 2)

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)
			noHoldWTAwaitArchivesPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			njGate.open()

			So(reliable4ACAwait(t, blocker), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)
			So(reliable4ACAwait(t, first), ShouldBeNil)
			So(reliable4ACAwait(t, second), ShouldBeNil)
			So(reliable4ACAwait(t, arDone), ShouldBeNil)

			So(txLog.all("folded"), ShouldResemble, []int{txLog.get("folded"), txLog.get("folded")})
			So(txLog.get("folded"), ShouldEqual, txLog.get("blocker")+2)
			So(txLog.get(archive.Key()), ShouldEqual, txLog.get("folded")+1)
		})

		Convey("a small add folded with one too large to go ahead of the other writers commits "+
			"after a best-effort write queued before them, the only other writer waiting", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "mixedfold", 1)[0]
			changed := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

			arGate := newNoHoldWTGate()
			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)
				arGate.wait()
			}

			defer func() { archiveTxObserver = nil }()

			defer arGate.open()

			njGate := newNoHoldWTGate()
			defer njGate.open()

			blocker := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "blocker", njGate),
				"nohold-turns-mixed-blocker")
			njGate.awaitHeld(t)

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)
			noHoldWTAwaitArchivesPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			beDone := noHoldWTBestEffortAsync(t, database, changed)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			put := noHoldWTRecordingPutter(txLog, "mixed", nil)
			small := noHoldWTSizedAdd(t, database, put, "nohold-turns-mixed-small", noHoldWTSmallFoldAddBytes)
			large := noHoldWTSizedAdd(t, database, put, "nohold-turns-mixed-large", noHoldWTLargeAddBytes)
			reliable4ACAddAwaitPending(t, database, 2)

			njGate.open()
			arGate.awaitHeld(t)
			reliable4ACAddAwaitPending(t, database, 0)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			arGate.open()

			So(reliable4ACAwait(t, blocker), ShouldBeNil)
			So(reliable4ACAwait(t, arDone), ShouldBeNil)
			So(reliable4ACAwait(t, beDone), ShouldBeNil)
			So(reliable4ACAwait(t, small), ShouldBeNil)
			So(reliable4ACAwait(t, large), ShouldBeNil)

			So(txLog.all("mixed"), ShouldResemble, []int{txLog.get("mixed"), txLog.get("mixed")})
			So(txLog.get(archive.Key()), ShouldEqual, txLog.get("blocker")+1)
			So(txLog.get("mixed"), ShouldEqual, txLog.get(archive.Key())+2)
		})

		// a batch of a good and a failing archive takes three transactions: the
		// batch, which fails at the failing archive; the retry of the good one;
		// and the failing one's solo re-run. Each carries the batch's oldest
		// request, so a chunk that asked after that request, while the attempt
		// before was held, still goes after it, though an add went in between.
		for _, attempt := range []struct {
			name   string
			heldAt int
			solo   bool
		}{
			{"the retry of a batch's archives that did not fail", 1, false},
			{"a failed archive's solo re-run", 2, true},
		} {
			Convey(attempt.name+" goes ahead of a chunk that asked after the batch's first "+
				"archive was queued, once an add that went ahead of the chunk has gone and "+
				"nothing else waits", func() {
				prefix := fmt.Sprintf("failing%d", attempt.heldAt)
				jobs := reliable4ACCompletableJobs(t, ctx, database, prefix, 3)
				blocker, good, bad := jobs[0], jobs[1], jobs[2]

				blockerGate, attemptGate, addGate := newNoHoldWTGate(), newNoHoldWTGate(), newNoHoldWTGate()
				goodAttempts := 0

				archiveTxObserver = func(txID int, key []byte) {
					txLog.record(string(key), txID)

					switch string(key) {
					case blocker.Key():
						blockerGate.wait()
					case bad.Key():
						panic("nohold write turns failing archive")
					case good.Key():
						goodAttempts++
						if goodAttempts == attempt.heldAt {
							attemptGate.wait()
						}
					}
				}

				defer func() { archiveTxObserver = nil }()

				defer blockerGate.open()
				defer attemptGate.open()
				defer addGate.open()

				blockerDone := reliable4ACArchiveAsync(database, blocker.Key(), blocker)

				blockerGate.awaitHeld(t)

				goodDone := reliable4ACArchiveAsync(database, good.Key(), good)
				noHoldWTAwaitArchivesPending(t, database, 1)

				badDone := reliable4ACArchiveAsync(database, bad.Key(), bad)
				noHoldWTAwaitArchivesPending(t, database, 2)

				blockerGate.open()
				attemptGate.awaitHeld(t)

				bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, prefix+"-big", nil),
					"nohold-turns-"+prefix+"-big", noHoldWTChunkedChunks)
				noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

				addDone := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, prefix+"-add", addGate),
					"nohold-turns-"+prefix+"-add")
				reliable4ACAddAwaitPending(t, database, 0)
				noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

				attemptGate.open()
				addGate.awaitHeld(t)
				noHoldWTAwaitTurnWaiters(t, &database.turns, 2)
				addGate.open()

				So(reliable4ACAwait(t, blockerDone), ShouldBeNil)
				So(reliable4ACAwait(t, goodDone), ShouldBeNil)
				So(reliable4ACAwait(t, badDone), ShouldWrap, errArchivePanic)
				So(reliable4ACAwait(t, bigDone), ShouldBeNil)
				So(reliable4ACAwait(t, addDone), ShouldBeNil)

				checked := good.Key()
				if attempt.solo {
					checked = bad.Key()
				}

				attempts := txLog.all(checked)
				So(attempts, ShouldHaveLength, 2)
				So(txLog.get(prefix+"-add"), ShouldBeLessThan, attempts[1])

				// the solo re-run rolls back, and a rolled-back transaction has
				// the id the next committed one gets, so going first it shares
				// the chunk's id rather than coming before it.
				So(attempts[1], ShouldBeLessThanOrEqualTo, txLog.get(prefix+"-big"))
			})
		}

		Convey("with only a chunk waiting that asked after it was queued, an archive queued "+
			"during a failing batch's first attempt keeps the turn for the archive writer at each "+
			"attempt's end, so the next batch, which carries it, commits ahead of the chunk", func() {
			jobs := reliable4ACCompletableJobs(t, ctx, database, "keptattempts", 4)
			blocker, good, bad, later := jobs[0], jobs[1], jobs[2], jobs[3]

			blockerGate, attemptGate := newNoHoldWTGate(), newNoHoldWTGate()

			archiveTxObserver = func(txID int, key []byte) {
				txLog.record(string(key), txID)

				switch string(key) {
				case blocker.Key():
					blockerGate.wait()
				case bad.Key():
					panic("nohold write turns failing archive")
				case good.Key():
					attemptGate.wait()
				}
			}

			defer func() { archiveTxObserver = nil }()

			defer blockerGate.open()
			defer attemptGate.open()

			blockerDone := reliable4ACArchiveAsync(database, blocker.Key(), blocker)

			blockerGate.awaitHeld(t)

			goodDone := reliable4ACArchiveAsync(database, good.Key(), good)
			noHoldWTAwaitArchivesPending(t, database, 1)

			badDone := reliable4ACArchiveAsync(database, bad.Key(), bad)
			noHoldWTAwaitArchivesPending(t, database, 2)

			blockerGate.open()
			attemptGate.awaitHeld(t)

			laterDone := reliable4ACArchiveAsync(database, later.Key(), later)
			noHoldWTAwaitArchivesPending(t, database, 1)

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "keptattempts-big", nil),
				"nohold-turns-keptattempts-big", noHoldWTChunkedChunks)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			attemptGate.open()

			So(reliable4ACAwait(t, blockerDone), ShouldBeNil)
			So(reliable4ACAwait(t, goodDone), ShouldBeNil)
			So(reliable4ACAwait(t, badDone), ShouldWrap, errArchivePanic)
			So(reliable4ACAwait(t, laterDone), ShouldBeNil)
			So(reliable4ACAwait(t, bigDone), ShouldBeNil)

			So(txLog.all(bad.Key()), ShouldHaveLength, 2)
			So(txLog.get(later.Key()), ShouldBeLessThan, txLog.get("keptattempts-big"))
		})

		Convey("an add too large to go ahead of the other writers gets no preference when it "+
			"is re-run alone after failing in its transaction", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "largesolo", 1)[0]

			archiveTxObserver = func(txID int, key []byte) { txLog.record(string(key), txID) }

			defer func() { archiveTxObserver = nil }()

			largeGate, bigGate := newNoHoldWTGate(), newNoHoldWTGate()
			defer largeGate.open()
			defer bigGate.open()

			failing := func(tx *bolt.Tx, _ []byte, _ sobsd) error {
				txLog.record("large", tx.ID())
				largeGate.wait()

				return errNoHoldWTChunk
			}

			large := noHoldWTSizedAdd(t, database, failing, "nohold-turns-large-solo", noHoldWTLargeAddBytes)
			largeGate.awaitHeld(t)

			bigDone := noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, "big", bigGate),
				"nohold-turns-large-solo-big", noHoldWTChunkedChunks)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 1)

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)

			largeGate.open()
			bigGate.awaitHeld(t)
			noHoldWTAwaitTurnWaiters(t, &database.turns, 2)
			bigGate.open()

			So(reliable4ACAwait(t, large), ShouldWrap, errNoHoldWTChunk)
			So(reliable4ACAwait(t, bigDone), ShouldBeNil)
			So(reliable4ACAwait(t, arDone), ShouldBeNil)

			attempts := txLog.all("large")
			So(attempts, ShouldHaveLength, 2)
			So(txLog.get(archive.Key()), ShouldBeLessThan, attempts[1])
		})

		Convey("with only small adds waiting besides it, an archive commits after at most one "+
			"more add transaction while they stay queued", func() {
			archive := reliable4ACCompletableJobs(t, ctx, database, "starve", 1)[0]

			archiveTxObserver = func(txID int, key []byte) { txLog.record(string(key), txID) }

			defer func() { archiveTxObserver = nil }()

			njGate := newNoHoldWTGate()
			defer njGate.open()

			blocker := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "blocker", njGate),
				"nohold-turns-blocker")
			njGate.awaitHeld(t)

			ops := make([]*newJobsOp, noHoldWTQueuedAdds)
			for i := range ops {
				ops[i] = reliable4ACAddCostedOp("nohold-turns-queued-" + string(rune('a'+i)))

				So(database.enqueueNewJobs(ops[i]), ShouldBeTrue)
			}

			arDone := reliable4ACArchiveAsync(database, archive.Key(), archive)
			noHoldWTAwaitArchivesPending(t, database, 0)
			time.Sleep(reliable4ACAddSettle)

			njGate.open()

			So(reliable4ACAwait(t, blocker), ShouldBeNil)
			So(reliable4ACAwait(t, arDone), ShouldBeNil)

			for _, op := range ops {
				So(reliable4ACAwait(t, op.result), ShouldBeNil)
			}

			addsAhead := txLog.get(archive.Key()) - txLog.get("blocker") - 1
			So(addsAhead, ShouldBeLessThanOrEqualTo, 1)
		})
	})
}

func newNoHoldWTGate() *noHoldWTGate {
	return &noHoldWTGate{held: make(chan struct{}), opened: make(chan struct{})}
}

// noHoldWTBestEffortAsync queues a durable best-effort write of job and waits
// until the best-effort writer has taken it for its next transaction. That
// writer may not yet be waiting for its turn when this returns, so callers wait
// for it with noHoldWTAwaitTurnWaiters. It returns a channel for the write's
// outcome.
func noHoldWTBestEffortAsync(t *testing.T, database *db, job *Job) chan error {
	t.Helper()

	beSwapped := make(chan struct{})

	var beOnce sync.Once

	bestEffortSwappedHook = func() { beOnce.Do(func() { close(beSwapped) }) }

	defer func() { bestEffortSwappedHook = nil }()

	done := make(chan error, 1)

	go func() { done <- database.updateJobAfterChangeDurable(job) }()

	select {
	case <-beSwapped:
	case <-time.After(reliable4ACWaitTimeout):
		t.Fatalf("the best-effort writer never took its write")
	}

	return done
}

// noHoldWTAddAsync queues one add through putter and returns a channel for its
// outcome.
func noHoldWTAddAsync(database *db, put sobsdPutter, key string) chan error {
	done := make(chan error, 1)

	go func() {
		done <- reliable4ACAddStore(database, put, sobsd{{[]byte(key), []byte("nohold write turns")}})
	}()

	return done
}

// noHoldWTArchiveOp returns an archive of job, ready to queue for the archive
// writer as archiveCompletion would.
func noHoldWTArchiveOp(t *testing.T, database *db, job *Job) *archiveOp {
	t.Helper()

	encoded, err := database.encodeJob(job)
	if err != nil {
		t.Fatalf("encoding %s: %s", job.Key(), err)
	}

	return &archiveOp{
		key:     []byte(job.Key()),
		encoded: encoded,
		job:     job,
		result:  make(chan error, 1),
		queued:  time.Now(),
	}
}

// noHoldWTAwaitArchivesPending waits for the archive writer's pending queue to
// hold exactly want archives, failing rather than hanging.
func noHoldWTAwaitArchivesPending(t *testing.T, database *db, want int) {
	t.Helper()

	deadline := time.Now().Add(reliable4ACWaitTimeout)

	for time.Now().Before(deadline) {
		database.arMu.Lock()
		depth := len(database.arPending)
		database.arMu.Unlock()

		if depth == want {
			return
		}

		time.Sleep(reliable4ACAddPoll)
	}

	t.Fatalf("the archive writer's pending queue never held %d archives within %s",
		want, reliable4ACWaitTimeout)
}

// noHoldWTAwaitTurnWaiters waits for want writers to be waiting for a turn.
func noHoldWTAwaitTurnWaiters(t *testing.T, turns *writeTurns, want int) {
	t.Helper()

	deadline := time.Now().Add(reliable4ACWaitTimeout)

	for time.Now().Before(deadline) {
		turns.mu.Lock()
		n := len(turns.waiting)
		turns.mu.Unlock()

		if n == want {
			return
		}

		time.Sleep(reliable4ACAddPoll)
	}

	t.Fatalf("%d writers never waited for a turn within %s", want, reliable4ACWaitTimeout)
}

// noHoldWTChunkedAddAsync stores an add too big for one transaction, of chunks
// storeBatched-sized chunks, through put on the chunked add path, and returns a
// channel for its outcome.
func noHoldWTChunkedAddAsync(database *db, put sobsdPutter, prefix string, chunks int) chan error {
	encodes := make(sobsd, 0, chunks*storeBatchGranularity)
	for i := range chunks * storeBatchGranularity {
		encodes = append(encodes, [2][]byte{[]byte(fmt.Sprintf("%s-%06d", prefix, i)), []byte("big add")})
	}

	done := make(chan error, 1)

	go func() {
		done <- database.storeNewJobDataChunked([]newJobStore{{bucketJobsLive, encodes, put}})
	}()

	return done
}

// noHoldWTRecordingPutter returns a putter that records the transaction id it
// runs in under name, waits at gate if gate is not nil, and then stores the
// values in the live bucket.
func noHoldWTRecordingPutter(log *noHoldWTTxLog, name string, gate *noHoldWTGate) sobsdPutter {
	return func(tx *bolt.Tx, bucket []byte, encodes sobsd) error {
		log.record(name, tx.ID())

		if gate != nil {
			gate.wait()
		}

		return putLiveRecordsInBucket(tx, bucket, encodes)
	}
}

// noHoldWTStartAgedSequence starts bigA and waits until its first chunk is
// held in its transaction.
func noHoldWTStartAgedSequence(t *testing.T, database *db, txLog *noHoldWTTxLog,
	prefix string) *noHoldWTAgedSequence {
	t.Helper()

	s := &noHoldWTAgedSequence{
		database: database, txLog: txLog, prefix: prefix,
		bigAGate: newNoHoldWTGate(), bigBGate: newNoHoldWTGate(), add1Gate: newNoHoldWTGate(),
	}

	s.bigA = noHoldWTChunkedAddAsync(database, noHoldWTRecordingPutter(txLog, prefix+"-bigA", s.bigAGate),
		prefix+"-bigA", noHoldWTChunkedChunks)
	s.bigAGate.awaitHeld(t)

	return s
}

// noHoldWTAwaitBestEffortWaiters waits for the best-effort writer's pending
// batch to hold exactly want durable waiters, failing rather than hanging.
func noHoldWTAwaitBestEffortWaiters(t *testing.T, database *db, want int) {
	t.Helper()

	deadline := time.Now().Add(reliable4ACWaitTimeout)

	for time.Now().Before(deadline) {
		database.beMu.Lock()
		n := len(database.beWaiters)
		database.beMu.Unlock()

		if n == want {
			return
		}

		time.Sleep(reliable4ACAddPoll)
	}

	t.Fatalf("the best-effort writer's pending batch never held %d waiters within %s",
		want, reliable4ACWaitTimeout)
}

// noHoldWTSizedAdd queues one add of exactly size encoded bytes, costed as
// storeNewJobStores costs an add, stored through put, and returns a channel
// for its outcome.
func noHoldWTSizedAdd(t *testing.T, database *db, put sobsdPutter, prefix string, size int) chan error {
	t.Helper()

	var encodes sobsd

	for remaining := size; remaining > 0; {
		key := []byte(fmt.Sprintf("%s-%06d", prefix, len(encodes)))
		value := make([]byte, max(0, min(noHoldWTSizedAddValue, remaining-len(key))))
		encodes = append(encodes, [2][]byte{key, value})
		remaining -= len(key) + len(value)
	}

	stores := []newJobStore{{bucketJobsLive, encodes, put}}
	foldBytes, foldPuts := newJobsFoldCost(stores)

	if foldBytes != size {
		t.Fatalf("sized add of %d bytes costed at %d", size, foldBytes)
	}

	op := &newJobsOp{stores: stores, result: make(chan error, 1), foldBytes: foldBytes, foldPuts: foldPuts}

	if !database.enqueueNewJobs(op) {
		t.Fatalf("the new jobs writer has stopped")
	}

	return op.result
}

// noHoldWTGate holds the first caller of wait inside its transaction until
// open is called, and tells the test once it is held.
type noHoldWTGate struct {
	once      sync.Once
	held      chan struct{}
	opened    chan struct{}
	closeOnce sync.Once
}

func (g *noHoldWTGate) wait() {
	g.once.Do(func() {
		close(g.held)
		<-g.opened
	})
}

func (g *noHoldWTGate) open() {
	g.closeOnce.Do(func() { close(g.opened) })
}

func (g *noHoldWTGate) awaitHeld(t *testing.T) {
	t.Helper()

	select {
	case <-g.held:
	case <-time.After(reliable4ACWaitTimeout):
		t.Fatalf("no write transaction reached the gate within %s", reliable4ACWaitTimeout)
	}
}

// noHoldWTAgedSequence drives the sequence of item 6.4d of
// .docs/nohold/phase6.md: a big add's first chunk (bigA) is held in its
// transaction while a coalescing writer waits for its turn with one request
// and has a second queued behind it; an add (add1) and another big add's first
// chunk (bigB) wait too. Once bigA's chunk commits, add1 goes ahead of the
// waiting writer, then that writer's first transaction, then bigB's chunk,
// which is held while bigA's second chunk and the writer, now carrying its
// second request, ask for their turns. A second add (add2) then arrives before
// bigB's chunk commits.
type noHoldWTAgedSequence struct {
	database *db
	txLog    *noHoldWTTxLog
	prefix   string
	bigAGate *noHoldWTGate
	bigBGate *noHoldWTGate
	add1Gate *noHoldWTGate
	// duringAdd1, if set, is called while add1 is held in its transaction.
	duringAdd1 func()
	// add2Size, if not 0, makes add2 an add of about that many encoded bytes
	// instead of a one-job add.
	add2Size int
	bigA     chan error
	bigB     chan error
	add1     chan error
	add2     chan error
}

// queueFirstAddAndBigB queues add1 and then bigB behind the one writer
// already waiting.
func (s *noHoldWTAgedSequence) queueFirstAddAndBigB(t *testing.T) {
	t.Helper()

	s.add1 = noHoldWTAddAsync(s.database, noHoldWTRecordingPutter(s.txLog, s.prefix+"-add1", s.add1Gate),
		s.prefix+"-add1")
	reliable4ACAddAwaitPending(t, s.database, 0)
	noHoldWTAwaitTurnWaiters(t, &s.database.turns, 2)

	s.bigB = noHoldWTChunkedAddAsync(s.database,
		noHoldWTRecordingPutter(s.txLog, s.prefix+"-bigB", s.bigBGate), s.prefix+"-bigB",
		noHoldWTChunkedChunks)
	noHoldWTAwaitTurnWaiters(t, &s.database.turns, 3)
}

// releaseBigA lets bigA's first chunk commit, then add1, calling duringAdd1
// while add1 is held in its transaction, and waits until bigB's first chunk is
// held in its transaction with bigA's second chunk and the coalescing writer
// both waiting for a turn.
func (s *noHoldWTAgedSequence) releaseBigA(t *testing.T) {
	t.Helper()

	s.bigAGate.open()
	s.add1Gate.awaitHeld(t)

	if s.duringAdd1 != nil {
		s.duringAdd1()
	}

	s.add1Gate.open()
	s.bigBGate.awaitHeld(t)
	noHoldWTAwaitTurnWaiters(t, &s.database.turns, 2)
}

// releaseBigAThenQueueSecondAdd is releaseBigA, then queues add2 and releases
// bigB's chunk once add2 waits for its turn.
func (s *noHoldWTAgedSequence) releaseBigAThenQueueSecondAdd(t *testing.T) {
	t.Helper()

	s.releaseBigA(t)

	put := noHoldWTRecordingPutter(s.txLog, s.prefix+"-add2", nil)

	if s.add2Size > 0 {
		s.add2 = noHoldWTSizedAdd(t, s.database, put, s.prefix+"-add2", s.add2Size)
	} else {
		s.add2 = noHoldWTAddAsync(s.database, put, s.prefix+"-add2")
	}

	reliable4ACAddAwaitPending(t, s.database, 0)
	noHoldWTAwaitTurnWaiters(t, &s.database.turns, 3)

	s.bigBGate.open()
}

// open releases both gates, so a failed test does not leave a writer held.
func (s *noHoldWTAgedSequence) open() {
	s.bigAGate.open()
	s.add1Gate.open()
	s.bigBGate.open()
}

// await waits for every add the sequence started, requiring each to succeed.
func (s *noHoldWTAgedSequence) await(t *testing.T) {
	t.Helper()

	for _, done := range []chan error{s.bigA, s.bigB, s.add1, s.add2} {
		if done != nil {
			So(reliable4ACAwait(t, done), ShouldBeNil)
		}
	}
}

// noHoldWTFoldedOp is an op for applyFolded that fails whenever it is written,
// if bad, and records the outcome it is given.
type noHoldWTFoldedOp struct {
	bad     bool
	replied error
}

func (op *noHoldWTFoldedOp) reply(err error) {
	op.replied = err
}

// TestNoHoldWriteTurnsFailingBatchAttempts calls applyFolded with a fake
// transaction and counts the transactions it would take for a batch with
// failing ops (in production each would be a write turn of its own for the
// archive writer), against the counts in Bound 6 of the writeTurns doc. Its
// cases are a batch in which some ops fail, one in which every op fails, and
// one whose first transaction fails as a whole, and they check the transaction
// count and each op's reply (item 6.4g of .docs/nohold/phase6.md).
func TestNoHoldWriteTurnsFailingBatchAttempts(t *testing.T) {
	if runnermode || servermode {
		return
	}

	attempts := func(ops []*noHoldWTFoldedOp, whole bool) int {
		n := 0

		applyFolded(slices.Clone(ops), func(batch []*noHoldWTFoldedOp, failed *int) error {
			n++

			if whole {
				return errNoHoldWTWholeTx
			}

			for i, op := range batch {
				if !op.bad {
					continue
				}

				if failed != nil {
					*failed = i
				}

				return errNoHoldWTBadOp
			}

			return nil
		})

		return n
	}

	batch := func(bad ...bool) []*noHoldWTFoldedOp {
		ops := make([]*noHoldWTFoldedOp, len(bad))
		for i, b := range bad {
			ops[i] = &noHoldWTFoldedOp{bad: b}
		}

		return ops
	}

	Convey("A batch with k failing ops and at least one that does not fail takes 2k+1 "+
		"transactions, the failing ops getting their own error and the rest none", t, func() {
		for k, ops := range [][]*noHoldWTFoldedOp{
			batch(false, false, false),
			batch(false, true, false),
			batch(true, false, true),
		} {
			So(attempts(ops, false), ShouldEqual, 2*k+1)

			for _, op := range ops {
				var want error
				if op.bad {
					want = errNoHoldWTBadOp
				}

				So(op.replied, ShouldEqual, want)
			}
		}
	})

	Convey("A batch whose every op fails takes 2k transactions", t, func() {
		ops := batch(true, true, true)
		So(attempts(ops, false), ShouldEqual, 6)
	})

	Convey("A batch whose first transaction fails as a whole takes 1, every op getting "+
		"that error", t, func() {
		ops := batch(false, false)
		So(attempts(ops, true), ShouldEqual, 1)
		So(ops[0].replied, ShouldEqual, errNoHoldWTWholeTx)
		So(ops[1].replied, ShouldEqual, errNoHoldWTWholeTx)
	})
}

// TestNoHoldWriteTurnsAddOvertakesOnce drives writeTurns directly (item 6.4b
// of .docs/nohold/phase6.md). Its cases set up a turn in flight with an archive
// and a best-effort write waiting while adds keep arriving, with a big add's
// chunk or a large add waiting alongside an archive, and with two adds queued
// before an archive, and check the order the writers' turns go in.
func TestNoHoldWriteTurnsAddOvertakesOnce(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a write turn in flight and an archive and a best-effort write waiting", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerArchive, time.Time{})

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)

		queue(writerArchive, "archive", 1)
		queue(writerBestEffort, "best-effort", 2)

		Convey("an add that keeps arriving goes ahead of each of them only once", func() {
			queue(writerNewJobs, "add1", 3)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queue(writerNewJobs, "add2", 3)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			queue(writerArchive, "archive2", 1)
			queue(writerNewJobs, "add3", 2)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add3")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive2")

			turns.give()
		})

		Convey("an add arriving after the archive writer's turn still goes ahead of the "+
			"best-effort write, since no add has overtaken it", func() {
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			queue(writerNewJobs, "add", 2)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
		})
	})

	for _, writer := range []struct {
		kind        writerKind
		label, name string
	}{
		{writerChunkedAdd, "chunk", "a big add's chunk"},
		{writerLargeNewJobs, "large", "a large add"},
	} {
		Convey("Given a write turn in flight and "+writer.name+" and an archive waiting", t, func() {
			turns := &writeTurns{}
			turns.takeSince(writerArchive, time.Time{})

			order := make(chan string, 8)
			queue := noHoldWTTurnQueuer(t, turns, order)

			queue(writer.kind, writer.label, 1)
			queue(writerArchive, "archive", 2)

			Convey("its turn does not count as an add going ahead of the archive", func() {
				turns.give()
				So(noHoldWTNextTurn(t, order), ShouldEqual, writer.label)

				queue(writerNewJobs, "add", 2)
				turns.give()
				So(noHoldWTNextTurn(t, order), ShouldEqual, "add")

				turns.give()
				So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

				turns.give()
			})

			Convey("an add that went ahead of it waits for it, the oldest of the writers "+
				"it went ahead of, the next time", func() {
				queue(writerNewJobs, "first add", 3)
				turns.give()
				So(noHoldWTNextTurn(t, order), ShouldEqual, "first add")

				queue(writerNewJobs, "next add", 3)

				// the archive, which it also went ahead of, goes in between.
				labels := noHoldWTGiveTurns(t, turns, order, 3)
				So(labels[0], ShouldEqual, writer.label)
				So(labels[2], ShouldEqual, "next add")
			})
		})
	}

	Convey("Given a write turn in flight and two adds queued before an archive", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerArchive, time.Time{})

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)

		queue(writerNewJobs, "add1", 1)
		queue(writerNewJobs, "add2", 2)
		queue(writerArchive, "archive", 3)

		Convey("the first add goes next, then the archive, ahead of the second add", func() {
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			turns.give()
		})
	})
}

// TestNoHoldWriteTurnsKept drives writeTurns directly (items 6.4d and 6.4e of
// .docs/nohold/phase6.md). Its cases set up an archive or best-effort writer's
// turn in flight with a request queued behind it, and a best-effort write, a
// chunk, a small add or nobody waiting, and check which writer goes next, that
// a kept turn is used up once its writer takes it, and that each writer's keep
// is decided on its own queued requests.
func TestNoHoldWriteTurnsKept(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given an archive writer's turn in flight with an archive queued behind it", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerArchive, time.Time{})

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)
		queueSince := noHoldWTTurnQueuerSince(t, turns, order)

		Convey("a best-effort write that was waiting before that archive was queued goes "+
			"first", func() {
			queue(writerBestEffort, "best-effort", 1)
			turns.queued(writerArchive)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			queue(writerArchive, "archive", 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		Convey("with nobody waiting the archive writer keeps the turn, and a chunk asking "+
			"meanwhile waits for it", func() {
			turns.queued(writerArchive)
			turns.give()

			queue(writerChunkedAdd, "chunk", 1)

			oldest := turns.swapped(writerArchive)
			So(oldest.IsZero(), ShouldBeFalse)

			go func() {
				turns.takeSince(writerArchive, oldest)

				order <- "archive"
			}()

			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
		})

		Convey("a kept turn is used up once its writer takes it: a chunk asking after the "+
			"next hand-over waits for the chunk that has the turn", func() {
			turns.queued(writerArchive)
			turns.give()

			turns.takeSince(writerArchive, turns.swapped(writerArchive))

			queue(writerChunkedAdd, "chunk1", 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk1")

			queue(writerChunkedAdd, "chunk2", 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk2")

			turns.give()
		})

		Convey("a small add asking while the archive writer keeps the turn waits for that "+
			"writer's next transaction", func() {
			turns.queued(writerArchive)
			turns.give()

			queue(writerNewJobs, "add", 1)

			oldest := turns.swapped(writerArchive)

			go func() {
				turns.takeSince(writerArchive, oldest)

				order <- "archive"
			}()

			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add")

			turns.give()
		})

		Convey("an archive queued while its writer has not yet asked again counts only in that "+
			"writer's keep: once an add has gone, a chunk that asked after it, the only writer "+
			"waiting, goes before it", func() {
			turns.queued(writerArchive)
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerNewJobs, "add", 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			oldest := turns.swapped(writerArchive)
			So(oldest.IsZero(), ShouldBeFalse)

			queueSince(writerArchive, "archive", oldest, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})
	})

	Convey("Given a best-effort writer's turn in flight with a write queued behind it", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerBestEffort, time.Time{})
		turns.queued(writerBestEffort)

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)

		Convey("with nobody waiting the best-effort writer keeps the turn, and a chunk asking "+
			"meanwhile waits for it", func() {
			turns.give()

			queue(writerChunkedAdd, "chunk", 1)

			oldest := turns.swapped(writerBestEffort)
			So(oldest.IsZero(), ShouldBeFalse)

			go func() {
				turns.takeSince(writerBestEffort, oldest)

				order <- "best-effort"
			}()

			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
		})
	})

	Convey("Given a best-effort writer's turn in flight and an archive queued for the archive "+
		"writer", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerBestEffort, time.Time{})
		turns.queued(writerArchive)

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)

		Convey("the best-effort writer's keep is decided on its own queued requests: one queued "+
			"after a waiting chunk asked keeps no turn, though the archive queued is older than "+
			"the chunk's request", func() {
			queue(writerChunkedAdd, "chunk", 1)
			turns.queued(writerBestEffort)

			turns.give()

			go func() {
				turns.takeSince(writerBestEffort, turns.swapped(writerBestEffort))

				order <- "best-effort"
			}()

			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			noHoldWTAwaitTurnWaiters(t, turns, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
		})
	})
}

// TestNoHoldWriteTurnsCoalescingByAsking drives writeTurns directly, with no
// small add asking (item 6.4g of .docs/nohold/phase6.md). Its cases set up a
// big add's chunk, an archive writer's turn or a failing archive batch's first
// attempt in flight, with the archive and best-effort writers, chunks and
// large adds waiting with older, younger or exactly as old times, and check
// the order their turns go in and whether the writer whose turn ends keeps it,
// including at the end of each of a failing batch's attempts.
func TestNoHoldWriteTurnsCoalescingByAsking(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a big add's chunk in flight", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerChunkedAdd, time.Time{})

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)
		queueSince := noHoldWTTurnQueuerSince(t, turns, order)
		old := time.Now().Add(-time.Minute)

		Convey("with no add waiting, of the archive and best-effort writers the one that asked "+
			"first goes, though the other carries the older request", func() {
			queue(writerBestEffort, "best-effort", 1)
			queueSince(writerArchive, "archive", old, 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		Convey("with no add waiting, the archive and best-effort writers go ahead of a chunk "+
			"that asked before both, in the order they asked, when the one that asked last "+
			"carries a request older than the chunk's", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerBestEffort, "best-effort", 2)
			queueSince(writerArchive, "archive", old, 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
		})

		Convey("with no add waiting, the pair is judged by the older request either carries, "+
			"though the first to ask carries it: a chunk waits behind an archive that asked after "+
			"it with an older request, then goes ahead of a best-effort writer that asked last", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queueSince(writerArchive, "archive", old, 2)
			queue(writerBestEffort, "best-effort", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
		})

		Convey("with no add waiting, a coalescing writer carrying a request exactly as old as a "+
			"waiting chunk's goes ahead of it", func() {
			queueSince(writerChunkedAdd, "chunk", old, 1)
			queueSince(writerBestEffort, "best-effort", old, 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
		})

		Convey("of a waiting chunk and large add whose requests are exactly as old, the one "+
			"that asked first goes first", func() {
			queueSince(writerChunkedAdd, "chunk", old, 1)
			queueSince(writerLargeNewJobs, "large", old, 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "large")

			turns.give()
		})

		Convey("with no add waiting, a large add whose request is older than both of theirs goes "+
			"ahead of the archive and best-effort writers", func() {
			queueSince(writerLargeNewJobs, "large", old, 1)
			queue(writerBestEffort, "best-effort", 2)
			queueSince(writerArchive, "archive", old.Add(time.Second), 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "large")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		Convey("of a waiting large add and chunk, the one with the older request goes first", func() {
			queue(writerLargeNewJobs, "large", 1)
			queue(writerChunkedAdd, "chunk", 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "large")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
		})
	})

	Convey("Given an archive writer's turn in flight with an archive queued behind it", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerArchive, time.Time{})
		turns.queued(writerArchive)

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)
		queueSince := noHoldWTTurnQueuerSince(t, turns, order)

		Convey("a best-effort writer, the only writer waiting when the turn ends, goes next, "+
			"though the archive queued is older than its request, so the archive writer keeps no "+
			"turn", func() {
			queue(writerBestEffort, "best-effort", 1)

			turns.give()

			oldest := turns.swapped(writerArchive)

			go func() {
				turns.takeSince(writerArchive, oldest)

				order <- "archive"
			}()

			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			noHoldWTAwaitTurnWaiters(t, turns, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		takeArchiveTurn := func(oldest time.Time, label string) {
			go func() {
				turns.takeSince(writerArchive, oldest)

				order <- label
			}()
		}

		Convey("the archive writer keeps no turn for queued requests exactly as old as a waiting "+
			"chunk's: the chunk, the only writer waiting, goes next", func() {
			turns.mu.Lock()
			queuedAt := turns.queuedSince[writerArchive]
			turns.mu.Unlock()

			queueSince(writerChunkedAdd, "chunk", queuedAt, 1)

			turns.give()

			takeArchiveTurn(turns.swapped(writerArchive), "archive")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			noHoldWTAwaitTurnWaiters(t, turns, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		Convey("a best-effort writer chosen to go next keeps the archive writer from keeping its "+
			"turn though a chunk waits too: with only that chunk waiting besides it, a "+
			"best-effort writer whose request is younger than the archive queued but older than "+
			"the chunk's goes first", func() {
			beOldest := time.Now()

			queue(writerChunkedAdd, "chunk", 1)
			queueSince(writerBestEffort, "best-effort", beOldest, 2)

			turns.give()

			takeArchiveTurn(turns.swapped(writerArchive), "archive")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			noHoldWTAwaitTurnWaiters(t, turns, 2)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
		})

		Convey("with no add waiting, the archive writer keeps its turn while a best-effort "+
			"writer waits, when a chunk that asked after the archive was queued, and before the "+
			"best-effort writer asked, would go next", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerBestEffort, "best-effort", 2)

			turns.give()

			takeArchiveTurn(turns.swapped(writerArchive), "archive")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
		})

		Convey("a failing archive batch's retries keep the turn kept at the first attempt's end, "+
			"and the next batch takes it, while only a chunk and a best-effort writer wait", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerBestEffort, "best-effort", 2)

			turns.give()

			takeArchiveTurn(time.Time{}, "retry")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "retry")

			turns.give()

			takeArchiveTurn(time.Time{}, "solo re-run")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "solo re-run")

			turns.give()

			takeArchiveTurn(turns.swapped(writerArchive), "next batch")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "next batch")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
		})

		Convey("a turn kept at the end of a failing archive batch's first attempt with nobody "+
			"waiting is decided again at the retry's end: a best-effort writer, the only writer "+
			"to ask during the retry, goes before the solo re-run", func() {
			turns.give()

			takeArchiveTurn(time.Time{}, "retry")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "retry")

			queue(writerBestEffort, "best-effort", 1)

			turns.give()

			takeArchiveTurn(time.Time{}, "solo re-run")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			noHoldWTAwaitTurnWaiters(t, turns, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "solo re-run")

			turns.give()
		})
	})

	Convey("Given an archive writer's turn in flight with a chunk waiting", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerArchive, time.Time{})

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)
		queue(writerChunkedAdd, "chunk", 1)

		Convey("an archive queued after the chunk asked is younger than the chunk's request, so "+
			"the chunk goes when the turn ends, though a best-effort writer that asked later "+
			"waits too, the only other writer waiting", func() {
			turns.queued(writerArchive)
			queue(writerBestEffort, "best-effort", 2)

			turns.give()

			go func() {
				turns.takeSince(writerArchive, turns.swapped(writerArchive))

				order <- "archive"
			}()

			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			noHoldWTAwaitTurnWaiters(t, turns, 2)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})
	})

	Convey("Given a failing archive batch's first attempt in flight, carrying an old request, "+
		"with no archive queued since its writer took its queue", t, func() {
		turns := &writeTurns{}
		old := time.Now().Add(-time.Minute)
		turns.takeSince(writerArchive, old)

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)

		takeAttempt := func(label string) {
			go func() {
				turns.takeSince(writerArchive, old)

				order <- label
			}()
		}

		Convey("each chunk of one big add, the only writer waiting as an attempt ends, goes "+
			"before the next attempt, though the batch's request is older than every chunk's", func() {
			queue(writerChunkedAdd, "chunk 1", 1)

			turns.give()

			takeAttempt("retry")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk 1")

			noHoldWTAwaitTurnWaiters(t, turns, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "retry")

			queue(writerChunkedAdd, "chunk 2", 1)

			turns.give()

			takeAttempt("solo re-run")
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk 2")

			noHoldWTAwaitTurnWaiters(t, turns, 1)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "solo re-run")

			turns.give()
		})
	})
}

// TestNoHoldWriteTurnsSmallAddsByAsking drives writeTurns directly (items 6.4e
// and 6.4g of .docs/nohold/phase6.md). Its cases set up a big add's chunk in
// flight, an add that goes ahead of waiting chunks and archive and best-effort
// writers, and writers that ask after its turn, some with older times, and
// check which writers the next small add waits for and the order they go in.
func TestNoHoldWriteTurnsSmallAddsByAsking(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a big add's chunk in flight", t, func() {
		turns := &writeTurns{}
		turns.takeSince(writerChunkedAdd, time.Time{})

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)
		queueSince := noHoldWTTurnQueuerSince(t, turns, order)
		old := time.Now().Add(-time.Minute)

		Convey("with only an archive an add went ahead of and a best-effort writer that asked "+
			"after that add's turn waiting, the next add goes after the archive and before the "+
			"best-effort writer, though the best-effort writer carries an older request", func() {
			queue(writerArchive, "archive", 1)
			queue(writerNewJobs, "add1", 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queueSince(writerBestEffort, "best-effort", old, 2)
			queue(writerNewJobs, "add2", 3)

			So(noHoldWTGiveTurns(t, turns, order, 3), ShouldResemble, []string{"archive", "add2", "best-effort"})
		})

		Convey("with only a chunk an add went ahead of and an archive that asked after that "+
			"add's turn waiting, the next add goes after the chunk and before the archive, though "+
			"the archive carries an older request", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerNewJobs, "add1", 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queueSince(writerArchive, "archive", old, 2)
			queue(writerNewJobs, "add2", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		Convey("a best-effort writer asking after an add went ahead of a chunk, with the next "+
			"add waiting as that add's turn ends, goes after the chunk and that next add, though "+
			"its request is older than the chunk's", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerNewJobs, "add1", 2)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queueSince(writerBestEffort, "best-effort", old, 2)
			queue(writerNewJobs, "add2", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
		})

		Convey("an archive asking after an add went ahead of a chunk and a best-effort writer, "+
			"with the next add waiting as that add's turn ends, goes after both and that next "+
			"add, though its request is older than both of theirs", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queue(writerBestEffort, "best-effort", 2)
			queue(writerNewJobs, "add1", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queueSince(writerArchive, "archive", old, 3)
			queue(writerNewJobs, "add2", 4)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})

		Convey("of the archive and best-effort writers an add went ahead of, the one that asked "+
			"first goes first, though the other carries the older request", func() {
			queue(writerArchive, "archive", 1)
			queueSince(writerBestEffort, "best-effort", old, 2)
			queue(writerNewJobs, "add1", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queue(writerNewJobs, "add2", 3)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "best-effort")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			turns.give()
		})

		Convey("of a chunk and an archive an add went ahead of, the archive goes first when it "+
			"carries the older request, though it asked after the chunk", func() {
			queue(writerChunkedAdd, "chunk", 1)
			queueSince(writerArchive, "archive", old, 2)
			queue(writerNewJobs, "add1", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queue(writerNewJobs, "add2", 3)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add2")

			turns.give()
		})

		Convey("the archive and best-effort writers an add went ahead of both go before the next "+
			"add, in the order they asked", func() {
			turns.queued(writerArchive)
			queueSince(writerArchive, "archive", old, 1)
			queue(writerBestEffort, "best-effort", 2)
			queue(writerNewJobs, "add1", 3)

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add1")

			queue(writerNewJobs, "add2", 3)

			So(noHoldWTGiveTurns(t, turns, order, 3), ShouldResemble, []string{"archive", "best-effort", "add2"})
		})
	})
}

// noHoldWTTurnQueuer returns a func that starts a writer of the given kind
// taking a turn from turns, which sends its label to order once it has the
// turn, and waits until want writers are waiting for a turn.
func noHoldWTTurnQueuer(t *testing.T, turns *writeTurns, order chan string) func(writerKind, string, int) {
	t.Helper()

	since := noHoldWTTurnQueuerSince(t, turns, order)

	return func(kind writerKind, label string, want int) {
		since(kind, label, time.Time{}, want)
	}
}

// noHoldWTTurnQueuerSince is noHoldWTTurnQueuer for writers whose oldest
// request was queued at a given time (the zero time meaning now). The test
// holds the turn while it queues writers, so it fails at once if any writer is
// given a turn before want are waiting.
func noHoldWTTurnQueuerSince(t *testing.T, turns *writeTurns,
	order chan string) func(writerKind, string, time.Time, int) {
	t.Helper()

	return func(kind writerKind, label string, oldest time.Time, want int) {
		go func() {
			turns.takeSince(kind, oldest)

			order <- label
		}()

		deadline := time.Now().Add(reliable4ACWaitTimeout)

		for time.Now().Before(deadline) {
			turns.mu.Lock()
			n := len(turns.waiting)
			turns.mu.Unlock()

			if n == want {
				return
			}

			if len(order) > 0 {
				t.Fatalf("a writer was given a turn while %s was queued to wait for one", label)
			}

			time.Sleep(reliable4ACAddPoll)
		}

		t.Fatalf("%d writers never waited for a turn within %s", want, reliable4ACWaitTimeout)
	}
}

// noHoldWTGiveTurns ends the turn in flight n times, returning the labels of
// the writers given the turns in order, then ends the last of their turns.
func noHoldWTGiveTurns(t *testing.T, turns *writeTurns, order chan string, n int) []string {
	t.Helper()

	labels := make([]string, 0, n)

	for range n {
		turns.give()

		labels = append(labels, noHoldWTNextTurn(t, order))
	}

	turns.give()

	return labels
}

// noHoldWTNextTurn returns the label of the next writer given a turn.
func noHoldWTNextTurn(t *testing.T, order chan string) string {
	t.Helper()

	select {
	case label := <-order:
		return label
	case <-time.After(reliable4ACWaitTimeout):
		t.Fatalf("no writer got the turn within %s", reliable4ACWaitTimeout)
	}

	return ""
}
