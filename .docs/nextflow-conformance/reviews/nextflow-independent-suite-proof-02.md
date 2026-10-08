# Independent Suite Proofreading 02

Verdict: FIXED. Four grammar corrections preserve the existing requirements.
One textual ambiguity remains for the workflow owner to clarify.

## Scope and identities

- Reviewed only `.docs/nextflow-conformance/spec.md` as requirement material.
- Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`; owner: `/root`.
- Reviewer: `/root/nextflow_suite_proofread02`.
- Initial spec SHA-256:
  `ee8bb1be88a82ad0251888b1848926b9549fe59e1681b9a0ac82eb2e337f2e9c`.
- Final spec SHA-256:
  `c1db27a0b803695c482d47d58aa6880e4f56183b309200aaedecfdab3bde8f7a`.

The review followed agent-conduct, the delegated completion contract,
spec-proofreader, unslop and prose-principles. No feature description,
prompt, author report, feature report or other requirement source was read.
Linked local files were checked for existence only; their contents were
not read. No feature-coverage or technical-design verdict is implied.

## Exact corrections

1. C3 diagnostic grammar: replaced "Items are arrays of" with
   "`items` is an array of". This identifies the singular JSON member
   already defined by the closed diagnostic shape.
2. C3 parser-row location: replaced "in diagnostic value's" with
   "in the diagnostic value's". Added the missing article.
3. D2 child environment: replaced "key/value, except secrets are forbidden
   in bootstrap test environments." with "key/value. Secrets are forbidden
   in bootstrap test environments." Split two existing rules into sentences;
   secrets remain forbidden.
4. F3_01 replay counts: replaced "artifact replay has executed zero and zero
   new engine passes." with "artifact replay reports zero executions and
   zero new engine passes." Supplied the missing unit for the first count.

No acceptance ID, source identity, count, deadline, selection, scope decision,
expected value or command was changed.

## Textual ambiguity

E3_03 requires "the six frozen F1 Bash subjects". The spec does not define
that F1 label, and F1 also names the later milestone-seeding story. The pilot
references establish provenance generally but do not state which label this
phrase means. The workflow owner should identify the intended pilot label
in the text. No subject, source requirement or replacement label was invented.

## Completed checks

- Read the entire 2,466-line specification.
- Checked repetition, contradictions, undefined terms and prose quality.
- Confirmed six sequential sections A-F and 17 unique stories with matching
  section letters and sequential story numbers.
- Confirmed all 69 unique acceptance IDs are sequential within their stories
  and inside their matching story blocks.
- Confirmed all 17 story IDs occur in Implementation Order and no unknown
  story ID is listed there.
- Confirmed one h1, no skipped heading levels, named code-fence languages,
  ASCII outside code blocks, prose lines at most 80 columns, no trailing
  whitespace, no consecutive blank lines and a final newline.
- Confirmed Markdown reference labels resolve and local reference targets
  exist. Placeholder terms occur only as explicit invalid examples.
- Compared the saved initial bytes with the final spec: exactly four
  replacement hunks; all other spec bytes are unchanged.
- Checked this report against the same applicable Markdown mechanics.

This text-only review required no build, runtime test, network request or
heavy worker. No commits, pushes, nested agents or system installs occurred.
All owned commands completed; no owned live process, session or wait remains.
