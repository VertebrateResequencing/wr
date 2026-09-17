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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// testOtherCwdPath is a second fake absolute Cwd, for a Job that is not the one
// a JobEssence naming testCwdPath is describing.
const testOtherCwdPath = "/other"

const (
	// candidateMattersCmd is the command of the Job added with CwdMatters,
	// candidatePlainCmd that of the Job added without it, candidateElsewhereCmd
	// that of a Job in a different Cwd, and candidateUnknownCmd one no Job was
	// ever added with.
	candidateMattersCmd   = "echo candidate cwd matters"
	candidatePlainCmd     = "echo candidate cwd does not matter"
	candidateElsewhereCmd = "echo candidate cwd elsewhere"
	candidateUnknownCmd   = "echo candidate never added"

	candidateRepGroup = "candidate-keys"
)

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

// TestGetByEssencesCandidateKeys covers the plural read path that a file of
// commands reaches (`wr status -f`, and the kill/remove/retry/suspend selection
// built on it), where each JobEssence describes its command rather than carrying
// a precomputed key: such an essence must resolve the same Job the
// single-command path resolves, and no other.
func TestGetByEssencesCandidateKeys(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given jobs added with and without CwdMatters", t, func() {
		d := dgrStartServer(ctx)

		defer d.stop(ctx)

		jq := d.connect()

		defer disconnect(jq)

		matters := d.job(candidateMattersCmd, candidateRepGroup)
		matters.CwdMatters = true
		plain := d.job(candidatePlainCmd, candidateRepGroup)
		// elsewhere has no CwdMatters, so its key is one of the candidate keys of
		// an essence naming its Cmd and ANY Cwd: only the Cwd check in
		// pickCandidateJob keeps it out of the answers below.
		elsewhere := d.job(candidateElsewhereCmd, candidateRepGroup)
		elsewhere.Cwd = testOtherCwdPath

		dgrAddJobs(jq, []*Job{matters, plain, elsewhere})

		Convey("GetByEssences resolves each of them from its Cmd and Cwd alone", func() {
			jobs, err := jq.GetByEssences([]*JobEssence{
				{Cmd: candidateMattersCmd, Cwd: testCwd},
				{Cmd: candidatePlainCmd, Cwd: testCwd},
			})
			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 2)
			So(jobs[0].Key(), ShouldEqual, matters.Key())
			So(jobs[1].Key(), ShouldEqual, plain.Key())
		})

		Convey("GetByEssences does not resolve a job of another Cwd", func() {
			jobs, err := jq.GetByEssences([]*JobEssence{{Cmd: candidateElsewhereCmd, Cwd: testCwd}})
			So(err, ShouldBeNil)
			So(jobs, ShouldBeEmpty)
		})

		Convey("GetByEssences returns one job per essence that resolves, in order", func() {
			jobs, err := jq.GetByEssences([]*JobEssence{
				{Cmd: candidateUnknownCmd, Cwd: testCwd},
				{Cmd: candidatePlainCmd, Cwd: testCwd},
			})
			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 1)
			So(jobs[0].Key(), ShouldEqual, plain.Key())
		})

		Convey("GetByEssences still resolves an essence that carries a JobKey", func() {
			jobs, err := jq.GetByEssences([]*JobEssence{matters.ToEssense(), plain.ToEssense()})
			So(err, ShouldBeNil)
			So(jobs, ShouldHaveLength, 2)
			So(jobs[0].Key(), ShouldEqual, matters.Key())
			So(jobs[1].Key(), ShouldEqual, plain.Key())
		})
	})
}
