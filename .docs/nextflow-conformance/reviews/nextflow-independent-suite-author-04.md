# Independent suite foundation author clarification 04

The two ambiguities retained by [proofreading round 01][proof] are resolved
in [spec.md][spec]. F11 now identifies its source and finding title; C3's
closed neutral grammar governs the empty-emission notation. All 69
acceptance bodies remain unchanged, including all 49 original obligations.
This clarification awards no implementation, runtime or fresh-checkout
acceptance.

Owner: `/root/nextflow_suite_spec_author04`. Worktree: `/home/ubuntu/wr`.
Branch: `nextflowdsl`. Revision owner: `/root`. Date: 2026-10-08.
Applied spec-author, agent-conduct, go-conventions, testing-principles,
unslop, prose-principles and writing-for-agents. Only the spec, this report
and owned `author04` scratch were written. No commit or push was made.

## F11 provenance

Read the actual [schema review 06][schema-review]. Its finding is F11,
"Assert malformed UTF-8 inside locally valid strings", rather than a story
in this specification. The new spec reference names that finding and links
the source report. Every other F11 reference now resolves through this
definition.

The review reports correct production rejection and weak existing tests.
Its eleven malformed/valid free-string pairs use `61 FF 62` versus
`61 EF BF BD 62` inside otherwise valid serialized JSON strings. The
required rejection must fail under the isolated UTF-8 guard-removal fault.
The architecture and B3_05 retain that correction, supported entry points
and independent-acceptance hold before extraction. No production correction
or approval is inferred from the provenance link.

## Empty emission representation

C1 now names C3's closed neutral observation grammar as authoritative.
The existing suite-record contract already delegates expected and observed
types to C3. Native-original records retain their separate contract.

C3 gives the exact expected `values` field for zero emissions:

```json
{"check": true, "value": {"mode": "sequence", "items": []}}
```

The observed `values` field is:

```json
{"mode": "sequence", "items": []}
```

An emitted empty list instead has this observed field:

```json
{"mode": "sequence", "items": [{"type": "list", "items": []}]}
```

Its expected form uses the same checked wrapper. It fails comparison with
zero emissions. These are field examples; all other required record fields
remain present. Prose and E1 table notation `values []` means zero
emissions. A bare `values: []` field is invalid under the closed grammar.

The checked empty sequence remains distinct from not-applicable. Zero value
lines still require all declared error, task, exit and artifact assertions.
C1_04's missing-Java and extra-OBS failures, C3's tag distinctions, E1's
error contracts, raw-receipt replay and read-only authored expectations are
unchanged. No runtime output defines truth or supplies heuristic coercion.

## Preservation and checks

The bounded passive checker verifies all 69 acceptance bodies against the
exact proof01 generation and all 49 original bodies against the archived
pre-revision authority. IDs, sequential numbering and 17 story headings
remain unchanged. The exact revision changes only F11's architecture
definition, C1's observation narrative, C3's empty-value examples and the
new reference definition. All other spec bytes, including proof01's `also`
edit, F3's accepted IPC/fixture architecture, six-phase order and unfinished
boundaries, remain unchanged.

Archived authorities and reviewed non-spec inputs retain their recorded
hashes. The owned preservation inventory also verifies 628 non-spec files.
ASCII, 80-column prose, named fences, headings, reference resolution,
whitespace and final-newline checks pass; `git diff --check` passes.
The three new spec JSON examples decode exactly and distinguish zero
emissions from one emitted empty list. These are passive authoring checks;
no implementation test or engine attempt was run.

Evidence is retained under:

```text
.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/author04/
```

Input spec SHA-256:

```text
d64084a3dcf37184d9b98c2988476836e64ac3fcb776196a416d26cb86ee341e
```

Final spec SHA-256:

```text
ee8bb1be88a82ad0251888b1848926b9549fe59e1681b9a0ac82eb2e337f2e9c
```

## Completion

No authoring blocker remains. Root owns fresh consecutive feature reviews
followed by two clean proofreading reviews. Earlier feature PASS results
remain bound to `f38892d`; they do not approve this generation. Prompt,
plans, code, checklists, locks, pilot files, earlier reports and archives were
not edited. Every owned bounded command completed. No nested agent,
background workload, engine, build or container was started. No owned live
process, tool session, job, child or outstanding wait remains.

[spec]: ../spec.md
[proof]: nextflow-independent-suite-proof-01.md
[schema-review]: nextflow-phase2-schema-review-06.md
