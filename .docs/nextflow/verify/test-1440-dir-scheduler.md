# Test: Directives: Scheduler

**Spec files:** nf-1440-dir-scheduler.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1440-dir-scheduler.md, determine its classification.

### Checklist

1. Read nf-1440-dir-scheduler.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-arch, DIR-clusterOptions, DIR-penv, DIR-pod, DIR-queue

### Output format

```
DIR-arch: SUPPORTED | reason
...
```

## Results

- DIR-arch: GAP — `resolveArchOptions` in `nextflowdsl/translate.go` only applies `arch` for `lsf` scheduling and only recognizes the exact strings `linux/x86_64` and `linux/aarch64`; other schedulers and values are ignored, so this does not fully match Nextflow semantics.
- DIR-clusterOptions: GAP — `buildRequirements` forwards `clusterOptions` via `resolveDirectiveString` to `req.Other["scheduler_misc"]`, but `resolveDirectiveString` only accepts values that evaluate to a single string, so non-string variants do not match Nextflow's broader directive behavior.
- DIR-penv: UNSUPPORTED — there is no `penv` handling in the directive application list or in `buildRequirements` in `nextflowdsl/translate.go`; this is a scheduler-specific directive for SGE-style environments, and wr does not implement an SGE scheduler path here.
- DIR-pod: FUTURE — there is no `pod` handling in the directive application list or in `buildRequirements` in `nextflowdsl/translate.go`; this could theoretically be mapped for Kubernetes execution, but it would require new translation logic.
- DIR-queue: SUPPORTED — `buildRequirements` resolves `queue` with `resolveDirectiveString` and stores it as `req.Other["scheduler_queue"]`, which gives wr a scheduler queue selection matching the directive's practical effect.
