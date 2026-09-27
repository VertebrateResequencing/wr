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
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// tokenReloadPingWait is how long a client is given to find the restarted
// manager at all, before its first real request.
const tokenReloadPingWait = 10 * time.Second

// tokenReloadConcurrentCalls is how many requests race each other into the
// restarted manager in the concurrency test.
const tokenReloadConcurrentCalls = 8

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
