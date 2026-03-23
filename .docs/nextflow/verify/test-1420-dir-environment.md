# Test: Directives: Environment

**Spec files:** nf-1420-dir-environment.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1420-dir-environment.md, determine its classification.

### Checklist

1. Read nf-1420-dir-environment.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-afterScript, DIR-beforeScript, DIR-conda, DIR-container, DIR-containerOptions
  DIR-module, DIR-spack

### Output format

```
DIR-afterScript: SUPPORTED | reason
...
```
