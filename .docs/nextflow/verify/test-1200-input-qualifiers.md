# Test: Input Qualifiers

**Spec files:** nf-1200-input-qualifiers.md
**Impl files:** impl-01-parse.md, impl-04-translate-jobs.md, impl-09-ast.md

## Task

For each feature ID in nf-1200-input-qualifiers.md, determine its classification.

### Checklist

1. Check Declaration/TupleElement structs (ast.go) for kind support
2. Check parseTupleElement/applyTupleElementQualifier (parse.go) for parsing
3. Check resolveBindings (translate.go) for binding resolution
4. Classify per 00-instructions.md criteria

### Features to classify

- INP-val, INP-path, INP-path-named, INP-path-glob, INP-tuple,
  INP-env, INP-stdin, INP-each
- INP-opt-stageAs, INP-opt-arity

### Output format

```
INP-val: SUPPORTED | reason
...
```
