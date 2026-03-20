# Phase 2: Config, params, Groovy

Ref: [spec.md](spec.md) sections B1, B2, B3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: B1 - Parse Nextflow config files

spec.md section: B1

Implement `ParseConfig(r io.Reader) (*Config, error)` in
`nextflowdsl/config.go`. Define `Config`, `Profile`, and
`ProcessDefaults` types. Handle `params {}`, `process {}`, and
`profiles {}` blocks. Reuse lexer patterns from Phase 1.
Covering all 6 acceptance tests from B1.

Depends on Phase 1 for Expr types and lexer patterns.

- [ ] implemented
- [x] implemented
- [x] reviewed

### Batch 2 (parallel, after item 2.1 is reviewed)

#### Item 2.2: B2 - Parameter substitution [parallel with 2.3]

spec.md section: B2

Implement `LoadParams`, `MergeParams`, and `SubstituteParams` in
`nextflowdsl/params.go`. LoadParams reads JSON or YAML files
(auto-detected by extension, content-sniffed for ambiguous
extensions). MergeParams merges config, file, and CLI params
(later sources override earlier). SubstituteParams replaces
both `${params.KEY}` and bare `params.KEY` references, with
nested dot-separated key lookups. Covering all 8 acceptance
tests from B2.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 2.3: B3 - Minimal Groovy expression evaluator [parallel with 2.2]

spec.md section: B3

Implement `EvalExpr(expr Expr, vars map[string]any) (any, error)`
in `nextflowdsl/groovy.go`. Handle integer literals, string
interpolation, `params.*` and `task.*` references, and basic
arithmetic. Complex closures return an error containing the
original expression text. Covering all 7 acceptance tests from
B3.

Depends on Phase 1 for Expr AST types.

- [ ] implemented
- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
