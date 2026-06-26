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
