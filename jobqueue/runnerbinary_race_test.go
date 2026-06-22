//go:build race

package jobqueue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
