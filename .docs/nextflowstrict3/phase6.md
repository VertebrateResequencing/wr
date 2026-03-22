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
`toDouble`) pass through like `toInteger`. Covering all 18
acceptance tests from L1.

- [ ] implemented
- [ ] reviewed

#### Item 6.2: L2 - Implement data-dependent operators via PendingStage [parallel with 6.1]

spec.md section: L2

Implement `branch`, `multiMap`, `splitCsv`, `splitJson`,
`splitText`, `splitFasta`, `splitFastq`, and `collectFile` as
real PendingStage implementations in channel.go. `branch`
evaluates the branch closure against each completed item at
runtime, routing items to named output channels with distinct
dep_grps. `multiMap` evaluates the multiMap closure against
each completed item, producing one item per named output
channel. `split*` operators read actual file data at runtime
and split into N downstream items per chunk. `collectFile`
collects completed items into a file, producing a single
downstream item. All use the existing PendingStage/
TranslatePending infrastructure. Covering all 8 acceptance
tests from L2.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
