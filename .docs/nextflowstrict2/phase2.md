# Phase 2: Process Directive Parsing (F1, A1)

Ref: [spec.md](spec.md) sections F1, A1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 2.1: F1 - Parse and store new directives [parallel with 2.2]

spec.md section: F1

Add `Tag`, `BeforeScript`, `AfterScript`, `Module`, `Cache`, and
`Directives map[string]any` fields to `Process` in ast.go. Extend
directive parsing in parse.go to handle `tag`, `beforeScript`,
`afterScript`, `module`, `cache`, `scratch`, `storeDir`, `queue`,
`clusterOptions`, `executor`, `debug`, and `secret` directives.
Label parsing is covered by A1 separately. Depends on Phase 1 for
expression evaluation of directive values. Covering all 12
acceptance tests from F1.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 2.2: A1 - Parse label directive on processes [parallel with 2.1]

spec.md section: A1

Add `Labels []string` field to `Process` in ast.go. Extend
directive parsing in parse.go to handle `label` directives,
appending each value to the `Labels` slice. Multiple `label`
directives are allowed per process. Covering all 3 acceptance
tests from A1.

- [ ] implemented
- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
