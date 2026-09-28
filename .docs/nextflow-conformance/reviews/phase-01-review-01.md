# Phase 1 review 1

Verdict: FIXED

Reviewed `phase1.md` against `spec.md` Implementation Order step 1,
Architecture, A1, the A2 selectors, and the D2 attempt input fields.

## Corrections

- Moved the initial failing-test instruction before implementation so the
  item's written order follows the required red/green sequence.
- Made closed-schema scope explicit: all eleven records, nested constraints,
  and schemas emitted from the same definitions. Valid fixtures exercise
  later record types; later stories still own semantic and execution gates.
- Removed the A1-only test-name filter, which skipped `model_test.go` checks.
  The bounded command now runs both new tool packages, including schema
  round trips, malformed-record rejection, and schema/decoder agreement.

## Verification

A1 is the only assigned story. All five IDs map to `TestUAT_<ID>` in
`conformance/source_test.go`. Item 1.1 is sequential and has both checkboxes.
The phase requires real pinned acquisition, independent hash and selector
review, transactional failure, and offline preflight before completion.
Full runtime and semantic gates remain assigned to later phases.

Checked ASCII, 80-column prose, headings, fences, trailing whitespace,
blank lines, and acceptance IDs. This is a plan review; no implementation
or runtime tests were executed.

## SHA-256 records

Original `phase1.md`:

```text
18c02aa0f2f01fb7854c398a05287d87d1af8d60e891cb3400bd7a7ba3f65912
```

Reviewed `phase1.md`:

```text
09877f92597061f865cbb34dfe570df3722c715abaf5a89f694b5621bfeb25c6
```

Reviewed `spec.md`:

```text
80a0fb713aedc6492e4b56432c4c319a71075c0d5bd90a8096a4ca4ecb6cff8e
```
