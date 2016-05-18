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

package jobqueue

import (
	"fmt"
	"github.com/sb10/vrpipe/internal"
	. "github.com/smartystreets/goconvey/convey"
	"log"
	"math"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

var clientConnectTime = 150 * time.Millisecond

func TestJobqueue(t *testing.T) {
	runtime.GOMAXPROCS(runtime.NumCPU())

	// load our config to know where our development manager port is supposed to
	// be; we'll use that to test jobqueue
	config := internal.ConfigLoad("development", true)
	port := config.Manager_port
	addr := "localhost:" + port

	ServerLogClientErrors = false
	ServerInterruptTime = 10 * time.Millisecond // Stop() followed by Block() won't take 1s anymore
	ServerReserveTicker = 10 * time.Millisecond
	ServerItemTTR = 1 * time.Second
	ClientReleaseDelay = 100 * time.Millisecond

	Convey("Without the jobserver being up, clients can't connect and time out", t, func() {
		_, err := Connect(addr, "test_queue", clientConnectTime)
		So(err, ShouldNotBeNil)
		jqerr, ok := err.(Error)
		So(ok, ShouldBeTrue)
		So(jqerr.Err, ShouldEqual, ErrNoServer)
	})

	Convey("Once the jobqueue server is up", t, func() {
		server, _, err := Serve(port, config.Manager_db_file, config.Manager_db_bk_file, config.Deployment)
		So(err, ShouldBeNil)

		Convey("You can connect to the server and add jobs to the queue", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			sstats, err := jq.ServerStats()
			So(err, ShouldBeNil)
			So(sstats.ServerInfo.Port, ShouldEqual, port)
			So(sstats.ServerInfo.PID, ShouldBeGreaterThan, 0)

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

				jobs, err = jq.GetByRepGroup("manually_added")
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 10)

				jobs, err = jq.GetByRepGroup("foo")
				So(err, ShouldBeNil)
				So(len(jobs), ShouldEqual, 0)
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
						job, err := jq.ReserveScheduled(50*time.Millisecond, "2048.3600.2")
						So(err, ShouldBeNil)
						So(job, ShouldNotBeNil)
						So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", i))
					}
					job, err := jq.ReserveScheduled(50*time.Millisecond, "2048.3600.2")
					So(err, ShouldBeNil)
					So(job, ShouldBeNil)

					for i := 9; i >= 0; i-- {
						jid := i
						if i == 7 {
							jid = 4
						} else if i == 4 {
							jid = 7
						}
						job, err := jq.ReserveScheduled(50*time.Millisecond, "1024.14400.1")
						So(err, ShouldBeNil)
						So(job.Cmd, ShouldEqual, fmt.Sprintf("test cmd %d", jid))
					}
					job, err = jq.ReserveScheduled(50*time.Millisecond, "1024.14400.1")
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

				server, _, err = Serve(port, config.Manager_db_file, config.Manager_db_bk_file, config.Deployment)
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
			server.Block()
		})
	})

	// start these tests anew because I don't want to mess with the timings in
	// the above tests
	Convey("Once a new jobqueue server is up", t, func() {
		ServerItemTTR = 100 * time.Millisecond
		ClientTouchInterval = 50 * time.Millisecond
		server, _, err := Serve(port, config.Manager_db_file, config.Manager_db_bk_file, config.Deployment)
		So(err, ShouldBeNil)

		Convey("You can connect, and add some real jobs", func() {
			jq, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)
			jq2, err := Connect(addr, "test_queue", clientConnectTime)
			So(err, ShouldBeNil)

			var jobs []*Job
			jobs = append(jobs, NewJob("sleep 0.1 && true", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_pass"))
			jobs = append(jobs, NewJob("sleep 0.1 && false", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_fail"))
			inserts, already, err := jq.Add(jobs)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 2)
			So(already, ShouldEqual, 0)

			Convey("You can't execute a job without reserving it", func() {
				err := jq.Execute(jobs[0], config.Runner_exec_shell)
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

				err = jq.Execute(job, config.Runner_exec_shell)
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

				err = jq.Execute(job, config.Runner_exec_shell)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldEqual, "command [sleep 0.1 && false] exited with code 1, which may be a temporary issue, so it will be tried again")
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
						err = jq.Execute(job, config.Runner_exec_shell)
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

						err = jq.Execute(job, config.Runner_exec_shell)
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

							kicked, err := jq.Kick([][2]string{[2]string{"sleep 0.1 && false", "/tmp"}})
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

					deleted, err := jq.Delete([][2]string{[2]string{added.Cmd, added.Cwd}})
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

					deleted, err = jq.Delete([][2]string{[2]string{added.Cmd, added.Cwd}})
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

					deleted, err = jq.Delete([][2]string{[2]string{added.Cmd, added.Cwd}})
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

					err = jq.Execute(job, config.Runner_exec_shell)
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

					err = jq.Execute(job, config.Runner_exec_shell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command [sleep 0.1 && true | false | true] exited with code 1, which may be a temporary issue, so it will be tried again")
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

					err = jq.Execute(job, config.Runner_exec_shell)
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

					err = jq.Execute(job, config.Runner_exec_shell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, "complete")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)
					stdout, err := job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "print\n")
					stderr, err := job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "warn\n")

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

					err = jq.Execute(job, config.Runner_exec_shell)
					So(err, ShouldNotBeNil)
					So(job.State, ShouldEqual, "delayed")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 255)
					So(job.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "print\n")
					stderr, err = job.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "die\n")

					job2, err = jq2.GetByCmd("perl -e 'print qq[print\\n]; die qq[die\\n]'", "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					So(job2.State, ShouldEqual, "delayed")
					So(job2.FailReason, ShouldEqual, FailReasonExit)
					stdout, err = job2.StdOut()
					So(err, ShouldBeNil)
					So(stdout, ShouldEqual, "print\n")
					stderr, err = job2.StdErr()
					So(err, ShouldBeNil)
					So(stderr, ShouldEqual, "die\n")
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
					expectederr += "Died at -e line 1.\n"

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -e 'for (1..60) { print $_ x 130, qq[p\\n]; warn $_ x 130, qq[w\\n] } die'")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.Runner_exec_shell)
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
					jobs = append(jobs, NewJob("perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'", "/tmp", "fake_group", 10, 10*time.Second, 1, uint8(0), uint8(0), "should_pass"))
					inserts, _, err := jq.Add(jobs)
					So(err, ShouldBeNil)
					So(inserts, ShouldEqual, 1)

					job, err := jq.Reserve(50 * time.Millisecond)
					So(err, ShouldBeNil)
					So(job.Cmd, ShouldEqual, "perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.Runner_exec_shell)
					So(err, ShouldBeNil)
					So(job.State, ShouldEqual, "complete")
					So(job.Exited, ShouldBeTrue)
					So(job.Exitcode, ShouldEqual, 0)

					job2, err := jq2.GetByCmd("perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'", "/tmp", true, false)
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
					So(job.Cmd, ShouldEqual, "perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'")
					So(job.State, ShouldEqual, "reserved")

					err = jq.Execute(job, config.Runner_exec_shell)
					So(err, ShouldNotBeNil)
					So(err.Error(), ShouldEqual, "command [perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'] was running fine, but will need to be rerun due to a jobqueue server error")
					// because Execute() just returns in this situation, job.* won't be updated,

					job2, err = jq2.GetByCmd("perl -e '@a; for (1..3) { push(@a, q[a] x 50000000); sleep(1) }'", "/tmp", true, false)
					So(err, ShouldBeNil)
					So(job2, ShouldNotBeNil)
					// in this situation, the job got auto-released to ready
					// with no indication of a problem stored on the server
					So(job2.State, ShouldEqual, "ready")
					So(job2.Exited, ShouldBeFalse)
				})
			})
		})

		Reset(func() {
			server.Stop()
			server.Block()
		})
	})
}

func TestJobqueueSpeed(t *testing.T) {
	// some manual speed tests (don't like the way the benchmarking feature
	// works)
	if false {
		config := internal.ConfigLoad("development", true)
		port := config.Manager_port
		addr := "localhost:" + port
		runtime.GOMAXPROCS(runtime.NumCPU())
		n := 50000

		// bs, err := beanstalk.Connect("localhost:11300", "vrpipe.des", true)
		// if err != nil {
		// 	log.Fatal(err)
		// }

		// before := time.Now()
		// for i := 0; i < n; i++ {
		// 	_, err := bs.Add(fmt.Sprintf("test job %d", i), 30)
		// 	if err != nil {
		// 		log.Fatal(err)
		// 	}
		// }
		// e := time.Since(before)
		// per := int64(e.Nanoseconds() / int64(n))
		// log.Printf("Added %d beanstalk jobs in %s == %d per\n", n, e, per)

		server, _, err := Serve(port, config.Manager_db_file, config.Manager_db_bk_file, config.Deployment)
		if err != nil {
			log.Fatal(err)
		}

		clientConnectTime = 10 * time.Second
		jq, err := Connect(addr, "vrpipe.des", clientConnectTime)
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
				gjq, err := Connect(addr, "vrpipe.des", clientConnectTime)
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
}
