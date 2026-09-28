# Item 1.1 quote-boundary review

Verdict: FAIL. One P2 decoder/schema disagreement remains. The six cases
from review 03 pass. Reviewed on 2026-09-28. This review awards no Item 1.1,
package, A1, or phase pass.

## Finding

### P2: Literal masking manufactures the forbidden sentence

Location: `nextflowconformance/model.go:122`, in the replacement callback
at lines 113-123. Contract: `spec.md:161` through line 163 and lines 252-259.

The decoder substitutes the letter `x` for double-quoted spans and
backslash escapes before checking for the archived stand-in sentence.
That substitution can create text that was absent from the description.
The stock emitted schema and native ECMAScript expression accept all three
descriptions below, but the compiled Go CLI rejects them with `E_INPUT` at
`requirements.json`:

```text
TODO: describe e"a fixture"pected behaviour.
TODO: describe e\qpected behaviour.
Inspect this negative sample. TODO: describe e"x"pected behaviour. Reject that sample.
```

For the first two inputs, the replacement produces exactly
`TODO: describe expected behaviour.`. The third creates that sentence in
otherwise meaningful negative-test prose. None contains the unquoted
forbidden sentence in its original bytes. This both overrejects descriptions
and breaks the required agreement between decoding and emitted schemas.

Preserve quoted and escaped spans as opaque boundaries without substituting
characters that can form the forbidden text. Preserve outside sentence
boundaries and the archived heading rule. Add decoder and stock-schema
regressions for literals and escapes within the sentence's words, alongside
adjacent outside controls. A broader Markdown parser is not required.

The independent [probe](../evidence/phase1-schema-review-04-probes.txt)
contains 93 assertion-bearing cases. These three fail; the other 90 pass.
The accepted-record control reaches `E_TREE_INCOMPLETE`, so exit 2 alone is
never treated as description rejection. The probe retains complete corpora,
stdout, stderr, exits, expectations, and observations from all three engines.

## Scope and source verification

Approved the bounded input bundle before implementation review. It contains
the complete two-file correction diff, affected guard and emitter helpers,
targeted fixture generators, relevant Records/CLI contract spans, and prior
review 02/03 coverage. Generated schemas and manifests were parsed rather
than loaded wholesale. This is comfortably below the approximately
100,000-token bundle limit; the estimate is not measured token usage.

Applied go-reviewer, go-conventions, implementation-principles,
testing-principles, code-smells, agent-conduct, unslop, and prose-principles.
Unchanged full-scope corpus and CLI coverage relies on review 02 as assigned.

All 93 fix-manifest input paths were checked. The sole difference was
`phase1.md`: the parent had checked only Item 1.1's implemented box after
the implementation handoff. Reversing that one checkbox reproduces its
manifest hash. The parent confirmed this change and froze inputs throughout
review. All review-start source/input hashes remain unchanged at completion.

Both original Go source snapshots match review 03's final source hashes.
Their complete diffs are confined to quoted-span masking and schema guard
generation. The CLI, Go tests, and entry point retain their review 02 hashes.
All original 351 mutations remain intact, including their serialized prefix.
Eight schemas differ only in description pattern values; three are unchanged.

The paired single/double/backtick grammar, backslash escapes, contraction
boundaries, unmatched delimiters, explicit Unicode White_Space class, and
Unicode simple folds were reviewed together. Independent probes cover all
25 whitespace code points across quoted, unquoted, and archived contexts,
long-s folding, nested literals, contractions, unmatched delimiters, escapes,
and outside sentences. Existing tests exercise public decoding or CLI
results, not private helper outputs. No separate blocking code-smell finding
was identified.

## Reproduction and results

Run from `/home/ubuntu/wr`. Exact bounded commands, exits, input/output hashes,
and results are recorded in the
[manifest](../evidence/phase1-schema-review-04-manifest.json). Logs are under
`.tmp/agent/nextflow-conformance/review04/`.

```bash
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-fix03 ./cmd/wr-nextflow-conformance
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-review-04-probes.txt
```

- Build exits 0. The independent probe exits 1 with the three failures above.
- All sixteen owned Go tests pass with Go 1.27.1, CGO enabled, `netgo`, and
  `-count=1`.
- Stock Draft 2020-12 validation passes all eleven schemas and 539 mutations
  without a format-checking plugin.
- All 416 compiled CLI assertions and JSON result contracts pass.
- All six exact review regressions pass in Go, stock schema validation,
  and native ECMAScript expressions.
- All 404 existing description cases pass native ECMAScript regex checks.
  This verifies regex portability, not a second full schema engine.
- The preserved original-source Go overlay and original-schema stock
  checker both exit 1, reproducing the earlier quote-exemption defect.
- The full focused suite exits 1 with exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` failing. Their deferred acquisition fixtures lack packaging.
- Lint exits 1 with exactly 61 deferred findings: 50 in `source.go`, 11 in
  `source_test.go`, and zero in owned Item 1.1 files. Existing golangci-lint
  v2.12.2 ran under Go 1.26.3 with unchanged analyzers and configuration.

The reviewer changed only this review, its durable probe, and evidence.
No implementation, schema, fixture, spec, checkbox, module, or linter edits
were made. No commits or pushes were made. Full unrelated wr tests were
excluded. Item 1.2 still owns actual acquisition, new A1_06/A1_07 tests,
runtime evidence, and the deferred acquisition lint findings.
