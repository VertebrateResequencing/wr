# Test: Built-in Variables & Global Functions

**Spec files:** nf-4500-builtins.md
**Impl files:** impl-08-groovy.md, impl-05-translate-directives.md

## Task

For each feature ID in nf-4500-builtins.md, determine its classification.

### Checklist

1. Check resolveExprPath (groovy.go line 678) for variable resolution
2. Check defaultDirectiveTask() for task.* properties
3. Check evalStaticMethodCall and evalMethodCallExpr for global functions
4. Classify per 00-instructions.md criteria

### Features to classify

Built-in variables:
- BV-baseDir, BV-projectDir, BV-launchDir, BV-moduleDir, BV-workDir

Workflow object:
- BV-workflow-projectDir, BV-workflow-launchDir, BV-workflow-workDir,
  BV-workflow-profile, BV-workflow-configFiles, BV-workflow-runName,
  BV-workflow-sessionId, BV-workflow-resume, BV-workflow-revision,
  BV-workflow-commitId, BV-workflow-repository, BV-workflow-scriptName,
  BV-workflow-scriptFile, BV-workflow-start, BV-workflow-complete,
  BV-workflow-success, BV-workflow-failOnIgnore

Nextflow object:
- BV-nextflow-version, BV-nextflow-build, BV-nextflow-timestamp

Log:
- BV-log-info, BV-log-warn, BV-log-error

Global functions:
- GF-file, GF-groupKey, GF-branchCriteria, GF-multiMapCriteria,
  GF-error, GF-exit, GF-println, GF-tuple, GF-env, GF-sleep, GF-sendMail

### Output format

```
BV-baseDir: SUPPORTED | reason
...
```
