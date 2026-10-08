# Phase 5: Implement E1, E2 and E3 with genuine finite family proof

Ref: [spec.md](spec.md) sections E1, E2, E3

## Instructions

Begin after [phase4.md](phase4.md) exit conditions and independent review pass.

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

Require current independent selection, source, fixture, expectation, mapping,
observer, dependency and actual-result review for each genuine family. The
original seven E1 cases, eighteen E2 accounting mutations and three E2 semantic
observer mutations remain independent obligations beside E3's additions. Review
the actual diagnostic literals and corrected supervisor receipts; historical
pilot research passes supply no current foundation execution.

Retain all 69 imported foundation IDs, original 49 plus twenty additions, with
their complete spec subcases and provenance. Preserve their one-owner bindings;
this phase owns only the eleven E IDs below. Before E1 or E3 readiness work,
the queue owner assigns named authors/implementors and independent source,
observer/mapping, expectation, dependency, selector, launch and result
reviewers. Readiness review accepts the complete current inputs, fixed recipes,
argv/environment, isolation and corrected cleanup proof before execution;
result review accepts actual build outputs and engine receipts afterwards.
Changed inputs require readiness reapproval and fresh affected execution.

Use D3's 300-second native-spec, 120-second neutral/CLI-invocation, 540-second
original-CLI-runner and 1,800-second offline-recipe limits. Only E3 integration
bindings may declare up to 3,600 seconds with a one-hour suite ceiling here.

## Items

### Item 5.1: E1 - Run real pinned oracle cases with honest claim boundaries

spec.md section: E1

Implement `nextflowconformance/oracle.go` and the seven actual workflow/config
cases under `nextflowconformance/data/cases/`. Cover all four acceptance tests
in `nextflowconformance/oracle_test.go`: `E1_01`, `E1_02`, `E1_03`, and `E1_04`.
Execute the unchanged opaque Nextflow 26.04.6 distribution by absolute path
through its embedded `NXF_PACK=dist` launcher, which passes that same file to
Java 21. Keep the separately acquired launcher as provenance; its default `one`
package download path is outside this execution contract. Use parser v2, local
executor, two-task concurrency, static typing disabled, no plugins, disabled
automatic updates, isolated work and empty user home directories, and a private
Nextflow home. Enforce network denial and record its mechanism. Independently
review each workflow, config, and expectation before execution. Record verified
source and full-file distribution hashes, version output, argv, effective
environment, trace, stdout, stderr, and produced files. Use only the acquired
external execution closure. POM metadata and bundled classes do not create
additional runtime files or prove a complete Maven inventory. Successful offline
runs prove closure sufficiency for these seven cases only. A missing external
prerequisite fails the UAT and blocks this item without a skip or fixture
substitute.

Run `ORACLE_MAP`, `ORACLE_MAP_NULL`, `ORACLE_MIX`, `ORACLE_EMPTY`,
`ORACLE_IMPORT`, `ORACLE_FAIR`, and `ORACLE_FILE_ERROR`. Independently review
exact import and missing-output literals authored from pinned error/formatter
source before execution. An unapproved contract remains incomplete. Preserve
actual spelling or semantic disagreements for independent source-based
resolution; observed output cannot set expected truth. Check diagnostic stage,
category, source location, message literals, task count, and nonzero workflow
exit. Enforce zero value lines on both error cases and task script exit 0 on the
missing-output case. The empty
case requires zero value lines, zero tasks, and workflow exit 0. Retain raw
output and check every prefixed observation's schema; missing, extra, or
malformed values fail rather than being filtered away.

Submit fair tasks A then B with `maxForks 2` and tags A/B. A waits for a file
the Go supervisor releases only after Nextflow's trace records B's completion.
Bound polling under the case deadline; absent completion evidence fails the
harness. Prove trace completion order B,A while the downstream observer emits
A,B, each with exactly one file containing its declared bytes. Retain task IDs,
artifact hashes, and downstream emission evidence. Retain raw observations on
contradictions. The contradictory map expectation must fail `E_EXPECTATION` and
generate an unresolved disagreement candidate without approving it or altering
expected data. Keep oracle, foundation, and runtime evidence distinct; report
adapter absence and zero wr runtime passes on the restored tree.

The Item 5.1 implementor owns E1 case/observer readiness; a separate source
reviewer owns workflows/configs/diagnostic/expected acceptance before execution.
Record their identities and current hashes. E1_01 retains all seven actual runs;
E1_02 proves both raw B,A trace completion and A,B downstream emission, not only
priority or sorted output. Removing downstream emission evidence must leave
E1_02 incomplete even when priority and task counts match. E1_03 runs bootstrap
verification and separately checks wr-runtime's exit 1, E_ADAPTER_UNAVAILABLE
and zero wr passes. A fixture executable printing expected values cannot change
that result. Keep oracle and wr counts separate; E1_04 retains contradiction and
unresolved candidate without changing expected truth.
Missing prerequisites or network enforcement block this item. Ordinary E1
deadlines remain within D1; genuine workflow launches use the reviewed private
route and corrected D3 supervision.

- [ ] implemented
- [ ] reviewed

### Item 5.2: E2 - Detect every deliberate corruption for its intended reason

spec.md section: E2

After Item 5.1 review, implement mutation accounting in
`nextflowconformance/coverage.go` and reviewed fixtures in
`nextflowconformance/testdata/`. Cover all three acceptance tests in
`nextflowconformance/adversarial_test.go`: `E2_01`, `E2_02`, and `E2_03`.
Execute every named mutation from E2's 18-row manifest against an independent
known-valid temporary fixture copy. Record its starting hash, one change,
invoked command, expected exit and diagnostic, and protected acceptance ID.
After actual diagnostic contracts are approved, regenerate active fixtures
explicitly for required `cross_refs` and extraction generations. Preflight each
unmodified fixture with its control command and `extract --check` where
applicable. Verify the active manifest, retained hash-valid predecessor chain,
snapshots, review input catalogs and original payloads, including separately
bound expectations, fixtures, normalization and binding files. Current gates
need current accepted, independent, non-stale reviews; retained reviews alone
cannot approve a baseline. Keep historical review bytes unchanged.

The clean baseline must pass. Choose the accounting or observation command that
diagnoses each intended change, preserving unrelated generation inputs and
loader validity; do not repair the corruption or approve it as new input. The
declared exit and diagnostic must kill each mutation. Unrelated errors, crashes
and unchanged inputs remain invalid or surviving controls. For `E2_02`, change
one control to return an unrelated error or remove its intended input change;
record invalid or survived and require foundation verification to return 1 with
`E_MUTATION_NOT_KILLED`. `E2_01` reports `mutations:18`, `killed:18`,
`survived:0`, and `invalid:0`. Keep mutation results as foundation evidence.

Also prove the three semantic observer mutations for null retention, multiset
deduplication, and sorted fair output fail `E_EXPECTATION`. Replay raw evidence
and reject normalization changes that conceal them. Label these as faulty
fixture subjects, preserving later wr implementation mutation work as
outstanding.

Reconcile all eighteen active schemas and all eleven review-input categories
before approving clean hostile fixture baselines. Each accounting mutation
changes one intended accounting/observation layer, preserving unrelated inputs
and independent baseline validity. Execute and retain the manifest's command,
exit and intended diagnostic; crashes, unchanged input and unrelated schema
failures count invalid/survived. The three E2 semantic subjects are separate
from E3's five typed semantic mutations and cannot replace them.

- [ ] implemented
- [ ] reviewed

### Item 5.3: E3 - Review exact family selection and observer readiness

spec.md section: E3; Architecture, Bounded accounting; B3 and C3

After Item 5.2 review, freeze the actual family readiness handoffs in
`suite.json`, reviewed mapping/contracts and `data/tools/` sources. Preserve
pilot `contracts.md`, `inventory.json`, `expectations.json` and all original/
document fixtures as authority; use Item 2.13's independently accepted lossless
projection, never reconstruct expected truth from observed logs. Bind raw
source/helper/config/fixture hashes, exact callback/collection instrumentation
and every required expected/normalization/completion input.

NF_PARSER uses the following pinned source file:

```text
modules/nf-lang/src/test/groovy/nextflow/script/parser/ScriptAstBuilderTest.groovy
```

Use these exact ordered feature names:

```text
should report an error for invalid syntax
should report an error for mixing script declarations with statements
should report an error for params block without an entry workflow
```

Preserve P1-P8 order, one original shared parser, original TestUtils.check and
all helpers/config/classpath obligations. NF_MIX uses
`modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy` and:

```text
should mix channels
should mix with value channels
should mix with two singleton
```

Retain inherited BaseSpec/Dsl2Spec setup/cleanup/reset, MockSession, helper/
network/last-result/config closure and original five-second feature timeouts. No
internal state equivalent is inferred from raw scalar values.

NF_FILES uses unchanged `tests/process-arity.nf`, `tests/topic-channel.nf`,
their `tests/checks/<script>/.checks`, topic `.expected`,
`tests/nextflow.config`, `tests/checks/run.sh` and frozen ignore files. Preserve
ordered invocations ARITY-FRESH, ARITY-RESUME, TOPIC-FRESH and TOPIC-RESUME; map
logical cwd and literal argv to verified paths only. The reviewed expansion is:

```text
cwd: disposable-layout/tests/checks/process-arity.nf
argv: <pinned-dist> -q run ../../process-arity.nf
argv: <pinned-dist> -q run ../../process-arity.nf -resume
cwd: disposable-layout/tests/checks/topic-channel.nf
argv: <pinned-dist> -q run ../../topic-channel.nf
argv: <pinned-dist> -q run ../../topic-channel.nf -resume
```

Record literal source expressions, original runner Bash aggregates,
NXF_CMD/NXF_RUN/TEST_JDK/NXF_WORK/WITH_DOCKER, actual cwd/config/environment,
tput/Bash/tools and all resource identities; declared containers prove no
container execution. Only owned disposable layouts may run the original runner's
destructive scratch commands. Feature selection uses literal names, never regex.
All selections come from verified entries of the same pinned 2,856-file tree
without rewriting the 160 selections or eighteen A2 batches.

The Item 5.3 implementor owns selector/observer/expected preparation;
independent source reviewers own accepted selection/instrumentation/mapping and
expectation arguments, and the dependency reviewer owns the actual
extension/recipe sufficiency. Assign every readiness/build/launch/result role
listed in Instructions before readiness work. Accept complete current bundles
before Items 5.4-5.7 compile or launch; review actual generated build outputs
before dependent engine launches. Subsequent observer or closure changes
require new readiness review and invalidate old attempts. Preserve all
original/neutral/completion fixed denominators, selected unavailable originals
as incomplete, and seven
helper/internal equivalence obligations as pending.

- [ ] implemented
- [ ] reviewed

### Item 5.4: E3 - Execute all unchanged native originals and CLI pairs

spec.md section: E3_01 native component; D3

After Item 5.3 readiness review, compile unchanged selected specs/helpers using
actual fixed recipes and retain new command/classpath/output digests;
historical compiled outputs do not supply build success. Independent build
result review accepts those outputs before launch. Then launch all six exact
features and four original fresh/resume invocations through `reference.go` with
the genuine reviewed compiled source/JVM closure, enforced network denial and
corrected supervision. Preserve source order/shared state, original assertions,
Mix reset/config/five-second deadlines and Bash aggregate behavior. Retain XML,
reached checks, original aggregates, every invocation exit/signal/stdout/stderr,
exact topic bytes, completion and cleanup receipts.

Native evidence must show zero failures/skips, all 42 reached original
predicates and fifteen original completion receipts. Missing features/fixtures/
resume/completion return the specific D3/B3 failures with exact original IDs.
Native internal assertions create no raw neutral identity/lifecycle equality.
Independent actual-result review accepts this native component before the
neutral family handoffs. Item 5.8 owns the combined E3_01 UAT.

- [ ] implemented
- [ ] reviewed

### Item 5.5: E3 - Execute parser scalars and separate CLI mapping probes

spec.md section: E3_01 NF_PARSER neutral component; C3_04

After Item 5.4 review, publish/build the genuine parser/TestUtils scalar
observer from reviewed `data/tools/` source. Execute P1-P8 in their original
order against the same genuine shared parser boundary. Check all 29
count/location/message predicates and all eight authored normalization fixtures,
including P6 substring semantics and P2 literal backslash+n. Retain raw
callback/parser rows, expected hashes and collection/engine/ supervisor
receipts. Distinguish JVM objects from neutral typed scalars.

Separately execute all eight CLI parser probes using the reviewed formatter
boundary. P1-P7 preserve their 28 scalar projections. Retain CLI-P8's actual
required-greeting runtime failure as unresolved CLI_P8_MAPPING, without adding a
greeting default or promoting absent syntax rows to count zero. Its genuine JVM
parser count zero is a separate pass. Expected contracts remain frozen. The
eight JVM units count in the fifteen primary neutral units; eight CLI probes
have a separate denominator. Independent actual-result review precedes Item 5.6;
this supports E3_01, owned by Item 5.8.

- [ ] implemented
- [ ] reviewed

### Item 5.6: E3 - Execute typed Mix values and terminal collection

spec.md section: E3_01 NF_MIX neutral component; C3

After Item 5.5 review, execute the three independently authored entry workflows
preserving M1-M3's selected expressions, with reviewed callbacks tagging values
before rendering and retaining collection instrumentation only. Preserve
original workflow/ process/shell bytes. Check all nine original typed
predicates: M1's six members and not-c, M2/M3's sorted equality/multiplicity.
Separately check S-MIX's exact six-item multiset of integer 1,2,3 and string
a,b,z, with complete collection/engine/supervisor receipts and no
duplicate/extra value.

Retain all raw typed events and unchanged contract bytes. Do not confuse this
mixed-value M1 with D-MIX-EXAMPLE's unexecuted string multiset 1,2,3,a,b,z;
D-MIX-EXAMPLE/D-MIX-COMPLETION and helper internals remain pending. Report
original and strengthening results separately. These three launches are the Mix
portion of fifteen primary units. Independently review actual observations
before Item 5.7; Item 5.8 closes E3_01.

- [ ] implemented
- [ ] reviewed

### Item 5.7: E3 - Evaluate file contracts and four genuine gap workflows

spec.md section: E3_01 NF_FILES neutral component; C3_04

After Item 5.6 review, independently evaluate the four original CLI invocations'
stronger shared S-ARITY/S-TOPIC contracts. All four exits and topic's two exact
22-byte `bar: 0.9.0\nfoo: 0.1.0\n` files with final LF supply six exit/byte
contracts. Preserve original Bash verdicts separately; these stronger checks
create no extra neutral launches, raw topic order or multiplicity, cache
correctness or actual container proof.

Execute all four frozen gap workflows with genuine pinned Nextflow and reviewed
typed observers. G-IN/G-OUT require the associated process/path/
declared-two/actual-one input/output diagnostic, not generic tool/parser/count
failure. G-OUT additionally proves process script exit zero and one.txt bytes
`one\n`. G-SHAPE-FILE and G-SHAPE-LIST each emit one outer item containing the
same four-byte file, retaining file versus List<file> before rendering. These
are the four gap portion of fifteen primary neutral units. Preserve raw
files/events/trace/exits and all terminal receipts. Missing or unrelated errors
fail `E_EXPECTATION`; completed prefixes without receipts stay incomplete.
Independently review actual observations against frozen expectations;
Item 5.8 owns E3_01.

- [ ] implemented
- [ ] reviewed

### Item 5.8: E3 - Accept the complete measured-family execution gate

spec.md section: E3_01

After Items 5.4-5.7 review, close `TestUAT_E3_01` in
`nextflowconformance/families_test.go` from current genuine native and neutral
attempts and accepted independent result reviews. After this item's code/input
changes, reapprove current readiness, rebuild affected compiled inputs and rerun
all required native, neutral and CLI routes under current D2 hashes. Earlier
component captures supply readiness only while current; stale captures receive
zero result credit. Independently accept every new actual result before closing
E3_01. Require exactly all six unchanged native features, four original CLI
invocations, 42 original predicates and fifteen original completions. Require
fifteen primary neutral units: eight
JVM parser, three Mix and four gap units. Separately require eight CLI parser
mapping probes and the stronger six exit/byte contracts on four existing CLI
invocations. Check 29 JVM parser and 28 CLI-P1-P7 scalars, nine Mix predicates,
S-MIX/completion and all four gap cases.

Required family contracts pass with zero missing/skip/fail/timeout/stale; CLI-P8
retains its actual failed CLI mapping as a named pending dependency. Preserve
original, neutral, strengthening and document-gap counts separately. No
helper-equivalence, string-Mix, containers, wr or full-target pass follows. Use
genuine resources and current readiness/review hashes; a prerequisite failure
fails rather than skips this integration UAT. Record E3's distinct
up-to-3,600-second binding deadline and one-hour suite ceiling for actual
compile/execution. Paired controls in the next items remain required for the
phase and final foundation gate.

- [ ] implemented
- [ ] reviewed

### Item 5.9: E3 - Prove all 155 intact/loss controls with fixed subjects

spec.md section: E3_02

After Item 5.8 review, own `TestUAT_E3_02` in `families_test.go`. Reuse all
frozen loss subjects with exact identity/input/expected/control manifest
bindings; preserve pilot records and operate only on independent copied
subjects. There are eleven unit, 42 predicate, 83 fixture, two resume, fifteen
completion and two final-LF losses, totaling 155. Each subject has an
independently intact counterpart that passes the same gate first.

Remove only the declared selected unit, predicate, fixture, resume invocation,
completion, reviewed observer receipt or final LF. The altered gate returns its
intended `E_UPSTREAM_UNACCOUNTED`, `E_ARTIFACT_HASH` or
`E_OBSERVATION_INCOMPLETE`, with exact missing origin ID/hash/member and fixed
required counts. Preserve related
generation/schema validity so unrelated errors cannot count killed. Report 155
rejected, 155 accepted and zero invalid controls; a missing subject or weak
fixture proof fails. These accounting/byte losses award no mutated
engine-implementation proof. Split controls by coherent family/kind for fresh
context, with independent input/code acceptance of each split and one complete
155-pair reconciliation before closing this item.

- [ ] implemented
- [ ] reviewed

### Item 5.10: E3 - Execute six Bash controls and three analytical witnesses

spec.md section: E3_03

After Item 5.9 review, own `TestUAT_E3_03` in `families_test.go`. Execute the
six frozen F1 subjects from pilot inventory `control_contracts.subjects`,
recorded as F1_subjects in control-results.json, with unchanged arity/nullable
checks. In contract order they are F1-ARITY-FRESH-FAIL, F1-ARITY-RESUME-FAIL,
F1-NULLABLE-FRESH-BYTES-FAIL, F1-NULLABLE-RESUME-BYTES-FAIL, F1-ARITY-VALID and
F1-NULLABLE-VALID. Original aggregate exits must be 0,1,0,1,0,0; stronger gates
reject precisely four bad subjects and accept both valid ones. Capture each
actual invocation exit, pipeline/tee status, exact nullable `empty input\n\n`
bytes and terminal supervision separately. Original aggregate weakness stays
visible.

Evaluate the three B3_03 M1 witnesses independently under the original seven
predicates and exact S-MIX multiset. Both seven-value extras pass originals and
fail strengthening; the six-value permutation passes both. Preserve
analytical/foundation provenance; Bash subjects prove no nullable DSL/Nextflow
or engine pass. Tool/subject execution failure is invalid with
`E_MUTATION_NOT_KILLED`, not a killed subject. Review raw receipts and all
expected outcomes without changing frozen control contracts.

- [ ] implemented
- [ ] reviewed

### Item 5.11: E3 - Detect five typed semantic observer faults

spec.md section: E3_04

After Item 5.10 review, own `TestUAT_E3_04` in `families_test.go`. Prepare
independent intact/faulty fixture subjects and prove each intact baseline
against current reviewed mappings, expected truth and raw observations. Execute
five single faults: stringify S-MIX's integers; deduplicate ORACLE_MIX's
[1,1,2]; accept the correct complete S-MIX value prefix without terminal
collection; accept G-IN's unrelated-process error; collapse G-SHAPE-LIST to a
bare file. All five fail `E_EXPECTATION` or `E_OBSERVATION_INCOMPLETE` for their
intended reason, with five killed, zero survived and zero invalid. Each fault's
raw discriminator and exact diagnostic must be retained for independent review.

Never weaken/sort expected data or add normalization to conceal a fault. These
five typed controls supplement E2's three named observer controls. They are
faulty fixture subjects, not mutated wr implementations. Real wr implementation
mutations remain a later runtime obligation.

- [ ] implemented
- [ ] reviewed

## Acceptance ownership and dependencies

Every ID below binds to `TestUAT_<ID>` in `nextflowconformance/` plus the listed
test file. The owner closes the whole acceptance test, including its subcases;
earlier items supply reviewed prerequisites. The dependency column names the
last required item review, in addition to phase entry.

| ID | Owner | Dependency | Test file |
| --- | --- | --- | --- |
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

## Exit conditions

All eleven E UATs pass with current independent reviews. Retain seven actual E1
offline oracle runs, exact import/missing-output diagnostics and fair trace/
emission order; eighteen E2 accounting kills and three E2 semantic failures; all
three genuine E3 families and their distinct denominators; all 155 paired losses
accepted/rejected as specified; six Bash outcomes, three M1 witnesses and five
semantic observer kills with zero invalid controls. Every launch and executed
subject has complete raw and stopped-supervision receipts; analytical witnesses
retain independently reviewed evaluation receipts.

After the final Item 5.11 code/input changes, freeze and independently reapprove
all current readiness inputs. Rebuild affected compiled sources and execute
fresh seven-case E1 and complete E3 native/neutral/CLI gates under the final D2
input keys, then rerun all E2/E3 controls against current intact baselines.
Independently accept those actual results before phase closure. Recompute inputs
after execution and verification; any further bound-input change requires
affected reruns and result reacceptance. Earlier captures retain historical
status only when stale. Result-review receipts belong to evidence closure;
they cannot become expected truth or extraction inputs that create hash cycles.
Capture the focused gate and accepted result receipts before verification:

```bash
timeout 60m env CGO_ENABLED=1 go test -tags netgo -count=1 -timeout=60m ./nextflowconformance ./cmd/wr-nextflow-conformance -run '^TestUAT_E[123]_[0-9]+$'
timeout 2m go run ./cmd/wr-nextflow-conformance verify --suite wr-runtime
timeout 10m golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

The integration command and bindings record D3's distinct E3 deadlines; split
focused invocations under reviewed bounds if required without omitting any ID.
Each split sets Go's test timeout to its reviewed bound within the one-hour
suite ceiling; the outer timeout alone leaves Go's ten-minute default active.
wr-runtime returns 1 with `E_ADAPTER_UNAVAILABLE` and zero wr passes. Check that
expected nonzero result explicitly. CLI-P8, string Mix, all seven internal
obligations, five document closure links, full target inventory and both product
decisions remain pending. Native source review, copied resources and local
successful runs still supply no F3 fresh-checkout proof.
