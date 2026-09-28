# Nextflow naming migration

Status: IMPLEMENTED, awaiting fresh independent review. The user requested
explicit Nextflow names for this developer tool on 2026-09-28. The earlier
schema review was interrupted; no review pass is inferred from it. Item 1.2
acquisition failures and lint findings remain outstanding.

## Name and path mapping

- Go package and directory: `conformance` -> `nextflowconformance`.
- Developer executable: `cmd/wr-conformance/` ->
  `cmd/wr-nextflow-conformance/`; CLI name is `wr-nextflow-conformance`.
- Default corpus: `conformance/data` -> `nextflowconformance/data`.
- Default cache: `.tmp/conformance` -> `.tmp/nextflow-conformance`.
- Schema identity prefix: `urn:wr:conformance:schema:1:` ->
  `urn:wr:nextflow-conformance:schema:1:`.
- Schema regeneration switch: `CONFORMANCE_UPDATE_SCHEMAS` ->
  `NEXTFLOW_CONFORMANCE_UPDATE_SCHEMAS`.
- Documented Java variable: `CONFORMANCE_JAVA_HOME` ->
  `NEXTFLOW_CONFORMANCE_JAVA_HOME`.
- New logs and probe output: `.tmp/agent/conformance/` ->
  `.tmp/agent/nextflow-conformance/`. Existing logs stay at their old paths.

Imports, package declarations, the binding fixture, generated schema IDs,
active specification, all six phase plans, and prompt requirements use the
new names. Acceptance IDs, record contracts, all 138 independent schema
mutations, and acquisition behaviour are unchanged. The schema documents
still match emission from the shared definitions.

No new name-only tests were added. Existing behavioural tests cover the
same supported operations. Every migrated package, CLI, schema, and fixture
file reproduces its pre-migration SHA-256 after reversing the name changes.
The active spec and phase plans differ only in names and whitespace.
`cleanorder -min-diff` ran separately on each of the seven Go files. Its
second pass restored the original adjacent declaration order in `model.go`;
the final file has only its package-name change.

## Historical evidence

All pre-existing files in `reviews/` and `evidence/` remain byte-identical.
Their hashes and old paths describe their original snapshots. The existing
blocker report also retains its historical log references. Apply the mapping
above when reading the
[approved input bundle](../reviews/phase-01-schema-bundle.md); it approves
scope and input size, not correctness of the renamed implementation.

The historical
[assertion-bearing probe](phase1-schema-fix-01-probes.txt) is unchanged.
Its runnable successor is
[nextflow-naming-probes.txt](nextflow-naming-probes.txt).
Only package, binary, and output paths changed. It preserves all fifteen
public CLI probes and their assertions. Outputs are under
`.tmp/agent/nextflow-conformance/naming-probes/`, including input corpora,
stdout, stderr, exit codes, and `results.json`.

The [migration manifest](nextflow-naming-manifest.json) records pre-migration
hashes and current hashes under their respective paths. Historical manifests
are not rewritten to imply that old reviews inspected the current names.

## Validation

Run from the repository root. Tests and the CLI build use Go 1.27.1. Lint
uses the existing golangci-lint v2.12.2 binary and the approved Go 1.26.3
analyzer workaround; no analyzers or configuration changed.

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 10s python3 nextflowconformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-naming ./cmd/wr-nextflow-conformance
timeout 30s python3 .docs/nextflow-conformance/evidence/nextflow-naming-probes.txt
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

Logs and matching `.exit` files are in
`.tmp/agent/nextflow-conformance/`:

- `naming-owned-tests`: exit 0, all sixteen schema/CLI test functions pass.
  The developer entry point builds and has no separate test files.
- `naming-independent-schema`: exit 0, eleven schemas and 138 independently
  authored mutations pass with stock Draft 2020-12 validation.
- `naming-build`: exit 0, the renamed developer CLI builds.
- `naming-cli`: exit 0, all fifteen assertion-bearing probes pass. Each CLI
  process returns the expected exit 2 for malformed or incomplete fixtures.
  Valid baseline and acquired-source-span fixtures reach offline
  `E_TREE_INCOMPLETE`; these are not successful acquisition claims.
- `naming-full-tests`: exit 1, exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` fail. Their acquisition fixtures still lack required
  packaging and fail decoding before their intended acquisition assertions.
  A1_06/A1_07 and real acquisition evidence remain Item 1.2 work.
- `naming-lint`: exit 1, exactly 61 findings: 50 in
  `nextflowconformance/source.go` and 11 in
  `nextflowconformance/source_test.go`. See `naming-lint-ownership.json`.
  Item 1.1 files and the entry point have zero findings. This is not a
  package-wide lint pass.
- `naming-cleanorder`: exit 0. All seven edited Go files were processed.

Production wr code, module files, linter configuration, historical archive,
and phase checkboxes were not changed. Full unrelated wr tests were not run.
No commits or pushes were made. Edits stopped for fresh independent review.
