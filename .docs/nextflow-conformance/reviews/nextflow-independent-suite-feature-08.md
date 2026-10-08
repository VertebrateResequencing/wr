# Independent Suite Feature Review 08

PASS. The current foundation spec covers the accepted requirements with
concrete acceptance criteria. No actionable feature finding remains.
This verdict accepts the specification, not implementation or execution.

## Scope and authority

Reviewer: `/root/nextflow_suite_feature_review08`. Owner: `/root`.
Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`.
Git HEAD: `cad2b64d9702cd09c172630df72c736400dc5cac`.

Authority is [prompt.md][prompt], read with [the accepted pilot report][pilot]
and [its independent acceptance][pilot-review]. Review 07 supplies no verdict
for this review. The current spec and actual pinned sources were checked
independently. Agent-conduct, completion and liveness, spec-reviewer,
go-conventions, testing-principles and writing-for-agents apply. Unslop and
prose-principles apply to this report.

The reviewed prompt SHA-256 is:

```text
a51411aa3c4e8828abf7658c2e846ed043b2cf36c8b21473a13f19803c51146c
```

The reviewed spec SHA-256 is:

```text
d31bb8200d9b4bf690ed8d5eda50d8ad57887175c43ce6002f47e6797d642f2d
```

The accepted pilot-report SHA-256 is:

```text
c1233ec605d0bca3308c32a3f4623c10c536e2cb209dbea4e9751912ed545f2e
```

## Requirement coverage

The line references below are locations in the reviewed spec. Acceptance IDs
identify concrete assertions, including expected failures and incomplete
outcomes. The original 49 IDs remain present; 20 new IDs bring the total
to 69. All 69 acceptance bodies are unchanged by author06.

| Required behaviour | Line | Acceptance coverage |
| --- | --- | --- |
| Pin release and execution inputs | 86 | A1_01-A1_07 |
| Preserve bytes and meaningful blocks | 927 | A2_01-A2_04 |
| Retain references and review history | 289 | A2_03, A2_04, B1_05 |
| Review defaults, variants and interactions | 1084 | B1_01-B1_05 |
| Retain typed and JVM/plugin decisions | 1153 | B2_01-B2_03 |
| Separate coverage denominators | 1191 | B3_01-B3_05 |
| Freeze shared expected truth | 1357 | C3_01-C3_04 |
| Render UATs and discover bindings | 1255 | C1_01-C1_04, C2_01-C2_04 |
| Require events and fresh inputs | 1490 | D1_01-D1_04, D2_01-D2_05 |
| Separate native, neutral and replay | 1599 | D3_01-D3_03 |
| Prove real oracle behaviour | 1669 | E1_01-E1_04 |
| Reject verifier corruption | 1775 | E2_01-E2_03, E3_02-E3_04 |
| Reexecute finite proven families | 1838 | E3_01-E3_04 |
| Retain durable wr runtime obligations | 1952 | F1_01-F1_03 |
| Generate bounded handoffs and evidence | 2018 | F2_01-F2_03 |
| Deliver portable fresh-checkout proof | 2065 | F3_01-F3_04 |

The public Go entry point and private domain files at spec.md:36 match the
Go conventions. CLI parsing delegates to domain code. External engine
execution has fixed reviewed routes, cancellation and bounded supervision.
Fixtures can challenge comparators and the runner without becoming engine
evidence. Every acceptance ID requires a GoConvey test at a supported
observable boundary. Generated tagged-value round trips and multiset
properties supplement the named examples in C3_01.

## Six-record consistency

The six closed record lists at spec.md:557 preserve their declared shapes.
Their field relationships can represent the required accounting, independent
truth, observations and execution closure without adding undeclared fields.

- `upstream` preserves exact origin IDs, literal-record blobs, source spans,
  fixture arrays, original parents/order/dependencies and dispositions.
  B3_01 independently checks the projection. B3_02 rejects loss of helpers,
  shared state, internal entries, predicates, invocations and completions.
- `mappings` partitions selected IDs into preserved and unresolved sets.
  `contract_ids` identifies preserved predicates; `boundary` matches the
  contract route. Equivalent-observable strength requires a source argument
  and discriminating controls. Native success cannot supply that strength.
- `contracts` owns shared truth and both engine routes. Strengthening names
  its original purpose through `upstream_ids` and source-derived reason
  through reviewed `facet_ids`. Expected values use C3's closed tagged
  grammar. Origin and provenance remain distinct for original, strengthened
  and documented-gap contracts.
- `observations` uses declared `case_id`, `contract_hash` and `engine` to
  resolve the unique contract route and derive its boundary at spec.md:626.
  `route` remains `neutral`; no observation boundary field is implied.
  Raw receipts, typed values and independent decoding preserve the evidence
  boundary. Native originals retain XML and original checks separately.
- `suite` fixes required IDs before execution and preserves source-derived
  edges, exact native selections, controls and pending dependencies.
  `suite.review_id` approves selection. Its `authority_inputs` binds the
  dependency lock whose own `review_id` approves the closure. Both reviews
  must be current and accepted before ready family execution.
- `dependencies` binds the unchanged base lock and actual resources,
  origins, cache paths, modes and allowlisted recipes. Generated output
  digests belong to resource `file` references; logical/effective recipe
  command receipts belong to attempt `artifacts`. D2 binds the fresh
  implementation tree. These are declared evidence owners, not extra recipe
  fields.

Review-input categories explicitly retain observers, fixtures, expectations
and each new record. D2/D3 retain attempts, raw artifacts and result reviews
as evidence. This separates the review hash graph from observed output and
avoids deriving expected truth from a successful engine run.

## Actual pilot and source checks

The owned checker imports no author or earlier reviewer gate. It verifies
the actual frozen records, fixture bytes, source hashes and preserved input
authorities. The source lock contains 2,856 files with 160 selected files;
batches.json still has eighteen members. The unchanged opaque runtime has
42,355,106 bytes and its pinned whole-file hash.

Independent reconciliation confirms six methods, eleven units, 42 original
predicates, ten helpers, seven internal/helper obligations, fifteen original
completions, four invocations, 83 fixture identities, 37 full source files,
123 source/span identities, 151 original/document edges, 34 document facets
and 28 document expectations. Table/provider counts remain zero. All 83
fixture hashes and all 123 source spans match retained actual bytes.

Direct source reads confirm one shared parser, sequential P1-P8, P2's
literal backslash+n, P6's substring predicate and P8 count zero. Mix retains
three methods, five-second feature timeouts, typed membership/exclusion and
sorted-list equality. ScriptHelper retains normalization, network lifecycle
and same-error propagation. Arity/nullable checks retain set +e and final
expression aggregation; topic checks retain exact byte comparisons.
S-MIX's stronger multiset remains separate from original membership checks.

The 578 acquired resource paths independently match retained hashes and
sizes. Actual Gradle 9.3.1 class disassembly confirms the `Gradle Magic`
TCP preamble and ten-byte cache-lock packets with version 1, big-endian
lock ID and types 1-3. The real communicator uses DatagramSocket. These
source and bytecode facts support the specified finite IPC design; they
award no new build, sandbox, offline or engine execution pass.

## Completion boundaries and historical preservation

The three E3 families retain genuine native original execution for selected
obligations, independently reviewed neutral boundaries, typed raw receipts
and paired controls. Original-only/internal obligations stay visible and
block full independent coverage. Native fallback preserves regression
purposes without claiming neutral whole-harness or raw-value equivalence.

At spec.md:707 and spec.md:2400, CLI-P8's runtime/parser mismatch, the
unexecuted six-string Mix example, all seven internal/helper obligations,
five document closure links, typed milestone, JVM/plugin policy, full target
inventory and future wr execution remain unfinished. A scalar observation,
native assertion pass or resolved location cannot discharge those gates.
The current CLI still dispatches acquisition and validation only, as
confirmed in nextflowconformance/cli.go:294. No engine suite already exists
merely because these contracts have been specified.

F3 requires published sources, toolchain/cache reconstruction, historical
mode metadata and portable executable identity. It composes seven real
HTTPS fixture bindings, 61 offline bindings and the nonrecursive outer
F3_03 gate. Builds and genuine engines cannot receive fixture-produced
inputs. The portable OCI rootfs, published syscall supervisor, actual
Gradle TCP/UDP enforcement, genuine daemon denied-repository probe and all
cleanup receipts remain future measured obligations at spec.md:2065.
Disassembly, Docker availability or an offline flag cannot satisfy them.

The original six-phase implementation order remains at spec.md:2357.
Existing phase plans are retained pre-revision inputs. Their revision and
independent phase reviews are later workflow steps, not awarded here.
Historical Phase 1 remains accepted; Phases 2-6 and additions still need
implementation and review. Core F11's meaningful UTF-8 test correction and
fresh parent-input reconciliation remain required before extraction.

All 1,292 author06 baseline bindings match, with one separately composed
root transition for progress.md. Its recorded before/after hashes match
the actual bytes. The historical author baseline and reviewed authorities
remain immutable; no old gate is weakened to absorb that transition.

## Checks and actual completion

The owned passive checker passed:

```bash
timeout 60s python3 .tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review08/check.py
```

[Check results][checks] and [reviewed input hashes][inputs] retain the exact
comparisons. Earlier checker navigation mistakes were corrected against
actual path/comparator spellings; no reviewed artifact was repaired.
Report ASCII, 80-column prose, headings, whitespace, fence languages and
local links were checked. The task changes no supported behaviour, so no
new behavioural test is appropriate. No implementation, Nextflow, Gradle
build, sandbox or fresh-checkout execution pass is claimed.

Writes are confined to this new report and feature-review08 scratch.
No reviewed file, commit, push, system installation or child agent changed.
All owned commands completed; no owned live process, background job, tool
session, outstanding wait or child remains. [Completion receipt][completion]
binds the report and evidence hashes after final checks.

[prompt]: ../prompt.md
[pilot]: ../research-pilot/pilot-report.md
[pilot-review]: ../research-pilot/pilot-report-review.md
[checks]: ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review08/checks.json
[inputs]: ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review08/reviewed-inputs.json
[completion]: ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review08/completion.json
