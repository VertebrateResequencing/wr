# Test: Operators — Other

**Spec files:** nf-3500-operators-other.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3500-operators-other.md, determine its classification.

### Checklist

1. Check applyChannelOperator switch for each operator
2. Check warningOnlyChannelOperators and deprecatedChannelOperators maps
3. Classify per 00-instructions.md criteria

### Features to classify

- CO-view, CO-dump
- CO-buffer, CO-buffer-close, CO-buffer-open-close, CO-buffer-size, CO-buffer-skip
- CO-collate, CO-collate-size, CO-collate-step
- CO-min, CO-min-closure, CO-max, CO-max-closure, CO-sum, CO-sum-closure
- CO-randomSample
- CO-toInteger, CO-toLong, CO-toFloat, CO-toDouble
- CO-subscribe
- CO-countFasta, CO-countFastq, CO-countJson, CO-countLines

### Output format

```
CO-view: SUPPORTED | reason
...
```
