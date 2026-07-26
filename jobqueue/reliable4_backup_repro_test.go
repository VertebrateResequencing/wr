//go:build reliability_repro

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

// This file provides a portable, from-scratch big-DB generator used by the
// reliable4 backup-stall reproducer (developers/wrdev.sh backup-stall-check).
//
// WHY A NAIVE "BIG FILE" IS NOT ENOUGH (the key finding of the reinvestigation):
// the production stall correlates with the DB's complete-job RECORD COUNT, not
// merely its file size. An earlier padding inflater (one throwaway bucket full of
// 1MiB values) made the file big but DID NOT reproduce the stall even at 8GB,
// while a copy of the real ~6GB / 2.1M-record production DB did. The reason is in
// the write path, not the backup copy:
//
//   - db.backupToBackupFile copies the whole file via tx.CopyFile, which is a
//     SEQUENTIAL read of tx.Size() bytes (bbolt tx.go WriteTo -> io.CopyN). So the
//     copy's duration scales with FILE SIZE alone; an 8GB padding DB copies for
//     LONGER than a 6GB real one. The copy is therefore not what differs.
//   - What differs is every foreground archive/touch write. bbolt's tx.Commit
//     rewrites the ENTIRE freelist on every commit (tx.go: freelist.Write writes
//     FreeCount+PendingCount page ids). A heavily churned production DB (millions
//     of jobs added to jobslive then archived+deleted, plus the operator clearing
//     jobslive on the safe copy) carries a LARGE persisted freelist, so each
//     commit is slow. During a backup the freed pages become "pending" (an open
//     read tx pins them, so ReleasePendingPages can't reclaim them), forcing
//     writes to grow the file -> remap -> block on the copy's mmaplock. A
//     sequentially-written padding DB has a near-empty freelist, so it commits
//     cheaply and never stalls.
//
// So a FAITHFUL from-scratch DB needs three things together: (1) ~2.1M real
// complete-job records (wr's encoding) so the b-tree and the complete/end-time
// index are as dense as production; (2) a multi-GB total size so each backup
// CopyFile takes long enough to overlap archives; and (3) a LARGE PERSISTED
// FREELIST so every archive commit is slow and pending-page growth forces remaps
// during a backup. This generator produces all three, then the wrdev harness runs
// an isolated PROD-mode manager (backups on) over it and watches for the churn.
//
// Run it (via the wrdev command) with, e.g.:
//
//	WR_INFLATE_DB=/path/to/db WR_INFLATE_RECORDS=2100000 WR_INFLATE_GB=6 \
//	  WR_INFLATE_FREELIST_GB=2 \
//	  go test -tags reliability_repro ./jobqueue/ -run TestReliable4InflateDB
//
// Set WRDEV_ROOT (the wrdev harness) / WR_INFLATE_DB to a disk with room for the
// DB (~WR_INFLATE_GB) plus its backup copy (another ~WR_INFLATE_GB).

package jobqueue

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// reliable4ChurnBucket is a throwaway bucket used only to build up a large
// persisted freelist: it is filled with page-sized values and then deleted whole
// as the final write, so its pages remain free on disk (nothing reuses them
// before close) and reload as free on the next open, exactly like a churned
// production DB. wr never reads it.
var reliable4ChurnBucket = []byte("reliable4churn") //nolint:gochecknoglobals

const (
	reliable4MiB = 1 << 20
	reliable4GiB = 1 << 30

	// reliable4DefaultRecords matches the real production DB that reproduced the
	// stall (jobscomplete = 2,110,982 records).
	reliable4DefaultRecords = 2_100_000

	// reliable4DefaultGB / reliable4DefaultFreelistGB give a ~6GB file with ~2GB
	// of persisted free pages, matching the safe copy of the real DB.
	reliable4DefaultGB         = 6
	reliable4DefaultFreelistGB = 2

	// reliable4RecordOverhead approximates the non-Cmd bytes each record costs on
	// disk: the encoded Job's other fields, plus the end-time index entry, plus
	// b-tree/page overhead. Used only to size the Cmd padding towards the target.
	reliable4RecordOverhead = 400

	// reliable4KeyCopies is how many times the Cmd-sized key contributes to the
	// on-disk footprint: once as the jobscomplete key, once inside the encoded
	// value, and once inside the end-time index key.
	reliable4KeyCopies = 3

	// reliable4RecordsPerTx / reliable4ChurnValsPerTx bound how much each write
	// transaction buffers, so no single transaction is huge.
	reliable4RecordsPerTx   = 20000
	reliable4ChurnValsPerTx = 8192

	// reliable4ChurnValueSize is the size of each throwaway churn value (one
	// page), so the freelist gains ~one free page per value written then freed.
	reliable4ChurnValueSize = 4096

	reliable4MinCmdPad = 8

	reliable4ReportEveryRecords = 100_000
)

// reliable4AllBuckets is every bucket wr's initDB creates. The generator creates
// them all (even empty) so that when the real manager later opens this DB in
// production mode it finds a complete schema and does NOT trigger the one-time
// index-rebuild upgrade path (which would confound the reproduction).
//
//nolint:gochecknoglobals // mirrors the initDB bucket set for the generator.
var reliable4AllBuckets = [][]byte{
	bucketJobsLive, bucketJobsComplete, bucketRTK, bucketRGs, bucketLGs,
	bucketDTK, bucketDepGroups, bucketRDTK, bucketJobLookupEntries, bucketEnvs,
	bucketStdO, bucketStdE, bucketJobRAM, bucketJobDisk, bucketJobSecs,
	bucketRGEndTime, bucketEndTimeToKey,
}

// reliable4InflateParams holds the parsed generation knobs.
type reliable4InflateParams struct {
	path        string
	records     int
	targetBytes int64
	freelistGB  int
	cmdPad      int
}

func TestReliable4InflateDB(t *testing.T) {
	if runnermode || servermode {
		return
	}

	p, ok := parseReliable4InflateParams(t)
	if !ok {
		return
	}

	t.Logf("INFLATE-DB: generating %d complete-job records, target ~%dGiB total, ~%dGiB freelist, cmdPad=%d bytes -> %s",
		p.records, p.targetBytes>>30, p.freelistGB, p.cmdPad, p.path)

	db, err := bolt.Open(p.path, 0o600, &bolt.Options{
		Timeout:      30 * time.Second,
		FreelistType: bolt.FreelistMapType, // match production initDB
	})
	if err != nil {
		t.Fatalf("could not open/create bolt DB at %s: %v", p.path, err)
	}

	createReliable4Buckets(t, db)
	writeReliable4CompleteRecords(t, db, p)
	buildReliable4Freelist(t, db, p.freelistGB)

	if errc := db.Close(); errc != nil {
		t.Fatalf("closing generated DB failed: %v", errc)
	}

	reportReliable4DB(t, p.path)
}

// parseReliable4InflateParams reads the WR_INFLATE_* env vars, applies defaults
// and derives the per-record Cmd padding needed to approach the target size. It
// returns ok=false (and skips) when no path is set.
func parseReliable4InflateParams(t *testing.T) (reliable4InflateParams, bool) {
	path := os.Getenv("WR_INFLATE_DB")
	if path == "" {
		t.Skip("set WR_INFLATE_DB (path) to generate a record-dense big bolt DB")

		return reliable4InflateParams{}, false
	}

	records := envIntDefault("WR_INFLATE_RECORDS", reliable4DefaultRecords)
	if records <= 0 {
		records = reliable4DefaultRecords
	}

	gb := envIntDefault("WR_INFLATE_GB", reliable4DefaultGB)
	if gb <= 0 {
		gb = reliable4DefaultGB
	}

	freelistGB := envIntDefault("WR_INFLATE_FREELIST_GB", reliable4DefaultFreelistGB)
	if freelistGB < 0 {
		freelistGB = 0
	}

	if freelistGB >= gb {
		freelistGB = gb / 2
	}

	p := reliable4InflateParams{
		path:        path,
		records:     records,
		targetBytes: int64(gb) * reliable4GiB,
		freelistGB:  freelistGB,
	}
	p.cmdPad = reliable4CmdPad(gb-freelistGB, records)

	return p, true
}

// reliable4CmdPad derives the Cmd padding (bytes) so that `records` records take
// roughly recordDataGB on disk. Each record contributes its Cmd bytes roughly
// reliable4KeyCopies times (jobscomplete key + encoded value + end-time index
// key) plus a fixed overhead.
func reliable4CmdPad(recordDataGB, records int) int {
	if records <= 0 {
		return reliable4MinCmdPad
	}

	perRecord := (int64(recordDataGB) * reliable4GiB) / int64(records)

	pad := int(perRecord/reliable4KeyCopies) - reliable4RecordOverhead
	if pad < reliable4MinCmdPad {
		pad = reliable4MinCmdPad
	}

	return pad
}

func envIntDefault(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}

	return n
}

// createReliable4Buckets creates every bucket the real initDB creates, so a later
// production open sees a complete schema and skips the upgrade/rebuild path.
func createReliable4Buckets(t *testing.T, db *bolt.DB) {
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range reliable4AllBuckets {
			if _, errc := tx.CreateBucketIfNotExists(b); errc != nil {
				return fmt.Errorf("create bucket %s: %w", b, errc)
			}
		}

		return nil
	}); err != nil {
		t.Fatalf("creating buckets failed: %v", err)
	}
}

// writeReliable4CompleteRecords fills bucketJobsComplete with `records` real,
// codec-encoded archived Jobs and mirrors each into the time-ordered
// bucketEndTimeToKey index (as archiveJobTx would), plus a per-RepGroup end-time
// entry. End times are spread over the ~90 days ending now so the index and any
// getJobsRecent scan behave like production. Written in bounded batches.
func writeReliable4CompleteRecords(t *testing.T, db *bolt.DB, p reliable4InflateParams) {
	pad := reliable4Pad(p.cmdPad)
	handle := new(codec.BincHandle)
	baseTime := time.Now().Add(-90 * 24 * time.Hour)
	spread := (90 * 24 * time.Hour) / time.Duration(max(p.records, 1))

	written := 0
	for written < p.records {
		batchEnd := min(written+reliable4RecordsPerTx, p.records)

		if err := db.Update(func(tx *bolt.Tx) error {
			return writeReliable4Batch(tx, handle, pad, baseTime, spread, written, batchEnd)
		}); err != nil {
			t.Fatalf("writing records [%d,%d) failed: %v", written, batchEnd, err)
		}

		written = batchEnd
		if written%reliable4ReportEveryRecords == 0 {
			t.Logf("INFLATE-DB: %d/%d complete records written", written, p.records)
		}
	}

	t.Logf("INFLATE-DB: all %d complete records written", p.records)
}

// writeReliable4Batch writes records [from,to) into a single transaction.
func writeReliable4Batch(tx *bolt.Tx, handle codec.Handle, pad string,
	baseTime time.Time, spread time.Duration, from, to int,
) error {
	complete := tx.Bucket(bucketJobsComplete)
	endIndex := tx.Bucket(bucketEndTimeToKey)
	rgEnd := tx.Bucket(bucketRGEndTime)

	for i := from; i < to; i++ {
		job := reliable4ArchivedJob(i, pad, baseTime.Add(spread*time.Duration(i)))
		key := []byte(job.Key())

		var encoded []byte
		if err := codec.NewEncoderBytes(&encoded, handle).Encode(job); err != nil {
			return fmt.Errorf("encode job %d: %w", i, err)
		}

		if err := complete.Put(key, encoded); err != nil {
			return err
		}

		nanos := job.EndTime.UnixNano()
		if err := endIndex.Put(endTimeIndexKey(endTimeToBytes(nanos), key), nil); err != nil {
			return err
		}

		if err := updateRGEndTime(rgEnd, job); err != nil {
			return err
		}
	}

	return nil
}

// reliable4ArchivedJob builds a realistic completed Job. Cmd carries the padding
// and a zero-padded unique index (so keys are distinct AND sort in generation
// order, letting bbolt do fast sequential leaf appends instead of random-order
// page splits - a big generation speedup; the large persisted freelist, not the
// complete-bucket's internal fragmentation, is what drives the stall). It is
// spread across a bounded set of RepGroups/ReqGroups like a real workload.
func reliable4ArchivedJob(i int, pad string, endTime time.Time) *Job {
	cmd := fmt.Sprintf("reliable4 #%012d %s", i, pad)

	return &Job{
		Cmd:      cmd,
		Cwd:      "/tmp",
		RepGroup: fmt.Sprintf("reliable4_repgroup_%d", i%64),
		ReqGroup: fmt.Sprintf("reliable4_reqgroup_%d", i%16),
		Requirements: &scheduler.Requirements{
			RAM: 100, Time: time.Hour, Cores: 1, Disk: 1,
		},
		State:     JobStateComplete,
		Exited:    true,
		Exitcode:  0,
		PeakRAM:   100,
		PeakDisk:  1,
		Host:      "reliable4-host",
		StartTime: endTime.Add(-time.Minute),
		EndTime:   endTime,
	}
}

// buildReliable4Freelist inflates a throwaway bucket with page-sized values and
// then deletes the whole bucket in a final transaction, leaving ~freelistGB of
// free pages persisted on disk (they reload as free on the next open). This is
// what makes every subsequent archive commit rewrite a large freelist, the
// record-count/churn-correlated cost that a sequentially-written DB lacks.
func buildReliable4Freelist(t *testing.T, db *bolt.DB, freelistGB int) {
	if freelistGB <= 0 {
		t.Logf("INFLATE-DB: freelist build skipped (WR_INFLATE_FREELIST_GB=0)")

		return
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		_, errc := tx.CreateBucketIfNotExists(reliable4ChurnBucket)

		return errc
	}); err != nil {
		t.Fatalf("creating churn bucket failed: %v", err)
	}

	val := make([]byte, reliable4ChurnValueSize)
	totalVals := (int64(freelistGB) * reliable4GiB) / reliable4ChurnValueSize

	var written int64
	for written < totalVals {
		batchEnd := written + reliable4ChurnValsPerTx
		if batchEnd > totalVals {
			batchEnd = totalVals
		}

		if err := db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket(reliable4ChurnBucket)
			for j := written; j < batchEnd; j++ {
				var key [8]byte
				binary.BigEndian.PutUint64(key[:], uint64(j)) //nolint:gosec // loop-bounded

				if errp := b.Put(key[:], val); errp != nil {
					return errp
				}
			}

			return nil
		}); err != nil {
			t.Fatalf("writing churn values failed at %d: %v", written, err)
		}

		written = batchEnd
	}

	t.Logf("INFLATE-DB: wrote %d churn pages, now freeing them into the freelist", written)

	// delete the whole bucket as the FINAL write, so its pages stay free on disk.
	if err := db.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket(reliable4ChurnBucket)
	}); err != nil {
		t.Fatalf("deleting churn bucket failed: %v", err)
	}
}

// reportReliable4DB reopens the generated DB and logs its size and the freelist
// size, confirming the persisted freelist reloads as free pages (the key
// property production has that a padding DB does not).
func reportReliable4DB(t *testing.T, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		t.Logf("INFLATE-DB: could not stat %s: %v", path, err)

		return
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{
		Timeout:      30 * time.Second,
		FreelistType: bolt.FreelistMapType,
	})
	if err != nil {
		t.Logf("INFLATE-DB: reopen for stats failed: %v", err)

		return
	}
	defer func() { _ = db.Close() }()

	// a no-op write tx forces the pending->free reclaim and populates stats.
	if errU := db.Update(func(*bolt.Tx) error { return nil }); errU != nil {
		t.Logf("INFLATE-DB: stats write tx failed: %v", errU)
	}

	stats := db.Stats()
	freeMiB := int64(stats.FreePageN) * int64(db.Info().PageSize) / reliable4MiB

	var complete, endIdx int

	if errV := db.View(func(tx *bolt.Tx) error {
		complete = tx.Bucket(bucketJobsComplete).Stats().KeyN
		endIdx = tx.Bucket(bucketEndTimeToKey).Stats().KeyN

		return nil
	}); errV != nil {
		t.Logf("INFLATE-DB: view for counts failed: %v", errV)
	}

	t.Logf("INFLATE-DB: done. file=%d bytes (~%.2fGiB) jobscomplete=%d endTimeToKey=%d freelist=%d pages (~%dMiB)",
		fi.Size(), float64(fi.Size())/float64(reliable4GiB), complete, endIdx, stats.FreePageN, freeMiB)
}

// reliable4Pad returns a reusable padding string of n 'x' bytes.
func reliable4Pad(n int) string {
	if n <= 0 {
		return ""
	}

	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}

	return string(b)
}
