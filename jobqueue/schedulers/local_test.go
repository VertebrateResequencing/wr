// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

package scheduler

import (
	"fmt"
	. "github.com/smartystreets/goconvey/convey"
	"io/ioutil"
	"log"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestSchedulerLocal(t *testing.T) {
	maxCPU := runtime.NumCPU()
	runtime.GOMAXPROCS(maxCPU)

	Convey("You can get a new local scheduler", t, func() {
		s, err := New("local", "bash")
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{1, 1 * time.Second, 1, ""}
		impossibleReq := &Requirements{999999999, 24 * time.Hour, 999, ""}

		Convey("Busy() starts off false", func() {
			So(s.Busy(), ShouldBeFalse)
		})

		Convey("Schedule() gives impossible error when given impossible reqs", func() {
			err := s.Schedule("foo", impossibleReq, 1)
			So(err, ShouldNotBeNil)
			serr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(serr.Err, ShouldEqual, ErrImpossible)
		})

		Convey("Schedule() lets you schedule more jobs than localhost CPUs", func() {
			tmpdir, err := ioutil.TempDir("", "vrpipe_schedulers_local_test_immediate_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)
			tmpdir2, err := ioutil.TempDir("", "vrpipe_schedulers_local_test_end_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir2)

			cmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75); @a = tempfile(DIR => q[%s]); exit(0);'", tmpdir, tmpdir2) // creates a file, sleeps for 0.75s and then creates another file
			count := maxCPU * 2
			err = s.Schedule(cmd, possibleReq, count)
			So(err, ShouldBeNil)
			So(s.Busy(), ShouldBeTrue)

			Convey("It eventually runs them all", func() {
				<-time.After(700 * time.Millisecond)

				files, err := ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran := 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, maxCPU)

				<-time.After(900 * time.Millisecond)

				files, err = ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran = 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, count)
				So(s.Busy(), ShouldBeTrue)

				<-time.After(750 * time.Millisecond) // *** don't know why we need an extra 850ms for the cmds to finish running

				files, err = ioutil.ReadDir(tmpdir2)
				if err != nil {
					log.Fatal(err)
				}

				ran = 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, count)
				So(s.Busy(), ShouldBeFalse)
			})

			Convey("You can Schedule() again to drop the count", func() {
				newcount := maxCPU + 1 // (this test only really makes sense if newcount is now less than count, ie. we have more than 1 cpu)

				<-time.After(700 * time.Millisecond)

				files, err := ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran := 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(900 * time.Millisecond)

				files, err = ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran = 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, newcount)
			})

			Convey("Dropping the count below the number currently running doesn't kill those that are running", func() {
				newcount := maxCPU - 1

				<-time.After(700 * time.Millisecond)

				files, err := ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran := 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(900 * time.Millisecond)

				files, err = ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran = 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, maxCPU)
			})

			Convey("You can Schedule() again to increase the count", func() {
				newcount := count + 5

				<-time.After(700 * time.Millisecond)

				files, err := ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran := 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(1650 * time.Millisecond)

				files, err = ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}

				ran = 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, newcount)
			})

			if maxCPU > 1 {
				Convey("You can Schedule() a new job and have it run while the first is still running", func() {
					newcount := maxCPU + 1

					<-time.After(700 * time.Millisecond)

					files, err := ioutil.ReadDir(tmpdir)
					if err != nil {
						log.Fatal(err)
					}

					ran := 0
					for range files {
						ran++
					}
					So(ran, ShouldEqual, maxCPU)

					err = s.Schedule(cmd, possibleReq, newcount)
					So(err, ShouldBeNil)
					newcmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@b = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75);'", tmpdir)
					err = s.Schedule(newcmd, possibleReq, 1)
					So(err, ShouldBeNil)

					<-time.After(900 * time.Millisecond)

					files, err = ioutil.ReadDir(tmpdir)
					if err != nil {
						log.Fatal(err)
					}

					ran = 0
					for range files {
						ran++
					}
					So(ran, ShouldEqual, newcount+1)
				})

				//*** want a test where the first job fills up all resources
				// and has more to do, and a second job could slip and complete
				// before resources for the first become available
			}
		})
	})
}
