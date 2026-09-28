# Item 1.1 schema review fixes

Status: IMPLEMENTED, awaiting fresh independent review. All three findings
in [review 01](../reviews/phase-01-schema-review-01.md) have regression
coverage and passing Item 1.1 checks. Edits stopped for fresh review on
2026-09-28. Item 1.2 acquisition tests and lint still fail as listed below.

## Scope and implementation

The [approved bundle](../reviews/phase-01-schema-bundle.md) remains the
handoff boundary. The four owned Go files total 106,087 bytes. Generated
schemas were regenerated and validated without dumping their contents.
Applied go-implementor, go-conventions, implementation-principles, and
testing-principles; this evidence follows unslop and prose-principles.

The schema emitter now uses strict end-of-input assertions for IDs, hashes,
Git IDs, test names, Maven coordinates, package roots, and artifact suffix
conditions. Path patterns check whole components across newlines. Include
paths permit leading parent components and reject every cancellable parent
component, including a preceding directory whose name ends with a dot.
Legitimate newline-containing POSIX names remain accepted.

Go and schema emission share the HTTPS grammar. It checks percent escapes
in hosts, paths, queries, and IPv6 zones; admits full and compressed IPv6,
IPv4 tails, numeric ports, and valid relative URI components; and rejects
credentials, fragments, malformed escapes, and malformed IPv6 literals.

Go checks the timestamp pattern before parsing. The shared pattern enforces
UTC spelling, fractional seconds, clock ranges, month lengths, and Gregorian
leap years. The installed Python FormatChecker does not provide date-time
validation. The new calendar probes exposed that gap, so the emitted schema
now enforces those constraints with its pattern and does not depend on an
optional format plugin. The independent checker uses stock Draft 2020-12
validation with no FormatChecker.

Span validation now resolves against `lockedSourceReferences`, the same
locked source identities used by review input hashes. Source-tree files and
acquired source artifacts retain path, hash, and half-open byte-bound checks.
Execution artifacts cannot supply source spans. Tests include a matching
external source input and span, an empty final-boundary span, and wrong hash,
path, role, and end-offset cases.

Standard schema validation remains separate from Go's JSON lexical checks,
sorted identities, typed corpus references, span comparisons, and dependency
relationships. Existing `x-wr-*` annotations describe those additional rules;
they do not enforce them.

## Red evidence

Logs below are under `.tmp/agent/conformance/`. Commands ran from the
repository root, with Go 1.27.1 and bounded execution.

- `fix01-review-probes-red.log`: the review's public CLI probe reproduced
  the original scalar disagreements, accepted malformed timestamps, and
  rejected the acquired source span before offline verification. The
  original probe records observations; its exit 0 is not a validation pass.
- `fix01-tests-red.log` and `.exit`: exit 1 for the added decoder cases and
  the external source input plus span. Command:
  `timeout 2m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance -run 'Test(IndependentSchemaCases|ReviewSourceFileReferences)$'`.
- `fix01-schema-red.log` and `.exit`: exit 1; the stock validator accepted
  the final-newline ID. Command:
  `timeout 10s python3 conformance/testdata/check_schemas.py`.
- `fix01-schema-green-initial.log` and `.exit`: despite the historical
  filename, this intermediate run failed, exit 1. It exposed acceptance of
  February 29 in a non-leap year when the optional date-time checker was
  absent. The final schema pattern rejects it.

## Final checks

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 10s python3 conformance/testdata/check_schemas.py
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-conformance-fix01 ./cmd/wr-conformance
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-fix-01-probes.txt
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

Observed results and corresponding `.log`/`.exit` files:

- `fix01-owned-tests`: exit 0, all sixteen Item 1.1 test functions pass.
  The developer entry-point package builds and has no separate test files.
- `fix01-independent-schema`: exit 0, eleven schemas and 138 independently
  authored mutations pass, including 89 additions for the review fixes.
- `fix01-build`: exit 0, compiled developer CLI built with Go 1.27.1.
- `fix01-cli`: exit 0 for the assertion-bearing probe script. Its fifteen
  public CLI executions each return process exit 2 as expected for these
  malformed or deliberately incomplete fixture corpora.
- `fix01-full-tests`: exit 1. Exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` fail. Their old acquisition fixtures lack required
  packaging and fail decoding before acquisition assertions. They remain
  Item 1.2 work, together with A1_06/A1_07 and real acquisition evidence.
- `fix01-lint`: exit 1. Exactly 61 findings remain, 50 in `source.go` and
  11 in `source_test.go`; see `fix01-lint-ownership.json`. Owned Item 1.1
  files and the entry point have zero findings. No package lint pass is
  claimed. All configured analyzers stayed enabled under the approved
  Go 1.26.3 workaround with golangci-lint v2.12.2.
- `fix01-cleanorder`: exit 0, ran on all four edited Go files.
  `fix01-emission`: exit 0, regenerated schemas through TestSchemaEmission
  with `CONFORMANCE_UPDATE_SCHEMAS=1`; the final owned test run confirms
  exact generated bytes. `fix01-lint-fix`: exit 0, applied supported fixes
  using an owned-file patch filter. The final lint command above uses the
  full focused package scope and reports the acquisition findings.

## Public CLI evidence

The durable [probe script](phase1-schema-fix-01-probes.txt) writes input
corpora, stdout, stderr, exit codes, and `results.json` under
`.tmp/agent/conformance/fix01-probes/`. It asserts the schema result and CLI
boundary for every probe.

Malformed scalar values and both malformed timestamp spellings produce
`E_INPUT`. Valid IPv6 passes scalar decoding. The full unmodified corpus and
the corpus binding an external source input plus its matching span both
reach the expected offline `E_TREE_INCOMPLETE` fixture failure. Corrupting
that span's hash, path, or end offset, or changing the artifact to a valid
execution dependency, produces `E_REFERENCE`. These fixtures do not claim
successful acquisition, offline verification, or a completed A1 story.

The [final manifest](phase1-schema-fix-01-manifest.json) records SHA-256
identities for the reviewed inputs and new evidence. Compared with review
01, `source.go`, `source_test.go`, the specification, module files, linter
configuration, and both developer entry points remain byte-identical.
No production package, phase checkbox, or progress file was edited. No
commits or pushes were made. Full unrelated wr tests were not run.
