/*******************************************************************************
 * Copyright (c) 2016-2022, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

type runnerServerOptions struct {
	itemTTR         time.Duration
	checkRunner     time.Duration
	expectedWaitLog expectedRunnerWaitLog
	failRunner      bool
}

type runnerServerFixture struct {
	config            internal.Config
	addr              string
	clientConnectTime time.Duration
	server            *Server
	token             []byte
	runnerTmpDir      string
	maxCPU            int
}

func TestJobqueueRunnerModeEntrypoint(t *testing.T) {
	if servermode {
		return
	}

	if runnermode {
		runner(context.Background())
		os.Exit(0)
	}
}

func TestJobqueueRunnerLostJobs(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	Convey("A killed runner process is detected and buried as lost", t, func() {
		withRunnerServer(t, ctx, registerExpectedRunnerWaitLog, runnerServerOptions{
			itemTTR:         10 * time.Second,
			checkRunner:     10 * time.Second,
			expectedWaitLog: expectedRunnerSignalKilled,
		}, func(fixture runnerServerFixture) {
			jq, err := Connect(
				fixture.addr,
				fixture.config.ManagerCAFile,
				fixture.config.ManagerCertDomain,
				fixture.token,
				fixture.clientConnectTime,
			)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			// This test waits for a killed job to be detected as lost, which
			// happens ~TTR after its last touch. The shared runner setup uses a
			// larger TTR so jobs survive scheduling load; shorten just this
			// server's TTR for the jobs queued below.
			fixture.server.SetItemTTR(3 * time.Second)

			cmd := "perl -e 'for (1..20) { sleep(1) }'"
			jobs := []*Job{{
				Cmd:          cmd,
				Cwd:          testCwd,
				ReqGroup:     reqGroupSleep,
				Requirements: &jqs.Requirements{RAM: 1, Time: 20 * time.Second, Cores: 1},
				Retries:      uint8(0),
				Override:     uint8(2),
				RepGroup:     manuallyAdded,
			}}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			waitForStartedJobPID := func() int {
				// generous bound for a server-spawned runner to start its job under
				// a CPU-starved box (see runnerStartWait); free on success.
				limit := time.After(runnerStartWait)

				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
						if err != nil {
							continue
						}

						if len(jobs) == 1 && jobs[0].Pid > 0 && !jobs[0].StartTime.IsZero() {
							if errp := syscall.Kill(jobs[0].Pid, 0); errp == nil {
								return jobs[0].Pid
							}
						}

					case <-limit:
						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", true, false)
						timelimitDebug(jobs, err)

						return 0
					}
				}
			}
			jobPID := waitForStartedJobPID()
			So(jobPID, ShouldNotEqual, 0)

			if jobPID == 0 {
				return
			}

			jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].Pid, ShouldEqual, jobPID)

			lostJobCheckRetry := 2 * time.Second

			// Initially, force the "is this job really dead?" check to time out
			// so the test observes the lost state before the job is buried.
			fixture.server.SetLostJobCheckTimeout(1 * time.Nanosecond)
			fixture.server.SetLostJobCheckRetryTime(lostJobCheckRetry)

			defer func() {
				fixture.server.SetLostJobCheckTimeout(5 * time.Second)
				fixture.server.SetLostJobCheckRetryTime(1 * time.Hour)
			}()

			pgid, err := syscall.Getpgid(jobPID)
			So(err, ShouldBeNil)

			if err != nil {
				t.Logf("get process group failed for pid %d: %s", jobPID, err)

				return
			}

			err = syscall.Kill(-pgid, syscall.SIGKILL)
			So(err, ShouldBeNil)

			killed := make(chan bool, 1)
			checkLost := true

			var timeToBury time.Duration

			lostStatePollInterval := 50 * time.Millisecond

			go func() {
				var lostTime time.Time

				// generous bound: the server must detect the killed job as lost
				// (~TTR after its last touch) and then bury it; under a CPU-starved
				// box that server-side detection can lag, so allow plenty of
				// headroom. Free on success - returns the instant the job is buried.
				// timeToBury below is measured from lostTime, not this limit, so the
				// retry-timing assertion is unaffected.
				limit := time.After(runnerStartWait)
				ticker := time.NewTicker(lostStatePollInterval)
				markLostJobSeen := func() bool {
					jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateLost, false, false)
					if err != nil || len(jobs) != 1 {
						return false
					}

					checkLost = false
					lostTime = time.Now()

					// Re-enable the real dead-process check once the lost state
					// has been observed.
					jobs[0].Lock()
					fixture.server.SetLostJobCheckTimeout(5 * time.Second)
					jobs[0].Unlock()

					return true
				}

				for {
					select {
					case <-ticker.C:
						if checkLost && !markLostJobSeen() {
							continue
						}

						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateBuried, false, false)
						if err != nil {
							continue
						}

						if len(jobs) == 1 {
							ticker.Stop()

							timeToBury = time.Since(lostTime)

							killed <- true

							return
						}

						continue
					case <-limit:
						ticker.Stop()

						jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", true, false)
						timelimitDebug(jobs, err)

						killed <- false

						return
					}
				}
			}()

			So(<-killed, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonLost)
			So(jobs[0].Exitcode, ShouldEqual, -1)
			So(timeToBury, ShouldBeGreaterThanOrEqualTo, lostJobCheckRetry-(2*lostStatePollInterval))
		})
	})
}

func TestJobqueueRunnerResourceLearning(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	Convey("Runner resource learning honours override modes", t, func() {
		withRunnerServer(
			t, ctx, registerExpectedRunnerWaitLog, defaultRunnerServerOptions(),
			func(fixture runnerServerFixture) {
				jq, err := Connect(
					fixture.addr,
					fixture.config.ManagerCAFile,
					fixture.config.ManagerCertDomain,
					fixture.token,
					fixture.clientConnectTime,
				)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				tmpdir := t.TempDir()

				zeroReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0}
				fallocateJob := func(cmd string, req *jqs.Requirements, override uint8, repGroup string) *Job {
					return &Job{
						Cmd:          cmd,
						Cwd:          tmpdir,
						ReqGroup:     reqGroupFallocate,
						Requirements: req,
						Retries:      uint8(0),
						Override:     override,
						RepGroup:     repGroup,
					}
				}

				jobs := make([]*Job, 0, 5)
				jobs = append(jobs, fallocateJob("fallocate -l 200M foo && echo 1", zeroReq, 2, reqGroupFallocate))
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				waitForCompleteRepGroups := func(repGroups ...string) bool {
					// generous bound: these jobs run via server-spawned runners,
					// which can lag under a CPU-starved box (see runnerStartWait);
					// free on success - returns as soon as all are complete.
					return pollUntilFor(runnerStartWait, func() bool {
						for _, repGroup := range repGroups {
							complete, errj := jq.GetByRepGroup(repGroup, false, 0, JobStateComplete, false, false)
							if errj != nil || len(complete) != 1 {
								return false
							}
						}

						return true
					})
				}

				// Run the first job by itself, so learning occurs (even when disk
				// is 0 and override is 2). Wait for the job state we actually need,
				// not for runner cleanup, which can lag under load.
				So(waitForCompleteRepGroups(reqGroupFallocate), ShouldBeTrue)

				complete, errj := jq.GetByRepGroup(reqGroupFallocate, false, 0, JobStateComplete, false, false)
				So(errj, ShouldBeNil)
				So(len(complete), ShouldEqual, 1)
				So(complete[0].Requirements, ShouldResemble, zeroReq)
				So(complete[0].PeakDisk, ShouldEqual, 200)

				jobs = append(jobs,
					fallocateJob("fallocate -l 200M foo && echo 2", zeroReq, 0, "learns"),
					fallocateJob("fallocate -l 200M foo && echo 3", zeroReq, 2, "learnsDiskNotMem"),
				)
				// Specifying Disk of 0 and override 2 should result in 0
				// overriding learned disk, even though it is a zero value, when
				// DiskSet is true.
				notOverrideReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0, Disk: 0}
				overrideReq := &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0, Disk: 0, DiskSet: true}

				jobs = append(jobs,
					fallocateJob("fallocate -l 200M foo && echo 4", notOverrideReq, 2, "learnsDiskNotMem2"),
					fallocateJob("fallocate -l 200M foo && echo 5", overrideReq, 2, "nolearning"),
				)

				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 4)
				So(already, ShouldEqual, 1)

				So(waitForCompleteRepGroups("learns", "learnsDiskNotMem", "learnsDiskNotMem2", "nolearning"), ShouldBeTrue)

				complete, errj = jq.GetByRepGroup("learns", false, 0, JobStateComplete, false, false)
				So(errj, ShouldBeNil)
				So(len(complete), ShouldEqual, 1)
				So(complete[0].Requirements, ShouldNotResemble, zeroReq)
				So(complete[0].Requirements.Disk, ShouldEqual, 1)
				So(complete[0].Requirements.RAM, ShouldEqual, 100)
				So(complete[0].PeakDisk, ShouldEqual, 200)

				complete, errj = jq.GetByRepGroup("learnsDiskNotMem", false, 0, JobStateComplete, false, false)
				So(errj, ShouldBeNil)
				So(len(complete), ShouldEqual, 1)
				So(complete[0].Requirements, ShouldNotResemble, zeroReq)
				So(complete[0].Requirements.Disk, ShouldEqual, 1)
				So(complete[0].Requirements.RAM, ShouldEqual, 1)
				So(complete[0].PeakDisk, ShouldEqual, 200)

				complete, errj = jq.GetByRepGroup("learnsDiskNotMem2", false, 0, JobStateComplete, false, false)
				So(errj, ShouldBeNil)
				So(len(complete), ShouldEqual, 1)
				So(complete[0].Requirements, ShouldNotResemble, zeroReq)
				So(complete[0].Requirements.Disk, ShouldEqual, 1)
				So(complete[0].Requirements.RAM, ShouldEqual, 1)
				So(complete[0].PeakDisk, ShouldEqual, 200)

				complete, errj = jq.GetByRepGroup("nolearning", false, 0, JobStateComplete, false, false)
				So(errj, ShouldBeNil)
				So(len(complete), ShouldEqual, 1)
				So(complete[0].Requirements, ShouldResemble, overrideReq)
				So(complete[0].PeakDisk, ShouldEqual, 200)
			})
	})
}

func TestJobqueueRunnerKillRequests(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	Convey("Kill requests bury running runner jobs", t, func() {
		withRunnerServer(
			t, ctx, registerExpectedRunnerWaitLog, defaultRunnerServerOptions(),
			func(fixture runnerServerFixture) {
				jq, err := Connect(
					fixture.addr,
					fixture.config.ManagerCAFile,
					fixture.config.ManagerCertDomain,
					fixture.token,
					fixture.clientConnectTime,
				)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				cmd := "perl -e 'for (1..20) { sleep(1) }'"
				jobs := []*Job{{
					Cmd:          cmd,
					Cwd:          testCwd,
					ReqGroup:     reqGroupSleep,
					Requirements: &jqs.Requirements{RAM: 1, Time: 20 * time.Second, Cores: 1},
					Retries:      uint8(0),
					Override:     uint8(2),
					RepGroup:     manuallyAdded,
				}}
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				started := make(chan bool, 1)

				go func() {
					// generous bound: the server spawns runners with a 1s reserve
					// timeout, so a CPU-starved runner can give up before reserving
					// and only retry on the next runner-availability check; wait
					// long enough to span several such cycles. Free on success - this
					// returns the instant the job reaches Running.
					limit := time.After(runnerStartWait)
					ticker := time.NewTicker(50 * time.Millisecond)

					for {
						select {
						case <-ticker.C:
							jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
							if err != nil {
								continue
							}

							if len(jobs) == 1 {
								ticker.Stop()

								started <- true

								return
							}

							continue
						case <-limit:
							ticker.Stop()

							started <- false

							return
						}
					}
				}()

				So(<-started, ShouldBeTrue)
				So(len(jobs), ShouldEqual, 1)

				killCount, err := jq.Kill([]*JobEssence{{JobKey: jobs[0].Key()}})
				So(err, ShouldBeNil)
				So(killCount, ShouldEqual, 1)

				killed := make(chan bool, 1)

				go func() {
					// generous bound: after Kill, the job is buried on the runner's
					// next Touch, which can lag under a CPU-starved box (see
					// runnerStartWait). Free on success.
					limit := time.After(runnerStartWait)
					ticker := time.NewTicker(50 * time.Millisecond)

					for {
						select {
						case <-ticker.C:
							jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateBuried, false, false)
							if err != nil {
								continue
							}

							if len(jobs) == 1 {
								ticker.Stop()

								killed <- true

								return
							}

							continue
						case <-limit:
							ticker.Stop()

							jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", true, false)
							timelimitDebug(jobs, err)

							killed <- false

							return
						}
					}
				}()

				So(<-killed, ShouldBeTrue)

				jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateBuried, false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				So(jobs[0].State, ShouldEqual, JobStateBuried)
				So(jobs[0].FailReason, ShouldEqual, FailReasonKilled)
				So(jobs[0].Exitcode, ShouldEqual, -1)
			})
	})
}

func TestJobqueueRunnerAutomaticExecution(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	Convey("Spawned runners execute queued jobs without over-spawning", t, func() {
		withRunnerServer(
			t, ctx, registerExpectedRunnerWaitLog, defaultRunnerServerOptions(),
			func(fixture runnerServerFixture) {
				jq, err := Connect(
					fixture.addr,
					fixture.config.ManagerCAFile,
					fixture.config.ManagerCertDomain,
					fixture.token,
					fixture.clientConnectTime,
				)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				tmpdir := t.TempDir()

				count := fixture.maxCPU * 2
				jobs := make([]*Job, 0, count)

				for i := range count {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), Override: 2, RepGroup: manuallyAdded}) //nolint:lll
				}

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, count)
				So(already, ShouldEqual, 0)

				// generous bound for the batch of server-spawned runners to finish
				// all the jobs under a CPU-starved box (see runnerStartWait); free
				// on the success path - returns the instant no runners remain.
				So(waitUntilNoRunners(ctx, fixture.server), ShouldBeTrue)

				jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, count)

				ran := 0

				for _, job := range jobs {
					files, errr := os.ReadDir(job.ActualCwd)
					if errr != nil {
						log.Fatal(errr)
					}

					for range files {
						ran++
					}
				}

				So(ran, ShouldEqual, count)

				// The runners we did run should have exited without error even when
				// there were no more jobs left. We can't assert an exact runner count
				// (one runner reserves and runs several of these instant 1-core jobs
				// in sequence before exiting), but the scheduler caps concurrent
				// 1-core runners at maxCPU, so bounding the clean-marker count at
				// maxCPU still enforces this Convey's "without over-spawning" intent
				// while tolerating reuse; the ran==count check above already proves
				// every job ran.
				assertCleanRunnerMarkers(fixture.runnerTmpDir, fixture.maxCPU)
			})
	})
}

func TestJobqueueRunnerFailureRetry(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	Convey("Managers retry after spawned runners fail before reserving", t, func() {
		withRunnerServer(t, ctx, registerExpectedRunnerWaitLog, runnerServerOptions{
			itemTTR:         1 * time.Second,
			checkRunner:     2 * time.Second,
			expectedWaitLog: expectedRunnerExitStatus1,
			failRunner:      true,
		}, func(fixture runnerServerFixture) {
			jq, err := Connect(
				fixture.addr,
				fixture.config.ManagerCAFile,
				fixture.config.ManagerCertDomain,
				fixture.token,
				fixture.clientConnectTime,
			)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			jobs := []*Job{{
				Cmd:          restFormTrue,
				Cwd:          tmpdir,
				ReqGroup:     restFormTrue,
				Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1},
				Retries:      uint8(0),
				Override:     uint8(2),
				RepGroup:     manuallyAdded,
			}}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			runnerCheck := func() (runners int) {
				files, errf := os.ReadDir(fixture.runnerTmpDir)
				if errf != nil {
					log.Fatal(errf)
				}

				ranFailed := 0

				for _, file := range files {
					if !strings.HasPrefix(file.Name(), "fail") {
						continue
					}

					ranFailed++
				}

				return ranFailed
			}

			So(runnerCheck(), ShouldEqual, 0)

			hadRunner := make(chan bool, 1)

			go func() {
				// generous bound for the server to spawn a runner under a
				// CPU-starved box (see runnerStartWait); free on success.
				limit := time.After(runnerStartWait)
				ticker := time.NewTicker(100 * time.Millisecond)

				for {
					select {
					case <-ticker.C:
						if fixture.server.HasRunners(ctx) {
							ticker.Stop()

							hadRunner <- true

							return
						}

						continue
					case <-limit:
						ticker.Stop()

						hadRunner <- false

						return
					}
				}
			}()

			So(<-hadRunner, ShouldBeTrue)

			// The failed runner releases its job back to ready, and the manager
			// keeps retrying; poll for these instead of assuming fixed timings,
			// which flake when the box is under heavy load. These all gate on
			// server-spawned runners failing/re-spawning/exiting, so use the
			// generous runnerStartWait bound (free on the success path).
			So(pollUntilFor(runnerStartWait, func() bool {
				jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateReady, false, false)

				return err == nil && len(jobs) == 1
			}), ShouldBeTrue)

			So(pollUntilFor(runnerStartWait, func() bool { return runnerCheck() >= 2 }), ShouldBeTrue)

			err = fixture.server.Drain(ctx)
			So(err, ShouldBeNil)
			So(pollUntilFor(runnerStartWait, func() bool { return !fixture.server.HasRunners(ctx) }), ShouldBeTrue)
		})
	})
}

func defaultRunnerServerOptions() runnerServerOptions {
	return runnerServerOptions{
		itemTTR:     10 * time.Second,
		checkRunner: 10 * time.Second,
	}
}

// assertCleanRunnerMarkers checks that the server-spawned runners ran the jobs
// and exited cleanly, given a runner tmpdir that holds the copied "runner" exe
// plus one "ok" marker per runner that completed its reserve loop cleanly (see
// the test runner() in jobqueue_test.go) and one "fail" marker per runner that
// exited uncleanly.
//
// It deliberately inspects the marker NAMES rather than asserting an exact entry
// count: the test runner reserves and runs MULTIPLE jobs in one process before
// writing its single "ok" marker, so the number of runners (hence "ok" files)
// is a range of 1..maxJobs, not exactly one-per-job. The caller's own
// assertions on simultaneity and on every job having run already prove the
// parallelism and completion; this only proves "runners ran cleanly and the exe
// is present" without the brittle one-runner-per-job assumption that flakes when
// a runner reuses its process for a second job.
func assertCleanRunnerMarkers(runnerTmpDir string, maxJobs int) {
	files, err := os.ReadDir(runnerTmpDir)
	So(err, ShouldBeNil)

	haveExe := false
	cleanRunners := 0
	failedRunners := 0

	for _, file := range files {
		switch name := file.Name(); {
		case name == "runner":
			haveExe = true
		case strings.HasPrefix(name, "ok"):
			cleanRunners++
		case strings.HasPrefix(name, "fail"):
			failedRunners++
		}
	}

	So(haveExe, ShouldBeTrue)
	So(failedRunners, ShouldEqual, 0)
	So(cleanRunners, ShouldBeBetweenOrEqual, 1, maxJobs)
}

func withRunnerServer(
	t *testing.T,
	ctx context.Context,
	registerExpectedRunnerWaitLog func(string, expectedRunnerWaitLog),
	options runnerServerOptions,
	run func(runnerServerFixture),
) {
	t.Helper()

	runtime.GOMAXPROCS(runtime.NumCPU())

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)
	serverConfig.Timings.ItemTTR = options.itemTTR
	serverConfig.Timings.CheckRunnerTime = options.checkRunner
	serverConfig.Timings.TouchInterval = 50 * time.Millisecond
	runnerTmpDir := t.TempDir()

	runnerCmd, err := copyCompiledSelf(filepath.Join(runnerTmpDir, "runner"))
	So(err, ShouldBeNil)

	if err != nil {
		return
	}

	if options.expectedWaitLog != 0 {
		registerExpectedRunnerWaitLog(runnerCmd, options.expectedWaitLog)
	}

	runningConfig := serverConfig
	rmd := strings.TrimSuffix(config.ManagerDir, "_"+config.Deployment)

	failArg := ""
	if options.failRunner {
		failArg = " --runnerfail"
	}

	runningConfig.RunnerCmd = runnerCmd + " --runnermode" + failArg +
		" --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s" +
		" --rtimeout %d --maxmins %d --rmanagerdir " + rmd + " --tmpdir " + runnerTmpDir
	server, _, token, errs := serve(ctx, runningConfig)
	So(errs, ShouldBeNil)

	if errs != nil {
		return
	}

	defer server.Stop(ctx, true)

	maxCPU := runtime.NumCPU()
	runtime.GOMAXPROCS(maxCPU)

	run(runnerServerFixture{
		config:            config,
		addr:              addr,
		clientConnectTime: clientConnectTime,
		server:            server,
		token:             token,
		runnerTmpDir:      runnerTmpDir,
		maxCPU:            maxCPU,
	})
}
