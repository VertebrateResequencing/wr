# Phase 03 Review 01

Verdict: FIXED

Reviewed: `phase3.md` against `spec.md`.

Before SHA-256:
`6d46b646e1a34bca47061ea1d8312988355d2c69e8870fb59312b824331dfb4e`

After SHA-256:
`61080170a33c7827232a2e7426102108a302f13f5b7eaf24b405e81b0269bd43`

Spec SHA-256:
`80a0fb713aedc6492e4b56432c4c319a71075c0d5bd90a8096a4ca4ecb6cff8e`

## Corrections

- Made C1's early expectation-review hash check explicit so C1_02 can pass
  before D2 implements complete attempt freshness. Listed its three exact
  corruption diagnostics and required fresh review of changed bindings.
- Specified draft schema fields for imported, unfinished acceptance tests
  so importing all 47 records cannot award execution or review credit.
- Made C1_03's comparison cases and C1_04's two error fixtures explicit.
  Each error fixture checks an empty value sequence and the correct task
  contract. Diagnostic replacement and extra raw values fail independently;
  fixture review cannot replace later review of actual oracle diagnostics.
- Made C2's early selector execution explicit before D1's complete runner.
  Discovery and execution share build settings; execution records actual
  argv and uses uncached JSON test output. Listed missing-test, discovery,
  and evidence-kind diagnostics with their specified exits.

## Validation

The specification contains 47 unique numbered acceptance IDs. Both assigned
stories and all eight C acceptance IDs map to their specified Go test files.
Items 3.1 and 3.2 retain sequential review dependencies and both checkboxes.
Foundation comparison fixtures remain separate from E1's real oracle proof.
Partial discovery remains incomplete and does not imply execution success.

ASCII, prose width, named fences, whitespace, numbering, and local links
pass. Focused conformance/CLI tests and lint remain required by the phase;
the final foundation gates remain in later phases. No implementation tests
ran during this document review.
