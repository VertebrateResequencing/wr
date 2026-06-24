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

package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

func TestResumeCommand(t *testing.T) {
	Convey("wr resume handles suspended jobs selected by report group", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			one := newQueueCommandJob("echo resume rg one", "rg-resume", reqs)
			two := newQueueCommandJob("echo resume rg two", "rg-resume", reqs)
			addQueueCommandJobs(jq, one, two)
			suspendQueueCommandJobs(jq, one, two)

			output, err := runResumeForTest(t, "-i", "rg-resume")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 2 suspended commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateReady)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume supports report group substring matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			one := newQueueCommandJob("echo resume team b one", "team-b-1", reqs)
			two := newQueueCommandJob("echo resume team b two", "team-b-2", reqs)
			addQueueCommandJobs(jq, one, two)
			suspendQueueCommandJobs(jq, one, two)

			output, err := runResumeForTest(t, "-i", "team-b", "-z")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 2 suspended commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateReady)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume supports internal job id matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo resume internal", "rg-resume-internal", reqs)
			addQueueCommandJobs(jq, job)
			suspendQueueCommandJobs(jq, job)

			output, err := runResumeForTest(t, "-i", job.Key(), "-y")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume supports command file matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			cmdPath := filepath.Join(t.TempDir(), "commands.txt")
			err := os.WriteFile(cmdPath, []byte("echo resume file one\necho resume file two\n"), 0o600)
			So(err, ShouldBeNil)
			configureQueueCommandFileSelection(t, cmdPath)

			one := newQueueCommandJob("echo resume file one", "rg-resume-file", reqs)
			two := newQueueCommandJob("echo resume file two", "rg-resume-file", reqs)
			addQueueCommandJobs(jq, one, two)
			suspendQueueCommandJobs(jq, one, two)

			output, err := runResumeForTest(t, "-f", cmdPath)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 2 suspended commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateReady)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume supports command line and cwd matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			cwd := filepath.Join(t.TempDir(), "wr207")
			job := newQueueCommandJob("echo by-line", "rg-resume-line", reqs)
			job.Cwd = cwd
			job.CwdMatters = true
			addQueueCommandJobs(jq, job)
			suspendQueueCommandJobs(jq, job)

			output, err := runResumeForTest(t, "-l", "echo by-line", "-c", cwd)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume -a selects all suspended jobs", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			one := newQueueCommandJob("echo resume all one", "rg-resume-all", reqs)
			two := newQueueCommandJob("echo resume all two", "rg-resume-all", reqs)
			addQueueCommandJobs(jq, one, two)
			suspendQueueCommandJobs(jq, one, two)

			output, err := runResumeForTest(t, "-a")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 2 suspended commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateReady)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume restores a suspended child to dependent when its parent is incomplete", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			parent := newQueueCommandJob("echo resume parent", "rg-resume-parent", reqs)
			child := newQueueCommandJob("echo resume child", "rg-resume-child", reqs)
			child.Dependencies = jobqueue.Dependencies{jobqueue.NewEssenceDependency(parent.Cmd, "")}
			addQueueCommandJobs(jq, parent, child)
			suspendQueueCommandJobs(jq, child)

			output, err := runResumeForTest(t, "-i", "rg-resume-child")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, child), ShouldEqual, jobqueue.JobStateDependent)
		})
	})

	Convey("wr resume makes an expired delayed suspended job ready", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, serverConfig jobqueue.ServerConfig) {
			delayed := newQueueCommandJob("echo resume delayed ready", "rg-resume-delay", reqs)
			addQueueCommandJobs(jq, delayed)
			releaseQueueCommandJob(jq, delayed)
			suspendQueueCommandJobs(jq, delayed)
			time.Sleep(serverConfig.Timings.ReleaseDelayMin + 50*time.Millisecond)

			output, err := runResumeForTest(t, "-i", "rg-resume-delay")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, delayed), ShouldEqual, jobqueue.JobStateReady)

			reserved, err := jq.Reserve(2 * time.Second)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, delayed.Key())
		})
	})

	Convey("wr resume keeps an expired delayed suspended job dependent when dependencies are unresolved", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, serverConfig jobqueue.ServerConfig) {
			delayed := newQueueCommandJob("echo resume delayed dependent", "rg-resume-delay-dep", reqs)
			parent := newQueueCommandJob("echo resume delayed parent", "rg-resume-delay-parent", reqs)
			addQueueCommandJobs(jq, delayed, parent)
			releaseQueueCommandJob(jq, delayed)
			parentReserved := reserveQueueCommandJob(jq, parent)
			suspendQueueCommandJobs(jq, delayed)
			time.Sleep(serverConfig.Timings.ReleaseDelayMin + 50*time.Millisecond)
			setQueueCommandDependency(jq, delayed, parent)

			output, err := runResumeForTest(t, "-i", "rg-resume-delay-dep")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, delayed), ShouldEqual, jobqueue.JobStateDependent)

			reserved, err := jq.Reserve(50 * time.Millisecond)
			So(err, ShouldBeNil)
			So(reserved, ShouldBeNil)
			touchQueueCommandJob(jq, parentReserved)
		})
	})

	Convey("wr resume reports matching non-suspended jobs without changing them", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			suspended := newQueueCommandJob("echo resume mixed suspended", "rg-resume-mixed", reqs)
			ready := newQueueCommandJob("echo resume mixed ready", "rg-resume-mixed", reqs)
			addQueueCommandJobs(jq, suspended, ready)
			suspendQueueCommandJobs(jq, suspended)

			output, err := runResumeForTest(t, "-i", "rg-resume-mixed")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Resumed 1 suspended commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, suspended), ShouldEqual, jobqueue.JobStateReady)
			So(jobStateByEssence(jq, ready), ShouldEqual, jobqueue.JobStateReady)
		})
	})

	Convey("wr resume validates selectors", t, func() {
		for _, tc := range []struct {
			name string
			args []string
			want error
		}{
			{
				name: "missing selector",
				want: errSelectedJobsNeedSelector,
			},
			{
				name: "mutually exclusive selectors",
				args: []string{"-i", "rg", "-a"},
				want: errSelectedJobsExclusive,
			},
			{
				name: "search without identifier",
				args: []string{"-z"},
				want: errSelectedJobsNeedID,
			},
			{
				name: "internal without identifier",
				args: []string{"-y"},
				want: errSelectedJobsNeedID,
			},
		} {
			Convey(tc.name, func() {
				output, err := runResumeForTest(t, tc.args...)
				So(output, ShouldBeEmpty)
				So(err, ShouldNotBeNil)
				So(err, ShouldEqual, tc.want)
			})
		}
	})
}

func suspendQueueCommandJobs(jq *jobqueue.Client, jobs ...*jobqueue.Job) {
	jes := make([]*jobqueue.JobEssence, 0, len(jobs))
	for _, job := range jobs {
		jes = append(jes, job.ToEssense())
	}

	suspended, err := jq.Suspend(jes)
	So(err, ShouldBeNil)
	So(suspended, ShouldEqual, len(jobs))

	for _, job := range jobs {
		So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateSuspended)
	}
}

func runResumeForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()

	return runSelectionCommandForTest(t, resumeCmd, runResumeCommand, args...)
}

func setQueueCommandDependency(jq *jobqueue.Client, child, parent *jobqueue.Job) {
	modifier := jobqueue.NewJobModifer()
	modifier.SetDependencies(jobqueue.Dependencies{jobqueue.NewEssenceDependency(parent.Cmd, "")})

	modified, err := jq.Modify([]*jobqueue.JobEssence{child.ToEssense()}, modifier)
	So(err, ShouldBeNil)
	So(modified[child.Key()], ShouldEqual, child.Key())
}

func touchQueueCommandJob(jq *jobqueue.Client, job *jobqueue.Job) {
	_, err := jq.Touch(job)
	So(err, ShouldBeNil)
}
