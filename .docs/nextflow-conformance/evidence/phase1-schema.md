# Item 1.1 schema and CLI implementation evidence

Status: IMPLEMENTED, awaiting independent review. Item 1.1 tests pass and
its files have no lint findings. The full package tests and lint still fail
on Item 1.2 acquisition work. No A1 acceptance completion is claimed.

## Review inputs and changes

Use the accepted [schema bundle](../reviews/phase-01-schema-bundle.md),
[phase plan](../phase1.md), and [specification](../spec.md). The four owned
Go files are `conformance/model.go`, `schema.go`, `cli.go`, and
`model_test.go`. Together they are about 102 kB. Read emitted schemas only
for the property under review; run the complete comparison command below.

The implementation adds required packaging and nullable Maven coordinates,
the exact opaque runtime identity, metadata-only POMs, actual dependency
references, cycle detection, and runtime closure membership. Environment
coordinate records now allow a null `artifact_id`; a POM does not invent an
external execution JAR. The two acquisition files only received interface
migration, ordering, and formatter changes. Their acquisition algorithms
still need Item 1.2 work.

Corpus loading requires `target.json`, checks unique target and lock types
against the pinned version/tag/commit/tree, loads individual
`attempts/<ID>.json` objects, and verifies filenames against attempt IDs.
Typed references cover profiles, cases, facets, includes, reviews, decisions,
bindings, batches, and attempt results. Source spans and reviewed source
file references bind locked paths, hashes, and bounds. Reviewed sources may
be source-tree files or acquired source artifacts. Accepted reviews and
resolved exclusion decisions are checked where the record contract requires
approval. Later coverage, discovery, execution, freshness, and evidence
state derivation remain assigned to their later stories.

The CLI rejects flags outside their commands, requires applicable suites or
case IDs, preserves the requested verify claim on failure, uses null for an
inapplicable suite, reports record counts, and checks cancellation before
execution, between corpus reads, and around offline verification. A failed
acquisition keeps zero verified counts. The entry point already forwards
SIGINT and SIGTERM through its context; no production wr entry point changed.

## Standard schema and semantic boundary

Stock JSON Schema enforces closed objects, required and nullable fields,
scalar constraints, integer bounds, conditional readiness and decisions,
packaging, the pinned runtime hash and byte count, and nested observation
and batch shapes. It cannot enforce file encoding, final newlines,
duplicate lexical JSON keys, sorted IDs, sibling span comparisons,
cross-record reference types, or dependency graph properties.

The emitted `x-wr-*` annotations identify those additional decoder and corpus
rules. They are metadata, not enforcement. Go validators enforce the rules;
Go tests exercise their rejections separately. No stock-validator claim is
made for cross-record or relational checks. JSON Schema also treats an
integral JSON number as an integer irrespective of its lexical spelling;
the Go integer decoder accepts integer lexical forms without float
conversion. That decoder constraint is recorded in the emitted annotations.

Independent fixtures are the eleven files in
`conformance/testdata/records/`, two ready/resolved fixtures in
`conformance/testdata/schema-records/`, and 49 authored mutations in
`conformance/testdata/schema-cases.json`. The Go decoder and the installed
Python `jsonschema` 4.10.3 validator produce the expected result for every
mutation. `conformance/testdata/check_schemas.py` runs the independent
standard-schema check. Python and jsonschema are evidence prerequisites;
they are not dependencies of production code or the Go test suite.

The opaque runtime fixture contains the required metadata only. No fixture
creates substitute runtime bytes or establishes acquisition success.

## Red and green evidence

All log names below are relative to `.tmp/agent/conformance/`. Red commands
used Go 1.27.1, `CGO_ENABLED=1`, `-tags netgo`, and `-count=1`, bounded by
`timeout 2m`.

- `schema-contract-red.log`: the amended lock was rejected for its new
  fields; unsupported validation flags were accepted; cancellation was
  ignored. Tests were `TestAmendedArtefactContract` and
  `TestCLIInvocationContract`. `schema-contract-green.log` records their
  first passing run alongside existing record tests.
- `schema-corpus-red.log`: a nonempty corpus without a target reached
  reference checking, and a decision ID satisfied a block reference.
  `TestCorpusContract` now passes in `schema-owned-tests.log`.
- `schema-closure-red.log`: a disconnected external JAR was accepted and a
  valid relative include was rejected. `schema-closure-green.log` records
  `TestRuntimeClosureMembership` passing.
- `schema-result-red.log`: failed verify output lost its requested claim.
  `schema-result-green.log` records `TestFailedCommandResultContract`
  passing, including null suite output for validation.
- `schema-source-reference-red.log`: a review could name a missing source
  file. `schema-source-reference-green.log` records
  `TestReviewSourceFileReferences` passing for missing paths and changed
  byte counts. `schema-external-source-red.log` records rejection of a valid
  acquired Maven source artifact; `schema-external-source-green.log` records
  the corrected source lookup passing.

`schema-contract-first-green.log` is an intermediate FAILED run despite its
filename. It exposed a reflection panic while inspecting a string `File`
field. The code now checks the field kind. Its filename is not pass evidence.
The final logs below supersede all intermediate runs.

## Final checks

Run these commands from `/home/ubuntu/wr`:

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 10s python3 conformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-conformance ./cmd/wr-conformance
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

Observed results:

- `schema-owned-tests.log` and `.exit`: all sixteen Item 1.1 tests pass,
  exit 0. The entry point package builds and has no separate tests.
- `schema-independent-python.log` and `.exit`: eleven schemas and 49
  independent mutations pass, exit 0.
- `schema-build.log` and `.exit`: the Go 1.27.1 CLI build passes, exit 0.
- `schema-full-tests.log` and `.exit`: exit 1. Exactly `TestUAT_A1_01`
  through `TestUAT_A1_05` fail. Their old acquisition fixture constructors
  leave packaging empty, so decoding fails before the expected fetch or
  archive operation. They also retain the obsolete closure assumptions.
  Item 1.2 must migrate those fixtures without weakening pinned identity.
- `schema-lint-final.log` and `.exit`: exit 1, with 61 acquisition findings.
  `schema-lint-ownership.json` assigns 50 to `source.go` and 11 to
  `source_test.go`. Item 1.1 files and the entry point have zero findings.
  No full-package lint pass is claimed. All analyzers remain enabled.
- `schema-cleanorder.log`: ordering ran on every edited Go file.
  `schema-emission.log`: regeneration and byte comparison passed.

The linter is the existing repository-local v2.12.2 binary, built with
Go 1.27.1. Its run uses the approved Go 1.26.3 analyzer toolchain because
analysis of Go 1.27 standard-library syntax previously panicked. `go.mod`,
`go.sum`, and `.golangci.yml` were not changed.

## Compiled CLI artifacts

The compiled executable was run for empty and malformed corpora, unsupported
flags, an unknown command, deferred verify, and acquisition with a malformed
lock. The acquisition case fails before requests. Each returns process exit
2, one JSON result, `complete:false`, diagnostics on stderr, and zero
verified artifacts. Files use these prefixes under
`.tmp/agent/conformance/`, each with `.json`, `.stderr`, and `.exit`:

- `schema-cli-empty`
- `schema-cli-malformed`
- `schema-cli-flags`
- `schema-cli-unknown`
- `schema-cli-verify`
- `schema-cli-acquire-failure`

Empty validation reports `E_CORPUS_EMPTY` and claim `records-valid`.
Unsupported flags report `E_INVOCATION`. Deferred verify reports
`E_PREREQUISITE` with claim `foundation-bootstrap`. Failed acquisition
reports claim `inputs-acquired` and zero counts.

A successful `validate` over a real acquired cache remains Item 1.2 evidence.
No networking, runtime conversion, Java installation, oracle execution,
phase checkbox changes, commits, or pushes were performed for this handoff.
