# Phase 3: Implement C1 and C2

Ref: [spec.md](spec.md) sections C1, C2

## Instructions

Begin after [phase2.md](phase2.md) is implemented and reviewed.

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

### Item 3.1: C1 - Store expectations and generate readable specifications

spec.md section: C1; Implementation Order step 3

Implement fixed observation comparisons and generated views in
`conformance/render.go`. Cover all four acceptance tests in
`conformance/render_test.go`: `C1_01`, `C1_02`, `C1_03`, and `C1_04`.
Author executable expectation records with all observation fields, reviewed
normalizations, typed values, artifact hashes, and exact error contracts.
Replay raw observations; preserve sequence and multiset multiplicity.
Comparison fixtures are foundation evidence and cannot establish an oracle
pass. Expected files remain unchanged after execution.

Implement the expectation-review hash check needed by `C1_02` now;
complete attempt freshness remains in D2. Verify `E_UAT_MISSING`,
`E_REVIEW_STALE`, and `E_RENDER_MISMATCH` for its three corruptions.
For `C1_03`, check unequal multiplicities, reordered sequences, reviewed
multiset equality, and rejection of global sorting with `E_NORMALIZATION`.
For `C1_04`, use both E1 error contracts with checked empty value sequences.
The import fixture requires zero tasks; missing output requires one task
and script exit 0. Independently replace each diagnostic with a missing-Java
error or append raw `OBS:1`; every mutation fails `E_EXPECTATION`.
Review fixture diagnostic literals now; E1 independently confirms the real
oracle literals before awarding oracle evidence.

Import all 47 numbered spec acceptance tests into foundation requirement
and UAT records with unchanged IDs and source provenance. Independently
compare that complete import with the spec. Records for unfinished tests
remain drafts with measurable cases and null fixture, expected, binding,
and review fields until ready under the spec's schema. Changed expectation
or binding bytes require fresh independent review. Generate
escaped, deterministic pages and checklists under
`.docs/nextflow-conformance/generated/`; check mode detects manual changes.
After this import, update authoritative records first and regenerate views.

- [ ] implemented
- [ ] reviewed

### Item 3.2: C2 - Require exact discoverable Go test bindings

spec.md section: C2

After Item 3.1 review, implement discovery in `conformance/runner.go` with
`go list -json` and `go test -list`. Cover all four acceptance tests in
`conformance/runner_test.go`: `C2_01`, `C2_02`, `C2_03`, and `C2_04`.
Record actual build selection, active source, discovery bytes, and exact
package/test bindings. Use anchored escaped selectors and reject package
patterns, regex test names, unavailable tests, and incompatible evidence
kinds. Discovery and execution use the same build selection, including
`-tags netgo` and `CGO_ENABLED=1`. Execute with `-count=1 -json` and record
actual argv. Prove `C2_03` executes only `TestUAT_ONE`, even when
`TestUAT_ONE_EXTRA` exists, using temporary fixture packages. Implement
this narrow execution path now; complete attempt recording and freshness
belong to the next phase. Missing or excluded tests return 1 with
`E_TEST_MISSING`; invalid-package discovery returns 2 with
`E_TEST_DISCOVERY`. Both leave executed count 0. Evidence-kind mismatches
return 2 with `E_EVIDENCE_KIND`, regardless of fixture success.

- [ ] implemented
- [ ] reviewed

## Exit conditions

All eight C UATs pass. Independent review confirms exactly 47 acceptance
records with unchanged IDs and provenance. Generated pages reproduce their
records, preserve failed and incomplete states, and render byte-identically.
Discovery identifies implemented tests and reports future missing bindings
honestly; it cannot claim that all 47 have executed at this stage. Capture:

```bash
timeout 2m go run ./cmd/wr-conformance render --check
timeout 5m go run ./cmd/wr-conformance discover --suite foundation-bootstrap
timeout 10m env CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance -run '^TestUAT_C[12]_[0-9]+$'
timeout 10m golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

Review discovery's exit and diagnostics against the actual remaining work;
a partial discovery result is not a completed foundation claim.
