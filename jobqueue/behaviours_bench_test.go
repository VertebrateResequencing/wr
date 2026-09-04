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

// This file holds benchmarks for the default on_exit cleanup behaviour, which
// almost every job carries and which therefore runs once per job. Like the
// benchmarks in db_bench_test.go they are plain testing.B benchmarks, so only
// `go test -bench` (ie. `make bench`) runs them, never `make test`.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// BenchmarkJobCleanup measures the per-job cost of the default on_exit cleanup
// behaviour for a normal (non-cwd_matters) job: wr made the hashed workspace,
// the cmd wrote some output files into it, then cleanup wipes the workspace and
// tidies the empty parent dirs up to Cwd. Setup is excluded from the timer.
//
// The cost of this path is dominated by path metadata operations, and jobs
// typically run on a shared filesystem (Lustre) where those are orders of
// magnitude dearer than the local disk this benchmark uses, over batches of tens
// of thousands of jobs. So the number of syscalls per cleanup matters far more
// than this ns/op figure suggests: the containment guard that stops cleanup
// deleting anything outside the job's own workspace must stay O(depth of the
// workspace below Cwd), and must not grow with the depth of Cwd itself.
func BenchmarkJobCleanup(b *testing.B) {
	cwd := b.TempDir()
	behaviour := &Behaviour{When: OnExit, Do: Cleanup}

	for i := range b.N {
		b.StopTimer()
		job := benchCleanupJob(b, cwd, i)
		b.StartTimer()

		if err := behaviour.Trigger(OnExit, job); err != nil {
			b.Fatal(err)
		}
	}
}

// benchCleanupJob creates the hashed working dir that wr would have made for a
// job, fills it with output files, and returns the Job that cleanup will be
// triggered on.
func benchCleanupJob(tb testing.TB, cwd string, i int) *Job {
	tb.Helper()

	// the Job is built FIRST because mkHashedDir must hash the Job's own
	// Key(): cleanup rebuilds the path it will accept from that key (see
	// relIsJobCreatedCwd) and refuses every other, so a fixture that hashes
	// anything else measures the refusal instead of the cleanup. Key() is built
	// from Cmd, Cwd (when CwdMatters), the mounts and the container image, so
	// every field feeding it has to be set before the key is taken.
	job := &Job{Cwd: cwd, Cmd: fmt.Sprintf("echo bench.cleanup.%d", i)}

	actualCwd, tmpDir, wsToken, err := mkHashedDir(cwd, job.Key())
	if err != nil {
		tb.Fatal(err)
	}

	files := []string{
		filepath.Join(actualCwd, "out.txt"),
		filepath.Join(actualCwd, "err.txt"),
		filepath.Join(actualCwd, "result.rds"),
		filepath.Join(tmpDir, "scratch"),
	}

	for _, file := range files {
		if err = os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
	}

	job.ActualCwd = actualCwd
	job.ActualCwdToken = wsToken

	return job
}

// TestBenchCleanupJobIsCleanable pins that BenchmarkJobCleanup's own fixture
// yields a Job whose cleanup actually SUCCEEDS, so the benchmark measures the
// cleanup path it exists to measure rather than an early refusal.
//
// mkHashedDir lays the workspace down from a string, and relIsJobCreatedCwd
// (via createdCwdRel) rebuilds the expected path from the Job's OWN Key(). If
// the fixture hashes anything other than that key, cleanup refuses the path
// with errNotACreatedCwd having deleted nothing, and the benchmark's own
// b.Fatal then turns that into a bare `--- FAIL: BenchmarkJobCleanup` with no
// measurement at all. A benchmark cannot be its own guard, though: `make test`
// never runs one, so that b.Fatal fires only for whoever runs `make bench`.
func TestBenchCleanupJobIsCleanable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("BenchmarkJobCleanup's fixture yields a job that cleanup can clean", t, func() {
		cwd := t.TempDir()
		job := benchCleanupJob(t, cwd, 0)

		err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)
		So(err, ShouldBeNil)

		Convey("and the working directory it made is gone afterwards", func() {
			_, err = os.Stat(job.ActualCwd)
			So(os.IsNotExist(err), ShouldBeTrue)
		})
	})
}

// BenchmarkJobCleanupDepth1 and BenchmarkJobCleanupDepth8 are a PAIR, and the
// signal is the ratio between them, not either figure on its own. They sweep
// the SAME 200 files, once at depth 1 and once at depth 8 below the job's
// working directory. A sweep that costs the same per entry wherever the entry
// sits therefore reads Depth8 at about Depth1, while a ratio near 2 means it is
// re-resolving each entry's whole path from the swept root again, once per
// entry. This is the depth half of the invariant BenchmarkJobCleanup states and
// cannot itself measure, so see that benchmark for why the syscalls behind
// these figures matter more than the figures do.
//
// allocs/op is the deterministic half of the verdict, because each re-resolved
// path component allocates; ns/op corroborates it. Reference figures on the
// fixed tree, at -benchtime=50x:
//
//	Depth1   2,848,995 ns/op   2,190 allocs/op
//	Depth8   3,048,264 ns/op   2,372 allocs/op    ratio 1.07x
//
// and the same pair before the fix, when the sweep named every entry by its
// accumulated path against one os.Root:
//
//	Depth1   4,238,179 ns/op   3,772 allocs/op
//	Depth8   8,604,540 ns/op   7,663 allocs/op    ratio 2.03x
func BenchmarkJobCleanupDepth1(b *testing.B) { benchJobCleanupDepth(b, 1, 200) }
func BenchmarkJobCleanupDepth8(b *testing.B) { benchJobCleanupDepth(b, 8, 200) }

// benchJobCleanupDepth triggers the default on_exit cleanup on nFiles files
// buried depth directories below the job's working directory, with the setup
// excluded from the timer as in BenchmarkJobCleanup.
func benchJobCleanupDepth(b *testing.B, depth, nFiles int) {
	b.Helper()

	cwd := b.TempDir()
	behaviour := &Behaviour{When: OnExit, Do: Cleanup}

	for i := range b.N {
		b.StopTimer()
		job := benchDepthJob(b, cwd, i, depth, nFiles)
		b.StartTimer()

		if err := behaviour.Trigger(OnExit, job); err != nil {
			b.Fatal(err)
		}
	}
}

// benchDepthJob creates the hashed working dir that wr would have made for a
// job, then puts nFiles output files depth directories below it, and returns
// the Job that cleanup will be triggered on. Holding nFiles fixed while depth
// varies is what lets the pair separate the per-entry cost of naming an entry
// from the cost of deleting it.
//
// Like benchCleanupJob it builds the Job before hashing, and hashes the Job's
// own Key(), for the reason given there.
func benchDepthJob(tb testing.TB, cwd string, i, depth, nFiles int) *Job {
	tb.Helper()

	job := &Job{Cwd: cwd, Cmd: fmt.Sprintf("echo bench.depth.%d", i)}

	actualCwd, _, err := mkHashedDir(cwd, job.Key())
	if err != nil {
		tb.Fatal(err)
	}

	parts := make([]string, depth)
	for d := range depth {
		parts[d] = "n" + strconv.Itoa(d)
	}

	leaf := filepath.Join(actualCwd, strings.Join(parts, string(filepath.Separator)))
	if err = os.MkdirAll(leaf, 0o700); err != nil {
		tb.Fatal(err)
	}

	for f := range nFiles {
		if err = os.WriteFile(filepath.Join(leaf, "f"+strconv.Itoa(f)), []byte("x\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
	}

	job.ActualCwd = actualCwd

	return job
}

// TestBenchDepthJobIsCleanable does for benchDepthJob what
// TestBenchCleanupJobIsCleanable does for benchCleanupJob, and for the reason
// given there: a benchmark cannot be its own guard, so a depth fixture that
// stopped yielding a cleanable Job would quietly measure an early refusal for
// everyone except whoever next runs `make bench`. It is a test of its own
// rather than another Convey block in that one so that a failure names the
// fixture that broke.
func TestBenchDepthJobIsCleanable(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("The depth benchmarks' fixture yields a job that cleanup can clean", t, func() {
		cwd := t.TempDir()
		job := benchDepthJob(t, cwd, 0, 8, 2)

		err := (&Behaviour{When: OnExit, Do: Cleanup}).Trigger(OnExit, job)
		So(err, ShouldBeNil)

		Convey("and the nested dirs it made are gone afterwards", func() {
			_, err = os.Stat(job.ActualCwd)
			So(os.IsNotExist(err), ShouldBeTrue)
		})
	})
}
