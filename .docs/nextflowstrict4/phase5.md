# Phase 5: When Guard + randomSample

Ref: [spec.md](spec.md) sections C1, H1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel, after phase 4 is reviewed)

#### Item 5.1: C1 - Evaluate `when:` guard in follow loop [parallel with 5.2]

spec.md section: C1

Implement `EvalWhenGuard` in `nextflowdsl/translate.go`. At
translate time, if a process has a non-empty `When` field, mark
the stage as pending. In the follow loop, when input bindings
are resolved, evaluate the `when:` expression using the Groovy
evaluator. If true, call `TranslatePending` to create jobs and
downstream wiring. If false, emit nothing on output channels
(downstream processes simply don't execute for those items).
Update `cmd/nextflow.go` for follow-loop integration. Depends
on Phase 1 evaluator enhancements. Covering all 6 acceptance
tests from C1.

- [x] implemented
- [x] reviewed

#### Item 5.2: H1 - Implement `randomSample` via PendingStage [parallel with 5.1]

spec.md section: H1

Add `randomSample(n)` and `randomSample(n, seed)` channel
operator support in `nextflowdsl/channel.go` and
`nextflowdsl/translate.go`. Use `PendingStage` to wait for all
upstream items via `awaitRepGrps`, then in `TranslatePending`
collect completed items and randomly sample N. If seeded, use
deterministic sampling. If N >= total items, all pass through.
Covering all 4 acceptance tests from H1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
