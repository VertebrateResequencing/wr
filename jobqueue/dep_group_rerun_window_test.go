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

// This file covers item 7 of .docs/bugfixes/260930-dep-group-rerun-gaps.md: an
// add that brings back a job between its archive removing its queue item and
// that archive dropping its dep group memberships and rep group lookup must not
// lose them.

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dgwOwnGroup is the dep group the waiter is itself a member of.
	dgwOwnGroup = "dep-group-rerun-window-own-group"

	// dgwRepGroup is the waiter's own rep group, so it is the only job in it.
	dgwRepGroup = "dep-group-rerun-window-waiter"

	// dgwOtherRepGroup is the rep group the waiter is added again with.
	dgwOtherRepGroup = "dep-group-rerun-window-other"
)

// dgwOrder is when, relative to the archive of a job, an add brings it back.
type dgwOrder int

const (
	// dgwAddFromWindow adds while the archive is held in the window.
	dgwAddFromWindow dgwOrder = iota

	// dgwAddReadRunning adds, reading the job running, and applies its
	// dependency updates once the archive is held in the window.
	dgwAddReadRunning

	// dgwArchiveDuringAdd adds while the archive is held in the window, and lets
	// the archive finish while the add applies its dependency updates, before it
	// has queued the job again.
	dgwArchiveDuringAdd
)

// TestDepGroupRerunWindow proves a job brought back to run again while its
// archive is between removing its queue item and releasing its dep group
// memberships and rep group lookup keeps both, and does not release the jobs
// waiting on those groups, whichever way the add brings it back and whenever
// the archive gets to its cleanup.
func TestDepGroupRerunWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on one dep group and is a member of another", t, func() {
		d, runner, waiter, earlier := dgwRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(runner)

		adder := d.connect()
		defer disconnect(adder)

		defer func() { archiveRemovedHook, dependencyUpdatesHook = nil, nil }()

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		window := dgwHoldArchiveWindow(waiter.Key())

		var archiveErr error

		archived := make(chan struct{})
		archive := func() {
			defer close(archived)

			archiveErr = runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		}

		// soAddedInWindow adds second, a new member of the group the waiter
		// depends on, so that the add brings the waiter back while its archive is
		// held in the window, then lets the archive finish: after the add, or,
		// for dgwArchiveDuringAdd, while the add is bringing it back.
		soAddedInWindow := func(order dgwOrder) {
			// set by the manager's goroutine, so atomic to be race free.
			var missedDuringAdd atomic.Bool

			switch order {
			case dgwAddFromWindow:
				go archive()

				So(window.reached(), ShouldBeTrue)
			case dgwAddReadRunning:
				dependencyUpdatesHook = func() {
					dependencyUpdatesHook = nil

					go archive()

					missedDuringAdd.Store(!window.reached())
				}
			case dgwArchiveDuringAdd:
				go archive()

				So(window.reached(), ShouldBeTrue)

				dependencyUpdatesHook = func() {
					dependencyUpdatesHook = nil

					window.release()
					<-archived
				}
			}

			inserts, _, erra := adder.Add([]*Job{second}, envVars, true)
			So(erra, ShouldBeNil)
			So(missedDuringAdd.Load(), ShouldBeFalse)
			So(inserts, ShouldEqual, 2)

			window.release()
			<-archived
			So(archiveErr, ShouldBeNil)

			rdrSoCompleteRecorded(d, waiter.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)
			So(dgaItemState(d.server, earlier), ShouldEqual, queue.ItemStateDependent)
		}

		for _, route := range []struct {
			name  string
			order dgwOrder
		}{
			{"an add made once it is archived and has left the queue", dgwAddFromWindow},
			{"an add that read it running, applying its updates once it has left the queue", dgwAddReadRunning},
			{"an add made once it is archived and has left the queue, which its archive finishes during,",
				dgwArchiveDuringAdd},
		} {
			Convey("if "+route.name+" brings it back while its archive is in the window", func() {
				soAddedInWindow(route.order)

				Convey("its rep group still finds it waiting to run again", func() {
					jobs, err := adder.GetByRepGroup(dgwRepGroup, false, 0, JobStateDependent, false, false)
					So(err, ShouldBeNil)
					So(jobs, ShouldHaveLength, 1)
					So(jobs[0].Key(), ShouldEqual, waiter.Key())
				})

				Convey("a job then added depending on its own dep group waits for it to run again", func() {
					child := dgaWaiterJob(d, dgwOwnGroup, "child")
					dgrAddJobs(adder, []*Job{child})

					So(dgaItemState(d.server, child.Key()), ShouldEqual, queue.ItemStateDependent)
				})
			})
		}

		Convey("if it is added again with another rep group while its archive is in the window", func() {
			again := dgaWaiterJob(d, rdrGroup, rdrWaiterName)
			again.RepGroup = dgwOtherRepGroup
			again.DepGroups = []string{dgwOwnGroup}
			So(again.Key(), ShouldEqual, waiter.Key())

			go archive()

			So(window.reached(), ShouldBeTrue)

			inserts, _, erra := adder.Add([]*Job{again}, envVars, false)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			window.release()
			<-archived
			So(archiveErr, ShouldBeNil)

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
			So(dgaItemState(d.server, earlier), ShouldEqual, queue.ItemStateDependent)

			Convey("only its new rep group finds it waiting to run again", func() {
				jobs, err := adder.GetByRepGroup(dgwOtherRepGroup, false, 0, JobStateReady, false, false)
				So(err, ShouldBeNil)
				So(jobs, ShouldHaveLength, 1)
				So(jobs[0].Key(), ShouldEqual, waiter.Key())

				jobs, err = adder.GetByRepGroup(dgwRepGroup, false, 0, JobStateReady, false, false)
				So(err, ShouldBeNil)
				So(jobs, ShouldBeEmpty)
			})
		})
	})
}

// dgwRunningWaiter starts a server holding a completed member of rdrGroup and a
// job depending on that group, a member of dgwOwnGroup, which a runner (the
// returned client) has reserved and started, and then a job depending on
// dgwOwnGroup. It returns the server, the client, the reserved waiter and the
// key of the job depending on the waiter's group.
func dgwRunningWaiter(ctx context.Context) (*dgrServer, *Client, *Job, string) {
	d := dgrStartServer(ctx)
	jq := d.connect()

	member := dgaMemberJob(d, rdrGroup, rdrFirstName)
	waiter := dgaWaiterJob(d, rdrGroup, rdrWaiterName)
	waiter.RepGroup = dgwRepGroup
	waiter.DepGroups = []string{dgwOwnGroup}

	dgrAddJobs(jq, []*Job{member})
	dgrAddJobs(jq, []*Job{waiter})

	dgaExecuteReserved(ctx, d, jq, member.Key())

	reserved, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, waiter.Key())
	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

	earlier := dgrAddJobs(jq, []*Job{dgaWaiterJob(d, dgwOwnGroup, "earlier")})[0]
	So(dgaItemState(d.server, earlier), ShouldEqual, queue.ItemStateDependent)

	return d, jq, reserved, earlier
}

// dgwHoldArchiveWindow installs an archiveRemovedHook that holds the keyed job's
// archive in the window until release is called.
func dgwHoldArchiveWindow(key string) *dgwWindow {
	w := &dgwWindow{in: make(chan struct{}), out: make(chan struct{})}

	archiveRemovedHook = func(removed string) {
		if removed != key {
			return
		}

		w.once.Do(func() {
			close(w.in)

			select {
			case <-w.out:
			case <-time.After(dgrReserveWait):
			}
		})
	}

	return w
}

// dgwWindow holds the archive of one job in archiveRemovedHook's window.
type dgwWindow struct {
	in, out           chan struct{}
	once, releaseOnce sync.Once
}

// reached reports whether the archive got to the window within dgrReserveWait.
// It uses no So(), so a hook can call it from the manager's goroutine.
func (w *dgwWindow) reached() bool {
	select {
	case <-w.in:
		return true
	case <-time.After(dgrReserveWait):
		return false
	}
}

// release lets the held archive carry on. Only the first call does anything.
func (w *dgwWindow) release() {
	w.releaseOnce.Do(func() { close(w.out) })
}
