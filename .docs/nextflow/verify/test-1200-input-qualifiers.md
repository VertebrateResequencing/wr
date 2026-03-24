# Test: Input Qualifiers & Options

**Spec files:** nf-1200-input-qualifiers.md
**Impl files:** impl-04-translate-jobs.md

## Task

For each feature ID in nf-1200-input-qualifiers.md, determine its classification.

### Checklist

1. Read nf-1200-input-qualifiers.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- INP-val, INP-file, INP-path, INP-path-arity, INP-path-name
  INP-path-stageAs, INP-env, INP-stdin, INP-tuple

### Output format

```
INP-val: SUPPORTED | reason
...
```

## Results

- INP-val: SUPPORTED — `resolveBindings` and `bindingsForInputDeclaration` pass non-tuple inputs through unchanged, and `buildCommandBody` exports named inputs so `val(x)` is available to the job as `x`.
- INP-file: GAP — identifier inputs work because translation treats `file` like a generic non-tuple binding, but the `stageName` form is not implemented: input translation never uses `decl.Expr`, so no staging/renaming semantics are applied.
- INP-path: GAP — identifier inputs are bound from upstream channel items in `resolveBindings`, but the `stageName` form is silently ignored because non-tuple input handling uses only tuple shape and `decl.Name`; there is no input-side staging logic.
- INP-path-arity: GAP — tuple element `arity:` is accepted by `applyTupleElementQualifier`, but the value is not stored on the AST and is never enforced by `resolveBindings` or `bindingsForInputDeclaration`; top-level `path(..., arity: ...)` would be rejected by `applyDeclarationQualifier`.
- INP-path-name: PARSE_ERROR — `name:` is not an accepted qualifier in either `applyDeclarationQualifier` or `applyTupleElementQualifier`.
- INP-path-stageAs: PARSE_ERROR — `stageAs:` is not an accepted qualifier in either `applyDeclarationQualifier` or `applyTupleElementQualifier`.
- INP-env: SUPPORTED — `parseDeclarationPrimary` captures the env variable name for `env(...)`, and `buildCommandBody` exports the bound value under that name.
- INP-stdin: GAP — `stdin` parses, but translation never connects the bound value to process standard input; `buildCommandBody` only exports bindings as environment variables.
- INP-tuple: SUPPORTED — `parseTupleDeclaration`, `bindingsForInputDeclaration`, and `flattenedInputDeclarations` bind tuple items element-by-element and expose named elements to the generated job.
