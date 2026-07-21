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

// This file covers spec B1: Serve() reorders so prior-state recovery runs in a
// background goroutine behind a recovering flag, letting the manager answer
// clients immediately. A recoveryPauseHook (test seam, mirrored on
// statusWSDetailsHook) blocks recovery so the recovering window is observable
// without timing flakiness.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// recoveryWaitTimeout bounds how long waitUntilRecovered blocks for background
// recovery to finish; recovery of the small test db is near-instant once its
// pause hook is released.
const recoveryWaitTimeout = 10 * time.Second

// backfillWaitTimeout bounds how long waitForBackfill blocks for the A3
// background counter backfill to finish; the small test db backfills near-
// instantly once the manager is serving.
const backfillWaitTimeout = 20 * time.Second

// priorState holds the keys of the jobs a prior server left incomplete, grouped
// by the sub-queue recovery should restore them into.
type priorState struct {
	ready   []string
	running []string
	buried  []string
}

// createPriorStateDB runs a first server against serverConfig's db, adds a mix
// of ready/running/buried jobs, then stops the server leaving the db populated
// for a subsequent recovering restart. It returns the keys of the jobs left in
// each state. recoveryPauseHookForTest must be nil while this runs (this first
// server has no prior jobs to recover, but a blocking hook would still stall its
// recovery goroutine and shutdown).
func createPriorStateDB(ctx context.Context, config internal.Config, serverConfig ServerConfig,
	addr string, reqs *jqs.Requirements, connectTime time.Duration, repGroup string,
	nReady, nRunning, nBuried int) priorState {
	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
	So(err, ShouldBeNil)

	newJob := func(suffix string) *Job {
		return &Job{
			Cmd: restFormTrue + " " + repGroup + " " + suffix, Cwd: testCwdPath,
			RepGroup: repGroup, ReqGroup: repGroup, Requirements: reqs, Retries: 30,
		}
	}

	reserveStart := func() *Job {
		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		return reserved
	}

	var state priorState

	// running jobs first: add them and reserve+start each while they are the
	// only ready jobs, so we know exactly which keys become running.
	runningJobs := make([]*Job, nRunning)
	for i := range runningJobs {
		runningJobs[i] = newJob("run" + strconv.Itoa(i))
	}

	if nRunning > 0 {
		added, _, errr := jq.Add(runningJobs, envVars, true)
		So(errr, ShouldBeNil)
		So(added, ShouldEqual, nRunning)

		for range runningJobs {
			state.running = append(state.running, reserveStart().Key())
		}
	}

	// buried jobs next: add, reserve+start, then bury each.
	buriedJobs := make([]*Job, nBuried)
	for i := range buriedJobs {
		buriedJobs[i] = newJob("bury" + strconv.Itoa(i))
	}

	if nBuried > 0 {
		added, _, errr := jq.Add(buriedJobs, envVars, true)
		So(errr, ShouldBeNil)
		So(added, ShouldEqual, nBuried)

		for range buriedJobs {
			j := reserveStart()
			So(jq.Bury(j, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, FailReasonExit), ShouldBeNil)
			state.buried = append(state.buried, j.Key())
		}
	}

	// ready jobs last: add them and leave them untouched.
	readyJobs := make([]*Job, nReady)
	for i := range readyJobs {
		readyJobs[i] = newJob("ready" + strconv.Itoa(i))
	}

	if nReady > 0 {
		added, _, errr := jq.Add(readyJobs, envVars, true)
		So(errr, ShouldBeNil)
		So(added, ShouldEqual, nReady)

		for _, j := range readyJobs {
			state.ready = append(state.ready, j.Key())
		}
	}

	disconnect(jq)
	server.Stop(ctx, true)

	return state
}

func (p priorState) all() []string {
	keys := make([]string, 0, len(p.ready)+len(p.running)+len(p.buried))
	keys = append(keys, p.ready...)
	keys = append(keys, p.running...)
	keys = append(keys, p.buried...)

	return keys
}

func TestB1NonBlockingResponsiveWhilePaused(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const rg = "b1_responsive"

	Convey("The manager answers clients within 2s while recovery is paused", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, 2, 2, 2)
		So(len(state.all()), ShouldEqual, 6)

		hookEntered := make(chan struct{})
		release := make(chan struct{})

		var once sync.Once

		recoveryPauseHookForTest = func() {
			once.Do(func() { close(hookEntered) })
			<-release
		}
		defer func() { recoveryPauseHookForTest = nil }()

		serverConfig.dontWipeDevDB = true
		server, _, token, err := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false
		recoveryPauseHookForTest = nil

		So(err, ShouldBeNil)

		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}

			server.Stop(ctx, true)
		}()

		// recovery has reached the hook and is blocked there.
		select {
		case <-hookEntered:
		case <-time.After(2 * time.Second):
			So("timed out waiting for recovery to reach the pause hook", ShouldBeBlank)

			return
		}

		start := time.Now()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// Ping succeeds.
		_, err = jq.Ping(2 * time.Second)
		So(err, ShouldBeNil)

		// manager status responds and we are recovering.
		_, err = jq.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(server.isRecovering(), ShouldBeTrue)

		// a new job can be added and reserved (recovered jobs are not enqueued
		// yet, so the only reservable job is this new one).
		newRG := rg + "_new"
		newJob := &Job{
			Cmd: restFormTrue + " " + newRG, Cwd: testCwdPath, RepGroup: newRG,
			ReqGroup: newRG, Requirements: reqs, Retries: 0,
		}
		added, _, err := jq.Add([]*Job{newJob}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, newJob.Key())

		So(time.Since(start), ShouldBeLessThan, 2*time.Second)

		// release the hook and let recovery finish.
		close(release)
		So(waitUntilRecovered(server), ShouldBeTrue)
	})
}

// waitUntilRecovered blocks until the server stops recovering or
// recoveryWaitTimeout elapses, returning whether recovery finished in time.
func waitUntilRecovered(server *Server) bool {
	deadline := time.Now().Add(recoveryWaitTimeout)
	for time.Now().Before(deadline) {
		if !server.isRecovering() {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return !server.isRecovering()
}

// TestB1StopDuringActiveRecovery checks that a graceful Stop() overlapping the
// live background prior-state recovery goroutine neither hangs nor panics and is
// race-clean (finding 1: shutdown coordinates with recovery/backfill before
// scheduler cleanup, DB close and queue destroy). It blocks recovery at
// recoveryPauseHook, starts Stop() in a goroutine (shutdown cancels the
// background context and waits for the goroutine early), then releases the hook
// so recovery observes the cancellation and returns; Stop must then finish
// promptly. The brief sleep only biases toward exercising the overlap - none of
// the assertions depend on it, so the test is deterministic either way.
func TestB1StopDuringActiveRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const rg = "b1_stop_during_recovery"

	Convey("Stop() during active recovery completes without hang or panic", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, 2, 2, 2)
		So(len(state.all()), ShouldEqual, 6)

		hookEntered := make(chan struct{})
		release := make(chan struct{})

		var once sync.Once

		recoveryPauseHookForTest = func() {
			once.Do(func() { close(hookEntered) })
			<-release
		}
		defer func() { recoveryPauseHookForTest = nil }()

		serverConfig.dontWipeDevDB = true
		server, _, _, err := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false
		recoveryPauseHookForTest = nil

		So(err, ShouldBeNil)

		// recovery has reached the hook and is blocked there, so the recovery
		// goroutine is live for the duration of the Stop below.
		select {
		case <-hookEntered:
		case <-time.After(2 * time.Second):
			So("timed out waiting for recovery to reach the pause hook", ShouldBeBlank)

			return
		}

		So(server.isRecovering(), ShouldBeTrue)

		stopped := make(chan struct{})

		go func() {
			server.Stop(ctx, true)
			close(stopped)
		}()

		// let Stop reach its early cancel-and-wait for the recovery goroutine,
		// then release the hook so recovery observes the cancellation.
		time.Sleep(50 * time.Millisecond)
		close(release)

		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			So("Stop() did not complete during active recovery", ShouldBeBlank)

			return
		}

		So(server.isRecovering(), ShouldBeFalse)
	})
}

func TestB1RecoveryReproducesGroundTruth(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const (
		rg       = "b1_ground_truth"
		nReady   = 3
		nRunning = 2
		nBuried  = 2
		m        = nReady + nRunning + nBuried
	)

	Convey("Background recovery restores the exact prior state with no loss or dups", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg,
			nReady, nRunning, nBuried)
		So(len(state.all()), ShouldEqual, m)

		serverConfig.dontWipeDevDB = true
		server, _, _, err := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false

		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)
		So(server.isRecovering(), ShouldBeFalse)

		restored, total := server.recoveryProgress()
		So(total, ShouldEqual, m)
		So(restored, ShouldEqual, m)

		keys, lost := queueKeys(server)
		So(lost, ShouldEqual, 0)
		So(len(keys), ShouldEqual, m)

		for _, k := range state.all() {
			So(keys[k], ShouldBeTrue)
		}

		stats := server.q.Stats()
		So(stats.Items, ShouldEqual, m)
		So(stats.Ready, ShouldEqual, nReady)
		So(stats.Running, ShouldEqual, nRunning)
		So(stats.Buried, ShouldEqual, nBuried)
	})
}

// queueKeys returns the set of item keys currently in the server's queue, and
// how many of them are flagged Lost.
func queueKeys(server *Server) (map[string]bool, int) {
	keys := make(map[string]bool)
	lost := 0

	for _, item := range server.q.AllItems() {
		keys[item.Key] = true

		if j, ok := item.Data().(*Job); ok {
			j.RLock()

			if j.Lost {
				lost++
			}

			j.RUnlock()
		}
	}

	return keys, lost
}

func TestB1HammerDuringRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const (
		rg     = "b1_hammer"
		nReady = 6
	)

	Convey("Clients hammering Add/Reserve/status during recovery keep accounting exact", t, func() {
		// only ready prior jobs, so recovery never touches recoveredRunningJobs
		// (guarded only in B3); this keeps the -race run clean on B1 alone.
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, nReady, 0, 0)
		So(len(state.all()), ShouldEqual, nReady)

		hookEntered := make(chan struct{})
		release := make(chan struct{})

		var once sync.Once

		recoveryPauseHookForTest = func() {
			once.Do(func() { close(hookEntered) })
			<-release
		}
		defer func() { recoveryPauseHookForTest = nil }()

		serverConfig.dontWipeDevDB = true
		server, _, token, err := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false
		recoveryPauseHookForTest = nil

		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		select {
		case <-hookEntered:
		case <-time.After(2 * time.Second):
			So("timed out waiting for recovery to reach the pause hook", ShouldBeBlank)

			return
		}

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// hammer with distinct-key Adds, Reserves (jobs left reserved, not
		// archived, so nothing is removed) and status queries, spanning the
		// window where recovery is released and completes.
		const hammerAdds = 20

		newRG := rg + "_new"
		addedKeys := make(map[string]bool, hammerAdds)
		stop := make(chan struct{})

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := range hammerAdds {
				j := &Job{
					Cmd: restFormTrue + " " + newRG + " " + strconv.Itoa(i), Cwd: testCwdPath,
					RepGroup: newRG, ReqGroup: newRG, Requirements: reqs, Retries: 0,
				}
				if a, _, aerr := jq.Add([]*Job{j}, envVars, true); aerr == nil && a == 1 {
					addedKeys[j.Key()] = true
				}

				time.Sleep(time.Millisecond)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				select {
				case <-stop:
					return
				default:
				}

				_, rerr := jq.Reserve(20 * time.Millisecond)

				_, serr := jq.GetStatusByRepGroupMatch(rg, RepGroupMatchExact, nil, true, false)
				if rerr != nil || serr != nil {
					continue
				}
			}
		}()

		// let some hammering happen, then release recovery.
		time.Sleep(50 * time.Millisecond)
		close(release)

		So(waitUntilRecovered(server), ShouldBeTrue)

		// stop hammering and wait for the goroutines to finish.
		close(stop)
		wg.Wait()

		keys, lost := queueKeys(server)
		So(lost, ShouldEqual, 0)

		// every recovered key is present exactly once (queue keys are a set).
		for _, k := range state.all() {
			So(keys[k], ShouldBeTrue)
		}

		// every added key is present too, and the total is exactly the recovered
		// plus the added jobs (nothing lost, nothing duplicated, nothing removed).
		for k := range addedKeys {
			So(keys[k], ShouldBeTrue)
		}

		So(len(keys), ShouldEqual, len(state.all())+len(addedKeys))
	})
}

func TestB1RecoveryProgressMonotonic(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const (
		rg       = "b1_progress"
		nReady   = 3
		nRunning = 1
		nBuried  = 1
		m        = nReady + nRunning + nBuried
	)

	Convey("recoveryProgress reports total up front and reaches restored==total monotonically", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg,
			nReady, nRunning, nBuried)
		So(len(state.all()), ShouldEqual, m)

		hookEntered := make(chan struct{})
		release := make(chan struct{})

		var once sync.Once

		recoveryPauseHookForTest = func() {
			once.Do(func() { close(hookEntered) })
			<-release
		}
		defer func() { recoveryPauseHookForTest = nil }()

		serverConfig.dontWipeDevDB = true
		server, _, _, err := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false
		recoveryPauseHookForTest = nil

		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		select {
		case <-hookEntered:
		case <-time.After(2 * time.Second):
			So("timed out waiting for recovery to reach the pause hook", ShouldBeBlank)

			return
		}

		// paused at the hook: total known, nothing restored yet.
		restored, total := server.recoveryProgress()
		So(total, ShouldEqual, m)
		So(restored, ShouldEqual, 0)

		// sample progress across the run; each sample must be non-decreasing and
		// never exceed total. Sampling in its own goroutine, asserting after join
		// so So() is only called from the test goroutine.
		type sample struct{ restored, total int }

		var samples []sample

		sampled := make(chan struct{})

		go func() {
			defer close(sampled)

			for {
				r, tot := server.recoveryProgress()
				samples = append(samples, sample{r, tot})

				if !server.isRecovering() {
					return
				}

				time.Sleep(time.Millisecond)
			}
		}()

		close(release)
		<-sampled

		So(server.isRecovering(), ShouldBeFalse)

		restored, total = server.recoveryProgress()
		So(total, ShouldEqual, m)
		So(restored, ShouldEqual, m)

		prev := 0
		maxRestored := 0
		exceededTotal := false

		for _, s := range samples {
			if s.restored < prev {
				exceededTotal = true // reuse flag: monotonicity violated
			}

			if s.restored > s.total {
				exceededTotal = true
			}

			prev = s.restored
			if s.restored > maxRestored {
				maxRestored = s.restored
			}
		}

		So(exceededTotal, ShouldBeFalse)
		So(maxRestored, ShouldEqual, m)
	})
}

func TestB2WindowArchiveReturnsRecovering(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const rg = "b2_window_archive"

	Convey("B2.1: a window archive of a to-be-restored running key returns ErrRecovering", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, 0, 1, 0)
		So(len(state.running), ShouldEqual, 1)

		runKey := state.running[0]

		server, token, release := b2PausedRecoveringServer(ctx, serverConfig)

		defer func() {
			release()
			server.Stop(ctx, true)
		}()

		So(server.isRecovering(), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		archiveJob := b2RunningJob(rg, reqs)
		So(archiveJob.Key(), ShouldEqual, runKey)

		// the key is not yet in the queue (recovery is paused), so the archive is
		// refused with a retryable ErrRecovering, not the terminal ErrBadJob.
		err = jq.Archive(archiveJob, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		So(err, ShouldNotBeNil)
		So(strings.Contains(err.Error(), ErrRecovering), ShouldBeTrue)
		So(strings.Contains(err.Error(), ErrBadJob), ShouldBeFalse)
		So(strings.Contains(err.Error(), ErrBadRequest), ShouldBeFalse)
	})
}

// b2PausedRecoveringServer starts a server against the (undeleted) db, blocking
// its background recovery at recoveryPauseHook so the recovering window is
// observable. It returns the server, a connect token, and a release func that
// unblocks recovery (idempotent). createPriorStateDB must have populated the db
// first. The caller is responsible for server.Stop.
func b2PausedRecoveringServer(ctx context.Context, serverConfig ServerConfig) (*Server, []byte, func()) {
	hookEntered := make(chan struct{})
	release := make(chan struct{})

	var (
		once    sync.Once
		relOnce sync.Once
	)

	recoveryPauseHookForTest = func() {
		once.Do(func() { close(hookEntered) })
		<-release
	}
	defer func() { recoveryPauseHookForTest = nil }()

	serverConfig.dontWipeDevDB = true
	server, _, token, err := serve(ctx, serverConfig)
	serverConfig.dontWipeDevDB = false
	recoveryPauseHookForTest = nil

	So(err, ShouldBeNil)

	releaseFn := func() { relOnce.Do(func() { close(release) }) }

	select {
	case <-hookEntered:
	case <-time.After(2 * time.Second):
		So("timed out waiting for recovery to reach the pause hook", ShouldBeBlank)
	}

	return server, token, releaseFn
}

// b2RunningJob rebuilds, byte-for-byte, the first running job createPriorStateDB
// adds (suffix "run0"), so its Key() matches state.running[0]. A reconnecting
// runner reconstructs its job this way to touch/archive a key that recovery has
// not yet restored into the queue.
func b2RunningJob(repGroup string, reqs *jqs.Requirements) *Job {
	return &Job{
		Cmd: restFormTrue + " " + repGroup + " run0", Cwd: testCwdPath,
		RepGroup: repGroup, ReqGroup: repGroup, Requirements: reqs, Retries: 30,
	}
}

func TestB2ArchiveRetryAfterRecoverySucceeds(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const rg = "b2_archive_retry"

	Convey("B2.2: after recovery restores the job, a retried archive succeeds and counts once", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, 0, 1, 0)
		So(len(state.running), ShouldEqual, 1)

		runKey := state.running[0]

		server, token, release := b2PausedRecoveringServer(ctx, serverConfig)

		defer func() {
			release()
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		archiveJob := b2RunningJob(rg, reqs)
		So(archiveJob.Key(), ShouldEqual, runKey)

		// during the window the archive is refused with a retryable error.
		err = jq.Archive(archiveJob, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		So(err, ShouldNotBeNil)
		So(strings.Contains(err.Error(), ErrRecovering), ShouldBeTrue)

		// release recovery and wait for the running job to be restored.
		release()
		So(waitUntilRecovered(server), ShouldBeTrue)

		// a reconnecting runner keeps its identity: adopt the recovered job's
		// ReservedBy so the retried archive is accepted (as that same runner).
		item, gerr := server.q.Get(runKey)
		So(gerr, ShouldBeNil)

		rj, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		rj.RLock()
		reservedBy := rj.ReservedBy
		rj.RUnlock()

		jq.clientid = reservedBy

		// retry the archive (as reportFinalState would): it now succeeds.
		err = jq.Archive(archiveJob, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
		So(err, ShouldBeNil)

		// the job left the queue, complete.
		_, gerr = server.q.Get(runKey)
		So(gerr, ShouldNotBeNil)

		// its repgroup counter was incremented exactly once, matching the RAW scan.
		raw, rerr := server.db.retrieveCompleteJobCountsByRepGroups([]string{rg})
		So(rerr, ShouldBeNil)

		maintained, merr := server.db.retrieveMaintainedCompleteCounts([]string{rg})
		So(merr, ShouldBeNil)

		So(raw[rg], ShouldEqual, 1)
		So(maintained[rg], ShouldEqual, raw[rg])
	})
}

func TestB2WindowTouchReturnsRecovering(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const rg = "b2_window_touch"

	Convey("B2.3: a window touch of a not-yet-restored running key returns ErrRecovering and records contact", t, func() {
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, 0, 1, 0)
		So(len(state.running), ShouldEqual, 1)

		runKey := state.running[0]

		server, token, release := b2PausedRecoveringServer(ctx, serverConfig)

		defer func() {
			release()
			server.Stop(ctx, true)
		}()

		So(server.isRecovering(), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		touchJob := b2RunningJob(rg, reqs)
		So(touchJob.Key(), ShouldEqual, runKey)

		// the key is not yet restored, so the touch is refused with a retryable
		// ErrRecovering rather than the terminal ErrBadJob.
		_, err = jq.Touch(touchJob)
		So(err, ShouldNotBeNil)
		So(strings.Contains(err.Error(), ErrRecovering), ShouldBeTrue)
		So(strings.Contains(err.Error(), ErrBadJob), ShouldBeFalse)
		So(strings.Contains(err.Error(), ErrBadRequest), ShouldBeFalse)
	})
}

func TestB3RecoveryReAddConverges(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const rg = "b3_readd_converge"

	Convey("B3.1: recovery + a concurrent re-add of the same key converge to one item", t, func() {
		// a single prior running job; recovering it exercises recoverRunningJob,
		// the write site for recoveredRunningJobs.
		state := createPriorStateDB(ctx, config, serverConfig, addr, reqs, connectTime, rg, 0, 1, 0)
		So(len(state.running), ShouldEqual, 1)

		runKey := state.running[0]

		server, token, release := b2PausedRecoveringServer(ctx, serverConfig)

		defer func() {
			release()
			server.Stop(ctx, true)
		}()

		So(server.isRecovering(), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		// while recovery is paused a client re-adds the byte-for-byte identical job
		// (same Cmd/Cwd => same key). The key is not yet in the live queue, so the
		// add creates exactly one item for it.
		readdJob := b2RunningJob(rg, reqs)
		So(readdJob.Key(), ShouldEqual, runKey)

		added, _, err := jq.Add([]*Job{readdJob}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		// release recovery: its single-batch enqueue (AddMany) sees the key already
		// present and dedups it, so recovery and the client add converge on one item.
		release()
		So(waitUntilRecovered(server), ShouldBeTrue)

		// exactly one queue item exists, and it is the shared key.
		keys, lost := queueKeys(server)
		So(lost, ShouldEqual, 0)
		So(len(keys), ShouldEqual, 1)
		So(keys[runKey], ShouldBeTrue)

		stats := server.q.Stats()
		So(stats.Items, ShouldEqual, 1)

		// drive the single item to completion once.
		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, runKey)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
		So(jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		// the job left the queue and there is nothing else to run: it did not run
		// twice.
		_, gerr := server.q.Get(runKey)
		So(gerr, ShouldNotBeNil)

		So(server.q.Stats().Items, ShouldEqual, 0)

		again, err := jq.Reserve(200 * time.Millisecond)
		So(err, ShouldBeNil)
		So(again, ShouldBeNil)

		// the repgroup counter was incremented exactly once and matches the RAW scan.
		raw, rerr := server.db.retrieveCompleteJobCountsByRepGroups([]string{rg})
		So(rerr, ShouldBeNil)

		maintained, merr := server.db.retrieveMaintainedCompleteCounts([]string{rg})
		So(merr, ShouldBeNil)

		So(raw[rg], ShouldEqual, 1)
		So(maintained[rg], ShouldEqual, raw[rg])
	})
}

func TestB3RecoveredRunningJobsRaceFree(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	_, serverConfig, _, reqs, _ := jobqueueTestInit(false) //nolint:dogsled
	serverConfig.Timings.ItemTTR = time.Hour

	const (
		rg      = "b3_race"
		nJobs   = 4
		nWrites = 60
	)

	Convey("B3.2: concurrent recoverRunningJob (write) and confirmOrReleaseLostJob (read) are race-free", t, func() {
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		jobs := make([]*Job, nJobs)
		for i := range jobs {
			jobs[i] = &Job{
				Cmd: restFormTrue + " " + rg + " race" + strconv.Itoa(i), Cwd: testCwdPath,
				RepGroup: rg, ReqGroup: rg, Requirements: reqs, Retries: 0,
			}
		}

		// seed the map serially so every reader key is present; with the key
		// present and killCalled false, confirmOrReleaseLostJob only reads the map
		// (confirmedDead is false) and returns without side effects.
		for _, j := range jobs {
			server.recoverRunningJob(ctx, j, "", 0)
		}

		timeout, retry := server.lostJobCheckDurations()

		start := make(chan struct{})
		done := make(chan struct{})

		var wg sync.WaitGroup

		// writer: re-runs the real write site, re-asserting each key under rrjMu.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)

			<-start

			for range nWrites {
				for _, j := range jobs {
					server.recoverRunningJob(ctx, j, "", 0)
				}
			}
		}()

		// reader: exercises the real read site continuously for the whole writer
		// window, so any unguarded concurrent map access would be flagged by -race.
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start

			for {
				select {
				case <-done:
					return
				default:
				}

				for _, j := range jobs {
					server.confirmOrReleaseLostJob(ctx, j, lostJobDetails{
						key: j.Key(), killCalled: false, checkTimeout: timeout, checkRetryTime: retry,
					})
				}
			}
		}()

		close(start)
		wg.Wait()

		// all seeded keys survived the concurrent writes (sanity that the write site
		// actually populated the guarded map).
		present := 0

		server.rrjMu.RLock()

		for _, j := range jobs {
			if server.recoveredRunningJobs[j.Key()] {
				present++
			}
		}

		server.rrjMu.RUnlock()

		So(present, ShouldEqual, nJobs)
	})
}

func TestIntegrationNonBlockingServeBackgroundBackfill(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, reqs, connectTime := jobqueueTestInit(false)
	serverConfig.Timings.ItemTTR = time.Hour

	const (
		rgIA = "integ_bf_a"
		rgIB = "integ_bf_b"
		rgIC = "integ_bf_c"
	)

	repGroups := []string{rgIA, rgIB, rgIC}
	expected := map[string]int{rgIA: 2, rgIB: 1, rgIC: 3}

	Convey("A3+B1: a non-blocking Serve stays responsive while a background backfill converges", t, func() {
		// build a pre-upgrade DB: archived complete history whose RAW scan is
		// non-zero, but with the counter and backfill-marker buckets emptied so it
		// looks like a DB from before the maintained counter existed.
		testDB, _, err := initDB(ctx, serverConfig.DBFile, serverConfig.DBFileBackup,
			internal.Development, false, false)
		So(err, ShouldBeNil)

		archiveCounterJob(ctx, testDB, "echo a1", rgIA)
		archiveCounterJob(ctx, testDB, "echo a2", rgIA)
		archiveCounterJob(ctx, testDB, "echo b1", rgIB)
		archiveCounterJob(ctx, testDB, "echo c1", rgIC)
		archiveCounterJob(ctx, testDB, "echo c2", rgIC)
		archiveCounterJob(ctx, testDB, "echo c3", rgIC)

		raw, err := testDB.retrieveCompleteJobCountsByRepGroups(repGroups)
		So(err, ShouldBeNil)
		So(raw, ShouldResemble, expected)

		clearCounterBuckets(testDB)

		// pre-upgrade state: the maintained counters disagree with the RAW scan
		// (empty => 0), no markers, no sentinel, so a genuine backfill is needed.
		maintained, err := testDB.retrieveMaintainedCompleteCounts(repGroups)
		So(err, ShouldBeNil)
		So(maintained, ShouldResemble, map[string]int{rgIA: 0, rgIB: 0, rgIC: 0})

		for _, rg := range repGroups {
			So(repGroupHasMarker(testDB, rg), ShouldBeFalse)
		}

		So(backfillSentinelSet(testDB), ShouldBeFalse)
		So(testDB.close(ctx), ShouldBeNil)

		// start the manager against that same DB file; Serve launches the A3
		// backfill in its own goroutine (startCounterBackfill) after clients are
		// already being served, so readiness never waits on it.
		serverConfig.dontWipeDevDB = true
		server, _, token, err := serve(ctx, serverConfig)
		serverConfig.dontWipeDevDB = false

		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		// responsiveness is checked BEFORE we wait for the backfill, so these
		// answers cannot be gated on it: Ping, status and Add+Reserve of a brand
		// new job all complete within the 2s window.
		start := time.Now()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		_, err = jq.Ping(2 * time.Second)
		So(err, ShouldBeNil)

		_, err = jq.GetStatusByRepGroupMatch(rgIA, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)

		newRG := "integ_bf_new"
		newJob := &Job{
			Cmd: restFormTrue + " " + newRG, Cwd: testCwdPath, RepGroup: newRG,
			ReqGroup: newRG, Requirements: reqs, Retries: 0,
		}
		added, _, err := jq.Add([]*Job{newJob}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, newJob.Key())

		So(time.Since(start), ShouldBeLessThan, 2*time.Second)

		// now let the background backfill finish and assert it converged every
		// repGroup's maintained counter to the RAW scan, with markers + sentinel.
		So(waitForBackfill(server, repGroups), ShouldBeTrue)

		counts := counterMatchesRaw(server.db, repGroups...)
		So(counts, ShouldResemble, expected)

		for _, rg := range repGroups {
			So(repGroupHasMarker(server.db, rg), ShouldBeTrue)
		}

		So(backfillSentinelSet(server.db), ShouldBeTrue)
	})
}

// waitForBackfill blocks until the A3 background backfill has converged (see
// backfillConverged) or backfillWaitTimeout elapses, returning whether it
// converged in time.
func waitForBackfill(server *Server, repGroups []string) bool {
	deadline := time.Now().Add(backfillWaitTimeout)
	for time.Now().Before(deadline) {
		if backfillConverged(server, repGroups) {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return backfillConverged(server, repGroups)
}

// backfillConverged reports whether the A3 background backfill has finished: the
// fully-backfilled sentinel is set AND every repGroup's maintained counter
// equals the RAW scan. It swallows transient read errors (returning false) so it
// is safe to call repeatedly from a poll loop without So(), which must not run in
// a tight loop.
func backfillConverged(server *Server, repGroups []string) bool {
	var sentinel bool

	if err := server.db.bolt.View(func(tx *bolt.Tx) error {
		sentinel = tx.Bucket(bucketRepGroupBackfilled).Get(backfillSentinelKey) != nil

		return nil
	}); err != nil || !sentinel {
		return false
	}

	maintained, err := server.db.retrieveMaintainedCompleteCounts(repGroups)
	if err != nil {
		return false
	}

	raw, err := server.db.retrieveCompleteJobCountsByRepGroups(repGroups)
	if err != nil {
		return false
	}

	for _, rg := range repGroups {
		if maintained[rg] != raw[rg] {
			return false
		}
	}

	return true
}
