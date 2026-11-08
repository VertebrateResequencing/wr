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
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/cloud"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/fatih/color"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// cloudTestExportPayload is what a test's exportStore writes.
	cloudTestExportPayload = "exported store"

	// cloudTestStoreMarker is a file in a test's store directory, so a test
	// can tell where that directory went.
	cloudTestStoreMarker = "marker"

	// cloudTestBackupPayload is what a test's teardown sync backup writes.
	cloudTestBackupPayload = "remote backup"

	// ownerReadWriteExecute is the mode of a test's directories, and
	// ownerReadExecute that of one a test cannot write.
	ownerReadWriteExecute = 0o700
	ownerReadExecute      = 0o500
)

// errCloudTestExport is the error a test's exportStore fails with.
var errCloudTestExport = errors.New("export failed")

// errCloudTestBackup is the error a test's teardown sync backup fails with.
var errCloudTestBackup = errors.New("backup failed")

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
				Host: statusTestHost,
				Port: "46407",
			}}
		}

		jq := connectToStartedCloudManager(50 * time.Millisecond)

		So(jq, ShouldNotBeNil)
		So(attempts, ShouldEqual, 2)
	})
}

func TestCloudDeployWebsiteURL(t *testing.T) {
	oldDeployFQDN := cloudDeployFQDN

	t.Cleanup(func() {
		cloudDeployFQDN = oldDeployFQDN
	})

	Convey("cloud deploy web URL uses the deploy host with manager-start formatting", t, func() {
		cloudDeployFQDN = func(context.Context) string {
			return "deploy-host.example.org"
		}
		remoteManager := &jobqueue.ServerInfo{
			Host:    statusTestHost,
			FQDN:    "remote-openstack-manager.example.org",
			WebPort: "46408",
		}
		token := []byte("test-token")

		expected := websiteURL(&jobqueue.ServerInfo{
			FQDN:    "deploy-host.example.org",
			WebPort: "46408",
		}, token)

		So(cloudDeployWebsiteURL(context.Background(), remoteManager, token), ShouldEqual, expected)
		So(cloudDeployWebsiteURL(context.Background(), remoteManager, token), ShouldNotContainSubstring, statusTestHost)
		So(cloudDeployWebsiteURL(context.Background(), remoteManager, token), ShouldNotContainSubstring,
			"remote-openstack-manager")
	})
}

// failingCloudTestBackup is a teardown sync's backup that fails.
func failingCloudTestBackup(string) error {
	return errCloudTestBackup
}

// cloudUpload is what a test's uploadDBFile was given: the remote path, the
// local path and the local file's content at the time.
type cloudUpload struct {
	local, remote string
	content       []byte
}

// recordCloudUploads replaces uploadDBFile, for the rest of the test, with one
// that uploads nothing but appends what it was given to the returned slice.
func recordCloudUploads(t *testing.T) *[]cloudUpload {
	t.Helper()

	oldUpload := uploadDBFile

	t.Cleanup(func() { uploadDBFile = oldUpload })

	var uploads []cloudUpload

	uploadDBFile = func(_ context.Context, _ *cloud.Server, local, remote string) error {
		content, err := os.ReadFile(local)
		uploads = append(uploads, cloudUpload{local: local, remote: remote, content: content})

		return err
	}

	return &uploads
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

func TestStartTestForwarderArgv(t *testing.T) {
	Convey("a started test forwarder already reads as a forwarder", t, func() {
		// before the fix up to 1 in 100 starts read an empty argv
		const starts = 1000

		notForwarders := 0

		for range starts {
			forwarder, done := startTestForwarder(t)
			if !isForwarderProcess(forwarder.Process.Pid) {
				notForwarders++
			}

			_ = syscall.Kill(-forwarder.Process.Pid, syscall.SIGKILL) //nolint:errcheck

			<-done
		}

		So(notForwarders, ShouldEqual, 0)
	})
}

func TestCheckProcessStalePid(t *testing.T) {
	Convey("a forwarder pid file naming an unrelated live process is not running, and cleanup spares it", t, func() {
		dir := t.TempDir()
		unrelated, unrelatedDone := startTestProcess(t, "sleep", "30")
		pidPath := filepath.Join(dir, "forwarder.pid")

		So(os.WriteFile(pidPath, []byte(strconv.Itoa(unrelated.Process.Pid)), ownerReadWrite), ShouldBeNil)

		pid, running := checkProcess(pidPath)
		So(pid, ShouldEqual, unrelated.Process.Pid)
		So(running, ShouldBeFalse)

		cleanupDeployForwardingProcesses(pidPath)

		So(fileIsMissing(pidPath), ShouldBeTrue)
		So(processExited(unrelatedDone), ShouldBeFalse)
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

// startTestForwarder starts a process whose argv has the shape of an ssh
// forwarder that startForwarding() runs (sh keeps its own argv, so the extra
// arguments are visible in its cmdline), so checkProcess recognises it.
func startTestForwarder(t *testing.T) (*exec.Cmd, <-chan error) {
	t.Helper()

	return startTestProcess(t, "sh", "-c", "sleep 30 & wait", sshForwarderFlags, "1234:0.0.0.0:1234", "user@host")
}

// startTestProcess starts name with args in its own process group, returning a
// channel that receives its Wait() result. The whole group is killed when the
// test ends. It returns only once the child's argv reads as name and args.
func startTestProcess(t *testing.T, name string, args ...string) (*exec.Cmd, <-chan error) {
	t.Helper()

	cmd := exec.Command(name, args...) //nolint:noctx // test process killed by cleanup under test
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck
	})

	waitForTestProcessArgv(cmd)

	return cmd, done
}

func TestCloudDeployUploadsLocalDB(t *testing.T) {
	oldConfig := config

	t.Cleanup(func() { config = oldConfig })

	Convey("Given a production deployment with a local db backup location", t, func() {
		dir := t.TempDir()
		config = &internal.Config{
			Deployment:      internal.Production,
			ManagerDBFile:   filepath.Join(dir, "db"),
			ManagerDBBkFile: filepath.Join(dir, "db_bk"),
		}
		server := cloud.NewServer("ubuntu", "192.0.2.10", "")
		remote := filepath.Join(".wr_"+internal.Production, "db")
		uploads := recordCloudUploads(t)

		Convey("with a bbolt file as its db file, the upload step sends that file unchanged, without "+
			"ExportStore", func() {
			exports := recordExportStoreCalls(t, "", errCloudTestExport)

			db, err := bolt.Open(config.ManagerDBFile, ownerReadWrite, nil)
			So(err, ShouldBeNil)
			So(db.Update(func(tx *bolt.Tx) error {
				b, errc := tx.CreateBucket([]byte("jobslive"))
				if errc != nil {
					return errc
				}

				return b.Put([]byte("key"), []byte("value"))
			}), ShouldBeNil)
			So(db.Close(), ShouldBeNil)

			want, err := os.ReadFile(config.ManagerDBFile)
			So(err, ShouldBeNil)

			So(bootstrapDBBackupLocation(&cloud.Provider{}, server, false), ShouldEqual, defaultDBBackupName)
			So(*uploads, ShouldHaveLength, 1)
			So((*uploads)[0].remote, ShouldEqual, remote)
			So(sha256.Sum256((*uploads)[0].content), ShouldEqual, sha256.Sum256(want))
			So(*exports, ShouldBeEmpty)
		})

		Convey("with a store directory as its db file, the upload step sends the file ExportStore made of "+
			"it to .wr_<deployment>/db, removing it afterwards", func() {
			So(os.Mkdir(config.ManagerDBFile, ownerReadWriteExecute), ShouldBeNil)

			exports := recordExportStoreCalls(t, cloudTestExportPayload, nil)

			So(bootstrapDBBackupLocation(&cloud.Provider{}, server, false), ShouldEqual, defaultDBBackupName)
			So(*exports, ShouldResemble, []string{config.ManagerDBFile})
			So(*uploads, ShouldHaveLength, 1)
			So((*uploads)[0].remote, ShouldEqual, remote)
			So(string((*uploads)[0].content), ShouldEqual, cloudTestExportPayload)
			So(filepath.Dir((*uploads)[0].local), ShouldEqual, dir)
			So(fileIsMissing((*uploads)[0].local), ShouldBeTrue)
			soDirHoldsOnly(dir, "db")
		})

		Convey("with a store directory as its db file that ExportStore fails on, the upload step returns "+
			"its error, uploading nothing and leaving no file behind", func() {
			So(os.Mkdir(config.ManagerDBFile, ownerReadWriteExecute), ShouldBeNil)

			recordExportStoreCalls(t, "", errCloudTestExport)

			So(uploadLocalDB(context.Background(), server, config.ManagerDBFile, remote), ShouldWrap,
				errCloudTestExport)
			So(*uploads, ShouldBeEmpty)
			soDirHoldsOnly(dir, "db")
		})
	})
}

// recordExportStoreCalls replaces exportStore, for the rest of the test, with
// one that writes payload, or returns err if it is not nil, and appends the
// db dir it was given to the returned slice.
func recordExportStoreCalls(t *testing.T, payload string, err error) *[]string {
	t.Helper()

	oldExport := exportStore

	t.Cleanup(func() { exportStore = oldExport })

	var calls []string

	exportStore = func(dbDir string, w io.Writer) error {
		calls = append(calls, dbDir)

		if err != nil {
			return err
		}

		_, errw := io.WriteString(w, payload)

		return errw
	}

	return &calls
}

func TestCloudTeardownSetsLocalDBAside(t *testing.T) {
	oldConfig := config

	t.Cleanup(func() { config = oldConfig })

	Convey("Given a local store directory db holding a file", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "db")
		old := dbFile + ".old"
		config = &internal.Config{ManagerDBFile: dbFile, ManagerDBBkFile: "s3://bucket/path/db_bk"}

		So(os.Mkdir(dbFile, ownerReadWriteExecute), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dbFile, cloudTestStoreMarker), []byte("store"), ownerReadWrite), ShouldBeNil)

		staleDir := func() {
			So(os.Mkdir(old, ownerReadWriteExecute), ShouldBeNil)
			So(os.WriteFile(filepath.Join(old, "stale"), []byte("stale"), ownerReadWrite), ShouldBeNil)
		}

		soSetAside := func() {
			content, err := os.ReadFile(filepath.Join(old, cloudTestStoreMarker))
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, "store")
			So(fileIsMissing(filepath.Join(old, "stale")), ShouldBeTrue)
			So(fileIsMissing(dbFile), ShouldBeTrue)
		}

		Convey("and a stale db.old directory, teardown's rename step for a local backup location makes "+
			"db.old the store directory, leaving db free for the backup file", func() {
			staleDir()

			setAside, err := freeLocalDBForBackup(dbFile)
			So(err, ShouldBeNil)
			So(setAside, ShouldBeTrue)
			soSetAside()
		})

		Convey("and a stale db.old directory holding files, teardown's rename step for an S3 backup location "+
			"makes db.old the store directory, leaving no db and logging no warning", func() {
			staleDir()

			logged := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			So(moveLocalDBAsideForS3(), ShouldNotBeEmpty)
			soSetAside()
			So(logged.String(), ShouldBeEmpty)
		})

		Convey("and a stale bbolt file at db.old, teardown's rename step for an S3 backup location makes "+
			"db.old the store directory, leaving no db and logging no warning", func() {
			db, err := bolt.Open(old, ownerReadWrite, nil)
			So(err, ShouldBeNil)
			So(db.Close(), ShouldBeNil)

			logged := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			So(moveLocalDBAsideForS3(), ShouldNotBeEmpty)
			soSetAside()
			So(logged.String(), ShouldBeEmpty)
		})

		Convey("in a directory that cannot be written, teardown's rename step for an S3 backup location "+
			"warns that it could not rename the local database, adds nothing to teardown's message and "+
			"leaves db as it was", func() {
			So(os.Chmod(dir, ownerReadExecute), ShouldBeNil)

			Reset(func() { So(os.Chmod(dir, ownerReadWriteExecute), ShouldBeNil) })

			logged := clog.ToBufferAtLevel("warn")

			defer clog.ToDefault()

			So(moveLocalDBAsideForS3(), ShouldBeEmpty)
			So(logged.String(), ShouldContainSubstring, "could not rename the local database")
			soStoreDirAt(dbFile)
		})

		Convey("in a directory that cannot be searched, teardown's rename step for a local backup location "+
			"fails with the error of looking at db", func() {
			So(os.Chmod(dir, 0), ShouldBeNil)

			Reset(func() { So(os.Chmod(dir, ownerReadWriteExecute), ShouldBeNil) })

			_, err := freeLocalDBForBackup(dbFile)
			So(err, ShouldWrap, fs.ErrPermission)
		})
	})

	Convey("Given no local db and a stale db.old directory, teardown's rename step for an S3 backup "+
		"location adds nothing to teardown's message, logs no warning and leaves db.old alone", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "db")
		config = &internal.Config{ManagerDBFile: dbFile, ManagerDBBkFile: "s3://bucket/path/db_bk"}

		So(os.Mkdir(dbFile+".old", ownerReadWriteExecute), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dbFile+".old", "stale"), []byte("stale"), ownerReadWrite), ShouldBeNil)

		logged := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		So(moveLocalDBAsideForS3(), ShouldBeEmpty)
		So(logged.String(), ShouldBeEmpty)
		soDirHoldsOnly(dir, "db.old")
		soDirHoldsOnly(dbFile+".old", "stale")
	})

	Convey("Given a local bbolt file db and a stale db.old, teardown's rename step for a local backup "+
		"location leaves both alone, since the backup file replaces db as before", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "db")

		So(os.WriteFile(dbFile, []byte("bbolt"), ownerReadWrite), ShouldBeNil)
		So(os.WriteFile(dbFile+".old", []byte("old"), ownerReadWrite), ShouldBeNil)

		setAside, err := freeLocalDBForBackup(dbFile)
		So(err, ShouldBeNil)
		So(setAside, ShouldBeFalse)

		content, err := os.ReadFile(dbFile)
		So(err, ShouldBeNil)
		So(string(content), ShouldEqual, "bbolt")

		content, err = os.ReadFile(dbFile + ".old")
		So(err, ShouldBeNil)
		So(string(content), ShouldEqual, "old")
	})
}

func TestCloudTeardownSyncsLocalDB(t *testing.T) {
	Convey("Given a local store directory db holding a file and a stale db.old", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "db")
		old := dbFile + ".old"

		So(os.Mkdir(dbFile, ownerReadWriteExecute), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dbFile, cloudTestStoreMarker), []byte("store"), ownerReadWrite), ShouldBeNil)
		So(os.WriteFile(old, []byte("stale"), ownerReadWrite), ShouldBeNil)

		Convey("a teardown sync whose backup fails returns its error, leaving db the store directory and "+
			"db.old as they were", func() {
			So(syncLocalDB(dbFile, failingCloudTestBackup), ShouldWrap, errCloudTestBackup)
			soStoreDirAt(dbFile)
			soFileHolds(old, "stale")
			soDirHoldsOnly(dir, "db", "db.old")
		})

		Convey("a teardown sync makes db the remote's backup file and db.old the store directory", func() {
			So(syncLocalDB(dbFile, writingCloudTestBackup), ShouldBeNil)
			soFileHolds(dbFile, cloudTestBackupPayload)
			soStoreDirAt(old)
			soDirHoldsOnly(dir, "db", "db.old")
		})

		Convey("a teardown sync whose backup file cannot be moved to db returns that error, leaving db the "+
			"store directory", func() {
			err := syncLocalDB(dbFile, func(string) error { return nil })
			So(err, ShouldWrap, fs.ErrNotExist)
			So(err.Error(), ShouldContainSubstring, "the remote database is at "+dbFile+".sync")
			soStoreDirAt(dbFile)
			soDirHoldsOnly(dir, "db")
		})

		Convey("a teardown sync that cannot set the store directory aside returns that error, naming "+
			"db.sync as where the remote database is, and leaving db and db.old as they were", func() {
			Reset(func() { So(os.Chmod(dir, ownerReadWriteExecute), ShouldBeNil) })

			err := syncLocalDB(dbFile, func(path string) error {
				if errw := writingCloudTestBackup(path); errw != nil {
					return errw
				}

				return os.Chmod(dir, ownerReadExecute)
			})
			So(err, ShouldWrap, fs.ErrPermission)
			So(err.Error(), ShouldContainSubstring, "the remote database is at "+dbFile+".sync")
			soStoreDirAt(dbFile)
			soFileHolds(old, "stale")
			soFileHolds(dbFile+".sync", cloudTestBackupPayload)
			soDirHoldsOnly(dir, "db", "db.old", "db.sync")
		})
	})

	Convey("Given no local db", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "db")

		Convey("a teardown sync makes db the remote's backup file, leaving nothing else", func() {
			So(syncLocalDB(dbFile, writingCloudTestBackup), ShouldBeNil)
			soFileHolds(dbFile, cloudTestBackupPayload)
			soDirHoldsOnly(dir, "db")
		})
	})

	Convey("Given a local bbolt file db and a stale db.old", t, func() {
		dir := t.TempDir()
		dbFile := filepath.Join(dir, "db")

		db, err := bolt.Open(dbFile, ownerReadWrite, nil)
		So(err, ShouldBeNil)
		So(db.Update(func(tx *bolt.Tx) error {
			_, errc := tx.CreateBucket([]byte("jobslive"))

			return errc
		}), ShouldBeNil)
		So(db.Close(), ShouldBeNil)

		want, err := os.ReadFile(dbFile)
		So(err, ShouldBeNil)
		So(os.WriteFile(dbFile+".old", []byte("old"), ownerReadWrite), ShouldBeNil)

		Convey("a teardown sync whose backup fails returns its error, leaving db unchanged", func() {
			So(syncLocalDB(dbFile, failingCloudTestBackup), ShouldWrap, errCloudTestBackup)

			got, errr := os.ReadFile(dbFile)
			So(errr, ShouldBeNil)
			So(sha256.Sum256(got), ShouldEqual, sha256.Sum256(want))
			soFileHolds(dbFile+".old", "old")
			soDirHoldsOnly(dir, "db", "db.old")
		})

		Convey("a teardown sync replaces db with the remote's backup file, leaving db.old alone", func() {
			So(syncLocalDB(dbFile, writingCloudTestBackup), ShouldBeNil)
			soFileHolds(dbFile, cloudTestBackupPayload)
			soFileHolds(dbFile+".old", "old")
			soDirHoldsOnly(dir, "db", "db.old")
		})
	})
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

// soStoreDirAt asserts that path is a test's store directory, holding its
// marker.
func soStoreDirAt(path string) {
	soFileHolds(filepath.Join(path, cloudTestStoreMarker), "store")
}

// soFileHolds asserts that the file at path holds content.
func soFileHolds(path, content string) {
	got, err := os.ReadFile(path)
	So(err, ShouldBeNil)
	So(string(got), ShouldEqual, content)
}

// soDirHoldsOnly asserts that dir holds exactly the entries names.
func soDirHoldsOnly(dir string, names ...string) {
	entries, err := os.ReadDir(dir)
	So(err, ShouldBeNil)

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}

	So(got, ShouldResemble, names)
}

// writingCloudTestBackup is a teardown sync's backup that writes
// cloudTestBackupPayload to path.
func writingCloudTestBackup(path string) error {
	return os.WriteFile(path, []byte(cloudTestBackupPayload), ownerReadWrite)
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
