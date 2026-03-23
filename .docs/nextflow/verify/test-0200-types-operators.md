# Test: Types & Operators

**Spec files:** nf-0200-types-operators.md
**Impl files:** impl-01-parse.md, impl-08-groovy.md

## Task

For each feature ID in nf-0200-types-operators.md, determine its classification.

### Checklist

1. Check if the operator/type is parsed (impl-01-parse.md expression parsing)
2. Check if the operator/type is evaluated (impl-08-groovy.md evalBinaryExpr etc.)
3. Classify per 00-instructions.md criteria

### Features to classify

- SYN-int, SYN-float, SYN-big-decimal
- SYN-arith-plus, SYN-arith-minus, SYN-arith-mult, SYN-arith-div,
  SYN-arith-mod, SYN-arith-power
- SYN-unary-neg, SYN-unary-pos, SYN-unary-not, SYN-unary-bitwise-not,
  SYN-unary-pre-inc, SYN-unary-pre-dec, SYN-unary-post-inc, SYN-unary-post-dec
- SYN-compare-eq, SYN-compare-neq, SYN-compare-lt, SYN-compare-gt,
  SYN-compare-le, SYN-compare-ge, SYN-compare-spaceship,
  SYN-compare-identical, SYN-compare-not-identical
- SYN-log-and, SYN-log-or
- SYN-bitwise-and, SYN-bitwise-or, SYN-bitwise-xor,
  SYN-bitwise-lshift, SYN-bitwise-rshift, SYN-bitwise-urshift
- SYN-ternary, SYN-elvis
- SYN-regex, SYN-regex-find, SYN-regex-match
- SYN-range, SYN-range-exclusive
- SYN-instanceof, SYN-not-instanceof
- SYN-in, SYN-not-in
- SYN-cast, SYN-as-cast
- SYN-null-safe, SYN-spread
- SYN-index, SYN-index-negative, SYN-index-range
- SYN-assign-eq, SYN-assign-compound
- SYN-precedence

### Output format

```
SYN-int: SUPPORTED | reason
...
```
