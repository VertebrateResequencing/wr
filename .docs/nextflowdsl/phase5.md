# Phase 5: Command layer

Ref: [spec.md](spec.md) sections E1, E2, E3, E4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 5.1: E1 - `wr nextflow run` sub-command

spec.md section: E1

Implement cobra commands in `cmd/nextflow.go`: parent
`nextflowCmd` ("wr nextflow") and `nextflowRunCmd`
("wr nextflow run"). Add flags: --config/-c, --params-file/-p,
--param/-P (repeatable KEY=VALUE), --run-id,
--container-runtime (default "singularity"), --follow/-f,
--poll-interval (default 5s), --profile. Positional arg is the
workflow file path. Parse workflow file, load config and params,
call Translate, and submit jobs to wr. Auto-generate run-id as
lowercase hex hash (at least 8 chars, matching
`[0-9a-f]{8,}`) when not provided. Covering all 9 acceptance
tests from E1.

Depends on Phase 4 for Translate().

- [ ] implemented
- [x] implemented
- [x] reviewed

### Batch 2 (parallel, after item 5.1 is reviewed)

#### Item 5.2: E2 - `--follow` dynamic polling [parallel with 5.3]

spec.md section: E2

Extend the run command in `cmd/nextflow.go` to support --follow
mode. Poll jobqueue server at --poll-interval for completed
jobs by rep_grp prefix. Build CompletedJob records and call
TranslatePending for each pending stage to create next-stage
jobs. Exit 0 when all jobs reach terminal state; exit non-zero
if any job is buried. Covering all 4 acceptance tests from E2.

- [x] implemented
- [x] reviewed

#### Item 5.3: E4 - `wr nextflow status` sub-command [parallel with 5.2]

spec.md section: E4

Implement `nextflowStatusCmd` ("wr nextflow status") in
`cmd/nextflow.go`. Add flags --run-id/-r and --workflow/-w.
Query jobs by rep_grp prefix, aggregate per-process counts
(Pending/Running/Complete/Buried/Total), and display as a
table. Show "no jobs found" when no matches. Covering all 3
acceptance tests from E4.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Item 5.4: E3 - `wr nextflow run` resume/recovery

spec.md section: E3

Extend the run command in `cmd/nextflow.go` to support resume
when re-invoked with the same --run-id and --follow.
Regenerate the full DAG, query wr for existing jobs by rep_grp
prefix, and only add missing jobs. Handle job states: skip
complete/running/dependent, remove then re-add buried, re-add
deleted. Covering all 4 acceptance tests from E3.

Depends on Item 5.2 for --follow integration.

- [x] implemented
- [x] reviewed
