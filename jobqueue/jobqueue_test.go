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

package jobqueue

import (
	"flag"
	"fmt"
	"github.com/VertebrateResequencing/wr/internal"
	// "github.com/boltdb/bolt"
	"github.com/sevlyar/go-daemon"
	. "github.com/smartystreets/goconvey/convey"
	// "github.com/ugorji/go/codec"
	"io/ioutil"
	"log"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	// "sync"
	"syscall"
	"testing"
	"time"
)

var runnermode bool
var queuename string
var schedgrp string
var runnermodetmpdir string
var rdeployment string
var rserver string
var rtimeout int
var maxmins int

func init() {
	flag.BoolVar(&runnermode, "runnermode", false, "enable to disable tests and act as a 'runner' client")
	flag.StringVar(&queuename, "queue", "", "queue for runnermode")
	flag.StringVar(&schedgrp, "schedgrp", "", "schedgrp for runnermode")
	flag.StringVar(&rdeployment, "rdeployment", "", "deployment for runnermode")
	flag.StringVar(&rserver, "rserver", "", "server for runnermode")
	flag.IntVar(&rtimeout, "rtimeout", 1, "reserve timeout for runnermode")
	flag.IntVar(&maxmins, "maxmins", 0, "maximum mins allowed for  runnermode")
	flag.StringVar(&runnermodetmpdir, "tmpdir", "", "tmp dir for runnermode")
}

func TestJobqueue(t *testing.T) {
	runtime.GOMAXPROCS(runtime.NumCPU())
	if runnermode {
		// we have a full test of Serve() below that needs a client executable;
		// we say this test script is that exe, and when --runnermode is passed
		// to us we skip all tests and just act like a runner
		runner()
		return
	}

	// load our config to know where our development manager port is supposed to
	// be; we'll use that to test jobqueue
	config := internal.ConfigLoad("development", true)
	port := config.ManagerPort
	webport := config.ManagerWeb
	addr := "localhost:" + port

	ServerLogClientErrors = false
	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelay = 100 * time.Millisecond
	clientConnectTime := 150 * time.Millisecond
	rc := ""

	// these tests need the server running in it's own pid so we can test signal
	// handling in the client; to get the server in its own pid we need to
	// "fork", and that means these must be the first tests to run or else we
	// won't know in our parent process when our desired server is ready
	Convey("Once a jobqueue server is up as a daemon", t, func() {
		ServerItemTTR = 100 * time.Millisecond
		ClientTouchInterval = 50 * time.Millisecond

		context := &daemon.Context{
			PidFileName: config.ManagerPidFile,
			PidFilePerm: 0644,
			WorkDir:     "/",
			Umask:       config.ManagerUmask,
		}
		child, err := context.Reborn()
		if err != nil {
			log.Fatalf("failed to daemonize for the initial test: %s (you probably need to `wr manager stop`)", err)
		}
		if child == nil {
			// daemonized child, that will run until signalled to stop
			defer context.Release()

			//*** we need a log rotation scheme in place to have this...
			// logfile, errlog := os.OpenFile(config.ManagerLogFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
			// if errlog == nil {
			// 	defer logfile.Close()
			// 	log.SetOutput(logfile)
			// }

			server, msg, err := Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
			if err != nil {
				log.Fatalf("test daemon failed to start: %s\n", err)
			}
			if msg != "" {
				log.Println(msg)
			}

			// we'll Block() later, but just in case the parent tests bomb out
			// without killing us, we'll stop after 10s
			go func() {
				<-time.After(10 * time.Second)
				log.Println("test daemon stopping after 10s")
				server.Stop()
			}()

			log.Println("test daemon up, will block")

			// wait until we are killed
			server.Block()
			log.Println("test daemon exiting")
			os.Exit(0)
		}
		// parent; wait a while for our child to bring up the server
		defer syscall.Kill(child.Pid, syscall.SIGTERM)
		jq, err := Connect(addr, "test_queue", 10*time.Second)
		So(err, ShouldBeNil)

		sstats, err := jq.ServerStats()
		So(err, ShouldBeNil)
		So(sstats.ServerInfo.PID, ShouldEqual, child.Pid)

		Convey("You can set up a long-running job for execution", func() {
			cmd := "perl -e 'for (1..3) { sleep(1) }'"
			cmd2 := "perl -e 'for (2..4) { sleep(1) }'"
			var jobs []*Job
			jobs = append(jobs, NewJob(cmd, "/tmp", "fake_group", 10, 4*time.Second, 1, uint8(0), uint8(0), "3secs_pass"))
			jobs = append(jobs, NewJob(cmd2, "/tmp", "fake_group", 10, 1*time.Second, 1, uint8(0), uint8(0), "3secs_fail"))
			RecSecRound = 1
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, "reserved")

			job2, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job2.Cmd, ShouldEqual, cmd2)
			So(job2.State, ShouldEqual, "reserved")

			Convey("Signals are handled during execution, and we can see when jobs take too long", func() {
				go func() {
					<-time.After(2 * time.Second)
					syscall.Kill(os.Getpid(), syscall.SIGTERM)
				}()

				j1worked := make(chan bool)
				go func() {
					err = jq.Execute(job, config.RunnerExecShell)
					if err != nil {
						if jqerr, ok := err.(Error); ok && jqerr.Err == FailReasonSignal && job.State == "delayed" && job.Exited && job.Exitcode == -1 && job.FailReason == FailReasonSignal {
							j1worked <- true
						}
					}
					j1worked <- false
				}()

				j2worked := make(chan bool)
				go func() {
					err = jq.Execute(job2, config.RunnerExecShell)
					if err != nil {
						if jqerr, ok := err.(Error); ok && jqerr.Err == FailReasonTime && job2.State == "delayed" && job2.Exited && job2.Exitcode == -1 && job2.FailReason == FailReasonTime {
							j2worked <- true
						}
					}
					j2worked <- false
				}()

				So(<-j1worked, ShouldBeTrue)
				So(<-j2worked, ShouldBeTrue)

				jq2, err := Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldBeNil)
				job, err = jq2.GetByCmd(cmd, "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, cmd)
				So(job.State, ShouldEqual, "delayed")
				So(job.FailReason, ShouldEqual, FailReasonSignal)

				job2, err = jq2.GetByCmd(cmd2, "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, cmd2)
				So(job2.State, ShouldEqual, "delayed")
				So(job2.FailReason, ShouldEqual, FailReasonTime)
				So(job2.Time.Seconds(), ShouldEqual, 3601)

				// all signals handled the same way, so no need for further
				// tests
			})

			RecSecRound = 1800 // revert back to normal
		})

		Reset(func() {
			syscall.Kill(child.Pid, syscall.SIGTERM)
		})
	})

	ServerItemTTR = 1 * time.Second
	ClientTouchInterval = 500 * time.Millisecond

	var server *Server
	var err error
	Convey("Without the jobserver being up, clients can't connect and time out", t, func() {
		_, err = Connect(addr, "test_queue", clientConnectTime)
		So(err, ShouldNotBeNil)
		jqerr, ok := err.(Error)
		So(ok, ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, ErrNoServer)
	})

	Convey("Once the jobqueue server is up", t, func() {
		server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
		So(err, ShouldBeNil)

		Convey("You can connect to the server and add jobs to the queue", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			sstats, err := jq.ServerStats()
			So(err, ShouldBeNil)
			So(sstats.ServerInfo.Port, ShouldEqual, port)
			So(sstats.ServerInfo.PID, ShouldBeGreaterThan, 0)
			So(sstats.ServerInfo.Deployment, ShouldEqual, "development")

			var jobs []*Job
			for i := 0; i < 10; i++ {
				pri := i
				if i == 7 {
					pri = 4
				} else if i == 4 {
					pri = 7
				}
				jobs = append(jobs, NewJob(fmt.Sprintf("test cmd %d", i), "/fake/cwd", "fake_group", 1024, 4*time.Hour, 1, uint8(0), uint8(pri), "manually_added"))
			}
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 10)
			So(already, ShouldEqual, 0)

			Convey("You can't add the same jobs to the queue again", func() {
				inserts, already, err := jq.Add(jobs)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 0)
				So(already, ShouldEqual, 10)
			})

			Convey("You can get back jobs you've just added", func() {
				job, err := jq.GetByCmd("test cmd 3", "/fake/cwd", false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, "test cmd 3")
				So(job.State, ShouldEqual, "ready")

				job, err = jq.GetByCmd("test cmd x", "/fake/cwd", false, false)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)

				var ccs [][2]string
				for i := 0; i < 10; i++ {
					ccs = append(ccs, [2]string{fmt.Sprintf("test cmd %d", i), "/fake/cwd"})
				}
				jobs, err := jq.GetByCmds(ccs)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)
				for i, job := range jobs {
					So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
					So(job.State, ShouldEqual, "ready")
				}

				jobs, err = jq.GetByRepGroup("manually_added", 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)

				jobs, err = jq.GetByRepGroup("foo", 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 0)
			})

			Convey("You can store their (fake) runtime stats and get recommendations", func() {
				for index, job := range jobs {
					job.Peakmem = index + 1
					job.starttime = time.Now()
					job.endtime = job.starttime.Add(time.Duration(index+1) * time.Second)
					server.db.updateJobAfterExit(job, []byte{}, []byte{})
				}
				<-time.After(100 * time.Millisecond)
				rmem, err := server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 100)
				rtime, err := server.db.recommendedReqGroupTime("fake_group")
				So(err, ShouldBeNil)
				So(rtime, ShouldEqual, 1800)

				for i := 11; i <= 100; i++ {
					job := NewJob(fmt.Sprintf("test cmd %d", i), "/fake/cwd", "fake_group", 1024, 4*time.Hour, 1, uint8(0), uint8(0), "manually_added")
					job.Peakmem = i * 100
					job.starttime = time.Now()
					job.endtime = job.starttime.Add(time.Duration(i*100) * time.Second)
					server.db.updateJobAfterExit(job, []byte{}, []byte{})
				}
				<-time.After(100 * time.Millisecond)
				rmem, err = server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 9500)
				rtime, err = server.db.recommendedReqGroupTime("fake_group")
				So(err, ShouldBeNil)
				So(rtime, ShouldEqual, 10800)
			})

			Convey("You can reserve jobs from the queue in the correct order", func() {
				for i := 9; i >= 0; i-- {
					jid := i
					if i == 7 {
						jid = 4
					} else if i == 4 {
						jid = 7
					}
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
					So(job.EnvC, ShouldNotBeNil)
					So(job.State, ShouldEqual, "reserved")
				}

				Convey("Reserving when all have been reserved returns nil", func() {
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					Convey("Adding one while waiting on a Reserve will return the new job", func() {
						worked := make(chan bool)
						go func() {
							job, err := jq.Reserve(100 * time.Millisecond)
							if err != nil {
								worked <- false
								return
							}
							if job == nil {
								worked <- false
								return
							}
							if job.Cmd == "new" {
								worked <- true
								return
							}
							worked <- false
						}()

						ok := make(chan bool)
						go func() {
							ticker := time.NewTicker(15 * time.Millisecond)
							ticks := 0
							for {
								select {
								case <-ticker.C:
									ticks++
									if ticks == 2 {
										jobs = append(jobs, NewJob("new", "/fake/cwd", "add_group", 10, 20*time.Hour, 1, uint8(0), uint8(0), "manually_added"))
										gojq, _ := Connect(addr, "test_queue", clientConnectTime)
										gojq.Add(jobs)
									}
									continue
								case w := <-worked:
									ticker.Stop()
									if w && ticks <= 6 {
										ok <- true
									}
									ok <- false
									return
								}
							}
						}()

						<-time.After(55 * time.Millisecond)
						So(<-ok, ShouldBeTrue)
					})
				})
			})

			Convey("You can subsequently add more jobs", func() {
				for i := 10; i < 20; i++ {
					jobs = append(jobs, NewJob(fmt.Sprintf("test cmd %d", i), "/fake/cwd", "new_group", 2048, 1*time.Hour, 2, uint8(0), uint8(0), "manually_added"))
				}
				inserts, already, err := jq.Add(jobs)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 10)
				So(already, ShouldEqual, 10)

				Convey("You can reserve jobs for a particular scheduler group", func() {
					for i := 10; i < 20; i++ {
						job, err := jq.ReserveScheduled(10*time.Millisecond, "2148:60:2")
						So(err, ShouldBeNil)
						So(job, ShouldNotBeNil)
						So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
					}
					job, err := jq.ReserveScheduled(10*time.Millisecond, "2148:60:2")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					for i := 9; i >= 0; i-- {
						jid := i
						if i == 7 {
							jid = 4
						} else if i == 4 {
							jid = 7
						}
						job, err := jq.ReserveScheduled(10*time.Millisecond, "1124:240:1")
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
					}
					job, err = jq.ReserveScheduled(10*time.Millisecond, "1124:240:1")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)
				})
			})

			Convey("You can stop the server by sending it a SIGTERM or SIGINT", func() {
				jq.Disconnect()

				syscall.Kill(os.Getpid(), syscall.SIGTERM)
				<-time.After(50 * time.Millisecond)
				_, err := Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldNotBeNil)
				jqerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrNoServer)

				server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
				So(err, ShouldBeNil)

				jq, err = Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldBeNil)
				jq.Disconnect()

				syscall.Kill(os.Getpid(), syscall.SIGINT)
				<-time.After(50 * time.Millisecond)
				_, err = Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldNotBeNil)
				jqerr, ok = err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrNoServer)
			})

			Convey("You get a nice error if you send the server junk", func() {
				_, err := jq.request(&clientRequest{Method: "junk", Queue: "test_queue"})
				So(err, ShouldNotBeNil)
				jqerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrUnknownCommand)
			})
		})

		Reset(func() {
			server.Stop()
		})
	})

	if server != nil {
		server.Stop()
	}

	// start these tests anew because I don't want to mess with the timings in
	// the above tests
	Convey("Once a new jobqueue server is up", t, func() {
		ServerItemTTR = 100 * time.Millisecond
		ClientTouchInterval = 50 * time.Millisecond
		server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
		So(err, ShouldBeNil)

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)
			jq2, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			var jobs []*Job
			jobs = append(jobs, NewJob("sleep 0.1 && true", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "manually_added"))
			jobs = append(jobs, NewJob("sleep 0.1 && false", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "manually_added"))
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			Convey("You can't execute a job without reserving it", func() {
				err := jq.Execute(jobs[0], config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				jqerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrMustReserve)
			})

			Convey("Once reserved you can execute jobs, and other clients see the correct state on gets", func() {
				// job that succeeds, no std out
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "sleep 0.1 && true")
				So(job.State, ShouldEqual, "reserved")
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 3)

				job2, err := jq2.GetByCmd("sleep 0.1 && true", "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, "sleep 0.1 && true")
				So(job2.State, ShouldEqual, "reserved")

				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, "complete")
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)
				So(job.Peakmem, ShouldBeGreaterThan, 0)
				So(job.Pid, ShouldBeGreaterThan, 0)
				host, _ := os.Hostname()
				So(job.Host, ShouldEqual, host)
				So(job.Walltime, ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, 0*time.Millisecond)
				So(job.Attempts, ShouldEqual, 1)
				So(job.UntilBuried, ShouldEqual, 3)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "")
				stderr, err := job.StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")

				job2, err = jq2.GetByCmd("sleep 0.1 && true", "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.State, ShouldEqual, "complete")
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 0)
				So(job2.Peakmem, ShouldEqual, job.Peakmem)
				So(job2.Pid, ShouldEqual, job.Pid)
				So(job2.Host, ShouldEqual, host)
				So(job2.Walltime, ShouldBeLessThanOrEqualTo, job.Walltime)
				So(job2.Walltime, ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job2.CPUtime, ShouldEqual, job.CPUtime)
				So(job2.Attempts, ShouldEqual, 1)

				// job that fails, no std out
				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
				So(job.State, ShouldEqual, "reserved")
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 3)

				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "command [sleep 0.1 && false] exited with code 1, which may be a temporary issue, so it will be tried again") // *** fails randomly with a receive time out error instead of the correct error - why?!
				So(job.State, ShouldEqual, "delayed")
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 1)
				So(job.Peakmem, ShouldBeGreaterThan, 0)
				So(job.Pid, ShouldBeGreaterThan, 0)
				So(job.Host, ShouldEqual, host)
				So(job.Walltime, ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, 0*time.Millisecond)
				So(job.Attempts, ShouldEqual, 1)
				So(job.UntilBuried, ShouldEqual, 2)
				stdout, err = job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "")
				stderr, err = job.StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")

				job2, err = jq2.GetByCmd("sleep 0.1 && false", "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.State, ShouldEqual, "delayed")
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 1)
				So(job2.Peakmem, ShouldEqual, job.Peakmem)
				So(job2.Pid, ShouldEqual, job.Pid)
				So(job2.Host, ShouldEqual, host)
				So(job2.Walltime, ShouldBeLessThanOrEqualTo, job.Walltime)
				So(job2.Walltime, ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job2.CPUtime, ShouldEqual, job.CPUtime)
				So(job2.Attempts, ShouldEqual, 1)

				Convey("Both current and archived jobs can be retrieved with GetByRepGroup", func() {
					jobs, err := jq.GetByRepGroup("manually_added", 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(jobs), ShouldEqual, 2)

					Convey("But only current jobs are retrieved with GetIncomplete", func() {
						jobs, err := jq.GetIncomplete(0, "", false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 1)
						So(jobs[0].Cmd, ShouldEqual, "sleep 0.1 && false")
						//*** should probably have a better test, where there are incomplete jobs in each of the sub queues
					})
				})

				Convey("A temp failed job is reservable after a delay", func() {
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)
					job2, err = jq2.GetByCmd("sleep 0.1 && false", "/tmp", false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "delayed")

					<-time.After(60 * time.Millisecond)
					job, err = jq.Reserve(5 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
					So(job.State, ShouldEqual, "reserved")
					So(job.Attempts, ShouldEqual, 1)
					So(job.UntilBuried, ShouldEqual, 2)
					job2, err = jq2.GetByCmd("sleep 0.1 && false", "/tmp", false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "reserved")

					Convey("After 3 failures it gets buried", func() {
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldNotBeNil)
						So(job.State, ShouldEqual, "delayed")
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 1)
						So(job.Attempts, ShouldEqual, 2)
						So(job.UntilBuried, ShouldEqual, 1)

						<-time.After(110 * time.Millisecond)
						job, err = jq.Reserve(5 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
						So(job.State, ShouldEqual, "reserved")
						So(job.Attempts, ShouldEqual, 2)
						So(job.UntilBuried, ShouldEqual, 1)

						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldNotBeNil)
						So(job.State, ShouldEqual, "buried")
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 1)
						So(job.Attempts, ShouldEqual, 3)
						So(job.UntilBuried, ShouldEqual, 0)

						<-time.After(110 * time.Millisecond)
						job, err = jq.Reserve(5 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job, ShouldBeNil)

						Convey("Once buried it can be kicked back to ready state and be reserved again", func() {
							job2, err = jq2.GetByCmd("sleep 0.1 && false", "/tmp", false, false)
							So(err, ShouldBeNil)
							So(job2, ShouldNotBeNil)
							So(job2.State, ShouldEqual, "buried")

							kicked, err := jq.Kick([][2]string{{"sleep 0.1 && false", "/tmp"}})
							So(err, ShouldBeNil)
							So(kicked, ShouldEqual, 1)

							job, err = jq.Reserve(5 * time.Millisecond)
							So(err, ShouldBeNil)
							So(job, ShouldNotBeNil)
							So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
							So(job.State, ShouldEqual, "reserved")
							So(job.Attempts, ShouldEqual, 3)
							So(job.UntilBuried, ShouldEqual, 3)

							job2, err = jq2.GetByCmd("sleep 0.1 && false", "/tmp", false, false)
							So(err, ShouldBeNil)
							So(job2, ShouldNotBeNil)
							So(job2.State, ShouldEqual, "reserved")
							So(job2.Attempts, ShouldEqual, 3)
							So(job2.UntilBuried, ShouldEqual, 3)

							Convey("If you do nothing with a reserved job, it auto reverts back to ready", func() {
								<-time.After(110 * time.Millisecond)
								job2, err = jq2.GetByCmd("sleep 0.1 && false", "/tmp", false, false)
								So(err, ShouldBeNil)
								So(job2, ShouldNotBeNil)
								So(job2.State, ShouldEqual, "ready")
								So(job2.Attempts, ShouldEqual, 3)
								So(job2.UntilBuried, ShouldEqual, 3)
							})
						})
					})
				})
			})

			Convey("Jobs can be deleted, but only once buried, and you can only bury once reserved", func() {
				for _, added := range jobs {
					job, err := jq.GetByCmd(added.Cmd, added.Cwd, false, false)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.State, ShouldEqual, "ready")

					deleted, err := jq.Delete([][2]string{{added.Cmd, added.Cwd}})
					So(err, ShouldBeNil)
					So(deleted, ShouldEqual, 0)

					err = jq.Bury(job, "test bury")
					So(err, ShouldNotBeNil)
					jqerr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(jqerr.Err, ShouldEqual, ErrBadJob)

					job, err = jq.Reserve(5 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, added.Cmd)
					So(job.State, ShouldEqual, "reserved")

					deleted, err = jq.Delete([][2]string{{added.Cmd, added.Cwd}})
					So(err, ShouldBeNil)
					So(deleted, ShouldEqual, 0)

					err = jq.Bury(job, "test bury")
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, "buried")
					So(job.FailReason, ShouldEqual, "test bury")

					job2, err := jq2.GetByCmd(added.Cmd, added.Cwd, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "buried")
					So(job2.FailReason, ShouldEqual, "test bury")

					deleted, err = jq.Delete([][2]string{{added.Cmd, added.Cwd}})
					So(err, ShouldBeNil)
					So(deleted, ShouldEqual, 1)

					job, err = jq.GetByCmd(added.Cmd, added.Cwd, false, false)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)
				}
				job, err := jq.Reserve(5 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)

				Convey("Cmds with pipes in them are handled correctly", func() {
					jobs = nil
					jobs = append(jobs, NewJob("sleep 0.1 && true | true", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_pass"))
					jobs = append(jobs, NewJob("sleep 0.1 && true | false | true", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_fail"))
					inserts, _, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					// pipe job that succeeds
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && true | true")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, "complete")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					So(job.FailReason, ShouldEqual, "")

					// pipe job that fails in the middle
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && true | false | true")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command [sleep 0.1 && true | false | true] exited with code 1, which may be a temporary issue, so it will be tried again") //*** can fail with a receive time out; why?!
					So(job.State, ShouldEqual, "delayed")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)
				})

				Convey("Invalid commands are immediately buried", func() {
					jobs = nil
					jobs = append(jobs, NewJob("awesjnalakjf --foo", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_fail"))
					inserts, _, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					// job that fails because of non-existent exe
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "awesjnalakjf --foo")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command [awesjnalakjf --foo] exited with code 127 (command not found), which seems permanent, so it has been buried")
					So(job.State, ShouldEqual, "buried")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 127)
					So(job.FailReason, ShouldEqual, FailReasonCFound)

					job2, err := jq2.GetByCmd("awesjnalakjf --foo", "/tmp", false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "buried")
					So(job2.FailReason, ShouldEqual, FailReasonCFound)

					//*** how to test the other bury cases of invalid exit code
					// and permission problems on the exe?
				})

				Convey("If a job uses too much memory it is killed and we recommend more next time", func() {
					jobs = nil
					cmd := "perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'"
					jobs = append(jobs, NewJob(cmd, "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "run_out_of_mem"))
					RecMBRound = 1
					inserts, already, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					jqerr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(jqerr.Err, ShouldEqual, FailReasonMem)
					So(job.State, ShouldEqual, "delayed")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, -1)
					So(job.FailReason, ShouldEqual, FailReasonMem)
					So(job.Memory, ShouldEqual, 1200)
					jq.Delete([][2]string{{cmd, "/tmp"}})
				})

				RecMBRound = 100 // revert back to normal

				Convey("The stdout/err of jobs is only kept for failed jobs", func() {
					jobs = nil
					jobs = append(jobs, NewJob("perl -e 'print qq[print\\n]; warn qq[warn\\n]'", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_pass"))
					jobs = append(jobs, NewJob("perl -e 'print qq[print\\n]; die qq[die\\n]'", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_fail"))
					inserts, _, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					// job that outputs to stdout and stderr but succeeds
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -e 'print qq[print\\n]; warn qq[warn\\n]'")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, "complete")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "print")
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "warn")

					job2, err := jq2.GetByCmd("perl -e 'print qq[print\\n]; warn qq[warn\\n]'", "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "complete")
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "")
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "")

					// job that outputs to stdout and stderr and fails
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -e 'print qq[print\\n]; die qq[die\\n]'")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					<-time.After(50 * time.Millisecond)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, "delayed")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					So(job.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "print")
					stderr, err = job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "die")

					job2, err = jq2.GetByCmd("perl -e 'print qq[print\\n]; die qq[die\\n]'", "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "delayed")
					So(job2.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "print")
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "die")
				})

				Convey("The stdout/err of jobs is limited in size", func() {
					jobs = nil
					jobs = append(jobs, NewJob("perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_fail"))
					inserts, _, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					// job that outputs tons of stdout and stderr and fails
					expectedout := ""
					expectederr := ""
					for i := 1; i <= 60; i++ {
						if i > 21 && i < 46 {
							continue
						}
						if i == 21 {
							expectedout += "21212121212121212121212121\n... omitting 6358 bytes ...\n45454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545p\n"
							expectederr += "21212121212121212121212121\n... omitting 6377 bytes ...\n5454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545454545w\n"
						} else {
							s := strconv.Itoa(i)
							for j := 1; j <= 130; j++ {
								expectedout += s
								expectederr += s
							}
							expectedout += "p\n"
							expectederr += "w\n"
						}
					}
					expectederr += "Died at -e line 1."
					expectedout = strings.TrimSpace(expectedout)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					<-time.After(50 * time.Millisecond)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, "delayed")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, expectederr)

					job2, err := jq2.GetByCmd("perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'", "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "delayed")
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, expectederr)

					Convey("If you don't ask for stdout, you don't get it", func() {
						job2, err = jq2.GetByCmd("perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'", "/tmp", false, false)
						So(err, ShouldBeNil)
						So(job2, ShouldNotBeNil)
						So(job2.State, ShouldEqual, "delayed")
						stdout, err = job2.StdOut()
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "")
						stderr, err = job2.StdErr()
						So(err, ShouldBeNil)
						So(stderr, ShouldEqual, "")
					})
				})

				Convey("Jobs that take longer than the ttr can execute successfully, unless the clienttouchinterval is > ttr", func() {
					jobs = nil
					cmd := "perl -e 'for (1..3) { sleep(1) }'"
					jobs = append(jobs, NewJob(cmd, "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_pass"))
					inserts, _, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					<-time.After(100 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, "complete")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					job2, err := jq2.GetByCmd(cmd, "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "complete")

					// same again, but we'll alter the clienttouchinterval to be invalid
					ClientTouchInterval = 150 * time.Millisecond
					inserts, _, err = jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.RunnerExecShell)
					<-time.After(100 * time.Millisecond)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command ["+cmd+"] was running fine, but will need to be rerun due to a jobqueue server error")
					// because Execute() just returns in this situation, job.* won't be updated,

					job2, err = jq2.GetByCmd(cmd, "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					// in this situation, the job got auto-released to ready
					// with no indication of a problem stored on the server
					So(job2.State, ShouldEqual, "ready")
					So(job2.Exited, ShouldBeFalse)
				})
			})
		})

		Convey("After connecting and adding some jobs under one RepGroup", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			var jobs []*Job
			for i := 0; i < 3; i++ {
				jobs = append(jobs, NewJob(fmt.Sprintf("echo rgduptest %d", i), "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "rp1"))
			}
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can reserve and execute those", func() {
				for i := 0; i < 3; i++ {
					job, err := jq.Reserve(50 * time.Millisecond)
					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
				}

				Convey("Then you can add dups and a new one under a new RepGroup and reserve/execute all of them", func() {
					jobs = nil
					for i := 0; i < 4; i++ {
						jobs = append(jobs, NewJob(fmt.Sprintf("echo rgduptest %d", i), "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "rp2"))
					}
					inserts, already, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 4)
					So(already, ShouldEqual, 0)

					for i := 0; i < 4; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					Convey("The jobs can be retrieved by either RepGroup and will have the expected RepGroup", func() {
						jobs, err := jq.GetByRepGroup("rp1", 0, "complete", false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 3)
						So(jobs[0].RepGroup, ShouldEqual, "rp1")

						jobs, err = jq.GetByRepGroup("rp2", 0, "complete", false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 4)
						So(jobs[0].RepGroup, ShouldEqual, "rp2")
					})
				})
			})

			Convey("You can add dups and a new one under a new RepGroup", func() {
				jobs = nil
				for i := 0; i < 4; i++ {
					jobs = append(jobs, NewJob(fmt.Sprintf("echo rgduptest %d", i), "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "rp2"))
				}
				inserts, already, err := jq.Add(jobs)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 3)

				Convey("You can then reserve and execute the only 4 jobs", func() {
					for i := 0; i < 4; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}
					job, err := jq.Reserve(10 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					Convey("The jobs can be retrieved by either RepGroup and will have the expected RepGroup", func() {
						jobs, err := jq.GetByRepGroup("rp1", 0, "complete", false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 3)
						So(jobs[0].RepGroup, ShouldEqual, "rp1")

						jobs, err = jq.GetByRepGroup("rp2", 0, "complete", false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 4)
						So(jobs[0].RepGroup, ShouldEqual, "rp2")
					})
				})
			})
		})

		Reset(func() {
			server.Stop()
		})
	})

	if server != nil {
		server.Stop()
	}

	// start these tests anew because I need to disable dev-mode wiping of the
	// db to test some behaviours
	Convey("Once a new jobqueue server is up", t, func() {
		ServerItemTTR = 2 * time.Second
		server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
		So(err, ShouldBeNil)

		Convey("You can connect, and add 2 jobs", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			var jobs []*Job
			jobs = append(jobs, NewJob("echo 1", "/tmp", "fake_group", 10, 1*time.Second, 1, uint8(0), uint8(0), "manually_added"))
			jobs = append(jobs, NewJob("echo 2", "/tmp", "fake_group", 10, 1*time.Second, 1, uint8(0), uint8(0), "manually_added"))
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			Convey("You can reserve & execute just 1 of the jobs, stop the server, restart it, and then reserve & execute the other", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "echo 1")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, "complete")
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)

				server.Stop()
				wipeDevDBOnInit = false
				server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
				wipeDevDBOnInit = true
				So(err, ShouldBeNil)
				jq, err = Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, "echo 2")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, "complete")
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)
			})
		})

		Convey("You can connect and add a non-instant job", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			var jobs []*Job
			job1Cmd := "sleep 1 && echo noninstant"
			jobs = append(jobs, NewJob(job1Cmd, "/tmp", "fake_group", 10, 1*time.Second, 1, uint8(0), uint8(0), "nij"))
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("You can reserve & execute the job, drain the server, add a new job while draining, restart it, and then reserve & execute the new one", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, job1Cmd)
				go jq.Execute(job, config.RunnerExecShell)
				So(job.Exited, ShouldBeFalse)

				running, etc, err := jq.DrainServer()
				So(err, ShouldBeNil)
				So(running, ShouldEqual, 1)
				So(etc.Minutes(), ShouldBeLessThanOrEqualTo, 30)

				jobs = append(jobs, NewJob("echo added", "/tmp", "fake_group", 10, 1*time.Second, 1, uint8(0), uint8(0), "nij"))
				inserts, already, err = jq.Add(jobs)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 1)

				job2, err := jq.Reserve(10 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldBeNil)

				<-time.After(2 * time.Second)

				up := jq.Ping(10 * time.Millisecond)
				So(up, ShouldBeFalse)

				wipeDevDBOnInit = false
				server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
				wipeDevDBOnInit = true
				So(err, ShouldBeNil)
				jq, err = Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.GetByCmd(job1Cmd, "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)

				job2, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, "echo added")
				err = jq.Execute(job2, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job2.State, ShouldEqual, "complete")
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 0)
			})

			Convey("You can reserve & execute the job, shut down the server and then can't add new jobs and the started job did not complete", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, job1Cmd)
				go jq.Execute(job, config.RunnerExecShell)
				So(job.Exited, ShouldBeFalse)

				ok := jq.ShutdownServer()
				So(ok, ShouldBeTrue)

				jobs = append(jobs, NewJob("echo added", "/tmp", "fake_group", 10, 1*time.Second, 1, uint8(0), uint8(0), "nij"))
				inserts, already, err = jq.Add(jobs)
				So(err, ShouldNotBeNil)

				<-time.After(2 * time.Second)

				up := jq.Ping(10 * time.Millisecond)
				So(up, ShouldBeFalse)

				jq.Disconnect() // user must always Disconnect before connecting again!

				wipeDevDBOnInit = false
				server, _, err = Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
				wipeDevDBOnInit = true
				So(err, ShouldBeNil)
				jq, err = Connect(addr, "test_queue", clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.GetByCmd(job1Cmd, "/tmp", false, false)
				So(err, ShouldBeNil)
				So(job.Exited, ShouldBeFalse)
			})
		})

		Reset(func() {
			server.Stop()
		})
	})

	if server != nil {
		server.Stop()
	}

	// start these tests anew because these tests have the server spawn runners
	Convey("Once a new jobqueue server is up", t, func() {
		ServerItemTTR = 100 * time.Millisecond
		ClientTouchInterval = 50 * time.Millisecond
		runnertmpdir, err := ioutil.TempDir("", "wr_jobqueue_test_runner_dir_")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(runnertmpdir)
		server, _, err = Serve(port, webport, "local", config.RunnerExecShell, "go test -tags netgo -run TestJobqueue ../jobqueue -args --runnermode --queue %s --schedgrp '%s' --rdeployment %s --rserver '%s' --rtimeout %d --maxmins %d --tmpdir "+runnertmpdir, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment) // +" > /dev/null 2>&1"
		So(err, ShouldBeNil)
		maxCPU := runtime.NumCPU()
		runtime.GOMAXPROCS(maxCPU)

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			tmpdir, err := ioutil.TempDir("", "wr_jobqueue_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			var jobs []*Job
			count := maxCPU * 2
			for i := 0; i < count; i++ {
				jobs = append(jobs, NewJob(fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), tmpdir, "perl", 1, 1*time.Second, 1, uint8(0), uint8(0), "manually_added"))
			}
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// we need some time for 'go test' to live-compile and run
				// ourselves in runnermode *** not sure if it's legit for this
				// to take over 2mins though!
				done := make(chan bool, 1)
				go func() {
					limit := time.After(240 * time.Second)
					ticker := time.NewTicker(500 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							if !server.HasRunners() {
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
				So(<-done, ShouldBeTrue) // we shouldn't have hit our time limit

				files, err := ioutil.ReadDir(tmpdir)
				if err != nil {
					log.Fatal(err)
				}
				ran := 0
				for range files {
					ran++
				}
				So(ran, ShouldEqual, count)

				// we shouldn't have executed any unnecessary runners, and those
				// we did run should have exited without error, even if there
				// were no more jobs left
				files, err = ioutil.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}
				ranClean := 0
				for range files {
					ranClean++
				}
				So(ranClean, ShouldEqual, maxCPU)
			})
		})

		Reset(func() {
			server.Stop()
		})
	})

	if server != nil {
		server.Stop()
	}
}

func TestJobqueueSpeed(t *testing.T) {
	if runnermode {
		return
	}

	// some manual speed tests (don't like the way the benchmarking feature
	// works)
	if false {
		config := internal.ConfigLoad("development", true)
		port := config.ManagerPort
		webport := config.ManagerWeb
		addr := "localhost:" + port
		rc := ""
		runtime.GOMAXPROCS(runtime.NumCPU())
		n := 50000

		server, _, err := Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
		if err != nil {
			log.Fatal(err)
		}

		clientConnectTime := 10 * time.Second
		jq, err := Connect(addr, "wr.des", clientConnectTime)
		if err != nil {
			log.Fatal(err)
		}

		before := time.Now()
		var jobs []*Job
		for i := 0; i < n; i++ {
			jobs = append(jobs, NewJob(fmt.Sprintf("test cmd %d", i), "/fake/cwd", "fake_group", 1024, 4*time.Hour, 1, uint8(0), uint8(0), "manually_added"))
		}
		inserts, already, err := jq.Add(jobs)
		if err != nil {
			log.Fatal(err)
		}
		e := time.Since(before)
		per := int64(e.Nanoseconds() / int64(n))
		log.Printf("Added %d jobqueue jobs (%d inserts, %d dups) in %s == %d per\n", n, inserts, already, e, per)

		jq.Disconnect()

		reserves := make(chan int, n)
		beginat := time.Now().Add(1 * time.Second)
		o := runtime.NumCPU() // from here up to 1650 the time taken is around 6-7s, but beyond 1675 it suddenly drops to 14s, and seems to just hang forever at much higher values
		m := int(math.Ceil(float64(n) / float64(o)))
		for i := 1; i <= o; i++ {
			go func(i int) {
				start := time.After(beginat.Sub(time.Now()))
				gjq, err := Connect(addr, "wr.des", clientConnectTime)
				if err != nil {
					log.Fatal(err)
				}
				defer gjq.Disconnect()
				for {
					select {
					case <-start:
						reserved := 0
						na := 0
						for j := 0; j < m; j++ {
							job, err := gjq.Reserve(5 * time.Second)
							if err != nil || job == nil {
								for k := j; k < m; k++ {
									na++
									reserves <- -i
								}
								break
							} else {
								reserved++
								reserves <- i
							}
						}
						return
					}
				}
			}(i)
		}

		r := 0
		na := 0
		for i := 0; i < n; i++ {
			res := <-reserves
			if res >= 0 {
				r++
			} else {
				na++
			}
		}

		e = time.Since(beginat)
		per = int64(e.Nanoseconds() / int64(n))
		log.Printf("Reserved %d jobqueue jobs (%d not available) in %s == %d per\n", r, na, e, per)

		// q := queue.New("myqueue")
		// before = time.Now()
		// for i := 0; i < n; i++ {
		// 	_, err := q.Add(fmt.Sprintf("test job %d", i), "data", 0, 0*time.Second, 30*time.Second)
		// 	if err != nil {
		// 		log.Fatal(err)
		// 	}
		// }
		// e = time.Since(before)
		// per = int64(e.Nanoseconds() / int64(n))
		// log.Printf("Added %d items to queue in %s == %d per\n", n, e, per)

		server.Stop()
	}

	/* test speed of bolt db when there are lots of jobs already stored
		if true {
			config := internal.ConfigLoad("development", true)
			port := config.ManagerPort
			webport := config.ManagerWeb
			addr := "localhost:" + port
			rc := ""
			n := 10000000 // num jobs to start with
			b := 10000    // jobs per identifier

			server, _, err := Serve(port, webport, config.ManagerScheduler, config.RunnerExecShell, rc, config.ManagerDbFile, config.ManagerDbBkFile, config.Deployment)
			if err != nil {
				log.Fatal(err)
			}

			jq, err := Connect(addr, "cmds", 60*time.Second)
			if err != nil {
				log.Fatal(err)
			}

			// get timings when the bolt db is empty
			total := 0
			batchNum := 1
			timeDealingWithBatch(addr, jq, batchNum, b)
			batchNum++
			total += b

			// add n jobs in b batches to the completed bolt db bucket to simulate
			// a well used database
			before := time.Now()
			q := server.getOrCreateQueue("cmds")
			for total < n {
				var jobs []*Job
				for i := 0; i < b; i++ {
					jobs = append(jobs, NewJob(fmt.Sprintf("test cmd %d", i+((batchNum-1)*b)), "/fake/cwd", "reqgroup", 1024, 4*time.Hour, 1, uint8(0), uint8(0), fmt.Sprintf("batch_%d", batchNum)))
				}
				_, _, err := jq.Add(jobs)
				if err != nil {
					log.Fatal(err)
				}
				fmt.Printf("\nadded batch %d", batchNum)

				// it's too slow to reserve and archive things properly in this
				// test; this is not a real-world performance concern though, since
				// normally you wouldn't archive so many jobs in a row in a single
	            // process...
				// for {
				// 	job, _ := jq.Reserve(1 * time.Millisecond)
				// 	if job == nil {
				// 		break
				// 	}
				// 	jq.Started(job, 123, "host")
				// 	jq.Ended(job, 0, 5, 1*time.Second, []byte{}, []byte{})
				// 	err = jq.Archive(job)
				// 	if err != nil {
				// 		log.Fatal(err)
				// 	}
				// 	fmt.Print(".")
				// }

				// ... Instead we bypass the client interface and directly add to
				// bolt db
				err = server.db.bolt.Batch(func(tx *bolt.Tx) error {
					bl := tx.Bucket(bucketJobsLive)
					b := tx.Bucket(bucketJobsComplete)

					var puterr error
					for _, job := range jobs {
						key := jobKey(job)
						var encoded []byte
						enc := codec.NewEncoderBytes(&encoded, server.db.ch)
						enc.Encode(job)

						bl.Delete([]byte(key))
						q.Remove(key)

						puterr = b.Put([]byte(key), encoded)
						if puterr != nil {
							break
						}
					}
					return puterr
				})
				if err != nil {
					log.Fatal(err)
				}

				batchNum++
				total += b
			}
			e := time.Since(before)
			log.Printf("Archived %d jobqueue jobs in %d sized groups in %s\n", n, b, e)

			// now re-time how long it takes to deal with a single new batch
			timeDealingWithBatch(addr, jq, batchNum, b)

			server.Stop()
		}
	*/
}

/* this func is used by the commented out test above
func timeDealingWithBatch(addr string, jq *Client, batchNum int, b int) {
	before := time.Now()
	var jobs []*Job
	batchName := fmt.Sprintf("batch_%d", batchNum)
	for i := 0; i < b; i++ {
		jobs = append(jobs, NewJob(fmt.Sprintf("test cmd %d", i+((batchNum-1)*b)), "/fake/cwd", "reqgroup", 1024, 4*time.Hour, 1, uint8(0), uint8(0), batchName))
	}
	_, _, err := jq.Add(jobs)
	if err != nil {
		log.Fatal(err)
	}
	e := time.Since(before)
	log.Printf("\nAdded a new batch of %d jobs in %s\n", b, e)

	before = time.Now()
	runtime.GOMAXPROCS(runtime.NumCPU())
	var wg sync.WaitGroup
	wg.Add(runtime.NumCPU())
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			defer wg.Done()
			gojq, _ := Connect(addr, "cmds", 10*time.Second)
			for {
				job, _ := gojq.Reserve(1 * time.Millisecond)
				if job == nil {
					break
				}
				gojq.Started(job, 123, "host")
				gojq.Ended(job, 0, 5, 1*time.Second, []byte{}, []byte{})
				err = gojq.Archive(job)
				if err != nil {
					log.Fatal(err)
				}
			}
		}()
	}
	wg.Wait()
	e = time.Since(before)
	log.Printf("Reserved and Archived that batch of jobs in %s\n", e)

	before = time.Now()
	jobs, err = jq.GetByRepGroup(batchName, 1, "complete", false, false) // without a limit this takes longer than 60s, so would time out
	if err != nil {
		log.Fatal(err)
	}
	e = time.Since(before)
	log.Printf("Was able to get all %d jobs in that batch in %s\n", 1+jobs[0].Similar, e)
}
*/

func runner() {
	if queuename == "" {
		log.Fatal("queue missing")
	}
	if schedgrp == "" {
		log.Fatal("schedgrp missing")
	}

	config := internal.ConfigLoad(rdeployment, true)
	addr := rserver

	timeout := 6 * time.Second
	rtimeoutd := time.Duration(rtimeout) * time.Second
	// (we don't bother doing anything with maxmins in this test, but in a real
	//  runner client it would be used to end the below for loop before hitting
	//  this limit)

	jq, err := Connect(addr, queuename, timeout)

	//*** it only takes some milliseconds to go from entering the script to get
	// here, and running in runnermode manually takes less than 2 seconds, so
	// I've no idea why during the test there's a 15s+ overhead!

	if err != nil {
		log.Fatalf("connect err: %s\n", err)
	}
	defer jq.Disconnect()

	for {
		job, err := jq.ReserveScheduled(rtimeoutd, schedgrp)
		if err != nil {
			log.Fatalf("reserve err: %s\n", err)
		}
		if job == nil {
			break
		}

		// actually run the cmd
		err = jq.Execute(job, config.RunnerExecShell)
		if err != nil {
			if jqerr, ok := err.(Error); ok && jqerr.Err == FailReasonSignal {
				break
			} else {
				log.Fatalf("execute err: %s\n", err)
			}
		} else {
			jq.Archive(job)
		}
	}

	// if everything ran cleanly, create a tmpfile in our tmp dir
	tmpfile, _ := ioutil.TempFile(runnermodetmpdir, "ok")
	tmpfile.Close()
}
