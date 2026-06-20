// Copyright © 2016-2021,2024,2025 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
		testConfig, serverConfig, addr, reqs := statusTestServerConfig(t)

		oldConfig, oldCAFile := config, caFile

		config, caFile = testConfig, testConfig.ManagerCAFile
		defer func() {
			config, caFile = oldConfig, oldCAFile
		}()

		server, _, token, err := jobqueue.Serve(ctx, serverConfig)
		So(err, ShouldBeNil)

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
	port := freeStatusTestPort(t)
	webPort := freeStatusTestPort(t)

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

func freeStatusTestPort(t *testing.T) string {
	t.Helper()

	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)

	defer func() {
		So(listener.Close(), ShouldBeNil)
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	So(ok, ShouldBeTrue)

	return strconv.Itoa(addr.Port)
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
