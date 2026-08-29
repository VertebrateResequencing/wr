//go:build reliability_repro

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

// FAITHFUL in-process reproducer of the FULL reliable4 prod FREEZE (not just the
// goroutine spike): the write-storm STARVES the synchronous archive path past the
// 60s client floor. Self-contained (only stable APIs), so it compiles at both the
// pre-fix and post-fix commits for a clean A/B, and it is SAFE (in-process, no
// manager, no LSF, no real job commands ever execute).
//
// The prod freeze (live pprof, .docs/reliable4/prod-freeze-pprof-diagnosis.md):
// a mass state-change burst spawns one `go db.bolt.Batch` per change; bbolt
// coalescing collapses into thousands of tiny fsync'd write-txns each CPU-bound in
// freelist.Free/spill on the churn-bloated 7.9GB freelist, serialized on the one
// write lock; the SYNCHRONOUS archiveJob queues behind them and blocks >60s ->
// the job is falsely lost -> churn. That per-commit cost is freelist work (CPU),
// so it reproduces in-process on a big-freelist DB (see TestReliable4InflateDB /
// pristine6 ~3.2GB freelist / pristine10 ~4.6GB).
//
// This opens such a DB (WR_WSFREEZE_DB), seeds N live jobs, times db.archiveJob
// from a few "archiver" goroutines (the prod victim), then fires the N-job
// updateJobAfterChange storm and measures how long an archive is starved.
//   PRE-FIX  (commit before the single-writer fix): goroutines explode ~= N AND
//            max archive latency crosses the TTR (freeze) -> the assertions FAIL.
//   POST-FIX (single coalescing writer): goroutines bounded AND archive latency
//            stays well under the TTR -> PASS.
//
// CONFIRMED A/B (pristine10, ~4.6GB freelist, N=100000, identical DB both sides):
//   PRE-FIX (7373697):  +99,106 goroutines, max archive latency 1m13.5s (8 over the
//                       60s floor), storm drain 1m27s -> FAIL (freeze reproduced).
//   POST-FIX (fix):     +1 goroutine, max archive latency 6.9s (0 over the floor),
//                       storm drain 9s -> PASS.
//
// Run on a farm node (needs the big DB + RAM for N goroutines):
//   WR_WSFREEZE_DB=/nfs/hgi/wr/sb10-bigdb/pristine10 WR_WSFREEZE_N=100000 \
//     go test -tags reliability_repro ./jobqueue/ -run TestReliable4WriteStormFreeze -v -timeout 30m

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	wsfDefaultN         = 100000
	wsfDefaultArchivers = 8
	wsfDefaultTTRms     = 60_000 // the ClientMinRequestTimeout floor: an archive over this is falsely lost -> churn
	wsfGoroutineBound   = 512    // post-fix the best-effort write path must add O(1), not O(N), goroutines
	wsfCmdPad           = 256
)

// TestReliable4WriteStormFreeze proves the write-storm starves the synchronous
// archive path past the client-timeout floor on the pre-fix code, and does not on
// the fixed code.
func TestReliable4WriteStormFreeze(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbFile := os.Getenv("WR_WSFREEZE_DB")
	if dbFile == "" {
		t.Skip("set WR_WSFREEZE_DB to a big freelist-bloated DB (see TestReliable4InflateDB; e.g. pristine10)")

		return
	}

	n := wsfEnvInt("WR_WSFREEZE_N", wsfDefaultN)
	archivers := wsfEnvInt("WR_WSFREEZE_ARCHIVERS", wsfDefaultArchivers)
	ttr := time.Duration(wsfEnvInt("WR_WSFREEZE_TTR_MS", wsfDefaultTTRms)) * time.Millisecond

	ctx := context.Background()

	database := wsfOpenBigDB(t, ctx, dbFile)
	defer func() { _ = database.close(ctx) }()

	wsfSeedLiveJobs(t, ctx, database, n)

	// archiver goroutines time db.archiveJob (the prod freeze victim) throughout.
	var (
		maxArchNs atomic.Int64
		archCount atomic.Int64
		overTTR   atomic.Int64
		nonce     = time.Now().UnixNano()
		counter   atomic.Int64
	)

	stop := make(chan struct{})

	var awg sync.WaitGroup

	for a := 0; a < archivers; a++ {
		awg.Add(1)

		go func() {
			defer awg.Done()

			wsfArchiverLoop(ctx, database, stop, &counter, nonce, ttr, &maxArchNs, &archCount, &overTTR)
		}()
	}

	time.Sleep(500 * time.Millisecond) // let the archivers establish a baseline before the storm

	base := runtime.NumGoroutine()

	// FIRE THE STORM: N job-state changes, exactly as a mass un-suspend does
	// (server.go suspendJobs/resumeJobs call updateJobAfterChange per job).
	t0 := time.Now()
	jobs := wsfLiveJobs(n)

	go func() {
		for _, job := range jobs {
			job.State = JobStateReserved
			job.StartTime = time.Now()
			database.updateJobAfterChange(ctx, job)
		}
	}()

	peak := wsfSamplePeakUntilDrained(database, base)
	stormDur := time.Since(t0)

	close(stop)
	awg.Wait()

	added := peak - base
	maxArch := time.Duration(maxArchNs.Load())

	t.Logf("WSFREEZE: N=%d archivers=%d stormDrain=%s peakGoroutines(added)=%d archives=%d maxArchiveLat=%s overTTR(%s)=%d",
		n, archivers, stormDur.Round(time.Second), added, archCount.Load(), maxArch.Round(time.Millisecond),
		ttr, overTTR.Load())

	if maxArch > ttr {
		t.Logf("WRITE-STORM FREEZE REPRODUCED: a synchronous archive was starved %s (> the %s client floor) "+
			"behind the update storm's tiny commits on the bloated freelist -> that job would be falsely lost -> churn",
			maxArch.Round(time.Millisecond), ttr)
	} else {
		t.Logf("NO FREEZE: max archive latency %s stayed under the %s client floor", maxArch.Round(time.Millisecond), ttr)
	}

	if added > wsfGoroutineBound {
		t.Errorf("goroutine explosion: the %d-job storm held %d concurrent best-effort write goroutines (bound %d)",
			n, added, wsfGoroutineBound)
	}

	if maxArch > ttr {
		t.Errorf("archive starvation: a synchronous archive took %s, over the %s client floor (the freeze -> churn)",
			maxArch.Round(time.Millisecond), ttr)
	}
}

// wsfOpenBigDB copies the big DB to scratch (so the pristine one is never mutated)
// and opens the copy in development mode (no wipe, NO backups) so this isolates the
// write-storm from the backup path, via the real initDB.
//
// Scratch is $WRDEV_ROOT when set (developers/wrdev.sh passes it in, and removes
// the copy itself if this process is killed before its own cleanup runs), else the
// source DB's own directory.
func wsfOpenBigDB(t *testing.T, ctx context.Context, dbFile string) *db {
	t.Helper()

	scratch := os.Getenv("WRDEV_ROOT")
	if scratch == "" {
		scratch = filepath.Dir(dbFile)
	}

	work := filepath.Join(scratch, "wsfreeze_work_db")
	_ = os.Remove(work)
	_ = os.Remove(work + "_bk")

	t.Logf("WSFREEZE: copying big DB %s -> %s (mutated by the run)", dbFile, work)

	if err := wsfCopyFile(dbFile, work); err != nil {
		t.Fatalf("failed to copy big DB: %v", err)
	}

	t.Cleanup(func() { _ = os.Remove(work); _ = os.Remove(work + "_bk") })

	database, _, err := initDB(ctx, work, work+"_bk", internal.Development, false, false)
	if err != nil {
		t.Fatalf("initDB(%s) failed: %v", work, err)
	}

	stats := database.bolt.Stats()
	if fi, errs := os.Stat(work); errs == nil {
		t.Logf("WSFREEZE: opened DB file=%.2fGiB freelist=%d pages (~%dMiB)",
			float64(fi.Size())/(1<<30), stats.FreePageN, int64(stats.FreePageN)*int64(database.bolt.Info().PageSize)>>20)
	}

	return database
}

// wsfSeedLiveJobs stores n live jobs (so the storm's updateJobAfterChange guard,
// which only rewrites a key still present in the live bucket, applies) and waits
// for the store to quiesce.
func wsfSeedLiveJobs(t *testing.T, ctx context.Context, database *db, n int) {
	t.Helper()

	jobs := wsfLiveJobs(n)
	if _, _, _, err := database.storeNewJobs(ctx, jobs, false); err != nil {
		t.Fatalf("storeNewJobs failed: %v", err)
	}

	database.wg.Wait(10 * time.Minute)
}

// wsfLiveJobs builds n live jobs with stable keys (so seeding and the storm refer
// to the same live-bucket entries).
func wsfLiveJobs(n int) []*Job {
	pad := wsfPad(wsfCmdPad)
	jobs := make([]*Job, n)

	for i := range jobs {
		jobs[i] = &Job{
			Cmd:          fmt.Sprintf("wsfreeze-live %d %s", i, pad),
			Cwd:          testCwd,
			RepGroup:     "wsfreeze",
			ReqGroup:     "wsfreeze",
			Requirements: &jqs.Requirements{RAM: 100, Time: time.Hour, Cores: 1, Disk: 1},
			State:        JobStateReady,
		}
	}

	return jobs
}

// wsfArchiverLoop times db.archiveJob on fresh unique jobs (the prod freeze
// victim) until stop, folding each latency into the shared max/over-TTR counters.
func wsfArchiverLoop(ctx context.Context, database *db, stop <-chan struct{}, counter *atomic.Int64,
	nonce int64, ttr time.Duration, maxArchNs, archCount, overTTR *atomic.Int64,
) {
	pad := wsfPad(wsfCmdPad)

	for {
		select {
		case <-stop:
			return
		default:
		}

		i := counter.Add(1)
		now := time.Now()
		job := &Job{
			Cmd:          fmt.Sprintf("wsfreeze-archive %d-%d %s", nonce, i, pad),
			Cwd:          testCwd,
			RepGroup:     "wsfreeze",
			ReqGroup:     "wsfreeze",
			Requirements: &jqs.Requirements{RAM: 100, Time: time.Hour, Cores: 1, Disk: 1},
			State:        JobStateComplete,
			Exited:       true,
			Host:         "wsfreeze-host",
			StartTime:    now.Add(-time.Minute),
			EndTime:      now,
		}

		t0 := time.Now()
		_ = database.archiveJob(ctx, job.Key(), job)
		lat := time.Since(t0)

		archCount.Add(1)

		if ns := lat.Nanoseconds(); ns > maxArchNs.Load() {
			for {
				m := maxArchNs.Load()
				if ns <= m || maxArchNs.CompareAndSwap(m, ns) {
					break
				}
			}
		}

		if lat > ttr {
			overTTR.Add(1)
		}
	}
}

// wsfSamplePeakUntilDrained samples the goroutine count until the background
// best-effort writes have drained (db.wg quiescent), returning the peak seen.
func wsfSamplePeakUntilDrained(database *db, base int) int {
	peak := base
	quiescentSince := time.Time{}
	deadline := time.Now().Add(25 * time.Minute)

	for time.Now().Before(deadline) {
		if c := runtime.NumGoroutine(); c > peak {
			peak = c
		}

		// consider drained when the goroutine count has settled back near baseline
		// and stayed there briefly (the storm's writers have all completed).
		if runtime.NumGoroutine() <= base+wsfGoroutineBound {
			if quiescentSince.IsZero() {
				quiescentSince = time.Now()
			} else if time.Since(quiescentSince) > 3*time.Second {
				break
			}
		} else {
			quiescentSince = time.Time{}
		}

		time.Sleep(100 * time.Millisecond)
	}

	database.wg.Wait(5 * time.Minute)

	return peak
}

func wsfEnvInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}

	var i int
	if _, err := fmt.Sscanf(v, "%d", &i); err != nil || i <= 0 {
		return def
	}

	return i
}

func wsfPad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}

	return string(b)
}

func wsfCopyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // test-controlled path
	if err != nil {
		return err
	}

	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	buf := make([]byte, 8<<20)
	if _, err = io.CopyBuffer(out, in, buf); err != nil {
		_ = out.Close()

		return err
	}

	return out.Close()
}
