# Nextflow DSL2 Translation Completeness Specification

## Overview

Closes all remaining translation and evaluation gaps in the
`nextflowdsl` package. After prior specs (nextflowdsl,
nextflowstrict, nextflowstrict2, nextflowstrict3), the parser
accepts all valid DSL2 syntax. This spec bridges from "parsed
and stored" to "translated to wr jobs" for 15 gaps:

1. `stub:` section translation
2. `shell:` section `!{var}` interpolation
3. `when:` guard evaluation
4. `accelerator` directive LSF mapping
5. `arch` directive LSF mapping
6. `ext` directive / `task.ext.*` resolution
7. `fair` directive priority mapping
8. `randomSample(n)` channel operator
9. `errorStrategy 'finish'` via limit groups
10. Process config defaults full propagation
11. `new` constructor evaluation
12. Multi-variable assignment evaluation
13. Additional Groovy methods (string, list, map, number)
14. `publish:` workflow section + `output {}` block translation
15. `assert` / `throw` statement evaluation

All changes confined to `nextflowdsl/` and `cmd/` packages.
Design principles from prior specs maintained. All existing
tests must pass.

## Architecture

### TranslateConfig changes (translate.go)

Add `Scheduler` and `StubRun` fields:

```go
type TranslateConfig struct {
    // ... existing fields ...
    Scheduler string // "lsf", "openstack", etc.; empty = local
    StubRun   bool   // true: use stub body instead of script
}
```

### ProcessDefaults changes (config.go)

Extend with typed fields for directives the translator uses,
plus a generic catch-all:

```go
type ProcessDefaults struct {
    // ... existing Cpus, Memory, Time, Disk, Container, Env ...
    ErrorStrategy    any // string or Expr
    MaxRetries       any // int or Expr
    MaxForks         any // int or Expr
    PublishDir       []*PublishDir
    Queue            any // string or Expr
    ClusterOptions   any // string or Expr
    Ext              map[string]any
    ContainerOptions any // string or Expr
    Accelerator      any
    Arch             any
    Shell            any
    BeforeScript     any
    AfterScript      any
    Cache            any
    Scratch          any
    StoreDir         any
    Module           any
    Conda            any
    Spack            any
    Fair             any
    Tag              any
    Directives       map[string]any // catch-all for warn-only
}
```

### AST: no new nodes

All 15 gaps use existing AST types. `Stub`, `Shell`, `When`
already stored on `Process`. `NewExpr`, `MultiAssignExpr`,
`AssertStmt`, `ThrowStmt` already parsed (strict3).

### OutputTarget type (translate.go)

```go
// OutputTarget represents a parsed target from the output {}
// block.
type OutputTarget struct {
    Name      string
    Path      string
    IndexPath string // path for TSV index file; empty if none
}
```

### ParseOutputBlock function (parse.go or translate.go)

```go
// ParseOutputBlock extracts named targets from a raw output
// block body string.
func ParseOutputBlock(body string) ([]*OutputTarget, error)
```

---

## A. Stub Section Translation (GAP 1)

### A1: `--stub-run` flag and stub body substitution

As a developer, I want a `--stub-run` flag on `wr nextflow
run` that substitutes stub bodies for script bodies, so that
dry-run mode uses lightweight stubs.

When `TranslateConfig.StubRun` is true, the translator uses
`Process.Stub` instead of `Process.Script` for every process
that has a non-empty `Stub`. Processes without stubs use their
normal script. The flag only controls which body becomes the
job's `Cmd`.

**Package:** `cmd/`, `nextflowdsl/`
**File:** `cmd/nextflow.go`, `nextflowdsl/translate.go`
**Test file:** `cmd/nextflow_test.go`,
`nextflowdsl/translate_test.go`

```go
// cmd/nextflow.go: add flag
cmd.Flags().BoolVar(&options.stubRun, "stub-run", false,
    "use stub section instead of script")
```

**Acceptance tests:**

1. Given process with `script: 'real_cmd'` and
   `stub: 'touch out.txt'`, when translated with
   `StubRun: true`, then job `Cmd` contains `touch out.txt`
   and does not contain `real_cmd`.
2. Given process with `script: 'real_cmd'` and no stub, when
   translated with `StubRun: true`, then job `Cmd` contains
   `real_cmd` (fallback to script).
3. Given process with `script: 'real_cmd'` and
   `stub: 'touch out.txt'`, when translated with
   `StubRun: false`, then job `Cmd` contains `real_cmd`.
4. Given workflow with 2 processes where only one has a stub,
   when translated with `StubRun: true`, then the stubbed
   process uses its stub and the other uses its script.

---

## B. Shell Section Interpolation (GAP 2)

### B1: `!{expr}` interpolation for shell sections

As a developer, I want processes using `shell:` sections to
have `!{expr}` patterns resolved and `${...}` left untouched,
so that bash variables pass through correctly.

When `Process.Shell` is non-empty, apply `!{expr}` interpolation
instead of `${expr}` interpolation. Scan for `!{expr}` patterns,
evaluate each `expr` using the existing Groovy evaluator with
input bindings and params, substitute the result, and leave all
`${...}` sequences unmodified.

String form: `shell: '!{var} and ${bash_var}'`.
List form: `shell: ['bash', '-ue', '!{cmd}']` -- first element
is interpreter, middle elements are flags, last element is the
script body with `!{expr}` interpolation. Interpreter and flags
construct the execution command (e.g. `bash -ue -c '<script>'`).
List form replaces the default `set -euo pipefail` header.

```go
// interpolateShellSection replaces !{expr} with evaluated
// values, leaving ${...} untouched.
func interpolateShellSection(
    shell string, vars map[string]any,
) (string, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `shell: 'echo !{name} ${BASH_VAR}'` and
   input binding `name='Alice'`, when translated, then job `Cmd`
   contains `echo Alice ${BASH_VAR}`.
2. Given process with shell containing `!{params.outdir}` and
   params `{outdir: '/data'}`, when translated, then job `Cmd`
   contains `/data` and `${...}` sequences are untouched.
3. Given process with `shell: ['bash', '-ue', '!{cmd}']` and
   input binding `cmd='ls -la'`, when translated, then job `Cmd`
   uses `bash -ue -c 'ls -la'` (or equivalent wrapper).
4. Given process with shell containing no `!{...}` patterns,
   when translated, then all `${...}` pass through verbatim.
5. Given process with both `script:` and `shell:` sections (only
   `shell:` applies), when translated, then `shell:` body is
   used with `!{expr}` interpolation.

---

## C. When Guard Evaluation (GAP 3)

### C1: Evaluate `when:` guard in follow loop

As a developer, I want processes with `when:` guards to skip
execution when the guard is false, so that conditional
processes work.

Use existing follow-loop + `TranslatePending`. At translate
time, if a process has a non-empty `When` field, mark the stage
as pending. When the follow loop resolves input bindings from
completed upstream jobs:

1. Evaluate the `when:` expression in Go using the existing
   Groovy evaluator with resolved input bindings.
2. If true: call `TranslatePending` to create the real process
   jobs and downstream wiring.
3. If false: emit nothing on output channels. Downstream
   processes simply don't execute for missing input
   combinations. No sentinel values.

```go
// EvalWhenGuard evaluates a process when: expression with
// the given input bindings.
func EvalWhenGuard(
    whenExpr string, bindings map[string]any,
    params map[string]any,
) (bool, error)
```

**Package:** `nextflowdsl/`, `cmd/`
**File:** `nextflowdsl/translate.go`, `cmd/nextflow.go`
**Test file:** `nextflowdsl/translate_test.go`,
`cmd/nextflow_test.go`

**Acceptance tests:**

1. Given process with `when: params.run_step` and params
   `{run_step: true}`, when guard evaluated, then result is
   true and jobs are created.
2. Given process with `when: params.run_step` and params
   `{run_step: false}`, when guard evaluated, then result is
   false and no jobs created for that process.
3. Given process with `when: id != 'skip'` and input binding
   `id='skip'`, when guard evaluated, then result is false.
4. Given process with `when: id != 'skip'` and input binding
   `id='keep'`, when guard evaluated, then result is true.
5. Given process with no `when:` guard, when translated, then
   existing behaviour preserved (jobs always created).
6. Given process A with `when:` guard returning false, and
   downstream process B depending on A's output, when A is
   skipped, then B receives no input from A and does not
   execute for those items.

---

## D. Accelerator Directive LSF Mapping (GAP 4)

### D1: Map `accelerator` to LSF cluster options

As a developer, I want `accelerator` directives mapped to LSF
GPU resource strings when the scheduler is LSF, so that GPU
jobs get proper resource requests.

When `TranslateConfig.Scheduler == "lsf"`:
- `accelerator 1` -> append
  `-R "select[ngpus>0] rusage[ngpus_physical=1]"` to
  `Requirements.Other["scheduler_misc"]`.
- `accelerator 2, type: 'nvidia-tesla-v100'` -> append
  `-R "select[ngpus>0] rusage[ngpus_physical=2]"`.
  (Type stored as informational warning; LSF uses physical
  GPU count, not model selection.)

When scheduler is not LSF, store with a warning.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `accelerator 1` and
   `Scheduler: "lsf"`, when translated, then
   `Requirements.Other["scheduler_misc"]` contains
   `select[ngpus>0] rusage[ngpus_physical=1]`.
2. Given process with `accelerator 2, type: 'nvidia-tesla-v100'`
   and `Scheduler: "lsf"`, when translated, then
   `Requirements.Other["scheduler_misc"]` contains
   `ngpus_physical=2`.
3. Given process with `accelerator 1` and `Scheduler: ""`,
   when translated, then `Requirements.Other["scheduler_misc"]`
   does not contain GPU options, and a warning is emitted.
4. Given process with both `accelerator` and `clusterOptions`,
   when translated with LSF, then both are merged into
   `Requirements.Other["scheduler_misc"]`.

---

## E. Arch Directive LSF Mapping (GAP 5)

### E1: Map `arch` to LSF cluster options

As a developer, I want `arch 'linux/x86_64'` mapped to LSF
architecture selection when the scheduler is LSF.

When `TranslateConfig.Scheduler == "lsf"`:
- `arch 'linux/x86_64'` ->
  `-R "select[type==X86_64]"` appended to
  `Requirements.Other["scheduler_misc"]`.

Map known arch strings to LSF type names:
- `linux/x86_64` -> `X86_64`
- `linux/aarch64` -> `AARCH64`

When scheduler is not LSF, store with a warning.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `arch 'linux/x86_64'` and
   `Scheduler: "lsf"`, when translated, then
   `Requirements.Other["scheduler_misc"]` contains
   `select[type==X86_64]`.
2. Given process with `arch 'linux/aarch64'` and
   `Scheduler: "lsf"`, when translated, then
   `Requirements.Other["scheduler_misc"]` contains
   `select[type==AARCH64]`.
3. Given process with `arch 'linux/x86_64'` and
   `Scheduler: ""`, when translated, then no LSF options
   emitted and a warning produced.

---

## F. Ext Directive and task.ext.* Resolution (GAP 6)

### F1: Resolve `task.ext.*` in script interpolation

As a developer, I want `task.ext.args` and other `task.ext.*`
references in script bodies resolved from the process's `ext`
directive values, so that nf-core modules work.

1. Collect `ext` values from: process-level `ext` directive
   (in `Process.Directives["ext"]`), config-level `ext` via
   `ProcessDefaults.Ext` and selector-scoped `ext`.
2. Merge: config `ext` values override process-level `ext` at
   the key level (Nextflow semantics).
3. If `ext` value is a `ClosureExpr`, evaluate at job-build
   time with per-item input bindings (same as `${meta.id}` in
   script body).
4. During script interpolation, when `${task.ext.args}` (or
   any `task.ext.*`) is encountered, resolve from the merged
   ext map. If not found, substitute empty string.

```go
func resolveExtDirective(
    proc *Process, defaults *ProcessDefaults,
    bindings []string, params map[string]any,
) (map[string]any, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `ext.args: '--verbose'` and script
   `cmd ${task.ext.args}`, when translated, then job `Cmd`
   contains `cmd --verbose`.
2. Given process with no `ext.args` and script
   `cmd ${task.ext.args}`, when translated, then job `Cmd`
   contains `cmd ` (empty substitution).
3. Given process with `ext.args: '--verbose'` and config
   selector `withLabel: 'foo'` setting `ext.args: '--quiet'`,
   when process has label `foo`, then job `Cmd` contains
   `cmd --quiet` (config overrides process).
4. Given process with `ext.prefix: { meta.id }` (closure) and
   input binding `meta=[id:'sample1']`, when translated, then
   `task.ext.prefix` resolves to `'sample1'`.
5. Given process with `ext.args: '--a'` and
   `ext.args2: '--b'`, and script `cmd ${task.ext.args}
   ${task.ext.args2}`, when translated, then job `Cmd`
   contains `cmd --a --b`.
6. Given process with `ext.args: '--quiet'` and config
   default `process { ext.args = '--verbose' }`, when
   translated, then `task.ext.args` resolves to
   `'--verbose'` (config default overrides process-level
   ext).

---

## G. Fair Directive Priority Mapping (GAP 7)

### G1: Map `fair` to job priority

As a developer, I want `fair true` to set incrementing job
priorities so that jobs with lower input index complete first.

When `fair true` on a process, set `Job.Priority` on each job
from that process: jobs with lower input index get higher
priority value. Priority = `255 - min(index, 254)`. This
ensures item 0 gets priority 255, item 1 gets 254, etc.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `fair true` and 3 input items, when
   translated, then job 0 has `Priority` == 255, job 1 has
   `Priority` == 254, job 2 has `Priority` == 253.
2. Given process with `fair true` and 300 input items, when
   translated, then job 254 has `Priority` == 1 and job 255+
   has `Priority` == 1 (clamped).
3. Given process with `fair false` or no `fair` directive, when
   translated, then all jobs have default `Priority` == 0.

---

## H. randomSample(n) Channel Operator (GAP 8)

### H1: Implement `randomSample` via PendingStage

As a developer, I want `randomSample(n)` to collect all
upstream items and randomly sample N, so that downstream
processes receive only the sampled items.

Use `PendingStage` / dynamic workflow. The pending stage:
1. Waits for all upstream items (via `awaitRepGrps`).
2. In `TranslatePending`, collects completed items.
3. Randomly samples N items (deterministic if seed provided:
   `randomSample(n, seed)`).
4. Creates downstream jobs for the N sampled items only.

If N >= total items, all items pass through.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`, `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/channel_test.go`,
`nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given channel with 10 items and `.randomSample(3)`, when
   resolved via PendingStage with completed items, then result
   has exactly 3 items.
2. Given channel with 10 items and `.randomSample(3, 42)`
   (seeded), when resolved twice with same seed, then same 3
   items selected both times.
3. Given channel with 2 items and `.randomSample(5)`, when
   resolved, then result has 2 items (all pass through).
4. Given channel with 0 items and `.randomSample(3)`, when
   resolved, then result has 0 items.

---

## I. errorStrategy 'finish' via Limit Groups (GAP 9)

### I1: Translate `errorStrategy 'finish'`

As a developer, I want `errorStrategy 'finish'` to let running
jobs complete but prevent new jobs for the failed process,
so that the workflow drains gracefully on failure.

At translate time: assign all jobs from a `finish`-strategy
process a unique limit group
`nf-finish-<process_name>-<run_id>`.

In the follow loop (`cmd/nextflow.go`):
1. After polling, check buried jobs.
2. For each buried job belonging to a `finish`-strategy process,
   set that process's limit group count to 0 via the wr client
   API. This prevents new jobs for that process from starting.
3. Other processes continue independently.
4. No separate monitor job needed.

```go
// finishStrategyLimitGroup returns the limit group name for
// a process with errorStrategy 'finish'.
func finishStrategyLimitGroup(
    processName, runID string,
) string
```

**Package:** `nextflowdsl/`, `cmd/`
**File:** `nextflowdsl/translate.go`, `cmd/nextflow.go`
**Test file:** `nextflowdsl/translate_test.go`,
`cmd/nextflow_test.go`

**Acceptance tests:**

1. Given process with `errorStrategy 'finish'`, when translated,
   then all jobs have a `LimitGroups` entry matching
   `nf-finish-<processName>-<runID>`.
2. Given process with `errorStrategy 'terminate'`, when
   translated, then no `nf-finish-` limit group is set.
3. Given process with `errorStrategy 'finish'` and a buried
   job detected in the follow loop, then the limit group
   count for that process is set to 0.
4. Given two processes A (finish) and B (terminate), when A
   has a buried job, then B's jobs continue unaffected.

---

## J. Process Config Defaults Full Propagation (GAP 10)

### J1: Extend config parsing for all directive types

As a developer, I want all process directives set in config
`process {}` blocks to propagate to matching processes, so
that nf-core pipelines that configure directives via config
work correctly.

Extend `parseProcessAssignment` to recognise and store all
directive types listed in `ProcessDefaults`:
`errorStrategy`, `maxRetries`, `maxForks`, `publishDir`,
`queue`, `clusterOptions`, `ext`, `containerOptions`,
`accelerator`, `arch`, `shell`, `beforeScript`,
`afterScript`, `cache`, `scratch`, `storeDir`, `module`,
`conda`, `spack`, `fair`, `tag`.

Directives not in the typed set go into
`ProcessDefaults.Directives` catch-all.

At translate time, apply config defaults to the process before
directive resolution. Selector-scoped (`withLabel:`,
`withName:`) overrides merge on top of defaults.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`, `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/config_test.go`,
`nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given config `process { errorStrategy = 'retry' }` and
   process with no `errorStrategy`, when translated, then
   process uses `'retry'`.
2. Given config `process { ext.args = '--verbose' }` and
   process with no `ext.args`, when translated, then
   `task.ext.args` resolves to `'--verbose'`.
3. Given config `process { withLabel: 'gpu' { accelerator = 1
   } }` and process labeled `gpu`, when translated with LSF
   scheduler, then GPU resources applied.
4. Given config `process { queue = 'long' }` and process with
   `queue 'short'`, when translated, then process-level
   `'short'` takes precedence over config default.
5. Given config `process { maxForks = 4 }`, when translated,
   then jobs get limit group capping concurrent jobs to 4.
6. Given config `process { ext.args = '--a' }` and selector
   `withName: 'FOO' { ext.args = '--b' }`, when process
   name is `FOO`, then `ext.args` is `'--b'` (selector
   overrides default).
7. Given config `process { fair = true }`, when translated,
   then all processes get priority-ordered jobs.
8. Given process with `queue 'short'`, config default
   `process { queue = 'long' }`, and selector
   `withName: 'FOO' { queue = 'priority' }` where process
   name is `FOO`, when translated, then `queue` is
   `'priority'` (selector wins over both process-level
   and config default).

---

## K. `new` Constructor Evaluation (GAP 11)

### K1: Evaluate common `new` constructors

As a developer, I want `new File(path)`, `new URL(str)`, etc.
evaluated, so that expressions using constructors produce
values instead of `UnsupportedExpr`.

Special-case these constructors in `evalNewExpr`:

| Constructor                     | Result              |
|---------------------------------|---------------------|
| `new File(path)`                | path string         |
| `new URL(str)`                  | URL string          |
| `new Date()`                    | current date string |
| `new BigDecimal(str)`           | float64             |
| `new BigInteger(str)`           | int64               |
| `new ArrayList(collection)`     | list copy           |
| `new HashMap(map)`              | map copy            |
| `new LinkedHashMap(map)`        | map copy            |
| `new Random()`                  | int64(0) seed       |

Unknown constructors remain `UnsupportedExpr`.

```go
func evalNewExpr(expr NewExpr, vars map[string]any) (any, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `new File('/data/test.txt')`, when evaluated, then
   result is `"/data/test.txt"`.
2. Given `new File('relative/path.txt')`, when evaluated, then
   result is `"relative/path.txt"`.
3. Given `new URL('https://example.com')`, when evaluated, then
   result is `"https://example.com"`.
4. Given `new Date()`, when evaluated, then result is a
   non-empty string.
5. Given `new BigDecimal('3.14')`, when evaluated, then result
   is float64 `3.14`.
6. Given `new BigInteger('42')`, when evaluated, then result is
   int64 `42`.
7. Given `new ArrayList([1, 2, 3])`, when evaluated, then
   result is `[1, 2, 3]` (list copy).
8. Given `new HashMap([a: 1, b: 2])`, when evaluated, then
   result is map `{a: 1, b: 2}` (map copy).
9. Given `new LinkedHashMap([x: 'y'])`, when evaluated, then
   result is map `{x: "y"}`.
10. Given `new Random()`, when evaluated, then result is a
    numeric value (seed).
11. Given `new UnknownClass()`, when evaluated, then result is
    `UnsupportedExpr`.

---

## L. Multi-Variable Assignment Evaluation (GAP 12)

### L1: Evaluate destructuring assignments

As a developer, I want `def (x, y) = [1, 2]` evaluated so that
`x` and `y` receive the correct values.

In `evalMultiAssignExpr`:
1. Evaluate RHS.
2. Assert RHS is a list.
3. Assign each variable the corresponding element by index.
4. If list is shorter than variables, extra variables get `null`.
5. If list is longer, extra elements ignored.

```go
func evalMultiAssignExpr(
    expr MultiAssignExpr, vars map[string]any,
) (any, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `def (x, y) = [1, 2]`, when evaluated, then `x` == 1
   and `y` == 2.
2. Given `(a, b, c) = ['foo', 'bar', 'baz']`, when evaluated,
   then `a` == `"foo"`, `b` == `"bar"`, `c` == `"baz"`.
3. Given `def (x, y) = [1]` (short list), when evaluated, then
   `x` == 1 and `y` == nil.
4. Given `def (x) = [1, 2, 3]` (long list), when evaluated,
   then `x` == 1 (extras ignored).
5. Given `def (a, b) = someFunc()` where `someFunc` returns
   `[10, 20]`, when evaluated, then `a` == 10 and `b` == 20.

---

## M. Additional Groovy Methods (GAP 13)

### M1: String methods

As a developer, I want additional string methods evaluated, so
that nf-core string processing patterns work.

Add to `evalStringMethodCall`:

| Method                               | Behaviour            |
|--------------------------------------|----------------------|
| `replaceFirst(pattern, replacement)` | regex first replace  |
| `padLeft(n, [char])`                | left-pad to width n  |
| `padRight(n, [char])`               | right-pad to width n |
| `capitalize()`                       | uppercase first char |
| `uncapitalize()`                     | lowercase first char |
| `isNumber()`                         | true if numeric      |
| `isInteger()`                        | true if integer      |
| `isLong()`                           | alias for isInteger  |
| `isDouble()`                         | true if float        |
| `isBigDecimal()`                     | alias for isDouble   |
| `toBoolean()`                        | "true"->true etc.    |
| `stripIndent()`                      | remove leading ws    |
| `readLines()`                        | split into lines     |
| `count(str)`                         | count occurrences    |
| `toLong()`                           | parse to int64       |
| `toDouble()`                         | parse to float64     |
| `toBigDecimal()`                     | alias for toDouble   |
| `eachLine(closure)`                  | iterate each line    |
| `execute()`                          | warn-only, returns   |
|                                      | UnsupportedExpr      |

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `'hello123'.replaceFirst('[0-9]+', 'X')`, when
   evaluated, then result is `"helloX"`.
2. Given `'42'.padLeft(5, '0')`, when evaluated, then result
   is `"00042"`.
3. Given `'42'.padLeft(5)`, when evaluated, then result is
   `"   42"` (space-padded).
4. Given `'hi'.padRight(5, '.')`, when evaluated, then result
   is `"hi..."`.
5. Given `'hello'.capitalize()`, when evaluated, then result
   is `"Hello"`.
6. Given `'Hello'.uncapitalize()`, when evaluated, then result
   is `"hello"`.
7. Given `'123'.isNumber()`, when evaluated, then result is
   true.
8. Given `'abc'.isNumber()`, when evaluated, then result is
   false.
9. Given `'42'.isInteger()`, when evaluated, then result is
   true.
10. Given `'3.14'.isInteger()`, when evaluated, then result is
    false.
11. Given `'42'.isLong()`, when evaluated, then result is
    true.
12. Given `'3.14'.isDouble()`, when evaluated, then result is
    true.
13. Given `'3.14'.isBigDecimal()`, when evaluated, then result
    is true.
14. Given `'true'.toBoolean()`, when evaluated, then result is
    true.
15. Given `'false'.toBoolean()`, when evaluated, then result is
    false.
16. Given `'  line1\n  line2'.stripIndent()`, when evaluated,
    then result is `"line1\nline2"`.
17. Given `'a\nb\nc'.readLines()`, when evaluated, then result
    is `["a","b","c"]`.
18. Given `'banana'.count('an')`, when evaluated, then result
    is 2.
19. Given `''.capitalize()`, when evaluated, then result is
    `""`.
20. Given `'100'.toLong()`, when evaluated, then result is
    int64(100).
21. Given `'3.14'.toDouble()`, when evaluated, then result is
    float64(3.14).
22. Given `'3.14'.toBigDecimal()`, when evaluated, then result
    is float64(3.14).
23. Given `'line1\nline2\nline3'.eachLine { it.toUpperCase() }`,
    when evaluated, then closure receives each line and result
    is `["LINE1","LINE2","LINE3"]`.
24. Given `'hello'.execute()`, when evaluated, then a warning
    is emitted and result is `UnsupportedExpr`.

### M2: List methods

As a developer, I want additional list methods evaluated, so
that nf-core list processing patterns work.

Add to `evalListMethodCall` and closure-based list methods:

| Method                     | Behaviour                    |
|----------------------------|------------------------------|
| `inject(init, closure)`   | fold/reduce                   |
| `withIndex()`             | pairs `[[elem,0],[elem,1]...]`|
| `indexed()`               | alias for withIndex           |
| `groupBy(closure)`        | group into map by key         |
| `countBy(closure)`        | count into map by key         |
| `count(value\|closure)`   | count matching elements       |
| `collectMany(closure)`    | flatMap                       |
| `collectEntries(closure)` | list to map                   |
| `transpose()`             | transpose list of lists       |
| `head()`                  | first element                 |
| `tail()`                  | all but first                 |
| `init()`                  | all but last                  |
| `pop()`                   | remove+return last            |
| `push(item)`              | append item (alias for add)   |
| `add(item)`               | append item                   |
| `addAll(items)`           | append all items              |
| `remove(index)`           | remove at index               |
| `contains(item)`          | membership test               |
| `intersect(other)`        | set intersection              |
| `disjoint(other)`         | test disjointness             |
| `toSet()`                 | unique set (deduplicated)     |
| `reverseEach(closure)`    | iterate in reverse, return    |
| `eachWithIndex(closure)`  | iterate with index, return    |
| `reverse()`               | reverse list                  |
| `sum()`                   | sum all elements              |
| `sum(closure)`            | sum mapped values             |
| `max()`/`max(closure)`    | maximum value                 |
| `min()`/`min(closure)`    | minimum value                 |
| `asType(Class)`           | type conversion (e.g. Set)    |
| `spread(closure)`         | apply closure to each element |

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `[1,2,3].inject(0) { acc, v -> acc + v }`, when
   evaluated, then result is 6.
2. Given `['a','b'].withIndex()`, when evaluated, then result is
   `[["a",0],["b",1]]`.
3. Given `['a','b'].indexed()`, when evaluated, then result is
   `[["a",0],["b",1]]`.
4. Given `[1,2,3,4].groupBy { it % 2 }`, when evaluated, then
   result is map `{0: [2,4], 1: [1,3]}`.
5. Given `['a','b','a','c'].countBy { it }`, when evaluated,
   then result is map `{a: 2, b: 1, c: 1}`.
6. Given `[1,2,3].count(2)`, when evaluated, then result is 1.
7. Given `[1,2,3].count { it > 1 }`, when evaluated, then
   result is 2.
8. Given `[[1,2],[3,4]].collectMany { it }`, when evaluated,
   then result is `[1,2,3,4]`.
9. Given `['a','b'].collectEntries { [it, it.toUpperCase()] }`,
   when evaluated, then result is map `{a: "A", b: "B"}`.
10. Given `[[1,2],[3,4],[5,6]].transpose()`, when evaluated,
    then result is `[[1,3,5],[2,4,6]]`.
11. Given `[1,2,3].head()`, when evaluated, then result is 1.
12. Given `[1,2,3].tail()`, when evaluated, then result is
    `[2,3]`.
13. Given `[1,2,3].init()`, when evaluated, then result is
    `[1,2]`.
14. Given `[1,2,3].contains(2)`, when evaluated, then result is
    true.
15. Given `[1,2,3].contains(4)`, when evaluated, then result is
    false.
16. Given `[1,2,3].intersect([2,3,4])`, when evaluated, then
    result is `[2,3]`.
17. Given `[1,2].disjoint([3,4])`, when evaluated, then result
    is true.
18. Given `[1,2].disjoint([2,3])`, when evaluated, then result
    is false.
19. Given `[1,2,2,3,3].toSet()`, when evaluated, then result
    has 3 elements (unique, order preserved).
20. Given `[].head()`, when evaluated, then result is nil.
21. Given `[1,2,3].reverse()`, when evaluated, then result is
    `[3,2,1]`.
22. Given `[1,2,3].sum()`, when evaluated, then result is 6.
23. Given `[3,1,2].max()`, when evaluated, then result is 3.
24. Given `['a','bb','ccc'].max { it.size() }`, when evaluated,
    then result is `"ccc"`.
25. Given `[3,1,2].min()`, when evaluated, then result is 1.
26. Given `[1,2,2,3].asType(Set)`, when evaluated, then result
    is deduplicated: `[1,2,3]`.
27. Given `[1,2,3].asType(UnknownType)`, when evaluated, then
    a warning is emitted and result is `UnsupportedExpr`.
28. Given `[1,2,3].spread { it * 2 }`, when evaluated, then
    result is `[2,4,6]`.
29. Given `[1,2,3].pop()`, when evaluated, then result is 3
    and list becomes `[1,2]`.
30. Given `[1,2].push(3)`, when evaluated, then list becomes
    `[1,2,3]`.
31. Given `[1,2].add(3)`, when evaluated, then list becomes
    `[1,2,3]`.
32. Given `[1].addAll([2,3])`, when evaluated, then list
    becomes `[1,2,3]`.
33. Given `[10,20,30].remove(1)`, when evaluated, then result
    is 20 and list becomes `[10,30]`.
34. Given `[1,2,3].reverseEach { it * 2 }`, when evaluated,
    then closure receives 3, 2, 1 in order and result is
    `[1,2,3]` (returns original list).
35. Given `['a','b'].eachWithIndex { val, i -> "$i:$val" }`,
    when evaluated, then closure receives `('a',0)` then
    `('b',1)` and result is `['a','b']` (returns original
    list).
36. Given `[1,2,3].sum { it * 2 }`, when evaluated, then
    result is 12.
37. Given `['ab','c','def'].min { it.size() }`, when
    evaluated, then result is `'c'`.

### M3: Map methods

As a developer, I want map method calls evaluated, so that
nf-core map processing patterns work.

Add `map[string]any` handling to `evalMethodCallExpr`:

| Method                     | Behaviour                    |
|----------------------------|------------------------------|
| `findAll(closure)`        | filter entries                |
| `find(closure)`           | first matching entry          |
| `any(closure)`            | true if any entry matches     |
| `every(closure)`          | true if all entries match     |
| `groupBy(closure)`        | group entries by key          |
| `collectEntries(closure)` | transform entries             |
| `plus(other_map)`         | merge maps                    |
| `minus(keys)`             | remove keys                   |
| `sort(closure)`           | sort entries                  |
| `inject(init, closure)`   | fold entries                  |
| `size()`                  | entry count                   |
| `isEmpty()`               | true if empty                 |
| `keySet()`                | list of keys                  |
| `values()`                | list of values                |
| `containsKey(key)`        | key membership                |
| `containsValue(val)`      | value membership              |
| `subMap(keys)`            | sub-map for given keys        |
| `collect(closure)`        | transform to list             |
| `each(closure)`           | iterate, return map           |
| `getOrDefault(key, def)`  | lookup with default           |

```go
func evalMapMethodCall(
    receiver map[string]any, method string,
    args []any, expr MethodCallExpr,
    vars map[string]any,
) (any, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `[a:1, b:2, c:3].findAll { k, v -> v > 1 }`, when
   evaluated, then result is map `{b: 2, c: 3}`.
2. Given `[a:1, b:2].any { k, v -> v > 1 }`, when evaluated,
   then result is true.
3. Given `[a:1, b:2].every { k, v -> v > 0 }`, when evaluated,
   then result is true.
4. Given `[a:1, b:2].every { k, v -> v > 1 }`, when evaluated,
   then result is false.
5. Given `[a:1].plus([b:2])`, when evaluated, then result is
   map `{a: 1, b: 2}`.
6. Given `[a:1, b:2, c:3].minus(['b'])`, when evaluated, then
   result is map `{a: 1, c: 3}`.
7. Given `[a:1, b:2].size()`, when evaluated, then result is 2.
8. Given `[:].isEmpty()`, when evaluated, then result is true.
9. Given `[a:1, b:2].keySet()`, when evaluated, then result is
   a list containing `"a"` and `"b"`.
10. Given `[a:1, b:2].values()`, when evaluated, then result is
    a list containing 1 and 2.
11. Given `[a:1, b:2].containsKey('a')`, when evaluated, then
    result is true.
12. Given `[a:1, b:2].containsKey('c')`, when evaluated, then
    result is false.
13. Given `[a:1, b:2, c:3].subMap(['a','c'])`, when evaluated,
    then result is map `{a: 1, c: 3}`.
14. Given `[a:1, b:2].collect { k, v -> "$k=$v" }`, when
    evaluated, then result is a list of `["a=1","b=2"]`.
15. Given `[a:1, b:2].inject(0) { acc, k, v -> acc + v }`,
    when evaluated, then result is 3.
16. Given `[a:1, b:2].getOrDefault('c', 99)`, when evaluated,
    then result is 99.
17. Given `[a:1, b:2].getOrDefault('a', 99)`, when evaluated,
    then result is 1.
18. Given `[a:1, b:2, c:3].find { k, v -> v == 2 }`, when
    evaluated, then result is entry `b=2`.
19. Given `[a:1, b:2, c:1].groupBy { k, v -> v }`, when
    evaluated, then result is map `{1: {a:1, c:1}, 2: {b:2}}`.
20. Given `[a:1, b:2].collectEntries { k, v -> [k, v * 2] }`,
    when evaluated, then result is map `{a: 2, b: 4}`.
21. Given `[b:2, a:1].sort { a, b -> a.key <=> b.key }`, when
    evaluated, then result is ordered map `{a: 1, b: 2}`.
22. Given `[a:1, b:2].containsValue(2)`, when evaluated, then
    result is true.
23. Given `[a:1, b:2].containsValue(3)`, when evaluated, then
    result is false.
24. Given `[a:1, b:2].each { k, v -> }`, when evaluated, then
    result is the original map `{a: 1, b: 2}`.

### M4: Number methods

As a developer, I want number methods evaluated, so that
numeric expressions in nf-core work.

Add number type handling to `evalMethodCallExpr`:

| Method                | Behaviour               |
|-----------------------|-------------------------|
| `abs()`               | absolute value          |
| `round()`             | round to nearest int    |
| `intdiv(n)`           | integer division        |
| `toInteger()`         | convert to int          |
| `toLong()`            | convert to int64        |
| `toDouble()`          | convert to float64      |
| `toBigDecimal()`      | convert to float64      |

```go
func evalNumberMethodCall(
    receiver any, method string, args []any,
) (any, error)
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `(-5).abs()`, when evaluated, then result is 5.
2. Given `(3.7).round()`, when evaluated, then result is 4.
3. Given `(3.2).round()`, when evaluated, then result is 3.
4. Given `(7).intdiv(2)`, when evaluated, then result is 3.
5. Given `(3.14).toInteger()`, when evaluated, then result is
   3.
6. Given `(42).toDouble()`, when evaluated, then result is
   float64(42.0).
7. Given `(0).abs()`, when evaluated, then result is 0.
8. Given `(42).toLong()`, when evaluated, then result is
   int64(42).
9. Given `(3.14).toBigDecimal()`, when evaluated, then result
   is float64(3.14).

---

## N. Publish Section + Output Block Translation (GAP 14)

### N1: Parse `output {}` block targets

As a developer, I want the `output {}` block parsed
structurally to extract named targets with `path` and `index`,
so that publish wiring can use them.

Parse `Workflow.OutputBlock` (raw body from strict3) into
`[]*OutputTarget`. Extract `path` and `index` from each named
target. Other properties (mode, overwrite, tags) ignored.

Only static string paths supported; closure-valued paths produce
a warning and the target is skipped.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given `output { samples { path 'results/fastq' } }`, when
   parsed, then 1 target with `Name` == `"samples"` and
   `Path` == `"results/fastq"`.
2. Given `output { samples { path 'fastq'; index { path
   'index.csv' } } }`, when parsed, then target has
   `IndexPath` == `"index.csv"`.
3. Given `output { }` (empty), when parsed, then 0 targets.
4. Given `output { a { path 'x' }; b { path 'y' } }`, when
   parsed, then 2 targets.
5. Given `output { a { path { meta -> "${meta.id}" } } }`, when
   parsed, then warning emitted and target skipped.

### N2: Translate `publish:` wiring with output targets

As a developer, I want `publish:` assignments wired to output
targets, so that workflow outputs are copied to publish paths.

For each `WFPublish` in the workflow:
1. Look up `Target` in parsed output block targets.
2. Get the publish path from the target.
3. At translate time, for final jobs producing items in the
   source channel, add output-copying commands (same mechanism
   as `publishDir`) using the target's path.
4. If `IndexPath` is set, create a final aggregator job that
   writes a TSV index file after all items are published.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given workflow with `publish:\nsamples = ch_out` and
   `output { samples { path 'results/fastq' } }`, when
   translated, then final jobs include a publish command
   copying to `results/fastq`.
2. Given output target with `index { path 'index.csv' }` and
   2 published items, when translated, then an aggregator job
   is created that writes TSV to `index.csv`.
3. Given `publish:` referencing a target not in `output {}`,
   when translated, then a warning emitted and publish skipped.
4. Given workflow with no `publish:` section, when translated,
   then no publish-related jobs added.

---

## O. Assert and Throw Evaluation (GAP 15)

### O1: Evaluate `assert` and `throw` statements

As a developer, I want `assert expr` and `throw new
Exception('message')` evaluated at translation time, so that
compile-time-resolvable assertions produce warnings.

- `assert expr`: evaluate expression. If false/null, emit
  translation warning with assertion message (or default). Do
  not halt translation. If expression references only constants
  (params, literals) and fails, emit error-level warning.
- `assert expr : 'message'`: use the message string in the
  warning.
- `throw new Exception('msg')`: if reached during evaluation,
  emit warning with message. Surrounding try/catch handles
  routing.

```go
func evalAssertStmt(
    expr Expr, message Expr, vars map[string]any,
) error

func evalThrowStmt(
    expr Expr, vars map[string]any,
) error
```

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

**Acceptance tests:**

1. Given `assert true`, when evaluated, then no warning
   emitted.
2. Given `assert false`, when evaluated, then a warning is
   emitted containing "Assertion failed".
3. Given `assert false : 'x must be positive'`, when evaluated,
   then warning contains `"x must be positive"`.
4. Given `assert params.x > 0` with params `{x: 5}`, when
   evaluated, then no warning.
5. Given `assert params.x > 0` with params `{x: -1}`, when
   evaluated, then warning emitted (compile-time constant
   known false).
6. Given `throw new RuntimeException('bad input')`, when
   evaluated in a try/catch block, then catch clause handles
   it and warning contains `"bad input"`.
7. Given `throw new Exception('msg')` outside try/catch, when
   evaluated, then warning emitted with `"msg"`.

---

## Implementation Order

### Phase 1: Groovy Methods and Evaluation (K1, L1, M1-M4, O1)

New constructor evaluation, multi-variable assignment, all
Groovy methods (string, list, map, number), assert/throw.
Pure evaluator extensions with no translate dependencies.
All items can be implemented in parallel.

### Phase 2: Simple Script Translations (A1, B1)

Stub section translation (trivial body swap) and shell section
`!{expr}` interpolation. Depend only on existing evaluator
(enhanced by Phase 1). Can be parallel.

### Phase 3: Ext Directive + Config Propagation (F1, J1)

Ext directive resolution and full config-to-process directive
propagation. F1 depends on evaluator for closure-valued ext.
J1 extends config parsing. Can be parallel.

### Phase 4: LSF Directives + Fair + Finish (D1, E1, G1, I1)

Accelerator/arch LSF mapping, fair priority, finish strategy
limit groups. All translate-time directive processing. Depend
on J1 for config-level directive values. Can be parallel within
phase.

### Phase 5: When Guard + randomSample (C1, H1)

Dynamic workflow features using follow loop + PendingStage.
When guard depends on evaluator (Phase 1). randomSample uses
PendingStage collection pattern. Can be parallel.

### Phase 6: Publish + Output Block (N1, N2)

Parse output block targets, wire publish assignments. Depends
on all prior phases being stable. N2 depends on N1.
Sequential within phase.

---

## Appendix: Key Decisions

1. **`Scheduler` field on TranslateConfig.** Populated by
   querying wr manager via client API in `cmd/nextflow.go`.
   Unit tests set it directly. Defaults to empty (non-LSF).
2. **`StubRun` flag approach.** Simple boolean on
   TranslateConfig. Body swap at translate time, not parse.
3. **Shell `!{expr}` interpolation.** Separate regex pass:
   `!{([^}]+)}`. Evaluate each match, leave `${...}` alone.
4. **When guard in Go evaluator.** No lightweight evaluator
   job -- evaluate directly in the follow loop using resolved
   bindings. Limited to Go evaluator capabilities.
5. **Accelerator/arch LSF strings.** Only emit LSF-specific
   cluster options when `Scheduler == "lsf"`. Append to
   existing `scheduler_misc` value if present.
6. **Ext closure evaluation.** Per-item bindings, same pattern
   as script interpolation. Critical for nf-core compatibility.
7. **Fair via Priority.** `255 - min(index, 254)` gives higher
   priority to earlier items. Clamped to avoid underflow.
8. **randomSample via PendingStage.** Collector waits for all
   upstream via `awaitRepGrps`, then samples in Go.
9. **errorStrategy 'finish' via limit groups.** Follow loop
   detects buried jobs and zeroes the limit group. No separate
   monitor job. Per-process scope.
10. **ProcessDefaults hybrid.** Typed fields for directives
    the translator uses; `Directives map[string]any` catch-all
    for warn-only directives.
11. **New constructor special-casing.** Only listed constructors
    implemented. Others remain `UnsupportedExpr`.
12. **Multi-variable assignment.** Only list destructuring.
    Short list -> nil for missing vars. Long list -> extras
    ignored.
13. **Map method support.** New `evalMapMethodCall` function
    handles `map[string]any` receivers. Closure-based methods
    (findAll, collect, etc.) receive `(key, value)` pairs.
14. **Number method support.** New `evalNumberMethodCall`
    handles int/int64/float64 receivers.
15. **Output block minimal parse.** Extract `path` and `index`
    from named targets. Closure paths produce warnings.
    Index format is TSV.
16. **Publish wiring.** Same mechanism as `publishDir`. Final
    jobs get copy commands. Index aggregator is a separate job.
17. **Assert/throw are warnings.** Never halt translation.
    Error-level only for compile-time-constant failures.
18. **Backward compatibility.** No changes to existing `Process`
    struct layout for existing fields. `ProcessDefaults` gains
    fields but existing code that only reads `Cpus`/`Memory`/
    `Time`/`Disk`/`Container`/`Env` is unaffected.
19. **Testing.** GoConvey, synthetic minimal test cases. Each
    method/feature gets independent tests. See go-conventions.
20. **Config ext merging.** Key-level merge: selector `ext`
    overrides specific keys in defaults, other keys survive.
21. **Config default ext overrides process-level ext.**
    Unlike regular directives (where process-level wins over
    config defaults), `ext` follows Nextflow semantics where
    config `ext` values override process-level `ext` values
    at the key level. Config selectors then override config
    defaults.
