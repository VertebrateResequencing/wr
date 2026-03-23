# Test: Channel Factories

**Spec files:** nf-2500-channel-factories.md
**Impl files:** impl-03-channel-factories.md

## Task

For each feature ID in nf-2500-channel-factories.md, determine its classification.

### Checklist

1. Read nf-2500-channel-factories.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CF-empty, CF-from, CF-fromLineage, CF-fromList, CF-fromPath
  CF-fromFilePairs, CF-fromSRA, CF-interval, CF-of, CF-topic
  CF-value, CF-watchPath

### Output format

```
CF-empty: SUPPORTED | reason
...
```
