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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kballard/go-shellquote"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
)

// startReportSocket wraps a captureSocket to disrupt the FIRST post-exec Started()
// report RPC (method "jstart"). With transient=true the first jstart's reply is a
// transport error ("receive time out"), exactly as a saturated server produces;
// with definitiveErr set the first jstart instead gets a normal reply carrying
// that server-side Err (e.g. ErrBadJob), i.e. a definitive rejection. Every other
// request behaves normally, so only the Started() report is disrupted - the
// command itself is healthy. It is a pure test seam living entirely in the socket
// the test injects.
type startReportSocket struct {
	*captureSocket

	transient     bool   // first jstart Recv returns a transport error
	definitiveErr string // first jstart Recv returns a serverResponse with this Err

	mu        sync.Mutex
	armed     bool
	firedOnce bool
	startSeen int
}

func (s *startReportSocket) Send(msg []byte) error {
	req := &clientRequest{}
	dec := codec.NewDecoderBytes(msg, s.ch)
	_ = dec.Decode(req) //nolint:errcheck // best-effort peek at the method

	s.mu.Lock()
	if req.Method == requestMethodStart {
		s.startSeen++

		if !s.firedOnce {
			s.armed = true
			s.firedOnce = true
		}
	}
	s.mu.Unlock()

	return s.captureSocket.Send(msg)
}

func (s *startReportSocket) Recv() ([]byte, error) {
	s.mu.Lock()
	fire := s.armed
	s.armed = false
	s.mu.Unlock()

	if !fire {
		return s.captureSocket.Recv()
	}

	if s.transient {
		// mimic the production error a saturated server's blocked reply produces.
		return nil, Error{requestMethodStart, "", "receive time out"}
	}

	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, s.ch)
	err := enc.Encode(&serverResponse{Err: s.definitiveErr})

	return encoded, err
}

func (s *startReportSocket) starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.startSeen
}

// newStartReportClient builds an in-process capture client (no real server) around
// the given startReportSocket, mirroring newLiveExecuteCaptureClient's timing
// overrides so Execute runs quickly.
func newStartReportClient(sock *startReportSocket, capture *liveTouchCapture) *Client {
	client, base := newCaptureClient()
	sock.captureSocket = base
	client.sock = sock
	client.touchInterval = liveExecuteTouchInterval
	client.retryWait = liveExecuteRetryWait
	client.retryTime = liveExecuteRetryTime
	client.percentMemoryKill = ClientPercentMemoryKill
	client.liveTouchHook = capture.record

	return client
}

// TestReliable4StartedReportFailure is the untagged behavioural test for reliable4
// issue #3: a transient failure of the post-exec Started() report must keep the
// healthy command running to completion, while a definitive server-side rejection
// must still kill it (avoiding a double-run).
func TestReliable4StartedReportFailure(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A transient Started() report failure keeps the healthy command running to completion", t, func() {
		capture := &liveTouchCapture{}
		sock := &startReportSocket{transient: true}
		client := newStartReportClient(sock, capture)
		cwd := liveExecuteCwd(t)
		marker := filepath.Join(cwd, "ran")
		cmd := "sleep 1; echo ran > " + shellquote.Join(marker)
		job := liveExecuteJob(client, cwd, cmd)

		execErr := client.Execute(ctx, job, "/bin/sh")

		_, statErr := os.Stat(marker)

		// sanity: the Started() report path really was exercised (and failed once,
		// then retried in the background).
		So(sock.starts(), ShouldBeGreaterThanOrEqualTo, 2)

		Convey("the command runs to completion, its side effect happens and Execute succeeds", func() {
			So(statErr, ShouldBeNil)
			So(execErr, ShouldBeNil)
		})
	})

	Convey("A transient Started() failure re-reports the start IMMEDIATELY, before a full retryWait", t, func() {
		// A fast command (no sleep) whose FIRST jstart fails transiently, with
		// retryWait set far LARGER than the command's lifetime. The server only
		// records StartTime on a successful Started(), and completion is rejected
		// while StartTime is zero, so a short command needs its start re-reported
		// promptly. Pre-fix the ticker fires first (after retryWait): it never ticks
		// within this command's life, so Execute finishes and closes stop having
		// re-sent nothing (only the initial, failed jstart is seen). Post-fix the
		// immediate first attempt re-sends right away, so a second jstart is seen
		// well within the 30s ticker window.
		capture := &liveTouchCapture{}
		sock := &startReportSocket{transient: true}
		client := newStartReportClient(sock, capture)
		client.retryWait = 30 * time.Second
		cwd := liveExecuteCwd(t)
		marker := filepath.Join(cwd, "ran")
		cmd := "echo ran > " + shellquote.Join(marker)
		job := liveExecuteJob(client, cwd, cmd)

		execErr := client.Execute(ctx, job, "/bin/sh")

		_, statErr := os.Stat(marker)

		// the immediate retry runs in a background goroutine; give it a brief moment
		// to send. Pre-fix nothing ever re-sends (the goroutine exits via stop), so
		// this simply exhausts the wait and the assertion below fails as intended.
		deadline := time.Now().Add(2 * time.Second)
		for sock.starts() < 2 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}

		Convey("a second start report is sent long before retryWait and the command completes", func() {
			So(sock.starts(), ShouldBeGreaterThanOrEqualTo, 2)
			So(statErr, ShouldBeNil)
			So(execErr, ShouldBeNil)
		})
	})

	Convey("A definitive Started() rejection still kills the command", t, func() {
		capture := &liveTouchCapture{}
		sock := &startReportSocket{definitiveErr: ErrBadJob}
		client := newStartReportClient(sock, capture)
		cwd := liveExecuteCwd(t)
		marker := filepath.Join(cwd, "ran")
		cmd := "sleep 1; echo ran > " + shellquote.Join(marker)
		job := liveExecuteJob(client, cwd, cmd)

		execErr := client.Execute(ctx, job, "/bin/sh")

		_, statErr := os.Stat(marker)

		So(sock.starts(), ShouldBeGreaterThanOrEqualTo, 1)

		Convey("the command is killed before its side effect and Execute reports the error", func() {
			So(os.IsNotExist(statErr), ShouldBeTrue)
			So(execErr, ShouldNotBeNil)
		})
	})
}
