# Test: Channel Factories

**Spec files:** nf-2500-channel-factories.md
**Impl files:** impl-03-channel-factories.md

## Task

For each feature ID in nf-2500-channel-factories.md, determine its classification.

### Checklist

1. Check resolveChannelFactoryItems switch (channel.go line 91) for each factory
2. Check supportedChannelFactories map (parse.go line 101) for parsing
3. Note warning-only factories that are parsed but produce empty results
4. Classify per 00-instructions.md criteria

### Features to classify

- CF-of, CF-from, CF-fromList, CF-value, CF-empty,
  CF-fromPath, CF-fromPath-glob, CF-fromPath-opts,
  CF-fromFilePairs, CF-fromFilePairs-opts,
  CF-fromSRA, CF-watchPath, CF-topic, CF-interval, CF-fromLineage

### Output format

```
CF-of: SUPPORTED | reason
...
```
