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

// This file covers item 6 of .docs/bugfixes/260930-dep-group-rerun-gaps.md: a
// manager that crashes after an add has written its new dep group member, but
// before the add has put back in the live bucket a dependent whose run was
// archived after the add read it, must still re-run that dependent after the
// new member, whether or not the client, which got no reply, retries the add.

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestCrashMidAddRerun proves a dependent archived during an add to its dep
// group, with the manager crashing after the add's write and before its reply,
// is re-run after the new member once the manager is back.
func TestCrashMidAddRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on a dep group an add is adding a member to", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() { dependentsReadHook, dependencyUpdatesHook = nil, nil }()

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)

		for _, archivedAfterWrite := range []bool{false, true} {
			Convey("if its run is archived "+cmrWhen(archivedAfterWrite)+
				" the add's write, and the manager crashes before the add replies", func() {
				client := cmrAddArchivingAndCrash(ctx, d, jq, waiter, second, archivedAfterWrite)
				defer disconnect(client)

				rdrSoCompleteRecorded(d, waiter.Key())
				So(cmrJobState(client, second), ShouldEqual, JobStateReady)

				Convey("a retry of the add makes it wait on the new member, then run again", func() {
					_, _, errr := client.Add([]*Job{second}, envVars, true)
					So(errr, ShouldBeNil)

					cmrSoRerunsAfterSecond(ctx, d, client, waiter, second)
				})

				Convey("recovery alone makes it wait on the new member, then run again", func() {
					cmrSoRerunsAfterSecond(ctx, d, client, waiter, second)
				})
			})
		}
	})
}

// cmrWhen describes when, relative to the add's write, the waiter is archived.
func cmrWhen(archivedAfterWrite bool) string {
	if archivedAfterWrite {
		return "after"
	}

	return "before"
}

// cmrAddArchivingAndCrash has jq add second while the running waiter's runner
// archives it, straight after the add read its dependents or, if
// archivedAfterWrite, straight after the add's write. It then replaces the
// manager with one recovering the database as committed after both that write
// and the archive, but before the add went on to handle the waiter, and returns
// a client of the new manager. The first manager's reply to the add stands for
// one the client never saw.
func cmrAddArchivingAndCrash(ctx context.Context, d *dgrServer, jq *Client, waiter, second *Job,
	archivedAfterWrite bool) *Client {
	archive := func(runner *Client) error {
		return runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
	}

	return cmrAddFinishingAndCrash(ctx, d, jq, second, archive, archivedAfterWrite)
}

// cmrAddFinishingAndCrash is cmrAddArchivingAndCrash with finish, given a
// client that is jq's runner, taking the waiter from wherever it is to archived.
func cmrAddFinishingAndCrash(ctx context.Context, d *dgrServer, jq *Client, second *Job,
	finish func(runner *Client) error, archivedAfterWrite bool) *Client {
	runner := rdrReconnect(d, jq)
	defer disconnect(runner)

	var archiveErr, backupErr error

	archive := func() {
		archiveErr = finish(runner)
	}

	crashImage := &bytes.Buffer{}

	dependentsReadHook = func() {
		dependentsReadHook = nil

		if !archivedAfterWrite {
			archive()
		}
	}

	dependencyUpdatesHook = func() {
		dependencyUpdatesHook = nil

		if archivedAfterWrite {
			archive()
		}

		backupErr = d.server.BackupDB(crashImage)
	}

	_, _, erra := jq.Add([]*Job{second}, envVars, true)
	So(erra, ShouldBeNil)
	So(archiveErr, ShouldBeNil)
	So(backupErr, ShouldBeNil)

	cmrCrashTo(ctx, d, crashImage)

	return d.connect()
}

// cmrCrashTo replaces the manager with one recovering the given crash image, as
// if the manager had been killed when the image was taken.
func cmrCrashTo(ctx context.Context, d *dgrServer, crashImage *bytes.Buffer) {
	d.server.Stop(ctx, true)

	So(os.WriteFile(d.serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

	d.serverConfig.dontWipeDevDB = true

	server, _, token, err := serve(ctx, d.serverConfig)
	So(err, ShouldBeNil)

	d.server, d.token = server, token

	So(waitUntilRecovered(d.server), ShouldBeTrue)
}

// cmrJobState returns the state the manager reports for the job.
func cmrJobState(client *Client, job *Job) JobState {
	got, err := client.GetByEssence(&JobEssence{JobKey: job.Key()}, false, false)
	So(err, ShouldBeNil)
	So(got, ShouldNotBeNil)

	return got.State
}

// cmrSoRerunsAfterSecond asserts the waiter is dependent on second, becomes
// ready once second completes, and then runs to completion again.
func cmrSoRerunsAfterSecond(ctx context.Context, d *dgrServer, client *Client, waiter, second *Job) {
	So(cmrJobState(client, waiter), ShouldEqual, JobStateDependent)

	dgaExecuteReserved(ctx, d, client, second.Key())
	So(cmrJobState(client, waiter), ShouldEqual, JobStateReady)

	rdrExecuteAsRunner(ctx, d, client, waiter.Key())
	So(cmrJobState(client, waiter), ShouldEqual, JobStateComplete)
}
