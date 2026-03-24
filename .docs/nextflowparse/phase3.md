# Phase 3: Declaration and import fixes (Groups F, G, I)

Ref: [spec.md](spec.md) sections F1, G1, I1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 3.1: F1 - Add name: and stageAs: qualifiers [parallel with 3.2, 3.3]

spec.md section: F1

Add fields `StageName string` and `StageAs string` to both
`Declaration` and `TupleElement` structs in `ast.go`. In
`parse.go`, add "name" and "stageAs" cases to
`applyDeclarationQualifier` (storing in `decl.StageName` /
`decl.StageAs`) and matching cases in
`applyTupleElementQualifier` (storing in `element.StageName` /
`element.StageAs`). Covering all 6 acceptance tests from F1.
Test file: `parse_e1_test.go`.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 3.2: G1 - Skip DSL1 combined input/output [parallel with 3.1, 3.3]

spec.md section: G1

In `parseDeclarationLine` in `parse.go`, change the existing
`into` hard error to a stderr warning ("nextflowdsl: DSL 1
'into' syntax at line %d is not supported, skipping
declaration\n") and return nil, nil. In `parseDeclarations`,
add `if decl == nil { continue }` after the error check to
skip nil declarations. Covering all 3 acceptance tests from
G1. Test file: `parse_e1_test.go`.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 3.3: I1 - Skip deprecated addParams/params [parallel with 3.1, 3.2]

spec.md section: I1

In `parseImport` in `parse.go`, after setting
`importNode.Source` and advancing `p.pos`, check if
`p.current()` is an identifier with lit "addParams" or
"params". If so, emit a deprecation warning, advance past the
identifier, and skip the argument (parenthesised or
brace-delimited) using paren/brace-counting. Covering all 4
acceptance tests from I1. Test file: `parse_e1_test.go`.

- [ ] implemented
- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
