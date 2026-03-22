# Phase 8: Translation

Ref: [spec.md](spec.md) sections B2, C1, N1, O1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 8.1: B2 - Translate `each` cross-product [parallel with 8.2, 8.3]

spec.md section: B2

At translate time in translate.go, when a process has
`each`-flagged inputs, enumerate the Cartesian product of
regular input items and each-channel items. Create N x M jobs
where N is from regular inputs and M is from each channel.
Each combination gets a unique CWD. All resulting jobs share
the same dep_grp for downstream wiring. Depends on B1
(Phase 4) for the `Each` flag on `Declaration`. Covering all 4
acceptance tests from B2.

- [ ] implemented
- [ ] reviewed

#### Item 8.2: C1 - Translate `eval` output [parallel with 8.1, 8.3]

spec.md section: C1

Append eval command capture to the script body during
translation in translate.go. For `eval('hostname')`, append
`__nf_eval_0=$(hostname)` to the job `Cmd`. Each eval output
gets a unique variable name. Handle processes with both `path`
and `eval` outputs. Covering all 3 acceptance tests from C1.

- [ ] implemented
- [ ] reviewed

#### Item 8.3: N1 - Evaluate dynamic directive closures [parallel with 8.1, 8.2]

spec.md section: N1

Extend directive evaluation in translate.go to detect
`ClosureExpr` in directive values, unwrap the closure body, and
evaluate with `task.*` defaults (`task.attempt = 1`, etc.).
Handles `memory { 2048 * task.attempt }`, `cpus { params.cpus
?: 4 }`, and `errorStrategy { ... }` patterns. Existing
`resolveDirectiveInt` infrastructure extended. Covering all 5
acceptance tests from N1.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Batch 2 (parallel, after batch 1 is reviewed)

#### Item 8.4: O1 - Cross-product CWD and dep_grp structure

spec.md section: O1

Refine the cross-product translation from B2 to use the CWD
pattern `{base}/nf-work/{runId}/{process}/{regIdx}_{eachIdx}`.
Verify all cross-product jobs share the same dep_grp
`nf.<runId>.<process>` and that downstream processes depend on
that dep_grp. Depends on B2 (item 8.1). Covering all 3
acceptance tests from O1.

- [ ] implemented
- [ ] reviewed
