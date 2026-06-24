# Phase 1: Add runner live tail capture and touch state

Ref: [spec.md](spec.md) sections A1, A2

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer`
skills.

## Items

### Item 1.1: A1 - Bound live output tail payloads

spec.md section: A1

Implement the unexported `liveTailSaver`, `Write`, and `FlushCompressed`
helpers plus the `liveStdRawTailLimit` and `liveStdCompressedLimit` constants
in `jobqueue/utils.go`. Add `jobqueue/utils_test.go` coverage for all 5
acceptance tests from A1.

- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Send live metrics on existing touches

spec.md section: A2

Implement the unexported `Client.touch(job, endState)` helper and wire
`Touch` and `Execute` in `jobqueue/client.go` to assemble live `JobEndState`
snapshots using the A1 tail saver, actual cwd, resource metrics, and flushed
stdout/stderr tails. Add `jobqueue/client_payload_test.go` coverage for all 5
acceptance tests from A2. Depends on item 1.1.

- [x] implemented
- [x] reviewed
