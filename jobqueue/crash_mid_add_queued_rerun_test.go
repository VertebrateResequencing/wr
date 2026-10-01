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

// This file covers the rest of item 6 of
// .docs/bugfixes/260930-dep-group-rerun-gaps.md: crash_mid_add_rerun_test.go's
// dependent is running when the add reads it, and here it is still ready or
// only reserved, so it is reserved, started and archived inside the add.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// TestCrashMidAddQueuedRerun proves a dependent that is ready, or reserved but
// not started, when an add to its dep group reads it, and that then runs and is
// archived during the add, is re-run after the new member once a manager that
// crashed after the add's write and before its reply is back.
func TestCrashMidAddQueuedRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, reserved := range []bool{false, true} {
		desc := "ready"
		if reserved {
			desc = "reserved and not started"
		}

		Convey("Given a "+desc+" job that depends on a dep group an add is adding a member to", t, func() {
			d, jq, waiter := cmqQueuedWaiter(ctx, reserved)

			defer d.stop(ctx)
			defer disconnect(jq)

			defer func() { dependentsReadHook, dependencyUpdatesHook = nil, nil }()

			second := dgaMemberJob(d, rdrGroup, rdrSecondName)

			finish := func(runner *Client) error {
				if !reserved {
					return rdrReserveAndComplete(runner, waiter.Key())
				}

				if err := runner.Started(waiter, os.Getpid()); err != nil {
					return err
				}

				return runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			}

			for _, archivedAfterWrite := range []bool{false, true} {
				Convey("if it runs and is archived "+cmrWhen(archivedAfterWrite)+
					" the add's write, and the manager crashes before the add replies, "+
					"it waits on the new member, then runs again", func() {
					client := cmrAddFinishingAndCrash(ctx, d, jq, second, finish, archivedAfterWrite)
					defer disconnect(client)

					rdrSoCompleteRecorded(d, waiter.Key())
					So(cmrJobState(client, second), ShouldEqual, JobStateReady)

					cmrSoRerunsAfterSecond(ctx, d, client, waiter, second)
				})
			}
		})
	}
}

// cmqQueuedWaiter starts a server holding a completed member of rdrGroup and a
// ready job depending on that group, returning the server, a client and the
// waiter. If reserved, the client has reserved the waiter, and the returned job
// is the reserved one.
func cmqQueuedWaiter(ctx context.Context, reserved bool) (*dgrServer, *Client, *Job) {
	d := dgrStartServer(ctx)
	jq := d.connect()

	member := dgaMemberJob(d, rdrGroup, rdrFirstName)
	waiter := dgaWaiterJob(d, rdrGroup, rdrWaiterName)
	waiter.Retries = rdrRetries

	dgrAddJobs(jq, []*Job{member})
	dgrAddJobs(jq, []*Job{waiter})

	dgaExecuteReserved(ctx, d, jq, member.Key())
	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

	if !reserved {
		return d, jq, waiter
	}

	got, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(got, ShouldNotBeNil)
	So(got.Key(), ShouldEqual, waiter.Key())
	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateRun)

	return d, jq, got
}
