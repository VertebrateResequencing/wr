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

//nolint:goconst,wsl_v5 // Lane-name tables and straightforward command steps are clearer as-is.
package testsuite

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/term"
)

const (
	envRunnerExecShell    = "WR_RUNNEREXECSHELL"
	envTestRunnerBinary   = "WR_TEST_RUNNER_BINARY"
	envTestPortBase       = "WR_TEST_PORT_BASE"
	envMaxParallel        = "WR_TESTSUITE_MAX_PARALLEL"
	defaultExecShell      = "/bin/bash"
	minDefaultParallel    = 4
	maxDefaultParallel    = 24
	parallelPerCPU        = 6
	minTestPortBase       = 10000
	tempPrefixTest        = "wrtest."
	tempPrefixRace        = "wrrace."
	lanePortSpan          = 200
	defaultEphemeralStart = 32768
	maxTCPPort            = 65535
)

var (
	errMissingCompiledBinary = errors.New("missing compiled binary")
	errNoPortRange           = errors.New("not enough port space")
	errNoFreePortRange       = errors.New("could not find a free port range")
	errInvalidPortBase       = errors.New("invalid test port base")
	errUnsafePortBase        = errors.New("test port base enters ephemeral port range")
	errUnexpectedModule      = errors.New("unexpected module discovery output")
	errUnknownLaneKind       = errors.New("unknown lane kind")
)

// ErrSuiteFailed reports that one or more lanes failed. Callers print the red
// FAILED marker themselves, so this sentinel is silent on stderr.
var ErrSuiteFailed = errors.New("test suite failed")

// isTerminal reports whether the writer is a real terminal (TTY), so colour is
// only emitted to an interactive terminal and never to pipes, files, /dev/null,
// CI, or the buffers used by unit tests.
func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}

	return term.IsTerminal(int(file.Fd()))
}

func reportFailure(stdout io.Writer, failed []laneResult, colourize bool) error {
	if err := printLaneLogs(stdout, failed); err != nil {
		return err
	}

	if _, err := io.WriteString(stdout, "\n"+summaryIndent+finalMarker(false, colourize)); err != nil {
		return fmt.Errorf("write failure marker: %w", err)
	}

	return ErrSuiteFailed
}

func reportSuccess(stdout io.Writer, module string, results []laneResult, colourize bool, elapsed time.Duration) error {
	lanes, err := laneSummaryInputs(results)
	if err != nil {
		return err
	}

	if _, err := io.WriteString(stdout, summarizeLanes(module, lanes, colourize, elapsed)); err != nil {
		return fmt.Errorf("write success summary: %w", err)
	}

	return nil
}

func laneSummaryInputs(results []laneResult) ([]laneSummaryInput, error) {
	lanes := make([]laneSummaryInput, 0, len(results))

	for _, result := range results {
		content, err := os.ReadFile(result.log)
		if err != nil {
			return nil, fmt.Errorf("open lane log %s: %w", result.lane.Name, err)
		}

		lanes = append(lanes, laneSummaryInput{
			name: result.lane.Name,
			kind: result.lane.Kind,
			pkg:  result.lane.Package,
			pkgs: result.lane.Packages,
			log:  string(content),
		})
	}

	return lanes, nil
}

// Run discovers packages, plans the requested suite mode, and executes it.
func Run(ctx context.Context, stdout io.Writer, stderr io.Writer, mode Mode) error {
	root, module, packages, err := discover(ctx)
	if err != nil {
		return err
	}

	return RunPlan(ctx, stdout, stderr, root, NewPlan(mode, module, packages))
}

// RunPlan executes an already-created test-suite plan.
func RunPlan(ctx context.Context, stdout io.Writer, stderr io.Writer, root string, plan Plan) error {
	started := time.Now()

	reapDeadSuiteTemps()

	base, err := os.MkdirTemp("", tempPrefix(plan.Mode))
	if err != nil {
		return fmt.Errorf("create test-suite temp dir: %w", err)
	}

	defer removeTemp(stderr, base)

	restorePortBase, err := setRunPortBase(ctx, plan)
	if err != nil {
		return err
	}

	defer restorePortBase()

	prog := newProgress(stderr, len(plan.Serial)+len(plan.Parallel))
	prog.start()

	defer prog.stop()

	prog.setPhase("compiling test binaries")

	binaries, err := compileBinaries(ctx, prog.bypass(stdout), prog.bypass(stderr), base, plan.Compiles)
	if err != nil {
		return err
	}

	prog.beginTesting()

	results := runSerialLanes(ctx, root, base, binaries, plan.Serial, prog)
	results = append(results, runParallelLanes(ctx, root, base, binaries, plan.Parallel, prog)...)

	prog.stop()

	return reportResults(stdout, plan.Module, results, time.Since(started))
}

// reapDeadSuiteTemps removes the temp dirs earlier suite runs left in
// os.TempDir() because they were killed before RunPlan could remove their own -
// by a timeout wrapper, a Ctrl-C, or an agent's tool deadline. Each such run
// leaks one dir, of 130-230MB, that nothing else ever removes.
//
// It keys on whether the pid encoded in the dir's name - the pid of the process
// that CREATED it - is still alive, so a suite running concurrently keeps its
// dir. That distinction is the whole reason this is not an rm -rf /tmp/wrtest*
// in make clean: on a /tmp shared with other checkouts and agents that deletes
// a live run's manager databases, which is not hypothetical - it happened
// during review of the equivalent per-config-dir reaper.
//
// A dir from a PRE-FIX binary is named wrtest.<random>, carrying no pid, so it
// matches nothing here.
func reapDeadSuiteTemps() {
	for _, prefix := range []string{tempPrefixTest, tempPrefixRace} {
		matches, err := filepath.Glob(filepath.Join(os.TempDir(), prefix+"*.*"))
		if err != nil {
			continue
		}

		for _, path := range matches {
			if pid, ok := suiteTempPid(prefix, path); ok && !pidExists(pid) {
				_ = os.RemoveAll(path)
			}
		}
	}
}

// suiteTempPid returns the pid encoded in a temp dir that tempPrefix named with
// the given prefix. The pid must be written canonically, so that a planted
// "wrtest.+123.x" or "wrtest.0123.x" cannot borrow the liveness of pid 123.
func suiteTempPid(prefix, path string) (int, bool) {
	pidStr, _, found := strings.Cut(strings.TrimPrefix(filepath.Base(path), prefix), ".")
	if !found {
		return 0, false
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != pidStr {
		return 0, false
	}

	return pid, true
}

// pidExists says whether pid is a process on this host, erring towards yes so
// that an unreadable /proc never costs us someone else's temp dir. The jobqueue
// test suite's reaper of its own per-config dirs has the same helper, on
// purpose: both give the same guarantee about the creating process.
func pidExists(pid int) bool {
	exists, err := process.PidExists(int32(pid)) //nolint:gosec // a pid always fits in an int32

	return err != nil || exists
}

// modeTempPrefix returns the part of a temp dir name that says which suite made
// it.
func modeTempPrefix(mode Mode) string {
	if mode == ModeRace {
		return tempPrefixRace
	}

	return tempPrefixTest
}

func setRunPortBase(ctx context.Context, plan Plan) (func(), error) {
	if baseEnv := os.Getenv(envTestPortBase); baseEnv != "" {
		return useConfiguredRunPortBase(ctx, plan, baseEnv)
	}

	base, err := chooseRunPortBase(ctx, plan)
	if err != nil {
		return nil, err
	}

	if err := os.Setenv(envTestPortBase, strconv.Itoa(base)); err != nil {
		return nil, fmt.Errorf("set %s: %w", envTestPortBase, err)
	}

	return func() { _ = os.Unsetenv(envTestPortBase) }, nil
}

func useConfiguredRunPortBase(ctx context.Context, plan Plan, baseEnv string) (func(), error) {
	base, err := strconv.Atoi(baseEnv)
	if err != nil || base < minTestPortBase {
		return nil, fmt.Errorf("%w: %s=%q", errInvalidPortBase, envTestPortBase, baseEnv)
	}

	if err := validateRunPortBase(ctx, plan, base); err != nil {
		return nil, err
	}

	return func() {}, nil
}

func validateRunPortBase(ctx context.Context, plan Plan, base int) error {
	return validateRunPortBaseWithEphemeralStart(ctx, plan, base, ephemeralPortStart())
}

func validateRunPortBaseWithEphemeralStart(ctx context.Context, plan Plan, base int, ephemeralStart int) error {
	maxLane := maxPlanLane(plan)
	maxBase, err := maxRunPortBase(maxLane, ephemeralStart)
	if err != nil {
		return err
	}

	if base > maxBase {
		return fmt.Errorf("%w: %s=%d would use ports at or above ephemeral port start %d",
			errUnsafePortBase, envTestPortBase, base, ephemeralStart)
	}

	if !runPortBaseAvailable(ctx, base, maxLane) {
		return fmt.Errorf("%w for %s=%d", errNoFreePortRange, envTestPortBase, base)
	}

	return nil
}

func chooseRunPortBase(ctx context.Context, plan Plan) (int, error) {
	maxLane := maxPlanLane(plan)
	maxBase, err := maxRunPortBase(maxLane, ephemeralPortStart())
	if err != nil {
		return 0, err
	}

	width := maxBase - minTestPortBase + 1
	startOffset, err := cryptoRandomInt(width)
	if err != nil {
		return 0, err
	}

	for offset := range width {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		base := minTestPortBase + ((startOffset + offset) % width)

		if runPortBaseAvailable(ctx, base, maxLane) {
			return base, nil
		}
	}

	return 0, fmt.Errorf("%w for %s", errNoFreePortRange, envTestPortBase)
}

func maxRunPortBase(maxLane int, ephemeralStart int) (int, error) {
	maxBase := min(ephemeralStart-1, maxTCPPort) - ((maxLane + 1) * lanePortSpan)
	if maxBase < minTestPortBase {
		return 0, fmt.Errorf("%w for WR_TEST_LANE=%d", errNoPortRange, maxLane)
	}

	return maxBase, nil
}

func ephemeralPortStart() int {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return defaultEphemeralStart
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return defaultEphemeralStart
	}

	start, err := strconv.Atoi(fields[0])
	if err != nil || start <= 0 {
		return defaultEphemeralStart
	}

	return start
}

func cryptoRandomInt(limit int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, fmt.Errorf("choose random port base: %w", err)
	}

	return int(n.Int64()), nil
}

func runPortBaseAvailable(ctx context.Context, base int, maxLane int) bool {
	for lane := range maxLane + 1 {
		for _, offset := range []int{1, 2, 3} {
			if !portAvailable(ctx, base+lane*lanePortSpan+offset) {
				return false
			}
		}
	}

	return true
}

func portAvailable(ctx context.Context, port int) bool {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return false
	}

	return listener.Close() == nil
}

func maxPlanLane(plan Plan) int {
	maxLane := 0

	for _, lane := range append(plan.Serial, plan.Parallel...) {
		value, err := strconv.Atoi(lane.Env["WR_TEST_LANE"])
		if err == nil && value > maxLane {
			maxLane = value
		}
	}

	return maxLane
}

func discover(ctx context.Context) (string, string, []string, error) {
	root, module, err := discoverModule(ctx)
	if err != nil {
		return "", "", nil, err
	}

	packages, err := discoverPackages(ctx)
	if err != nil {
		return "", "", nil, err
	}

	return root, module, packages, nil
}

func discoverModule(ctx context.Context) (string, string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}\n{{.Path}}")
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("discover module: %w", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	fields := make([]string, 0, 2)

	for scanner.Scan() {
		fields = append(fields, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("read module discovery output: %w", err)
	}

	if len(fields) != 2 {
		return "", "", fmt.Errorf("%w: expected 2 fields, got %d", errUnexpectedModule, len(fields))
	}

	return fields[0], fields[1], nil
}

func discoverPackages(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "./...")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("discover packages: %w", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	packages := make([]string, 0, 64)

	for scanner.Scan() {
		packages = append(packages, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read package discovery output: %w", err)
	}

	return packages, nil
}

func compileBinaries(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	base string,
	compiles []Compile,
) (map[string]string, error) {
	results := make([]compileResult, len(compiles))
	var wg sync.WaitGroup
	sem := make(chan struct{}, compileParallelism(len(compiles)))

	for index, compile := range compiles {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			results[index] = compileBinary(ctx, stdout, stderr, base, compile)
		})
	}

	wg.Wait()

	binaries := make(map[string]string, len(compiles))

	for _, result := range results {
		if result.err != nil {
			return nil, result.err
		}

		binaries[result.name] = result.path
	}

	return binaries, nil
}

type compileResult struct {
	name string
	path string
	err  error
}

func compileBinary(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	base string,
	compile Compile,
) compileResult {
	output := filepath.Join(base, compile.Name+".test")
	args := []string{"test", "-tags", "netgo"}

	if compile.Race {
		args = append(args, "-race")
	}

	args = append(args, "-c", "-o", output, compile.Package)

	if err := runCommand(ctx, "", stdout, stderr, "go", args, nil); err != nil {
		return compileResult{name: compile.Name, path: output, err: fmt.Errorf("compile %s: %w", compile.Name, err)}
	}

	return compileResult{name: compile.Name, path: output}
}

func compileParallelism(compileCount int) int {
	return compileParallelismForCPU(compileCount, runtime.GOMAXPROCS(0))
}

func compileParallelismForCPU(compileCount int, cpus int) int {
	if compileCount < 1 {
		return 1
	}

	return min(compileCount, max(cpus, 1))
}

func runSerialLanes(
	ctx context.Context,
	root string,
	base string,
	binaries map[string]string,
	lanes []Lane,
	prog *progress,
) []laneResult {
	results := make([]laneResult, 0, len(lanes))

	for _, lane := range lanes {
		results = append(results, runLane(ctx, root, base, binaries, lane, prog))
	}

	return results
}

func runParallelLanes(
	ctx context.Context,
	root string,
	base string,
	binaries map[string]string,
	lanes []Lane,
	prog *progress,
) []laneResult {
	lanes = prioritizedLanes(lanes)
	results := make([]laneResult, len(lanes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallel(len(lanes)))

	for index, lane := range lanes {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			results[index] = runLane(ctx, root, base, binaries, lane, prog)
		})
	}

	wg.Wait()

	return results
}

func prioritizedLanes(lanes []Lane) []Lane {
	ordered := slices.Clone(lanes)

	slices.SortStableFunc(ordered, func(left Lane, right Lane) int {
		return lanePriority(right.Name) - lanePriority(left.Name)
	})

	return ordered
}

func lanePriority(name string) int {
	weights := map[string]int{
		"jq_execution_retries":     112,
		"signal_b":                 110,
		"runner_lost_jobs":         108,
		"runner_scheduling_a":      106,
		"runner_scheduling_b":      104,
		"runner_resource_learning": 102,
		"runner_kill_requests":     100,
		"runner_auto_execution":    98,
		"runner_failure_retry":     96,
		"server_webi":              94,
		"other":                    92,
		"jq_sub_live":              90,
		"jq_sub_aggregate":         88,
		"jq_dependency":            86,
		"subscription_catchup":     84,
		"jq_sub_add":               82,
		"jq_sub_long":              80,
		"production":               78,
		"jq_execution_details":     76,
		"client_wait":              74,
		"client_a":                 72,
		"jqA1":                     70,
		"signal_a":                 68,
		"cmd_resume":               66,
		"subscription_teardown":    64,
		"modify_a":                 60,
		"modify_b":                 58,
		"jq_status":                56,
		"cmd_suspend":              54,
		"cmd_status":               52,
		"client_wait_jobs":         50,
		"client_basics":            48,
		"scheduler":                46,
		"cmd_add":                  44,
	}

	return weights[name]
}

func maxParallel(laneCount int) int {
	raw := os.Getenv(envMaxParallel)
	if raw == "" {
		return defaultParallelLimit(laneCount)
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return defaultParallelLimit(laneCount)
	}

	if limit > laneCount {
		return laneCount
	}

	return limit
}

func defaultParallelLimit(laneCount int) int {
	return defaultParallelLimitForCPU(laneCount, runtime.GOMAXPROCS(0))
}

func defaultParallelLimitForCPU(laneCount int, cpus int) int {
	limit := cpus * parallelPerCPU
	limit = max(limit, minDefaultParallel)
	limit = min(limit, maxDefaultParallel)

	return min(limit, laneCount)
}

type laneResult struct {
	lane     Lane
	log      string
	duration time.Duration
	err      error
}

func runLane(
	ctx context.Context,
	root string,
	base string,
	binaries map[string]string,
	lane Lane,
	prog *progress,
) laneResult {
	prog.laneStarted()
	defer prog.laneFinished()

	start := time.Now()
	logPath := filepath.Join(base, lane.Name+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return laneResult{
			lane:     lane,
			log:      logPath,
			duration: time.Since(start),
			err:      fmt.Errorf("create lane log: %w", err),
		}
	}

	defer closeLog(logFile)

	name, args, err := laneCommand(lane, binaries)
	if err != nil {
		if _, writeErr := io.WriteString(logFile, err.Error()+"\n"); writeErr != nil {
			err = errors.Join(err, writeErr)
		}

		return laneResult{lane: lane, log: logPath, duration: time.Since(start), err: err}
	}

	if lane.Nice {
		args = append([]string{"-n", "19", name}, args...)
		name = "nice"
	}

	sink := prog.tee(logFile)
	err = runCommand(ctx, laneWorkDir(root, lane), sink, sink, name, args, laneEnvWithBinaries(lane, binaries))

	return laneResult{lane: lane, log: logPath, duration: time.Since(start), err: err}
}

func laneEnvWithBinaries(lane Lane, binaries map[string]string) map[string]string {
	if lane.Binary != "jobqueue" {
		return lane.Env
	}

	runnerBinary, ok := binaries["jobqueue_runner"]
	if !ok {
		return lane.Env
	}

	env := make(map[string]string, len(lane.Env)+1)
	maps.Copy(env, lane.Env)

	env[envTestRunnerBinary] = runnerBinary

	return env
}

func laneCommand(lane Lane, binaries map[string]string) (string, []string, error) {
	switch lane.Kind {
	case LaneKindBinary:
		binary, ok := binaries[lane.Binary]
		if !ok {
			return "", nil, fmt.Errorf("%w %q", errMissingCompiledBinary, lane.Binary)
		}

		return binary, binaryArgs(lane), nil
	case LaneKindGoTest:
		return "go", goTestArgs(lane), nil
	default:
		return "", nil, fmt.Errorf("%w %q", errUnknownLaneKind, lane.Kind)
	}
}

func binaryArgs(lane Lane) []string {
	args := []string{"-test.timeout=" + defaultTimeout, "-test.failfast", "-test.v"}

	if lane.RunPattern != "" {
		args = append(args, "-test.run", lane.RunPattern)
	}

	if lane.SkipPattern != "" {
		args = append(args, "-test.skip", lane.SkipPattern)
	}

	return args
}

func goTestArgs(lane Lane) []string {
	args := []string{"test", "-tags", "netgo", "-timeout", defaultTimeout, "--count", "1", "-failfast", "-v"}

	if lane.Race {
		args = append(args, "-race")
	}

	if lane.RunPattern != "" {
		args = append(args, "-run", lane.RunPattern)
	}

	if lane.SkipPattern != "" {
		args = append(args, "-skip", lane.SkipPattern)
	}

	if lane.Parallelism > 0 {
		args = append(args, "-p", strconv.Itoa(lane.Parallelism))
	}

	return append(args, lane.Packages...)
}

func laneWorkDir(root string, lane Lane) string {
	if lane.Dir == "" {
		return root
	}

	return filepath.Join(root, lane.Dir)
}

func runCommand(
	ctx context.Context,
	dir string,
	stdout io.Writer,
	stderr io.Writer,
	name string,
	args []string,
	extraEnv map[string]string,
) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = commandEnv(extraEnv)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

func commandEnv(extra map[string]string) []string {
	env := envMap(os.Environ())

	if env[envRunnerExecShell] == "" {
		env[envRunnerExecShell] = defaultExecShell
	}

	maps.Copy(env, extra)

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}

	return out
}

func envMap(values []string) map[string]string {
	env := make(map[string]string, len(values))

	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if ok {
			env[key] = val
		}
	}

	return env
}

func reportResults(stdout io.Writer, module string, results []laneResult, elapsed time.Duration) error {
	if os.Getenv("WR_TESTSUITE_TIMINGS") != "" {
		if err := printTimings(stdout, results); err != nil {
			return err
		}
	}

	colourize := isTerminal(stdout)

	failed := failedResults(results)
	if len(failed) > 0 {
		return reportFailure(stdout, failed, colourize)
	}

	return reportSuccess(stdout, module, results, colourize, elapsed)
}

func failedResults(results []laneResult) []laneResult {
	failed := make([]laneResult, 0)

	for _, result := range results {
		if result.err != nil {
			failed = append(failed, result)
		}
	}

	return failed
}

func printTimings(stdout io.Writer, results []laneResult) error {
	slices.SortFunc(results, func(left laneResult, right laneResult) int {
		if left.duration > right.duration {
			return -1
		}

		if left.duration < right.duration {
			return 1
		}

		return 0
	})

	for _, result := range results {
		timing := result.lane.Name + " " + result.duration.Round(time.Millisecond).String() + "\n"
		if _, err := io.WriteString(stdout, timing); err != nil {
			return fmt.Errorf("write lane timing: %w", err)
		}
	}

	return nil
}

func printLaneLogs(stdout io.Writer, results []laneResult) error {
	for _, result := range results {
		if _, err := io.WriteString(stdout, "===== "+result.lane.Name+" =====\n"); err != nil {
			return fmt.Errorf("write lane header: %w", err)
		}

		if err := copyLaneLog(stdout, result); err != nil {
			return err
		}
	}

	return nil
}

func copyLaneLog(stdout io.Writer, result laneResult) error {
	content, err := os.ReadFile(result.log)
	if err != nil {
		return fmt.Errorf("open lane log %s: %w", result.lane.Name, err)
	}

	if _, err := io.WriteString(stdout, summarizeFailureLog(string(content))); err != nil {
		return fmt.Errorf("copy lane log %s: %w", result.lane.Name, err)
	}

	return nil
}

func closeLog(file *os.File) {
	_ = file.Close()
}

func removeTemp(stderr io.Writer, path string) {
	if err := os.RemoveAll(path); err != nil {
		writeCleanupWarning(stderr, path, err)
	}
}

func writeCleanupWarning(stderr io.Writer, path string, err error) {
	_, _ = io.WriteString(stderr, "warning: cleanup "+path+": "+err.Error()+"\n") //nolint:errcheck
}

// tempPrefix returns the os.MkdirTemp prefix for the suite's own temp dir in
// mode. The creating pid is part of it so that a dir left behind by a run that
// was killed before RunPlan could remove its own can be reaped by a later run;
// see reapDeadSuiteTemps.
func tempPrefix(mode Mode) string {
	return modeTempPrefix(mode) + strconv.Itoa(os.Getpid()) + "."
}
