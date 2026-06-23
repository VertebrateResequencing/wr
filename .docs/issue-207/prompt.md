# Feature: Suspend and resume non-running jobs

## Issue

Issue #207 asks for first-class suspend and resume of jobs, so users can pause
queued work and let urgent work run without relying on the workaround of
setting a limit group to zero.

There is no suspended job state today. A real state affects queue scheduling,
status counts, CLI selectors and filters, REST, persistence, and the web UI.

## Required behaviour

Add a first-class suspended state for non-running jobs.

- Add `wr suspend` and `wr resume` commands.
- Use the usual job selection flags such as `-i`, `-z`, and `-y`, following
  existing `wr mod`/status selection conventions where appropriate.
- Suspending running jobs is out of scope for v1.
- Suspended jobs must not be scheduled.
- To dependency and limit-group systems, suspended jobs should behave like jobs
  in pending/ready-style non-running states: they remain present in the queue
  and continue to participate in dependency/limit accounting as appropriate,
  but they are not candidates for reservation until resumed.
- Resuming restores the job to the appropriate non-running schedulable or
  dependency-waiting state based on current dependencies and limits.
- The state must persist across manager restarts.

## Visibility

Expose suspended jobs everywhere users inspect state.

- `wr status` must show, count, and filter suspended jobs.
- REST/status APIs must include the new state.
- The web UI must show suspended jobs, add a status filter for them, and may
  reuse the delayed colour.
- The web UI scope is included in v1.

## Notes

No web-terminal or running-process pause is required.

Spec questions should be surfaced to the human only if they require a product
or maintainer choice. Implementation details should be decided from existing
wr patterns or sensible defaults and recorded in the spec.
