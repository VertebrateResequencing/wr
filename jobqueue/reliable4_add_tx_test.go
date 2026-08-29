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
// transactions (.docs/bugfixes/260827-2.md): its environment (item 1), its
// limit groups (item 2), the job and lookup data itself (item 3), and
// storeBatched's empty final batch (item 4). Every commit rewrites the freelist
// page and fsyncs twice, with no early-out for a transaction that changes
// nothing, and the add path waits for each of them in turn.
//
// The cost is measured with bbolt's meta transaction id, which it increments
// once per COMMITTED write transaction, so the difference across a call is an
// exact count of the write transactions it committed. DB.Stats().TxN cannot see
// this: bbolt increments it in beginTx, not beginRWTx, so it counts only READ
// transactions.

import (
	"bytes"
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	addTxRepGroup = "rl4tx-rg"
	addTxDepGroup = "rl4tx-dg"
	addTxWaitedOn = "rl4tx-dep"
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

// TestReliable4EnvMissNotCached guards the env cache against memoising an
// absence. retrieve() returns a nil value for a key the database does not hold,
// and caching that nil under the key makes envcache.Contains report true - which
// is precisely what storeEnv takes as proof that those bytes are already stored.
// A cached miss would therefore make storeEnv of those exact bytes a no-op
// forever, leaving jobs pointing at an EnvKey with no record behind it
// (.docs/bugfixes/260827-2.md item 8).
func TestReliable4EnvMissNotCached(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		Convey("Retrieving an env that was never stored leaves it storable", func() {
			env := []byte("reliable4-addtx-absent-env")
			envkey := byteKey(env)

			So(d.server.db.retrieveEnv(ctx, envkey), ShouldBeEmpty)
			So(d.server.db.envcache.Contains(envkey), ShouldBeFalse)

			So(addTxStoreCost(ctx, d, env), ShouldEqual, 1)
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

func TestReliable4AddOneWriteTx(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server where every remaining Batch call commits alone", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		// the add's own bucket writes ride in the coalescing newJobsWriter's
		// db.bolt.Update, which no bbolt batching knob reaches, so the one
		// transaction counted below is that fold and nothing else: storeEnv and
		// storeLimitGroups contribute none. MaxBatchSize 1 is what makes that
		// attributable, by keeping the db.bolt.Batch callers the add path still
		// reaches (storeEnv via db.store, and storeLimitGroups) from hiding
		// inside a batch shared with a concurrent Batch call elsewhere in the
		// server: a transaction either of them committed is then counted here
		// instead of being coalesced away.
		d.server.db.bolt.MaxBatchSize = 1

		// the first add of all pays for storing the client's environment, which
		// later adds find cached
		_, _, err := jq.Add([]*Job{d.job("echo rl4tx warm", "rl4tx-warm")}, envVars, true)
		So(err, ShouldBeNil)

		Convey("An add costs one write transaction, and all its data is stored", func() {
			job := d.job("echo rl4tx one", addTxRepGroup)
			job.DepGroups = []string{addTxDepGroup}
			job.Dependencies = Dependencies{NewDepGroupDependency(addTxWaitedOn)}

			before := addTxWriteTxID(d)

			added, _, errA := jq.Add([]*Job{job}, envVars, true)
			So(errA, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(addTxWriteTxID(d)-before, ShouldEqual, 1)

			live, errL := d.server.db.checkIfLive(job.Key())
			So(errL, ShouldBeNil)
			So(live, ShouldBeTrue)

			rgs, errR := d.server.db.retrieveRepGroups()
			So(errR, ShouldBeNil)
			So(rgs, ShouldContain, addTxRepGroup)

			seen, errD := d.server.db.depGroupEverSeen(addTxDepGroup)
			So(errD, ShouldBeNil)
			So(seen, ShouldBeTrue)

			// the job waits on addTxWaitedOn, so a later add of a member of that
			// dep group has to find it through the reverse lookup
			_, jobsToUpdate, errW := d.server.db.retrieveDependentJobs(
				map[string]bool{addTxWaitedOn: true}, nil)
			So(errW, ShouldBeNil)
			So(len(jobsToUpdate), ShouldEqual, 1)
			So(jobsToUpdate[0].Key(), ShouldEqual, job.Key())

			// and the rep group lookup the completed-job queries use is there
			So(addTxRepGroupLookupKeys(d, addTxRepGroup), ShouldResemble, []string{job.Key()})
		})
	})
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

// addTxRepGroupLookupKeys returns the job keys bucketRTK holds for the given rep
// group, which is how the completed-job queries find a rep group's jobs.
func addTxRepGroupLookupKeys(d *dgrServer, repGroup string) []string {
	var keys []string

	err := d.server.db.bolt.View(func(tx *bolt.Tx) error {
		prefix := []byte(repGroup + dbDelimiter)

		cursor := tx.Bucket(bucketRTK).Cursor()
		for k, _ := cursor.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
			keys = append(keys, string(bytes.TrimPrefix(k, prefix)))
		}

		return nil
	})
	So(err, ShouldBeNil)

	return keys
}

func TestReliable4StoreBatchedNoEmptyBatch(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		Convey("storeBatched stores every item without an empty final batch", func() {
			for _, tc := range []struct {
				num   int
				calls int
			}{
				{999, 1},
				{1000, 1},
				{1001, 2},
				{2000, 2},
			} {
				calls, empty, stored := storeBatchedCost(d, tc.num)
				So(empty, ShouldEqual, 0)
				So(calls, ShouldEqual, tc.calls)
				So(stored, ShouldEqual, tc.num)
			}
		})
	})
}

// storeBatchedCost stores num lookups through storeBatched, returning how many
// storer calls it made, how many of those had nothing to store (each of which
// would be a committed, empty write transaction), and how many of the lookups
// can be read back afterwards.
func storeBatchedCost(d *dgrServer, num int) (calls, empty, stored int) {
	prefix := "rl4sb-" + strconv.Itoa(num) + "-"

	data := make(sobsd, num)
	for i := range data {
		data[i] = [2][]byte{[]byte(prefix + strconv.Itoa(i)), nil}
	}

	sort.Sort(data)

	err := d.server.db.storeBatched(bucketRGs, data, func(bucket []byte, chunk sobsd) error {
		calls++

		if len(chunk) == 0 {
			empty++
		}

		return d.server.db.storeLookups(bucket, chunk)
	})
	So(err, ShouldBeNil)

	rgs, err := d.server.db.retrieveRepGroups()
	So(err, ShouldBeNil)

	for _, rg := range rgs {
		if strings.HasPrefix(rg, prefix) {
			stored++
		}
	}

	return calls, empty, stored
}
