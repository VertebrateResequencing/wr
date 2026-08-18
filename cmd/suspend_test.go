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
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/spf13/cobra"
)

const (
	queueCommandDelay = 120 * time.Millisecond
	queueCommandCwd   = "/tmp"
	queueCommandReq   = "queue-command"
)

func TestSuspendCommand(t *testing.T) {
	Convey("wr suspend handles selected queued jobs", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			delayed := newQueueCommandJob("echo suspend delayed", "rg-suspend", reqs)
			parent := newQueueCommandJob("echo suspend parent", "rg-suspend-parent", reqs)
			dependent := newQueueCommandJob("echo suspend dependent", "rg-suspend", reqs)
			dependent.Dependencies = jobqueue.Dependencies{jobqueue.NewEssenceDependency(parent.Cmd, "")}
			ready := newQueueCommandJob("echo suspend ready", "rg-suspend", reqs)
			addQueueCommandJobs(jq, delayed, parent, dependent, ready)
			releaseQueueCommandJob(jq, delayed)

			output, err := runSuspendForTest(t, "-i", "rg-suspend")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 3 queued commands (out of 3 matching)\n")
			assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 3, "-i", "rg-suspend", "-o", "plain")
			So(jobStateByEssence(jq, delayed), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, dependent), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, ready), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend supports report group substring matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			one := newQueueCommandJob("echo suspend team a one", "team-a-1", reqs)
			two := newQueueCommandJob("echo suspend team a two", "team-a-2", reqs)
			addQueueCommandJobs(jq, one, two)

			output, err := runSuspendForTest(t, "-i", "team-a", "-z")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 2 queued commands (out of 2 matching)\n")
			assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 2, "-i", "team-a", "-z", "-o", "plain")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend supports internal job id matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo suspend internal", "rg-suspend-internal", reqs)
			addQueueCommandJobs(jq, job)

			output, err := runSuspendForTest(t, "-i", job.Key(), "-y")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			assertStatusPlainOutput(t, job.Key()+"\t"+string(jobqueue.JobStateSuspended)+"\n",
				"-i", job.Key(), "-y", "-o", "plain")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend supports command file matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			cmdPath := filepath.Join(t.TempDir(), "commands.txt")
			err := os.WriteFile(cmdPath, []byte("echo suspend file one\necho suspend file two\n"), 0o600)
			So(err, ShouldBeNil)
			configureQueueCommandFileSelection(t, cmdPath)

			one := newQueueCommandJob("echo suspend file one", "rg-suspend-file", reqs)
			two := newQueueCommandJob("echo suspend file two", "rg-suspend-file", reqs)
			addQueueCommandJobs(jq, one, two)

			output, err := runSuspendForTest(t, "-f", cmdPath)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 2 queued commands (out of 2 matching)\n")
			assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 2, "-f", cmdPath, "-o", "plain")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend supports command line and cwd matching", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			cwd := filepath.Join(t.TempDir(), "wr207")
			job := newQueueCommandJob("echo by-line", "rg-suspend-line", reqs)
			job.Cwd = cwd
			job.CwdMatters = true
			addQueueCommandJobs(jq, job)

			output, err := runSuspendForTest(t, "-l", "echo by-line", "-c", cwd)
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 1 matching)\n")
			assertStatusPlainStateCount(t, jobqueue.JobStateSuspended, 1, "-l", "echo by-line", "-c", cwd,
				"-o", "plain")
			So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend -a selects all live jobs", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			one := newQueueCommandJob("echo suspend all one", "rg-suspend-all", reqs)
			two := newQueueCommandJob("echo suspend all two", "rg-suspend-all", reqs)
			addQueueCommandJobs(jq, one, two)

			output, err := runSuspendForTest(t, "-a")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 2 queued commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, one), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, two), ShouldEqual, jobqueue.JobStateSuspended)
		})
	})

	Convey("wr suspend reports matching ineligible jobs without changing them", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			ready := newQueueCommandJob("echo suspend mixed ready", "rg-suspend-mixed", reqs)
			running := newQueueCommandJob("echo suspend mixed running", "rg-suspend-mixed", reqs)
			addQueueCommandJobs(jq, running, ready)
			startQueueCommandJob(jq, running)

			output, err := runSuspendForTest(t, "-i", "rg-suspend-mixed")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 1 queued commands (out of 2 matching)\n")
			So(jobStateByEssence(jq, ready), ShouldEqual, jobqueue.JobStateSuspended)
			So(jobStateByEssence(jq, running), ShouldEqual, jobqueue.JobStateRunning)
		})
	})

	Convey("wr suspend leaves buried jobs buried", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			buried := newQueueCommandJob("echo suspend buried", "rg-suspend-buried", reqs)
			addQueueCommandJobs(jq, buried)
			buryQueueCommandJob(jq, buried)

			output, err := runSuspendForTest(t, "-i", "rg-suspend-buried")
			So(err, ShouldBeNil)
			So(output, ShouldEqual, "Suspended 0 queued commands (out of 1 matching)\n")
			So(jobStateByEssence(jq, buried), ShouldEqual, jobqueue.JobStateBuried)
		})
	})

	Convey("wr suspend validates selectors", t, func() {
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
		} {
			Convey(tc.name, func() {
				output, err := runSuspendForTest(t, tc.args...)
				So(output, ShouldBeEmpty)
				So(err, ShouldNotBeNil)
				So(err, ShouldEqual, tc.want)
			})
		}
	})
}

func withQueueCommandTestServer(t *testing.T, run func(*jobqueue.Client, *jqs.Requirements, jobqueue.ServerConfig)) {
	t.Helper()

	ctx := context.Background()
	testConfig, serverConfig, addr, reqs, server, token := startQueueCommandTestServer(ctx, t)

	oldConfig, oldCAFile := config, caFile
	config, caFile = testConfig, testConfig.ManagerCAFile

	defer func() {
		config, caFile = oldConfig, oldCAFile
	}()
	defer server.Stop(ctx, true)

	jq, err := jobqueue.Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, 2*time.Second)

	So(err, ShouldBeNil)
	defer func() {
		So(jq.Disconnect(), ShouldBeNil)
	}()

	run(jq, reqs, serverConfig)
}

func startQueueCommandTestServer(ctx context.Context, t *testing.T) (
	*internal.Config, jobqueue.ServerConfig, string, *jqs.Requirements, *jobqueue.Server, []byte,
) {
	t.Helper()

	for attempt := range 20 {
		testConfig, serverConfig, addr, reqs := statusTestServerConfig(t)
		serverConfig.Timings.ReleaseDelayMin = queueCommandDelay

		server, _, token, err := jobqueue.Serve(ctx, serverConfig)
		if err == nil {
			return testConfig, serverConfig, addr, reqs, server, token
		}

		if attempt == 19 || !strings.Contains(err.Error(), "address already in use") {
			So(err, ShouldBeNil)
		}

		time.Sleep(5 * time.Millisecond)
	}

	panic("unreachable")
}

func newQueueCommandJob(cmd, repGroup string, reqs *jqs.Requirements) *jobqueue.Job {
	return &jobqueue.Job{
		Cmd:          cmd,
		Cwd:          queueCommandCwd,
		ReqGroup:     queueCommandReq,
		Requirements: reqs,
		Retries:      1,
		RepGroup:     repGroup,
	}
}

func addQueueCommandJobs(jq *jobqueue.Client, jobs ...*jobqueue.Job) {
	added, existed, err := jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, len(jobs))
	So(existed, ShouldEqual, 0)
}

func releaseQueueCommandJob(jq *jobqueue.Client, job *jobqueue.Job) {
	reserved := reserveQueueCommandJob(jq, job)
	So(jq.Release(reserved, nil, "queue command test delay"), ShouldBeNil)
	So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateDelayed)
}

func runSuspendForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()

	return runSelectionCommandForTest(t, suspendCmd, runSuspendCommand, args...)
}

func runSelectionCommandForTest(
	t *testing.T, command *cobra.Command, run func() error, args ...string,
) (string, error) {
	t.Helper()

	resetSelectionCommandForTest(t, command)
	So(command.ParseFlags(args), ShouldBeNil)

	reader, writer, err := os.Pipe()
	So(err, ShouldBeNil)

	defer reader.Close()

	originalStdout := os.Stdout

	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	runErr := run()

	So(writer.Close(), ShouldBeNil)

	output, err := io.ReadAll(reader)
	So(err, ShouldBeNil)

	return string(output), runErr
}

type statusExitPanic struct {
	code int
}

func runStatusPlainForTest(t *testing.T, args ...string) (string, int) {
	t.Helper()

	resetStatusForTest(t)
	So(statusCmd.ParseFlags(args), ShouldBeNil)

	reader, writer, err := os.Pipe()
	So(err, ShouldBeNil)

	defer reader.Close()

	originalStdout := os.Stdout
	originalStatusExit := statusExit

	os.Stdout = writer
	statusExit = func(code int) {
		panic(statusExitPanic{code: code})
	}

	defer func() {
		os.Stdout = originalStdout
		statusExit = originalStatusExit
	}()

	exitCode := 0

	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			exitPanic, ok := recovered.(statusExitPanic)
			if !ok {
				panic(recovered)
			}

			exitCode = exitPanic.code
		}()

		statusCmd.Run(statusCmd, nil)
	}()

	So(writer.Close(), ShouldBeNil)

	output, err := io.ReadAll(reader)
	So(err, ShouldBeNil)

	return string(output), exitCode
}

func assertStatusPlainStateCount(t *testing.T, state jobqueue.JobState, count int, args ...string) {
	t.Helper()

	output, exitCode := runStatusPlainForTest(t, args...)

	So(exitCode, ShouldEqual, 0)
	So(strings.Count(output, "\t"+string(state)+"\n"), ShouldEqual, count)
}

func assertStatusPlainOutput(t *testing.T, expected string, args ...string) {
	t.Helper()

	output, exitCode := runStatusPlainForTest(t, args...)

	So(exitCode, ShouldEqual, 0)
	So(output, ShouldEqual, expected)
}

func resetSelectionCommandForTest(t *testing.T, command *cobra.Command) {
	t.Helper()

	cmdFileStatus = ""
	cmdIDStatus = ""
	cmdIDIsSubStr = false
	cmdIDIsInternal = false
	cmdLine = ""
	cmdCwd = ""
	cmdAll = false
	// cmdRecent is one of the mutually-exclusive selectors countGetJobArgs counts, so
	// a status test that ran earlier in this package and left --recent set would
	// otherwise make every selection command here die with "-f, -i, -l and -a are
	// mutually exclusive". It has no flag on these commands, so only the var is reset.
	cmdRecent = ""
	cmdRecentPeriod = 0
	mountJSON = ""
	mountSimple = ""
	timeoutint = 120

	for _, flag := range []struct {
		name  string
		value string
	}{
		{"file", ""},
		{"identifier", ""},
		{"search", statusTestFalse},
		{"internal", statusTestFalse},
		{"cmdline", ""},
		{"cwd", ""},
		{"all", statusTestFalse},
		{"mount_json", ""},
		{"mounts", ""},
		{"timeout", "120"},
	} {
		So(command.Flags().Set(flag.name, flag.value), ShouldBeNil)
	}
}

func configureQueueCommandFileSelection(t *testing.T, cmdPath string) {
	t.Helper()

	oldConfig := config

	configureAddParserTest(t, cmdPath)

	config = oldConfig
	cmdFileStatus = cmdPath
	cmdFile = cmdPath
	cmdCwd = queueCommandCwd
	cmdCwdMatters = false
}

func startQueueCommandJob(jq *jobqueue.Client, job *jobqueue.Job) {
	reserved := reserveQueueCommandJob(jq, job)
	So(jq.Started(reserved, os.Getpid()), ShouldBeNil)
	So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateRunning)
}

func buryQueueCommandJob(jq *jobqueue.Client, job *jobqueue.Job) {
	reserved := reserveQueueCommandJob(jq, job)
	end := &jobqueue.JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}
	So(jq.Bury(reserved, end, "queue command test bury"), ShouldBeNil)
	So(jobStateByEssence(jq, job), ShouldEqual, jobqueue.JobStateBuried)
}

func reserveQueueCommandJob(jq *jobqueue.Client, job *jobqueue.Job) *jobqueue.Job {
	reserved, err := jq.Reserve(2 * time.Second)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, job.Key())

	return reserved
}

func jobStateByEssence(jq *jobqueue.Client, job *jobqueue.Job) jobqueue.JobState {
	got, err := jq.GetByEssence(job.ToEssense(), false, false)
	So(err, ShouldBeNil)
	So(got, ShouldNotBeNil)

	return got.State
}
