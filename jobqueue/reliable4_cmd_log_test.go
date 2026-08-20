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

// This file tests the client and MANAGER halves of reliable4 ITEM C2:
// Client.Execute logged and quoted the whole command line, so a runner working on
// a pathological Cmd wrote it out several more times per job on top of the
// runner's own log lines - and the manager did the same, once per job, into its
// OWN log, which is the log the next profiling round reads.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

// cmdLogTestCmdBytes is how long a test command line is: far over
// internal.AbbreviateMax so an unbounded log line or error is unmistakable, and
// well under Linux's 128KB MAX_ARG_STRLEN so the command still execs.
const cmdLogTestCmdBytes = 20000

// cmdLogTestMaxLog bounds the whole captured log of one execution. The command
// line alone is cmdLogTestCmdBytes, so a single unbounded copy of it breaks this.
const cmdLogTestMaxLog = 4096

// cmdLogTestMaxErr bounds the error Execute returns for a failed command.
const cmdLogTestMaxErr = 2048

// cmdLogTestSentinel ends a test command line. Filler bytes alone cannot prove
// anything (they also appear in the kept prefix), so the tail is a distinctive
// marker that can only be present if the WHOLE command line was logged.
const cmdLogTestSentinel = "-TAIL-OF-THE-CMD"

// cmdLogTestMode is the permission for the test job's cwd.
const cmdLogTestMode = 0o755

// cmdLogTestMaxManagerLine bounds ONE manager log line about a job: the
// abbreviated command plus the job key, scheduler group, level, timestamp and
// caller. A single unbounded copy of a cmdLogTestCmdBytes command line is 20x
// this.
const cmdLogTestMaxManagerLine = 1024

// cmdLogTestTouchInterval makes the kill test's server tell its client to touch
// every 100ms, so the kill is delivered promptly instead of after the 15s
// production default.
const cmdLogTestTouchInterval = 100 * time.Millisecond

// cmdLogTestKillCmd is a command that keeps the shell forked (so it has a CHILD
// process to kill) for long enough to be killed mid-run.
const cmdLogTestKillCmd = "sleep 30 && true"

// cmdLogManagerLine is one manager log line the test requires to exist, be
// bounded, and name the job it is about.
type cmdLogManagerLine struct {
	msg string
	key string
}

// TestReliable4ManagerLogsBoundedCmd is the pin for the MANAGER-side per-job log
// lines. They are clog.Debug, so they are silent at the shipped default log
// level - but production was running with --debug (~12MB/min), which is exactly
// the regime the next profiling round reads, and each of these fired once per job
// with the whole user-supplied command line in it.
//
// It FAILS pre-fix on the sentinel assertion, because "reserved job",
// "completed job", "buried job" and "unburied job" all embedded job.Cmd whole.
func TestReliable4ManagerLogsBoundedCmd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)

	Convey("Given a manager whose own log is captured at every level", t, func() {
		logCtx, buf := cmdLogSyncCapture(context.Background())

		server, _, token, err := serve(logCtx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(logCtx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("Its per-job lines are bounded and still name the job key", func() {
			okKey := cmdLogManagerJob(t, jq, "true"+cmdLogPad("m"), true)
			failKey := cmdLogManagerJob(t, jq, "true"+cmdLogPad("n"), false)

			kicked, errk := jq.Kick([]*JobEssence{{JobKey: failKey}})
			So(errk, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			deleted, errd := jq.Delete([]*JobEssence{{JobKey: failKey}})
			So(errd, ShouldBeNil)
			So(deleted, ShouldEqual, 1)

			out := buf.String()
			t.Logf("MANAGERLOG-MEASURED cmdLen=%d managerLogBytes=%d",
				len("true")+len(cmdLogPad("m")), len(out))

			// the sharp assertion: nothing anywhere in the MANAGER's own log carries
			// the END of a 20KB command line, so no line copied one whole. A bytes
			// bound alone could be satisfied by deleting a log line rather than
			// bounding it.
			So(out, ShouldNotContainSubstring, cmdLogTestSentinel)

			var missing, oversized, unabbreviated []string

			for _, want := range []cmdLogManagerLine{
				{"reserved job", okKey},
				{"completed job", okKey},
				{"reserved job", failKey},
				{"buried job", failKey},
				{"unburied job", failKey},
				{"removed job", failKey},
			} {
				line := cmdLogManagerFindLine(out, want.msg, want.key)
				switch {
				case line == "":
					// each line must exist AND carry the key, which is what lets an
					// operator get the whole command back out of `wr status`.
					missing = append(missing, fmt.Sprintf("%s(%s)", want.msg, want.key))
				case len(line) >= cmdLogTestMaxManagerLine:
					oversized = append(oversized, fmt.Sprintf("%s=%d", want.msg, len(line)))
				case !strings.Contains(line, "truncated"):
					unabbreviated = append(unabbreviated, want.msg)
				}
			}

			So(missing, ShouldBeEmpty)
			So(oversized, ShouldBeEmpty)
			So(unabbreviated, ShouldBeEmpty)
		})
	})
}

// cmdLogSyncCapture returns a child of ctx whose clog output is captured, plus
// the concurrency-safe sink it is captured into. Unlike captureLogCtx's plain
// bytes.Buffer this is safe to read while OTHER goroutines are still writing to
// it - which both a whole server's log and Execute's kill path need, since the
// latter logs from goroutines that outlive the Execute call.
func cmdLogSyncCapture(ctx context.Context) (context.Context, *cmdLogSyncBuffer) {
	buf := new(cmdLogSyncBuffer)
	handler := log15.StreamHandler(buf, log15.LogfmtFormat())

	return clog.ContextWithLogHandler(ctx, handler), buf
}

// cmdLogManagerJob adds one job with the given huge command line, reserves it,
// and then either executes it to completion (exec true) or buries it, returning
// its key. Together those cover the manager's "reserved job", "completed job" and
// "buried job" lines; the caller kicks the buried one for "unburied job".
func cmdLogManagerJob(t *testing.T, jq *Client, cmd string, exec bool) string {
	t.Helper()

	cwd := filepath.Join(t.TempDir(), "job")
	So(os.MkdirAll(cwd, cmdLogTestMode), ShouldBeNil)

	repGroup := "reliable4_mgrlog"
	job := &Job{
		Cmd: cmd, Cwd: cwd, CwdMatters: true,
		RepGroup: repGroup, ReqGroup: repGroup,
		Requirements: &jqs.Requirements{
			RAM: 10, Time: 10 * time.Second, Cores: 0, Other: make(map[string]string),
		},
	}

	added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, job.Key())

	if exec {
		So(jq.Execute(context.Background(), reserved, "/bin/sh"), ShouldBeNil)
	} else {
		So(jq.Bury(reserved, nil, FailReasonAbnormal), ShouldBeNil)
	}

	return job.Key()
}

// cmdLogPad returns a shell comment of cmdLogTestCmdBytes filler bytes, so a
// command line can be made huge without producing any output (output would be
// quoted in the error for its own, separate, reason).
func cmdLogPad(char string) string {
	return " # " + strings.Repeat(char, cmdLogTestCmdBytes) + cmdLogTestSentinel
}

// cmdLogManagerFindLine returns the first line of a captured log whose message is
// exactly msg and which names key, or "" if there is none. The message is matched
// as the quoted logfmt value so that "buried job" cannot match an "unburied job"
// line.
func cmdLogManagerFindLine(out, msg, key string) string {
	needle := fmt.Sprintf("msg=%q", msg)

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, needle) && strings.Contains(line, key) {
			return line
		}
	}

	return ""
}

// cmdLogSyncBuffer is a log sink that can be written by many goroutines and read
// by the test goroutine.
type cmdLogSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p to the buffer.
func (b *cmdLogSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *cmdLogSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// TestReliable4KilledCmdLogsBoundedCmd pins the kill path's log lines. They are
// Info/Warn, so unlike the manager's per-job lines they are visible at the
// SHIPPED log level, and "killed child of cmd" fires once per CHILD process of
// every killed job - so a kill storm, which is this branch's recurring failure
// mode, turns "exceptional per job" into a multiplier.
func TestReliable4KilledCmdLogsBoundedCmd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(false)
	serverConfig.Timings.TouchInterval = cmdLogTestTouchInterval

	Convey("Given a live manager that asks for frequent touches", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		Convey("A huge command line KILLED mid-run is logged bounded", func() {
			cmd := cmdLogTestKillCmd + cmdLogPad("k")
			cwd := filepath.Join(t.TempDir(), "job")
			So(os.MkdirAll(cwd, cmdLogTestMode), ShouldBeNil)

			job := &Job{
				Cmd: cmd, Cwd: cwd, CwdMatters: true,
				RepGroup: "reliable4_killlog", ReqGroup: "reliable4_killlog",
				Requirements: &jqs.Requirements{
					RAM: 10, Time: time.Minute, Cores: 0, Other: make(map[string]string),
				},
			}

			added, _, erra := jq.Add([]*Job{job}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(added, ShouldEqual, 1)

			reserved, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			// killing before Execute makes the kill land on the FIRST touch, which
			// is what runs the kill path deterministically.
			killed, errk := jq.Kill([]*JobEssence{{JobKey: job.Key()}})
			So(errk, ShouldBeNil)
			So(killed, ShouldEqual, 1)

			// the kill path logs from goroutines that outlive Execute, so this
			// capture has to be the concurrency-safe one.
			logCtx, buf := cmdLogSyncCapture(ctx)
			execErr := jq.Execute(logCtx, reserved, "/bin/sh")

			out := buf.String()
			t.Logf("KILLLOG-MEASURED cmdLen=%d logBytes=%d errBytes=%d",
				len(cmd), len(out), errLen(execErr))

			So(execErr, ShouldNotBeNil)
			So(out, ShouldContainSubstring, "killed child of cmd")
			So(out, ShouldNotContainSubstring, cmdLogTestSentinel)
			So(len(out), ShouldBeLessThan, cmdLogTestMaxLog)
			So(reserved.Cmd, ShouldEqual, cmd)
		})
	})
}

// errLen is len(err.Error()), or 0 for a nil error.
func errLen(err error) int {
	if err == nil {
		return 0
	}

	return len(err.Error())
}

// cmdLogRun adds one job with the given command, reserves it, and Executes it
// with a captured log context, returning the captured log, the job's key and the
// error Execute returned.
func cmdLogRun(ctx context.Context, t *testing.T, jq *Client, cmd, shell string) (string, string, error) {
	t.Helper()

	cwd := filepath.Join(t.TempDir(), "job")
	So(os.MkdirAll(cwd, cmdLogTestMode), ShouldBeNil)

	repGroup := "reliable4_cmdlog"
	job := &Job{
		Cmd: cmd, Cwd: cwd, CwdMatters: true,
		RepGroup: repGroup, ReqGroup: repGroup,
		Requirements: &jqs.Requirements{
			RAM: 10, Time: 10 * time.Second, Cores: 0, Other: make(map[string]string),
		},
	}
	added, _, err := jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	key := job.Key()

	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Cmd, ShouldEqual, cmd)

	logCtx, buf := captureLogCtx(ctx)
	execErr := jq.Execute(logCtx, reserved, shell)

	// the job the runner goes on to work with must still carry its whole command
	// line: bounding a log line is presentation only.
	So(reserved.Cmd, ShouldEqual, cmd)
	So(len(reserved.Cmd), ShouldBeGreaterThan, cmdLogTestCmdBytes)

	t.Logf("CMDLOG-MEASURED cmdLen=%d logBytes=%d errBytes=%d", len(cmd), buf.Len(), errLen(execErr))

	return buf.String(), key, execErr
}

// TestReliable4ExecuteLogsBoundedCmd is the behavioural pin for the client half
// of ITEM C2. It FAILS pre-fix, because Execute's "started executing" line and
// its exit-outcome errors each embedded the whole 20KB command line.
func TestReliable4ExecuteLogsBoundedCmd(t *testing.T) {
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

		Convey("Executing a job with a huge command line logs a bounded line naming it", func() {
			cmd := "true" + cmdLogPad("s")

			out, key, execErr := cmdLogRun(ctx, t, jq, cmd, "/bin/sh")

			So(execErr, ShouldBeNil)
			So(out, ShouldContainSubstring, "started executing")
			So(out, ShouldContainSubstring, key)
			So(out, ShouldContainSubstring, "truncated")
			So(out, ShouldNotContainSubstring, cmdLogTestSentinel)
			So(len(out), ShouldBeLessThan, cmdLogTestMaxLog)
		})

		Convey("A huge command line that exits non-zero is quoted bounded in the error", func() {
			cmd := "exit 1" + cmdLogPad("f")

			out, _, execErr := cmdLogRun(ctx, t, jq, cmd, "/bin/sh")

			So(execErr, ShouldNotBeNil)
			So(execErr.Error(), ShouldContainSubstring, "exited with code 1")
			So(execErr.Error(), ShouldContainSubstring, "truncated")
			So(execErr.Error(), ShouldNotContainSubstring, cmdLogTestSentinel)
			So(len(execErr.Error()), ShouldBeLessThan, cmdLogTestMaxErr)
			So(len(out), ShouldBeLessThan, cmdLogTestMaxLog)
		})

		Convey("A huge command line that fails TRANSIENTLY to start is quoted bounded too", func() {
			// 0d22eda abbreviated only the PERMANENT start-failure message and left
			// its transient twin quoting the whole command line, which is the very
			// line that reached 1.3MB in production. ETXTBSY (a write handle held
			// open on the shell) is the canonical transient fork/exec failure.
			shell, release := testBusyExecutable(t.TempDir())
			defer release()

			cmd := "true" + cmdLogPad("t")

			out, _, execErr := cmdLogRun(ctx, t, jq, cmd, shell)

			So(execErr, ShouldNotBeNil)
			So(execErr.Error(), ShouldContainSubstring, "text file busy")
			So(execErr.Error(), ShouldContainSubstring, "truncated")
			So(execErr.Error(), ShouldNotContainSubstring, cmdLogTestSentinel)
			So(len(execErr.Error()), ShouldBeLessThan, cmdLogTestMaxErr)
			So(len(out), ShouldBeLessThan, cmdLogTestMaxLog)
		})
	})
}
