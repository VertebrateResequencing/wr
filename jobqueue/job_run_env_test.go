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
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// unrelatedEnvVar is an environment variable setting envWithRunDirs must leave alone.
const unrelatedEnvVar = "FOO=bar"

// TestJobRunEnv covers the environment Execute gives a run's command. The
// interesting part is JobCwdEnvVar: a command that runs `wr add` finds its own
// Cwd there instead of having to trust os.Getwd(), which is the disposable
// working directory wr made for it.
func TestJobRunEnv(t *testing.T) {
	origCwd := filepath.Join("/", "some", "user", "dir")
	workSpace := filepath.Join(origCwd, "wr_cwd", "d", "4", "1", "7364d1", "cwd")
	tmpDir := filepath.Join(origCwd, "wr_cwd", "d", "4", "1", "7364d1", "tmp")

	Convey("A run in a wr-created working directory is told its Job's own Cwd", t, func() {
		job := &Job{Cmd: "wr add -f cmds", Cwd: origCwd}

		env := envWithRunDirs([]string{unrelatedEnvVar}, job, workSpace, tmpDir)

		So(env, ShouldContain, JobCwdEnvVar+"="+origCwd)
		So(env, ShouldContain, "TMPDIR="+tmpDir)
		So(env, ShouldContain, unrelatedEnvVar)

		Convey("Which is the Cwd, not the working directory, so all generations are siblings", func() {
			So(env, ShouldNotContain, JobCwdEnvVar+"="+workSpace)
		})

		Convey("And --change_home still moves HOME to the working directory", func() {
			job.ChangeHome = true

			env = envWithRunDirs([]string{unrelatedEnvVar, "HOME=/home/user"}, job, workSpace, tmpDir)

			So(env, ShouldContain, "HOME="+workSpace)
			So(env, ShouldContain, JobCwdEnvVar+"="+origCwd)
		})

		Convey("An existing value is replaced, not duplicated", func() {
			env = envWithRunDirs([]string{JobCwdEnvVar + "=" + filepath.Join("/", "grandparent")}, job, workSpace, tmpDir)

			So(env, ShouldContain, JobCwdEnvVar+"="+origCwd)
			So(env, ShouldNotContain, JobCwdEnvVar+"="+filepath.Join("/", "grandparent"))
		})
	})

	Convey("A cwd_matters run, which runs in its Cwd, is told nothing", t, func() {
		job := &Job{Cmd: "wr add -f cmds", Cwd: origCwd, CwdMatters: true}

		env := envWithRunDirs([]string{unrelatedEnvVar}, job, "", "")

		So(env, ShouldResemble, []string{unrelatedEnvVar})

		Convey("Even if it inherited a value from the Job that added it, since os.Getwd() is right for it", func() {
			env = envWithRunDirs([]string{unrelatedEnvVar, JobCwdEnvVar + "=" + filepath.Join("/", "grandparent")}, job, "", "")

			So(env, ShouldResemble, []string{unrelatedEnvVar})
		})

		Convey("And a nil environment stays nil, so the run inherits the runner's", func() {
			So(envWithRunDirs(nil, job, "", ""), ShouldBeNil)
		})
	})
}
