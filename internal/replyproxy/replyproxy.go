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

// Package replyproxy provides a TCP proxy for tests that need a client's
// connection to a manager to lose or delay the manager's replies, as when a
// manager stops after acting on a request but before its reply arrives.
package replyproxy

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

const bufferSize = 32 * 1024

// Proxy forwards TCP connections to a manager. Once Armed, it closes the
// connection the manager next sends anything on instead of forwarding it,
// counting that in Dropped.
//
// While Paused, it refuses new connections, counting them in Refused. While
// Swallow is set, it discards what the manager sends, counting each read in
// Swallowed, but keeps the connection open, as when a reply is slow to arrive.
type Proxy struct {
	Armed     atomic.Bool
	Dropped   atomic.Int32
	Swallow   atomic.Bool
	Swallowed atomic.Int32
	Paused    atomic.Bool
	Refused   atomic.Int32

	listener net.Listener
	target   string
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

// Start starts a Proxy to target, stopping it when the test ends.
func Start(t *testing.T, target string) *Proxy {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", "localhost:0")
	if err != nil {
		t.Fatalf("replyproxy listen: %s", err)
	}

	p := &Proxy{listener: listener, target: target}

	p.wg.Go(p.accept)

	t.Cleanup(func() {
		_ = listener.Close()

		p.Cut()
		p.wg.Wait()
	})

	return p
}

// Port is the port the proxy listens on.
func (p *Proxy) Port() string {
	addr, ok := p.listener.Addr().(*net.TCPAddr)
	if !ok {
		return ""
	}

	return strconv.Itoa(addr.Port)
}

// Addr is the localhost address the proxy listens on.
func (p *Proxy) Addr() string {
	return net.JoinHostPort("localhost", p.Port())
}

func (p *Proxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}

		if p.Paused.Load() {
			p.Refused.Add(1)

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

// Cut closes every connection the proxy is forwarding.
func (p *Proxy) Cut() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range p.conns {
		_ = conn.Close()
	}

	p.conns = nil
}

// forward copies what arrives on from to to, closing both once either closes,
// or, if fromManager, once something arrives while the proxy is armed.
func (p *Proxy) forward(from, to net.Conn, fromManager bool) {
	defer func() {
		_ = from.Close()
		_ = to.Close()
	}()

	buf := make([]byte, bufferSize)

	for {
		n, err := from.Read(buf)
		if !p.pass(to, buf[:n], fromManager) || err != nil {
			return
		}
	}
}

// pass forwards data, read from the manager if fromManager, to to, unless the
// proxy is armed or swallowing. It reports whether to keep forwarding.
func (p *Proxy) pass(to net.Conn, data []byte, fromManager bool) bool {
	if len(data) == 0 {
		return true
	}

	if fromManager && p.Armed.CompareAndSwap(true, false) {
		p.Dropped.Add(1)

		return false
	}

	if fromManager && p.Swallow.Load() {
		p.Swallowed.Add(1)

		return true
	}

	_, err := to.Write(data)

	return err == nil
}
