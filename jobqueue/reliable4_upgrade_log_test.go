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
	"testing"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// The upgrade log lines this file matches, and logLinesForPhase, are in
// store_convert_test.go, whose conversion tests match them too and which F5
// keeps.

const (
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
// TestDBUpgradeProgressLogRateLimit, which the zero interval here
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

	testDB, _, err := initDB(ctx, dbFile, filepath.Join(dir, "queue.db.bak"), internal.Development, false)
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

	testDB, _, err := initDB(serverCtx, dbFile, dbFile+".bak", internal.Development, false)
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)

	return readLogFile(t, logPath)
}
