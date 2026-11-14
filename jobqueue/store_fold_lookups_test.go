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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestStoreFoldTxLimit(t *testing.T) {
	Convey("foldTxLimit is foldTxBytes up to a lag of foldLagStep, then that many times lag/foldLagStep",
		t, func() {
			So(foldTxLimit(0), ShouldEqual, foldTxBytes)
			So(foldTxLimit(foldLagStep), ShouldEqual, foldTxBytes)
			So(foldTxLimit(2*foldLagStep), ShouldEqual, 2*foldTxBytes)
			So(foldTxLimit(12*foldLagStep), ShouldEqual, 12*foldTxBytes)
		})

	Convey("foldTxLimit does not grow for a portal burst's lag of up to about 50s (G5 grew it there and failed "+
		"its burst lines), and has doubled by a lag of 2 minutes", t, func() {
		So(foldTxLimit(55*time.Second), ShouldEqual, foldTxBytes)
		So(foldTxLimit(2*time.Minute), ShouldEqual, 2*foldTxBytes)
	})
}

func TestStoreFoldLookupsInBoundedTransactions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with its folder paused and an add of 1,000 jobs in rep group a unfolded", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold lookups", "a", 1000)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		before, err := s.history.foldedPos()
		So(err, ShouldBeNil)

		txs, items := s.foldStats.txs.Load(), s.foldStats.items.Load()

		Convey("a fold commits them in one transaction, as they write less than foldTxBytes", func() {
			So(s.foldNow(), ShouldBeNil)
			So(s.foldStats.txs.Load()-txs, ShouldEqual, 1)
			So(historyLookupCount(s, "a"), ShouldEqual, 1000)
		})

		Convey("with the bound at 1 byte, a fold commits them 64 a transaction, observing each of the 1,000 "+
			"records once, and history.bolt then holds each rglookup once", func() {
			setFoldTxBytes(1)

			So(s.foldNow(), ShouldBeNil)
			So(s.foldStats.txs.Load()-txs, ShouldEqual, 16)
			So(s.foldStats.items.Load()-items, ShouldEqual, 1000)
			So(historyLookupCount(s, "a"), ShouldEqual, 1000)
		})

		Convey("once folded, a fold of an add of 1,000 more in rep group a, with the bound at 32 pages, "+
			"splits by its estimated size, not by every 64", func() {
			So(s.foldNow(), ShouldBeNil)

			_, err = s.storeNewJobsGuarded(ctx, testLogJobs("fold lookups more", "a", 1000), false, nil)
			So(err, ShouldBeNil)

			setFoldTxBytes(int64(32 * s.history.bolt.Info().PageSize))

			txs = s.foldStats.txs.Load()

			So(s.foldNow(), ShouldBeNil)
			So(s.foldStats.txs.Load()-txs, ShouldBeBetween, 1, 16)
			So(historyLookupCount(s, "a"), ShouldEqual, 2000)
		})

		Convey("with the bound at 1 byte, a fold whose 4th transaction fails, after the 3 before it put 192 "+
			"rglookups, leaves the folded position, and the rep groups ever added, as they were, and the "+
			"next fold holds each rglookup once", func() {
			setFoldTxBytes(1)

			calls := 0
			fail := func() error {
				calls++
				if calls == 4 {
					return errIncTestCrash
				}

				return nil
			}
			logFoldTxHook.Store(&fail)

			Reset(func() { logFoldTxHook.Store(nil) })

			So(s.foldNow(), ShouldWrap, errIncTestCrash)
			So(historyLookupCount(s, "a"), ShouldEqual, 3*foldLookupBatch)

			after, errp := s.history.foldedPos()
			So(errp, ShouldBeNil)
			So(after, ShouldResemble, before)
			So(historyRepGroups(s), ShouldBeEmpty)
			So(retrievedRepGroups(s), ShouldResemble, []string{"a"})

			logFoldTxHook.Store(nil)

			So(s.foldNow(), ShouldBeNil)
			So(historyLookupCount(s, "a"), ShouldEqual, 1000)
			So(historyRepGroups(s), ShouldResemble, []string{"a"})
		})

		Convey("once folded, a modify of the 1,000 into rep group b, keys unchanged, folded with the bound "+
			"at 1 byte, leaves their rglookups under b alone", func() {
			So(s.foldNow(), ShouldBeNil)

			keys := make([]string, len(jobs))

			for i, job := range jobs {
				keys[i] = job.Key()
				job.RepGroup = "b"
			}

			So(s.modifyLiveJobs(ctx, keys, jobs), ShouldBeNil)

			setFoldTxBytes(1)

			So(s.foldNow(), ShouldBeNil)

			So(historyLookupCount(s, "a"), ShouldEqual, 0)
			So(historyLookupCount(s, "b"), ShouldEqual, 1000)
		})
	})
}

// historyLookupCount returns how many rglookups of repGroup s's history.bolt
// holds.
func historyLookupCount(s *logStore, repGroup string) int {
	n := 0
	prefix := []byte(repGroup + dbDelimiter)

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketRTK).Cursor()

		for k, _ := cursor.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
			n++
		}

		return nil
	})
	So(err, ShouldBeNil)

	return n
}

func TestStoreFoldCompletionsInBoundedTransactions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store with an add of 100 jobs in rep group a folded, then the folder paused and the "+
		"100 archived, unfolded", t, func() {
		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold completions", "a", 100)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		pauseLogFolder()
		archiveIncTestJobs(s, jobs)

		before, err := s.history.foldedPos()
		So(err, ShouldBeNil)

		txs, items := s.foldStats.txs.Load(), s.foldStats.items.Load()

		Convey("a fold records them in one transaction with the default bound", func() {
			So(s.foldNow(), ShouldBeNil)
			So(s.foldStats.txs.Load()-txs, ShouldEqual, 1)
			So(historyCompleteCount(s), ShouldEqual, 100)
		})

		Convey("with the bound at 1 byte, a fold records them 16 a transaction, observing each record once, "+
			"and history.bolt then holds each, with the highest seq", func() {
			setFoldTxBytes(1)

			So(s.foldNow(), ShouldBeNil)
			So(s.foldStats.txs.Load()-txs, ShouldEqual, 7)
			So(s.foldStats.items.Load()-items, ShouldEqual, 100)
			So(historyCompleteCount(s), ShouldEqual, 100)
			So(historyMaxSeqOf(s), ShouldEqual, maxCompleteSeq(s, jobs))
		})

		Convey("with the bound at 1 byte, a fold whose 3rd transaction fails, after the 2 before it "+
			"recorded 32, leaves the folded position and highest seq as they were, every completion "+
			"queryable, and the next fold records each once", func() {
			setFoldTxBytes(1)

			seqBefore := historyMaxSeqOf(s)

			calls := 0
			fail := func() error {
				calls++
				if calls == 3 {
					return errIncTestCrash
				}

				return nil
			}
			logFoldTxHook.Store(&fail)

			Reset(func() { logFoldTxHook.Store(nil) })

			So(s.foldNow(), ShouldWrap, errIncTestCrash)
			So(historyCompleteCount(s), ShouldEqual, 2*foldTxMinCompletions)

			after, errp := s.history.foldedPos()
			So(errp, ShouldBeNil)
			So(after, ShouldResemble, before)
			So(historyMaxSeqOf(s), ShouldEqual, seqBefore)

			got, errr := s.retrieveCompleteJobsByRepGroup("a")
			So(errr, ShouldBeNil)
			So(got, ShouldHaveLength, 100)

			logFoldTxHook.Store(nil)

			So(s.foldNow(), ShouldBeNil)
			So(historyCompleteCount(s), ShouldEqual, 100)
			So(historyMaxSeqOf(s), ShouldEqual, maxCompleteSeq(s, jobs))

			got, errr = s.retrieveCompleteJobsByRepGroup("a")
			So(errr, ShouldBeNil)
			So(got, ShouldHaveLength, 100)
		})
	})
}

func TestStoreFoldTransactionsWriteAboutTheBound(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, cmdBytes := range []int{100, 8 * 1024, 40 * 1024} {
		given := fmt.Sprintf("Given a log store with 1,000 jobs of %d byte commands archived and folded, then "+
			"1 more, then its folder paused and 1,000 more archived, unfolded, and the bound at 2MiB", cmdBytes)

		Convey(given, t, func() {
			s, _ := openTestLogStore(t, ctx)

			addArchived := func(prefix string) {
				jobs := bigCmdJobs(prefix, 1000, cmdBytes)
				_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
				So(err, ShouldBeNil)
				So(s.foldNow(), ShouldBeNil)
				archiveIncTestJobs(s, jobs)
			}

			addArchived("fold size base")
			So(s.foldNow(), ShouldBeNil)

			one := bigCmdJobs("fold size one", 1, cmdBytes)
			_, err := s.storeNewJobsGuarded(ctx, one, false, nil)
			So(err, ShouldBeNil)
			archiveIncTestJobs(s, one)
			So(s.foldNow(), ShouldBeNil)

			pauseLogFolder()
			addArchived("fold size")

			const bound = 2 << 20

			setFoldTxBytes(bound)

			Convey("a fold, after a fold of one, commits them in transactions each writing, but for its "+
				"freelist, at most 1.5 times the bound (the first, on the last fold's scale, twice), and all "+
				"but the last at least a third of it", func() {
				allocs := foldTxPageAllocs(s)

				So(len(allocs), ShouldBeGreaterThan, 1)

				for i, alloc := range allocs {
					if i == 0 {
						So(alloc, ShouldBeLessThanOrEqualTo, bound*2)
					} else {
						So(alloc, ShouldBeLessThanOrEqualTo, bound*3/2)
					}

					if i < len(allocs)-1 {
						So(alloc, ShouldBeGreaterThanOrEqualTo, bound/3)
					}
				}

				So(historyCompleteCount(s), ShouldEqual, 2001)
			})
		})
	}
}

func TestStoreFoldBacklogCapacity(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	// newBacklog opens a log store with 2,000 jobs of 8KiB commands added,
	// folded and archived, its folder paused before the archive, and returns
	// it with the archive unfolded: 2,000 completions, about 40MB to write.
	newBacklog := func() *logStore {
		s, _ := openTestLogStore(t, ctx)
		jobs := bigCmdJobs("fold capacity", 2000, 8*1024)

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		pauseLogFolder()
		archiveIncTestJobs(s, jobs)

		return s
	}

	Convey("Given a log store with 2,000 completions of jobs with 8KiB commands unfolded", t, func() {
		Convey("a fold whose records have waited 20 foldLagSteps commits them in at most 2 transactions, "+
			"as with the bound off, writing at most 1.25 times as much; one whose records have just been "+
			"written splits them by the bound", func() {
			off := newBacklog()

			setFoldTxBytes(1 << 40)

			offAllocs := foldTxPageAllocs(off)

			lagged := newBacklog()

			setFoldTxBytes(0)

			lagged.foldLagFrom = time.Now().Add(-20 * foldLagStep)

			lagAllocs := foldTxPageAllocs(lagged)
			So(lagged.foldStats.maxLag.Load(), ShouldBeGreaterThanOrEqualTo, uint64(20*foldLagStep))

			fresh := newBacklog()
			freshAllocs := foldTxPageAllocs(fresh)

			So(len(offAllocs), ShouldEqual, 1)
			So(len(lagAllocs), ShouldBeLessThanOrEqualTo, 2)
			So(sumInt64(lagAllocs), ShouldBeLessThanOrEqualTo, sumInt64(offAllocs)*5/4)
			So(len(freshAllocs), ShouldBeGreaterThan, 4)

			Convey("and once the lagged fold reached its target, the next fold's lag is since it began", func() {
				lagged.foldStats.maxLag.Store(0)

				_, err := lagged.storeNewJobsGuarded(ctx, testLogJobs("fold capacity next", "a", 10), false, nil)
				So(err, ShouldBeNil)
				So(lagged.foldNow(), ShouldBeNil)
				So(lagged.foldStats.maxLag.Load(), ShouldBeLessThan, uint64(foldLagStep))
			})
		})
	})

	Convey("Given a log store with an add of 200 jobs unfolded, folded with the bound at 1 byte and each "+
		"transaction taking 100ms", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)
		So(s.foldNow(), ShouldBeNil)

		_, err := s.storeNewJobsGuarded(ctx, testLogJobs("fold slow", "a", 200), false, nil)
		So(err, ShouldBeNil)

		setFoldTxBytes(1)

		slow := func() error { //nolint:unparam // logFoldTxHook's type
			time.Sleep(100 * time.Millisecond)

			return nil
		}

		logFoldTxHook.Store(&slow)

		Reset(func() { logFoldTxHook.Store(nil) })

		began := time.Now()

		So(s.foldNow(), ShouldBeNil)

		took := time.Since(began)

		logFoldTxHook.Store(nil)

		Convey("the next fold's maxLag is at least that slow fold's duration, as its records may have been "+
			"written as the slow fold began", func() {
			s.foldStats.maxLag.Store(0)

			_, err = s.storeNewJobsGuarded(ctx, testLogJobs("fold slow next", "a", 10), false, nil)
			So(err, ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)
			So(s.foldStats.maxLag.Load(), ShouldBeGreaterThanOrEqualTo, uint64(took)) //nolint:gosec // a duration
		})
	})
}

// bigCmdJobs returns n jobs in rep group a, each with a command of about
// cmdBytes bytes.
func bigCmdJobs(prefix string, n, cmdBytes int) []*Job {
	jobs := make([]*Job, n)
	pad := strings.Repeat("x", cmdBytes)

	for i := range jobs {
		jobs[i] = testDBJob(fmt.Sprintf("echo %s %d %s", prefix, i, pad), "a")
	}

	return jobs
}

func TestStoreFoldLargeFreelist(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	// newFreelisted opens a log store whose history.bolt carries a freelist of
	// about 320KiB, with 2,000 jobs added and folded, then archived unfolded.
	newFreelisted := func() *logStore {
		s, _ := openTestLogStore(t, ctx)
		growHistoryFreelist(s)

		jobs := testLogJobs("fold freelist", "a", 2000)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		pauseLogFolder()
		archiveIncTestJobs(s, jobs)

		return s
	}

	Convey("Given log stores whose history.bolt has a freelist over 4 times a 64KiB bound, and 2,000 "+
		"completions unfolded", t, func() {
		Convey("a fold with the bound writes, freelists included, at most 3.5 times what one with it off "+
			"does, in transactions each writing at least half the freelist of its own", func() {
			off := newFreelisted()

			setFoldTxBytes(1 << 40)

			offAllocs := foldTxPageAllocs(off)

			on := newFreelisted()

			pageSize := on.history.bolt.Info().PageSize
			freelist := int64(on.history.freelistWrite(pageSize))
			So(freelist, ShouldBeGreaterThan, 4*64<<10)

			setFoldTxBytes(64 << 10)

			onAllocs := foldTxPageAllocs(on)

			So(sumInt64(onAllocs)+int64(len(onAllocs))*freelist, ShouldBeLessThanOrEqualTo,
				(sumInt64(offAllocs)+freelist)*7/2)

			for _, alloc := range onAllocs[:len(onAllocs)-1] {
				So(alloc, ShouldBeGreaterThanOrEqualTo, freelist/2)
			}
		})
	})
}

// growHistoryFreelist puts 160 values of 1MiB in s's history.bolt's
// repgroups, then deletes them, leaving their 41,000 or so pages free, so that
// each commit writes a freelist of about 320KiB.
func growHistoryFreelist(s *logStore) {
	value := make([]byte, 1<<20)
	keys := make([][]byte, 160)

	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("freelist %03d", i))
	}

	So(s.history.bolt.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRGs)

		for _, k := range keys {
			if err := b.Put(k, value); err != nil {
				return err
			}
		}

		return nil
	}), ShouldBeNil)

	So(s.history.bolt.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRGs)

		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}

		return nil
	}), ShouldBeNil)
}

func TestStoreFoldSplitCrashImages(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given 40 jobs added and folded, the first by key buried with std then archived and kept live to "+
		"run again, the rest archived, all unfolded, and the bound at 1 byte", t, func() {
		pauseLogFolder()
		setFoldTxBytes(1)

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold split crash", "a", 40)
		for _, job := range jobs {
			job.ReqGroup = foldTestReqGroup
		}

		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		sorted := sortedByKey(jobs)
		target := sorted[0]
		key := target.Key()

		target.State = JobStateBuried
		target.Exitcode = 1
		So(s.updateJobAfterExitDurable(target, []byte("out"), []byte("err"), false), ShouldBeNil)

		target.State = JobStateRunning
		target.Exitcode = 0
		target.RerunAfterRun = true
		target.StartTime = time.Now().Add(-time.Minute)
		target.EndTime = time.Now()

		outcome, err := s.archiveCompletion(key, target)
		So(err, ShouldBeNil)
		So(outcome, ShouldEqual, archiveKeptLive)

		for _, job := range sorted[1:] {
			job.StartTime = time.Now().Add(-time.Minute)
		}

		archiveIncTestJobs(s, sorted[1:])

		Convey("a store imaged after the fold's 1st of its 3 transactions, opened, answers and checkpoints as "+
			"one imaged before the fold, and both, folded, answer as the store", func() {
			images := imageFoldTransactions(t, dir, 2)

			So(s.foldNow(), ShouldBeNil)
			logFoldTxHook.Store(nil)
			So(*images, ShouldHaveLength, 2)

			want := foldCrashAnswers(ctx, s, jobs)
			So(want, ShouldContainSubstring, key+` live=true complete=true stdo="" stde=""`)

			soImagesAnswerAsBefore(ctx, *images, jobs, want, inspectedCount(s, kindStat))
		})
	})

	Convey("Given an add of 1,000 jobs in rep group a unfolded, then a modify of the first 100 into rep group "+
		"b, unfolded, and the bound at 1 byte", t, func() {
		pauseLogFolder()
		setFoldTxBytes(1)

		s, dir := openTestLogStore(t, ctx)

		jobs := testLogJobs("fold split crash lookups", "a", 1000)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		mod := jobs[:100]
		keys := make([]string, len(mod))

		for i, job := range mod {
			keys[i] = job.Key()
			job.RepGroup = "b"
		}

		So(s.modifyLiveJobs(ctx, keys, mod), ShouldBeNil)

		Convey("stores imaged between the fold's transactions, opened, answer and checkpoint as one imaged "+
			"before it, and, folded, answer as the store", func() {
			images := imageFoldTransactions(t, dir, 0)

			So(s.foldNow(), ShouldBeNil)
			logFoldTxHook.Store(nil)
			So(len(*images), ShouldBeGreaterThan, 2)

			all := *images
			picked := []string{all[0], all[1], all[len(all)/2], all[len(all)-1]}

			soImagesAnswerAsBefore(ctx, picked, jobs, foldCrashAnswers(ctx, s, jobs), inspectedCount(s, kindStat))
		})
	})
}

// historyCompleteCount returns how many completed jobs s's history.bolt holds.
func historyCompleteCount(s *logStore) int {
	n := 0

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketJobsComplete).Stats().KeyN

		return nil
	})
	So(err, ShouldBeNil)

	return n
}

// setFoldTxBytes sets logFoldTxBytes to n until the Convey block ends.
func setFoldTxBytes(n int64) {
	logFoldTxBytes.Store(n)

	Reset(func() { logFoldTxBytes.Store(0) })
}

// imageFoldTransactions has each of the next fold's transactions, up to max
// of them (all if max is 0), copy dir as a crash would leave it before the
// transaction commits, into a new directory whose path it appends to the
// returned slice.
func imageFoldTransactions(t *testing.T, dir string, maxImages int) *[]string {
	t.Helper()

	var images []string

	hook := func() error { //nolint:unparam // logFoldTxHook's type
		if maxImages == 0 || len(images) < maxImages {
			img := filepath.Join(t.TempDir(), fmt.Sprintf("image%d", len(images)))
			So(copyCrashStore(dir, img), ShouldBeNil)

			images = append(images, img)
		}

		return nil
	}
	logFoldTxHook.Store(&hook)

	Reset(func() { logFoldTxHook.Store(nil) })

	return &images
}

// foldTxPageAllocs folds s and returns the bytes of pages each of the fold's
// history.bolt transactions allocated when it committed, but for its freelist:
// the bytes it wrote of its own records, which the bound is of. Nothing else
// writes history.bolt while it folds, so the PageAlloc history.bolt adds
// between one transaction's hook and the next's is the first's, and the
// freelist history.bolt's stats hold at the second hook is the one the first
// wrote.
func foldTxPageAllocs(s *logStore) []int64 {
	var marks, freelists []int64

	pageSize := s.history.bolt.Info().PageSize

	mark := func() error { //nolint:unparam // logFoldTxHook's type
		stats := s.history.bolt.Stats()
		marks = append(marks, stats.TxStats.GetPageAlloc())
		freelists = append(freelists, int64(s.history.freelistWrite(pageSize)))

		return nil
	}
	logFoldTxHook.Store(&mark)

	defer logFoldTxHook.Store(nil)

	So(s.foldNow(), ShouldBeNil)
	So(mark(), ShouldBeNil)

	allocs := make([]int64, 0, len(marks)-1)
	for i := 1; i < len(marks); i++ {
		allocs = append(allocs, marks[i]-marks[i-1]-freelists[i])
	}

	return allocs
}

// sumInt64 returns the sum of vs.
func sumInt64(vs []int64) int64 {
	var sum int64

	for _, v := range vs {
		sum += v
	}

	return sum
}

// historyMaxSeqOf returns the highest folded seq s's history.bolt holds.
func historyMaxSeqOf(s *logStore) uint64 {
	var seq uint64

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		seq = historyMaxSeq(tx)

		return nil
	})
	So(err, ShouldBeNil)

	return seq
}

// maxCompleteSeq returns the highest completeSeq s's history.bolt holds of
// jobs' keys.
func maxCompleteSeq(s *logStore, jobs []*Job) uint64 {
	var seq uint64

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		seqs := tx.Bucket(bucketCompleteSeq)

		for _, job := range jobs {
			if v := seqs.Get([]byte(job.Key())); len(v) == historySeqLen {
				seq = max(seq, binary.BigEndian.Uint64(v))
			}
		}

		return nil
	})
	So(err, ShouldBeNil)

	return seq
}

// historyRepGroups returns the rep groups ever added that s's history.bolt
// holds.
func historyRepGroups(s *logStore) []string {
	var rgs []string

	err := s.history.bolt.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRGs).ForEach(func(k, _ []byte) error {
			rgs = append(rgs, string(k))

			return nil
		})
	})
	So(err, ShouldBeNil)

	return rgs
}

// retrievedRepGroups returns s's retrieveRepGroups, asserting it did not fail.
func retrievedRepGroups(s *logStore) []string {
	rgs, err := s.retrieveRepGroups()
	So(err, ShouldBeNil)

	return rgs
}

// soImagesAnswerAsBefore opens each of images, the first taken before any of a
// fold's transactions committed, and asserts each answers about jobs as the
// first does, checkpoints, folds, then answers as want, the store's answers
// after the fold, holds stats learned-requirement samples, and
// checkpoints again. (A sample is committed with its completion, so an image
// between transactions can hold more than the first.)
func soImagesAnswerAsBefore(ctx context.Context, images []string, jobs []*Job, want string, stats int) {
	before := ""

	for i, img := range images {
		r := openLogStoreIn(ctx, img)

		got := foldCrashAnswers(ctx, r, jobs)
		if i == 0 {
			before = got
		}

		So(got, ShouldEqual, before)
		So(r.checkpoint(), ShouldBeNil)
		So(r.foldNow(), ShouldBeNil)
		So(foldCrashAnswers(ctx, r, jobs), ShouldEqual, want)
		So(inspectedCount(r, kindStat), ShouldEqual, stats)
		So(r.checkpoint(), ShouldBeNil)
		So(r.close(ctx), ShouldBeNil)
	}
}

// foldCrashAnswers describes every answer s gives about jobs: the rep groups
// ever added, each one's complete jobs, each job's live and complete state and
// std, and the complete records an inspection finds.
func foldCrashAnswers(ctx context.Context, s *logStore, jobs []*Job) string {
	var b strings.Builder

	rgs, err := s.retrieveRepGroups()
	So(err, ShouldBeNil)
	slices.Sort(rgs)
	fmt.Fprintf(&b, "rgs=%v\n", rgs)

	for _, rg := range rgs {
		got, errr := s.retrieveCompleteJobsByRepGroup(rg)
		So(errr, ShouldBeNil)
		fmt.Fprintf(&b, "complete[%s]=%v\n", rg, jobKeys(sortedByKey(got)))
	}

	for _, job := range sortedByKey(jobs) {
		key := job.Key()

		live, errl := s.checkIfLive(key)
		So(errl, ShouldBeNil)

		complete, errc := s.checkIfComplete(key)
		So(errc, ShouldBeNil)

		stdo, stde := s.retrieveJobStd(ctx, key)
		fmt.Fprintf(&b, "%s live=%v complete=%v stdo=%q stde=%q\n", key, live, complete, stdo, stde)
	}

	fmt.Fprintf(&b, "inspect complete=%d\n", inspectedCount(s, kindComplete))

	return b.String()
}
