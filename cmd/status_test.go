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
	"bytes"
	"context"
	"encoding/json"
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
	statusTestCwd             = "/tmp"
	statusTestDetails         = statusOutputFormatDetails
	statusTestFalse           = "false"
	statusTestFlagBury        = "buried"
	statusTestHost            = "localhost"
	statusTestReqGroup        = "status"
	statusTestRepGroup        = "status-filter"
	statusTestRepGroupA       = "rg-a"
	statusTestFutureDepGroup  = "future"
	statusTestCarrierDepGroup = "carrier"
	statusTestLiveDepGroup    = "live"
)

//nolint:gosmopolitan // This test fixes time.Local to assert local CLI rendering.
func TestStatusAlertTimeFormatting(t *testing.T) {
	Convey("wr status renders scheduler alert Unix timestamps in local time", t, func() {
		oldLocal := time.Local

		time.Local = time.FixedZone("status-alert-test", 90*60)
		defer func() {
			time.Local = oldLocal
		}()

		unixSeconds := time.Date(2024, 3, 9, 12, 0, 0, 0, time.UTC).Unix()

		So(formatStatusAlertTime(unixSeconds), ShouldEqual, "24/3/9-13:30:00")
	})
}

type statusAlertsGetterFunc func() (*jobqueue.SchedulerAlerts, error)

func (f statusAlertsGetterFunc) GetSchedulerAlerts() (*jobqueue.SchedulerAlerts, error) {
	return f()
}

func TestStatusSchedulerAlertsFooter(t *testing.T) {
	Convey("wr status renders scheduler alerts as a footer", t, func() {
		alerts := &jobqueue.SchedulerAlerts{
			Issues: []*jobqueue.SchedulerIssue{
				{
					Msg:       "scheduler backed off",
					FirstDate: 1710000000,
					LastDate:  1710000060,
					Count:     2,
				},
			},
			BadServers: []*jobqueue.BadServer{
				{
					ID:      "serverid-footer-alert",
					Name:    "worker-alert",
					IP:      "192.168.0.9",
					Date:    1710000120,
					IsBad:   true,
					Problem: "boot failed",
				},
				{
					ID:    "serverid-footer-maybe",
					Name:  "worker-maybe",
					IP:    "192.168.0.10",
					Date:  1710000180,
					IsBad: true,
				},
				{
					ID:    "serverid-footer-recovered",
					Name:  "worker-recovered",
					IP:    "192.168.0.11",
					Date:  1710000240,
					IsBad: false,
				},
			},
		}

		var output bytes.Buffer

		writeStatusAlertsFooter(&output, alerts)

		got := output.String()
		So(got, ShouldContainSubstring, "Scheduler alerts:")
		So(got, ShouldContainSubstring, "Scheduler Issue")
		So(got, ShouldContainSubstring, "scheduler backed off")
		So(got, ShouldContainSubstring, "reported 2 times")
		So(got, ShouldContainSubstring, "Bad server")
		So(got, ShouldContainSubstring, "worker-alert")
		So(got, ShouldContainSubstring, "boot failed")
		So(got, ShouldContainSubstring, "worker-maybe")
		So(got, ShouldContainSubstring, "might be dead")
		So(got, ShouldNotContainSubstring, "worker-recovered")
	})

	Convey("wr status skips recovered bad servers in the footer", t, func() {
		alerts := &jobqueue.SchedulerAlerts{
			BadServers: []*jobqueue.BadServer{
				{
					ID:      "serverid-footer-recovered",
					Name:    "worker-recovered",
					IP:      "192.168.0.11",
					Date:    1710000240,
					IsBad:   false,
					Problem: "boot failed",
				},
			},
		}

		var output bytes.Buffer

		writeStatusAlertsFooter(&output, alerts)

		So(output.String(), ShouldBeEmpty)
	})
}

func TestStatusSchedulerAlertsFooterOutputModes(t *testing.T) {
	Convey("wr status leaves count and machine-readable outputs unchanged by scheduler alerts", t, func() {
		calls := 0
		getter := statusAlertsGetterFunc(func() (*jobqueue.SchedulerAlerts, error) {
			calls++

			return statusAlertsForTest(), nil
		})

		for _, format := range []string{
			statusOutputFormatCounts,
			statusOutputFormatCountsAlias,
			statusOutputFormatJSON,
			statusOutputFormatJSONAlias,
			statusOutputFormatPlain,
			statusOutputFormatPlainAlias,
		} {
			var output bytes.Buffer

			writeStatusAlerts(&output, getter, format)

			So(output.String(), ShouldBeEmpty)
		}

		So(calls, ShouldEqual, 0)
	})

	Convey("wr status appends scheduler alerts to human-readable outputs", t, func() {
		for _, format := range []string{
			statusOutputFormatDetails,
			statusOutputFormatDetailsAlias,
			statusOutputFormatSummary,
			statusOutputFormatSummaryAlias,
			statusOutputFormatTable,
			statusOutputFormatTableAlias,
		} {
			var output bytes.Buffer

			writeStatusAlerts(&output, statusAlertsGetterFunc(func() (*jobqueue.SchedulerAlerts, error) {
				return statusAlertsForTest(), nil
			}), format)

			So(output.String(), ShouldContainSubstring, "Scheduler alerts:")
			So(output.String(), ShouldContainSubstring, "scheduler backed off")
		}
	})
}

func statusAlertsForTest() *jobqueue.SchedulerAlerts {
	return &jobqueue.SchedulerAlerts{
		Issues: []*jobqueue.SchedulerIssue{
			{
				Msg: "scheduler backed off",
			},
		},
	}
}

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

func TestStatusFiltersMissingDepGroups(t *testing.T) {
	Convey("wr status --missing_deps filters jobs waiting on never-seen dep groups", t, func() {
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

		missing := &jobqueue.Job{
			Cmd:          "echo status missing dep",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     statusTestRepGroupA,
			Dependencies: jobqueue.Dependencies{
				jobqueue.NewDepGroupDependency(statusTestFutureDepGroup),
			},
		}
		liveDependent := &jobqueue.Job{
			Cmd:          "echo status live dependent",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     statusTestRepGroupA,
			Dependencies: jobqueue.Dependencies{
				jobqueue.NewDepGroupDependency(statusTestLiveDepGroup),
			},
		}
		liveCarrier := &jobqueue.Job{
			Cmd:          "echo status live carrier",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     statusTestRepGroupA,
			DepGroups:    []string{statusTestLiveDepGroup},
		}
		ready := &jobqueue.Job{
			Cmd:          "echo status ready",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     statusTestRepGroupA,
		}
		otherMissing := &jobqueue.Job{
			Cmd:          "echo status other missing dep",
			Cwd:          statusTestCwd,
			ReqGroup:     statusTestReqGroup,
			Requirements: reqs,
			RepGroup:     "other",
			Dependencies: jobqueue.Dependencies{
				jobqueue.NewDepGroupDependency("elsewhere"),
			},
		}

		inserts, already, err := jq.Add(
			[]*jobqueue.Job{missing, liveDependent, liveCarrier, ready, otherMissing},
			os.Environ(),
			true,
		)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 5)
		So(already, ShouldEqual, 0)

		output := runStatusForTest(t, "--missing_deps", "--output", "counts")
		So(output, ShouldContainSubstring, "dependent: 2\n")
		So(output, ShouldContainSubstring, "ready: 0\n")
		So(output, ShouldContainSubstring, "buried: 0\n")

		output = runStatusForTest(t, "--identifier", statusTestRepGroupA, "--missing_deps", "--output", "counts")
		So(output, ShouldContainSubstring, "dependent: 1\n")
		So(output, ShouldContainSubstring, "ready: 0\n")
		So(output, ShouldContainSubstring, "buried: 0\n")

		output = runStatusForTest(t, "--identifier", "rg-", "--search", "--missing_deps", "--output", "counts")
		So(output, ShouldContainSubstring, "dependent: 1\n")
		So(output, ShouldContainSubstring, "ready: 0\n")
		So(output, ShouldContainSubstring, "buried: 0\n")
	})

	Convey("wr status --missing_deps validates where scoped filters can apply", t, func() {
		Convey("allows the filter in default and report group modes", func() {
			resetStatusForTest(t)

			showMissingDeps = true

			So(validateStatusStateFilters(statusStateFilters()), ShouldBeNil)

			cmdIDStatus = statusTestRepGroup

			So(validateStatusStateFilters(statusStateFilters()), ShouldBeNil)
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
			Convey("rejects the filter in "+tc.name, func() {
				resetStatusForTest(t)

				showMissingDeps = true

				tc.setup()

				err := validateStatusStateFilters(statusStateFilters())
				So(err, ShouldNotBeNil)
				So(err.Error(), ShouldContainSubstring, "state filters")
				So(err.Error(), ShouldContainSubstring, tc.want)
			})
		}
	})
}

func TestStatusDisplaysMissingDepGroups(t *testing.T) {
	Convey("wr status displays never-seen dep-group waits distinctly", t, func() {
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

		waiting := addStatusWaitingDepJob(t, jq, reqs)

		details := runStatusForTest(t, "--identifier", waiting.RepGroup, "--output", "details")
		So(details, ShouldContainSubstring, "Status: waiting on dep group(s) not yet seen: "+statusTestFutureDepGroup)

		table := runStatusForTest(t, "--identifier", waiting.RepGroup, "--output", "table")
		So(table, ShouldContainSubstring, "waiting-deps")

		plain, exitCode := runStatusForTestWithExit(t, "--identifier", waiting.RepGroup, "--output", "plain")
		So(exitCode, ShouldEqual, 0)
		So(plain, ShouldContainSubstring, waiting.Key()+"\twaiting-deps\n")

		jsonOutput := runStatusForTest(t, "--identifier", waiting.RepGroup, "--output", "json")

		var statuses []map[string]json.RawMessage
		So(json.Unmarshal([]byte(jsonOutput), &statuses), ShouldBeNil)
		So(statuses, ShouldHaveLength, 1)

		var state string
		So(json.Unmarshal(statuses[0]["State"], &state), ShouldBeNil)
		So(state, ShouldEqual, string(jobqueue.JobStateDependent))

		var depGroups []string
		So(json.Unmarshal(statuses[0]["DepGroups"], &depGroups), ShouldBeNil)
		So(depGroups, ShouldResemble, []string{statusTestCarrierDepGroup})

		var waitingGroups []string
		So(json.Unmarshal(statuses[0]["WaitingForDepGroups"], &waitingGroups), ShouldBeNil)
		So(waitingGroups, ShouldResemble, []string{statusTestFutureDepGroup})

		_, hasLowerState := statuses[0]["state"]
		_, hasSnakeDepGroups := statuses[0]["dep_groups"]
		_, hasSnakeWaitingGroups := statuses[0]["waiting_for_dep_groups"]

		So(hasLowerState, ShouldBeFalse)
		So(hasSnakeDepGroups, ShouldBeFalse)
		So(hasSnakeWaitingGroups, ShouldBeFalse)

		counts := runStatusForTest(t, "--identifier", waiting.RepGroup, "--output", "counts")
		So(counts, ShouldContainSubstring, "dependent: 1\n")
	})
}

func TestStatusTableOutput(t *testing.T) {
	Convey("wr status renders an aligned table", t, func() {
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

		jobs := []*jobqueue.Job{
			{
				Cmd:          "echo status table one",
				Cwd:          statusTestCwd,
				ReqGroup:     statusTestReqGroup,
				Requirements: reqs,
				RepGroup:     statusTestRepGroup,
			},
			{
				Cmd:          "echo status table two",
				Cwd:          statusTestCwd,
				ReqGroup:     statusTestReqGroup,
				Requirements: reqs,
				RepGroup:     statusTestRepGroup,
			},
		}
		inserts, already, err := jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)
		So(already, ShouldEqual, 0)

		output := runStatusForTest(t, "--output", "table")
		lines := nonEmptyStatusLines(output)

		So(lines, ShouldHaveLength, 2)
		So(lines[0], ShouldContainSubstring, "Command")
		So(lines[0], ShouldContainSubstring, "ID")
		So(lines[0], ShouldContainSubstring, "Status")
		So(lines[0], ShouldContainSubstring, "Attempts")
		So(lines[0], ShouldContainSubstring, "Host")
		So(lines[0], ShouldContainSubstring, "Requirements group")
		So(lines[0], ShouldContainSubstring, "Count")
		So(lines[1], ShouldContainSubstring, "echo status table")
		So(lines[1], ShouldContainSubstring, "ready")
		So(lines[1], ShouldContainSubstring, statusTestReqGroup)
		So(lines[1], ShouldContainSubstring, "2")
		So(strings.Count(output, "echo status table"), ShouldEqual, 1)

		t.Setenv("WR_STATUS_FORMAT", "status:9 count:5")

		output = runStatusForTest(t, "--output", "t")
		lines = nonEmptyStatusLines(output)

		So(lines, ShouldHaveLength, 2)
		So(lines[0], ShouldContainSubstring, "Status")
		So(lines[0], ShouldContainSubstring, "Count")
		So(lines[0], ShouldNotContainSubstring, "Command")
		So(lines[1], ShouldContainSubstring, "ready")
		So(lines[1], ShouldContainSubstring, "2")
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

func addStatusWaitingDepJob(t *testing.T, jq *jobqueue.Client, reqs *jqs.Requirements) *jobqueue.Job {
	t.Helper()

	waiting := &jobqueue.Job{
		Cmd:          "echo status waiting dep",
		Cwd:          statusTestCwd,
		ReqGroup:     statusTestReqGroup,
		Requirements: reqs,
		RepGroup:     "status-waiting-deps",
		DepGroups:    []string{statusTestCarrierDepGroup},
		Dependencies: jobqueue.Dependencies{
			jobqueue.NewDepGroupDependency(statusTestFutureDepGroup),
		},
	}

	inserts, already, err := jq.Add([]*jobqueue.Job{waiting}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)
	So(already, ShouldEqual, 0)

	return waiting
}

func runStatusForTestWithExit(t *testing.T, args ...string) (string, int) {
	t.Helper()

	exitCode := -1
	oldStatusExit := statusExit

	statusExit = func(code int) {
		exitCode = code
	}
	defer func() {
		statusExit = oldStatusExit
	}()

	return runStatusForTest(t, args...), exitCode
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
	showMissingDeps = false
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
		{"missing_deps", statusTestFalse},
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

func nonEmptyStatusLines(output string) []string {
	var lines []string

	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}

	return lines
}
