# Test: Syntax Basics

**Spec files:** nf-0100-syntax-basics.md
**Impl files:** impl-01-parse.md

## Task

For each feature ID in the spec file, determine its classification by
checking the implementation descriptions.

### Checklist

For each SYN-* feature in nf-0100-syntax-basics.md:

1. Find the corresponding implementation entry in impl-01-parse.md
2. If the feature is mentioned as handled by a specific function → **check
   that function exists** at the stated line in `nextflowdsl/parse.go`
3. Classify:
   - **SUPPORTED** — parser handles it AND downstream code evaluates it
   - **PARSE_ERROR** — parser rejects it with a clear error
   - **GAP** — parser accepts it but evaluation is incomplete/wrong
   - **UNSUPPORTED** — parser silently ignores or produces wrong AST
   - **FUTURE** — parser and evaluator don't address it at all

### Features to classify

- SYN-comments (line, block, doc)
- SYN-string-single, SYN-string-double, SYN-string-triple-single,
  SYN-string-triple-double, SYN-string-slashy, SYN-string-dollar-slashy
- SYN-string-interp, SYN-string-interp-closure
- SYN-string-concat
- SYN-boolean
- SYN-null
- SYN-list-literal
- SYN-map-literal
- SYN-closure
- SYN-closure-params
- SYN-gstring-lazy
- SYN-feature-flag
- SYN-constructor
- SYN-named-args
- SYN-index-expr
- SYN-property-expr
- SYN-scoping

### Output format

```
SYN-comments: SUPPORTED | reason
SYN-string-single: SUPPORTED | reason
...
```
