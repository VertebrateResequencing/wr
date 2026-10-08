# Phase 2 Item 2.1 independent schema review 06

FAIL on 2026-10-08. F1-F10 are resolved. One P2 test-strength gap remains:
invalid UTF-8 inside otherwise valid free-string fields is accepted by a
separately compiled guard-removal fault while all 50 package tests pass.
Production decoding rejects those inputs correctly. Item 2.1 remains unreviewed;
this verdict does not authorize Item 2.2.

Review owner: `/root/nextflow_phase2_schema_review06`. Queue owner: `/root`.
Branch: `nextflowdsl`; shared worktree: `/home/ubuntu/wr`. All requested shared
source, specification, checklist, schema, fixture and case inputs were
preserved. This reviewer wrote only this report and owned disposable evidence.
No commits or pushes were made.

## Finding

### F11: Assert malformed UTF-8 inside locally valid strings

Priority P2. `nextflowconformance/model_reference_test.go:518` overwrites byte 1
of a JSON fixture with FF. That position is outside a string, so JSON syntax
rejects it even if the UTF-8 guard is removed.
`nextflowconformance/model_test.go:65` puts FF in an ID on an incomplete target
record. The resulting replacement character fails the ID contract and other
required fields are absent. Neither assertion isolates the envelope requirement.

The exact compiled fault changes only `validJSONEnvelope` at
`nextflowconformance/model.go:1268`. It preserves nonempty input and the final
newline, and keeps the UTF-8 import used:

```go
// Before
return utf8.Valid(data) && len(data) > 0 && data[len(data)-1] == '\n'
// After
return (utf8.Valid(data) || true) && len(data) > 0 && data[len(data)-1] == '\n'
```

Go JSON decoding replaces the malformed byte inside a string with U+FFFD. The
resulting records satisfy local shape, ordering and uniqueness rules. The fault
therefore survives the final 17 focused tests, the four additional existing
boundary tests, and all 50 package tests. It is non-equivalent: the correct
decoder rejects all eleven malformed controls, while the mutant accepts them
with nil errors.

The independent lossless projection supplies four complete fixture templates and
eleven exact field paths. For each row, replace the selected string with the
bytes 61 FF 62 inside otherwise unchanged compact JSON followed by LF. Reject
that input. Accept the paired bytes 61 EF BF BD 62, which encode the actual
U+FFFD character. Freeze every other identity field and member.

```text
Block span.file
Edge span.file
Edge target
Tree destination.path
Tree destination.fragment
Path candidate.path
Path candidate.fragment
Definition candidate.span.file
Definition candidate.target
Current input path
Reviewed input path
```

All eleven valid controls satisfy their stock schemas. The malformed controls
normalize to those same locally valid objects if the byte guard is suppressed.
This is a decoder envelope obligation in Item 2.1. Future byte, origin,
ownership or loader checks do not replace the missing assertion.

Add `TestExtractionRecordUTF8Strings` for these eleven invalid/valid pairs.
Check fixture construction and marshaling errors separately. Insert malformed
bytes into serialized JSON, because marshaling an invalid Go string can
normalize it before decoding. Both block decoding and standalone catalog
decoding must exercise their supported entry points. Preserve every existing
assertion and keep production unchanged.

The 22-control probe is green on pristine source, red on the fault with all
eleven rejection expectations violated, and green after restoration. Exact
controls, templates, expectations, patch text and logs are retained. One related
finding covers the shared guard across all eleven field locations.

## Complete Item 2.1 review

Reviewed required `cross_refs`, three closed destination variants, both
homogeneous ambiguity-candidate variants, the standalone extraction manifest and
embedded catalog, spans, strict envelopes, recursive closure and validation,
local tuple identities, order and uniqueness, nulls, UTF-8, safe paths,
decoder/stock agreement and regex portability. `include_refs` remains identical.
No production correctness defect was found within this item.

The complete current tests preserve F3-F9 controls, including nineteen F7
metadata pairs, F8 snapshot exclusions, F9 ten adjacent-priority controls,
nullable-fragment handling and literal URI preservation. Prior test
declarations, constants, helpers and imports are byte-identical after removing
only the two F10 additions and the fmt import. All 47 other package/CLI files
are unchanged from Review 05.

The manually ordered free13 and uppercase-ID9 alphabets reconstruct 1,172
complete controls. Each freezes all other identity fields. Complete alphabets
and every forward distinct pair accept; every reverse pair rejects. All local
shapes pass stock validation. Expected order is independent of production
comparators.

All 95 historical faults were independently compiled and tested in detached
scratch. Ninety-four non-equivalent faults are killed; the reviewed-ID UTF-16
fault is equivalent because every valid ID is ASCII. Its 12,544 mechanical
comparisons agree with byte order, and the complete focused suite stays green.
It is excluded from the defect count.

The exact eleven former survivors are now killed: eight lowercasing faults and
three reachable UTF-16 faults. Each passes the original complete 16-test suite,
fails the added byte-alphabet assertions, fails the final complete 17-test suite
and fails the independent alphabet probe. Each restoration passes. The remaining
historical faults retain their independent proof; schema faults also use the
actual stock checker and ECMAScript checks where relevant.

The additional UTF-8 guard fault brings the total to 96 valid compiled faults:
94 killed, one equivalent survivor and one genuine survivor. There are no
invalid or untriaged faults. The survivor, rather than a coverage percentage,
determines FAIL. Whole-package coverage was captured to inspect partially
covered mechanisms; it does not replace mutation evidence.

Independent URI controls cover eighteen positives, seventeen other invalid
cases, 350 literal forbidden-byte/component cases and all 256 percent-encoded
octets in five components. All 1,665 expectations agree with stock and
ECMAScript validation. URI scheme and IPvFuture requirements were checked
against [RFC 3986](https://www.rfc-editor.org/rfc/rfc3986.html); URI/IRI
character distinctions were checked against [RFC
3987](https://www.rfc-editor.org/rfc/rfc3987.html).

Byte/origin/ownership/history relations, snapshot path/hash equality and
publication belong to Item 2.2 or later work. This review does not claim
complete public A2_04, full A2 acceptance or a wr runtime implementation.

## Actual final gates

The final shared-source gates all pass. Go used 1.27.1, CGO_ENABLED=1,
GOPROXY=off, netgo and count=1, with the explicitly provisioned
NEXTFLOW_CONFORMANCE_TEST_INPUTS root. No downloads, skips or unrelated full-wr
tests were used. One heavy workload ran at a time with bounded timeouts.

```text
Focused expression: Test(CrossReference|ExtractionRecord|ExtractionCatalog|
SchemaEmission|ClosedSchemas|IndependentSchemaCases)
Focused final: 17 tests PASS
Python actual stock: 12 schemas / 1,575 cases PASS
Full final package and CLI: 50 package tests PASS; CLI has no tests
Configured lint: 0 issues; no autofix
ECMAScript: all 294 emitted patterns PASS
Independent nonempty/newline: 4 controls PASS
Independent URI: 1,665 controls PASS
Go format and git diff --check: PASS
Package test coverage: 89.1%; package plus CLI total: 88.8%
```

Lint used the configured `.tmp/agent/bin/golangci-lint`, authorized
GOTOOLCHAIN=go1.26.3, original analyzers, netgo and origin/master baseline
499b350e56af1395f03b3a91a3ad0192e9ea9079. ECMAScript used Node v22.22.2. The
actual final package/CLI regression was run on the shared final source.
Unchanged completed gates were not repeated gratuitously.

The first full UTF-8 mutant attempt in scratch failed six tests because their
relative pinned runtime prerequisites were absent. That environmental failure
was retained and was never counted as a kill or survival. An isolated copy of
the four pinned runtime-packaging files and Java tree was provisioned; all 458
file/symlink identities were verified against the shared prerequisites. The
corrected full mutant run passes all 50 tests. Scratch source was restored and
the distinguishing probe passes again.

## Complete input consumption and authority

Read `consume.py --index`, then complete entries 0-60 in exclusive-end chunks
[0,4), [4,6), [6,13), [13,23), [23,30), [30,47), [47,48), [48,57) and [57,61).
An outer response truncated entry 57; its complete single-entry read recovered
the missing text. Entries 58-60 were already visible. Entry 61 is machine-only
historical FAIL01.

Read every exact Review 05 supplement completely, reconstructed all eleven
complete patches and all 1,172 complete objects and expectations. The main
helper historical F1-F5 scope label does not restrict the current Review 05
authority. All current test code remained required. Resolved F1-F9 narratives
and controls were verified as hash-bound machine evidence.

Verified all 31 old approval bindings against preserved old read-plan, measure,
verify and approved-handoff snapshots in the F10 bundle. Verified current source
against the actual regenerated plan. Independently regenerated current metadata
into the owned evidence directory; every plan, audit, handoff,
schema/case/fixture projection matched canonical current metadata byte-for-byte.
All changed declarations, twelve schemas, 1,575 cases and 28 prior artifacts
pass verification.

The actual complete input contains 274,752 content bytes, 4,540 stream-label
bytes, 6,992 main-report/handoff bytes, 22,243 supplemental read bytes and
14,036 approval-metadata bytes. Total 322,563 read bytes yields 123,188
conservative proxy tokens including the 2,000-byte correction reserve and 15,000
named reserve tokens. F10 growth is 3,967 bytes; with the full 2,000-byte
reserve, 5,967 stays within the approved 6,000-byte bound. The approximate 100k
goal is a planning target, not an exact ceiling.

## Independently approved bounded follow-up inputs

The complete-source F11 follow-up allocation is independently approved for
planning in `followup-input-allocation.json`. It requires every current entry
0-60, the current main report and handoff, this complete report, the full
lossless UTF-8 projection and the exact complete guard patch. Resolved F10
narrative, controls, patches and proofs become machine-only bindings; all
current F10 test code stays required. Historical F1-F9, old/current authority
snapshots and prior projections remain hash-bound.

Only `model_reference_test.go` may grow, by at most 2,000 author bytes plus a
separate 2,000-byte reviewer-correction reserve from the current pinned
36,091-byte baseline. Production and all other authoritative inputs remain
unchanged. Preserve all prior tests and controls. Regenerate and verify every
final declaration/projection and remeasure actual complete inputs before
dispatch and verdict.

The follow-up must independently compile this exact guard fault, demonstrate
green original assertions, red added assertions and restoration, then review
complete Item 2.1 and rerun actual final focused, stock, package/CLI, configured
lint, ECMAScript, format and diff gates. Its reviewer must keep the historical
95-fault classification and all earlier controls valid. A fresh complete PASS is
required before Item 2.2. This planning approval is not an instruction to
implement while the parent researches the upstream-test approach or revises the
specification.

## Preservation and evidence

The detached scratch worktree started at Phase1 HEAD
ec487ed27111e61dba7d104c2579ef053e478a24 with a verified complete 48-file final
package/CLI overlay. Every mutation and added probe was confined there. All
owned sessions finished. Restored sources, untracked/ignored files, pinned
prerequisites, stashes, HEAD and process references were audited before exact
worktree removal. The worktree is gone, with no new commits and no stash
changes. Shared source and authority identities match the initial snapshot.

```text
Evidence root:
.tmp/agent/nextflow-conformance/phase2-schema-review06/
Finding: utf8-proof.json, utf8-patch-complete.json
Controls: utf8-inputs-complete.json, utf8-controls.json
Original, mutated and restored probes: utf8-*.log
Environmental failed attempt: utf8-full-regression-missing-prerequisites.log
Historical strength: mutation-results.json, mutation-summary.json
Current gates: gate-results.json, regression.log, ecmascript.log
Input authority: approval-bindings.json, input-consumption.json
Reconstruction: alphabet-reconstruction.json, eleven-patches-reconstructed.json
Actual final measurement: final-input-allocation.json, remeasured/
Bounded follow-up: followup-input-allocation.json
Preservation: final-integrity.json, scratch-cleanup.json
```

Production model SHA256:
79caf20ff4e77b64bbe1a09aee1f65b6f4ce66b1d5e4f2823cbef58480f2ea5b. Schema emitter
SHA256: b9cc0c4c87f863db81e2ba1b8655ed22b4be67377de23f90b1d135b672f8c625.
Specification SHA256:
8f510fd449d06fd24dcf4b0a657a9a6fc47e11471c89ff87547ea8d17a2b9cf5. Final
reference test SHA256:
014b1b07404d2a71e5221cfccd42ac3060f4afea292f17fc28cc550f2614b1e1.
