# Phase 6: Scheduler compatibility tests

Ref: [spec.md](spec.md) sections F1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 6.1: F1 - Compile old Scheduler callers unchanged

spec.md section: F1

Add `client/client_compat_test.go` using `package client_test`. Define
compile-only interfaces and helpers that mirror the current `ibackup`,
`wrstat`, and `wrstat-ui` uses of `client.Scheduler`, including `client.New`,
`client.DefaultRequirements`, `client.UniqueString`, `Executable`, `NewJob`,
`SubmitJobs`, `FindIncompleteJobsByRepGroup`,
`GetLastCompletionTimeByRepGroup`, `SubmittedJobs`,
`FindJobsByRepGroupSuffix`, `FindJobsByRepGroupPrefixAndState`, `KillJobs`,
`RemoveJobs`, and `Disconnect`. The test imports only `client`, `jobqueue`,
and `jobqueue/scheduler`; it must not require sibling repos, a wr manager, or
changed downstream source files. Covering all 3 acceptance tests from F1.
Depends on public signatures existing and can run in parallel with Phases 4
and 5.

- [x] implemented
- [x] reviewed
