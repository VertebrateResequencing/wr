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
	"sync/atomic"
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

// limitRaceHold is how long addRacingLimitChange holds an add, after it has
// stored its limit groups, for the racing change to complete. A change that is
// correctly made to wait for the add cannot complete in it, while one that is
// not completes in a fraction of it.
const limitRaceHold = time.Second

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
			_ = addRacingLimitChange(jq, d, fmt.Sprintf("%s:%d", luGroup, oldLimit), func() error {
				return d.server.setLimitGroup(ctx, luGroup, newer)
			})
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

// TestLimitStoreAppliedInCommitOrder covers an add that stores a group's limit
// for the first time, racing another request that stores a different limit for
// the group after the add's database write: whichever is stored last in the
// database must also be the limit the manager enforces.
func TestLimitStoreAppliedInCommitOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server with a limit group that has no limit", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		const addedLimit, newLimit = 5, 3

		t.Cleanup(func() { limitGroupsStoredHook = nil })

		added := fmt.Sprintf("%s:%d", luGroup, addedLimit)

		Convey("an add setting a limit, racing wr limit changing it, ends with the same limit in memory as on disk", func() {
			early := addRacingLimitChange(jq, d, added, func() error {
				return d.server.setLimitGroup(ctx, luGroup, limiter.NewCountGroupData(newLimit))
			})

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, limitGroupStored(ctx, d, luGroup))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)
			So(early, ShouldBeFalse)
		})

		Convey("an add setting a limit, racing wr limit removing it, ends with it removed in memory as on disk", func() {
			early := addRacingLimitChange(jq, d, added, func() error {
				return d.server.setLimitGroup(ctx, luGroup, limiter.NewCountGroupData(-1))
			})

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, lgrNoLimit)
			So(limitGroupRecorded(d, luGroup), ShouldBeFalse)
			So(early, ShouldBeFalse)
		})

		Convey("an add setting a limit, racing another add setting another, ends with memory agreeing with disk", func() {
			early := addRacingLimitChange(jq, d, added, func() error {
				return d.server.storeLimitGroups(map[string]*limiter.GroupData{
					luGroup: limiter.NewCountGroupData(newLimit),
				})
			})

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, limitGroupStored(ctx, d, luGroup))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)
			So(early, ShouldBeFalse)
		})
	})
}

// addRacingLimitChange adds a job in limitGroup (eg. "name:5"), and once the add
// has stored its limit groups in the database, before it gives them to the
// limiter, starts change in another goroutine and holds the add for up to
// limitRaceHold while change runs. It asserts that change completes without
// error once the add has, and returns whether change completed while the add
// was held.
func addRacingLimitChange(jq *Client, d *dgrServer, limitGroup string, change func() error) bool {
	var fired atomic.Bool

	done := make(chan error, 1)
	completedWhileHeld := make(chan bool, 1)

	limitGroupsStoredHook = func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}

		go func() { done <- change() }()

		select {
		case err := <-done:
			done <- err

			completedWhileHeld <- true
		case <-time.After(limitRaceHold):
			completedWhileHeld <- false
		}
	}

	job := d.job("echo lu racing", "lu")
	job.LimitGroups = []string{limitGroup}
	dgrAddJobs(jq, []*Job{job})

	select {
	case err := <-done:
		So(err, ShouldBeNil)
	case <-time.After(dgrReserveWait):
		So("the racing change did not complete after the add", ShouldBeEmpty)
	}

	// only now, as change itself may call the hook
	limitGroupsStoredHook = nil

	return <-completedWhileHeld
}
