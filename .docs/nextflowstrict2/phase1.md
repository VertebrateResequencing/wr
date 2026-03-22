# Phase 1: Groovy Expression Extensions (D1-D6)

Ref: [spec.md](spec.md) sections D1, D2, D3, D4, D5, D6

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 1.1: D4 - List and map literals [parallel with 1.2, 1.3]

spec.md section: D4

Add `ListExpr`, `MapExpr`, and `IndexExpr` AST nodes to ast.go.
Extend `parseExprTokens` in parse.go to recognise `[...]` list
and map literal syntax. Extend `EvalExpr` in groovy.go to
evaluate list literals, map literals (with `key:` syntax), empty
list `[]`, empty map `[:]`, and subscript access `list[0]` /
`map['key']`. Covering all 8 acceptance tests from D4.

- [ ] implemented
- [ ] reviewed

#### Item 1.2: D2 - Comparison and logical operators [parallel with 1.1, 1.3]

spec.md section: D2

Add `UnaryExpr` AST node to ast.go. Extend `parseExprTokens` in
parse.go to recognise `==`, `!=`, `>=`, `<=`, `&&`, `||`, and `!`
operators. Extend `EvalExpr` in groovy.go to evaluate `BinaryExpr`
for comparison and logical operators, and `UnaryExpr` for `!`.
Covering all 11 acceptance tests from D2.

- [ ] implemented
- [ ] reviewed

#### Item 1.3: D6 - Cast expressions [parallel with 1.1, 1.2]

spec.md section: D6

Add `CastExpr` AST node to ast.go. Extend `parseExprTokens` in
parse.go to recognise `as Integer`, `as String` postfix syntax.
Extend `EvalExpr` in groovy.go to evaluate casts: `as Integer`
converts via `strconv.Atoi`, `as String` via `fmt.Sprintf`.
Unknown casts produce `UnsupportedExpr`. Covering all 3
acceptance tests from D6.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Batch 2 (parallel, after batch 1 is reviewed)

#### Item 1.4: D5 - Null literal and task.* references [parallel with 1.5]

spec.md section: D5

Add `NullExpr` and `NullSafeExpr` AST nodes to ast.go. Extend
`parseExprTokens` in parse.go to recognise `null` literal and
`?.` null-safe navigation. Extend `EvalExpr` in groovy.go to
evaluate null, null equality (uses D2's `==`), `task.attempt` and
other `task.*` property resolution, and null-safe property access
`x?.property`. Covering all 6 acceptance tests from D5.

- [ ] implemented
- [ ] reviewed

#### Item 1.5: D3 - Method calls on strings and lists [parallel with 1.4]

spec.md section: D3

Add `MethodCallExpr` AST node to ast.go. Extend `parseExprTokens`
in parse.go to recognise `.method(args)` call syntax and method
chaining. Extend `EvalExpr` in groovy.go with string method
dispatch (`trim`, `size`, `toInteger`, `toLowerCase`, `toUpperCase`,
`contains`, `startsWith`, `endsWith`, `replace`, `split`,
`substring`) and list method dispatch (`size`, `isEmpty`, `first`,
`last`, `flatten`). Uses D4's list types. The `collect` method
(test 19) depends on H2 closure evaluation from Phase 6; implement
stub that returns `UnsupportedExpr` for now. Covering all 19
acceptance tests from D3 (test 19 deferred to Phase 6).

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Batch 3 (parallel, after batch 2 is reviewed)

#### Item 1.6: D1 - Ternary and elvis operators

spec.md section: D1

Add `TernaryExpr` AST node to ast.go. Extend `parseExprTokens`
in parse.go to recognise `?` (ternary) and `?:` (elvis) operators.
Extend `EvalExpr` in groovy.go to evaluate ternary and elvis
expressions using truthiness rules (null, 0, "", false, empty
list/map are falsy). Depends on D2 for condition evaluation and
D5 for null truthiness. Covering all 6 acceptance tests from D1.

- [ ] implemented
- [ ] reviewed
