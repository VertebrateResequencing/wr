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

// This file is a FAST, deterministic, in-process reproducer for the reliable4
// "TTR-miss archive-reject churn".
//
// MECHANISM (verified against the code): a runner reports the COMMAND's child pid
// via Started (client.go startedRequest(job, cmd.Process.Pid)), so once the
// command exits that pid is genuinely dead even though the wr-runner process is
// alive. The runner keeps TOUCHING (every ~TouchInterval) until just before it
// archives (client.go stopTouching), and each touch resets the TTR - so a healthy,
// promptly-touching runner is NOT falsely lost. The false-lost only happens when
// touches LAPSE for > TTR while the command has already finished: the runner died
// (crash/OOM/node failure) after the command succeeded but before archiving, or is
// so CPU-starved its touch/archive cannot run for ~a TTR. Then ttrCallback marks
// the job Lost, confirmJobDead sees the (command) pid dead, the job is killed and
// RE-RUN, and the late successful archive is rejected (ErrBadJob / ErrMustReserve).
// The successful work is discarded and the job churns.
//
// This reproducer drives real in-process Server + Client runners. Each runner:
//   reserve -> Started(deadPid)  (command ran; its process is gone)
//   -> optionally keep touching (WR_TTRMISS_TOUCH=1 models a healthy runner)
//   -> wait archiveDelay         (a starved/slow client; touches lapse if TOUCH=0)
//   -> Archive(success)
// With WR_TTRMISS_TOUCH=0 (default) and archiveDelay > TTR this reproduces the
// churn; with WR_TTRMISS_TOUCH=1 the touches protect the job (control: no churn).
// A correct fix makes the TOUCH=0 case drain too (accept the late success).
//
// Run via developers/wrdev.sh ttrmiss-check, or directly:
//
//	WR_TTRMISS_JOBS=200 WR_TTRMISS_TTR_MS=500 WR_TTRMISS_ARCHIVE_DELAY_MS=1500 \
//	  WR_TTRMISS_RUNNERS=40 WR_TTRMISS_SECONDS=60 WR_TTRMISS_TOUCH=0 \
//	  go test -tags reliability_repro ./jobqueue/ -run TestReliable4TtrMissChurn -v

package jobqueue

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	reliable4TtrDefaultJobs    = 200
	reliable4TtrDefaultTTRms   = 500
	reliable4TtrDefaultDelayMs = 1500 // > TTR, so the archive lands after the confirm-dead rerun
	reliable4TtrDefaultRunners = 40
	reliable4TtrDefaultSeconds = 60
	reliable4TtrReserveTimeout = 200 * time.Millisecond
)

// reliable4TtrStats accumulates archive outcomes across the runner pool.
type reliable4TtrStats struct {
	attempts    atomic.Int64
	accepted    atomic.Int64 // archives the server accepted
	badjob      atomic.Int64 // ErrBadJob: item left Run (confirm-dead rerun) -> success discarded
	mustReserve atomic.Int64 // ErrMustReserve: re-reserved by another runner -> success discarded
	otherErr    atomic.Int64

	mu        sync.Mutex
	completed map[string]bool // distinct job keys that have completed (first accepted archive)
}

func (s *reliable4TtrStats) markCompleted(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.completed[key] = true
}

func (s *reliable4TtrStats) completedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.completed)
}

// reliable4TtrParams holds the parsed knobs for one reproducer run.
type reliable4TtrParams struct {
	jobs         int
	runners      int
	seconds      int
	ttr          time.Duration
	archiveDelay time.Duration
	touch        bool
	runnerDead   bool // model a genuinely-dead runner: overwrite the server-side RunnerPid with a dead pid
}

func TestReliable4TtrMissChurn(t *testing.T) {
	if runnermode || servermode {
		return
	}

	p := reliable4TtrParams{
		jobs:         envIntDefault("WR_TTRMISS_JOBS", reliable4TtrDefaultJobs),
		runners:      envIntDefault("WR_TTRMISS_RUNNERS", reliable4TtrDefaultRunners),
		seconds:      envIntDefault("WR_TTRMISS_SECONDS", reliable4TtrDefaultSeconds),
		ttr:          time.Duration(envIntDefault("WR_TTRMISS_TTR_MS", reliable4TtrDefaultTTRms)) * time.Millisecond,
		archiveDelay: time.Duration(envIntDefault("WR_TTRMISS_ARCHIVE_DELAY_MS", reliable4TtrDefaultDelayMs)) * time.Millisecond,
		touch:        os.Getenv("WR_TTRMISS_TOUCH") == "1",
		runnerDead:   os.Getenv("WR_TTRMISS_RUNNER_DEAD") == "1",
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = p.ttr

	server, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	defer server.Stop(ctx, true)

	// make the lost/confirm-dead path fire promptly so the reproducer is fast.
	server.SetLostJobCheckTimeout(2 * time.Second)
	server.SetLostJobCheckRetryTime(200 * time.Millisecond)

	caFile := config.ManagerCAFile
	certDomain := config.ManagerCertDomain
	deadPid := definitelyDeadPid(t)

	reliable4TtrAddJobs(t, addr, caFile, certDomain, token, clientConnectTime, standardReqs, p.jobs)

	t.Logf("TTRMISS: %d jobs, TTR=%s, archiveDelay=%s (%.1fxTTR), %d runners, touch=%v, deadline %ds; deadPid=%d",
		p.jobs, p.ttr, p.archiveDelay, float64(p.archiveDelay)/float64(p.ttr), p.runners, p.touch, p.seconds, deadPid)

	stats := &reliable4TtrStats{completed: make(map[string]bool)}
	done := make(chan struct{})

	var wg sync.WaitGroup

	for r := 0; r < p.runners; r++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			reliable4TtrRunner(server, addr, caFile, certDomain, token, clientConnectTime, stats, deadPid, p, done)
		}()
	}

	reliable4TtrMonitor(t, stats, p.jobs, p.seconds, done)

	close(done)
	wg.Wait()

	reliable4TtrVerdict(t, stats, p.jobs)
}

// reliable4TtrAddJobs connects one client and adds `jobs` trivially-succeeding
// jobs with generous retries (so lost-reruns keep re-queuing rather than burying).
func reliable4TtrAddJobs(t *testing.T, addr, caFile, certDomain string, token []byte,
	connectTime time.Duration, reqs *jqs.Requirements, jobs int,
) {
	jq, err := Connect(addr, caFile, certDomain, token, connectTime)
	if err != nil {
		t.Fatalf("add-client connect failed: %v", err)
	}

	defer disconnect(jq)

	toAdd := make([]*Job, jobs)
	for i := range toAdd {
		toAdd[i] = &Job{
			Cmd:          restFormTrue + " ttrmiss " + strconv.Itoa(i),
			Cwd:          testCwdPath,
			RepGroup:     "reliable4_ttrmiss_rg",
			ReqGroup:     "reliable4_ttrmiss_rg",
			Requirements: reqs,
			Retries:      250,
		}
	}

	inserts, _, err := jq.Add(toAdd, os.Environ(), true)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if inserts != jobs {
		t.Fatalf("expected to add %d jobs, added %d", jobs, inserts)
	}
}

// reliable4TtrRunner is one pool worker: it repeatedly reserves a job, reports it
// started with a definitely-dead pid (the command has run and its process is gone),
// optionally keeps touching (a healthy runner), waits archiveDelay (a slow/starved
// client), then archives the success and classifies the server's response.
func reliable4TtrRunner(server *Server, addr, caFile, certDomain string, token []byte, connectTime time.Duration,
	stats *reliable4TtrStats, deadPid int, p reliable4TtrParams, done <-chan struct{},
) {
	jq, err := Connect(addr, caFile, certDomain, token, connectTime)
	if err != nil {
		return
	}

	defer disconnect(jq)

	for {
		select {
		case <-done:
			return
		default:
		}

		job, rerr := jq.Reserve(reliable4TtrReserveTimeout)
		if rerr != nil || job == nil {
			continue
		}

		if serr := jq.Started(job, deadPid); serr != nil {
			continue
		}

		// model a genuinely-dead runner (crash/OOM/node-fail after success): overwrite
		// the server-side runner pid with a dead one, so confirmJobDead's both-dead
		// check confirms death and re-runs (the correct behaviour). Default keeps the
		// live test-process pid the client reported (a live/starved runner).
		if p.runnerDead {
			setServerJobRunnerPid(server, job.Key(), deadPid)
		}

		reliable4TtrWaitThenArchive(jq, job, stats, p, done)
	}
}

// setServerJobRunnerPid overwrites the server-side job's recorded RunnerPid under
// lock (white-box; the test is in package jobqueue), to model a dead runner.
func setServerJobRunnerPid(server *Server, key string, pid int) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return
	}

	job.Lock()
	job.RunnerPid = pid
	job.Unlock()
}

// reliable4TtrWaitThenArchive waits archiveDelay (optionally touching, like a
// healthy runner) then archives the success and records the outcome. Touching is
// stopped just before the archive, mirroring the real runner's stopTouching.
func reliable4TtrWaitThenArchive(jq *Client, job *Job, stats *reliable4TtrStats,
	p reliable4TtrParams, done <-chan struct{},
) {
	stopTouch := make(chan struct{})

	if p.touch {
		go reliable4TtrTouchLoop(jq, job, p.ttr/3, stopTouch)
	}

	select {
	case <-done:
		close(stopTouch)

		return
	case <-time.After(p.archiveDelay):
	}

	close(stopTouch) // stop touching just before archiving, as a real runner does

	stats.attempts.Add(1)

	aerr := jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
	switch {
	case aerr == nil:
		stats.accepted.Add(1)
		stats.markCompleted(job.Key())
	case strings.Contains(aerr.Error(), ErrBadJob):
		stats.badjob.Add(1)
	case strings.Contains(aerr.Error(), ErrMustReserve):
		stats.mustReserve.Add(1)
	default:
		stats.otherErr.Add(1)
	}
}

// reliable4TtrTouchLoop touches the job every interval until stop is closed,
// modelling a healthy runner that keeps its reservation alive.
func reliable4TtrTouchLoop(jq *Client, job *Job, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_, _ = jq.Touch(job)
		}
	}
}

// reliable4TtrMonitor prints progress every few seconds and returns when all jobs
// have completed or the deadline passes.
func reliable4TtrMonitor(t *testing.T, stats *reliable4TtrStats, jobs, seconds int, done <-chan struct{}) {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	start := time.Now()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		completed := stats.completedCount()

		t.Logf("TTRMISS t+%3.0fs completed=%d/%d attempts=%d accepted=%d badjob=%d mustReserve=%d otherErr=%d",
			time.Since(start).Seconds(), completed, jobs, stats.attempts.Load(), stats.accepted.Load(),
			stats.badjob.Load(), stats.mustReserve.Load(), stats.otherErr.Load())

		if completed >= jobs || time.Now().After(deadline) {
			return
		}
	}
}

// reliable4TtrVerdict prints the final metrics and the churn verdict.
func reliable4TtrVerdict(t *testing.T, stats *reliable4TtrStats, jobs int) {
	completed := stats.completedCount()
	rejected := stats.badjob.Load() + stats.mustReserve.Load()

	t.Logf("TTRMISS VERDICT: completed=%d/%d attempts=%d accepted=%d rejected=%d (badjob=%d mustReserve=%d) otherErr=%d",
		completed, jobs, stats.attempts.Load(), stats.accepted.Load(), rejected,
		stats.badjob.Load(), stats.mustReserve.Load(), stats.otherErr.Load())

	if completed < jobs {
		t.Logf("TTRMISS CHURN REPRODUCED: only %d/%d jobs completed; %d successful archives were rejected "+
			"and their jobs re-run (successful work discarded)", completed, jobs, rejected)
	} else {
		t.Logf("TTRMISS NO CHURN: all %d jobs completed; %d late archives rejected as duplicates "+
			"(fine as long as each job completed once)", jobs, rejected)
	}
}

// TestReliable4TtrBackstopKill validates the Fix-C + kill-backstop trial: a job whose
// command finished (dead command pid) but whose runner is alive-but-wedged (never
// archives) is NOT re-run while the runner lives (Fix C parks it), and is reclaimed
// only after the backstop force-kills that runner, after which the normal
// dead-confirmation path re-runs it.
func TestReliable4TtrBackstopKill(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr      = 500 * time.Millisecond
		backstop = 3 * time.Second
		rg       = "reliable4_ttr_backstop_rg"
	)

	t.Setenv("WR_EXP_RUNNERPID", "1")
	t.Setenv("WR_EXP_LOSTBACKSTOP_MS", strconv.Itoa(int(backstop/time.Millisecond)))

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	server, _, token, err := serve(ctx, serverConfig)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	defer server.Stop(ctx, true)

	server.SetLostJobCheckTimeout(2 * time.Second)
	server.SetLostJobCheckRetryTime(200 * time.Millisecond)

	// a live, killable stand-in for the wedged runner process; reaped so a kill truly
	// ends it (no lingering zombie that ps would still report as alive).
	child := exec.CommandContext(ctx, "sleep", "120")
	if errs := child.Start(); errs != nil {
		t.Fatalf("failed to start runner stand-in: %v", errs)
	}

	childPid := child.Process.Pid

	go func() { _ = child.Wait() }()
	defer func() { _ = child.Process.Kill() }()

	deadPid := definitelyDeadPid(t)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	defer disconnect(jq)

	job := &Job{
		Cmd: restFormTrue + " backstop", Cwd: testCwdPath, RepGroup: rg,
		ReqGroup: rg, Requirements: standardReqs, Retries: 3,
	}
	if _, _, erra := jq.Add([]*Job{job}, os.Environ(), true); erra != nil {
		t.Fatalf("add failed: %v", erra)
	}

	reserved, errr := jq.Reserve(2 * time.Second)
	if errr != nil || reserved == nil {
		t.Fatalf("reserve failed: %v", errr)
	}

	key := reserved.Key()

	// command finished (a dead command pid); the runner is the live child and is
	// wedged (this test never archives on its behalf).
	if errs := jq.Started(reserved, deadPid); errs != nil {
		t.Fatalf("started failed: %v", errs)
	}

	setServerJobRunnerPid(server, key, childPid)

	if !waitForJobLost(server, key, 6*ttr) {
		t.Fatal("job did not go Lost")
	}

	// while the runner (child) is alive and before the backstop, the job is parked
	// Lost-in-Run and must NOT be re-reservable (Fix C must not re-run it yet).
	if reReserved, _ := countReReserves(addr, config.ManagerCAFile, config.ManagerCertDomain,
		token, clientConnectTime, 3); reReserved != 0 {
		t.Fatalf("job was re-reservable (%d) while its runner was still alive; Fix C should park it", reReserved)
	}

	// after the backstop the runner is force-killed, death is confirmed, and the job
	// is re-run -> a fresh client can reserve it again.
	var got *Job

	deadline := time.Now().Add(backstop + 10*time.Second)
	for time.Now().Before(deadline) {
		jq2, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		if errc == nil {
			j, _ := jq2.Reserve(200 * time.Millisecond)
			disconnect(jq2)

			if j != nil {
				got = j

				break
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	if got == nil || got.Key() != key {
		t.Fatalf("job was not reclaimed/re-run after the backstop (got=%v)", got)
	}

	if processAliveLocally(childPid) {
		t.Fatal("the wedged runner stand-in was not killed by the backstop")
	}

	t.Logf("BACKSTOP OK: job parked Lost while its runner was alive, then the backstop killed the "+
		"runner (pid %d) and the job was reclaimed/re-run after ~%s", childPid, backstop)
}

// processAliveLocally reports whether pid is a live (non-zombie) process on this host.
func processAliveLocally(pid int) bool {
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))

	return state != "" && !strings.HasPrefix(state, "Z")
}
