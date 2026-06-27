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
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/phayes/freeport"
	. "github.com/smartystreets/goconvey/convey"
)

// TestPprofServerLifecycle proves the opt-in WR_PPROF_ADDR endpoint is reachable
// while the manager runs and that its listener is genuinely closed once the
// manager stops, rather than being leaked for the lifetime of the process.
func TestPprofServerLifecycle(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("With WR_PPROF_ADDR set to a free loopback port", t, func() {
		ctx := context.Background()
		_, serverConfig, _, _, _ := jobqueueTestInit(false)

		port, err := freeport.GetFreePort()
		So(err, ShouldBeNil)

		pprofAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		pprofURL := fmt.Sprintf("http://%s/debug/pprof/", pprofAddr)

		t.Setenv(envPprofAddr, pprofAddr)

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		Convey("the pprof endpoint is reachable while the server runs", func() {
			So(pprofEndpointReachable(pprofURL), ShouldBeTrue)

			Convey("and after the server stops the listener is closed", func() {
				server.Stop(ctx, true)

				// the ListenAndServe goroutine is shut down asynchronously, so
				// poll briefly for the port to become re-bindable (which it can
				// only be once the pprof listener has actually closed).
				So(eventuallyRebindable(ctx, pprofAddr), ShouldBeTrue)
				So(pprofEndpointReachable(pprofURL), ShouldBeFalse)
			})
		})

		Reset(func() {
			// belt-and-braces: make sure the server is stopped even if an inner
			// assertion failed before the explicit Stop above.
			server.Stop(ctx, true)
		})
	})
}

// pprofEndpointReachable reports whether an HTTP GET to the given pprof URL
// succeeds (any response status counts as reachable; we only care that the
// listener answered).
func pprofEndpointReachable(url string) bool {
	client := &http.Client{Timeout: time.Second}

	resp, err := client.Get(url) //nolint:noctx // short-lived test probe with its own timeout
	if err != nil {
		return false
	}

	_ = resp.Body.Close()

	return true
}

// eventuallyRebindable polls for up to a few seconds until a fresh listener can
// bind the given address, which proves the previous listener has been closed.
func eventuallyRebindable(ctx context.Context, addr string) bool {
	var lc net.ListenConfig

	limit := time.After(5 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)

	defer ticker.Stop()

	for {
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err == nil {
			_ = ln.Close()

			return true
		}

		select {
		case <-limit:
			return false
		case <-ticker.C:
		}
	}
}
