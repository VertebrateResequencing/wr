# Test: Operators: Forking

**Spec files:** nf-3400-operators-forking.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3400-operators-forking.md, determine its classification.

### Checklist

1. Read nf-3400-operators-forking.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-branch, CO-ifEmpty, CO-multiMap, CO-set, CO-tap

### Output format

```
CO-branch: SUPPORTED | reason
...
```

## Results

- CO-branch: SUPPORTED — `applyChannelOperator` dispatches `branch` to `resolveBranchChannelItems`, which evaluates named entries in order, applies `default` fallback, and labels emitted items; downstream label selection is implemented by `NamedChannelRef` plus `selectNamedChannelItems` in `channel.go`.
- CO-ifEmpty: GAP — `applyChannelOperator` implements the non-empty and single-argument default-value cases, but only via `operator.Args`; closure-form `ifEmpty { ... }` parses as a closure operator and is not handled here, so that variant does not match Nextflow semantics.
- CO-multiMap: SUPPORTED — `applyChannelOperator` dispatches `multiMap` to `resolveMultiMapChannelItems`, which evaluates every named mapping for each item and labels results so `NamedChannelRef` can select each forked output.
- CO-set: GAP — parsing accepts `.set { ... }`, but runtime handling in `applyChannelOperator` groups `set` with `dump`, `tap`, and `view` as a pure pass-through, so no named channel binding or side effect is created.
- CO-tap: GAP — parsing accepts both closure and channel-argument forms (`parseChannelOperatorArgs` handles `tap` channels), but runtime handling in `applyChannelOperator` is pass-through only and ignores the side-channel target.
