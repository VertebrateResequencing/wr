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

package client

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	resendTestJobs          = 3
	resendTestTimeout       = 10 * time.Second
	resendTestPoll          = 10 * time.Millisecond
	resendTestReconnectHold = 500 * time.Millisecond
	proxyBufferSize         = 32 * 1024
)

var errResendTestPending = errors.New("SubmitJobs did not return")

// replyDroppingProxy forwards TCP connections to a manager. Once armed, it
// closes the connection the manager next sends anything on instead of
// forwarding it, as happens when a manager stops after acting on a request but
// before its reply reaches the client.
//
// While paused, it refuses new connections, counting them.
type replyDroppingProxy struct {
	listener net.Listener
	target   string
	armed    atomic.Bool
	dropped  atomic.Int32
	paused   atomic.Bool
	refused  atomic.Int32
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

// startReplyDroppingProxy starts a replyDroppingProxy to target, stopping it
// when the test ends.
func startReplyDroppingProxy(t *testing.T, target string) *replyDroppingProxy {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "localhost:0")
	So(err, ShouldBeNil)

	p := &replyDroppingProxy{listener: listener, target: target}

	p.wg.Go(p.accept)

	t.Cleanup(func() {
		_ = listener.Close()

		p.wg.Wait()
	})

	return p
}

// port is the port the proxy listens on.
func (p *replyDroppingProxy) port() string {
	addr, ok := p.listener.Addr().(*net.TCPAddr)
	So(ok, ShouldBeTrue)

	return strconv.Itoa(addr.Port)
}

func (p *replyDroppingProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}

		if p.paused.Load() {
			p.refused.Add(1)

			_ = client.Close()

			continue
		}

		var dialer net.Dialer

		server, err := dialer.DialContext(context.Background(), "tcp", p.target)
		if err != nil {
			_ = client.Close()

			continue
		}

		p.mu.Lock()
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()

		p.wg.Go(func() { p.forward(client, server, false) })
		p.wg.Go(func() { p.forward(server, client, true) })
	}
}

// cut closes every connection the proxy is forwarding.
func (p *replyDroppingProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range p.conns {
		_ = conn.Close()
	}

	p.conns = nil
}

// forward copies what arrives on from to to, closing both once either closes,
// or, if fromManager, once something arrives while the proxy is armed.
func (p *replyDroppingProxy) forward(from, to net.Conn, fromManager bool) {
	defer func() {
		_ = from.Close()
		_ = to.Close()
	}()

	buf := make([]byte, proxyBufferSize)

	for {
		n, err := from.Read(buf)
		if n > 0 && fromManager && p.armed.CompareAndSwap(true, false) {
			p.dropped.Add(1)

			return
		}

		if n > 0 {
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
		}

		if err != nil {
			return
		}
	}
}

// TestSchedulerSubmitJobsResentAfterItsReplyWasLost checks that a SubmitJobs
// whose add the manager acted on, but whose reply was lost with its connection,
// succeeds when the client resends it and the manager reports those jobs as
// already queued: they are this call's own jobs
// (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestSchedulerSubmitJobsResentAfterItsReplyWasLost(t *testing.T) {
	Convey("Given a Scheduler talking to a manager through a connection that can lose a reply", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		proxy := startReplyDroppingProxy(t, net.JoinHostPort("localhost", config.Port))

		jq, err := jobqueue.ConnectWithTokenFile(net.JoinHostPort("localhost", proxy.port()), config.CAFile,
			config.CertDomain, config.TokenFile, resendTestTimeout)
		So(err, ShouldBeNil)

		defer func() {
			So(jq.Disconnect(), ShouldBeNil)
		}()

		s := &Scheduler{jq: jq, cwd: t.TempDir()}

		jobs := make([]*jobqueue.Job, resendTestJobs)
		for i := range jobs {
			jobs[i] = s.NewJob("echo resent "+strconv.Itoa(i), "rg-resent", "req-resent", "", "", nil)
		}

		Convey("SubmitJobs succeeds, adding each job once, when its add is resent after the manager acted on it", func() {
			proxy.armed.Store(true)

			err = s.SubmitJobs(jobs)

			So(proxy.dropped.Load(), ShouldEqual, 1)
			So(err, ShouldBeNil)
			So(server.GetServerStats().Ready, ShouldEqual, resendTestJobs)

			Convey("and submitting the same jobs again without a resend still returns ErrDuplicateJobs", func() {
				err = s.SubmitJobs(jobs)
				So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
				So(server.GetServerStats().Ready, ShouldEqual, resendTestJobs)
			})
		})

		Convey("SubmitJobs of jobs already queued, sent while the client reconnects, returns ErrDuplicateJobs", func() {
			So(s.SubmitJobs(jobs), ShouldBeNil)

			proxy.paused.Store(true)
			proxy.cut()

			// a refused connection is the client redialling, so its
			// connection has gone and the next add must wait for a new one
			deadline := time.Now().Add(resendTestTimeout)
			for proxy.refused.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(resendTestPoll)
			}

			So(proxy.refused.Load(), ShouldBeGreaterThan, 0)

			done := make(chan error, 1)

			go func() { done <- s.SubmitJobs(jobs) }()

			time.Sleep(resendTestReconnectHold)
			proxy.paused.Store(false)

			select {
			case err = <-done:
			case <-time.After(resendTestTimeout):
				err = errResendTestPending
			}

			So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
			So(proxy.dropped.Load(), ShouldEqual, 0)
			So(server.GetServerStats().Ready, ShouldEqual, resendTestJobs)
		})

		Convey("SubmitJobs of jobs already queued returns ErrDuplicateJobs when nothing was resent", func() {
			So(s.SubmitJobs(jobs), ShouldBeNil)

			err = s.SubmitJobs(jobs)
			So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
			So(proxy.dropped.Load(), ShouldEqual, 0)
		})
	})
}
