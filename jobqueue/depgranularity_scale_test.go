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

package jobqueue

// This file holds the dep-granularity measurements that are too big for
// make test, behind the reliability_repro build tag. Two full serve recoveries
// over 10k and 50k live-job databases would materially lengthen the suite, which
// spec F4 item 4 gates at the branch-point baseline.
//
// Spec F3 (phase 6) also lands TestDepGranularityFixture here.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// dgscSmall and dgscLarge are the two live-job counts E9 acceptance test 2
	// compares. The 150k production point is measured by hand through this same
	// entry point, by raising these.
	dgscSmall = 10000
	dgscLarge = 50000

	// dgscLinearTolerance is how far from a linear relationship in live-job count
	// the decode and build costs may sit. The assertion is deliberately only "not
	// superlinear by more than 2x" rather than a wall-clock budget: this host runs
	// at a load average well above its core count, so an absolute figure would be
	// a flake while a ratio between two runs on the same host is not.
	dgscLinearTolerance = 2.0

	// dgscRecoveryWait bounds a whole 50k-job recovery. It is a hang detector,
	// not a latency budget.
	dgscRecoveryWait = 30 * time.Minute

	// dgscGroupSize is how many live jobs share a dep group, so the fixture has
	// the shape production has: many groups, each with many live members, and a
	// waiter on the previous group.
	dgscGroupSize = 100

	// dgscRecoveredPoll is how often the recovery wait re-checks the flag.
	dgscRecoveredPoll = 50 * time.Millisecond

	dgscRepGroup = "depgranularity-scale"
)

// dgscPhaseTimings holds all five phases E9 asks to be measured. The assertion
// is on decode and build, which are the two this work changes the cost of; the
// rest are recorded because the ceiling an operator cares about is their sum.
type dgscPhaseTimings struct {
	initDB  time.Duration
	decode  time.Duration
	build   time.Duration
	resolve time.Duration
	enqueue time.Duration
}

// dgscMeasureStartup builds a database holding count live jobs of one shape,
// starts a server on it, and returns the decode and dependency-group build
// durations the startup phase log lines report.
func dgscMeasureStartup(t *testing.T, count int) dgscPhaseTimings {
	t.Helper()

	ctx, logs := cmdLogSyncCapture(context.Background())
	_, serverConfig, _, _, _ := jobqueueTestInit(true)
	serverConfig.dontWipeDevDB = true

	dgscSeedLiveJobs(ctx, t, serverConfig, count)

	server, _, _, err := serveWithoutPublication(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	// publication is the tail of recovery, so this is also what bounds a whole
	// 50k-job recovery; waitUntilRecovered's own 10s bound is for the make test
	// scale, not this one.
	So(dgscWaitServing(server), ShouldBeTrue)
	So(dgscWaitRecovered(server), ShouldBeTrue)

	logged := logs.String()

	return dgscPhaseTimings{
		initDB:  dgscPhaseElapsed(logged, "recovering: opened database"),
		decode:  dgscPhaseElapsed(logged, "recovering: decoded live jobs"),
		build:   dgscPhaseElapsed(logged, "recovering: built dependency-group state"),
		resolve: dgscPhaseElapsed(logged, "recovering: resolved prior job dependencies"),
		enqueue: dgscPhaseElapsed(logged, "recovering: enqueued prior jobs"),
	}
}

// total is the whole startup window: what the manager is unreachable for.
func (t dgscPhaseTimings) total() time.Duration {
	return t.initDB + t.decode + t.build + t.resolve + t.enqueue
}

// String renders the phases for the recorded measurements.
func (t dgscPhaseTimings) String() string {
	return "initDB " + t.initDB.String() +
		", decode " + t.decode.String() +
		", build " + t.build.String() +
		", resolve " + t.resolve.String() +
		", enqueue " + t.enqueue.String() +
		", total " + t.total().String()
}

// TestDepGranularityStartupScaling covers E9 acceptance test 2: the startup
// window this work introduces is bounded by LIVE jobs, and an operator sizing it
// needs to know it grows with that count linearly rather than quadratically -
// which is the whole point of retiring the per-member dependency expansion.
//
// It measures the decode and the dependency-group state build at two live-job
// counts and records both, asserting only that neither is superlinear by more
// than dgscLinearTolerance.
func TestDepGranularityStartupScaling(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The decode and dependency-group build scale linearly in live-job count", t, func() {
		small := dgscMeasureStartup(t, dgscSmall)
		large := dgscMeasureStartup(t, dgscLarge)

		ratio := float64(dgscLarge) / float64(dgscSmall)

		t.Logf("live jobs %d: %s", dgscSmall, small)
		t.Logf("live jobs %d: %s", dgscLarge, large)

		So(small.decode, ShouldBeGreaterThan, 0)
		So(small.build, ShouldBeGreaterThan, 0)
		So(dgscScaling(small.decode, large.decode, ratio), ShouldBeLessThanOrEqualTo, dgscLinearTolerance)
		So(dgscScaling(small.build, large.build, ratio), ShouldBeLessThanOrEqualTo, dgscLinearTolerance)
	})
}

// dgscScaling is how much worse than linear the growth from small to large was,
// for a jobs ratio of jobsRatio. 1.0 is exactly linear; below 1.0 is sublinear.
func dgscScaling(small, large time.Duration, jobsRatio float64) float64 {
	if small <= 0 {
		return 0
	}

	return (float64(large) / float64(small)) / jobsRatio
}

// dgscWaitServing waits up to dgscRecoveryWait for the server to publish itself.
func dgscWaitServing(server *Server) bool {
	select {
	case <-server.Serving():
		return true
	case <-time.After(dgscRecoveryWait):
		return false
	}
}

// dgscWaitRecovered waits up to dgscRecoveryWait for the recovering flag to
// clear, which publication precedes by a sub-millisecond.
func dgscWaitRecovered(server *Server) bool {
	deadline := time.Now().Add(dgscRecoveryWait)

	for time.Now().Before(deadline) {
		if !server.isRecovering() {
			return true
		}

		<-time.After(dgscRecoveredPoll)
	}

	return !server.isRecovering()
}

// dgscSeedLiveJobs writes count live jobs, in one dep group per hundred with the
// hundred's first job waiting on the previous hundred's group, so the decode and
// the membership build both have real work of the shape production has.
func dgscSeedLiveJobs(ctx context.Context, t *testing.T, config ServerConfig, count int) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobs := make([]*Job, count)

	for i := range count {
		job := testDBJob("echo dgsc "+strconv.Itoa(i), dgscRepGroup)
		group := dgscRepGroup + "-" + strconv.Itoa(i/dgscGroupSize)
		job.DepGroups = []string{group}

		if i > 0 && i%dgscGroupSize == 0 {
			prior := dgscRepGroup + "-" + strconv.Itoa(i/dgscGroupSize-1)
			job.Dependencies = Dependencies{NewDepGroupDependency(prior)}
		}

		jobs[i] = job
	}

	jobsToQueue, _, _, err := testDB.storeNewJobs(ctx, jobs, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, count)
	So(testDB.close(ctx), ShouldBeNil)
}

// dgscPhaseElapsed pulls the elapsed duration out of the named phase's log line,
// returning -1 when the line is absent so a comparison fails rather than
// silently reading zero.
func dgscPhaseElapsed(logged, phase string) time.Duration {
	for line := range strings.SplitSeq(logged, "\n") {
		if !strings.Contains(line, phase) {
			continue
		}

		_, after, found := strings.Cut(line, "elapsed=")
		if !found {
			continue
		}

		field, _, _ := strings.Cut(after, " ")

		elapsed, err := time.ParseDuration(strings.Trim(field, `"`))
		if err != nil {
			return -1
		}

		return elapsed
	}

	return -1
}
