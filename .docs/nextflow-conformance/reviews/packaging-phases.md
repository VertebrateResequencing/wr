# Runtime packaging phase update

Updated [phase 1](../phase1.md), [phase 3](../phase3.md),
[phase 4](../phase4.md), [phase 5](../phase5.md), and
[phase 6](../phase6.md) against the accepted [spec](../spec.md).
[Phase 2](../phase2.md) was inspected and remains unchanged; its source
extraction and independent semantic review scope still matches the spec.

## Changed plans

- Phase 1 now has sequential schema/CLI and acquisition handoffs, each
  requiring fresh implementation, independent review, and bounded-input
  approval. Both remain prerequisites for phase 2. The partial implementation
  handoff and initial bundle review are linked without claiming completion.
- Phase 1 covers packaging and nullable coordinates, provenance POMs, exact
  opaque runtime bytes, actual external execution inputs, revised archive
  safety and missing-runtime checks, and both new runtime acceptance tests.
- Phase 3 imports all 49 acceptance IDs and revised contracts with unchanged
  IDs and source provenance; unfinished records remain drafts.
- Phase 4 binds freshness to the full opaque distribution and actual external
  execution inputs; distribution changes exercise `D2_01` independently.
- Phase 5 invokes the unchanged distribution through its embedded launcher
  with enforced network denial. POMs remain provenance, and offline success
  proves the closure only for the seven bootstrap cases.
- Phase 6 requires all 49 acceptance IDs, actual acquisition and tamper proof,
  and oracle evidence bound to the exact distribution and execution inputs.

## Acceptance coverage

| Phase | Stories | Assigned acceptance IDs |
| --- | --- | --- |
| 1 | A1 | 7 |
| 2 | A2, B1, B2 | 12 |
| 3 | C1, C2 | 8 |
| 4 | D1, D2 | 9 |
| 5 | E1, E2 | 7 |
| 6 | F1, F2 | 6 |
| Total | All stories | 49 |

A bounded Python check extracted 49 unique numbered IDs from the spec and
verified each appears in its owning phase. `A1_06` and `A1_07` are assigned
to Item 1.2. Item 1.1 supplies shared schema/CLI checks without adding UATs.
The final gate retains seven actual oracle cases, 18 accounting mutations,
and three semantic observer mutations.

## Mechanical checks

All six phase files pass ASCII, 80-column prose, whitespace, blank-line,
heading, typed-fence, relative-link, and unfinished-text checks. Item counts
match 13 unchecked implemented/reviewed pairs. No completion boxes were
checked. All six retain roughly 100k-token input-bundle review, fresh
subagent handoffs, focused isolated-tooling tests, bounded commands, and the
`go-implementor`/`go-reviewer` skills. Old 47-UAT totals are absent.

`git diff --check` passes. These are plan and coverage checks only; no code
was changed, implementation tests run, completion claimed, or commits made.
Existing review records remain unchanged.
