# Item 1.1 placeholder whitespace correction

Status: IMPLEMENTED, awaiting fresh independent review. This corrects the
single P2 finding in
[review 02](../reviews/phase-01-schema-review-02.md). It does not award an
Item 1.1, package, A1, or phase pass. Work used the approved schema input
bundle with the [Nextflow path mapping](nextflow-naming.md).

## Change and contract

The Go decoder and emitted description expressions use the same explicit
Unicode White_Space ranges. They match the existing Go `strings.TrimSpace`
contract: U+0009 through U+000D, U+0020, U+0085, U+00A0, U+1680, U+2000
through U+200A, U+2028, U+2029, U+202F, U+205F, and U+3000. Leading padding,
sentence boundaries, sentence endings, blank descriptions, and padded
single-word placeholders use that contract. The archived backticked heading
prefix still rejects.

Explicit ranges avoid different meanings of `\s` in Go, Python, and
ECMAScript. U+001C through U+001F, U+180E, U+200B, and U+FEFF are outside
Unicode White_Space and retain Go's previous treatment as ordinary text.
Quoted literals and meaningful padded descriptions remain allowed. The
literal sentence's internal spaces and punctuation are unchanged; this
regression guard does not classify arbitrary prose.

The adjacent case-insensitive check also had an engine disagreement: Go
rejected the archived sentence with U+017F in `describe`, but the emitted
schema accepted it. The shared class generator now includes Unicode simple
folds, preserving Go's previous case-insensitive behavior in stock schemas.
Quoted versions still pass. No other scalar rules changed.

The original 138 independent mutations are unchanged. Added 209 whitespace
cases cover all 25 whitespace characters, nearby non-whitespace characters,
quoted descriptions, and the archived prefix. Four additional casefold
cases bring the total to 351. The existing Go mutation test and independent
Python checker consume these cases; neither checker was weakened. All eleven
schemas were regenerated, with eight files changed by the description rule.

## Red evidence

Logs and matching `.exit` files are under
`.tmp/agent/nextflow-conformance/fix02/`. Before implementation changes:

- `red-go`: exit 1; 74 added mutation assertions failed in the decoder.
- `red-stock`: exit 1; the stock validator accepted the leading-tab sentence.
- `red-build`: exit 0 with Go 1.27.1.
- `red-cli`: exit 1; 90 new cases disagreed with the expected boundary.
  Schema acceptance was wrong in 43 cases and CLI acceptance in 74, with
  overlap. All fifteen inherited probes passed.

The exact leading-space, leading-tab, leading-NBSP, and post-sentence-NBSP
inputs from review 02 are asserted by the new probe. Accepted invalid input
reached `E_TREE_INCOMPLETE`; that exit 2 was not an input rejection. Each
invalid description must instead produce `E_INPUT`.

After the whitespace correction, before the casefold correction:

- `casefold-red-go`: exit 0; Go already rejected the two long-s sentences.
- `casefold-red-stock` and `casefold-red-cli`: exit 1; schemas accepted both.

`red-probes/` and `casefold-red-probes/` retain input corpora, stdout, stderr,
exit files, expected validity, observed diagnostics, and failure lists.
These commands produced the original red results before the corresponding
implementation changes:

```bash
timeout 3m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance -run '^TestIndependentSchemaCases$'
timeout 15s python3 nextflowconformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-fix02 ./cmd/wr-nextflow-conformance
timeout 60s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-02-probes.txt
```

To reproduce the original defect without changing the fixed working files,
use the preserved original-source overlay and original-schema snapshot.
Their SHA-256 values match the baseline captured before this fix. These
commands were also rerun against the final 351 mutations, both with exit 1:

```bash
timeout 3m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -overlay .tmp/agent/nextflow-conformance/fix02/red-overlay.json -tags netgo -count=1 -v ./nextflowconformance -run '^TestIndependentSchemaCases$'
timeout 15s python3 .tmp/agent/nextflow-conformance/fix02/red-stock-reproduction/testdata/check_schemas.py
```

## Final validation

Run from `/home/ubuntu/wr`. Tests and builds use Go 1.27.1. The existing
v2.12.2 linter runs under Go 1.26.3 with all configured analyzers enabled.
`cleanorder -min-diff` ran on both edited Go files.

```bash
timeout 3m env GOTOOLCHAIN=go1.27.1 NEXTFLOW_CONFORMANCE_UPDATE_SCHEMAS=1 CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance -run '^TestSchemaEmission$'
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 15s python3 nextflowconformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-fix02 ./cmd/wr-nextflow-conformance
timeout 60s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-02-probes.txt
timeout 15s node .docs/nextflow-conformance/evidence/phase1-schema-fix-02-ecmascript.txt
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

- `owned`: exit 0; all sixteen Item 1.1 test functions pass.
- `green-stock`: exit 0; eleven schemas and 351 mutations pass stock
  Draft 2020-12 validation without a format-checking plugin.
- `green-build`: exit 0.
- `green-cli`: exit 0; fifteen inherited and 213 new compiled CLI probes
  meet their assertions. All 228 outputs contain one JSON result, all sixteen
  explicit counts, zero verified artifacts, `complete:false`, required
  diagnostic fields, nonempty stderr, and the expected exit 2. Valid records
  reach the baseline offline tree failure, not successful acquisition.
- `ecmascript`: exit 0; all 216 description cases agree with native
  ECMAScript regex evaluation of the emitted constraints. This is a regex
  portability check, not a second full schema-engine run.
- `full`: exit 1; exactly `TestUAT_A1_01` through `TestUAT_A1_05` fail.
  Their deferred acquisition fixtures still lack required packaging.
- `lint`: exit 1; exactly 61 deferred findings, 50 in `source.go` and 11
  in `source_test.go`. Item 1.1 files and the entry point have zero findings.
  This is not a package lint pass.

The [manifest](phase1-schema-fix-02-manifest.json) records command outcomes,
input and output hashes, and unchanged historical evidence. Acquisition,
production code, module files, linter configuration, the spec, checkboxes,
and historical probes were not changed by this fix. No commits or pushes
were made. Edits stopped for fresh independent review.
