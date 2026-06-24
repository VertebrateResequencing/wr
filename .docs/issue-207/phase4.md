# Phase 4: Status And APIs

Ref: [spec.md](spec.md) sections D1, D2, D3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 4.1: D2 - Include Suspended In Client, REST, And Subscriptions

spec.md section: D2

Expose suspended state in status APIs, REST filtering, deletable filters,
subscriptions, and `Job.ToStatus` through `jobqueue/client.go`,
`jobqueue/server.go`, `jobqueue/serverREST.go`, and
`jobqueue/server_subscription.go`, covering all 6 acceptance tests from D2.
Depends on phase 2.

- [x] implemented
- [x] reviewed

### Batch 1 (parallel, after item 4.1 is reviewed)

#### Item 4.2: D1 - Show And Filter Suspended In `wr status` [parallel with D3]

spec.md section: D1

Add `--suspended`, count and summary output, plain/details/table status text,
and filter validation in `cmd/status.go` and `cmd/status_table.go`, covering
all 10 acceptance tests from D1. Depends on item 4.1.

- [x] implemented
- [x] reviewed

#### Item 4.3: D3 - Show Suspended In Web And LSF Views [parallel with D1]

spec.md section: D3

Update web status websocket counts, static status filters, details styling and
text, JavaScript/CSS assets, and `cmd/lsf.go` mapping of suspended jobs to
`PEND`, covering all 5 acceptance tests from D3. Depends on item 4.1.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review pass).
