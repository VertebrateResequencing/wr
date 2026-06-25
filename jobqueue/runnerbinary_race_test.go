//go:build race

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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const envTestRunnerBinary = "WR_TEST_RUNNER_BINARY"

//nolint:gochecknoglobals // TestMain cleans up the process-wide compiled binary.
var runnerBinaryTempDir string

func TestMain(m *testing.M) {
	code := m.Run()

	if runnerBinaryTempDir != "" {
		if err := os.RemoveAll(runnerBinaryTempDir); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to remove %s: %s\n", runnerBinaryTempDir, err)
			if code == 0 {
				code = 1
			}
		}
	}

	os.Exit(code)
}

// runnerBinary returns the path to a test binary to run as the --servermode or
// --runnermode subprocess. Under the race detector we must NOT reuse the
// running (race-instrumented) binary: race-instrumenting the runner
// subprocesses makes every job far slower and inflates their measured memory,
// which breaks the memory-learning tests. So compile a plain (non-race) binary
// for them instead - the runner code paths are still race-checked by the
// in-process server during the rest of the suite.
func runnerBinary() (string, error) {
	if path := os.Getenv(envTestRunnerBinary); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("%s=%q is not usable: %w", envTestRunnerBinary, path, err)
		}

		if info.IsDir() {
			return "", fmt.Errorf("%s=%q is a directory", envTestRunnerBinary, path)
		}

		return path, nil
	}

	dir, err := os.MkdirTemp("", "wr_self_test")
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, "wr.test")

	out, err := exec.CommandContext(context.Background(), "go", "test",
		"-tags", "netgo", "-run", "TestJobqueue", "-c", "-o", path).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dir)

		return "", fmt.Errorf("failed to compile self: %w: %s", err, string(out))
	}

	runnerBinaryTempDir = dir

	return path, nil
}
