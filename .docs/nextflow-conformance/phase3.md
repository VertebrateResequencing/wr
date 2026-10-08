# Phase 3: Implement C1, C2 and C3

Ref: [spec.md](spec.md) sections C1, C2, C3

## Instructions

Begin after [phase2.md](phase2.md) exit conditions and independent review pass.

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

C1 consumes C3's authoritative grammar. Item 3.1 supplies that bounded shared
prerequisite before C1 rendering; C3's execution/comparison gates still close in
Items 3.5 and 3.6. Import all 69 numbered acceptance obligations with unchanged
IDs and provenance, preserving the original 49 and adding exactly the twenty
B3/C3/D3/E3/F3 obligations.

## Items

### Item 3.1: C1/C3 - Implement the closed tagged contract grammar

spec.md section: C1 and C3; Architecture, Independent suite records and routes

After phase entry, implement C3's fixed neutral expected/observed grammar in
`nextflowconformance/observe.go` using Item 2.3's reviewed schema shapes.
Expected has exactly exit, values, artifacts, tasks, diagnostics, completion and
predicates. Each of the first six is checked or has a reviewed not-applicable
reason. Keep observed fields unwrapped and separate from expected. Preserve
null/Boolean/integer-decimal/string/file/list tags; integer syntax is
`0|-?[1-9][0-9]*`. Do not coerce files or lists to display strings. Checked
empty emission sequence differs from one emitted empty list.

Validate every fixed JSON Pointer and comparator: equals, contains,
not-contains, sorted-equals and substring. Retain tagged predicate expectations
and source strength; native-object comparators stay unresolved. Known parser
integer/string scalars receive their actual tags, with count-zero nullable
locations/messages only. Completion has independent collection, engine and
supervisor booleans/receipt arrays; each checked true needs raw receipts. Review
local grammar and worked positive/negative forms before C1 uses it. Supply
shared tagged encode/decode, sequence/multiset comparison and raw-receipt replay
primitives here, before Item 3.3 needs them. Item 3.3 adds its error-fixture
decoders; Item 3.5 adds parser/family decoding and closes C3 comparison gates.
This item supports all C3 tests but owns no separate acceptance binding.

- [ ] implemented
- [ ] reviewed

### Item 3.2: C1 - Import all 69 foundation requirements and UAT obligations

spec.md section: C1; C2 binding contract; Implementation Order step 3

After Item 3.1 review, import every numbered acceptance test as exactly one
foundation requirement/UAT with unchanged acceptance ID, complete amended
subcases, original source spans and spec provenance. Independently compare the
import with the accepted spec, the original 49-ID set and the twenty additions.
Phase counts are 7, 17, 12, 12, 11 and 10. Keep actual execution kind separate
from the foundation acceptance test which verifies it.

Keep unfinished cases measurable schema-valid drafts with null fixture,
expected, binding and review fields; record planned ownership without inventing
ready bindings. Ready contracts require reviewed actual input and expected bytes
plus exact discoverable test binding. Retain all A1 acquisition contracts,
including A1_04 extracted-archive safety, A1_05 missing/altered runtime, A1_06
actual opaque distribution and A1_07 shell-prefix/JAR identity controls. Retain
A2 reference/history subcases, B1 freshness, D2_01's seven independent freshness
changes including the whole distribution, and F2 retained reconciliation.
Include B3_05's F11/reconciliation obligation explicitly. Fixture successes
never stand in for native/oracle/wr execution.

Bind each separately stored fixture, expected file, normalization, observer and
binding input explicitly in its catalog category; nested hashes cannot retain
payloads. Include all new upstream/mapping/contract/dependency/suite inputs.
Refresh extraction after reviewed semantic edits. Each current gate uses current
accepted independent non-stale reviews, with retained history reported
separately. This handoff supplies C1/C2 and final F3's 69-ID authority; Item 3.3
owns C1 acceptance tests.

- [ ] implemented
- [ ] reviewed

### Item 3.3: C1 - Render concrete expectations and deterministic views

spec.md section: C1, all four acceptance tests

After Item 3.2 review, implement `nextflowconformance/render.go` and all four C1
tests in `render_test.go`. Use shared tagged contracts, reviewed normalizations
and exact artifact/error expectations. Re-decode raw receipts rather than
trusting a supplied summary. Expected inputs remain read-only. Permit only
run-root prefix replacement, named CRLF-to-LF text conversion and named outer
multiset comparison. Retain before/after forms; global sorting, whitespace
stripping, wildcard regex, error suppression and artifact exclusion fail
`E_NORMALIZATION` before comparison.

C1_01 renders two exact cases, one failed binding and an unresolved decision,
with unchecked status, decision link and byte-identical regeneration/check.
C1_02's missing UAT, changed reviewed hash and hand-edited checkbox produce
`E_UAT_MISSING`, `E_REVIEW_STALE` and `E_RENDER_MISMATCH`. Implement these
review/view checks here; complete attempt freshness stays in D2. C1_03
independently exercises duplicate multiplicity, sequence order, reviewed
unordered equality and rejected global-sort policy.

C1_04 uses independently authored E1 import/missing-output comparison fixtures,
checked empty value sequences, exact stage/category/location/literals, exit,
task and artifact checks. Import requires zero tasks; missing output requires
one task and script exit 0. Replace each diagnostic with missing-Java or append
raw OBS:1 independently; every altered fixture fails `E_EXPECTATION` with
expected files unchanged. Derive literals from pinned source and review before
use; Phase 5 confirms actual runs without copying observed truth.

Render escaped deterministic requirement/case/provenance/decision/evidence pages
and checklists under `.docs/nextflow-conformance/generated/`, retaining stable
IDs and batch/dependency grouping. Follow
ASCII/80-column/one-h1 mechanics; link non-ASCII fixture bytes without changing
them. After import, update records first and regenerate views.

- [ ] implemented
- [ ] reviewed

### Item 3.4: C2 - Discover exact active Go test bindings

spec.md section: C2, all four acceptance tests

After Item 3.3 review, implement discovery in `nextflowconformance/runner.go`
and four C2 tests in `runner_test.go`. Run actual `go list -json` and `go test
-list` under the same CGO/tags/build selection used for execution. Record active
source/dependency files, discovery bytes and exact package/test bindings.
Bindings contain module-local literal packages/test names; reject package
patterns and regex metacharacters. Use anchored regexp-escaped exact selectors
and `-count=1 -json -tags netgo`, recording actual argv.

C2_01 discovers exactly two real fixture declarations, not comments or paths.
C2_02's missing/build-excluded test returns 1 with `E_TEST_MISSING`; invalid
package returns 2 with `E_TEST_DISCOVERY`; executed stays zero. C2_03 launches
only TestUAT_ONE beside TestUAT_ONE_EXTRA in a temporary package. Implement that
narrow execution here; D1/D2 later own full attempt recording/freshness. C2_04
rejects runtime-to-oracle/foundation/native mismatches with exit 2 and
`E_EVIDENCE_KIND` regardless of passing fixture output. Review the complete
69-ID planned/ready/missing inventory honestly; discovery cannot call missing
future
bindings executed.

- [ ] implemented
- [ ] reviewed

### Item 3.5: C3 - Decode raw typed observations and original predicates

spec.md section: C3_01, C3_02 and C3_04

After Item 3.4 review, implement fixed raw decoders/comparison in
`nextflowconformance/observe.go` and the named three tests in `observe_test.go`.
Freeze each reviewed decoder source boundary and normalization input before
comparing. No record text supplies arbitrary executable code or decoders.

C3_01 separates integer 1, string 1, file one.txt and List<file>; exact
identical tags/shapes alone pass. Preserve duplicate counts, nested-list order
and integer 9007199254740993. Name deterministic generated-input properties:
tagged encode/decode round-trip is lossless; outer permutations preserve exactly
multiplicities. Keep all worked examples and fixed seeds, and pin any failing
generated case. A copied comparator implementation is not an independent oracle.
C3_02 first proves the complete reviewed six-item Mix observation passes. Each
collection-terminal, engine-exit and supervisor-stopped receipt deletion then
returns 1 with `E_OBSERVATION_INCOMPLETE`, missing member and
zero passes; extra/malformed values fail `E_OBSERVATION_FORMAT`.

C3_04 replays P1-P7's frozen scalar count/location/message and P6 substring
semantics. Changing P2's literal backslash+n to LF fails `E_EXPECTATION`. Retain
CLI-P8's genuine captured required-greeting failure as `E_MAPPING_UNPROVEN`; its
separate JVM count-zero row does not fix that CLI route. G-IN/G-OUT generic
tool/parser/unrelated-process failures fail; matching associated
process/path/declared-two/actual-one fixtures pass. G-OUT retains script exit 0
and exact one.txt bytes. Replay/comparator fixtures remain foundation evidence
and generate no engine execution identity.

- [ ] implemented
- [ ] reviewed

### Item 3.6: C3 - Share one contract across genuine fixed engine routes

spec.md section: C3_03; Architecture, Independent suite records and routes

After Item 3.5 review, implement the private fixed Nextflow/wr route selection
and `observe --case ID --engine nextflow|wr` CLI boundary. Both routes resolve
one contract ID/hash and the same expected-file bytes. The restored wr route has
null binding, no observer and returns 1 with `E_ADAPTER_UNAVAILABLE`, zero wr
passes and no observation. A route-specific expected hash fails validation with
`E_CONTRACT_DIVERGENCE`. Foundation supplied fixtures cannot become an engine
route or pass by printing expected values.

Own C3_03 in `observe_test.go`, including these prerequisites before its first
genuine launch. Use one new M1 entry workflow preserving the original reviewed
channel expressions and S_MIX's six typed values, with separately reviewed
callback instrumentation. Publish `mix-smoke.nf`, `mix-smoke.config` and any
wrapper source under `nextflowconformance/data/tools/`; bind their exact bytes,
original source spans, mapping, shared expected and normalization files. The
collection terminal callback writes its own raw event; engine exit and cleanup
produce distinct receipts. Do not infer collection termination from exit or a
matching value prefix.

Implement the narrow fixed launch/cleanup primitive in this item. It executes
the unchanged acquired Nextflow 26.04.6 opaque distribution by absolute path
through its embedded `NXF_PACK=dist` launcher with verified Java 21, parser v2,
static typing disabled, local executor and no plugins. Use isolated work, empty
user home and private Nextflow home, disabled automatic updates and enforced
network denial. Rehash the actual distribution and external Java/tool closure
from Item 2.15 offline; metadata or historical cache success proves no execution
closure. Any changed observer/tool resource or fixed recipe requires reviewed
extension records before launch. Retain current suite selection and dependency
review IDs and the lock's suite authority binding.

The queue owner assigns named workflow/observer author, launch implementor,
source reviewer and dependency reviewer before readiness work. The source
reviewer accepts original expressions, callbacks, mapping and independent
expected bytes; the dependency reviewer accepts actual executable resources,
fixed argv/environment and any build recipe/output inputs. The launch
implementor owns the isolation and bounded cleanup proof. These current accepted
readiness reviews precede genuine execution; independent code and actual-result
review
close this item. Input changes require reapproval before rerunning.

Bound the launch to 120 seconds and terminate/wait for every owned process
before emitting a stopped receipt. Retain raw callback/stdout/stderr/trace,
version, logical/effective argv/environment, verified resource identities,
network-denial proof, engine exit and cleanup receipts. Re-decode those fresh
bytes through Item 3.5's fixed decoder and compare read-only shared truth. This
item invokes no future foundation runner, E1 harness or D3 implementation.
Implement `observe`'s new attempt and raw observation records using Phase 2's
reviewed schema shapes and the public JSON result/exit contract. D1/D2/D3 still
own their full event, freshness and route-accounting gates.
Missing closure, callback, isolation or truthful cleanup leaves C3_03 and this
phase incomplete. D3 later extends descendant fault controls and durable route
accounting; E1/E3 require their own fresh executions and readiness reviews.

A source-reviewed contradictory contract fails `E_EXPECTATION`, retains raw
observations and yields an unresolved disagreement candidate, without approving
it or changing expected bytes. Review both paths and actual receipts. This
narrow C3 pass supplies no E1/E3 family, native or wr runtime completion.

- [ ] implemented
- [ ] reviewed

## Acceptance ownership and dependencies

Every ID below binds to `TestUAT_<ID>` in `nextflowconformance/` plus the listed
test file. The owner closes the whole acceptance test, including its subcases;
earlier items supply reviewed prerequisites. The dependency column names the
last required item review, in addition to phase entry.

| ID | Owner | Dependency | Test file |
| --- | --- | --- | --- |
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

## Exit conditions

All twelve C UATs pass. Independent review compares all 69 unchanged IDs,
complete subcases and source provenance with the accepted spec, including the
original 49 and twenty additions. Shared tagged grammar, raw decoding,
normalization and reviewed fixtures are accepted. C3_03 retains an actual
Nextflow attempt and explicit wr absence, with read-only expected truth.
Generated views reproduce current records byte-identically. Capture:

```bash
timeout 2m go run ./cmd/wr-nextflow-conformance render --check
timeout 5m go run ./cmd/wr-nextflow-conformance discover --suite foundation-bootstrap
timeout 15m env CGO_ENABLED=1 go test -tags netgo -count=1 -timeout=15m ./nextflowconformance ./cmd/wr-nextflow-conformance -run '^TestUAT_C[123]_[0-9]+$'
timeout 10m golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

Split focused invocations under reviewed bounds if required without omitting
any C ID. Each split sets Go's test timeout to its reviewed bound within the
focused command's fifteen-minute outer bound.

Check discovery's actual remaining draft/missing bindings. It does not claim all
69 executed. D/E/F evidence, native original routes and fresh-checkout
reconstruction remain incomplete. Both product decisions, all exact pending
suite dependencies and zero wr passes remain visible.
