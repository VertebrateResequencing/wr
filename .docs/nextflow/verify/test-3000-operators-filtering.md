# Test: Operators — Filtering

**Spec files:** nf-3000-operators-filtering.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3000-operators-filtering.md, determine its classification.

### Checklist

1. Check applyChannelOperator switch (channel.go line 357) for each operator
2. Look for specific handling (e.g. filter with closure vs regex)
3. Classify per 00-instructions.md criteria

### Features to classify

- CO-filter, CO-filter-closure, CO-filter-regex, CO-filter-type, CO-filter-literal
- CO-first, CO-first-no-arg, CO-first-closure, CO-first-regex, CO-first-type
- CO-last
- CO-take
- CO-unique, CO-unique-closure
- CO-distinct
- CO-until

### Output format

```
CO-filter: SUPPORTED | reason
...
```
