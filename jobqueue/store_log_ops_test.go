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

// Tests of the log store's deletes, modifies and reruns (spec C4, tests 1 to
// 8), and of how they interleave with adds, archives and folds.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// archivedDepsGroup is the dep group of TestStoreArchivedDependentsAfterRestart's
	// members, and archivedDepsNewMember the command of the member it adds.
	archivedDepsGroup     = "grow"
	archivedDepsNewMember = "echo archived deps new member"
)

// heldWrite is a write whose records were made, taking their seqs, but not yet
// appended: a best-effort or exit write snapshotted before something else and
// appended after it.
type heldWrite struct {
	f        logFrames
	finished func()
}

// holdStateWrite returns job's state record, as writeJobState makes it, held.
func holdStateWrite(s *logStore, job *Job) heldWrite {
	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	var h heldWrite

	h.f, h.finished = s.stateFrames(job, k)

	return h
}

// holdExitWrite returns job's exit block with std stdo and stde, as
// updateJobAfterExit makes it, held.
func holdExitWrite(s *logStore, job *Job, stdo, stde string) heldWrite {
	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	f, finished := s.exitFrames(job, k, []byte(stdo), []byte(stde), false)

	return heldWrite{f: f, finished: finished}
}

// write appends h's records and waits until they are durable.
func (h *heldWrite) write(s *logStore) error {
	w, err := s.writeOpen(&h.f, h.finished)
	if err != nil {
		h.finished()

		return err
	}

	return w.wait()
}

func TestLogStoreDeleteModifyRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with 3 live jobs", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("c4 delete", "c4", 3)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		Convey("deleting 2 of them leaves 1 live, also after reopen", func() {
			So(s.deleteLiveJobs(ctx, []string{jobs[0].Key(), jobs[2].Key()}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(inspectedCount(st, kindLive), ShouldEqual, 1)
				soLive(st, jobs[1].Key(), true)
				soLive(st, jobs[0].Key(), false)
			})
		})

		Convey("deleting them appends one frame of 3 delete records, durable on return", func() {
			before := len(walFrames(s.dir))

			So(s.deleteLiveJobs(ctx, jobKeys(jobs)), ShouldBeNil)
			So(inspectedCount(s, kindLive), ShouldEqual, 0)

			frames := walFrames(s.dir)[before:]
			So(frames, ShouldHaveLength, 1)
			So(frames[0].recs, ShouldResemble, []logRecordType{logRecDelete, logRecDelete, logRecDelete})
		})

		Convey("deleting none writes nothing", func() {
			before := len(walFrames(s.dir))

			So(s.deleteLiveJobs(ctx, nil), ShouldBeNil)
			So(walFrames(s.dir), ShouldHaveLength, before)
		})
	})

	Convey("Given live job k1 modified to key k2 by a block whose last byte is lost", t, func() {
		s, dir := openTestLogStore(t, ctx)

		k1 := testDBJob("echo c4 modify torn", "c4")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{k1}, false, nil)
		So(err, ShouldBeNil)

		k2 := testDBJob("echo c4 modify torn", "c4")
		k2.Cmd = "echo c4 modified torn"

		before := len(walFrames(dir))

		So(s.modifyLiveJobs(ctx, []string{k1.Key()}, []*Job{k2}), ShouldBeNil)

		frames := walFrames(dir)[before:]
		So(frames, ShouldHaveLength, 1)
		So(frames[0].isBlock, ShouldBeTrue)
		So(frames[0].recs[:3], ShouldResemble, []logRecordType{logRecModify, logRecSpec, logRecState})

		soLive(s, k1.Key(), false)
		soLive(s, k2.Key(), true)

		So(s.close(ctx), ShouldBeNil)
		truncateLastWALByte(dir)

		Convey("on reopen k1 is live with its old spec and k2 does not exist", func() {
			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			So(storedLiveJob(t, reopened, k1.Key()).Cmd, ShouldEqual, k1.Cmd)
			soLive(reopened, k2.Key(), false)
			So(inspectedCount(reopened, kindLive), ShouldEqual, 1)
		})
	})

	Convey("Given a modify of 2 jobs to new keys split into a block per job, the last of them torn", t, func() {
		old := logBlockMaxFrames
		logBlockMaxFrames = 1

		Reset(func() { logBlockMaxFrames = old })

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("c4 modify split", "c4split", 2)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		moved := []*Job{modifiedCopy(jobs[0], "echo c4 modify split moved 0"),
			modifiedCopy(jobs[1], "echo c4 modify split moved 1")}
		So(s.modifyLiveJobs(ctx, jobKeys(jobs), moved), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)
		truncateLastWALByte(dir)

		Convey("on reopen the first job is wholly modified, found by its rep group once complete, and the "+
			"second untouched", func() {
			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			soLive(reopened, jobs[0].Key(), false)
			soLive(reopened, moved[0].Key(), true)
			soLive(reopened, jobs[1].Key(), true)
			soLive(reopened, moved[1].Key(), false)

			for _, job := range []*Job{moved[0], jobs[1]} {
				job.State = JobStateComplete
				job.EndTime = time.Now()
				_, err = reopened.archiveCompletion(job.Key(), job)
				So(err, ShouldBeNil)
			}

			byRG, errr := reopened.retrieveCompleteJobsByRepGroup("c4split")
			So(errr, ShouldBeNil)

			got, want := jobKeys(byRG), []string{moved[0].Key(), jobs[1].Key()}
			slices.Sort(got)
			slices.Sort(want)
			So(got, ShouldResemble, want)
		})
	})

	Convey("Given a completed job put back live with storeLiveForRerun", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c4 put back", "c4rerun")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateComplete
		job.EndTime = time.Now()
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)

		job.RerunAfterRun = true
		job.ReservedBy = uuid.Must(uuid.NewV4())
		So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)

		Convey("it is live and complete, found by key but not by rep group, before and after reopen; deleted and "+
			"re-added with ignoreAdded, it counts as complete and is not stored", func() {
			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, job.Key(), true)
				soComplete(st, job.Key())

				live := storedLiveJob(t, st, job.Key())
				So(live.RerunAfterRun, ShouldBeFalse)
				So(live.ReservedBy, ShouldEqual, uuid.UUID{})

				byKey, errk := st.retrieveCompleteJobsByKeys([]string{job.Key()})
				So(errk, ShouldBeNil)
				So(jobKeys(byKey), ShouldResemble, []string{job.Key()})

				byRG, errr := st.retrieveCompleteJobsByRepGroup("c4rerun")
				So(errr, ShouldBeNil)
				So(byRG, ShouldBeEmpty)
			})

			So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				readded := testDBJob("echo c4 put back", "c4rerun")
				stored, erra := st.storeNewJobsGuarded(ctx, []*Job{readded}, true, nil)
				So(erra, ShouldBeNil)
				So(stored.alreadyAdded.Complete, ShouldEqual, 1)
				So(stored.jobsToQueue, ShouldBeEmpty)
				soLive(st, job.Key(), false)
				soComplete(st, job.Key())
			})
		})

		Convey("putting it back appends one block of its spec and state records", func() {
			before := len(walFrames(s.dir))
			So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)

			frames := walFrames(s.dir)[before:]
			So(frames, ShouldHaveLength, 1)
			So(frames[0].recs, ShouldResemble, []logRecordType{logRecSpec, logRecState})
		})
	})

	Convey("Given a log store", t, func() {
		s, _ := openTestLogStore(t, ctx)

		Convey("a job that exited 1 with std, then exited 0, has no std after reopen", func() {
			job := failedWithStd(ctx, s, "echo c4 std cleared", "o", "e")

			job.State = JobStateReady
			job.Exitcode = 0
			So(s.updateJobAfterExitDurable(job, []byte("o2"), []byte("e2"), false), ShouldBeNil)

			reopened := reopenLogStore(ctx, s)
			soStd(ctx, reopened, job.Key(), "", "")
			So(inspectedCount(reopened, kindStdOut), ShouldEqual, 0)
			So(inspectedCount(reopened, kindStdErr), ShouldEqual, 0)
		})

		Convey("of two jobs that exited 1 with std, one archived and one deleted, no std remains after reopen",
			func() {
				archived := failedWithStd(ctx, s, "echo c4 std archived", "o", "e")
				deleted := failedWithStd(ctx, s, "echo c4 std deleted", "o", "e")
				So(inspectedCount(s, kindStdOut), ShouldEqual, 2)

				archived.State = JobStateComplete
				archived.Exitcode = 0
				archived.EndTime = time.Now()
				_, err := s.archiveCompletion(archived.Key(), archived)
				So(err, ShouldBeNil)
				So(s.deleteLiveJobs(ctx, []string{deleted.Key()}), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					So(inspectedCount(st, kindStdOut), ShouldEqual, 0)
					So(inspectedCount(st, kindStdErr), ShouldEqual, 0)
					soStd(ctx, st, deleted.Key(), "", "")
				})
			})

		Convey("jobs that exited 1 with std, modified to new keys in one call, take their std there", func() {
			k1 := failedWithStd(ctx, s, "echo c4 std modified", "o", "e")
			k2 := modifiedCopy(k1, "echo c4 std modified to k2")

			other := failedWithStd(ctx, s, "echo c4 std modified other", "o2", "")
			otherMoved := modifiedCopy(other, "echo c4 std modified other moved")

			So(s.modifyLiveJobs(ctx, []string{k1.Key(), other.Key()}, []*Job{k2, otherMoved}), ShouldBeNil)

			check := func(st *logStore) {
				soStd(ctx, st, k2.Key(), "o", "e")
				soStd(ctx, st, k1.Key(), "", "")
				soStd(ctx, st, otherMoved.Key(), "o2", "")
				So(inspectedCount(st, kindStdOut), ShouldEqual, 2)
				So(inspectedCount(st, kindStdErr), ShouldEqual, 1)
				soLive(st, k1.Key(), false)
				soLive(st, other.Key(), false)
				So(storedLiveJob(t, st, k2.Key()).Cmd, ShouldEqual, k2.Cmd)
			}

			eachReopen(ctx, &s, check)

			cut := checkpointed(s)
			So(walFilesBelow(s.dir, cut), ShouldBeEmpty)
			eachReopen(ctx, &s, check)
		})

		Convey("a job modified keeping its key keeps its std, takes the new spec and keeps its run state", func() {
			job := failedWithStd(ctx, s, "echo c4 std same key", "o", "e")
			same := modifiedCopy(job, job.Cmd)
			same.Priority = 7
			same.Attempts = job.Attempts

			So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{same}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soStd(ctx, st, job.Key(), "o", "e")

				got := storedLiveJob(t, st, job.Key())
				So(got.Priority, ShouldEqual, 7)
				So(got.Attempts, ShouldEqual, job.Attempts)
				So(inspectedCount(st, kindLive), ShouldEqual, 1)
			})
		})

		Convey("a modify to a new rep group finds the new key's completion under it once folded", func() {
			k1 := testDBJob("echo c4 modify rep group", "c4before")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k1}, false, nil)
			So(err, ShouldBeNil)

			k2 := modifiedCopy(k1, "echo c4 modify rep group to k2")
			k2.RepGroup = "c4after"
			So(s.modifyLiveJobs(ctx, []string{k1.Key()}, []*Job{k2}), ShouldBeNil)

			k2.State = JobStateComplete
			k2.EndTime = time.Now()
			_, err = s.archiveCompletion(k2.Key(), k2)
			So(err, ShouldBeNil)

			foldOverlay(s)

			eachReopen(ctx, &s, func(st *logStore) {
				byRG, errr := st.retrieveCompleteJobsByRepGroup("c4after")
				So(errr, ShouldBeNil)
				So(jobKeys(byRG), ShouldResemble, []string{k2.Key()})
			})
		})

		Convey("a modify adding a dep group not seen before makes it seen, also after reopen", func() {
			k1 := testDBJob("echo c4 modify dep group", "c4")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k1}, false, nil)
			So(err, ShouldBeNil)

			k2 := modifiedCopy(k1, k1.Cmd)
			k2.DepGroups = []string{"c4new"}
			So(s.modifyLiveJobs(ctx, []string{k1.Key()}, []*Job{k2}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				seen, errs := st.depGroupEverSeen("c4new")
				So(errs, ShouldBeNil)
				So(seen, ShouldBeTrue)
			})
		})

		Convey("a modify with mismatched keys and jobs is refused", func() {
			job := testDBJob("echo c4 modify mismatched", "c4")
			So(s.modifyLiveJobs(ctx, []string{job.Key(), job.Key()}, []*Job{job}), ShouldNotBeNil)
		})

		Convey("a running job marked to run again keeps the mark across reopen, and its archive keeps it live", func() {
			job := testDBJob("echo c4 rerun mark", "c4mark")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			runner := uuid.Must(uuid.NewV4())
			job.State = JobStateRunning
			job.ReservedBy = runner
			job.StartTime = time.Now()
			So(s.updateJobRunStateDurable(job), ShouldBeNil)

			job.RerunAfterRun = true
			before := len(walFrames(s.dir))
			So(s.storeRunningRerunMarks([]*Job{job}), ShouldBeNil)

			frames := walFrames(s.dir)[before:]
			So(frames, ShouldHaveLength, 1)
			So(frames[0].recs, ShouldResemble, []logRecordType{logRecState})

			readded := receiveStored(addAsync(ctx, s, testDBJob("echo c4 rerun mark", "c4mark")))
			So(readded.err, ShouldBeNil)
			So(readded.stored.jobsToQueue, ShouldHaveLength, 1)

			s = reopenLogStore(ctx, s)
			marked := storedLiveJob(t, s, job.Key())
			So(marked.RerunAfterRun, ShouldBeTrue)
			So(marked.ReservedBy, ShouldEqual, runner)

			job.State = JobStateComplete
			job.EndTime = time.Now()
			outcome, err := s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)
			So(outcome, ShouldEqual, archiveKeptLive)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, job.Key(), true)
				soComplete(st, job.Key())

				live := storedLiveJob(t, st, job.Key())
				So(live.RerunAfterRun, ShouldBeFalse)
				So(live.ReservedBy, ShouldEqual, uuid.UUID{})

				status, errs := st.retrieveCompleteJobStatusByRepGroup("c4mark", false)
				So(errs, ShouldBeNil)
				So(status.Counts[JobStateComplete], ShouldEqual, 0)
			})
		})

		Convey("rerun marks are not stored for a job not live, not marked, or with an archive pending, and "+
			"leave no write in flight that a re-add of the jobs waits for", func() {
			gone := testDBJob("echo c4 mark gone", "c4")
			gone.RerunAfterRun = true

			unmarked := testDBJob("echo c4 mark unmarked", "c4")
			archiving := testDBJob("echo c4 mark archiving", "c4")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{unmarked, archiving}, false, nil)
			So(err, ShouldBeNil)

			archiving.RerunAfterRun = true
			archiving.archivesPending = 1

			before := len(walFrames(s.dir))
			So(s.storeRunningRerunMarks([]*Job{gone, unmarked, archiving}), ShouldBeNil)
			So(walFrames(s.dir), ShouldHaveLength, before)
			So(s.storeRunningRerunMarks(nil), ShouldBeNil)

			readded := addAsync(ctx, s, testDBJob(gone.Cmd, gone.RepGroup),
				testDBJob(unmarked.Cmd, unmarked.RepGroup), testDBJob(archiving.Cmd, archiving.RepGroup))
			r := receiveStored(readded)
			So(r.err, ShouldBeNil)
			So(r.stored.jobsToQueue, ShouldHaveLength, 3)
		})
	})

	Convey("Given live jobs k1, k2 and k3, each with a state record and an exit block snapshotted but not appended",
		t, func() {
			s, _ := openTestLogStore(t, ctx)

			k1 := testDBJob("echo c4 held archived", "c4held")
			k2 := testDBJob("echo c4 held deleted", "c4held")
			k3 := failedWithStd(ctx, s, "echo c4 held modified", "x", "y")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{k1, k2}, false, nil)
			So(err, ShouldBeNil)

			held := make([]heldWrite, 0, 6)

			for _, job := range []*Job{k1, k2, k3} {
				job.State = JobStateBuried
				job.Exitcode = 1
				job.Attempts++
				held = append(held, holdStateWrite(s, job), holdExitWrite(s, job, "o", "e"))
			}

			k1.State = JobStateComplete
			k1.Exitcode = 0
			k1.EndTime = time.Now()
			_, err = s.archiveCompletion(k1.Key(), k1)
			So(err, ShouldBeNil)
			So(s.deleteLiveJobs(ctx, []string{k2.Key()}), ShouldBeNil)

			k4 := modifiedCopy(k3, "echo c4 held modified to k4")
			So(s.modifyLiveJobs(ctx, []string{k3.Key()}, []*Job{k4}), ShouldBeNil)

			stdOuts, stdErrs := inspectedCount(s, kindStdOut), inspectedCount(s, kindStdErr)
			So(stdOuts, ShouldEqual, 1)

			appendHeld := func() {
				for _, h := range held {
					So(h.write(s), ShouldBeNil)
				}
			}

			check := func(st *logStore) {
				for _, job := range []*Job{k1, k2, k3} {
					soLive(st, job.Key(), false)
				}

				So(inspectedCount(st, kindLive), ShouldEqual, 1)
				So(inspectedCount(st, kindStdOut), ShouldEqual, stdOuts)
				So(inspectedCount(st, kindStdErr), ShouldEqual, stdErrs)
				soStd(ctx, st, k4.Key(), "x", "y")

				readded := testDBJob("echo c4 held archived", "c4held")
				stored, erra := st.storeNewJobsGuarded(ctx, []*Job{readded}, true, nil)
				So(erra, ShouldBeNil)
				So(stored.alreadyAdded.Complete, ShouldEqual, 1)
				So(stored.jobsToQueue, ShouldBeEmpty)
			}

			Convey("the held records, appended after the archive, delete and modify, recreate nothing, also after "+
				"reopen", func() {
				appendHeld()
				check(s)
				check(reopenLogStore(ctx, s))
			})

			Convey("the held records, appended after the archive, delete and modify, recreate nothing after a "+
				"checkpoint then reopen", func() {
				appendHeld()
				checkpointed(s)
				check(s)
				check(reopenLogStore(ctx, s))
			})

			Convey("the held records, appended after a checkpoint made after the archive, delete and modify, "+
				"recreate nothing, also after reopen", func() {
				checkpointed(s)
				appendHeld()
				check(s)
				check(reopenLogStore(ctx, s))
			})
		})
}

// eachReopen calls check with *s, then reopens *s, as a restart would, and
// calls check with the reopened store, which *s becomes.
func eachReopen(ctx context.Context, s **logStore, check func(st *logStore)) {
	check(*s)

	*s = reopenLogStore(ctx, *s)
	check(*s)
}

// soLive asserts whether the keyed job is live in st.
func soLive(st *logStore, key string, want bool) {
	live, err := st.checkIfLive(key)
	So(err, ShouldBeNil)
	So(live, ShouldEqual, want)
}

// truncateLastWALByte removes the last byte of the last wal file in dir.
func truncateLastWALByte(dir string) {
	files, err := walFileNumbers(dir)
	So(err, ShouldBeNil)
	So(files, ShouldNotBeEmpty)

	path := walFilePath(dir, files[len(files)-1])

	info, err := os.Stat(path)
	So(err, ShouldBeNil)
	So(os.Truncate(path, info.Size()-1), ShouldBeNil)
}

// modifiedCopy returns a new job with job's spec and Cmd cmd, as a modify of
// job would make it.
func modifiedCopy(job *Job, cmd string) *Job {
	copied := testDBJob(cmd, job.RepGroup)
	copied.Dependencies = job.Dependencies
	copied.DepGroups = slices.Clone(job.DepGroups)

	return copied
}

// soComplete asserts that the keyed job is complete in st.
func soComplete(st *logStore, key string) {
	complete, err := st.checkIfComplete(key)
	So(err, ShouldBeNil)
	So(complete, ShouldBeTrue)
}

// failedWithStd adds a job of cmd to s and records an exit 1 of it with std
// stdo and stde, returning the job.
func failedWithStd(ctx context.Context, s *logStore, cmd, stdo, stde string) *Job {
	job := testDBJob(cmd, "c4")
	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	job.State = JobStateBuried
	job.Exited = true
	job.Exitcode = 1
	job.Attempts = 1
	So(s.updateJobAfterExitDurable(job, []byte(stdo), []byte(stde), false), ShouldBeNil)

	return job
}

// soStd asserts the keyed job's std in st.
func soStd(ctx context.Context, st *logStore, key, wantOut, wantErr string) {
	stdo, stde := st.retrieveJobStd(ctx, key)
	So(string(stdo), ShouldEqual, wantOut)
	So(string(stde), ShouldEqual, wantErr)
}

// foldOverlay folds everything in s's overlay into history.bolt, as the folder
// does, up to the durable end of the log.
func foldOverlay(s *logStore) {
	So(s.foldNow(), ShouldBeNil)
}

// appendedHold is a logAppendedHook that holds the first write to append after
// arm, before that write has its Refs, until Release.
type appendedHold struct {
	armed            atomic.Bool
	entered, release chan struct{}
	releaseOnce      sync.Once
}

// newAppendedHold installs an appendedHold, released and uninstalled when the
// Convey block ends. Make it before the store, so that the release comes
// before the store's close.
func newAppendedHold() *appendedHold {
	h := &appendedHold{entered: make(chan struct{}), release: make(chan struct{})}

	logAppendedHook = func() {
		if h.armed.CompareAndSwap(true, false) {
			close(h.entered)
			<-h.release
		}
	}

	Reset(func() {
		h.Release()

		logAppendedHook = nil
	})

	return h
}

// arm makes the next write to append hold.
func (h *appendedHold) arm() {
	h.armed.Store(true)
}

// Release lets the held write go on.
func (h *appendedHold) Release() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// awaitEntered waits for a write to be held.
func (h *appendedHold) awaitEntered() {
	select {
	case <-h.entered:
	case <-time.After(logSyncHoldWait):
		So("no write was held", ShouldBeEmpty)
	}
}

func TestLogStoreArchiveRacesRemoval(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job archived, folded, put back live and deleted", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c4 archive after delete", "c4late")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateComplete
		job.EndTime = time.Now().Add(-time.Hour)
		_, err = s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)
		foldOverlay(s)

		So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)
		So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

		later := time.Now().Truncate(time.Second)
		job.EndTime = later

		Convey("a later archive of it records the new completion, and leaves it not live, also after reopen", func() {
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, job.Key(), false)
				soCompletedAt(st, job, later)
			})
		})
	})

	Convey("Given a live job whose delete is appended but held before it is durable", t, func() {
		hold := newLogSyncHold()
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c4 archive racing delete", "c4race")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		hold.arm()

		deleted := make(chan error, 1)
		go func() { deleted <- s.deleteLiveJobs(ctx, []string{job.Key()}) }()

		hold.awaitEntered()

		Convey("its archive meanwhile waits for the delete, then records the completion, also after reopen", func() {
			end := time.Now().Truncate(time.Second)
			job.State = JobStateComplete
			job.EndTime = end

			archived := make(chan error, 1)

			go func() {
				_, erra := s.archiveCompletion(job.Key(), job)
				archived <- erra
			}()

			So(receiveWithin(archived, logStorePlanWait), ShouldEqual, errOutageTestPending)
			hold.Release()

			So(receiveWithin(deleted, logSyncHoldWait), ShouldBeNil)
			So(receiveWithin(archived, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, job.Key(), false)
				soCompletedAt(st, job, end)
			})
		})
	})

	Convey("Given a running job whose archive is appended but held before it has its Refs, so before it applies",
		t, func() {
			hold := newAppendedHold()
			s, _ := openTestLogStore(t, ctx)

			job := testDBJob("echo c4 delete racing archive", "c4race")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			end := time.Now().Truncate(time.Second)
			job.State = JobStateComplete
			job.EndTime = end

			hold.arm()

			archived := make(chan error, 1)

			go func() {
				_, erra := s.archiveCompletion(job.Key(), job)
				archived <- erra
			}()

			hold.awaitEntered()

			Convey("a delete of it meanwhile applies after the archive, leaving it complete and not live", func() {
				deleted := make(chan error, 1)
				go func() { deleted <- s.deleteLiveJobs(ctx, []string{job.Key()}) }()

				So(receiveWithin(deleted, logGuardHoldWait), ShouldEqual, errOutageTestPending)
				hold.Release()

				So(receiveWithin(archived, logSyncHoldWait), ShouldBeNil)
				So(receiveWithin(deleted, logSyncHoldWait), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, job.Key(), false)
					soCompletedAt(st, job, end)
				})
			})
		})

	Convey("Given a queued job whose re-add is appended but held before it has its Refs, so before it applies",
		t, func() {
			hold := newAppendedHold()
			s, _ := openTestLogStore(t, ctx)

			job := testDBJob("echo c4 delete racing re-add", "c4race")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			hold.arm()

			added := addAsync(ctx, s, testDBJob("echo c4 delete racing re-add", "c4race"))

			hold.awaitEntered()

			removed := make(chan error, 1)

			Convey("a delete of it waits for the re-add to apply, so it is not live, also after reopen", func() {
				go func() { removed <- s.deleteLiveJobs(ctx, []string{job.Key()}) }()

				So(receiveWithin(removed, logGuardHoldWait), ShouldEqual, errOutageTestPending)
				hold.Release()

				So(receiveStored(added).err, ShouldBeNil)
				So(receiveWithin(removed, logSyncHoldWait), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, job.Key(), false)
				})
			})

			Convey("a modify of it to a new key waits for the re-add to apply, so only the new key is live, also "+
				"after reopen", func() {
				moved := modifiedCopy(job, "echo c4 modify racing re-add")
				go func() { removed <- s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}) }()

				So(receiveWithin(removed, logGuardHoldWait), ShouldEqual, errOutageTestPending)
				hold.Release()

				So(receiveStored(added).err, ShouldBeNil)
				So(receiveWithin(removed, logSyncHoldWait), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, job.Key(), false)
					soLive(st, moved.Key(), true)
				})
			})
		})

	Convey("Given a running job marked to run again once decided on by its archive, which is not kept live", t,
		func() {
			s, _ := openTestLogStore(t, ctx)

			job := testDBJob("echo c4 archive racing put back", "c4race")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			job.State = JobStateComplete
			job.EndTime = time.Now()

			parked := signalAddWaits()
			putBack := make(chan error, 1)

			logArchiveDecidedHook = func() {
				logArchiveDecidedHook = nil

				job.Lock()
				job.RerunAfterRun = true
				job.Unlock()

				go func() { putBack <- s.storeLiveForRerun([]*Job{job}) }()

				select {
				case <-parked:
				case <-time.After(logGuardHoldWait):
				}
			}

			Reset(func() { logArchiveDecidedHook = nil })

			Convey("the put back made after that decision, waiting for the archive's claim, leaves it live, and "+
				"its completion is recorded unmarked, also after reopen", func() {
				outcome, erra := s.archiveCompletion(job.Key(), job)
				So(erra, ShouldBeNil)
				So(receiveWithin(putBack, logSyncHoldWait), ShouldBeNil)
				So(outcome, ShouldEqual, archiveRemovedLive)

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, job.Key(), true)
					soComplete(st, job.Key())

					completed, errk := st.retrieveCompleteJobsByKeys([]string{job.Key()})
					So(errk, ShouldBeNil)
					So(completed, ShouldHaveLength, 1)
					So(completed[0].RerunAfterRun, ShouldBeFalse)
				})
			})
		})
}

// soCompletedAt asserts that st's history entry of job, found by key and by
// its rep group's last completion time, ended at end.
func soCompletedAt(st *logStore, job *Job, end time.Time) {
	soComplete(st, job.Key())

	byKey, err := st.retrieveCompleteJobsByKeys([]string{job.Key()})
	So(err, ShouldBeNil)
	So(byKey, ShouldHaveLength, 1)
	So(byKey[0].EndTime.Equal(end), ShouldBeTrue)

	last, err := st.retrieveLastCompletionTimeByRepGroup([]string{job.RepGroup})
	So(err, ShouldBeNil)
	So(last[job.RepGroup].Equal(end), ShouldBeTrue)
}

func TestLogStoreClaimedWrites(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given job k added and deleted, and archived job x depending on dep group g", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		x := testDBJob("echo c4 claim x", "c4claim")
		x.Dependencies = Dependencies{NewDepGroupDependency("g")}
		archiveNew(ctx, s, x)

		k := testDBJob("echo c4 claim k", "c4claim")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{k}, false, nil)
		So(err, ShouldBeNil)
		So(s.deleteLiveJobs(ctx, []string{k.Key()}), ShouldBeNil)

		Convey("a stale archive of k, made once a re-add of k joining g has taken its seqs, applies after the "+
			"re-add, so k is complete and not live, also after reopen", func() {
			parked := signalAddWaits()

			stale := testDBJob(k.Cmd, k.RepGroup)
			stale.State = JobStateComplete
			stale.EndTime = time.Now()

			readd := testDBJob(k.Cmd, k.RepGroup)
			readd.DepGroups = []string{"g"}

			var archived <-chan archivedResult

			logPutBackHook = func() {
				logPutBackHook = nil

				hold.arm()

				archived = archiveAsync(s, stale)

				select {
				case <-parked:
					hold.armed.Store(false)
				case <-hold.entered:
				case <-time.After(logSyncHoldWait):
					So("the archive neither waited nor appended", ShouldBeEmpty)
				}
			}

			Reset(func() { logPutBackHook = nil })

			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{readd}, false, nil)
			So(erra, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 2)

			hold.Release()
			So(receiveArchived(archived, logSyncHoldWait).err, ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, k.Key(), false)
				soComplete(st, k.Key())
				soLive(st, x.Key(), true)
			})
		})

		Convey("a delete of x made while an add of a member of g putting x back is appended but not applied "+
			"waits for it, so x is not live, also after reopen", func() {
			member := testDBJob("echo c4 claim member", "c4claim")
			member.DepGroups = []string{"g"}

			raceHeldWrite(hold, func() error {
				_, errs := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)

				return errs
			}, func() error {
				return s.deleteLiveJobs(ctx, []string{x.Key()})
			})

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, x.Key(), false)
				soLive(st, member.Key(), true)
			})
		})

		Convey("a delete of x made while its put back (storeLiveForRerun) is appended but not applied waits for "+
			"it, so x is not live, also after reopen", func() {
			raceHeldWrite(hold, func() error {
				return s.storeLiveForRerun([]*Job{x})
			}, func() error {
				return s.deleteLiveJobs(ctx, []string{x.Key()})
			})

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, x.Key(), false)
				soComplete(st, x.Key())
			})
		})

		Convey("put back live, a modify of x to a new key made while a lookup of x under another rep group is "+
			"appended but not applied waits for it, so that rep group does not find x, also after reopen", func() {
			So(s.storeLiveForRerun([]*Job{x}), ShouldBeNil)

			again := testDBJob(x.Cmd, "c4claim2")
			moved := modifiedCopy(x, "echo c4 claim x moved")

			raceHeldWrite(hold, func() error {
				return s.storeRepGroupLookups(ctx, []*Job{again})
			}, func() error {
				return s.modifyLiveJobs(ctx, []string{x.Key()}, []*Job{moved})
			})

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, x.Key(), false)
				soLive(st, moved.Key(), true)

				byRG, errr := st.retrieveCompleteJobsByRepGroup("c4claim2")
				So(errr, ShouldBeNil)
				So(byRG, ShouldBeEmpty)
			})
		})

		member := testDBJob("echo c4 claim member", "c4claim")
		member.DepGroups = []string{"g"}

		addMember := func(stored *storedNewJobs) func() error {
			return func() error {
				var err error

				*stored, err = s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)

				return err
			}
		}

		Convey("an add of a member of g made while x's put back (storeLiveForRerun) is appended but not applied "+
			"waits for it, then updates x rather than putting it back again", func() {
			var stored storedNewJobs

			raceHeldWrite(hold, func() error {
				return s.storeLiveForRerun([]*Job{x})
			}, addMember(&stored))

			So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{member.Key()})
			So(jobKeys(stored.dependents.jobs), ShouldResemble, []string{x.Key()})
		})

		Convey("an add of a member of g made while a re-add of x is appended but not applied waits for it, "+
			"then updates x rather than putting it back again", func() {
			var stored storedNewJobs

			readd := testDBJob(x.Cmd, x.RepGroup)
			readd.Dependencies = x.Dependencies

			raceHeldWrite(hold, func() error {
				_, err := s.storeNewJobsGuarded(ctx, []*Job{readd}, false, nil)

				return err
			}, addMember(&stored))

			So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{member.Key()})
			So(jobKeys(stored.dependents.jobs), ShouldResemble, []string{x.Key()})
		})

		Convey("an add of a member of g made while a lookup of x under another rep group is appended but not "+
			"applied plans again once it applies, and puts x back, also after reopen", func() {
			var stored storedNewJobs

			raceHeldWrite(hold, func() error {
				return s.storeRepGroupLookups(ctx, []*Job{testDBJob(x.Cmd, "c4claim2")})
			}, addMember(&stored))

			So(jobKeys(stored.jobsToQueue), ShouldResemble, sortedKeys(member, x))

			eachReopen(ctx, &s, func(st *logStore) { soLive(st, x.Key(), true) })
		})

		Convey("a modify of x to key k made while a stale archive of k under another rep group is appended but "+
			"not applied waits for it, so k is complete under that rep group and live, also after reopen", func() {
			So(s.storeLiveForRerun([]*Job{x}), ShouldBeNil)

			stale := testDBJob(k.Cmd, "c4stale")
			stale.State = JobStateComplete
			stale.EndTime = time.Now()

			moved := modifiedCopy(x, k.Cmd)
			So(moved.Key(), ShouldEqual, k.Key())

			raceHeldWrite(hold, func() error {
				_, err := s.archiveCompletion(k.Key(), stale)

				return err
			}, func() error {
				return s.modifyLiveJobs(ctx, []string{x.Key()}, []*Job{moved})
			})

			eachReopen(ctx, &s, func(st *logStore) {
				done, errk := st.retrieveCompleteJobsByKeys([]string{k.Key()})
				So(errk, ShouldBeNil)
				So(done, ShouldHaveLength, 1)
				So(done[0].RepGroup, ShouldEqual, "c4stale")
				soLive(st, k.Key(), true)
			})
		})
	})
	Convey("Given a running job whose archive is appended but held before it is durable", t, func() {
		hold := newLogSyncHold()
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c4 claim re-add after archive", "c4claim")
		job.State = JobStateRunning
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		Convey("a re-add of it waits for the archive to apply, so stores it live again, also after reopen", func() {
			parked := signalAddWaits()

			job.State = JobStateComplete
			job.EndTime = time.Now()

			hold.arm()

			archived := archiveAsync(s, job)

			hold.awaitEntered()

			added := addAsync(ctx, s, testDBJob(job.Cmd, job.RepGroup))

			awaitParkedOrWriting(s, parked)
			hold.Release()

			So(receiveArchived(archived, logSyncHoldWait).err, ShouldBeNil)
			So(receiveStored(added).err, ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, job.Key(), true)
				soComplete(st, job.Key())
			})
		})
	})

	Convey("Given live job x that exited 1 with std, whose delete is appended but not applied", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		x := failedWithStd(ctx, s, "echo c4 claim std", "out", "err")
		moved := modifiedCopy(x, "echo c4 claim std moved")

		Convey("a modify of it to a new key waits for the delete before reading its std, so the new key has "+
			"none, also after reopen", func() {
			raceHeldWrite(hold, func() error {
				return s.deleteLiveJobs(ctx, []string{x.Key()})
			}, func() error {
				return s.modifyLiveJobs(ctx, []string{x.Key()}, []*Job{moved})
			})

			eachReopen(ctx, &s, func(st *logStore) {
				soStd(ctx, st, moved.Key(), "", "")
			})
		})
	})

	Convey("Given archived jobs p and q depending on dep group g, p's key sorting first", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		p := testDBJob("echo c4 replan p", "c4replan")
		q := testDBJob("echo c4 replan q", "c4replan")

		if q.Key() < p.Key() {
			p, q = q, p
		}

		for _, job := range []*Job{p, q} {
			job.Dependencies = Dependencies{NewDepGroupDependency("g")}
			archiveNew(ctx, s, job)
		}

		Convey("an add of a member of g that puts p back, then finds q's key claimed by a rep group lookup "+
			"appended but not applied, plans again once it has applied, putting both back, also after reopen",
			func() {
				parked := signalAddWaits()

				hold.arm()

				looked := make(chan error, 1)
				go func() { looked <- s.storeRepGroupLookups(ctx, []*Job{q}) }()

				hold.awaitEntered()

				member := testDBJob("echo c4 replan member", "c4replan")
				member.DepGroups = []string{"g"}

				added := addAsync(ctx, s, member)

				awaitParked(parked)
				hold.Release()

				So(receiveWithin(looked, logSyncHoldWait), ShouldBeNil)

				r := receiveStored(added)
				So(r.err, ShouldBeNil)
				So(sortedKeys(r.stored.jobsToQueue...), ShouldResemble, sortedKeys(p, q, member))

				eachReopen(ctx, &s, func(st *logStore) {
					soLive(st, p.Key(), true)
					soLive(st, q.Key(), true)
				})
			})
	})

	Convey("Given archived jobs a and d, depending on dep groups g2 and g1", t, func() {
		s, _ := openTestLogStore(t, ctx)

		a := testDBJob("echo c4 crossed a", "c4crossed")
		a.Dependencies = Dependencies{NewDepGroupDependency("g2")}
		d := testDBJob("echo c4 crossed d", "c4crossed")
		d.Dependencies = Dependencies{NewDepGroupDependency("g1")}

		archiveNew(ctx, s, a)
		archiveNew(ctx, s, d)

		Convey("an add of a with a member of g1 and an add of d with a member of g2, each finding the other's "+
			"key claimed as it is about to put it back, both finish with a and d live, also after reopen", func() {
			meetPutBacks(2)

			readd := func(job *Job, cmd, depGroup string) []*Job {
				again := testDBJob(job.Cmd, job.RepGroup)
				again.Dependencies = job.Dependencies

				member := testDBJob(cmd, "c4crossed")
				member.DepGroups = []string{depGroup}

				return []*Job{again, member}
			}

			first := addAsync(ctx, s, readd(a, "echo c4 crossed g1 member", "g1")...)
			second := addAsync(ctx, s, readd(d, "echo c4 crossed g2 member", "g2")...)

			So(receiveStored(first).err, ShouldBeNil)
			So(receiveStored(second).err, ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, a.Key(), true)
				soLive(st, d.Key(), true)
			})
		})
	})

	Convey("Given a key claimed by one write, and two more writes waiting to claim it", t, func() {
		s, _ := openTestLogStore(t, ctx)

		k, err := parseLogKey(testDBJob("echo c4 claim one at a time", "c4claim").Key())
		So(err, ShouldBeNil)

		waits := make(chan struct{}, 4)
		logStoreWaitHook = func() {
			select {
			case waits <- struct{}{}:
			default:
			}
		}

		Reset(func() { logStoreWaitHook = nil })

		first := s.claimKeySet(k)
		claimed := make(chan *logClaim, 2)

		for range 2 {
			go func() { claimed <- s.claimKeySet(k) }()
		}

		awaitParked(waits)
		awaitParked(waits)

		Convey("once the first releases it, one of them claims it and the other waits again until that one "+
			"releases it", func() {
			first.release()

			second := receiveClaim(claimed, "neither waiting write claimed the key")

			select {
			case third := <-claimed:
				third.release()
				So("both waiting writes claimed the key", ShouldBeEmpty)
			case <-waits:
			case <-time.After(logSyncHoldWait):
				So("the other waiting write neither claimed the key nor waited again", ShouldBeEmpty)
			}

			second.release()
			receiveClaim(claimed, "the last waiting write never claimed the key").release()
		})
	})
}

// archiveNew adds job to s and archives its successful completion.
func archiveNew(ctx context.Context, s *logStore, job *Job) {
	_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
	So(err, ShouldBeNil)

	job.State = JobStateComplete
	job.EndTime = time.Now()
	_, err = s.archiveCompletion(job.Key(), job)
	So(err, ShouldBeNil)
}

// sortedKeys returns the keys of jobs, sorted.
func sortedKeys(jobs ...*Job) []string {
	keys := jobKeys(jobs)
	slices.Sort(keys)

	return keys
}

// awaitParked waits for a write to signal on parked that it is about to wait,
// failing if none does within logSyncHoldWait.
func awaitParked(parked <-chan struct{}) {
	select {
	case <-parked:
	case <-time.After(logSyncHoldWait):
		So("no write waited", ShouldBeEmpty)
	}
}

// meetPutBacks makes the first n adds to read archived dependents to put back
// wait for each other there, before any decides on them, so each plans its
// put-backs with the others' keys claimed; later ones go straight on. It
// waits at most logSyncHoldWait for them all to arrive.
func meetPutBacks(n int32) {
	var arrived atomic.Int32

	all := make(chan struct{})

	logPutBackHook = func() {
		switch k := arrived.Add(1); {
		case k > n:
			return
		case k == n:
			close(all)
		}

		select {
		case <-all:
		case <-time.After(logSyncHoldWait):
		}
	}

	Reset(func() { logPutBackHook = nil })
}

// receiveClaim returns the claim that arrives on ch, failing with what if none
// does within logSyncHoldWait.
func receiveClaim(ch <-chan *logClaim, what string) *logClaim {
	select {
	case c := <-ch:
		return c
	case <-time.After(logSyncHoldWait):
		So(what, ShouldBeEmpty)

		return nil
	}
}

func TestLogStoreRevivedKeyTransitions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	bury := func(s *logStore, job *Job) func() error {
		return func() error {
			job.Lock()
			job.State = JobStateBuried
			job.FailReason = "c4 buried meanwhile"
			job.Unlock()

			return s.updateJobAfterChangeDurable(job)
		}
	}

	soBuried := func(st *logStore, key string) {
		job := storedLiveJob(t, st, key)
		So(job.State, ShouldEqual, JobStateBuried)
		So(job.FailReason, ShouldEqual, "c4 buried meanwhile")
	}

	Convey("Given live job x modified to a new key k, the modify appended but not applied", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		x := testDBJob("echo c4 revived x", "c4revived")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{x}, false, nil)
		So(err, ShouldBeNil)

		moved := modifiedCopy(x, "echo c4 revived moved")
		modify := func() error { return s.modifyLiveJobs(ctx, []string{x.Key()}, []*Job{moved}) }

		Convey("a state record of k made meanwhile waits for the modify, so k ends with it, also after reopen",
			func() {
				raceHeldWrite(hold, modify, bury(s, moved))

				eachReopen(ctx, &s, func(st *logStore) { soBuried(st, moved.Key()) })
			})

		Convey("an exit block of k made meanwhile waits for the modify, so k ends with its std, also after reopen",
			func() {
				raceHeldWrite(hold, modify, func() error {
					moved.Lock()
					moved.State = JobStateBuried
					moved.Exited = true
					moved.Exitcode = 1
					moved.Unlock()

					return s.updateJobAfterExitDurable(moved, []byte("out"), []byte("err"), false)
				})

				eachReopen(ctx, &s, func(st *logStore) {
					So(storedLiveJob(t, st, moved.Key()).State, ShouldEqual, JobStateBuried)
					soStd(ctx, st, moved.Key(), "out", "err")
				})
			})
	})

	Convey("Given live job x, and an add of a member of dep group g, which x depends on, appended but not "+
		"applied while holding guardMu", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)
		parked := signalAddWaits()

		x := testDBJob("echo c4 revived lock", "c4revived")
		x.Dependencies = Dependencies{NewDepGroupDependency("g")}
		_, err := s.storeNewJobsGuarded(ctx, []*Job{x}, false, nil)
		So(err, ShouldBeNil)

		member := testDBJob("echo c4 revived lock member", "c4revived")
		member.DepGroups = []string{"g"}

		hold.arm()

		added := addAsync(ctx, s, member)

		hold.awaitEntered()

		Convey("a modify of x to a new key k, waiting for guardMu to make its records, and a state record of k "+
			"waiting for the modify, do not stop k's job being locked meanwhile, so all finish", func() {
			moved := modifiedCopy(x, "echo c4 revived lock moved")

			modified := make(chan error, 1)
			go func() { modified <- s.modifyLiveJobs(ctx, []string{x.Key()}, []*Job{moved}) }()

			awaitReviving(s, moved.Key())

			buried := make(chan error, 1)

			go func() {
				buried <- bury(s, moved)()
			}()

			select {
			case <-parked:
			case <-time.After(logSyncHoldWait):
				So("the state record never waited for the modify", ShouldBeEmpty)
			}

			locked := make(chan error, 1)

			go func() {
				moved.Lock()
				moved.Priority = 3
				moved.Unlock()

				locked <- nil
			}()

			So(receiveWithin(locked, logSyncHoldWait), ShouldBeNil)
			hold.Release()

			So(receiveStored(added).err, ShouldBeNil)
			So(receiveWithin(modified, logSyncHoldWait), ShouldBeNil)
			So(receiveWithin(buried, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) { soBuried(st, moved.Key()) })
		})
	})

	Convey("Given archived job x, and a transition or exit of x that has taken its records' seqs", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		x := testDBJob("echo c4 revived exit", "c4revived")
		archiveNew(ctx, s, x)

		again := testDBJob(x.Cmd, x.RepGroup)
		putBack := make(chan error, 1)

		logTransitionSeqsHook = func() {
			logTransitionSeqsHook = nil

			hold.arm()

			go func() { putBack <- s.storeLiveForRerun([]*Job{again}) }()

			hold.awaitEntered()
		}

		Reset(func() { logTransitionSeqsHook = nil })

		Convey("a put back of x appended meanwhile, applying after the exit, leaves x with no std of the exit, "+
			"also after reopen", func() {
			stale := testDBJob(x.Cmd, x.RepGroup)
			stale.State = JobStateBuried
			stale.Exited = true
			stale.Exitcode = 1
			So(s.updateJobAfterExitDurable(stale, []byte("out"), []byte("err"), false), ShouldBeNil)

			hold.Release()
			So(receiveWithin(putBack, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				soLive(st, x.Key(), true)
				soStd(ctx, st, x.Key(), "", "")
			})
		})

		Convey("a put back of x appended meanwhile, applying after the state record, leaves x with its own "+
			"state, also after reopen", func() {
			So(bury(s, testDBJob(x.Cmd, x.RepGroup))(), ShouldBeNil)

			hold.Release()
			So(receiveWithin(putBack, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(storedLiveJob(t, st, x.Key()).State, ShouldNotEqual, JobStateBuried)
			})
		})
	})

	Convey("Given archived job x whose put back (storeLiveForRerun) is appended but not applied", t, func() {
		hold := newAppendedHold()
		s, _ := openTestLogStore(t, ctx)

		x := testDBJob("echo c4 revived put back", "c4revived")
		archiveNew(ctx, s, x)

		again := testDBJob(x.Cmd, x.RepGroup)

		Convey("a state record of x made meanwhile waits for the put back, so x ends with it, also after reopen",
			func() {
				raceHeldWrite(hold, func() error {
					return s.storeLiveForRerun([]*Job{again})
				}, bury(s, again))

				eachReopen(ctx, &s, func(st *logStore) { soBuried(st, x.Key()) })
			})

		Convey("a rerun mark of x made meanwhile waits for the put back, so is kept, also after reopen", func() {
			raceHeldWrite(hold, func() error {
				return s.storeLiveForRerun([]*Job{again})
			}, func() error {
				again.Lock()
				again.State = JobStateRunning
				again.RerunAfterRun = true
				again.Unlock()

				return s.storeRunningRerunMarks([]*Job{again})
			})

			eachReopen(ctx, &s, func(st *logStore) {
				So(storedLiveJob(t, st, x.Key()).RerunAfterRun, ShouldBeTrue)
			})
		})
	})

	Convey("Given archived job x whose put back (storeLiveForRerun) waits to make its records", t, func() {
		s, _ := openTestLogStore(t, ctx)

		x := testDBJob("echo c4 revived building", "c4revived")
		archiveNew(ctx, s, x)

		first, unlockFirst := lockedCopy(x)
		Reset(unlockFirst)

		firstDone := make(chan error, 1)
		go func() { firstDone <- s.storeLiveForRerun([]*Job{first}) }()

		awaitReviving(s, x.Key())

		parked := make(chan struct{}, 2)

		logStoreWaitHook = func() {
			select {
			case parked <- struct{}{}:
			default:
			}
		}

		Reset(func() { logStoreWaitHook = nil })

		Convey("a rerun mark of x made meanwhile waits for the put back, so is kept, also after reopen", func() {
			marker := testDBJob(x.Cmd, x.RepGroup)
			marker.State = JobStateRunning
			marker.RerunAfterRun = true

			marked := make(chan error, 1)
			go func() { marked <- s.storeRunningRerunMarks([]*Job{marker}) }()

			awaitParked(parked)
			unlockFirst()

			So(receiveWithin(firstDone, logSyncHoldWait), ShouldBeNil)
			So(receiveWithin(marked, logSyncHoldWait), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(storedLiveJob(t, st, x.Key()).RerunAfterRun, ShouldBeTrue)
			})
		})

		Convey("a state record of x waiting for the put back also waits for a second put back of x made once "+
			"the first has applied and before the state record looks again, so x ends with it, also after reopen",
			func() {
				buried := testDBJob(x.Cmd, x.RepGroup)

				buriedDone := make(chan error, 1)
				go func() { buriedDone <- bury(s, buried)() }()

				awaitParked(parked)

				buried.Lock()
				unlockBuried := sync.OnceFunc(buried.Unlock)
				Reset(unlockBuried)

				unlockFirst()
				So(receiveWithin(firstDone, logSyncHoldWait), ShouldBeNil)

				second, unlockSecond := lockedCopy(x)
				Reset(unlockSecond)

				secondDone := make(chan error, 1)
				go func() { secondDone <- s.storeLiveForRerun([]*Job{second}) }()

				awaitReviving(s, x.Key())
				unlockBuried()
				awaitParked(parked)
				unlockSecond()

				So(receiveWithin(secondDone, logSyncHoldWait), ShouldBeNil)
				So(receiveWithin(buriedDone, logSyncHoldWait), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) { soBuried(st, x.Key()) })
			})
	})
}

// awaitReviving waits until a write in flight is making the keyed job live in
// s, failing if none is within logSyncHoldWait.
func awaitReviving(s *logStore, key string) {
	awaitRevivingWrites(s, key, 1)
}

// awaitRevivingWrites waits until at least n writes in flight are making the
// keyed job live in s, failing if fewer are within logSyncHoldWait.
func awaitRevivingWrites(s *logStore, key string, n int) {
	k, err := parseLogKey(key)
	So(err, ShouldBeNil)

	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	deadline := time.After(logSyncHoldWait)

	for {
		s.stateMu.Lock()
		p := s.reviving[k]
		reviving := p != nil && p.n >= n
		s.stateMu.Unlock()

		if reviving {
			return
		}

		select {
		case <-tick.C:
		case <-deadline:
			So("too few writes made the job live", ShouldBeEmpty)

			return
		}
	}
}

// lockedCopy returns a new job with job's command and RepGroup, so its key,
// locked for writing, and the func that unlocks it, once: a write of it made
// meanwhile waits to make its records.
func lockedCopy(job *Job) (*Job, func()) {
	again := testDBJob(job.Cmd, job.RepGroup)
	again.Lock()

	return again, sync.OnceFunc(again.Unlock)
}

// raceHeldWrite runs first, holding its write once appended and before it has
// its Refs, so before it applies, then runs second, a write of one of the same
// keys, releasing first once second waits for first's claim, or has returned
// (applied before first) if it does not wait, and returns once both have
// returned without error.
func raceHeldWrite(hold *appendedHold, first, second func() error) {
	parked := signalAddWaits()

	hold.arm()

	firstDone := make(chan error, 1)
	go func() { firstDone <- first() }()

	hold.awaitEntered()

	secondDone := make(chan error, 1)
	go func() { secondDone <- second() }()

	var secondErr error

	select {
	case <-parked:
		hold.Release()

		secondErr = receiveWithin(secondDone, logSyncHoldWait)
	case secondErr = <-secondDone:
		hold.Release()
	case <-time.After(logSyncHoldWait):
		So("the second write neither waited nor returned", ShouldBeEmpty)
	}

	So(receiveWithin(firstDone, logSyncHoldWait), ShouldBeNil)
	So(secondErr, ShouldBeNil)
}

// modifyLookupStore is what TestStoreModifyDropsRepGroupLookups uses of a
// store, which the bbolt and log stores both have.
type modifyLookupStore interface {
	storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool, guard *rerunGuard) (storedNewJobs, error)
	archiveCompletion(key string, job *Job) (archiveOutcome, error)
	storeLiveForRerun(jobs []*Job) error
	storeRepGroupLookups(ctx context.Context, jobs []*Job) error
	modifyLiveJobs(ctx context.Context, oldKeys []string, jobs []*Job) error
	deleteLiveJobs(ctx context.Context, keys []string) error
	checkIfComplete(key string) (bool, error)
	retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error)
	retrieveRepGroups() ([]string, error)
}

func TestStoreModifyDropsRepGroupLookups(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, c := range []struct {
		kind      storeKind
		foldFirst bool
	}{{storeKindBolt, false}, {storeKindLog, false}, {storeKindLog, true}} {
		Convey(fmt.Sprintf("Given a %s store with a job of rep group A completed and put back live, folded "+
			"before the modify: %t", storeKindName(c.kind), c.foldFirst), t, func() {
			var (
				st modifyLookupStore
				ls *logStore
			)

			if c.kind == storeKindLog {
				pauseLogFolder()

				ls, _ = openTestLogStore(t, ctx)
				st = ls
			} else {
				st, _ = openTestStore(t, ctx, c.kind)
			}

			// beforeModify folds the log store's history if asked to, so
			// that the modify must drop folded rglookups.
			beforeModify := func() {
				if c.foldFirst {
					So(ls.foldNow(), ShouldBeNil)
				}
			}

			// each calls check with st, then, for the log store, once the
			// modify is folded too, and after reopen.
			each := func(check func(st modifyLookupStore)) {
				check(st)

				if ls == nil {
					return
				}

				So(ls.foldNow(), ShouldBeNil)
				check(ls)
				check(reopenLogStore(ctx, ls))
			}

			job := testDBJob("echo c4 modify lookups", "c4rgA")
			_, err := st.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			job.State = JobStateComplete
			job.EndTime = time.Now()
			_, err = st.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			job.State = JobStateReady
			So(st.storeLiveForRerun([]*Job{job}), ShouldBeNil)

			// droppedFromA asserts job is still complete but rep group A no
			// longer finds it, though A is still a rep group ever added.
			droppedFromA := func(st modifyLookupStore) {
				complete, errc := st.checkIfComplete(job.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)

				byRG, errr := st.retrieveCompleteJobsByRepGroup("c4rgA")
				So(errr, ShouldBeNil)
				So(byRG, ShouldBeEmpty)

				rgs, errg := st.retrieveRepGroups()
				So(errg, ShouldBeNil)
				So(rgs, ShouldContain, "c4rgA")
			}

			Convey("modified to a new key in rep group B, it is still complete but rep group A no longer finds it",
				func() {
					beforeModify()

					moved := modifiedCopy(job, "echo c4 modify lookups moved")
					moved.RepGroup = "c4rgB"
					So(st.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)

					each(droppedFromA)
				})

			Convey("deleted, then modified to a new key, it is still complete but rep group A no longer finds "+
				"it", func() {
				So(st.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)
				beforeModify()

				moved := modifiedCopy(job, "echo c4 modify lookups moved")
				So(st.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)

				each(droppedFromA)
			})

			Convey("looked up under rep group B and then modified keeping its key, an add of it under B with "+
				"ignoreAdded counts it complete under A", func() {
				inB := testDBJob(job.Cmd, "c4rgB")
				So(st.storeRepGroupLookups(ctx, []*Job{inB}), ShouldBeNil)
				beforeModify()

				same := modifiedCopy(job, job.Cmd)
				same.Priority = 7
				So(st.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{same}), ShouldBeNil)

				each(func(st modifyLookupStore) {
					stored, erra := st.storeNewJobsGuarded(ctx, []*Job{testDBJob(job.Cmd, "c4rgB")}, true, nil)
					So(erra, ShouldBeNil)
					So(stored.alreadyAdded.Complete, ShouldEqual, 1)
					So(stored.alreadyAdded.CompleteSameRepGroup, ShouldEqual, 0)
					So(stored.alreadyAdded.OtherRepGroups, ShouldHaveLength, 1)
					So(stored.alreadyAdded.OtherRepGroups[0].RepGroup, ShouldEqual, "c4rgA")
				})
			})
		})
	}
}

// archivedDependentsRestart is a way TestStoreArchivedDependentsAfterRestart
// restarts a log store: optionally folding its history first, optionally
// checkpointing, then reopening it, with its folder paused throughout if
// paused, and optionally folding once reopened.
type archivedDependentsRestart struct {
	name                                         string
	paused, foldBefore, checkpoint, foldReopened bool
}

// restartLogStore restarts s as r says, returning the reopened store.
func (r archivedDependentsRestart) restartLogStore(ctx context.Context, s *logStore) *logStore {
	if r.foldBefore {
		So(s.foldNow(), ShouldBeNil)
	}

	if r.checkpoint {
		So(s.checkpoint(), ShouldBeNil)
	}

	s = reopenLogStore(ctx, s)

	if r.foldReopened {
		So(s.foldNow(), ShouldBeNil)
	}

	return s
}

func TestStoreArchivedDependentsAfterRestart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	restarts := []archivedDependentsRestart{
		{name: "reopened"},
		{name: "folded and reopened", foldBefore: true},
		{name: "folded, checkpointed and reopened", foldBefore: true, checkpoint: true},
		{name: "checkpointed with its folder paused and reopened", paused: true, checkpoint: true},
		{name: "checkpointed with its folder paused, reopened and folded", paused: true, checkpoint: true,
			foldReopened: true},
	}

	// each runs steps on a bbolt store and, in a Convey each, on a log store
	// restarted each way between steps' writes and the new member's add,
	// asserting that every log store queues and updates what bbolt does, and
	// returns bbolt's answer.
	each := func(steps func(st store, restart func())) newMemberPlan {
		bdb, _ := openTestStore(t, ctx, storeKindBolt)
		steps(bdb, func() {})
		fromBolt := planOfNewMember(ctx, bdb)

		for _, r := range restarts {
			Convey("on a log store "+r.name+" before the add, as on bbolt", func() {
				if r.paused {
					pauseLogFolder()
				}

				s, _ := openTestLogStore(t, ctx)
				steps(s, func() { s = r.restartLogStore(ctx, s) })
				So(planOfNewMember(ctx, s), ShouldResemble, fromBolt)
			})
		}

		return fromBolt
	}

	Convey("Given a member of dep group grow, a dependent of grow, a dependent of that dependent's dep group and "+
		"a dependent of the member's command, all archived, an add of a new member of grow puts back both dep "+
		"group dependents but not the command's", t, func() {
		j := newArchivedDependentsJobs()

		fromBolt := each(func(st store, restart func()) {
			addAndArchive(ctx, st, j.m, j.d, j.e, j.k)
			restart()
		})

		So(fromBolt.queued, ShouldResemble, []string{j.d.Cmd, archivedDepsNewMember, j.e.Cmd})
		So(fromBolt.updated, ShouldBeEmpty)
	})

	Convey("Given the dependent archived, put back live and modified to a new key, which is archived too, an add "+
		"of a new member of grow puts back the new key but not the old", t, func() {
		j := newArchivedDependentsJobs()
		moved := modifiedCopy(j.d, "echo archived deps dependent moved")

		for _, foldFirst := range []bool{false, true} {
			Convey(fmt.Sprintf("with the archive folded before the modify: %t", foldFirst), func() {
				fromBolt := each(func(st store, restart func()) {
					addAndArchive(ctx, st, j.m, j.d)

					if ls, ok := st.(*logStore); ok && foldFirst {
						So(ls.foldNow(), ShouldBeNil)
					}

					So(st.storeLiveForRerun([]*Job{j.d}), ShouldBeNil)
					So(st.modifyLiveJobs(ctx, []string{j.d.Key()}, []*Job{moved}), ShouldBeNil)
					_, err := st.archiveCompletion(moved.Key(), moved)
					So(err, ShouldBeNil)

					restart()
				})

				So(fromBolt.queued, ShouldResemble, []string{moved.Cmd, archivedDepsNewMember})
				So(fromBolt.updated, ShouldBeEmpty)
			})
		}
	})

	Convey("Given the dependent archived, put back live and modified keeping its key, an add of a new member of "+
		"grow updates it", t, func() {
		j := newArchivedDependentsJobs()
		same := modifiedCopy(j.d, j.d.Cmd)
		same.Priority = 7

		fromBolt := each(func(st store, restart func()) {
			addAndArchive(ctx, st, j.m, j.d)
			So(st.storeLiveForRerun([]*Job{j.d}), ShouldBeNil)
			So(st.modifyLiveJobs(ctx, []string{j.d.Key()}, []*Job{same}), ShouldBeNil)
			restart()
		})

		So(fromBolt.queued, ShouldResemble, []string{archivedDepsNewMember})
		So(fromBolt.updated, ShouldResemble, []string{j.d.Cmd})
	})

	Convey("Given the dependent archived, put back live, looked up under another rep group and removed from the "+
		"live plane, an add of a new member of grow puts it back: the lookup keeps its postings", t, func() {
		j := newArchivedDependentsJobs()
		other := modifiedCopy(j.d, j.d.Cmd)
		other.RepGroup = "zzother"

		fromBolt := each(func(st store, restart func()) {
			addAndArchive(ctx, st, j.m, j.d)
			So(st.storeLiveForRerun([]*Job{j.d}), ShouldBeNil)
			So(st.storeRepGroupLookups(ctx, []*Job{other}), ShouldBeNil)
			So(st.deleteLiveJobs(ctx, []string{j.d.Key()}), ShouldBeNil)
			restart()
		})

		So(fromBolt.queued, ShouldResemble, []string{j.d.Cmd, archivedDepsNewMember})
		So(fromBolt.updated, ShouldBeEmpty)
	})

	Convey("Given an archived dependent of grox, a dep group whose postings sort straight after grow's, an add "+
		"of a new member of grow does not put it back", t, func() {
		j := newArchivedDependentsJobs()
		after := modifiedCopy(j.d, "echo archived deps dependent of grox")
		after.Dependencies = Dependencies{NewDepGroupDependency("grox")}

		fromBolt := each(func(st store, restart func()) {
			addAndArchive(ctx, st, j.m, after)
			restart()
		})

		So(fromBolt.queued, ShouldResemble, []string{archivedDepsNewMember})
		So(fromBolt.updated, ShouldBeEmpty)
	})
}

// planOfNewMember adds a new member of dep group "grow" to st and returns what
// the add queues and updates.
func planOfNewMember(ctx context.Context, st store) newMemberPlan {
	member := testDBJob(archivedDepsNewMember, "adm")
	member.DepGroups = []string{archivedDepsGroup}

	stored, err := st.storeNewJobsGuarded(ctx, []*Job{member}, true, nil)
	So(err, ShouldBeNil)

	return newMemberPlan{queued: sortedCmds(stored.jobsToQueue), updated: sortedCmds(stored.dependents.jobs)}
}

// sortedCmds returns the sorted commands of jobs.
func sortedCmds(jobs []*Job) []string {
	cmds := make([]string, 0, len(jobs))
	for _, job := range jobs {
		cmds = append(cmds, job.Cmd)
	}

	slices.Sort(cmds)

	return cmds
}

func newArchivedDependentsJobs() archivedDependentsJobs {
	j := archivedDependentsJobs{
		m: testDBJob("echo archived deps member", "adm"),
		d: testDBJob("echo archived deps dependent", "add"),
		e: testDBJob("echo archived deps transitive", "ade"),
		k: testDBJob("echo archived deps by key", "adk"),
	}

	j.m.DepGroups = []string{archivedDepsGroup}
	j.d.DepGroups = []string{"dgrp"}
	j.d.Dependencies = Dependencies{NewDepGroupDependency(archivedDepsGroup)}
	j.e.Dependencies = Dependencies{NewDepGroupDependency("dgrp")}
	j.k.Dependencies = Dependencies{NewEssenceDependency(j.m.Cmd, j.m.Cwd)}

	return j
}

// addAndArchive adds jobs to st, then archives each.
func addAndArchive(ctx context.Context, st store, jobs ...*Job) {
	_, err := st.storeNewJobsGuarded(ctx, jobs, true, nil)
	So(err, ShouldBeNil)

	for _, job := range jobs {
		_, err = st.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)
	}
}

// archivedDependentsJobs are TestStoreArchivedDependentsAfterRestart's jobs:
// member m of dep group "grow"; d, in dep group "dgrp", depending on "grow";
// e depending on "dgrp"; and k depending on m by its command, not a dep group.
type archivedDependentsJobs struct {
	m, d, e, k *Job
}

func TestLogStoreFoldModifyAfterCompletion(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and a completed dependent of grow", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		d := newArchivedDependentsJobs().d
		d.State, d.Exited, d.EndTime = JobStateComplete, true, time.Now()
		k := mustLogKey(d)
		seq := s.takeSeqs(3)

		// fold folds d's completion at completed and a modify of its key at
		// each of modified.
		fold := func(completed uint64, modified ...uint64) {
			lookups := make([]historyLookup, len(modified))
			for i, m := range modified {
				lookups[i] = historyLookup{key: k, seq: m, dropped: true}
			}

			So(s.foldHistory(&historyFold{
				complete: []historyFolded{{key: k, seq: completed, job: d}},
				lookups:  lookups,
			}), ShouldBeNil)
		}

		Convey("a fold of a modify of its key and then its completion keeps the completion's postings: an add "+
			"of a new member of grow puts it back", func() {
			fold(seq+1, seq)

			So(planOfNewMember(ctx, s).queued, ShouldResemble, []string{d.Cmd, archivedDepsNewMember})
		})

		Convey("a fold of its completion and, at the very next seq, a modify of its key drops the completion's "+
			"postings, as the bbolt store's modify does: an add of a new member of grow does not put it back",
			func() {
				fold(seq, seq+1)

				So(planOfNewMember(ctx, s).queued, ShouldResemble, []string{archivedDepsNewMember})
			})

		Convey("a fold of a modify of its key, its completion, then another modify of its key drops the "+
			"completion's postings, as the bbolt store's later modify does: an add of a new member of grow does "+
			"not put it back", func() {
			fold(seq+1, seq, seq+2)

			So(planOfNewMember(ctx, s).queued, ShouldResemble, []string{archivedDepsNewMember})
		})

		Convey("a fold of its completion and two modifies of its key, read in the opposite order to their seqs, "+
			"drops the completion's postings, since the later modify is after it: an add of a new member of grow "+
			"does not put it back", func() {
			fold(seq+1, seq+2, seq)

			So(planOfNewMember(ctx, s).queued, ShouldResemble, []string{archivedDepsNewMember})
		})
	})
}

// mustLogKey returns job's key as a logKey, panicking if it is not one.
func mustLogKey(job *Job) logKey {
	k, err := parseLogKey(job.Key())
	if err != nil {
		panic(err)
	}

	return k
}

// newMemberPlan is the sorted commands of the jobs an add queues and of the
// live dependents it updates.
type newMemberPlan struct {
	queued, updated []string
}

func TestStoreFoldRetryKeepsReverseDeps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a dependent of grow modified keeping its key, then archived with 40 more jobs, whose fold "+
		"records the dependent's completion in its first transaction and fails in its second", t, func() {
		const fillers = 40

		// steps adds a member of grow, the dependent and the fillers to st,
		// modifies the dependent keeping its key, then archives it and the
		// fillers. The dependent's key sorts before every filler's, so a fold
		// records its completion first.
		steps := func(st store) {
			j := newFoldRetryJobs(fillers)

			_, err := st.storeNewJobsGuarded(ctx, append([]*Job{j.m, j.d}, j.fillers...), true, nil)
			So(err, ShouldBeNil)
			So(st.modifyLiveJobs(ctx, []string{j.d.Key()}, []*Job{j.same}), ShouldBeNil)

			for _, job := range append([]*Job{j.same}, j.fillers...) {
				_, err = st.archiveCompletion(job.Key(), job)
				So(err, ShouldBeNil)
			}
		}

		bdb, _ := openTestStore(t, ctx, storeKindBolt)
		steps(bdb)
		fromBolt := planOfNewMember(ctx, bdb)
		So(fromBolt.queued, ShouldContain, newFoldRetryJobs(fillers).d.Cmd)

		pauseLogFolder()

		Reset(func() { logFoldTxBytes.Store(0) })

		logFoldTxBytes.Store(1)

		s, _ := openTestLogStore(t, ctx)
		steps(s)

		calls := 0
		failSecond := func() error {
			calls++
			if calls == 2 {
				return errIncTestCrash
			}

			return nil
		}

		Reset(func() { logFoldTxHook.Store(nil) })

		logFoldTxHook.Store(&failSecond)

		So(s.foldNow(), ShouldWrap, errIncTestCrash)

		logFoldTxHook.Store(nil)

		Convey("the fold made again keeps the dependent's reverse dep group postings: after a checkpoint and "+
			"restart an add of a new member of grow puts it back, as on bbolt", func() {
			So(s.foldNow(), ShouldBeNil)
			So(s.checkpoint(), ShouldBeNil)

			s = reopenLogStore(ctx, s)

			So(planOfNewMember(ctx, s), ShouldResemble, fromBolt)
		})
	})
}

// newFoldRetryJobs returns new foldRetryJobs with n fillers.
func newFoldRetryJobs(n int) foldRetryJobs {
	j := foldRetryJobs{
		m:       testDBJob("echo fold retry member", "frm"),
		fillers: testLogJobs("fold retry filler", "frf", n),
	}
	j.m.DepGroups = []string{archivedDepsGroup}

	var first logKey

	for i, filler := range j.fillers {
		if k := mustLogKey(filler); i == 0 || bytes.Compare(k[:], first[:]) < 0 {
			first = k
		}
	}

	for i := 0; ; i++ {
		j.d = testDBJob(fmt.Sprintf("echo fold retry dependent %d", i), "frd")
		if k := mustLogKey(j.d); n == 0 || bytes.Compare(k[:], first[:]) < 0 {
			break
		}
	}

	j.d.Dependencies = Dependencies{NewDepGroupDependency(archivedDepsGroup)}
	j.same = modifiedCopy(j.d, j.d.Cmd)
	j.same.Priority = 7

	return j
}

// foldRetryJobs are TestStoreFoldRetryKeepsReverseDeps' jobs: member m of dep
// group "grow"; d depending on "grow", whose key sorts before every one of
// fillers'; and same, d modified keeping its key.
type foldRetryJobs struct {
	m, d, same *Job
	fillers    []*Job
}

func TestLogStoreReverseDepsAfterSpecChange(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a live job depending on dep group g1", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo c4 reverse deps", "c4deps")
		job.Dependencies = Dependencies{NewDepGroupDependency("g1")}
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		dependents := func(st *logStore, dg string) []string {
			found, errd := st.reverseDepKeys(dg)
			So(errd, ShouldBeNil)

			keys := make([]string, 0, len(found))

			for _, k := range found {
				keys = append(keys, k.String())
			}

			return keys
		}

		Convey("modified to depend on g2 instead, keeping its key, only g2 finds it, also after reopen", func() {
			same := modifiedCopy(job, job.Cmd)
			same.Dependencies = Dependencies{NewDepGroupDependency("g2")}
			So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{same}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(dependents(st, "g1"), ShouldBeEmpty)
				So(dependents(st, "g2"), ShouldResemble, []string{job.Key()})
			})
		})

		Convey("a spec depending on g2 and a later one depending on g3, applied in either order, leave only g3", func() {
			k, errk := parseLogKey(job.Key())
			So(errk, ShouldBeNil)

			var earlier, later logFrames

			job.Dependencies = Dependencies{NewDepGroupDependency("g2")}
			So(s.addSpecAndState(&earlier, job, k), ShouldBeNil)

			job.Dependencies = Dependencies{NewDepGroupDependency("g3")}
			So(s.addSpecAndState(&later, job, k), ShouldBeNil)

			Convey("in seq order", func() {
				So(s.writeAndWait(&earlier), ShouldBeNil)
				So(s.writeAndWait(&later), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					So(dependents(st, "g1"), ShouldBeEmpty)
					So(dependents(st, "g2"), ShouldBeEmpty)
					So(dependents(st, "g3"), ShouldResemble, []string{job.Key()})
				})
			})

			Convey("in reverse seq order", func() {
				writeInReverse(s, &earlier, &later)

				eachReopen(ctx, &s, func(st *logStore) {
					So(dependents(st, "g1"), ShouldBeEmpty)
					So(dependents(st, "g2"), ShouldBeEmpty)
					So(dependents(st, "g3"), ShouldResemble, []string{job.Key()})
				})
			})
		})

		Convey("archived, put back and modified to a new key, an add of a member of g1 puts back neither key", func() {
			job.State = JobStateComplete
			job.EndTime = time.Now()
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			job.State = JobStateReady
			So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)

			moved := modifiedCopy(job, "echo c4 reverse deps moved")
			moved.Dependencies = Dependencies{NewDepGroupDependency("g2")}
			So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(dependents(st, "g1"), ShouldBeEmpty)
				So(dependents(st, "g2"), ShouldResemble, []string{moved.Key()})
			})

			member := testDBJob("echo c4 reverse deps member", "c4deps")
			member.DepGroups = []string{"g1"}
			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			So(erra, ShouldBeNil)
			So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{member.Key()})
			soLive(s, job.Key(), false)
		})

		Convey("archived, put back, deleted and modified to a new key, an add of a member of g1 puts back neither "+
			"key, also after reopen", func() {
			job.State = JobStateComplete
			job.EndTime = time.Now()
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			job.State = JobStateReady
			So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)
			So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

			moved := modifiedCopy(job, "echo c4 reverse deps moved")
			moved.Dependencies = Dependencies{NewDepGroupDependency("g2")}
			So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{moved}), ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(dependents(st, "g1"), ShouldBeEmpty)
				So(dependents(st, "g2"), ShouldResemble, []string{moved.Key()})
			})

			member := testDBJob("echo c4 reverse deps member", "c4deps")
			member.DepGroups = []string{"g1"}
			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			So(erra, ShouldBeNil)
			So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{member.Key()})
			soLive(s, job.Key(), false)
		})

		Convey("deleted while not complete and re-added depending on g2 instead, only g2 finds it, also after "+
			"reopen", func() {
			So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

			again := testDBJob(job.Cmd, job.RepGroup)
			again.Dependencies = Dependencies{NewDepGroupDependency("g2")}
			_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
			So(err, ShouldBeNil)

			eachReopen(ctx, &s, func(st *logStore) {
				So(dependents(st, "g1"), ShouldBeEmpty)
				So(dependents(st, "g2"), ShouldResemble, []string{job.Key()})
			})
		})

		Convey("deleted while complete, it is still put back by an add of a member of g1", func() {
			job.State = JobStateComplete
			job.EndTime = time.Now()
			_, err = s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			job.State = JobStateReady
			So(s.storeLiveForRerun([]*Job{job}), ShouldBeNil)
			So(s.deleteLiveJobs(ctx, []string{job.Key()}), ShouldBeNil)

			member := testDBJob("echo c4 reverse deps member", "c4deps")
			member.DepGroups = []string{"g1"}
			stored, erra := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			So(erra, ShouldBeNil)
			So(stored.jobsToQueue, ShouldHaveLength, 2)
			soLive(s, job.Key(), true)
		})
	})
}
