# Independent suite proofreading round 01

Verdict: FIXED.

Reviewed `.docs/nextflow-conformance/spec.md` as a standalone written
specification using spec-proofreader, unslop and prose-principles. No
feature description, prompt, feature-review report or author-review report
was read. Technical design and feature coverage were outside this review.

## Exact edit

At spec line 1826, replace `CLI-P1-P7 additionally check their 28` with
`CLI-P1-P7 also check their 28`. This applies prose-principles' plain-word
rule and preserves the additional CLI checks and their count. The final
spec differs from the archived author03 generation by this one word only.

## Checks

- Read the complete specification and checked repetition, contradictions,
  terminology and prose. Repeated claim boundaries serve distinct record,
  execution and acceptance contexts; no redundant obligation was removed.
- Mechanics passed: ASCII prose, 80-column wrapping outside fenced code,
  no trailing whitespace or consecutive blank lines, a final newline,
  named languages on every fence, one h1 and no skipped heading levels.
  Placeholder strings occur only as explicit rejection examples.
- Structure passed: sections A-F, 17 sequential stories under matching
  section headings, every story represented in implementation order,
  69 unique acceptance IDs with matching sequential list numbers, and
  every acceptance test inside its matching story.
- Markdown reference labels resolve. Both local pilot link targets exist;
  their contents were not read. No external links were fetched.
- The bounded Python validation returned the semantic PASS result and
  verified the exact one-word diff against the archived author03 generation.
  No code tests were needed for the prose-only edit.

## Ambiguities retained for the owner

- Spec lines 300, 1232 and 2326 refer to F11 and schema review 06 without
  a document path or defined provenance reference. The surrounding text
  explains the malformed-UTF-8 guard obligation, but the external finding's
  identity cannot be verified from this document alone. F11 is not one of
  this spec's stories. No provenance was invented.
- Spec line 1261 says an empty error observation declares `values: []`.
  C3 lines 1354-1361 define expected checked fields with a wrapper and
  values with `{mode, items}`. It is unclear whether C1's notation is
  shorthand or a competing literal shape. The empty-sequence requirement
  is clear; the representation was left unchanged for author resolution.

## Artifact identities

Input generation SHA-256:

```text
f38892df1d81d255295f77569b54df46926f94baf7b04adc8323b4dc6c4f959f
```

Final spec SHA-256:

```text
d64084a3dcf37184d9b98c2988476836e64ac3fcb776196a416d26cb86ee341e
```

## Completion

Only the authorized spec and this new report were written. No commit, push,
nested agent or background workload was started. Every owned command
completed; no owned tool session, child process, job or wait remains live.
