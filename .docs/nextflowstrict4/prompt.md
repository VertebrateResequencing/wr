# Nextflow DSL2 Translation Completeness — Close Remaining Translation Gaps

## Background

We have a pure-Go Nextflow DSL2 parser in the `nextflowdsl/` package which
parses `.nf` workflow files and config files, translating them into
`jobqueue.Job` slices that wr executes. A `cmd/nextflow.go` adds CLI commands.

Four prior specs (`nextflowdsl/spec.md`, `nextflowstrict/spec.md`,
`nextflowstrict2/spec.md`, `nextflowstrict3/spec.md`) have been implemented
and merged. After nextflowstrict3, the parser accepts all valid DSL2 syntax
without error. However, several parsed constructs are stored but not
translated, and some evaluation gaps remain. This spec closes every
remaining *translation and evaluation* gap that has a clean path to
implementation.

See `.docs/nextflow/unsupported.md` for features that cannot be supported
and `.docs/nextflow/future.md` for features deferred until demand exists.

## Design principles (from prior specs — maintain these)

- Pure Go, no CGo dependencies beyond what's already in go.mod.
- No intermediate files written to disk.
- No separate state store — crash recovery uses wr's existing job persistence.
- All changes confined to existing `nextflowdsl/` and `cmd/` packages.
- Tests use GoConvey style.
- Constructs that cannot be meaningfully translated to wr jobs should be
  parsed without error and produce clear warnings, never parse failures.

## Gap Analysis

### GAP 1: `stub:` section translation

The `stub:` section defines a lightweight script that runs instead of the
real `script:` during dry-run/stub-run mode. nf-core modules nearly all
have stubs. Currently parsed and stored but never translated.

**Implementation:** Add a `--stub-run` flag to `cmd/nextflow.go`. When set,
the translator substitutes the `stub:` body for the `script:` body in every
process that has one. Processes without a stub section use their normal
script. The flag simply controls which script body becomes the job's `Cmd`.

### GAP 2: `shell:` section translation

The `shell:` section is an alternative to `script:` that uses `!{var}`
interpolation instead of `${var}`. This lets `${bash_var}` pass through
to bash untouched while `!{nf_var}` is replaced with Nextflow variable
values.

**Implementation:** When a process uses a `shell:` section, the translator
applies a different interpolation pass: scan for `!{expr}` patterns,
evaluate and substitute them, and leave `${...}` untouched. The result
becomes the job's `Cmd`.

Supports both string and list forms. In list form
(`shell: ['bash', '-ue', '!{cmd}']`), the first element is the shell
interpreter, middle elements are flags, and the last element is the
script body. Only the script body gets `!{expr}` interpolation. The
interpreter and flags are used to construct the execution command.

### GAP 3: `when:` section translation

The `when:` section is a Groovy boolean guard. If false, the process is
skipped for that input. Used in nf-core and many real pipelines.

**Implementation:** Use the existing follow-loop + `TranslatePending`
mechanism. At translate time, if a process has a `when:` guard:
1. Create a lightweight "evaluator" job that receives the input values
   and evaluates the `when:` expression.
2. If true, the follow loop's TranslatePending call creates the real
   process job (plus its downstream wiring).
3. If false, emit nothing on the process's output channels. Downstream
   processes simply don't execute for the missing input combinations.
   No sentinel values or transitive skipping.

### GAP 4: `accelerator` directive — LSF mapping

When wr manager is in LSF mode, map the `accelerator` directive to LSF
cluster options. Example: `accelerator 1, type: 'nvidia-tesla-v100'`
becomes `-R "select[ngpus>0] rusage[ngpus_physical=1]"` appended to
`clusterOptions`.

If not in LSF mode, store with a warning.

### GAP 5: `arch` directive — LSF mapping

When wr manager is in LSF mode, map the `arch` directive to LSF cluster
options. Example: `arch 'linux/x86_64'` becomes an appropriate
`-R "select[type==X86_64]"` appended to `clusterOptions`.

If not in LSF mode, store with a warning.

### GAP 6: `ext` directive — task.ext support

The `ext` directive is a namespace for arbitrary user-defined key-value
metadata. Used heavily in nf-core for `task.ext.args`, `task.ext.args2`,
`task.ext.prefix`, etc.

**Implementation:**
1. Parse `ext` directive values (already done in strict3) including nested
   keys: `ext.args`, `ext.prefix`, etc.
2. During script interpolation, resolve `task.ext.args` (and other
   `task.ext.*` references) to the values set by the `ext` directive.
3. Config-level `ext` settings via process selectors
   (`withLabel:`, `withName:`) must propagate to the process's ext map.
4. The process config `ext` merging follows Nextflow semantics: config
   `ext` values override process-level `ext` values.

### GAP 7: `fair` directive — job priority

When `fair true`, guarantee output ordering by setting incrementing job
priorities on the jobs created from that process. Jobs with lower input
index get higher priority, so they are scheduled first and tend to
complete in order.

### GAP 8: `randomSample(n)` channel operator

Currently warning-only pass-through.

**Implementation:** Use `TranslatePending` / dynamic workflow. Create a
special collector job that:
1. Waits for all upstream items (via dependency groups).
2. Randomly samples N items from the collected set.
3. Dynamically creates N downstream jobs, one per sampled item.

### GAP 9: `errorStrategy 'finish'`

Currently falls back to `terminate` semantics.

**Implementation:** When `errorStrategy 'finish'` is set on a process:
1. At translate time, assign all jobs from that process a unique limit
   group (e.g. `nf-finish-<process_name>-<run_id>`).
2. The existing `--follow` polling loop detects buried jobs and sets the
   relevant per-process limit group count to 0. This prevents new jobs
   for that process from starting while letting currently running jobs
   (both from this process and other processes) complete.
3. Other processes in the workflow continue independently.
4. No separate monitor job needed — the follow loop handles detection.

### GAP 10: Process config defaults — full propagation

Currently only `cpus`, `memory`, `time`, `disk`, `container` are extracted
from config-level `process {}` blocks. All other process directives set in
config must also propagate.

**Implementation:** Extend the config parser to extract ALL directive
settings from `process {}` blocks (both default and selector-scoped via
`withLabel:` / `withName:`). These include: `errorStrategy`, `maxRetries`,
`maxForks`, `publishDir`, `queue`, `clusterOptions`, `conda`, `container`,
`containerOptions`, `ext`, `label`, `tag`, `beforeScript`, `afterScript`,
`cache`, `scratch`, `storeDir`, `module`, `shell`, `accelerator`, `arch`,
and any other directive. Apply them to matching processes during
translation with the existing config-override mechanism.

### GAP 11: `new` constructor evaluation

Currently parsed (strict3) but not evaluated. Special-case the commonly
used constructors:

- `new File(path)` — evaluate to the path string (files in Nextflow are
  essentially path references in our translation model)
- `new URL(str)` — evaluate to the URL string
- `new Date()` — evaluate to current date string
- `new BigDecimal(str)` / `new BigInteger(str)` — evaluate to numeric value
- `new ArrayList(collection)` — evaluate to list copy
- `new HashMap(map)` — evaluate to map copy
- `new LinkedHashMap(map)` — evaluate to map copy
- `new Random()` — evaluate to a random seed object (for `.nextInt()` etc.)

Unknown constructors produce `UnsupportedExpr` as before.

### GAP 12: Multi-variable assignment evaluation

Currently parsed (strict3) but not evaluated. Implement evaluation of:

- `def (x, y) = [1, 2]` — destructuring a list into named variables
- `(x, y) = [1, 2]` — without `def` prefix

The RHS must evaluate to a list. Each variable is assigned the
corresponding list element by index.

### GAP 13: Additional Groovy methods used in nf-core

Implement all methods commonly used in nf-core pipelines that are not yet
in the evaluator. Based on nf-core module analysis, these include:

**String methods:**
- `replaceFirst(pattern, replacement)` — regex replace first match
- `padLeft(n, [char])` / `padRight(n, [char])` — padding
- `capitalize()` — capitalize first letter
- `uncapitalize()` — lowercase first letter
- `isNumber()` / `isInteger()` / `isLong()` / `isDouble()` / `isBigDecimal()` — type checking
- `toBoolean()` — string to boolean
- `stripIndent()` — remove leading whitespace
- `readLines()` — split into lines list
- `eachLine(closure)` — iterate lines
- `count(str)` — count occurrences
- `execute()` (on File paths) — not supported, warn

**List methods:**
- `inject(initial, closure)` — fold/reduce (alias for reduce)
- `withIndex()` — pairs each element with its index
- `indexed()` — alias for withIndex
- `eachWithIndex(closure)` — iterate with index
- `reverseEach(closure)` — iterate in reverse
- `groupBy(closure)` — group elements by key (if not already present)
- `countBy(closure)` — count elements by key
- `count(value|closure)` — count matching elements
- `collectMany(closure)` — alias for flatMap
- `collectEntries(closure)` — transform list to map
- `transpose()` — transpose list of lists
- `head()` / `tail()` / `init()` — list slicing (first, rest, all-but-last)
- `pop()` / `push(item)` — stack operations
- `add(item)` / `addAll(items)` / `remove(index)` — mutation
- `contains(item)` — membership test
- `intersect(other)` — set intersection
- `disjoint(other)` — test disjointness
- `toSet()` — convert to unique set
- `asType(Class)` — type conversion
- `spread(closure)` — spread operator support

**Map methods:**
- `findAll(closure)` — filter entries
- `find(closure)` — find first matching entry
- `any(closure)` / `every(closure)` — predicate testing
- `groupBy(closure)` — group entries
- `collectEntries(closure)` — transform entries
- `plus(other_map)` — merge maps
- `minus(keys)` — remove keys
- `sort(closure)` — sort entries
- `inject(initial, closure)` — fold

**Number methods:**
- `abs()` — absolute value
- `round()` — rounding
- `intdiv(n)` — integer division
- `toInteger()` / `toLong()` / `toDouble()` / `toBigDecimal()`

### GAP 14: `publish:` workflow section translation

Currently parsed and stored (strict3) but not translated. The `publish:`
section maps channels to output targets defined in the `output {}` block.

**Implementation:**
1. Parse the `output {}` block structurally (currently stored as raw text).
   Parse `path` and `index` from each named output target — minimal
   depth. Other properties (mode, overwrite, tags, etc.) are ignored.
2. For each `publish:` assignment (`target = source_channel`), look up the
   target in the parsed output block to get the publish path.
3. At translate time, for each published channel, add output-copying
   commands (same mechanism as `publishDir`) to the final jobs that produce
   items in that channel. The publish path comes from the output block
   definition.
4. If an output target has a nested `index` block, produce a TSV index
   file after all items are published (via a final aggregator job).

### GAP 15: `assert` and `throw` statement evaluation

Currently parsed (strict3) but only warned on.

**Implementation:**
- `assert expr` — evaluate the expression. If it evaluates to false/null,
  emit a translation warning with the assertion message (or a default).
  Do not halt translation (the pipeline may still be valid at runtime
  with different values). If the expression references only compile-time
  constants (params, literals), and fails, emit an error-level warning.
- `throw new Exception('message')` — if reached during evaluation (e.g.
  inside a catch block or conditional), emit a warning with the message.
  The surrounding try/catch already handles routing.

## Notes

- Phase ordering: translation changes that depend on parsing (already done
  in strict3) come first. Complex dynamic-workflow features (when, finish,
  randomSample) in later phases.
- `stub:` and `shell:` are simple script-body substitutions — minimal risk.
- `ext` is critical for nf-core compatibility — highest priority after
  simple items.
- `when:` guard uses the existing TranslatePending/dynamic mechanism,
  not a new execution model.
- `errorStrategy 'finish'` uses limit groups — no new wr API needed.
- `randomSample` uses dynamic workflow — same pattern as other pending
  operators.
- Config process defaults: extend existing config extraction, no new
  architecture.
- Groovy method additions are incremental — each method is a small,
  independent function in groovy.go.
- `new` constructor special-casing: only the listed constructors are
  implemented. Others remain UnsupportedExpr.
- Multi-variable assignment: only list destructuring, not arbitrary
  Groovy destructuring patterns.
- `publish:` + `output {}`: implement together since they are tightly
  coupled.
- `assert`/`throw`: evaluation is best-effort. Runtime-dependent
  assertions produce warnings, not translation failures.
- All existing tests must continue to pass.
- Acceptance tests use synthetic minimal test cases, not real nf-core
  snippets.
- Parameter override precedence: CLI > config > script (maintaining
  existing behaviour).
- `accelerator` and `arch` LSF mapping: only active when wr's scheduler
  mode is LSF. Other modes store with a warning. Add a `Scheduler string`
  field to `TranslateConfig`, populated by `cmd/nextflow.go` from wr
  manager's configured scheduler. Only emit LSF-specific clusterOptions
  when `Scheduler == "lsf"`.
- `fair` via priority: sets `Priority` field on wr jobs. Lower input
  index = higher priority value.
- `ext` config merging: key-level merge — process selectors
  (`withLabel:`, `withName:`) can set `ext` values that override specific
  keys in the process-level defaults. Other process-level ext keys survive.
  Matches Nextflow semantics.
- `when:` guard false → emit nothing on output channels. Downstream
  processes simply don't execute for missing input combinations. No
  sentinel values or transitive skipping. Uses existing follow-loop +
  TranslatePending mechanism: the evaluator is a no-op job whose output
  indicates pass/skip; the follow loop reads the result and calls
  TranslatePending to create or skip the real job.
- `when:` guard evaluation happens in Go during the follow loop, using
  the existing Groovy evaluator. Once input bindings are resolved from
  completed upstream jobs, the follow loop evaluates the `when:`
  expression and decides whether to create the real process jobs.
  Limited to Go Groovy evaluator capabilities.
- ProcessDefaults expansion (GAP 10): hybrid approach — typed fields for
  directives the translator actually uses (errorStrategy, maxRetries,
  maxForks, publishDir, queue, clusterOptions, ext, containerOptions,
  accelerator, arch), generic `Directives map[string]any` catch-all for
  warn-only directives.
- `errorStrategy 'finish'`: per-process scope matching Nextflow semantics.
  Only the process with `finish` stops creating new jobs when one of its
  jobs fails; other processes continue independently. Each process with
  `finish` gets its own limit group. The existing `--follow` polling loop
  detects buried jobs and sets the relevant limit group count to 0.
  Detection logic lives in `cmd/nextflow.go`'s follow loop; limit-group
  metadata established at translate time in `nextflowdsl/translate.go`.
- Scheduler type detection for GAPs 4/5: query the running wr manager via
  client API to get its configured scheduler. No `--scheduler` CLI flag
  needed. If manager is unreachable (unit tests, offline translation),
  `TranslateConfig.Scheduler` defaults to empty string — same as non-LSF
  mode. Unit tests set Scheduler directly on TranslateConfig.
- Closure-valued `ext` directives: evaluate at job-build time with
  per-item input bindings. Each job gets the closure evaluated with its
  specific input values (meta, reads, etc.), same as how `${meta.id}` in
  the script body is resolved. Matches Nextflow semantics, critical for
  nf-core.
- Groovy methods (GAP 13): implement all listed methods — no prioritised
  subset.
- `shell:` section supports both string and list forms. List form:
  `['bash', '-ue', '!{cmd}']` — first element is interpreter, last is
  script body with `!{expr}` interpolation. List form replaces the
  default `set -euo pipefail` header entirely; the specified interpreter
  and flags are used directly (e.g. `bash -ue -c '<script>'`).
- `output {}` block: minimal parse depth — extract `path` and `index`
  from each target. Other properties ignored. Index format is TSV. Only
  static string paths supported; closure-valued paths produce a warning
  and the target is skipped.
- `randomSample` collector uses existing rep group gating:
  `awaitRepGrps` in PendingStage. Follow loop only calls
  TranslatePending once all upstream jobs complete.
