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

// Reproducer for the production symptom profiled on 2026-08-27 16:35-17:16 on
// farm22-ibackup01 (profiles in /nfs/hgi/wr/sb10-pprof/prof260827/, binary
// v0.37.2 = 11fb939). What the manager's own log recorded over that window:
//
//	33,585 "slow request" warnings (>10s server-side), of which 28,815 were
//	method=add and 4,767 method=jarchive; p50 12.8s, p90 16.8s, max 40.0s;
//	and 20,524 of the slow adds carried selector="jobs=1" - a SINGLE job,
//	writing a handful of small keys, taking over ten seconds.
//
// What the profiles say is happening while that goes on:
//
//   - the process is NOT busy: the 30s CPU profile totals 4.79s, 16% of ONE
//     core on an 8-core host, so nothing is compute-bound;
//   - the goroutine dump has 763 goroutines parked inside bbolt's DB.Batch
//     (524 in db.storeLookups, 131 in db.storeEncodedJobs, 108 in
//     db.storeLimitGroups) and 260 more parked in db.archiveJob;
//   - 29 goroutines are queued in bbolt.(*DB).beginRWTx, ie. 29 WRITE
//     TRANSACTIONS are in flight at once against a database that has exactly
//     one writer;
//   - the mutex profile attributes 4,478s (85%) of the delay on bbolt's writer
//     lock to bbolt.(*batch).run - the DB.Batch path, which in wr is only
//     reachable from storeLookups/storeEncodedJobs/storeLimitGroups/db.store -
//     against 480s (9%) for the archive writer and 295s (6%) for the
//     best-effort writer. The two purpose-built coalescing writers are NOT the
//     contention; the add path is, and the archives queue behind it.
//
// The mechanism those add up to is transaction FRAGMENTATION on the add path.
// One single-job add makes 3-5 separate db.bolt.Batch calls: Server.createJobs
// unconditionally calls storeLimitGroups first (and for a job that carries no
// `group:limit` suffix, or no limit group at all, that map is EMPTY, so it is a
// write transaction that stores nothing whatsoever), and only then does
// db.storeNewJobData fan out one Batch per bucket (bucketRTK, bucketRGs, any
// dep-group lookups, bucketJobsLive). The fan-out's calls are launched together
// and do coalesce with each other, but storeLimitGroups is a SEPARATE, EARLIER,
// SERIAL call, so an add costs about TWO transactions - this reproducer measures
// 1.7 per add at low concurrency, and the block profile has
// Server.storeLimitGroups blocking for 3.65 hours against storeEncodedJobs'
// 3.55, ie. the empty transaction costs about what writing the job costs.
//
// What bbolt cannot do is coalesce ACROSS adds, because batch.run() detaches
// db.batch the instant a batch starts and the next arrival opens a fresh batch
// on a fresh MaxBatchDelay (10ms) timer, whether or not the previous
// transaction has committed. So the transactions pile up instead of merging -
// and every one of them pays a FIXED cost that dwarfs its payload: bbolt
// rewrites the whole freelist (NoFreelistSync is off) and fdatasyncs twice, on
// a 9.1GiB database on NFS. That fixes the database's write-transaction rate at
// a low constant (this reproducer measures ~14/s at BOTH concurrencies), so the
// achievable add rate is that rate divided by transactions-per-add, no matter
// how many clients are waiting, and every waiting client's latency is the fixed
// cost times its queue position. That is why a one-job add takes 12s.
//
// So this reproducer measures the SYMPTOM (client-observed add RPC latency and
// achieved add throughput) and the MECHANISM (write transactions committed per
// job added, and how many goroutines are parked in DB.Batch / queued in
// beginRWTx) side by side, through the REAL server over the REAL socket with
// REAL clients, so that any fix anywhere on the add path shows up and no
// particular fix is assumed.
//
// It runs TWO phases against the same server - LOW then HIGH client
// concurrency - because a ratio between them measured in the same run is far
// less sensitive to this shared host's load than any absolute latency is, and
// because "57x the concurrency for 1.6x the throughput" is how this family of
// faults has previously shown itself. Each client Connect()s once and then
// loops think-then-add-ONE-job with a JITTERED think time, because arrivals in
// lockstep would all land in one of bbolt's 10ms batching windows and coalesce
// even without a fix, which would make the gate pass vacuously.
//
// WR_AS_BATCH_DELAY_MS / WR_AS_BATCH_SIZE are a MUTATION CONTROL, not a fix:
// they call db.setBatchTuning to widen bbolt's coalescing window, which removes
// the fragmentation without touching wr's own code, so the gate can be shown to
// go RED->GREEN on the thing it claims to measure.
//
// Driven by developers/wrdev.sh add-storm, which parses the ADDSTORM-PHASE and
// ADDSTORM-SUMMARY lines below and FAILS LOUDLY if any is missing or
// unparseable.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	// asDefaultLow is the low-concurrency phase's client count. It is small
	// enough that the write path is never queued, so the phase measures the
	// serial cost of an add rather than any queueing.
	asDefaultLow = 20

	// asDefaultHigh is the high-concurrency phase's client count, modelling the
	// 745 client connections the production manager was serving (its goroutine
	// dump has 745 mangos receiver and 745 sender goroutines, with 239 adds and
	// 262 archives in flight between them).
	asDefaultHigh = 700

	// asDefaultThinkMs is each client's think time between adds, ie. the modelled
	// gap between one workflow step finishing and the next add. Production's
	// ~347 concurrently-blocked adds achieved ~28 adds/s, so its offered rate was
	// well above what the write path could absorb; this default offers
	// high/think = 350/s so the high phase is likewise offer-saturated.
	asDefaultThinkMs = 2000

	// asDefaultSeconds is each phase's measurement window.
	asDefaultSeconds = 120

	// asSampleInterval is how often the in-flight add count, the goroutine
	// parking counts and the backup temp file's size are sampled. It matches the
	// 2s sampling of the production capture's samples.csv.
	asSampleInterval = 2 * time.Second

	// asDefaultCmdBytes pads each added job's Cmd so its record has a realistic
	// size. Production's portal_builder commands are ~25KB; the default here is
	// deliberately SMALL, because the point being made is that even a tiny
	// single-job add takes over ten seconds.
	asDefaultCmdBytes = 256

	// asRepGroups is how many distinct RepGroups the adds are spread over. A
	// handful, as production has, so the bucketRGs lookup writes are mostly
	// repeats while the bucketRTK ones are all new.
	asRepGroups = 8

	// asLimitGroup is the one limit group every added job belongs to. It bounds
	// how many runners the mock scheduler is ever asked for (so a growing ready
	// backlog cannot turn into a growing goroutine population), and it makes the
	// add path production-faithful in the other direction too: because the jobs
	// name the group WITHOUT a `:limit` suffix, each add's limitGroups map is
	// EMPTY and db.storeLimitGroups therefore opens a write transaction that
	// stores nothing whatsoever.
	asLimitGroup = "addstorm"

	// asLimit is that group's limit, set once before the phases start.
	asLimit = 50

	// asJitterPercent spreads each client's think time either side of the mean so
	// the clients do not arrive in lockstep.
	asJitterPercent = 50

	// asSeed seeds each client's jitter deterministically, so a run is repeatable
	// rather than differently-random every time.
	asSeed = 0x4164_6453_746F_726D

	// asConnectTimeout is how long a storm client waits to connect. It is
	// generous because 700 TLS handshakes against a busy manager take a while.
	asConnectTimeout = 60 * time.Second

	// asStackBuf is the initial size of the buffer runtime.Stack dumps into. The
	// production dump of 2,715 goroutines was 4.6MB.
	asStackBuf = 8 << 20

	// asSettle is how long the server is left alone between the two phases, so
	// the low phase's queued writes drain before the high phase starts.
	asSettle = 5 * time.Second
)

// asParked is one sample of where goroutines are parked, taken from the same
// place the production evidence came from: a full goroutine dump.
type asParked struct {
	inBatch   int // parked inside bbolt.(*DB).Batch (production dump: 763)
	inBeginRW int // queued in bbolt.(*DB).beginRWTx (production dump: 28)
	total     int
}

// asPhaseResult is one phase's measurement.
type asPhaseResult struct {
	name        string
	clients     int
	adds        int
	offeredRate float64
	rate        float64
	mean        time.Duration
	p50         time.Duration
	p99         time.Duration
	maxLat      time.Duration
	overSlow    int64
	overFloor   int64
	timedOut    int64
	errs        int64
	meanDepth   int64
	maxDepth    int64
	writeTxns   int
	txnsPerAdd  float64
	maxBatch    int
	maxBeginRW  int
}

// TestReliable4AddStorm drives concurrent single-job adds through a real server
// on a real (copied) big database, at low then high client concurrency, and
// reports the add latency distribution, the achieved throughput, the write
// transactions the add path cost per job, and where goroutines were parked.
func TestReliable4AddStorm(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dbFile := os.Getenv("WR_AS_DB")
	if dbFile == "" {
		t.Skip("set WR_AS_DB to a big production-shaped DB (eg. /nfs/hgi/wr/sb10-bigdb/prod.db)")

		return
	}

	low := wsfEnvInt("WR_AS_LOW", asDefaultLow)
	high := wsfEnvInt("WR_AS_HIGH", asDefaultHigh)
	think := time.Duration(wsfEnvInt("WR_AS_THINK_MS", asDefaultThinkMs)) * time.Millisecond
	window := time.Duration(wsfEnvInt("WR_AS_SECONDS", asDefaultSeconds)) * time.Second
	cmdBytes := wsfEnvInt("WR_AS_CMD_BYTES", asDefaultCmdBytes)
	backups := os.Getenv("WR_AS_BACKUP") == "1"

	ctx := context.Background()
	config, serverConfig, addr, reqs, _ := jobqueueTestInit(false)

	work := asConfigureDB(t, &serverConfig, dbFile, backups)

	done := make(chan struct{})

	serverConfig.SchedulerName = "mock"
	serverConfig.RunnerCmd = "addstormrunner %s %s %s %s %d %d"
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

	// deferred AFTER Stop so LIFO runs it BEFORE Stop: it releases the mock
	// scheduler's parked "runners", which Stop(wait=true) waits for.
	defer close(done)

	asLogDBShape(t, server, work, backups)
	asTuneBatching(t, server)
	asSetLimit(t, addr, config.ManagerCAFile, config.ManagerCertDomain, token, reqs)

	t.Logf("ADDSTORM: lowClients=%d highClients=%d think=%s window=%s cmdBytes=%d backups=%v "+
		"slowThreshold=%s clientFloor=%s (production 2026-08-27: 28,815 slow adds in 23min, "+
		"p50 12.8s / max 40.0s, 20,524 of them adding ONE job; 763 goroutines in DB.Batch, "+
		"29 in beginRWTx)",
		low, high, think, window, cmdBytes, backups, slowRequestThreshold, ClientMinRequestTimeout)

	watcher := newACBackupWatcher(work + "_bk.tmp")
	watcher.start()

	lowResult := asPhase(t, server, addr, config, token, "low", low, think, window, cmdBytes, reqs)

	time.Sleep(asSettle)

	highResult := asPhase(t, server, addr, config, token, "high", high, think, window, cmdBytes, reqs)

	copies, bytesWritten, seconds := watcher.stop()

	asReport(t, server, lowResult, highResult, cmdBytes, copies, bytesWritten, seconds)
}

// asReport prints the ADDSTORM-SUMMARY line the wrdev.sh gate parses.
func asReport(t *testing.T, server *Server, low, high asPhaseResult, cmdBytes, backupCopies int,
	backupBytes int64, backupSeconds float64,
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

	t.Logf("ADDSTORM-SUMMARY lowClients=%d lowRate=%.2f highClients=%d highRate=%.2f "+
		"concurrencyFactor=%.1f throughputFactor=%.2f lowP50Ms=%d highP50Ms=%d highP99Ms=%d "+
		"highMaxMs=%d overSlow=%d overFloor=%d timedOut=%d errors=%d adds=%d txnsPerAdd=%.2f "+
		"maxBatchParked=%d maxBeginRWTx=%d queueSize=%d cmdBytes=%d backupCopies=%d backupMb=%d "+
		"backupMbPerSec=%.1f",
		low.clients, low.rate, high.clients, high.rate,
		float64(high.clients)/float64(max(low.clients, 1)), scaling,
		low.p50.Milliseconds(), high.p50.Milliseconds(), high.p99.Milliseconds(),
		high.maxLat.Milliseconds(), low.overSlow+high.overSlow, low.overFloor+high.overFloor,
		low.timedOut+high.timedOut, low.errs+high.errs, low.adds+high.adds, high.txnsPerAdd,
		high.maxBatch, high.maxBeginRW, len(server.q.AllItems()), cmdBytes,
		backupCopies, backupBytes>>20, mbPerSec)

	if low.errs+high.errs > 0 {
		t.Errorf("ADDSTORM: %d adds failed for a reason other than outliving the client's request "+
			"timeout, so the harness itself is suspect", low.errs+high.errs)
	}
}

// asPhase runs one phase: clients concurrent think-then-add loops for window,
// sampling in-flight adds and goroutine parking, and returns the measurement.
func asPhase(t *testing.T, server *Server, addr string, config internal.Config, token []byte,
	name string, clients int, think, window time.Duration, cmdBytes int, reqs *jqs.Requirements,
) asPhaseResult {
	t.Helper()

	m := &arMeter{}
	slow := &asMeter{}
	stop := make(chan struct{})

	var wg sync.WaitGroup

	for c := range clients {
		wg.Add(1)

		go func() {
			defer wg.Done()

			asClientLoop(addr, config.ManagerCAFile, config.ManagerCertDomain, token,
				stop, m, slow, name, c, think, cmdBytes, reqs)
		}()
	}

	txnsBefore := asTxID(server)
	depths, parked := asSample(t, m, stop, window, name)
	txnsAfter := asTxID(server)

	// snapshot at the window's end, BEFORE draining: an add still in flight here
	// is one of the slowest, and counting it would credit the window with work it
	// did not finish (and, in the high phase, a third of the sample). Excluding
	// them makes the achieved rate honest and the latency figures conservative,
	// and matches what production's log records - a request only appears there
	// once it has completed.
	res := asResult(t, m, slow, depths, parked, name, clients, think, window, txnsAfter-txnsBefore)

	close(stop)
	wg.Wait()

	return res
}

// asResult folds one phase's meter, depth samples and goroutine-parking samples
// into its result, and prints the ADDSTORM-PHASE line for it.
func asResult(t *testing.T, m *arMeter, slow *asMeter, depths []int64, parked []asParked,
	name string, clients int, think, window time.Duration, writeTxns int,
) asPhaseResult {
	t.Helper()

	res := asPhaseResult{
		name:        name,
		clients:     clients,
		offeredRate: float64(clients) / think.Seconds(),
		errs:        slow.hardErrs.Load(),
		overFloor:   m.overFloor.Load(),
		overSlow:    slow.overSlow.Load(),
		timedOut:    slow.timedOut.Load(),
		writeTxns:   writeTxns,
	}

	res.meanDepth, res.maxDepth = arDepthStats(depths)

	for _, p := range parked {
		res.maxBatch = max(res.maxBatch, p.inBatch)
		res.maxBeginRW = max(res.maxBeginRW, p.inBeginRW)
	}

	lats := m.latencies()
	res.adds = len(lats)

	if res.adds == 0 {
		t.Logf("ADDSTORM-PHASE phase=%s clients=%d adds=0 NOT-MEASURED: no add completed in %s",
			name, clients, window)

		return res
	}

	if writeTxns > 0 {
		res.txnsPerAdd = float64(writeTxns) / float64(res.adds)
	}

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })

	var total time.Duration
	for _, lat := range lats {
		total += lat
	}

	res.mean = total / time.Duration(res.adds)
	res.p50 = lats[res.adds*50/100]
	res.p99 = lats[min(res.adds*99/100, res.adds-1)]
	res.maxLat = lats[res.adds-1]
	res.rate = float64(res.adds) / window.Seconds()

	t.Logf("ADDSTORM-PHASE phase=%s clients=%d adds=%d offeredRate=%.2f/s achievedRate=%.2f/s "+
		"efficiency=%.3f meanMs=%d p50Ms=%d p99Ms=%d maxMs=%d meanDepth=%d maxDepth=%d "+
		"writeTxns=%d txnsPerAdd=%.2f maxBatchParked=%d maxBeginRWTx=%d overSlow=%d overFloor=%d "+
		"timedOut=%d errors=%d",
		name, clients, res.adds, res.offeredRate, res.rate, res.rate/res.offeredRate,
		res.mean.Milliseconds(), res.p50.Milliseconds(), res.p99.Milliseconds(),
		res.maxLat.Milliseconds(), res.meanDepth, res.maxDepth, res.writeTxns, res.txnsPerAdd,
		res.maxBatch, res.maxBeginRW, res.overSlow, res.overFloor, res.timedOut, res.errs)

	return res
}

// asSample samples the in-flight add count and a full goroutine dump every
// asSampleInterval for window, logging progress, and returns every sample.
func asSample(t *testing.T, m *arMeter, stop chan struct{}, window time.Duration,
	name string,
) ([]int64, []asParked) {
	t.Helper()

	deadline := time.Now().Add(window)
	depths := make([]int64, 0, int(window/asSampleInterval)+1)
	parked := make([]asParked, 0, int(window/asSampleInterval)+1)
	buf := make([]byte, asStackBuf)
	lastCount := int64(0)

	for time.Now().Before(deadline) {
		select {
		case <-stop:
			return depths, parked
		case <-time.After(asSampleInterval):
		}

		depths = append(depths, m.inFlight.Load())

		var p asParked
		p, buf = asParkedNow(buf)
		parked = append(parked, p)

		if count := m.count.Load(); len(depths)%15 == 0 {
			t.Logf("ADDSTORM: %s t+%ds inFlightAdds=%d added=%d (+%d in the last 30s) "+
				"parkedInBatch=%d queuedInBeginRWTx=%d goroutines=%d",
				name, len(depths)*int(asSampleInterval/time.Second), m.inFlight.Load(), count,
				count-lastCount, p.inBatch, p.inBeginRW, p.total)

			lastCount = count
		}
	}

	return depths, parked
}

// asParkedNow counts, from a full goroutine dump, how many goroutines are parked
// inside bbolt's DB.Batch and how many are queued for its single writer lock in
// beginRWTx. These are the two numbers the production goroutine dump reported
// (763 and 29, by these same two substrings), so they are what makes this run
// comparable to it. It returns a possibly-grown buffer to reuse next time.
func asParkedNow(buf []byte) (asParked, []byte) {
	var n int

	for {
		n = runtime.Stack(buf, true)
		if n < len(buf) {
			break
		}

		buf = make([]byte, len(buf)*2)
	}

	dump := string(buf[:n])

	return asParked{
		inBatch:   strings.Count(dump, "bbolt.(*DB).Batch("),
		inBeginRW: strings.Count(dump, "bbolt.(*DB).beginRWTx("),
		total:     strings.Count(dump, "\ngoroutine ") + 1,
	}, buf
}

// asTxID reads the database's current write-transaction id. bbolt increments it
// once per committed write transaction, so the difference across a phase counts
// every write transaction the phase cost - the mechanism number the production
// mutex profile implies but cannot count. It is charged per add because the adds
// are what the phase is doing; the few transactions the server takes on its own
// account (best-effort job updates, scheduling state) ride in the same figure,
// which is right: they are competing for the same single writer.
func asTxID(server *Server) int {
	tx, err := server.db.bolt.Begin(false)
	if err != nil {
		return 0
	}

	id := tx.ID()

	_ = tx.Rollback()

	return id
}

// asMeter counts what arMeter cannot distinguish: adds that crossed the
// manager's own slow-request threshold, and the difference between an add that
// FAILED because it outlived the client's request timeout - which is the
// production symptom itself, the twin of the runner-side "receive time out" that
// loses a completed job's report - and one that failed for any other reason,
// which would mean the harness is broken rather than the add path.
type asMeter struct {
	overSlow atomic.Int64
	timedOut atomic.Int64
	hardErrs atomic.Int64
}

// record folds one add's outcome into the meter.
func (a *asMeter) record(lat time.Duration, err error) {
	if lat >= slowRequestThreshold {
		a.overSlow.Add(1)
	}

	if err == nil {
		return
	}

	if lat >= ClientMinRequestTimeout {
		a.timedOut.Add(1)

		return
	}

	a.hardErrs.Add(1)
}

// asClientLoop is one client: Connect() once, then think for a jittered period
// and synchronously add ONE new job, until stop. Each job is unique, as
// production's added jobs are, so every add does real work in every bucket.
func asClientLoop(addr, caFile, certDomain string, token []byte, stop <-chan struct{},
	m *arMeter, slow *asMeter, phase string, client int, think time.Duration,
	cmdBytes int, reqs *jqs.Requirements,
) {
	jq, err := Connect(addr, caFile, certDomain, token, asConnectTimeout)
	if err != nil {
		slow.hardErrs.Add(1)

		return
	}

	defer disconnect(jq)

	env := os.Environ()
	pad := wsfPad(cmdBytes)
	repGroup := asLimitGroup + strconv.Itoa(client%asRepGroups)
	rng := rand.New(rand.NewPCG(asSeed, uint64(client))) //nolint:gosec // jitter, not cryptography
	pause := arJitter(rng, think, 100)

	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		case <-time.After(pause):
		}

		pause = arJitter(rng, think, asJitterPercent)

		job := &Job{
			Cmd:          fmt.Sprintf("addstorm %s %d %d-%d %s", phase, os.Getpid(), client, i, pad),
			Cwd:          testCwdPath,
			RepGroup:     repGroup,
			ReqGroup:     asLimitGroup,
			Requirements: reqs,
			LimitGroups:  []string{asLimitGroup},
		}

		m.inFlight.Add(1)

		t0 := time.Now()
		_, _, err := jq.Add([]*Job{job}, env, true)
		lat := time.Since(t0)

		m.inFlight.Add(-1)
		m.record(lat, err)
		slow.record(lat, err)
	}
}

// asSetLimit sets asLimitGroup's limit once, with a single add that carries the
// `group:limit` suffix. Every subsequent add then names the group WITHOUT a
// suffix, which is the production-faithful case: the per-add limitGroups map is
// empty, and db.storeLimitGroups opens an empty write transaction anyway.
func asSetLimit(t *testing.T, addr, caFile, certDomain string, token []byte, reqs *jqs.Requirements) {
	t.Helper()

	jq, err := Connect(addr, caFile, certDomain, token, asConnectTimeout)
	if err != nil {
		t.Fatalf("limit-setting client connect failed: %v", err)
	}

	defer disconnect(jq)

	job := &Job{
		Cmd:          "addstorm setlimit",
		Cwd:          testCwdPath,
		RepGroup:     asLimitGroup + "0",
		ReqGroup:     asLimitGroup,
		Requirements: reqs,
		LimitGroups:  []string{asLimitGroup + ":" + strconv.Itoa(asLimit)},
	}

	if _, _, err = jq.Add([]*Job{job}, os.Environ(), true); err != nil {
		t.Fatalf("limit-setting add failed: %v", err)
	}
}

// asTuneBatching applies the MUTATION CONTROL: with WR_AS_BATCH_DELAY_MS and/or
// WR_AS_BATCH_SIZE set, bbolt's coalescing window is widened so that concurrent
// Batch calls fold into ONE transaction instead of fragmenting into many. It
// changes no wr code, so a RED run that turns GREEN under it has been shown to
// be about the fragmentation and not about the database merely being big.
func asTuneBatching(t *testing.T, server *Server) {
	t.Helper()

	delay := wsfEnvInt("WR_AS_BATCH_DELAY_MS", 0)
	size := wsfEnvInt("WR_AS_BATCH_SIZE", 0)

	if delay == 0 && size == 0 {
		t.Logf("ADDSTORM: bbolt batching left at its defaults (MaxBatchDelay=%s MaxBatchSize=%d)",
			server.db.bolt.MaxBatchDelay, server.db.bolt.MaxBatchSize)

		return
	}

	server.db.setBatchTuning(time.Duration(delay)*time.Millisecond, size)

	t.Logf("ADDSTORM: MUTATION CONTROL applied - MaxBatchDelay=%s MaxBatchSize=%d "+
		"(this is not a fix, it is a check that the gate measures the fragmentation)",
		server.db.bolt.MaxBatchDelay, server.db.bolt.MaxBatchSize)
}

// asLogDBShape reports the shape of the database the run is against, because the
// per-transaction fixed cost this reproducer is about is dominated by it: the
// freelist is rewritten on every commit, however little that commit stores.
func asLogDBShape(t *testing.T, server *Server, work string, backups bool) {
	t.Helper()

	stats := server.db.bolt.Stats()
	pageSize := int64(server.db.bolt.Info().PageSize)

	var size int64
	if fi, err := os.Stat(work); err == nil {
		size = fi.Size()
	}

	t.Logf("ADDSTORM: DB file=%.2fGiB freePages=%d (%.1f%% of the file, %dMiB, so %.1fMiB of "+
		"freelist is written per commit) backupsEnabled=%v "+
		"(production 2026-08-27: 9.10GiB, 99,443 free pages = 4.2%%, 0.8MiB per commit)",
		float64(size)/(1<<30), stats.FreePageN, float64(stats.FreePageN)*float64(pageSize)/float64(size)*100,
		int64(stats.FreePageN)*pageSize>>20, float64(stats.FreePageN)*8/(1<<20), backups)
}

// asConfigureDB points serve() at a mutable COPY of the big DB (so the source is
// never touched), with backups forced on when asked for, and returns the copy's
// path. The copy is removed at the end of the test.
//
// The working root is WR_AS_WORK, else WRDEV_ROOT, else the source DB's own
// directory - which is how the same run is taken on NFS (production's
// filesystem) or on local disk.
func asConfigureDB(t *testing.T, serverConfig *ServerConfig, dbFile string, backups bool) string {
	t.Helper()

	scratch := os.Getenv("WR_AS_WORK")
	if scratch == "" {
		scratch = os.Getenv("WRDEV_ROOT")
	}

	if scratch == "" {
		scratch = filepath.Dir(dbFile)
	}

	work := filepath.Join(scratch, "addstorm_work_db")
	paths := []string{work, work + "_bk", work + "_bk.tmp"}

	for _, path := range paths {
		_ = os.Remove(path)
	}

	t.Logf("ADDSTORM: copying big DB %s -> %s (mutated by the run)", dbFile, work)

	t0 := time.Now()
	if err := wsfCopyFile(dbFile, work); err != nil {
		t.Fatalf("failed to copy big DB: %v", err)
	}

	t.Logf("ADDSTORM: copy took %s", time.Since(t0).Round(time.Second))

	t.Cleanup(func() {
		for _, path := range paths {
			_ = os.Remove(path)
		}
	})

	serverConfig.DBFile = work
	serverConfig.DBFileBackup = work + "_bk"
	serverConfig.forceBackups = backups
	serverConfig.dontWipeDevDB = true

	return work
}
