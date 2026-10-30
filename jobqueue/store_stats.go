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

// This file makes the store's write paths, and the two waits clients see on
// them, say what they cost in an ordinary production manager log, so that a
// burst can be attributed to a writer without a code change (spec A3).
//
// It follows archivefold.go: atomics only, no lock on any write path, no
// behaviour derived from the figures, and periodic summaries at WARN (the level
// the manager's server handler passes without --debug) every
// archiveFoldReportInterval, never a line per write. A writer or wait with
// nothing to report in an interval logs nothing, so an idle manager adds no
// log volume.
//
//   - "db writer" (dbWriterStatsLogMsg), one line per writer that ran a
//     transaction: txs and items, meanLock/maxLock (waiting for its write turn
//     and bbolt's single write lock, before the transaction body ran) and
//     meanHold/maxHold (the transaction itself, including its commit). The
//     log store's writer has its own line, writer=log, with the batches,
//     records and bytes it made durable and meanSync/maxSync (logWriterReport).
//   - "add wait" (addWaitLogMsg): n, p50, p99 and max of how long adds waited
//     for storeNewJobsGuarded.
//   - "reservation wait" (reservationWaitLogMsg): n, p50, p99 and max of how
//     long reservations waited to be recorded on disk, how many were handed
//     out before they were (notDurable), and how many were abandoned instead
//     (abandoned; always 0 until reservations can be abandoned).

import (
	"context"
	"math/bits"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
)

const (
	// dbWriterStatsLogMsg, addWaitLogMsg and reservationWaitLogMsg are the log
	// messages of the periodic summaries. Operators, soakgate and tests grep for
	// these literals, so they are the contract.
	dbWriterStatsLogMsg   = "db writer"
	addWaitLogMsg         = "add wait"
	reservationWaitLogMsg = "reservation wait"

	// statsDurationPrecision is what the summaries round durations to. Finer than
	// archivefold.go's millisecond, because a healthy add, reservation or
	// transaction lock wait is well under one.
	statsDurationPrecision = time.Microsecond

	// histogramSubBits is log2 of the number of linear sub-buckets each power of
	// two is divided into, which bounds a quantile's error to 1/16 of its value.
	histogramSubBits = 4
	histogramSubs    = 1 << histogramSubBits

	// histogramBuckets covers every uint64 nanosecond count: values below
	// histogramSubs get a bucket each, then each power of two from there up to
	// 2^63 gets histogramSubs.
	histogramBuckets = (64 - histogramSubBits + 1) * histogramSubs

	// waitP50 and waitP99 are the quantiles the wait lines report, in percent.
	waitP50 = 50
	waitP99 = 99
	percent = 100
)

// dbWriterNames are the writer= values of the "db writer" line.
//
//nolint:gochecknoglobals // a constant table
var dbWriterNames = [dbWriterCount]string{"newJobs", "addChunk", "archive", "bestEffort", "batch"}

// dbWriter names one of the bbolt store's writers in the "db writer" line.
type dbWriter int

const (
	// dbWriterNewJobs is the coalescing new jobs writer; its items are adds.
	dbWriterNewJobs dbWriter = iota
	// dbWriterAddChunk is one chunk of an add too big for one transaction; its
	// items are the chunk's records.
	dbWriterAddChunk
	// dbWriterArchive is the coalescing archive writer; its items are archives.
	dbWriterArchive
	// dbWriterBestEffort is the coalescing best-effort writer; its items are
	// the queued writes it applied.
	dbWriterBestEffort
	// dbWriterBatch is every write outside write turns: bolt.Batch calls (wr
	// mod, wr remove, limit groups, envs), which bbolt may coalesce into fewer
	// transactions than the txs counted, and the serving-time rerun writes'
	// own transactions (updateUncoalesced). Its items are the records they
	// wrote.
	dbWriterBatch
	// dbWriterCount is the number of writers.
	dbWriterCount
)

// writerSummary is one interval's worth of a writerStats.
type writerSummary struct {
	txs, items                             uint64
	lockNanos, maxLock, holdNanos, maxHold uint64
}

// writerStats accumulates one writer's transactions, lock-free: callers only
// ever add, and the reporter takes and resets. As for archiveFoldStats, a
// transaction recorded during take() can be split over two intervals. Unlike
// archiveFoldStats (delivery-queue row 18), an interval that took only part of
// one, without its txs, gives that part back rather than dropping it with the
// line it would not be logged on.
type writerStats struct {
	txs       atomic.Uint64
	items     atomic.Uint64
	lockNanos atomic.Uint64
	maxLock   atomic.Uint64
	holdNanos atomic.Uint64
	maxHold   atomic.Uint64
}

// observe records one transaction, holding the given number of items, that was
// asked for at asked, whose body began at begun (after its write turn and
// bbolt's write lock), and which has just ended. A zero begun means the
// transaction never began, so there is nothing to record.
func (w *writerStats) observe(items int, asked, begun time.Time) {
	if begun.IsZero() {
		return
	}

	w.items.Add(uint64(max(items, 0)))
	addDuration(&w.lockNanos, &w.maxLock, begun.Sub(asked))
	addDuration(&w.holdNanos, &w.maxHold, time.Since(begun))
	w.txs.Add(1)
}

// take returns and resets this interval's figures. If they hold no
// transaction, it gives back whatever part of one it took and returns a summary
// with txs 0.
func (w *writerStats) take() writerSummary {
	summary := writerSummary{
		txs:       w.txs.Swap(0),
		items:     w.items.Swap(0),
		lockNanos: w.lockNanos.Swap(0),
		maxLock:   w.maxLock.Swap(0),
		holdNanos: w.holdNanos.Swap(0),
		maxHold:   w.maxHold.Swap(0),
	}

	if summary.txs == 0 {
		w.items.Add(summary.items)
		w.lockNanos.Add(summary.lockNanos)
		storeMax(&w.maxLock, summary.maxLock)
		w.holdNanos.Add(summary.holdNanos)
		storeMax(&w.maxHold, summary.maxHold)
	}

	return summary
}

// dbWriterStats holds every bbolt writer's writerStats.
type dbWriterStats [dbWriterCount]writerStats

// observe records one transaction by writer; see writerStats.observe.
func (s *dbWriterStats) observe(writer dbWriter, items int, asked, begun time.Time) {
	s[writer].observe(items, asked, begun)
}

// report logs one "db writer" line per writer that ran a transaction since the
// last report.
func (s *dbWriterStats) report(ctx context.Context, interval time.Duration) {
	for writer := range dbWriterCount {
		summary := s[writer].take()
		if summary.txs == 0 {
			continue
		}

		clog.Warn(ctx, dbWriterStatsLogMsg,
			"writer", dbWriterNames[writer],
			"txs", summary.txs,
			"items", summary.items,
			"meanLock", nanosDuration(summary.lockNanos/summary.txs),
			"maxLock", nanosDuration(summary.maxLock),
			"meanHold", nanosDuration(summary.holdNanos/summary.txs),
			"maxHold", nanosDuration(summary.maxHold),
			"interval", interval)
	}
}

// nanosDuration is an accumulated nanosecond count as a reported duration.
func nanosDuration(nanos uint64) time.Duration {
	return time.Duration(nanos).Round(statsDurationPrecision) //nolint:gosec // accumulated durations
}

// logWriterReport turns the log writer's totals into the per-interval "db
// writer" line with writer=log: batches, records and bytes made durable, and
// the mean and longest sync. Only the log store's reporter, and its close once
// that has stopped, call report.
type logWriterReport struct {
	last wal.WriterStats
}

// report logs the log writer's line for the interval since the last report,
// given its totals now and its longest sync in the interval, unless it made no
// batch durable.
func (r *logWriterReport) report(ctx context.Context, interval time.Duration, now wal.WriterStats,
	maxSync time.Duration,
) {
	last := r.last
	r.last = now

	batches := now.Batches - last.Batches
	if batches == 0 {
		return
	}

	syncs := max(now.Syncs-last.Syncs, 1)
	meanSync := (now.SyncTime - last.SyncTime) / time.Duration(syncs) //nolint:gosec // a count of syncs

	clog.Warn(ctx, dbWriterStatsLogMsg,
		"writer", logWriterName,
		"batches", batches,
		"records", now.Records-last.Records,
		"bytes", now.Bytes-last.Bytes,
		"meanSync", meanSync.Round(statsDurationPrecision),
		"maxSync", maxSync.Round(statsDurationPrecision),
		"interval", interval)
}

// waitSummary is what a waitHistogram observed between two takes.
type waitSummary struct {
	n             uint64
	p50, p99, max time.Duration
}

// waitHistogram is a lock-free log-linear histogram of durations: each power of
// two is split into histogramSubs linear buckets, so any quantile it reports is
// within 1/16 of the true value. observe is an atomic add and a
// compare-and-swap; take, once per report interval, walks the buckets.
type waitHistogram struct {
	buckets [histogramBuckets]atomic.Uint64
	max     atomic.Uint64
}

// observe records one wait of d; a negative d counts as 0.
func (h *waitHistogram) observe(d time.Duration) {
	nanos := uint64(max(d, 0))

	h.buckets[histogramBucket(nanos)].Add(1)
	storeMax(&h.max, nanos)
}

// histogramBucket returns the bucket holding nanos.
func histogramBucket(nanos uint64) int {
	if nanos < histogramSubs {
		return int(nanos)
	}

	shift := bits.Len64(nanos) - 1 - histogramSubBits

	return shift*histogramSubs + int(nanos>>shift) //nolint:gosec // nanos>>shift < 2*histogramSubs
}

// take returns and resets what was observed since the last take. It resets
// field by field, so an observation made during it can have its count in this
// summary and its max in the next; max is then raised to the smallest value
// the largest counted bucket holds, and a max taken with no count is given
// back.
func (h *waitHistogram) take() waitSummary {
	var counts [histogramBuckets]uint64

	n, top := h.takeBuckets(&counts)
	highest := h.max.Swap(0)

	if n == 0 {
		storeMax(&h.max, highest)

		return waitSummary{}
	}

	lowest, _ := histogramBucketRange(top)
	highest = max(highest, lowest)

	return waitSummary{
		n:   n,
		p50: histogramQuantile(&counts, n, waitP50, highest),
		p99: histogramQuantile(&counts, n, waitP99, highest),
		max: time.Duration(highest), //nolint:gosec // an observed duration
	}
}

// histogramBucketRange returns the smallest value bucket holds and its width.
func histogramBucketRange(bucket int) (lowest, width uint64) {
	if bucket < 2*histogramSubs {
		return uint64(bucket), 1 //nolint:gosec // a bucket index is never negative
	}

	shift := bucket/histogramSubs - 1
	mantissa := uint64(bucket%histogramSubs + histogramSubs)

	return mantissa << shift, 1 << shift
}

// histogramQuantile returns the pct percentile of the n observations counted
// in counts: the middle of the bucket holding it, but never more than the
// largest observation, highest.
func histogramQuantile(counts *[histogramBuckets]uint64, n, pct, highest uint64) time.Duration {
	rank := (n*pct + percent - 1) / percent

	var seen uint64

	for bucket, count := range counts {
		seen += count
		if seen < rank {
			continue
		}

		lowest, width := histogramBucketRange(bucket)

		return time.Duration(min(lowest+(width-1)/2, highest)) //nolint:gosec // an observed duration
	}

	return time.Duration(highest) //nolint:gosec // an observed duration
}

// takeBuckets swaps every bucket's count into counts, returning their total
// and the largest bucket with a count.
func (h *waitHistogram) takeBuckets(counts *[histogramBuckets]uint64) (n uint64, top int) {
	for i := range h.buckets {
		counts[i] = h.buckets[i].Swap(0)
		n += counts[i]

		if counts[i] > 0 {
			top = i
		}
	}

	return n, top
}

// serverWaitStats is what the server's clients waited for on the store: adds
// for storeNewJobsGuarded and reservations for their write. Atomics only.
type serverWaitStats struct {
	add               waitHistogram
	reservation       waitHistogram
	reserveNotDurable atomic.Uint64
	reserveAbandoned  atomic.Uint64
}

// observeReservation records one reservation that waited d for its write, and
// whether it was handed out before that write was durable.
//
// The count goes in before the wait, so a report that takes the wait also
// takes the count; at worst a count reaches a line before its wait does.
func (s *serverWaitStats) observeReservation(d time.Duration, notDurable bool) {
	if notDurable {
		s.reserveNotDurable.Add(1)
	}

	s.reservation.observe(d)
}

// report logs the "add wait" and "reservation wait" lines for whichever had
// anything to report since the last report.
func (s *serverWaitStats) report(ctx context.Context) {
	if adds := s.add.take(); adds.n > 0 {
		clog.Warn(ctx, addWaitLogMsg, waitSummaryArgs(adds)...)
	}

	reservations := s.reservation.take()
	if reservations.n == 0 {
		return
	}

	clog.Warn(ctx, reservationWaitLogMsg, append(waitSummaryArgs(reservations),
		"notDurable", s.reserveNotDurable.Swap(0), "abandoned", s.reserveAbandoned.Swap(0))...)
}

// waitSummaryArgs are the n, p50, p99 and max fields of a wait line.
func waitSummaryArgs(summary waitSummary) []any {
	return []any{
		"n", summary.n,
		"p50", summary.p50.Round(statsDurationPrecision),
		"p99", summary.p99.Round(statsDurationPrecision),
		"max", summary.max.Round(statsDurationPrecision),
	}
}

// waitStatsReporter logs the server's wait lines every interval until ctx is
// done, then once more so the last interval is not lost, and marks wgk done on
// s.bgWG. interval is passed in, not read from archiveFoldReportInterval here,
// for the reason db.arFoldInterval is (see archiveFoldReportInterval).
func (s *Server) waitStatsReporter(ctx context.Context, interval time.Duration, wgk string) {
	defer internal.LogPanic(ctx, "jobqueue wait stats reporter", false)
	defer s.bgWG.Done(wgk)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.waitStats.report(ctx)

			return
		case <-ticker.C:
			s.waitStats.report(ctx)
		}
	}
}
