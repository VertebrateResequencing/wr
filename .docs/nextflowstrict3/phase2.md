# Phase 2: Groovy Evaluation

Ref: [spec.md](spec.md) sections D3, D4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 2.1: D3 - Evaluate common operators [parallel with 2.2]

spec.md section: D3

Implement evaluation of `%`, `**`, `in`/`!in`, `=~`/`==~`,
`..`/`..<`, spread-dot (`*.`), bitwise (`&`, `^`, `|`, `~`),
and shift (`<<`, `>>`, `>>>`) in groovy.go. Add
`evalInExpr`, `evalRegexExpr`, `evalRangeExpr`,
`evalSpreadExpr` functions. Bitwise and shift operators are
fully evaluated as trivial one-line Go int operations. Only
`<=>` (spaceship) and `instanceof` return `UnsupportedExpr`
with a warning. Covering all 21 acceptance tests from D3.

- [ ] implemented
- [ ] reviewed

#### Item 2.2: D4 - Evaluate additional string/list methods [parallel with 2.1]

spec.md section: D4

Add method evaluation in `evalMethodCallExpr` for `matches`,
`replaceAll`, `tokenize`, `findAll`, `find`, `any`, `every`,
`join`, `unique`, `sort`, `plus`, `minus`, `multiply`, `take`,
and `drop`. Implement in groovy.go, covering all 15 acceptance
tests from D4.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
