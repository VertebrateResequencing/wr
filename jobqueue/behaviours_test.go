// Copyright © 2017 Genome Research Limited
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
	"fmt"
	. "github.com/smartystreets/goconvey/convey"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBehaviours(t *testing.T) {
	Convey("You can create individual Behaviour", t, func() {
		b1 := &Behaviour{When: OnExit, Do: CleanupAll}
		b2 := &Behaviour{When: OnSuccess, Do: CleanupAll}
		b3 := &Behaviour{When: OnFailure, Do: CleanupAll}
		b4 := &Behaviour{When: OnSuccess, Do: Run, Arg: "touch ../../foo"}
		b5 := &Behaviour{When: OnSuccess, Do: Run, Arg: "touch foo"}
		b6 := &Behaviour{When: OnSuccess, Do: Run, Arg: []string{"in", "valid"}}
		b7 := &Behaviour{When: OnSuccess, Do: CopyToManager, Arg: []string{"a.file", "b.file"}}
		b8 := &Behaviour{When: OnSuccess, Do: CopyToManager, Arg: "a.file"}

		cwd, err := ioutil.TempDir("", "wr_jobqueue_test_behaviour_dir_")
		So(err, ShouldBeNil)
		defer os.RemoveAll(cwd)
		actualCwd := filepath.Join(cwd, "a", "b", "c", "def", "cwd")
		os.MkdirAll(actualCwd, os.ModePerm)
		os.OpenFile(filepath.Join(actualCwd, "a.file"), os.O_RDONLY|os.O_CREATE, 0666)
		os.OpenFile(filepath.Join(actualCwd, "b.file"), os.O_RDONLY|os.O_CREATE, 0666)
		foo := filepath.Join(cwd, "a", "b", "c", "foo")
		adir := filepath.Join(cwd, "a")
		job1 := &Job{Cwd: cwd, ActualCwd: actualCwd}
		job2 := &Job{Cwd: cwd}

		Convey("Individual Behaviour Trigger() correctly", func() {
			err = b7.Trigger(OnSuccess, job1)
			So(err, ShouldBeNil)
			err = b8.Trigger(OnSuccess, job1)
			So(err, ShouldNotBeNil)
			//*** CopyToManager not yet implemented, so no proper tests for it yet

			err = b6.Trigger(OnSuccess, job1)
			So(err, ShouldNotBeNil)
			err = b4.Trigger(OnFailure, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)
			err = b4.Trigger(OnSuccess, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			os.Remove(foo)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)
			err = b4.Trigger(OnExit, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)

			err = b5.Trigger(OnSuccess, job2)
			So(err, ShouldBeNil)
			foo2 := filepath.Join(cwd, "foo")
			_, err = os.Stat(foo2)
			So(err, ShouldBeNil)

			err = b1.Trigger(OnSuccess|OnFailure, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(actualCwd)
			So(err, ShouldBeNil)
			err = b1.Trigger(OnExit, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(actualCwd)
			So(err, ShouldNotBeNil)
			_, err = os.Stat(cwd)
			So(err, ShouldBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldNotBeNil)

			err = b1.Trigger(OnExit, job2)
			So(err, ShouldBeNil)
			_, err = os.Stat(cwd)
			So(err, ShouldBeNil)
		})

		Convey("CleanupAll works when actual cwd contains root-owned files", func() {
			rootFile := filepath.Join(actualCwd, "root")
			err = exec.Command("sh", "-c", "sudo -n touch "+rootFile).Run()
			if err != nil {
				SkipConvey("Can't do this test without ability to sudo", func() {})
			} else {
				_, err = os.Stat(rootFile)
				So(err, ShouldBeNil)

				out, _ := exec.Command("sh", "-c", "ls -alth "+actualCwd).CombinedOutput()
				fmt.Printf("\n%s\n", string(out))

				err = b1.Trigger(OnExit, job1)
				So(err, ShouldBeNil)
				_, err = os.Stat(actualCwd)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(cwd)
				So(err, ShouldBeNil)
				_, err = os.Stat(adir)
				So(err, ShouldNotBeNil)
			}
		})

		Convey("Behaviours are triggered in order b2,b4, as specified", func() {
			bs := Behaviours{b2, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldNotBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldNotBeNil)
		})

		Convey("Behaviours are triggered in order b4,b2, as specified", func() {
			bs := Behaviours{b4, b2}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(filepath.Join(cwd, "a", "b", "c", "def"))
			So(err, ShouldNotBeNil)
		})

		Convey("OnExit triggers after others", func() {
			bs := Behaviours{b1, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(filepath.Join(cwd, "a", "b", "c", "def"))
			So(err, ShouldNotBeNil)
		})

		Convey("Non-matching Behaviours are ignored during a Trigger()", func() {
			bs := Behaviours{b3, b4}
			err = bs.Trigger(true, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(foo)
			So(err, ShouldBeNil)
			_, err = os.Stat(actualCwd)
			So(err, ShouldBeNil)

			os.Remove(foo)
			_, err = os.Stat(foo)
			So(err, ShouldNotBeNil)

			bs = Behaviours{b4, b3}
			err = bs.Trigger(false, job1)
			So(err, ShouldBeNil)
			_, err = os.Stat(adir)
			So(err, ShouldNotBeNil)
		})
	})
}
