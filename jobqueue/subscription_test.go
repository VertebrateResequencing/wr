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

func mismatchedToken(token []byte) []byte {
	wrong := append([]byte(nil), token...)
	wrong[0] ^= 1

	return wrong
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
