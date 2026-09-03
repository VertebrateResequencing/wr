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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/muxfys/v5"
	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	"github.com/kballard/go-shellquote"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
	"go.nanomsg.org/mangos/v3"
)

var errCaptureSocketUnsupported = errors.New("unsupported capture socket operation")

const (
	liveExecuteTouchInterval      = 100 * time.Millisecond
	liveExecuteRetryWait          = 10 * time.Millisecond
	liveExecuteRetryTime          = time.Second
	liveExecuteTimeLimit          = 10 * time.Second
	liveExecuteOutputSize         = 128 * 1024
	liveExecuteMarkerWaitAttempts = 300
	liveExecuteFileMode           = 0o600
	liveExecuteDirMode            = 0o755
)

func TestExecuteLiveStateSnapshots(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Live execute snapshots keep cwd and resources on repeated touches", t, func() {
		state := newExecuteLiveState("/work/actual", &liveTailSaver{}, &liveTailSaver{})
		state.updateResources(123, 456, 7*time.Second)

		first := state.snapshot()
		second := state.snapshot()

		So(first.Cwd, ShouldEqual, "/work/actual")
		So(first.PeakRAM, ShouldEqual, 123)
		So(first.PeakDisk, ShouldEqual, int64(456))
		So(first.CPUtime, ShouldEqual, 7*time.Second)
		So(second.Cwd, ShouldEqual, "/work/actual")
		So(second.PeakRAM, ShouldEqual, 123)
		So(second.PeakDisk, ShouldEqual, int64(456))
		So(second.CPUtime, ShouldEqual, 7*time.Second)
	})

	Convey("Live execute snapshots of a cwd_matters job carry no cwd", t, func() {
		state := newExecuteLiveState("", &liveTailSaver{}, &liveTailSaver{})
		state.updateResources(123, 456, 7*time.Second)

		snapshot := state.snapshot()

		So(snapshot.Cwd, ShouldBeBlank)
		So(snapshot.PeakRAM, ShouldEqual, 123)
	})

	Convey("Live execute resource snapshots keep maximum observed values", t, func() {
		state := newExecuteLiveState("/work/actual", &liveTailSaver{}, &liveTailSaver{})
		state.updateResources(123, 456, 7*time.Second)
		state.updateResources(111, 400, 6*time.Second)

		snapshot := state.snapshot()
		So(snapshot.PeakRAM, ShouldEqual, 123)
		So(snapshot.PeakDisk, ShouldEqual, int64(456))
		So(snapshot.CPUtime, ShouldEqual, 7*time.Second)

		state.updateResources(124, 457, 8*time.Second)
		snapshot = state.snapshot()
		So(snapshot.PeakRAM, ShouldEqual, 124)
		So(snapshot.PeakDisk, ShouldEqual, int64(457))
		So(snapshot.CPUtime, ShouldEqual, 8*time.Second)
	})
}

// TestUnstartedRunWorkSpaceCleanupOrder pins the order the unstarted-run cleanup
// does its two jobs in, which is the one thing about it that cannot be got wrong
// safely: a mount point of a job with no relative Mount IS its working directory,
// so deleting before unmounting would delete the user's remote data.
func TestUnstartedRunWorkSpaceCleanupOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a directory a job's workspace can be made in", t, func() {
		cwd := t.TempDir()

		// the Job's mounted filesystems are what unmountAll clears, so how many
		// are left when the deletion proves what it may delete says which of the
		// two happened first. -1 means the deletion never got that far.
		mountedWhenProven := -1

		Reset(func() { cleanupProvenHook = nil })

		Convey("The cleanup unmounts before it deletes anything", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)

			fs, err := muxfys.New(&muxfys.Config{Mount: filepath.Join(t.TempDir(), testWSMount)})
			So(err, ShouldBeNil)

			job.mountedFS = append(job.mountedFS, fs)
			cleanupProvenHook = func() { mountedWhenProven = len(job.mountedFS) }

			cleanupUnstartedWorkSpace(context.Background(), job)

			So(mountedWhenProven, ShouldEqual, 0)
			soPathsGone(actualCwd, tmpDir, workSpace, filepath.Join(cwd, AppName+createdCwdBaseSuffix))
			soPathsExist(cwd)
		})

		Convey("The cleanup deletes nothing at all when the unmount fails", func() {
			// a mount point that is not a real dir makes Unmount's own empty-dir
			// tidy-up refuse, which is the cheapest failed unmount to force; what
			// matters is that ANY of them stops the deletion.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, MountConfigs: MountConfigs{{Mount: testWSMount}}}
			actualCwd, workSpace, tmpDir := realWorkSpace(job)
			cleanupProvenHook = func() { mountedWhenProven = len(job.mountedFS) }

			So(os.WriteFile(filepath.Join(actualCwd, testWSMount), []byte("x"), liveExecuteFileMode), ShouldBeNil)

			cleanupUnstartedWorkSpace(context.Background(), job)

			So(mountedWhenProven, ShouldEqual, -1)
			soPathsExist(actualCwd, tmpDir, workSpace, cwd)
		})
	})
}

// TestJobTmpDirRemoval covers how Execute reclaims the TMPDIR it gave a run. It
// has to be removed on every exit, including for the great majority of Jobs that
// have no cleanup Behaviour and so keep the workspace it sits in, and it must be
// removed by descending into the workspace wr proved is its own: the path a Job
// was given for TMPDIR is a string whose every component above tmp lies in the
// tree the Job's own Cmd writes to for the whole run, so re-resolving it at
// deletion time lets a component swapped for a symlink aim the deletion outside
// the Job's Cwd entirely.
func TestJobTmpDirRemoval(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a job wr made a workspace for", t, func() {
		base := t.TempDir()
		cwd := filepath.Join(base, "cwd")
		So(os.MkdirAll(cwd, os.ModePerm), ShouldBeNil)

		job := &Job{Cwd: cwd, Cmd: testWSCmd}
		actualCwd, workSpace, tmpDir := realWorkSpace(job)

		buff := clog.ToBufferAtLevel("warn")

		Reset(clog.ToDefault)

		Convey("its tmp dir is removed, and nothing else is", func() {
			output := writeFileIn(actualCwd, "out.txt")

			removeJobTmpDir(context.Background(), job)

			soPathsGone(tmpDir)
			soPathsExist(output, actualCwd, workSpace, cwd)
			So(buff.String(), ShouldBeEmpty)
		})

		Convey("and the job's own Cmd cannot redirect that removal out of its Cwd", func() {
			victim := filepath.Join(base, "precious")
			victimTmp := filepath.Join(victim, createdTmpName)
			victimFile := writeFileIn(victimTmp, "irreplaceable.txt")

			// the Job's Cmd, run the way Execute runs it: in the working
			// directory wr made, with TMPDIR naming the tmp dir beside it. It
			// puts a symlink to the victim where its workspace was.
			cmd := exec.CommandContext(context.Background(), "/bin/bash", "-c", `
				set -e
				cd "$TMPDIR/../.."
				leaf=$(basename "$(dirname "$TMPDIR")")
				mv "$leaf" "$leaf.moved"
				ln -s "$VICTIM" "$leaf"
			`)
			cmd.Dir = actualCwd

			cmd.Env = append(os.Environ(), "TMPDIR="+tmpDir, "VICTIM="+victim)

			out, err := cmd.CombinedOutput()
			So(string(out), ShouldBeEmpty)
			So(err, ShouldBeNil)

			removeJobTmpDir(context.Background(), job)

			// what survived comes first: it is the whole point of the test, and
			// the default FailureHalts would hide it behind the warning below.
			soPathsExist(victimFile, victimTmp, victim)
			So(buff.String(), ShouldContainSubstring, "could not remove the job's tmp dir")
		})

		Convey("and a nested job's workspace inside it is left alone", func() {
			// a Job's script can `cd $TMPDIR` before it adds jobs, and wr then
			// builds the children's workspaces inside the very directory this
			// reclaims, while they are still running in them.
			scratch := writeFileIn(tmpDir, "scratch.txt")

			child := &Job{Cwd: tmpDir, Cmd: "still running child"}
			childCwd, childWorkSpace, childTmp := realWorkSpace(child)
			childOutput := writeFileIn(childCwd, "child_output.txt")

			removeJobTmpDir(context.Background(), job)

			soPathsExist(childOutput, childCwd, childTmp, childWorkSpace, tmpDir)
			soPathsGone(scratch)
			So(buff.String(), ShouldBeEmpty)
		})

		Convey("and a tmp dir one of the job's mounts caches in is left alone", func() {
			// deleting it could delete a writable mount's output before Unmount
			// has uploaded it, so it is kept unconditionally, exactly as the
			// cleanup Behaviour keeps it; see keptDirs. The MountConfigs go in
			// before the workspace is made because they feed Job.Key(), and the
			// key is what proves the workspace is this Job's.
			cacher := &Job{
				Cwd: cwd, Cmd: testWSCmd,
				MountConfigs: MountConfigs{{Targets: []MountTarget{{CacheDir: createdTmpName}}}},
			}
			cacherCwd, cacherWorkSpace, cacherTmp := realWorkSpace(cacher)

			removeJobTmpDir(context.Background(), cacher)

			soPathsExist(cacherTmp, cacherCwd, cacherWorkSpace, cwd)
			So(buff.String(), ShouldBeEmpty)
		})
	})
}

type captureSocket struct {
	ch        codec.Handle
	sent      []byte
	sentMsg   []byte
	server    *Server
	serverErr error
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
	if s.server != nil {
		s.serverErr = s.server.handleRequest(context.Background(), &mangos.Message{Body: s.sent})
	}

	return nil
}

func (s *captureSocket) Recv() ([]byte, error) {
	if s.sentMsg != nil {
		response := slices.Clone(s.sentMsg)
		s.sentMsg = nil

		return response, nil
	}

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

func (s *captureSocket) DialOptions(_ string, _ map[string]any) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) NewDialer(_ string, _ map[string]any) (mangos.Dialer, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) Listen(_ string) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) ListenOptions(_ string, _ map[string]any) error {
	return errCaptureSocketUnsupported
}

func (s *captureSocket) NewListener(_ string, _ map[string]any) (mangos.Listener, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) GetOption(_ string) (any, error) {
	return nil, errCaptureSocketUnsupported
}

func (s *captureSocket) SetOption(_ string, _ any) error {
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

func TestClientSuspendResumeRequests(t *testing.T) {
	Convey("Suspend and Resume send typed methods with JobEssence keys", t, func() {
		jes := []*JobEssence{
			{Cmd: "echo suspend by essence"},
			{JobKey: "already-known-key"},
		}
		expectedKeys := []string{jes[0].Key(), jes[1].Key()}

		client, sock := newCaptureClient()
		suspended, err := client.Suspend(jes)
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 0)

		req := sock.request()
		So(req.Method, ShouldEqual, "jsuspend")
		So(req.Keys, ShouldResemble, expectedKeys)
		So(req.Job, ShouldBeNil)

		client, sock = newCaptureClient()
		resumed, err := client.Resume(jes)
		So(err, ShouldBeNil)
		So(resumed, ShouldEqual, 0)

		req = sock.request()
		So(req.Method, ShouldEqual, "jresume")
		So(req.Keys, ShouldResemble, expectedKeys)
		So(req.Job, ShouldBeNil)
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

func TestServerRejectsAddWithoutRequirements(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("An add request without requirements is rejected instead of panicking", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch := new(codec.BincHandle)
		token := bytes.Repeat([]byte("x"), tokenLength)
		sock := &captureSocket{ch: ch}
		server := &Server{
			ch:    ch,
			sock:  sock,
			token: token,
			q:     queue.New(ctx, "payload-missing-requirements"),
			up:    true,
		}

		var encoded []byte

		enc := codec.NewEncoderBytes(&encoded, ch)
		err := enc.Encode(&clientRequest{
			Method: requestMethodAdd,
			Token:  token,
			Env:    []byte("environment"),
			Jobs:   []*Job{{Cmd: "echo missing requirements"}},
		})
		So(err, ShouldBeNil)

		err = server.handleRequest(ctx, &mangos.Message{Body: encoded})
		So(err, ShouldNotBeNil)

		var jqErr Error

		So(errors.As(err, &jqErr), ShouldBeTrue)
		So(jqErr, ShouldResemble, Error{Op: requestMethodAdd, Err: ErrBadRequest})
		So(sock.response().Err, ShouldEqual, ErrBadRequest)
	})
}

func TestServerRejectsAddWithNilJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A mixed add request containing a nil job is rejected before anything is queued", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch := new(codec.BincHandle)
		token := bytes.Repeat([]byte("x"), tokenLength)
		sock := &captureSocket{ch: ch}
		server := &Server{
			ch:    ch,
			sock:  sock,
			token: token,
			q:     queue.New(ctx, "payload-nil-job"),
			up:    true,
		}
		valid := &Job{
			Cmd:          "echo valid job",
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
		}

		var encoded []byte

		enc := codec.NewEncoderBytes(&encoded, ch)
		err := enc.Encode(&clientRequest{
			Method: requestMethodAdd,
			Token:  token,
			Env:    []byte("environment"),
			Jobs:   []*Job{valid, nil},
		})
		So(err, ShouldBeNil)

		err = server.handleRequest(ctx, &mangos.Message{Body: encoded})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "job at index 1 is nil")
		So(sock.response().Err, ShouldEqual, ErrBadRequest)
		So(server.q.Stats().Items, ShouldEqual, 0)
		So(valid.EnvKey, ShouldBeBlank)
	})
}

func TestServerRejectsAddWithNilDependency(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Every jobqueue add API gets a bad request for a nil dependency at the server boundary", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tmpDir := t.TempDir()
		testDB, _, err := initDB(ctx, filepath.Join(tmpDir, "queue.db"),
			filepath.Join(tmpDir, "queue.db.bak"), internal.Development, false, false)

		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		ch := new(codec.BincHandle)
		token := bytes.Repeat([]byte("x"), tokenLength)
		sock := &captureSocket{ch: ch}
		server := &Server{
			ch:        ch,
			sock:      sock,
			token:     token,
			db:        testDB,
			q:         queue.New(ctx, "payload-nil-dependency"),
			rpl:       newRGToKeys(),
			depGroups: newDepGroupMembers(),
			up:        true,
		}
		sock.server = server

		id, err := uuid.NewV4()
		So(err, ShouldBeNil)

		client := &Client{ch: ch, clientid: id, sock: sock, token: token}
		valid := &Job{
			Cmd:          "echo valid job",
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
		}
		malformed := &Job{
			Cmd:          "echo malformed job",
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
			Dependencies: Dependencies{&Dependency{}, nil},
		}
		jobs := []*Job{valid, malformed}
		env := []string{"PAYLOAD_NIL_DEPENDENCY=1"}

		assertRejected := func(err error) {
			var jqErr Error

			So(errors.As(err, &jqErr), ShouldBeTrue)
			So(jqErr, ShouldResemble, Error{Op: requestMethodAdd, Err: ErrBadRequest})
			So(sock.serverErr, ShouldNotBeNil)
			So(sock.serverErr.Error(), ShouldContainSubstring, "jobs[1].Dependencies[1] is nil")
			So(server.q.Stats().Items, ShouldEqual, 0)
			So(valid.EnvKey, ShouldBeBlank)
			So(malformed.EnvKey, ShouldBeBlank)
		}

		_, _, err = client.Add(jobs, env, false)
		assertRejected(err)

		_, _, _, err = client.AddWithWarnings(jobs, env, false)
		assertRejected(err)

		_, err = client.AddAndReturnIDs(jobs, env, false)
		assertRejected(err)

		_, _, err = client.AddAndReturnIDsWithWarnings(jobs, env, false)
		assertRejected(err)

		_, err = client.AddAndWait(ctx, jobs, env, false)
		assertRejected(err)

		_, _, err = client.AddAndWaitWithWarnings(ctx, jobs, env, false)
		assertRejected(err)

		err = testDB.bolt.View(func(tx *bolt.Tx) error {
			So(tx.Bucket(bucketEnvs).Stats().KeyN, ShouldEqual, 0)

			return nil
		})
		So(err, ShouldBeNil)

		validAfterError := &Job{
			Cmd:          "echo manager remains available",
			RepGroup:     "payload-nil-dependency",
			ReqGroup:     "payload-nil-dependency",
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
			Dependencies: Dependencies{&Dependency{}},
		}
		added, existed, err := client.Add([]*Job{validAfterError}, env, false)

		So(sock.serverErr, ShouldBeNil)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)
		So(server.q.Stats().Items, ShouldEqual, 1)
	})
}

func TestServerRejectsAddWithNilBehaviour(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Every encoded and public add variant gets a bad request for a nil behaviour "+
		"at the server boundary", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tmpDir := t.TempDir()
		testDB, _, err := initDB(ctx, filepath.Join(tmpDir, "queue.db"),
			filepath.Join(tmpDir, "queue.db.bak"), internal.Development, false, false)

		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		ch := new(codec.BincHandle)
		token := bytes.Repeat([]byte("x"), tokenLength)
		sock := &captureSocket{ch: ch}
		server := &Server{
			ch:        ch,
			sock:      sock,
			token:     token,
			db:        testDB,
			q:         queue.New(ctx, "payload-nil-behaviour"),
			rpl:       newRGToKeys(),
			depGroups: newDepGroupMembers(),
			up:        true,
		}
		sock.server = server

		id, err := uuid.NewV4()
		So(err, ShouldBeNil)

		client := &Client{ch: ch, clientid: id, sock: sock, token: token}
		valid := &Job{
			Cmd:          "echo valid behaviour job",
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
		}
		malformed := &Job{
			Cmd:          "echo malformed job",
			Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
			Behaviours: Behaviours{
				&Behaviour{When: OnSuccess, Do: Nothing},
				nil,
			},
		}
		jobs := []*Job{valid, malformed}
		env := []string{"PAYLOAD_NIL_BEHAVIOUR=1"}

		assertRejected := func(err error) {
			var jqErr Error

			So(errors.As(err, &jqErr), ShouldBeTrue)
			So(jqErr, ShouldResemble, Error{Op: requestMethodAdd, Err: ErrBadRequest})
			So(sock.serverErr, ShouldNotBeNil)
			So(sock.serverErr.Error(), ShouldContainSubstring, "jobs[1].Behaviours[1] is nil")
			So(server.q.Stats().Items, ShouldEqual, 0)
			So(valid.EnvKey, ShouldBeBlank)
			So(malformed.EnvKey, ShouldBeBlank)
		}

		for _, returnIDs := range []bool{false, true} {
			var encoded []byte

			enc := codec.NewEncoderBytes(&encoded, ch)
			err = enc.Encode(&clientRequest{
				Method: requestMethodAdd, Token: token, Env: []byte("encoded environment"),
				Jobs: jobs, ReturnIDs: returnIDs,
			})
			So(err, ShouldBeNil)

			err = server.handleRequest(ctx, &mangos.Message{Body: encoded})
			So(err, ShouldNotBeNil)

			var detailedErr Error

			So(errors.As(err, &detailedErr), ShouldBeTrue)
			So(detailedErr, ShouldResemble, Error{
				Op: requestMethodAdd, Err: "jobs[1].Behaviours[1] is nil",
			})
			So(sock.response().Err, ShouldEqual, ErrBadRequest)
			So(server.q.Stats().Items, ShouldEqual, 0)
			So(valid.EnvKey, ShouldBeBlank)
			So(malformed.EnvKey, ShouldBeBlank)
		}

		_, _, err = client.Add(jobs, env, false)
		assertRejected(err)

		_, _, _, err = client.AddWithWarnings(jobs, env, false)
		assertRejected(err)

		_, err = client.AddAndReturnIDs(jobs, env, false)
		assertRejected(err)

		_, _, err = client.AddAndReturnIDsWithWarnings(jobs, env, false)
		assertRejected(err)

		_, err = client.AddAndWait(ctx, jobs, env, false)
		assertRejected(err)

		_, _, err = client.AddAndWaitWithWarnings(ctx, jobs, env, false)
		assertRejected(err)

		err = testDB.bolt.View(func(tx *bolt.Tx) error {
			So(tx.Bucket(bucketEnvs).Stats().KeyN, ShouldEqual, 0)

			return nil
		})
		So(err, ShouldBeNil)

		repGroup := "payload-nil-behaviour"
		validAfterError := []*Job{
			{
				Cmd: "echo nil behaviours", RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
			},
			{
				Cmd: "echo empty behaviours", RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
				Behaviours:   Behaviours{},
			},
			{
				Cmd: "echo zero behaviour", RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
				Behaviours:   Behaviours{&Behaviour{}},
			},
			{
				Cmd: "echo remove behaviour", RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
				Behaviours:   Behaviours{&Behaviour{When: OnFailure, Do: Remove}},
			},
		}
		added, existed, err := client.Add(validAfterError, env, false)

		So(sock.serverErr, ShouldBeNil)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 4)
		So(existed, ShouldEqual, 0)
		So(server.q.Stats().Items, ShouldEqual, 4)

		persisted, err := client.GetByRepGroup(repGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(persisted, ShouldHaveLength, 4)
		So(slices.ContainsFunc(persisted, func(job *Job) bool {
			return job.Cmd == "echo remove behaviour" && job.RemovalRequested()
		}), ShouldBeTrue)
	})
}

// TestServerAddDropsImpossibleCleanups covers the Go API add path, which
// neither JobViaJSON.resolveBehaviours nor JobModifier.applyBehaviours is on: a
// caller that builds a *Job itself must still not get a cleanup stored on a
// cwd_matters job, where it could only ever be a no-op.
func TestServerAddDropsImpossibleCleanups(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A hand-built job added with the Go API is stored without cleanups it could never do", t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tmpDir := t.TempDir()
		testDB, _, err := initDB(ctx, filepath.Join(tmpDir, "queue.db"),
			filepath.Join(tmpDir, "queue.db.bak"), internal.Development, false, false)

		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		ch := new(codec.BincHandle)
		token := bytes.Repeat([]byte("x"), tokenLength)
		sock := &captureSocket{ch: ch}
		repGroup := "payload-impossible-cleanups"
		server := &Server{
			ch:        ch,
			sock:      sock,
			token:     token,
			db:        testDB,
			q:         queue.New(ctx, repGroup),
			rpl:       newRGToKeys(),
			depGroups: newDepGroupMembers(),
			up:        true,
		}
		sock.server = server

		id, err := uuid.NewV4()
		So(err, ShouldBeNil)

		client := &Client{ch: ch, clientid: id, sock: sock, token: token}

		newJob := func(cmd string, cwdMatters bool, behaviours Behaviours) *Job {
			return &Job{
				Cmd:          cmd,
				Cwd:          testCwd,
				CwdMatters:   cwdMatters,
				RepGroup:     repGroup,
				ReqGroup:     repGroup,
				Requirements: &scheduler.Requirements{RAM: 100, Time: time.Second, Cores: 1, Disk: 1},
				Behaviours:   behaviours,
			}
		}

		added, existed, err := client.Add([]*Job{
			newJob("echo cwd matters cleanup", true, Behaviours{
				&Behaviour{When: OnExit, Do: Cleanup},
				&Behaviour{When: OnFailure, Do: Run, Arg: "echo add failed"},
			}),
			newJob("echo cwd matters cleanup all", true, Behaviours{
				&Behaviour{When: OnExit, Do: CleanupAll},
			}),
			newJob("echo wr made the cwd", false, Behaviours{
				&Behaviour{When: OnExit, Do: Cleanup},
			}),
		}, []string{"PAYLOAD_IMPOSSIBLE_CLEANUPS=1"}, false)

		So(err, ShouldBeNil)
		So(added, ShouldEqual, 3)
		So(existed, ShouldEqual, 0)

		stored, err := client.GetByRepGroup(repGroup, false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(stored, ShouldHaveLength, 3)

		byCmd := make(map[string]*Job, len(stored))
		for _, job := range stored {
			byCmd[job.Cmd] = job
		}

		Convey("a cwd_matters job keeps its other behaviours but loses its cleanup", func() {
			job := byCmd["echo cwd matters cleanup"]
			So(job, ShouldNotBeNil)
			So(job.Behaviours.String(), ShouldEqual, `{"on_failure":[{"run":"echo add failed"}]}`)
			So(hasCleanupBehaviour(job.Behaviours), ShouldBeFalse)
			So(job.CwdMatters, ShouldBeTrue)
		})

		Convey("a cwd_matters job loses a cleanup_all just the same", func() {
			job := byCmd["echo cwd matters cleanup all"]
			So(job, ShouldNotBeNil)
			So(job.Behaviours.String(), ShouldBeBlank)
			So(hasCleanupBehaviour(job.Behaviours), ShouldBeFalse)
			So(job.CwdMatters, ShouldBeTrue)
		})

		Convey("a job that runs in a directory wr makes keeps its cleanup", func() {
			job := byCmd["echo wr made the cwd"]
			So(job, ShouldNotBeNil)
			So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"cleanup":true}]}`)
			So(hasCleanupBehaviour(job.Behaviours), ShouldBeTrue)
			So(job.CwdMatters, ShouldBeFalse)
		})
	})
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
	states             []*JobEndState
	releaseStdoutSeen  bool
	releaseStderrSeen  bool
	releaseMarkersDone bool
	releaseMarkersErr  error
}

func (c *liveTouchCapture) record(state *JobEndState) {
	c.Lock()
	defer c.Unlock()

	c.states = append(c.states, cloneTestJobEndState(state))
}

func (c *liveTouchCapture) recordAndReleaseOnMarkers(
	releaseFile string,
	stdoutMarker string,
	stderrMarker string,
) func(*JobEndState) {
	return func(state *JobEndState) {
		c.record(state)
		c.releaseOnMarkers(state, releaseFile, stdoutMarker, stderrMarker)
	}
}

func (c *liveTouchCapture) releaseOnMarkers(
	state *JobEndState,
	releaseFile string,
	stdoutMarker string,
	stderrMarker string,
) {
	stdout := decompressLiveTouch(state.Stdout)
	stderr := decompressLiveTouch(state.Stderr)

	c.Lock()
	if strings.Contains(stdout, stdoutMarker) {
		c.releaseStdoutSeen = true
	}

	if strings.Contains(stderr, stderrMarker) {
		c.releaseStderrSeen = true
	}

	release := c.releaseStdoutSeen && c.releaseStderrSeen && !c.releaseMarkersDone
	if release {
		c.releaseMarkersDone = true
	}
	c.Unlock()

	if !release {
		return
	}

	if err := os.WriteFile(releaseFile, []byte("ok"), liveExecuteFileMode); err != nil {
		c.Lock()
		c.releaseMarkersErr = err
		c.releaseMarkersDone = false
		c.Unlock()

		return
	}

	c.Lock()
	c.releaseMarkersErr = nil
	c.Unlock()
}

func (c *liveTouchCapture) releaseErr() error {
	c.Lock()
	defer c.Unlock()

	return c.releaseMarkersErr
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

func TestLiveTouchCaptureReleaseOnMarkersRetry(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Live touch release retry clears stale marker errors", t, func() {
		capture := &liveTouchCapture{}
		releaseDir := filepath.Join(t.TempDir(), "missing")
		releaseFile := filepath.Join(releaseDir, "release")
		state := &JobEndState{
			Stdout: compressStd([]byte("stdout-marker\n")),
			Stderr: compressStd([]byte("stderr-marker\n")),
		}

		capture.releaseOnMarkers(state, releaseFile, "stdout-marker\n", "stderr-marker\n")
		So(capture.releaseErr(), ShouldNotBeNil)

		So(os.Mkdir(releaseDir, liveExecuteDirMode), ShouldBeNil)
		capture.releaseOnMarkers(state, releaseFile, "stdout-marker\n", "stderr-marker\n")

		So(capture.releaseErr(), ShouldBeNil)

		release, err := os.ReadFile(releaseFile)
		So(err, ShouldBeNil)
		So(string(release), ShouldEqual, "ok")
	})
}

func TestClientExecuteLiveTouchPayloads(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Execute sends stdout tails once per touch", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		job := liveExecuteJob(client, cwd, "printf 'alpha\\n'; sleep 0.6; printf 'beta\\n'; sleep 0.6")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

		states := capture.matching(withLiveStdout)
		So(len(states), ShouldBeGreaterThanOrEqualTo, 2)
		So(decompressLiveTouch(states[0].Stdout), ShouldEqual, "alpha\n")
		So(decompressLiveTouch(states[1].Stdout), ShouldEqual, "beta\n")
	})

	Convey("Execute of a cwd_matters job sends no cwd in its live snapshots", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		job := liveExecuteJob(client, liveExecuteCwd(t), "printf 'alpha\\n'; sleep 0.6")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)
		So(capture.matching(withLiveStdout), ShouldNotBeEmpty)

		// a cwd_matters job runs in the user's own Cwd, so it has no actual cwd
		// to report; a reported one becomes an ActualCwd whose parent the
		// cleanup behaviours would delete
		So(capture.matching(withLiveCwd), ShouldBeEmpty)
		So(job.ActualCwd, ShouldBeBlank)
	})

	Convey("Execute of a job with a wr-created working dir sends it in live snapshots", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		job := liveExecuteHashedCwdJob(client, cwd, "printf 'alpha\\n'; sleep 0.6")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

		states := capture.matching(withLiveCwd)
		So(len(states), ShouldBeGreaterThanOrEqualTo, 1)
		So(states[0].Cwd, ShouldStartWith, cwd+string(filepath.Separator))
		So(states[0].Cwd, ShouldEqual, job.ActualCwd)
	})

	Convey("Execute sends stderr tails once per touch", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		job := liveExecuteJob(client, cwd, "printf 'err-alpha\\n' >&2; sleep 0.6; printf 'err-beta\\n' >&2; sleep 0.6")

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

		states := capture.matching(withLiveStderr)
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
				"x = bytearray(32 * 1024 * 1024)",
				"end = time.time() + 5",
				"while time.time() < end:",
				"    x[0] = (x[0] + 1) % 256",
				"PY",
			}, "\n"))

			So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)

			states := capture.matching(func(state *JobEndState) bool {
				return state.CPUtime >= time.Millisecond
			})
			So(len(states), ShouldBeGreaterThanOrEqualTo, 1)

			states = capture.matching(func(state *JobEndState) bool {
				return state.PeakRAM >= 1
			})
			So(len(states), ShouldBeGreaterThanOrEqualTo, 1)
		})
	}

	Convey("Execute bounds live output tails without truncating final archives", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		releaseFile := filepath.Join(cwd, "live-release")
		stdoutStream := liveMarkedStream("OUT-OLD\n", "OUT-NEW\n")
		stderrStream := liveMarkedStream("ERR-OLD\n", "ERR-NEW\n")
		stdoutFile := filepath.Join(cwd, "stdout.bin")
		stderrFile := filepath.Join(cwd, "stderr.bin")
		client.liveTouchHook = capture.recordAndReleaseOnMarkers(releaseFile, "OUT-NEW\n", "ERR-NEW\n")

		So(os.WriteFile(stdoutFile, stdoutStream, liveExecuteFileMode), ShouldBeNil)
		So(os.WriteFile(stderrFile, stderrStream, liveExecuteFileMode), ShouldBeNil)

		cmd := fmt.Sprintf(
			"cat %s; cat %s >&2; i=0; while [ ! -f %s ] && [ \"$i\" -lt %d ]; do i=$((i + 1)); sleep %.2f; done",
			shellquote.Join(stdoutFile),
			shellquote.Join(stderrFile),
			shellquote.Join(releaseFile),
			liveExecuteMarkerWaitAttempts,
			(liveExecuteTouchInterval / 2).Seconds(),
		)
		job := liveExecuteJob(client, cwd, cmd)

		So(client.Execute(context.Background(), job, "/bin/sh"), ShouldBeNil)
		So(capture.releaseErr(), ShouldBeNil)

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

func withLiveCwd(state *JobEndState) bool {
	return state.Cwd != ""
}

func withLiveStdout(state *JobEndState) bool {
	return len(state.Stdout) != 0
}

func withLiveStderr(state *JobEndState) bool {
	return len(state.Stderr) != 0
}

// liveExecuteHashedCwdJob returns a job that is not CwdMatters, so that
// Execute() creates a unique working directory below cwd for it.
func liveExecuteHashedCwdJob(client *Client, cwd string, cmd string) *Job {
	job := liveExecuteJob(client, cwd, cmd)
	job.CwdMatters = false

	return job
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

// TestClientExecuteUnstartedRunWorkSpace covers the runs that fail between
// resolveWorkingDir making a working directory and cmd.Start() succeeding. None
// of them runs the job's behaviours - they bury or release, or return with the
// job still reserved - so if Execute does not remove the directory it made,
// nothing ever will, and the abandoned leaf also stops rmEmptyDirsIn ever
// reclaiming the hashed levels above it.
func TestClientExecuteUnstartedRunWorkSpace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a job that gets a wr-created working directory", t, func() {
		capture := &liveTouchCapture{}
		client := newLiveExecuteCaptureClient(capture)
		cwd := liveExecuteCwd(t)
		precious := writeFileIn(cwd, "05_RunCisEQTL.R")
		hashedBase := filepath.Join(cwd, AppName+createdCwdBaseSuffix)

		Convey("A run whose command cannot start leaves no working directory behind", func() {
			job := liveExecuteHashedCwdJob(client, cwd, "true")
			noShell := filepath.Join(t.TempDir(), "no-such-shell")

			err := client.Execute(context.Background(), job, noShell)

			So(job.ActualCwd, ShouldNotBeBlank)
			soPathsGone(job.ActualCwd, filepath.Dir(job.ActualCwd), hashedBase)
			soPathsExist(precious, cwd)

			So(err, ShouldNotBeNil)
		})

		Convey("A run whose mounts fail leaves no working directory behind either", func() {
			// this is the path that buries the job without ever unmounting, and
			// nothing here is left mounted: Job.Mount unmounts what it managed
			// before the config that failed, and the cleanup unmounts again.
			job := liveExecuteHashedCwdJob(client, cwd, "true")
			job.MountConfigs = MountConfigs{{Mount: testWSMount}}

			err := client.Execute(context.Background(), job, "/bin/sh")

			So(job.ActualCwd, ShouldNotBeBlank)
			soPathsGone(job.ActualCwd, filepath.Dir(job.ActualCwd), hashedBase)
			soPathsExist(precious, cwd)

			So(err, ShouldNotBeNil)
		})
	})
}
