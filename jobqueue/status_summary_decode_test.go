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

// Regression test for the prodsim finding that `wr status -i <rg> -o summary`
// slowed with the RepGroup's archived history (25.6s median at 3,000 runners).
// The summary's usage figures need only a handful of each archived record's
// fields, but it fully decoded every record, Cmd and Env included. The cost is
// asserted as a count of full archived decodes (db.archivedDecodes), not as a
// timing bound, and the summary is compared with what the full decode gave.

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	statusSummaryDecodeRepGroup = "status-summary-decode"
	statusSummaryDecodeOther    = "status-summary-decode-other"
	statusSummaryDecodeArchived = 300
	statusSummaryDecodeLiveKey  = 3
	statusSummaryDecodeCmdBytes = 4096
)

// TestStatusSummaryWithoutDecodingHistory pins that a RepGroup's detailed
// status summary (counts plus memory, disk, walltime, cputime and start/end)
// is built without fully decoding any archived job, and that it is exactly the
// summary the full decode produced.
func TestStatusSummaryWithoutDecodingHistory(t *testing.T) {
	Convey("Given a RepGroup with a varied archived history", t, func() {
		ctx := context.Background()
		tmpdir := t.TempDir()

		testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() { So(testDB.close(ctx), ShouldBeNil) }()

		So(seedStatusSummaryHistory(testDB, statusSummaryDecodeArchived, statusSummaryDecodeCmdBytes), ShouldBeNil)

		want := fullDecodeCompleteJobStatus(testDB, statusSummaryDecodeRepGroup)
		So(want.Counts[JobStateComplete], ShouldEqual, statusSummaryDecodeArchived-1)
		So(want.Memory.NumDataValues(), ShouldEqual, statusSummaryDecodeArchived-1)
		So(want.StartTime.IsZero(), ShouldBeFalse)

		Convey("the detailed summary decodes none of it and matches the full decode exactly", func() {
			before := testDB.archivedDecodes.Load()
			allocated := allocatedBytes()

			got, errr := testDB.retrieveCompleteJobStatusByRepGroup(statusSummaryDecodeRepGroup, true)
			So(errr, ShouldBeNil)

			// a full decode allocates each record's whole Cmd, so this also
			// catches one that bypasses the decode counter
			perRecord := (allocatedBytes() - allocated) / statusSummaryDecodeArchived
			So(perRecord, ShouldBeLessThan, statusSummaryDecodeCmdBytes/2)

			So(testDB.archivedDecodes.Load()-before, ShouldEqual, 0)
			So(got, ShouldResemble, want)
		})

		Convey("the count-only summary still counts without decoding", func() {
			before := testDB.archivedDecodes.Load()

			got, errr := testDB.retrieveCompleteJobStatusByRepGroup(statusSummaryDecodeRepGroup, false)
			So(errr, ShouldBeNil)

			So(testDB.archivedDecodes.Load()-before, ShouldEqual, 0)
			So(got.Counts, ShouldResemble, want.Counts)
		})
	})
}

// seedStatusSummaryHistory writes count archived jobs of the test RepGroup,
// each with a cmdBytes-long Cmd and its own usage figures, straight into the
// complete bucket, interleaved with another RepGroup's. One of them is also
// live again (being re-run), and the RepGroup also has a live-only job, so
// both must be left out of the summary.
func seedStatusSummaryHistory(testDB *db, count, cmdBytes int) error {
	zone := time.FixedZone("summary", 3600)
	base := time.Date(2026, 9, 28, 10, 0, 0, 0, zone)
	padding := strings.Repeat("x", cmdBytes)

	return testDB.bolt.Update(func(tx *bolt.Tx) error {
		complete := tx.Bucket(bucketJobsComplete)
		live := tx.Bucket(bucketJobsLive)
		lookup := tx.Bucket(bucketRTK)

		for i := range count {
			for _, rg := range []string{statusSummaryDecodeRepGroup, statusSummaryDecodeOther} {
				job := statusSummaryArchivedJob(rg, i, base, padding)
				key := []byte(job.Key())

				encoded, errp := testDBEncode(testDB, job)
				if errp != nil {
					return errp
				}

				if errp = complete.Put(key, encoded); errp != nil {
					return errp
				}

				if errp = lookup.Put(testDB.generateLookupKey(rg, key), nil); errp != nil {
					return errp
				}

				if rg == statusSummaryDecodeRepGroup && i == statusSummaryDecodeLiveKey {
					if errp = live.Put(key, encoded); errp != nil {
						return errp
					}
				}
			}
		}

		liveOnly := []byte("status-summary-decode-live-only")
		if errp := live.Put(liveOnly, []byte("x")); errp != nil {
			return errp
		}

		return lookup.Put(testDB.generateLookupKey(statusSummaryDecodeRepGroup, liveOnly), nil)
	})
}

// statusSummaryArchivedJob returns the i'th archived job of repGroup. Its usage
// figures vary with i, some are zero (so are not stored at all), and every 23rd
// has no start time, so the summary's handling of each is exercised.
func statusSummaryArchivedJob(repGroup string, i int, base time.Time, padding string) *Job {
	job := testDBJob("echo "+repGroup+" "+strconv.Itoa(i)+" "+padding, repGroup)
	job.State = JobStateComplete
	job.Exited = true
	job.PeakRAM = (i % 17) * 131
	job.PeakDisk = int64(i%5) * 3
	job.CPUtime = time.Duration(i%13)*time.Second + time.Duration(i)*time.Microsecond

	start := base.Add(time.Duration(i) * time.Second)
	job.EndTime = start.Add(time.Duration(i%11+1)*time.Minute + time.Duration(i)*time.Nanosecond)

	if i%23 != 0 {
		job.StartTime = start
	}

	return job
}

// allocatedBytes returns the bytes this process has allocated so far.
func allocatedBytes() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)

	return stats.TotalAlloc
}

// testDBEncode encodes job the way the db stores it.
func testDBEncode(testDB *db, job *Job) ([]byte, error) {
	var encoded []byte

	err := codec.NewEncoderBytes(&encoded, testDB.ch).Encode(job)

	return encoded, err
}

// fullDecodeCompleteJobStatus is the detailed summary as it was built before
// the fix: every archived record of repGroup that is not live again, walked in
// lookup order, fully decoded and added with AddCompleteJob. It does not touch
// db.archivedDecodes.
func fullDecodeCompleteJobStatus(testDB *db, repGroup string) *RepGroupStatus {
	summary := NewRepGroupStatus()

	err := testDB.bolt.View(func(tx *bolt.Tx) error {
		live := tx.Bucket(bucketJobsLive)
		complete := tx.Bucket(bucketJobsComplete)
		cursor := tx.Bucket(bucketRTK).Cursor()
		prefix := []byte(repGroup + dbDelimiter)

		for k, _ := cursor.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = cursor.Next() {
			key := k[len(prefix):]

			encoded := complete.Get(key)
			if len(encoded) == 0 || live.Get(key) != nil {
				continue
			}

			job, errd := testDB.decodeJob(encoded)
			if errd != nil {
				return errd
			}

			summary.AddCompleteJob(job)
		}

		return nil
	})
	So(err, ShouldBeNil)

	return summary
}

// BenchmarkRepGroupStatusDetails measures the detailed status summary of a
// RepGroup with an archived history of 20KB-command jobs, the shape prodsim's
// portal jobs had, reporting full archived decodes per summary alongside the
// usual time and allocations.
func BenchmarkRepGroupStatusDetails(b *testing.B) {
	for _, archived := range []int{1000, 10000} {
		b.Run(strconv.Itoa(archived), func(b *testing.B) {
			benchRepGroupStatusDetails(b, archived)
		})
	}
}

func benchRepGroupStatusDetails(b *testing.B, archived int) {
	b.Helper()

	ctx := context.Background()
	tmpdir := b.TempDir()

	testDB, _, err := initDB(ctx, filepath.Join(tmpdir, "queue.db"),
		filepath.Join(tmpdir, "queue.db.bak"), internal.Development, false, false)
	if err != nil {
		b.Fatal(err)
	}

	defer func() {
		if errc := testDB.close(ctx); errc != nil {
			b.Fatal(errc)
		}
	}()

	if err = seedStatusSummaryHistory(testDB, archived, 20*1024); err != nil {
		b.Fatal(err)
	}

	before := testDB.archivedDecodes.Load()

	b.ResetTimer()

	for b.Loop() {
		if _, err = testDB.retrieveCompleteJobStatusByRepGroup(statusSummaryDecodeRepGroup, true); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportMetric(float64(testDB.archivedDecodes.Load()-before)/float64(b.N), "decodes/op")
}
