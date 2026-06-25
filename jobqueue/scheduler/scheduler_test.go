/*******************************************************************************
 * Copyright (c) 2016-2021, 2024-2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: [Theo Barber-Bany] <theobarberbany@gmail.com>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Rosie Kern
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

//nolint:forbidigo,gochecknoglobals,lll // Legacy scheduler integration tests use diagnostic prints and shared CPU sizing.
package scheduler

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
)

const devHost = "farm22-hgi01"

var maxCPU = runtime.NumCPU()

func TestMock(t *testing.T) {
	ctx := context.Background()

	Convey("You can get a new mock scheduler with a runner function", t, func() {
		runnerFunc := func(context.Context, string) {}
		s, err := New(ctx, mockSchedulerName, ConfigMock{RunnerFunc: runnerFunc})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		Convey("It rejects configs that cannot run mock runners", func() {
			badConfigs := []any{
				nil,
				&ConfigLocal{},
				(*ConfigMock)(nil),
				ConfigMock{},
			}

			for _, badConfig := range badConfigs {
				_, err = New(ctx, mockSchedulerName, badConfig)
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "SchedulerConfig")
			}
		})
	})
}

type startOrderRecorder struct {
	dir        string
	lock       string
	order      string
	releaseAll string
}

func newStartOrderRecorder() (*startOrderRecorder, error) {
	dir, err := os.MkdirTemp("", "wr_schedulers_local_test_order_dir_")
	if err != nil {
		return nil, err
	}

	return &startOrderRecorder{
		dir:        dir,
		lock:       filepath.Join(dir, "lock"),
		order:      filepath.Join(dir, "order"),
		releaseAll: filepath.Join(dir, "release_all"),
	}, nil
}

func (r *startOrderRecorder) Close() {
	os.RemoveAll(r.dir)
}

// Command returns a shell command for label that, when run, creates a unique
// "running" marker in tmpdir, records label in the start-order file, then
// blocks until either its own marker is deleted (releaseOne) or the release-all
// file appears (releaseAllJobs). Because a running job holds its marker, the
// test sees which jobs run concurrently by counting markers and releases them
// one at a time; it never depends on the real-time order in which dispatched
// processes happen to reach this code, which is unreliable under heavy load.
func (r *startOrderRecorder) Command(label, tmpdir string) string {
	return fmt.Sprintf("marker=$(mktemp --tmpdir=%s run.XXXXXX); "+
		"trap 'rm -f \"$marker\"' EXIT; "+
		"while ! mkdir %s 2>/dev/null; do sleep 0.001; done; echo %s >> %s; rmdir %s; "+
		"while [ -e \"$marker\" ] && [ ! -e %s ]; do sleep 0.02; done",
		tmpdir, r.lock, label, r.order, r.lock, r.releaseAll)
}

// running returns how many instances of a label are currently running (holding
// a marker) in tmpdir.
func (r *startOrderRecorder) running(tmpdir string) int {
	entries, err := os.ReadDir(tmpdir)
	if err != nil {
		return 0
	}

	return len(entries)
}

// waitForRunning waits for tmpdir to hold exactly n running markers, returning
// true as soon as it does and false on timeout.
func (r *startOrderRecorder) waitForRunning(tmpdir string, n int) bool {
	return pollUntil(func() bool { return r.running(tmpdir) == n })
}

// releaseOne lets one running instance in tmpdir finish, by deleting one of its
// markers.
func (r *startOrderRecorder) releaseOne(tmpdir string) {
	entries, err := os.ReadDir(tmpdir)
	if err != nil || len(entries) == 0 {
		return
	}

	os.Remove(filepath.Join(tmpdir, entries[0].Name()))
}

// releaseAllJobs lets every still-running instance finish.
func (r *startOrderRecorder) releaseAllJobs() {
	if err := os.WriteFile(r.releaseAll, []byte{}, 0600); err != nil {
		log.Fatal(err)
	}
}

// started returns how many times label has started (cumulative, even if it has
// since been released).
func (r *startOrderRecorder) started(label string) int {
	data, err := os.ReadFile(r.order)
	if err != nil {
		return 0
	}

	n := 0

	for _, field := range strings.Fields(string(data)) {
		if field == label {
			n++
		}
	}

	return n
}

// pollUntil polls cond every 20ms for up to 30s, returning true as soon as cond
// returns true and false on timeout.
func pollUntil(cond func() bool) bool {
	return pollUntilFor(30*time.Second, 20*time.Millisecond, cond)
}

// pollUntilFor polls cond at interval for up to maxWait, returning true as soon
// as cond returns true and false on timeout.
func pollUntilFor(maxWait, interval time.Duration, cond func() bool) bool {
	limit := time.After(maxWait)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if cond() {
			return true
		}

		select {
		case <-limit:
			return false
		case <-ticker.C:
		}
	}
}

func TestStartOrderRecorder(t *testing.T) {
	Convey("Start-order commands remove their marker after release-all", t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		order, err := newStartOrderRecorder()
		So(err, ShouldBeNil)

		if err != nil {
			return
		}

		defer order.Close()

		tmpdir := t.TempDir()
		cmd := exec.CommandContext(ctx, "bash", "-c", order.Command("job", tmpdir)) //nolint:gosec
		err = cmd.Start()
		So(err, ShouldBeNil)

		if err != nil {
			return
		}

		So(order.waitForRunning(tmpdir, 1), ShouldBeTrue)

		order.releaseAllJobs()

		err = cmd.Wait()
		So(err, ShouldBeNil)
		So(ctx.Err(), ShouldBeNil)
		So(order.running(tmpdir), ShouldEqual, 0)
	})
}

func TestLocal(t *testing.T) {
	ctx := context.Background()
	runtime.GOMAXPROCS(maxCPU)

	Convey("You can get a new local scheduler", t, func() {
		otherReqs := make(map[string]string)

		s, err := New(ctx, "local", &ConfigLocal{"bash", 1 * time.Second, 0, 0})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		possibleReq := &Requirements{1, 1 * time.Second, 1, 20, otherReqs, true, true, true}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, 20, otherReqs, true, true, true}

		Convey("Debug log contains context based on scheduler type", func() {
			ctx = s.typeContext(ctx)
			buff := clog.ToBufferAtLevel("debug")
			clog.Debug(ctx, "msg", "foo", 1)
			So(buff.String(), ShouldContainSubstring, "schedulertype=local")
		})

		Convey("ReserveTimeout() returns 1 second", func() {
			So(s.ReserveTimeout(ctx, possibleReq), ShouldEqual, 1)

			Convey("It can log error with scheduler type context for wrong timeout reqs", func() {
				buff := clog.ToBufferAtLevel("error")
				otherRTReqs := make(map[string]string)
				otherRTReqs["rtimeout"] = "foo"
				_ = s.ReserveTimeout(ctx, &Requirements{Other: otherRTReqs})
				So(buff.String(), ShouldContainSubstring, "schedulertype=local")
			})
		})

		Convey("MaxQueueTime() returns req time plus 1m", func() {
			So(s.MaxQueueTime(possibleReq).Seconds(), ShouldEqual, 61)
		})

		Convey("Busy() starts off false", func() {
			So(s.Busy(ctx), ShouldBeFalse)
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
			err := s.Schedule(ctx, "foo", impossibleReq, 0, 1)
			So(err, ShouldNotBeNil)
			serr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(serr.Err, ShouldEqual, ErrImpossible)
		})

		Convey("Given a running command", func() {
			testProcessNotRunning(ctx, s, possibleReq)
		})

		Convey("Schedule() lets you schedule more jobs than localhost CPUs", func() {
			tmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_immediate_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir)
			tmpdir2, err := os.MkdirTemp("", "wr_schedulers_local_test_end_output_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(tmpdir2)

			defer waitToFinish(ctx, s, 120, 100)

			cmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75); @a = tempfile(DIR => q[%s]); exit(0);'", tmpdir, tmpdir2) // creates a file, sleeps for 0.75s and then creates another file

			// different machines take different amounts of times to actually
			// run the above command, so we first need to run the command (in
			// parallel still, since it is slower to run when many are running
			// at once) to find how long it takes, as subsequent tests are very
			// timing dependent
			count := maxCPU * 2
			sched := func() {
				serr := s.Schedule(ctx, cmd, possibleReq, 0, count)
				So(serr, ShouldBeNil)
				So(s.Busy(ctx), ShouldBeTrue)
				scheduled, serr := s.Scheduled(ctx, cmd)
				So(serr, ShouldBeNil)
				So(scheduled, ShouldEqual, count)
			}

			// each cmd creates a file in tmpdir when it starts and another in tmpdir2
			// when it finishes, so started-minus-finished is how many are running right
			// now. We poll these instead of sleeping for fixed (load-sensitive)
			// durations and checking exact counts at a fixed moment.
			started := func() int { return testDirForFiles(tmpdir, 0) }
			finished := func() int { return testDirForFiles(tmpdir2, 0) }

			Convey("It eventually runs them all, at most maxCPU at a time", func() {
				sched()

				maxConcurrent := 0

				So(pollUntil(func() bool {
					if r := started() - finished(); r > maxConcurrent {
						maxConcurrent = r
					}

					return finished() == count
				}), ShouldBeTrue)

				So(maxConcurrent, ShouldEqual, maxCPU)
				So(started(), ShouldEqual, count)
				// Busy lags the last finish-marker (the scheduler still has to
				// reap the exited job), so poll for idle rather than asserting it.
				So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
			})

			Convey("Dropping the count below the number currently running doesn't kill those that are running", func() {
				sched()
				So(pollUntil(func() bool { return started() >= maxCPU }), ShouldBeTrue)
				So(started(), ShouldEqual, maxCPU)

				newcount := maxCPU - 1
				So(s.Schedule(ctx, cmd, possibleReq, 0, newcount), ShouldBeNil)

				So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
				So(started(), ShouldEqual, maxCPU)
				So(finished(), ShouldEqual, maxCPU)
			})

			Convey("You can Schedule() again to increase the count", func() {
				sched()
				So(pollUntil(func() bool { return started() >= maxCPU }), ShouldBeTrue)
				So(started(), ShouldEqual, maxCPU)

				newcount := count + 1
				So(s.Schedule(ctx, cmd, possibleReq, 0, newcount), ShouldBeNil)

				So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
				So(started(), ShouldEqual, newcount)
				So(finished(), ShouldEqual, newcount)
			})

			if maxCPU > 1 {
				Convey("You can Schedule() again to drop the count", func() {
					sched()
					So(pollUntil(func() bool { return started() >= maxCPU }), ShouldBeTrue)
					So(started(), ShouldEqual, maxCPU)

					newcount := maxCPU + 1
					So(s.Schedule(ctx, cmd, possibleReq, 0, newcount), ShouldBeNil)

					So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
					So(started(), ShouldEqual, newcount)
					So(finished(), ShouldEqual, newcount)
				})

				Convey("You can Schedule() a new job and have it run while the first is still running", func() {
					sched()
					So(pollUntil(func() bool { return started() >= maxCPU }), ShouldBeTrue)
					So(started(), ShouldEqual, maxCPU)

					newcount := maxCPU + 1
					So(s.Schedule(ctx, cmd, possibleReq, 0, newcount), ShouldBeNil)
					newcmd := fmt.Sprintf("perl -MFile::Temp=tempfile -e '@b = tempfile(DIR => q[%s]); select(undef, undef, undef, 0.75);'", tmpdir)
					So(s.Schedule(ctx, newcmd, possibleReq, 0, 1), ShouldBeNil)

					So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
					So(started(), ShouldEqual, newcount+1)
					So(finished(), ShouldEqual, newcount)
				})
			} else {
				SkipConvey("Skipping Schedule() tests that need more than 1 cpu", func() {})
			}
		})

		if maxCPU > 2 {
			Convey("Schedule() bin-packs a small cmd alongside a big one, deferring the second big", func() {
				smallTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_small_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(smallTmpdir)

				bigTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_big_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(bigTmpdir)

				order, err := newStartOrderRecorder()
				if err != nil {
					log.Fatal(err)
				}

				defer order.Close()

				smallReq := &Requirements{1, 1 * time.Second, 1, 0, otherReqs, true, true, true}
				bigReq := &Requirements{1, 1 * time.Second, float64(maxCPU - 1), 0, otherReqs, true, true, true}

				// 2 big cmds (each needs all-but-one core) and 1 small (1 core). Bin
				// packing must run the first big and the small together (filling the
				// machine), so the second big has to wait. We assert on what is running
				// concurrently, not on the (load-sensitive) order processes happen to start.
				So(s.Schedule(ctx, order.Command("big", bigTmpdir), bigReq, 0, 2), ShouldBeNil)
				So(s.Schedule(ctx, order.Command("small", smallTmpdir), smallReq, 0, 1), ShouldBeNil)

				So(order.waitForRunning(smallTmpdir, 1), ShouldBeTrue)
				So(order.waitForRunning(bigTmpdir, 1), ShouldBeTrue)
				So(order.running(bigTmpdir), ShouldEqual, 1)
				So(order.running(smallTmpdir), ShouldEqual, 1)

				// release the first big; the second big now has room and runs
				order.releaseOne(bigTmpdir)
				So(pollUntil(func() bool { return order.started("big") == 2 }), ShouldBeTrue)

				order.releaseAllJobs()
				So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
				So(order.started("big"), ShouldEqual, 2)
				So(order.started("small"), ShouldEqual, 1)
			})

			Convey("The biggest scheduled cmd runs first when the machine frees up", func() {
				smallTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_small_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(smallTmpdir)

				bigTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_big_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(bigTmpdir)

				order, err := newStartOrderRecorder()
				if err != nil {
					log.Fatal(err)
				}

				defer order.Close()

				blockTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_block_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(blockTmpdir)

				allReq := &Requirements{1, 1 * time.Second, float64(maxCPU), 0, otherReqs, true, true, true}
				smallReq := &Requirements{1, 1 * time.Second, 1, 0, otherReqs, true, true, true}

				// a blocker fills the whole machine, then 2 small (1 core) and 1 big (all
				// cores) are queued behind it; when the machine frees, the biggest cmd
				// must take priority and run before the smalls.
				So(s.Schedule(ctx, order.Command("block", blockTmpdir), allReq, 0, 1), ShouldBeNil)
				So(order.waitForRunning(blockTmpdir, 1), ShouldBeTrue)
				So(s.Schedule(ctx, order.Command("small", smallTmpdir), smallReq, 0, 2), ShouldBeNil)
				So(s.Schedule(ctx, order.Command("big", bigTmpdir), allReq, 0, 1), ShouldBeNil)

				So(order.running(smallTmpdir), ShouldEqual, 0)
				So(order.running(bigTmpdir), ShouldEqual, 0)

				order.releaseOne(blockTmpdir)
				So(order.waitForRunning(bigTmpdir, 1), ShouldBeTrue)
				So(order.running(smallTmpdir), ShouldEqual, 0)
				So(order.started("small"), ShouldEqual, 0)

				order.releaseAllJobs()
				So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
				So(order.started("big"), ShouldEqual, 1)
				So(order.started("small"), ShouldEqual, 2)
			})

			Convey("Priority overrides bin-packing for smaller cmds", func() {
				smallTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_small_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(smallTmpdir)

				bigTmpdir, err := os.MkdirTemp("", "wr_schedulers_local_test_big_output_dir_")
				if err != nil {
					log.Fatal(err)
				}
				defer os.RemoveAll(bigTmpdir)

				order, err := newStartOrderRecorder()
				if err != nil {
					log.Fatal(err)
				}

				defer order.Close()

				smallReq := &Requirements{1, 1 * time.Second, 1, 0, otherReqs, true, true, true}
				bigReq := &Requirements{1, 1 * time.Second, float64(maxCPU) / 2, 0, otherReqs, true, true, true}

				// 3 big cmds at priority 0 (two fill the machine) and 1 small at higher
				// priority 1. When a slot frees, the higher-priority small must take it
				// before the third big does.
				So(s.Schedule(ctx, order.Command("big", bigTmpdir), bigReq, 0, 3), ShouldBeNil)
				So(s.Schedule(ctx, order.Command("small", smallTmpdir), smallReq, 1, 1), ShouldBeNil)

				So(order.waitForRunning(bigTmpdir, 2), ShouldBeTrue)
				So(order.running(smallTmpdir), ShouldEqual, 0)

				order.releaseOne(bigTmpdir)
				So(order.waitForRunning(smallTmpdir, 1), ShouldBeTrue)
				So(order.running(bigTmpdir), ShouldEqual, 1)
				// The higher-priority small now holds the freed slot (asserted
				// above), so the third big cannot have started. Poll for the
				// start-order file to show exactly the two bigs that ran: the
				// marker that makes a job "running" is created slightly before
				// it appends to that file, so a bare read here can momentarily
				// see only one big under load.
				So(pollUntil(func() bool { return order.started("big") == 2 }), ShouldBeTrue)

				order.releaseAllJobs()
				So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
				So(order.started("big"), ShouldEqual, 3)
				So(order.started("small"), ShouldEqual, 1)
			})
		}

		// wait a while for any remaining jobs to finish
		So(waitToFinish(ctx, s, 120, 100), ShouldBeTrue)
	})
	if maxCPU > 1 {
		Convey("You can get a new local scheduler that uses less than all CPUs", t, func() {
			otherReqs := make(map[string]string)

			s, err := New(ctx, "local", &ConfigLocal{"bash", 1 * time.Second, 1, 0})
			So(err, ShouldBeNil)
			So(s, ShouldNotBeNil)

			tmpDir, err := os.MkdirTemp("", "wr_schedulers_local_test_slee[_output_dir_")
			if err != nil {
				log.Fatal(err)
			}

			cmd := fmt.Sprintf("mktemp --tmpdir=%s tmp.XXXXXX && sleep 0.5", tmpDir)
			sleepReq := &Requirements{1, 1 * time.Second, 1, 0, otherReqs, true, true, true}

			err = s.Schedule(ctx, cmd, sleepReq, 0, 2)
			So(err, ShouldBeNil)

			for {
				if !s.Busy(ctx) {
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

func TestOpenstack(t *testing.T) {
	ctx := context.Background()
	// check if we have our special openstack-related variable
	osPrefix := os.Getenv("OS_OS_PREFIX")
	osUser := os.Getenv("OS_OS_USERNAME")
	localUser := os.Getenv("OS_LOCAL_USERNAME")
	flavorRegex := os.Getenv("OS_FLAVOR_REGEX")
	rName := "wr-testing-" + localUser
	keepTime := 5 * time.Second
	config := &ConfigOpenStack{
		ResourceName:              rName,
		OSPrefix:                  osPrefix,
		OSUser:                    osUser,
		OSRAM:                     2048,
		FlavorRegex:               flavorRegex,
		FlavorSets:                os.Getenv("OS_FLAVOR_SETS"),
		ServerPorts:               []int{22},
		ServerKeepTime:            keepTime,
		StateUpdateFrequency:      1 * time.Second,
		Shell:                     "bash",
		MaxInstances:              -1,
		SimultaneousSpawns:        1,
		PostCreationScript:        []byte("#!/bin/bash\necho b > /tmp/a"),
		PostCreationForcedCommand: "echo bar > /tmp/foo",
		PreDestroyScript:          []byte("#!/bin/bash\n[ -d /shared/ ] && cat /tmp/a > /shared/test4 || true"),
	}
	if osPrefix == "" || osUser == "" || localUser == "" {
		Convey("You can't get a new openstack scheduler without the required environment variables", t, func() {
			_, err := New(ctx, "openstack", config)
			So(err, ShouldNotBeNil)
		})
		return
	}
	if flavorRegex == "" {
		SkipConvey("OpenStack scheduler tests are skipped without special OS_FLAVOR_REGEX environment variable being set", t, func() {})
		return
	}
	host, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}

	var novaCmd string
	if _, errl := exec.LookPath("openstack"); errl == nil {
		novaCmd = "openstack server"
	} else if _, errl := exec.LookPath("nova"); errl == nil {
		novaCmd = "nova"
	}

	Convey("You can get a new openstack scheduler", t, func() {
		otherReqs := make(map[string]string)

		tmpdir, errt := os.MkdirTemp("", "wr_schedulers_openstack_test_output_dir_")
		if errt != nil {
			log.Fatal(errt)
		}
		defer os.RemoveAll(tmpdir)
		config.SavePath = filepath.Join(tmpdir, "os_resources")
		s, errn := New(ctx, "openstack", config)
		So(errn, ShouldBeNil)
		So(s, ShouldNotBeNil)
		defer s.Cleanup(ctx)
		oss := s.impl.(*opst)

		possibleReq := &Requirements{100, 1 * time.Minute, 1, 1, otherReqs, true, true, true}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, 20, otherReqs, true, true, true}

		Convey("ReserveTimeout() returns 25 seconds", func() {
			So(s.ReserveTimeout(ctx, possibleReq), ShouldEqual, 1)
		})

		// author specific tests, based on hostname, where we know what the
		// expected server types are
		if host == devHost {
			Convey("determineFlavor() picks the best server flavor depending on given resource requirements", func() {
				flavor, err := oss.determineFlavor(ctx, possibleReq, "a")
				So(err, ShouldBeNil)

				So(flavor.ID, ShouldEqual, "2100")
				So(flavor.RAM, ShouldEqual, 11190)
				So(flavor.Disk, ShouldEqual, 26)
				So(flavor.Cores, ShouldEqual, 1)

				flavor, err = oss.determineFlavor(ctx, &Requirements{
					100, 1 * time.Minute, 1, 30, otherReqs,
					true, true, true,
				}, "l")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2100")

				flavor, err = oss.determineFlavor(ctx, oss.reqForSpawn(possibleReq), "m")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2100")

				flavor, err = oss.determineFlavor(ctx, &Requirements{
					100, 1 * time.Minute, 2, 1, otherReqs,
					true, true, true,
				}, "n")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2101")
				So(flavor.RAM, ShouldEqual, 23800)
				So(flavor.Disk, ShouldEqual, 53)
				So(flavor.Cores, ShouldEqual, 2)

				flavor, err = oss.determineFlavor(ctx, &Requirements{
					30000, 1 * time.Minute, 1, 1, otherReqs,
					true, true, true,
				}, "o")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2102")
				So(flavor.RAM, ShouldEqual, 47600)
				So(flavor.Disk, ShouldEqual, 106)
				So(flavor.Cores, ShouldEqual, 4)

				flavor, err = oss.determineFlavor(ctx, &Requirements{
					64000, 1 * time.Minute, 1, 1, otherReqs,
					true, true, true,
				}, "p")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2103")
				So(flavor.RAM, ShouldEqual, 95200)
				So(flavor.Disk, ShouldEqual, 213)
				So(flavor.Cores, ShouldEqual, 8)

				flavor, err = oss.determineFlavor(ctx, &Requirements{100, 1 * time.Minute, 3, 1, otherReqs, true, true, true}, "r")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2102")

				flavor, err = oss.determineFlavor(ctx, &Requirements{100, 1 * time.Minute, 5, 1, otherReqs, true, true, true}, "s")
				So(err, ShouldBeNil)
				So(flavor.ID, ShouldEqual, "2103")
			})

			Convey("MaxQueueTime() always returns enough time to complete 1 job, plus a minute leeway", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 2)
				So(s.MaxQueueTime(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs, true, true, true}).Minutes(), ShouldEqual, 781)
			})
		}

		Convey("Busy() starts off false", func() {
			So(s.Busy(ctx), ShouldBeFalse)
		})

		Convey("Schedule() gives impossible error when given impossible reqs", func() {
			err := s.Schedule(ctx, "foo", impossibleReq, 0, 1)
			So(err, ShouldNotBeNil)
			serr, ok := err.(Error)
			So(ok, ShouldBeTrue)
			So(serr.Err, ShouldEqual, ErrImpossible)
		})

		if os.Getenv("OS_TENANT_ID") == "" {
			Convey("Schedule() gives impossible error when reqs don't fit in the requested flavor", func() {
				flavor, err := oss.determineFlavor(ctx, possibleReq, "a")
				So(err, ShouldBeNil)
				other := make(map[string]string)
				other["cloud_flavor"] = flavor.Name
				brokenReq := &Requirements{flavor.RAM + 1, 1 * time.Minute, 1, 1, other, true, true, true}
				err = s.Schedule(ctx, "foo", brokenReq, 0, 1)
				So(err, ShouldNotBeNil)
				serr, ok := err.(Error)
				So(ok, ShouldBeTrue)
				So(serr.Err, ShouldEqual, ErrImpossible)
			})
		}

		// we need to not actually run the real scheduling tests if we're not
		// running in openstack, because the scheduler will try to ssh to
		// the servers it spawns
		if novaCmd != "" && oss.provider.InCloud() {
			oFile := filepath.Join(tmpdir, "out")

			Convey("Schedule() lets you...", func() {
				Convey("Run lots of jobs on a deathrow server", func() {
					count := 10
					eta := 200
					oReqs := make(map[string]string)
					oReqs["cloud_script"] = "touch /tmp/foo" // force a server to be spawned
					thisReq := &Requirements{1, 1 * time.Minute, 0, 0, oReqs, true, true, true}
					err := s.Schedule(ctx, "echo first", thisReq, 0, 1)
					So(err, ShouldBeNil)
					So(s.Busy(ctx), ShouldBeTrue)

					// spawn a server, run the first job, get on deathrow
					So(waitToFinish(ctx, s, eta, 1000), ShouldBeTrue)

					// now Schedule a bunch of cmds in quick succession
					var wg sync.WaitGroup
					for i := 0; i < count; i++ {
						wg.Add(1)
						go func(i int) {
							defer wg.Done()
							s.Schedule(ctx, fmt.Sprintf("echo %d", i), thisReq, 0, count)
						}(i)
					}
					wg.Wait()

					// the test is that we don't hit a deadlock
					So(waitToFinish(ctx, s, eta, 1000), ShouldBeTrue)
				})

				Convey("Run jobs that use a NFS shared disk and rely on the PostCreationScript, ForcedCommand having run, and the PreDestroyScript runs on scale down", func() {
					cmd := "touch /shared/test1"
					other := make(map[string]string)
					other["cloud_shared"] = "true"
					localReq := &Requirements{100, 1 * time.Minute, 1, 1, other, true, true, true}
					err := s.Schedule(ctx, cmd, localReq, 0, 1)
					So(err, ShouldBeNil)

					remoteReq := oss.reqForSpawn(localReq)
					for _, server := range oss.servers {
						if server.Flavor.RAM >= remoteReq.RAM {
							remoteReq.RAM = server.Flavor.RAM + 1000
						}
					}
					remoteReq.Other = other
					cmd = "cat /tmp/foo > /shared/test2; cat /tmp/a > /shared/test3"
					err = s.Schedule(ctx, cmd, remoteReq, 0, 1)
					So(err, ShouldBeNil)

					So(s.Busy(ctx), ShouldBeTrue)
					So(waitToFinish(ctx, s, 240, 1000), ShouldBeTrue)

					_, err = os.Stat("/shared/test1")
					So(err, ShouldBeNil)

					content, err := os.ReadFile("/shared/test2")
					So(err, ShouldBeNil)
					So(string(content), ShouldEqual, "bar\n")

					content, err = os.ReadFile("/shared/test3")
					So(err, ShouldBeNil)
					So(string(content), ShouldEqual, "b\n")

					<-time.After(keepTime)
					content, err = os.ReadFile("/shared/test4")
					So(err, ShouldBeNil)
					So(string(content), ShouldEqual, "b\n")

					err = os.Remove("/shared/test1")
					So(err, ShouldBeNil)
					err = os.Remove("/shared/test2")
					So(err, ShouldBeNil)
					err = os.Remove("/shared/test3")
					So(err, ShouldBeNil)
					err = os.Remove("/shared/test4")
					So(err, ShouldBeNil)
				})

				if flavorRegex == `^[mso].*$` && os.Getenv("OS_TENANT_ID") == "" {
					Convey("Run a job on a specific flavor", func() {
						cmd := "sleep 10"
						other := make(map[string]string)
						other["cloud_flavor"] = "o2.small"
						thisReq := &Requirements{100, 1 * time.Minute, 1, 1, other, true, true, true}
						err := s.Schedule(ctx, cmd, thisReq, 0, 1)
						So(err, ShouldBeNil)
						So(s.Busy(ctx), ShouldBeTrue)

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

						So(waitToFinish(ctx, s, 120, 1000), ShouldBeTrue)
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
					// get up to 3 instances and then kill an un-needed 4th
					// prior to cleaning up *** would be good to test hitting
					// the quota as well, but that takes too long and is
					// unreliable
					count := 18
					eta := 200
					cmd := "sleep 10"
					oReqs := make(map[string]string)
					thisReq := &Requirements{100, 1 * time.Minute, 16, 1, oReqs, true, true, true}
					err := s.Schedule(ctx, cmd, thisReq, 0, count)
					So(err, ShouldBeNil)
					So(s.Busy(ctx), ShouldBeTrue)

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

					So(waitToFinish(ctx, s, eta, 1000), ShouldBeTrue)
					stopCh <- true
					spawned := <-spawnedCh
					close(spawnedCh)
					So(spawned, ShouldBeBetweenOrEqual, 2, count)

					foundServers := novaCountServers(novaCmd, rName, "")
					So(foundServers, ShouldBeBetweenOrEqual, 1, eta/10) // (assuming a ~10s spawn time)

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
					newReq := &Requirements{100, 1 * time.Minute, 1, 1, oReqs, true, true, true}
					newCount := 3
					eta := 120
					cmd := "sleep 10"
					err := s.Schedule(ctx, cmd, newReq, 0, newCount)
					So(err, ShouldBeNil)
					So(s.Busy(ctx), ShouldBeTrue)
					So(waitToFinish(ctx, s, eta, 1000), ShouldBeTrue)
				})

				Convey("Run jobs and have servers still self-terminate when a server is slow to spawn", func() {
					debugCounter = 0
					debugEffect = "slowSecondSpawn"
					oReqs := make(map[string]string)
					newReq := &Requirements{100, 1 * time.Minute, 1, 1, oReqs, true, true, true}
					newCount := 3
					eta := 120
					cmd := "sleep 10"
					err := s.Schedule(ctx, cmd, newReq, 0, newCount)
					So(err, ShouldBeNil)
					So(s.Busy(ctx), ShouldBeTrue)
					So(waitToFinish(ctx, s, eta, 1000), ShouldBeTrue)

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
						newReq := &Requirements{100, 1 * time.Minute, 1, 1, oReqs, true, true, true}
						newCount := 3
						eta := 120
						cmd := "sleep 10 && (echo override > " + oFile + ") || true"
						err := s.Schedule(ctx, cmd, newReq, 0, newCount)
						So(err, ShouldBeNil)
						So(s.Busy(ctx), ShouldBeTrue)

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

						So(waitToFinish(ctx, s, eta, 1000), ShouldBeTrue)
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
				multiCoreFlavor, err := oss.determineFlavor(ctx, &Requirements{
					1024, 1 * time.Minute, float64(numCores), 0, oReqsm,
					true, true, true,
				}, "u")
				if err == nil && multiCoreFlavor.Cores >= numCores {
					oReqs := make(map[string]string)
					oReqs["cloud_os_ram"] = strconv.Itoa(multiCoreFlavor.RAM)
					jobReq := &Requirements{multiCoreFlavor.RAM / numCores, 1 * time.Minute, 1, 0, oReqs, true, true, true}
					confirmFlavor, err := oss.determineFlavor(ctx, oss.reqForSpawn(jobReq), "v")
					if err == nil && confirmFlavor.Cores >= numCores {
						Convey("Run multiple jobs at once on multi-core servers", func() {
							cmd := "sleep 30"
							jobReq := &Requirements{multiCoreFlavor.RAM / numCores, 1 * time.Minute, 1, 0, oReqs, true, true, true}
							err = s.Schedule(ctx, cmd, jobReq, 0, numCores)
							So(err, ShouldBeNil)
							So(s.Busy(ctx), ShouldBeTrue)

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
							So(waitToFinish(ctx, s, waitSecs, 1000), ShouldBeTrue)
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
			So(waitToFinish(ctx, s, 60, 1000), ShouldBeTrue)
		} else {
			SkipConvey("Actual OpenStack scheduling tests are skipped if not in OpenStack with nova or openstack installed", func() {})
		}
	})

	if novaCmd != "" {
		Convey("You can get a new openstack scheduler that can do multiple spawns", t, func() {
			tmpdir, errt := os.MkdirTemp("", "wr_schedulers_openstack_test_output_dir_")
			if errt != nil {
				log.Fatal(errt)
			}
			defer os.RemoveAll(tmpdir)
			config.SavePath = filepath.Join(tmpdir, "os_resources")
			config.SimultaneousSpawns = 5
			s, errn := New(ctx, "openstack", config)
			So(errn, ShouldBeNil)
			So(s, ShouldNotBeNil)
			defer func() {
				s.Cleanup(ctx)
			}()
			oss := s.impl.(*opst)

			if oss.provider.InCloud() {
				ignoreServers := make(map[string]bool)
				oss.serversMutex.RLock()
				for _, server := range oss.servers {
					ignoreServers[server.ID] = true
				}
				oss.serversMutex.RUnlock()

				getServerFlavors := func() map[int]int {
					oss.serversMutex.RLock()
					defer oss.serversMutex.RUnlock()
					flavors := make(map[int]int)
					for _, server := range oss.servers {
						if ignoreServers[server.ID] {
							continue
						}
						flavors[server.Flavor.Cores]++
					}
					return flavors
				}

				waitForServers := func(wanted map[int]int) bool {
					limit := time.After(120 * time.Second)
					ticker := time.NewTicker(1 * time.Second)
					for {
						select {
						case <-ticker.C:
							if len(wanted) == 0 {
								oss.stateUpdate(ctx)
							}
							have := getServerFlavors()
							ok := true
							for cpus, desired := range wanted {
								if actual, exists := have[cpus]; exists {
									if actual < desired {
										ok = false
										// fmt.Printf("only %d not %d for flavor %d\n", actual, desired, cpus)
										break
									}
								} else {
									ok = false
									// fmt.Printf("missing flavor %d\n", cpus)
									break
								}
							}
							for cpus := range have {
								if _, exists := wanted[cpus]; !exists {
									ok = false
									// fmt.Printf("extra flavor %d\n", cpus)
									break
								}
							}

							if ok {
								ticker.Stop()
								<-time.After(2 * time.Second)
								return true
							}
							continue
						case <-limit:
							ticker.Stop()
							return false
						}
					}
				}

				other := make(map[string]string)
				other["cloud_script"] = "echo forced new servers"

				Convey("You can Schedule many cmds and a bunch run right away", func() {
					smallCmd := "sleep 30"
					smallReq := &Requirements{100, 1 * time.Minute, 2, 1, other, true, true, true}
					err := s.Schedule(ctx, smallCmd, smallReq, 0, config.SimultaneousSpawns*2)
					So(err, ShouldBeNil)

					wanted := make(map[int]int)
					wanted[2] = config.SimultaneousSpawns
					So(waitForServers(wanted), ShouldBeTrue)

					err = s.Schedule(ctx, smallCmd, smallReq, 0, 0)
					So(err, ShouldBeNil)

					wanted = make(map[int]int)
					So(waitForServers(wanted), ShouldBeTrue)
				})

				Convey("You can Schedule many small cmds and then a higher priority large cmd and the large runs asap", func() {
					smallCmd := "sleep 60"
					smallReq := &Requirements{100, 1 * time.Minute, 2, 1, other, true, true, true}
					err := s.Schedule(ctx, smallCmd, smallReq, 0, config.SimultaneousSpawns*3)
					So(err, ShouldBeNil)

					bigCmd := "sleep 2"
					bigReq := &Requirements{100, 1 * time.Minute, 4, 1, other, true, true, true}
					err = s.Schedule(ctx, bigCmd, bigReq, 1, 1)
					So(err, ShouldBeNil)

					wanted := make(map[int]int)
					wanted[2] = (config.SimultaneousSpawns * 2) - 1
					wanted[4] = 1
					So(waitForServers(wanted), ShouldBeTrue)

					err = s.Schedule(ctx, smallCmd, smallReq, 0, 0)
					So(err, ShouldBeNil)
					err = s.Schedule(ctx, bigCmd, bigReq, 0, 0)
					So(err, ShouldBeNil)

					wanted = make(map[int]int)
					So(waitForServers(wanted), ShouldBeTrue)
				})

				Convey("You can Schedule a large command and then a small cmd and get both running and sharing servers", func() {
					bigCmd := "sleep 15"
					bigReq := &Requirements{100, 1 * time.Minute, 6, 1, other, true, true, true}
					err := s.Schedule(ctx, bigCmd, bigReq, 0, config.SimultaneousSpawns-1)
					So(err, ShouldBeNil)

					smallCmd := "sleep 16"
					smallReq := &Requirements{100, 1 * time.Minute, 2, 1, other, true, true, true}
					err = s.Schedule(ctx, smallCmd, smallReq, 0, config.SimultaneousSpawns)
					So(err, ShouldBeNil)

					wanted := make(map[int]int)
					wanted[8] = config.SimultaneousSpawns - 1
					wanted[2] = 1
					So(waitForServers(wanted), ShouldBeTrue)

					oss.serversMutex.RLock()
					eightcores := 0
					twocores := 0
					space := 0
					for _, server := range oss.servers {
						if server.Flavor.Cores == 8 {
							eightcores++
							thisSpace := server.HasSpaceFor(2, 1, 1)
							space += thisSpace
						} else {
							twocores++
						}
					}
					oss.serversMutex.RUnlock()

					err = s.Schedule(ctx, smallCmd, smallReq, 0, 0)
					So(err, ShouldBeNil)
					err = s.Schedule(ctx, bigCmd, bigReq, 0, 0)
					So(err, ShouldBeNil)

					wanted = make(map[int]int)
					So(waitForServers(wanted), ShouldBeTrue)

					So(eightcores, ShouldEqual, config.SimultaneousSpawns-1)
					So(space, ShouldEqual, 0)
					So(twocores, ShouldBeBetweenOrEqual, 1, config.SimultaneousSpawns)
				})
			}
		})
	}
}

func getInfoOfFilesInDir(tmpdir string, expected int) []fs.DirEntry {
	files, err := os.ReadDir(tmpdir)
	if err != nil {
		log.Fatal(err)
	}
	if len(files) < expected {
		// wait a little longer for things to sync up, by running ls
		cmd := exec.Command("ls", tmpdir)
		err = cmd.Run()
		if err != nil {
			log.Fatal(err)
		}
		files, err = os.ReadDir(tmpdir)
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
	times := make([]time.Time, 0, len(files))
	for _, entry := range files {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		times = append(times, info.ModTime())
		os.Remove(filepath.Join(tmpdir, info.Name()))
	}
	return times
}

func waitToFinish(ctx context.Context, s *Scheduler, maxS int, interval int) bool {
	done := make(chan bool, 1)
	go func() {
		limit := time.After(time.Duration(maxS) * time.Second)
		ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
		for {
			select {
			case <-ticker.C:
				if !s.Busy(ctx) {
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

func testProcessNotRunning(ctx context.Context, s *Scheduler, r *Requirements) {
	tmpdir, err := os.MkdirTemp("./", "wr_schedulers_test_output_dir_")
	So(err, ShouldBeNil)
	defer os.RemoveAll(tmpdir)

	pidHostFile, err := filepath.Abs(path.Join(tmpdir, "pid.host"))
	So(err, ShouldBeNil)
	pidHostFileTmp := pidHostFile + ".tmp"

	cmd := fmt.Sprintf("perl -e '$tmp = shift; $path = shift; open($fh, q[>], $tmp); print $fh qq[$$\n]; use Sys::Hostname qw(hostname); print $fh hostname(), qq[\n]; close($fh); rename $tmp, $path; for (1..120) { sleep(1) }' %s %s", pidHostFileTmp, pidHostFile)

	err = s.Schedule(ctx, cmd, r, 0, 1)
	So(err, ShouldBeNil)
	So(s.Busy(ctx), ShouldBeTrue)

	pid, host, worked := parsePidHostFile(pidHostFile, processStartTimeout(s))
	So(worked, ShouldBeTrue)

	Convey("ProcessNotRunngingOnHost() returns false if its still running", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		So(s.ProcessNotRunningOnHost(ctx, pid, host), ShouldBeFalse)

		Convey("But true if we kill it", func() {
			server, exists := s.impl.getHost(host)
			So(exists, ShouldBeTrue)
			So(server, ShouldNotBeNil)

			_, _, err := server.RunCmd(context.Background(), fmt.Sprintf("kill -9 %d", pid), false)
			So(err, ShouldBeNil)

			So(waitForProcessNotRunningOnHost(context.Background(), s, pid, host), ShouldBeTrue)
		})
	})
}

func processStartTimeout(s *Scheduler) time.Duration {
	if _, ok := s.impl.(*lsf); ok {
		return 120 * time.Second
	}

	return 13 * time.Second
}

func waitForProcessNotRunningOnHost(ctx context.Context, s *Scheduler, pid int, host string) bool {
	return pollUntilFor(30*time.Second, 250*time.Millisecond, func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		notRunning := s.ProcessNotRunningOnHost(probeCtx, pid, host)

		cancel()

		return notRunning
	})
}

func parsePidHostFile(path string, maxWait time.Duration) (int, string, bool) {
	dir := filepath.Dir(path)
	limit := time.After(maxWait)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// read the dir because on NFS we never see the file as existing
			// until the dir is read
			_, err := os.ReadDir(dir)
			if err != nil {
				fmt.Printf("error reading directory %s: %s\n", dir, err)

				return 0, "", false
			}

			_, err = os.Stat(path)
			if os.IsNotExist(err) {
				continue
			}

			content, err := os.ReadFile(path)
			if err != nil {
				fmt.Printf("%s couldn't be read: %s\n", path, err)

				return 0, "", false
			}

			split := strings.Split(strings.TrimSpace(string(content)), "\n")
			if len(split) < 2 {
				continue
			}

			pid, err := strconv.Atoi(split[0])
			if err != nil {
				continue
			}

			host := strings.TrimSpace(split[1])
			if host == "" {
				continue
			}

			return pid, host, true
		case <-limit:
			return 0, "", false
		}
	}
}
