# Phase 3: Process Directive Parsing

Ref: [spec.md](spec.md) sections A1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: A1 - Parse all remaining process directives

spec.md section: A1

Add all missing Nextflow-defined process directives to the
accepted set in `parseProcessDirective` in parse.go:
`accelerator`, `arch`, `array`, `clusterOptions`, `conda`,
`containerOptions`, `debug`, `echo`, `executor`, `ext`, `fair`,
`machineType`, `maxErrors`, `maxSubmitAwait`, `penv`, `pod`,
`queue`, `resourceLabels`, `resourceLimits`, `scratch`,
`secret`, `spack`, `stageInMode`, `stageOutMode`, `storeDir`,
and `shell` (as directive). Also handle unknown directives with
a warning, `stage:` section skipping, and `topic:` output
qualifier. All stored via `parseDirectiveExpr` with
`warnStoredDirective`. Modify `errorStrategy` and `container`
directive parsing to accept any `Expr` (including
`ClosureExpr`), falling back to `Directives[name]` for
non-string expressions. Covering all 30 acceptance tests
from A1.

- [ ] implemented
- [ ] reviewed
