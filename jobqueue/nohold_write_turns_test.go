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

// Items 6.3e and 6.4b of .docs/nohold/phase6.md: the new-jobs, archive and
// best-effort writers, and each chunk of an add too big for one transaction,
// take turns at bolt's single write lock, so a pending add commits within a
// bounded number of other transactions, and archives still get their turns
// while adds keep arriving.
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
)

// noHoldWTQueuedAdds is how many adds, each needing a transaction of its own,
// the starvation test has queued behind the one in flight before an archive
// arrives.
const noHoldWTQueuedAdds = 8

// noHoldWTChunkedChunks is how many write transactions the big add of the
// chunked-add tests needs.
const noHoldWTChunkedChunks = 2

// errNoHoldWTChunk is what a big add's failing putter returns.
var errNoHoldWTChunk = errors.New("nohold write turns chunk failure")

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

		Convey("an add commits in the transaction right after the archive in flight, ahead of "+
			"best-effort and archive writes queued before it", func() {
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

		Convey("a best-effort write queued before a big add's first transaction commits ahead "+
			"of it, so a big add's transactions wait their turn too", func() {
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

		Convey("a big add's first transaction arriving while no add waits commits after a "+
			"best-effort write queued before it", func() {
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

		Convey("a best-effort write queued while a big add's first transaction is in flight "+
			"commits between that transaction and the add's next one", func() {
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
				recovered = database.updateInTurn(writerBestEffort, func(*bolt.Tx) error {
					panic("nohold write turns panic")
				})
			}()

			So(recovered, ShouldNotBeNil)

			done := make(chan error, 1)

			go func() { done <- database.updateInTurn(writerArchive, func(*bolt.Tx) error { return nil }) }()

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

			database.turns.take(writerArchive)
			defer database.turns.give()

			done := make(chan error, 1)

			go func() { done <- database.deleteLiveJobs(ctx, []string{job.Key()}) }()

			So(reliable4ACAwait(t, done), ShouldBeNil)
		})

		Convey("an archive commits after at most one more add transaction while adds stay "+
			"queued", func() {
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

// TestNoHoldWriteTurnsAddOvertakesOnce drives writeTurns directly: an add that
// keeps arriving goes ahead of each other waiting writer at most once (item
// 6.4b), so a best-effort write is not left behind one add per turn of every
// other writer.
func TestNoHoldWriteTurnsAddOvertakesOnce(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a write turn in flight and an archive and a best-effort write waiting", t, func() {
		turns := &writeTurns{}
		turns.take(writerArchive)

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

	Convey("Given a write turn in flight and a big add's chunk and an archive waiting", t, func() {
		turns := &writeTurns{}
		turns.take(writerArchive)

		order := make(chan string, 8)
		queue := noHoldWTTurnQueuer(t, turns, order)

		queue(writerChunkedAdd, "chunk", 1)
		queue(writerArchive, "archive", 2)

		Convey("the chunk's turn does not count as an add going ahead of the archive", func() {
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "chunk")

			queue(writerNewJobs, "add", 2)
			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "add")

			turns.give()
			So(noHoldWTNextTurn(t, order), ShouldEqual, "archive")

			turns.give()
		})
	})

	Convey("Given a write turn in flight and two adds queued before an archive", t, func() {
		turns := &writeTurns{}
		turns.take(writerArchive)

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

// noHoldWTTurnQueuer returns a func that starts a writer of the given kind
// taking a turn from turns, which sends its label to order once it has the
// turn, and waits until want writers are waiting for a turn.
func noHoldWTTurnQueuer(t *testing.T, turns *writeTurns, order chan string) func(writerKind, string, int) {
	t.Helper()

	return func(kind writerKind, label string, want int) {
		go func() {
			turns.take(kind)

			order <- label
		}()

		noHoldWTAwaitTurnWaiters(t, turns, want)
	}
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
