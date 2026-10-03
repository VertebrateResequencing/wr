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
	// testRetryTime is how long the manager tells clients to keep trying to
	// reach it: far longer than an actor may take to end once the run has.
	testRetryTime = time.Minute
	testCallWait  = 30 * time.Second
	testEndWait   = 4 * testClientTimeout
	testPoll      = 50 * time.Millisecond
)

func TestActorEndsWithTheRunWhileTheManagerIsDown(t *testing.T) {
	Convey("Given an actor adding jobs to a manager that then stops and stays down", t, func() {
		config, d := clienttesting.PrepareWrConfig(t)
		defer d()

		// an add the stop cuts off waits for its reply for the longer of the
		// Timeout and this floor (60s); prodsim's real 2 minute Timeout is the
		// longer, so make the shortened test Timeout the longer too
		minRequest := jobqueue.ClientMinRequestTimeout
		jobqueue.ClientMinRequestTimeout = testClientTimeout

		defer func() { jobqueue.ClientMinRequestTimeout = minRequest }()

		// production keeps the DB across the stop, and RetryTime is what the
		// manager tells clients to keep trying for
		config.Deployment = "production"
		config.Timings.RetryTime = testRetryTime
		server := clienttesting.Serve(t, config)

		out := t.TempDir()
		s, err := newSim(simTestConfig(out))
		So(err, ShouldBeNil)

		defer s.close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ended := make(chan struct{})

		go func() {
			s.ibackupServer(ctx)
			close(ended)
		}()

		So(waitForCalls(out, "add_put", 1), ShouldBeTrue)

		server.Stop(context.Background(), true)

		// the next add, a simulated minute later, now waits for the manager
		time.Sleep(testSimMinute + testClientTimeout)

		Convey("ending the run ends the actor within about the client Timeout, recording the cut-short add", func() {
			endedAt := time.Now()

			cancel()

			select {
			case <-ended:
			case <-time.After(testRetryTime + testCallWait):
			}

			So(time.Since(endedAt), ShouldBeLessThan, testEndWait)

			rows := callRows(out, "add_put")
			So(len(rows), ShouldBeGreaterThanOrEqualTo, 2)
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
