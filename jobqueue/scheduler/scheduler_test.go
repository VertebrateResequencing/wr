// Copyright © 2016-2019 Genome Research Limited
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
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/inconshreveable/log15"
	. "github.com/smartystreets/goconvey/convey"
)

var maxCPU = runtime.NumCPU()
var otherReqs = make(map[string]string)

var testLogger = log15.New()

func init() {
	testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))
}

func TestLocal(t *testing.T) {
	runtime.GOMAXPROCS(maxCPU)

	Convey("You can get a new local scheduler", t, func() {
		s, err := New("local", &ConfigLocal{"bash", 1 * time.Second, 0, 0}, testLogger)
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{1, 1 * time.Second, 1, true, 20, true, otherReqs, true}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, true, 20, true, otherReqs, true}

		Convey("ReserveTimeout() returns 1 second", func() {
			So(s.ReserveTimeout(possibleReq), ShouldEqual, 1)
		})

		Convey("MaxQueueTime() always returns 0", func() {
			So(s.MaxQueueTime(possibleReq).Seconds(), ShouldEqual, 0)
		})

		Convey("Busy() starts off false", func() {
			So(s.Busy(), ShouldBeFalse)
		})

		Convey("Requirements.Stringify() works", func() {
			So(possibleReq.Stringify(), ShouldEqual, "1:0:1:20")
			testReq := &Requirements{RAM: 300, Time: 2 * time.Hour, Cores: 2}
			So(testReq.Stringify(), ShouldEqual, "300:120:2:0")
			other := make(map[string]string)
			other["foo"] = "bar"
			other["goo"] = "lar"
			testReq.Other = other
			So(testReq.Stringify(), ShouldEqual, "300:120:2:0:f88250fdf9c81d47c18d63354b85f26e")
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

			cmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75); @a = tempfile(DIR => q[%s]); exit(0);'", tmpdir, tmpdir2) // creates a file, sleeps for 0.75s and then creates another file

			// different machines take difference amounts of times to actually
			// run the above command, so we first need to run the command (in
			// parallel still, since it is slower to run when many are running
			// at once) to find how long it takes, as subsequent tests are very
			// timing dependent
			err = s.Schedule(cmd, possibleReq, maxCPU)
			So(err, ShouldBeNil)
			before := time.Now()
			var overhead time.Duration
			for {
				if !s.Busy() {
					overhead = time.Since(before) - time.Duration(750*time.Millisecond)
					break
				}
				<-time.After(1 * time.Millisecond)
			}

			count := maxCPU * 2
			err = s.Schedule(cmd, possibleReq, count)
			So(err, ShouldBeNil)
			So(s.Busy(), ShouldBeTrue)

			Convey("It eventually runs them all", func() {
				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU+maxCPU) // from the speed test + half of the newly scheduled tests
				So(numfiles, ShouldEqual, maxCPU+maxCPU)

				<-time.After(750*time.Millisecond + overhead)

				numfiles = testDirForFiles(tmpdir, maxCPU+count) // from the speed test + all the newly scheduled tests have at least started
				So(numfiles, ShouldEqual, maxCPU+count)
				numfiles = testDirForFiles(tmpdir2, maxCPU+count)
				if numfiles < maxCPU+count {
					So(s.Busy(), ShouldBeTrue) // but they might not all have finished quite yet
				}

				<-time.After(200*time.Millisecond + overhead) // an extra 150ms for leeway

				numfiles = testDirForFiles(tmpdir2, maxCPU+count)
				So(numfiles, ShouldEqual, maxCPU+count)
				So(s.Busy(), ShouldBeFalse)
			})

			Convey("You can Schedule() again to drop the count", func() {
				newcount := maxCPU + 1 // (this test only really makes sense if newcount is now less than count, ie. we have more than 1 cpu)

				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU+maxCPU)
				So(numfiles, ShouldEqual, maxCPU+maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(750*time.Millisecond + overhead)

				numfiles = testDirForFiles(tmpdir, maxCPU+newcount)
				So(numfiles, ShouldEqual, maxCPU+newcount)

				So(waitToFinish(s, 3, 100), ShouldBeTrue)
			})

			Convey("Dropping the count below the number currently running doesn't kill those that are running", func() {
				newcount := maxCPU - 1

				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU+maxCPU)
				So(numfiles, ShouldEqual, maxCPU+maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(750*time.Millisecond + overhead)

				numfiles = testDirForFiles(tmpdir, maxCPU+maxCPU)
				So(numfiles, ShouldEqual, maxCPU+maxCPU)

				So(waitToFinish(s, 3, 100), ShouldBeTrue)
			})

			Convey("You can Schedule() again to increase the count", func() {
				newcount := count + 1

				<-time.After(700 * time.Millisecond)

				numfiles := testDirForFiles(tmpdir, maxCPU+maxCPU)
				So(numfiles, ShouldEqual, maxCPU+maxCPU)

				err = s.Schedule(cmd, possibleReq, newcount)
				So(err, ShouldBeNil)

				<-time.After(1500*time.Millisecond + overhead + overhead)

				numfiles = testDirForFiles(tmpdir, maxCPU+newcount)
				So(numfiles, ShouldEqual, maxCPU+newcount)

				So(waitToFinish(s, 3, 100), ShouldBeTrue)
			})

			if maxCPU > 1 {
				Convey("You can Schedule() a new job and have it run while the first is still running", func() {
					newcount := maxCPU + 1

					<-time.After(700 * time.Millisecond)

					numfiles := testDirForFiles(tmpdir, maxCPU+maxCPU)
					So(numfiles, ShouldEqual, maxCPU+maxCPU)

					err = s.Schedule(cmd, possibleReq, newcount)
					So(err, ShouldBeNil)
					newcmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@b = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75);'", tmpdir)
					err = s.Schedule(newcmd, possibleReq, 1)
					So(err, ShouldBeNil)

					<-time.After(750*time.Millisecond + overhead)

					numfiles = testDirForFiles(tmpdir, maxCPU+newcount+1)
					So(numfiles, ShouldEqual, maxCPU+newcount+1)

					So(waitToFinish(s, 3, 100), ShouldBeTrue)
				})

				//*** want a test where the first job fills up all resources
				// and has more to do, and a second job could slip and complete
				// before resources for the first become available
			}
		})

		if maxCPU > 2 {
			Convey("Schedule() does bin packing and fills up the machine with different size cmds", func() {
				smallTmpdir, err := ioutil.TempDir("", "wr_schedulers_local_test_small_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(smallTmpdir)
				bigTmpdir, err := ioutil.TempDir("", "wr_schedulers_local_test_big_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(bigTmpdir)

				blockCmd := "sleep 0.25"
				blockReq := &Requirements{1, 1 * time.Second, float64(maxCPU), true, 0, true, otherReqs, true}
				smallCmd := fmt.Sprintf("mktemp --tmpdir=%s tmp.XXXXXX && sleep 0.75", smallTmpdir)
				smallReq := &Requirements{1, 1 * time.Second, 1, true, 0, true, otherReqs, true}
				bigCmd := fmt.Sprintf("mktemp --tmpdir=%s tmp.XXXXXX && sleep 0.75", bigTmpdir)
				bigReq := &Requirements{1, 1 * time.Second, float64(maxCPU - 1), true, 0, true, otherReqs, true}

				// schedule 2 big cmds and then a small one to prove the small
				// one fits the gap and runs before the second big one
				err = s.Schedule(bigCmd, bigReq, 2)
				So(err, ShouldBeNil)
				err = s.Schedule(smallCmd, smallReq, 1)
				So(err, ShouldBeNil)

				for {
					if !s.Busy() {
						break
					}
					<-time.After(1 * time.Millisecond)
				}

				bigTimes := mtimesOfFilesInDir(bigTmpdir, 2)
				So(len(bigTimes), ShouldEqual, 2)
				smallTimes := mtimesOfFilesInDir(smallTmpdir, 1)
				So(len(smallTimes), ShouldEqual, 1)
				firstBig := bigTimes[0]
				secondBig := bigTimes[1]
				if secondBig.Before(firstBig) {
					firstBig = bigTimes[1]
					secondBig = bigTimes[0]
				}
				So(smallTimes[0], ShouldHappenOnOrAfter, firstBig)
				So(smallTimes[0], ShouldHappenBefore, secondBig)

				// schedule a blocker so that subsequent schedules will be
				// compared to each other, then schedule 2 small cmds and a big
				// command that uses all cpus to prove that the biggest one
				// takes priority
				err = s.Schedule(blockCmd, blockReq, 1)
				So(err, ShouldBeNil)
				err = s.Schedule(smallCmd, smallReq, 2)
				So(err, ShouldBeNil)
				err = s.Schedule(bigCmd, blockReq, 1)
				So(err, ShouldBeNil)

				for {
					if !s.Busy() {
						break
					}
					<-time.After(1 * time.Millisecond)
				}

				bigTimes = mtimesOfFilesInDir(bigTmpdir, 1)
				So(len(bigTimes), ShouldEqual, 1)
				smallTimes = mtimesOfFilesInDir(smallTmpdir, 2)
				So(len(smallTimes), ShouldEqual, 2)
				So(bigTimes[0], ShouldHappenOnOrBefore, smallTimes[0])
				So(bigTimes[0], ShouldHappenOnOrBefore, smallTimes[1])
				// *** one of the above 2 tests can fail; the jobs start in the
				// correct order, which is what we're trying to test for, but
				// finish in the wrong order. That is, the big job takes a few
				// extra ms before it does anything. Not sure how to test for
				// actual job start time order...
			})
		}

		// wait a while for any remaining jobs to finish
		So(waitToFinish(s, 30, 100), ShouldBeTrue)
	})

	if maxCPU > 1 {
		Convey("You can get a new local scheduler that uses less than all CPUs", t, func() {
			s, err := New("local", &ConfigLocal{"bash", 1 * time.Second, 1, 0}, testLogger)
			So(err, ShouldBeNil)
			So(s, ShouldNotBeNil)

			tmpDir, err := ioutil.TempDir("", "wr_schedulers_local_test_slee[_output_dir_")
			if err != nil {
				log.Fatal(err)
			}

			cmd := fmt.Sprintf("mktemp --tmpdir=%s tmp.XXXXXX && sleep 0.5", tmpDir)
			sleepReq := &Requirements{1, 1 * time.Second, 1, true, 0, true, otherReqs, true}

			err = s.Schedule(cmd, sleepReq, 2)
			So(err, ShouldBeNil)

			for {
				if !s.Busy() {
					break
				}
				<-time.After(1 * time.Millisecond)
			}

			times := mtimesOfFilesInDir(tmpDir, 2)
			So(len(times), ShouldEqual, 2)
			first := times[0]
			second := times[1]
			if second.Before(first) {
				first = times[1]
				second = times[0]
			}
			So(first, ShouldHappenBefore, second)
			So(first, ShouldHappenBefore, second.Add(-400*time.Millisecond))
		})
	}
}

func TestLSF(t *testing.T) {
	// check if LSF seems to be installed
	_, err := exec.LookPath("lsadmin")
	if err == nil {
		_, err = exec.LookPath("bqueues")
	}
	if err != nil {
		Convey("You can't get a new lsf scheduler without LSF being installed", t, func() {
			_, err := New("lsf", &ConfigLSF{"development", "bash"}, testLogger)
			So(err, ShouldNotBeNil)
		})
		return
	}

	host, _ := os.Hostname()
	Convey("You can get a new lsf scheduler", t, func() {
		s, err := New("lsf", &ConfigLSF{"development", "bash"}, testLogger)
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{100, 1 * time.Minute, 1, true, 20, true, otherReqs, true}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, true, 20, true, otherReqs, true}

		Convey("ReserveTimeout() returns 25 seconds", func() {
			So(s.ReserveTimeout(possibleReq), ShouldEqual, 1)
		})

		// author specific tests, based on hostname, where we know what the
		// expected queue names are *** could also break out initialize() to
		// mock some textual input instead of taking it from lsadmin...
		if host == "vr-2-2-02" {
			Convey("determineQueue() picks the best queue depending on given resource requirements", func() {
				queue, err := s.impl.(*lsf).determineQueue(possibleReq, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 5 * time.Minute, 1, true, 20, true, otherReqs, true}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 5 * time.Minute, 1, true, 20, true, otherReqs, true}, 10)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "yesterday")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{37000, 1 * time.Hour, 1, true, 20, true, otherReqs, true}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal") // used to be "test" before our memory limits were removed from all queues

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 13 * time.Hour, 1, true, 20, true, otherReqs, true}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 73 * time.Hour, 1, true, 20, true, otherReqs, true}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "basement")
			})

			Convey("MaxQueueTime() returns appropriate times depending on the requirements", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 720)
				So(s.MaxQueueTime(&Requirements{1, 13 * time.Hour, 1, true, 20, true, otherReqs, true}).Minutes(), ShouldEqual, 4320)
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

func TestOpenstack(t *testing.T) {
	// check if we have our special openstack-related variable
	osPrefix := os.Getenv("OS_OS_PREFIX")
	osUser := os.Getenv("OS_OS_USERNAME")
	localUser := os.Getenv("OS_LOCAL_USERNAME")
	flavorRegex := os.Getenv("OS_FLAVOR_REGEX")
	rName := "wr-testing-" + localUser
	config := &ConfigOpenStack{
		ResourceName:         rName,
		OSPrefix:             osPrefix,
		OSUser:               osUser,
		OSRAM:                2048,
		FlavorRegex:          flavorRegex,
		ServerPorts:          []int{22},
		ServerKeepTime:       15 * time.Second,
		StateUpdateFrequency: 1 * time.Second,
		Shell:                "bash",
		MaxInstances:         -1,
	}
	if osPrefix == "" || osUser == "" || localUser == "" {
		Convey("You can't get a new openstack scheduler without the required environment variables", t, func() {
			_, err := New("openstack", config, testLogger)
			So(err, ShouldNotBeNil)
		})
		return
	}
	if flavorRegex == "" {
		SkipConvey("OpenStack scheduler tests are skipped without special OS_FLAVOR_REGEX environment variable being set", t, func() {})
		return
	}
	host, _ := os.Hostname()

	Convey("You can get a new openstack scheduler", t, func() {
		tmpdir, errt := ioutil.TempDir("", "wr_schedulers_openstack_test_output_dir_")
		if errt != nil {
			log.Fatal(errt)
		}
		defer os.RemoveAll(tmpdir)
		config.SavePath = filepath.Join(tmpdir, "os_resources")
		s, errn := New("openstack", config, testLogger)
		So(errn, ShouldBeNil)
		So(s, ShouldNotBeNil)
		defer s.Cleanup()
		oss := s.impl.(*opst)

		possibleReq := &Requirements{100, 1 * time.Minute, 1, true, 1, true, otherReqs, true}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, true, 20, true, otherReqs, true}
		Convey("ReserveTimeout() returns 25 seconds", func() {
			So(s.ReserveTimeout(possibleReq), ShouldEqual, 1)
		})

		// author specific tests, based on hostname, where we know what the
		// expected server types are
		if host == "vr-2-2-02" {
			Convey("determineFlavor() picks the best server flavor depending on given resource requirements", func() {
				flavor, err := oss.determineFlavor(possibleReq, "a")
				So(err, ShouldBeNil)

				if os.Getenv("OS_TENANT_ID") != "" {
					// author's pre-pike install
					So(flavor.ID, ShouldEqual, "2000")
					So(flavor.RAM, ShouldEqual, 1024)
					So(flavor.Disk, ShouldEqual, 8)
					So(flavor.Cores, ShouldEqual, 1)

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 1, true, 20, true, otherReqs, true}, "b")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2000") // we now ignore the 20GB disk requirement

					flavor, err = oss.determineFlavor(oss.reqForSpawn(possibleReq), "c")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2001")
					So(flavor.RAM, ShouldEqual, 4096)

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 2, true, 1, true, otherReqs, true}, "d")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2001")
					So(flavor.RAM, ShouldEqual, 4096)
					So(flavor.Disk, ShouldEqual, 12)
					So(flavor.Cores, ShouldEqual, 2)

					flavor, err = oss.determineFlavor(&Requirements{5000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "e")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2002")
					So(flavor.RAM, ShouldEqual, 16384)
					So(flavor.Disk, ShouldEqual, 20)
					So(flavor.Cores, ShouldEqual, 4)

					flavor, err = oss.determineFlavor(&Requirements{64000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "f")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2003")
					So(flavor.RAM, ShouldEqual, 65536)
					So(flavor.Disk, ShouldEqual, 20)
					So(flavor.Cores, ShouldEqual, 8)

					flavor, err = oss.determineFlavor(&Requirements{66000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "g")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2004")
					So(flavor.RAM, ShouldEqual, 122880)
					So(flavor.Disk, ShouldEqual, 128)
					So(flavor.Cores, ShouldEqual, 16)

					flavor, err = oss.determineFlavor(&Requirements{261000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "h")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2005")
					So(flavor.RAM, ShouldEqual, 262144)
					So(flavor.Disk, ShouldEqual, 128)
					So(flavor.Cores, ShouldEqual, 52)

					flavor, err = oss.determineFlavor(&Requirements{263000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "i")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2006")
					So(flavor.RAM, ShouldEqual, 496640)
					So(flavor.Disk, ShouldEqual, 128)
					So(flavor.Cores, ShouldEqual, 56)

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 3, true, 1, true, otherReqs, true}, "j")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2002")

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 5, true, 1, true, otherReqs, true}, "k")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2003")
				} else {
					// author's pike install
					So(flavor.ID, ShouldEqual, "2000")
					So(flavor.RAM, ShouldEqual, 8600)
					So(flavor.Disk, ShouldEqual, 15)
					So(flavor.Cores, ShouldEqual, 1)

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 1, true, 20, true, otherReqs, true}, "l")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2000")

					flavor, err = oss.determineFlavor(oss.reqForSpawn(possibleReq), "m")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2000")

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 2, true, 1, true, otherReqs, true}, "n")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2001")
					So(flavor.RAM, ShouldEqual, 17200)
					So(flavor.Disk, ShouldEqual, 31)
					So(flavor.Cores, ShouldEqual, 2)

					flavor, err = oss.determineFlavor(&Requirements{30000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "o")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2002")
					So(flavor.RAM, ShouldEqual, 34400)
					So(flavor.Disk, ShouldEqual, 62)
					So(flavor.Cores, ShouldEqual, 4)

					flavor, err = oss.determineFlavor(&Requirements{64000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "p")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2003")
					So(flavor.RAM, ShouldEqual, 68800)
					So(flavor.Disk, ShouldEqual, 125)
					So(flavor.Cores, ShouldEqual, 8)

					flavor, err = oss.determineFlavor(&Requirements{120000, 1 * time.Minute, 1, true, 1, true, otherReqs, true}, "q")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2004")
					So(flavor.RAM, ShouldEqual, 137600)
					So(flavor.Disk, ShouldEqual, 250)
					So(flavor.Cores, ShouldEqual, 16)

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 3, true, 1, true, otherReqs, true}, "r")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2002")

					flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 5, true, 1, true, otherReqs, true}, "s")
					So(err, ShouldBeNil)
					So(flavor.ID, ShouldEqual, "2003")
				}
			})

			Convey("MaxQueueTime() always returns 'infinite'", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 0)
				So(s.MaxQueueTime(&Requirements{1, 13 * time.Hour, 1, true, 20, true, otherReqs, true}).Minutes(), ShouldEqual, 0)
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

		if os.Getenv("OS_TENANT_ID") == "" {
			Convey("Schedule() gives impossible error when reqs don't fit in the requested flavor", func() {
				flavor, err := oss.determineFlavor(possibleReq, "a")
				So(err, ShouldBeNil)
				other := make(map[string]string)
				other["cloud_flavor"] = flavor.Name
				brokenReq := &Requirements{flavor.RAM + 1, 1 * time.Minute, 1, true, 1, true, other, true}
				err = s.Schedule("foo", brokenReq, 1)
				So(err, ShouldNotBeNil)
				serr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(serr.Err, ShouldEqual, ErrImpossible)
			})
		}

		// we need to not actually run the real scheduling tests if we're not
		// running in openstack, because the scheduler will try to ssh to
		// the servers it spawns
		var novaCmd string
		if _, errl := exec.LookPath("openstack"); errl == nil {
			novaCmd = "openstack server"
		} else if _, errl := exec.LookPath("nova"); errl == nil {
			novaCmd = "nova"
		}
		if novaCmd != "" && oss.provider.InCloud() {
			Convey("When running on a new server...", func() {
				// avoid running anything on ourselves, so we actually spawn a
				// new server
				r := oss.reqForSpawn(possibleReq)
				for _, server := range oss.servers {
					if server.Flavor.RAM >= r.RAM {
						r.RAM = server.Flavor.RAM + 1000
					}
				}
				flavor, err := oss.determineFlavor(r, "t")
				So(err, ShouldBeNil)

				existingKeys := make(map[string]bool)
				for key := range oss.servers {
					existingKeys[key] = true
				}

				waitForNewServer := func() *cloud.Server {
					var newServer *cloud.Server
					i := 0
					for {
						i++

						oss.runMutex.Lock()
						oss.serversMutex.RLock()
						for key, server := range oss.servers {
							if !existingKeys[key] {
								newServer = server
							}
						}
						oss.serversMutex.RUnlock()
						oss.runMutex.Unlock()

						if newServer != nil {
							break
						}

						if i == 120 {
							break
						}
						<-time.After(1 * time.Second)
					}
					return newServer
				}

				Convey("Cancelling jobs prior to server boot up still results in correct resource allocation when...", func() {
					// since these tests run small jobs, we need to force a new
					// server in a different way: by having a boot script
					other := make(map[string]string)
					other["cloud_script"] = "true\n"
					testReq := &Requirements{5, 1 * time.Minute, float64(0), true, 0, true, other, true}
					testReq2 := &Requirements{10, 1 * time.Minute, float64(0), true, 0, true, other, true}
					flavor, err = oss.determineFlavor(oss.reqForSpawn(testReq2), "random")
					So(err, ShouldBeNil)

					numJobs := 10
					submitted := make(chan bool, numJobs+1)
					done := make(chan error, numJobs+1)

					runCmds := func(cmd string, req *Requirements, count int) {
						for i := 0; i < count; i++ {
							go func(i int) {
								reserved := make(chan bool)
								go func() {
									<-reserved
									submitted <- true
								}()
								err := oss.runCmd(cmd, req, reserved, "random")
								done <- err
							}(i)
						}
					}

					waitSubmitted := func(count int) {
						for i := 0; i < count; i++ {
							<-submitted
						}
					}

					half := int(numJobs / 2)
					cmd := "sleep 2"
					cmd2 := "sleep 3"
					runCmds(cmd, testReq, half)
					waitSubmitted(half)
					runCmds(cmd2, testReq2, half)
					waitSubmitted(half)
					<-time.After(1 * time.Second)

					destroyedOrComplete := 0
					notNeeded := 0
					checkErrors := func() {
						for i := 0; i < numJobs; i++ {
							err := <-done
							if err == nil {
								destroyedOrComplete++
							} else {
								if strings.Contains(err.Error(), "destruction of server") {
									destroyedOrComplete++
								} else if strings.Contains(err.Error(), "no longer needed") {
									notNeeded++
								}
							}
						}
					}

					Convey("... all commands are cancelled", func() {
						oss.cancelRun(cmd, 0)
						oss.cancelRun(cmd2, 0)

						newServer := waitForNewServer()
						So(newServer == nil, ShouldBeFalse) // avoid race condition read by ShouldNotBeNil

						So(newServer.HasSpaceFor(float64(0), flavor.RAM, 0), ShouldEqual, 1)

						checkErrors()
						So(destroyedOrComplete, ShouldEqual, 0)
						So(notNeeded, ShouldEqual, 10)
					})

					Convey("... all but the first command is cancelled", func() {
						oss.cancelRun(cmd, 1)
						oss.cancelRun(cmd2, 0)

						newServer := waitForNewServer()
						So(newServer == nil, ShouldBeFalse)

						So(newServer.HasSpaceFor(float64(0), flavor.RAM, 0), ShouldEqual, 0)
						So(newServer.HasSpaceFor(float64(0), flavor.RAM-5, 0), ShouldEqual, 1)

						checkErrors()
						So(destroyedOrComplete, ShouldEqual, 1)
						So(notNeeded, ShouldEqual, 9)
					})

					Convey("... all but the first command of a second group is cancelled", func() {
						oss.cancelRun(cmd, 0)
						oss.cancelRun(cmd2, 1)

						newServer := waitForNewServer()
						So(newServer == nil, ShouldBeFalse)

						So(newServer.HasSpaceFor(float64(0), flavor.RAM-5, 0), ShouldEqual, 0)
						So(newServer.HasSpaceFor(float64(0), flavor.RAM-10, 0), ShouldEqual, 1)

						checkErrors()
						So(destroyedOrComplete, ShouldEqual, 1)
						So(notNeeded, ShouldEqual, 9)
					})

					Convey("... all but 1 command of each group are cancelled", func() {
						oss.cancelRun(cmd, 1)
						oss.cancelRun(cmd2, 1)

						newServer := waitForNewServer()
						So(newServer == nil, ShouldBeFalse)

						So(newServer.HasSpaceFor(float64(0), flavor.RAM-10, 0), ShouldEqual, 0)
						So(newServer.HasSpaceFor(float64(0), flavor.RAM-15, 0), ShouldEqual, 1)

						checkErrors()
						So(destroyedOrComplete, ShouldEqual, 2)
						So(notNeeded, ShouldEqual, 8)
					})
				})

				Convey("Changing requirements mid-run doesn't break server resource release", func() {
					inititalRAM := flavor.RAM
					testReq := &Requirements{inititalRAM, 1 * time.Minute, float64(flavor.Cores), true, 0, true, otherReqs, true}

					done := make(chan error, 1)
					go func() {
						reserved := make(chan bool)
						go func() {
							<-reserved
						}()
						err := oss.runCmd("sleep 4", testReq, reserved, "random")
						done <- err
					}()

					newServer := waitForNewServer()
					testReq.RAM = inititalRAM - 5
					So(newServer == nil, ShouldBeFalse)

					err := <-done
					So(err, ShouldBeNil)

					So(newServer.HasSpaceFor(float64(flavor.Cores), inititalRAM, 0), ShouldEqual, 1)
				})

				Convey("The canCount during and after spawning is correct", func() {
					// *** these tests are only going to work if no external process
					// changes resource usage before we finish...
					testReq := &Requirements{flavor.RAM, 1 * time.Minute, float64(flavor.Cores), true, 0, true, otherReqs, true}
					numServers := len(oss.servers)
					can := oss.canCount(testReq, "random")

					done := make(chan bool, 1)
					go func() {
						i := 0
						for {
							i++
							reserved := make(chan bool)
							go func() {
								<-reserved
							}()
							err := oss.runCmd("sleep 4", testReq, reserved, "random")
							if err == nil || i == 3 {
								done <- true
								break
							}
						}
					}()
					<-time.After(3 * time.Second)

					oss.runMutex.Lock()
					oss.serversMutex.RLock()
					So(len(oss.servers)+len(oss.standins), ShouldEqual, numServers+1)
					oss.serversMutex.RUnlock()
					oss.runMutex.Unlock()
					So(oss.canCount(testReq, "random2"), ShouldEqual, can-1)

					<-done

					oss.serversMutex.Lock()
					for sid, server := range oss.servers {
						if server.Destroyed() {
							delete(oss.servers, sid)
						}
					}
					So(len(oss.servers), ShouldEqual, numServers+1)
					oss.serversMutex.Unlock()
					So(oss.canCount(testReq, "random3"), ShouldEqual, can)

					<-time.After(20 * time.Second)

					oss.serversMutex.Lock()
					for sid, server := range oss.servers {
						if server.Destroyed() {
							delete(oss.servers, sid)
						}
					}
					So(len(oss.servers), ShouldEqual, numServers)
					oss.serversMutex.Unlock()
					So(oss.canCount(testReq, "random4"), ShouldEqual, can)
				})
			})

			Convey("Schedule() lets you...", func() {
				oFile := filepath.Join(tmpdir, "out")

				Convey("Run jobs that use a NFS shared disk", func() {
					cmd := "touch /shared/test1"
					other := make(map[string]string)
					other["cloud_shared"] = "true"
					localReq := &Requirements{100, 1 * time.Minute, 1, true, 1, true, other, true}
					err := s.Schedule(cmd, localReq, 1)
					So(err, ShouldBeNil)

					remoteReq := oss.reqForSpawn(localReq)
					for _, server := range oss.servers {
						if server.Flavor.RAM >= remoteReq.RAM {
							remoteReq.RAM = server.Flavor.RAM + 1000
						}
					}
					remoteReq.Other = other
					cmd = "touch /shared/test2"
					err = s.Schedule(cmd, remoteReq, 1)
					So(err, ShouldBeNil)

					So(s.Busy(), ShouldBeTrue)
					So(waitToFinish(s, 240, 1000), ShouldBeTrue)

					_, err = os.Stat("/shared/test1")
					So(err, ShouldBeNil)
					_, err = os.Stat("/shared/test2")
					So(err, ShouldBeNil)

					err = os.Remove("/shared/test1")
					So(err, ShouldBeNil)
					err = os.Remove("/shared/test2")
					So(err, ShouldBeNil)
				})

				if flavorRegex == `^m.*$` && os.Getenv("OS_TENANT_ID") == "" {
					Convey("Run a job on a specific flavor", func() {
						cmd := "sleep 10"
						other := make(map[string]string)
						other["cloud_flavor"] = "o2.small"
						thisReq := &Requirements{100, 1 * time.Minute, 1, true, 1, true, other, true}
						err := s.Schedule(cmd, thisReq, 1)
						So(err, ShouldBeNil)
						So(s.Busy(), ShouldBeTrue)

						spawnedCh := make(chan int)
						stopCh := make(chan bool)
						go func() {
							max := 0
							ticker := time.NewTicker(5 * time.Second)
							for {
								select {
								case <-ticker.C:
									novaCount := novaCountServers(novaCmd, rName, "", "o2.small")
									if novaCount > max {
										max = novaCount
									}
									continue
								case <-stopCh:
									ticker.Stop()
									spawnedCh <- max
									return
								}
							}
						}()

						So(waitToFinish(s, 120, 1000), ShouldBeTrue)
						stopCh <- true
						spawned := <-spawnedCh
						close(spawnedCh)
						So(spawned, ShouldEqual, 1)
					})
				} else {
					SkipConvey("Skipping author's flavor test", func() {})
				}

				Convey("Run jobs with no inputs/outputs", func() {
					// on authors setup, the following count is sufficient to
					// test spawning instances over the quota in the test
					// environment if we reserve 26 cores per job
					count := 18
					eta := 200 // if it takes longer than this, it's a likely indicator of a bug where it has actually stalled on a stuck lock
					cmd := "sleep 10"
					oReqs := make(map[string]string)
					thisReq := &Requirements{100, 1 * time.Minute, 26, true, 1, true, oReqs, true}
					err := s.Schedule(cmd, thisReq, count)
					So(err, ShouldBeNil)
					So(s.Busy(), ShouldBeTrue)

					spawnedCh := make(chan int)
					stopCh := make(chan bool)
					go func() {
						max := 0
						ticker := time.NewTicker(5 * time.Second)
						for {
							select {
							case <-ticker.C:
								novaCount := novaCountServers(novaCmd, rName, "")
								if novaCount > max {
									max = novaCount
								}
								continue
							case <-stopCh:
								ticker.Stop()
								spawnedCh <- max
								return
							}
						}
					}()

					So(waitToFinish(s, eta, 1000), ShouldBeTrue)
					stopCh <- true
					spawned := <-spawnedCh
					close(spawnedCh)
					So(spawned, ShouldBeBetweenOrEqual, 2, count)

					foundServers := novaCountServers(novaCmd, rName, "")
					So(foundServers, ShouldBeBetweenOrEqual, 1, int(eta/10)) // (assuming a ~10s spawn time)

					// after the last run, they are all auto-destroyed
					<-time.After(20 * time.Second)

					foundServers = novaCountServers(novaCmd, rName, "")
					So(foundServers, ShouldEqual, 0)

					// *** not really confirming that the cmds actually ran on
					// the spawned servers
				})

				// *** we really need to mock OpenStack instead of setting
				// these debug package variables...
				Convey("Run everything even when a server fails to spawn", func() {
					debugCounter = 0
					debugEffect = "failFirstSpawn"
					oReqs := make(map[string]string)
					newReq := &Requirements{100, 1 * time.Minute, 1, true, 1, true, oReqs, true}
					newCount := 3
					eta := 120
					cmd := "sleep 10"
					err := s.Schedule(cmd, newReq, newCount)
					So(err, ShouldBeNil)
					So(s.Busy(), ShouldBeTrue)
					So(waitToFinish(s, eta, 1000), ShouldBeTrue)
				})

				Convey("Run jobs and have servers still self-terminate when a server is slow to spawn", func() {
					debugCounter = 0
					debugEffect = "slowSecondSpawn"
					oReqs := make(map[string]string)
					newReq := &Requirements{100, 1 * time.Minute, 1, true, 1, true, oReqs, true}
					newCount := 3
					eta := 120
					cmd := "sleep 10"
					err := s.Schedule(cmd, newReq, newCount)
					So(err, ShouldBeNil)
					So(s.Busy(), ShouldBeTrue)
					So(waitToFinish(s, eta, 1000), ShouldBeTrue)

					<-time.After(20 * time.Second)

					foundServers := novaCountServers(novaCmd, rName, "")
					So(foundServers, ShouldEqual, 0)

					debugCounter = 0
					debugEffect = ""
				})

				// *** test if we have a Centos 7 image to use...
				if osPrefix != "CentOS-7" {
					oReqs := make(map[string]string)
					oReqs["cloud_os"] = "CentOS-7"
					oReqs["cloud_user"] = "centos"
					oReqs["cloud_os_ram"] = "4096"

					Convey("Override the default os image and ram", func() {
						newReq := &Requirements{100, 1 * time.Minute, 1, true, 1, true, oReqs, true}
						newCount := 3
						eta := 120
						cmd := "sleep 10 && (echo override > " + oFile + ") || true"
						err := s.Schedule(cmd, newReq, newCount)
						So(err, ShouldBeNil)
						So(s.Busy(), ShouldBeTrue)

						spawned := 0
						var ssync sync.Mutex
						go func() {
							ticker := time.NewTicker(1 * time.Second)
							limit := time.After(time.Duration(eta-5) * time.Second)
							for {
								select {
								case <-ticker.C:
									ssync.Lock()
									spawned = novaCountServers(novaCmd, rName, oReqs["cloud_os"])
									if spawned > 0 {
										ticker.Stop()
										ssync.Unlock()
										return
									}
									ssync.Unlock()
									continue
								case <-limit:
									ticker.Stop()
									return
								}
							}
						}()

						So(waitToFinish(s, eta, 1000), ShouldBeTrue)
						ssync.Lock()
						So(spawned, ShouldBeBetweenOrEqual, 1, newCount)
						ssync.Unlock()

						<-time.After(20 * time.Second)

						foundServers := novaCountServers(novaCmd, rName, "")
						So(foundServers, ShouldEqual, 0)

						// none of the cmds should have run on the local machine
						_, err = os.Stat(oFile)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)
					})
				}

				numCores := 5
				oReqsm := make(map[string]string)
				multiCoreFlavor, err := oss.determineFlavor(&Requirements{1024, 1 * time.Minute, float64(numCores), true, 0, true, oReqsm, true}, "u")
				if err == nil && multiCoreFlavor.Cores >= numCores {
					oReqs := make(map[string]string)
					oReqs["cloud_os_ram"] = strconv.Itoa(multiCoreFlavor.RAM)
					jobReq := &Requirements{int(multiCoreFlavor.RAM / numCores), 1 * time.Minute, 1, true, 0, true, oReqs, true}
					confirmFlavor, err := oss.determineFlavor(oss.reqForSpawn(jobReq), "v")
					if err == nil && confirmFlavor.Cores >= numCores {
						Convey("Run multiple jobs at once on multi-core servers", func() {
							cmd := "sleep 30"
							jobReq := &Requirements{int(multiCoreFlavor.RAM / numCores), 1 * time.Minute, 1, true, 0, true, oReqs, true}
							err = s.Schedule(cmd, jobReq, numCores)
							So(err, ShouldBeNil)
							So(s.Busy(), ShouldBeTrue)

							waitSecs := 150
							spawnedCh := make(chan int, 1)
							go func() {
								maxSpawned := 0
								ticker := time.NewTicker(1 * time.Second)
								limit := time.After(time.Duration(waitSecs-5) * time.Second)
								for {
									select {
									case <-ticker.C:
										spawned := novaCountServers(novaCmd, rName, oReqs["cloud_os"])
										if spawned > maxSpawned {
											maxSpawned = spawned
										}
										continue
									case <-limit:
										ticker.Stop()
										spawnedCh <- maxSpawned
										return
									}
								}
							}()

							// wait for enough time to have spawned a server
							// and run the commands in parallel, but not
							// sequentially *** but how long does it take to
							// spawn?! (50s in authors test area, but this
							// will vary...) we need better confirmation of
							// parallel run...
							So(waitToFinish(s, waitSecs, 1000), ShouldBeTrue)
							spawned := <-spawnedCh
							So(spawned, ShouldEqual, 1)
						})
					} else {
						SkipConvey("Skipping multi-core server tests due to lack of suitable multi-core server flavors", func() {})
					}
				} else {
					SkipConvey("Skipping multi-core server tests due to lack of suitable multi-core server flavors", func() {})
				}

				// *** when we have mocks, need to test that flavor sets work
				// as expected by filling up hardware in one set and seeing that
				// we fail over to the other set etc.
			})

			// wait a while for any remaining jobs to finish
			So(waitToFinish(s, 60, 1000), ShouldBeTrue)
		} else {
			SkipConvey("Actual OpenStack scheduling tests are skipped if not in OpenStack with nova or openstack installed", func() {})
		}
	})
}

func getInfoOfFilesInDir(tmpdir string, expected int) []os.FileInfo {
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
	return files
}

func testDirForFiles(tmpdir string, expected int) (numfiles int) {
	return len(getInfoOfFilesInDir(tmpdir, expected))
}

func mtimesOfFilesInDir(tmpdir string, expected int) []time.Time {
	files := getInfoOfFilesInDir(tmpdir, expected)
	var times []time.Time
	for _, info := range files {
		times = append(times, info.ModTime())
		os.Remove(filepath.Join(tmpdir, info.Name()))
	}
	return times
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
	answer := <-done
	return answer
}

func novaCountServers(novaCmd string, rName, osPrefix string, flavor ...string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var extra string
	if len(flavor) == 1 {
		extra = "--flavor " + flavor[0] + " "
	}

	cmdStr := novaCmd + " list " + extra
	if osPrefix == "" {
		cmdStr += "| grep -c "
	} else {
		cmdStr += "| grep "
	}
	cmdStr += rName
	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		log.Printf("exec of [%s] timed out\n", cmdStr)
		return 0
	}
	if err != nil {
		// uncomment if debugging failures where count is always 0:
		// log.Printf("cmd [%s] failed: %s\n", cmdStr, err)
		return 0
	}

	if osPrefix == "" {
		count, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err == nil {
			return count
		}
		log.Printf("Atoi following [%s] failed: %s\n", cmdStr, err)
	} else {
		r := regexp.MustCompile(rName + "-\\S+")
		count := 0
		for _, name := range r.FindAll(out, -1) {
			showCmdStr := novaCmd + " show " + string(name) + " | grep image"
			showCmd := exec.Command("bash", "-c", showCmdStr)
			showOut, err := showCmd.Output()
			if err == nil {
				if strings.Contains(string(showOut), osPrefix) {
					count++
				}
			} else {
				log.Printf("cmd [%s] failed: %s\n", showCmdStr, err)
			}
		}
		return count
	}
	return 0
}
