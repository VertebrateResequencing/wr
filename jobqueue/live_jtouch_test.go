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
	"strings"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	"go.nanomsg.org/mangos/v3"
)

const (
	liveJTouchActualCwd      = "/tmp/wr/job1"
	liveJTouchPreviousCwd    = "/tmp/old"
	liveJTouchPreviousStderr = "olderr\n"
	liveJTouchPreviousStdout = "old\n"
	liveJTouchQueue          = "live-jtouch"
	liveJTouchStderr         = "err\n"
	liveJTouchStdout         = "out\n"
	liveJTouchTTR            = time.Second
	liveJTouchWebPort        = "1234"
	liveStatusCloudUser      = "cloud_user"
	liveStatusHost           = "worker1"
	liveStatusHostIP         = "10.0.0.8"
)

type liveJTouchFixture struct {
	server *Server
	sock   *captureSocket
	job    *Job
	item   *queue.Item
	token  []byte
	key    string
	client uuid.UUID
}

func newLiveJTouchFixture(ctx context.Context, webPort string) *liveJTouchFixture {
	return newLiveJTouchFixtureWithCwdMatters(ctx, webPort, false)
}

func newLiveJTouchFixtureWithCwdMatters(
	ctx context.Context,
	webPort string,
	cwdMatters bool,
) *liveJTouchFixture {
	ch := new(codec.BincHandle)
	sock := &captureSocket{ch: ch}
	clientID, err := uuid.NewV4()
	So(err, ShouldBeNil)

	token := []byte(strings.Repeat("x", tokenLength))
	job := &Job{
		Cmd:          "echo live jtouch",
		Cwd:          testCwd,
		CwdMatters:   cwdMatters,
		RepGroup:     liveJTouchQueue,
		Requirements: &jqs.Requirements{RAM: 1, Time: time.Minute, Cores: 1},
		ReservedBy:   clientID,
		State:        JobStateRunning,
		StartTime:    time.Now(),
	}
	key := job.Key()
	q := queue.New(ctx, liveJTouchQueue)
	item, err := q.Add(ctx, key, "", job, 0, 0, liveJTouchTTR, queue.SubQueueRun)
	So(err, ShouldBeNil)

	return &liveJTouchFixture{
		server: &Server{
			ch:         ch,
			sock:       sock,
			token:      token,
			q:          q,
			up:         true,
			ServerInfo: &ServerInfo{WebPort: webPort},
		},
		sock:   sock,
		job:    job,
		item:   item,
		token:  token,
		key:    key,
		client: clientID,
	}
}

func newKillCalledLiveJTouchFixture(ctx context.Context) *liveJTouchFixture {
	fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)
	setLiveJTouchFields(
		fixture.job,
		liveJTouchPreviousCwd,
		111,
		7,
		2*time.Second,
		liveJTouchPreviousStdout,
		liveJTouchPreviousStderr,
	)
	fixture.job.Lock()
	fixture.job.killCalled = true
	fixture.job.Unlock()

	return fixture
}

func (fixture *liveJTouchFixture) touch(
	ctx context.Context,
	token []byte,
	endState *JobEndState,
) (*serverResponse, error) {
	var encoded []byte

	enc := codec.NewEncoderBytes(&encoded, fixture.server.ch)
	err := enc.Encode(&clientRequest{
		Method:      requestMethodTouch,
		Token:       token,
		Keys:        []string{fixture.key},
		ClientID:    fixture.client,
		JobEndState: endState,
	})
	So(err, ShouldBeNil)

	err = fixture.server.handleRequest(ctx, &mangos.Message{Body: encoded})

	return fixture.sock.response(), err
}

func (fixture *liveJTouchFixture) remainingTTRAfterDelay() time.Duration {
	time.Sleep(20 * time.Millisecond)

	return fixture.item.Stats().Remaining
}

func TestManagerLiveJTouch(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("An authenticated live jtouch stores a live snapshot behind the secure gate", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)
		before := fixture.remainingTTRAfterDelay()
		endState := &JobEndState{
			Cwd:      liveJTouchActualCwd,
			PeakRAM:  321,
			PeakDisk: 9,
			CPUtime:  4 * time.Second,
			Stdout:   compressStd([]byte(liveJTouchStdout)),
			Stderr:   compressStd([]byte(liveJTouchStderr)),
		}

		resp, err := fixture.touch(ctx, fixture.token, endState)
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(
			fixture.job,
			liveJTouchActualCwd,
			321,
			9,
			4*time.Second,
			liveJTouchStdout,
			liveJTouchStderr,
		)
	})

	Convey("A live jtouch of a cwd_matters job stores no ActualCwd", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixtureWithCwdMatters(ctx, liveJTouchWebPort, true)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:      testCwd,
			PeakRAM:  321,
			PeakDisk: 9,
			CPUtime:  4 * time.Second,
			Stdout:   compressStd([]byte(liveJTouchStdout)),
			Stderr:   compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)

		// a cwd_matters job runs in the user's own Cwd, so it has no ActualCwd;
		// letting a client set one makes the cleanup behaviours treat Cwd's
		// parent as a disposable wr workspace
		assertLiveJTouchFields(fixture.job, "", 321, 9, 4*time.Second, liveJTouchStdout, liveJTouchStderr)

		fixture.job.Lock()
		fixture.job.Host = liveStatusHost
		fixture.job.Unlock()

		update, err := jobUpdateFromLiveJob(fixture.job)
		So(err, ShouldBeNil)
		So(update.CwdBase, ShouldEqual, testCwd)
		So(update.Cwd, ShouldBeBlank)
		So(update.SSHCommand, ShouldContainSubstring, "cd "+testCwd+" &&")
	})

	Convey("An authenticated resource-only live jtouch preserves existing output tails", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:      liveJTouchActualCwd,
			PeakRAM:  321,
			PeakDisk: 9,
			CPUtime:  4 * time.Second,
			Stdout:   compressStd([]byte(liveJTouchStdout)),
			Stderr:   compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)

		resp, err = fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:      liveJTouchActualCwd,
			PeakRAM:  654,
			PeakDisk: 12,
			CPUtime:  7 * time.Second,
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchFields(
			fixture.job,
			liveJTouchActualCwd,
			654,
			12,
			7*time.Second,
			liveJTouchStdout,
			liveJTouchStderr,
		)
	})

	Convey("An authenticated reserved live jtouch is visible through itemToJob", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)
		fixture.job.Lock()
		fixture.job.State = JobStateReserved
		fixture.job.StartTime = time.Time{}
		fixture.job.Unlock()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte(liveJTouchStdout)),
			Stderr:  compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)

		job := fixture.server.itemToJob(ctx, fixture.item, true, false)
		So(job.State, ShouldEqual, JobStateReserved)

		stdout, err := job.StdOut()
		So(err, ShouldBeNil)
		So(stdout, ShouldEqual, liveJTouchStdout)

		stderr, err := job.StdErr()
		So(err, ShouldBeNil)
		So(stderr, ShouldEqual, liveJTouchStderr)
	})

	Convey("An older runner jtouch with no live fields only extends TTR", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)
		setLiveJTouchFields(
			fixture.job,
			liveJTouchPreviousCwd,
			111,
			7,
			2*time.Second,
			liveJTouchPreviousStdout,
			liveJTouchPreviousStderr,
		)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(
			fixture.job,
			liveJTouchPreviousCwd,
			111,
			7,
			2*time.Second,
			liveJTouchPreviousStdout,
			liveJTouchPreviousStderr,
		)
	})

	Convey("An authenticated live jtouch with no HTTPS web port only extends TTR", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, "")
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte(liveJTouchStdout)),
			Stderr:  compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchExtendedTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "", 0, 0, 0, "", "")
	})

	Convey("A live jtouch with an invalid token is denied without touching TTR or live fields", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, []byte(strings.Repeat("y", tokenLength)), &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte(liveJTouchStdout)),
			Stderr:  compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldNotBeNil)
		So(resp.Err, ShouldEqual, ErrPermissionDenied)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(fixture.job, "", 0, 0, 0, "", "")
	})

	Convey("A live jtouch with no token is denied before a KillCalled response", t, func() {
		ctx := context.Background()
		fixture := newLiveJTouchFixture(ctx, liveJTouchWebPort)
		setLiveJTouchFields(
			fixture.job,
			liveJTouchPreviousCwd,
			111,
			7,
			2*time.Second,
			liveJTouchPreviousStdout,
			liveJTouchPreviousStderr,
		)
		fixture.job.Lock()
		fixture.job.killCalled = true
		fixture.job.Unlock()
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, nil, &JobEndState{
			Cwd:     liveJTouchActualCwd,
			PeakRAM: 321,
			CPUtime: 4 * time.Second,
			Stdout:  compressStd([]byte(liveJTouchStdout)),
			Stderr:  compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldNotBeNil)
		So(resp.Err, ShouldEqual, ErrPermissionDenied)
		So(resp.KillCalled, ShouldBeFalse)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(
			fixture.job,
			liveJTouchPreviousCwd,
			111,
			7,
			2*time.Second,
			liveJTouchPreviousStdout,
			liveJTouchPreviousStderr,
		)
	})

	Convey("An authenticated live jtouch preserves KillCalled jobs and existing live fields", t, func() {
		ctx := context.Background()
		fixture := newKillCalledLiveJTouchFixture(ctx)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{
			Cwd:      liveJTouchActualCwd,
			PeakRAM:  321,
			PeakDisk: 9,
			CPUtime:  4 * time.Second,
			Stdout:   compressStd([]byte(liveJTouchStdout)),
			Stderr:   compressStd([]byte(liveJTouchStderr)),
		})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeTrue)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(
			fixture.job,
			liveJTouchPreviousCwd,
			111,
			7,
			2*time.Second,
			liveJTouchPreviousStdout,
			liveJTouchPreviousStderr,
		)
	})

	Convey("An authenticated older-runner jtouch preserves KillCalled jobs and existing live fields", t, func() {
		ctx := context.Background()
		fixture := newKillCalledLiveJTouchFixture(ctx)
		before := fixture.remainingTTRAfterDelay()

		resp, err := fixture.touch(ctx, fixture.token, &JobEndState{})
		So(err, ShouldBeNil)
		So(resp.KillCalled, ShouldBeTrue)
		assertLiveJTouchDidNotExtendTTR(before, fixture.item.Stats().Remaining)
		assertLiveJTouchFields(
			fixture.job,
			liveJTouchPreviousCwd,
			111,
			7,
			2*time.Second,
			liveJTouchPreviousStdout,
			liveJTouchPreviousStderr,
		)
	})
}

func assertLiveJTouchExtendedTTR(before, after time.Duration) {
	So(after.Nanoseconds(), ShouldBeGreaterThan, before.Nanoseconds())
}

func assertLiveJTouchFields(
	job *Job,
	actualCwd string,
	peakRAM int,
	peakDisk int64,
	cpuTime time.Duration,
	stdout string,
	stderr string,
) {
	job.RLock()
	So(job.State, ShouldEqual, JobStateRunning)
	So(job.Exited, ShouldBeFalse)
	So(job.ActualCwd, ShouldEqual, actualCwd)
	So(job.PeakRAM, ShouldEqual, peakRAM)
	So(job.PeakDisk, ShouldEqual, peakDisk)
	So(job.CPUtime, ShouldEqual, cpuTime)
	job.RUnlock()

	out, err := job.StdOut()
	So(err, ShouldBeNil)
	So(out, ShouldEqual, stdout)

	errOut, err := job.StdErr()
	So(err, ShouldBeNil)
	So(errOut, ShouldEqual, stderr)
}

func setLiveJTouchFields(
	job *Job,
	actualCwd string,
	peakRAM int,
	peakDisk int64,
	cpuTime time.Duration,
	stdout string,
	stderr string,
) {
	job.Lock()
	job.ActualCwd = actualCwd
	job.PeakRAM = peakRAM
	job.PeakDisk = peakDisk
	job.CPUtime = cpuTime
	job.StdOutC = compressStd([]byte(stdout))
	job.StdErrC = compressStd([]byte(stderr))
	job.Unlock()
}

func assertLiveJTouchDidNotExtendTTR(before, after time.Duration) {
	So(after.Nanoseconds(), ShouldBeLessThanOrEqualTo, before.Nanoseconds())
}
