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
	"os/exec"
	"strconv"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

// static errors standing in for the problems Execute accumulates.
var (
	errTestUploadFailed   = errors.New("unmounting also caused problem(s): failed to upload 1 files")
	errTestBehaviour      = errors.New("cleanup failed")
	errTestStderrHandling = errors.New("disk full")
	errTestCmdNotFound    = errors.New("command not found")
	errTestCmdExited      = errors.New("command exited with code 1")
	errTestCmdAbnormal    = errors.New("command failed to complete normally")
	errTestCmdKilled      = errors.New("command was killed")
)

func TestExecProblemReporting(t *testing.T) {
	Convey("Given a job's stderr", t, func() {
		stderr := []byte("cmd output")

		Convey("a behaviour problem is reported even when the job succeeded", func() {
			final := appendExecProblems(stderr, false, "", errTestBehaviour, nil)

			So(string(final), ShouldContainSubstring, "Behaviour problems:\ncleanup failed")
		})

		Convey("mount logs are reported when the job failed", func() {
			final := appendExecProblems(stderr, true, "could not upload out.txt", nil, nil)

			So(string(final), ShouldContainSubstring, "Mount logs:\ncould not upload out.txt")
		})

		Convey("mount logs are not reported when the job succeeded", func() {
			final := appendExecProblems(stderr, false, "uploaded out.txt", nil, nil)

			So(string(final), ShouldEqual, "cmd output")
		})

		Convey("a stderr handling problem is always reported", func() {
			final := appendExecProblems(stderr, false, "", nil, errTestStderrHandling)

			So(string(final), ShouldContainSubstring, "STDERR handling problems:\ndisk full")
		})
	})
}

// execOutcomeRow is one verdict classifyExecOutcome really reaches about a
// finished command, to be combined with the verdict of a clean unmount and of one
// that could not upload the Job's output.
//
// The upload failure is the only thing that can make a Job whose command
// SUCCEEDED need running again, and the only thing whose loss silently archives
// a Job whose output no longer exists anywhere: the cache the output was in is
// deleted at unmount regardless. So the combination must never archive unless
// both halves are happy, and must never drop a bury or a release the command
// itself earned.
//
// Two of these shapes carry dobury AND dorelease at once (classifyReleasedExit
// starts every shape as a release and some arms then bury as well), which is
// exactly the pairing a combination reading one flag alone would get wrong.
type execOutcomeRow struct {
	name string
	cmd  execOutcome
}

func execOutcomeRows() []execOutcomeRow {
	return []execOutcomeRow{
		{name: "exited zero", cmd: execOutcome{doarchive: true}},
		{
			name: "exited with a code that buries",
			cmd: execOutcome{
				myerr: errTestCmdNotFound, failreason: FailReasonCFound,
				exitcode: exitCodeCommandNotFound, dobury: true,
			},
		},
		{
			name: "exited non-zero, to be retried",
			cmd: execOutcome{
				myerr: errTestCmdExited, failreason: FailReasonExit, exitcode: 1, dorelease: true,
			},
		},
		{
			name: "failed to complete normally",
			cmd: execOutcome{
				myerr: errTestCmdAbnormal, failreason: FailReasonAbnormal,
				exitcode: exitCodeAbnormal, dorelease: true,
			},
		},
		{
			name: "was killed, which releases AND buries",
			cmd: execOutcome{
				myerr: errTestCmdKilled, failreason: FailReasonKilled,
				exitcode: 1, dorelease: true, dobury: true,
			},
		},
		{
			name: "exited non-zero past its noretries time, which releases AND buries",
			cmd: execOutcome{
				myerr: errTestCmdExited, failreason: FailReasonExit,
				exitcode: 1, dorelease: true, dobury: true,
			},
		},
	}
}

func TestExecOutcomeCombination(t *testing.T) {
	Convey("Given a command that finished", t, func() {
		for _, row := range execOutcomeRows() {
			Convey("one that "+row.name, func() {
				Convey("keeps its own verdict when the unmount was clean", func() {
					So(combineExecOutcomes(execOutcome{}, row.cmd), ShouldResemble, row.cmd)
				})

				Convey("never archives, and keeps any bury or release, when the output did not upload", func() {
					final := combineExecOutcomes(uploadFailedOutcome(errTestUploadFailed), row.cmd)

					So(final.doarchive, ShouldBeFalse)
					So(final.dobury, ShouldEqual, row.cmd.dobury)
					So(final.dorelease, ShouldEqual, row.cmd.dorelease || row.cmd.doarchive)

					if row.cmd.doarchive {
						So(final.failreason, ShouldEqual, FailReasonUpload)
						So(final.exitcode, ShouldEqual, exitCodeUploadFailure)
						So(final.myerr, ShouldEqual, errTestUploadFailed)

						return
					}

					So(final.failreason, ShouldEqual, row.cmd.failreason)
					So(final.exitcode, ShouldEqual, row.cmd.exitcode)
					So(final.myerr, ShouldEqual, row.cmd.myerr)
				})
			})
		}
	})
}

// uploadFailedOutcome is the verdict Execute reaches when unmounting a job's
// writable mount could not upload the job's output.
func uploadFailedOutcome(myerr error) execOutcome {
	return execOutcome{
		myerr:      myerr,
		failreason: FailReasonUpload,
		exitcode:   exitCodeUploadFailure,
		dorelease:  true,
	}
}

func TestExecOutcomeContainerStartFailure(t *testing.T) {
	Convey("Given commands that exited with a container runtime's start-failure code", t, func() {
		c := &Client{}

		Convey("a docker job exiting 125 failed because its container did not start", func() {
			out := classifyTestExit(c, &Job{WithDocker: testContainerImage}, dockerStartFailureExitCode)

			So(out.failreason, ShouldEqual, FailReasonContainer)
			So(out.exitcode, ShouldEqual, dockerStartFailureExitCode)
			So(out.dorelease, ShouldBeTrue)
			So(out.dobury, ShouldBeFalse)
		})

		Convey("a singularity job exiting 255 failed because its container did not start", func() {
			out := classifyTestExit(c, &Job{WithSingularity: testContainerImage}, singularityStartFailureExitCode)

			So(out.failreason, ShouldEqual, FailReasonContainer)
			So(out.dorelease, ShouldBeTrue)
		})

		Convey("a job with both images is docker's, as it runs under docker", func() {
			both := &Job{WithDocker: testContainerImage, WithSingularity: testContainerImage}

			So(classifyTestExit(c, both, dockerStartFailureExitCode).failreason, ShouldEqual, FailReasonContainer)
			So(classifyTestExit(c, both, singularityStartFailureExitCode).failreason, ShouldEqual, FailReasonExit)
		})

		Convey("the other runtime's code, or a job with no container, is an ordinary non-zero exit", func() {
			So(classifyTestExit(c, &Job{WithDocker: testContainerImage}, singularityStartFailureExitCode).failreason,
				ShouldEqual, FailReasonExit)
			So(classifyTestExit(c, &Job{WithSingularity: testContainerImage}, dockerStartFailureExitCode).failreason,
				ShouldEqual, FailReasonExit)
			So(classifyTestExit(c, &Job{}, dockerStartFailureExitCode).failreason, ShouldEqual, FailReasonExit)
			So(classifyTestExit(c, &Job{}, singularityStartFailureExitCode).failreason, ShouldEqual, FailReasonExit)
		})
	})
}

// testContainerImage stands in for a container image; nothing runs it.
const testContainerImage = "image"

// classifyTestExit runs a real process that exits with code, and classifies its
// ending as Execute would for job.
func classifyTestExit(c *Client, job *Job, code int) execOutcome {
	job.Cmd = "exit " + strconv.Itoa(code)
	job.Requirements = &scheduler.Requirements{RAM: 1}

	// job.Cmd is "exit <int>", built above.
	cmd := exec.CommandContext(context.Background(), "sh", "-c", job.Cmd) //nolint:gosec
	err := cmd.Run()
	So(err, ShouldNotBeNil)

	return c.classifyExecOutcome(execOutcomeInput{
		err: err, cmd: cmd, job: job, serverContact: &serverContactState{},
	})
}
