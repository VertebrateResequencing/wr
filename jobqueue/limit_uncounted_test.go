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
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	luGroup = "lu-lg"
	luJobs  = 5
	luLimit = 2

	// luBlockedWait is how long a reserve that the limit must refuse is given.
	// A refused reserve waits for all of it, so it is short; a reserve that
	// should succeed is given dgrReserveWait instead.
	luBlockedWait = 250 * time.Millisecond
)

// TestLimitSetOnRunningGroup covers GitHub issue #448: a limit set on a limit
// group whose jobs were already running (with `wr limit -g name:n` or by adding
// a job with name:n) must count those jobs, so that no more start until fewer
// than the limit are running. Nor may removing the limit and setting it again
// forget the jobs that are running.
func TestLimitSetOnRunningGroup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server with jobs in a limit group that has no limit", t, func() {
		config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		server.setRC(serverRC)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobs := make([]*Job, 0, luJobs)
		for i := range luJobs {
			jobs = append(jobs, &Job{
				Cmd: fmt.Sprintf("echo lu %d", i), Cwd: testCwd, ReqGroup: reqGroupFake,
				Requirements: reqs, RepGroup: "lu", LimitGroups: []string{luGroup},
			})
		}

		dgrAddJobs(jq, jobs)

		group := schedulerGroupString(reqForScheduler(reqs), []string{luGroup})
		reserve := func(wait time.Duration) *Job {
			job, errr := jq.ReserveScheduled(wait, group)
			So(errr, ShouldBeNil)

			return job
		}

		execute := func(job *Job) {
			So(jq.Execute(ctx, job, config.RunnerExecShell), ShouldBeNil)
		}

		setLimit := func(limit int) {
			got, errl := jq.GetOrSetLimitGroup(fmt.Sprintf("%s:%d", luGroup, limit))
			So(errl, ShouldBeNil)
			So(got, ShouldEqual, limit)
		}

		// running has luLimit+1 jobs of the group running, and the group's limit
		// is luLimit: no more may start until two have finished.
		running := make([]*Job, 0, luLimit+1)
		reserveRunning := func(count int) {
			for range count {
				job := reserve(dgrReserveWait)
				So(job, ShouldNotBeNil)

				running = append(running, job)
			}
		}

		limitHolds := func() {
			So(reserve(luBlockedWait), ShouldBeNil)

			execute(running[0])
			So(reserve(luBlockedWait), ShouldBeNil)

			execute(running[1])
			So(reserve(dgrReserveWait), ShouldNotBeNil)
			So(reserve(luBlockedWait), ShouldBeNil)
		}

		Convey("a limit set while more than it are running lets no more start until fewer than it run", func() {
			reserveRunning(luLimit + 1)

			Convey("when set with wr limit", func() {
				setLimit(luLimit)
				limitHolds()
			})

			Convey("when set by adding a job with the limit", func() {
				dgrAddJobs(jq, []*Job{{
					Cmd: "echo lu limited", Cwd: testCwd, ReqGroup: reqGroupFake, Requirements: reqs,
					RepGroup: "lu", LimitGroups: []string{fmt.Sprintf("%s:%d", luGroup, luLimit)},
				}})

				limitHolds()
			})
		})

		Convey("a limit removed while its jobs run, then set again, still counts them", func() {
			setLimit(luLimit)
			reserveRunning(luLimit)
			So(reserve(luBlockedWait), ShouldBeNil)

			setLimit(-1)
			reserveRunning(1)

			setLimit(luLimit)
			limitHolds()
		})
	})
}

// TestLimitAddKeepsNewerLimit covers an add that carries a group's limit
// unaltered, racing a `wr limit` that changes or removes it after the add's
// database read: the add must leave the newer limit in place, in memory as on
// disk, rather than bring its own back.
func TestLimitAddKeepsNewerLimit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server with a limit group whose limit is set", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		const oldLimit, newLimit = 5, 3

		_, err := jq.GetOrSetLimitGroup(fmt.Sprintf("%s:%d", luGroup, oldLimit))
		So(err, ShouldBeNil)

		t.Cleanup(func() { limitGroupsStoredHook = nil })

		addRacing := func(newer *limiter.GroupData) {
			var once sync.Once

			raced := make(chan error, 1)
			limitGroupsStoredHook = func() {
				once.Do(func() {
					raced <- d.server.setLimitGroup(ctx, luGroup, newer)
				})
			}

			job := d.job("echo lu racing", "lu")
			job.LimitGroups = []string{fmt.Sprintf("%s:%d", luGroup, oldLimit)}
			dgrAddJobs(jq, []*Job{job})

			limitGroupsStoredHook = nil

			So(<-raced, ShouldBeNil)
		}

		Convey("an add with that limit, racing wr limit changing it, keeps the new limit", func() {
			addRacing(limiter.NewCountGroupData(newLimit))

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)
			So(limitGroupStored(ctx, d, luGroup), ShouldEqual, newLimit)
		})

		Convey("an add with that limit, racing wr limit removing it, keeps it removed", func() {
			addRacing(limiter.NewCountGroupData(-1))

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, lgrNoLimit)
			So(limitGroupRecorded(d, luGroup), ShouldBeFalse)
		})
	})
}
