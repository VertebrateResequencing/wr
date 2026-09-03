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
	"errors"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
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

func TestExecOutcomeCombination(t *testing.T) {
	Convey("Given a command that exited zero", t, func() {
		cmdWorked := execOutcome{doarchive: true}

		Convey("its job is archived when its mounts unmounted cleanly", func() {
			final := combineExecOutcomes(execOutcome{}, cmdWorked)

			So(final.doarchive, ShouldBeTrue)
			So(final.dorelease, ShouldBeFalse)
			So(final.dobury, ShouldBeFalse)
			So(final.failreason, ShouldBeBlank)
			So(final.exitcode, ShouldEqual, 0)
			So(final.myerr, ShouldBeNil)
		})

		Convey("its job is not archived when its output failed to upload", func() {
			final := combineExecOutcomes(uploadFailedOutcome(errTestUploadFailed), cmdWorked)

			So(final.doarchive, ShouldBeFalse)
			So(final.dorelease, ShouldBeTrue)
			So(final.failreason, ShouldEqual, FailReasonUpload)
			So(final.exitcode, ShouldEqual, exitCodeUploadFailure)
			So(final.myerr, ShouldEqual, errTestUploadFailed)
		})
	})

	Convey("Given a command that failed", t, func() {
		Convey("an upload failure does not replace its bury reason", func() {
			cmdBuried := execOutcome{
				myerr: errTestCmdNotFound, failreason: FailReasonCFound,
				exitcode: exitCodeCommandNotFound, dobury: true,
			}

			final := combineExecOutcomes(uploadFailedOutcome(errTestUploadFailed), cmdBuried)

			So(final.dobury, ShouldBeTrue)
			So(final.dorelease, ShouldBeFalse)
			So(final.doarchive, ShouldBeFalse)
			So(final.failreason, ShouldEqual, FailReasonCFound)
			So(final.exitcode, ShouldEqual, exitCodeCommandNotFound)
		})

		Convey("an upload failure does not replace its release reason", func() {
			cmdReleased := execOutcome{
				myerr: errTestCmdExited, failreason: FailReasonExit,
				exitcode: 1, dorelease: true,
			}

			final := combineExecOutcomes(uploadFailedOutcome(errTestUploadFailed), cmdReleased)

			So(final.dorelease, ShouldBeTrue)
			So(final.doarchive, ShouldBeFalse)
			So(final.failreason, ShouldEqual, FailReasonExit)
			So(final.exitcode, ShouldEqual, 1)
		})
	})
}

// static errors standing in for the problems Execute accumulates.
var (
	errTestUploadFailed   = errors.New("unmounting also caused problem(s): failed to upload 1 files")
	errTestBehaviour      = errors.New("cleanup failed")
	errTestStderrHandling = errors.New("disk full")
	errTestCmdNotFound    = errors.New("command not found")
	errTestCmdExited      = errors.New("command exited with code 1")
)

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
