# Phase 1 contribution review 01

Item 1.1: PASS. Item 1.2: FAIL. One blocking fixture-identity finding remains
in Item 1.2. Neither verdict awards runtime, observer equivalence, oracle,
translation, wr execution or R-UAT-01 acceptance of the combined handoff.

## Scope and evidence

Reviewed the two independent staging contributions against charter A1-A3,
B1-B2 and the C2 frozen control requirements on branch `nextflowdsl` in
`/home/ubuntu/wr`. The pinned source is Nextflow 26.04.6, commit
`232b60569865e9a4577e48c1955409238359d6ca`.

Both author data checkers completed with their semantic data-only PASS output.
Original validation checked 106 source resources and 70 fixtures. Document
validation checked 17 source resources, 13 fixtures, 28 expectations and 34
facets. The original checker writes its results file; this review redirected
only that write to reviewer staging through `Path.write_text`, retaining the
checker code and assertions unchanged. Unexpected writes would raise an error.
No author contribution was changed. All 77 original and 40 document files in
the authors' hash lists still match their declared bytes and SHA-256.

The reviewer separately read selected source, helper, runner and document
spans, compared all frozen value predicates, inspected the four authored gap
workflows and derived all six control outcomes statically. The independent
static check detects the fixture-ID defect that the author checker misses.
It retains hashes of all 119 contribution files, including author records.
`facet-review.json` records a detection rationale and disposition for each of
the 34 facets. These are bounded contract assessments, not observer approvals.

Reviewer evidence is in the [review staging directory]. The retained files are
`original-checker.stdout`, `original-checker-results.json`,
`document-checker.stdout`, `independent-review.py`, `independent-review.stdout`,
`independent-results.json`, `facet-review.json`, `review-record.json` and
`evidence-manifest.json`.

No Nextflow, JVM, Groovy, Gradle, build, download, original Bash harness,
control Bash harness or executable adapter ran. Pipeline nf-test and nf-core
lint requirements do not apply to this charter's data-only research item.
No production, lock, bootstrap batch, core plan, status or checkbox changed.
Root owns correction routing, combined handoff acceptance and later execution.

## Item 1.1: Accepted original contracts

The inventory reconciles six methods, eight P units, three M units, 29 parser
and nine Mix predicates, two workflow/check pairs, four CLI invocations and
four literal CLI checks. Tables and generated providers are explicitly zero.
Seven helper observation obligations and 15 required completion records remain
separate from the selected 42 predicates. All outcomes are pending execution.

P1-P7 retain four independent predicates each. P8 retains count zero only.
Counts, locations and messages match the literal source. P6 retains substring
comparison; the other selected messages retain equality. P2's expected message
contains bytes `5c6e`, backslash plus n, rather than LF. All eight literal,
raw, decoded and source-derived normalized fixture representations are frozen
separately. The nested script strings and terminal twelve spaces are retained.

The selected source-derived expectations are sufficient for this Phase 1
freeze. Exact original expressions and source bytes remain the oracle; the
manually derived normalized inputs are expressly provisional runtime inputs.
The pending Java/Groovy `stripIndent()` dispatch check cannot silently replace
these inputs or expected diagnostics with observed CLI output. Phase 2 must
capture genuine literal decoding and the bytes passed to parse, preserving
any disagreement as a prerequisite or adaptation result.

One shared parser, original method/subcase order, `main.nf` naming,
parse/analyze sequence, SyntaxErrorMessage filtering, cause extraction and
line/column sorting are retained. Full parser/compiler support source is
frozen. Feature selection and actual JUnit ordering remain Phase 2 obligations.
No clean-per-unit parser or internal-to-CLI equivalence is assumed.

M1 retains numeric 1, 2 and 3, string a, b and z, and exclusion of c. The two
analytical witnesses with duplicate 1 and extra d satisfy its seven original
conditions. M2/M3 retain exact numeric sorted-list equality, including
multiplicity. Leading LF and indentation remain in Mix literals without
TestUtils normalization. Per-feature reset, five-second timeout, MockSession,
last result, normalization, network fire/await/destroy and session.error
propagation are retained. Loader session/mainScript assertions remain internal
helper obligations. Mock scriptlet success is not real shell proof.

The complete arity and topic source/checks, hidden expected bytes, runner,
config and ignore inputs are frozen. Arity's `set +e` permits fresh failure
followed by successful resume to yield original aggregate zero. Topic's
inherited errexit stops on failed engine invocation or final false after cmp.
Each reached check, engine exit and completion remains distinct from aggregate
status. Topic retains exactly 22 expected bytes and its final LF. Neither
workflow's resume invocation proves cache reuse. Declared container settings
remain distinct from actual execution mode and discovered configs.

Build and fixture declarations are frozen as dependency starting points.
Resolved transitive prerequisites, generated classes, compiler/test launch,
actual environment and internal observer equivalence remain pending. This is
the expected Phase 1 boundary and does not invalidate the source freeze.

## Item 1.2: Blocking finding R01

`fixtures-manifest.json` lines 80-136 and `inventory-contribution.json` lines
938-994 contain 13 fixture records but only 10 distinct fixture IDs:

| Reused ID | First member | Second member |
| --- | --- | --- |
| FIX-MIX-DOC | mix-doc.nf | mix-doc.out |
| FIX-NULLABLE-CONTROL | nullable-control.checks | nullable-control.expected |
| FIX-TOPIC | topic.checks | topic.expected |

The paths and hashes distinguish the files, but the contribution defines no
bundle/member identity convention. Every record presents its reused `id` as
its fixture identity. An ID-keyed handoff loses one member of each pair and a
missing-ID loss report cannot identify which required fixture was removed.
This conflicts with the charter's per-fixture identity/accounting obligation
and its independent fixture-loss controls.

Give each file a distinct stable ID in both fixture inventories, carry those
identities through affected references, and re-freeze affected artifact hashes.
A documented bundle plus distinct member identities would also satisfy the
requirement. Add a data-only uniqueness/reference check so the intact 13-file
inventory cannot pass with 10 fixture IDs. `verify.py` lines 144-171 currently
checks paths/bytes/modes but never verifies fixture-ID uniqueness. Re-review
the corrected contribution before accepting Item 1.2 or the merged handoff.

## Item 1.2: Other contract assessments

All six bounded document ranges and both complete Mix includes are frozen.
The 34 facets retain allowed/rejected forms, types, counts, ordering, warnings,
feature conditions and bounded absence of unspecified defaults. The five
outgoing links remain unresolved, including the unreviewed remainder of the
strict-syntax page. Typed workflow/topic examples remain outside this runtime
group; params/output flag exemption is recorded separately.

The document Mix example contains six strings and permits any order. Its
complete output include has no final LF; that illustrative byte sequence is
not an engine-output oracle. S-MIX contains three integers and three strings,
requires the complete six-item multiset and rejects both original M1 witnesses.
Exact multiplicity is justified by the document's emitted items and complete
example, with its strengthened origin retained. Variadic original Mix and
chained document Mix still need separate reviewed observer mappings.

S-ARITY requires both exits zero; S-TOPIC separately requires both exits zero
and both exact file bytes. Neither adds cache-reuse assurance. The arity
expectations' references to failure facets provide rationale, not detection
coverage. Valid-count success cannot detect absence of invalid-count checks.
Only G-IN/G-OUT attempt those specific failure facets. Topic's unique/sorted
file transformation can detect missing distinct version values but hides raw
multiplicity and order. The original and strengthened byte checks therefore
leave the full all-sent-values topic facet partially untested.

G-IN names one existing input file with arity two. G-OUT produces one exact
output file with arity two. Their comparator requires nonzero exit associated
with the named process, affected path and declared/actual counts. Neither
promises exact diagnostic text or accepts parse/tool/fixture failures.
G-SHAPE-FILE and G-SHAPE-LIST differ in declared arity 1 versus 1..* and
expect file versus one-element file list before display. Successful completion
and type-preserving observation remain required. Identical printed basenames
cannot satisfy that mapping. No observer execution pass is awarded here.

The six static F1 subjects have original aggregates 0, 1, 0, 1, 0 and 0, with
all four invalid stronger outcomes rejected and both valid outcomes accepted.
The specific stronger failures include both nonzero nullable engine exits and
the fresh byte mismatch in the third subject. Nullable retains four literal
expressions, tee status without pipefail, final-expression aggregation and
exactly `empty input\n\n`, 13 bytes. These are frozen Bash control
expectations; nullable typed DSL execution remains outside their claim.

## Review effort and completion

This is independent review round 1 for both items and all six contract groups.
The first recorded reviewer clock was 2026-10-08T11:49:01Z. Skills and initial
charter reads preceded that clock; the caller's stage start was
2026-10-08T11:34:06Z and its deadline is 2026-10-08T13:34:06Z.

Exact active review minutes were not measured. `review-record.json` records a
conservative per-group bound from stage start to the final review-record clock
for P, M, CLI, doc-strengthening, doc-gaps and controls. Those bounds overlap
and must not be summed or presented as measured group effort. The independent
script records exact static-check wall durations per group; those durations
are not human/agent review minutes. No second review round or runtime work is
included. The record also binds per-item verdicts and retained evidence hashes.

All reviewer commands completed synchronously. No child agent, background
process, tool wait or job remains live. Root should route R01 to correction
work and obtain a fresh Item 1.2 review before freezing the combined handoff.

[review staging directory]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/contributions-review01/
