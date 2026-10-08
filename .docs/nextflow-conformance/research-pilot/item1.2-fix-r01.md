# Item 1.2 correction R01

R01 is corrected in the document contribution. The data gate passes with
13 distinct per-file fixture IDs. Fresh independent review remains pending.
This correction awards no runtime, observer, oracle, translation or handoff
acceptance pass.

## Identity change

Both fixture inventories now distinguish the six members of the three pairs:

| File | Stable fixture ID |
| --- | --- |
| mix-doc.nf | FIX-MIX-DOC-NF |
| mix-doc.out | FIX-MIX-DOC-OUT |
| nullable-control.checks | FIX-NULLABLE-CONTROL-CHECKS |
| nullable-control.expected | FIX-NULLABLE-CONTROL-EXPECTED |
| topic.checks | FIX-TOPIC-CHECKS |
| topic.expected | FIX-TOPIC-EXPECTED |

Expectation and control references resolve each fixture ID with its file
path. Their inventory counterparts carry the same references. Manifest
fixture artifacts carry the corresponding ID. The gate rejects collisions,
missing IDs, wrong reference paths and disagreement between inventories.
There is no bundle identity convention or shared ID.

## Evidence

The [correction staging directory] retains the complete old 41-file document
contribution under `before/contract-documents/`, including its original author
record. `before/hash-inventory.json` retains all 119 original reviewer input
identities and matches the old review's input inventory. The 78 files of the
original contribution remain unchanged. Old review evidence is unchanged.

The bounded `red-check.py` command exited 1 before contribution edits. It
reported 13 records and 10 IDs in each inventory and named all six duplicate
records. `red.stdout` and `red.exit` retain that result.

The bounded full `contract-documents/verify.py` command exited 0 with semantic
PASS. `green.stdout` and the refreshed `checks.json` record 71 resolved fixture
references, 17 source resources, 13 fixtures, 28 expectations, 34 facets,
10 decisive document statements, six static F1 subjects and 38 artifacts.

`probe-fixture-identities.py` passed one intact isolated counterpart and
31 deliberate invalid copies with their specific expected rejection reasons.
These cover all three collisions in each inventory, loss of each fixture ID,
a missing counterpart ID, missing or wrong expectation/control/manifest
references, reference disagreement and required input/control reference loss.
Each invalid copy retains its own stdout and stderr under `cases/`.
`identity-probes.json` records the semantic results. These are JSON identity
checks, not runtime executions of fixture-loss controls.

`prove-preservation.py` passed. All 13 fixture byte sequences and modes, all
17 source span byte sequences and modes, all 34 facets and all 28 authored
expectation values remain unchanged. Control semantics, unresolved links,
observer conditions and dispatch conditions are retained. Only identities,
references, validation and their current hash records changed.

Python syntax checks passed for four owned scripts. Ruff and pyright are not
installed in this environment and were not run. No dependency was acquired.
No Nextflow, JVM, Groovy, Bash original/control harness, build, download or
adapter ran. No child, background process or live tool session remains.

## Effort and ownership

`start.json` records the first correction clock at 2026-10-08T12:02:27Z and
actual snapshot/correction start at 2026-10-08T12:02:46.978156Z. Initial skill
and assigned document reads preceded those clocks. The current author record
and `correction-record.json` record the actual end and measured elapsed
correction interval. This interval excludes the initial unmeasured reads;
review effort is zero and fresh review remains root-owned.

Stage 1 began at 2026-10-08T11:34:06Z and ends at 2026-10-08T13:34:06Z.
All owned correction checks completed before that deadline. Root retains
ownership of queue routing, deferred F11, Item 2.2, combined acceptance,
status, phase checkboxes, commits and pushes.

[correction staging directory]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/fix-r01/
