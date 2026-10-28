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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// addBenchDirEnv names the directory TestAddLatency1000 puts its database
	// in; the test skips when it is unset.
	addBenchDirEnv = "WR_ADDBENCH_DIR"

	// addBenchP50Env names the p50 add round trip above which
	// TestAddLatency1000 fails.
	addBenchP50Env = "WR_ADDBENCH_P50"

	// addBenchDefaultP50 is that limit when addBenchP50Env is unset.
	addBenchDefaultP50 = 200 * time.Millisecond

	// addBenchBackground is how many jobs are added before the measured adds,
	// so they land in a queue and database that are not empty.
	addBenchBackground = 100000

	// addBenchBackgroundChunk is how many of those background jobs go in one
	// add.
	addBenchBackgroundChunk = 10000

	// addBenchAdds and addBenchAddSize are the measured adds: this many
	// sequential adds of this many new jobs each.
	addBenchAdds    = 100
	addBenchAddSize = 1000
)

// errAddBenchShort is returned when an add stores fewer new jobs than it was
// given.
var errAddBenchShort = errors.New("add stored too few new jobs")

// TestAddLatency1000FailsOverItsLimit covers spec A3 test 4: run as a child
// process with an impossible p50 limit, TestAddLatency1000 fails and reports
// the figures it measured.
func TestAddLatency1000FailsOverItsLimit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("TestAddLatency1000 with a 1ns p50 limit fails and reports n and p50", t, func() {
		cmd := exec.CommandContext(context.Background(), os.Args[0], //nolint:gosec // this test binary
			"-test.run", "^TestAddLatency1000$", "-test.v", "-test.count", "1")

		cmd.Env = append(os.Environ(), addBenchDirEnv+"="+t.TempDir(), addBenchP50Env+"=1ns")

		output, err := cmd.CombinedOutput()
		t.Logf("child output:\n%s", output)

		So(err, ShouldNotBeNil)
		So(regexp.MustCompile(`\bn=\d+\b`).FindAllString(string(output), -1), ShouldContain, "n=100")
		So(regexp.MustCompile(`\bp50=\S+`).FindString(string(output)), ShouldNotBeEmpty)
		So(string(output), ShouldContainSubstring, "--- FAIL: TestAddLatency1000")
	})
}

// TestAddLatency1000 is the add gate's fixed-size measurement (spec A3): the
// "add wait" log line mixes add sizes, so this times adds of exactly 1,000 jobs
// against a store already holding 100,000. It runs only when WR_ADDBENCH_DIR
// names where to put the database.
func TestAddLatency1000(t *testing.T) {
	if runnermode || servermode {
		return
	}

	dir := os.Getenv(addBenchDirEnv)
	if dir == "" {
		t.Skip(addBenchDirEnv + " is not set")
	}

	forEachStore(t, func(kind storeKind) {
		Convey("Adds of 1,000 jobs to a "+storeKindName(kind)+" store holding 100,000 are fast enough", t, func() {
			limit := addBenchDefaultP50

			if value := os.Getenv(addBenchP50Env); value != "" {
				parsed, err := time.ParseDuration(value)
				So(err, ShouldBeNil)

				limit = parsed
			}

			summary := addBenchMeasure(t, filepath.Join(dir, storeKindName(kind)), kind)

			t.Logf("store=%s n=%d p50=%s p99=%s", storeKindName(kind), summary.n, summary.p50, summary.p99)

			So(summary.n, ShouldEqual, addBenchAdds)
			So(summary.p50, ShouldBeLessThanOrEqualTo, limit)
		})
	})
}

// addBenchMeasure starts a server whose database is under dir, adds the
// background jobs, then returns the round trips of the measured adds.
func addBenchMeasure(t *testing.T, dir string, kind storeKind) waitSummary {
	t.Helper()

	ctx := context.Background()

	So(os.MkdirAll(dir, 0o700), ShouldBeNil)

	_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)
	serverConfig.DBFile = filepath.Join(dir, "db")
	serverConfig.DBFileBackup = filepath.Join(dir, "db.bk")
	serverConfig.storeKind = kind

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	defer jq.Disconnect() //nolint:errcheck // best-effort test cleanup

	env := os.Environ()

	for start := 0; start < addBenchBackground && err == nil; start += addBenchBackgroundChunk {
		err = addBenchAdd(jq, env, "background", start, addBenchBackgroundChunk, standardReqs)
	}

	So(err, ShouldBeNil)

	var roundTrips waitHistogram

	for i := 0; i < addBenchAdds && err == nil; i++ {
		began := time.Now()

		err = addBenchAdd(jq, env, "measured", i*addBenchAddSize, addBenchAddSize, standardReqs)

		roundTrips.observe(time.Since(began))
	}

	So(err, ShouldBeNil)

	return roundTrips.take()
}

// addBenchAdd adds n new jobs numbered from first, as `wr add` does.
func addBenchAdd(jq *Client, envVars []string, kind string, first, n int, reqs *jqs.Requirements) error {
	jobs := make([]*Job, 0, n)
	for i := first; i < first+n; i++ {
		jobs = append(jobs, &Job{
			Cmd: "echo addbench " + kind + " " + strconv.Itoa(i), Cwd: testCwd,
			RepGroup: "addbench", ReqGroup: "addbench", Requirements: reqs,
		})
	}

	added, _, err := jq.Add(jobs, envVars, true)
	if err != nil {
		return err
	}

	if added != n {
		return fmt.Errorf("%w: %d of %d %s jobs", errAddBenchShort, added, n, kind)
	}

	return nil
}
