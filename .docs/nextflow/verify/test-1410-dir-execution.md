# Test: Directives: Execution

**Spec files:** nf-1410-dir-execution.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1410-dir-execution.md, determine its classification.

### Checklist

1. Read nf-1410-dir-execution.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-cache, DIR-errorStrategy, DIR-executor, DIR-fair, DIR-maxErrors
  DIR-maxForks, DIR-maxRetries, DIR-maxSubmitAwait, DIR-scratch, DIR-storeDir

### Output format

```
DIR-cache: SUPPORTED | reason
...
```

## Results

- DIR-cache: GAP — `parseDirective` stores `cache` in `proc.Cache` in `nextflowdsl/parse.go`, and defaults propagate through `resolveDirectiveString` in `nextflowdsl/translate.go`, but the translation path never applies `proc.Cache` to job creation or execution, so cache mode semantics are ignored.
- DIR-errorStrategy: GAP — `applyErrorStrategy` in `nextflowdsl/translate.go` implements `retry`, `ignore`, and terminate-style handling, but `finish` is only represented by `applyFinishStrategyLimitGroup` adding a limit-group token; the cited execution path does not show full Nextflow `finish` orchestration.
- DIR-executor: GAP — `parseDirective` stores `executor` in `proc.Directives["executor"]` in `nextflowdsl/parse.go`, but there is no corresponding use in `nextflowdsl/translate.go`, so per-process executor selection is silently ignored.
- DIR-fair: GAP — `applyFairPriority` in `nextflowdsl/translate.go` only converts input index into `job.Priority`; no cited code enforces Nextflow-style ordered output emission, so this is only an approximation.
- DIR-maxErrors: SUPPORTED — `newProcessMaxErrorsJob` in `nextflowdsl/translate.go` resolves `maxErrors`, adds a helper job, and that helper polls `wr status`, then kills or removes remaining jobs once the buried-count exceeds the configured limit.
- DIR-maxForks: SUPPORTED — `applyMaxForks` in `nextflowdsl/translate.go` appends a process-specific limit group `name:maxForks` to each job during translation, which is the runtime control used for concurrent job limiting.
- DIR-maxRetries: SUPPORTED — `parseDirective` stores `maxRetries` on the process in `nextflowdsl/parse.go`, and `applyErrorStrategy` uses `proc.MaxRetries` when `errorStrategy` resolves to `retry`.
- DIR-maxSubmitAwait: GAP — `parseDirective` stores `maxSubmitAwait` in `proc.Directives` in `nextflowdsl/parse.go`, but no cited translation code reads or applies it, so submit-wait behaviour is ignored.
- DIR-scratch: GAP — `resolveScratchDirective` and `wrapScratchCommand` in `nextflowdsl/translate.go` run the script in a scratch directory and copy outputs back, but the cited implementation does not stage inputs into scratch first, so behaviour differs from Nextflow scratch execution.
- DIR-storeDir: SUPPORTED — `resolveStoreDirDirective` and `wrapStoreDirCommand` in `nextflowdsl/translate.go` resolve the cache directory, skip execution when declared outputs already exist there, copy cached outputs back into the work directory, and copy fresh outputs into the store after a successful run.
