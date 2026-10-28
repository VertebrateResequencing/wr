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

// Tests of storing, archiving and recovering jobs through the store API, so
// that they run on every store kind (see forEachStore).

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// storeCloseWrites is how many adds, how many archives, and how many live jobs
// with best-effort changes, TestStoreCloseDrainsWrites has in flight as the
// store closes.
const storeCloseWrites = 64

// storeCloseChangeRounds is how many times TestStoreCloseDrainsWrites changes
// each of its live jobs back and forth before its last change, so that the
// best-effort writer is usually mid-drain with more changes queued when the
// store closes.
const storeCloseChangeRounds = 16

// storeCloseWait bounds the wait for a write or close racing a close. It is a
// hang detector: each returns in milliseconds unless close loses a waiter.
const storeCloseWait = 30 * time.Second

func TestStoreHighPeakMemoryRecommendation(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("On a %s store, a high-memory non-RAM failure seeds recommendations but honours "+
			"override always", storeKindName(kind)), t, func() {
			s, _ := openTestStore(t, ctx, kind)

			const (
				reqGroup     = "high-peak-signal"
				requestedRAM = 16
				peakRAM      = 543
			)

			now := time.Now()
			job := testDBJob("python3 -c alloc", "rg-high-peak")
			job.ReqGroup = reqGroup
			job.Requirements.RAM = requestedRAM
			job.Requirements.Time = time.Minute
			job.State = JobStateDelayed
			job.Exited = true
			job.Exitcode = -1
			job.FailReason = FailReasonSignal
			job.PeakRAM = peakRAM
			job.StartTime = now.Add(-time.Second)
			job.EndTime = now

			_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			s.updateJobAfterExit(ctx, job, nil, nil, false)
			So(inspectorOf(s).flush(), ShouldBeNil)

			server := &Server{db: s}
			recommendedReq := server.recommendedReqForGroup(reqGroup, make(map[string]*jqs.Requirements))
			So(recommendedReq, ShouldNotBeNil)
			So(recommendedReq.RAM, ShouldEqual, 600)

			newRetry := func(override uint8) *Job {
				retry := testDBJob("python3 -c alloc", "rg-high-peak")
				retry.ReqGroup = reqGroup
				retry.Override = override
				retry.Requirements.RAM = requestedRAM
				retry.Requirements.Time = time.Minute
				retry.State = JobStateDelayed
				retry.FailReason = FailReasonSignal
				retry.PeakRAM = peakRAM

				return retry
			}

			systemRetry := newRetry(jobOverridePreferSystemReqs)
			updateJobRequirementsForRetry(systemRetry, systemRetry.Override, recommendedReq)
			So(systemRetry.Requirements.RAM, ShouldEqual, 600)
			So(systemRetry.RequirementsOrig.RAM, ShouldEqual, requestedRAM)
			So(systemRetry.FailReason, ShouldEqual, FailReasonSignal)

			alwaysRetry := newRetry(jobOverrideAlwaysUseJobReqs)
			updateJobRequirementsForRetry(alwaysRetry, alwaysRetry.Override, recommendedReq)
			So(alwaysRetry.Requirements.RAM, ShouldEqual, requestedRAM)
			So(alwaysRetry.RequirementsOrig.RAM, ShouldEqual, requestedRAM)
			So(alwaysRetry.FailReason, ShouldEqual, FailReasonSignal)
		})
	})
}

func TestStoreResurrectedDependentSkipsCompleteJobs(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("On a %s store, resurrecting a dependent does not re-queue complete jobs from the "+
			"same batch", storeKindName(kind)), t, func() {
			s, _ := openTestStore(t, ctx, kind)
			inspector := inspectorOf(s)

			const growDepGroup = "grow"

			firstMember := testDBJob("echo first member", "first-member")
			firstMember.DepGroups = []string{growDepGroup}
			dependent := testDBJob("echo dependent", "dependent")
			dependent.Dependencies = Dependencies{NewDepGroupDependency(growDepGroup)}

			_, err := s.storeNewJobsGuarded(ctx, []*Job{firstMember, dependent}, true, nil)
			So(err, ShouldBeNil)

			for _, job := range []*Job{firstMember, dependent} {
				_, err = s.archiveCompletion(job.Key(), job)
				So(err, ShouldBeNil)
			}

			secondMember := testDBJob("echo second member", "second-member")
			secondMember.DepGroups = []string{growDepGroup}

			stored, err := s.storeNewJobsGuarded(ctx, []*Job{firstMember, secondMember}, true, nil)
			So(err, ShouldBeNil)
			So(stored.alreadyAdded, ShouldResemble, DuplicateBreakdown{Complete: 1, CompleteSameRepGroup: 1})
			So(inspector.flush(), ShouldBeNil)

			queued := make([]string, 0, len(stored.jobsToQueue))
			notLive := 0

			for _, job := range stored.jobsToQueue {
				queued = append(queued, job.Cmd)

				// every queued job must also be recoverable as live
				_, live, errl := inspector.liveJob(job.Key())
				So(errl, ShouldBeNil)

				if !live {
					notLive++
				}
			}

			So(notLive, ShouldEqual, 0)
			So(queued, ShouldNotContain, firstMember.Cmd)
			So(queued, ShouldContain, secondMember.Cmd)
			So(queued, ShouldContain, dependent.Cmd)
			So(queued, ShouldHaveLength, 2)
		})
	})
}

func TestStoreCheckIfComplete(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a fresh %s store with one archived job", storeKindName(kind)), t, func() {
			s, _ := openTestStore(t, ctx, kind)

			job := testDBArchivedJob("echo complete", "rg-complete", time.Now().Truncate(time.Nanosecond))
			_, err := s.archiveCompletion(job.Key(), job)
			So(err, ShouldBeNil)

			Convey("checkIfComplete is true for the archived job's key", func() {
				complete, errc := s.checkIfComplete(job.Key())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)
			})

			Convey("checkIfComplete is false for a key that was never archived", func() {
				complete, errc := s.checkIfComplete("no.such.key")
				So(errc, ShouldBeNil)
				So(complete, ShouldBeFalse)
			})
		})
	})
}

func TestStoreRetrieveCompleteJobsRecent(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a fresh %s store", storeKindName(kind)), t, func() {
			s, _ := openTestStore(t, ctx, kind)

			archive := func(job *Job) {
				_, err := s.archiveCompletion(job.Key(), job)
				So(err, ShouldBeNil)
			}

			Convey("With three archived jobs ending at now-3h, now-30m and now-1m", func() {
				now := time.Now()
				oldEnd := now.Add(-3 * time.Hour).Truncate(time.Nanosecond)
				midEnd := now.Add(-30 * time.Minute).Truncate(time.Nanosecond)
				newEnd := now.Add(-1 * time.Minute).Truncate(time.Nanosecond)

				oldJob := testDBArchivedJob("echo old", "rg-old", oldEnd)
				midJob := testDBArchivedJob("echo mid", "rg-mid", midEnd)
				newJob := testDBArchivedJob("echo new", "rg-new", newEnd)

				So(oldJob.Key(), ShouldNotEqual, midJob.Key())
				So(midJob.Key(), ShouldNotEqual, newJob.Key())

				for _, job := range []*Job{oldJob, midJob, newJob} {
					archive(job)
				}

				Convey("retrieveCompleteJobsRecent(now-1h) returns the in-window jobs in ascending end-time order", func() {
					jobs, errr := s.retrieveCompleteJobsRecent(now.Add(-time.Hour))
					So(errr, ShouldBeNil)
					So(len(jobs), ShouldEqual, 2)
					So(jobs[0].Key(), ShouldEqual, midJob.Key())
					So(jobs[0].EndTime.UnixNano(), ShouldEqual, midEnd.UnixNano())
					So(jobs[1].Key(), ShouldEqual, newJob.Key())
					So(jobs[1].EndTime.UnixNano(), ShouldEqual, newEnd.UnixNano())

					keys := []string{jobs[0].Key(), jobs[1].Key()}
					So(keys, ShouldNotContain, oldJob.Key())
				})
			})

			Convey("With a job archived at T whose key is also live again (re-running)", func() {
				endTime := time.Now().Add(-30 * time.Minute).Truncate(time.Nanosecond)
				job := testDBArchivedJob("echo rerunning", "rg-rerun", endTime)

				archive(job)

				Convey("It is returned before being made live again", func() {
					jobs, errr := s.retrieveCompleteJobsRecent(endTime.Add(-time.Minute))
					So(errr, ShouldBeNil)
					So(len(jobs), ShouldEqual, 1)
					So(jobs[0].Key(), ShouldEqual, job.Key())
				})

				Convey("retrieveCompleteJobsRecent(T-1m) skips it once the key is live again", func() {
					liveAgain := testDBJob("echo rerunning", "rg-rerun")
					So(liveAgain.Key(), ShouldEqual, job.Key())

					_, errs := s.storeNewJobsGuarded(ctx, []*Job{liveAgain}, false, nil)
					So(errs, ShouldBeNil)

					jobs, errr := s.retrieveCompleteJobsRecent(endTime.Add(-time.Minute))
					So(errr, ShouldBeNil)
					So(jobs, ShouldBeEmpty)
				})
			})

			Convey("With a job archived twice at the same end time, it is returned once with that end time", func() {
				endTime := time.Now().Add(-15 * time.Minute).Truncate(time.Nanosecond)
				job := testDBArchivedJob("echo same", "rg-same", endTime)

				archive(job)
				archive(job)

				jobs, errr := s.retrieveCompleteJobsRecent(endTime.Add(-time.Minute))
				So(errr, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				So(jobs[0].Key(), ShouldEqual, job.Key())
				So(jobs[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
			})

			Convey("With an empty history, retrieveCompleteJobsRecent(now-1h) returns empty and no error", func() {
				jobs, errr := s.retrieveCompleteJobsRecent(time.Now().Add(-time.Hour))
				So(errr, ShouldBeNil)
				So(jobs, ShouldBeEmpty)
			})

			Convey("With a job archived at now, a cutoff before 1970 returns it (huge window)", func() {
				endTime := time.Now().Add(-time.Minute).Truncate(time.Nanosecond)
				job := testDBArchivedJob("echo ancient", "rg-ancient", endTime)

				archive(job)

				Convey("retrieveCompleteJobsRecent(pre-1970) returns the job, not an empty slice", func() {
					preEpoch := time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC)
					So(preEpoch.UnixNano() < 0, ShouldBeTrue)

					jobs, errr := s.retrieveCompleteJobsRecent(preEpoch)
					So(errr, ShouldBeNil)
					So(len(jobs), ShouldEqual, 1)
					So(jobs[0].Key(), ShouldEqual, job.Key())
					So(jobs[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
				})
			})
		})
	})
}

func TestStoreCompleteJobsRecentDurability(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store backed by on-disk files", storeKindName(kind)), t, func() {
			dir := t.TempDir()
			s := openStoreIn(ctx, kind, dir)

			archive := func(job *Job) {
				_, err := s.archiveCompletion(job.Key(), job)
				So(err, ShouldBeNil)
			}

			reopen := func() {
				So(s.close(ctx), ShouldBeNil)

				s = openStoreIn(ctx, kind, dir)
			}

			defer func() { So(s.close(ctx), ShouldBeNil) }()

			Convey("An archived job is still recent after a close and reopen", func() {
				now := time.Now()
				endTime := now.Add(-30 * time.Minute).Truncate(time.Nanosecond)
				job := testDBArchivedJob("echo durable", "rg-durable", endTime)

				archive(job)
				reopen()

				jobs, errr := s.retrieveCompleteJobsRecent(now.Add(-time.Hour))
				So(errr, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				So(jobs[0].Key(), ShouldEqual, job.Key())
				So(jobs[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
			})

			Convey("A key re-archived at T2 is returned once with its T2 end time after a reopen", func() {
				t1 := time.Now().Add(-2 * time.Hour).Truncate(time.Nanosecond)
				job := testDBArchivedJob("echo rerun", "rg-rerun", t1)

				archive(job)

				t2 := time.Now().Add(-1 * time.Minute).Truncate(time.Nanosecond)
				job.EndTime = t2

				archive(job)
				reopen()

				jobs, errr := s.retrieveCompleteJobsRecent(t1.Add(-time.Minute))
				So(errr, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				So(jobs[0].Key(), ShouldEqual, job.Key())
				So(jobs[0].EndTime.UnixNano(), ShouldEqual, t2.UnixNano())
			})
		})
	})
}

// TestStoreArchivedJobNotResurrected proves that a change or exit written for a
// job after it was archived does not bring it back live, so recovery does not
// run it again. TestReliable4ArchiveBeatsResurrectingChange covers the same
// race with bbolt's archive transaction held open.
func TestStoreArchivedJobNotResurrected(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store with 2 jobs added and archived", storeKindName(kind)), t, func() {
			dir := t.TempDir()
			s := openStoreIn(ctx, kind, dir)

			defer func() { So(s.close(ctx), ShouldBeNil) }()

			changed := testDBJob("echo archived then changed", "rg-resurrect")
			exited := testDBJob("echo archived then exited", "rg-resurrect")
			_, err := s.storeNewJobsGuarded(ctx, []*Job{changed, exited}, false, nil)
			So(err, ShouldBeNil)

			for _, job := range []*Job{changed, exited} {
				job.State = JobStateComplete
				job.Exited = true
				job.EndTime = time.Now()
				_, err = s.archiveCompletion(job.Key(), job)
				So(err, ShouldBeNil)
			}

			Convey("a later change of one and exit of the other leave both archived and neither live", func() {
				reserve := func(job *Job) { job.State = JobStateReserved }
				s.queueJobChangeAhead(ctx, changed, s.prepareJobChange(ctx, changed, reserve), reserve)
				s.updateJobAfterExit(ctx, exited, []byte("stdout"), []byte("stderr"), true)

				inspector := inspectorOf(s)
				So(inspector.flush(), ShouldBeNil)

				for _, job := range []*Job{changed, exited} {
					_, live, errl := inspector.liveJob(job.Key())
					So(errl, ShouldBeNil)
					So(live, ShouldBeFalse)

					_, complete, errc := inspector.completeJob(job.Key())
					So(errc, ShouldBeNil)
					So(complete, ShouldBeTrue)
				}

				So(s.close(ctx), ShouldBeNil)
				s = openStoreIn(ctx, kind, dir)

				recovered, _, errr := s.recoverIncompleteJobs()
				So(errr, ShouldBeNil)
				So(recovered, ShouldBeEmpty)
			})
		})
	})
}

// TestStoreEnv proves that an env retrieved before it was stored is still
// stored when storeEnv is then called, and is retrievable, also after a
// reopen: a store must not remember the miss as proof the env is stored
// (.docs/bugfixes/260827-2.md item 8).
func TestStoreEnv(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store and an env it has never stored", storeKindName(kind)), t, func() {
			dir := t.TempDir()
			s := openStoreIn(ctx, kind, dir)

			defer func() { So(s.close(ctx), ShouldBeNil) }()

			env := []byte("store-env-absent")

			Convey("retrieving it returns nothing, and once stored it is retrievable, also after a reopen", func() {
				So(s.retrieveEnv(ctx, byteKey(env)), ShouldBeEmpty)

				envkey, err := s.storeEnv(env)
				So(err, ShouldBeNil)
				So(envkey, ShouldEqual, byteKey(env))
				So(s.retrieveEnv(ctx, envkey), ShouldResemble, env)

				So(s.close(ctx), ShouldBeNil)
				s = openStoreIn(ctx, kind, dir)

				So(s.retrieveEnv(ctx, envkey), ShouldResemble, env)
			})
		})
	})
}

// TestStoreReopenRoundTrip proves a store closed and reopened gives back the
// live and archived jobs it held.
func TestStoreReopenRoundTrip(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store holding 2 live jobs and 1 archived one, closed", storeKindName(kind)),
			t, func() {
				dir := t.TempDir()
				s := openStoreIn(ctx, kind, dir)

				liveA := testDBJob("echo live a", "rg-live-a")
				liveB := testDBJob("echo live b", "rg-live-b")
				_, err := s.storeNewJobsGuarded(ctx, []*Job{liveA, liveB}, false, nil)
				So(err, ShouldBeNil)

				endTime := time.Now().Add(-30 * time.Minute).Truncate(time.Nanosecond)
				archived := testDBArchivedJob("echo archived", "rg-archived", endTime)
				_, err = s.archiveCompletion(archived.Key(), archived)
				So(err, ShouldBeNil)
				So(s.close(ctx), ShouldBeNil)

				Convey("reopening it recovers both live jobs and retrieves the archived one by key", func() {
					live := recoveredJobsByCmd(ctx, kind, dir)
					So(live, ShouldHaveLength, 2)
					So(live[liveA.Cmd], ShouldNotBeNil)
					So(live[liveA.Cmd].RepGroup, ShouldEqual, liveA.RepGroup)
					So(live[liveB.Cmd], ShouldNotBeNil)
					So(live[liveB.Cmd].RepGroup, ShouldEqual, liveB.RepGroup)

					s = openStoreIn(ctx, kind, dir)

					defer func() { So(s.close(ctx), ShouldBeNil) }()

					complete, errr := s.retrieveCompleteJobsByKeys([]string{archived.Key()})
					So(errr, ShouldBeNil)
					So(complete, ShouldHaveLength, 1)
					So(complete[0].Key(), ShouldEqual, archived.Key())
					So(complete[0].Cmd, ShouldEqual, archived.Cmd)
					So(complete[0].State, ShouldEqual, JobStateComplete)
					So(complete[0].EndTime.UnixNano(), ShouldEqual, endTime.UnixNano())
				})
			})
	})
}

// TestStoreRecoveryDropsImpossibleCleanups covers reading back a job persisted
// by a wr that stored a cleanup behaviour on a cwd_matters job, where it can only
// ever be a no-op: v0.37.0 and v0.37.1 did that, so real databases hold one.
// storeNewJobsGuarded writes it here because it is the same encoder those
// versions used.
func TestStoreRecoveryDropsImpossibleCleanups(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store holding a cwd_matters job persisted with a cleanup behaviour",
			storeKindName(kind)), t, func() {
			dir := t.TempDir()
			s := openStoreIn(ctx, kind, dir)

			poisoned := testDBJob("echo poisoned", "rg-poisoned")
			poisoned.CwdMatters = true
			poisoned.Behaviours = Behaviours{
				{When: OnExit, Do: Cleanup},
				{When: OnFailure, Do: Run, Arg: "echo load failed"},
			}

			normal := testDBJob("echo normal", "rg-normal")
			normal.Behaviours = Behaviours{{When: OnExit, Do: Cleanup}}

			_, err := s.storeNewJobsGuarded(ctx, []*Job{poisoned, normal}, false, nil)
			So(err, ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)

			Convey("recovering it drops the cleanup, but leaves an ordinary job's cleanup alone", func() {
				byCmd := recoveredJobsByCmd(ctx, kind, dir)
				So(byCmd, ShouldHaveLength, 2)

				So(byCmd["echo normal"], ShouldNotBeNil)
				So(byCmd["echo normal"].Behaviours.String(), ShouldEqual, `{"on_exit":[{"cleanup":true}]}`)
				So(byCmd["echo poisoned"], ShouldNotBeNil)
				So(byCmd["echo poisoned"].Behaviours.String(), ShouldEqual,
					`{"on_failure":[{"run":"echo load failed"}]}`)
			})
		})
	})
}

func TestStoreContainerImageUser(t *testing.T) {
	ctx := context.Background()

	const (
		imageUserCmd   = "echo image user"
		callingUserCmd = "echo calling user"
		imageUserImage = "ubuntu:latest"
	)

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store holding docker jobs that differ only in ContainerImageUser",
			storeKindName(kind)), t, func() {
			dir := t.TempDir()
			s := openStoreIn(ctx, kind, dir)

			imageUser := testDBJob(imageUserCmd, "rg-image-user")
			imageUser.WithDocker = imageUserImage
			imageUser.ContainerImageUser = true

			callingUser := testDBJob(callingUserCmd, "rg-calling-user")
			callingUser.WithDocker = imageUserImage

			_, err := s.storeNewJobsGuarded(ctx, []*Job{imageUser, callingUser}, false, nil)
			So(err, ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)

			Convey("recovering it brings each job's flag back as it was stored", func() {
				byCmd := recoveredJobsByCmd(ctx, kind, dir)
				So(byCmd, ShouldHaveLength, 2)

				So(byCmd[imageUserCmd], ShouldNotBeNil)
				So(byCmd[imageUserCmd].ContainerImageUser, ShouldBeTrue)
				So(byCmd[callingUserCmd], ShouldNotBeNil)
				So(byCmd[callingUserCmd].ContainerImageUser, ShouldBeFalse)
				So(byCmd[imageUserCmd].Key(), ShouldNotEqual, byCmd[callingUserCmd].Key())
			})
		})
	})
}

// recoveredJobsByCmd reopens the store of kind in dir, recovers its incomplete
// jobs, closes it and returns the jobs keyed by Cmd.
func recoveredJobsByCmd(ctx context.Context, kind storeKind, dir string) map[string]*Job {
	s := openStoreIn(ctx, kind, dir)

	defer func() { So(s.close(ctx), ShouldBeNil) }()

	jobs, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)

	byCmd := make(map[string]*Job, len(jobs))
	for _, job := range jobs {
		byCmd[job.Cmd] = job
	}

	return byCmd
}

// TestStoreCloseDrainsWrites proves that close() with adds and archives in
// flight returns, every one of them returns, each that reported success is
// durable once the store is reopened, every best-effort change queued before
// close() was called is there after a reopen with its latest state, and an add
// or archive offered after close is refused. TestReliable4AddCloseDrains,
// TestReliable4ArchiveCloseDrains and TestReliable4BestEffortCloseDrains cover
// the same with bbolt's writer held so that every write is queued when close()
// is called.
func TestStoreCloseDrainsWrites(t *testing.T) {
	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s store with jobs to archive, and more to add, while it closes",
			storeKindName(kind)), t, func() {
			dir := t.TempDir()
			s := openStoreIn(ctx, kind, dir)

			toArchive := make([]*Job, storeCloseWrites)
			toAdd := make([]*Job, storeCloseWrites)
			toChange := make([]*Job, storeCloseWrites)

			for i := range storeCloseWrites {
				toArchive[i] = testDBArchivedJob(fmt.Sprintf("echo close archive %d", i), "rg-close", time.Now())
				toAdd[i] = testDBJob(fmt.Sprintf("echo close add %d", i), "rg-close")
				toChange[i] = testDBJob(fmt.Sprintf("echo close change %d", i), "rg-close")
				toChange[i].State = JobStateReady
			}

			_, err := s.storeNewJobsGuarded(ctx, append(slices.Clone(toArchive), toChange...), false, nil)
			So(err, ShouldBeNil)

			// changes made as a server makes its best-effort changes, ending
			// with each job buried, so that is the state that must be recovered.
			states := make([]JobState, 0, 2*storeCloseChangeRounds+1)
			for range storeCloseChangeRounds {
				states = append(states, JobStateReserved, JobStateReady)
			}

			states = append(states, JobStateBuried)

			queueChanges := func() {
				for _, state := range states {
					change := func(job *Job) { job.State = state }

					for _, job := range toChange {
						s.queueJobChangeAhead(ctx, job, s.prepareJobChange(ctx, job, change), change)
					}
				}
			}

			archiveErrs, addErrs, closeErr := closeWhileWriting(ctx, s, toArchive, toAdd, queueChanges)

			Convey("close returns, and what reported success is there after a reopen", func() {
				So(closeErr, ShouldBeNil)

				s = openStoreIn(ctx, kind, dir)

				defer func() { So(s.close(ctx), ShouldBeNil) }()

				live, _, errr := s.recoverIncompleteJobs()
				So(errr, ShouldBeNil)

				liveStates := make(map[string]JobState, len(live))
				for _, job := range live {
					liveStates[job.Key()] = job.State
				}

				isLive := func(job *Job) bool {
					_, ok := liveStates[job.Key()]

					return ok
				}

				lost := 0

				for i, job := range toAdd {
					if addErrs[i] == nil && !isLive(job) {
						lost++
					}
				}

				for i, job := range toArchive {
					complete, errc := s.checkIfComplete(job.Key())
					if errc != nil || (archiveErrs[i] == nil && (!complete || isLive(job))) {
						lost++
					}
				}

				// how many writes win the race with close varies: usually all,
				// rarely none, so the count of those that did is not asserted.
				So(lost, ShouldEqual, 0)

				notLatest := 0

				for _, job := range toChange {
					if liveStates[job.Key()] != JobStateBuried {
						notLatest++
					}
				}

				So(notLatest, ShouldEqual, 0)
			})

			Convey("an add and an archive offered after close are refused", func() {
				_, erra := s.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo after close", "rg-close")}, false, nil)
				So(erra, ShouldNotBeNil)

				late := testDBArchivedJob("echo archive after close", "rg-close", time.Now())
				_, erra = s.archiveCompletion(late.Key(), late)
				So(erra, ShouldNotBeNil)
			})
		})
	})
}

// closeWhileWriting archives toArchive and adds toAdd, one goroutine each,
// then, once they have all started, calls queueChanges and closes s. It
// returns each archive's and add's error and close's, and fails the test if
// any of them has not returned within storeCloseWait.
func closeWhileWriting(ctx context.Context, s store, toArchive, toAdd []*Job,
	queueChanges func(),
) ([]error, []error, error) {
	archiveErrs := make([]error, len(toArchive))
	addErrs := make([]error, len(toAdd))

	var started, finished sync.WaitGroup

	for i, job := range toArchive {
		started.Add(1)
		finished.Go(func() {
			started.Done()

			_, archiveErrs[i] = s.archiveCompletion(job.Key(), job)
		})
	}

	for i, job := range toAdd {
		started.Add(1)
		finished.Go(func() {
			started.Done()

			_, addErrs[i] = s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		})
	}

	started.Wait()
	queueChanges()

	closed := make(chan error, 1)

	go func() { closed <- s.close(ctx) }()

	done := make(chan struct{})

	go func() {
		finished.Wait()
		close(done)
	}()

	var closeErr error

	select {
	case <-done:
	case <-time.After(storeCloseWait):
		So("a write racing close did not return", ShouldBeBlank)
	}

	select {
	case closeErr = <-closed:
	case <-time.After(storeCloseWait):
		So("close with writes in flight did not return", ShouldBeBlank)
	}

	return archiveErrs, addErrs, closeErr
}

// TestStoreJobStatsCorruptDurationGuard proves that archiving a job whose end
// time is missing or before its start stores its peak RAM and disk but no
// duration. TestDBPutJobStatsCorruptDurationGuard pins the value bbolt stores
// for a valid duration.
func TestStoreJobStatsCorruptDurationGuard(t *testing.T) {
	ctx := context.Background()

	// statsStored archives a job of reqGroup with the given start and end times
	// into a fresh store and returns how many learned-requirement stats it
	// stored, and the requirements it learned from them.
	statsStored := func(kind storeKind, reqGroup string, start, end time.Time) (int, *jqs.Requirements) {
		s, _ := openTestStore(t, ctx, kind)

		job := testDBArchivedJob("echo "+reqGroup, "rg-"+reqGroup, end)
		job.ReqGroup = reqGroup
		job.PeakRAM = 100
		job.PeakDisk = 200
		job.StartTime = start

		_, err := s.archiveCompletion(job.Key(), job)
		So(err, ShouldBeNil)

		inspector := inspectorOf(s)
		So(inspector.flush(), ShouldBeNil)

		return inspectedCount(inspector, kindStat), storeLearnedReqs(s, reqGroup)
	}

	// soLearnedRAMAndDisk asserts rec learned the job's 100MB peak RAM and its
	// 200MB peak disk, which is 1GB rounded up.
	soLearnedRAMAndDisk := func(rec *jqs.Requirements) {
		So(rec.RAM, ShouldEqual, 100)
		So(rec.Disk, ShouldEqual, 1)
	}

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("A %s store does not store a corrupt duration stat", storeKindName(kind)), t, func() {
			now := time.Now()

			Convey("A job with a zero EndTime stores RAM and disk but no duration", func() {
				n, rec := statsStored(kind, "zero-endtime", now, time.Time{})
				So(n, ShouldEqual, 2)
				soLearnedRAMAndDisk(rec)
				So(rec.Time, ShouldEqual, 0)
			})

			Convey("A job with an EndTime before StartTime stores RAM and disk but no duration", func() {
				n, rec := statsStored(kind, "negative-duration", now, now.Add(-time.Minute))
				So(n, ShouldEqual, 2)
				soLearnedRAMAndDisk(rec)
				So(rec.Time, ShouldEqual, 0)
			})

			Convey("A job with a valid positive duration stores RAM, disk and duration", func() {
				n, rec := statsStored(kind, "positive-duration", now, now.Add(1500*time.Millisecond))
				So(n, ShouldEqual, 3)
				soLearnedRAMAndDisk(rec)
				So(rec.Time, ShouldBeGreaterThan, 0)
			})
		})
	})
}

// storeLearnedReqs returns the requirements s has learned for reqGroup from every
// stat it has committed, loading them if they are not current. It waits out
// any refresh interval s has.
func storeLearnedReqs(s store, reqGroup string) *jqs.Requirements {
	var rec *jqs.Requirements

	So(pollUntil(func() bool {
		rec = s.refreshReqGroupRecs(reqGroup)
		_, current := s.cachedReqGroupRecs(reqGroup)

		return current && rec != nil
	}), ShouldBeTrue)

	return rec
}
