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

// Tests of the log store's transitions, reads, recovery base and stats, beyond
// C2's acceptance tests (store_log_add_test.go).

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

// logStoreStatsWait bounds the wait for the log writer's first "db writer"
// line: many report intervals of storeStatsTestInterval.
const logStoreStatsWait = 2 * time.Second

func TestLogStoreInspector(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with 3 jobs added and 1 of them archived", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("inspect", "inspect", 3)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		archived := jobs[0]
		archived.State = JobStateComplete
		archived.Exited = true
		archived.EndTime = time.Now()
		outcome, err := s.archiveCompletion(archived.Key(), archived)
		So(err, ShouldBeNil)
		So(outcome, ShouldEqual, archiveRemovedLive)
		So(s.flush(), ShouldBeNil)

		check := func(st *logStore) {
			So(inspectedCount(st, kindLive), ShouldEqual, 2)
			So(inspectedCount(st, kindComplete), ShouldEqual, 1)

			_, live, errl := st.liveJob(archived.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeFalse)

			complete, found, errc := st.completeJob(archived.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(complete.Cmd, ShouldEqual, archived.Cmd)
			So(complete.State, ShouldEqual, JobStateComplete)

			_, found, errc = st.completeJob(jobs[1].Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeFalse)

			isComplete, errc := st.checkIfComplete(archived.Key())
			So(errc, ShouldBeNil)
			So(isComplete, ShouldBeTrue)
		}

		Convey("it counts 2 live and 1 complete, before and after reopen", func() {
			check(s)
			check(reopenLogStore(ctx, s))
		})

		Convey("archiving the same completion again writes nothing", func() {
			outcome, err = s.archiveCompletion(archived.Key(), archived)
			So(err, ShouldBeNil)
			So(outcome, ShouldEqual, archiveAlreadyWritten)
		})

		Convey("it makes no backup yet", func() {
			So(s.forceBackup(), ShouldWrap, errLogStoreBackup)
		})
	})

	Convey("Given a log store, a job that exited 1 with both std streams and one with only stdout", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo inspect std", "inspect")
		outOnly := testDBJob("echo inspect stdout only", "inspect")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job, outOnly}, false, nil)
		So(err, ShouldBeNil)

		for _, failed := range []*Job{job, outOnly} {
			failed.State = JobStateBuried
			failed.Exited = true
			failed.Exitcode = 1
		}

		So(s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false), ShouldBeNil)
		s.updateJobAfterExit(ctx, outOnly, []byte("o2"), nil, false)

		check := func(st *logStore) {
			So(st.flush(), ShouldBeNil)
			So(inspectedCount(st, kindStdOut), ShouldEqual, 2)
			So(inspectedCount(st, kindStdErr), ShouldEqual, 1)

			stdo, stde := st.retrieveJobStd(ctx, job.Key())
			So(string(stdo), ShouldEqual, "o")
			So(string(stde), ShouldEqual, "e")

			stdo, stde = st.retrieveJobStd(ctx, outOnly.Key())
			So(string(stdo), ShouldEqual, "o2")
			So(stde, ShouldBeNil)

			got := storedLiveJob(t, st, job.Key())
			So(got.State, ShouldEqual, JobStateBuried)
			So(got.Exitcode, ShouldEqual, 1)
		}

		Convey("2 stdouts and 1 stderr are stored and retrieved, before and after reopen", func() {
			check(s)
			check(reopenLogStore(ctx, s))
		})

		Convey("a later successful exit clears the std, and archiving drops it", func() {
			job.State = JobStateReady
			job.Exitcode = 0
			So(s.updateJobAfterExitDurable(job, []byte("ignored"), []byte("ignored"), false), ShouldBeNil)

			outOnly.State = JobStateComplete
			outOnly.Exitcode = 0
			_, err = s.archiveCompletion(outOnly.Key(), outOnly)
			So(err, ShouldBeNil)

			reopened := reopenLogStore(ctx, s)
			So(inspectedCount(reopened, kindStdOut), ShouldEqual, 0)
			So(inspectedCount(reopened, kindStdErr), ShouldEqual, 0)

			stdo, stde := reopened.retrieveJobStd(ctx, job.Key())
			So(stdo, ShouldBeNil)
			So(stde, ShouldBeNil)
		})

		Convey("a forced exit stores std even with exit code 0", func() {
			job.Exitcode = 0
			So(s.updateJobAfterExitDurable(job, []byte("forced"), nil, true), ShouldBeNil)

			stdo, _ := reopenLogStore(ctx, s).retrieveJobStd(ctx, job.Key())
			So(string(stdo), ShouldEqual, "forced")
		})
	})
}

func TestLogStoreTransitionRecords(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job added", t, func() {
		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo transitions", "transitions")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		before := len(walFrames(dir))

		Convey("a reservation, a start and a change each append one plain state record", func() {
			job.State = JobStateReserved
			So(s.updateJobRunStateDurable(job), ShouldBeNil)

			job.State = JobStateRunning
			So(s.updateJobRunStateDurable(job), ShouldBeNil)

			job.State = JobStateDelayed
			So(s.updateJobAfterChangeDurable(job), ShouldBeNil)

			frames := walFrames(dir)[before:]
			So(frames, ShouldHaveLength, 3)

			for _, frame := range frames {
				So(frame.isBlock, ShouldBeFalse)
				So(frame.recs, ShouldResemble, []logRecordType{logRecState})
			}

			So(storedLiveJobState(t, reopenLogStore(ctx, s), job.Key()), ShouldEqual, JobStateDelayed)
		})

		Convey("after a reopen, a transition's state record applies, also after a further reopen", func() {
			reopened := reopenLogStore(ctx, s)

			job.State = JobStateRunning
			So(reopened.updateJobRunStateDurable(job), ShouldBeNil)
			So(storedLiveJobState(t, reopened, job.Key()), ShouldEqual, JobStateRunning)
			So(storedLiveJobState(t, reopenLogStore(ctx, reopened), job.Key()), ShouldEqual, JobStateRunning)
		})

		Convey("an exit appends one block of its state, exitstat and both std records", func() {
			job.State = JobStateBuried
			job.Exitcode = 1
			So(s.updateJobAfterExitDurable(job, []byte("o"), nil, false), ShouldBeNil)

			frames := walFrames(dir)[before:]
			So(frames, ShouldHaveLength, 1)
			So(frames[0].isBlock, ShouldBeTrue)
			So(frames[0].recs, ShouldResemble, []logRecordType{logRecState, logRecExitStat, logRecStd, logRecStd})
		})

		Convey("an archive appends one block of its complete and delete records", func() {
			job.State = JobStateComplete
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			frames := walFrames(dir)[before:]
			So(frames, ShouldHaveLength, 1)
			So(frames[0].recs, ShouldResemble, []logRecordType{logRecComplete, logRecDelete})
		})

		Convey("an archive of a job marked to run again keeps it live, unreserved, to run again", func() {
			runner := uuid.Must(uuid.NewV4())
			job.State = JobStateRunning
			job.ReservedBy = runner
			job.RerunAfterRun = true
			job.EndTime = time.Now()

			outcome, erra := s.archiveCompletion(job.Key(), job)
			So(erra, ShouldBeNil)
			So(outcome, ShouldEqual, archiveKeptLive)

			frames := walFrames(dir)[before:]
			So(frames, ShouldHaveLength, 1)
			So(frames[0].recs, ShouldResemble, []logRecordType{logRecComplete, logRecState})

			reopened := reopenLogStore(ctx, s)

			live := storedLiveJob(t, reopened, job.Key())
			So(live.RerunAfterRun, ShouldBeFalse)
			So(live.ReservedBy, ShouldEqual, uuid.UUID{})

			complete, found, errc := reopened.completeJob(job.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(complete.RerunAfterRun, ShouldBeFalse)
			So(complete.ReservedBy, ShouldEqual, runner)
		})
	})
}

func TestLogStoreAddDedupAndDependents(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job archived under rep group a", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo dedup", "a")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateComplete
		job.EndTime = time.Now().Truncate(time.Second)
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)

		readd := func(st *logStore, repGroup string) storedNewJobs {
			stored, errs := st.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo dedup", repGroup)}, true, nil)
			So(errs, ShouldBeNil)

			return stored
		}

		Convey("adding it again with ignoreAdded stores nothing and counts it complete in its rep group", func() {
			for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
				stored := readd(st, "a")
				So(stored.jobsToQueue, ShouldBeEmpty)
				So(stored.alreadyAdded.Complete, ShouldEqual, 1)
				So(stored.alreadyAdded.CompleteSameRepGroup, ShouldEqual, 1)

				live, errl := st.checkIfLive(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeFalse)
			}
		})

		Convey("adding it under rep group b says it completed under a", func() {
			stored := readd(reopenLogStore(ctx, s), "b")
			So(stored.alreadyAdded.Complete, ShouldEqual, 1)
			So(stored.alreadyAdded.CompleteSameRepGroup, ShouldEqual, 0)
			So(stored.alreadyAdded.OtherRepGroups, ShouldHaveLength, 1)
			So(stored.alreadyAdded.OtherRepGroups[0].RepGroup, ShouldEqual, "a")
			So(stored.alreadyAdded.OtherRepGroups[0].LastCompleted.Equal(job.EndTime), ShouldBeTrue)
		})

		Convey("after a rep group lookup under b, adding it under b counts it as b's", func() {
			So(s.storeRepGroupLookups(ctx, []*Job{testDBJob("echo dedup", "b")}), ShouldBeNil)

			stored := readd(reopenLogStore(ctx, s), "b")
			So(stored.alreadyAdded.CompleteSameRepGroup, ShouldEqual, 1)
		})
	})

	Convey("Given a log store with an archived job and a queued job, each depending on dep group g", t, func() {
		s, _ := openTestLogStore(t, ctx)

		archived := testDBJob("echo dependent archived", "deps")
		archived.Dependencies = Dependencies{NewDepGroupDependency("g")}
		queued := testDBJob("echo dependent queued", "deps")
		queued.Dependencies = Dependencies{NewDepGroupDependency("g")}

		_, err := s.storeNewJobsGuarded(ctx, []*Job{archived, queued}, false, nil)
		So(err, ShouldBeNil)

		archived.State = JobStateComplete
		_, err = s.archiveCompletion(archived.Key(), archived)
		So(err, ShouldBeNil)

		s = reopenLogStore(ctx, s)

		seen, err := s.depGroupEverSeen("g")
		So(err, ShouldBeNil)
		So(seen, ShouldBeFalse)

		Convey("adding a member of g puts the archived one back live to queue, and returns the queued one", func() {
			member := testDBJob("echo member", "deps")
			member.DepGroups = []string{"g"}

			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			So(erra, ShouldBeNil)
			So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{archived.Key(), member.Key()})
			So(jobKeys(stored.dependents.jobs), ShouldResemble, []string{queued.Key()})

			reopened := reopenLogStore(ctx, s)

			for _, key := range []string{archived.Key(), queued.Key(), member.Key()} {
				live, errl := reopened.checkIfLive(key)
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
			}

			seenGroups, errs := reopened.depGroupsEverSeen([]string{"g", "h"})
			So(errs, ShouldBeNil)
			So(seenGroups, ShouldResemble, map[string]bool{"g": true, "h": false})
		})
	})
}

func TestLogStoreFailedWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose next batch sync fails with EIO", t, func() {
		var failed bool

		Reset(wal.SetTestSyncHook(func(path string) error {
			if !failed && strings.HasSuffix(path, walFileExt) {
				failed = true

				return syscall.EIO
			}

			return nil
		}))

		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo failed add", "failed")

		Convey("an add returns the error and leaves the job neither live nor, once a later write succeeds, recorded",
			func() {
				_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
				So(err, ShouldWrap, syscall.EIO)

				live, errl := s.checkIfLive(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeFalse)

				later := testDBJob("echo later add", "failed")
				_, err = s.storeNewJobsGuarded(ctx, []*Job{later}, false, nil)
				So(err, ShouldBeNil)

				reopened := reopenLogStore(ctx, s)
				live, errl = reopened.checkIfLive(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeFalse)

				live, errl = reopened.checkIfLive(later.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)

				_, err = reopened.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
				So(err, ShouldBeNil)

				live, errl = reopenLogStore(ctx, reopened).checkIfLive(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
			})
	})

	Convey("Given a closed log store, writes are refused with errDBClosed", t, func() {
		s, _ := openTestLogStore(t, ctx)
		So(s.close(ctx), ShouldBeNil)

		_, err := s.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo closed", "closed")}, false, nil)
		So(err, ShouldEqual, errDBClosed)
		So(s.updateJobRunStateDurable(testDBJob("echo closed", "closed")), ShouldEqual, errDBClosed)

		_, err = s.storeEnv([]byte("closed"))
		So(err, ShouldEqual, errDBClosed)

		Convey("a refused transition or exit does not hold up a later add of its job, which is refused too", func() {
			transitioned := testDBJob("echo closed transition", "closed")
			So(s.updateJobRunStateDurable(transitioned), ShouldEqual, errDBClosed)

			exited := testDBJob("echo closed exit", "closed")
			So(s.updateJobAfterExitDurable(exited, nil, nil, false), ShouldEqual, errDBClosed)

			for _, job := range []*Job{transitioned, exited} {
				So(receiveStored(addAsync(ctx, s, job)).err, ShouldEqual, errDBClosed)
			}
		})
	})
}

func TestLogStoreTornTail(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store whose last wal file has 10 garbage bytes appended", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)
		s, dir := openTestLogStore(t, logCtx)

		job := testDBJob("echo torn", "torn")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(dir)
		So(err, ShouldBeNil)

		last := walFilePath(dir, files[len(files)-1])
		info, err := os.Stat(last)
		So(err, ShouldBeNil)

		f, err := os.OpenFile(last, os.O_APPEND|os.O_WRONLY, 0)
		So(err, ShouldBeNil)
		_, err = f.WriteString("0123456789")
		So(err, ShouldBeNil)
		So(f.Close(), ShouldBeNil)

		Convey("it reopens with its jobs, the tail truncated and logged", func() {
			reopened := openLogStoreIn(logCtx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			after, errs := os.Stat(last)
			So(errs, ShouldBeNil)
			So(after.Size(), ShouldEqual, info.Size())

			live, errl := reopened.checkIfLive(job.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)

			lines := logLinesContaining(readLogFile(t, logPath), walTailTruncatedLogMsg)
			So(lines, ShouldHaveLength, 1)
			So(logLineValue(lines[0], "bytes"), ShouldEqual, "10")

			Convey("and a further reopen keeps every write", func() {
				more := testDBJob("echo torn more", "torn")
				_, err = reopened.storeNewJobsGuarded(ctx, []*Job{more}, false, nil)
				So(err, ShouldBeNil)

				again := reopenLogStore(ctx, reopened)
				So(inspectedCount(again, kindLive), ShouldEqual, 2)
			})
		})
	})
}

func TestLogStoreWriterStats(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store reporting every 50ms", t, func() {
		old := archiveFoldReportInterval
		archiveFoldReportInterval = storeStatsTestInterval

		Reset(func() { archiveFoldReportInterval = old })

		logPath, logCtx := managerLogContext(t, ctx)
		s, _ := openTestLogStore(t, logCtx)

		Convey("an add makes a db writer line for the log writer", func() {
			_, err := s.storeNewJobsGuarded(ctx, testLogJobs("stats", "stats", 3), false, nil)
			So(err, ShouldBeNil)

			var lines []string

			deadline := time.Now().Add(logStoreStatsWait)
			for len(lines) == 0 && time.Now().Before(deadline) {
				time.Sleep(storeStatsTestInterval)

				lines = storeStatsWriterLines(readLogFile(t, logPath), logWriterName)
			}

			So(lines, ShouldNotBeEmpty)
			So(storeStatsSum(t, lines, "batches"), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "records"), ShouldEqual, 9)

			bytes, err := strconv.Atoi(logLineValue(lines[0], "bytes"))
			So(err, ShouldBeNil)
			So(bytes, ShouldBeGreaterThan, 0)

			maxSync := storeStatsDuration(t, lines[0], "maxSync")
			So(maxSync, ShouldBeGreaterThan, 0)
			So(storeStatsDuration(t, lines[0], "meanSync"), ShouldBeLessThanOrEqualTo, maxSync)
			So(storeStatsDuration(t, lines[0], "interval"), ShouldEqual, storeStatsTestInterval)
		})

		Convey("an idle store writes no db writer line", func() {
			time.Sleep(storeStatsIdleIntervals * storeStatsTestInterval)
			So(s.close(ctx), ShouldBeNil)
			So(logLinesContaining(readLogFile(t, logPath), dbWriterLogMatch), ShouldBeEmpty)
		})
	})
}

func TestLogStoreRerunGuard(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with live job d depending on dep group g, and a guarded add of member m", t, func() {
		s, _ := openTestLogStore(t, ctx)

		d := testDBJob("echo guarded dependent", "guard")
		d.Dependencies = Dependencies{NewDepGroupDependency("g")}
		_, err := s.storeNewJobsGuarded(ctx, []*Job{d}, false, nil)
		So(err, ShouldBeNil)

		m := testDBJob("echo guarded member", "guard")
		m.DepGroups = []string{"g"}

		guard := s.registerRerunGuard()

		Reset(func() { s.releaseRerunGuard(guard) })

		d.State = JobStateComplete
		d.EndTime = time.Now()

		Convey("d archived after the add read it but before it wrote is put back by the add", func() {
			var outcome archiveOutcome

			dependentsReadHook = func() {
				dependentsReadHook = nil
				outcome, err = s.archiveCompletion(d.Key(), d)
			}

			Reset(func() { dependentsReadHook = nil })

			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
			So(erra, ShouldBeNil)
			So(err, ShouldBeNil)
			So(outcome, ShouldEqual, archiveRemovedLive)
			So(jobKeys(stored.dependents.jobs), ShouldResemble, []string{d.Key()})
			So(stored.dependents.wasPutBack(d.Key()), ShouldBeTrue)

			reopened := reopenLogStore(ctx, s)
			live := storedLiveJob(t, reopened, d.Key())
			So(live.RerunAfterRun, ShouldBeFalse)

			complete, errc := reopened.checkIfComplete(d.Key())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeTrue)
		})

		Convey("d archived once the add has written is kept live by the guard", func() {
			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
			So(erra, ShouldBeNil)
			So(stored.dependents.putBack, ShouldBeEmpty)

			outcome, errc := s.archiveCompletion(d.Key(), d)
			So(errc, ShouldBeNil)
			So(outcome, ShouldEqual, archiveRemovedLive)
			So(guard.keptLive(d.Key()), ShouldBeTrue)

			live, errl := reopenLogStore(ctx, s).checkIfLive(d.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)
		})

		Convey("d archived with no add under way leaves the live plane", func() {
			s.releaseRerunGuard(guard)

			_, errc := s.archiveCompletion(d.Key(), d)
			So(errc, ShouldBeNil)

			live, errl := s.checkIfLive(d.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeFalse)
		})
	})

	Convey("Given live dependent d of dep group g, archived after a guarded add of member m read it", t, func() {
		s, _ := openTestLogStore(t, ctx)

		dependent := func() *Job {
			d := testDBJob("echo guard put back race", "guard")
			d.Dependencies = Dependencies{NewDepGroupDependency("g")}

			return d
		}

		d := dependent()
		_, err := s.storeNewJobsGuarded(ctx, []*Job{d}, false, nil)
		So(err, ShouldBeNil)

		m := testDBJob("echo guard put back race member", "guard")
		m.DepGroups = []string{"g"}

		guard := s.registerRerunGuard()

		Reset(func() { s.releaseRerunGuard(guard) })

		var archiveErr error

		dependentsReadHook = func() {
			dependentsReadHook = nil

			d.State = JobStateComplete
			d.EndTime = time.Now()
			_, archiveErr = s.archiveCompletion(d.Key(), d)
		}

		Reset(func() { dependentsReadHook = nil })

		Convey("d re-added and reserved before the add puts it back stays running, not put back", func() {
			runner := uuid.Must(uuid.NewV4())

			var hookErr error

			logPutBackHook = func() {
				logPutBackHook = nil

				readded := dependent()
				if _, hookErr = s.storeNewJobsGuarded(ctx, []*Job{readded}, false, nil); hookErr != nil {
					return
				}

				readded.State = JobStateRunning
				readded.ReservedBy = runner
				readded.StartTime = time.Now()
				hookErr = s.updateJobRunStateDurable(readded)
			}

			Reset(func() { logPutBackHook = nil })

			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
			So(erra, ShouldBeNil)
			So(archiveErr, ShouldBeNil)
			So(hookErr, ShouldBeNil)
			So(stored.dependents.putBack, ShouldBeEmpty)

			for _, st := range []*logStore{s, reopenLogStore(ctx, s)} {
				got, live, errl := st.liveJob(d.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
				So(got.State, ShouldEqual, JobStateRunning)
				So(got.ReservedBy, ShouldEqual, runner)
			}
		})
	})

	Convey("Given 3 live dependents of dep group g, a 1 byte block limit, and a guarded add of member m", t, func() {
		s, dir := openTestLogStore(t, ctx)

		dependents := testLogJobs("guard split", "guard", 3)
		for _, d := range dependents {
			d.Dependencies = Dependencies{NewDepGroupDependency("g")}
		}

		_, err := s.storeNewJobsGuarded(ctx, dependents, false, nil)
		So(err, ShouldBeNil)

		m := testDBJob("echo guard split member", "guard")
		m.DepGroups = []string{"g"}

		guard := s.registerRerunGuard()

		Reset(func() { s.releaseRerunGuard(guard) })

		old := logBlockMaxFrames
		logBlockMaxFrames = 1

		Reset(func() { logBlockMaxFrames = old })

		Convey("dependents archived after the add read them are each put back in a block of their own", func() {
			var archiveErrs []error

			dependentsReadHook = func() {
				dependentsReadHook = nil

				for _, d := range dependents {
					d.State = JobStateComplete
					d.EndTime = time.Now()
					_, erra := s.archiveCompletion(d.Key(), d)
					archiveErrs = append(archiveErrs, erra)
				}
			}

			Reset(func() { dependentsReadHook = nil })

			before := len(walFrames(dir))

			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{m}, false, guard)
			So(erra, ShouldBeNil)
			So(archiveErrs, ShouldResemble, []error{nil, nil, nil})
			So(stored.dependents.putBack, ShouldHaveLength, 3)

			So(specsPerFrame(walFrames(dir)[before:]), ShouldResemble, []int{1, 1, 1, 1})
		})
	})
}

func TestLogStoreExpectedExit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job, and an exit update of it expected", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo expected exit", "expected")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		done := s.expectJobExitUpdate()

		Convey("retrieveJobStd waits for that update, then returns its std", func() {
			type std struct{ out, err []byte }

			got := make(chan std, 1)

			go func() {
				o, e := s.retrieveJobStd(ctx, job.Key())
				got <- std{o, e}
			}()

			select {
			case <-got:
				So("retrieveJobStd returned before the expected exit update", ShouldBeEmpty)
			case <-time.After(10 * jobExitUpdatePollInterval):
			}

			job.Exitcode = 1
			s.updateJobAfterExit(ctx, job, []byte("o"), []byte("e"), false)
			done()

			select {
			case retrieved := <-got:
				So(string(retrieved.out), ShouldEqual, "o")
				So(string(retrieved.err), ShouldEqual, "e")
			case <-time.After(logStoreStatsWait):
				So("retrieveJobStd never returned", ShouldBeEmpty)
			}
		})
	})
}

func TestLogStoreStateSeqOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job's two state records appended in reverse seq order", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo seq order", "seq")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		k, err := parseLogKey(job.Key())
		So(err, ShouldBeNil)

		var earlier, later logFrames

		job.State = JobStateReady
		ready := newLogRunState(job)
		s.addState(&earlier, logRecState, k, &ready)

		job.State = JobStateBuried
		buried := newLogRunState(job)
		s.addState(&later, logRecState, k, &buried)

		So(later.ops[0].seq, ShouldBeGreaterThan, earlier.ops[0].seq)
		So(s.writeAndWait(&later), ShouldBeNil)
		So(s.writeAndWait(&earlier), ShouldBeNil)

		Convey("the job has the later-seq state, before and after reopen", func() {
			So(storedLiveJobState(t, s, job.Key()), ShouldEqual, JobStateBuried)
			So(storedLiveJobState(t, reopenLogStore(ctx, s), job.Key()), ShouldEqual, JobStateBuried)
		})
	})
}

func TestLogStoreFailedWriteNotReplayed(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose next batch sync fails with EIO, and no write after it", t, func() {
		var failed atomic.Bool

		Reset(wal.SetTestSyncHook(func(path string) error {
			if strings.HasSuffix(path, walFileExt) && failed.CompareAndSwap(false, true) {
				return syscall.EIO
			}

			return nil
		}))

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo failed then closed", "failed")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldWrap, syscall.EIO)

		Convey("after close and reopen the failed add's records are not replayed", func() {
			So(s.close(ctx), ShouldBeNil)

			frames := walFrames(dir)
			So(frames, ShouldHaveLength, 1)
			So(countRecords(frames[0].recs, logRecSpec), ShouldEqual, 1)

			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			live, errl := reopened.checkIfLive(job.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeFalse)
			So(walFrames(dir), ShouldBeEmpty)
		})
	})
}

// logLiveView is what of a live key's state its records decide: a state's or
// std's Ref only while it has one; with no state it is never handed out.
type logLiveView struct {
	specSeq, stateSeq uint64
	spec, state       wal.Ref
	handedOut         bool
	stdSeq            [2]uint64
	std               [2]wal.Ref
	hasStd            [2]bool
}

// newLogLiveView returns e's view.
func newLogLiveView(e *logLive) logLiveView {
	v := logLiveView{
		specSeq: e.specSeq, spec: e.spec, stateSeq: e.stateSeq, handedOut: e.handedOut,
		stdSeq: e.stdSeq, hasStd: e.hasStd,
	}

	if e.stateSeq > 0 {
		v.state = e.state
	}

	for stream := range v.std {
		if e.hasStd[stream] {
			v.std[stream] = e.std[stream]
		}
	}

	return v
}

// expectedLiveView returns the view, by the Records seq rules, of a key live
// with a spec of seq 1 once ops apply: its latest spec, and of the state and
// each std stream the latest record after that spec, if any.
func expectedLiveView(ops []logOp) logLiveView {
	v := logLiveView{specSeq: 1, spec: wal.Ref{File: 1, Off: 1}}

	for _, op := range ops {
		if op.typ == logRecSpec && op.seq > v.specSeq {
			v.specSeq, v.spec = op.seq, op.ref
		}
	}

	for _, op := range ops {
		switch {
		case op.seq < v.specSeq:
		case op.typ == logRecState && op.seq > v.stateSeq:
			v.stateSeq, v.state, v.handedOut = op.seq, op.ref, op.handedOut
		case op.typ == logRecStd && op.seq > v.stdSeq[op.stream]:
			v.stdSeq[op.stream], v.hasStd[op.stream], v.std[op.stream] = op.seq, op.hasStd, wal.Ref{}

			if op.hasStd {
				v.std[op.stream] = op.ref
			}
		}
	}

	return v
}

func TestLogStoreReducerOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a live key's later spec, state and std records, applied in random orders", t, func() {
		const trials = 5000

		rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed, for reproducible cases
		k := logKey{1}
		mismatched := 0

		for range trials {
			ops := randomKeyOps(rng, k)
			want := expectedLiveView(ops)

			rng.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })

			st := newLogState()
			st.apply(&logOp{typ: logRecSpec, seq: 1, key: k, ref: wal.Ref{File: 1, Off: 1}})

			for i := range ops {
				st.apply(&ops[i])
			}

			if newLogLiveView(st.live[k]) != want {
				mismatched++
			}
		}

		Convey("the key always ends with its latest spec, and its latest state and std of each stream after "+
			"that spec", func() {
			So(mismatched, ShouldEqual, 0)
		})
	})
}

// randomKeyOps returns 1 to 8 spec, state and std ops of k, with seqs 2 on in
// a random order, each with a Ref naming its seq.
func randomKeyOps(rng *rand.Rand, k logKey) []logOp {
	types := [...]logRecordType{logRecSpec, logRecState, logRecStd}
	ops := make([]logOp, 1+rng.IntN(8))

	for i, seq := range rng.Perm(len(ops)) {
		s := uint64(seq) + 2 //nolint:gosec // a small index
		ops[i] = logOp{
			typ: types[rng.IntN(len(types))], seq: s, key: k, ref: wal.Ref{File: 1, Off: s},
			handedOut: rng.IntN(2) == 0, stream: uint8(rng.IntN(2)), hasStd: rng.IntN(2) == 0, //nolint:gosec // 0 or 1
		}
	}

	return ops
}

func TestLogStoreFailedWriteNotReplayedAfterSpentFile(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store holding job a, whose add of job b fails, then whose open of wal file 2 fails "+
		"with its header not durable and file 2 left behind, before file 3 opens", t, func() {
		var failing, spent atomic.Bool

		var dir string

		Reset(wal.SetTestSyncHook(func(path string) error {
			switch {
			case path == walFilePath(dir, 1) && failing.Load() && !spent.Load():
				return syscall.EIO
			case path == walFilePath(dir, 2) && spent.CompareAndSwap(false, true):
				// tear the header, and leave something that cannot be removed
				return errors.Join(syscall.EIO, os.Remove(path), os.Mkdir(path, 0o700),
					os.WriteFile(filepath.Join(path, "stray"), nil, 0o600))
			default:
				return nil
			}
		}))

		var s *logStore

		s, dir = openTestLogStore(t, ctx)

		a := testDBJob("echo spent a", "spent")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{a}, false, nil)
		So(err, ShouldBeNil)

		failing.Store(true)

		b := testDBJob("echo spent b", "spent")
		_, err = s.storeNewJobsGuarded(ctx, []*Job{b}, false, nil)
		So(err, ShouldWrap, syscall.EIO)

		So(s.close(ctx), ShouldBeNil)
		So(spent.Load(), ShouldBeTrue)

		Convey("after a crash leaving file 2's header torn, reopening keeps a and does not replay b", func() {
			spentPath := walFilePath(dir, 2)
			So(os.RemoveAll(spentPath), ShouldBeNil)
			writeWALFile(dir, 2, walHeaderFrame(1, 0)[:5])

			files, errf := walFileNumbers(dir)
			So(errf, ShouldBeNil)
			So(files, ShouldResemble, []uint32{1, 2, 3})

			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			for job, want := range map[*Job]bool{a: true, b: false} {
				live, errl := reopened.checkIfLive(job.Key())
				So(errl, ShouldBeNil)
				So(live, ShouldEqual, want)
			}
		})
	})
}

func TestLogStoreRecoveryFileRules(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a closed log store holding a job in wal file 1", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)
		s, dir := openTestLogStore(t, logCtx)

		job := testDBJob("echo file rules", "rules")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		files, err := walFileNumbers(dir)
		So(err, ShouldBeNil)
		So(files, ShouldResemble, []uint32{1})

		reopenKeeps := func() *logStore {
			reopened := openLogStoreIn(logCtx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			live, errl := reopened.checkIfLive(job.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)

			return reopened
		}

		Convey("a last file 2 whose header is torn holds nothing durable: it is removed and logged", func() {
			writeWALFile(dir, 2, walHeaderFrame(1, 0)[:5])

			reopened := reopenKeeps()

			lines := logLinesContaining(readLogFile(t, logPath), walTornHeaderLogMsg)
			So(lines, ShouldHaveLength, 1)
			So(logLineValue(lines[0], "bytes"), ShouldEqual, "5")

			more := testDBJob("echo file rules more", "rules")
			_, err = reopened.storeNewJobsGuarded(ctx, []*Job{more}, false, nil)
			So(err, ShouldBeNil)

			files, err = walFileNumbers(dir)
			So(err, ShouldBeNil)
			So(files, ShouldResemble, []uint32{1, 2})
			So(inspectedCount(reopenLogStore(ctx, reopened), kindLive), ShouldEqual, 2)
		})

		corrupt := func() {
			_, erro := openLogStore(logCtx, dir)
			So(erro, ShouldWrap, errStoreCorrupt)
		}

		Convey("a missing file number is corrupt, even when the next header names it with durable end 0", func() {
			for _, header := range [][]byte{walHeaderFrame(2, 0), walHeaderFrame(1, 0), walHeaderFrame(2, 100)} {
				writeWALFile(dir, 3, header)
				corrupt()
			}

			So(os.Remove(walFilePath(dir, 3)), ShouldBeNil)
			writeWALFile(dir, 4, walHeaderFrame(3, 0))
			corrupt()
		})

		Convey("file 1 damaged before the durable end file 2's header gives it is corrupt", func() {
			path := walFilePath(dir, 1)

			data, errr := os.ReadFile(path)
			So(errr, ShouldBeNil)

			writeWALFile(dir, 2, walHeaderFrame(1, uint64(len(data))))

			data[len(data)-1] ^= 0xff
			So(os.WriteFile(path, data, 0o600), ShouldBeNil)
			corrupt()
		})

		Convey("a file 2 whose header names a file other than 1 is corrupt", func() {
			info, errs := os.Stat(walFilePath(dir, 1))
			So(errs, ShouldBeNil)

			writeWALFile(dir, 2, walHeaderFrame(9, uint64(info.Size()))) //nolint:gosec // a file size
			corrupt()
		})

		Convey("a file 2 whose header is torn, skipped by file 3's header naming file 1, was spent: file 1 is "+
			"valid up to the end file 3 gives it, and file 2 holds nothing", func() {
			path := walFilePath(dir, 1)

			info, errs := os.Stat(path)
			So(errs, ShouldBeNil)

			abandoned, erro := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			So(erro, ShouldBeNil)

			_, errw := abandoned.Write(wal.AppendFrame(nil, []byte("an abandoned batch")))
			So(errw, ShouldBeNil)
			So(abandoned.Close(), ShouldBeNil)

			writeWALFile(dir, 2, walHeaderFrame(1, 0)[:5])
			writeWALFile(dir, 3, walHeaderFrame(1, uint64(info.Size()))) //nolint:gosec // a file size

			reopenKeeps()

			files, err = walFileNumbers(dir)
			So(err, ShouldBeNil)
			So(files, ShouldResemble, []uint32{1, 2, 3, 4})

			for n, size := range map[uint32]int64{1: info.Size(), 2: 0} {
				got, errn := os.Stat(walFilePath(dir, n))
				So(errn, ShouldBeNil)
				So(got.Size(), ShouldEqual, size)
			}
		})

		Convey("a file 2 holding only zeros, as a header torn before any of its bytes reached the disk, is "+
			"spent like any torn header when file 3's header names file 1", func() {
			info, errs := os.Stat(walFilePath(dir, 1))
			So(errs, ShouldBeNil)

			writeWALFile(dir, 2, make([]byte, len(walHeaderFrame(1, 0))))
			writeWALFile(dir, 3, walHeaderFrame(1, uint64(info.Size()))) //nolint:gosec // a file size

			reopenKeeps()

			got, errn := os.Stat(walFilePath(dir, 2))
			So(errn, ShouldBeNil)
			So(got.Size(), ShouldEqual, 0)
		})

		Convey("a file 2 whose header is torn, followed by a file 3 whose header names file 2 (so its header "+
			"was durable), is corrupt", func() {
			writeWALFile(dir, 2, walHeaderFrame(1, 0)[:5])
			writeWALFile(dir, 3, walHeaderFrame(2, 0))
			corrupt()
		})

		Convey("a file 2 with no header, followed by a file 3 whose header is torn, is corrupt", func() {
			writeWALFile(dir, 2, nil)
			writeWALFile(dir, 3, walHeaderFrame(2, 0)[:5])
			corrupt()
		})

		Convey("a file 2 that starts with a record, as long as a header or longer, followed by a file 3 whose "+
			"header names file 1, is corrupt, and file 2 is left as it was", func() {
			info, errs := os.Stat(walFilePath(dir, 1))
			So(errs, ShouldBeNil)

			writeWALFile(dir, 3, walHeaderFrame(1, uint64(info.Size()))) //nolint:gosec // a file size

			for _, rec := range []string{"not a header but maybe records", "header-length record"} {
				record := wal.AppendFrame(nil, []byte(rec))
				writeWALFile(dir, 2, record)
				corrupt()

				data, errr := os.ReadFile(walFilePath(dir, 2))
				So(errr, ShouldBeNil)
				So(data, ShouldResemble, record)
			}
		})

		Convey("a file 2 that starts with a torn record shorter than a header, followed by a file 3 whose "+
			"header names file 1, is corrupt, and file 2 is left as it was", func() {
			info, errs := os.Stat(walFilePath(dir, 1))
			So(errs, ShouldBeNil)

			writeWALFile(dir, 3, walHeaderFrame(1, uint64(info.Size()))) //nolint:gosec // a file size

			torn := wal.AppendFrame(nil, []byte("short"))
			torn = torn[:len(torn)-1]
			writeWALFile(dir, 2, torn)
			corrupt()

			data, errr := os.ReadFile(walFilePath(dir, 2))
			So(errr, ShouldBeNil)
			So(data, ShouldResemble, torn)
		})

		Convey("a first file that starts with a record, not a header, is corrupt", func() {
			writeWALFile(dir, 1, wal.AppendFrame(nil, []byte("not a header")))
			corrupt()
		})

		Convey("a first file whose records follow no header, bounded by file 2's header, is corrupt", func() {
			data, errr := os.ReadFile(walFilePath(dir, 1))
			So(errr, ShouldBeNil)

			records := data[len(walHeaderFrame(0, 0)):]
			writeWALFile(dir, 1, records)
			writeWALFile(dir, 2, walHeaderFrame(1, uint64(len(records))))
			corrupt()
		})

		Convey("a spent first file holding records but no header is corrupt, not truncated", func() {
			data, errr := os.ReadFile(walFilePath(dir, 1))
			So(errr, ShouldBeNil)

			records := data[len(walHeaderFrame(0, 0)):]
			writeWALFile(dir, 1, records)
			writeWALFile(dir, 2, walHeaderFrame(0, 0))
			corrupt()

			got, errg := os.ReadFile(walFilePath(dir, 1))
			So(errg, ShouldBeNil)
			So(got, ShouldResemble, records)
		})

		Convey("a last file whose first frame is torn but is not a header is corrupt, not a torn header, "+
			"and is left as it was", func() {
			short := wal.AppendFrame(nil, []byte("short"))
			flipped, errr := os.ReadFile(walFilePath(dir, 1))
			So(errr, ShouldBeNil)

			flipped[1] ^= 0x01

			for _, data := range [][]byte{
				wal.AppendFrame(nil, []byte("not a header but maybe records"))[:30],
				short[:len(short)-1],
				flipped,
			} {
				writeWALFile(dir, 1, data)
				corrupt()

				got, errg := os.ReadFile(walFilePath(dir, 1))
				So(errg, ShouldBeNil)
				So(got, ShouldResemble, data)
			}
		})
	})
}

// writeWALFile writes data as wal file n of dir.
func writeWALFile(dir string, n uint32, data []byte) {
	So(os.WriteFile(walFilePath(dir, n), data, 0o600), ShouldBeNil)
}

// walHeaderFrame returns a wal file's header frame naming prevFile and
// prevEnd.
func walHeaderFrame(prevFile uint32, prevEnd uint64) []byte {
	rec := []byte("WRWALHDR")
	rec = binary.LittleEndian.AppendUint32(rec, prevFile)
	rec = binary.LittleEndian.AppendUint64(rec, prevEnd)

	return wal.AppendFrame(nil, rec)
}

func TestLogStoreReducerSeqRules(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job added", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo reducer", "reducer")
		key := job.Key()
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		k, err := parseLogKey(key)
		So(err, ShouldBeNil)

		beforeAndAfterReopen := func(check func(st *logStore)) {
			check(s)
			check(reopenLogStore(ctx, s))
		}

		stdOf := func(st *logStore) (string, string) {
			stdo, stde := st.retrieveJobStd(ctx, key)

			return string(stdo), string(stde)
		}

		Convey("of two spec records applied in reverse seq order, the later one is live", func() {
			var earlier, later logFrames

			job.Priority = 1
			So(s.addSpecAndState(&earlier, job, k), ShouldBeNil)

			job.Priority = 2
			So(s.addSpecAndState(&later, job, k), ShouldBeNil)

			writeInReverse(s, &earlier, &later)

			beforeAndAfterReopen(func(st *logStore) {
				So(storedLiveJob(t, st, key).Priority, ShouldEqual, 2)
			})
		})

		Convey("of two complete records applied in reverse seq order, the later one is the history entry", func() {
			var earlier, later logFrames

			job.State = JobStateComplete
			job.Attempts = 1
			first := newLogRunState(job)
			s.addState(&earlier, logRecComplete, k, &first)

			job.Attempts = 2
			second := newLogRunState(job)
			s.addState(&later, logRecComplete, k, &second)

			writeInReverse(s, &earlier, &later)

			beforeAndAfterReopen(func(st *logStore) {
				complete, found, errc := st.completeJob(key)
				So(errc, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(complete.Attempts, ShouldEqual, 2)
			})
		})

		Convey("a complete record of a key neither live nor complete makes no history entry", func() {
			orphan := testDBJob("echo reducer orphan", "reducer")
			orphan.State = JobStateComplete

			ko, errk := parseLogKey(orphan.Key())
			So(errk, ShouldBeNil)

			var f logFrames

			rs := newLogRunState(orphan)
			s.addState(&f, logRecComplete, ko, &rs)
			So(s.writeAndWait(&f), ShouldBeNil)

			beforeAndAfterReopen(func(st *logStore) {
				complete, errc := st.checkIfComplete(orphan.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeFalse)
			})
		})

		Convey("an archive keeping a failed job live to run again drops its std", func() {
			job.State = JobStateBuried
			job.Exitcode = 1
			So(s.updateJobAfterExitDurable(job, []byte("out"), []byte("err"), false), ShouldBeNil)

			stdo, stde := stdOf(s)
			So(stdo, ShouldEqual, "out")
			So(stde, ShouldEqual, "err")

			job.State = JobStateRunning
			job.Exitcode = 0
			job.RerunAfterRun = true
			job.EndTime = time.Now()

			outcome, erra := s.archiveCompletion(key, job)
			So(erra, ShouldBeNil)
			So(outcome, ShouldEqual, archiveKeptLive)

			beforeAndAfterReopen(func(st *logStore) {
				stdo, stde := stdOf(st)
				So(stdo, ShouldBeEmpty)
				So(stde, ShouldBeEmpty)
			})
		})

		Convey("a std record applied after a later complete record of its live key is dropped", func() {
			var std, complete logFrames

			s.addStd(&std, k, stdOut, []byte("stale"))

			job.State = JobStateComplete
			rs := newLogRunState(job)
			s.addState(&complete, logRecComplete, k, &rs)

			writeInReverse(s, &std, &complete)

			beforeAndAfterReopen(func(st *logStore) {
				stdo, _ := stdOf(st)
				So(stdo, ShouldBeEmpty)
			})
		})

		Convey("an archive's delete applied after a re-add's later spec leaves the key live", func() {
			job.State = JobStateComplete
			archive := s.archiveFrames(job, k, false)

			var readd logFrames

			job.State = ""
			So(s.addSpecAndState(&readd, job, k), ShouldBeNil)

			writeInReverse(s, &archive, &readd)

			beforeAndAfterReopen(func(st *logStore) {
				live, errl := st.checkIfLive(key)
				So(errl, ShouldBeNil)
				So(live, ShouldBeTrue)
			})
		})

		Convey("of two std records of a stream applied in reverse seq order, the later one is kept", func() {
			var earlier, later logFrames

			s.addStd(&earlier, k, stdOut, []byte("first"))
			s.addStd(&later, k, stdOut, []byte("second"))

			writeInReverse(s, &earlier, &later)

			beforeAndAfterReopen(func(st *logStore) {
				stdo, _ := stdOf(st)
				So(stdo, ShouldEqual, "second")
			})
		})
	})
}

// writeInReverse writes later's records, then earlier's, which must all have
// lower seqs, each waiting until durable.
func writeInReverse(s *logStore, earlier, later *logFrames) {
	So(later.ops[0].seq, ShouldBeGreaterThan, earlier.ops[len(earlier.ops)-1].seq)
	So(s.writeAndWait(later), ShouldBeNil)
	So(s.writeAndWait(earlier), ShouldBeNil)
}
