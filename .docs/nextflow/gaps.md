# Nextflow Features — Gaps

These Nextflow features are not yet covered in `supported.md`,
`unsupported.md`, or `future.md`. They could potentially be implemented.

## Input Qualifiers

### `stdin`

The `stdin` input qualifier is parsed but not correctly translated.
In real Nextflow, `stdin` pipes the input value to the process's
standard input. Our implementation does not pipe to stdin — the
value is only available through variable interpolation. Scripts that
read from standard input (e.g. `read line`, `cat`) will not receive
the expected data.

### `env(x)` — Not a Real Environment Export

The `env(x)` input qualifier is parsed and the received value is made
available for Nextflow script interpolation (e.g. `echo $x` in the
script block resolves to the value). However, in real Nextflow,
`env FOO` exports the variable as a real shell environment variable
(via `export FOO=value` prepended to the script). Our implementation
does not export it — tools or subprocesses that read `$FOO` from the
process environment (rather than through Nextflow string interpolation)
will not see the value.

## Output Qualifiers

### `env(x)` — Runtime Capture

The `env(x)` output qualifier is parsed and falls through to static
variable lookup. In real Nextflow, `env(FOO)` captures the value of
the shell environment variable `FOO` **after the script runs**. Our
implementation resolves `x` at translate time using the input binding
value, so the output is whatever the variable was set to before
execution — not the runtime value. Scripts that compute and export new
environment variables will have wrong output values.

## Groovy Statements

### `while` loops

The `WhileStmt` AST type exists but the parser and evaluator do not
implement it — a `while (cond) { ... }` loop will cause a parse error.
`for` loops and recursion cover most use-cases in practice, but `while`
does appear in some bespoke pipelines.

### `continue` statement

The `continue` keyword is not implemented — no AST type, parser rule, or
evaluator handling exists. A `continue` in a `for` loop will cause a
parse error. Most loops do not need `continue` but it appears in some
bespoke pipelines.

## Config Scopes

### `workflow`

Top-level workflow execution settings including `failOnIgnore` (exit
non-zero when `ignore` error strategy is used) and output publishing
options: `mode` (copy/move/link/symlink/rellink), `overwrite`,
`contentType`, `storageClass`, `tags`, `copyAttributes`, `enabled`,
`ignoreErrors`. New in Nextflow 24.10.

nf-core pipelines are beginning to migrate from `publishDir` to the
workflow output block model. The `workflow.output` sub-settings will
become increasingly common.

The `publish:` workflow section and `output {}` block target paths are
supported for translation. The `workflow` config scope's output sub-settings
(mode, overwrite, etc.) are not yet mapped — wr uses copy mode for output
publishing. Implementing mode/overwrite could be done by extending the
existing publish translation to respect these settings.

## Path/File Output Options

Output declarations like `path '*.bam', type: 'file'` accept several
named options beyond `emit:` and `optional:`. The following cause
**parse errors** when encountered:

- `type` — restrict to `'file'`, `'dir'`, or `'any'` (default: `'any'`).
  Occasionally used in nf-core to filter directory outputs.
- `glob` — enable glob pattern matching (default: `true`). Rarely set
  explicitly since the default is already true.
- `followLinks` — follow symbolic links when collecting outputs.
- `hidden` — include hidden files (dotfiles) in glob results.
- `includeInputs` — include input files in output collection.
- `maxDepth` — maximum directory traversal depth for glob.
- `arity` — min/max file count constraint (e.g. `arity: '1..*'`).
  Parsed within tuple elements but the value is discarded; errors at the
  top-level declaration.

Implementing `type` would be the highest-value addition since it appears
in some nf-core modules. The others are rarely used.

## Path/File Input Options

Input declarations like `path x, stageAs: 'custom_name'` accept:

- `name` / `stageAs` — rename the staged input file. Cause **parse
  errors**. Used occasionally in nf-core when a process needs input files
  with specific names.
- `arity` — min/max multiplicity. Parsed but discarded in tuple elements;
  errors at top-level.

## publishDir Options

The `publishDir` directive currently supports `path`, `mode`, and
`pattern`. The following options cause **parse errors**:

- `saveAs` — closure to rename published files. Used in many nf-core
  pipelines to customise output file names.
- `overwrite` — whether to overwrite existing files (default: `true`).
- `enabled` — conditionally enable/disable publishing.
- `failOnError` — fail the task if publishing fails.

The cloud-only options (`tags`, `contentType`, `storageClass`) are not
relevant to wr and are listed in `unsupported.md`.

## Channel Operators — `cross` Behaviour

In Nextflow, `cross(other)` emits every pairwise combination of two
channels **for which the pair has a matching key**. By default the key
is the first element of each tuple (or the value itself for non-tuple
items). An optional closure can be provided to customise the matching
key.

In our implementation, `cross` produces a **full Cartesian product** —
every left item is paired with every right item regardless of keys.
This gives incorrect results when items from the two channels have
different first elements. Custom key closures are also not supported.

## Channel Operators — Type Conversion

The `toInteger()`, `toLong()`, `toFloat()`, and `toDouble()` channel
operators are recognised and parsed without error, but they are no-ops —
items pass through without actual type conversion. In real Nextflow these
convert each channel item to the respective numeric type. If upstream
items are already the correct type (common in practice) the end result
is the same, but string-to-number conversion will not happen.

## Channel Operators — Missing Closure/Condition Variants

Several channel operators accept closure or condition arguments in
Nextflow that are not implemented. The basic (no-argument) form works
correctly but the closure/condition variants silently fall back to
default behaviour or error:

- `filter(literal)` / `filter(regex)` / `filter(Type)` — only closure
  predicates work; literal, regex, and type-qualifier filter criteria
  are not evaluated and items pass through unfiltered.
- `first(condition)` — only `first()` with no arguments works; regex,
  type-qualifier, and predicate conditions are not supported.
- `unique { closure }` — the closure to transform items before
  uniqueness comparison is ignored; only raw value comparison is used.
- `distinct { closure }` — the closure to transform items before
  consecutive-duplicate comparison is ignored; only raw value
  comparison is used.
- `sum { closure }` — the closure to transform items before summation
  is not supported. Additionally, `sum()` only works on integer items
  — floating-point and other numeric types will error.
- `count(literal)` / `count(regex)` / `count(Type)` — only closure
  predicates and the no-argument form work; literal, regex, and
  type-qualifier criteria are not evaluated.

## Channel Operators — `collate` Variants

The `collate(size)` operator is supported, but the two-argument
`collate(size, step)` sliding-window variant and the three-argument
`collate(size, step, remainder)` variant are not implemented. Only the
simple fixed-size chunking form works.

The `collate(size, remainder)` form (basic chunking with explicit
remainder control, where `remainder=false` discards trailing items)
is also not supported — only the one-argument form is accepted.

## Channel Operators — `buffer` Variants

The `buffer(size: n)` operator is supported for fixed-size grouping.
The following variants are not implemented:

- `buffer(closingCondition)` — buffer until a closure returns true.
- `buffer(openingCondition, closingCondition)` — start and stop buffering
  based on conditions.
- `buffer(size: n, skip: m)` — fixed-size buffer with step/skip.
- `buffer(size: n, remainder: true)` — include trailing partial buffer.

These require runtime closure evaluation against individual items.

## Channel Operator Options — Silently Ignored

Several channel operators accept named options that are parsed without
error but silently ignored during evaluation. The operator works for the
default/simple case but the option has no effect:

### Combining / Grouping

- `groupTuple([by: n])` — `by`, `size`, `remainder`, and `sort` are ignored;
  always groups by the first tuple element (index 0).
- `join([by: n, remainder: true])` — `by`, `remainder`, `failOnDuplicate`, and
  `failOnMismatch` are ignored; always joins by first element; unmatched items
  are dropped.
- `combine([by: n])` — `by` IS supported and correctly used for key-based
  combining. The `flatMap` option is not applicable.

### Splitting

- `splitCsv([sep: ','])` — `sep`, `strip`, `skip`, `limit`, `quote`,
  `charset`, and `decompress` are ignored; always uses comma as delimiter.
  `header` and `by` are supported.
- `splitFasta([record: [...]])` — `record`, `size`, `limit`, `compress`,
  `decompress`, `file`, `elem`, and `charset` are ignored; only `by` (chunk
  count) works.
- `splitText([keepHeader: true])` — `keepHeader`, `limit`, `charset`,
  `compress`, `decompress`, `file`, and `elem` are ignored; only `by` (line
  count) works.
- `splitFastq([record: [...]])` — `record`, `limit`, `compress`, `decompress`,
  `file`, `elem`, and `charset` are ignored; `by` and `pe` work.
- `splitJson([limit: n])` — `limit` is ignored; `path` is supported.

### Collecting

- `collectFile([sort: true])` — `sort`, `seed`, `newLine`, `storeDir`,
  `tempDir`, `keepHeader`, `skip`, and `cache` are ignored; only `name` works.

### Aggregation

- `min { closure }` / `max { closure }` — closures and comparators are
  ignored; only natural int/string comparison is supported.
- `toSortedList { comparator }` — comparator is ignored; only natural
  ordering of int/string.
- `collect { closure }` — closures are ignored at the channel operator level;
  items are always collected without transformation. The `flat` and `sort`
  named options are also ignored.
- `transpose([remainder: true])` — `remainder` is ignored; incomplete tuples
  are silently dropped. The `by` option IS supported.

## Channel Factory Options — Silently Ignored

- `Channel.fromPath(pattern, [type: 'file'])` — `type`, `checkIfExists`,
  `maxDepth`, `hidden`, `followLinks`, `relative`, and `glob` are ignored;
  only the glob pattern is used.
- `Channel.fromFilePairs(pattern, [size: 2])` — `size`, `flat`,
  `checkIfExists`, `hidden`, `maxDepth`, `followLinks`, and `type` are
  ignored; always groups by filename stem, any number of matching files
  per group.

## Container Config — `runOptions`

The `docker.runOptions`, `singularity.runOptions`, and
`apptainer.runOptions` config settings allow passing arbitrary flags to
the container runtime (e.g. `--gpus all` for GPU access). Only the
`enabled` flag is currently parsed from container config scopes. Adding
`runOptions` support would be valuable for GPU and device-access
workflows — the value could be appended to wr's container flags.

## Groovy Evaluator — Built-in Variables

Nextflow defines several implicit variables that are available in
expressions, closures, and process scripts. None of these are defined
in the Groovy evaluator — referencing them will fail with "unknown
variable":

- `baseDir` / `projectDir` — pipeline project directory
- `launchDir` — directory where `nextflow run` was invoked
- `moduleDir` — directory of the current module file
- `workDir` — pipeline work directory
- `workflow.projectDir`, `workflow.launchDir`, `workflow.workDir` —
  same values via the workflow namespace
- `workflow.resume`, `workflow.sessionId`, `workflow.runName` — run metadata
- `workflow.profile` — comma-separated list of active configuration profiles;
  commonly used in nf-core config files: `if (workflow.profile.contains('test'))`
- `workflow.stubRun` — whether the current run is a stub-run
- `workflow.homeDir` — user system home directory
- `workflow.manifest.*` — pipeline metadata from manifest config scope
- `workflow.container` / `workflow.containerEngine` — container image/engine info
- `workflow.scriptFile` / `workflow.scriptName` / `workflow.scriptId` — script metadata
- `workflow.repository` / `workflow.commitId` / `workflow.revision` — Git metadata
- `workflow.userName` — user system account name
- `workflow.configFiles` — list of config files used
- `nextflow.version` — Nextflow version string
- `nextflow.build` — Nextflow build number
- `nextflow.timestamp` — Nextflow build timestamp

These are commonly used in nf-core config files for paths like
`"${projectDir}/assets/schema.json"`.

## Groovy Evaluator — `log` Namespace

`log.info()`, `log.warn()`, and `log.error()` are not implemented.
These are used in nf-core pipelines for user-facing messages (e.g.
parameter validation summaries). They are display-only and do not
affect pipeline logic, but their absence causes evaluation errors
if encountered in config or workflow scope Groovy code.

## `task` Object — Missing Properties

The `task` object is available in dynamic directives with 4 properties:
`attempt`, `cpus`, `memory`, `exitStatus`. The following properties from
real Nextflow are not defined:

- `disk` — disk requirement
- `time` — time limit
- `workDir` — task working directory
- `name` — task name
- `hash` — task hash
- `index` — task index
- `process` — process name

`task.ext.*` IS supported as a nested map namespace.

## Global Nextflow Functions

Nextflow defines several global functions that can be called in
expressions and closures. The following are not implemented:

- `file(path)` / `files(pattern)` — file locator functions; used in
  `params` declarations and workflow bodies.
- `groupKey(key, size)` — creates a group key for `groupTuple` early
  release; occasionally used in nf-core.
- `branchCriteria { ... }` / `multiMapCriteria { ... }` — reusable
  criteria for `branch`/`multiMap` operators.
- `error(msg)` — terminate pipeline with an error message; used in
  parameter validation and branch defaults.
- `print(text)` / `println(text)` / `printf(fmt, args...)` — console
  output functions. `println` in process scripts is converted to `echo`
  for shell execution, but none are available as callable Groovy
  functions in expressions or closures.
- `env(name)` — returns the value of an environment variable from the
  launch environment. New in Nextflow 25.04. Different from `Channel.env`
  (a channel factory). May appear in config files and parameter defaults.
- `sendMail(...)` — send email notifications from pipeline code.
- `sleep(ms)` — pause execution.

## File/Path Methods

File and Path objects (from `new File(path)` or `file(path)`) support
many methods in Groovy/Nextflow that are not evaluated:

- `.text` / `.getText()` — read file contents
- `.baseName` — filename without extension
- `.name` — filename
- `.extension` — file extension
- `.parent` — parent directory path
- `.exists()` — check existence
- `.isFile()` / `.isDirectory()` — type checks
- `.readLines()` — read lines into list

These appear in bespoke pipelines but are uncommon in nf-core modules.
