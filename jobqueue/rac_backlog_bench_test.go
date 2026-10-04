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
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
)

const (
	// racBacklogJobs is the ready backlog BenchmarkBuildSchedulerGroupsBacklog
	// cycles over: the scale at which soak9 measured the cycle at about 11% of
	// a core per 100k ready jobs.
	racBacklogJobs = 200000

	// racBacklogLimitGroups is how many limit groups, and so scheduler groups,
	// the backlog is spread over.
	racBacklogLimitGroups = 10
)

// BenchmarkBuildSchedulerGroupsBacklog records the per-rac-cycle cost of
// buildSchedulerGroups over a large, unchanging, limit-blocked ready backlog,
// which is what a live manager with a long queue pays on every ready-added
// callback. One op is one steady-state cycle over racBacklogJobs jobs.
//
// The jobs are allocated among other allocations of varied size and visited in
// an order unrelated to allocation order, grouped by scheduler group as the
// queue hands them over, so the cycle meets them scattered over the heap as a
// live manager does rather than packed in allocation order. The priorities=1
// variant gives every job the same priority, priorities=4 spreads them over 4.
// It only calls functions an older tree also has, so it can be copied into one
// to compare versions.
func BenchmarkBuildSchedulerGroupsBacklog(b *testing.B) {
	ctx := context.Background()
	s, q, jobs := racBacklogFixture(b)
	data := racBacklogData(jobs)

	defer func() {
		if err := q.Destroy(); err != nil {
			b.Fatal(err)
		}
	}()

	for _, priorities := range []int{1, 4} {
		racBacklogSetPriorities(jobs, priorities)

		b.Run(fmt.Sprintf("priorities=%d", priorities), func(b *testing.B) {
			b.ReportAllocs()

			for range b.N {
				racBacklogCycle(ctx, b, s, q, data)
			}
		})
	}
}

// racBacklogFixture returns a server whose every limit group has a limit of 0,
// the queue its cycles use, and racBacklogJobs jobs blocked by those limit
// groups, after one cold cycle over them, in the order a cycle visits them.
func racBacklogFixture(b *testing.B) (*Server, *queue.Queue, []*Job) {
	b.Helper()

	ctx := context.Background()
	q := queue.New(ctx, "rac-backlog-bench")
	s := &Server{
		q: q,
		limiter: limiter.New(func(context.Context, string) *limiter.GroupData {
			return limiter.NewCountGroupData(0)
		}),
	}

	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec
	jobs := make([]*Job, 0, racBacklogJobs)
	others := make([][]byte, 0, racBacklogJobs)

	for i := range racBacklogJobs {
		jobs = append(jobs, &Job{
			Cmd:      fmt.Sprintf("echo rac-backlog %d", i),
			Cwd:      testCwd,
			ReqGroup: "rac-backlog",
			RepGroup: "rac-backlog",
			Requirements: &jqs.Requirements{
				RAM: 1, Time: time.Minute, Cores: 1,
				Other: map[string]string{"scheduler_queues_avoid": "interactive,inference"},
			},
			LimitGroups: []string{fmt.Sprintf("rac-backlog-lg%d", i%racBacklogLimitGroups)},
		})

		others = append(others, make([]byte, 64+rng.IntN(512)))
	}

	rng.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })

	// the cold cycle derives every job's memo and sets its scheduler group
	racBacklogCycle(ctx, b, s, q, racBacklogData(jobs))

	slices.SortStableFunc(jobs, func(a, b *Job) int {
		return cmp.Compare(a.getSchedulerGroup(), b.getSchedulerGroup())
	})

	b.Cleanup(func() { others = nil })

	return s, q, jobs
}

// racBacklogSetPriorities spreads the jobs' priorities over the given number of
// values, in a pattern unrelated to their scheduler groups.
func racBacklogSetPriorities(jobs []*Job, priorities int) {
	for i, job := range jobs {
		job.Lock()
		job.Priority = uint8((i / 7) % priorities) //nolint:gosec
		job.Unlock()
	}
}

// racBacklogCycle runs one rac cycle's buildSchedulerGroups over data, failing
// unless it finds every scheduler group.
func racBacklogCycle(ctx context.Context, b *testing.B, s *Server, q *queue.Queue, data []any) {
	b.Helper()

	if groups := s.buildSchedulerGroups(ctx, q, data, racLimitRunnerCmd); len(groups) != racBacklogLimitGroups {
		b.Fatalf("%d scheduler groups, not %d", len(groups), racBacklogLimitGroups)
	}
}

// racBacklogData returns the jobs as the ready item data a cycle is given.
func racBacklogData(jobs []*Job) []any {
	data := make([]any, len(jobs))
	for i, job := range jobs {
		data[i] = job
	}

	return data
}
