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

	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

const (
	clientRequestLogErrorLvl = "lvl=eror"
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

		handle := func(cr *clientRequest) (string, error) {
			cr.Token = token
			cr.ClientID = clientID

			var encoded []byte

			So(codec.NewEncoderBytes(&encoded, server.ch).Encode(cr), ShouldBeNil)

			herr := server.handleRequest(ctx, &mangos.Message{Body: encoded})
			So(herr, ShouldNotBeNil)

			logCtx, buf := captureLogCtx(ctx)
			logClientRequestError(logCtx, herr)

			return buf.String(), herr
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

		Convey("a wait on an unknown subscription is still logged as an error", func() {
			out, herr := handle(&clientRequest{
				Method:         requestMethodWaitForUpdates,
				SubscriptionID: "sub-unknown",
				Timeout:        serverSubscriptionHoldTime,
			})

			So(herr.Error(), ShouldContainSubstring, errUnknownSubscription.Error())
			So(out, ShouldContainSubstring, clientRequestLogErrorLvl)
		})

		Convey("a wait without a subscription id is still logged as an error", func() {
			out, herr := handle(&clientRequest{Method: requestMethodWaitForUpdates})

			So(herr.Error(), ShouldContainSubstring, ErrBadRequest)
			So(out, ShouldContainSubstring, clientRequestLogErrorLvl)
			So(out, ShouldContainSubstring, ErrBadRequest)
		})
	})
}
