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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

// This file covers .docs/bugfixes/260928-archive-stall-rerun.md: a job whose
// command exited 0 ran again, with no manager crash, because its archive was
// waiting on a stalled database commit. The manager marks a job Exited when it
// accepts the runner's success report, before the archive's write transaction
// commits, and its TTR callback sent every Exited job in the run queue to the
// delay queue as if it had been released. The runner stops touching once its
// command ends, so a commit that stalled for longer than the TTR (the 1m52s one
// in prodsim-1790591410, when the database's filesystem filled up) let the job
// time out, go through delay to ready and be reserved by a fresh runner while
// the first run's archive was still queued.

const (
	// archiveStallTTR is the manager's ItemTTR in these tests: short, so the
	// stall below outlasts it several times over.
	archiveStallTTR = time.Second

	// archiveStallHold is how long the archive's write transaction is kept from
	// committing: several TTRs plus the delay, so a job the TTR callback lets go
	// has time to come back through ready and be reserved again.
	archiveStallHold = 5 * time.Second

	// archiveStallReserveWait is how long each attempt by the second runner to
	// reserve the job waits.
	archiveStallReserveWait = 200 * time.Millisecond

	// archiveStallExecuteWait bounds how long the first run's Execute is given to
	// return once the stall is released.
	archiveStallExecuteWait = 30 * time.Second

	// archiveStallRepGroup names the one job these tests add.
	archiveStallRepGroup = "archive_stall"
)

// TestArchiveStallDoesNotRerunJob proves that a job whose runner has reported
// success is not handed to another runner while that success is waiting on a
// stalled database commit, however many TTRs the stall lasts, and that the job
// then completes having run exactly once.
func TestArchiveStallDoesNotRerunJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job whose archive is stalled behind a held write transaction", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = archiveStallTTR
		serverConfig.Timings.TouchInterval = archiveStallTTR / 5
		serverConfig.Timings.ReserveWriteWait = 300 * time.Millisecond

		// the write held below must not stall the job's start: the runner settles
		// its start report before it sends its archive, so a start still waiting
		// on its write would keep the archive from arriving at all. The start
		// time is set in memory before that write, so only this hook says the
		// start has reached disk.
		startPersisted := make(chan struct{})

		var startPersistedOnce sync.Once

		startPersistedHook = func(string) { startPersistedOnce.Do(func() { close(startPersisted) }) }

		defer func() { startPersistedHook = nil }()

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		other, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(other)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")

		inserts, _, err := jq.Add([]*Job{{
			Cmd: startDurabilityCmd(marker, stopFile), Cwd: testCwd, RepGroup: archiveStallRepGroup,
			ReqGroup: archiveStallRepGroup, Requirements: standardReqs, Retries: 3,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		executed := make(chan error, 1)

		go func() { executed <- jq.Execute(ctx, reserved, config.RunnerExecShell) }()

		So(waitForRuns(marker, 1, startDurabilityAckWait), ShouldBeTrue)

		select {
		case <-startPersisted:
		case <-time.After(startDurabilityAckWait):
			So("the job's start was not persisted", ShouldBeEmpty)
		}

		holdTx, err := server.db.bolt.Begin(true)
		So(err, ShouldBeNil)

		// released by each case once its stall has done its work, and here on any
		// early exit, so a failed assertion cannot leave the server unable to stop.
		var releaseOnce sync.Once

		release := func() error {
			var errr error

			releaseOnce.Do(func() { errr = holdTx.Rollback() })

			return errr
		}

		defer func() { _ = release() }() //nolint:errcheck // only an early exit gets here unreleased

		So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)

		Convey("no other runner is handed the job during the stall, and it runs once", func() {
			var rerun *Job

			deadline := time.Now().Add(archiveStallHold)
			for rerun == nil && time.Now().Before(deadline) {
				rerun, err = other.Reserve(archiveStallReserveWait)
				So(err, ShouldBeNil)
			}

			So(release(), ShouldBeNil)

			// a second run is let go through to its command, so the marker shows
			// the double run the stall used to cause. Its error is not the verdict:
			// the first run's archive removing the job under it is what fails it.
			var rerunErr error
			if rerun != nil {
				rerunErr = other.Execute(ctx, rerun, config.RunnerExecShell)
			}

			select {
			case errx := <-executed:
				So(errx, ShouldBeNil)
			case <-time.After(archiveStallExecuteWait):
				So("the first run's Execute did not return", ShouldBeEmpty)
			}

			So(rerun != nil, ShouldBeFalse)
			So(rerunErr, ShouldBeNil)
			So(runCount(marker), ShouldEqual, 1)

			got, errg := jq.GetByEssence(&JobEssence{JobKey: reserved.Key()}, false, false)
			So(errg, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, JobStateComplete)
		})

		Convey("a repeat of the runner's archive during the stall also succeeds", func() {
			pending := func(n int) bool {
				return waitForServerJob(server, reserved.Key(), startDurabilityAckWait, func(job *Job) bool {
					return job.archivesPending == n
				})
			}

			So(pending(1), ShouldBeTrue)

			// the same report again from the same runner, as its client sends
			// after its first request times out on the stalled commit. It goes on
			// a second connection, since the first is still waiting on its reply.
			dup, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(dup)

			dup.clientid = jq.clientid
			repeated := make(chan error, 1)

			go func() {
				_, errr := dup.request(&clientRequest{
					Method: "jarchive", Keys: []string{reserved.Key()},
					JobEndState: &JobEndState{Exited: true, EndTime: time.Now()},
				})
				repeated <- errr
			}()

			So(pending(2), ShouldBeTrue)
			So(release(), ShouldBeNil)

			select {
			case errr := <-repeated:
				So(errr, ShouldBeNil)
			case <-time.After(archiveStallExecuteWait):
				So("the repeated archive did not return", ShouldBeEmpty)
			}

			select {
			case errx := <-executed:
				So(errx, ShouldBeNil)
			case <-time.After(archiveStallExecuteWait):
				So("the first run's Execute did not return", ShouldBeEmpty)
			}

			So(runCount(marker), ShouldEqual, 1)
		})
	})
}

// waitForServerJob reports whether the server's copy of the keyed job satisfies
// cond, which is called with the job read-locked, within timeout.
func waitForServerJob(server *Server, key string, timeout time.Duration, cond func(*Job) bool) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if serverJobMeets(server, key, cond) {
			return true
		}

		time.Sleep(startDurabilityPollTime)
	}

	return false
}

// serverJobMeets reports whether the server's copy of the keyed job exists and
// satisfies cond, which is called with the job read-locked.
func serverJobMeets(server *Server, key string, cond func(*Job) bool) bool {
	item, err := server.q.Get(key)
	if err != nil {
		return false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return false
	}

	job.RLock()
	defer job.RUnlock()

	return cond(job)
}

// TestKillLostRunLeavesPendingArchive proves that neither confirming a lost run
// dead nor a user's kill releases the job while a successful completion of it is
// being archived: the command has exited 0, so releasing it would run it again.
// It also proves that an archive whose write fails does not leave that hold in
// place, so the job cannot be stuck in the run queue for ever.
func TestKillLostRunLeavesPendingArchive(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a reserved job that the manager has declared lost", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo archive stall kill", Cwd: testCwd, RepGroup: archiveStallRepGroup,
			ReqGroup: archiveStallRepGroup, Requirements: standardReqs, Retries: 3,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		item, err := server.q.Get(reserved.Key())
		So(err, ShouldBeNil)

		job, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		lose := func(archivesPending int) pinnedBehaviours {
			job.Lock()
			defer job.Unlock()

			job.Lost = true
			job.archivesPending = archivesPending

			return job.pinBehavioursLocked()
		}

		Convey("killing it leaves it in the run queue while its archive is pending", func() {
			released, errk := server.killLostRun(ctx, lose(1))
			So(errk, ShouldBeNil)
			So(released, ShouldBeFalse)
			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)
		})

		Convey("killing it releases it when no archive is pending", func() {
			released, errk := server.killLostRun(ctx, lose(0))
			So(errk, ShouldBeNil)
			So(released, ShouldBeTrue)
			So(item.Stats().State, ShouldNotEqual, queue.ItemStateRun)
		})

		Convey("a user's kill neither marks nor releases it while its archive is pending", func() {
			lose(1)

			killed, errk := server.killJob(ctx, reserved.Key())
			So(errk, ShouldBeNil)
			So(killed, ShouldBeFalse)
			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)

			job.RLock()
			defer job.RUnlock()

			So(job.killCalled, ShouldBeFalse)
		})

		Convey("an archive whose write fails releases its hold, so a TTR expiry can release the job", func() {
			job.Lock()
			job.StartTime = time.Now()
			job.Unlock()

			archiveTxObserver = func(_ int, _ []byte) { panic("archive stall write failure test") }

			_, srerr, _ := server.handleArchive(ctx, &clientRequest{
				Method: "jarchive", Keys: []string{reserved.Key()}, ClientID: jq.clientid,
				JobEndState: &JobEndState{Exited: true, EndTime: time.Now()},
			})

			archiveTxObserver = nil

			So(srerr, ShouldEqual, ErrDBError)
			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)
			So(serverJobMeets(server, reserved.Key(), func(j *Job) bool {
				return j.Exited && !j.archivePendingLocked()
			}), ShouldBeTrue)
			So(server.ttrCallback(ctx, job), ShouldEqual, queue.SubQueueDelay)
		})
	})
}
