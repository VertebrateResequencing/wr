# Phase 6: Publish + Output Block

Ref: [spec.md](spec.md) sections N1, N2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 6.1: N1 - Parse `output {}` block targets (after phase 5 is reviewed)

spec.md section: N1

Implement `ParseOutputBlock` in `nextflowdsl/translate.go` (or
`nextflowdsl/parse.go`) along with the `OutputTarget` type.
Parse `Workflow.OutputBlock` raw body into `[]*OutputTarget`,
extracting `Name`, `Path`, and `IndexPath` from each named
target. Only static string paths supported; closure-valued
paths produce a warning and the target is skipped. Covering
all 5 acceptance tests from N1.

- [ ] implemented
- [ ] reviewed

### Item 6.2: N2 - Translate `publish:` wiring with output targets (after 6.1)

spec.md section: N2

Depends on item 6.1. Wire `WFPublish` assignments to parsed
output block targets in `nextflowdsl/translate.go`. For each
publish assignment, look up the target, get the publish path,
and add output-copying commands to final jobs (same mechanism
as `publishDir`). If `IndexPath` is set, create an aggregator
job that writes a TSV index file. Warn if publish references a
target not in the output block. Covering all 4 acceptance tests
from N2.

- [ ] implemented
- [ ] reviewed
