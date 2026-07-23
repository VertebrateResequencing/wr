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

// This file covers spec.md section C1 and C2 acceptance tests. C1: a reserved
// job carries the reserving runner's host+pid immediately (before Started), and
// an old-client reserve request that carries no host+pid still reserves
// successfully with the job's Pid left at 0. C2: a reserved-not-started job
// whose TTR expires is parked (marked Lost, kept in SubQueueRun) and requeued
// only after its runner is confirmed dead - so an alive owner's job is never
// re-reserved, a confirmed-dead owner's job is reclaimed (no hole), and an
// old-client (pid 0) job parks safely rather than being blindly re-reserved.

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2Reserve covers spec.md section C1 acceptance tests 1 and 2.
func TestReliable2Reserve(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a running server and a connected client", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("C1.1: a reserved job records the reserving runner's host+pid before Started", func() {
			const rg = "reliable2_reserve_hostpid_rg"

			job := &Job{
				Cmd: restFormTrue + " hostpid", Cwd: testCwdPath, RepGroup: rg,
				ReqGroup: rg, Requirements: standardReqs, Retries: 30,
			}
			_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
			So(err, ShouldBeNil)

			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			// The reserving process is this test process, and the client stamps its
			// own hostname (falling back to localhost) the same way.
			expectedHost, expectedPid := reserveHostAndPid()

			// Read the server-side job BEFORE any Started call.
			host, pid, ok := serverJobHostPid(server, reserved.Key())
			So(ok, ShouldBeTrue)
			So(host, ShouldEqual, expectedHost)
			So(pid, ShouldEqual, expectedPid)
			So(pid, ShouldEqual, os.Getpid())
		})

		Convey("C1.2: an old-client reserve (no host+pid) succeeds with the job's Pid left at 0", func() {
			const rg = "reliable2_reserve_oldclient_rg"

			job := &Job{
				Cmd: restFormTrue + " oldclient", Cwd: testCwdPath, RepGroup: rg,
				ReqGroup: rg, Requirements: standardReqs, Retries: 30,
			}
			_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
			So(err, ShouldBeNil)

			// Mirror how the real client builds a reserve request, but omit the
			// additive Host/Pid fields (the old-client wire shape). White-box: the
			// test is in package jobqueue, so it can send a raw clientRequest.
			resp, errr := jq.request(&clientRequest{Method: requestMethodReserve, Timeout: 2 * time.Second, FirstReserve: true})
			So(errr, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.Job, ShouldNotBeNil)

			// The reservation succeeded, and the server-side job's Pid is 0 (and
			// Host empty) because the old client sent neither.
			host, pid, ok := serverJobHostPid(server, resp.Job.Key())
			So(ok, ShouldBeTrue)
			So(pid, ShouldEqual, 0)
			So(host, ShouldEqual, "")
		})
	})
}

// TestReliable2ReserveAliveOwnerNotReReserved covers spec.md section C2
// acceptance test 1: a reserved-but-never-started job whose recorded pid is
// alive (this test process) is parked in SubQueueRun with Lost==true when its
// TTR expires, and a second client cannot re-reserve it.
func TestReliable2ReserveAliveOwnerNotReReserved(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "reliable2_reserve_alive_owner_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A reserved-not-started job with an alive owner parks in Run and is not re-reserved", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " aliveowner", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 0,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		key := reserved.Key()

		// the reserving process (this test) is alive, so its pid was recorded and
		// the async confirm-dead check will never confirm death.
		_, pid, ok := serverJobHostPid(server, key)
		So(ok, ShouldBeTrue)
		So(pid, ShouldEqual, os.Getpid())

		// deliberately never call Started; wait for the TTR to mark the
		// reserved-not-started job Lost (allow a few TTRs so it is not flaky).
		So(waitForJobLost(server, key, 6*ttr), ShouldBeTrue)

		// it stays parked in SubQueueRun (un-reservable), not sent to delay.
		inRun, lost, failReason, okj := serverJobState(server, key)
		So(okj, ShouldBeTrue)
		So(inRun, ShouldBeTrue)
		So(lost, ShouldBeTrue)
		So(failReason, ShouldEqual, FailReasonLost)

		// a second client's 20 Reserve(200ms) calls all return nil - the
		// alive-owned parked job cannot be re-reserved.
		reReserved, errs := countReReserves(addr, config.ManagerCAFile, config.ManagerCertDomain,
			token, clientConnectTime, 20)
		So(errs, ShouldEqual, 0)
		So(reReserved, ShouldEqual, 0)

		// and it is still parked Lost in Run afterwards.
		inRun2, lost2, _, okj2 := serverJobState(server, key)
		So(okj2, ShouldBeTrue)
		So(inRun2, ShouldBeTrue)
		So(lost2, ShouldBeTrue)
	})
}

// TestReliable2ReserveOldClientParks covers spec.md section C2 acceptance test
// 3: a reserved-not-started job with Pid==0 (old-client shape) is parked in
// SubQueueRun when its TTR expires (never confirmed dead) and a second client's
// repeated Reserve returns nil.
func TestReliable2ReserveOldClientParks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "reliable2_reserve_oldclient_park_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A reserved-not-started job with Pid==0 parks in Run and is not re-reserved", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " oldclientpark", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 0,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		// old-client reserve (no host+pid) -> the job's recorded Pid is 0.
		resp, errr := jq.request(&clientRequest{Method: requestMethodReserve, Timeout: 2 * time.Second, FirstReserve: true})
		So(errr, ShouldBeNil)
		So(resp, ShouldNotBeNil)
		So(resp.Job, ShouldNotBeNil)

		key := resp.Job.Key()

		_, pid, ok := serverJobHostPid(server, key)
		So(ok, ShouldBeTrue)
		So(pid, ShouldEqual, 0)

		// the TTR marks it Lost; pid 0 is never confirmed dead, so it stays parked.
		So(waitForJobLost(server, key, 6*ttr), ShouldBeTrue)

		inRun, lost, failReason, okj := serverJobState(server, key)
		So(okj, ShouldBeTrue)
		So(inRun, ShouldBeTrue)
		So(lost, ShouldBeTrue)
		So(failReason, ShouldEqual, FailReasonLost)

		// a second client cannot re-reserve the parked pid-0 job.
		reReserved, errs := countReReserves(addr, config.ManagerCAFile, config.ManagerCertDomain,
			token, clientConnectTime, 20)
		So(errs, ShouldEqual, 0)
		So(reReserved, ShouldEqual, 0)

		// and it is still parked Lost in Run afterwards.
		inRun2, lost2, _, okj2 := serverJobState(server, key)
		So(okj2, ShouldBeTrue)
		So(inRun2, ShouldBeTrue)
		So(lost2, ShouldBeTrue)
	})
}

// serverJobHostPid reads the server-side queue item's job Host and Pid under
// lock. ok is false if the item is not in the queue.
func serverJobHostPid(server *Server, key string) (host string, pid int, ok bool) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return "", 0, false
	}

	j, isJob := item.Data().(*Job)
	if !isJob {
		return "", 0, false
	}

	j.RLock()
	host = j.Host
	pid = j.Pid
	j.RUnlock()

	return host, pid, true
}

// waitForJobLost polls the server-side job until it is marked Lost or the
// deadline passes, returning whether it went Lost.
func waitForJobLost(server *Server, key string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if _, lost, _, ok := serverJobState(server, key); ok && lost {
			return true
		}

		time.Sleep(20 * time.Millisecond)
	}

	return false
}

// countReReserves connects a fresh client and calls Reserve(200ms) the given
// number of times, returning how many calls returned a (re-reserved) job and how
// many returned an error.
func countReReserves(addr, caFile, certDomain string, token []byte, connectTime time.Duration,
	attempts int) (reReserved, errs int) {
	jq, err := Connect(addr, caFile, certDomain, token, connectTime)
	if err != nil {
		return 0, attempts
	}

	defer disconnect(jq)

	for range attempts {
		j, errr := jq.Reserve(200 * time.Millisecond)
		if errr != nil {
			errs++

			continue
		}

		if j != nil {
			reReserved++
		}
	}

	return reReserved, errs
}

// TestReliable2ReserveConfirmedDeadReclaimed covers spec.md section C2
// acceptance test 2: a reserved-not-started job whose recorded pid is a
// definitely-dead pid on a reachable host is requeued and becomes reservable
// again once death is confirmed (no stuck-in-Run hole).
func TestReliable2ReserveConfirmedDeadReclaimed(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr = 500 * time.Millisecond
		rg  = "reliable2_reserve_confirmed_dead_rg"
	)

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = ttr

	Convey("A reserved-not-started job with a dead owner is reclaimed and becomes reservable again", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd: restFormTrue + " confirmeddead", Cwd: testCwdPath, RepGroup: rg,
			ReqGroup: rg, Requirements: standardReqs, Retries: 3,
		}
		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, errr := jq.Reserve(2 * time.Second)
		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		key := reserved.Key()

		// the recorded host is this (reachable) host; overwrite the recorded pid
		// with a definitely-dead one so the confirm-dead check confirms death.
		So(setServerJobPid(server, key, definitelyDeadPid(t)), ShouldBeTrue)

		// deliberately never call Started; the TTR marks it Lost, death is
		// confirmed, and it is killed and requeued. Poll a fresh client until it
		// can reserve the requeued job again (no stuck-in-Run hole).
		var reReserved *Job

		deadline := time.Now().Add(30 * ttr)
		for time.Now().Before(deadline) {
			jq2, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			j, errr2 := jq2.Reserve(200 * time.Millisecond)
			So(errr2, ShouldBeNil)
			disconnect(jq2)

			if j != nil {
				reReserved = j

				break
			}

			time.Sleep(50 * time.Millisecond)
		}

		So(reReserved, ShouldNotBeNil)
		So(reReserved.Key(), ShouldEqual, key)
	})
}

// setServerJobPid overwrites the server-side job's recorded pid under lock, so a
// reserved-not-started job can be given a definitely-dead pid before its TTR
// fires (white-box: the test is in package jobqueue).
func setServerJobPid(server *Server, key string, pid int) bool {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return false
	}

	j, isJob := item.Data().(*Job)
	if !isJob {
		return false
	}

	j.Lock()
	j.Pid = pid
	j.Unlock()

	return true
}

// definitelyDeadPid runs a trivial child process to completion (reaping it) and
// returns its pid, which is therefore not running on this host - the "definitely
// dead pid" used by the C2 confirmed-dead reclaim test.
func definitelyDeadPid(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to run helper process to obtain a dead pid: %v", err)
	}

	return cmd.Process.Pid
}
