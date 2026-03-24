# Test: Workflow

**Spec files:** nf-2000-workflow.md
**Impl files:** impl-04-translate-jobs.md, impl-01-parse.md

## Task

For each feature ID in nf-2000-workflow.md, determine its classification.

### Checklist

1. Read nf-2000-workflow.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- WF-workflow, WF-output-block, WF-take, WF-main, WF-emit
  WF-publish, WF-onComplete, WF-onError, WF-pipe, WF-and
  WF-recurse, WF-until, WF-times, WF-out-directory, WF-out-mode
  WF-out-overwrite, WF-out-path, WF-out-index, WF-out-index-header, WF-out-index-path
  WF-out-index-sep, WF-out-enabled, WF-out-tags, WF-out-contentType, WF-out-label
  WF-out-ignoreErrors, WF-out-storageClass, WF-out-copyAttributes

### Output format

```
WF-workflow: SUPPORTED | reason
...
```
