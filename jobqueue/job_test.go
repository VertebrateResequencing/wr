/*******************************************************************************
 * Copyright (c) 2021-2022, 2026 Genome Research Ltd.
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
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/dgryski/go-farm"
	. "github.com/smartystreets/goconvey/convey"
)

// testCwdPath is a fake absolute Cwd used by Job tests that don't actually run.
const testCwdPath = "/cwd"

// testTrueCmd is the no-op shell command used as a Job Cmd in tests.
const testTrueCmd = "true"

// testMountPath is a fake S3 target Path used when building MountConfigs in Job
// key tests.
const testMountPath = "path"

// testOnFailureCmd is the Cmd of an OnFailure Run Behaviour in Job
// modification tests.
const testOnFailureCmd = "echo failed"

// testOnExitCmd is the Cmd of a pre-existing OnExit Run Behaviour in Job
// modification tests.
const testOnExitCmd = "echo old"

// liveStatusCwd is the Cwd of the Job that liveStatusJob() returns.
const liveStatusCwd = "/tmp/wr"

func TestJobEnv(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Job env distinguishes absent and explicitly empty stored environments", t, func() {
		t.Setenv("WR_JOBQUEUE_ENV_FALLBACK", "present")

		Convey("a requested job with no stored env uses the current environment", func() {
			env, err := (&Job{EnvCRetrieved: true}).Env()
			So(err, ShouldBeNil)
			So(env, ShouldContain, "WR_JOBQUEUE_ENV_FALLBACK=present")
		})

		Convey("a stored nil env uses the current environment", func() {
			envc, err := compressEnv(nil)
			So(err, ShouldBeNil)

			env, err := (&Job{EnvC: envc, EnvCRetrieved: true}).Env()
			So(err, ShouldBeNil)
			So(env, ShouldContain, "WR_JOBQUEUE_ENV_FALLBACK=present")
		})

		Convey("a stored empty env remains empty", func() {
			envc, err := compressEnv([]string{})
			So(err, ShouldBeNil)

			env, err := (&Job{EnvC: envc, EnvCRetrieved: true}).Env()
			So(err, ShouldBeNil)
			So(env, ShouldResemble, []string{})
		})
	})
}

func TestJobModifierBehaviours(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Modifying a Job's Behaviours replaces them per trigger", t, func() {
		Convey("every behaviour supplied for a trigger is kept, in order", func() {
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath}

			jm := NewJobModifer()
			jm.SetBehaviours(Behaviours{
				{When: OnExit, Do: Run, Arg: "echo x"},
				{When: OnExit, Do: CleanupAll},
			})
			jm.applyTo(job)

			So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"run":"echo x"},{"cleanup_all":true}]}`)
		})

		Convey("a supplied trigger replaces all the Job's existing behaviours for it", func() {
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, Behaviours: Behaviours{
				{When: OnExit, Do: Run, Arg: "echo old1"},
				{When: OnExit, Do: Run, Arg: "echo old2"},
			}}

			jm := NewJobModifer()
			jm.SetBehaviours(Behaviours{{When: OnExit, Do: Run, Arg: "echo new"}})
			jm.applyTo(job)

			So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"run":"echo new"}]}`)
		})

		Convey("triggers the modification does not mention are left untouched", func() {
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, Behaviours: Behaviours{
				{When: OnFailure, Do: Run, Arg: testOnFailureCmd},
				{When: OnExit, Do: Run, Arg: testOnExitCmd},
			}}

			jm := NewJobModifer()
			jm.SetBehaviours(Behaviours{{When: OnExit, Do: CleanupAll}})
			jm.applyTo(job)

			So(job.Behaviours.String(), ShouldEqual,
				`{"on_failure":[{"run":"echo failed"}],"on_exit":[{"cleanup_all":true}]}`)
		})

		Convey("a trigger the Job does not have yet is added", func() {
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, Behaviours: Behaviours{
				{When: OnFailure, Do: Run, Arg: testOnFailureCmd},
			}}

			jm := NewJobModifer()
			jm.SetBehaviours(Behaviours{
				{When: OnSuccess, Do: Cleanup},
				{When: OnSuccess, Do: Run, Arg: "echo done"},
			})
			jm.applyTo(job)

			So(job.Behaviours.String(), ShouldEqual,
				`{"on_failure":[{"run":"echo failed"}],`+
					`"on_success":[{"cleanup":true},{"run":"echo done"}]}`)
		})

		Convey("a Nothing behaviour turns off the Job's behaviours for its trigger", func() {
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, Behaviours: Behaviours{
				{When: OnExit, Do: CleanupAll},
				{When: OnExit, Do: Run, Arg: testOnExitCmd},
			}}

			jm := NewJobModifer()
			jm.SetBehaviours(BehavioursViaJSON{{Nothing: true}}.Behaviours(OnExit))
			jm.applyTo(job)

			So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"nothing":true}]}`)
		})

		Convey("Behaviours that were not set are not altered", func() {
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, Behaviours: Behaviours{
				{When: OnExit, Do: Run, Arg: testOnExitCmd},
			}}

			jm := NewJobModifer()
			jm.SetPriority(7)
			jm.applyTo(job)

			So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"run":"echo old"}]}`)
		})
	})
}

func TestJobModifierActualCwd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// ActualCwd is the path mkHashedDir built below Cwd from the Job's Key(), and
	// cleanup recognises it by rebuilding it from that key, so a modification that
	// changes the key must not leave the old path behind: a stored path the current
	// definition cannot build is one wr can prove nothing about.
	Convey("A modification that changes a Job's key clears its ActualCwd", t, func() {
		const ran = testCwdPath + "/wr_cwd/a/b/c/uniq0/cwd"

		newJob := func() *Job {
			return &Job{Cmd: testTrueCmd, Cwd: testCwdPath, ActualCwd: ran}
		}

		for _, tc := range []struct {
			name   string
			modify func(*JobModifier)
		}{
			{"the Cmd", func(jm *JobModifier) { jm.SetCmd("echo something else") }},
			{"the MountConfigs", func(jm *JobModifier) {
				jm.SetMountConfigs(MountConfigs{{Mount: "mnt", Targets: []MountTarget{{Path: "s3/path"}}}})
			}},
			{"the container image", func(jm *JobModifier) { jm.SetWithDocker("ubuntu:latest") }},
			{"the Cwd", func(jm *JobModifier) { jm.SetCwd("/elsewhere") }},
			{"CwdMatters", func(jm *JobModifier) { jm.SetCwdMatters(true) }},
		} {
			Convey("modifying "+tc.name+" clears it", func() {
				job := newJob()

				jm := NewJobModifer()
				tc.modify(jm)
				jm.applyTo(job)

				So(job.ActualCwd, ShouldBeBlank)
			})
		}

		Convey("but a modification that leaves the key alone keeps it", func() {
			// clearing it costs the user a leaked workspace, so it is only done
			// where the stored path has actually stopped describing the Job.
			job := newJob()

			jm := NewJobModifer()
			jm.SetPriority(7)
			jm.applyTo(job)

			So(job.ActualCwd, ShouldEqual, ran)
		})

		Convey("and so does one that sets a key field to the value it already had", func() {
			job := newJob()

			jm := NewJobModifer()
			jm.SetCmd(testTrueCmd)
			jm.applyTo(job)

			So(job.ActualCwd, ShouldEqual, ran)
		})

		Convey("a container image change clears it even though the Cmd is untouched", func() {
			// WithDocker, WithSingularity and ContainerMounts all reach Key()
			// without being a Cmd or a Cwd.
			job := newJob()
			job.WithSingularity = "ubuntu.sif"

			jm := NewJobModifer()
			jm.SetContainerMounts("/data:/data")
			jm.applyTo(job)

			So(job.ActualCwd, ShouldBeBlank)
		})

		Convey("--cwd_matters on a Job that already had it clears the v0.37 poison", func() {
			// such a Job's key does not change, but wr v0.37.0|1 persisted it
			// with ActualCwd set to Cwd, and this is where a user clears that.
			job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, CwdMatters: true, ActualCwd: testCwdPath}

			jm := NewJobModifer()
			jm.SetCwdMatters(true)
			jm.applyTo(job)

			So(job.ActualCwd, ShouldBeBlank)
		})
	})
}

func TestJobMountBaseDirs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const cwd = "/some/cwd"

	workSpace := cwd + "/wr_cwd/a/b/c/uniq"
	ranIn := workSpace + "/cwd"

	for _, tc := range []struct {
		name          string
		job           *Job
		onCwd         []bool
		wantCwd       string
		wantMount     string
		wantCacheBase string
	}{
		{
			name:          "a job that ran in a wr-created working directory mounts there",
			job:           &Job{Cwd: cwd, ActualCwd: ranIn},
			wantCwd:       ranIn,
			wantMount:     ranIn,
			wantCacheBase: workSpace,
		},
		{
			name:          "an ActualCwd takes precedence over onCwd",
			job:           &Job{Cwd: cwd, ActualCwd: ranIn},
			onCwd:         []bool{true},
			wantCwd:       ranIn,
			wantMount:     ranIn,
			wantCacheBase: workSpace,
		},
		{
			name:          "a cwd_matters job mounts in cwd's mnt subdirectory, cached in cwd",
			job:           &Job{Cwd: cwd, CwdMatters: true},
			wantCwd:       cwd,
			wantMount:     cwd + "/mnt",
			wantCacheBase: cwd,
		},
		{
			name:          "a cwd_matters job with an ActualCwd poisoned by wr v0.37.0|1 caches in cwd too",
			job:           &Job{Cwd: cwd, CwdMatters: true, ActualCwd: cwd},
			wantCwd:       cwd,
			wantMount:     cwd + "/mnt",
			wantCacheBase: cwd,
		},
		{
			name:          "a job that has not run yet mounts in cwd's mnt subdirectory too",
			job:           &Job{Cwd: cwd},
			wantCwd:       cwd,
			wantMount:     cwd + "/mnt",
			wantCacheBase: cwd,
		},
		{
			name:          "onCwd without an ActualCwd mounts on cwd itself, cached in its parent",
			job:           &Job{Cwd: cwd},
			onCwd:         []bool{true},
			wantCwd:       cwd,
			wantMount:     cwd,
			wantCacheBase: "/some",
		},
	} {
		Convey("mountBaseDirs says "+tc.name, t, func() {
			gotCwd, gotMount, gotCacheBase := tc.job.mountBaseDirs(tc.onCwd)
			So(gotCwd, ShouldEqual, tc.wantCwd)
			So(gotMount, ShouldEqual, tc.wantMount)
			So(gotCacheBase, ShouldEqual, tc.wantCacheBase)
		})
	}
}

func TestJobUnmount(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a cwd_matters job with a mount and an ActualCwd poisoned by wr v0.37.0|1", t, func() {
		cwd := t.TempDir()
		job := &Job{Cwd: cwd, CwdMatters: true, ActualCwd: cwd, MountConfigs: MountConfigs{{}}}

		Convey("Unmount() deletes nothing and doesn't complain about the user's own cwd", func() {
			logs, err := job.Unmount()
			So(err, ShouldBeNil)
			So(logs, ShouldBeBlank)
			soPathsExist(cwd, filepath.Dir(cwd))
		})
	})
}

func TestJob(t *testing.T) {
	if runnermode || servermode {
		return
	}

	cm := "/out:/in"
	image := "alpine"

	Convey("Key() depends on Cmd", t, func() {
		job1 := &Job{Cmd: "a", Cwd: "cwd/1"}
		job2 := &Job{Cmd: "b", Cwd: "cwd/1"}
		job3 := &Job{Cmd: "a", Cwd: "cwd/2"}

		So(job1.Key(), ShouldNotEqual, job2.Key())
		So(job1.Key(), ShouldEqual, job3.Key())
		So(job1.Key(), ShouldEqual, "4d846ed67258e4c39a4840eea4d851dd")

		Convey("and Cwd if CwdMatters", func() {
			job1.CwdMatters = true
			So(job1.Key(), ShouldNotEqual, job3.Key())
			So(job1.Key(), ShouldEqual, "05555567897fdfbd3d83cbe37a533712")

			job3.CwdMatters = true
			So(job1.Key(), ShouldNotEqual, job3.Key())
		})

		Convey("and MountConfigs", func() {
			mcs := MountConfigs{{Targets: []MountTarget{{Path: testMountPath}}}}
			job1.MountConfigs = mcs
			So(job1.Key(), ShouldNotEqual, job3.Key())
			So(job1.Key(), ShouldEqual, "a95a914ccb411f268502f5bff81bdfca")

			job3.MountConfigs = mcs
			So(job1.Key(), ShouldEqual, job3.Key())

			Convey("which is also affected by CwdMatters", func() {
				job1.CwdMatters = true
				So(job1.Key(), ShouldNotEqual, job3.Key())
				So(job1.Key(), ShouldEqual, "e496fa460912bdd24d854d44ee540fd9")
			})
		})

		Convey("but not on ContainerMounts if not using a container", func() {
			job1.ContainerMounts = cm
			So(job1.Key(), ShouldEqual, job3.Key())
		})

		Convey("and WithDocker", func() {
			job1.WithDocker = image
			So(job1.Key(), ShouldNotEqual, job3.Key())
			So(job1.Key(), ShouldEqual, "ae87ca2898ee15157db9804718368723")

			job3.WithDocker = image
			So(job1.Key(), ShouldEqual, job3.Key())

			Convey("which is also affected by CwdMatters", func() {
				job1.CwdMatters = true
				So(job1.Key(), ShouldNotEqual, job3.Key())
				So(job1.Key(), ShouldEqual, "7c1d8eb670b811be0556de67da115e0a")
			})

			Convey("which is also affected by ContainerMounts", func() {
				job1.ContainerMounts = cm
				So(job1.Key(), ShouldNotEqual, job3.Key())
				So(job1.Key(), ShouldEqual, "2547e3f2bcadaf437828a51fa7301145")

				job3.ContainerMounts = cm
				So(job1.Key(), ShouldEqual, job3.Key())

				Convey("which is also affected by CwdMatters", func() {
					job1.CwdMatters = true
					So(job1.Key(), ShouldNotEqual, job3.Key())
					So(job1.Key(), ShouldEqual, "f28a8cbf3dd1151d37258c66e3033b7e")
				})
			})
		})

		Convey("and WithSingularity", func() {
			job1.WithSingularity = image
			So(job1.Key(), ShouldNotEqual, job3.Key())
			So(job1.Key(), ShouldEqual, "c5ea91079a8270931c3627ff56859482")

			job3.WithSingularity = image
			So(job1.Key(), ShouldEqual, job3.Key())

			job3.WithDocker = image
			So(job1.Key(), ShouldNotEqual, job3.Key())
		})
	})

	Convey("CmdLine() returns Cmd", t, func() {
		ctx := context.Background()
		job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath}
		cmd, cleanup, err := job.CmdLine(ctx)
		So(err, ShouldBeNil)
		So(cmd, ShouldEqual, testTrueCmd)
		So(cleanup, ShouldNotBeNil)
		So(job.MonitorDocker, ShouldBeBlank)

		Convey("Though with WithDocker it returns a docker run command", func() {
			job.WithDocker = image

			cmd, cleanup, err = job.CmdLine(ctx)
			So(err, ShouldBeNil)
			So(cleanup, ShouldNotBeNil)

			defer cleanup()

			So(cmd, ShouldStartWith, "cat ")

			dockerPrefix := " | docker run --rm --name %s -w $PWD --mount type=bind,source=$PWD,target=$PWD"
			dockerSuffix := " -i %s /bin/sh"
			So(cmd, ShouldEndWith, fmt.Sprintf(dockerPrefix+dockerSuffix, job.Key(), image))
			So(job.MonitorDocker, ShouldEqual, job.Key())

			Convey("That can include additional mounts and env vars", func() {
				job.ContainerMounts = "/foo/bar:/bar,/foo/baz:/baz"
				So(job.EnvAddOverride([]string{"FOO=bar", "OOF=rab"}), ShouldBeNil)

				cmd, cleanup, err = job.CmdLine(ctx)
				So(err, ShouldBeNil)
				So(cleanup, ShouldNotBeNil)

				defer cleanup()

				dockerExtra := " --mount type=bind,source=/foo/bar,target=/bar" +
					" --mount type=bind,source=/foo/baz,target=/baz"
				dockerEnv1 := " -e FOO=bar -e OOF=rab"
				dockerEnv2 := " -e OOF=rab -e FOO=bar"

				exp1 := fmt.Sprintf(dockerPrefix+dockerExtra+dockerEnv1+dockerSuffix, job.Key(), image)
				exp2 := fmt.Sprintf(dockerPrefix+dockerExtra+dockerEnv2+dockerSuffix, job.Key(), image)

				if strings.HasSuffix(cmd, exp1) {
					So(cmd, ShouldEndWith, exp1)
				} else {
					So(cmd, ShouldEndWith, exp2)
				}
			})
		})

		Convey("Though with WithSingularity it returns a singularity shell command", func() {
			job.WithSingularity = image

			cmd, cleanup, err = job.CmdLine(ctx)
			So(err, ShouldBeNil)
			So(cleanup, ShouldNotBeNil)

			defer cleanup()

			So(cmd, ShouldStartWith, "cat ")
			So(cmd, ShouldEndWith, " | singularity shell "+image)
			So(job.MonitorDocker, ShouldBeBlank)

			Convey("That can include additional mounts", func() {
				job.ContainerMounts = "/foo/bar:/bar,/foo/baz:/baz"
				So(job.EnvAddOverride([]string{"FOO=bar", "OOF=rab"}), ShouldBeNil)

				cmd, cleanup, err = job.CmdLine(ctx)
				So(err, ShouldBeNil)
				So(cleanup, ShouldNotBeNil)

				defer cleanup()

				So(cmd, ShouldEndWith, " | singularity shell -B /foo/bar:/bar -B /foo/baz:/baz "+image)
			})
		})
	})

	Convey("ToStatus() reports start and end times as Unix nanoseconds", t, func() {
		started := time.Unix(100, 123456789)
		ended := started.Add(25 * time.Millisecond)
		job := &Job{
			Cmd:                 testTrueCmd,
			Cwd:                 testCwdPath,
			Requirements:        &scheduler.Requirements{RAM: 1, Time: time.Second, Cores: 1},
			WaitingForDepGroups: []string{futureDepGroup},
			StartTime:           started,
			EndTime:             ended,
		}

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.Started, ShouldNotBeNil)
		So(status.Ended, ShouldNotBeNil)

		if status.Started == nil || status.Ended == nil {
			return
		}

		So(*status.Started, ShouldEqual, started.UnixNano())
		So(*status.Ended, ShouldEqual, ended.UnixNano())
		So(*status.Ended, ShouldBeGreaterThan, *status.Started)
		So(status.WaitingForDepGroups, ShouldResemble, []string{futureDepGroup})
	})

	Convey("ToStatus() reports never-seen dependency group waits", t, func() {
		job := &Job{
			Cmd:                 testTrueCmd,
			Cwd:                 testCwdPath,
			Requirements:        &scheduler.Requirements{RAM: 1, Time: time.Second, Cores: 1},
			DepGroups:           []string{testCarrierDepGroup},
			WaitingForDepGroups: []string{futureDepGroup},
			State:               JobStateDependent,
		}

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, JobStateDependent)
		So(status.DepGroups, ShouldResemble, []string{testCarrierDepGroup})
		So(status.WaitingForDepGroups, ShouldResemble, []string{futureDepGroup})
	})

	Convey("ToStatus() safely reports env overrides while they change", t, func() {
		job := &Job{
			Cmd:          testTrueCmd,
			Cwd:          testCwdPath,
			Requirements: &scheduler.Requirements{RAM: 1, Time: time.Second, Cores: 1},
			State:        JobStateReady,
		}
		overrideA, erra := compressEnv([]string{"WR_RACE=A"})
		So(erra, ShouldBeNil)

		overrideB, errb := compressEnv([]string{"WR_RACE=B"})
		So(errb, ShouldBeNil)

		start := make(chan struct{})

		var wg sync.WaitGroup

		wg.Go(func() {
			<-start

			for i := range 5000 {
				job.Lock()
				if i%2 == 0 {
					job.EnvOverride = overrideA
				} else {
					job.EnvOverride = overrideB
				}
				job.Unlock()
			}
		})

		close(start)

		readErrors := 0

		for range 5000 {
			if _, err := job.ToStatus(); err != nil {
				readErrors++
			}
		}

		wg.Wait()
		So(readErrors, ShouldEqual, 0)
	})

	Convey("ToStatus() includes live fields and an SSH command for running jobs", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.Requirements.Other["cloud_user"] = "ubuntu"

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdBase, ShouldEqual, liveStatusCwd)
		So(status.Cwd, ShouldEqual, "/job1")
		So(status.PeakRAM, ShouldEqual, 321)
		So(status.CPUtime, ShouldEqual, 4)
		So(status.StdOut, ShouldEqual, "out\n")
		So(status.StdErr, ShouldEqual, "err\n")
		So(status.SSHCommand, ShouldEqual,
			"ssh -- ubuntu@10.0.0.8 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("ToStatus() uses Host when a running job has no HostIP", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.HostIP = ""

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual,
			"ssh -- worker1 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("ToStatus() separates ssh options from a target that starts with a hyphen", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.Host = "-worker1"
		job.HostIP = ""

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual,
			"ssh -- -worker1 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("ToStatus() leaves SSHCommand empty without a running host or cwd", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.Host = ""
		job.HostIP = ""

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual, "")

		job = liveStatusJob(JobStateRunning)
		job.ActualCwd = ""

		status, err = job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual, "")
	})

	Convey("ToStatus() of a cwd_matters job with no ActualCwd puts its Cwd in SSHCommand", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.CwdMatters = true
		job.ActualCwd = ""
		job.HostIP = ""

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdBase, ShouldEqual, liveStatusCwd)
		So(status.Cwd, ShouldBeBlank)
		So(status.SSHCommand, ShouldEqual,
			"ssh -- worker1 'cd /tmp/wr && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("ToStatus() of a job with an ActualCwd poisoned by wr v0.37.0|1 shows Cwd as the working dir", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.CwdMatters = true
		job.ActualCwd = job.Cwd

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdBase, ShouldEqual, liveStatusCwd)

		// blank, the same as the cwd_matters Convey above reports for a Job with no
		// ActualCwd at all: wr created no directory for a cwd_matters Job, so there
		// is no leaf below Cwd to show, and carrying the v0.37.0|1 poison must not
		// change what is displayed.
		So(status.Cwd, ShouldBeBlank)

		// the poisoning always wrote Cwd itself, so the assertion above cannot tell
		// "ActualCwd is ignored on a CwdMatters Job" apart from "ActualCwd was used
		// and happened to give the same answer". A poisoned value that DIFFERS from
		// Cwd is what makes it discriminate.
		job.ActualCwd = liveStatusCwd + "/poisoned"

		status, err = job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdBase, ShouldEqual, liveStatusCwd)
		So(status.Cwd, ShouldBeBlank)
		So(status.SSHCommand, ShouldEqual,
			"ssh -- 10.0.0.8 'cd /tmp/wr && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("ToStatus() of a job with an ActualCwd outside its Cwd shows that dir in full", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.Cwd = "/tmp/wr-modified"

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdBase, ShouldEqual, "/tmp/wr-modified")
		So(status.Cwd, ShouldEqual, liveStatusCwd+"/job1")
	})

	Convey("ToStatus() shell-quotes a running job's actual cwd in SSHCommand", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.HostIP = ""
		job.ActualCwd = "/tmp/wr/live jobs/it's-ok"

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual,
			`ssh -- worker1 'cd '"'"'/tmp/wr/live jobs/it'"'"'"'"'"'"'"'"'s-ok'"'"' `+
				`&& exec ${SHELL:-/bin/sh} -l'`)
	})

	Convey("ToStatus() hides SSHCommand for non-running status details", t, func() {
		for _, state := range []JobState{JobStateComplete, JobStateBuried, JobStateLost} {
			job := liveStatusJob(state)

			status, err := job.ToStatus()
			So(err, ShouldBeNil)
			So(status.SSHCommand, ShouldEqual, "")
		}
	})

	Convey("ToStatus() reports a never-run suspended job without start or end times", t, func() {
		job := &Job{
			Cmd:          testTrueCmd,
			Cwd:          testCwdPath,
			State:        JobStateSuspended,
			Requirements: &scheduler.Requirements{RAM: 1, Time: time.Second, Cores: 1},
		}

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.State, ShouldEqual, JobStateSuspended)
		So(status.Started, ShouldBeNil)
		So(status.Ended, ShouldBeNil)
	})
}

func TestJobModifierCwdMatters(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Modifying a job to cwd_matters clears its ActualCwd", t, func() {
		job := &Job{Cwd: liveStatusCwd, ActualCwd: liveStatusCwd + "/abc/cwd"}

		jm := NewJobModifer()
		jm.SetCwdMatters(true)
		jm.applyTo(job)

		// the job now runs in Cwd itself, and a blank ActualCwd is what stops
		// the cleanup behaviours treating Cwd's parent as a wr workspace
		So(job.CwdMatters, ShouldBeTrue)
		So(job.ActualCwd, ShouldBeBlank)
	})

	Convey("A job modified to cwd_matters still displays its working dir and ssh command", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.HostIP = ""

		jm := NewJobModifer()
		jm.SetCwdMatters(true)
		jm.applyTo(job)

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdMatters, ShouldBeTrue)
		So(status.CwdBase, ShouldEqual, liveStatusCwd)
		So(status.Cwd, ShouldBeBlank)
		So(status.SSHCommand, ShouldEqual,
			"ssh -- worker1 'cd /tmp/wr && exec ${SHELL:-/bin/sh} -l'")
	})
}

func TestJobModifierCwd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	newCwd := "/tmp/wr-modified"

	Convey("Modifying a job's cwd clears the ActualCwd of its last run", t, func() {
		job := liveStatusJob(JobStateRunning)

		jm := NewJobModifer()
		jm.SetCwd(newCwd)
		jm.applyTo(job)

		So(job.Cwd, ShouldEqual, newCwd)

		// the old ActualCwd is not below the new Cwd, so it names a directory
		// that has nothing to do with this job any more
		So(job.ActualCwd, ShouldBeBlank)
	})

	Convey("A job with a modified cwd shows that cwd, with no working dir below it", t, func() {
		job := liveStatusJob(JobStateRunning)

		jm := NewJobModifer()
		jm.SetCwd(newCwd)
		jm.applyTo(job)

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.CwdBase, ShouldEqual, newCwd)
		So(status.Cwd, ShouldBeBlank)
		So(status.SSHCommand, ShouldBeBlank)
	})
}

func TestJobModifierBehavioursCwdMatters(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Modifying a job's on_exit to a cleanup stores the cleanup", t, func() {
		job := &Job{Cwd: liveStatusCwd}

		jm := NewJobModifer()
		jm.SetBehaviours(Behaviours{{When: OnExit, Do: Cleanup}})
		jm.applyTo(job)

		So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"cleanup":true}]}`)
	})

	Convey("Modifying a cwd_matters job's on_exit to a cleanup stores nothing", t, func() {
		job := &Job{
			Cwd:        liveStatusCwd,
			CwdMatters: true,
			Behaviours: Behaviours{{When: OnExit, Do: Run, Arg: "echo old"}},
		}

		jm := NewJobModifer()
		jm.SetBehaviours(Behaviours{{When: OnExit, Do: Cleanup}})
		jm.applyTo(job)

		// a cleanup can only delete a dir wr made, so wr never stores one on a
		// cwd_matters job; the on_exit the user replaced must still be gone
		So(job.Behaviours.String(), ShouldBeBlank)
	})

	Convey("Modifying a job to cwd_matters drops a cleanup it already stored", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.Behaviours = Behaviours{
			{When: OnExit, Do: Cleanup},
			{When: OnFailure, Do: Run, Arg: "echo failed"},
		}

		jm := NewJobModifer()
		jm.SetCwdMatters(true)
		jm.applyTo(job)

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.Behaviours, ShouldEqual, `{"on_failure":[{"run":"echo failed"}]}`)
	})

	Convey("Modifying a cwd_matters job to cwd not mattering stores the cleanup asked for", t, func() {
		job := &Job{Cwd: liveStatusCwd, CwdMatters: true}

		jm := NewJobModifer()
		jm.SetCwdMatters(false)
		jm.SetBehaviours(Behaviours{{When: OnExit, Do: Cleanup}})
		jm.applyTo(job)

		So(job.Behaviours.String(), ShouldEqual, `{"on_exit":[{"cleanup":true}]}`)
	})
}

func liveStatusJob(state JobState) *Job {
	return &Job{
		Cmd:       "echo live status",
		Cwd:       liveStatusCwd,
		ActualCwd: liveStatusCwd + "/job1",
		Requirements: &scheduler.Requirements{
			RAM:   1,
			Time:  time.Minute,
			Cores: 1,
			Other: map[string]string{},
		},
		State:   state,
		Host:    liveStatusHost,
		HostIP:  liveStatusHostIP,
		Pid:     44,
		PeakRAM: 321,
		CPUtime: 4 * time.Second,
		StdOutC: compressStd([]byte("out\n")),
		StdErrC: compressStd([]byte("err\n")),
	}
}

func TestKeyByteIdentity(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The optimised byteKey hex formatting is byte-identical to fmt.Sprintf", t, func() {
		// Exercise leading-zero padding in each half, the all-zero and all-ones
		// extremes, and a mix of representative values, for both halves.
		vals := []uint64{
			0, 1, 0xf, 0xff, 0x100, 0xffff, 0xdeadbeef,
			0x0000000100000000, 0x00ff00ff00ff00ff, 0x8000000000000000,
			0xfedcba9876543210, math.MaxUint64 - 1, math.MaxUint64,
		}

		mismatches := 0
		checked := 0

		for _, l := range vals {
			for _, h := range vals {
				checked++

				if newHexKey(l, h) != oldHexFormat(l, h) {
					mismatches++
				}
			}
		}

		So(checked, ShouldEqual, len(vals)*len(vals))
		So(mismatches, ShouldEqual, 0)

		// Spot-check the exact expected output, including 32-char width and
		// zero padding of both halves.
		So(newHexKey(0, 0xff), ShouldEqual, "000000000000000000000000000000ff")
		So(newHexKey(math.MaxUint64, 0), ShouldEqual, "ffffffffffffffff0000000000000000")
		So(len(newHexKey(1, 1)), ShouldEqual, 32)
	})

	Convey("The optimised byteKey is byte-identical to the previous implementation", t, func() {
		inputs := [][]byte{
			nil,
			{},
			[]byte("a"),
			[]byte("true."),
			[]byte("/cwd.true."),
			[]byte("/cwd.true..docker:img."),
			[]byte("some longer concatenated key.with.dots.and:colons"),
			[]byte{0, 1, 2, 255, 254, 0},
		}

		mismatches := 0

		for _, in := range inputs {
			if byteKey(in) != oldByteKey(in) {
				mismatches++
			}
		}

		So(mismatches, ShouldEqual, 0)
	})

	Convey("The optimised Job.Key() is byte-identical to the previous fmt-based concatenation", t, func() {
		mcs := MountConfigs{{Targets: []MountTarget{{Path: testMountPath}}}}
		img := "alpine"
		cms := "/out:/in"

		jobs := []*Job{
			{Cmd: testTrueCmd},
			{Cmd: testTrueCmd, Cwd: testCwdPath},
			{Cmd: testTrueCmd, Cwd: testCwdPath, CwdMatters: true},
			{Cmd: testTrueCmd, MountConfigs: mcs},
			{Cmd: testTrueCmd, Cwd: testCwdPath, CwdMatters: true, MountConfigs: mcs},
			// ContainerMounts is ignored unless a container image is set.
			{Cmd: testTrueCmd, ContainerMounts: cms},
			{Cmd: testTrueCmd, WithDocker: img},
			{Cmd: testTrueCmd, WithDocker: img, ContainerMounts: cms},
			{Cmd: testTrueCmd, Cwd: testCwdPath, CwdMatters: true, WithDocker: img, ContainerMounts: cms},
			{Cmd: testTrueCmd, WithSingularity: img},
			{Cmd: testTrueCmd, WithSingularity: img, ContainerMounts: cms, MountConfigs: mcs},
			// Both set: WithDocker wins, matching the original logic.
			{Cmd: testTrueCmd, WithDocker: "d", WithSingularity: "s", ContainerMounts: "/m"},
		}

		mismatches := 0

		for _, j := range jobs {
			if j.Key() != oldJobKey(j) {
				mismatches++
			}
		}

		So(mismatches, ShouldEqual, 0)
	})

	Convey("The optimised JobEssence.Key() is byte-identical to the previous fmt-based concatenation", t, func() {
		mcs := MountConfigs{{Targets: []MountTarget{{Path: testMountPath}}}}

		essences := []*JobEssence{
			{Cmd: testTrueCmd},
			{Cmd: testTrueCmd, Cwd: testCwdPath},
			{Cmd: testTrueCmd, MountConfigs: mcs},
			{Cmd: testTrueCmd, Cwd: testCwdPath, MountConfigs: mcs},
			{JobKey: "preset-key-value"},
		}

		mismatches := 0

		for _, je := range essences {
			if je.Key() != oldJobEssenceKey(je) {
				mismatches++
			}
		}

		So(mismatches, ShouldEqual, 0)

		// JobEssence.Key() must still match Job.Key() for an equivalent
		// container-free job (the cross-type identity the lookups rely on).
		So((&JobEssence{Cmd: testTrueCmd, Cwd: testCwdPath, MountConfigs: mcs}).Key(),
			ShouldEqual, (&Job{Cmd: testTrueCmd, Cwd: testCwdPath, CwdMatters: true, MountConfigs: mcs}).Key())
	})
}

// oldJobKey reproduces the previous fmt.Sprintf-based Job.Key() concatenation
// (then byteKey) exactly, as the oracle for the optimised Job.Key().
func oldJobKey(j *Job) string {
	concat := fmt.Sprintf("%s.%s", j.Cmd, j.MountConfigs.Key())

	if j.CwdMatters {
		concat = fmt.Sprintf("%s.%s", j.Cwd, concat)
	}

	var image string

	if j.WithDocker != "" {
		image = "docker:" + j.WithDocker
	} else if j.WithSingularity != "" {
		image = "singularity:" + j.WithSingularity
	}

	if image != "" {
		concat = fmt.Sprintf("%s.%s.%s", concat, image, j.ContainerMounts)
	}

	return oldByteKey([]byte(concat))
}

// oldJobEssenceKey reproduces the previous fmt.Appendf-based JobEssence.Key()
// concatenation (then byteKey) exactly, as the oracle for the optimised one.
func oldJobEssenceKey(j *JobEssence) string {
	if j.JobKey != "" {
		return j.JobKey
	}

	if j.Cwd != "" {
		return oldByteKey(fmt.Appendf(nil, "%s.%s.%s", j.Cwd, j.Cmd, j.MountConfigs.Key()))
	}

	return oldByteKey(fmt.Appendf(nil, "%s.%s", j.Cmd, j.MountConfigs.Key()))
}

// oldByteKey reproduces the previous byteKey implementation exactly, as the
// oracle for the optimised version.
func oldByteKey(b []byte) string {
	l, h := farm.Hash128(b)

	return oldHexFormat(l, h)
}

// oldHexFormat is the previous fmt-based formulation of byteKey's hex encoding
// step. It is kept here purely as the oracle the optimised byteKey must match
// byte-for-byte; these strings are used as BoltDB keys, lookup-index keys and
// Go map keys, so any divergence would corrupt job identity and DB lookups.
func oldHexFormat(l, h uint64) string {
	return fmt.Sprintf("%016x%016x", l, h)
}

// twoMounts returns two MountConfigs deliberately NOT in Mount order, which is
// the order MountConfigs.Key() reads them in.
func twoMounts() MountConfigs {
	return MountConfigs{
		{Mount: "zeta", Targets: []MountTarget{{Path: testMountPath + "/z"}}},
		{Mount: "alpha", Targets: []MountTarget{{Path: testMountPath + "/a"}}},
	}
}

func TestMountConfigsKeyDoesNotMutate(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// asking a Job for its key is a read at every call site in wr -
	// workSpaceSnapshot asks under the Job's READ lock, and the REST and CLI
	// handlers, the job transitions, ToEssense and the client ask under no lock at
	// all - so a Key() that sorted the caller's slice would make every reader a
	// writer of the Job's own MountConfigs, leaving jobs permanently holding a
	// slice with one config lost and another duplicated: a dropped writable S3
	// mount is never mounted, so the Cmd's results are written into a plain
	// directory that cleanup then deletes.
	Convey("Asking MountConfigs for their key leaves them as they were", t, func() {
		mcs := twoMounts()

		key := mcs.Key()

		So(mcs[0].Mount, ShouldEqual, "zeta")
		So(mcs[1].Mount, ShouldEqual, "alpha")

		Convey("while still ignoring the order they were configured in", func() {
			So(MountConfigs{mcs[1], mcs[0]}.Key(), ShouldEqual, key)
		})

		Convey("and answering the same way every time when two share a Mount", func() {
			// sorting a fresh copy on every call is only safe if the sort is
			// stable: an unstable one could order these differently each time,
			// and a Job's key decides both its identity and the name of the
			// working directory it is allowed to delete.
			same := MountConfigs{
				{Mount: testMountPath, Targets: []MountTarget{{Path: "one"}}},
				{Mount: testMountPath, Targets: []MountTarget{{Path: "two"}}},
			}

			first := same.Key()
			differed := 0

			for range 20 {
				if same.Key() != first {
					differed++
				}
			}

			So(differed, ShouldEqual, 0)
		})
	})

	Convey("Asking a Job for its key leaves its MountConfigs as they were", t, func() {
		job := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, MountConfigs: twoMounts()}

		_ = job.Key()

		So(job.MountConfigs, ShouldResemble, twoMounts())
	})

	Convey("Asking a JobEssence for its key leaves its MountConfigs as they were", t, func() {
		je := &JobEssence{Cmd: testTrueCmd, MountConfigs: twoMounts()}

		_ = je.Key()

		So(je.MountConfigs, ShouldResemble, twoMounts())
	})

	Convey("Modifying a batch's mounts gives each job its own MountConfigs", t, func() {
		// one JobModifier is applied to every job of a `wr mod --mounts` batch, so
		// assigning its OWN slice to each of them would give distinct jobs one
		// backing array, guarded by as many mutexes as there were jobs.
		jm := &JobModifier{}
		jm.SetMountConfigs(twoMounts())

		jobs := []*Job{{Cmd: testTrueCmd, Cwd: testCwdPath}, {Cmd: testOnExitCmd, Cwd: testCwdPath}}
		for _, job := range jobs {
			jm.applyTo(job)
		}

		jobs[0].MountConfigs[0].Mount = "changed"

		So(jobs[1].MountConfigs[0].Mount, ShouldEqual, "zeta")
	})
}
