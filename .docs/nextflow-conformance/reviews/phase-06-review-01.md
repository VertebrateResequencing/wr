# Phase 06 Review 01

Verdict: FIXED

Reviewed: `phase6.md` against `spec.md` using `phase-reviewer`.

Before SHA-256:
`bb20c2e920a796a946c784c70e38f60a9bacd5eab8ef19436f86adeeecc1b523`

After SHA-256:
`386f958fd12515903def13f9f294e1e1bcf0e615ce88cc1b597fc59bd43a659a`

Spec SHA-256:
`80a0fb713aedc6492e4b56432c4c319a71075c0d5bd90a8096a4ca4ecb6cff8e`

## Findings and fixes

- The final command sequence checked generated views without explicitly
  regenerating them. Added bounded render commands before the gate and
  after the new attempt, before final verification and the view check.
  Evidence-derived checkboxes must reflect the current attempt.
- F1's seed instructions omitted their explicit origin, required scope,
  and separation from the foundation execution denominator. Added those
  constraints while retaining membership in the target ledger and runtime
  profile.
- Remaining runtime and scope obligations were implicit. Added supported
  invocation modes, crash receipts and logical task IDs, future actual wr
  implementation mutations, and the exact two unresolved policy IDs.

F1 and F2 map to their correct source and test files. All six F acceptance
IDs appear, and item 6.2 follows independent review of item 6.1. The final
gate retains all 47 numbered acceptance tests, seven real oracle cases,
18 accounting mutations, and three semantic observer mutations. It requires
current independent bootstrap reviews, byte-complete extraction, hashes,
generated ledger and handoffs, and no missing or stale foundation evidence.
Both later profiles must remain incomplete with zero wr runtime passes.

## Validation

A bounded document check passed acceptance-ID counts, F story mapping,
continuous item numbering, both checkboxes per item, all 12 bounded final
commands, ASCII, prose width, whitespace, named fences, heading levels,
and local links. The final wrap-only edit preserved these checks.

No implementation tests ran during this document review. Focused tooling
checks remain the implementation gate; full unrelated wr suites remain
outside the caller's scope. The spec and other phases were not edited.
