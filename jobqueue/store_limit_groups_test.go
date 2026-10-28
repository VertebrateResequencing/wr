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
	"fmt"
	"slices"
	"testing"

	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
)

// addTxLimitGroupA and addTxLimitGroupB are the two limit groups the limit group
// tests store.
const (
	addTxLimitGroupA = "rl4lg-a"
	addTxLimitGroupB = "rl4lg-b"
)

// TestStoreLimitGroups proves what storeLimitGroups reports as changed and
// removed, and what it leaves stored, on every store kind.
// TestReliable4LimitGroupsNoWrite pins what each call costs bbolt.
func TestStoreLimitGroups(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store", storeKindName(kind)), t, func() {
			s, _ := openTestStore(t, ctx, kind)

			storeGroups := func(groups map[string]*limiter.GroupData) ([]string, []string) {
				changed, removed, err := s.storeLimitGroups(groups)
				So(err, ShouldBeNil)

				return slices.Sorted(slices.Values(changed)), removed
			}

			stored := func(group string) int64 {
				limits, err := s.retrieveStoredLimits([]string{group})
				So(err, ShouldBeNil)
				So(limits, ShouldHaveLength, 1)

				return limits[0]
			}

			Convey("storing no limit groups reports nothing", func() {
				for _, groups := range []map[string]*limiter.GroupData{{}, nil} {
					changed, removed := storeGroups(groups)
					So(changed, ShouldBeEmpty)
					So(removed, ShouldBeEmpty)
				}
			})

			Convey("storing new limit groups reports them changed and stores them", func() {
				changed, removed := storeGroups(limitGroupsAB(3, 0))
				So(changed, ShouldResemble, []string{addTxLimitGroupA, addTxLimitGroupB})
				So(removed, ShouldBeEmpty)
				So(stored(addTxLimitGroupA), ShouldEqual, 3)
				So(stored(addTxLimitGroupB), ShouldEqual, 0)
				So(s.retrieveLimitGroup(ctx, addTxLimitGroupA).Limit(), ShouldEqual, 3)

				Convey("storing the same limits again reports nothing", func() {
					changed, removed = storeGroups(limitGroupsAB(3, 0))
					So(changed, ShouldBeEmpty)
					So(removed, ShouldBeEmpty)
					So(stored(addTxLimitGroupA), ShouldEqual, 3)
					So(stored(addTxLimitGroupB), ShouldEqual, 0)
				})

				Convey("changing one limit reports only it changed", func() {
					changed, removed = storeGroups(limitGroupsAB(5, 0))
					So(changed, ShouldResemble, []string{addTxLimitGroupA})
					So(removed, ShouldBeEmpty)
					So(stored(addTxLimitGroupA), ShouldEqual, 5)
					So(stored(addTxLimitGroupB), ShouldEqual, 0)
				})

				Convey("removing one limit reports it removed and deletes only its record", func() {
					name, gd := limiter.NameToGroupData(addTxLimitGroupB + ":-1")
					So(name, ShouldEqual, addTxLimitGroupB)

					changed, removed = storeGroups(map[string]*limiter.GroupData{name: gd})
					So(changed, ShouldBeEmpty)
					So(removed, ShouldResemble, []string{addTxLimitGroupB})
					So(stored(addTxLimitGroupA), ShouldEqual, 3)
					So(stored(addTxLimitGroupB), ShouldBeLessThan, 0)
				})
			})

			Convey("a non-count group with no record is reported removed", func() {
				name, gd := limiter.NameToGroupData("storelg-nc:-1")
				So(gd.IsCount(), ShouldBeFalse)

				changed, removed := storeGroups(map[string]*limiter.GroupData{name: gd})
				So(changed, ShouldBeEmpty)
				So(removed, ShouldResemble, []string{"storelg-nc"})
			})
		})
	})
}

// limitGroupsAB returns the two limit groups the limit group tests store, with
// the given limits.
func limitGroupsAB(a, b int64) map[string]*limiter.GroupData {
	return map[string]*limiter.GroupData{
		addTxLimitGroupA: limiter.NewCountGroupData(a),
		addTxLimitGroupB: limiter.NewCountGroupData(b),
	}
}
