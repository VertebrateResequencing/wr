# Nextflow Features — Supported in wr

Everything listed below is parsed without error AND translated to wr jobs
(or stored/evaluated as appropriate) with the same practical end-result
behaviour as real Nextflow.

## Pipeline Structure

- DSL2 workflow files (`.nf`)
- `include { PROC; SUBWF } from './module'` — local and remote includes
- `include { PROC as ALIAS }` — aliased includes
- GitHub remote module resolution (`owner/repo@revision`)
- `includeConfig 'path'` — config file inclusion
- Top-level function definitions (`def funcName(args) { ... }`)
- `params.x = y` — legacy parameter assignment
- `params {}` block syntax with typed declarations
- `enum` and `record` type definitions — parsed and stored
- `output {}` top-level block — parsed, publish targets extracted and wired
- `nextflow.enable.*` / `nextflow.preview.*` feature flag assignments — parsed and silently ignored

## Process Definitions

### Sections

- `input:` — input declarations
- `output:` — output declarations with emit labels
- `script:` — shell script body with Nextflow interpolation
- `when:` — conditional guard expression; evaluated to skip process when false (deprecated by Nextflow but supported)
- `stub:` — stub script for dry-run mode; used as job command when `--stub-run` flag set
- `shell:` — alternative script with `!{var}` interpolation resolved, `${...}` left for bash (deprecated by Nextflow but supported)

### Input Qualifiers

- `val(x)` — value input
- `path(x)` / `file(x)` — file input (file is deprecated alias)
- `tuple val(x), path(y)` — tuple input with mixed qualifiers
- `env(x)` — environment variable input
- `stdin` — standard input
- `each val(x)` / `each path(x)` — cross-product input (generates separate jobs per each-value)

### Output Qualifiers

- `val(x)` — value output
- `path('pattern')` / `file('pattern')` — file output
- `tuple val(x), path(y)` — tuple output
- `env(x)` — environment variable output
- `stdout` — standard output capture
- `eval('command')` — evaluate command and capture stdout

### Output Modifiers

- `emit: name` — named emit label for workflow wiring
- `optional true` — output may not exist

### Directives (Parsed and Translated)

- `cpus` — CPU count → wr `Requirements.Cores`
- `memory` — memory requirement → wr `Requirements.RAM`
- `time` — time limit → wr `Requirements.Time`
- `disk` — disk requirement → wr `Requirements.Disk`
- `container` — container image → wr container execution
- `errorStrategy` — retry/ignore/terminate/finish behaviour
- `maxRetries` — retry count
- `maxErrors` — total error count limit → polling monitor job
- `maxForks` — concurrency limit → wr limit groups
- `publishDir` — output publishing (`path`, `mode`, `pattern` options)
- `label` — process labels for config selector matching
- `tag` — job name substitution tag
- `beforeScript` — pre-execution command prepended to job
- `afterScript` — post-execution command appended to job
- `module` — environment module loading
- `cache` — caching strategy (`true`/`false`/`'deep'`/`'lenient'`)
- `env` — environment variables
- `clusterOptions` — native scheduler options → `Requirements.Other`
- `queue` — scheduler queue → `Requirements.Other`
- `containerOptions` — extra container flags → appended to container cmd
- `conda` — Conda package spec → prepend `conda activate` to job
- `spack` — Spack package spec → prepend `spack load` to job
- `scratch` — temp directory wrapper → job command wrapping
- `storeDir` — permanent cache directory → skip-if-exists wrapper
- `shell` (directive) — custom shell binary → `RunnerExecShell` env var

### Directives (Parsed and Translated, Scheduler-Dependent)

- `accelerator` — GPU type/count → LSF `-R "select[ngpus>0] rusage[ngpus_physical=N]"`
- `arch` — CPU architecture → LSF `-R "select[type==X86_64]"` (or `AARCH64`)
- `ext` — custom key-value namespace; `task.ext.*` resolved in script interpolation
- `fair` — output ordering → wr job priority (earlier items get higher priority)

### Dynamic Directives

Directives can use closures referencing `task.*` properties:

```groovy
memory { 2.GB * task.attempt }
errorStrategy { task.exitStatus in [137,140] ? 'retry' : 'terminate' }
```

Evaluated with `task.attempt=1` (and other defaults) at translate time.

## Workflow Blocks

- `workflow { }` — unnamed entry workflow
- `workflow NAME { }` — named sub-workflows
- `take:` — input channel declarations
- `main:` — process calls and channel wiring
- `emit:` — output channel declarations
- `publish:` — publish statements wired to `output {}` block targets
- `onComplete:` — completion handler (body stored as raw text)
- `onError:` — error handler (body stored as raw text)
- Variable assignments in workflow main — channel tracking
- Pipe operator `|` — chaining calls
- `if/else` conditional blocks in workflow bodies
- Process calls with positional arguments: `PROC(ch1, ch2)`
- Sub-workflow calls: `SUBWF(ch1)`
- Process output access: `PROC.out`, `PROC.out.name`

## Channel Factories

- `Channel.of(items...)` — create from values
- `Channel.value(item)` — single-value channel
- `Channel.empty()` — empty channel
- `Channel.fromPath(pattern)` — files matching glob
- `Channel.fromFilePairs(pattern)` — paired files (e.g. `*_{1,2}.fastq`)
- `Channel.fromList(list)` — from list items
- `Channel.from(items...)` — deprecated alias for `Channel.of`

## Channel Operators

### Filtering

- `filter(closure)` — keep items matching condition
- `first()` — first item only
- `last()` — last item only
- `take(n)` — first N items
- `unique()` — deduplicate
- `distinct()` — deduplicate consecutive

### Transforming

- `map(closure)` — transform each item
- `flatMap(closure)` — transform and flatten
- `flatten()` — flatten nested structures
- `collect()` — collect all items into one list
- `groupTuple([by: n, size: n])` — group by key
- `transpose([by: n])` — un-group tuples
- `toList()` — collect into list
- `toSortedList()` — collect into sorted list
- `reduce(acc, closure)` — fold/accumulate
- `count([filter])` — count items
- `ifEmpty(value)` — default for empty channel

### Combining

- `mix(other)` — unordered merge
- `join(other, [by: n, remainder: true])` — keyed join
- `combine(other, [by: n])` — cross product
- `concat(ch1, ch2, ...)` — ordered concatenation
- `cross(other)` — cross product

### Splitting

- `splitCsv([header: true, sep: char])` — CSV splitting
- `splitJson([path: '...'])` — JSON splitting
- `splitText([by: n])` — line-based text splitting
- `splitFasta([by: n, record: [...]])` — FASTA splitting
- `splitFastq([by: n, pe: true])` — FASTQ splitting
- `collectFile([name: '...'])` — collect items to file

### Routing

- `branch { criteria }` — split into named outputs
- `multiMap { criteria }` — map to multiple outputs

### Viewing/Debugging

- `view()` / `view(closure)` — print items
- `dump()` — debug output
- `tap(closure)` — side-effect without consuming
- `set()` — bind to variable

### Grouping

- `buffer(size: n)` — group items into fixed-size sublists
- `collate(n)` — group items into fixed-size chunks (one-argument form only)

### Sampling

- `randomSample(n, [seed])` — random sample of N items from channel

### Aggregation

- `min()` / `max()` — extremes
- `sum()` — sum items

## Expression / Groovy Evaluation

### Statements

- `if (cond) { } else if (cond) { } else { }` — conditional execution
- `for (x in collection) { }` — iteration over lists, ranges, maps
- `try { } catch (Type e) { } finally { }` — exception handling with typed catches
- `switch (val) { case X: ...; default: ... }` — multi-branch dispatch
- `assert condition : 'message'` — assertion with optional message
- `throw new Exception('msg')` — throw exceptions
- `def x = value` / `def (a, b) = [1, 2]` — variable declarations (including multi-assign)
- `return value` — explicit return from function/closure

### Operators

- Arithmetic: `+`, `-`, `*`, `/`, `%`, `**` (power)
- Comparison: `==`, `!=`, `<`, `>`, `<=`, `>=`, `<=>` (spaceship)
- Logical: `&&`, `||`, `!`
- Bitwise: `&`, `|`, `^`, `~`, `<<`, `>>`, `>>>`
- Regex: `=~` (find), `==~` (match)
- Membership: `in`, `!in`
- Type: `instanceof`, `!instanceof`, `as` (cast)
- Range: `..` (inclusive), `..<` (right-exclusive)
- Ternary: `cond ? a : b`
- Elvis: `value ?: default`
- Null-safe navigation: `obj?.prop`
- Spread: `list*.prop`

### String Methods (~30)

`trim`, `size`, `length`, `toLowerCase`, `toUpperCase`, `contains`,
`startsWith`, `endsWith`, `replace`, `replaceAll`, `replaceFirst`,
`split`, `tokenize`, `substring`, `padLeft`, `padRight`, `capitalize`,
`stripIndent`, `readLines`, `count`, `isNumber`, `isInteger`, `isLong`,
`isDouble`, `isBigDecimal`, `toBoolean`, `toInteger`, `toLong`,
`multiply`, `eachLine`

### List Methods (~43)

`size`, `isEmpty`, `first`, `last`, `head`, `tail`, `init`, `flatten`,
`join`, `unique`, `sort`, `reverse`, `contains`, `intersect`, `disjoint`,
`toSet`, `plus`, `minus`, `take`, `drop`, `withIndex`, `indexed`,
`transpose`, `pop`, `push`, `add`, `addAll`, `remove`, `any`, `every`,
`find`, `findAll`, `inject`, `groupBy`, `countBy`, `count`,
`collectMany`, `collectEntries`, `reverseEach`, `eachWithIndex`, `sum`,
`max`, `min`

### Map Methods (~20)

`size`, `isEmpty`, `keySet`, `values`, `containsKey`, `containsValue`,
`subMap`, `getOrDefault`, `find`, `findAll`, `any`, `every`, `groupBy`,
`collectEntries`, `collect`, `each`, `plus`, `minus`, `sort`, `inject`

### Number Methods

`abs`, `round`, `intdiv`, `toInteger`, `toLong`, `toDouble`,
`toBigDecimal`

### Constructor Calls

`new File(path)`, `new URL(url)`, `new Date()`, `new BigDecimal(v)`,
`new BigInteger(v)`, `new ArrayList(list)`, `new HashMap(map)`,
`new LinkedHashMap(map)`, `new Random()`

## Config Parsing

### Scopes Parsed and Applied

- `params {}` — pipeline parameters
- `process {}` — process directive defaults
- `process.withLabel('pattern') {}` / `process.withName('pattern') {}` — selectors
- `env {}` — environment variables
- `executor {}` — executor settings
- `profiles { name { ... } }` — profile-scoped overrides
- `docker { enabled = true }` / `singularity { enabled = true }` / `apptainer { enabled = true }` — container engine toggle (only `enabled` setting parsed)

### Scopes Silently Skipped

- `conda`, `dag`, `manifest`, `notification`, `report`, `timeline`,
  `tower`, `trace`, `wave`, `weblog` — platform features not relevant to wr
