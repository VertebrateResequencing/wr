# Nextflow Features — Supported in wr

Everything listed below is parsed without error AND translated to wr jobs
(or stored/evaluated as appropriate). Items marked **(strict3)** are being
added by the nextflowstrict3 spec implementation.

## Pipeline Structure

- DSL2 workflow files (`.nf`)
- `include { PROC; SUBWF } from './module'` — local and remote includes
- `include { PROC as ALIAS }` — aliased includes
- GitHub remote module resolution (`owner/repo@revision`)
- `includeConfig 'path'` — config file inclusion
- Top-level function definitions (`def funcName(args) { ... }`)
- `params.x = y` — legacy parameter assignment
- `params {}` block syntax with typed declarations **(strict3)**
- `enum` and `record` type definitions — parsed and stored **(strict3)**
- `output {}` top-level block — parsed and stored **(strict3)**

## Process Definitions

### Sections

- `input:` — input declarations
- `output:` — output declarations with emit labels
- `script:` — shell script body with Nextflow interpolation
- `when:` — conditional guard expression (parsed, stored)
- `stub:` — stub script for dry-run mode (parsed, stored)
- `exec:` — Groovy exec block (parsed, stored; not translated)
- `shell:` — alternative script with `!{var}` interpolation (parsed, stored)

### Input Qualifiers

- `val(x)` — value input
- `path(x)` / `file(x)` — file input (file is deprecated alias)
- `tuple val(x), path(y)` — tuple input with mixed qualifiers
- `env(x)` — environment variable input
- `stdin` — standard input
- `each val(x)` / `each path(x)` — cross-product input **(strict3)**

### Output Qualifiers

- `val(x)` — value output
- `path('pattern')` / `file('pattern')` — file output
- `tuple val(x), path(y)` — tuple output
- `env(x)` — environment variable output
- `stdout` — standard output capture
- `eval('command')` — evaluate command and capture stdout **(strict3)**

### Output Modifiers

- `emit: name` — named emit label for workflow wiring
- `optional true` — output may not exist

### Directives (Parsed and Translated)

- `cpus` — CPU count → wr `Requirements.Cores`
- `memory` — memory requirement → wr `Requirements.RAM`
- `time` — time limit → wr `Requirements.Time`
- `disk` — disk requirement → wr `Requirements.Disk`
- `container` — container image → wr container execution
- `errorStrategy` — retry/ignore/terminate behaviour
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

### Directives (Parsed and Stored, Translation Varies) **(strict3)**

All of the following are parsed without error and stored in the
`Process.Directives` map:

- `accelerator` — GPU requests (type, count)
- `arch` — CPU architecture specification
- `array` — job array size
- `clusterOptions` — native scheduler options → `Requirements.Other`
- `conda` — Conda package spec → prepend `conda activate` to job
- `containerOptions` — extra container flags → appended to container cmd
- `debug` / `echo` — stdout forwarding flag
- `executor` — per-process executor override
- `ext` — custom key-value metadata namespace
- `fair` — output ordering guarantee
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

### Dynamic Directives **(strict3)**

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
- `publish:` — publish statements **(strict3)**
- `onComplete:` — completion handler **(strict3)**
- `onError:` — error handler **(strict3)**
- Variable assignments in workflow main — channel tracking **(strict3)**
- Pipe operator `|` — chaining calls **(strict3)**
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

## Channel Operators

### Filtering

- `filter(closure)` — keep items matching condition
- `first()` — first item only
- `last()` — last item only
- `take(n)` — first N items
- `unique()` — deduplicate **(strict3)**
- `distinct()` — deduplicate consecutive **(strict3)**

### Transforming

- `map(closure)` — transform each item
- `flatMap(closure)` — transform and flatten
- `flatten()` — flatten nested structures **(strict3)**
- `collect()` — collect all items into one list
- `groupTuple([by: n, size: n])` — group by key
- `transpose([by: n])` — un-group tuples **(strict3)**
- `toList()` — collect into list **(strict3)**
- `toSortedList()` — collect into sorted list **(strict3)**
- `reduce(acc, closure)` — fold/accumulate **(strict3)**
- `count([filter])` — count items **(strict3)**
- `ifEmpty(value)` — default for empty channel **(strict3)**

### Combining

- `mix(other)` — unordered merge
- `join(other, [by: n, remainder: true])` — keyed join
- `combine(other, [by: n])` — cross product **(strict3)**
- `concat(ch1, ch2, ...)` — ordered concatenation **(strict3)**
- `cross(other)` — cross product

### Splitting

- `splitCsv([header: true, sep: char])` — CSV splitting **(strict3)**
- `splitJson([path: '...'])` — JSON splitting **(strict3)**
- `splitText([by: n])` — line-based text splitting **(strict3)**
- `splitFasta([by: n, record: [...]])` — FASTA splitting **(strict3)**
- `splitFastq([by: n, pe: true])` — FASTQ splitting **(strict3)**
- `collectFile([name: '...'])` — collect items to file **(strict3)**

### Routing

- `branch { criteria }` — split into named outputs **(strict3)**
- `multiMap { criteria }` — map to multiple outputs **(strict3)**

### Viewing/Debugging

- `view()` / `view(closure)` — print items
- `dump()` — debug output
- `tap(closure)` — side-effect without consuming
- `set()` — bind to variable

### Aggregation

- `min()` / `max()` — extremes
- `sum()` — sum items

## Groovy Expression Evaluation

### Operators

- `+`, `-`, `*`, `/` — arithmetic
- `%` — modulo **(strict3)**
- `**` — exponentiation **(strict3)**
- `==`, `!=`, `<`, `>`, `<=`, `>=` — comparison
- `<=>` — spaceship (three-way comparison) **(strict3)**
- `&&`, `||`, `!` — logical
- `&`, `^`, `|` — bitwise **(strict3)**
- `~` — bitwise NOT **(strict3)**
- `<<`, `>>`, `>>>` — shift **(strict3)**
- `in`, `!in` — membership testing **(strict3)**
- `instanceof`, `!instanceof` — type checking **(strict3)**
- `=~` — regex find **(strict3)**
- `==~` — regex full match **(strict3)**
- `..` — inclusive range **(strict3)**
- `..<` — exclusive range **(strict3)**
- `*.property` — spread-dot **(strict3)**
- `?:` — elvis operator
- `? :` — ternary conditional
- `?.` — null-safe navigation
- `as` — type cast

### Literals and Expressions

- Integer, float, boolean, null literals
- Single-quoted strings
- Double-quoted strings with `${interpolation}`
- Triple-quoted strings (single and double)
- Slashy strings `/pattern/` **(strict3)**
- List literals `[1, 2, 3]`
- Map literals `[key: value]`
- Closure literals `{ args -> body }`
- `new ClassName(args)` constructors — parsed **(strict3)**
- `def (x, y) = [1, 2]` multi-variable assignment **(strict3)**
- Index access `list[0]`, `map['key']`
- Property access `obj.field`
- Method call chaining `obj.method1().method2()`
- `params.key` parameter references

### String Methods

- `trim()`, `strip()`
- `size()`, `length()`
- `toLowerCase()`, `toUpperCase()`
- `contains(str)`, `startsWith(prefix)`, `endsWith(suffix)`
- `indexOf(str)`, `lastIndexOf(str)`
- `replace(old, new)`
- `replaceAll(pattern, replacement)` **(strict3)**
- `matches(regex)` **(strict3)**
- `split(regex)`, `tokenize(separators)`
- `substring(start, [end])`
- `toInteger()`, `toLong()`, `toDouble()`
- `plus(str)`, `minus(str)`, `multiply(n)` **(strict3)**

### List Methods

- `size()`, `isEmpty()`
- `get(index)`, `first()`, `last()`
- `take(n)`, `drop(n)` **(strict3)**
- `flatten()`, `reverse()` **(strict3)**
- `sort()`, `unique()` **(strict3)**
- `min()`, `max()`, `sum()`
- `join(separator)` **(strict3)**
- `collect(closure)` — map
- `findAll(closure)` **(strict3)**
- `find(closure)` **(strict3)**
- `any(closure)`, `every(closure)` **(strict3)**
- `plus(item|list)`, `minus(item|list)`
- `groupBy(closure)` **(strict3)**
- `withIndex()`, `indexed()` **(strict3)**

### Map Methods

- `size()`, `isEmpty()`
- `get(key)`, `containsKey(key)`
- `keySet()`, `values()`, `entrySet()`
- `each(closure)`, `collect(closure)`
- `subMap(keys)` **(strict3)**

### Statement Types **(strict3)**

- `if / else if / else` — conditional execution
- `for (x in collection) { }` — iteration
- `while (condition) { }` — loops
- `switch / case / default` — multi-way branching
- `try / catch / finally` — error handling
- `return expr` — early return
- `assert expr : 'message'` — assertions (warns only)
- `throw new Exception('...')` — error raising (warns only)

## Configuration

### Scopes Parsed

- `params {}` — parameter defaults
- `process {}` — process defaults and selectors
- `profiles {}` — named profile overrides
- `docker {}` / `singularity {}` / `apptainer {}` — container engines
- `env {}` — environment variables
- `executor {}` — executor settings **(strict3)**

### Process Config Selectors

- `withLabel:` — match by process label
- `withName:` — match by process name

### Process Config Settings Extracted

- `cpus`, `memory`, `time`, `disk`
- `container`
- `errorStrategy`, `maxRetries`
- `publishDir`
- `env`

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
- `Requirements.Other` — scheduler-specific options **(strict3)**
- Container image and execution wrapping
- `DepGroups` — dependency group wiring between processes
- Retry behaviour from `errorStrategy` and `maxRetries`
- Limit groups from `maxForks`
- `beforeScript` / `afterScript` wrapping
- Environment module loading
- Output publishing (copy/move/link to publishDir)
- `each` cross-product expansion (N×M jobs) **(strict3)**
- `eval` output appended to script **(strict3)**
- `scratch` directory wrapping **(strict3)**
- `storeDir` skip-if-exists wrapping **(strict3)**
- `conda activate` / `spack load` prepending **(strict3)**
- `onComplete` final job with all dep_grps **(strict3)**
- `onError` polling monitor job **(strict3)**

### Dynamic Workflow Support

- `TranslatePending` mechanism for runtime-dependent operations
- Data-dependent channel operators create pending stages
- Runtime job creation from pending stages
