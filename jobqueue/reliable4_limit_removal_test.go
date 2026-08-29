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
	bolt "go.etcd.io/bbolt"
)

const (
	rl4rmGroup = "rl4rm-lg"
	rl4rmLimit = 3

	// rl4rmUnlimited is what the limiter reports as the remaining capacity of a
	// group it knows no limit for.
	rl4rmUnlimited = -1
)

// TestReliable4LimitGroupRemoval covers what `wr limit -g 'name:-1'` has to do
// on disk (.docs/bugfixes/260827-2.md item 10). Dropping the limit from memory
// is not enough: limiter.New(db.retrieveLimitGroup) vivifies a group from
// bucketLGs on demand, so a surviving record brings the limit back the next time
// anything asks about the group, and certainly after a restart.
func TestReliable4LimitGroupRemoval(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server with a limit group set through wr limit", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		So(rl4rmLimitCmd(d, rl4rmGroup+":"+strconv.Itoa(rl4rmLimit)),
			ShouldResemble, map[string]int{rl4rmGroup: rl4rmLimit})
		So(limitGroupRecorded(d, rl4rmGroup), ShouldBeTrue)
		So(limitGroupStored(ctx, d, rl4rmGroup), ShouldEqual, rl4rmLimit)

		Convey("A limit that was not removed survives a restart", func() {
			d.restart(ctx)

			So(rl4rmLimitCmd(d, rl4rmGroup), ShouldResemble, map[string]int{rl4rmGroup: rl4rmLimit})
			So(limitGroupRecorded(d, rl4rmGroup), ShouldBeTrue)
			So(d.server.limiter.GetRemainingCapacity(ctx, []string{rl4rmGroup}), ShouldEqual, rl4rmLimit)
		})

		Convey("Removing it with a limit of -1 deletes its record, and it does not come back", func() {
			So(rl4rmLimitCmd(d, rl4rmGroup+":-1"), ShouldBeEmpty)
			So(limitGroupRecorded(d, rl4rmGroup), ShouldBeFalse)
			So(d.server.db.retrieveLimitGroup(ctx, rl4rmGroup).IsValid(), ShouldBeFalse)

			d.restart(ctx)

			So(rl4rmLimitCmd(d, rl4rmGroup), ShouldBeEmpty)
			So(limitGroupRecorded(d, rl4rmGroup), ShouldBeFalse)
			So(d.server.limiter.GetRemainingCapacity(ctx, []string{rl4rmGroup}), ShouldEqual, rl4rmUnlimited)
		})
	})
}

// rl4rmLimitCmd does what `wr limit -g <group>` does, and then what `wr limit`
// with no options does: it asks the server about the group, which sets the
// group's limit if group carries a :n suffix and otherwise makes a fresh limiter
// vivify the group from the database, and returns the limits the server then
// reports as being in place.
func rl4rmLimitCmd(d *dgrServer, group string) map[string]int {
	jq := d.connect()

	defer disconnect(jq)

	_, err := jq.GetOrSetLimitGroup(group)
	So(err, ShouldBeNil)

	limits, err := jq.GetLimitGroups()
	So(err, ShouldBeNil)

	return limits
}

// limitGroupRecorded says whether bucketLGs holds a record for group. That
// record is what has to go for a removed limit not to be vivified back into
// memory.
func limitGroupRecorded(d *dgrServer, group string) bool {
	var recorded bool

	err := d.server.db.bolt.View(func(tx *bolt.Tx) error {
		recorded = tx.Bucket(bucketLGs).Get([]byte(group)) != nil

		return nil
	})
	So(err, ShouldBeNil)

	return recorded
}
