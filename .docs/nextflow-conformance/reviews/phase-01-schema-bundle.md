# Phase 1 schema and CLI input bundle review

Verdict: APPROVED for Item 1.1 implementation continuation. This approves
input size and scope only. Code correctness, acquisition, and phase
completion require their separate reviews. Reviewed on 2026-09-28 against
the amended phase plan and the current partial implementation.

## Approved inputs

Paths below are relative to `/home/ubuntu/wr`. Line spans refer to the
current files; section and function names identify them after edits.

- `.docs/nextflow-conformance/phase1.md`, complete.
- `.docs/nextflow-conformance/spec.md`, lines 23-366: Architecture,
  Records and schema, Commands and result contract, and A1 boundaries.
  Lines 699-758: D2 attempt inputs and deferred execution obligations.
  Lines 1034-1083: Implementation Order and bounded validation commands.
- `.docs/nextflow-conformance/evidence/phase1.md`, complete. Its historical
  acquisition assumptions are superseded by the amended spec and plan.
- `conformance/model.go`, `conformance/schema.go`, `conformance/cli.go`,
  and `conformance/model_test.go`, complete.
- All eleven fixtures in `conformance/testdata/records/`: `target.json`,
  `sources.lock.json`, `blocks.json`, `obligations.json`,
  `requirements.json`, `uats.json`, `reviews.json`, `decisions.json`,
  `bindings.json`, `batches.json`, and `attempts.json`.
- `cmd/wr-conformance/main.go` and `cmd/wr-testsuite/main.go`, complete.
  Production `main.go`, lines 105-129, for the dependency boundary only.
- `conformance/source.go`, lines 56-177: pinned constants,
  `acquisitionOptions`, `acquire`, `readLock`, `offlinePreflight`, and
  `failure`. Lines 1177-1185: `sourceError`. Read further caller spans only
  when required to preserve a changed record interface; budget those reads
  as tool output and leave acquisition implementation to Item 1.2.
- `go.mod` and `.golangci.yml`, complete, for toolchain and quality gates.
- `.tmp/agent/conformance/lint-go126-current.log`: diagnostic headers for
  the four Item 1.1 Go files above; aggregate the remaining findings by file.
- The eleven matching `conformance/data/schema/<kind>.v1.json` files are
  approved generated artifacts. Read only properties or conditions under
  repair, with at most 4,000 tokens of excerpts across the handoff. Compare
  and validate the complete files with bounded commands rather than loading
  every generated document into context.

Read the implementation and review skills required by the phase plan.
This bundle review used `go-reviewer` and its referenced skills, plus
`writing-for-agents`, `prose-principles`, and `unslop`, from
`/home/ubuntu/.agents/skills/`. The implementation handoff also reads
`/home/ubuntu/.agents/skills/go-implementor/SKILL.md`.

## Budget

The primary files and source/spec spans total about 122,000 characters,
excluding skills, generated schemas, and lint excerpts. Three characters
per token gives about 41,000 input tokens. The generated schemas alone add
92,662 bytes, so a complete schema dump is outside this approved bundle.

Planning allowances are 55,000 tokens for primary inputs, skills, and the
brief; 8,000 for bounded tool output and supplemental reads; 15,000 for
implementation and review output; and 17,000 for reasoning. The estimated
total is 95,000, leaving 5,000 below the roughly 100,000-token limit. These
are planning estimates, not measured model usage. Reserve half the tool
output allowance for schema excerpts and half for diagnostics and tests.

No further split is required now. If implementation growth exceeds these
allowances, request approval for two sequential acceptance boundaries:
closed records plus schema agreement, then corpus loading plus CLI contract.
Each needs its own independent review; neither completes acquisition or
awards an A1 acceptance ID separately from Item 1.2.

## Scope and handoff priority

Item 1.1 owns all eleven closed record shapes, nested and typed reference
constraints, schema emission and independent agreement fixtures, required
target loading, attempt-file loading, CLI arguments, counts, cancellation,
JSON diagnostics, and exit handling. Tests belong in `model_test.go`, with
entry-point tests beside the entry point when needed. Later semantic
coverage, discovery, execution, evidence derivation, and freshness gates
remain with their assigned stories.

Start with failing malformed-record and corpus tests for the amended
artifact contract. `model.go`'s `artefact` lacks required `packaging` and
nullable `coordinate`, permits obsolete POM/source-JAR roles, and retains
the old coordinate-to-JAR assumptions. Add the exact pinned opaque runtime,
dependency-metadata POM constraints, actual dependency references, cycle
checks, and runtime-closure constraints. Keep physical acquisition and
runtime byte verification with Item 1.2.

Then close the documented schema/decoder disagreement, wrong-type and
missing references, target identity and uniqueness, nested attempt loading,
command-specific flags, validation counts, and cancellation gaps. Generated
byte equality alone does not prove schema agreement. Preserve valid fixtures
for later record kinds without claiming their later semantic gates passed.

The recorded 148 lint findings have distinct owners:

- Item 1.1 owns 83: `model.go` 38, `schema.go` 24, `cli.go` 14, and
  `model_test.go` 7. These include complexity, exhaustive switches, repeated
  constants, line length, shadowing, spelling, and whitespace findings.
  Preserve the specified JSON wire spelling when fixing UK spelling lint.
- Item 1.2 owns 65: `source.go` 53 and `source_test.go` 12, including the
  acquisition security and path findings. Report these as outstanding
  acquisition work; their deferral does not make the package lint gate pass.

These counts come from the saved lint log, whose line numbers predate some
current code. Rerun lint after changes and report findings by current file.

## Required continuation evidence

Capture meaningful red and green commands for the schema/CLI gaps, emitted
JSON and stderr, exit codes, and relevant artifact paths. Run from the repo
root with bounded commands. Tests and builds use Go 1.27.1. The same rebuilt
golangci-lint v2.12.2 uses Go 1.26.3 because its analyzer panics on Go 1.27
standard-library syntax. Keep analyzers enabled and `go.mod` unchanged.

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance
timeout 10m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

No tests or linter were rerun for this input-size review. Existing fixture
passes and the saved lint ledger establish continuation context only. All
seven A1 UATs, real acquisition, measured hashes, and candidate-lock review
remain Item 1.2 work after Item 1.1 passes independent code review.
