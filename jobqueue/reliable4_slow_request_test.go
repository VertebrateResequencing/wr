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

// This file tests reliable4 ITEM C1: production ran a 12-minute, 12GB request
// and logged NOTHING about it - it was only ever identified because a profiling
// session happened to be attached. handleRequest must now name a slow request
// itself, while staying silent (and free) for the overwhelming majority of RPCs
// that are fast.

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

// slowRequestTestMethod and slowRequestTestQuietMethod are the request methods
// the direct warnIfSlowRequest tests report on: a repgroup query (the shape of
// production's 12-minute, 12GB request) and one that returns nothing.
const (
	slowRequestTestMethod      = "getbr"
	slowRequestTestNoReplyMeth = "shutdown"
)

// slowRequestTestElapsed is the fake duration the direct warnIfSlowRequest tests
// report. It is production's own 12-minute request, and being minutes long it
// renders unambiguously ("12m...") however loaded the host is.
const slowRequestTestElapsed = 12 * time.Minute

// slowRequestTestWait is how long the live handleRequest tests make a real
// reserve block for. Long enough to beat a driven-down threshold, short enough
// not to slow the suite.
const slowRequestTestWait = 400 * time.Millisecond

// slowRequestTestThreshold is the driven-down threshold used to make
// slowRequestTestWait count as slow.
const slowRequestTestThreshold = 50 * time.Millisecond

// slowRequestTestBodyBytes is the fake request size the decode tests report. A
// decode failure has no method and no selector, so the body's size is the only
// actionable fact the warning can carry.
const slowRequestTestBodyBytes = 12345

// errSlowRequestTestDecode stands in for a codec error in the decode tests.
var errSlowRequestTestDecode = errors.New("test decode failure")

// TestReliable4SlowRequestSelector pins what the warning says a slow request was
// asking about - the only thing that makes it actionable without a profiler
// attached - and that a hostile selector cannot itself bloat the log.
func TestReliable4SlowRequestSelector(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("requestSelector describes what a request was asking about", t, func() {
		So(requestSelector(&clientRequest{Method: "ping"}), ShouldBeEmpty)

		So(requestSelector(&clientRequest{
			Job:           &Job{RepGroup: "rg1"},
			RepGroupMatch: RepGroupMatchPrefix,
			State:         JobStateRunning,
		}), ShouldEqual, "repgroup=rg1 match=prefix state=running")

		So(requestSelector(&clientRequest{LimitGroup: "lg1"}), ShouldEqual, "limitgroup=lg1")
		So(requestSelector(&clientRequest{SchedulerGroup: "sg1"}), ShouldEqual, "schedgroup=sg1")
		So(requestSelector(&clientRequest{Keys: []string{"a", "b"}}), ShouldEqual, "keys=2")
		So(requestSelector(&clientRequest{Jobs: []*Job{{}}}), ShouldEqual, "jobs=1")
	})

	Convey("requestSelector bounds a huge user-supplied selector", t, func() {
		huge := strings.Repeat("r", internal.AbbreviateMax*100)

		got := requestSelector(&clientRequest{Job: &Job{RepGroup: huge}})

		So(len(got), ShouldBeLessThan, internal.AbbreviateMax*2)
		So(got, ShouldContainSubstring, "truncated")
	})
}

// TestReliable4SlowRequestWarning covers the warning itself: its content, and
// that it does not appear below the threshold.
func TestReliable4SlowRequestWarning(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given the default slow-request threshold", t, func() {
		restore := slowRequestThreshold
		defer func() { slowRequestThreshold = restore }()

		Convey("It is well below the client's own give-up timeout", func() {
			// the point of the warning is to reach an operator while the manager
			// is merely degrading, not only after every client has already
			// failed, so it must fire comfortably before ClientMinRequestTimeout.
			So(slowRequestThresholdDefault, ShouldBeLessThan, ClientMinRequestTimeout)
			So(slowRequestThresholdDefault, ShouldBeGreaterThan, time.Second)
			So(slowRequestThreshold, ShouldEqual, slowRequestThresholdDefault)
		})

		Convey("A request over the threshold logs exactly one warning naming it", func() {
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{
				Method:        slowRequestTestMethod,
				Job:           &Job{RepGroup: "rg-slow"},
				RepGroupMatch: RepGroupMatchSubStr,
				State:         JobStateComplete,
			}
			sr := &serverResponse{Jobs: []*Job{{}, {}, {}}}

			warnIfSlowRequest(ctx, cr, sr, "", 4096, time.Now().Add(-slowRequestTestElapsed))

			out := buf.String()
			So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)

			// the LEVEL is the whole point, not an incidental: the manager runs at
			// logLevel "warn" unless started with --debug (cmd/manager.go's
			// setupManagerLogging) and clog wraps the handler in LvlFilterHandler,
			// so demoting this line to debug or info deletes it from the shipped
			// manager log and silently restores production's 12-minute silence.
			So(out, ShouldContainSubstring, "lvl=warn")

			So(out, ShouldContainSubstring, "method="+slowRequestTestMethod)
			So(out, ShouldContainSubstring, "duration=12m")
			So(out, ShouldContainSubstring, "repgroup=rg-slow")
			So(out, ShouldContainSubstring, "match=substr")
			So(out, ShouldContainSubstring, "state=complete")
			So(out, ShouldContainSubstring, "replyBytes=4096")
			So(out, ShouldContainSubstring, "replyJobs=3")
			So(out, ShouldNotContainSubstring, "replyErr")
		})

		Convey("A request under the threshold logs nothing at all", func() {
			slowRequestThreshold = time.Hour
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{Method: slowRequestTestMethod, Job: &Job{RepGroup: "rg-fast"}}

			warnIfSlowRequest(ctx, cr, &serverResponse{}, "", 4096, time.Now().Add(-slowRequestTestElapsed))

			So(buf.String(), ShouldBeEmpty)
		})

		Convey("A slow request that ERRORED reports the reply that was really sent", func() {
			// replyToClient discards sr entirely when srerr is set and sends
			// {Err: srerr}, so reporting len(sr.Jobs) there would credit the client
			// with jobs it never received - and hide the error that a slow request
			// finally produced.
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{Method: slowRequestTestMethod, Job: &Job{RepGroup: "rg-err"}}
			sr := &serverResponse{Jobs: []*Job{{}, {}, {}}}

			warnIfSlowRequest(ctx, cr, sr, ErrDBError, 12, time.Now().Add(-slowRequestTestElapsed))

			out := buf.String()
			So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "replyJobs=0")
			So(out, ShouldContainSubstring, "replyErr=")
			So(out, ShouldContainSubstring, ErrDBError)
		})

		Convey("A request that ASKED to be held for that long is not reported", func() {
			// a subscription long-poll is held for serverSubscriptionHoldTime, 25
			// SECONDS, by design: warning about it would put one line per idle
			// subscriber in the log every 25s and make the warning worthless.
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{Method: requestMethodWaitForUpdates, Timeout: serverSubscriptionHoldTime}

			warnIfSlowRequest(ctx, cr, &serverResponse{}, "", 0, time.Now().Add(-serverSubscriptionHoldTime))

			So(buf.String(), ShouldBeEmpty)

			Convey("but the same method held far longer than it asked for is", func() {
				warnIfSlowRequest(ctx, cr, &serverResponse{}, "", 0,
					time.Now().Add(-slowRequestTestElapsed))

				out := buf.String()
				So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)
				So(out, ShouldContainSubstring, "method="+requestMethodWaitForUpdates)
				So(out, ShouldContainSubstring, "clientWait=25s")
			})
		})

		Convey("A subscription long-poll that asked for NOTHING is still not reported", func() {
			// waitForSubscriptionUpdates clamps timeout <= 0 UP to
			// serverSubscriptionHoldTime, so a client that sends no Timeout at all
			// (which the wire format accepts, and which any hand-written client or a
			// refactor dropping Subscription.updateRequest's Timeout would do) still
			// gets held for 25s BY DESIGN. Without mirroring that clamp this would be
			// one spurious warning per idle subscriber per poll - exactly the log spam
			// the exemption exists to prevent.
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{Method: requestMethodWaitForUpdates}
			So(cr.Timeout, ShouldEqual, 0)

			warnIfSlowRequest(ctx, cr, &serverResponse{}, "", 0,
				time.Now().Add(-serverSubscriptionHoldTime))

			So(buf.String(), ShouldBeEmpty)
		})

		Convey("A subscription long-poll cannot buy MORE than the server's own hold", func() {
			// the other half of the same clamp: waitForSubscriptionUpdates also clamps
			// timeout > serverSubscriptionHoldTime back DOWN to the hold, so the
			// server never holds a poll for an hour however long the client claims.
			// Exempting the whole claim would let a client hide a genuinely wedged
			// waitForUpdates - one stuck on csmutex, say - for as long as it liked.
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{Method: requestMethodWaitForUpdates, Timeout: time.Hour}

			warnIfSlowRequest(ctx, cr, &serverResponse{}, "", 0,
				time.Now().Add(-slowRequestTestElapsed))

			out := buf.String()
			So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "method="+requestMethodWaitForUpdates)
			So(out, ShouldContainSubstring, "clientWait=25s")
		})

		Convey("A method that ignores its Timeout cannot use it to hide", func() {
			// only reserve and the subscription long-poll honour cr.Timeout, so a
			// client cannot mask a slow query by claiming it asked to wait.
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			cr := &clientRequest{Method: slowRequestTestMethod, Timeout: time.Hour}

			warnIfSlowRequest(ctx, cr, &serverResponse{}, "", 0, time.Now().Add(-slowRequestTestElapsed))

			out := buf.String()
			So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "clientWait=0s")
		})

		Convey("A nil response is reported rather than panicking", func() {
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			warnIfSlowRequest(ctx, &clientRequest{Method: slowRequestTestNoReplyMeth}, nil, "", 0,
				time.Now().Add(-slowRequestTestElapsed))

			out := buf.String()
			So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "replyJobs=0")
		})
	})
}

// TestReliable4SlowRequestDecode covers the one part of a request handleRequest
// cannot report as an ordinary slow request: time spent FAILING to decode it.
// Production's silent request was 12 GB, and a body that big can spend minutes
// inside the decoder and then return early, so this path must not be exempt.
func TestReliable4SlowRequestDecode(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a request that cannot be decoded", t, func() {
		restore := slowRequestThreshold
		defer func() { slowRequestThreshold = restore }()

		Convey("A slow decode failure is warned about, naming its size", func() {
			slowRequestThreshold = slowRequestTestThreshold
			ctx, buf := captureLogCtx(context.Background())

			warnIfSlowDecode(ctx, slowRequestTestBodyBytes, time.Now().Add(-slowRequestTestElapsed),
				errSlowRequestTestDecode)

			out := buf.String()
			So(strings.Count(out, slowRequestDecodeLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "lvl=warn")
			So(out, ShouldContainSubstring, "duration=12m")
			So(out, ShouldContainSubstring, "requestBytes="+strconv.Itoa(slowRequestTestBodyBytes))
			So(out, ShouldContainSubstring, errSlowRequestTestDecode.Error())

			// one grep finds both halves of "the manager sat on a request".
			So(out, ShouldContainSubstring, slowRequestLogMsg)
		})

		Convey("A fast decode failure logs nothing at all", func() {
			slowRequestThreshold = time.Hour
			ctx, buf := captureLogCtx(context.Background())

			warnIfSlowDecode(ctx, slowRequestTestBodyBytes, time.Now().Add(-slowRequestTestElapsed),
				errSlowRequestTestDecode)

			So(buf.String(), ShouldBeEmpty)
		})

		Convey("handleRequest reports an undecodable body it was slow to reject", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			server, token, clientID := slowRequestTestServer(ctx, "slow-request-decode")
			body := slowRequestTruncatedBody(server, token, clientID)

			// the real decode of a truncated body is instant, so the only honest way
			// to exercise the wiring is a zero threshold: every decode is then "slow"
			// and the warning must appear for one that FAILED.
			slowRequestThreshold = 0

			logCtx, buf := captureLogCtx(ctx)
			err := server.handleRequest(logCtx, &mangos.Message{Body: body})
			So(err, ShouldNotBeNil)

			out := buf.String()
			So(strings.Count(out, slowRequestDecodeLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "requestBytes="+strconv.Itoa(len(body)))

			// a failed decode has no method, so the ordinary slow-request warning
			// cannot cover this and must not have been emitted instead.
			So(out, ShouldNotContainSubstring, "replyBytes=")

			Convey("but says nothing about it at the shipped default", func() {
				slowRequestThreshold = slowRequestThresholdDefault

				quietCtx, quietBuf := captureLogCtx(ctx)
				errq := server.handleRequest(quietCtx,
					&mangos.Message{Body: slowRequestTruncatedBody(server, token, clientID)})
				So(errq, ShouldNotBeNil)
				So(quietBuf.String(), ShouldBeEmpty)
			})
		})
	})
}

// TestReliable4SlowRequestHandleRequestWiring proves the warning is actually
// wired into handleRequest, the one dispatch point every client RPC passes
// through. Without this, deleting the call site would leave every other
// assertion in this file passing while production went silent again.
func TestReliable4SlowRequestHandleRequestWiring(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a minimal server whose queue is empty", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		restore := slowRequestThreshold
		defer func() { slowRequestThreshold = restore }()

		Convey("handleRequest warns when the MANAGER held a request past the threshold", func() {
			server, token, clientID := slowRequestTestServer(ctx, "slow-request-warn")
			slowRequestThreshold = slowRequestTestThreshold

			slowRequestStallServer(server, slowRequestTestWait)

			logCtx, buf := captureLogCtx(ctx)
			slowRequestReserve(logCtx, server, token, clientID, 0)

			out := buf.String()
			So(strings.Count(out, slowRequestLogMsg), ShouldEqual, 1)
			So(out, ShouldContainSubstring, "method="+requestMethodReserve)
			So(out, ShouldContainSubstring, "duration=")

			// the reply's real encoded size is what stands in for an allocation
			// delta, so it has to be the actual figure, not a placeholder.
			So(out, ShouldNotContainSubstring, "replyBytes=0")
		})

		Convey("handleRequest says nothing about the same stall at the shipped default", func() {
			server, token, clientID := slowRequestTestServer(ctx, "slow-request-quiet-default")
			slowRequestStallServer(server, slowRequestTestWait)

			logCtx, buf := captureLogCtx(ctx)
			slowRequestReserve(logCtx, server, token, clientID, 0)

			So(buf.String(), ShouldBeEmpty)
		})

		Convey("handleRequest says nothing about the same stall at a high threshold", func() {
			server, token, clientID := slowRequestTestServer(ctx, "slow-request-quiet-high")
			slowRequestThreshold = time.Hour

			slowRequestStallServer(server, slowRequestTestWait)

			logCtx, buf := captureLogCtx(ctx)
			slowRequestReserve(logCtx, server, token, clientID, 0)

			So(buf.String(), ShouldBeEmpty)
		})

		Convey("handleRequest says nothing about a reserve that ASKED to wait that long", func() {
			// a runner polling an empty queue with -r seconds is not the manager
			// being slow, and there is one such reserve per idle runner per poll.
			server, token, clientID := slowRequestTestServer(ctx, "slow-request-quiet-wait")
			slowRequestThreshold = slowRequestTestThreshold

			logCtx, buf := captureLogCtx(ctx)
			slowRequestReserve(logCtx, server, token, clientID, slowRequestTestWait)

			So(buf.String(), ShouldBeEmpty)
		})
	})
}

// slowRequestTestServer builds the smallest server that can serve a reserve
// request: no db, no scheduler, no limits, just a queue and a capture socket, so
// a reserve on an empty queue blocks for exactly its timeout and nothing else in
// the manager can interfere with the measurement.
func slowRequestTestServer(ctx context.Context, name string) (*Server, []byte, uuid.UUID) {
	ch := new(codec.BincHandle)
	token := bytes.Repeat([]byte("s"), tokenLength)
	server := &Server{
		ch:    ch,
		sock:  &captureSocket{ch: ch},
		token: token,
		q:     queue.New(ctx, name),
		up:    true,
	}

	clientID, err := uuid.NewV4()
	So(err, ShouldBeNil)

	return server, token, clientID
}

// slowRequestStallServer makes the server hold every reserve inside
// waitForPendingReserves (as a rac cycle does) for hold, then lets it through.
// That is MANAGER slowness with no client-requested wait at all, which is the
// only thing the warning is supposed to report.
func slowRequestStallServer(server *Server, hold time.Duration) {
	server.racPending = true

	go func() {
		time.Sleep(hold)
		server.finishRAC()
	}()
}

// captureLogCtx returns a context whose clog output is captured into the
// returned buffer, so a test can assert exactly what a code path logged - and,
// as importantly, that it logged nothing.
func captureLogCtx(ctx context.Context) (context.Context, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	handler := log15.StreamHandler(buf, log15.LogfmtFormat())

	return clog.ContextWithLogHandler(ctx, handler), buf
}

// slowRequestReserve makes the given server handle one reserve request that
// blocks for wait (the queue is empty), logging via ctx.
func slowRequestReserve(ctx context.Context, server *Server, token []byte,
	clientID uuid.UUID, wait time.Duration,
) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, server.ch)
	err := enc.Encode(&clientRequest{
		Method:   requestMethodReserve,
		Token:    token,
		ClientID: clientID,
		Timeout:  wait,
	})
	So(err, ShouldBeNil)

	So(server.handleRequest(ctx, &mangos.Message{Body: encoded}), ShouldBeNil)
}

// slowRequestTruncatedBody returns the first half of a validly encoded
// clientRequest, which the server's codec genuinely cannot decode - unlike
// arbitrary junk, which binc will happily decode into a partly-populated request
// that then fails token validation instead.
func slowRequestTruncatedBody(server *Server, token []byte, clientID uuid.UUID) []byte {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, server.ch)
	err := enc.Encode(&clientRequest{
		Method:   requestMethodReserve,
		Token:    token,
		ClientID: clientID,
	})
	So(err, ShouldBeNil)
	So(len(encoded), ShouldBeGreaterThan, 2)

	return encoded[:len(encoded)/2]
}
