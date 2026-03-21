# Phase 2: Process section parsing

Ref: [spec.md](spec.md) sections A4, A5, A1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 2.1: A4 - Additional process sections

spec.md section: A4

Extend the process parser in `nextflowdsl/parse.go` to accept
`stub:`, `exec:`, `shell:`, and `when:` sections. Add `Stub`,
`Exec`, `Shell`, and `When` string fields to the `Process` struct
in `nextflowdsl/ast.go`. Parse and store each section's body as
raw text; emit a stderr warning for sections not yet translatable.
Covering all 6 acceptance tests from A4.

- [x] implemented
- [x] reviewed

### Item 2.2: A5 - Additional I/O types and qualifiers

spec.md section: A5

Depends on Item 2.1. Extend `parseDeclarationLine` in
`nextflowdsl/parse.go` to recognise `stdout`, `env(NAME)`,
`stdin`, and `eval(command)` I/O types, plus `emit:`, `optional:`,
and `topic:` output qualifiers. Add `Emit` (string), `Optional`
(bool) fields to `Declaration` in `nextflowdsl/ast.go`. Covering
all 5 acceptance tests from A5.

- [x] implemented
- [x] reviewed

### Item 2.3: A1 - Tuple input/output declarations

spec.md section: A1

Depends on Item 2.2. Implement `tuple` compound declarations in
`parseDeclarationLine`. Add the `TupleElement` struct and
`Elements []*TupleElement` field to `Declaration` in
`nextflowdsl/ast.go`. Handle `tuple val(x), path(y)` syntax
with arbitrary element counts, `emit:` and `optional:` qualifiers
on tuple lines, and `arity:` qualifier acceptance. Covering all 9
acceptance tests from A1.

- [x] implemented
- [x] reviewed
