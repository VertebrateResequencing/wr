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

// This file holds the learned-stat cache test that holds a bbolt write
// transaction open, so is deleted with bbolt (spec F5).

import (
	"context"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// reqStatLoadHangTimeout bounds the wait for a learned-stat load made while a
// write transaction is open. It is a hang detector: the load reads committed
// state and takes no write lock, so it returns in well under a millisecond
// unless it wrongly waits on the open transaction, which it never would see end.
const reqStatLoadHangTimeout = 10 * time.Second

// TestReqStatCacheUncommittedStat covers spec B1's commit rule: a load that
// runs while a new stat key's write is uncommitted is not current once it
// commits.
func TestReqStatCacheUncommittedStat(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server with no learned stats, a load during an uncommitted new stat key is not current once "+
		"it commits", t, func() {
		countReqStatLoads()

		db := newReqStatTest(ctx, t).server.boltDB()

		db.refreshReqGroupRecs(reqStatTestGroup)

		err := db.bolt.Update(func(tx *bolt.Tx) error {
			if err := db.reqStats.putStat(tx, bucketJobRAM, reqStatTestGroup, 300); err != nil {
				return err
			}

			done := make(chan *jqs.Requirements, 1)

			go func() { done <- db.refreshReqGroupRecs(reqStatTestGroup) }()

			select {
			case recs := <-done:
				So(recs.RAM, ShouldEqual, 0)
			case <-time.After(reqStatLoadHangTimeout):
				So("the load blocked on the open write transaction", ShouldBeBlank)
			}

			return nil
		})
		So(err, ShouldBeNil)

		_, current := db.cachedReqGroupRecs(reqStatTestGroup)
		So(current, ShouldBeFalse)
		So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)
	})
}
