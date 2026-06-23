# Feature: Dep-group dependencies wait for future groups

## Issue

Issue #316: `wr add --deps foo` currently runs immediately if no job with
dep group `foo` has been added yet. Users expect the job to wait until that
dep group appears and completes.

Current behaviour is by design: `--deps` waits only on dep groups that already
exist or are being added. There is no semantic for "hold until a group that may
appear later shows up".

## Required behaviour

Change dep-group dependency semantics, not command/essence dependency
semantics.

- `--deps <group>` must block when `<group>` has never existed.
- The dependency becomes eligible only after at least one job has carried that
  dep group and all live carriers of that group are complete.
- A persistent "dep groups ever seen" record must distinguish "never existed"
  from "existed and already completed".
- Manager restarts must preserve the ever-seen record.
- Existing live dependency re-evaluation is unchanged: if a group whose jobs
  completed later receives new jobs, dependents should re-block as they do
  today.
- Same-batch adds keep working: when an add request includes both a dependent
  job and a carrier for the dep group, dependency resolution must behave as it
  does today.
- `--cmd_deps` and essence dependencies keep today's behaviour when targets do
  not yet exist.

## Diagnostics

Blocking on a never-seen group must be visible and diagnosable.

- At `wr add` time, warn if a depended-on dep group has never been seen. The
  job is still accepted and waits.
- Status output must surface such jobs distinctly, for example as "waiting on a
  dep group not yet seen".
- Add a filter or selector so users can list jobs waiting on never-seen dep
  groups and fix them with `wr mod` or remove them.

## Notes

This is a deliberate behaviour change with documentation/help updates.
No opt-in flag is required.

Release-note the change for existing pipelines: a typo in `--deps` may now
block indefinitely instead of allowing the job to run immediately, but the
blocked condition is warned at add time and visible/filterable in status.

Spec questions should be surfaced to the human only if they require a product
or maintainer choice. Implementation details should be decided from existing
wr patterns or sensible defaults and recorded in the spec.
