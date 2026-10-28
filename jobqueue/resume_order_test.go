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
// new attempt. Nor may a reservation that overtakes the resume take the job
// before it is prepared, and other reserves do not wait for the ready-added
// callback the resume queued.

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
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

// TestResumeRacingReserveGetsPreparedJob proves that a reservation made as soon
// as a resume makes its job ready gets the job already prepared at the size
// learned while it was suspended, and that another reserve does not wait for the
// ready-added callback the resume queued.
func TestResumeRacingReserveGetsPreparedJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job reserved as soon as its resume makes it ready", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute
		serverConfig.Timings.ReqStatRefreshInterval = -1

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

		seedReqGroupStats(server, FailReasonRAM, resumeOrderRepGroup, noHoldLearnedRAM)

		So(pollUntil(server.racIdle), ShouldBeTrue)

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

		// stands in for a runner whose reserve was already waiting when the
		// resume began: it takes the item as soon as it is ready
		var (
			hookReservedRAM = -1
			hookSrerr       string
		)

		resumeItemReadyHook = func(hooked string) {
			if hooked != key {
				return
			}

			item, srerr := server.reserveItem(ctx, &clientRequest{})
			hookSrerr = srerr

			if item == nil || item.Key != key {
				return
			}

			if job, ok := item.Data().(*Job); ok {
				job.RLock()
				hookReservedRAM = job.Requirements.RAM
				job.RUnlock()
			}
		}
		defer func() { resumeItemReadyHook = nil }()

		So(server.resumeJobs(ctx, []string{key}), ShouldEqual, 1)

		select {
		case <-racCalled:
		case <-time.After(5 * time.Second):
			t.Fatal("the resume did not call the ready-added callback")
		}

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		Convey("the job reserved at once is prepared at the learned size", func() {
			So(hookSrerr, ShouldBeBlank)
			So(hookReservedRAM, ShouldEqual, noHoldLearnedRAM)
		})

		Convey("another reserve returns nothing without waiting for the ready-added callback", func() {
			// a reserve still held by the gate must be let go before runner
			// disconnects, or the disconnect waits for it
			defer openGate()

			returned, job := reserveReturnsWhileGated(runner)
			So(returned, ShouldBeTrue)
			So(job, ShouldBeNil)
		})
	})
}

// TestFailedResumeLeavesJobAndStops proves that a resume that fails after its
// write was prepared, because the item stopped being suspended in between,
// changes nothing and gives back what the prepare took: the job keeps its
// state, and the manager still stops, which a leaked write slot would block
// forever.
func TestFailedResumeLeavesJobAndStops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job whose item is resumed behind its resume's back", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		serverStopped := false

		defer func() {
			if !serverStopped {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " resumefail", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		var itemResumeErr error

		jobChangeAheadHook = func(hooked string) {
			if hooked == key {
				itemResumeErr = server.q.Resume(ctx, key)
			}
		}
		defer func() { jobChangeAheadHook = nil }()

		Convey("resuming it resumes nothing, leaves it suspended, and the manager stops", func() {
			resumed := server.resumeJobs(ctx, []string{key})
			jobChangeAheadHook = nil

			sjob.RLock()
			state := sjob.State
			sjob.RUnlock()

			serverStopped = true

			So(itemResumeErr, ShouldBeNil)
			So(resumed, ShouldEqual, 0)
			So(state, ShouldEqual, JobStateSuspended)
			So(stopsPromptly(ctx, server), ShouldBeTrue)
		})
	})
}

// TestResumeWritesResumedState proves that a resume with nothing changing
// around it stores the job in the state it resumed to.
func TestResumeWritesResumedState(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job that is resumed", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const depGroup = resumeOrderRepGroup + "_plaindep"

		parent := &Job{
			Cmd: restFormTrue + " resumeplain parent", Cwd: testCwd, RepGroup: resumeOrderRepGroup + "_parent",
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3, DepGroups: []string{depGroup},
		}
		other := &Job{
			Cmd: restFormTrue + " resumeplain other", Cwd: testCwd, RepGroup: resumeOrderRepGroup + "_other",
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		target := &Job{
			Cmd: restFormTrue + " resumeplain", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}

		resumeAndStore := func(jobs ...*Job) string {
			inserts, _, erra := jq.Add(jobs, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, len(jobs))

			key := target.Key()

			suspended, errs := jq.Suspend([]*JobEssence{{JobKey: key}})
			So(errs, ShouldBeNil)
			So(suspended, ShouldEqual, 1)

			So(server.resumeJobs(ctx, []string{key}), ShouldEqual, 1)

			otherItem, errg := server.q.Get(other.Key())
			So(errg, ShouldBeNil)

			otherJob, ok := otherItem.Data().(*Job)
			So(ok, ShouldBeTrue)

			// a durable write queued after the resume's commits no earlier than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			return key
		}

		Convey("one with no unresolved dependencies is stored ready", func() {
			key := resumeAndStore(target, other)
			So(storedLiveJobState(t, server.inspector(), key), ShouldEqual, JobStateReady)
		})

		Convey("one with an unresolved dependency is stored dependent", func() {
			target.Dependencies = Dependencies{NewDepGroupDependency(depGroup)}

			key := resumeAndStore(parent, target, other)
			So(storedLiveJobState(t, server.inspector(), key), ShouldEqual, JobStateDependent)
		})
	})
}

// TestResumeStoredWithoutLaterWrite proves that a resume's write reaches the
// store on its own, on a manager that makes no other write afterwards: nothing
// else may be needed to wake the writer, or a crash of an idle manager would
// recover the resumed job as still suspended.
func TestResumeStoredWithoutLaterWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job stored as suspended on an otherwise idle manager", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " resumeidle", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		suspended, err := jq.Suspend([]*JobEssence{{JobKey: key}})
		So(err, ShouldBeNil)
		So(suspended, ShouldEqual, 1)
		So(storedStateWithin(t, server.inspector(), key, JobStateSuspended), ShouldEqual, JobStateSuspended)

		Convey("resuming it stores it ready with no other write following", func() {
			resumed, errr := jq.Resume([]*JobEssence{{JobKey: key}})
			So(errr, ShouldBeNil)
			So(resumed, ShouldEqual, 1)
			So(storedStateWithin(t, server.inspector(), key, JobStateReady), ShouldEqual, JobStateReady)
		})
	})
}

// TestResumeAfterChangeWritesResumedJob proves that a resume whose write was
// encoded before the suspended job changed writes the job as it is resumed: a
// modify landing in between keeps its fields, a dependency resolved in between
// leaves the job stored as ready rather than as the dependent it was expected to
// resume to, and a dependency gained in between leaves it stored as dependent
// rather than as the ready it was expected to resume to.
func TestResumeAfterChangeWritesResumedJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a suspended job that changes after its resume prepared its write", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const depGroup = resumeOrderRepGroup + "_dep"

		parent := &Job{
			Cmd: restFormTrue + " resumechange parent", Cwd: testCwd, RepGroup: resumeOrderRepGroup + "_parent",
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 2,
			DepGroups: []string{depGroup},
		}
		other := &Job{
			Cmd: restFormTrue + " resumechange other", Cwd: testCwd, RepGroup: resumeOrderRepGroup + "_other",
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		target := &Job{
			Cmd: restFormTrue + " resumechange", Cwd: testCwd, RepGroup: resumeOrderRepGroup,
			ReqGroup: resumeOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 1,
		}

		resumeTarget := func(hook func()) (int, string) {
			key := target.Key()

			suspended, errs := jq.Suspend([]*JobEssence{{JobKey: key}})
			So(errs, ShouldBeNil)
			So(suspended, ShouldEqual, 1)

			jobChangeAheadHook = func(hooked string) {
				if hooked == key {
					hook()
				}
			}
			defer func() { jobChangeAheadHook = nil }()

			resumed := server.resumeJobs(ctx, []string{key})
			jobChangeAheadHook = nil

			otherItem, errg := server.q.Get(other.Key())
			So(errg, ShouldBeNil)

			otherJob, ok := otherItem.Data().(*Job)
			So(ok, ShouldBeTrue)

			// a durable write queued after the resume's commits no earlier than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			return resumed, key
		}

		Convey("a modify landing in between keeps its fields in the resume's write", func() {
			inserts, _, erra := jq.Add([]*Job{target, other}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 2)

			const modifiedPriority = 9

			modifier := &JobModifier{}
			modifier.SetPriority(modifiedPriority)

			var (
				modified  map[string]string
				modifyErr error
			)

			resumed, key := resumeTarget(func() {
				modified, modifyErr = jq.Modify([]*JobEssence{{JobKey: target.Key()}}, modifier)
			})

			So(modifyErr, ShouldBeNil)
			So(modified, ShouldResemble, map[string]string{key: key})
			So(resumed, ShouldEqual, 1)

			stored := storedLiveJob(t, server.inspector(), key)
			So(stored.State, ShouldEqual, JobStateReady)
			So(stored.Priority, ShouldEqual, modifiedPriority)
		})

		Convey("a dependency resolved in between leaves it stored ready", func() {
			target.Dependencies = Dependencies{NewDepGroupDependency(depGroup)}

			inserts, _, erra := jq.Add([]*Job{parent, target, other}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 3)

			resumed, key := resumeTarget(func() { resumeOrderComplete(jq, parent.Key()) })
			So(resumed, ShouldEqual, 1)

			item, errg := server.q.Get(key)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, queue.ItemStateReady)
			So(storedLiveJobState(t, server.inspector(), key), ShouldEqual, JobStateReady)
		})

		Convey("a dependency gained in between leaves it stored dependent", func() {
			inserts, _, erra := jq.Add([]*Job{target, other}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 2)

			// the item gains its dependency without the job being write-locked,
			// so only the resume's own prediction can tell its write is stale.
			resumed, key := resumeTarget(func() {
				item, errg := server.q.Get(target.Key())
				So(errg, ShouldBeNil)

				sjob, ok := item.Data().(*Job)
				So(ok, ShouldBeTrue)

				stats := item.Stats()

				sjob.RLock()
				priority := sjob.Priority
				sjob.RUnlock()

				So(server.q.Update(ctx, target.Key(), sjob.getSchedulerGroup(), sjob, priority,
					stats.Delay, stats.TTR, []string{other.Key()}), ShouldBeNil)
			})

			So(resumed, ShouldEqual, 1)

			item, errg := server.q.Get(key)
			So(errg, ShouldBeNil)
			So(item.Stats().State, ShouldEqual, queue.ItemStateDependent)
			So(storedLiveJobState(t, server.inspector(), key), ShouldEqual, JobStateDependent)
		})
	})
}

// resumeOrderComplete reserves the job with the given key, which must be the
// highest priority ready one, and archives it as having exited 0.
func resumeOrderComplete(jq *Client, key string) {
	job, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(job.Key(), ShouldEqual, key)
	So(jq.Started(job, os.Getpid()), ShouldBeNil)
	So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
}
