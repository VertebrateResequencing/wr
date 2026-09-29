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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

// This file covers .docs/bugfixes/260929-archive-before-start.md. A runner
// whose Started() report went unanswered because the manager died keeps its
// command running and re-sends the start in the background. When the command
// finished while the manager was down, the runner's archive could reach the
// restarted manager before the retried start. The manager rejected it as a bad
// request, because the job's StartTime was still zero, and the runner took that
// as final and discarded the completed work. The start landed a moment later,
// and the job ran again once its TTR lapsed and its runner was found dead:
// 1,091 such re-runs in prodsim round 4.

const (
	// archiveBeforeStartRepGroup names the one job the crash test adds.
	archiveBeforeStartRepGroup = "archive_before_start"

	// archiveBeforeStartWait bounds each wait in these tests.
	archiveBeforeStartWait = 30 * time.Second

	// archiveBeforeStartRetryWait is the runner's retry interval in these tests.
	archiveBeforeStartRetryWait = 100 * time.Millisecond

	// archiveBeforeStartTimeout is the error a lost start report gets.
	archiveBeforeStartTimeout = "receive time out"

	// archiveBeforeStartMethod is the method of a runner's archive request.
	archiveBeforeStartMethod = "jarchive"
)

// archiveBeforeStartSocket wraps a runner's real socket. It stands in for a
// manager that died before answering the runner's first Started() report: that
// report is not forwarded, and its reply is a timeout once crashed is closed.
// Every later start report also fails, as it would while the manager is down,
// until the manager has replied to an archive. The first archive therefore
// reaches the manager before any start report does, unless the runner holds it
// back until its start has been acknowledged. Other requests are forwarded.
type archiveBeforeStartSocket struct {
	mangos.Socket

	ch         codec.Handle
	crashed    <-chan struct{}
	firstStart chan struct{}

	mu        sync.Mutex
	starts    int
	released  bool
	dropped   bool
	waitCrash bool
	method    string
}

func newArchiveBeforeStartSocket(inner mangos.Socket, ch codec.Handle,
	crashed <-chan struct{},
) *archiveBeforeStartSocket {
	return &archiveBeforeStartSocket{
		Socket:     inner,
		ch:         ch,
		crashed:    crashed,
		firstStart: make(chan struct{}),
	}
}

func (s *archiveBeforeStartSocket) Send(msg []byte) error {
	req := &clientRequest{}
	_ = codec.NewDecoderBytes(msg, s.ch).Decode(req) //nolint:errcheck // best-effort peek at the method

	s.mu.Lock()
	s.method = req.Method
	s.dropped = req.Method == requestMethodStart && !s.released
	s.waitCrash = false

	if s.dropped {
		s.starts++

		if s.starts == 1 {
			s.waitCrash = true
			close(s.firstStart)
		}
	}

	dropped := s.dropped
	s.mu.Unlock()

	if dropped {
		return nil
	}

	return s.Socket.Send(msg)
}

func (s *archiveBeforeStartSocket) Recv() ([]byte, error) {
	s.mu.Lock()
	dropped, waitCrash, method := s.dropped, s.waitCrash, s.method
	s.mu.Unlock()

	if dropped {
		if waitCrash {
			<-s.crashed
		}

		return nil, Error{requestMethodStart, "", archiveBeforeStartTimeout}
	}

	resp, err := s.Socket.Recv()
	if err == nil && method == archiveBeforeStartMethod {
		s.mu.Lock()
		s.released = true
		s.mu.Unlock()
	}

	return resp, err
}

// TestArchiveBeforeRetriedStart proves that a job whose command finished while
// the manager was down completes once, and is not run again, when the runner's
// start report was lost to the crash.
func TestArchiveBeforeRetriedStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner whose start report was lost when the manager crashed", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")
		cmd := startDurabilityCmd(marker, stopFile)

		defer func() {
			_ = os.WriteFile(stopFile, nil, 0o600) //nolint:errcheck // best-effort test cleanup
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		job := &Job{
			Cmd: cmd, Cwd: testCwd, RepGroup: archiveBeforeStartRepGroup,
			ReqGroup: archiveBeforeStartRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		// the manager's committed state with the reservation recorded but not
		// the start: what it wrote before it died.
		crashImage := &bytes.Buffer{}
		So(server.BackupDB(crashImage), ShouldBeNil)

		crashed := make(chan struct{})
		sock := newArchiveBeforeStartSocket(jq.sock, jq.ch, crashed)
		jq.sock = sock

		// no touches, so none can land on the manager while it shuts down and
		// be told to kill the command.
		jq.touchInterval = time.Hour
		jq.retryWait = archiveBeforeStartRetryWait
		jq.retryTime = archiveBeforeStartWait

		execErr := make(chan error, 1)

		go func() {
			execErr <- jq.Execute(ctx, reserved, config.RunnerExecShell)
		}()

		select {
		case <-sock.firstStart:
		case <-time.After(archiveBeforeStartWait):
		}

		So(waitForRuns(marker, 1, archiveBeforeStartWait), ShouldBeTrue)

		server.Stop(ctx, true)
		So(os.WriteFile(serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

		serverConfig.dontWipeDevDB = true

		server, _, _, err = serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() { server.Stop(ctx, true) }()

		So(waitUntilRecovered(server), ShouldBeTrue)
		close(crashed)

		// the runner's connection to the restarted manager is back, so its
		// archive reaches the manager at the first attempt.
		So(waitForPing(jq), ShouldBeTrue)

		So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)

		var errExec error

		select {
		case errExec = <-execErr:
		case <-time.After(2 * archiveBeforeStartWait):
			errExec = errStartNeverReturned
		}

		Convey("the runner's archive is accepted, and the job completes once and is not run again", func() {
			// a runner that reported its job's end after trouble reaching the
			// manager says to stop reserving, but nothing worse.
			if errExec != nil {
				var jqErr Error
				So(errors.As(errExec, &jqErr), ShouldBeTrue)
				So(jqErr.Err, ShouldEqual, ErrStopReserving)
			}

			jq2, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			done, errg := jq2.GetByRepGroup(archiveBeforeStartRepGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(len(done), ShouldEqual, 1)
			So(done[0].State, ShouldEqual, JobStateComplete)
			So(done[0].StartTime.IsZero(), ShouldBeFalse)

			second, errr := jq2.Reserve(time.Second)
			So(errr, ShouldBeNil)
			So(second, ShouldBeNil)
			So(runCount(marker), ShouldEqual, 1)
		})
	})
}

// startOrderSocket wraps a runner's real socket. Start reports fail as if the
// manager were unreachable, without reaching it, until startOrderFailFor has
// passed since the first of them. It records whether an archive was sent while
// no start report had been accepted. A runner that meets a failure while
// reporting its final state reconnects, replacing this socket, so it only sees
// what the runner sends before then.
type startOrderSocket struct {
	mangos.Socket

	ch codec.Handle

	mu                  sync.Mutex
	firstStart          time.Time
	startAccepted       bool
	archivedBeforeStart bool
	failing             bool
}

// startOrderFailFor is how long start reports fail for. It is far longer than
// a runner takes to report the end of a command that exits at once, so that a
// runner that does not wait for its start to be acknowledged sends its archive
// while its start reports are failing.
const startOrderFailFor = 2 * time.Second

func (s *startOrderSocket) Send(msg []byte) error {
	req := &clientRequest{}
	_ = codec.NewDecoderBytes(msg, s.ch).Decode(req) //nolint:errcheck // best-effort peek at the method

	s.mu.Lock()
	s.failing = false

	switch req.Method {
	case requestMethodStart:
		if s.firstStart.IsZero() {
			s.firstStart = time.Now()
		}

		s.failing = time.Since(s.firstStart) < startOrderFailFor
		s.startAccepted = s.startAccepted || !s.failing
	case archiveBeforeStartMethod:
		s.archivedBeforeStart = s.archivedBeforeStart || !s.startAccepted
	}

	failing := s.failing
	s.mu.Unlock()

	if failing {
		return nil
	}

	return s.Socket.Send(msg)
}

func (s *startOrderSocket) Recv() ([]byte, error) {
	s.mu.Lock()
	failing := s.failing
	s.mu.Unlock()

	if failing {
		return nil, Error{requestMethodStart, "", archiveBeforeStartTimeout}
	}

	return s.Socket.Recv()
}

// TestFinalStateWaitsForStartReport proves that a runner whose start report
// failed does not report its command's end until the start has been accepted.
func TestFinalStateWaitsForStartReport(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A runner whose start report keeps failing does not archive before the start is accepted", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: restFormTrue + " startorder", Cwd: testCwd, RepGroup: archiveBeforeStartRepGroup,
			ReqGroup: archiveBeforeStartRepGroup, Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		sock := &startOrderSocket{Socket: jq.sock, ch: jq.ch}
		jq.sock = sock
		jq.touchInterval = time.Hour
		jq.retryWait = archiveBeforeStartRetryWait
		jq.retryTime = archiveBeforeStartWait

		execErr := jq.Execute(ctx, reserved, config.RunnerExecShell)

		sock.mu.Lock()
		archivedBeforeStart := sock.archivedBeforeStart
		sock.mu.Unlock()

		So(archivedBeforeStart, ShouldBeFalse)

		var jqErr Error
		So(errors.As(execErr, &jqErr), ShouldBeTrue)
		So(jqErr.Err, ShouldEqual, ErrStopReserving)

		jobs, errg := jq.GetByRepGroup(archiveBeforeStartRepGroup, false, 0, "", true, false)
		So(errg, ShouldBeNil)
		So(len(jobs), ShouldEqual, 1)
		So(jobs[0].State, ShouldEqual, JobStateComplete)
		So(jobs[0].Attempts, ShouldEqual, 1)
	})
}

// TestArchiveImpliesStart proves that the manager completes a job in the run
// queue whose runner reports its successful end before its start, but only for
// the runner that holds the reservation.
func TestArchiveImpliesStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job reserved by a runner whose start has not been recorded", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: restFormTrue + " impliedstart", Cwd: testCwd, RepGroup: archiveBeforeStartRepGroup,
			ReqGroup: archiveBeforeStartRepGroup, Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		jq2, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq2)

		endTime := time.Now()
		endState := func() *JobEndState {
			return &JobEndState{Exited: true, Exitcode: 0, EndTime: endTime}
		}

		Convey("another client's archive is refused, and the job stays reserved", func() {
			var jqErr Error
			So(errors.As(jq2.Archive(reserved, endState()), &jqErr), ShouldBeTrue)
			So(jqErr.Err, ShouldEqual, ErrMustReserve)

			jobs, errg := jq2.GetByRepGroup(archiveBeforeStartRepGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateReserved)
		})

		Convey("its runner's archive completes it, implying the start, and a late start is refused", func() {
			So(jq.Archive(reserved, endState()), ShouldBeNil)

			jobs, errg := jq2.GetByRepGroup(archiveBeforeStartRepGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateComplete)
			So(jobs[0].StartTime.Equal(endTime), ShouldBeTrue)
			So(jobs[0].Attempts, ShouldEqual, 1)

			var jqErr Error
			So(errors.As(jq.Started(reserved, os.Getpid()), &jqErr), ShouldBeTrue)
			So(jqErr.Err, ShouldEqual, ErrBadJob)
		})
	})

	Convey("A job out of the run queue with no recorded start is not completed", t, func() {
		job := &Job{Cmd: restFormTrue, Cwd: testCwd, State: JobStateDelayed}
		endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}

		_, _, _, srerr := markJobComplete(job, endState, nil, false)
		So(srerr, ShouldEqual, ErrBadRequest)
		So(job.StartTime.IsZero(), ShouldBeTrue)
		So(job.State, ShouldEqual, JobStateDelayed)
	})
}
