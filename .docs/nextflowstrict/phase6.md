# Phase 6: Translation

Ref: [spec.md](spec.md) sections B1, B2, B3, C1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 6.1: B1 - Tuple input binding

spec.md section: B1

Depends on Phase 2 (tuple AST types from A1). Update the
translator in `nextflowdsl/translate.go` so that when a process
has `tuple` input declarations, each element is bound individually
from upstream channel items. For `tuple val(id), path(reads)`,
the job's `Cmd` exports `id='sampleA'` and `reads='/data/a.fq'`
from a 2-element upstream list. Existing non-tuple input behaviour
must be preserved. Covering all 3 acceptance tests from B1.

- [ ] implemented
- [ ] reviewed

### Item 6.2: B2 - Tuple output path wiring

spec.md section: B2

Depends on Item 6.1. Wire each `path`/`file` element's pattern
from tuple output declarations into the output path list in
`nextflowdsl/translate.go`. Stages with dynamic `path` elements
are marked as pending; val-only tuple outputs are static.
Covering all 3 acceptance tests from B2.

- [ ] implemented
- [ ] reviewed

### Item 6.3: B3 - Tuple output in TranslatePending

spec.md section: B3

Depends on Item 6.2. Update `TranslatePending` in
`nextflowdsl/translate.go` to handle tuple outputs by matching
each element's pattern against completed output paths. Downstream
jobs receive resolved file references from tuple outputs.
Covering all 2 acceptance tests from B3.

- [ ] implemented
- [ ] reviewed

### Item 6.4: C1 - Parse and resolve emit labels

spec.md section: C1

Depends on Phase 2 (emit labels on declarations) and Phase 3
(workflow emit wiring). Update the translator in
`nextflowdsl/translate.go` to index process outputs by their
`emit:` labels and resolve `process.out.labelName` references
against that index. When no emit label matches, fall back to
existing full-output behaviour with a warning. Handle emit label
resolution in `TranslatePending` as well, filtering completed
paths by the emit label's pattern. Covering all 3 acceptance
tests from C1.

- [ ] implemented
- [ ] reviewed
