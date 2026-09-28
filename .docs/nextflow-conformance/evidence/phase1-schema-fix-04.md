# Item 1.1 opaque-span correction

Status: IMPLEMENTED, awaiting fresh independent review. This fixes the P2
literal-masking finding in
[review 04](../reviews/phase-01-schema-review-04.md). It awards no Item 1.1,
package, A1, or phase pass. The approved bounded schema bundle and Nextflow
path mapping apply.

## Change and contract

The decoder replaces quoted spans and backslash escapes with NUL instead
of the letter `x`. NUL cannot form a word in the guarded sentence or match
its whitespace boundaries. The guard still preserves the preceding boundary
of a single-quoted span and the closing backtick of an archived heading.
The production diff changes two replacements and their explanatory comment
in `model.go`. Schema generation and all eleven emitted schemas are unchanged.

The [case generator](phase1-schema-fix-04-cases.txt) adds 651 independently
specified mutations. The finite families are:

- Replace each of the sentence's 29 letters with a double-quoted span,
  backtick span, single-quoted text, or backslash escape. Include an intact
  outside sentence before and after each input: 348 cases.
- Insert the same four forms at each of 25 positions inside words, with
  the same adjacent outside controls: 300 cases.
- Preserve the three exact reported descriptions as regressions: 3 cases.

These expectations include 219 accepted descriptions and 432 rejected
outside-sentence controls. Single quotes inside words retain their existing
contraction-boundary interpretation. No broader parser was introduced.
All original 539 mutations retain their values and serialized prefix.

The [public CLI probe](phase1-schema-fix-04-probes.txt) combines the review's
93 cases with the 651 additions. It asserts the independently specified
outcome in the compiled CLI, stock Draft 2020-12 validator, and native
ECMAScript expressions. Accepted descriptions must reach `E_TREE_INCOMPLETE`;
rejected descriptions must produce `E_INPUT`. Exit 2 alone never counts as
successful description validation. These fixture results do not prove
acquisition success.

## Red evidence and replay

All logs, exits, input corpora, stdout, stderr, source snapshots, source diff,
and the original-source overlay are under
`.tmp/agent/nextflow-conformance/fix04/`. The
[manifest](phase1-schema-fix-04-manifest.json) records exact commands and
hashes. Retained artifact-tree digests cover the complete probe corpora.

Before production edits, `red-review04` exits 1 with exactly the three
reported disagreements. `red-go` exits 1 with five added decoder failures.
`red-new-probes` exits 1 with eight failures: the three original review
cases and five added cases, including the three repeated exact regressions.
All stock-schema and ECMAScript expectations pass in that red probe.

After correction, the original-source overlay reproduces the same five
Go failures and eight public probe failures. These commands leave the fixed
working files intact:

```bash
timeout 3m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -overlay .tmp/agent/nextflow-conformance/fix04/red-overlay.json -tags netgo -count=1 -v ./nextflowconformance -run '^TestIndependentSchemaCases$'
timeout 2m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go build -overlay .tmp/agent/nextflow-conformance/fix04/red-overlay.json -tags netgo -o .tmp/agent/bin/wr-nextflow-conformance-fix04-red ./cmd/wr-nextflow-conformance
timeout 60s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-04-probes.txt --binary .tmp/agent/bin/wr-nextflow-conformance-fix04-red --output .tmp/agent/nextflow-conformance/fix04/replay-red-probes
```

## Validation

All commands run from `/home/ubuntu/wr`. Tests and builds use Go 1.27.1,
CGO enabled, and `netgo`; tests also use `-count=1`. Existing golangci-lint
v2.12.2 runs with `GOTOOLCHAIN=go1.26.3`, retaining the approved workaround
for Go 1.27 analyzer panics. No analyzers, modules, or configuration changed.
`cleanorder -min-diff` passed for the only edited Go file.

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 30s python3 nextflowconformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go build -tags netgo -o .tmp/agent/bin/wr-nextflow-conformance-fix04 ./cmd/wr-nextflow-conformance
timeout 60s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-04-probes.txt
timeout 60s python3 .tmp/agent/nextflow-conformance/fix04/existing-cli-probes.txt
timeout 30s python3 .tmp/agent/nextflow-conformance/fix04/six-regressions.txt
timeout 15s node .docs/nextflow-conformance/evidence/phase1-schema-fix-02-ecmascript.txt
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

- All sixteen owned Go tests pass, including all 1,190 decoder mutations.
  The emission test confirms all eleven stock schemas match generation.
- Stock Python jsonschema 4.10.3 passes eleven schemas and 1,190 mutations,
  without a format-checking plugin.
- All 744 new probe cases pass in the compiled CLI, stock schema validator,
  and native ECMAScript. This includes all 93 review 04 cases.
- All 416 inherited compiled CLI assertions and JSON result contracts pass.
  The six review 03 regressions pass in all three engines. Scratch copies of
  the old probes change only their output paths and binary name, preserving
  historical evidence and output.
- All 1,055 description mutations pass Node v22.22.2 native ECMAScript
  expression checks. This is regex portability evidence, not validation by
  a second full schema engine. Python and Node are evidence tools only.
- The full focused suite exits 1 with exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` failing. Those deferred acquisition fixtures lack required
  packaging. A1_06/A1_07 and actual acquisition remain Item 1.2 work.
- Lint exits 1 with exactly 61 deferred findings: 50 in `source.go` and 11
  in `source_test.go`. Owned files have zero findings. This does not award a
  package-wide lint pass.

Only `model.go`, the appended mutation corpus, and new fix 04 evidence
changed. Acquisition files, Go tests, entry point, schemas, modules, linter
configuration, spec, phase/progress files, and historical evidence retain
their baseline hashes. Full unrelated wr tests were excluded. No commits
or pushes were made. Edits stopped for fresh independent review.
