# Nextflow Features — Gaps

These Nextflow features are not yet covered in `supported.md`,
`unsupported.md`, or `future.md`. They could potentially be implemented.

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

## Channel Operators — Type Conversion

The `toInteger()`, `toLong()`, `toFloat()`, and `toDouble()` channel
operators are recognised and parsed without error, but they are no-ops —
items pass through without actual type conversion. In real Nextflow these
convert each channel item to the respective numeric type. If upstream
items are already the correct type (common in practice) the end result
is the same, but string-to-number conversion will not happen.

## Channel Operators — `collate` Sliding Window

The `collate(size)` operator is supported, but the two-argument
`collate(size, step)` variant (sliding window) and the three-argument
`collate(size, step, remainder)` variant are not implemented. Only the
simple fixed-size chunking form works.

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

- `groupTuple([by: n])` — `by` and `size` are ignored; always groups by
  the first tuple element (index 0).
- `join([by: n, remainder: true])` — `by` and `remainder` are ignored;
  always joins by first element; unmatched items are dropped.
- `cross([by: n])` — `by` is ignored; always does full Cartesian product.

### Splitting

- `splitCsv([sep: ','])` — `sep`, `strip`, `skip`, `limit` are ignored;
  always uses comma as delimiter.
- `splitFasta([record: [...]])` — `record`, `size`, `limit`, `pe`,
  `compress`, `file`, `elem` are ignored; only `by` (chunk count) works.
- `splitText([keepHeader: true])` — `keepHeader`, `limit`, `charset`,
  `compress`, `file`, `elem` are ignored; only `by` (line count) works.
- `splitFastq([record: [...]])` — `record`, `limit`, `compress`, `file`,
  `elem` are ignored; `by` and `pe` work.

### Collecting

- `collectFile([sort: true])` — `sort`, `seed`, `newLine`, `storeDir`,
  `tempDir`, `keepHeader`, `skip` are ignored; only `name` works.

### Aggregation

- `min { closure }` / `max { closure }` — closures and comparators are
  ignored; only natural int/string comparison is supported.
- `toSortedList { comparator }` — comparator is ignored; only natural
  ordering of int/string.

## Channel Factory Options — Silently Ignored

- `Channel.fromPath(pattern, [type: 'file'])` — `type`, `checkIfExists`,
  `maxDepth`, `hidden`, `followLinks` are ignored; only the glob pattern
  is used.
- `Channel.fromFilePairs(pattern, [size: 2])` — `size`, `flat`,
  `checkIfExists` are ignored; always groups by filename stem, any number
  of matching files per group.

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
- `nextflow.version` — Nextflow version string

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
