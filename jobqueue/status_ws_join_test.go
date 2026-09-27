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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// statusListenerLateness is how late the status websocket handler is made to
// carry on once it has opened the socket, in TestStatusWSOnDeltaFeedOnceDialled.
const statusListenerLateness = time.Second

// TestStatusWSOnDeltaFeedOnceDialled: once a status page's websocket is open,
// every job transition must reach it, however late the handler that opened it
// gets to carry on. The page asks for its seed counts as soon as the socket
// opens, so a transition the socket is not yet listening for, but that the seed
// was taken too early to include, would leave its counts wrong until a refresh.
func TestStatusWSOnDeltaFeedOnceDialled(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager whose status websocket handler is slow after the upgrade", t, func() {
		serverConfig, addr, standardReqs, clientConnectTime := subscriptionTestConfig(t)
		serverConfig.Timings.ItemTTR = time.Hour

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const repGroup = "rg-status-ws-join"

		added, _, err := jq.Add(subscriptionTestJobs(repGroup, standardReqs, 1), envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)

		server.statusWSUpgradedHook = func() { time.Sleep(statusListenerLateness) }

		Convey("a transition made just after the dial is sent to the page", func() {
			recorder := dialStatusWS(ctx, t, server, token)

			So(len(startSeedOverlapJobs(t, jq, 1)), ShouldEqual, 1)

			So(recorder.waitForMessages(1), ShouldBeTrue)
			So(liveTransitionCount(recorder.snapshot(), repGroup, JobStateReady, JobStateRunning), ShouldEqual, 1)
		})
	})
}
