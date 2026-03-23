# Test: Output Qualifiers & Options

**Spec files:** nf-1300-output-qualifiers.md
**Impl files:** impl-04-translate-jobs.md

## Task

For each feature ID in nf-1300-output-qualifiers.md, determine its classification.

### Checklist

1. Read nf-1300-output-qualifiers.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- OUT-val, OUT-file, OUT-path, OUT-path-arity, OUT-path-followLinks
  OUT-path-glob, OUT-path-hidden, OUT-path-includeInputs, OUT-path-maxDepth, OUT-path-type
  OUT-env, OUT-stdout, OUT-eval, OUT-tuple

### Output format

```
OUT-val: SUPPORTED | reason
...
```
