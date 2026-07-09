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

			count := maxCPU * 3
			jobs := make([]*Job, 0, count)

			for i := range count {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("sleep 1 && perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: jobMB, Time: 1 * time.Second, Cores: 0}, Retries: uint8(0), Override: 2, RepGroup: manuallyAdded}) //nolint:lll
			}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// wait for the jobs to get run
				done := make(chan bool, 1)

				var simultaneous int

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

							running, errj := jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
							if errj == nil && len(running) > simultaneous {
								simultaneous = len(running)
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
				So(simultaneous, ShouldBeBetweenOrEqual, maxCPU, maxCPU*2)

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

		Convey("You can connect, and add 2 large batches of jobs sequentially", func() {
			if skipInShard("b") {
				return
			}

			count := 200
			count2 := 50

			if raceDetectorEnabled {
				// The race detector makes every manager/runner interaction much
				// slower, especially on small CI runners. Keep this scenario above
				// the >100 completions needed to exercise learning, but avoid a
				// 250-job subprocess workload in race mode.
				count = 110
				count2 = 10
			}

			batchtest := func() {
				clientConnectTime = 20 * time.Second // it takes a long time with -race to add 10000 jobs...
				jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				tmpdir := t.TempDir()

				req := &jqs.Requirements{RAM: 300, Time: 1 * time.Second, Cores: 1}

				jobs := make([]*Job, 0, count)
				for i := 0; i < count; i++ {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>batch1.%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: req, Retries: uint8(3), RepGroup: manuallyAdded}) //nolint:lll
				}

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, count)
				So(already, ShouldEqual, 0)

				// wait for 101 of them to complete
				done := make(chan bool, 1)
				fourHundredCount := 0

				go func() {
					// generous give-up cap: the loop returns as soon as the jobs
					// finish (a few seconds normally), so this only fires if they
					// never do. Set high so `make race`, where the
					// race-instrumented job runs are far slower, doesn't time out.
					limit := time.After(600 * time.Second)
					ticker := time.NewTicker(50 * time.Millisecond)

					for {
						select {
						case <-ticker.C:
							jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateComplete, false, false)
							if err != nil {
								continue
							}

							ran := 0

							for _, job := range jobs {
								files, errf := os.ReadDir(job.ActualCwd)
								if errf != nil {
									log.Fatalf("job [%s] had actual cwd %s: %s\n", job.Cmd, job.ActualCwd, errf)
								}

								for range files {
									ran++
								}
							}

							if ran > 100 {
								ticker.Stop()

								done <- true

								return
							}

							// Track the PEAK simultaneously-scheduled count for this
							// group rather than a single early snapshot. The count is
							// the number of this group's jobs that still need a runner;
							// it is highest right after the jobs are added (before any
							// complete) and only falls as they finish. A single
							// snapshot's value therefore depends on exactly when the
							// first tick lands relative to that decay, so under a slow
							// machine it can settle on the assertion boundary; the peak
							// is the stable maximum the group actually reached and
							// cannot land on a transient value.
							if c := scheduledGroupCount(server, "400:30:1:0"); c > fourHundredCount {
								fourHundredCount = c
							}

							continue
						case <-limit:
							ticker.Stop()

							done <- false

							return
						}
					}
				}()

				So(<-done, ShouldBeTrue)
				So(fourHundredCount, ShouldBeBetweenOrEqual, count/2, count)

				// now add a new batch of jobs with the same reqs and reqgroup
				jobs = make([]*Job, 0, count2)
				for i := 0; i < count2; i++ {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>batch2.%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: req, Retries: uint8(3), RepGroup: manuallyAdded}) //nolint:lll
				}

				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, count2)
				So(already, ShouldEqual, 0)

				// wait for all the jobs to get run
				done = make(chan bool, 1)
				twoHundredCount := 0

				go func() {
					// generous give-up cap (see the first batch above): all
					// count+count2 jobs are much slower to finish under -race.
					limit := time.After(1800 * time.Second)
					ticker := time.NewTicker(50 * time.Millisecond)

					for {
						select {
						case <-ticker.C:
							// Track the PEAK simultaneously-scheduled count for the
							// learned "200" group on every tick (see the first batch
							// for why a peak is stable where a single snapshot is not).
							// This group ends up holding jobs that get re-learned to
							// the smaller memory size. The sampled peak can miss jobs
							// that reserve and complete between ticks, so the stable
							// assertion is that we observed this learned group before
							// all jobs completed.
							if c := scheduledGroupCount(server, "200:30:1:0"); c > twoHundredCount {
								twoHundredCount = c
							}

							// only check for completion once we've actually observed
							// the group being scheduled, so the peak above is always
							// captured before we can return
							if twoHundredCount > 0 && !server.HasRunners(ctx) {
								// check they're really all complete, since the
								// switch to a new job array could leave us with no
								// runners temporarily
								jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateComplete, false, false)
								if err == nil && len(jobs) == count+count2 {
									ticker.Stop()

									done <- true

									return
								}
							}

							continue
						case <-limit:
							ticker.Stop()

							done <- false

							return
						}
					}
				}()

				So(<-done, ShouldBeTrue)
				So(twoHundredCount, ShouldBeGreaterThan, 0)
				So(twoHundredCount, ShouldBeLessThanOrEqualTo, count+count2)

				jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, count+count2)

				ran := 0

				for _, job := range jobs {
					files, err := os.ReadDir(job.ActualCwd)
					if err != nil {
						continue
					}

					for range files {
						ran++
					}
				}

				So(ran, ShouldEqual, count+count2)
			}

			batchtest()

			// if possible, we want to repeat these tests with the LSF
			// scheduler, which reveals more issues
			_, err := exec.LookPath("lsadmin")
			if err == nil {
				_, err = exec.LookPath("bqueues")
			}

			privateKeyPath := os.Getenv("WR_LSF_TEST_KEY")
			if err == nil && privateKeyPath != "" && os.Getenv("WR_DISABLE_UNRELIABLE_LSF_TESTS") != restFormTrue {
				count = 10000
				count2 = 1000
				lsfConfig := runningConfig
				lsfConfig.SchedulerName = "lsf"
				lsfConfig.SchedulerConfig = &jqs.ConfigLSF{
					Shell:          config.RunnerExecShell,
					Deployment:     "testing",
					PrivateKeyPath: privateKeyPath,
				}

				server.Stop(ctx, true)
				server, _, token, errs = serve(ctx, lsfConfig)
				So(errs, ShouldBeNil)

				batchtest()
			}
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
