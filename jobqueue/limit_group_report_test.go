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

const (
	lgrGroup = "lgr-lg"
	lgrLimit = 5

	// lgrNoLimit is what a client has to be told about a group that puts no
	// limit on how many of its jobs run at once, as cmd/limit.go's help
	// promises: "Groups that are not known about will report -1".
	lgrNoLimit = -1
)

// TestLimitGroupReport covers what `wr limit -g <group>` tells the user a
// group's limit is (.docs/bugfixes/260917-limit-group.md). A group nothing
// knows a limit for, and a group whose limit was removed with :-1, are the same
// unlimited group data internally, and both have to reach the client as -1
// rather than as the MaxInt64 that limiter.GroupData.Limit() saturates them to
// for outside consumers of that exported API.
func TestLimitGroupReport(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		Convey("A group nothing knows a limit for reports no limit", func() {
			So(lgrLimitCmd(jq, "lgr-unknown"), ShouldEqual, lgrNoLimit)
		})

		Convey("A group given a limit reports that limit", func() {
			So(lgrLimitCmd(jq, lgrGroup+":"+strconv.Itoa(lgrLimit)), ShouldEqual, lgrLimit)
			So(lgrLimitCmd(jq, lgrGroup), ShouldEqual, lgrLimit)

			Convey("A limit of 0 is reported as itself, not as no limit", func() {
				So(lgrLimitCmd(jq, lgrGroup+":0"), ShouldEqual, 0)
				So(lgrLimitCmd(jq, lgrGroup), ShouldEqual, 0)
			})

			Convey("Removing the limit with :-1 reports no limit, then and after", func() {
				So(lgrLimitCmd(jq, lgrGroup+":-1"), ShouldEqual, lgrNoLimit)
				So(lgrLimitCmd(jq, lgrGroup), ShouldEqual, lgrNoLimit)
			})
		})

		// a time-limited group has no count limit, which is also why `wr limit`
		// with no options leaves such groups out of its listing.
		Convey("A group limited by time reports no limit on its count", func() {
			So(lgrLimitCmd(jq, "00:00:01 < time"), ShouldEqual, lgrNoLimit)
		})
	})
}

// lgrLimitCmd does what `wr limit -g <group>` does: it asks the server for the
// group's limit, having first set that limit if group carries a :n suffix.
func lgrLimitCmd(jq *Client, group string) int {
	limit, err := jq.GetOrSetLimitGroup(group)
	So(err, ShouldBeNil)

	return limit
}
