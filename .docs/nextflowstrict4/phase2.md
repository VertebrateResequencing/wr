# Phase 2: Simple Script Translations

Ref: [spec.md](spec.md) sections A1, B1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel, after phase 1 is reviewed)

#### Item 2.1: A1 - `--stub-run` flag and stub body substitution [parallel with 2.2]

spec.md section: A1

Add `--stub-run` flag to `cmd/nextflow.go` and add `StubRun`
field to `TranslateConfig` in `nextflowdsl/translate.go`. When
`StubRun` is true, substitute `Process.Stub` for
`Process.Script` for processes with non-empty stubs; processes
without stubs fall back to their script. Covering all 4
acceptance tests from A1.

- [x] implemented
- [x] reviewed

#### Item 2.2: B1 - `!{expr}` interpolation for shell sections [parallel with 2.1]

spec.md section: B1

Implement `interpolateShellSection` in
`nextflowdsl/translate.go`. Scan for `!{expr}` patterns using
regex, evaluate each via the Groovy evaluator with input
bindings and params, substitute results, and leave all
`${...}` untouched. Handle string form and list form (where
first element is interpreter, middle are flags, last is script
body). List form replaces the default `set -euo pipefail`
header. Covering all 5 acceptance tests from B1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
