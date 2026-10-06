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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const kickOrderRepGroup = "kick_order"

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
