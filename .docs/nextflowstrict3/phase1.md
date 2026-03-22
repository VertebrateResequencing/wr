# Phase 1: Groovy Expression Parsing

Ref: [spec.md](spec.md) sections D1, D2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 1.1: D1 - Parse all missing operators [parallel with 1.2]

spec.md section: D1

Add lexer tokens and parser handling for all missing Groovy/
Nextflow operators: `%`, `**`, `in`/`!in`, `instanceof`/
`!instanceof`, `=~`/`==~`, `<=>`, `..`/`..<`, `<<`/`>>`/`>>>`,
`&`/`^`/`|` (bitwise), `~` (unary prefix), and `*.`
(spread-dot). Produce `BinaryExpr`, `InExpr`, `RegexExpr`,
`RangeExpr`, `SpreadExpr`, `UnaryExpr`, or `UnsupportedExpr`
AST nodes as specified. Add AST node types to ast.go. Implement
in parse.go, covering all 18 acceptance tests from D1.

- [ ] implemented
- [ ] reviewed

#### Item 1.2: D2 - Parse missing expression features [parallel with 1.1]

spec.md section: D2

Parse slashy strings (`/pattern/`) as `SlashyStringExpr`, `new`
constructors as `NewExpr`, and multi-variable assignments
(`def (x, y) = expr` / `(a, b) = expr`) as `MultiAssignExpr`.
Add AST node types to ast.go. Implement in parse.go, covering
all 5 acceptance tests from D2.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
