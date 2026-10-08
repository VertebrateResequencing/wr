# Phase 2: Reconcile contracts, then implement A2, B1, B2 and B3

Ref: [spec.md](spec.md) sections A2, B1, B2, B3

## Instructions

Begin after [phase1.md](phase1.md) exit conditions and independent review pass.

Use the `orchestrator` skill with fresh `go-implementor` and independent
`go-reviewer` handoffs. Read these skills before the assigned work:

- `/home/ubuntu/.agents/skills/go-implementor/SKILL.md`
- `/home/ubuntu/.agents/skills/go-reviewer/SKILL.md`
- `/home/ubuntu/.agents/skills/go-conventions/SKILL.md`
- `/home/ubuntu/.agents/skills/implementation-principles/SKILL.md`
- `/home/ubuntu/.agents/skills/testing-principles/SKILL.md`

Read the accepted spec's Architecture, assigned stories and Implementation
Order. Production wr remains pure Go; Nextflow/JVM sources and fixtures are
development test assets. Keep the accepted source lock, all 160 selections,
eighteen bootstrap batches and every frozen pilot record unchanged. New suite
inputs and dependencies use reviewed extension records. Existing retained
production commands implement acquisition and validation only; planned commands
and unchecked items below require implementation and evidence.

Give each item its own measured input manifest and fresh implementation and
review contexts. Record exact source spans, skill bytes, fixture/expectation
bytes, retained source and changed-code bytes, hashes and measured token counts.
Include tool output, growth and reasoning allowances inside the roughly 100k
total context ceiling. An independent input reviewer must approve the complete
bundle before work and reapprove changed inputs before code review. Character
counts alone grant no approval. Split a large item at a coherent
semantic/dependency boundary, retain its acceptance owner, and measure/review
each sub-handoff; every resulting sub-handoff must pass before closure. The
queue owner assigns named implementor, input reviewer and code reviewer
identities before launch. Readiness owners named below must produce accepted,
current source/fixture/expected/dependency reviews before genuine execution.

Run dependent implementation items sequentially after predecessor review. There
are no parallel implementation batches in this phase. Independent source or
semantic reviews may run concurrently only after source locking, with separate
assigned spans and named owners. Retained reports do not approve new input
bytes. Each acceptance ID has exactly one `TestUAT_<ID>` GoConvey function in
the ownership table's test file; supporting tests do not duplicate bindings or
shrink fixed required sets. Capture a meaningful red command before implementing
each new obligation and a green command afterwards. Where production already
satisfies an obligation, demonstrate the named isolated fault makes its test
red, then restore and prove green. Tests exercise CLI/results and artifacts;
runner tests launch only temporary fixture subjects.

Use bounded commands with `CGO_ENABLED=1`, `-tags netgo` and `-count=1`. Save
stdout JSON, stderr, exits, raw receipts and artifact paths for review. Run
relevant lint and required project gates with recorded deadlines; report
unrelated baseline failures separately. No implicit downloads, skipped missing
prerequisites, fixture engine substitutes or system installs are permitted. Only
`acquire` may fetch; other commands use verified offline inputs. Expected truth
remains read-only and separately reviewed. Missing prerequisites or
unenforceable isolation leave the affected item and phase incomplete.

Historical Phase 1 acceptance at `ec487ed2` remains intact. The old [A2 bundle
review](reviews/nextflow-phase2-a2-bundle.md) and schema input reviews predate
this revision and grant no new implementation approval. The first outstanding
Item 2.1 review has an explicit prerequisite exception: run Item 2.2's bounded
F11 test correction before closing Item 2.1 review. Item 2.2 does not depend on
that review. All other dependencies remain sequential. Eighteen-schema
reconciliation in Item 2.4 must be independently accepted before Item 2.5's
loader or any extraction implementation starts.

## Items

### Item 2.1: A2 - Add the closed reference and extraction schemas

spec.md section: A2; Architecture, Extraction references and retained
generations

Retain the already implemented twelve-schema amendment in
`nextflowconformance/model.go`, `schema.go`, emitted schema files and active
fixtures. Its required `cross_refs`, three destination kinds, both ambiguity
candidate kinds and extraction/catalog shapes remain implemented work. This mark
records those retained changes only. It does not accept F11, six new suite
schemas or the current eighteen-schema contract.

The pending [schema review 06](reviews/nextflow-phase2-schema-review-06.md)
reports F1-F10 resolved and F11 outstanding. Preserve production UTF-8 rejection
and all existing assertions. Item 2.2 is an authorized prerequisite test
correction while this review stays open. After Item 2.2's independent PASS,
supply a freshly measured complete-input bundle and rerun this full schema
review under the current spec. Require decoder/stock JSON Schema agreement and
all prior required-field, unknown-field, ordering, uniqueness, nullability,
URI/path, span and strict-envelope controls. Keep `include_refs` unchanged. Only
a new independent PASS can mark this item reviewed.

- [x] implemented
- [ ] reviewed

### Item 2.2: B3 - Correct F11 before closing the retained schema review

spec.md section: B3_05; Architecture, Extraction references and retained
generations

This is the prerequisite exception stated above; start after new input approval,
without requiring Item 2.1 review. Read F11's exact fixture templates and
supported block/catalog entry points in schema review 06. Add the named
`TestExtractionRecordUTF8Strings` correction in the existing model tests.

Use all eleven field locations: Block span.file, Edge span.file, Edge target,
Tree destination.path, Tree destination.fragment, Path candidate.path, Path
candidate.fragment, Definition candidate.span.file, Definition candidate.target,
Current input path and Reviewed input path. Insert bytes `61 FF 62` inside
otherwise valid serialized JSON strings with final LF; assert rejection. Paired
bytes `61 EF BF BD 62` must pass the documented local decoder entry points and
stock schemas. Check fixture construction and marshaling errors separately.
Freeze other IDs and required fields so an unrelated syntax, ID, reference or
path failure cannot prove rejection.

Demonstrate green production, then compile the isolated guard-removal fault from
F11 in an owned disposable copy. All eleven malformed-byte rejection assertions
must fail there; all eleven valid controls must still pass. Restore and prove
green. Retain exact patch, 22 controls, commands and logs. Production rejection
needs no relaxation. Preserve all previous tests and schema fixtures. The
independent reviewer accepts this correction and fresh complete-input hashes
before Item 2.1's pending review closes; no acceptance is inferred from the old
package's passing tests. This supports B3_05, owned by Item 2.4.

- [ ] implemented
- [ ] reviewed

### Item 2.3: B3 - Define the six closed suite schemas and extension inputs

spec.md section: B3_05; Architecture, Independent suite records and routes

Start only after Items 2.2 and 2.1 have new independent PASS verdicts. Implement
private record types and matching emitted schemas for `upstream`, `mappings`,
`contracts`, `observations`, `dependencies` and `suite`. Together with the
retained twelve, emit exactly eighteen active schemas. `suite` also owns F3's
closed bundle shape; the extraction catalog is an embedded definition, not an
additional schema. Extend UAT/binding/attempt vocabulary for `native-original`
and D3's required nullable/empty route fields, without adding an exported API
beyond `Run`.

Validate C3's closed tagged expected/observed grammar, checked wrappers,
completion receipts, fixed comparators/pointers, native expected IDs, exact
native selection and fixed recipe/resource/root shapes. Add the `upstream`,
`mapping`, `contract`, `dependency` and `suite` review catalog categories;
observer code uses `binding`, fixtures/expectations use `uat`, original spans
use `source`. The full eleven-category reconciliation suite is required.
Explicitly regenerate active fixtures under reviewed definitions. Preserve
literal pilot identity/blob fields, alias collision handling and requested
versus resolved dependency versions. Stock schemas check closed local shapes;
loader and later story checks enforce cross-record relationships. Independently
review each coherent model/schema sub-handoff before dependent use. This
supports B3_05; no suite execution is awarded by schema validity.

- [ ] implemented
- [ ] reviewed

### Item 2.4: B3 - Accept current retained-contract reconciliation

spec.md section: B3_05

Own all of `B3_05` in `nextflowconformance/upstream_test.go`. Produce the
retained-versus-current reconciliation report for model, emitted schemas, CLI
argument/results/loader contracts, review categories and active fixtures. Name
differences, preserved historical bytes, corrected F11 controls, fresh
parent-input hashes and the reviewed implementation work still absent. Reconcile
the current acquisition/validation implementation only; extraction, runner,
native, neutral, packaging and replay remain future capabilities.

Validate all eighteen schemas with independent positive and negative records.
Implement the revised suite cross-record checks at the current validation
boundary here and exercise their fixtures through the public CLI. Retained
generation/history loading remains Item 2.5; these controls must not depend on
that future loader. Preserve the source lock, 160 selections, eighteen batches
and the archived eleven-schema acceptance byte-identically. Require independent
current acceptance of reconciliation and F11 correction before the loader or
extraction. The gate returns 1 with `E_CONTRACT_RECONCILIATION` when that
authority is missing or stale. Write its red absence/drift controls and green
accepted control; a report carrying its own editable success field cannot
satisfy it. Review all changed files and parent input bytes in fresh complete
contexts. Only this item's independent PASS authorizes Item 2.5.

- [ ] implemented
- [ ] reviewed

### Item 2.5: A2 - Load genuine extraction inputs and retained generations

spec.md section: A2; Architecture, Extraction references and retained
generations; Commands and result contract

After Item 2.4 review, implement the generation loader and CLI integration in
`nextflowconformance/`. Use independently authored generation fixtures for
cross-record checks. Full corpus loading captures `extraction.json` once and
follows only hash-bound snapshots; an active root `blocks.json` is an error.
Validate the acyclic predecessor chain, historical locks, review bytes, source
spans, catalogs and explicit payload closure. Resolve historical block IDs
through retained generations, while active links need current IDs. Reject
review-ID reuse with changed bytes and generated-output review bindings,
including symlink aliases. Preserve the exact Architecture exit codes for
malformed references, unsafe paths and damaged evidence.

Give `extract` its command-specific loader for the genuine reviewed lock and
batches. Reuse closed decoding, offline preflight and pinned identity checks;
verify each batch span and the exact eighteen-member selection. Defer only batch
`assigned_ids` resolution until B1. Empty review catalogs are valid before
semantic initialization. Keep full `validate` target, profile, required-file and
semantic-reference checks intact. Extraction claims only `source-enumerated`,
counts every unaccounted semantic block as pending, and reports occurrence-based
`pending_references` with explicit zero counts. This handoff supports loader
subcases of `A2_03` and `A2_04`; public extraction success awaits Item 2.9.

- [ ] implemented
- [ ] reviewed

### Item 2.6: A2 - Independently label extraction and reference fixtures

spec.md section: A2, all four acceptance tests

After Item 2.5 review, before extractor implementation, a separate fixture
author labels original bytes under `nextflowconformance/testdata/`; an
independent reviewer accepts exact spans, kinds, destinations and ambiguity
candidates. Keep this work independent of extractor output. Cover `A2_01`'s
heading, two signatures, three options, table, nested warning, code and include
in LF, CRLF and no-final-newline variants. Label grammar alternatives, comments,
actions, quoted separators, lexer fragments/modes, declarations, quoted/named
test methods, multiline strings and closures from original fixture bytes.

Label every `A2_04` reference fixture, including repeated same-file labels,
explicit-label/heading collisions, repeated reference-style definitions with
distinct and equal URIs, and extensionless path ambiguity. Preserve the spec's
exact spans, literal targets, sorted candidates and pending counts. Add reviewed
originals for all eleven review-input categories and multi-generation scenarios
in `A2_03`. This fixture handoff supports all four A2 UATs; it awards no
real-corpus extraction acceptance.

- [ ] implemented
- [ ] reviewed

### Item 2.7: A2 - Extract documentation units and reference occurrences

spec.md section: A2; Architecture, Extraction references and retained
generations

After Item 2.6 review, implement documentation extraction in
`nextflowconformance/extract.go`, tested in `extract_test.go` against the
accepted labels. Cover `A2_01` and documentation/reference parts of `A2_04`.
Preserve exact parent/child and leaf partitions, stable IDs, unknown constructs
and trivia. Resolve recursive includes with verified local bytes; missing
targets and cycles fail before publication.

Extract each reference occurrence once into its deepest containing block, with
the complete original span, literal token and specified ordering. Index locked
documentation bytes, including unselected files, without changing selection.
Implement the exact Architecture Markdown/MyST/HTML, heading-anchor, path/URI,
escape and ambiguity rules. Keep definition occurrences distinct even within one
file or with equal targets. Resolve pending references without fetching;
acquired external IDs require exact origin matching and verified bytes. Test
every destination kind, missing reason, unsafe path and malformed edge from
`A2_04`. Retain unresolved edges as pending inventory, separately from include
failures. Review this bounded extractor work before adding grammar/source
handling; it grants no complete real-corpus or publication acceptance.

- [ ] implemented
- [ ] reviewed

### Item 2.8: A2 - Extract grammar, source and exact bootstrap spans

spec.md section: A2, acceptance tests `A2_02` and `A2_03`

After Item 2.7 review, extend `nextflowconformance/extract.go` with grammar
alternatives, source declarations and upstream test methods. Preserve every
selected byte, unknown structure and included map/mix snippet. Resolve the
complete pinned file list and exact locked bootstrap selectors; absent or
ambiguous selectors fail. Compare independently labelled originals and locked
spans, rather than inferring selectors from extracted headings.

Independently reconstruct every selected original file from leaves and check
ordered parent/child partitions. Check the real `process-page` reference to
unselected `docs/process.md`, the Google Cloud occurrence in process.md bytes
`[11678,11742)`, and the Groovy library reference. The two URLs stay pending
without artifact IDs or requests. Keep the source lock and eighteen bootstrap
members byte-identical. Prepare independent missing warning, collapsed `stageAs`
signature, grammar-alternative and missing-edge corruptions for Item 2.9's
public check-mode proof. Review the complete extractor before generation
publication work.

- [ ] implemented
- [ ] reviewed

### Item 2.9: A2 - Publish and reconcile retained extraction generations

spec.md section: A2, all four acceptance tests; Architecture, Extraction
references and retained generations

After Item 2.8 review, implement candidate construction, immutable payloads and
atomic publication through `extraction.json`. Snapshot exact lock, batches,
blocks, reviews and inputs, compute the specified six-hash ID, and retain every
predecessor and reachable blob. The input catalog retains exact current or
absent inputs and original reviewed bytes for the union of current and retained
reviews. Its closure follows explicit review bindings only. Missing original
payloads fail `E_EVIDENCE_MISSING`.

Compute exact removed/added IDs and stale review IDs from candidate inputs,
originally reviewed obligation bytes and retained block provenance. Reproduce
each historical reconciliation from its own catalog. Preserve review bytes and
old stale entries without transferring acceptance. Compare the five candidate
snapshots before assigning a predecessor; unchanged valid extraction is a
byte-identical no-op. An invalid active reconciliation cannot be repaired by
appending another generation.

Flush prepared blobs, then recheck lock, batches, reviews, current inputs
including absence, inspected source inputs and previous publication before one
atomic rename. Prove cancellation/failure leaves the old generation intact,
readers see one complete generation, and barrier-controlled semantic changes or
creation of absent inputs fail `E_INPUT_CHANGED` with exit 2.

Implement read-only `extract --check` from verified original inputs and retained
history. Active block/edge/catalog/reconciliation drift returns 1 with
`E_EXTRACTION_MISMATCH`, including active generated-block hash drift. Malformed
shapes, missing/damaged historical snapshots or payloads, source integrity
failures and cyclic chains retain the distinct Architecture exit 2 diagnostics;
full validation rejects damaged snapshots with exit 2.

Complete all four A2 acceptance tests in `nextflowconformance/extract_test.go`:
`A2_01`, `A2_02`, `A2_03`, and `A2_04`. Run the real CLI twice and compare all
bytes/IDs. Exercise all eleven `A2_03` input categories independently, two
semantic-only changes, current file deletion, missing retained/original bytes,
forbidden bindings, three generations, concurrent publication and no-op repeats.
Every check subcase leaves all input and generated files byte-identical. Rerun
all labelled fixtures and corruption probes; obtain independent A2 acceptance
before B1.

- [ ] implemented
- [ ] reviewed

### Item 2.10: B1 - Author obligations from the complete bootstrap spans

spec.md section: B1, all five acceptance tests

After Item 2.9 review, author original-source obligations and linked
requirements/draft cases in `nextflowconformance/data/`. Assign a separate
semantic author and independent source reviewer for each coherent batch of
documentation, grammar, test methods or dependency declarations. Bind exact
original spans, surrounding meaning, source hashes and all separately stored
fixture/expectation/normalization/binding inputs. Each required semantic leaf
and each distinct default, alternative, overload, error, boundary, warning,
example and interaction has its own obligation or reviewed nonrequirement
rationale. Containers cannot discharge children.

Preserve both `stageAs` overloads, `file()` defaults, the `files()` optional
exception and feature flags. Include every original A2 Mix method, even beyond
the narrower three-method E3 neutral selection. Keep unreviewed selected regions
pending. Initialize genuine target/profiles only after actual obligations and
schema-valid linked draft cases exist; extraction supplies no ledger meaning.
Refresh extraction after independently reviewed semantic authoring. Item 2.11
owns B1's executable accounting tests and must compare against these accepted
original-source expectations, not invent them from its own output.

- [ ] implemented
- [ ] reviewed

### Item 2.11: B1 - Review source facets and current semantic acceptance

spec.md section: B1

After Item 2.10 review, implement accounting in
`nextflowconformance/coverage.go`. Cover all five acceptance tests in
`nextflowconformance/coverage_test.go`: `B1_01`, `B1_02`, `B1_03`, `B1_04`, and
`B1_05`. Use Item 2.10's independently accepted obligations and requirement/case
links. Extraction cannot approve these records. Check every facet and child,
bidirectional links, cycles, provenance, meaningful descriptions, review
independence and freshness. Preserve typed overloads, defaults, exceptions and
feature flags independently of extraction counts.

Create actual obligations and linked draft cases before initializing the
production target/profiles. Keep unfinished cases schema-valid drafts; phase 3
supplies executable expectations and bindings. Full `validate` can succeed only
after this real ledger initialization. Refresh extraction snapshots after
semantic authoring before awarding any completion claim.

Use captured current inputs to detect `E_REVIEW_STALE` both before and after
re-extraction. Under `B1_05`, prove obligation-only and UAT-only changes
preserve review bytes and grant zero current accepted reviews. Retain
removed-block review provenance; unknown IDs without history fail. A matching
independent review may satisfy a current gate even if also retained in history;
removing it from current reviews removes eligibility. Pending source edges
remain valid bootstrap inventory but keep target inventory incomplete with
`E_SOURCE_REFERENCE_PENDING`. Keep all other selected semantics pending and
historical reconciliation unchanged.

- [ ] implemented
- [ ] reviewed

### Item 2.12: B2 - Keep policy decisions separate from observations

spec.md section: B2

After Item 2.11 review, extend `nextflowconformance/model.go` and seed both
unresolved policy decisions with their affected requirements in
`nextflowconformance/data/`. Cover all three acceptance tests in
`nextflowconformance/model_test.go`: `B2_01`, `B2_02`, and `B2_03`. Verify scope
and observation status independently through CLI results. Explicit reviewed
decisions alone authorize exclusion; an oracle observation cannot settle
compatibility policy.

Provide the scope check used by `verify --suite wr-runtime` for `B2_01` now:
unresolved decisions return 1 with `E_SCOPE_UNRESOLVED`. This check cannot award
runtime completion; D1 and D2 add execution and freshness verification in phase
4. Use isolated records for resolution and observation cases; keep both
production decisions unresolved.

- [ ] implemented
- [ ] reviewed

### Item 2.13: B3 - Project frozen original accounting without loss

spec.md section: B3_01 and B3_02

After Item 2.12 review, implement the lossless projection in
`nextflowconformance/upstream.go` and checked extension data. Bind immutable
pilot `contracts.md`, `inventory.json`, `expectations.json`, the accepted
[pilot report](research-pilot/pilot-report.md) and separate
[report review](research-pilot/pilot-report-review.md), source snapshots,
fixtures and result provenance in `suite.authority_inputs`. These reports
authorize finite source accounting only; new packaged runs need fresh evidence.
Separate author and source reviewer compare every scalar, comparator, type,
relationship, state/order/config field and frozen hash. Origin-ID encoding alone
proves no preservation. Use `UP_` plus uppercase hex of UTF-8
`family + ":" + origin_id`.
Preserve contract origin-to-local maps, such as S-MIX to S_MIX, with collision
controls returning `E_DUPLICATE_ID`.

Retain six methods, eleven P/M units, 42 predicates, ten helpers, seven
helper-observation obligations, fifteen completions, four invocations, 83
fixtures, 37 full files, 123 spans, 151 original/document edges, 34 document
facets and 28 expectations. Explicit table/provider counts are zero. Preserve
151 source-derived edges separately from resolved JVM resources. Literal record
blobs retain exact original entries and original source fields. Historical
native verdicts stay historical and create no new neutral passes.

Prepare independently intact controls for missing predicate, helper/internal
obligation, shared-parser state, resume, completion and fixture; the fixed
required set never shrinks. These support B3_01/B3_02, closed by Item 2.16. No
new execution or whole-target mapping claim follows from projection.

- [ ] implemented
- [ ] reviewed

### Item 2.14: B3 - Freeze family selectors, mappings and pending controls

spec.md section: B3; Architecture, Bounded accounting and unfinished work; E3
preparation

After Item 2.13 review, own the suite selection and mapping preparation. Record
exact E3 feature names and original CLI templates in `suite.json` from the
frozen inventory; Item 5.3 lists the required names and closure. Selections
accept ordered literal names/argv only, never regex. Required native, neutral,
fixture and completion IDs are fixed before execution. Bind each required
control manifest, all frozen authority inputs, typed shared contracts and
source-derived mapping arguments. Give unresolved/original-only mappings
specific rationale; preservation and unresolved sets partition every mapping.
Review design strength and actual execution separately.

Seed CLI_P8_MAPPING, STRING_MIX_EXECUTION, INTERNAL_EQUIVALENCE,
DOCUMENT_CLOSURE and FULL_TARGET_INVENTORY with original affected IDs and
reviewed dependencies. Preserve the seven origin IDs H-M-LAST-SESSION,
H-M-LAST-MAINSCRIPT, H-M-RUN-NETWORK, H-M-RUN-ERROR, H-P-SHARED-INSTANCE,
H-P-TESTUTILS-ORDER and H-CLI-RUNNER-STATUS. Preserve the five link IDs
LINK-MULTIPLE-INPUT-FILES to process-multiple-input-files, LINK-WORKFLOW-TYPED
to syntax-workflow-typed, LINK-STATIC-TYPES to migrating-static-types,
LINK-PROCESS-TYPED-TOPICS to process-typed-topics and LINK-STRICT-PARSER to the
unreviewed strict-syntax-page remainder. Keep D-MIX-EXAMPLE and D-MIX-COMPLETION
unexecuted; numeric M1 supplies no pass. CLI-P8's original input and expected
parser count zero stay unchanged; retain its required-greeting failure without
adding a greeting default. Current native session success or resolved locations
cannot fill these pending gates.

The Item 2.14 implementor owns selection/translation readiness preparation; its
independent source reviewer owns acceptance of selectors, lossless projection,
controls, mappings and shared expectations. Ready execution also requires Item
2.15's dependency review. Record those distinct named owners before handoff.
Phase 5 independently rechecks observer/execution readiness; record creation
here awards accounting only.

- [ ] implemented
- [ ] reviewed

### Item 2.15: B3 - Review and acquire the development closure extension

spec.md section: B3; Architecture, Target acquisition and Independent suite
records; F3 preparation

After Item 2.14 review, own `dependencies.lock.json` preparation and extend
`source.go`/the private CLI for `acquire --dependency-lock PATH`. Initial
resources come from the pilot's actual measured closure, with immutable origin
URLs, exact bytes/hashes/cache paths, actual requested/resolved versions,
source/generated/acquired/reused roles and reviewed fixed compile recipes. The
base lock and selections remain unchanged. Record Gradle 9.3.1, Spock
2.4-groovy-4.0, JUnit Platform 1.14.1, compiler Groovy 4.0.31 versus Gradle
4.0.29, source target 17 and execution JDK 21. Preserve the 578-path measurement
as historical evidence; actual extension completeness includes any additional
verified resource demanded by F3's build/runtime recipes.

Bind source observers/wrappers/supervisors under `data/tools/`, Java/Go/Bash and
tool executable/link trees, loaders/libraries, full Gradle URL/cache metadata
and Go module/build prerequisites. Publish only fixed allowlisted recipe
templates with resource-ID inputs/outputs, logical/effective argv, environment,
deadlines and the seven allowed `@ROOT_ID@` tokens. Generated outputs bind
recipes, not invented acquisition URLs. POMs stay metadata; original source
edges stay distinct from resolved resource dependencies. The future
offline-built OCI image/rootfs, seccomp and supervisor sources have explicit
extension inputs/recipes finalized in Phase 6.

The Item 2.15 implementor owns closure enumeration and acquisition receipts; a
separate dependency reviewer owns source/resource/recipe acceptance. Assign
their identities before acquisition or genuine build handoff. Acquire only
reviewed immutable origins into a new cache generation; transaction failure
preserves old authority/cache. Rehash all actual resource/cache placements
offline, returning `E_DEPENDENCY_MISSING` before execution for missing inputs.
No local cache alone proves portability. Bind the accepted dependency lock in
`suite.authority_inputs` before any ready execution handoff. Require both the
suite's selection `review_id` and the lock's dependency `review_id` to be
nonnull, accepted, independent and current. Item 2.14's source reviewer accepts
selection authority; Item 2.15's dependency reviewer accepts lock authority.
Independently review extension acquisition without accepting build or engine
success. Phases 4-6 own fresh actual compile, execution and reconstruction
receipts and re-review any closure extension before using it.

- [ ] implemented
- [ ] reviewed

### Item 2.16: B3 - Verify original purposes and distinct pending gates

spec.md section: B3_01, B3_02, B3_03 and B3_04

After Item 2.15 review, close the first four B3 tests in
`nextflowconformance/upstream_test.go`, using Items 2.13-2.15's reviewed
records. B3_01 proves every finite frozen count/hash/type/relationship and zero
created neutral passes. B3_02 independently deletes each specified kind; return
1 with `E_UPSTREAM_UNACCOUNTED` and exact origin ID, with fixed required counts,
even beside a genuine native pass. Include internal loss.

Implement the public `verify` command's read-only source-accounting and mapping
checks here, using the spec's existing profiles and JSON/exit contract. Exercise
B3_02 and B3_04 through that CLI boundary; an unsupported command or missing
prerequisite cannot substitute for their intended diagnostics. Intact controls
prove accounting preservation without awarding missing execution gates or
bootstrap completion. Phase 4 adds event/freshness verification to this command.

For B3_03 analytically evaluate all seven original M1 predicates on
`[1,2,3,"a","b","z",1]`, `[1,2,3,"a","b","z","d"]` and a six-value permutation.
Both seven-value witnesses pass original predicates and fail S-MIX's exact typed
multiset; the permutation passes both. Report these as foundation analytical
controls, with original and strengthened results separate. Remove G-IN's facet
link and require `E_FACET_UNCOVERED` despite all passing originals. C3 later
generalizes the typed observation checker; these concrete source-reviewed
witnesses require no invented engine pass.

B3_04 proves every named mapping/internal/document/target dependency remains
pending with affected IDs. Attempts to promote missing CLI-P8 rows, numeric M1,
native success or resolved semantic-link locations return 1 with
`E_MAPPING_UNPROVEN`. Target inventory remains incomplete and both product
decisions remain unresolved. Independently accept all five B3 UATs, including
Item 2.4's B3_05 reconciliation, before Phase 3.

- [ ] implemented
- [ ] reviewed

## Acceptance ownership and dependencies

Every ID below binds to `TestUAT_<ID>` in `nextflowconformance/` plus the listed
test file. The owner closes the whole acceptance test, including its subcases;
earlier items supply reviewed prerequisites. The dependency column names the
last required item review, in addition to phase entry.

| ID | Owner | Dependency | Test file |
| --- | --- | --- | --- |
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

## Exit conditions

All 17 Phase 2 UATs pass. Independent acceptance covers F11's eleven pairs and
guard-removal red proof, retained Item 2.1, eighteen active schemas, current
model/schema/CLI reconciliation, reviewed fixture labels, single-generation
loading, complete extraction/history, original bootstrap obligations, frozen
lossless projection, fixed selectors and the dependency extension. Items
2.2/2.1/2.3/2.4 pass before Item 2.5 starts. No new checked mark is awarded by
this plan revision.

A2 extraction initializes from genuine lock+batches before B1. Full validation
succeeds after genuine B1 ledger initialization. Run after reviewed semantic
records, extraction snapshots and extension records agree:

```bash
timeout 2m go run ./cmd/wr-nextflow-conformance extract
timeout 2m go run ./cmd/wr-nextflow-conformance validate
timeout 2m go run ./cmd/wr-nextflow-conformance extract --check
timeout 10m env CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -run '^TestUAT_(A2|B1|B2|B3)_[0-9]+$'
timeout 10m golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

Read JSON and diagnostics, not exit 0 alone. Record nonzero pending semantic,
reference, mapping/internal/document and target counts; both product decisions
and no wr adapter remain. Validation claims `records-valid` only. Source
projection/acquisition supplies no family execution or portable proof. C1/C2/C3,
D1/D2/D3, E1/E2/E3 and F1/F2/F3 remain incomplete until their own reviews.

[Phase 3](phase3.md) Item 3.2 owns the separate import of all 69 foundation
requirement/UAT obligations. These 17 Phase 2 UATs do not complete that import
or the linked whole-target, typed, JVM/plugin and durable wr milestones.
