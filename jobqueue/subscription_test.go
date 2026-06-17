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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	gpnet "github.com/shirou/gopsutil/net"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"nanomsg.org/go-mangos"
)

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

	Convey("A full Go subscription queue does not block dispatch", t, func() {
		server := &Server{clientSubscriptions: make(map[string]*serverSubscription)}
		sub := newServerSubscription([]string{"subscription-a1-full"}, "")
		server.clientSubscriptions["sub"] = sub

		defer sub.close()

		update := &JobUpdate{
			Kind:     JobUpdateTerminal,
			Key:      "subscription-a1-full",
			RepGroup: "subscription-a1",
			State:    JobStateComplete,
		}

		enqueued := 0

		for range serverSubscriptionQueueSize {
			if sub.enqueue(update) {
				enqueued++
			}
		}

		So(enqueued, ShouldEqual, serverSubscriptionQueueSize)

		done := make(chan struct{})
		go func() {
			server.enqueueSubscriptionUpdate(update)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			So("timed out waiting for full subscription queue dispatch to return", ShouldBeBlank)
		}
	})

	Convey("A full Go subscription queue does not accumulate dispatch goroutines", t, func() {
		server := &Server{clientSubscriptions: make(map[string]*serverSubscription)}
		sub := newServerSubscription([]string{"subscription-a1-goroutines"}, "")
		server.clientSubscriptions["sub"] = sub

		defer sub.close()

		update := &JobUpdate{
			Kind:     JobUpdateTerminal,
			Key:      "subscription-a1-goroutines",
			RepGroup: "subscription-a1",
			State:    JobStateComplete,
		}

		enqueued := 0

		for range serverSubscriptionQueueSize {
			if sub.enqueue(update) {
				enqueued++
			}
		}

		So(enqueued, ShouldEqual, serverSubscriptionQueueSize)

		goroutinesBefore := runtime.NumGoroutine()
		done := make(chan struct{})

		go func() {
			for range serverSubscriptionQueueSize + 128 {
				server.enqueueSubscriptionUpdate(update)
			}

			close(done)
		}()

		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
			So("timed out waiting for repeated full subscription queue dispatch", ShouldBeBlank)
		}

		time.Sleep(20 * time.Millisecond)

		goroutinesAfter := runtime.NumGoroutine()
		growth := 0

		if goroutinesAfter > goroutinesBefore {
			growth = goroutinesAfter - goroutinesBefore
		}

		So(growth, ShouldBeLessThan, 16)
	})
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

		defer sock.Close()

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
