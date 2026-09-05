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

// This file holds throughput benchmarks for the jobqueue manager's critical
// persistence paths, so future performance regressions (in particular a loss of
// BoltDB write-coalescing) can be caught. They are plain testing.B benchmarks
// and therefore only run under `go test -bench`; `go test` (and so `make test`/
// `make race`) never executes them. Run them with `make bench`.
//
// Each benchmark uses a real on-disk BoltDB in a temp dir (via initDB, the same
// harness the existing db tests use) so that disk and commit behaviour is
// represented, and reuses the testDBJob helper to build valid jobs. Alongside
// the standard ns/op and -benchmem metrics, each benchmark reports BoltDB write
// behaviour per benchmarked job via b.ReportMetric:
//
//	bolt_writes/job - low-level page+meta disk writes performed by BoltDB,
//	                  obtained from db.bolt.Stats().TxStats deltas. This is the
//	                  signal that exposes write-coalescing: batching many job
//	                  changes into fewer commits drives this number down, while
//	                  a regression to a commit-per-job pushes it up.
//	bolt_pages/job  - BoltDB page allocations over the same window.
//
// benchJobCount is deliberately a few thousand so throughput, and the per-job
// commit cost, are meaningful rather than dominated by fixed overheads.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

// benchJobCount is the number of jobs each benchmark iteration operates on.
const benchJobCount = 3000

// benchDBWaitTimeout bounds how long we wait for the database's background
// update goroutines to drain before measuring; it only affects a (logged)
// warning, never correctness, since the underlying wait blocks until all
// goroutines finish.
const benchDBWaitTimeout = 5 * time.Minute

// benchArchiveConcurrency is how many jobs are archived concurrently.
// archiveJob encodes outside any transaction and hands the job to the single
// coalescing archiveWriter, which folds every archive pending when it wakes
// into one db.bolt.Update. Only concurrent callers can leave more than one
// archive pending (each blocks on its own outcome), so how deep that fold gets
// is a function of concurrency. The live server reaches archiveJob from many
// simultaneous client-request handlers, so archiving concurrently here mirrors
// that critical path; a single-threaded loop would instead measure the
// degenerate one-transaction-per-job worst case and could not show folding at
// all.
const benchArchiveConcurrency = 64

// BenchmarkOwnMemoryAccounting guards the per-job own-memory accounting cost on
// the runner's hot path. After a job command exits, Client.Execute adds the
// runner's own memory footprint to the job's peak RAM. It does this once per
// job, so the cost of that single measurement directly scales `wr add`
// throughput for large batches of trivial jobs.
//
// The two sub-benchmarks contrast the two ways to obtain that footprint:
//
//	own_pss - ownMemoryMB(): reads only this process's /proc/<pid>/smaps Pss.
//	          This is what Execute now uses.
//	tree    - currentMemory(os.Getpid()): the previous approach, which also
//	          walks the whole process tree via gopsutil Children(), enumerating
//	          every entry under /proc. On a busy host this whole-/proc scan is
//	          the bulk of the cost and was the source of the #503 v3->v4 gopsutil
//	          regression.
//
// Reporting ns/op for both makes the win visible: own_pss should be far cheaper
// than tree (the gap widens with the number of processes on the host). There is
// no behaviour change for normal jobs because the exited job command's peak RSS
// is captured separately via rusage Maxrss, so the child sum that `tree` pays
// for adds nothing useful at this call site.
func BenchmarkOwnMemoryAccounting(b *testing.B) {
	pid := os.Getpid()

	b.Run("own_pss", func(b *testing.B) {
		for range b.N {
			if _, err := ownMemoryMB(); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("tree", func(b *testing.B) {
		for range b.N {
			if _, err := currentMemory(pid); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkAddJobs measures the add/store path: encoding a batch of new jobs
// and persisting them (and their lookup indexes) into the live bucket via
// storeNewJobs, which is what the server does when jobs are added.
func BenchmarkAddJobs(b *testing.B) {
	ctx := context.Background()
	testDB := newBenchDB(b)

	writesBefore := boltWrites(testDB)
	pagesBefore := boltPages(testDB)

	b.ResetTimer()

	for i := range b.N {
		// Build this iteration's batch with the timer stopped, so we measure
		// storage and not job construction, and keep memory bounded regardless of
		// b.N (a time-based benchtime grows b.N large). Each batch needs unique
		// keys, as storeNewJobs treats duplicate keys as already-added no-ops, so
		// every iteration builds a distinct set rather than reusing one. Building
		// jobs does no bolt writes, so the cumulative counters captured above stay
		// correct for the per-job metric.
		b.StopTimer()

		batch := makeBenchJobs(fmt.Sprintf("add-%d", i), benchJobCount)

		b.StartTimer()

		if _, _, _, err := testDB.storeNewJobs(ctx, batch, false); err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()
	reportBoltWriteMetrics(b, testDB, writesBefore, pagesBefore, benchJobCount*b.N)
}

// BenchmarkUpdateJobState measures the per-job state-change persistence path:
// the reserve/start/touch style updates that re-encode a live job and rewrite
// its live-bucket entry via updateJobAfterChange. updateJobAfterChange performs
// its BoltDB write in a background goroutine tracked by db.wg, so each iteration
// fires the whole batch of updates and then waits for those goroutines to drain
// before the next iteration; the timer therefore covers the full persistence of
// the batch, and the bolt write counters reflect every commit. How those
// per-job changes coalesce into BoltDB commits is exactly the write-coalescing
// behaviour we want to keep observable.
func BenchmarkUpdateJobState(b *testing.B) {
	ctx := context.Background()
	testDB := newBenchDB(b)

	jobs := makeBenchJobs("update", benchJobCount)
	if _, _, _, err := testDB.storeNewJobs(ctx, jobs, false); err != nil {
		b.Fatal(err)
	}

	// Drain the background backup/store activity from seeding before measuring.
	testDB.wg.Wait(benchDBWaitTimeout)

	writesBefore := boltWrites(testDB)
	pagesBefore := boltPages(testDB)

	b.ResetTimer()

	for range b.N {
		now := time.Now()

		for _, job := range jobs {
			job.Lock()
			// Simulate a reserve/start/touch state change: flip the state and
			// bump a timestamp so the encoded value genuinely differs.
			if job.State == JobStateRunning {
				job.State = JobStateReserved
			} else {
				job.State = JobStateRunning
				job.StartTime = now
			}
			job.Unlock()

			testDB.updateJobAfterChange(ctx, job)
		}

		// Wait for the background live-bucket writes of this batch to complete
		// so the next iteration (and the final metric capture) sees a quiescent
		// db, and so the timer covers the full persistence of the batch.
		testDB.wg.Wait(benchDBWaitTimeout)
	}

	b.StopTimer()
	reportBoltWriteMetrics(b, testDB, writesBefore, pagesBefore, benchJobCount*b.N)
}

// BenchmarkArchiveJobs measures the completion path: archiveJob moves a job from
// the live bucket to the complete bucket, deletes its std buckets and records
// its per-ReqGroup stats, in the write transaction the archiveWriter folds it
// into. This is the per-job work behind the server's archiveCompletedJob, driven
// concurrently as the server drives it. Each iteration freshly seeds
// benchJobCount live jobs (outside the timer) and then archives them all (inside
// the timer). The reported bolt_writes/job is the headline write-coalescing
// signal for the completion path: a regression that stopped the writer folding
// archives together would push it up sharply (towards one fsync per job).
// bolt_txns/job counts the same thing directly (how many separate write
// transactions the archives were applied in, per archive) via the prod-inert
// archiveTxObserver seam.
//
// This is the SATURATED regime - benchArchiveConcurrency archivers looping with
// no think time - where every writer wake finds a deep queue of pending
// archives, so the fold is at its most effective and ns/op is dominated by the
// WAIT to be picked up and committed. BenchmarkArchiveSpacedArrivals covers the
// production regime, where arrivals are spread wider than bbolt's 10ms batching
// window - the case bbolt's own Batch could not coalesce at all.
func BenchmarkArchiveJobs(b *testing.B) {
	// the recorder is installed first so that, cleanups running LIFO, the database
	// (and so its archive writer) is closed before the observer is cleared.
	rec := newArchiveTxRecorder(b)
	testDB := newBenchDB(b)

	var (
		writesDuringArchive int64
		pagesDuringArchive  int64
	)

	b.ResetTimer()

	for i := range b.N {
		b.StopTimer()

		jobs := seedCompletableJobs(b, testDB, fmt.Sprintf("archive-%d", i))

		writesBefore := boltWrites(testDB)
		pagesBefore := boltPages(testDB)

		b.StartTimer()

		archiveJobsConcurrently(b, testDB, jobs)

		b.StopTimer()

		writesDuringArchive += boltWrites(testDB) - writesBefore
		pagesDuringArchive += boltPages(testDB) - pagesBefore

		b.StartTimer()
	}

	b.StopTimer()

	if jobsTotal := benchJobCount * b.N; jobsTotal > 0 {
		txns, _ := rec.transactions()

		b.ReportMetric(float64(writesDuringArchive)/float64(jobsTotal), "bolt_writes/job")
		b.ReportMetric(float64(pagesDuringArchive)/float64(jobsTotal), "bolt_pages/job")
		b.ReportMetric(float64(txns)/float64(jobsTotal), "bolt_txns/job")
	}
}

// benchSpacedArchives is how many archives BenchmarkArchiveSpacedArrivals drives.
// It is small because that benchmark deliberately paces arrivals in wall-clock
// time rather than saturating the write path.
const benchSpacedArchives = 200

// benchSpacedInterval paces those arrivals. It is longer than bbolt's 10ms
// MaxBatchDelay, so bbolt's own batching cannot coalesce them (production's
// archives arrived ~83ms apart) - only an explicit coalescing writer can.
const benchSpacedInterval = 12 * time.Millisecond

// benchSpacedCommitCost is the artificial cost charged once per archive write
// transaction, standing in for production's freelist-bound commit on its 10.3GB
// database. Without it a fresh temp-dir DB commits in well under the arrival
// interval, so no queue of pending archives can form and there is nothing to
// coalesce.
const benchSpacedCommitCost = 60 * time.Millisecond

// BenchmarkArchiveSpacedArrivals measures the completion path in the PRODUCTION
// regime that reliable4 FINDING 2 diagnosed: archives arriving further apart than
// bbolt's MaxBatchDelay while each commit costs far more than that. There, bbolt's
// Batch gives every archive a write transaction of its own (it detaches its batch
// the instant one starts), so the archives queue on the single bolt write lock -
// live production measured that queue ~600 deep, draining at ~12/s, for a mean
// archive block of 43s against the 60s client timeout floor.
//
// bolt_txns/job is the headline metric: 1.0 means one transaction per archive (the
// pre-fix behaviour), while a coalescing archive writer folds a commit's worth of
// arrivals into each transaction and drives it far below 1. bolt_writes/job and
// ns/op follow it down.
func BenchmarkArchiveSpacedArrivals(b *testing.B) {
	ctx := context.Background()

	// the recorder is installed first so that, cleanups running LIFO, the database
	// (and so its archive writer) is closed before the observer is cleared.
	rec := newArchiveTxRecorder(b)
	rec.commitCost = benchSpacedCommitCost
	testDB := newBenchDB(b)

	var (
		writesDuringArchive int64
		pagesDuringArchive  int64
	)

	b.ResetTimer()

	for i := range b.N {
		b.StopTimer()

		jobs := makeBenchJobs(fmt.Sprintf("spaced-%d", i), benchSpacedArchives)
		if _, _, _, err := testDB.storeNewJobs(ctx, jobs, false); err != nil {
			b.Fatal(err)
		}

		markBenchJobsCompleted(jobs)
		testDB.wg.Wait(benchDBWaitTimeout)

		writesBefore := boltWrites(testDB)
		pagesBefore := boltPages(testDB)

		b.StartTimer()

		archiveJobsSpaced(b, testDB, jobs, benchSpacedInterval)

		b.StopTimer()

		writesDuringArchive += boltWrites(testDB) - writesBefore
		pagesDuringArchive += boltPages(testDB) - pagesBefore

		b.StartTimer()
	}

	b.StopTimer()

	if jobsTotal := benchSpacedArchives * b.N; jobsTotal > 0 {
		txns, biggest := rec.transactions()

		b.ReportMetric(float64(writesDuringArchive)/float64(jobsTotal), "bolt_writes/job")
		b.ReportMetric(float64(pagesDuringArchive)/float64(jobsTotal), "bolt_pages/job")
		b.ReportMetric(float64(txns)/float64(jobsTotal), "bolt_txns/job")
		b.ReportMetric(float64(biggest), "archives/biggest_txn")
	}
}

// archiveJobsSpaced archives every job in its own goroutine, launching them
// interval apart on an ABSOLUTE schedule so the submission window is unaffected by
// a loaded host oversleeping (it catches up instead of stretching).
func archiveJobsSpaced(b *testing.B, testDB *db, jobs []*Job, interval time.Duration) {
	b.Helper()

	ctx := context.Background()
	start := time.Now()

	var wg sync.WaitGroup

	for i, job := range jobs {
		time.Sleep(time.Until(start.Add(time.Duration(i) * interval)))

		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := testDB.archiveJob(ctx, job.Key(), job); err != nil {
				b.Error(err)
			}
		}()
	}

	wg.Wait()
}

// newBenchDB opens a real on-disk BoltDB in a temp dir using the production
// initDB path, in Development mode so background S3/file backups are disabled
// and do not perturb the measured write counts. It registers cleanup that
// closes the db (which also waits for any in-flight background transactions).
func newBenchDB(b *testing.B) *db {
	b.Helper()

	ctx := context.Background()
	tmpdir := b.TempDir()

	testDB, _, err := initDB(
		ctx,
		filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"),
		internal.Development,
		false,
		false,
	)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if closeErr := testDB.close(ctx); closeErr != nil {
			b.Fatal(closeErr)
		}
	})

	return testDB
}

// seedCompletableJobs stores benchJobCount fresh live jobs and marks each as a
// successfully exited job with realistic completion data, so that archiving them
// does the full putJobStats/end-time work. It waits for the seeding background
// writes to drain before returning.
func seedCompletableJobs(b *testing.B, testDB *db, prefix string) []*Job {
	b.Helper()

	jobs := makeBenchJobs(prefix, benchJobCount)
	if _, _, _, err := testDB.storeNewJobs(context.Background(), jobs, false); err != nil {
		b.Fatal(err)
	}

	markBenchJobsCompleted(jobs)

	testDB.wg.Wait(benchDBWaitTimeout)

	return jobs
}

// markBenchJobsCompleted marks each job as a successfully exited job with
// realistic completion data, so that archiving it does the full
// putJobStats/end-time work.
//
// Each job gets a DISTINCT end time spread over a small window. Real jobs finish
// at distinct nanosecond instants; identical end times are an unrealistic worst
// case for the time-ordered end-time index, where a constant time prefix forces
// every forward-index key to sort by its random job-key suffix, scattering BoltDB
// pages. These benchmarks measure the archive write-coalescing path, which is
// unaffected by giving jobs distinct end times, so distinct instants keep the
// index's per-archive page cost representative of real workloads.
func markBenchJobsCompleted(jobs []*Job) {
	start := time.Now()

	for i, job := range jobs {
		job.Lock()
		job.State = JobStateComplete
		job.Exited = true
		job.StartTime = start
		job.EndTime = start.Add(time.Second).Add(time.Duration(i) * time.Microsecond)
		job.PeakRAM = 100
		job.Unlock()
	}
}

// makeBenchJobs builds n valid, distinct jobs (distinct Cmd => distinct Key).
// prefix keeps keys unique across separate batches within one benchmark run.
func makeBenchJobs(prefix string, n int) []*Job {
	jobs := make([]*Job, n)
	for i := range n {
		jobs[i] = testDBJob(fmt.Sprintf("echo %s %d", prefix, i), "bench")
	}

	return jobs
}

// TestArchiveJobsConcurrentlyDoesNotDeadlockOnError guards the drain behaviour
// of archiveJobsConcurrently: a worker that hits an archiveJob error must keep
// draining the unbuffered work channel rather than returning, otherwise the
// sender deadlocks once enough workers have exited. We force every archiveJob to
// fail by closing the database first (the stopped archive writer then rejects
// each archive with errDBClosed, before bolt is reached), and run the real helper
// via testing.Benchmark inside a watchdog: a regression to early-return would
// hang here, which the timeout turns into a clear failure instead of a stuck
// test run.
func TestArchiveJobsConcurrentlyDoesNotDeadlockOnError(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("archiveJobsConcurrently drains its work channel even when every archive errors", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(
			ctx,
			filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"),
			internal.Development,
			false,
			false,
		)
		So(err, ShouldBeNil)

		// closing the DB makes every subsequent archiveJob fail, so all
		// benchArchiveConcurrency workers take the error path.
		So(testDB.close(ctx), ShouldBeNil)

		// more jobs than workers, so an early-returning worker would leave the
		// sender blocked on the unbuffered channel.
		jobs := make([]*Job, benchArchiveConcurrency*4)
		for i := range jobs {
			jobs[i] = testDBJob(fmt.Sprintf("echo deadlock %d", i), "bench")
		}

		done := make(chan struct{})

		go func() {
			defer close(done)

			// testing.Benchmark gives us a real *testing.B to pass to the helper.
			// The forced archive errors mark this inner benchmark as failed (its
			// result is discarded); they do not fail the parent test.
			_ = testing.Benchmark(func(b *testing.B) {
				b.Helper()
				archiveJobsConcurrently(b, testDB, jobs)
			})
		}()

		select {
		case <-done:
			So(true, ShouldBeTrue)
		case <-time.After(30 * time.Second):
			t.Fatal("archiveJobsConcurrently deadlocked on archiveJob error")
		}
	})
}

// reportBoltWriteMetrics reports BoltDB write/page counts normalised per job
// operated on, so the numbers are comparable regardless of benchJobCount or
// b.N. writesBefore/pagesBefore are the cumulative counters captured at the
// start of the timed work; jobsTotal is the number of jobs processed across all
// iterations (benchJobCount * b.N).
func reportBoltWriteMetrics(b *testing.B, testDB *db, writesBefore, pagesBefore int64, jobsTotal int) {
	b.Helper()

	if jobsTotal == 0 {
		return
	}

	writes := boltWrites(testDB) - writesBefore
	pages := boltPages(testDB) - pagesBefore

	b.ReportMetric(float64(writes)/float64(jobsTotal), "bolt_writes/job")
	b.ReportMetric(float64(pages)/float64(jobsTotal), "bolt_pages/job")
}

// boltWrites returns BoltDB's cumulative low-level disk-write count (data pages
// plus meta pages across all committed write transactions).
func boltWrites(testDB *db) int64 {
	stats := testDB.bolt.Stats()

	return stats.TxStats.GetWrite()
}

// boltPages returns BoltDB's cumulative page-allocation count across all write
// transactions.
func boltPages(testDB *db) int64 {
	stats := testDB.bolt.Stats()

	return stats.TxStats.GetPageCount()
}

// archiveJobsConcurrently archives every job using benchArchiveConcurrency
// worker goroutines, so the archiveWriter finds a queue of pending archives to
// fold into each transaction, the way it does under the concurrent server
// request load.
//
// On an archiveJob error a worker records it (b.Error is goroutine-safe) and
// sets a shared failed flag, but keeps draining work to completion rather than
// returning early. Returning early would let the unbuffered-channel sender block
// forever once enough workers had exited (a deadlock); draining guarantees the
// sender always makes progress.
func archiveJobsConcurrently(b *testing.B, testDB *db, jobs []*Job) {
	b.Helper()

	ctx := context.Background()
	work := make(chan *Job)

	var (
		wg     sync.WaitGroup
		failed atomic.Bool
	)

	for range benchArchiveConcurrency {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for job := range work {
				if failed.Load() {
					continue
				}

				if err := testDB.archiveJob(ctx, job.Key(), job); err != nil {
					b.Error(err)
					failed.Store(true)
				}
			}
		}()
	}

	for _, job := range jobs {
		work <- job
	}

	close(work)
	wg.Wait()
}
