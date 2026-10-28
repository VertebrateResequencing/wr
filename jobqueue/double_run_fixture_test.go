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

// Helpers for tests that restart a manager under a job whose command is still
// running, and count the command's runs to catch a double run.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	// startDurabilityRerunWait is how long a second run of the command is given
	// to appear before we conclude it did not happen.
	startDurabilityRerunWait = 5 * time.Second

	// startDurabilityPollTime is how often the marker file is re-read.
	startDurabilityPollTime = 20 * time.Millisecond
)

// errStartNeverReturned stands in for the Started() error when the call did not
// come back at all within startDurabilityAckWait.
var errStartNeverReturned = errors.New("Started() did not return")

// startDurabilityConfig gives a test server its own database, database backup and
// token file under t.TempDir(), so the crash image this test writes over that
// database cannot reach any other test's manager directory.
func startDurabilityConfig(t *testing.T) (internal.Config, ServerConfig, string, *jqs.Requirements, time.Duration) {
	t.Helper()

	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

	dir := t.TempDir()
	serverConfig.DBFile = filepath.Join(dir, "db")
	serverConfig.DBFileBackup = filepath.Join(dir, "db.bk")
	serverConfig.TokenFile = filepath.Join(dir, "token")

	return config, serverConfig, addr, standardReqs, clientConnectTime
}

// startDurabilityCmd is a command that records one line per run in marker and
// then stays alive until stopFile appears, so a second run of it overlaps the
// first.
func startDurabilityCmd(marker, stopFile string) string {
	return fmt.Sprintf("echo run >> %s; while [ ! -e %s ]; do sleep 0.05; done", marker, stopFile)
}

// waitForRuns reports whether marker records at least want runs within timeout.
func waitForRuns(marker string, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if runCount(marker) >= want {
			return true
		}

		time.Sleep(startDurabilityPollTime)
	}

	return runCount(marker) >= want
}

// runCount is how many runs of a startDurabilityCmd have recorded themselves in
// marker.
func runCount(marker string) int {
	content, err := os.ReadFile(marker)
	if err != nil {
		return 0
	}

	return len(strings.Fields(string(content)))
}
