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

// Regression test for the reliable3 runner over-provisioning bug: several sibling
// scheduler groups that map to the SAME limit group must not each independently be
// granted the limit group's full remaining capacity in one rac cycle. On the
// production manager this caused up to 13,271 runners to be requested for a
// limit-2000 group (6.6x). The accounting lives in countJobInGroup /
// groupRemainingCapacity (server.go): the per-scheduler-group cache means each
// sibling sees the full remaining capacity. The fix shares one budget per limit
// group across siblings, so the summed request never exceeds the limit.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
)

// envInt reads a positive integer from an env var, falling back to def. Used so
// developers/wrdev.sh can run this same deterministic check at production scale
// (WR_OP_LIMIT / WR_OP_SIBLINGS / WR_OP_READY) without a running manager or LSF.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}

	return def
}

func TestReliable3LimitGroupOverProvision(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Sibling scheduler groups sharing one limit group share its capacity", t, func() {
		limit := envInt("WR_OP_LIMIT", 4)
		siblingGroups := envInt("WR_OP_SIBLINGS", 5)
		readyPerGroup := envInt("WR_OP_READY", 20)

		lim := limiter.New(func(_ context.Context, _ string) *limiter.GroupData {
			return nil
		})
		lim.SetLimit("lg", *limiter.NewCountGroupData(int64(limit)))

		s := &Server{limiter: lim}

		groups := make(map[string]*sgroup)
		groupLimits := make(map[string]int)
		req := &scheduler.Requirements{RAM: 100, Cores: 1, Disk: 1, Time: time.Minute}

		// build the sibling scheduler-group strings (distinct sched part, same
		// "~lg" limit-group suffix), then count many ready jobs in each, interleaved
		// as a real rac cycle would see them.
		grpNames := make([]string, siblingGroups)
		for g := range siblingGroups {
			grpNames[g] = fmt.Sprintf("%d:30:1:1:samehash", 100+g*100) + jobSchedLimitGroupSeparator + "lg"
		}

		for range readyPerGroup {
			for g := range siblingGroups {
				s.countJobInGroup(ctx, groups, groupLimits, schedulerGroupSnapshot{
					group:        grpNames[g],
					requirements: req,
					priority:     0,
				})
			}
		}

		total := 0
		for _, grp := range groups {
			total += grp.count
		}

		t.Logf("over-provision check: limit=%d siblingGroups=%d readyPerGroup=%d "+
			"=> summed runner request=%d (buggy per-group accounting would give ~%d)",
			limit, siblingGroups, readyPerGroup, total, siblingGroups*limit)

		Convey("the summed runner request across siblings does not exceed the limit", func() {
			So(total, ShouldBeLessThanOrEqualTo, limit)
		})
	})
}
