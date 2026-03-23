# Test: Built-in Variables, Functions and Namespaces

**Spec files:** nf-4500-builtins.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-4500-builtins.md, determine its classification.

### Checklist

1. Read nf-4500-builtins.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- BV-baseDir, BV-launchDir, BV-moduleDir, BV-params, BV-projectDir
  BV-secrets, BV-workDir, BV-branchCriteria, BV-env, BV-error
  BV-exit, BV-file, BV-checkIfExists, BV-followLinks, BV-glob
  BV-hidden, BV-maxDepth, BV-type, BV-files, BV-groupKey
  BV-multiMapCriteria, BV-print, BV-printf, BV-println, BV-sendMail
  BV-sleep, BV-record, BV-tuple, BV-log-error, BV-log-info
  BV-log-warn, BV-nextflow-build, BV-nextflow-timestamp, BV-nextflow-version, BV-workflow-commandLine
  BV-workflow-commitId, BV-workflow-complete, BV-workflow-configFiles, BV-workflow-container, BV-workflow-containerEngine
  BV-workflow-duration, BV-workflow-errorMessage, BV-workflow-errorReport, BV-workflow-exitStatus, BV-workflow-failOnIgnore
  BV-workflow-fusion, BV-workflow-enabled, BV-workflow-version, BV-workflow-homeDir, BV-workflow-launchDir
  BV-workflow-manifest, BV-workflow-outputDir, BV-workflow-preview, BV-workflow-profile, BV-workflow-projectDir
  BV-workflow-repository, BV-workflow-resume, BV-workflow-revision, BV-workflow-runName, BV-workflow-scriptFile
  BV-workflow-scriptId, BV-workflow-scriptName, BV-workflow-sessionId, BV-workflow-start, BV-workflow-stubRun
  BV-workflow-success, BV-workflow-userName, BV-workflow-wave, BV-workflow-workDir, BV-workflow-onComplete
  BV-workflow-onError

### Output format

```
BV-baseDir: SUPPORTED | reason
...
```
