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

## Results

- OUT-val: GAP — `staticOutputValue` in `nextflowdsl/translate.go` only evaluates against `outputVarsWithValues` (named inputs plus `params`) and otherwise falls back to the single binding or `""`; it does not capture general runtime-generated values the way Nextflow `val(...)` can.
- OUT-file: SUPPORTED — `parseDeclarationPrimary` in `nextflowdsl/parse.go` accepts `file(...)`, and `outputPatterns`, `outputPaths`, `outputValue`, and `completedOutputPathsForJob` in `nextflowdsl/translate.go` handle `file` exactly like `path`, including glob expansion to concrete completed files.
- OUT-path: SUPPORTED — `parseDeclarationPrimary` accepts `path(...)`, and `outputPatterns`, `outputPaths`, `resolveCompletedOutputValue`, and `completedOutputPathsForJob` in `nextflowdsl/translate.go` carry path patterns through translation and resolve them to concrete output paths after job completion.
- OUT-path-arity: GAP — extra `path(...)` options are silently discarded because `parseDeclarationPrimary` only uses the first comma-separated argument inside the call, and although tuple elements accept an `arity` qualifier in `applyTupleElementQualifier`, no arity value is stored in the AST or enforced anywhere in `nextflowdsl/translate.go`.
- OUT-path-followLinks: GAP — `followLinks` is parsed only as an ignored extra `path(...)` argument because `parseDeclarationPrimary` keeps just the first inner argument, and there is no follow-links handling in `nextflowdsl/translate.go`.
- OUT-path-glob: GAP — basic glob patterns in `path('*.txt')` are supported by `outputPatterns` and `expandCompletedOutputPattern`, but the `glob` option itself is ignored because additional `path(...)` arguments are discarded in `parseDeclarationPrimary`.
- OUT-path-hidden: GAP — the `hidden` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and no hidden-file filtering logic exists in `nextflowdsl/translate.go`.
- OUT-path-includeInputs: GAP — the `includeInputs` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and translation only matches completed output paths, not declared inputs, in `completedOutputPathsForJob` and `resolveCompletedOutputValue`.
- OUT-path-maxDepth: GAP — the `maxDepth` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and `expandCompletedOutputPattern` only uses `filepath.Glob`, with no recursive depth control.
- OUT-path-type: GAP — the `type` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and translation never filters outputs by file type in `nextflowdsl/translate.go`.
- OUT-env: GAP — `parseDeclarationPrimary` accepts `env(name)`, but `staticOutputValue` only looks up the name in `outputVarsWithValues` (inputs and `params`), so it does not capture the process environment value produced at runtime.
- OUT-stdout: GAP — commands are wrapped by `captureCommand` to write stdout into `.nf-stdout`, but `outputValue` and `outputValueForDeclaration` never surface that captured stdout as the channel item, so `stdout` outputs do not behave like Nextflow stdout channels.
- OUT-eval: GAP — `evalOutputCaptureLines` appends shell lines that capture command output into `__nf_eval_*`, but `outputValue` falls back to `staticOutputValue`, which evaluates the `eval(...)` expression itself and returns the command string rather than the captured command output.
- OUT-tuple: GAP — `parseTupleDeclaration` and `tupleOutputValue` support tuples of `path`/`file` plus static value-like elements, but tuple elements that rely on runtime captures such as `env`, `stdout`, or `eval` still go through `staticOutputValue`, so tuple output semantics are only partially implemented.
