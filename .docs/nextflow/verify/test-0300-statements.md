# Test: Statements

**Spec files:** nf-0300-statements.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0300-statements.md, determine its classification.

### Checklist

1. Read nf-0300-statements.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- STMT-assignment, STMT-expression-statement, STMT-assert, STMT-ifelse, STMT-return
  STMT-throw, STMT-trycatch, STMT-multi-assignment

### Output format

```
STMT-assignment: SUPPORTED | reason
...
```

## Results

- STMT-assignment: SUPPORTED — `parseAssignmentStmt` in `nextflowdsl/groovy.go` parses `def x = ...`, `x = ...`, and augmented assignment for bare identifiers, and `evalStatement` executes them via `evalAssignStmt` and `evalAugAssignStmt` by updating the evaluation scope.
- STMT-expression-statement: SUPPORTED — `parseStatement` falls back to parsing an expression statement, and `evalStatement` executes `evalExprStmt`, returning the expression value as the current block result.
- STMT-assert: GAP — `parseAssertStmt` parses `assert`, but `evalAssertStatement` only logs a warning/error and always returns `nil`, so failed assertions do not stop evaluation or raise an exception.
- STMT-ifelse: SUPPORTED — `parseIfStmt` parses `if`/`else if`/`else`, and `evalStatement` executes `evalIfStmt` using Groovy-style truthiness via `isTruthy`.
- STMT-return: SUPPORTED — `parseReturnStmt` parses `return`, and `evalStatement` marks `evalReturnStmt` as returned so `evalStatementBlock` exits early with the return value.
- STMT-throw: GAP — `parseThrowStmt` parses `throw`, but uncaught throws are swallowed by `evalStatementBody`, which converts `evalThrownError` into a `nil` result after warning instead of propagating failure.
- STMT-trycatch: GAP — `parseTryStmt` and `evalStatement` implement `try`/`catch`/`finally`, but exception matching is only approximate in `matchesCatchClause` and uncaught `evalThrownError` is later swallowed by `evalStatementBody`, so behavior does not match real Groovy/Nextflow exception handling.
- STMT-multi-assignment: PARSE_ERROR — the statement parser does not accept `def (a, b) = ...`; `parseAssignmentStmt` requires a single identifier after `def`, so this form fails before `evalMultiAssignExpr` can be used.
