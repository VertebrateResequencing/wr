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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// testOtherCwdPath is a second fake absolute Cwd, for a Job that is not the one
// a JobEssence naming testCwdPath is describing.
const testOtherCwdPath = "/other"

func TestCandidateJobSelection(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// A JobEssence with a Cwd describes 2 possible Jobs (see candidateKeys()),
	// so the server can return 2 Jobs in either order, and picking the wrong one
	// would kill, remove or retry a Job the user did not name.
	Convey("Given a JobEssence with a Cwd, and the 2 Jobs it could describe", t, func() {
		je := &JobEssence{Cmd: testTrueCmd, Cwd: testCwdPath}
		cwdMatters := &Job{Cmd: testTrueCmd, Cwd: testCwdPath, CwdMatters: true}
		cwdDoesNot := &Job{Cmd: testTrueCmd, Cwd: testCwdPath}

		Convey("the CwdMatters Job is picked whichever order the server lists them in", func() {
			So(je.pickCandidateJob([]*Job{cwdMatters, cwdDoesNot}), ShouldEqual, cwdMatters)
			So(je.pickCandidateJob([]*Job{cwdDoesNot, cwdMatters}), ShouldEqual, cwdMatters)
		})

		Convey("the other Job is picked when it is the only one, since its Cwd is the requested one", func() {
			So(je.pickCandidateJob([]*Job{cwdDoesNot}), ShouldEqual, cwdDoesNot)
		})

		Convey("but a Job with the same key and a different Cwd is not picked", func() {
			So(je.pickCandidateJob([]*Job{{Cmd: testTrueCmd, Cwd: testOtherCwdPath}}), ShouldBeNil)
		})

		Convey("and no Job is picked out of no Jobs", func() {
			So(je.pickCandidateJob(nil), ShouldBeNil)
		})
	})
}
