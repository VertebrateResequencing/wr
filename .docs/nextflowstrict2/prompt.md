# Nextflow DSL2 Parser Completeness: Gap-Filling Spec

## Background

The `nextflowdsl` package in wr is a pure-Go Nextflow DSL2 parser and
translator. It parses `.nf` workflow files and `nextflow.config` files,
translating them into `jobqueue.Job` slices that wr executes. Three specs
already exist:

- `.docs/nextflowdsl/spec.md` — original parser/translator
- `.docs/nextflowstrict/spec.md` — strict parsing extensions (tuple I/O,
  emit labels, includeConfig, expanded operators, gap fixes A1-A7)
- `.docs/nextflowstd/spec.md` — stdout/stderr capture

This spec fills the remaining gaps between our implementation and the
full official Nextflow DSL2 specification, as documented at
https://nextflow.io/docs/latest/reference/syntax.html,
https://nextflow.io/docs/latest/reference/process.html,
https://nextflow.io/docs/latest/reference/channel.html, and
https://nextflow.io/docs/latest/reference/operator.html.

## General Principles (from existing specs, must be maintained)

1. **Pure Go.** No shelling out to Nextflow or Java. No CGo.
2. **No intermediate files.** Channel data flows via file paths in working
   directories, not serialised channel state on disk.
3. **Override=0 always.** wr's resource learning is the primary mechanism;
   Nextflow directives are initial hints.
4. **Parse-first strategy.** Parser accepts valid DSL2 without error even
   if translation is incomplete. Untranslatable constructs produce
   warnings, not errors.
5. **GoConvey testing.** All unit tests use GoConvey per go-conventions.
6. **CwdMatters=true** with deterministic per-process working directories.

## Current Implementation Status

### What works (already implemented)

**Parsing:**
- Process blocks with input/output/script/stub/exec/shell/when sections
- Tuple I/O declarations with val/path/file/env/stdin/stdout/eval types
- Emit, optional, topic, arity qualifiers on output declarations
- Workflow blocks with take/main/emit/publish sections
- Subworkflow definitions
- Include statements (local + GitHub remote modules)
- Top-level function definitions (stored, not evaluated)
- Channel factories: of, value, empty, fromPath, fromFilePairs
- 33 channel operators (13 fully translated, 20 parse-as-passthrough)
- Shebang lines, block comments, feature flag assignments
- Top-level output blocks (parsed and skipped)
- Triple-quoted strings (both `'''` and `"""`)
- Process directives: cpus, memory, time, disk, container, maxForks,
  errorStrategy, maxRetries, publishDir, env
- All unrecognised directives warned and skipped

**Config:**
- params, process, profiles scopes
- includeConfig with relative path resolution and circular detection
- Unknown config scopes skipped with warning (docker, singularity,
  conda, env, executor, manifest, timeline, report, trace, dag,
  notification, weblog, tower)

**Translation:**
- Static DAG → jobqueue.Job with dep_grps, deps, rep_grp, req_grp
- Dynamic workflows via TranslatePending
- Resource mapping (cpus→Cores, memory→RAM, time→Time, disk→Disk)
- Container wrapping (docker/singularity)
- maxForks → LimitGroups
- errorStrategy → Retries + failure behaviours
- publishDir → OnSuccess copy/move/link behaviours
- env → EnvOverride
- Tuple I/O binding and wiring
- Emit label resolution
- Channel factory resolution (item cardinality → job count)
- Operator cardinality effects (collect, first, last, take, filter,
  map, flatMap, mix, join, groupTuple)

**Groovy expression evaluator:**
- Integer, string, boolean literals
- params.* and variable references with dotted paths
- Binary arithmetic (+, -, *, /)
- Comparisons (>, <) — integer only
- String interpolation (${varname})

### Gap Analysis: What Is Missing

The gaps below are ordered by impact on real-world pipeline compatibility.
Each gap includes what the official Nextflow spec requires and what our
implementation currently does.

---

### GAP 1: Process Selectors in Config (CRITICAL)

**Official spec:** Config files use `process { withLabel: 'LABEL' { ... } }`
and `process { withName: 'NAME' { ... } }` to apply directive overrides
to matching processes. The `label` directive on processes is the primary
mechanism for resource allocation in nf-core pipelines.

**Current state:** Not implemented. The config parser handles only flat
`process { cpus = X }` defaults. The `label` directive on processes is
silently ignored.

**Impact:** Almost every nf-core pipeline uses `withLabel` selectors.
Without this, config-driven resource allocation fails silently — all
processes get default resources.

**Required:**
- Parse `label` directive on processes (store as `[]string`)
- Parse `withLabel:` and `withName:` selectors in config `process {}`
  blocks, including glob/regex patterns
- At translate time, match process labels/names against selectors and
  apply the most specific matching directive overrides

---

### GAP 2: Missing Channel Operators (HIGH)

**Official spec:** 48+ operators. We support 33 (13 fully, 20 passthrough).

**Missing operators that appear in real pipelines:**
- `cross` — Cartesian-like join of two channels by key
- `splitJson` — Splits JSON content into channel items
- `splitText` — Splits text file into lines/chunks
- `buffer` — Buffers items into groups
- `collate` — Groups items into fixed-size sublists
- `until` — Takes items until condition is true
- `subscribe` — Terminal operator, triggers side-effects
- `sum` — Reduces to sum
- `min` / `max` — Reduces to min/max
- `randomSample` — Random sampling
- `merge` — Deprecated but still used in older pipelines
- `toInteger` — Deprecated but still used

**Required:** Parse all official operators without error. Operators that
cannot be meaningfully translated should pass through with a warning
(existing pattern). Operators where translation IS feasible should be
translated:
- `cross` → cartesian product of two channels
- `splitJson`/`splitText` → item expansion
- `buffer`/`collate` → grouping (similar to groupTuple)
- `min`/`max`/`sum` → reduction to single item
- `until` → take-while semantics

---

### GAP 3: Missing Channel Factories (HIGH)

**Official spec:** 13 factories. We support 5.

**Missing factories:**
- `Channel.fromList(list)` — Creates channel from a list; commonly used
- `Channel.fromSRA(id)` — Fetches from NCBI SRA; niche but used in
  bioinformatics pipelines
- `Channel.topic(name)` — Topic-based pub/sub channel; new feature
- `Channel.watchPath(glob)` — Watches filesystem for new files
- `Channel.from(items)` — Deprecated but still in older pipelines
- `Channel.fromLineage(query)` — New lineage-based factory
- `Channel.interval(duration)` — Timer-based factory

**Required:** Parse all factories without error. Translate where feasible:
- `Channel.fromList` → same as `Channel.of` (expand list items)
- `Channel.from` → same as `Channel.of` (deprecated alias)
- Others → warn as untranslatable (real-time/network factories)

---

### GAP 4: Groovy Expression Limitations (HIGH)

**Official spec:** Full Groovy expression language in directives and
closures.

**Current state:** Only supports integer literals, string literals,
booleans, params.* references, variable references, binary arithmetic
(+, -, *, /), and integer comparisons (>, <).

**Missing expression features that appear in real pipelines:**
- Ternary operator: `condition ? a : b` — very common in directives like
  `memory { task.attempt > 1 ? '16 GB' : '8 GB' }`
- Elvis operator: `x ?: default` — common for fallback values
- Comparison operators: `==`, `!=`, `>=`, `<=`
- Logical operators: `&&`, `||`, `!`
- Method calls: `.trim()`, `.size()`, `.toInteger()`, `.split()`,
  `.toLowerCase()`, `.toUpperCase()`
- Null literal and null-safe navigation: `x?.property`
- List literals: `[1, 2, 3]`
- Map literals: `[key: 'value']`
- Index/subscript access: `list[0]`, `map['key']`
- String methods in interpolation: `"${x.trim()}"`
- `as` type casts: `x as Integer`
- Ranges: `1..10`
- `task.attempt` references in error retry logic

**Required:** Extend `EvalExpr` to handle:
- Ternary and elvis operators
- All comparison and logical operators
- Common string/list/map methods
- List and map literal construction
- Index/subscript access
- `task.*` references (attempt, cpus, memory, etc.)

Closures that still cannot be evaluated should continue to return
`UnsupportedExpr` error with the original expression text and fall back
to defaults with Override=0.

---

### GAP 5: Conditional Logic in Workflow Blocks (HIGH)

**Official spec:** Workflow blocks support `if`/`else` conditional
process invocation:
```nextflow
workflow {
    if (params.aligner == 'bwa') {
        BWA(reads)
    } else {
        BOWTIE(reads)
    }
}
```

**Current state:** Not handled. Workflow blocks only parse process calls
and channel operations.

**Required:** Parse `if`/`else`/`else if` in workflow blocks. At
translate time, evaluate the condition against resolved params and only
emit jobs for the matching branch. Conditions that cannot be statically
evaluated produce a warning and both branches are emitted.

---

### GAP 6: Map and List Literals (MEDIUM)

**Official spec:** Groovy list `[1, 2, 3]` and map `[key: 'value']`
literals appear in directive values, channel factory arguments, and
operator closures.

**Current state:** Not parsed. Appear as `UnsupportedExpr`.

**Examples in real pipelines:**
- `publishDir path: [mode: 'copy', path: '/results']`
- `ext args: ['--threads', task.cpus]`
- `Channel.of([id:'s1', reads:'/data/s1.fq'])` (map-based channel items)

**Required:** Parse list and map literals as expression types. Add
`ListExpr` and `MapExpr` AST nodes. Evaluate them in the Groovy
evaluator.

---

### GAP 7: Additional Process Directives (MEDIUM)

**Official spec:** 35+ directives. We handle 10, ignore the rest.

**Directives that should be handled for correct execution:**
- `label` — CRITICAL (see Gap 1)
- `tag` — Used for logging/identification; store and use in RepGroup
- `cache` — Controls caching strategy; relevant for resume semantics
- `scratch` — Use local scratch space; affects CWD behaviour
- `storeDir` — Alternative to publishDir for cached outputs
- `beforeScript` / `afterScript` — Prepend/append to process script
- `module` — Environment module loading (HPC systems)
- `queue` — HPC queue/partition selection
- `clusterOptions` — Additional HPC scheduler options
- `executor` — Per-process executor override
- `debug` — When true, forward stdout to terminal
- `secret` — Secret variable injection

**Directives that can remain silently ignored:**
- `accelerator` — GPU support (future work)
- `arch` — CPU architecture constraint
- `array` — Job arrays
- `fair` — Fair scheduling
- `machineType` — Cloud machine type
- `maxErrors` — Max total errors
- `maxSubmitAwait` — Submission backpressure
- `penv` — SGE parallel environment
- `pod` — Kubernetes pod options
- `resourceLabels` — Cloud resource labels
- `resourceLimits` — Resource limit caps
- `spack` — Spack environment
- `stageInMode` / `stageOutMode` — File staging strategy
- `containerOptions` — Additional container runtime options

**Required:** Parse `label`, `tag`, `cache`, `scratch`, `storeDir`,
`beforeScript`, `afterScript`, `module`, `queue`, `clusterOptions`,
`executor`, `debug`, `secret` as directives with stored values. Translate
those that map to wr job fields. The rest remain warned-and-skipped.

---

### GAP 8: Config Environment Scope (MEDIUM)

**Official spec:** The `env {}` config scope exports environment variables
to all tasks. The `docker`/`singularity`/`apptainer` scopes control
container runtime settings.

**Current state:** Top-level `env {}` is skipped. Container scopes
are skipped. Process-level `env {}` IS handled.

**Required:**
- Parse `env {}` scope and extract variables into Config
- At translate time, merge env scope variables into every job's
  environment
- Parse relevant container scope settings (`docker.enabled`,
  `singularity.enabled`, `apptainer.enabled`) to auto-detect container
  runtime if not specified via CLI flag

---

### GAP 9: Process Selector Patterns (MEDIUM)

**Official spec:** Config process selectors support:
- `withName: 'exactName'`
- `withName: 'glob*Pattern'`
- `withName: '~regexPattern'`
- `withLabel: 'labelName'`
- `withLabel: 'glob*'`
- `withLabel: '~regex'`
- Nesting: `withLabel: 'big' { withName: 'foo' { ... } }`
- Generic selectors: `process { cpus = 2 }` (applies to all)

**Required:** Full selector matching at translate time. Specificity
ordering: process-level directive > withName > withLabel > generic
default.

---

### GAP 10: Closure Parameter Handling (MEDIUM)

**Official spec:** Closures in operators take parameters:
```nextflow
ch.map { item -> [item.id, item.files] }
ch.filter { id, files -> files.size() > 0 }
```

**Current state:** Closures are captured as raw text. The Groovy
evaluator cannot execute them.

**Required:** Parse closure parameters (before `->`) and closure body.
For simple closures (field access, method calls), evaluate them during
channel resolution. For complex closures, continue to warn and pass
through.

---

### GAP 11: Workflow-Level Features (LOW)

**Official spec:**
- `workflow.onComplete { ... }` / `workflow.onError { ... }` — lifecycle
  handlers
- `workflow.output { ... }` — new output definition syntax
- Named workflow `main:` entry point selection

**Current state:** Output blocks parsed and skipped. Lifecycle handlers
not parsed.

**Required:** Parse `workflow.onComplete` and `workflow.onError` blocks
and skip them (wr handles lifecycle differently). Continue to skip
`output {}` blocks.

---

### GAP 12: Deprecation Compatibility (LOW)

**Official spec:** Several constructs are deprecated but still appear in
existing pipelines:
- `Channel.from()` — replaced by `Channel.of()`
- `Channel.create()` — removed
- `into` keyword for channel output — DSL1 only
- `set` keyword for channel assignment — DSL1 only
- `merge` operator — deprecated
- `countFasta`, `countFastq`, `countJson`, `countLines` — deprecated
- `toInteger` — deprecated

**Required:** Parse deprecated constructs that still appear in DSL2 code
with a deprecation warning. Return clear error for DSL1-only constructs.

## Notes

- Process selectors (withLabel/withName) are the single most impactful
  gap. Every nf-core pipeline relies on them for resource allocation.
- The Groovy expression evaluator gaps compound: many directive values
  use ternary expressions, method calls, or `task.attempt` references
  that cannot currently be evaluated.
- For operators and factories that cannot be meaningfully translated
  (e.g. watchPath, interval, subscribe), the parse-and-warn strategy
  is correct. Users can still run simpler pipelines.
- Conditional workflow logic (if/else) is common in nf-core and other
  production pipelines. Without it, multi-path workflows fail to parse.
- The implementation should continue to be incremental: parse everything
  possible without error, translate what we can, warn on the rest.
- This spec covers ALL 12 gaps in one comprehensive spec (not split
  into multiple specs).
- Process selectors support BOTH glob and regex patterns, matching
  official Nextflow syntax: `withName: 'foo*'` (glob) and
  `withName: '~foo.*'` (regex with `~` prefix).
- Groovy evaluator adds ternary (`?:`), elvis (`?:`), all comparison
  operators (`==`, `!=`, `>=`, `<=`), logical operators (`&&`, `||`),
  and common methods (`.trim()`, `.size()`, `.toInteger()`,
  `.toLowerCase()`, `.toUpperCase()`).
- When if/else conditions in workflow blocks cannot be statically
  evaluated, emit BOTH branches as jobs with a warning about
  non-deterministic branching.
- New operators (cross, splitJson, splitText, buffer/collate,
  min/max/sum) get translation semantics where feasible. Operators
  where translation is unclear (subscribe, until, randomSample)
  remain parse-and-warn.
- Config selector matching uses Nextflow's specificity ordering:
  generic defaults < withLabel < withName < process-level directive.
  When multiple selectors match, their directives are MERGED with
  later/more-specific selectors overriding earlier ones.
- beforeScript/afterScript run INSIDE the container with the main
  process script. Container wrapping applies to the entire
  before+main+after block as one unit.
- `task.attempt` and other `task.*` references in directives are
  evaluated STATICALLY at translate time. `task.attempt` defaults to
  1 for the initial job. wr's resource learning (Override=0) handles
  subsequent attempts.
- DSL1-only constructs (Channel.create(), `into` keyword, `set`
  keyword for assignment) produce a parse ERROR. Deprecated-but-
  still-DSL2 constructs (Channel.from(), merge operator, toInteger)
  produce a deprecation WARNING and continue parsing.
- Arity qualifiers on tuple elements (e.g. `arity: '1..*'`) are
  parsed and accepted but ignored for translation purposes — they
  don't affect how outputs are grouped into jobs.
- New operator cardinality matches official Nextflow semantics:
  cross(ch1,ch2) → N×M jobs (Cartesian product), buffer(n)/
  collate(n) → ceil(N/n) jobs (one per group), min/max/sum → 1 job
  (reduction).
- When multiple config selectors of the same specificity match a
  process, LAST-match-wins (later selectors override earlier).
  Nested selectors like `withLabel('big') { withName('foo') { } }`
  create a COMPOSITE selector (must match both label and name).
- Groovy method call support includes chaining (e.g.
  `text.trim().toLowerCase()`) and covers common string methods
  (trim, size, toInteger, toLowerCase, toUpperCase, contains,
  startsWith, endsWith, replace, split, substring) and list methods
  (size, isEmpty, first, last, collect, flatten). No regex methods.
- Pattern matching for withName/withLabel: glob uses shell-style
  patterns (*, ?, [abc]). Regex uses `~` prefix (matches Go
  regexp syntax). Case-sensitive matching (matches Nextflow).
- Non-evaluable if/else conditions: emit BOTH branches with a
  warning. Downstream jobs get distinct dep_grps so they don't
  conflict on output paths (each branch uses a separate CWD subtree).
  Both branches actually execute (wr has no conditional execution);
  unnecessary jobs complete quickly with no effect.
- `tag` directive: stored on process AST but does NOT affect RepGroup
  naming (which is deterministic). Included in job metadata for
  logging/identification purposes only.
- `cache` directive: stored and ignored for translation. wr handles
  caching via its own job deduplication. Warn if set.
- `scratch` directive: stored and ignored. Our CwdMatters=true with
  deterministic paths serves a similar purpose. Warn if set.
- `storeDir` directive: treated similarly to publishDir (copy outputs
  to the specified directory). Unlike publishDir, storeDir also
  checks if outputs already exist and skips execution if so — this
  maps to checking if the output files exist before job creation.
- Config env scope merge: process-level env overrides config-level env
  for the same key (more specific wins). Both are merged into the
  final job environment.
- Selector nesting: limited to one level of nesting (withLabel
  containing withName, or vice versa). Same-type nesting
  (withLabel inside withLabel) is treated as AND (process must
  have both labels). Arbitrary depth is not required.
- cross operator output: each combination produces a list `[item1,
  item2]` passed to downstream. N×M input items → N×M downstream
  jobs. buffer(n) produces ceil(N/n) groups, each group passed as a
  list to one downstream job. min/max/sum produce a single value
  passed to one downstream job.
