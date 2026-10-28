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
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// storeStatsTestInterval is the shrunken report interval the server tests
	// run with (spec A3 tests 2 and 3).
	storeStatsTestInterval = 50 * time.Millisecond

	// storeStatsReportWait is how long after the last add and reservation the
	// lines must have been written (spec A3 test 2).
	storeStatsReportWait = 500 * time.Millisecond

	// storeStatsIdleIntervals is how many report intervals the idle server is
	// left alone for (spec A3 test 3).
	storeStatsIdleIntervals = 3

	// the lines' messages, spelled out literally because the literal is what
	// operators and soakgate grep for.
	dbWriterLogMatch        = `msg="db writer"`
	addWaitLogMatch         = `msg="add wait"`
	reservationWaitLogMatch = `msg="reservation wait"`

	// storeStatsGroup is the rep and req group of the server tests' jobs.
	storeStatsGroup = "store_stats"
)

func TestWaitHistogram(t *testing.T) {
	Convey("Given a histogram observing 1ms, 2ms, ..., 100ms once each", t, func() {
		var h waitHistogram

		for i := 1; i <= 100; i++ {
			h.observe(time.Duration(i) * time.Millisecond)
		}

		Convey("take() summarises them and resets", func() {
			summary := h.take()
			So(summary.n, ShouldEqual, 100)
			So(summary.max, ShouldEqual, 100*time.Millisecond)
			So(summary.p50, ShouldBeBetweenOrEqual, 45*time.Millisecond, 55*time.Millisecond)
			So(summary.p99, ShouldBeBetweenOrEqual, 89*time.Millisecond, 110*time.Millisecond)

			So(h.take().n, ShouldEqual, 0)
		})
	})

	Convey("A histogram keeps sub-bucket precision across its whole range", t, func() {
		var h waitHistogram

		for _, d := range []time.Duration{0, time.Nanosecond, 15, 16, 17, time.Microsecond, time.Second,
			time.Hour, -time.Second} {
			h.observe(d)
			summary := h.take()
			So(summary.n, ShouldEqual, 1)

			want := max(d, 0)
			So(summary.max, ShouldEqual, want)
			So(summary.p50, ShouldBeGreaterThanOrEqualTo, want-want/16)
			So(summary.p50, ShouldBeLessThanOrEqualTo, want)
			So(summary.p99, ShouldBeGreaterThanOrEqualTo, want-want/16)
			So(summary.p99, ShouldBeLessThanOrEqualTo, want)
		}
	})

	Convey("A histogram's quantiles come from the observation at their rank", t, func() {
		var h waitHistogram

		h.observe(time.Millisecond)
		h.observe(100 * time.Millisecond)

		summary := h.take()
		So(summary.n, ShouldEqual, 2)
		So(summary.p50, ShouldAlmostEqual, time.Millisecond, time.Millisecond/16)
		So(summary.p99, ShouldAlmostEqual, 100*time.Millisecond, 100*time.Millisecond/16)

		for range 99 {
			h.observe(time.Millisecond)
		}

		h.observe(time.Second)

		summary = h.take()
		So(summary.n, ShouldEqual, 100)
		So(summary.p50, ShouldAlmostEqual, time.Millisecond, time.Millisecond/16)
		So(summary.p99, ShouldAlmostEqual, time.Millisecond, time.Millisecond/16)
		So(summary.max, ShouldEqual, time.Second)
	})
}

func TestWriterStats(t *testing.T) {
	Convey("Given a writer's two transactions with fixed asks and begins", t, func() {
		var w writerStats

		firstBegun := time.Now()
		w.observe(5, firstBegun.Add(-2*time.Second), firstBegun)

		secondBegun := time.Now()
		w.observe(2, secondBegun.Add(-time.Second), secondBegun)

		ended := time.Now()
		elapsed := uint64(ended.Sub(firstBegun))             //nolint:gosec // elapsed time
		elapsedSinceSecond := uint64(ended.Sub(secondBegun)) //nolint:gosec // elapsed time

		Convey("take() sums their items and locks, keeps the longest, and resets", func() {
			summary := w.take()
			So(summary.txs, ShouldEqual, 2)
			So(summary.items, ShouldEqual, 7)
			So(summary.lockNanos, ShouldEqual, uint64(3*time.Second))
			So(summary.maxLock, ShouldEqual, uint64(2*time.Second))
			So(summary.holdNanos, ShouldBeLessThanOrEqualTo, elapsed+elapsedSinceSecond)
			So(summary.maxHold, ShouldBeLessThanOrEqualTo, elapsed)
			So(summary.maxHold, ShouldBeLessThanOrEqualTo, summary.holdNanos)

			So(w.take(), ShouldResemble, writerSummary{})
		})
	})

	Convey("A transaction's hold is at least the time since it began", t, func() {
		var w writerStats

		begun := time.Now().Add(-time.Second)
		w.observe(1, begun, begun)

		summary := w.take()
		So(summary.holdNanos, ShouldBeGreaterThanOrEqualTo, uint64(time.Second))
		So(summary.maxHold, ShouldEqual, summary.holdNanos)
		So(summary.lockNanos, ShouldEqual, 0)
	})

	Convey("A transaction that never began records nothing", t, func() {
		var w writerStats

		w.observe(3, time.Now(), time.Time{})

		So(w.take(), ShouldResemble, writerSummary{})
	})
}

// TestStatsSplitByTakeAreKept covers a take() landing in the middle of an
// observation's atomic adds: the part it took is reported with the rest of the
// observation, not dropped with an interval that has nothing to log.
func TestStatsSplitByTakeAreKept(t *testing.T) {
	Convey("A writer's items taken without their transaction reach the next summary", t, func() {
		var w writerStats

		w.items.Add(5)
		w.lockNanos.Add(uint64(3 * time.Second))
		w.maxLock.Store(uint64(2 * time.Second))
		w.holdNanos.Add(uint64(4 * time.Second))
		w.maxHold.Store(uint64(time.Second))

		So(w.take().txs, ShouldEqual, 0)

		w.txs.Add(1)

		So(w.take(), ShouldResemble, writerSummary{
			txs:       1,
			items:     5,
			lockNanos: uint64(3 * time.Second),
			maxLock:   uint64(2 * time.Second),
			holdNanos: uint64(4 * time.Second),
			maxHold:   uint64(time.Second),
		})
	})

	Convey("A wait's max taken without its count reaches the next summary", t, func() {
		var h waitHistogram

		h.max.Store(uint64(time.Second))

		So(h.take().n, ShouldEqual, 0)

		h.buckets[histogramBucket(uint64(time.Second))].Add(1)

		summary := h.take()
		So(summary.n, ShouldEqual, 1)
		So(summary.max, ShouldEqual, time.Second)
	})

	Convey("A wait's count taken without its max still reports a max within its bucket", t, func() {
		var h waitHistogram

		h.buckets[histogramBucket(uint64(time.Second))].Add(1)

		summary := h.take()
		So(summary.n, ShouldEqual, 1)
		So(summary.max, ShouldBeGreaterThanOrEqualTo, time.Second-time.Second/16)
		So(summary.p50, ShouldBeLessThanOrEqualTo, summary.max)
	})

	Convey("Waits' counts taken without their max report a max within the largest one's bucket", t, func() {
		var h waitHistogram

		h.buckets[histogramBucket(uint64(time.Millisecond))].Add(1)
		h.buckets[histogramBucket(uint64(time.Second))].Add(1)

		summary := h.take()
		So(summary.n, ShouldEqual, 2)
		So(summary.max, ShouldBeGreaterThanOrEqualTo, time.Second-time.Second/16)
		So(summary.max, ShouldBeLessThanOrEqualTo, time.Second)
	})
}

func TestStoreStatsReport(t *testing.T) {
	ctx := context.Background()

	Convey("A writer's report carries its lock and hold separately", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)

		var stats dbWriterStats

		begun := time.Now()
		stats.observe(dbWriterArchive, 4, begun.Add(-2*time.Second), begun)
		stats.report(logCtx, time.Minute)

		elapsed := time.Since(begun)

		lines := logLinesContaining(readLogFile(t, logPath), dbWriterLogMatch)
		So(lines, ShouldHaveLength, 1)
		So(logLineValue(lines[0], "writer"), ShouldEqual, "archive")
		So(logLineValue(lines[0], "txs"), ShouldEqual, "1")
		So(logLineValue(lines[0], "items"), ShouldEqual, "4")
		So(storeStatsDuration(t, lines[0], "meanLock"), ShouldEqual, 2*time.Second)
		So(storeStatsDuration(t, lines[0], "maxLock"), ShouldEqual, 2*time.Second)
		So(storeStatsDuration(t, lines[0], "meanHold"), ShouldBeLessThanOrEqualTo, elapsed+statsDurationPrecision)
		So(storeStatsDuration(t, lines[0], "maxHold"), ShouldBeLessThanOrEqualTo, elapsed+statsDurationPrecision)
		So(storeStatsDuration(t, lines[0], "interval"), ShouldEqual, time.Minute)
	})

	Convey("A writer's report gives its mean lock and hold per transaction", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)

		var stats dbWriterStats

		now := time.Now()
		stats.observe(dbWriterBatch, 1, now.Add(-6*time.Second), now.Add(-4*time.Second))
		stats.observe(dbWriterBatch, 1, now.Add(-3*time.Second), now.Add(-2*time.Second))
		stats.report(logCtx, time.Minute)

		lines := logLinesContaining(readLogFile(t, logPath), dbWriterLogMatch)
		So(lines, ShouldHaveLength, 1)
		So(logLineValue(lines[0], "txs"), ShouldEqual, "2")
		So(storeStatsDuration(t, lines[0], "meanLock"), ShouldEqual, 1500*time.Millisecond)
		So(storeStatsDuration(t, lines[0], "maxLock"), ShouldEqual, 2*time.Second)

		// the holds were at least 4s and 2s, so their mean is at least 3s and,
		// being a mean, no more than their max, which is at least 4s.
		meanHold := storeStatsDuration(t, lines[0], "meanHold")
		So(meanHold, ShouldBeGreaterThanOrEqualTo, 3*time.Second)

		maxHold := storeStatsDuration(t, lines[0], "maxHold")
		So(meanHold, ShouldBeLessThanOrEqualTo, maxHold)
		So(maxHold, ShouldBeGreaterThanOrEqualTo, 4*time.Second)
	})

	Convey("A wait line's quantiles and max are each their own", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)

		var stats serverWaitStats

		for range 99 {
			stats.add.observe(time.Millisecond)
		}

		stats.add.observe(time.Second)
		stats.report(logCtx)

		lines := logLinesContaining(readLogFile(t, logPath), addWaitLogMatch)
		So(lines, ShouldHaveLength, 1)
		So(logLineValue(lines[0], "n"), ShouldEqual, "100")
		So(storeStatsDuration(t, lines[0], "p50"), ShouldAlmostEqual, time.Millisecond, time.Millisecond/16)
		So(storeStatsDuration(t, lines[0], "p99"), ShouldAlmostEqual, time.Millisecond, time.Millisecond/16)
		So(storeStatsDuration(t, lines[0], "max"), ShouldEqual, time.Second)
	})

	Convey("A reservation handed out before it was durable is counted on the next line", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)

		var stats serverWaitStats

		stats.observeReservation(time.Millisecond, true)
		stats.observeReservation(time.Millisecond, false)
		stats.report(logCtx)

		lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
		So(lines, ShouldHaveLength, 1)
		So(logLineValue(lines[0], "n"), ShouldEqual, "2")
		So(logLineValue(lines[0], "notDurable"), ShouldEqual, "1")
		So(logLineValue(lines[0], "abandoned"), ShouldEqual, "0")
	})

	Convey("A reservation handed out before it was durable is counted on one line only", t, func() {
		logPath, logCtx := managerLogContext(t, ctx)

		var stats serverWaitStats

		stats.observeReservation(time.Millisecond, true)
		stats.report(logCtx)
		stats.observeReservation(time.Millisecond, false)
		stats.report(logCtx)

		lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
		So(lines, ShouldHaveLength, 2)
		So(logLineValue(lines[0], "notDurable"), ShouldEqual, "1")
		So(logLineValue(lines[1], "n"), ShouldEqual, "1")
		So(logLineValue(lines[1], "notDurable"), ShouldEqual, "0")
	})
}

// storeStatsDuration is the duration logged for key on line.
func storeStatsDuration(t *testing.T, line, key string) time.Duration {
	t.Helper()

	d, err := time.ParseDuration(logLineValue(line, key))
	So(err, ShouldBeNil)

	return d
}

// TestStoreStatsLogLines covers spec A3 tests 2 and 3: a server's adds,
// reservations and writer transactions reach the default manager log as
// periodic summary lines, and an idle server writes none of them.
func TestStoreStatsLogLines(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey("Given a "+storeKindName(kind)+" server reporting every 50ms", t, func() {
			productionInterval := archiveFoldReportInterval
			archiveFoldReportInterval = storeStatsTestInterval

			defer func() { archiveFoldReportInterval = productionInterval }()

			_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
			serverConfig.storeKind = kind
			logPath, serverCtx := managerLogContext(t, ctx)

			server, _, token, err := serve(serverCtx, serverConfig)
			So(err, ShouldBeNil)

			defer server.Stop(ctx, true)

			So(waitUntilRecovered(server), ShouldBeTrue)

			Convey("3 adds and 5 reservations are reported within 500ms", func() {
				jq, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
				So(errc, ShouldBeNil)

				defer jq.Disconnect() //nolint:errcheck // best-effort test cleanup

				for add, jobs := range []int{1, 2, 2} {
					batch := make([]*Job, 0, jobs)
					for i := range jobs {
						batch = append(batch, &Job{
							Cmd: "echo store stats " + strconv.Itoa(add) + " " + strconv.Itoa(i), Cwd: testCwd,
							RepGroup: storeStatsGroup, ReqGroup: storeStatsGroup, Requirements: standardReqs,
						})
					}

					inserts, _, erra := jq.Add(batch, os.Environ(), true)
					So(erra, ShouldBeNil)
					So(inserts, ShouldEqual, jobs)
				}

				reserved := 0

				for range 5 {
					job, errr := jq.Reserve(5 * time.Second)
					So(errr, ShouldBeNil)

					if job != nil {
						reserved++
					}
				}

				So(reserved, ShouldEqual, 5)

				wantWriter := dbWriterNames[dbWriterNewJobs]
				if kind == storeKindLog {
					wantWriter = storeKindName(storeKindLog)
				}

				var adds, reservations, writers []string

				reported := pollUntilFor(storeStatsReportWait, func() bool {
					logged := readLogFile(t, logPath)
					adds = logLinesContaining(logged, addWaitLogMatch)
					reservations = logLinesContaining(logged, reservationWaitLogMatch)
					writers = storeStatsWriterLines(logged, wantWriter)

					return storeStatsN(t, adds) == 3 && storeStatsN(t, reservations) == 5 &&
						len(writers) > 0
				})

				for _, line := range append(append(adds, reservations...), writers...) {
					t.Logf("STORE-STATS: %s", line)
				}

				So(reported, ShouldBeTrue)
				So(storeStatsN(t, adds), ShouldEqual, 3)
				So(storeStatsN(t, reservations), ShouldEqual, 5)
				So(writers, ShouldNotBeEmpty)
			})

			Convey("an idle server writes none of the lines over 3 intervals", func() {
				time.Sleep(storeStatsIdleIntervals*storeStatsTestInterval + storeStatsTestInterval/2)

				logged := readLogFile(t, logPath)
				So(logLinesContaining(logged, dbWriterLogMatch), ShouldBeEmpty)
				So(logLinesContaining(logged, addWaitLogMatch), ShouldBeEmpty)
				So(logLinesContaining(logged, reservationWaitLogMatch), ShouldBeEmpty)
			})
		})
	})
}

// TestStoreStatsReportedAtStop covers the last interval of a stopping server:
// its adds, reservations and writer transactions are logged even though no
// report interval ended after them.
func TestStoreStatsReportedAtStop(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		Convey("Given a "+storeKindName(kind)+" server whose report interval never ends", t, func() {
			productionInterval := archiveFoldReportInterval
			archiveFoldReportInterval = time.Hour

			defer func() { archiveFoldReportInterval = productionInterval }()

			_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
			serverConfig.storeKind = kind
			logPath, serverCtx := managerLogContext(t, ctx)

			server, _, token, err := serve(serverCtx, serverConfig)
			So(err, ShouldBeNil)

			stopped := false

			defer func() {
				if !stopped {
					server.Stop(ctx, true)
				}
			}()

			So(waitUntilRecovered(server), ShouldBeTrue)

			Convey("an add and a reservation are logged when it stops", func() {
				jq, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
				So(errc, ShouldBeNil)

				inserts, _, erra := jq.Add([]*Job{{
					Cmd: "echo store stats at stop", Cwd: testCwd,
					RepGroup: storeStatsGroup, ReqGroup: storeStatsGroup, Requirements: standardReqs,
				}}, os.Environ(), true)
				So(erra, ShouldBeNil)
				So(inserts, ShouldEqual, 1)

				job, errr := jq.Reserve(5 * time.Second)
				So(errr, ShouldBeNil)
				So(job, ShouldNotBeNil)

				So(jq.Disconnect(), ShouldBeNil)

				So(logLinesContaining(readLogFile(t, logPath), addWaitLogMatch), ShouldBeEmpty)

				server.Stop(ctx, true)

				stopped = true

				wantWriter := dbWriterNames[dbWriterNewJobs]
				if kind == storeKindLog {
					wantWriter = storeKindName(storeKindLog)
				}

				logged := readLogFile(t, logPath)
				So(storeStatsN(t, logLinesContaining(logged, addWaitLogMatch)), ShouldEqual, 1)
				So(storeStatsN(t, logLinesContaining(logged, reservationWaitLogMatch)), ShouldEqual, 1)
				So(storeStatsWriterLines(logged, wantWriter), ShouldNotBeEmpty)
			})
		})
	})
}

// storeStatsN sums the n values logged on lines.
func storeStatsN(t *testing.T, lines []string) int {
	t.Helper()

	return storeStatsSum(t, lines, "n")
}

// storeStatsSum sums the integer values logged for key on lines.
func storeStatsSum(t *testing.T, lines []string, key string) int {
	t.Helper()

	sum := 0

	for _, line := range lines {
		value, err := strconv.Atoi(logLineValue(line, key))
		So(err, ShouldBeNil)

		sum += value
	}

	return sum
}

// storeStatsWriterLines returns the "db writer" lines logged for writer.
func storeStatsWriterLines(logged, writer string) []string {
	return slices.DeleteFunc(logLinesContaining(logged, dbWriterLogMatch), func(line string) bool {
		return logLineValue(line, "writer") != writer
	})
}
