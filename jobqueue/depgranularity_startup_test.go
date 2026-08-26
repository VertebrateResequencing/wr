//go:build !windows

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

// This file covers spec section E: the manager is invisible until prior-state
// recovery completes, and everything that observes, survives or measures that
// startup window.

import (
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dgsIncompleteJobs is how many prior incomplete jobs the startup-window
	// tests recover, matching E1 acceptance test 3.
	dgsIncompleteJobs = 3

	dgsRepGroup = "depgranularity-startup"

	// dgsServingWait is a hang detector, not a latency budget: it costs nothing
	// on the success path, and only fires when publication never happens at all.
	// If it ever fires spuriously, the answer is a larger bound.
	dgsServingWait = 30 * time.Second

	// dgsClosedWait is how long something that should NOT happen is given to
	// prove it does not: a port that should be closed, or a publication that
	// should not have occurred. It is paid in full on every run, so it is short;
	// what makes it sound is that each thing it waits on has already been
	// sequenced by something observable (Serve returning, or publishExit being
	// called).
	dgsClosedWait = 200 * time.Millisecond

	// dgsExpiredCertBits is the RSA key size of the deliberately-expired
	// certificate E1 acceptance test 6 uses. It matches the manager's own root
	// key size, since a smaller one is a lint finding rather than a saving.
	dgsExpiredCertBits = 2048

	// dgsStopReleaseDelay is how long a shutdown-during-the-window test waits
	// before releasing the parked recovery, so beginShutdown has provably run
	// first.
	dgsStopReleaseDelay = 100 * time.Millisecond

	// dgsStopBound is how long Stop may take after the parked recovery is
	// released. The figure is load-bearing: it must be well under
	// ServerShutdownWaitTime, which is exactly 5s, or the test passes whether or
	// not the never-closing clientHandlingDone wait was skipped, and so never
	// fails pre-fix.
	dgsStopBound = 2 * time.Second

	// dgsHeartbeatInterval is the recovery heartbeat the sidecar tests run with,
	// well under their sampling window, and dgsHeartbeatTicks how many of them
	// they wait between samples. The production interval is a minute, so without
	// lowering it a paused recovery's sidecar would never be refreshed inside a
	// test.
	dgsHeartbeatInterval = 50 * time.Millisecond
	dgsHeartbeatTicks    = 4

	// dgsSidecarPollInterval is how often a sidecar poll re-reads the file.
	dgsSidecarPollInterval = 5 * time.Millisecond

	// dgsRunnerRetryWait and dgsRunnerRetryTime stand in for ClientRetryWait and
	// ClientRetryTime (15s and 24h): E8's scenario is about surviving an absence
	// longer than one retry interval, which needs a short interval and a bounded
	// total, not a real day. dgsRunnerRetryRounds is how many intervals the test
	// keeps the manager down for, so the archive provably retries across the
	// outage rather than catching its first attempt after it.
	dgsRunnerRetryWait = 200 * time.Millisecond
	dgsRunnerRetryTime = 2 * time.Minute

	// dgsRunnerOutage is how long the manager stays down. It must exceed the
	// client's request deadline, which Connect sets from its timeout (1.5s in
	// these tests): mangos queues the send and holds the receive open, so a
	// shorter outage lets the archive's FIRST attempt land on the restarted
	// manager and the retry loop is never entered - which is a false PASS, caught
	// by the hadProblems assertion.
	dgsRunnerOutage = 2500 * time.Millisecond

	// dgsRunnerItemTTR keeps the recovered running job reservable for as long as
	// the retrying archive could need. It is free on the success path.
	dgsRunnerItemTTR = 5 * time.Minute

	// dgsPhaseJobs is how many live jobs E9 acceptance test 1 starts on: enough
	// to produce every phase line, small enough to leave make test at its
	// baseline.
	dgsPhaseJobs = 100
)

// dgsPhaseLines are the messages of the per-phase log lines, in the order
// startup emits them. They are the five phases E9 asks to be measured
// separately: initDB (open plus mmap), the live-bucket decode, the
// dependency-group state build, the dependency resolution pass, and
// enqueueItems.
//
//nolint:gochecknoglobals // a test fixture list, not state
var dgsPhaseLines = []string{
	"recovering: opened database",
	"recovering: decoded live jobs",
	"recovering: built dependency-group state",
	"recovering: resolved prior job dependencies",
	"recovering: enqueued prior jobs",
}

// TestDepGranularityStartupSidecarNamesTheRealPhase pins the FIRST phase the
// sidecar reports, the span from the database being open to prior-state recovery
// starting, in which the manager sets up sockets, certificates, this host's IP
// and the scheduler.
//
// Spec E4 made this file be written on EVERY start, not only after a database
// upgrade. The phase it names therefore has to depend on whether an upgrade
// actually happened: reporting the post-upgrade phase unconditionally has every
// reader of the sidecar - `wr manager start`'s log line and `wr manager status`
// alike - tell an operator about a database upgrade that never took place.
//
// It drives the reporter directly, because the phase moves on within
// milliseconds of Serve reaching it and no server-level test could sample it
// without a manufactured flake. The assertions are on the sidecar file itself,
// which is the operator-visible artefact.
func TestDepGranularityStartupSidecarNamesTheRealPhase(t *testing.T) {
	if runnermode || servermode {
		return
	}

	logger := resolveServerLogger(ServerConfig{})

	Convey("A start that upgraded nothing does not claim a database upgrade", t, func() {
		dbFile := filepath.Join(t.TempDir(), "db")

		reporter := newStartupStatusReporter(dbFile, logger, false)

		defer reporter.remove()

		status, _, err := internal.ReadDBUpgradeStatus(dbFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, internal.DBStartupPrepareState)
		So(internal.IsDBUpgradeState(status.State), ShouldBeFalse)
		So(status.State, ShouldNotEqual, internal.DBUpgradePostStartupState)
		So(status.Detail, ShouldNotEqual, internal.DBUpgradePostStartupDetail)
	})

	Convey("A start that did upgrade the database on open says so", t, func() {
		dbFile := filepath.Join(t.TempDir(), "db")

		reporter := newStartupStatusReporter(dbFile, logger, true)

		defer reporter.remove()

		status, _, err := internal.ReadDBUpgradeStatus(dbFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, internal.DBUpgradePostStartupState)
		So(status.Detail, ShouldEqual, internal.DBUpgradePostStartupDetail)
	})
}

// dgsArchiveOutcome is what a retrying archive came back with: whether it
// eventually succeeded, and whether it had to retry to get there.
type dgsArchiveOutcome struct {
	worked      bool
	hadProblems bool
}

// TestDepGranularityRunnerSurvivesLongAbsence covers both E8 acceptance tests.
// The whole justification for making startup blocking again (spec E1) is that a
// runner which had connected before the manager went down keeps retrying for up
// to ClientRetryTime, so a 20-40 minute startup window is immaterial to it.
// ClientRetryTime being a constant is not the same as proving that, so this pins
// it: a reserved job's archive retries across a stop and a restart whose recovery
// window is longer than one client retry interval, and the job then ends complete
// exactly once without ever having been run twice.
//
// The timings come from the server's own RetryWait/RetryTime knobs rather than
// waiting 24 hours, and the archive is driven through reportFinalState, which is
// the retry loop production's Execute uses.
func TestDepGranularityRunnerSurvivesLongAbsence(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A reserved job's archive survives a stop and a slow restart", t, func() {
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true
		serverConfig.Timings.RetryWait = dgsRunnerRetryWait
		serverConfig.Timings.RetryTime = dgsRunnerRetryTime
		// the recovered running job must still be in Run when the retrying archive
		// finally lands, and nothing here touches it: the default 1s test TTR lets
		// it be reclaimed as lost first, which turns the archive into a terminal
		// ErrMustReserve and makes this test a load flake. The subject is the
		// client's retry across an absence, not TTR reclamation.
		serverConfig.Timings.ItemTTR = dgsRunnerItemTTR

		server, _, token, err := serveWithoutPublication(ctx, serverConfig)
		So(err, ShouldBeNil)
		So(dgsWaitServing(server), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := testDBJob("echo dgs long absence", dgsRepGroup)
		added, existed, err := jq.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		reserved, err := jq.Reserve(dgrReserveWait)
		So(err, ShouldBeNil)
		So(reserved != nil, ShouldBeTrue)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		key := reserved.Key()

		server.Stop(ctx, true)

		// the archive now has nothing to talk to, and retries. Its retry interval
		// is shorter than the window the restart below opens, so it provably
		// retries across it rather than happening to catch the first attempt.
		archived := make(chan dgsArchiveOutcome, 1)

		go func() {
			worked, hadProblems := jq.reportFinalState(ctx, reserved, dgsEndState(), execAction{archive: true})
			archived <- dgsArchiveOutcome{worked: worked, hadProblems: hadProblems}
		}()

		<-time.After(dgsRunnerOutage)

		restarted, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, restarted, release)()

		// nothing is reservable or archivable while recovery is parked, so the
		// runner is still retrying at this point.
		So(restarted.q.Stats().Items, ShouldEqual, 0)

		release()
		So(dgsWaitServing(restarted), ShouldBeTrue)
		So(waitUntilRecovered(restarted), ShouldBeTrue)

		select {
		case outcome := <-archived:
			So(outcome.worked, ShouldBeTrue)
			// hadProblems is what proves the retry loop really retried rather than
			// its first attempt happening to land after the restart: without it a
			// client that gave up on any transient error would still pass.
			So(outcome.hadProblems, ShouldBeTrue)
		case <-time.After(dgsRunnerRetryTime):
			So("the archive never succeeded", ShouldBeBlank)
		}

		// complete exactly once, and gone from the live queue: exactly one record
		// for the key, one attempt on it, and nothing left over.
		complete, err := restarted.db.checkIfComplete(key)
		So(err, ShouldBeNil)
		So(complete, ShouldBeTrue)
		So(restarted.q.Stats().Items, ShouldEqual, 0)

		done, err := restarted.db.retrieveCompleteJobsByKeys([]string{key})
		So(err, ShouldBeNil)
		So(done, ShouldHaveLength, 1)
		So(done[0].State, ShouldEqual, JobStateComplete)
		So(done[0].Exited, ShouldBeTrue)
		So(done[0].Attempts, ShouldEqual, 1)
	})
}

// dgsWaitServing waits for the server to publish its externally observable
// surface, reporting whether it did so within dgsServingWait.
func dgsWaitServing(server *Server) bool {
	select {
	case <-server.Serving():
		return true
	case <-time.After(dgsServingWait):
		return false
	}
}

// dgsEndState is a successful end state for a job the test never really ran.
func dgsEndState() *JobEndState {
	return &JobEndState{
		Cwd: testCwd, Exitcode: 0, PeakRAM: 1, CPUtime: time.Millisecond,
		EndTime: time.Now(), Exited: true,
	}
}

// dgsCleanup is the teardown every paused-recovery test must defer as soon as it
// has its server, BEFORE any assertion.
//
// Releasing is not optional even on the failure path: stopBackgroundStartupTasks
// calls bgWG.Wait(ServerShutdownWaitTime) and that wait does not time out (the
// duration only schedules the log of unfinished tasks), so a Stop with recovery
// still parked at the hook never returns. GoConvey's FailureHalts abandons the
// rest of the block on the first failed assertion, so a release written into the
// body rather than a defer would be skipped exactly when it is needed, wedging
// Stop and leaking the held bolt file lock and both ports into the rest of the
// package run. Both funcs are idempotent, so a test that also releases and stops
// in its own body can defer this as well.
func dgsCleanup(ctx context.Context, server *Server, release func()) func() {
	return func() {
		release()
		server.Stop(ctx, true)
	}
}

// TestDepGranularityStartupWindowIsInvisible covers E1 acceptance test 1: while
// prior-state recovery is still running, nothing about the manager is externally
// observable. That is the whole point of the story: a dep group the in-memory
// state has not yet learned looks empty, and an empty seen group means
// satisfied, so a request served in this window could release a job ahead of its
// dependencies.
func TestDepGranularityStartupWindowIsInvisible(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A server whose prior-state recovery is still running is not reachable", t, func() {
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(server.isRecovering(), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(jq, ShouldBeNil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, ErrNoServer)

		So(waitForTLSWebPort(
			"localhost:"+serverConfig.WebPort,
			serverConfig.CAFile,
			serverConfig.CertDomain,
			dgsClosedWait,
		), ShouldBeFalse)
	})
}

// TestDepGranularityStartupPublishesAtRecoveryEnd covers E1 acceptance test 2:
// once recovery ends the manager publishes everything at once, and only then is
// it reachable.
func TestDepGranularityStartupPublishesAtRecoveryEnd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A server publishes its serving surface when prior-state recovery ends", t, func() {
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		release()
		So(dgsWaitServing(server), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sinfo, err := jq.Ping(connectTime)
		So(err, ShouldBeNil)
		So(sinfo != nil, ShouldBeTrue)

		So(waitForTLSWebPort(
			"localhost:"+serverConfig.WebPort,
			serverConfig.CAFile,
			serverConfig.CertDomain,
			dgsServingWait,
		), ShouldBeTrue)

		// the flag is only asserted once waitUntilRecovered has returned true.
		// Publication is a tail statement and finishRecovering is a defer, so
		// Serving() closes a sub-millisecond before the flag clears; asserting
		// straight off Serving() would contradict that deliberate ordering.
		So(waitUntilRecovered(server), ShouldBeTrue)
		So(server.isRecovering(), ShouldBeFalse)
	})
}

// TestDepGranularityStartupRecoversPriorJobsBeforePublishing covers E1
// acceptance test 3: nothing prior is in the queue while recovery is paused, and
// everything prior is reservable once publication has happened.
func TestDepGranularityStartupRecoversPriorJobsBeforePublishing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Prior incomplete jobs are enqueued before the server becomes reachable", t, func() {
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(server.q.Stats().Items, ShouldEqual, 0)

		restored, total := server.recoveryProgress()
		So(restored, ShouldEqual, 0)
		So(total, ShouldEqual, dgsIncompleteJobs)

		release()
		So(dgsWaitServing(server), ShouldBeTrue)

		restored, total = server.recoveryProgress()
		So(restored, ShouldEqual, dgsIncompleteJobs)
		So(total, ShouldEqual, dgsIncompleteJobs)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		reserved := 0

		for range dgsIncompleteJobs {
			job, errr := jq.Reserve(dgrReserveWait)
			So(errr, ShouldBeNil)

			if job != nil && job.RepGroup == dgsRepGroup {
				reserved++
			}
		}

		So(reserved, ShouldEqual, dgsIncompleteJobs)
	})
}

// TestDepGranularityStartupPublishesAfterFailedRecovery covers E1 acceptance
// test 4: publication hangs off recovery ENDING, not succeeding. A recovery that
// fails still publishes, because a manager that is up, holds the database lock
// and is invisible forever is worse than one that is up with jobs it could not
// restore - and wr manager start would poll it indefinitely.
//
// The queue is destroyed before the hook is released so recovery's enqueueItems
// fails with queue.ErrQueueClosed inside recoverPriorJobsAndNote. A corrupted job
// record is the wrong seam: that fails at decode inside db.recoverIncompleteJobs,
// which startPriorStateRecovery calls synchronously, so Serve returns the error
// and publication never runs at all - the opposite of what this test is for.
func TestDepGranularityStartupPublishesAfterFailedRecovery(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A server whose prior-state recovery fails still publishes", t, func() {
		ctx, logs := cmdLogSyncCapture(context.Background())
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(server.q.Destroy(), ShouldBeNil)

		release()
		So(dgsWaitServing(server), ShouldBeTrue)

		// handlePing reads only s.ServerInfo, so a published server with a
		// destroyed queue still answers a connect.
		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		So(waitUntilRecovered(server), ShouldBeTrue)
		So(logs.String(), ShouldContainSubstring, "prior-state recovery failed")
	})
}

// TestDepGranularityShutdownClosesServing covers E2 acceptance test 2: a caller
// waiting for publication must not wait forever on a server that is being
// stopped, so shutdown closes the channel too. Publication is skipped once bgCtx
// is cancelled, and stopBackgroundStartupTasks cancels it before it waits, so
// this receive really is shutdown's close and not a publication.
func TestDepGranularityShutdownClosesServing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Stopping a server inside the startup window closes Serving()", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		elapsed, stopped := dgsStopReleasingHook(ctx, server, release)
		So(stopped, ShouldBeTrue)
		So(elapsed, ShouldBeLessThan, dgsStopBound)

		So(dgsWaitServing(server), ShouldBeTrue)
		So(server.clientHandlingStarted(), ShouldBeFalse)
	})
}

// TestDepGranularityServingIsIdempotent covers E2 acceptance test 3: Serving()
// is a closed channel, not a one-shot signal, so every caller sees it; and
// publication racing shutdown must not double-close it.
func TestDepGranularityServingIsIdempotent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Serving() can be received from repeatedly, and racing closes do not panic", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		var wg sync.WaitGroup

		wg.Add(2)

		go func() {
			defer wg.Done()

			release()
		}()

		go func() {
			defer wg.Done()

			server.Stop(ctx, true)
		}()

		wg.Wait()

		So(dgsWaitServing(server), ShouldBeTrue)
		So(dgsWaitServing(server), ShouldBeTrue)
	})
}

// TestDepGranularityStopDuringWindow covers E3 acceptance test 1: wr manager
// stop must work inside the startup window, because the documented rollback
// procedure is "stop the manager and restore a pre-upgrade DB copy". Stop reaches
// a manager in the window, since the daemonized child writes its pid file before
// Serve.
func TestDepGranularityStopDuringWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Stop inside the startup window returns promptly and releases the database", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		elapsed, stopped := dgsStopReleasingHook(ctx, server, release)
		So(stopped, ShouldBeTrue)
		So(elapsed, ShouldBeLessThan, dgsStopBound)

		result, returned := dgbInitDB(ctx, serverConfig.DBFile, serverConfig.DBFileBackup)

		defer dgbClose(ctx, result)

		So(returned, ShouldBeTrue)
		So(result.err, ShouldBeNil)
	})
}

// TestDepGranularitySigtermDuringWindow covers E3 acceptance test 2: a SIGTERM
// inside the startup window shuts the manager down cleanly. It is delivered the
// way handleSignals delivers it - s.shutdown with the SIGTERM reason, wait, and
// signal handling left alone - because the sigs channel is a local of Serve and
// signalling the real process would tear down every other server in the test
// binary.
func TestDepGranularitySigtermDuringWindow(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A SIGTERM inside the startup window shuts the server down without panicking", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		released := make(chan struct{})

		go func() {
			<-time.After(dgsStopReleaseDelay)
			release()
			close(released)
		}()

		down := make(chan struct{})

		go func() {
			server.shutdown(ctx, ErrClosedTerm, true, false)
			close(down)
		}()

		select {
		case <-down:
		case <-time.After(dgsServingWait):
			So("SIGTERM shutdown did not complete", ShouldBeBlank)
		}

		<-released

		result, returned := dgbInitDB(ctx, serverConfig.DBFile, serverConfig.DBFileBackup)

		defer dgbClose(ctx, result)

		So(returned, ShouldBeTrue)
		So(result.err, ShouldBeNil)
	})
}

// TestDepGranularitySidecarReportsRecoveryPhase covers E4 acceptance tests 2 and
// 3: the sidecar is the primary operator channel during the window, and it is
// removed the moment the manager can answer for itself. The DB needs no upgrade,
// which pre-change wrote no sidecar at all.
func TestDepGranularitySidecarReportsRecoveryPhase(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A recovering server reports its phase in the sidecar and removes it at publication", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(server.db.upgradedOnOpen, ShouldBeFalse)

		status, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, internal.DBStartupRecoveryState)
		So(status.Total, ShouldEqual, dgsIncompleteJobs)

		release()
		So(dgsWaitServing(server), ShouldBeTrue)

		_, _, err = internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

// TestDepGranularitySidecarRemovedOnShutdown covers E4 acceptance test 4: the
// sidecar does not outlive a manager stopped inside the window. Asserting it is
// present first is what makes this discriminate: pre-change nothing was written
// at all for a DB needing no upgrade, so a bare "removed" assertion passed
// vacuously.
func TestDepGranularitySidecarRemovedOnShutdown(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Stopping a server inside the startup window removes the sidecar", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		status, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, internal.DBStartupRecoveryState)

		_, stopped := dgsStopReleasingHook(ctx, server, release)
		So(stopped, ShouldBeTrue)

		_, _, err = internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

// TestDepGranularitySidecarReportsElapsedTime covers E4 acceptance test 5: the
// recovery phase reports ELAPSED TIME, not a count. Recovery enqueues in one
// batch, so a Processed fed from its restored count would read a constant 0 for
// the whole multi-minute window and an operator watching 0/150472 would read a
// hang. A "between 0 and total" assertion would be satisfied by that constant 0
// and would test nothing, so this asserts a strictly growing elapsed time, a
// moving UpdatedAt, and an unset Processed.
//
// The pause is what makes two samples possible: with only three prior jobs an
// unpaused recovery finishes before the first tick. It does not cost the
// heartbeat, because startRecoveryHeartbeat runs before the hook fires.
func TestDepGranularitySidecarReportsElapsedTime(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("The sidecar's recovery phase reports a growing elapsed time", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsIncompleteJobs)

		defer dgsWithShortHeartbeat()()

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		first, found := dgsWaitForSidecarState(serverConfig.DBFile, internal.DBStartupRecoveryState)
		So(found, ShouldBeTrue)

		<-time.After(dgsHeartbeatInterval * dgsHeartbeatTicks)

		second, found := dgsWaitForSidecarState(serverConfig.DBFile, internal.DBStartupRecoveryState)
		So(found, ShouldBeTrue)

		So(first.Total, ShouldEqual, dgsIncompleteJobs)
		So(second.Total, ShouldEqual, dgsIncompleteJobs)
		So(first.Processed, ShouldEqual, 0)
		So(second.Processed, ShouldEqual, 0)
		So(second.UpdatedAt.After(first.UpdatedAt), ShouldBeTrue)
		So(dgsSidecarElapsed(second), ShouldBeGreaterThan, dgsSidecarElapsed(first))
	})
}

// TestDepGranularityStartupPhasesAreLogged covers E9 acceptance test 1: this
// change converts prior-state recovery into total unavailability, so every phase
// of it reports its own elapsed time, at warn level where the default log level
// shows it (as the committed recovery lines do). Without that an operator has no
// way to tell which phase a slow start is in.
//
// 100 live jobs is enough to produce every phase line and small enough to leave
// make test at its baseline; E9's scaling measurements live behind the
// reliability_repro tag.
func TestDepGranularityStartupPhasesAreLogged(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Every startup phase logs its elapsed time", t, func() {
		ctx, logs := cmdLogSyncCapture(context.Background())
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		dgsSeedIncompleteJobs(ctx, t, serverConfig, dgsPhaseJobs)

		server, _, _, err := serveWithoutPublication(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(dgsWaitServing(server), ShouldBeTrue)
		So(waitUntilRecovered(server), ShouldBeTrue)

		logged := logs.String()

		for _, phase := range dgsPhaseLines {
			So(dgsPhaseLogged(logged, phase), ShouldBeTrue)
		}
	})
}

// dgsSeedIncompleteJobs pre-populates config's DB with count incomplete
// (live-bucket) jobs, so a server started on that DB has that many prior jobs to
// recover. The caller must set config.dontWipeDevDB so Serve opens rather than
// wipes it.
func dgsSeedIncompleteJobs(ctx context.Context, t *testing.T, config ServerConfig, count int) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobs := make([]*Job, count)
	for i := range count {
		jobs[i] = testDBJob("echo dgs "+strconv.Itoa(i), dgsRepGroup)
	}

	jobsToQueue, jobsToUpdate, alreadyAdded, err := testDB.storeNewJobs(ctx, jobs, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, count)
	So(jobsToUpdate, ShouldHaveLength, 0)
	So(alreadyAdded, ShouldEqual, 0)
	So(testDB.close(ctx), ShouldBeNil)
}

// dgsPhaseLogged reports whether the captured log carries the named phase line at
// warn with an elapsed field.
func dgsPhaseLogged(logged, phase string) bool {
	for line := range strings.SplitSeq(logged, "\n") {
		warnWithElapsed := strings.Contains(line, "lvl=warn") && strings.Contains(line, "elapsed=")
		if strings.Contains(line, phase) && warnWithElapsed {
			return true
		}
	}

	return false
}

// dgsWithShortHeartbeat lowers recoveryHeartbeatInterval so a test can watch the
// sidecar being refreshed, returning the func that restores it.
func dgsWithShortHeartbeat() func() {
	original := recoveryHeartbeatInterval
	recoveryHeartbeatInterval = dgsHeartbeatInterval

	return func() { recoveryHeartbeatInterval = original }
}

// dgsWaitForSidecarState polls the sidecar until it reports the given state,
// returning it and whether it was seen.
func dgsWaitForSidecarState(dbFile, state string) (internal.DBUpgradeStatus, bool) {
	deadline := time.Now().Add(dgsServingWait)

	for time.Now().Before(deadline) {
		status, _, err := internal.ReadDBUpgradeStatus(dbFile)
		if err == nil && status.State == state {
			return status, true
		}

		<-time.After(dgsSidecarPollInterval)
	}

	return internal.DBUpgradeStatus{}, false
}

// dgsSidecarElapsed parses the elapsed time out of a recovery-phase sidecar's
// detail, reporting -1 when it does not carry one (so a test comparing two
// samples fails rather than silently comparing zeroes).
func dgsSidecarElapsed(status internal.DBUpgradeStatus) time.Duration {
	trimmed := strings.TrimSuffix(
		strings.TrimPrefix(status.Detail, startupRecoveryDetailPrefix),
		startupRecoveryDetailSuffix,
	)

	elapsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return -1
	}

	return elapsed
}

// dgsStopReleasingHook stops a server whose recovery is parked at the pause
// hook, releasing the hook once Stop has been entered, and returns how long Stop
// took to return after that release.
//
// The release is required, not tidiness: stopBackgroundStartupTasks calls
// bgWG.Wait(ServerShutdownWaitTime) and that wait does not time out (the
// duration only schedules the log of unfinished tasks), so a Stop with recovery
// parked at the hook never returns at all. A test that skipped the release would
// pass while leaking a wedged Stop, the held bolt file lock and both held ports
// into the rest of the package run.
func dgsStopReleasingHook(ctx context.Context, server *Server, release func()) (time.Duration, bool) {
	released := make(chan time.Time, 1)

	go func() {
		// the delay is what makes this a shutdown-during-the-window test:
		// beginShutdown has to have run first, or the release simply lets
		// publication happen and nothing under test is exercised.
		<-time.After(dgsStopReleaseDelay)
		release()

		released <- time.Now()
	}()

	stopped := make(chan struct{})

	go func() {
		server.Stop(ctx, true)
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(dgsServingWait):
		return 0, false
	}

	return time.Since(<-released), true
}

// TestDepGranularityStartupExitsWhenPortUnavailable covers the first half of E1
// acceptance test 5: an invisible manager holding the database lock is worse
// than a dead one, so publication that cannot bind the manager port within its
// retry budget exits the process, and returns immediately rather than falling
// through to start readers against an unbound socket.
//
// The order of three steps makes or breaks this test: observe publishExit, then
// close the test's own listener, then assert Connect. Connect against a plain
// listener that never speaks TLS would hang forever (mangos dials synchronously
// and the tls+tcp dialer has no handshake deadline), and while the test owns the
// port a failed Connect would say nothing about whether publication bound
// anything. Closing first is also what lets Stop return, since shutdown calls
// waitForPortsClosed unconditionally.
func TestDepGranularityStartupExitsWhenPortUnavailable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Publication exits the process when the manager port cannot be bound", t, func() {
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)

		var listenConfig net.ListenConfig

		listener, err := listenConfig.Listen(ctx, "tcp", "0.0.0.0:"+serverConfig.Port)
		So(err, ShouldBeNil)

		exits := make(chan int, 2)
		publishExit = func(code int) { exits <- code }

		defer func() { publishExit = os.Exit }()

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()
		// registered after the cleanup, so it runs BEFORE it: shutdown calls
		// waitForPortsClosed unconditionally, and that loop has no deadline, so a
		// Stop with the test still holding the manager port never returns.
		defer func() { _ = listener.Close() }()

		started := time.Now()

		release()

		var code int

		select {
		case code = <-exits:
		case <-time.After(dgsServingWait):
			So("timed out waiting for publication to give up on the manager port", ShouldBeBlank)
		}

		elapsed := time.Since(started)

		So(code, ShouldNotEqual, 0)
		So(elapsed, ShouldBeGreaterThanOrEqualTo, serverBindRetryBudget)

		// publication returns straight after publishExit, so the server is left
		// unpublished.
		So(dgsNotServing(server), ShouldBeTrue)

		So(listener.Close(), ShouldBeNil)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, server.token, connectTime)
		So(jq, ShouldBeNil)
		So(errc, ShouldNotBeNil)
		So(errc.Error(), ShouldContainSubstring, ErrNoServer)

		// asserted after the fact rather than inside a loop: publication returns
		// immediately after its single publishExit call, so nothing can add
		// another.
		So(exits, ShouldHaveLength, 0)
	})
}

// dgsNotServing reports whether the server has NOT published its externally
// observable surface, giving it dgsClosedWait to prove otherwise.
func dgsNotServing(server *Server) bool {
	select {
	case <-server.Serving():
		return false
	case <-time.After(dgsClosedWait):
		return true
	}
}

// TestDepGranularityStartupRetriesPortBind covers the second half of E1
// acceptance test 5: a transient bind failure must not kill the process. The
// bind moved out of Serve, where an in-use port came back as an error the caller
// retried, into the recovery goroutine where nothing can, so publication carries
// the retry itself.
func TestDepGranularityStartupRetriesPortBind(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Publication retries a manager port that is briefly in use", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		var listenConfig net.ListenConfig

		listener, err := listenConfig.Listen(ctx, "tcp", "0.0.0.0:"+serverConfig.Port)
		So(err, ShouldBeNil)

		exits := make(chan int, 2)
		publishExit = func(code int) { exits <- code }

		defer func() { publishExit = os.Exit }()

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()
		defer func() { _ = listener.Close() }()

		go func() {
			<-time.After(serverBindRetryInterval * 2)

			_ = listener.Close()
		}()

		release()

		So(dgsWaitServing(server), ShouldBeTrue)
		So(exits, ShouldHaveLength, 0)
	})
}

// TestDepGranularityStartupFailsFastOnExpiredCert covers E1 acceptance test 6:
// the fast-fail certificate path is preserved. Everything that can fail on bad
// input still fails inside Serve, before recovery is launched, so wr manager
// start still dies cleanly rather than 20 minutes later inside the recovery
// goroutine.
func TestDepGranularityStartupFailsFastOnExpiredCert(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Serve fails before launching recovery when a certificate has expired", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		expiredCA := filepath.Join(t.TempDir(), "expired-ca.pem")
		dgsWriteExpiredCert(t, expiredCA)
		serverConfig.CAFile = expiredCA

		server, _, _, err := Serve(ctx, serverConfig)
		So(err, ShouldNotBeNil)
		So(server == nil, ShouldBeTrue)
		So(err.Error(), ShouldContainSubstring, string(internal.ErrExpiredCert))
	})
}

// dgsWriteExpiredCert writes a self-signed certificate that expired an hour ago
// to path, so earliestCertExpiry rejects it. internal has no API for generating
// one, and a committed fixture would itself expire.
func dgsWriteExpiredCert(t *testing.T, path string) {
	t.Helper()

	key, err := rsa.GenerateKey(crand.Reader, dgsExpiredCertBits)
	So(err, ShouldBeNil)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: localhost},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(-time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	So(err, ShouldBeNil)

	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	So(os.WriteFile(path, encoded, ownerReadWrite), ShouldBeNil)
}

// TestDepGranularityStartupFailsFastOnMismatchedKeypair covers E1 acceptance
// test 7: a keypair that does not match is a tls.LoadX509KeyPair error from
// Serve, not a failure raised inside the recovery goroutine minutes later.
func TestDepGranularityStartupFailsFastOnMismatchedKeypair(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Serve fails before launching recovery when the keypair does not match", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		serverConfig.KeyFile = dgsForeignKeyFile(t, serverConfig.CertDomain)

		server, _, _, err := Serve(ctx, serverConfig)
		So(err, ShouldNotBeNil)
		So(server == nil, ShouldBeTrue)
		So(err.Error(), ShouldContainSubstring, "private key does not match public key")
	})
}

// TestDepGranularitySidecarRemovedOnFailedStart covers E4 acceptance test 6:
// moving the removal off Serve's defer uncovered Serve's error path, where
// neither publication nor shutdown runs, so an error-only removal stays on the
// defer. The mismatched keypair of E1 acceptance test 7 fails in
// prepareListener, which is after initDB and so after the sidecar exists.
func TestDepGranularitySidecarRemovedOnFailedStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A failed start does not leave its sidecar behind", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.KeyFile = dgsForeignKeyFile(t, serverConfig.CertDomain)

		server, _, _, err := Serve(ctx, serverConfig)
		So(err, ShouldNotBeNil)
		So(server == nil, ShouldBeTrue)

		_, _, err = internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

// dgsForeignKeyFile generates a whole new CA/cert/key set in a temp dir and
// returns the path of its key, which does not match the caller's certificate.
func dgsForeignKeyFile(t *testing.T, certDomain string) string {
	t.Helper()

	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.pem")

	So(internal.GenerateCerts(
		filepath.Join(dir, "ca.pem"),
		filepath.Join(dir, "cert.pem"),
		keyFile,
		certDomain,
		internal.DefaultBitsForRootRSAKey,
		internal.DefualtBitsForServerRSAKey,
		crand.Reader,
		internal.DefaultCertFileFlags,
	), ShouldBeNil)

	return keyFile
}

// TestDepGranularityServingMeansReachable covers E2 acceptance test 1: waiting
// on Serving() is a race-free way to know the manager is reachable, so a caller
// that waits needs no retry of its own. Publication brings the web interface up
// first and waits serverListenWait before the RPC bind, so a caller that did not
// wait would get ErrNoServer on every run, not intermittently.
func TestDepGranularityServingMeansReachable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A single Connect succeeds once Serving() has returned", t, func() {
		config, serverConfig, addr, _, connectTime := jobqueueTestInit(true)

		server, _, token, err := serveWithoutPublication(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(dgsWaitServing(server), ShouldBeTrue)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, connectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sinfo, err := jq.Ping(connectTime)
		So(err, ShouldBeNil)
		So(sinfo != nil, ShouldBeTrue)
	})
}

// TestDepGranularityStopAfterPublication covers E3 acceptance test 3: skipping
// the reader teardown inside the window must not change what a published
// server's shutdown does. The readers really did start, so clientHandlingDone
// closes, the HTTP server shuts down, and no timeout warning is logged.
func TestDepGranularityStopAfterPublication(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Stopping a published server still tears the readers down", t, func() {
		ctx, logs := cmdLogSyncCapture(context.Background())
		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		server, _, _, err := serveWithoutPublication(ctx, serverConfig)
		So(err, ShouldBeNil)
		So(dgsWaitServing(server), ShouldBeTrue)
		So(server.clientHandlingStarted(), ShouldBeTrue)

		server.Stop(ctx, true)

		select {
		case <-server.clientHandlingDone:
		case <-time.After(dgsClosedWait):
			So("client handling did not stop", ShouldBeBlank)
		}

		So(logs.String(), ShouldNotContainSubstring, "timed out waiting for client handling")
	})
}
