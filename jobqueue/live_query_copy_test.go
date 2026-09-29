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

// Regression tests for prodsim round 4: prefix, state and limited queries over
// the live queue made a client copy of every live job before filtering, so one
// `find_incomplete_prefix` at ~570k live jobs held 3GB of heap and a
// `wr status -b --limit 1` took over a minute. The queries must copy only the
// jobs they return, and must return exactly what they returned before.

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	liveQueryUnrelated  = 10000
	liveQueryPrefix     = "ibackup_fofn_"
	liveQueryMatching   = 5
	liveQueryBuriedEach = 100
	liveQueryBusyRG     = "busy"

	// liveQueryMaxAllocs is far below the 2 allocations per job a client copy
	// costs, so any query that copies the unrelated jobs exceeds it many times
	// over, while one that copies only what it returns stays well inside it.
	liveQueryMaxAllocs = 1000
)

func TestLiveQueriesCopyOnlyReturnedJobs(t *testing.T) {
	ctx := context.Background()

	Convey("Given a queue with many live jobs that a query does not want", t, func() {
		q := queue.New(ctx, "live-query-copy")

		defer func() {
			So(q.Destroy(), ShouldBeNil)
		}()

		s := &Server{q: q, rpl: newRGToKeys()}

		addErrs := 0

		add := func(rg string, i int, sq queue.SubQueue, failReason string) {
			job := &Job{
				Cmd:          fmt.Sprintf("echo %s %d", rg, i),
				Cwd:          "/tmp",
				RepGroup:     rg,
				ReqGroup:     "req",
				Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 1},
				FailReason:   failReason,
			}
			key := job.Key()

			if _, err := q.Add(ctx, key, "", job, 0, 0, time.Hour, sq); err != nil {
				addErrs++
			}

			s.rpl.Lock()
			s.rpl.Add(rg, key)
			s.rpl.Unlock()
		}

		for i := range liveQueryUnrelated {
			add(fmt.Sprintf("%s%d", liveQueryBusyRG, i%10), i, queue.SubQueueReady, "")
		}

		for i := range liveQueryMatching {
			add(fmt.Sprintf("%s%d", liveQueryPrefix, i), i, queue.SubQueueReady, "")
		}

		for i := range liveQueryBuriedEach {
			add(liveQueryBusyRG+"0", liveQueryUnrelated+i, queue.SubQueueBury, "fail-a")
			add(liveQueryBusyRG+"0", liveQueryUnrelated+liveQueryBuriedEach+i, queue.SubQueueBury, "fail-b")
		}

		So(addErrs, ShouldEqual, 0)

		Convey("a prefix query copies only its matching jobs and returns what it did before", func() {
			var jobs []*Job

			allocs := testing.AllocsPerRun(1, func() {
				jobs = s.getJobsCurrent(ctx, liveQueryPrefix, RepGroupMatchPrefix, 0, "", false, false, false)
			})

			So(liveQueryIdentities(jobs), ShouldResemble, liveQueryIdentities(liveQueryReference(ctx, s,
				func(j *Job) bool { return RepGroupMatches(j.RepGroup, liveQueryPrefix, RepGroupMatchPrefix) },
				limitJobsOptions{})))
			So(len(jobs), ShouldEqual, liveQueryMatching)
			t.Logf("%d allocations", int(allocs))
			So(allocs, ShouldBeLessThan, liveQueryMaxAllocs)
		})

		Convey("a limited state query copies only the jobs it returns, keeping Similar counts", func() {
			var jobs []*Job

			allocs := testing.AllocsPerRun(1, func() {
				jobs = s.getJobsCurrent(ctx, "", RepGroupMatchExact, 1, JobStateBuried, false, false, false)
			})

			So(liveQueryIdentities(jobs), ShouldResemble, liveQueryIdentities(liveQueryReference(ctx, s,
				func(*Job) bool { return true }, limitJobsOptions{Limit: 1, State: JobStateBuried})))
			So(len(jobs), ShouldEqual, 2)
			So(jobs[0].Similar+jobs[1].Similar, ShouldEqual, 2*liveQueryBuriedEach-2)
			t.Logf("%d allocations", int(allocs))
			So(allocs, ShouldBeLessThan, liveQueryMaxAllocs)
		})

		Convey("a limited query with an offset pages identically", func() {
			jobs := s.getJobsCurrent(ctx, "", RepGroupMatchExact, 2, JobStateBuried, false, false, false)

			So(liveQueryIdentities(jobs), ShouldResemble, liveQueryIdentities(liveQueryReference(ctx, s,
				func(*Job) bool { return true }, limitJobsOptions{Limit: 2, State: JobStateBuried})))

			paged, _, _ := s.getJobsByRepGroup(ctx, repGroupOptions{
				RepGroup:         liveQueryBusyRG + "0",
				Match:            RepGroupMatchExact,
				limitJobsOptions: limitJobsOptions{Limit: 1, Offset: 3},
			})
			So(liveQueryIdentities(paged), ShouldResemble, liveQueryIdentities(liveQueryReference(ctx, s,
				func(j *Job) bool { return j.RepGroup == liveQueryBusyRG+"0" }, limitJobsOptions{Limit: 1, Offset: 3})))
			So(len(paged), ShouldEqual, 3)
		})

		Convey("a limited state query on one busy report group copies only the jobs it returns", func() {
			opts := repGroupOptions{
				RepGroup:         liveQueryBusyRG + "0",
				Match:            RepGroupMatchExact,
				limitJobsOptions: limitJobsOptions{Limit: 1, State: JobStateBuried},
			}

			var jobs []*Job

			allocs := testing.AllocsPerRun(1, func() {
				jobs, _, _ = s.getJobsByRepGroup(ctx, opts)
			})

			So(liveQueryIdentities(jobs), ShouldResemble, liveQueryIdentities(liveQueryReference(ctx, s,
				func(j *Job) bool { return j.RepGroup == opts.RepGroup }, opts.limitJobsOptions)))
			So(len(jobs), ShouldEqual, 2)
			t.Logf("%d allocations", int(allocs))
			So(allocs, ShouldBeLessThan, liveQueryMaxAllocs)
		})
	})
}

// liveQueryIdentities summarises jobs order-independently, since the queue
// walks its items in map order.
func liveQueryIdentities(jobs []*Job) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, fmt.Sprintf("%s|%s|%s|%d", job.RepGroup, job.State, job.FailReason, job.Similar))
	}

	sort.Strings(ids)

	return ids
}

// liveQueryReference is the old algorithm: copy every live job, keep those in
// wanted report groups, then limit.
func liveQueryReference(ctx context.Context, s *Server, wanted func(*Job) bool, opts limitJobsOptions) []*Job {
	var jobs []*Job

	for _, item := range s.q.AllItems() {
		job := s.itemToJob(ctx, item, false, false)
		if wanted(job) {
			jobs = append(jobs, job)
		}
	}

	return s.limitJobs(ctx, jobs, opts)
}
