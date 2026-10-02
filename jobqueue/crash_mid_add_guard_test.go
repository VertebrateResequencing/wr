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

// This file covers item 12 of .docs/bugfixes/260930-dep-group-rerun-gaps.md:
// an add's write looks up only the dependents whose archive may have taken them
// out of the live bucket (see rerunGuard.atRiskKeys), so it must still find one
// whose archive committed after the add read it but before the add guarded it,
// whether or not the archive has yet removed its queue item, and one whose
// archive's transaction was open when the add started.

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// cmgHoldLimit bounds how long an archive's transaction is held open for an add
// to read its dependents, so a test that has gone wrong fails rather than hangs.
const cmgHoldLimit = 30 * time.Second

// TestRerunGuardKeepsOnlyDependentsLive proves an archive that commits after an
// add's write, while the add guards the live dependents it read, still takes a
// job that is not one of them out of the live bucket.
func TestRerunGuardKeepsOnlyDependentsLive(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on a dep group, and a running job that does not", t, func() {
		d, jq, _ := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() { dependencyUpdatesHook = nil }()

		unrelated := d.job("echo cmg unrelated", dgaMemberRepGroup)
		dgrAddJobs(jq, []*Job{unrelated})

		reserved, err := jq.Reserve(dgrReserveWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, unrelated.Key())
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		Convey("archiving the unrelated job after an add to the group has written leaves it complete only", func() {
			runner := rdrReconnect(d, jq)
			defer disconnect(runner)

			var archiveErr error

			dependencyUpdatesHook = func() {
				dependencyUpdatesHook = nil
				archiveErr = runner.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			}

			dgrAddJobs(jq, []*Job{dgaMemberJob(d, rdrGroup, rdrSecondName)})
			So(archiveErr, ShouldBeNil)

			rdrSoCompleteRecorded(d, unrelated.Key())
			So(len(d.server.db.retrieve(ctx, bucketJobsLive, unrelated.Key())), ShouldEqual, 0)
		})
	})
}

// TestCrashMidAddArchivedBeforeGuard proves a dependent archived between an
// add to its dep group reading it and guarding it, with the manager crashing
// after the add's write and before its reply, is re-run after the new member
// once the manager is back.
func TestCrashMidAddArchivedBeforeGuard(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on a dep group an add is adding a member to", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() {
			dependentsGuardHook, dependencyUpdatesHook, archiveCommittedHook = nil, nil, nil
		}()

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)

		for _, itemRemoved := range []bool{true, false} {
			Convey("if its archive commits before the add guards it, "+cmgItemWhen(itemRemoved)+
				", and the manager crashes before the add replies, it waits on the new member, then runs again", func() {
				client := cmgAddArchivedBeforeGuardAndCrash(ctx, d, jq, waiter, second, itemRemoved)
				defer disconnect(client)

				rdrSoCompleteRecorded(d, waiter.Key())
				So(cmrJobState(client, second), ShouldEqual, JobStateReady)

				cmrSoRerunsAfterSecond(ctx, d, client, waiter, second)
			})
		}
	})
}

// cmgItemWhen describes whether the waiter's archive has removed its queue item
// by the time the add guards it.
func cmgItemWhen(itemRemoved bool) string {
	if itemRemoved {
		return "having removed its queue item"
	}

	return "with its queue item not yet removed"
}

// cmgAddArchivedBeforeGuardAndCrash has jq add second while the running
// waiter's runner archives it, the archive committing after the add read its
// dependents and before it guards them. If itemRemoved, the archive has also
// removed the waiter's queue item by then; otherwise it is held before doing so
// until the add's write has committed. It then replaces the manager with one
// recovering the database as committed after that write, but before the add
// went on to handle the waiter, and returns a client of the new manager.
func cmgAddArchivedBeforeGuardAndCrash(ctx context.Context, d *dgrServer, jq *Client, waiter, second *Job,
	itemRemoved bool) *Client {
	runner := rdrReconnect(d, jq)
	defer disconnect(runner)

	var archiveErr, backupErr error

	archived := make(chan error, 1)
	committed := make(chan struct{})
	finish := make(chan struct{})

	if !itemRemoved {
		archiveCommittedHook = func(key string) {
			if key != waiter.Key() {
				return
			}

			close(committed)
			<-finish
		}
	}

	dependentsGuardHook = func() {
		dependentsGuardHook = nil

		go func() {
			archived <- runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		}()

		if itemRemoved {
			archiveErr = <-archived

			return
		}

		<-committed
	}

	crashImage := &bytes.Buffer{}

	dependencyUpdatesHook = func() {
		dependencyUpdatesHook = nil
		backupErr = d.server.BackupDB(crashImage)

		close(finish)
	}

	_, _, erra := jq.Add([]*Job{second}, envVars, true)
	So(erra, ShouldBeNil)

	if !itemRemoved {
		archiveErr = <-archived
	}

	So(archiveErr, ShouldBeNil)
	So(backupErr, ShouldBeNil)

	archiveCommittedHook = nil

	cmrCrashTo(ctx, d, crashImage)

	return d.connect()
}

// TestCrashMidAddArchiveOpenAtStart proves a dependent whose archive's
// transaction had looked for adds in flight before an add to its dep group
// started, and commits after the add read it, with the manager crashing after
// the add's write and before its reply, is re-run after the new member once the
// manager is back.
func TestCrashMidAddArchiveOpenAtStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job that depends on a dep group an add is adding a member to", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() {
			dependentsGuardHook, dependencyUpdatesHook, archiveGuardsSeenHook = nil, nil, nil
		}()

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)

		Convey("if its archive's transaction is open when the add starts and commits after the add read it, "+
			"and the manager crashes before the add replies, it waits on the new member, then runs again", func() {
			client := cmgAddDuringOpenArchiveAndCrash(ctx, d, jq, waiter, second)
			defer disconnect(client)

			rdrSoCompleteRecorded(d, waiter.Key())
			So(cmrJobState(client, second), ShouldEqual, JobStateReady)

			cmrSoRerunsAfterSecond(ctx, d, client, waiter, second)
		})
	})
}

// cmgAddDuringOpenArchiveAndCrash has the running waiter's runner archive it,
// holding the archive's transaction open once it has looked for adds in flight
// while jq adds second, until the add has read its dependents. It then replaces
// the manager with one recovering the database as committed after the add's
// write, but before the add went on to handle the waiter, and returns a client
// of the new manager.
func cmgAddDuringOpenArchiveAndCrash(ctx context.Context, d *dgrServer, jq *Client, waiter, second *Job) *Client {
	runner := rdrReconnect(d, jq)
	defer disconnect(runner)

	seen := make(chan struct{})
	read := make(chan struct{})

	var heldUntilRead bool

	archiveGuardsSeenHook = func(key []byte) {
		if string(key) != waiter.Key() {
			return
		}

		close(seen)

		select {
		case <-read:
			heldUntilRead = true
		case <-time.After(cmgHoldLimit):
		}
	}

	archived := make(chan error, 1)

	go func() {
		archived <- runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
	}()

	<-seen

	dependentsGuardHook = func() {
		dependentsGuardHook = nil

		close(read)
	}

	crashImage := &bytes.Buffer{}

	var backupErr error

	dependencyUpdatesHook = func() {
		dependencyUpdatesHook = nil
		backupErr = d.server.BackupDB(crashImage)
	}

	_, _, erra := jq.Add([]*Job{second}, envVars, true)
	So(erra, ShouldBeNil)
	So(<-archived, ShouldBeNil)
	So(heldUntilRead, ShouldBeTrue)
	So(backupErr, ShouldBeNil)

	archiveGuardsSeenHook = nil

	cmrCrashTo(ctx, d, crashImage)

	return d.connect()
}
