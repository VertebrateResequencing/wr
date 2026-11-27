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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

const (
	// historyNeverRepGroup is a rep group no job is ever added under.
	historyNeverRepGroup = "never"

	// histOutrankingSeq is the seq of a segment's completion that outranks
	// one a fresh store folded into history.bolt.
	histOutrankingSeq = 1000

	// segHideGroup is the rep group of TestHistorySegmentDropsHideBolt's
	// dependent, and segHideOtherGroup the other it is looked up under.
	segHideGroup      = "segHide"
	segHideOtherGroup = "segHideB"
)

const (
	// histSamplesReqGroup is the req group whose samples
	// TestHistorySegmentLearnedSamples puts in both history.bolt and a
	// segment, and histSamplesBothFrom the first of its 300 RAM samples in
	// both, so that its highest are, and counting them twice would move the
	// recommendation.
	histSamplesReqGroup = "q"
	histSamplesBothFrom = 250

	// histSamplesBoltGroup is a req group whose samples only history.bolt
	// holds.
	histSamplesBoltGroup = "boltonly"

	// histSamplesPrefixedGroup is a req group whose name starts with
	// histSamplesReqGroup's, whose samples history.bolt holds after its keys.
	histSamplesPrefixedGroup = "qx"

	// histSamplesRAM is histSamplesReqGroup's learned RAM: the 285th of its
	// 300 RAM samples in value order, their 95th percentile.
	histSamplesRAM = 100 + 7*284
)

// TestHistoryBoltBackupRemap checks that a fold commit growing history.bolt
// past bbolt's default mapping, and a history read after it, do not wait for a
// backup tick's copy of history.bolt, which holds one read transaction for the
// whole copy (spec E4): a remap waits for every open read transaction while
// blocking new reads, so without the headroom history.bolt is mapped with,
// the fold and every history read would stall until the copy ended.
func TestHistoryBoltBackupRemap(t *testing.T) {
	if runnermode || servermode {
		return
	}

	if math.MaxInt < managerMmapMaxSize {
		t.Skip("no headroom is mapped on 32-bit platforms")
	}

	ctx := context.Background()

	Convey("Given a log store in production deployment whose backup tick is held mid-way through copying "+
		"history.bolt", t, func() {
		withManualBackupTicks()

		cfg := incBackupConfig(t, internal.Production)
		s := openIncBackupStore(ctx, cfg)

		info, err := os.Stat(filepath.Join(s.dir, historyBoltFile))
		So(err, ShouldBeNil)

		// bbolt's default mapping of a file this small is at most twice its
		// size, so this value must extend history.bolt past it.
		growValue := make([]byte, 2*info.Size()+1<<20)

		origBytes, origHook := backupCopySyncBytes, backupPaceHook

		Reset(func() { backupCopySyncBytes, backupPaceHook = origBytes, origHook })

		inCopy := make(chan struct{})
		release := make(chan struct{})

		var holdOnce, releaseOnce sync.Once

		releaseCopy := func() { releaseOnce.Do(func() { close(release) }) }

		backupCopySyncBytes = int64(os.Getpagesize())
		backupPaceHook = func() {
			holdOnce.Do(func() {
				close(inCopy)
				<-release
			})
		}

		var tickErr error

		tickDone := make(chan struct{})

		go func() {
			defer close(tickDone)

			tickErr = s.backupTick()
		}()

		Reset(func() {
			releaseCopy()
			<-tickDone
		})

		var copyHeld bool

		select {
		case <-inCopy:
			copyHeld = true
		case <-time.After(remapTestWait):
		}

		So(copyHeld, ShouldBeTrue)

		Convey("a write growing it past its mapping, and a read after it, complete during the copy", func() {
			writeDone := make(chan error, 1)

			go func() {
				writeDone <- s.history.bolt.Update(func(tx *bolt.Tx) error {
					b, errc := tx.CreateBucketIfNotExists(remapTestBucket)
					if errc != nil {
						return errc
					}

					return b.Put([]byte("grow"), growValue)
				})
			}()

			wrote := waitForRemapTestOp(writeDone)

			readDone := make(chan error, 1)

			go func() {
				_, errf := s.history.foldedPos()
				readDone <- errf
			}()

			read := waitForRemapTestOp(readDone)

			releaseCopy()

			So(wrote, ShouldBeTrue)
			So(read, ShouldBeTrue)

			select {
			case <-tickDone:
			case <-time.After(backupTestWarmWait):
			}

			So(tickErr, ShouldBeNil)
		})
	})
}

func TestReadStoreHistoryEntryMissing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped log store whose history key index names a complete job history.bolt has no "+
		"record of", t, func() {
		dir := filepath.Join(t.TempDir(), "store")
		So(openLogStoreIn(ctx, dir).close(ctx), ShouldBeNil)

		history, err := openHistoryBolt(dir, new(codec.BincHandle))
		So(err, ShouldBeNil)

		var k logKey

		k[logKeyLen-1] = 1

		So(history.bolt.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(bucketCompleteSeq).Put([]byte(k.String()), binary.BigEndian.AppendUint64(nil, 1))
		}), ShouldBeNil)
		So(history.close(), ShouldBeNil)

		Convey("ReadStore fails with errStoreCorrupt, calling fn for nothing", func() {
			calls := 0

			So(ReadStore(dir, func(bool, *Job) error {
				calls++

				return nil
			}), ShouldWrap, errStoreCorrupt)
			So(calls, ShouldEqual, 0)
		})
	})
}

func TestStoreArchivedDependentsFromSegments(t *testing.T) {
	if runnermode || servermode {
		return
	}

	archivedDependentsScenarios(t, []archivedDependentsRestart{
		{name: "folded, its history moved into segments and reopened", foldBefore: true,
			reopen: moveHistoryToSegments},
		{name: "folded, checkpointed, its history moved into segments and reopened", foldBefore: true,
			checkpoint: true, reopen: moveHistoryToSegments},
	})
}

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
		pauseLogFolder()

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

func TestLogStoreHistoryFromSegmentsMatchesBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given the same history made in the bbolt store and the log store, the log store's moved from "+
		"history.bolt into segments, its completions in one and its rglookups in the next", t, func() {
		pauseLogFolder()

		h := historyScenario{t0: time.Now().Truncate(time.Second).Add(-time.Hour)}

		bdb, _ := openTestStore(t, ctx, storeKindBolt)
		bolt, ok := bdb.(historyTestStore)
		So(ok, ShouldBeTrue)

		h.stage1(ctx, bolt)
		bolt1 := h.answers(bolt)
		boltDups := h.stage2(ctx, bolt)
		bolt2 := h.answers(bolt)

		s, _ := openTestLogStore(t, ctx)
		h.stage1(ctx, s)
		foldOverlay(s)

		s = moveHistoryToSegments(ctx, s)
		So(historyBoltCompletes(s), ShouldEqual, 0)

		Convey("the log store answers every history query as bbolt does, from the segments alone, and from "+
			"them and the overlay after more completions", func() {
			So(h.answers(s), ShouldResemble, bolt1)
			So(h.stage2(ctx, s), ShouldResemble, boltDups)
			So(h.answers(s), ShouldResemble, bolt2)
			So(h.answers(reopenLogStore(ctx, s)), ShouldResemble, bolt2)
		})
	})
}

// moveHistoryToSegments closes s, moves the completions, rglookups and reverse
// dep group postings its history.bolt holds into two sealed segments, the
// completions in the first, with a drop after each completion whose job's
// dep group dependencies history.bolt holds no postings of (as a modify
// dropped them), and the rglookups in the second, leaving history.bolt holding
// none, and returns the store opened again, closed when the Convey block ends.
// Every posting history.bolt holds must be of a dependency of its key's
// completion's job, which a segment can hold.
func moveHistoryToSegments(ctx context.Context, s *logStore) *logStore {
	So(s.close(ctx), ShouldBeNil)

	history, err := openHistoryBolt(s.dir, s.ch)
	So(err, ShouldBeNil)

	var (
		complete []historyFolded
		lookups  []historyLookup
		postings map[logKey][]string
	)

	So(history.bolt.View(func(tx *bolt.Tx) error {
		complete, lookups, err = boltHistoryOf(tx, s.ch)
		if err == nil {
			postings, err = boltReverseDepsOf(tx)
		}

		return err
	}), ShouldBeNil)

	So(history.bolt.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketJobsComplete, bucketRTK, bucketRGs, bucketEndTimeToKey,
			bucketRGEndTime, bucketCompleteSeq, bucketKeyRGs, bucketKeyRGsPending, bucketRDTK} {
			if errd := tx.DeleteBucket(name); errd != nil {
				return errd
			}

			if _, errc := tx.CreateBucket(name); errc != nil {
				return errc
			}
		}

		return nil
	}), ShouldBeNil)
	So(history.close(), ShouldBeNil)

	w, err := createHistSegment(s.dir, 1, s.ch)
	So(err, ShouldBeNil)

	unheld := 0

	for i := range complete {
		So(w.addCompletion(&complete[i]), ShouldBeNil)

		deps := slices.Compact(slices.Sorted(slices.Values(complete[i].job.Dependencies.DepGroups())))
		held, posted := postings[complete[i].key]

		switch {
		case posted && !slices.Equal(held, deps):
			unheld++
		case !posted && len(deps) > 0:
			w.addDrop(complete[i].key, complete[i].seq+1)
		}

		delete(postings, complete[i].key)
	}

	So(unheld, ShouldEqual, 0)
	So(postings, ShouldBeEmpty)
	So(w.seal(historyPos{}), ShouldBeNil)

	w, err = createHistSegment(s.dir, 2, s.ch)
	So(err, ShouldBeNil)

	for _, l := range lookups {
		So(w.addLookup(l.repGroup, l.key, l.seq), ShouldBeNil)
	}

	So(w.seal(historyPos{}), ShouldBeNil)

	reopened := openLogStoreIn(ctx, s.dir)

	Reset(func() { So(reopened.close(ctx), ShouldBeNil) })

	return reopened
}

// boltHistoryOf returns the completions, each with its completeSeq, and the
// rglookups history.bolt holds in tx.
func boltHistoryOf(tx *bolt.Tx, ch codec.Handle) ([]historyFolded, []historyLookup, error) {
	var complete []historyFolded

	seqs := tx.Bucket(bucketCompleteSeq)

	err := tx.Bucket(bucketJobsComplete).ForEach(func(key, encoded []byte) error {
		k, err := parseLogKey(string(key))
		if err != nil {
			return err
		}

		job, err := decodeStoredJob(encoded, ch)
		complete = append(complete, historyFolded{key: k, seq: binary.BigEndian.Uint64(seqs.Get(key)), job: job})

		return err
	})
	if err != nil {
		return nil, nil, err
	}

	var lookups []historyLookup

	err = tx.Bucket(bucketRTK).ForEach(func(entry, _ []byte) error {
		cut := len(entry) - logKeyHexLen
		k, errp := parseLogKey(string(entry[cut:]))
		lookups = append(lookups, historyLookup{repGroup: string(entry[:cut-len(dbDelimiter)]), key: k, seq: 1})

		return errp
	})

	return complete, lookups, err
}

// boltReverseDepsOf returns, by key, the dep groups, sorted, of the reverse
// dep group postings history.bolt holds in tx.
func boltReverseDepsOf(tx *bolt.Tx) (map[logKey][]string, error) {
	postings := make(map[logKey][]string)

	err := tx.Bucket(bucketRDTK).ForEach(func(posting, _ []byte) error {
		cut := len(posting) - logKeyHexLen
		k, err := parseLogKey(string(posting[cut:]))
		postings[k] = append(postings[k], string(posting[:cut-len(dbDelimiter)]))

		return err
	})

	return postings, err
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
// live and incomplete. Each archived job has its own PeakDisk and CPUtime, and
// c a non-zero Exitcode, Lost and a FailReason, so that every answer depends
// on them.
func (h historyScenario) stage1(ctx context.Context, st historyTestStore) {
	jobs := map[string]*Job{}
	for _, name := range []string{"a", "b", "c", "e"} {
		jobs[name] = newHistoryJob(name, historyTestRepGroup)
	}

	jobs["d"] = newHistoryJob("d", "rg2")

	_, err := st.storeNewJobsGuarded(ctx, []*Job{jobs["a"], jobs["b"], jobs["c"], jobs["d"], jobs["e"]}, false, nil)
	So(err, ShouldBeNil)

	archiveTestJobWith(st, jobs["a"], h.t0, h.t0.Add(10*time.Second), func(job *Job) {
		job.PeakDisk, job.CPUtime = 5, 7*time.Second
	})
	archiveTestJobWith(st, jobs["d"], h.t0.Add(time.Second), h.t0.Add(20*time.Second), func(job *Job) {
		job.PeakDisk, job.CPUtime = 9, 11*time.Second
	})

	lookup := newHistoryJob("c", "rgx")
	So(st.storeRepGroupLookups(ctx, []*Job{lookup}), ShouldBeNil)
	archiveTestJobWith(st, jobs["c"], h.t0.Add(2*time.Second), h.t0.Add(30*time.Second), func(job *Job) {
		job.Exitcode, job.Lost, job.FailReason = 3, true, FailReasonExit
		job.PeakDisk, job.CPUtime = 13, 17*time.Second
	})

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
		group: func(f *archivedJobFacets) (string, bool) {
			return fmt.Sprint(f.State, f.Exitcode, f.Lost, f.FailReason, f.StartTime.Unix(), f.EndTime.Unix()), true
		},
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

func TestHistoryBoltRegroupedEnds(t *testing.T) {
	Convey("Given a key completed under one rep group, then another, history.bolt keeps both end times", t, func() {
		first, second := segRegroupedCompletions()
		So(boltRGEndTimes(t, first, second), ShouldResemble, segRegroupedEnds(first, second))
	})
}

// boltRGEndTimes returns history.bolt's rep group end times after its
// putComplete of each of complete, in order.
func boltRGEndTimes(t *testing.T, complete ...historyFolded) []histGroupEnd {
	t.Helper()

	h, err := openHistoryBolt(t.TempDir(), new(codec.BincHandle))
	So(err, ShouldBeNil)

	defer func() { So(h.close(), ShouldBeNil) }()

	var ends []histGroupEnd

	So(h.bolt.Update(func(tx *bolt.Tx) error {
		for i := range complete {
			if _, err := h.putComplete(tx, &complete[i]); err != nil {
				return err
			}
		}

		return tx.Bucket(bucketRGEndTime).ForEach(func(k, v []byte) error {
			ends = append(ends, histGroupEnd{name: string(k), end: int64(binary.BigEndian.Uint64(v))}) //nolint:gosec

			return nil
		})
	}), ShouldBeNil)

	return ends
}

func TestLogStoreHistoryBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with a job archived and folded into history.bolt", t, func() {
		pauseLogFolder()

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
		pauseLogFolder()

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
				lookups:  []historyLookup{{repGroup: lookupRepGroup, key: k, seq: lookupSeq}},
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

func TestHistorySegmentOutranksBoltRecent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job completed and folded into history.bolt, then completed again in a segment of a "+
		"higher seq, both ending after a cutoff", t, func() {
		pauseLogFolder()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		job := testDBJob("echo outranked", "segO")
		_, err = s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		boltEnd := segTestEnd
		archiveTestJob(s, job, boltEnd.Add(-time.Second), boltEnd)
		foldOverlay(s)
		So(s.close(ctx), ShouldBeNil)

		k := logKeyOf(job)
		segEnd := boltEnd.Add(time.Hour)
		again := historyFolded{key: k, seq: histOutrankingSeq, job: testDBArchivedJob(job.Cmd, "segO", segEnd)}

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			So(w.addCompletion(&again), ShouldBeNil)
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s

		Convey("retrieveCompleteJobsRecent gives it once, as the segment's completion", func() {
			recent, errt := s.retrieveCompleteJobsRecent(boltEnd.Add(-time.Minute))
			So(errt, ShouldBeNil)
			So(jobKeys(recent), ShouldResemble, []string{k.String()})
			So(recent[0].EndTime.Equal(segEnd), ShouldBeTrue)
		})
	})
}

func TestHistorySegmentRemovedReadd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job completed depending on dep group grox, added again depending on grow and removed, an add "+
		"of a new member of grow puts it back, as bbolt keeps the postings of a removed job that is complete", t,
		func() {
			pauseLogFolder()

			dependent := func() *Job {
				d := testDBJob("echo seg regrouped dependent", "segR")
				d.Dependencies = Dependencies{NewDepGroupDependency("grox")}

				return d
			}
			readdAndRemove := func(st store) newMemberPlan {
				again := dependent()
				again.Dependencies = Dependencies{NewDepGroupDependency(archivedDepsGroup)}

				_, errs := st.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
				So(errs, ShouldBeNil)
				So(st.deleteLiveJobs(ctx, []string{again.Key()}), ShouldBeNil)

				return planOfNewMember(ctx, st)
			}

			bdb, _ := openTestStore(t, ctx, storeKindBolt)
			addAndArchive(ctx, bdb, dependent())
			fromBolt := readdAndRemove(bdb)
			So(fromBolt.queued, ShouldContain, dependent().Cmd)

			cfg, _ := logStoreOpenConfig(t)
			s, err := openLogStoreLocked(ctx, cfg)
			So(err, ShouldBeNil)
			So(s.close(ctx), ShouldBeNil)

			done := historyFolded{key: logKeyOf(dependent()), seq: 1, job: dependent()}
			done.job.State, done.job.EndTime = JobStateComplete, segTestEnd

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&done), ShouldBeNil) })

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)
			So(readdAndRemove(result.s), ShouldResemble, fromBolt)
		})
}

func TestHistorySegmentDropsHideBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose history.bolt holds an archived member of grow and an archived dependent of "+
		"grow of segHide, also looked up under segHideB, folded and checkpointed", t, func() {
		pauseLogFolder()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		j := newArchivedDependentsJobs()
		j.d.RepGroup = segHideGroup
		addAndArchive(ctx, s, j.m, j.d)
		So(s.storeRepGroupLookups(ctx, []*Job{testDBJob(j.d.Cmd, segHideOtherGroup)}), ShouldBeNil)
		foldOverlay(s)
		So(s.checkpoint(), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		reopen := func() *logStore {
			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			return result.s
		}

		Convey("an open finds the dependent under segX and puts it back for a new member of grow", func() {
			s = reopen()

			byRG, errr := s.retrieveCompleteJobsByRepGroup(segHideGroup)
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, []string{j.d.Key()})
			So(planOfNewMember(ctx, s).queued, ShouldContain, j.d.Cmd)
		})

		Convey("and a segment holding a modify of the dependent, which drops its rglookups and postings, an "+
			"open finds it under neither, as bbolt's modify deletes them", func() {
			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
				w.addDrop(logKeyOf(j.d), histOutrankingSeq)
			})

			s = reopen()

			byRG, errr := s.retrieveCompleteJobsByRepGroup(segHideGroup)
			So(errr, ShouldBeNil)
			So(byRG, ShouldBeEmpty)

			status, errs := s.retrieveCompleteJobStatusByRepGroup(segHideGroup, false)
			So(errs, ShouldBeNil)
			So(status.Counts[JobStateComplete], ShouldEqual, 0)

			So(planOfNewMember(ctx, s).queued, ShouldNotContain, j.d.Cmd)
		})

		Convey("and a segment holding a modify of the dependent, adds of its command under segHideB and segHide "+
			"find it complete under segHide alone, as a bbolt store's do once its modify deletes the rglookups",
			func() {
				writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
					w.addDrop(logKeyOf(j.d), histOutrankingSeq)
				})

				s = reopen()

				got := segHideAdds(ctx, s, j.d.Cmd)
				So(got, ShouldResemble, segHideBoltAdds(t, ctx, false))
				So(got[0].CompleteSameRepGroup, ShouldEqual, 0)
				So(got[0].OtherRepGroups, ShouldResemble, []DuplicateRepGroup{{RepGroup: segHideGroup, Count: 1}})
			})

		Convey("and a segment holding a modify of the dependent, then a new rglookup of it under segHideB, adds "+
			"of its command find it complete under segHideB again, as a bbolt store's do once it is looked up "+
			"there after its modify", func() {
			k := logKeyOf(j.d)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
				w.addDrop(k, histOutrankingSeq)
				So(w.addLookup(segHideOtherGroup, k, histOutrankingSeq+1), ShouldBeNil)
			})

			s = reopen()

			got := segHideAdds(ctx, s, j.d.Cmd)
			So(got, ShouldResemble, segHideBoltAdds(t, ctx, true))
			So(got[0].CompleteSameRepGroup, ShouldEqual, 1)
			So(got[0].OtherRepGroups, ShouldBeEmpty)
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

	decoded, err := (&historyRead{s: s, overlay: map[logKey]logHistory{k: h}}).overlayJob(h)
	So(err, ShouldBeNil)
	So(s.foldHistory(&historyFold{complete: []historyFolded{{key: k, seq: h.seq, job: decoded}}}), ShouldBeNil)
}

// logKeyOf returns job's key as a logKey.
func logKeyOf(job *Job) logKey {
	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	return k
}

// segHideBoltAdds returns segHideAdds of a bbolt store holding what
// TestHistorySegmentDropsHideBolt's log store holds: an archived member of
// grow and an archived dependent of grow of segHide, looked up under
// segHideB, then put back, modified keeping its key, which deletes its
// rglookups, and archived again, then, if relookup, looked up under segHideB
// again.
func segHideBoltAdds(t *testing.T, ctx context.Context, relookup bool) []DuplicateBreakdown {
	t.Helper()

	st, _ := openTestStore(t, ctx, storeKindBolt)

	j := newArchivedDependentsJobs()
	j.d.RepGroup = segHideGroup
	addAndArchive(ctx, st, j.m, j.d)
	So(st.storeRepGroupLookups(ctx, []*Job{testDBJob(j.d.Cmd, segHideOtherGroup)}), ShouldBeNil)

	j.d.State = JobStateReady
	So(st.storeLiveForRerun([]*Job{j.d}), ShouldBeNil)

	same := modifiedCopy(j.d, j.d.Cmd)
	same.Priority = 7
	So(st.modifyLiveJobs(ctx, []string{j.d.Key()}, []*Job{same}), ShouldBeNil)

	_, err := st.archiveCompletion(same.Key(), same)
	So(err, ShouldBeNil)

	if relookup {
		So(st.storeRepGroupLookups(ctx, []*Job{testDBJob(j.d.Cmd, segHideOtherGroup)}), ShouldBeNil)
	}

	return segHideAdds(ctx, st, j.d.Cmd)
}

// segHideAdds adds cmd to st with ignoreAdded under segHideB, then segHide,
// returning what each found already added, without the end times, which
// differ between stores.
func segHideAdds(ctx context.Context, st modifyLookupStore, cmd string) []DuplicateBreakdown {
	groups := []string{segHideOtherGroup, segHideGroup}
	added := make([]DuplicateBreakdown, 0, len(groups))

	for _, rg := range groups {
		stored, err := st.storeNewJobsGuarded(ctx, []*Job{testDBJob(cmd, rg)}, true, nil)
		So(err, ShouldBeNil)

		for i := range stored.alreadyAdded.OtherRepGroups {
			stored.alreadyAdded.OtherRepGroups[i].LastCompleted = time.Time{}
		}

		added = append(added, stored.alreadyAdded)
	}

	return added
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
	r := &historyRead{s: s, overlay: entries}

	for k, h := range entries {
		job, err := r.overlayJob(h)
		if err != nil {
			return
		}

		f.complete = append(f.complete, historyFolded{key: k, seq: h.seq, job: job})
	}

	_ = s.foldHistory(&f) //nolint:errcheck // a failed fold leaves the overlay as it was
}

// TestLogStoreFastStartupNoHistoryScan is TestReliable2FastStartupNoHistoryScan
// for the log store, whose 250,000-job history is seeded straight into
// history.bolt. The log store loads its history key index from completeSeq at
// open (spec "In-memory state of logStore"), which costs about 0.25µs per
// completed key, so unlike bbolt's its startup grows with the number of
// completed keys, by about 60ms at this scale. What the bbolt test guards
// still holds: startup reads no history record (each is undecodable here)
// and seeds no complete count from one, and it stays well within the absolute
// bound.
func TestLogStoreFastStartupNoHistoryScan(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Log store startup reads no history record, however many completed-only jobs it has", t, func() {
		var small, large time.Duration

		for _, n := range []int{c2SmallHistorySize, c2LargeHistorySize} {
			dir := seedLogStoreHistory(ctx, t, n)

			var fastest time.Duration

			for i := range c2StartupSamples {
				started := time.Now()
				s := openLogStoreIn(ctx, dir)
				elapsed := time.Since(started)

				So(inspectedCount(s, kindComplete), ShouldEqual, n)
				So(s.close(ctx), ShouldBeNil)

				if i == 0 || elapsed < fastest {
					fastest = elapsed
				}
			}

			small, large = large, fastest
		}

		t.Logf("fastest of %d log store startups took %s with %d completed-only jobs and %s with %d",
			c2StartupSamples, small, c2SmallHistorySize, large, c2LargeHistorySize)

		So(large, ShouldBeLessThan, c2AbsoluteStartupLimit)
	})
}

// seedLogStoreHistory returns the directory of a closed log store whose
// history.bolt holds n completed-only jobs, each under its own rep group, as a
// fold of them would leave it, except that each history record is a byte no
// decoder accepts.
func seedLogStoreHistory(ctx context.Context, t *testing.T, n int) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "store")
	So(openLogStoreIn(ctx, dir).close(ctx), ShouldBeNil)

	history, err := openHistoryBolt(dir, new(codec.BincHandle))
	So(err, ShouldBeNil)

	err = history.bolt.Update(func(tx *bolt.Tx) error {
		for i := range n {
			var k logKey

			binary.BigEndian.PutUint64(k[logKeyLen-8:], uint64(i))

			key, rg := k.String(), fmt.Sprintf("%s%08d", c2HistoryRepGroupPrefix, i)

			for _, put := range [][3][]byte{
				{bucketJobsComplete, []byte(key), {0xff}},
				{bucketRTK, []byte(rg + dbDelimiter + key), nil},
				{bucketKeyRGs, []byte(key + dbDelimiter + rg), nil},
				{bucketRGs, []byte(rg), nil},
				{bucketCompleteSeq, []byte(key), binary.BigEndian.AppendUint64(nil, uint64(i+1))},
			} {
				if errp := tx.Bucket(put[0]).Put(put[1], put[2]); errp != nil {
					return errp
				}
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
	So(history.close(), ShouldBeNil)

	return dir
}

func TestHistorySegmentLearnedSamples(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given learned-requirement samples of req groups q, qx and boltonly in history.bolt and of q in a "+
		"segment, some of q's values in both", t, func() {
		boltSamples, segSamples := histLearnedSamples()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(putHistSamples(s.history.bolt, s.reqStats, boltSamples), ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		writeSegment(cfg.dbFile, 1, nil, nil, segSamples, historyPos{})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		s.setRecRounding(1, 1)

		oracle := histSamplesOracle(t, slices.Concat(boltSamples, segSamples))

		Convey("each req group's learned requirements and the sample count are what bbolt gives holding them "+
			"all", func() {
			for _, reqGroup := range []string{histSamplesReqGroup, histSamplesPrefixedGroup, histSamplesBoltGroup} {
				want, errl := loadLearnedReqs(oracle, reqGroup, 1, 1)
				So(errl, ShouldBeNil)
				So(*s.refreshReqGroupRecs(reqGroup), ShouldResemble, *want)
			}

			So(s.refreshReqGroupRecs(histSamplesReqGroup).RAM, ShouldEqual, histSamplesRAM)

			So(learnedReqsOf(s.refreshReqGroupRecs(histSamplesReqGroup)),
				ShouldNotResemble, learnedReqsOf(s.refreshReqGroupRecs(histSamplesBoltGroup)))

			n, errc := s.sampleCount()
			So(errc, ShouldBeNil)
			So(n, ShouldEqual, histOracleSampleCount(oracle))
		})

		Convey("q's samples of each kind, merged from history.bolt and the segment, are each once, in the "+
			"order bbolt's keys of them all sort in, negative ones included", func() {
			wrong := 0

			for kind, bucket := range histSampleBuckets {
				values, errv := s.boltSampleValues(bucket, histSamplesReqGroup)
				So(errv, ShouldBeNil)

				merged := mergeReqStatValues(values, s.segs().sampleValues(histSamplesReqGroup, histSampleKind(kind)))
				if !slices.Equal(merged, histOracleValues(oracle, bucket, histSamplesReqGroup)) {
					wrong++
				}
			}

			So(wrong, ShouldEqual, 0)
		})

		Convey("a sample in history.bolt that is not a number fails q's learned requirements", func() {
			So(s.history.bolt.Update(func(tx *bolt.Tx) error {
				return tx.Bucket(bucketJobSecs).Put(reqStatKey(histSamplesReqGroup, 99), []byte("x"))
			}), ShouldBeNil)

			_, errl := s.loadReqGroupRecs(histSamplesReqGroup)
			So(errl, ShouldNotBeNil)
		})

		Convey("with history.bolt closed under it, adding a segment with a new sample fails, leaving the "+
			"published index as it was, and so does a dependent scan", func() {
			published := s.segs()

			So(s.history.bolt.Close(), ShouldBeNil)

			writeSegment(cfg.dbFile, 2, nil, nil, []histSample{{kind: histSampleRAM,
				reqGroup: histSamplesReqGroup, value: 1}}, historyPos{})
			So(s.addSegment(2), ShouldWrap, berrors.ErrDatabaseNotOpen)
			So(s.segs(), ShouldEqual, published)

			_, errd := s.reverseDepKeys(histSamplesReqGroup)
			So(errd, ShouldWrap, berrors.ErrDatabaseNotOpen)
		})

		Convey("a segment added while it is open whose samples history holds already leaves q's learned "+
			"requirements current", func() {
			s.refreshReqGroupRecs(histSamplesReqGroup)
			s.refreshReqGroupRecs(histSamplesBoltGroup)
			before := s.reqStatChanges()

			writeSegment(cfg.dbFile, 2, nil, nil, []histSample{boltSamples[0], segSamples[0]}, historyPos{})
			So(s.addSegment(2), ShouldBeNil)

			_, current := s.cachedReqGroupRecs(histSamplesReqGroup)
			So(current, ShouldBeTrue)

			Convey("while one with a new sample of q of each kind leaves the index taken before it as it was",
				func() {
					before := s.segs()

					var held [histSampleKinds][]int

					fresh := make([]histSample, 0, histSampleKinds)

					for kind := range histSampleKinds {
						held[kind] = slices.Clone(before.sampleValues(histSamplesReqGroup, kind))
						fresh = append(fresh, histSample{kind: kind, reqGroup: histSamplesReqGroup, value: 1})
					}

					writeSegment(cfg.dbFile, 3, nil, nil, fresh, historyPos{})
					So(s.addSegment(3), ShouldBeNil)

					for kind := range histSampleKinds {
						So(before.sampleValues(histSamplesReqGroup, kind), ShouldResemble, held[kind])
						So(s.segs().sampleValues(histSamplesReqGroup, kind), ShouldHaveLength, len(held[kind])+1)
					}
				})

			Convey("while one with a sample of q and of boltonly whose values history holds only as samples of "+
				"another kind makes their learned requirements stale", func() {
				So(slices.Contains(s.segs().sampleValues(histSamplesReqGroup, histSampleRAM), 107), ShouldBeTrue)

				writeSegment(cfg.dbFile, 3, nil, nil, []histSample{
					{kind: histSampleDisk, reqGroup: histSamplesReqGroup, value: 107},
					{kind: histSampleDisk, reqGroup: histSamplesBoltGroup, value: 50},
				}, historyPos{})
				So(s.addSegment(3), ShouldBeNil)

				_, current = s.cachedReqGroupRecs(histSamplesReqGroup)
				So(current, ShouldBeFalse)

				_, current = s.cachedReqGroupRecs(histSamplesBoltGroup)
				So(current, ShouldBeFalse)
			})

			Convey("while one with a new sample of boltonly makes its learned requirements stale, and a load "+
				"then counts it", func() {
				fresh := histSample{kind: histSampleDisk, reqGroup: histSamplesBoltGroup, value: 50_000}
				writeSegment(cfg.dbFile, 3, nil, nil, []histSample{fresh}, historyPos{})
				So(s.addSegment(3), ShouldBeNil)

				_, current = s.cachedReqGroupRecs(histSamplesBoltGroup)
				So(current, ShouldBeFalse)

				_, current = s.cachedReqGroupRecs(histSamplesReqGroup)
				So(current, ShouldBeTrue)

				oracle = histSamplesOracle(t, slices.Concat(boltSamples, segSamples, []histSample{fresh}))
				want, errl := loadLearnedReqs(oracle, histSamplesBoltGroup, 1, 1)
				So(errl, ShouldBeNil)
				So(want.Disk, ShouldBeGreaterThan, 0)
				So(*s.refreshReqGroupRecs(histSamplesBoltGroup), ShouldResemble, *want)
				So(s.reqStatChanges(), ShouldBeGreaterThan, before)
			})
		})
	})
}

// histLearnedSamples returns TestHistorySegmentLearnedSamples's samples for
// history.bolt and for a segment: of q, RAM samples alternating between them,
// the highest in both, enough that the recommendation is not the highest; disk
// samples, a 0 among them, in both; and time samples in the segment alone, but
// for negative ones, as a clock stepped back during a job records: one in both,
// and one in the segment wider than the rest, whose key sorts after theirs (see
// reqStatKey); and of boltonly, RAM samples in history.bolt alone, 50 among
// them; and of qx, RAM samples in history.bolt alone, higher than any of q's.
func histLearnedSamples() (inBolt, inSegment []histSample) {
	add := func(to *[]histSample, kind histSampleKind, reqGroup string, value int) {
		*to = append(*to, histSample{kind: kind, reqGroup: reqGroup, value: value})
	}

	for i := range 300 {
		switch {
		case i >= histSamplesBothFrom:
			add(&inBolt, histSampleRAM, histSamplesReqGroup, 100+i*7)
			add(&inSegment, histSampleRAM, histSamplesReqGroup, 100+i*7)
		case i%2 == 0:
			add(&inBolt, histSampleRAM, histSamplesReqGroup, 100+i*7)
		default:
			add(&inSegment, histSampleRAM, histSamplesReqGroup, 100+i*7)
		}
	}

	for i, v := range []int{0, 5000, 70, 900, 12000} {
		add(&inBolt, histSampleDisk, histSamplesReqGroup, v+i%2)
		add(&inSegment, histSampleDisk, histSamplesReqGroup, v)
	}

	add(&inBolt, histSampleSecs, histSamplesReqGroup, -7)
	add(&inSegment, histSampleSecs, histSamplesReqGroup, -7)
	add(&inSegment, histSampleSecs, histSamplesReqGroup, -1000)

	for i := range 40 {
		add(&inSegment, histSampleSecs, histSamplesReqGroup, 30*(i+1))
		add(&inBolt, histSampleRAM, histSamplesBoltGroup, 50*(i+1))
		add(&inBolt, histSampleRAM, histSamplesPrefixedGroup, 1_000_000+i)
	}

	return inBolt, inSegment
}

// histSamplesOracle returns a bbolt database holding samples in its sample
// buckets, closed when the Convey block ends.
func histSamplesOracle(t *testing.T, samples []histSample) *bolt.DB {
	t.Helper()

	bdb, err := bolt.Open(filepath.Join(t.TempDir(), "oracle.db"), dbFilePermission, nil)
	So(err, ShouldBeNil)

	Reset(func() { So(bdb.Close(), ShouldBeNil) })

	So(bdb.Update(func(tx *bolt.Tx) error {
		for _, bucket := range histSampleBuckets {
			if _, errc := tx.CreateBucket(bucket); errc != nil {
				return errc
			}
		}

		return nil
	}), ShouldBeNil)
	So(putHistSamples(bdb, newReqStatCache(), samples), ShouldBeNil)

	return bdb
}

// putHistSamples puts samples in bdb's sample buckets, as a fold does.
func putHistSamples(bdb *bolt.DB, stats *reqStatCache, samples []histSample) error {
	return bdb.Update(func(tx *bolt.Tx) error {
		for _, sample := range samples {
			if err := stats.putStat(tx, histSampleBuckets[sample.kind], sample.reqGroup, sample.value); err != nil {
				return err
			}
		}

		return nil
	})
}

// histOracleSampleCount returns how many samples bdb's sample buckets hold.
func histOracleSampleCount(bdb *bolt.DB) int {
	n := 0

	So(bdb.View(func(tx *bolt.Tx) error {
		for _, bucket := range histSampleBuckets {
			n += tx.Bucket(bucket).Stats().KeyN
		}

		return nil
	}), ShouldBeNil)

	return n
}

// histOracleValues returns, in key order, the values of reqGroup's samples
// in bdb's bucket.
func histOracleValues(bdb *bolt.DB, bucket []byte, reqGroup string) []int {
	var values []int

	prefix := []byte(reqGroup + dbDelimiter)

	So(bdb.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucket).Cursor()

		for k, v := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, v = c.Next() {
			val, err := strconv.Atoi(string(v))
			if err != nil {
				return err
			}

			values = append(values, val)
		}

		return nil
	}), ShouldBeNil)

	return values
}

// historyJobSummary is the parts of an archived job the stores must agree on.
func historyJobSummary(job *Job) string {
	return fmt.Sprintf("%s %s %s %s %d %d %d %d", job.Key(), job.Cmd, job.RepGroup, job.State,
		job.StartTime.UnixNano(), job.EndTime.UnixNano(), job.PeakRAM, job.Exitcode)
}
