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
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	compactStdTestJobs      = 20
	compactStdTestRawBytes  = 4096
	compactStdTestRepGroup  = "rg-compact-std"
	compactStdTestNested    = "compactStdTestParent"
	compactStdTestNestedSeq = 42
)

// boltData is every value and every bucket sequence in a BoltDB file, keyed by
// slash-joined bucket path.
type boltData struct {
	values    map[string]string
	sequences map[string]uint64
}

// readBoltData reads every value and bucket sequence, at any depth, of the
// BoltDB file at path.
func readBoltData(t *testing.T, path string) boltData {
	t.Helper()

	data := boltData{values: make(map[string]string), sequences: make(map[string]uint64)}

	bdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	So(err, ShouldBeNil)

	defer func() { So(bdb.Close(), ShouldBeNil) }()

	err = bdb.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			return collectBoltData(string(name)+"/", b, data)
		})
	})
	So(err, ShouldBeNil)

	return data
}

func TestDBCompactStripsOldCompleteStd(t *testing.T) {
	ctx := context.Background()

	Convey("Given an unversioned db whose complete records hold output, as pre-#608 code wrote them", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")
		keys, quietKey := populateCompactStdDB(ctx, t, dbFile, true)

		before := readBoltData(t, dbFile)

		Convey("CompactDBFile strips the output once, keeps everything else, stamps it and shrinks it", func() {
			decodes := observeCompactStdDecodes(t)

			stats, err := CompactDBFile(dbFile)
			So(err, ShouldBeNil)
			So(stats.OutputStripped, ShouldBeTrue)
			So(stats.JobsStripped, ShouldEqual, len(keys))
			So(*decodes, ShouldEqual, len(keys)+1)
			So(stats.AfterSize, ShouldBeLessThan, stats.BeforeSize)

			info, errs := os.Stat(dbFile)
			So(errs, ShouldBeNil)
			So(info.Size(), ShouldEqual, stats.AfterSize)

			version, _ := testDBSchemaVersion(t, dbFile)
			So(version, ShouldEqual, dbSchemaVersionNoCompleteStd)

			after := readBoltData(t, dbFile)
			So(after.sequences, ShouldContainKey, "meta/")
			delete(after.sequences, "meta/")
			So(after.sequences, ShouldResemble, before.sequences)
			So(after.sequences[compactStdTestNested+"/child/"], ShouldEqual, compactStdTestNestedSeq)
			So(withoutPrefixes(after.values, "jobscomplete/", "meta/"),
				ShouldResemble, withoutPrefixes(before.values, "jobscomplete/"))
			So(after.values["jobscomplete/"+quietKey], ShouldEqual, before.values["jobscomplete/"+quietKey])

			var stripped int

			for _, key := range keys {
				was := rawDecodeJob(t, before.values["jobscomplete/"+key])
				now := rawDecodeJob(t, after.values["jobscomplete/"+key])

				if len(was.StdOutC) > 0 && len(was.StdErrC) > 0 && now.StdOutC == nil && now.StdErrC == nil {
					stripped++
				}

				was.StdOutC, was.StdErrC = nil, nil
				So(now, ShouldResemble, was)
			}

			So(stripped, ShouldEqual, len(keys))

			reDB, _, errr := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(errr, ShouldBeNil)

			jobs, errj := reDB.retrieveCompleteJobsByKeys(keys)
			So(errj, ShouldBeNil)
			So(reDB.close(ctx), ShouldBeNil)
			So(len(jobs), ShouldEqual, len(keys))

			var empty int

			for _, job := range jobs {
				stdout, erro := job.StdOut()
				stderr, erre := job.StdErr()

				noOutput := erro == nil && erre == nil && stdout == "" && stderr == ""
				if noOutput && strings.HasPrefix(job.Cmd, "echo compact std ") {
					empty++
				}
			}

			So(empty, ShouldEqual, len(keys))

			Convey("and a second compaction does no strip pass", func() {
				*decodes = 0

				stats, err = CompactDBFile(dbFile)
				So(err, ShouldBeNil)
				So(stats.OutputStripped, ShouldBeFalse)
				So(stats.JobsStripped, ShouldEqual, 0)
				So(*decodes, ShouldEqual, 0)
			})
		})

		Convey("the strip copy commits whenever a transaction would exceed txMaxSize bytes", func() {
			dstFile := filepath.Join(filepath.Dir(dbFile), "dst.db")

			src, err := bolt.Open(dbFile, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
			So(err, ShouldBeNil)

			dst, err := bolt.Open(dstFile, dbFilePermission, &bolt.Options{Timeout: time.Second})
			So(err, ShouldBeNil)

			txBefore := boltTxID(dst)

			// a 1-byte limit commits before every key after the first.
			result, err := compactStrippingStd(dst, src, 1)
			So(err, ShouldBeNil)
			So(result.stripped, ShouldEqual, len(keys))
			So(boltTxID(dst)-txBefore, ShouldBeGreaterThanOrEqualTo, len(before.values))
			So(src.Close(), ShouldBeNil)
			So(dst.Close(), ShouldBeNil)

			after := readBoltData(t, dstFile)
			So(withoutPrefixes(after.values, "jobscomplete/", "meta/"),
				ShouldResemble, withoutPrefixes(before.values, "jobscomplete/"))
			So(len(withoutPrefixes(after.values, "meta/")), ShouldEqual, len(before.values))

			version, _ := testDBSchemaVersion(t, dstFile)
			So(version, ShouldEqual, dbSchemaVersionNoCompleteStd)
		})

		Convey("CompactDBFile copies undecodable complete records unchanged, reports them and still stamps", func() {
			const badRecords = maxUnreadableKeysReported + 2

			badKeys := make([]string, 0, badRecords)

			for i := range badRecords {
				key := fmt.Sprintf("undecodable%02d", i)
				So(putRawBolt(t, dbFile, bucketJobsComplete, []byte(key), []byte{0xff, 0xff, 0xff}), ShouldBeNil)

				badKeys = append(badKeys, key)
			}

			stats, err := CompactDBFile(dbFile)
			So(err, ShouldBeNil)
			So(stats.OutputStripped, ShouldBeTrue)
			So(stats.JobsStripped, ShouldEqual, len(keys))
			So(stats.JobsUnreadable, ShouldEqual, badRecords)
			So(stats.UnreadableKeys, ShouldResemble, badKeys[:maxUnreadableKeysReported])

			after := readBoltData(t, dbFile)

			var identical int

			for _, key := range badKeys {
				if after.values["jobscomplete/"+key] == "\xff\xff\xff" {
					identical++
				}
			}

			So(identical, ShouldEqual, badRecords)

			version, _ := testDBSchemaVersion(t, dbFile)
			So(version, ShouldEqual, dbSchemaVersionNoCompleteStd)
		})

		Convey("CompactDBFile leaves the original untouched when it fails", func() {
			So(updateRawBolt(t, dbFile, func(tx *bolt.Tx) error {
				b, errc := tx.CreateBucket(bucketMeta)
				if errc != nil {
					return errc
				}

				return b.Put(metaKeySchemaVersion, []byte("bad"))
			}), ShouldBeNil)

			original, errr := os.ReadFile(dbFile)
			So(errr, ShouldBeNil)

			stats, err := CompactDBFile(dbFile)
			So(err, ShouldWrap, errBadDBSchemaVersion)
			So(stats.OutputStripped, ShouldBeFalse)

			now, errr := os.ReadFile(dbFile)
			So(errr, ShouldBeNil)
			So(bytes.Equal(now, original), ShouldBeTrue)

			entries, errr := os.ReadDir(filepath.Dir(dbFile))
			So(errr, ShouldBeNil)

			var leftovers int

			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".compact-") {
					leftovers++
				}
			}

			So(leftovers, ShouldEqual, 0)
		})
	})

	Convey("Given a v1 db whose complete records hold output, as a downgraded wr could write them", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")
		populateCompactStdDB(ctx, t, dbFile, false)

		before := readBoltData(t, dbFile)

		Convey("CompactDBFile copies it verbatim without decoding any record", func() {
			decodes := observeCompactStdDecodes(t)

			stats, err := CompactDBFile(dbFile)
			So(err, ShouldBeNil)
			So(stats.OutputStripped, ShouldBeFalse)
			So(stats.JobsStripped, ShouldEqual, 0)
			So(*decodes, ShouldEqual, 0)

			after := readBoltData(t, dbFile)
			So(after.values, ShouldResemble, before.values)
			So(after.sequences, ShouldResemble, before.sequences)
		})
	})
}

// populateCompactStdDB creates a db at dbFile holding live jobs with std bucket
// entries, compactStdTestJobs complete jobs archived with output, one complete
// job archived without output, and a nested bucket with a sequence. With
// unversioned, the db is unstamped before the jobs are written, as a db from an
// older wr would be. It returns the keys of the jobs archived with output and
// the key of the one without.
func populateCompactStdDB(ctx context.Context, t *testing.T, dbFile string, unversioned bool) ([]string, string) {
	t.Helper()

	testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
	So(err, ShouldBeNil)

	if unversioned {
		So(testDB.close(ctx), ShouldBeNil)
		unstampDB(t, dbFile)

		testDB, _, err = initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
		So(err, ShouldBeNil)
	}

	live := []*Job{testDBJob("echo compact live 1", compactStdTestRepGroup),
		testDBJob("echo compact live 2", compactStdTestRepGroup)}
	queued, _, _, errs := testDB.storeNewJobs(ctx, live, false)
	So(errs, ShouldBeNil)
	So(len(queued), ShouldEqual, len(live))

	endTime := time.Now().Add(-time.Hour).Truncate(time.Nanosecond)
	keys := make([]string, 0, compactStdTestJobs)

	for i := range compactStdTestJobs {
		job := testDBArchivedJob(fmt.Sprintf("echo compact std %d", i), compactStdTestRepGroup, endTime)
		job.StdOutC = compressStd([]byte(rand.Text()))
		job.StdErrC = compressStd(randomTestBytes(compactStdTestRawBytes))
		So(testDB.archiveJob(ctx, job.Key(), job), ShouldBeNil)

		keys = append(keys, job.Key())
	}

	quiet := testDBArchivedJob("echo compact quiet", compactStdTestRepGroup, endTime)
	So(testDB.archiveJob(ctx, quiet.Key(), quiet), ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)

	So(putRawBolt(t, dbFile, bucketStdO, []byte(live[0].Key()), compressStd([]byte("live stdout"))), ShouldBeNil)
	So(putRawBolt(t, dbFile, bucketStdE, []byte(live[0].Key()), compressStd([]byte("live stderr"))), ShouldBeNil)
	So(updateRawBolt(t, dbFile, func(tx *bolt.Tx) error {
		parent, errc := tx.CreateBucket([]byte(compactStdTestNested))
		if errc != nil {
			return errc
		}

		child, errc := parent.CreateBucket([]byte("child"))
		if errc != nil {
			return errc
		}

		if errc = child.SetSequence(compactStdTestNestedSeq); errc != nil {
			return errc
		}

		return child.Put([]byte("k"), []byte("v"))
	}), ShouldBeNil)

	return keys, quiet.Key()
}

// unstampDB removes the meta bucket from the BoltDB file at path, making it look
// like a db written before schema versions existed.
func unstampDB(t *testing.T, path string) {
	t.Helper()

	So(updateRawBolt(t, path, func(tx *bolt.Tx) error {
		return tx.DeleteBucket(bucketMeta)
	}), ShouldBeNil)
}

// updateRawBolt runs fn in a write transaction on the closed BoltDB file at
// path.
func updateRawBolt(t *testing.T, path string, fn func(*bolt.Tx) error) error {
	t.Helper()

	bdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{Timeout: time.Second})
	So(err, ShouldBeNil)

	defer func() { So(bdb.Close(), ShouldBeNil) }()

	return bdb.Update(fn)
}

// randomTestBytes returns n incompressible bytes.
func randomTestBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)

	return b
}

func putRawBolt(t *testing.T, path string, bucket, key, value []byte) error {
	t.Helper()

	return updateRawBolt(t, path, func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put(key, value)
	})
}

// observeCompactStdDecodes counts the complete records the strip pass decodes,
// restoring the observer when the test ends.
func observeCompactStdDecodes(t *testing.T) *int {
	t.Helper()

	var decodes int

	compactStdDecodeObserver = func() { decodes++ }

	t.Cleanup(func() { compactStdDecodeObserver = nil })

	return &decodes
}

// testDBSchemaVersion returns the schema version of the BoltDB file at path and
// whether it has a meta bucket at all.
func testDBSchemaVersion(t *testing.T, path string) (uint64, bool) {
	t.Helper()

	bdb, err := bolt.Open(path, dbFilePermission, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	So(err, ShouldBeNil)

	defer func() { So(bdb.Close(), ShouldBeNil) }()

	var hasMeta bool

	So(bdb.View(func(tx *bolt.Tx) error {
		hasMeta = tx.Bucket(bucketMeta) != nil

		return nil
	}), ShouldBeNil)

	version, err := dbFileSchemaVersion(bdb)
	So(err, ShouldBeNil)

	return version, hasMeta
}

// withoutPrefixes returns a copy of values without the keys that start with any
// of prefixes.
func withoutPrefixes(values map[string]string, prefixes ...string) map[string]string {
	kept := make(map[string]string, len(values))

	for k, v := range values {
		if !hasAnyPrefix(k, prefixes) {
			kept[k] = v
		}
	}

	return kept
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}

	return false
}

// rawDecodeJob decodes an encoded Job with the db's codec and nothing else, so
// two records can be compared field by field.
func rawDecodeJob(t *testing.T, encoded string) *Job {
	t.Helper()

	job := &Job{}
	So(codec.NewDecoderBytes([]byte(encoded), new(codec.BincHandle)).Decode(job), ShouldBeNil)

	return job
}

// boltTxID returns the ID of bdb's last committed write transaction.
func boltTxID(bdb *bolt.DB) int {
	var id int

	So(bdb.View(func(tx *bolt.Tx) error {
		id = tx.ID()

		return nil
	}), ShouldBeNil)

	return id
}

func TestDBCompactGoldenFixture(t *testing.T) {
	Convey("The unversioned db-compat golden db compacts and is stamped", t, func() {
		golden, err := os.ReadFile(dbcompatFixture)
		So(err, ShouldBeNil)

		dbFile := copyFixtureToTempDB(t, "queue.db")

		version, hasMeta := testDBSchemaVersion(t, dbFile)
		So(hasMeta, ShouldBeFalse)
		So(version, ShouldEqual, 0)

		before := readBoltData(t, dbFile)

		stats, err := CompactDBFile(dbFile)
		So(err, ShouldBeNil)
		So(stats.OutputStripped, ShouldBeTrue)
		So(stats.JobsStripped, ShouldEqual, 0)

		version, _ = testDBSchemaVersion(t, dbFile)
		So(version, ShouldEqual, dbSchemaVersionNoCompleteStd)

		after := readBoltData(t, dbFile)
		So(withoutPrefixes(after.values, "meta/"), ShouldResemble, before.values)
		So(countBucketKeys(t, dbFile, bucketJobsComplete), ShouldEqual, dbcompatCompleteCount)

		stillGolden, err := os.ReadFile(dbcompatFixture)
		So(err, ShouldBeNil)
		So(bytes.Equal(stillGolden, golden), ShouldBeTrue)
	})
}

func collectBoltData(prefix string, b *bolt.Bucket, data boltData) error {
	data.sequences[prefix] = b.Sequence()

	return b.ForEach(func(k, v []byte) error {
		if v == nil {
			return collectBoltData(prefix+string(k)+"/", b.Bucket(k), data)
		}

		data.values[prefix+string(k)] = string(v)

		return nil
	})
}

func TestDBSchemaVersionOnOpen(t *testing.T) {
	ctx := context.Background()

	Convey("initDB stamps a new database with the current schema version", t, func() {
		dbFile := filepath.Join(t.TempDir(), "queue.db")

		testDB, _, err := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
		So(err, ShouldBeNil)
		So(testDB.close(ctx), ShouldBeNil)

		version, hasMeta := testDBSchemaVersion(t, dbFile)
		So(hasMeta, ShouldBeTrue)
		So(version, ShouldEqual, dbSchemaVersionNoCompleteStd)

		Convey("and leaves an existing unversioned database unstamped", func() {
			unstampDB(t, dbFile)

			reDB, _, errr := initDB(ctx, dbFile, dbFile+".bak", internal.Development, false, false)
			So(errr, ShouldBeNil)
			So(reDB.close(ctx), ShouldBeNil)

			version, hasMeta = testDBSchemaVersion(t, dbFile)
			So(hasMeta, ShouldBeFalse)
			So(version, ShouldEqual, 0)
		})
	})
}
