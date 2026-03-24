# Test: Operators: Other

**Spec files:** nf-3500-operators-other.md
**Impl files:** impl-02-channel-operators.md

## Task

For each feature ID in nf-3500-operators-other.md, determine its classification.

### Checklist

1. Read nf-3500-operators-other.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CO-dump, CO-subscribe, CO-view

### Output format

```
CO-dump: SUPPORTED | reason
...
```

## Results

- CO-dump: GAP — `applyChannelOperator` in `nextflowdsl/channel.go` handles `dump` via the `case "dump", "set", "tap", "view": return cloneChannelItems(items), nil` pass-through path, so it parses but does not perform any dump/output side effect.
- CO-subscribe: GAP — `warningOnlyChannelOperators` includes `subscribe`, and the default path in `applyChannelOperator` warns then returns `cloneChannelItems(items)` unchanged, so it parses but does not perform subscription/callback behaviour.
- CO-view: GAP — `applyChannelOperator` in `nextflowdsl/channel.go` handles `view` via the same pass-through branch as `dump`, returning items unchanged without any view/output side effect.
