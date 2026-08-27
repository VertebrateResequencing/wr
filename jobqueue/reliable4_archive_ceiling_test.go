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

// Reproducer for the SYMPTOM measured in production on 2026-08-27, 07:58-08:08
// (.docs/reliable4/prod-validation-260827.md), rather than for any particular
// mechanism that might remove it:
//
//  1. archive RPC latency walked up to the 60s ClientMinRequestTimeout floor and
//     pinned there (max per minute 11.0s -> 29.4s -> 47.7s -> 51.6s -> 59.9s);
//  2. 470 runner-side `failed to update server with cmd's final state`
//     err="receive time out" in the single minute 08:06 - completed work whose
//     report was lost, which is an archive that took longer than the client floor;
//  3. throughput did not scale with concurrency: ~8.7 completions/s at ~20
//     runners, ~14/s at 1,143 - 57x the concurrency for ~1.6x the throughput.
//
// It runs TWO phases against the same database - a LOW-concurrency phase and a
// HIGH-concurrency one - because a ratio between them is far less sensitive to
// this host's load than an absolute latency is, and because the ratio is what
// production actually reported. Each archiver does think-then-synchronously-
// archive with a JITTERED think time, exactly as arArchiverLoop does (see the
// archive-rate gate's header for why the jitter is load-bearing), so each phase's
// OFFERED rate is archivers/think and its ACHIEVED rate is what the write path
// could absorb.
//
// The ingredients are switchable, because which of them is REQUIRED to reproduce
// the symptom is itself the open question (`archive-rate` at 660 archivers on the
// same 7.4GB DB reached 172/s, mean 41ms, nothing over the floor - so the big DB
// and the archive code path alone do not reproduce it):
//
//   - WR_AC_WORK: where the DB copy and its backup live, so the same run can be
//     taken on NFS (production's filesystem) or on local disk;
//   - WR_AC_BACKUP=1: open the copy with backups FORCED ON, so the periodic
//     full-file backup copy streams the whole DB continuously, as production's
//     does (db_bk.tmp is perpetually being rewritten there).
//
// The backup ingredient is MEASURED, not assumed: acBackupWatcher samples the
// backup temp file's size for the whole run and reports the bytes it saw written
// and how many copies started, so a run whose backup never streamed is reported
// as such instead of passing cheaply.
//
// Driven by developers/wrdev.sh archive-ceiling, which parses the ARCHCEIL-PHASE
// and ARCHCEIL-SUMMARY lines below and FAILS LOUDLY if any is missing or
// unparseable.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	// acDefaultLow is the low-concurrency phase's archiver count, mirroring the
	// production limit of 20 that sustained ~8.7 completions/s.
	acDefaultLow = 20

	// acDefaultHigh is the high-concurrency phase's archiver count, mirroring the
	// 1,143 concurrent runners LSF filled the raised limit with.
	acDefaultHigh = 1143

	// acDefaultThinkMs is each archiver's think time, ie. the modelled command
	// runtime. Production's 20 runners completed ~8.7 jobs/s, so a job took ~2.3s;
	// that fixes both phases' offered rate at archivers/think.
	acDefaultThinkMs = 2300

	// acDefaultSeconds is each phase's measurement window.
	acDefaultSeconds = 180

	// acSampleInterval is how often the archive queue depth and the backup temp
	// file's size are sampled.
	acSampleInterval = time.Second

	// acDefaultCmdBytes pads each job's Cmd so its record is a realistic size. It is
	// the SIZE OF THE RECORD each archive writes, which is an ingredient in its own
	// right: production's portal_builder commands are ~25KB (the reliable4 log-volume
	// work measured them), so each of its completions writes ~40x the bytes a short
	// command does, and batching amortises a transaction's fixed cost but not the
	// bytes its members carry. WR_AC_CMD_BYTES varies it.
	acDefaultCmdBytes = 256

	// acJitterPercent spreads each archiver's think time either side of the mean so
	// the archivers do not arrive in lockstep.
	acJitterPercent = 50

	// acSeed seeds each archiver's jitter deterministically, so the run is
	// repeatable rather than differently-random every time.
	acSeed = 0x4172_6368_4365_696C
)

// acPhaseResult is one phase's measurement.
type acPhaseResult struct {
	name        string
	archivers   int
	archives    int
	offeredRate float64
	rate        float64
	mean        time.Duration
	p50         time.Duration
	p99         time.Duration
	maxLat      time.Duration
	overFloor   int64
	errs        int64
	meanDepth   int64
	maxDepth    int64
}

// TestReliable4ArchiveCeiling measures the archive path's achieved throughput and
// latency distribution at LOW and then HIGH archiver concurrency against the same
// database, and reports whether the production symptom (latency at the client
// floor, reports lost, throughput that does not scale) is present.
func TestReliable4ArchiveCeiling(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbFile := os.Getenv("WR_AC_DB")
	if dbFile == "" {
		t.Skip("set WR_AC_DB to a big freelist-bloated DB (see TestReliable4InflateDB; e.g. pristine6)")

		return
	}

	low := wsfEnvInt("WR_AC_LOW", acDefaultLow)
	high := wsfEnvInt("WR_AC_HIGH", acDefaultHigh)
	think := time.Duration(wsfEnvInt("WR_AC_THINK_MS", acDefaultThinkMs)) * time.Millisecond
	window := time.Duration(wsfEnvInt("WR_AC_SECONDS", acDefaultSeconds)) * time.Second
	cmdBytes := wsfEnvInt("WR_AC_CMD_BYTES", acDefaultCmdBytes)
	backups := os.Getenv("WR_AC_BACKUP") == "1"

	ctx := context.Background()

	database, work := acOpenBigDB(t, ctx, dbFile, backups)
	defer func() { _ = database.close(ctx) }()

	t.Logf("ARCHCEIL: lowArchivers=%d highArchivers=%d think=%s window=%s cmdBytes=%d backups=%v "+
		"work=%s clientFloor=%s (production: 20 runners -> 8.7/s, 1143 runners -> ~14/s with latency "+
		"pinned at the floor and 470 reports lost in one minute, on ~25KB commands)",
		low, high, think, window, cmdBytes, backups, work, ClientMinRequestTimeout)

	watcher := newACBackupWatcher(work + "_bk.tmp")
	watcher.start()

	lowResult := acPhase(t, ctx, database, "low", low, think, window, cmdBytes)
	highResult := acPhase(t, ctx, database, "high", high, think, window, cmdBytes)

	copies, bytesWritten, seconds := watcher.stop()

	acReport(t, lowResult, highResult, cmdBytes, copies, bytesWritten, seconds)
}

// acReport prints the ARCHCEIL-SUMMARY line the wrdev.sh gate parses.
func acReport(t *testing.T, low, high acPhaseResult, cmdBytes, backupCopies int, backupBytes int64,
	backupSeconds float64,
) {
	t.Helper()

	scaling := 0.0
	if low.rate > 0 {
		scaling = high.rate / low.rate
	}

	mbPerSec := 0.0
	if backupSeconds > 0 {
		mbPerSec = float64(backupBytes) / backupSeconds / (1 << 20)
	}

	t.Logf("ARCHCEIL-SUMMARY lowArchivers=%d lowRate=%.2f highArchivers=%d highRate=%.2f "+
		"concurrencyFactor=%.1f throughputFactor=%.2f lowMaxMs=%d highMaxMs=%d lowP99Ms=%d highP99Ms=%d "+
		"overFloor=%d errors=%d archives=%d cmdBytes=%d backupCopies=%d backupMb=%d backupMbPerSec=%.1f",
		low.archivers, low.rate, high.archivers, high.rate,
		float64(high.archivers)/float64(max(low.archivers, 1)), scaling,
		low.maxLat.Milliseconds(), high.maxLat.Milliseconds(),
		low.p99.Milliseconds(), high.p99.Milliseconds(),
		low.overFloor+high.overFloor, low.errs+high.errs, low.archives+high.archives,
		cmdBytes, backupCopies, backupBytes>>20, mbPerSec)

	if low.errs+high.errs > 0 {
		t.Errorf("ARCHCEIL: %d archives errored", low.errs+high.errs)
	}
}

// acPhase runs one phase: archivers concurrent think-then-archive loops for
// window, sampling the archive queue depth, and returns the measurement.
func acPhase(t *testing.T, ctx context.Context, database *db, name string, archivers int,
	think, window time.Duration, cmdBytes int,
) acPhaseResult {
	t.Helper()

	m := &arMeter{}
	stop := make(chan struct{})

	var wg sync.WaitGroup

	for a := range archivers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			acArchiverLoop(ctx, database, stop, m, name, a, think, cmdBytes)
		}()
	}

	depths := acSampleDepth(t, m, stop, window, name)

	close(stop)
	wg.Wait()

	return acResult(t, m, depths, name, archivers, think, window)
}

// acResult folds one phase's meter and depth samples into its result, and prints
// the ARCHCEIL-PHASE line for it.
func acResult(t *testing.T, m *arMeter, depths []int64, name string, archivers int,
	think, window time.Duration,
) acPhaseResult {
	t.Helper()

	res := acPhaseResult{
		name:        name,
		archivers:   archivers,
		offeredRate: float64(archivers) / think.Seconds(),
		errs:        m.errs.Load(),
		overFloor:   m.overFloor.Load(),
	}

	res.meanDepth, res.maxDepth = arDepthStats(depths)

	lats := m.latencies()
	res.archives = len(lats)

	if res.archives == 0 {
		t.Logf("ARCHCEIL-PHASE phase=%s archivers=%d archives=0 NOT-MEASURED: no archive completed in %s",
			name, archivers, window)

		return res
	}

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })

	var total time.Duration
	for _, lat := range lats {
		total += lat
	}

	res.mean = total / time.Duration(res.archives)
	res.p50 = lats[res.archives*50/100]
	res.p99 = lats[min(res.archives*99/100, res.archives-1)]
	res.maxLat = lats[res.archives-1]
	res.rate = float64(res.archives) / window.Seconds()

	t.Logf("ARCHCEIL-PHASE phase=%s archivers=%d archives=%d offeredRate=%.2f/s achievedRate=%.2f/s "+
		"efficiency=%.3f meanMs=%d p50Ms=%d p99Ms=%d maxMs=%d meanDepth=%d maxDepth=%d overFloor=%d errors=%d",
		name, archivers, res.archives, res.offeredRate, res.rate, res.rate/res.offeredRate,
		res.mean.Milliseconds(), res.p50.Milliseconds(), res.p99.Milliseconds(), res.maxLat.Milliseconds(),
		res.meanDepth, res.maxDepth, res.overFloor, res.errs)

	return res
}

// acSampleDepth samples the archive queue depth every acSampleInterval for window,
// logging progress, and returns every sample.
func acSampleDepth(t *testing.T, m *arMeter, stop chan struct{}, window time.Duration,
	name string,
) []int64 {
	t.Helper()

	deadline := time.Now().Add(window)
	depths := make([]int64, 0, int(window/acSampleInterval)+1)
	lastCount := int64(0)

	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return depths
		case <-time.After(acSampleInterval):
		}

		depths = append(depths, m.inFlight.Load())

		if count := m.count.Load(); len(depths)%15 == 0 {
			t.Logf("ARCHCEIL: %s t+%ds depth=%d archived=%d (+%d in the last 15s)",
				name, len(depths), m.inFlight.Load(), count, count-lastCount)

			lastCount = count
		}
	}

	return depths
}

// acArchiverLoop is one runner: think for the modelled command runtime, then
// synchronously archive, until stop.
func acArchiverLoop(ctx context.Context, database *db, stop <-chan struct{}, m *arMeter,
	phase string, archiver int, think time.Duration, cmdBytes int,
) {
	pad := wsfPad(cmdBytes)
	rng := rand.New(rand.NewPCG(acSeed, uint64(archiver))) //nolint:gosec // jitter, not cryptography
	pause := arJitter(rng, think, 100)

	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-time.After(pause):
		}

		pause = arJitter(rng, think, acJitterPercent)

		now := time.Now()
		job := &Job{
			Cmd:          fmt.Sprintf("archceil %s %d-%d-%d %s", phase, os.Getpid(), archiver, i, pad),
			Cwd:          defaultUploadDir,
			RepGroup:     "archceil",
			ReqGroup:     "archceil",
			Requirements: &jqs.Requirements{RAM: 100, Time: time.Hour, Cores: 1, Disk: 1},
			State:        JobStateComplete,
			Exited:       true,
			Host:         "archceil-host",
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

// acOpenBigDB copies the big DB to the working root (so the pristine one is never
// mutated) and opens the copy through the real initDB, with backups forced on when
// asked for. The working root is WR_AC_WORK, else WRDEV_ROOT, else the source DB's
// own directory - which is how the same run is taken on NFS or on local disk.
func acOpenBigDB(t *testing.T, ctx context.Context, dbFile string, backups bool) (*db, string) {
	t.Helper()

	scratch := os.Getenv("WR_AC_WORK")
	if scratch == "" {
		scratch = os.Getenv("WRDEV_ROOT")
	}

	if scratch == "" {
		scratch = filepath.Dir(dbFile)
	}

	work := filepath.Join(scratch, "archceil_work_db")

	for _, path := range []string{work, work + "_bk", work + "_bk.tmp"} {
		_ = os.Remove(path)
	}

	t.Logf("ARCHCEIL: copying big DB %s -> %s (mutated by the run)", dbFile, work)

	copyStart := time.Now()
	if err := wsfCopyFile(dbFile, work); err != nil {
		t.Fatalf("failed to copy big DB: %v", err)
	}

	t.Logf("ARCHCEIL: copy took %s", time.Since(copyStart).Round(time.Second))

	t.Cleanup(func() {
		for _, path := range []string{work, work + "_bk", work + "_bk.tmp"} {
			_ = os.Remove(path)
		}
	})

	database, _, err := initDB(ctx, work, work+"_bk", internal.Development, false, backups)
	if err != nil {
		t.Fatalf("initDB(%s) failed: %v", work, err)
	}

	stats := database.bolt.Stats()
	if fi, errs := os.Stat(work); errs == nil {
		t.Logf("ARCHCEIL: opened DB file=%.2fGiB freelist=%d pages (~%dMiB) backupsEnabled=%v",
			float64(fi.Size())/(1<<30), stats.FreePageN,
			int64(stats.FreePageN)*int64(database.bolt.Info().PageSize)>>20, database.backupsEnabled)
	}

	return database, work
}

// acBackupWatcher measures the periodic full-file backup copy from OUTSIDE the
// product, by sampling the size of the temp file it streams into. It is how the
// gate knows the backup ingredient was really present: a run in which no bytes
// were copied did not have it, whatever was asked for.
type acBackupWatcher struct {
	path     string
	stopping chan struct{}
	done     chan struct{}
	started  time.Time
	copies   int
	bytes    int64
}

// newACBackupWatcher returns a watcher for the given backup temp file.
func newACBackupWatcher(path string) *acBackupWatcher {
	return &acBackupWatcher{
		path:     path,
		stopping: make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// start begins sampling.
func (w *acBackupWatcher) start() {
	w.started = time.Now()

	go func() {
		defer close(w.done)

		var last int64

		for {
			select {
			case <-w.stopping:
				return
			case <-time.After(acSampleInterval):
			}

			var size int64
			if fi, err := os.Stat(w.path); err == nil {
				size = fi.Size()
			}

			switch {
			case size > last:
				w.bytes += size - last
			case size < last:
				// the temp file was renamed into place or restarted, so a copy
				// finished and whatever is there now is a new one's beginning.
				w.copies++
				w.bytes += size
			}

			last = size
		}
	}()
}

// stop ends sampling and returns how many copies were seen to finish, how many
// bytes were seen written, and over how many seconds.
func (w *acBackupWatcher) stop() (int, int64, float64) {
	close(w.stopping)
	<-w.done

	return w.copies, w.bytes, time.Since(w.started).Seconds()
}
