# Nextflow Process Stdout/Stderr Capture Specification

## Overview

Nextflow processes that write to stdout/stderr produce no visible output
through wr because wr discards stdout/stderr for successful jobs. This
feature captures stdout and stderr to well-known files in each job's
nf-work CWD, then displays them during `--follow` and via
`nextflow status --output`.

Three changes:
1. `buildCommand()` wraps every process script so stdout/stderr redirect
   to `.nf-stdout`/`.nf-stderr` in the job's CWD.
2. `followNextflowWorkflow()` prints captured output for newly completed
   or buried jobs each poll iteration.
3. `nextflow status` gains `--output`/`-o` to display captured output
   after the count table.

Empty/whitespace-only capture files are deleted via an OnExit behaviour.

## Architecture

**Package:** `nextflowdsl/` -- command wrapping.
**File:** `nextflowdsl/translate.go` -- modify `buildCommand()`.
**Test file:** `nextflowdsl/translate_test.go`

**Package:** `cmd/` -- follow output display, status `--output` flag.
**File:** `cmd/nextflow.go` -- modify `followNextflowWorkflow()`,
`runNextflowStatus()`, `newNextflowStatusCommand()`. Add output
reading/formatting helpers.
**Test file:** `cmd/nextflow_test.go`

### Data flow

1. `buildCommand()` returns:
   `{ <exports>\n<script>; } > .nf-stdout 2> .nf-stderr`
   The group construct `{ ...; }` preserves the exit code.
2. A cleanup behaviour (OnExit, Run) deletes `.nf-stdout`/`.nf-stderr`
   when they are empty or whitespace-only after the command finishes.
3. During `--follow`, each poll reads `.nf-stdout` and `.nf-stderr` from
   newly terminal jobs and prints them.
4. `status --output` reads the same files from complete/buried jobs.

### Output format

Single-line output:
```text
[processName (index)] content here
```

Multi-line output (header on its own line, content 2-space indented):
```text
[processName (index)]
  line 1
  line 2
```

Single-instance processes omit the index: `[processName]`.
Multi-instance means >1 job shares the same process name among ALL
jobs known to the queue (complete + buried + incomplete), not just
the displayed set.

Stdout is shown before stderr for each job. Files >1 MB are truncated
to 1 MB with a trailing `[... output truncated ...]` line.

### Key types/functions (existing)

- `parseNextflowRepGroup(repGroup) (nextflowRepGroup, bool)` -- extracts
  workflow, runID, process from RepGroup.
- `deterministicCwd()` / `deterministicIndexedCwd()` -- build CWD paths
  under `nf-work/{runID}/{process}[/{index}]`.
- `nextflowRepGroup` struct: `{workflow, runID, process string}`.
- `Behaviour{When, Do, Arg}` -- job lifecycle hooks.

### New helper signatures (cmd/nextflow.go)

```go
// readCapturedOutput reads a file, returning at most maxBytes.
// Returns ("", nil) for missing, empty, or whitespace-only files.
// Appends "[... output truncated ...]" if truncated.
func readCapturedOutput(path string, maxBytes int64) (string, error)

// formatJobOutput formats captured stdout/stderr for one job.
// Returns "" if both are empty. label is e.g. "[sayHello (1)]".
func formatJobOutput(label string, stdout, stderr string) string

// jobOutputLabel builds "[process]" or "[process (index)]".
// index comes from the Cwd path (last segment if numeric).
// isMultiInstance indicates whether to include the index.
func jobOutputLabel(
    process string, cwd string, isMultiInstance bool,
) string

// instanceIndexFromCwd extracts the trailing numeric segment from
// a CWD path, returning (index, true) or (0, false).
func instanceIndexFromCwd(cwd string) (int, bool)

// printJobsOutput writes formatted output for displayJobs.
// Groups by process name alphabetically, then by index.
// allJobs is used for multi-instance detection (includes
// complete + buried + incomplete).
func printJobsOutput(
    w io.Writer, displayJobs []*jobqueue.Job,
    allJobs []*jobqueue.Job, maxBytes int64,
) error
```

### Constants

```go
const nfStdoutFile = ".nf-stdout"
const nfStderrFile = ".nf-stderr"
const nfOutputMaxBytes int64 = 1 << 20 // 1 MB
```

## A. Command Wrapping

### A1: Wrap process scripts to capture stdout/stderr

As a user, I want every Nextflow process command to redirect stdout and
stderr to `.nf-stdout` and `.nf-stderr` in the job's CWD, so that
process output is always available for inspection.

`buildCommand()` wraps unconditionally. The group construct preserves
exit code. No bindings case: `{ <script>; } > .nf-stdout 2> .nf-stderr`.
With bindings: `{ export ...\n<script>; } > .nf-stdout 2> .nf-stderr`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given a process with script `echo hello` and no input bindings,
   when `buildCommand()` is called, then the result equals
   `{ echo hello; } > .nf-stdout 2> .nf-stderr`.
2. Given a process with script `cat $reads` and one binding
   `"/data/a.fq"` with input name `reads`, when `buildCommand()` is
   called, then the result equals
   `{ export reads='/data/a.fq'\ncat $reads; } > .nf-stdout 2> .nf-stderr`.
3. Given a process with a multi-line script `line1\nline2` and no
   bindings, when `buildCommand()` is called, then the result equals
   `{ line1\nline2; } > .nf-stdout 2> .nf-stderr`.
4. Given a process with script `echo ${params.greeting}` and params
   `{"greeting": "hi"}`, when `buildCommand()` is called, then the
   result equals `{ echo hi; } > .nf-stdout 2> .nf-stderr`.

### A2: Cleanup behaviour for empty capture files

As a user, I want empty or whitespace-only `.nf-stdout` and
`.nf-stderr` files deleted after the job finishes, so that disk space
is not wasted on empty files.

An OnExit Run behaviour is added to every Nextflow process job. The
behaviour command removes `.nf-stdout` and `.nf-stderr` if they
contain only whitespace:

```sh
for f in .nf-stdout .nf-stderr; do
  if [ -f "$f" ] && ! grep -qP '\S' "$f"; then rm -f "$f"; fi;
done
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given a Translate call producing a single-process job, when the
   resulting job's Behaviours are inspected, then there is a Behaviour
   with `When == OnExit`, `Do == Run`, and `Arg` containing
   `.nf-stdout` and `.nf-stderr` and `grep` and `rm`.

## B. Follow Output Display

### B1: Display output for newly completed/buried jobs during follow

As a user, I want `--follow` to print process stdout and stderr when
jobs transition to complete or buried, so that I can see output in
real time without checking files.

`followNextflowWorkflow()` tracks which job keys have been displayed.
Each poll iteration, for each complete or buried job not yet displayed,
reads `.nf-stdout` and `.nf-stderr` from `job.Cwd`, formats with
`formatJobOutput()`, and prints to stdout. Stdout shown before stderr.
Read errors and missing/whitespace-only files are silently skipped.
Output from both successful and failed (buried) jobs is displayed.
Multi-instance is determined from ALL known jobs (complete + buried +
incomplete), not just terminal ones, so labels are consistent across
poll iterations.

The output writer is added as a parameter to `followNextflowWorkflow()`
(or uses `os.Stdout`).

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** `cmd/nextflow_test.go`

**Acceptance tests:**

1. Given a fakeNextflowQueue scenario where a job with CWD containing
   `.nf-stdout` = `"Hello world!\n"` transitions from incomplete to
   complete and the process is single-instance, when
   `followNextflowWorkflow()` completes, then the output writer
   contains `[sayHello]\n  Hello world!\n`.
2. Given a scenario where a job's CWD has `.nf-stdout` = `"line1\nline2\n"`,
   when follow completes, then output contains a header
   `[sayHello]` on its own line followed by `  line1\n  line2\n`.
3. Given a scenario where a job's `.nf-stdout` is missing and
   `.nf-stderr` is missing, when follow completes, then no output
   lines are printed for that job.
4. Given a scenario where a job's `.nf-stdout` contains only
   whitespace `"   \n"`, when follow completes, then no output is
   printed for that job's stdout.
5. Given a scenario where a job transitions to buried and `.nf-stdout`
   has content, when follow completes, then the output is still
   displayed.
6. Given two jobs with the same process name `sayHello` (indexed 0
   and 1), when follow displays output, then the labels are
   `[sayHello (0)]` and `[sayHello (1)]`.
7. Given a job whose `.nf-stderr` = `"error msg\n"`, when follow
   displays output, then stdout is printed before stderr, and stderr
   has label `[processName] (stderr)` (single-instance) or
   `[processName (index)] (stderr)` (multi-instance).
8. Given a `.nf-stdout` file larger than 1 MB, when follow reads it,
   then only the first 1 MB is shown plus a trailing
   `[... output truncated ...]` line.

## C. Status Output Display

### C1: Add --output flag to nextflow status

As a user, I want `wr nextflow status --output --run-id <id>` to
display captured stdout/stderr from complete and buried jobs, so that
I can inspect output after the workflow finishes.

`newNextflowStatusCommand()` adds `--output`/`-o` bool flag.
`runNextflowStatus()` checks: if `--output` is set without `--run-id`,
return error `"--output requires --run-id"`. After printing the count
table, filter jobs to complete/buried states for display, but pass all
queried jobs to `printJobsOutput()` for multi-instance detection.

`nextflowStatusOptions` gains `Output bool`.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** `cmd/nextflow_test.go`

**Acceptance tests:**

1. Given `--output` without `--run-id`, when the command executes,
   then it returns an error containing `"--output requires --run-id"`.
2. Given a complete job with RepGroup `nf.mywf.r1.prepare` and CWD
   containing `.nf-stdout` = `"done\n"`, when `status --output
   --run-id r1` executes, then output after the count table contains
   `[prepare]\n  done\n`.
3. Given a buried job with `.nf-stderr` = `"failed\n"`, when
   `status --output --run-id r1` executes, then the stderr content
   appears with label `[prepare] (stderr)`.
4. Given a running job with `.nf-stdout` content, when
   `status --output --run-id r1` executes, then no output is shown
   for running jobs (only complete/buried).
5. Given two complete jobs with process `align` at CWDs ending in
   `/0` and `/1`, when `status --output --run-id r1` executes, then
   output is grouped by process alphabetically, with labels
   `[align (0)]` and `[align (1)]`.
6. Given no complete or buried jobs with output files, when
   `status --output --run-id r1` executes, then only the count table
   is printed (no extra output section).
7. Given a `.nf-stdout` larger than 1 MB, when `status --output`
   reads it, then the displayed content is truncated to 1 MB with
   `[... output truncated ...]` appended.

## D. Output Reading and Formatting Helpers

### D1: readCapturedOutput

As a developer, I want a helper that reads a capture file with size
limits, so that display code is DRY and memory-safe.

```go
func readCapturedOutput(path string, maxBytes int64) (string, error)
```

Returns `("", nil)` for missing files, read errors, empty content,
or whitespace-only content. On success returns raw content.
If file exceeds `maxBytes`, reads first `maxBytes` bytes and appends
`"\n[... output truncated ...]"`.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** `cmd/nextflow_test.go`

**Acceptance tests:**

1. Given a missing file path, when `readCapturedOutput` is called,
   then it returns `("", nil)`.
2. Given a file containing `"hello\n"`, when called with
   `maxBytes=1048576`, then it returns `("hello\n", nil)`.
3. Given a file containing only `"  \n\t\n"`, when called, then it
   returns `("", nil)`.
4. Given a file of 2 MB of `'x'`, when called with `maxBytes=1048576`,
   then it returns a string of length 1048576 + len of
   `"\n[... output truncated ...]"`, ending with
   `"[... output truncated ...]"`.
5. Given a file read error (e.g. permission denied), when called,
   then it returns `("", nil)`.

### D2: formatJobOutput and jobOutputLabel

As a developer, I want formatting helpers to produce the spec'd
output format consistently for both follow and status display.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** `cmd/nextflow_test.go`

**Acceptance tests:**

1. Given `formatJobOutput("[P]", "one line\n", "")`, then result is
   `"[P] one line\n"`.
2. Given `formatJobOutput("[P]", "a\nb\n", "")`, then result is
   `"[P]\n  a\n  b\n"`.
3. Given `formatJobOutput("[P]", "out\n", "err\n")`, then result is
   `"[P] out\n[P] (stderr) err\n"`.
4. Given `formatJobOutput("[P]", "", "err\n")`, then result is
   `"[P] (stderr) err\n"`.
5. Given `formatJobOutput("[P]", "", "")`, then result is `""`.
6. Given `formatJobOutput("[P]", "a\nb\n", "c\nd\n")`, then result
   is `"[P]\n  a\n  b\n[P] (stderr)\n  c\n  d\n"`.
7. Given `jobOutputLabel("sayHello", "/w/nf-work/r1/sayHello", false)`,
   then result is `"[sayHello]"`.
8. Given `jobOutputLabel("sayHello", "/w/nf-work/r1/sayHello/2", true)`,
   then result is `"[sayHello (2)]"`.
9. Given `instanceIndexFromCwd("/w/nf-work/r1/proc/3")`, then it
   returns `(3, true)`.
10. Given `instanceIndexFromCwd("/w/nf-work/r1/proc")`, then it
    returns `(0, false)`.

### D3: printJobsOutput

As a developer, I want a function that iterates jobs, reads their
capture files, and writes formatted output grouped by process then
index.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** `cmd/nextflow_test.go`

**Acceptance tests:**

1. Given two displayJobs: process `beta` CWD with `.nf-stdout` =
   `"b\n"`, and process `alpha` CWD with `.nf-stdout` = `"a\n"`,
   when `printJobsOutput` is called with allJobs == displayJobs,
   then `alpha` output appears before `beta` output.
2. Given two displayJobs with same process `align` at CWD indices
   1 and 0, when `printJobsOutput` is called with
   allJobs == displayJobs, then index 0 output appears before
   index 1.
3. Given a displayJob whose `.nf-stdout` and `.nf-stderr` are both
   missing, when `printJobsOutput` is called, then nothing is
   written for that job.
4. Given one displayJob with process `align` at CWD index 0, and
   allJobs containing two jobs with process `align` (indices 0
   and 1), when `printJobsOutput` is called, then the label is
   `[align (0)]` (multi-instance detected from allJobs).

## Implementation Order

1. **Phase 1 (A1, A2):** Command wrapping and cleanup behaviour in
   `nextflowdsl/translate.go`. Foundation -- all subsequent phases
   depend on `.nf-stdout`/`.nf-stderr` existing. Sequential.
2. **Phase 2 (D1, D2, D3):** Output reading and formatting helpers in
   `cmd/nextflow.go`. Pure functions, testable in isolation. Can be
   done in parallel within the phase.
3. **Phase 3 (B1):** Follow output display in `cmd/nextflow.go`.
   Depends on phases 1 and 2. Sequential.
4. **Phase 4 (C1):** Status `--output` flag in `cmd/nextflow.go`.
   Depends on phases 1 and 2. Can run in parallel with phase 3.

## Appendix: Key Decisions

- **Unconditional wrapping:** all process commands are wrapped, not
  just stdout-heavy ones. Simplifies implementation; disk cost is
  negligible for empty files since the cleanup behaviour removes them.
- **Group construct `{ ...; }`:** preserves exit code without extra
  error handling. Standard POSIX shell.
- **1 MB truncation:** guards against unbounded memory usage when
  reading output. Truncation indicator makes it clear content was cut.
- **OnExit behaviour for cleanup:** runs after both success and failure,
  ensuring whitespace-only files are always removed.
- **Instance index from CWD:** the nf-work directory structure
  `nf-work/{runID}/{process}/{index}` already encodes the index.
  Using CWD is more reliable than parsing RepGroup (which has no
  index).
- **Multi-instance detection:** a process is multi-instance when >1
  job shares the same process name among ALL known jobs (complete +
  buried + incomplete), not just the displayed set. This ensures labels
  are consistent across follow poll iterations and between follow and
  status output.
- **Testing strategy:** unit tests for `buildCommand()` changes use
  direct function calls in `translate_test.go`. Follow tests use the
  existing `fakeNextflowQueue` pattern with files in `t.TempDir()`.
  Status tests use the existing `newNextflowCommandTestEnv` pattern.
  All per GoConvey conventions in go-conventions skill.
- **Stderr labeling:** stderr sections append ` (stderr)` to the
  label to distinguish from stdout.
