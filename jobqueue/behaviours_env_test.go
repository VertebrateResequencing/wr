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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

// testEnvVar is a variable the tests put in a Job's stored environment and
// nowhere else, so a `run` Behaviour that reports a value for it can only have
// got it from the Job.
const testEnvVar = "WR_TEST_BEHAVIOUR_ENV"

// testStoredHome is the HOME in a Job's stored environment: neither the calling
// process's nor any directory wr creates, so each of the three is told apart by
// the value the behaviour reports.
const testStoredHome = "/nonexistent/stored-home"

// probedEnv is what a `run` Behaviour's command found in its OWN environment. The
// behaviour runs in a process of its own, so it writes these to a file and the
// test reads them back; see envProbeBehaviour.
type probedEnv struct {
	home    string
	tmpDir  string
	jobCwd  string
	testVar string
}

// envProbeBehaviour is a `run` Behaviour whose command reports the environment it
// was given to the named file.
func envProbeBehaviour(out string) *Behaviour {
	return &Behaviour{
		When: OnExit,
		Do:   Run,
		Arg: `printf '%s\n%s\n%s\n%s\n' "$HOME" "$TMPDIR" "$` + JobCwdEnvVar +
			`" "$` + testEnvVar + `" > ` + out,
	}
}

// readProbedEnv reads back what envProbeBehaviour's command reported.
func readProbedEnv(out string) probedEnv {
	reported, err := os.ReadFile(filepath.Clean(out))
	So(err, ShouldBeNil)

	lines := strings.Split(strings.TrimSuffix(string(reported), "\n"), "\n")
	So(len(lines), ShouldEqual, 4)

	return probedEnv{home: lines[0], tmpDir: lines[1], jobCwd: lines[2], testVar: lines[3]}
}

// probedEnvIfRun is what a `run` Behaviour's command reported, or the zero
// probedEnv when the behaviour refused to run the command at all: a refused
// behaviour writes nothing, so any value here names an environment some process
// handed the command.
func probedEnvIfRun(out string) probedEnv {
	if _, err := os.Stat(filepath.Clean(out)); os.IsNotExist(err) {
		return probedEnv{}
	}

	return readProbedEnv(out)
}

// storeTestEnv gives job the environment it was added with: the calling process's
// (so the command has a PATH) with its HOME replaced, a variable of the test's
// own added, and any extra variables the caller names, the way wr's own --env
// override does it.
func storeTestEnv(job *Job, extra ...string) {
	env, err := compressEnv(envOverride(os.Environ(),
		append([]string{"HOME=" + testStoredHome, testEnvVar + "=from_job_env"}, extra...)))
	So(err, ShouldBeNil)

	job.EnvC = env
	job.EnvCRetrieved = true
}

func TestBehaviourRunEnv(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// a `run` behaviour is part of the same job as the Cmd, so it has to run in
	// the same environment: the one captured when the job was added, with the
	// job's own TMPDIR, and with the HOME --change_home gave the Cmd. Inheriting
	// the calling process's environment instead pointed `rm -rf $HOME/scratch`
	// at the user's real home - the one directory --change_home exists to keep
	// the job out of - and, on the manager's lost-job path, at a different
	// machine's home altogether.
	Convey("Given a Job whose stored environment differs from this process's", t, func() {
		cwd := t.TempDir()
		reports := t.TempDir()
		out := filepath.Join(reports, "env")
		probe := envProbeBehaviour(out)

		So(os.Getenv("HOME"), ShouldNotEqual, testStoredHome)
		So(os.Getenv(testEnvVar), ShouldBeBlank)

		Convey("a run behaviour executes with the Job's environment, not this process's", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			storeTestEnv(job)
			actualCwd, _, tmpDir := realWorkSpace(job)

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.testVar, ShouldEqual, "from_job_env")
			So(got.home, ShouldEqual, testStoredHome)
			So(got.tmpDir, ShouldEqual, tmpDir)
			So(got.tmpDir, ShouldNotEqual, os.Getenv("TMPDIR"))
			// without --change_home, HOME stays whatever the Job was added with
			So(got.home, ShouldNotEqual, actualCwd)
		})

		Convey("--change_home points a run behaviour's HOME at the Job's working directory", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, ChangeHome: true}
			storeTestEnv(job)
			actualCwd, _, _ := realWorkSpace(job)

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.home, ShouldEqual, actualCwd)
			So(got.home, ShouldNotEqual, os.Getenv("HOME"))
		})

		Convey("an env override reaches a run behaviour", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			storeTestEnv(job)
			realWorkSpace(job)
			So(job.EnvAddOverride([]string{testEnvVar + "=from_override"}), ShouldBeNil)

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			So(readProbedEnv(out).testVar, ShouldEqual, "from_override")
		})

		Convey("a CwdMatters Job's run behaviour gets its environment untouched", func() {
			// wr creates no working directory for such a Job, so Execute gives
			// its Cmd no TMPDIR and no HOME of wr's, whatever --change_home says.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, CwdMatters: true, ChangeHome: true}
			storeTestEnv(job)

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.testVar, ShouldEqual, "from_job_env")
			So(got.home, ShouldEqual, testStoredHome)
			So(got.tmpDir, ShouldBeBlank)
		})

		// a `run` behaviour's command adds jobs as readily as the Cmd does -
		// `on_success: {"run": "wr add -f followup.txt"}` - and it runs in the
		// working directory wr made for the Cmd, so os.Getwd() there is that
		// disposable directory. It needs the Job's own Cwd for the same reason
		// the Cmd does, or the jobs it adds get their workspaces built inside
		// this one's; see JobCwdEnvVar.
		Convey("a run behaviour is told the Job's own Cwd, not the working directory it runs in", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			storeTestEnv(job)
			actualCwd, _, _ := realWorkSpace(job)

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.jobCwd, ShouldEqual, cwd)
			So(got.jobCwd, ShouldNotEqual, actualCwd)
		})

		Convey("a CwdMatters Job's run behaviour is told nothing, not even what it inherited", func() {
			// wr moved this Job out of nothing, so os.Getwd() is the right answer
			// for anything its command adds - while the value the Job that added
			// THIS one was given names a different directory altogether.
			job := &Job{Cwd: cwd, Cmd: testWSCmd, CwdMatters: true}
			storeTestEnv(job, JobCwdEnvVar+"="+filepath.Join("/", "grandparent"))

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.jobCwd, ShouldBeBlank)
			So(got.testVar, ShouldEqual, "from_job_env")
		})

		Convey("a CwdMatters Job with no environment of its own is told nothing either", func() {
			// with nothing stored and nothing retrieved the Job names no
			// environment at all, so the run gets the triggering process's - and
			// that process is a wr runner or manager, which was itself given a
			// WR_JOB_CWD naming ITS Job's Cwd. Leaving the run to inherit it
			// silently would hand this Job's command a directory it was never in.
			t.Setenv(JobCwdEnvVar, filepath.Join("/", "grandparent"))

			job := &Job{Cwd: cwd, Cmd: testWSCmd, CwdMatters: true}

			So(probe.Trigger(OnExit, job), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.jobCwd, ShouldBeBlank)
			// the rest of that environment is still what the run has to work
			// with, so only the one variable goes
			So(got.home, ShouldEqual, os.Getenv("HOME"))
		})

		Convey("the behaviours the manager pins carry the Job's environment", func() {
			job := &Job{Cwd: cwd, Cmd: testWSCmd, ChangeHome: true, Behaviours: Behaviours{probe}}
			storeTestEnv(job)
			actualCwd, _, _ := realWorkSpace(job)

			pin := job.pinBehaviours()
			So(pin.trigger(), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.testVar, ShouldEqual, "from_job_env")
			So(got.home, ShouldEqual, actualCwd)
		})

		Convey("the manager gives a lost Job's run behaviour the environment from its database", func() {
			// the manager keeps one copy of each distinct environment in its
			// database and only the key naming it on the Job, so a pin taken
			// there carries no environment at all and the behaviour would run
			// with the MANAGER's. This is what triggerLostRunBehaviours does,
			// without the concurrency token.
			ctx := context.Background()
			job := &Job{Cwd: cwd, Cmd: testWSCmd, ChangeHome: true, Behaviours: Behaviours{probe}}
			actualCwd, _, _ := realWorkSpace(job)

			env, err := compressEnv(envOverride(os.Environ(),
				[]string{"HOME=" + testStoredHome, testEnvVar + "=from_job_env"}))
			So(err, ShouldBeNil)

			testDB, _, err := initDB(ctx, filepath.Join(t.TempDir(), "queue.db"), "",
				internal.Development, false, false)
			So(err, ShouldBeNil)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			job.EnvKey, err = testDB.storeEnv(env)
			So(err, ShouldBeNil)
			So(job.EnvC, ShouldBeNil)

			pin := job.pinBehaviours()
			pin.fillEnvFromDB(ctx, testDB)
			So(pin.trigger(), ShouldBeNil)

			got := readProbedEnv(out)
			So(got.testVar, ShouldEqual, "from_job_env")
			So(got.home, ShouldEqual, actualCwd)
		})

		Convey("the manager refuses a lost Job's run behaviour when the environment is missing from its database", func() {
			// an environment the database cannot produce is NOT an empty one:
			// falling back to the current environment here would run the user's
			// command with the MANAGER's, which is the exact leak the cases
			// above pin. It must refuse, and say so.
			ctx := context.Background()
			job := &Job{Cwd: cwd, Cmd: testWSCmd, Behaviours: Behaviours{probe}}
			realWorkSpace(job)

			testDB, _, err := initDB(ctx, filepath.Join(t.TempDir(), "queue.db"), "",
				internal.Development, false, false)
			So(err, ShouldBeNil)

			defer func() { So(testDB.close(ctx), ShouldBeNil) }()

			// the key of an environment that was never stored, as a Job whose
			// env record has gone from the database has
			job.EnvKey = byteKey([]byte("an environment that was never stored"))

			pin := job.pinBehaviours()
			pin.fillEnvFromDB(ctx, testDB)

			err = pin.trigger()

			// nothing reported means the command never ran; a HOME or a TMPDIR
			// here is one the triggering process handed it
			got := probedEnvIfRun(out)
			So(got.home, ShouldBeBlank)
			So(got.tmpDir, ShouldBeBlank)

			// and the refusal is reported rather than silent
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "the job's environment could not be read")
		})

		Convey("only a run behaviour makes the Job's environment be decoded", func() {
			// decoding is a decompress and a decode of every variable the Job was
			// added with, on the path of every Job that exits, so a Job with no
			// behaviours - most of them - and one with only a cleanup must not
			// pay for it.
			decodes := 0
			envDecodeHook = func() { decodes++ }

			defer func() { envDecodeHook = nil }()

			job := &Job{Cwd: cwd, Cmd: testWSCmd}
			storeTestEnv(job)
			realWorkSpace(job)

			So(job.TriggerBehaviours(false), ShouldBeNil)
			So(decodes, ShouldEqual, 0)

			job.Behaviours = Behaviours{{When: OnExit, Do: CleanupAll}}
			So(job.TriggerBehaviours(false), ShouldBeNil)
			So(decodes, ShouldEqual, 0)

			job.Behaviours = Behaviours{probe}
			realWorkSpace(job)

			So(job.TriggerBehaviours(false), ShouldBeNil)
			So(decodes, ShouldEqual, 1)
			So(readProbedEnv(out).testVar, ShouldEqual, "from_job_env")
		})
	})
}
