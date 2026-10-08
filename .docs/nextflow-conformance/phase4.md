# Phase 4: Implement D1, D2 and D3

Ref: [spec.md](spec.md) sections D1, D2, D3

## Instructions

Begin after [phase3.md](phase3.md) exit conditions and independent review pass.

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
and unchecked items below require implementation and evidence. Preserve all 69
foundation obligation IDs and their complete imported subcases from Item 3.2.

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

Keep ordinary D1 UAT deadlines at 1-180 seconds and its twenty-minute suite
ceiling. D3 gives native specs 300 seconds, neutral/CLI invocations 120,
original CLI runner 540 and offline compile recipes 1,800. Only E3/F3
integration bindings may declare 3,600 seconds and a one-hour suite ceiling;
record those distinct deadlines instead of globally raising D1 limits.

## Items

### Item 4.1: D1 - Derive status from complete execution events

spec.md section: D1

Extend `nextflowconformance/runner.go` to record runner-owned subprocess events,
stdout, stderr, exits, and artifacts. Cover all four acceptance tests in
`nextflowconformance/runner_test.go`: `D1_01`, `D1_02`, `D1_03`, and `D1_04`.
Require exact run/terminal/package events, successful exits, completed subtests,
and observations for a pass. Reject malformed, contradictory, missing, and
truncated evidence at the recorder boundary.

For D1_01, the real intact fixture exits 0 with executed 1 and passed 1; verify
its raw event log and manifest hashes. For D1_02, separate skip, fail,
one-second timeout, exit-0/no-match and missing-observation fixtures return 1
with `E_TEST_SKIPPED`, `E_TEST_FAILED`, `E_TEST_TIMEOUT`, `E_TEST_NOT_RUN` and
`E_OBSERVATION_MISSING`, respectively. Each awards zero passes; timeout cleanup
leaves no child alive. Enforce per-UAT deadlines, the suite ceiling,
process-group cleanup and log limits. Publish manifests atomically only after
closing and hashing logs.

Require every selected subtest to finish without skip/fail; unknown, duplicate,
contradictory, unmatched or truncated events invalidate an attempt. A package
pass or no-tests exit cannot replace exact test run/pass events and required
observations. Bound each log to 16 MiB; over-limit terminates the process with
`E_OUTPUT_LIMIT` rather than truncating a successful result. Propagate context
cancellation to readers, processes and cleanup. In-progress manifests award
nothing. D1_03 feeds malformed/truncated JSON, unmatched passes and duplicate
terminal events through the recorder and returns 2 with `E_TEST_EVENTS`. A
passing test inside a failing package returns 1 with `E_TEST_FAILED`. D1_04
interrupts between log creation and publication; verify returns 1 with
`E_ATTEMPT_INCOMPLETE`. An old attempt with different inputs cannot substitute.

- [ ] implemented
- [ ] reviewed

### Item 4.2: D2 - Bind evidence to the code, corpus, tests, and environment

spec.md section: D2

After Item 4.1 review, implement `nextflowconformance/evidence.go`. Cover all
five acceptance tests in `nextflowconformance/evidence_test.go`: `D2_01`,
`D2_02`, `D2_03`, `D2_04`, and `D2_05`. Hash the complete D2 input inventory
before and after execution, including dirty and untracked inputs, active source
and dependencies outside the bound package, tool identities, effective
environment, and applicable runtime artifacts. Include the opaque runtime's
full-file hash, Java tree, required tools, and actual external JARs; retain POM
provenance through the lock and corpus input hashes. For `D2_01`, change the
distribution independently of the other input subcases and require
`E_EVIDENCE_STALE` with passed count 0. Bundled classes remain covered by the
full distribution hash.

Include the active extraction manifest, input catalog, every reachable snapshot
and payload, plus current bound inputs and recorded absence in both input
inventories. Retained generations are authoritative evidence; exclude only the
fixed output classes in D2. Prove changed generation or bound semantic bytes
invalidate an attempt even before re-extraction, and missing/damaged retained
payloads cannot count as current evidence. D2_01 first verifies an unchanged
completed fixture, then independently changes implementation bytes with
unchanged HEAD, a corpus block, test bytes, expected bytes, normalization, build
tags or the distribution. Each yields `E_EVIDENCE_STALE` and zero passes. D2_02
deletes a raw event log or alters artifact bytes; return 2 with
`E_EVIDENCE_MISSING` or `E_ARTIFACT_HASH`, respectively. Setting manifest status
to `passed` cannot change either failure. Start children from an allowlisted
environment.

Revalidate raw events and artifacts on verify. Select the newest completed
attempt for the exact input key by runner sequence, preserving historical
failures and stale records. Prove input-change races with a controlled barrier,
newer-failure precedence, and generated-view independence.

Hash newly introduced upstream/mapping/contract/dependency/suite records, all
observer/wrapper/supervisor sources and fixed recipe inputs too. Include Git
commit plus dirty tracked/untracked content, embedded files, module/build
configuration, compiler, go.mod/go.sum, discovery hash and active dependencies
from `go list`. Fixed excluded classes are generated views/cache/attempt outputs
only. No record can add arbitrary exclusions. Bind PATH resolutions,
shell/Go/Java executables, OS/architecture, locale/timezone and every effective
allowlisted child key/value; bootstrap environments carry no secrets or ambient
config/plugins/credentials. Record normalized environment separately.

Verify the newest completed exact suite/input key by monotonic sequence;
editable timestamps or status cannot resurrect an earlier pass. Select a newer
failure, re-read raw events/artifacts and recompute current inputs. D2_03's
barrier-controlled mutation returns 1 with `E_INPUT_CHANGED` and no current
pass. D2_04 records pass then failure with identical hashes and moves the older
pass's timestamp forward; verification must still select the failure. D2_05
keeps a generated Markdown edit outside the runtime input key while render check
fails; rerendering never fabricates a new execution.

- [ ] implemented
- [ ] reviewed

### Item 4.3: D3 - Supervise every owned descendant through termination

spec.md section: D3_03; D1 cleanup extension

After Item 4.2 review, implement and publish the fixed supervisor sources used
by private tool routes under `nextflowconformance/data/tools/` and extend Item
3.6's reviewed narrow Go launch/cleanup primitive. Track current owned
process/thread identities and relationships, including detached descendants,
TERM-ignoring children and children created during termination. Escalate KILL,
drain bounded streams, wait/reap and verify every stopped state before completed
supervision. Preserve historical PREREQ-F1 defect evidence unchanged; only
corrected actual receipts can satisfy this gate. Unrelated processes remain
untouched.

Own D3_03 in `reference_test.go`. A controlled subject creates the specified
detached TERM-ignoring child and termination-time grandchild; at one second all
owned children are killed/reaped, timeout returns 1 with `E_TEST_TIMEOUT`,
stopped receipts name every child and passes stay zero. Prove a fabricated
stopped summary without receipts returns `E_OBSERVATION_INCOMPLETE`. Unknown
descendants/cleanup states cannot produce a completed attempt. Review test
strength with the known faulty early-completion behavior in an owned disposable
copy and restore. Phase 6 separately extends this same published supervision
boundary for Linux syscall/socket lane enforcement.

- [ ] implemented
- [ ] reviewed

### Item 4.4: D3 - Launch genuine originals with frozen selection and closure

spec.md section: D3_01; Architecture, Independent suite records; E3 launch
prerequisite

After Item 4.3 review, implement `nextflowconformance/reference.go` and the
private `reference --family ID` command using only the suite's frozen literal
selectors and reviewed source/helper/config fixtures. Execute unchanged native
originals; no user-supplied executable/argv or generated fake spec is allowed.
Build genuine specs/helpers from published source with fixed reviewed offline
recipes; retain toolchain/classpath identities, logical/effective argv/env,
source/build output digests, command XML/stdout/stderr/exit and supervisor
receipts. Compilation output identity is distinct from acquired resources. Use
original parser shared instance/TestUtils order and Mix reset/config/
MockSession/five-second feature deadlines. Preserve fresh/resume CLI layout and
original Bash aggregates. Supply an intact original CLI fresh/resume pair for
D3_01's resume-loss subcase, retaining both actual invocation/completion
receipts.

Before readiness work, the queue owner names compile/native and neutral launch
implementors plus source, observer, dependency, selector, launch and result
reviewers, each independent of the relevant author/implementor. Source review
accepts unchanged originals/helpers/fixtures and independent expected bytes;
observer review accepts callback/decoder sources and the mapping argument.
Dependency review accepts actual executable closure and fixed offline recipe
inputs. Selector review accepts exact ordered names/invocations, fixed required
sets and current suite/lock authority bindings. Launch review accepts fixed
argv/environment, isolation and Item 4.3's cleanup proof. Current accepted
reviews of these inputs precede compilation/engine launch; result reviewers
accept actual build/engine receipts afterwards. Reapprove changed inputs.

Item 2.15's acquired closure and historical pilot success prove no new build or
execution. Compile current selected upstream specs/helpers offline, retain
actual output digests and successful command receipts, and run a genuine native
smoke under the accepted selectors. Independent build/result review must pass
before Item 4.4 closes. For neutral readiness, use Item 3.6's published
`mix-smoke.nf`, `mix-smoke.config`, observer and narrow launch primitive under
Item 4.3 supervision. Require its actual accepted smoke and current exact-byte
source/observer/expected/dependency/selection/launch reviews; stale inputs need
fresh smoke execution and result review. Missing resource returns
`E_DEPENDENCY_MISSING` before build/engine execution. Enforce offline networking
and bounded descendant cleanup; no implicit fetch.

Produce an actual selected-feature attempt with complete native XML, exact
order, reached original checks, unchanged helper assertions and stopped
supervision. It reports only original native passes, no invented neutral values
or observed internal identity. This establishes the native launch prerequisite;
Item 4.5 owns the complete D3_01/D3_02 acceptance tests using fresh attempts. E3
later runs all required original families and frozen completion denominators.

- [ ] implemented
- [ ] reviewed

### Item 4.5: D3 - Validate route attempts, freshness and artifact replay

spec.md section: D3_01 and D3_02

After Item 4.4 review, extend attempts/bindings/results in `reference.go`,
`evidence.go` and `runner.go`. Require engine, route, family_id, contract_ids,
mapping_ids, dependency_lock, selection and result_review. Foundation fixture
fields are explicit nulls/empty arrays, never inferred engine identity. Native
attempts retain original per-feature XML/checks, invocation exits, selection and
completion. Neutral attempts bind one reviewed expected contract,
observer/mapping source argument, dependency inputs and raw typed receipts.
Result review is a hashed independently accepted receipt or null until review.
Retain all raw artifacts/result-review bytes in the attempt evidence closure;
attempts cannot become extraction review inputs or create hash cycles.

Close D3_01/D3_02 in `reference_test.go`. Item 4.4's accepted compiled native
smoke and Item 3.6's actual neutral smoke supply readiness, not new result
credit. After this item's code/input changes, record fresh genuine native and
separate neutral attempts under current D2 hashes and reaccepted readiness;
rebuild changed compiled inputs and review each new actual result. Rerun the
intact original CLI pair before its resume-loss subcase. Native success records
originals only; neutral success records only preserved observable predicates.
Helper session/MockSession/engine-exit success creates no neutral raw-object
identity/lifecycle/equality claim. Remove feature, resume or terminal receipt
independently and return 1 with `E_NATIVE_INCOMPLETE` naming its original ID.
Actual raw original/candidate equality would need both genuine captures plus
instrumentation review; unsupported internal equivalence stays pending. Preserve
source-selection/accounting and original XML predicate execution separately from
unobserved native assertion values. The unavailable wr route retains null
binding/no observer, returns 1 with `E_ADAPTER_UNAVAILABLE`, creates no
observation and awards zero wr passes.

Independently change observer code, mapping source argument, extension resource,
build recipe and selected feature order; affected old attempts become stale with
`E_EVIDENCE_STALE` and zero current passes. Missing callback/XML bytes returns
`E_EVIDENCE_MISSING`. Replay identical receipts as `artifact-replay`, zero
executed and zero new engine passes. A newer genuine failed attempt wins for the
same D2 input/route key; replay cannot supersede or revive it. Separate
partial/unresolved mappings from execution status and preserve them as
incomplete regardless of a native/neutral run. F3 later exposes portable
package/replay commands; this item establishes their evidence semantics.

- [ ] implemented
- [ ] reviewed

## Acceptance ownership and dependencies

Every ID below binds to `TestUAT_<ID>` in `nextflowconformance/` plus the listed
test file. The owner closes the whole acceptance test, including its subcases;
earlier items supply reviewed prerequisites. The dependency column names the
last required item review, in addition to phase entry.

| ID | Owner | Dependency | Test file |
| --- | --- | --- | --- |
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

## Exit conditions

All twelve D UATs pass. Actual recorder subjects, corrected owned-descendant
supervision, genuine compiled native smoke, actual neutral smoke and fresh
native/neutral acceptance attempts with independent result reviews prove route
separation. Reverification rejects stale, corrupt or interrupted evidence;
newest genuine failures supersede prior successes and replay adds zero
executions. Every timeout leaves no owned child alive. Capture complete raw
events, XML/callback/completion and cleanup receipts. Run offline compile and
reviewed readiness smoke commands separately before the focused UAT command,
each with its D3 deadline. Reuse verified build outputs only while their inputs
remain current; changed recipe inputs require fresh build receipts. Capture the
focused gate:

```bash
timeout 30m env CGO_ENABLED=1 go test -tags netgo -count=1 -timeout=30m ./nextflowconformance ./cmd/wr-nextflow-conformance -run '^TestUAT_D[123]_[0-9]+$'
timeout 2m go run ./cmd/wr-nextflow-conformance verify --suite foundation-bootstrap
timeout 10m golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

Split focused invocations under reviewed bounds if required without omitting
any D ID. Each split sets Go's test timeout to its reviewed bound within the
focused command's thirty-minute outer bound.

Record explicit spec/invocation deadlines under D3 within the focused command's
bound; keep offline recipe deadlines in their separate build receipts. Bootstrap
verification still returns incomplete for missing E/F obligations and the
remaining actual family/control proof. Inspect its missing IDs and diagnostic
counts. Record validity, fixture comparison, native smoke and replay do not
award whole-target or wr conformance.
