# Feature Coverage Review 03

Verdict: PASS

Reviewed the complete prompt and specification independently against
`spec-reviewer`, `go-conventions`, `implementation-principles`, and
`testing-principles`. No prior verdicts were read. The review covers the
declared foundation milestone, including its retained later milestones.

## Inputs

SHA-256 of `.docs/nextflow-conformance/prompt.md`:

```text
a080ae1a612102925776d0c2b18094a5ec2acaf67143a5116c36660d047d3acd
```

SHA-256 of `.docs/nextflow-conformance/spec.md`:

```text
15c9b78860dd205e9d454a996d528cfee596dd6df15e81ba063123ba45ecfd86
```

## Coverage findings

No blocking feature-coverage findings.

- Lines 5-21 and 1007-1014 define a bounded bootstrap completion claim and
  require explicit pending inventory, unresolved decisions, and zero wr
  runtime passes.
- Lines 70-125 and A1 cover pinned release, source, dependencies, runtime,
  environment, offline verification, failed acquisition, and changed bytes.
- A2 preserves complete selected files, nested semantic units, includes,
  grammar alternatives, and unclassified content. Its independent fixtures
  and deletion controls test accounting beyond headings.
- B1 requires original-source review, separate behavioural facets, reviewed
  nonrequirements, complete links, and current independent review. B2 keeps
  typed semantics and JVM/plugin policy visible as unresolved decisions.
- C1 specifies machine-checkable observations, limited normalization, and
  generated views. C2 requires exact active test discovery and prevents
  fixture or oracle evidence from satisfying runtime bindings.
- D1 and D2 cover executed tests, skips, failures, timeouts, incomplete event
  streams, artifacts, input changes, stale evidence, and newer failed runs.
- E1 requires seven actual offline oracle cases, artifact and task evidence,
  and demonstrated completion order distinct from emission order. Oracle
  disagreement remains unresolved work, and no adapter can be simulated.
- E2 requires all 18 accounting corruptions and three observation mutations
  to fail for their intended reasons, starting from passing baselines.
- F1 retains all nine wr requirements, unsupported-semantics errors, and the
  durable runtime prerequisite for broad operator implementation. F2 covers
  bounded handoffs, dependencies, original source excerpts, and generated
  checklists that revalidate evidence.
- Lines 27-68 keep domain logic in a public Go package and the executable
  as wiring. Lines 583-588 and 1072-1076 require observable GoConvey tests
  for every acceptance ID. Actual oracle cases supply integration coverage.

## Empty observations and expected errors

Lines 513-527 require checked empty values and replay of raw observations.
Lines 757-766 distinguish the successful empty-channel contract from both
error contracts. Each error must satisfy its diagnostic, task, and workflow
exit checks; the missing-output case also proves script exit 0.

C1_04 independently replaces each error diagnostic with a missing-Java
failure and adds an unexpected value. Both corruptions must fail comparison.
E1_01 separately requires the actual import and missing-output oracle runs
to satisfy those contracts. The revision therefore accepts legitimate zero
observations without accepting missing prerequisites or unrelated failures.

This is a specification coverage verdict. It does not claim that the
foundation has been implemented or that an oracle has been executed.
