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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.nanomsg.org/mangos/v3"
)

// unreadPingWait is how long a ping must take for us to conclude the manager
// never read it: far longer than a reply from a manager that is still serving,
// and far shorter than the ClientMinRequestTimeout floor an unbounded ping
// would wait for.
const unreadPingWait = 2 * time.Second

// unboundedRequestBudget is the deadline a request asks for when the socket it
// goes out on has none: short enough that waiting for it is unmistakable from
// waiting for either the ClientMinRequestTimeout floor or forever.
const unboundedRequestBudget = 200 * time.Millisecond

// TestClientRequestTimeoutDecoupledFromConnect proves that the per-request
// send/recv deadline is decoupled from the (possibly short) timeout passed to
// Connect. A request whose reply legitimately takes longer than the connect
// timeout must still succeed, rather than failing with a spurious mangos
// 'receive time out'. We exercise this with a blocking Reserve on an empty
// queue: the server holds the reply open for the reserve wait before answering
// "nothing ready", and that wait deliberately exceeds the connect timeout.
//
// Regression guard for the CI flake where contention/the race detector delayed
// a client request's reply past the short test connect timeout (1500ms), which
// was being reused as the socket recv deadline (see .docs/bugfixes/260626-3.md).
func TestClientRequestTimeoutDecoupledFromConnect(t *testing.T) {
	Convey("Given a running server", t, func() {
		ctx := context.Background()
		_, serverConfig, addr, _, _ := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		Convey("A request whose reply takes longer than the connect timeout still succeeds", func() {
			// connect with a timeout shorter than the reserve wait below; this
			// timeout bounds only connect-readiness, not subsequent requests.
			connectTimeout := 1 * time.Second
			reserveWait := connectTimeout + 1*time.Second

			jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, connectTimeout)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			// the queue is empty, so the server holds this request open for the
			// full reserveWait before replying "nothing ready". Before the fix,
			// the socket recv deadline equalled connectTimeout and this failed
			// with 'receive time out'; now it returns cleanly.
			start := time.Now()
			job, err := jq.Reserve(reserveWait)
			elapsed := time.Since(start)

			So(err, ShouldBeNil)
			So(job, ShouldBeNil)
			So(elapsed, ShouldBeGreaterThan, connectTimeout)
		})
	})
}

// TestPingBoundedDuringManagerShutdown proves that a client talking to a
// manager part-way through shutdown is bounded by its own timeouts, not by the
// socket's ClientMinRequestTimeout floor. Shutdown stops the RPC readers before
// closing the command socket (jobqueue/server.go closeServerCommsAndDB), so for
// ShutdownSocketWait the port still accepts a request that nothing will ever
// read; the reply the client waits for never comes.
//
// Regression guard for .docs/bugfixes/260828-4.md BUG 6: Ping()'s timeout was
// advisory to the server only, so such a ping blocked for the socket's 60s
// floor, and ShutdownServer() - which pings every ClientShutdownTestInterval -
// took a minute to report a manager that had already gone.
func TestPingBoundedDuringManagerShutdown(t *testing.T) {
	Convey("Given a manager whose shutdown leaves its command socket listening but unread", t, func() {
		ctx := context.Background()

		// a single RPC reader makes the window deterministic: after client
		// handling stops the reader admits one last request and then exits, so
		// everything sent after that goes unread. InterruptTime is long so the
		// reader only ever leaves its receive by admitting a request, never by
		// timing out first.
		defer setNumRPCReaders(1)()

		_, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
		serverConfig.Timings.InterruptTime = 5 * time.Second
		serverConfig.Timings.ShutdownSocketWait = 3 * time.Second

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("A Ping into that window fails on its own timeout, not the socket's floor", func() {
			stopped := make(chan struct{})

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			took, unread := pingUntilUnread(jq, serverConfig.Timings.ShutdownSocketWait)

			So(unread, ShouldBeTrue)
			So(took, ShouldBeLessThan, time.Second)

			<-stopped
		})

		Convey("A request on a socket with no receive deadline is bounded by its own timeout", func() {
			stopped := make(chan struct{})

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			_, unread := pingUntilUnread(jq, serverConfig.Timings.ShutdownSocketWait)
			So(unread, ShouldBeTrue)

			// mangos reads a non-positive receive deadline as "wait forever", so
			// this is the socket state a bounded request most needs to narrow: a
			// reply that never comes would otherwise never be given up on.
			So(jq.sock.SetOption(mangos.OptionRecvDeadline, time.Duration(0)), ShouldBeNil)

			took, returned, err := requestWithinUnread(jq, unboundedRequestBudget, unreadPingWait)
			So(returned, ShouldBeTrue)
			So(took, ShouldBeLessThan, time.Second)

			deadline, optErr := jq.sock.GetOption(mangos.OptionRecvDeadline)
			So(deadline, ShouldEqual, time.Duration(0))
			So(optErr, ShouldBeNil)
			So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)

			<-stopped
		})

		Convey("ShutdownServer reports the manager gone promptly", func() {
			start := time.Now()
			ok := jq.ShutdownServer()
			elapsed := time.Since(start)

			So(ok, ShouldBeTrue)
			So(elapsed, ShouldBeLessThan, 5*time.Second)
		})
	})
}

// pingUntilUnread pings a stopping manager until one of the pings goes unread,
// returning how long that ping took and whether the window was reached within
// limit. limit must be no longer than the manager's ShutdownSocketWait: a ping
// sent after the command socket closes blocks on the send deadline instead, and
// would measure something else.
func pingUntilUnread(c *Client, limit time.Duration) (time.Duration, bool) {
	giveUp := time.Now().Add(limit)

	for time.Now().Before(giveUp) {
		start := time.Now()
		_, err := c.Ping(ClientSuggestedPingTimeout)
		took := time.Since(start)

		if errors.Is(err, mangos.ErrRecvTimeout) || took > unreadPingWait {
			return took, true
		}

		time.Sleep(ClientSuggestedPingTimeout)
	}

	return 0, false
}

// requestWithinUnread sends a request the stopping manager will never answer,
// asking for budget as its receive deadline, and reports how long it took, and
// whether it came back at all within limit. It closes the client's socket if it
// did not: an unbounded receive holds the client lock, so nothing else on that
// client (Disconnect included) could proceed while it waits.
func requestWithinUnread(c *Client, budget, limit time.Duration) (time.Duration, bool, error) {
	type outcome struct {
		took time.Duration
		err  error
	}

	done := make(chan outcome, 1)
	start := time.Now()

	go func() {
		_, err := c.requestWithin(&clientRequest{Method: requestMethodPing, Timeout: budget}, budget)
		done <- outcome{took: time.Since(start), err: err}
	}()

	select {
	case o := <-done:
		return o.took, true, o.err
	case <-time.After(limit):
		_ = c.sock.Close()

		return limit, false, nil
	}
}
