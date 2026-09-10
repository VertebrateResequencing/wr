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

// This file tests item 4 of .docs/bugfixes/260909-fix-env-propagation.md: an
// environment entry that is a bare NAME with no "=" was accepted by every route
// that stores an environment on a job, and then made the runner panic when it
// read the value after the "=". The entries that define no variable for other
// reasons - an empty one, and one with no name before the "=" - are refused by
// the same routes.

import (
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// envValidationBare is the entry a user produces by typing `--env PATH`
	// when they mean "pass my PATH through".
	envValidationBare = "PATH"

	envValidationGood    = "PATH=/usr/bin"
	envValidationOther   = "WR_ENV_TEST=1"
	envValidationTestCmd = "echo env validation"

	// the other entries that define no variable: the empty one a trailing or
	// doubled comma leaves behind, and one with nothing before the "=".
	envValidationEmpty  = ""
	envValidationNoName = "=value"

	// envValidationColon is a well formed entry whose value holds a ":", which
	// must go on being accepted; envValidationEqualsName below covers a value
	// holding a further "=".
	envValidationColon = "WR_ENV_COLON=/usr/bin:/bin"

	// the parts of a refusal that make it actionable: what wr wanted instead,
	// how to write the pass-through the bare name was meant to be, and the
	// words that name the other two mistakes and their likely cause.
	envValidationFormat      = "key=value"
	envValidationPassThrough = "NAME=$NAME"
	envValidationEmptyWord   = "empty"
	envValidationComma       = "comma"
	envValidationNameless    = "no name"

	// the variables of a job that was stored before the write routes started
	// refusing a bare name: one bare, one ordinary, and one whose value
	// contains a further "=".
	envValidationStoredBare = "WR_ENV_BARE"
	envValidationStoredName = "WR_ENV_OK"
	envValidationStoredVal  = "value"
	envValidationEqualsName = "WR_ENV_EQUALS"
	envValidationEqualsVal  = "a=b"

	// envValidationPrepend stands in for the directory holding wr's bsub
	// symlinks, which a bsub-mode job wants at the front of its PATH.
	envValidationPrepend = "/wr/bsub"
	envValidationPathVar = "PATH"
)

// TestGetenvSurvivesStoredBareName covers a job that was stored with a bare
// environment name before the write routes started refusing them: validation
// does nothing for those, and they would go on crash-looping their scheduler
// group after an upgrade.
func TestGetenvSurvivesStoredBareName(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a job stored with an environment entry that has no =", t, func() {
		envc, err := compressEnv([]string{
			envValidationStoredBare,
			envValidationStoredName + "=" + envValidationStoredVal,
			envValidationEqualsName + "=" + envValidationEqualsVal,
		})
		So(err, ShouldBeNil)

		job := &Job{EnvC: envc, EnvCRetrieved: true}

		Convey("Getenv of that name answers blank instead of panicking", func() {
			So(func() { job.Getenv(envValidationStoredBare) }, ShouldNotPanic)
			So(job.Getenv(envValidationStoredBare), ShouldBeBlank)
		})

		Convey("Getenv of the job's other variables still answers their whole value", func() {
			So(job.Getenv(envValidationStoredName), ShouldEqual, envValidationStoredVal)
			So(job.Getenv(envValidationEqualsName), ShouldEqual, envValidationEqualsVal)
		})
	})
}

// TestBsubEnvSurvivesStoredBareName covers the same stored bare name reaching
// the bsub emulation environment, which reads the value after the "=" of the
// job's PATH so it can put wr's bsub symlink directory in front of it. This read
// is inside Client.Execute, in the runner, so it crash-looped a scheduler group
// the same way the runner's own read did.
func TestBsubEnvSurvivesStoredBareName(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a bsub-mode job whose stored environment holds a bare PATH", t, func() {
		client := &Client{}
		job := &Job{Cmd: envValidationTestCmd, Requirements: &scheduler.Requirements{}}

		bareEnv := func() []string { return []string{envValidationBare, envValidationOther} }

		Convey("Building its bsub environment leaves the runner alive, "+
			"with a PATH of the bsub directory alone", func() {
			var (
				env []string
				err error
			)

			So(func() {
				env, err = client.addBsubEnv(bareEnv(), job, envValidationPrepend, "localhost", "/bin/sh")
			}, ShouldNotPanic)

			So(err, ShouldBeNil)
			So(env, ShouldContain, envValidationPathVar+"="+envValidationPrepend)
			So(env, ShouldNotContain, envValidationBare)
		})

		Convey("A usable PATH still gets the bsub directory put in front of it", func() {
			env, err := client.addBsubEnv([]string{envValidationGood}, job,
				envValidationPrepend, "localhost", "/bin/sh")

			So(err, ShouldBeNil)
			So(env, ShouldContain, envValidationPathVar+"="+envValidationPrepend+":/usr/bin")
		})
	})
}

// TestEnvWriteRoutesRejectMalformedEntries checks that no route that stores an
// environment on a job accepts an entry that defines no variable, and that each
// refusal says enough for the user to fix what they typed.
func TestEnvWriteRoutesRejectMalformedEntries(t *testing.T) {
	if runnermode || servermode {
		return
	}

	for _, route := range envValidationRoutes() {
		Convey("Given the "+route.name+" way of setting a job's environment", t, func() {
			for _, bad := range envValidationBadEntries() {
				Convey(bad.desc, func() {
					err := route.set([]string{envValidationOther, bad.entry})

					So(err, ShouldNotBeNil)

					for _, says := range bad.says {
						So(err.Error(), ShouldContainSubstring, says)
					}
				})
			}

			Convey("Entries that are all key=value are accepted, including values "+
				"that themselves hold a = or a :", func() {
				So(route.set([]string{
					envValidationOther,
					envValidationGood,
					envValidationEqualsName + "=" + envValidationEqualsVal,
					envValidationColon,
				}), ShouldBeNil)
			})
		})
	}
}

// envValidationRoutes returns every route by which a user's environment reaches
// a Job's stored EnvOverride, each expressed as a call taking the environment
// the user supplied.
func envValidationRoutes() []struct {
	name string
	set  func(envars []string) error
} {
	return []struct {
		name string
		set  func(envars []string) error
	}{
		{
			name: "wr mod --env",
			set: func(envars []string) error {
				return NewJobModifer().SetEnvOverride(strings.Join(envars, ","))
			},
		},
		{
			name: "REST PATCH /jobs with an env array",
			set: func(envars []string) error {
				_, err := (&JobModifyViaJSON{Env: &envars}).Convert()

				return err
			},
		},
		{
			name: "wr add --env, and REST POST /jobs with an env default",
			set: func(envars []string) error {
				_, err := (&JobViaJSON{Cmd: envValidationTestCmd}).
					Convert(&JobDefaults{Env: strings.Join(envars, ",")})

				return err
			},
		},
		{
			name: "wr add with a JSON env, and REST POST /jobs with a job env array",
			set: func(envars []string) error {
				_, err := (&JobViaJSON{Cmd: envValidationTestCmd, Env: envars}).Convert(&JobDefaults{})

				return err
			},
		},
	}
}

// envValidationBadEntries returns every entry that defines no variable, with
// what the refusal of each should tell the user.
func envValidationBadEntries() []struct {
	desc  string
	entry string
	says  []string
} {
	return []struct {
		desc  string
		entry string
		says  []string
	}{
		{
			desc: "An entry with no = is refused, naming it, the format wanted, " +
				"and how to pass a variable through",
			entry: envValidationBare,
			says:  []string{envValidationBare, envValidationFormat, envValidationPassThrough},
		},
		{
			desc: "An empty entry, as a trailing or doubled comma leaves behind, " +
				"is refused with the comma pointed at",
			entry: envValidationEmpty,
			says:  []string{envValidationEmptyWord, envValidationComma},
		},
		{
			desc:  "An entry with no name before the = is refused, naming it",
			entry: envValidationNoName,
			says:  []string{envValidationNoName, envValidationNameless},
		},
	}
}
