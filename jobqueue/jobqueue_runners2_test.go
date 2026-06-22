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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

var errWaitForJobRunningOrDoneTimeout = errors.New("timed out waiting for job to reach running or terminal state")

// TestJobqueueRunners2 holds the second half of TestJobqueueRunners's
// runner-spawning scenarios. It lives in its own test (and file) purely so the
// two halves run as separate, concurrent `go test` lanes (see the Makefile):
// these scenarios spend most of their wall-clock time waiting on real runner
// subprocesses, so splitting them roughly halves that lane's duration. The
// runner subprocess re-runs the test binary in --runnermode, where every test
// here returns early and TestJobqueueRunners' runner(ctx) does the work.
func TestJobqueueRunners2(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

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
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			count := maxCPU * 2
			jobs := make([]*Job, 0, count)

			for i := 0; i < count; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("sleep 1 && perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: reqGroupPerl, Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 0.5}, Retries: uint8(0), RepGroup: manuallyAdded}) //nolint:lll
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

							running, errj := jq.GetByRepGroup(manuallyAdded, false, 0, JobStateRunning, false, false)
							if errj == nil && len(running) > simultaneous {
								simultaneous = len(running)
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
				So(simultaneous, ShouldBeGreaterThan, maxCPU)

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

				files, err := os.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ranClean := 0
				for range files {
					ranClean++
				}

				So(ranClean, ShouldEqual, count+1) // +1 for the runner exe
			})
		})

		Convey("You can connect, and add some 0 CPU jobs, which are limited by memory", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			tmpdir := t.TempDir()

			maxMem, errp := internal.ProcMeminfoMBs()
			So(errp, ShouldBeNil)

			jobMB := int(math.Floor(float64(maxMem) / float64(maxCPU*2)))

			count := maxCPU * 3
			jobs := make([]*Job, 0, count)

			for i := 0; i < count; i++ {
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

		if maxCPU > 2 {
			Convey("You can connect and add jobs in alternating scheduler groups and they don't pend", func() {
				jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				req1 := &jqs.Requirements{RAM: 10, Time: 4 * time.Second, Cores: 1}
				jobs := []*Job{{Cmd: "echo 1 && sleep 2", Cwd: testCwd, ReqGroup: "req1", Requirements: req1, RepGroup: "a"}}
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err := waitForJobRunningOrDone(jq, &JobEssence{Cmd: "echo 1 && sleep 2"}, 30*time.Second)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				jobs = []*Job{{Cmd: "echo 2 && sleep 2", Cwd: testCwd, ReqGroup: "req2", Requirements: &jqs.Requirements{RAM: 10, Time: 4 * time.Hour, Cores: 1}, RepGroup: "a"}} //nolint:lll
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err = waitForJobRunningOrDone(jq, &JobEssence{Cmd: "echo 2 && sleep 2"}, 30*time.Second)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				jobs = []*Job{{Cmd: "echo 3 && sleep 2", Cwd: testCwd, ReqGroup: "req1", Requirements: req1, RepGroup: "a"}}
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err = waitForJobRunningOrDone(jq, &JobEssence{Cmd: "echo 3 && sleep 2"}, 30*time.Second)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				// let them all complete
				So(waitUntilNoRunners(ctx, server, 30*time.Second), ShouldBeTrue)
			})
		} else {
			SkipConvey("Skipping a test that needs at least 3 cores", func() {})
		}

		if runtime.NumCPU() >= 2 {
			Convey("You can connect, and add 2 real jobs with the same reqs sequentially that run simultaneously", func() {
				jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				defer disconnect(jq)

				jobs := []*Job{{Cmd: fmt.Sprintf("perl -e 'print q[%s2sim%d]; sleep(2);'", runnertmpdir, 1), Cwd: runnertmpdir, ReqGroup: "perl2sim", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: manuallyAdded}} //nolint:lll

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				// wait for this first command to start running
				So(waitForSleepingProc(ctx, server, runnertmpdir+"2sim", 30*time.Second), ShouldBeTrue)

				jobs = []*Job{{Cmd: fmt.Sprintf("perl -e 'print q[%s2sim%d]; sleep(2);'", runnertmpdir, 2), Cwd: runnertmpdir, ReqGroup: "perl2sim", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: manuallyAdded}} //nolint:lll

				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				// wait for the jobs to get run, and while waiting we'll check to
				// see if we get both of our commands running at once
				So(maxSimultaneousSleeping(ctx, server, runnertmpdir+"2sim", 30*time.Second), ShouldEqual, 2)
			})
		}

		Convey("You can connect, and add 2 large batches of jobs sequentially", func() {
			lsfMode := false
			count := 200
			count2 := 50

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

							if fourHundredCount == 0 {
								fourHundredCount = scheduledGroupCount(server, "400:30:1:0")
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
							switch {
							case twoHundredCount > 0 && !server.HasRunners(ctx):
								// check they're really all complete, since the
								// switch to a new job array could leave us with no
								// runners temporarily
								jobs, err = jq.GetByRepGroup(manuallyAdded, false, 0, JobStateComplete, false, false)
								if err == nil && len(jobs) == count+count2 {
									ticker.Stop()

									done <- true

									return
								}
							case twoHundredCount == 0:
								twoHundredCount = scheduledGroupCount(server, "200:30:1:0")
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
				So(twoHundredCount, ShouldBeBetween, fourHundredCount/2, count+count2)

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

				if !lsfMode {
					// we should end up running maxCPU*2 runners, because the first set
					// will be for our given reqs, and the second set will be for when
					// the system learns actual memory usage
					files, err := os.ReadDir(runnertmpdir)
					if err != nil {
						log.Fatal(err)
					}

					// *** we can get up to 2 more than (maxCPU * 2) due to timing
					// issues, but I don't think this is a significant bug...
					So(len(files), ShouldBeBetweenOrEqual, (maxCPU * 2), (maxCPU*2)+2)
				} // *** else under LSF we want to test that we never request more than count+count2 runners...
			}

			batchtest()

			// if possible, we want to repeat these tests with the LSF
			// scheduler, which reveals more issues
			_, err := exec.LookPath("lsadmin")
			if err == nil {
				_, err = exec.LookPath("bqueues")
			}

			privateKeyPath := os.Getenv("WR_LSF_TEST_KEY")
			//nolint:goconst // "true" is an env-var value here, not the REST-form constant
			if err == nil && privateKeyPath != "" && os.Getenv("WR_DISABLE_UNRELIABLE_LSF_TESTS") != "true" {
				lsfMode = true
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

// countSleepingProcs returns the number of currently-sleeping processes whose
// command line contains cmdMatch. The runner-spawning tests use it to observe,
// via real OS process state, how many of their jobs are actually running at
// once.
func countSleepingProcs(cmdMatch string) int {
	pids, err := process.Pids()
	if err != nil {
		return 0
	}

	n := 0

	for _, pid := range pids {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}

		cmd, err := p.Cmdline()
		if err != nil || !strings.Contains(cmd, cmdMatch) {
			continue
		}

		status, err := p.Status()
		if err == nil && slices.Contains(status, process.Sleep) {
			n++
		}
	}

	return n
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

// waitUntilNoRunners polls until the server reports no runners (returning true)
// or maxWait elapses (returning false).
func waitUntilNoRunners(ctx context.Context, server *Server, maxWait time.Duration) bool {
	limit := time.After(maxWait)

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

// waitForSleepingProc polls until at least one sleeping process matches
// cmdMatch (returning true), or the server runs out of runners or maxWait
// elapses (returning false).
func waitForSleepingProc(ctx context.Context, server *Server, cmdMatch string, maxWait time.Duration) bool {
	limit := time.After(maxWait)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if countSleepingProcs(cmdMatch) > 0 {
				return true
			}

			if !server.HasRunners(ctx) {
				return false
			}
		case <-limit:
			return false
		}
	}
}

// maxSimultaneousSleeping polls until the server runs out of runners or maxWait
// elapses, returning the highest number of simultaneously-sleeping processes
// matching cmdMatch that it observed.
func maxSimultaneousSleeping(ctx context.Context, server *Server, cmdMatch string, maxWait time.Duration) int {
	limit := time.After(maxWait)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	maxSeen := 0

	for {
		select {
		case <-ticker.C:
			if n := countSleepingProcs(cmdMatch); n > maxSeen {
				maxSeen = n
			}

			if !server.HasRunners(ctx) {
				return maxSeen
			}
		case <-limit:
			return maxSeen
		}
	}
}
