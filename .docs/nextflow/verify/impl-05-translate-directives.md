# Implementation: Directive Resolution (translate.go, part 2)

**File:** `nextflowdsl/translate.go`

## Requirements Building

### buildRequirements (line 1813)
Builds `scheduler.Requirements` from process directives:
- `resolveDirectiveInt("cpus", ...)` → CPUs (default 1)
- `resolveDirectiveInt("memory", ...)` → Memory in MB (parsed via `memoryRE`)
- `resolveDirectiveInt("time", ...)` → Time in minutes (parsed via `timeRE`)
- `resolveDirectiveInt("disk", ...)` → Disk in GB (parsed via `diskRE`)
- `resolveDirectiveString("queue", ...)` → Queue name
- `resolveDirectiveString("clusterOptions", ...)` → Scheduler-specific options
- `resolveAcceleratorOptions` → GPU requirements
- `resolveArchOptions` → Architecture constraints

**Coverage:** DIR-cpus, DIR-memory, DIR-time, DIR-disk, DIR-queue,
DIR-clusterOptions, DIR-accelerator, DIR-arch

## Directive Evaluation

### resolveDirectiveString (line 1482)
Resolves a directive that should produce a string. Evaluates expression
if it's an Expr or string with interpolation.
Used by: queue, clusterOptions, tag, module, container, etc.

### resolveDirectiveValue (line 1510)
Resolves a directive to any value. Handles:
- String expressions → parse + evaluate
- Already-evaluated values → return as-is
Returns: (value, isSet, error)

### resolveDirectiveInt (line 1580)
Resolves a directive to int with fallback. Parses memory/time strings.

### resolveDirectiveBool (line 2423)
Resolves a directive to bool. Used for `fair`, `cache`, `debug`.

### resolveShellDirective (line 1678)
Resolves `shell` directive — list of shell paths or single string.
**Coverage:** DIR-shell

### resolveAcceleratorOptions (line 2148)
Parses accelerator directive: count + type (e.g. `1, type: 'nvidia-tesla-v100'`).
Generates scheduler-specific options (e.g. LSF `-R 'rusage[ngpus_physical=N]'`).
**Coverage:** DIR-accelerator

### resolveArchOptions (line 2324)
Parses arch directive: name + target.
**Coverage:** DIR-arch

## Job Modifiers

### applyContainer (line 2350)
Applies container directive to job.
Detects runtime (docker, singularity, apptainer) from image URL patterns.
**Coverage:** DIR-container

### applyMaxForks (line 2394)
Sets limit group on job when `maxForks` is set.
**Coverage:** DIR-maxForks

### applyFairPriority (line 2408)
Sets job priority based on binding index when `fair` is true.
**Coverage:** DIR-fair

### applyErrorStrategy (line 2478)
Maps Nextflow error strategies to wr behaviours:
- `retry` → retries with maxRetries
- `ignore` → continue on failure
- `terminate`/`finish` → fail pipeline
**Coverage:** DIR-errorStrategy, DIR-maxRetries

### applyFinishStrategyLimitGroup (line 2451)
Creates a limit group for `finish` error strategy.
**Coverage:** DIR-errorStrategy (finish variant)

### applyEnv (line 2502)
Resolves `env` directive map, sets environment variables on job.
**Coverage:** DIR-env

## Dynamic Directives

### resolveDirectiveValue, evalDirectiveExpr (line 1535)
These evaluate directive expressions that may reference `task.*` properties.
Built-in task map: `defaultDirectiveTask()` (line 1571) provides:
- task.cpus, task.memory, task.time, task.attempt, task.name, etc.

**Coverage:** DIR-dynamic, BV-task-*

### exprVarsWithTask (line 1554)
Merges params + task map for directive evaluation context.

## Ext Directive

### resolveExtDirectiveWithValues (line 2782)
Resolves `ext` directive map with input bindings available.
Handles nested string interpolation within ext values.
**Coverage:** DIR-ext

### extDirectiveSourceMap (line 1602)
Extracts source map from raw ext value (could be map or already-parsed).

### mergeExtValues (line 1661)
Deep-merges ext maps from multiple sources (process + config).
