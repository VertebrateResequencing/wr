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

// This file contains how a Client notices that mangos may have sent a request
// to the manager more than once.

import (
	"sync/atomic"

	"go.nanomsg.org/mangos/v3"
)

// resendWatch notices mangos resending a request on a new connection. A req
// socket that loses the connection a request went out on sends that request
// again on the next connection it makes, without telling its caller, though
// the manager may already have acted on the first copy. The socket only makes
// a new connection after losing its last one, so a connection attaching after
// the request went out means it was sent again.
//
// It passes every event on to the hook the socket already had, and puts that
// hook back when it stops.
type resendWatch struct {
	sock     mangos.Socket
	previous mangos.PipeEventHook
	ready    chan struct{}
	attaches atomic.Uint64
	sentAt   uint64
	sent     bool
}

// watchForResend starts watching sock, which must be the client's socket with
// the client's lock held, for a resend of the request about to be sent on it.
func watchForResend(sock mangos.Socket) *resendWatch {
	w := &resendWatch{sock: sock, ready: make(chan struct{})}
	w.previous = sock.SetPipeEventHook(w.hook)
	close(w.ready)

	return w
}

// hook counts connections as mangos attaches them, before it sends anything
// on them, then calls the previous hook. An event arriving while the watch is
// being installed waits until the previous hook is known.
func (w *resendWatch) hook(event mangos.PipeEvent, pipe mangos.Pipe) {
	if event == mangos.PipeEventAttaching {
		w.attaches.Add(1)
	}

	<-w.ready

	if w.previous != nil {
		w.previous(event, pipe)
	}
}

// markSent records that the request has gone out.
func (w *resendWatch) markSent() {
	w.sentAt = w.attaches.Load()
	w.sent = true
}

// stop stops watching, reporting whether a connection attached after the
// request went out, so it may have been sent more than once. That can also be
// reported when the first copy never reached the manager, or its reply arrived
// just before its connection was lost.
func (w *resendWatch) stop() bool {
	w.sock.SetPipeEventHook(w.previous)

	return w.sent && w.attaches.Load() > w.sentAt
}
