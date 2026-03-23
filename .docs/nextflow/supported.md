# Nextflow Features — Supported in wr

Everything listed below is parsed without error AND translated to wr jobs
(or stored/evaluated as appropriate).

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

## Process Definitions

### Sections

- `input:` — input declarations
- `output:` — output declarations with emit labels
- `script:` — shell script body with Nextflow interpolation
- `when:` — conditional guard expression; evaluated at runtime to skip process when false
- `stub:` — stub script for dry-run mode; used as job command when `--stub-run` flag set
- `exec:` — Groovy exec block (parsed, stored; not translated)
- `shell:` — alternative script with `!{var}` interpolation resolved, `${...}` left for bash

### Input Qualifiers

- `val(x)` — value input
- `path(x)` / `file(x)` — file input (file is deprecated alias)
- `tuple val(x), path(y)` — tuple input with mixed qualifiers
- `env(x)` — environment variable input
- `stdin` — standard input
- `each val(x)` / `each path(x)` — cross-product input

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
- `maxForks` — concurrency limit → wr limit groups
- `publishDir` — output publishing (path, mode, pattern, saveAs)
- `label` — process labels for config selector matching
- `tag` — job name substitution tag
- `beforeScript` — pre-execution command prepended to job
- `afterScript` — post-execution command appended to job
- `module` — environment module loading
- `cache` — caching strategy
- `env` — environment variables

### Directives (Parsed and Translated, Scheduler-Dependent)

- `accelerator` — GPU type/count → LSF `-R "select[ngpus>0] rusage[ngpus_physical=N]"`
- `arch` — CPU architecture → LSF `-R "select[type==X86_64]"` (or `AARCH64`)
- `ext` — custom key-value namespace; `task.ext.*` resolved in script interpolation
- `fair` — output ordering → wr job priority (earlier items get higher priority)

### Directives (Parsed and Stored, Translation Varies)

All of the following are parsed without error and stored in the
`Process.Directives` map:

- `array` — job array size
- `clusterOptions` — native scheduler options → `Requirements.Other`
- `conda` — Conda package spec → prepend `conda activate` to job
- `containerOptions` — extra container flags → appended to container cmd
- `debug` / `echo` — stdout forwarding flag
- `executor` — per-process executor override
- `machineType` — cloud VM instance type
- `maxErrors` — total error count limit → polling monitor job
- `maxSubmitAwait` — queue wait timeout
- `penv` — SGE parallel environment
- `pod` — Kubernetes pod configuration
- `queue` — scheduler queue → `Requirements.Other`
- `resourceLabels` — cloud resource metadata
- `resourceLimits` — resource caps
- `scratch` — temp directory wrapper → job command wrapping
- `secret` — secret environment variables
- `shell` (directive) — custom shell binary → `RunnerExecShell` env var
- `spack` — Spack package spec → prepend `spack load` to job
- `stageInMode` / `stageOutMode` — file staging modes
- `storeDir` — permanent cache directory → skip-if-exists wrapper

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
- `onComplete:` — completion handler
- `onError:` — error handler
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
- `collate(n)` — group items into fixed-size chunks

### Sampling

- `randomSample(n, [seed])` — random sample of N items from channel

### Aggregation

- `min()` / `max()` — extremes
- `sum()` — sum items

## Groovy Expression Evaluation

### Operators

- `+`, `-`, `*`, `/` — arithmetic
- `%` — modulo
- `**` — exponentiation
- `==`, `!=`, `<`, `>`, `<=`, `>=` — comparison
- `<=>` — spaceship (three-way comparison)
- `&&`, `||`, `!` — logical
- `&`, `^`, `|` — bitwise
- `~` — bitwise NOT
- `<<`, `>>`, `>>>` — shift
- `in`, `!in` — membership testing
- `instanceof`, `!instanceof` — type checking
- `=~` — regex find
- `==~` — regex full match
- `..` — inclusive range
- `..<` — exclusive range
- `*.property` — spread-dot
- `?:` — elvis operator
- `? :` — ternary conditional
- `?.` — null-safe navigation
- `as` — type cast

### Literals and Expressions

- Integer, float, boolean, null literals
- Single-quoted strings
- Double-quoted strings with `${interpolation}`
- Triple-quoted strings (single and double)
- Slashy strings `/pattern/`
- List literals `[1, 2, 3]`
- Map literals `[key: value]`
- Closure literals `{ args -> body }`
- `new ClassName(args)` constructors — evaluated (File, URL, Date, BigDecimal, BigInteger, ArrayList, HashMap, LinkedHashMap, Random)
- `def (x, y) = [1, 2]` multi-variable assignment — destructured from list
- Index access `list[0]`, `map['key']`
- Property access `obj.field`
- Method call chaining `obj.method1().method2()`
- `params.key` parameter references

### String Methods

- `trim()`, `strip()`
- `size()`, `length()`
- `toLowerCase()`, `toUpperCase()`
- `capitalize()`, `uncapitalize()`
- `contains(str)`, `startsWith(prefix)`, `endsWith(suffix)`
- `indexOf(str)`, `lastIndexOf(str)`
- `replace(old, new)`
- `replaceAll(pattern, replacement)`, `replaceFirst(pattern, replacement)`
- `matches(regex)`
- `split(regex)`, `tokenize(separators)`
- `substring(start, [end])`
- `padLeft(n, [char])`, `padRight(n, [char])`
- `stripIndent()`
- `readLines()`
- `count(str)` — count occurrences
- `eachLine(closure)` — iterate lines
- `toInteger()`, `toLong()`, `toDouble()`, `toBigDecimal()`
- `toBoolean()`
- `isNumber()`, `isInteger()`, `isLong()`, `isDouble()`, `isBigDecimal()`
- `plus(str)`, `minus(str)`, `multiply(n)`

### List Methods

- `size()`, `isEmpty()`
- `get(index)`, `first()`, `last()`
- `head()`, `tail()`, `init()`
- `take(n)`, `drop(n)`
- `flatten()`, `reverse()`
- `sort()`, `unique()`, `toSet()`
- `min()`, `max()`, `sum()` — with optional closure
- `join(separator)`
- `collect(closure)` — map
- `collectMany(closure)` — flatMap
- `collectEntries(closure)` — list to map
- `findAll(closure)`
- `find(closure)`
- `any(closure)`, `every(closure)`
- `count(value|closure)` — count matching elements
- `countBy(closure)` — count into map by key
- `plus(item|list)`, `minus(item|list)`
- `add(item)`, `addAll(items)`, `push(item)`, `pop()`
- `remove(index)`
- `contains(item)`
- `intersect(other)`, `disjoint(other)`
- `groupBy(closure)`
- `inject(init, closure)` — fold/reduce
- `withIndex()`, `indexed()`
- `eachWithIndex(closure)`, `reverseEach(closure)`
- `transpose()` — transpose list of lists
- `asType(Class)` — type conversion (e.g. Set)
- `spread(closure)` — apply closure to each element

### Map Methods

- `size()`, `isEmpty()`
- `get(key)`, `getOrDefault(key, default)`
- `containsKey(key)`, `containsValue(val)`
- `keySet()`, `values()`, `entrySet()`
- `each(closure)`, `collect(closure)`
- `findAll(closure)`, `find(closure)`
- `any(closure)`, `every(closure)`
- `groupBy(closure)`, `collectEntries(closure)`
- `plus(other_map)`, `minus(keys)`
- `sort(closure)`
- `inject(init, closure)` — fold entries
- `subMap(keys)`

### Number Methods

- `abs()` — absolute value
- `round()` — round to nearest integer
- `intdiv(n)` — integer division
- `toInteger()`, `toLong()`, `toDouble()`, `toBigDecimal()`

### Statement Types

- `if / else if / else` — conditional execution
- `for (x in collection) { }` — iteration
- `while (condition) { }` — loops
- `switch / case / default` — multi-way branching
- `try / catch / finally` — error handling
- `return expr` — early return
- `assert expr : 'message'` — assertions evaluated; emit warning when false
- `throw new Exception('...')` — error raising; emit warning with message

## Configuration

### Scopes Parsed

- `params {}` — parameter defaults
- `process {}` — process defaults and selectors
- `profiles {}` — named profile overrides
- `docker {}` / `singularity {}` / `apptainer {}` — container engines
- `env {}` — environment variables
- `executor {}` — executor settings

### Process Config Selectors

- `withLabel:` — match by process label
- `withName:` — match by process name

### Process Config Settings Extracted

- `cpus`, `memory`, `time`, `disk`
- `container`, `containerOptions`
- `errorStrategy`, `maxRetries`, `maxForks`
- `publishDir`
- `env`
- `queue`, `clusterOptions`
- `accelerator`, `arch`
- `ext` (key-value namespace)
- `shell`, `beforeScript`, `afterScript`
- `cache`, `scratch`, `storeDir`
- `module`, `conda`, `spack`
- `fair`, `tag`

### Parameter Resolution

- `params.x = value` in config and workflow files
- `-params-file` JSON/YAML parameter files
- CLI parameter overrides
- Precedence: CLI > config > script
- Nested parameter access: `params.db.path`

## Module System

- Local includes: `include { PROC } from './module'`
- GitHub includes: `include { PROC } from 'owner/repo'`
- Aliased includes: `include { PROC as MY_PROC }`
- Chained module resolution (local then remote)
- Recursive include resolution
- `addParams` / `params` on includes

## Translation to wr Jobs

### Job Properties Set

- `Cmd` — shell command from script body with interpolation
- `Requirements.Cores` — from `cpus` directive
- `Requirements.RAM` — from `memory` directive (MB)
- `Requirements.Time` — from `time` directive
- `Requirements.Disk` — from `disk` directive
- `Requirements.Other` — scheduler-specific options
- Container image and execution wrapping
- `DepGroups` — dependency group wiring between processes
- Retry behaviour from `errorStrategy` and `maxRetries`
- Limit groups from `maxForks`
- `beforeScript` / `afterScript` wrapping
- Environment module loading
- Output publishing (copy/move/link to publishDir)
- `each` cross-product expansion (N×M jobs)
- `eval` output appended to script
- `scratch` directory wrapping
- `storeDir` skip-if-exists wrapping
- `conda activate` / `spack load` prepending
- `onComplete` final job with all dep_grps
- `onError` polling monitor job
- `stub` body substitution (when `--stub-run` flag is set)
- `when:` guard evaluation — skip jobs when guard is false
- `accelerator` → LSF GPU resource requirements
- `arch` → LSF architecture constraint
- `ext` / `task.ext.*` resolution in script interpolation
- `fair` → wr job priority ordering
- `errorStrategy 'finish'` → limit group set to 0 on failure
- `publish:` + `output {}` block → output copying to target paths
- Index file generation for `output {}` targets with `index`

### Dynamic Workflow Support

- `TranslatePending` mechanism for runtime-dependent operations
- Data-dependent channel operators create pending stages
- Runtime job creation from pending stages
