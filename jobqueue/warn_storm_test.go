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

// Regression tests for prodsim round 4's warning storm: during 16 minutes of
// slow commits the manager logged 55k "reservation not yet recorded on disk"
// warnings and 175k slow-request warnings, one per reservation or request, and
// the log handler's lock became 58% of its mutex contention. Each warning must
// now be logged in full once per interval per shape, with the rest summarised
// by count and a sample, so the information stays but the lines are bounded.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/inconshreveable/log15/v3"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	reserveNotRecordedMsg = "reservation not yet recorded on disk"
	warnStormRepGroup     = "warn-storm"
	warnStormReserves     = 20
	warnStormQueries      = 50
)

func TestWarningStormIsAggregated(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager whose reservation writes are stalled and whose requests are all slow", t, func() {
		restoreThreshold, restoreInterval := slowRequestThreshold, warnAggregateInterval
		slowRequestThreshold = time.Nanosecond
		warnAggregateInterval = time.Hour

		defer func() {
			slowRequestThreshold, warnAggregateInterval = restoreThreshold, restoreInterval
		}()

		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ReserveWriteWait = 50 * time.Millisecond

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		stopped := false

		defer func() {
			if !stopped {
				server.Stop(ctx, true)
			}
		}()

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobs := make([]*Job, warnStormReserves)
		for i := range jobs {
			jobs[i] = &Job{
				Cmd: fmt.Sprintf("echo %d", i), Cwd: testCwd, RepGroup: warnStormRepGroup,
				ReqGroup: warnStormRepGroup, Requirements: standardReqs,
			}
		}

		inserts, _, err := jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, warnStormReserves)

		logs := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		holdTx, err := server.db.bolt.Begin(true)
		So(err, ShouldBeNil)

		reserved := 0

		for range warnStormReserves {
			if job, errr := jq.Reserve(time.Second); errr == nil && job != nil {
				reserved++
			}
		}

		So(holdTx.Rollback(), ShouldBeNil)
		So(reserved, ShouldEqual, warnStormReserves)

		queried := 0

		for range warnStormQueries {
			if _, errg := jq.GetIncomplete(1, JobStateBuried, false, false); errg == nil {
				queried++
			}
		}

		So(queried, ShouldEqual, warnStormQueries)

		disconnect(jq)
		server.Stop(ctx, true)

		stopped = true
		out := logs.String()

		Convey("the reservation warning is logged once, with the rest counted and a sample job key", func() {
			So(countLines(out, reserveNotRecordedMsg, "repeated"), ShouldEqual, 1)

			summary := findLine(out, reserveNotRecordedMsg, "(repeated)")
			So(summary, ShouldContainSubstring, fmt.Sprintf("repeats=%d", warnStormReserves-1))
			So(regexp.MustCompile(`sample_key=[0-9a-f]{32}`).MatchString(summary), ShouldBeTrue)
		})

		Convey("the slow-request warning is logged once per request shape, with the rest counted", func() {
			So(countLines(out, "method="+requestMethodGetIncomplete, "repeated"), ShouldEqual, 1)

			summary := findLine(out, slowRequestLogMsg+" (repeated)", "sample_method="+requestMethodGetIncomplete)
			So(summary, ShouldContainSubstring, fmt.Sprintf("repeats=%d", warnStormQueries-1))
			So(summary, ShouldContainSubstring, "maxDuration=")
			So(summary, ShouldContainSubstring, "state=buried")
		})
	})
}

func TestWarnAggregator(t *testing.T) {
	Convey("Given a warn aggregator with a long interval", t, func() {
		agg := newWarnAggregator(time.Hour)
		now := time.Now()
		agg.now = func() time.Time { return now }
		ctx, buf := captureLogCtx(context.Background())

		Convey("many concurrent occurrences log one full line, then one summary with the right count", func() {
			const (
				workers = 50
				each    = 200
			)

			var wg sync.WaitGroup

			for w := range workers {
				wg.Go(func() {
					for i := range each {
						agg.warn(ctx, "storm", "", time.Duration(w*each+i), "key", fmt.Sprintf("k%d-%d", w, i))
					}
				})
			}

			wg.Wait()

			So(countLines(buf.String(), "msg=storm", ""), ShouldEqual, 1)

			agg.stop()

			out := buf.String()
			So(countLines(out, "msg=storm", ""), ShouldEqual, 1)
			So(countLines(out, `msg="storm (repeated)"`, ""), ShouldEqual, 1)

			summary := findLine(out, "storm (repeated)")
			So(summary, ShouldContainSubstring, fmt.Sprintf("repeats=%d", workers*each-1))
			So(summary, ShouldContainSubstring, fmt.Sprintf("maxDuration=%s", time.Duration(workers*each-1)))
			So(summary, ShouldContainSubstring, "sample_key=k")

			Convey("and after stopping, every occurrence is logged in full", func() {
				agg.warn(ctx, "storm", "", 0, "key", "late1")
				agg.warn(ctx, "storm", "", 0, "key", "late2")

				So(countLines(buf.String(), "msg=storm", ""), ShouldEqual, 3)
			})
		})

		Convey("each key has its own window", func() {
			agg.warn(ctx, "storm", "a", 0, "key", "a1")
			agg.warn(ctx, "storm", "b", 0, "key", "b1")
			agg.warn(ctx, "storm", "a", 0, "key", "a2")

			So(countLines(buf.String(), "msg=storm", ""), ShouldEqual, 2)

			agg.stop()

			So(findLine(buf.String(), "storm (repeated)", "sample_key=a2"), ShouldContainSubstring, "repeats=1")
			So(countLines(buf.String(), "repeated", ""), ShouldEqual, 1)
		})

		Convey("an occurrence after the interval logs the last window's summary and a new full line", func() {
			agg.warn(ctx, "storm", "", 0, "key", "k1")
			agg.warn(ctx, "storm", "", 0, "key", "k2")
			agg.warn(ctx, "storm", "", 0, "key", "k3")

			now = now.Add(time.Hour)

			agg.warn(ctx, "storm", "", 0, "key", "k4")

			out := buf.String()
			So(countLines(out, "msg=storm", ""), ShouldEqual, 2)
			So(findLine(out, "storm (repeated)"), ShouldContainSubstring, "repeats=2")
			So(findLine(out, "storm (repeated)"), ShouldContainSubstring, "sample_key=k3")
			So(findLine(out, "msg=storm", "key=k4"), ShouldNotBeEmpty)

			agg.stop()
		})

		Convey("a nil aggregator logs every occurrence", func() {
			var none *warnAggregator

			none.warn(ctx, "storm", "", 0, "key", "k1")
			none.warn(ctx, "storm", "", 0, "key", "k2")
			none.stop()

			So(countLines(buf.String(), "msg=storm", ""), ShouldEqual, 2)
		})
	})

	Convey("A warn aggregator summarises a window once its interval ends, without another occurrence", t, func() {
		agg := newWarnAggregator(20 * time.Millisecond)
		buf := &cmdLogSyncBuffer{}
		ctx := clog.ContextWithLogHandler(context.Background(), log15.StreamHandler(buf, log15.LogfmtFormat()))

		defer agg.stop()

		agg.warn(ctx, "storm", "", 0, "key", "k1")
		agg.warn(ctx, "storm", "", 0, "key", "k2")

		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(buf.String(), "repeated") && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}

		So(findLine(buf.String(), "storm (repeated)"), ShouldContainSubstring, "repeats=1")

		agg.mu.Lock()
		remaining := len(agg.windows)
		agg.mu.Unlock()

		So(remaining, ShouldEqual, 0)
	})
}

// countLines counts the lines of out containing want but not without (unless
// without is blank).
func countLines(out, want, without string) int {
	n := 0

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, want) && (without == "" || !strings.Contains(line, without)) {
			n++
		}
	}

	return n
}

// findLine returns the first line of out containing all of wants.
func findLine(out string, wants ...string) string {
	for line := range strings.SplitSeq(out, "\n") {
		all := true

		for _, want := range wants {
			if !strings.Contains(line, want) {
				all = false

				break
			}
		}

		if all {
			return line
		}
	}

	return ""
}
