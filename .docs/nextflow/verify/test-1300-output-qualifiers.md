# Test: Output Qualifiers

**Spec files:** nf-1300-output-qualifiers.md
**Impl files:** impl-01-parse.md, impl-06-translate-command.md, impl-09-ast.md

## Task

For each feature ID in nf-1300-output-qualifiers.md, determine its classification.

### Checklist

1. Check Declaration struct (ast.go) for output kind support
2. Check parse.go for output qualifier parsing (warnUnsupportedOutputQualifier etc.)
3. Check evalOutputCaptureLines (translate.go) for output capture
4. Classify per 00-instructions.md criteria

### Features to classify

- OUT-val, OUT-path, OUT-path-glob, OUT-path-named, OUT-tuple,
  OUT-env, OUT-stdout, OUT-eval, OUT-topic
- OUT-opt-emit, OUT-opt-optional, OUT-opt-topic, OUT-opt-arity

### Output format

```
OUT-val: SUPPORTED | reason
...
```
