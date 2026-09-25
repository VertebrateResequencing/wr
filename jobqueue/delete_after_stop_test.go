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
	"os"
	"slices"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// deleteAfterStopGrace is how long the test gives a Stop that does not wait for
// the delete to come back on its own. Past its database close, such a Stop has
// nothing left that takes this long.
const deleteAfterStopGrace = 2 * time.Second

// TestStopWaitsForARemoveOnFailureDelete holds the goroutine that removes a
// buried remove-on-failure job at deleteOnFailureHook while the manager stops.
// A Stop that did not wait for it used to finish, nil the queue, and leave the
// released goroutine to dereference it, crashing the process.
func TestStopWaitsForARemoveOnFailureDelete(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a buried remove-on-failure job whose delete has not run yet", t, func() {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		deleteOnFailureHook = func() {
			select {
			case entered <- struct{}{}:
			default:
			}

			<-release
		}

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		stopStarted := false

		defer func() {
			if !stopStarted {
				server.Stop(ctx, true)
			}

			deleteOnFailureHook = nil
		}()

		released := false
		releaseDelete := func() {
			if !released {
				released = true

				close(release)
			}
		}

		defer releaseDelete()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo delete after stop", Cwd: testCwd, RepGroup: "delete_after_stop", ReqGroup: "delete_after_stop",
			Requirements: standardReqs, Behaviours: Behaviours{&Behaviour{When: OnFailure, Do: Remove}},
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)
		So(jq.Bury(job, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed"), ShouldBeNil)

		So(pollUntil(func() bool {
			select {
			case <-entered:
				return true
			default:
				return false
			}
		}), ShouldBeTrue)

		disconnect(jq)

		Convey("stopping the manager waits for the delete, which removes it, then shuts down cleanly", func() {
			stopped := make(chan struct{})
			stopStarted = true

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			So(pollUntil(func() bool {
				server.krmutex.RLock()
				defer server.krmutex.RUnlock()

				return server.deletesStopped
			}), ShouldBeTrue)

			stoppedBeforeRelease := false

			select {
			case <-stopped:
				stoppedBeforeRelease = true
			case <-time.After(deleteAfterStopGrace):
			}

			releaseDelete()

			stoppedAfterRelease := false

			select {
			case <-stopped:
				stoppedAfterRelease = true
			case <-time.After(30 * time.Second):
			}

			So(stoppedBeforeRelease, ShouldBeFalse)
			So(stoppedAfterRelease, ShouldBeTrue)
			So(liveBucketHas(serverConfig.DBFile, job.Key()), ShouldBeFalse)
		})
	})
}

// TestStopStillRemovesAJobBuriedWhileRunnersDie holds shutdown in
// waitForRunnersToDie, the window in which a runner the shutdown kills buries
// its job, and buries a remove-on-failure job there. The job must still be
// removed, as it was before shutdown waited for such deletes.
func TestStopStillRemovesAJobBuriedWhileRunnersDie(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a running remove-on-failure job and a manager waiting for runners to die", t, func() {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		shutdownRunnersWaitHook = func() {
			select {
			case entered <- struct{}{}:
			default:
			}

			<-release
		}

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		stopped := make(chan struct{})
		stopStarted := false

		defer func() {
			if stopStarted {
				<-stopped
			} else {
				server.Stop(ctx, true)
			}

			shutdownRunnersWaitHook = nil
		}()

		released := false
		releaseShutdown := func() {
			if !released {
				released = true

				close(release)
			}
		}

		defer releaseShutdown()

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo buried while runners die", Cwd: testCwd, RepGroup: "delete_during_stop",
			ReqGroup: "delete_during_stop", Requirements: standardReqs,
			Behaviours: Behaviours{&Behaviour{When: OnFailure, Do: Remove}},
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)
		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		stopStarted = true

		go func() {
			server.Stop(ctx, true)
			close(stopped)
		}()

		So(pollUntil(func() bool {
			select {
			case <-entered:
				return true
			default:
				return false
			}
		}), ShouldBeTrue)

		Convey("burying it then still removes it from the queue and the database", func() {
			So(jq.Bury(job, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "killed"), ShouldBeNil)

			key := job.Key()

			So(pollUntil(func() bool {
				_, errg := server.queueIfPresent().Get(key)
				if errg == nil {
					return false
				}

				live, errr := server.db.recoverIncompleteJobs()
				if errr != nil {
					return false
				}

				return !slices.ContainsFunc(live, func(j *Job) bool { return j.Key() == key })
			}), ShouldBeTrue)

			releaseShutdown()
		})
	})
}

// liveBucketHas reports whether the live bucket of the stopped manager's
// database at path still holds key, which is what a restarted manager would
// recover.
func liveBucketHas(path, key string) bool {
	boltdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	So(err, ShouldBeNil)

	defer func() { So(boltdb.Close(), ShouldBeNil) }()

	found := false

	So(boltdb.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bucketJobsLive).Get([]byte(key)) != nil

		return nil
	}), ShouldBeNil)

	return found
}
