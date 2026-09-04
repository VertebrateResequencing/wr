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
	"sync/atomic"
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
// testJobKey stands in for a Job's Key(): 32 hex characters, which is what
// byteKey produces and what calculateHashedDir and relIsJobCreatedCwd expect.
const testJobKey = "0123456789abcdef0123456789abcdef"

func TestCreatedCwdDepthMatchesMkHashedDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The working dir mkHashedDir creates is createdCwdDepth below the base", t, func() {
		base := t.TempDir()

		actualCwd, _, err := mkHashedDir(base, testJobKey)
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

// mkHashedDirSweepTries is how many workspaces TestMkHashedDirDuringSweep makes
// while a sweep runs against the hashed dir it makes them in. The window it is
// trying to land in is the microseconds between one try making that dir and
// putting the workspace into it, so the trigger is looped rather than timed.
//
// The count is what the two ways of losing the race were measured to need: a
// mkHashedDir that does not retry the workspace at all fails ten runs in ten,
// by tens of workspaces, while one that retries without pausing between tries
// fails seven runs in ten, by one workspace in six of those runs and two in the
// seventh.
//
// Raising the count does not strengthen the guard: at 60000 tries the no-pause
// mutation fails only four runs in ten, worse than 20000's seven, because the
// failures cluster by run phase rather than by iteration count. That is why the
// count is 20000.
const mkHashedDirSweepTries = 20000

// TestMkHashedDirDuringSweep pins that a Job still gets a working directory when
// another Job's cleanup is sweeping the hashed directory the two share. The
// sweep is an upward walk of empty dirs, so it can take the hashed dir
// mkHashedDir has just made and has not yet put a workspace into.
func TestMkHashedDirDuringSweep(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("mkHashedDir makes a workspace while a sweep runs against the dir it makes it in", t, func() {
		base := t.TempDir()
		key := testJobKey
		cwdBase := filepath.Join(base, AppName+createdCwdBaseSuffix)
		hashed, _ := calculateHashedDir(cwdBase, key)

		// the hashed dirs of a third Job, differing from ours only at the level
		// below the one we share, which is the ordinary state of a Cwd that jobs
		// have run in and stops the sweep walking above the dir the two of us
		// share. The levels above that are not what mkHashedDir races for: they
		// are MkdirAll's own to make and its own budget covers them, and how many
		// of them one sweep can take at once is a separate matter this test would
		// only obscure.
		thirdHashed, _ := calculateHashedDir(cwdBase, "01f3456789abcdef0123456789abcdef")
		So(os.MkdirAll(thirdHashed, os.ModePerm), ShouldBeNil)

		root := rootOf(base)
		defer root.Close()

		// the sweep walks up from the workspace of another Job whose key hashes to
		// the same dir as ours, so it takes that dir whenever it is empty. It goes
		// through the production entry point with a handle on the Job's Cwd,
		// exactly as jobWorkSpace.rmEmptyMountDirs does.
		tick, stop := sweepEmptyDirsPerTick(root, filepath.Join(hashed, "otherWorkSpace"), hashed)

		failed, missing := 0, 0

		for range mkHashedDirSweepTries {
			tick()

			cwd, tmpDir, err := mkHashedDir(base, key)
			if err != nil {
				failed++

				continue
			}

			if !allDirsExist(cwd, tmpDir) {
				missing++
			}

			os.RemoveAll(filepath.Dir(cwd))
		}

		So(failed, ShouldEqual, 0)
		So(missing, ShouldEqual, 0)

		// the sweep removed the shared hashed dir at least once, so the loop
		// above ran against a live sweep and not an idle one. That is all this
		// proves: it does not show that a removal landed in the window between
		// MkdirAll and MkdirTemp, which is what losing the race takes. The
		// mutation set recorded in .docs/bugfixes/260904-2.md is what proves
		// the guard is live.
		So(stop(), ShouldBeGreaterThan, 0)
	})
}

// sweepEmptyDirsPerTick runs one rmEmptyDirsIn on leafDir per call of the
// returned tick func, in a goroutine, so that each sweep races one operation of
// the caller's rather than hammering it: a Job's cleanup sweeps once when the Job
// exits, so a spinning sweep would say nothing about production and everything
// about how many retries wr happens to have.
//
// stop waits for the goroutine and reports how many of those sweeps left
// removedDir gone, ie. how many really removed the dir they were walking up
// through.
//
// Sweep errors are not reported: a sweep racing with a dir being remade sees the
// level it proved replaced by a new inode, and refusing that is the sweep working
// as intended. What is under test is what mkHashedDir does.
func sweepEmptyDirsPerTick(root *os.Root, leafDir, removedDir string) (tick func(), stop func() int64) {
	var removals atomic.Int64

	ticks, done := make(chan struct{}), make(chan struct{})

	go func() {
		defer close(done)

		for range ticks {
			_ = rmEmptyDirsIn(root, leafDir) //nolint:errcheck // the sweep racing us is the point; its own outcome is irrelevant

			if _, err := os.Stat(removedDir); os.IsNotExist(err) {
				removals.Add(1)
			}
		}
	}()

	return func() {
			ticks <- struct{}{}
		}, func() int64 {
			close(ticks)
			<-done

			return removals.Load()
		}
}

// allDirsExist reports whether every one of the given paths is a directory that
// exists.
func allDirsExist(paths ...string) bool {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return false
		}
	}

	return true
}

// TestMkHashedDirUnwritableHashedDir covers what mkHashedDir does when it can
// make the hashed directories but cannot create the workspace inside them. A
// full filesystem, an exceeded quota, a read-only remount or an unwritable
// hashed level all do that, and every one of them is permanent: the job must be
// buried with the error rather than the runner spinning on it for ever, holding
// its scheduler slot.
func TestMkHashedDirUnwritableHashedDir(t *testing.T) {
	if runnermode || servermode {
		return
	}

	key := testJobKey

	Convey("mkHashedDir gives up when it cannot create anything in the hashed dirs", t, func() {
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

	// TestMkHashedDirDuringSweep makes this claim against the real sweep, but only
	// for the interleavings the machine it runs on happens to produce. This states
	// it deterministically, by driving the tries one at a time: the arrangement
	// that makes the failure transient is cleared BETWEEN two of them, which
	// nothing outside a loop that runs its tries back to back could do in time to
	// be seen.
	Convey("A failure that clears is retried, not treated as fatal", t, func() {
		dir := filepath.Join(t.TempDir(), "hashed")
		So(os.MkdirAll(dir, 0o700), ShouldBeNil)
		So(os.Chmod(dir, 0o500), ShouldBeNil)

		mkdirTries, leafTries := 0, 0
		unique, retry, err := tryMkUniqueDir(dir, "leaf", &mkdirTries, &leafTries)
		So(retry, ShouldBeTrue)
		So(err, ShouldBeNil)
		So(unique, ShouldBeBlank)

		So(os.Chmod(dir, 0o700), ShouldBeNil)

		unique, retry, err = tryMkUniqueDir(dir, "leaf", &mkdirTries, &leafTries)
		So(retry, ShouldBeFalse)
		So(err, ShouldBeNil)
		So(allDirsExist(unique), ShouldBeTrue)
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

	key := testJobKey

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
			hookErr = mkdirAllManaged(path, workSpaceNamePerm)
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

			err := mkdirAllManaged(path, workSpaceNamePerm)
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
			err := os.RemoveAll(filepath.Clean(filepath.Dir(path)))
			if err != nil && hookErr == nil {
				hookErr = err
			}
		}

		Reset(func() { workSpaceMintedHook = nil })

		returned, err := mkHashedDirWithin(30*time.Second, base, key)
		So(returned, ShouldBeTrue)
		So(hookErr, ShouldBeNil)

		// the taken-name budget does not retry this: an ENOENT is not EEXIST, so
		// mkWorkSpace returns it at once rather than drawing another name.
		// mkUniqueDir's own budget one level up DOES retry it, and rightly - a
		// hashed dir that has gone from under us is the concurrent rmEmptyDirsIn
		// that retry exists for. So the count is mkHashedDirMaxTries+1.
		So(calls, ShouldEqual, mkHashedDirMaxTries+1)
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
