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

	Convey("ToStatus() falls back to Cwd in SSHCommand when there is no ActualCwd", t, func() {
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
