# Phase 1: Freeze and accept independent contracts

Ref: [charter.md](charter.md) sections A1-A3, B1-B2, C3 R-UAT-01 and
Experiment order and deadlines stage 1.

## Instructions

Use the `orchestrator` skill with `nextflow-implementor` and
`nextflow-reviewer`, under `/home/ubuntu/.agents/skills/`. Apply their shared
conduct, implementation, testing and Nextflow conventions to this research
experiment. Pipeline scaffolding, nf-test modules, containers and production
APIs are outside its scope. For bounded Python research scripts, read
`/home/ubuntu/.agents/skills/python-conventions/SKILL.md` for applicable code
quality conventions; reuse existing tools without creating a Python package.

Work in `/home/ubuntu/wr` on `nextflowdsl`. Retain artifacts in this directory
and use `.tmp/agent/nextflow-conformance/research-pilot/` for executable
scratch, staging and caches. Root owns checkbox transitions, delivery/status,
commits and pushes. Preserve the production lock, eighteen bootstrap batches,
code, core spec and six core phase plans; F11 remains deferred and Item 2.2
held.

Stage 1 has two active hours, including independent review. Start cumulative
time accounting here; all four stages together have eight active hours. Keep
one heavy workload active. Read the charter's artifact and deadline rules
before execution; they govern all subsequent phases.

## Items

### Batch 1 (parallel)

#### Item 1.1: A1-A3 - Freeze original contracts [parallel with 1.2]

charter.md sections: A1-A3, Research artifacts and boundaries.

Independently author original contracts, inventory, expectations, literal
fixtures and provenance contributions in scratch `contract-originals/`.
Freeze exact test/helper/runner/config/fixture spans and dependency edges.
Keep raw and genuinely decoded bytes, parser state and method/subcase order,
Mix lifecycle and completion, fresh/resume checks and propagation distinct.
Cover all eight P inputs, three M inputs and both workflow/check pairs.
Account for six methods, eleven Spock units, 29 parser plus nine Mix
predicates, four CLI invocations and four literal CLI predicates. Explicitly
record zero table rows and generated providers. Preserve types, all source
comparators, topic bytes and original aggregate outcomes, per A1-A3.
This supplies original-contract coverage for the one R-UAT-01 acceptance
test; runtime results are authored only in Phase 2.

- [x] implemented
- [x] reviewed

#### Item 1.2: B1-B2 - Reconstruct document contracts [parallel with 1.1]

charter.md sections: B1-B2, A2-A3, C2.

Independently author document facets, expectations, fixtures and provenance
contributions in scratch `contract-documents/`, without reading observations
or treating Item 1.1 as the document oracle. Freeze all B1 ranges and both
complete Mix includes. Record forms, types, ordering, counts, defaults,
warnings, feature conditions and unresolved outgoing semantic links.
Retain numeric original Mix inputs versus string document examples.
Define separate S-MIX, S-ARITY, S-TOPIC, G-IN, G-OUT and both G-SHAPE cases
with their source/document rationales. Review G-IN/G-OUT's intended count
violation and G-SHAPE's file/list distinction without promising exact error
text. Freeze nullable bytes and harness semantics for C2 controls only.
This supplies document/strengthened/gap coverage for R-UAT-01 and authored
expectations used by R-UAT-03 and R-UAT-04, three of the five research UATs.

- [x] implemented
- [x] reviewed

For parallel batch items, use separate subagents per item under the
`subagents` skill's shared concurrency limits. Each item writes only its
named staging directory. Launch one `nextflow-reviewer` subagent to review
both contributions together, returning a verdict per item.

### Batch 2 (after batch 1 is reviewed)

#### Item 1.3: C3 - Accept the hashed handoff

charter.md sections: Research artifacts and boundaries, C3 R-UAT-01.

Merge reviewed contributions into `research-manifest.json`, `contracts.md`,
`inventory.json`, `expectations.json` and `fixtures/`. Bind full/span hashes,
byte counts, Git blobs, modes, lines/offsets, tools, acquisition origins and
dependencies. Mark reused and acquired resources separately. Each typed
expectation needs a stable ID, comparator, value, boundary and rationale.
Inventory every predicate, helper, fixture, invocation and child disposition;
retain original, strengthened and documented-gap origins separately.

Hand these artifacts to an independent expectation/preservation reviewer.
Record acceptance, immutable hashes, review minutes and rounds in
`contracts-review.md`. Resolve disagreements from source/documents through
review. Finish the one R-UAT-01 test only on accepted, complete accounting.
Acceptance precedes every executable adapter. Phase 2 starts only after this
item is reviewed; an exhausted review deadline leaves the pilot incomplete.

- [x] implemented
- [x] reviewed
