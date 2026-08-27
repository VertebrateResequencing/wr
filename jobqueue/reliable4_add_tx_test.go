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

// Transaction-cost regression tests for what one wr add pays in bolt write
// transactions (.docs/bugfixes/260827-2.md): its environment (item 1) and its
// limit groups (item 2). Every commit rewrites the freelist page and fsyncs
// twice, with no early-out for a transaction that changes nothing, and the add
// path waits for each of them in turn.
//
// The cost is measured with bbolt's meta transaction id, which it increments
// once per COMMITTED write transaction, so the difference across a call is an
// exact count of the write transactions it committed. DB.Stats().TxN cannot see
// this: bbolt increments it in beginTx, not beginRWTx, so it counts only READ
// transactions.

import (
	"context"
	"strconv"
	"testing"

	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestReliable4AddEnvNoRewrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		Convey("Storing an env for the first time costs one write transaction", func() {
			So(addTxStoreCost(ctx, d, []byte("reliable4-addtx-new-env")), ShouldEqual, 1)
		})

		Convey("Storing an env already in the db, evicted from the cache, costs none", func() {
			env := []byte("reliable4-addtx-repeat-env")

			envkey, err := d.server.db.storeEnv(env)
			So(err, ShouldBeNil)

			addTxEvictEnvCache(d, "repeat")
			So(d.server.db.envcache.Contains(envkey), ShouldBeFalse)

			So(addTxStoreCost(ctx, d, env), ShouldEqual, 0)
		})
	})
}

// addTxStoreCost returns how many write transactions storing env committed, and
// asserts that the env is retrievable from the database afterwards regardless:
// a store that commits nothing must still leave the env there to be read back.
// The cache is emptied of the env before the read, so retrieveEnv has to go to
// the database for it.
func addTxStoreCost(ctx context.Context, d *dgrServer, env []byte) int {
	before := addTxWriteTxID(d)

	envkey, err := d.server.db.storeEnv(env)
	So(err, ShouldBeNil)
	So(envkey, ShouldEqual, byteKey(env))

	cost := addTxWriteTxID(d) - before

	d.server.db.envcache.Remove(envkey)
	So(d.server.db.envcache.Contains(envkey), ShouldBeFalse)
	So(d.server.db.retrieveEnv(ctx, envkey), ShouldResemble, env)

	return cost
}

func TestReliable4LimitGroupsNoWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		Convey("Storing no limit groups costs no write transaction", func() {
			changed, removed, cost := limitGroupsStoreCost(d, map[string]*limiter.GroupData{})
			So(cost, ShouldEqual, 0)
			So(changed, ShouldBeEmpty)
			So(removed, ShouldBeEmpty)

			changed, removed, cost = limitGroupsStoreCost(d, nil)
			So(cost, ShouldEqual, 0)
			So(changed, ShouldBeEmpty)
			So(removed, ShouldBeEmpty)
		})

		Convey("Storing new limit groups costs a write transaction and stores them", func() {
			changed, removed, cost := limitGroupsStoreCost(d, limitGroupsAB(3, 0))
			So(cost, ShouldEqual, 1)
			So(changed, ShouldBeEmpty) // a group stored for the first time is not a change
			So(removed, ShouldBeEmpty)
			So(limitGroupStored(ctx, d, "rl4lg-a"), ShouldEqual, 3)
			So(limitGroupStored(ctx, d, "rl4lg-b"), ShouldEqual, 0)

			Convey("Storing the same limits again costs no write transaction", func() {
				changed, removed, cost = limitGroupsStoreCost(d, limitGroupsAB(3, 0))
				So(cost, ShouldEqual, 0)
				So(changed, ShouldBeEmpty)
				So(removed, ShouldBeEmpty)
				So(limitGroupStored(ctx, d, "rl4lg-a"), ShouldEqual, 3)
				So(limitGroupStored(ctx, d, "rl4lg-b"), ShouldEqual, 0)
			})

			Convey("Changing one limit costs a write transaction, and only it is reported changed", func() {
				changed, removed, cost = limitGroupsStoreCost(d, limitGroupsAB(3, 7))
				So(cost, ShouldEqual, 1)
				So(changed, ShouldResemble, []string{"rl4lg-b"})
				So(removed, ShouldBeEmpty)
				So(limitGroupStored(ctx, d, "rl4lg-a"), ShouldEqual, 3)
				So(limitGroupStored(ctx, d, "rl4lg-b"), ShouldEqual, 7)
			})
		})

		// a limit group that is not a count group - here the name:-1 a user gives
		// to drop a limit - is reported removed so its caller drops the in-memory
		// limit, and nothing is stored for it, so no write transaction is needed
		// to report that.
		Convey("Storing a non-count group reports it removed without a write transaction", func() {
			name, gd := limiter.NameToGroupData("rl4lg-nc:-1")
			So(name, ShouldEqual, "rl4lg-nc")
			So(gd.IsCount(), ShouldBeFalse)

			changed, removed, cost := limitGroupsStoreCost(d, map[string]*limiter.GroupData{name: gd})
			So(cost, ShouldEqual, 0)
			So(changed, ShouldBeEmpty)
			So(removed, ShouldResemble, []string{"rl4lg-nc"})
		})
	})
}

// limitGroupsStoreCost stores limitGroups, returning what storeLimitGroups
// reported as changed and as removed, and how many write transactions it
// committed.
func limitGroupsStoreCost(d *dgrServer, limitGroups map[string]*limiter.GroupData) ([]string, []string, int) {
	before := addTxWriteTxID(d)

	changed, removed, err := d.server.db.storeLimitGroups(limitGroups)
	So(err, ShouldBeNil)

	return changed, removed, addTxWriteTxID(d) - before
}

// addTxWriteTxID returns the database's current meta transaction id. A read
// transaction reports the id of the last committed write transaction, so the
// difference between two of these is how many write transactions committed in
// between.
func addTxWriteTxID(d *dgrServer) int {
	var id int

	err := d.server.db.bolt.View(func(tx *bolt.Tx) error {
		id = tx.ID()

		return nil
	})
	So(err, ShouldBeNil)

	return id
}

// addTxEvictEnvCache stores more distinct envs than the env cache can hold, so
// that nothing stored before the call is still cached. The label keeps one
// call's envs distinct from another's.
func addTxEvictEnvCache(d *dgrServer, label string) {
	for i := range envCacheSize + 5 {
		_, err := d.server.db.storeEnv([]byte("reliable4-addtx-filler-" + label + "-" + strconv.Itoa(i)))
		So(err, ShouldBeNil)
	}
}

// limitGroupsAB returns the two limit groups the limit group test stores, with
// the given limits.
func limitGroupsAB(a, b int64) map[string]*limiter.GroupData {
	return map[string]*limiter.GroupData{
		"rl4lg-a": limiter.NewCountGroupData(a),
		"rl4lg-b": limiter.NewCountGroupData(b),
	}
}

// limitGroupStored returns the limit the database holds for group, so that a
// store which commits nothing can still be held to leaving the right value
// there. Asserts that a limit is stored for the group at all.
func limitGroupStored(ctx context.Context, d *dgrServer, group string) int64 {
	gd := d.server.db.retrieveLimitGroup(ctx, group)
	So(gd.IsCount(), ShouldBeTrue)

	return gd.Limit()
}
