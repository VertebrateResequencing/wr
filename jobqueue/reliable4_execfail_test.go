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

package jobqueue

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// testMaxArgStrLen is Linux's MAX_ARG_STRLEN: the kernel caps a SINGLE argv
// element at 32 pages (128KB) regardless of the much larger total ARG_MAX, and
// wr hands the whole command line to the shell as one `-c` argument. A command
// line over this can therefore never exec, on any host, at any time.
const testMaxArgStrLen = 128 * 1024

// testExecFailFileMode / testExecFailExecMode are the modes used for the
// non-executable and executable helper files these tests create.
const (
	testExecFailFileMode = 0o600
	testExecFailExecMode = 0o755
)

// execFailMaxAttempts is how many reserve+Execute cycles execFailRun will do
// before giving up. It only needs to be more than one: the fix must consume
// exactly ONE reservation for a permanently unrunnable command, and pre-fix (or
// for a genuinely transient failure) every cycle is consumed.
const execFailMaxAttempts = 4

// TestReliable4ForkExecErrnoWrapping pins HOW cmd.Start() surfaces the errnos
// this fix classifies, so the classification is built on a measured fact rather
// than an assumption about the standard library: a fork/exec failure arrives as
// an *fs.PathError with Op "fork/exec" wrapping the raw errno, so errors.Is
// finds the errno through the wrapper. It also pins the one case that is NOT an
// errno (a bare-name $PATH lookup miss, which yields *exec.Error wrapping
// exec.ErrNotFound), because that is why the classification deliberately does
// not cover it.
func TestReliable4ForkExecErrnoWrapping(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("cmd.Start() wraps a fork/exec errno in an *fs.PathError that errors.Is can see", t, func() {
		dir := t.TempDir()

		Convey("an over-long single argv element gives E2BIG", func() {
			overLong := "echo " + strings.Repeat("x", 2*testMaxArgStrLen)
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", overLong)

			err := cmd.Start()
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "argument list too long")

			var pathErr *fs.PathError
			So(errors.As(err, &pathErr), ShouldBeTrue)
			So(pathErr.Op, ShouldEqual, "fork/exec")
			So(errors.Is(err, syscall.E2BIG), ShouldBeTrue)
		})

		Convey("a missing absolute executable gives ENOENT", func() {
			cmd := exec.CommandContext(t.Context(), filepath.Join(dir, "nosuchshell"), "-c", "true") //nolint:gosec

			err := cmd.Start()
			So(err, ShouldNotBeNil)

			var pathErr *fs.PathError
			So(errors.As(err, &pathErr), ShouldBeTrue)
			So(pathErr.Op, ShouldEqual, "fork/exec")
			So(errors.Is(err, syscall.ENOENT), ShouldBeTrue)
		})

		Convey("a non-executable absolute file gives EACCES", func() {
			noexec := filepath.Join(dir, "noexec")
			So(os.WriteFile(noexec, []byte("#!/bin/sh\ntrue\n"), testExecFailFileMode), ShouldBeNil)

			cmd := exec.CommandContext(t.Context(), noexec, "-c", "true")

			err := cmd.Start()
			So(err, ShouldNotBeNil)

			var pathErr *fs.PathError
			So(errors.As(err, &pathErr), ShouldBeTrue)
			So(errors.Is(err, syscall.EACCES), ShouldBeTrue)
		})

		Convey("a bare name missing from $PATH gives exec.ErrNotFound, NOT an errno", func() {
			cmd := exec.CommandContext(t.Context(), "definitelynosuchshell12345", "-c", "true")

			err := cmd.Start()
			So(err, ShouldNotBeNil)

			var execErr *exec.Error
			So(errors.As(err, &execErr), ShouldBeTrue)
			So(errors.Is(err, exec.ErrNotFound), ShouldBeTrue)
			So(errors.Is(err, syscall.ENOENT), ShouldBeFalse)
			So(errors.Is(err, syscall.EACCES), ShouldBeFalse)
		})
	})
}

// execFailOutcome is what execFailRun measured: how many reservations the job
// consumed, the error from the LAST Execute, and the server-side state of the
// job afterwards.
type execFailOutcome struct {
	key         string
	reserves    int
	execErr     error
	state       JobState
	failReason  string
	attempts    int
	untilBuried int
}

// execFailRun adds a single job with the given command, then reserves and
// Executes it under the given shell up to execFailMaxAttempts times, stopping as
// soon as the job is no longer reservable (which is what burying it achieves).
// It returns what it measured. Retries is 2 so that an UntilBuried-driven bury
// would happen well within execFailMaxAttempts if the server counted these as
// real attempts - it does not, which is the unbounded-retry half of the bug.
func execFailRun(ctx context.Context, t *testing.T, jq *Client, server *Server, cmd, shell string) execFailOutcome {
	t.Helper()

	cwd := filepath.Join(t.TempDir(), "job")
	So(os.MkdirAll(cwd, testExecFailExecMode), ShouldBeNil)

	repGroup := "reliable4_execfail"
	job := &Job{
		Cmd: cmd, Cwd: cwd, CwdMatters: true,
		RepGroup: repGroup, ReqGroup: repGroup, Retries: 2,
		Requirements: &jqs.Requirements{
			RAM: 10, Time: 10 * time.Second, Cores: 0, Other: make(map[string]string),
		},
	}
	added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	out := execFailOutcome{key: job.Key()}

	for range execFailMaxAttempts {
		reserved := execFailReserve(jq)
		if reserved == nil {
			break
		}

		out.reserves++
		out.execErr = jq.Execute(ctx, reserved, shell)
	}

	state, failReason, attempts, untilBuried, ok := execFailServerJob(server, out.key)
	So(ok, ShouldBeTrue)

	out.state, out.failReason, out.attempts, out.untilBuried = state, failReason, attempts, untilBuried

	// logged unconditionally so the whole measurement is on the record even
	// though GoConvey's default FailureHalts stops at the first failed So.
	t.Logf("EXECFAIL-MEASURED shell=%s cmdLen=%d reserves=%d state=%s failReason=%q attempts=%d untilBuried=%d",
		shell, len(cmd), out.reserves, out.state, out.failReason, out.attempts, out.untilBuried)

	return out
}

// TestReliable4ExecImpossibleBuriedFirstAttempt is the behavioural reproducer for
// reliable4 FINDING 5: a command that can NEVER exec was treated as a transient
// failure and released for a retry, so it consumed a scheduled runner, a
// reservation, the full command over RPC and a bolt write over and over (600+
// such events across 150 runner logs in 25 production minutes, 109 from one
// runner alone). Worse than the doc's observed ~31 attempts, the retries are
// UNBOUNDED: the server only decrements UntilBuried for a job whose StartTime is
// set, and an exec that never started never reported a start, so the retry
// ceiling is never reached however many times it fails.
//
// It asserts the fixed invariant - buried on the FIRST attempt with a FailReason
// naming the real cause, exactly one reservation consumed - so it FAILS on
// pre-fix code.
func TestReliable4ExecImpossibleBuriedFirstAttempt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a live manager", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("A command line over MAX_ARG_STRLEN is buried on the first attempt", func() {
			// deliberately over the 128KB single-argv-element cap, so exec fails
			// immediately and deterministically - no waiting, no timing.
			cmd := "echo " + strings.Repeat("x", testMaxArgStrLen+1024)
			run := execFailRun(ctx, t, jq, server, cmd, "/bin/sh")

			So(run.reserves, ShouldEqual, 1)
			So(run.state, ShouldEqual, JobStateBuried)
			So(run.failReason, ShouldEqual, FailReasonCArgs)
			So(run.untilBuried, ShouldEqual, 0)
			So(run.attempts, ShouldEqual, 0)
			So(run.execErr, ShouldNotBeNil)
			So(run.execErr.Error(), ShouldContainSubstring, "argument list too long")
			So(run.execErr.Error(), ShouldContainSubstring, FailReasonCArgs)

			Convey("and the enormous command line is not quoted whole into the error", func() {
				So(len(run.execErr.Error()), ShouldBeLessThan, 2048)
				So(run.execErr.Error(), ShouldContainSubstring, "bytes total")
			})
		})

		Convey("A missing shell (ENOENT) is buried on the first attempt as command not found", func() {
			shell := filepath.Join(t.TempDir(), "nosuchshell")
			run := execFailRun(ctx, t, jq, server, "echo hi", shell)

			So(run.reserves, ShouldEqual, 1)
			So(run.state, ShouldEqual, JobStateBuried)
			So(run.failReason, ShouldEqual, FailReasonCFound)
			So(run.attempts, ShouldEqual, 0)
		})

		Convey("A non-executable shell (EACCES) is buried on the first attempt as a permission problem", func() {
			shell := filepath.Join(t.TempDir(), "noexec")
			So(os.WriteFile(shell, []byte("#!/bin/sh\nexec /bin/sh \"$@\"\n"), testExecFailFileMode), ShouldBeNil)

			run := execFailRun(ctx, t, jq, server, "echo hi", shell)

			So(run.reserves, ShouldEqual, 1)
			So(run.state, ShouldEqual, JobStateBuried)
			So(run.failReason, ShouldEqual, FailReasonCPerm)
			So(run.attempts, ShouldEqual, 0)
		})

		Convey("A genuinely transient start failure (ETXTBSY) is still released and retried", func() {
			// this is the negative control that stops the permanent set being
			// widened: a shell that is being written to right now cannot exec, but
			// will exec fine a moment later, so burying it would destroy healthy
			// work. Proven transient in-band below: the very same job runs to
			// completion once the write handle is closed.
			busy, closeBusy := testBusyExecutable(t.TempDir())
			defer closeBusy()

			run := execFailRun(ctx, t, jq, server, "echo hi", busy)

			So(run.reserves, ShouldEqual, execFailMaxAttempts)
			So(run.state, ShouldEqual, JobStateDelayed)
			So(run.failReason, ShouldEqual, FailReasonStart)
			So(run.execErr, ShouldNotBeNil)
			So(run.execErr.Error(), ShouldContainSubstring, "text file busy")

			Convey("and once the write handle is closed the same job runs and completes", func() {
				closeBusy()

				job := execFailReserve(jq)
				So(job, ShouldNotBeNil)
				So(jq.Execute(ctx, job, busy), ShouldBeNil)

				state, _, _, _, ok := execFailServerJob(server, run.key)
				So(ok, ShouldBeFalse)
				So(state, ShouldEqual, JobState(""))
			})
		})
	})
}

// testBusyExecutable creates a working shell wrapper script in dir and keeps a
// write handle open on it, so exec'ing it fails with ETXTBSY - the canonical
// TRANSIENT fork/exec failure (a binary being deployed/copied right now). The
// returned func closes the handle, after which the same script execs fine; it is
// safe to call more than once.
func testBusyExecutable(dir string) (string, func()) {
	path := filepath.Join(dir, "busyshell")

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, testExecFailExecMode)
	So(err, ShouldBeNil)

	_, err = f.WriteString("#!/bin/sh\nexec /bin/sh \"$@\"\n")
	So(err, ShouldBeNil)

	closed := false

	return path, func() {
		if closed {
			return
		}

		closed = true

		So(f.Close(), ShouldBeNil)
	}
}

// execFailReserve tries to reserve a job, allowing for the release delay of a
// job that was just released back to the queue (serverConfig.Timings
// .ReleaseDelayMin is 100ms in tests). It returns nil if nothing became
// reservable, which for a buried job is the permanent truth.
func execFailReserve(jq *Client) *Job {
	for range 20 {
		job, err := jq.Reserve(100 * time.Millisecond)
		So(err, ShouldBeNil)

		if job != nil {
			return job
		}
	}

	return nil
}

// execFailServerJob reads the server-side job's state, fail reason, attempts and
// remaining retry budget under its lock, returning ok=false if the item is not in
// the queue (i.e. it completed and was archived).
func execFailServerJob(server *Server, key string) (JobState, string, int, int, bool) {
	item, err := server.q.Get(key)
	if err != nil || item == nil {
		return "", "", 0, 0, false
	}

	j, isJob := item.Data().(*Job)
	if !isJob {
		return "", "", 0, 0, false
	}

	j.RLock()
	defer j.RUnlock()

	return j.State, j.FailReason, int(j.Attempts), int(j.UntilBuried), true
}

// TestReliable4PermanentStartClassification is the boundary guard: it asserts
// exactly which cmd.Start() failures are permanent (bury) and, more
// importantly, that every load- or race-dependent one is still transient
// (release and retry). Misclassifying a transient failure as permanent buries
// healthy work, which is far worse than an extra retry, so this test exists to
// stop the permanent set being widened by accident.
func TestReliable4PermanentStartClassification(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("An exec-impossible fork/exec failure classifies as permanent, with the right FailReason", t, func() {
		dir := t.TempDir()

		Convey("E2BIG (command line over MAX_ARG_STRLEN) is permanent", func() {
			overLong := "echo " + strings.Repeat("x", 2*testMaxArgStrLen)
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", overLong)

			failReason, permanent := permanentStartFailReason(cmd.Start())
			So(permanent, ShouldBeTrue)
			So(failReason, ShouldEqual, FailReasonCArgs)
		})

		Convey("ENOENT (no such executable) is permanent", func() {
			cmd := exec.CommandContext(t.Context(), filepath.Join(dir, "nosuchshell"), "-c", "true") //nolint:gosec

			failReason, permanent := permanentStartFailReason(cmd.Start())
			So(permanent, ShouldBeTrue)
			So(failReason, ShouldEqual, FailReasonCFound)
		})

		Convey("EACCES (not executable) is permanent", func() {
			noexec := filepath.Join(dir, "noexec")
			So(os.WriteFile(noexec, []byte("#!/bin/sh\ntrue\n"), testExecFailFileMode), ShouldBeNil)

			cmd := exec.CommandContext(t.Context(), noexec, "-c", "true")

			failReason, permanent := permanentStartFailReason(cmd.Start())
			So(permanent, ShouldBeTrue)
			So(failReason, ShouldEqual, FailReasonCPerm)
		})
	})

	Convey("A load- or race-dependent fork/exec failure stays transient, so the job is retried", t, func() {
		dir := t.TempDir()

		Convey("ETXTBSY, produced for real by a still-open write handle, is transient", func() {
			busy, closeBusy := testBusyExecutable(dir)
			defer closeBusy()

			cmd := exec.CommandContext(t.Context(), busy, "-c", "true")

			startErr := cmd.Start()
			So(errors.Is(startErr, syscall.ETXTBSY), ShouldBeTrue)

			failReason, permanent := permanentStartFailReason(startErr)
			So(permanent, ShouldBeFalse)
			So(failReason, ShouldBeBlank)
		})

		Convey("ENOEXEC (not a valid executable format) is transient, i.e. outside the permanent set", func() {
			badfmt := filepath.Join(dir, "badfmt")
			So(os.WriteFile(badfmt, []byte("\x00\x01\x02\x03garbage"), testExecFailExecMode), ShouldBeNil)

			cmd := exec.CommandContext(t.Context(), badfmt, "-c", "true")

			startErr := cmd.Start()
			So(errors.Is(startErr, syscall.ENOEXEC), ShouldBeTrue)

			_, permanent := permanentStartFailReason(startErr)
			So(permanent, ShouldBeFalse)
		})

		Convey("resource-exhaustion, interruption and bad-path-shape errnos are all left transient", func() {
			transient := map[string]syscall.Errno{
				"ENOMEM":  syscall.ENOMEM,
				"EAGAIN":  syscall.EAGAIN,
				"EMFILE":  syscall.EMFILE,
				"ENFILE":  syscall.ENFILE,
				"EINTR":   syscall.EINTR,
				"EISDIR":  syscall.EISDIR,
				"ENOTDIR": syscall.ENOTDIR,
			}

			permanentNames := make([]string, 0, len(transient))

			for name, errno := range transient {
				// wrapped exactly as cmd.Start() wraps a real fork/exec errno.
				wrapped := &fs.PathError{Op: "fork/exec", Path: "/bin/sh", Err: errno}
				if _, permanent := permanentStartFailReason(wrapped); permanent {
					permanentNames = append(permanentNames, name)
				}
			}

			So(permanentNames, ShouldBeEmpty)
		})

		Convey("a bare-name $PATH miss is transient, because $PATH is per-host", func() {
			cmd := exec.CommandContext(t.Context(), "definitelynosuchshell12345", "-c", "true")

			_, permanent := permanentStartFailReason(cmd.Start())
			So(permanent, ShouldBeFalse)
		})

		Convey("a nil error is never permanent", func() {
			_, permanent := permanentStartFailReason(nil)
			So(permanent, ShouldBeFalse)
		})
	})
}
