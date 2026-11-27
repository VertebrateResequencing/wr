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

// Spec D3's tests: the log store's history queries answered from sealed
// history segments.

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

const (
	// histBigRepGroup is the rep group of tests 2 and 3, histBigJobs how many
	// jobs completed in it, over histBigSegments segments.
	histBigRepGroup = "big"
	histBigJobs     = 50_000
	histBigSegments = 5

	// histBigStartStep spreads test 3's start times: job i's offset is
	// (i*histBigStartStep)%histBigJobs, a step coprime with histBigJobs, so
	// every offset differs and the oldest are in every segment, and it starts
	// half its offset in seconds after segTestEnd.
	histBigStartStep = 7919

	// histBigMaxHeapPerJob bounds the peak heap growth, a job, of the oldest
	// query and the status with details of histBigRepGroup, about twice what
	// they take, which is about what the status without details takes for
	// the job's key: holding each job's run state took over 1,100 bytes. The
	// race detector's shadow memory is not in the heap, so it holds under
	// -race too.
	histBigMaxHeapPerJob = 400

	// histPricedCopies is how many copies test 4 prices, and
	// histPricedBudgetCopies how many of today's Binc records its budget
	// holds.
	histPricedCopies       = 1000
	histPricedBudgetCopies = 500

	// histPricedSlack is the divisor of the count at which test 4's budget
	// trips today that the Phase 2 count may differ from it by: 10%.
	histPricedSlack = 10

	// segTestLookedUpGroup is the rep group the segment lookup tests look
	// two keys up under, which complete under others or it.
	segTestLookedUpGroup = "segK"

	// histOwnedJobs is how many completions the facets ownership test puts
	// in one segment: enough for many blocks.
	histOwnedJobs = 10_000

	// histMatchFilteredGroup and histMatchTiedGroup are the rep groups of the
	// segment oldest query's filter and tie tests.
	histMatchFilteredGroup = "segQ"
	histMatchTiedGroup     = "segT"
)

const (
	// histAddedRepGroup, histAddedBatch and histAddedSegments are the rep
	// group of TestHistorySegmentAddedWhileQuerying, how many of its jobs each
	// of its segments holds, and how many segments it has once all are added.
	histAddedRepGroup = "segA"
	histAddedBatch    = 200
	histAddedSegments = 6

	// histAddedDepGroup is the dep group its jobs depend on.
	histAddedDepGroup = "segAdep"

	// histMemHeldGroup is the req group TestHistorySegmentAddedKeepsNoFooter's
	// opened index holds a sample of, and histMemFooterKeys how many keys its
	// added segment's footer has rglookups of, so a footer of some MB, none
	// of which the index keeps.
	histMemHeldGroup  = "segmemA"
	histMemFooterKeys = 200000
)

// histSegOutrankingSeq is the seq of a segment's completion that outranks the
// log's records of a fresh store.
const histSegOutrankingSeq = 1000

// histAddedSeen is what TestHistorySegmentAddedWhileQuerying's queries saw:
// counts of segA's jobs and of segAdep's dependents, segA's latest end times,
// as unix times, and how many times segA had no history.
type histAddedSeen struct {
	counts    [2][]int
	ends      []int64
	noHistory int
}

func TestHistorySegmentAddedWhileQuerying(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store holding a segment of completions of segA, depending on segAdep, ending an hour "+
		"after segTestEnd", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		batchEnd := func(n int) time.Time { return segTestEnd.Add(time.Duration(n) * time.Hour) }
		batch := func(n int) []historyFolded {
			return histMadeCompletions(fmt.Sprintf("segA%d", n), histAddedBatch, func(_ int, job *Job) {
				job.RepGroup = histAddedRepGroup
				job.Dependencies = Dependencies{NewDepGroupDependency(histAddedDepGroup)}
				job.EndTime = batchEnd(n)
				job.StartTime = job.EndTime.Add(-time.Second)
			})
		}

		writeRepGroupSegment(cfg.dbFile, 1, histAddedRepGroup, batch(1))

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		first := s.segs()
		dropped := segKeyOf(testDBJob("echo segA dropped", "segX"))

		Convey("segments added, each ending an hour later, while queries of segA, of its latest end and of "+
			"segAdep's dependents run are seen by them whole, one at a time, and leave the index they began with "+
			"as it was", func() {
			stop := make(chan struct{})
			seen := make(chan histAddedSeen, 1)

			go func() {
				var got histAddedSeen

				for {
					select {
					case <-stop:
						seen <- got

						return
					default:
					}

					jobs, errr := s.retrieveCompleteJobsByRepGroup(histAddedRepGroup)
					got.counts[0] = append(got.counts[0], histAddedCount(len(jobs), errr))

					keys, errd := s.reverseDepKeys(histAddedDepGroup)
					got.counts[1] = append(got.counts[1], histAddedCount(len(keys), errd))

					got.ends = append(got.ends, histAddedEnd(s.retrieveLastCompletionTimeByRepGroup(
						[]string{histAddedRepGroup})))

					if !s.repGroupHasHistory(histAddedRepGroup) {
						got.noHistory++
					}
				}
			}()

			for n := uint32(2); n <= histAddedSegments; n++ {
				writeSegmentOps(cfg.dbFile, n, func(w *histSegmentWriter) {
					histAddCompletions(w, histAddedRepGroup, batch(int(n)))
					w.addDrop(dropped, uint64(n))
				})
				So(s.addSegment(n), ShouldBeNil)
			}

			close(stop)

			var got histAddedSeen
			select {
			case got = <-seen:
			case <-time.After(time.Minute):
			}

			for _, seq := range got.counts {
				So(seq, ShouldNotBeEmpty)
				So(slices.IsSorted(seq), ShouldBeTrue)
				So(slices.ContainsFunc(seq, func(c int) bool { return c%histAddedBatch != 0 || c <= 0 }),
					ShouldBeFalse)
			}

			So(got.ends, ShouldNotBeEmpty)
			So(slices.IsSorted(got.ends), ShouldBeTrue)
			So(slices.ContainsFunc(got.ends, func(end int64) bool {
				return end < batchEnd(1).Unix() || end > batchEnd(histAddedSegments).Unix() ||
					(end-segTestEnd.Unix())%int64(time.Hour/time.Second) != 0
			}), ShouldBeFalse)
			So(got.noHistory, ShouldEqual, 0)

			latest, errl := s.retrieveLastCompletionTimeByRepGroup([]string{histAddedRepGroup})
			So(errl, ShouldBeNil)
			So(latest[histAddedRepGroup], ShouldEqual, batchEnd(histAddedSegments).UTC())

			jobs, errr := s.retrieveCompleteJobsByRepGroup(histAddedRepGroup)
			So(errr, ShouldBeNil)
			So(jobs, ShouldHaveLength, histAddedBatch*histAddedSegments)

			keys, errd := s.reverseDepKeys(histAddedDepGroup)
			So(errd, ShouldBeNil)
			So(keys, ShouldHaveLength, histAddedBatch*histAddedSegments)
			So(s.segs().dropped, ShouldResemble, map[logKey]uint32{dropped: histAddedSegments})

			So(first.segs, ShouldHaveLength, 1)
			So(first.repGroups[histAddedRepGroup], ShouldResemble, []uint32{1})
			So(first.revDeps, ShouldResemble, map[string][]uint32{histAddedDepGroup: {1}})
			So(first.dropped, ShouldBeEmpty)
			So(first.groupEnds, ShouldResemble, map[string]int64{histAddedRepGroup: batchEnd(1).Unix()})
		})
	})
}

// segKeyOf returns job's key as a logKey.
func segKeyOf(job *Job) logKey {
	k, err := parseLogKey(job.Key())
	So(err, ShouldBeNil)

	return k
}

// histAddedCount returns n, or -1 if err is not nil.
func histAddedCount(n int, err error) int {
	if err != nil {
		return -1
	}

	return n
}

// histAddedEnd returns the unix time of times' histAddedRepGroup, or -1 if err
// is not nil.
func histAddedEnd(times map[string]time.Time, err error) int64 {
	if err != nil {
		return -1
	}

	return times[histAddedRepGroup].Unix()
}

func TestHistoryQueriesFromSegments(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given rep group big with 50,000 completions over 5 segments", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		complete := histBigCompletions()
		perSegment := histBigJobs / histBigSegments

		for n := range histBigSegments {
			writeRepGroupSegment(cfg.dbFile, uint32(n+1), histBigRepGroup, complete[n*perSegment:(n+1)*perSegment])
		}

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s

		Convey("retrieveCompleteJobStatusByRepGroup counts 50,000 complete from footers, reading no record "+
			"block", func() {
			blocks := s.segs().blockReads.Load()

			status, errs := s.retrieveCompleteJobStatusByRepGroup(histBigRepGroup, false)
			So(errs, ShouldBeNil)
			So(status.Counts[JobStateComplete], ShouldEqual, histBigJobs)
			So(s.segs().blockReads.Load()-blocks, ShouldEqual, 0)
			So(s.segs().footers.reads.Load(), ShouldBeGreaterThan, 0)

			details, errd := s.retrieveCompleteJobStatusByRepGroup(histBigRepGroup, true)
			So(errd, ShouldBeNil)
			So(details.Counts[JobStateComplete], ShouldEqual, histBigJobs)
			So(s.segs().blockReads.Load()-blocks, ShouldBeGreaterThan, 0)
		})

		Convey("retrieveOldestCompleteJobsByRepGroup with a limit of 10 returns the 10 oldest by StartTime "+
			"and counts 50,000", func() {
			page, errp := s.retrieveOldestCompleteJobsByRepGroup(histBigRepGroup, allCompleteJobsQuery(10))
			So(errp, ShouldBeNil)
			So(jobKeys(page.jobs), ShouldResemble, histOldestKeys(complete, 10))
			So(page.counted, ShouldResemble, map[string]int{allCompleteGroup: histBigJobs})
			So(page.fetched, ShouldResemble, map[string]int{allCompleteGroup: 10})
		})

		Convey("the oldest query with a limit of 10 and the status with details each grow the heap by under "+
			"400 bytes a job, holding no job's run state", func() {
			var errq error

			query := completeJobsQuery{
				group: func(*archivedJobFacets) (string, bool) { return allCompleteGroup, true },
				limit: func(string) int { return 10 },
			}

			keysOnly := peakHeapGrowthDuring(func() {
				_, errq = s.retrieveCompleteJobStatusByRepGroup(histBigRepGroup, false)
			})

			So(errq, ShouldBeNil)

			oldest := peakHeapGrowthDuring(func() {
				_, errq = s.retrieveOldestCompleteJobsByRepGroup(histBigRepGroup, query)
			})

			So(errq, ShouldBeNil)

			details := peakHeapGrowthDuring(func() {
				_, errq = s.retrieveCompleteJobStatusByRepGroup(histBigRepGroup, true)
			})

			So(errq, ShouldBeNil)

			runtime.KeepAlive(complete)

			t.Logf("peak heap growth a job: status %d, oldest %d, details %d", keysOnly/histBigJobs,
				oldest/histBigJobs, details/histBigJobs)
			So(oldest, ShouldBeLessThan, histBigMaxHeapPerJob*histBigJobs)
			So(details, ShouldBeLessThan, histBigMaxHeapPerJob*histBigJobs)
		})
	})
}

func TestHistorySegmentFacetsOwned(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a segment of 10,000 completions over many blocks, each with its own FailReason and one of "+
		"four States", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		// States of different lengths, so that a State aliasing a reused
		// block buffer reads as another's bytes.
		states := []JobState{JobStateComplete, JobStateBuried, JobStateReserved, JobStateLost}
		complete := histBigCompletions()[:histOwnedJobs]
		wantReasons := make(map[string]int, len(complete))
		wantStates := make(map[string]int, len(states))

		for i := range complete {
			complete[i].job.FailReason = fmt.Sprintf("reason %d", i)
			complete[i].job.State = states[i%len(states)]
			wantReasons[complete[i].job.FailReason] = 1
			wantStates[string(complete[i].job.State)]++
		}

		writeRepGroupSegment(cfg.dbFile, 1, histBigRepGroup, complete)

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		So(len(s.segs().segs[0].blocks), ShouldBeGreaterThan, 1)

		Convey("retrieveOldestCompleteJobsByRepGroup groups every job by its own FailReason, its group the "+
			"facets' string itself", func() {
			page, errp := s.retrieveOldestCompleteJobsByRepGroup(histBigRepGroup, histFacetQuery(
				func(f *archivedJobFacets) string { return f.FailReason }))
			So(errp, ShouldBeNil)
			So(histWrongCounts(page.counted, wantReasons), ShouldEqual, 0)
		})

		Convey("retrieveOldestCompleteJobsByRepGroup groups every job by its own State, its group the facets' "+
			"string itself", func() {
			page, errp := s.retrieveOldestCompleteJobsByRepGroup(histBigRepGroup, histFacetQuery(
				func(f *archivedJobFacets) string { return string(f.State) }))
			So(errp, ShouldBeNil)
			So(page.counted, ShouldResemble, wantStates)
		})
	})
}

func TestHistorySegmentUsageState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a segment of 10,000 completions over many blocks, every other one recorded as reserved and "+
		"the rest as buried, each started an hour ago and ended a second later", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		start := time.Now().Add(-time.Hour)
		complete := histBigCompletions()[:histOwnedJobs]

		for i := range complete {
			job := complete[i].job
			job.State, job.StartTime, job.EndTime = JobStateBuried, start, start.Add(time.Second)

			if i%2 == 0 {
				job.State = JobStateReserved
			}
		}

		writeRepGroupSegment(cfg.dbFile, 1, histBigRepGroup, complete)

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)
		So(len(result.s.segs().segs[0].blocks), ShouldBeGreaterThan, 1)

		Convey("the status with details takes each reserved one's wall time to now, as wallTime does of a "+
			"reserved job, and each buried one's to its end", func() {
			status, errs := result.s.retrieveCompleteJobStatusByRepGroup(histBigRepGroup, true)
			So(errs, ShouldBeNil)
			So(status.Walltime.NumDataValues(), ShouldEqual, histOwnedJobs)

			reserved := float64(histOwnedJobs / 2)
			minMean := (reserved*float64(time.Hour) + (histOwnedJobs-reserved)*float64(time.Second)) / histOwnedJobs
			So(status.Walltime.Mean(), ShouldBeGreaterThanOrEqualTo, minMean)
		})
	})
}

// histBigCompletions returns tests 2 and 3's completions of histBigRepGroup,
// at offsets spread by histBigStartStep: two jobs start at each second, and
// each job ends later the earlier its offset, so that ordering by start time,
// then end time, differs from ordering by either alone.
func histBigCompletions() []historyFolded {
	complete := make([]historyFolded, histBigJobs)
	bad := 0

	for i := range complete {
		job := testDBArchivedJob(fmt.Sprintf("echo big %d", i), histBigRepGroup, segTestEnd)
		offset := (i * histBigStartStep) % histBigJobs
		job.StartTime = segTestEnd.Add(time.Duration(offset/2) * time.Second)
		job.EndTime = job.StartTime.Add(time.Duration(histBigJobs-offset) * time.Second)

		k, err := parseLogKey(job.Key())
		if err != nil {
			bad++
		}

		complete[i] = historyFolded{key: k, seq: uint64(i + 1), job: job}
	}

	So(bad, ShouldEqual, 0)

	return complete
}

func TestHistoryArchivedPricing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given copies of a production-shaped completed job in segments, and a budget of 500 of today's "+
		"Binc records", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		copies := make([]historyFolded, histPricedCopies)
		bincLens := make([]int, histPricedCopies)
		bad := 0

		for i := range copies {
			job := productionShapedJob(i)

			k, errk := parseLogKey(job.Key())

			var binc []byte
			if errk != nil || codec.NewEncoderBytes(&binc, s.ch).Encode(job) != nil {
				bad++
			}

			copies[i] = historyFolded{key: k, seq: uint64(i + 1), job: job}
			bincLens[i] = len(binc)
		}

		So(bad, ShouldEqual, 0)

		writeRepGroupSegment(cfg.dbFile, 1, copies[0].job.RepGroup, copies)

		old := maxArchivedBytes
		maxArchivedBytes = histPricedBudgetCopies * bincLens[0]

		Reset(func() { maxArchivedBytes = old })

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		Convey("the count at which ErrArchivedHistoryTooBig trips differs from Binc pricing's by at most "+
			"10%", func() {
			today := newArchivedBytesBudget()

			var todayErr error
			for _, n := range bincLens {
				if todayErr = today.spendLen(n); todayErr != nil {
					break
				}
			}

			So(todayErr, ShouldWrap, ErrArchivedHistoryTooBig)

			budget := newArchivedBytesBudget()
			errp := result.s.spendArchivedBytesByRepGroup(copies[0].job.RepGroup, budget)
			So(errp, ShouldWrap, ErrArchivedHistoryTooBig)

			t.Logf("Binc trips at %d, Phase 2 at %d", today.priced, budget.priced)
			So(budget.priced, ShouldBeBetweenOrEqual, today.priced-today.priced/histPricedSlack,
				today.priced+today.priced/histPricedSlack)
		})

		Convey("each copy costs its spec and complete records' lengths, as the log writes them, times "+
			"archivedFlatScale per mille", func() {
			want := 0
			for i := range copies {
				want += histRecordsPrice(copies[i].key, copies[i].job, s.ch)
			}

			budget := &archivedBytesBudget{remaining: math.MaxInt}
			So(result.s.spendArchivedBytesByRepGroup(copies[0].job.RepGroup, budget), ShouldBeNil)
			So(budget.priced, ShouldEqual, histPricedCopies)
			So(math.MaxInt-budget.remaining, ShouldEqual, want)
		})
	})

	Convey("Given a production-shaped job completed into the log store's overlay", t, func() {
		pauseLogFolder()

		s, _ := openTestLogStore(t, ctx)
		job := productionShapedJob(0)
		job.State, job.Exited = "", false

		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		archiveTestJob(s, job, job.StartTime, job.EndTime)

		k, err := parseLogKey(job.Key())
		So(err, ShouldBeNil)

		Convey("it costs its spec and complete records' lengths times archivedFlatScale per mille, as a "+
			"segment entry does", func() {
			budget := &archivedBytesBudget{remaining: math.MaxInt}
			So(s.spendArchivedBytesByRepGroup(job.RepGroup, budget), ShouldBeNil)
			So(budget.priced, ShouldEqual, 1)
			So(math.MaxInt-budget.remaining, ShouldEqual, histRecordsPrice(k, job, s.ch))
		})
	})
}

// productionShapedJob returns D3 test 4's production-shaped completed job,
// its Cmd numbered i, the same length for every i.
func productionShapedJob(i int) *Job {
	cmd := fmt.Sprintf("bash -c 'run_pipeline_step --sample %06d ", i)
	cmd += strings.Repeat("x", 300-len(cmd)-1) + "'"
	start := time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.UTC)

	return &Job{
		Cmd:      cmd,
		Cwd:      "/lustre/scratch126/project/team123/pipelines/run/" + strings.Repeat("c", 10),
		RepGroup: "pipeline_step_" + strings.Repeat("r", 26),
		ReqGroup: "run_pipeline_step_" + strings.Repeat("q", 12),
		Requirements: &jqs.Requirements{
			RAM: 4000, Time: 2 * time.Hour, Cores: 2, Disk: 10,
			Other:    map[string]string{"queue": "normal"},
			CoresSet: true, DiskSet: true, OtherSet: true,
		},
		DepGroups: []string{"depgroup_aaaaaaaaaaa1", "depgroup_aaaaaaaaaaa2", "depgroup_aaaaaaaaaaa3"},
		Dependencies: Dependencies{
			NewDepGroupDependency("dependency_bbbbbbbbb1"), NewDepGroupDependency("dependency_bbbbbbbbb2"),
		},
		Behaviours: Behaviours{{When: OnSuccess, Do: CleanupAll}},
		EnvKey:     "0123456789abcdef0123456789abcdef",
		Host:       "node-5-12-3", HostID: "4f3c2a1b-0000-1111-2222-333344445555", HostIP: "172.27.20.13",
		State: JobStateComplete, Exited: true,
		StartTime: start, EndTime: start.Add(37 * time.Minute),
		PeakRAM: 3172, CPUtime: 31 * time.Minute,
	}
}

// writeRepGroupSegment writes and seals segment n in dir holding an rglookup
// under repGroup of each of complete's keys, as their adds did, then complete.
func writeRepGroupSegment(dir string, n uint32, repGroup string, complete []historyFolded) {
	w, err := createHistSegment(dir, n, new(codec.BincHandle))
	So(err, ShouldBeNil)

	failed := 0

	for i := range complete {
		if w.addLookup(repGroup, complete[i].key, complete[i].seq) != nil {
			failed++
		}
	}

	for i := range complete {
		if w.addCompletion(&complete[i]) != nil {
			failed++
		}
	}

	So(failed, ShouldEqual, 0)
	So(w.seal(historyPos{}), ShouldBeNil)
}

// histRecordsPrice returns what the log's spec and complete records of job,
// of key k, as the log writes them, cost at archivedFlatScale per mille.
func histRecordsPrice(k logKey, job *Job, ch codec.Handle) int {
	spec, err := appendFlatSpec(nil, job, ch)
	So(err, ShouldBeNil)

	rs := newLogRunState(job)
	records := len(appendKeyedRecord(nil, logRecSpec, 1, k, spec)) +
		len(appendKeyedRecord(nil, logRecComplete, 1, k, appendFlatRunState(nil, &rs)))

	return records * archivedFlatScale / 1000
}

// histFacetQuery is a completeJobsQuery keeping every job, in the group of
// the string group returns of its facets, of which it can use none.
func histFacetQuery(group func(f *archivedJobFacets) string) completeJobsQuery {
	return completeJobsQuery{
		group: func(f *archivedJobFacets) (string, bool) { return group(f), true },
		limit: func(string) int { return 0 },
	}
}

// histWrongCounts returns how many groups of counted and want differ in count.
func histWrongCounts(counted, want map[string]int) int {
	wrong := 0

	for group, n := range want {
		if counted[group] != n {
			wrong++
		}
	}

	for group := range counted {
		if _, ok := want[group]; !ok {
			wrong++
		}
	}

	return wrong
}

// histOldestKeys returns the keys of histOldest.
func histOldestKeys(complete []historyFolded, n int) []string {
	keys := make([]string, 0, n)
	for _, c := range histOldest(complete, n) {
		keys = append(keys, c.key.String())
	}

	return keys
}

// histOldest returns the n of complete that started first, of those that
// started together the first to end first.
func histOldest(complete []historyFolded, n int) []historyFolded {
	sorted := slices.SortedFunc(slices.Values(complete), func(a, b historyFolded) int {
		return cmp.Or(a.job.StartTime.Compare(b.job.StartTime), a.job.EndTime.Compare(b.job.EndTime))
	})

	return sorted[:n]
}

func TestHistorySegmentMembership(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store holding a live job, with its folder paused", t, func() {
		pauseLogFolder()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)

		job := testDBJob("echo seg member", "segM")
		_, err = s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		segEnd := segTestEnd.Add(time.Hour)
		later := historyFolded{key: segKeyOf(job), seq: histSegOutrankingSeq,
			job: testDBArchivedJob(job.Cmd, "segM", segEnd)}

		Convey("archived, whose completion of a higher seq a segment also holds, an open keeps the segment's, as "+
			"history holds a later completion than the log's", func() {
			archiveTestJob(s, job, segTestEnd.Add(-time.Second), segTestEnd)
			So(s.close(ctx), ShouldBeNil)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&later), ShouldBeNil) })

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			byKeys, errk := result.s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(segEnd), ShouldBeTrue)
		})

		Convey("archived, whose completion of the same seq a segment also holds, an open keeps the segment's, "+
			"as a complete record at or below its key's seq in history changes only the live plane", func() {
			archiveTestJob(s, job, segTestEnd.Add(-time.Second), segTestEnd)

			s.mu.RLock()
			same := historyFolded{key: later.key, seq: s.state.overlay[later.key].seq, job: later.job}
			s.mu.RUnlock()

			So(s.close(ctx), ShouldBeNil)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&same), ShouldBeNil) })

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			byKeys, errk := result.s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(segEnd), ShouldBeTrue)
		})

		Convey("archived, whose earlier completion a segment holds beside a later completion of another key, an "+
			"open keeps the log's, as the segment's highest seq is not its", func() {
			archiveTestJob(s, job, segTestEnd.Add(-time.Second), segTestEnd)
			So(s.close(ctx), ShouldBeNil)

			earlier := historyFolded{key: later.key, seq: 1, job: testDBArchivedJob(job.Cmd, "segM", segEnd)}
			other := segCompletion(1, "segM", histSegOutrankingSeq)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
				So(w.addCompletion(&earlier), ShouldBeNil)
				So(w.addCompletion(&other), ShouldBeNil)
			})

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			byKeys, errk := result.s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(segTestEnd), ShouldBeTrue)
		})

		Convey("archived, whose completion of a higher seq a segment holds, before a later segment holding only "+
			"a later completion of another key, an open keeps the earlier segment's, as history holds a later "+
			"completion than the log's", func() {
			archiveTestJob(s, job, segTestEnd.Add(-time.Second), segTestEnd)
			So(s.close(ctx), ShouldBeNil)

			other := segCompletion(1, "segM", 2*histSegOutrankingSeq)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&later), ShouldBeNil) })
			writeSegmentOps(cfg.dbFile, 2, func(w *histSegmentWriter) { So(w.addCompletion(&other), ShouldBeNil) })

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			byKeys, errk := result.s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(segEnd), ShouldBeTrue)
		})

		Convey("archived, whose completion of a higher seq a segment holds, before a later segment holding only "+
			"an rglookup, whose highest complete seq is none, an open keeps the earlier segment's", func() {
			archiveTestJob(s, job, segTestEnd.Add(-time.Second), segTestEnd)
			So(s.close(ctx), ShouldBeNil)

			other := segCompletion(1, "segM", 2*histSegOutrankingSeq)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&later), ShouldBeNil) })
			writeSegmentOps(cfg.dbFile, 2, func(w *histSegmentWriter) {
				So(w.addLookup("segM", other.key, other.seq), ShouldBeNil)
			})

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			byKeys, errk := result.s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(segEnd), ShouldBeTrue)
		})

		Convey("archived and folded into history.bolt, added and archived again, and completed of a higher seq "+
			"in a segment, an open keeps the segment's, as history holds a later completion than the log's", func() {
			archiveTestJob(s, job, segTestEnd.Add(-2*time.Second), segTestEnd.Add(-time.Second))
			foldOverlay(s)

			_, err = s.storeNewJobsGuarded(ctx, []*Job{testDBJob(job.Cmd, "segM")}, false, nil)
			So(err, ShouldBeNil)
			archiveTestJob(s, testDBJob(job.Cmd, "segM"), segTestEnd.Add(-time.Second), segTestEnd)
			So(s.close(ctx), ShouldBeNil)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&later), ShouldBeNil) })

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			byKeys, errk := result.s.retrieveCompleteJobsByKeys([]string{job.Key()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(segEnd), ShouldBeTrue)
		})

		Convey("whose completion of a higher seq a segment holds, as if damage cut its delete from the log, an "+
			"open recovers it complete but not live", func() {
			So(s.close(ctx), ShouldBeNil)

			writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) { So(w.addCompletion(&later), ShouldBeNil) })

			result := openLogStoreBounded(ctx, cfg)
			So(result.err, ShouldBeNil)

			live, errl := result.s.checkIfLive(job.Key())
			So(errl, ShouldBeNil)
			So(live, ShouldBeFalse)

			complete, errc := result.s.checkIfComplete(job.Key())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeTrue)
		})
	})
}

func TestHistorySegmentOldestFiltersAndTies(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given completions written over two segments: segQ's 8 with mixed exit codes, FailReasons and "+
		"Lost, and segT's two with identical times, the higher key's in the first segment", t, func() {
		t0 := segTestEnd.Add(time.Hour)
		mixed := histMadeCompletions(histMatchFilteredGroup, 8, func(i int, job *Job) {
			job.StartTime = t0.Add(time.Duration(i) * time.Second)
			job.EndTime = job.StartTime.Add(10 * time.Second)

			if i%2 == 1 {
				job.Exitcode, job.FailReason, job.Lost = 1, FailReasonExit, i%4 == 3
			}
		})
		tied := histMadeCompletions(histMatchTiedGroup, 2, func(_ int, job *Job) {
			job.StartTime, job.EndTime = t0, t0.Add(time.Second)
		})

		lo, hi := tied[0], tied[1]
		if hi.key.String() < lo.key.String() {
			lo, hi = hi, lo
		}

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		half := len(mixed) / 2

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			histAddCompletions(w, histMatchFilteredGroup, mixed[:half])
			histAddCompletions(w, histMatchTiedGroup, []historyFolded{hi})
		})
		writeSegmentOps(cfg.dbFile, 2, func(w *histSegmentWriter) {
			histAddCompletions(w, histMatchFilteredGroup, mixed[half:])
			histAddCompletions(w, histMatchTiedGroup, []historyFolded{lo})
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s

		Convey("the oldest query whose group func discards the jobs that exited 0, as a failure filter does, "+
			"counts and returns only the kept ones", func() {
			group := func(f *archivedJobFacets) string { return fmt.Sprint(f.State, f.Exitcode, f.Lost, f.FailReason) }
			query := completeJobsQuery{
				group: func(f *archivedJobFacets) (string, bool) { return group(f), f.Exitcode != 0 },
				limit: func(string) int { return 10 },
			}

			var wantKeys []string

			wantCounted := map[string]int{}

			for _, c := range mixed {
				if c.job.Exitcode != 0 {
					wantKeys = append(wantKeys, c.key.String())
					wantCounted[group(&archivedJobFacets{State: c.job.State, Exitcode: c.job.Exitcode,
						Lost: c.job.Lost, FailReason: c.job.FailReason})]++
				}
			}

			So(wantCounted, ShouldHaveLength, 2)

			page, errl := s.retrieveOldestCompleteJobsByRepGroup(histMatchFilteredGroup, query)
			So(errl, ShouldBeNil)
			So(jobKeys(page.jobs), ShouldResemble, wantKeys)
			So(page.counted, ShouldResemble, wantCounted)
			So(page.fetched, ShouldResemble, wantCounted)
		})

		Convey("the oldest query with a limit of 1 returns the lower key of the two with identical times, as "+
			"the bbolt store's stable sort of its key-order walk does", func() {
			page, errl := s.retrieveOldestCompleteJobsByRepGroup(histMatchTiedGroup, allCompleteJobsQuery(1))
			So(errl, ShouldBeNil)
			So(jobKeys(page.jobs), ShouldResemble, []string{lo.key.String()})
			So(page.counted, ShouldResemble, map[string]int{allCompleteGroup: 2})
		})
	})
}

// histMadeCompletions returns n completions of jobs of repGroup, each as set
// makes it.
func histMadeCompletions(repGroup string, n int, set func(i int, job *Job)) []historyFolded {
	complete := make([]historyFolded, n)
	bad := 0

	for i := range complete {
		job := testDBArchivedJob(fmt.Sprintf("echo %s %d", repGroup, i), repGroup, segTestEnd)
		set(i, job)

		k, err := parseLogKey(job.Key())
		if err != nil {
			bad++
		}

		complete[i] = historyFolded{key: k, seq: uint64(i + 1), job: job}
	}

	So(bad, ShouldEqual, 0)

	return complete
}

func TestHistorySegmentLookups(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given segments whose rglookups and completions of a key are in different segments, and modifies "+
		"dropping rglookups", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		k1 := segCompletion(1, segTestLookedUpGroup, 1)
		k2 := segCompletion(2, "segC", 2)
		k3 := segCompletion(3, "segB", 3)
		k4 := segCompletion(4, "segD", 4)
		k5 := segCompletion(5, "segE", 5)
		k6 := segCompletion(6, "segG", 6)
		k7 := segCompletion(7, "segJ", 8)
		k8 := segCompletion(8, segTestLookedUpGroup, 9)
		k1Again := segCompletion(1, segTestLookedUpGroup, 7)
		k1Again.job.EndTime = segTestEnd.Add(-time.Hour)
		k1Again.job.StartTime = k1Again.job.EndTime.Add(-time.Second)

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			So(w.addLookup(segTestLookedUpGroup, k1.key, 1), ShouldBeNil)
			So(w.addLookup(segTestLookedUpGroup, k2.key, 2), ShouldBeNil)
			So(w.addLookup("segB", k3.key, 3), ShouldBeNil)
			So(w.addLookup("segD", k4.key, 4), ShouldBeNil)
			So(w.addLookup("segE", k5.key, 5), ShouldBeNil)
			So(w.addLookup("segG", k6.key, 6), ShouldBeNil)
			So(w.addLookup("segI", k7.key, 7), ShouldBeNil)
			w.addDrop(k5.key, 5)
			So(w.addCompletion(&k1), ShouldBeNil)
			So(w.addCompletion(&k3), ShouldBeNil)
		})
		writeSegmentOps(cfg.dbFile, 2, func(w *histSegmentWriter) {
			So(w.addCompletion(&k2), ShouldBeNil)
			w.addDrop(k3.key, 3)
			w.addDrop(k4.key, 4)
			w.addDrop(k6.key, 5)
			So(w.addLookup("segG", k6.key, 6), ShouldBeNil)
			So(w.addCompletion(&k6), ShouldBeNil)
			So(w.addCompletion(&k8), ShouldBeNil)
			w.addDrop(k7.key, 7)
			So(w.addLookup("segI", k7.key, 7), ShouldBeNil)
		})
		writeSegmentOps(cfg.dbFile, 3, func(w *histSegmentWriter) {
			So(w.addLookup("segD", k4.key, 6), ShouldBeNil)
			So(w.addCompletion(&k4), ShouldBeNil)
			So(w.addCompletion(&k1Again), ShouldBeNil)
			w.addDrop(k7.key, 7)
			So(w.addCompletion(&k7), ShouldBeNil)
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		byRG := func(rg string) []string {
			jobs, errr := s.retrieveCompleteJobsByRepGroup(rg)
			So(errr, ShouldBeNil)

			return jobKeys(jobs)
		}

		Convey("a rep group finds each key looked up under it in any segment and not dropped since, "+
			"whichever segment holds its completion", func() {
			So(byRG(segTestLookedUpGroup), ShouldResemble, sortedLogKeys(k1.key, k2.key))
			So(byRG("segC"), ShouldBeEmpty)
			So(byRG("segB"), ShouldBeEmpty)
			So(byRG("segD"), ShouldResemble, sortedLogKeys(k4.key))
			So(byRG("segE"), ShouldBeEmpty)
			So(byRG("segG"), ShouldResemble, sortedLogKeys(k6.key))
			So(byRG("segI"), ShouldBeEmpty)

			status, errs := s.retrieveCompleteJobStatusByRepGroup(segTestLookedUpGroup, true)
			So(errs, ShouldBeNil)
			So(status.Counts[JobStateComplete], ShouldEqual, 2)
			So(status.Memory.N, ShouldEqual, 2)
			So(status.Memory.MeanValue, ShouldEqual, k1.job.PeakRAM)
			So(status.CPUtime.MeanValue, ShouldEqual, float64(k1.job.CPUtime))

			budget := newArchivedBytesBudget()
			So(s.spendArchivedBytesByRepGroup(segTestLookedUpGroup, budget), ShouldBeNil)
			So(budget.priced, ShouldEqual, 2)

			budget = newArchivedBytesBudget()
			So(s.spendArchivedBytesByRepGroup("segE", budget), ShouldBeNil)
			So(budget.priced, ShouldEqual, 0)
		})

		Convey("every rep group ever looked up is a rep group, though its rglookups were dropped, and only "+
			"those completed under have history", func() {
			rgs, errr := s.retrieveRepGroups()
			So(errr, ShouldBeNil)
			So(rgs, ShouldResemble, []string{"segB", "segD", "segE", "segG", "segI", segTestLookedUpGroup})

			So(s.repGroupHasHistory("segC"), ShouldBeTrue)
			So(s.repGroupHasHistory("segB"), ShouldBeTrue)
			So(s.repGroupHasHistory("segE"), ShouldBeFalse)

			times, errt := s.retrieveLastCompletionTimeByRepGroup([]string{segTestLookedUpGroup, "segC", "segE"})
			So(errt, ShouldBeNil)
			So(times, ShouldResemble, map[string]time.Time{
				segTestLookedUpGroup: k8.job.EndTime.Truncate(time.Second).UTC(),
				"segC":               k2.job.EndTime.Truncate(time.Second).UTC(),
			})
		})

		Convey("a key is complete if a segment holds its completion, whatever its rglookups", func() {
			for _, c := range []historyFolded{k1, k2, k3, k4, k6} {
				complete, errc := s.checkIfComplete(c.key.String())
				So(errc, ShouldBeNil)
				So(complete, ShouldBeTrue)
			}

			complete, errc := s.checkIfComplete(k5.key.String())
			So(errc, ShouldBeNil)
			So(complete, ShouldBeFalse)

			byKeys, errk := s.retrieveCompleteJobsByKeys([]string{k3.key.String(), k5.key.String()})
			So(errk, ShouldBeNil)
			So(jobKeys(byKeys), ShouldResemble, []string{k3.key.String()})
		})

		Convey("an add ignoring added jobs finds a key completed under the rep group it was looked up under "+
			"in another segment, and one completed elsewhere", func() {
			stored, errs := s.storeNewJobsGuarded(ctx, []*Job{
				testDBJob(k2.job.Cmd, segTestLookedUpGroup), testDBJob(k4.job.Cmd, "segHide"), testDBJob(k7.job.Cmd, "segI"),
			}, true, nil)
			So(errs, ShouldBeNil)
			So(stored.jobsToQueue, ShouldBeEmpty)
			So(stored.alreadyAdded.Complete, ShouldEqual, 3)
			So(stored.alreadyAdded.CompleteSameRepGroup, ShouldEqual, 1)
			So(stored.alreadyAdded.OtherRepGroups, ShouldHaveLength, 2)
			So(stored.alreadyAdded.OtherRepGroups[0].RepGroup, ShouldEqual, "segD")
			So(stored.alreadyAdded.OtherRepGroups[1].RepGroup, ShouldEqual, "segJ")
		})

		Convey("the recent completions are of each key's latest completion, by end time, so not of one "+
			"a later completion ending earlier superseded", func() {
			recent, errr := s.retrieveCompleteJobsRecent(k1.job.EndTime)
			So(errr, ShouldBeNil)
			So(jobKeys(recent), ShouldResemble, []string{k2.key.String(), k3.key.String(), k4.key.String(),
				k6.key.String(), k7.key.String(), k8.key.String()})

			recent, errr = s.retrieveCompleteJobsRecent(time.Time{})
			So(errr, ShouldBeNil)
			So(jobKeys(recent), ShouldResemble, []string{k1.key.String(), k2.key.String(), k3.key.String(),
				k4.key.String(), k6.key.String(), k7.key.String(), k8.key.String()})
		})

		Convey("reading the stopped store gives each completed job once, as its latest completion", func() {
			So(s.close(ctx), ShouldBeNil)

			var complete []string

			ends := make(map[string]time.Time)

			So(ReadStore(cfg.dbFile, func(live bool, job *Job) error {
				if !live {
					complete = append(complete, job.Key())
					ends[job.Key()] = job.EndTime
				}

				return nil
			}), ShouldBeNil)
			So(complete, ShouldResemble, sortedLogKeys(k1.key, k2.key, k3.key, k4.key, k6.key, k7.key, k8.key))
			So(ends[k1.key.String()].Equal(k1Again.job.EndTime), ShouldBeTrue)
		})
	})

	Convey("Given a segment's rglookup of a live job, and the folder paused", t, func() {
		pauseLogFolder()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		job := testDBJob("echo seg modified", "segF")

		k, err := parseLogKey(job.Key())
		So(err, ShouldBeNil)

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			So(w.addLookup("segF", k, 1), ShouldBeNil)
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		_, err = s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		priced := func() int {
			budget := newArchivedBytesBudget()
			So(s.spendArchivedBytesByRepGroup("segF", budget), ShouldBeNil)

			return budget.priced
		}

		So(priced(), ShouldEqual, 1)

		Convey("modifying its key drops it from the rep group before the modify is folded", func() {
			modified := modifiedCopy(job, job.Cmd+" again")
			So(s.modifyLiveJobs(ctx, []string{job.Key()}, []*Job{modified}), ShouldBeNil)
			So(priced(), ShouldEqual, 1)
		})
	})
}

func TestHistorySegmentRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job completed in a segment, then added again, completed again and that folded", t, func() {
		pauseLogFolder()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		first := segCompletion(1, "segR", 1)

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			So(w.addLookup("segR", first.key, 1), ShouldBeNil)
			So(w.addCompletion(&first), ShouldBeNil)
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		again := testDBJob(first.job.Cmd, "segR")
		_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
		So(err, ShouldBeNil)

		end := first.job.EndTime.Add(time.Hour)
		archiveTestJob(s, again, end.Add(-time.Second), end)
		foldOverlay(s)

		Convey("every history query gives the second completion, once", func() {
			byRG, errr := s.retrieveCompleteJobsByRepGroup("segR")
			So(errr, ShouldBeNil)
			So(jobKeys(byRG), ShouldResemble, []string{first.key.String()})
			So(byRG[0].EndTime.Equal(end), ShouldBeTrue)

			byKeys, errk := s.retrieveCompleteJobsByKeys([]string{first.key.String()})
			So(errk, ShouldBeNil)
			So(byKeys, ShouldHaveLength, 1)
			So(byKeys[0].EndTime.Equal(end), ShouldBeTrue)

			recent, errt := s.retrieveCompleteJobsRecent(time.Time{})
			So(errt, ShouldBeNil)
			So(jobKeys(recent), ShouldResemble, []string{first.key.String()})
			So(recent[0].EndTime.Equal(end), ShouldBeTrue)

			status, errs := s.retrieveCompleteJobStatusByRepGroup("segR", true)
			So(errs, ShouldBeNil)
			So(status.Counts[JobStateComplete], ShouldEqual, 1)
			So(status.EndTime.Equal(end), ShouldBeTrue)
		})
	})
}

func TestHistorySegmentLookupModified(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job completed under segH in a segment that also looks it up under segF, added again "+
		"under segF and modified to another key with the folder paused", t, func() {
		pauseLogFolder()

		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		done := segCompletion(1, "segH", 1)

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			So(w.addLookup("segF", done.key, 1), ShouldBeNil)
			So(w.addCompletion(&done), ShouldBeNil)
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s
		again := testDBJob(done.job.Cmd, "segF")
		_, err = s.storeNewJobsGuarded(ctx, []*Job{again}, false, nil)
		So(err, ShouldBeNil)
		So(s.modifyLiveJobs(ctx, []string{again.Key()}, []*Job{modifiedCopy(again, again.Cmd+" modified")}),
			ShouldBeNil)

		Convey("an add of it under segF finds it completed elsewhere, as the modify dropped its rglookup", func() {
			stored, errs := s.storeNewJobsGuarded(ctx, []*Job{testDBJob(done.job.Cmd, "segF")}, true, nil)
			So(errs, ShouldBeNil)
			So(stored.alreadyAdded.Complete, ShouldEqual, 1)
			So(stored.alreadyAdded.CompleteSameRepGroup, ShouldEqual, 0)
			So(stored.alreadyAdded.OtherRepGroups, ShouldHaveLength, 1)
			So(stored.alreadyAdded.OtherRepGroups[0].RepGroup, ShouldEqual, "segH")

			byRG, errr := s.retrieveCompleteJobsByRepGroup("segF")
			So(errr, ShouldBeNil)
			So(byRG, ShouldBeEmpty)
		})
	})
}

// writeSegmentOps writes segment n in dir with ops, then seals it.
func writeSegmentOps(dir string, n uint32, ops func(w *histSegmentWriter)) {
	w, err := createHistSegment(dir, n, new(codec.BincHandle))
	So(err, ShouldBeNil)

	ops(w)

	So(w.seal(historyPos{}), ShouldBeNil)
}

// sortedLogKeys returns keys as strings, sorted.
func sortedLogKeys(keys ...logKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.String())
	}

	slices.Sort(out)

	return out
}

// histAddCompletions adds to w an rglookup under repGroup of each of
// complete's keys, then complete.
func histAddCompletions(w *histSegmentWriter, repGroup string, complete []historyFolded) {
	failed := 0

	for i := range complete {
		if w.addLookup(repGroup, complete[i].key, complete[i].seq) != nil {
			failed++
		}

		if w.addCompletion(&complete[i]) != nil {
			failed++
		}
	}

	So(failed, ShouldEqual, 0)
}

func TestHistorySegmentAddedKeepsNoFooter(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store opened on a segment holding a learned sample of req group segmemA", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		writeSegmentOps(cfg.dbFile, 1, func(w *histSegmentWriter) {
			w.addSamples(histSample{kind: histSampleRAM, reqGroup: histMemHeldGroup, value: 1})
		})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s

		for _, c := range []struct{ name, reqGroup string }{
			{name: "the held req group segmemA", reqGroup: histMemHeldGroup},
			{name: "a new req group segmemB", reqGroup: "segmemB"},
		} {
			Convey("adding a segment with a big footer and a new sample of "+c.name+" keeps none of the "+
				"footer's bytes", func() {
				writeSegmentOps(cfg.dbFile, 2, func(w *histSegmentWriter) {
					failed := 0

					for i := range histMemFooterKeys {
						if w.addLookup(segTestLookupGroup, segTestKey(i), uint64(i+1)) != nil {
							failed++
						}
					}

					So(failed, ShouldEqual, 0)
					w.addSamples(histSample{kind: histSampleRAM, reqGroup: c.reqGroup, value: 2})
				})

				_, _, length, errr := readHistFooter(cfg.dbFile, 2)
				So(errr, ShouldBeNil)

				growth := heapGrowth(func() { So(s.addSegment(2), ShouldBeNil) })
				t.Logf("footer of %d bytes: heap growth %d", length, growth)

				So(length, ShouldBeGreaterThan, histMemFooterKeys*logKeyLen)
				So(growth, ShouldBeLessThan, length/4)
				So(s.segs().sampleValues(c.reqGroup, histSampleRAM), ShouldContain, 2)
			})
		}
	})
}

// heapGrowth returns how much the live heap grew over fn, after collecting
// garbage before and after it.
func heapGrowth(fn func()) int {
	runtime.GC()

	var before runtime.MemStats

	runtime.ReadMemStats(&before)

	fn()

	runtime.GC()

	var after runtime.MemStats

	runtime.ReadMemStats(&after)

	if after.HeapAlloc <= before.HeapAlloc {
		return 0
	}

	return int(after.HeapAlloc - before.HeapAlloc) //nolint:gosec // a heap size
}

func TestHistorySegmentDamagedSinceOpen(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store opened on a segment holding a completion looked up under segTestRepGroup and "+
		"depending on segdep1, the segment since damaged", t, func() {
		cfg, _ := logStoreOpenConfig(t)
		s, err := openLogStoreLocked(ctx, cfg)
		So(err, ShouldBeNil)
		So(s.close(ctx), ShouldBeNil)

		complete := segCompletion(1, segTestRepGroup, histSegOutrankingSeq)
		writeRepGroupSegment(cfg.dbFile, 1, segTestRepGroup, []historyFolded{complete})

		result := openLogStoreBounded(ctx, cfg)
		So(result.err, ShouldBeNil)

		s = result.s

		damageSegment(cfg.dbFile, 1, func(data []byte) []byte { return data[:len(data)-1] })

		Convey("a query of segdep1's dependents fails as corrupt, rather than missing the completion", func() {
			keys, errk := s.reverseDepKeys("segdep1")
			So(errk, ShouldWrap, errStoreCorrupt)
			So(keys, ShouldBeEmpty)
		})

		Convey("a query of segTestRepGroup's completions fails as corrupt", func() {
			_, errr := s.retrieveCompleteJobsByRepGroup(segTestRepGroup)
			So(errr, ShouldWrap, errStoreCorrupt)
		})

		Convey("adding a damaged or missing later segment fails, leaving the published index as it was", func() {
			published := s.segs()

			writeSegmentOps(cfg.dbFile, 2, func(w *histSegmentWriter) {
				w.addSamples(histSample{kind: histSampleRAM, reqGroup: segTestRepGroup, value: 1})
			})
			damageSegment(cfg.dbFile, 2, func(data []byte) []byte { return data[:len(data)-1] })

			So(s.addSegment(2), ShouldWrap, errStoreCorrupt)
			So(s.addSegment(3), ShouldWrap, os.ErrNotExist)
			So(s.segs(), ShouldEqual, published)
		})
	})
}
