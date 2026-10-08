# Phase 3: Measure neutral projections and paired controls

Ref: [charter.md](charter.md) sections B2, C1-C3 R-UAT-03 and R-UAT-04,
and Experiment order and deadlines stage 3.

## Instructions

Use the `orchestrator` skill with `nextflow-implementor` and
`nextflow-reviewer`, under `/home/ubuntu/.agents/skills/`. Apply
[Phase 1 instructions](nextflow-pilot-phase1.md#instructions) and start only
after Item 2.2 is reviewed. This stage has two active hours within the
eight-hour execution cap. Items are independent with separate outputs;
serialize executable workloads even when authoring/review runs in parallel.

## Items

### Batch 1 (parallel)

#### Item 3.1: B2, C1 - Review and measure mappings [parallel with 3.2]

charter.md sections: A1-A3, B2, C1, C3 R-UAT-03.

Investigate only small manual neutral projections and observers for the
accepted contracts. Review each mapping against pinned source, independent
expectations and genuine original captures. Write review arguments, defects,
disagreements, minutes and rounds in `mapping-review.md`; require reviewed
observers for attempted projections/gaps and review before equivalence.
Parser count, line/column and message need separate mappings. Preserve typed
Mix values, multiplicity and completion, literal fresh/resume outcomes and
original aggregate versus strengthened status. Internal AST, JVM identity
and mock claims require equivalence arguments or unresolved/internal records.

Write `projection-results.json` with every attempted outcome and all charter
capture metadata. Limit each projection/gap launch to two minutes. G-IN and
G-OUT must identify their input/output and declared/actual arity count;
unrelated failure cannot pass. Both G-SHAPE cases need a type-preserving
observer, rather than identical printed names. Preserve fixture bytes and
v2 with typing disabled. Record failed/unresolved mappings with causes;
stop runtime mappings whose genuine prerequisites are unavailable.

Cover the one R-UAT-03 test with distinct original-only, partial,
unresolved/internal, strengthened, documented-gap, oracle and pending-wr
claims. Nonexecuted mappings and unavailable originals earn no execution,
oracle or translation pass. A supported wr DSL boundary remains pending;
award no wr execution pass.

- [ ] implemented
- [ ] reviewed

#### Item 3.2: C2 - Reproduce all paired controls [parallel with 3.1]

charter.md sections: A2-A3, C2, C3 R-UAT-04.

Write `control-results.json` with independently reproduced six F1 subjects
using byte-identical arity/nullable checks under `bash -ex .checks` in
isolated layouts. Bound each Bash subject at ten seconds. The four bad
subjects must produce original aggregate exits 0, 1, 0, 1 in charter order;
the stronger gates reject each specific failing invocation/predicate.
Both valid counterparts pass original and stronger gates. Preserve nullable
pipeline/byte expressions, tee status without pipefail, `set +e`, aggregate
rule and exact 13-byte fixture. These are harness-only outcomes.

In isolated copies, remove each P1-P8/M1-M3 unit, each of the 38 Spock and
four CLI predicates, each selected fixture, each resume invocation and each
required completion record, one at a time. Separately mutate topic's final
newline and one of nullable's two terminal newlines. Each applicable loss
must reject through its matching identity/accounting/byte/completion gate
with the missing ID or changed hash; every intact counterpart passes.
Record applicability and unavailable execution separately; generic command
or infrastructure failure cannot substitute for a control rejection.

Evaluate both M1 seven-item witnesses against original predicates and S-MIX:
original accepts both; strengthening rejects duplicate 1 and extra 'd'.
A six-value permutation passes both. Retain analytical/control origin and
all hash-linked captures and charter metadata. Complete the one R-UAT-04
test only when all six subjects and all applicable loss controls have their
paired expected results. Unfinished controls leave the pilot incomplete.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item under the
`subagents` skill's shared concurrency limits. Item 3.1 owns mapping and
projection records; Item 3.2 owns control records. Launch one
`nextflow-reviewer` subagent to review both items together, returning a
verdict per item. Phase 4 requires the reviewed results or explicit
incomplete/cause records. An incomplete control record cannot award
R-UAT-04; measured prerequisite/mapping failure retains its execution verdict.
