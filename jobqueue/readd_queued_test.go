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
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// This file covers .docs/bugfixes/260929-readd-overwrites-running-job.md: an add
// with ignoreComplete false (client.Scheduler.SubmitJobs, wr add --rerun) of a
// job that is already in the queue counted as a duplicate in memory, but still
// wrote a fresh, unstarted copy of the job over its live database record. A
// manager that crashed afterwards recovered that copy, put the running job back
// on the ready queue, handed it to a second runner and refused the first
// runner's reports, so the job ran twice at once.

const (
	// readdQueuedRepGroup names the jobs these tests add.
	readdQueuedRepGroup = "readd_queued"

	// readdQueuedParentDepGroup is the dep group of the job that
	// TestReaddQueuedKeepsRecord's dependent job waits for.
	readdQueuedParentDepGroup = "readd_queued_parent"

	// readdQueuedSettle bounds the wait for background database writes to
	// finish before a stored record is read.
	readdQueuedSettle = 10 * time.Second
)

// TestReaddRunningJobSurvivesCrash proves that re-adding a running job with
// ignoreComplete false leaves its stored record alone, so that a manager that
// crashes afterwards recovers it as running, owned by its runner, and does not
// run it again.
func TestReaddRunningJobSurvivesCrash(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a running job re-added with ignoreComplete false, then a manager crash", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		dir := t.TempDir()
		marker := filepath.Join(dir, "runs")
		stopFile := filepath.Join(dir, "stop")
		cmd := startDurabilityCmd(marker, stopFile)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		newJob := func() *Job {
			return &Job{
				Cmd: cmd, Cwd: testCwd, RepGroup: readdQueuedRepGroup,
				ReqGroup: readdQueuedRepGroup, Requirements: standardReqs, Retries: 3,
			}
		}

		inserts, _, err := jq.Add([]*Job{newJob()}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		reserved, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		key := reserved.Key()

		first := exec.CommandContext(ctx, config.RunnerExecShell, "-c", cmd) //nolint:gosec // test-authored command
		So(first.Start(), ShouldBeNil)

		defer func() {
			_ = os.WriteFile(stopFile, nil, 0o600) //nolint:errcheck // best-effort test cleanup
			_ = first.Wait()                       //nolint:errcheck // best-effort test cleanup
		}()

		So(waitForRuns(marker, 1, startDurabilityRerunWait), ShouldBeTrue)
		So(jq.Started(reserved, first.Process.Pid), ShouldBeNil)

		startedRecord := liveJobRecord(server, key)
		So(startedRecord, ShouldNotBeNil)

		inserts, existed, err := jq.Add([]*Job{newJob()}, os.Environ(), false)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 0)
		So(existed, ShouldEqual, 1)

		crashImage := &bytes.Buffer{}
		So(server.BackupDB(crashImage), ShouldBeNil)

		clientID := jq.clientid

		server.Stop(ctx, true)
		disconnect(jq)

		So(os.WriteFile(serverConfig.DBFile, crashImage.Bytes(), 0o600), ShouldBeNil)

		crashedRecord := liveJobRecordInImage(t, crashImage.Bytes(), key)

		serverConfig.dontWipeDevDB = true

		server, _, token, err = serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() { server.Stop(ctx, true) }()

		So(waitUntilRecovered(server), ShouldBeTrue)

		runner, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(runner)

		runner.clientid = clientID

		Convey("it recovers as running, runs once, and its stored record is the started one", func() {
			jq2, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq2)

			recovered, errg := jq2.GetByRepGroup(readdQueuedRepGroup, false, 0, "", false, false)
			So(errg, ShouldBeNil)
			So(len(recovered), ShouldEqual, 1)

			recoveredState := recovered[0].State
			recoveredOwner := recovered[0].ReservedBy

			second, errr := jq2.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)

			if second != nil {
				go func() {
					_ = jq2.Execute(ctx, second, config.RunnerExecShell) //nolint:errcheck // the run count is the assertion
				}()
			}

			rerun := waitForRuns(marker, 2, startDurabilityRerunWait)

			So(runCount(marker), ShouldEqual, 1)
			So(rerun, ShouldBeFalse)
			So(first.Process.Signal(syscall.Signal(0)), ShouldBeNil)
			So(second, ShouldBeNil)
			So(recoveredState, ShouldEqual, JobStateRunning)
			So(recoveredOwner, ShouldEqual, clientID)

			killCalled, errt := runner.Touch(reserved)
			So(errt, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			So(os.WriteFile(stopFile, nil, 0o600), ShouldBeNil)
			So(first.Wait(), ShouldBeNil)

			So(runner.Archive(reserved, &JobEndState{
				Exited: true, Exitcode: 0, EndTime: time.Now(),
			}), ShouldBeNil)

			done, errg := jq2.GetByRepGroup(readdQueuedRepGroup, false, 0, "", true, false)
			So(errg, ShouldBeNil)
			So(len(done), ShouldEqual, 1)
			So(done[0].State, ShouldEqual, JobStateComplete)
			So(runCount(marker), ShouldEqual, 1)

			// the cause: what recovery read was the record of the started job,
			// not a fresh copy the re-add wrote over it.
			So(bytes.Equal(crashedRecord, startedRecord), ShouldBeTrue)
		})
	})
}

// TestReaddQueuedKeepsRecord proves that re-adding with ignoreComplete false a
// job that is in the queue in any state counts it as a duplicate and leaves its
// stored record alone, while re-adding a complete job still re-runs it.
func TestReaddQueuedKeepsRecord(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager with a job in the queue", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ItemTTR = time.Minute

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer func() { server.Stop(ctx, true) }()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		newJob := func(cmd string) *Job {
			return &Job{
				Cmd: cmd, Cwd: testCwd, RepGroup: readdQueuedRepGroup,
				ReqGroup: readdQueuedRepGroup, Requirements: standardReqs, Retries: 3,
			}
		}

		add := func(job *Job, ignoreComplete bool) (int, int) {
			inserts, existed, erra := jq.Add([]*Job{job}, os.Environ(), ignoreComplete)
			So(erra, ShouldBeNil)

			return inserts, existed
		}

		// the state changes before a re-add are also written in the background,
		// so they must all be on disk before the record is compared.
		settledRecord := func(key string) []byte {
			server.db.wg.Wait(readdQueuedSettle)

			return liveJobRecord(server, key)
		}

		readdKeepsRecord := func(job *Job, key string, wantState JobState) {
			before := settledRecord(key)
			So(before, ShouldNotBeNil)

			inserts, existed := add(job, false)
			So(inserts, ShouldEqual, 0)
			So(existed, ShouldEqual, 1)

			So(bytes.Equal(settledRecord(key), before), ShouldBeTrue)

			got, errg := jq.GetByEssence(&JobEssence{JobKey: key}, false, false)
			So(errg, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, wantState)
		}

		reserveStarted := func() *Job {
			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(jq.Started(reserved, os.Getpid()), ShouldBeNil)

			return reserved
		}

		cmd := "echo readd_queued"
		inserts, _ := add(newJob(cmd), true)
		So(inserts, ShouldEqual, 1)

		Convey("a ready one", func() {
			job := newJob(cmd)
			readdKeepsRecord(job, job.Key(), JobStateReady)
		})

		Convey("a reserved one", func() {
			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			readdKeepsRecord(newJob(cmd), reserved.Key(), JobStateReserved)
		})

		Convey("a running one", func() {
			reserved := reserveStarted()

			readdKeepsRecord(newJob(cmd), reserved.Key(), JobStateRunning)
		})

		Convey("a delayed one", func() {
			reserved := reserveStarted()
			So(jq.Release(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed"), ShouldBeNil)

			readdKeepsRecord(newJob(cmd), reserved.Key(), JobStateDelayed)
		})

		Convey("a buried one", func() {
			reserved := reserveStarted()
			So(jq.Bury(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, "failed"), ShouldBeNil)

			readdKeepsRecord(newJob(cmd), reserved.Key(), JobStateBuried)
		})

		Convey("a dependent one", func() {
			parent := newJob("echo readd_queued_parent")
			parent.DepGroups = []string{readdQueuedParentDepGroup}

			child := newJob("echo readd_queued_child")
			child.Dependencies = Dependencies{NewDepGroupDependency(readdQueuedParentDepGroup)}

			inserts, _ = add(parent, true)
			So(inserts, ShouldEqual, 1)
			inserts, _ = add(child, true)
			So(inserts, ShouldEqual, 1)

			readdChild := newJob("echo readd_queued_child")
			readdChild.Dependencies = Dependencies{NewDepGroupDependency(readdQueuedParentDepGroup)}

			readdKeepsRecord(readdChild, readdChild.Key(), JobStateDependent)
		})

		Convey("a complete one is re-run, with a fresh record", func() {
			reserved := reserveStarted()
			key := reserved.Key()
			So(jq.Archive(reserved, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()}), ShouldBeNil)
			So(liveJobRecord(server, key), ShouldBeNil)

			inserts, existed := add(newJob(cmd), false)
			So(inserts, ShouldEqual, 1)
			So(existed, ShouldEqual, 0)

			So(liveJobRecord(server, key), ShouldNotBeNil)

			got, errg := jq.GetByEssence(&JobEssence{JobKey: key}, false, false)
			So(errg, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.State, ShouldEqual, JobStateReady)
		})
	})
}

// TestStoreNewJobsKeepsHandedOutRecord proves that the database write of an add
// never replaces the live record of a job that has been handed out, which is
// what keeps the stored record right when two adds of the same job race and the
// first one's job is reserved before the second one's write lands. A complete
// job's live record, which only a dependency re-run leaves there, is still
// replaced, since that is how the job is re-run.
func TestStoreNewJobsKeepsHandedOutRecord(t *testing.T) {
	ctx := context.Background()

	Convey("Given a database holding a job's live record", t, func() {
		database := openReliable4WriteStormDB(t, ctx)
		defer func() { _ = database.close(ctx) }()

		freshJob := func() *Job {
			job := testDBJob("echo readd_queued_store", readdQueuedRepGroup)
			job.State = JobStateReady

			return job
		}

		_, _, _, err := database.storeNewJobs(ctx, []*Job{freshJob()}, false)
		So(err, ShouldBeNil)

		key := freshJob().Key()

		recordOf := func() []byte {
			var record []byte

			errv := database.bolt.View(func(tx *bolt.Tx) error {
				record = bytes.Clone(tx.Bucket(bucketJobsLive).Get([]byte(key)))

				return nil
			})
			So(errv, ShouldBeNil)
			So(record, ShouldNotBeNil)

			return record
		}

		progress := func(state JobState) []byte {
			job := freshJob()
			job.State = state
			job.Attempts = 1
			job.StartTime = time.Now()
			job.Pid = 1
			job.Host = reserveDurabilityHost

			So(database.updateJobAfterChangeDurable(job), ShouldBeNil)

			return recordOf()
		}

		for _, state := range []JobState{
			JobStateReserved, JobStateRunning, JobStateLost, JobStateDelayed, JobStateBuried,
		} {
			Convey("an add does not replace it once it is "+string(state), func() {
				before := progress(state)

				_, _, _, err = database.storeNewJobs(ctx, []*Job{freshJob()}, false)
				So(err, ShouldBeNil)
				So(bytes.Equal(recordOf(), before), ShouldBeTrue)
			})
		}

		Convey("an add replaces it when it is a complete job's, so re-running it", func() {
			before := progress(JobStateComplete)

			_, _, _, err = database.storeNewJobs(ctx, []*Job{freshJob()}, false)
			So(err, ShouldBeNil)

			after := recordOf()
			So(bytes.Equal(after, before), ShouldBeFalse)

			stored, errd := database.decodeJob(after)
			So(errd, ShouldBeNil)
			So(stored.State, ShouldEqual, JobStateReady)
			So(stored.Attempts, ShouldEqual, 0)
		})
	})
}

// liveJobRecord returns a copy of the given job's encoded record in server's
// live bucket, or nil if it has none.
func liveJobRecord(server *Server, key string) []byte {
	var record []byte

	err := server.db.bolt.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketJobsLive).Get([]byte(key)); v != nil {
			record = bytes.Clone(v)
		}

		return nil
	})
	So(err, ShouldBeNil)

	return record
}

// liveJobRecordInImage returns a copy of the given job's encoded record in the
// live bucket of the given database image, or nil if it has none.
func liveJobRecordInImage(t *testing.T, image []byte, key string) []byte {
	t.Helper()

	path := filepath.Join(t.TempDir(), "image.db")
	So(os.WriteFile(path, image, 0o600), ShouldBeNil)

	boltdb, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	So(err, ShouldBeNil)

	defer func() { _ = boltdb.Close() }()

	var record []byte

	err = boltdb.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketJobsLive).Get([]byte(key)); v != nil {
			record = bytes.Clone(v)
		}

		return nil
	})
	So(err, ShouldBeNil)

	return record
}
