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
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

func TestStoreTarWithBoltAtPageOffsetIsNotBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a self-contained backup tar whose history.bolt member's content starts at offset 4096, "+
		"where a bbolt file's second meta page of 4096-byte pages is", t, func() {
		dir := t.TempDir()
		boltPath := filepath.Join(dir, historyBoltFile)

		db, err := bolt.Open(boltPath, dbFilePermission, &bolt.Options{PageSize: 4096})
		So(err, ShouldBeNil)
		So(db.Close(), ShouldBeNil)

		history, err := os.ReadFile(boltPath)
		So(err, ShouldBeNil)

		tarPath := filepath.Join(dir, "backup")
		f, err := os.Create(tarPath)
		So(err, ShouldBeNil)

		// Headers of 512 bytes: VERSION's at 0 with 512 bytes of content, a
		// checkpoint's at 1024 with 2048, then history.bolt's at 3584, so its
		// content starts at 4096.
		tw := tar.NewWriter(f)

		for _, member := range []struct {
			name string
			data []byte
		}{
			{storeVersionFile, make([]byte, 512)},
			{"00000002" + checkpointFileExt, make([]byte, 2048)},
			{historyBoltFile, history},
		} {
			So(tw.WriteHeader(backupTarHeader(member.name, int64(len(member.data)))), ShouldBeNil)
			_, err = tw.Write(member.data)
			So(err, ShouldBeNil)
		}

		So(tw.Close(), ShouldBeNil)
		So(f.Close(), ShouldBeNil)

		got := make([]byte, 4)
		r, err := os.Open(tarPath)
		So(err, ShouldBeNil)
		_, err = r.ReadAt(got, 4096+boltMetaMagicOffset)
		So(err, ShouldBeNil)
		So(r.Close(), ShouldBeNil)
		So(got, ShouldResemble, history[boltMetaMagicOffset:boltMetaMagicOffset+4])

		Convey("it is a tar and not a bbolt file", func() {
			isTar, errt := hasTarMagic(tarPath)
			So(errt, ShouldBeNil)
			So(isTar, ShouldBeTrue)

			isBolt, errb := hasBoltMagic(tarPath)
			So(errb, ShouldBeNil)
			So(isBolt, ShouldBeFalse)
		})
	})
}
