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
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	. "github.com/smartystreets/goconvey/convey"
)

// errCmdlineNeverMatched is returned by startMarkedProcess when a spawned
// process's command line never becomes the expected marked value.
var errCmdlineNeverMatched = errors.New("process cmdline never became expected value")

func TestLSFRecoverNoEnumeration(t *testing.T) {
	ctx := context.Background()
	req := &Requirements{RAM: 1, Time: time.Second, Cores: 0, CoresSet: true}

	Convey("Given an LSF scheduler, Recover called 50 times does no enumeration and returns nil", t, func() {
		// the lsf impl has no processLister and its recover() is a no-op, so no
		// process enumeration can occur; constructing it directly avoids needing
		// a real LSF environment.
		s := &Scheduler{Name: "lsf", impl: &lsf{}}

		errs := 0

		for range 50 {
			if err := s.Recover(ctx, "some cmd", req, nil); err != nil {
				errs++
			}
		}

		So(errs, ShouldEqual, 0)
	})
}

func TestLocalRecoverOnce(t *testing.T) {
	ctx := context.Background()
	req := &Requirements{RAM: 1, Time: time.Second, Cores: 0, CoresSet: true}

	Convey("Given a local scheduler with a counting processLister double", t, func() {
		s, err := New(ctx, "local", &ConfigLocal{testShell, time.Second, 0, 0})
		So(err, ShouldBeNil)

		l, ok := s.impl.(*local)
		So(ok, ShouldBeTrue)

		Convey("Recover for 50 distinct cmds in one pass enumerates exactly once and tracks every pid", func() {
			const n = 50

			cmds := make([]*exec.Cmd, 0, n)
			procs := make([]*process.Process, 0, n)
			cmdlines := make([]string, 0, n)
			spawnErrs := 0

			for i := range n {
				c, p, cl, serr := startMarkedProcess(ctx, fmt.Sprintf("wr_recover_test_%d", i))
				if c != nil {
					cmds = append(cmds, c)
				}

				if serr != nil {
					spawnErrs++

					continue
				}

				procs = append(procs, p)
				cmdlines = append(cmdlines, cl)
			}

			defer killProcesses(cmds)
			defer close(l.stopPidMonitoring)

			So(spawnErrs, ShouldEqual, 0)
			So(len(procs), ShouldEqual, n)

			var count int64

			l.processLister = func() ([]*process.Process, error) {
				atomic.AddInt64(&count, 1)

				return procs, nil
			}

			recoverErrs := 0

			for _, cl := range cmdlines {
				if rerr := s.Recover(ctx, cl, req, nil); rerr != nil {
					recoverErrs++
				}
			}

			So(recoverErrs, ShouldEqual, 0)
			So(atomic.LoadInt64(&count), ShouldEqual, 1)

			l.rpMutex.Lock()
			tracked := len(l.recoveredPids)
			l.rpMutex.Unlock()

			So(tracked, ShouldEqual, n)
		})
	})
}

func TestLocalRecoverDedup(t *testing.T) {
	ctx := context.Background()
	req := &Requirements{RAM: 1, Time: time.Second, Cores: 0, CoresSet: true}

	Convey("Given two processes matching one cmd, Recover tracks only one pid", t, func() {
		s, err := New(ctx, "local", &ConfigLocal{testShell, time.Second, 0, 0})
		So(err, ShouldBeNil)

		l, ok := s.impl.(*local)
		So(ok, ShouldBeTrue)

		c1, p1, cl1, err1 := startMarkedProcess(ctx, "wr_recover_dedup")
		c2, p2, cl2, err2 := startMarkedProcess(ctx, "wr_recover_dedup")

		defer killProcesses([]*exec.Cmd{c1, c2})
		defer close(l.stopPidMonitoring)

		So(err1, ShouldBeNil)
		So(err2, ShouldBeNil)
		So(cl1, ShouldEqual, cl2)

		l.processLister = func() ([]*process.Process, error) {
			return []*process.Process{p1, p2}, nil
		}

		So(s.Recover(ctx, cl1, req, nil), ShouldBeNil)

		l.rpMutex.Lock()
		tracked := len(l.recoveredPids)
		l.rpMutex.Unlock()

		So(tracked, ShouldEqual, 1)
	})
}

// startMarkedProcess starts a long-running `sleep` process whose command line
// argv[0] is set to marker (via bash's `exec -a`), so that its enumerated
// Cmdline() is deterministic and matchable by recover(). It returns the started
// command (for later killing), the corresponding *process.Process, and its
// enumerated command line.
func startMarkedProcess(ctx context.Context, marker string) (*exec.Cmd, *process.Process, string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", "exec -a "+marker+" sleep 300") //nolint:gosec

	if err := cmd.Start(); err != nil {
		return nil, nil, "", err
	}

	p, err := process.NewProcess(int32(cmd.Process.Pid)) //nolint:gosec
	if err != nil {
		return cmd, nil, "", err
	}

	// bash exec's into sleep in place (keeping the same pid), but that happens
	// asynchronously; poll until the command line reflects our marker.
	expected := marker + " 300"

	for range 200 {
		cl, cerr := p.Cmdline()
		if cerr == nil && cl == expected {
			return cmd, p, cl, nil
		}

		time.Sleep(10 * time.Millisecond)
	}

	return cmd, p, "", fmt.Errorf("%w: %s", errCmdlineNeverMatched, expected)
}

// killProcesses kills and reaps the given started commands.
func killProcesses(cmds []*exec.Cmd) {
	for _, c := range cmds {
		if c == nil || c.Process == nil {
			continue
		}

		_ = c.Process.Kill() //nolint:errcheck // best-effort cleanup
		_ = c.Wait()         //nolint:errcheck // best-effort cleanup, reaps the killed process
	}
}
