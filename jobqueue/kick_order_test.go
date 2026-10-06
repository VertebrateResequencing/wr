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

// This file covers a kick racing a reservation of the job it kicks: the kick
// makes the job's item reservable before it updates the job, so a runner can
// reserve the job in between. The kick must not then reset that reservation to
// ready, in memory or on disk, or a manager crash before the runner's Started
// recovers the job onto the ready queue while the runner is running it, and a
// second runner runs it again.

import (
	"bytes"
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const kickOrderRepGroup = "kick_order"

// failedChangeStopTimeout bounds how long a manager may take to stop after a
// failed kick or resume.
const failedChangeStopTimeout = 20 * time.Second

// storedStateWait bounds how long a test waits for a queued write of a job's
// state to reach the store.
const storedStateWait = 5 * time.Second

// TestKickAfterModifyKeepsModification proves that a kick whose write was
// encoded before a modify of the buried job landed writes the modified job: the
// write a kick prepares ahead of taking the queue's lock must not put back the
// fields the modify changed, nor the retry budget of the old Retries.
func TestKickAfterModifyKeepsModification(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job modified after its kick prepared its write", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " kickmodify", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 1,
		}
		other := &Job{
			Cmd: restFormTrue + " kickmodify other", Cwd: testCwd, RepGroup: kickOrderRepGroup + "_other",
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target, other}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		key := target.Key()

		first, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(first, ShouldNotBeNil)
		So(first.Key(), ShouldEqual, key)
		So(jq.Bury(first, nil, "failed"), ShouldBeNil)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		otherItem, err := server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		const modifiedRetries, modifiedPriority = 7, 9

		modifier := &JobModifier{}
		modifier.SetRetries(modifiedRetries)
		modifier.SetPriority(modifiedPriority)

		var (
			modified  map[string]string
			modifyErr error
		)

		jobChangeAheadHook = func(hooked string) {
			if hooked == key {
				modified, modifyErr = jq.Modify([]*JobEssence{{JobKey: key}}, modifier)
			}
		}
		defer func() { jobChangeAheadHook = nil }()

		kicked := server.kickJobs(ctx, []*Job{sjob})
		jobChangeAheadHook = nil

		So(modifyErr, ShouldBeNil)
		So(modified, ShouldResemble, map[string]string{key: key})
		So(kicked, ShouldEqual, 1)

		// a durable write queued after the kick's commits no earlier than it.
		So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

		Convey("the kick's write keeps the modified fields and the budget of the new Retries", func() {
			stored := storedLiveJob(t, server.db, key)
			So(stored.State, ShouldEqual, JobStateReady)
			So(stored.Retries, ShouldEqual, modifiedRetries)
			So(stored.Priority, ShouldEqual, modifiedPriority)
			So(stored.UntilBuried, ShouldEqual, initialUntilBuried(modifiedRetries))
		})
	})
}

// TestKickDuringBuryWriteStoresKicked proves that a kick landing while the bury
// it undoes is still queueing its write leaves the job stored as kicked: the
// bury's write, encoded first, must not queue after the kick's and put the job
// back on disk as buried while it is ready in memory.
func TestKickDuringBuryWriteStoresKicked(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a kick prepared before a bury whose write pauses once encoded", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " kickbury", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 1,
		}
		other := &Job{
			Cmd: restFormTrue + " kickbury other", Cwd: testCwd, RepGroup: kickOrderRepGroup + "_other",
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target, other}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		key := target.Key()

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		otherItem, err := server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		buryErr, kickedN := make(chan error, 1), make(chan int, 1)
		exitEncoded, releaseExit := make(chan struct{}), make(chan struct{})

		jobExitSnapshotHook = func(hooked string) {
			if hooked == key {
				close(exitEncoded)
				<-releaseExit
			}
		}
		defer func() { jobExitSnapshotHook = nil }()

		// the kick prepares its write while the job is still running, then waits
		// for the bury to have encoded its write before it kicks the buried item.
		jobChangeAheadHook = func(hooked string) {
			if hooked != key {
				return
			}

			go func() { buryErr <- jq.Bury(reserved, nil, "failed") }()

			<-exitEncoded
		}
		defer func() { jobChangeAheadHook = nil }()

		go func() { kickedN <- server.kickJobs(ctx, []*Job{sjob}) }()

		<-exitEncoded

		// give a kick that does not wait for the bury's write the time to queue
		// its own first.
		select {
		case n := <-kickedN:
			kickedN <- n
		case <-time.After(500 * time.Millisecond):
		}

		close(releaseExit)

		So(<-buryErr, ShouldBeNil)
		So(<-kickedN, ShouldEqual, 1)

		jobExitSnapshotHook, jobChangeAheadHook = nil, nil

		// a durable write queued after both commits no earlier than them.
		So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

		Convey("the job is ready in memory and stored ready", func() {
			sjob.RLock()
			state := sjob.State
			sjob.RUnlock()

			So(state, ShouldEqual, JobStateReady)
			So(storedLiveJobState(t, server.db, key), ShouldEqual, JobStateReady)
		})
	})
}

// TestFailedKickLeavesJobAndStops proves that a kick of a job that is not
// buried, whose write was nonetheless prepared, changes nothing and gives back
// what the prepare took: the job keeps its state and retry budget, and the
// manager still stops, which a leaked write slot would block forever.
func TestFailedKickLeavesJobAndStops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a reserved job that has used a retry", t, func() {
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
			Cmd: restFormTrue + " kickfail", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		sjob.Lock()
		sjob.UntilBuried--
		wantUntilBuried := sjob.UntilBuried
		sjob.Unlock()

		Convey("kicking it kicks nothing, leaves it reserved with its budget, and the manager stops", func() {
			kicked := server.kickJobs(ctx, []*Job{sjob})

			sjob.RLock()
			state, untilBuried := sjob.State, sjob.UntilBuried
			sjob.RUnlock()

			serverStopped = true

			So(kicked, ShouldEqual, 0)
			So(state, ShouldEqual, JobStateReserved)
			So(untilBuried, ShouldEqual, wantUntilBuried)
			So(stopsWithin(ctx, server, failedChangeStopTimeout), ShouldBeTrue)
		})
	})
}

// TestFailedKickOfDependentJobStops proves that a failed kick gives back what
// its prepare took even when the kick expected no ready callback, because the
// job's item has unresolved dependencies: the manager still stops, which a
// leaked write slot would block.
func TestFailedKickOfDependentJobStops(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job dependent on an incomplete parent", t, func() {
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

		const depGroup = kickOrderRepGroup + "_dep"

		parent := &Job{
			Cmd: restFormTrue + " kickfaildep parent", Cwd: testCwd, RepGroup: kickOrderRepGroup + "_parent",
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3, DepGroups: []string{depGroup},
		}
		target := &Job{
			Cmd: restFormTrue + " kickfaildep", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
			Dependencies: Dependencies{NewDepGroupDependency(depGroup)},
		}
		inserts, _, err := jq.Add([]*Job{parent, target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		item, err := server.q.Get(target.Key())
		So(err, ShouldBeNil)
		So(item.Stats().State, ShouldEqual, queue.ItemStateDependent)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		Convey("kicking it kicks nothing, and the manager stops", func() {
			kicked := server.kickJobs(ctx, []*Job{sjob})

			serverStopped = true

			So(kicked, ShouldEqual, 0)
			So(stopsWithin(ctx, server, failedChangeStopTimeout), ShouldBeTrue)
		})
	})
}

// stopsWithin stops server and says whether it stopped within d.
func stopsWithin(ctx context.Context, server *Server, d time.Duration) bool {
	stopped := make(chan struct{})

	go func() {
		server.Stop(ctx, true)
		close(stopped)
	}()

	select {
	case <-stopped:
		return true
	case <-time.After(d):
		return false
	}
}

// TestKickStoredWithoutLaterWrite proves that a kick's write reaches the store
// on its own, on a manager that makes no other write afterwards: nothing else
// may be needed to wake the writer, or a crash of an idle manager would recover
// the kicked job as still buried.
func TestKickStoredWithoutLaterWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job stored as buried on an otherwise idle manager", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " kickidle", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Bury(reserved, nil, "failed"), ShouldBeNil)
		So(storedStateWithin(t, server.db, key, JobStateBuried), ShouldEqual, JobStateBuried)

		Convey("kicking it stores it ready with no other write following", func() {
			kicked, errk := jq.Kick([]*JobEssence{{JobKey: key}})
			So(errk, ShouldBeNil)
			So(kicked, ShouldEqual, 1)
			So(storedStateWithin(t, server.db, key, JobStateReady), ShouldEqual, JobStateReady)
		})
	})
}

// TestOverlappingChangesAheadKeepModification proves that a change prepared
// ahead of a job, while another prepared ahead of it is discarded first, still
// writes a modification made in between: ending the first must not let the
// second take its stale encoding for the job's current one.
func TestOverlappingChangesAheadKeepModification(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job stored as buried with two kicks prepared ahead", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " kickoverlap", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 1,
		}
		other := &Job{
			Cmd: restFormTrue + " kickoverlap other", Cwd: testCwd, RepGroup: kickOrderRepGroup + "_other",
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target, other}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		key := target.Key()

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Bury(reserved, nil, "failed"), ShouldBeNil)
		So(storedStateWithin(t, server.db, key, JobStateBuried), ShouldEqual, JobStateBuried)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		otherItem, err := server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		first := server.db.prepareJobChange(ctx, sjob, kickChange)
		So(first, ShouldNotBeNil)

		second := server.db.prepareJobChange(ctx, sjob, kickChange)
		So(second, ShouldNotBeNil)

		Convey("discarding the first, then modifying the job, has the second write the modification", func() {
			const modifiedPriority = 9

			server.db.discardJobChangeAhead(sjob, first)

			sjob.Lock()
			sjob.Priority = modifiedPriority
			sjob.Unlock()
			server.db.updateJobAfterChange(ctx, sjob)

			server.db.queueJobChangeAhead(ctx, sjob, second, kickChange)

			// a durable write queued after the second's commits no earlier than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			stored := storedLiveJob(t, server.db, key)
			So(stored.State, ShouldEqual, JobStateReady)
			So(stored.Priority, ShouldEqual, modifiedPriority)
		})
	})
}

// TestKickQueuesBeforeLaterChange proves that a change made to a job while a
// kick is queueing its prepared write is not lost: the job stays write-locked
// until that write is queued, so the change's own write, encoded later, queues
// after it rather than before the kick's stale encoding.
func TestKickQueuesBeforeLaterChange(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job stored as buried", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " kickqueueing", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 1,
		}
		other := &Job{
			Cmd: restFormTrue + " kickqueueing other", Cwd: testCwd, RepGroup: kickOrderRepGroup + "_other",
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target, other}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		key := target.Key()

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Bury(reserved, nil, "failed"), ShouldBeNil)
		So(storedStateWithin(t, server.db, key, JobStateBuried), ShouldEqual, JobStateBuried)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		otherItem, err := server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		Convey("kicking it while its priority changes stores it ready with the new priority", func() {
			const modifiedPriority = 9

			changed := make(chan struct{})

			var hooked atomic.Bool

			jobChangeAheadQueueingHook = func(queueing string) {
				if queueing != key || !hooked.CompareAndSwap(false, true) {
					return
				}

				go func() {
					defer close(changed)

					sjob.Lock()
					sjob.Priority = modifiedPriority
					sjob.Unlock()
					server.db.updateJobAfterChange(ctx, sjob)
				}()

				// if the job stays write-locked until the kick's write is
				// queued, the change cannot finish now, so this only waits long
				// enough for it to try.
				select {
				case <-changed:
					t.Log("the change queued its write while the kick's was still unqueued")
				case <-time.After(time.Second):
				}
			}
			defer func() { jobChangeAheadQueueingHook = nil }()

			kicked := server.kickJobs(ctx, []*Job{sjob})
			jobChangeAheadQueueingHook = nil

			So(kicked, ShouldEqual, 1)
			So(hooked.Load(), ShouldBeTrue)

			<-changed

			// a durable write queued after the change's commits no earlier than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			stored := storedLiveJob(t, server.db, key)
			So(stored.State, ShouldEqual, JobStateReady)
			So(stored.Priority, ShouldEqual, modifiedPriority)
		})
	})
}

// TestKickAfterDBClosedStillKicks proves that a kick made once the database has
// closed, as during a manager's stop, still kicks the job in memory, and that
// the write it cannot make gives back no write slot it never took.
func TestKickAfterDBClosedStillKicks(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job on a manager whose database has closed", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		// a kick that panics in the queue's callback leaves the queue locked,
		// and the stop would then hang.
		kickPanicked := false

		defer func() {
			if !kickPanicked {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		target := &Job{
			Cmd: restFormTrue + " kickclosed", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		key := target.Key()

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(jq.Bury(reserved, nil, "failed"), ShouldBeNil)
		So(storedStateWithin(t, server.db, key, JobStateBuried), ShouldEqual, JobStateBuried)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		So(server.db.close(ctx), ShouldBeNil)

		Convey("kicking it kicks it to ready in memory without panicking", func() {
			kicked := 0
			kickPanicked = true

			So(func() { kicked = server.kickJobs(ctx, []*Job{sjob}) }, ShouldNotPanic)

			kickPanicked = false

			sjob.RLock()
			state, untilBuried := sjob.State, sjob.UntilBuried
			sjob.RUnlock()

			So(kicked, ShouldEqual, 1)
			So(state, ShouldEqual, JobStateReady)
			So(untilBuried, ShouldEqual, initialUntilBuried(target.Retries))
		})
	})
}

// storedStateWithin polls the state stored for key in database's live bucket
// until it is want or storedStateWait has passed, and returns the last state it
// read.
func storedStateWithin(t *testing.T, database *db, key string, want JobState) JobState {
	t.Helper()

	deadline := time.Now().Add(storedStateWait)

	for {
		state := storedLiveJobState(t, database, key)
		if state == want || time.Now().After(deadline) {
			return state
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// TestKickRacingReservation proves that a reservation landing between a kick
// making the job reservable and the kick updating it is neither undone in memory
// nor lost to a crash.
func TestKickRacingReservation(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a buried job whose kick is overtaken by a runner's reservation", t, func() {
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
			Cmd: restFormTrue + " kickorder", Cwd: testCwd, RepGroup: kickOrderRepGroup,
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3, Priority: 1,
		}
		other := &Job{
			Cmd: restFormTrue + " kickorder other", Cwd: testCwd, RepGroup: kickOrderRepGroup + "_other",
			ReqGroup: kickOrderRepGroup, Requirements: standardReqs, Retries: 3,
		}
		inserts, _, err := jq.Add([]*Job{target, other}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		key := target.Key()

		first, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(first, ShouldNotBeNil)
		So(first.Key(), ShouldEqual, key)
		So(jq.Bury(first, nil, "failed"), ShouldBeNil)

		otherItem, err := server.q.Get(other.Key())
		So(err, ShouldBeNil)

		otherJob, ok := otherItem.Data().(*Job)
		So(ok, ShouldBeTrue)

		// park the other job out of the way so only the kicked job is ready.
		_, err = jq.Suspend([]*JobEssence{{JobKey: other.Key()}})
		So(err, ShouldBeNil)

		kicked, resume := make(chan struct{}), make(chan struct{})

		// the image a crash just as the job becomes reservable leaves: a durable
		// write queued now commits no earlier than any write queued before it.
		kickedImage := &bytes.Buffer{}

		var kickedImageErr error

		kickQueuedHook = func(hooked string) {
			if hooked != key {
				return
			}

			kickedImageErr = server.db.updateJobAfterChangeDurable(otherJob)
			if kickedImageErr == nil {
				kickedImageErr = server.BackupDB(kickedImage)
			}

			close(kicked)
			<-resume
		}
		defer func() { kickQueuedHook = nil }()

		type kickOutcome struct {
			n   int
			err error
		}

		kickResult := make(chan kickOutcome, 1)

		go func() {
			n, errk := jq.Kick([]*JobEssence{{JobKey: key}})
			kickResult <- kickOutcome{n, errk}
		}()

		select {
		case <-kicked:
		case outcome := <-kickResult:
			t.Fatalf("the kick returned %+v without reaching kickQueuedHook", outcome)
		}

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		reserved, err := runner.Reserve(2 * time.Second)

		close(resume)

		So(err, ShouldBeNil)
		So(kickedImageErr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, key)
		So(<-kickResult, ShouldResemble, kickOutcome{n: 1})

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		sjob, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		Convey("the job stays reserved in memory", func() {
			sjob.RLock()
			state := sjob.State
			sjob.RUnlock()

			So(state, ShouldEqual, JobStateReserved)
		})

		Convey("a crash before its start recovers it reserved, so a fresh runner is not given it", func() {
			// a durable write queued after the kick's commits no earlier than it,
			// so the crash image holds whatever the kick wrote.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			crashImage := &bytes.Buffer{}
			So(server.BackupDB(crashImage), ShouldBeNil)

			serverStopped = true

			jq2, stop := kickOrderRestartOn(ctx, server, serverConfig, addr, crashImage, clientConnectTime)
			defer stop()

			recovered, errg := jq2.GetByRepGroup(kickOrderRepGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(recovered), ShouldEqual, 1)

			recoveredState := recovered[0].State

			second, errr := jq2.Reserve(200 * time.Millisecond)
			So(errr, ShouldBeNil)

			var secondKey string
			if second != nil {
				secondKey = second.Key()
			}

			So(secondKey, ShouldBeEmpty)
			So(recoveredState, ShouldEqual, JobStateReserved)
		})

		Convey("a crash as the kick makes it reservable recovers it kicked, ahead of any reservation", func() {
			serverStopped = true

			jq2, stop := kickOrderRestartOn(ctx, server, serverConfig, addr, kickedImage, clientConnectTime)
			defer stop()

			recovered, errg := jq2.GetByRepGroup(kickOrderRepGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(recovered), ShouldEqual, 1)
			So(recovered[0].State, ShouldEqual, JobStateReady)
			So(recovered[0].UntilBuried, ShouldEqual, initialUntilBuried(target.Retries))
		})
	})
}

// kickOrderRestartOn stops server, restarts the manager on image as its
// database, as a crash leaving that image would, and returns a client of the
// recovered manager and a function that disconnects it and stops that manager.
func kickOrderRestartOn(ctx context.Context, server *Server, serverConfig ServerConfig, addr string,
	image *bytes.Buffer, clientConnectTime time.Duration) (*Client, func()) {
	server.Stop(ctx, true)

	So(os.WriteFile(serverConfig.DBFile, image.Bytes(), 0o600), ShouldBeNil)

	serverConfig.dontWipeDevDB = true

	recoveredServer, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	stop := func() { recoveredServer.Stop(ctx, true) }

	So(waitUntilRecovered(recoveredServer), ShouldBeTrue)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	return jq, func() {
		disconnect(jq)
		stop()
	}
}
