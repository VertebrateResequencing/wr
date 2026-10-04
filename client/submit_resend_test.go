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

package client

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/internal/replyproxy"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	resendTestJobs          = 3
	resendTestTimeout       = 10 * time.Second
	resendTestPoll          = 10 * time.Millisecond
	resendTestReconnectHold = 500 * time.Millisecond
)

var errResendTestPending = errors.New("SubmitJobs did not return")

// TestSchedulerSubmitJobsResentAfterItsReplyWasLost checks that a SubmitJobs
// whose add the manager acted on, but whose reply was lost with its connection,
// succeeds when the client resends it and the manager reports those jobs as
// already queued: they are this call's own jobs
// (.docs/bugfixes/261002-client-restart-2ca6d42f.md).
func TestSchedulerSubmitJobsResentAfterItsReplyWasLost(t *testing.T) {
	Convey("Given a Scheduler talking to a manager through a connection that can lose a reply", t, func() {
		ctx := context.Background()

		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		server := clienttesting.Serve(t, config)
		defer server.Stop(ctx, true)

		proxy := replyproxy.Start(t, net.JoinHostPort("localhost", config.Port))

		jq, err := jobqueue.ConnectWithTokenFile(proxy.Addr(), config.CAFile,
			config.CertDomain, config.TokenFile, resendTestTimeout)
		So(err, ShouldBeNil)

		defer func() {
			So(jq.Disconnect(), ShouldBeNil)
		}()

		s := &Scheduler{jq: jq, cwd: t.TempDir()}

		jobs := make([]*jobqueue.Job, resendTestJobs)
		for i := range jobs {
			jobs[i] = s.NewJob("echo resent "+strconv.Itoa(i), "rg-resent", "req-resent", "", "", nil)
		}

		Convey("SubmitJobs succeeds, adding each job once, when its add is resent after the manager acted on it", func() {
			proxy.Armed.Store(true)

			err = s.SubmitJobs(jobs)

			So(proxy.Dropped.Load(), ShouldEqual, 1)
			So(err, ShouldBeNil)
			So(server.GetServerStats().Ready, ShouldEqual, resendTestJobs)

			Convey("and submitting the same jobs again without a resend still returns ErrDuplicateJobs", func() {
				err = s.SubmitJobs(jobs)
				So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
				So(server.GetServerStats().Ready, ShouldEqual, resendTestJobs)
			})
		})

		Convey("SubmitJobs does not re-add a job its add queued and that completed before the connection dropped", func() {
			runner, errc := jobqueue.ConnectWithTokenFile(net.JoinHostPort("localhost", config.Port), config.CAFile,
				config.CertDomain, config.TokenFile, resendTestTimeout)
			So(errc, ShouldBeNil)

			defer func() {
				So(runner.Disconnect(), ShouldBeNil)
			}()

			job := jobs[0]

			proxy.Swallow.Store(true)

			done := make(chan error, 1)

			go func() { done <- s.SubmitJobs([]*jobqueue.Job{job}) }()

			// the manager has answered the add, so it has queued the job, but
			// the client has not had the reply
			deadline := time.Now().Add(resendTestTimeout)
			for proxy.Swallowed.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(resendTestPoll)
			}

			So(proxy.Swallowed.Load(), ShouldBeGreaterThan, 0)
			So(archiveNextSchedulerJob(runner), ShouldBeNil)

			proxy.Swallow.Store(false)
			proxy.Cut()

			select {
			case err = <-done:
			case <-time.After(resendTestTimeout):
				err = errResendTestPending
			}

			So(server.GetServerStats().Ready, ShouldEqual, 0)

			stored, errg := s.GetJobByKey(job.Key(), false, false)
			So(errg, ShouldBeNil)
			So(stored.State, ShouldEqual, jobqueue.JobStateComplete)
			So(errors.Is(err, jobqueue.ErrResentAddSkippedComplete), ShouldBeTrue)
		})

		Convey("SubmitJobs of jobs already queued, sent while the client reconnects, returns ErrDuplicateJobs", func() {
			So(s.SubmitJobs(jobs), ShouldBeNil)

			proxy.Paused.Store(true)
			proxy.Cut()

			// a refused connection is the client redialling, so its
			// connection has gone and the next add must wait for a new one
			deadline := time.Now().Add(resendTestTimeout)
			for proxy.Refused.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(resendTestPoll)
			}

			So(proxy.Refused.Load(), ShouldBeGreaterThan, 0)

			done := make(chan error, 1)

			go func() { done <- s.SubmitJobs(jobs) }()

			time.Sleep(resendTestReconnectHold)
			proxy.Paused.Store(false)

			select {
			case err = <-done:
			case <-time.After(resendTestTimeout):
				err = errResendTestPending
			}

			So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
			So(proxy.Dropped.Load(), ShouldEqual, 0)
			So(server.GetServerStats().Ready, ShouldEqual, resendTestJobs)
		})

		Convey("SubmitJobs of jobs already queued returns ErrDuplicateJobs when nothing was resent", func() {
			So(s.SubmitJobs(jobs), ShouldBeNil)

			err = s.SubmitJobs(jobs)
			So(errors.Is(err, ErrDuplicateJobs), ShouldBeTrue)
			So(proxy.Dropped.Load(), ShouldEqual, 0)
		})
	})
}
