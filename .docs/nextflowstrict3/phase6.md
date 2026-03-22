# Phase 6: Channel Operator Implementation

Ref: [spec.md](spec.md) sections L1, L2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 6.1: L1 - Implement cardinality-changing operators [parallel with 6.2]

spec.md section: L1

Move `combine`, `concat`, `flatten`, `transpose`, `unique`,
`distinct`, `ifEmpty`, `toList`, `toSortedList`, `count`,
`reduce`, `toLong`, `toFloat`, and `toDouble` from
`unsupportedCardinalityOperators` to real implementations in
`applyChannelOperator` in channel.go. `combine` supports keyed
cross-product via `by:` parameter. `reduce` evaluates a closure
to fold items. Type-conversion operators (`toLong`, `toFloat`,
`toDouble`) pass through like `toInteger`. Covering all 17
acceptance tests from L1.

- [ ] implemented
- [ ] reviewed

#### Item 6.2: L2 - Data-dependent ops as TranslatePending [parallel with 6.1]

spec.md section: L2

Implement `splitCsv`, `splitJson`, `splitText`, `splitFasta`,
`splitFastq`, `collectFile`, `branch`, and `multiMap` as
pass-through operators that emit a warning and produce
TranslatePending when cardinality is unknown. Items pass through
unchanged at compile time. Covering all 8 acceptance tests
from L2.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
