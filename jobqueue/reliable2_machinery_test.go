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
	"os"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReliable2RecoveryMachineryRetained covers spec E6 acceptance test 3. E6's
// decision is to KEEP the recovery-window RPC machinery as defence in depth even
// though spec E1 makes the server-request pathway unreachable in production, for
// three recorded reasons: the scheduling gate is still load-bearing (
// buildSchedulerGroups still runs during the window and no bsub must happen
// before serving begins), the sub-millisecond publish-before-finishRecovering
// window keeps the getij branches reachable, and .docs/reliable2/spec.md H2 is a
// binding written contract.
//
// This is a live grep of the tree rather than a behavioural assertion because
// what it guards against is a future reader deleting all three as dead code; the
// behaviour itself is covered by TestReliable2RecoveryWindowReturnsRecovering
// and TestReliable2RecoveryRestoresIncompleteJobs in reliable2_dbcompat_test.go.
// It is not in that file because it does not need bbolt, so it outlives it.
func TestReliable2RecoveryMachineryRetained(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The retained recovery-window machinery is still present and referenced", t, func() {
		// the sentinel itself, getij's recovering branch, getijForReport's, and the
		// runner-dispatch gate. The sentinel's name and value are matched as two
		// substrings of one line rather than as the single gofmt-aligned string
		// they appear as, so that adding a longer name to that const block
		// re-aligns it without failing this test with a misleading "the machinery
		// was deleted".
		So(dbcompatGrepCount(t, "server.go", "ErrRecovering", `= "server is recovering`), ShouldEqual, 1)
		So(dbcompatGrepCount(t, "serverCLI.go", "return item, nil, ErrRecovering"), ShouldEqual, 1)
		// getijForReport's branch returns no item or job; this counts it with
		// getij's.
		So(dbcompatGrepCount(t, "serverCLI.go", "nil, ErrRecovering"), ShouldEqual, 2)
		So(dbcompatGrepCount(t, "server.go", "!s.isRecovering()"), ShouldBeGreaterThanOrEqualTo, 1)
	})
}

// dbcompatGrepCount counts the lines of the named package source file that
// contain every one of substrs.
func dbcompatGrepCount(t *testing.T, file string, substrs ...string) int {
	t.Helper()

	source, err := os.ReadFile(file)
	So(err, ShouldBeNil)

	count := 0

	for line := range strings.SplitSeq(string(source), "\n") {
		if lineContainsAll(line, substrs) {
			count++
		}
	}

	return count
}

// lineContainsAll reports whether line contains every one of substrs.
func lineContainsAll(line string, substrs []string) bool {
	for _, substr := range substrs {
		if !strings.Contains(line, substr) {
			return false
		}
	}

	return true
}
