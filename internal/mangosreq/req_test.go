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

package mangosreq

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol"
	"go.nanomsg.org/mangos/v3/protocol/rep"
	_ "go.nanomsg.org/mangos/v3/transport/inproc"
)

// testSendDeadline is the send deadline the test's req socket uses: short, so
// the test waits out a couple of them quickly.
const testSendDeadline = 200 * time.Millisecond

// testTimerLimit bounds the wait for a timer of testSendDeadline to have fired
// and run: its deadline plus generous slack for a loaded host.
const testTimerLimit = testSendDeadline + 5*time.Second

// TestReplyAfterSendDeadline proves that a request's send timer, firing after
// the request was sent, does not cancel the request while it waits for its
// reply, so a reply that arrives after the send deadline is still received.
func TestReplyAfterSendDeadline(t *testing.T) {
	Convey("Given a req socket with a send deadline and a connected rep socket", t, func() {
		fired := make(chan struct{}, 1)

		proto := NewProtocol()
		s, ok := proto.(*socket)
		So(ok, ShouldBeTrue)

		s.sendTimerFired = func() {
			select {
			case fired <- struct{}{}:
			default:
			}
		}

		sock := protocol.MakeSocket(proto)

		Reset(func() { So(sock.Close(), ShouldBeNil) })
		So(sock.SetOption(mangos.OptionSendDeadline, testSendDeadline), ShouldBeNil)

		const addr = "inproc://mangosreq-reply-after-send-deadline"

		repSock, err := rep.NewSocket()
		So(err, ShouldBeNil)
		Reset(func() { So(repSock.Close(), ShouldBeNil) })
		So(repSock.Listen(addr), ShouldBeNil)
		So(sock.Dial(addr), ShouldBeNil)

		Convey("a request whose reply arrives after the send deadline still gets its reply", func() {
			m := mangos.NewMessage(0)
			m.Body = append(m.Body, "request"...)

			So(sock.SendMsg(m), ShouldBeNil)

			got, err := repSock.RecvMsg()
			So(err, ShouldBeNil)

			select {
			case <-fired:
			case <-time.After(testTimerLimit):
				So("it did not", ShouldEqual, "the request's send timer fired")
			}

			// The timer's callback takes the socket lock after the seam
			// returns; give it a whole send deadline to do so before replying.
			time.Sleep(testSendDeadline)

			So(repSock.SendMsg(got), ShouldBeNil)

			reply, err := sock.RecvMsg()
			So(err, ShouldBeNil)
			So(string(reply.Body), ShouldEqual, "request")
			reply.Free()
		})
	})
}

// TestStaleSendTimer proves that an earlier request's send timer, firing as a
// later request on the same context starts sending the same pooled message
// with no peer to send to, does not cancel the later request's own send
// deadline, so the later SendMsg still fails at its deadline instead of
// waiting forever.
func TestStaleSendTimer(t *testing.T) {
	Convey("Given a req socket whose first request was answered and whose peer then went away", t, func() {
		fired := make(chan struct{}, 1)
		release := make(chan struct{})
		releaseStale := sync.OnceFunc(func() { close(release) })

		var firings atomic.Int32

		proto := NewProtocol()
		s, ok := proto.(*socket)
		So(ok, ShouldBeTrue)

		// The first send timer to fire is the first request's, which can only
		// be stale; hold it before it takes the socket lock until the second
		// request is waiting to send.
		s.sendTimerFired = func() {
			if firings.Add(1) == 1 {
				fired <- struct{}{}

				<-release
			}
		}

		sock := protocol.MakeSocket(proto)

		Reset(func() {
			releaseStale()
			So(sock.Close(), ShouldBeNil)
		})
		So(sock.SetOption(mangos.OptionSendDeadline, testSendDeadline), ShouldBeNil)

		const addr = "inproc://mangosreq-stale-send-timer"

		repSock, err := rep.NewSocket()
		So(err, ShouldBeNil)
		So(repSock.Listen(addr), ShouldBeNil)
		So(sock.Dial(addr), ShouldBeNil)

		// Holding an extra reference lets the test send the same *Message
		// again, as the message pool hands a freed message to a later request.
		m := mangos.NewMessage(0)
		m.Body = append(m.Body, "request"...)
		m.Clone()

		So(sock.SendMsg(m), ShouldBeNil)

		got, err := repSock.RecvMsg()
		So(err, ShouldBeNil)
		So(repSock.SendMsg(got), ShouldBeNil)

		reply, err := sock.RecvMsg()
		So(err, ShouldBeNil)
		reply.Free()

		So(repSock.Close(), ShouldBeNil)
		So(waitFor(func() bool { return len(s.pipes) == 0 }, s), ShouldBeTrue)

		select {
		case <-fired:
		case <-time.After(testTimerLimit):
			So("it did not", ShouldEqual, "the first request's send timer fired")
		}

		Convey("a second request with the same message fails at its own send deadline", func() {
			result := make(chan error, 1)

			go func() { result <- sock.SendMsg(m) }()

			So(waitFor(func() bool { return s.defCtx.sendMsg == m }, s), ShouldBeTrue)

			releaseStale()

			select {
			case err = <-result:
				So(err, ShouldEqual, mangos.ErrSendTimeout)
			case <-time.After(testTimerLimit):
				So("it was still waiting", ShouldEqual, "the second SendMsg returned")
			}
		})
	})
}

// waitFor polls cond under the socket's lock until it holds, returning false if
// it does not within testTimerLimit.
func waitFor(cond func() bool, s *socket) bool {
	deadline := time.Now().Add(testTimerLimit)

	for time.Now().Before(deadline) {
		s.Lock()
		ok := cond()
		s.Unlock()

		if ok {
			return true
		}

		time.Sleep(time.Millisecond)
	}

	return false
}
