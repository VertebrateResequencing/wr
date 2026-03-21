# Phase 3: Workflow block parsing

Ref: [spec.md](spec.md) sections A2, A3, A7

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 3.1: A2 - Workflow take/emit/publish sections [parallel with 3.2, 3.3]

spec.md section: A2

Extend `parseWorkflowBlockDecl` in `nextflowdsl/parse.go` to
detect and parse `take:`, `main:`, `emit:`, and `publish:`
section labels inside workflow blocks. Add `Take []string`,
`Emit []*WFEmit` fields to `WorkflowBlock` and the `WFEmit`
struct to `nextflowdsl/ast.go`. Covering all 5 acceptance tests
from A2.

- [ ] implemented
- [ ] reviewed

#### Item 3.2: A3 - Function definitions [parallel with 3.1, 3.3]

spec.md section: A3

Extend `parseWorkflow` in `nextflowdsl/parse.go` to recognise
top-level `def` function definitions. Add `Functions []*FuncDef`
field to `Workflow` and the `FuncDef` struct to
`nextflowdsl/ast.go`. Functions are stored in the AST but not
evaluated at translate time. Covering all 3 acceptance tests
from A3.

- [ ] implemented
- [ ] reviewed

#### Item 3.3: A7 - Top-level output block [parallel with 3.1, 3.2]

spec.md section: A7

Extend `parseWorkflow` in `nextflowdsl/parse.go` to skip
top-level `output { ... }` blocks (including nested braces).
The block is parsed and discarded since wr handles output via
publishDir. Covering all 3 acceptance tests from A7.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
