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

// This file pins the operator-visible half of the archive-fold instrumentation
// (archivefold.go): on a DEFAULT `wr manager start` (no --debug, no pprof) the
// manager log must say how many archives the coalescing writer folded per write
// transaction, and must separate the two explanations of a slow archive - a
// STARVED writer (callers wait, transactions are quick) from a SLOW one
// (callers do not wait, transactions are long).
//
// It exists because production ran the folding code (f7e36bc, deployed in
// fb5df01) and behaved like the code with no folding at all - ~14 completions/s,
// archive latency pinned at the 60s ClientMinRequestTimeout floor, 470
// final-state reports lost in one minute on 2026-08-27 - and nothing in the log
// distinguished the two. `wrdev.sh archive-ceiling` measures the same code at
// 364/s on production's own filesystem, so the next production restart has to be
// able to settle it from an ordinary log.
//
// The boundary asserted is therefore the manager LOG FILE, written through
// production's own handler configuration (managerLogContext, shared with
// reliable4_recovery_log_test.go): the context handler the manager hands the
// server drops everything below warn, so a summary logged at info or debug
// cannot pass this test, and neither can a captured buffer at debug level.
//
// The archives are driven through db.archiveJob against a database opened by the
// real initDB on that warn-filtered context - which is where the reporter is
// started - because what is under test is what the WRITER reports, and driving
// real runners would add scheduling noise without changing a single field on the
// line.

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// archiveFoldLogMatch is what an operator (and this test) greps the manager
	// log for. Spelled out literally rather than shared with archivefold.go,
	// because the literal is the contract.
	archiveFoldLogMatch = `msg="archive fold"`

	// archiveFoldTestInterval is the shrunken reporting interval this test runs
	// with, so a summary lands promptly instead of a minute later.
	archiveFoldTestInterval = 100 * time.Millisecond

	// archiveFoldTestArchives is how many archives are queued behind one held-open
	// transaction. It only has to be far enough above 1 that a fold of one archive
	// per transaction and a fold of many are unmistakably different numbers.
	archiveFoldTestArchives = 50

	// archiveFoldMinReportedFold is the smallest fold this arrangement must be
	// seen to report. All archiveFoldTestArchives archives are queued while a
	// transaction is deliberately held open, so they are offered to the writer
	// together; a comfortably lower bound keeps a heavily loaded host from
	// splitting them across enough drains to fail spuriously, while still being
	// far from the fold of 1 that production's numbers look like.
	archiveFoldMinReportedFold = 5

	// archiveFoldTestSettle is how long the queued archives are given to reach the
	// writer while the first transaction is held open.
	archiveFoldTestSettle = 2 * time.Second

	// archiveFoldTestSequential is how many archives are then submitted ONE AT A
	// TIME, so each gets a transaction of its own. It is what makes the
	// "periodic summary, not a line per transaction" assertion bite: a
	// per-transaction line would produce at least this many lines, whereas a
	// summary over archiveFoldTestInterval produces a couple.
	archiveFoldTestSequential = 20

	// archiveFoldIdleIntervals is how many reporting intervals the idle check
	// waits, having stopped archiving, for a line that must not appear.
	archiveFoldIdleIntervals = 5

	// archiveFoldBoundaryInterval is a reporting interval far shorter than a
	// transaction takes, so an interval boundary is certain to fall between a
	// drain recording its callers' waits and the transaction that drain becomes.
	// That is the interleaving that made a line report a meanWait of 1m41s
	// alongside a maxWait of 2s, so this is what makes that regression
	// deterministic rather than a matter of luck.
	archiveFoldBoundaryInterval = time.Millisecond
)

// TestReliable4ArchiveFoldVisibleInManagerLog covers the whole contract of the
// line: it appears in a default manager log (ie. at warn), it accounts for every
// archive, it reports a fold well above 1 when the writer really did fold,
// it carries the wait-versus-transaction split that says WHERE the time went,
// and it is a periodic summary rather than a per-transaction line.
func TestReliable4ArchiveFoldVisibleInManagerLog(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("The archive writer reports the fold it achieved in a default manager log", t, func() {
		productionInterval := archiveFoldReportInterval
		archiveFoldReportInterval = archiveFoldTestInterval

		defer func() { archiveFoldReportInterval = productionInterval }()

		logPath, serverCtx := managerLogContext(t, ctx)
		database := archiveFoldFixtureDB(t, serverCtx)

		archived := archiveFoldDriveHeldOpenBatch(t, serverCtx, database)
		So(database.close(serverCtx), ShouldBeNil)

		var lines []string

		So(pollUntil(func() bool {
			lines = logLinesContaining(readLogFile(t, logPath), archiveFoldLogMatch)

			return archiveFoldSum(t, lines, "archives") >= archived
		}), ShouldBeTrue)

		for _, line := range lines {
			t.Logf("ARCHIVE-FOLD: %s", line)
		}

		// every archive is accounted for, so the line cannot silently under-report
		// the work the writer did.
		So(archiveFoldSum(t, lines, "archives"), ShouldEqual, archived)

		// the fold is really measured: recording one archive per transaction
		// regardless of the batch, or counting drains instead of archives, cannot
		// produce this.
		So(archiveFoldMax(t, lines, "maxFold"), ShouldBeGreaterThanOrEqualTo, archiveFoldMinReportedFold)

		// ... and the summary that saw the fold says so in its mean too, which is
		// the figure production's next restart is read for.
		So(archiveFoldBiggestLineMean(t, lines), ShouldBeGreaterThan, 1.0)

		// folding happened at all: far fewer transactions than archives.
		transactions := archiveFoldSum(t, lines, "txs")
		So(transactions, ShouldBeGreaterThanOrEqualTo, archiveFoldTestSequential)
		So(transactions, ShouldBeLessThan, archived)

		// a periodic summary, not a per-transaction line: the run included
		// archiveFoldTestSequential one-at-a-time archives, each in its own
		// transaction, and they did not each produce a line.
		So(len(lines), ShouldBeLessThan, transactions)

		// the wait-versus-transaction split is populated: these archives queued
		// while a transaction was held open, so both halves are non-zero and the
		// line can distinguish a starved writer from a slow one.
		So(archiveFoldMaxDuration(t, lines, "maxWait"), ShouldBeGreaterThan, time.Duration(0))
		So(archiveFoldMaxDuration(t, lines, "maxTx"), ShouldBeGreaterThan, time.Duration(0))

		// and the write-lock half of a transaction's duration is reported too, so
		// an expensive commit can be told apart from queuing behind other writers.
		// Nothing else is writing here, so it is only asserted to be present and
		// no larger than the transaction it is part of.
		So(archiveFoldMaxDuration(t, lines, "maxLock"), ShouldBeLessThanOrEqualTo,
			archiveFoldMaxDuration(t, lines, "maxTx"))
	})
}

// TestReliable4ArchiveFoldMeansStaySelfConsistent covers the figures being means
// of something: no line may report a mean larger than its own maximum. Waits are
// accumulated once per DRAIN and transactions once per TRANSACTION, so a mean
// built from one count and the other's total is not a mean at all - it read
// 1m41s against a 2s maximum. The reporting interval is driven far below a
// transaction's duration so the boundary lands inside that window every run.
func TestReliable4ArchiveFoldMeansStaySelfConsistent(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("No archive-fold line reports a mean larger than its own maximum", t, func() {
		productionInterval := archiveFoldReportInterval
		archiveFoldReportInterval = archiveFoldBoundaryInterval

		defer func() { archiveFoldReportInterval = productionInterval }()

		logPath, serverCtx := managerLogContext(t, ctx)
		database := archiveFoldFixtureDB(t, serverCtx)

		archived := archiveFoldDriveHeldOpenBatch(t, serverCtx, database)
		So(database.close(serverCtx), ShouldBeNil)

		var lines []string

		So(pollUntil(func() bool {
			lines = logLinesContaining(readLogFile(t, logPath), archiveFoldLogMatch)

			return archiveFoldSum(t, lines, "archives") >= archived
		}), ShouldBeTrue)

		So(len(lines), ShouldBeGreaterThan, 1)
		archiveFoldAssertMeanWithinMax(t, lines)
	})
}

// TestReliable4ArchiveFoldQuietWhenIdle covers the cost of putting the line at
// warn: an interval in which nothing was archived must log nothing, and the
// production interval must be long enough that a busy manager adds one line a
// minute rather than a stream.
func TestReliable4ArchiveFoldQuietWhenIdle(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A manager that is not archiving logs no archive-fold lines", t, func() {
		productionInterval := archiveFoldReportInterval
		archiveFoldReportInterval = archiveFoldTestInterval

		defer func() { archiveFoldReportInterval = productionInterval }()

		logPath, serverCtx := managerLogContext(t, ctx)
		database := archiveFoldFixtureDB(t, serverCtx)

		// several reporting intervals with the database open and nothing archiving.
		time.Sleep(archiveFoldIdleIntervals * archiveFoldTestInterval)

		So(logLinesContaining(readLogFile(t, logPath), archiveFoldLogMatch), ShouldBeEmpty)

		// and the same after close, whose final summary has nothing to report.
		So(database.close(serverCtx), ShouldBeNil)
		So(logLinesContaining(readLogFile(t, logPath), archiveFoldLogMatch), ShouldBeEmpty)

		So(productionInterval, ShouldBeGreaterThanOrEqualTo, time.Minute)
	})
}

// archiveFoldFixtureDB opens a fresh database through the real initDB on the
// given context, which is what starts the fold reporter, and closes nothing:
// each test closes it itself, because close() is what flushes the final summary.
func archiveFoldFixtureDB(t *testing.T, ctx context.Context) *db {
	t.Helper()

	tmpdir := t.TempDir()

	database, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
	So(err, ShouldBeNil)

	// compared to nil rather than handed to ShouldNotBeNil, which formats its
	// argument with fmt and so reflects over every field of the db struct - a
	// -race failure against the atomic counters the reporter goroutine is
	// concurrently resetting.
	So(database == nil, ShouldBeFalse)

	return database
}

// archiveFoldDriveHeldOpenBatch archives one job with its transaction held open,
// queues archiveFoldTestArchives more behind it, then releases the gate, so the
// writer is offered a batch it can really fold. It then archives
// archiveFoldTestSequential jobs one at a time, each getting a transaction of its
// own. It returns how many archives completed.
func archiveFoldDriveHeldOpenBatch(t *testing.T, ctx context.Context, database *db) int {
	t.Helper()

	rec := newArchiveTxRecorder(t)
	rec.gateFirstTx()

	blocker := reliable4ACCompletableJobs(t, ctx, database, "foldlog-blocker", 1)
	queued := reliable4ACCompletableJobs(t, ctx, database, "foldlog", archiveFoldTestArchives)
	solo := reliable4ACCompletableJobs(t, ctx, database, "foldlog-solo", archiveFoldTestSequential)

	blockerDone := reliable4ACArchiveAsync(ctx, database, blocker[0].Key(), blocker[0])

	rec.awaitTx(t)

	var wg sync.WaitGroup

	errs := make([]error, len(queued))

	for i, job := range queued {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = database.archiveJob(ctx, job.Key(), job)
		}()
	}

	// let them all reach the writer's queue while the first transaction is stuck.
	time.Sleep(archiveFoldTestSettle)
	rec.releaseGate()

	wg.Wait()

	So(<-blockerDone, ShouldBeNil)

	for _, err := range errs {
		So(err, ShouldBeNil)
	}

	for _, job := range solo {
		So(database.archiveJob(ctx, job.Key(), job), ShouldBeNil)
	}

	return len(queued) + len(solo) + 1
}

// archiveFoldSum totals an integer field across the given archive-fold lines.
func archiveFoldSum(t *testing.T, lines []string, key string) int {
	t.Helper()

	total := 0

	for _, line := range lines {
		total += archiveFoldInt(t, line, key)
	}

	return total
}

// archiveFoldAssertMeanWithinMax checks each archive-fold line's reported mean
// wait against its own reported maximum. A mean above the maximum means the
// numerator and denominator came from different accumulation points and the
// figure is not a mean of anything.
func archiveFoldAssertMeanWithinMax(t *testing.T, lines []string) {
	t.Helper()

	exceeded := 0

	for _, line := range lines {
		mean, err := time.ParseDuration(logLineValue(line, "meanWait"))
		So(err, ShouldBeNil)

		maximum, err := time.ParseDuration(logLineValue(line, "maxWait"))
		So(err, ShouldBeNil)

		if mean > maximum {
			exceeded++
		}
	}

	So(exceeded, ShouldEqual, 0)
}

// archiveFoldMax is the largest value of an integer field across the given
// archive-fold lines.
func archiveFoldMax(t *testing.T, lines []string, key string) int {
	t.Helper()

	best := 0

	for _, line := range lines {
		best = max(best, archiveFoldInt(t, line, key))
	}

	return best
}

// archiveFoldBiggestLineMean returns the meanFold reported by whichever
// archive-fold line held the biggest single transaction, ie. the line that
// actually saw the fold.
func archiveFoldBiggestLineMean(t *testing.T, lines []string) float64 {
	t.Helper()

	best, mean := 0, 0.0

	for _, line := range lines {
		fold := archiveFoldInt(t, line, "maxFold")
		if fold < best {
			continue
		}

		parsed, err := strconv.ParseFloat(logLineValue(line, "meanFold"), 64)
		So(err, ShouldBeNil)

		best, mean = fold, parsed
	}

	return mean
}

// archiveFoldInt reads one integer field out of an archive-fold log line.
func archiveFoldInt(t *testing.T, line, key string) int {
	t.Helper()

	value, err := strconv.Atoi(logLineValue(line, key))
	So(err, ShouldBeNil)

	return value
}

// archiveFoldMaxDuration is the largest value of a duration field across the
// given archive-fold lines.
func archiveFoldMaxDuration(t *testing.T, lines []string, key string) time.Duration {
	t.Helper()

	var best time.Duration

	for _, line := range lines {
		value, err := time.ParseDuration(logLineValue(line, key))
		So(err, ShouldBeNil)

		best = max(best, value)
	}

	return best
}
