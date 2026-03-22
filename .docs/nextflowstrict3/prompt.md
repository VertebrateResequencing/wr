# Nextflow DSL2 Parser Completeness — Close All Remaining Gaps

## Background

We have a pure-Go Nextflow DSL2 parser in the `nextflowdsl/` package which
parses `.nf` workflow files and config files, translating them into
`jobqueue.Job` slices that wr executes. A `cmd/nextflow.go` adds CLI commands.

Three prior specs (`nextflowdsl/spec.md`, `nextflowstrict/spec.md`,
`nextflowstrict2/spec.md`) have been implemented and merged. The current
parser handles the most common DSL2 constructs, but a systematic comparison
against the official Nextflow syntax reference
(https://nextflow.io/docs/latest/reference/syntax.html) reveals many gaps
that will cause parse errors or incorrect behaviour on real-world pipelines.

This spec must close **every remaining gap** so that any syntactically valid
DSL2 workflow file (excluding very new v25.10+ typed-process syntax) can be
parsed without error, and correctly translated where applicable.

## Design principles (from prior specs — maintain these)

- Pure Go, no CGo dependencies beyond what's already in go.mod.
- No intermediate files written to disk.
- No separate state store — crash recovery uses wr's existing job persistence.
- All changes confined to existing `nextflowdsl/` and `cmd/` packages.
- Tests use GoConvey style.
- Constructs that cannot be meaningfully translated to wr jobs should be
  parsed without error and produce clear warnings, never parse failures.

## Gap Analysis vs Nextflow Reference

### GAP 1: Missing process directives (parse.go)

The parser currently handles: `cpus`, `memory`, `time`, `disk`, `container`,
`errorStrategy`, `maxRetries`, `maxForks`, `publishDir`, `label`, `tag`,
`beforeScript`, `afterScript`, `module`, `cache`, `env`.

The Nextflow reference defines these additional directives that the parser
must accept (store in `Process.Directives` map or dedicated fields):

- `accelerator` — GPU requests (int + named options like `type:`)
- `arch` — CPU architecture specification
- `array` — job array size (int)
- `clusterOptions` — native scheduler options (string or string list)
- `conda` — Conda package specification
- `containerOptions` — extra container engine options
- `debug` — forward stdout (bool); also `echo` (deprecated alias)
- `executor` — override executor for the process
- `ext` — namespace for custom key-value directives
- `fair` — guarantee output order (bool)
- `machineType` — cloud machine type string
- `maxErrors` — total error count across instances (int)
- `maxSubmitAwait` — max time in queue before failure
- `penv` — SGE parallel environment
- `pod` — Kubernetes pod configuration (map or list of maps)
- `queue` — scheduler queue name(s)
- `resourceLabels` — custom name-value pairs for cloud resources
- `resourceLimits` — caps on cpus/memory/time/disk
- `scratch` — use local scratch directory (bool/string)
- `secret` — expose secrets as env vars
- `shell` — custom shell command (string list) — this is the directive,
  distinct from the `shell:` section
- `spack` — Spack package specification
- `stageInMode` — how input files are staged (symlink/link/copy/rellink)
- `stageOutMode` — how output files are staged out
- `storeDir` — permanent cache directory

All of these should be parsed and stored without error. Those with direct
wr equivalents should be translated; others stored for potential future use.

### GAP 2: `each` input qualifier (parse.go)

The `each` input qualifier repeats the process for each item in the
associated channel. Syntax: `each val(x)` or `each path(x)`. Our parser
does not currently recognise this. It needs to be parsed and stored as a
flag on the Declaration, and translation needs to create a cross-product
of jobs.

### GAP 3: `eval` output qualifier (parse.go)

The `eval(command)` output runs a command after the main script and captures
its stdout. This needs parsing and a translation approach (append command
to the script wrapper and capture its output).

### GAP 4: Groovy expression gaps (groovy.go)

Current support: `+`, `-`, `*`, `/`, `==`, `!=`, `<`, `<=`, `>`, `>=`,
`&&`, `||`, `!`, ternary, elvis, null-safe, method calls, closures,
list/map literals, index access, string interpolation, `as` cast.

Missing operators that appear in real workflows:

- `%` modulo
- `**` power/exponentiation
- `in` / `!in` membership testing (e.g. `x in ['a','b']`)
- `instanceof` / `!instanceof`
- `=~` regex find, `==~` regex match
- `<=>` spaceship/three-way comparison
- `..` inclusive range, `..<` exclusive range
- `<<` left shift (often used for append), `>>` right shift, `>>>` unsigned
- `&` bitwise AND, `^` bitwise XOR, `|` bitwise OR
- `~` bitwise NOT (unary)
- Spread-dot operator `*.property` → `collect { it.property }`

Missing expression features:
- Slashy strings `/pattern/` (used for regex)
- `new` constructor calls, e.g. `new File('x')` or `new Date()`
- Multi-variable declaration: `def (x, y) = [1, 2]`
- Multi-variable assignment: `(x, y) = [1, 2]`

For **parsing without error**, all of these must at minimum be recognised.
For evaluation, implement the ones commonly used in real pipelines:
`%`, `in`, `=~`/`==~`, `..`/`..<` ranges, spread-dot.

### GAP 5: Statement types (parse.go)

The parser needs to handle these statement types in workflow bodies and
function definitions, at minimum to skip them without error:

- `assert expr : 'message'`
- `throw new Exception('...')`
- `try { ... } catch (ExType e) { ... }`
- `return expr` in closures/functions
- `for (x in collection) { ... }` loops
- `while (cond) { ... }` loops
- `switch/case` blocks

These appear in real nf-core pipelines, particularly in custom functions
and library code. They don't need full execution semantics — just enough
to parse without error and skip gracefully.

### GAP 6: `params {}` block syntax (parse.go)

Modern Nextflow (strict syntax) uses `params { input: Path; save: Boolean
= false }` block syntax with typed declarations. The parser must accept
this block syntax in addition to the legacy `params.x = y` form. Types
can be stored as strings or ignored; default values must be captured.

### GAP 7: Enum and record types (parse.go)

Nextflow 26.04+ adds `enum Day { MONDAY, TUESDAY, ... }` and
`record FastqPair { id: String; fastq_1: Path; ... }`. These need to be
parsed and stored (at minimum skipped cleanly). Enum values referenced as
`Day.MONDAY` need to resolve without error.

### GAP 8: Output block (parse.go)

The top-level `output { samples { path 'fastq'; index { path 'index.csv' }
} }` block is currently skipped. This should be parsed properly (even if
not translated) to support workflows that use the new output definition
syntax.

### GAP 9: Workflow block sections (parse.go)

Missing sections in workflow blocks:
- `publish:` — publish statements like `messages = messages`
- `onComplete:` — statements executed on workflow completion
- `onError:` — statements executed on workflow error

These need parsing and storage. Translation can emit warnings for
`onComplete`/`onError` since wr doesn't have lifecycle hooks.

### GAP 10: Pipe operator in workflow bodies

Nextflow supports the pipe operator `|` for chaining:
`channel.of(1,2,3) | foo | bar | view`

This is equivalent to `bar(foo(channel.of(1,2,3))).view()`. The parser
must recognise pipe expressions in workflow bodies and convert them to
the equivalent Call chain.

### GAP 11: Variable declarations and assignments in workflow main

Real workflows freely use:
```groovy
workflow {
    main:
    ch_input = Channel.fromPath(params.input)
    filtered = ch_input.filter { it.size() > 0 }
    PROCESS_A(filtered)
    result = PROCESS_A.out.bam
    PROCESS_B(result)
}
```

The parser needs to handle arbitrary variable assignments (not just
process calls) in workflow bodies, track them as named channel references,
and wire them correctly during translation.

### GAP 12: Channel operators — full behaviour implementation

Currently many operators are pass-through with warnings. For correct
translation, the following need real implementation in `channel.go`:

**Cardinality-changing operators:**
- `combine(other, [by: n])` — cross product, optionally keyed
- `concat(ch1, ch2, ...)` — ordered concatenation
- `flatten()` — flatten nested lists
- `transpose([by: n])` — un-group tuples
- `unique()` / `distinct()` — deduplication

**Multi-channel operators:**
- `branch { criteria }` — split into named output channels
- `multiMap { criteria }` — map to multiple named output channels

**Data-processing operators:**
- `splitCsv([header: true, sep: ','])` — CSV splitting
- `splitJson([path: '...'])` — JSON splitting
- `splitText([by: n])` — line-based text splitting
- `splitFasta([by: n, record: [...]])` — FASTA splitting
- `splitFastq([by: n, pe: true])` — FASTQ splitting
- `collectFile([name: '...'])` — collect items to file(s)
- `ifEmpty(value)` — default for empty channel
- `toList()` / `toSortedList()` — collect all items into one list
- `count([filter])` — count items
- `reduce(accumulator, closure)` — fold/accumulate

For initial implementation, data-processing operators that depend on file
content (splitCsv, splitFasta, etc.) can produce items at runtime through
`TranslatePending`. The key requirement is that the parser accepts them
and translation doesn't crash.

### GAP 13: Config — complete scope handling

The config parser skips many scopes with warnings. It should properly
parse (even if only storing as raw key-value maps) all standard scopes:
- `conda {}` — Conda settings
- `dag {}` — DAG visualisation settings
- `executor {}` — executor-level settings (needed for translation)
- `manifest {}` — pipeline metadata
- `notification {}` — notification settings
- `report {}` — report settings
- `timeline {}` — timeline settings
- `tower {}` / `wave {}` — Seqera Platform settings
- `trace {}` — trace report settings
- `weblog {}` — weblog settings

The `executor {}` scope is particularly important because it can set
default executor type, queue names, and resource limits that affect job
translation.

### GAP 14: Dynamic directives

Nextflow allows directives to use closures that reference task properties:
```groovy
process foo {
    memory { 2.GB * task.attempt }
    errorStrategy { task.exitStatus in [137,140] ? 'retry' : 'terminate' }
    cpus { params.cpus ?: 4 }
}
```

The parser stores these as raw text. The evaluator needs to handle
`task.attempt`, `task.cpus`, etc. for dynamic resource allocation on retry.

### GAP 15: `each` cross-product in translation

When a process has `each` inputs, wr must create N×M jobs where N is the
count from the regular input channel and M is the count from the `each`
channel. This is a cross-product expansion in `translate.go`.

## Notes

- Phase ordering: parse completeness first (all constructs parse without
  error), then translation semantics. The spec sections should be ordered
  so that parsing gaps come before translation/behaviour gaps.
- Cardinality modelling: compile-time for simple operators (flatten,
  unique, concat, count); TranslatePending with warnings for data-dependent
  operators (splitCsv, splitFasta, etc.) and combine with unknown keys.
- Groovy evaluation scope: implement `%`, `in`/`!in`, `=~`/`==~`, `..`/`..<`
  ranges, and common closure patterns. Other operators (`**`, `<=>`, bitwise,
  `instanceof`, shift) need parsing only; evaluation emits warnings.
- Error handling: parse-permissive. Accept anything that looks like valid
  Nextflow, warn liberally, only fail on tokeniser errors. Unknown
  directives, statement types, and operators should never cause parse errors.
- AST modelling: new process directives stay in the existing
  `map[string]any` Directives field. No new typed struct fields for
  directives.
- All 15 gaps addressed in a single comprehensive spec (not split across
  phases).
- `each` input cross-product: computed at compile time deterministically
  (N × M jobs), not deferred via TranslatePending. Parser creates a
  separate InputDecl with an EachQualifier flag; translator enumerates
  the cross-product at translate time.
- `params {}` block: store type annotations in AST for completeness, but
  don't validate types at translate time. Last-seen wins if both
  `params {}` and `params.x = y` exist.
- Groovy operators: parse ALL operators so files never fail to parse.
  Evaluate where possible (`%`, `in`, `=~`, ranges, spread-dot etc.),
  warn on operators that can't be meaningfully evaluated.
- Unsupported statement types (try/catch, for/while, switch/case, assert,
  throw): parse and store as AST nodes. Translator evaluates simple cases
  (e.g. simple for loops, return statements), warns on complex ones.
- Config `executor {}` and other new scopes: parse and extract key fields
  (executor type, queue, clusterOptions). Store as raw `map[string]any`
  for potential scheduling hints.
- Variable assignment tracking in workflow main: only track assignments
  whose RHS is a recognised Channel expression or process output. Plain
  variable assignments (e.g. `x = 42`) are silently ignored for wiring.
- `params {}` block vs legacy `params.x = y`: last-seen wins. If block
  syntax appears after legacy assignments, block values override; if legacy
  appears after block, legacy overrides.
- `branch` operator: parse fully but warn; emit all items to a single
  channel (current pass-through behaviour). No separate dep groups per
  branch.
- Acceptance tests use synthetic minimal test cases, not real nf-core
  snippets.
- Parameter override precedence: CLI > config > script (matching existing
  behaviour). CLI/config params always win over script-level params.
- Channel assignment disambiguation in workflow main: syntactic pattern
  matching only — RHS contains `Channel.*`, `process.out`, or a known
  channel variable name. No type inference system.
- Dynamic directive evaluation: evaluate with `task.attempt=1` (and other
  defaults) at translate time. Re-evaluate on retry if possible. If a
  `task.*` property is missing, use a sensible default.
- `each` file staging: each N×M job gets its own unique working directory,
  consistent with the existing per-job CWD model.
- Operator chain cardinality: best-effort approximation. Use
  TranslatePending when cardinality is unknown at compile time.
- This spec targets Nextflow DSL2 as documented at
  https://nextflow.io/docs/latest/reference/syntax.html (circa 2026).
- Typed processes (`nextflow.preview.types`, v25.10+) are out of scope
  for now — they are too new and experimental. The parser should skip
  `stage:` and `topic:` sections without error if encountered.
- The `plugin/nf-*` include source scheme should be recognised in parsing
  but can produce a warning since we don't run JVM plugins.
- For operators that change channel cardinality (branch, combine, etc.),
  the translation must model the cardinality change to create the correct
  number of downstream jobs. Where the exact count is unknowable at
  compile time (e.g. splitCsv of a runtime-determined file), use the
  existing `TranslatePending` mechanism.
- Backward compatibility: all existing tests must continue to pass.
- The spec should be organised into logical sections (A, B, C, ...) with
  user stories and acceptance tests for each gap.
