# Test: Typed Process Sections

**Spec files:** nf-1600-typed-process.md
**Impl files:** impl-01-parse.md, impl-05-translate-directives.md

## Task

For each feature ID in nf-1600-typed-process.md, determine its classification.

### Checklist

1. Check if typed input syntax (`: Type`) is parsed (impl-01-parse.md)
2. Check if `stage:` section is parsed and translated
3. Check if typed output functions (`env()`, `eval()`, `file()`, `files()`,
   `stdout()`) are handled in translation
4. Check if `topic:` section with `>>` syntax is parsed
5. Classify per 00-instructions.md criteria

### Features to classify

- PSEC-typed-input
- PSEC-stage, PSEC-stage-env, PSEC-stage-stageAs, PSEC-stage-stdin
- PSEC-typed-output, PSEC-typed-output-env, PSEC-typed-output-eval,
  PSEC-typed-output-file, PSEC-typed-output-files, PSEC-typed-output-stdout
- PSEC-topic

### Output format

```
PSEC-typed-input: FUTURE | reason
...
```
