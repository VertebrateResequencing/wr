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
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestOwnMemoryMB(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("ownMemoryMB reports this process's own memory without error", t, func() {
		mb, err := ownMemoryMB()
		So(err, ShouldBeNil)
		So(mb, ShouldBeGreaterThanOrEqualTo, 0)

		Convey("and it never meaningfully exceeds currentMemory, which also includes children", func() {
			// currentMemory(self) reads the same smaps Pss and then adds the
			// memory of any child processes, so the own-only figure should never
			// be larger. The two figures sample /proc at slightly different
			// instants and both truncate to whole MB, though, so if Pss drops by a
			// sub-MB amount between the reads (eg. GC returning pages) ownMemoryMB()
			// can come out 1MB higher purely from truncation. We therefore allow a
			// 1MB tolerance: the ordering invariant is still validated, without
			// flaking on that boundary effect.
			withChildren, errc := currentMemory(os.Getpid())
			So(errc, ShouldBeNil)
			So(mb, ShouldBeLessThanOrEqualTo, withChildren+1)
		})
	})
}

func TestRmEmptyDirs(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a base dir with a nested leaf dir inside it", t, func() {
		outer := t.TempDir()
		base := filepath.Join(outer, "base")
		aDir := filepath.Join(base, "wr_cwd", "a")
		leaf := filepath.Join(aDir, "b", "unique")
		err := os.MkdirAll(leaf, os.ModePerm)
		So(err, ShouldBeNil)

		Convey("rmEmptyDirs deletes the leaf and its empty parents, but not baseDir", func() {
			So(rmEmptyDirs(leaf, base), ShouldBeNil)

			_, err = os.Stat(filepath.Join(base, "wr_cwd"))
			So(err, ShouldNotBeNil)
			soPathsExist(base, outer)
		})

		Convey("rmEmptyDirs stops at the first non-empty parent", func() {
			err = os.WriteFile(filepath.Join(aDir, "output.txt"), []byte("kept\n"), 0o600)
			So(err, ShouldBeNil)

			So(rmEmptyDirs(leaf, base), ShouldBeNil)

			_, err = os.Stat(filepath.Join(aDir, "b"))
			So(err, ShouldNotBeNil)
			soPathsExist(aDir, base, outer)
		})

		Convey("rmEmptyDirs treats an unclean baseDir as the same dir, so still stops at it", func() {
			So(rmEmptyDirs(leaf, base+string(filepath.Separator)), ShouldBeNil)

			_, err = os.Stat(filepath.Join(base, "wr_cwd"))
			So(err, ShouldNotBeNil)
			soPathsExist(base, outer)
		})

		Convey("rmEmptyDirs refuses a leafDir that is baseDir, so can't walk above it", func() {
			err = rmEmptyDirs(base, base)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(leaf, base, outer)
		})

		Convey("rmEmptyDirs refuses a leafDir that is not below baseDir", func() {
			other := filepath.Join(outer, "other")
			otherLeaf := filepath.Join(other, "leaf")
			err = os.MkdirAll(otherLeaf, os.ModePerm)
			So(err, ShouldBeNil)

			err = rmEmptyDirs(otherLeaf, base)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(otherLeaf, other, base, outer)
		})

		Convey("rmEmptyDirs refuses a leafDir that only looks below baseDir before symlinks resolve", func() {
			err = os.Symlink(outer, filepath.Join(base, "escape"))
			So(err, ShouldBeNil)

			err = rmEmptyDirs(filepath.Join(base, "escape", "base"), base)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(leaf, base, outer)
		})

		Convey("rmEmptyDirs leaves an empty dir outside baseDir alone, even reached via a symlink", func() {
			// an empty dir is the dangerous case: the upward walk only stops
			// deleting when it hits a dir it cannot remove, so if the
			// containment guard ever stopped working, this dir (and the
			// symlink leading to it) would actually be deleted, rather than
			// the walk merely failing on a non-empty dir outside baseDir.
			outsideEmpty := filepath.Join(outer, "empty")
			err = os.Mkdir(outsideEmpty, os.ModePerm)
			So(err, ShouldBeNil)

			escape := filepath.Join(base, "escape")
			err = os.Symlink(outer, escape)
			So(err, ShouldBeNil)

			err = rmEmptyDirs(filepath.Join(escape, "empty"), base)

			// survival is asserted before the error, so that a broken guard
			// shows up as the deletion it is, not as a missing error value.
			soPathsExist(outsideEmpty, escape, leaf, base, outer)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})
	})
}

func TestLiveTailSaver(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A live tail saver flushes a compressed recent tail", t, func() {
		saver := &liveTailSaver{}

		n, err := saver.Write([]byte("one\n"))
		So(err, ShouldBeNil)
		So(n, ShouldEqual, len("one\n"))

		compressed := saver.FlushCompressed()
		So(compressed, ShouldNotBeNil)
		So(len(compressed), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)
		So(decompressLiveTail(compressed), ShouldResemble, []byte("one\n"))
	})

	Convey("A live tail saver returns nil when flushed twice without more writes", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("one\n"))
		So(err, ShouldBeNil)
		So(saver.FlushCompressed(), ShouldNotBeNil)
		So(saver.FlushCompressed(), ShouldBeNil)
	})

	Convey("A live tail saver bounds incompressible output to a compressed suffix", t, func() {
		written := deterministicLiveBytes(liveStdRawTailLimit)
		saver := &liveTailSaver{}

		n, err := saver.Write(written)
		So(err, ShouldBeNil)
		So(n, ShouldEqual, len(written))

		compressed := saver.FlushCompressed()
		So(compressed, ShouldNotBeNil)
		So(len(compressed), ShouldBeLessThanOrEqualTo, liveStdCompressedLimit)

		decompressed := decompressLiveTail(compressed)
		So(decompressed, ShouldNotBeEmpty)
		So(bytes.HasSuffix(written, decompressed), ShouldBeTrue)
	})

	Convey("A live tail saver keeps the newest marker and drops old output", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("UNIQUE-PREFIX\n"))
		So(err, ShouldBeNil)
		_, err = saver.Write(deterministicLiveBytes(2 * liveStdRawTailLimit))
		So(err, ShouldBeNil)
		_, err = saver.Write([]byte("UNIQUE-SUFFIX\n"))
		So(err, ShouldBeNil)

		decompressed := decompressLiveTail(saver.FlushCompressed())
		So(string(decompressed), ShouldContainSubstring, "UNIQUE-SUFFIX\n")
		So(string(decompressed), ShouldNotContainSubstring, "UNIQUE-PREFIX\n")
	})

	Convey("A live tail saver resets after each flush", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("old\n"))
		So(err, ShouldBeNil)
		So(saver.FlushCompressed(), ShouldNotBeNil)

		_, err = saver.Write([]byte("new\n"))
		So(err, ShouldBeNil)
		So(decompressLiveTail(saver.FlushCompressed()), ShouldResemble, []byte("new\n"))
	})

	Convey("A live tail saver lets writes continue while flushing compressed output", t, func() {
		saver := &liveTailSaver{}

		_, err := saver.Write([]byte("old\n"))
		So(err, ShouldBeNil)

		started := make(chan struct{})
		release := make(chan struct{})
		originalCompressor := liveTailCompressor

		liveTailCompressor = func(tail []byte) []byte {
			close(started)
			<-release

			return originalCompressor(tail)
		}
		defer func() {
			liveTailCompressor = originalCompressor
		}()

		flushed := make(chan []byte, 1)
		go func() {
			flushed <- saver.FlushCompressed()
		}()

		<-started

		writeDone := make(chan error, 1)

		go func() {
			_, writeErr := saver.Write([]byte("new\n"))
			writeDone <- writeErr
		}()

		writeCompleted := false

		select {
		case writeErr := <-writeDone:
			So(writeErr, ShouldBeNil)

			writeCompleted = true
		case <-time.After(200 * time.Millisecond):
		}

		close(release)
		So(writeCompleted, ShouldBeTrue)

		flushedCompressed := <-flushed
		liveTailCompressor = originalCompressor

		So(decompressLiveTail(flushedCompressed), ShouldResemble, []byte("old\n"))
		So(decompressLiveTail(saver.FlushCompressed()), ShouldResemble, []byte("new\n"))
	})
}

func decompressLiveTail(compressed []byte) []byte {
	decompressed, err := decompress(compressed)
	So(err, ShouldBeNil)

	return decompressed
}

//nolint:gosec // deterministic test data must be reproducible.
func deterministicLiveBytes(size int) []byte {
	r := rand.New(rand.NewSource(1))

	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Intn(256))
	}

	return data
}
