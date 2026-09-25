/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
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

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	batchCheckTimeout = 5 * time.Second
	fakeShellPid      = 7777
)

func TestProcessCheckCommandForcedCommandContract(t *testing.T) {
	Convey("The documented forced commands read the process check as a ps of its first pid", t, func() {
		for _, pids := range [][]int{{424242}, {424242, 424243}} {
			cmd := processCheckCommand(pids)

			for _, parser := range []string{psOnlyForcedCommandParser, updatedForcedCommandParser} {
				got, err := runForcedCommandParser(parser, cmd)
				So(err, ShouldBeNil)
				So(got, ShouldEqual, "PS 424242")
			}
		}
	})
}

func TestProcessesNotRunningWhenPsFails(t *testing.T) {
	Convey("A live process is not confirmed dead when the host's ps exits 1 with no output", t, func() {
		// a ps that rejects its options (procps exits 1 on a bad -o field, as
		// busybox and other ps variants may for what wr asks) prints nothing
		bin := t.TempDir()
		So(os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o700), ShouldBeNil) //nolint:gosec
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		ctx := context.Background()
		s, err := New(ctx, "local", &ConfigLocal{testShell, time.Second, 0, 0})
		So(err, ShouldBeNil)

		defer s.Cleanup(ctx)

		live := exec.CommandContext(ctx, "sleep", "30")
		So(live.Start(), ShouldBeNil)

		defer func() {
			So(live.Process.Kill(), ShouldBeNil)
			So(live.Wait(), ShouldNotBeNil)
		}()

		pid := live.Process.Pid

		Convey("checked alone", func() {
			So(s.ProcessesNotRunningOnHost(ctx, "localhost", []int{pid}, batchCheckTimeout)[pid], ShouldBeFalse)
		})

		Convey("checked in a batch", func() {
			So(s.ProcessesNotRunningOnHost(ctx, "localhost", []int{pid, pid + 1}, batchCheckTimeout)[pid], ShouldBeFalse)
		})

		Convey("checked with ProcessNotRunningOnHost", func() {
			So(s.ProcessNotRunningOnHost(ctx, pid, "localhost"), ShouldBeFalse)
		})
	})
}

// scriptedHost is a Host whose RunCmd answers from a function of the command,
// and which records every command it was sent.
type scriptedHost struct {
	answer func(ctx context.Context, cmd string) (string, error)
	mu     sync.Mutex
	cmds   []string
}

func (h *scriptedHost) RunCmd(ctx context.Context, cmd string, _ bool) (string, string, error) {
	h.mu.Lock()
	h.cmds = append(h.cmds, cmd)
	h.mu.Unlock()

	out, err := h.answer(ctx, cmd)

	return out, "", err
}

func (h *scriptedHost) Close(_ context.Context) {}

func (h *scriptedHost) sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string(nil), h.cmds...)
}

// shellAnswer returns a scriptedHost answer that behaves like a shell running
// the command wr sent on a host whose processes have the given stats: it echoes
// the marker and its own pid, then lists each requested pid that exists.
func shellAnswer(procs map[int]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, cmd string) (string, error) {
		var out strings.Builder

		fmt.Fprintf(&out, "%s %d\n", psBatchMarker, fakeShellPid)

		for _, pid := range requestedPids(cmd) {
			stat, ok := procs[pid]
			if pid == fakeShellPid {
				stat, ok = "Ss", true
			}

			if ok {
				fmt.Fprintf(&out, "%7d %s\n", pid, stat)
			}
		}

		return out.String(), nil
	}
}

// requestedPids returns the pids after "-p " in a command wr sent, with the
// shell's "$$" standing for fakeShellPid.
func requestedPids(cmd string) []int {
	_, after, _ := strings.Cut(cmd, "-p ")
	list, _, _ := strings.Cut(after, " ")

	var pids []int

	for field := range strings.SplitSeq(list, ",") {
		if field == "$$" {
			pids = append(pids, fakeShellPid)

			continue
		}

		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}

		pids = append(pids, pid)
	}

	return pids
}

func checkPids(h Host, pids ...int) map[int]bool {
	s := &Scheduler{impl: &processStatusScheduler{host: h}}

	return s.ProcessesNotRunningOnHost(context.Background(), "host", pids, batchCheckTimeout)
}

func TestProcessesNotRunningOnHost(t *testing.T) {
	Convey("ProcessesNotRunningOnHost checks a host's pids in one remote command", t, func() {
		Convey("a pid missing from the output is not running", func() {
			h := &scriptedHost{answer: shellAnswer(nil)}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: true, 20: true})
			So(h.sent(), ShouldHaveLength, 1)
		})

		Convey("a zombie is not running", func() {
			h := &scriptedHost{answer: shellAnswer(map[int]string{10: "Z", 20: "Z+"})}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: true, 20: true})
		})

		Convey("a live pid is running", func() {
			h := &scriptedHost{answer: shellAnswer(map[int]string{10: "Ss", 20: "R+"})}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: false})
		})

		Convey("a mixed batch gets a verdict per pid", func() {
			h := &scriptedHost{answer: shellAnswer(map[int]string{10: "S", 30: "Z", 40: "Dl"})}

			So(checkPids(h, 10, 20, 30, 40), ShouldResemble,
				map[int]bool{10: false, 20: true, 30: true, 40: false})
			So(h.sent(), ShouldHaveLength, 1)
		})

		Convey("output before the marker, such as a login banner, is ignored", func() {
			h := &scriptedHost{answer: func(ctx context.Context, cmd string) (string, error) {
				out, err := shellAnswer(map[int]string{10: "S"})(ctx, cmd)

				return "Welcome to node1\n" + out, err
			}}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: true})
		})

		Convey("very many pids are split across a few commands", func() {
			h := &scriptedHost{answer: shellAnswer(nil)}
			pids := make([]int, 2*maxPidsPerPsCommand+1)

			for i := range pids {
				pids[i] = i + 1
			}

			notRunning := checkPids(h, pids...)
			So(notRunning, ShouldHaveLength, len(pids))
			So(h.sent(), ShouldHaveLength, 3)
		})
	})

	Convey("ProcessesNotRunningOnHost confirms nothing from an untrustworthy shell answer", t, func() {
		for name, output := range map[string]string{
			"ps printed nothing":             psBatchMarker + " 7777\n",
			"the marker names no shell pid":  psBatchMarker + "\n",
			"the shell pid is not a number":  psBatchMarker + " x\n",
			"the shell is shown as a zombie": psBatchMarker + " 7777\n 7777 Z\n",
			"a pid was not asked about":      psBatchMarker + " 7777\n 7777 S\n   99 S\n",
			"a line is not a pid and a stat": psBatchMarker + " 7777\n 7777 S\n   10 S extra\n",
		} {
			Convey("when "+name, func() {
				h := &scriptedHost{answer: rawAnswer(output)}

				So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: false})
				So(h.sent(), ShouldHaveLength, 1)
			})
		}
	})

	Convey("ProcessesNotRunningOnHost checks each other pid on its own after a forced command answered", t, func() {
		procs := map[int]string{10: "S\n", 30: "Z\n"}

		Convey("taking that answer for the first pid", func() {
			h := &scriptedHost{answer: forcedAnswer(procs)}

			So(checkPids(h, 10, 20, 30), ShouldResemble, map[int]bool{10: false, 20: true, 30: true})
			So(h.sent(), ShouldHaveLength, 3)
		})

		Convey("giving each pid its own timeout", func() {
			const perCall = 40 * time.Millisecond

			h := &scriptedHost{answer: func(ctx context.Context, cmd string) (string, error) {
				select {
				case <-time.After(perCall):
				case <-ctx.Done():
					return "", ctx.Err()
				}

				return forcedAnswer(procs)(ctx, cmd)
			}}
			s := &Scheduler{impl: &processStatusScheduler{host: h}}

			notRunning := s.ProcessesNotRunningOnHost(context.Background(), "host", []int{10, 20, 30, 40}, 3*perCall)
			So(notRunning, ShouldResemble, map[int]bool{10: false, 20: true, 30: true, 40: true})
		})

		Convey("stopping, with nothing more confirmed, once ctx ends", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			h := &scriptedHost{answer: func(ctx context.Context, cmd string) (string, error) {
				cancel()

				return forcedAnswer(nil)(ctx, cmd)
			}}
			s := &Scheduler{impl: &processStatusScheduler{host: h}}

			notRunning := s.ProcessesNotRunningOnHost(ctx, "host", []int{10, 20, 30}, batchCheckTimeout)
			So(notRunning, ShouldResemble, map[int]bool{10: true, 20: false, 30: false})
			So(h.sent(), ShouldHaveLength, 1)
		})
	})

	Convey("A failed command confirms nothing and is not retried per pid", t, func() {
		Convey("when it timed out", func() {
			h := &scriptedHost{answer: func(ctx context.Context, _ string) (string, error) {
				<-ctx.Done()

				return "", ctx.Err()
			}}
			s := &Scheduler{impl: &processStatusScheduler{host: h}}

			notRunning := s.ProcessesNotRunningOnHost(context.Background(), "host", []int{10, 20}, 50*time.Millisecond)
			So(notRunning, ShouldResemble, map[int]bool{10: false, 20: false})
			So(h.sent(), ShouldHaveLength, 1)
		})

		Convey("when it exited non-zero", func() {
			h := &scriptedHost{answer: func(context.Context, string) (string, error) {
				return "", errors.New("Process exited with status 2") //nolint:err113
			}}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: false})
			So(h.sent(), ShouldHaveLength, 1)
		})
	})
}

// rawAnswer returns a scriptedHost answer that always prints output.
func rawAnswer(output string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) {
		return output, nil
	}
}

// forcedAnswer returns a scriptedHost answer that behaves like the ps-only
// forced command: it prints only the stat of the first requested pid, or
// nothing if that pid does not exist.
func forcedAnswer(procs map[int]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, cmd string) (string, error) {
		pids := requestedPids(cmd)
		if len(pids) == 0 {
			return "", nil
		}

		return procs[pids[0]], nil
	}
}

func TestProcessesNotRunningOnLocalHost(t *testing.T) {
	Convey("ProcessesNotRunningOnHost gives the right verdicts for real local processes", t, func() {
		ctx := context.Background()
		s, err := New(ctx, "local", &ConfigLocal{testShell, time.Second, 0, 0})
		So(err, ShouldBeNil)

		defer s.Cleanup(ctx)

		live := exec.CommandContext(ctx, "sleep", "30")
		So(live.Start(), ShouldBeNil)

		defer func() {
			So(live.Process.Kill(), ShouldBeNil)
			So(live.Wait(), ShouldNotBeNil)
		}()

		zombie := exec.CommandContext(ctx, "true")
		So(zombie.Start(), ShouldBeNil)

		defer func() { So(zombie.Wait(), ShouldBeNil) }()

		dead := exec.CommandContext(ctx, "true")
		So(dead.Run(), ShouldBeNil)

		host, ok := s.impl.getHost("localhost")
		So(ok, ShouldBeTrue)

		zombieState := func() string {
			out, _, _ := host.RunCmd(ctx, fmt.Sprintf("ps -o stat= -p %d", zombie.Process.Pid), false) //nolint:errcheck

			return strings.TrimSpace(out)
		}

		So(pollUntilFor(10*time.Second, 20*time.Millisecond, func() bool {
			return strings.HasPrefix(zombieState(), "Z")
		}), ShouldBeTrue)

		notRunning := s.ProcessesNotRunningOnHost(ctx, "localhost",
			[]int{live.Process.Pid, zombie.Process.Pid, dead.Process.Pid}, batchCheckTimeout)

		So(notRunning, ShouldResemble, map[int]bool{
			live.Process.Pid:   false,
			zombie.Process.Pid: true,
			dead.Process.Pid:   true,
		})
	})
}
