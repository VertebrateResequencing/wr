# Test: Directives: Execution

**Spec files:** nf-1410-dir-execution.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1410-dir-execution.md, determine its classification.

### Checklist

1. Read nf-1410-dir-execution.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-cache, DIR-errorStrategy, DIR-executor, DIR-fair, DIR-maxErrors
  DIR-maxForks, DIR-maxRetries, DIR-maxSubmitAwait, DIR-scratch, DIR-storeDir

### Output format

```
DIR-cache: SUPPORTED | reason
...
```
