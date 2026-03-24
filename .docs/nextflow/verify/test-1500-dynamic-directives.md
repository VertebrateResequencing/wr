# Test: Dynamic Directives & Task Properties

**Spec files:** nf-1500-dynamic-directives.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1500-dynamic-directives.md, determine its classification.

### Checklist

1. Read nf-1500-dynamic-directives.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- BV-task-attempt, BV-task-exitStatus, BV-task-hash, BV-task-index, BV-task-name
  BV-task-previousException, BV-task-previousTrace, BV-task-process, BV-task-workDir

### Output format

```
BV-task-attempt: SUPPORTED | reason
...
```

## Results

- BV-task-attempt: GAP — `evalDirectiveExpr` and `resolveDirectiveValue` in `nextflowdsl/translate.go` evaluate dynamic directives during translation, and `defaultDirectiveTask()` only seeds `task.attempt` with the placeholder value `1`; retries do not rebind it to the real attempt count, so semantics differ from Nextflow.
- BV-task-exitStatus: GAP — `defaultDirectiveTask()` in `nextflowdsl/translate.go` hard-codes `task.exitStatus` to `0`, and `resolveErrorStrategy`/`applyErrorStrategy` resolve strategy from that translation-time value rather than a real task failure status.
- BV-task-hash: PARSE_ERROR — `defaultDirectiveTask()` does not provide `hash`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.hash"`.
- BV-task-index: PARSE_ERROR — `defaultDirectiveTask()` does not provide `index`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.index"`.
- BV-task-name: PARSE_ERROR — `defaultDirectiveTask()` does not provide `name`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.name"`.
- BV-task-previousException: PARSE_ERROR — `defaultDirectiveTask()` does not provide `previousException`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.previousException"`.
- BV-task-previousTrace: PARSE_ERROR — `defaultDirectiveTask()` does not provide `previousTrace`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.previousTrace"`.
- BV-task-process: PARSE_ERROR — `defaultDirectiveTask()` does not provide `process`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.process"`.
- BV-task-workDir: PARSE_ERROR — `defaultDirectiveTask()` does not provide `workDir`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.workDir"`.
