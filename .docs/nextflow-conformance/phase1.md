# Phase 1: Implement A1 and closed schema validation

Ref: [spec.md](spec.md) sections A1

## Instructions

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

### Item 1.1: A1 - Verify every acquired input

spec.md section: A1; Architecture, Records and schema, and Commands and
result contract supply the shared validation and CLI requirements.

Write fetch-all-failed, empty-corpus, and malformed-record failures first.
Then implement the closed record types and schema emission in
`conformance/model.go`, acquisition in `conformance/source.go`, and the
`Run(ctx, args, stdout, stderr) int` entry point. Wire cancellation and exit
handling through `cmd/wr-conformance/main.go`, following `cmd/wr-testsuite`.
Keep production wr packages independent of the new developer tool.

Implement all eleven record schemas and their nested field constraints
from Records and schema, including D2's attempt input fields. Validate
required fields, duplicate keys, UTF-8, IDs, hashes, and references without
float conversion or a generic schema framework. Emit matching versioned
schemas in `conformance/data/schema/` from the same definitions. Use valid
fixtures for record types whose real ledger data arrives in later phases;
semantic coverage and execution gates remain with their assigned stories.

Cover the five A1 acceptance tests in `conformance/source_test.go`:
`A1_01`, `A1_02`,
`A1_03`, `A1_04`, and `A1_05`. Shared schema tests belong in
`conformance/model_test.go`; later stories retain their acceptance IDs.

Prove transactional acquisition, immutable Git tree accounting, archive
and path safety, bounded requests, and offline rehashing with independent
HTTPS fixtures. Acquire actual pinned source, launcher, distribution,
dependency closure, and an existing Java 21 tree. Review their measured
hashes and the exact A2 bootstrap selectors before accepting the candidate
lock. Only acquisition may fetch. Preserve previous locks and cache after
failure, and fail explicitly when Java or an artifact is unavailable.

- [ ] implemented
- [ ] reviewed

## Exit conditions

All five A1 UATs and closed-schema checks pass, including round trips,
malformed records, and agreement between emitted schemas and decoding.
The reviewer checks the actual acquired bytes, recorded identities, and
candidate lock against the spec before it becomes a reviewed input. The
CLI emits the required JSON and exit contract, including zero counts on
failed acquisition. Capture:

```bash
timeout 16m go run ./cmd/wr-conformance acquire --java-home "$CONFORMANCE_JAVA_HOME"
timeout 10m env CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance
timeout 10m golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

Set `CONFORMANCE_JAVA_HOME` to the existing verified Java 21 installation.
Check offline preflight with acquisition network fixtures stopped. Full
semantic validity and bootstrap completion await the later phases.
