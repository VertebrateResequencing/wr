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
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	statusTestCwd      = "/tmp"
	statusTestDetails  = "details"
	statusTestFalse    = "false"
	statusTestFlagBury = "buried"
	statusTestHost     = "localhost"
	statusTestReqGroup = "status"
	statusTestRepGroup = "status-filter"
)

func TestStatusFiltersPendingAndDependentJobs(t *testing.T) {
	Convey("wr status filters pending and dependent jobs", t, func() {
		ctx := context.Background()
		testConfig, serverConfig, addr, reqs, server, token := startStatusTestServer(ctx, t)

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

		parent := &jobqueue.Job{
			Cmd:          "echo status pending parent",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     statusTestRepGroup,
		}
		inserts, already, err := jq.Add([]*jobqueue.Job{parent}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		child := &jobqueue.Job{
			Cmd:          "echo status dependent child",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     statusTestRepGroup,
			Dependencies: jobqueue.Dependencies{
				jobqueue.NewEssenceDependency(parent.Cmd, ""),
			},
		}
		inserts, already, err = jq.Add([]*jobqueue.Job{child}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)
		So(already, ShouldEqual, 0)

		pendingOutput := runStatusForTest(t, "--pending", "--output", "counts")
		So(pendingOutput, ShouldContainSubstring, "ready: 1\n")
		So(pendingOutput, ShouldContainSubstring, "dependent: 0\n")

		dependentOutput := runStatusForTest(t, "--dependent", "--output", "counts")
		So(dependentOutput, ShouldContainSubstring, "ready: 0\n")
		So(dependentOutput, ShouldContainSubstring, "dependent: 1\n")

		combinedOutput := runStatusForTest(t, "--pending", "--dependent", "--output", "counts")
		So(combinedOutput, ShouldContainSubstring, "ready: 1\n")
		So(combinedOutput, ShouldContainSubstring, "dependent: 1\n")

		identifierOutput := runStatusForTest(t, "--identifier", statusTestRepGroup, "--pending", "--dependent",
			"--output", "counts")
		So(identifierOutput, ShouldContainSubstring, "ready: 1\n")
		So(identifierOutput, ShouldContainSubstring, "dependent: 1\n")
	})

	Convey("wr status validates where state filters can apply", t, func() {
		Convey("allows combined state filters in default and report group modes", func() {
			resetStatusForTest(t)

			showPending = true
			showDependent = true

			states := statusStateFilters()
			So(states, ShouldResemble, []jobqueue.JobState{jobqueue.JobStateReady, jobqueue.JobStateDependent})
			So(validateStatusStateFilters(states), ShouldBeNil)

			cmdIDStatus = statusTestRepGroup

			So(validateStatusStateFilters(states), ShouldBeNil)
		})

		for _, tc := range []struct {
			name  string
			setup func()
			want  string
		}{
			{
				name:  "file mode",
				setup: func() { cmdFileStatus = "commands.txt" },
				want:  "-f",
			},
			{
				name:  "cmdline mode",
				setup: func() { cmdLine = "echo status" },
				want:  "-l",
			},
			{
				name: "internal identifier mode",
				setup: func() {
					cmdIDStatus = "abc123"
					cmdIDIsInternal = true
				},
				want: "--internal",
			},
		} {
			Convey("rejects state filters in "+tc.name, func() {
				resetStatusForTest(t)

				showPending = true

				tc.setup()

				err := validateStatusStateFilters(statusStateFilters())
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "state filters")
				So(err.Error(), ShouldContainSubstring, tc.want)
			})
		}
	})
}

func statusTestServerConfig(t *testing.T) (*internal.Config, jobqueue.ServerConfig, string, *jqs.Requirements) {
	t.Helper()

	tmpDir := t.TempDir()
	port, webPort := freeStatusTestPorts(t)

	testConfig := &internal.Config{
		ManagerHost:       statusTestHost,
		ManagerPort:       port,
		ManagerWeb:        webPort,
		ManagerDBFile:     filepath.Join(tmpDir, "db"),
		ManagerTokenFile:  filepath.Join(tmpDir, "client.token"),
		ManagerCAFile:     filepath.Join(tmpDir, "ca.pem"),
		ManagerCertFile:   filepath.Join(tmpDir, "cert.pem"),
		ManagerKeyFile:    filepath.Join(tmpDir, "key.pem"),
		ManagerCertDomain: statusTestHost,
		RunnerExecShell:   "bash",
		Deployment:        internal.Development,
	}

	serverConfig := jobqueue.ServerConfig{
		Port:            testConfig.ManagerPort,
		WebPort:         testConfig.ManagerWeb,
		SchedulerName:   "local",
		SchedulerConfig: &jqs.ConfigLocal{Shell: testConfig.RunnerExecShell},
		DBFile:          testConfig.ManagerDBFile,
		DBFileBackup:    testConfig.ManagerDBFile + "_bk",
		TokenFile:       testConfig.ManagerTokenFile,
		CAFile:          testConfig.ManagerCAFile,
		CertFile:        testConfig.ManagerCertFile,
		KeyFile:         testConfig.ManagerKeyFile,
		CertDomain:      testConfig.ManagerCertDomain,
		Deployment:      testConfig.Deployment,
	}
	reqs := &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1, Disk: 0, Other: make(map[string]string)}

	return testConfig, serverConfig, statusTestHost + ":" + port, reqs
}

// freeStatusTestPorts returns two distinct free ports, for a test server's
// manager and web listeners. It holds both listeners open at the same time
// before reading their ports, so the two can never come back equal (the old
// approach closed the first listener before opening the second, letting the OS
// hand out the same port twice, which made the web listener fail to bind the
// manager's port). Ephemeral ports also avoid the collisions a fixed per-lane
// scheme would cause when test runs overlap on a shared machine.
func freeStatusTestPorts(t *testing.T) (string, string) {
	t.Helper()

	listenConfig := net.ListenConfig{}

	l1, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)

	defer func() {
		So(l1.Close(), ShouldBeNil)
	}()

	l2, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)

	defer func() {
		So(l2.Close(), ShouldBeNil)
	}()

	a1, ok := l1.Addr().(*net.TCPAddr)
	So(ok, ShouldBeTrue)

	a2, ok := l2.Addr().(*net.TCPAddr)
	So(ok, ShouldBeTrue)

	return strconv.Itoa(a1.Port), strconv.Itoa(a2.Port)
}

func runStatusForTest(t *testing.T, args ...string) string {
	t.Helper()

	resetStatusForTest(t)
	So(statusCmd.ParseFlags(args), ShouldBeNil)

	reader, writer, err := os.Pipe()
	So(err, ShouldBeNil)

	defer reader.Close()

	originalStdout := os.Stdout

	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	statusCmd.Run(statusCmd, nil)

	So(writer.Close(), ShouldBeNil)

	output, err := io.ReadAll(reader)
	So(err, ShouldBeNil)

	return string(output)
}

func resetStatusForTest(t *testing.T) {
	t.Helper()

	cmdFileStatus = ""
	cmdIDStatus = ""
	cmdIDIsSubStr = false
	cmdIDIsInternal = false
	cmdLine = ""
	cmdCwd = ""
	cmdAll = false
	mountJSON = ""
	mountSimple = ""
	showBuried = false
	showRunning = false
	showPending = false
	showDependent = false
	showEnv = false
	outputFormat = statusTestDetails
	statusLimit = 1
	fromHost = ""
	timeoutint = 120

	for _, flag := range []struct {
		name  string
		value string
	}{
		{statusTestFlagBury, statusTestFalse},
		{"running", statusTestFalse},
		{"pending", statusTestFalse},
		{"dependent", statusTestFalse},
		{"env", statusTestFalse},
		{"output", statusTestDetails},
		{"limit", "1"},
		{"timeout", "120"},
	} {
		So(statusCmd.Flags().Set(flag.name, flag.value), ShouldBeNil)
	}
}

// startStatusTestServer builds a test config and starts a server, retrying with
// fresh ports if a chosen port is momentarily in use (which can happen on a
// busy machine in the window between picking a free port and the server binding
// it). It returns the config, server config, connection address, requirements,
// running server and its token.
func startStatusTestServer(ctx context.Context, t *testing.T) (
	*internal.Config, jobqueue.ServerConfig, string, *jqs.Requirements, *jobqueue.Server, []byte,
) {
	t.Helper()

	var (
		testConfig   *internal.Config
		serverConfig jobqueue.ServerConfig
		addr         string
		reqs         *jqs.Requirements
		server       *jobqueue.Server
		token        []byte
		err          error
	)

	for attempt := 0; ; attempt++ {
		testConfig, serverConfig, addr, reqs = statusTestServerConfig(t)

		server, _, token, err = jobqueue.Serve(ctx, serverConfig)
		if err == nil || attempt >= 20 || !strings.Contains(err.Error(), "address already in use") {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	So(err, ShouldBeNil)

	return testConfig, serverConfig, addr, reqs, server, token
}
