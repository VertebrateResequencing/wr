# Test: Directives: Resources

**Spec files:** nf-1400-dir-resources.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1400-dir-resources.md, determine its classification.

### Checklist

1. Read nf-1400-dir-resources.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-accelerator, DIR-cpus, DIR-disk, DIR-machineType, DIR-memory
  DIR-resourceLabels, DIR-resourceLimits, DIR-time

### Output format

```
DIR-accelerator: SUPPORTED | reason
...
```
