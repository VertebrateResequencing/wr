# Phase 4: Implement D1 and D2

Ref: [spec.md](spec.md) sections D1, D2

## Instructions

Begin after [phase3.md](phase3.md) is implemented and reviewed.

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

### Item 4.1: D1 - Derive status from complete execution events

spec.md section: D1

Extend `conformance/runner.go` to record runner-owned subprocess events,
stdout, stderr, exits, and artifacts. Cover all four acceptance tests in
`conformance/runner_test.go`: `D1_01`, `D1_02`, `D1_03`, and `D1_04`.
Require exact run/terminal/package events, successful exits, completed
subtests, and observations for a pass. Reject malformed, contradictory,
missing, and truncated evidence at the recorder boundary.

Enforce per-UAT deadlines, the suite ceiling, process-group cleanup, and
log limits. Publish manifests atomically only after closing and hashing
logs. Prove skip, failure, timeout, zero execution, missing observation,
and interruption failures before reports may use a passing state.

- [ ] implemented
- [ ] reviewed

### Item 4.2: D2 - Bind evidence to the code, corpus, tests, and environment

spec.md section: D2

After Item 4.1 review, implement `conformance/evidence.go`. Cover all five
acceptance tests in `conformance/evidence_test.go`: `D2_01`, `D2_02`,
`D2_03`, `D2_04`, and `D2_05`. Hash the complete D2 input inventory before
and after execution, including dirty and untracked inputs, active source
and dependencies outside the bound package, tool identities, effective
environment, and applicable runtime artifacts. Include the opaque runtime's
full-file hash, Java tree, required tools, and actual external JARs; retain
POM provenance through the lock and corpus input hashes. For `D2_01`, change
the distribution independently of the other input subcases and require
`E_EVIDENCE_STALE` with passed count 0. Bundled classes remain covered by the
full distribution hash. Fix the permitted output exclusions in the tool
and start children from an allowlisted environment.

Revalidate raw events and artifacts on verify. Select the newest completed
attempt for the exact input key by runner sequence, preserving historical
failures and stale records. Prove input-change races with a controlled
barrier, newer-failure precedence, and generated-view independence.

- [ ] implemented
- [ ] reviewed

## Exit conditions

All nine D UATs pass using actual temporary subprocess subjects. Their raw
logs and manifests can be reverified; corrupted or stale evidence cannot
count as passing. Deadlines leave no child alive, interrupted attempts stay
incomplete, and a newer failure supersedes an older pass. Capture:

```bash
timeout 15m env CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance -run '^TestUAT_D[12]_[0-9]+$'
timeout 2m go run ./cmd/wr-conformance verify --suite foundation-bootstrap
timeout 10m golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

The production bootstrap verify remains incomplete until the remaining
oracle, mutation, and handoff requirements have current evidence. Check its
JSON identifies the real missing work instead of accepting an old pass.
