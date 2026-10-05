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

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	clienttesting "github.com/VertebrateResequencing/wr/client/testing"
	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	testClientTimeout = time.Second
	testSimMinute     = 100 * time.Millisecond
	// testWrstatSimMinute makes wrstat's 8 hour gap between runs under a
	// second.
	testWrstatSimMinute = time.Millisecond
	// testRetryTime is how long the manager tells clients to keep trying to
	// reach it: far longer than an actor may take to end once the run has.
	testRetryTime  = time.Minute
	testCallWait   = 30 * time.Second
	testEndWait    = 4 * testClientTimeout
	testPoll       = 50 * time.Millisecond
	maxJitterTimes = 1.5
)

// outageRun is a prodsim run of one actor against a test manager the test can
// stop.
type outageRun struct {
	s      *sim
	out    string
	server *jobqueue.Server
	cancel context.CancelFunc
	ended  chan struct{}
	undo   []func()
}

// startOutageRun starts actor at the given simulated minute and scale against
// a new test manager that keeps its DB across a stop, as production does.
func startOutageRun(t *testing.T, simMinute time.Duration, scale float64,
	actor func(*sim, context.Context),
) *outageRun {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	r := &outageRun{out: t.TempDir(), cancel: cancel, ended: make(chan struct{})}

	config, d := clienttesting.PrepareWrConfig(t)
	r.undo = append(r.undo, d)

	// a request the stop cuts off waits for its reply for the longer of the
	// Timeout and this floor (60s); prodsim's real 2 minute Timeout is the
	// longer, so make the shortened test Timeout the longer too
	minRequest := jobqueue.ClientMinRequestTimeout
	jobqueue.ClientMinRequestTimeout = testClientTimeout

	r.undo = append(r.undo, func() { jobqueue.ClientMinRequestTimeout = minRequest })

	config.Deployment = "production"
	config.Timings.RetryTime = testRetryTime
	r.server = clienttesting.Serve(t, config)
	r.undo = append(r.undo, func() { r.server.Stop(context.Background(), true) })

	cfg := simTestConfig(r.out)
	cfg.simMinute = simMinute
	cfg.scale = scale

	var err error

	r.s, err = newSim(cfg)
	So(err, ShouldBeNil)

	r.undo = append(r.undo, r.s.close)

	go func() {
		actor(r.s, ctx)
		close(r.ended)
	}()

	return r
}

// end ends the run and returns how long the actor took to end.
func (r *outageRun) end() time.Duration {
	endedAt := time.Now()

	r.cancel()

	select {
	case <-r.ended:
	case <-time.After(testRetryTime + testCallWait):
	}

	return time.Since(endedAt)
}

// cleanup ends the run and waits for its actor, if the test did not, then
// undoes startOutageRun's setup, stopping the manager.
func (r *outageRun) cleanup() {
	r.end()

	for _, undo := range slices.Backward(r.undo) {
		undo()
	}
}

func TestActorEndsWithTheRunWhileTheManagerIsDown(t *testing.T) {
	Convey("Given an actor adding jobs to a manager that then stops and stays down", t, func() {
		r := startOutageRun(t, testSimMinute, 0.1, (*sim).ibackupServer)
		defer r.cleanup()

		So(waitForCalls(r.out, "add_put", 1), ShouldBeTrue)

		r.server.Stop(context.Background(), true)

		// the next add, a simulated minute later, now waits for the manager
		time.Sleep(testSimMinute + testClientTimeout)

		Convey("ending the run ends the actor within about the client Timeout, recording the cut-short add", func() {
			So(r.end(), ShouldBeLessThan, testEndWait)

			rows := callRows(r.out, "add_put")
			So(len(rows), ShouldBeGreaterThanOrEqualTo, 2)
			So(rows[len(rows)-1][5], ShouldContainSubstring, context.Canceled.Error())
		})
	})

	Convey("Given wrstat, whose run is many calls, and a manager that stops and stays down between runs", t, func() {
		r := startOutageRun(t, testWrstatSimMinute, 1, (*sim).wrstatMulti)
		defer r.cleanup()

		So(waitForCalls(r.out, "add_tidy", 1), ShouldBeTrue)

		r.server.Stop(context.Background(), true)

		// by then the next run has started, and its first find waits for the
		// manager
		time.Sleep(time.Duration(float64(r.s.sim(wrstatEvery))*maxJitterTimes) + testClientTimeout)

		Convey("ending the run ends wrstat within about the client Timeout, without trying its later calls", func() {
			So(r.end(), ShouldBeLessThan, testEndWait)

			rows := callRows(r.out, "find_dependent_prefix")
			So(len(rows), ShouldEqual, 2*wrstatPaths)
			So(rows[len(rows)-1][5], ShouldContainSubstring, context.Canceled.Error())
		})
	})
}

// simTestConfig is a run against the development deployment that
// clienttesting.PrepareWrConfig configured, writing its TSVs to out. Its jobs
// are never run: the test manager has no runner command.
func simTestConfig(out string) config {
	return config{
		deployment:    "development",
		jobScript:     "true",
		workDir:       out,
		outDir:        out,
		queue:         "normal",
		simMinute:     testSimMinute,
		scale:         0.1,
		seed:          1,
		clientTimeout: testClientTimeout,
	}
}

// waitForCalls waits for calls.tsv in out to have n rows for op.
func waitForCalls(out, op string, n int) bool {
	for deadline := time.Now().Add(testCallWait); time.Now().Before(deadline); time.Sleep(testPoll) {
		if len(callRows(out, op)) >= n {
			return true
		}
	}

	return false
}

// callRows returns calls.tsv's rows for op in out.
func callRows(out, op string) [][]string {
	b, err := os.ReadFile(filepath.Join(out, "calls.tsv"))
	if err != nil {
		return nil
	}

	var rows [][]string

	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Split(line, "\t"); len(f) == callCols && f[2] == op {
			rows = append(rows, f)
		}
	}

	return rows
}
