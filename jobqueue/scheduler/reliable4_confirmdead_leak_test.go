//go:build reliability_repro

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

package scheduler

// This is a real-LSF, on-farm reproducer for the reliable4 confirm-dead SSH
// connection LEAK (diagnosis Fix 5; see .docs/reliable4/freeze-fix-plan.md).
//
// When a job is marked lost, the manager confirms it dead by ssh'ing to the exec
// host to run `ps` (Scheduler.ProcessNotRunningOnHost). On the LSF scheduler that
// path is: getHost -> cloud.NewServer(host) [a FRESH server every call] ->
// RunCmd -> dials a new ssh.Client, runs ps, closes only the SESSION. The Host
// interface has no Close(), the throwaway server is never Destroy()ed, so the
// dialed ssh.Client (and its background goroutines + open TCP socket) is NEVER
// closed. confirmJobDead does this TWICE per lost job (command pid + runner pid),
// so a lost-job storm leaks ~2 ssh connections per job — the prod diagnosis saw
// the confirm-dead ssh connections climb 892 -> ~5,300 (~31,875 goroutines).
//
// This drives the real ProcessNotRunningOnHost against a reachable host N times
// and counts the ssh-client goroutines left alive afterwards. It needs a host the
// manager's key can ssh into (the leak only manifests on SUCCESSFUL dials — a
// failed dial errors out before the client is cached); it uses localhost by
// default (WR_CDLEAK_HOST / WR_CDLEAK_KEY / WR_CDLEAK_N to override) and SKIPS if
// passwordless ssh is unavailable or LSF is absent. RED on current code (leaked
// ssh goroutines ~= 2-4 per check); GREEN once the confirm-dead path closes its
// host connection after use (add Host.Close()).
//
// Run via developers/wrdev.sh confirm-dead-leak, or directly on a farm node:
//
//	go test -tags reliability_repro ./jobqueue/scheduler/ -run TestReliable4ConfirmDeadSSHLeak -v

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// reliable4CDLeakChecks is how many confirm-dead ssh checks to drive.
	reliable4CDLeakChecks = 40

	// reliable4CDLeakBound is the most ssh-client goroutines the confirm-dead path
	// may leave alive after the checks. Each leaked ssh.Client carries ~2-4
	// long-lived goroutines (transport read/kex loops, mux loop), so the current
	// never-close code leaves ~2-4x the check count; this bound is far below that
	// yet generous for transients, so it is RED now and GREEN once connections are
	// closed after each check.
	reliable4CDLeakBound = 20
)

// TestReliable4ConfirmDeadSSHLeak proves the confirm-dead ssh path leaks one
// unclosed ssh client per check (Host has no Close(), the throwaway cloud.Server
// is never destroyed).
func TestReliable4ConfirmDeadSSHLeak(t *testing.T) {
	host := reliable4CDLeakEnv("WR_CDLEAK_HOST", "localhost")
	key := reliable4CDLeakEnv("WR_CDLEAK_KEY", "~/.ssh/id_rsa")

	n := reliable4CDLeakChecks
	if v, err := strconv.Atoi(os.Getenv("WR_CDLEAK_N")); err == nil && v > 0 {
		n = v
	}

	if !reliable4CDLeakSSHWorks(host) {
		t.Skipf("passwordless ssh to %q unavailable; the confirm-dead leak only shows on successful dials", host)
	}

	ctx := context.Background()

	sched, err := New(ctx, "lsf", &ConfigLSF{Deployment: "development", Shell: "bash", PrivateKeyPath: key})
	if err != nil {
		t.Skipf("could not init lsf scheduler (is LSF present on this host?): %v", err)
	}

	livePid := os.Getpid() // alive => ps returns non-empty; the return value is irrelevant, the DIAL is what leaks

	base := reliable4CDLeakSSHGoroutines()

	for range n {
		_ = sched.ProcessNotRunningOnHost(ctx, livePid, host)
	}

	// let the dialled clients' goroutines settle, sampling the peak.
	peak := base

	for range 20 {
		if c := reliable4CDLeakSSHGoroutines(); c > peak {
			peak = c
		}

		time.Sleep(100 * time.Millisecond)
	}

	leaked := peak - base

	t.Logf("CONFIRMDEAD-LEAK: %d confirm-dead ssh checks to %s -> ssh-client goroutines base=%d peak=%d leaked=%d bound=%d",
		n, host, base, peak, leaked, reliable4CDLeakBound)

	if leaked > reliable4CDLeakBound {
		t.Errorf("confirm-dead SSH leak: %d checks left %d ssh-client goroutines alive (bound %d). Each "+
			"ProcessNotRunningOnHost dials a cloud.Server ssh client that is never closed (the Host interface has "+
			"no Close(), the throwaway server is never Destroy()ed). Fix: close the host connection after the "+
			"check (and group checks per host over one connection).", n, leaked, reliable4CDLeakBound)
	}
}

// reliable4CDLeakEnv returns the env var or a default.
func reliable4CDLeakEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}

	return def
}

// reliable4CDLeakSSHWorks reports whether passwordless ssh to host works and can
// run the confirm-dead ps check, so the test only runs where the leak can form.
func reliable4CDLeakSSHWorks(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=no", host, "ps -o stat= -p 1 2>/dev/null || test $? -eq 1")

	return cmd.Run() == nil
}

// reliable4CDLeakSSHGoroutines counts the currently-live goroutines whose stack is
// in the ssh client library — i.e. the per-connection transport/mux goroutines
// that a leaked (unclosed) ssh.Client keeps running.
func reliable4CDLeakSSHGoroutines() int {
	buf := make([]byte, 8<<20)
	n := runtime.Stack(buf, true)
	dump := string(buf[:n])

	count := 0

	for _, block := range strings.Split(dump, "\n\ngoroutine ") {
		if strings.Contains(block, "crypto/ssh") {
			count++
		}
	}

	return count
}
