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
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const batchCheckTimeout = 5 * time.Second

func TestBatchProcessCommandForcedCommandContract(t *testing.T) {
	Convey("The documented forced commands read the batched ps as a ps of its first pid", t, func() {
		cmd := batchProcessCommand([]int{424242, 424243})

		for _, parser := range []string{psOnlyForcedCommandParser, updatedForcedCommandParser} {
			got, err := runForcedCommandParser(parser, cmd)
			So(err, ShouldBeNil)
			So(got, ShouldEqual, "PS 424242")
		}
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

func TestProcessesNotRunningOnHost(t *testing.T) {
	Convey("ProcessesNotRunningOnHost checks a host's pids in one remote command", t, func() {
		Convey("a pid missing from the output is not running", func() {
			h := &scriptedHost{answer: batchAnswer("", "S")}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: true, 20: true})
			So(h.sent(), ShouldHaveLength, 1)
		})

		Convey("a zombie is not running", func() {
			h := &scriptedHost{answer: batchAnswer("   10 Z\n   20 Z+\n", "S")}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: true, 20: true})
		})

		Convey("a live pid is running", func() {
			h := &scriptedHost{answer: batchAnswer("   10 Ss\n   20 R+\n", "")}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: false})
		})

		Convey("a mixed batch gets a verdict per pid", func() {
			h := &scriptedHost{answer: batchAnswer("   10 S\n   30 Z\n   40 Dl\n", "")}

			So(checkPids(h, 10, 20, 30, 40), ShouldResemble,
				map[int]bool{10: false, 20: true, 30: true, 40: false})
			So(h.sent(), ShouldHaveLength, 1)
		})

		Convey("output before the marker, such as a login banner, is ignored", func() {
			h := &scriptedHost{answer: func(context.Context, string) (string, error) {
				return "Welcome to node1\n" + psBatchMarker + "\n   10 S\n", nil
			}}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: true})
		})

		Convey("very many pids are split across a few commands", func() {
			h := &scriptedHost{answer: batchAnswer("", "S")}
			pids := make([]int, 2*maxPidsPerPsCommand+1)

			for i := range pids {
				pids[i] = i + 1
			}

			notRunning := checkPids(h, pids...)
			So(notRunning, ShouldHaveLength, len(pids))
			So(h.sent(), ShouldHaveLength, 3)
		})
	})

	Convey("ProcessesNotRunningOnHost falls back to one command per pid", t, func() {
		single := func(_ context.Context, cmd string) (string, error) {
			switch {
			case strings.HasPrefix(cmd, "echo "):
				return "S\n", nil // what the ps-only forced command says about the first pid
			case strings.Contains(cmd, "-p 20 "):
				return "", nil
			default:
				return "S\n", nil
			}
		}

		Convey("when a forced command answered for only one pid", func() {
			h := &scriptedHost{answer: single}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: false, 20: true})
			So(h.sent(), ShouldHaveLength, 3)
		})

		Convey("when the output names a pid that was not asked about", func() {
			h := &scriptedHost{answer: batchAnswer("   99 S\n", "")}

			So(checkPids(h, 10), ShouldResemble, map[int]bool{10: true})
			So(h.sent(), ShouldHaveLength, 2)
		})

		Convey("when a line after the marker is not a pid and a stat", func() {
			h := &scriptedHost{answer: batchAnswer("   10 S extra\n", "S")}

			So(checkPids(h, 10), ShouldResemble, map[int]bool{10: false})
			So(h.sent(), ShouldHaveLength, 2)
		})

		Convey("when the batched command failed without timing out", func() {
			h := &scriptedHost{answer: func(_ context.Context, cmd string) (string, error) {
				if strings.HasPrefix(cmd, "echo ") {
					return "", errors.New("Process exited with status 2") //nolint:err113
				}

				return "", nil
			}}

			So(checkPids(h, 10, 20), ShouldResemble, map[int]bool{10: true, 20: true})
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

				return single(ctx, cmd)
			}}
			s := &Scheduler{impl: &processStatusScheduler{host: h}}

			notRunning := s.ProcessesNotRunningOnHost(context.Background(), "host", []int{10, 20, 30, 40}, 3*perCall)
			So(notRunning, ShouldResemble, map[int]bool{10: false, 20: true, 30: false, 40: false})
		})
	})

	Convey("A batched command that times out confirms nothing and is not retried per pid", t, func() {
		h := &scriptedHost{answer: func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()

			return "", ctx.Err()
		}}
		s := &Scheduler{impl: &processStatusScheduler{host: h}}

		notRunning := s.ProcessesNotRunningOnHost(context.Background(), "host", []int{10, 20}, 50*time.Millisecond)
		So(notRunning, ShouldResemble, map[int]bool{10: false, 20: false})
		So(h.sent(), ShouldHaveLength, 1)
	})
}

// batchAnswer returns a scriptedHost answer that replies to a batched ps with
// psOutput after the marker a real shell would echo, and to any single-pid ps
// with single.
func batchAnswer(psOutput, single string) func(context.Context, string) (string, error) {
	return func(_ context.Context, cmd string) (string, error) {
		if strings.HasPrefix(cmd, "echo ") {
			return psBatchMarker + "\n" + psOutput, nil
		}

		return single, nil
	}
}

func checkPids(h Host, pids ...int) map[int]bool {
	s := &Scheduler{impl: &processStatusScheduler{host: h}}

	return s.ProcessesNotRunningOnHost(context.Background(), "host", pids, batchCheckTimeout)
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
