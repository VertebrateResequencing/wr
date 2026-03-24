# Test: Process Sections

**Spec files:** nf-1100-process-sections.md
**Impl files:** impl-01-parse.md, impl-04-translate-jobs.md

## Task

For each feature ID in nf-1100-process-sections.md, determine its classification.

### Checklist

1. Read nf-1100-process-sections.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- PSEC-inputs-and-outputs-legacy, PSEC-inputs, PSEC-generic-options, PSEC-generic-emit, PSEC-generic-optional
  PSEC-generic-topic, PSEC-directives, PSEC-script, PSEC-exec, PSEC-stub

### Output format

```
PSEC-inputs-and-outputs-legacy: SUPPORTED | reason
...
```
