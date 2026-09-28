# Nextflow restart progress

## Baseline

- [x] Preserve previous HEAD on codex/archive-nextflowdsl-2026-09-28.
- [x] Restore develop baseline in commit 0add8846.
- [x] Verify restoration tree equals develop f2888015 with git diff.
- [x] Record original request and agreed direction in prompt.md.

## Conformance foundation spec

- [x] Clarification rounds complete with fresh-agent NONE verdict.
- [x] Spec authored.
- [x] Two consecutive feature coverage reviews pass.
- [x] Two consecutive proofreading reviews pass.
- [x] Phase files created.
- [x] Each phase file receives a clean review.

## Subsequent milestones

- [ ] Implement and adversarially validate the conformance foundation.
- [ ] Expand and independently review the complete target inventory.
- [ ] Prove a dynamic runtime slice with real manager and CLI crash recovery.
- [ ] Implement dependency-ordered batches against executable UATs.

## Current handoff

Spec-writer workflow was completed and committed as 3dd25b15. During phase 1,
actual runtime acquisition exposed the archive contract conflict recorded
in blocker-phase1-1.1.md. Agent phase1_implement returned an incomplete
handoff in evidence/phase1.md. The corrected spec has passed two feature
reviews, two proofreading reviews, and all affected phase reviews. Next
action: approve a fresh input bundle and complete phase 1 Item 1.1, then
review it before resuming acquisition in Item 1.2. No oracle or runtime
conformance is claimed.

## Review evidence

- conformance_questions_1 returned NONE after repository and upstream research.
  No oracle execution or runtime conformance has been claimed.
- conformance_author_1 wrote spec.md with 47 acceptance tests. Mechanical
  checks passed. Feature review is pending.
- conformance_review_1 returned PASS. See reviews/feature-01.md. The reviewer
  checked release asset hashes and the source-level feasibility of the fair
  supervisor. Actual oracle execution remains an implementation gate.
- conformance_review_2 returned FAIL. See reviews/feature-02.md. The required
  import-error case cannot emit values, conflicting with the rule allowing
  zero observation lines only for a successful empty-channel case. Consecutive
  feature passes reset to zero.
- conformance_author_2 addressed F02-01 in C1/C1_04 and E1/E1_01. Mechanical
  checks passed; 47 acceptance IDs remain. Fresh feature review is pending.
- conformance_review_3 returned PASS on the revised spec. See
  reviews/feature-03.md. Consecutive feature pass count is one.
- conformance_review_4 returned PASS on the same revised spec. See
  reviews/feature-04.md. Consecutive feature pass count is two.
- conformance_proofread_1 returned FIXED for headings, formatting, TSV path
  spacing, and prose wrapping. See reviews/proofread-01.md. Proofreading pass
  count remains zero; technical contracts were preserved.
- conformance_proofread_2 returned PASS without changes. See
  reviews/proofread-02.md. Consecutive proofreading pass count is one.
- conformance_proofread_3 returned PASS without changes. See
  reviews/proofread-03.md. Consecutive proofreading pass count is two.
- conformance_phases created phase1.md through phase6.md. Coverage counts
  are 5, 12, 8, 9, 7, and 6 acceptance IDs respectively. Mechanical checks
  passed; each phase still requires its independent review.
- phase_review_1 returned FIXED. Phase 1 now clarifies schema ownership,
  failing tests before implementation, and schema validation commands.
  See reviews/phase-01-review-01.md. A clean re-review is pending.
- phase_review_2 returned FIXED. Phase 2 now clarifies draft case links,
  early scope checks, and later execution evidence dependencies.
  See reviews/phase-02-review-01.md. A clean re-review is pending.
- phase_review_1b returned PASS without changes. See
  reviews/phase-01-review-02.md. Phase 1 planning review is complete.
- phase_review_3 returned FIXED. Phase 3 clarifies early freshness checks,
  draft imports, comparison errors, and discovery settings. See
  reviews/phase-03-review-01.md. A clean re-review is pending.
- phase_review_2b returned PASS without changes. See
  reviews/phase-02-review-02.md. Phase 2 planning review is complete.
- phase_review_3b returned PASS without changes. See
  reviews/phase-03-review-02.md. Phase 3 planning review is complete.
- phase_review_4 returned PASS without changes. See
  reviews/phase-04-review-01.md. Phase 4 planning review is complete.
- phase_review_5 returned FIXED for oracle prerequisite review, contradictory
  expectations, and mutation outcomes. See reviews/phase-05-review-01.md.
  A clean re-review is pending.
- phase_review_5b returned PASS without changes. See
  reviews/phase-05-review-02.md. Phase 5 planning review is complete.
- phase_review_6 returned FIXED for view regeneration and seed accounting.
  See reviews/phase-06-review-01.md. A clean re-review is pending.
- phase_review_6b returned PASS without changes. See
  reviews/phase-06-review-02.md. Phase 6 planning review is complete.

## Environment findings

- Go and golangci-lint are available. Java and Nextflow are absent from PATH.
- Unprivileged user/network namespace creation failed with EPERM.
- Docker server 29.1.3 responds. A pinned, network-disabled container is a
  possible oracle environment, not yet provisioned or proven.

## Runtime packaging amendment

- [x] Reproduce rejection of the actual pinned distribution.
- [x] Independently inspect official bytes and upstream packaging code.
- [x] Author corrected opaque-runtime and extracted-archive contracts.
- [x] Two consecutive feature reviews pass on the amendment.
- [x] Two consecutive proofreading reviews pass on the amendment.
- [x] Affected phase files are updated and receive clean reviews.
- [ ] Resume runtime acquisition under the reviewed corrected contract.

The corrected spec has 49 acceptance IDs, adding A1_06 and A1_07. Source
evidence is in reviews/runtime-packaging-author.md; the blocked acquisition
is recorded in blocker-phase1-1.1.md. The first implementor returned
INCOMPLETE with a durable handoff in evidence/phase1.md. Five A1 fixture UATs
and current model tests pass under Go 1.27.1, but lint reports 148 findings
under Go 1.26.3 and schema/validation/acquisition work remains. No phase
checkbox may be marked from this partial work. Resume with fresh bounded
implementation bundles after amendment and phase review.

- packaging_review_1 returned PASS with independent artifact inspection.
  See reviews/packaging-feature-01.md. Amendment feature pass count is one.
- packaging_review_2 returned PASS without changes. See
  reviews/packaging-feature-02.md. Amendment feature pass count is two.
- packaging_proofread_1 and packaging_proofread_2 returned PASS without
  changes. See reviews/packaging-proofread-01.md and
  reviews/packaging-proofread-02.md. Amendment proofreading pass count is two.
- packaging_phase_update amended phases 1, 3, 4, 5, and 6. Phase 2 remains
  applicable without edits. Coverage is 49 IDs across the six phases.
- Amendment phase reviews passed for phases 1, 4, 5, and 6 on their first
  round. Phase 3 received a prose-only fix, then a clean second review.
  See reviews/packaging-phase-01-review-01.md,
  reviews/packaging-phase-03-review-02.md,
  reviews/packaging-phase-04-review-01.md,
  reviews/packaging-phase-05-review-01.md, and
  reviews/packaging-phase-06-review-01.md.
