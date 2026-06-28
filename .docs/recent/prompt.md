# Feature: `wr status --recent` for jobs that finished in a time window

## Background

`wr status` can currently select jobs by file (`-f`), report-group identifier
(`-i`), command line (`-l`), or default to the user's incomplete commands. There
is no way to ask "which jobs *finished* recently, regardless of report group?".

Operators frequently want exactly that: a single view of everything that ended
in the last day or week, to spot recent failures or confirm a batch completed,
without knowing or enumerating report groups.

## Required behaviour

Add a new selection mode to `wr status`:

- A new flag, `--recent <duration>`, returns every job belonging to the user
  that *finished running* (reached a terminal state, i.e. has a non-zero end
  time) within the last `<duration>`, across all report groups.
- `--recent` is a job-selection mode mutually exclusive with `-f`, `-i`, and
  `-l`. Supplying more than one selection mode must fail with a clear error, the
  same way the existing modes already conflict.
- The duration accepts Go's standard `time.ParseDuration` units (`s`, `m`, `h`)
  **and** the convenience units `d` (days) and `w` (weeks), which the stdlib
  does not support. For example `--recent 90m`, `--recent 1d`, `--recent 2w`.
  An invalid duration must produce a clear error.
- "Finished within the last duration" means the job's end time is later than
  `now - duration`. Only jobs that have been successfully archived to the
  complete store are considered; incomplete jobs and jobs not yet archived are
  never returned by this mode.
- The mode must honour the same secondary options the other status modes do:
  state filter, result limit, and the `--std` / `--env` extra-detail toggles, so
  output and `-o` formatting behave consistently with the existing modes.

## Diagnostics and help

- Update the `wr status` long help to document `--recent`, its mutual exclusion
  with the other selectors, and the `d`/`w` duration units, with an example
  (e.g. "`--recent 1w` reports jobs that finished in the last week").

## Performance and data model

The natural implementation is a time-ordered index from end time to job key, so
the recent-window query is a bounded range scan rather than a full table scan.

- Persist an end-time → job-key lookup when a job is archived, keyed so that a
  lexical/byte range scan over `[now-duration, now]` yields the matching keys
  efficiently (an RFC3339 timestamp prefix works because it sorts
  chronologically).
- The query should seek to the start of the window and stop at the end, decoding
  each referenced job from the existing complete-jobs store.
- Any place that deletes or rewrites job lookup entries (e.g. when live jobs are
  modified or removed) must keep this new index consistent, exactly as it
  maintains the other lookup indexes today. Do not leave stale end-time entries
  pointing at deleted keys.
- This change must not noticeably slow down job throughput or any existing
  operation. Writing the new index on archive, and maintaining it on
  modify/remove, adds work to hot paths, so the implementation must keep that
  overhead negligible. Benchmarking is a required part of implementation: run the
  project's benchmarks (e.g. `make bench`) before and after the change and
  confirm there is no measurable regression in archive/add/modify throughput. If
  a regression appears, it must be resolved (or the data-model approach
  reconsidered) before the work is considered done.
- Note that current `develop` already maintains a *report-group* end-time record
  (`bucketRGEndTime` / `updateRGEndTime`, added during the status rework) that
  stores each report group's latest end time. That is related but distinct: it
  is per-report-group latest-time for ordering, not a per-job time index for
  windowed retrieval. The spec should decide whether to extend that machinery or
  add a sibling per-job index, and justify the choice; the requirement is the
  windowed per-job query, however it is best stored.

## Suggested API shape

Names are for the spec to settle, but the capability needed is:

- A `jobqueue.Client` method analogous to `GetIncomplete` / `GetByRepGroup`,
  e.g. `GetRecent(period time.Duration, limit int, state JobState, getStd, getEnv bool) ([]*Job, error)`,
  that returns the finished-in-window jobs.
- A matching server request handler and a server-side helper that retrieves the
  archived jobs from the end-time index, then applies the shared
  limit/state/std/env filtering used by the other getters.
- The `cmd/status.go` wiring: a `--recent` flag, inclusion in the
  mutually-exclusive selection count, duration parsing (with `d`/`w` support),
  and a call into the new client method.

## Acceptance criteria

- With a running manager, `wr status --recent 1h` lists jobs that ended in the
  last hour across all report groups and omits jobs that ended earlier and jobs
  that are still incomplete.
- `--recent 1d` and `--recent 2w` parse and behave as 24h and 14d respectively;
  an unparseable duration errors clearly.
- Combining `--recent` with any of `-f`, `-i`, `-l` errors with the existing
  mutually-exclusive message (extended to mention `--recent`).
- An archived job whose end time is within the window appears; one whose end
  time is just outside the window does not.
- `--recent` respects state filter, limit, and `--std`/`--env`, and renders
  under all `-o` output formats like the other modes.
- The end-time index is populated on archive and cleaned up when jobs are
  modified/removed, with no stale entries and no duplicate jobs in results.
- New GoConvey tests cover the client/server path in `jobqueue` (a job becomes
  retrievable via the recent query after it completes, and drops out once it is
  older than the window) and the duration parsing in `cmd`.
- Manager restart preserves the ability to query recently-finished archived
  jobs (the index is durable).
- Benchmarks (`make bench`) run before and after the change show no noticeable
  regression in job archive/add/modify throughput; any regression introduced by
  the end-time index is resolved before completion.

## Out of scope

- Changing how jobs are scheduled, run, or archived beyond adding/maintaining
  the end-time index.
- Reworking the existing `-f`/`-i`/`-l` selection modes or the status output
  formats themselves.
- Any web-UI changes (this is a CLI/`jobqueue` feature); a web surface can be a
  later feature.
- Cross-user queries; `--recent` stays scoped to the requesting user like the
  other modes.

## Notes

Spec questions should be surfaced to the human only if they require a product or
maintainer choice. Implementation details should be decided from existing wr
patterns or sensible defaults and recorded in the spec.
