# Test: Operators: Filtering

**Spec files:** nf-3000-operators-filtering.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3000-operators-filtering.md, determine its classification.

### Checklist

1. Read nf-3000-operators-filtering.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-distinct, CO-filter, CO-first, CO-last, CO-randomSample
  CO-take, CO-unique, CO-until

### Output format

```
CO-distinct: SUPPORTED | reason
...
```

## Results

- CO-distinct: GAP — `applyChannelOperator` dispatches `distinct` to `distinctChannelItems`, which only removes consecutive duplicate whole values via `reflect.DeepEqual`; parsed args/closures are ignored, so keyed/comparator variants are not implemented.
- CO-filter: GAP — `applyChannelOperator` handles `filter` only through `evalChannelClosureBool`; parsed args are ignored entirely, and unsupported closures trigger `warnUnsupportedChannelClosure` and return the original items unchanged.
- CO-first: SUPPORTED — `applyChannelOperator` returns `items[:1]` for `first`, matching the basic first-item behavior and returning no items for an empty channel.
- CO-last: SUPPORTED — `applyChannelOperator` returns the final item with `items[len(items)-1:]` for `last`, matching the basic last-item behavior and returning no items for an empty channel.
- CO-randomSample: SUPPORTED — `randomSampleChannelItems` implements sampling without replacement, supports an optional integer seed via `randomSampleOperatorArgs`, and preserves selected items in source order after sampling.
- CO-take: SUPPORTED — `applyChannelOperator` evaluates a single integer argument for `take` and returns the first `n` items, clamping to channel length and yielding no items for non-positive counts.
- CO-unique: GAP — `applyChannelOperator` dispatches `unique` to `uniqueChannelItems`, which performs whole-value de-duplication across the full channel but ignores any parsed args or closure variants.
- CO-until: GAP — `until` is listed in `warningOnlyChannelOperators`; `applyChannelOperator` falls through to the warning-only path and returns items unchanged instead of stopping on a predicate.
