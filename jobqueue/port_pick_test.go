//go:build linux

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
	"net"
	"strconv"
	"syscall"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// ppIPv6OnlyHolders is how many ports TestPickedPortsBindForTheManager holds
// with IPv6-only listeners. Among the ~28000 ephemeral ports, a picker that
// ignores them would pick one of them every ~30 picks.
const ppIPv6OnlyHolders = 1000

// TestPickedPortsBindForTheManager checks the test port pickers only hand out
// ports the manager can listen on. The manager listens on 0.0.0.0, which Go
// binds as a dual-stack [::] socket, so a port some other process holds with an
// IPv6-only listener (rpc.statd does, on whatever port it got at boot) refuses
// it with "address already in use", although an IPv4 socket can bind it.
func TestPickedPortsBindForTheManager(t *testing.T) {
	t.Setenv("WR_TEST_LANE", "")

	ppHoldIPv6Only(t, ppIPv6OnlyHolders)

	Convey("With many ports held by IPv6-only listeners", t, func() {
		Convey("freeTestPort only returns ports the manager can listen on", func() {
			for range ppIPv6OnlyHolders {
				port, err := freeTestPort()
				So(err, ShouldBeNil)
				So(ppManagerCanListen(strconv.Itoa(port)), ShouldBeTrue)
			}
		})

		Convey("pscFreePort only returns ports the manager can listen on", func() {
			for _, parity := range []int{-1, 0, 1} {
				for range ppIPv6OnlyHolders / 3 {
					So(ppManagerCanListen(pscFreePort(parity)), ShouldBeTrue)
				}
			}
		})
	})
}

// ppHoldIPv6Only listens on n ports with IPv6-only sockets until the test
// ends, or skips the test if it can't.
func ppHoldIPv6Only(t *testing.T, n int) {
	t.Helper()

	listenConfig := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var sockErr error

		err := c.Control(func(fd uintptr) {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
		})
		if err != nil {
			return err
		}

		return sockErr
	}}

	for range n {
		l, err := listenConfig.Listen(context.Background(), "tcp6", "[::]:0")
		if err != nil {
			t.Skipf("this host cannot make IPv6-only listeners: %s", err)
		}

		t.Cleanup(func() { _ = l.Close() })
	}
}

// ppManagerCanListen reports whether a listener on the manager's address can
// bind port, as the manager's own listener does.
func ppManagerCanListen(port string) bool {
	var listenConfig net.ListenConfig

	l, err := listenConfig.Listen(context.Background(), "tcp", "0.0.0.0:"+port)
	if err != nil {
		return false
	}

	return l.Close() == nil
}
