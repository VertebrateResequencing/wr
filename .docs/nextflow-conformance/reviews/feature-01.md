# Feature Review 01

Verdict: PASS. No blocking feature-coverage or feasibility findings.

Reviewed `.docs/nextflow-conformance/spec.md` against
`.docs/nextflow-conformance/prompt.md` on 2026-09-28. This verdict concerns
the bounded foundation specification, not implemented conformance.

```text
spec SHA-256:
b18094a7c4dd91db1b42cb14472fa25e3ae3473ba7d5a9adec218a65c8946f1a
prompt SHA-256:
a080ae1a612102925776d0c2b18094a5ec2acaf67143a5116c36660d047d3acd
```

## Coverage findings

- `spec.md:5` and `spec.md:994` bound completion to the declared bootstrap.
  The final gate requires current evidence while reporting pending target
  inventory, unresolved policy, no wr adapter, and zero wr runtime passes.
- `spec.md:87`, `spec.md:359`, and `spec.md:386` anchor source accounting
  in the pinned tree and byte partitions. Fixed reviewed selectors prevent
  generated records from defining their own denominator.
- `spec.md:436` and `spec.md:455` require original-source semantic review,
  separate facets, bidirectional links, and independent reviews. Defaults
  and overloads cannot disappear merely because a paragraph stays mapped.
- `spec.md:525` and `spec.md:584` define observable expectations, constrained
  normalization, exact Go test discovery, and actual execution bindings.
  Draft runtime contracts remain incomplete and cannot become passes.
- `spec.md:627` and `spec.md:665` require execution events, artifacts,
  deadlines, content hashes, effective environments, and evidence replay.
  Newer failures supersede earlier passes with the same inputs.
- `spec.md:818` and `spec.md:844` specify 18 accounting mutations and three
  semantic observer mutations. An unrelated error cannot kill a mutation.
- `spec.md:773` and `spec.md:876` retain missing wr bindings, policy
  decisions, and all nine runtime seeds. The durable dynamic slice remains
  a dependency of broad operator implementation.
- `spec.md:942` defines bounded handoffs and a durable evidence ledger.
  Generated checkboxes require current evidence rather than editable
  completion assertions.

## Pinned-oracle feasibility

The fair supervisor has source support. At the pinned commit,
`TraceFileObserver.onTaskComplete` writes and flushes each completed task
record before workflow shutdown. `TaskProcessor.fairBindOutputs0` buffers
an out-of-order task and returns without waiting for the earlier task.
Together these support releasing A after observing B's completion while
requiring downstream A,B emission. This is a source-based feasibility
finding, not evidence that the proposed oracle case has run.

- [Pinned trace writer][trace]
- [Pinned fair output implementation][processor]

The release API independently reports the launcher and distribution SHA-256
digests recorded at `spec.md:81`. Actual asset download and hashing remain
required by `A1` and `E1_01`. See the [release metadata][release].

The pinned parser contains a specific unsupported-import diagnostic, and
the pinned file collector contains a specific missing-output diagnostic.
Those paths support the two negative oracle contracts. Exact observed
formatting and task-script exit evidence must still be captured and
reviewed as required at `spec.md:766`; any nonzero exit is insufficient.

- [Pinned import diagnostic][parser]
- [Pinned missing-output diagnostic][collector]

No Java or Nextflow executable was available on PATH during this review.
No oracle execution, downloaded distribution verification, or network
isolation proof is claimed. These remain explicit implementation gates at
`spec.md:714`, `spec.md:783`, and `spec.md:987`. Their absence during a spec
review does not establish that the requirements are externally impossible.

[trace]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/trace/TraceFileObserver.groovy
[processor]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/processor/TaskProcessor.groovy
[release]: https://api.github.com/repos/nextflow-io/nextflow/releases/tags/v26.04.6
[parser]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/main/java/nextflow/script/parser/ScriptAstBuilder.java
[collector]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/processor/TaskFileCollector.groovy
