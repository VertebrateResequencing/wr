# Phase 4: LSF Directives + Fair + Finish

Ref: [spec.md](spec.md) sections D1, E1, G1, I1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel, after phase 3 is reviewed)

#### Item 4.1: D1 - Map `accelerator` to LSF cluster options [parallel with 4.2, 4.3, 4.4]

spec.md section: D1

Map `accelerator` directives to LSF GPU resource strings in
`nextflowdsl/translate.go`. When `TranslateConfig.Scheduler`
is `"lsf"`, append
`-R "select[ngpus>0] rusage[ngpus_physical=N]"` to
`Requirements.Other["scheduler_misc"]`. Type parameter stored
as informational warning. When scheduler is not LSF, store with
a warning. Merge with existing `clusterOptions`. Depends on J1
for config-level `accelerator` values. Covering all 4
acceptance tests from D1.

- [x] implemented
- [x] reviewed

#### Item 4.2: E1 - Map `arch` to LSF cluster options [parallel with 4.1, 4.3, 4.4]

spec.md section: E1

Map `arch` directive to LSF architecture selection in
`nextflowdsl/translate.go`. When scheduler is `"lsf"`, map
`linux/x86_64` to `-R "select[type==X86_64]"` and
`linux/aarch64` to `-R "select[type==AARCH64]"`, appended to
`Requirements.Other["scheduler_misc"]`. When scheduler is not
LSF, store with a warning. Depends on J1 for config-level
`arch` values. Covering all 3 acceptance tests from E1.

- [x] implemented
- [x] reviewed

#### Item 4.3: G1 - Map `fair` to job priority [parallel with 4.1, 4.2, 4.4]

spec.md section: G1

Set `Job.Priority` for processes with `fair true` in
`nextflowdsl/translate.go`. Priority = `255 - min(index, 254)`:
job 0 gets 255, job 1 gets 254, etc., clamped at 1. Processes
with `fair false` or no `fair` directive use default priority 0.
Depends on J1 for config-level `fair` values. Covering all 3
acceptance tests from G1.

- [x] implemented
- [x] reviewed

#### Item 4.4: I1 - Translate `errorStrategy 'finish'` [parallel with 4.1, 4.2, 4.3]

spec.md section: I1

Implement `finishStrategyLimitGroup` in
`nextflowdsl/translate.go`. Assign all jobs from a
`finish`-strategy process a unique limit group
`nf-finish-<processName>-<runID>`. In the follow loop in
`cmd/nextflow.go`, detect buried jobs belonging to
`finish`-strategy processes and set the limit group count to 0
to prevent new jobs from starting. Other processes continue
independently. Depends on J1 for config-level `errorStrategy`
values. Covering all 4 acceptance tests from I1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
