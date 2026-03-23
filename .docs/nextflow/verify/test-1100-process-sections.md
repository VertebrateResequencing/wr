# Test: Process Sections

**Spec files:** nf-1100-process-sections.md
**Impl files:** impl-01-parse.md, impl-04-translate-jobs.md, impl-06-translate-command.md, impl-09-ast.md

## Task

For each feature ID in nf-1100-process-sections.md, determine its classification.

### Checklist

1. Check Process struct (ast.go) for field presence
2. Check parseWorkflow (parse.go) for section parsing
3. Check buildCommandBody/renderScript (translate.go) for execution
4. Classify per 00-instructions.md criteria

### Features to classify

- PSEC-input, PSEC-output, PSEC-script, PSEC-shell, PSEC-exec,
  PSEC-stub, PSEC-when

### Output format

```
PSEC-input: SUPPORTED | reason
...
```
