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

package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestClearLive(t *testing.T) {
	Convey("Given a bbolt file with 2 jobslive keys", t, func() {
		path := filepath.Join(t.TempDir(), "db")
		db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 10 * time.Second})
		So(err, ShouldBeNil)

		putKeys := func(bucket []byte, n int) {
			errp := db.Update(func(tx *bolt.Tx) error {
				b, errc := tx.CreateBucketIfNotExists(bucket)
				if errc != nil {
					return errc
				}

				for i := range n {
					if errc = b.Put(fmt.Appendf(nil, "key%d", i), []byte("v")); errc != nil {
						return errc
					}
				}

				return nil
			})
			So(errp, ShouldBeNil)
		}

		countKeys := func(bucket []byte) (n int, exists bool) {
			rdb, erro := bolt.Open(path, 0600, &bolt.Options{ReadOnly: true, Timeout: 10 * time.Second})
			So(erro, ShouldBeNil)

			defer rdb.Close()

			errv := rdb.View(func(tx *bolt.Tx) error {
				b := tx.Bucket(bucket)
				if b == nil {
					return nil
				}

				exists = true

				return b.ForEach(func(_, _ []byte) error {
					n++

					return nil
				})
			})
			So(errv, ShouldBeNil)

			return n, exists
		}

		putKeys(bucketJobsLive, 2)

		Convey("and 3 jobRunState keys, clearLive empties both buckets and reports it", func() {
			putKeys(bucketJobRunState, 3)
			So(db.Close(), ShouldBeNil)

			var out bytes.Buffer
			So(clearLive(path, &out), ShouldBeNil)
			So(out.String(), ShouldContainSubstring, "jobslive keys: before=2 after=0")
			So(out.String(), ShouldContainSubstring, "jobRunState keys: before=3 after=0")

			n, exists := countKeys(bucketJobsLive)
			So(exists, ShouldBeTrue)
			So(n, ShouldEqual, 0)

			n, exists = countKeys(bucketJobRunState)
			So(exists, ShouldBeTrue)
			So(n, ShouldEqual, 0)
		})

		Convey("and no jobRunState bucket, clearLive succeeds without a jobRunState line", func() {
			So(db.Close(), ShouldBeNil)

			var out bytes.Buffer
			So(clearLive(path, &out), ShouldBeNil)
			So(out.String(), ShouldContainSubstring, "jobslive keys: before=2 after=0")
			So(out.String(), ShouldNotContainSubstring, "jobRunState")

			n, _ := countKeys(bucketJobsLive)
			So(n, ShouldEqual, 0)
		})
	})
}
