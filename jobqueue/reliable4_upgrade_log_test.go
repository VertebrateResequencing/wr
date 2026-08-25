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

// This file pins the operator-visible half of a database upgrade, the sibling of
// reliable4_recovery_log_test.go: on a DEFAULT `wr manager start` (no --debug)
// the manager log must say that an index rebuild started and that it finished,
// because an upgrade runs before the manager serves anything, so a silent
// rebuild that takes minutes on a large DB is indistinguishable from a hang (see
// .docs/bugfixes/260825-3.md item 2).
//
// The boundary asserted is therefore the manager LOG FILE, produced under
// production's own handler configuration (managerLogContext, shared with
// reliable4_recovery_log_test.go): a line the server logs below warn on that
// context never reaches the file, so asserting on a buffer captured at info or
// debug - which is what jobqueue/db_test.go's progress test does - would pass
// with the bug present. That is the trap that let this bug live.
//
// The expected message substrings are spelled out literally here rather than
// shared with db.go, because those literals are the contract: they are what an
// operator (and .docs/bugfixes/260825-3-red-upgrade-visibility.sh) greps for.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// these are the upgrade lines an operator must be able to find in the manager
	// log at the default log level.
	dbUpgradeStartLogMatch     = `msg="database upgrade started"`
	dbUpgradeStepStartLogMatch = `msg="database upgrade step started"`
	dbUpgradeStepDoneLogMatch  = `msg="database upgrade step complete"`
	dbUpgradeDoneLogMatch      = `msg="database upgrade complete"`

	// dbUpgradeProgressLogMatch is the per-progress-point line, which must NOT
	// reach that log for every point: a real rebuild processes millions of
	// entries.
	dbUpgradeProgressLogMatch = `msg="database upgrade progress"`

	// dbUpgradeStillRunningLogMatch is the line that does reach it while a phase
	// is running, at most once per dbUpgradeLogInterval. It carries its own
	// message so that a default-level log cannot be mistaken for one line per
	// progress point.
	dbUpgradeStillRunningLogMatch = `msg="database upgrade step still running"`

	// depGroupRebuildPhase is the state the dep-group index rebuild reports.
	depGroupRebuildPhase = "rebuild dep-group index"

	// dbUpgradeLogFixtureEntries is how many dep-group lookup entries the fixture
	// database holds. It is a multiple of dbUpgradeProgressEntries so the rebuild
	// is guaranteed to reach several progress points, which is what makes
	// "progress lines do not flood the default log" a real measurement rather
	// than a vacuous one.
	dbUpgradeLogFixtureEntries = 5 * dbUpgradeProgressEntries

	// dbUpgradeLogFixtureProgressPoints is how many progress points the fixture's
	// rebuild reaches at minimum (it reaches more if a point also falls due on
	// dbUpgradeProgressInterval).
	dbUpgradeLogFixtureProgressPoints = dbUpgradeLogFixtureEntries / dbUpgradeProgressEntries

	// dbUpgradeRateLimitPoints is how many progress points
	// TestReliable4DBUpgradeProgressLogRateLimit drives through the reporter, and
	// dbUpgradeRateLimitPointsPerInterval is how many of them it packs into each
	// dbUpgradeLogInterval. At dbUpgradeProgressEntries entries per point that is
	// a million-entry rebuild, the scale at which the difference between "once per
	// interval" and "once per point" is the difference between a readable log and
	// a flooded one.
	dbUpgradeRateLimitPoints            = 100
	dbUpgradeRateLimitPointsPerInterval = 4
)

// TestReliable4DBUpgradeVisibleInManagerLog covers the phase milestones an
// operator needs to tell a working upgrade from a hung manager, and the volume
// boundary that keeps them usable: the phases are visible at the default level,
// the per-progress-point lines are not.
func TestReliable4DBUpgradeVisibleInManagerLog(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A default manager start logs a database index rebuild starting and finishing", t, func() {
		logged := openDBLoggingAsManager(t, ctx, dbUpgradeLogFixture(t, ctx))

		So(logLinesContaining(logged, dbUpgradeStartLogMatch), ShouldHaveLength, 1)
		So(logLinesForPhase(logged, dbUpgradeStepStartLogMatch, depGroupRebuildPhase), ShouldHaveLength, 1)

		done := logLinesForPhase(logged, dbUpgradeStepDoneLogMatch, depGroupRebuildPhase)
		So(done, ShouldHaveLength, 1)
		So(logLineValue(done[0], "processed"), ShouldEqual, strconv.Itoa(dbUpgradeLogFixtureEntries))

		So(logLinesContaining(logged, dbUpgradeDoneLogMatch), ShouldHaveLength, 1)
	})

	Convey("That rebuild does not add a default-level log line per entry it processes", t, func() {
		logged := openDBLoggingAsManager(t, ctx, dbUpgradeLogFixture(t, ctx))

		// the completion line proves the rebuild really walked every fixture entry,
		// and progress falls due every dbUpgradeProgressEntries of them, so at least
		// dbUpgradeLogFixtureProgressPoints progress points happened here.
		done := logLinesForPhase(logged, dbUpgradeStepDoneLogMatch, depGroupRebuildPhase)
		So(done, ShouldHaveLength, 1)
		So(logLineValue(done[0], "processed"), ShouldEqual, strconv.Itoa(dbUpgradeLogFixtureEntries))
		So(dbUpgradeLogFixtureProgressPoints, ShouldBeGreaterThan, 1)

		// none of them reaches the log: the phase finishes well inside one
		// dbUpgradeLogInterval, which is counted from the phase's start, so its own
		// start and completion lines are the whole story. This is also what pins
		// dbUpgradeLogIntervalDefault as coarse enough to be worth having: at a
		// production-shaped interval, a rebuild this size promotes nothing.
		So(logLinesContaining(logged, dbUpgradeProgressLogMatch), ShouldBeEmpty)
		So(logLinesContaining(logged, dbUpgradeStillRunningLogMatch), ShouldBeEmpty)
	})
}

// TestReliable4LongDBUpgradeReportsProgress covers the part that matters on a
// production-sized DB: a phase that outlasts dbUpgradeLogInterval keeps saying
// how far it has got, at the default log level, so a rebuild of millions of
// entries visibly moves instead of reading as a hang. The interval is shrunk
// rather than the fixture grown, so the test stays fast and deterministic.
//
// It pins that the promoted line reaches the real manager log file, with the
// phase and the entry count on it; how often it is promoted is pinned by
// TestReliable4DBUpgradeProgressLogRateLimit, which the zero interval here
// deliberately takes out of play.
func TestReliable4LongDBUpgradeReportsProgress(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A rebuild phase that outlasts the log interval reports its progress", t, func() {
		previousInterval := dbUpgradeLogInterval
		dbUpgradeLogInterval = 0

		defer func() { dbUpgradeLogInterval = previousInterval }()

		logged := openDBLoggingAsManager(t, ctx, dbUpgradeLogFixture(t, ctx))

		progress := logLinesContaining(logged, dbUpgradeStillRunningLogMatch)
		So(len(progress), ShouldBeGreaterThanOrEqualTo, dbUpgradeLogFixtureProgressPoints)
		So(progress[0], ShouldContainSubstring, `state="`+depGroupRebuildPhase+`"`)
		So(logLineValue(progress[len(progress)-1], "processed"),
			ShouldEqual, strconv.Itoa(dbUpgradeLogFixtureEntries))
	})
}

// TestReliable4NoDBUpgradeStaysQuiet covers the other side of the volume
// boundary: an ordinary restart, with no upgrade to do, must say nothing about
// upgrades at all.
func TestReliable4NoDBUpgradeStaysQuiet(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Reopening an up-to-date database logs nothing about database upgrades", t, func() {
		dbFile := dbUpgradeLogFixture(t, ctx)
		So(openDBLoggingAsManager(t, ctx, dbFile), ShouldContainSubstring, dbUpgradeStartLogMatch)

		So(openDBLoggingAsManager(t, ctx, dbFile), ShouldNotContainSubstring, "database upgrade")
	})
}

// dbUpgradeLogFixture returns the path of a new database that holds
// dbUpgradeLogFixtureEntries dep-group lookup entries but has no dep-group
// index, so the next initDB rebuilds that index (initDB's openedExistingDB &&
// !hadDepGroups upgrade). That is the same trigger
// .docs/bugfixes/260825-3-red-upgrade-visibility.sh applies to a real manager's
// database, done here without a manager.
func dbUpgradeLogFixture(t *testing.T, ctx context.Context) string {
	t.Helper()

	dir := t.TempDir()
	dbFile := filepath.Join(dir, "queue.db")

	testDB, _, err := initDB(ctx, dbFile, filepath.Join(dir, "queue.db.bak"), internal.Development, false, false)
	So(err, ShouldBeNil)

	keys := make([][]byte, dbUpgradeLogFixtureEntries)
	for i := range dbUpgradeLogFixtureEntries {
		keys[i] = fmt.Appendf(nil, "dg-%06d%s%032d", i, dbDelimiter, i)
	}

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		return replaceLookupRebuildTestBucket(tx, bucketDTK, keys...)
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)

	boltdb, err := bolt.Open(dbFile, dbFilePermission, nil)
	So(err, ShouldBeNil)
	So(boltdb.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket(bucketDepGroups)
	}), ShouldBeNil)
	So(boltdb.Close(), ShouldBeNil)

	return dbFile
}

// openDBLoggingAsManager opens the database at dbFile with logging configured
// exactly as a default `wr manager start` configures it (managerLogContext), and
// returns the manager log's content afterwards. Any upgrade happens inside the
// open, so everything the upgrade logged is in the returned content.
func openDBLoggingAsManager(t *testing.T, ctx context.Context, dbFile string) string {
	t.Helper()

	logPath, serverCtx := managerLogContext(t, ctx)

	testDB, _, err := initDB(serverCtx, dbFile, dbFile+".bak", internal.Development, false, false)
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)

	return readLogFile(t, logPath)
}

// logLinesForPhase returns the log lines containing both the given message
// substring and the given upgrade phase state.
func logLinesForPhase(logged, msgMatch, state string) []string {
	var lines []string

	for _, line := range logLinesContaining(logged, msgMatch) {
		if strings.Contains(line, `state="`+state+`"`) {
			lines = append(lines, line)
		}
	}

	return lines
}

// TestReliable4DBUpgradeProgressLogRateLimit pins the rate limit itself: the
// default-level output of a long phase is proportional to how long it runs, not
// to how many entries it processes. Without this, a single elapsed interval
// latches promotion on and every remaining progress point reaches the default
// log - a million-entry rebuild going from 7 lines to 87 - which is the flood
// this whole guard exists to prevent, and which every log-file test above still
// passes through.
//
// It drives the reporter directly, with its clock injected, because the cadence
// cannot be measured through the log file without either a rebuild that really
// takes minutes or a wall-clock race on a loaded host. The log file remains the
// boundary for what gets logged; this covers only how often.
func TestReliable4DBUpgradeProgressLogRateLimit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A long phase promotes one progress line per log interval, not one per progress point", t, func() {
		clock := time.Now()
		reporter := newDBUpgradeReporter(ctx, filepath.Join(t.TempDir(), "queue.db"))
		reporter.now = func() time.Time { return clock }

		var everyPoint, promoted []string

		reporter.info = recordLoggedArgs(&everyPoint, "database upgrade progress")
		reporter.warn = recordLoggedArgs(&promoted, "database upgrade step still running")

		reporter.startPhase(depGroupRebuildPhase, "rebuilding database dependency-group index")

		for point := range dbUpgradeRateLimitPoints {
			clock = clock.Add(dbUpgradeLogInterval / dbUpgradeRateLimitPointsPerInterval)
			processed := (point + 1) * dbUpgradeProgressEntries

			reporter.writeProgress(depGroupRebuildPhase,
				fmt.Sprintf("rebuilding database dependency-group index (%d entries processed)", processed), processed)
		}

		// every point is still recorded for --debug, and exactly one point per
		// interval is promoted to the default log.
		So(len(everyPoint), ShouldEqual, dbUpgradeRateLimitPoints)
		So(len(promoted), ShouldEqual, dbUpgradeRateLimitPoints/dbUpgradeRateLimitPointsPerInterval)

		// and the promoted line is a progress report, not a bare "still alive": it
		// carries how far the phase has got.
		So(logLineValue(promoted[len(promoted)-1], "processed"),
			ShouldEqual, strconv.Itoa(dbUpgradeRateLimitPoints*dbUpgradeProgressEntries))
	})
}

// recordLoggedArgs returns a dbUpgradeReporter log function that appends the
// arguments of each call made with the given message to lines, rendered in the
// same key=value form the manager log uses, so logLineValue reads them.
func recordLoggedArgs(lines *[]string, msg string) func(string, ...any) {
	return func(logged string, args ...any) {
		if logged != msg {
			return
		}

		var line strings.Builder

		for i := 0; i+1 < len(args); i += 2 {
			fmt.Fprintf(&line, " %v=%v", args[i], args[i+1])
		}

		*lines = append(*lines, line.String())
	}
}
