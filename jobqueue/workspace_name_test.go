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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestWorkSpaceNameIsMintedPerRun asserts the invariant the workspace name
// carries: each run of a key is given a freshly minted name, so a finished
// run's stored ActualCwd is overwhelmingly unlikely to name a LIVE run's
// workspace, and the finished run's cleanup therefore does not sweep through
// the live run's mounts and caches.
//
// It asserts at the boundary wr hands out - the paths mkHashedDir returns and the
// cleanup the snapshot performs on them - and it does NOT prove that a collision
// is impossible, because no sampling test can. os.MkdirTemp, which named these
// dirs before, already drew a fresh 32-bit suffix on every call: measured at 0
// reuses in 20000 create/remove/recreate rounds on go1.26.3, so the pre-fix code
// passes this test too. What the fix moves is the odds, from 2^-32 to 2^-122, and
// the two are indistinguishable by sampling.
//
// The guarantee is therefore structural rather than measured: it lives in the
// NAME SPACE the mint draws from, and this test is red only when the mint stops
// drawing a fresh name per run - which is the mutation it exists to catch. For
// anything that does collide, #575's and #577's sweep guards remain the defence
// in depth: nestedWorkSpaceBase keeps a nested job's own workspace out of the
// sweep, and sweptDir.sweepable stops the sweep at a device boundary, so a
// live mount inside a colliding workspace is still not deleted through.
func TestWorkSpaceNameIsMintedPerRun(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A workspace name a finished run had is not given to a later run of the same key", t, func() {
		cwd := t.TempDir()

		stale := &Job{Cwd: cwd, Cmd: "echo same"}
		staleCwd, staleWorkSpace, staleTmp := realWorkSpace(stale)
		snap := stale.workSpaceSnapshot()

		// the workspace goes THE WAY CLEANUP TAKES IT, so the name the next run is
		// offered is one production really did free.
		So(snap.cleanupWorkSpace(), ShouldBeNil)
		So(snap.removeTmpDir(), ShouldBeNil)
		soPathsGone(staleCwd, staleTmp, staleWorkSpace)

		live := &Job{Cwd: cwd, Cmd: "echo same"}
		So(live.Key(), ShouldEqual, stale.Key())

		liveCwd, liveWorkSpace, liveTmp := realWorkSpace(live)
		So(liveWorkSpace, ShouldNotEqual, staleWorkSpace)
		So(liveCwd, ShouldNotEqual, staleCwd)

		Convey("so the finished run's cleanup does not reach the live run's data", func() {
			// this is the loss the invariant exists to stop: the stale run's stored
			// ActualCwd naming the live run's workspace, cleanup sweeping through
			// it, and reporting nil.
			planted := writeFileIn(liveCwd, "REMOTE_DATA")

			So(snap.cleanupWorkSpace(), ShouldBeNil)
			So(snap.removeTmpDir(), ShouldBeNil)

			soPathsExist(planted, liveCwd, liveTmp, liveWorkSpace)
		})
	})
}
