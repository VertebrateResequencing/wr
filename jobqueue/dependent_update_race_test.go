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

// This file covers item 7c of .docs/bugfixes/260930-dep-group-rerun-gaps.md: an
// add that has read a dependent's in-memory job, and then finds its queue item
// replaced by a fresh copy another add queued, must apply its new dependencies
// to that copy, not put the job it read on the item, so the dependent still
// runs again after the add's new member.

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	durOtherGroup = "dependent-update-race-other-group"
	durOtherName  = "other"
	durThirdName  = "third"
)

var errDURNotReached = errors.New("the window was not reached")

// durRace records what happened in the window durRunFreshCopyInWindow opens.
type durRace struct {
	started *Job
	err     error
}

// durRunFreshCopyInWindow installs a dependentReadHook that, the first time an
// add is about to update the keyed dependent's queue item, lets the held archive
// remove that item, lets the held add queue its fresh copy of the dependent and
// finish, and has client run that add's member to completion and then reserve
// and start the fresh copy, before the add about to update the item carries on.
func durRunFreshCopyInWindow(ctx context.Context, d *dgrServer, client *Client, key string, archive *dgwWindow,
	removed chan struct{}, held *dgwWindow, heldAdded chan error) *durRace {
	race := &durRace{err: errDURNotReached}

	var fired atomic.Bool

	dependentReadHook = func(dependent string) {
		if dependent != key || !fired.CompareAndSwap(false, true) {
			return
		}

		race.started, race.err = durStartFreshCopy(ctx, d, client, archive, removed, held, heldAdded)
	}

	return race
}

// TestDependentUpdateRace proves a dependent still runs again after an add's
// new member when, after the add read its in-memory job and before the add
// updated its queue item, its archive removed the item and another add, which
// had read it complete before this add's member existed, queued a fresh copy of
// it that then started running.
func TestDependentUpdateRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on two dep groups, whose archive has committed", t, func() {
		d, jq, waiter := durRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() {
			archiveCommittedHook = nil
			archiveRemovedHook = nil
			dependencyUpdatesHook = nil
			dependentReadHook = nil
		}()

		runner := rdrReconnect(d, jq)
		defer disconnect(runner)

		other := d.connect()
		defer disconnect(other)

		freshRunner := d.connect()
		defer disconnect(freshRunner)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		third := dgaMemberJob(d, durOtherGroup, durThirdName)
		window := acwHoldCommittedArchive(waiter.Key())
		removed := durSignalRemoved(waiter.Key())

		archived := make(chan error, 1)

		go func() {
			archived <- runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		}()

		So(window.reached(), ShouldBeTrue)

		Convey("an add to one group held after reading it complete, and an add to the other that reads it queued", func() {
			held := durHoldFirstDependencyUpdates()

			thirdAdded := make(chan error, 1)

			go func() {
				_, _, err := other.Add([]*Job{third}, envVars, true)
				thirdAdded <- err
			}()

			So(held.reached(), ShouldBeTrue)

			race := durRunFreshCopyInWindow(ctx, d, freshRunner, waiter.Key(), window, removed, held, thirdAdded)

			_, _, erra := jq.Add([]*Job{second}, envVars, true)
			So(erra, ShouldBeNil)
			So(race.err, ShouldBeNil)
			So(race.started, ShouldNotBeNil)
			So(<-archived, ShouldBeNil)

			Convey("leave it to run again after the second add's member, once its fresh copy's run ends", func() {
				So(freshRunner.Archive(race.started, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}),
					ShouldBeNil)

				cmrSoRerunsAfterSecond(ctx, d, jq, waiter, second)
			})
		})
	})
}

// durRunningWaiter starts a manager with a job running that depends on two dep
// groups, each with one member that has completed.
func durRunningWaiter(ctx context.Context) (*dgrServer, *Client, *Job) {
	d := dgrStartServer(ctx)
	jq := d.connect()

	first := dgaMemberJob(d, rdrGroup, rdrFirstName)
	otherFirst := dgaMemberJob(d, durOtherGroup, durOtherName)
	waiter := dgaWaiterJob(d, rdrGroup, rdrWaiterName)
	waiter.Dependencies = append(waiter.Dependencies, NewDepGroupDependency(durOtherGroup))

	dgrAddJobs(jq, []*Job{first, otherFirst})
	dgrAddJobs(jq, []*Job{waiter})

	durExecuteNextReserved(ctx, d, jq)
	durExecuteNextReserved(ctx, d, jq)

	reserved, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, waiter.Key())
	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateRun)

	return d, jq, reserved
}

// durExecuteNextReserved reserves the next ready job and runs it to completion,
// which archives it.
func durExecuteNextReserved(ctx context.Context, d *dgrServer, jq *Client) {
	job, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)

	execute(ctx, jq, job, d.config.RunnerExecShell)
}

// durSignalRemoved installs an archiveRemovedHook that closes the returned
// channel once the keyed job's archive has first removed its queue item.
func durSignalRemoved(key string) chan struct{} {
	removed := make(chan struct{})

	var fired atomic.Bool

	archiveRemovedHook = func(archiving string) {
		if archiving == key && fired.CompareAndSwap(false, true) {
			close(removed)
		}
	}

	return removed
}

// durHoldFirstDependencyUpdates installs a dependencyUpdatesHook that holds the
// first add to call it, once its write has committed and before it queues
// anything, until the returned window is released.
func durHoldFirstDependencyUpdates() *dgwWindow {
	w := &dgwWindow{in: make(chan struct{}), out: make(chan struct{})}

	var fired atomic.Bool

	dependencyUpdatesHook = func() {
		if !fired.CompareAndSwap(false, true) {
			return
		}

		close(w.in)

		select {
		case <-w.out:
		case <-time.After(dgrReserveWait):
		}
	}

	return w
}

// durStartFreshCopy is the work of durRunFreshCopyInWindow's hook. It uses no
// So(), since it runs on the manager's goroutine.
func durStartFreshCopy(ctx context.Context, d *dgrServer, client *Client, archive *dgwWindow,
	removed chan struct{}, held *dgwWindow, heldAdded chan error) (*Job, error) {
	archive.release()

	select {
	case <-removed:
	case <-time.After(dgrReserveWait):
		return nil, errDURNotReached
	}

	held.release()

	if err := <-heldAdded; err != nil {
		return nil, err
	}

	member, err := client.Reserve(dgrReserveWait)
	if err != nil || member == nil {
		return nil, errDURNotReached
	}

	execute(ctx, client, member, d.config.RunnerExecShell)

	fresh, err := client.Reserve(dgrReserveWait)
	if err != nil || fresh == nil {
		return nil, errDURNotReached
	}

	return fresh, client.Started(fresh, os.Getpid())
}
