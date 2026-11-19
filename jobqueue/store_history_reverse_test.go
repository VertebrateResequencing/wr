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
	"fmt"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestStoreFoldReverseLookupsMerged(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose pending reverse rglookups merge once 300 gather, its folder paused, and "+
		"1,000 jobs added under rep group a and folded", t, func() {
		pauseLogFolder()
		logReverseMergeEntries.Store(300)
		setFoldTxBytes(1)

		Reset(func() { logReverseMergeEntries.Store(0) })

		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("reverse merge", "a", 1000)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		pending, merged := reverseLookupCounts(s)
		So(pending, ShouldEqual, 1000)
		So(merged, ShouldEqual, 0)

		Convey("the next folds merge them into keyRepgroups a transaction at a time, in its key order, "+
			"holding the keys of a sixteenth of them at a time", func() {
			So(foldAgain(ctx, s, "reverse merge next 0"), ShouldBeNil)

			pending, merged = reverseLookupCounts(s)
			So(merged, ShouldBeBetween, 0, 1000)
			So(pending, ShouldEqual, 1000-merged+1)
			So(mergedAreFirstKeys(s, jobs, merged), ShouldBeTrue)

			held := len(s.history.reverse.merging) + merged

			for i := 1; s.history.reverse.underWay() && i < 100; i++ {
				So(foldAgain(ctx, s, fmt.Sprintf("reverse merge next %d", i)), ShouldBeNil)

				_, now := reverseLookupCounts(s)
				held = max(held, len(s.history.reverse.merging)+now-merged)
				merged = now
			}

			// A sixteenth of 1,000 random keys is about 63; 1,000/8 is far
			// above any part's share but far below all of them.
			So(held, ShouldBeLessThan, 1000/8)

			So(mergedAreFirstKeys(s, jobs, 1000), ShouldBeTrue)
			So(historyLookupCount(s, "a"), ShouldEqual, 1000)
		})

		Convey("a modify of all 1,000 into rep group b, made while their merge is half done and folded, "+
			"leaves none of them under a, merged or pending, and the merge then skips them", func() {
			So(foldAgain(ctx, s, "reverse merge half"), ShouldBeNil)

			_, merged = reverseLookupCounts(s)
			So(merged, ShouldBeBetween, 0, 1000)

			keys := make([]string, len(jobs))

			for i, job := range jobs {
				keys[i] = job.Key()
				job.RepGroup = "b"
			}

			So(s.modifyLiveJobs(ctx, keys, jobs), ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			So(historyLookupCount(s, "a"), ShouldEqual, 0)
			So(historyLookupCount(s, "b"), ShouldEqual, 1000)

			for i := 0; s.history.reverse.underWay() && i < 100; i++ {
				So(s.foldNow(), ShouldBeNil)
				So(foldAgain(ctx, s, fmt.Sprintf("reverse merge after %d", i)), ShouldBeNil)
			}

			So(historyLookupCount(s, "a"), ShouldEqual, 0)

			Convey("and a second modify of them, into rep group c, after the merge of b's, leaves them under c "+
				"alone, also after a reopen", func() {
				logReverseMergeEntries.Store(1)

				// Each fold adds a job of rep group z, whose reverse rglookup
				// the next fold merges.
				for i := 0; reverseLookupPending(s) > 1 && i < 1000; i++ {
					So(foldAgain(ctx, s, fmt.Sprintf("reverse merge b %d", i)), ShouldBeNil)
				}

				So(reverseLookupPending(s), ShouldEqual, 1)

				for _, job := range jobs {
					job.RepGroup = "c"
				}

				So(s.modifyLiveJobs(ctx, keys, jobs), ShouldBeNil)
				So(s.foldNow(), ShouldBeNil)

				eachReopen(ctx, &s, func(st *logStore) {
					So(historyLookupCount(st, "a"), ShouldEqual, 0)
					So(historyLookupCount(st, "b"), ShouldEqual, 0)
					So(historyLookupCount(st, "c"), ShouldEqual, 1000)
				})
			})
		})
	})
}

func TestStoreFoldReverseMergeCountsDrops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose pending reverse rglookups merge once 1,500 gather, its folder paused, "+
		"1,000 jobs added under rep group a and folded, then modified into rep group b and folded, so that "+
		"1,000 have been put pending twice and dropped once", t, func() {
		pauseLogFolder()
		logReverseMergeEntries.Store(1500)

		Reset(func() { logReverseMergeEntries.Store(0) })

		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("reverse merge drops", "a", 1000)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		keys := make([]string, len(jobs))

		for i, job := range jobs {
			keys[i] = job.Key()
			job.RepGroup = "b"
		}

		So(s.modifyLiveJobs(ctx, keys, jobs), ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		Convey("later folds start no merge until 1,500 are pending, and then do", func() {
			So(foldAgain(ctx, s, "reverse merge drops 0"), ShouldBeNil)

			pending, merged := reverseLookupCounts(s)
			So(pending, ShouldEqual, 1001)
			So(merged, ShouldEqual, 0)

			more := testLogJobs("reverse merge drops more", "c", 499)
			_, err = s.storeNewJobsGuarded(ctx, more, false, nil)
			So(err, ShouldBeNil)
			So(s.foldNow(), ShouldBeNil)

			_, merged = reverseLookupCounts(s)
			So(merged, ShouldEqual, 0)

			So(foldAgain(ctx, s, "reverse merge drops 1"), ShouldBeNil)

			_, merged = reverseLookupCounts(s)
			So(merged, ShouldBeGreaterThan, 0)
		})
	})
}

func TestStoreFoldReverseMergeTxBounded(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store whose pending reverse rglookups merge once 5,000 gather, whose fold "+
		"transactions are bounded at a byte, its folder paused, and 5,000 jobs added and folded", t, func() {
		pauseLogFolder()
		logReverseMergeEntries.Store(5000)
		setFoldTxBytes(1)

		Reset(func() { logReverseMergeEntries.Store(0) })

		s, _ := openTestLogStore(t, ctx)

		jobs := testLogJobs("reverse merge bounded", "a", 5000)
		_, err := s.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)
		So(s.foldNow(), ShouldBeNil)

		pending, merged := reverseLookupCounts(s)
		So(pending, ShouldEqual, 5000)
		So(merged, ShouldEqual, 0)

		Convey("the next fold's merge transaction moves one batch of them, not its whole part of the "+
			"key space", func() {
			So(foldAgain(ctx, s, "reverse merge bounded next"), ShouldBeNil)

			_, merged = reverseLookupCounts(s)
			So(merged, ShouldBeBetweenOrEqual, 1, foldLookupBatch)
		})
	})
}

// mergedAreFirstKeys reports whether keyRepgroups holds, of rep group a's,
// exactly the first n of jobs' keyRepgroups keys under it, in key order.
func mergedAreFirstKeys(s *logStore, jobs []*Job, n int) bool {
	want := make([][]byte, 0, len(jobs))
	for _, job := range jobs {
		want = append(want, []byte(job.Key()+dbDelimiter+"a"))
	}

	slices.SortFunc(want, bytes.Compare)

	var got [][]byte

	So(s.history.bolt.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketKeyRGs).ForEach(func(k, _ []byte) error {
			if bytes.HasSuffix(k, []byte(dbDelimiter+"a")) {
				got = append(got, bytes.Clone(k))
			}

			return nil
		})
	}), ShouldBeNil)

	return fmt.Sprint(got) == fmt.Sprint(want[:n])
}

// reverseLookupPending returns how many entries s's history.bolt has in
// keyRepgroupsPending.
func reverseLookupPending(s *logStore) int {
	pending, _ := reverseLookupCounts(s)

	return pending
}

// foldAgain adds a job under rep group z with command cmd to s and folds,
// returning the first error.
func foldAgain(ctx context.Context, s *logStore, cmd string) error {
	if _, err := s.storeNewJobsGuarded(ctx, []*Job{testDBJob(cmd, "z")}, false, nil); err != nil {
		return err
	}

	return s.foldNow()
}

// reverseLookupCounts returns how many entries s's history.bolt has in
// keyRepgroupsPending and in keyRepgroups.
func reverseLookupCounts(s *logStore) (int, int) {
	var pending, merged int

	So(s.history.bolt.View(func(tx *bolt.Tx) error {
		pending, merged = tx.Bucket(bucketKeyRGsPending).Stats().KeyN, tx.Bucket(bucketKeyRGs).Stats().KeyN

		return nil
	}), ShouldBeNil)

	return pending, merged
}
