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

// Tests of the log store's history.bolt and overlay (spec "Folder and
// history", Phase 1) beyond D1's acceptance tests: history queries answer as
// the bbolt store's do however much of history is folded, and history.bolt
// survives reopening. Phase 2 deletes history.bolt and the bbolt store, so this
// file is in boltOnlyTests.

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
)

// historyNeverRepGroup is a rep group no job is ever added under.
const historyNeverRepGroup = "never"

// historyTestStore is what the history tests ask of a store.
type historyTestStore interface {
	historyQuerier
	historyArchiver
	storeNewJobsGuarded(ctx context.Context, jobs []*Job, ignoreAdded bool,
		guard *rerunGuard) (storedNewJobs, error)
	storeRepGroupLookups(ctx context.Context, jobs []*Job) error
	spendArchivedBytesByRepGroup(repgroup string, budget *archivedBytesBudget) error
}

func TestLogStoreHistoryMatchesBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given the same history made in the bbolt store and the log store", t, func() {
		h := historyScenario{t0: time.Now().Truncate(time.Second).Add(-time.Hour)}

		bdb, _ := openTestStore(t, ctx, storeKindBolt)
		bolt, ok := bdb.(historyTestStore)
		So(ok, ShouldBeTrue)

		h.stage1(ctx, bolt)
		bolt1 := h.answers(bolt)
		boltDups := h.stage2(ctx, bolt)
		bolt2 := h.answers(bolt)

		So(bolt2.Recent, ShouldNotResemble, bolt1.Recent)
		So(bolt1.ByRG[historyTestRepGroup], ShouldHaveLength, 2)
		So(bolt2.Last[historyTestRepGroup].After(bolt1.Last[historyTestRepGroup]), ShouldBeTrue)
		So(boltDups.Complete, ShouldEqual, 2)
		So(boltDups.CompleteSameRepGroup, ShouldEqual, 1)
		So(boltDups.OtherRepGroups, ShouldHaveLength, 1)
		So(bolt2.Last["rg2"].Before(bolt2.Last["rgz"]), ShouldBeTrue)

		s, _ := openTestLogStore(t, ctx)
		h.stage1(ctx, s)

		Convey("the log store answers every history query as bbolt does with nothing folded", func() {
			So(h.answers(s), ShouldResemble, bolt1)
			So(h.stage2(ctx, s), ShouldResemble, boltDups)
			So(h.answers(s), ShouldResemble, bolt2)
			So(h.answers(reopenLogStore(ctx, s)), ShouldResemble, bolt2)
		})

		Convey("it answers as bbolt does with everything folded, then more completions in the overlay", func() {
			foldOverlay(s)
			So(overlayEmpty(s), ShouldBeTrue)
			So(h.answers(s), ShouldResemble, bolt1)

			s = reopenLogStore(ctx, s)
			entries, lookups := overlaySize(s)
			So(entries, ShouldEqual, 0)
			So(lookups, ShouldEqual, 0)
			So(h.answers(s), ShouldResemble, bolt1)

			So(h.stage2(ctx, s), ShouldResemble, boltDups)
			entries, _ = overlaySize(s)
			So(entries, ShouldEqual, 2)
			So(h.answers(s), ShouldResemble, bolt2)
			So(h.answers(reopenLogStore(ctx, s)), ShouldResemble, bolt2)
		})

		Convey("it answers as bbolt does with stage 1 folded and stage 2 folded too", func() {
			foldOverlay(s)
			So(h.stage2(ctx, s), ShouldResemble, boltDups)
			foldOverlay(s)
			So(overlayEmpty(s), ShouldBeTrue)
			So(h.answers(s), ShouldResemble, bolt2)
			So(h.answers(reopenLogStore(ctx, s)), ShouldResemble, bolt2)
		})
	})
}

// overlayEmpty reports whether s's overlay holds no history entry, no
// rglookup, by rep group or by key, and no rep group's end time.
func overlayEmpty(s *logStore) bool {
	entries, lookups := overlaySize(s)

	s.mu.RLock()
	ends, byKey := len(s.state.overlayEnds), len(s.state.keyRepGroups)
	s.mu.RUnlock()

	return entries == 0 && lookups == 0 && byKey == 0 && ends == 0
}

// overlaySize returns how many history entries and rglookups s's overlay holds.
func overlaySize(s *logStore) (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lookups := 0
	for _, keys := range s.state.repGroupKeys {
		lookups += len(keys)
	}

	return len(s.state.overlay), lookups
}

// historyAnswers is every history query's answer about the scenario, with
// jobs and summaries as strings, so that the stores' answers compare.
type historyAnswers struct {
	Complete  map[string]bool
	ByKeys    []string
	ByRG      map[string][]string
	Oldest    map[string][]string
	Counted   map[string]map[string]int
	Fetched   map[string]map[string]int
	Recent    map[string][]string
	Last      map[string]time.Time
	Status    map[string]string
	Details   map[string]string
	Has       map[string]bool
	RepGroups []string
	Priced    map[string]int
	TooBig    map[string]bool
}

// historyScenario makes the same history in any store, in two stages, its
// jobs starting from t0.
type historyScenario struct {
	t0 time.Time
}

// stage1 adds jobs a, b, c and e in "rg" and d in "rg2", archives a and d,
// looks c up under "rgx" (a queued job added again under another rep group)
// and archives it, and archives b kept live (marked to run again). e stays
// live and incomplete.
func (h historyScenario) stage1(ctx context.Context, st historyTestStore) {
	jobs := map[string]*Job{}
	for _, name := range []string{"a", "b", "c", "e"} {
		jobs[name] = newHistoryJob(name, historyTestRepGroup)
	}

	jobs["d"] = newHistoryJob("d", "rg2")

	_, err := st.storeNewJobsGuarded(ctx, []*Job{jobs["a"], jobs["b"], jobs["c"], jobs["d"], jobs["e"]}, false, nil)
	So(err, ShouldBeNil)

	archiveTestJob(st, jobs["a"], h.t0, h.t0.Add(10*time.Second))
	archiveTestJob(st, jobs["d"], h.t0.Add(time.Second), h.t0.Add(20*time.Second))

	lookup := newHistoryJob("c", "rgx")
	So(st.storeRepGroupLookups(ctx, []*Job{lookup}), ShouldBeNil)
	archiveTestJob(st, jobs["c"], h.t0.Add(2*time.Second), h.t0.Add(30*time.Second))

	jobs["b"].RerunAfterRun = true
	archiveTestJob(st, jobs["b"], h.t0.Add(3*time.Second), h.t0.Add(40*time.Second))
}

// stage2 adds a again and archives it with a later EndTime, and adds d again
// under "rgy" and c again under "rgx", ignoring jobs already complete,
// returning that add's breakdown of duplicates: d completed elsewhere, and c
// under the rep group it was looked up under. Then it adds d again under
// "rgz" and archives it, so its history entry moves to "rgz".
func (h historyScenario) stage2(ctx context.Context, st historyTestStore) DuplicateBreakdown {
	again := newHistoryJob("a", historyTestRepGroup)
	_, err := st.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
	So(err, ShouldBeNil)

	archiveTestJob(st, again, h.t0.Add(45*time.Second), h.t0.Add(50*time.Second))

	stored, err := st.storeNewJobsGuarded(ctx, []*Job{newHistoryJob("d", "rgy"), newHistoryJob("c", "rgx")}, true, nil)
	So(err, ShouldBeNil)
	So(stored.jobsToQueue, ShouldBeEmpty)

	moved := newHistoryJob("d", "rgz")
	_, err = st.storeNewJobsGuarded(ctx, []*Job{moved}, false, nil)
	So(err, ShouldBeNil)

	archiveTestJob(st, moved, h.t0.Add(55*time.Second), h.t0.Add(60*time.Second))

	return stored.alreadyAdded
}

// answers asks st every history query about the scenario.
func (h historyScenario) answers(st historyTestStore) historyAnswers {
	rgs := []string{historyTestRepGroup, "rg2", "rgx", "rgy", "rgz", historyNeverRepGroup}
	keys := make([]string, 0, 6)
	keys = append(keys, "0123456789abcdef0123456789abcdef")

	for _, name := range []string{"a", "b", "c", "d", "e"} {
		keys = append(keys, newHistoryJob(name, "").Key())
	}

	a := historyAnswers{
		Complete: map[string]bool{}, ByRG: map[string][]string{}, Oldest: map[string][]string{},
		Counted: map[string]map[string]int{}, Fetched: map[string]map[string]int{},
		Recent: map[string][]string{}, Status: map[string]string{}, Details: map[string]string{},
		Has: map[string]bool{}, Priced: map[string]int{}, TooBig: map[string]bool{},
	}

	for _, key := range keys {
		complete, err := st.checkIfComplete(key)
		So(err, ShouldBeNil)

		a.Complete[key] = complete
	}

	byKeys, err := st.retrieveCompleteJobsByKeys(keys)
	So(err, ShouldBeNil)

	a.ByKeys = historyJobSummaries(byKeys)

	for _, rg := range rgs {
		h.repGroupAnswers(st, rg, &a)
	}

	for name, cutoff := range map[string]time.Time{"all": {}, "after a's first": h.t0.Add(15 * time.Second)} {
		recent, errr := st.retrieveCompleteJobsRecent(cutoff)
		So(errr, ShouldBeNil)

		a.Recent[name] = historyJobSummaries(recent)
	}

	a.Last, err = st.retrieveLastCompletionTimeByRepGroup(rgs)
	So(err, ShouldBeNil)

	a.RepGroups, err = st.retrieveRepGroups()
	So(err, ShouldBeNil)

	return a
}

// newHistoryJob returns a new job of the scenario's, named name, in repGroup.
func newHistoryJob(name, repGroup string) *Job {
	return testDBJob("echo history "+name, repGroup)
}

// historyJobSummaries is historyJobSummary of each of jobs.
func historyJobSummaries(jobs []*Job) []string {
	summaries := make([]string, len(jobs))
	for i, job := range jobs {
		summaries[i] = historyJobSummary(job)
	}

	return summaries
}

// repGroupAnswers adds to a the answers of the history queries of rg.
func (h historyScenario) repGroupAnswers(st historyTestStore, rg string, a *historyAnswers) {
	byRG, err := st.retrieveCompleteJobsByRepGroup(rg)
	So(err, ShouldBeNil)

	a.ByRG[rg] = historyJobSummaries(byRG)

	page, err := st.retrieveOldestCompleteJobsByRepGroup(rg, completeJobsQuery{
		group: func(f *archivedJobFacets) (string, bool) { return fmt.Sprint(f.State), true },
		limit: func(string) int { return 2 },
	})
	So(err, ShouldBeNil)

	a.Oldest[rg], a.Counted[rg], a.Fetched[rg] = historyJobSummaries(page.jobs), page.counted, page.fetched

	for details, into := range map[bool]map[string]string{false: a.Status, true: a.Details} {
		status, errs := st.retrieveCompleteJobStatusByRepGroup(rg, details)
		So(errs, ShouldBeNil)

		into[rg] = historyStatusSummary(status)
	}

	a.Has[rg] = st.repGroupHasHistory(rg)

	budget := newArchivedBytesBudget()
	So(st.spendArchivedBytesByRepGroup(rg, budget), ShouldBeNil)
	a.Priced[rg] = budget.priced

	tight := &archivedBytesBudget{remaining: 1}
	err = st.spendArchivedBytesByRepGroup(rg, tight)
	a.TooBig[rg] = err != nil

	if err != nil {
		So(err, ShouldWrap, ErrArchivedHistoryTooBig)
	}
}

// historyStatusSummary is a RepGroupStatus as a string, its times as instants.
func historyStatusSummary(status *RepGroupStatus) string {
	return fmt.Sprintf("%v %+v %+v %+v %+v %d %d", status.Counts, status.Memory, status.Disk, status.Walltime,
		status.CPUtime, status.StartTime.UnixNano(), status.EndTime.UnixNano())
}

func TestLogStoreHistoryBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job archived and folded into history.bolt", t, func() {
		s, dir := openTestLogStore(t, ctx)

		job := newHistoryJob("folded", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		end := time.Now().Truncate(time.Second).Add(-time.Minute)
		archiveTestJob(s, job, end.Add(-time.Minute), end)
		foldOverlay(s)

		Convey("its complete record survives the deletion of the wal files below the folded position, and a later "+
			"completion outranks it", func() {
			sealed, errr := s.w.Rotate()
			So(errr, ShouldBeNil)
			foldOverlay(s)
			So(s.close(ctx), ShouldBeNil)

			files, errw := walFileNumbers(dir)
			So(errw, ShouldBeNil)
			So(files, ShouldContain, sealed)

			for _, n := range files {
				if n <= sealed {
					So(os.Remove(walFilePath(dir, n)), ShouldBeNil)
				}
			}

			reopened := openLogStoreIn(ctx, dir)

			Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

			checkArchivedAnswers(reopened, job, end.Add(-time.Hour), end)
			So(inspectedCount(reopened, kindComplete), ShouldEqual, 1)

			again := newHistoryJob("folded", historyTestRepGroup)
			_, err = reopened.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
			So(err, ShouldBeNil)

			later := end.Add(time.Second)
			archiveTestJob(reopened, again, later.Add(-time.Minute), later)

			got, found, errc := reopened.completeJob(job.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(got.EndTime.Equal(later), ShouldBeTrue)

			checkArchivedAnswers(reopenLogStore(ctx, reopened), job, end.Add(-time.Hour), later)
		})

		Convey("folding the same completion again, or an older one, changes nothing", func() {
			older := newHistoryJob("folded", historyTestRepGroup)
			older.State = JobStateComplete
			older.EndTime = end.Add(-time.Hour)

			So(s.foldHistory(&historyFold{complete: []historyFolded{{key: logKeyOf(job), seq: 1, job: older}}}),
				ShouldBeNil)
			foldOverlay(s)

			check := func(st *logStore) {
				checkArchivedAnswers(st, job, end.Add(-time.Hour), end)

				got, found, errc := st.completeJob(job.Key())
				So(errc, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(got.EndTime.Equal(end), ShouldBeTrue)
			}

			check(s)
			check(reopenLogStore(ctx, s))
		})

		Convey("folding an older completion while a newer one is in the overlay leaves the newer answering", func() {
			k := logKeyOf(job)

			again := newHistoryJob("folded", historyTestRepGroup)
			_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
			So(err, ShouldBeNil)

			later := end.Add(time.Minute)
			archiveTestJob(s, again, later.Add(-time.Second), later)

			s.mu.RLock()
			newer := *s.state.overlay[k]
			s.mu.RUnlock()

			older := newHistoryJob("folded", historyTestRepGroup)
			older.State, older.Exited, older.EndTime = JobStateComplete, true, end

			So(s.foldHistory(&historyFold{complete: []historyFolded{{key: k, seq: newer.seq - 1, job: older}}}),
				ShouldBeNil)

			got, found, errc := s.completeJob(job.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(got.EndTime.Equal(later), ShouldBeTrue)
		})

		Convey("a job that ended exactly at the cutoff is recent, folded or in the overlay", func() {
			edge := newHistoryJob("edge", historyTestRepGroup)
			_, err = s.storeNewJobsGuarded(ctx, []*Job{edge}, false, nil)
			So(err, ShouldBeNil)

			edgeEnd := end.Add(time.Hour)
			archiveTestJob(s, edge, edgeEnd.Add(-time.Second), edgeEnd)

			recentKeys := func(cutoff time.Time) []string {
				recent, errr := s.retrieveCompleteJobsRecent(cutoff)
				So(errr, ShouldBeNil)

				return jobKeys(recent)
			}

			So(recentKeys(end), ShouldResemble, jobKeys([]*Job{job, edge}))
			So(recentKeys(edgeEnd), ShouldResemble, []string{edge.Key()})

			foldOverlay(s)
			So(overlayEmpty(s), ShouldBeTrue)
			So(recentKeys(edgeEnd), ShouldResemble, []string{edge.Key()})
		})

		Convey("a rep group's last completion time is its latest EndTime, whatever order its completions are "+
			"archived and folded in", func() {
			x, y := newHistoryJob("ends x", "ends"), newHistoryJob("ends y", "ends")
			_, err = s.storeNewJobsGuarded(ctx, []*Job{x, y}, false, nil)
			So(err, ShouldBeNil)

			archiveTestJob(s, x, end, end.Add(2*time.Second))
			archiveTestJob(s, y, end, end.Add(time.Second))

			last := func() time.Time {
				times, errt := s.retrieveLastCompletionTimeByRepGroup([]string{"ends"})
				So(errt, ShouldBeNil)

				return times["ends"]
			}

			So(last(), ShouldEqual, end.Add(2*time.Second).UTC())

			foldOnly(s, y)
			So(last(), ShouldEqual, end.Add(2*time.Second).UTC())

			foldOverlay(s)
			So(overlayEmpty(s), ShouldBeTrue)
			So(last(), ShouldEqual, end.Add(2*time.Second).UTC())
		})

		Convey("a completion right after the folded position is not taken as folded on reopen", func() {
			next := newHistoryJob("after fold", historyTestRepGroup)
			_, err = s.storeNewJobsGuarded(ctx, []*Job{next}, false, nil)
			So(err, ShouldBeNil)

			foldOverlay(s)
			archiveTestJob(s, next, end, end.Add(time.Second))

			reopened := reopenLogStore(ctx, s)

			entries, lookups := overlaySize(reopened)
			So(entries, ShouldEqual, 1)
			So(lookups, ShouldEqual, 0)

			byRG, errr := reopened.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, slices.Sorted(slices.Values(jobKeys([]*Job{job, next}))))
		})

		Convey("a folded job depending on a dep group is put back live when a member of it is added", func() {
			dependent := newHistoryJob("dependent", historyTestRepGroup)
			dependent.Dependencies = Dependencies{NewDepGroupDependency("g")}
			_, err = s.storeNewJobsGuarded(ctx, []*Job{dependent}, false, nil)
			So(err, ShouldBeNil)

			archiveTestJob(s, dependent, end, end)
			foldOverlay(s)

			reopened := reopenLogStore(ctx, s)

			member := newHistoryJob("member", historyTestRepGroup)
			member.DepGroups = []string{"g"}

			stored, erra := reopened.storeNewJobsGuarded(ctx, []*Job{member}, false, nil)
			So(erra, ShouldBeNil)
			So(jobKeys(stored.jobsToQueue), ShouldResemble, []string{dependent.Key(), member.Key()})
			So(stored.jobsToQueue[0].Cmd, ShouldEqual, dependent.Cmd)

			live, errl := reopened.checkIfLive(dependent.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeTrue)
		})

		Convey("a completion kept live and folded keeps its std cleared after reopen", func() {
			kept := newHistoryJob("kept", historyTestRepGroup)
			_, err = s.storeNewJobsGuarded(ctx, []*Job{kept}, false, nil)
			So(err, ShouldBeNil)

			kept.State = JobStateBuried
			kept.Exited = true
			kept.Exitcode = 1
			So(s.updateJobAfterExitDurable(kept, []byte("o"), []byte("e"), false), ShouldBeNil)

			kept.RerunAfterRun = true
			archiveTestJob(s, kept, end, end.Add(time.Second))
			foldOverlay(s)

			reopened := reopenLogStore(ctx, s)
			stdo, stde := reopened.retrieveJobStd(ctx, kept.Key())
			So(stdo, ShouldBeNil)
			So(stde, ShouldBeNil)
			So(inspectedCount(reopened, kindStdOut), ShouldEqual, 0)

			entries, _ := overlaySize(reopened)
			So(entries, ShouldEqual, 0)
		})
	})
}

func TestLogStoreHistoryFoldBeforeApply(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job's complete, delete and rglookup records, which the folder may fold, reading the log up "+
		"to its durable end, before their done funcs apply them", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := newHistoryJob("early fold", historyTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		const lookupRepGroup = "early lookup"

		k := logKeyOf(job)
		end := time.Now().Truncate(time.Second)
		job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end, end

		// a wal file after any the store has written, so that the ops' Refs
		// are after every real record.
		const file = 1 << 20

		completeSeq, deleteSeq, lookupSeq := s.nextSeq(), s.nextSeq(), s.nextSeq()
		ops := []logOp{
			{typ: logRecComplete, seq: completeSeq, key: k, ref: wal.Ref{File: file, Off: 100}, endTime: end},
			{typ: logRecDelete, seq: deleteSeq, key: k, ref: wal.Ref{File: file, Off: 200}},
			{typ: logRecRGLookup, seq: lookupSeq, key: k, name: lookupRepGroup, ref: wal.Ref{File: file, Off: 300}},
		}

		// overlaid reports whether the overlay holds k's entry, its rglookup
		// or its rep group's end time.
		overlaid := func() bool {
			s.mu.RLock()
			defer s.mu.RUnlock()

			_, entry := s.state.overlay[k]
			_, lookup := s.state.repGroupKeys[lookupRepGroup][k]
			_, ends := s.state.overlayEnds[historyTestRepGroup]

			return entry || lookup || ends
		}

		fold := func(to historyPos) {
			So(s.foldHistory(&historyFold{
				complete: []historyFolded{{key: k, seq: completeSeq, job: job}},
				lookups:  []historyLookup{{repGroup: lookupRepGroup, key: k}},
				to:       to,
			}), ShouldBeNil)
		}

		Convey("a fold past them leaves nothing of theirs in the overlay once they apply, and the folded "+
			"completion answers", func() {
			fold(historyPos{file: file, off: 400})
			s.apply(ops)

			So(overlaid(), ShouldBeFalse)

			complete, errc := s.checkIfComplete(job.Key())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeTrue)

			got, found, errc := s.completeJob(job.Key())
			So(errc, ShouldBeNil)
			So(found, ShouldBeTrue)
			So(got.EndTime.Equal(end), ShouldBeTrue)

			byRG, errr := s.retrieveCompleteJobsByRepGroup(lookupRepGroup)
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, []string{job.Key()})
		})

		Convey("a fold ending at their start does not fold them, so their apply puts them in the overlay", func() {
			So(s.foldHistory(&historyFold{to: historyPos{file: file, off: 100}}), ShouldBeNil)
			s.apply(ops)

			s.mu.RLock()
			_, entry := s.state.overlay[k]
			_, lookup := s.state.repGroupKeys[lookupRepGroup][k]
			s.mu.RUnlock()

			So(entry, ShouldBeTrue)
			So(lookup, ShouldBeTrue)
		})

		Convey("a fold ending in a later wal file covers them, as after a roll or a reopen", func() {
			fold(historyPos{file: file + 1})
			s.apply(ops)

			So(overlaid(), ShouldBeFalse)
		})

		Convey("a fold of an older completion after a newer one applied keeps the newer's seq, so a middle "+
			"one's apply does not replace it", func() {
			newer := ops[0]
			newer.seq, newer.ref.Off = s.nextSeq()+10, 500
			middle := ops[0]
			middle.seq, middle.ref.Off = newer.seq-1, 600
			s.apply([]logOp{newer})
			fold(historyPos{file: file, off: 150})
			s.apply([]logOp{middle})

			s.mu.RLock()
			h := s.state.overlay[k]
			s.mu.RUnlock()

			So(h, ShouldNotBeNil)
			So(h.seq, ShouldEqual, newer.seq)
		})

		Convey("an earlier fold after a later one does not move the folded position back", func() {
			fold(historyPos{file: file, off: 400})
			fold(historyPos{file: file, off: 50})
			fold(historyPos{})
			s.apply(ops)

			So(overlaid(), ShouldBeFalse)
		})
	})
}

// foldOnly folds job's overlay entry alone into history.bolt, leaving the
// folded position at the log's start.
func foldOnly(s *logStore, job *Job) {
	k := logKeyOf(job)

	s.mu.RLock()
	h := *s.state.overlay[k]
	s.mu.RUnlock()

	decoded, err := s.decodeJob(h.spec, h.complete, logRecComplete)
	So(err, ShouldBeNil)
	So(s.foldHistory(&historyFold{complete: []historyFolded{{key: k, seq: h.seq, job: decoded}}}), ShouldBeNil)
}

// logKeyOf returns job's key as a logKey.
func logKeyOf(job *Job) logKey {
	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	return k
}

func TestLogStoreHistoryConcurrentFolds(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store archiving jobs while folds run and queries read", t, func() {
		s, _ := openTestLogStore(t, ctx)

		const n = 60

		jobs := testLogJobs("concurrent fold", historyTestRepGroup, n)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		var (
			archived atomic.Int64
			missed   atomic.Int64
			wg       sync.WaitGroup
		)

		done := make(chan struct{})
		end := time.Now().Truncate(time.Second)

		wg.Go(func() {
			defer close(done)

			for _, job := range jobs {
				job.Lock()
				job.State, job.Exited, job.StartTime, job.EndTime = JobStateComplete, true, end, end
				job.Unlock()

				if _, errc := s.archiveCompletion(job.Key(), job); errc != nil {
					missed.Add(1)
				}

				archived.Add(1)
			}
		})

		wg.Go(func() {
			// each fold ends at a later offset in wal file 0, before every
			// real record (wal files start at 1), so it folds no record by
			// position yet moves the folded position while archives apply.
			var off uint64

			for {
				select {
				case <-done:
					return
				default:
				}

				off++
				foldConcurrently(s, historyPos{off: off})
			}
		})

		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}

				want := archived.Load()

				byRG, errr := s.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
				status, errs := s.retrieveCompleteJobStatusByRepGroup(historyTestRepGroup, false)

				if errr != nil || errs != nil || int64(len(byRG)) < want || int64(status.Counts[JobStateComplete]) < want {
					missed.Add(1)
				}
			}
		})

		wg.Wait()

		Convey("no query misses a completion archived before it began, while its fold moves it", func() {
			So(missed.Load(), ShouldEqual, 0)

			byRG, errr := s.retrieveCompleteJobsByRepGroup(historyTestRepGroup)
			So(errr, ShouldBeNil)
			So(byRG, ShouldHaveLength, n)
		})
	})
}

// foldConcurrently folds the completions of s's overlay into history.bolt
// while archives are being made, without asserting, for use off the test's
// goroutine. Its folded position is to, which must be before every real
// record, so that a reopen would not skip a record it did not fold.
func foldConcurrently(s *logStore, to historyPos) {
	s.mu.RLock()
	entries := make(map[logKey]logHistory, len(s.state.overlay))

	for k, h := range s.state.overlay {
		entries[k] = *h
	}

	s.mu.RUnlock()

	f := historyFold{to: to}

	for k, h := range entries {
		job, err := s.decodeJob(h.spec, h.complete, logRecComplete)
		if err != nil {
			return
		}

		f.complete = append(f.complete, historyFolded{key: k, seq: h.seq, job: job})
	}

	_ = s.foldHistory(&f) //nolint:errcheck // a failed fold leaves the overlay as it was
}

// historyJobSummary is the parts of an archived job the stores must agree on.
func historyJobSummary(job *Job) string {
	return fmt.Sprintf("%s %s %s %s %d %d %d %d", job.Key(), job.Cmd, job.RepGroup, job.State,
		job.StartTime.UnixNano(), job.EndTime.UnixNano(), job.PeakRAM, job.Exitcode)
}
