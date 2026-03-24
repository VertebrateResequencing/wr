# Test: Syntax Basics

**Spec files:** nf-0100-syntax-basics.md
**Impl files:** impl-01-parse.md, impl-08-groovy.md

## Task

For each feature ID in nf-0100-syntax-basics.md, determine its classification.

### Checklist

1. Read nf-0100-syntax-basics.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- SYN-comments, SYN-shebang, SYN-feature-flag, SYN-function, SYN-enum-type
  SYN-record-type, SYN-variable-declaration, SYN-string, SYN-dynamic-string, SYN-closure
  SYN-index-expression, SYN-property-expression, SYN-function-call, SYN-constructor-call, SYN-multi-line-string
  SYN-multi-line-dynamic-string, SYN-slashy-string

### Output format

```
SYN-comments: SUPPORTED | reason
...
```

## Results

- SYN-comments: SUPPORTED — `lex` in `nextflowdsl/parse.go` skips `//` line comments and `/* */` block comments during tokenization.
- SYN-shebang: SUPPORTED — `lex` in `nextflowdsl/parse.go` skips a `#` line when `lineHasContent` is false, so a shebang-style first line is ignored by the parser.
- SYN-feature-flag: GAP — `skipFeatureFlagAssignment` in `nextflowdsl/parse.go` explicitly discards `nextflow.enable.*` and `nextflow.preview.*` assignments, so they parse but have no effect.
- SYN-function: GAP — `parseWorkflow` and `parseFunctionDef` in `nextflowdsl/parse.go` collect top-level functions, and `evalSimpleFuncDef` exists in `nextflowdsl/groovy.go`, but the inspected evaluator code exposes no reachable call/binding path for user-defined functions.
- SYN-enum-type: GAP — `parseEnumDef` in `nextflowdsl/parse.go` parses enums, while `bindWorkflowEnumValues` and `resolveEnumExprPath` in `nextflowdsl/groovy.go` only expose `Enum.VALUE` as plain string constants rather than full enum objects.
- SYN-record-type: GAP — `parseRecordDef` in `nextflowdsl/parse.go` stores record definitions on `Workflow.Records`, but the inspected evaluator code in `nextflowdsl/groovy.go` has no record construction or runtime field handling.
- SYN-variable-declaration: GAP — `parseStatement` routes `def` declarations to `parseAssignmentStmt` and `evalStatement` executes them inside statement bodies, but `parseWorkflow` treats top-level `def` only as function definitions and there is no typed declaration path here.
- SYN-string: GAP — `readString` in `nextflowdsl/parse.go` tokenizes both single- and double-quoted strings as `tokenString`, and `EvalExpr` in `nextflowdsl/groovy.go` always runs `StringExpr` through `interpolateGroovyString`, so literal strings are not kept distinct from dynamic strings.
- SYN-dynamic-string: GAP — `interpolateGroovyString` and `resolveInterpolation` in `nextflowdsl/groovy.go` only resolve `${...}` using dotted variable paths via `resolveExprPath`; full Groovy interpolation semantics are not implemented.
- SYN-closure: SUPPORTED — `parseClosureExpr` in `nextflowdsl/parse.go` parses closure literals, and `EvalExpr` in `nextflowdsl/groovy.go` preserves `ClosureExpr` values for supported consumers.
- SYN-index-expression: GAP — `parseIndexExprTokens` in `nextflowdsl/parse.go` parses `expr[index]`, but `evalIndexExpr` in `nextflowdsl/groovy.go` only handles positive integer indexes into slices/arrays and string-key map lookups.
- SYN-property-expression: GAP — `parseVarExprTokens` and `parsePropertyPathTokens` in `nextflowdsl/parse.go` parse dotted access, but `resolveExprPath` and `resolvePropertyPath` in `nextflowdsl/groovy.go` only walk map-like values, not general Groovy object property semantics.
- SYN-function-call: FUTURE — `parseMethodCallExprTokens` in `nextflowdsl/parse.go` requires `receiver.method(...)` or `receiver.method { ... }`; bare `foo()` calls are not parsed into a callable form, and `EvalExpr` in `nextflowdsl/groovy.go` has no top-level function invocation case.
- SYN-constructor-call: GAP — `parseNewExprTokens` in `nextflowdsl/parse.go` parses `new Class(...)`, but `evalNewExpr` in `nextflowdsl/groovy.go` only implements a small constructor whitelist and returns `UnsupportedExpr` for others.
- SYN-multi-line-string: GAP — `readString` in `nextflowdsl/parse.go` accepts triple-quoted literals, but `EvalExpr` in `nextflowdsl/groovy.go` still treats the resulting `StringExpr` as interpolated text rather than a distinct literal-only form.
- SYN-multi-line-dynamic-string: GAP — triple-quoted dynamic strings lex via `readString`, but interpolation is still limited by `interpolateGroovyString` and `resolveInterpolation` in `nextflowdsl/groovy.go` to `${...}` dotted-path substitutions.
- SYN-slashy-string: GAP — `readSlashyString` in `nextflowdsl/parse.go` tokenizes `/.../` as `SlashyStringExpr`, and `EvalExpr` in `nextflowdsl/groovy.go` returns its raw string value, which is usable as a regex-pattern string but does not implement full Groovy slashy-string semantics.
