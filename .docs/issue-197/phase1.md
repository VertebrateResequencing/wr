# Phase 1: Status and schema foundations

Ref: [spec.md](spec.md) sections B1, A2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: B1 - Expose editable status fields to the UI

spec.md section: B1

Extend `JStatus` and `Job.ToStatus()` in `jobqueue/serverWebI.go` so
websocket details and REST status rows expose `ReqGroup`, `EnvOverrides`,
`Override`, `Priority`, `Retries`, `NoRetryOverWalltime`, and `CwdMatters`.
Add GoConvey coverage in `jobqueue/serverWebI_test.go` for the websocket and
`GET /rest/v1/jobs/<key>` paths. Cover all 2 acceptance tests from B1.

- [x] implemented
- [ ] reviewed

### Item 1.2: A2 - Validate edits and editable states

spec.md section: A2

Add the public `JobModifyViaJSON` schema, body validation, conversion to
`JobModifier`, and the `PATCH` behavior needed for A2's validation and
editable-state outcomes in `jobqueue/serverREST.go`, including range checks,
empty command/cwd errors, env clearing, duplicate-key edits, and the
editable-state policy. Add GoConvey coverage in `jobqueue/rest_test.go` for
bad values and state/identity outcomes. Cover all 12 acceptance tests from A2.
Depends on item 1.1 for the `JStatus` fields asserted by successful A2
responses. The broader A1/A3 field coverage, key mapping, auth integration,
and bulk integration continue in phase 2.

- [x] implemented
- [ ] reviewed
