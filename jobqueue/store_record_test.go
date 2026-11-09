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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// storeReplayEnv names the env var holding the recording TestStoreReplay
	// replays.
	storeReplayEnv = "WR_STORE_REPLAY"

	// removedMethodsRecording is the committed bbolt recording of calls to the
	// methods Phase 1 removes from store, which spec A5 acceptance test 4
	// replays onto a store without them.
	removedMethodsRecording = "testdata/store_record/removed_methods.rec"

	// recordTestRepGroup* are the RepGroups recordWorkload adds jobs in.
	recordTestRepGroupA     = "record-a"
	recordTestRepGroupB     = "record-b"
	recordTestRepGroupChild = "record-child"
	recordTestDepGroup      = "record-dg"
	recordTestDepGroup2     = "record-dg-2"
	recordTestChildDepGroup = "record-child-dg"
	recordTestLimitGroup    = "record-lg"
	recordTestReqGroup      = "db_test"

	// recordTestHost is the host the recorded jobs run on.
	recordTestHost = "record-host"
)

// recordTestBase is the fixed time recordWorkload's job times count from, so
// that its cutoff picks the same jobs whenever it runs.
var recordTestBase = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) //nolint:gochecknoglobals

// removedMethodsCalls are the methods of removedMethodsRecording's calls, in
// order.
var removedMethodsCalls = slices.Concat( //nolint:gochecknoglobals
	slices.Repeat([]string{"storeNewJobsGuarded"}, 3),
	slices.Repeat([]string{"updateJobRunStateDurableWithin"}, 3),
	[]string{"logReserveNotDurable", "setBatchTuning"},
)

const (
	// recordNeverAsked is the question an askingStore asks.
	recordNeverAsked = "record-never-asked"

	// recordRepGroupsMethod is the recorded method name of retrieveRepGroups.
	recordRepGroupsMethod = "retrieveRepGroups"
)

// errBoom is the error boomRepGroupsStore adds.
var errBoom = errors.New("boom")

// pausingArchiveWait is the longest a pausingArchiveStore or its test waits
// for the other: far longer than one archive takes.
const pausingArchiveWait = 10 * time.Second

const (
	// recordCachedMethod and recordRefreshMethod are the recorded method names
	// of cachedReqGroupRecs and refreshReqGroupRecs.
	recordCachedMethod  = "cachedReqGroupRecs"
	recordRefreshMethod = "refreshReqGroupRecs"

	// learnedTestReqGroup is the ReqGroup of the jobs the learned-requirements
	// recordings archive.
	learnedTestReqGroup = "record-learned"

	// learnedTestInterval is the refresh interval recordLearnedThrottled sets
	// and then sleeps past, as a manager's clock passes it between two loads,
	// so that its recording loads where a replay at full speed would not.
	learnedTestInterval = 500 * time.Millisecond

	// learnedTestLoadDelay is how long each load of learned requirements takes
	// while recordLearnedBetweenWrites's recording replays: far longer than a
	// replay takes to reach its next calls, so that a load the replay starts in
	// the background cannot finish before them.
	learnedTestLoadDelay = 100 * time.Millisecond
)

func exitDependentDurably(_ context.Context, s store, dependent *Job) *Job {
	dependent.State, dependent.Exited, dependent.Exitcode = JobStateBuried, true, 1
	So(s.updateJobAfterExitDurable(dependent, []byte("o"), []byte("e"), false), ShouldBeNil)

	return dependent
}

func exitDependent(ctx context.Context, s store, dependent *Job) *Job {
	done := s.expectJobExitUpdate()
	dependent.State, dependent.Exited, dependent.Exitcode = JobStateBuried, true, 1
	s.updateJobAfterExit(ctx, dependent, []byte("o"), nil, false)
	done()

	// waits for every expected exit update
	stdo, _ := s.retrieveJobStd(ctx, dependent.Key())
	So(string(stdo), ShouldEqual, "o")

	return dependent
}

func markDependent(_ context.Context, s store, dependent *Job) *Job {
	dependent.RerunAfterRun = true
	So(s.storeRunningRerunMarks([]*Job{dependent}), ShouldBeNil)

	return dependent
}

// unmarkedDependent changes dependent without storing the change, then asks for
// its mark to be stored, which stores nothing, since it is not marked.
func unmarkedDependent(_ context.Context, s store, dependent *Job) *Job {
	dependent.State, dependent.UntilBuried = JobStateDelayed, 2
	So(s.storeRunningRerunMarks([]*Job{dependent}), ShouldBeNil)

	return dependent
}

// modifyDependentKeepingKey modifies dependent's priority, which keeps its key,
// as `wr mod` does when it leaves the command alone: bbolt deletes its live
// record and then stores the modified one under the same key.
func modifyDependentKeepingKey(ctx context.Context, s store, dependent *Job) *Job {
	modified := testDBJob(dependent.Cmd, "record-dependent")
	modified.Dependencies = dependent.Dependencies
	modified.Priority = dependent.Priority + 1
	So(modified.Key(), ShouldEqual, dependent.Key())
	So(s.modifyLiveJobs(ctx, []string{dependent.Key()}, []*Job{modified}), ShouldBeNil)

	return modified
}

func modifyDependent(ctx context.Context, s store, dependent *Job) *Job {
	modified := testDBJob("echo record dependent modified", "record-dependent")
	modified.Dependencies = dependent.Dependencies
	So(s.modifyLiveJobs(ctx, []string{dependent.Key()}, []*Job{modified}), ShouldBeNil)

	return modified
}

// flippedChangeAheadStore is a store whose prepareJobChange discards the handle
// its store prepared and returns none, or returns a handle where its store
// prepared none: a store whose handle answer differs from the recorded one,
// whether the recording was of bbolt, which prepares handles, or of the log
// store, which does not.
type flippedChangeAheadStore struct {
	store
}

func (f flippedChangeAheadStore) prepareJobChange(ctx context.Context, job *Job,
	change func(*Job)) *jobChangeAhead {
	ahead := f.store.prepareJobChange(ctx, job, change)
	if ahead == nil {
		return &jobChangeAhead{key: job.Key()}
	}

	f.discardJobChangeAhead(job, ahead)

	return nil
}

// TestStoreRecordReplayLiveDependents covers the live dependents of an add in
// spec A5: a bbolt recording of an add reading a started live dependent, after
// each write that leaves bbolt's live record of it other than as it was
// added, replays against the log store with no mismatch.
func TestStoreRecordReplayLiveDependents(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	unwrapped := func(s store) store { return s }

	for name, write := range dependentWrites() {
		Convey("Given a bbolt recording of an add reading a started live dependent "+name, t, func() {
			path := filepath.Join(t.TempDir(), "dependent.rec")
			recordDependentRead(t, ctx, path, unwrapped, write)

			Convey("replayed against a fresh log store, there is no mismatch", func() {
				replayTo, _ := openTestStore(t, ctx, storeKindLog)

				stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
				So(err, ShouldBeNil)
				So(mismatchedMethods(stats), ShouldBeEmpty)
			})
		})
	}

	Convey("Given a bbolt recording of an add reading a started live dependent changed ahead with no handle, "+
		"which bbolt does not store", t, func() {
		path := filepath.Join(t.TempDir(), "dependent.rec")
		flipped := func(s store) store { return flippedChangeAheadStore{store: s} }
		recordDependentRead(t, ctx, path, flipped, func(ctx context.Context, s store, dependent *Job) *Job {
			ahead := s.prepareJobChange(ctx, dependent, recordKickChange)
			So(ahead, ShouldBeNil)
			s.queueJobChangeAhead(ctx, dependent, ahead, recordKickChange)

			return dependent
		})

		Convey("replayed against a fresh log store, there is no mismatch", func() {
			replayTo, _ := openTestStore(t, ctx, storeKindLog)

			stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
			So(err, ShouldBeNil)
			So(mismatchedMethods(stats), ShouldBeEmpty)
		})
	})

	Convey("Given a bbolt recording of an add reading a started live dependent reserved through "+
		"reserveRunState and added again, with the reservation rewritten as Phase 0's "+
		"updateJobRunStateDurableWithin", t, func() {
		recorded := filepath.Join(t.TempDir(), "dependent.rec")
		recordDependentRead(t, ctx, recorded, unwrapped, addRunStateReservedDependentAgain)

		path, rewritten := asPhase0Within(t, recorded)
		So(rewritten, ShouldEqual, 1)

		Convey("replayed against a fresh log store, there is no mismatch", func() {
			replayTo, _ := openTestStore(t, ctx, storeKindLog)

			stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
			So(err, ShouldBeNil)
			So(mismatchedMethods(stats), ShouldBeEmpty)
		})
	})

	Convey("Given a bbolt recording through a store whose add reading a live dependent also fails, "+
		"of that add reading a started live dependent", t, func() {
		path := filepath.Join(t.TempDir(), "dependent.rec")
		failing := func(s store) store { return failingDependentsAddStore{store: s} }
		recordDependentRead(t, ctx, path, failing, func(_ context.Context, _ store, dependent *Job) *Job {
			return dependent
		})

		Convey("replayed against such a log store, there is no mismatch", func() {
			replayTo, _ := openTestStore(t, ctx, storeKindLog)

			stats, err := replayRecordingFolding(ctx, path, failingDependentsAddStore{store: replayTo}, replayTo)
			So(err, ShouldBeNil)
			So(mismatchedMethods(stats), ShouldBeEmpty)
		})
	})
}

// dependentWrites are, by name, the writes after which bbolt's live record of
// a job, which is what bbolt answers it as when an add reads it as a live
// dependent, is not as the job was added (spec A5).
func dependentWrites() map[string]dependentWrite {
	return map[string]dependentWrite{
		"modified":                 modifyDependent,
		"modified keeping its key": modifyDependentKeepingKey,
		"reserved through reserveRunState and added again":                    addRunStateReservedDependentAgain,
		"delayed, deleted and added again":                                    readdDeletedDependent,
		"alongside a second started dependent with other Dependencies":        startSecondDependent,
		"reserved, put back to run again, and added again":                    readdReservedPutBackDependent,
		"delayed, modified to another key, and added again under its old key": readdModifiedDependent,
		"added again in another RepGroup":                                     addDependentAgain,
		"added again in another RepGroup once reserved":                       addReservedDependentAgain,
		"added again in another RepGroup once delayed":                        addDelayedDependentAgain,
		"kept live by its archive and added again ignoring complete jobs":     addArchivedDependentAgain,
		"changed durably":                         changeDependentDurably,
		"changed ahead with a handle":             changeDependentAhead,
		"exited durably":                          exitDependentDurably,
		"exited":                                  exitDependent,
		"put back to run again":                   putBackDependent,
		"kept live by its archive":                archiveDependentKeptLive,
		"marked to run again":                     markDependent,
		"not marked to run again, being unmarked": unmarkedDependent,
	}
}

// recordDependentRead records to path, against bbolt wrapped by wrap: an add
// of a job depending on a dep group, write to it, its start, and then an add
// of a member of that dep group, which reads the started job as a live
// dependent. bbolt answers that dependent as its live record holds it, without
// its run state, and the log store with its latest state.
func recordDependentRead(t *testing.T, ctx context.Context, path string, wrap func(store) store,
	write dependentWrite) {
	t.Helper()

	s, err := newRecordingStore(ctx, wrap(openStoreIn(ctx, storeKindBolt, t.TempDir())), path)
	So(err, ShouldBeNil)

	dependent := testDBJob("echo record dependent", "record-dependent")
	dependent.Dependencies = Dependencies{NewDepGroupDependency(recordTestDepGroup)}
	_, err = s.storeNewJobsGuarded(ctx, []*Job{dependent}, false, nil)
	So(err, ShouldBeNil)

	dependent = write(ctx, s, dependent)
	startDependent(s, dependent)

	member := testDBJob("echo record member", "record-member")
	member.DepGroups = []string{recordTestDepGroup}

	stored, err := s.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
	So(slices.ContainsFunc(stored.dependents.jobs, func(job *Job) bool { return job.Key() == dependent.Key() }),
		ShouldBeTrue)

	for _, read := range stored.dependents.jobs {
		So(read.State, ShouldNotEqual, JobStateRunning)
	}

	if errors.Is(err, errBoom) {
		err = nil
	}

	So(err, ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)
}

// startDependent starts dependent, which bbolt writes to its run state, apart
// from its live record.
func startDependent(s store, dependent *Job) {
	dependent.State = JobStateRunning
	dependent.StartTime = recordTestBase.Add(time.Minute)
	dependent.Host = recordTestHost
	So(s.updateJobRunStateDurable(dependent), ShouldBeNil)
}

// asPhase0Within writes a copy of the recording at path with each
// reserveRunState call made the call Phase 0 recorded for a reservation,
// updateJobRunStateDurableWithin, waited for within reserveDurableWait. It
// returns the copy's path and the number of calls it rewrote. No current
// recorder records that call, so this is how a test reaches its replay.
func asPhase0Within(t *testing.T, path string) (string, int) {
	t.Helper()

	calls := recordedCalls(t, path)

	var rewritten int

	for i, call := range calls {
		if call.Method != "reserveRunState" {
			continue
		}

		var args recJob
		So(decodeRecorded(call.Args, &args), ShouldBeNil)

		calls[i].Method = "updateJobRunStateDurableWithin"
		calls[i].Args = encodeRecorded(recWithin{Job: args.Job, Wait: reserveDurableWait()})
		rewritten++
	}

	return writeRecording(t, calls...), rewritten
}

// recordedCalls returns the calls of the recording at path, in order.
func recordedCalls(t *testing.T, path string) []recordedCall {
	t.Helper()

	file, err := os.Open(path)
	So(err, ShouldBeNil)

	defer file.Close()

	var calls []recordedCall

	_, err = wal.Scan(file, func(_ wal.Ref, rec []byte) error {
		var call recordedCall
		if errd := decodeRecorded(rec, &call); errd != nil {
			return errd
		}

		calls = append(calls, call)

		return nil
	})
	So(err, ShouldBeNil)

	return calls
}

// shouldBeChangeAheadHandle returns the assertion a change-ahead handle that a
// store of the given kind returns meets: bbolt returns one for each prepared
// or moved change, and the log store returns none (spec, The store
// interface).
func shouldBeChangeAheadHandle(kind storeKind) func(any, ...any) string {
	if kind == storeKindLog {
		return ShouldBeNil
	}

	return ShouldNotBeNil
}

// replayRecordingFolding is replayRecording against s, with replayed, the store
// s is or wraps, folded by learnedFold before each probe of learned
// requirements, as spec A5 says a replay must for the log store.
func replayRecordingFolding(ctx context.Context, path string, s, replayed store) (replayStats, error) {
	rp := newReplayer(ctx, s)
	rp.fold = learnedFold(replayed)

	return rp.replayFile(path)
}

// learnedFold returns the fold a replay against s runs before each probe of
// its learned requirements: for the log store, which learns a sample only once
// its folder folds it (spec D1), its flush; for bbolt, which learns as it
// archives, nil, since its flush also waits for every expected job exit update
// and so never returns while the replay has one it has yet to finish.
func learnedFold(s store) func() error {
	if ls, ok := s.(*logStore); ok {
		return ls.flush
	}

	return nil
}

func changeDependentAhead(ctx context.Context, s store, dependent *Job) *Job {
	delay := func(job *Job) { job.State, job.UntilBuried = JobStateDelayed, 2 }

	ahead := s.prepareJobChange(ctx, dependent, delay)
	So(ahead, ShouldNotBeNil)
	s.queueJobChangeAhead(ctx, dependent, ahead, delay)

	return dependent
}

// undependentStore is a store whose storeNewJobsGuarded answers each live
// dependent with no dependencies: a store that gets wrong the one field of a
// dependent a server reads besides its key.
type undependentStore struct {
	store
}

func (u undependentStore) storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
	guard *rerunGuard) (storedNewJobs, error) {
	stored, err := u.store.storeNewJobsGuarded(ctx, jobs, ignoreAdded, guard)
	for _, job := range stored.dependents.jobs {
		job.Dependencies = nil
	}

	return stored, err
}

// startSecondDependent adds and starts a second job depending on dependent's
// dep group, and on another, so that the add reading dependent also reads a
// job with other Dependencies.
func startSecondDependent(ctx context.Context, s store, dependent *Job) *Job {
	second := testDBJob("echo record second dependent", "record-dependent")
	second.Dependencies = Dependencies{
		NewDepGroupDependency(recordTestDepGroup),
		NewDepGroupDependency(recordTestDepGroup2),
	}
	So(second.Dependencies, ShouldNotResemble, dependent.Dependencies)

	_, err := s.storeNewJobsGuarded(ctx, []*Job{second}, false, nil)
	So(err, ShouldBeNil)
	startDependent(s, second)

	return dependent
}

// addAgain adds a job with dependent's key and Dependencies in another
// RepGroup, ignoring complete jobs if ignoreComplete.
func addAgain(ctx context.Context, s store, dependent *Job, ignoreComplete bool) storedNewJobs {
	again := testDBJob(dependent.Cmd, "record-dependent-again")
	again.Dependencies = dependent.Dependencies
	So(again.Key(), ShouldEqual, dependent.Key())

	stored, err := s.storeNewJobsGuarded(ctx, []*Job{again}, ignoreComplete, nil)
	So(err, ShouldBeNil)

	return stored
}

// addAfresh adds a job with dependent's key and Dependencies in another
// RepGroup, which bbolt stores, and returns it.
func addAfresh(ctx context.Context, s store, dependent *Job) *Job {
	again := testDBJob(dependent.Cmd, "record-dependent-again")
	again.Dependencies = dependent.Dependencies

	stored, err := s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
	So(err, ShouldBeNil)
	So(stored.jobsToQueue, ShouldHaveLength, 1)

	return again
}

// nilMoveStore is a store whose moveJobChangeAhead discards the handle its
// store moved and returns none: a store that drops a handle it was given, which
// only prepareJobChange may answer none for (spec, The store interface).
type nilMoveStore struct {
	store
}

func (n nilMoveStore) moveJobChangeAhead(from, to *Job, ahead *jobChangeAhead) *jobChangeAhead {
	n.discardJobChangeAhead(to, n.store.moveJobChangeAhead(from, to, ahead))

	return nil
}

// failingDependentsAddStore is a store whose storeNewJobsGuarded, when it
// answers live dependents, also answers errBoom.
type failingDependentsAddStore struct {
	store
}

func (f failingDependentsAddStore) storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
	guard *rerunGuard) (storedNewJobs, error) {
	stored, err := f.store.storeNewJobsGuarded(ctx, jobs, ignoreAdded, guard)
	if len(stored.dependents.jobs) > 0 {
		err = errors.Join(err, errBoom)
	}

	return stored, err
}

// dependentWrite is a write recordDependentRead makes to its live dependent
// between the dependent's add and the add that reads it, returning the job
// under the key that add reads.
type dependentWrite func(ctx context.Context, s store, dependent *Job) *Job

func archiveDependentKeptLive(_ context.Context, s store, dependent *Job) *Job {
	dependent.RerunAfterRun = true
	dependent.ReservedBy = uuid.Must(uuid.NewV4())
	dependent.State, dependent.Exited = JobStateComplete, true
	dependent.EndTime = recordTestBase.Add(time.Hour)

	outcome, err := s.archiveCompletion(dependent.Key(), dependent)
	So(err, ShouldBeNil)
	So(outcome, ShouldEqual, archiveKeptLive)

	return dependent
}

// replayedFoldErrors replays the recording at path against a fresh store of
// the given kind with a fold that fails with errBoom on its failAt'th call
// (never, for 0). It returns the number of folds the replay ran if it then
// failed with that error, or ran them all with no error; -1 otherwise.
func replayedFoldErrors(t *testing.T, ctx context.Context, kind storeKind, path string, failAt int) int {
	t.Helper()

	replayTo, _ := openTestStore(t, ctx, kind)

	var folds int

	rp := newReplayer(ctx, replayTo)
	rp.fold = func() error {
		folds++
		if folds == failAt {
			return errBoom
		}

		return nil
	}

	_, err := rp.replayFile(path)
	if (failAt == 0 && err == nil) || (failAt > 0 && errors.Is(err, errBoom)) {
		return folds
	}

	return -1
}

// foldedRunToCompletion is recordRunToCompletion followed by a flush of inner,
// the store s records, so that its learned requirements include job's sample:
// the log store learns a sample only once its folder folds it, which lags the
// archive (spec D1), where bbolt learns it as it archives. A flush is not a
// store call, so it is not recorded.
func foldedRunToCompletion(s, inner store, job *Job, i int) {
	recordRunToCompletion(s, job, i)
	So(inspectorOf(inner).flush(), ShouldBeNil)
}

// recordLearnedReadBetweenArchives records to path, against bbolt, which learns
// a sample as it archives: an archive, a load of its ReqGroup's learned
// requirements, and a second archive of a higher peak RAM, the last call.
func recordLearnedReadBetweenArchives(t *testing.T, ctx context.Context, path string) {
	t.Helper()

	s := openRecordingStoreIn(t, ctx, storeKindBolt, path)

	jobs := learnedTestJobs(100, 300)
	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	recordRunToCompletion(s, jobs[0], 0)
	So(s.refreshReqGroupRecs(learnedTestReqGroup).RAM, ShouldEqual, 100)
	recordRunToCompletion(s, jobs[1], 1)
	So(s.close(ctx), ShouldBeNil)
}

// TestStoreReplay replays the recording named by WR_STORE_REPLAY against a
// fresh store of each kind WR_TEST_STORE selects, folding it before each probe
// of learned requirements as spec A5 says. It is the A5 gate's runner, and
// skips when WR_STORE_REPLAY is unset.
func TestStoreReplay(t *testing.T) {
	path := os.Getenv(storeReplayEnv)
	if path == "" || runnermode || servermode {
		t.Skipf("%s is not set", storeReplayEnv)
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("The recording at %s replays against a fresh %s store with no mismatch", path,
			storeKindName(kind)), t, func() {
			replayTo, _ := openTestStore(t, ctx, kind)

			stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
			t.Logf("replayed %d calls, %d skipped, %d mismatches", stats.calls, stats.skipped,
				len(stats.mismatches))
			So(err, ShouldBeNil)
			So(stats.mismatches, ShouldBeEmpty)
		})
	})
}

// recordKickChange is the change recordChangesAhead makes ahead of time.
func recordKickChange(job *Job) {
	job.State = JobStateReady
	job.UntilBuried = 3
}

// dropLastCompleteStore is a store whose retrieveCompleteJobsByKeys drops its
// last result: a store that answers wrongly, which a replay must catch.
type dropLastCompleteStore struct {
	store
}

func (d dropLastCompleteStore) retrieveCompleteJobsByKeys(keys []string) ([]*Job, error) {
	jobs, err := d.store.retrieveCompleteJobsByKeys(keys)
	if len(jobs) > 0 {
		jobs = jobs[:len(jobs)-1]
	}

	return jobs, err
}

func TestStoreRecordReplay(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a scripted workload recorded against a %s store", storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "workload.rec")
			recordWorkloadTo(t, ctx, kind, path)

			methods := recordedMethods(t, path)

			Convey("every store method but close was recorded", func() {
				recorded := make(map[string]bool)
				for _, method := range methods {
					recorded[method] = true
				}

				var missing []string

				for _, name := range storeMethodNames() {
					if name != "close" && !recorded[name] {
						missing = append(missing, name)
					}
				}

				So(missing, ShouldBeEmpty)
				So(recorded[expectJobExitUpdateDone], ShouldBeTrue)
			})

			Convey("replayed against a fresh store, every call replays with no skip or mismatch", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
				So(err, ShouldBeNil)
				So(stats.calls, ShouldEqual, len(methods))
				So(stats.skipped, ShouldEqual, 0)
				So(stats.mismatches, ShouldBeEmpty)
			})

			Convey("replayed against a store whose retrieveCompleteJobsByKeys drops its last result, "+
				"the first mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, dropLastCompleteStore{store: replayTo}, replayTo)
				So(err, ShouldBeNil)
				So(stats.mismatches, ShouldNotBeEmpty)
				So(stats.mismatches[0].Method, ShouldEqual, "retrieveCompleteJobsByKeys")
			})

			Convey("replayed against a store asking a dependency question the recording has no answer to, "+
				"the first mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, unaskedQuestions()[0].asking(replayTo), replayTo)
				So(err, ShouldBeNil)
				So(stats.mismatches, ShouldNotBeEmpty)
				So(stats.mismatches[0].Method, ShouldEqual, "resolveDependencies")
			})

			Convey("replayed against a store asking any question the recording has no answer to, "+
				"the only mismatch is the call that asked it", func() {
				questions := unaskedQuestions()
				got, want := make([][]string, 0, len(questions)), make([][]string, 0, len(questions))

				for _, question := range questions {
					replayTo, _ := openTestStore(t, ctx, kind)

					stats, err := replayRecordingFolding(ctx, path, question.asking(replayTo), replayTo)
					So(err, ShouldBeNil)

					got = append(got, mismatchedMethods(stats))
					want = append(want, []string{question.method})
				}

				So(got, ShouldResemble, want)
			})

			Convey("replayed against a store whose archiveCompletion reports writing nothing, "+
				"the mismatches are those calls", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, notWrittenArchiveStore{store: replayTo}, replayTo)
				So(err, ShouldBeNil)
				So(mismatchedMethods(stats), ShouldResemble, []string{"archiveCompletion"})
			})

			Convey("replayed against a store whose prepareJobChange answers a handle where the recording "+
				"has none, the first mismatch is that call; or none where it has one, there is no mismatch, "+
				"since any store may prepare none", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, flippedChangeAheadStore{store: replayTo}, replayTo)
				So(err, ShouldBeNil)

				if kind == storeKindLog {
					So(stats.mismatches, ShouldNotBeEmpty)
					So(stats.mismatches[0].Method, ShouldEqual, "prepareJobChange")
				} else {
					So(stats.mismatches, ShouldBeEmpty)
				}
			})

			Convey("replayed against a store whose moveJobChangeAhead answers none where it was given a handle, "+
				"the first mismatch is that call; given none, there is no mismatch", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, nilMoveStore{store: replayTo}, replayTo)
				So(err, ShouldBeNil)

				if kind == storeKindBolt {
					So(stats.mismatches, ShouldNotBeEmpty)
					So(stats.mismatches[0].Method, ShouldEqual, "moveJobChangeAhead")
				} else {
					So(stats.mismatches, ShouldBeEmpty)
				}
			})

			Convey("replayed against a store whose add answers its live dependent without its dependencies, "+
				"the mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, undependentStore{store: replayTo}, replayTo)
				So(err, ShouldBeNil)
				So(mismatchedMethods(stats), ShouldResemble, []string{"storeNewJobsGuarded"})
			})

			Convey("replayed against a store whose retrieveRepGroups also returns an error, "+
				"the mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, boomRepGroupsStore{store: replayTo}, replayTo)
				So(err, ShouldBeNil)
				So(mismatchedMethods(stats), ShouldResemble, []string{recordRepGroupsMethod})
			})

			Convey("replayed against a store giving its unordered results in reverse, there is no mismatch",
				func() {
					replayTo, _ := openTestStore(t, ctx, kind)

					stats, err := replayRecordingFolding(ctx, path, reversingStore{store: replayTo}, replayTo)
					So(err, ShouldBeNil)
					So(stats.mismatches, ShouldBeEmpty)
				})

			Convey("replayed, the replay no longer holds the jobs it saw archived or deleted", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				rp := newReplayer(ctx, replayTo)
				rp.fold = learnedFold(replayTo)
				stats, err := rp.replayFile(path)
				So(err, ShouldBeNil)
				So(stats.mismatches, ShouldBeEmpty)

				var held []string

				for i := range 50 {
					a := testDBJob(fmt.Sprintf("echo record a %d", i), recordTestRepGroupA).Key()
					b := testDBJob(fmt.Sprintf("echo record b %d", i), recordTestRepGroupB).Key()

					// a[0] and a[1] are named again after they are archived
					if i > 1 && rp.jobs[a] != nil {
						held = append(held, a)
					}

					if i < 10 && rp.jobs[b] != nil {
						held = append(held, b)
					}
				}

				So(held, ShouldBeEmpty)
				So(rp.jobs, ShouldContainKey, testDBJob("echo record b 20", recordTestRepGroupB).Key())
			})

			Convey("with its last frame truncated by 3 bytes, it replays every complete frame with no error", func() {
				info, err := os.Stat(path)
				So(err, ShouldBeNil)
				So(os.Truncate(path, info.Size()-3), ShouldBeNil)

				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
				So(err, ShouldBeNil)
				So(stats.calls, ShouldEqual, len(methods)-1)
				So(stats.mismatches, ShouldBeEmpty)
			})
		})

		Convey(fmt.Sprintf("Given a %s store opened without %s, it is not wrapped", storeKindName(kind), storeRecordEnv),
			t, func() {
				t.Setenv(storeRecordEnv, "")

				s, _ := openTestStore(t, ctx, kind)
				_, recording := s.(*recordingStore)
				So(recording, ShouldBeFalse)
			})

		Convey(fmt.Sprintf("Given %s naming a file that exists, a %s store does not open", storeRecordEnv,
			storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "exists.rec")
			So(os.WriteFile(path, []byte("kept"), recordFileMode), ShouldBeNil)

			t.Setenv(storeRecordEnv, path)

			dir := t.TempDir()
			_, _, err := openStore(ctx, storeConfig{
				dbFile:     filepath.Join(dir, "queue.db"),
				dbBkFile:   filepath.Join(dir, testStoreBackupFile),
				deployment: "development",
				kind:       kind,
			})
			So(err, ShouldNotBeNil)

			content, err := os.ReadFile(path)
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, "kept")
		})
	})
}

// recordWorkloadTo records recordWorkload against a store of the given kind to
// path.
func recordWorkloadTo(t *testing.T, ctx context.Context, kind storeKind, path string) {
	t.Helper()

	s := openRecordingStoreIn(t, ctx, kind, path)
	recordWorkload(ctx, s, kind)
	So(s.close(ctx), ShouldBeNil)
}

// openRecordingStoreIn opens a store of the given kind in a new temporary
// directory through openStore with WR_STORE_RECORD set to path, so that it
// records every call to path. The caller closes it, which ends the recording.
func openRecordingStoreIn(t *testing.T, ctx context.Context, kind storeKind, path string) store {
	t.Helper()

	t.Setenv(storeRecordEnv, path)

	s := openStoreIn(ctx, kind, t.TempDir())

	t.Setenv(storeRecordEnv, "")

	return s
}

// recordWorkload makes calls to s covering every store method: it adds 100 jobs
// in 2 calls, reserves, starts and archives 50 of them, deletes 10 and modifies
// 5, reads history every way a server does (status by RepGroup, recent
// completions with a fixed cutoff, by key), kicks a buried job through
// prepareJobChange and queueJobChangeAhead, discards and moves changes ahead,
// makes a guarded add, spends an archived-bytes budget and resolves the
// dependencies of two jobs. s is a recordingStore wrapping a store of the given
// kind.
func recordWorkload(ctx context.Context, s store, kind storeKind) {
	_, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)
	So(s.upgradedOnOpen(), ShouldBeFalse)

	s.setRecRounding(1, 100)
	s.setReqStatRefreshInterval(time.Minute)

	a, b := make([]*Job, 50), make([]*Job, 50)
	for i := range 50 {
		a[i] = testDBJob(fmt.Sprintf("echo record a %d", i), recordTestRepGroupA)
		b[i] = testDBJob(fmt.Sprintf("echo record b %d", i), recordTestRepGroupB)
	}

	b[30].DepGroups = []string{recordTestDepGroup, recordTestDepGroup2}
	b[31].Dependencies = Dependencies{NewDepGroupDependency(recordTestChildDepGroup)}

	for _, jobs := range [][]*Job{a, b} {
		_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
	}

	So(s.storeRepGroupLookups(ctx, b[:2]), ShouldBeNil)

	envKey, err := s.storeEnv([]byte("recorded env"))
	So(err, ShouldBeNil)
	So(string(s.retrieveEnv(ctx, envKey)), ShouldEqual, "recorded env")

	_, _, err = s.storeLimitGroups(map[string]*limiter.GroupData{recordTestLimitGroup: limiter.NewCountGroupData(5)})
	So(err, ShouldBeNil)
	So(s.retrieveLimitGroup(ctx, recordTestLimitGroup).Limit(), ShouldEqual, 5)

	_, err = s.retrieveStoredLimits([]string{recordTestLimitGroup})
	So(err, ShouldBeNil)

	for i, job := range a {
		recordRunToCompletion(s, job, i)
	}

	recordJobExits(ctx, s, b)

	deleted := make([]string, 10)
	for i := range deleted {
		deleted[i] = b[i].Key()
	}

	So(s.deleteLiveJobs(ctx, deleted), ShouldBeNil)

	oldKeys, modified := make([]string, 0, 5), make([]*Job, 0, 5)
	for i, job := range b[10:15] {
		oldKeys = append(oldKeys, job.Key())
		modified = append(modified, testDBJob(fmt.Sprintf("echo record b modified %d", i), recordTestRepGroupB))
	}

	So(s.modifyLiveJobs(ctx, oldKeys, modified), ShouldBeNil)

	recordChangesAhead(ctx, s, kind, b)
	recordHistoryReads(ctx, s, a)
	recordGuardedAdd(ctx, s, b[30], b[31])

	// the log store learns a sample only once its folder folds it (spec D1),
	// so without this flush the reads below see however many of a's samples
	// the folder has folded by then, a value a folding replay never notes. A
	// flush is not a store call, so it is not recorded.
	recording, ok := s.(*recordingStore)
	So(ok, ShouldBeTrue)
	So(inspectorOf(recording.s).flush(), ShouldBeNil)
	recordReqGroupCalls(s, a)

	So(s.backup(io.Discard), ShouldBeNil)

	b[24].RerunAfterRun = true
	So(s.storeRunningRerunMarks([]*Job{b[24]}), ShouldBeNil)
	So(s.storeLiveForRerun([]*Job{a[0]}), ShouldBeNil)
}

// recordRunToCompletion reserves, starts and archives job, the i'th of its
// add, at times fixed by i.
func recordRunToCompletion(s store, job *Job, i int) {
	job.State = JobStateReserved
	So(s.updateJobRunStateDurable(job), ShouldBeNil)

	job.State = JobStateRunning
	job.StartTime = recordTestBase.Add(time.Duration(i) * time.Second)
	job.Host = recordTestHost
	So(s.updateJobRunStateDurable(job), ShouldBeNil)

	job.State = JobStateComplete
	job.Exited = true
	job.EndTime = job.StartTime.Add(time.Minute)
	outcome, err := s.archiveCompletion(job.Key(), job)
	So(err, ShouldBeNil)
	So(outcome, ShouldEqual, archiveRemovedLive)
}

// recordJobExits buries b[20] and b[21] with output, durably, and records a
// change and a best-effort exit of others.
func recordJobExits(ctx context.Context, s store, b []*Job) {
	for _, job := range b[20:22] {
		job.State = JobStateBuried
		job.Exited = true
		job.Exitcode = 1
		So(s.updateJobAfterExitDurable(job, []byte("o"), []byte("e"), false), ShouldBeNil)
	}

	stdo, stde := s.retrieveJobStd(ctx, b[20].Key())
	So(string(stdo), ShouldEqual, "o")
	So(string(stde), ShouldEqual, "e")

	done := s.expectJobExitUpdate()
	b[22].State = JobStateBuried
	b[22].Exited = true
	s.updateJobAfterExit(ctx, b[22], []byte("o22"), nil, false)
	done()

	// waits for every expected exit update, so it hangs if done is not called.
	s.retrieveJobStd(ctx, b[22].Key())

	b[23].State = JobStateDelayed
	So(s.updateJobAfterChangeDurable(b[23]), ShouldBeNil)

	b[41].State = JobStateReserved
	reservation, err := s.reserveRunState(b[41])
	So(err, ShouldBeNil)

	durable, err := reservation.WaitWithin(time.Minute)
	So(durable, ShouldBeTrue)
	So(err, ShouldBeNil)
}

// recordChangesAhead kicks buried b[20] through prepareJobChange and
// queueJobChangeAhead, prepares and discards a kick of b[21], and moves a change
// prepared for b[42] onto a job replacing it. bbolt returns a handle from each
// prepare and move, so its recording maps handles; the log store returns none
// (spec, The store interface), so its recording holds nil ones.
func recordChangesAhead(ctx context.Context, s store, kind storeKind, b []*Job) {
	handle := shouldBeChangeAheadHandle(kind)

	ahead := s.prepareJobChange(ctx, b[20], recordKickChange)
	So(ahead, handle)
	s.queueJobChangeAhead(ctx, b[20], ahead, recordKickChange)

	discarded := s.prepareJobChange(ctx, b[21], recordKickChange)
	So(discarded, handle)
	s.discardJobChangeAhead(b[21], discarded)

	replacement := testDBJob("echo record b 42 replacement", recordTestRepGroupB)
	moving := s.prepareJobChange(ctx, b[42], recordKickChange)
	moved := s.moveJobChangeAhead(b[42], replacement, moving)
	So(moved, handle)
	s.queueJobChangeAhead(ctx, replacement, moved, recordKickChange)
}

// recordGuardedAdd starts dependent, then adds, under a rerun guard, a job
// depending on parent's two dep groups whose own dep group the running
// dependent depends on, archives dependent while the guard is held, which keeps
// it live to run again, then resolves the dependencies of the added job and
// dependent. The add's live dependent is running, so bbolt, which answers it as
// it was added, and the log store, which answers it with its latest state,
// answer it differently.
func recordGuardedAdd(ctx context.Context, s store, parent, dependent *Job) {
	dependent.State = JobStateRunning
	dependent.StartTime = recordTestBase.Add(time.Minute)
	dependent.Host = recordTestHost
	So(s.updateJobRunStateDurable(dependent), ShouldBeNil)

	child := testDBJob("echo record child", recordTestRepGroupChild)
	child.Dependencies = Dependencies{
		NewDepGroupDependency(recordTestDepGroup),
		NewDepGroupDependency(recordTestDepGroup2),
	}
	child.DepGroups = []string{recordTestChildDepGroup}

	live, err := s.checkIfLive(parent.Key())
	So(err, ShouldBeNil)
	So(live, ShouldBeTrue)

	seen, err := s.depGroupEverSeen(recordTestDepGroup)
	So(err, ShouldBeNil)
	So(seen, ShouldBeTrue)

	_, err = s.depGroupsEverSeen([]string{recordTestDepGroup, "record-unseen"})
	So(err, ShouldBeNil)

	guard := s.registerRerunGuard()

	stored, err := s.storeNewJobsGuarded(ctx, []*Job{child}, false, guard)
	So(err, ShouldBeNil)
	So(stored.dependents.jobs, ShouldHaveLength, 1)

	dependent.State = JobStateComplete
	dependent.Exited = true
	dependent.EndTime = recordTestBase.Add(time.Hour)
	outcome, err := s.archiveCompletion(dependent.Key(), dependent)
	So(err, ShouldBeNil)
	So(outcome, ShouldEqual, archiveRemovedLive)

	keptLive, err := s.checkIfLive(dependent.Key())
	So(err, ShouldBeNil)
	So(keptLive, ShouldBeTrue)

	s.releaseRerunGuard(guard)

	groups := newDepGroupMembers()
	groups.add([]string{recordTestDepGroup, recordTestDepGroup2}, parent.Key())

	resolved, err := s.resolveDependencies(ctx, []*Job{child, dependent}, groups)
	So(err, ShouldBeNil)
	So(resolved, ShouldHaveLength, 2)
	So(resolved[0].deps, ShouldHaveLength, 2)
}

// recordReqGroupCalls loads and reads the learned requirements of a's
// ReqGroup.
func recordReqGroupCalls(s store, a []*Job) {
	s.warmReqGroupRecs(a[:2])
	So(s.refreshReqGroupRecs(recordTestReqGroup), ShouldNotBeNil)

	_, cached := s.cachedReqGroupRecs(recordTestReqGroup)
	So(cached, ShouldBeTrue)

	s.refreshReqGroupRecsLater("record-unread-reqgroup")
	s.reqStatChanges()
}

// recordedMethods returns the method of each call in the recording at path, in
// order.
func recordedMethods(t *testing.T, path string) []string {
	t.Helper()

	calls := recordedCalls(t, path)
	methods := make([]string, len(calls))

	for i, call := range calls {
		methods[i] = call.Method
	}

	return methods
}

// storeMethodNames returns the name of every method of the store interface.
func storeMethodNames() []string {
	storeType := reflect.TypeFor[store]()
	names := make([]string, storeType.NumMethod())

	for i := range names {
		names[i] = storeType.Method(i).Name
	}

	return names
}

// recordHistoryReads reads a's archived history every way the store offers.
func recordHistoryReads(ctx context.Context, s store, a []*Job) {
	keys := []string{a[1].Key(), a[2].Key(), a[3].Key()}
	found, err := s.retrieveCompleteJobsByKeys(keys)
	So(err, ShouldBeNil)
	So(found, ShouldHaveLength, 3)

	all, err := s.retrieveCompleteJobsByRepGroup(recordTestRepGroupA)
	So(err, ShouldBeNil)
	So(all, ShouldHaveLength, 50)

	page, err := s.retrieveOldestCompleteJobsByRepGroup(recordTestRepGroupA, completeJobsQuery{
		group: func(facets *archivedJobFacets) (string, bool) { return string(facets.State), true },
		limit: func(string) int { return 10 },
	})
	So(err, ShouldBeNil)
	So(page.jobs, ShouldHaveLength, 10)

	status, err := s.retrieveCompleteJobStatusByRepGroup(recordTestRepGroupA, true)
	So(err, ShouldBeNil)
	So(status.Counts[JobStateComplete], ShouldEqual, 50)

	recent, err := s.retrieveCompleteJobsRecent(recordTestBase.Add(25*time.Second + time.Minute))
	So(err, ShouldBeNil)
	So(len(recent), ShouldBeBetween, 0, 50)

	_, err = s.retrieveLastCompletionTimeByRepGroup([]string{recordTestRepGroupA, recordTestRepGroupB})
	So(err, ShouldBeNil)
	So(s.repGroupHasHistory(recordTestRepGroupA), ShouldBeTrue)

	_, err = s.retrieveRepGroups()
	So(err, ShouldBeNil)

	complete, err := s.checkIfComplete(a[4].Key())
	So(err, ShouldBeNil)
	So(complete, ShouldBeTrue)

	So(s.spendArchivedBytesByRepGroup(recordTestRepGroupA, newArchivedBytesBudget()), ShouldBeNil)

	_ = ctx
}

// askingStore is a store that asks a question before resolving dependencies
// or before reading the oldest complete jobs, through askGroups or askQuery when
// set: a store that asks what the recorded one did not.
type askingStore struct {
	store

	askGroups func(depGroupState)
	askQuery  func(completeJobsQuery)
}

func (a askingStore) resolveDependencies(ctx context.Context, jobs []*Job,
	groups depGroupState) ([]resolvedJob, error) {
	if a.askGroups != nil {
		a.askGroups(groups)
	}

	return a.store.resolveDependencies(ctx, jobs, groups)
}

func (a askingStore) retrieveOldestCompleteJobsByRepGroup(repgroup string,
	query completeJobsQuery) (*completeJobsPage, error) {
	if a.askQuery != nil {
		a.askQuery(query)
	}

	return a.store.retrieveOldestCompleteJobsByRepGroup(repgroup, query)
}

// unaskedQuestion is a store over s asking one question the workload
// recording has no answer to, and the store method that asks it.
type unaskedQuestion struct {
	asking func(s store) askingStore
	method string
}

// unaskedQuestions are an unaskedQuestion for each question a store can ask:
// hasMembers, liveCommandVariants, group and limit.
func unaskedQuestions() []unaskedQuestion {
	return []unaskedQuestion{
		{asking: func(s store) askingStore {
			return askingStore{store: s, askGroups: func(g depGroupState) { g.hasMembers(recordNeverAsked) }}
		}, method: "resolveDependencies"},
		{asking: func(s store) askingStore {
			return askingStore{store: s, askGroups: func(g depGroupState) { g.liveCommandVariants(recordNeverAsked) }}
		}, method: "resolveDependencies"},
		{asking: func(s store) askingStore {
			return askingStore{store: s, askQuery: func(q completeJobsQuery) {
				q.group(&archivedJobFacets{FailReason: recordNeverAsked})
			}}
		}, method: "retrieveOldestCompleteJobsByRepGroup"},
		{asking: func(s store) askingStore {
			return askingStore{store: s, askQuery: func(q completeJobsQuery) { q.limit(recordNeverAsked) }}
		}, method: "retrieveOldestCompleteJobsByRepGroup"},
	}
}

// notWrittenArchiveStore is a store whose archiveCompletion archives but
// reports that it wrote nothing: a store that answers a write wrongly.
type notWrittenArchiveStore struct {
	store
}

func (n notWrittenArchiveStore) archiveCompletion(key string, job *Job) (archiveOutcome, error) {
	_, err := n.store.archiveCompletion(key, job)

	return archiveNotWritten, err
}

// boomRepGroupsStore is a store whose retrieveRepGroups returns its groups
// with an error: a store that fails where the recorded one did not.
type boomRepGroupsStore struct {
	store
}

func (b boomRepGroupsStore) retrieveRepGroups() ([]string, error) {
	groups, err := b.store.retrieveRepGroups()

	return groups, errors.Join(err, errBoom)
}

// reversingStore is a store returning the results whose order is not part of
// the store's contract in reverse: a store that answers the same in another
// order.
type reversingStore struct {
	store
}

func (r reversingStore) retrieveCompleteJobsByRepGroup(repgroup string) ([]*Job, error) {
	jobs, err := r.store.retrieveCompleteJobsByRepGroup(repgroup)
	slices.Reverse(jobs)

	return jobs, err
}

func (r reversingStore) retrieveRepGroups() ([]string, error) {
	groups, err := r.store.retrieveRepGroups()
	slices.Reverse(groups)

	return groups, err
}

func (r reversingStore) resolveDependencies(ctx context.Context, jobs []*Job,
	groups depGroupState) ([]resolvedJob, error) {
	resolved, err := r.store.resolveDependencies(ctx, jobs, groups)
	slices.Reverse(resolved)

	for _, rj := range resolved {
		slices.Reverse(rj.deps)
	}

	return resolved, err
}

// mismatchedMethods returns the methods of stats' mismatches, sorted, each
// once.
func mismatchedMethods(stats replayStats) []string {
	methods := make([]string, len(stats.mismatches))
	for i, m := range stats.mismatches {
		methods[i] = m.Method
	}

	slices.Sort(methods)

	return slices.Compact(methods)
}

// handleNotingStore is a store that notes, by job key, the handle each
// prepareJobChange returned, and whether each queueJobChangeAhead was given the
// handle its own job's prepareJobChange returned (none, for a store that
// prepares none).
type handleNotingStore struct {
	store

	prepared map[string]*jobChangeAhead
	queued   map[string]bool
}

func (h *handleNotingStore) prepareJobChange(ctx context.Context, job *Job, change func(*Job)) *jobChangeAhead {
	ahead := h.store.prepareJobChange(ctx, job, change)
	h.prepared[job.Key()] = ahead

	return ahead
}

func (h *handleNotingStore) queueJobChangeAhead(ctx context.Context, job *Job, ahead *jobChangeAhead,
	change func(*Job)) {
	h.queued[job.Key()] = ahead == h.prepared[job.Key()]
	h.store.queueJobChangeAhead(ctx, job, ahead, change)
}

func TestStoreReplayChangeHandles(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s recording of two changes prepared and then queued in reverse order",
			storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "changes.rec")
			first := testDBJob("echo record change 1", "record-change")
			second := testDBJob("echo record change 2", "record-change")

			bury := func(job *Job) { job.State, job.UntilBuried = JobStateBuried, 0 }
			delay := func(job *Job) { job.State, job.UntilBuried = JobStateDelayed, 2 }

			s := openRecordingStoreIn(t, ctx, kind, path)
			_, err := s.storeNewJobsGuarded(ctx, []*Job{first, second}, false, nil)
			So(err, ShouldBeNil)

			firstAhead := s.prepareJobChange(ctx, first, bury)
			secondAhead := s.prepareJobChange(ctx, second, delay)
			s.queueJobChangeAhead(ctx, second, secondAhead, delay)
			s.queueJobChangeAhead(ctx, first, firstAhead, bury)
			So(s.close(ctx), ShouldBeNil)

			Convey("replayed against a store noting handles, each queue gets its own job's handle and the changes are stored",
				func() {
					replayTo, _ := openTestStore(t, ctx, kind)
					noting := &handleNotingStore{store: replayTo, prepared: make(map[string]*jobChangeAhead),
						queued: make(map[string]bool)}

					stats, err := replayRecording(ctx, path, noting)
					So(err, ShouldBeNil)
					So(stats.mismatches, ShouldBeEmpty)
					So(noting.prepared, ShouldHaveLength, 2)
					So(noting.queued, ShouldResemble, map[string]bool{first.Key(): true, second.Key(): true})

					handle := shouldBeChangeAheadHandle(kind)

					So(noting.prepared[first.Key()], handle)
					So(noting.prepared[second.Key()], handle)

					inspector := inspectorOf(replayTo)
					So(inspector.flush(), ShouldBeNil)

					stored := storedLiveJob(t, inspector, first.Key())
					So(stored.State, ShouldEqual, JobStateBuried)
					So(stored.UntilBuried, ShouldEqual, 0)

					stored = storedLiveJob(t, inspector, second.Key())
					So(stored.State, ShouldEqual, JobStateDelayed)
					So(stored.UntilBuried, ShouldEqual, 2)
				})
		})
	})
}

// TestStoreRecordReplayAcrossStores replays a bbolt recording against the log
// store, as the A5 gate replays the Phase 0 recording: the log store prepares
// no change-ahead handle where bbolt prepares one, and answers an add's running
// live dependent with its latest state where bbolt answers it as it was added,
// neither of which is a different answer to a server.
func TestStoreRecordReplayAcrossStores(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given the scripted workload recorded against a bbolt store", t, func() {
		path := filepath.Join(t.TempDir(), "workload.rec")
		recordWorkloadTo(t, ctx, storeKindBolt, path)

		Convey("replayed against a fresh log store, every call replays with no skip or mismatch", func() {
			replayTo, _ := openTestStore(t, ctx, storeKindLog)

			stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
			So(err, ShouldBeNil)
			So(stats.calls, ShouldEqual, len(recordedMethods(t, path)))
			So(stats.skipped, ShouldEqual, 0)
			So(mismatchedMethods(stats), ShouldBeEmpty)
		})
	})
}

// TestStoreRecordRemovedMethodsInput covers spec A5 acceptance test 4: the
// committed bbolt recording of 3 adds, 3 updateJobRunStateDurableWithin calls
// reserving the added jobs, 1 logReserveNotDurable and 1 setBatchTuning, made
// before Phase 1 removed those three methods from store, replays onto each
// kind of store with the two calls that leave nothing to replay skipped, no
// mismatch, and the 3 jobs reserved.
func TestStoreRecordRemovedMethodsInput(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, kind := range []storeKind{storeKindBolt, storeKindLog} {
		Convey(fmt.Sprintf("The committed removed-methods recording holds its calls and replays onto a %s store",
			storeKindName(kind)), t, func() {
			So(recordedMethods(t, removedMethodsRecording), ShouldResemble, removedMethodsCalls)

			replayTo, _ := openTestStore(t, ctx, kind)

			stats, err := replayRecording(ctx, removedMethodsRecording, replayTo)
			So(err, ShouldBeNil)
			So(stats.calls, ShouldEqual, len(removedMethodsCalls))
			So(stats.skipped, ShouldEqual, 2)
			So(stats.mismatches, ShouldBeEmpty)

			inspector := inspectorOf(replayTo)
			So(inspector.flush(), ShouldBeNil)

			for i := range 3 {
				job := testDBJob(fmt.Sprintf("echo removed methods %d", i), "removed-methods")
				So(storedLiveJobState(t, inspector, job.Key()), ShouldEqual, JobStateReserved)
			}
		})
	}
}

func TestStoreRecordServe(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a manager on a %s store serving with %s set", storeKindName(kind), storeRecordEnv),
			t, func() {
				_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
				serverConfig.storeKind = kind
				path := filepath.Join(t.TempDir(), "serve.rec")

				t.Setenv(storeRecordEnv, path)

				server, _, token, err := serve(ctx, serverConfig)
				So(err, ShouldBeNil)

				t.Setenv(storeRecordEnv, "")

				stopped := false

				defer func() {
					if !stopped {
						server.Stop(ctx, true)
					}
				}()

				jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				recordServedRun(jq, standardReqs)

				server.Stop(ctx, true)

				stopped = true

				Convey("its recording replays against a fresh store with no mismatch", func() {
					methods := recordedMethods(t, path)
					So(methods, ShouldContain, "storeNewJobsGuarded")
					So(methods, ShouldContain, "archiveCompletion")
					So(methods, ShouldContain, "retrieveCompleteJobsByKeys")

					replayTo, _ := openTestStore(t, ctx, kind)

					stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
					So(err, ShouldBeNil)
					So(stats.calls, ShouldEqual, len(methods))
					So(stats.mismatches, ShouldBeEmpty)
				})
			})
	})
}

// recordServedRun adds 3 jobs through jq, runs 2 of them to completion and
// buries the other, then asks for each by key.
func recordServedRun(jq *Client, reqs *jqs.Requirements) {
	jobs := make([]*Job, 3)
	for i := range jobs {
		jobs[i] = &Job{Cmd: fmt.Sprintf("%s record serve %d", restFormTrue, i), Cwd: testCwd,
			RepGroup: "record-serve", ReqGroup: "record-serve", Requirements: reqs}
	}

	inserts, _, err := jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(jobs))

	for i := range jobs {
		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		if i == len(jobs)-1 {
			So(jq.Bury(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed"), ShouldBeNil)

			continue
		}

		So(jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
	}

	for _, job := range jobs {
		got, errg := jq.GetByEssence(&JobEssence{JobKey: job.Key()}, false, false)
		So(errg, ShouldBeNil)
		So(got, ShouldNotBeNil)
	}
}

// writeRecording writes calls, in the order given, as a recording in a new
// temporary file whose path it returns.
func writeRecording(t *testing.T, calls ...recordedCall) string {
	t.Helper()

	var log []byte
	for _, call := range calls {
		log = wal.AppendFrame(log, encodeRecorded(call))
	}

	path := filepath.Join(t.TempDir(), "written.rec")
	So(os.WriteFile(path, log, recordFileMode), ShouldBeNil)

	return path
}

func TestStoreReplayOverlap(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given storeEnv write calls with their %s answer and retrieveRepGroups calls with a "+
			"wrong one", storeKindName(kind)), t, func() {
			env := []byte("written env")
			answerFrom, _ := openTestStore(t, ctx, kind)
			envDigest := storeDigest(answerFrom.storeEnv(env))

			write := func(start, end uint64) recordedCall {
				return recordedCall{Start: start, End: end, Method: "storeEnv", Args: encodeRecorded(recEnv{Env: env}),
					Digest: envDigest, Write: true}
			}
			wrongRead := func(start, end uint64) recordedCall {
				return recordedCall{Start: start, End: end, Method: recordRepGroupsMethod,
					Args: encodeRecorded(recNone{}), Digest: [32]byte{1}}
			}
			replayed := func(calls ...recordedCall) []replayMismatch {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, writeRecording(t, calls...), replayTo)
				So(err, ShouldBeNil)
				So(stats.calls, ShouldEqual, len(calls))

				return stats.mismatches
			}

			Convey("a read inside a write is not a mismatch, but the same read after it is", func() {
				So(replayed(wrongRead(2, 3), write(1, 4), wrongRead(5, 6)), ShouldResemble,
					[]replayMismatch{{Seq: 6, Method: recordRepGroupsMethod}})
			})

			Convey("a read during which a write started is not a mismatch", func() {
				So(replayed(write(2, 3), wrongRead(1, 4)), ShouldBeEmpty)
			})

			Convey("a read after a short write but inside a long write that started before it is not a mismatch",
				func() {
					So(replayed(write(2, 3), wrongRead(5, 6), write(1, 10)), ShouldBeEmpty)
				})

			Convey("a write overlapping no other write is compared like a read", func() {
				wrongWrite := write(1, 2)
				wrongWrite.Digest = [32]byte{1}

				So(replayed(wrongWrite, write(3, 4)), ShouldResemble, []replayMismatch{{Seq: 2, Method: "storeEnv"}})
			})
		})

		Convey(fmt.Sprintf("Given a %s recording of a read by key made while an archive of that key had "+
			"written but not returned", storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "overlap.rec")
			job := testDBJob("echo record overlap", "record-overlap")

			paused := &pausingArchiveStore{store: openStoreIn(ctx, kind, t.TempDir()),
				archived: make(chan struct{}), release: make(chan struct{})}
			s, err := newRecordingStore(ctx, paused, path)
			So(err, ShouldBeNil)

			_, err = s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
			So(err, ShouldBeNil)

			job.State = JobStateRunning
			So(s.updateJobRunStateDurable(job), ShouldBeNil)

			job.State, job.Exited, job.EndTime = JobStateComplete, true, recordTestBase

			archiveErr := make(chan error, 1)

			go func() {
				_, erra := s.archiveCompletion(job.Key(), job)
				archiveErr <- erra
			}()

			So(waitFor(paused.archived), ShouldBeTrue)

			found, err := s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(err, ShouldBeNil)
			So(found, ShouldHaveLength, 1)

			close(paused.release)

			select {
			case err = <-archiveErr:
				So(err, ShouldBeNil)
			case <-time.After(pausingArchiveWait):
				So("archiveCompletion returned", ShouldBeEmpty)
			}

			So(s.close(ctx), ShouldBeNil)

			Convey("replayed, where the read comes before the archive, it is not a mismatch", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, path, replayTo)
				So(err, ShouldBeNil)
				So(stats.calls, ShouldEqual, 4)
				So(stats.mismatches, ShouldBeEmpty)
			})
		})

		Convey(fmt.Sprintf("Given a %s recording of a rep group status with buried keys k1 and k2",
			storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "buried.rec")

			s, err := newRecordingStore(ctx,
				buriedStatusStore{store: openStoreIn(ctx, kind, t.TempDir()), keys: []string{"k1", "k2"}}, path)
			So(err, ShouldBeNil)

			_, err = s.retrieveCompleteJobStatusByRepGroup("record-buried", true)
			So(err, ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)

			replayedWith := func(keys ...string) []replayMismatch {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, errr := replayRecording(ctx, path, buriedStatusStore{store: replayTo, keys: keys})
				So(errr, ShouldBeNil)

				return stats.mismatches
			}

			Convey("replayed against a store giving them as k2 and k1, there is no mismatch", func() {
				So(replayedWith("k2", "k1"), ShouldBeEmpty)
			})

			Convey("replayed against a store giving k1 and k3, the status is a mismatch", func() {
				So(replayedWith("k1", "k3"), ShouldResemble,
					[]replayMismatch{{Seq: 2, Method: "retrieveCompleteJobStatusByRepGroup"}})
			})
		})
	})
}

// pausingArchiveStore is a store whose archiveCompletion, having archived,
// closes archived and waits for release to be closed before it returns.
type pausingArchiveStore struct {
	store

	archived, release chan struct{}
}

func (p *pausingArchiveStore) archiveCompletion(key string, job *Job) (archiveOutcome, error) {
	outcome, err := p.store.archiveCompletion(key, job)
	close(p.archived)

	select {
	case <-p.release:
	case <-time.After(pausingArchiveWait):
	}

	return outcome, err
}

// waitFor reports whether ch was closed within pausingArchiveWait.
func waitFor(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(pausingArchiveWait):
		return false
	}
}

// buriedStatusStore is a store whose retrieveCompleteJobStatusByRepGroup gives
// keys, in that order, as the jobs buried for one reason.
type buriedStatusStore struct {
	store

	keys []string
}

func (b buriedStatusStore) retrieveCompleteJobStatusByRepGroup(repgroup string,
	includeDetails bool) (*RepGroupStatus, error) {
	status, err := b.store.retrieveCompleteJobStatusByRepGroup(repgroup, includeDetails)
	if status != nil {
		status.Buried = map[string][]string{"record-reason": b.keys}
	}

	return status, err
}

func TestStoreReplayLearned(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("Given a %s recording that loaded learned requirements once its refresh interval had "+
			"passed", storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "throttled.rec")
			s := openRecordingStoreIn(t, ctx, kind, path)
			recordLearnedThrottled(ctx, s)
			So(s.close(ctx), ShouldBeNil)

			replayedTo := func(wrap func(store) store) replayStats {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecordingFolding(ctx, path, wrap(replayTo), replayTo)
				So(err, ShouldBeNil)

				return stats
			}

			Convey("replayed at full speed against a fresh store, there is no mismatch", func() {
				So(replayedTo(func(s store) store { return s }).mismatches, ShouldBeEmpty)
			})

			Convey("replayed against a store whose learned requirements are wrong, its reads of them mismatch",
				func() {
					stats := replayedTo(func(s store) store { return wrongLearnedStore{store: s} })
					So(mismatchedMethods(stats), ShouldResemble, []string{recordCachedMethod, recordRefreshMethod})
				})

			Convey("replayed against a store whose learned requirements never change once loaded, its reads of "+
				"them mismatch", func() {
				stats := replayedTo(func(s store) store {
					return &staleLearnedStore{store: s, first: make(map[string]*jqs.Requirements)}
				})
				So(mismatchedMethods(stats), ShouldResemble, []string{recordCachedMethod, recordRefreshMethod})
			})

			Convey("replayed against a store whose cache never reports its learned requirements current, "+
				"cachedReqGroupRecs mismatches", func() {
				stats := replayedTo(func(s store) store { return notCurrentLearnedStore{store: s} })
				So(mismatchedMethods(stats), ShouldResemble, []string{recordCachedMethod})
			})

			Convey("replayed against a store whose cache alone answers wrongly, cachedReqGroupRecs mismatches",
				func() {
					stats := replayedTo(func(s store) store { return wrongCacheOnlyStore{store: s} })
					So(mismatchedMethods(stats), ShouldResemble, []string{recordCachedMethod})
				})

			Convey("replayed against a store whose refreshes alternate between right and wrong answers, its "+
				"refreshReqGroupRecs reads mismatch", func() {
				stats := replayedTo(func(s store) store { return &alternatingLearnedStore{store: s} })
				So(mismatchedMethods(stats), ShouldContain, recordRefreshMethod)
			})

			Convey("replayed against a store whose change stamp never rises, reqStatChanges mismatches", func() {
				stats := replayedTo(func(s store) store { return fixedStampStore{store: s} })
				So(mismatchedMethods(stats), ShouldResemble, []string{"reqStatChanges"})
			})

			Convey("replayed with a fold that fails, whichever fold it is, the replay fails with the fold's error",
				func() {
					folds := replayedFoldErrors(t, ctx, kind, path, 0)
					So(folds, ShouldBeGreaterThan, 1)

					var unreported int

					for n := 1; n <= folds; n++ {
						if replayedFoldErrors(t, ctx, kind, path, n) != n {
							unreported++
						}
					}

					So(unreported, ShouldEqual, 0)
				})
		})

		Convey(fmt.Sprintf("Given a bolt recording that read learned requirements between two archives, replayed "+
			"against a %s store whose folder is paused", storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "last-write.rec")
			recordLearnedReadBetweenArchives(t, ctx, path)

			pauseLogFolder()

			replayTo, _ := openTestStore(t, ctx, kind)

			rp := newReplayer(ctx, replayTo)
			rp.fold = learnedFold(replayTo)
			stats, err := rp.replayFile(path)
			So(err, ShouldBeNil)
			So(stats.mismatches, ShouldBeEmpty)

			Convey("the latest learned value it noted after the last archive is the store's", func() {
				So(inspectorOf(replayTo).flush(), ShouldBeNil)
				So(rp.learned.isLatest(learnedTestReqGroup, replayTo.refreshReqGroupRecs(learnedTestReqGroup)),
					ShouldBeTrue)
			})
		})

		Convey(fmt.Sprintf("Given a %s recording whose ReqGroups were first named by a cache read, a warm and a "+
			"background load", storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "first.rec")
			recordLearnedFirstReads(t, ctx, kind, path)

			Convey("replayed against a fresh store whose loads are slow, there is no mismatch", func() {
				So(replayedWithSlowLoads(t, ctx, kind, path), ShouldBeEmpty)
			})
		})

		Convey(fmt.Sprintf("Given a %s recording whose background load of learned requirements read them between "+
			"two archives", storeKindName(kind)), t, func() {
			path := filepath.Join(t.TempDir(), "between.rec")
			recordLearnedBetweenWrites(t, ctx, kind, path)

			Convey("replayed against a fresh store whose loads are slow, there is no mismatch", func() {
				So(replayedWithSlowLoads(t, ctx, kind, path), ShouldBeEmpty)
			})
		})
	})
}

// replayedWithSlowLoads replays the recording at path against a fresh store of
// the given kind, each of whose loads of learned requirements takes
// learnedTestLoadDelay, and returns the mismatches.
func replayedWithSlowLoads(t *testing.T, ctx context.Context, kind storeKind, path string) []replayMismatch {
	t.Helper()

	replayTo, _ := openTestStore(t, ctx, kind)

	reqStatLoadHook = func(string) error {
		time.Sleep(learnedTestLoadDelay)

		return nil
	}

	Reset(func() { reqStatLoadHook = nil })

	stats, err := replayRecordingFolding(ctx, path, replayTo, replayTo)
	So(err, ShouldBeNil)

	return stats.mismatches
}

// learnedTestJobs returns a new job in learnedTestReqGroup peaking at each of
// peakRAMs.
func learnedTestJobs(peakRAMs ...int) []*Job {
	jobs := make([]*Job, len(peakRAMs))

	for i, peak := range peakRAMs {
		jobs[i] = testDBJob(fmt.Sprintf("echo record learned %d", i), "record-learned-rg")
		jobs[i].ReqGroup = learnedTestReqGroup
		jobs[i].PeakRAM = peak
	}

	return jobs
}

// recordLearnedThrottled sets a refresh interval of learnedTestInterval,
// archives a job, loads its ReqGroup's learned requirements, waits out the
// interval, archives another job of a higher peak RAM and loads them again,
// then reads them from the cache and reads the change stamp.
func recordLearnedThrottled(ctx context.Context, s store) {
	recording, ok := s.(*recordingStore)
	So(ok, ShouldBeTrue)

	inner := recording.s

	s.setReqStatRefreshInterval(learnedTestInterval)

	jobs := learnedTestJobs(100, 300)
	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	foldedRunToCompletion(s, inner, jobs[0], 0)
	So(s.refreshReqGroupRecs(learnedTestReqGroup).RAM, ShouldEqual, 100)

	time.Sleep(learnedTestInterval)

	foldedRunToCompletion(s, inner, jobs[1], 1)
	So(s.refreshReqGroupRecs(learnedTestReqGroup).RAM, ShouldEqual, 300)

	rec, current := s.cachedReqGroupRecs(learnedTestReqGroup)
	So(rec.RAM, ShouldEqual, 300)
	So(current, ShouldBeTrue)
	So(s.reqStatChanges(), ShouldEqual, 3)
}

// recordLearnedBetweenWrites records to path, against a store of the given
// kind: an archive and a load of its ReqGroup's learned requirements, a second
// archive, then a background load (refreshReqGroupRecsLater) that reads them
// only after a third archive, and a fourth archive once it has; then a read of
// the cache, which holds the values that held only between the third and
// fourth archives, and of the change stamp.
func recordLearnedBetweenWrites(t *testing.T, ctx context.Context, kind storeKind, path string) {
	t.Helper()

	inner := openStoreIn(ctx, kind, t.TempDir())
	s, err := newRecordingStore(ctx, inner, path)
	So(err, ShouldBeNil)

	jobs := learnedTestJobs(100, 300, 500, 700)
	_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	foldedRunToCompletion(s, inner, jobs[0], 0)
	So(s.refreshReqGroupRecs(learnedTestReqGroup).RAM, ShouldEqual, 100)
	foldedRunToCompletion(s, inner, jobs[1], 1)

	loading, release := make(chan struct{}), make(chan struct{})
	reqStatLoadHook = func(string) error {
		close(loading)
		waitFor(release)

		return nil
	}

	Reset(func() { reqStatLoadHook = nil })

	s.refreshReqGroupRecsLater(learnedTestReqGroup)
	So(waitFor(loading), ShouldBeTrue)

	reqStatLoadHook = nil

	foldedRunToCompletion(s, inner, jobs[2], 2)
	close(release)
	So(cacheBecomes(inner, learnedTestReqGroup, func(rec *jqs.Requirements, _ bool) bool {
		return rec != nil && rec.RAM == 500
	}), ShouldBeTrue)

	foldedRunToCompletion(s, inner, jobs[3], 3)

	rec, current := s.cachedReqGroupRecs(learnedTestReqGroup)
	So(rec.RAM, ShouldEqual, 500)
	So(current, ShouldBeFalse)
	So(s.reqStatChanges(), ShouldEqual, 3)
	So(s.close(ctx), ShouldBeNil)
}

// recordLearnedFirstReads records to path, against a store of the given kind,
// the first naming of three ReqGroups, each with one job archived: the first by
// a read of the cache before any load; the second by a warm, after which a
// second job is archived and the cache read; and the third by a background
// load, which completes before the cache is read.
func recordLearnedFirstReads(t *testing.T, ctx context.Context, kind storeKind, path string) {
	t.Helper()

	inner := openStoreIn(ctx, kind, t.TempDir())
	s, err := newRecordingStore(ctx, inner, path)
	So(err, ShouldBeNil)

	jobs := learnedTestJobs(100, 300, 100, 100)
	unloaded, warmed, background := learnedTestReqGroup+"-unloaded", learnedTestReqGroup+"-warmed",
		learnedTestReqGroup+"-background"
	jobs[0].ReqGroup, jobs[1].ReqGroup, jobs[2].ReqGroup, jobs[3].ReqGroup = warmed, warmed, unloaded, background

	_, err = s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	foldedRunToCompletion(s, inner, jobs[0], 0)
	foldedRunToCompletion(s, inner, jobs[2], 2)
	foldedRunToCompletion(s, inner, jobs[3], 3)

	rec, current := s.cachedReqGroupRecs(unloaded)
	So(rec, ShouldBeNil)
	So(current, ShouldBeFalse)

	s.warmReqGroupRecs(jobs[1:2])
	foldedRunToCompletion(s, inner, jobs[1], 1)

	rec, current = s.cachedReqGroupRecs(warmed)
	So(rec.RAM, ShouldEqual, 100)
	So(current, ShouldBeFalse)

	s.refreshReqGroupRecsLater(background)

	So(cacheBecomes(inner, background, func(_ *jqs.Requirements, current bool) bool { return current }),
		ShouldBeTrue)

	rec, current = s.cachedReqGroupRecs(background)
	So(rec.RAM, ShouldEqual, 100)
	So(current, ShouldBeTrue)
	So(s.close(ctx), ShouldBeNil)
}

// cacheBecomes reports whether s's cached learned requirements of reqGroup
// became what done accepts within pausingArchiveWait, as a background load
// finishes.
func cacheBecomes(s store, reqGroup string, done func(rec *jqs.Requirements, current bool) bool) bool {
	deadline := time.Now().Add(pausingArchiveWait)

	for time.Now().Before(deadline) {
		if done(s.cachedReqGroupRecs(reqGroup)) {
			return true
		}

		time.Sleep(time.Millisecond)
	}

	return false
}

// wrongLearnedStore is a store whose learned requirements have 1MB more RAM
// than its store's.
type wrongLearnedStore struct {
	store
}

func (w wrongLearnedStore) cachedReqGroupRecs(reqGroup string) (*jqs.Requirements, bool) {
	rec, current := w.store.cachedReqGroupRecs(reqGroup)

	return moreRAM(rec), current
}

func (w wrongLearnedStore) refreshReqGroupRecs(reqGroup string) *jqs.Requirements {
	return moreRAM(w.store.refreshReqGroupRecs(reqGroup))
}

// moreRAM returns a copy of rec with 1MB more RAM; nil for nil.
func moreRAM(rec *jqs.Requirements) *jqs.Requirements {
	if rec == nil {
		return nil
	}

	more := *rec
	more.RAM++

	return &more
}

// wrongCacheOnlyStore is a store whose cachedReqGroupRecs answers with 1MB more
// RAM than its store's, while its refreshReqGroupRecs answers rightly.
type wrongCacheOnlyStore struct {
	store
}

func (w wrongCacheOnlyStore) cachedReqGroupRecs(reqGroup string) (*jqs.Requirements, bool) {
	rec, current := w.store.cachedReqGroupRecs(reqGroup)

	return moreRAM(rec), current
}

// alternatingLearnedStore is a store whose refreshReqGroupRecs answers with 1MB
// more RAM than its store's on every second call, so a refresh can answer with
// a value other than the one the refresh just before it gave.
type alternatingLearnedStore struct {
	store

	calls atomic.Uint64
}

func (a *alternatingLearnedStore) refreshReqGroupRecs(reqGroup string) *jqs.Requirements {
	rec := a.store.refreshReqGroupRecs(reqGroup)
	if a.calls.Add(1)%2 == 0 {
		return moreRAM(rec)
	}

	return rec
}

// staleLearnedStore is a store whose learned requirements of a ReqGroup, once
// its store has given any, stay those first ones, as from a cache that new
// samples never invalidate.
type staleLearnedStore struct {
	store

	first map[string]*jqs.Requirements
}

func (s *staleLearnedStore) cachedReqGroupRecs(reqGroup string) (*jqs.Requirements, bool) {
	rec, current := s.store.cachedReqGroupRecs(reqGroup)

	return s.stale(reqGroup, rec), current
}

func (s *staleLearnedStore) refreshReqGroupRecs(reqGroup string) *jqs.Requirements {
	return s.stale(reqGroup, s.store.refreshReqGroupRecs(reqGroup))
}

// stale returns the first non-nil rec given for reqGroup, rec if none was.
func (s *staleLearnedStore) stale(reqGroup string, rec *jqs.Requirements) *jqs.Requirements {
	if first := s.first[reqGroup]; first != nil {
		return first
	}

	s.first[reqGroup] = rec

	return rec
}

// notCurrentLearnedStore is a store whose cachedReqGroupRecs never reports its
// learned requirements current.
type notCurrentLearnedStore struct {
	store
}

func (n notCurrentLearnedStore) cachedReqGroupRecs(reqGroup string) (*jqs.Requirements, bool) {
	rec, _ := n.store.cachedReqGroupRecs(reqGroup)

	return rec, false
}

// fixedStampStore is a store whose learned-requirements change stamp stays at
// its starting value of 1.
type fixedStampStore struct {
	store
}

func (fixedStampStore) reqStatChanges() uint32 {
	return 1
}

// readdModifiedDependent delays dependent, modifies it to another key with no
// dependencies, which removes bbolt's live record of it, and then adds it
// again, which bbolt stores afresh, since it holds no record under its key to
// keep.
func readdModifiedDependent(ctx context.Context, s store, dependent *Job) *Job {
	changeDependentDurably(ctx, s, dependent)

	modified := testDBJob("echo record dependent modified", "record-dependent")
	So(s.modifyLiveJobs(ctx, []string{dependent.Key()}, []*Job{modified}), ShouldBeNil)

	return addAfresh(ctx, s, dependent)
}

// readdDeletedDependent delays dependent, deletes it, which removes bbolt's
// live record of it, and then adds it again, which bbolt stores afresh, since
// it holds no record under its key to keep.
func readdDeletedDependent(ctx context.Context, s store, dependent *Job) *Job {
	changeDependentDurably(ctx, s, dependent)
	So(s.deleteLiveJobs(ctx, []string{dependent.Key()}), ShouldBeNil)

	return addAfresh(ctx, s, dependent)
}

// addReservedDependentAgain reserves dependent and then adds it again, which
// leaves bbolt's live record of it as it was, since the reservation's run state
// is over it.
func addReservedDependentAgain(ctx context.Context, s store, dependent *Job) *Job {
	dependent.State = JobStateReserved
	So(s.updateJobRunStateDurable(dependent), ShouldBeNil)

	return addDependentAgain(ctx, s, dependent)
}

// addRunStateReservedDependentAgain reserves dependent through
// reserveRunState, waiting for the reservation to be durable, and then adds it
// again, which leaves bbolt's live record of it as it was, since the
// reservation's run state is over it.
func addRunStateReservedDependentAgain(ctx context.Context, s store, dependent *Job) *Job {
	dependent.State = JobStateReserved
	commit, err := s.reserveRunState(dependent)
	So(err, ShouldBeNil)

	done, err := commit.WaitWithin(reserveDurableWait())
	So(err, ShouldBeNil)
	So(done, ShouldBeTrue)

	return addDependentAgain(ctx, s, dependent)
}

// addDelayedDependentAgain delays dependent and then adds it again, which
// leaves bbolt's live record of it as it was, since it is handed out.
func addDelayedDependentAgain(ctx context.Context, s store, dependent *Job) *Job {
	return addDependentAgain(ctx, s, changeDependentDurably(ctx, s, dependent))
}

// readdReservedPutBackDependent reserves dependent, ends its run and puts it
// back to run again, which rewrites bbolt's live record of it so that the
// reservation's run state is no longer over it, and then adds it again, which
// bbolt stores over that record, since it is of a complete job, not one handed
// out.
func readdReservedPutBackDependent(ctx context.Context, s store, dependent *Job) *Job {
	dependent.State = JobStateReserved
	So(s.updateJobRunStateDurable(dependent), ShouldBeNil)

	return addDependentAgain(ctx, s, putBackDependent(ctx, s, dependent))
}

// addDependentAgain adds dependent again in another RepGroup, which bbolt
// stores over its live record.
func addDependentAgain(ctx context.Context, s store, dependent *Job) *Job {
	stored := addAgain(ctx, s, dependent, false)
	So(stored.jobsToQueue, ShouldHaveLength, 1)

	return dependent
}

// putBackDependent ends dependent's run and puts it back to run again.
func putBackDependent(_ context.Context, s store, dependent *Job) *Job {
	dependent.RerunAfterRun = true
	dependent.ReservedBy = uuid.Must(uuid.NewV4())
	dependent.State, dependent.Exited = JobStateComplete, true
	dependent.EndTime = recordTestBase.Add(time.Hour)
	So(s.storeLiveForRerun([]*Job{dependent}), ShouldBeNil)

	return dependent
}

func changeDependentDurably(_ context.Context, s store, dependent *Job) *Job {
	dependent.State, dependent.UntilBuried = JobStateDelayed, 2
	So(s.updateJobAfterChangeDurable(dependent), ShouldBeNil)

	return dependent
}

// addArchivedDependentAgain archives dependent, which keeps it live, and then
// adds it again ignoring complete jobs, which does not store it.
func addArchivedDependentAgain(ctx context.Context, s store, dependent *Job) *Job {
	archiveDependentKeptLive(ctx, s, dependent)

	stored := addAgain(ctx, s, dependent, true)
	So(stored.jobsToQueue, ShouldBeEmpty)
	So(stored.alreadyAdded.Complete, ShouldEqual, 1)

	return dependent
}
