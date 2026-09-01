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

// This file holds a benchmark for the default on_exit cleanup behaviour, which
// almost every job carries and which therefore runs once per job. Like the
// benchmarks in db_bench_test.go it is a plain testing.B benchmark, so only
// `go test -bench` (ie. `make bench`) runs it, never `make test`.

import (
	"fmt"
	"os"
	"path/filepath"
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

	actualCwd, tmpDir, err := mkHashedDir(cwd, job.Key())
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
