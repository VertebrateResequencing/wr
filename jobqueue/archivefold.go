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

// This file makes the coalescing archiveWriter (db.go) say what it actually
// achieves, in an ORDINARY production manager log - no --debug, no pprof.
//
// The writer folds every archive pending at its wake into ONE bolt write
// transaction, and `wrdev.sh archive-ceiling` measures that shape delivering
// 364/s on production's own filesystem with the continuous backup streaming,
// against 21/s when mutated to one transaction per archive. Production runs the
// folding code and behaves like the one-per-archive code (~14/s, archive latency
// pinned at the 60s ClientMinRequestTimeout floor, 470 final-state reports lost
// in one minute on 2026-08-27; see .docs/reliable4/prod-validation-260827.md).
// Nothing in the log said which, because a queue that drains one archive per
// transaction looks identical from outside however the transaction came to hold
// one archive.
//
// So this reports both halves of that question on ONE periodic line:
//
//   - the FOLD (txs, archives, meanFold, maxFold): whether the writer has more
//     than one archive to fold. meanFold ~= 1 with maxFold 1 means completions
//     are arriving one at a time, ie. something UPSTREAM of db.archiveJob is
//     serialising them and no amount of extra batching has anything to divide;
//   - WHERE the waiting is (meanWait/maxWait against meanTx/maxTx): wait is how
//     long a caller's archive sat queued before the writer picked it up, tx is
//     how long the write transaction it landed in took, INCLUDING acquiring
//     bolt's single write lock (so an `add`'s own transaction starving the
//     archive writer shows up as tx, not as wait).
//
// The three regimes read as follows, and they need different work:
//
//	meanFold  meanWait  meanTx   reading
//	~1.00     << meanTx small    STARVED: completions reach db.archiveJob one at
//	                             a time, so something UPSTREAM serialises them
//	                             and more batching has nothing to divide
//	~1.00     >= meanTx any      the WRITER itself is only taking one archive per
//	                             transaction (the archive-ceiling mutation's
//	                             shape); the queue is deep and nothing folds it
//	>> 1      ~ meanTx  large    SLOW: the writer folds fine but its transaction
//	                             is expensive. meanLock then says which: close to
//	                             meanTx means it is queuing on bolt's single write
//	                             lock behind other writers (the add path takes
//	                             several bolt.Batch transactions per request),
//	                             near zero means its own commit is the cost
//
// It follows the established inert-counter convention (db.archivedDecodes
// 5c75a15, Job.derivations 8087866, db.archiveTxObserver f7e36bc,
// db.depGroupSeenGets from the dep-granularity delivery): atomics only, no lock
// on the archive hot path, and no behaviour of any kind derived from the
// figures. It differs from those in one deliberate way - they are read only by
// tests, and this one is also reported to the log, because a diagnosis that
// needs a code change to read is a diagnosis the next production restart cannot
// make.
//
// The line is at WARN because the handler the manager hands the server is
// warn-filtered unless --debug is given (cmd.setupManagerLogging), exactly as
// the startup phase lines and the recovery heartbeat are. That makes it
// mandatory that the volume stay trivial, so it is a PERIODIC SUMMARY over
// archiveFoldReportInterval, never a per-transaction line, and an interval in
// which nothing archived logs nothing at all.

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	"github.com/VertebrateResequencing/wr/internal"
)

const (
	// archiveFoldLogMsg is the log message of the periodic archive-fold summary.
	// Operators and tests grep for this literal, so it is the contract.
	archiveFoldLogMsg = "archive fold"

	// foldMeanDecimals is how many decimal places the reported mean fold carries.
	foldMeanDecimals = 2
)

// archiveFoldReportInterval is how often the archive writer summarises the fold
// it achieved. It has to be long enough that the line is trivial volume in a
// production log at warn (one line a minute, and none at all while nothing is
// archiving), and short enough to localise a saturation window that lasted
// minutes - production's 07:58-08:08 stress test walked to the client floor over
// six minutes. It is a package var (not user-configurable) purely so tests can
// shorten it, exactly as recoveryHeartbeatInterval is.
//
//nolint:gochecknoglobals // internal tuning knob; a var only so tests can vary it
var archiveFoldReportInterval = time.Minute

// archiveFoldSummary is one interval's worth of archiveFoldStats, taken and
// reset together.
type archiveFoldSummary struct {
	txs       uint64
	archives  uint64
	maxFold   uint64
	waits     uint64
	waitNanos uint64
	maxWait   uint64
	txNanos   uint64
	maxTx     uint64
	lockNanos uint64
	maxLock   uint64
}

// meanFold is how many archives the average transaction in this interval folded.
// It is the figure that says whether the coalescing writer had anything to
// coalesce: ~1.00 means completions reached it one at a time.
func (s archiveFoldSummary) meanFold() float64 {
	if s.txs == 0 {
		return 0
	}

	return float64(s.archives) / float64(s.txs)
}

// meanWait is the average time an archive spent queued before the writer picked
// it up.
func (s archiveFoldSummary) meanWait() time.Duration {
	if s.waits == 0 {
		return 0
	}

	//nolint:gosec // both operands are counts this process accumulated
	return time.Duration(s.waitNanos / s.waits)
}

// meanTx is the average duration of this interval's write transactions.
func (s archiveFoldSummary) meanTx() time.Duration {
	return s.perTx(s.txNanos)
}

// meanLock is the average part of those durations spent acquiring bolt's single
// write lock. Close to meanTx means the writer is queuing behind other writers
// (the add path takes several bolt.Batch transactions per request); near zero
// means the transaction's own commit is what costs.
func (s archiveFoldSummary) meanLock() time.Duration {
	return s.perTx(s.lockNanos)
}

// perTx divides an accumulated nanosecond total by this interval's transaction
// count.
func (s archiveFoldSummary) perTx(nanos uint64) time.Duration {
	if s.txs == 0 {
		return 0
	}

	//nolint:gosec // both operands are counts this process accumulated
	return time.Duration(nanos / s.txs)
}

// archiveFoldStats accumulates what the coalescing archive writer achieved,
// lock-free. Every field is only ever added to by the single writer goroutine
// and reset by the single reporter goroutine, so plain atomics suffice and the
// archive hot path gains no lock (DEVELOPERS.md rule 2).
//
// take() swaps the fields one at a time rather than under a lock, so a single
// observation recorded during the swap can have part of itself counted in this
// interval and part in the next. That is deliberate: the alternative is a mutex
// to make a diagnostic line self-consistent to the digit. The window is the few
// nanoseconds between one observation's own atomic adds - each mean is a ratio of
// quantities accumulated in the SAME call, so a boundary can only ever make a
// mean read low, and cannot mis-scale it by a whole batch (which dividing
// per-drain waits by per-transaction archives once did).
type archiveFoldStats struct {
	// txs counts the bolt write transactions the writer applied archives in
	// (including a rolled-back one that a per-job failure made the writer retry,
	// since that transaction held the write lock too).
	txs atomic.Uint64

	// archives counts the archives folded into those transactions.
	archives atomic.Uint64

	// maxFold is the most archives any one of those transactions held.
	maxFold atomic.Uint64

	// waits, waitNanos and maxWait are the number of archives whose queue time was
	// measured, and the summed and worst time an archive spent queued between its
	// caller enqueuing it and the writer picking it up. The count is separate from
	// archives because waits are recorded per DRAIN and archives per TRANSACTION,
	// so dividing one by the other would mis-scale meanWait whenever an interval
	// boundary fell between a drain and its transaction.
	waits     atomic.Uint64
	waitNanos atomic.Uint64
	maxWait   atomic.Uint64

	// txNanos and maxTx are the summed and worst duration of those transactions,
	// including acquiring bolt's single write lock.
	txNanos atomic.Uint64
	maxTx   atomic.Uint64

	// lockNanos and maxLock are the summed and worst part of those durations
	// spent acquiring bolt's single write lock, before the transaction body ran.
	lockNanos atomic.Uint64
	maxLock   atomic.Uint64
}

// observeWaits records how long each of the archives the writer has just taken
// ownership of waited to be picked up. Called by the writer with the batch it
// has already swapped out, so it contends with nothing.
func (s *archiveFoldStats) observeWaits(ops []*archiveOp, now time.Time) {
	var total, measured uint64

	for _, op := range ops {
		if op.queued.IsZero() {
			continue
		}

		waited := now.Sub(op.queued)
		if waited < 0 {
			waited = 0
		}

		//nolint:gosec // a non-negative duration cannot overflow the conversion
		nanos := uint64(waited.Nanoseconds())

		total += nanos
		measured++

		storeMax(&s.maxWait, nanos)
	}

	// the total before the count, so a take() landing between them can only make
	// this interval's meanWait read low, never higher than its own maxWait.
	s.waitNanos.Add(total)
	s.waits.Add(measured)
}

// observeTx records one write transaction: how many archives it folded, how long
// it took in total, and how much of that was acquiring bolt's single write lock.
func (s *archiveFoldStats) observeTx(fold int, took, lockWait time.Duration) {
	if fold <= 0 {
		return
	}

	folded := uint64(fold)

	s.txs.Add(1)
	s.archives.Add(folded)
	storeMax(&s.maxFold, folded)

	addDuration(&s.txNanos, &s.maxTx, took)
	addDuration(&s.lockNanos, &s.maxLock, lockWait)
}

// storeMax raises target to value if value is the larger. A compare-and-swap
// loop rather than a mutex, so the archive write path stays lock-free.
func storeMax(target *atomic.Uint64, value uint64) {
	for {
		current := target.Load()
		if value <= current {
			return
		}

		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

// addDuration adds d to a summing counter and raises its companion maximum,
// clamping a negative or unset duration to zero.
func addDuration(total, highest *atomic.Uint64, d time.Duration) {
	if d < 0 {
		d = 0
	}

	//nolint:gosec // a non-negative duration cannot overflow the conversion
	nanos := uint64(d.Nanoseconds())

	total.Add(nanos)
	storeMax(highest, nanos)
}

// take returns and resets this interval's figures.
func (s *archiveFoldStats) take() archiveFoldSummary {
	return archiveFoldSummary{
		txs:       s.txs.Swap(0),
		archives:  s.archives.Swap(0),
		maxFold:   s.maxFold.Swap(0),
		waits:     s.waits.Swap(0),
		waitNanos: s.waitNanos.Swap(0),
		maxWait:   s.maxWait.Swap(0),
		txNanos:   s.txNanos.Swap(0),
		maxTx:     s.maxTx.Swap(0),
		lockNanos: s.lockNanos.Swap(0),
		maxLock:   s.maxLock.Swap(0),
	}
}

// formatMeanFold formats a mean fold to 2 decimal places, so a fold of exactly
// one archive per transaction reads as an unmistakable 1.00 rather than as a
// rounded 1.
func formatMeanFold(mean float64) string {
	return strconv.FormatFloat(mean, 'f', foldMeanDecimals, 64)
}

// archiveFoldReporter is the goroutine that logs one archive-fold summary per
// archiveFoldReportInterval. An interval with no transaction logs nothing, so an
// idle manager pays a single atomic load a minute and adds no log volume.
// Started by initDB; told to exit by close() via stopArchiveFoldReporter, which
// writes the final summary itself.
func (db *db) archiveFoldReporter(ctx context.Context) {
	defer internal.LogPanic(ctx, "jobqueue archive fold reporter", false)

	ticker := time.NewTicker(archiveFoldReportInterval)
	defer ticker.Stop()

	for {
		select {
		case <-db.arFoldStop:
			return
		case <-ticker.C:
			db.reportArchiveFold(ctx)
		}
	}
}

// reportArchiveFold logs one interval's archive-fold summary, or nothing if
// nothing was archived in it.
func (db *db) reportArchiveFold(ctx context.Context) {
	summary := db.arFold.take()
	if summary.txs == 0 {
		return
	}

	clog.Warn(ctx, archiveFoldLogMsg,
		"txs", summary.txs,
		"archives", summary.archives,
		"meanFold", formatMeanFold(summary.meanFold()),
		"maxFold", summary.maxFold,
		"meanWait", summary.meanWait().Round(time.Millisecond),
		"maxWait", time.Duration(summary.maxWait).Round(time.Millisecond), //nolint:gosec // accumulated count
		"meanTx", summary.meanTx().Round(time.Millisecond),
		"maxTx", time.Duration(summary.maxTx).Round(time.Millisecond), //nolint:gosec // accumulated count
		"meanLock", summary.meanLock().Round(time.Millisecond),
		"maxLock", time.Duration(summary.maxLock).Round(time.Millisecond), //nolint:gosec // accumulated count
		"interval", archiveFoldReportInterval)
}

// stopArchiveFoldReporter writes the final summary and tells the reporter
// goroutine to exit. Called once from finaliseBackup (close()), after
// stopArchiveWriter, so the shutdown drain's transactions are reported too.
//
// It writes that summary ITSELF rather than handing the job to the reporter and
// waiting for it: a channel round-trip with another goroutine adds however long
// the scheduler takes to run it to every db.close(), and on a loaded host that is
// tens of milliseconds of extra shutdown latency for a diagnostic line. Doing it
// here also guarantees the last interval is reported before close() returns.
// take() is atomic, so a ticker firing concurrently can at worst split this
// interval over two lines; it cannot double-count.
func (db *db) stopArchiveFoldReporter(ctx context.Context) {
	db.reportArchiveFold(ctx)
	close(db.arFoldStop)
}
