# Independent suite Phase 3 review 01

FIXED on 2026-10-08. Corrected Phase 3's shared-decoder dependency and genuine
route readiness gap against the accepted spec. This verdict reviews the plan;
it grants no implementation, execution or input-bundle acceptance.

Review owner: `/root/nextflow_suite_phase03_review01`. Queue owner: `/root`.
Branch: `nextflowdsl`; shared worktree: `/home/ubuntu/wr`.

## Reviewed authority and exact diff

```text
spec.md SHA-256:
57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c
phase2.md reviewed dependency SHA-256:
b23734ddd8e13f5250deb8596dfa734c836da65ad2c8b912bd0681e116a07648
phase3.md initial SHA-256:
379f1528d12ea6d84a3da026f5985750c513039b68d1a74bbad0adff9b37e852
phase3.md corrected SHA-256:
4f9a9f7eed551dadd4e800941b8fc118c77a73a764c3cf3c7db123c1f435c560
phase3.diff SHA-256:
b3a2acdaa99bc5335d5a117d65d9d0f79a934bf4002b013d88321d539f8930f3
```

The exact owned phase diff is `phase3.diff` in the reviewer scratch directory:

```text
.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/
phase-review03-01/
```

It adds 59 lines and removes 18 lines. Only `phase3.md`, this new report and
that scratch directory were written. The accepted spec and corrected Phase 2
retain their supplied hashes. Read Architecture, all C1/C2/C3 stories and
acceptance text, Implementation Order, corrected Phase 2 and later launch plans.

## Errors and corrections

1. C1 required raw replay and typed comparison before Item 3.5's decoder work.
   Item 3.1 now supplies shared tagged encode/decode, sequence/multiset
   comparison and raw replay before Item 3.3 consumes them. Item 3.3 owns its
   error-fixture decoders; Item 3.5 retains parser/family decoding and its three
   C3 tests. Acceptance ownership stays unchanged.
2. Item 3.6 required a genuine smoke before its concrete observer and launch
   prerequisites existed. It now owns published `mix-smoke.nf` and config,
   original M1 expressions and S_MIX truth, separately reviewed typed callback
   source, fixed opaque-distribution/Java 21 launch and truthful cleanup.
   Current expected/observer/source and dependency reviews, named readiness
   and execution owners, exact closure, network denial and a 120-second launch
   deadline precede execution. Collection, engine and cleanup receipts are
   independent. The CLI writes a new schema-valid attempt and raw observation
   through the public result/exit contract. It uses no future runner, E1
   harness or D3 implementation. Missing prerequisites leave it incomplete.
   D1/D2/D3 and E1/E3 retain full later gates and their own fresh executions.
3. Made C2_04 validation exit 2 explicit, restored wildcard-regex rejection
   and stable-ID/batch/dependency rendering, and required C3_02's successful
   complete six-item control before its three receipt deletions.

## Acceptance ownership and full subcases

Six continuously numbered items remain sequential. All twelve C acceptance
IDs match the spec, with one owner and exact GoConvey binding each. Shared
prerequisite Items 3.1 and 3.2 create no duplicate binding.

- Item 3.3 owns C1_01-C1_04 in `render_test.go`. Two-case rendering, failed
  binding, unresolved decision and byte-identical regeneration remain. Missing
  UAT, reviewed-hash drift and edited checkbox have separate diagnostics.
  Multiplicity, sequence order, reviewed multiset equality and normalization
  rejection remain separate. Both independently authored error fixtures check
  empty emissions, exact diagnostics, task/exit/artifact observations. Each
  missing-Java and appended-OBS mutation fails without changing expected
  bytes. Actual E1 evidence remains later.
- Item 3.4 owns C2_01-C2_04 in `runner_test.go`. Actual `go list -json` and
  `go test -list` discover active declarations under matching build settings.
  Comments and paths count zero. Missing, excluded and invalid-package cases
  preserve specified exits and zero executions. Exact ONE selection excludes
  ONE_EXTRA; regex and package patterns fail validation. Passing fixtures
  cannot satisfy mismatched runtime evidence kind.
- Item 3.5 owns C3_01, C3_02 and C3_04 in `observe_test.go`. Scalar/file/list
  tags, large decimal integer, nested order, duplicate multiplicity and
  deterministic generated properties remain. Complete Mix passes before the
  three independent receipt deletions fail with zero passes; malformed/extra
  values fail format checks. P1-P7 retain count/location/message and P6
  substring semantics. P2's backslash+n mutation fails. CLI-P8 stays failed
  despite separate JVM count-zero success. G-IN/G-OUT associated errors pass;
  generic tool/parser/unrelated-process errors fail. G-OUT retains script
  exit zero and one.txt bytes. Fixtures remain foundation evidence.
- Item 3.6 owns C3_03 in `observe_test.go`. Both routes share contract ID/hash
  and expected bytes. Nextflow requires a fresh actual smoke. Restored wr has
  no binding, observer or observation and fails explicitly with zero wr
  passes. Route-specific truth fails validation. Source-reviewed contradiction
  retains raw data and an unresolved candidate without changing expected truth.

## Dependencies, import and handoffs

Phase entry requires corrected Phase 2 exit and independent review. C3 grammar
and shared replay/comparison precede C1 rendering. Items 3.2-3.6 follow reviewed
predecessors. Item 3.6 consumes Items 2.13-2.15's original projection, mapping
and closure, rechecking current suite/dependency review IDs and authority
binding.
Historical pilot observations and acquisition/validation success award no new
engine evidence. Retained CLI dispatch still implements acquire/validate only;
other commands return `E_PREREQUISITE`. All phase items remain unchecked.

Item 3.2 imports all 69 obligations with full amended subcases and provenance.
Comparison with HEAD's original spec confirms all original 49 IDs and exactly
twenty B3/C3/D3/E3/F3 additions. Phase counts are 7, 17, 12, 12, 11 and 10.
Drafts retain null executable fields and planned ownership. Ready cases need
reviewed actual input/expected bytes and exact discoverable binding. Explicit
catalog payloads cover all eleven categories, including suite extensions.
B3_05 reconciliation, A1 archive/runtime/opaque identity controls, A2 history,
B1 freshness, D2_01's seven changes and F2 reconciliation remain in the import.

Closed grammar retains seven expected fields, six checked/inapplicable wrappers,
unwrapped observations, decimal integer tags, files/lists, empty emission
versus emitted empty list, completion receipts, exact parser scalars and fixed
pointers/comparators. Original predicates and strength stay distinct from
stronger multiset checks. Expected truth remains read-only and independently
reviewed; raw replay checks observations. Unavailable wr remains an explicit
failure. Supplied fixtures acquire no genuine engine identity.

Every item and coherent split requires measured complete fresh-context inputs,
source/skill/fixture/changed-code hashes and tool-output/growth/reasoning
allowances within roughly 100k tokens. Independent approval precedes each
handoff; changed bytes need renewed approval before code review. Named owners
are assigned before work. This plan verdict approves no future input bundle.

## Completed checks and liveness

Bounded Python checks passed all 18 recorded checks: exact authority hashes,
six continuous items, twelve unique owners, 69 IDs preserving 49+20, no checked
marks, ASCII, 80-column prose, one h1, heading levels, labelled balanced fences,
resolved local links, no placeholders, trailing whitespace or repeated blanks.
`checks.json` records identities, owner rows, original/addition IDs and counts.
Report mechanics and scoped diff whitespace checks also passed. No production
tests or engine commands ran because only the plan was reviewed and corrected.

No commits, pushes, nested agents, installs or background jobs were started.
All owned calls completed; no live process, session, child or wait remains.
