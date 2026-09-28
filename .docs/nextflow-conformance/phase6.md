# Phase 6: Implement F1 and F2, regenerate views, and execute the final gate

Ref: [spec.md](spec.md) sections F1, F2

## Instructions

Begin after [phase5.md](phase5.md) is implemented and reviewed.

Use the `orchestrator` skill to complete this phase, coordinating fresh
subagents with the `go-implementor` and `go-reviewer` skills. Read these
skill files before implementation or review:

- `/home/ubuntu/.agents/skills/go-implementor/SKILL.md`
- `/home/ubuntu/.agents/skills/go-reviewer/SKILL.md`
- `/home/ubuntu/.agents/skills/go-conventions/SKILL.md`
- `/home/ubuntu/.agents/skills/implementation-principles/SKILL.md`
- `/home/ubuntu/.agents/skills/testing-principles/SKILL.md`

Read the spec's Architecture, assigned stories, and Implementation Order.
Give each item its own implementation and independent review handoff. Keep
its input bundle, tool output, and reasoning within roughly 100k tokens;
a reviewer must approve the bundle's size before work. Use exact relevant
source spans rather than loading the whole acquired tree into context.
Split an oversized item at its acceptance boundaries without adding scope.

Run items sequentially, starting the next only after the prior review
passes. Independent semantic reviews may run concurrently after source
locking, with separate assigned spans; implementation remains sequential.
Each acceptance ID maps to `TestUAT_<ID>` in its specified test file. Record
a meaningful failing command before implementation and a passing command
afterwards. Tests exercise observable CLI/results and artifacts. Runner
tests use temporary fixture subjects and never invoke their outer suite.

Use bounded commands from the repository root. Run focused GoConvey tests
with `CGO_ENABLED=1`, `-tags netgo`, and `-count=1`, plus relevant linter
checks. Report baseline or unrelated failures without unrelated edits.
Full wr repository tests are outside this isolated tooling plan. Save CLI
JSON, stderr, exit codes, and artifact paths for independent review; a
missing prerequisite leaves the affected item incomplete.

## Items

### Item 6.1: F1 - Seed later milestones without awarding them completion

spec.md section: F1

Extend `conformance/coverage.go` and the authoritative ledger with all nine
named wr requirements, linked measurable draft UATs, accepted-prompt
provenance, origin `wr`, scope `required`, and null runtime bindings. Keep
these seeds outside the foundation execution denominator and inside the
target ledger and runtime profile. Cover all three acceptance tests in
`conformance/milestones_test.go`: `F1_01`, `F1_02`, and `F1_03`.

Preserve both unresolved policies and all bootstrap semantics. Enforce the
foundation, target accounting/policy, durable runtime slice, and broad
operator dependency order. Require real crash and runtime evidence for the
later claims; queue, parser, and oracle passes cannot discharge them.
Runtime and target inventory remain incomplete after foundation success.
The later durable slice must cover every supported invocation mode and
retain the crash receipts and logical task IDs required by F1. Actual wr
implementation mutations remain required once an adapter exists.

- [ ] implemented
- [ ] reviewed

### Item 6.2: F2 - Generate bounded handoffs and a durable evidence ledger

spec.md section: F2; Implementation Order final gate

After Item 6.1 review, extend `conformance/render.go` with generated batch
briefings and a durable ledger. Cover all three acceptance tests in
`conformance/render_test.go`: `F2_01`, `F2_02`, and `F2_03`. Generate exact
assigned IDs, dependencies, source excerpts and hashes, relevant inputs,
commands and deadlines, unresolved questions, and required profiles.
Require independent approval of bounded input bundles and reject cycles.

Derive every checked item by revalidating current evidence. Preserve
historical attempts, decisions, reviews, reconciliations, and artifact
links; missing and stale evidence leave unchecked items. Regenerate every
view from records and execute the complete final foundation gate.

- [ ] implemented
- [ ] reviewed

## Exit conditions

All six F UATs pass. Review the final mapping of all 49 acceptance IDs to
real GoConvey functions in their specified files, current reviewed records,
and runner evidence. All seven oracle cases, all 18 accounting mutations,
and all three semantic observer mutations have the required actual proof.
Byte-complete extraction, independent bootstrap semantic reviews, generated
views, and current input/artifact hashes satisfy the spec's final gate.
The mapping includes
`A1_06` and `A1_07`; review actual byte-preserving acquisition, both runtime
mutation subcases, and packaging/lock-hash bypass failures. Oracle evidence
must identify the unchanged opaque distribution and its actual external
execution inputs, with hashed POMs retained as provenance.

Run the following focused gates and save JSON, logs, and exit codes. Bound
any required follow-up command with a recorded deadline and communicate
progress during longer checks:

```bash
timeout 2m go run ./cmd/wr-conformance validate
timeout 2m go run ./cmd/wr-conformance extract --check
timeout 2m go run ./cmd/wr-conformance render
timeout 5m go run ./cmd/wr-conformance discover --suite foundation-bootstrap
timeout 20m go run ./cmd/wr-conformance run --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-conformance render
timeout 2m go run ./cmd/wr-conformance verify --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-conformance render --check
timeout 20m env CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance/... ./cmd/wr-conformance/...
timeout 10m golangci-lint run ./conformance/... ./cmd/wr-conformance/...
timeout 2m go run ./cmd/wr-conformance verify --suite target-inventory
timeout 2m go run ./cmd/wr-conformance verify --suite wr-runtime
```

Foundation CLI commands and focused tests must return 0 with zero missing,
skipped, failed, timed-out, or stale foundation UATs. Both later-profile
verify commands must return 1 with their outstanding work. Render before
the gate, then regenerate after the new attempt so the final verification
and view check use its evidence. Reports retain unresolved decisions
`D_TYPED_MILESTONE` and `D_JVM_PLUGIN_POLICY`, nonzero pending target
inventory, all nine runtime seed IDs, `E_ADAPTER_UNAVAILABLE`, and zero wr
runtime passes. Inspect those
expected nonzero results explicitly. Baseline linter failures are reported
without broad unrelated fixes; they cannot erase a failed foundation gate.
