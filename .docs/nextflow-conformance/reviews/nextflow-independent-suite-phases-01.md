# Independent suite phase-plan revision 01

Authoring complete for independent review of each of the six phases. This is a
plan revision, not an implementation acceptance. No spec, production code,
checklist, source lock, bootstrap batch or frozen pilot record was changed by
this worker. No commits, pushes, nested agents or system installs were
performed.

## Authority and retained state

Branch nextflowdsl, shared worktree /home/ubuntu/wr, Git HEAD
cad2b64d9702cd09c172630df72c736400dc5cac and queue owner /root. Accepted spec
SHA-256:

```text
57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c
```

Phase 1 at ec487ed2 retains both implemented/reviewed item pairs. Its complete
Items and Exit conditions record remains byte-identical to
authority-before-phase-plan-revision, with this suffix hash:

```text
a471f284dd655559d0880a558c1f79f8a297131c97fbc263e879e888005d33eb
```

Item 2.1 retains implemented and unreviewed. No new checked mark was awarded.
Item 2.2 corrects F11 as an explicit prerequisite while Item 2.1 review is open;
it has no dependency on that review. Its fresh correction/input acceptance
permits closing Item 2.1. Item 2.3 then adds six suite schemas, and Item 2.4
owns B3_05 acceptance of the eighteen-schema reconciliation before Item 2.5
loader/extraction. The historical eleven and unfinished twelve-schema records
remain distinct.

## Plan hashes and exact diffs

The exact diffs compare the root-owned pre-plan authority snapshots with the
final files. Hashes and full 69 binding/dependency records are also in
checks.json in the worker scratch directory.

### Phase 1

[Plan](../phase1.md), 2 items, 7 UATs, 10176 bytes.

```text
plan sha256 5e3218e13bf9393fa5cb42b3492183fd5a50df7b73ee0c012ca2442e0b1c590c
diff sha256 6213df9d2a20582f5603406dddac0cff216193cabc4ae92fa941371b6f469b83
```

Exact diff in the worker scratch root: phase1.md.diff.

### Phase 2

[Plan](../phase2.md), 16 items, 17 UATs, 32867 bytes.

```text
plan sha256 f2a9b0b4d415e08bb128e0438a4b701605012aad0a08f079a474d3b18fde7b56
diff sha256 540c53c5674f0c1c81b015e750407b5afb5bffe85a989166f94b9b1666a51a96
```

Exact diff in the worker scratch root: phase2.md.diff.

### Phase 3

[Plan](../phase3.md), 6 items, 12 UATs, 15775 bytes.

```text
plan sha256 379f1528d12ea6d84a3da026f5985750c513039b68d1a74bbad0adff9b37e852
diff sha256 e648673a8b4e917ecab66d59e20b8be169ae3136d417be3204c98c79b4561f99
```

Exact diff in the worker scratch root: phase3.md.diff.

### Phase 4

[Plan](../phase4.md), 5 items, 12 UATs, 15815 bytes.

```text
plan sha256 3e4f11c67173391844d7eb1a62bc8ed5def423b5f4ca080dde8fb52871d3a358
diff sha256 d29a39e35ae708f222b2107d692f2c100208f5f823efba4209b584241232f109
```

Exact diff in the worker scratch root: phase4.md.diff.

### Phase 5

[Plan](../phase5.md), 11 items, 11 UATs, 26126 bytes.

```text
plan sha256 7401fdb72a1e102670dae6105d35c68644575920a71964f71b751efe8766ef55
diff sha256 453d5595b10a1cbc67c70926e1104237e367d3acbd855e84db27df8550043102
```

Exact diff in the worker scratch root: phase5.md.diff.

### Phase 6

[Plan](../phase6.md), 12 items, 10 UATs, 35644 bytes.

```text
plan sha256 9fa542176e5ac2f25eb720693fe59d67f108574766a7401bd6e4f5f719a14216
diff sha256 b112c9996d8916331f82a954c666a681976fd4b66587e4fa30c3964f323dc31b
```

Exact diff in the worker scratch root: phase6.md.diff.

## Ownership and dependency map

Each row below owns one TestUAT_<ID> GoConvey function in nextflowconformance/
plus the listed file. The complete named test strings are retained in
checks.json. Dependency entries name required item reviews in addition to phase
entry. Phases 2-6 have fifty items and retain only Item 2.1's implemented mark.
Phase 1's two historical implemented/reviewed item pairs remain accepted.

| ID | Phase/item owner | Required review | Test file |
| --- | --- | --- | --- |
| A1_01 | 1.2 | 1.1 | source_test.go |
| A1_02 | 1.2 | 1.1 | source_test.go |
| A1_03 | 1.2 | 1.1 | source_test.go |
| A1_04 | 1.2 | 1.1 | source_test.go |
| A1_05 | 1.2 | 1.1 | source_test.go |
| A1_06 | 1.2 | 1.1 | source_test.go |
| A1_07 | 1.2 | 1.1 | source_test.go |
| A2_01 | 2.9 | 2.8 | extract_test.go |
| A2_02 | 2.9 | 2.8 | extract_test.go |
| A2_03 | 2.9 | 2.8 | extract_test.go |
| A2_04 | 2.9 | 2.8 | extract_test.go |
| B1_01 | 2.11 | 2.10 | coverage_test.go |
| B1_02 | 2.11 | 2.10 | coverage_test.go |
| B1_03 | 2.11 | 2.10 | coverage_test.go |
| B1_04 | 2.11 | 2.10 | coverage_test.go |
| B1_05 | 2.11 | 2.10 | coverage_test.go |
| B2_01 | 2.12 | 2.11 | model_test.go |
| B2_02 | 2.12 | 2.11 | model_test.go |
| B2_03 | 2.12 | 2.11 | model_test.go |
| B3_01 | 2.16 | 2.15 | upstream_test.go |
| B3_02 | 2.16 | 2.15 | upstream_test.go |
| B3_03 | 2.16 | 2.15 | upstream_test.go |
| B3_04 | 2.16 | 2.15 | upstream_test.go |
| B3_05 | 2.4 | 2.1-2.3 | upstream_test.go |
| C1_01 | 3.3 | 3.2 | render_test.go |
| C1_02 | 3.3 | 3.2 | render_test.go |
| C1_03 | 3.3 | 3.2 | render_test.go |
| C1_04 | 3.3 | 3.2 | render_test.go |
| C2_01 | 3.4 | 3.3 | runner_test.go |
| C2_02 | 3.4 | 3.3 | runner_test.go |
| C2_03 | 3.4 | 3.3 | runner_test.go |
| C2_04 | 3.4 | 3.3 | runner_test.go |
| C3_01 | 3.5 | 3.4 | observe_test.go |
| C3_02 | 3.5 | 3.4 | observe_test.go |
| C3_03 | 3.6 | 3.5 | observe_test.go |
| C3_04 | 3.5 | 3.4 | observe_test.go |
| D1_01 | 4.1 | phase3 | runner_test.go |
| D1_02 | 4.1 | phase3 | runner_test.go |
| D1_03 | 4.1 | phase3 | runner_test.go |
| D1_04 | 4.1 | phase3 | runner_test.go |
| D2_01 | 4.2 | 4.1 | evidence_test.go |
| D2_02 | 4.2 | 4.1 | evidence_test.go |
| D2_03 | 4.2 | 4.1 | evidence_test.go |
| D2_04 | 4.2 | 4.1 | evidence_test.go |
| D2_05 | 4.2 | 4.1 | evidence_test.go |
| D3_01 | 4.5 | 4.4 | reference_test.go |
| D3_02 | 4.5 | 4.4 | reference_test.go |
| D3_03 | 4.3 | 4.2 | reference_test.go |
| E1_01 | 5.1 | phase4 | oracle_test.go |
| E1_02 | 5.1 | phase4 | oracle_test.go |
| E1_03 | 5.1 | phase4 | oracle_test.go |
| E1_04 | 5.1 | phase4 | oracle_test.go |
| E2_01 | 5.2 | 5.1 | adversarial_test.go |
| E2_02 | 5.2 | 5.1 | adversarial_test.go |
| E2_03 | 5.2 | 5.1 | adversarial_test.go |
| E3_01 | 5.8 | 5.3-5.7 | families_test.go |
| E3_02 | 5.9 | 5.8 | families_test.go |
| E3_03 | 5.10 | 5.9 | families_test.go |
| E3_04 | 5.11 | 5.10 | families_test.go |
| F1_01 | 6.1 | phase5 | milestones_test.go |
| F1_02 | 6.1 | phase5 | milestones_test.go |
| F1_03 | 6.1 | phase5 | milestones_test.go |
| F2_01 | 6.2 | 6.1 | render_test.go |
| F2_02 | 6.2 | 6.1 | render_test.go |
| F2_03 | 6.2 | 6.1 | render_test.go |
| F3_01 | 6.4 | 6.3 | package_test.go |
| F3_02 | 6.5 | 6.4 | package_test.go |
| F3_03 | 6.11 | 6.3-6.10 | package_test.go |
| F3_04 | 6.10 | 6.9 | package_test.go |

The mapping has exactly 69 unique IDs, the original 49 plus twenty
B3/C3/D3/E3/F3 additions. Phase counts are 7, 17, 12, 12, 11 and 10. Each file
matches the accepted spec's story test file; every owner item exists. All
original per-phase UAT-ID references remain present. The review-order graph is
acyclic, including the explicit F11 exception. F3 has exactly seven A1 fixture
IDs, 61 nonrecursive offline IDs and outer F3_03.

## Execution and review gates

Phase 2 assigns lossless projection, exact family selectors, mappings and
separate closure-extension preparation/acquisition owners before genuine
execution. It retains fixed source/fixture/expected/completion denominators, 160
selections and eighteen batches. It tests all eleven F11 malformed/valid
free-string pairs plus the compiled guard-removal fault before dependent
extraction. New schemas and parent-input reconciliation require fresh
acceptance.

Phase 3 imports all 69 obligations unchanged, supplies the C3 grammar before C1
consumes it, and requires actual discovery and genuine C3 route smoke evidence.
Expected truth is shared, read-only and independently reviewed. Phase 4 requires
raw event/freshness failures, actual compiled native and neutral smoke attempts,
corrected owned-descendant supervision and separate native/neutral/replay result
semantics.

Phase 5 names the six exact native feature selectors, four original literal CLI
expansions and their source/helper/config closure. Native, parser, Mix and
file/gap readiness/execution are separate handoffs. It retains all seven E1
cases, eighteen E2 accounting mutations and three E2 semantic controls; E3 adds
the actual finite families, all 155 paired loss subjects, six F1 Bash controls,
three analytical Mix witnesses and five semantic observer mutations. Every
actual result and raw/control receipt requires independent review.

Phase 6 separates published sources/resources/recipes, portable bundle/modes,
Linux ptrace/seccomp supervision, actual namespace/mount lanes, genuine
role/endpoint/preamble TCP and lock UDP admission, live undeclared
fixture/Gradle HTTP denial, missing-input controls and fresh reconstruction.
Actual fresh Go/JVM builds and all 68 nonrecursive children plus outer F3_03 are
required. Fixture HTTPS counts are separate from zero offline
acquisition/dependency/engine/external/DNS requests and old-root reads. Every
descendant/socket/container cleanup receipt is required.

Every active handoff measures exact
source/skills/fixture/expected/retained/changed-code bytes, hashes and tokens
plus tool/growth/reasoning allowances inside roughly 100k total tokens.
Independent complete-input review precedes work and reapproves changed bundles;
coherent splits retain the same acceptance owner. Named source, closure,
observer, isolation, Gradle-policy and result-review owners are assigned before
genuine execution. Parent phase reviews are still pending; this report grants no
input, implementation or execution approval.

Foundation reports remain bounded, with separate
accounting/native/neutral/strengthened/document-gap/replay denominators. CLI-P8,
unexecuted string Mix, seven internal obligations, five document closure
dependencies, both product decisions, whole-target inventory and all nine wr
seeds remain incomplete; wr stays pure Go with no adapter and zero runtime
passes. Research/foundation success cannot claim full language coverage.

## Authoring checks and completion

check_plans.py PASS: exact accepted-spec hash; 69 IDs/test files/owners; 49+20
split; phase counts; preserved old references and checked marks; acyclic review
graph; seven/61/outer lane accounting; ASCII, 80-column prose, typed code
fences, heading levels, blank-line/trailing-space mechanics, resolving local
links and continuous item numbers. All 312 captured authority inputs remain
byte-identical, including retained implementation, source lock/batches and
frozen pilot records. Exact diffs and check receipts are in the worker scratch
directory.

Production tests were not run because only plans and this authoring report
changed. Their implementation red/green commands and required independent
execution reviews remain future work. All owned commands completed; no tool
session, child agent, background process, live wait or retained worker job
remains. Queue owner /root can now independently review each phase before
continuing authorized implementation.
