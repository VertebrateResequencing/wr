# Test: Typed Process (Preview)

**Spec files:** nf-1600-typed-process.md
**Impl files:** impl-04-translate-jobs.md

## Task

For each feature ID in nf-1600-typed-process.md, determine its classification.

### Checklist

1. Read nf-1600-typed-process.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- PSEC-inputs-and-outputs-typed, PSEC-stage-directives, PSEC-typed-outputs, PSEC-typed-env, PSEC-typed-stageAs
  PSEC-typed-stageAs-2, PSEC-typed-stdin, PSEC-typed-env-1, PSEC-typed-eval, PSEC-typed-file
  PSEC-typed-file-followLinks, PSEC-typed-file-glob, PSEC-typed-file-hidden, PSEC-typed-file-includeInputs, PSEC-typed-file-maxDepth
  PSEC-typed-file-optional, PSEC-typed-file-type, PSEC-typed-files, PSEC-typed-stdout

### Output format

```
PSEC-inputs-and-outputs-typed: SUPPORTED | reason
...
```
