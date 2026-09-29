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

// This file covers .docs/bugfixes/260929-running-dependent-rerun.md: a job
// running while one of the dep groups it depends on gains a new member is left
// to finish, its completion is accepted and recorded, and it then waits on the
// new member and runs again, as a complete dependent would.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	rdrGroup      = "running-dependent-rerun-group"
	rdrFirstName  = "first"
	rdrSecondName = "second"
	rdrWaiterName = "waiter"

	// rdrRetries lets the waiter be released without being buried.
	rdrRetries = 3
)

// errRDRWrongJob is rdrReserveAndComplete's error for reserving some other job.
var errRDRWrongJob = errors.New("did not reserve the expected job")

// TestRunningDependentRerun proves that a running job whose dep group gains a
// member is not clobbered: it keeps running, its archive is accepted, and it is
// then re-run after the new member.
func TestRunningDependentRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job running while the dep group it depends on gains a member", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		dgrAddJobs(jq, []*Job{second})

		Convey("it is still running, and after it finishes it waits on the new member and runs again", func() {
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateRun)

			killCalled, errt := jq.Touch(waiter)
			So(errt, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			rdrSoFinishesThenReruns(ctx, d, jq, jq, waiter, second)
		})

		Convey("a manager restart before it finishes still re-runs it after the new member", func() {
			clientID := jq.clientid

			d.restart(ctx)

			runner := d.connect()
			defer disconnect(runner)

			runner.clientid = clientID

			rdrSoFinishesThenReruns(ctx, d, runner, runner, waiter, second)
		})

		Convey("a crash before it finishes still re-runs it after the new member", func() {
			rdrCrash(ctx, d, func() {})

			runner := rdrReconnect(d, jq)
			defer disconnect(runner)

			rdrSoFinishesThenReruns(ctx, d, runner, runner, waiter, second)
		})

		Convey("a crash before the add stored its mark still re-runs it after the new member", func() {
			rdrCrash(ctx, d, func() { rdrSetStoredMark(d, waiter.Key(), false) })

			runner := rdrReconnect(d, jq)
			defer disconnect(runner)

			rdrSoFinishesThenReruns(ctx, d, runner, runner, waiter, second)
		})

		Convey("if the new member completes first, it still runs again once it finishes, across a restart", func() {
			dgaExecuteReserved(ctx, d, jq, second.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateRun)

			rdrCrash(ctx, d, func() {})

			runner := rdrReconnect(d, jq)
			defer disconnect(runner)

			So(runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
			rdrSoCompleteRecorded(d, waiter.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

			rdrExecuteAsRunner(ctx, d, runner, waiter.Key())

			_, errg := d.server.q.Get(waiter.Key())
			So(errg, ShouldNotBeNil)
		})

		Convey("two archives of its one completion in flight at once requeue it once, when both are done", func() {
			item, err := d.server.q.Get(waiter.Key())
			So(err, ShouldBeNil)

			serverJob, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			end := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}

			key, repGroup, schedGroup, srerr := markJobComplete(serverJob, end, d.server.limiter, jq.clientid)
			So(srerr, ShouldBeEmpty)

			_, _, _, srerr = markJobComplete(serverJob, end, d.server.limiter, jq.clientid)
			So(srerr, ShouldBeEmpty)

			_, srerr, qerr := d.server.archiveCompletedJob(ctx, serverJob, key, repGroup, schedGroup)
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateRun)

			_, srerr, qerr = d.server.archiveCompletedJob(ctx, serverJob, key, repGroup, schedGroup)
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)

			rdrSoCompleteRecorded(d, waiter.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			So(jq.Archive(waiter, end), ShouldBeNil)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			dgaExecuteReserved(ctx, d, jq, waiter.Key())

			_, errg := d.server.q.Get(waiter.Key())
			So(errg, ShouldNotBeNil)
		})

		Convey("if it fails with retries left, it waits on the new member before it retries", func() {
			So(jq.Release(waiter, rdrFailedEnd(), "failed"), ShouldBeNil)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

			dgaExecuteReserved(ctx, d, jq, waiter.Key())

			_, errg := d.server.q.Get(waiter.Key())
			So(errg, ShouldNotBeNil)
		})

		Convey("if it is lost and then confirmed dead, it waits on the new member before it retries", func() {
			item, err := d.server.q.Get(waiter.Key())
			So(err, ShouldBeNil)

			serverJob, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			// what the manager's TTR expiry does to a running job whose runner
			// stopped touching it.
			serverJob.Lock()
			serverJob.Lost = true
			serverJob.Unlock()

			killed, errk := jq.Kill([]*JobEssence{waiter.ToEssense()})
			So(errk, ShouldBeNil)
			So(killed, ShouldEqual, 1)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

			dgaExecuteReserved(ctx, d, jq, waiter.Key())

			_, errg := d.server.q.Get(waiter.Key())
			So(errg, ShouldNotBeNil)
		})

		Convey("if it is buried, it stays buried, and a kick makes it wait on the new member", func() {
			So(jq.Bury(waiter, rdrFailedEnd(), "failed"), ShouldBeNil)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateBury)

			kicked, errk := jq.Kick([]*JobEssence{waiter.ToEssense()})
			So(errk, ShouldBeNil)
			So(kicked, ShouldEqual, 1)
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
		})
	})

	Convey("Given a running job whose success was accepted just before its dep group gained a member", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		item, err := d.server.q.Get(waiter.Key())
		So(err, ShouldBeNil)

		serverJob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		key, repGroup, schedGroup, srerr := markJobComplete(serverJob,
			&JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}, d.server.limiter, jq.clientid)
		So(srerr, ShouldBeEmpty)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)
		dgrAddJobs(jq, []*Job{second})

		Convey("once that archive finishes, it waits on the new member and runs again", func() {
			_, srerr, qerr := d.server.archiveCompletedJob(ctx, serverJob, key, repGroup, schedGroup)
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)

			rdrSoCompleteRecorded(d, waiter.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

			dgaExecuteReserved(ctx, d, jq, second.Key())
			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

			d.restart(ctx)

			runner := d.connect()
			defer disconnect(runner)

			So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)
			rdrExecuteAsRunner(ctx, d, runner, waiter.Key())

			_, errg := d.server.q.Get(waiter.Key())
			So(errg, ShouldNotBeNil)
		})
	})
}

// TestRunningDependentRerunRaces proves a running job whose run ends while an
// add to its dep group is under way is still re-run, even if the manager then
// crashes before its in-memory handling of that has finished.
//
// The cases that call db.archiveJob directly, after markJobComplete, are only
// for the crash image they leave: the archive's transaction is written but the
// in-memory half of the archive (the queue removal and what follows) never runs,
// as when the manager dies between the two, and each such case crashes the
// manager straight after.
func TestRunningDependentRerunRaces(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job whose run ends while an add to its dep group is under way", t, func() {
		d, jq, waiter := rdrRunningWaiter(ctx)

		defer d.stop(ctx)
		defer disconnect(jq)

		defer func() { dependentsReadHook, dependencyUpdatesHook = nil, nil }()

		item, err := d.server.q.Get(waiter.Key())
		So(err, ShouldBeNil)

		serverJob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		success := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		second := dgaMemberJob(d, rdrGroup, rdrSecondName)

		// soArchivedInHookReruns has the waiter's runner archive it from the hook
		// set by setHook, then adds second, and asserts the waiter runs again.
		soArchivedInHookReruns := func(setHook func(func())) {
			runner := rdrReconnect(d, jq)
			defer disconnect(runner)

			var archiveErr error

			setHook(func() { archiveErr = runner.Archive(waiter, success) })

			// the waiter it re-runs is counted as added, as an archived dependent
			// an add resurrects always has been.
			inserts, _, erra := jq.Add([]*Job{second}, envVars, true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(archiveErr, ShouldBeNil)

			rdrSoRerunsAfterCrash(ctx, d, waiter.Key(), second.Key())
		}

		Convey("if it is archived and leaves the queue after the add read it, it still runs again", func() {
			soArchivedInHookReruns(func(archive func()) {
				dependentsReadHook = func() {
					dependentsReadHook = nil

					archive()
				}
			})
		})

		Convey("if it is archived and leaves the queue after the add's write, it still runs again", func() {
			soArchivedInHookReruns(func(archive func()) {
				dependencyUpdatesHook = func() {
					dependencyUpdatesHook = nil

					archive()
				}
			})
		})

		Convey("if its archive is written after the add read it, a crash before it is queued again loses nothing", func() {
			var archiveErr error

			dependentsReadHook = func() {
				dependentsReadHook = nil

				_, _, _, srerr := markJobComplete(serverJob, success, d.server.limiter, jq.clientid)
				if srerr == "" {
					archiveErr = d.server.db.archiveJob(waiter.Key(), serverJob)
				}
			}

			dgrAddJobs(jq, []*Job{second})
			So(archiveErr, ShouldBeNil)

			rdrSoRerunsAfterCrash(ctx, d, waiter.Key(), second.Key())
		})

		Convey("if its archive is accepted before the add but written after it, that write keeps it live", func() {
			_, _, _, srerr := markJobComplete(serverJob, success, d.server.limiter, jq.clientid)
			So(srerr, ShouldBeEmpty)

			dgrAddJobs(jq, []*Job{second})

			So(d.server.db.archiveJob(waiter.Key(), serverJob), ShouldBeNil)

			rdrSoRerunsAfterCrash(ctx, d, waiter.Key(), second.Key())
		})
	})
}

// rdrRunningWaiter starts a server holding a completed member of rdrGroup and a
// job depending on that group which a runner (the returned client) has reserved
// and started, returning the server, the client and the reserved waiter.
func rdrRunningWaiter(ctx context.Context) (*dgrServer, *Client, *Job) {
	d := dgrStartServer(ctx)
	jq := d.connect()

	member := dgaMemberJob(d, rdrGroup, rdrFirstName)
	waiter := dgaWaiterJob(d, rdrGroup, rdrWaiterName)
	waiter.Retries = rdrRetries

	dgrAddJobs(jq, []*Job{member})
	dgrAddJobs(jq, []*Job{waiter})

	dgaExecuteReserved(ctx, d, jq, member.Key())

	reserved, err := jq.Reserve(dgrReserveWait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, waiter.Key())
	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateRun)

	return d, jq, reserved
}

// rdrSoFinishesThenReruns archives the running waiter successfully and asserts
// the archive was accepted and recorded, that the waiter then waits on the new
// member, and that it runs again once that member completes.
func rdrSoFinishesThenReruns(ctx context.Context, d *dgrServer, runner, jq *Client, waiter *Job, second *Job) {
	So(runner.Archive(waiter, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

	complete, err := d.server.db.retrieveCompleteJobsByKeys([]string{waiter.Key()})
	So(err, ShouldBeNil)
	So(complete, ShouldHaveLength, 1)
	So(complete[0].Exited, ShouldBeTrue)
	So(complete[0].Exitcode, ShouldEqual, 0)

	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateDependent)

	dgaExecuteReserved(ctx, d, jq, second.Key())

	So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

	rdrExecuteAsRunner(ctx, d, jq, waiter.Key())

	_, errg := d.server.q.Get(waiter.Key())
	So(errg, ShouldNotBeNil)
}

// TestRunningDependentRerunStartedDuringAdd proves a job that is waiting to run
// when an add to its dep group reads it, and then runs to completion before the
// add has applied its new dependencies, still runs again after the new member.
func TestRunningDependentRerunStartedDuringAdd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a ready job whose run completes while an add to its dep group is under way", t, func() {
		d := dgrStartServer(ctx)
		jq := d.connect()

		defer d.stop(ctx)
		defer disconnect(jq)

		runner := d.connect()
		defer disconnect(runner)

		defer func() { dependentsReadHook, dependencyUpdatesHook = nil, nil }()

		member := dgaMemberJob(d, rdrGroup, rdrFirstName)
		waiter := dgaWaiterJob(d, rdrGroup, rdrWaiterName)

		dgrAddJobs(jq, []*Job{member})
		dgrAddJobs(jq, []*Job{waiter})
		dgaExecuteReserved(ctx, d, jq, member.Key())
		So(dgaItemState(d.server, waiter.Key()), ShouldEqual, queue.ItemStateReady)

		second := dgaMemberJob(d, rdrGroup, rdrSecondName)

		var runErr error

		completeWaiter := func() { runErr = rdrReserveAndComplete(runner, waiter.Key()) }

		// the waiter it re-runs is counted as added, as an archived dependent an
		// add resurrects always has been.
		soAddRerunsWaiter := func() {
			inserts, _, erra := jq.Add([]*Job{second}, envVars, true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(runErr, ShouldBeNil)

			rdrSoRerunsAfterCrash(ctx, d, waiter.Key(), second.Key())
		}

		Convey("if it completes after the add read it but before the add's write, it runs again", func() {
			dependentsReadHook = func() {
				dependentsReadHook = nil

				completeWaiter()
			}

			soAddRerunsWaiter()
		})

		Convey("if it completes after the add's write but before its dependencies are updated, it runs again", func() {
			dependencyUpdatesHook = func() {
				dependencyUpdatesHook = nil

				completeWaiter()
			}

			soAddRerunsWaiter()
		})
	})
}

// rdrReserveAndComplete has runner reserve the keyed ready job, report it
// started, and archive it as a success, returning the first error. It uses no
// So(), so a hook can call it from the manager's goroutine.
func rdrReserveAndComplete(runner *Client, key string) error {
	job, err := runner.Reserve(dgrReserveWait)
	if err != nil {
		return err
	}

	if job == nil || job.Key() != key {
		return errRDRWrongJob
	}

	if err = runner.Started(job, os.Getpid()); err != nil {
		return err
	}

	return runner.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
}

// rdrReconnect returns a client of the current manager that is the runner that
// reserved the job before the manager was replaced.
func rdrReconnect(d *dgrServer, runner *Client) *Client {
	reconnected := d.connect()
	reconnected.clientid = runner.clientid

	return reconnected
}

// rdrSoRerunsAfterCrash asserts the waiter's success was recorded, crashes the
// manager at once, and asserts that it recovers the waiter complete and waiting
// to run again after second, and runs it again once second completes.
func rdrSoRerunsAfterCrash(ctx context.Context, d *dgrServer, waiterKey, secondKey string) {
	rdrSoCompleteRecorded(d, waiterKey)

	rdrCrash(ctx, d, func() {})

	runner := d.connect()
	defer disconnect(runner)

	rdrSoCompleteRecorded(d, waiterKey)
	So(dgaItemState(d.server, waiterKey), ShouldEqual, queue.ItemStateDependent)

	dgaExecuteReserved(ctx, d, runner, secondKey)
	So(dgaItemState(d.server, waiterKey), ShouldEqual, queue.ItemStateReady)

	rdrExecuteAsRunner(ctx, d, runner, waiterKey)

	_, errg := d.server.q.Get(waiterKey)
	So(errg, ShouldNotBeNil)
}

// rdrSetStoredMark rewrites the job's live record with or without the mark that
// it must run again, asserting it had the other. Without it is what a crash
// between an add's commit and its storing the mark would leave.
func rdrSetStoredMark(d *dgrServer, key string, mark bool) {
	err := d.server.db.bolt.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketJobsLive)

		job, errd := d.server.db.decodeJob(bucket.Get([]byte(key)))
		if errd != nil {
			return errd
		}

		So(job.RerunAfterRun, ShouldEqual, !mark)

		job.RerunAfterRun = mark

		var encoded []byte
		if erre := codec.NewEncoderBytes(&encoded, d.server.db.ch).Encode(job); erre != nil {
			return erre
		}

		return bucket.Put([]byte(key), encoded)
	})
	So(err, ShouldBeNil)
}

// rdrSoCompleteRecorded asserts the job's successful completion is in the
// complete bucket.
func rdrSoCompleteRecorded(d *dgrServer, key string) {
	complete, err := d.server.db.retrieveCompleteJobsByKeys([]string{key})
	So(err, ShouldBeNil)
	So(complete, ShouldHaveLength, 1)
	So(complete[0].Exited, ShouldBeTrue)
	So(complete[0].Exitcode, ShouldEqual, 0)
}

// rdrCrash replaces the manager with one recovering the database as it was
// committed at this moment, as if the manager had been killed, after first
// letting alterImage change what was committed.
func rdrCrash(ctx context.Context, d *dgrServer, alterImage func()) {
	alterImage()

	crashImage := &bytes.Buffer{}
	So(d.server.BackupDB(crashImage), ShouldBeNil)

	d.server.Stop(ctx, true)

	So(os.WriteFile(d.serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

	d.serverConfig.dontWipeDevDB = true

	server, _, token, err := serve(ctx, d.serverConfig)
	So(err, ShouldBeNil)

	d.server, d.token = server, token

	So(waitUntilRecovered(d.server), ShouldBeTrue)
}

// rdrExecuteAsRunner reserves the named ready job the way a runner for its
// scheduler group would, and runs it to completion. A job recovered into the
// run sub-queue keeps the scheduler group recovery gave it, which a group-less
// reserve does not pop, and this server has no runner command to give it
// another.
func rdrExecuteAsRunner(ctx context.Context, d *dgrServer, jq *Client, key string) {
	item, err := d.server.q.Get(key)
	So(err, ShouldBeNil)

	job, err := jq.ReserveScheduled(dgrReserveWait, item.ReserveGroup)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(job.Key(), ShouldEqual, key)

	execute(ctx, jq, job, d.config.RunnerExecShell)
}

// rdrFailedEnd is the end state of a run whose command failed.
func rdrFailedEnd() *JobEndState {
	return &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}
}
