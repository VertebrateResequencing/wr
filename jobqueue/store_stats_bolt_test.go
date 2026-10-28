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
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/limiter"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

// This file holds the "db writer" tests of bbolt's own writers (spec A3),
// which the log store replaces with its own (Phase 1), and those that hold
// bbolt's write lock or a write turn, so is deleted with bbolt (spec F5).

const (
	// storeStatsBoltJobs is how many jobs each writer is given to write.
	storeStatsBoltJobs = 3

	// storeStatsLockHold is how long the lock tests keep a writer from bbolt's
	// write lock or its write turn after it has asked. storeStatsLockMin is the
	// least lock wait a writer kept from bbolt's lock must then report: the
	// test only knows when the writer's goroutine was about to ask, so the rest
	// of the hold is left for it to get from there to asking.
	storeStatsLockHold = 400 * time.Millisecond
	storeStatsLockMin  = storeStatsLockHold / 2

	// storeStatsQueueWait bounds the wait for a writer to be seen waiting for
	// bbolt's lock, or for writes to be seen queued behind it: a few scheduler
	// rounds, on a loaded machine.
	storeStatsQueueWait = 10 * time.Second

	// storeStatsChunkedJobs is the smallest add storeBatched splits into chunks
	// (batchSizeFor's minimum batch).
	storeStatsChunkedJobs = storeBatchGranularity

	// storeStatsReserveWait is the ReserveWriteWait of the not-durable test: a
	// reservation write held up for longer is handed out before it is durable.
	storeStatsReserveWait = 50 * time.Millisecond
)

// TestStoreStatsWriterKinds checks that every write turn kind is reported
// under the A3 writer name that runs it.
func TestStoreStatsWriterKinds(t *testing.T) {
	Convey("Every write turn kind is reported under the writer that runs it", t, func() {
		want := map[writerKind]dbWriter{
			writerNewJobs:      dbWriterNewJobs,
			writerArchive:      dbWriterArchive,
			writerBestEffort:   dbWriterBestEffort,
			writerChunkedAdd:   dbWriterAddChunk,
			writerLargeNewJobs: dbWriterNewJobs,
		}

		So(want, ShouldHaveLength, writerKinds)

		for kind := range writerKinds {
			So(kind.statsWriter(), ShouldEqual, want[kind])
		}
	})
}

// TestStoreStatsBoltWriters covers spec A3's "db writer" line for each of
// bbolt's writers other than newJobs's plain report (which
// TestStoreStatsLogLines covers): it names the writer that wrote, counts the
// items written, and reports a wait for a write turn or bbolt's write lock as
// lock, not hold.
func TestStoreStatsBoltWriters(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bolt store holding 3 live jobs, which reports only when it closes", t, func() {
		productionInterval := archiveFoldReportInterval
		archiveFoldReportInterval = time.Hour

		defer func() { archiveFoldReportInterval = productionInterval }()

		logPath, logCtx := managerLogContext(t, ctx)
		database, ok := openStoreIn(logCtx, storeKindBolt, t.TempDir()).(*db)
		So(ok, ShouldBeTrue)

		defer func() { So(database.close(ctx), ShouldBeNil) }()

		jobs := storeStatsReadyJobs("store_stats_bolt", storeStatsBoltJobs)
		keys := make([]string, 0, len(jobs))

		for _, job := range jobs {
			keys = append(keys, job.Key())
		}

		_, err := database.storeNewJobsGuarded(ctx, jobs, false, nil)
		So(err, ShouldBeNil)

		for writer := range dbWriterCount {
			database.writerStats[writer].take()
		}

		writerLines := func(writer dbWriter) []string {
			So(database.close(ctx), ShouldBeNil)

			return storeStatsWriterLines(readLogFile(t, logPath), dbWriterNames[writer])
		}

		archive := func(job *Job) error {
			job.State = JobStateComplete
			job.Exited = true
			job.EndTime = time.Now()

			_, erra := database.archiveCompletion(job.Key(), job)

			return erra
		}

		Convey("3 archives are reported under archive", func() {
			for _, job := range jobs {
				So(archive(job), ShouldBeNil)
			}

			storeStatsAssertItems(t, writerLines(dbWriterArchive))
		})

		Convey("3 exits are reported under bestEffort", func() {
			for _, job := range jobs {
				job.State = JobStateBuried
				job.Exited = true
				job.Exitcode = 1

				database.updateJobAfterExit(ctx, job, nil, nil, false)
			}

			storeStatsAssertItems(t, writerLines(dbWriterBestEffort))
		})

		Convey("deleting the 3 is reported under batch as 1 call of 3 items", func() {
			So(database.deleteLiveJobs(ctx, keys), ShouldBeNil)

			So(storeStatsAssertItems(t, writerLines(dbWriterBatch)), ShouldEqual, 1)
		})

		Convey("storing the 3 live to run again is reported under batch as 1 tx of 3 items", func() {
			So(database.storeLiveForRerun(jobs), ShouldBeNil)

			So(storeStatsAssertItems(t, writerLines(dbWriterBatch)), ShouldEqual, 1)
		})

		Convey("marking the 3 to run again is reported under batch as 1 tx of 3 items", func() {
			for _, job := range jobs {
				job.RerunAfterRun = true
			}

			So(database.storeRunningRerunMarks(jobs), ShouldBeNil)

			So(storeStatsAssertItems(t, writerLines(dbWriterBatch)), ShouldEqual, 1)
		})

		Convey("modifying 1 is reported under batch as 1 tx of 1 item", func() {
			So(database.modifyLiveJobs(ctx, keys[:1], jobs[:1]), ShouldBeNil)

			lines := writerLines(dbWriterBatch)
			So(storeStatsSum(t, lines, "txs"), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "items"), ShouldEqual, 1)
		})

		Convey("storing 3 lookups is reported under batch as 1 call of 3 items", func() {
			lookups := make(sobsd, 0, len(keys))
			for _, key := range keys {
				lookups = append(lookups, [2][]byte{[]byte("store_stats_lookup" + key), []byte(key)})
			}

			So(database.storeLookups(bucketRGs, lookups), ShouldBeNil)

			So(storeStatsAssertItems(t, writerLines(dbWriterBatch)), ShouldEqual, 1)
		})

		Convey("storing 2 limit groups is reported under batch as 1 call of 2 items", func() {
			_, _, errl := database.storeLimitGroups(map[string]*limiter.GroupData{
				"store_stats_a": limiter.NewCountGroupData(1),
				"store_stats_b": limiter.NewCountGroupData(2),
			})
			So(errl, ShouldBeNil)

			lines := writerLines(dbWriterBatch)
			So(storeStatsSum(t, lines, "txs"), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "items"), ShouldEqual, 2)
		})

		Convey("storing an environment is reported under batch as 1 call of 1 item", func() {
			_, erre := database.storeEnv([]byte("STORE_STATS=1"))
			So(erre, ShouldBeNil)

			lines := writerLines(dbWriterBatch)
			So(storeStatsSum(t, lines, "txs"), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "items"), ShouldEqual, 1)
		})

		Convey("an add too big for one transaction is reported under addChunk with an item per record", func() {
			before := storeStatsNewJobRecords(database)

			_, erra := database.storeNewJobsGuarded(ctx,
				storeStatsReadyJobs("store_stats_bolt_chunked", storeStatsChunkedJobs), false, nil)
			So(erra, ShouldBeNil)

			written := storeStatsNewJobRecords(database) - before
			So(written, ShouldBeGreaterThan, storeStatsChunkedJobs)

			lines := writerLines(dbWriterAddChunk)
			So(lines, ShouldNotBeEmpty)
			So(storeStatsSum(t, lines, "items"), ShouldEqual, written)
			So(storeStatsSum(t, lines, "txs"), ShouldBeGreaterThan, 1)
		})

		Convey("3 adds queued while one waits for bbolt's write lock are reported as 3 items in 2 txs", func() {
			held := storeStatsHoldWriteLock(database)
			defer held.release()

			added := make(chan error, storeStatsBoltJobs)
			add := func(i int) {
				go func() {
					_, erra := database.storeNewJobsGuarded(ctx,
						storeStatsReadyJobs("store_stats_bolt_coalesced"+strconv.Itoa(i), 1), false, nil)
					added <- erra
				}()
			}

			add(0)
			So(pollUntilFor(storeStatsQueueWait, func() bool { return database.turns.heldBy(writerNewJobs) }),
				ShouldBeTrue)

			for i := 1; i < storeStatsBoltJobs; i++ {
				add(i)
			}

			So(pollUntilFor(storeStatsQueueWait, func() bool {
				database.njMu.Lock()
				defer database.njMu.Unlock()

				return len(database.njPending) == storeStatsBoltJobs-1
			}), ShouldBeTrue)

			held.release()
			storeStatsAwaitAll(added, storeStatsBoltJobs)

			lines := writerLines(dbWriterNewJobs)
			So(storeStatsSum(t, lines, "items"), ShouldEqual, storeStatsBoltJobs)
			So(storeStatsSum(t, lines, "txs"), ShouldEqual, 2)
		})

		Convey("3 archives queued while one waits for bbolt's write lock are reported as 3 items in 2 txs", func() {
			held := storeStatsHoldWriteLock(database)
			defer held.release()

			archived := make(chan error, len(jobs))
			queue := func(job *Job) { go func() { archived <- archive(job) }() }

			queue(jobs[0])
			So(pollUntilFor(storeStatsQueueWait, func() bool { return database.turns.heldBy(writerArchive) }),
				ShouldBeTrue)

			for _, job := range jobs[1:] {
				queue(job)
			}

			So(pollUntilFor(storeStatsQueueWait, func() bool {
				database.arMu.Lock()
				defer database.arMu.Unlock()

				return len(database.arPending) == len(jobs)-1
			}), ShouldBeTrue)

			held.release()
			storeStatsAwaitAll(archived, len(jobs))

			lines := writerLines(dbWriterArchive)
			So(storeStatsSum(t, lines, "items"), ShouldEqual, len(jobs))
			So(storeStatsSum(t, lines, "txs"), ShouldEqual, 2)
		})

		Convey("an archive kept waiting for its write turn reports that wait as lock, not hold", func() {
			database.turns.takeSince(writerChunkedAdd, time.Time{})

			given := false

			defer func() {
				if !given {
					database.turns.give()
				}
			}()

			done := make(chan error, 1)

			go func() { done <- archive(jobs[0]) }()

			So(pollUntilFor(storeStatsQueueWait, func() bool {
				database.turns.mu.Lock()
				defer database.turns.mu.Unlock()

				return len(database.turns.waiting) == 1
			}), ShouldBeTrue)

			waiting := time.Now()

			time.Sleep(storeStatsLockHold)

			giving := time.Now()

			database.turns.give()

			given = true

			storeStatsAwaitAll(done, 1)

			returned := time.Now()

			lines := writerLines(dbWriterArchive)
			So(lines, ShouldHaveLength, 1)
			So(storeStatsDuration(t, lines[0], "maxLock"), ShouldBeGreaterThanOrEqualTo,
				giving.Sub(waiting)-statsDurationPrecision)
			So(storeStatsDuration(t, lines[0], "maxHold"), ShouldBeLessThanOrEqualTo,
				returned.Sub(giving)+statsDurationPrecision)
		})

		for _, held := range []struct {
			name   string
			writer dbWriter
			write  func() error
		}{
			{"an archive", dbWriterArchive, func() error { return archive(jobs[0]) }},
			{"a delete", dbWriterBatch, func() error { return database.deleteLiveJobs(ctx, keys[:1]) }},
			{"a store to run again", dbWriterBatch, func() error { return database.storeLiveForRerun(jobs[:1]) }},
		} {
			Convey(held.name+" kept from bbolt's write lock reports that wait as lock, not hold", func() {
				lock := storeStatsHoldWriteLock(database)
				defer lock.release()

				asking := make(chan struct{})
				done := make(chan error, 1)

				go func() {
					close(asking)

					done <- held.write()
				}()

				<-asking
				time.Sleep(storeStatsLockHold)

				releasing := time.Now()

				lock.release()
				storeStatsAwaitAll(done, 1)

				returned := time.Now()

				// the hold began no sooner than the lock was released, and
				// ended before the write returned.
				lines := writerLines(held.writer)
				So(lines, ShouldHaveLength, 1)
				So(storeStatsDuration(t, lines[0], "maxLock"), ShouldBeGreaterThanOrEqualTo, storeStatsLockMin)
				So(storeStatsDuration(t, lines[0], "maxHold"), ShouldBeLessThanOrEqualTo,
					returned.Sub(releasing)+statsDurationPrecision)
			})
		}
	})
}

// TestStoreStatsBoltWaits covers the waits of spec A3's "add wait" and
// "reservation wait" lines when bbolt's write lock holds them up: each is
// reported as at least as long as it was held up for, and a reservation handed
// out before its write reached disk is counted as notDurable.
func TestStoreStatsBoltWaits(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a bolt server holding 1 job, whose reservations wait 50ms to be written, and which reports when it "+
		"stops", t, func() {
		productionInterval := archiveFoldReportInterval
		archiveFoldReportInterval = time.Hour

		defer func() { archiveFoldReportInterval = productionInterval }()

		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.storeKind = storeKindBolt
		serverConfig.Timings.ReserveWriteWait = storeStatsReserveWait
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

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo store stats not durable", Cwd: testCwd,
			RepGroup: storeStatsGroup, ReqGroup: storeStatsGroup, Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		Convey("a reservation handed out while bbolt's write lock is held is counted as not durable", func() {
			held := storeStatsHoldWriteLock(server.boltDB())
			job, errr := jq.Reserve(5 * time.Second)

			held.release()

			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(jq.Disconnect(), ShouldBeNil)

			server.Stop(ctx, true)

			stopped = true

			lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
			So(storeStatsN(t, lines), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "notDurable"), ShouldEqual, 1)
			So(storeStatsDuration(t, lines[0], "max"), ShouldBeGreaterThanOrEqualTo, storeStatsReserveWait)
		})

		Convey("an add made while bbolt's write lock is held reports at least the time it was kept waiting", func() {
			database := server.boltDB()
			held := storeStatsHoldWriteLock(database)

			defer held.release()

			added := make(chan error, 1)

			go func() {
				_, _, erra := jq.Add([]*Job{{
					Cmd: "echo store stats add wait", Cwd: testCwd,
					RepGroup: storeStatsGroup, ReqGroup: storeStatsGroup, Requirements: standardReqs,
				}}, os.Environ(), true)
				added <- erra
			}()

			So(pollUntilFor(storeStatsQueueWait, func() bool { return database.turns.heldBy(writerNewJobs) }),
				ShouldBeTrue)

			waiting := time.Now()

			time.Sleep(storeStatsLockHold)

			releasing := time.Now()

			held.release()
			storeStatsAwaitAll(added, 1)
			So(jq.Disconnect(), ShouldBeNil)

			server.Stop(ctx, true)

			stopped = true

			lines := logLinesContaining(readLogFile(t, logPath), addWaitLogMatch)
			So(storeStatsN(t, lines), ShouldEqual, 2)
			So(storeStatsDuration(t, lines[0], "max"), ShouldBeGreaterThanOrEqualTo,
				releasing.Sub(waiting)-statsDurationPrecision)
		})
	})
}

// storeStatsReadyJobs returns n ready jobs in repGroup.
func storeStatsReadyJobs(repGroup string, n int) []*Job {
	jobs := make([]*Job, 0, n)

	for i := range n {
		job := testDBJob("echo "+repGroup+" "+strconv.Itoa(i), repGroup)
		job.State = JobStateReady
		jobs = append(jobs, job)
	}

	return jobs
}

// heldWriteLock is bbolt's write lock, held by a test.
type heldWriteLock struct {
	tx       *bolt.Tx
	released bool
}

// storeStatsHoldWriteLock takes bbolt's single write lock, which every write
// transaction then waits for until release.
func storeStatsHoldWriteLock(database *db) *heldWriteLock {
	tx, err := database.bolt.Begin(true)
	So(err, ShouldBeNil)

	return &heldWriteLock{tx: tx}
}

// release gives the lock back; later calls do nothing.
func (h *heldWriteLock) release() {
	if h.released {
		return
	}

	h.released = true

	So(h.tx.Rollback(), ShouldBeNil)
}

// heldBy says whether kind's writer has a write turn in progress, which it
// takes before asking for bbolt's write lock.
func (w *writeTurns) heldBy(kind writerKind) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.busy && !w.kept && w.holder == kind
}

// storeStatsAwaitAll waits for n writes to report on done, asserting each
// succeeded.
func storeStatsAwaitAll(done <-chan error, n int) {
	for range n {
		select {
		case err := <-done:
			So(err, ShouldBeNil)
		case <-time.After(time.Minute):
			So("a write did not finish", ShouldBeEmpty)

			return
		}
	}
}

// storeStatsNewJobRecords counts the records in the buckets an add writes.
func storeStatsNewJobRecords(database *db) int {
	records := 0

	err := database.bolt.View(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketRTK, bucketRGs, bucketDepGroups, bucketRDTK, bucketJobsLive} {
			records += tx.Bucket(bucket).Stats().KeyN
		}

		return nil
	})
	So(err, ShouldBeNil)

	return records
}

// storeStatsAssertItems asserts that lines report one item per job, in
// between 1 and that many transactions, and returns how many transactions.
func storeStatsAssertItems(t *testing.T, lines []string) int {
	t.Helper()

	So(storeStatsSum(t, lines, "items"), ShouldEqual, storeStatsBoltJobs)

	txs := storeStatsSum(t, lines, "txs")
	So(txs, ShouldBeBetweenOrEqual, 1, storeStatsBoltJobs)

	return txs
}
