# Phase 3: Web UI

Ref: [spec.md](spec.md) sections B2, B3, B4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: B2 - Edit one selected job from the details panel

spec.md section: B2

Add the Modify action and modal in `jobqueue/static/status.html`,
`jobqueue/static/js/wr/action-handlers.js`,
`jobqueue/static/js/wr/modal-handlers.js`, and
`jobqueue/static/js/wr/status-viewmodel.js`, or a focused new module under
`jobqueue/static/js/wr/` if that keeps the current style clearer. Pre-fill all
mutable fields, create the exact `PATCH` payload, submit with bearer auth,
replace the old details row with the returned `JStatus`, and only show Modify
for editable states. Cover all 5 acceptance tests from B2. Depends on phases 1
and 2.

- [ ] implemented
- [ ] reviewed

### Item 3.2: B3 - Edit env overrides only

spec.md section: B3

Build the env override editor in the Modify modal so it reads only
`EnvOverrides`, never inherited effective `Env` values, and submits `env` as
the changed override list or `[]` when overrides are cleared. After success,
preserve inherited env display while updating override values in the visible
row. Cover all 3 acceptance tests from B3. Depends on item 3.1 and phase 1's
`EnvOverrides` status field.

- [ ] implemented
- [ ] reviewed

### Item 3.3: B4 - Report web edit failures

spec.md section: B4

Handle failed Modify submissions in `jobqueue/static/js/wr/modal-handlers.js`
so `400 Bad Request` and `409 Conflict` response bodies are shown without a
trailing newline, the modal remains open, and the details row is unchanged.
Cover all 2 acceptance tests from B4. Depends on item 3.1's submit path.

- [ ] implemented
- [ ] reviewed
