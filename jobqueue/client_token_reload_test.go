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
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.nanomsg.org/mangos/v3"
)

// tokenReloadPingWait is how long a client is given to find the restarted
// manager at all, before its first real request.
const tokenReloadPingWait = 10 * time.Second

// tokenReloadConcurrentCalls is how many requests race each other into the
// restarted manager in the concurrency test.
const tokenReloadConcurrentCalls = 8

// tokenReloadBoundedBudget is the bound given to a request whose first send is
// rejected for its token, and tokenReloadSlowRejection is how long that
// rejection is made to take to arrive, most of the bound.
const (
	tokenReloadBoundedBudget = 1 * time.Second
	tokenReloadSlowRejection = 600 * time.Millisecond
)

func TestClientTokenReload(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a live manager that writes its token to a file", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		applySubscriptionReconnectTimings(&serverConfig, 50*time.Millisecond, tokenReloadPingWait)
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		Convey("connecting with a blank token file path is refused with a clear error", func() {
			jq, errc := ConnectWithTokenFile(addr, serverConfig.CAFile, serverConfig.CertDomain, "", clientConnectTime)
			So(errc, ShouldEqual, ErrNoTokenFile)
			So(jq, ShouldBeNil)
		})

		Convey("a client connected with the token file keeps working after a clean restart", func() {
			jq, errc := ConnectWithTokenFile(addr, serverConfig.CAFile, serverConfig.CertDomain,
				serverConfig.TokenFile, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			_, err = jq.GetLimitGroups()
			So(err, ShouldBeNil)

			var newToken []byte

			server, newToken = cleanlyRestartManager(ctx, server, serverConfig)
			So(string(newToken), ShouldNotEqual, string(token))
			So(waitForPing(jq), ShouldBeTrue)

			_, err = jq.GetLimitGroups()
			So(err, ShouldBeNil)

			_, err = jq.GetLimitGroups()
			So(err, ShouldBeNil)
		})

		Convey("concurrent requests from such a client all succeed after a clean restart", func() {
			jq, errc := ConnectWithTokenFile(addr, serverConfig.CAFile, serverConfig.CertDomain,
				serverConfig.TokenFile, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			server, _ = cleanlyRestartManager(ctx, server, serverConfig)

			So(waitForPing(jq), ShouldBeTrue)

			var (
				wg     sync.WaitGroup
				mu     sync.Mutex
				failed []error
			)

			for range tokenReloadConcurrentCalls {
				wg.Go(func() {
					if _, errg := jq.GetLimitGroups(); errg != nil {
						mu.Lock()

						failed = append(failed, errg)
						mu.Unlock()
					}
				})
			}

			wg.Wait()
			So(failed, ShouldBeEmpty)
		})

		Convey("a subscription from such a client resubscribes after a clean restart", func() {
			jq, errc := ConnectWithTokenFile(addr, serverConfig.CAFile, serverConfig.CertDomain,
				serverConfig.TokenFile, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			sub, errs := jq.SubscribeToJobKeys(ctx, []string{"token-reload-subscription"})
			So(errs, ShouldBeNil)

			defer sub.Unsubscribe()

			server, _ = cleanlyRestartManager(ctx, server, serverConfig)

			update := receiveSubscriptionUpdate(sub, tokenReloadPingWait)
			So(update, ShouldNotBeNil)
			So(update.Kind, ShouldEqual, JobUpdateResync)
		})

		Convey("a bounded request resent after a reload gets only what is left of its bound", func() {
			So(os.WriteFile(serverConfig.TokenFile, mismatchedToken(token), ownerReadWrite), ShouldBeNil)

			jq, errc := ConnectWithTokenFile(addr, serverConfig.CAFile, serverConfig.CertDomain,
				serverConfig.TokenFile, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			So(os.WriteFile(serverConfig.TokenFile, token, ownerReadWrite), ShouldBeNil)

			realSock := jq.sock
			slowSock := &slowFirstReplySocket{Socket: realSock, delay: tokenReloadSlowRejection}
			jq.sock = slowSock

			// the manager holds a reserve against an empty queue open for
			// heldReplyWait, longer than the bound, so the resend times out
			_, err = jq.requestWithin(&clientRequest{Method: requestMethodReserve, Timeout: heldReplyWait},
				tokenReloadBoundedBudget)

			jq.sock = realSock

			So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)
			So(jq.tokenReloads, ShouldEqual, 1)

			deadlines := slowSock.recvDeadlines()
			So(len(deadlines), ShouldEqual, 3)
			So(deadlines[0], ShouldEqual, tokenReloadBoundedBudget)
			So(deadlines[1], ShouldBeLessThanOrEqualTo, tokenReloadBoundedBudget-tokenReloadSlowRejection)
		})

		Convey("a client given a raw token does not look for a new one", func() {
			jq, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			server, _ = cleanlyRestartManager(ctx, server, serverConfig)

			So(waitForPing(jq), ShouldBeTrue)

			_, err = jq.GetLimitGroups()
			So(isPermissionDeniedErr(err), ShouldBeTrue)
		})

		Convey("a client whose token file still holds a wrong token fails without retrying", func() {
			So(os.WriteFile(serverConfig.TokenFile, mismatchedToken(token), ownerReadWrite), ShouldBeNil)

			jq, errc := ConnectWithTokenFile(addr, serverConfig.CAFile, serverConfig.CertDomain,
				serverConfig.TokenFile, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			_, err = jq.GetLimitGroups()
			So(isPermissionDeniedErr(err), ShouldBeTrue)
			So(jq.tokenReloads, ShouldEqual, 0)

			Convey("and retries once, still failing, when the file changes to another wrong token", func() {
				otherWrong := append([]byte(nil), token...)
				otherWrong[1] ^= 1
				So(os.WriteFile(serverConfig.TokenFile, otherWrong, ownerReadWrite), ShouldBeNil)

				_, err = jq.GetLimitGroups()
				So(isPermissionDeniedErr(err), ShouldBeTrue)
				So(jq.tokenReloads, ShouldEqual, 1)
			})
		})
	})
}

// cleanlyRestartManager stops server the way `wr manager stop` does, deleting
// its token file, then starts a new manager on the same config, which generates
// a new token. It returns the new server and its token.
func cleanlyRestartManager(ctx context.Context, server *Server, serverConfig ServerConfig) (*Server, []byte) {
	server.Stop(ctx, true)
	So(os.Remove(serverConfig.TokenFile), ShouldBeNil)

	newServer, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	return newServer, token
}

// waitForPing reports whether jq can ping its manager within
// tokenReloadPingWait. Ping needs no token, so this only says the client's
// socket has found the manager again.
func waitForPing(jq *Client) bool {
	deadline := time.Now().Add(tokenReloadPingWait)

	for time.Now().Before(deadline) {
		if _, err := jq.Ping(time.Second); err == nil {
			return true
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}

// slowFirstReplySocket is the socket it wraps, except that the first reply it
// receives is handed over only after delay, and it records every receive
// deadline set on it.
type slowFirstReplySocket struct {
	mangos.Socket
	delay time.Duration

	mu        sync.Mutex
	received  bool
	deadlines []time.Duration
}

func (s *slowFirstReplySocket) Recv() ([]byte, error) {
	b, err := s.Socket.Recv()

	s.mu.Lock()
	first := !s.received
	s.received = true
	s.mu.Unlock()

	if first {
		time.Sleep(s.delay)
	}

	return b, err
}

func (s *slowFirstReplySocket) SetOption(name string, value any) error {
	if d, ok := value.(time.Duration); ok && name == mangos.OptionRecvDeadline {
		s.mu.Lock()
		s.deadlines = append(s.deadlines, d)
		s.mu.Unlock()
	}

	return s.Socket.SetOption(name, value)
}

func (s *slowFirstReplySocket) recvDeadlines() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]time.Duration(nil), s.deadlines...)
}
