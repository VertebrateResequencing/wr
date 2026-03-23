# Test: Dynamic Directives & Task Properties

**Spec files:** nf-1500-dynamic-directives.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1500-dynamic-directives.md, determine its classification.

### Checklist

1. Check resolveDirectiveValue/evalDirectiveExpr (translate.go) for dynamic evaluation
2. Check defaultDirectiveTask() for available task properties
3. Check interpolateKnownScriptVars for task.* in scripts
4. Classify per 00-instructions.md criteria

### Features to classify

- DIR-dynamic
- BV-task-cpus, BV-task-memory, BV-task-time, BV-task-disk,
  BV-task-attempt, BV-task-name, BV-task-process, BV-task-index,
  BV-task-hash, BV-task-workDir, BV-task-container, BV-task-ext

### Output format

```
DIR-dynamic: SUPPORTED | reason
...
```
