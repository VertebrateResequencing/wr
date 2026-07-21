//go:build !windows

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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

var errTimedOutWaitingForServeTokenRead = errors.New("timed out waiting for Serve to read the initial token")

var errTimedOutWaitingForServeTokenWrite = errors.New("timed out waiting for Serve to write the startup token")

type serveStartupResult struct {
	server *Server
	token  []byte
	err    error
}

func waitForServeStartup(t *testing.T, result <-chan serveStartupResult) serveStartupResult {
	t.Helper()

	select {
	case startup := <-result:
		return startup
	case <-time.After(2 * time.Second):
		So("timed out waiting for Serve to finish startup", ShouldBeBlank)

		return serveStartupResult{}
	}
}

func TestServeReportsPostUpgradeStartupUntilTokenReady(t *testing.T) {
	Convey("Serve keeps a live startup-progress sidecar after a DB upgrade until the token is ready", t, func() {
		ctx := context.Background()
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		prepareDBNeedingStartupUpgrade(ctx, t, serverConfig)
		initialTokenWritten := prepareFIFOBackedToken(t, serverConfig.TokenFile)

		serveResult := make(chan serveStartupResult, 1)

		go func() {
			server, _, token, err := Serve(ctx, serverConfig)
			serveResult <- serveStartupResult{server: server, token: token, err: err}
		}()

		So(waitForFIFOInitialToken(initialTokenWritten), ShouldBeNil)

		status, found := waitForDBUpgradeStatusDetail(
			serverConfig.DBFile,
			postUpgradeStartupDetail,
			2*time.Second,
		)

		tokenInfo, statErr := os.Stat(serverConfig.TokenFile)
		So(statErr, ShouldBeNil)
		So(tokenInfo.Size(), ShouldEqual, 0)
		So(found, ShouldBeTrue)
		So(status.State, ShouldEqual, "start manager after database upgrade")
		So(status.Detail, ShouldEqual, postUpgradeStartupDetail)
		So(status.PID, ShouldEqual, os.Getpid())

		tokenRead := readFIFOAsync(serverConfig.TokenFile)

		result := waitForServeStartup(t, serveResult)
		if result.server != nil {
			defer result.server.Stop(ctx, true)
		}

		So(result.err, ShouldBeNil)

		tokenReadResult := waitForFIFORead(t, serverConfig.TokenFile, tokenRead)
		So(tokenReadResult.err, ShouldBeNil)
		So(tokenReadResult.payload, ShouldResemble, result.token)

		_, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)
	})
}

func prepareDBNeedingStartupUpgrade(ctx context.Context, t *testing.T, config ServerConfig) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	parent := testDBJob("echo parent", "startup-upgrade-parent")
	parent.DepGroups = []string{"startup-upgrade-parent-dg"}

	child := testDBJob("echo child", "startup-upgrade-child")
	child.Dependencies = Dependencies{NewDepGroupDependency("startup-upgrade-parent-dg")}

	jobsToQueue, jobsToUpdate, alreadyAdded, err := testDB.storeNewJobs(ctx, []*Job{parent, child}, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, 2)
	So(jobsToUpdate, ShouldHaveLength, 0)
	So(alreadyAdded, ShouldEqual, 0)

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		if errd := tx.DeleteBucket(bucketDepGroups); errd != nil {
			return errd
		}

		return tx.DeleteBucket(bucketJobLookupEntries)
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)
}

func prepareFIFOBackedToken(t *testing.T, tokenFile string) <-chan error {
	t.Helper()

	err := syscall.Mkfifo(tokenFile, ownerReadWrite)
	So(err, ShouldBeNil)

	initialToken := []byte("0123456789012345678901234567890123456789012")
	written := make(chan error, 1)

	go func() {
		fifo, errw := os.OpenFile(tokenFile, os.O_WRONLY, 0)
		if errw != nil {
			written <- errw

			return
		}

		_, errw = fifo.Write(initialToken)
		if errc := fifo.Close(); errw == nil {
			errw = errc
		}

		written <- errw
	}()

	return written
}

func waitForFIFOInitialToken(written <-chan error) error {
	select {
	case err := <-written:
		return err
	case <-time.After(2 * time.Second):
		return errTimedOutWaitingForServeTokenRead
	}
}

func waitForDBUpgradeStatusDetail(dbFile, detail string, timeout time.Duration) (internal.DBUpgradeStatus, bool) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		status, _, err := internal.ReadDBUpgradeStatus(dbFile)
		if err == nil && status.Detail == detail {
			return status, true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return internal.DBUpgradeStatus{}, false
}

func readFIFOAsync(tokenFile string) <-chan fifoReadResult {
	result := make(chan fifoReadResult, 1)

	go func() {
		fifo, err := os.OpenFile(tokenFile, os.O_RDONLY, 0)
		if err != nil {
			result <- fifoReadResult{err: err}

			return
		}

		payload, err := io.ReadAll(fifo)
		if errc := fifo.Close(); err == nil {
			err = errc
		}

		result <- fifoReadResult{payload: payload, err: err}
	}()

	return result
}

func waitForFIFORead(t *testing.T, tokenFile string, result <-chan fifoReadResult) fifoReadResult {
	t.Helper()

	select {
	case read := <-result:
		return read
	case <-time.After(2 * time.Second):
		unblockFIFOReader(tokenFile)
		waitForFIFOReadCleanup(result)
		So("timed out waiting for Serve to write the startup token", ShouldBeBlank)

		return fifoReadResult{err: errTimedOutWaitingForServeTokenWrite}
	}
}

func unblockFIFOReader(tokenFile string) {
	fd, err := syscall.Open(tokenFile, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}

	_ = syscall.Close(fd)
}

func waitForFIFOReadCleanup(result <-chan fifoReadResult) {
	select {
	case <-result:
	case <-time.After(500 * time.Millisecond):
	}
}

func TestServeDoesNotReportPostUpgradeStartupForBrandNewDB(t *testing.T) {
	Convey("Serve does not report post-upgrade startup for a brand-new DB", t, func() {
		ctx := context.Background()
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		initialTokenWritten := prepareFIFOBackedToken(t, serverConfig.TokenFile)

		serveResult := make(chan serveStartupResult, 1)

		go func() {
			server, _, token, err := Serve(ctx, serverConfig)
			serveResult <- serveStartupResult{server: server, token: token, err: err}
		}()

		So(waitForFIFOInitialToken(initialTokenWritten), ShouldBeNil)
		So(waitForTLSWebPort(
			"localhost:"+serverConfig.WebPort,
			serverConfig.CAFile,
			serverConfig.CertDomain,
			2*time.Second,
		), ShouldBeTrue)

		_, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)

		tokenRead := readFIFOAsync(serverConfig.TokenFile)

		result := waitForServeStartup(t, serveResult)
		if result.server != nil {
			defer result.server.Stop(ctx, true)
		}

		So(result.err, ShouldBeNil)

		tokenReadResult := waitForFIFORead(t, serverConfig.TokenFile, tokenRead)
		So(tokenReadResult.err, ShouldBeNil)
		So(tokenReadResult.payload, ShouldResemble, result.token)
	})
}

func waitForTLSWebPort(address, caFile, certDomain string, timeout time.Duration) bool {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return false
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCert) {
		return false
	}

	serverName := certDomain
	if serverName == "" {
		serverName = localhost
	}

	deadline := time.Now().Add(timeout)
	dialer := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 50 * time.Millisecond},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: serverName,
		},
	}

	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(context.Background(), "tcp", address)
		if err == nil {
			_ = conn.Close()

			return true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return false
}

type fifoReadResult struct {
	payload []byte
	err     error
}

const recoveryWaitTimeout = 10 * time.Second

// waitUntilRecovered blocks until the server stops recovering or
// recoveryWaitTimeout elapses, returning whether recovery finished in time.
func waitUntilRecovered(server *Server) bool {
	deadline := time.Now().Add(recoveryWaitTimeout)
	for time.Now().Before(deadline) {
		if !server.isRecovering() {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return !server.isRecovering()
}
