# Phase 6: Closure Parsing and Workflow Conditions (H1, H2, E1)

Ref: [spec.md](spec.md) sections H1, H2, E1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 6.1: H1 - Parse closure parameters [parallel with 6.2]

spec.md section: H1

Add `ClosureExpr` AST node to ast.go with `Params []string` and
`Body string` fields. Extend closure capture in parse.go to
recognise `{ params -> body }` syntax, multi-parameter
`{ a, b -> body }`, explicit empty `{ -> body }`, and implicit
`it` parameter (no `->` present). Depends on Phase 1 for
expression parsing infrastructure. Covering all 4 acceptance
tests from H1.

- [ ] implemented
- [ ] reviewed

#### Item 6.2: E1 - Parse if/else in workflow blocks [parallel with 6.1]

spec.md section: E1

Add `IfBlock` struct (Condition, Body, ElseIf, ElseBody) and
`Conditions []*IfBlock` field on `WorkflowBlock` in ast.go.
Extend workflow block parsing in parse.go to handle `if`,
`else if`, and `else` blocks, capturing the condition string
and calls within each branch. Depends on Phase 1 for expression
parsing. Covering all 4 acceptance tests from E1.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Batch 2 (parallel, after batch 1 is reviewed)

#### Item 6.3: H2 - Evaluate simple closures during channel resolution

spec.md section: H2

Extend channel resolution in channel.go and `EvalExpr` in
groovy.go to evaluate simple closures. Bind named parameters
(or implicit `it`) to current channel items before evaluating
the body via `EvalExpr`. For `map`, the result replaces the
item. For `filter`, falsy results exclude items. Complex or
unsupported closure bodies fall back to passthrough with warning.
Also wire up D3's `collect` list method (test D3.19) now that
closure evaluation is available. Depends on H1 (item 6.1) for
closure parsing. Covering all 4 acceptance tests from H2.

- [ ] implemented
- [ ] reviewed
