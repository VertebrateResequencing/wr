# Nextflow Process Stdout/Stderr Capture

## Problem

When running Nextflow workflows through `wr nextflow run`, processes that only
write to stdout/stderr (like nextflow-io/hello) produce no visible output. wr
discards stdout/stderr for successful jobs to avoid filling the database, so
users have no way to see what these processes printed.

## Proposed Solution

Capture stdout and stderr of every Nextflow process to well-known files inside
each job's nf-work directory. Since each Nextflow job already gets a
deterministic CWD under `nf-work/{runID}/{process}/[{index}/]`, the files live
alongside the job's other artefacts with zero database overhead.

### Command Wrapping

In `buildCommand()` (nextflowdsl/translate.go), wrap the generated shell script
so stdout and stderr are redirected to `.nf-stdout` and `.nf-stderr` in the
job's CWD:

```sh
{ <original script>; } > .nf-stdout 2> .nf-stderr
```

Apply this unconditionally to all Nextflow process jobs — not just stdout-only
processes — so output is always available.

### Live Display During `--follow`

In `followNextflowWorkflow()` (cmd/nextflow.go), track which jobs have already
been displayed. Each poll iteration, for newly-completed jobs, read
`.nf-stdout` from `job.Cwd` and print non-empty content to the terminal with
a process-name prefix:

```
[sayHello (1)] Hello world!
[sayHello (2)] Bonjour monde!
```

### After-the-Fact Display via `nextflow status`

Add an `--output`/`-o` flag to `wr nextflow status`. When provided together
with `--run-id`, after the normal count table, iterate over complete jobs for
that run, read `.nf-stdout`/`.nf-stderr` from their CWDs, and print them with
process/index headers.

## Notes

- All Nextflow process commands are wrapped unconditionally — no opt-in config.
- During `--follow`, output is displayed once when a job transitions to complete
  (not on every poll, not incrementally).
- `nextflow status --output` prints stdout then stderr using the shared
  D2/D3 formatter: single-line content stays inline with the label, while
  multi-line content uses a header line with 2-space-indented content below.
- Empty or missing `.nf-stdout`/`.nf-stderr` files are silently skipped.
- Single-instance processes omit the index: `[processName]` not
  `[processName (0)]`. Multi-instance processes include it: `[processName (1)]`.
- `--output` requires `--run-id`; error if used without it.
- Single-line output uses `[processName] content`; multi-line output uses a
  header line on its own with content indented below.
- Output files are truncated at 1 MB per file to guard against huge output.
- Process name and instance index are extracted from the job's RepGroup using
  the existing parseNextflowRepGroup() function.
- Read errors on .nf-stdout/.nf-stderr are silently skipped (same as missing).
- Output in `status --output` is grouped by process name alphabetically, then
  by index within each process.
- Instance index is extracted from the job's Cwd path (the nf-work directory
  has an index directory segment for multi-instance processes).
- A process is multi-instance when multiple completed jobs share the same
  process name.
- When output exceeds 1 MB, read just the first 1 MB and append a truncation
  indicator like "[... output truncated ...]".
- Multi-line output content is indented with 2 spaces below the header line.
- The shell wrapping uses `{ <script>; } > .nf-stdout 2> .nf-stderr` with no
  additional error handling — the group construct preserves the exit code.
- During `--follow`, both stdout and stderr are displayed for newly completed
  jobs using the same shared formatter as `status --output`.
- `--follow` uses the same inline-for-single-line and indented-for-multi-line
  formatting rules as `status --output`.
- Files containing only whitespace are treated as empty and silently skipped.
- Output from failed/buried jobs is also displayed, not just successful ones.
- Process name comes from RepGroup via parseNextflowRepGroup(); instance index
  comes from Cwd path. Both are used together.
- Truncation indicator appears on its own line at the end of the displayed
  content.
- During `--follow`, stdout is shown before stderr for each job.
- `status --output` shows output for complete and buried jobs only (not
  pending/running).
- Empty or whitespace only `.nf-stdout`/`.nf-stderr` files should be deleted on
  step completion.
