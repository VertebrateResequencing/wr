# Item 1.1 description guard review

Verdict: FAIL. One P2 defect remains in the quoted-description exemption.
Reviewed on 2026-09-28. The whitespace bypass reported in review 02 is fixed.
This review awards no Item 1.1, package, A1, or phase pass.

## Finding

### P2: The stand-in guard rejects quoted negative fixtures

Locations: `nextflowconformance/schema.go:262` and
`nextflowconformance/model.go:111`. Contract: `spec.md:252` through line 259.

The schema and decoder reject this meaningful description, where `\u00a0`
denotes an actual nonbreaking space inside the quoted fixture:

```text
The negative fixture is `\u00a0TODO: describe expected behaviour.\u00a0` and must fail.
```

The backtick branch treats the opening quote as an archived heading suffix.
It then matches the sentence and trailing whitespace without considering
the closing quote. The stock Draft 2020-12 validator and native ECMAScript
regexes both reject it. The compiled Go CLI returns exit 2 with `E_INPUT` at
`requirements.json`, so the description cannot be stored for its negative
test. The specification explicitly permits quoted source, fixtures, and
negative tests containing these strings.

The preserved original Go source accepts this NBSP-padded quoted fixture
and reaches the expected offline `E_TREE_INCOMPLETE` boundary. The current
change therefore introduces this rejection while fixing the unquoted NBSP
bypass. Two related existing cases also reject: an ASCII space before the
closing backtick, and a multi-sentence double-quoted fixture containing the
stand-in sentence. These share the same lack of quote context.

Keep the explicit whitespace and case-fold agreement, but make the guard
distinguish a quoted literal from an unquoted stand-in sentence and an
archived heading suffix. Add decoder and emitted-schema regressions for
padding inside quotes and multi-sentence quoted fixtures. Preserve rejection
of the unquoted padded sentence and the archived heading form.

The independent assertion-bearing
[probe](../evidence/phase1-schema-review-03-probes.txt) retains six cases.
Three quote-exemption assertions fail; the unquoted NBSP, unpadded quoted
sentence, and archived heading controls pass. It compares the current Go
CLI, the original-source overlay CLI, stock Python schema validation, and
native ECMAScript evaluation of the emitted description expressions.

## Scope and source verification

Approved the bounded input bundle before implementation review. The four
owned Go files total approximately 107 kB, and the 68 kB mutation file was
read by relevant groups. Complete fix diffs, affected functions, relevant
contract spans, prior evidence, and bounded command output fit within the
approximately 100,000-token limit. This is an estimate, not measured token
usage.

Applied go-reviewer, go-conventions, implementation-principles,
testing-principles, code-smells, agent-conduct, unslop, and prose-principles.
Read Item 1.1, the Records/CLI contract, A1/D2 boundaries, review 02, the fix
02 evidence and manifest, the original-source overlay, and the Nextflow
naming mapping. Unchanged code coverage relies on review 02 as authorized.

All 87 fix-manifest source/input hashes matched. All four owned Go baseline
hashes match review 02. The original model/schema snapshot hashes match the
captured baseline; their complete diffs contain only the description guard
and class generator changes. The CLI and Go tests are unchanged. The
original 138 mutations remain intact, with 213 additional cases. Generated
schema differences are confined to description pattern values in eight
files; the other three schemas are unchanged.

The shared explicit Unicode White_Space class corrects the Go/Python/JS
whitespace mismatch. Explicit simple-fold classes also preserve Go's long-s
handling in the schema. The current fixtures cover all 25 whitespace code
points, nearby non-whitespace characters, archived headings, and simple
quoted literals. They do not cover the quote contexts in this finding.
Cross-engine agreement alone does not establish the quoted-text contract.
No separate blocking code-smell finding was identified.

Review 02's first three fixes and naming migration remain unchanged.
Metadata annotations remain documentation; they do not enforce relational
rules. Acceptance IDs A1_01 through A1_07 remain Item 1.2 work.

## Reproduction and results

Run from `/home/ubuntu/wr`. Exact commands, exits, source hashes, output
hashes, and summarized results are in the
[manifest](../evidence/phase1-schema-review-03-manifest.json). Logs and input
corpora are under `.tmp/agent/nextflow-conformance/review03/`.

```bash
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-fix02 ./cmd/wr-nextflow-conformance
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -overlay .tmp/agent/nextflow-conformance/fix02/red-overlay.json -o .tmp/agent/bin/wr-nextflow-conformance-review03-original ./cmd/wr-nextflow-conformance
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-review-03-probes.txt
```

- Both builds exit 0. The independent quoted-description probe exits 1
  with three contract failures and retains stdout, stderr, exit codes,
  complete input corpora, expected validity, and engine observations.
- All sixteen owned Go tests pass with Go 1.27.1, CGO enabled, `netgo`, and
  `-count=1`.
- All eleven schemas and 351 existing mutations pass stock Draft 2020-12
  validation without a format-checking plugin.
- All 228 inherited/current compiled CLI assertions and JSON result
  contracts pass. Accepted fixtures reach the expected offline failure;
  they do not prove successful acquisition.
- All 216 existing description cases pass native ECMAScript regex checks.
  This is a regex portability check, not a full second schema engine.
- The original-source overlay Go test and original-schema stock checker
  both exit 1, independently reproducing the prior whitespace defect.
- The full focused suite exits 1 with exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` failing. Deferred acquisition fixtures lack packaging.
- Lint exits 1 with exactly 61 deferred findings: 50 in `source.go` and 11
  in `source_test.go`. Owned Item 1.1 files have zero findings. The existing
  golangci-lint v2.12.2 ran under Go 1.26.3 with unchanged analyzers.

The reviewer changed only this review, its durable probe, and evidence.
Source, fixtures, schemas, spec, checkboxes, module files, linter configuration,
and earlier evidence retain their captured hashes. No commits or pushes were
made. Full unrelated wr tests were excluded. Item 1.2 still owns acquisition,
new A1_06/A1_07 tests, measured runtime evidence, and acquisition lint fixes.
