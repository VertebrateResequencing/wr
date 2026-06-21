//go:build !race

package jobqueue

import "os"

// runnerBinary returns the path to a test binary to run as the --servermode or
// --runnermode subprocess. Outside the race detector the running test binary is
// exactly what those subprocesses need, so we reuse it directly (no recompile).
func runnerBinary() (string, error) {
	return os.Executable()
}
