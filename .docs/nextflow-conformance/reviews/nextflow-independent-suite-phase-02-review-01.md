# Independent suite Phase 2 review 01

Verdict: FIXED. Five plan gaps were corrected in [phase2.md](../phase2.md).
The corrected plan passes independent document review against the accepted
spec. This verdict grants no implementation, semantic, input-bundle or
execution acceptance. Item 2.1 remains implemented and unreviewed; all other
item marks remain unchecked.

## Authority

Review owner: `/root/nextflow_suite_phase02_review01`. Queue owner: `/root`.
Branch: `nextflowdsl`. Shared worktree: `/home/ubuntu/wr`.

Accepted spec SHA-256:

```text
57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c
```

Initial Phase 2 SHA-256:

```text
f2a9b0b4d415e08bb128e0438a4b701605012aad0a08f079a474d3b18fde7b56
```

Corrected Phase 2 SHA-256:

```text
b23734ddd8e13f5250deb8596dfa734c836da65ad2c8b912bd0681e116a07648
```

The review used the spec's Architecture, A2/B1/B2/B3 and Implementation Order;
the retained current CLI boundary; schema review 06 F11; the accepted pilot
report and separate report review; and other plans for dependency references.
Authoring reports supplied history only. The reviewer independently compared
ownership, subcases, ordering, authority and preserved inputs.

## Corrections

1. Item 2.4 required revised cross-record fixtures to pass through an
   implemented boundary without assigning implementation of that boundary.
   Item 2.3 supplied local shapes and deferred cross-record checks; Item 2.5
   supplied the later generation loader. Item 2.4 now explicitly owns revised
   suite cross-record checks through the current validation CLI, independent
   of Item 2.5's retained-generation/history loader. This makes the
   reconciliation prerequisite executable without reversing its dependency.

2. Item 2.16 required B3_02's public `verify` rejection but did not assign
   that accounting branch before Phase 4. The current CLI implements only
   acquisition and validation; `verify` returns `E_PREREQUISITE`. Item 2.16
   now owns read-only source-accounting/mapping checks through the existing
   public command, profiles and JSON/exit contract. B3_02/B3_04 controls must
   produce their intended diagnostics. Intact accounting awards no missing
   execution gate or bootstrap completion; Phase 4 adds event/freshness work.

3. Item 2.13 did not explicitly name the accepted pilot report and separate
   report review among frozen `suite.authority_inputs`. Both are now linked
   and bound, alongside original truth, snapshots, fixtures and provenance.
   Their authority is finite source accounting; new packaged runs need fresh
   evidence. The report's historical author-time pending statement stays
   distinct from the later accepted independent research verdict.

4. Items 2.14/2.15 assigned readiness owners but left the exact joint
   selection/dependency authority condition implicit. Item 2.15 now requires
   the dependency-lock file reference in `suite.authority_inputs` and both
   selection and dependency `review_id` values to be nonnull, independently
   accepted and current before ready execution handoff. Item 2.14's source
   reviewer owns selection authority; Item 2.15's dependency reviewer owns
   lock authority. Acquisition still accepts neither build nor engine success.

5. Exit conditions did not point to the separate foundation import owner.
   They now link Phase 3 Item 3.2's complete 69-obligation import and preserve
   the distinct whole-target, typed, JVM/plugin and durable wr milestones.
   The seventeen Phase 2 UATs do not discharge those later obligations.

## Acceptance and dependency checks

All sixteen items are continuously numbered. The ownership table contains
exactly seventeen distinct IDs from the spec, with matching test files:
A2_01-A2_04 belong to Item 2.9; B1_01-B1_05 to Item 2.11; B2_01-B2_03 to
Item 2.12; B3_01-B3_04 to Item 2.16; B3_05 to Item 2.4. Every item has both
implementation and review marks. The only checked mark is retained Item 2.1
implementation, exactly matching the initial plan.

The explicit Item 2.2 exception avoids a review cycle. New input approval
precedes its eleven F11 malformed/valid free-string pairs at the documented
block/catalog entry points. The isolated guard-removal fault must fail all
eleven rejection assertions while valid controls pass. Fresh correction and
complete-input acceptance precede Item 2.1 review; Items 2.3/2.4 follow. The
eighteen-schema reconciliation must pass before Item 2.5 or any extractor.
Historical eleven-schema Phase 1 acceptance, the twelve-schema amendment,
160 selections and eighteen bootstrap batches remain distinct and preserved.

A2's complete subcases have labelled prerequisites and a single closure owner.
The plan covers LF/CRLF/no-final-newline partitions; exact real selectors and
included snippets; real tree/external edges without fetching or selection
growth; independent reference ambiguity fixtures; all eleven review-input
categories; semantic-only edits, deletion and retained original bytes; three
generations and no-ops; forbidden bindings; cancellation, publication readers
and barrier-controlled changes; and read-only check-mode mismatch versus
malformed/history/source diagnostics. Independent original labels precede
extractor implementation and generated output cannot define expected truth.

B1 closes all five tests after independently authored original-source
obligations, including distinct defaults/errors, typed overloads, exceptions,
feature flags, placeholder/empty-corpus controls, link/cycle failures and
current review independence/freshness. Obligation-only and UAT-only edits must
retain review bytes and grant zero current accepted reviews. Full validation
waits for genuine linked drafts and production ledger/profile initialization.
B2 keeps both production decisions unresolved; exclusion and observation
subcases use isolated fixtures without creating runtime pass events.

B3 preserves all finite counts, original scalar types/comparators, literal
records, state/order/config, source edges and frozen hashes. Its six loss
kinds plus internal loss retain fixed required sets and exact missing origins.
The three analytical M1 witnesses keep original/strengthening outcomes
separate, and G-IN's facet loss remains incomplete. All named mapping,
internal, string-Mix, document-link and target dependencies remain pending.
Native success and resolved locations cannot fill neutral/document gates.

Every implementation and review handoff requires measured complete inputs,
independent approval within roughly 100k tokens including output, growth and
reasoning, and reapproval of changed or split inputs. Items remain sequential
apart from the stated F11 exception. Coherent sub-handoffs retain their UAT
owner and all must pass. Meaningful red/green proof, reviewed source/fixture/
expectation inputs, closed stock schema/decoder controls and source projection
review precede dependent use. Named source/selection and dependency reviewers
must accept current authority before genuine execution. Missing prerequisites
leave the affected item incomplete.

## Completed checks and artifacts

The independent checker passes seventeen checks after including this report.
It verifies exact spec/initial hashes, all seventeen ownership/test-file rows,
continuous numbering, checkbox preservation, valid primary story references,
dependency rows, critical gates, all eleven F11 locations and Markdown
mechanics. ASCII, one h1, heading order, 80-column prose, named fences,
whitespace, complete sections and local links pass. The spec independently
contains exactly 69 distinct numbered foundation acceptance IDs.

All 52 snapshotted authority/code/data inputs remain hash-identical, including
the accepted spec, Phase 1, both pilot reports, tooling code, current schema
fixtures, source lock and bootstrap batches. `git diff --check` passes for the
changed plan. No implementation tests, stock validators, engine attempts,
acquisitions or input-bundle reviews were run or claimed by this prose review.

Owned scratch artifacts are under
`.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/`
in `phase-review02-01/`: `check.py`, `checks.json`, `phase2-before.md`,
`preserved-input-hashes.json` and the exact `phase2-review.diff`.

Exact phase diff SHA-256:

```text
583a225b30dd057a85ab186001c219cd1bbcea5446aead850a035ba022e93422
```

Only Phase 2, this new report and owned scratch were written. Other plans,
spec/code/history and existing acceptance state were preserved by this worker.
No commits, pushes, nested agents or system installs occurred. All owned
commands completed; no live work remains.
