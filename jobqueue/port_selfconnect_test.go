//go:build !windows

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

// These tests cover a restarting manager losing its own port to a local client
// that redials it. A client's dial takes an ephemeral source port, and when that
// happens to be the manager's (closed) port, TCP simultaneous open connects the
// socket to itself. Go's dialer spots that and closes the socket, which leaves
// the port in TIME_WAIT for 60s, and because a dialled socket has no
// SO_REUSEADDR, nothing can bind the port until that expires. Linux hands out
// even source ports first, so even manager and web ports were the ones exposed.

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal/publishexit"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// pscSweepDials is how many dials the sweep makes: more than there are even
	// ports in Linux's default ephemeral range (32768-60999), so a sweep of
	// sequential source ports passes every one of them.
	pscSweepDials = 20000

	// pscDialTimeout bounds each of the sweep's dials to a closed local port.
	pscDialTimeout = time.Second

	// pscFastFailSlack is how much longer than the busy-port retry budget a
	// Serve against a port another process listens on may take to fail.
	pscFastFailSlack = 3 * time.Second
)

// pscPublication is how a server's publication ended.
type pscPublication string

const (
	pscPublished pscPublication = "published"
	pscExited    pscPublication = "exited through publishexit"
	pscTimedOut  pscPublication = "timed out"
)

// pscWaitPublication waits for server to publish, or for publication to exit
// through publishexit, whichever comes first.
func pscWaitPublication(server *Server, exits <-chan int) pscPublication {
	select {
	case <-server.Serving():
		select {
		case <-exits:
			return pscExited
		default:
			return pscPublished
		}
	case <-exits:
		return pscExited
	case <-time.After(dgsServingWait):
		return pscTimedOut
	}
}

func TestManagerPortSelfConnect(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A client self-connecting to the manager's ports while it recovers does not stop it publishing", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.Port = pscFreePort(-1)
		serverConfig.WebPort = pscFreePort(-1)

		exits := make(chan int, 2)

		defer publishexit.Set(func(code int) { exits <- code })()

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		// a self-connect needs the port to be bindable by a socket without
		// SO_REUSEADDR, which the manager's reservation stops.
		So(pscSelfConnect(serverConfig.Port), ShouldNotBeNil)
		So(pscSelfConnect(serverConfig.WebPort), ShouldNotBeNil)

		release()

		So(pscWaitPublication(server, exits), ShouldEqual, pscPublished)
		So(server.portsBound(), ShouldContain, serverConfig.WebPort)
	})

	Convey("Clients redialling the manager's even port while it recovers never self-connect to it", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.Port = pscFreePort(0)

		exits := make(chan int, 2)

		defer publishexit.Set(func(code int) { exits <- code })()

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		So(pscSweep(serverConfig.Port), ShouldEqual, 0)

		release()

		So(pscWaitPublication(server, exits), ShouldEqual, pscPublished)
	})

	Convey("A manager port held by a closing connection at startup delays the manager rather than killing it", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.Port = pscFreePort(-1)
		serverConfig.WebPort = pscFreePort(-1)

		// a bound but unlistening socket without SO_REUSEADDR is refused to
		// dials and blocks binds exactly as a self-connect's TIME_WAIT does,
		// but for as long as we choose rather than a fixed 60s.
		held := serverBindRetryBudget + 2*time.Second
		release := pscHoldLikeTimeWait(serverConfig.Port)

		go func() {
			<-time.After(held)
			release()
		}()

		defer release()

		started := time.Now()

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(time.Since(started), ShouldBeGreaterThanOrEqualTo, held)
	})

	Convey("A manager port another process listens on fails Serve fast and says so", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.Port = pscFreePort(-1)
		serverConfig.WebPort = pscFreePort(-1)

		listener := pscAcceptingListener(ctx, serverConfig.Port)

		defer func() { _ = listener.Close() }()

		defer publishexit.Set(func(int) {})()

		started := time.Now()

		server, _, _, err := Serve(ctx, serverConfig)
		if server != nil {
			server.Stop(ctx, true)
		}

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "in use by another process")
		So(err.Error(), ShouldContainSubstring, serverConfig.Port)
		So(time.Since(started), ShouldBeLessThan, serverBindRetryBudget+pscFastFailSlack)
	})
}

// pscFreePort returns a free port in the ephemeral range. If parity is 0 or 1,
// the port has that parity (port%2).
func pscFreePort(parity int) string {
	for range 100 {
		var listenConfig net.ListenConfig

		l, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
		So(err, ShouldBeNil)

		port := l.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert,errcheck

		So(l.Close(), ShouldBeNil)

		if parity >= 0 && port%2 != parity {
			port++
		}

		if pscBindable(port) {
			return strconv.Itoa(port)
		}
	}

	So("no free port", ShouldBeBlank)

	return ""
}

// pscBindable reports whether a socket without SO_REUSEADDR can bind port, which
// is what a self-connect and pscHoldLikeTimeWait both need: a port a previous
// listener left sockets in TIME_WAIT on is not.
func pscBindable(port int) bool {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	So(err, ShouldBeNil)

	defer func() { _ = syscall.Close(fd) }()

	return syscall.Bind(fd, &syscall.SockaddrInet4{Port: port}) == nil
}

// pscSelfConnect connects a socket on port to itself, as a client redialling
// the port does when its ephemeral source port happens to be the port it is
// dialling, then closes it, which leaves the port in TIME_WAIT.
func pscSelfConnect(port string) error {
	p, err := strconv.Atoi(port)
	if err != nil {
		return err
	}

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}

	defer func() { _ = syscall.Close(fd) }()

	addr := &syscall.SockaddrInet4{Port: p, Addr: [4]byte{127, 0, 0, 1}}

	if err = syscall.Bind(fd, addr); err != nil {
		return err
	}

	return syscall.Connect(fd, addr)
}

// pscSweep dials the closed local port pscSweepDials times, as clients
// redialling a stopped manager do, and returns how many of the dials connected.
// Go's dialer retries a dial that self-connected, so none should.
func pscSweep(port string) int {
	var (
		dialer    net.Dialer
		connected int
	)

	addr := net.JoinHostPort("127.0.0.1", port)

	for range pscSweepDials {
		ctx, cancel := context.WithTimeout(context.Background(), pscDialTimeout)

		conn, err := dialer.DialContext(ctx, "tcp", addr)

		cancel()

		if err == nil {
			connected++

			_ = conn.Close()
		}
	}

	return connected
}

// pscHoldLikeTimeWait binds 0.0.0.0:port without SO_REUSEADDR and without
// listening, and returns a func that closes it (safe to call more than once).
func pscHoldLikeTimeWait(port string) func() {
	p, err := strconv.Atoi(port)
	So(err, ShouldBeNil)

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	So(err, ShouldBeNil)
	So(syscall.Bind(fd, &syscall.SockaddrInet4{Port: p}), ShouldBeNil)

	closed := make(chan struct{})

	return func() {
		select {
		case <-closed:
		default:
			close(closed)

			_ = syscall.Close(fd)
		}
	}
}

// pscAcceptingListener listens on port and accepts (and closes) connections,
// like another live server would.
func pscAcceptingListener(ctx context.Context, port string) net.Listener {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", "0.0.0.0:"+port)
	So(err, ShouldBeNil)

	go func() {
		for {
			conn, erra := listener.Accept()
			if erra != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	return listener
}
