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

// This file provides a FAST, deterministic, in-process reproducer for the
// reliable4 backup-stall, used to iterate on fixes without needing LSF or real
// runners. It complements developers/wrdev.sh backup-stall-check (the faithful
// end-to-end LSF gate).
//
// It opens a pre-generated record-dense big DB (see TestReliable4InflateDB) via
// the REAL initDB in production mode (backups on), then hammers db.archiveJob
// from many goroutines - exactly what the server's archive RPC handler does when
// runners report completed jobs. Each archiveJob triggers a background backup
// (rate-limited to backupWait); on a big, high-freelist DB those backups make the
// concurrent archive commits stall. archiveJob's own wall-clock latency is
// therefore a direct proxy for the production churn: if it exceeds the job's TTR
// the manager would mark the (already-finished) job lost -> confirm dead -> rerun
// -> "bad job" archive reject. A run that keeps every archive well under the TTR
// is a run that would NOT churn.
//
// Usage (via a wrdev helper, or directly):
//
//	WR_STALL_DB=/path/to/biginflateddb WR_STALL_ARCHIVERS=50 WR_STALL_SECONDS=180 \
//	  go test -tags reliability_repro ./jobqueue/ -run TestReliable4BackupStall -v
//
// The DB is MUTATED (new records archived, backups written); restore a pristine
// copy between runs. Experimental fix knobs (WR_EXP_*) are read by initDB / the
// backup path and let this same test A/B candidate mitigations.

package jobqueue

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	reliable4StallDefaultArchivers = 50
	reliable4StallDefaultSeconds   = 180
	reliable4StallDefaultPauseMs   = 100
	reliable4StallDefaultTTRms     = 30_000 // archive latency over this would churn
	reliable4StallSoftMs           = 5_000  // "concerning" latency threshold
	reliable4StallMonitorInterval  = 5 * time.Second
	reliable4StallCmdPad           = 256
)

// reliable4StallStats accumulates archive latency outcomes across all archivers.
type reliable4StallStats struct {
	total     atomic.Int64
	errors    atomic.Int64
	overSoft  atomic.Int64   // latency > reliable4StallSoftMs
	overHard  atomic.Int64   // latency > TTR (would churn)
	maxNs     atomic.Int64   // max latency ever
	windowMax atomic.Int64   // max latency this monitor window (reset each tick)
	lastDone  atomic.Int64   // UnixNano of the most recent archive completion
	inflight  []atomic.Int64 // per-archiver UnixNano start of the in-progress archiveJob (0 = idle)
}

func (s *reliable4StallStats) record(lat time.Duration, ttr time.Duration, err error) {
	s.total.Add(1)
	s.lastDone.Store(time.Now().UnixNano())

	if err != nil {
		s.errors.Add(1)
	}

	ns := lat.Nanoseconds()

	for {
		m := s.maxNs.Load()
		if ns <= m || s.maxNs.CompareAndSwap(m, ns) {
			break
		}
	}

	for {
		m := s.windowMax.Load()
		if ns <= m || s.windowMax.CompareAndSwap(m, ns) {
			break
		}
	}

	if lat > reliable4StallSoftMs*time.Millisecond {
		s.overSoft.Add(1)
	}

	if lat > ttr {
		s.overHard.Add(1)
	}
}

func TestReliable4BackupStall(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbFile := os.Getenv("WR_STALL_DB")
	if dbFile == "" {
		t.Skip("set WR_STALL_DB to a pre-generated big DB (see TestReliable4InflateDB)")

		return
	}

	archivers := envIntDefault("WR_STALL_ARCHIVERS", reliable4StallDefaultArchivers)
	seconds := envIntDefault("WR_STALL_SECONDS", reliable4StallDefaultSeconds)
	pauseMs := envIntDefault("WR_STALL_PAUSE_MS", reliable4StallDefaultPauseMs)
	ttr := time.Duration(envIntDefault("WR_STALL_TTR_MS", reliable4StallDefaultTTRms)) * time.Millisecond

	ctx := context.Background()
	dbBk := dbFile + "_bk"
	_ = os.Remove(dbBk)
	_ = os.Remove(dbBk + ".tmp")

	db := openReliable4StallDB(t, ctx, dbFile, dbBk)
	defer func() { _ = db.close(ctx) }()

	reportReliable4OpenDB(t, db)

	t.Logf("STALL: %d archivers, pause %dms, for %ds, TTR=%s; backup path=%s",
		archivers, pauseMs, seconds, ttr, dbBk)

	stats := &reliable4StallStats{inflight: make([]atomic.Int64, archivers)}
	stop := make(chan struct{})

	var wg sync.WaitGroup

	// a per-run key nonce so newly-archived keys never collide with the generated
	// records or with a previous run.
	nonce := time.Now().UnixNano()

	var counter atomic.Int64

	pause := time.Duration(pauseMs) * time.Millisecond

	for a := 0; a < archivers; a++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			reliable4StallArchiver(ctx, db, stats, stop, &counter, nonce, ttr, pause, id)
		}(a)
	}

	if os.Getenv("WR_STALL_DUMP") == "1" {
		wg.Add(1)

		go func() {
			defer wg.Done()

			reliable4StallWatchdog(t, stats, stop)
		}()
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		reliable4BackupTimer(t, dbBk, stop)
	}()

	runReliable4StallMonitor(t, db, stats, dbBk, seconds)

	close(stop)
	wg.Wait()

	reportReliable4StallVerdict(t, stats, ttr)
}

// openReliable4StallDB opens the pre-generated DB in production mode (backups
// enabled) via the real initDB, so this exercises the exact code path the manager
// uses.
func openReliable4StallDB(t *testing.T, ctx context.Context, dbFile, dbBk string) *db {
	database, msg, err := initDB(ctx, dbFile, dbBk, internal.Production, false, false)
	if err != nil {
		t.Fatalf("initDB(%s) failed: %v", dbFile, err)
	}

	if msg != "" {
		t.Logf("STALL: initDB: %s", msg)
	}

	return database
}

// reportReliable4OpenDB logs the opened DB's size and freelist, confirming the
// large persisted freelist reloaded as free pages.
func reportReliable4OpenDB(t *testing.T, db *db) {
	stats := db.bolt.Stats()
	pageSize := db.bolt.Info().PageSize
	freeMiB := int64(stats.FreePageN) * int64(pageSize) / reliable4MiB

	if fi, err := os.Stat(db.bolt.Path()); err == nil {
		t.Logf("STALL: opened DB file=%.2fGiB freelist=%d pages (~%dMiB) pending=%d",
			float64(fi.Size())/float64(reliable4GiB), stats.FreePageN, freeMiB, stats.PendingPageN)
	}
}

// reliable4StallArchiver repeatedly archives a fresh unique job, recording each
// archiveJob's latency, until stop is closed.
func reliable4StallArchiver(ctx context.Context, db *db, stats *reliable4StallStats,
	stop <-chan struct{}, counter *atomic.Int64, nonce int64, ttr, pause time.Duration, id int,
) {
	pad := reliable4Pad(reliable4StallCmdPad)

	for {
		select {
		case <-stop:
			return
		default:
		}

		i := counter.Add(1)
		job := reliable4StallJob(nonce, i, pad)
		key := job.Key()

		t0 := time.Now()
		stats.inflight[id].Store(t0.UnixNano())
		err := db.archiveJob(ctx, key, job)
		stats.inflight[id].Store(0)
		stats.record(time.Since(t0), ttr, err)

		if pause > 0 {
			select {
			case <-stop:
				return
			case <-time.After(pause):
			}
		}
	}
}

// reliable4StallJob builds a fresh completed job with a key that is unique to
// this run (nonce) and archiver iteration (i), so archiving it adds a new record
// (as production archives do), growing the DB during the run.
func reliable4StallJob(nonce, i int64, pad string) *Job {
	cmd := fmt.Sprintf("reliable4stall %d-%d %s", nonce, i, pad)
	now := time.Now()

	return &Job{
		Cmd:      cmd,
		Cwd:      "/tmp",
		RepGroup: "reliable4stall",
		ReqGroup: "reliable4stall",
		Requirements: &scheduler.Requirements{
			RAM: 100, Time: time.Hour, Cores: 1, Disk: 1,
		},
		State:     JobStateComplete,
		Exited:    true,
		Host:      "reliable4-host",
		StartTime: now.Add(-time.Minute),
		EndTime:   now,
	}
}

// runReliable4StallMonitor prints per-interval progress (archive count, rate,
// window-max latency, and whether a backup temp file is present) for `seconds`.
func runReliable4StallMonitor(t *testing.T, db *db, stats *reliable4StallStats, dbBk string, seconds int) {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	ticker := time.NewTicker(reliable4StallMonitorInterval)
	defer ticker.Stop()

	prevTotal := int64(0)
	start := time.Now()

	for time.Now().Before(deadline) {
		<-ticker.C

		total := stats.total.Load()
		windowMax := time.Duration(stats.windowMax.Swap(0))
		rate := float64(total-prevTotal) / reliable4StallMonitorInterval.Seconds()
		prevTotal = total

		backingUp := reliable4BackupInProgress(db, dbBk)

		t.Logf("STALL t+%3.0fs archives=%d (%.0f/s) windowMaxLat=%s overSoft=%d overHard=%d errors=%d backup=%v",
			time.Since(start).Seconds(), total, rate, windowMax.Round(time.Millisecond),
			stats.overSoft.Load(), stats.overHard.Load(), stats.errors.Load(), backingUp)
	}
}

// reliable4BackupTimer watches the temp backup file to time each backup copy's
// wall-clock duration, so we can see whether a mitigation (e.g. incremental
// fsync) bloats the copy. It logs each completed backup's duration.
func reliable4BackupTimer(t *testing.T, dbBk string, stop <-chan struct{}) {
	tmp := dbBk + ".tmp"
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var (
		started time.Time
		n       int
	)

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		_, err := os.Stat(tmp)
		present := err == nil

		switch {
		case present && started.IsZero():
			started = time.Now()
		case !present && !started.IsZero():
			n++
			t.Logf("BACKUP#%d copy took %s", n, time.Since(started).Round(time.Millisecond))
			started = time.Time{}
		}
	}
}

// reliable4BackupInProgress reports whether a backup is currently running, via
// the db flag and the presence of the temp backup file.
func reliable4BackupInProgress(db *db, dbBk string) bool {
	db.RLock()
	flag := db.backingUp
	db.RUnlock()

	if flag {
		return true
	}

	_, err := os.Stat(dbBk + ".tmp")

	return err == nil
}

// reportReliable4StallVerdict prints the final verdict: a run with any
// over-TTR archive would churn in production.
func reportReliable4StallVerdict(t *testing.T, stats *reliable4StallStats, ttr time.Duration) {
	maxLat := time.Duration(stats.maxNs.Load())

	t.Logf("STALL VERDICT: archives=%d maxLat=%s overSoft(>%s)=%d overHard(>TTR %s)=%d errors=%d",
		stats.total.Load(), maxLat.Round(time.Millisecond),
		(reliable4StallSoftMs * time.Millisecond).Round(time.Millisecond), stats.overSoft.Load(),
		ttr, stats.overHard.Load(), stats.errors.Load())

	if stats.overHard.Load() > 0 {
		t.Logf("STALL REPRODUCED: %d archive(s) exceeded the TTR (%s) - these jobs would be falsely lost and churn (maxLat=%s)",
			stats.overHard.Load(), ttr, maxLat.Round(time.Millisecond))
	} else {
		t.Logf("NO STALL: every archive completed within the TTR (maxLat=%s) - no churn",
			maxLat.Round(time.Millisecond))
	}
}

// reliable4StallWatchdog (WR_STALL_DUMP=1) samples archive progress; when it
// stalls (no archive completes for >reliable4StallDumpThreshold) it captures ALL
// goroutine stacks - the decisive evidence of WHERE writers block during a backup
// (mmaplock remap vs fdatasync/NFS I/O). It dumps at most a few times.
func reliable4StallWatchdog(t *testing.T, stats *reliable4StallStats, stop <-chan struct{}) {
	const (
		sample    = 250 * time.Millisecond
		threshold = 2 * time.Second
		maxDumps  = 4
	)

	ticker := time.NewTicker(sample)
	defer ticker.Stop()

	dumps := 0
	dumpedThisFreeze := false

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		// find the longest-running in-flight archiveJob right now.
		now := time.Now().UnixNano()

		var longest time.Duration

		for i := range stats.inflight {
			start := stats.inflight[i].Load()
			if start == 0 {
				continue
			}

			if d := time.Duration(now - start); d > longest {
				longest = d
			}
		}

		if longest >= threshold {
			if !dumpedThisFreeze && dumps < maxDumps {
				reliable4DumpGoroutines(t, dumps, longest)

				dumps++
				dumpedThisFreeze = true
			}
		} else {
			dumpedThisFreeze = false
		}
	}
}

var reliable4GoroutineHeader = regexp.MustCompile(`(?m)^goroutine \d+ \[([^,\]]+)`)

// reliable4DumpGoroutines logs a histogram of goroutine states plus the stacks of
// the goroutines most relevant to the stall (bbolt/db/syscall frames).
func reliable4DumpGoroutines(t *testing.T, seq int, stuckFor time.Duration) {
	buf := make([]byte, 16<<20)
	n := runtime.Stack(buf, true)
	dump := string(buf[:n])

	states := map[string]int{}
	for _, m := range reliable4GoroutineHeader.FindAllStringSubmatch(dump, -1) {
		states[m[1]]++
	}

	type kv struct {
		state string
		count int
	}

	hist := make([]kv, 0, len(states))
	for s, c := range states {
		hist = append(hist, kv{s, c})
	}

	sort.Slice(hist, func(i, j int) bool { return hist[i].count > hist[j].count })

	var sb strings.Builder
	for _, h := range hist {
		fmt.Fprintf(&sb, "%s=%d ", h.state, h.count)
	}

	t.Logf("DUMP#%d (stuck ~%s): goroutine states: %s", seq, stuckFor.Round(time.Millisecond), sb.String())

	if path := os.Getenv("WR_STALL_DUMP_FILE"); path != "" {
		f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if ferr == nil {
			fmt.Fprintf(f, "\n===== DUMP#%d stuck ~%s =====\n%s\n", seq, stuckFor, dump)
			_ = f.Close()
		}
	}

	// Surface the goroutines that are actually WORKING (syscall/running/semacquire),
	// i.e. the stuck committer and the backup copy - not the 80 archivers parked in
	// Batch's chan receive.
	for _, g := range reliable4RelevantStacks(dump) {
		t.Logf("DUMP#%d busy stack:\n%s", seq, g)
	}
}

// reliable4RelevantStacks returns the goroutine stack blocks that are NOT parked
// in a plain "chan receive"/"select" wait - i.e. the ones doing the work
// (syscall, running, or blocked acquiring a lock), which is where the stall lives.
func reliable4RelevantStacks(dump string) []string {
	var relevant []string

	for _, block := range strings.Split(dump, "\n\ngoroutine ") {
		header := block
		if i := strings.IndexByte(block, '\n'); i >= 0 {
			header = block[:i]
		}

		// skip the parked archivers/monitor and the watchdog itself.
		if strings.Contains(header, "chan receive") || strings.Contains(header, "select") {
			continue
		}

		if strings.Contains(block, "reliable4DumpGoroutines") || strings.Contains(block, "reliable4StallWatchdog") {
			continue
		}

		// only care about the db/bolt/io path.
		if !strings.Contains(block, "bbolt") && !strings.Contains(block, "jobqueue") &&
			!strings.Contains(block, "syscall") && !strings.Contains(block, "internal/poll") {
			continue
		}

		relevant = append(relevant, "goroutine "+block)
		if len(relevant) >= 10 {
			break
		}
	}

	return relevant
}
