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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

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
			mcs := MountConfigs{{Targets: []MountTarget{{Path: "path"}}}}
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
		job := &Job{Cmd: "true", Cwd: "/cwd"}
		cmd, cleanup, err := job.CmdLine(ctx)
		So(err, ShouldBeNil)
		So(cmd, ShouldEqual, "true")
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
				job.EnvAddOverride([]string{"FOO=bar", "OOF=rab"})

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
			So(cmd, ShouldEndWith, fmt.Sprintf(" | singularity shell %s", image))
			So(job.MonitorDocker, ShouldBeBlank)

			Convey("That can include additional mounts", func() {
				job.ContainerMounts = "/foo/bar:/bar,/foo/baz:/baz"
				job.EnvAddOverride([]string{"FOO=bar", "OOF=rab"})

				cmd, cleanup, err = job.CmdLine(ctx)
				So(err, ShouldBeNil)
				So(cleanup, ShouldNotBeNil)

				defer cleanup()

				So(cmd, ShouldEndWith, fmt.Sprintf(" | singularity shell -B /foo/bar:/bar -B /foo/baz:/baz %s", image))
			})
		})
	})

	Convey("ToStatus() reports start and end times as Unix nanoseconds", t, func() {
		started := time.Unix(100, 123456789)
		ended := started.Add(25 * time.Millisecond)
		job := &Job{
			Cmd:                 "true",
			Cwd:                 "/cwd",
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
			Cmd:                 "true",
			Cwd:                 "/cwd",
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
			Cmd:          "true",
			Cwd:          "/cwd",
			Requirements: &scheduler.Requirements{RAM: 1, Time: time.Second, Cores: 1},
			State:        JobStateReady,
		}
		overrideA, erra := compressEnv([]string{"WR_RACE=A"})
		So(erra, ShouldBeNil)

		overrideB, errb := compressEnv([]string{"WR_RACE=B"})
		So(errb, ShouldBeNil)

		start := make(chan struct{})

		var wg sync.WaitGroup
		wg.Add(1)

		go func() {
			defer wg.Done()

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
		}()

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
		So(status.CwdBase, ShouldEqual, "/tmp/wr")
		So(status.Cwd, ShouldEqual, "/job1")
		So(status.PeakRAM, ShouldEqual, 321)
		So(status.CPUtime, ShouldEqual, 4)
		So(status.StdOut, ShouldEqual, "out\n")
		So(status.StdErr, ShouldEqual, "err\n")
		So(status.SSHCommand, ShouldEqual,
			"ssh ubuntu@10.0.0.8 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
	})

	Convey("ToStatus() uses Host when a running job has no HostIP", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.HostIP = ""

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual,
			"ssh worker1 'cd /tmp/wr/job1 && exec ${SHELL:-/bin/sh} -l'")
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

	Convey("ToStatus() shell-quotes a running job's actual cwd in SSHCommand", t, func() {
		job := liveStatusJob(JobStateRunning)
		job.HostIP = ""
		job.ActualCwd = "/tmp/wr/live jobs/it's-ok"

		status, err := job.ToStatus()
		So(err, ShouldBeNil)
		So(status.SSHCommand, ShouldEqual,
			`ssh worker1 'cd '"'"'/tmp/wr/live jobs/it'"'"'"'"'"'"'"'"'s-ok'"'"' `+
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
}

func liveStatusJob(state JobState) *Job {
	return &Job{
		Cmd:       "echo live status",
		Cwd:       "/tmp/wr",
		ActualCwd: "/tmp/wr/job1",
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
