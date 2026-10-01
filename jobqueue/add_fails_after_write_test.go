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

// This file covers item 9 of .docs/bugfixes/260930-dep-group-rerun-gaps.md: an
// add of a new dep group member that fails after its write committed, having
// put back in the live bucket (or kept there) a dependent whose run was
// archived after the add read it, must not stop the client's retry of the add
// from making that dependent wait on the new member and run again.

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// errAFAWInjected is the error newJobsStoredErrHook fails the add with.
var errAFAWInjected = errors.New("injected failure after the add's write")

// TestAddFailsAfterWrite proves a dependent archived during an add to its dep
// group, where the add fails after its write, is re-run after the new member
// once the client retries the add.
func TestAddFailsAfterWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on a dep group an add is adding a member to", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() { dependentsReadHook, newJobsStoredErrHook = nil, nil }()

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)

		for _, archivedAfterWrite := range []bool{false, true} {
			when := "before"
			if archivedAfterWrite {
				when = "after"
			}

			Convey("if its run is archived "+when+" the add's write, and the add then fails", func() {
				afawAddArchivingAndFail(d, jq, waiter, archivedAfterWrite)

				rdrSoCompleteRecorded(d, waiter.Key())

				Convey("a retry of the add makes it wait on the new member, then run again", func() {
					_, _, errr := jq.Add([]*Job{second}, envVars, true)
					So(errr, ShouldBeNil)

					cmrSoRerunsAfterSecond(ctx, d, jq, waiter, second)
				})
			})
		}
	})
}

// afawAddArchivingAndFail has jq add second while the running waiter's runner
// archives it, straight after the add read its dependents or, if
// archivedAfterWrite, straight after the add's write, and has the add fail once
// its write has committed.
func afawAddArchivingAndFail(d *dgrServer, jq *Client, waiter *Job, archivedAfterWrite bool) {
	runner := rdrReconnect(d, jq)
	defer disconnect(runner)

	var archiveErr error

	archive := func() {
		archiveErr = runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
	}

	dependentsReadHook = func() {
		dependentsReadHook = nil

		if !archivedAfterWrite {
			archive()
		}
	}

	newJobsStoredErrHook = func() error {
		newJobsStoredErrHook = nil

		if archivedAfterWrite {
			archive()
		}

		return errAFAWInjected
	}

	_, _, erra := jq.Add([]*Job{dgaMemberJob(d, rdrGroup, rdrSecondName)}, envVars, true)
	So(erra, ShouldNotBeNil)
	So(archiveErr, ShouldBeNil)
}
