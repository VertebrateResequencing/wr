# Test: Operators — Forking

**Spec files:** nf-3400-operators-forking.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3400-operators-forking.md, determine its classification.

### Checklist

1. Check applyChannelOperator switch for branch/multiMap
2. Check resolveBranchChannelItems/resolveMultiMapChannelItems helpers
3. Check set/tap handling (likely in workflow resolution, not operator switch)
4. Classify per 00-instructions.md criteria

### Features to classify

- CO-branch, CO-branch-criteria
- CO-multiMap, CO-multiMap-criteria
- CO-tap
- CO-set

### Output format

```
CO-branch: SUPPORTED | reason
...
```
