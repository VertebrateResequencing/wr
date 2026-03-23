# Test: Operators — Combining

**Spec files:** nf-3200-operators-combining.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3200-operators-combining.md, determine its classification.

### Checklist

1. Check applyChannelOperator switch (channel.go) for each operator
2. Check combineChannelItems, crossChannelItems, joinChannelItems helpers
3. Classify per 00-instructions.md criteria

### Features to classify

- CO-mix, CO-join, CO-join-by, CO-join-remainder,
  CO-combine, CO-combine-by, CO-combine-with,
  CO-concat, CO-cross, CO-cross-by, CO-merge

### Output format

```
CO-mix: SUPPORTED | reason
...
```
