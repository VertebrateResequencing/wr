# Phase 4: Input/Output Parsing

Ref: [spec.md](spec.md) sections B1, C1 parse, E1, F1, G1, G2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 4.1: B1 - Parse `each` input declarations [parallel with 4.2, 4.3, 4.4, 4.5, 4.6]

spec.md section: B1

Add `Each bool` field to `Declaration` in ast.go. Modify
`parseDeclarationLine` in parse.go to recognise the `each`
prefix before a qualifier (e.g. `each val(x)`, `each path(p)`)
and bare `each x` (equivalent to `each val(x)`). Set
`Declaration.Each = true`. Covering all 4 acceptance tests
from B1.

- [ ] implemented
- [ ] reviewed

#### Item 4.2: C1 - Verify `eval` output parsing [parallel with 4.1, 4.3, 4.4, 4.5, 4.6]

spec.md section: C1

Parsing of `eval(command)` output declarations already exists
from prior spec (A5). Verify existing parse behaviour handles
`eval('hostname')` and similar patterns. Add parse-level tests
if missing. The translation work for C1 is in Phase 8.

- [ ] implemented
- [ ] reviewed

#### Item 4.3: E1 - Parse skippable statement types [parallel with 4.1, 4.2, 4.4, 4.5, 4.6]

spec.md section: E1

Ensure the parser handles `assert`, `throw`, `try/catch/finally`,
`return`, `for (x in coll)`, `while`, and `switch/case` in
process scripts, function bodies, and closures without error.
Add statement AST node types (`AssertStmt`, `ThrowStmt`,
`TryCatchStmt`, `ReturnStmt`, `ForStmt`, `WhileStmt`,
`SwitchStmt`) to ast.go. The brace-matching logic in parse.go
must handle these keywords gracefully. Covering all 9
acceptance tests from E1.

- [ ] implemented
- [ ] reviewed

#### Item 4.4: F1 - Parse `params {}` block [parallel with 4.1, 4.2, 4.3, 4.5, 4.6]

spec.md section: F1

Add `ParamDecl` struct and `ParamBlock []*ParamDecl` to
`Workflow` in ast.go. Implement `params {}` block parsing in
`parseTopLevel` in parse.go. Type annotations stored as strings
in `ParamDecl.Type`, defaults parsed as `Expr`. Support nested
blocks (`params { nested { x = 1 } }` as `"nested.x"`).
Last-seen-wins for `params {}` vs `params.x` overrides.
Covering all 6 acceptance tests from F1.

- [ ] implemented
- [ ] reviewed

#### Item 4.5: G1 - Parse enum definitions [parallel with 4.1, 4.2, 4.3, 4.4, 4.6]

spec.md section: G1

Add `EnumDef` struct and `Enums []*EnumDef` to `Workflow` in
ast.go. Parse `enum Name { VALUE1, VALUE2, ... }` in
`parseTopLevel` in parse.go. Enum value references
(`Day.MONDAY`) resolve to the string `"MONDAY"` in expression
evaluation (groovy.go). Covering all 4 acceptance tests
from G1.

- [ ] implemented
- [ ] reviewed

#### Item 4.6: G2 - Parse record definitions [parallel with 4.1, 4.2, 4.3, 4.4, 4.5]

spec.md section: G2

Add `RecordDef`, `RecordField` structs and
`Records []*RecordDef` to `Workflow` in ast.go. Parse
`record Name { field: Type; field: Type = default }` in
`parseTopLevel` in parse.go. Covering all 3 acceptance tests
from G2.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
