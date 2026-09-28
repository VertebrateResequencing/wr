//go:build reliability_repro

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

// The slow half of port_selfconnect_test.go: a real TIME_WAIT, left by a real
// self-connect before the manager starts, lasts Linux's fixed 60s, so a test of
// it takes that long. Run it with:
//
//	CGO_ENABLED=1 go test -tags netgo,reliability_repro --count 1 ./jobqueue \
//	  -run TestManagerStartsOverSelfConnectTimeWait -v

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestManagerStartsOverSelfConnectTimeWait(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A manager whose port a client self-connected on before it started waits for the TIME_WAIT", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.Port = pscFreePort(-1)
		serverConfig.WebPort = pscFreePort(-1)

		So(pscSelfConnect(serverConfig.Port), ShouldBeNil)

		var listenConfig net.ListenConfig

		_, err := listenConfig.Listen(ctx, "tcp", "0.0.0.0:"+serverConfig.Port)
		So(errors.Is(err, syscall.EADDRINUSE), ShouldBeTrue)

		started := time.Now()

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		waited := time.Since(started)
		t.Logf("SELFCONNECT-TIMEWAIT port=%s: the manager published after %s", serverConfig.Port,
			waited.Round(time.Second))

		So(waited, ShouldBeGreaterThan, serverBindRetryBudget)
		So(waited, ShouldBeLessThan, serverBindLingerBudget)
	})
}
