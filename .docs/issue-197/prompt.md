# Feature: Modify jobs via REST and web UI

## Issues

Issue #197 asks for job modification through the web UI and public REST API,
matching the capability already exposed by `wr mod` on the CLI.

Issue #19 is folded into this feature. It asks for web editing of command env
vars and expected resource requirements for buried, delayed, and other
non-running incomplete jobs.

REST currently supports only GET/POST/DELETE on jobs. The web UI has no modify
action. Server-side modification already exists through `wr mod`.

## Required behaviour

Add REST and web UI job editing for non-running incomplete jobs.

- Add a REST PUT or PATCH endpoint that applies `wr mod`-style changes using
  existing server-side validation and modification logic where possible.
- No auth model change: use the existing token/auth behaviour.
- The web UI must allow editing every field it currently displays for a job,
  for any non-running incomplete job.
- Running jobs must not be editable in v1.
- Include env vars and resource requirements from #19.
- Include fields shown by the web UI such as requirements, env, priority,
  retries, limit groups, and behaviours where they are displayed.
- Add a Modify action/button in the job details UI.
- Invalid edits must be reported with an error popup.
- Successful edits should update the displayed job state without requiring a
  manual page refresh.

## Notes

Deliver #197 and #19 together on branch `feat/issue-197`. There is no separate
#19 branch or PR.

Editable fields in v1 are the mutable job fields that existing `wr mod`-style
server logic can already validate and change, when those fields are displayed
by the web UI. Immutable identity, status, output, and execution-result fields
remain read-only even if displayed.

Environment editing modifies job-specific environment overrides, matching the
existing `wr mod --env` model. It must not replace the full effective inherited
environment unless existing modify logic already supports that safely.

If a modification changes a job key, REST responses must expose enough mapping
for callers and the web UI to track the edited job after the update, for
example old key to new key.

The web UI v1 Modify action edits one selected job at a time. Bulk edit of all
matching or similar jobs is out of scope.

Spec questions should be surfaced to the human only if they require a product
or maintainer choice. Implementation details should be decided from existing
wr patterns or sensible defaults and recorded in the spec.
