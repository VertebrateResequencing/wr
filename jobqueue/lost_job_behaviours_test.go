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

// This file guards the moment a lost job's behaviours run in the MANAGER.
//
// killJob RELEASES a lost job back to ready before it returns, so a runner can
// reserve the RETRY of that job while the behaviours of the lost run are still
// to come - and the retry's first Touch writes its new working directory onto
// the very same *Job the behaviours were about to be read from. Same job, same
// key, so nothing the workspace resolution proves about a path can tell the two
// runs apart. The only thing that can is triggering the behaviours against the
// state as it was when the job was declared lost.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestLostJobBehavioursActOnTheLostRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const rg = "lost_job_behaviours"

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a lost job whose retry is reserved before its behaviours run", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		cwd := t.TempDir()

		// the `run` behaviour reports where it actually ran, to a file outside
		// the workspace so that the cleanup behaviour cannot take the evidence
		// with it. OnFailure runs before OnExit, so it reports before the sweep.
		ranIn := filepath.Join(cwd, "ran_in.txt")
		job := &Job{
			Cmd: restFormTrue, Cwd: cwd, RepGroup: rg, ReqGroup: rg,
			Requirements: standardReqs, Retries: 3,
			Behaviours: Behaviours{
				{When: OnFailure, Do: Run, Arg: "pwd > " + ranIn},
				{When: OnExit, Do: CleanupAll},
			},
		}

		_, _, err = jq.Add([]*Job{job}, os.Environ(), true)
		So(err, ShouldBeNil)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		key := reserved.Key()
		So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

		item, err := server.q.Get(key)
		So(err, ShouldBeNil)

		live, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		// the lost run: the workspace the real mkHashedDir made for it, reported
		// to the manager the way a Touch reports it, with output in it.
		lostCwd, lostTmp, err := mkHashedDir(cwd, key)
		So(err, ShouldBeNil)

		lostOutput := writeFileIn(lostCwd, "abandoned.txt")
		applyLiveSnapshot(live, &JobEndState{Cwd: lostCwd})

		live.Lock()
		live.Lost = true
		live.Unlock()

		// the retry: a second workspace of the SAME key, since it is the same
		// job, reported onto the same *Job by its first Touch in the window
		// killJob opens by releasing the lost job back to ready.
		var retryCwd, retryTmp, retryOutput string

		lostJobKilledHook = func() {
			lostJobKilledHook = nil

			retryCwd, retryTmp, err = mkHashedDir(cwd, key)
			So(err, ShouldBeNil)

			retryOutput = writeFileIn(retryCwd, "partial.txt")

			applyLiveSnapshot(live, &JobEndState{Cwd: retryCwd})
		}

		Reset(func() { lostJobKilledHook = nil })

		Convey("the behaviours act on the run that was lost, not on the live retry", func() {
			server.killLostJobAndTriggerBehaviours(ctx, key)

			So(retryCwd, ShouldNotBeBlank)
			So(retryCwd, ShouldNotEqual, lostCwd)

			// the retry is running in these; the survival is asserted before
			// anything else, since it is the data loss that matters.
			soPathsExist(retryOutput, retryCwd, retryTmp, filepath.Dir(retryCwd))

			// and the workspace that was really abandoned is the one that goes,
			// rather than being leaked while the live one is swept.
			soPathsGone(lostOutput, lostCwd, lostTmp, filepath.Dir(lostCwd))

			ran, errr := os.ReadFile(ranIn)
			So(errr, ShouldBeNil)
			So(strings.TrimSpace(string(ran)), ShouldEqual, lostCwd)
		})
	})
}

func TestPinBehavioursIsLocked(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Pinning a lost job's behaviours reads its state under the Job's lock", t, func() {
		// the pin is taken in the manager, on the queue's live *Job, while a
		// runner's touches are writing ActualCwd onto it under that same lock
		// (applyLiveSnapshot). An unlocked read here is a data race whose
		// outcome decides which directory the behaviours delete and run in.
		// -race is what makes this test bite.
		const (
			concurrentRounds          = 50
			concurrentWriterAndPinner = 2
		)

		cwd := t.TempDir()
		job := &Job{Cwd: cwd, Cmd: testWSCmd, Behaviours: Behaviours{{When: OnExit, Do: CleanupAll}}}
		actualCwd, _, _ := realWorkSpace(job)

		var wg sync.WaitGroup

		wg.Add(concurrentWriterAndPinner)

		go func() {
			defer wg.Done()

			for range concurrentRounds {
				applyLiveSnapshot(job, &JobEndState{Cwd: actualCwd})
			}
		}()

		go func() {
			defer wg.Done()

			for range concurrentRounds {
				_ = job.pinBehaviours()
			}
		}()

		wg.Wait()

		soPathsExist(cwd)
	})
}
