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

// This file pins spec.md section F1: the reworked build must open a DB already
// upgraded by current (pre-removal reliable2) code without error or data loss.
// It uses the committed binary fixture jobqueue/testdata/dbcompat/db.golden,
// produced once by jobqueue/testdata/dbcompat/gen.go run from a pre-removal
// commit that still maintains the now-dead per-RepGroup complete-counter buckets
// (repGroupCompleteCount / repGroupCompleteBackfilled). The fixture holds ~4
// jobs across two rep groups - two complete (archived) and two incomplete in
// jobslive, the incomplete ones carrying non-empty WaitingForDepGroups and
// LimitGroupsForDisplay - plus populated index buckets, so opening it exercises
// the decode of the two post-v0.36.5 Job fields and the one-time index-rebuild
// guards. The fixture is unversioned, so each test compacts its copy first, as
// an operator must before a manager will open it.
//
// Item 4.3 (spec.md section H2, the retained recovery window) adds further tests
// to this file that reuse the same fixture helpers below.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// dbcompatFixture is the committed golden DB produced by pre-removal code.
	dbcompatFixture = "testdata/dbcompat/db.golden"

	// dbcompatCompleteRepGroup holds the two archived (complete) fixture jobs.
	dbcompatCompleteRepGroup = "reliable2-dbcompat-complete"

	// dbcompatIncompleteRepGroup holds the two incomplete fixture jobs left in
	// jobslive, each carrying non-empty WaitingForDepGroups and
	// LimitGroupsForDisplay.
	dbcompatIncompleteRepGroup = "reliable2-dbcompat-incomplete"

	// dbcompatCompleteCount and dbcompatIncompleteCount are the known job counts
	// baked into the fixture.
	dbcompatCompleteCount   = 2
	dbcompatIncompleteCount = 2

	// dbcompatIncompleteCmd1 is the Cmd of the first incomplete fixture job (see
	// testdata/dbcompat/gen.go). Because those jobs set neither CwdMatters nor any
	// mounts, Cmd alone fixes their Job.Key(), so it lets a test address a key
	// that recovery has not yet restored during the recovery window.
	dbcompatIncompleteCmd1 = "true 3"
)

// TestReliable2DBCompatOpen covers all four F1 acceptance tests against the
// committed fixture opened with the reworked build.
func TestReliable2DBCompatOpen(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("The reworked build opens a current-code-upgraded DB fixture without error or data loss", t, func() {
		config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

		dbPath := compactedFixtureCopy(t, serverConfig.DBFile)
		serverConfig.DBFile = dbPath
		serverConfig.DBFileBackup = dbPath + "_bk"
		serverConfig.dontWipeDevDB = true

		// F1 acceptance test 4 (before): record the already-populated index bucket
		// key counts before opening, so we can prove the one-time rebuilds did not
		// re-run.
		rtkBefore := countBucketKeys(t, dbPath, bucketRTK)
		lookupBefore := countBucketKeys(t, dbPath, bucketJobLookupEntries)

		So(rtkBefore, ShouldEqual, dbcompatCompleteCount+dbcompatIncompleteCount)
		So(lookupBefore, ShouldEqual, dbcompatCompleteCount+dbcompatIncompleteCount)

		// F1 acceptance test 1: the reworked serve opens the fixture with no error
		// and no crash - no panic on the dead buckets, no decode error on the two
		// new Job fields (recovery decodes every job in jobslive).
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)
		So(server != nil, ShouldBeTrue)

		// F1 acceptance test 4 (rebuild flag): opening an already-indexed DB must
		// not report a post-upgrade rebuild. upgradedOnOpen is set once during
		// initDB and never mutated afterwards, so reading it here is race-free.
		So(server.db.upgradedOnOpen(), ShouldBeFalse)

		So(waitUntilRecovered(server), ShouldBeTrue)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		// F1 acceptance test 2: the complete rep group, queried with
		// includeComplete=true, returns the known complete jobs as
		// JobStateComplete with the expected count.
		summaries, serr := jq.GetStatusByRepGroupMatch(dbcompatCompleteRepGroup, RepGroupMatchExact, nil, true, false)
		So(serr, ShouldBeNil)
		So(summaries[dbcompatCompleteRepGroup], ShouldNotBeNil)
		So(summaries[dbcompatCompleteRepGroup].Counts[JobStateComplete], ShouldEqual, dbcompatCompleteCount)

		// F1 acceptance test 3: the known incomplete jobs are recovered and become
		// reservable/runnable.
		reservedRepGroups := reserveIncompleteJobs(jq)
		So(len(reservedRepGroups), ShouldEqual, dbcompatIncompleteCount)

		for _, rg := range reservedRepGroups {
			So(rg, ShouldEqual, dbcompatIncompleteRepGroup)
		}

		disconnect(jq)
		server.Stop(ctx, true)

		// F1 acceptance test 4 (after): the index bucket key counts are unchanged
		// versus before the open, proving the one-time index rebuilds did not
		// re-run on the already-populated indices.
		rtkAfter := countBucketKeys(t, dbPath, bucketRTK)
		lookupAfter := countBucketKeys(t, dbPath, bucketJobLookupEntries)

		So(rtkAfter, ShouldEqual, rtkBefore)
		So(lookupAfter, ShouldEqual, lookupBefore)
	})
}

// TestReliable2RecoveryWindowReturnsRecovering covers H2 acceptance test 1, and
// spec E6 acceptance test 1, against the retained recovery window: a j* call for
// a key that recovery has not yet restored into the queue must receive the
// retryable ErrRecovering, not the terminal ErrBadJob. It drives the window
// deterministically by blocking recovery at the retained recoveryPauseHook seam,
// so the fixture's incomplete jobs are provably not yet enqueued.
//
// The call is made in-package on the server rather than through a connected
// client. Under spec E1 the manager publishes nothing until recovery ends, so no
// client can connect during the window at all: what becomes unreachable is the
// SERVER-REQUEST pathway in production, which is why E6 keeps ErrRecovering as
// defence in depth (the sub-millisecond publish-before-finishRecovering window
// keeps the branch reachable) and satisfies .docs/reliable2/spec.md H2's written
// contract vacuously rather than actively.
func TestReliable2RecoveryWindowReturnsRecovering(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A recovery-window j* call for a not-yet-restored key returns ErrRecovering not ErrBadJob", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)

		dbPath := compactedFixtureCopy(t, serverConfig.DBFile)
		serverConfig.DBFile = dbPath
		serverConfig.DBFileBackup = dbPath + "_bk"
		serverConfig.dontWipeDevDB = true

		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		// recovery is blocked at the pause hook, so the prior incomplete jobs have
		// not yet been re-enqueued (the single-batch enqueue runs only after the
		// hook returns) and recovery reports none restored of the known total.
		So(server.isRecovering(), ShouldBeTrue)

		restored, total := server.recoveryProgress()
		So(total, ShouldEqual, dbcompatIncompleteCount)
		So(restored, ShouldEqual, 0)

		// a reconnecting runner's request for an incomplete fixture job whose key
		// recovery has not yet restored. getij misses in the queue but, because the
		// server is still recovering, returns the retryable ErrRecovering rather
		// than the terminal ErrBadJob.
		cr := &clientRequest{Job: dbcompatIncompleteJob(dbcompatIncompleteCmd1)}

		_, _, errStr := server.getij(cr, true)
		So(errStr, ShouldEqual, ErrRecovering)
		So(strings.Contains(errStr, ErrBadJob), ShouldBeFalse)
		So(strings.Contains(errStr, ErrBadRequest), ShouldBeFalse)
	})
}

// TestReliable2RecoveryRestoresIncompleteJobs covers H2 acceptance test 2 (and
// serves acceptance #5's "incomplete jobs recover and run"): the fixture's prior
// incomplete jobs are not reservable while recovery is still running, but once
// recovery finishes they are recovered and become reservable. It again drives
// the window deterministically via the retained recoveryPauseHook seam so the
// before/after contrast is race-free.
func TestReliable2RecoveryRestoresIncompleteJobs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Prior incomplete jobs are recovered and become reservable once the recovery window finishes", t, func() {
		config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

		dbPath := compactedFixtureCopy(t, serverConfig.DBFile)
		serverConfig.DBFile = dbPath
		serverConfig.DBFileBackup = dbPath + "_bk"
		serverConfig.dontWipeDevDB = true

		server, token, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer dgsCleanup(ctx, server, release)()

		// during the window the incomplete jobs are not yet enqueued, so nothing is
		// reservable. Asserted server-side rather than through a client Reserve,
		// because under spec E1 no client can connect until publication.
		So(server.isRecovering(), ShouldBeTrue)
		So(server.q.Stats().Items, ShouldEqual, 0)

		// release recovery and wait for the background goroutine to finish and for
		// the server to publish itself.
		release()
		So(waitUntilRecovered(server), ShouldBeTrue)
		So(server.isRecovering(), ShouldBeFalse)
		So(dgsWaitServing(server), ShouldBeTrue)

		// recovery restored the known incomplete jobs (all-or-nothing single batch).
		restored, total := server.recoveryProgress()
		So(total, ShouldEqual, dbcompatIncompleteCount)
		So(restored, ShouldEqual, dbcompatIncompleteCount)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		defer disconnect(jq)

		// H2 acceptance test 2: the recovered jobs are now reservable/runnable.
		reservedRepGroups := reserveIncompleteJobs(jq)
		So(len(reservedRepGroups), ShouldEqual, dbcompatIncompleteCount)

		for _, rg := range reservedRepGroups {
			So(rg, ShouldEqual, dbcompatIncompleteRepGroup)
		}
	})
}

// compactedFixtureCopy is copyFixtureToTempDB followed by CompactDBFileStats,
// as an operator must compact an unversioned database before a manager will
// open it.
func compactedFixtureCopy(t *testing.T, suggestedName string) string {
	t.Helper()

	dbPath := copyFixtureToTempDB(t, suggestedName)

	_, err := CompactDBFileStats(dbPath)
	So(err, ShouldBeNil)

	return dbPath
}

// copyFixtureToTempDB copies the committed golden fixture into a fresh file in
// t.TempDir() (BoltDB needs an exclusive read-write open, so tests never open
// the committed file in place) and returns the copy's path. The suggestedName's
// base name is reused so the on-disk name is unsurprising.
func copyFixtureToTempDB(t *testing.T, suggestedName string) string {
	t.Helper()

	src, err := os.Open(dbcompatFixture)
	So(err, ShouldBeNil)

	defer func() { So(src.Close(), ShouldBeNil) }()

	dstPath := filepath.Join(t.TempDir(), filepath.Base(suggestedName))

	dst, err := os.Create(dstPath)
	So(err, ShouldBeNil)

	_, err = io.Copy(dst, src)
	So(err, ShouldBeNil)
	So(dst.Close(), ShouldBeNil)

	return dstPath
}

// dbcompatIncompleteJob rebuilds one of the two incomplete fixture jobs so its
// Key() matches the key recovery will restore. The fixture jobs set neither
// CwdMatters nor any mounts/container image, so Cmd alone determines Key(); the
// remaining fields mirror the fixture (see testdata/dbcompat/gen.go) for
// clarity. A reconnecting runner reconstructs its job this way to reference a
// key that recovery has not yet restored into the queue.
func dbcompatIncompleteJob(cmd string) *Job {
	return &Job{
		Cmd: cmd, Cwd: defaultUploadDir, RepGroup: dbcompatIncompleteRepGroup, ReqGroup: "reliable2-dbcompat",
	}
}

// countBucketKeys opens the BoltDB at path read-only and returns the number of
// keys in the named bucket (0 if the bucket is absent). The DB must not be open
// elsewhere when this is called.
func countBucketKeys(t *testing.T, path string, bucket []byte) int {
	t.Helper()

	db, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	So(err, ShouldBeNil)

	defer func() { So(db.Close(), ShouldBeNil) }()

	var n int

	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}

		n = b.Stats().KeyN

		return nil
	})
	So(err, ShouldBeNil)

	return n
}

// reserveIncompleteJobs reserves the recovered incomplete jobs and returns each
// distinct job's RepGroup. Each reserved job is immediately Started with this
// process's (alive) PID so a TTR expiry parks it Lost in the run sub-queue
// rather than recycling it back to ready and being re-reserved (which would
// otherwise inflate the count under the test's short TTR). Reserving is bounded
// and stops once a Reserve yields no job.
func reserveIncompleteJobs(jq *Client) []string {
	seen := make(map[string]string)

	for range dbcompatIncompleteCount + 1 {
		job, err := jq.Reserve(2 * time.Second)
		So(err, ShouldBeNil)

		if job == nil {
			break
		}

		So(jq.Started(job, os.Getpid()), ShouldBeNil)

		seen[job.Key()] = job.RepGroup
	}

	repGroups := make([]string, 0, len(seen))
	for _, rg := range seen {
		repGroups = append(repGroups, rg)
	}

	return repGroups
}
