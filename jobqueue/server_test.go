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
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	"github.com/gofrs/uuid/v5"
	. "github.com/smartystreets/goconvey/convey"
)

const archivePortalCompress = "portal_compress"

func TestServerClosedQueueErrors(t *testing.T) {
	Convey("Closed queue errors include queue context", t, func() {
		s := &Server{}

		killed, err := s.killJob(context.Background(), "job-1")

		So(killed, ShouldBeFalse)
		So(err, ShouldNotBeNil)

		var qerr queue.Error
		So(errors.As(err, &qerr), ShouldBeTrue)
		So(qerr.Queue, ShouldEqual, serverQueueName)
		So(qerr.Op, ShouldEqual, "Get")
		So(qerr.Item, ShouldEqual, "job-1")
		So(errors.Is(qerr.Err, queue.ErrQueueClosed), ShouldBeTrue)
		So(err.Error(), ShouldEqual, "queue("+serverQueueName+") Get(job-1): queue closed")
	})
}

func TestMarkJobCompleteUsesEndStateAtomically(t *testing.T) {
	Convey("A successful archive can mark completion from the terminal end state", t, func() {
		ctx := context.Background()
		q := queue.New(ctx, "archive-terminal-state")
		job := &Job{
			Cmd:        restFormTrue,
			Cwd:        testCwd,
			RepGroup:   archivePortalCompress,
			ReqGroup:   archivePortalCompress,
			StartTime:  time.Now().Add(-2 * time.Minute),
			State:      JobStateRunning,
			FailReason: FailReasonLost,
			Lost:       true,
		}

		_, err := q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)

		// markJobComplete no longer gates on the queue sub-state (that guard was
		// reverted to v0.36.5 semantics and now lives in getij(cr, true), exercised
		// end-to-end by TestReliable2HoldingRunnerArchiveAccepted); a Lost job whose
		// owner archives success is accepted.
		endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		key, repGroup, schedulerGroup, srerr := markJobComplete(job, endState, nil)

		So(srerr, ShouldEqual, "")
		So(key, ShouldEqual, job.Key())
		So(repGroup, ShouldEqual, archivePortalCompress)
		So(schedulerGroup, ShouldEqual, "")
		So(job.Exited, ShouldBeTrue)
		So(job.Exitcode, ShouldEqual, 0)
		So(job.State, ShouldEqual, JobStateComplete)
		// markJobComplete intentionally does NOT clear Lost: a parked-lost job's
		// removal must count lost->complete (not running->complete) in the web-UI
		// counter, so Lost is left set until the job leaves the run queue (as the
		// delete path also leaves it). It is invisible on a Complete job since
		// buildJStatus only surfaces Lost when State==Running.
		So(job.Lost, ShouldBeTrue)
		So(job.FailReason, ShouldEqual, "")
	})

	Convey("A successful archive with a nil limiter does not panic after limit groups were noted", t, func() {
		ctx := context.Background()
		q := queue.New(ctx, "archive-nil-limiter")
		job := &Job{
			Cmd:         restFormTrue,
			Cwd:         testCwd,
			RepGroup:    archivePortalCompress,
			ReqGroup:    archivePortalCompress,
			StartTime:   time.Now().Add(-time.Minute),
			State:       JobStateRunning,
			LimitGroups: []string{"archive-limit"},
		}
		job.noteIncrementedLimitGroups(job.LimitGroups)

		_, err := q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)

		endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}

		So(func() {
			_, _, _, _ = markJobComplete(job, endState, nil)
		}, ShouldNotPanic)
		So(job.State, ShouldEqual, JobStateComplete)
	})

	Convey("markJobComplete rejects a stale archive after another runner reserves the job", t, func() {
		ctx := context.Background()
		q := queue.New(ctx, "archive-stale-reserver")
		originalRunner, err := uuid.NewV4()
		So(err, ShouldBeNil)
		rerunner, err := uuid.NewV4()
		So(err, ShouldBeNil)

		job := &Job{
			Cmd:        restFormTrue,
			Cwd:        testCwd,
			RepGroup:   archivePortalCompress,
			ReqGroup:   archivePortalCompress,
			StartTime:  time.Now().Add(-time.Minute),
			State:      JobStateRunning,
			ReservedBy: rerunner,
		}

		_, err = q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)

		endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		_, _, _, srerr := markJobComplete(job, endState, nil, originalRunner)

		So(srerr, ShouldEqual, ErrMustReserve)
		So(job.State, ShouldEqual, JobStateRunning)
		So(job.Exited, ShouldBeFalse)
	})

	Convey("A successful archive can override a lost reclaim before rerun", t, func() {
		ctx := context.Background()
		q := queue.New(ctx, "archive-lost-reclaim")
		startTime := time.Now().Add(-2 * time.Minute)
		lostTime := startTime.Add(2 * time.Minute)
		job := &Job{
			Cmd:         restFormTrue,
			Cwd:         testCwd,
			RepGroup:    archivePortalCompress,
			ReqGroup:    archivePortalCompress,
			StartTime:   startTime,
			EndTime:     lostTime,
			State:       JobStateDelayed,
			FailReason:  FailReasonLost,
			Exitcode:    -1,
			Exited:      true,
			UntilBuried: 1,
		}

		item, err := q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)
		So(q.Release(ctx, job.Key()), ShouldBeNil)
		So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

		endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: lostTime.Add(500 * time.Millisecond)}
		_, _, _, srerr := markJobComplete(job, endState, nil)

		So(srerr, ShouldEqual, "")
		So(job.Exited, ShouldBeTrue)
		So(job.Exitcode, ShouldEqual, 0)
		So(job.EndTime, ShouldEqual, endState.EndTime)
		So(job.State, ShouldEqual, JobStateComplete)
		So(job.Lost, ShouldBeFalse)
		So(job.FailReason, ShouldEqual, "")
	})

	Convey("A successful archive with an invalid (non-zero exit) end state is rejected", t, func() {
		ctx := context.Background()
		q := queue.New(ctx, "archive-bad-endstate")
		job := &Job{
			Cmd:       restFormTrue,
			Cwd:       testCwd,
			RepGroup:  archivePortalCompress,
			ReqGroup:  archivePortalCompress,
			StartTime: time.Now().Add(-time.Minute),
			State:     JobStateRunning,
		}

		_, err := q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)

		endState := &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}
		_, _, _, srerr := markJobComplete(job, endState, nil)

		So(srerr, ShouldEqual, ErrBadRequest)
		So(job.State, ShouldEqual, JobStateRunning)
		So(job.Exited, ShouldBeFalse)
	})
}

func TestSuccessfulArchiveOverridesLostReclaimBeforeRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("A successful archive is rejected once the job was released out of the run queue", t, func() {
		// The reverted jarchive requires the queue item to be in SubQueueRun
		// (getij(cr, true)); an explicitly-released job has left Run, so its archive
		// is rejected as ErrBadJob. Under the full reliability fix an alive job is
		// never released - it parks Lost in Run, where its owner's archive is
		// accepted (TestReliable2HoldingRunnerArchiveAccepted) - so this only bites
		// a genuinely-released job, exactly as in v0.36.5.
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		job := &Job{
			Cmd:          restFormTrue,
			Cwd:          testCwd,
			RepGroup:     archivePortalCompress,
			ReqGroup:     archivePortalCompress,
			Requirements: standardReqs,
			Retries:      1,
		}
		inserts, already, err := jq.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved.Cmd, ShouldEqual, restFormTrue)
		So(jq.Started(reserved, 1), ShouldBeNil)

		lostEnd := &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}
		So(jq.Release(reserved, lostEnd, FailReasonLost), ShouldBeNil)

		successEnd := &JobEndState{Exited: true, Exitcode: 0, EndTime: lostEnd.EndTime.Add(time.Second)}
		archiveErr := jq.Archive(reserved, successEnd)
		So(archiveErr, ShouldNotBeNil)

		var jqerr Error
		So(errors.As(archiveErr, &jqerr), ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, ErrBadJob)

		summaries, err := jq.GetStatusByRepGroupMatch(archivePortalCompress, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries[archivePortalCompress].Counts[JobStateComplete], ShouldEqual, 0)
	})

	Convey("A stale successful archive cannot win after another runner reserves the job", t, func() {
		fastConfig := serverConfig
		fastConfig.Timings.ReleaseDelayMin = time.Nanosecond
		server, _, token, err := serve(ctx, fastConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		original, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(original)

		rerunner, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(rerunner)

		job := &Job{
			Cmd:          restFormTrue,
			Cwd:          testCwd,
			RepGroup:     archivePortalCompress,
			ReqGroup:     archivePortalCompress,
			Requirements: standardReqs,
			Retries:      1,
		}
		inserts, already, err := original.Add([]*Job{job}, envVars, true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		reserved, err := original.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(original.Started(reserved, 1), ShouldBeNil)

		lostEnd := &JobEndState{Exited: true, Exitcode: -1, EndTime: time.Now()}
		So(original.Release(reserved, lostEnd, FailReasonLost), ShouldBeNil)

		rerun, err := rerunner.Reserve(time.Second)
		So(err, ShouldBeNil)
		So(rerun.Cmd, ShouldEqual, restFormTrue)

		successEnd := &JobEndState{Exited: true, Exitcode: 0, EndTime: lostEnd.EndTime.Add(time.Second)}
		err = original.Archive(reserved, successEnd)
		So(err, ShouldNotBeNil)

		var jqerr Error
		So(errors.As(err, &jqerr), ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, ErrMustReserve)
	})
}
