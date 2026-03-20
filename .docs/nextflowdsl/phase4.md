# Phase 4: Translation

Ref: [spec.md](spec.md) sections D1, D2, D3, D4, D5, D6

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: D1 - Static DAG translation

spec.md section: D1

Implement `Translate(wf *Workflow, cfg *Config,
tc TranslateConfig) (*TranslateResult, error)` in
`nextflowdsl/translate.go`. Define `TranslateConfig`,
`TranslateResult`, `PendingStage`, and `CompletedJob` types.
Convert parsed workflows into `[]jobqueue.Job` with correct
dep_grps, deps, rep_grp, req_grp, CwdMatters=true,
deterministic Cwd paths
(`{Cwd}/nf-work/{runId}/{processName}/`), resource mapping
(cpus/memory/time/disk with Override=0), container runtime
selection, maxForks limit groups, errorStrategy handling, env
overrides, config defaults, profile overrides, params
substitution in scripts, and Groovy closure fallback with
warnings. Covering all 19 acceptance tests from D1.

Depends on Phases 1-3 for AST, config, params, Groovy, and
module types.

- [ ] implemented
- [x] implemented
- [x] reviewed

### Batch 2 (parallel, after item 4.1 is reviewed)

#### Item 4.2: D2 - publishDir translation [parallel with 4.3, 4.4, 4.5]

spec.md section: D2

Extend `Translate` in `nextflowdsl/translate.go` to generate
OnSuccess Run behaviours for `publishDir` directives. Handle
copy (default), link, and move modes. Resolve relative paths
against the workflow file directory. Substitute params in
publishDir paths. Support multiple publishDir directives and
glob patterns. Covering all 8 acceptance tests from D2.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 4.3: D3 - Subworkflow translation [parallel with 4.2, 4.4, 4.5]

spec.md section: D3

Extend `Translate` in `nextflowdsl/translate.go` to inline
subworkflow processes with the subworkflow name in rep_grp,
dep_grp, and Cwd hierarchies
(`{Cwd}/nf-work/{runId}/{subwf}/{processName}/`). Detect and
error on rep_grp collisions. Covering all 3 acceptance tests
from D3.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 4.4: D4 - Dynamic workflow detection [parallel with 4.2, 4.3, 4.5]

spec.md section: D4

Extend `Translate` in `nextflowdsl/translate.go` to classify
stages as pending when any input channel derives from an
upstream process's `path`/`file` output (all `path`/`file`
outputs are dynamic; only `val` outputs are static). Implement
`TranslatePending` to resolve pending stages into concrete
jobs given completed upstream job info. Covering all 4
acceptance tests from D4.

- [ ] implemented
- [x] implemented
- [x] reviewed

#### Item 4.5: D5 - Channel factory resolution [parallel with 4.2, 4.3, 4.4]

spec.md section: D5

Implement `ResolveChannel(ce ChanExpr, cwd string)
([]any, error)` in `nextflowdsl/channel.go`. Resolve
`Channel.of`, `Channel.value`, `Channel.empty`,
`Channel.fromPath`, and `Channel.fromFilePairs` factories.
Create multiple jobs per process when N > 1 items, with
indexed Cwd (`{Cwd}/nf-work/{runId}/{process}/{itemIdx}/`)
and DepGroups (`nf.{runId}.{process}.{itemIdx}`). Covering
all 6 acceptance tests from D5.

- [ ] implemented
- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Item 4.6: D6 - Channel operator effects on translation

spec.md section: D6

Extend `ResolveChannel` in `nextflowdsl/channel.go` to apply
operator transformations (`collect`, `first`, `last`, `take`,
`filter`, `map`, `flatMap`, `mix`, `join`, `groupTuple`) that
modify item cardinality during translation. Covering all 6
acceptance tests from D6.

Depends on Item 4.5 for channel factory resolution.

- [ ] implemented
- [x] implemented
- [x] reviewed
