# Item 1.1 quoted-description correction

Status: IMPLEMENTED, awaiting fresh independent review. This fixes the P2
quoted-description finding in
[review 03](../reviews/phase-01-schema-review-03.md). It awards no Item 1.1,
package, A1, or phase pass. The approved schema bundle and current Nextflow
path mapping apply.

## Change and contract

The decoder masks complete quoted spans before checking for a stand-in
sentence. The schema walks those same spans and uses a negative lookahead
to prevent backtracking into their contents. The shared span expression
recognizes paired double quotes, backticks, and single quotes, with
backslash escapes. Single quotes open at the start of text or after
whitespace or ASCII punctuation other than another quote or backslash;
apostrophes in contractions remain ordinary prose. Unmatched delimiters
grant no exemption. This is a literal-span guard, not a Markdown parser.

The decoder retains a quoted span's preceding boundary and a backtick's
closing boundary. This preserves rejection of an adjacent outside stand-in
and the archived backticked heading form. Padding and multiple sentences
inside quoted fixtures no longer cause rejection. Both paths still use the
shared Unicode White_Space ranges and explicit Unicode simple-fold classes.
No spec scope or scalar rule outside the description guard changed.

The original 351 mutations retain their values and serialized bytes. Added
188 independent cases cover three quote delimiters, ASCII and Unicode
padding, multiple sentences, simple folds, quoted placeholder words,
contractions, nested other delimiters, escaped opening and closing quotes,
odd and even backslashes, unmatched delimiters, and adjacent outside
controls. The [case generator](phase1-schema-fix-03-cases.txt) records the
expected outcomes independently of the guard. The existing Go mutation test
and stock schema checker consume all 539 cases.

Eight regenerated schemas differ only in description pattern values. The
other three schemas, entry point, Go tests, acquisition implementation,
module files, linter configuration, spec, phase checkboxes, and historical
evidence retain their baseline hashes.

## Red evidence

Logs, exit files, input corpora, source snapshots, and results are under
`.tmp/agent/nextflow-conformance/fix03/`. The
[manifest](phase1-schema-fix-03-manifest.json) records exact bounded commands
and hashes.

- `red-six` exits 1 with exactly three quote-exemption failures among the
  six review cases. The unquoted padded sentence, unpadded quoted sentence,
  and archived heading controls pass.
- `red-go`, `red-stock`, and `red-cli` exit 1 before the main change.
  The decoder and compiled CLI each disagree with 46 new expected outcomes.
- `red-boundary-go` exits 1 with three adjacent single-quote controls. It
  uses the preserved intermediate source to reproduce the need to retain
  the outside whitespace when masking a single-quoted span.
- `red-unmatched-stock` exits 1 for an unmatched backtick immediately before
  the stand-in. Three added controls preserve the existing rejection while
  keeping paired backticks exempt.
- `replay-red-go` and `replay-red-stock` exit 1 against the final mutations.
  The original-source overlay and all eleven restored original schemas
  match the captured pre-fix hashes. `replay-red-six` independently repeats
  the original three failures with a rebuilt baseline binary.

These commands reproduce the original failures without changing the fixed
working files:

```bash
timeout 3m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -overlay .tmp/agent/nextflow-conformance/fix03/red-overlay.json -tags netgo -count=1 -v ./nextflowconformance -run '^TestIndependentSchemaCases$'
timeout 30s python3 .tmp/agent/nextflow-conformance/fix03/red-stock-reproduction/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -overlay .tmp/agent/nextflow-conformance/fix03/red-overlay.json -o .tmp/agent/bin/wr-nextflow-conformance-fix03-red ./cmd/wr-nextflow-conformance
timeout 30s python3 .tmp/agent/nextflow-conformance/fix03/replay-red-six.py
```

## Validation

All commands run from `/home/ubuntu/wr`. Tests and both builds use Go 1.27.1.
The existing golangci-lint v2.12.2 runs under Go 1.26.3 with unchanged
analyzers. `cleanorder -min-diff` ran on both edited Go files.

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 30s python3 nextflowconformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-fix03 ./cmd/wr-nextflow-conformance
timeout 60s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-03-probes.txt
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-03-review-regressions.txt
timeout 15s node .docs/nextflow-conformance/evidence/phase1-schema-fix-02-ecmascript.txt
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

- All sixteen owned Go tests pass. The normal schema-emission test confirms
  all eleven files match their generated contents.
- Stock Draft 2020-12 validation passes all eleven schemas and 539 mutations
  without a format-checking plugin.
- All 416 compiled CLI assertions pass: 228 inherited checks and 188 new
  quote cases. Each output satisfies the JSON result contract, including
  all sixteen counts, `complete:false`, zero verified artifacts, required
  diagnostic fields, stderr, and exit 2.
- All six exact review regressions pass independently in the compiled CLI,
  stock schema validator, and native ECMAScript regex evaluator.
- All 404 description cases pass native ECMAScript regex checks. This
  proves regex portability, not a second full schema engine.
- The full focused suite exits 1 with exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` failing. Their deferred acquisition fixtures lack required
  packaging. Acquisition and the new A1_06/A1_07 tests remain Item 1.2 work.
- Lint exits 1 with exactly 61 deferred findings: 50 in `source.go` and 11
  in `source_test.go`. Owned files have zero findings. This is not a package
  lint pass; no analyzers were disabled or findings suppressed.

The new [CLI probe](phase1-schema-fix-03-probes.txt) and
[six-case probe](phase1-schema-fix-03-review-regressions.txt) invoke the current
`wr-nextflow-conformance-fix03` binary. They assert `E_INPUT` for invalid
descriptions and `E_TREE_INCOMPLETE` for accepted records; exit 2 alone never
counts as successful validation. These offline fixture passes do not prove
acquisition success.

No commits or pushes were made. Edits stopped for fresh independent review.
