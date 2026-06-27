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
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
)

// benchJobCount is the number of jobs each benchmark iteration operates on.
const benchJobCount = 3000

// benchDBWaitTimeout bounds how long we wait for the database's background
// update goroutines to drain before measuring; it only affects a (logged)
// warning, never correctness, since the underlying wait blocks until all
// goroutines finish.
const benchDBWaitTimeout = 5 * time.Minute

// benchArchiveConcurrency is how many jobs are archived concurrently. archiveJob
// commits via db.bolt.Batch, and BoltDB only coalesces commits from concurrent
// callers (up to its MaxBatchSize/MaxBatchDelay). The live server reaches
// archiveJob from many simultaneous client-request handlers, so archiving
// concurrently here mirrors that critical path; a single-threaded loop would
// instead measure the degenerate one-commit-per-job worst case and could not
// show coalescing at all.
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

	// Pre-build every batch outside the timer so we measure storage, not job
	// construction, and so each batch has unique keys (storeNewJobs would treat
	// duplicate keys as already-added no-ops).
	batches := make([][]*Job, b.N)
	for i := range b.N {
		batches[i] = makeBenchJobs(fmt.Sprintf("add-%d", i), benchJobCount)
	}

	writesBefore := boltWrites(testDB)
	pagesBefore := boltPages(testDB)

	b.ResetTimer()

	for i := range b.N {
		if _, _, _, err := testDB.storeNewJobs(ctx, batches[i], false); err != nil {
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
// its per-ReqGroup stats, in a BoltDB batch. This is the per-job work behind the
// server's archiveCompletedJob, driven concurrently as the server drives it.
// Each iteration freshly seeds benchJobCount live jobs (outside the timer) and
// then archives them all (inside the timer). The reported bolt_writes/job is the
// headline write-coalescing signal for the completion path: a regression that
// stopped batching archive commits would push it up sharply (towards one fsync
// per job).
func BenchmarkArchiveJobs(b *testing.B) {
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
		b.ReportMetric(float64(writesDuringArchive)/float64(jobsTotal), "bolt_writes/job")
		b.ReportMetric(float64(pagesDuringArchive)/float64(jobsTotal), "bolt_pages/job")
	}
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

	start := time.Now()

	for _, job := range jobs {
		job.Lock()
		job.State = JobStateComplete
		job.Exited = true
		job.StartTime = start
		job.EndTime = start.Add(time.Second)
		job.PeakRAM = 100
		job.Unlock()
	}

	testDB.wg.Wait(benchDBWaitTimeout)

	return jobs
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
// worker goroutines, so the per-job archiveJob/bolt.Batch commits can coalesce
// the way they do under the concurrent server request load.
func archiveJobsConcurrently(b *testing.B, testDB *db, jobs []*Job) {
	b.Helper()

	ctx := context.Background()
	work := make(chan *Job)

	var wg sync.WaitGroup

	for range benchArchiveConcurrency {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for job := range work {
				if err := testDB.archiveJob(ctx, job.Key(), job); err != nil {
					b.Error(err)

					return
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
