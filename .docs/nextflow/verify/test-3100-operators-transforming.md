# Test: Operators: Transforming

**Spec files:** nf-3100-operators-transforming.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3100-operators-transforming.md, determine its classification.

### Checklist

1. Read nf-3100-operators-transforming.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-buffer, CO-collate, CO-collect, CO-count, CO-flatMap
  CO-flatten, CO-groupTuple, CO-map, CO-max, CO-min
  CO-reduce, CO-sum, CO-toInteger, CO-toList, CO-toSortedList
  CO-transpose

### Output format

```
CO-buffer: SUPPORTED | reason
...
```

## Results

- CO-buffer: GAP — `applyChannelOperator` handles `buffer` only through `resolveChunkSize` and `chunkChannelItems` in `nextflowdsl/channel.go`, so only fixed-size chunking is implemented; other buffer modes/options are not.
- CO-collate: GAP — `applyChannelOperator` handles `collate` with the same `resolveChunkSize` plus `chunkChannelItems` path as `buffer`, so it supports only fixed-size chunking and not the fuller collate variants.
- CO-collect: GAP — `applyChannelOperator` implements `collect` as a plain `collectChannelValues(items)` wrapper in `nextflowdsl/channel.go` and ignores operator args/options, so only the basic collect behaviour is present.
- CO-count: GAP — `countChannelItems` in `nextflowdsl/channel.go` supports plain counting, a simple closure filter, or exact-value equality via `channelOperatorMatches`; broader matcher variants are not implemented.
- CO-flatMap: GAP — `applyChannelOperator` evaluates only compile-time-resolvable closures through `evalChannelClosure`, and unsupported closures become pass-through; flattening is limited to scalars, `[]any`, and `[]string` via `flattenChannelValues`.
- CO-flatten: SUPPORTED — `applyChannelOperator` calls `flattenSliceValue`, and `flattenSliceValue` in `nextflowdsl/groovy.go` recursively flattens nested slices/arrays, which matches the core flatten behaviour.
- CO-groupTuple: GAP — `groupTupleItems` in `nextflowdsl/channel.go` always groups on tuple element `0` and collects remaining positions into lists; operator arguments/options are not read.
- CO-map: GAP — `applyChannelOperator` uses `evalChannelClosure` for compile-time-evaluable closures only, and unsupported closures are downgraded to pass-through with `warnUnsupportedChannelClosure`.
- CO-max: GAP — `applyChannelOperator` routes `max` to `reduceChannelItems(maxChannelValue)`, but `maxChannelValue` only compares `int` and `string` values and ignores any comparator/closure arguments.
- CO-min: GAP — `applyChannelOperator` routes `min` to `reduceChannelItems(minChannelValue)`, but `minChannelValue` only compares `int` and `string` values and ignores any comparator/closure arguments.
- CO-reduce: GAP — `reduceOperatorSeedAndClosure` supports only the basic seed-plus-closure forms, and the reduction closure is evaluated at compile time through `evalChannelClosure`, so complex runtime Groovy reductions are not supported.
- CO-sum: GAP — `sumChannelItems` in `nextflowdsl/channel.go` sums only `int` items and does not implement richer sum variants.
- CO-toInteger: GAP — `toInteger` appears only in `deprecatedChannelOperators` in `nextflowdsl/channel.go`; the default path warns and returns items unchanged, so no integer conversion happens.
- CO-toList: SUPPORTED — `applyChannelOperator` emits a single item containing `collectChannelValues(items)` in `nextflowdsl/channel.go`, matching the basic `toList` behaviour.
- CO-toSortedList: GAP — `sortedChannelValues` sorts collected values using `lessSortableValue`, which only supports `int` and `string`; comparator/closure variants and other comparable types are unsupported.
- CO-transpose: GAP — `transposeChannelItems` in `nextflowdsl/channel.go` expands list-valued tuple positions independently, producing cartesian expansion across indexed columns rather than a strict position-wise transpose.
