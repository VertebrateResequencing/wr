# Test: Types, Literals and Operators

**Spec files:** nf-0200-types-operators.md
**Impl files:** impl-01-parse.md, impl-08-groovy.md

## Task

For each feature ID in nf-0200-types-operators.md, determine its classification.

### Checklist

1. Read nf-0200-types-operators.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- SYN-variable, SYN-number, SYN-boolean, SYN-null, SYN-list
  SYN-map, SYN-unary-expressions, SYN-binary-expressions, SYN-ternary-expression, SYN-parentheses
  SYN-precedence

### Output format

```
SYN-variable: SUPPORTED | reason
...
```

## Results

- SYN-variable: SUPPORTED — `parseVarExprTokens` in `nextflowdsl/parse.go` parses identifiers and dotted paths, and `EvalExpr`/`resolveExprPath` in `nextflowdsl/groovy.go` resolve `VarExpr` and `ParamsExpr` values from the evaluation context.
- SYN-number: GAP — the lexer only emits `tokenInt` via `readInt` in `nextflowdsl/parse.go`, `parsePrimaryExprTokens` only builds `IntExpr`, and arithmetic in `nextflowdsl/groovy.go` goes through `requireIntegerOperand`, so integer literals work but floating-point/decimal numeric literals and arithmetic do not match Nextflow/Groovy semantics.
- SYN-boolean: SUPPORTED — `parsePrimaryExprTokens` maps `true` and `false` to `BoolExpr`, and `EvalExpr` returns native booleans in `nextflowdsl/groovy.go`.
- SYN-null: SUPPORTED — `parsePrimaryExprTokens` maps `null` to `NullExpr`, and `EvalExpr` returns `nil`; `isTruthy` also treats nil as false in `nextflowdsl/groovy.go`.
- SYN-list: SUPPORTED — `parseCollectionExprTokens` in `nextflowdsl/parse.go` parses `[]` and comma-separated list literals into `ListExpr`, and `evalListExpr` in `nextflowdsl/groovy.go` evaluates elements into a Go slice.
- SYN-map: GAP — `parseCollectionExprTokens` parses map literals, but `parseMapKeyExpr` coerces bare identifier keys to strings and `evalMapExpr` only accepts keys that evaluate to `string`, so map support is narrower than Groovy/Nextflow's general map semantics.
- SYN-unary-expressions: GAP — `parseUnaryExprTokens` and `evalUnaryExpr` only handle `!`, `-`, and `~`; unary `+` and increment/decrement forms are not implemented, and `!` requires a real boolean through `evalBoolOperand` rather than Groovy truthiness.
- SYN-binary-expressions: GAP — the parser has precedence layers (`parseLogicalOrExprTokens`, `parseAdditiveExprTokens`, `parseMultiplicativeExprTokens`, etc.), but `evalBinaryExpr` in `nextflowdsl/groovy.go` only gives correct semantics for a limited subset (primarily bool/int comparisons and integer arithmetic). It also relies on `requireIntegerOperand`, so non-integer numeric operations differ, and repeated same-precedence expressions can leave `UnsupportedExpr` operands because `parseBinaryExprTokens` parses both sides with the lower-precedence operand parser.
- SYN-ternary-expression: SUPPORTED — `parseTernaryExprTokens` in `nextflowdsl/parse.go` parses both `cond ? a : b` and Elvis-style `a ?: b`, and `evalTernaryExpr` in `nextflowdsl/groovy.go` evaluates them using `isTruthy`.
- SYN-parentheses: SUPPORTED — `isParenthesisedExpr` in `nextflowdsl/parse.go` detects surrounding parentheses and `parsePrimaryExprTokens` reparses the inner expression, so explicit grouping is supported.
- SYN-precedence: GAP — precedence levels are encoded in the recursive descent parser (`parseExprTokens` through `parsePrimaryExprTokens`), so mixed-precedence forms like additive vs multiplicative are structured, but associativity within the same precedence level is incomplete because `parseBinaryExprTokens` parses left and right operands with the next lower parser. Expressions like chained additions therefore do not evaluate with full Groovy/Nextflow semantics unless extra parentheses are added.
