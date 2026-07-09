/*******************************************************************************
 * Copyright (c) 2016, 2018, 2026 Genome Research Ltd.
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
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/spf13/cobra"
)

var (
	errMissingSynchronousAddContext = errors.New("missing context")
	errUnexpectedSynchronousJobs    = errors.New("AddAndWait received unexpected jobs")
	errMissingIgnoreComplete        = errors.New("AddAndWait did not preserve ignoreComplete")
	errUnexpectedSynchronousEnv     = errors.New("AddAndWait received unexpected env vars")
	errUnexpectedSynchronousKeys    = errors.New("SubscribeToJobKeys received unexpected keys")
)

const (
	remoteManagerAddr          = "remote:1234"
	synchronousAddBuriedHelper = "buried"
	testFutureDepGroup         = "future"
	testSyncCommand            = "sync command"
	testSyncEnv                = "SYNC_TEST=1"
	testSyncJobKey             = "sync-job"
	testWarningJobKey          = "job1"
	futureDepGroupWarningLine  = "dependency group \"" + testFutureDepGroup +
		"\" has not been seen; dependent job(s) will wait until it appears\n"
)

func TestAddQueuesAvoidDefault(t *testing.T) {
	Convey("add queues_avoid default is applied when unset", t, func() {
		flag := addCmd.Flags().Lookup("queues_avoid")
		So(flag, ShouldNotBeNil)
		So(flag.Value.String(), ShouldEqual, "interactive")
		So(cmdQueuesAvoidAdd, ShouldEqual, "interactive")
	})
}

func TestSynchronousAddDoesNotUsePollingHelper(t *testing.T) {
	Convey("cmd/add.go no longer contains the sync polling helper", t, func() {
		source, err := os.ReadFile(filepath.Join("..", "cmd", "add.go"))
		So(err, ShouldBeNil)
		So(string(source), ShouldNotContainSubstring, "waitForJobCompletion")
	})
}

func TestAddRemoteSameAsLocal(t *testing.T) {
	oldConfig := config
	oldCmdRemoteSameAsLocal := cmdRemoteSameAsLocal

	t.Cleanup(func() {
		config = oldConfig
		cmdRemoteSameAsLocal = oldCmdRemoteSameAsLocal
	})

	t.Setenv("WR_ADD_REMOTE_SAME_AS_LOCAL_TEST", "1")

	Convey("remote manager adds do not send submitter environment by default", t, func() {
		config = &internal.Config{}
		cmdRemoteSameAsLocal = false

		envVars := addEnvVars(false, remoteSameAsLocal(false))

		So(envVars, ShouldBeNil)
	})

	Convey("remote manager adds send submitter environment when config opts in", t, func() {
		config = &internal.Config{ManagerRemoteSameAsLocal: true}
		cmdRemoteSameAsLocal = false

		envVars := addEnvVars(false, remoteSameAsLocal(false))

		So(slices.Contains(envVars, "WR_ADD_REMOTE_SAME_AS_LOCAL_TEST=1"), ShouldBeTrue)
	})

	Convey("remote manager adds send submitter environment when CLI opts in", t, func() {
		config = &internal.Config{}
		cmdRemoteSameAsLocal = true

		envVars := addEnvVars(false, remoteSameAsLocal(true))

		So(slices.Contains(envVars, "WR_ADD_REMOTE_SAME_AS_LOCAL_TEST=1"), ShouldBeTrue)
	})

	Convey("remote manager CLI can preserve default env suppression over config", t, func() {
		config = &internal.Config{ManagerRemoteSameAsLocal: true}
		cmdRemoteSameAsLocal = false

		envVars := addEnvVars(false, remoteSameAsLocal(true))

		So(envVars, ShouldBeNil)
	})
}

type testJobSubscription struct {
	updates <-chan *jobqueue.JobUpdate
}

func (s testJobSubscription) Updates() <-chan *jobqueue.JobUpdate {
	return s.updates
}

func (s testJobSubscription) Unsubscribe() {}

type synchronousAddWarningTestClient struct {
	releaseWait chan struct{}
}

func (c synchronousAddWarningTestClient) AddAndReturnIDsWithWarnings(jobs []*jobqueue.Job,
	envVars []string, ignoreComplete bool) ([]string, jobqueue.AddWarnings, error) {
	if len(jobs) != 1 || jobs[0].Cmd != testSyncCommand {
		return nil, jobqueue.AddWarnings{}, errUnexpectedSynchronousJobs
	}

	if !ignoreComplete {
		return nil, jobqueue.AddWarnings{}, errMissingIgnoreComplete
	}

	if len(envVars) != 1 || envVars[0] != testSyncEnv {
		return nil, jobqueue.AddWarnings{}, errUnexpectedSynchronousEnv
	}

	return []string{testWarningJobKey},
		jobqueue.AddWarnings{NeverSeenDepGroups: []string{testFutureDepGroup}},
		nil
}

func (c synchronousAddWarningTestClient) SubscribeToJobKeys(
	ctx context.Context,
	keys []string,
) (jobUpdateSubscription, error) {
	if ctx == nil {
		return nil, errMissingSynchronousAddContext
	}

	if len(keys) != 1 || keys[0] != testWarningJobKey {
		return nil, errUnexpectedSynchronousKeys
	}

	updates := make(chan *jobqueue.JobUpdate, 1)

	go func() {
		select {
		case <-ctx.Done():
		case <-c.releaseWait:
			updates <- &jobqueue.JobUpdate{
				Kind:  jobqueue.JobUpdateTerminal,
				Key:   testWarningJobKey,
				State: jobqueue.JobStateComplete,
			}
		}
	}()

	return testJobSubscription{updates: updates}, nil
}

func (c synchronousAddWarningTestClient) GetByEssence(essence *jobqueue.JobEssence,
	getStd bool, _ bool) (*jobqueue.Job, error) {
	if essence == nil || essence.JobKey != testWarningJobKey || !getStd {
		return nil, errUnexpectedSynchronousKeys
	}

	return &jobqueue.Job{
		Exitcode: 0,
		StdOutC:  zlibCompress([]byte("sync complete")),
	}, nil
}

func TestSynchronousAddPrintsWarningsBeforeWaiting(t *testing.T) {
	Convey("wr add --sync prints never-seen warnings before terminal waiting completes", t, func() {
		releaseWait := make(chan struct{})

		stderrReader, stderrWriter := synchronousAddPipe(t)
		defer stderrReader.Close()

		stdoutFile, err := os.CreateTemp(t.TempDir(), "sync-stdout")
		So(err, ShouldBeNil)

		defer stdoutFile.Close()

		originalStdout, originalStderr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = stdoutFile, stderrWriter

		defer func() {
			os.Stdout, os.Stderr = originalStdout, originalStderr
		}()

		exitCode := make(chan int, 1)
		done := make(chan struct{})

		go func() {
			defer close(done)

			synchronousAddWithExit(
				synchronousAddWarningTestClient{releaseWait: releaseWait},
				&jobqueue.Job{Cmd: testSyncCommand},
				[]string{testSyncEnv},
				true,
				func(code int) {
					exitCode <- code
				},
			)
		}()

		lineRead := make(chan string, 1)
		readErr := make(chan error, 1)

		go func() {
			line, readLineErr := bufio.NewReader(stderrReader).ReadString('\n')
			if readLineErr != nil {
				readErr <- readLineErr

				return
			}

			lineRead <- line
		}()

		select {
		case line := <-lineRead:
			So(line, ShouldEqual, futureDepGroupWarningLine)
		case readLineErr := <-readErr:
			So(readLineErr, ShouldBeNil)
		case <-time.After(time.Second):
			So("timed out waiting for synchronous add warning", ShouldBeBlank)
		}

		_, err = stdoutFile.Seek(0, io.SeekStart)
		So(err, ShouldBeNil)

		stdout, err := io.ReadAll(stdoutFile)
		So(err, ShouldBeNil)
		So(string(stdout), ShouldBeBlank)

		close(releaseWait)

		select {
		case code := <-exitCode:
			So(code, ShouldEqual, 0)
		case <-time.After(time.Second):
			So("timed out waiting for synchronous add exit", ShouldBeBlank)
		}

		select {
		case <-done:
		case <-time.After(time.Second):
			So("timed out waiting for synchronous add completion", ShouldBeBlank)
		}

		So(stderrWriter.Close(), ShouldBeNil)
	})
}

func (c synchronousAddTestClient) AddAndReturnIDsWithWarnings(jobs []*jobqueue.Job,
	envVars []string, ignoreComplete bool) ([]string, jobqueue.AddWarnings, error) {
	if len(jobs) != 1 || jobs[0].Cmd != testSyncCommand {
		return nil, jobqueue.AddWarnings{}, errUnexpectedSynchronousJobs
	}

	if !ignoreComplete {
		return nil, jobqueue.AddWarnings{}, errMissingIgnoreComplete
	}

	if len(envVars) != 1 || envVars[0] != testSyncEnv {
		return nil, jobqueue.AddWarnings{}, errUnexpectedSynchronousEnv
	}

	return []string{testSyncJobKey}, jobqueue.AddWarnings{}, nil
}

func (c synchronousAddTestClient) SubscribeToJobKeys(
	ctx context.Context,
	keys []string,
) (jobUpdateSubscription, error) {
	if ctx == nil {
		return nil, errMissingSynchronousAddContext
	}

	if len(keys) != 1 || keys[0] != testSyncJobKey {
		return nil, errUnexpectedSynchronousKeys
	}

	updates := make(chan *jobqueue.JobUpdate, 1)

	state := jobqueue.JobStateComplete
	if c.exitCode != 0 {
		state = jobqueue.JobStateBuried
	}

	updates <- &jobqueue.JobUpdate{Kind: jobqueue.JobUpdateTerminal, Key: testSyncJobKey, State: state}

	return testJobSubscription{updates: updates}, nil
}

func (c synchronousAddTestClient) GetByEssence(essence *jobqueue.JobEssence,
	getStd bool, _ bool) (*jobqueue.Job, error) {
	if essence == nil || essence.JobKey != testSyncJobKey || !getStd {
		return nil, errUnexpectedSynchronousKeys
	}

	if c.missingJob {
		return nil, nil //nolint:nilnil // Simulates jobqueue.Client.GetByEssence not finding a job.
	}

	return &jobqueue.Job{
		Exitcode: c.exitCode,
		StdOutC:  zlibCompress([]byte(c.stdout)),
		StdErrC:  zlibCompress([]byte(c.stderr)),
	}, nil
}

func TestWaitForSynchronousJobReportsMissingTerminalJob(t *testing.T) {
	Convey("sync add reports an explicit error when a terminal job cannot be fetched", t, func() {
		job, err := waitForSynchronousJob(context.Background(),
			synchronousAddTestClient{missingJob: true}, testSyncJobKey)

		So(job, ShouldBeNil)
		So(errors.Is(err, errSynchronousJobMissing), ShouldBeTrue)
		So(err.Error(), ShouldContainSubstring, testSyncJobKey)
	})
}

func TestAddHelpDocumentsDependencySemantics(t *testing.T) {
	Convey("wr add -h explains live dep-group waits and static command dependencies", t, func() {
		help := compactWhitespace(commandHelpForTest(t, addCmd))

		So(help, ShouldContainSubstring,
			`Dep-group dependencies from "deps" and --deps wait even when the dep-group has not appeared yet`)
		So(help, ShouldContainSubstring,
			`Command dependencies from "cmd_deps" and --cmd_deps keep static behaviour`)
	})
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func commandHelpForTest(t *testing.T, command *cobra.Command) string {
	t.Helper()

	var output bytes.Buffer

	oldOut := command.OutOrStdout()
	oldErr := command.ErrOrStderr()

	command.SetOut(&output)
	command.SetErr(&output)

	defer command.SetOut(oldOut)
	defer command.SetErr(oldErr)

	So(command.Help(), ShouldBeNil)

	return output.String()
}

func TestAddRemoteSameAsLocalCwdDefault(t *testing.T) {
	Convey("remote manager adds default cwd to /tmp by default", t, func() {
		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("cmd one\n"), 0600)
		So(err, ShouldBeNil)

		wd := t.TempDir()
		t.Chdir(wd)
		configureAddParserTest(t, cmdPath)

		cmdCwd = ""
		cmdCwdMatters = false

		jq := &jobqueue.Client{ServerInfo: &jobqueue.ServerInfo{Addr: remoteManagerAddr}}
		jobs, _, _ := parseCmdFile(jq, false, false)

		So(jobs, ShouldHaveLength, 1)
		So(jobs[0].Cwd, ShouldEqual, "/tmp")
	})

	Convey("remote manager adds default cwd like local adds when opted in", t, func() {
		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("cmd one\n"), 0600)
		So(err, ShouldBeNil)

		wd := t.TempDir()
		t.Chdir(wd)
		configureAddParserTest(t, cmdPath)

		cmdCwd = ""
		cmdCwdMatters = false

		jq := &jobqueue.Client{ServerInfo: &jobqueue.ServerInfo{Addr: remoteManagerAddr}}
		jobs, _, _ := parseCmdFile(jq, false, true)

		So(jobs, ShouldHaveLength, 1)
		So(jobs[0].Cwd, ShouldEqual, wd)
	})
}

type synchronousAddTestClient struct {
	exitCode   int
	missingJob bool
	stdout     string
	stderr     string
}

func (c synchronousAddTestClient) AddAndWait(ctx context.Context, jobs []*jobqueue.Job,
	envVars []string, ignoreComplete bool) ([]*jobqueue.Job, error) {
	if ctx == nil {
		return nil, errMissingSynchronousAddContext
	}

	if len(jobs) != 1 || jobs[0].Cmd != testSyncCommand {
		return nil, errUnexpectedSynchronousJobs
	}

	if !ignoreComplete {
		return nil, errMissingIgnoreComplete
	}

	if len(envVars) != 1 || envVars[0] != testSyncEnv {
		return nil, errUnexpectedSynchronousEnv
	}

	return []*jobqueue.Job{{
		Exitcode: c.exitCode,
		StdOutC:  zlibCompress([]byte(c.stdout)),
		StdErrC:  zlibCompress([]byte(c.stderr)),
	}}, nil
}

func zlibCompress(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	var compressed bytes.Buffer

	writer, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		panic(err)
	}

	_, err = writer.Write(data)
	if err != nil {
		panic(err)
	}

	if err = writer.Close(); err != nil {
		panic(err)
	}

	return compressed.Bytes()
}

type synchronousAddExitCode int

func runSynchronousAddHelper(exitCode int, stdout string, stderr string, exit func(int)) {
	synchronousAddWithExit(
		synchronousAddTestClient{exitCode: exitCode, stdout: stdout, stderr: stderr},
		&jobqueue.Job{Cmd: testSyncCommand},
		[]string{testSyncEnv},
		true,
		exit,
	)
}

func TestAddCommandDependenciesDoNotWarnForMissingTargets(t *testing.T) {
	ctx := context.Background()

	Convey("wr add --cmd_deps keeps absent command targets ready and warning-free", t, func() {
		testConfig, serverConfig, addr, _, server, token := startStatusTestServer(ctx, t)
		defer server.Stop(ctx, true)

		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("echo actual\n"), 0o600)
		So(err, ShouldBeNil)

		configureAddParserTest(t, cmdPath)

		config = testConfig
		caFile = testConfig.ManagerCAFile
		timeoutint = 2
		cmdRepGroup = "cmd-missing"
		cmdCmdDeps = "echo missing,"

		stderr := runAddForTest(t)
		So(stderr, ShouldNotContainSubstring, "has not been seen")

		jq, err := jobqueue.Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, 2*time.Second)

		So(err, ShouldBeNil)
		defer func() {
			So(jq.Disconnect(), ShouldBeNil)
		}()

		jobs, err := jq.GetByRepGroup("cmd-missing", false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(jobs, ShouldHaveLength, 1)
		So(jobs[0].State, ShouldEqual, jobqueue.JobStateReady)
		So(jobs[0].WaitingForDepGroups, ShouldBeNil)

		job, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(job, ShouldNotBeNil)

		if job == nil {
			return
		}

		So(job.Key(), ShouldEqual, jobs[0].Key())
	})
}

func TestAddWarnsForNeverSeenDepGroups(t *testing.T) {
	ctx := context.Background()

	Convey("wr add --deps emits one warning and accepts the command", t, func() {
		testConfig, serverConfig, addr, _, server, token := startStatusTestServer(ctx, t)
		defer server.Stop(ctx, true)

		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("echo waits\n"), 0o600)
		So(err, ShouldBeNil)

		configureAddParserTest(t, cmdPath)

		config = testConfig
		caFile = testConfig.ManagerCAFile
		timeoutint = 2
		cmdRepGroup = "add-warning"
		cmdGroupDeps = testFutureDepGroup

		stdout, stderr := runAddCaptureForTest(t)
		So(strings.Count(stderr, futureDepGroupWarningLine), ShouldEqual, 1)
		So(stdout, ShouldContainSubstring, "Added 1 new commands (0 were duplicates) to the queue")

		jq, err := jobqueue.Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, 2*time.Second)

		So(err, ShouldBeNil)
		defer func() {
			So(jq.Disconnect(), ShouldBeNil)
		}()

		jobs, err := jq.GetByRepGroup("add-warning", false, 0, "", false, false)
		So(err, ShouldBeNil)
		So(jobs, ShouldHaveLength, 1)
		So(jobs[0].State, ShouldEqual, jobqueue.JobStateDependent)
		So(jobs[0].WaitingForDepGroups, ShouldResemble, []string{testFutureDepGroup})
	})

	Convey("wr add --simple prints IDs to stdout and warnings to stderr", t, func() {
		testConfig, _, _, _, server, _ := startStatusTestServer(ctx, t)
		defer server.Stop(ctx, true)

		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("echo simple waits\n"), 0o600)
		So(err, ShouldBeNil)

		configureAddParserTest(t, cmdPath)

		config = testConfig
		caFile = testConfig.ManagerCAFile
		timeoutint = 2
		cmdRepGroup = "add-warning-simple"
		cmdGroupDeps = testFutureDepGroup
		simpleOutput = true

		stdout, stderr := runAddCaptureForTest(t)
		So(strings.Count(stderr, futureDepGroupWarningLine), ShouldEqual, 1)
		So(stdout, ShouldNotContainSubstring, "has not been seen")
		So(strings.TrimSpace(stdout), ShouldNotBeBlank)
	})
}

func TestAddDoesNotWarnForSeenDepGroups(t *testing.T) {
	ctx := context.Background()

	Convey("wr add --deps stays quiet for a completed dep group", t, func() {
		testConfig, serverConfig, addr, reqs, server, token := startStatusTestServer(ctx, t)
		defer server.Stop(ctx, true)

		jq, err := jobqueue.Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, 2*time.Second)

		So(err, ShouldBeNil)
		defer func() {
			So(jq.Disconnect(), ShouldBeNil)
		}()

		carrier := &jobqueue.Job{
			Cmd:          "echo completed carrier",
			Cwd:          t.TempDir(),
			ReqGroup:     "cmd-add-test",
			Requirements: reqs,
			Retries:      uint8(0),
			RepGroup:     "seen-carrier",
			DepGroups:    []string{"done"},
		}

		added, existed, err := jq.Add([]*jobqueue.Job{carrier}, nil, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 1)
		So(existed, ShouldEqual, 0)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		if reserved == nil {
			return
		}

		So(jq.Execute(ctx, reserved, testConfig.RunnerExecShell), ShouldBeNil)

		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err = os.WriteFile(cmdPath, []byte("echo seen dependent\n"), 0o600)
		So(err, ShouldBeNil)

		configureAddParserTest(t, cmdPath)

		config = testConfig
		caFile = testConfig.ManagerCAFile
		timeoutint = 2
		cmdRepGroup = "seen-dependent"
		cmdGroupDeps = "done"

		_, stderr := runAddCaptureForTest(t)
		So(stderr, ShouldNotContainSubstring, "has not been seen")
	})
}

func TestAddHeadKeepsFirstParsedCommands(t *testing.T) {
	Convey("wr add --head keeps only the first parsed commands from a command file", t, func() {
		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("\ncmd one\ncmd two\t{\"rep_grp\":\"custom\"}\ncmd three\n"), 0600)
		So(err, ShouldBeNil)

		configureAddParserTest(t, cmdPath)
		So(addCmd.Flags().Set("head", "2"), ShouldBeNil)

		jq := &jobqueue.Client{ServerInfo: &jobqueue.ServerInfo{Addr: remoteManagerAddr}}
		jobs, _, _ := parseCmdFile(jq, false, false)

		So(jobs, ShouldHaveLength, 2)
		So(jobs[0].Cmd, ShouldEqual, "cmd one")
		So(jobs[1].Cmd, ShouldEqual, "cmd two")
		So(jobs[1].RepGroup, ShouldEqual, "custom")
	})
}

func TestAddHeadZeroKeepsAllParsedCommands(t *testing.T) {
	Convey("wr add --head 0 keeps all parsed commands from a command file", t, func() {
		cmdPath := filepath.Join(t.TempDir(), "cmds.txt")
		err := os.WriteFile(cmdPath, []byte("\ncmd one\ncmd two\ncmd three\n"), 0600)
		So(err, ShouldBeNil)

		configureAddParserTest(t, cmdPath)
		So(addCmd.Flags().Set("head", "0"), ShouldBeNil)

		jq := &jobqueue.Client{ServerInfo: &jobqueue.ServerInfo{Addr: remoteManagerAddr}}
		jobs, _, _ := parseCmdFile(jq, false, false)

		So(jobs, ShouldHaveLength, 3)
		So(jobs[0].Cmd, ShouldEqual, "cmd one")
		So(jobs[1].Cmd, ShouldEqual, "cmd two")
		So(jobs[2].Cmd, ShouldEqual, "cmd three")
	})
}

func configureAddParserTest(t *testing.T, cmdPath string) {
	t.Helper()

	oldConfig := config
	oldCmdFile := cmdFile
	oldCmdRepGroup := cmdRepGroup
	oldCmdLimitGroups := cmdLimitGroups
	oldCmdModules := cmdModules
	oldCmdDepGroups := cmdDepGroups
	oldCmdCwd := cmdCwd
	oldCmdCwdMatters := cmdCwdMatters
	oldCmdChangeHome := cmdChangeHome
	oldReqGroup := reqGroup
	oldCmdMem := cmdMem
	oldCmdTime := cmdTime
	oldCmdCPUs := cmdCPUs
	oldCmdDisk := cmdDisk
	oldCmdOvr := cmdOvr
	oldCmdPri := cmdPri
	oldCmdRet := cmdRet
	oldCmdNoRetry := cmdNoRetry
	oldCmdCmdDeps := cmdCmdDeps
	oldCmdGroupDeps := cmdGroupDeps
	oldCmdMonitorDocker := cmdMonitorDocker
	oldCmdWithDocker := cmdWithDocker
	oldCmdWithSingularity := cmdWithSingularity
	oldCmdContainerMounts := cmdContainerMounts
	oldCmdOnFailure := cmdOnFailure
	oldCmdOnSuccess := cmdOnSuccess
	oldCmdOnExit := cmdOnExit
	oldMountJSON := mountJSON
	oldMountSimple := mountSimple
	oldCmdOsPrefix := cmdOsPrefix
	oldCmdOsUsername := cmdOsUsername
	oldCmdOsRAM := cmdOsRAM
	oldCmdFlavor := cmdFlavor
	oldCmdPostCreationScript := cmdPostCreationScript
	oldCmdCloudConfigs := cmdCloudConfigs
	oldCmdCloudSharedDisk := cmdCloudSharedDisk
	oldCmdQueue := cmdQueue
	oldCmdQueuesAvoidAdd := cmdQueuesAvoidAdd
	oldCmdMisc := cmdMisc
	oldCmdEnv := cmdEnv
	oldCmdRemoteSameAsLocal := cmdRemoteSameAsLocal
	oldCmdReRun := cmdReRun
	oldCmdBsubMode := cmdBsubMode
	oldCmdDisableRelativeCheck := cmdDisableRelativeCheck
	oldCmdGroup := cmdGroup
	oldRtimeoutint := rtimeoutint
	oldSimpleOutput := simpleOutput
	oldSyncMode := syncMode

	t.Cleanup(func() {
		config = oldConfig
		cmdFile = oldCmdFile
		cmdRepGroup = oldCmdRepGroup
		cmdLimitGroups = oldCmdLimitGroups
		cmdModules = oldCmdModules
		cmdDepGroups = oldCmdDepGroups
		cmdCwd = oldCmdCwd
		cmdCwdMatters = oldCmdCwdMatters
		cmdChangeHome = oldCmdChangeHome
		reqGroup = oldReqGroup
		cmdMem = oldCmdMem
		cmdTime = oldCmdTime
		cmdCPUs = oldCmdCPUs
		cmdDisk = oldCmdDisk
		cmdOvr = oldCmdOvr
		cmdPri = oldCmdPri
		cmdRet = oldCmdRet
		cmdNoRetry = oldCmdNoRetry
		cmdCmdDeps = oldCmdCmdDeps
		cmdGroupDeps = oldCmdGroupDeps
		cmdMonitorDocker = oldCmdMonitorDocker
		cmdWithDocker = oldCmdWithDocker
		cmdWithSingularity = oldCmdWithSingularity
		cmdContainerMounts = oldCmdContainerMounts
		cmdOnFailure = oldCmdOnFailure
		cmdOnSuccess = oldCmdOnSuccess
		cmdOnExit = oldCmdOnExit
		mountJSON = oldMountJSON
		mountSimple = oldMountSimple
		cmdOsPrefix = oldCmdOsPrefix
		cmdOsUsername = oldCmdOsUsername
		cmdOsRAM = oldCmdOsRAM
		cmdFlavor = oldCmdFlavor
		cmdPostCreationScript = oldCmdPostCreationScript
		cmdCloudConfigs = oldCmdCloudConfigs
		cmdCloudSharedDisk = oldCmdCloudSharedDisk
		cmdQueue = oldCmdQueue
		cmdQueuesAvoidAdd = oldCmdQueuesAvoidAdd
		cmdMisc = oldCmdMisc
		cmdEnv = oldCmdEnv
		cmdRemoteSameAsLocal = oldCmdRemoteSameAsLocal
		cmdReRun = oldCmdReRun
		cmdBsubMode = oldCmdBsubMode
		cmdDisableRelativeCheck = oldCmdDisableRelativeCheck
		cmdGroup = oldCmdGroup
		rtimeoutint = oldRtimeoutint
		simpleOutput = oldSimpleOutput
		syncMode = oldSyncMode

		if addCmd.Flags().Lookup("head") != nil {
			if err := addCmd.Flags().Set("head", "0"); err != nil {
				t.Errorf("reset add --head: %s", err)
			}
		}
	})

	config = &internal.Config{ManagerPort: "4321"}
	cmdFile = cmdPath
	cmdRepGroup = "head-test"
	cmdLimitGroups = ""
	cmdModules = ""
	cmdDepGroups = ""
	cmdCwd = t.TempDir()
	cmdCwdMatters = true
	cmdChangeHome = false
	reqGroup = ""
	cmdMem = "1G"
	cmdTime = "1h"
	cmdCPUs = 1
	cmdDisk = 0
	cmdOvr = "no"
	cmdPri = 0
	cmdRet = 3
	cmdNoRetry = ""
	cmdCmdDeps = ""
	cmdGroupDeps = ""
	cmdMonitorDocker = ""
	cmdWithDocker = ""
	cmdWithSingularity = ""
	cmdContainerMounts = ""
	cmdOnFailure = ""
	cmdOnSuccess = ""
	cmdOnExit = `[{"cleanup":true}]`
	mountJSON = ""
	mountSimple = ""
	cmdOsPrefix = ""
	cmdOsUsername = ""
	cmdOsRAM = 0
	cmdFlavor = ""
	cmdPostCreationScript = ""
	cmdCloudConfigs = ""
	cmdCloudSharedDisk = false
	cmdQueue = ""
	cmdQueuesAvoidAdd = "interactive"
	cmdMisc = ""
	cmdEnv = ""
	cmdRemoteSameAsLocal = false
	cmdReRun = false
	cmdBsubMode = false
	cmdDisableRelativeCheck = true
	cmdGroup = ""
	rtimeoutint = 1
	simpleOutput = false
	syncMode = false
}

func runAddForTest(t *testing.T) string {
	t.Helper()

	_, stderr := runAddCaptureForTest(t)

	return stderr
}

func runAddCaptureForTest(t *testing.T) (string, string) {
	t.Helper()

	stdoutReader, stdoutWriter := synchronousAddPipe(t)
	defer stdoutReader.Close()

	stderrReader, stderrWriter := synchronousAddPipe(t)
	defer stderrReader.Close()

	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter

	func() {
		defer func() {
			os.Stdout, os.Stderr = originalStdout, originalStderr

			So(stdoutWriter.Close(), ShouldBeNil)
			So(stderrWriter.Close(), ShouldBeNil)
		}()

		addCmd.Run(addCmd, nil)
	}()

	stdout, err := io.ReadAll(stdoutReader)
	So(err, ShouldBeNil)

	stderr, err := io.ReadAll(stderrReader)
	So(err, ShouldBeNil)

	return string(stdout), string(stderr)
}

func TestSynchronousAddPrintsStdoutAndExitsZero(t *testing.T) {
	Convey("wr add --sync prints stdout and exits zero for a successful job", t, func() {
		stdout, stderr, exitCode := runSynchronousAddInProcess(t, "success")

		So(exitCode, ShouldEqual, 0)
		So(stdout, ShouldEqual, "sync stdout\n")
		So(stderr, ShouldBeBlank)
	})
}

func TestSynchronousAddExitsWithBuriedJobExitCode(t *testing.T) {
	Convey("wr add --sync exits with the buried job exit code", t, func() {
		stdout, stderr, exitCode := runSynchronousAddInProcess(t, synchronousAddBuriedHelper)

		So(exitCode, ShouldEqual, 3)
		So(stdout, ShouldBeBlank)
		So(stderr, ShouldEqual, "sync stderr\n")
	})
}

func runSynchronousAddInProcess(t *testing.T, helper string) (string, string, int) {
	t.Helper()

	stdoutReader, stdoutWriter := synchronousAddPipe(t)
	defer stdoutReader.Close()

	stderrReader, stderrWriter := synchronousAddPipe(t)
	defer stderrReader.Close()

	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter

	exitCode := 0

	func() {
		defer func() {
			os.Stdout, os.Stderr = originalStdout, originalStderr

			So(stdoutWriter.Close(), ShouldBeNil)
			So(stderrWriter.Close(), ShouldBeNil)

			recovered := recover()
			if recovered == nil {
				return
			}

			code, ok := recovered.(synchronousAddExitCode)
			if !ok {
				panic(recovered)
			}

			exitCode = int(code)
		}()

		runSynchronousAddNamedHelper(helper, func(code int) {
			panic(synchronousAddExitCode(code))
		})
	}()

	stdout, err := io.ReadAll(stdoutReader)
	So(err, ShouldBeNil)

	stderr, err := io.ReadAll(stderrReader)
	So(err, ShouldBeNil)

	return string(stdout), string(stderr), exitCode
}

func synchronousAddPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	return reader, writer
}

func runSynchronousAddNamedHelper(helper string, exit func(int)) {
	switch helper {
	case "success":
		runSynchronousAddHelper(0, "sync stdout", "", exit)
	case synchronousAddBuriedHelper:
		runSynchronousAddHelper(3, "", "sync stderr", exit)
	}
}
