# Implementation: Translation Pipeline (translate.go, part 1)

**File:** `nextflowdsl/translate.go` (~3100 lines)

## Entry Point

### TranslatePending (line 79)
Main entry point. Takes a `PendingStage` + completed upstream jobs, returns `[]*jobqueue.Job`.

Pipeline:
1. Merge params (tc.Params over pending.params)
2. Index completed jobs by repGroup
3. Update translated call items from completed output paths
4. Call `resolveBindings` → gets input binding sets
5. Call `filterWhenBindingSets` → applies `when` guard
6. Call `processWithConfigDefaults` → merges config defaults/selectors
7. Call `translateProcessBindingSets` → produces final jobs

**Coverage:** Everything — orchestrates the full translation.

## Binding Resolution

### resolveBindings (line 465)
Resolves process inputs from channel expressions and upstream outputs.
- For each call arg, resolves channel items
- Matches items to input declarations (val, path, tuple, each, env, stdin)
- Produces binding sets (cartesian product for `each` inputs)
- Handles `each` combinatorics

**Coverage:** INP-val, INP-path, INP-tuple, INP-each, INP-env, INP-stdin

### resolveBindingArg (line 630)
Resolves one call argument — looks up translated output, resolves channel items.

### resolveTranslatedOutput (line 712)
Looks up named output from previously translated calls.
Handles scoped names and emit references (`process.out.label`).

### filterWhenBindingSets (line 910)
Evaluates `when:` guard for each binding set, keeps only matching ones.
**Coverage:** PSEC-when, DIR-dynamic (evaluates expressions with bindings)

### EvalWhenGuard (line 996)
Public API for evaluating a `when:` expression with bindings and params.

## Job Generation

### translateProcessBindingSets (line 1045)
Core function — translates each binding set into a `jobqueue.Job`:
1. `processWithConfigDefaults` → merged process
2. `buildRequirements` → scheduler.Requirements
3. `buildCommandWithValues` → shell command
4. `applyContainer` → container settings
5. `applyMaxForks` → concurrency limits
6. `applyFairPriority` → fair scheduling priority
7. `applyErrorStrategy` → retry/ignore behaviour
8. `applyEnv` → environment variables
9. Sets RepGroup, Cwd, Dependencies, DepGroups

**Coverage:** DIR-*, INP-*, OUT-*, PSEC-script, PSEC-shell, PSEC-exec

### processWithConfigDefaults (line 1190)
Clones process, applies config defaults and matching selectors.
**Coverage:** CFG-process, CFG-selectors

### applyProcessDefaults (line 1270)
Merges ProcessDefaults fields into Process struct.
Handles: cpus, memory, time, disk, container, errorStrategy, maxRetries,
maxForks, publishDir, queue, clusterOptions, ext, containerOptions,
accelerator, arch, shell, beforeScript, afterScript, cache, scratch,
storeDir, module, conda, spack, fair, tag, env, arbitrary directives.
**Coverage:** DIR-cpus through DIR-other (all directive defaults)

### MatchSelectors (line 1933)
Public API — matches process against selectors, returns effective defaults.
Uses `selectorMatchesProcess` (1727) with label/name wildcards.
**Coverage:** CFG-selectors

### selectorMatchesProcess (line 1727)
Pattern matching: `withLabel` checks process labels, `withName` checks name.
Supports `*`, `?` wildcards and `!` negation.
