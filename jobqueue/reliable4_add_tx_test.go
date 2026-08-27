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

// Transaction-cost regression tests for what a wr add pays to store its
// environment (.docs/bugfixes/260827-2.md). handleAdd calls storeEnv
// sequentially before createJobs, and a cache miss used to mean a full bolt
// write transaction - which rewrites the freelist and fsyncs twice, with no
// early-out for a Put of identical bytes - even though the ordinary production
// case is that bucketEnvs already holds those exact bytes under that key: the
// production database holds 549 distinct envs against a 12-entry cache.
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
