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

// SCALE GATE for reliable4 FINDING 2 (.docs/reliable4/prod-run-20260817.md): a
// SUSTAINED archive rate on a 10GB-class database must not queue up on the single
// bbolt write lock.
//
// It reproduces the measured production regime in-process, faithfully and safely
// (no LSF, no manager, no real job command ever executes):
//
//   - a 10GB-class, freelist-bloated DB (WR_ARCHRATE_DB; see TestReliable4InflateDB
//     / pristine10), COPIED so the pristine one is never mutated, opened through
//     the real initDB, so each archive commit pays production's real freelist and
//     page cost;
//   - ARCHIVERS concurrent archivers, each doing "run a job then archive it": a
//     JITTERED THINK_MS pause and then one synchronous db.archiveJob, exactly as a
//     runner does. Production had ~660 runners on ~3.8s compress jobs. The jitter
//     is essential, not cosmetic: real runners do not finish in lockstep, and 660
//     archives arriving in the same microsecond all fall inside ONE of bbolt's 10ms
//     batching windows, so they coalesce even without the fix and the gate passes
//     vacuously. Jittered arrivals reproduce production's ~83ms spacing;
//   - which makes the ARRIVAL RATE (archivers / (think + wait)) outrun the
//     TRANSACTION rate, the condition under which bbolt's Batch stops coalescing
//     at all: it detaches its batch the instant one starts, so arrivals further
//     apart than MaxBatchDelay each get their own transaction.
//
// It reports what production reported: archive throughput, mean/p50/p99/max archive
// latency, and archive queue depth (archivers blocked inside archiveJob).
//
// Production, pre-fix, on ~660 runners: queue ~600 deep, ~12 archives/s, MEAN
// archive block 43.0s, tail over the 60s ClientMinRequestTimeout floor (which is
// what put successfully exited compress jobs into `delayed`). Targets for the fix:
// mean < 5s, p99 < 60s.
//
// Driven by developers/wrdev.sh archive-rate, which parses the ARCHRATE-SUMMARY
// line below and FAILS LOUDLY if it is missing or unparseable (a gate that passes
// when nothing was measured is worse than no gate).

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	// arDefaultArchivers mirrors the concurrency at which production's archive
	// queue was measured (~660 running runners).
	arDefaultArchivers = 660

	// arDefaultThinkMs mirrors the production job it was measured on: the compress
	// jobs' Walltime was 3.8s, so each runner archived about that often.
	arDefaultThinkMs = 3800

	// arDefaultSeconds is the measurement window. It must be several times the
	// pre-fix mean block (43s) for the pre-fix steady state to be reached.
	arDefaultSeconds = 180

	// arSampleInterval is how often the archive queue depth is sampled.
	arSampleInterval = time.Second

	// arCmdPad pads each job's Cmd so its record is a realistic size.
	arCmdPad = 256

	// arThinkJitterPercent is how far either side of the think time each archiver's
	// pause is spread, so the archivers do not stay in lockstep (see the header).
	arThinkJitterPercent = 50

	// arSeed seeds each archiver's jitter deterministically, so the gate is
	// repeatable rather than differently-random every run.
	arSeed = 0x5265_6C69_6162_6C34
)

// TestReliable4ArchiveRate drives a sustained archive rate on a big DB and reports
// the archive latency distribution and queue depth.
func TestReliable4ArchiveRate(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbFile := os.Getenv("WR_ARCHRATE_DB")
	if dbFile == "" {
		t.Skip("set WR_ARCHRATE_DB to a big freelist-bloated DB (see TestReliable4InflateDB; e.g. pristine10)")

		return
	}

	archivers := wsfEnvInt("WR_ARCHRATE_ARCHIVERS", arDefaultArchivers)
	think := time.Duration(wsfEnvInt("WR_ARCHRATE_THINK_MS", arDefaultThinkMs)) * time.Millisecond
	window := time.Duration(wsfEnvInt("WR_ARCHRATE_SECONDS", arDefaultSeconds)) * time.Second

	ctx := context.Background()

	database := arOpenBigDB(t, ctx, dbFile)
	defer func() { _ = database.close(ctx) }()

	t.Logf("ARCHRATE: archivers=%d thinkTime=%s window=%s (production: ~660 runners, ~3.8s jobs, "+
		"queue ~600 deep, ~12 archives/s, mean block 43.0s of the %s client floor)",
		archivers, think, window, ClientMinRequestTimeout)

	m := &arMeter{}
	stop := make(chan struct{})

	var wg sync.WaitGroup

	for a := range archivers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			arArchiverLoop(ctx, database, stop, m, a, think)
		}()
	}

	depths := arSampleDepth(t, m, stop, window)

	close(stop)
	wg.Wait()

	arReport(t, m, depths, window, archivers)
}

// arReport prints the ARCHRATE-SUMMARY line the wrdev.sh gate parses, plus a
// verdict. Zero archives is reported as such (never as a pass).
func arReport(t *testing.T, m *arMeter, depths []int64, window time.Duration, archivers int) {
	t.Helper()

	lats := m.latencies()
	if len(lats) == 0 {
		t.Errorf("ARCHRATE-SUMMARY archives=0 NOT-MEASURED: no archive completed in %s, so nothing was "+
			"measured (is the DB readable and writable?)", window)

		return
	}

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })

	var total time.Duration
	for _, lat := range lats {
		total += lat
	}

	mean := total / time.Duration(len(lats))
	p50 := lats[len(lats)*50/100]
	p99 := lats[min(len(lats)*99/100, len(lats)-1)]
	maxLat := lats[len(lats)-1]
	rate := float64(len(lats)) / window.Seconds()
	meanDepth, maxDepth := arDepthStats(depths)
	errs := m.errs.Load()

	t.Logf("ARCHRATE-SUMMARY archivers=%d archives=%d errors=%d rate=%.1f/s meanMs=%d p50Ms=%d p99Ms=%d "+
		"maxMs=%d meanDepth=%d maxDepth=%d overFloor=%d",
		archivers, len(lats), errs, rate, mean.Milliseconds(), p50.Milliseconds(), p99.Milliseconds(),
		maxLat.Milliseconds(), meanDepth, maxDepth, m.overFloor.Load())

	if errs > 0 {
		t.Errorf("ARCHRATE: %d archives errored", errs)
	}
}

// arDepthStats returns the mean and maximum of the sampled queue depths.
func arDepthStats(depths []int64) (int64, int64) {
	if len(depths) == 0 {
		return 0, 0
	}

	var total, maxDepth int64

	for _, d := range depths {
		total += d

		if d > maxDepth {
			maxDepth = d
		}
	}

	return total / int64(len(depths)), maxDepth
}

// arSampleDepth samples the archive queue depth (archivers currently blocked
// inside archiveJob) every arSampleInterval for window, logging progress, and
// returns every sample.
func arSampleDepth(t *testing.T, m *arMeter, stop chan struct{}, window time.Duration) []int64 {
	t.Helper()

	deadline := time.Now().Add(window)
	depths := make([]int64, 0, int(window/arSampleInterval)+1)
	lastCount := int64(0)

	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return depths
		case <-time.After(arSampleInterval):
		}

		depth := m.inFlight.Load()
		depths = append(depths, depth)

		if count := m.count.Load(); len(depths)%10 == 0 {
			t.Logf("ARCHRATE: t+%ds depth=%d archived=%d (+%d in the last 10s)",
				len(depths), depth, count, count-lastCount)

			lastCount = count
		}
	}

	return depths
}

// arMeter collects the archive latency distribution, the queue depth and the
// error count across all archivers.
type arMeter struct {
	mu        sync.Mutex
	lats      []time.Duration
	inFlight  atomic.Int64
	count     atomic.Int64
	errs      atomic.Int64
	overFloor atomic.Int64
}

// record folds one archive's outcome into the meter.
func (m *arMeter) record(lat time.Duration, err error) {
	m.mu.Lock()
	m.lats = append(m.lats, lat)
	m.mu.Unlock()

	m.count.Add(1)

	if err != nil {
		m.errs.Add(1)
	}

	if lat >= ClientMinRequestTimeout {
		m.overFloor.Add(1)
	}
}

// latencies returns a copy of every recorded latency.
func (m *arMeter) latencies() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]time.Duration(nil), m.lats...)
}

// arArchiverLoop is one runner: think for the job's runtime, then synchronously
// archive it, until stop. The archived jobs are fresh and unique, exactly as
// production's completions are (each adds a new complete-bucket record and a new
// end-time index entry).
func arArchiverLoop(ctx context.Context, database *db, stop <-chan struct{}, m *arMeter,
	archiver int, think time.Duration,
) {
	pad := wsfPad(arCmdPad)
	rng := rand.New(rand.NewPCG(arSeed, uint64(archiver))) //nolint:gosec // jitter, not cryptography

	// start each archiver somewhere random within the first think period, so they
	// never begin in lockstep (see arThinkJitterPercent).
	pause := arJitter(rng, think, 100)

	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-time.After(pause):
		}

		pause = arJitter(rng, think, arThinkJitterPercent)

		now := time.Now()
		job := &Job{
			Cmd:          fmt.Sprintf("archrate %d-%d-%d %s", os.Getpid(), archiver, i, pad),
			Cwd:          defaultUploadDir,
			RepGroup:     "archrate",
			ReqGroup:     "archrate",
			Requirements: &jqs.Requirements{RAM: 100, Time: time.Hour, Cores: 1, Disk: 1},
			State:        JobStateComplete,
			Exited:       true,
			Host:         "archrate-host",
			StartTime:    now.Add(-think),
			EndTime:      now,
		}

		m.inFlight.Add(1)

		t0 := time.Now()
		err := database.archiveJob(ctx, job.Key(), job)

		m.inFlight.Add(-1)
		m.record(time.Since(t0), err)
	}
}

// arJitter returns think spread uniformly by up to percent either side of it (or,
// for percent 100, uniformly over the whole [0, think) start-up window).
func arJitter(rng *rand.Rand, think time.Duration, percent int64) time.Duration {
	spread := think * time.Duration(percent) / 100
	if spread <= 0 {
		return think
	}

	if percent >= 100 {
		return time.Duration(rng.Int64N(int64(think) + 1))
	}

	return think - spread + time.Duration(rng.Int64N(int64(2*spread)+1))
}

// arOpenBigDB copies the big DB to scratch (so the pristine one is never mutated)
// and opens the copy through the real initDB in development mode, ie. with backups
// OFF: the 2026-08-17 profiling explicitly ruled the backup out (backup frames were
// absent when the runner-side timeouts fired), so this isolates the write lock.
//
// Scratch is $WRDEV_ROOT when set (developers/wrdev.sh passes it in, and removes
// the copy itself if this process is killed before its own cleanup runs), else the
// source DB's own directory.
func arOpenBigDB(t *testing.T, ctx context.Context, dbFile string) *db {
	t.Helper()

	scratch := os.Getenv("WRDEV_ROOT")
	if scratch == "" {
		scratch = filepath.Dir(dbFile)
	}

	work := filepath.Join(scratch, "archrate_work_db")
	_ = os.Remove(work)
	_ = os.Remove(work + "_bk")

	t.Logf("ARCHRATE: copying big DB %s -> %s (mutated by the run)", dbFile, work)

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
		t.Logf("ARCHRATE: opened DB file=%.2fGiB freelist=%d pages (~%dMiB)",
			float64(fi.Size())/(1<<30), stats.FreePageN,
			int64(stats.FreePageN)*int64(database.bolt.Info().PageSize)>>20)
	}

	return database
}
