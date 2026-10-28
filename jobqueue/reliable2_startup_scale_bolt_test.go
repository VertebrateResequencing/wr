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
	"fmt"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// This file holds the startup scale test, whose 250,000-job history is
// bulk-inserted as bbolt records because adding and archiving that many jobs
// through the store would take minutes, so is deleted with bbolt (spec F5).

// TestReliable2FastStartupNoHistoryScan covers the C2 acceptance test: startup
// time does not scale with completed-only history size (there is no per-RepGroup
// complete counter to seed from history - the web feed is live-delta only).
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
			"fastest of %d Serve startups took %s with %d completed-only jobs and %s with %d",
			c2StartupSamples, smallElapsed, c2SmallHistorySize, largeElapsed, c2LargeHistorySize,
		)

		// C2 acceptance test: no scaling with history size, and an absolute
		// responsiveness bound. Wait for the background recovery to finish so the
		// large-history startup is fully settled before the assertions.
		So(c2StartupScalesWithHistory(smallElapsed, largeElapsed), ShouldBeFalse)
		So(largeElapsed, ShouldBeLessThan, c2AbsoluteStartupLimit)
		So(waitUntilRecovered(largeServer), ShouldBeTrue)
	})
}

// measureCompletedHistoryStartup pre-populates config's DB with count
// completed-only jobs, then times Serve against it c2StartupSamples times,
// returning the fastest startup along with the server left running by the last
// sample (the caller stops it). dontWipeDevDB is set so every Serve opens the
// same pre-populated DB.
//
// Only the startup is repeated, not the population, so the extra samples cost
// milliseconds rather than another history build.
func measureCompletedHistoryStartup(ctx context.Context, t *testing.T, config ServerConfig,
	count int,
) (time.Duration, *Server) {
	t.Helper()

	config.dontWipeDevDB = true
	prepareCompletedHistory(ctx, t, config, count)

	var (
		fastest time.Duration
		server  *Server
	)

	for i := range c2StartupSamples {
		if server != nil {
			server.Stop(ctx, true)
		}

		started := time.Now()
		sampled, _, token, err := Serve(ctx, config)
		elapsed := time.Since(started)

		So(err, ShouldBeNil)
		// assert on a bool rather than passing the live *Server to ShouldNotBeNil:
		// the latter reflectively deep-formats the whole struct, racing the
		// background recovery goroutine that is still mutating it.
		So(sampled != nil, ShouldBeTrue)
		So(token, ShouldHaveLength, tokenLength)

		if i == 0 || elapsed < fastest {
			fastest = elapsed
		}

		server = sampled
	}

	return fastest, server
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
	So(alreadyAdded, ShouldResemble, DuplicateBreakdown{})
	So(testDB.archiveJob(seed.Key(), seed), ShouldBeNil)

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

// c2StartupScalesWithHistory reports whether the large-history startup took so
// much longer than the small one that startup must be scanning history: over
// c2HistoryScaleLimit times as long, and more than c2HistoryScaleSlack longer.
func c2StartupScalesWithHistory(smallElapsed, largeElapsed time.Duration) bool {
	return largeElapsed >= max(c2HistoryScaleLimit*smallElapsed, smallElapsed+c2HistoryScaleSlack)
}
