# Independent Suite Author Revision 05

Verdict: CLARIFIED. E3_03 now identifies the pilot failure-propagation controls
and their exact inventory/result fields. Acceptance meaning is unchanged.

## Scope and identities

- Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`; owner: `/root`.
- Author: `/root/nextflow_suite_spec_author05`.
- Initial spec SHA-256:
  `c1db27a0b803695c482d47d58aa6880e4f56183b309200aaedecfdab3bde8f7a`.
- Revised spec SHA-256:
  `0ad8942ba1de97f644044ddf8ec9d8fda5001923c02ac7c8e37d2b8d2a826a0e`.

This revision addresses only [proofreading 02][proof]'s E3_03 ambiguity under
[the accepted prompt][prompt]. The author read agent-conduct, completion and
liveness, spec-author, go-conventions, unslop, writing-for-agents,
prose-principles and final-response. The current spec and accepted pilot
report were read in full. Pilot control records were inspected passively.

## Intended label and preserved controls

[The pilot report][pilot]'s failure-propagation section accepts six F1 shell
subjects. [Charter C2][charter] defines F1 as failure propagation: four bad
subjects and two valid counterparts. [The frozen inventory][inventory]'s
`control_contracts.subjects` enumerates them; [control results][controls]'s
`F1_subjects` retains their measured results. [Independent Phase 3
review][joint-review] accepts the same set and outcomes.

The six IDs in unchanged contract order are:

1. `F1-ARITY-FRESH-FAIL`
2. `F1-ARITY-RESUME-FAIL`
3. `F1-NULLABLE-FRESH-BYTES-FAIL`
4. `F1-NULLABLE-RESUME-BYTES-FAIL`
5. `F1-ARITY-VALID`
6. `F1-NULLABLE-VALID`

Their original aggregate exits remain `0,1,0,1,0,0`. Stronger gates reject
the same four bad subjects and accept the same two valid subjects. The label
names this existing pilot set; it does not refer to the spec's F1
milestone-seeding story. No subject, comparator, expected byte, fixture,
source identity or completion requirement changes.

## Exact spec change

Only E3_03's first line changes, becoming four lines with direct field/file
references. Every other input spec byte is identical.

```diff
-3. `E3_03`: Execute the six frozen F1 Bash subjects with the unchanged
+3. `E3_03`: Execute the six frozen pilot failure-propagation (F1) Bash
+   subjects from `control_contracts.subjects` in
+   [inventory.json](research-pilot/inventory.json), recorded as `F1_subjects`
+   in [control results](research-pilot/control-results.json), with the unchanged
```

## Completed passive checks

- One exact replacement hunk; reversing it reproduces the initial spec hash.
- All 69 unique acceptance IDs and 17 story IDs retain their exact order.
- All 49 original acceptance IDs remain present. Every obligation outside
  the clarified reference is byte-identical to the task input.
- All four proof02 grammar fixes remain present.
- Implementation Order and the appendix remain byte-identical.
- All 540 other saved documentation, package and module input paths retain
  their initial hashes, including pilot records, fixtures and expectations.
- Inventory and result subject IDs, contract order, aggregate exits,
  stronger verdicts and specific rejection lists agree exactly.
- Spec and report pass ASCII, final-newline, 80-column prose, whitespace,
  heading, fence-language and local/reference-link checks.

Evidence is in `author05/` beneath the revision scratch directory named in
the prompt. It contains the initial spec, preservation hashes, replacement,
exact diff, checker and `checks.json`. Run the passive checker from the
recorded worktree:

```bash
timeout 30s python3 .tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/author05/check.py
```

The change is textual provenance clarification, so no runtime/build test or
new behavioural test is required. Writes are confined to spec.md, this new
report and owned author05 scratch. No other file, commit, push, nested agent
or system installation was created. All owned commands completed; no owned
live process, background job, tool session or outstanding wait remains.

[proof]: nextflow-independent-suite-proof-02.md
[prompt]: ../prompt.md
[pilot]: ../research-pilot/pilot-report.md
[charter]: ../research-pilot/charter.md
[inventory]: ../research-pilot/inventory.json
[controls]: ../research-pilot/control-results.json
[joint-review]: ../research-pilot/phase3-results-review-01.md
