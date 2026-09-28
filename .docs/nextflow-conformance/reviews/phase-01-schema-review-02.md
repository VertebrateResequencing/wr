# Item 1.1 schema, CLI, and Nextflow naming review

Verdict: FAIL. One Item 1.1 description-validation defect remains. The three
findings from review 01 are fixed. The naming migration is mechanical.
Reviewed on 2026-09-28. This verdict does not assess Item 1.2 or award a
package, A1, or phase pass.

## Finding

### P2: Whitespace bypasses the required placeholder-sentence guard

Locations: `nextflowconformance/model.go:101`, especially lines 107-109;
`nextflowconformance/schema.go:256`, especially line 257.
Contract: `spec.md:252` through line 259.

The whole-description stand-in sentence is explicitly forbidden. Adding one
leading space, tab, or nonbreaking space makes both the emitted schema and
Go decoder accept it. `meaningfulText` trims text for its single-word check
but applies the sentence expression to the original untrimmed value. The
schema's sentence expression likewise starts at column zero without
allowing leading whitespace. These are descriptions, not quoted fixtures.

The same guard also disagrees between the stock schema validator and Go for
nonbreaking space after a sentence boundary. Go's regular expression uses
ASCII `\s`, while the independent schema validator recognizes that space.

| Requirement text, with escapes shown | Stock schema | Public CLI |
| --- | --- | --- |
| `TODO: describe expected behaviour.` | Rejects | `E_INPUT` |
| ` TODO: describe expected behaviour.` | Accepts | `E_TREE_INCOMPLETE` |
| `\tTODO: describe expected behaviour.` | Accepts | `E_TREE_INCOMPLETE` |
| `\u00a0TODO: describe expected behaviour.` | Accepts | `E_TREE_INCOMPLETE` |
|`First.\u00a0TODO: describe expected behaviour.`| Rejects |`E_TREE_INCOMPLETE`|

Each mutated complete corpus reaches the same offline tree-fixture failure
as the valid baseline when the description is accepted. Thus the observed
exit 2 does not mean the malformed description was rejected. Exact inputs,
stdout, stderr, and exits are preserved under
`.tmp/agent/nextflow-conformance/review02/boundaries/`.

Make both guards recognize the same leading and sentence-boundary whitespace
while preserving permitted quoted literals. Add independent schema and Go
regression cases for these inputs. This is an Item 1.1 scalar rule, not a
deferred semantic interpretation or cross-record relationship.

## Scope and previous findings

Reviewed the complete owned `model.go`, `schema.go`, `cli.go`, and
`model_test.go`; all eleven independent record fixtures; the schema mutation
checker and cases; both developer entry points; the production entry-point
dependency boundary; bounded acquisition caller spans; and the relevant
Architecture, Records, Commands, A1, D2, and Implementation Order contract.
Applied go-reviewer, go-conventions, implementation-principles,
testing-principles, code-smells, agent-conduct, unslop, and prose-principles.

The four owned Go files total 106,163 bytes, approximately 35,388 tokens at
three bytes per token. Their full contents, bounded supporting reads, and
probe output fit the approved approximately 100,000-token bundle. Generated
schemas were validated as complete files without dumping them into context.
This is an input-size estimate, not measured model token usage.

All three previous findings have executable regression evidence:

- Final-newline IDs/hashes and concealed path traversal reject consistently.
  Relative includes retain their permitted forms. Malformed URL escapes
  reject, and bracketed IPv6 origins pass scalar decoding.
- Comma fractional timestamps and single-digit hours reject. Valid fractional
  timestamps and leap-year dates pass; invalid dates and clock fields reject.
- An acquired source artifact can supply both a hashed review input and its
  matching span. Wrong span hash, path, bounds, or execution role rejects.
  The valid source-span corpus reaches the baseline offline tree failure.

The source-span fix uses the same locked identities for both checks. Standard
schema constraints cover local shape and scalar rules; Go separately checks
JSON lexical properties, sorted IDs, typed references, dependency cycles,
runtime closure, source bounds, and attempt chronology. `x-wr-*` annotations
are documentation, not enforcement. Independent probes exercised wrong-type
references, missing dependencies, cycles, reversed spans, an empty span at
the final boundary, and exact signed 64-bit limits.

All eleven closed record definitions and D2 input fields are present. The
review covered required/null/conditional fields, packaging and coordinates,
target identity, attempt-file loading, counts, command-specific flags, failed
result claims, and cancellation wiring. Existing tests exercise cancelled
contexts; the entry point propagates SIGINT/SIGTERM. No claim is made about
in-flight acquisition cancellation or later execution/freshness semantics.
No separate blocking code-smell finding was identified.

## Naming migration

All 33 migrated package, CLI, fixture, and generated-schema files reproduce
their recorded pre-migration SHA-256 after reversing only the documented
names. The schema checker retains all 138 cases. Code has no active old
package paths, binary names, schema identity prefixes, or environment
switches. Old names remaining in the prompt quote or historical evidence
describe the original state; they are not active tool paths.

All 244 current hashes in the migration manifest match at review completion.
During review, `phase1.md` differed because the orchestrator had checked
Item 1.1's implemented box. The parent confirmed that state change and then
unchecked it after this FAIL finding. These checkbox changes are separate
from the migration. Historical evidence and reviews retain their recorded
hashes. Production code, module files, and linter configuration are unchanged.

## Reproduction and results

Run from `/home/ubuntu/wr`. Logs and matching exit files are under
`.tmp/agent/nextflow-conformance/review02/`. The two new durable probe scripts
preserve the prior fifteen assertions and add 24 independent boundary
observations. The boundary script exits 0 when it records results; that is
not a conformance pass.

```bash
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-nextflow-conformance-review02 ./cmd/wr-nextflow-conformance
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-review-02-probes.txt
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-review-02-boundaries.txt
timeout 10s python3 nextflowconformance/testdata/check_schemas.py
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 -v ./nextflowconformance ./cmd/wr-nextflow-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

- `build`: exit 0 with Go 1.27.1.
- `owned`: exit 0; all sixteen Item 1.1 test functions pass.
- `stock`: exit 0; all eleven schemas and 138 mutations pass with stock
  Draft 2020-12 validation, without a format-checking plugin.
- `cli`: exit 0; all fifteen inherited compiled CLI probes meet their
  assertions. Their processes return the expected exit 2.
- `boundaries`: 24 additional compiled CLI observations, including the
  description defect above. Valid scalar/relational cases reach the expected
  offline fixture failure; they do not establish acquisition success.
- `json-contract`: all 39 CLI outputs contain one JSON result, all sixteen
  explicit counts, zero verified artifacts, `complete:false`, required
  diagnostic fields, and nonempty stderr.
- `full`: exit 1; exactly `TestUAT_A1_01` through `TestUAT_A1_05` fail.
  Their old acquisition fixtures lack packaging and fail before their
  intended acquisition assertions.
- `lint`: exit 1; exactly 61 findings, 50 in `source.go` and 11 in
  `source_test.go`. Owned Item 1.1 files and the entry point have none.
  golangci-lint v2.12.2 ran under Go 1.26.3 with all configured analyzers
  enabled. No configuration or module change was made.

The [review manifest](../evidence/phase1-schema-review-02-manifest.json)
records source, spec, fixture, schema, historical evidence, probe, and output
hashes. It also records command results and naming verification. No
implementation, specification, checkbox, or historical evidence was changed
by this reviewer. No commits or pushes were made.

Item 1.2 still owns acquisition, fixture migration, all seven A1 UATs,
A1_06/A1_07 additions, measured runtime evidence, and the 61 acquisition
lint findings. Full unrelated wr tests were excluded as required. Item 1.1
remains unapproved until the description guard is corrected and reviewed.
