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
shift (`<<`, `>>`, `>>>`), `<=>` (spaceship), and
`instanceof` in groovy.go. Add `evalInExpr`, `evalRegexExpr`,
`evalRangeExpr`, `evalSpreadExpr` functions. All operators
are fully evaluated: bitwise and shift as trivial one-line Go
int operations; `<=>` compares operands and returns -1/0/1;
`instanceof` checks Go runtime type against a Groovy type
name map (String→string, Integer→int/int64, List→[]any,
Map→map[string]any, Boolean→bool) and returns bool.
Covering all 30 acceptance tests from D3.

- [x] implemented
- [x] reviewed

#### Item 2.2: D4 - Evaluate additional string/list methods [parallel with 2.1]

spec.md section: D4

Add method evaluation in `evalMethodCallExpr` for `matches`,
`replaceAll`, `tokenize`, `findAll`, `find`, `any`, `every`,
`join`, `unique`, `sort`, `plus`, `minus`, `multiply`, `take`,
and `drop`. Implement in groovy.go, covering all 15 acceptance
tests from D4.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
