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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// serverInfoTestReaders is how many callers read a client's ServerInfo at once
// while serverInfoTestReplacements replacements of it are made, as reconnects
// would make them.
const (
	serverInfoTestReaders      = 4
	serverInfoTestReplacements = 200
)

// TestClientServerInfoAcrossReconnect checks that what a client knows of its
// manager can be read while a subscription reconnect replaces it, as an app
// calling GetSchedulerAlerts alongside a WaitForJobs that rides out a restart
// does. Run under -race, an unguarded read is reported as a data race.
func TestClientServerInfoAcrossReconnect(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a client with a subscription to a manager that writes its token to a file", t, func() {
		ctx := context.Background()
		serverConfig, addr, _, clientConnectTime := subscriptionTestConfig(t)
		applySubscriptionReconnectTimings(&serverConfig, 50*time.Millisecond, tokenReloadPingWait)
		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		jq, sub := subscribedTokenFileClient(ctx, addr, serverConfig, clientConnectTime)
		defer disconnect(jq)
		defer sub.Unsubscribe()

		Convey("its manager's details can be read by many callers while a reconnect replaces them", func() {
			stop := make(chan struct{})

			var (
				wg     sync.WaitGroup
				failed atomic.Int64
			)

			for range serverInfoTestReaders {
				wg.Go(func() {
					for {
						select {
						case <-stop:
							return
						default:
						}

						if _, errg := jq.GetSchedulerAlerts(); errg != nil {
							failed.Add(1)
						}

						if si := jq.CurrentServerInfo(); si == nil || si.WebPort == "" {
							failed.Add(1)
						}
					}
				})
			}

			for range serverInfoTestReplacements {
				jq.adoptServerInfo(jq.CurrentServerInfo())
				time.Sleep(time.Millisecond)
			}

			close(stop)
			wg.Wait()

			So(failed.Load(), ShouldEqual, 0)
		})

		Convey("its manager's details can be read throughout a clean restart that the subscription rides out", func() {
			stop := make(chan struct{})

			var (
				wg      sync.WaitGroup
				unknown atomic.Bool
			)

			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}

					jq.GetSchedulerAlerts() //nolint:errcheck // the manager is down for part of this

					if si := jq.CurrentServerInfo(); si == nil || si.WebPort == "" {
						unknown.Store(true)
					}
				}
			})

			server, _ = cleanlyRestartManager(ctx, server, serverConfig)

			update := receiveSubscriptionUpdate(sub, tokenReloadPingWait)

			close(stop)
			wg.Wait()

			So(update, ShouldNotBeNil)
			So(update.Kind, ShouldEqual, JobUpdateResync)
			So(unknown.Load(), ShouldBeFalse)

			_, err = jq.GetSchedulerAlerts()
			So(err, ShouldBeNil)

			Convey("and changing the copy it is given does not change what the client uses", func() {
				si := jq.CurrentServerInfo()
				si.WebPort = ""

				_, err = jq.GetSchedulerAlerts()
				So(err, ShouldBeNil)
				So(jq.CurrentServerInfo().WebPort, ShouldNotBeEmpty)
			})
		})
	})
}
