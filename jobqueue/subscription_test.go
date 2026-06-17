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

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	gpnet "github.com/shirou/gopsutil/net"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"nanomsg.org/go-mangos"
)

type addAndWaitResult struct {
	jobs []*Job
	err  error
}

func receiveAddAndWaitResult(resultCh <-chan addAndWaitResult, timeout time.Duration) addAndWaitResult {
	select {
	case result := <-resultCh:
		return result
	case <-time.After(timeout):
		return addAndWaitResult{err: fmt.Errorf("timed out waiting for AddAndWait")}
	}
}

func TestClientAddAndWait(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("AddAndWait returns all just-added complete and buried jobs by key", t, func() {
		ctx := context.Background()
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

		input := subscriptionTestJobs("subscription-e1-mixed", standardReqs, 3)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		archiveNextAddAndWaitJob(runner)
		buryNextAddAndWaitJob(runner, 12, "subscription e1 buried", "subscription e1 stderr")
		archiveNextAddAndWaitJob(runner)

		result := receiveAddAndWaitResult(resultCh, 6*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 3)
		So(addAndWaitStatesByKey(result.jobs, input), ShouldResemble, []JobState{
			JobStateComplete,
			JobStateBuried,
			JobStateComplete,
		})
		So(addAndWaitExitCodesByKey(result.jobs, input), ShouldResemble, []int{0, 12, 0})
	})

	Convey("AddAndWait returns a successful complete job with exit code 0", t, func() {
		ctx := context.Background()
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

		input := subscriptionTestJobs("subscription-e1-complete", standardReqs, 1)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		archiveNextAddAndWaitJob(runner)

		result := receiveAddAndWaitResult(resultCh, 6*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
		So(result.jobs[0].Exitcode, ShouldEqual, 0)
	})

	Convey("AddAndWait returns a buried job with non-zero exit code and inline stderr without a Go error", t, func() {
		ctx := context.Background()
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

		input := subscriptionTestJobs("subscription-e1-buried", standardReqs, 1)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		buryNextAddAndWaitJob(runner, 29, "subscription e1 failed", "subscription e1 failed stderr")

		result := receiveAddAndWaitResult(resultCh, 6*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].State, ShouldEqual, JobStateBuried)
		So(result.jobs[0].Exitcode, ShouldEqual, 29)

		stderr, err := result.jobs[0].StdErr()
		So(err, ShouldBeNil)
		So(stderr, ShouldContainSubstring, "subscription e1 failed stderr")
	})

	Convey("AddAndWait catch-up counts a job that completes before its internal subscription", t, func() {
		ctx := context.Background()
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

		workerDone := archiveNextAddAndWaitJobAsync(runner)
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		input := subscriptionTestJobs("subscription-e1-catch-up", standardReqs, 1)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		So(receiveAsyncError(workerDone, 2*time.Second), ShouldBeNil)

		result := receiveAddAndWaitResult(resultCh, 6*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
		So(result.jobs[0].Key(), ShouldEqual, input[0].Key())
	})

	Convey("AddAndWait rerun waits for the live job instead of archived catch-up", t, func() {
		ctx := context.Background()
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

		prefix := "subscription-e1-rerun-live"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(prefix, standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		archiveNextAddAndWaitJob(runner)

		waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		input := subscriptionTestJobs(prefix, standardReqs, 1)
		resultCh := addAndWaitAsyncWithIgnoreComplete(waitCtx, jq, input, false)

		job := startNextAddAndWaitJob(runner)

		time.Sleep(200 * time.Millisecond)

		select {
		case result := <-resultCh:
			So(result.err, ShouldNotBeNil)
			So("AddAndWait returned before the live rerun terminal event", ShouldBeBlank)
		default:
		}

		So(runner.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		result := receiveAddAndWaitResult(resultCh, 3*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
		So(result.jobs[0].Key(), ShouldEqual, ids[0])
	})

	Convey("AddAndWait deadline returns gathered jobs and names unfinished lost keys", t, func() {
		restore := overrideSubscriptionTimings(50 * time.Millisecond)
		defer restore()

		ctx := context.Background()
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

		waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()

		input := subscriptionTestJobs("subscription-e1-deadline", standardReqs, 2)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		archiveNextAddAndWaitJob(runner)
		_ = startNextAddAndWaitJob(runner)

		result := receiveAddAndWaitResult(resultCh, time.Second)
		So(errors.Is(result.err, context.DeadlineExceeded), ShouldBeTrue)
		So(result.err.Error(), ShouldContainSubstring, input[1].Key())
		So(result.err.Error(), ShouldNotContainSubstring, input[0].Key())
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].Key(), ShouldEqual, input[0].Key())
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
	})

	Convey("AddAndWait ignores a lost update and waits for the later terminal event", t, func() {
		restore := overrideSubscriptionTimings(50 * time.Millisecond)
		defer restore()

		ctx := context.Background()
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

		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		input := subscriptionTestJobs("subscription-e1-lost-then-complete", standardReqs, 1)
		resultCh := addAndWaitAsync(waitCtx, jq, input)

		job := startNextAddAndWaitJob(runner)
		time.Sleep(200 * time.Millisecond)

		select {
		case result := <-resultCh:
			So(result.err, ShouldNotBeNil)
			So("AddAndWait returned before the terminal event", ShouldBeBlank)
		default:
		}

		So(runner.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		result := receiveAddAndWaitResult(resultCh, 3*time.Second)
		So(result.err, ShouldBeNil)
		So(result.jobs, ShouldHaveLength, 1)
		So(result.jobs[0].State, ShouldEqual, JobStateComplete)
		So(result.jobs[0].Key(), ShouldEqual, input[0].Key())
	})
}

func archiveNextAddAndWaitJob(jq *Client) {
	job := startNextAddAndWaitJob(jq)
	So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
}

func startNextAddAndWaitJob(jq *Client) *Job {
	job, err := reserveAndStartAddAndWaitJob(jq)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)

	if job == nil {
		return &Job{}
	}

	return job
}

func reserveAndStartAddAndWaitJob(jq *Client) (*Job, error) {
	job, err := jq.Reserve(2 * time.Second)
	if err != nil {
		return nil, err
	}

	if job == nil {
		return nil, fmt.Errorf("reserve returned no job")
	}

	if err = jq.Started(job, os.Getpid()); err != nil {
		return nil, err
	}

	return job, nil
}

func buryNextAddAndWaitJob(jq *Client, exitCode int, failReason string, stderr string) {
	job := startNextAddAndWaitJob(jq)
	endState := &JobEndState{
		Exited:   true,
		Exitcode: exitCode,
		EndTime:  time.Now(),
		Stderr:   compressStd([]byte(stderr)),
	}

	So(jq.Bury(job, endState, failReason), ShouldBeNil)
}

func addAndWaitStatesByKey(got []*Job, input []*Job) []JobState {
	byKey := addAndWaitJobsByKey(got)
	states := make([]JobState, 0, len(input))

	for _, job := range input {
		if gotJob := byKey[job.Key()]; gotJob != nil {
			states = append(states, gotJob.State)
		} else {
			states = append(states, JobState("missing"))
		}
	}

	return states
}

func addAndWaitJobsByKey(jobs []*Job) map[string]*Job {
	byKey := make(map[string]*Job, len(jobs))

	for _, job := range jobs {
		byKey[job.Key()] = job
	}

	return byKey
}

func addAndWaitExitCodesByKey(got []*Job, input []*Job) []int {
	byKey := addAndWaitJobsByKey(got)
	exitCodes := make([]int, 0, len(input))

	for _, job := range input {
		if gotJob := byKey[job.Key()]; gotJob != nil {
			exitCodes = append(exitCodes, gotJob.Exitcode)
		} else {
			exitCodes = append(exitCodes, -999999)
		}
	}

	return exitCodes
}

func archiveNextAddAndWaitJobAsync(jq *Client) <-chan error {
	done := make(chan error, 1)

	go func() {
		job, err := reserveAndStartAddAndWaitJob(jq)
		if err != nil {
			done <- err

			return
		}

		done <- jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
	}()

	return done
}

func receiveAsyncError(done <-chan error, timeout time.Duration) error {
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("timed out waiting for async job driver")
	}
}

func addAndWaitAsync(ctx context.Context, jq *Client, jobs []*Job) <-chan addAndWaitResult {
	return addAndWaitAsyncWithIgnoreComplete(ctx, jq, jobs, true)
}

func addAndWaitAsyncWithIgnoreComplete(
	ctx context.Context,
	jq *Client,
	jobs []*Job,
	ignoreComplete bool,
) <-chan addAndWaitResult {
	resultCh := make(chan addAndWaitResult, 1)

	go func() {
		got, err := jq.AddAndWait(ctx, jobs, envVars, ignoreComplete)
		resultCh <- addAndWaitResult{jobs: got, err: err}
	}()

	return resultCh
}

func TestSubscriptionLongPollOverExistingPort(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("SubscribeToJobKeys opens one dedicated long-poll socket to the existing mangos port", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd:          "echo subscription a1 dial",
			Cwd:          "/tmp",
			ReqGroup:     "subscription-a1",
			Requirements: standardReqs,
			RepGroup:     "subscription-a1",
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)
		So(sub, ShouldNotBeNil)
		So(sub.Err(), ShouldBeNil)
		So(sub.dialAddr, ShouldEqual, jq.ServerInfo.Addr)
		sub.Unsubscribe()
	})

	Convey("A subscribed complete transition is delivered promptly through the parked waitForUpdates reply", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd:          "echo subscription a1 complete",
			Cwd:          "/tmp",
			ReqGroup:     "subscription-a1",
			Requirements: standardReqs,
			RepGroup:     "subscription-a1",
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		err = jq.Started(job, os.Getpid())
		So(err, ShouldBeNil)

		transitioned := time.Now()
		err = jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: transitioned})
		So(err, ShouldBeNil)

		select {
		case update := <-sub.Updates():
			So(time.Since(transitioned), ShouldBeLessThan, time.Second)
			So(update.Kind, ShouldEqual, JobUpdateTerminal)
			So(update.State, ShouldEqual, JobStateComplete)
			So(update.Key, ShouldEqual, ids[0])
		case <-time.After(time.Second):
			So("timed out waiting for subscription update", ShouldBeBlank)
		}
	})

	Convey("Active subscriptions do not add any new server listening ports", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		before, err := listeningTCPPortsForCurrentProcess()
		So(err, ShouldBeNil)
		So(before, ShouldContain, portNumber(server.ServerInfo.Port))
		So(before, ShouldContain, portNumber(server.ServerInfo.WebPort))

		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd:          "echo subscription a1 ports",
			Cwd:          "/tmp",
			ReqGroup:     "subscription-a1",
			Requirements: standardReqs,
			RepGroup:     "subscription-a1",
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		after, err := listeningTCPPortsForCurrentProcess()
		So(err, ShouldBeNil)
		So(after, ShouldResemble, before)
		So(server.ServerInfo.Port, ShouldNotBeBlank)
		So(server.ServerInfo.WebPort, ShouldNotBeBlank)
	})

	Convey("Existing Add and Get client behaviour is unchanged when no subscription is used", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd:          "echo subscription a1 unchanged",
			Cwd:          "/tmp",
			ReqGroup:     "subscription-a1",
			Requirements: standardReqs,
			RepGroup:     "subscription-a1",
		}
		added, existed, err := jq.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		got, err := jq.GetByEssence(job.ToEssense(), false, false)
		So(err, ShouldBeNil)
		So(got, ShouldNotBeNil)
		So(got.Cmd, ShouldEqual, job.Cmd)
		So(got.State, ShouldEqual, JobStateReady)
	})

}

func TestSubscriptionBoundedIsolatedBuffer(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A full subscriber parks one delivery worker instead of one goroutine per blocked event", t, func() {
		total := serverSubscriptionQueueSize + 16
		keys := subscriptionTestKeys("subscription-d2-worker", total)
		sub := newServerSubscription(keys, "", nil)
		defer sub.close()

		deliveries := make([]repGroupSubscriptionUpdate, 0, total)
		for _, key := range keys {
			deliveries = append(deliveries, repGroupSubscriptionUpdate{
				sub: sub,
				update: &JobUpdate{
					Kind:  JobUpdateTerminal,
					Key:   key,
					State: JobStateComplete,
				},
			})
		}

		before := subscriptionEnqueueGoroutines()
		(&Server{}).enqueueSubscriptionDeliveries(deliveries)

		So(serverSubscriptionQueueDepthBecomes(sub, serverSubscriptionQueueSize, time.Second), ShouldBeTrue)
		So(maxSubscriptionEnqueueGoroutines(100*time.Millisecond), ShouldBeLessThanOrEqualTo, before+1)
	})

	Convey("A full subscriber does not stall a peer receiving many terminal updates", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		total := serverSubscriptionQueueSize + 8
		itemdefs, ids := buriedSubscriptionItemDefs("subscription-d2-shared", standardReqs, total)

		stalledID, err := server.registerClientSubscription(ids, "")
		So(err, ShouldBeNil)

		peerID, err := server.registerClientSubscription(ids, "")
		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(peerID)

		added, dups, err := server.enqueueItems(ctx, itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, total)
		So(dups, ShouldEqual, 0)

		updates, ok := collectServerSubscriptionUpdatesByID(server, peerID, total, time.Second)
		peerCount := len(updates)
		server.unregisterClientSubscription(stalledID)

		So(ok, ShouldBeTrue)
		So(peerCount, ShouldEqual, total)
		So(distinctSubscriptionUpdateKeys(updates), ShouldEqual, total)
	})

	Convey("A subscriber receives every terminal event after its full queue resumes", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		total := serverSubscriptionQueueSize + 8
		itemdefs, ids := buriedSubscriptionItemDefs("subscription-d2-resume", standardReqs, total)

		subID, err := server.registerClientSubscription(ids, "")
		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(subID)

		sub, exists := server.clientSubscription(subID)
		So(exists, ShouldBeTrue)

		added, dups, err := server.enqueueItems(ctx, itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, total)
		So(dups, ShouldEqual, 0)
		So(serverSubscriptionQueueDepthBecomes(sub, serverSubscriptionQueueSize, time.Second), ShouldBeTrue)

		updates, ok := collectServerSubscriptionUpdatesByID(server, subID, total, time.Second)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, total)
		So(distinctSubscriptionUpdateKeys(updates), ShouldEqual, total)
	})

	Convey("A stalled subscriber does not stop unrelated terminal transitions becoming observable", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		total := serverSubscriptionQueueSize + 1
		itemdefs, stalledIDs := buriedSubscriptionItemDefs("subscription-d2-stalled", standardReqs, total)

		stalledID, err := server.registerClientSubscription(stalledIDs, "")
		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(stalledID)

		stalled, exists := server.clientSubscription(stalledID)
		So(exists, ShouldBeTrue)

		added, dups, err := server.enqueueItems(ctx, itemdefs)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, total)
		So(dups, ShouldEqual, 0)
		So(serverSubscriptionQueueDepthBecomes(stalled, serverSubscriptionQueueSize, time.Second), ShouldBeTrue)

		independentDone := archiveIndependentSubscriptionJob(jq, standardReqs, "subscription-d2-independent")
		So(completeJobsByRepGroupBecome(jq, "subscription-d2-independent", 1, time.Second), ShouldBeTrue)

		server.unregisterClientSubscription(stalledID)
		So(completionFinished(independentDone, time.Second), ShouldBeTrue)
	})
}

func subscriptionTestKeys(prefix string, count int) []string {
	keys := make([]string, 0, count)

	for i := range count {
		keys = append(keys, fmt.Sprintf("%s-%d", prefix, i))
	}

	return keys
}

func TestSubscriptionAtLeastOnceDedup(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("SubscribeToJobKeys delivers a terminal update when completion races the subscribe call", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-d3-race", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		type subscribeResult struct {
			sub *Subscription
			err error
		}

		subscribed := make(chan subscribeResult, 1)
		go func() {
			sub, subErr := jq.SubscribeToJobKeys(ctx, ids)
			subscribed <- subscribeResult{sub: sub, err: subErr}
		}()

		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		var result subscribeResult
		select {
		case result = <-subscribed:
		case <-time.After(time.Second):
			So("timed out waiting for SubscribeToJobKeys", ShouldBeBlank)

			return
		}

		So(result.err, ShouldBeNil)
		So(result.sub, ShouldNotBeNil)
		if result.err != nil || result.sub == nil {
			return
		}

		defer result.sub.Unsubscribe()

		updates, ok := collectSubscriptionUpdates(result.sub, 1, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, 1)

		if second := receiveSubscriptionUpdate(result.sub, 150*time.Millisecond); second != nil {
			updates = append(updates, second)
		}

		for _, update := range updates {
			So(update.Kind, ShouldEqual, JobUpdateTerminal)
			So(update.Key, ShouldEqual, ids[0])
			So(update.State, ShouldEqual, JobStateComplete)
		}

		if len(updates) == 2 {
			So(updates[1].Key, ShouldEqual, updates[0].Key)
			So(updates[1].State, ShouldEqual, updates[0].State)
		}
	})

	Convey("A key completed during subscribe catch-up can be seen as an identical catch-up/live duplicate", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-d3-duplicate", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		subID, err := server.registerClientSubscription(ids, "")
		So(err, ShouldBeNil)

		defer server.unregisterClientSubscription(subID)

		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		catchUp, err := server.subscriptionCatchUpForRegistered(ctx, subID, ids, "")
		So(err, ShouldBeNil)
		So(catchUp, ShouldHaveLength, 1)

		live, ok := collectServerSubscriptionUpdatesByID(server, subID, 1, time.Second)
		So(ok, ShouldBeTrue)
		So(live, ShouldHaveLength, 1)

		for _, update := range []*JobUpdate{catchUp[0], live[0]} {
			So(update.Kind, ShouldEqual, JobUpdateTerminal)
			So(update.Key, ShouldEqual, ids[0])
			So(update.State, ShouldEqual, JobStateComplete)
		}

		So(live[0].Key, ShouldEqual, catchUp[0].Key)
		So(live[0].State, ShouldEqual, catchUp[0].State)
	})

	Convey("The AddAndWait terminal collector counts distinct keys when a duplicate terminal event is injected", t, func() {
		updates := make(chan *JobUpdate, 4)
		keys := []string{"subscription-d3-a", "subscription-d3-b", "subscription-d3-c"}

		updates <- &JobUpdate{Kind: JobUpdateTerminal, Key: keys[0], State: JobStateComplete}
		updates <- &JobUpdate{Kind: JobUpdateTerminal, Key: keys[1], State: JobStateBuried}
		updates <- &JobUpdate{Kind: JobUpdateTerminal, Key: keys[1], State: JobStateBuried}
		updates <- &JobUpdate{Kind: JobUpdateTerminal, Key: keys[2], State: JobStateComplete}

		seen, err := collectDistinctTerminalKeys(context.Background(), updates, keys)
		So(err, ShouldBeNil)
		So(seen, ShouldResemble, map[string]JobState{
			keys[0]: JobStateComplete,
			keys[1]: JobStateBuried,
			keys[2]: JobStateComplete,
		})
	})
}

func TestSubscriptionReconnectResync(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A restarted manager delivers a resync marker and catch-up terminal update", t, func() {
		restore := overrideSubscriptionReconnectTimings(250*time.Millisecond, 2*time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-d4-catch-up", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		server.Stop(ctx, true)
		sub.closeSock()
		time.Sleep(100 * time.Millisecond)

		server = restartSubscriptionTestServer(ctx, serverConfig)

		catchUpClient, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)
		catchUpClient.clientid = jq.clientid

		defer disconnect(catchUpClient)

		So(catchUpClient.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		updates, ok := collectSubscriptionUpdates(sub, 2, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, 2)
		So(updates[0].Kind, ShouldEqual, JobUpdateResync)
		So(updates[1].Kind, ShouldEqual, JobUpdateTerminal)
		So(updates[1].Key, ShouldEqual, ids[0])
		So(updates[1].State, ShouldEqual, JobStateComplete)
		So(sub.Err(), ShouldBeNil)
		So(subscriptionUpdatesStillOpen(sub, 150*time.Millisecond), ShouldBeTrue)
	})

	Convey("A permanently stopped manager closes only after reconnect retries are exhausted", t, func() {
		restore := overrideSubscriptionReconnectTimings(50*time.Millisecond, 200*time.Millisecond)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-d4-permanent"})
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		server.Stop(ctx, true)
		sub.closeSock()

		So(subscriptionUpdatesStillOpen(sub, 75*time.Millisecond), ShouldBeTrue)
		So(sub.Err(), ShouldBeNil)
		So(subscriptionErrBecomes(sub, ErrSubscriptionClosed, 3*time.Second), ShouldBeTrue)
		So(errors.Is(sub.Err(), ErrSubscriptionClosed), ShouldBeTrue)
		So(subscriptionUpdatesClosed(sub), ShouldBeTrue)
		So(ErrSubscriptionClosed.Error(), ShouldEqual, "jobqueue subscription closed: unrecoverable disconnect")
	})

	Convey("A successful transient reconnect never sets a fatal subscription error", t, func() {
		restore := overrideSubscriptionReconnectTimings(200*time.Millisecond, 2*time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-d4-transient"})
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		server.Stop(ctx, true)
		sub.closeSock()
		time.Sleep(75 * time.Millisecond)
		So(sub.Err(), ShouldBeNil)

		server = restartSubscriptionTestServer(ctx, serverConfig)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateResync)
		So(sub.Err(), ShouldBeNil)
		So(subscriptionUpdatesStillOpen(sub, 150*time.Millisecond), ShouldBeTrue)
	})
}

func overrideSubscriptionReconnectTimings(
	retryWait time.Duration,
	retryTime time.Duration,
) func() {
	originalRetryWait := ClientRetryWait
	originalRetryTime := ClientRetryTime

	ClientRetryWait = retryWait
	ClientRetryTime = retryTime

	return func() {
		ClientRetryWait = originalRetryWait
		ClientRetryTime = originalRetryTime
	}
}

func TestSubscriptionAuthorization(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A client with a mismatched token cannot subscribe to job keys", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, mismatchedToken(token), clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-a2-client"})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, ErrPermissionDenied)
		So(sub, ShouldBeNil)
		So(serverClientSubscriptionCount(server), ShouldEqual, 0)
	})

	Convey("A dedicated long-poll socket with a mismatched token receives no subscription updates", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs([]*Job{{
			Cmd:          "echo subscription a2 unauthorised",
			Cwd:          "/tmp",
			ReqGroup:     "subscription-a2",
			Requirements: standardReqs,
			RepGroup:     "subscription-a2",
		}}, envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sock, err := dialSubscriptionSocket(jq.ServerInfo.Addr, serverConfig.CAFile, serverConfig.CertDomain, 2*time.Second)
		So(err, ShouldBeNil)

		defer func() {
			So(sock.Close(), ShouldBeNil)
		}()

		wrongToken := mismatchedToken(token)
		subscribeResp, err := sendRawSubscriptionRequest(sock, &clientRequest{
			Method: "subscribe",
			Keys:   ids,
			Token:  wrongToken,
		})
		So(err, ShouldBeNil)
		So(subscribeResp.Err, ShouldEqual, ErrPermissionDenied)
		So(subscribeResp.SubscriptionID, ShouldBeBlank)
		So(subscribeResp.JobUpdates, ShouldHaveLength, 0)
		So(serverClientSubscriptionCount(server), ShouldEqual, 0)

		waitResp, err := sendRawSubscriptionRequest(sock, &clientRequest{
			Method:         "waitForUpdates",
			SubscriptionID: "sub-unauthorised",
			Token:          wrongToken,
			Timeout:        2 * time.Second,
		})
		So(err, ShouldBeNil)
		So(waitResp.Err, ShouldEqual, ErrPermissionDenied)
		So(waitResp.JobUpdates, ShouldHaveLength, 0)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		So(serverClientSubscriptionCount(server), ShouldEqual, 0)
	})
}

func TestSubscriptionCatchUp(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("An archived subscribed key is returned in the synchronous subscribe reply and emitted", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-c1-archived-key", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		sock, err := dialSubscriptionSocket(jq.ServerInfo.Addr, serverConfig.CAFile, serverConfig.CertDomain, 2*time.Second)
		So(err, ShouldBeNil)

		defer func() {
			So(sock.Close(), ShouldBeNil)
		}()

		subscribeResp, err := sendRawSubscriptionRequest(sock, &clientRequest{
			Method: "subscribe",
			Keys:   ids,
			Token:  token,
		})
		So(err, ShouldBeNil)
		So(subscribeResp.Err, ShouldBeBlank)
		So(subscribeResp.SubscriptionID, ShouldNotBeBlank)
		So(subscribeResp.JobUpdates, ShouldHaveLength, 1)
		So(subscribeResp.JobUpdates[0].Kind, ShouldEqual, JobUpdateTerminal)
		So(subscribeResp.JobUpdates[0].State, ShouldEqual, JobStateComplete)
		So(subscribeResp.JobUpdates[0].Key, ShouldEqual, ids[0])

		unsubscribeResp, err := sendRawSubscriptionRequest(sock, &clientRequest{
			Method:         "unsubscribe",
			SubscriptionID: subscribeResp.SubscriptionID,
			Token:          token,
		})
		So(err, ShouldBeNil)
		So(unsubscribeResp.Err, ShouldBeBlank)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		update := receiveSubscriptionUpdate(sub, time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateTerminal)
		So(update.State, ShouldEqual, JobStateComplete)
		So(update.Key, ShouldEqual, ids[0])
	})

	Convey("An archived RepGroup catch-up returns one aggregate update with counts", t, func() {
		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "subscription-c1-archived-rg"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 2), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 2)

		for range ids {
			job, errr := jq.Reserve(50 * time.Millisecond)
			So(errr, ShouldBeNil)
			So(jq.Started(job, os.Getpid()), ShouldBeNil)
			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
		}

		sub, err := jq.SubscribeToRepGroup(ctx, repGroup)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		update := receiveSubscriptionUpdate(sub, time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateRepGroupDone)
		So(update.RepGroup, ShouldEqual, repGroup)
		So(update.Complete, ShouldEqual, 2)
		So(update.Buried, ShouldEqual, 0)
		So(update.Lost, ShouldEqual, 0)
		So(update.Total, ShouldEqual, 2)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A running subscribed key emits no catch-up until it becomes terminal", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-c1-running", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateTerminal)
		So(update.Key, ShouldEqual, ids[0])
		So(update.State, ShouldEqual, JobStateComplete)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A live rerun subscribed key suppresses archived terminal catch-up", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		prefix := "subscription-c1-rerun-live"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(prefix, standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		archiveNextSubscriptionJob(jq)

		liveIDs, err := jq.AddAndReturnIDs(subscriptionTestJobs(prefix, standardReqs, 1), envVars, false)
		So(err, ShouldBeNil)
		So(liveIDs, ShouldResemble, ids)

		job := startNextSubscriptionJob(jq)
		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateTerminal)
		So(update.Key, ShouldEqual, ids[0])
		So(update.State, ShouldEqual, JobStateComplete)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A subscribed missing key emits no catch-up event", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-c1-missing-key"})
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A catch-up DB read failure returns ErrDBError without registering a subscription", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		So(server.db.close(ctx), ShouldBeNil)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-c1-db-error"})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, ErrDBError)
		So(sub, ShouldBeNil)
		So(serverClientSubscriptionCount(server), ShouldEqual, 0)
	})
}

func TestSubscriptionTeardown(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Unsubscribe closes Updates with nil Err and removes server registration", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-d1-unsubscribe"})
		So(err, ShouldBeNil)
		So(sub.Err(), ShouldBeNil)
		So(serverClientSubscriptionCount(server), ShouldEqual, 1)

		sub.Unsubscribe()

		So(subscriptionUpdatesClosed(sub), ShouldBeTrue)
		So(sub.Err(), ShouldBeNil)
		So(subscriptionSocketClosed(sub), ShouldBeTrue)
		So(serverClientSubscriptionCount(server), ShouldEqual, 0)
	})

	Convey("A subscription closes with DeadlineExceeded when its context deadline passes", t, func() {
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(context.Background(), serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(context.Background(), true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-d1-deadline"})
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		So(subscriptionUpdatesClosed(sub), ShouldBeTrue)
		So(errors.Is(sub.Err(), context.DeadlineExceeded), ShouldBeTrue)
		So(serverClientSubscriptionCountBecomes(server, 0, time.Second), ShouldBeTrue)
	})

	Convey("A subscription closes with Canceled when its context is canceled", t, func() {
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(context.Background(), serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(context.Background(), true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ctx, cancel := context.WithCancel(context.Background())
		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-d1-cancel"})
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		cancel()

		So(subscriptionUpdatesClosed(sub), ShouldBeTrue)
		So(errors.Is(sub.Err(), context.Canceled), ShouldBeTrue)
		So(serverClientSubscriptionCountBecomes(server, 0, time.Second), ShouldBeTrue)
	})

	Convey("Unsubscribe is idempotent and leaves Err nil", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToJobKeys(ctx, []string{"subscription-d1-twice"})
		So(err, ShouldBeNil)

		sub.Unsubscribe()

		So(func() {
			sub.Unsubscribe()
		}, ShouldNotPanic)
		So(subscriptionUpdatesClosed(sub), ShouldBeTrue)
		So(sub.Err(), ShouldBeNil)
		So(serverClientSubscriptionCount(server), ShouldEqual, 0)
	})
}

func TestSubscriptionPerKeyTerminalEvents(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Subscribed keys each receive exactly one terminal update for complete or buried jobs", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-b1-terminal", standardReqs, 3), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 3)

		expected := map[string]JobState{
			ids[0]: JobStateComplete,
			ids[1]: JobStateBuried,
			ids[2]: JobStateComplete,
		}

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		for range ids {
			job, errr := jq.Reserve(50 * time.Millisecond)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			expectedState, ok := expected[job.Key()]
			So(ok, ShouldBeTrue)

			switch expectedState {
			case JobStateComplete:
				errr = jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
			case JobStateBuried:
				errr = jq.Bury(job, &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}, "subscription test buried")
			default:
				So(expectedState == JobStateComplete || expectedState == JobStateBuried, ShouldBeTrue)
			}

			So(errr, ShouldBeNil)
		}

		updates, ok := collectSubscriptionUpdates(sub, 3, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, 3)

		seen := make(map[string]JobState)

		for _, update := range updates {
			So(update.Kind, ShouldEqual, JobUpdateTerminal)
			seen[update.Key] = update.State
		}

		So(seen, ShouldResemble, expected)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A running subscribed job emits one lost update and no terminal update while it stays lost", t, func() {
		restore := overrideSubscriptionTimings(200 * time.Millisecond)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-b1-lost", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateLost)
		So(update.Key, ShouldEqual, ids[0])
		So(update.State, ShouldEqual, JobStateLost)
		So(update.FailReason, ShouldEqual, FailReasonLost)
		So(receiveSubscriptionUpdate(sub, 500*time.Millisecond), ShouldBeNil)
	})

	Convey("Reserved and running states are not delivered before the final terminal update", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-b1-running", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)

		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)

		So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateTerminal)
		So(update.Key, ShouldEqual, ids[0])
		So(update.State, ShouldEqual, JobStateComplete)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A lost subscribed job later emits a terminal buried update when it is confirmed dead", t, func() {
		restore := overrideSubscriptionTimings(200 * time.Millisecond)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs("subscription-b1-lost-buried", standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		sub, err := jq.SubscribeToJobKeys(ctx, ids)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job.Key(), ShouldEqual, ids[0])
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateLost)
		So(update.Key, ShouldEqual, ids[0])
		So(update.State, ShouldEqual, JobStateLost)

		killed, err := server.killJob(ctx, ids[0])
		So(err, ShouldBeNil)
		So(killed, ShouldBeTrue)

		update = receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateTerminal)
		So(update.Key, ShouldEqual, ids[0])
		So(update.State, ShouldEqual, JobStateBuried)
	})
}

func TestSubscriptionRepGroupAggregate(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A RepGroup subscription emits one aggregate when all known jobs are complete or buried", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "subscription-b2-done"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 2), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 2)

		sub, err := jq.SubscribeToRepGroup(ctx, repGroup)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		archiveNextSubscriptionJob(jq)
		buryNextSubscriptionJob(jq)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateRepGroupDone)
		So(update.RepGroup, ShouldEqual, repGroup)
		So(update.Complete, ShouldEqual, 1)
		So(update.Buried, ShouldEqual, 1)
		So(update.Lost, ShouldEqual, 0)
		So(update.Total, ShouldEqual, 2)
		So(update.JobKeys, ShouldHaveLength, 2)
		So(update.JobStates, ShouldHaveLength, 2)
		So(subscriptionStatesByKey(update), ShouldResemble, map[string]JobState{
			ids[0]: JobStateComplete,
			ids[1]: JobStateBuried,
		})
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A lost RepGroup job holds the aggregate back until it settles", t, func() {
		restore := overrideSubscriptionTimings(200 * time.Millisecond)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "subscription-b2-lost"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 2), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 2)

		sub, err := jq.SubscribeToRepGroup(ctx, repGroup)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		lostJob := startNextSubscriptionJob(jq)
		archiveNextSubscriptionJob(jq)

		So(receiveSubscriptionUpdate(sub, 2*time.Second), ShouldBeNil)

		killed, err := server.killJob(ctx, lostJob.Key())
		So(err, ShouldBeNil)
		So(killed, ShouldBeTrue)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateRepGroupDone)
		So(update.Complete, ShouldEqual, 1)
		So(update.Buried, ShouldEqual, 1)
		So(update.Lost, ShouldEqual, 0)
		So(update.Total, ShouldEqual, 2)
		So(subscriptionStatesByKey(update), ShouldResemble, map[string]JobState{
			ids[0]: JobStateBuried,
			ids[1]: JobStateComplete,
		})
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("An empty RepGroup subscription closes on context deadline without a done event", t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(context.Background(), serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(context.Background(), true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		sub, err := jq.SubscribeToRepGroup(ctx, "subscription-b2-empty")
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		var updates []*JobUpdate
		for update := range sub.Updates() {
			updates = append(updates, update)
		}

		So(updates, ShouldHaveLength, 0)
		So(errors.Is(sub.Err(), context.DeadlineExceeded), ShouldBeTrue)
	})

	Convey("A RepGroup subscription delivers only the aggregate when its jobs finish", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "subscription-b2-only-aggregate"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 2), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 2)

		sub, err := jq.SubscribeToRepGroup(ctx, repGroup)
		So(err, ShouldBeNil)

		defer sub.Unsubscribe()

		archiveNextSubscriptionJob(jq)
		archiveNextSubscriptionJob(jq)

		update := receiveSubscriptionUpdate(sub, 2*time.Second)
		So(update, ShouldNotBeNil)
		So(update.Kind, ShouldEqual, JobUpdateRepGroupDone)
		So(receiveSubscriptionUpdate(sub, 150*time.Millisecond), ShouldBeNil)
	})

	Convey("A post-registration catch-up snapshot holds back a missed live RepGroup job", t, func() {
		restore := overrideSubscriptionTimings(5 * time.Second)
		defer restore()

		ctx := context.Background()
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "subscription-b2-catch-up-race"
		ids, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(ids, ShouldHaveLength, 1)

		archiveNextSubscriptionJob(jq)

		liveJobs := subscriptionTestJobs(repGroup+"-live", standardReqs, 1)
		liveJobs[0].RepGroup = repGroup
		liveIDs, err := jq.AddAndReturnIDs(liveJobs, envVars, true)
		So(err, ShouldBeNil)
		So(liveIDs, ShouldHaveLength, 1)

		id := "sub-catch-up-race"
		server.csmutex.Lock()
		server.clientSubscriptions[id] = newServerSubscription(nil, repGroup, nil)
		server.csmutex.Unlock()

		defer server.unregisterClientSubscription(id)

		catchUp, err := server.subscriptionCatchUpForRegistered(ctx, id, nil, repGroup)
		So(err, ShouldBeNil)
		So(catchUp, ShouldBeNil)

		archiveNextSubscriptionJob(jq)

		updates, ok := collectServerSubscriptionUpdatesByID(server, id, 1, 2*time.Second)
		So(ok, ShouldBeTrue)
		So(updates, ShouldHaveLength, 1)

		update := updates[0]
		So(update.Kind, ShouldEqual, JobUpdateRepGroupDone)
		So(update.RepGroup, ShouldEqual, repGroup)
		So(update.Complete, ShouldEqual, 2)
		So(update.Buried, ShouldEqual, 0)
		So(update.Lost, ShouldEqual, 0)
		So(update.Total, ShouldEqual, 2)
		So(subscriptionStatesByKey(update), ShouldResemble, map[string]JobState{
			ids[0]:     JobStateComplete,
			liveIDs[0]: JobStateComplete,
		})
	})
}

func maxSubscriptionEnqueueGoroutines(timeout time.Duration) int {
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	maxCount := 0

	for {
		runtime.Gosched()

		maxCount = max(maxCount, subscriptionEnqueueGoroutines())

		select {
		case <-deadline:
			return maxCount
		case <-ticker.C:
		}
	}
}

func subscriptionEnqueueGoroutines() int {
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return 0
	}

	return strings.Count(buf.String(), "jobqueue.(*serverSubscription).enqueue")
}

func buriedSubscriptionItemDefs(
	prefix string,
	standardReqs *jqs.Requirements,
	count int,
) ([]*queue.ItemDef, []string) {
	jobs := subscriptionTestJobs(prefix, standardReqs, count)
	itemdefs := make([]*queue.ItemDef, 0, count)
	ids := make([]string, 0, count)
	now := time.Now()

	for _, job := range jobs {
		job.State = JobStateBuried
		job.Exited = true
		job.Exitcode = -1
		job.FailReason = "subscription test buried"
		job.StartTime = now
		job.EndTime = now

		key := job.Key()
		ids = append(ids, key)
		itemdefs = append(itemdefs, &queue.ItemDef{
			Key:          key,
			ReserveGroup: job.getSchedulerGroup(),
			Data:         job,
			TTR:          ServerItemTTR,
			StartQueue:   queue.SubQueueBury,
		})
	}

	return itemdefs, ids
}

func distinctSubscriptionUpdateKeys(updates []*JobUpdate) int {
	keys := make(map[string]struct{}, len(updates))

	for _, update := range updates {
		keys[update.Key] = struct{}{}
	}

	return len(keys)
}

func serverSubscriptionQueueDepthBecomes(sub *serverSubscription, expected int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if len(sub.queue) == expected {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

func archiveIndependentSubscriptionJob(
	jq *Client,
	standardReqs *jqs.Requirements,
	repGroup string,
) <-chan error {
	done := make(chan error, 1)

	go func() {
		_, err := jq.AddAndReturnIDs(subscriptionTestJobs(repGroup, standardReqs, 1), envVars, true)
		if err != nil {
			done <- err

			return
		}

		job, err := jq.Reserve(50 * time.Millisecond)
		if err != nil {
			done <- err

			return
		}

		if err = jq.Started(job, os.Getpid()); err != nil {
			done <- err

			return
		}

		done <- jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
	}()

	return done
}

func restartSubscriptionTestServer(ctx context.Context, serverConfig ServerConfig) *Server {
	originalWipe := wipeDevDBOnInit
	wipeDevDBOnInit = false
	server, _, _, err := serve(ctx, serverConfig)
	wipeDevDBOnInit = originalWipe

	So(err, ShouldBeNil)

	return server
}

func subscriptionUpdatesStillOpen(sub *Subscription, timeout time.Duration) bool {
	select {
	case _, ok := <-sub.Updates():
		return ok
	case <-time.After(timeout):
		return true
	}
}

func subscriptionErrBecomes(sub *Subscription, target error, timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if errors.Is(sub.Err(), target) {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

func completeJobsByRepGroupBecome(jq *Client, repGroup string, expected int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		jobs, err := jq.GetByRepGroup(repGroup, false, 0, JobStateComplete, false, false)
		if err == nil && len(jobs) == expected {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

func completionFinished(done <-chan error, timeout time.Duration) bool {
	select {
	case err := <-done:
		So(err, ShouldBeNil)

		return err == nil
	case <-time.After(timeout):
		return false
	}
}

func collectServerSubscriptionUpdatesByID(
	server *Server,
	id string,
	count int,
	perUpdateTimeout time.Duration,
) ([]*JobUpdate, bool) {
	updates := make([]*JobUpdate, 0, count)

	for len(updates) < count {
		batch, err := server.waitForSubscriptionUpdates(id, perUpdateTimeout)
		if err != nil {
			return updates, false
		}

		updates = append(updates, batch...)

		if len(batch) == 0 {
			return updates, false
		}
	}

	return updates, true
}

func overrideSubscriptionTimings(ttr time.Duration) func() {
	originalTTR := ServerItemTTR
	originalLostCheckTimeout := ServerLostJobCheckTimeout
	originalLostCheckRetry := ServerLostJobCheckRetryTime

	ServerItemTTR = ttr
	ServerLostJobCheckTimeout = 100 * time.Millisecond
	ServerLostJobCheckRetryTime = time.Hour

	return func() {
		ServerItemTTR = originalTTR
		ServerLostJobCheckTimeout = originalLostCheckTimeout
		ServerLostJobCheckRetryTime = originalLostCheckRetry
	}
}

func subscriptionTestConfig(t *testing.T) (ServerConfig, string, *jqs.Requirements, time.Duration) {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
	dir := t.TempDir()
	serverConfig.DBFile = filepath.Join(dir, "db")
	serverConfig.DBFileBackup = filepath.Join(dir, "db.bk")
	serverConfig.TokenFile = filepath.Join(dir, "token")
	serverConfig.CAFile = filepath.Join(dir, "ca.pem")
	serverConfig.CertFile = filepath.Join(dir, "cert.pem")
	serverConfig.KeyFile = filepath.Join(dir, "key.pem")

	return serverConfig, addr, standardReqs, clientConnectTime
}

func subscriptionTestJobs(prefix string, standardReqs *jqs.Requirements, count int) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		name := fmt.Sprintf("%s-%d", prefix, i)
		jobs = append(jobs, &Job{
			Cmd:          "echo " + name,
			Cwd:          "/tmp",
			ReqGroup:     name,
			Requirements: standardReqs,
			RepGroup:     prefix,
		})
	}

	return jobs
}

func collectSubscriptionUpdates(sub *Subscription, count int, timeout time.Duration) ([]*JobUpdate, bool) {
	deadline := time.After(timeout)
	updates := make([]*JobUpdate, 0, count)

	for len(updates) < count {
		select {
		case update, ok := <-sub.Updates():
			if !ok {
				return updates, false
			}

			updates = append(updates, update)
		case <-deadline:
			return updates, false
		}
	}

	return updates, true
}

func archiveNextSubscriptionJob(jq *Client) {
	job := startNextSubscriptionJob(jq)
	So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
}

func buryNextSubscriptionJob(jq *Client) {
	job := startNextSubscriptionJob(jq)
	So(
		jq.Bury(job, &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}, "subscription test buried"),
		ShouldBeNil,
	)
}

func receiveSubscriptionUpdate(sub *Subscription, timeout time.Duration) *JobUpdate {
	select {
	case update, ok := <-sub.Updates():
		if !ok {
			return nil
		}

		return update
	case <-time.After(timeout):
		return nil
	}
}

func mismatchedToken(token []byte) []byte {
	wrong := append([]byte(nil), token...)
	wrong[0] ^= 1

	return wrong
}

func subscriptionStatesByKey(update *JobUpdate) map[string]JobState {
	states := make(map[string]JobState, len(update.JobKeys))

	for i, key := range update.JobKeys {
		states[key] = update.JobStates[i]
	}

	return states
}

func startNextSubscriptionJob(jq *Client) *Job {
	job, err := jq.Reserve(50 * time.Millisecond)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(jq.Started(job, os.Getpid()), ShouldBeNil)

	return job
}

func subscriptionUpdatesClosed(sub *Subscription) bool {
	select {
	case _, ok := <-sub.Updates():
		return !ok
	case <-time.After(time.Second):
		return false
	}
}

func subscriptionSocketClosed(sub *Subscription) bool {
	if err := sub.sock.SetOption(mangos.OptionRecvDeadline, 10*time.Millisecond); err != nil {
		return errors.Is(err, mangos.ErrClosed)
	}

	_, err := sub.sock.Recv()

	return errors.Is(err, mangos.ErrClosed)
}

func serverClientSubscriptionCountBecomes(server *Server, expected int, timeout time.Duration) bool {
	deadline := time.After(timeout)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if serverClientSubscriptionCount(server) == expected {
			return true
		}

		select {
		case <-deadline:
			return false
		case <-ticker.C:
		}
	}
}

func serverClientSubscriptionCount(server *Server) int {
	server.csmutex.RLock()
	defer server.csmutex.RUnlock()

	return len(server.clientSubscriptions)
}

func sendRawSubscriptionRequest(sock mangos.Socket, cr *clientRequest) (*serverResponse, error) {
	ch := new(codec.BincHandle)

	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, ch)

	if err := enc.Encode(cr); err != nil {
		return nil, err
	}

	if err := sock.Send(encoded); err != nil {
		return nil, err
	}

	resp, err := sock.Recv()
	if err != nil {
		return nil, err
	}

	sr := &serverResponse{}
	dec := codec.NewDecoderBytes(resp, ch)

	err = dec.Decode(sr)
	if err != nil {
		return nil, err
	}

	return sr, nil
}

func listeningTCPPortsForCurrentProcess() ([]uint32, error) {
	pid, err := strconv.ParseInt(strconv.Itoa(os.Getpid()), 10, 32)
	if err != nil {
		return nil, err
	}

	conns, err := gpnet.ConnectionsPid("tcp", int32(pid))
	if err != nil {
		return nil, err
	}

	seen := make(map[uint32]bool)

	for _, conn := range conns {
		if conn.Status == "LISTEN" {
			seen[conn.Laddr.Port] = true
		}
	}

	ports := make([]uint32, 0, len(seen))

	for port := range seen {
		ports = append(ports, port)
	}

	slices.Sort(ports)

	return ports, nil
}

func portNumber(port string) uint32 {
	parsed, err := strconv.ParseUint(port, 10, 32)
	So(err, ShouldBeNil)

	return uint32(parsed)
}
