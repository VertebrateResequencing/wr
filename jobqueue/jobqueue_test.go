// Copyright © 2016-2018 Genome Research Limited
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
	"io"
	"io/ioutil"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/muxfys"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/inconshreveable/log15"
	"github.com/sevlyar/go-daemon"
	"github.com/shirou/gopsutil/process"
	. "github.com/smartystreets/goconvey/convey"
)

var runnermode bool
var runnerfail bool
var schedgrp string
var runnermodetmpdir string
var rdeployment string
var rserver string
var rdomain string
var rtimeout int
var maxmins int
var envVars = os.Environ()

var testLogger = log15.New()

func init() {
	testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))

	flag.BoolVar(&runnermode, "runnermode", false, "enable to disable tests and act as a 'runner' client")
	flag.BoolVar(&runnerfail, "runnerfail", false, "make the runner client fail")
	flag.StringVar(&schedgrp, "schedgrp", "", "schedgrp for runnermode")
	flag.StringVar(&rdeployment, "rdeployment", "", "deployment for runnermode")
	flag.StringVar(&rserver, "rserver", "", "server for runnermode")
	flag.StringVar(&rdomain, "rdomain", "", "domain for runnermode")
	flag.IntVar(&rtimeout, "rtimeout", 1, "reserve timeout for runnermode")
	flag.IntVar(&maxmins, "maxmins", 0, "maximum mins allowed for  runnermode")
	flag.StringVar(&runnermodetmpdir, "tmpdir", "", "tmp dir for runnermode")
	ServerLogClientErrors = false
}

func TestJobqueueUtils(t *testing.T) {
	if runnermode {
		return
	}

	Convey("CurrentIP() works", t, func() {
		ip, err := internal.CurrentIP("")
		So(err, ShouldBeNil)
		So(ip, ShouldNotBeBlank)
		ip, err = internal.CurrentIP("9.9.9.9/24")
		So(err, ShouldBeNil)
		So(ip, ShouldBeBlank)
		ip, err = internal.CurrentIP(ip + "/16")
		So(err, ShouldBeNil)
		So(ip, ShouldEqual, ip)
	})

	Convey("generateToken() and tokenMatches() work", t, func() {
		token, err := generateToken()
		So(err, ShouldBeNil)
		So(len(token), ShouldEqual, tokenLength)
		token2, err := generateToken()
		So(err, ShouldBeNil)
		So(len(token2), ShouldEqual, tokenLength)
		So(token, ShouldNotEqual, token2)
		So(tokenMatches(token, token2), ShouldBeFalse)
		So(tokenMatches(token, token), ShouldBeTrue)
	})
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

	defer os.RemoveAll(filepath.Join(os.TempDir(), AppName+"_cwd"))

	// load our config to know where our development manager port is supposed to
	// be; we'll use that to test jobqueue
	config := internal.ConfigLoad("development", true, testLogger)
	managerDBBkFile := config.ManagerDbFile + "_bk" // not config.ManagerDbBkFile in case it is an s3 url
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDbFile,
		DBFileBackup:    managerDBBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
		Logger:          testLogger,
	}
	addr := "localhost:" + config.ManagerPort

	setDomainIP(config.ManagerCertDomain)

	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelay = 100 * time.Millisecond
	clientConnectTime := 1500 * time.Millisecond

	standardReqs := &jqs.Requirements{RAM: 10, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}

	// these tests need the server running in it's own pid so we can test signal
	// handling in the client; to get the server in its own pid we need to
	// "fork", and that means these must be the first tests to run or else we
	// won't know in our parent process when our desired server is ready
	Convey("Once a jobqueue server is up as a daemon", t, func() {
		ServerItemTTR = 200 * time.Millisecond
		ClientTouchInterval = 50 * time.Millisecond

		errr := os.Remove(config.ManagerTokenFile)
		So(errr == nil || os.IsNotExist(errr), ShouldBeTrue)

		context := &daemon.Context{
			PidFileName: config.ManagerPidFile,
			PidFilePerm: 0644,
			WorkDir:     "/",
			Umask:       config.ManagerUmask,
		}
		child, errc := context.Reborn()
		if errc != nil {
			log.Fatalf("failed to daemonize for the initial test: %s (you probably need to `wr manager stop`)", errc)
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

			server, msg, _, err := Serve(serverConfig)
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
				server.Stop(true)
			}()

			log.Println("test daemon up, will block")

			// wait until we are killed
			err = server.Block()
			log.Printf("test daemon exiting due to %s\n", err)
			os.Exit(0)
		}
		// parent; wait a while for our child to bring up the server
		defer syscall.Kill(child.Pid, syscall.SIGTERM)

		mTimeout := 10 * time.Second
		internal.WaitForFile(config.ManagerTokenFile, mTimeout)
		token, err := ioutil.ReadFile(config.ManagerTokenFile)
		So(err, ShouldBeNil)
		So(token, ShouldNotBeNil)
		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, 10*time.Second)
		So(err, ShouldBeNil)
		defer jq.Disconnect()

		So(jq.ServerInfo.PID, ShouldEqual, child.Pid)

		Convey("You can set up a long-running job for execution", func() {
			cmd := "perl -e 'for (1..3) { sleep(1) }'"
			cmd2 := "perl -e 'for (2..4) { sleep(1) }'"
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 4 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "3secs_pass"})
			jobs = append(jobs, &Job{Cmd: cmd2, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "3secs_fail"})
			RecSecRound = 1
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job.Cmd, ShouldEqual, cmd)
			So(job.State, ShouldEqual, JobStateReserved)

			job2, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job2.Cmd, ShouldEqual, cmd2)
			So(job2.State, ShouldEqual, JobStateReserved)

			Convey("Signals are handled during execution, and we can see when jobs take too long", func() {
				go func() {
					<-time.After(2 * time.Second)
					syscall.Kill(os.Getpid(), syscall.SIGTERM)
				}()

				ClientReleaseDelay = 100 * time.Second
				j1worked := make(chan bool)
				go func() {
					err := jq.Execute(job, config.RunnerExecShell)
					if err != nil {
						if jqerr, ok := err.(Error); ok && jqerr.Err == FailReasonSignal && job.State == JobStateDelayed && job.Exited && job.Exitcode == -1 && job.FailReason == FailReasonSignal {
							j1worked <- true
						}
					}
					j1worked <- false
				}()

				j2worked := make(chan bool)
				go func() {
					err := jq.Execute(job2, config.RunnerExecShell)
					if err != nil {
						if jqerr, ok := err.(Error); ok && jqerr.Err == FailReasonTime && job2.State == JobStateDelayed && job2.Exited && job2.Exitcode == -1 && job2.FailReason == FailReasonTime {
							j2worked <- true
						}
					}
					j2worked <- false
				}()

				So(<-j1worked, ShouldBeTrue)
				So(<-j2worked, ShouldBeTrue)

				jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				defer jq2.Disconnect()
				job, err = jq2.GetByEssence(&JobEssence{Cmd: cmd}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, cmd)
				So(job.State, ShouldEqual, JobStateDelayed)
				So(job.FailReason, ShouldEqual, FailReasonSignal)

				job2, err = jq2.GetByEssence(&JobEssence{Cmd: cmd2}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, cmd2)
				So(job2.State, ShouldEqual, JobStateDelayed)
				So(job2.FailReason, ShouldEqual, FailReasonTime)
				So(job2.Requirements.Time.Seconds(), ShouldEqual, 3601)

				ClientReleaseDelay = 100 * time.Millisecond

				// all signals handled the same way, so no need for further
				// tests
			})

			RecSecRound = 1800 // revert back to normal
		})

		Reset(func() {
			syscall.Kill(child.Pid, syscall.SIGTERM)
		})
	})

	<-time.After(5 * time.Second) // wait for the "fork" to really not be listening on the ports any more

	ServerItemTTR = 1 * time.Second
	ClientTouchInterval = 500 * time.Millisecond

	var server *Server
	var token []byte
	var errs error
	Convey("Without the jobserver being up, clients can't connect and time out", t, func() {
		_, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldNotBeNil)
		jqerr, ok := err.(Error)
		So(ok, ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, ErrNoServer)
	})

	Convey("Once the jobqueue server is up", t, func() {
		server, _, token, errs = Serve(serverConfig)
		So(errs, ShouldBeNil)

		server.rc = `echo %s %s %s %s %d %d` // ReserveScheduled() only works if we have an rc

		Convey("You can connect to the server and add jobs to the queue", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			So(jq.ServerInfo.Port, ShouldEqual, serverConfig.Port)
			So(jq.ServerInfo.PID, ShouldBeGreaterThan, 0)
			So(jq.ServerInfo.Deployment, ShouldEqual, "development")

			var jobs []*Job
			for i := 0; i < 10; i++ {
				pri := i
				if i == 7 {
					pri = 4
				} else if i == 4 {
					pri = 7
				}
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 4 * time.Hour, Cores: 1}, Priority: uint8(pri), Retries: uint8(3), RepGroup: "manually_added"})
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 10)
			So(already, ShouldEqual, 0)

			Convey("You can't add the same jobs to the queue again", func() {
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 0)
				So(already, ShouldEqual, 10)
			})

			Convey("You can get back jobs you've just added", func() {
				job, err := jq.GetByEssence(&JobEssence{Cmd: "test cmd 3"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, "test cmd 3")
				So(job.State, ShouldEqual, JobStateReady)

				job, err = jq.GetByEssence(&JobEssence{Cmd: "test cmd x"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)

				var jes []*JobEssence
				for i := 0; i < 10; i++ {
					jes = append(jes, &JobEssence{Cmd: fmt.Sprintf("test cmd %d", i)})
				}
				jobs, err = jq.GetByEssences(jes)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)
				for i, job := range jobs {
					So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
					So(job.State, ShouldEqual, JobStateReady)
				}

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)

				jobs, err = jq.GetByRepGroup("foo", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 0)
			})

			Convey("You can store their (fake) runtime stats and get recommendations", func() {
				for index, job := range jobs {
					job.PeakRAM = index + 1
					job.PeakDisk = int64(index + 2)
					job.StartTime = time.Now()
					job.EndTime = job.StartTime.Add(time.Duration(index+1) * time.Second)
					server.db.updateJobAfterExit(job, []byte{}, []byte{}, false)
				}
				<-time.After(100 * time.Millisecond)
				rmem, err := server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 100)
				rdisk, err := server.db.recommendedReqGroupDisk("fake_group")
				So(err, ShouldBeNil)
				So(rdisk, ShouldEqual, 100)
				rtime, err := server.db.recommendedReqGroupTime("fake_group")
				So(err, ShouldBeNil)
				So(rtime, ShouldEqual, 1800)

				for i := 11; i <= 100; i++ {
					job := &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 4 * time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"}
					job.PeakRAM = i * 100
					job.PeakDisk = int64(i * 200)
					job.StartTime = time.Now()
					job.EndTime = job.StartTime.Add(time.Duration(i*100) * time.Second)
					server.db.updateJobAfterExit(job, []byte{}, []byte{}, false)
				}
				<-time.After(500 * time.Millisecond)
				rmem, err = server.db.recommendedReqGroupMemory("fake_group")
				So(err, ShouldBeNil)
				So(rmem, ShouldEqual, 9500)
				rdisk, err = server.db.recommendedReqGroupDisk("fake_group")
				So(err, ShouldBeNil)
				So(rdisk, ShouldEqual, 19000)
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
					job, err := jq.ReserveScheduled(50*time.Millisecond, "1024:240:1:0")
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
					So(job.EnvC, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateReserved)
				}

				Convey("Reserving when all have been reserved returns nil", func() {
					job, err := jq.ReserveScheduled(50*time.Millisecond, "1024:240:1:0")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					Convey("Adding one while waiting on a Reserve will return the new job", func() {
						worked := make(chan bool)
						go func() {
							job, err := jq.ReserveScheduled(1000*time.Millisecond, "1024:300:1:0")
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
							ticker := time.NewTicker(100 * time.Millisecond)
							ticks := 0
							for {
								select {
								case <-ticker.C:
									ticks++
									if ticks == 2 {
										jobs = append(jobs, &Job{Cmd: "new", Cwd: "/fake/cwd", ReqGroup: "add_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 5 * time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
										gojq, _ := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
										defer gojq.Disconnect()
										gojq.Add(jobs, envVars, true)
									}
									continue
								case w := <-worked:
									ticker.Stop()
									if w && ticks <= 8 {
										ok <- true
									}
									ok <- false
									return
								}
							}
						}()

						<-time.After(2 * time.Second)
						So(<-ok, ShouldBeTrue)
					})
				})
			})

			Convey("You can subsequently add more jobs", func() {
				for i := 10; i < 20; i++ {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "new_group", Requirements: &jqs.Requirements{RAM: 2048, Time: 1 * time.Hour, Cores: 2}, Retries: uint8(3), RepGroup: "manually_added"})
				}
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 10)
				So(already, ShouldEqual, 10)

				Convey("You can reserve jobs for a particular scheduler group", func() {
					for i := 10; i < 20; i++ {
						job, err := jq.ReserveScheduled(20*time.Millisecond, "2048:60:2:0")
						So(err, ShouldBeNil)
						So(job, ShouldNotBeNil)
						So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
					}
					job, err := jq.ReserveScheduled(10*time.Millisecond, "2048:60:2:0")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					for i := 9; i >= 0; i-- {
						jid := i
						if i == 7 {
							jid = 4
						} else if i == 4 {
							jid = 7
						}
						job, err = jq.ReserveScheduled(10*time.Millisecond, "1024:240:1:0")
						So(err, ShouldBeNil)
						So(job, ShouldNotBeNil)
						So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
					}
					job, err = jq.ReserveScheduled(10*time.Millisecond, "1024:240:1:0")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)
				})
			})

			Convey("You can add more jobs, but without any environment variables", func() {
				server.racmutex.Lock()
				server.rc = ""
				server.racmutex.Unlock()
				os.Setenv("wr_jobqueue_test_no_envvar", "a")
				inserts, already, err := jq.Add([]*Job{{Cmd: "echo $wr_jobqueue_test_no_envvar && false", Cwd: "/tmp", ReqGroup: "new_group", Requirements: standardReqs, Priority: uint8(100), RepGroup: "noenvvar"}}, []string{}, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.RepGroup, ShouldEqual, "noenvvar")

				env, err := job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldNotBeEmpty)

				os.Setenv("wr_jobqueue_test_no_envvar", "b")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "b")

				// by comparison, compare normal behaviour, where the initial
				// value of the envvar gets used for the job
				os.Setenv("wr_jobqueue_test_no_envvar", "a")
				inserts, already, err = jq.Add([]*Job{{Cmd: "echo $wr_jobqueue_test_no_envvar && false && false", Cwd: "/tmp", ReqGroup: "new_group", Requirements: standardReqs, Priority: uint8(101), RepGroup: "withenvvar"}}, os.Environ(), true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.RepGroup, ShouldEqual, "withenvvar")

				env, err = job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldNotBeEmpty)

				os.Setenv("wr_jobqueue_test_no_envvar", "b")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				stdout, err = job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "a")
			})

			Convey("You can add more jobs, overriding certain environment variables", func() {
				server.racmutex.Lock()
				server.rc = ""
				server.racmutex.Unlock()
				os.Setenv("wr_jobqueue_test_no_envvar", "a")
				compressed, err := jq.CompressEnv([]string{"wr_jobqueue_test_no_envvar=c", "wr_jobqueue_test_no_envvar2=d"})
				So(err, ShouldBeNil)
				inserts, already, err := jq.Add([]*Job{{
					Cmd:          "echo $wr_jobqueue_test_no_envvar && echo $wr_jobqueue_test_no_envvar2 && false",
					Cwd:          "/tmp",
					RepGroup:     "noenvvar",
					ReqGroup:     "new_group",
					Requirements: standardReqs,
					Priority:     uint8(100),
					Retries:      uint8(0),
					EnvOverride:  compressed,
				}}, []string{}, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.RepGroup, ShouldEqual, "noenvvar")

				env, err := job.Env()
				So(err, ShouldBeNil)
				So(env, ShouldNotBeEmpty)

				os.Setenv("wr_jobqueue_test_no_envvar", "b")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(job.FailReason, ShouldEqual, FailReasonExit)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "c\nd")
			})

			Convey("You can stop the server by sending it a SIGTERM or SIGINT", func() {
				jq.Disconnect()

				syscall.Kill(os.Getpid(), syscall.SIGTERM)
				<-time.After(ClientTouchInterval)
				<-time.After(ClientTouchInterval)
				_, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldNotBeNil)
				jqerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrNoServer)

				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				jq.Disconnect()

				syscall.Kill(os.Getpid(), syscall.SIGINT)
				<-time.After(ClientTouchInterval)
				<-time.After(ClientTouchInterval)
				_, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldNotBeNil)
				jqerr, ok = err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrNoServer)
			})

			Convey("You get a nice error if you send the server junk", func() {
				_, err := jq.request(&clientRequest{Method: "junk"})
				So(err, ShouldNotBeNil)
				jqerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrUnknownCommand)
				jq.Disconnect()
			})
		})

		Reset(func() {
			server.Stop(true)
		})
	})

	if server != nil {
		server.Stop(true)
	}

	// start these tests anew because I don't want to mess with the timings in
	// the above tests
	Convey("Once a new jobqueue server is up", t, func() {
		ServerItemTTR = 200 * time.Millisecond
		ClientTouchInterval = 50 * time.Millisecond
		server, _, token, errs = Serve(serverConfig)
		So(errs, ShouldBeNil)

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()
			jq2, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq2.Disconnect()

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "sleep 0.1 && true", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(2), RepGroup: "manually_added"})
			jobs = append(jobs, &Job{Cmd: "sleep 0.1 && false", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(2), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			Convey("You can't execute a job without reserving it", func() {
				err := jq.Execute(jobs[0], config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				jqerr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(jqerr.Err, ShouldEqual, ErrMustReserve)
				jq.Disconnect()
			})

			Convey("Once reserved you can execute jobs, and other clients see the correct state on gets", func() {
				// job that succeeds, no std out
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "sleep 0.1 && true")
				So(job.State, ShouldEqual, JobStateReserved)
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 3)

				job2, err := jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && true"}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, "sleep 0.1 && true")
				So(job2.State, ShouldEqual, JobStateReserved)

				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, JobStateComplete)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)
				So(job.PeakRAM, ShouldBeGreaterThan, 0)
				So(job.PeakDisk, ShouldEqual, 0)
				So(job.Pid, ShouldBeGreaterThan, 0)
				host, _ := os.Hostname()
				So(job.Host, ShouldEqual, host)
				So(job.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, 0*time.Millisecond)
				So(job.Attempts, ShouldEqual, 1)
				So(job.UntilBuried, ShouldEqual, 3)
				stdout, err := job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "")
				stderr, err := job.StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")
				actualCwd := job.ActualCwd
				So(actualCwd, ShouldStartWith, filepath.Join("/tmp", "jobqueue_cwd", "7", "4", "7", "27e23009c78b126f274aa64416f30"))
				So(actualCwd, ShouldEndWith, "cwd")

				job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && true"}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.State, ShouldEqual, JobStateComplete)
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 0)
				So(job2.PeakRAM, ShouldEqual, job.PeakRAM)
				So(job2.Pid, ShouldEqual, job.Pid)
				So(job2.Host, ShouldEqual, host)
				So(job2.WallTime(), ShouldBeLessThanOrEqualTo, job.WallTime())
				So(job2.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job2.CPUtime, ShouldEqual, job.CPUtime)
				So(job2.Attempts, ShouldEqual, 1)
				So(job2.ActualCwd, ShouldEqual, actualCwd)

				// job that fails, no std out
				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
				So(job.State, ShouldEqual, JobStateReserved)
				So(job.Attempts, ShouldEqual, 0)
				So(job.UntilBuried, ShouldEqual, 3)

				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "command [sleep 0.1 && false] exited with code 1, which may be a temporary issue, so it will be tried again")
				So(job.State, ShouldEqual, JobStateDelayed)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 1)
				So(job.PeakRAM, ShouldBeGreaterThan, 0)
				So(job.Pid, ShouldBeGreaterThan, 0)
				So(job.Host, ShouldEqual, host)
				So(job.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, 0*time.Millisecond)
				So(job.Attempts, ShouldEqual, 1)
				So(job.UntilBuried, ShouldEqual, 2)
				stdout, err = job.StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, "")
				stderr, err = job.StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")

				job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && false"}, false, false)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.State, ShouldEqual, JobStateDelayed)
				So(job2.Exited, ShouldBeTrue)
				So(job2.Exitcode, ShouldEqual, 1)
				So(job2.PeakRAM, ShouldEqual, job.PeakRAM)
				So(job2.Pid, ShouldEqual, job.Pid)
				So(job2.Host, ShouldEqual, host)
				So(job2.WallTime(), ShouldBeLessThanOrEqualTo, job.WallTime())
				So(job2.WallTime(), ShouldBeGreaterThanOrEqualTo, 1*time.Millisecond)
				So(job2.CPUtime, ShouldEqual, job.CPUtime)
				So(job2.Attempts, ShouldEqual, 1)

				Convey("Both current and archived jobs can be retrieved with GetByRepGroup", func() {
					jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(jobs), ShouldEqual, 2)

					Convey("But only current jobs are retrieved with GetIncomplete", func() {
						jobs, err = jq.GetIncomplete(0, "", false, false)
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
					job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && false"}, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateDelayed)

					<-time.After(60 * time.Millisecond)
					job, err = jq.Reserve(5 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Attempts, ShouldEqual, 1)
					So(job.UntilBuried, ShouldEqual, 2)
					job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && false"}, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateReserved)

					Convey("After 2 retries (3 attempts) it gets buried", func() {
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldNotBeNil)
						So(job.State, ShouldEqual, JobStateDelayed)
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 1)
						So(job.Attempts, ShouldEqual, 2)
						So(job.UntilBuried, ShouldEqual, 1)

						<-time.After(210 * time.Millisecond)
						job, err = jq.Reserve(5 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
						So(job.State, ShouldEqual, JobStateReserved)
						So(job.Attempts, ShouldEqual, 2)
						So(job.UntilBuried, ShouldEqual, 1)

						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldNotBeNil)
						So(job.State, ShouldEqual, JobStateBuried)
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, 1)
						So(job.Attempts, ShouldEqual, 3)
						So(job.UntilBuried, ShouldEqual, 0)

						<-time.After(210 * time.Millisecond)
						job, err = jq.Reserve(5 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job, ShouldBeNil)

						Convey("Once buried it can be kicked back to ready state and be reserved again", func() {
							job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && false"}, false, false)
							So(err, ShouldBeNil)
							So(job2, ShouldNotBeNil)
							So(job2.State, ShouldEqual, JobStateBuried)

							kicked, err := jq.Kick([]*JobEssence{{Cmd: "sleep 0.1 && false"}})
							So(err, ShouldBeNil)
							So(kicked, ShouldEqual, 1)

							job, err = jq.Reserve(5 * time.Millisecond)
							So(err, ShouldBeNil)
							So(job, ShouldNotBeNil)
							So(job.Cmd, ShouldEqual, "sleep 0.1 && false")
							So(job.State, ShouldEqual, JobStateReserved)
							So(job.Attempts, ShouldEqual, 3)
							So(job.UntilBuried, ShouldEqual, 3)

							job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && false"}, false, false)
							So(err, ShouldBeNil)
							So(job2, ShouldNotBeNil)
							So(job2.State, ShouldEqual, JobStateReserved)
							So(job2.Attempts, ShouldEqual, 3)
							So(job2.UntilBuried, ShouldEqual, 3)

							Convey("If you do nothing with a reserved job, it auto reverts back to delayed", func() {
								<-time.After(210 * time.Millisecond)
								job2, err = jq2.GetByEssence(&JobEssence{Cmd: "sleep 0.1 && false"}, false, false)
								So(err, ShouldBeNil)
								So(job2, ShouldNotBeNil)
								So(job2.State, ShouldEqual, JobStateDelayed)
								So(job2.Attempts, ShouldEqual, 3)
								So(job2.UntilBuried, ShouldEqual, 3)
							})
						})
					})
				})
			})

			Convey("Jobs can be deleted in any state except running", func() {
				for _, added := range jobs {
					job, err := jq.GetByEssence(&JobEssence{Cmd: added.Cmd}, false, false)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateReady)

					deleted, err := jq.Delete([]*JobEssence{{Cmd: added.Cmd}})
					So(err, ShouldBeNil)
					So(deleted, ShouldEqual, 1)

					job, err = jq.GetByEssence(&JobEssence{Cmd: added.Cmd}, false, false)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					//*** add tests to show this doesn't work if running...
				}
				job, err := jq.Reserve(5 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)

				Convey("Cmds with pipes in them are handled correctly", func() {
					jobs = nil
					jobs = append(jobs, &Job{Cmd: "sleep 0.1 && true | true", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_pass"})
					jobs = append(jobs, &Job{Cmd: "sleep 0.1 && true | false | true", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					// pipe job that succeeds
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && true | true")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					So(job.FailReason, ShouldEqual, "")

					// pipe job that fails in the middle
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "sleep 0.1 && true | false | true")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command [sleep 0.1 && true | false | true] exited with code 1, which may be a temporary issue, so it will be tried again") //*** can fail with a receive time out; why?!
					So(job.State, ShouldEqual, JobStateDelayed)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)
				})

				Convey("Invalid commands are immediately buried", func() {
					jobs = nil
					jobs = append(jobs, &Job{Cmd: "awesjnalakjf --foo", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					// job that fails because of non-existent exe
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "awesjnalakjf --foo")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command [awesjnalakjf --foo] exited with code 127 (command not found), which seems permanent, so it has been buried")
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 127)
					So(job.FailReason, ShouldEqual, FailReasonCFound)

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: "awesjnalakjf --foo"}, false, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateBuried)
					So(job2.FailReason, ShouldEqual, FailReasonCFound)

					//*** how to test the other bury cases of invalid exit code
					// and permission problems on the exe?
				})

				Convey("If a job uses more memory than expected it is not killed, but we recommend more next time", func() {
					jobs = nil
					cmd := "perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'"
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "highmem", Requirements: standardReqs, Retries: uint8(0), RepGroup: "too_much_mem"})
					RecMBRound = 1
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Requirements.RAM, ShouldEqual, 10)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					cmd2 := "echo another high mem job"
					jobs = append(jobs, &Job{Cmd: cmd2, Cwd: "/tmp", ReqGroup: "highmem", Requirements: standardReqs, Retries: uint8(0), RepGroup: "too_much_mem"})
					inserts, already, err = jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 1)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd2)
					So(job.State, ShouldEqual, JobStateReserved)
					So(job.Requirements.RAM, ShouldBeGreaterThanOrEqualTo, 100)
					jq.Release(job, &JobEndState{}, "")

					deleted, errd := jq.Delete([]*JobEssence{{Cmd: cmd}})
					So(errd, ShouldBeNil)
					So(deleted, ShouldEqual, 0)
					deleted, errd = jq.Delete([]*JobEssence{{Cmd: cmd2}})
					So(errd, ShouldBeNil)
					So(deleted, ShouldEqual, 1)
				})

				maxRAM, errp := internal.ProcMeminfoMBs()
				if errp == nil && maxRAM > 80000 { // authors high memory system
					Convey("If a job uses close to all memory on machine it is killed and we recommend more next time", func() {
						jobs = nil
						cmd := "perl -e '@a; for (1..1000) { push(@a, q[a] x 8000000000) }'"
						jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "run_out_of_mem"})
						RecMBRound = 1
						inserts, already, err := jq.Add(jobs, envVars, true)
						So(err, ShouldBeNil)
						So(inserts, ShouldEqual, 1)
						So(already, ShouldEqual, 0)

						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, cmd)
						So(job.State, ShouldEqual, JobStateReserved)

						ClientPercentMemoryKill = 1
						err = jq.Execute(job, config.RunnerExecShell)
						ClientPercentMemoryKill = 90
						So(err, ShouldNotBeNil)
						jqerr, ok := err.(Error)
						So(ok, ShouldBeTrue)
						So(jqerr.Err, ShouldEqual, FailReasonRAM)
						So(job.State, ShouldEqual, JobStateDelayed)
						So(job.Exited, ShouldBeTrue)
						So(job.Exitcode, ShouldEqual, -1)
						So(job.FailReason, ShouldEqual, FailReasonRAM)
						So(job.Requirements.RAM, ShouldBeGreaterThanOrEqualTo, 1000)
						deleted, errd := jq.Delete([]*JobEssence{{Cmd: cmd}})
						So(errd, ShouldBeNil)
						So(deleted, ShouldEqual, 1)
					})
				} else {
					SkipConvey("Skipping test that uses most of machine memory", func() {})
				}

				RecMBRound = 100 // revert back to normal

				Convey("Jobs that fork and change processgroup can still be fully killed", func() {
					jobs = nil

					tmpdir, err := ioutil.TempDir("", "wr_kill_test")
					So(err, ShouldBeNil)
					defer os.RemoveAll(tmpdir)

					cmd := fmt.Sprintf("perl -Mstrict -we 'open(OUT, qq[>%s/$$]); my $pid = fork; if ($pid == 0) { setpgrp; my $subpid = fork; if ($subpid == 0) { sleep(60); exit 0; } open(OUT, qq[>%s/$subpid]); waitpid $subpid, 0; exit 0; }  open(OUT, qq[>%s/$pid]); sleep(30); waitpid $pid, 0'", tmpdir, tmpdir, tmpdir)
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "forker"})
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					ich := make(chan int, 1)
					ech := make(chan error, 1)
					go func() {
						<-time.After(1 * time.Second)
						i, errk := jq.Kill([]*JobEssence{job.ToEssense()})
						ich <- i
						ech <- errk
					}()

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					jqerr, ok := err.(Error)
					So(ok, ShouldBeTrue)
					So(jqerr.Err, ShouldEqual, FailReasonKilled)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, -1)
					So(job.FailReason, ShouldEqual, FailReasonKilled)

					i := <-ich
					So(i, ShouldEqual, 1)
					err = <-ech
					So(err, ShouldBeNil)

					files, err := ioutil.ReadDir(tmpdir)
					So(err, ShouldBeNil)
					count := 0
					for _, file := range files {
						if file.IsDir() {
							continue
						}
						count++
						pid, err := strconv.Atoi(file.Name())
						So(err, ShouldBeNil)
						process, err := os.FindProcess(pid)
						So(err, ShouldBeNil)
						err = process.Signal(syscall.Signal(0))
						So(err, ShouldNotBeNil)
						So(err.Error(), ShouldContainSubstring, "process already finished")
					}
					So(count, ShouldEqual, 3)

					deleted, errd := jq.Delete([]*JobEssence{{Cmd: cmd}})
					So(errd, ShouldBeNil)
					So(deleted, ShouldEqual, 1)
				})

				Convey("Jobs that fork and change processgroup have correct memory usage reported", func() {
					jobs = nil
					cmd := `perl -Mstrict -we 'my $pid = fork; if ($pid == 0) { setpgrp; my $subpid = fork; if ($subpid == 0) { my @a; for (1..100) { push(@a, q[a] x 10000000); } exit 0; } waitpid $subpid, 0; exit 0; } my @b; for (1..100) { push(@b, q[b] x 1000000); } waitpid $pid, 0'`
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "forker"})
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					So(job.PeakRAM, ShouldBeGreaterThan, 500)
				})

				Convey("Jobs that fork and change processgroup have correct CPU time reported", func() {
					jobs = nil
					cmd := `perl -Mstrict -we 'my $pid = fork; if ($pid == 0) { setpgrp; my $subpid = fork; if ($subpid == 0) { my $a = 2; for (1..10000000) { $a *= $a } exit 0; } waitpid $subpid, 0; exit 0; } my $b = 2; for (1..10000000) { $b *= $b } waitpid $pid, 0'`
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(0), RepGroup: "forker"})
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)
					So(already, ShouldEqual, 0)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					So(job.CPUtime, ShouldBeGreaterThanOrEqualTo, job.WallTime()+(job.WallTime()/2))
				})

				Convey("The stdout/err of jobs is only kept for failed jobs, and cwd&TMPDIR&HOME get set appropriately", func() {
					jobs = nil
					baseDir, err := ioutil.TempDir("", "wr_jobqueue_test_runner_dir_")
					So(err, ShouldBeNil)
					defer os.RemoveAll(baseDir)
					tmpDir := filepath.Join(baseDir, "jobqueue tmpdir") // testing that it works with spaces in the name
					err = os.Mkdir(tmpDir, os.ModePerm)
					So(err, ShouldBeNil)
					jobs = append(jobs, &Job{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; warn File::Spec->tmpdir, qq[\\n]'", Cwd: tmpDir, CwdMatters: true, ChangeHome: true, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_pass"})
					jobs = append(jobs, &Job{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; die File::Spec->tmpdir, qq[\\n]'", Cwd: tmpDir, CwdMatters: false, ChangeHome: true, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					// job that outputs to stdout and stderr but succeeds
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; warn File::Spec->tmpdir, qq[\\n]'")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, tmpDir+"-"+os.Getenv("HOME"))
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, os.TempDir())

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; warn File::Spec->tmpdir, qq[\\n]'", Cwd: tmpDir}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateComplete)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "")
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "")

					// job that outputs to stdout and stderr and fails
					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; die File::Spec->tmpdir, qq[\\n]'")
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					So(job.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job.StdOut()
					So(err, ShouldBeNil)
					actualCwd := job.ActualCwd
					So(actualCwd, ShouldStartWith, filepath.Join(tmpDir, "jobqueue_cwd", "d", "4", "1", "7364d743329da784e74f2d69d438d"))
					So(actualCwd, ShouldEndWith, "cwd")
					So(stdout, ShouldEqual, actualCwd+"-"+actualCwd)
					stderr, err = job.StdErr()
					So(err, ShouldBeNil)
					tmpDir = actualCwd[:len(actualCwd)-3] + "tmp"
					So(stderr, ShouldEqual, tmpDir)

					job2, err = jq2.GetByEssence(&JobEssence{Cmd: "perl -MCwd -MFile::Spec -e '$cwd = getcwd(); print $cwd, qq[-], $ENV{HOME}, qq[\\n]; die File::Spec->tmpdir, qq[\\n]'"}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateBuried)
					So(job2.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, actualCwd+"-"+actualCwd)
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, tmpDir)
				})

				Convey("The stdout/err of jobs is limited in size", func() {
					jobs = nil
					jobs = append(jobs, &Job{Cmd: "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
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
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, expectederr)

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'"}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateBuried)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, expectederr)

					Convey("If you don't ask for stdout, you don't get it", func() {
						job2, err = jq2.GetByEssence(&JobEssence{Cmd: "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'"}, false, false)
						So(err, ShouldBeNil)
						So(job2, ShouldNotBeNil)
						So(job2.State, ShouldEqual, JobStateBuried)
						stdout, err = job2.StdOut()
						So(err, ShouldBeNil)
						So(stdout, ShouldEqual, "")
						stderr, err = job2.StdErr()
						So(err, ShouldBeNil)
						So(stderr, ShouldEqual, "")
					})
				})

				Convey("The stdout/err of jobs is filtered for \\r blocks", func() {
					jobs = nil
					progressCmd := "perl -e '$|++; print qq[a\nb\n\nprogress: 98%\r]; for (99..100) { print qq[progress: $_%\r]; sleep(1); } print qq[\n\nc\n]; exit(1)'"
					jobs = append(jobs, &Job{Cmd: progressCmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					expectedout := "a\nb\n\nprogress: 98%\n[...]\nprogress: 100%\n\nc\n"
					expectedout = strings.TrimSpace(expectedout)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, progressCmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "")

					jobs = nil
					progressCmd = "perl -e '$|++; print qq[a\nb\n\n]; for (99..100) { print qq[progress: $_%\r]; sleep(1); } print qq[\n\nc\n]; exit(1)'"
					jobs = append(jobs, &Job{Cmd: progressCmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail"})
					inserts, _, err = jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					expectedout = "a\nb\n\nprogress: 99%\nprogress: 100%\n\nc\n"
					expectedout = strings.TrimSpace(expectedout)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, progressCmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					stdout, err = job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, expectedout)
					stderr, err = job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "")
				})

				Convey("Jobs with long lines of stderr do not cause execution to hang", func() {
					jobs = nil
					bigerrCmd := `perl -e 'for (1..10) { for (1..65536) { print STDERR qq[e] } print STDERR qq[\n] }' && false`
					jobs = append(jobs, &Job{Cmd: bigerrCmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "bigerr"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, bigerrCmd)
					So(job.State, ShouldEqual, JobStateReserved)

					// wait for the job to finish executing
					done := make(chan bool, 1)
					go func() {
						go jq.Execute(job, config.RunnerExecShell)

						limit := time.After(10 * time.Second)
						ticker := time.NewTicker(500 * time.Millisecond)
						for {
							select {
							case <-ticker.C:
								jobs, err = jq.GetByRepGroup("bigerr", false, 0, JobStateBuried, false, false)
								if err != nil {
									continue
								}
								if len(jobs) == 1 {
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
					So(<-done, ShouldBeTrue)

					job, err = jq.GetByEssence(&JobEssence{Cmd: bigerrCmd}, true, false)
					So(err, ShouldBeNil)
					So(job, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldBeBlank)
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldStartWith, "eeeeeeeeeeeeeeeeeeeeeeeeeeee")
					So(stderr, ShouldContainSubstring, "... omitting 647178 bytes ...")
					So(stderr, ShouldEndWith, "eeeeeeeeeeeeeeeeeeeeeeeeeeee")
				})

				Convey("Job behaviours trigger correctly", func() {
					jobs = nil
					cwd, err := ioutil.TempDir("", "wr_jobqueue_test_runner_dir_")
					So(err, ShouldBeNil)
					defer os.RemoveAll(cwd)
					b1 := &Behaviour{When: OnSuccess, Do: CleanupAll}
					b2 := &Behaviour{When: OnFailure, Do: Run, Arg: "touch foo"}
					bs := Behaviours{b1, b2}
					jobs = append(jobs, &Job{Cmd: "touch bar", Cwd: cwd, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_pass", Behaviours: bs})
					jobs = append(jobs, &Job{Cmd: "touch bar && false", Cwd: cwd, ReqGroup: "fake_group", Requirements: standardReqs, RepGroup: "should_fail", Behaviours: bs})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "touch bar")
					So(job.State, ShouldEqual, JobStateReserved)
					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					actualCwd := job.ActualCwd
					So(actualCwd, ShouldStartWith, filepath.Join(cwd, "jobqueue_cwd", "6", "0", "8", "3ab5943a1918a9774e4644acb36f6"))
					So(actualCwd, ShouldEndWith, "cwd")
					_, err = os.Stat(filepath.Join(actualCwd, "bar"))
					So(err, ShouldNotBeNil)
					_, err = os.Stat(filepath.Join(actualCwd, "foo"))
					So(err, ShouldNotBeNil)
					entries, err := ioutil.ReadDir(cwd)
					So(err, ShouldBeNil)
					So(len(entries), ShouldEqual, 0)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "touch bar && false")
					So(job.State, ShouldEqual, JobStateReserved)
					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, JobStateBuried)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 1)
					So(job.FailReason, ShouldEqual, FailReasonExit)

					actualCwd = job.ActualCwd
					So(actualCwd, ShouldStartWith, filepath.Join(cwd, "jobqueue_cwd", "4", "4", "a", "758484033bddc46a51d3ec7517f2c"))
					So(actualCwd, ShouldEndWith, "cwd")
					_, err = os.Stat(filepath.Join(actualCwd, "bar"))
					So(err, ShouldBeNil)
					_, err = os.Stat(filepath.Join(actualCwd, "foo"))
					So(err, ShouldBeNil)
					entries, err = ioutil.ReadDir(cwd)
					So(err, ShouldBeNil)
					So(len(entries), ShouldEqual, 1)
					So(entries[0].Name(), ShouldEqual, "jobqueue_cwd")
				})

				Convey("Jobs that take longer than the ttr can execute successfully, even if clienttouchinterval is > ttr", func() {
					jobs = nil
					cmd := "perl -e 'for (1..3) { sleep(1) }'"
					jobs = append(jobs, &Job{Cmd: cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "should_pass"})
					inserts, _, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, JobStateComplete)
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					job2, err := jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, JobStateComplete)

					// same again, but we'll alter the clienttouchinterval to be > ttr
					ClientTouchInterval = 500 * time.Millisecond
					inserts, _, err = jq.Add(jobs, envVars, false)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err = jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, cmd)
					So(job.State, ShouldEqual, JobStateReserved)

					lostCh := make(chan bool)
					go func() {
						<-time.After(300 * time.Millisecond)
						// after ttr but before first touch, it becomes lost
						job2f, errf := jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
						if errf != nil || job2f == nil || job2f.State != JobStateLost || job2f.FailReason != FailReasonLost || job2f.Exited {
							lostCh <- false
						}

						<-time.After(250 * time.Millisecond)
						// after the first touch, it becomes running again
						job2f, errf = jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
						if errf != nil || job2f == nil || job2f.State != JobStateRunning || job2f.FailReason != FailReasonLost || job2f.Exited {
							lostCh <- false
						}
						lostCh <- true
					}()

					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)

					job2, err = jq2.GetByEssence(&JobEssence{Cmd: cmd}, true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)

					So(job2.State, ShouldEqual, JobStateComplete)
					So(job2.Exited, ShouldBeTrue)
					So(<-lostCh, ShouldBeTrue)
				})
			})
		})

		Convey("After connecting and adding some jobs under one RepGroup", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			var jobs []*Job
			for i := 0; i < 3; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo rgduptest %d", i), Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "rp1"})
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can reserve and execute those", func() {
				for i := 0; i < 3; i++ {
					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					err = jq.Execute(job, config.RunnerExecShell)
					So(err, ShouldBeNil)
				}

				Convey("Then you can add dups and a new one under a new RepGroup and reserve/execute all of them", func() {
					jobs = nil
					for i := 0; i < 4; i++ {
						jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo rgduptest %d", i), Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "rp2"})
					}
					inserts, already, err := jq.Add(jobs, envVars, false)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 4)
					So(already, ShouldEqual, 0)

					for i := 0; i < 4; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					Convey("The jobs can be retrieved by either RepGroup and will have the expected RepGroup", func() {
						jobsg, err := jq.GetByRepGroup("rp1", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobsg), ShouldEqual, 3)
						So(jobsg[0].RepGroup, ShouldEqual, "rp1")

						jobsg, err = jq.GetByRepGroup("rp2", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobsg), ShouldEqual, 4)
						So(jobsg[0].RepGroup, ShouldEqual, "rp2")
					})
				})

				Convey("Previously complete jobs are rejected when adding with ignoreComplete", func() {
					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 0)
					So(already, ShouldEqual, 3)
				})
			})

			Convey("You can add dups and a new one under a new RepGroup", func() {
				jobs = nil
				for i := 0; i < 4; i++ {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo rgduptest %d", i), Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "rp2"})
				}
				inserts, already, err := jq.Add(jobs, envVars, false)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 3)

				Convey("You can then reserve and execute the only 4 jobs", func() {
					for i := 0; i < 4; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}
					job, err := jq.Reserve(10 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					Convey("The jobs can be retrieved by either RepGroup and will have the expected RepGroup", func() {
						jobs, err := jq.GetByRepGroup("rp1", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 3)
						So(jobs[0].RepGroup, ShouldEqual, "rp1")

						jobs, err = jq.GetByRepGroup("rp2", false, 0, JobStateComplete, false, false)
						So(err, ShouldBeNil)
						So(len(jobs), ShouldEqual, 4)
						So(jobs[0].RepGroup, ShouldEqual, "rp2")
					})
				})
			})
		})

		Convey("After connecting and adding some jobs under some RepGroups", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo deptest1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep1"})
			jobs = append(jobs, &Job{Cmd: "echo deptest2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep2"})
			jobs = append(jobs, &Job{Cmd: "echo deptest3", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep3"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can search for the jobs using a common substring of their repgroups", func() {
				gottenJobs, err := jq.GetByRepGroup("dep", true, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 3)
				gottenJobs, err = jq.GetByRepGroup("2", true, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
			})

			Convey("You can reserve and execute one of them", func() {
				j1, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(j1.RepGroup, ShouldEqual, "dep1")
				err = jq.Execute(j1, config.RunnerExecShell)
				So(err, ShouldBeNil)

				<-time.After(6 * time.Millisecond)

				gottenJobs, err := jq.GetByRepGroup("dep1", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

				Convey("You can then add jobs dependent on the initial jobs and themselves", func() {
					// https://i-msdn.sec.s-msft.com/dynimg/IC332764.gif
					jobs = nil
					d1 := NewEssenceDependency("echo deptest1", "")
					d2 := NewEssenceDependency("echo deptest2", "")
					d3 := NewEssenceDependency("echo deptest3", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", Dependencies: Dependencies{d1}})
					d4 := NewEssenceDependency("echo deptest4", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5", Dependencies: Dependencies{d1, d2, d3}})
					d5 := NewEssenceDependency("echo deptest5", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest6", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep6", Dependencies: Dependencies{d3, d4}})
					d6 := NewEssenceDependency("echo deptest6", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest7", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep7", Dependencies: Dependencies{d5, d6}})
					jobs = append(jobs, &Job{Cmd: "echo deptest8", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep8", Dependencies: Dependencies{d5}})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 5)
					So(already, ShouldEqual, 0)

					// dep4 was added with a dependency on dep1, but after dep1
					// was already completed; it should start off in the ready
					// queue, not the dependent queue
					gottenJobs, err = jq.GetByRepGroup("dep4", false, 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(gottenJobs), ShouldEqual, 1)
					So(gottenJobs[0].State, ShouldEqual, JobStateReady)

					Convey("They are then only reservable according to the dependency chain", func() {
						j2, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j2.RepGroup, ShouldEqual, "dep2")
						j3, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j3.RepGroup, ShouldEqual, "dep3")
						j4, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j4.RepGroup, ShouldEqual, "dep4")
						jNil, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						// since things can take a while, we keep our reserved
						// jobs touched
						touchJ2 := true
						touchJ3 := true
						var touchLock sync.Mutex
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ2 {
									jq.Touch(j2)
								}
								if touchJ3 {
									jq.Touch(j3)
								}
								if !touchJ2 && !touchJ3 {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(j4, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ3 = false
						touchLock.Unlock()
						err = jq.Execute(j3, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ2 = false
						touchLock.Unlock()
						err = jq.Execute(j2, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						j5, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j5.RepGroup, ShouldEqual, "dep5")
						j6, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j6.RepGroup, ShouldEqual, "dep6")
						jNil, err = jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						touchJ6 := true
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ6 {
									jq.Touch(j6)
								} else {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(j5, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ6 = false
						touchLock.Unlock()
						err = jq.Execute(j6, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)
					})
				})

				Convey("You can add jobs with non-existent dependencies", func() {
					// first get rid of the jobs added earlier
					for i := 0; i < 2; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					jobs = nil
					d5 := NewEssenceDependency("echo deptest5", "")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", Dependencies: Dependencies{d5}})
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5"})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)
					So(already, ShouldEqual, 0)

					j5, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j5.RepGroup, ShouldEqual, "dep5")
					jNil, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(jNil, ShouldBeNil)

					err = jq.Execute(j5, config.RunnerExecShell)
					So(err, ShouldBeNil)

					j4, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j4.RepGroup, ShouldEqual, "dep4")

					jq.Release(j4, nil, "")

					// *** we should implement rejection of dependency cycles
					// and test for that
				})
			})
		})

		Convey("After connecting you can add some jobs with DepGroups", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo deptest1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep1", DepGroups: []string{"dep1", "dep1+2+3"}})
			jobs = append(jobs, &Job{Cmd: "echo deptest2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep2", DepGroups: []string{"dep2", "dep1+2+3"}})
			jobs = append(jobs, &Job{Cmd: "echo deptest3", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep3", DepGroups: []string{"dep3", "dep1+2+3"}})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 3)
			So(already, ShouldEqual, 0)

			Convey("You can reserve and execute one of them", func() {
				j1, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(j1.RepGroup, ShouldEqual, "dep1")
				err = jq.Execute(j1, config.RunnerExecShell)
				So(err, ShouldBeNil)

				<-time.After(6 * time.Millisecond)

				gottenJobs, err := jq.GetByRepGroup("dep1", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(gottenJobs), ShouldEqual, 1)
				So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

				Convey("You can then add jobs dependent on the initial jobs and themselves", func() {
					// https://i-msdn.sec.s-msft.com/dynimg/IC332764.gif
					jobs = nil
					d1 := NewDepGroupDependency("dep1")
					d123 := NewDepGroupDependency("dep1+2+3")
					d3 := NewDepGroupDependency("dep3")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", DepGroups: []string{"dep4"}, Dependencies: Dependencies{d1}})
					d4 := NewDepGroupDependency("dep4")
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5", DepGroups: []string{"dep5"}, Dependencies: Dependencies{d123}})
					d5 := NewDepGroupDependency("dep5")
					jobs = append(jobs, &Job{Cmd: "echo deptest6", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep6", DepGroups: []string{"dep6"}, Dependencies: Dependencies{d3, d4}})
					d6 := NewDepGroupDependency("dep6")
					jobs = append(jobs, &Job{Cmd: "echo deptest7", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep7", DepGroups: []string{"final"}, Dependencies: Dependencies{d5, d6}})
					jobs = append(jobs, &Job{Cmd: "echo deptest8", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep8", DepGroups: []string{"final"}, Dependencies: Dependencies{d5}})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 5)
					So(already, ShouldEqual, 0)

					// dep4 was added with a dependency on dep1, but after dep1
					// was already completed; it should start off in the ready
					// queue, not the dependent queue
					gottenJobs, err = jq.GetByRepGroup("dep4", false, 0, "", false, false)
					So(err, ShouldBeNil)
					So(len(gottenJobs), ShouldEqual, 1)
					So(gottenJobs[0].State, ShouldEqual, JobStateReady)

					Convey("They are then only reservable according to the dependency chain", func() {
						j2, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j2.RepGroup, ShouldEqual, "dep2")
						j3, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j3.RepGroup, ShouldEqual, "dep3")
						j4, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j4.RepGroup, ShouldEqual, "dep4")
						jNil, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						// since things can take a while, we keep our reserved
						// jobs touched
						touchJ2 := true
						touchJ3 := true
						var touchLock sync.Mutex
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ2 {
									jq.Touch(j2)
								}
								if touchJ3 {
									jq.Touch(j3)
								}
								if !touchJ2 && !touchJ3 {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(j4, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ3 = false
						touchLock.Unlock()
						err = jq.Execute(j3, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep6", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ2 = false
						touchLock.Unlock()
						err = jq.Execute(j2, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep5", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						j5, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j5.RepGroup, ShouldEqual, "dep5")
						j6, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(j6.RepGroup, ShouldEqual, "dep6")
						jNil, err = jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						So(jNil, ShouldBeNil)

						touchJ6 := true
						go func() {
							ticker := time.NewTicker(50 * time.Millisecond)
							for range ticker.C {
								touchLock.Lock()
								if touchJ6 {
									jq.Touch(j6)
								} else {
									ticker.Stop()
									touchLock.Unlock()
									return
								}
								touchLock.Unlock()
								continue
							}
						}()

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						err = jq.Execute(j5, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep8", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

						touchLock.Lock()
						touchJ6 = false
						touchLock.Unlock()
						err = jq.Execute(j6, config.RunnerExecShell)
						So(err, ShouldBeNil)

						gottenJobs, err = jq.GetByRepGroup("dep7", false, 0, "", false, false)
						So(err, ShouldBeNil)
						So(len(gottenJobs), ShouldEqual, 1)
						So(gottenJobs[0].State, ShouldEqual, JobStateReady)

						Convey("DepGroup dependencies are live, bringing back jobs if new jobs are added that match their dependencies", func() {
							jobs = nil
							dfinal := NewDepGroupDependency("final")
							jobs = append(jobs, &Job{Cmd: "echo after final", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "afterfinal", DepGroups: []string{"afterfinal"}, Dependencies: Dependencies{dfinal}})
							dafinal := NewDepGroupDependency("afterfinal")
							jobs = append(jobs, &Job{Cmd: "echo after after-final", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "after-afterfinal", Dependencies: Dependencies{dafinal}})
							inserts, already, err := jq.Add(jobs, envVars, true)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 2)
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							j7, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j7.RepGroup, ShouldEqual, "dep7")
							err = jq.Execute(j7, config.RunnerExecShell)
							So(err, ShouldBeNil)
							j8, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j8.RepGroup, ShouldEqual, "dep8")
							err = jq.Execute(j8, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							jobs = nil
							jobs = append(jobs, &Job{Cmd: "echo deptest9", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep9", DepGroups: []string{"final"}})
							inserts, already, err = jq.Add(jobs, envVars, true)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 1)
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							j9, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j9.RepGroup, ShouldEqual, "dep9")
							err = jq.Execute(j9, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							faf, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(faf.RepGroup, ShouldEqual, "afterfinal")
							err = jq.Execute(faf, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							inserts, already, err = jq.Add(jobs, envVars, false)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 2) // the job I added, and the resurrected afterfinal job
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							j9, err = jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(j9.RepGroup, ShouldEqual, "dep9")
							err = jq.Execute(j9, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							faf, err = jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(faf.RepGroup, ShouldEqual, "afterfinal")
							err = jq.Execute(faf, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateReady)

							faaf, err := jq.Reserve(50 * time.Millisecond)
							So(err, ShouldBeNil)
							So(faaf.RepGroup, ShouldEqual, "after-afterfinal")
							err = jq.Execute(faaf, config.RunnerExecShell)
							So(err, ShouldBeNil)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateComplete)

							jobs = nil
							jobs = append(jobs, &Job{Cmd: "echo deptest10", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep10", DepGroups: []string{"final"}})
							inserts, already, err = jq.Add(jobs, envVars, true)
							So(err, ShouldBeNil)
							So(inserts, ShouldEqual, 3)
							So(already, ShouldEqual, 0)

							gottenJobs, err = jq.GetByRepGroup("afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)

							gottenJobs, err = jq.GetByRepGroup("after-afterfinal", false, 0, "", false, false)
							So(err, ShouldBeNil)
							So(len(gottenJobs), ShouldEqual, 1)
							So(gottenJobs[0].State, ShouldEqual, JobStateDependent)
						})
					})
				})

				Convey("You can add jobs with non-existent depgroup dependencies", func() {
					// first get rid of the jobs added earlier
					for i := 0; i < 2; i++ {
						job, err := jq.Reserve(50 * time.Millisecond)
						So(err, ShouldBeNil)
						err = jq.Execute(job, config.RunnerExecShell)
						So(err, ShouldBeNil)
					}

					jobs = nil
					d5 := NewDepGroupDependency("dep5")
					jobs = append(jobs, &Job{Cmd: "echo deptest4", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep4", Dependencies: Dependencies{d5}})
					jobs = append(jobs, &Job{Cmd: "echo deptest5", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: standardReqs, Retries: uint8(3), RepGroup: "dep5", DepGroups: []string{"dep5"}})

					inserts, already, err := jq.Add(jobs, envVars, true)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 2)
					So(already, ShouldEqual, 0)

					j5, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j5.RepGroup, ShouldEqual, "dep5")
					jNil, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(jNil, ShouldBeNil)

					err = jq.Execute(j5, config.RunnerExecShell)
					So(err, ShouldBeNil)

					j4, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(j4.RepGroup, ShouldEqual, "dep4")

					jq.Release(j4, nil, "")

					// *** we should implement rejection of dependency cycles
					// and test for that
				})
			})
		})

		Reset(func() {
			server.Stop(true)
		})
	})

	if server != nil {
		server.Stop(true)
	}

	// start these tests anew because I need to disable dev-mode wiping of the
	// db to test some behaviours
	Convey("Once a new jobqueue server is up it creates a db file", t, func() {
		ServerItemTTR = 2 * time.Second
		forceBackups = true
		defer func() {
			forceBackups = false
		}()
		server, _, token, errs = Serve(serverConfig)
		So(errs, ShouldBeNil)
		_, err := os.Stat(config.ManagerDbFile)
		So(err, ShouldBeNil)
		_, err = os.Stat(managerDBBkFile)
		So(err, ShouldNotBeNil)

		Convey("You can connect, and add 2 jobs, which creates a db backup", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			// do this in 2 separate Add() calls to better test how backups
			// work
			server.db.slowBackups = true
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			wait := make(chan bool)
			go func() {
				<-time.After(150 * time.Millisecond)
				wait <- true
				<-time.After(300 * time.Millisecond)
				wait <- true
			}()
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)
			jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 1)

			tmpPath := managerDBBkFile + ".tmp"
			<-wait
			_, err = os.Stat(tmpPath)
			So(err, ShouldBeNil)

			<-wait

			info, err := os.Stat(config.ManagerDbFile)
			So(err, ShouldBeNil)
			So(info.Size(), ShouldEqual, 65536) // don't know if this will be consistent across platforms and versions...
			info2, err := os.Stat(managerDBBkFile)
			So(err, ShouldBeNil)
			So(info2.Size(), ShouldEqual, 32768) // *** don't know why it's so much smaller...
			_, err = os.Stat(tmpPath)
			So(err, ShouldNotBeNil)

			Convey("You can create manual backups that work correctly", func() {
				manualBackup := managerDBBkFile + ".manual"
				err = jq.BackupDB(manualBackup)
				So(err, ShouldBeNil)
				info3, err := os.Stat(manualBackup)
				So(err, ShouldBeNil)
				So(info3.Size(), ShouldEqual, 32768)

				server.Stop(true)
				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err := jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 0)

				server.Stop(true)
				os.Rename(manualBackup, config.ManagerDbFile)
				wipeDevDBOnInit = false
				defer func() {
					wipeDevDBOnInit = true
				}()
				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)
			})

			Convey("You can stop the server, delete or corrupt the database, and it will be restored from backup", func() {
				jobsByRepGroup, err := jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)

				server.Stop(true)
				wipeDevDBOnInit = false
				defer func() {
					wipeDevDBOnInit = true
				}()
				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)

				server.Stop(true)
				os.Remove(config.ManagerDbFile)
				_, err = os.Stat(config.ManagerDbFile)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(managerDBBkFile)
				So(err, ShouldBeNil)

				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)

				info, err = os.Stat(config.ManagerDbFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 32768)
				info2, err = os.Stat(managerDBBkFile)
				So(err, ShouldBeNil)
				So(info2.Size(), ShouldEqual, 32768)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)

				server.Stop(true)
				f, err := os.OpenFile(config.ManagerDbFile, os.O_TRUNC|os.O_RDWR, dbFilePermission)
				So(err, ShouldBeNil)
				f.WriteString("corrupt!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				f.Sync()
				f.Close()

				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)

				info, err = os.Stat(config.ManagerDbFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 32768)
				info2, err = os.Stat(managerDBBkFile)
				So(err, ShouldBeNil)
				So(info2.Size(), ShouldEqual, 32768)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 2)
			})

			Convey("You can reserve & execute just 1 of the jobs, stop the server, restart it, and then reserve & execute the other", func() {
				job, err := jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job.Cmd, ShouldEqual, "echo 1")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, JobStateComplete)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)

				server.Stop(true)
				wipeDevDBOnInit = false
				server, _, token, errs = Serve(serverConfig)
				wipeDevDBOnInit = true
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Cmd, ShouldEqual, "echo 2")
				err = jq.Execute(job, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job.State, ShouldEqual, JobStateComplete)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)
			})
		})

		Convey("You can connect, add a job, then immediately shutdown, and the db backup still completes", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			server.db.slowBackups = true
			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)
			server.Stop(true)

			info, err := os.Stat(config.ManagerDbFile)
			So(err, ShouldBeNil)
			So(info.Size(), ShouldEqual, 32768)
			info2, err := os.Stat(managerDBBkFile)
			So(err, ShouldBeNil)
			So(info2.Size(), ShouldEqual, 28672)

			Convey("You can restart the server with that existing job, delete it, and it stays deleted when restoring from backup", func() {
				wipeDevDBOnInit = false
				defer func() {
					wipeDevDBOnInit = true
				}()
				errr := os.Remove(config.ManagerDbFile)
				So(errr, ShouldBeNil)
				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				job, err := jq.Reserve(15 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				errb := jq.Bury(job, nil, "")
				So(errb, ShouldBeNil)
				deleted, err := jq.Delete([]*JobEssence{{JobKey: job.Key()}})
				server.Stop(true)
				So(deleted, ShouldEqual, 1)
				So(err, ShouldBeNil)

				errr = os.Remove(config.ManagerDbFile)
				So(errr, ShouldBeNil)
				server, _, token, errs = Serve(serverConfig)
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				job, err = jq.Reserve(15 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job, ShouldBeNil)
			})
		})

		Convey("You can connect and add a non-instant job", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			var jobs []*Job
			job1Cmd := "sleep 1 && echo noninstant"
			jobs = append(jobs, &Job{Cmd: job1Cmd, Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "nij"})
			inserts, already, err := jq.Add(jobs, envVars, true)
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

				jobs = append(jobs, &Job{Cmd: "echo added", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "nij"})
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 1)

				job2, err := jq.Reserve(10 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldBeNil)

				<-time.After(3 * time.Second)

				_, err = jq.Ping(10 * time.Millisecond)
				So(err, ShouldNotBeNil)

				wipeDevDBOnInit = false
				server, _, token, errs = Serve(serverConfig)
				wipeDevDBOnInit = true
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.Exited, ShouldBeTrue)
				So(job.Exitcode, ShouldEqual, 0)

				job2, err = jq.Reserve(50 * time.Millisecond)
				So(err, ShouldBeNil)
				So(job2, ShouldNotBeNil)
				So(job2.Cmd, ShouldEqual, "echo added")
				err = jq.Execute(job2, config.RunnerExecShell)
				So(err, ShouldBeNil)
				So(job2.State, ShouldEqual, JobStateComplete)
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

				jobs = append(jobs, &Job{Cmd: "echo added", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "nij"})
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldNotBeNil)

				<-time.After(2 * time.Second)

				_, err = jq.Ping(10 * time.Millisecond)
				So(err, ShouldNotBeNil)

				jq.Disconnect() // user must always Disconnect before connecting again!

				wipeDevDBOnInit = false
				server, _, token, errs = Serve(serverConfig)
				wipeDevDBOnInit = true
				So(errs, ShouldBeNil)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				job, err = jq.GetByEssence(&JobEssence{Cmd: job1Cmd}, false, false)
				So(err, ShouldBeNil)
				So(job.Exited, ShouldBeFalse)
			})
		})

		Reset(func() {
			server.Stop(true)
		})
	})

	if server != nil {
		server.Stop(true)
	}

	// start these tests anew because these tests have the server spawn runners
	Convey("Once a new jobqueue server is up", t, func() {
		ServerItemTTR = 10 * time.Second
		ClientTouchInterval = 50 * time.Millisecond
		pwd, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		runnertmpdir, err := ioutil.TempDir(pwd, "wr_jobqueue_test_runner_dir_")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(runnertmpdir)

		// our runnerCmd will be running ourselves in --runnermode, so first
		// we'll compile ourselves to the tmpdir
		runnerCmd := filepath.Join(runnertmpdir, "runner")
		cmd := exec.Command("go", "test", "-tags", "netgo", "-run", "TestJobqueue", "-c", "-o", runnerCmd)
		err = cmd.Run()
		if err != nil {
			log.Fatal(err)
		}

		runningConfig := serverConfig
		runningConfig.RunnerCmd = runnerCmd + " --runnermode --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s --rtimeout %d --maxmins %d --tmpdir " + runnertmpdir
		server, _, token, errs = Serve(runningConfig)
		So(errs, ShouldBeNil)
		maxCPU := runtime.NumCPU()
		runtime.GOMAXPROCS(maxCPU)

		Convey("You can connect, and add a job that you can kill while it's running", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "sleep 20", Cwd: "/tmp", ReqGroup: "sleep", Requirements: &jqs.Requirements{RAM: 1, Time: 20 * time.Second, Cores: 1}, Retries: uint8(3), Override: uint8(2), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the job to start running
			started := make(chan bool, 1)
			go func() {
				limit := time.After(10 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateRunning, false, false)
						if err != nil {
							continue
						}
						if len(jobs) == 1 {
							ticker.Stop()
							started <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						started <- false
						return
					}
				}
			}()
			So(<-started, ShouldBeTrue)

			killCount, err := jq.Kill([]*JobEssence{{Cmd: "sleep 20"}})
			So(err, ShouldBeNil)
			So(killCount, ShouldEqual, 1)

			// wait for the job to get killed
			killed := make(chan bool, 1)
			go func() {
				limit := time.After(2 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
						if err != nil {
							continue
						}
						if len(jobs) == 1 {
							ticker.Stop()
							killed <- true
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						killed <- false
						return
					}
				}
			}()
			So(<-killed, ShouldBeTrue)

			jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].State, ShouldEqual, JobStateBuried)
			So(jobs[0].FailReason, ShouldEqual, FailReasonKilled)
			So(jobs[0].Exitcode, ShouldEqual, -1)
		})

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			tmpdir, err := ioutil.TempDir("", "wr_jobqueue_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			var jobs []*Job
			count := maxCPU * 2
			for i := 0; i < count; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: "perl", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run", func() {
				// wait for the jobs to get run
				done := make(chan bool, 1)
				go func() {
					limit := time.After(30 * time.Second)
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

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, count)
				ran := 0
				for _, job := range jobs {
					files, err := ioutil.ReadDir(job.ActualCwd)
					if err != nil {
						log.Fatal(err)
					}
					for range files {
						ran++
					}
				}
				So(ran, ShouldEqual, count)

				// we shouldn't have executed any unnecessary runners, and those
				// we did run should have exited without error, even if there
				// were no more jobs left
				files, err := ioutil.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}
				ranClean := 0
				for range files {
					ranClean++
				}
				So(ranClean, ShouldEqual, maxCPU+1) // +1 for the runner exe
			})
		})

		Convey("You can connect, and add a job that buries with no retries", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			tmpdir, err := ioutil.TempDir("", "wr_jobqueue_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1 && false", Cwd: tmpdir, ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(0), Override: uint8(2), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("After some time the jobs get automatically run without excess runners", func() {
				// wait for the jobs to get run
				done := make(chan bool, 1)
				waitForBury := func() {
					limit := time.After(30 * time.Second)
					ticker := time.NewTicker(50 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
							if err != nil {
								continue
							}
							if len(jobs) == 1 {
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
				}
				go waitForBury()
				So(<-done, ShouldBeTrue)

				jobs = nil
				jobs = append(jobs, &Job{Cmd: "echo 2 && true", Cwd: tmpdir, ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(0), Override: uint8(2), RepGroup: "manually_added"})
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				done2 := make(chan bool, 1)
				waitForNoRunners := func() {
					limit := time.After(30 * time.Second)
					ticker := time.NewTicker(500 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							if !server.HasRunners() {
								ticker.Stop()
								done2 <- true
								return
							}
							continue
						case <-limit:
							ticker.Stop()
							done2 <- false
							return
						}
					}
				}
				go waitForNoRunners()
				So(<-done2, ShouldBeTrue)

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateBuried, false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)
				buriedKey := jobs[0].Key()
				jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)

				// we shouldn't have executed any unnecessary runners
				files, err := ioutil.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}
				ranClean := 0
				for _, file := range files {
					if !strings.HasPrefix(file.Name(), "ok") {
						continue
					}
					ranClean++
				}
				So(ranClean, ShouldEqual, 1)

				Convey("Retrying buried jobs works multiple times in a row", func() {
					kicked, err := jq.Kick([]*JobEssence{{JobKey: buriedKey}})
					So(err, ShouldBeNil)
					So(kicked, ShouldEqual, 1)

					go waitForBury()
					So(<-done, ShouldBeTrue)

					kicked, err = jq.Kick([]*JobEssence{{JobKey: buriedKey}})
					So(err, ShouldBeNil)
					So(kicked, ShouldEqual, 1)

					go waitForBury()
					So(<-done, ShouldBeTrue)

					go waitForNoRunners()
					So(<-done2, ShouldBeTrue)
				})
			})
		})

		if maxCPU > 2 {
			Convey("You can connect and add jobs in alternating scheduler groups and they don't pend", func() {
				jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)
				defer jq.Disconnect()

				req1 := &jqs.Requirements{RAM: 10, Time: 4 * time.Second, Cores: 1}
				jobs := []*Job{{Cmd: "echo 1 && sleep 4", Cwd: "/tmp", ReqGroup: "req1", Requirements: req1, RepGroup: "a"}}
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				<-time.After(1 * time.Second)

				job, err := jq.GetByEssence(&JobEssence{Cmd: "echo 1 && sleep 4"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				jobs = []*Job{{Cmd: "echo 2 && sleep 3", Cwd: "/tmp", ReqGroup: "req2", Requirements: &jqs.Requirements{RAM: 10, Time: 4 * time.Hour, Cores: 1}, RepGroup: "a"}}
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				<-time.After(1 * time.Second)

				job, err = jq.GetByEssence(&JobEssence{Cmd: "echo 2 && sleep 3"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				jobs = []*Job{{Cmd: "echo 3 && sleep 2", Cwd: "/tmp", ReqGroup: "req1", Requirements: req1, RepGroup: "a"}}
				inserts, already, err = jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				<-time.After(1 * time.Second)

				job, err = jq.GetByEssence(&JobEssence{Cmd: "echo 3 && sleep 2"}, false, false)
				So(err, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(job.State, ShouldEqual, JobStateRunning)

				// let them all complete
				done := make(chan bool, 1)
				go func() {
					limit := time.After(30 * time.Second)
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
				So(<-done, ShouldBeTrue)
			})
		} else {
			SkipConvey("Skipping a test that needs at least 3 cores", func() {})
		}

		Convey("You can connect, and add 2 real jobs with the same reqs sequentially that run simultaneously", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			jobs := []*Job{{Cmd: fmt.Sprintf("perl -e 'print q[%s2sim%d]; sleep(5);'", runnertmpdir, 1), Cwd: runnertmpdir, ReqGroup: "perl2sim", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"}}

			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for this first command to start running
			running := make(chan bool, 1)
			go func() {
				limit := time.After(30 * time.Second)
				ticker := time.NewTicker(500 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						pids, errf := process.Pids()
						if errf == nil {
							for _, pid := range pids {
								p, errf := process.NewProcess(pid)
								if errf == nil {
									cmd, errf := p.Cmdline()
									if errf == nil {
										if strings.Contains(cmd, runnertmpdir+"2sim") {
											status, errf := p.Status()
											if errf == nil && status == "S" {
												ticker.Stop()
												running <- true
												return
											}
										}
									}
								}
							}
						}
						if !server.HasRunners() {
							ticker.Stop()
							running <- false
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						running <- false
						return
					}
				}
			}()
			So(<-running, ShouldBeTrue)

			jobs = []*Job{{Cmd: fmt.Sprintf("perl -e 'print q[%s2sim%d]; sleep(5);'", runnertmpdir, 2), Cwd: runnertmpdir, ReqGroup: "perl2sim", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"}}

			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			// wait for the jobs to get run, and while waiting we'll check to
			// see if we get both of our commands running at once
			numRanSimultaneously := make(chan int, 1)
			go func() {
				limit := time.After(30 * time.Second)
				ticker := time.NewTicker(500 * time.Millisecond)
				maxSimultaneous := 0
				for {
					select {
					case <-ticker.C:
						pids, err := process.Pids()
						if err == nil {
							simultaneous := 0
							for _, pid := range pids {
								p, err := process.NewProcess(pid)
								if err == nil {
									cmd, err := p.Cmdline()
									if err == nil {
										if strings.Contains(cmd, runnertmpdir+"2sim") {
											status, err := p.Status()
											if err == nil && status == "S" {
												simultaneous++
											}
										}
									}
								}
							}
							if simultaneous > maxSimultaneous {
								maxSimultaneous = simultaneous
							}
						}
						if !server.HasRunners() {
							ticker.Stop()
							numRanSimultaneously <- maxSimultaneous
							return
						}
						continue
					case <-limit:
						ticker.Stop()
						numRanSimultaneously <- maxSimultaneous
						return
					}
				}
			}()
			So(<-numRanSimultaneously, ShouldEqual, 2)
		})

		Convey("You can connect, and add 2 large batches of jobs sequentially", func() {
			// if possible, we want these tests to use the LSF scheduler which
			// reveals more issues
			lsfMode := false
			count := 1000
			count2 := 100
			_, err := exec.LookPath("lsadmin")
			if err == nil {
				_, err = exec.LookPath("bqueues")
			}
			if err == nil {
				lsfMode = true
				count = 10000
				count2 = 1000
				lsfConfig := runningConfig
				lsfConfig.SchedulerName = "lsf"
				lsfConfig.SchedulerConfig = &jqs.ConfigLSF{Shell: config.RunnerExecShell, Deployment: "testing"}
				server.Stop(true)
				server, _, token, errs = Serve(lsfConfig)
				So(errs, ShouldBeNil)
			}

			clientConnectTime = 20 * time.Second // it takes a long time with -race to add 10000 jobs...
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			tmpdir, err := ioutil.TempDir(pwd, "wr_jobqueue_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			var jobs []*Job
			for i := 0; i < count; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>batch1.%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: "perl", Requirements: &jqs.Requirements{RAM: 300, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			}
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count)
			So(already, ShouldEqual, 0)

			// wait for 101 of them to complete
			done := make(chan bool, 1)
			fourHundredCount := 0
			go func() {
				limit := time.After(45 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateComplete, false, false)
						if err != nil {
							continue
						}
						ran := 0
						for _, job := range jobs {
							files, errf := ioutil.ReadDir(job.ActualCwd)
							if errf != nil {
								log.Fatalf("job [%s] had actual cwd %s: %s\n", job.Cmd, job.ActualCwd, errf)
							}
							for range files {
								ran++
							}
						}
						if ran > 100 {
							ticker.Stop()
							done <- true
							return
						} else if fourHundredCount == 0 {
							server.sgcmutex.Lock()
							if countg, existed := server.sgroupcounts["400:0:1:0"]; existed {
								fourHundredCount = countg
							}
							server.sgcmutex.Unlock()
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)
			So(fourHundredCount, ShouldBeBetweenOrEqual, count/2, count)

			// now add a new batch of jobs with the same reqs and reqgroup
			jobs = nil
			for i := 0; i < count2; i++ {
				jobs = append(jobs, &Job{Cmd: fmt.Sprintf("perl -e 'open($fh, q[>batch2.%d]); print $fh q[foo]; close($fh)'", i), Cwd: tmpdir, ReqGroup: "perl", Requirements: &jqs.Requirements{RAM: 300, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			}
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, count2)
			So(already, ShouldEqual, 0)

			// wait for all the jobs to get run
			done = make(chan bool, 1)
			twoHundredCount := 0
			go func() {
				limit := time.After(180 * time.Second)
				ticker := time.NewTicker(50 * time.Millisecond)
				for {
					select {
					case <-ticker.C:
						if twoHundredCount > 0 && !server.HasRunners() {
							// check they're really all complete, since the
							// switch to a new job array could leave us with no
							// runners temporarily
							jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateComplete, false, false)
							if err == nil && len(jobs) == count+count2 {
								ticker.Stop()
								done <- true
								return
							}
						} else if twoHundredCount == 0 {
							server.sgcmutex.Lock()
							if countg, existed := server.sgroupcounts["200:30:1:0"]; existed {
								twoHundredCount = countg
							}
							server.sgcmutex.Unlock()
						}
						continue
					case <-limit:
						ticker.Stop()
						done <- false
						return
					}
				}
			}()
			So(<-done, ShouldBeTrue)
			So(twoHundredCount, ShouldBeBetween, fourHundredCount/2, count+count2)

			jobs, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, count+count2)
			ran := 0
			for _, job := range jobs {
				files, err := ioutil.ReadDir(job.ActualCwd)
				if err != nil {
					continue
				}
				for range files {
					ran++
				}
			}
			So(ran, ShouldEqual, count+count2)

			if !lsfMode {
				// we should end up running maxCPU*2 runners, because the first set
				// will be for our given reqs, and the second set will be for when
				// the system learns actual memory usage
				files, err := ioutil.ReadDir(runnertmpdir)
				if err != nil {
					log.Fatal(err)
				}
				ranClean := 0
				for range files {
					ranClean++
				}
				So(ranClean, ShouldBeBetweenOrEqual, (maxCPU * 2), (maxCPU*2)+1) // *** not sure why it's sometimes 1 less than expected...
			} // *** else under LSF we want to test that we never request more than count+count2 runners...
		})

		Reset(func() {
			if server != nil {
				server.Stop(true)
			}
		})
	})

	if server != nil {
		server.Stop(true)
	}

	// start these tests anew because these tests have the server spawn runners
	// that fail, simulating some network issue
	Convey("Once a new jobqueue server is up with bad runners", t, func() {
		ServerItemTTR = 1 * time.Second
		ServerCheckRunnerTime = 2 * time.Second
		ClientTouchInterval = 50 * time.Millisecond
		pwd, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		runnertmpdir, err := ioutil.TempDir(pwd, "wr_jobqueue_test_runner_dir_")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(runnertmpdir)

		// our runnerCmd will be running ourselves in --runnermode, so first
		// we'll compile ourselves to the tmpdir
		runnerCmd := filepath.Join(runnertmpdir, "runner")
		cmd := exec.Command("go", "test", "-tags", "netgo", "-run", "TestJobqueue", "-c", "-o", runnerCmd)
		err = cmd.Run()
		if err != nil {
			log.Fatal(err)
		}

		runningConfig := serverConfig
		runningConfig.RunnerCmd = runnerCmd + " --runnermode --runnerfail --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s --rtimeout %d --maxmins %d --tmpdir " + runnertmpdir
		server, _, token, errs = Serve(runningConfig)
		So(errs, ShouldBeNil)

		Convey("You can connect, and add a job", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			tmpdir, err := ioutil.TempDir("", "wr_jobqueue_test_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "true", Cwd: tmpdir, ReqGroup: "true", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Second, Cores: 1}, Retries: uint8(0), Override: uint8(2), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			Convey("After some time the manager will have tried to spawn runners more than once", func() {
				runnerCheck := func() (runners int) {
					files, errf := ioutil.ReadDir(runnertmpdir)
					if errf != nil {
						log.Fatal(errf)
					}
					ranFailed := 0
					for _, file := range files {
						if !strings.HasPrefix(file.Name(), "fail") {
							continue
						}
						ranFailed++
					}
					return ranFailed
				}

				So(runnerCheck(), ShouldEqual, 0)

				hadRunner := make(chan bool, 1)
				go func() {
					limit := time.After(3 * time.Second)
					ticker := time.NewTicker(100 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							if server.HasRunners() {
								ticker.Stop()
								hadRunner <- true
								return
							}
							continue
						case <-limit:
							ticker.Stop()
							hadRunner <- false
							return
						}
					}
				}()
				So(<-hadRunner, ShouldBeTrue)
				<-time.After(1 * time.Second)

				jobs, err = jq.GetByRepGroup("manually_added", false, 0, JobStateReady, false, false)
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 1)

				So(runnerCheck(), ShouldEqual, 1)

				<-time.After(3 * time.Second)

				So(runnerCheck(), ShouldEqual, 2)

				server.Drain()
				<-time.After(4 * time.Second)
				So(server.HasRunners(), ShouldBeFalse)
			})
		})

		Reset(func() {
			if server != nil {
				server.Stop(true)
			}
		})
	})

	if server != nil {
		server.Stop(true)
	}
}

func TestJobqueueWithOpenStack(t *testing.T) {
	if runnermode {
		return
	}

	osPrefix := os.Getenv("OS_OS_PREFIX")
	osUser := os.Getenv("OS_OS_USERNAME")
	localUser := os.Getenv("OS_LOCAL_USERNAME")
	flavorRegex := os.Getenv("OS_FLAVOR_REGEX")

	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelay = 100 * time.Millisecond
	clientConnectTime := 10 * time.Second
	ServerItemTTR = 1 * time.Second
	ClientTouchInterval = 50 * time.Millisecond

	host, _ := os.Hostname()
	if strings.HasPrefix(host, "wr-development-"+localUser) && osPrefix != "" && osUser != "" && flavorRegex != "" {
		var server *Server
		var token []byte
		var errs error
		config := internal.ConfigLoad("development", true, testLogger)
		addr := "localhost:" + config.ManagerPort

		setDomainIP(config.ManagerCertDomain)

		runnertmpdir, err := ioutil.TempDir("", "wr_jobqueue_test_runner_dir_")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(runnertmpdir)

		// our runnerCmd will be running ourselves in --runnermode, so first
		// we'll compile ourselves to the tmpdir
		runnerCmd := filepath.Join(runnertmpdir, "runner")
		cmd := exec.Command("go", "test", "-tags", "netgo", "-run", "TestJobqueue", "-c", "-o", runnerCmd)
		err = cmd.Run()
		if err != nil {
			log.Fatal(err)
		}

		resourceName := "wr-testing-" + localUser
		cloudConfig := &jqs.ConfigOpenStack{
			ResourceName:         resourceName,
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
		cloudConfig.AddConfigFile(config.ManagerTokenFile + ":~/.wr_" + config.Deployment + "/client.token")
		if config.ManagerCAFile != "" {
			cloudConfig.AddConfigFile(config.ManagerCAFile + ":~/.wr_" + config.Deployment + "/ca.pem")
		}

		osConfig := ServerConfig{
			Port:            config.ManagerPort,
			WebPort:         config.ManagerWeb,
			SchedulerName:   "openstack",
			SchedulerConfig: cloudConfig,
			UploadDir:       config.ManagerUploadDir,
			DBFile:          config.ManagerDbFile,
			DBFileBackup:    config.ManagerDbBkFile,
			TokenFile:       config.ManagerTokenFile,
			CAFile:          config.ManagerCAFile,
			CertFile:        config.ManagerCertFile,
			CertDomain:      config.ManagerCertDomain,
			KeyFile:         config.ManagerKeyFile,
			Deployment:      config.Deployment,
			RunnerCmd:       runnerCmd + " --runnermode --schedgrp '%s' --rdeployment %s --rserver '%s' --rdomain %s --rtimeout %d --maxmins %d --tmpdir " + runnertmpdir,
			Logger:          testLogger,
		}

		dockerInstallScript := `sudo mkdir -p /etc/docker/
sudo bash -c "echo '{ \"bip\": \"192.168.3.3/24\", \"dns\": [\"8.8.8.8\",\"8.8.4.4\"], \"mtu\": 1380 }' > /etc/docker/daemon.json"
sudo apt-get -y install --no-install-recommends apt-transport-https ca-certificates curl software-properties-common
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add -
sudo add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable"
sudo apt-get -yq update
sudo apt-get -y install docker-ce
sudo usermod -aG docker ` + osUser

		Convey("You can connect with an OpenStack scheduler", t, func() {
			server, _, token, errs = Serve(osConfig)
			So(errs, ShouldBeNil)
			defer server.Stop(true)

			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			Convey("You can run cmds that start docker containers and get correct memory and cpu usage", func() {
				var jobs []*Job
				other := make(map[string]string)
				other["cloud_script"] = dockerInstallScript

				jobs = append(jobs, &Job{Cmd: "docker run sendu/usememory:v1", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "first_docker", MonitorDocker: "?"})

				dockerName := "jobqueue_test." + internal.RandomString()
				jobs = append(jobs, &Job{Cmd: "docker run --name " + dockerName + " sendu/usememory:v1", Cwd: "/tmp", ReqGroup: "docker", Requirements: &jqs.Requirements{RAM: 3, Time: 5 * time.Second, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "named_docker", MonitorDocker: dockerName})

				dockerCidFile := "jobqueue_test.cidfile"
				jobs = append(jobs, &Job{Cmd: "docker run --cidfile " + dockerCidFile + " sendu/usecpu:v1 && rm " + dockerCidFile, Cwd: "/tmp", ReqGroup: "docker2", Requirements: &jqs.Requirements{RAM: 1, Time: 5 * time.Second, Cores: 2, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "cidfile_docker", MonitorDocker: dockerCidFile})

				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 3)
				So(already, ShouldEqual, 0)

				// wait for the jobs to get run
				done := make(chan bool, 1)
				go func() {
					limit := time.After(180 * time.Second)
					ticker := time.NewTicker(1 * time.Second)
					for {
						select {
						case <-ticker.C:
							if !server.HasRunners() {
								got, errg := jq.GetIncomplete(0, "", false, false)
								if errg != nil {
									fmt.Printf("GetIncomplete failed: %s\n", errg)
								}
								if len(got) == 0 {
									ticker.Stop()
									done <- true
									return
								}
							}
							continue
						case <-limit:
							ticker.Stop()
							done <- false
							return
						}
					}
				}()
				So(<-done, ShouldBeTrue)

				expectedRAM := 2000
				got, err := jq.GetByRepGroup("first_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeGreaterThanOrEqualTo, expectedRAM)
				So(got[0].WallTime(), ShouldBeBetweenOrEqual, 5*time.Second, 15*time.Second)
				So(got[0].CPUtime, ShouldBeLessThan, 4*time.Second)

				got, err = jq.GetByRepGroup("named_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeGreaterThanOrEqualTo, expectedRAM)

				got, err = jq.GetByRepGroup("cidfile_docker", false, 0, JobStateComplete, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				So(got[0].PeakRAM, ShouldBeLessThan, 100)
				So(got[0].WallTime(), ShouldBeBetweenOrEqual, 5*time.Second, 15*time.Second)
				So(got[0].CPUtime, ShouldBeGreaterThan, 5*time.Second)

				// *** want to test that when we kill a running job, its docker
				// is also immediately killed...
			})

			Convey("You can run a cmd with a per-cmd set of config files", func() {
				// create a config file locally
				localConfigPath := filepath.Join(runnertmpdir, "test.config")
				configContent := []byte("myconfig\n")
				err := ioutil.WriteFile(localConfigPath, configContent, 0600)
				So(err, ShouldBeNil)

				// pretend the server is remote to us, and upload our config
				// file first
				remoteConfigPath, err := jq.UploadFile(localConfigPath, "")
				So(err, ShouldBeNil)
				So(remoteConfigPath, ShouldEqual, filepath.Join(os.Getenv("HOME"), ".wr_development", "uploads", "4", "2", "5", "a65424cddbee3271f937530c6efc6"))

				// check the remote config file was saved properly
				content, err := ioutil.ReadFile(remoteConfigPath)
				So(err, ShouldBeNil)
				So(content, ShouldResemble, configContent)

				// create a job that cats a config file that should only exist
				// if the supplied cloud_config_files option worked. It then
				// fails so we can check the stdout afterwards.
				var jobs []*Job
				other := make(map[string]string)
				configPath := "~/.wr_test.config"
				other["cloud_config_files"] = remoteConfigPath + ":" + configPath
				jobs = append(jobs, &Job{Cmd: "cat " + configPath + " && false", Cwd: "/tmp", ReqGroup: "cat", Requirements: &jqs.Requirements{RAM: 1, Time: 1 * time.Hour, Cores: 1, Other: other}, Override: uint8(2), Retries: uint8(0), RepGroup: "with_config_file"})
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 1)
				So(already, ShouldEqual, 0)

				// wait for the job to get run
				done := make(chan bool, 1)
				go func() {
					limit := time.After(180 * time.Second)
					ticker := time.NewTicker(1 * time.Second)
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
				So(<-done, ShouldBeTrue)

				got, err := jq.GetByRepGroup("with_config_file", false, 0, JobStateBuried, true, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 1)
				stderr, err := got[0].StdErr()
				So(err, ShouldBeNil)
				So(stderr, ShouldEqual, "")
				stdout, err := got[0].StdOut()
				So(err, ShouldBeNil)
				So(stdout, ShouldEqual, strings.TrimSuffix(string(configContent), "\n"))
			})

			Convey("You can run commands with different hardware requirements while dropping the count", func() {
				var jobs []*Job
				dropReq := &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 1, Disk: 0}
				jobs = append(jobs, &Job{Cmd: "sleep 1", Cwd: "/tmp", ReqGroup: "sleep", Requirements: dropReq, Retries: uint8(3), RepGroup: "manually_added"})
				jobs = append(jobs, &Job{Cmd: "echo 2", Cwd: "/tmp", ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 2048, Time: 1 * time.Hour, Cores: 1}, Override: uint8(2), Retries: uint8(3), RepGroup: "manually_added"})
				jobs = append(jobs, &Job{Cmd: "echo 3", Cwd: "/tmp", ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 2, Disk: 0}, Retries: uint8(3), RepGroup: "manually_added"})
				jobs = append(jobs, &Job{Cmd: "echo 4", Cwd: "/tmp", ReqGroup: "echo", Requirements: dropReq, Priority: uint8(255), Retries: uint8(3), RepGroup: "manually_added"})
				jobs = append(jobs, &Job{Cmd: "echo 5", Cwd: "/tmp", ReqGroup: "echo", Requirements: &jqs.Requirements{RAM: 1024, Time: 1 * time.Hour, Cores: 1, Disk: 20}, Retries: uint8(3), RepGroup: "manually_added"})
				count := 100
				for i := 6; i <= count; i++ {
					jobs = append(jobs, &Job{Cmd: fmt.Sprintf("echo %d", i), Cwd: "/tmp", ReqGroup: "sleep", Requirements: dropReq, Retries: uint8(3), RepGroup: "manually_added"})
				}
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, count)
				So(already, ShouldEqual, 0)

				// wait for the jobs to get run
				done := make(chan bool, 1)
				go func() {
					limit := time.After(180 * time.Second)
					ticker := time.NewTicker(1 * time.Second)
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
				So(<-done, ShouldBeTrue)
			})

			Convey("The manager reacts correctly to spawned servers going down", func() {
				p, err := cloud.New("openstack", resourceName, filepath.Join(runnertmpdir, "os_resources"))
				So(err, ShouldBeNil)

				// for this test to work, we need 1 job to run on another
				// server, so we need to use all the cores of this server per
				// job
				cores := runtime.NumCPU()

				flavor, err := p.CheapestServerFlavor(cores, 2048, flavorRegex)
				So(err, ShouldBeNil)

				destroyedBadServer := 0
				badServerCB := func(server *cloud.Server) {
					errf := server.Destroy()
					if errf == nil {
						destroyedBadServer++
					}
				}

				server.scheduler.SetBadServerCallBack(badServerCB)

				var jobs []*Job
				req := &jqs.Requirements{RAM: flavor.RAM, Time: 1 * time.Hour, Cores: cores, Disk: 0}
				schedGrp := fmt.Sprintf("%d:60:%d:0", flavor.RAM, cores)
				jobs = append(jobs, &Job{Cmd: "sleep 300", Cwd: "/tmp", ReqGroup: "sleep", Requirements: req, Retries: uint8(1), Override: uint8(2), RepGroup: "sleep"})
				jobs = append(jobs, &Job{Cmd: "sleep 301", Cwd: "/tmp", ReqGroup: "sleep", Requirements: req, Retries: uint8(1), Override: uint8(2), RepGroup: "sleep"})
				inserts, already, err := jq.Add(jobs, envVars, true)
				So(err, ShouldBeNil)
				So(inserts, ShouldEqual, 2)
				So(already, ShouldEqual, 0)

				// wait for the jobs to start running
				started := make(chan bool, 1)
				waitForBothRunning := func() {
					limit := time.After(180 * time.Second)
					ticker := time.NewTicker(1 * time.Second)
					for {
						select {
						case <-ticker.C:
							if server.HasRunners() {
								running, errf := jq.GetByRepGroup("sleep", false, 0, JobStateRunning, false, false)
								complete, errf := jq.GetByRepGroup("sleep", false, 0, JobStateComplete, false, false)
								if errf != nil {
									ticker.Stop()
									started <- false
									return
								}
								if len(running)+len(complete) == 2 {
									ticker.Stop()
									started <- true
									return
								}
							}
							continue
						case <-limit:
							ticker.Stop()
							started <- false
							return
						}
					}
				}
				go waitForBothRunning()
				So(<-started, ShouldBeTrue)

				// pretend a server went down by manually terminating one of
				// them, while monitoring that we never request more than 2
				// runners, and that we eventually spawn exactly 1 new server
				// to get the killed job running again
				got, err := jq.GetByRepGroup("sleep", false, 0, JobStateRunning, false, false)
				So(err, ShouldBeNil)
				So(len(got), ShouldEqual, 2)

				moreThan2 := make(chan bool, 1)
				stopChecking := make(chan bool, 1)
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							server.sgcmutex.Lock()
							if server.sgroupcounts[schedGrp] > 2 {
								ticker.Stop()
								moreThan2 <- true
								server.sgcmutex.Unlock()
								return
							}
							server.sgcmutex.Unlock()
						case <-stopChecking:
							ticker.Stop()
							moreThan2 <- false
							return
						}
					}
				}()

				destroyed := false
				var killedJobEssence *JobEssence
				for _, job := range got {
					if job.Host != host {
						So(job.HostID, ShouldNotBeBlank)
						So(job.HostIP, ShouldNotBeBlank)
						err = p.DestroyServer(job.HostID)
						So(err, ShouldBeNil)
						destroyed = true
						killedJobEssence = &JobEssence{JobKey: job.Key()}
						break
					}
				}
				So(destroyed, ShouldBeTrue)

				// wait for the killed job to be marked as lost and then release
				// it
				gotLost := make(chan bool, 1)
				go func() {
					limit := time.After(20 * time.Second)
					ticker := time.NewTicker(10 * time.Millisecond)
					for {
						select {
						case <-ticker.C:
							job, err := jq.GetByEssence(killedJobEssence, false, false)
							if err != nil {
								ticker.Stop()
								gotLost <- false
								return
							}
							if job.State == JobStateLost {
								ticker.Stop()
								e, err := server.killJob(killedJobEssence.JobKey)
								if !e || err != nil {
									gotLost <- false
								}
								gotLost <- true
								return
							}
						case <-limit:
							ticker.Stop()
							gotLost <- false
							return
						}
					}
				}()
				So(<-gotLost, ShouldBeTrue)

				// wait until they both start running again
				go waitForBothRunning()
				So(<-started, ShouldBeTrue)
				stopChecking <- true
				So(<-moreThan2, ShouldBeFalse)
				So(destroyedBadServer, ShouldEqual, 1)
			})

			Reset(func() {
				if server != nil {
					server.Stop(true)
				}
			})
		})
	} else {
		SkipConvey("Skipping the OpenStack tests", t, func() {})
	}
}

func TestJobqueueWithMounts(t *testing.T) {
	if runnermode {
		return
	}

	if runtime.NumCPU() == 1 {
		// we lock up with only 1 proc
		runtime.GOMAXPROCS(2)
	}

	// for these tests to work, JOBQUEUE_REMOTES3_PATH must be the bucket name
	// and path to an S3 directory set up as per TestS3RemoteIntegration in
	// github.com/VertebrateResequencing/muxfys/s3_test.go. You must also have
	// an ~/.s3cfg file with a default section containing your s3 configuration.

	s3Path := os.Getenv("JOBQUEUE_REMOTES3_PATH")
	_, s3cfgErr := os.Stat(filepath.Join(os.Getenv("HOME"), ".s3cfg"))

	if s3Path == "" || s3cfgErr != nil {
		SkipConvey("Without the JOBQUEUE_REMOTES3_PATH environment variable and an ~/.s3cfg file, we'll skip jobqueue S3 tests", t, func() {})
		return
	}

	ServerInterruptTime = 10 * time.Millisecond
	ServerReserveTicker = 10 * time.Millisecond
	ClientReleaseDelay = 100 * time.Millisecond
	clientConnectTime := 10 * time.Second
	ServerItemTTR = 10 * time.Second
	ClientTouchInterval = 50 * time.Millisecond

	config := internal.ConfigLoad("development", true, testLogger)
	addr := "localhost:" + config.ManagerPort
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDbFile,
		DBFileBackup:    config.ManagerDbBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
		Logger:          testLogger,
	}

	Convey("You can bring up a server configured with an S3 db backup", t, func() {
		s3ServerConfig := serverConfig
		s3ServerConfig.DBFileBackup = fmt.Sprintf("s3://default@%s/db.bk", s3Path)
		localBkPath := filepath.Join(filepath.Dir(config.ManagerDbFile), ".db_bk_mount", s3Path, "db.bk.development")
		os.Remove(config.ManagerDbFile)
		forceBackups = true
		defer func() {
			forceBackups = false
		}()
		server, _, token, errs := Serve(s3ServerConfig)
		So(errs, ShouldBeNil)

		defer func() {
			// stop the server
			server.Stop(true)

			// and delete the db.bk file in s3, which means we need to mount the
			// thing ourselves
			accessorConfig, err := muxfys.S3ConfigFromEnvironment("default", s3Path)
			if err != nil {
				return
			}
			accessor, err := muxfys.NewS3Accessor(accessorConfig)
			if err != nil {
				return
			}
			remoteConfig := &muxfys.RemoteConfig{
				Accessor: accessor,
				Write:    true,
			}
			cfg := &muxfys.Config{
				Mount:   filepath.Dir(localBkPath),
				Retries: 10,
			}
			fs, err := muxfys.New(cfg)
			if err != nil {
				return
			}
			err = fs.Mount(remoteConfig)
			if err != nil {
				return
			}
			fs.UnmountOnDeath()
			os.Remove(localBkPath)
			fs.Unmount()
		}()

		_, err := os.Stat(config.ManagerDbFile)
		So(err, ShouldBeNil)
		_, err = os.Stat(localBkPath)
		So(err, ShouldNotBeNil)

		Convey("You can connect and add a job, which creates a db backup", func() {
			jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)
			defer jq.Disconnect()

			var jobs []*Job
			jobs = append(jobs, &Job{Cmd: "echo 1", Cwd: "/tmp", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 10, Time: 1 * time.Second, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			<-time.After(4 * time.Second)

			info, err := os.Stat(config.ManagerDbFile)
			So(err, ShouldBeNil)
			So(info.Size(), ShouldEqual, 32768)
			info2, err := os.Stat(localBkPath)
			So(err, ShouldBeNil)
			So(info2.Size(), ShouldEqual, 28672)

			Convey("You can stop the server, delete the database, and it will be restored from S3 backup", func() {
				jobsByRepGroup, err := jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 1)

				server.Stop(true)
				wipeDevDBOnInit = false
				defer func() {
					wipeDevDBOnInit = true
				}()
				server, _, token, errs = Serve(s3ServerConfig)
				So(errs, ShouldBeNil)
				defer server.Stop(true)
				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 1)

				server.Stop(true)
				os.Remove(config.ManagerDbFile)
				_, err = os.Stat(config.ManagerDbFile)
				So(err, ShouldNotBeNil)
				_, err = os.Stat(localBkPath)
				So(err, ShouldNotBeNil)

				server, _, token, errs = Serve(s3ServerConfig)
				So(errs, ShouldBeNil)
				defer server.Stop(true)

				info, err = os.Stat(config.ManagerDbFile)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldEqual, 28672)
				info2, err = os.Stat(localBkPath)
				So(err, ShouldBeNil)
				So(info2.Size(), ShouldEqual, 28672)

				jq, err = Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				So(err, ShouldBeNil)

				jobsByRepGroup, err = jq.GetByRepGroup("manually_added", false, 0, "", false, false)
				So(err, ShouldBeNil)
				So(len(jobsByRepGroup), ShouldEqual, 1)
			})
		})
	})

	Convey("You can connect and run commands that rely on files in a remote S3 object store", t, func() {
		// pwd, err := os.Getwd()
		// if err != nil {
		// 	log.Fatal(err)
		// }
		cwd, err := ioutil.TempDir("", "wr_jobqueue_test_s3_dir_")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(cwd)

		server, _, token, err := Serve(serverConfig)
		So(err, ShouldBeNil)

		standardReqs := &jqs.Requirements{RAM: 10, Time: 10 * time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)
		defer jq.Disconnect()

		var jobs []*Job
		mcs := MountConfigs{
			{Targets: []MountTarget{
				{Path: s3Path, Cache: true},
				{Path: s3Path + "/sub/deep", Cache: true},
			}, Verbose: true},
		}
		b := &Behaviour{When: OnExit, Do: CleanupAll}
		bs := Behaviours{b}

		Convey("Commands can read remote data and the cache gets deleted afterwards", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt && cat bar", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "s3", MountConfigs: mcs, Behaviours: bs})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 0)

			job, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, "s3")

			// muxfys.SetLogHandler(log15.StderrHandler)
			jeerr := jq.Execute(job, config.RunnerExecShell)
			So(jeerr, ShouldBeNil)

			job, err = jq.GetByEssence(&JobEssence{Cmd: "cat numalphanum.txt && cat bar"}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldBeNil)

			job, err = jq.GetByEssence(&JobEssence{Cmd: "cat numalphanum.txt && cat bar", MountConfigs: mcs}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.State, ShouldEqual, JobStateComplete)

			// test that the cache dirs get deleted; this test fails if cwd is based
			// in an nfs mount, the problem confirmed upstream in muxfys but with no
			// solution apparent...
			So(job.ActualCwd, ShouldNotBeEmpty)
			_, err = os.Stat(job.ActualCwd)
			So(err, ShouldNotBeNil)
			So(os.IsNotExist(err), ShouldBeTrue)
			f, err := os.Open(job.Cwd)
			So(err, ShouldBeNil)
			defer f.Close()
			_, err = f.Readdirnames(100)
			So(err, ShouldEqual, io.EOF) // ie. the whole created working dir got wiped
		})

		t1 := MountTarget{Path: s3Path + "/sub", Cache: true}
		t2 := MountTarget{Path: s3Path, Cache: true}
		Convey("You can add identical commands with different mounts", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "a", MountConfigs: mcs, Behaviours: bs})

			mcs2 := MountConfigs{
				{Targets: []MountTarget{t1}, Verbose: true},
			}

			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "b", MountConfigs: mcs2, Behaviours: bs})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			mcs3 := MountConfigs{
				{Targets: []MountTarget{t1, t2}},
			}
			mcs4 := MountConfigs{
				{Targets: []MountTarget{t2, t1}},
			}

			jobs = nil
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "c", MountConfigs: mcs3})
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "d", MountConfigs: mcs4})
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			job, err := jq.GetByEssence(&JobEssence{Cmd: "cat numalphanum.txt", MountConfigs: mcs3}, false, false)
			So(err, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, "c")
		})

		Convey("You can't add identical commands with the same mounts", func() {
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "a", MountConfigs: mcs, Behaviours: bs})
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "b", MountConfigs: mcs})
			inserts, already, err := jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 1)

			mcs5 := MountConfigs{
				{Targets: []MountTarget{t1}, Verbose: true, Mount: "a"},
				{Targets: []MountTarget{t2}, Verbose: true, Mount: "b"},
			}
			mcs6 := MountConfigs{
				{Targets: []MountTarget{t2}, Verbose: true, Mount: "b"},
				{Targets: []MountTarget{t1}, Verbose: true, Mount: "a"},
			}

			jobs = nil
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "c", MountConfigs: mcs5})
			jobs = append(jobs, &Job{Cmd: "cat numalphanum.txt", Cwd: cwd, ReqGroup: "cat", Requirements: standardReqs, RepGroup: "d", MountConfigs: mcs6})
			inserts, already, err = jq.Add(jobs, envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)
			So(already, ShouldEqual, 1)
		})

		Reset(func() {
			if server != nil {
				server.Stop(true)
			}
		})
	})
}

func TestJobqueueSpeed(t *testing.T) {
	if runnermode {
		return
	}

	config := internal.ConfigLoad("development", true, testLogger)
	serverConfig := ServerConfig{
		Port:            config.ManagerPort,
		WebPort:         config.ManagerWeb,
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: config.RunnerExecShell},
		DBFile:          config.ManagerDbFile,
		DBFileBackup:    config.ManagerDbBkFile,
		TokenFile:       config.ManagerTokenFile,
		CAFile:          config.ManagerCAFile,
		CertFile:        config.ManagerCertFile,
		CertDomain:      config.ManagerCertDomain,
		KeyFile:         config.ManagerKeyFile,
		Deployment:      config.Deployment,
		Logger:          testLogger,
	}
	addr := "localhost:" + config.ManagerPort

	// some manual speed tests (don't like the way the benchmarking feature
	// works)
	if false {
		runtime.GOMAXPROCS(runtime.NumCPU())
		n := 50000

		server, _, token, err := Serve(serverConfig)
		if err != nil {
			log.Fatal(err)
		}

		clientConnectTime := 10 * time.Second
		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		if err != nil {
			log.Fatal(err)
		}
		defer jq.Disconnect()

		before := time.Now()
		var jobs []*Job
		for i := 0; i < n; i++ {
			jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i), Cwd: "/fake/cwd", ReqGroup: "fake_group", Requirements: &jqs.Requirements{RAM: 1024, Time: 4 * time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: "manually_added"})
		}
		inserts, already, err := jq.Add(jobs, envVars, true)
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
				start := time.After(time.Until(beginat))
				gjq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
				if err != nil {
					log.Fatal(err)
				}
				defer gjq.Disconnect()
				<-start
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

		server.Stop(true)
	}

	/* test speed of bolt db when there are lots of jobs already stored
				if true {
					n := 10000000 // num jobs to start with
					b := 10000    // jobs per identifier

					server, _, err := Serve(serverConfig)
					if err != nil {
						log.Fatal(err)
					}

					jq, err := Connect(addr, "cmds", 60*time.Second)
					if err != nil {
						log.Fatal(err)
					}
		            defer jq.Disconnect()

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
	                        jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i+((batchNum-1)*b)), Cwd: "/fake/cwd", ReqGroup: "reqgroup", Requirements: &jqs.Requirements{RAM: 1024, Time: 4*time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: fmt.Sprintf("batch_%d", batchNum)})
						}
						_, _, err := jq.Add(jobs, envVars, true)
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
						// 	jq.Started(job, 123)
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
								key := job.key()
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
        jobs = append(jobs, &Job{Cmd: fmt.Sprintf("test cmd %d", i+((batchNum-1)*b)), Cwd: "/fake/cwd", ReqGroup: "reqgroup", Requirements: &jqs.Requirements{RAM: 1024, Time: 4*time.Hour, Cores: 1}, Retries: uint8(3), RepGroup: batchName})
	}
	_, _, err := jq.Add(jobs, envVars, true)
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
            defer gojq.Disconnect()
			for {
				job, _ := gojq.Reserve(1 * time.Millisecond)
				if job == nil {
					break
				}
				gojq.Started(job, 123)
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
	jobs, err = jq.GetByRepGroup(batchName, 1, JobStateComplete, false, false) // without a limit this takes longer than 60s, so would time out
	if err != nil {
		log.Fatal(err)
	}
	e = time.Since(before)
	log.Printf("Was able to get all %d jobs in that batch in %s\n", 1+jobs[0].Similar, e)
}
*/

func runner() {
	if runnerfail {
		// simulate loss of network connectivity between a spawned runner and
		// the manager by just exiting without reserving any job
		<-time.After(250 * time.Millisecond)
		tmpfile, _ := ioutil.TempFile(runnermodetmpdir, "fail")
		tmpfile.Close()
		return
	}

	ServerItemTTR = 10 * time.Second
	ClientTouchInterval = 50 * time.Millisecond

	// uncomment and fill out log path to debug "exit status 1" outputs when
	// running the test:
	// logfile, errlog := ioutil.TempFile("", "wrrunnerlog")
	// if errlog == nil {
	// 	defer logfile.Close()
	// 	log.SetOutput(logfile)
	// }

	if schedgrp == "" {
		log.Fatal("schedgrp missing")
	}

	config := internal.ConfigLoad(rdeployment, true, testLogger)

	token, err := ioutil.ReadFile(config.ManagerTokenFile)
	if err != nil {
		log.Fatalf("token read err: %s\n", err)
	}

	timeout := 6 * time.Second
	rtimeoutd := time.Duration(rtimeout) * time.Second
	// (we don't bother doing anything with maxmins in this test, but in a real
	//  runner client it would be used to end the below for loop before hitting
	//  this limit)

	jq, err := Connect(rserver, config.ManagerCAFile, rdomain, token, timeout)

	if err != nil {
		log.Fatalf("connect err: %s\n", err)
	}
	defer jq.Disconnect()

	clean := true
	for {
		job, err := jq.ReserveScheduled(rtimeoutd, schedgrp)
		if err != nil {
			log.Fatalf("reserve err: %s\n", err)
		}
		if job == nil {
			// log.Printf("reserve gave no job after %s\n", rtimeoutd)
			break
		}
		// log.Printf("working on job %s\n", job.Cmd)

		// actually run the cmd
		err = jq.Execute(job, config.RunnerExecShell)
		if err != nil {
			if jqerr, ok := err.(Error); ok && jqerr.Err == FailReasonSignal {
				break
			} else {
				log.Printf("execute err: %s\n", err) // make this a Fatalf if you want to see these in the test output
				clean = false
				break
			}
		} else {
			jq.Archive(job, nil)
		}
	}

	// if everything ran cleanly, create a tmpfile in our tmp dir
	if clean {
		tmpfile, _ := ioutil.TempFile(runnermodetmpdir, "ok")
		tmpfile.Close()
	}
}

// setDomainIP is an author-only func to ensure that domain points to localhost
func setDomainIP(domain string) {
	host, _ := os.Hostname()
	if host == "vr-2-2-02" {
		ip, _ := internal.CurrentIP("")
		internal.InfobloxSetDomainIP(domain, ip)
	}
}
