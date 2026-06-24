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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	"github.com/kballard/go-shellquote"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

var errCaptureSocketUnsupported = errors.New("unsupported capture socket operation")

const (
	liveExecuteTouchInterval = 100 * time.Millisecond
	liveExecuteRetryWait     = 10 * time.Millisecond
	liveExecuteRetryTime     = time.Second
	liveExecuteTimeLimit     = 10 * time.Second
	liveExecuteOutputSize    = 128 * 1024
	liveExecuteFileMode      = 0o600
	liveExecuteDirMode       = 0o755
)

type captureSocket struct {
	ch      codec.Handle
	sent    []byte
	sentMsg []byte
}

func newCaptureClient() (*Client, *captureSocket) {
	ch := new(codec.BincHandle)
	sock := &captureSocket{ch: ch}
	id, err := uuid.NewV4()
	So(err, ShouldBeNil)

	return &Client{ch: ch, clientid: id, sock: sock}, sock
}

func (s *captureSocket) Info() mangos.ProtocolInfo {
	return mangos.ProtocolInfo{}
}

func (s *captureSocket) Close() error {
	return nil
}

func (s *captureSocket) Send(msg []byte) error {
	s.sent = append([]byte(nil), msg...)

	return nil
}

func (s *captureSocket) Recv() ([]byte, error) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, s.ch)
	err := enc.Encode(&serverResponse{})

	return encoded, err
}

func (s *captureSocket) SendMsg(msg *mangos.Message) error {
	s.sentMsg = append([]byte(nil), msg.Body...)

	return nil
}

func (s *captureSocket) RecvMsg() (*mangos.Message, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) Dial(_ string) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) DialOptions(_ string, _ map[string]interface{}) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) NewDialer(_ string, _ map[string]interface{}) (mangos.Dialer, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) Listen(_ string) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) ListenOptions(_ string, _ map[string]interface{}) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) NewListener(_ string, _ map[string]interface{}) (mangos.Listener, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) GetOption(_ string) (interface{}, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) SetOption(_ string, _ interface{}) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) OpenContext() (mangos.Context, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) SetPipeEventHook(_ mangos.PipeEventHook) mangos.PipeEventHook {
	return nil
}

func (s *captureSocket) request() *clientRequest {
	req := &clientRequest{}
	dec := codec.NewDecoderBytes(s.sent, s.ch)
	err := dec.Decode(req)
	So(err, ShouldBeNil)

	return req
}

func (s *captureSocket) response() *serverResponse {
	resp := &serverResponse{}
	dec := codec.NewDecoderBytes(s.sentMsg, s.ch)
	err := dec.Decode(resp)
	So(err, ShouldBeNil)

	return resp
}

func TestClientLifecycleRequestsTrimJobPayload(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Lifecycle methods send only keys and required state, not whole jobs", t, func() {
		client, sock := newCaptureClient()
		job := newLargePayloadJob(client.clientid)
		key := job.Key()
		endState := &JobEndState{
			Cwd:      "/work/actual",
			Exitcode: 0,
			PeakRAM:  123,
			PeakDisk: 456,
			CPUtime:  7 * time.Second,
			EndTime:  time.Now(),
			Stdout:   []byte("stdout"),
			Stderr:   []byte("stderr"),
			Exited:   true,
		}

		So(client.Archive(job, endState), ShouldBeNil)
		assertTrimmedLifecycleRequest(sock.request(), "jarchive", key, "", endState)

		client, sock = newCaptureClient()
		job = newLargePayloadJob(client.clientid)
		So(client.Release(job, endState, "temporary"), ShouldBeNil)
		assertTrimmedLifecycleRequest(sock.request(), "jrelease", job.Key(), "temporary", endState)

		client, sock = newCaptureClient()
		job = newLargePayloadJob(client.clientid)
		So(client.Bury(job, endState, "permanent"), ShouldBeNil)
		assertTrimmedLifecycleRequest(sock.request(), "jbury", job.Key(), "permanent", endState)

		client, sock = newCaptureClient()
		job = newLargePayloadJob(client.clientid)
		killCalled, err := client.Touch(job)
		So(err, ShouldBeNil)
		So(killCalled, ShouldBeFalse)
		assertTrimmedLifecycleRequest(sock.request(), "jtouch", job.Key(), "", touchEndState(job))
	})
}

func TestClientTouchSendsLiveEndState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Touch sends a key-only jtouch request with current live end state fields", t, func() {
		client, sock := newCaptureClient()
		stdout := compressStd([]byte("out\n"))
		stderr := compressStd([]byte("err\n"))
		job := &Job{
			Cmd:        "echo live",
			PeakRAM:    123,
			PeakDisk:   456,
			CPUtime:    7 * time.Second,
			StdOutC:    stdout,
			StdErrC:    stderr,
			ReservedBy: client.clientid,
		}
		key := job.Key()

		killCalled, err := client.Touch(job)
		So(err, ShouldBeNil)
		So(killCalled, ShouldBeFalse)

		req := sock.request()
		So(req.Method, ShouldEqual, "jtouch")
		So(req.Keys, ShouldResemble, []string{key})
		So(req.Job, ShouldBeNil)
		So(req.JobEndState, ShouldNotBeNil)
		So(req.JobEndState.PeakRAM, ShouldEqual, 123)
		So(req.JobEndState.PeakDisk, ShouldEqual, int64(456))
		So(req.JobEndState.CPUtime, ShouldEqual, 7*time.Second)
		So(req.JobEndState.Stdout, ShouldResemble, stdout)
		So(req.JobEndState.Stderr, ShouldResemble, stderr)
		So(req.JobEndState.Cwd, ShouldEqual, "")
		So(req.JobEndState.Exited, ShouldBeFalse)
	})
}

func TestServerRejectsKeyOnlyStartedRequest(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A malformed key-only jstart request is rejected instead of panicking", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch := new(codec.BincHandle)
		token := bytes.Repeat([]byte("x"), tokenLength)
		sock := &captureSocket{ch: ch}
		server := &Server{
			ch:    ch,
			sock:  sock,
			token: token,
			q:     queue.New(ctx, "payload-trim-start"),
			up:    true,
		}
		clientID, err := uuid.NewV4()
		So(err, ShouldBeNil)

		job := &Job{Cmd: "echo key-only-start", ReservedBy: clientID}
		key := job.Key()
		_, err = server.q.Add(ctx, key, "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)

		var encoded []byte

		enc := codec.NewEncoderBytes(&encoded, ch)
		err = enc.Encode(&clientRequest{
			Method:   requestMethodStart,
			Token:    token,
			ClientID: clientID,
			Keys:     []string{key},
		})
		So(err, ShouldBeNil)

		err = server.handleRequest(ctx, &mangos.Message{Body: encoded})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, ErrBadRequest)
		So(sock.response().Err, ShouldEqual, ErrBadRequest)
	})
}

func newLargePayloadJob(clientID uuid.UUID) *Job {
	return &Job{
		Cmd:         strings.Repeat("echo payload ", 100),
		Cwd:         "/work",
		RepGroup:    "payload-trim",
		Behaviours:  Behaviours{{When: OnSuccess, Do: Run, Arg: strings.Repeat("touch marker ", 100)}},
		EnvC:        []byte(strings.Repeat("ENV=value", 100)),
		StdOutC:     []byte(strings.Repeat("stdout", 100)),
		StdErrC:     []byte(strings.Repeat("stderr", 100)),
		PeakRAM:     11,
		PeakDisk:    22,
		CPUtime:     33 * time.Second,
		UntilBuried: 2,
		ReservedBy:  clientID,
	}
}

func assertTrimmedLifecycleRequest(req *clientRequest, method, key, failReason string, endState *JobEndState) {
	So(req.Method, ShouldEqual, method)
	So(req.Keys, ShouldResemble, []string{key})
	So(req.Job, ShouldBeNil)
	So(req.FailReason, ShouldEqual, failReason)

	if endState == nil {
		So(req.JobEndState, ShouldBeNil)

		return
	}

	So(req.JobEndState, ShouldNotBeNil)
	So(req.JobEndState.PeakRAM, ShouldEqual, endState.PeakRAM)
	So(req.JobEndState.PeakDisk, ShouldEqual, endState.PeakDisk)
	So(req.JobEndState.CPUtime, ShouldEqual, endState.CPUtime)
	So(req.JobEndState.Stdout, ShouldResemble, endState.Stdout)
	So(req.JobEndState.Stderr, ShouldResemble, endState.Stderr)
}

func newLiveExecuteCaptureClient(capture *liveTouchCapture) *Client {
	client, _ := newCaptureClient()
	client.touchInterval = liveExecuteTouchInterval
	client.retryWait = liveExecuteRetryWait
	client.retryTime = liveExecuteRetryTime
	client.percentMemoryKill = ClientPercentMemoryKill
	client.liveTouchHook = capture.record

	return client
}

type liveTouchCapture struct {
	sync.Mutex
	states []*JobEndState
}

func (c *liveTouchCapture) record(state *JobEndState) {
	c.Lock()
	defer c.Unlock()

	c.states = append(c.states, cloneTestJobEndState(state))
}

func (c *liveTouchCapture) matching(match func(*JobEndState) bool) []*JobEndState {
	c.Lock()
	defer c.Unlock()

	var states []*JobEndState
	for _, state := range c.states {
		if match(state) {
			states = append(states, cloneTestJobEndState(state))
		}
	}

	return states
}

func (c *liveTouchCapture) firstStdoutWithMarker(marker string) (*JobEndState, string) {
	c.Lock()
	defer c.Unlock()

	for _, state := range c.states {
		stdout := decompressLiveTouch(state.Stdout)
		if strings.Contains(stdout, marker) {
			return cloneTestJobEndState(state), stdout
		}
	}

	return nil, ""
}

func decompressLiveTouch(compressed []byte) string {
	if len(compressed) == 0 {
		return ""
	}

	decompressed, err := decompress(compressed)
	if err != nil {
		return ""
	}

	return string(decompressed)
}

func cloneTestJobEndState(state *JobEndState) *JobEndState {
	if state == nil {
		return nil
	}

	clone := *state
	clone.Stdout = append([]byte(nil), state.Stdout...)
	clone.Stderr = append([]byte(nil), state.Stderr...)

	return &clone
}

func (c *liveTouchCapture) firstStderrWithMarker(marker string) (*JobEndState, string) {
	c.Lock()
	defer c.Unlock()

	for _, state := range c.states {
		stderr := decompressLiveTouch(state.Stderr)
		if strings.Contains(stderr, marker) {
			return cloneTestJobEndState(state), stderr
		}
	}

	return nil, ""
}

func TestClientExecuteLiveTouchPayloads(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Execute sends stdout tails once per touch from the actual cwd", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		job := liveExecuteJob(client, cwd, "printf 'alpha\\n'; sleep 0.6; printf 'beta\\n'; sleep 0.6")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

		states := capture.matching(func(state *JobEndState) bool {
			return len(state.Stdout) != 0
		})
		So(len(states), ShouldBeGreaterThanOrEqualTo, 2)
		So(decompressLiveTouch(states[0].Stdout), ShouldEqual, "alpha\n")
		So(states[0].Cwd, ShouldEqual, cwd)
		So(decompressLiveTouch(states[1].Stdout), ShouldEqual, "beta\n")
		So(states[1].Cwd, ShouldEqual, cwd)
	})

	Convey("Execute sends stderr tails once per touch", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		job := liveExecuteJob(client, cwd, "printf 'err-alpha\\n' >&2; sleep 0.6; printf 'err-beta\\n' >&2; sleep 0.6")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

		states := capture.matching(func(state *JobEndState) bool {
			return len(state.Stderr) != 0
		})
		So(len(states), ShouldBeGreaterThanOrEqualTo, 2)
		So(decompressLiveTouch(states[0].Stderr), ShouldEqual, "err-alpha\n")
		So(decompressLiveTouch(states[1].Stderr), ShouldEqual, "err-beta\n")
	})

	if _, err := exec.LookPath("python3"); err != nil {
		SkipConvey("Execute sends cumulative CPU time and observed peak RAM", t, func() {})
	} else {
		Convey("Execute sends cumulative CPU time and observed peak RAM", t, func() {
			capture := &liveTouchCapture{}
			client := newLiveExecuteCaptureClient(capture)
			cwd := liveExecuteCwd(t)
			job := liveExecuteJob(client, cwd, strings.Join([]string{
				"python3 - <<'PY'",
				"import time",
				"x = bytearray(2 * 1024 * 1024)",
				"end = time.time() + 3",
				"while time.time() < end:",
				"    x[0] = (x[0] + 1) % 256",
				"PY",
			}, "\n"))

			So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

			states := capture.matching(func(state *JobEndState) bool {
				return state.CPUtime >= time.Millisecond && state.PeakRAM >= 1
			})
			So(len(states), ShouldBeGreaterThanOrEqualTo, 1)
		})
	}

	Convey("Execute bounds live output tails without truncating final archives", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		stdoutStream := liveMarkedStream("OUT-OLD\n", "OUT-NEW\n")
		stderrStream := liveMarkedStream("ERR-OLD\n", "ERR-NEW\n")
		stdoutFile := filepath.Join(cwd, "stdout.bin")
		stderrFile := filepath.Join(cwd, "stderr.bin")

		So(os.WriteFile(stdoutFile, stdoutStream, liveExecuteFileMode), ShouldBeNil)
		So(os.WriteFile(stderrFile, stderrStream, liveExecuteFileMode), ShouldBeNil)

		cmd := fmt.Sprintf(
			"cat %s; cat %s >&2; sleep 2",
			shellquote.Join(stdoutFile),
			shellquote.Join(stderrFile),
		)
		job := liveExecuteJob(client, cwd, cmd)

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

		stdoutState, liveStdout := capture.firstStdoutWithMarker("OUT-NEW\n")
		stderrState, liveStderr := capture.firstStderrWithMarker("ERR-NEW\n")

		So(stdoutState, ShouldNotBeNil)
		So(stderrState, ShouldNotBeNil)
		So(stdoutState.Stdout, ShouldNotBeNil)
		So(stderrState.Stderr, ShouldNotBeNil)
		So(len(stdoutState.Stdout), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)
		So(len(stderrState.Stderr), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)
		So(liveStdout, ShouldContainSubstring, "OUT-NEW\n")
		So(liveStdout, ShouldNotContainSubstring, "OUT-OLD\n")
		So(liveStderr, ShouldContainSubstring, "ERR-NEW\n")
		So(liveStderr, ShouldNotContainSubstring, "ERR-OLD\n")

		finalStdout, err := job.StdOut()
		So(err, ShouldBeNil)
		So(finalStdout, ShouldEqual, expectedPrefixSuffixOutput(stdoutStream))

		finalStderr, err := job.StdErr()
		So(err, ShouldBeNil)
		So(finalStderr, ShouldEqual, expectedPrefixSuffixOutput(stderrStream))
	})
}

func liveExecuteCwd(t *testing.T) string {
	t.Helper()

	cwd := filepath.Join(t.TempDir(), "job1")
	So(os.MkdirAll(cwd, liveExecuteDirMode), ShouldBeNil)

	return cwd
}

func liveExecuteJob(client *Client, cwd string, cmd string) *Job {
	return &Job{
		Cmd:        cmd,
		Cwd:        cwd,
		CwdMatters: true,
		ReqGroup:   "live-execute",
		Requirements: &scheduler.Requirements{
			RAM:  4096,
			Time: liveExecuteTimeLimit,
		},
		ReservedBy: client.clientid,
	}
}

func liveMarkedStream(prefix, suffix string) []byte {
	stream := append([]byte(prefix), deterministicLiveASCII(liveExecuteOutputSize)...)
	stream = append(stream, suffix...)

	return stream
}

func deterministicLiveASCII(size int) []byte {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	data := deterministicLiveBytes(size)
	for i := range data {
		data[i] = alphabet[int(data[i])%len(alphabet)]
	}

	return data
}

func expectedPrefixSuffixOutput(data []byte) string {
	saver := &prefixSuffixSaver{N: 4096}
	_, err := saver.Write(data)
	So(err, ShouldBeNil)

	return string(bytes.TrimSpace(saver.Bytes()))
}
