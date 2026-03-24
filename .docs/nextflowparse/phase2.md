# Phase 2: Config parser fixes (Groups D, E)

Ref: [spec.md](spec.md) sections D1, E1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 2.1: D1 - Skip nested blocks in parseExecutorBlock [parallel with 2.2]

spec.md section: D1

In `parseExecutorBlock` in `config.go`, after consuming the
identifier token, peek at the next token before calling
`parseAssignmentValue`. If it is `{`, skip the nested block
using brace-counting. Also handle `tokenString`-keyed blocks
(e.g. '$local' { ... }) by adding a tokenString case that peeks
and skips if `{` follows. Covering all 4 acceptance tests from
D1. Test file: `config_test.go`.

- [ ] implemented
- [ ] reviewed

#### Item 2.2: E1 - Handle bare assignments in config parser [parallel with 2.1]

spec.md section: E1

In `configParser.parse` in `config.go`, in the default branch,
after `skipUnknownTopLevelConfigScope` returns false, check if
the next token after the identifier is `=`. If so, skip the
identifier and the assignment value (consume tokens until
newline/semicolon/EOF) and emit a warning. If neither a known
scope, skippable scope, nor bare assignment, return the existing
"unsupported config section" error. Covering all 4 acceptance
tests from E1. Test file: `config_test.go`.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
