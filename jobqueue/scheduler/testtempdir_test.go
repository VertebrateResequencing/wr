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

package scheduler

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// pristineEnv is the environment this test binary was started with, captured
// before any test can alter it. The child TestTestBinaryTempDirs runs gets this
// rather than os.Environ(), because the claim being made is about a real
// `go test` run of this package, and a real run starts from the environment the
// binary was given, not from one an earlier test left behind.
//
//nolint:gochecknoglobals // read once at startup, so that no test can taint it.
var pristineEnv = os.Environ()

// childTestTimeout bounds the child run, which takes a few seconds, so a hung
// test fails here rather than waiting for the parent's own deadline.
const childTestTimeout = "5m"

// TestTestBinaryTempDirs proves that a passing run of the local scheduler tests
// leaves nothing behind in the shared temp dir. It runs this test binary again
// with TMPDIR pointing at a directory of its own, so what the child creates
// there is exactly what a real run would have added to /tmp, and checks that
// directory is empty once the child has exited.
//
// An empty TMPDIR is also what a child that ran nothing leaves, and a -test.run
// pattern naming a test that no longer exists runs nothing and still exits 0.
// So the child runs verbosely and each named test must be seen to pass; neither
// can legitimately skip, as neither calls t.Skip.
func TestTestBinaryTempDirs(t *testing.T) {
	Convey("A passing run of the local scheduler tests leaves nothing in TMPDIR", t, func() {
		tmpdir := t.TempDir()

		names := localTempDirTests()

		cmd := exec.CommandContext(t.Context(), os.Args[0], //nolint:gosec
			"-test.run", "^("+strings.Join(names, "|")+")$",
			"-test.v", "-test.timeout", childTestTimeout)

		cmd.Env = append(slices.Clone(pristineEnv), "TMPDIR="+tmpdir)

		out, err := cmd.CombinedOutput()
		So(string(out), ShouldNotContainSubstring, "--- FAIL")
		So(err, ShouldBeNil)

		for _, name := range names {
			So(string(out), ShouldContainSubstring, "--- PASS: "+name+" ")
		}

		entries, err := os.ReadDir(tmpdir)
		So(err, ShouldBeNil)
		So(entries, ShouldBeEmpty)
	})
}

// localTempDirTests returns the names of the tests that create the temp dirs
// this package's local scheduler tests need. They are the ones that used to
// leak; the rest of the package needs credentials or an LSF cluster, so running
// them here would prove nothing and cost minutes. The child's -test.run pattern
// and the check that each one ran are both built from this one list, so they
// cannot drift apart.
func localTempDirTests() []string {
	return []string{"TestLocal", "TestStartOrderRecorder"}
}
