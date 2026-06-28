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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/fatih/color"
	. "github.com/smartystreets/goconvey/convey"
)

func TestCloudDeployDebugManagerStartFlags(t *testing.T) {
	oldCloudDebug := cloudDebug

	t.Cleanup(func() {
		cloudDebug = oldCloudDebug
	})

	Convey("cloud deploy debug uses supported manager start flags", t, func() {
		cloudDebug = true

		So(buildDebugArg(), ShouldEqual, " --debug --runner_syslog")
		So(buildDebugArg(), ShouldNotContainSubstring, "--runner_debug")
	})
}

func TestBuildManagerStartCmdDebugFlags(t *testing.T) {
	oldConfig := config
	oldProviderName := providerName
	oldCloudMaxServers := cloudMaxServers
	oldServerKeepAlive := serverKeepAlive
	oldOSPrefix := osPrefix
	oldOSUsername := osUsername
	oldOSRAM := osRAM
	oldOSDisk := osDisk
	oldFlavorRegex := flavorRegex
	oldFlavorSets := flavorSets
	oldPostCreationScript := postCreationScript
	oldPreDestroyScript := preDestroyScript
	oldCloudSpawns := cloudSpawns
	oldCloudCIDR := cloudCIDR
	oldCloudConfigFiles := cloudConfigFiles
	oldCloudDebug := cloudDebug
	oldCloudManagerTimeoutSeconds := cloudManagerTimeoutSeconds
	oldCloudResourceNameUniquer := cloudResourceNameUniquer
	oldMaxManagerCores := maxManagerCores
	oldMaxManagerRAM := maxManagerRAM
	oldCloudServersAutoConfirmDead := cloudServersAutoConfirmDead
	oldMountJSON := mountJSON

	t.Cleanup(func() {
		config = oldConfig
		providerName = oldProviderName
		cloudMaxServers = oldCloudMaxServers
		serverKeepAlive = oldServerKeepAlive
		osPrefix = oldOSPrefix
		osUsername = oldOSUsername
		osRAM = oldOSRAM
		osDisk = oldOSDisk
		flavorRegex = oldFlavorRegex
		flavorSets = oldFlavorSets
		postCreationScript = oldPostCreationScript
		preDestroyScript = oldPreDestroyScript
		cloudSpawns = oldCloudSpawns
		cloudCIDR = oldCloudCIDR
		cloudConfigFiles = oldCloudConfigFiles
		cloudDebug = oldCloudDebug
		cloudManagerTimeoutSeconds = oldCloudManagerTimeoutSeconds
		cloudResourceNameUniquer = oldCloudResourceNameUniquer
		maxManagerCores = oldMaxManagerCores
		maxManagerRAM = oldMaxManagerRAM
		cloudServersAutoConfirmDead = oldCloudServersAutoConfirmDead
		mountJSON = oldMountJSON
	})

	Convey("wr cloud deploy --debug builds a valid remote manager start command", t, func() {
		config = &internal.Config{Deployment: "debug-test"}
		providerName = "openstack"
		cloudMaxServers = 3
		serverKeepAlive = 7
		osPrefix = "Ubuntu 24"
		osUsername = "ubuntu"
		osRAM = 2048
		osDisk = 0
		flavorRegex = ""
		flavorSets = ""
		postCreationScript = ""
		preDestroyScript = ""
		cloudSpawns = 2
		cloudCIDR = "192.168.64.0/18"
		cloudConfigFiles = ""
		cloudDebug = true
		cloudManagerTimeoutSeconds = 60
		cloudResourceNameUniquer = "testuser"
		maxManagerCores = -1
		maxManagerRAM = -1
		cloudServersAutoConfirmDead = 0
		mountJSON = ""

		cmd := buildManagerStartCmd(&cloud.Provider{}, cloud.NewServer("ubuntu", "192.0.2.1", ""), "/tmp/wr", false, false)

		So(cmd, ShouldContainSubstring, "/tmp/wr manager start")
		So(cmd, ShouldContainSubstring, " --debug")
		So(cmd, ShouldContainSubstring, " --runner_syslog")
		So(cmd, ShouldNotContainSubstring, "--runner_debug")
	})
}

func TestConnectToStartedCloudManager(t *testing.T) {
	oldConnect := connectToManager
	oldRetryWait := cloudManagerConnectRetryWait

	t.Cleanup(func() {
		connectToManager = oldConnect
		cloudManagerConnectRetryWait = oldRetryWait
	})

	Convey("cloud deploy retries transient manager connection failures until the start deadline", t, func() {
		cloudManagerConnectRetryWait = time.Nanosecond

		attempts := 0
		connectToManager = func(time.Duration, ...bool) *jobqueue.Client {
			attempts++

			if attempts == 1 {
				return nil
			}

			return &jobqueue.Client{ServerInfo: &jobqueue.ServerInfo{
				Addr: "127.0.0.1:46407",
				Host: "localhost",
				Port: "46407",
			}}
		}

		jq := connectToStartedCloudManager(50 * time.Millisecond)

		So(jq, ShouldNotBeNil)
		So(attempts, ShouldEqual, 2)
	})
}

func TestCleanupDeployForwardingProcesses(t *testing.T) {
	Convey("deploy forwarder cleanup kills process and removes pid files when manager connection fails", t, func() {
		dir := t.TempDir()
		managerForwarder, managerDone := startTestForwarder(t)
		webForwarder, webDone := startTestForwarder(t)
		unrelatedForwarder, unrelatedDone := startTestForwarder(t)
		managerPidPath := filepath.Join(dir, "manager-forwarder.pid")
		webPidPath := filepath.Join(dir, "web-forwarder.pid")
		unrelatedPidPath := filepath.Join(dir, "unrelated-forwarder.pid")

		So(os.WriteFile(managerPidPath, []byte(strconv.Itoa(managerForwarder.Process.Pid)), ownerReadWrite), ShouldBeNil)
		So(os.WriteFile(webPidPath, []byte(strconv.Itoa(webForwarder.Process.Pid)), ownerReadWrite), ShouldBeNil)
		So(os.WriteFile(unrelatedPidPath, []byte(strconv.Itoa(unrelatedForwarder.Process.Pid)), ownerReadWrite), ShouldBeNil)

		cleanupDeployForwardingProcesses(managerPidPath, webPidPath)

		So(fileIsMissing(managerPidPath), ShouldBeTrue)
		So(fileIsMissing(webPidPath), ShouldBeTrue)
		So(fileIsMissing(unrelatedPidPath), ShouldBeFalse)
		So(processExited(managerDone), ShouldBeTrue)
		So(processExited(webDone), ShouldBeTrue)
		So(processIsRunning(unrelatedDone), ShouldBeTrue)
	})
}

func TestHandleManagerConnectFailure(t *testing.T) {
	const (
		managerConnectFailureEventLogs     = "logs"
		managerConnectFailureEventPause    = "pause"
		managerConnectFailureEventResume   = "resume"
		managerConnectFailureEventTeardown = "teardown"
	)

	oldConfig := config
	oldOSUsername := osUsername
	oldDisplayLogs := displayRemoteManagerLogsForFailure
	oldWaitForInput := waitForManagerFailureDebugInput
	oldTeardown := teardownAfterManagerFailure
	oldDie := dieAfterManagerFailure
	oldColorOutput := color.Output
	oldNoColor := color.NoColor

	t.Cleanup(func() {
		config = oldConfig
		osUsername = oldOSUsername
		displayRemoteManagerLogsForFailure = oldDisplayLogs
		waitForManagerFailureDebugInput = oldWaitForInput
		teardownAfterManagerFailure = oldTeardown
		dieAfterManagerFailure = oldDie
		color.Output = oldColorOutput
		color.NoColor = oldNoColor
	})

	Convey("manager connect failure pauses for SSH debug before cleanup and teardown", t, func() {
		dir := t.TempDir()
		managerForwarder, managerDone := startTestForwarder(t)
		webForwarder, webDone := startTestForwarder(t)
		managerPidPath := filepath.Join(dir, "manager-forwarder.pid")
		webPidPath := filepath.Join(dir, "web-forwarder.pid")
		server := cloud.NewServer("ubuntu", "192.0.2.10", "")
		managerStartCmd := "source .wr_envvars && /tmp/wr manager start"

		So(os.WriteFile(managerPidPath, []byte(strconv.Itoa(managerForwarder.Process.Pid)), ownerReadWrite),
			ShouldBeNil)
		So(os.WriteFile(webPidPath, []byte(strconv.Itoa(webForwarder.Process.Pid)), ownerReadWrite), ShouldBeNil)

		config = &internal.Config{Deployment: "connect-failure"}
		osUsername = "ubuntu"
		color.NoColor = true

		var debugOutput bytes.Buffer

		color.Output = &debugOutput

		promptStarted := make(chan struct{})
		releasePrompt := make(chan struct{})
		handlerDone := make(chan struct{})
		teardownState := make(chan bool, 1)

		var (
			mu     sync.Mutex
			events []string
		)

		record := func(event string) {
			mu.Lock()
			defer mu.Unlock()

			events = append(events, event)
		}
		eventsSnapshot := func() []string {
			mu.Lock()
			defer mu.Unlock()

			return append([]string(nil), events...)
		}

		displayRemoteManagerLogsForFailure = func(_ *cloud.Server) {
			record(managerConnectFailureEventLogs)
		}
		waitForManagerFailureDebugInput = func() {
			record(managerConnectFailureEventPause)
			close(promptStarted)
			<-releasePrompt
			record(managerConnectFailureEventResume)
		}
		teardownAfterManagerFailure = func(_ context.Context, _ *cloud.Provider) {
			record(managerConnectFailureEventTeardown)

			teardownState <- processExited(managerDone) &&
				processExited(webDone) &&
				fileIsMissing(managerPidPath) &&
				fileIsMissing(webPidPath)
		}
		dieAfterManagerFailure = func(msg string, _ ...any) {
			record("die:" + msg)
		}

		go func() {
			defer close(handlerDone)

			handleManagerConnectFailure(&cloud.Provider{}, server, "/tmp/test-key", managerStartCmd,
				managerPidPath, webPidPath)
		}()

		select {
		case <-promptStarted:
		case <-handlerDone:
			t.Fatal("manager connect failure handler returned before debug prompt")
		case <-time.After(2 * time.Second):
			t.Fatal("manager connect failure handler did not reach debug prompt")
		}

		So(processIsRunning(managerDone), ShouldBeTrue)
		So(processIsRunning(webDone), ShouldBeTrue)
		So(fileIsMissing(managerPidPath), ShouldBeFalse)
		So(fileIsMissing(webPidPath), ShouldBeFalse)
		So(debugOutput.String(), ShouldContainSubstring, "ssh -i /tmp/test-key ubuntu@192.0.2.10")
		So(debugOutput.String(), ShouldContainSubstring, managerStartCmd)

		close(releasePrompt)

		select {
		case <-handlerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("manager connect failure handler did not finish after debug prompt was released")
		}

		So(<-teardownState, ShouldBeTrue)
		So(eventsSnapshot(), ShouldResemble, []string{
			managerConnectFailureEventLogs,
			managerConnectFailureEventPause,
			managerConnectFailureEventResume,
			managerConnectFailureEventTeardown,
			"die:could not talk to wr manager on server at %s after 40s",
		})
	})
}

func startTestForwarder(t *testing.T) (*exec.Cmd, <-chan error) {
	t.Helper()

	cmd := exec.Command("sleep", "30") //nolint:noctx // test process killed by cleanup under test
	err := cmd.Start()
	So(err, ShouldBeNil)

	if err != nil {
		return cmd, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	t.Cleanup(func() {
		if cmd.ProcessState != nil {
			return
		}

		if err := cmd.Process.Kill(); err != nil {
			t.Logf("failed to kill test forwarder pid %d: %s", cmd.Process.Pid, err)

			return
		}

		<-done
	})

	return cmd, done
}

func fileIsMissing(path string) bool {
	_, err := os.Stat(path)

	return os.IsNotExist(err)
}

func processExited(done <-chan error) bool {
	if done == nil {
		return false
	}

	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func processIsRunning(done <-chan error) bool {
	if done == nil {
		return false
	}

	select {
	case <-done:
		return false
	default:
		return true
	}
}
