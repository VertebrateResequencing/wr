// Copyright © 2021 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package jobqueue

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

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
}
