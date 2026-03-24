# Phase 4: Statement and workflow fixes (Groups H, J, K)

Ref: [spec.md](spec.md) sections H1, J1, K1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 4.1: H1 - Multi-assignment parsing [parallel with 4.2, 4.3]

spec.md section: H1

In `parseAssignmentStmt` in `groovy.go`, when `declare` is true
and the current token after consuming `def` is `(`, read tokens
through matching `)`, expect `=`, read the value expression,
parse via `parseMultiAssignExprTokens` (prepending a synthetic
`def` token), and return an `evalMultiAssignStmt` (a new type
wrapping `MultiAssignExpr`). Add a case in `evalStatement` for
`evalMultiAssignStmt` that calls `evalMultiAssignExpr`. Covering
all 3 acceptance tests from H1. Test file:
`parse_e1_test.go`.

- [ ] implemented
- [ ] reviewed

#### Item 4.2: J1 - Deprecated loop skipping [parallel with 4.1, 4.3]

spec.md section: J1

In `parseWorkflowBlock` in `parse.go`, in the "main" case,
before calling `parseWorkflowStatement`, check for
`current.lit == "for"` or `current.lit == "while"`. If matched,
emit a deprecation warning, advance past the keyword, skip the
parenthesised condition using paren-counting, and skip the body
using brace-counting. No AST nodes needed. Covering all 4
acceptance tests from J1. Test file: `parse_e1_test.go`.

- [ ] implemented
- [ ] reviewed

#### Item 4.3: K1 - & parallel operator desugar [parallel with 4.1, 4.2]

spec.md section: K1

In `desugarWorkflowPipe` in `parse.go`, before the existing
`PipeExpr` check, check if the expression (or any pipe stage)
is a `BinaryExpr` with `Op == "&"`. If so, flatten the `&` tree
into multiple `Call` entries sharing the same input channel.
Each leaf of the `&` tree must be a `ChanRef` and produces one
`Call` per leaf. Also handle `&` within pipe stages. Covering
all 4 acceptance tests from K1. Test file:
`parse_e1_test.go`.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
