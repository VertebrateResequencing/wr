# Test: Operators — Transforming

**Spec files:** nf-3100-operators-transforming.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3100-operators-transforming.md, determine its classification.

### Checklist

1. Check applyChannelOperator switch (channel.go line 357) for each operator
2. Verify closure evaluation support
3. Classify per 00-instructions.md criteria

### Features to classify

- CO-map, CO-flatMap, CO-flatten, CO-collect, CO-collect-closure,
  CO-groupTuple, CO-groupTuple-by, CO-groupTuple-size,
  CO-transpose, CO-transpose-by,
  CO-toList, CO-toSortedList, CO-toSortedList-closure,
  CO-reduce, CO-reduce-seed, CO-count, CO-count-filter, CO-ifEmpty

### Output format

```
CO-map: SUPPORTED | reason
...
```
