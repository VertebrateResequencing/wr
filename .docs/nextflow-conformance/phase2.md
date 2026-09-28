# Phase 2: Implement A2, B1, and B2 sequentially

Ref: [spec.md](spec.md) sections A2, B1, B2

## Instructions

Begin after [phase1.md](phase1.md) is implemented and reviewed.

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

### Item 2.1: A2 - Preserve source units and expose incomplete meaning

spec.md section: A2

Implement `nextflowconformance/extract.go` and independent fixtures under
`nextflowconformance/testdata/`. Cover all four acceptance tests in
`nextflowconformance/extract_test.go`: `A2_01`, `A2_02`, `A2_03`, and `A2_04`.
Prove exact partitioning and source-unit kinds on hand-labelled LF, CRLF, and
final-line fixtures before extracting the complete pinned file set. Resolve
recursive includes, grammar alternatives, source declarations, upstream tests,
and bootstrap selectors to exact spans. Preserve unknown constructs and
pending blocks; retain reconciliation records on changes. Use the complete
file list and bootstrap selectors in A2. Reject absent or ambiguous selectors;
reuse their locked spans rather than narrowing scope.

Run extraction twice against the real locked corpus. Compare bytes and
IDs, independently check each leaf partition, and resolve included map/mix
snippets. Corrupt warnings, signatures, and alternatives to prove check
mode detects their removal without deriving its expectation from output.

- [ ] implemented
- [ ] reviewed

### Item 2.2: B1 - Link source facets to independently reviewed obligations

spec.md section: B1

After Item 2.1 review, implement semantic accounting in
`nextflowconformance/coverage.go`. Cover all five acceptance tests in
`nextflowconformance/coverage_test.go`: `B1_01`, `B1_02`, `B1_03`, `B1_04`,
and `B1_05`. Author obligations and requirement/case links from original
bootstrap spans, then obtain independent semantic review with exact input
hashes. Extraction cannot approve these records.

Create linked case records needed for coverage now. Keep unfinished UATs
as schema-valid drafts; C1 and C2 supply their executable expectations and
bindings in phase 3. Draft case links never satisfy reviewed-UAT or
execution gates. Later changes to review-bound inputs require fresh review.

Check per-facet and bidirectional coverage, cycles, provenance, placeholders,
review independence, and staleness. Verify real typed overloads, defaults,
exceptions, and feature flags independently of extraction counts. Preserve
historical reviews and leave all other selected semantics pending.

- [ ] implemented
- [ ] reviewed

### Item 2.3: B2 - Keep policy decisions separate from observations

spec.md section: B2

After Item 2.2 review, extend `nextflowconformance/model.go` and seed both
unresolved policy decisions with their affected requirements in
`nextflowconformance/data/`. Cover all three acceptance tests in
`nextflowconformance/model_test.go`: `B2_01`, `B2_02`, and `B2_03`. Verify
scope and observation status independently through CLI results. Explicit
reviewed decisions alone authorize exclusion; an oracle observation cannot
settle compatibility policy.

Provide the scope check used by `verify --suite wr-runtime` for `B2_01`
now: unresolved decisions return 1 with `E_SCOPE_UNRESOLVED`. This check
cannot award runtime completion; D1 and D2 add execution and freshness
verification in phase 4. Use isolated records for the resolution and
observation cases; keep both production decisions unresolved.

- [ ] implemented
- [ ] reviewed

## Exit conditions

All 12 assigned UATs pass. Independent review accepts the bootstrap
obligations against exact source bytes. Real extraction is byte-complete,
non-bootstrap pending counts are nonzero, and the two decisions remain
unresolved in production records. Capture:

```bash
timeout 2m go run ./cmd/wr-nextflow-conformance validate
timeout 2m go run ./cmd/wr-nextflow-conformance extract --check
timeout 10m env CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -run '^TestUAT_(A2|B1|B2)_[0-9]+$'
timeout 10m golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

Validation claims record validity only. Runtime scope remains incomplete;
phase 3 supplies executable expectations and bindings. Execution evidence,
freshness checks, and real oracle observations follow in phases 4 and 5.
