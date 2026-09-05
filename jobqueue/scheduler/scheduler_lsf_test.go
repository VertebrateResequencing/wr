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

//nolint:gochecknoinits,goconst,lll // Legacy LSF integration tests keep setup and command literals close to assertions.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

var testLogger = log15.Root() //nolint:gochecknoglobals

// TestLSFQueueSelection tests the LSF scheduler's queue-selection logic
// (determineQueue and its helpers) directly, by constructing the parsed queue
// data that the scheduler would normally build from `bqueues -l` at setup. This needs no
// real LSF installation, so it runs everywhere (unlike TestLSF, which is gated
// on LSF being installed and WR_LSF_TEST_KEY); the real bsub/bqueues paths
// remain covered by TestLSF. determineQueue picks the first queue, in the
// scheduler's preferred order, that isn't excluded and has enough memory and
// runtime for the job.
const (
	memlimitKey = "memlimit"
	runlimitKey = "runlimit"
)

// testPipeCloseGrace is the pipe-close grace the fake-LSF tests run with. The
// shipped graces are a minute (bsub) and 15s (bkill), so a fake exe that
// deliberately leaves a descendant on its pipes would cost that long; lowering the
// grace makes the same code path cost milliseconds.
const testPipeCloseGrace = 200 * time.Millisecond

func init() {
	testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.StderrHandler))
}

// TestLSFLoadPrivateKey is the reliable3 §1b regression test: lsf.initialize used
// to swallow a private-key read error (the `if err == nil { store }`), leaving an
// empty key so every lost-job ssh check silently failed. loadPrivateKey must now
// log a warning when a configured key path cannot be read, while still loading a
// readable key and staying quiet when no path is configured. It needs no real
// LSF, so it runs everywhere.
func TestLSFLoadPrivateKey(t *testing.T) {
	Convey("loadPrivateKey warns when a configured key path cannot be read", t, func() {
		s := &lsf{config: &ConfigLSF{PrivateKeyPath: filepath.Join(t.TempDir(), "does-not-exist")}}
		ctx, buf := captureLogCtx()

		s.loadPrivateKey(ctx)

		So(s.privateKey, ShouldBeEmpty)
		So(buf.String(), ShouldContainSubstring, "could not read the private key")
	})

	Convey("loadPrivateKey loads a readable key without warning", t, func() {
		keyPath := filepath.Join(t.TempDir(), "id_wr")
		So(os.WriteFile(keyPath, []byte("PRIVATE-KEY-CONTENT"), 0o600), ShouldBeNil)

		s := &lsf{config: &ConfigLSF{PrivateKeyPath: keyPath}}
		ctx, buf := captureLogCtx()

		s.loadPrivateKey(ctx)

		So(s.privateKey, ShouldEqual, "PRIVATE-KEY-CONTENT")
		So(buf.String(), ShouldBeEmpty)
	})

	Convey("loadPrivateKey stays quiet when no key path is configured", t, func() {
		s := &lsf{config: &ConfigLSF{PrivateKeyPath: ""}}
		ctx, buf := captureLogCtx()

		s.loadPrivateKey(ctx)

		So(s.privateKey, ShouldBeEmpty)
		So(buf.String(), ShouldBeEmpty)
	})
}

// fakeLSFDelays is how the fake exes of newFakeLSFScheduler should hold things
// up. The zero value is an LSF that responds immediately and leaves nothing
// behind.
type fakeLSFDelays struct {
	// sleepSecs, if >0, makes the fake bsub sleep that many seconds before
	// responding, to exercise bsubExecTimeout.
	sleepSecs int

	// lingerSecs, if >0, makes the fake bsub background a subshell that holds the
	// inherited stdout pipe open for that many seconds after bsub itself has
	// exited successfully, to exercise bsubPipeCloseGrace (see
	// TestReliable4BsubPipeLinger).
	lingerSecs int

	// bjobsSleepSecs, if >0, makes the fake bjobs sleep that many seconds before
	// answering a `bjobs -w` list call (see bjobsAppearSleepSecs for the
	// appearance check, which is otherwise answered at once), to exercise
	// bjobsExecTimeout.
	bjobsSleepSecs int

	// bjobsAppearSleepSecs, if >0, makes the fake bjobs sleep that many seconds
	// before answering a `bjobs -w <id>` appearance check (the call waitForBjob
	// polls with), to exercise the bound on that check (see
	// TestReliable4BjobAppearedBound).
	bjobsAppearSleepSecs int

	// bjobsLingerSecs, if >0, makes the fake bjobs background a subshell that
	// holds the inherited stdout pipe open for that many seconds after bjobs
	// itself has exited successfully, to exercise bjobsPipeCloseGrace (see
	// TestReliable4BjobsBound).
	bjobsLingerSecs int

	// bjobsListJobs is how many RUN jobs of the "false" cmd the fake `bjobs -w`
	// list call reports, so a caller can tell a complete read of that list from a
	// short one.
	bjobsListJobs int
}

// setPipeCloseGraces lowers the bsub and bkill pipe-close graces for the duration
// of the test, restoring them afterwards.
func setPipeCloseGraces(t *testing.T, grace time.Duration) {
	t.Helper()

	origBsub, origBkill := bsubPipeCloseGrace, bkillPipeCloseGrace
	bsubPipeCloseGrace, bkillPipeCloseGrace = grace, grace

	t.Cleanup(func() {
		bsubPipeCloseGrace, bkillPipeCloseGrace = origBsub, origBkill
	})
}

// fakeBjobsListBody is the part of newFakeLSFScheduler's fake bjobs that answers
// a `bjobs -w` list call: it sleeps and/or lingers on its stdout pipe as delays
// asks, and reports delays.bjobsListJobs RUN elements of one array of the "false"
// cmd (the cmd the fake-LSF tests schedule), in real `bjobs -w` column order.
func fakeBjobsListBody(delays fakeLSFDelays) string {
	var body strings.Builder

	if delays.bjobsSleepSecs > 0 {
		fmt.Fprintf(&body, "sleep %d\n", delays.bjobsSleepSecs)
	}

	prefix := jobName("false", "development", false)

	for i := 1; i <= delays.bjobsListJobs; i++ {
		fmt.Fprintf(&body, "echo %q\n", fmt.Sprintf(
			"9876543 sb10 RUN normal host1 exec-host-1 %s_Xn0KpDLt[%d] Jul 22 12:00", prefix, i))
	}

	if delays.bjobsLingerSecs > 0 {
		fmt.Fprintf(&body, "( sleep %d ) &\n", delays.bjobsLingerSecs)
	}

	body.WriteString("exit 0\n")

	return body.String()
}

// TestLSFSubmitToQueueStderr guards submitToQueue's bsub-failure error message.
// When bsub runs but exits non-zero it surfaces bsub's stderr (which holds the
// real LSF rejection reason) rather than only the bare "exit status 255"; when
// bsub could not be executed at all there is no stderr, so the "(bsub stderr:"
// suffix is omitted rather than misleadingly showing an empty one. It needs no
// real LSF: bsubExe is pointed at a tiny script (or a missing path), and
// submitToQueue returns on the Output() error before any bjobs/regex work, so
// the fake alone exercises the real error path.
func TestLSFSubmitToQueueStderr(t *testing.T) {
	Convey("submitToQueue surfaces bsub's stderr in the returned error when bsub exits non-zero", t, func() {
		const wantStderr = "Job not submitted: pending job threshold reached"

		fakeBsub := filepath.Join(t.TempDir(), "bsub")
		writeFakeExe(t, fakeBsub, "#!/bin/sh\necho '"+wantStderr+"' >&2\nexit 255\n")

		s := &lsf{bsubExe: fakeBsub}

		err := s.submitToQueue(context.Background(), []string{"-J", "test"})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, wantStderr)
		So(err.Error(), ShouldContainSubstring, "(bsub stderr:")
	})

	Convey("submitToQueue omits the stderr suffix when bsub could not be executed at all", t, func() {
		// a non-existent bsub makes Output() fail with an *exec.Error (not an
		// *exec.ExitError), so bsubStderr returns "": the empty `(bsub stderr: "")`
		// suffix must be omitted rather than hiding the real failure mode.
		missingBsub := filepath.Join(t.TempDir(), "does-not-exist-bsub")

		s := &lsf{bsubExe: missingBsub}

		err := s.submitToQueue(context.Background(), []string{"-J", "test"})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "failed to run "+missingBsub)
		So(err.Error(), ShouldNotContainSubstring, "(bsub stderr:")
	})
}

// TestLSFParseBjobsStderr guards parseBjobs' bjobs-failure error message, as
// TestLSFSubmitToQueueStderr does submitToQueue's. Every scheduling pass asks
// LSF what it already has with `bjobs -w`, and when that exits non-zero the
// error used to carry only the bare "exit status 1" while the reason mbatchd
// gave went to bjobs' stderr and was dropped. When there is no stderr to
// surface, the "(bjobs stderr:" suffix is omitted rather than misleadingly
// showing an empty one. It needs no real LSF: bjobsExe is pointed at a tiny
// script.
func TestLSFParseBjobsStderr(t *testing.T) {
	countNothing := func(_, _, _ string) {}

	Convey("parseBjobs surfaces bjobs' stderr in the returned error when bjobs exits non-zero", t, func() {
		const wantStderr = "Batch system daemon not responding ... still trying"

		fakeBjobs := filepath.Join(t.TempDir(), "bjobs")
		writeFakeExe(t, fakeBjobs, "#!/bin/sh\necho '"+wantStderr+"' >&2\nexit 1\n")

		s := &lsf{config: &ConfigLSF{Shell: "bash"}, bjobsExe: fakeBjobs}

		err := s.parseBjobs(context.Background(), "wrd_", countNothing)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, wantStderr)
		So(err.Error(), ShouldContainSubstring, "(bjobs stderr:")
	})

	Convey("parseBjobs omits the stderr suffix when bjobs wrote nothing to stderr", t, func() {
		fakeBjobs := filepath.Join(t.TempDir(), "bjobs")
		writeFakeExe(t, fakeBjobs, "#!/bin/sh\nexit 1\n")

		s := &lsf{config: &ConfigLSF{Shell: "bash"}, bjobsExe: fakeBjobs}

		err := s.parseBjobs(context.Background(), "wrd_", countNothing)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "failed to run [bjobs -w]")
		So(err.Error(), ShouldNotContainSubstring, "(bjobs stderr:")
	})
}

func TestLSF(t *testing.T) {
	ctx := context.Background()
	// check if LSF seems to be installed
	_, err := exec.LookPath("lsadmin")
	if err == nil {
		_, err = exec.LookPath("bqueues")
	}

	if err != nil {
		SkipConvey("You can't get a new lsf scheduler without LSF being installed", t, func() {
			_, err = New(ctx, "lsf", &ConfigLSF{"development", "bash", "~/.ssh/id_rsa"})
			So(err, ShouldNotBeNil)
		})

		return
	}

	if os.Getenv("WR_LSF_TEST_KEY") == "" {
		SkipConvey("LSF tests disabled since WR_LSF_TEST_KEY is not set", t, func() {})

		return
	}

	Convey("You can get a new lsf scheduler", t, func() {
		otherReqs := make(map[string]string)

		specifiedOther := make(map[string]string)
		specifiedOther["scheduler_queue"] = "yesterday"
		specifiedOther["scheduler_misc"] = "-R avx"
		possibleReq := &Requirements{100, 1 * time.Minute, 1, 20, otherReqs, true, true, true}
		specifiedReq := &Requirements{100, 1 * time.Minute, 1, 20, specifiedOther, true, true, true}
		impossibleReq := &Requirements{9999999999, 999999 * time.Hour, 99999, 20, otherReqs, true, true, true}

		host, err := os.Hostname()
		if err != nil {
			log.Fatal(err)
		}

		username := internal.CachedUsername

		if host == devHost {
			// author needs to disable access to his own queues to test normal
			// behaviour
			internal.CachedUsername = "invalid"
			defer func() {
				internal.CachedUsername = username
			}()
		}

		s, err := New(ctx, "lsf", &ConfigLSF{"development", "bash", os.Getenv("WR_LSF_TEST_KEY")})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		Convey("ReserveTimeout() returns 25 seconds", func() {
			So(s.ReserveTimeout(ctx, possibleReq), ShouldEqual, 1)
		})

		impl, ok := s.impl.(*lsf)
		So(ok, ShouldBeTrue)

		// author specific tests, based on hostname, where we know what the
		// expected queue names are *** could also break out initialise() to
		// mock some textual input instead of taking it from lsadmin...
		if host == devHost {
			Convey("determineQueue() picks the best queue depending on given queues to avoid or select", func() {
				queue, err := impl.determineQueue(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long-chkpt")

				otherReqs["scheduler_queues_avoid"] = "-chkpt"
				queue, err = impl.determineQueue(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long")

				otherReqs["scheduler_queues_avoid"] = "-chkpt,parallel"
				queue, err = impl.determineQueue(&Requirements{1, 100 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "week")

				otherReqs["scheduler_queue"] = "long"
				queue, err = impl.determineQueue(&Requirements{1, 49 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long")

				otherReqs["scheduler_queue"] = "normal,long"
				queue, err = impl.determineQueue(&Requirements{1, 47 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long")

				otherReqs["scheduler_queue"] = "normal,long"
				queue, err = impl.determineQueue(&Requirements{1, 11 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")
			})

			Convey("determineQueue() picks the best queue depending on given resource requirements", func() {
				queue, err := impl.determineQueue(possibleReq)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = impl.determineQueue(&Requirements{1, 5 * time.Minute, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = impl.determineQueue(&Requirements{37000, 1 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "normal")

				queue, err = impl.determineQueue(&Requirements{1000000, 1 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "teramem")

				queue, err = impl.determineQueue(&Requirements{3000000, 1 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "hugemem")

				queue, err = impl.determineQueue(&Requirements{1, 13 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "long-chkpt")

				SkipConvey("Should be week, but we need to check 'span[hosts]' as well", func() {
					queue, err = impl.determineQueue(&Requirements{1, 167 * time.Hour, 1, 20, otherReqs, true, true, true})
					So(err, ShouldBeNil)
					So(queue, ShouldEqual, "week")
				})

				queue, err = impl.determineQueue(&Requirements{1, 361 * time.Hour, 1, 20, otherReqs, true, true, true})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "basement-chkpt")
			})

			Convey("MaxQueueTime() returns appropriate times depending on the requirements", func() {
				So(s.MaxQueueTime(possibleReq).Minutes(), ShouldEqual, 720)
				So(s.MaxQueueTime(&Requirements{1, 49 * time.Hour, 1, 20, otherReqs, true, true, true}).Minutes(),
					ShouldEqual, 10080)
			})

			Convey("determineQueue() picks the best queue for systems", func() {
				internal.CachedUsername = "isgbot"
				defer func() {
					internal.CachedUsername = username
				}()

				ssys, err := New(ctx, "lsf", &ConfigLSF{"development", "bash", os.Getenv("WR_LSF_TEST_KEY")})
				So(err, ShouldBeNil)
				So(ssys, ShouldNotBeNil)

				impl, ok = ssys.impl.(*lsf)
				So(ok, ShouldBeTrue)

				queue, err := impl.determineQueue(possibleReq)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, "system")
			})
		}

		Convey("determineQueue() returns user queue if specified", func() {
			queue, err := impl.determineQueue(specifiedReq)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, "yesterday")
		})

		Convey("generateBsubArgs() adds in user-specified options", func() {
			bsubArgs := impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)
			So(bsubArgs[9], ShouldEndWith, "[1-2]")
			bsubArgs[9] = "random1"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]",
				"-R", "avx", "-J", "random1", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			specifiedOther["scheduler_misc"] = `-R "avx foo"`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)
			bsubArgs[9] = "random2"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]",
				"-R", "avx foo", "-J", "random2", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			specifiedOther["scheduler_misc"] = `-E "also supported"`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)
			bsubArgs[9] = "random3"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]", "-E", "also supported",
				"-J", "random3", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			specifiedOther["scheduler_misc"] = `-R "select[(hname!='qpg-gpu-01') && (hname!='qpg-gpu-02')]"` +
				` -gpu "num=1:mig=2:aff=no"`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)
			bsubArgs[11] = "random4"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]", "-R",
				"select[(hname!='qpg-gpu-01') && (hname!='qpg-gpu-02')]", "-gpu", `num=1:mig=2:aff=no`,
				"-J", "random4", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			specifiedOther["scheduler_misc"] = `-R "select[(mem>d)] rusage[mem=d] span[hosts=e]"`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)
			bsubArgs[9] = "random5"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]", "-R",
				"select[(mem > 100)] rusage[mem=100] span[hosts=1]",
				"-J", "random5", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			specifiedOther["scheduler_misc"] = `((`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)
			bsubArgs[7] = "random6"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]",
				"-J", "random6", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			logMsg := ""

			testLogger.SetHandler(log15.LvlFilterHandler(log15.LvlWarn, log15.FuncHandler(func(r log15.Record) error {
				logMsg += r.Msg

				return nil
			})))

			specifiedOther["scheduler_misc"] = `-R "select[mem>100] rusage[mem=100] span[hosts=1"`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)

			So(logMsg, ShouldContainSubstring, "missing closing bracket")

			bsubArgs[7] = "random7"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]",
				"-J", "random7", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			logMsg = ""
			specifiedOther["scheduler_misc"] = `select[host="foo"]`
			bsubArgs = impl.generateBsubArgs(ctx, "yesterday", specifiedReq, "mycmd", 2)

			So(logMsg, ShouldContainSubstring, "invalid lsf bsub options")

			bsubArgs[7] = "random7"
			So(bsubArgs, ShouldResemble, []string{
				"-q", "yesterday", "-M", "100",
				"-R", "select[mem>100] rusage[mem=100] span[hosts=1]",
				"-J", "random7", "-o", "/dev/null", "-e", "/dev/null", "mycmd",
			})

			validator := make(BsubValidator)
			valid := validator.Validate(`-R "select[mem=1]"`, "anything")
			So(valid, ShouldBeTrue)

			valid = validator.Validate(`-R "select[abc=abc]"`, "anything")
			So(valid, ShouldBeFalse)
		})

		Convey("Busy() starts off false", func() {
			So(s.Busy(ctx), ShouldBeFalse)
		})

		Convey("Schedule() gives impossible error when given impossible reqs", func() {
			err := s.Schedule(ctx, "foo", impossibleReq, 0, 1)
			So(err, ShouldNotBeNil)

			var serr Error

			ok := errors.As(err, &serr)
			So(ok, ShouldBeTrue)
			So(serr.Err, ShouldEqual, ErrImpossible)
		})

		// following tests are unreliable due to needing LSF nodes to be all
		// working well and for there to be capacity to run jobs
		if os.Getenv("WR_DISABLE_UNRELIABLE_LSF_TESTS") == "true" {
			SkipConvey("Further LSF tests disabled since WR_DISABLE_UNRELIABLE_LSF_TESTS is set", func() {})

			return
		}

		Convey("Given a cmd running on a host", func() {
			testProcessNotRunning(ctx, s, possibleReq)
		})

		Convey("Schedule() lets you schedule more jobs than localhost CPUs", func() {
			// tmpdir, err := os.MkdirTemp("", "wr_schedulers_lsf_test_output_dir_")
			// if err != nil {
			// 	log.Fatal(err)
			// }
			// defer os.RemoveAll(tmpdir)

			// cmd := fmt.Sprintf("ssh %s 'perl -MFile::Temp=tempfile -e '"'"'$sleep = rand(60); select(undef, undef, $sleep); @a = tempfile(DIR => q[%s]); select(undef, undef, 5 - $sleep); exit(0);'"'"'", host, tmpdir) // sleep for a random amount of time so that ssh does not fail due to too many run at once, then ssh back to us and create a file in our tmp dir

			// the above wouldn't work due to some issue with all the ssh's not
			// working and some high proportion of the LSF jobs immediately
			// failing; instead we assume, since this is LSF, that our current
			// directory is on a shared disk, and just have all the jobs write
			// their files here directly
			startedDir, err := os.MkdirTemp("./", "wr_schedulers_lsf_test_started_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(startedDir)

			finishedDir, err := os.MkdirTemp("./", "wr_schedulers_lsf_test_finished_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(finishedDir)

			cmd := lsfMarkerCmd(startedDir, finishedDir, 10)

			count := maxCPU * 2
			err = s.Schedule(ctx, cmd, possibleReq, 0, count)
			So(err, ShouldBeNil)
			So(s.Busy(ctx), ShouldBeTrue)

			Convey("It eventually runs them all", func() {
				So(waitToFinish(ctx, s, 300, 1000), ShouldBeTrue)

				startedFiles := testDirForFiles(startedDir, count)
				So(startedFiles, ShouldEqual, count)
				finishedFiles := testDirForFiles(finishedDir, count)
				So(finishedFiles, ShouldEqual, count)
			})

			// *** no idea how to reliably test dropping the count, since I
			// don't have any way of ensuring some jobs are still pending by the
			// time I try and drop the count... unless I did something like
			// have a count of 1000000?...

			Convey("You can Schedule() again to increase the count", func() {
				newcount := count + 5
				err = s.Schedule(ctx, cmd, possibleReq, 0, newcount)
				So(err, ShouldBeNil)
				So(waitToFinish(ctx, s, 300, 1000), ShouldBeTrue)

				startedFiles := testDirForFiles(startedDir, newcount)
				So(startedFiles, ShouldEqual, newcount)
				finishedFiles := testDirForFiles(finishedDir, newcount)
				So(finishedFiles, ShouldEqual, newcount)
			})

			Convey("You can Schedule() a new job and have it run while the first is still running", func() {
				running, ok := waitForLSFRunningJobs(ctx, s, startedDir, finishedDir, count, 120*time.Second)
				So(ok, ShouldBeTrue)

				if !ok {
					return
				}

				So(running, ShouldBeBetweenOrEqual, 1, count)

				newcmd := lsfMarkerCmd(startedDir, finishedDir, 1)
				err = s.Schedule(ctx, newcmd, possibleReq, 0, 1)
				So(err, ShouldBeNil)

				So(waitToFinish(ctx, s, 300, 1000), ShouldBeTrue)

				startedFiles := testDirForFiles(startedDir, count+1)
				So(startedFiles, ShouldEqual, count+1)
				finishedFiles := testDirForFiles(finishedDir, count+1)
				So(finishedFiles, ShouldEqual, count+1)
			})
		})

		Convey("Schedule() lets you schedule more jobs than could reasonably start all at once", func() {
			startedDir, err := os.MkdirTemp("./", "wr_schedulers_lsf_test_started_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(startedDir)

			finishedDir, err := os.MkdirTemp("./", "wr_schedulers_lsf_test_finished_dir_")
			if err != nil {
				log.Fatal(err)
			}
			defer os.RemoveAll(finishedDir)

			cmd := lsfMarkerCmd(startedDir, finishedDir, 2)

			count := 10000 // 1,000,000 just errors out, and 100,000 could be bad for LSF in some way
			err = s.Schedule(ctx, cmd, possibleReq, 0, count)
			So(err, ShouldBeNil)
			So(s.Busy(ctx), ShouldBeTrue)

			Convey("It runs some of them and you can Schedule() again to drop the count", func() {
				numfiles, ok := waitForLSFStartedJobs(ctx, s, startedDir, 1, count-(maxCPU*2)-2, 120*time.Second)
				So(ok, ShouldBeTrue)

				if !ok {
					return
				}

				newcount := numfiles + maxCPU
				err = s.Schedule(ctx, cmd, possibleReq, 0, newcount)
				So(err, ShouldBeNil)

				So(waitToFinish(ctx, s, 300, 1000), ShouldBeTrue)

				startedFiles := testDirForFiles(startedDir, newcount)
				So(startedFiles, ShouldBeGreaterThanOrEqualTo, newcount)
				finishedFiles := testDirForFiles(finishedDir, newcount)
				So(finishedFiles, ShouldBeGreaterThanOrEqualTo, newcount)
			})
		})

		// wait a while for any remaining jobs to finish
		So(waitToFinish(ctx, s, 300, 1000), ShouldBeTrue)
	})
}

func TestLSFQueueSelection(t *testing.T) {
	Convey("Given an lsf scheduler with parsed queues", t, func() {
		// preferred order (as the scheduler would rank them), and per-queue
		// memlimit (MB) and runlimit (seconds); 0 means unlimited.
		s := &lsf{
			sortedqs: []string{"normal", "long", "hugemem", "basement"},
			queues: map[string]map[string]int{
				"normal":   {memlimitKey: 36000, runlimitKey: 12 * 60 * 60},
				"long":     {memlimitKey: 36000, runlimitKey: 720 * 60 * 60},
				"hugemem":  {memlimitKey: 3000000, runlimitKey: 720 * 60 * 60},
				"basement": {memlimitKey: 3000000, runlimitKey: 0},
			},
		}

		noOther := make(map[string]string)

		Convey("a small, short job goes to the first (most-preferred) suitable queue", func() {
			q, err := s.determineQueue(&Requirements{100, 1 * time.Minute, 1, 20, noOther, true, true, true})
			So(err, ShouldBeNil)
			So(q, ShouldEqual, "normal")
		})

		Convey("a long-running job skips queues whose runlimit is too low", func() {
			q, err := s.determineQueue(&Requirements{100, 100 * time.Hour, 1, 20, noOther, true, true, true})
			So(err, ShouldBeNil)
			So(q, ShouldEqual, "long")
		})

		Convey("a high-memory job skips queues whose memlimit is too low", func() {
			q, err := s.determineQueue(&Requirements{100000, 1 * time.Minute, 1, 20, noOther, true, true, true})
			So(err, ShouldBeNil)
			So(q, ShouldEqual, "hugemem")
		})

		Convey("a queue can be explicitly requested", func() {
			q, err := s.determineQueue(&Requirements{100, 1 * time.Minute, 1, 20,
				map[string]string{"scheduler_queue": "long"}, true, true, true})
			So(err, ShouldBeNil)
			So(q, ShouldEqual, "long")
		})

		Convey("queues can be avoided by substring", func() {
			q, err := s.determineQueue(&Requirements{100, 1 * time.Minute, 1, 20,
				map[string]string{"scheduler_queues_avoid": "normal,long"}, true, true, true})
			So(err, ShouldBeNil)
			So(q, ShouldEqual, "hugemem")
		})

		Convey("an impossible job (more memory than any queue allows) errors", func() {
			_, err := s.determineQueue(&Requirements{9999999999, 1 * time.Minute, 1, 20, noOther, true, true, true})
			So(err, ShouldNotBeNil)
		})
	})
}

// TestLSFReservedElements covers the C3 acceptance tests (never bkill a reserved
// LSF array element) as pure-function tests, needing no real LSF.
func TestLSFReservedElements(t *testing.T) {
	Convey("Given a killCollector over maxAllowed with a reserved element recorded", t, func() {
		// reAid matches the [index] suffix that killableID uses to build the
		// jobid[index] killable id from a bjobs job id + job name.
		reAid := regexp.MustCompile(`\[(\d+)\]$`)
		kc := &killCollector{
			reAid:      reAid,
			toKill:     []string{"-b"},
			maxAllowed: 1,
			reserved:   map[string]bool{"12345[7]": true},
		}

		// first element (RUN) fills the single allowed slot.
		kc.consider("100", "RUN", "wrname[1]")

		// the reserved element is PEND (non-RUN, normally killable as excess)...
		kc.consider("12345", "PEND", "wrname[7]")

		// ...as is an unreserved excess element.
		kc.consider("200", "PEND", "wrname[8]")

		Convey("the reserved element is protected while the unreserved excess is killed", func() {
			So(kc.toKill, ShouldNotContain, "12345[7]")
			So(kc.toKill, ShouldContain, "200[8]")
		})
	})

	Convey("Given an lsf scheduler with a reserved set", t, func() {
		s := &lsf{
			reservedElements: map[string]bool{
				"12345[7]": true,
				"12345[8]": true,
				"99999[1]": true,
			},
		}

		Convey("pruneReserved drops ids absent from a subsequent full bjobs snapshot", func() {
			// 12345[7] has exited so parseBjobs no longer reports it.
			present := map[string]bool{"12345[8]": true, "99999[1]": true}
			s.pruneReserved(present)

			So(s.reservedElements, ShouldNotContainKey, "12345[7]")
			So(s.reservedElements, ShouldContainKey, "12345[8]")
			So(s.reservedElements, ShouldContainKey, "99999[1]")
			So(len(s.reservedElements), ShouldEqual, 2)
		})
	})

	Convey("Given a non-LSF scheduler", t, func() {
		ctx := context.Background()
		s, err := New(ctx, "local", &ConfigLocal{testShell, time.Second, 0, 0})
		So(err, ShouldBeNil)
		So(s, ShouldNotBeNil)

		Convey("Reserved() is a no-op and does not panic", func() {
			So(func() { s.Reserved("12345[7]") }, ShouldNotPanic)
		})
	})
}

func lsfMarkerCmd(startDir, finishDir string, sleepSeconds int) string {
	return fmt.Sprintf(
		"perl -MFile::Temp=tempfile -e '@a = tempfile(DIR => q[%s]); sleep(%d); @b = tempfile(DIR => q[%s]); exit(0);'",
		startDir, sleepSeconds, finishDir,
	)
}

func waitForLSFStartedJobs(ctx context.Context, s *Scheduler, startDir string, minStarted, maxStarted int, maxWait time.Duration) (int, bool) {
	started := 0
	ok := pollUntilFor(maxWait, time.Second, func() bool {
		started = testDirForFiles(startDir, minStarted)

		return started >= minStarted && started <= maxStarted && s.Busy(ctx)
	})

	return started, ok
}

func waitForLSFRunningJobs(ctx context.Context, s *Scheduler, startDir, finishDir string, maxStarted int, maxWait time.Duration) (int, bool) {
	started := 0
	ok := pollUntilFor(maxWait, time.Second, func() bool {
		started = testDirForFiles(startDir, 1)
		finished := testDirForFiles(finishDir, started)

		return started > finished && started <= maxStarted && s.Busy(ctx)
	})

	return started, ok
}

func TestLSFArrayChunking(t *testing.T) {
	ctx := context.Background()
	req := &Requirements{RAM: 100, Time: time.Minute, Cores: 1, Other: map[string]string{}}

	Convey("Given an lsf scheduler with fake LSF exes and a small max array size", t, func() {
		dir := t.TempDir()
		jArgsFile := filepath.Join(dir, "jargs")
		s := newFakeLSFScheduler(t, dir, jArgsFile, fakeLSFDelays{})

		origMax := maxBsubArraySize

		maxBsubArraySize = 1000
		defer func() { maxBsubArraySize = origMax }()

		Convey("scheduling a count far above the cap splits into capped, unique arrays", func() {
			const count = 160000

			err := s.schedule(ctx, "false", req, 0, count)
			So(err, ShouldBeNil)

			names, sizes := parseJArrays(t, jArgsFile)

			// (a) no single array exceeds the cap.
			maxSeen := 0
			total := 0

			for _, n := range sizes {
				if n > maxSeen {
					maxSeen = n
				}

				total += n
			}

			So(maxSeen, ShouldBeLessThanOrEqualTo, maxBsubArraySize)

			// (b) the arrays' sizes sum to the needed count.
			So(total, ShouldEqual, count)

			// number of arrays is ceil(count/cap).
			So(len(sizes), ShouldEqual, (count+maxBsubArraySize-1)/maxBsubArraySize)

			// (c) each array name is unique and correlated to the cmd (shares the
			// non-unique cmd prefix that checkCmd/killExcessCmds filter on).
			prefix := jobName("false", "development", false)
			seen := make(map[string]bool)
			nonUnique := 0
			badPrefix := 0

			for _, name := range names {
				if seen[name] {
					nonUnique++
				}

				seen[name] = true

				if !strings.HasPrefix(name, prefix) {
					badPrefix++
				}
			}

			So(nonUnique, ShouldEqual, 0)
			So(badPrefix, ShouldEqual, 0)
			So(len(seen), ShouldEqual, len(sizes))
		})
	})

	Convey("Given an lsf scheduler whose bsub hangs", t, func() {
		dir := t.TempDir()
		jArgsFile := filepath.Join(dir, "jargs")
		s := newFakeLSFScheduler(t, dir, jArgsFile, fakeLSFDelays{sleepSecs: 30})

		origTimeout := bsubExecTimeout

		bsubExecTimeout = 300 * time.Millisecond
		defer func() { bsubExecTimeout = origTimeout }()

		// the fake bsub's `sleep 30` inherits the stdout pipe and outlives the
		// bsub the timeout kills, so what bounds the return is the exec timeout
		// plus the pipe-close grace; the shipped grace is a minute (see
		// bsubPipeCloseGrace), so lower it too to keep this fast.
		setPipeCloseGraces(t, testPipeCloseGrace)

		Convey("schedule returns a retryable error bounded by the exec timeout", func() {
			start := time.Now()
			err := s.schedule(ctx, "false", req, 0, 1)
			elapsed := time.Since(start)

			So(err, ShouldNotBeNil)
			So(elapsed, ShouldBeLessThan, 10*time.Second)
		})
	})
}

// newFakeLSFScheduler builds an *lsf wired to fake bsub/bjobs/bkill executables
// written into dir, so schedule() can be driven without a real LSF. The fake
// bsub appends the value of each -J argument (one per line) to jArgsFile, prints
// a parseable "Job <id>" line and exits 0, held up as delays asks.
func newFakeLSFScheduler(t *testing.T, dir, jArgsFile string, delays fakeLSFDelays) *lsf {
	t.Helper()

	sleep := ""
	if delays.sleepSecs > 0 {
		sleep = fmt.Sprintf("sleep %d\n", delays.sleepSecs)
	}

	linger := ""
	if delays.lingerSecs > 0 {
		linger = fmt.Sprintf("( sleep %d ) &\n", delays.lingerSecs)
	}

	bsubExe := filepath.Join(dir, "bsub")
	writeFakeExe(t, bsubExe, fmt.Sprintf(`#!/bin/bash
%scapture=0
for a in "$@"; do
  if [ "$capture" = "1" ]; then echo "$a" >> %q; capture=0; fi
  if [ "$a" = "-J" ]; then capture=1; fi
done
echo "Job <321>"
%sexit 0
`, sleep, jArgsFile, linger))

	appearSleep := ""
	if delays.bjobsAppearSleepSecs > 0 {
		appearSleep = fmt.Sprintf("  sleep %d\n", delays.bjobsAppearSleepSecs)
	}

	// bjobs is called both as `bjobs -w` (list, reporting delays.bjobsListJobs
	// jobs of the "false" cmd, so by default the scheduler thinks 0 are already
	// scheduled) and as `bjobs -w <id>` (the post-submit appearance check, which
	// must report a long-enough line).
	bjobsExe := filepath.Join(dir, "bjobs")
	writeFakeExe(t, bjobsExe, `#!/bin/bash
if [ -n "$2" ]; then
`+appearSleep+`  echo "$2 sb10 RUN normal host1 host2 fakejobname000000000000000 Jul 22 12:00"
  exit 0
fi
`+fakeBjobsListBody(delays))

	bkillExe := filepath.Join(dir, "bkill")
	writeFakeExe(t, bkillExe, "#!/bin/bash\nexit 0\n")

	s := &lsf{
		config:             &ConfigLSF{Deployment: "development", Shell: "bash"},
		bsubExe:            bsubExe,
		bjobsExe:           bjobsExe,
		bkillExe:           bkillExe,
		memLimitMultiplier: 1,
		sortedqs:           []string{"normal"},
		queues:             map[string]map[string]int{"normal": {memlimitKey: 0, runlimitKey: 0}},
	}
	s.setupMonthsAndRegexes()

	return s
}

func writeFakeExe(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0700); err != nil { //nolint:gosec
		t.Fatal(err)
	}
}

// parseJArrays reads the recorded -J arguments and returns, per array, the
// name (portion before any [1-N]) and the element count N (1 when there is no
// [1-N] suffix).
func parseJArrays(t *testing.T, jArgsFile string) (names []string, sizes []int) {
	t.Helper()

	data, err := os.ReadFile(jArgsFile)
	if err != nil {
		t.Fatal(err)
	}

	re := regexp.MustCompile(`^(.+)\[1-(\d+)\]$`)

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}

		name, size := parseJArray(t, re, line)
		names = append(names, name)
		sizes = append(sizes, size)
	}

	return names, sizes
}

// parseJArray parses a single recorded -J argument into its name and element
// count (1 when there is no [1-N] suffix).
func parseJArray(t *testing.T, re *regexp.Regexp, line string) (string, int) {
	t.Helper()

	m := re.FindStringSubmatch(line)
	if len(m) != 3 {
		return line, 1
	}

	n, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatal(err)
	}

	return m[1], n
}

// TestLSFJobNamePrefix pins that the job-name prefix the LSF checkCmd full-scan
// and cleanup() paths scan (and bkill) for is derived from the same source as
// jobName, so it is WR_JOBNAME_TOKEN-inclusive and can never drift into matching
// a *different* deployment's jobs. A legacy, un-tokenised cleanup prefix would
// both miss this manager's own namespaced jobs and match (and bkill) another
// deployment's jobs - the cross-deployment kill the token exists to prevent.
// See jobNamePrefix in scheduler.go and .docs/bugfixes/260727-1.md.
func TestLSFJobNamePrefix(t *testing.T) {
	const deployment = "production"

	Convey("The shared job-name scan prefix is consistent with jobName", t, func() {
		Convey("Without WR_JOBNAME_TOKEN it is the legacy wr<initial>_ prefix", func() {
			t.Setenv("WR_JOBNAME_TOKEN", "")

			prefix := jobNamePrefix(deployment)

			So(prefix, ShouldEqual, "wrp_")
			So(jobName("anycmd", deployment, false), ShouldStartWith, prefix)
		})

		Convey("With WR_JOBNAME_TOKEN set it namespaces the prefix with the token", func() {
			t.Setenv("WR_JOBNAME_TOKEN", "iso42")

			prefix := jobNamePrefix(deployment)

			// the scan prefix (used by both cleanup() and the checkCmd
			// full-scan) must include the token so it matches jobName's
			// namespaced output, i.e. this manager's OWN jobs...
			So(prefix, ShouldEqual, "wrpiso42_")
			So(jobName("anycmd", deployment, false), ShouldStartWith, prefix)

			// ...and must NOT be the legacy wr<initial>_ prefix, which would
			// instead match a different (e.g. production) deployment's jobs.
			So(jobName("anycmd", deployment, false), ShouldNotStartWith, "wrp_")
		})
	})
}
