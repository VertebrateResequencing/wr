# Nextflow test coverage research status

## Current instruction

On 2026-10-08 the user asked whether Nextflow's tests exhaustively cover its
documented language and requested proper research before choosing the exact
approach. Research precedes spec revision and further implementation.

The current Item 2.1 schema review may finish independently. Item 2.2 waits
for researched conclusions and any resulting reviewed spec revision. Keep
accepted Phase 1 work and current source identities intact.

## Research checklist

- [x] Establish available upstream coverage evidence and language boundaries.
- [x] Assess assertion-preserving upstream test reuse and translation.
- [x] Evaluate how requirement coverage can be established and audited.
- [x] Synthesize findings with sources and explicit unresolved evidence.
- [x] Independently review the recommended approach before spec authoring.

Review 02 accepts the corrected sourced synthesis as a basis for a bounded
executable pilot. It does not accept an exact architecture or exhaustive
coverage. The reviewed experiment is under `research-pilot/`, using manually authored
neutral contracts before any general importer or six-phase specification
revision. Its Phase 1 immutable handoff has passed independent review.

The bounded initial assessment is
`reviews/nextflow-upstream-test-assessment-01.md`. It is evidence for the
research, not approval of an exact approach. Research notes are under
`research_notes/Nextflow test coverage research/`; the report belongs under
`reports/`. Queue owner: `/root`; branch: `nextflowdsl`; worktree: `.`.

Coverage-evidence notes are complete. They identify the official Syntax
specification and accepted ADR, JaCoCo configuration, CI lanes and exclusions.
They establish no exhaustive requirement-to-test coverage guarantee. Exact
release CI outcomes and numerical coverage reports were not obtained; those
limits remain explicit in the notes. Requirement-coverage notes are also
complete, with fifteen verified files, twenty-two exact spans and bounded
assertion analysis. They do not certify a whole-suite semantic omission.
Translation research is complete, with forty-one verified source files and
ten pilot spans. No proposed translation was executed; its runtime feasibility
remains unverified. The sourced report and independent review 02 are accepted.
The bounded pilot charter and all four execution plans passed their reviews.
The Phase 1 source-derived handoff passed review, accepting R-UAT-01 only.
Pilot execution is tracked in `research-pilot/execution-status.md`; genuine
prerequisite closure and original attempts passed independent review, accepting
R-UAT-02 for the finite group. Six Spock features and four original CLI checks
passed. Successful raw Spock values remain unobserved; no observer equivalence,
translation, full-language or wr pass is awarded. Manual mappings and controls
come next. Permission portability finding P01
is owned by root and must be resolved before fresh-checkout reuse.

## Candidate under investigation

The user proposed an independent suite that covers upstream scenarios,
corrects identified weaknesses, runs against real Nextflow and future wr,
then adds documented requirements not adequately tested upstream. Assess
this candidate before choosing the exact design.

Retain upstream behavioural purposes and separately account for deliberate
strengthening and spec-gap tests. Both engine adapters use independently
reviewed expectations. A Nextflow observation cannot silently define the
expected answer. Internal JVM, AST and mock assertions need reviewed
behavioural equivalents or explicit unresolved dispositions. The bounded
pilot must establish preservation and execution before broader adoption.

## Schema review handoff

Item 2.1 review 06 returned FAIL for one UTF-8 envelope test gap, F11.
Production decoding remains correct; F1-F10 are resolved. All fifty package
tests, seventeen focused tests, twelve schemas and 1,575 stock cases pass.
The guard-removal fault survives the original tests and is rejected by the
independent twenty-two-control probe. Review evidence and bounded correction
planning are in `reviews/nextflow-phase2-schema-review-06.md`. All owned
sessions completed and disposable scratch was removed after its risk audit.

The correction remains deferred while research determines the approach.
Item 2.1 is unreviewed; Item 2.2 remains on hold. Existing hash-bound parent
metadata will be preserved before any research-driven phase revision.
