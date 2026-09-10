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

// This file tests items 2 and 4 of
// .docs/bugfixes/260909-fix-env-propagation.md. Item 2: a runner accumulated
// its environment overrides across the jobs it ran, so a job that needed no
// PATH override of its own was handed the PREVIOUS job's PATH and could resolve
// the wrong binaries. Item 4: a job whose stored environment held a bare name
// with no "=" made the runner panic as it read the value after the "=".

import (
	"slices"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// runnerEnvExeDir stands in for the directory holding the runner's own wr
	// exe, which the runner wants on every job's PATH.
	runnerEnvExeDir = "/opt/wrtest/bin"

	// runnerEnvPathWithout is a job PATH that lacks runnerEnvExeDir, so the
	// runner must append it.
	runnerEnvPathWithout = "/jobone/bin:/usr/bin"

	// runnerEnvPathWith is a job PATH that already has runnerEnvExeDir, so the
	// runner must leave it exactly as it is.
	runnerEnvPathWith = "/opt/wrtest/bin:/jobtwo/bin"

	// runnerEnvPathLater is a later job's PATH that also lacks runnerEnvExeDir,
	// so the runner appends the exe dir to this one as well.
	runnerEnvPathLater = "/jobthree/bin:/usr/bin"

	runnerEnvPathName    = "PATH"
	runnerEnvHostName    = "WR_MANAGERHOST"
	runnerEnvHost        = "localhost"
	runnerEnvTestRepGrp  = "runner-env-test"
	runnerEnvTestCmd     = "true"
	runnerEnvSequenceLen = 3
)

func TestRunnerJobEnvOverrides(t *testing.T) {
	Convey("Given a runner's job environment overrider", t, func() {
		overrider := &jobEnvOverrider{exePath: runnerEnvExeDir}

		// the base overrides are appended one at a time, as the runner appends
		// them, which leaves the slice with spare capacity; a composite literal
		// would not, and that spare capacity is what would let one job's
		// overrides overwrite another's.
		overrider.base = append(overrider.base, runnerEnvHostName+"="+runnerEnvHost)
		overrider.base = append(overrider.base, "WR_MANAGERPORT=1234")
		overrider.base = append(overrider.base, "WR_MANAGERCERTDOMAIN=wr.example.com")

		baseOnly := slices.Clone(overrider.base)

		Convey("A job whose PATH lacks the exe dir runs with the exe dir appended", func() {
			job := runnerEnvTestJob(runnerEnvPathWithout)

			runnerEnvTestRun(overrider, job)

			So(job.Getenv(runnerEnvPathName), ShouldEqual, runnerEnvPathWithout+":"+runnerEnvExeDir)
			So(job.Getenv(runnerEnvHostName), ShouldEqual, runnerEnvHost)
		})

		Convey("A job whose PATH already has the exe dir runs with its own PATH", func() {
			job := runnerEnvTestJob(runnerEnvPathWith)

			runnerEnvTestRun(overrider, job)

			So(job.Getenv(runnerEnvPathName), ShouldEqual, runnerEnvPathWith)
			So(job.Getenv(runnerEnvHostName), ShouldEqual, runnerEnvHost)
		})

		Convey("It still does when an earlier job in the same runner needed the exe dir appended", func() {
			first := runnerEnvTestJob(runnerEnvPathWithout)
			runnerEnvTestRun(overrider, first)

			second := runnerEnvTestJob(runnerEnvPathWith)
			runnerEnvTestRun(overrider, second)

			So(second.Getenv(runnerEnvPathName), ShouldEqual, runnerEnvPathWith)
			So(second.Getenv(runnerEnvHostName), ShouldEqual, runnerEnvHost)
			So(first.Getenv(runnerEnvPathName), ShouldEqual, runnerEnvPathWithout+":"+runnerEnvExeDir)
		})

		Convey("A job's overrides keep its own PATH once a later job's are built", func() {
			first := overrider.overridesFor([]string{runnerEnvPathName + "=" + runnerEnvPathWithout})

			overrider.overridesFor([]string{runnerEnvPathName + "=" + runnerEnvPathLater})

			So(first, ShouldResemble, append(slices.Clone(baseOnly),
				runnerEnvPathName+"="+runnerEnvPathWithout+":"+runnerEnvExeDir))
		})

		Convey("A job with no environment of its own gets only the base overrides, "+
			"even after an earlier job needed a PATH override", func() {
			runnerEnvTestRun(overrider, runnerEnvTestJob(runnerEnvPathWithout))

			noEnv := &jobqueue.Job{Cmd: runnerEnvTestCmd, Cwd: statusTestCwd, RepGroup: runnerEnvTestRepGrp}
			env, err := noEnv.Env()
			So(err, ShouldBeNil)
			So(env, ShouldBeEmpty)

			So(overrider.overridesFor(env), ShouldResemble, baseOnly)
		})

		Convey("The overrides do not grow as the runner works through jobs", func() {
			for range runnerEnvSequenceLen {
				runnerEnvTestRun(overrider, runnerEnvTestJob(runnerEnvPathWithout))
			}

			last := runnerEnvTestJob(runnerEnvPathWith)
			env, err := last.Env()
			So(err, ShouldBeNil)
			So(overrider.overridesFor(env), ShouldResemble, baseOnly)
		})
	})
}

// TestRunnerStoredBareEnvName covers a job that was stored with an environment
// entry that has no "=" in it. Rejecting such an entry at the routes that write
// it does nothing for the jobs already in a database, and one of those used to
// panic the runner that reserved it: the runner died, the job was never touched
// so it returned to the queue as lost with numrun 0, and the manager started
// another runner to die on it in turn.
func TestRunnerStoredBareEnvName(t *testing.T) {
	Convey("Given a runner's overrider and a job stored with a bare environment name", t, func() {
		overrider := &jobEnvOverrider{exePath: runnerEnvExeDir}
		overrider.base = append(overrider.base, runnerEnvHostName+"="+runnerEnvHost)

		baseOnly := slices.Clone(overrider.base)

		job := &jobqueue.Job{
			Cmd:           runnerEnvTestCmd,
			Cwd:           statusTestCwd,
			RepGroup:      runnerEnvTestRepGrp,
			EnvCRetrieved: true,
		}
		So(job.EnvAddOverride([]string{runnerEnvPathName}), ShouldBeNil)

		env, err := job.Env()
		So(err, ShouldBeNil)
		So(env, ShouldContain, runnerEnvPathName)

		Convey("Preparing that job to run leaves the runner alive", func() {
			So(func() { runnerEnvTestRun(overrider, job) }, ShouldNotPanic)
		})

		Convey("The job runs with the base overrides and no PATH the runner made up", func() {
			So(overrider.overridesFor(env), ShouldResemble, baseOnly)

			runnerEnvTestRun(overrider, job)

			So(job.Getenv(runnerEnvHostName), ShouldEqual, runnerEnvHost)
			So(job.Getenv(runnerEnvPathName), ShouldBeBlank)
		})

		Convey("A later job with a usable PATH still gets the exe dir appended", func() {
			runnerEnvTestRun(overrider, job)

			later := runnerEnvTestJob(runnerEnvPathWithout)
			runnerEnvTestRun(overrider, later)

			So(later.Getenv(runnerEnvPathName), ShouldEqual, runnerEnvPathWithout+":"+runnerEnvExeDir)
			So(later.Getenv(runnerEnvHostName), ShouldEqual, runnerEnvHost)
		})
	})
}

// runnerEnvTestJob returns a job that stands in for one the runner reserved,
// with the given PATH in the environment its Cmd would run under.
//
// A reserved job carries the environment its client stored, which Env() applies
// the job's own overrides to; an empty EnvC with EnvCRetrieved set means the
// current environment, so an override is how a test gives the job a PATH of its
// own.
func runnerEnvTestJob(path string) *jobqueue.Job {
	job := &jobqueue.Job{
		Cmd:           runnerEnvTestCmd,
		Cwd:           statusTestCwd,
		RepGroup:      runnerEnvTestRepGrp,
		EnvCRetrieved: true,
	}

	So(job.EnvAddOverride([]string{runnerEnvPathName + "=" + path}), ShouldBeNil)

	return job
}

// runnerEnvTestRun does to job what the runner's reserve loop does before
// executing it: read the environment the job would run under, then add the
// overrides that environment calls for.
func runnerEnvTestRun(overrider *jobEnvOverrider, job *jobqueue.Job) {
	env, err := job.Env()
	So(err, ShouldBeNil)

	So(job.EnvAddOverride(overrider.overridesFor(env)), ShouldBeNil)
}
