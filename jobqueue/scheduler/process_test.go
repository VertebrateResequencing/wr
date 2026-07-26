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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	log15 "github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

// updatedForcedCommandParser mirrors the "ps + backstop kill" forced command
// documented in cmd/conf.go (the case/grep parsing), with the terminal kill/ps
// actions replaced by an echo of what they WOULD run, so the parse contract can be
// exercised in a test without actually killing anything.
const updatedForcedCommandParser = `c="$SSH_ORIGINAL_COMMAND"; ` +
	`p=$(echo "$c" | grep -oE '[-]p [0-9]+' | grep -oE '[0-9]+' | head -1); ` +
	`case "$c" in kill*) echo "KILL ${p:-0}";; *) echo "PS ${p:-0}";; esac`

// psOnlyForcedCommandParser mirrors the ORIGINAL ps-only forced command in
// cmd/conf.go, which ignores the requested command and only ever runs ps on the
// extracted pid (here echoed as "PS <pid>").
const psOnlyForcedCommandParser = `p=$(echo "$SSH_ORIGINAL_COMMAND" | grep -oE '[-]p [0-9]+' | ` +
	`grep -oE '[0-9]+' | head -1); echo "PS ${p:-0}"`

// captureLogCtx returns a context whose clog output is captured into the returned
// buffer, so a test can assert whether a code path logged anything.
func captureLogCtx() (context.Context, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	handler := log15.StreamHandler(buf, log15.LogfmtFormat())

	return clog.ContextWithLogHandler(context.Background(), handler), buf
}

type processStatusScheduler struct {
	mock
	host Host
}

func (s *processStatusScheduler) getHost(_ string) (Host, bool) {
	return s.host, s.host != nil
}

// TestProcessNotRunningOnHostBoundsLoggedOutput guards that a misbehaving or verbose
// forced command (see ProcessNotRunningOnHost's CONTRACT WARNING) whose output is
// neither empty nor a bare process state cannot blow up the manager log: the warn
// must carry only a short, single-line, length-capped excerpt of that output, not
// the whole (potentially multi-line/banner) blob.
func TestProcessNotRunningOnHostBoundsLoggedOutput(t *testing.T) {
	Convey("loggableProcessOutput bounds an excerpt to a single capped line", t, func() {
		Convey("a short single-line value is passed through verbatim, no ellipsis", func() {
			So(loggableProcessOutput("3"), ShouldEqual, "3")
		})

		Convey("only the first line is kept, with an ellipsis marker", func() {
			So(loggableProcessOutput("first\nsecond\nthird"), ShouldEqual, "first...")
		})

		Convey("an over-long first line is capped to loggableProcessOutputMax + ellipsis", func() {
			excerpt := loggableProcessOutput(strings.Repeat("x", loggableProcessOutputMax+50))
			So(excerpt, ShouldEqual, strings.Repeat("x", loggableProcessOutputMax)+"...")
		})

		Convey("the length cap falls on a rune boundary (no split multi-byte rune)", func() {
			excerpt := loggableProcessOutput(strings.Repeat("€", loggableProcessOutputMax+10))
			So(excerpt, ShouldEqual, strings.Repeat("€", loggableProcessOutputMax)+"...")
		})
	})

	Convey("A huge multi-line unexpected ps output is logged only as a bounded excerpt", t, func() {
		blob := strings.Repeat("x", 5000) + "\n" + strings.Repeat("noise line\n", 1000)
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: blob}}}
		ctx, buf := captureLogCtx()

		So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)

		logged := buf.String()

		Convey("the could-not-confirm verdict is still logged", func() {
			So(logged, ShouldContainSubstring, "could not confirm")
		})

		Convey("the whole log line is bounded far below the raw output size", func() {
			So(len(blob), ShouldBeGreaterThan, 10000)
			So(len(logged), ShouldBeLessThan, 512)
		})

		Convey("later lines of the blob are dropped, and a truncation marker is present", func() {
			So(logged, ShouldNotContainSubstring, "noise")
			So(logged, ShouldContainSubstring, "...")
		})
	})
}

type processStatusHost struct {
	stdout string
	err    error
}

func (h *processStatusHost) RunCmd(_ context.Context, _ string, _ bool) (string, string, error) {
	return h.stdout, "", h.err
}

func TestProcessNotRunningOnHostUsesProcessState(t *testing.T) {
	Convey("ProcessNotRunningOnHost treats absent processes as not running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: ""}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeTrue)
	})

	Convey("ProcessNotRunningOnHost treats zombie processes as not running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Z+\n"}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeTrue)
	})

	Convey("ProcessNotRunningOnHost treats sleeping processes as still running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "S\n"}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeFalse)
	})

	Convey("ProcessNotRunningOnHost treats host command failures as inconclusive", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{err: context.Canceled}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeFalse)
	})
}

// TestProcessNotRunningOnHostLogsWhenInconclusive is the reliable3 §1 regression
// test: when ProcessNotRunningOnHost cannot determine whether a process is alive
// or dead (so a lost job's death cannot be confirmed) it must fail LOUDLY - a warn
// log - rather than silently returning "assume alive". The three could-not-
// determine cases are a missing host, a host command (ssh) error, and ps output
// that is neither empty nor a recognised process state. Crucially, the alive/dead
// verdict for a correctly-configured, working check must be UNCHANGED and produce
// no spurious warning.
func TestProcessNotRunningOnHostLogsWhenInconclusive(t *testing.T) {
	Convey("A could-not-determine outcome returns false AND logs a warning", t, func() {
		Convey("when the host cannot be found", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: nil}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})

		Convey("when the host command errors", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{err: context.Canceled}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})

		Convey("when the ps output is neither empty nor a plausible process state", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "3\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})

		Convey("when the ps output is a header rather than a bare stat", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "STAT\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})

		Convey("when the ps output starts with Z but is not a valid stat token", func() {
			// Regression (reliable3): "Zebra"-style output was previously treated
			// as a dead zombie (returned true, logged nothing), so a live job
			// whose confirm-check returned garbage could be wrongly reclaimed.
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Zebra\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldContainSubstring, "could not confirm")
		})
	})

	Convey("A working check keeps its verdict and logs nothing", t, func() {
		Convey("a live (sleeping) process is still reported running, no warning", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Ss\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeFalse)
			So(buf.String(), ShouldBeEmpty)
		})

		Convey("an absent process is still reported not-running, no warning", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: ""}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeTrue)
			So(buf.String(), ShouldBeEmpty)
		})

		Convey("a zombie process is still reported not-running, no warning", func() {
			s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Z+\n"}}}
			ctx, buf := captureLogCtx()

			So(s.ProcessNotRunningOnHost(ctx, 42, "host"), ShouldBeTrue)
			So(buf.String(), ShouldBeEmpty)
		})
	})
}

// TestInterpretProcessState pins down the ps-stat parsing that underpins
// ProcessNotRunningOnHost's liveness verdict (reliable3 §1). Only a genuine, single-
// token `ps -o stat=` value counts as alive; empty output or a zombie is dead; and
// anything that is not a well-formed stat token - a "STAT" header, a multi-line
// banner, output with an embedded space, or an over-long blob - must be unknown, so
// it is surfaced by warnCannotConfirm instead of silently masquerading as a live
// process just because it happens to start with a state letter.
func TestInterpretProcessState(t *testing.T) {
	Convey("interpretProcessState maps ps stat output to a liveness verdict", t, func() {
		Convey("a well-formed live stat token is alive", func() {
			for _, state := range []string{"S", "Ss", "R+", "S<", "SNs", "D", "I", "Ts"} {
				So(isProcessState(state), ShouldBeTrue)
				So(interpretProcessState(state), ShouldEqual, processAlive)
			}
		})

		Convey("empty output is dead", func() {
			So(interpretProcessState(""), ShouldEqual, processDead)
		})

		Convey("a valid stat token starting with Z (a zombie) is dead", func() {
			for _, state := range []string{"Z", "Z+", "Zs"} {
				So(isProcessState(state), ShouldBeTrue)
				So(interpretProcessState(state), ShouldEqual, processDead)
			}
		})

		Convey("output starting with Z that is not a valid stat token is unknown, not dead", func() {
			// Regression (reliable3): a loose strings.HasPrefix(state, "Z") check
			// previously classified any Z-led output as a confirmed-dead zombie, so
			// garbage or banner output that merely began with Z wrongly declared a
			// live job dead and eligible for reclaim. It must instead be unknown
			// (surfaced by warnCannotConfirm), exactly like non-Z garbage.
			for _, state := range []string{
				"Zebra",                // a word that merely starts with the zombie code
				"Zombie detected\nfoo", // a multi-line banner starting with Z
				"Z zombie",             // a Z stat code then an embedded space and text
			} {
				So(isProcessState(state), ShouldBeFalse)
				So(interpretProcessState(state), ShouldEqual, processUnknown)
			}
		})

		Convey("output that is not a single stat token is unknown", func() {
			for _, state := range []string{
				"STAT",      // a ps header line
				"Ss\nextra", // a multi-line banner starting with a state letter
				"S foo",     // an embedded space
				"Sssssssss", // over-long, though every character is otherwise valid
				"Rebooting", // an over-long all-letters blob starting with a state code
				"foo",       // lowercase-led garbage that is not a valid token
			} {
				So(isProcessState(state), ShouldBeFalse)
				So(interpretProcessState(state), ShouldEqual, processUnknown)
			}
		})
	})
}

// TestKillProcessCommandContract guards the compatibility contract of the string
// KillProcessOnHost sends (see its CONTRACT WARNING), the way TestInterpretProcessState
// guards the ps contract: it must (a) start with "kill", (b) carry the pid in a
// "-p <pid>" token, and (c) be parsed by the documented updated forced command to
// an actual `kill -9 <pid>`. It also pins the SAFE NO-OP degradation on the old
// ps-only forced command, and that the updated forced command still runs the ps
// liveness check correctly.
func TestKillProcessCommandContract(t *testing.T) {
	const pid = 424242

	killStr := killProcessCommand(pid)
	psStr := fmt.Sprintf("ps -o stat= -p %d 2>/dev/null || test $? -eq 1", pid)

	Convey("The kill command string satisfies the forced-command contract", t, func() {
		Convey("(a) it starts with the kill marker an updated forced command branches on", func() {
			So(strings.HasPrefix(killStr, "kill"), ShouldBeTrue)
		})

		Convey("(b) it carries the pid in the same '-p <pid>' token the ps extractor uses", func() {
			So(killStr, ShouldContainSubstring, fmt.Sprintf("-p %d", pid))
			So(killStr, ShouldContainSubstring, fmt.Sprintf("kill -9 %d", pid))
		})

		Convey("(c) the documented updated forced command parses it to kill -9 <pid>", func() {
			got, err := runForcedCommandParser(updatedForcedCommandParser, killStr)
			So(err, ShouldBeNil)
			So(got, ShouldEqual, fmt.Sprintf("KILL %d", pid))
		})

		Convey("the same forced command still runs the ps liveness check on the ps string", func() {
			got, err := runForcedCommandParser(updatedForcedCommandParser, psStr)
			So(err, ShouldBeNil)
			So(got, ShouldEqual, fmt.Sprintf("PS %d", pid))
		})

		Convey("on the OLD ps-only forced command the kill string is a SAFE NO-OP (ps, no kill)", func() {
			got, err := runForcedCommandParser(psOnlyForcedCommandParser, killStr)
			So(err, ShouldBeNil)
			So(got, ShouldEqual, fmt.Sprintf("PS %d", pid))
		})
	})
}

// runForcedCommandParser runs one of the forced-command parsers above under sh,
// with SSH_ORIGINAL_COMMAND set to the command wr would send, and returns its
// trimmed stdout (e.g. "KILL 424242" or "PS 424242").
func runForcedCommandParser(parser, sshOriginalCommand string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", parser) // #nosec G204 -- fixed test parser

	cmd.Env = append(os.Environ(), "SSH_ORIGINAL_COMMAND="+sshOriginalCommand)
	out, err := cmd.Output()

	return strings.TrimSpace(string(out)), err
}
