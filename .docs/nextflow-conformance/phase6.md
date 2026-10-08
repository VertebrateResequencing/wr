# Phase 6: Implement F1, F2 and F3, then execute the 69-UAT gate

Ref: [spec.md](spec.md) sections F1, F2, F3

## Instructions

Begin after [phase5.md](phase5.md) exit conditions and independent review pass.

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

F3 is split into source/closure publication, bundle restoration, portable
identity, Linux supervision, isolated lanes, genuine Gradle IPC, denied
boundaries, missing-input controls and fresh reconstruction. Each split has a
new input review, named readiness owners and independent code/evidence review.
Current genuine build/native attempts must pass the exact fixed IPC policy;
historical recipes/disassembly or socket-free substitutes supply no proof. A
missing Docker/kernel/ptrace/seccomp/tool/resource prerequisite leaves the
affected proof incomplete; no system package installation or broadened socket
permission is authorized.

Before readiness work, the queue owner assigns named source/selection,
fixture/expectation, observer/mapping, closure/recipe/image, build,
build-output, supervision/isolation, Gradle/control, reconstruction/launch,
composition and actual-result owners. Reviewers are independent of the authors
and implementors they review. Accept current readiness before compilation;
accept actual generated build outputs before dependent tool/engine launches;
accept actual results afterwards. Changes require affected readiness reapproval,
rebuilding, rerunning and new result acceptance. Result-review receipts remain
in evidence closure, separate from expected truth and extraction inputs.

Use D3's 300-second native-spec, 120-second neutral/CLI-invocation, 540-second
original-CLI-runner and 1,800-second offline-recipe bounds. Ordinary D1 UATs
retain their 180-second ceiling; only E3/F3 integration bindings may declare
up to 3,600 seconds with a one-hour suite ceiling.

## Items

### Item 6.1: F1 - Seed later milestones without awarding them completion

spec.md section: F1

Extend `nextflowconformance/coverage.go` and the authoritative ledger with all
nine named wr requirements, linked measurable draft UATs, accepted-prompt
provenance, origin `wr`, scope `required`, and null runtime bindings. Keep these
seeds outside the foundation execution denominator and inside the target ledger
and runtime profile. The seed IDs are `WR_IMMUTABLE_RUN`,
`WR_DURABLE_EXPANSION`, `WR_CRASH_BOUNDARIES`, `WR_CONTAINERS`, `WR_RESOURCES`,
`WR_GROUPING`, `WR_OUTPUT_ACCESS`, `WR_INTERMEDIATES`, and `WR_UNSUPPORTED`.
Each draft UAT stores its measurable contract in `cases` and null fixture,
expected, binding, and review fields. Independently review fixture details
before those later milestones execute or complete. Cover all three acceptance
tests in `nextflowconformance/milestones_test.go`: `F1_01`, `F1_02`, and
`F1_03`.

Preserve both unresolved policies and all bootstrap semantics. Enforce the
foundation, target accounting/policy, durable runtime slice, and broad operator
dependency order. Require real crash and runtime evidence for the later claims;
queue, parser, and oracle passes cannot discharge them. Runtime and target
inventory remain incomplete after foundation success. The later durable slice
must cover every supported invocation mode and retain the crash receipts and
logical task IDs required by F1. Actual wr implementation mutations remain
required once an adapter exists.

Under `F1_01`, runtime verification names all nine seeds as incomplete. Under
`F1_02`, a completed broad operator batch without current durable-slice evidence
returns 1 with `E_MILESTONE_DEPENDENCY`. Under `F1_03`, parser diagnostics or
submission-priority evidence for `WR_CRASH_BOUNDARIES` returns 2 with
`E_EVIDENCE_KIND`, leaves its item unchecked, and names the missing crash
artifacts. Logical task deduplication does not prove external side effects
happen exactly once.

Preserve later CLI_P8_MAPPING, STRING_MIX_EXECUTION, INTERNAL_EQUIVALENCE,
DOCUMENT_CLOSURE and FULL_TARGET_INVENTORY dependencies with their exact frozen
affected IDs, beside both product policies and nine wr seeds. Target accounting
must eventually enumerate every original assertion/helper/state/ completion
purpose and compare independently with documentation. Successful native-only
regressions or reviewed unresolved dispositions cannot complete that
neutral/full-target coverage gate. Foundation and research completion claim only
their finite reviewed boundaries.

- [ ] implemented
- [ ] reviewed

### Item 6.2: F2 - Generate bounded handoffs and a durable evidence ledger

spec.md section: F2; Implementation Order final gate

After Item 6.1 review, extend `nextflowconformance/render.go` with generated
batch briefings and a durable ledger. Cover all three acceptance tests in
`nextflowconformance/render_test.go`: `F2_01`, `F2_02`, and `F2_03`. Generate
exact assigned IDs, dependencies, source excerpts and hashes, relevant inputs,
commands and deadlines, unresolved questions, and required profiles. Require
independent approval of bounded input bundles and reject cycles. Under `F2_01`,
generate two assigned IDs, one dependency, and one unresolved question with the
exact completion claim; regeneration is byte-identical. Under `F2_03`, missing
bounded-input review or a dependency cycle fails with `E_BATCH_REVIEW` or
`E_DEPENDENCY_CYCLE` and awards no approved handoff.

Derive every checked item by revalidating current evidence. Preserve historical
attempts, decisions, reviews, reconciliations, and artifact links; missing and
stale evidence leave unchecked items. Hand-edited boxes or arbitrary ledger
completion fields cannot award completion; under `F2_02`, a hand-edited checked
box makes `render --check` fail. Independently delete one linked artifact and
change one assigned input hash; each regenerated box becomes unchecked and
names missing or stale evidence. Restore the intact baseline between controls.
Render generation IDs, exact removed/added
block IDs, stale review IDs and pending reference occurrences from the retained
chain. Recompute historical reconciliation from each retained input catalog and
label current review eligibility separately. Under `F2_02`, retain two changes
including semantic-only edits; a later live obligation replacement cannot alter
either reconciliation. Missing predecessors or payloads keep items unchecked.
Regenerate every view from records; Item 6.12 owns the complete final foundation
gate.

Generate the complete 69-UAT ownership/dependency/test-file map and separate
source/upstream/facet/native/neutral/strengthening/gap/replay denominators.
Ready runtime assignments require named input, source, closure, observer and
actual-result review owners. Measure retained source/skills/fixtures/changed
code and reasoning/tool/growth allowances under the roughly 100k ceiling;
missing independent input approval cannot generate an approved handoff.

- [ ] implemented
- [ ] reviewed

### Item 6.3: F3 - Publish all sources, resources and fixed recipes

spec.md section: F3; Architecture, dependency extension and bundle contract

After Item 6.2 review, own the final published source/closure readiness under
`nextflowconformance/data/tools/`, `data/`, `package.go` and reviewed dependency
extension. Include all Go developer tools, JVM scalar observers, selected
unchanged native specs/helpers, original CLI runners, wrappers, supervision,
fixture go-test-exec launcher, fixed Linux syscall supervisor and seccomp
profile. No ignored pilot script or local absolute executable supplies a
foundation dependency. Publish exact reviewed fixture layouts and fixed literal
feature/CLI selection, sources and resource-ID build/cache recipes.

Retain actual immutable origins, requested/resolved identities and full Gradle
URL/cache metadata, Java symlink/file tree, Go module/build prerequisites,
Bash/tools and their loaders/libraries. Include every actual transitive build/
runtime input rather than only the historical 578 copied blobs. Separate
source/generated/acquired identities; generated classes must be rebuilt in a new
workspace. Recipe tool/argv templates are fixed in package code and use only
reviewed seven-root tokens expanded before launch; record logical and effective
arguments/environment, source/tool/classpath/output hashes and command receipts.
No recipe evaluates record text as shell or resolves missing downloads. Missing
resource fails before its affected execution.

Build the content-addressed OCI root filesystem offline from those locked
tools/loaders/libraries, and publish its reviewed recipe, actual build receipt
and digest as dependency extension inputs. Ambient image tags/pulls are not
resources. Record supported finite host kernel/OS/architecture plus existing
Docker service/kernel/OCI runtime/seccomp control prerequisites separately.
Resource/recipe additions revalidate/review the extension without altering base
source lock, selections, bootstrap batches or frozen pilot records.

The Item 6.3 implementor owns source/resource/image preparation; a separate
closure reviewer owns resource/recipe/image input acceptance. The queue owner
assigns both identities before reconstruction. Selection/observer/expectation
source reviewers reaccept every affected input before execution, not after
observing outputs. Current actual build results and portability remain with
later items; this handoff cannot import successful historical status.

- [ ] implemented
- [ ] reviewed

### Item 6.4: F3 - Export and restore the complete portable bundle atomically

spec.md section: F3_01

After Item 6.3 review, implement `nextflowconformance/package.go` and the
private package/restore/replay commands through the unchanged public `Run` API.
Own `TestUAT_F3_01` in `package_test.go`. `bundle.json` is the sole atomic
publication point with exactly schema, id, revision, suite, dependency_lock,
roots, entries, attempts and review_id. Hash canonical manifest bytes with id
omitted for its ID. Revision binds D2 commit/dirty digest. Require exactly
corpus/source/gradle/go/java/tools/evidence roots and safe relative paths.
Entries retain regular-file/symlink kind, content-addressed payload bytes, Git
executable identity, historical modes and approved link targets. Attempts bind
retained manifests/raw receipts. Historical absolute cwd/argv remain unchanged
raw bytes with explicit old-root historical prefixes; current file references
never resolve through those prefixes.

Verify the entire manifest, destination uniqueness, payload hashes and approved
paths/links before publication. New output/restore roots must be empty. Restore
checks checked-out corpus against the bundle and stages only cache/ evidence;
corpus authority is never replaced by restore. Reconstitute all reviewed Gradle
cache metadata/resources, source/observer/tool executable bits, Java/Go/Bash
trees and inputs into new named roots without global cache fallback or network
fetch. Verify restored identities before any recipe execution.

Export a reviewed intact fixture bundle, relocate it, deny old-root access and
restore into an empty cache. Replay with published decoder reports
`artifacts-replayed`/`artifact-replay`, zero executions and zero new engine
passes. Missing acquired blob/cache metadata returns 2 with
`E_DEPENDENCY_MISSING`, changed bytes `E_ARTIFACT_HASH`, absolute/current
escaping path `E_SOURCE_PATH`; failure publishes no restore. Retain red/green
commands and independent bundle/source/receipt review. These small fixture
controls establish bundle mechanics; genuine fresh builds belong to 6.11.

- [ ] implemented
- [ ] reviewed

### Item 6.5: F3 - Preserve portable executable identity and historical modes

spec.md section: F3_02

After Item 6.4 review, own `TestUAT_F3_02` in `package_test.go`. In a disposable
fresh Git checkout, verify all 79 historical nonexecutables with actual 0o644
checkout mode retain content, regular-file/symlink kind, Git executable bit and
link target, while every historical 0o664 value remains immutable metadata.
Report portable mode verified and historical full-mode replay not performed.
Historical P01 full-mode findings/receipts and sealed manifests stay unchanged.

Independently remove a required 100755 executable bit or change a symlink
target; fail `E_EXECUTABLE_IDENTITY`. Link execution must stay within its
reviewed restored tool tree. Never revise old mode/hash records to fit a
portable checkout. A comparison of executable bit alone cannot claim full-mode
reproduction. Independently accept actual checkout identity/mutation receipts
before sandbox/reconstruction handoffs.

- [ ] implemented
- [ ] reviewed

### Item 6.6: F3 - Publish fixed Linux syscall and descendant enforcement

spec.md section: F3 supervision; D3 cleanup; F3_03 and F3_04 prerequisites

After Item 6.5 review, extend the published fixed Linux supervisor to trace its
descendants from launch, including every thread, fork/clone and exec, using
ptrace exit-kill and fork/clone/exec events. Parent-child tracing adds no
container capability. Publish/hash the fixed seccomp profile permitting this
tracing and denying namespace escape, raw/packet sockets and untraced
asynchronous socket I/O. Reject tracee detach and socket transfers to unowned
processes. Lost descendant, unsupported profile/kernel or unknown cleanup
returns `E_OFFLINE_UNAVAILABLE`, with incomplete proof.

Enforce every connection/data decision before delivery: socket creation,
bind/listen/accept/connect, datagram sends, socket write/vector-write and all
send paths. Track descriptor duplication/inheritance/close, socket inode/
generation and ownership; reject unhandled socket I/O. Denials return EACCES and
supervisor-owned decision receipts. Ordinary connect failure is not denial
proof. Start commands deny-all; only reviewed genuine Gradle recipes and fixed
fixture policies may have the exact allowances in Items 6.7/6.8. Inherited
anonymous pipes/socketpairs carry owned control/tool IPC only; named Unix
endpoints cannot supply host/dependency data.

The Item 6.6 implementor owns supervisor/profile readiness; an independent Linux
supervision/security reviewer owns source and enforcement/control acceptance.
Assign identities before lane launches. Exercise the named wrong namespace-only,
executable-only, unchecked write and lost-descendant behaviors in disposable
controls with meaningful red/green proof. Keep unrelated process owners
untouched. Every success/failure/timeout must retain all descendant exit/reap
receipts and a closed socket inventory. This fixed policy is bounded to
Gradle/fixtures, not an arbitrary protocol or plugin framework.

- [ ] implemented
- [ ] reviewed

### Item 6.7: F3 - Enforce isolated offline and HTTPS fixture lanes

spec.md section: F3 lane contract; C2 actual discovery; F3_03 prerequisites

After Item 6.6 review, implement lane launch/access in the fixed published
wrapper/supervisor. Use a disposable fresh Git worktree of the reviewed
implementation at a different absolute path; read-only shared Git metadata is a
separately recorded control input. The Item 6.7 reconstruction owner records
the current D2 commit and dirty digest and prepares a reviewed input inventory
of tracked changes, deletions, untracked bound files and executable/link
identities. Create the checkout at that commit, then materialize those exact
reviewed changes from the hashed implementation handoff before bundle restore
or lane launch. An independent source reviewer verifies its commit, dirty
digest and every bound input against the reviewed current implementation.
A checkout of HEAD alone cannot represent uncommitted reviewed code. Restore
then checks this corpus authority without replacing it. Item 6.11 repeats this
procedure; Item 6.12 repeats it for the final implementation. Start empty cache,
HOME/XDG,
GRADLE_USER_HOME/GOPATH/GOMODCACHE roots. Deny original workspace source/
evidence, ignored research scratch and ambient caches. All toolchain paths
resolve to verified bundle resources.

Use existing Linux Docker service with --network none, separate PID/mount/
network namespaces, read-only root filesystem, all capabilities dropped and
no-new-privileges. Mount the verified offline-built OCI rootfs and reviewed
inputs read-only, each lane's declared writable roots and recorded kernel /proc,
/dev and tmpfs mounts. Mount no daemon socket/old root/host service/ device or
host network/PID namespace. Record Docker/kernel/OCI/seccomp identities,
complete container config, namespace IDs, interfaces/routes and mount/access
inventories. Only loopback exists; external routes and DNS are absent. Literal
loopback addresses and empty resolver configuration are fixed.

The offline lane denies acquisition/application/fixture/external traffic and
DNS; only Item 6.8's actual admitted owned Gradle IPC can pass. Build Go tools,
test binaries and JVM specs/observers there from published verified sources. The
fixture lane has loopback-only private networking, test-owned HTTPS listeners,
separate empty writable corpus/cache/home/temp roots and hashed offline-built
test binaries/reviewed fixtures/tools read-only. No genuine build/engine
executes there. Its writable inputs never mount/read into offline build/engine
roots. Deliver only declared fixture requests with exact listener
address/port/TLS/server identity, method/path, response status/body hash and
UAT/subcase counts; proxies/host services/dependency repositories remain denied.

Publish fixed `go test -exec` supervision so actual Go driver/compiler and C2
discovery remain offline; only the compiled acquisition test binary launches in
the fixture lane. Record driver/compiler/test identities, fixed argv and binary
hash. Completion receipts return only to the outer evidence supervisor, with no
fixture-written build input. Offline lane runs exactly the 61 bindings other
than A1_01-A1_07/F3_03; fixture lane runs the seven A1 bindings. Children never
invoke F3_03 or the outer suite runner.

The Item 6.7 implementor owns lane/access/fixture readiness; an independent
isolation reviewer owns actual network/mount/syscall boundary controls, and
source/closure reviewers own mounted bytes. Assign those identities before
execution. Deny old-source/cache reads, cross-lane input transfer, undeclared
requests and DNS with actual decision receipts. Enforce/verify stopped/removed
containers/namespaces on success/failure/timeout. Missing enforcement is
`E_OFFLINE_UNAVAILABLE`, never an offline-flag pass.

- [ ] implemented
- [ ] reviewed

### Item 6.8: F3 - Admit only genuine current Gradle TCP and lock UDP IPC

spec.md section: F3 fixed IPC policy; F3_03 genuine build proof

After Item 6.7 review, own exact IPC-policy preparation and real compiled
attempt proof. Bind Gradle 9.3.1/JDK/resources and the measured unchanged
compile/native recipes, retaining --offline, --no-daemon, --max-workers=1 and
-Dorg.gradle.jvmargs=-Xmx2g. That last flag forks a single-use daemon; flags and
an offline namespace cannot prove zero sockets. Other commands retain deny-all.
Assign the Item 6.8 implementor as IPC/readiness owner and a distinct
pinned-Gradle source reviewer as admission/decoder/control acceptance owner
before genuine compilation.

Identify client, single-use daemon and worker by current owned PID/start-time,
parent, executable hash, actual main class/argv, classpath and recipe ID. Java
executable identity alone grants nothing. Record socket inode/descriptor
generation, transport/address/port, actual owner/peer and admission receipt.
Closing the socket or owner exit revokes permission, including PID/port reuse.
Allow only actual client/daemon and daemon/worker TCP pairs from the pinned
connector. Decode dynamic addresses from the current daemon registry or
serialized worker launch bytes with the reviewed pinned decoder, then match the
actual supervised tool bind. Original address bytes stay artifacts. Provisional
tool bind/listen may await publication; hold accept at syscall entry while
traced threads publish. No connect/accept/data precedes the match; release after
admission or fail at recipe deadline. Require split-write-aware literal Gradle
Magic preamble before subsequent bytes, as an extra discriminator alongside
role/endpoint/ownership, never sole admission.

Cache-lock UDP binds, including wildcard, are allowed only for those current
tool roles. Delivery requires loopback destination, an owned current listener
and matching restored-cache lock-owner record. Validate version 1 ten-byte
payload with big-endian lock ID and type 1,2 or 3. Record every bind/datagram,
including zero sends without contention. Deny DNS/arbitrary UDP. Deny other
listeners/connections, applications/HTTP repositories even inside a Gradle JVM,
other descendants, fixture endpoints and host services. Record every denied
probe separately from admitted TCP/UDP and dependency/engine requests.

Run genuine fresh compile and native attempts under this exact policy and retain
allowed flows/datagrams, original endpoint bytes, decoder/main/recipe identities
and complete cleanup. Require zero delivered offline acquisition/
dependency/application requests, zero external/DNS and old-source/cache reads. A
tooling need for another channel leaves the gate incomplete until pinned
independent policy review and new actual attempts; no fake/socket-free tool
replacement is accepted. This handoff supports F3_03; Item 6.9 tests denied
undeclared HTTP admission using the actual Gradle daemon.

- [ ] implemented
- [ ] reviewed

### Item 6.9: F3 - Prove live fixture and undeclared Gradle HTTP denial

spec.md section: F3_03 boundary subcases

After Item 6.8 review, implement two separately restored boundary subcases.
Assign the Item 6.9 implementor as boundary-control readiness owner; separate
isolation/Gradle source reviewers accept listener/probe/init-script declarations
and byte-flow controls before execution. Remove resolved Spock from the separate
offline cache. A declared fixture serves its exact reviewed bytes via HTTPS at
/F3_UNDECLARED_SPOCK. One fixture-lane GET returns 200 with matching resource
hash confined to its own root. Offline probes to that live listener and an
external address, plus reads of fixture output and old cache, return supervisor
denials with zero delivered requests/bytes. Genuine affected build preflight
returns 2 `E_DEPENDENCY_MISSING` naming Spock, starts no build/ engine and
awards zero passes. Restoring solely from the bundle makes a new genuine offline
build/engine pass; fixture bytes never supply the resource.

The separate IPC control starts an undeclared HTTP repository in a boundary
container with the same offline image, namespaces, mounts and exact IPC policy.
It serves missing Spock at /F3_UNDECLARED_SPOCK. One explicit supervisor-only
readiness GET returns 200/hash, with diagnostic bytes barred from build roots.
Mark this diagnostic listener/GET as control traffic outside tool admission;
revoke that permission before launching the genuine configured Gradle recipe.
Preserve the same live usable address/owner so readiness rules out a dead
server. Genuine daemon/worker roles still cannot connect/deliver HTTP there.

With restored closure, a separately reviewed control init script makes one
literal URL connection probe from the actual Gradle daemon, asserts the
supervisor denial and continues the unchanged native selection. Retain denied
connect receipt, zero repository build/dependency requests and no acquired
resource. With Spock missing, preflight fails before affected build; with bundle
Spock restored, a new genuine build/native attempt passes with owned IPC and
zero repository requests. The namespace-only and Java-executable-only wrong
allowances must fail this live control. Stop/reap diagnostic server and all
descendants, close sockets and record container removal. An ordinary connection
refusal cannot replace the required denial/readiness evidence.

- [ ] implemented
- [ ] reviewed

### Item 6.10: F3 - Reject every missing published reconstruction prerequisite

spec.md section: F3_04

After Item 6.9 review, own `TestUAT_F3_04` in `package_test.go`. Independently
remove published observer source, native source dependency, resolved Spock,
resolved JUnit, Gradle metadata and Go prerequisite from known-valid separately
restored setups. Every affected replay/build gate fails `E_DEPENDENCY_MISSING`
before affected execution, naming the input and producing no substituted engine
pass. Baselines prove genuine closure/recipe execution, not merely editable
manifest fields. Keep intact identity/hash relationships except the intended
absence so unrelated schema drift cannot satisfy the mutation.

Build/capture timeout stays incomplete with actual descendant/container cleanup.
An edited complete:true field or replayed historical success cannot pass the
fresh-execution gate. Remove sandbox enforcement independently and require
`E_OFFLINE_UNAVAILABLE` with incomplete proof. Run focused temporary subjects
rather than the outer F3_03 or foundation runner. Independent review accepts all
actual controls before Item 6.11's full lane composition.

- [ ] implemented
- [ ] reviewed

### Item 6.11: F3 - Execute fresh builds and compose exactly 68 lane results

spec.md section: F3_03

After Items 6.3-6.10 review, own `TestUAT_F3_03` in `package_test.go` as the
outer reconstruction/evidence supervisor. Restore the genuine reviewed bundle in
a fresh checkout at a different absolute path with empty homes/caches and
old-root access denied. Actual offline Go developer tools/test binaries and
genuine JVM native specs/observers must compile from published sources. Retain
new source/tool/classpath/output/environment/argv/build receipts; old generated
hash agreement is no compile attempt. The named independent build-output
reviewer accepts each actual Go/JVM output before its dependent launch. Run all
required E1/E3 genuine cases, six
native features, four original CLI invocations and all accounting/loss/
semantic/Bash controls with current independent readiness and result reviews.

Use actual C2 go list/go test discovery, active sources/build flags, anchored
literal selectors and raw Go JSON events. Compose exactly seven fixture A1
results plus 61 offline results, each bound to the same current reviewed
implementation/contracts/bundle/dependency lock. The 68 child bindings exclude
F3_03; no child recursively invokes it or its parent suite. Assign the Item 6.11
implementor as reconstruction/attempt owner and a distinct independent reviewer
as composition/raw-result acceptance owner before launch. Record the integration
binding's up-to-3,600-second deadline and one-hour ceiling.

A1_01 makes exactly three actual local HTTPS requests for three blobs, stops its
server, then makes zero validation requests. A1_02 retains real 503 and
retry/transaction behavior: all-failed makes two requests, each single-failed
subcase four, excluding separately logged successful setup acquisition. Other A1
setup/acquisition requests are declared and counted separately; preserve all
existing zero-request validation/preflight subcases. A1_06 acquires the actual
42,355,106-byte opaque distribution with repeated ZIP names and zero member
extraction; A1_07 separately rejects prefix/JAR byte mutations and packaging/
lock-hash target bypass. Serve reviewed local pinned bytes/errors, never fetch
upstream or preload acquisition results or mock transport. Fixture
binaries/inputs are read-only and results remain fixture-owned; no genuine
engine/build runs there or consumes its writable outputs.

Require every delivered fixture request matches its current declaration. Record
zero delivered offline acquisition/dependency/engine requests, zero external
traffic/DNS and zero old-source/cache reads. Owned Gradle TCP flows, UDP
binds/datagrams and denied probes have separate counts/receipts. Include both
live boundary controls from Item 6.9 and all Item 6.10 missing-input controls.
Unexpected fixture requests, any undeclared offline/external bytes, cross-lane
transfer or missing enforcement fails the proof. Every descendant exit/reap,
closed socket and stopped/removed container/namespace receipt is required on
success/failure/timeout; unknown or live daemon/worker leaves incomplete
evidence. No ignored research script/local absolute tool supplies execution.

The outer F3_03 reconstruction/boundary proof plus all 68 current child results
establish the 69-UAT fresh-checkout foundation gate. It still reports exact
pending mapping/internal/document/target dependencies, both product decisions
and zero wr passes. Replay remains historical with zero new executions.
Independent source/closure/isolation/Gradle/control/result reviews accept this
finite proof before final regenerated reports; foundation is not full language
coverage or production runtime completion.

- [ ] implemented
- [ ] reviewed

### Item 6.12: F2/F3 - Regenerate views and verify the final foundation claim

spec.md section: F2 and F3; Implementation Order final gate

After Item 6.11 review, independently compare all 69 real one-to-one GoConvey
bindings against the accepted spec and the ownership tables. Every original 49
obligation and twenty additions, including amended subcases, must have current
ready input/review/expected/binding authority and actual proof. Revalidate
byte-complete extraction, exact reference edges, all retained
catalog/payload/reconciliation generations and current semantic freshness,
including drift hidden behind an unchanged manifest. Retain source lock, 160
selections, eighteen batches and frozen historical records unchanged.

Require seven actual E1 cases; eighteen E2 accounting controls and three E2
semantic subjects; E3's genuine finite family denominators, 155 accepted/
rejected pairs, six Bash/three analytical controls and five semantic kills; F3's
actual fresh compiles, seven fixture/61 offline/outer-F3_03 composition, live
denied boundaries and all cleanup. Require current independent reviews of every
selected accounting/mapping/expectation/observer/dependency/result input,
matching generated views and current artifact/input hashes.

After this item's final code/input changes, the Item 6.12 implementor owns the
final reconstruction and attempt, with independent source/closure/readiness,
build-output and actual-result/composition reviewers assigned before launch. The
queue owner freezes the candidate commit or reviewed dirty revision before these
runs. Freeze and reapprove all current D2 inputs, re-export/review the bundle
bound to that commit/dirty digest, and reconstruct the implementation using Item
6.7's input handoff in another fresh checkout with empty roots. Rebuild affected
Go/JVM sources and accept their outputs before launch. Execute a new outer F3_03
proof and all seven fixture/61 offline children, including complete E1/E3 runs
and E2/E3/F3 controls, under those final input keys. Independently accept all
new actual results; stale Item 6.11 captures receive zero result credit.
Recompute bound inputs after execution and final verification; any further
change requires affected rebuilds/reruns and result reacceptance. If a later
delivery commit changes HEAD or the dirty digest, the queue owner schedules a
newly bound bundle, reconstruction and current attempts before claiming final
completion. Precommit receipts retain their original revision as history;
identical source bytes cannot waive D2's commit binding.

Regenerate before the final attempt and again afterwards so final verification
and view checks use that new evidence. Generated checked marks derive from
reverified current receipts, never this authoring plan or imported historical
status. Record zero missing/skipped/failed/timed-out/stale foundation UATs and
required family executions, with distinct accounting/native/neutral/
strengthened/document-gap/replay/wr counts. Do not merge denominators into a
language-coverage percentage. Both later profiles stay incomplete with exact
pending IDs and expected nonzero diagnostics as listed below.

- [ ] implemented
- [ ] reviewed

## Acceptance ownership and dependencies

Every ID below binds to `TestUAT_<ID>` in `nextflowconformance/` plus the listed
test file. The owner closes the whole acceptance test, including its subcases;
earlier items supply reviewed prerequisites. The dependency column names the
last required item review, in addition to phase entry.

| ID | Owner | Dependency | Test file |
| --- | --- | --- | --- |
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

## Exit conditions

All ten F UATs and all 69 total foundation bindings pass with current
independent evidence. Item 6.11's actual fresh-checkout composition is required;
package copying, recipe text, restored old captures or prior research passes
cannot satisfy it. All implementation and independent review sub-handoffs have
accepted complete-input authority within their measured context bounds. Use the
recorded ordinary and E3/F3 integration deadlines. After offline
acquisition/restoration and source-based readiness review, capture:

```bash
timeout 2m go run ./cmd/wr-nextflow-conformance extract
timeout 2m go run ./cmd/wr-nextflow-conformance validate
timeout 2m go run ./cmd/wr-nextflow-conformance extract --check
timeout 2m go run ./cmd/wr-nextflow-conformance render
timeout 5m go run ./cmd/wr-nextflow-conformance discover --suite foundation-bootstrap
timeout 60m go run ./cmd/wr-nextflow-conformance run --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-nextflow-conformance render
timeout 2m go run ./cmd/wr-nextflow-conformance verify --suite foundation-bootstrap
timeout 2m go run ./cmd/wr-nextflow-conformance render --check
timeout 60m env CGO_ENABLED=1 go test -tags netgo -count=1 -timeout=60m ./nextflowconformance/...
timeout 20m env CGO_ENABLED=1 go test -tags netgo -count=1 -timeout=20m ./cmd/wr-nextflow-conformance/...
timeout 10m golangci-lint run
timeout 2m go run ./cmd/wr-nextflow-conformance verify --suite target-inventory
timeout 2m go run ./cmd/wr-nextflow-conformance verify --suite wr-runtime
```

Execute required repository tests from applicable repository instructions with
recorded deadlines, and report baseline/unrelated failures separately. Those
failures cannot erase a failed foundation gate. Keep the final runner
nonrecursive: runner tests use temporary subjects; F3_03 itself composes 68
children and its own proof rather than invoking the outer suite.

Split focused invocations at reviewed dependency boundaries when needed without
omitting any ID/subcase. Each split sets Go's test timeout to its reviewed bound
within the applicable suite ceiling and retains the integration deadline.
The outer timeout alone leaves Go's ten-minute default active.

Foundation commands/tests return 0 with complete actual evidence and no
missing/skip/fail/timeout/stale required results. Both later-profile verify
commands return 1 with outstanding work, inspected explicitly. Reports retain
D_TYPED_MILESTONE and D_JVM_PLUGIN_POLICY, CLI_P8_MAPPING and its actual failed
CLI attempt, STRING_MIX_EXECUTION's two unexecuted string contracts, all seven
internal helper obligations, five document closure dependencies, nonzero
remaining target inventory/pending references and all nine wr seeds. Preserve
`E_SOURCE_REFERENCE_PENDING`, `E_ADAPTER_UNAVAILABLE` and zero wr passes.
Whole-target inventories, internal equivalents, product policy resolution and
actual durable wr runtime remain later incomplete milestones. Historical Phase 1
at ec487ed2 and frozen pilot results remain their original evidence; this
revision grants no new implementation or review acceptance.
