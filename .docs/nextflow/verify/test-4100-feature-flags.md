# Test: Feature Flags

**Spec files:** nf-4100-feature-flags.md
**Impl files:** impl-07-config.md

## Task

For each feature ID in nf-4100-feature-flags.md, determine its classification.

### Checklist

1. Read nf-4100-feature-flags.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CFG-enable-configProcessNamesValidation, CFG-enable-dsl, CFG-enable-moduleBinaries, CFG-enable-strict, CFG-preview-output
  CFG-preview-recursion, CFG-preview-topic, CFG-preview-types

### Output format

```
CFG-enable-configProcessNamesValidation: SUPPORTED | reason
...
```
