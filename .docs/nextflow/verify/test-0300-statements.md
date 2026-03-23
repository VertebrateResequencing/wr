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
  STMT-throw, STMT-trycatch

### Output format

```
STMT-assignment: SUPPORTED | reason
...
```
