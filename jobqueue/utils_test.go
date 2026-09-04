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
	"strings"
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

// rootOf opens dir the way every caller of rmEmptyDirsIn has already had to:
// as a handle that bounds the walk, so that a component swapped after the proof
// cannot redirect a deletion out of it.
func rootOf(dir string) *os.Root {
	root, err := openBaseRoot(dir)
	So(err, ShouldBeNil)

	return root
}

func TestRmEmptyDirsIn(t *testing.T) {
	if runnermode || servermode {
		return
	}

	// rmEmptyDirsIn is the upward walk Job.Unmount's tidy-up makes from a mount
	// point, and every deletion it makes is bounded by the handle on the Job's Cwd
	// that the caller has already proven its way to.
	Convey("Given a base dir with a nested leaf dir inside it", t, func() {
		outer := t.TempDir()
		base := filepath.Join(outer, "base")
		aDir := filepath.Join(base, "wr_cwd", "a")
		leaf := filepath.Join(aDir, "b", "unique")
		err := os.MkdirAll(leaf, os.ModePerm)
		So(err, ShouldBeNil)

		Convey("rmEmptyDirsIn deletes the leaf and its empty parents, but not baseDir", func() {
			So(rmEmptyDirsIn(rootOf(base), leaf), ShouldBeNil)

			_, err = os.Stat(filepath.Join(base, "wr_cwd"))
			So(err, ShouldNotBeNil)
			soPathsExist(base, outer)
		})

		Convey("rmEmptyDirsIn stops at the first non-empty parent", func() {
			err = os.WriteFile(filepath.Join(aDir, "output.txt"), []byte("kept\n"), 0o600)
			So(err, ShouldBeNil)

			So(rmEmptyDirsIn(rootOf(base), leaf), ShouldBeNil)

			_, err = os.Stat(filepath.Join(aDir, "b"))
			So(err, ShouldNotBeNil)
			soPathsExist(aDir, base, outer)
		})

		Convey("rmEmptyDirsIn keeps a non-empty leaf, without error", func() {
			err = os.WriteFile(filepath.Join(leaf, "output.txt"), []byte("kept\n"), 0o600)
			So(err, ShouldBeNil)

			So(rmEmptyDirsIn(rootOf(base), leaf), ShouldBeNil)

			soPathsExist(leaf, aDir, base, outer)

			Convey("because the OS reports that as an errno we recognise, not as a message", func() {
				err = os.Remove(leaf)
				So(err, ShouldNotBeNil)
				So(errIsDirNotEmpty(err), ShouldBeTrue)
			})
		})

		Convey("rmEmptyDirsIn treats an unclean baseDir as the same dir, so still stops at it", func() {
			So(rmEmptyDirsIn(rootOf(base+string(filepath.Separator)), leaf), ShouldBeNil)

			_, err = os.Stat(filepath.Join(base, "wr_cwd"))
			So(err, ShouldNotBeNil)
			soPathsExist(base, outer)
		})

		Convey("rmEmptyDirsIn refuses a leafDir that is baseDir, so can't walk above it", func() {
			err = rmEmptyDirsIn(rootOf(base), base)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(leaf, base, outer)
		})

		Convey("rmEmptyDirsIn refuses a leafDir that is not below baseDir", func() {
			other := filepath.Join(outer, "other")
			otherLeaf := filepath.Join(other, "leaf")
			err = os.MkdirAll(otherLeaf, os.ModePerm)
			So(err, ShouldBeNil)

			err = rmEmptyDirsIn(rootOf(base), otherLeaf)
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(otherLeaf, other, base, outer)
		})

		Convey("rmEmptyDirsIn refuses a leafDir that only looks below baseDir before symlinks resolve", func() {
			err = os.Symlink(outer, filepath.Join(base, "escape"))
			So(err, ShouldBeNil)

			err = rmEmptyDirsIn(rootOf(base), filepath.Join(base, "escape", "base"))
			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)

			soPathsExist(leaf, base, outer)
		})

		Convey("rmEmptyDirsIn leaves an empty dir inside baseDir alone when a symlink leads to it", func() {
			// the outside-baseDir case below is caught by the containment guard;
			// this one is inside baseDir, so containment says yes and only the
			// refusal to follow a symlink stops it. The Job's own Cmd can create
			// this link where wr expected the mount dir it made, and point it at an
			// empty directory of the user's, whose parent the walk would take too.
			userDir := filepath.Join(base, "userdata")
			userEmpty := filepath.Join(userDir, "results")
			err = os.MkdirAll(userEmpty, os.ModePerm)
			So(err, ShouldBeNil)

			link := filepath.Join(base, "mnt")
			err = os.Symlink(userEmpty, link)
			So(err, ShouldBeNil)

			err = rmEmptyDirsIn(rootOf(base), link)

			// survival first, so a broken guard shows up as the deletion it is.
			soPathsExist(userEmpty, userDir, link, base)

			So(err, ShouldNotBeNil)
			So(errors.Is(err, errNotBelowBaseDir), ShouldBeTrue)
		})

		Convey("rmEmptyDirsIn leaves an empty dir outside baseDir alone, even reached via a symlink", func() {
			// an empty dir is the dangerous case: the upward walk only stops
			// deleting when it hits a dir it cannot remove, so a broken containment
			// guard deletes this dir and the symlink leading to it, rather than
			// merely failing on a non-empty dir outside baseDir.
			outsideEmpty := filepath.Join(outer, "empty")
			err = os.Mkdir(outsideEmpty, os.ModePerm)
			So(err, ShouldBeNil)

			escape := filepath.Join(base, "escape")
			err = os.Symlink(outer, escape)
			So(err, ShouldBeNil)

			err = rmEmptyDirsIn(rootOf(base), filepath.Join(escape, "empty"))

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

// TestCreatedCwdDepthMatchesMkHashedDir pins createdCwdDepth against what
// mkHashedDir actually creates. Cleanup refuses to treat a directory at any
// other depth as a workspace, so if the two ever drift apart every cleanup
// would silently stop working rather than fail loudly. It calls the real
// mkHashedDir, so it also pins the workspace's mode, which is user-visible on a
// shared filesystem rather than an implementation detail.
func TestCreatedCwdDepthMatchesMkHashedDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The working dir mkHashedDir creates is createdCwdDepth below the base", t, func() {
		base := t.TempDir()

		actualCwd, _, err := mkHashedDir(base, "0123456789abcdef0123456789abcdef")
		So(err, ShouldBeNil)

		rel, err := filepath.Rel(base, actualCwd)
		So(err, ShouldBeNil)
		So(len(strings.Split(rel, string(filepath.Separator))), ShouldEqual, createdCwdDepth)
		So(filepath.Base(actualCwd), ShouldEqual, createdCwdName)

		Convey("and nobody but its owner can enter the workspace that dir sits in", func() {
			// the workspace holds the Job's output and its TMPDIR, so on a shared
			// filesystem any group or other bit here would let another user read
			// and write another user's job data. umask can only clear bits, never
			// set them, so what the mode has to say is that those bits are already
			// clear before any umask is applied.
			const groupAndOtherPerms = 0o077

			info, err := os.Stat(filepath.Dir(actualCwd))
			So(err, ShouldBeNil)
			So(info.Mode()&os.ModePerm&groupAndOtherPerms, ShouldEqual, os.FileMode(0))
		})
	})
}

// TestMkHashedDirHoldFileFailure covers what mkHashedDir does when it can make
// the hashed directories but not the hold file inside them. A full filesystem,
// an exceeded quota, a read-only remount or an unwritable hashed level all do
// that, and every one of them is permanent: the job must be buried with the
// error rather than the runner spinning on it for ever, holding its scheduler
// slot.
func TestMkHashedDirHoldFileFailure(t *testing.T) {
	if runnermode || servermode {
		return
	}

	key := "0123456789abcdef0123456789abcdef"

	Convey("mkHashedDir gives up when the hold file cannot be created", t, func() {
		base := t.TempDir()

		// a first call leaves the hashed levels behind, which is the normal state
		// after any job has run in this Cwd, and means the MkdirAll of every later
		// call succeeds.
		_, _, err := mkHashedDir(base, key)
		So(err, ShouldBeNil)

		hashed, _ := calculateHashedDir(filepath.Join(base, AppName+createdCwdBaseSuffix), key)
		So(os.Chmod(hashed, 0o500), ShouldBeNil)

		defer func() {
			So(os.Chmod(hashed, 0o700), ShouldBeNil)
		}()

		returned, err := mkHashedDirWithin(30*time.Second, base, key)
		So(returned, ShouldBeTrue)
		So(err, ShouldNotBeNil)
	})

	// the transient half is driven one attempt at a time rather than through
	// mkHashedDir, because the failure the retry exists for - a concurrent
	// rmEmptyDirsIn removing the directory between our MkdirAll and our hold
	// file - heals in the microseconds between two attempts of the loop, so
	// nothing outside the loop can repair the arrangement in time to be seen.
	Convey("A hold-file failure that clears is retried, not treated as fatal", t, func() {
		dir := filepath.Join(t.TempDir(), "hashed")
		So(os.MkdirAll(dir, 0o700), ShouldBeNil)

		holdFile := filepath.Join(dir, ".hold")
		So(os.Chmod(dir, 0o500), ShouldBeNil)

		mkdirTries, holdTries := 0, 0
		retry, err := tryMkHeldDir(dir, holdFile, &mkdirTries, &holdTries)
		So(retry, ShouldBeTrue)
		So(err, ShouldBeNil)

		So(os.Chmod(dir, 0o700), ShouldBeNil)

		retry, err = tryMkHeldDir(dir, holdFile, &mkdirTries, &holdTries)
		So(retry, ShouldBeFalse)
		So(err, ShouldBeNil)

		_, err = os.Stat(holdFile)
		So(err, ShouldBeNil)
	})
}

// TestMkHashedDirNameTaken covers what mkHashedDir does when the workspace name
// it minted is already taken on disk. os.MkdirTemp, which used to name the
// workspace, drew a fresh suffix and tried again whenever the name it drew
// existed, so a taken name never reached the caller; the UUID mint has to keep
// that, or a name-exists error - from a UUID repeat, or from anything else
// sitting at that exact path - buries the job with FailReasonCwd where it used
// to succeed.
//
// The name is driven through workSpaceMintedHook because a v4 UUID cannot be
// made to collide, and the tests call mkHashedDir itself rather than the retry,
// so what they pin is the directory a job actually gets.
func TestMkHashedDirNameTaken(t *testing.T) {
	if runnermode || servermode {
		return
	}

	key := "0123456789abcdef0123456789abcdef"

	Convey("mkHashedDir works around a workspace name that is already taken", t, func() {
		base := t.TempDir()

		var (
			taken   string
			hookErr error
			calls   int
		)

		workSpaceMintedHook = func(path string) {
			calls++
			if calls > 1 {
				return
			}

			taken = path
			hookErr = os.MkdirAll(path, workSpaceNamePerm) //nolint:gosec // test-controlled path under a temp dir
		}

		Reset(func() { workSpaceMintedHook = nil })

		cwd, tmpDir, err := mkHashedDir(base, key)

		So(hookErr, ShouldBeNil)

		cwdInfo, statErr := os.Stat(cwd)
		So(statErr, ShouldBeNil)
		So(cwdInfo.IsDir(), ShouldBeTrue)

		tmpInfo, statErr := os.Stat(tmpDir)
		So(statErr, ShouldBeNil)
		So(tmpInfo.IsDir(), ShouldBeTrue)

		// the workspace the job got must be a fresh sibling of the taken one, not
		// the taken one itself: reusing it would put this job's output in whatever
		// already owns that directory.
		workSpace := filepath.Dir(cwd)
		So(workSpace, ShouldNotEqual, taken)
		So(filepath.Dir(workSpace), ShouldEqual, filepath.Dir(taken))
		So(calls, ShouldEqual, 2)

		So(err, ShouldBeNil)
	})

	// hookTakeLimit is how many minted names the hook below takes before it
	// stops taking them. It is generously above mkHashedDirMaxTries so that
	// correctly bounded code never reaches it, while code that retries without a
	// bound - or that resets its counter mid-loop, which is the livelock #569
	// fixed in mkHeldDir - runs past it and then succeeds. That way this test
	// reports a verdict instead of hanging the suite or filling the disk.
	const hookTakeLimit = 20

	Convey("mkHashedDir gives up when every workspace name it mints is taken", t, func() {
		base := t.TempDir()

		var (
			hookErr error
			calls   int
		)

		workSpaceMintedHook = func(path string) {
			calls++
			if calls > hookTakeLimit {
				return
			}

			err := os.MkdirAll(path, workSpaceNamePerm) //nolint:gosec // test-controlled path under a temp dir
			if err != nil && hookErr == nil {
				hookErr = err
			}
		}

		Reset(func() { workSpaceMintedHook = nil })

		returned, err := mkHashedDirWithin(30*time.Second, base, key)
		So(returned, ShouldBeTrue)
		So(hookErr, ShouldBeNil)
		So(calls, ShouldBeLessThanOrEqualTo, hookTakeLimit)
		So(err, ShouldNotBeNil)
	})

	// the retry is only for a name that is taken. Every other reason a workspace
	// cannot be created - a full filesystem, an exceeded quota, a read-only
	// remount, a hashed level that has gone - is answered by drawing another
	// name identically, so retrying it only delays the job's burial.
	Convey("mkHashedDir returns a non-existence workspace failure without retrying", t, func() {
		base := t.TempDir()

		var (
			hookErr error
			calls   int
		)

		workSpaceMintedHook = func(path string) {
			calls++
			// removing the hashed dir the workspace was to go in makes the create
			// fail with ENOENT rather than EEXIST.
			err := os.RemoveAll(filepath.Dir(path)) //nolint:gosec // test-controlled path under a temp dir
			if err != nil && hookErr == nil {
				hookErr = err
			}
		}

		Reset(func() { workSpaceMintedHook = nil })

		returned, err := mkHashedDirWithin(30*time.Second, base, key)
		So(returned, ShouldBeTrue)
		So(hookErr, ShouldBeNil)
		So(calls, ShouldEqual, 1)
		So(err, ShouldNotBeNil)
		So(os.IsExist(err), ShouldBeFalse)
	})
}

// mkHashedDirWithin calls mkHashedDir in a goroutine and reports whether it
// returned within limit, so a livelock fails the test instead of hanging the
// suite for ever.
func mkHashedDirWithin(limit time.Duration, base, tohash string) (returned bool, err error) {
	errCh := make(chan error, 1)

	go func() {
		_, _, errm := mkHashedDir(base, tohash)
		errCh <- errm
	}()

	select {
	case errm := <-errCh:
		return true, errm
	case <-time.After(limit):
		return false, nil
	}
}
