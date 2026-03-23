# Phase 3: Ext Directive + Config Propagation

Ref: [spec.md](spec.md) sections F1, J1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel, after phase 2 is reviewed)

#### Item 3.1: F1 - Resolve `task.ext.*` in script interpolation [parallel with 3.2]

spec.md section: F1

Implement `resolveExtDirective` in `nextflowdsl/translate.go`.
Collect `ext` values from process-level directive, config-level
`ProcessDefaults.Ext`, and selector-scoped `ext`. Merge at key
level (config overrides process-level per Nextflow semantics,
selectors override config defaults). Evaluate closure-valued
`ext` entries with per-item input bindings. During script
interpolation, resolve `${task.ext.args}` and other
`task.ext.*` references from the merged ext map; substitute
empty string if not found. Covering all 6 acceptance tests
from F1.

- [x] implemented
- [x] reviewed

#### Item 3.2: J1 - Extend config parsing for all directive types [parallel with 3.1]

spec.md section: J1

Extend `parseProcessAssignment` in `nextflowdsl/config.go` to
recognise and store all directive types listed in
`ProcessDefaults`: `errorStrategy`, `maxRetries`, `maxForks`,
`publishDir`, `queue`, `clusterOptions`, `ext`,
`containerOptions`, `accelerator`, `arch`, `shell`,
`beforeScript`, `afterScript`, `cache`, `scratch`, `storeDir`,
`module`, `conda`, `spack`, `fair`, `tag`. Directives not in
the typed set go into `ProcessDefaults.Directives` catch-all.
Apply config defaults at translate time; selector-scoped values
override defaults. Covering all 8 acceptance tests from J1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).
