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

Spec-writer workflow complete. Six reviewed phase files cover all 12 stories
and 47 acceptance IDs. Next action: orchestrator implements phase 1 item 1.1
with a fresh implementor and independent reviewer. No oracle execution or
runtime conformance is claimed.

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
