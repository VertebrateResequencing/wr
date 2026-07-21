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

// This file covers spec.md section H1 (KEEP-feature regression coverage,
// acceptance #4): the post-v0.36.5 features that must keep working after the
// #533 count-machinery removal (Phases 1-4). These are focused acceptance
// checks that COMPLEMENT (do not duplicate or weaken) the existing KEEP anchor
// suites (subscription_test.go, live_jtouch_test.go, serverWebI_test.go,
// suspend_resume_test.go, modify_validation_test.go and the AddAndWait tests).
// They assert the behaviour at the in-process jobqueue API / subscription layer
// so they run reliably under -race. The four blocks map 1:1 to H1 acceptance
// tests #4.1-#4.4.

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2KeepLiveSubscription maps to H1 acceptance #4.1: a subscriber
// following a job new->running->complete still receives per-job JobUpdates AND
// a live RAM/CPU/STDOUT snapshot from a touch (emitLiveTouchSnapshot), unchanged
// by the #533 removal.
func TestReliable2KeepLiveSubscription(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A per-job subscriber gets a live touch snapshot and a terminal complete update", t, func() {
		server, jq, runner, _, standardReqs := startSubscriptionIntegration(ctx, t)
		defer server.Stop(ctx, true)
		defer disconnect(jq)
		defer disconnect(runner)

		// add + reserve + start (new -> running) a job set up for live
		// introspection (cloud_user + host/pid), exactly as the #530/#534 anchor
		// does.
		job := addAndStartLiveSubscriptionJob(server, jq, runner, standardReqs, "reliable2-h1-live")

		sub, err := jq.SubscribeToJobKeys(ctx, []string{job.Key()})
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		// a runner touch delivers a live RAM/CPU/STDOUT snapshot to the per-job
		// subscriber (emitLiveTouchSnapshot).
		_, err = runner.touch(job, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte("out\n")),
			Stderr:  compressStd([]byte("err\n")),
		})
		So(err, ShouldBeNil)

		updates, ok := collectSubscriptionUpdates(sub, 1)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, 1)
		assertLiveJobUpdate(updates[0], job.Key())

		// a successful archive (running -> complete) still delivers the per-job
		// terminal update with the real complete state.
		So(runner.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		term := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(term, ShouldNotBeNil)
		So(term.Kind, ShouldEqual, JobUpdateTerminal)
		So(term.Key, ShouldEqual, job.Key())
		So(term.State, ShouldEqual, JobStateComplete)
	})
}

// TestReliable2KeepReconnectResync maps to H1 acceptance #4.2: a subscriber that
// reconnects mid-run (its manager restarts) receives a JobUpdateResync marker
// and then catches up with the job's terminal event.
func TestReliable2KeepReconnectResync(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A subscriber reconnecting mid-run receives a resync marker then catches up", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		applySubscriptionReconnectTimings(&serverConfig, 250*time.Millisecond, 2*time.Second)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("reliable2-h1-resync", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		// drop the manager and the subscription socket mid-run, then bring the
		// manager back so the subscription reconnects.
		server.Stop(ctx, true)
		sub.closeSock()
		time.Sleep(100 * time.Millisecond)

		server = restartSubscriptionTestServer(ctx, serverConfig)

		// recovery runs in the background (spec B1/H2); wait for the prior running
		// job to be restored before archiving it, else the archive races the
		// recovery window and is refused with ErrRecovering.
		So(waitUntilRecovered(server), ShouldBeTrue)

		catchUpClient, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		catchUpClient.clientid = jq.clientid

		defer disconnect(catchUpClient)

		So(catchUpClient.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		updates, ok := collectSubscriptionUpdates(sub, 2)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, 2)
		So(updates[0].Kind, ShouldEqual, JobUpdateResync)
		So(updates[1].Kind, ShouldEqual, JobUpdateTerminal)
		So(updates[1].Key, ShouldEqual, ids[0])
		So(updates[1].State, ShouldEqual, JobStateComplete)
		So(sub.Err(), ShouldBeNil)
	})
}

// TestReliable2KeepActions maps to H1 acceptance #4.3: a completed job Reruns,
// and an incomplete job's Modify and Suspend/Resume still work, with the
// suspended-state listing that backs `wr status --suspended` still returning the
// suspended job. Asserted at the jobqueue API layer (the web/REST rerun path is
// already covered robustly by serverWebI_test.go).
func TestReliable2KeepActions(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Rerun, modify and suspend/resume still work on an unchanged jobqueue", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("A completed job can be rerun back to ready", func() {
			const rg = "reliable2-h1-rerun"

			ids, erra := jq.AddAndReturnIDs(subscriptionTestJobs(rg, standardReqs, 1), envVars, true)
			So(erra, ShouldBeNil)
			So(ids, ShouldHaveLength, 1)

			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved.Key(), ShouldEqual, ids[0])
			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
			So(jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

			complete, errg := jq.GetByRepGroup(rg, false, 0, JobStateComplete, false, false)
			So(errg, ShouldBeNil)
			So(complete, ShouldHaveLength, 1)

			// rerun: re-add the same job with ignoreComplete=false returns it to
			// ready (the jobqueue-API rerun path).
			_, _, erra = jq.Add(subscriptionTestJobs(rg, standardReqs, 1), envVars, false)
			So(erra, ShouldBeNil)

			So(pollUntil(func() bool {
				ready, errp := jq.GetByRepGroup(rg, false, 0, JobStateReady, false, false)

				return errp == nil && len(ready) == 1
			}), ShouldBeTrue)

			ready, errg := jq.GetByRepGroup(rg, false, 0, JobStateReady, false, false)
			So(errg, ShouldBeNil)
			So(ready, ShouldHaveLength, 1)
			So(ready[0].Key(), ShouldEqual, ids[0])
			So(ready[0].Exited, ShouldBeFalse)
			So(ready[0].Attempts, ShouldEqual, 0)
		})

		Convey("An incomplete job can be modified", func() {
			const rg = "reliable2-h1-modify"

			job := &Job{
				Cmd: "echo reliable2 h1 modify", Cwd: testCwd,
				ReqGroup: rg, Requirements: standardReqs, RepGroup: rg,
			}
			added, _, erra := jq.Add([]*Job{job}, envVars, true)
			So(erra, ShouldBeNil)
			So(added, ShouldEqual, 1)

			jm := NewJobModifer()
			jm.SetPriority(42)

			modified, errm := jq.Modify([]*JobEssence{job.ToEssense()}, jm)
			So(errm, ShouldBeNil)
			So(modified, ShouldHaveLength, 1)

			got, errg := jq.GetByRepGroup(rg, false, 0, JobStateReady, false, false)
			So(errg, ShouldBeNil)
			So(got, ShouldHaveLength, 1)
			So(got[0].Priority, ShouldEqual, 42)
		})

		Convey("An incomplete job can be suspended, listed as suspended, then resumed", func() {
			const rg = "reliable2-h1-suspend"

			job := &Job{
				Cmd: "echo reliable2 h1 suspend", Cwd: testCwd,
				ReqGroup: rg, Requirements: standardReqs, RepGroup: rg,
			}
			added, _, erra := jq.Add([]*Job{job}, envVars, true)
			So(erra, ShouldBeNil)
			So(added, ShouldEqual, 1)

			changed, errs := jq.Suspend([]*JobEssence{job.ToEssense()})
			So(errs, ShouldBeNil)
			So(changed, ShouldEqual, 1)

			// the suspended-state listing is what `wr status --suspended` shows.
			suspended, errg := jq.GetByRepGroup(rg, false, 0, JobStateSuspended, false, false)
			So(errg, ShouldBeNil)
			So(suspended, ShouldHaveLength, 1)
			So(suspended[0].Key(), ShouldEqual, job.Key())
			So(suspended[0].State, ShouldEqual, JobStateSuspended)

			changed, errr := jq.Resume([]*JobEssence{job.ToEssense()})
			So(errr, ShouldBeNil)
			So(changed, ShouldEqual, 1)

			ready, errg := jq.GetByRepGroup(rg, false, 0, JobStateReady, false, false)
			So(errg, ShouldBeNil)
			So(ready, ShouldHaveLength, 1)
			So(ready[0].Key(), ShouldEqual, job.Key())
		})
	})
}

// TestReliable2KeepAddSync maps to H1 acceptance #4.4: `wr add --sync` for a
// completing command returns on completion via the subscription (non-polling),
// unchanged. cmd/add.go's waitForSynchronousJob is a thin wrapper over
// AddAndWait, which blocks on the per-job subscription (not a poll loop), so it
// is exercised here at the jobqueue-client layer.
func TestReliable2KeepAddSync(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("AddAndWait (wr add --sync) returns on completion via the subscription", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		input := subscriptionTestJobs("reliable2-h1-sync", standardReqs, 1)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		// a separate runner completes the command; AddAndWait must return purely
		// because the terminal event arrives over its subscription.
		archiveNextAddAndWaitJob(runner)

		result := receiveAddAndWaitResult(resultCh, 6*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
		So(result.jobs[0].Key(), ShouldEqual, input[0].Key())
	})
}
