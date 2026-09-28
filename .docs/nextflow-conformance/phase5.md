# Phase 5: Implement E1, then E2

Ref: [spec.md](spec.md) sections E1, E2

## Instructions

Begin after [phase4.md](phase4.md) is implemented and reviewed.

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

### Item 5.1: E1 - Run real pinned oracle cases with honest claim boundaries

spec.md section: E1

Implement `conformance/oracle.go` and the seven actual workflow/config
cases under `conformance/data/cases/`. Cover all four acceptance tests in
`conformance/oracle_test.go`: `E1_01`, `E1_02`, `E1_03`, and `E1_04`.
Execute the unchanged opaque Nextflow 26.04.6 distribution by absolute path
through its embedded `NXF_PACK=dist` launcher, which passes that same file to
Java 21. Keep the separately acquired launcher as provenance; its default
`one` package download path is outside this execution contract. Use parser
v2, local executor, two-task concurrency, static typing disabled, no plugins,
disabled automatic updates, isolated work and empty user home directories,
and a private Nextflow home. Enforce network denial and record its mechanism.
Independently review each workflow, config, and expectation before
execution. Record verified
source and full-file distribution hashes, version output, argv, effective
environment, trace, stdout, stderr, and produced files. Use only the acquired
external execution closure. POM metadata and bundled classes do not create
additional runtime files or prove a complete Maven inventory. Successful
offline runs prove closure sufficiency for these seven cases only. A missing
external prerequisite fails the UAT and blocks this item without a skip or
fixture substitute.

Run `ORACLE_MAP`, `ORACLE_MAP_NULL`, `ORACLE_MIX`, `ORACLE_EMPTY`,
`ORACLE_IMPORT`, `ORACLE_FAIR`, and `ORACLE_FILE_ERROR`. Independently
review exact import and missing-output diagnostics from actual runs before
accepting those contracts. Enforce zero value lines on both error cases
and task script exit 0 on the missing-output case.

Prove fair completion order B,A through trace-driven synchronization while
downstream emission remains A,B with exact files. Retain raw observations
on contradictions. The contradictory map expectation must fail
`E_EXPECTATION` and generate an unresolved disagreement candidate without
approving it or altering expected data. Keep oracle, foundation, and runtime
evidence distinct; report adapter absence and zero wr runtime passes on the
restored tree.

- [ ] implemented
- [ ] reviewed

### Item 5.2: E2 - Detect every deliberate corruption for its intended reason

spec.md section: E2

After Item 5.1 review, implement mutation accounting in
`conformance/coverage.go` and reviewed fixtures in `conformance/testdata/`.
Cover all three acceptance tests in `conformance/adversarial_test.go`:
`E2_01`, `E2_02`, and `E2_03`. Execute every named mutation from E2's
18-row manifest against an independent known-valid fixture copy. The clean
baseline must pass; the specific expected exit and diagnostic must kill
each mutation. Unrelated errors and unchanged inputs remain invalid or
surviving controls. For `E2_02`, make one control invalid or surviving and
require foundation verification to return 1 with `E_MUTATION_NOT_KILLED`.
Keep mutation results as foundation evidence.

Also prove the three semantic observer mutations for null retention,
multiset deduplication, and sorted fair output fail `E_EXPECTATION`.
Replay raw evidence and reject normalization changes that conceal them.
Label these as faulty fixture subjects, preserving later wr implementation
mutation work as outstanding.

- [ ] implemented
- [ ] reviewed

## Exit conditions

All seven E UATs pass. Evidence includes seven actual offline oracle runs,
18 killed accounting mutations with no invalid or surviving controls, and
three detected semantic observer mutations. Independent review accepts
actual diagnostic literals and raw fair-order evidence. Capture:

```bash
timeout 20m env CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance -run '^TestUAT_E[12]_[0-9]+$'
timeout 2m go run ./cmd/wr-conformance verify --suite wr-runtime
timeout 10m golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

The runtime verification must return 1 with `E_ADAPTER_UNAVAILABLE` and zero
wr runtime passes. Capture that expected nonzero result without suppressing
it. Full bootstrap completion still requires the F stories and final gate.
