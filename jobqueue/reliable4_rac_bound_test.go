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

// Untagged behavioural regression test for the reliable4 BUG 1 fix (rac backlog
// rescan): buildSchedulerGroups must bound its EXPENSIVE per-job work
// (prepareReadyJob) by the SCHEDULABLE count, not the ready-backlog size, while
// (a) scheduling/reserving the HIGHEST-priority jobs within a limit group,
// (b) keeping them reservable, and (c) picking up the deferred (limit-blocked)
// jobs once the higher-priority contenders drain. Unlike the build-tagged
// reliable4_repro_test.go this runs at small scale under `make test`, so the
// guarantee is covered by the normal test suite.

import (
	"context"
	"fmt"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestReliable4RacBoundedBySchedulable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("buildSchedulerGroups bounds its per-job work by the schedulable count, priority-fair", t, func() {
		const (
			limit        = 5
			highCount    = 8
			lowCount     = 6
			highPriority = uint8(200)
			lowPriority  = uint8(10)
			limitGroup   = "lg:5"
			highRAM      = 10
			lowRAM       = 100
		)

		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// two sibling scheduler groups (distinct RAM => distinct requirements =>
		// distinct scheduler group) sharing ONE count-limited limit group. The HIGH
		// group is higher priority, so priority-fair selection must schedule its
		// jobs first and starve the LOW group's this cycle.
		jobs := make([]*Job, 0, highCount+lowCount)
		jobs = append(jobs, racBoundJobs("reliable4-high", highRAM, highPriority, limitGroup, highCount)...)
		jobs = append(jobs, racBoundJobs("reliable4-low", lowRAM, lowPriority, limitGroup, lowCount)...)

		added, existed, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, highCount+lowCount)
		So(existed, ShouldEqual, 0)

		So(pollUntil(func() bool {
			server.rpmutex.Lock()
			running := server.racRunning
			server.rpmutex.Unlock()

			return !running && len(server.q.AllItems()) == highCount+lowCount
		}), ShouldBeTrue)

		allitemdata := itemData(server.q.AllItems())

		groups := server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")
		scanWork := int(server.racScanWork.Load())

		var scheduled, blocked *sgroup

		for _, g := range groups {
			if g.count > 0 {
				scheduled = g
			} else {
				blocked = g
			}
		}

		Convey("only the schedulable (limit) jobs incur the expensive prepareReadyJob work", func() {
			// pre-fix this would be highCount+lowCount (the whole backlog).
			So(scanWork, ShouldEqual, limit)
		})

		Convey("exactly the limit is scheduled and it is the highest-priority group", func() {
			So(len(groups), ShouldEqual, 2)
			So(scheduled, ShouldNotBeNil)
			So(blocked, ShouldNotBeNil)

			So(scheduled.count, ShouldEqual, limit)
			So(scheduled.priority, ShouldEqual, highPriority) // proves HIGH-priority jobs won the budget
			So(scheduled.skipped, ShouldEqual, highCount-limit)

			So(blocked.count, ShouldEqual, 0)
			So(blocked.skipped, ShouldEqual, lowCount)
		})

		Convey("the limit-blocked jobs are recorded as skipped so a completion re-triggers scheduling", func() {
			So(blocked.hasSkips(), ShouldBeTrue)
		})

		Convey("once the higher-priority contenders drain, the deferred jobs are scheduled next cycle", func() {
			// simulate the HIGH group's jobs having left ready (scheduled+completed):
			// the next rac cycle sees only the previously-blocked LOW group.
			lowItems := itemsInGroup(server.q.AllItems(), blocked.name)
			So(len(lowItems), ShouldEqual, lowCount)

			nextGroups := server.buildSchedulerGroups(ctx, server.q, lowItems, "true")
			nextScanWork := int(server.racScanWork.Load())

			lowGroup := nextGroups[blocked.name]
			So(lowGroup, ShouldNotBeNil)
			So(lowGroup.count, ShouldEqual, limit) // the deferred jobs now get scheduled
			So(lowGroup.skipped, ShouldEqual, lowCount-limit)
			So(nextScanWork, ShouldEqual, limit) // still bounded by the schedulable count
		})
	})
}

// racBoundJobs builds n jobs with the given RAM (to fix their scheduler group),
// job priority and shared limit group, for driving buildSchedulerGroups.
func racBoundJobs(repGroup string, ram int, priority uint8, limitGroup string, n int) []*Job {
	jobs := make([]*Job, 0, n)

	for i := range n {
		jobs = append(jobs, &Job{
			Cmd:          fmt.Sprintf("%s %d", repGroup, i),
			Cwd:          testCwd,
			ReqGroup:     repGroup,
			Requirements: &jqs.Requirements{RAM: ram, Time: 10 * time.Second, Cores: 1, Disk: 0},
			RepGroup:     repGroup,
			Priority:     priority,
			LimitGroups:  []string{limitGroup},
		})
	}

	return jobs
}

// itemData extracts the *Job data of queue items, as the ready-added callback
// receives it.
func itemData(items []*queue.Item) []any {
	allitemdata := make([]any, 0, len(items))
	for _, item := range items {
		allitemdata = append(allitemdata, item.Data())
	}

	return allitemdata
}

// itemsInGroup returns the *Job data of the queue items that belong to the given
// scheduler group (by their computed requirements+limit-group snapshot, which is
// what buildSchedulerGroups keys on).
func itemsInGroup(items []*queue.Item, group string) []any {
	filtered := make([]any, 0, len(items))

	for _, item := range items {
		job, ok := item.Data().(*Job)
		if !ok {
			continue
		}

		if job.schedulerGroupSnapshot().group == group {
			filtered = append(filtered, job)
		}
	}

	return filtered
}
