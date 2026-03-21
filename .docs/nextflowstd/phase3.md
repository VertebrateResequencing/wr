# Phase 3: Follow Output Display

Ref: [spec.md](spec.md) sections B1

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 3.1: B1 - Display output for newly completed/buried jobs during follow

spec.md section: B1

Modify `followNextflowWorkflow()` in `cmd/nextflow.go` to track
displayed job keys and, each poll iteration, read `.nf-stdout` and
`.nf-stderr` from newly terminal (complete or buried) jobs. Format
output using `formatJobOutput()` and `printJobsOutput()` helpers
from phase 2. Multi-instance detection uses all known jobs
(complete + buried + incomplete). Depends on phases 1 and 2.
Covering all 8 acceptance tests from B1.

- [x] implemented
- [x] reviewed
