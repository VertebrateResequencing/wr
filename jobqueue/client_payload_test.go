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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

var errCaptureSocketUnsupported = errors.New("unsupported capture socket operation")

type captureSocket struct {
	ch   codec.Handle
	sent []byte
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

	return encoded, enc.Encode(&serverResponse{})
}

func (s *captureSocket) SendMsg(_ *mangos.Message) error {
	return errCaptureSocketUnsupported
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

func TestClientLifecycleRequestsTrimJobPayload(t *testing.T) {
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
