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
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.nanomsg.org/mangos/v3"
)

const (
	// outageTestReplyWait is the receive deadline of the clients below: much
	// shorter than outageTestHold, so every held request times out.
	outageTestReplyWait = 300 * time.Millisecond
	outageTestHold      = 10 * time.Second
	outageTestRetryWait = 100 * time.Millisecond
	outageTestRetryTime = time.Second
	outageTestKey       = "key"

	outageTestKickMethod    = "jkick"
	outageTestSuspendMethod = "jsuspend"
	outageTestResumeMethod  = "jresume"

	// outageTestMaxCopies is the most copies of a request a client waiting
	// outageTestRetryWait between attempts can send in outageTestRetryTime.
	outageTestMaxCopies = int(outageTestRetryTime/outageTestRetryWait) + 2
)

// errOutageTestPending is what receiveWithin returns if nothing arrived.
var errOutageTestPending = errors.New("still pending")

// TestClientRetryWhileManagerUnreachable checks which failed requests a Client
// that rides out outages sends again: those the manager cannot have acted on
// for every method but shutdown, and those it may have acted on only for
// methods that are safe to apply twice.
func TestClientRetryWhileManagerUnreachable(t *testing.T) {
	Convey("Given a manager that takes requests and is slow to reply, or refuses them", t, func() {
		m := startAnsweringManager(t, outageTestHold, false, map[string]string{
			outageTestKickMethod:    ErrClosedStop,
			requestMethodSubscribe:  ErrClosedStop,
			outageTestResumeMethod:  ErrRecovering,
			outageTestSuspendMethod: ErrBadRequest,
			requestMethodShutdown:   ErrClosedStop,
		})

		jq := m.connect(10 * time.Second)
		defer disconnect(jq)

		jq.Lock()
		So(jq.sock.SetOption(mangos.OptionRecvDeadline, outageTestReplyWait), ShouldBeNil)
		jq.Unlock()

		setOutageTimings(jq, outageTestRetryWait, outageTestRetryTime)

		Convey("a Client that did not opt in sends each request once", func() {
			_, err := addOne(jq)
			So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)
			So(m.adds.Load(), ShouldEqual, 1)

			_, err = jq.Kick([]*JobEssence{{JobKey: outageTestKey}})
			So(err, ShouldResemble, Error{outageTestKickMethod, outageTestKey, ErrClosedStop})
			So(m.receivedCopies(outageTestKickMethod), ShouldEqual, 1)
		})

		Convey("a Client that opted in", func() {
			jq.RetryWhileManagerUnreachable(context.Background())

			Convey("resends an add whose reply timed out until the RetryTime is spent", func() {
				calledAt := time.Now()
				_, err := addOne(jq)

				So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeGreaterThanOrEqualTo, outageTestRetryTime)
				So(m.adds.Load(), ShouldBeGreaterThan, 1)
			})

			Convey("does not resend an add that re-adds complete jobs whose reply timed out", func() {
				calledAt := time.Now()
				_, err := addOneJob(jq, false)

				So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeLessThan, outageTestRetryTime)
				So(m.adds.Load(), ShouldEqual, 1)
			})

			Convey("does not resend a reserve whose reply timed out, since it may have been acted on", func() {
				calledAt := time.Now()
				_, err := jq.Reserve(0)

				So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeLessThan, outageTestRetryTime)
				So(m.receivedCopies(requestMethodReserve), ShouldEqual, 1)
			})

			Convey("resends any request the manager refused while stopping until the RetryTime is spent", func() {
				calledAt := time.Now()
				_, err := jq.Kick([]*JobEssence{{JobKey: outageTestKey}})

				So(err, ShouldResemble, Error{outageTestKickMethod, outageTestKey, ErrClosedStop})
				So(time.Since(calledAt), ShouldBeGreaterThanOrEqualTo, outageTestRetryTime)
				So(m.receivedCopies(outageTestKickMethod), ShouldBeGreaterThan, 1)
				So(m.receivedCopies(outageTestKickMethod), ShouldBeLessThanOrEqualTo, outageTestMaxCopies)
			})

			Convey("resends any request the manager refused while recovering until the RetryTime is spent", func() {
				calledAt := time.Now()
				_, err := jq.Resume([]*JobEssence{{JobKey: outageTestKey}})

				So(err, ShouldResemble, Error{outageTestResumeMethod, outageTestKey, ErrRecovering})
				So(time.Since(calledAt), ShouldBeGreaterThanOrEqualTo, outageTestRetryTime)
				So(m.receivedCopies(outageTestResumeMethod), ShouldBeGreaterThan, 1)
				So(m.receivedCopies(outageTestResumeMethod), ShouldBeLessThanOrEqualTo, outageTestMaxCopies)
			})

			Convey("still waits between attempts when its retry wait is 0", func() {
				setOutageTimings(jq, 0, outageTestRetryTime)

				_, err := jq.Kick([]*JobEssence{{JobKey: outageTestKey}})

				So(err, ShouldResemble, Error{outageTestKickMethod, outageTestKey, ErrClosedStop})
				So(m.receivedCopies(outageTestKickMethod), ShouldBeLessThanOrEqualTo,
					int(outageTestRetryTime/outageRetryFloor)+2)
			})

			Convey("returns any other answer of the manager at once", func() {
				_, err := jq.Suspend([]*JobEssence{{JobKey: outageTestKey}})

				So(err, ShouldResemble, Error{outageTestSuspendMethod, outageTestKey, ErrBadRequest})
				So(m.receivedCopies(outageTestSuspendMethod), ShouldEqual, 1)
			})

			Convey("never resends a shutdown", func() {
				So(jq.ShutdownServer(), ShouldBeFalse)
				So(m.receivedCopies(requestMethodShutdown), ShouldEqual, 1)
			})

			Convey("stops retrying a subscribe once its context is cancelled", func() {
				setOutageTimings(jq, outageTestRetryWait, time.Hour)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				subscribed := make(chan error, 1)

				go func() {
					_, err := jq.SubscribeToJobKeys(ctx, []string{outageTestKey})
					subscribed <- err
				}()

				So(receiveWithin(subscribed, 3*outageTestRetryWait), ShouldEqual, errOutageTestPending)

				cancelledAt := time.Now()

				cancel()

				err := receiveWithin(subscribed, outageTestRetryTime)
				So(errors.Is(err, context.Canceled), ShouldBeTrue)
				So(time.Since(cancelledAt), ShouldBeLessThan, outageTestRetryTime)
				So(m.receivedCopies(requestMethodSubscribe), ShouldBeGreaterThan, 1)
			})

			Convey("stops retrying once the context of the call is done", func() {
				setOutageTimings(jq, outageTestRetryWait, time.Hour)

				ctx, cancel := context.WithTimeout(context.Background(), 2*outageTestReplyWait)
				defer cancel()

				calledAt := time.Now()
				_, err := jq.GetByEssenceContext(ctx, &JobEssence{JobKey: outageTestKey}, false, false)

				So(errors.Is(err, context.DeadlineExceeded), ShouldBeTrue)
				So(errors.Is(err, mangos.ErrRecvTimeout), ShouldBeTrue)
				So(time.Since(calledAt), ShouldBeLessThan, 4*outageTestReplyWait+outageTestRetryWait)
				So(m.receivedCopies(requestMethodGetByCmd), ShouldBeGreaterThan, 1)
			})
		})
	})
}

// setOutageTimings overrides jq's retry wait and retry time.
func setOutageTimings(jq *Client, retryWait, retryTime time.Duration) {
	jq.timingsMu.Lock()
	defer jq.timingsMu.Unlock()

	jq.retryWait = retryWait
	jq.retryTime = retryTime
}

// receiveWithin returns what arrives on ch within wait, or errOutageTestPending.
func receiveWithin(ch <-chan error, wait time.Duration) error {
	select {
	case err := <-ch:
		return err
	case <-time.After(wait):
		return errOutageTestPending
	}
}
