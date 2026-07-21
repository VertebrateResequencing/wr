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

// This file replaces the deleted seeding startup test. It pins spec.md section
// C2 (fast startup: no history scan, no seedStatusState) and section D2 (the CLI
// wr status count path stays a scan, unchanged). With seedStatusStateForItemDefs
// and startCounterBackfill removed, startup no longer seeds any status counts:
// the slim repGroupCounts counter (Item 2.1) starts empty and only fills from
// live transitions, so a large completed-only history cannot make startup scale.
// The CLI count path (getStatusByRepGroup) still scans the live queue + complete
// bucket, so it stays accurate after a restart even though the web counter is
// empty.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	// c2SmallHistorySize and c2LargeHistorySize are the completed-only history
	// sizes compared to prove startup does not scale with history. Startup no
	// longer scans this history, so a 10x size difference must not translate
	// into a proportional startup-time difference.
	c2SmallHistorySize = 25000
	c2LargeHistorySize = 250000

	// c2HistoryScaleLimit is the maximum acceptable ratio largeElapsed /
	// smallElapsed. A scanning startup would be ~10x (the size ratio); a
	// non-scanning startup is ~1x, so 4x leaves generous headroom for jitter
	// while still failing a genuinely history-scaling startup.
	c2HistoryScaleLimit = 4

	// c2AbsoluteStartupLimit is the "within a few seconds" absolute bound: even
	// the large-history startup must be responsive.
	c2AbsoluteStartupLimit = 5 * time.Second

	c2HistoryRepGroupPrefix = "reliable2-history-group-"
	c2SeedRepGroup          = "reliable2-startup-seed"

	// d2ArchivedJobCount is the number of genuinely-archived complete jobs the
	// D2 scan must count after a restart.
	d2ArchivedJobCount = 5
	d2RepGroup         = "reliable2-d2-rg"
)

// TestReliable2FastStartupNoHistoryScan covers both C2 acceptance tests: (1)
// startup time does not scale with completed-only history size, and (2) no
// per-RepGroup complete counter is seeded (the web counter's whole map carries
// no pre-seeded complete counts until a live transition occurs).
func TestReliable2FastStartupNoHistoryScan(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Serve startup does not scale with completed-only history and seeds no complete counts", t, func() {
		_, smallConfig, _, _, _ := jobqueueTestInit(true)
		smallElapsed, smallServer := measureCompletedHistoryStartup(ctx, t, smallConfig, c2SmallHistorySize)
		smallServer.Stop(ctx, true)

		_, largeConfig, _, _, _ := jobqueueTestInit(true)
		largeElapsed, largeServer := measureCompletedHistoryStartup(ctx, t, largeConfig, c2LargeHistorySize)

		defer largeServer.Stop(ctx, true)

		t.Logf(
			"Serve startup took %s with %d completed-only jobs and %s with %d",
			smallElapsed, c2SmallHistorySize, largeElapsed, c2LargeHistorySize,
		)

		// C2 acceptance test 1: no scaling with history size, and an absolute
		// responsiveness bound.
		So(largeElapsed, ShouldBeLessThan, c2HistoryScaleLimit*smallElapsed)
		So(largeElapsed, ShouldBeLessThan, c2AbsoluteStartupLimit)

		// C2 acceptance test 2: the web counter is not seeded from history. Even
		// though the large DB holds 250k+ complete jobs, the slim counter carries
		// no complete counts until a live transition occurs. Wait for the
		// background recovery to finish first so the assertion is deterministic.
		So(waitUntilRecovered(largeServer), ShouldBeTrue)
		So(completeCountsInWholeMap(largeServer), ShouldEqual, 0)
	})
}

// measureCompletedHistoryStartup pre-populates config's DB with count
// completed-only jobs, starts Serve, and returns the startup duration together
// with the running server (the caller stops it). dontWipeDevDB is set so Serve
// opens the pre-populated DB.
func measureCompletedHistoryStartup(ctx context.Context, t *testing.T, config ServerConfig,
	count int,
) (time.Duration, *Server) {
	t.Helper()

	config.dontWipeDevDB = true
	prepareCompletedHistory(ctx, t, config, count)

	started := time.Now()
	server, _, token, err := Serve(ctx, config)
	elapsed := time.Since(started)

	So(err, ShouldBeNil)
	// assert on a bool rather than passing the live *Server to ShouldNotBeNil:
	// the latter reflectively deep-formats the whole struct, racing the
	// background recovery goroutine that is still mutating it.
	So(server != nil, ShouldBeTrue)
	So(token, ShouldHaveLength, tokenLength)

	return elapsed, server
}

// prepareCompletedHistory creates a DB pre-populated with a completed-only
// history: one job archived through the normal path (so the complete bucket,
// RTK index and rep-groups bucket are genuinely populated), plus count further
// completed entries bulk-inserted directly for speed. There are no incomplete
// jobs, so recovery is trivial and any startup cost is fixed, not proportional
// to count.
func prepareCompletedHistory(ctx context.Context, t *testing.T, config ServerConfig, count int) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	seed := testDBArchivedJob("echo seed", c2SeedRepGroup, time.Now())

	jobsToQueue, jobsToUpdate, alreadyAdded, err := testDB.storeNewJobs(ctx, []*Job{seed}, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, 1)
	So(jobsToUpdate, ShouldHaveLength, 0)
	So(alreadyAdded, ShouldEqual, 0)
	So(testDB.archiveJob(ctx, seed.Key(), seed), ShouldBeNil)

	historical := testDBArchivedJob("echo historical", "historical", time.Now())

	var encoded []byte
	So(codec.NewEncoderBytes(&encoded, testDB.ch).Encode(historical), ShouldBeNil)

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		completeBucket := tx.Bucket(bucketJobsComplete)
		repGroupLookupBucket := tx.Bucket(bucketRTK)
		repGroupsBucket := tx.Bucket(bucketRGs)

		for i := range count {
			key := []byte(fmt.Sprintf("reliable2-history-key-%08d", i))
			repGroup := fmt.Sprintf("%s%08d", c2HistoryRepGroupPrefix, i)

			if errp := completeBucket.Put(key, encoded); errp != nil {
				return errp
			}

			if errp := repGroupLookupBucket.Put(testDB.generateLookupKey(repGroup, key), nil); errp != nil {
				return errp
			}

			if errp := repGroupsBucket.Put([]byte(repGroup), nil); errp != nil {
				return errp
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)
}

// TestReliable2CLIStatusCountStaysAScan covers the D2 acceptance test: after a
// restart on a DB with N archived jobs in a rep group, with the web counter
// empty (never seeded), the CLI fast-count path still returns an accurate
// complete count because getStatusByRepGroup scans the live queue + complete
// bucket rather than consuming the slim web-UI counter.
func TestReliable2CLIStatusCountStaysAScan(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)
	serverConfig.dontWipeDevDB = true

	Convey("wr status count path scans an archived rep group accurately after restart", t, func() {
		prepareArchivedJobsInRepGroup(ctx, t, serverConfig, d2RepGroup, d2ArchivedJobCount)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		// the slim web-UI counter is never seeded from history, so it carries no
		// complete counts here - proving the scan below is independent of it.
		So(completeCountsInWholeMap(server), ShouldEqual, 0)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		defer disconnect(jq)

		summaries, serr := jq.GetStatusByRepGroupMatch(d2RepGroup, RepGroupMatchExact, nil, true, false)
		So(serr, ShouldBeNil)
		So(summaries[d2RepGroup], ShouldNotBeNil)
		So(summaries[d2RepGroup].Counts[JobStateComplete], ShouldEqual, d2ArchivedJobCount)
	})
}

// prepareArchivedJobsInRepGroup creates a DB with n genuinely-archived complete
// jobs in repGroup, each with a distinct command (so each has a distinct key),
// via the normal store+archive path so they are queryable by
// GetStatusByRepGroupMatch's complete-bucket scan.
func prepareArchivedJobsInRepGroup(ctx context.Context, t *testing.T, config ServerConfig,
	repGroup string, n int,
) {
	t.Helper()

	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	jobs := make([]*Job, n)
	for i := range n {
		jobs[i] = testDBArchivedJob(fmt.Sprintf("echo d2 %d", i), repGroup, time.Now())
	}

	jobsToQueue, jobsToUpdate, alreadyAdded, err := testDB.storeNewJobs(ctx, jobs, false)
	So(err, ShouldBeNil)
	So(jobsToQueue, ShouldHaveLength, n)
	So(jobsToUpdate, ShouldHaveLength, 0)
	So(alreadyAdded, ShouldEqual, 0)

	for _, job := range jobs {
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)
	}

	So(testDB.close(ctx), ShouldBeNil)
}

// completeCountsInWholeMap sums the JobStateComplete counts across every
// RepGroup in the server's slim web-UI counter. A never-seeded counter returns
// 0 until a live complete transition occurs.
func completeCountsInWholeMap(server *Server) int {
	total := 0
	for _, counts := range server.repGroupCounts.wholeMap() {
		total += counts[JobStateComplete]
	}

	return total
}
