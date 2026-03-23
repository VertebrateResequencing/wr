# Test: Statements

**Spec files:** nf-0300-statements.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-0300-statements.md, determine its classification.

### Checklist

1. Check evalStatement (groovy.go line 255) for each statement type
2. Verify the statement type is parsed and evaluated
3. Note: STMT-for, STMT-while, STMT-switch, STMT-break, STMT-continue,
   and `finally` clause are ⚠️NON-STRICT (not in syntax.html strict mode).
   Classify them as normal but note the non-strict status.
4. Classify per 00-instructions.md criteria

### Features to classify

- STMT-assign, STMT-expr-statement
- STMT-if, STMT-if-else, STMT-if-elseif
- STMT-for-in, STMT-for-range
- STMT-while
- STMT-switch, STMT-switch-default
- STMT-try, STMT-try-catch, STMT-try-finally
- STMT-return
- STMT-throw
- STMT-assert
- STMT-break, STMT-continue
- STMT-var-def, STMT-multi-assign

### Output format

```
STMT-if: SUPPORTED | reason
...
```
