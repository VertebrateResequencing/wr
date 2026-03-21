# Phase 2: Output Reading and Formatting Helpers

Ref: [spec.md](spec.md) sections D1, D2, D3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 2.1: D1 - readCapturedOutput [parallel with 2.2]

spec.md section: D1

Implement `readCapturedOutput(path string, maxBytes int64)
(string, error)` in `cmd/nextflow.go`. Returns `("", nil)` for
missing, empty, whitespace-only files or read errors. Truncates
to `maxBytes` with `"[... output truncated ...]"` indicator.
Covering all 5 acceptance tests from D1.

- [ ] implemented
- [ ] reviewed

#### Item 2.2: D2 - formatJobOutput and jobOutputLabel [parallel with 2.1]

spec.md section: D2

Implement `formatJobOutput()`, `jobOutputLabel()`, and
`instanceIndexFromCwd()` in `cmd/nextflow.go`. Produces the
spec'd single-line and multi-line output formats. Labels use
`[process]` or `[process (index)]` depending on multi-instance.
Covering all 10 acceptance tests from D2.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Batch 2 (parallel, after batch 1 is reviewed)

#### Item 2.3: D3 - printJobsOutput

spec.md section: D3

Implement `printJobsOutput()` in `cmd/nextflow.go`. Iterates
display jobs, reads capture files via `readCapturedOutput()`,
formats via `formatJobOutput()`, and writes output grouped by
process name alphabetically then by index. Uses `allJobs` for
multi-instance detection. Depends on items 2.1 and 2.2.
Covering all 4 acceptance tests from D3.

- [ ] implemented
- [ ] reviewed
