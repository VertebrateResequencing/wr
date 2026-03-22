# Phase 8: Translation

Ref: [spec.md](spec.md) sections A2, B2, C1, I2 translate,
N1, O1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 8.1: A2 - Translate scratch, storeDir, conda, spack [parallel with 8.2, 8.3, 8.4, 8.5]

spec.md section: A2

Implement directive translation in translate.go for `scratch`,
`storeDir`, `conda`, and `spack`. `scratch true|'/path'` wraps
the job command to create a temp directory, run the script
there, and copy outputs back. `storeDir '/path'` adds an
output-existence check and skip logic. `conda 'env'` prepends
`conda activate <env> &&` to the command. `spack 'pkg'`
prepends `spack load <pkg> &&`. Handle combined directives
(e.g. conda + scratch). Covering all 8 acceptance tests
from A2.

- [ ] implemented
- [ ] reviewed

#### Item 8.2: B2 - Translate `each` cross-product [parallel with 8.1, 8.3, 8.4, 8.5]

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

#### Item 8.3: C1 - Translate `eval` output [parallel with 8.1, 8.2, 8.4, 8.5]

spec.md section: C1

Append eval command capture to the script body during
translation in translate.go. For `eval('hostname')`, append
`__nf_eval_0=$(hostname)` to the job `Cmd`. Each eval output
gets a unique variable name. Handle processes with both `path`
and `eval` outputs. Covering all 3 acceptance tests from C1.

- [ ] implemented
- [ ] reviewed

#### Item 8.4: I2 translate - Translate onComplete/onError [parallel with 8.1, 8.2, 8.3, 8.5]

spec.md section: I2

Implement onComplete/onError translation in translate.go.
`onComplete` creates a final wr job whose DepGroups list all
workflow-stage dep_grps, executing the parsed onComplete body
as a shell script when all stages finish. `onError` creates a
polling wr job with a time-based limit group that checks for
buried/failed jobs via wr client API, resubmits itself while
the workflow is running, and executes the onError body on
terminal error detection. Depends on I2 parse (Phase 5) for
parsed `OnComplete`/`OnError` fields. Covering acceptance
tests 5-8 from I2.

- [ ] implemented
- [ ] reviewed

#### Item 8.5: N1 - Evaluate dynamic directive closures [parallel with 8.1, 8.2, 8.3, 8.4]

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

#### Item 8.6: O1 - Cross-product CWD and dep_grp structure

spec.md section: O1

Refine the cross-product translation from B2 to use the CWD
pattern `{base}/nf-work/{runId}/{process}/{regIdx}_{eachIdx}`.
Verify all cross-product jobs share the same dep_grp
`nf.<runId>.<process>` and that downstream processes depend on
that dep_grp. Depends on B2 (item 8.2). Covering all 3
acceptance tests from O1.

- [ ] implemented
- [ ] reviewed
