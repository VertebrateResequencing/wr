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
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
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

	runnerEnvPathName     = "PATH"
	runnerEnvHostName     = "WR_MANAGERHOST"
	runnerEnvHost         = "localhost"
	runnerEnvTestRepGrp   = "runner-env-test"
	runnerEnvTestCmd      = "true"
	runnerEnvSequenceLen  = 3
	runnerEnvTestKey      = "runner-env-test-key"
	runnerEnvUnnamedValue = "runner-env-unnamed-value"

	runnerEnvTestDirPerm  = 0o700
	runnerEnvTestFilePerm = 0o600
)

// TestRunnerWarnsOfUndefinedEnvEntries covers item 2 of
// .docs/bugfixes/260910-1.md: a stored entry that defines no variable is
// skipped, and the runner now says so rather than leaving the job's owner to
// wonder where their variable went.
func TestRunnerWarnsOfUndefinedEnvEntries(t *testing.T) {
	Convey("Given the runner's log captured at warn", t, func() {
		logged := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		ctx := context.Background()

		Convey("a bare name is warned about, naming the job and the entry", func() {
			warnUndefinedEnvEntries(ctx, runnerEnvTestKey, []string{runnerEnvHostName + "=" + runnerEnvHost, runnerEnvPathName})

			So(logged.String(), ShouldContainSubstring, "defines no variable")
			So(logged.String(), ShouldContainSubstring, runnerEnvTestKey)
			So(logged.String(), ShouldContainSubstring, "entry="+runnerEnvPathName)
			So(logged.String(), ShouldNotContainSubstring, runnerEnvHost)
		})

		Convey("an entry with no name is warned about without logging its value", func() {
			warnUndefinedEnvEntries(ctx, runnerEnvTestKey, []string{"=" + runnerEnvUnnamedValue})

			So(logged.String(), ShouldContainSubstring, "defines no variable")
			So(logged.String(), ShouldNotContainSubstring, runnerEnvUnnamedValue)
		})

		Convey("a well-formed environment logs nothing", func() {
			warnUndefinedEnvEntries(ctx, runnerEnvTestKey, []string{runnerEnvPathName + "=" + runnerEnvPathWith})

			So(logged.String(), ShouldBeBlank)
		})
	})
}

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
			first := overrider.overridesFor([]string{runnerEnvPathName + "=" + runnerEnvPathWithout}, false)

			overrider.overridesFor([]string{runnerEnvPathName + "=" + runnerEnvPathLater}, false)

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

			So(overrider.overridesFor(env, false), ShouldResemble, baseOnly)
		})

		Convey("The overrides do not grow as the runner works through jobs", func() {
			for range runnerEnvSequenceLen {
				runnerEnvTestRun(overrider, runnerEnvTestJob(runnerEnvPathWithout))
			}

			last := runnerEnvTestJob(runnerEnvPathWith)
			env, err := last.Env()
			So(err, ShouldBeNil)
			So(overrider.overridesFor(env, false), ShouldResemble, baseOnly)
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
			So(overrider.overridesFor(env, false), ShouldResemble, baseOnly)

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

// TestRunnerChangeHomeJobFindsManager covers item 8 of
// .docs/bugfixes/260903-9.md: --change_home sets a job's HOME to its working
// directory, so a `wr` its Cmd runs looked for the manager's token and CA files
// under that directory, where they are not, and could not connect.
func TestRunnerChangeHomeJobFindsManager(t *testing.T) {
	Convey("Given a runner whose manager dir is in the real home", t, func() {
		realHome := t.TempDir()
		managerBase := filepath.Join(realHome, ".wr")
		managerDir := managerBase + "_" + internal.Production
		So(os.MkdirAll(managerDir, runnerEnvTestDirPerm), ShouldBeNil)

		tokenFile := filepath.Join(managerDir, "client.token")
		So(os.WriteFile(tokenFile, []byte("token"), runnerEnvTestFilePerm), ShouldBeNil)

		originalConfig := config
		config = &internal.Config{ManagerDir: managerDir, Deployment: internal.Production}

		defer func() {
			config = originalConfig
		}()

		overrider, err := newJobEnvOverrider("localhost:1234", "localhost")
		So(err, ShouldBeNil)

		// nestedConfig runs job the way the runner does, then loads config for
		// deployment the way a `wr` run by job's Cmd would, with HOME set to
		// jobHome as --change_home does.
		nestedConfig := func(job *jobqueue.Job, jobHome, deployment string) *internal.Config {
			runnerEnvTestRun(overrider, job)

			env, errc := job.Env()
			So(errc, ShouldBeNil)

			env = append(env, "HOME="+jobHome)

			for _, envvar := range env {
				name, value, _ := strings.Cut(envvar, "=")
				if name == "HOME" || strings.HasPrefix(name, "WR_") {
					t.Setenv(name, value)
				}
			}

			return internal.ConfigLoadFromCurrentDir(context.Background(), deployment)
		}

		changeHomeJob := func() *jobqueue.Job {
			return &jobqueue.Job{
				Cmd:           runnerEnvTestCmd,
				Cwd:           statusTestCwd,
				RepGroup:      runnerEnvTestRepGrp,
				ChangeHome:    true,
				EnvCRetrieved: true,
			}
		}

		Convey("a wr run by a --change_home job's Cmd uses the runner's token and CA files", func() {
			nested := nestedConfig(changeHomeJob(), t.TempDir(), internal.Production)
			So(nested.ManagerTokenFile, ShouldEqual, tokenFile)
			So(nested.ManagerCAFile, ShouldEqual, filepath.Join(managerDir, "ca.pem"))
		})

		Convey("a wr run by a --change_home job's Cmd for another deployment uses that deployment's dir", func() {
			nested := nestedConfig(changeHomeJob(), t.TempDir(), internal.Development)
			So(nested.ManagerTokenFile, ShouldEqual,
				filepath.Join(managerBase+"_"+internal.Development, "client.token"))
		})

		Convey("only a --change_home job gets a manager dir override", func() {
			managerDirOverride := "WR_MANAGERDIR=" + managerBase

			So(overrider.overridesFor(nil, true), ShouldContain, managerDirOverride)
			So(overrider.overridesFor(nil, false), ShouldNotContain, managerDirOverride)
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

	So(job.EnvAddOverride(overrider.overridesFor(env, job.ChangeHome)), ShouldBeNil)
}
