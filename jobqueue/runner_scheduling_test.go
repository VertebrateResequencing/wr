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
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	log15 "github.com/inconshreveable/log15/v3"
	"github.com/kballard/go-shellquote"
	. "github.com/smartystreets/goconvey/convey"
)

var errWaitForJobRunningOrDoneTimeout = errors.New("timed out waiting for job to reach running or terminal state")

type expectedRunnerWaitLog uint8

const (
	expectedRunnerExitStatus1 expectedRunnerWaitLog = 1 << iota
	expectedRunnerSignalKilled
)

func expectedRunnerWaitCmd(
	cmd string,
	expectedRunners map[string]expectedRunnerWaitLog,
	expectedRunnerMutex *sync.RWMutex,
) (expectedRunnerWaitLog, bool) {
	expectedRunnerMutex.RLock()
	defer expectedRunnerMutex.RUnlock()

	for runnerCmd, expected := range expectedRunners {
		matchesExpectedRunner := strings.HasPrefix(cmd, runnerCmd+" --runnermode ") &&
			strings.Contains(cmd, " --schedgrp '") &&
			strings.Contains(cmd, " --rdeployment ") &&
			strings.Contains(cmd, " --rserver '") &&
			strings.Contains(cmd, " --rdomain ") &&
			strings.Contains(cmd, " --rtimeout ") &&
			strings.Contains(cmd, " --maxmins ") &&
			strings.Contains(cmd, " --rmanagerdir ") &&
			strings.Contains(cmd, " --tmpdir ")

		if matchesExpectedRunner {
			return expected, true
		}
	}

	return 0, false
}

func silenceExpectedRunCmdWaitLogs(t *testing.T) func(string, expectedRunnerWaitLog) {
	t.Helper()

	var expectedRunnerMutex sync.RWMutex

	expectedRunners := make(map[string]expectedRunnerWaitLog)

	previous := clog.GetHandler()
	log15.Root().SetHandler(log15.FilterHandler(func(r log15.Record) bool {
		return !isExpectedRunnerWaitLog(r, expectedRunners, &expectedRunnerMutex)
	}, previous))

	t.Cleanup(func() {
		log15.Root().SetHandler(previous)
	})

	return func(runnerCmd string, expected expectedRunnerWaitLog) {
		expectedRunnerMutex.Lock()
		expectedRunners[runnerCmd] = expected
		expectedRunnerMutex.Unlock()
	}
}

func isExpectedRunnerWaitLog(
	r log15.Record, expectedRunners map[string]expectedRunnerWaitLog, expectedRunnerMutex *sync.RWMutex,
) bool {
	if r.Lvl != log15.LvlError || r.Msg != "runCmd wait" {
		return false
	}

	cmd, ok := logRecordStringValue(r, "cmd")
	if !ok {
		return false
	}

	expected, ok := expectedRunnerWaitCmd(cmd, expectedRunners, expectedRunnerMutex)
	if !ok {
		return false
	}

	errValue, ok := logRecordValue(r, "err")
	if !ok {
		return false
	}

	var exitErr *exec.ExitError

	err, ok := errValue.(error)
	if !ok {
		return false
	}

	if expected&expectedRunnerExitStatus1 != 0 && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true
	}

	return expected&expectedRunnerSignalKilled != 0 && strings.Contains(err.Error(), "signal: killed")
}

func logRecordStringValue(r log15.Record, key string) (string, bool) {
	value, ok := logRecordValue(r, key)
	if !ok {
		return "", false
	}

	str, ok := value.(string)

	return str, ok
}

func logRecordValue(r log15.Record, key string) (any, bool) {
	for i := 0; i+1 < len(r.Ctx); i += 2 {
		if r.Ctx[i] == key {
			return r.Ctx[i+1], true
		}
	}

	return nil, false
}

// TestJobqueueRunnerScheduling covers the runner-spawning scheduler/resource
// scenarios. The suite runs it in two shards because these scenarios spend most
// of their wall-clock time waiting on real runner subprocesses.
func TestJobqueueRunnerScheduling(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	runtime.GOMAXPROCS(runtime.NumCPU())

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	Convey("Once a new jobqueue server is up (part 2)", t, func() {
		serverConfig.Timings.ItemTTR = 10 * time.Second
		serverConfig.Timings.CheckRunnerTime = 10 * time.Second
		serverConfig.Timings.TouchInterval = 50 * time.Millisecond
		runnertmpdir := t.TempDir()

		// our runnerCmd will be running ourselves in --runnermode, so first
		// we'll compile ourselves to the tmpdir
		runnerCmd, err := copyCompiledSelf(filepath.Join(runnertmpdir, "runner"))
		if err != nil {
			log.Fatal(err)
		}

		registerExpectedRunnerWaitLog(runnerCmd, expectedRunnerExitStatus1)

		runningConfig := serverConfig
		rmd := strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment)
		runningConfig.RunnerCmd = runnerCmd +
			" --runnermode --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s" +
			" --rtimeout %d --maxmins %d --rmanagerdir " + rmd + " --tmpdir " + runnertmpdir
		server, _, token, errs := serve(ctx, runningConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		maxCPU := runtime.NumCPU()
		runtime.GOMAXPROCS(maxCPU)

		Convey("You can connect, and add some jobs with fractional CPU requirements", func() {
			if skipInShard("a") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			// Make the jobs BLOCK until the test releases them (rather than a short
			// `sleep 1`) so the simultaneous-running count is deterministic. Each job
			// touches a per-job "started" marker, waits for a shared release file to
			// appear, then writes its output file. Because all count 0.5-core jobs
			// can be scheduled at once (canCount = maxCPU*2 = count) and none finish
			// until released, every job is guaranteed to be RUNNING simultaneously,
			// so the peak running count reaches count (> maxCPU) regardless of how
			// gradually the server spawns the runners. The markers live outside
			// tmpdir so they don't inflate the per-job output-file count below.
			markerDir, err := os.MkdirTemp("", "wr_fractional_cpu_test")
			So(err, ShouldBeNil)

			defer os.RemoveAll(markerDir)

			releaseFile := filepath.Join(markerDir, "release")

			count := maxCPU * 2
			jobs := make([]*Job, 0, count)

			for i := range count {
				cmd := blockUntilReleasedCmd(
					filepath.Join(markerDir, fmt.Sprintf("started.%d", i)),
					releaseFile,
					fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i),
				)
				jobs = append(jobs, &Job{Cmd: cmd, Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0.5}, Retries: uint8(0), RepGroup: manuallyAdded}) //nolint:lll
			}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// Wait (bounded by the generous runnerStartWait, free on the success
				// path) until the peak number of simultaneously-running jobs reaches
				// count. This is deterministic because the jobs block until released:
				// once all count runners have spawned, all count jobs run at once and
				// stay running, so the peak is guaranteed to reach count rather than
				// racing short jobs that finish before the runners finish spawning.
				var simultaneous int

				reachedAllRunning := pollUntilFor(runnerStartWait, func() bool {
					running, errj := jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
					if errj == nil && len(running) > simultaneous {
						simultaneous = len(running)
					}

					return simultaneous >= count
				})
				So(reachedAllRunning, ShouldBeTrue) // we shouldn't have hit our time limit
				So(simultaneous, ShouldBeGreaterThan, maxCPU)

				// Release the jobs so they finish and the runners exit.
				So(os.WriteFile(releaseFile, []byte("go"), 0600), ShouldBeNil)

				// They should now all complete and the runners should all exit
				// (bounded by runnerStartWait, free on the success path).
				So(waitUntilNoRunners(ctx, server), ShouldBeTrue)

				jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, count)

				ran := 0

				for _, job := range jobs {
					files, err := os.ReadDir(job.ActualCwd)
					if err != nil {
						log.Fatal(err)
					}

					for range files {
						ran++
					}
				}

				So(ran, ShouldEqual, count)

				// Confirm the runners exited cleanly and the runner exe is present.
				// Now that the jobs block until released, all count of them run at
				// once, so we expect one runner per job (count "ok" markers); we keep
				// the lenient 1..count range so the check stays robust to scheduler
				// timing. The simultaneous>maxCPU and ran==count checks above already
				// prove the fractional-CPU parallelism and that every job ran.
				assertCleanRunnerMarkers(runnertmpdir, count)
			})
		})

		Convey("You can connect, and add some 0 CPU jobs, which are limited by memory", func() {
			if skipInShard("a") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			maxMem, errp := internal.ProcMeminfoMBs()
			So(errp, ShouldBeNil)

			jobMB := int(math.Floor(float64(maxMem) / float64(maxCPU*2)))

			// As in the fractional-CPU scenario above, make the jobs BLOCK until the
			// test releases them rather than `sleep 1`. With short jobs the peak
			// simultaneous count was a race between how quickly the server spawned
			// runners and how quickly those 1s jobs finished: on a slow or busy box
			// the early jobs completed before the later runners existed, so the peak
			// was an undercount of what memory actually permitted. CI saw peaks of 2
			// and 3 where >=maxCPU was required. Blocking removes the race: nothing
			// finishes until every runner memory allows has spawned, so the peak is
			// exactly the number memory permits.
			markerDir, err := os.MkdirTemp("", "wr_memory_limited_test")
			So(err, ShouldBeNil)

			defer os.RemoveAll(markerDir)

			releaseFile := filepath.Join(markerDir, "release")

			// canRun is how many of these jobs fit in memory at once, which is what
			// this scenario exists to check: each asks for maxMem/(maxCPU*2).
			canRun := maxCPU * 2
			count := maxCPU * 3
			jobs := make([]*Job, 0, count)

			for i := range count {
				cmd := blockUntilReleasedCmd(
					filepath.Join(markerDir, fmt.Sprintf("started.%d", i)),
					releaseFile,
					fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i),
				)
				jobs = append(jobs, &Job{Cmd: cmd, Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: jobMB, Time: 1 * time.Second, Cores: 0}, Retries: uint8(0), Override: 2, RepGroup: manuallyAdded}) //nolint:lll
			}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// Wait (bounded by the generous runnerStartWait, free on the success
				// path) until as many jobs are running at once as memory allows.
				// Deterministic because the jobs block: once canRun runners have
				// spawned, all canRun jobs run and stay running.
				var simultaneous int

				reachedMemoryLimit := pollUntilFor(runnerStartWait, func() bool {
					running, errj := jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
					if errj == nil && len(running) > simultaneous {
						simultaneous = len(running)
					}

					return simultaneous >= canRun
				})
				So(reachedMemoryLimit, ShouldBeTrue)

				// memory, not cores, is the limit here: more than maxCPU jobs run at
				// once, but never more than the canRun that fit in memory.
				So(simultaneous, ShouldBeBetweenOrEqual, maxCPU, canRun)

				// Release the jobs so they finish and the runners exit.
				So(os.WriteFile(releaseFile, []byte("go"), 0600), ShouldBeNil)

				// wait for the jobs to get run
				done := make(chan bool, 1)

				go func() {
					// generous bound for the batch of server-spawned runners to run
					// all the jobs under a CPU-starved box (see runnerStartWait); the
					// loop returns the instant no runners remain, so it is free on
					// the success path.
					limit := time.After(runnerStartWait)
					ticker := time.NewTicker(500 * time.Millisecond)

					for {
						select {
						case <-ticker.C:
							if !server.HasRunners(ctx) {
								ticker.Stop()

								done <- true

								return
							}

							continue
						case <-limit:
							ticker.Stop()

							gjobs, errj := jq.GetByRepGroup(manuallyAdded, false, 0, "", true, false)
							timelimitDebug(gjobs, errj)

							done <- false

							return
						}
					}
				}()

				So(<-done, ShouldBeTrue) // we shouldn't have hit our time limit

				jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, count)

				ran := 0

				for _, job := range jobs {
					files, err := os.ReadDir(job.ActualCwd)
					if err != nil {
						log.Fatal(err)
					}

					for range files {
						ran++
					}
				}

				So(ran, ShouldEqual, count)
			})
		})

		if maxCPU > 2 { //nolint:nestif // Existing scenario gate keeps the low-core skip message beside the test.
			Convey("You can connect and add jobs in alternating scheduler groups and they don't pend", func() {
				if skipInShard("a") {
					return
				}

				jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				req1 := &jqs.Requirements{RAM: 10, Time: 4 * time.Second, Cores: 1}
				jobs := []*Job{{Cmd: "echo 1 && sleep 2", Cwd: testCwd, ReqGroup: "req1", Requirements: req1, RepGroup: "a"}}
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err := waitForJobRunningOrDone(jq, &JobEssence{Cmd: "echo 1 && sleep 2"}, runnerStartWait)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				jobs = []*Job{{Cmd: "echo 2 && sleep 2", Cwd: testCwd, ReqGroup: "req2", Requirements: &jqs.Requirements{RAM: 10, Time: 4 * time.Hour, Cores: 1}, RepGroup: "a"}} //nolint:lll
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err = waitForJobRunningOrDone(jq, &JobEssence{Cmd: "echo 2 && sleep 2"}, runnerStartWait)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				jobs = []*Job{{Cmd: "echo 3 && sleep 2", Cwd: testCwd, ReqGroup: "req1", Requirements: req1, RepGroup: "a"}}
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err = waitForJobRunningOrDone(jq, &JobEssence{Cmd: "echo 3 && sleep 2"}, runnerStartWait)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				// let them all complete
				So(waitUntilNoRunners(ctx, server), ShouldBeTrue)
			})
		} else {
			SkipConvey("Skipping a test that needs at least 3 cores", func() {})
		}

		if runtime.NumCPU() >= 2 {
			Convey("You can connect, and add 2 real jobs with the same reqs sequentially that run simultaneously", func() {
				if skipInShard("b") {
					return
				}

				jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				started1 := filepath.Join(runnertmpdir, "2sim1.started")
				started2 := filepath.Join(runnertmpdir, "2sim2.started")
				done1 := filepath.Join(runnertmpdir, "2sim1.done")
				done2 := filepath.Join(runnertmpdir, "2sim2.done")
				req := &jqs.Requirements{RAM: 1, Time: 2 * time.Minute, Cores: 1}
				cmd1 := concurrentMarkerCmd(started1, started2, done1)
				cmd2 := concurrentMarkerCmd(started2, started1, done2)

				jobs := []*Job{{
					Cmd:          cmd1,
					Cwd:          runnertmpdir,
					ReqGroup:     "concurrent2sim",
					Requirements: req,
					Retries:      uint8(0),
					RepGroup:     manuallyAdded,
				}}

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				So(waitUntilFileExists(started1), ShouldBeTrue)

				jobs = []*Job{{
					Cmd:          cmd2,
					Cwd:          runnertmpdir,
					ReqGroup:     "concurrent2sim",
					Requirements: req,
					Retries:      uint8(0),
					RepGroup:     manuallyAdded,
				}}

				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				So(waitUntilFileExists(started2), ShouldBeTrue)
				So(waitUntilFileExists(done1), ShouldBeTrue)
				So(waitUntilFileExists(done2), ShouldBeTrue)
				So(waitUntilNoRunners(ctx, server), ShouldBeTrue)
			})
		}

		Convey("You can connect, and add 2 batches of jobs sequentially with learned resources", func() {
			if skipInShard("b") {
				return
			}

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			server.setRC(serverRC)

			const (
				firstBatch     = 2
				secondBatch    = 2
				learnedPeakRAM = 100
			)

			tmpdir := t.TempDir()
			reqGroup := "runner_scheduling_learning"
			repGroup := "runner_scheduling_learning_jobs"
			req := &jqs.Requirements{RAM: 300, Time: 1 * time.Second, Cores: 1}
			preLearningGroup := schedulerGroupString(reqForScheduler(req), nil)
			learnedReq := &jqs.Requirements{RAM: learnedPeakRAM, Time: 1 * time.Second, Cores: 1}
			learnedGroup := schedulerGroupString(reqForScheduler(learnedReq), nil)

			So(preLearningGroup, ShouldEqual, "400:30:1:0")
			So(learnedGroup, ShouldEqual, "200:30:1:0")

			archiveGroup := func(group string, count int, expectedRAM int) {
				for range count {
					job, reserveErr := jq.ReserveScheduled(2*time.Second, group)
					So(reserveErr, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.Requirements.RAM, ShouldEqual, expectedRAM)
					So(jq.Started(job, os.Getpid()), ShouldBeNil)
					So(jq.Archive(job, &JobEndState{
						Exited:   true,
						Exitcode: 0,
						PeakRAM:  learnedPeakRAM,
						CPUtime:  time.Second,
						EndTime:  time.Now(),
					}), ShouldBeNil)
				}
			}

			jobs := learningScheduleJobs(tmpdir, reqGroup, repGroup, "batch1", firstBatch, req)
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, firstBatch)
			So(already, ShouldEqual, 0)
			So(waitForScheduledGroupCount(server, preLearningGroup, firstBatch), ShouldBeTrue)
			So(scheduledGroupCount(server, learnedGroup), ShouldEqual, 0)

			archiveGroup(preLearningGroup, firstBatch, req.RAM)

			recRAM, err := server.db.recommendedReqGroupMemory(reqGroup)
			So(err, ShouldBeNil)
			So(recRAM, ShouldEqual, learnedPeakRAM)

			jobs = learningScheduleJobs(tmpdir, reqGroup, repGroup, "batch2", secondBatch, req)
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, secondBatch)
			So(already, ShouldEqual, 0)
			So(waitForScheduledGroupCount(server, learnedGroup, secondBatch), ShouldBeTrue)

			wrongGroupJob, err := jq.ReserveScheduled(25*time.Millisecond, preLearningGroup)
			So(err, ShouldBeNil)
			So(wrongGroupJob, ShouldBeNil)

			archiveGroup(learnedGroup, secondBatch, learnedPeakRAM)

			jobs, err = jq.GetByRepGroup(repGroup, false, 0, JobStateComplete, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, firstBatch+secondBatch)
		})

		Reset(func() {
			if server != nil {
				server.Stop(ctx, true)
			}
		})
	})
}

// blockUntilReleasedCmd builds a shell command for a job that touches startedPath
// (so the test can observe it has begun), then blocks until releasePath exists,
// then runs finalCmd. The wait is capped at a generous number of attempts (a
// safety net so a stray runner cannot hang forever); on the success path the
// test creates releasePath well within that cap, so the loop exits on release.
// This makes a batch of such jobs deterministically all-running-at-once until
// the test releases them, instead of racing short fixed sleeps.
func blockUntilReleasedCmd(startedPath, releasePath, finalCmd string) string {
	releaseMissing := shellquote.Join("test", "!", "-e", releasePath)

	return fmt.Sprintf(
		"%s; i=0; while %s && [ $i -lt 1800 ]; do i=$((i + 1)); sleep 0.1; done; %s",
		shellquote.Join("touch", startedPath),
		releaseMissing,
		finalCmd,
	)
}

// waitForJobRunningOrDone polls until the job starts running, reaches a state
// that means it will not become running, or maxWait elapses.
func waitForJobRunningOrDone(jq *Client, essence *JobEssence, maxWait time.Duration) (*Job, error) {
	limit := time.After(maxWait)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var job *Job

	for {
		got, err := jq.GetByEssence(essence, false, false)
		if err != nil {
			return nil, err
		}

		if got != nil {
			job = got
			if jobStateStopsRunningWait(job.State) {
				return job, nil
			}
		}

		select {
		case <-ticker.C:
		case <-limit:
			if job == nil {
				return nil, fmt.Errorf(
					"%w after %s for job %q: job not found",
					errWaitForJobRunningOrDoneTimeout,
					maxWait,
					essence.Cmd,
				)
			}

			return job, fmt.Errorf(
				"%w after %s for job %q: last state %s",
				errWaitForJobRunningOrDoneTimeout,
				maxWait,
				essence.Cmd,
				job.State,
			)
		}
	}
}

func jobStateStopsRunningWait(state JobState) bool {
	switch state {
	case JobStateRunning, JobStateComplete, JobStateBuried, JobStateLost, JobStateDeleted, JobStateUnknown:
		return true
	default:
		return false
	}
}

// waitUntilNoRunners polls until the server reports no runners (returning true)
// or runnerStartWait elapses (returning false). The bound is generous (see
// runnerStartWait) but free on the success path: it returns the instant no
// runners remain.
func waitUntilNoRunners(ctx context.Context, server *Server) bool {
	limit := time.After(runnerStartWait)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !server.HasRunners(ctx) {
				return true
			}
		case <-limit:
			return false
		}
	}
}

func concurrentMarkerCmd(startedPath, peerStartedPath, donePath string) string {
	peerStartedExists := shellquote.Join("test", "-e", peerStartedPath)

	return fmt.Sprintf(
		"%s; i=0; while [ $i -lt 600 ]; do %s && break; i=$((i + 1)); sleep 0.1; done; %s; %s",
		shellquote.Join("touch", startedPath),
		peerStartedExists,
		peerStartedExists,
		shellquote.Join("touch", donePath),
	)
}

func learningScheduleJobs(cwd, reqGroup, repGroup, label string, count int, req *jqs.Requirements) []*Job {
	jobs := make([]*Job, 0, count)

	for i := range count {
		jobs = append(jobs, &Job{
			Cmd:          fmt.Sprintf("echo %s-%d", label, i),
			Cwd:          cwd,
			ReqGroup:     reqGroup,
			Requirements: req.Clone(),
			Retries:      uint8(3),
			RepGroup:     repGroup,
		})
	}

	return jobs
}

func waitForScheduledGroupCount(server *Server, group string, expected int) bool {
	return pollUntilFor(15*time.Second, func() bool {
		return scheduledGroupCount(server, group) == expected
	})
}

// scheduledGroupCount returns the recorded runner count for a previously-
// scheduled group, or 0 if the server hasn't scheduled that group yet. It takes
// the necessary locks to read the count safely.
func scheduledGroupCount(server *Server, group string) int {
	server.psgmutex.RLock()
	defer server.psgmutex.RUnlock()

	g, existed := server.previouslyScheduledGroups[group]
	if !existed {
		return 0
	}

	g.RLock()
	defer g.RUnlock()

	return g.count
}
