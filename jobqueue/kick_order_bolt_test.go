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

// This file covers changes prepared ahead of a job on the bbolt store, whose
// prepareJobChange encodes the job before the queue's lock is taken and whose
// queueJobChangeAhead and queueJobExit write encodings made before they queue:
// a kick, bury or other change landing between the encode and the queueing
// must not let a stale encoding reach disk. The log store prepares nothing
// (spec, "The store interface": a store may return nil from prepareJobChange)
// and appends each change's state record under the job's lock once the change
// is made, so these windows do not exist there; its ordering of state records
// and exits is covered in store_log_ops_test.go.

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

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
		withBoltStore(&serverConfig)
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
			So(storedLiveJobState(t, server.inspector(), key), ShouldEqual, JobStateReady)
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
		withBoltStore(&serverConfig)
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
		So(storedStateWithin(t, server.inspector(), key, JobStateBuried), ShouldEqual, JobStateBuried)

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
			So(server.db.updateJobAfterChangeDurable(sjob), ShouldBeNil)

			server.db.queueJobChangeAhead(ctx, sjob, second, kickChange)

			// a durable write queued after the second's commits no earlier than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			stored := storedLiveJob(t, server.inspector(), key)
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
		withBoltStore(&serverConfig)
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
		So(storedStateWithin(t, server.inspector(), key, JobStateBuried), ShouldEqual, JobStateBuried)

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

			var (
				changeErr error
				hooked    atomic.Bool
			)

			jobChangeAheadQueueingHook = func(queueing string) {
				if queueing != key || !hooked.CompareAndSwap(false, true) {
					return
				}

				go func() {
					defer close(changed)

					sjob.Lock()
					sjob.Priority = modifiedPriority
					sjob.Unlock()
					changeErr = server.db.updateJobAfterChangeDurable(sjob)
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
			So(changeErr, ShouldBeNil)

			// a durable write queued after the change's commits no earlier than it.
			So(server.db.updateJobAfterChangeDurable(otherJob), ShouldBeNil)

			stored := storedLiveJob(t, server.inspector(), key)
			So(stored.State, ShouldEqual, JobStateReady)
			So(stored.Priority, ShouldEqual, modifiedPriority)
		})
	})
}
