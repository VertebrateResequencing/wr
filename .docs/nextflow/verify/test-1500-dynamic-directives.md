# Test: Dynamic Directives & Task Properties

**Spec files:** nf-1500-dynamic-directives.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1500-dynamic-directives.md, determine its classification.

### Checklist

1. Read nf-1500-dynamic-directives.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- BV-task-attempt, BV-task-exitStatus, BV-task-hash, BV-task-index, BV-task-name
  BV-task-previousException, BV-task-previousTrace, BV-task-process, BV-task-workDir

### Output format

```
BV-task-attempt: SUPPORTED | reason
...
```
