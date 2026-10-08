# Phase 2 A2 input bundle and contract gap

Verdict: CONTRACT GAP on 2026-09-30. Implementation is not approved until
the cross-reference and reconciliation contracts below receive independent
spec review. The parent will route that amendment, then resume A2. No user
permission is needed for this correction to the authorized six-phase work.

This sizing review changed no production code, data, schemas, or phase
status. Phase 1 remains accepted at
`ec487ed27111e61dba7d104c2579ef053e478a24`.

## Demonstrated gap

A2 requires every other cross-reference to become an edge to a resolved
tree path or an outstanding external source record. The existing closed
records cannot represent either destination independently of extraction.

The real pinned `docs/reference/process.md`, line 328, bytes
`[11678,11742)`, contains:

```markdown
- [Google Cloud](https://cloud.google.com/compute/docs/gpus/)
```

The same file's line 5 references the MyST label `process-page`, outside
the selected file. `docs/strict-syntax.md`, line 175, bytes `[4841,5078)`,
links the external Groovy standard library. These are original cached
bytes, not synthetic probes or extractor output.

`model.go:475-501` permits only `include_refs` on a block. Each reference
must have exactly one `block_id` or `external_id`; the latter resolves to
an acquired source artifact. `cli.go:966-1048` requires a destination block
or an artifact whose recorded file path equals the relative resolved path.
There is no record for a pending external URL or an unselected tree path.
The production lock contains one source artifact, `SOURCE`, the complete
source archive. Inventing downloaded files or using that archive's ID for
the URL would give false provenance. Selecting every link destination would
also change the locked selection and bootstrap contract.

A2 also requires a retained reconciliation list of removed/new block IDs
and stale reviews. No existing record or named generated artifact defines
its shape, persistence, or check-mode treatment. The amendment must settle
that storage contract before an implementor chooses a hidden format.

### Minimal amendment options

These are proposals for independent spec review, not approved schemas.

1. Add a closed source-reference record and a typed reference edge. A
   tree destination identifies its locked path and optional fragment. An
   outstanding external destination retains the literal URL and pending
   state, with no invented hash, byte count, or acquired artifact. Only an
   actual acquired reference may carry an artifact ID. An edge records its
   exact source span, literal target, and purpose (`include`, `literalinclude`,
   or cross-reference). Include edges require available local bytes and
   retain cycle/missing-target checks. Cross-references do not select files
   or enlarge bootstrap semantics. This requires explicitly extending the
   current eleven-schema vocabulary and its mutation tests.
2. Alternatively, extend block edges with a closed discriminated destination
   for block, locked tree path, acquired artifact, or pending external URL.
   This avoids a separate source record only if the spec explicitly accepts
   a pending edge as its external source record. Keep acquired and pending
   destinations structurally distinct and preserve the immutable lock.

For either option, define a deterministic retained reconciliation artifact
bound to old/new block-file hashes. It needs sorted removed/new IDs and
affected review IDs, plus retention rules for successive generations.
Specify its location, closed shape, atomic publication and `--check`
behavior. A generated artifact outside the authoritative record directory
can avoid a new top-level record only if that choice is explicit. Historical
reviews remain unchanged; no review moves to a similar replacement block.

The amendment should define deterministic label/fragment resolution and
the treatment of unresolved local labels. It must preserve A2's distinction
between missing includes, which fail, and outstanding cross-references.

## Honest A2 initialization

A2 can run before B1 without a target/profile workaround. Give `extract`
a private command-specific loader for the genuine lock and batches. Reuse
closed decoding, path safety, pinned identity checks, offline preflight,
source reading and atomic writing. Verify batch source spans and exact
eighteen-member selection against the locked bytes. Defer batch
`assigned_ids` resolution into the semantic ledger until B1 constructs it.
This is extraction input validation, not full corpus validation.

Keep `validate` and its target/profile/reference checks intact. The
production corpus currently contains only lock, batches and schemas, so a
full `validate` success is not an A2 completion criterion. B1 creates actual
obligations and linked draft cases before the target profiles can refer to
them. Extraction writes genuine block records and generated extraction
artifacts; it cannot invent semantic obligations, requirements or reviews.

Count each selected region without reviewed semantic accounting as pending,
including recognized constructs outside bootstrap. Unknown constructs remain
`unclassified` and pending. The present `countBlock` only counts unknown
kinds as pending, so extraction needs the honest A2 count. B1 subsequently
supplies reviewed semantic accounting. An extraction success claims byte
enumeration only; it cannot claim foundation or target-inventory completion.

## Measured inputs

All paths are relative to `/home/ubuntu/wr`. Scratch evidence lives under
`.tmp/agent/nextflow-conformance/phase2-a2-bundle/`. Its `measure.py`
reproduces exact inclusive line spans, half-open byte spans and SHA-256
values in `part-a-read-plan.tsv`, `part-b-read-plan.tsv`, and `manifest.json`.
Read plans and inventories are machine inputs; print only requested source
spans and compact summaries. The adjacent compact manifest preserves the
identities and totals for the parent handoff.

The measured real source root is:

```text
.tmp/nextflow-conformance/nextflow-generation-2687641921/source.tar.gz-tree
```

The old candidate manifest points at a review fixture cache. This plan uses
the production lock's source artifact to derive the real root instead.
Every one of the 160 selected files was independently checked for byte
count, SHA-256 and Git blob identity against that lock. They total 254,360
bytes, including 148 snippet files totaling 12,449 bytes. Candidate selector
hashes were checked against this real root; the audit is location evidence,
not a parser oracle. The original structures inspected include nested MyST
definition directives, separate typed signatures, grammar comments and
alternatives, lexer modes/fragments/actions, nested test strings and closures.

| Input | Part A bytes | Part B bytes |
| --- | ---: | ---: |
| Exact listed source, spec, model, CLI, tests and skills | 167,865 | 160,738 |
| Conservative token proxy at three bytes per token | 55,955 | 53,580 |

Part A reads documentation examples and all include snippets. Part B adds
the listed grammar, Gradle and Groovy spans. Its narrower existing-code
spans avoid rereading A1 schema and fixture internals. Both include the
complete relevant spec contracts and Phase 1 handoff. They reuse existing
source helpers instead of loading the acquisition implementation wholesale.
The selected inventory and 1,526,940-byte lock are processed by scripts,
never dumped into context. Run partition validation over every selected
byte; bounded human reading is not permission to sample extraction output.

### Conditional handoffs and budgets

Use separate fresh implementors and subsequent fresh reviewers. Re-measure
these plans after the contract amendment and before granting approval.
For each context reserve 60k tokens for the measured input plus this brief,
compact manifest and selector excerpts, 20k for new/changed source, fixtures
and reports, 5k for command summaries, and 13k for reasoning: 98k total.
The growth ceiling is 60,000 bytes at the same conservative proxy. For the
Part B reviewer that ceiling includes all Part A and B changed code and
tests needed to review the final extractor. Rebalance only within 98k.
If the measured final bundle exceeds it, stop and obtain a new bounded split
at an acceptance boundary; do not omit changed code from review.

1. Part A covers `A2_01` and `A2_04`: independent fixture expectations,
   Markdown/MyST partitioning, IDs, includes, unknown structures, and public
   CLI wiring. Source and grammar units may remain pending/unclassified
   until Part B; Part A awards no real-corpus acceptance.
2. After Part A independent PASS, Part B covers `A2_02` and `A2_03`:
   grammar alternatives, declarations and upstream methods, exact locked
   selectors, complete production extraction, reconciliation, and check-mode
   mutations. Part B re-runs all four UATs and reviews the complete extractor.

Expected files are `extract.go`, `extract_test.go`, independent fixtures in
`testdata/`, CLI wiring, genuine generated blocks, and the amended generated
reference/reconciliation artifacts. Model/schema edits belong only to the
approved amendment. No semantic ledger or F1/E1 production records are
authorized by this A2 sizing plan.

## Acceptance and independent expectations

Before extractor implementation, a separate fixture author records exact
byte boundaries and kinds for the `A2_01` fixture, then an independent
reviewer accepts those expectations against the original fixture bytes.
Include one heading, two signatures, three options, two table data rows
plus the separator/header, a nested warning, a code example and an include.
Supply LF, CRLF, and final-line-without-newline variants. Literal reviewed
offsets or independently counted line offsets are valid; running the future
extractor to generate its own expected data is not. Preserve punctuation
and whitespace in trivia leaves and test nested partitions, not only sums.

The grammar/source fixture needs alternatives with comments, embedded
actions, quoted separators, lexer fragments and modes, quoted test names,
named test methods, multiline strings, closures and declaration boundaries.
Use original source spans to label these expectations independently.
The Markdown fixture must also cover colon and fenced directives, nested
lists, options, signatures, tables, HTML headings, anchors and prose.

For `A2_04`, duplicate headings must yield distinct stable IDs. Unknown
directives remain visible as `unclassified`; their region stays pending.
Missing includes return 2 with `E_INCLUDE_MISSING`, cycles return 2 with
`E_INCLUDE_CYCLE`. Cover recursive includes even though the actual 148
include targets have no further include directives. Path escape and
ambiguous/absent selector probes must fail without publication.

For `A2_02`, run the public CLI twice against the real lock/cache. Compare
generated bytes and IDs. Independently walk root and child intervals for
every selected file; ensure ordered adjacent intervals cover each parent,
and reconstruct each original file from leaves. Verify included map/mix
snippets against their locked blobs. Preserve all eighteen batch IDs and
38 locked span entries, including sixteen grammar alternatives, three MixOp
methods, two dependency regions and four included bootstrap snippets. Exact
membership comes from accepted batches, not inferred extracted headings.
Re-resolve selectors uniquely and compare to locked boundaries. No shrinking
to the parser's recognized portion is allowed.

For `A2_03`, corrupt independent copies of generated blocks by removing a
warning, collapsing the two `stageAs` signatures, and deleting a grammar
alternative. Each public `extract --check` returns 1 and
`E_EXTRACTION_MISMATCH`, with the missing span named. Check mode derives
expectations from verified original bytes and leaves all inputs untouched.
Test deterministic reconciliation and retained stale-review information on
a genuine source-generation change, using isolated records.

## Required evidence and gates

Record a failing public-behavior GoConvey command for each assigned UAT
before implementation; an absent test matching zero functions is not red
evidence. Capture commands, exit status, stdout JSON and stderr separately.
Keep full logs in scratch and report compact counts and artifact identities.
Run focused UATs after each correction, then relevant package tests and lint.

```bash
timeout 10m env CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -run '^TestUAT_A2_0[1-4]$'
timeout 10m env CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance
timeout 10m golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
timeout 2m go run ./cmd/wr-nextflow-conformance extract
timeout 2m go run ./cmd/wr-nextflow-conformance extract --check
```

Run stock schema emission/mutation checks if the approved amendment changes
schemas. Use `cleanorder -min-diff` on changed Go files. The reviewer runs
the relevant tests and lint independently, reads the accepted fixture labels
and all changed code, and repeats the byte/partition/corruption audits.
No full wr repository suite belongs to this isolated tooling item. Keep
B1 and B2 after A2 independent PASS, and retain all Phase 2 exit gates.
