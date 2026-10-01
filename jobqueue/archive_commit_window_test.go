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

// This file covers item 7b of .docs/bugfixes/260930-dep-group-rerun-gaps.md: an
// add that reads a dependent complete after its archive's write committed, but
// before the archive removed its queue item, must still have it run again after
// the add's new member, without a restart.

import (
	"context"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestArchiveCommitWindow proves a dependent whose archive has committed, but
// not yet removed its queue item, when an add gives its dep group a new member,
// waits on that member and then runs again.
func TestArchiveCommitWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on a dep group", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() { archiveCommittedHook = nil }()

		runner := rdrReconnect(d, jq)
		defer disconnect(runner)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		window := acwHoldCommittedArchive(waiter.Key())

		var archiveErr error

		archived := make(chan struct{})

		go func() {
			defer close(archived)

			archiveErr = runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		}()

		So(window.reached(), ShouldBeTrue)

		Convey("an add of a new member while its archive has committed but not left the queue", func() {
			inserts, dups, erra := jq.Add([]*Job{second}, envVars, true)
			So(erra, ShouldBeNil)

			window.release()
			<-archived
			So(archiveErr, ShouldBeNil)

			rdrSoCompleteRecorded(d, waiter.Key())

			Convey("makes it wait on the new member, then run again", func() {
				So(cmrJobState(jq, waiter), ShouldEqual, JobStateDependent)

				// the new member and the dependent it brings back are each added,
				// as they are when the add reads the dependent complete after its
				// archive (TestDepGroupRerunWindow), and nothing is a duplicate.
				So(inserts, ShouldEqual, 2)
				So(dups, ShouldEqual, 0)

				cmrSoRerunsAfterSecond(ctx, d, jq, waiter, second)
			})

			Convey("leaves it on disk to wait on the new member, then run again, after a restart", func() {
				rdrCrash(ctx, d, func() {})

				client := d.connect()
				defer disconnect(client)

				cmrSoRerunsAfterSecond(ctx, d, client, waiter, second)
			})
		})
	})
}

// acwHoldCommittedArchive installs an archiveCommittedHook that holds the keyed
// job's archive, once its write has returned and before it removes the job's
// queue item, until release is called.
func acwHoldCommittedArchive(key string) *dgwWindow {
	w := &dgwWindow{in: make(chan struct{}), out: make(chan struct{})}

	archiveCommittedHook = func(archiving string) {
		if archiving != key {
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
