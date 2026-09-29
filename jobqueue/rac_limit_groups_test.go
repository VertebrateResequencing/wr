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

// Regression test for prodsim round 4: every ready-added callback walks the
// whole ready backlog, and it parsed each job's limit groups out of its
// scheduler group string again for every job, allocating twice per job per
// cycle. The ready backlog shares a handful of scheduler groups, so each is now
// parsed once per cycle.

import (
	"context"
	"fmt"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	racLimitReady       = 10000
	racLimitGroups      = 10
	racLimitMaxAllocs   = 1000
	racLimitRunnerCmd   = "runner %s"
	racLimitGroupPrefix = "rac-lg"
)

func TestRACParsesLimitGroupsOncePerGroup(t *testing.T) {
	ctx := context.Background()

	Convey("Given a large ready backlog blocked by its limit groups", t, func() {
		q := queue.New(ctx, "rac-limit-groups")

		defer func() {
			So(q.Destroy(), ShouldBeNil)
		}()

		s := &Server{
			q: q,
			limiter: limiter.New(func(context.Context, string) *limiter.GroupData {
				return limiter.NewCountGroupData(0)
			}),
		}

		data := make([]any, 0, racLimitReady)
		addErrs := 0

		for i := range racLimitReady {
			job := &Job{
				Cmd: fmt.Sprintf("echo %d", i), Cwd: testCwd, RepGroup: "rac", ReqGroup: "rac",
				LimitGroups:  []string{fmt.Sprintf("%s%d", racLimitGroupPrefix, i%racLimitGroups)},
				Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 1},
			}

			if _, err := q.Add(ctx, job.Key(), "", job, 0, 0, time.Hour, queue.SubQueueReady); err != nil {
				addErrs++
			}

			data = append(data, job)
		}

		So(addErrs, ShouldEqual, 0)

		// the first cycle gives every job its reserve group; later ones do not
		s.buildSchedulerGroups(ctx, q, data, racLimitRunnerCmd)

		Convey("a later cycle skips them all without allocating per job", func() {
			var groups map[string]*sgroup

			allocs := testing.AllocsPerRun(1, func() {
				groups = s.buildSchedulerGroups(ctx, q, data, racLimitRunnerCmd)
			})

			skipped, counted := 0, 0
			for _, g := range groups {
				skipped += g.skipped
				counted += g.count
			}

			So(len(groups), ShouldEqual, racLimitGroups)
			So(skipped, ShouldEqual, racLimitReady)
			So(counted, ShouldEqual, 0)

			t.Logf("%d allocations", int(allocs))
			So(allocs, ShouldBeLessThan, racLimitMaxAllocs)
		})
	})
}
