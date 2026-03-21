# Phase 1: Command Wrapping and Cleanup Behaviour

Ref: [spec.md](spec.md) sections A1, A2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A1 - Wrap process scripts to capture stdout/stderr

spec.md section: A1

Modify `buildCommand()` in `nextflowdsl/translate.go` to wrap every
process command with `{ ...; } > .nf-stdout 2> .nf-stderr`. The
group construct preserves exit code. Handle both the no-bindings and
with-bindings cases. Covering all 4 acceptance tests from A1.

- [ ] implemented
- [ ] reviewed

### Item 1.2: A2 - Cleanup behaviour for empty capture files

spec.md section: A2

Add an OnExit Run behaviour to every Nextflow process job in
`nextflowdsl/translate.go` that removes `.nf-stdout` and
`.nf-stderr` if they contain only whitespace. Depends on item 1.1
being complete (wrapping must exist for cleanup to be meaningful).
Covering all 1 acceptance test from A2.

- [ ] implemented
- [ ] reviewed
