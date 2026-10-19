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

// Item 6.3e of .docs/nohold/phase6.md: the new-jobs, archive and best-effort
// writers take turns at bolt's single write lock, so a pending add commits
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

// noHoldWTTxLog records the bolt transaction id each observed write ran in.
type noHoldWTTxLog struct {
	mu  sync.Mutex
	ids map[string]int
}

func newNoHoldWTTxLog() *noHoldWTTxLog {
	return &noHoldWTTxLog{ids: make(map[string]int)}
}

func (l *noHoldWTTxLog) record(name string, id int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.ids[name]; !ok {
		l.ids[name] = id
	}
}

func (l *noHoldWTTxLog) get(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.ids[name]
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

			beSwapped := make(chan struct{})

			var beOnce sync.Once

			bestEffortSwappedHook = func() { beOnce.Do(func() { close(beSwapped) }) }

			defer func() { bestEffortSwappedHook = nil }()

			beDone := make(chan error, 1)

			go func() { beDone <- database.updateJobAfterChangeDurable(changed) }()

			select {
			case <-beSwapped:
			case <-time.After(reliable4ACWaitTimeout):
				t.Fatalf("the best-effort writer never took its write")
			}

			time.Sleep(reliable4ACAddSettle)

			addDone := noHoldWTAddAsync(database, noHoldWTRecordingPutter(txLog, "add", nil), "nohold-turns-add")
			reliable4ACAddAwaitPending(t, database, 0)
			time.Sleep(reliable4ACAddSettle)

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
