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

// This file holds the log store forms of the tests that open their crash or
// race window by holding bbolt's write lock or writer, so are deleted with
// bbolt (spec F5): here a held sync holds the log's commit instead.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// logHeldHold is how long these tests hold the log's syncs.
	logHeldHold = time.Second

	// logHeldStdErr is the stderr of the job TestLogHeldBuriedStd buries.
	logHeldStdErr = "LOGHELDBURIEDSTDERR"

	// logHeldWrites is how many adds, archives and changed jobs
	// TestLogHeldCloseDrains has in flight when its store closes.
	logHeldWrites = 16
)

// errLogHeldStd is the stderr Client.Bury is given; it takes stderr as an
// error.
var errLogHeldStd = errors.New(logHeldStdErr)

// TestLogHeldArchiveStall proves that a job whose runner has reported success
// on the log store is not handed to another runner while that success waits
// on a stalled commit, however many TTRs the stall lasts, and that it then
// completes having run once.
func TestLogHeldArchiveStall(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job on a log store manager whose syncs can be held", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		config, _, _, _, _ := startDurabilityConfig(t)
		m := startReserveTestManager(t, ctx, storeKindLog, func(c *ServerConfig) {
			c.Timings.ItemTTR = archiveStallTTR
			c.Timings.TouchInterval = archiveStallTTR / 5
		})

		other := m.connect()

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")

		inserts, _, err := m.jq.Add([]*Job{{
			Cmd: startDurabilityCmd(marker, stopFile), Cwd: testCwd, RepGroup: archiveStallRepGroup,
			ReqGroup: archiveStallRepGroup, Requirements: m.reqs, Retries: 3,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := m.jq.Reserve(reserveTestWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		executed := make(chan error, 1)

		go func() { executed <- m.jq.Execute(ctx, reserved, config.RunnerExecShell) }()

		So(waitForRuns(marker, 1, reserveTestWait), ShouldBeTrue)
		So(waitForServerJob(m.server, reserved.Key(), reserveTestWait, func(job *Job) bool {
			return !job.StartTime.IsZero()
		}), ShouldBeTrue)

		Convey("when its success is stalled, no other runner is handed it, and it runs once", func() {
			until := syncs.holdFor(archiveStallHold)

			So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)
			So(waitForServerJob(m.server, reserved.Key(), reserveTestWait, func(job *Job) bool {
				return job.archivesPending == 1
			}), ShouldBeTrue)

			var rerun *Job

			for rerun == nil && time.Now().Before(until) {
				rerun, err = other.Reserve(archiveStallReserveWait)
				So(err, ShouldBeNil)
			}

			select {
			case errx := <-executed:
				So(errx, ShouldBeNil)
			case <-time.After(archiveStallExecuteWait):
				So("the run's Execute did not return", ShouldBeEmpty)
			}

			So(rerun, ShouldBeNil)
			So(runCount(marker), ShouldEqual, 1)

			got, errg := m.jq.GetByEssence(&JobEssence{JobKey: reserved.Key()}, false, false)
			So(errg, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, JobStateComplete)
		})
	})
}

// TestLogHeldLateStart proves that on the log store a start report that
// reaches the manager while its runner's archive is still waiting to commit is
// refused without waiting on the commit, and leaves the completed job alone.
func TestLogHeldLateStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const refusedWithin = time.Second

	ctx := context.Background()

	Convey("Given a log store manager with a runner's archive of a started job waiting on a held sync", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		m := startReserveTestManager(t, ctx, storeKindLog, nil)

		inserts, _, err := m.jq.Add([]*Job{{
			Cmd: "true logheld latestart", Cwd: testCwd, RepGroup: archiveBeforeStartRepGroup,
			ReqGroup: archiveBeforeStartRepGroup, Requirements: m.reqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := m.jq.Reserve(reserveTestWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(m.jq.Started(reserved, os.Getpid()), ShouldBeNil)

		startTime := reserved.StartTime
		endTime := time.Now()
		archived := make(chan error, 1)

		syncs.holdFor(logHeldHold + refusedWithin)

		go func() {
			archived <- m.jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: endTime})
		}()

		So(waitForServerJob(m.server, reserved.Key(), reserveTestWait, func(job *Job) bool {
			return job.archivesPending == 1
		}), ShouldBeTrue)

		late := m.connect()
		late.clientid = m.jq.clientid
		starting := time.Now()

		Convey("the start is refused without waiting on the commit, and the job completes as archived", func() {
			errStart := late.Started(reserved, os.Getpid())
			refused := time.Since(starting)

			var jqErr Error
			So(errors.As(errStart, &jqErr), ShouldBeTrue)
			So(jqErr.Err, ShouldEqual, ErrBadJob)
			So(refused, ShouldBeLessThan, refusedWithin)
			So(<-archived, ShouldBeNil)

			m.restart(ctx)

			done, errg := m.jq.GetByRepGroup(archiveBeforeStartRepGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(done, ShouldHaveLength, 1)
			So(done[0].State, ShouldEqual, JobStateComplete)
			So(done[0].EndTime.Equal(endTime), ShouldBeTrue)
			So(done[0].StartTime.Equal(startTime), ShouldBeTrue)
			So(done[0].Attempts, ShouldEqual, 1)

			again, errr := m.jq.Reserve(reserveTestBound)
			So(errr, ShouldBeNil)
			So(again, ShouldBeNil)
		})
	})
}

// TestLogHeldCloseDrains proves that the adds, archives and best-effort
// changes a log store has in flight, held off disk, when it is closed are all
// answered and durable once close returns, the changes with their latest
// state, so a resume or other change queued as a manager stops is kept.
func TestLogHeldCloseDrains(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose syncs are held, closed with adds, archives and changes in flight", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		dir := filepath.Join(t.TempDir(), "store")
		s := openLogStoreIn(ctx, dir)

		toArchive := make([]*Job, logHeldWrites)
		toAdd := make([]*Job, logHeldWrites)
		toChange := make([]*Job, logHeldWrites)

		for i := range logHeldWrites {
			toArchive[i] = testDBArchivedJob(fmt.Sprintf("echo held archive %d", i), "rg-held", time.Now())
			toAdd[i] = testDBJob(fmt.Sprintf("echo held add %d", i), "rg-held")
			toChange[i] = testDBJob(fmt.Sprintf("echo held change %d", i), "rg-held")
			toChange[i].State = JobStateReady
		}

		_, err := s.storeNewJobsGuarded(ctx, append(slices.Clone(toArchive), toChange...), false, nil)
		So(err, ShouldBeNil)

		syncs.holdFor(logHeldHold)

		errs := make([]error, 2*logHeldWrites)

		var wg sync.WaitGroup

		for i := range logHeldWrites {
			wg.Go(func() { _, errs[i] = s.archiveCompletion(toArchive[i].Key(), toArchive[i]) })
			wg.Go(func() { _, errs[logHeldWrites+i] = s.storeNewJobsGuarded(ctx, toAdd[i:i+1], false, nil) })
		}

		So(pollUntilFor(reserveTestWait, func() bool { return s.writing.Load() >= 2*logHeldWrites }), ShouldBeTrue)

		for _, state := range []JobState{JobStateReserved, JobStateReady, JobStateBuried} {
			change := func(job *Job) { job.State = state }

			for _, job := range toChange {
				s.queueJobChangeAhead(ctx, job, s.prepareJobChange(ctx, job, change), change)
			}
		}

		closeErr := s.close(ctx)

		wg.Wait()

		Convey("every write is answered with success, and is there after a reopen", func() {
			So(closeErr, ShouldBeNil)
			So(slices.ContainsFunc(errs, func(err error) bool { return err != nil }), ShouldBeFalse)

			reopened := openLogStoreIn(ctx, dir)

			defer func() { So(reopened.close(ctx), ShouldBeNil) }()

			wrong := 0

			for i := range logHeldWrites {
				_, live, errl := reopened.liveJob(toAdd[i].Key())
				if errl != nil || !live {
					wrong++
				}

				_, live, errl = reopened.liveJob(toArchive[i].Key())
				complete, errc := reopened.checkIfComplete(toArchive[i].Key())

				if errl != nil || errc != nil || live || !complete {
					wrong++
				}

				changed, live, errl := reopened.liveJob(toChange[i].Key())
				if errl != nil || !live || changed.State != JobStateBuried {
					wrong++
				}
			}

			So(wrong, ShouldEqual, 0)
		})
	})
}

// TestLogHeldArchiveBeatsChange proves that on the log store a change or exit
// of a job queued while its archive's commit is held does not bring the job
// back live once both are durable, so recovery does not run it again.
func TestLogHeldArchiveBeatsChange(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with 2 jobs whose archives are held off disk", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		s, _ := openTestLogStore(t, ctx)

		changed := testDBJob("echo held archived then changed", "rg-held-resurrect")
		exited := testDBJob("echo held archived then exited", "rg-held-resurrect")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{changed, exited}, false, nil)
		So(err, ShouldBeNil)

		syncs.holdFor(logHeldHold)

		archived := make(chan error, 2)

		for _, job := range []*Job{changed, exited} {
			job.State = JobStateComplete
			job.Exited = true
			job.EndTime = time.Now()

			go func() {
				_, erra := s.archiveCompletion(job.Key(), job)
				archived <- erra
			}()
		}

		So(pollUntilFor(reserveTestWait, func() bool { return s.writing.Load() >= 2 }), ShouldBeTrue)

		Convey("a change of one and an exit of the other queued behind them leave both archived and neither "+
			"live, also after a reopen", func() {
			reserve := func(job *Job) { job.State = JobStateReserved }
			s.queueJobChangeAhead(ctx, changed, s.prepareJobChange(ctx, changed, reserve), reserve)
			s.updateJobAfterExit(ctx, exited, []byte("stdout"), []byte("stderr"), true)

			So(<-archived, ShouldBeNil)
			So(<-archived, ShouldBeNil)
			So(s.flush(), ShouldBeNil)

			reopened := reopenLogStore(ctx, s)

			for _, job := range []*Job{changed, exited} {
				_, live, errl := reopened.liveJob(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeFalse)

				complete, errc := reopened.checkIfComplete(job.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)
			}

			recovered, _, errr := reopened.recoverIncompleteJobs()
			So(errr, ShouldBeNil)
			So(recovered, ShouldBeEmpty)
		})
	})
}

// TestLogHeldReleaseAndBury proves that a release or bury of a started job on
// the log store is acknowledged only once it is durable, so a crash straight
// after recovers the job released or buried, not running.
func TestLogHeldReleaseAndBury(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a started job on a log store manager whose syncs can be held", t, func() {
		pauseLogFolder()

		syncs := &reserveTestSyncs{}
		syncs.install()

		m, job := startedLogJob(t, ctx, releaseDurabilityRetries)

		Convey("a release after a failed attempt is acknowledged only once durable, and a crash then "+
			"recovers it released with its retry spent", func() {
			acked, crashed, err := heldReport(t, m, syncs, func() error {
				return m.jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit)
			})
			So(err, ShouldBeNil)
			So(acked, ShouldBeFalse)

			recovered, live := reopenedLogJob(ctx, crashed, job.Key())
			So(live, ShouldBeTrue)
			So(recovered.State, ShouldBeIn, []JobState{JobStateDelayed, JobStateReady})
			So(recovered.UntilBuried, ShouldEqual, releaseDurabilityRetries)
			So(recovered.Exitcode, ShouldEqual, releaseDurabilityExitCode)
		})

		Convey("a bury is acknowledged only once durable, and a crash then recovers it buried", func() {
			acked, crashed, err := heldReport(t, m, syncs, func() error {
				return m.jq.Bury(job, failedAttempt(), FailReasonExit)
			})
			So(err, ShouldBeNil)
			So(acked, ShouldBeFalse)

			recovered, live := reopenedLogJob(ctx, crashed, job.Key())
			So(live, ShouldBeTrue)
			So(recovered.State, ShouldEqual, JobStateBuried)
		})
	})
}

// TestLogHeldResentReport proves that a re-sent release or bury, which the
// manager has already applied, is not acknowledged while the log's syncs hold
// the job's state off disk.
func TestLogHeldResentReport(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job on a log store manager that a runner has released after a failed attempt", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		m, job := startedLogJob(t, ctx, releaseResendRetries)
		So(m.jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent release is not acknowledged while syncs are held, and spends no retry", func() {
			acked, _, err := heldReport(t, m, syncs, func() error {
				return m.jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit)
			})
			So(err, ShouldBeNil)
			So(acked, ShouldBeFalse)

			jobs, err := m.jq.GetByRepGroup(releaseDurabilityRepGroup, false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].UntilBuried, ShouldEqual, releaseResendRetries)
		})
	})

	Convey("Given a job on a log store manager that a runner has buried", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		m, job := startedLogJob(t, ctx, releaseDurabilityRetries)
		So(m.jq.Bury(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent bury is acknowledged, but not while syncs are held", func() {
			acked, _, err := heldReport(t, m, syncs, func() error {
				return m.jq.Bury(job, failedAttempt(), FailReasonExit)
			})
			So(err, ShouldBeNil)
			So(acked, ShouldBeFalse)
		})
	})
}

// TestLogHeldBuriedStd proves that a client that sees a job buried gets its
// stderr, even while the bury's write is held off disk: a job's item moves to
// bury before its stderr is durable, and holding the log's syncs parks the bury
// inside that window.
func TestLogHeldBuriedStd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const unwaitedFetch = time.Second

	ctx := context.Background()

	Convey("Given a started job on a log store manager being buried with some stderr while syncs are held", t,
		func() {
			syncs := &reserveTestSyncs{}
			syncs.install()

			m, job := startedLogJob(t, ctx, 0)

			syncs.holdFor(logHeldHold + unwaitedFetch)

			buried := make(chan error, 1)

			go func() {
				buried <- m.jq.Bury(job, &JobEndState{Exited: true, Exitcode: 12, EndTime: time.Now()},
					"log held std failed", errLogHeldStd)
			}()

			So(pollUntilFor(reserveTestWait, func() bool { return logHeldBuried(m.server, job.Key()) }),
				ShouldBeTrue)

			Convey("a client that sees it buried gets its stderr", func() {
				got := make(chan *Job, 1)
				fetcher := m.connect()

				go func() {
					fetched, errg := fetcher.GetByEssence(job.ToEssense(), true, false)
					if errg != nil {
						fetched = nil
					}

					got <- fetched
				}()

				var fetched *Job

				select {
				case fetched = <-got:
				case <-time.After(unwaitedFetch + logHeldHold + reserveTestWait):
				}

				So(<-buried, ShouldBeNil)
				So(fetched, ShouldNotBeNil)
				So(fetched.State, ShouldEqual, JobStateBuried)

				stderr, errs := fetched.StdErr()
				So(errs, ShouldBeNil)
				So(stderr, ShouldEqual, logHeldStdErr)
			})
		})
}

// startedLogJob starts a log store manager with a quick release delay, adds a
// job with retries, reserves it and reports it started with this test
// process's pid, returning the manager and the job.
func startedLogJob(t *testing.T, ctx context.Context, retries uint8) (*reserveTestManager, *Job) {
	t.Helper()

	m := startReserveTestManager(t, ctx, storeKindLog, func(c *ServerConfig) {
		c.Timings.ItemTTR = time.Minute
		c.Timings.ReleaseDelayMin = time.Millisecond
	})

	inserts, _, err := m.jq.Add([]*Job{{
		Cmd: fmt.Sprintf("true logheld %d", retries), Cwd: testCwd, RepGroup: releaseDurabilityRepGroup,
		ReqGroup: releaseDurabilityRepGroup, Requirements: m.reqs, Retries: retries,
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	reserved, err := m.jq.Reserve(reserveTestWait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(m.jq.Started(reserved, os.Getpid()), ShouldBeNil)

	return m, reserved
}

// logHeldBuried reports whether the manager has buried key's item and recorded
// the bury on its *Job, which is as far as a bury gets while its write is held.
func logHeldBuried(server *Server, key string) bool {
	item, err := server.q.Get(key)
	if err != nil || item.Stats().State != queue.ItemStateBury {
		return false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return false
	}

	job.RLock()
	defer job.RUnlock()

	return job.State == JobStateBuried
}

// heldReport calls report while every sync of m's log store is held for
// logHeldHold, and returns whether it returned while the syncs were held, a
// copy of what of the store was durable when it returned, and its error.
func heldReport(t *testing.T, m *reserveTestManager, syncs *reserveTestSyncs, report func() error) (bool,
	string, error,
) {
	t.Helper()

	until := syncs.holdFor(logHeldHold)
	done := make(chan error, 1)

	go func() { done <- report() }()

	select {
	case err := <-done:
		returned := time.Now()

		return returned.Before(until), durableLogCopy(t, m.logStore()), err
	case <-time.After(logHeldHold + reserveTestWait):
		So("the report did not return", ShouldBeEmpty)

		return false, "", nil
	}
}
