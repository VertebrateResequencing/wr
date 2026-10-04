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
// granted the limit group's full remaining capacity in one ready-added-callback
// (rac) cycle. On the production manager this caused up to 13,271 runners to be
// requested for a limit-2000 group (6.6x). The accounting lives in
// scheduleReadyJobsByPriority (server.go): the pre-fix per-scheduler-group cache
// meant each sibling saw the full remaining capacity. The fix shares one budget
// per limit group across siblings within a rac cycle, so the summed request never
// exceeds the limit.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestReliable3LimitGroupOverProvision(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Sibling scheduler groups sharing one limit group share its capacity", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 4)
		siblingGroups := opEnvInt("WR_OP_SIBLINGS", 5)
		readyPerGroup := opEnvInt("WR_OP_READY", 20)

		s := newOverProvisionServer(limit)

		// sibling scheduler groups (distinct RAM, same "lg" limit group), with
		// their ready jobs interleaved as a real rac cycle would see them.
		siblings := make([][]*Job, siblingGroups)
		for g := range siblingGroups {
			siblings[g] = racReadyJobs(fmt.Sprintf("op%d", g), 100+g*100, 0, []string{"lg"}, readyPerGroup)
		}

		jobs := make([]*Job, 0, siblingGroups*readyPerGroup)
		for i := range readyPerGroup {
			for g := range siblingGroups {
				jobs = append(jobs, siblings[g][i])
			}
		}

		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		total := 0
		for _, grp := range groups {
			total += grp.count
		}

		t.Logf("over-provision check: limit=%d siblingGroups=%d readyPerGroup=%d "+
			"=> summed runner request=%d (buggy per-group accounting would give ~%d)",
			limit, siblingGroups, readyPerGroup, total, siblingGroups*limit)

		Convey("the summed runner request across siblings does not exceed the limit", func() {
			So(len(groups), ShouldEqual, siblingGroups)
			So(total, ShouldBeLessThanOrEqualTo, limit)
		})
	})

	Convey("A single scheduler group is still capped at its limit group's capacity", t, func() {
		limit := opEnvInt("WR_OP_LIMIT", 4)
		readyJobs := limit + opEnvInt("WR_OP_READY", 20)

		s := newOverProvisionServer(limit)
		jobs := racReadyJobs("op", 200, 0, []string{"lg"}, readyJobs)
		grpName := racGroupOf(jobs[0])

		groups := make(map[string]*sgroup)
		racCountReadyJobs(ctx, s, groups, jobs)

		Convey("its count equals the limit, not the larger ready backlog", func() {
			So(groups[grpName].count, ShouldEqual, limit)
		})
	})
}

// opEnvInt reads a positive integer from an env var, falling back to def. It is
// named distinctly from the reliability-tagged envInt so this (untagged) test
// compiles under a plain `go test` and never clashes under `-tags reliability`.
// It lets developers/wrdev.sh run this same deterministic check at production
// scale (WR_OP_LIMIT / WR_OP_SIBLINGS / WR_OP_READY) with no manager or LSF.
func opEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}

	return def
}

// newOverProvisionServer returns a Server whose limiter has a single count limit
// group "lg" with the given limit, for exercising the rac cycle's
// per-limit-group accounting without a running manager.
func newOverProvisionServer(limit int) *Server {
	lim := limiter.New(func(_ context.Context, _ string) *limiter.GroupData {
		return nil
	})
	lim.SetLimit("lg", *limiter.NewCountGroupData(int64(limit)))

	return &Server{limiter: lim, previouslyScheduledGroups: make(map[string]*sgroup)}
}

// racReadyJobs builds n ready jobs for driving a rac cycle's live ready-job
// selection: each has the given RAM (which, with its limit groups, fixes its
// scheduler group), priority and limit groups, and a distinct command.
func racReadyJobs(name string, ram int, priority uint8, limitGroups []string, n int) []*Job {
	jobs := make([]*Job, 0, n)

	for i := range n {
		jobs = append(jobs, &Job{
			Cmd:          fmt.Sprintf("echo %s %d", name, i),
			Cwd:          testCwd,
			ReqGroup:     name,
			RepGroup:     name,
			Requirements: &scheduler.Requirements{RAM: ram, Time: time.Minute, Cores: 1, Disk: 1},
			Priority:     priority,
			LimitGroups:  limitGroups,
		})
	}

	return jobs
}

// racCountReadyJobs runs a rac cycle's live ready-job selection over jobs, as
// buildSchedulerGroups does with a runner command set: snapshotReadyJobs, then
// scheduleReadyJobsByPriority, counting into groups. It returns the candidates in
// the order they were considered. Every job's requirement group is given no
// learned recommendation up front, so the cycle needs no database and leaves each
// job's requirements as they are; the jobs need not be in a queue, since setting
// the reserve group of a job not in one is ignored.
func racCountReadyJobs(ctx context.Context, s *Server, groups map[string]*sgroup, jobs []*Job) []readyJobCandidate {
	q := queue.New(ctx, "rac-count-ready")
	defer func() { So(q.Destroy(), ShouldBeNil) }()

	data := make([]any, 0, len(jobs))
	reqGroupToReqs := make(map[string]*scheduler.Requirements)

	for _, job := range jobs {
		data = append(data, job)
		reqGroupToReqs[job.ReqGroup] = nil
	}

	candidates := s.snapshotReadyJobs(data)
	s.scheduleReadyJobsByPriority(ctx, q, groups, candidates, racLimitRunnerCmd, reqGroupToReqs)

	return candidates
}

// racGroupOf returns the scheduler group a rac cycle counts the job in.
func racGroupOf(job *Job) string {
	return job.schedulerGroupSnapshot().group
}
