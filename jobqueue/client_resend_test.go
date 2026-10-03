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
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol/rep"
)

// cliConnectTimeout is the connect timeout wr's commands give a Client by
// default (cmd/root.go defaultManagerConnectTimeout): over the 60s
// ClientMinRequestTimeout floor, so it is also the receive deadline.
const cliConnectTimeout = 120 * time.Second

// longConnectTimeout is a connect timeout far beyond any resend time a fix
// might pick as merely generous, so only one that never comes round passes.
const longConnectTimeout = 10 * time.Hour

// heldAddWait is how long a copyCountingManager holds its reply to an add in
// the scaled-down tests below: within the receive deadline they give the
// socket, and past the connect timeout.
const heldAddWait = 2 * time.Second

// copyCountingManagerWorkers is how many requests a copyCountingManager
// handles at once: more than one, so that a copy of a request arriving while
// the manager still holds the first is received and counted, as the real
// manager would receive and act on it.
const copyCountingManagerWorkers = 4

// copyCountingManager stands in for a manager on a real TLS rep socket. It
// answers pings at once, and counts every add it receives, holding its reply to
// each for hold. If dropFirst is set it closes the connection the first add
// arrived on without answering it, as a manager that goes away mid-request
// does. It counts every other request too, answering at once with the error in
// answers for its method if there is one, and otherwise holding its reply for
// hold as well.
type copyCountingManager struct {
	addr      string
	caFile    string
	hold      time.Duration
	dropFirst bool
	adds      atomic.Int32
	stop      chan struct{}
	answers   map[string]string

	mu       sync.Mutex
	received map[string]int
}

// startCopyCountingManager starts a copyCountingManager, stopping it when the
// test ends.
func startCopyCountingManager(t *testing.T, hold time.Duration, dropFirst bool) *copyCountingManager {
	t.Helper()

	return startAnsweringManager(t, hold, dropFirst, nil)
}

// startAnsweringManager is startCopyCountingManager, also giving it the answers
// to give at once, by method.
func startAnsweringManager(t *testing.T, hold time.Duration, dropFirst bool,
	answers map[string]string,
) *copyCountingManager {
	t.Helper()

	caFile := generateTestCerts(t)
	dir := filepath.Dir(caFile)

	tlsConfig, err := serverTLSConfig(caFile, filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	So(err, ShouldBeNil)

	sock, err := rep.NewSocket()
	So(err, ShouldBeNil)
	So(sock.SetOption(mangos.OptionMaxRecvSize, 0), ShouldBeNil)

	port, err := freeEphemeralTestPort()
	So(err, ShouldBeNil)
	So(listenTLS(sock, tlsConfig, strconv.Itoa(port)), ShouldBeNil)

	m := &copyCountingManager{
		addr:      "localhost:" + strconv.Itoa(port),
		caFile:    caFile,
		hold:      hold,
		dropFirst: dropFirst,
		stop:      make(chan struct{}),
		answers:   answers,
		received:  make(map[string]int),
	}

	var wg sync.WaitGroup

	for range copyCountingManagerWorkers {
		sctx, errc := sock.OpenContext()
		So(errc, ShouldBeNil)

		wg.Go(func() { m.serve(sctx) })
	}

	t.Cleanup(func() {
		close(m.stop)

		_ = sock.Close()

		wg.Wait()
	})

	return m
}

// serve answers requests arriving on sctx until the socket closes.
func (m *copyCountingManager) serve(sctx mangos.Context) {
	for {
		msg, err := sctx.RecvMsg()
		if err != nil {
			return
		}

		cr := &clientRequest{}
		if err = codec.NewDecoderBytes(msg.Body, new(codec.BincHandle)).Decode(cr); err != nil {
			msg.Free()

			continue
		}

		sr := &serverResponse{SInfo: &ServerInfo{}}

		if cr.Method == requestMethodAdd && m.adds.Add(1) == 1 && m.dropFirst {
			_ = msg.Pipe.Close()

			msg.Free()

			continue
		}

		if !m.answer(cr, sr) {
			return
		}

		var encoded []byte
		if err = codec.NewEncoderBytes(&encoded, new(codec.BincHandle)).Encode(sr); err != nil {
			return
		}

		msg.Body = encoded
		if err = sctx.SendMsg(msg); err != nil {
			msg.Free()
		}
	}
}

// answer fills in sr, the reply to cr, after holding it if cr is not a ping or a
// request with an answer in m.answers. It reports false if m was stopped
// first.
func (m *copyCountingManager) answer(cr *clientRequest, sr *serverResponse) bool {
	if cr.Method == requestMethodPing {
		return true
	}

	m.mu.Lock()
	m.received[cr.Method]++
	m.mu.Unlock()

	if answer, ok := m.answers[cr.Method]; ok {
		sr.Err = answer

		return true
	}

	select {
	case <-time.After(m.hold):
	case <-m.stop:
		return false
	}

	sr.Added = len(cr.Jobs)

	return true
}

// receivedCopies returns how many requests of method m has received.
func (m *copyCountingManager) receivedCopies(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.received[method]
}

// connect connects a Client to the manager with the given connect timeout.
func (m *copyCountingManager) connect(timeout time.Duration) *Client {
	jq, err := Connect(m.addr, m.caFile, "localhost", make([]byte, tokenLength), timeout)
	So(err, ShouldBeNil)

	return jq
}

// TestClientDoesNotResendSlowRequests proves that a request a live manager is
// slow to answer reaches it once. mangos's req socket resends any request still
// unanswered after its resend time (one minute unless set), on the same live
// connection, while waiting for the reply up to the receive deadline: with a
// connect timeout over 60s, such as wr's commands' 120s default, the manager
// was handed a second copy of an add, reserve, release or bury it was still
// working on, and acted on both (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestClientDoesNotResendSlowRequests(t *testing.T) {
	Convey("Given a manager that holds its reply to an add", t, func() {
		Convey("a Client connected with the CLI's default timeout never resends a request it is still waiting on", func() {
			m := startCopyCountingManager(t, heldAddWait, false)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			// a behavioural run of this at full scale takes over a minute,
			// so here the socket's own resend time is compared with the
			// longest wait for a reply it can be given, which is what decides
			// whether mangos resends before the reply is given up on
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))

			added, err := addOne(jq)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.adds.Load(), ShouldEqual, 1)
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))

			_, finished, err := requestWithinHeldAdd(jq, heldAddWait+time.Second)
			So(finished, ShouldBeTrue)
			So(err, ShouldBeNil)
			So(m.adds.Load(), ShouldEqual, 2)
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))
		})

		Convey("a Client connected with a timeout of many hours still never resends", func() {
			m := startCopyCountingManager(t, 0, false)

			jq := m.connect(longConnectTimeout)
			defer disconnect(jq)

			So(recvDeadline(jq), ShouldEqual, longConnectTimeout)
			So(resendTime(jq), ShouldBeGreaterThan, recvDeadline(jq))
		})

		Convey("a reply slower than the connect timeout is waited for without a resend", func() {
			defer setClientMinRequestTimeout(heldAddWait + 2*time.Second)()

			m := startCopyCountingManager(t, heldAddWait, false)

			jq := m.connect(time.Second)
			defer disconnect(jq)

			added, err := addOne(jq)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.adds.Load(), ShouldEqual, 1)
		})

		Convey("an add whose connection drops is still resent once the Client redials", func() {
			m := startCopyCountingManager(t, 0, true)

			jq := m.connect(cliConnectTimeout)
			defer disconnect(jq)

			added, err := addOne(jq)
			So(err, ShouldBeNil)
			So(added, ShouldEqual, 1)
			So(m.adds.Load(), ShouldEqual, 2)
		})
	})
}

// resendTime returns jq's socket's resend time.
func resendTime(jq *Client) time.Duration {
	return socketDuration(jq, mangos.OptionRetryTime)
}

// socketDuration returns the duration option of jq's socket.
func socketDuration(jq *Client, option string) time.Duration {
	jq.Lock()
	defer jq.Unlock()

	d, err := jq.deadline(option)
	So(err, ShouldBeNil)

	return d
}

// recvDeadline returns jq's socket's receive deadline.
func recvDeadline(jq *Client) time.Duration {
	return socketDuration(jq, mangos.OptionRecvDeadline)
}

// addOne adds a single job with jq, returning what Add returns.
func addOne(jq *Client) (int, error) {
	return addOneJob(jq, true)
}

// addOneJob adds a single job with jq, skipping it if complete only if
// ignoreComplete, returning what Add returns.
func addOneJob(jq *Client, ignoreComplete bool) (int, error) {
	added, _, err := jq.Add([]*Job{{Cmd: "echo resend", Cwd: "/tmp", RepGroup: "resend"}}, nil, ignoreComplete)

	return added, err
}

// requestWithinHeldAdd sends an add with requestWithin, asking for budget, and
// reports what requestWithinLimited does, with a limit past budget.
func requestWithinHeldAdd(jq *Client, budget time.Duration) (time.Duration, bool, error) {
	return requestWithinLimited(jq, &clientRequest{Method: requestMethodAdd}, budget, budget+time.Second)
}
