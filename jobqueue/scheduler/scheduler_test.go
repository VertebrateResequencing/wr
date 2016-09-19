// Copyright © 2016 Genome Research Limited
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

package scheduler

import (
	"fmt"
	. "github.com/smartystreets/goconvey/convey"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

var maxCPU = runtime.NumCPU()

func TestLocal(t *testing.T) {
	runtime.GOMAXPROCS(maxCPU)

	Convey("You can get a new local scheduler", t, func() {
		s, err := New("local", "development", "bash")
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{1, 1 * time.Second, 1, ""}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, ""}

		Convey("ReserveTimeout() returns 1 second", func() {
			So(s.ReserveTimeout(), ShouldEqual, 1)
		})

		Convey("MaxQueueTime() always returns 0", func() {
			So(s.MaxQueueTime(possibleReq).Seconds(), ShouldEqual, 0)
		})

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
			tmpdir, err := ioutil.TempDir("", "wr_schedulers_local_test_immediate_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)
			tmpdir2, err := ioutil.TempDir("", "wr_schedulers_local_test_end_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir2)

			cmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75); @a = tempfile(DIR => q[%s]); exit(0);'", tmpdir, tmpdir2) // creates a file, sleeps for 0.75s and then creates another file, though this actually completes in around 1.1s
			count := maxCPU * 2
			err = s.Schedule(cmd, possibleReq, count)
			So(err, ShouldBeNil)
			So(s.Busy(), ShouldBeTrue)

			Convey("It eventually runs them all", func() {
				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU)
				So(numfiles, ShouldEqual, maxCPU)

				<-time.After(1100 * time.Millisecond)

				numfiles = testDirForFiles(tmpdir, count)
				So(numfiles, ShouldEqual, count)
				So(s.Busy(), ShouldBeTrue)

				<-time.After(800 * time.Millisecond) // *** don't know why we need an extra 400ms for the cmds to finish running

				numfiles = testDirForFiles(tmpdir2, count)
				So(numfiles, ShouldEqual, count)
				So(s.Busy(), ShouldBeFalse)
			})

			Convey("You can Schedule() again to drop the count", func() {
				newcount := maxCPU + 1 // (this test only really makes sense if newcount is now less than count, ie. we have more than 1 cpu)

				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU)
				So(numfiles, ShouldEqual, maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(900 * time.Millisecond)

				numfiles = testDirForFiles(tmpdir, newcount)
				So(numfiles, ShouldEqual, newcount)

				So(waitToFinish(s, 3, 100), ShouldBeTrue)
			})

			Convey("Dropping the count below the number currently running doesn't kill those that are running", func() {
				newcount := maxCPU - 1

				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU)
				So(numfiles, ShouldEqual, maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(900 * time.Millisecond)

				numfiles = testDirForFiles(tmpdir, maxCPU)
				So(numfiles, ShouldEqual, maxCPU)

				So(waitToFinish(s, 3, 100), ShouldBeTrue)
			})

			Convey("You can Schedule() again to increase the count", func() {
				newcount := count + 5

				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU)
				So(numfiles, ShouldEqual, maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(2000 * time.Millisecond)

				numfiles = testDirForFiles(tmpdir, newcount)
				So(numfiles, ShouldEqual, newcount)

				So(waitToFinish(s, 3, 100), ShouldBeTrue)
			})

			if maxCPU > 1 {
				Convey("You can Schedule() a new job and have it run while the first is still running", func() {
					newcount := maxCPU + 1

					<-time.After(700 * time.Millisecond)

					numfiles := testDirForFiles(tmpdir, maxCPU)
					So(numfiles, ShouldEqual, maxCPU)

					err = s.Schedule(cmd, possibleReq, newcount)
					So(err, ShouldBeNil)
					newcmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@b = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75);'", tmpdir)
					err = s.Schedule(newcmd, possibleReq, 1)
					So(err, ShouldBeNil)

					<-time.After(900 * time.Millisecond)

					numfiles = testDirForFiles(tmpdir, newcount+1)
					So(numfiles, ShouldEqual, newcount+1)

					So(waitToFinish(s, 3, 100), ShouldBeTrue)
				})

				//*** want a test where the first job fills up all resources
				// and has more to do, and a second job could slip and complete
				// before resources for the first become available
			}
		})

		// wait a while for any remaining jobs to finish
		So(waitToFinish(s, 30, 100), ShouldBeTrue)
	})
}

func TestLSF(t *testing.T) {
	// check if LSF seems to be installed
	_, err := exec.LookPath("lsadmin")
	if err == nil {
		_, err = exec.LookPath("bqueues")
	}
	if err != nil {
		Convey("You can't get a new lsf scheduler without LSF being installed", t, func() {
			_, err := New("lsf", "development", "bash")
			So(err, ShouldNotBeNil)
		})
		return
	}

	host, _ := os.Hostname()
	Convey("You can get a new lsf scheduler", t, func() {
		s, err := New("lsf", "development", "bash")
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{100, 1 * time.Minute, 1, ""}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, ""}

		Convey("ReserveTimeout() returns 25 seconds", func() {
			So(s.ReserveTimeout(), ShouldEqual, 1)
		})

		// author specific tests, based on hostname, where we know what the
		// expected queue names are *** could also break out initialize() to
		// mock some textual input instead of taking it from lsadmin...
		if host == "vr-2-2-02" {
			Convey("determineQueue() picks the best queue depending on given resource requirements", func() {
				queue, err := s.impl.(*lsf).determineQueue(possibleReq, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 5 * time.Minute, 1, ""}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 5 * time.Minute, 1, ""}, 10)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "yesterday")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{37000, 1 * time.Hour, 1, ""}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal") // used to be "test" before our memory limits were removed from all queues

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 13 * time.Hour, 1, ""}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 73 * time.Hour, 1, ""}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "basement")
			})

			Convey("MaxQueueTime() returns appropriate times depending on the requirements", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 720)
				So(s.MaxQueueTime(&Requirements{1, 13 * time.Hour, 1, ""}).Minutes(), ShouldEqual, 4320)
			})
		}

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
			// tmpdir, err := ioutil.TempDir("", "wr_schedulers_lsf_test_output_dir_")
			// if err != nil {
			// 	log.Fatal(err)
			// }
			// defer os.RemoveAll(tmpdir)

			// cmd := fmt.Sprintf("ssh %s 'perl -MFile::Temp=tempfile -e '\"'\"'$sleep = rand(60); select(undef, undef, undef, $sleep); @a = tempfile(DIR => q[%s]); select(undef, undef, undef, 5 - $sleep); exit(0);'\"'\"", host, tmpdir) // sleep for a random amount of time so that ssh does not fail due to too many run at once, then ssh back to us and create a file in our tmp dir

			// the above wouldn't work due to some issue with all the ssh's not
			// working and some high proportion of the LSF jobs immediately
			// failing; instead we assume, since this is LSF, that our current
			// directory is on a shared disk, and just have all the jobs write
			// their files here directly
			tmpdir, err := ioutil.TempDir("./", "wr_schedulers_lsf_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			cmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); sleep(5); exit(0);'", tmpdir)

			count := maxCPU * 2
			err = s.Schedule(cmd, possibleReq, count)
			So(err, ShouldBeNil)
			So(s.Busy(), ShouldBeTrue)

			Convey("It eventually runs them all", func() {
				So(waitToFinish(s, 300, 1000), ShouldBeTrue)
				numfiles := testDirForFiles(tmpdir, count)
				So(numfiles, ShouldEqual, count)
			})

			// *** no idea how to reliably test dropping the count, since I
			// don't have any way of ensuring some jobs are still pending by the
			// time I try and drop the count... unless I did something like
			// have a count of 1000000?...

			Convey("You can Schedule() again to increase the count", func() {
				newcount := count + 5
				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)
				So(waitToFinish(s, 300, 1000), ShouldBeTrue)
				numfiles := testDirForFiles(tmpdir, newcount)
				So(numfiles, ShouldEqual, newcount)
			})

			Convey("You can Schedule() a new job and have it run while the first is still running", func() {
				<-time.After(500 * time.Millisecond)
				numfiles := testDirForFiles(tmpdir, 1)
				So(numfiles, ShouldBeBetweenOrEqual, 1, count)
				So(s.Busy(), ShouldBeTrue)

				newcmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); sleep(1); exit(0);'", tmpdir)
				err = s.Schedule(newcmd, possibleReq, 1)
				So(err, ShouldBeNil)

				So(waitToFinish(s, 300, 1000), ShouldBeTrue)
				numfiles = testDirForFiles(tmpdir, count+1)
				So(numfiles, ShouldEqual, count+1)
			})
		})

		Convey("Schedule() lets you schedule more jobs than could reasonably start all at once", func() {
			tmpdir, err := ioutil.TempDir("./", "wr_schedulers_lsf_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			cmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); sleep(2); exit(0);'", tmpdir)

			count := 10000 // 1,000,000 just errors out, and 100,000 could be bad for LSF in some way
			err = s.Schedule(cmd, possibleReq, count)
			So(err, ShouldBeNil)
			So(s.Busy(), ShouldBeTrue)

			Convey("It runs some of them and you can Schedule() again to drop the count", func() {
				So(waitToFinish(s, 3, 1000), ShouldBeFalse)
				numfiles := testDirForFiles(tmpdir, 1)
				So(numfiles, ShouldBeBetween, 1, count-(maxCPU*2)-2)

				newcount := numfiles + maxCPU
				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				So(waitToFinish(s, 300, 1000), ShouldBeTrue)
				numfiles = testDirForFiles(tmpdir, newcount)
				So(numfiles, ShouldBeBetweenOrEqual, newcount, numfiles*2) // we must allow it to run a few extra due to the implementation
			})
		})

		// wait a while for any remaining jobs to finish
		So(waitToFinish(s, 300, 1000), ShouldBeTrue)
	})
}

func testDirForFiles(tmpdir string, expected int) (numfiles int) {
	files, err := ioutil.ReadDir(tmpdir)
	if err != nil {
		log.Fatal(err)
	}
	if len(files) < expected {
		// wait a little longer for things to sync up, by running ls
		cmd := exec.Command("ls", tmpdir)
		cmd.Run()
		files, err = ioutil.ReadDir(tmpdir)
		if err != nil {
			log.Fatal(err)
		}
	}
	return len(files)
}

func waitToFinish(s *Scheduler, maxS int, interval int) bool {
	done := make(chan bool, 1)
	go func() {
		limit := time.After(time.Duration(maxS) * time.Second)
		ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
		for {
			select {
			case <-ticker.C:
				if !s.Busy() {
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
	return <-done
}
