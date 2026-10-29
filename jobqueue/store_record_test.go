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
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// storeReplayEnv names the env var holding the recording TestStoreReplay
	// replays.
	storeReplayEnv = "WR_STORE_REPLAY"

	// storeRecordRegenEnv names the env var that makes TestStoreRecordRegen
	// rewrite removedMethodsRecording.
	storeRecordRegenEnv = "WR_STORE_RECORD_REGEN"

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

// TestStoreReplay replays the recording named by WR_STORE_REPLAY against a
// fresh store of each kind WR_TEST_STORE selects. It is the A5 gate's runner,
// and skips when WR_STORE_REPLAY is unset.
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

			stats, err := replayRecording(ctx, path, replayTo)
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

				stats, err := replayRecording(ctx, path, replayTo)
				So(err, ShouldBeNil)
				So(stats.calls, ShouldEqual, len(methods))
				So(stats.skipped, ShouldEqual, 0)
				So(stats.mismatches, ShouldBeEmpty)
			})

			Convey("replayed against a store whose retrieveCompleteJobsByKeys drops its last result, "+
				"the first mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, path, dropLastCompleteStore{store: replayTo})
				So(err, ShouldBeNil)
				So(stats.mismatches, ShouldNotBeEmpty)
				So(stats.mismatches[0].Method, ShouldEqual, "retrieveCompleteJobsByKeys")
			})

			Convey("replayed against a store asking a dependency question the recording has no answer to, "+
				"the first mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, path, unaskedQuestions()[0].asking(replayTo))
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

					stats, err := replayRecording(ctx, path, question.asking(replayTo))
					So(err, ShouldBeNil)

					got = append(got, mismatchedMethods(stats))
					want = append(want, []string{question.method})
				}

				So(got, ShouldResemble, want)
			})

			Convey("replayed against a store whose archiveCompletion reports writing nothing, "+
				"the mismatches are those calls", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, path, notWrittenArchiveStore{store: replayTo})
				So(err, ShouldBeNil)
				So(mismatchedMethods(stats), ShouldResemble, []string{"archiveCompletion"})
			})

			Convey("replayed against a store whose prepareJobChange returns no handle, "+
				"the first mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, path, noChangeAheadStore{store: replayTo})
				So(err, ShouldBeNil)
				So(stats.mismatches, ShouldNotBeEmpty)
				So(stats.mismatches[0].Method, ShouldEqual, "prepareJobChange")
			})

			Convey("replayed against a store whose retrieveRepGroups also returns an error, "+
				"the mismatch is that call", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				stats, err := replayRecording(ctx, path, boomRepGroupsStore{store: replayTo})
				So(err, ShouldBeNil)
				So(mismatchedMethods(stats), ShouldResemble, []string{recordRepGroupsMethod})
			})

			Convey("replayed against a store giving its unordered results in reverse, there is no mismatch",
				func() {
					replayTo, _ := openTestStore(t, ctx, kind)

					stats, err := replayRecording(ctx, path, reversingStore{store: replayTo})
					So(err, ShouldBeNil)
					So(stats.mismatches, ShouldBeEmpty)
				})

			Convey("replayed, the replay no longer holds the jobs it saw archived or deleted", func() {
				replayTo, _ := openTestStore(t, ctx, kind)

				rp := newReplayer(ctx, replayTo)
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

				stats, err := replayRecording(ctx, path, replayTo)
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
	recordWorkload(ctx, s)
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
// dependencies of two jobs.
//

func recordWorkload(ctx context.Context, s store) {
	_, _, err := s.recoverIncompleteJobs()
	So(err, ShouldBeNil)
	So(s.upgradedOnOpen(), ShouldBeFalse)

	s.setRecRounding(1, 100)
	s.setBatchTuning(time.Millisecond, 100)
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

	recordChangesAhead(ctx, s, b)
	recordHistoryReads(ctx, s, a)
	recordGuardedAdd(ctx, s, b[30], b[31])
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
	job.Host = "record-host"
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
	So(s.updateJobRunStateDurableWithin(b[41], time.Minute), ShouldBeNil)
	s.logReserveNotDurable(b[41].Key())
}

// recordChangesAhead kicks buried b[20] through prepareJobChange and
// queueJobChangeAhead, prepares and discards a kick of b[21], and moves a change
// prepared for b[42] onto a job replacing it.
func recordChangesAhead(ctx context.Context, s store, b []*Job) {
	ahead := s.prepareJobChange(ctx, b[20], recordKickChange)
	So(ahead, ShouldNotBeNil)
	s.queueJobChangeAhead(ctx, b[20], ahead, recordKickChange)

	discarded := s.prepareJobChange(ctx, b[21], recordKickChange)
	So(discarded, ShouldNotBeNil)
	s.discardJobChangeAhead(b[21], discarded)

	replacement := testDBJob("echo record b 42 replacement", recordTestRepGroupB)
	moving := s.prepareJobChange(ctx, b[42], recordKickChange)
	moved := s.moveJobChangeAhead(b[42], replacement, moving)
	So(moved, ShouldNotBeNil)
	s.queueJobChangeAhead(ctx, replacement, moved, recordKickChange)
}

// recordGuardedAdd adds, under a rerun guard, a job depending on parent's two
// dep groups whose own dep group live dependent depends on, archives dependent
// while the guard is held, which keeps it live to run again, then resolves the
// dependencies of the added job and dependent.
func recordGuardedAdd(ctx context.Context, s store, parent, dependent *Job) {
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

	file, err := os.Open(path)
	So(err, ShouldBeNil)

	defer file.Close()

	var methods []string

	_, err = wal.Scan(file, func(_ wal.Ref, rec []byte) error {
		var call recordedCall
		if errd := decodeRecorded(rec, &call); errd != nil {
			return errd
		}

		methods = append(methods, call.Method)

		return nil
	})
	So(err, ShouldBeNil)

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

const (
	// recordNeverAsked is the question an askingStore asks.
	recordNeverAsked = "record-never-asked"

	// recordRepGroupsMethod is the recorded method name of retrieveRepGroups.
	recordRepGroupsMethod = "retrieveRepGroups"
)

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

// errBoom is the error boomRepGroupsStore adds.
var errBoom = errors.New("boom")

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

// noChangeAheadStore is a store whose prepareJobChange discards the handle it
// prepared and returns none: a store that answers a handle where the recorded
// one gave none.
type noChangeAheadStore struct {
	store
}

func (n noChangeAheadStore) prepareJobChange(ctx context.Context, job *Job, change func(*Job)) *jobChangeAhead {
	n.discardJobChangeAhead(job, n.store.prepareJobChange(ctx, job, change))

	return nil
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
// handle its own job's prepareJobChange returned.
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
	h.queued[job.Key()] = ahead != nil && ahead == h.prepared[job.Key()]
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

// TestStoreRecordRegen rewrites removedMethodsRecording against bbolt, when
// WR_STORE_RECORD_REGEN is set. Phase 1 removes the methods it calls.
func TestStoreRecordRegen(t *testing.T) {
	if os.Getenv(storeRecordRegenEnv) == "" || runnermode || servermode {
		t.Skipf("%s is not set", storeRecordRegenEnv)
	}

	ctx := context.Background()

	Convey("The removed-methods recording is rewritten against bbolt", t, func() {
		path := filepath.Join(t.TempDir(), "removed_methods.rec")

		s := openRecordingStoreIn(t, ctx, storeKindBolt, path)
		recordRemovedMethodCalls(ctx, s)
		So(s.close(ctx), ShouldBeNil)

		So(recordedMethods(t, path), ShouldResemble, removedMethodsCalls)

		content, err := os.ReadFile(path)
		So(err, ShouldBeNil)
		So(os.MkdirAll(filepath.Dir(removedMethodsRecording), 0o755), ShouldBeNil)
		So(os.WriteFile(removedMethodsRecording, content, 0o644), ShouldBeNil) //nolint:gosec
	})
}

// recordRemovedMethodCalls makes the calls removedMethodsRecording holds: 3
// adds, 3 updateJobRunStateDurableWithin calls reserving the added jobs, 1
// logReserveNotDurable and 1 setBatchTuning. It returns the added jobs.
func recordRemovedMethodCalls(ctx context.Context, s store) []*Job {
	jobs := make([]*Job, 3)

	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo removed methods %d", i), "removed-methods")
		_, err := s.storeNewJobsGuarded(ctx, []*Job{jobs[i]}, false, nil)
		So(err, ShouldBeNil)
	}

	for _, job := range jobs {
		job.State = JobStateReserved
		So(s.updateJobRunStateDurableWithin(job, time.Minute), ShouldBeNil)
	}

	s.logReserveNotDurable(jobs[0].Key())
	s.setBatchTuning(time.Millisecond, 100)

	return jobs
}

func TestStoreRecordRemovedMethodsInput(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey(fmt.Sprintf("The committed removed-methods recording holds its calls and replays onto a %s store",
			storeKindName(kind)), t, func() {
			So(recordedMethods(t, removedMethodsRecording), ShouldResemble, removedMethodsCalls)

			replayTo, _ := openTestStore(t, ctx, kind)

			stats, err := replayRecording(ctx, removedMethodsRecording, replayTo)
			So(err, ShouldBeNil)
			So(stats.calls, ShouldEqual, len(removedMethodsCalls))
			So(stats.mismatches, ShouldBeEmpty)

			inspector := inspectorOf(replayTo)
			So(inspector.flush(), ShouldBeNil)

			for i := range 3 {
				job := testDBJob(fmt.Sprintf("echo removed methods %d", i), "removed-methods")
				So(storedLiveJobState(t, inspector, job.Key()), ShouldEqual, JobStateReserved)
			}
		})
	})
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

					stats, err := replayRecording(ctx, path, replayTo)
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

// pausingArchiveWait is the longest a pausingArchiveStore or its test waits
// for the other: far longer than one archive takes.
const pausingArchiveWait = 10 * time.Second

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

				stats, err := replayRecording(ctx, path, wrap(replayTo))
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

	stats, err := replayRecording(ctx, path, replayTo)
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
	s.setReqStatRefreshInterval(learnedTestInterval)

	jobs := learnedTestJobs(100, 300)
	_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
	So(err, ShouldBeNil)

	recordRunToCompletion(s, jobs[0], 0)
	So(s.refreshReqGroupRecs(learnedTestReqGroup).RAM, ShouldEqual, 100)

	time.Sleep(learnedTestInterval)

	recordRunToCompletion(s, jobs[1], 1)
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

	recordRunToCompletion(s, jobs[0], 0)
	So(s.refreshReqGroupRecs(learnedTestReqGroup).RAM, ShouldEqual, 100)
	recordRunToCompletion(s, jobs[1], 1)

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

	recordRunToCompletion(s, jobs[2], 2)
	close(release)
	So(cacheBecomes(inner, learnedTestReqGroup, func(rec *jqs.Requirements, _ bool) bool {
		return rec != nil && rec.RAM == 500
	}), ShouldBeTrue)

	recordRunToCompletion(s, jobs[3], 3)

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

	recordRunToCompletion(s, jobs[0], 0)
	recordRunToCompletion(s, jobs[2], 2)
	recordRunToCompletion(s, jobs[3], 3)

	rec, current := s.cachedReqGroupRecs(unloaded)
	So(rec, ShouldBeNil)
	So(current, ShouldBeFalse)

	s.warmReqGroupRecs(jobs[1:2])
	recordRunToCompletion(s, jobs[1], 1)

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
