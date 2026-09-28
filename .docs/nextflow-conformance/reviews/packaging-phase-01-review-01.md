# Packaging phase 1 review 1

Verdict: PASS

Reviewed `phase1.md` against `spec.md` Implementation Order step 1,
Architecture, A1, the A2 bootstrap selectors, and D2 attempt input fields.
No phase changes were needed. All implementation and review boxes remain
unchecked.

## Findings

- A1 is the assigned story. Items 1.1 and 1.2 split shared schema and CLI
  work from acquisition, with an independent review between them. Item 1.2
  owns all seven A1 acceptance IDs in `conformance/source_test.go`.
- Item 1.1 requires all eleven closed schemas, emitted schema agreement,
  artifact packaging, nullable Maven coordinates, actual-byte references,
  dependency cycles, and the single opaque runtime closure.
- Item 1.2 preserves the exact 42,355,106-byte pinned distribution without
  member extraction. Extracted-archive checks remain separate. POMs are
  dependency metadata; external execution inputs are actual acquired files.
- A1_06 covers real distribution acquisition and offline validation. A1_07
  covers separate shell-prefix and shaded-JAR corruption, zero requests,
  no Nextflow start, and rejection of packaging or lock-hash bypasses.
- The plan requires meaningful failing commands, transactional acquisition,
  all seven A1 results, measured identities, exact selector review, and
  independent review before phase 2. Real oracle execution remains in E1.

## Verification

Story and acceptance references, sequential dependencies, item numbering,
unchecked implementation/review boxes, local links, ASCII, 80-column prose,
heading levels, fence languages, whitespace, and blank-line checks passed.
This review did not execute acquisition or implementation tests.

## SHA-256 records

Reviewed and unchanged `phase1.md`:

```text
e5c53c192330c5f3c04916bbb6255ea2faa8369d81fed06c69b3236a03bac14e
```

Reviewed `spec.md`:

```text
553c985cdd244a69ef3d76f35aa9658c1c3cfd017741c146f02d4f73b50ba7be
```
