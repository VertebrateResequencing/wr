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
	"context"
	"strconv"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// This file holds the dep-granularity tests that count bbolt transactions, so
// are deleted with bbolt (spec F5).

// TestDepGranularityAddTransactionCost covers D1 acceptance test 2, a regression
// guard: what one more member costs in bolt transactions does not scale with the
// group's membership.
func TestDepGranularityAddTransactionCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given the same add against a small and a large dep group", t, func() {
		small := dgaAddMemberTxCost(ctx, dgaMembers)
		big := dgaAddMemberTxCost(ctx, dgaBigMembers)

		Convey("The larger group's add costs no more transactions", func() {
			So(small, ShouldBeGreaterThan, 0)
			So(float64(big), ShouldBeLessThanOrEqualTo, dgaTxRatio*float64(small))
		})
	})
}

// dgaAddMemberTxCost builds a dep group of the given size with dgaWaiters
// waiters on it, then returns how many bolt read transactions dgaTxAdds further
// one-member adds cost.
func dgaAddMemberTxCost(ctx context.Context, members int) int {
	d := dgrStartServer(ctx)

	defer d.stop(ctx)

	jq := d.connect()

	defer disconnect(jq)

	So(dgrAddJobs(jq, dgaMemberJobs(d, dgaGroup, members)), ShouldHaveLength, members)
	So(dgrAddJobs(jq, dgaWaiterJobs(d, dgaGroup, dgaWaiters)), ShouldHaveLength, dgaWaiters)

	// pausing stops the ready-added callbacks, not adds, and waiting for any
	// callback already running leaves only the adds reading the database
	paused, err := d.server.Pause()
	So(err, ShouldBeNil)
	So(paused, ShouldBeTrue)
	So(pollUntil(d.server.racIdle), ShouldBeTrue)

	before := d.server.boltDB().bolt.Stats().TxN
	added := 0

	for i := range dgaTxAdds {
		inserts, _, err := jq.Add([]*Job{dgaMemberJob(d, dgaGroup, dgaExtraName+strconv.Itoa(i))}, envVars, true)
		So(err, ShouldBeNil)

		added += inserts
	}

	txns := d.server.boltDB().bolt.Stats().TxN - before

	So(added, ShouldEqual, dgaTxAdds)

	return txns
}

// TestDepGranularityRecoveryPassTransactions covers C1 acceptance test 7: the
// recovery dependency pass costs one bolt read transaction per chunk of jobs -
// not one for the whole pass, which would hold bolt's mmaplock for a whole
// recovery, and not one per job, which is the bug it replaced.
func TestDepGranularityRecoveryPassTransactions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database of recovered chain jobs and a small chunk size", t, func() {
		testDB, server, priorJobs := dgrResolutionFixture(t, ctx, dgrChainDBJobs(dgrPassJobs, 1, dgrNeverSeenGroup))
		So(priorJobs, ShouldHaveLength, dgrPassJobs)

		defer setDependencyResolutionChunkSize(dgrPassChunkSize)()

		Convey("The pass costs exactly one read transaction per chunk", func() {
			before := testDB.bolt.Stats().TxN

			resolved, err := testDB.resolveDependencies(ctx, priorJobs, server.depGroups)
			So(err, ShouldBeNil)
			So(resolved, ShouldHaveLength, dgrPassJobs)

			So(testDB.bolt.Stats().TxN-before, ShouldEqual, dgrPassChunks)
			So(dgrPassChunks, ShouldBeGreaterThan, 1)
			So(dgrPassChunks, ShouldBeLessThan, dgrPassJobs)
		})
	})
}
