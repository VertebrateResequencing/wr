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

// This file covers a resume racing a reservation of the job it resumes: the
// resume makes the job's item reservable before it updates the job, so a runner
// can reserve and start the job in between. The resume must not then reset that
// running job to reserved, or the runner's re-sent start report is taken for a
// new attempt. Nor may a reservation that overtakes the resume make it release
// other reserves before the ready-added callback it queued has run.

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const resumeOrderRepGroup = "resume_order"

// TestResumeRacingStart proves that a job reserved and started between a resume
// making it reservable and the resume updating it stays running.
func TestResumeRacingStart(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job whose resume is overtaken by a runner's reservation and start", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " resumeorder", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		resumed, release := make(chan struct{}), make(chan struct{})

		resumeQueuedHook = func(hooked string) {
			if hooked != key {
				return
			}

			close(resumed)
			<-release
		}
		defer func() { resumeQueuedHook = nil }()

		type resumeOutcome struct {
			n   int
			err error
		}

		resumeResult := make(chan resumeOutcome, 1)

		go func() {
			n, errr := jq.Resume([]*JobEssence{{JobKey: key}})
			resumeResult <- resumeOutcome{n, errr}
		}()

		select {
		case <-resumed:
		case outcome := <-resumeResult:
			t.Fatalf("the resume returned %+v without reaching resumeQueuedHook", outcome)
		}

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		reserved, errReserve := runner.Reserve(2 * time.Second)

		var errStart error
		if errReserve == nil && reserved != nil {
			errStart = runner.Started(reserved, os.Getpid())
		}

		close(release)

		So(errReserve, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(errStart, ShouldBeNil)
		So(<-resumeResult, ShouldResemble, resumeOutcome{n: 1})

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		Convey("the job stays running in memory", func() {
			sjob.RLock()
			state := sjob.State
			sjob.RUnlock()

			So(state, ShouldEqual, JobStateRunning)
		})

		Convey("its runner's re-sent start report is not counted as another attempt", func() {
			So(runner.Started(reserved, os.Getpid()), ShouldBeNil)

			sjob.RLock()
			attempts := sjob.Attempts
			sjob.RUnlock()

			So(attempts, ShouldEqual, 1)
		})
	})
}

// TestResumeRacingReserveKeepsRACPending proves that a resume whose item is
// reserved before the resume checks its state still holds back other reserves
// until the ready-added callback it queued has run.
func TestResumeRacingReserveKeepsRACPending(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job reserved between its resume making it ready and the resume checking it", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " resumerac", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		// a reserve waits out every ready-added callback the add and suspend
		// queued, and finds nothing, since the job is suspended
		idle, err := jq.Reserve(10 * time.Millisecond)
		So(err, ShouldBeNil)
		So(idle, ShouldBeNil)

		server.rpmutex.Lock()
		racIdle := !server.racPending && !server.racRunning
		server.rpmutex.Unlock()
		So(racIdle, ShouldBeTrue)

		// gate the ready-added callback, so the test decides when it runs
		racCalled, racGate := make(chan struct{}), make(chan struct{})

		var calledOnce, gateOnce sync.Once

		openGate := func() { gateOnce.Do(func() { close(racGate) }) }
		defer openGate()

		server.q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
			calledOnce.Do(func() { close(racCalled) })
			<-racGate
			server.readyAddedCallback(ctx, server.q, allitemdata)
		})

		// stands in for a runner whose reserve was already past
		// waitForPendingReserves when the resume began: it takes the item as
		// soon as it is ready
		var (
			hookReserved bool
			hookSrerr    string
		)

		resumeItemReadyHook = func(hooked string) {
			if hooked != key {
				return
			}

			item, srerr := server.reserveItem(ctx, &clientRequest{})
			hookReserved = item != nil && item.Key == key
			hookSrerr = srerr
		}
		defer func() { resumeItemReadyHook = nil }()

		So(server.resumeJobs(ctx, []string{key}), ShouldEqual, 1)
		So(hookSrerr, ShouldBeBlank)
		So(hookReserved, ShouldBeTrue)

		select {
		case <-racCalled:
		case <-time.After(5 * time.Second):
			t.Fatal("the resume did not call the ready-added callback")
		}

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		type reserveOutcome struct {
			job *Job
			err error
		}

		reserveResult := make(chan reserveOutcome, 1)

		go func() {
			job, errr := runner.Reserve(50 * time.Millisecond)
			reserveResult <- reserveOutcome{job, errr}
		}()

		Convey("another reserve waits until the ready-added callback has run", func() {
			returnedEarly := false

			select {
			case <-reserveResult:
				returnedEarly = true
			case <-time.After(time.Second):
			}

			So(returnedEarly, ShouldBeFalse)

			openGate()

			outcome := <-reserveResult
			So(outcome.err, ShouldBeNil)
			So(outcome.job, ShouldBeNil)
		})
	})
}
