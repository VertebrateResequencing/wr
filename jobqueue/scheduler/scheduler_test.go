// Copyright © 2016-2017 Genome Research Limited
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
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var maxCPU = runtime.NumCPU()
var otherReqs = make(map[string]string)

func TestLocal(t *testing.T) {
	runtime.GOMAXPROCS(maxCPU)

	Convey("You can get a new local scheduler", t, func() {
		s, err := New("local", &ConfigLocal{"bash"})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{1, 1 * time.Second, 1, 20, otherReqs}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, 20, otherReqs}

		Convey("ReserveTimeout() returns 1 second", func() {
			So(s.ReserveTimeout(), ShouldEqual, 1)
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
			So(testReq.Stringify(), ShouldEqual, "300:120:2:0:foo=bar:goo=lar")
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

				numfiles := testDirForFiles(tmpdir, maxCPU+maxCPU)
				So(numfiles, ShouldEqual, maxCPU+maxCPU)

				<-time.After(750*time.Millisecond + overhead)

				numfiles = testDirForFiles(tmpdir, maxCPU+count)
				So(numfiles, ShouldEqual, maxCPU+count)
				numfiles = testDirForFiles(tmpdir2, maxCPU+count)
				if numfiles < maxCPU+count {
					So(s.Busy(), ShouldBeTrue)
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
			_, err := New("lsf", &ConfigLSF{"development", "bash"})
			So(err, ShouldNotBeNil)
		})
		return
	}

	host, _ := os.Hostname()
	Convey("You can get a new lsf scheduler", t, func() {
		s, err := New("lsf", &ConfigLSF{"development", "bash"})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{100, 1 * time.Minute, 1, 20, otherReqs}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, 20, otherReqs}

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

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 5 * time.Minute, 1, 20, otherReqs}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 5 * time.Minute, 1, 20, otherReqs}, 10)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "yesterday")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{37000, 1 * time.Hour, 1, 20, otherReqs}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal") // used to be "test" before our memory limits were removed from all queues

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long")

				queue, err = s.impl.(*lsf).determineQueue(&Requirements{1, 73 * time.Hour, 1, 20, otherReqs}, 0)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "basement")
			})

			Convey("MaxQueueTime() returns appropriate times depending on the requirements", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 720)
				So(s.MaxQueueTime(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs}).Minutes(), ShouldEqual, 4320)
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
		ResourceName:   rName,
		OSPrefix:       osPrefix,
		OSUser:         osUser,
		OSRAM:          2048,
		FlavorRegex:    flavorRegex,
		ServerPorts:    []int{22},
		ServerKeepTime: 15 * time.Second,
		Shell:          "bash",
		MaxInstances:   -1,
	}
	if osPrefix == "" || osUser == "" || localUser == "" {
		Convey("You can't get a new openstack scheduler without the required environment variables", t, func() {
			_, err := New("openstack", config)
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
		tmpdir, err := ioutil.TempDir("", "wr_schedulers_openstack_test_output_dir_")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(tmpdir)
		config.SavePath = filepath.Join(tmpdir, "os_resources")

		s, err := New("openstack", config)
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)
		defer s.Cleanup()
		oss := s.impl.(*opst)
		//oss.debugMode = true

		possibleReq := &Requirements{100, 1 * time.Minute, 1, 1, otherReqs}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, 20, otherReqs}

		Convey("ReserveTimeout() returns 25 seconds", func() {
			So(s.ReserveTimeout(), ShouldEqual, 1)
		})

		// author specific tests, based on hostname, where we know what the
		// expected server types are
		if host == "vr-2-2-02" {
			Convey("determineFlavor() picks the best server flavor depending on given resource requirements", func() {
				flavor, err := oss.determineFlavor(possibleReq)
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "1")
				So(flavor.RAM, ShouldEqual, 512)
				So(flavor.Disk, ShouldEqual, 1)
				So(flavor.Cores, ShouldEqual, 1)

				flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 1, 20, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "1") // we now ignore the 20GB disk requirement

				flavor, err = oss.determineFlavor(oss.reqForSpawn(possibleReq))
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2")
				So(flavor.RAM, ShouldEqual, 2048)

				flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 2, 1, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "95fc0191-e4d1-4188-858e-c2131f177913")
				So(flavor.RAM, ShouldEqual, 2048)
				So(flavor.Disk, ShouldEqual, 8)
				So(flavor.Cores, ShouldEqual, 2)

				flavor, err = oss.determineFlavor(&Requirements{3000, 1 * time.Minute, 1, 20, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "3")
				So(flavor.RAM, ShouldEqual, 4096)
				So(flavor.Disk, ShouldEqual, 40)
				So(flavor.Cores, ShouldEqual, 2)

				flavor, err = oss.determineFlavor(&Requirements{8000, 1 * time.Minute, 1, 20, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "4")
				So(flavor.RAM, ShouldEqual, 8192)
				So(flavor.Disk, ShouldEqual, 80)
				So(flavor.Cores, ShouldEqual, 4)

				flavor, err = oss.determineFlavor(&Requirements{16000, 1 * time.Minute, 1, 20, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2101")
				So(flavor.RAM, ShouldEqual, 16384)
				So(flavor.Disk, ShouldEqual, 80)
				So(flavor.Cores, ShouldEqual, 4)

				flavor, err = oss.determineFlavor(&Requirements{100, 1 * time.Minute, 8, 20, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "5")
				So(flavor.RAM, ShouldEqual, 16384)
				So(flavor.Disk, ShouldEqual, 160)
				So(flavor.Cores, ShouldEqual, 8)

				flavor, err = oss.determineFlavor(&Requirements{32000, 1 * time.Minute, 1, 1, otherReqs})
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2102")
				So(flavor.RAM, ShouldEqual, 32768)
				So(flavor.Disk, ShouldEqual, 80)
				So(flavor.Cores, ShouldEqual, 8)
			})

			Convey("MaxQueueTime() always returns 'infinite'", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 0)
				So(s.MaxQueueTime(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs}).Minutes(), ShouldEqual, 0)
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

		// we need to not actually run the real scheduling tests if we're not
		// running in openstack, because the scheduler will try to ssh to
		// the servers it spawns
		_, err = exec.LookPath("nova")
		if err == nil && oss.provider.InCloud() {
			Convey("Schedule() lets you schedule some jobs with no inputs/outputs", func() {
				oFile := filepath.Join(tmpdir, "out")

				Convey("It eventually runs them all", func() {
					// on authors setup, running the test from a 2 cpu cloud
					// instance, the following count is sufficient to test
					// spawning instances over the quota in the test environment
					count := 130
					eta := 200 // if it takes longer than this, it's a likely indicator of a bug where it has actually stalled on a stuck lock
					cmd := "sleep 10 && (echo default > " + oFile + ") || true"
					err = s.Schedule(cmd, possibleReq, count)
					So(err, ShouldBeNil)
					So(s.Busy(), ShouldBeTrue)

					spawnedCh := make(chan int)
					go func() {
						<-time.After(time.Duration(int(eta/2)) * time.Second)
						spawnedCh <- novaCountServers(rName, "")
						return
					}()

					So(waitToFinish(s, eta, 1000), ShouldBeTrue)
					spawned := <-spawnedCh
					close(spawnedCh)
					So(spawned, ShouldBeBetweenOrEqual, 4, count)

					foundServers := novaCountServers(rName, "")
					So(foundServers, ShouldBeBetweenOrEqual, 1, int(eta/10)) // (assuming a ~10s spawn time)

					// after the last run, they are all auto-destroyed
					<-time.After(30 * time.Second)

					foundServers = novaCountServers(rName, "")
					So(foundServers, ShouldEqual, 0)

					// at least one of the cmds should have run on the local
					// machine
					_, err := os.Stat(oFile)
					So(err, ShouldBeNil)
				})

				// *** test if we have a Centos 7 image to use...
				if osPrefix != "Centos 7 (2016-09-06)" {
					oReqs := make(map[string]string)
					oReqs["cloud_os"] = "Centos 7 (2016-09-06)"
					oReqs["cloud_user"] = "centos"
					oReqs["cloud_os_ram"] = "4096"

					Convey("They can be run again, overriding the default os image and ram", func() {
						newReq := &Requirements{100, 1 * time.Minute, 1, 1, oReqs}
						newCount := 3
						eta := 120
						cmd := "sleep 10 && (echo override > " + oFile + ") || true"
						err = s.Schedule(cmd, newReq, newCount)
						So(err, ShouldBeNil)
						So(s.Busy(), ShouldBeTrue)

						spawned := 0
						go func() {
							ticker := time.NewTicker(1 * time.Second)
							limit := time.After(time.Duration(eta-5) * time.Second)
							for {
								select {
								case <-ticker.C:
									spawned = novaCountServers(rName, oReqs["cloud_os"])
									if spawned > 0 {
										ticker.Stop()
										return
									}
									continue
								case <-limit:
									ticker.Stop()
									return
								}
							}
						}()

						So(waitToFinish(s, eta, 1000), ShouldBeTrue)
						So(spawned, ShouldBeBetweenOrEqual, 1, newCount)

						<-time.After(30 * time.Second)

						foundServers := novaCountServers(rName, "")
						So(foundServers, ShouldEqual, 0)

						// none of the cmds should have run on the local machine
						_, err := os.Stat(oFile)
						So(err, ShouldNotBeNil)
						So(os.IsNotExist(err), ShouldBeTrue)
					})

					numCores := 4
					multiCoreFlavor, err := oss.determineFlavor(&Requirements{1024, 1 * time.Minute, numCores, 6 * numCores, oReqs})
					if err == nil && multiCoreFlavor.Cores >= numCores {
						oReqs["cloud_os_ram"] = strconv.Itoa(multiCoreFlavor.RAM)
						jobReq := &Requirements{int(multiCoreFlavor.RAM / numCores), 1 * time.Minute, 1, 6, oReqs}
						confirmFlavor, err := oss.determineFlavor(oss.reqForSpawn(jobReq))
						if err == nil && confirmFlavor.Cores >= numCores {
							Convey("You can run multiple jobs at once on multi-core servers", func() {
								cmd := "sleep 30"
								jobReq := &Requirements{int(multiCoreFlavor.RAM / numCores), 1 * time.Minute, 1, int(multiCoreFlavor.Disk / numCores), oReqs}
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
											spawned := novaCountServers(rName, oReqs["cloud_os"])
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
								// and run both commands in parallel, but not
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
				}

				// *** I have no tests for when servers fail to start...
			})

			Convey("Schedule() can run commands with different hardware requirements while dropping the count", func() {
				// with the ~instant complete jobs combined with the wait on
				// bringing up a new server, this is supposed to be able to
				// trigger a dropping count bug, but doesn't, but we're keeping
				// it anyway. See jobqueue_test.go for a similar test that did
				// manage to trigger the bug.
				err = s.Schedule("echo 2048:1:0", &Requirements{2048, 1 * time.Minute, 1, 0, otherReqs}, 1)
				So(err, ShouldBeNil)
				err = s.Schedule("echo 1024:2:0", &Requirements{1024, 1 * time.Minute, 2, 0, otherReqs}, 1)
				So(err, ShouldBeNil)
				err = s.Schedule("echo 1024:1:20", &Requirements{1024, 1 * time.Minute, 2, 20, otherReqs}, 1)
				So(err, ShouldBeNil)

				dropReq := &Requirements{1024, 1 * time.Minute, 1, 0, otherReqs}
				err = s.Schedule("echo 1024:1:0 && sleep 1", dropReq, 1)
				So(err, ShouldBeNil)
				dropCmd := fmt.Sprintf("echo 1024:1:0 && mkdir -p %s && perl -e 'use File::Temp qw/tempfile/; ($fh, $fn) = tempfile(DIR => q{%s}, SUFFIX => q/.wrst/); print $fh qq/test\\n/; close($fh)'", tmpdir, tmpdir)
				dropCount := 96

				stop := make(chan bool)
				completedLocally := 0
				prevRemaining := dropCount
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							files, err := ioutil.ReadDir(tmpdir)
							if err == nil {
								count := 0
								for _, file := range files {
									if strings.HasSuffix(file.Name(), ".wrst") {
										count++
									}
								}
								completedLocally = count

								remaining := dropCount - count
								if remaining < prevRemaining {
									s.Schedule(dropCmd, dropReq, remaining)
									if remaining == 0 {
										ticker.Stop()
										return
									}
									prevRemaining = remaining
								}
							}
							continue
						case <-stop:
							ticker.Stop()
							return
						}
					}
				}()

				err = s.Schedule(dropCmd, dropReq, dropCount)
				So(err, ShouldBeNil)
				So(s.Busy(), ShouldBeTrue)

				eta := 150
				So(waitToFinish(s, eta, 1000), ShouldBeTrue)
				stop <- true
				So(completedLocally, ShouldBeBetweenOrEqual, 50, 97)

				<-time.After(5 * time.Second)
				foundServers := novaCountServers(rName, "")
				So(foundServers, ShouldBeBetweenOrEqual, 0, 3)

				// after the last run, they are all auto-destroyed
				if foundServers > 0 {
					<-time.After(25 * time.Second)
					foundServers = novaCountServers(rName, "")
					So(foundServers, ShouldEqual, 0)
				}
			})

			// wait a while for any remaining jobs to finish
			So(waitToFinish(s, 60, 1000), ShouldBeTrue)
		} else {
			SkipConvey("Actual OpenStack scheduling tests are skipped if not in OpenStack with nova installed", func() {})
		}
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
	answer := <-done
	return answer
}

func novaCountServers(rName, osPrefix string) int {
	if osPrefix == "" {
		cmd := exec.Command("bash", "-c", "nova list | grep -c "+rName)
		out, err := cmd.Output()
		if err == nil {
			count, err := strconv.Atoi(strings.TrimSpace(string(out)))
			if err == nil {
				return count
			}
		}
	} else {
		cmd := exec.Command("bash", "-c", "nova list | grep "+rName)
		out, err := cmd.Output()
		if err == nil {
			r := regexp.MustCompile(rName + "-\\S+")
			count := 0
			for _, name := range r.FindAll(out, -1) {
				showCmd := exec.Command("bash", "-c", "nova show "+string(name)+" | grep image")
				showOut, err := showCmd.Output()
				if err == nil {
					if strings.Contains(string(showOut), osPrefix) {
						count++
					}
				}
			}
			return count
		}
	}
	return 0
}
