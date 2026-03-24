# Test: Deprecations

**Spec files:** nf-0350-deprecations.md
**Impl files:** impl-01-parse.md

## Task

For each feature ID in nf-0350-deprecations.md, determine its classification.

### Checklist

1. Read nf-0350-deprecations.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- SYN-deprecations, SYN-deprecated-addParams, SYN-deprecated-when-section, SYN-deprecated-shell-section, SYN-deprecated-for-loop
  SYN-deprecated-while-loop

### Output format

```
SYN-deprecations: SUPPORTED | reason
...
```

## Results

- SYN-deprecations: GAP — support is partial. `parseSection` in `nextflowdsl/parse.go` accepts deprecated `when:` and `shell:` sections, and translation applies them via `filterWhenBindingSets`/`EvalWhenGuard` and `renderShellSection` in `nextflowdsl/translate.go`, but deprecated include clauses and `for`/`while` loop syntax are not parsed.
- SYN-deprecated-addParams: PARSE_ERROR — `parseImport` in `nextflowdsl/parse.go` only parses `include { ... } from 'module'` and then returns; deprecated trailing `addParams`/`params` include clauses are not handled, so leftover tokens fail top-level parsing.
- SYN-deprecated-when-section: SUPPORTED — `parseSection` stores `when:` into `proc.When` in `nextflowdsl/parse.go`, and `filterWhenBindingSets` with `EvalWhenGuard` in `nextflowdsl/translate.go` evaluates the guard before creating jobs.
- SYN-deprecated-shell-section: SUPPORTED — `parseSection` stores `shell:` into `proc.Shell` in `nextflowdsl/parse.go`, and `renderScript` dispatches to `renderShellSection` in `nextflowdsl/translate.go`, which interpolates `!{...}` while preserving shell `${...}` syntax.
- SYN-deprecated-for-loop: PARSE_ERROR — there is no loop parser. Workflow `main` handling in `parseWorkflowBlock` only special-cases `if`, assignments, and call/pipeline statements, and `parseWorkflowStatement` has no `for` support, so `for` loop syntax is rejected.
- SYN-deprecated-while-loop: PARSE_ERROR — there is no `while` statement parser. Workflow `main` handling in `parseWorkflowBlock` and `parseWorkflowStatement` does not admit loop constructs, so `while` loop syntax is rejected.
