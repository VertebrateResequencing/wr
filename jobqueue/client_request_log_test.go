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
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

const (
	clientRequestLogErrorLvl = "lvl=eror"
	clientRequestLogWarnLvl  = "lvl=warn"
	clientRequestLogDebugLvl = "lvl=dbug"
)

// TestClientRequestErrorLogLevel proves that the manager does not log routine
// client actions at error level, while still doing so for genuine failures.
func TestClientRequestErrorLogLevel(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a minimal server", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		server, token, clientID := slowRequestTestServer(ctx, "client-request-log")
		sock, ok := server.sock.(*captureSocket)
		So(ok, ShouldBeTrue)

		handleWithToken := func(cr *clientRequest, presented []byte) (string, error) {
			cr.Token = presented
			cr.ClientID = clientID

			var encoded []byte

			So(codec.NewEncoderBytes(&encoded, server.ch).Encode(cr), ShouldBeNil)

			herr := server.handleRequest(ctx, &mangos.Message{Body: encoded})
			So(herr, ShouldNotBeNil)

			logCtx, buf := captureLogCtx(ctx)
			logClientRequestError(logCtx, herr)

			return buf.String(), herr
		}

		handle := func(cr *clientRequest) (string, error) {
			return handleWithToken(cr, token)
		}

		Convey("an add of zero jobs is still refused, but not logged as an error", func() {
			// a caller's empty `var jobs []*Job` reaches the server as nil Jobs
			out, herr := handle(&clientRequest{
				Method: requestMethodAdd,
				Env:    []byte("environment"),
			})

			var jqErr Error

			So(errors.As(herr, &jqErr), ShouldBeTrue)
			So(jqErr, ShouldResemble, Error{Op: requestMethodAdd, Err: ErrBadRequest})
			So(sock.response().Err, ShouldEqual, ErrBadRequest)

			So(out, ShouldNotContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, clientRequestLogDebugLvl)
			So(out, ShouldContainSubstring, ErrBadRequest)
		})

		Convey("a request with the wrong token is refused, and logged as a warning rather than an error", func() {
			// a long-lived client's first request after a clean manager restart
			// presents the previous manager's token, and is resent with the new
			// one once the client has re-read its token file.
			wrong := make([]byte, len(token))
			copy(wrong, token)
			wrong[0]++

			out, herr := handleWithToken(&clientRequest{
				Method: requestMethodAdd,
				Env:    []byte("environment"),
				Jobs:   []*Job{{Cmd: "echo wrong token"}},
			}, wrong)

			var jqErr Error

			So(errors.As(herr, &jqErr), ShouldBeTrue)
			So(jqErr.Op, ShouldEqual, requestMethodAdd)
			So(sock.response().Err, ShouldEqual, ErrPermissionDenied)

			So(out, ShouldNotContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, clientRequestLogWarnLvl)
			So(out, ShouldContainSubstring, "wrong token")
		})

		Convey("a malformed add is still logged as an error", func() {
			out, herr := handle(&clientRequest{
				Method: requestMethodAdd,
				Env:    []byte("environment"),
				Jobs:   []*Job{{Cmd: "echo missing requirements"}},
			})

			So(herr.Error(), ShouldContainSubstring, ErrBadRequest)
			So(out, ShouldContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, ErrBadRequest)
		})

		Convey("an add of zero jobs without an environment is still logged as an error", func() {
			out, herr := handle(&clientRequest{Method: requestMethodAdd})

			So(herr.Error(), ShouldContainSubstring, ErrBadRequest)
			So(out, ShouldContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, ErrBadRequest)
		})

		Convey("a wait on a subscription that was closed under it is not logged as an error", func() {
			id, err := server.registerStatusSubscription()
			So(err, ShouldBeNil)

			// closing the subscription while it stays registered is the state a
			// long poll is in when the client's unsubscribe (or a reconnect's
			// replacement) closes it mid-wait.
			sub, exists := server.clientSubscription(id)
			So(exists, ShouldBeTrue)
			sub.close()

			out, herr := handle(&clientRequest{
				Method:         requestMethodWaitForUpdates,
				SubscriptionID: id,
				Timeout:        serverSubscriptionHoldTime,
			})

			So(herr.Error(), ShouldContainSubstring, errSubscriptionClosed.Error())
			So(sock.response().Err, ShouldEqual, ErrBadRequest)

			So(out, ShouldNotContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, clientRequestLogDebugLvl)
			So(out, ShouldContainSubstring, errSubscriptionClosed.Error())
		})

		Convey("a wait on an unknown subscription is still refused, but not logged as an error", func() {
			// a long-lived client's first poll after a manager restart carries
			// the previous manager's subscription id; the refusal makes the
			// client resubscribe.
			out, herr := handle(&clientRequest{
				Method:         requestMethodWaitForUpdates,
				SubscriptionID: "sub-unknown",
				Timeout:        serverSubscriptionHoldTime,
			})

			So(herr.Error(), ShouldContainSubstring, errUnknownSubscription.Error())
			So(sock.response().Err, ShouldEqual, ErrBadRequest)

			So(out, ShouldNotContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, clientRequestLogDebugLvl)
			So(out, ShouldContainSubstring, errUnknownSubscription.Error())
		})

		Convey("a wait without a subscription id is still logged as an error", func() {
			out, herr := handle(&clientRequest{Method: requestMethodWaitForUpdates})

			So(herr.Error(), ShouldContainSubstring, ErrBadRequest)
			So(out, ShouldContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, ErrBadRequest)
		})
	})
}

// touchLogRecord is the level, message and err of one log record.
type touchLogRecord struct {
	lvl log15.Lvl
	msg string
	err string
}

// TestTouchAfterRunEndedLogLevel proves that the manager does not log at error
// level a runner's touch that arrives after that runner reported how its job's
// run ended, as touches in flight across a manager restart do, while a touch
// for a job that does not exist, or that another runner ran, still is.
func TestTouchAfterRunEndedLogLevel(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a server and a runner that has reserved and started a job", t, func() {
		logs := &touchLogCapture{}
		clog.ToHandlerAtLevel(logs.handler(), "debug")

		defer clog.ToDefault()

		// the package's tests turn client error logging off; this one is about it
		ServerLogClientErrors = true

		defer func() { ServerLogClientErrors = false }()

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		connect := func() *Client {
			jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			return jq
		}

		jq := connect()
		defer disconnect(jq)

		_, _, err = jq.Add([]*Job{{
			Cmd: "echo touch after end", Cwd: testCwd, RepGroup: "touch_after_end",
			Requirements: standardReqs, Retries: 1,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)

		job, err := jq.Reserve(time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		key := job.Key()
		touchErr := "jtouch(" + key + ")"

		endState := func(exitcode int) *JobEndState {
			return &JobEndState{
				Cwd: testCwd, Exitcode: exitcode, PeakRAM: 1, CPUtime: time.Millisecond,
				EndTime: time.Now(), Exited: true,
			}
		}

		touchRefusedAs := func(c *Client) touchLogRecord {
			_, errt := c.touch(job, nil)
			So(errt, ShouldNotBeNil)
			So(errt.Error(), ShouldContainSubstring, ErrBadJob)

			return logs.waitForErr(touchErr)
		}

		Convey("its touch just after it archived the job is refused, but not logged as an error", func() {
			So(jq.Archive(job, endState(0)), ShouldBeNil)

			rec := touchRefusedAs(jq)
			So(rec.err, ShouldNotBeBlank)
			So(rec.lvl, ShouldEqual, log15.LvlDebug)
		})

		Convey("its touch just after it released the job is refused, but not logged as an error", func() {
			So(jq.Release(job, endState(3), FailReasonExit), ShouldBeNil)

			rec := touchRefusedAs(jq)
			So(rec.err, ShouldNotBeBlank)
			So(rec.lvl, ShouldEqual, log15.LvlDebug)
		})

		Convey("its touch after the manager gave the job up as lost and released it is still logged as an error", func() {
			// as confirm-dead does once it finds a lost job's runner gone
			item, errg := server.q.Get(key)
			So(errg, ShouldBeNil)

			sjob, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			sjob.Lock()
			sjob.FailReason = FailReasonLost
			sjob.Unlock()

			So(server.q.Release(ctx, key), ShouldBeNil)

			rec := touchRefusedAs(jq)
			So(rec.err, ShouldNotBeBlank)
			So(rec.lvl, ShouldEqual, log15.LvlError)
		})

		Convey("another runner's touch of the job it archived is still logged as an error", func() {
			So(jq.Archive(job, endState(0)), ShouldBeNil)

			other := connect()
			defer disconnect(other)

			rec := touchRefusedAs(other)
			So(rec.err, ShouldNotBeBlank)
			So(rec.lvl, ShouldEqual, log15.LvlError)
		})

		Convey("another runner's touch of the job it released is still logged as an error", func() {
			So(jq.Release(job, endState(3), FailReasonExit), ShouldBeNil)

			other := connect()
			defer disconnect(other)

			rec := touchRefusedAs(other)
			So(rec.err, ShouldNotBeBlank)
			So(rec.lvl, ShouldEqual, log15.LvlError)
		})

		Convey("a touch of a job that was never added is still logged as an error", func() {
			unknown := &Job{Cmd: "echo never added", Cwd: testCwd}
			_, errt := jq.touch(unknown, nil)
			So(errt, ShouldNotBeNil)
			So(errt.Error(), ShouldContainSubstring, ErrBadJob)

			rec := logs.waitForErr("jtouch(" + unknown.Key() + ")")
			So(rec.err, ShouldNotBeBlank)
			So(rec.lvl, ShouldEqual, log15.LvlError)
		})
	})
}

// touchLogCapture collects log records, safe for the server's goroutines to
// write while a test reads.
type touchLogCapture struct {
	mu      sync.Mutex
	records []touchLogRecord
}

// handler returns a log15 handler that records into c.
func (c *touchLogCapture) handler() log15.Handler {
	return log15.FuncHandler(func(r log15.Record) error {
		rec := touchLogRecord{lvl: r.Lvl, msg: r.Msg}

		for i := 0; i+1 < len(r.Ctx); i += 2 {
			if r.Ctx[i] == "err" {
				rec.err = fmt.Sprint(r.Ctx[i+1])
			}
		}

		c.mu.Lock()
		c.records = append(c.records, rec)
		c.mu.Unlock()

		return nil
	})
}

// waitForErr waits up to 5s for a record whose err contains substr, returning
// it, or a zero record if none came.
func (c *touchLogCapture) waitForErr(substr string) touchLogRecord {
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		c.mu.Lock()

		for _, rec := range c.records {
			if strings.Contains(rec.err, substr) {
				c.mu.Unlock()

				return rec
			}
		}

		c.mu.Unlock()

		<-time.After(10 * time.Millisecond)
	}

	return touchLogRecord{}
}
