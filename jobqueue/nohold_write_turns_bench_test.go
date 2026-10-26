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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// benchTurnsArchiveCost is the artificial cost of each archive write
	// transaction, standing in for an archive commit on NFS (item 6.3d measured
	// a mean of 45 to 75ms there).
	benchTurnsArchiveCost = 20 * time.Millisecond

	// benchTurnsArchiveInterval paces the stream of archives, fast enough that
	// one is always pending while a transaction commits.
	benchTurnsArchiveInterval = 4 * time.Millisecond

	// benchTurnsChangeInterval paces the stream of best-effort writes.
	benchTurnsChangeInterval = 2 * time.Millisecond

	// benchTurnsChangeJobs is how many live jobs the best-effort stream cycles
	// through.
	benchTurnsChangeJobs = 100
)

// BenchmarkAddDuringWriterStreams measures single-job adds (ns/op) while the
// archive writer and the best-effort writer are kept busy by steady streams of
// work, as a busy manager's are, and reports the archives committed per second
// meanwhile. The archive stream draws on a pool of benchJobCount jobs, so run it
// with a fixed -benchtime count (such as 100x) that ends before the pool does.
func BenchmarkAddDuringWriterStreams(b *testing.B) {
	ctx := context.Background()

	rec := newArchiveTxRecorder(b)
	rec.commitCost = benchTurnsArchiveCost
	testDB := newBenchDB(b)

	archives := seedCompletableJobs(b, testDB, "turns-archive")
	changes := makeBenchJobs("turns-change", benchTurnsChangeJobs)

	if _, _, _, err := testDB.storeNewJobs(ctx, changes, false); err != nil {
		b.Fatal(err)
	}

	testDB.wg.Wait(benchDBWaitTimeout)

	stop := make(chan struct{})

	var (
		streams  sync.WaitGroup
		archived atomic.Int64
	)

	streams.Go(func() { benchStreamArchives(b, testDB, archives, stop, &archived) })
	streams.Go(func() { benchStreamChanges(ctx, testDB, changes, stop) })

	b.ResetTimer()

	start := time.Now()
	archivedBefore := archived.Load()

	for i := range b.N {
		job := testDBJob(fmt.Sprintf("echo turns-add %d", i), "bench")
		if _, _, _, err := testDB.storeNewJobs(ctx, []*Job{job}, false); err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()

	b.ReportMetric(float64(archived.Load()-archivedBefore)/time.Since(start).Seconds(), "archives/s")

	close(stop)
	streams.Wait()
}

// benchStreamArchives archives jobs one every benchTurnsArchiveInterval, each in
// its own goroutine, until stop is closed or jobs run out, counting those that
// succeed.
func benchStreamArchives(b *testing.B, testDB *db, jobs []*Job, stop chan struct{}, archived *atomic.Int64) {
	b.Helper()

	var wg sync.WaitGroup

	defer wg.Wait()

	ticker := time.NewTicker(benchTurnsArchiveInterval)
	defer ticker.Stop()

	for _, job := range jobs {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		wg.Go(func() {
			if err := testDB.archiveJob(job.Key(), job); err != nil {
				b.Error(err)

				return
			}

			archived.Add(1)
		})
	}
}

// benchStreamChanges queues a best-effort write of one of jobs every
// benchTurnsChangeInterval until stop is closed.
func benchStreamChanges(ctx context.Context, testDB *db, jobs []*Job, stop chan struct{}) {
	ticker := time.NewTicker(benchTurnsChangeInterval)
	defer ticker.Stop()

	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		testDB.updateJobAfterChange(ctx, jobs[i%len(jobs)])
	}
}
