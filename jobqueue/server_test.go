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

		item, err := q.Add(ctx, job.Key(), "", job, 0, 0, time.Minute, queue.SubQueueRun)
		So(err, ShouldBeNil)

		endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
		key, repGroup, schedulerGroup, srerr := markJobComplete(item, job, endState, nil)

		So(srerr, ShouldEqual, "")
		So(key, ShouldEqual, job.Key())
		So(repGroup, ShouldEqual, archivePortalCompress)
		So(schedulerGroup, ShouldEqual, "")
		So(job.Exited, ShouldBeTrue)
		So(job.Exitcode, ShouldEqual, 0)
		So(job.State, ShouldEqual, JobStateComplete)
		So(job.Lost, ShouldBeFalse)
		So(job.FailReason, ShouldEqual, "")
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
		_, _, _, srerr := markJobComplete(item, job, endState, nil)

		So(srerr, ShouldEqual, "")
		So(job.Exited, ShouldBeTrue)
		So(job.Exitcode, ShouldEqual, 0)
		So(job.EndTime, ShouldEqual, endState.EndTime)
		So(job.State, ShouldEqual, JobStateComplete)
		So(job.Lost, ShouldBeFalse)
		So(job.FailReason, ShouldEqual, "")
	})

	Convey("A successful archive still rejects an ordinary non-running job", t, func() {
		ctx := context.Background()
		cases := []struct {
			name       string
			startQueue queue.SubQueue
			delay      time.Duration
			state      JobState
		}{
			{name: "ready", startQueue: queue.SubQueueRun, state: JobStateDelayed},
			{name: "delayed", delay: time.Minute, state: JobStateDelayed},
			{name: "buried", startQueue: queue.SubQueueBury, state: JobStateBuried},
		}

		for _, tc := range cases {
			q := queue.New(ctx, "archive-ordinary-"+tc.name)
			job := &Job{
				Cmd:       restFormTrue + " # " + tc.name,
				Cwd:       testCwd,
				RepGroup:  archivePortalCompress,
				ReqGroup:  archivePortalCompress,
				StartTime: time.Now().Add(-time.Second),
				State:     tc.state,
			}

			item, err := q.Add(ctx, job.Key(), "", job, 0, tc.delay, time.Minute, tc.startQueue)
			So(err, ShouldBeNil)

			if tc.name == "ready" {
				So(q.Release(ctx, job.Key()), ShouldBeNil)
			}

			endState := &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}
			_, _, _, srerr := markJobComplete(item, job, endState, nil)

			So(srerr, ShouldEqual, ErrBadJob)
			So(job.State, ShouldEqual, tc.state)
		}
	})
}

func TestSuccessfulArchiveOverridesLostReclaimBeforeRerun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("A stale successful archive wins after lost reclaim but before rerun", t, func() {
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
		So(jq.Archive(reserved, successEnd), ShouldBeNil)

		summaries, err := jq.GetStatusByRepGroupMatch(archivePortalCompress, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries[archivePortalCompress].Counts, ShouldResemble, map[JobState]int{JobStateComplete: 1})
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
