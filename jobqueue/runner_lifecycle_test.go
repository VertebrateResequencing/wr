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

//nolint:prealloc // Legacy integration tests keep existing allocation shape.
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

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

func TestJobqueueRunnerLifecycle(t *testing.T) {
	ctx := context.Background()

	if servermode {
		return
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	if runnermode {
		// we have a full test of Serve() below that needs a client executable;
		// we say this test script is that exe, and when --runnermode is passed
		// to us we skip all tests and just act like a runner
		runner(ctx)

		return
	}

	registerExpectedRunnerWaitLog := silenceExpectedRunCmdWaitLogs(t)

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	// start these tests anew because these tests have the server spawn runners
	Convey("Once a new jobqueue server is up", t, func() {
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

		registerExpectedRunnerWaitLog(runnerCmd, expectedRunnerSignalKilled)

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

		Convey("You can connect, and add a job and then manually kill both the runner and process", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			// this leaf waits for a killed job to be detected as lost, which
			// happens ~TTR after its last touch. The group-wide TTR (10s) only
			// needs to be that high so jobs survive scheduling load in the other
			// leaves; here we shorten just this server's TTR (it takes effect
			// for jobs queued after this point) to speed up the lost detection.
			server.SetItemTTR(3 * time.Second)

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

			// wait for the job process to start running
			waitForStartedJobPID := func() int {
				limit := time.After(30 * time.Second)

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

			// initially, we force us to fail to be able to check if the job
			// is really dead or not, so that we can test this scenario
			server.SetLostJobCheckTimeout(1 * time.Nanosecond)
			server.SetLostJobCheckRetryTime(lostJobCheckRetry)

			defer func() {
				server.SetLostJobCheckTimeout(5 * time.Second)
				server.SetLostJobCheckRetryTime(1 * time.Hour)
			}()

			pgid, err := syscall.Getpgid(jobPID)
			So(err, ShouldBeNil)

			if err != nil {
				t.Logf("get process group failed for pid %d: %s", jobPID, err)

				return
			}

			err = syscall.Kill(-pgid, syscall.SIGKILL)
			So(err, ShouldBeNil)

			// wait for the job to become lost and then buried
			killed := make(chan bool, 1)
			checkLost := true

			var timeToBury time.Duration

			lostStatePollInterval := 50 * time.Millisecond

			go func() {
				var lostTime time.Time

				limit := time.After(8 * time.Second) // this server's TTR was shortened to 3s above
				ticker := time.NewTicker(lostStatePollInterval)
				markLostJobSeen := func() bool {
					jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateLost, false, false)
					if err != nil || len(jobs) != 1 {
						return false
					}

					checkLost = false
					lostTime = time.Now()

					// re-enable our ability to check the job is really dead
					jobs[0].Lock()
					server.SetLostJobCheckTimeout(5 * time.Second)
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

		Convey("You can connect, and add some jobs where reserved resources depend on override", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
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
				return pollUntil(func() bool {
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

			// add 3 similar jobs that only really differ in override behaviour
			jobs = append(jobs,
				fallocateJob("fallocate -l 200M foo && echo 2", zeroReq, 0, "learns"),
				fallocateJob("fallocate -l 200M foo && echo 3", zeroReq, 2, "learnsDiskNotMem"),
			)
			// following is the main test: specifying Disk of 0 and override 2
			// should result in 0 overriding learned value, even though its a
			// zero value, if DiskSet is true
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

		Convey("You can connect, and add a job that you can kill while it's running", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
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

			// wait for the job to start running
			started := make(chan bool, 1)

			go func() {
				limit := time.After(10 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)

				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateRunning, false, false)
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

			// wait for the job to get killed
			killed := make(chan bool, 1)

			go func() {
				limit := time.After(40 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)

				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
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

						jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", true, false)
						timelimitDebug(jobs, err)

						killed <- false

						return
					}
				}
			}()

			So(<-killed, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonKilled)
			So(jobs[0].Exitcode, ShouldEqual, -1)
		})

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			count := maxCPU * 2
			jobs := make([]*Job, 0, count)

			for i := 0; i < count; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), Override: 2, RepGroup: manuallyAdded}) //nolint:lll
			}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// wait for the jobs to get run
				done := make(chan bool, 1)

				go func() {
					limit := time.After(30 * time.Second)
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

							done <- false

							return
						}
					}
				}()

				So(<-done, ShouldBeTrue) // we shouldn't have hit our time limit

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
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

				// we shouldn't have executed any unnecessary runners, and those
				// we did run should have exited without error, even if there
				// were no more jobs left
				files, err := os.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ranClean := 0
				for range files {
					ranClean++
				}

				So(ranClean, ShouldEqual, maxCPU+1) // +1 for the runner exe
			})
		})

		Reset(func() {
			if server != nil {
				server.Stop(ctx, true)
			}
		})
	})

	// start these tests anew because these tests have the server spawn runners
	// that fail, simulating some network issue
	Convey("Once a new jobqueue server is up with bad runners", t, func() {
		serverConfig.Timings.ItemTTR = 1 * time.Second
		serverConfig.Timings.CheckRunnerTime = 2 * time.Second
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
			" --runnermode --runnerfail --schedgrp '%s' --rdeployment %s --rserver '%s'" +
			" --rdomain %s --rtimeout %d --maxmins %d --rmanagerdir " + rmd +
			" --tmpdir " + runnertmpdir
		server, _, token, errs := serve(ctx, runningConfig)
		So(errs, ShouldBeNil)

		defer func() {
			server.Stop(ctx, true)
		}()

		Convey("You can connect, and add a job", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			var jobs []*Job

			jobs = append(jobs, &Job{Cmd: "true", Cwd: tmpdir, ReqGroup: "true", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(0), Override: uint8(2), RepGroup: manuallyAdded}) //nolint:goconst,lll
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("After some time the manager will have tried to spawn runners more than once", func() {
				runnerCheck := func() (runners int) {
					files, errf := os.ReadDir(runnertmpdir)
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
					limit := time.After(3 * time.Second)
					ticker := time.NewTicker(100 * time.Millisecond)

					for {
						select {
						case <-ticker.C:
							if server.HasRunners(ctx) {
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

				// the failed runner releases its job back to ready, and the
				// manager keeps retrying; poll for these instead of assuming fixed
				// timings, which flake when the box is under heavy load.
				So(pollUntil(func() bool {
					jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateReady, false, false)

					return err == nil && len(jobs) == 1
				}), ShouldBeTrue)

				// the manager spawns (and fails) runners more than once
				So(pollUntil(func() bool { return runnerCheck() >= 2 }), ShouldBeTrue)

				err = server.Drain(ctx)
				So(err, ShouldBeNil)
				So(pollUntil(func() bool { return !server.HasRunners(ctx) }), ShouldBeTrue)
			})
		})

		Reset(func() {
			if server != nil {
				server.Stop(ctx, true)
			}
		})
	})
}
