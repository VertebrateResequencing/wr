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
)

// BenchmarkJobCleanup measures the per-job cost of the default on_exit cleanup
// behaviour for a normal (non-cwd_matters) job: wr made the hashed workspace,
// the cmd wrote some output files into it, then cleanup wipes the workspace and
// tidies the empty parent dirs up to Cwd. Setup is excluded from the timer.
//
// The point of guarding this path is that its cost is dominated by path
// metadata operations, and jobs typically run on a shared filesystem (Lustre)
// where those are orders of magnitude dearer than the local disk this
// benchmark uses, over batches of tens of thousands of jobs. So the number of
// syscalls per cleanup matters far more than this ns/op figure suggests: the
// containment guard that stops cleanup deleting anything outside the job's own
// workspace must stay O(depth of the workspace below Cwd), and must not grow
// with the depth of Cwd itself. A guard that re-resolves the whole path per
// parent dir level made this 69% slower with 6.4x the allocations, which was
// invisible locally and painful at scale.
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
func benchCleanupJob(b *testing.B, cwd string, i int) *Job {
	b.Helper()

	actualCwd, tmpDir, err := mkHashedDir(cwd, fmt.Sprintf("bench.cleanup.%d", i))
	if err != nil {
		b.Fatal(err)
	}

	files := []string{
		filepath.Join(actualCwd, "out.txt"),
		filepath.Join(actualCwd, "err.txt"),
		filepath.Join(actualCwd, "result.rds"),
		filepath.Join(tmpDir, "scratch"),
	}

	for _, file := range files {
		if err = os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}

	return &Job{Cwd: cwd, ActualCwd: actualCwd}
}
