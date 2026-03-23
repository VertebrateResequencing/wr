# Test: Directives — Execution

**Spec files:** nf-1410-dir-execution.md
**Impl files:** impl-05-translate-directives.md, impl-07-config.md

## Task

For each feature ID in nf-1410-dir-execution.md, determine its classification.

### Checklist

1. Check applyContainer (translate.go line 2350) for container
2. Check applyErrorStrategy (line 2478) for error handling
3. Check applyMaxForks (line 2394) for concurrency
4. Check ProcessDefaults for config fields
5. Classify per 00-instructions.md criteria

### Features to classify

- DIR-container, DIR-errorStrategy, DIR-maxRetries, DIR-maxForks,
  DIR-maxErrors, DIR-executor

### Output format

```
DIR-container: SUPPORTED | reason
...
```
