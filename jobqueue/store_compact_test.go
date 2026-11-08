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
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestStoreCompact(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, kind := range []storeKind{storeKindBolt, storeKindLog} {
		Convey("Given a "+storeKindName(kind)+" store's manager holding the lock (E6 test 3)", t, func() {
			dbFile := filepath.Join(t.TempDir(), convertDBName)
			s := openStoreOf(ctx, dbFile, kind)

			Reset(func() { So(s.close(ctx), ShouldBeNil) })

			previous := managerDBOpenTimeout

			Reset(func() { managerDBOpenTimeout = previous })

			managerDBOpenTimeout = 100 * time.Millisecond

			Convey("CompactDBFileStats returns an error wrapping ErrDBLocked", func() {
				before := besideDBFile(dbFile)

				_, err := CompactDBFileStats(dbFile)
				So(err, ShouldWrap, ErrDBLocked)
				So(besideDBFile(dbFile), ShouldResemble, before)
			})
		})
	}

	Convey("Given no file at dbFile", t, func() {
		dir := t.TempDir()

		paths := map[string]string{
			"in an existing directory":      filepath.Join(dir, convertDBName),
			"in a missing parent directory": filepath.Join(dir, "missing", convertDBName),
		}

		for name, dbFile := range paths {
			Convey(name+", CompactDBFileStats returns stat's not-exist error for dbFile and creates nothing", func() {
				_, err := CompactDBFileStats(dbFile)
				So(err, ShouldWrap, fs.ErrNotExist)
				So(err.Error(), ShouldEqual, "stat "+dbFile+": no such file or directory")

				entries, err := os.ReadDir(dir)
				So(err, ShouldBeNil)
				So(entries, ShouldBeEmpty)
			})
		}
	})

	Convey("Given a read-only bbolt file at dbFile, so compaction fails after making its temp file", t, func() {
		if os.Geteuid() == 0 {
			SkipConvey("root ignores file permissions", func() {})

			return
		}

		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeConvertFixture(ctx, dbFile)
		So(os.Chmod(dbFile, 0o400), ShouldBeNil)

		before, err := os.ReadFile(dbFile)
		So(err, ShouldBeNil)

		Convey("CompactDBFileStats returns the error, leaves dbFile unchanged and removes its temp file", func() {
			_, err := CompactDBFileStats(dbFile)
			So(err, ShouldWrap, fs.ErrPermission)

			after, err := os.ReadFile(dbFile)
			So(err, ShouldBeNil)
			So(after, ShouldResemble, before)
			So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
		})
	})

	Convey("Given a closed store directory at dbFile and no manager (E6 test 5)", t, func() {
		dbFile := filepath.Join(t.TempDir(), convertDBName)
		writeConvertFixture(ctx, dbFile)
		s := openStoreOf(ctx, dbFile, storeKindLog)
		So(s.close(ctx), ShouldBeNil)

		before := storeFileSizes(dbFile)
		So(before, ShouldNotBeEmpty)

		Convey("CompactDBFileStats returns errCompactNotSupported and leaves its files and sizes unchanged", func() {
			_, err := CompactDBFileStats(dbFile)
			So(err, ShouldEqual, errCompactNotSupported)
			So(err.Error(), ShouldEqual, "compacting the new database store is not supported yet")
			So(storeFileSizes(dbFile), ShouldResemble, before)
		})
	})

	Convey("Given bbolt files at dbFile and no manager (E6 test 6)", t, func() {
		withStd := completedTestJob("echo compact std", time.Unix(1_750_000_000, 0))
		withStd.StdOutC = []byte("stripped out")
		withStd.StdErrC = []byte("stripped err")

		fixtures := map[string]func(dbFile string){
			"made by the bbolt store": func(dbFile string) { writeConvertFixture(ctx, dbFile) },
			"unversioned, with a complete record holding std": func(dbFile string) {
				writeRawBoltDB(dbFile, nil, map[string]map[string][]byte{
					string(bucketJobsComplete): {withStd.Key(): encodedTestJob(withStd)},
				})
			},
		}

		for name, write := range fixtures {
			Convey(name+", CompactDBFileStats compacts it as bbolt, as before the store existed", func() {
				dbFile := filepath.Join(t.TempDir(), convertDBName)
				write(dbFile)

				copied := filepath.Join(t.TempDir(), convertDBName)
				copyTestFile(dbFile, copied)

				want, err := compactBoltDBFile(copied)
				So(err, ShouldBeNil)

				stats, err := CompactDBFileStats(dbFile)
				So(err, ShouldBeNil)
				So(stats, ShouldResemble, want)
				So(stats.AfterSize, ShouldBeGreaterThan, 0)

				isBolt, err := isBoltFile(dbFile)
				So(err, ShouldBeNil)
				So(isBolt, ShouldBeTrue)
				So(besideDBFile(dbFile), ShouldResemble, []string{convertDBName, convertLockName})
			})
		}
	})
}

// storeFileSizes returns the size of every file under dir, keyed by its path
// relative to dir.
func storeFileSizes(dir string) map[string]int64 {
	sizes := make(map[string]int64)

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		sizes[rel] = info.Size()

		return err
	})
	So(err, ShouldBeNil)

	return sizes
}
