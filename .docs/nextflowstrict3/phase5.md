# Phase 5: Workflow Block Sections

Ref: [spec.md](spec.md) sections H1, I1, I2 parse, J1, K1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 5.1: H1 - Parse top-level output block properly [parallel with 5.2, 5.3, 5.4, 5.5]

spec.md section: H1

Add `OutputBlock string` field to `Workflow` in ast.go. Extend
the existing `output {}` block skip logic in `parseTopLevel` in
parse.go to store the raw body text in `Workflow.OutputBlock`
instead of discarding it. Covering all 3 acceptance tests
from H1.

- [x] implemented
- [x] reviewed

#### Item 5.2: I1 - Parse `publish:` statement content [parallel with 5.1, 5.3, 5.4, 5.5]

spec.md section: I1

Add `WFPublish` struct and `Publish []*WFPublish` field to
`WorkflowBlock` in ast.go. Modify `parseWorkflowBlock` in
parse.go to store `publish:` lines as `WFPublish` entries
(Target/Source from `lhs = rhs` assignments) instead of
discarding them. Covering all 3 acceptance tests from I1.

- [x] implemented
- [x] reviewed

#### Item 5.3: I2 parse - Parse `onComplete:` and `onError:` in workflow blocks [parallel with 5.1, 5.2, 5.4, 5.5]

spec.md section: I2

Add `OnComplete string` and `OnError string` fields to
`WorkflowBlock` in ast.go. Modify `parseWorkflowBlock` in
parse.go to accept `onComplete:` and `onError:` sections,
storing raw body text. Translation of these sections into wr
jobs is handled in Phase 8. Covering acceptance tests 1-4
from I2 (parse-level tests only).

- [x] implemented
- [x] reviewed

#### Item 5.4: J1 - Verify pipe operator support [parallel with 5.1, 5.2, 5.3, 5.5]

spec.md section: J1

The pipe operator is already implemented via `PipeExpr` and
`desugarWorkflowPipe`. Verify it handles the full pattern
`channel.of(1,2,3) | foo | bar | view` and multi-step pipes.
Add tests if missing. Covering all 3 acceptance tests from J1.

- [x] implemented
- [x] reviewed

#### Item 5.5: K1 - Track channel-valued assignments [parallel with 5.1, 5.2, 5.3, 5.4]

spec.md section: K1

Extend `parseChannelAssignment` in parse.go to track plain
variable assignments whose RHS is a channel expression
(`Channel.*`), process output reference (`*.out`, `*.out.*`),
or a known channel variable. Plain assignments (`x = 42`)
silently ignored. Covering all 4 acceptance tests from K1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
