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
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

var errTimedOutWaitingForServeTokenRead = errors.New("timed out waiting for Serve to read the initial token")

var errTimedOutWaitingForServeTokenWrite = errors.New("timed out waiting for Serve to write the startup token")

const (
	recoveryWaitTimeout = 10 * time.Second

	// pscBindRetryBudget is the busy-port retry budget tests use when what they
	// test is how a budget is spent rather than the shipped length of it: two
	// retry intervals, so a retry still happens, and a fifth of the shipped 5s.
	pscBindRetryBudget = 2 * serverBindRetryInterval
)

// recoveryPauseHookWait bounds pausedRecoveringFixtureServer's wait for
// background recovery to reach its pause hook.
const recoveryPauseHookWait = 30 * time.Second

// TestServeFailsCleanlyWhenPortTaken covers the window between isolateTestConfig
// picking a manager port and the server binding it: if another process takes
// the port in that window, the serve helper must return an error for the test
// to fail on, not let publication exit the whole test binary. Serve reserves
// its ports before recovery, so it is Serve that now finds the port taken, and
// says so.
func TestServeFailsCleanlyWhenPortTaken(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("serve returns Serve's port-in-use error if the manager port is taken before Serve", t, func() {
		var squatter net.Listener

		defer func() {
			if squatter != nil {
				So(squatter.Close(), ShouldBeNil)
			}
		}()

		originalPick := pickTestPort

		defer func() { pickTestPort = originalPick }()

		pickTestPort = func() (int, error) {
			port, err := originalPick()
			if err != nil || squatter != nil {
				return port, err
			}

			squatter, err = (&net.ListenConfig{}).Listen(ctx, "tcp", "0.0.0.0:"+strconv.Itoa(port))

			return port, err
		}

		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		// a short budget keeps the test fast, and on Linux proves Serve's port
		// reservation honours the configured budget, not the shipped default.
		serverConfig.Timings.BindRetryBudget = pscBindRetryBudget

		started := time.Now()
		server, _, _, err := serve(ctx, serverConfig)
		elapsed := time.Since(started)

		if server != nil {
			defer server.Stop(ctx, true)
		}

		So(squatter, ShouldNotBeNil)

		if runtime.GOOS != "linux" {
			// only Linux reserves ports (port_reservation.go), so elsewhere
			// it is still publication that finds the port taken.
			So(errors.Is(err, errServePublishGaveUp), ShouldBeTrue)

			return
		}

		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "manager port "+serverConfig.Port+" is in use by another process")
		So(elapsed, ShouldBeLessThan, serverBindRetryBudget)
	})
}

// TestServeSkipsPortWithIPv6OnlyListener proves that a test manager outside a
// suite lane still starts when the kernel's first free-looking port has an
// IPv6-only listener on it, as rpc.statd has on some hosts. freeport only
// checks 127.0.0.1, and the manager's listener is dual-stack, so that port
// looks free but cannot be served.
func TestServeSkipsPortWithIPv6OnlyListener(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	squatter, err := (&net.ListenConfig{}).Listen(ctx, "tcp6", "[::]:0")
	if err != nil {
		t.Skipf("no IPv6 on this host: %s", err)
	}

	defer func() {
		if errc := squatter.Close(); errc != nil {
			t.Errorf("closing the IPv6-only listener: %s", errc)
		}
	}()

	squatted := squatter.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert,errcheck

	t.Setenv("WR_TEST_LANE", "")

	originalEphemeral := ephemeralTestPort

	defer func() { ephemeralTestPort = originalEphemeral }()

	handedOut := false
	ephemeralTestPort = func() (int, error) {
		if handedOut {
			return originalEphemeral()
		}

		handedOut = true

		return squatted, nil
	}

	Convey("serve starts a manager on a port free for IPv4 and IPv6 alike", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		server, _, _, errs := serve(ctx, serverConfig)
		if server != nil {
			defer server.Stop(ctx, true)
		}

		So(handedOut, ShouldBeTrue)
		So(errs, ShouldBeNil)
		So(serverConfig.Port, ShouldNotEqual, strconv.Itoa(squatted))
	})
}

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

// waitForDBUpgradeStatusState polls the startup sidecar until it reports the
// given state, matching on State rather than Detail because the recovery phase's
// Detail is an elapsed time that changes on every heartbeat tick.
func waitForDBUpgradeStatusState(dbFile, state string, timeout time.Duration) (internal.DBUpgradeStatus, bool) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		status, _, err := internal.ReadDBUpgradeStatus(dbFile)
		if err == nil && status.State == state {
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

// pausedRecoveringFixtureServer opens a server against serverConfig's DB (for
// this file, a copy of the committed fixture; the startup-window tests point it
// at their own) with its background prior-state recovery blocked at the retained
// recoveryPauseHook seam, so the startup window is observable without timing
// flakiness. It returns the server, a connect token, and an idempotent release
// func that unblocks recovery. The caller owns server.Stop and must arrange for
// release to run (recovery, publication and thus a clean shutdown are all stuck
// at the hook until then).
//
// It opens the server with serveWithoutPublication, not serve: publication is
// what the hook is holding, so waiting for it here would deadlock against a
// release that can only come after this function returns.
func pausedRecoveringFixtureServer(ctx context.Context, serverConfig ServerConfig) (*Server, []byte, func()) {
	hookEntered := make(chan struct{})
	release := make(chan struct{})

	var (
		once    sync.Once
		relOnce sync.Once
	)

	recoveryPauseHookForTest = func() {
		once.Do(func() { close(hookEntered) })
		<-release
	}
	defer func() { recoveryPauseHookForTest = nil }()

	server, _, token, err := serveWithoutPublication(ctx, serverConfig)
	recoveryPauseHookForTest = nil

	So(err, ShouldBeNil)

	releaseFn := func() { relOnce.Do(func() { close(release) }) }

	// a hang detector, not a latency budget: the hook is normally reached
	// within a millisecond of Serve returning, but only once the background
	// recovery goroutine is scheduled, which a heavily loaded host can delay by
	// seconds.
	select {
	case <-hookEntered:
	case <-time.After(recoveryPauseHookWait):
		So("timed out waiting for recovery to reach the pause hook", ShouldBeBlank)
	}

	return server, token, releaseFn
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

// waitForServing reports whether the server published its externally observable
// surface within recoveryWaitTimeout.
func waitForServing(server *Server) bool {
	select {
	case <-server.Serving():
		return true
	case <-time.After(recoveryWaitTimeout):
		return false
	}
}

// TestServeDoesNotReportPostUpgradeStartupForBrandNewDB covers E4 acceptance
// test 8. Both its old premises changed: the sidecar is now written on every
// start, not only after an upgrade, and the web port comes up only at
// publication. So it asserts instead that a brand-new DB's sidecar reports a
// non-upgrade startup phase during the window, that neither port answers before
// publication, and that the sidecar is gone and both ports answer afterwards.
//
// It drops the token FIFO for recoveryPauseHookForTest. The FIFO blocks at
// persistToken, which is publication step 2, by which time step 1 has already
// brought the web port up, so a FIFO window cannot assert the web port is still
// closed. The pause hook works even for a brand-new DB: it fires in
// recoverPriorJobsWithHeartbeat, before recoverPriorJobs reaches its
// len(priorJobs) == 0 early return. The token round-trip the FIFO also covered
// stays covered by TestServeReportsPostUpgradeStartupUntilTokenReady.
func TestServeDoesNotReportPostUpgradeStartupForBrandNewDB(t *testing.T) {
	Convey("Serve does not report post-upgrade startup for a brand-new DB", t, func() {
		ctx := context.Background()
		_, serverConfig, addr, _, connectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		status, _, err := internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(err, ShouldBeNil)
		So(status.State, ShouldNotEqual, internal.DBUpgradePostStartupState)
		So(status.State, ShouldEqual, internal.DBStartupRecoveryState)

		webAddr := "localhost:" + serverConfig.WebPort
		So(waitForTLSWebPort(webAddr, serverConfig.CAFile, serverConfig.CertDomain,
			200*time.Millisecond), ShouldBeFalse)

		jq, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, connectTime)
		So(jq, ShouldBeNil)
		So(errc, ShouldNotBeNil)

		release()
		So(waitForServing(server), ShouldBeTrue)

		_, _, err = internal.ReadDBUpgradeStatus(serverConfig.DBFile)
		So(os.IsNotExist(err), ShouldBeTrue)

		So(waitForTLSWebPort(webAddr, serverConfig.CAFile, serverConfig.CertDomain,
			recoveryWaitTimeout), ShouldBeTrue)

		jq, errc = Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, connectTime)
		So(errc, ShouldBeNil)
		So(jq.Disconnect(), ShouldBeNil)
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
