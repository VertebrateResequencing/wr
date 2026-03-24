# Test: Operators: Combining

**Spec files:** nf-3200-operators-combining.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3200-operators-combining.md, determine its classification.

### Checklist

1. Read nf-3200-operators-combining.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-collectFile, CO-combine, CO-concat, CO-cross, CO-join
  CO-merge, CO-mix

### Output format

```
CO-collectFile: SUPPORTED | reason
...
```

## Results

- CO-collectFile: GAP — `collectFileChannelItems` in `nextflowdsl/channel.go` writes all items to a single file and only honors the `name` option; grouping/closure-driven naming and broader `collectFile` behaviors are not implemented.
- CO-combine: GAP — `applyChannelOperator` and `combineChannelItems` implement the basic cartesian product and a single integer `by` key via `resolveOperatorByIndex`, but broader `combine` variants/options are not supported.
- CO-concat: SUPPORTED — `applyChannelOperator` resolves each extra channel and `concatChannelItems` appends them in sequence, matching `concat` semantics.
- CO-cross: GAP — `crossChannelItems` produces the full cartesian product of left and right items and `applyChannelOperator` ignores operator arguments, so keyed `cross` semantics are not matched.
- CO-join: GAP — `joinChannelItems` only performs a default first-element key join using `channelItemKey` and ignores join options/variants in `applyChannelOperator`.
- CO-merge: GAP — `merge` is only listed in `deprecatedChannelOperators`; the default branch warns and returns the original items unchanged, so merged channel inputs are ignored.
- CO-mix: SUPPORTED — `applyChannelOperator` for `mix` resolves each input channel and appends all items into one output stream, preserving the operator’s practical effect.
