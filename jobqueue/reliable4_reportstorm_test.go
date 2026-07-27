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

// This file is a FAITHFUL, in-process LOAD reproducer for the reliable4
// "post-resume report storm" production failure.
//
// PROD FAILURE (from live logs): under a thundering herd of thousands of fast
// jobs sitting behind a small limit group, the manager cannot service RPCs fast
// enough. A runner does reserve -> started executing -> its (sub-second) command
// finishes -> it tries to report the final state and gets err="receive time out"
// -> it reconnects, retries, and the retry is rejected "bad job (not in queue or
// correct sub-queue)" -> "will need to be rerun". One prod runner did 99 jobs, 0
// completions, 21 reconnects; the manager logged 910 `jstart: bad job` but only
// 291 `jarchive` total. Net: successful fast commands are never recorded and are
// re-run forever, and `complete` does not advance.
//
// The essential ingredients, all reproduced here in-process (no LSF, no manager
// process, no runner subprocess):
//   - N jobs behind ONE shared limit group across a few sibling scheduler/memory
//     groups (like prod's results_portal:2000), so only LIMIT run at once behind
//     a big ready backlog and the manager pays the rac scheduling cost.
//   - M concurrent real Connect()ed client "runners", each tight-looping
//     ReserveScheduled -> Started(os.Getpid()) -> [touch loop] -> [stop touching]
//     -> Archive(success), cycling FAST (sub-second commands), classifying every
//     RPC's outcome (accepted / bad job / must-reserve / receive-time-out /
//     other) exactly as the real runner's reportFinalState retry loop would.
//   - an OPTIONAL big-DB confound (WR_RS_DB): serve() opens a mutable COPY of a
//     pre-generated big DB with backups on, so archive commits have realistic
//     latency and the periodic full-file backup can stall them (as in
//     TestReliable4BackupStall). The pre-generated DB is at e.g.
//     /nfs/hgi/wr/sb10-bigdb/pristine10.
//
// The server is configured like prod as far as in-process allows (real ItemTTR,
// a 15s-style touch loop) and is given the "mock" scheduler with a non-empty
// RunnerCmd so ALL the server-side scheduling/limit/reserve-group machinery
// engages exactly as in production - but the scheduled runners are INERT (the
// RunnerFunc just parks), so OUR M goroutines are the only load.
//
// Run via developers/wrdev.sh report-storm, or directly:
//
//	WR_RS_JOBS=5000 WR_RS_RUNNERS=1000 WR_RS_LIMIT=2000 WR_RS_SECONDS=120 \
//	  [WR_RS_DB=/nfs/hgi/wr/sb10-bigdb/pristine10] \
//	  go test -tags reliability_repro ./jobqueue/ -run TestReliable4ReportStorm -v

package jobqueue

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	reliable4RSDefaultJobs    = 5000
	reliable4RSDefaultRunners = 200
	reliable4RSDefaultLimit   = 2000
	reliable4RSDefaultSeconds = 120
	reliable4RSDefaultTTRms   = 60_000 // real production ItemTTR
	reliable4RSSiblingGroups  = 4      // distinct memory/scheduler groups sharing the one limit
	reliable4RSReserveTimeout = 200 * time.Millisecond
	reliable4RSReportRetry    = 30 * time.Second // how long a runner retries its final-state report
	reliable4RSMonitorEvery   = 5 * time.Second
	reliable4RSGroupWaitMax   = 60 * time.Second // wait for the first rac to assign reserve groups
	reliable4RSInflightSample = 200 * time.Millisecond
	reliable4RSRunnerCmd      = "reportstormrunner %s %s %s %s %d %d"
	reliable4RSLimitGroup     = "reportstorm"
	reliable4RSRepGroup       = "reliable4_reportstorm_rg"
)

// reliable4RSParams holds the parsed knobs for one report-storm run.
type reliable4RSParams struct {
	jobs        int
	runners     int
	limit       int
	seconds     int
	ttr         time.Duration
	cmdDuration time.Duration // simulated command runtime (default 0 = pure storm)
	dbFile      string        // WR_RS_DB: a big DB to open a copy of (empty = fresh small DB)
	profileDir  string        // WR_RS_PROFILE_DIR: write cpu/mutex/block pprof profiles here (empty = off)
	statusPoll  int           // WR_RS_STATUS: concurrent `wr status`-style pollers (0 = off)
	statusEvery time.Duration // WR_RS_STATUS_MS: how often each poller scans (default 500ms)
}

// suffix builds a stable per-config profile-file suffix, e.g.
// "j50000_r1000_l2000_bigdb_status8".
func (p reliable4RSParams) suffix() string {
	tag := ""
	if p.dbFile != "" {
		tag += "_bigdb"
	}

	if p.statusPoll > 0 {
		tag += fmt.Sprintf("_status%d", p.statusPoll)
	}

	return fmt.Sprintf("j%d_r%d_l%d%s", p.jobs, p.runners, p.limit, tag)
}

// reliable4RSOutcome classifies the results of one kind of RPC (started, touch,
// archive or release) across the whole runner pool.
type reliable4RSOutcome struct {
	accepted    atomic.Int64 // no error
	badjob      atomic.Int64 // ErrBadJob: the item moved out of the run sub-queue (the churn signal)
	mustReserve atomic.Int64 // ErrMustReserve: re-reserved by someone else
	timeout     atomic.Int64 // "receive time out" / transport error (the RPC could not be serviced)
	otherErr    atomic.Int64
}

func (o *reliable4RSOutcome) classify(err error) {
	switch {
	case err == nil:
		o.accepted.Add(1)
	case reliable4RSErrContains(err, ErrBadJob) || reliable4RSErrContains(err, ErrBadRequest):
		o.badjob.Add(1)
	case reliable4RSErrContains(err, ErrMustReserve):
		o.mustReserve.Add(1)
	case reliable4RSErrContains(err, "receive time out") || reliable4RSErrContains(err, "time out") ||
		reliable4RSErrContains(err, ErrNoServer) || reliable4RSErrContains(err, "connection"):
		o.timeout.Add(1)
	default:
		o.otherErr.Add(1)
	}
}

func (o *reliable4RSOutcome) String() string {
	return fmt.Sprintf("accepted=%d badjob=%d mustReserve=%d timeout=%d otherErr=%d",
		o.accepted.Load(), o.badjob.Load(), o.mustReserve.Load(), o.timeout.Load(), o.otherErr.Load())
}

func reliable4RSErrContains(err error, sub string) bool {
	return err != nil && strings.Contains(err.Error(), sub)
}

// reliable4RSStats accumulates all metrics for one run.
type reliable4RSStats struct {
	started   reliable4RSOutcome
	touch     reliable4RSOutcome
	archive   reliable4RSOutcome
	release   reliable4RSOutcome
	reconnect atomic.Int64
	reserves  atomic.Int64 // reserve calls that returned a job

	lat      reliable4RSLatHist // archive-call latency distribution
	maxLatNs atomic.Int64

	status         reliable4RSOutcome // concurrent `wr status`-style poll outcomes
	statusLat      reliable4RSLatHist // status-call latency distribution
	statusCalls    atomic.Int64
	maxStatusLatNs atomic.Int64

	// inflight[id] is the UnixNano start of the report RPC (Started/Archive) that
	// runner id is currently blocked in, or 0 when idle. Sampling max(now-start)
	// across it shows head-of-line blocking forming even if the run still finishes.
	inflight      []atomic.Int64
	maxInflightNs atomic.Int64

	mu        sync.Mutex
	completed map[string]bool // distinct job keys whose success the server accepted
}

// beginInflight records that runner id has just started a report RPC.
func (s *reliable4RSStats) beginInflight(id int) {
	if id < len(s.inflight) {
		s.inflight[id].Store(time.Now().UnixNano())
	}
}

// endInflight records that runner id's report RPC has returned.
func (s *reliable4RSStats) endInflight(id int) {
	if id < len(s.inflight) {
		s.inflight[id].Store(0)
	}
}

// sampleMaxInflight returns the longest currently-in-flight report RPC and folds
// it into the run's global maximum.
func (s *reliable4RSStats) sampleMaxInflight() time.Duration {
	now := time.Now().UnixNano()

	var maxNs int64

	for i := range s.inflight {
		start := s.inflight[i].Load()
		if start == 0 {
			continue
		}

		if d := now - start; d > maxNs {
			maxNs = d
		}
	}

	for {
		m := s.maxInflightNs.Load()
		if maxNs <= m || s.maxInflightNs.CompareAndSwap(m, maxNs) {
			break
		}
	}

	return time.Duration(maxNs)
}

func (s *reliable4RSStats) markCompleted(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.completed[key] = true
}

func (s *reliable4RSStats) completedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.completed)
}

func (s *reliable4RSStats) recordArchiveLatency(d time.Duration) {
	s.lat.record(d)
	reliable4RSStoreMax(&s.maxLatNs, d)
}

// reliable4RSLatBuckets is the number of (non-overflow) archive-latency buckets.
const reliable4RSLatBuckets = 15

// reliable4RSLatBucketsMs are the inclusive upper bounds (in ms) of the archive
// latency histogram buckets; a final overflow bucket counts anything larger.
//
//nolint:gochecknoglobals // an immutable lookup table for the latency histogram
var reliable4RSLatBucketsMs = [reliable4RSLatBuckets]int64{
	1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000,
}

// reliable4RSLatHist is a concurrent, memory-bounded latency histogram: atomic
// per-bucket counters, so recording on the hot archive path is lock-free and we
// never store millions of individual samples.
type reliable4RSLatHist struct {
	buckets [reliable4RSLatBuckets + 1]atomic.Int64
}

func (h *reliable4RSLatHist) record(d time.Duration) {
	ms := d.Milliseconds()
	for i, bound := range reliable4RSLatBucketsMs {
		if ms <= bound {
			h.buckets[i].Add(1)

			return
		}
	}

	h.buckets[len(reliable4RSLatBucketsMs)].Add(1)
}

// percentileMs returns the upper bound (in ms) of the bucket that contains the
// p-th percentile (p in [0,1]); the overflow bucket reports as >60000.
func (h *reliable4RSLatHist) percentileMs(p float64) string {
	var total int64
	for i := range h.buckets {
		total += h.buckets[i].Load()
	}

	if total == 0 {
		return "n/a"
	}

	target := int64(p * float64(total))

	var cum int64

	for i := range h.buckets {
		cum += h.buckets[i].Load()
		if cum >= target {
			if i == len(reliable4RSLatBucketsMs) {
				return ">60000ms"
			}

			return fmt.Sprintf("<=%dms", reliable4RSLatBucketsMs[i])
		}
	}

	return ">60000ms"
}

// reliable4RSProfiler captures CPU, mutex and block profiles for one run, so the
// serialization point can be pinned by symbol name rather than inferred.
type reliable4RSProfiler struct {
	dir     string
	suffix  string
	cpuFile *os.File
	on      bool
}

// reliable4RSStartProfiling turns on mutex+block sampling and begins a CPU
// profile (all no-ops when dir is empty). It returns a profiler whose stop()
// finishes the CPU profile and writes the mutex+block profiles.
func reliable4RSStartProfiling(t *testing.T, dir string, p reliable4RSParams) *reliable4RSProfiler {
	if dir == "" {
		return &reliable4RSProfiler{}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Logf("REPORTSTORM: could not make profile dir %s: %v", dir, err)

		return &reliable4RSProfiler{}
	}

	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1_000_000) // ~one sample per 1ms of blocking

	suffix := p.suffix()

	f, err := os.Create(filepath.Join(dir, "reportstorm_"+suffix+".cpu.pprof")) //nolint:gosec // test path
	if err != nil {
		t.Logf("REPORTSTORM: could not create cpu profile: %v", err)

		return &reliable4RSProfiler{}
	}

	if err := pprof.StartCPUProfile(f); err != nil {
		t.Logf("REPORTSTORM: could not start cpu profile: %v", err)
		_ = f.Close()

		return &reliable4RSProfiler{}
	}

	t.Logf("REPORTSTORM: profiling ON -> %s/reportstorm_%s.{cpu,mutex,block}.pprof", dir, suffix)

	return &reliable4RSProfiler{dir: dir, suffix: suffix, cpuFile: f, on: true}
}

func (pr *reliable4RSProfiler) stop(t *testing.T) {
	if !pr.on {
		return
	}

	pprof.StopCPUProfile()
	_ = pr.cpuFile.Close()

	for _, name := range []string{"mutex", "block"} {
		prof := pprof.Lookup(name)
		if prof == nil {
			continue
		}

		f, err := os.Create(filepath.Join(pr.dir, "reportstorm_"+pr.suffix+"."+name+".pprof")) //nolint:gosec
		if err != nil {
			continue
		}

		_ = prof.WriteTo(f, 0)
		_ = f.Close()
	}

	runtime.SetMutexProfileFraction(0)
	runtime.SetBlockProfileRate(0)

	t.Logf("REPORTSTORM: profiles written to %s/reportstorm_%s.{cpu,mutex,block}.pprof", pr.dir, pr.suffix)
}

func TestReliable4ReportStorm(t *testing.T) {
	if runnermode || servermode {
		return
	}

	p := reliable4RSParams{
		jobs:        envIntDefault("WR_RS_JOBS", reliable4RSDefaultJobs),
		runners:     envIntDefault("WR_RS_RUNNERS", reliable4RSDefaultRunners),
		limit:       envIntDefault("WR_RS_LIMIT", reliable4RSDefaultLimit),
		seconds:     envIntDefault("WR_RS_SECONDS", reliable4RSDefaultSeconds),
		ttr:         time.Duration(envIntDefault("WR_RS_TTR_MS", reliable4RSDefaultTTRms)) * time.Millisecond,
		cmdDuration: time.Duration(envIntDefault("WR_RS_CMD_MS", 0)) * time.Millisecond,
		dbFile:      os.Getenv("WR_RS_DB"),
		profileDir:  os.Getenv("WR_RS_PROFILE_DIR"),
		statusPoll:  envIntDefault("WR_RS_STATUS", 0),
		statusEvery: time.Duration(envIntDefault("WR_RS_STATUS_MS", 500)) * time.Millisecond,
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = p.ttr
	serverConfig.Timings.TouchInterval = reliable4RSTouchInterval(p.ttr)

	cleanupDB := reliable4RSConfigureDB(t, &serverConfig, p)
	defer cleanupDB()

	// the "runners" the scheduler is asked to launch are inert (they just park):
	// OUR M goroutines below are the only load, but a non-empty RunnerCmd + a real
	// scheduler means the server runs its full scheduling/limit/reserve-group path.
	done := make(chan struct{})
	serverConfig.SchedulerName = "mock"
	serverConfig.RunnerCmd = reliable4RSRunnerCmd
	serverConfig.SchedulerConfig = &jqs.ConfigMock{
		RunnerFunc: func(fnctx context.Context, _ string) {
			select {
			case <-fnctx.Done():
			case <-done:
			}
		},
	}

	server, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	defer server.Stop(ctx, true)

	reliable4RSAddJobs(t, addr, config.ManagerCAFile, config.ManagerCertDomain, token,
		clientConnectTime, standardReqs, p)

	groups := reliable4RSWaitForGroups(t, server, p.jobs)

	t.Logf("REPORTSTORM: jobs=%d runners=%d limit=%s TTR=%s cmd=%s groups=%d bigDB=%q deadline=%ds",
		p.jobs, p.runners, reliable4RSLimitGroup+":"+strconv.Itoa(p.limit), p.ttr, p.cmdDuration,
		len(groups), p.dbFile, p.seconds)

	stats := &reliable4RSStats{
		completed: make(map[string]bool),
		inflight:  make([]atomic.Int64, p.runners),
	}

	profiler := reliable4RSStartProfiling(t, p.profileDir, p)

	var wg sync.WaitGroup

	for r := 0; r < p.runners; r++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			reliable4RSRunner(addr, config.ManagerCAFile, config.ManagerCertDomain, token,
				clientConnectTime, groups, id, stats, p, done)
		}(r)
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		reliable4RSInflightSampler(stats, done)
	}()

	for sp := 0; sp < p.statusPoll; sp++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			reliable4RSStatusPoller(addr, config.ManagerCAFile, config.ManagerCertDomain, token,
				clientConnectTime, stats, p, done)
		}()
	}

	reliable4RSMonitor(t, server, stats, p, done)

	close(done)
	profiler.stop(t)
	wg.Wait()

	reliable4RSVerdict(t, server, stats, p)
}

// reliable4RSInflightSampler folds the current max in-flight report-RPC duration
// into the run's global maximum at a fine cadence, so brief head-of-line blocking
// spikes are caught between the coarser monitor ticks.
func reliable4RSInflightSampler(stats *reliable4RSStats, done <-chan struct{}) {
	ticker := time.NewTicker(reliable4RSInflightSample)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			stats.sampleMaxInflight()
		}
	}
}

// reliable4RSStatusPoller models a user hammering `wr status` / the web UI during
// the storm: it repeatedly issues an all-rep-groups status request, which on the
// server runs the O(backlog) s.q.AllItems() scan (under the queue RWMutex's read
// lock, contending the report writers) plus a full complete-jobs DB read. This is
// the confound the runner storm alone does not exercise; it is the prime suspect
// for how a single report RPC could exceed the client's 60s receive floor in prod.
func reliable4RSStatusPoller(addr, caFile, certDomain string, token []byte, connectTime time.Duration,
	stats *reliable4RSStats, p reliable4RSParams, done <-chan struct{},
) {
	jq, err := Connect(addr, caFile, certDomain, token, connectTime)
	if err != nil {
		return
	}

	defer func() { disconnect(jq) }()

	for {
		select {
		case <-done:
			return
		default:
		}

		t0 := time.Now()
		// empty rep group => server scans ALL live items (addAllQueueJobStatuses),
		// includeComplete => it also reads the complete-jobs history from the DB.
		_, serr := jq.GetStatusByRepGroupMatch("", RepGroupMatchExact, nil, true, false)
		lat := time.Since(t0)

		stats.statusCalls.Add(1)
		stats.statusLat.record(lat)
		stats.status.classify(serr)
		reliable4RSStoreMax(&stats.maxStatusLatNs, lat)

		select {
		case <-done:
			return
		case <-time.After(p.statusEvery):
		}
	}
}

// reliable4RSStoreMax folds d into the max stored in dst.
func reliable4RSStoreMax(dst *atomic.Int64, d time.Duration) {
	ns := d.Nanoseconds()

	for {
		m := dst.Load()
		if ns <= m || dst.CompareAndSwap(m, ns) {
			break
		}
	}
}

// reliable4RSTouchInterval returns a touch interval that keeps a job well within
// its TTR (like the real 15s-touch / 60s-TTR ratio), but never longer than 15s.
func reliable4RSTouchInterval(ttr time.Duration) time.Duration {
	interval := ttr / 4
	if interval > ClientTouchInterval {
		interval = ClientTouchInterval
	}

	if interval <= 0 {
		interval = 50 * time.Millisecond
	}

	return interval
}

// reliable4RSConfigureDB optionally points serve() at a mutable COPY of a big
// pre-generated DB (WR_RS_DB) with backups enabled, so archive commits have
// realistic latency and periodic backups (as in TestReliable4BackupStall). It
// returns a cleanup func that removes the working copy.
func reliable4RSConfigureDB(t *testing.T, serverConfig *ServerConfig, p reliable4RSParams) func() {
	if p.dbFile == "" {
		return func() {}
	}

	scratch := os.Getenv("WRDEV_ROOT")
	if scratch == "" {
		scratch = filepath.Dir(p.dbFile)
	}

	work := filepath.Join(scratch, "reliable4_reportstorm_work_db")
	workBk := work + "_bk"

	_ = os.Remove(work)
	_ = os.Remove(workBk)
	_ = os.Remove(workBk + ".tmp")

	t.Logf("REPORTSTORM: copying big DB %s -> %s (mutated by the run)", p.dbFile, work)

	t0 := time.Now()
	if err := reliable4RSCopyFile(p.dbFile, work); err != nil {
		t.Fatalf("failed to copy big DB %s: %v", p.dbFile, err)
	}

	if fi, errs := os.Stat(work); errs == nil {
		t.Logf("REPORTSTORM: big DB copy of %.2fGiB took %s",
			float64(fi.Size())/float64(reliable4GiB), time.Since(t0).Round(time.Second))
	}

	serverConfig.DBFile = work
	serverConfig.DBFileBackup = workBk
	serverConfig.forceBackups = true  // backups on even for a development deployment
	serverConfig.dontWipeDevDB = true // keep our copy - a development server would otherwise wipe it

	return func() {
		_ = os.Remove(work)
		_ = os.Remove(workBk)
		_ = os.Remove(workBk + ".tmp")
	}
}

func reliable4RSCopyFile(src, dst string) error {
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

// reliable4RSAddJobs adds p.jobs jobs spread across reliable4RSSiblingGroups
// distinct memory groups, all sharing ONE count-limited limit group (so only
// p.limit run at once behind the backlog). Generous Retries keep any re-run job
// re-queuing rather than burying, so a churn spiral would keep churning.
func reliable4RSAddJobs(t *testing.T, addr, caFile, certDomain string, token []byte,
	connectTime time.Duration, reqs *jqs.Requirements, p reliable4RSParams,
) {
	jq, err := Connect(addr, caFile, certDomain, token, connectTime)
	if err != nil {
		t.Fatalf("add-client connect failed: %v", err)
	}

	defer disconnect(jq)

	limitGroup := reliable4RSLimitGroup + ":" + strconv.Itoa(p.limit)

	toAdd := make([]*Job, p.jobs)
	for i := range toAdd {
		// distinct RAM per sibling => distinct requirements => distinct scheduler
		// group, all sharing the one limit group (as prod's siblings do).
		ram := 100 + (i%reliable4RSSiblingGroups)*100
		reqCopy := &jqs.Requirements{
			RAM: ram, Time: reqs.Time, Cores: reqs.Cores, Disk: reqs.Disk, Other: reqs.Other,
		}
		toAdd[i] = &Job{
			Cmd:          fmt.Sprintf("reportstorm %d", i),
			Cwd:          testCwdPath,
			RepGroup:     reliable4RSRepGroup,
			ReqGroup:     reliable4RSRepGroup,
			Requirements: reqCopy,
			LimitGroups:  []string{limitGroup},
			Retries:      250,
		}
	}

	inserts, _, err := jq.Add(toAdd, os.Environ(), true)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if inserts != p.jobs {
		t.Fatalf("expected to add %d jobs, added %d", p.jobs, inserts)
	}
}

// reliable4RSWaitForGroups waits for the first ready-added callback to assign
// each job its real scheduler/reserve group (and to populate the server's
// previously-scheduled-groups so a first ReserveScheduled is not skipped), then
// returns the distinct group names our runners will reserve from.
func reliable4RSWaitForGroups(t *testing.T, server *Server, jobs int) []string {
	deadline := time.Now().Add(reliable4RSGroupWaitMax)

	for time.Now().Before(deadline) {
		server.rpmutex.Lock()
		running := server.racRunning
		server.rpmutex.Unlock()

		groups := reliable4RSDistinctGroups(server)
		if !running && len(groups) >= reliable4RSSiblingGroups && len(server.q.AllItems()) == jobs {
			return groups
		}

		time.Sleep(50 * time.Millisecond)
	}

	groups := reliable4RSDistinctGroups(server)
	if len(groups) == 0 {
		t.Fatalf("no scheduler groups were assigned within %s", reliable4RSGroupWaitMax)
	}

	t.Logf("REPORTSTORM: proceeding with %d group(s) after wait", len(groups))

	return groups
}

// reliable4RSDistinctGroups reads the distinct non-empty reserve groups the
// server has assigned to the live jobs.
func reliable4RSDistinctGroups(server *Server) []string {
	seen := make(map[string]bool)

	for _, item := range server.q.AllItems() {
		job, ok := item.Data().(*Job)
		if !ok {
			continue
		}

		if g := job.getSchedulerGroup(); g != "" {
			seen[g] = true
		}
	}

	groups := make([]string, 0, len(seen))
	for g := range seen {
		groups = append(groups, g)
	}

	sort.Strings(groups)

	return groups
}

// reliable4RSRunner is one pool worker modelling a real wr runner: it Connect()s
// once then tight-loops reserve -> Started(os.Getpid()) -> touch loop -> stop
// touching -> Archive(success), classifying every RPC. It reserves from a fixed
// sibling group (round-robin assigned by id) so the shared limit gates the pool.
func reliable4RSRunner(addr, caFile, certDomain string, token []byte,
	connectTime time.Duration, groups []string, id int, stats *reliable4RSStats,
	p reliable4RSParams, done <-chan struct{},
) {
	jq, err := Connect(addr, caFile, certDomain, token, connectTime)
	if err != nil {
		return
	}

	defer func() { disconnect(jq) }()

	group := groups[id%len(groups)]
	livePid := os.Getpid() // this process IS the runner; its pid is genuinely alive

	for {
		select {
		case <-done:
			return
		default:
		}

		job, rerr := jq.ReserveScheduled(reliable4RSReserveTimeout, group)
		if rerr != nil || job == nil {
			continue
		}

		stats.reserves.Add(1)

		stats.beginInflight(id)
		serr := jq.Started(job, livePid)
		stats.endInflight(id)
		stats.started.classify(serr)

		if serr != nil {
			// a definitive rejection (bad job / must-reserve) means the item moved;
			// a transient failure is retried by the archive stage below anyway.
			continue
		}

		reliable4RSRunOneJob(&jq, job, id, addr, caFile, certDomain, token, connectTime, stats, p, done)
	}
}

// reliable4RSRunOneJob simulates one command: it runs a touch loop for the
// (optional) command duration, stops touching (as the real runner does just
// before its final report), then archives the success via the faithful
// retry-and-reconnect path.
func reliable4RSRunOneJob(jq **Client, job *Job, id int, addr, caFile, certDomain string,
	token []byte, connectTime time.Duration, stats *reliable4RSStats, p reliable4RSParams, done <-chan struct{},
) {
	stopTouch := make(chan struct{})

	var touchWG sync.WaitGroup

	touchWG.Add(1)

	go func() {
		defer touchWG.Done()

		reliable4RSTouchLoop(*jq, job, reliable4RSTouchInterval(p.ttr), stats, stopTouch)
	}()

	if p.cmdDuration > 0 {
		select {
		case <-done:
		case <-time.After(p.cmdDuration):
		}
	}

	// stop touching just before reporting the final state, exactly as the real
	// runner does (client.go Execute: stopTouching <- true, then reportFinalState).
	close(stopTouch)
	touchWG.Wait()

	reliable4RSArchiveWithRetry(jq, job, id, addr, caFile, certDomain, token, connectTime, stats, done)
}

// reliable4RSTouchLoop touches the job every interval until stop is closed,
// modelling a healthy runner keeping its reservation's TTR alive.
func reliable4RSTouchLoop(jq *Client, job *Job, interval time.Duration,
	stats *reliable4RSStats, stop <-chan struct{},
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_, terr := jq.Touch(job)
			stats.touch.classify(terr)
		}
	}
}

// reliable4RSArchiveWithRetry archives the job's success, mirroring the real
// runner's reportFinalState: on a transient failure (receive time out / lost
// connection) it reconnects and retries within reliable4RSReportRetry; on a
// definitive rejection (bad job / bad request) it gives up (the successful work
// is discarded and the job "will need to be rerun" - the churn signal).
func reliable4RSArchiveWithRetry(jq **Client, job *Job, id int, addr, caFile, certDomain string,
	token []byte, connectTime time.Duration, stats *reliable4RSStats, done <-chan struct{},
) {
	jes := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
	retryEnd := time.Now().Add(reliable4RSReportRetry)

	for time.Now().Before(retryEnd) {
		select {
		case <-done:
			return
		default:
		}

		t0 := time.Now()
		stats.beginInflight(id)
		aerr := (*jq).Archive(job, jes)
		stats.endInflight(id)
		stats.recordArchiveLatency(time.Since(t0))
		stats.archive.classify(aerr)

		switch {
		case aerr == nil:
			stats.markCompleted(job.Key())

			return
		case reliable4RSErrContains(aerr, ErrBadJob) || reliable4RSErrContains(aerr, ErrBadRequest):
			return // permanent: the item moved; this success is lost and the job re-runs
		case reliable4RSErrContains(aerr, ErrMustReserve):
			return // someone else owns it now; this success is lost
		default:
			// transient (receive time out / lost connection): reconnect and retry.
			if reliable4RSReconnect(jq, addr, caFile, certDomain, token, connectTime, stats, done) {
				continue
			}

			return
		}
	}
}

// reliable4RSReconnect replaces the runner's client with a fresh connection
// after a transient report failure, counting the reconnect. Returns false if it
// could not reconnect before done.
func reliable4RSReconnect(jq **Client, addr, caFile, certDomain string,
	token []byte, connectTime time.Duration, stats *reliable4RSStats, done <-chan struct{},
) bool {
	disconnect(*jq)
	stats.reconnect.Add(1)

	for {
		select {
		case <-done:
			return false
		default:
		}

		newC, err := Connect(addr, caFile, certDomain, token, connectTime)
		if err == nil {
			*jq = newC

			return true
		}

		select {
		case <-done:
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// reliable4RSMonitor prints progress every few seconds and returns when all jobs
// have completed or the deadline passes.
func reliable4RSMonitor(t *testing.T, server *Server, stats *reliable4RSStats, p reliable4RSParams,
	done <-chan struct{},
) {
	deadline := time.Now().Add(time.Duration(p.seconds) * time.Second)
	ticker := time.NewTicker(reliable4RSMonitorEvery)
	defer ticker.Stop()

	start := time.Now()
	prevCompleted := 0

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		completed := stats.completedCount()
		qs := server.q.Stats()
		rate := float64(completed-prevCompleted) / reliable4RSMonitorEvery.Seconds()
		prevCompleted = completed
		curInflight := stats.sampleMaxInflight()

		t.Logf("REPORTSTORM t+%3.0fs completed=%d/%d (%.0f/s) live=%d[ready=%d run=%d delay=%d bury=%d] "+
			"reserves=%d reconnects=%d maxArchLat=%s inflightNow=%s",
			time.Since(start).Seconds(), completed, p.jobs, rate, qs.Items, qs.Ready, qs.Running,
			qs.Delayed, qs.Buried, stats.reserves.Load(), stats.reconnect.Load(),
			time.Duration(stats.maxLatNs.Load()).Round(time.Millisecond), curInflight.Round(time.Millisecond))
		t.Logf("REPORTSTORM t+%3.0fs   started[%s] archive[%s] touch[%s]",
			time.Since(start).Seconds(), stats.started.String(), stats.archive.String(), stats.touch.String())

		if p.statusPoll > 0 {
			t.Logf("REPORTSTORM t+%3.0fs   status(pollers=%d) calls=%d [%s] maxStatusLat=%s",
				time.Since(start).Seconds(), p.statusPoll, stats.statusCalls.Load(), stats.status.String(),
				time.Duration(stats.maxStatusLatNs.Load()).Round(time.Millisecond))
		}

		if completed >= p.jobs || time.Now().After(deadline) {
			return
		}
	}
}

// reliable4RSVerdict prints the final metrics and the churn verdict: whether all
// N drained cleanly, or churned (successful archives rejected while `complete`
// stalled), naming the dominant failure signal.
func reliable4RSVerdict(t *testing.T, server *Server, stats *reliable4RSStats, p reliable4RSParams) {
	completed := stats.completedCount()
	qs := server.q.Stats()
	maxLat := time.Duration(stats.maxLatNs.Load())

	t.Logf("REPORTSTORM VERDICT: completed=%d/%d live=%d[ready=%d run=%d delay=%d bury=%d] reserves=%d reconnects=%d",
		completed, p.jobs, qs.Items, qs.Ready, qs.Running, qs.Delayed, qs.Buried,
		stats.reserves.Load(), stats.reconnect.Load())
	t.Logf("REPORTSTORM VERDICT: started[%s]", stats.started.String())
	t.Logf("REPORTSTORM VERDICT: archive[%s]", stats.archive.String())
	t.Logf("REPORTSTORM VERDICT: touch[%s]", stats.touch.String())
	t.Logf("REPORTSTORM VERDICT: archive latency max=%s p50=%s p99=%s; max in-flight report RPC=%s",
		maxLat.Round(time.Millisecond), stats.lat.percentileMs(0.50), stats.lat.percentileMs(0.99),
		time.Duration(stats.maxInflightNs.Load()).Round(time.Millisecond))

	if p.statusPoll > 0 {
		t.Logf("REPORTSTORM VERDICT: status(pollers=%d) calls=%d [%s] maxStatusLat=%s p50=%s p99=%s",
			p.statusPoll, stats.statusCalls.Load(), stats.status.String(),
			time.Duration(stats.maxStatusLatNs.Load()).Round(time.Millisecond),
			stats.statusLat.percentileMs(0.50), stats.statusLat.percentileMs(0.99))
	}

	rejected := stats.archive.badjob.Load() + stats.archive.mustReserve.Load()
	timeouts := stats.archive.timeout.Load() + stats.started.timeout.Load() + stats.touch.timeout.Load()

	switch {
	case completed >= p.jobs:
		t.Logf("REPORTSTORM NO CHURN: all %d jobs completed; archive rejections=%d timeouts=%d "+
			"(the manager serviced every report within the client's %s receive floor)",
			p.jobs, rejected, timeouts, ClientMinRequestTimeout)
	case rejected > 0 || timeouts > 0:
		signal := reliable4RSDominantSignal(stats)
		t.Logf("REPORTSTORM CHURN REPRODUCED: only %d/%d completed while attempts kept climbing; "+
			"dominant signal=%s (archive rejected=%d, RPC timeouts=%d). Successful work discarded and re-run.",
			completed, p.jobs, signal, rejected, timeouts)
	default:
		t.Logf("REPORTSTORM INCOMPLETE (no churn signal): only %d/%d completed but no rejections/timeouts - "+
			"the run simply did not finish draining in %ds (raise WR_RS_SECONDS).", completed, p.jobs, p.seconds)
	}
}

// reliable4RSDominantSignal names the most frequent churn/failure signal across
// all report RPCs.
func reliable4RSDominantSignal(stats *reliable4RSStats) string {
	counts := map[string]int64{
		"receive-timeout": stats.archive.timeout.Load() + stats.started.timeout.Load() + stats.touch.timeout.Load(),
		"badjob":          stats.archive.badjob.Load() + stats.started.badjob.Load() + stats.touch.badjob.Load(),
		"mustReserve": stats.archive.mustReserve.Load() + stats.started.mustReserve.Load() +
			stats.touch.mustReserve.Load(),
	}

	best := "none"

	var bestN int64

	for name, n := range counts {
		if n > bestN {
			bestN, best = n, name
		}
	}

	return best
}
