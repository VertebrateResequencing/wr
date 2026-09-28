# Item 1.1 independent schema and CLI review

Verdict: FAIL. Three Item 1.1 defects remain. Reviewed on 2026-09-28.
Existing Item 1.1 tests pass, but independent probes expose scalar schema
disagreement, invalid timestamps accepted by Go, and rejection of source
spans bound to acquired source artifacts. Item 1.2 and phase completion
remain unreviewed.

## Input budget and scope

The updated four owned Go files total 102,924 bytes, about 34,308 tokens at
three characters per token. Skills, relevant specification spans, fixtures,
entry points, and bounded caller reads fit the approved 55,000-token primary
input allowance. Generated schemas were validated by tools without dumping
their contents. Bounded test output and selective diagnostics kept this
review within the approximately 100,000-token bundle. This is an estimate,
not measured model usage.

Reviewed all of `model.go`, `schema.go`, `cli.go`, `model_test.go`, and the
entry point, plus the amended record, CLI, architecture, A1, and D2 contract.
Checked acquisition callers for the changed interfaces, deferred ownership,
and test failures. Applied go-reviewer and its referenced conventions,
testing principles, implementation principles, and code-smell baseline.
Used unslop and prose-principles for this report.

## Findings

### 1. P2: Emitted scalar schemas disagree with the decoder

Locations: `conformance/schema.go:145`, `conformance/schema.go:182`,
`conformance/schema.go:248`, `conformance/schema.go:252`, and the shared
patterns at `conformance/model.go:1186`.

The emitted scalar constraints do not meet the promised standard-schema
boundary. These are local scalar checks, not deferred corpus relationships
or the documented integer lexical difference:

| Probe | Stock schema | Go decoder |
| --- | --- | --- |
| ID `TARGET\n` | Accepts | Rejects ID |
| SHA-256 of 64 `a` characters followed by `\n` | Accepts | Rejects hash |
| File path `line\n/../escape` | Accepts | Rejects path |
| Include path `dir./../shared.nf` | Accepts | Rejects include path |
| HTTPS origin `https://example.org/%zz` | Accepts | Rejects URL |
| HTTPS origin `https://[::1]/source` | Rejects | Accepts URL |

The table uses escaped notation for actual newline characters. `$` allows a
match before a final newline in the independent validator, unlike Go's
regular expression behaviour. The path lookaheads use `.*`, which misses
later lines; the include pattern also misses a cancellable directory whose
name ends in a dot. The URL pattern neither admits bracketed IPv6 hosts nor
rejects malformed percent escapes.

Use scalar constraints with the same acceptance contract across both
validators. Preserve legitimate relative includes and HTTPS IPv6 URLs.
Add these cases to the independent fixture set and decoder tests. Changing
`x-wr-*` metadata cannot repair standard-schema enforcement.

### 2. P2: Timestamp validation accepts non-RFC3339 text

Location: `conformance/model.go:120`.

`time.Parse(time.RFC3339, text)` accepts both
`2026-09-28T00:00:00,1Z` and `2026-09-28T0:00:00Z`. Both violate the required
RFC3339 timestamp spelling and fail the emitted schema. Mutating an attempt's
`started` field to either value passes Go decoding. The complete corpus with
the comma fraction proceeds through reference and identity validation to the
same offline tree-fixture failure as the unmodified corpus.

Check the RFC3339 lexical form as well as the parsed date, time, and UTC
zone. Add negative decoder tests for both accepted spellings and retain
positive tests for valid fractional seconds.

### 3. P2: Acquired source artifacts cannot supply review spans

Locations: `conformance/cli.go:710`, `conformance/cli.go:721`, and
`conformance/cli.go:768`.

The amended contract permits acquired source artifacts used for semantic
review. `lockedSourceReferences` now admits those artifacts in a review's
`input_hashes.source`, but the earlier span validation still builds its map
only from `lock.Files`. A source artifact with path `external.groovy`, role
`source`, packaging `file`, a valid hash, and byte count 3 is accepted by the
lock decoder. A review binding those bytes and the matching span `[0, 3)`
fails with `E_REFERENCE` at that path.

The full baseline fixture reaches `E_TREE_INCOMPLETE` during offline
verification. Adding the locked source artifact and changing that review's
source input and source span instead fails before offline verification.
This rejects a permitted source-review record at the corpus boundary.
Resolve review spans against the same locked source identities as their
source inputs, preserving hash and byte-bound checks. Add a corpus test
that includes both the source input and its span; the existing new source
artifact test changes only `input_hashes.source`.

## Reproduction and evidence

All commands below run from `/home/ubuntu/wr`. The durable
[probe script](../evidence/phase1-schema-review-01-probes.txt) constructs
each input from the independent fixtures and invokes the compiled public
CLI. It writes exact input corpora, stdout, stderr, exit codes, and a result
index under `.tmp/agent/conformance/review01-probes/`. No network request
occurs: validation stops before acquired-file verification in these fixtures.

```bash
timeout 2m env GOTOOLCHAIN=go1.27.1 go build -o .tmp/agent/bin/wr-conformance-review01 ./cmd/wr-conformance
timeout 30s python3 .docs/nextflow-conformance/evidence/phase1-schema-review-01-probes.txt
timeout 10s python3 conformance/testdata/check_schemas.py
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance -run 'Test(EmptyCorpus|MalformedRecords|ClosedSchemas|RecordConstraints|SchemaEmission|NestedRecordConstraints|AmendedArtefactContract|CLIInvocationContract|CorpusContract|IndependentSchemaCases|RuntimeClosureMembership|TypedCorpusRelations|AttemptLoadingAndCounts|RelationalRecordConstraints|FailedCommandResultContract|ReviewSourceFileReferences)$'
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./conformance ./cmd/wr-conformance
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./conformance/... ./cmd/wr-conformance/...
```

Observed results:

- CLI build: exit 0 with Go 1.27.1.
- Existing independent schemas: all eleven schemas and 49 mutations pass.
- Existing owned tests: exit 0; see `review01-owned-tests.log` and `.exit`
  under `.tmp/agent/conformance/`.
- New probes: eleven executions, with the discrepancies above. The script
  exits 0 when it records the observations; this is not a conformance pass.
- Full focused suite: exit 1, exactly `TestUAT_A1_01` through
  `TestUAT_A1_05` fail. See `review01-tests.log` and `.exit`. Their fixture
  constructors lack required packaging, so failures happen before the
  acquisition assertions. No test is skipped or converted into a pass.
- Relevant lint: exit 1, exactly 61 findings, with 50 in `source.go` and
  11 in `source_test.go`. Owned schema/CLI files and the entry point have
  zero findings. See `review01-lint.log` and `.exit`. The approved Go 1.26.3
  analyzer workaround was used without changing configuration or modules.

Compiled CLI checks also covered empty corpus, command-specific flags,
unknown command, deferred verify, and malformed-lock acquisition. Each
returned exit 2, one JSON result, `complete:false`, diagnostic stderr, all
sixteen required counts, and zero verified artifacts. Verify preserved its
requested claim and suite; other commands used null suites. Artifacts are
under `.tmp/agent/conformance/review01-cli/`. The acquisition probe used an
invalid lock and made no requests. Existing Go tests cover pre-cancelled
contexts; entry-point inspection confirms SIGINT/SIGTERM propagation. No
signal-driven in-flight network or subprocess cancellation claim is made.

## Snapshot and deferred work

The [SHA-256 file manifest](../evidence/phase1-schema-review-01-manifest.json)
records the reviewed source, fixtures, generated schemas, entry points,
module files, linter configuration, spec, phase plan, and implementation
evidence. Paths are repository-relative.

No implementation, specification, phase checkbox, or production entry point
was changed. No commits or pushes were made. No separate code-smell finding
was warranted beyond the correctness findings above.

Item 1.2 still owns the actual acquisition changes, old fixture migration,
all seven A1 UATs including A1_06/A1_07, runtime measurement, and remaining
acquisition lint. Its five current UAT failures and 61 lint findings do not
become a package or phase pass by scope separation. This review additionally
withholds the Item 1.1 pass until the three findings are corrected.
