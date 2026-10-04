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

// Tests that a rac cycle's per-scheduler-group bookkeeping, which it keeps for
// the cycle so that the ready backlog's jobs need not each look up their limit
// groups' budgets, hands out each limit group's shared budget exactly as
// counting job by job would, and that the cycle considers jobs in
// highest-priority-first order, keeping the order they came in within a priority.

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
)

// racGroupCounts is a scheduler group's count and skipped after a rac cycle.
type racGroupCounts struct {
	count, skipped int
}

// racCountsOf returns the count and skipped of the scheduler group of job.
func racCountsOf(groups map[string]*sgroup, job *Job) racGroupCounts {
	group, found := groups[racGroupOf(job)]
	if !found {
		return racGroupCounts{}
	}

	return racGroupCounts{count: group.count, skipped: group.skipped}
}

func TestRACSharedBudgetsAcrossInterleavedGroups(t *testing.T) {
	ctx := context.Background()

	Convey("Jobs of scheduler groups sharing limit groups, interleaved, share each limit group's budget", t, func() {
		const lgA, lgB, lgC = "lgA", "lgB", "lgC"

		s := newRACLimitsServer(map[string]int{lgA: 3, lgB: 2})

		// lgC has no limit
		g1 := racReadyJobs("g1", 100, 0, []string{lgA}, 4)
		g2 := racReadyJobs("g2", 200, 0, []string{lgA, lgB}, 4)
		g3 := racReadyJobs("g3", 300, 0, []string{lgB, lgC}, 4)
		g4 := racReadyJobs("g4", 400, 0, nil, 4)

		jobs := make([]*Job, 0, 16)
		for i := range 4 {
			jobs = append(jobs, g1[i], g2[i], g3[i], g4[i])
		}

		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		// g1 takes lgA 3->2, g2 lgA 2->1 and lgB 2->1, g3 lgB 1->0, g4 is
		// unlimited, g1 takes lgA 1->0; from then on only g4 is counted.
		So(racCountsOf(groups, g1[0]), ShouldResemble, racGroupCounts{count: 2, skipped: 2})
		So(racCountsOf(groups, g2[0]), ShouldResemble, racGroupCounts{count: 1, skipped: 3})
		So(racCountsOf(groups, g3[0]), ShouldResemble, racGroupCounts{count: 1, skipped: 3})
		So(racCountsOf(groups, g4[0]), ShouldResemble, racGroupCounts{count: 4, skipped: 0})
	})

	Convey("Random interleavings get the counts of a job-by-job budget", t, func() {
		rng := rand.New(rand.NewPCG(7, 11)) //nolint:gosec
		mismatches := 0

		for round := range 200 {
			got, want := racRandomCycle(ctx, rng, round)
			if !maps.Equal(got, want) {
				mismatches++

				t.Logf("round %d: got %v, want %v", round, got, want)
			}
		}

		So(mismatches, ShouldEqual, 0)
	})
}

// newRACLimitsServer returns a Server whose limiter has the given count limits;
// any other limit group has no limit.
func newRACLimitsServer(limits map[string]int) *Server {
	lim := limiter.New(func(_ context.Context, _ string) *limiter.GroupData {
		return nil
	})

	for name, limit := range limits {
		lim.SetLimit(name, *limiter.NewCountGroupData(int64(limit)))
	}

	return &Server{limiter: lim, previouslyScheduledGroups: make(map[string]*sgroup)}
}

// racRandomCycle runs a rac cycle over a random arrangement: up to 4 limit
// groups with random limits (some none), up to 6 scheduler groups each carrying a
// random subset of them, and their jobs in an order that mixes runs of one
// scheduler group with interleaving. It returns each scheduler group's counts,
// and the counts a plain job-by-job walk of the same order gives, which reads
// each limit group's limit when it first meets it and decrements it for each
// counted job.
func racRandomCycle(ctx context.Context, rng *rand.Rand, round int) (got, want map[string]racGroupCounts) {
	limits := make(map[string]int)
	limitGroups := make([]string, 0, 4)

	for i := range 1 + rng.IntN(4) {
		lg := fmt.Sprintf("lg%d", i)
		limitGroups = append(limitGroups, lg)

		if rng.IntN(4) > 0 {
			limits[lg] = rng.IntN(7)
		}
	}

	jobs := make([]*Job, 0, 64)

	for g := range 1 + rng.IntN(6) {
		var carried []string

		for _, lg := range limitGroups {
			if rng.IntN(2) == 0 {
				carried = append(carried, lg)
			}
		}

		jobs = append(jobs, racReadyJobs(fmt.Sprintf("r%d-g%d", round, g), 100*(g+1), 0, carried, rng.IntN(12))...)
	}

	racRunShuffle(rng, jobs)

	groups := make(map[string]*sgroup)
	racCountReadyJobs(ctx, newRACLimitsServer(limits), groups, jobs)

	got = make(map[string]racGroupCounts, len(groups))
	for name, group := range groups {
		got[name] = racGroupCounts{count: group.count, skipped: group.skipped}
	}

	return got, racJobByJobCounts(jobs, limits)
}

// racRunShuffle shuffles jobs, then sorts a random part of them back into runs
// of one scheduler group.
func racRunShuffle(rng *rand.Rand, jobs []*Job) {
	rng.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })

	if len(jobs) == 0 {
		return
	}

	start := rng.IntN(len(jobs))
	end := start + rng.IntN(len(jobs)-start+1)

	slices.SortStableFunc(jobs[start:end], func(a, b *Job) int {
		return strings.Compare(racGroupOf(a), racGroupOf(b))
	})
}

// racJobByJobCounts walks jobs in order, counting each against its scheduler
// group unless one of its limit groups has no remaining limit, in which case it
// is skipped; a counted job uses up one of each of its limited limit groups.
func racJobByJobCounts(jobs []*Job, limits map[string]int) map[string]racGroupCounts {
	remaining := make(map[string]int)
	for lg, limit := range limits {
		remaining[lg] = limit
	}

	counts := make(map[string]racGroupCounts)

	for _, job := range jobs {
		group := racGroupOf(job)
		c := counts[group]

		if racTakeLimits(job.LimitGroups, remaining) {
			c.count++
		} else {
			c.skipped++
		}

		counts[group] = c
	}

	return counts
}

// racTakeLimits uses up one of the remaining limit of each of the given limit
// groups that has a limit, returning true, unless one of them has none left, in
// which case it uses up nothing and returns false.
func racTakeLimits(limitGroups []string, remaining map[string]int) bool {
	for _, lg := range limitGroups {
		if left, limited := remaining[lg]; limited && left == 0 {
			return false
		}
	}

	for _, lg := range limitGroups {
		if _, limited := remaining[lg]; limited {
			remaining[lg]--
		}
	}

	return true
}

func TestRACPriorityOrder(t *testing.T) {
	ctx := context.Background()

	Convey("A rac cycle over ready jobs carrying limit groups", t, func() {
		s := newRACLimitsServer(map[string]int{"lgP": 100})

		jobs := make([]*Job, 0, 12)
		for g := range 3 {
			jobs = append(jobs, racReadyJobs(fmt.Sprintf("p%d", g), 100*(g+1), 0, []string{"lgP"}, 4)...)
		}

		rng := rand.New(rand.NewPCG(3, 5)) //nolint:gosec
		rng.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })

		Convey("considers them in the order they came when all have the same priority", func() {
			for _, job := range jobs {
				job.Priority = 7
			}

			candidates := racCountReadyJobs(ctx, s, make(map[string]*sgroup), jobs)

			So(racCandidateCmds(candidates), ShouldResemble, racJobCmds(jobs))
		})

		Convey("considers them highest-priority-first, in the order they came within a priority", func() {
			// the first few share a priority, so a check that stopped early would
			// take them all to share one.
			for i, job := range jobs {
				job.Priority = []uint8{5, 5, 5, 0, 9, 5}[i%6]
			}

			want := slices.Clone(jobs)
			slices.SortStableFunc(want, func(a, b *Job) int { return int(b.Priority) - int(a.Priority) })

			candidates := racCountReadyJobs(ctx, s, make(map[string]*sgroup), jobs)

			So(racCandidateCmds(candidates), ShouldResemble, racJobCmds(want))
			So(want[0].Priority, ShouldEqual, 9)
		})
	})
}

// racCandidateCmds returns the commands of the jobs of candidates, in order.
func racCandidateCmds(candidates []readyJobCandidate) []string {
	cmds := make([]string, len(candidates))
	for i, candidate := range candidates {
		cmds[i] = candidate.job.Cmd
	}

	return cmds
}

// racJobCmds returns the commands of jobs, in order.
func racJobCmds(jobs []*Job) []string {
	cmds := make([]string, len(jobs))
	for i, job := range jobs {
		cmds[i] = job.Cmd
	}

	return cmds
}
