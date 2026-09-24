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
	"os"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const releaseStdErr = "RELEASESTDBURIEDSTDERR"

// errReleaseStd is the stderr Client.Bury is given; it takes stderr as an error.
var errReleaseStd = errors.New(releaseStdErr)

// TestBuriedJobsStdIsThereOnceItIsBuried is the race behind a CI failure of
// client's TestSchedulerSubmitJobsAndWait: a job's queue item moves to bury
// (which is what `wr add --sync` and AddAndWait wait for) before the manager
// has queued the write of its stderr. A client that asks for the buried job's
// std in between used to find none. Holding the database lock parks the bury
// inside that window for as long as the test needs, so it is reached every time
// rather than one run in hundreds.
func TestBuriedJobsStdIsThereOnceItIsBuried(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a job that is being buried with some stderr", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo release std", Cwd: testCwd, RepGroup: "release_std", ReqGroup: "release_std",
			Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		server.db.Lock()

		dbLocked := true
		unlockDB := func() {
			if dbLocked {
				dbLocked = false
				server.db.Unlock()
			}
		}

		defer unlockDB()

		buried := make(chan error, 1)

		go func() {
			buried <- jq.Bury(job, &JobEndState{Exited: true, Exitcode: 12, EndTime: time.Now()},
				"release std failed", errReleaseStd)
		}()

		So(pollUntil(func() bool { return buryFinalised(server, job.Key()) }), ShouldBeTrue)

		Convey("a client that sees it buried gets its stderr", func() {
			jq2, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			got := make(chan *Job, 1)

			go func() {
				fetched, errg := jq2.GetByEssence(job.ToEssense(), true, false)
				if errg != nil {
					fetched = nil
				}

				got <- fetched
			}()

			// give a fetch that does not wait for the stderr write long enough
			// to come back without it, then let the bury write it.
			var fetched *Job

			select {
			case fetched = <-got:
			case <-time.After(time.Second):
			}

			unlockDB()

			if fetched == nil {
				fetched = <-got
			}

			So(<-buried, ShouldBeNil)
			So(fetched, ShouldNotBeNil)
			So(fetched.State, ShouldEqual, JobStateBuried)

			stderr, errs := fetched.StdErr()
			So(errs, ShouldBeNil)
			So(stderr, ShouldEqual, releaseStdErr)
		})
	})
}

// buryFinalised reports whether the manager has buried key's item and recorded
// the bury on its *Job, which is as far as a bury gets while the database lock
// is held.
func buryFinalised(server *Server, key string) bool {
	item, err := server.q.Get(key)
	if err != nil || item.Stats().State != queue.ItemStateBury {
		return false
	}

	job, ok := item.Data().(*Job)
	if !ok {
		return false
	}

	job.RLock()
	defer job.RUnlock()

	return job.State == JobStateBuried
}
