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

// wr always runs bkill with -b, and on farm22 (LSF 10.1) bkill -b does not
// report per element the way plain bkill does ("Job <id>: Job has already
// finished"). It prints ONE line on stderr for the whole invocation:
//
//   - "The requested operation is in progress." with exit status 0 if it
//     accepted ANY of the ids it was given for killing (a running or pending
//     element), however many of the others were already finished or unknown;
//   - otherwise, with exit status 255, "Job has already finished" if every id
//     was a finished job, or "No matching job found" if any id was unknown to
//     LSF.
//
// So an exit status of 255 with nothing but one of those id-less lines means
// none of the ids was a live job LSF could kill: every one of them was already
// gone.

import (
	"os/exec"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const bkillAggregateElements = 5

func TestBkillAggregateOutput(t *testing.T) {
	Convey("killSummary.account, given bkill -b's single id-less report", t, func() {
		var k killSummary

		exit255 := bkillExitError(t)

		Convey("credits every element as already gone when all were finished", func() {
			k.account(bkillAggregateIDs(), "Job has already finished\n", exit255)

			So(k.alreadyGone, ShouldEqual, bkillAggregateElements)
			So(k.unaccounted, ShouldEqual, 0)
			So(k.killed, ShouldEqual, 0)
			So(k.needsAttention(), ShouldBeFalse)
		})

		Convey("credits every element as already gone when some were unknown to LSF", func() {
			k.account(bkillAggregateIDs(), "No matching job found\n", exit255)

			So(k.alreadyGone, ShouldEqual, bkillAggregateElements)
			So(k.unaccounted, ShouldEqual, 0)
		})

		Convey("credits nothing as gone if bkill exited with another non-zero status", func() {
			exit1 := exec.CommandContext(t.Context(), "sh", "-c", "exit 1").Run()
			So(exit1, ShouldNotBeNil)

			k.account(bkillAggregateIDs(), "Job has already finished\n", exit1)

			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, bkillAggregateElements)
		})

		Convey("credits nothing as gone if bkill also said something else", func() {
			k.account(bkillAggregateIDs(), "Job has already finished\nUser permission denied\n", exit255)

			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, bkillAggregateElements)
		})

		Convey("credits nothing as gone if bkill rejected an id", func() {
			k.account(bkillAggregateIDs(), "5[x: Illegal job ID.\n", exit255)

			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, bkillAggregateElements)
		})

		Convey("credits nothing as gone if bkill was killed before it finished", func() {
			k.account(bkillAggregateIDs(), "Job has already finished\n", bkillSignalledError(t))

			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, bkillAggregateElements)
		})

		Convey("still credits per-element reports as before", func() {
			k.account(bkillAggregateIDs(), "Job <5[1-3:1]>: Job has already finished\n", exit255)

			So(k.alreadyGone, ShouldEqual, 3)
			So(k.unaccounted, ShouldEqual, 2)
		})

		Convey("counts the elements of an accepted request as killed", func() {
			k.account(bkillAggregateIDs(), "The requested operation is in progress.\n", nil)

			So(k.killed, ShouldEqual, bkillAggregateElements)
			So(k.alreadyGone, ShouldEqual, 0)
			So(k.unaccounted, ShouldEqual, 0)
		})
	})
}

// bkillExitError returns the real error exec returns for a command that exited
// with status 255, as bkill does when it does not accept a kill request.
func bkillExitError(t *testing.T) error {
	t.Helper()

	err := exec.CommandContext(t.Context(), "sh", "-c", "exit 255").Run()
	if err == nil {
		t.Fatal("expected a non-zero exit")
	}

	return err
}

func bkillAggregateIDs() []string {
	return []string{"5[1]", "5[2]", "5[3]", "8", "9[4]"}
}

// bkillSignalledError returns the real error exec returns for a command that
// was killed by a signal, as a bkill that hits bkillExecTimeout is.
func bkillSignalledError(t *testing.T) error {
	t.Helper()

	err := exec.CommandContext(t.Context(), "sh", "-c", "kill -9 $$").Run()
	if err == nil {
		t.Fatal("expected a signalled exit")
	}

	return err
}
