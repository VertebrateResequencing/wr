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
// to the manager more than once, and stops it doing so for an add that re-adds
// complete jobs.

import (
	"errors"
	"sync/atomic"
	"time"

	"go.nanomsg.org/mangos/v3"
)

// droppingAddSentHook, if set, runs in sendDroppingOnConnectionLossLocked
// between sending the add and waiting for its reply. Tests use it to lose the
// connection in that window.
//
//nolint:gochecknoglobals // test seam; nil in production.
var droppingAddSentHook func()

// completeSkippedErr returns ErrResentAddSkippedComplete if cr, an add asked to
// re-add complete jobs, was sent again skipping them and dups, from the reply,
// says some of its jobs were complete, or has duplicates without saying why (a
// manager too old to break them down). Otherwise it returns nil: every job is
// queued or was added, whichever copy of cr did it.
func completeSkippedErr(cr *clientRequest, dups AddDuplicates) error {
	if !cr.resentSkippingComplete || dups.Total() == 0 {
		return nil
	}

	if breakdown, ok := dups.Breakdown(); ok && breakdown.Complete == 0 {
		return nil
	}

	return ErrResentAddSkippedComplete
}

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

// requestWithSendDeadlineAtLeastLocked is requestWatchingForResendLocked with
// the socket's send deadline widened to at least sendWait for this request
// only, never narrowed; one that is not positive already waits for ever. It
// must be called with the client's lock held.
func (c *Client) requestWithSendDeadlineAtLeastLocked(cr *clientRequest,
	sendWait time.Duration,
) (sr *serverResponse, err error) {
	original, err := c.deadline(mangos.OptionSendDeadline)
	if err != nil {
		return nil, err
	}

	if original <= 0 || original >= sendWait {
		return c.requestWatchingForResendLocked(cr)
	}

	if err = c.sock.SetOption(mangos.OptionSendDeadline, sendWait); err != nil {
		return nil, err
	}

	defer func() {
		if errr := c.sock.SetOption(mangos.OptionSendDeadline, original); errr != nil {
			sr, err = nil, errors.Join(err, errr)
		}
	}()

	return c.requestWatchingForResendLocked(cr)
}

// sendDroppingOnConnectionLossLocked sends cr and receives the reply, with the
// socket's resend time 0 for this request only, which makes mangos drop the
// request instead of sending it again if the connection it went out on is lost
// before its reply arrives. It reports lost true, and no error, if that
// happened: the manager may or may not have acted on cr. It must be called with
// the client's lock held, which keeps every other request off the socket until
// the resend time is put back.
func (c *Client) sendDroppingOnConnectionLossLocked(cr *clientRequest) (sr *serverResponse, lost bool, err error) {
	if err = c.sock.SetOption(mangos.OptionRetryTime, time.Duration(0)); err != nil {
		return nil, false, err
	}

	defer func() {
		if errr := c.sock.SetOption(mangos.OptionRetryTime, clientRequestResendTime); errr != nil {
			sr, lost, err = nil, false, errors.Join(err, errr)
		}
	}()

	if err = c.encodeAndSend(cr); err != nil {
		return nil, false, err
	}

	if droppingAddSentHook != nil {
		droppingAddSentHook()
	}

	sr, err = c.recvAndDecode()

	// a dropped request wakes a waiting Recv with ErrCanceled; one dropped
	// before Recv began leaves no request to wait for, so ErrProtoState
	if errors.Is(err, mangos.ErrCanceled) || errors.Is(err, mangos.ErrProtoState) {
		return nil, true, nil
	}

	return sr, false, err
}
