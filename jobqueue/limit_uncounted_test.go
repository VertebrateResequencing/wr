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

const (
	// limitRaceWait is how long raceLimitStore holds a request, after it has
	// stored its limit groups, for a racing change that must not wait for it
	// to complete. It completes in a fraction of this; the bound only stops a
	// change that wrongly waits from hanging the test.
	limitRaceWait = 10 * time.Second

	// limitRaceHold is how long raceLimitStore holds a request that is giving
	// its limits to the limiter, for a racing change that must wait for it. A
	// change that wrongly does not wait completes in a fraction of this.
	limitRaceHold = time.Second
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
			_ = raceLimitStore(&limitGroupsStoredHook, limitRaceWait, func() {
				lruAdd(jq, d, fmt.Sprintf("%s:%d", luGroup, oldLimit))
			}, func() error {
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

		Convey("wr limit setting the limit it already has repairs a limiter that disagrees", func() {
			d.server.limiter.SetLimit(luGroup, *limiter.NewCountGroupData(newLimit))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)

			So(lgrLimitCmd(jq, fmt.Sprintf("%s:%d", luGroup, oldLimit)), ShouldEqual, oldLimit)
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, oldLimit)
			So(limitGroupStored(ctx, d, luGroup), ShouldEqual, oldLimit)
		})

		Convey("an add with the group at -1 removes its limit", func() {
			lruAdd(jq, d, luGroup+":-1")

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, lgrNoLimit)
			So(limitGroupRecorded(d, luGroup), ShouldBeFalse)
		})
	})
}

// TestLimitStoreAppliedInCommitOrder covers two requests that store different
// limits for a group at the same moment: whichever the database stores last
// must also be the limit the manager enforces, and neither request may wait for
// the other's database write.
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

		t.Cleanup(func() {
			limitGroupsStoredHook = nil
			limitGroupsAppliedHook = nil
		})

		added := fmt.Sprintf("%s:%d", luGroup, addedLimit)
		add := func() { lruAdd(jq, d, added) }
		setLimit := func(limit int64) func() error {
			return func() error {
				return d.server.setLimitGroup(ctx, luGroup, limiter.NewCountGroupData(limit))
			}
		}

		Convey("an add setting a limit, racing wr limit changing it, ends with the same limit in memory as on disk", func() {
			early := raceLimitStore(&limitGroupsStoredHook, limitRaceWait, add, setLimit(newLimit))

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, limitGroupStored(ctx, d, luGroup))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)
			So(early, ShouldBeTrue)
		})

		Convey("an add setting a limit, racing wr limit removing it, ends with it removed in memory as on disk", func() {
			early := raceLimitStore(&limitGroupsStoredHook, limitRaceWait, add, setLimit(-1))

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, lgrNoLimit)
			So(limitGroupRecorded(d, luGroup), ShouldBeFalse)
			So(early, ShouldBeTrue)
		})

		Convey("an add setting a limit, racing another add setting another, ends with memory agreeing with disk", func() {
			early := raceLimitStore(&limitGroupsStoredHook, limitRaceWait, add, func() error {
				return d.server.storeLimitGroups(map[string]*limiter.GroupData{
					luGroup: limiter.NewCountGroupData(newLimit),
				})
			})

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, limitGroupStored(ctx, d, luGroup))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)
			So(early, ShouldBeTrue)
		})

		Convey("wr limit setting a limit, racing an add setting another, ends with memory agreeing with disk", func() {
			jq2 := d.connect()

			defer disconnect(jq2)

			early := raceLimitStore(&limitGroupsStoredHook, limitRaceWait, func() {
				So(lgrLimitCmd(jq, fmt.Sprintf("%s:%d", luGroup, newLimit)), ShouldEqual, newLimit)
			}, func() error {
				job := d.job("echo lu other", "lu")
				job.LimitGroups = []string{added}
				_, _, err := jq2.Add([]*Job{job}, envVars, true)

				return err
			})

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, limitGroupStored(ctx, d, luGroup))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, addedLimit)
			So(early, ShouldBeTrue)
		})

		Convey("an add applying its limit, racing wr limit changing it, ends with memory agreeing with disk", func() {
			early := raceLimitStore(&limitGroupsAppliedHook, limitRaceHold, add, setLimit(newLimit))

			So(lgrLimitCmd(jq, luGroup), ShouldEqual, limitGroupStored(ctx, d, luGroup))
			So(lgrLimitCmd(jq, luGroup), ShouldEqual, newLimit)
			So(early, ShouldBeFalse)
		})
	})
}

// TestLimitAddKeepsTimeGroup covers an add naming a time-based limit group,
// whose limit comes from its name: it changes nothing, so must leave the
// limiter's group for it in place rather than drop it.
func TestLimitAddKeepsTimeGroup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server whose limiter has a time-based limit group", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		const timeGroup = "datetime<2099-01-01 00:00:00"

		before := d.server.limiter.GetLimit(ctx, timeGroup)
		So(before.IsCount(), ShouldBeFalse)
		So(before.IsValid(), ShouldBeTrue)

		Convey("an add naming it keeps that group", func() {
			lruAdd(jq, d, timeGroup)

			So(d.server.limiter.GetLimit(ctx, timeGroup), ShouldPointTo, before)
		})
	})
}

// lruAdd adds a job in the given limit group (eg. "name:5").
func lruAdd(jq *Client, d *dgrServer, limitGroup string) {
	job := d.job("echo lu racing", "lu")
	job.LimitGroups = []string{limitGroup}
	dgrAddJobs(jq, []*Job{job})
}

// raceLimitStore runs held, and the first time held's request calls *hook,
// starts change in another goroutine and holds held's request there for up to
// hold while change runs. It asserts that the hook was reached and that change
// completes without error once held has, and returns whether change completed
// while held's request was held.
func raceLimitStore(hook *func(), hold time.Duration, held func(), change func() error) bool {
	var fired atomic.Bool

	done := make(chan error, 1)
	completedWhileHeld := make(chan bool, 1)

	*hook = func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}

		go func() { done <- change() }()

		select {
		case err := <-done:
			done <- err

			completedWhileHeld <- true
		case <-time.After(hold):
			completedWhileHeld <- false
		}
	}

	held()
	So(fired.Load(), ShouldBeTrue)

	select {
	case err := <-done:
		So(err, ShouldBeNil)
	case <-time.After(dgrReserveWait):
		So("the racing change did not complete after the held request", ShouldBeEmpty)
	}

	// only now, as change itself may call the hook
	*hook = nil

	return <-completedWhileHeld
}
