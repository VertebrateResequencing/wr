# Measured Nextflow research pilot report

The accepted pilot supports expanding a finite independent contract suite for
Nextflow and eventual wr comparison, with separate native-original regression
and internal-observer research. Parser scalar observations, typed Mix values,
selected unchanged workflows and four document-gap cases have measured Nextflow
evidence. Every selected original assertion remains accounted for. Equivalence
of the whole original harness, successful raw Spock oracle comparison, wr
execution and full-language coverage remain unresolved. Independent report
review must accept this recommendation before R-UAT-05 or full pilot completion.

## Authority and research acceptance

This report reconciles [the charter][charter] and [Phase 4 Item 4.1][phase4]
against the accepted source-derived handoff and measured Phase 2/3 records. The
target is Nextflow 26.04.6, pinned source commit
`232b60569865e9a4577e48c1955409238359d6ca`, parser v2. Runtime cases use static
typing disabled. The worktree is /home/ubuntu/wr, branch nextflowdsl. Root owns
report review, queue, delivery, status and later assurance decisions.

- R-UAT-01 PASS: [independent handoff review][handoff-review] accepts exact
  source/document expectations, identities, provenance and local preservation.

- R-UAT-02 PASS: [independent original-results review][original-review] accepts
  actual prerequisite closure and every genuine scheduled attempt.

- R-UAT-03 PASS: [joint Phase 3 review][joint-review] accepts bounded mappings,
  gaps and specific unresolved/nonexecuted outcomes with completed captures.

- R-UAT-04 PASS: that joint review accepts all six F1 subjects and every
  applicable paired loss and analytical control.

- R-UAT-05 pending: root must obtain independent reconciliation of this report
  and its recommendation in pilot-report-review.md. This author report awards no
  independent report verdict. The pilot is incomplete until that acceptance.

These research verdicts do not replace execution and preservation verdicts. The
selected genuine originals passed. Finite candidate observations passed where
reviewed; CLI-P8's count projection remains unresolved. Identity, byte and
completion preservation passed locally. The full all-upstream objective has no
denominator here and cannot inherit this finite group's passes.

## Denominators, provenance and retained assertions

[The inventory][inventory], [expectations][expectations] and [reconciliation
index][index] retain every selected identity, source and disposition. The index
is a lossless readable JSON selection of relationships and captures, not a new
production ledger or rewritten expected truth.

The original denominator is six Spock methods and eleven Spock input units:
P1-P8 and M1-M3. P1-P7 each have four predicates; P8 has count zero only. That
gives 29 parser predicates. M1 has six membership predicates and one exclusion;
M2/M3 have one sorted-list equality each. That gives nine Mix predicates and 38
Spock predicates total. Two complete workflow/check pairs have four fresh/resume
invocations and four literal CLI predicates. The combined original predicate
denominator is 42. Tables and generated providers are both zero. Nullable's four
shell expressions are separate control evidence.

P1-P5 run sequentially within the invalid-syntax method. P6 occupies the mixed
declarations method. P7 rejection precedes P8 acceptance in the params method.
One original @Shared parser spans that specification. Each Mix method has
genuine inherited resets, MockSession/helper behavior and @Timeout(5). Passing
unmodified native features prove their sequential assertions reached and
completed. They do not expose every successful diagnostic list, collection,
normalization byte sequence or JVM object identity.

Every original expected comparator and typed value remains unchanged. P2's
message has backslash+n characters, P6 retains substring comparison, and P8
retains count zero. M1 retains integer 1, 2, 3 and strings a, b, z; its original
membership checks permit duplicates and unrelated extras except c. M2/M3's
numeric sorted equality preserves multiplicity. Source literals, raw bodies,
decoded strings and manually authored normalization candidates remain separate.

Ten selected helper records retain seven separate observation obligations.
H-P-SHARED, H-P-CHECK and H-P-TESTUTILS preserve the shared instance, main.nf,
parse/analyze, syntax-cause filtering and line/column sorting. H-M-DSL,
H-M-BASE, H-M-RUN, H-M-NORMALIZE, H-M-MOCK and H-M-LAST retain reset, network
start/await/destroy, error identity, normalization, mock execution and last
result. H-CLI-RUNNER preserves actual shell aggregation. The index carries each
helper's source ID and each result disposition. Successful session and
mainScript assertions are proved in genuine helpers; raw objects remain
unobserved. Ordered lifecycle events and the error-identity branch remain
unproved. Mock scriptlet success earns no real shell-execution claim.

Fifteen original completion IDs bind eleven P/M units and four CLI invocations.
All are completed in accepted original results. Candidate collection completion
and supervisor completion are separate requirements. The original source-derived
inventory's pending fields remain frozen history; completed results overlay that
history without rewriting it.

All 83 fixture IDs and paths remain distinct: 70 original and 13 document
fixtures. Byte-identical original/document topic files keep different origins.
All bytes, sizes and recorded modes pass current preservation. The 37 full
source files and 123 individually identified source records/spans retain
inclusive lines, half-open byte offsets, SHA-256, Git blob and archive/local
mode evidence. Fifteen later mapping-source spans are additional reviewed
observer arguments, not extra original fixtures or original predicates. All 151
initial dependency edges remain source-derived accounting: 142 original and nine
document edges. Actual resolved JVM edges are separate.

The document denominator is 34 facets and 28 document, strengthened, gap and
control expectations. The appendix preserves every accepted predicate-to-facet
subject and detection rationale. Accounting for a facet does not prove it
executed. Nine facets are not-asserted, three unresolved and four gap-only in
that historical original-to-document analysis. Other dispositions retain
partial, example-only and supporting-form boundaries. Phase 3 observations
remain separate additions; they do not retroactively rewrite those categories or
support a coverage percentage. No whole-suite test-absence claim was made.

## Genuine prerequisites and original harness results

The verified distribution initially lacked Spock/JUnit Platform, selected test
classes and ScriptHelper. [Prerequisite evidence][prerequisites] and the
[independent closure review][prereq-review] resolved that concrete resource need
through the unchanged pinned upstream multi-project Gradle build. The copied
2,856 source files matched the pinned tree. Genuine parser, test fixtures, Mock
classes and selected specs compiled together. The full compile command completed
in 93.196 seconds with 21 executed tasks. Resource export and launch resolution
followed; no test task ran during prerequisite work.

There were 578 unique acquired paths and 578 distinct hashes: 575 Gradle
artifacts/metadata, the Gradle 9.3.1 ZIP, its downloaded checksum and the Spock
source JAR. The [resource index][resources] binds every exact URL and file. The
Gradle ZIP/checksum came from services.gradle.org; the Spock source JAR came
from repo.maven.apache.org. Actual Gradle URL-cache evidence binds the 575 paths
rather than inferring origins from coordinates. Eighteen paths have multiple
legitimate cached URLs; each recorded origin is among those entries. Copies,
generated classes, extracted tool contents and capture logs are separate from
acquisitions. Three production resources SOURCE/RUNTIME/LAUNCHER and 454
retained JDK file/link identities were reused unchanged. Production lock and
eighteen bootstrap batches received no research acquisition edits.

Actual tools were Temurin 21.0.12.1+1, Gradle 9.3.1, project/compiler Groovy
4.0.31 and Spock 2.4-groovy-4.0. Gradle's embedded Groovy is 4.0.29. JUnit
Platform launcher/engine/commons resolve to 1.14.1 through Spock's JUnit 5.14.1
BOM, overriding requested launcher 1.10.5. ByteBuddy 1.14.17, Objenesis 3.4,
Jimfs 1.2, JaCoCo 0.8.14 and ASM 9.9 are recorded resolved resources. Java
source/target compatibility stays 17; retained execution uses JDK 21. The
resolution record contains genuine JVM opens and classpath edges.

[Original captures][originals] show these genuine executions:

- Parser native specification: 14:09:34.148339 to 14:09:48.652459 UTC; three
  selected features, exit zero, XML zero failures/errors/skips.

- Mix native specification: 14:10:30.091603 to 14:10:47.395677 UTC; three
  selected features, exit zero, XML zero failures/errors/skips. Actual feature
  times were 2.533, 0.193 and 0.102 seconds.

- Genuine selected CLI runner: 14:20:52.865283 to 14:21:06.791841 UTC; unchanged
  arity/topic workflows and hidden checks, four individual exits zero and outer
  runner exit zero.

Native Test tasks executed; compilation prerequisites were up to date. Three
exact feature-name filters per specification retained source order and shared
setup. No original assertion or helper was replaced. There were no unavailable
scheduled originals, skips, execution failures or timeouts. The 38 native
predicates are assertion-proved. Successful raw Spock values, actual historical
stripIndent bytes and historical JVM identity remain unobserved. No raw
original/candidate Spock oracle comparison follows.

The [independently accepted CLI wrapper][instrumentation] delegated original
arguments and streams in a disposable ten-file copied test layout. Each engine
invocation had 120 seconds; the outer runner had 540 seconds. Topic's two actual
snapshots each retain exactly 22 bytes, `bar: 0.9.0\nfoo: 0.1.0\n`, including
final LF, with SHA-256
`ce5e4c500ca731aa86fa5e5a3856b9bdbe3c64f685d0e51b2ba5d1886b13bceb`. Arity's two
immediate status checks and topic's two literal cmp predicates passed. Pair
success is inferred from the unchanged successful runner and complete traces;
pair subprocess exits were not separately instrumented. Invocation exits,
original aggregates, byte comparisons and completion are separate. These
successful runs do not repair known shell failure propagation.

Actual logs/config/task scripts show local executor, host Bash, standard
profile, copied tests/nextflow.config, offline mode, TEST_JDK 21 and empty
WITH_DOCKER. Eight fresh tasks ran without a container runtime. The declared
container remains configuration evidence. No typing or topic-preview flag was
added. Resume cache messages are observed and earn no cache-correctness or
cache-reuse assurance.

## Manual candidate observations, strengthening and documented gaps

[Mapping preflight][mapping-preflight] independently accepted observer design
before execution. [Joint result review][joint-review] then accepted actual
source mappings and evaluators. [Projection results][projections] contain 15
scheduled candidate units and 16 new mapping launches: one JVM parser observer
shared by eight P units, eight separate CLI parser probes, three Mix launches
and four gap launches. The eight CLI probes are an additional mapping route, not
eight more scheduled candidate units or original inputs. All launches completed
within their 120-second bounds; no new build or acquisition was needed. Four
genuine CLI invocations were reused unchanged.

The JVM candidate used one parser in P1-P8 order and the original compiled
TestUtils.check. It preserved normalization, parse/analyze, syntax-cause
filtering and sorting. All 29 separate scalar predicates agree with frozen
expectations. Eight measured candidate normalization files equal the frozen
source-authored bytes. This measures candidate normalization only. Historical
successful Spock normalization and internal object identities stay unobserved.

CLI-P1 through CLI-P7 have 28 accepted count/location/message projections,
limited to these seven single-source, single-error cases. Reviewed formatter and
loader source explains each raw diagnostic row and the original message. Engine
exit alone supplies none of those four values. CLI-P8 exits one because runtime
parameter greeting is required. Empty formatted syntax output cannot establish
TestUtils count zero. Its CLI count mapping remains unresolved with that
captured runtime cause; genuine P8 and JVM candidate count zero remain separate
successes. No default, source or expectation was changed to hide it.

M1-M3 preserve selected channel expressions inside real CLI entry workflows.
Typed callbacks observe integers versus strings before rendering. All nine
original value predicates pass. Candidate collections complete before the
callback; engine zero exit and completed supervision are additional gates.
S-MIX's exact typed six-item multiset and S-MIX-COMPLETION pass separately.
Entry-workflow value agreement does not discharge original MockSession,
last-result, resets, normalization, lifecycle or exception identity. The
six-string chained document example D-MIX-EXAMPLE and D-MIX-COMPLETION were not
scheduled or executed. Their nonexecution earns no document pass.

The reused genuine workflows also support six separately authored
S-ARITY/S-TOPIC per-invocation exit/byte expectations. All six pass. Valid
counts 1, 2 and 1..* do not prove invalid-count rejection or scalar/list shape.
Topic's unique and sorted collectFile hide raw multiplicity and emission order.
Exact final versions detect missing distinct values while the full
all-sent-values facet remains partial.

Four document-gap cases have specific accepted observations:

- G-IN supplies one existing gap-input.txt to input arity two. Engine failure
  identifies count_input, input_file, declared two and actual one together.

- G-OUT produces one one.txt for output arity two. The task exits zero and
  writes four bytes, `one\n`, before count_output output validation rejects
  declared two versus actual one. Source/task/path evidence associates it with
  the actual generated file.

- G-SHAPE-FILE produces one file with output arity one. Before rendering, the
  emitted item is a Path; the outer collection has one item.

- G-SHAPE-LIST produces one file with output arity 1..*. Before rendering, the
  emitted item is a one-element List<Path>, within a one-item outer collection.
  The two successful engine exits and completions pass separately.

Both shape scripts preserve process and shell bytes, appending only the reviewed
collection observer. Their files have basename one.txt, four bytes and the
frozen produced hash. Exact diagnostics and JVM class names are implementation
evidence; the document contracts require associated counts or file/list type,
not a promised message or JVM identity. Generic parse, tool, fixture and
unrelated-process failures cannot satisfy G-IN/G-OUT. These are additions to the
selected group's coverage, not proof that the entire upstream suite lacks
equivalent tests.

Original-only, partial, unresolved/internal, strengthened, documented-gap,
oracle and pending-wr claims stay separate. No wr runtime, engine-neutral
whole-harness equivalence, raw Spock oracle or universal translation pass is
awarded. The accepted mapping evidence preserves that boundary despite 15
successful candidate measurements.

## Failure propagation, loss controls and adaptation defects

[Control results][controls] and [joint review][joint-review] accept all six F1
shell subjects under byte-identical arity/nullable checks and bash -ex. Their
original aggregate exits in charter order are 0, 1, 0, 1, 0, 0. The stronger
bad-subject gates respectively reject fresh engine exit, resume engine exit,
both nullable engine exits plus fresh bytes, and nullable resume bytes. Both
valid subjects pass original and stronger gates.

Nullable retains set +e, both pipeline-status expressions, both exact byte
comparisons, actual tee without pipefail and final-expression aggregation. Its
expected 13 bytes are `empty input\n\n`, preserving both terminal LFs, SHA-256
`d4aa42007b1cbfce672a372a1a97587ffdd4102f52dc3b200314a8081206a019`. These
controlled numeric 0/1 Bash subjects prove harness propagation only. They award
no nullable, Nextflow or wr DSL execution.

All 155 mandatory loss subjects have distinct intact counterparts: eleven unit,
42 predicate, 83 fixture, two resume, fifteen completion and two newline losses.
All 310 loss-gate commands completed. Each intact layout accepts; each altered
layout rejects its named missing ID or changed hash. Topic loses its final LF;
nullable loses one of two terminal LFs. Actual copied files and semantic reasons
were checked independently. Infrastructure errors cannot count as rejection.
Source accounting of 37 full files/123 identities is separate; these loss
controls earn no runtime mutation coverage.

Three M1 analytical subjects have six paired gate commands. Duplicate integer 1
and extra string d seven-item witnesses satisfy all original predicates; S-MIX
rejects their multiplicity or extra value. A six-value permutation passes both.
These supplied typed witnesses explain assertion strength and do not prove an
engine produced those mutated collections. The 327 retained control capture
records also include failed initial attempts and supervisor checks; they are not
327 mandatory controls. The review's 360 inline command records are copied
command metadata, not 360 unique launches.

Every material defect and failed attempt remains visible:

- R01: the first document contribution had thirteen files with ten fixture IDs.
  The separately preserved correction gives every file a distinct ID, carries
  references through inventories/expectations, and passes independent review
  round two. No fixture bytes or expected values changed.

- PREREQ-F1: the original supervisor could leave a detached TERM-ignoring child
  alive while claiming completion. Independent review declined that timeout
  path. The new pidfd/subreaper version discovers owned descendants, including
  children created during TERM, escalates KILL and verifies stopped states. Red
  history and fifteen old successful captures remain unchanged. Correction
  review accepted the new route before originals. This defect is distinct from
  the charter's F1 shell-aggregation subjects.

- Nullable observer race: the initial engine-side snapshot could see resume tee
  truncate .stdout first. That attempt remains failed and archived with old
  metadata/path resolution. A separately reviewed wrapper snapshots after actual
  tee completion and before its pipeline process ends. Independent preflight
  proved binary passthrough, numeric failure, SIGTERM propagation and distinct
  fresh/resume snapshots before the six final subjects ran.

- CLI-P8 retains its required-greeting runtime cause. The unsupported count
  mapping stopped; the source was not repaired to fabricate equivalence.

- D01: one 102-column author prose line was wrapped in separately named report
  02, preserving report 01 and all tokens. The reviewed 22-to-23-field manifest
  correction binds both versions. Independent correction review resolved D01
  without changing the accepted Phase 3 semantic verdict.

- P01 remains root-owned. Seventy-nine sealed nonexecutable 0o664 records
  preserve observed local permissions; Git does not preserve group-write. Before
  fresh-checkout reuse, root must restore those historical local modes or
  distinguish archive/filesystem evidence from portable Git identity while
  retaining executable-bit checks. No portability pass is awarded here.

[Prerequisite authors][prereq-author], [original author][original-author] and
[control author][control-author] retain other recording/checker failures: JDK
symlink-target handling; absent NO-SOURCE directories; an open writer's stdout
self-binding; the accepted finite environment subset; and using a
repository-relative binder for a system binary. The documentary reviewer also
corrected an optional-mode assumption. These were evidence-tool defects, not
engine failures or reasons to loosen contracts. This report's inspection and
checker-development failures are retained in [author evidence][evidence]. No
acquisition or build failed. No measured expected-value disagreement required an
observation-derived repair.

## Time, effort and deadlines

[The root clock][clock] supplies actual sequential stage accounting:

- Stage 1: 11:34:06.104418 to 12:58:40.671383 UTC, 5,074.566965 seconds or
  84.576 minutes, through independently accepted handoff delivery.

- Stage 2: 12:58:40.671383 to 14:46:14.013942 UTC, 6,453.342559 seconds or
  107.556 minutes, through accepted original-result delivery.

- Stage 3: 14:46:14.013942 to 15:43:58.834315 UTC, 3,464.820373 seconds or
  57.747 minutes, including D01 review and delivery.

- Stage 4 starts at 15:43:58.834315 UTC, with hard deadline 17:43:58.834315 UTC.
  It remains in progress through independent report review; this author does not
  invent its final root completion clock.

Completed stages total 14,992.729897 seconds, or 249.879 minutes. Each stage
finished within two hours. Stage 4 includes independent review within its own
two-hour limit and the eight-hour active cap. The author's observed Stage 4
interval and final completion clock are in [completion evidence][end]; that
interval is separate from the later root-owned final stage duration. Its first
clock sample is 15:45:59 UTC. Initial instruction/evidence reading before that
sample is unmeasured and already lies inside the root stage.

[Effort reconciliation][effort] retains exact boundaries, source records, rounds
and measurement limitations. Grouped wall minutes are as follows:

- Original contract author upper bound 12.805; document author upper bound
  12.279. Worker starts/active allocations were not measured; these bounds
  overlap and cannot be summed.

- Initial P/M/CLI/document/strengthening/gap/control contract review round one
  observed window 10.542. Exact active per-group effort was unmeasured;
  conservative bounds overlap and automated static-check seconds are separate.

- R01 correction 7.878; document contract review round two 7.268.

- Combined handoff author 12.529; combined independent review round one 18.882
  through completion. Its decision-only interval was 15.818.

- Prerequisite author 24.246; closure/supervisor review round one 8.358, ending
  in PREREQ-F1. Supervisor correction 14.349; correction review round two 9.681
  through completion, versus 7.600 to substantive decision.

- Genuine original author 24.132, including CLI instrumentation review wait.
  Instrumentation review round one 4.728 through completion, versus 3.127 to
  decision. Original-results review round one 6.398 to decision.

- Mapping author grouped P/M/CLI/gaps 17.065. Its successive groups are
  preparation/preflight wait 4.049, launch setup 0.002, candidate execution
  0.690 and record/source/quality authoring 12.324 minutes.

- Mapping observer preflight round one authority-to-decision window 4.325,
  including reading. Controls tee preflight round one has actual functional
  decision 15:02:46.294557 and completion 15:03:29.557367 UTC; no separate start
  was sampled, so its individual review minutes remain unmeasured.

- Controls author 16.077 through completion, versus 12.391 to decision. It
  overlaps mapping authorship. Phase 3 integration author 3.909.

- Joint P/M/CLI/gap/control/integration result review round one observed
  semantic interval 9.184. Earlier authority/read/preflight windows and
  integration wait remain distinct; no per-contract minute allocation exists.

- D01 correction transition-to-completion observed interval 0.683; earlier
  preparation unmeasured. Independent correction review round one 3.847 to
  decision, with prior instruction reading unsampled.

These are grouped author/reviewer wall intervals, not measured staff-hours or a
scalable per-contract price. Overlapping authors, waiting reviewers, upper
bounds and automated command durations are never added to root active time or to
each other. The data supports feasibility for this finite group and identifies
costly prerequisite/review routes; it does not estimate the cost of all upstream
tests or a universal translator.

All acquisitions were bounded at five minutes, full compile at thirty minutes,
native selected launches at five minutes with Mix's original five-second feature
timeout, CLI/mapping launches at two minutes, and F1 subjects at ten seconds.
Recorded command deadlines, start/end, cwd, environment, exit/signal and
completed cleanup are hash-linked. The passive index checks 378 deduplicated
metadata records across prerequisite/original/ projection/control records. That
number includes enclosing supervisors and controlled subcommands and is not a
new execution denominator. Required subject counts above remain authoritative.
All scheduled subjects finished; no timeout or unavailable original is converted
into a pass. The historical failed timeout-supervisor subjects retain their
actual failure/cleanup evidence.

## Comparison and recommended next research route

The genuine original harness preserves all 42 selected source assertions,
original shared state, helpers and method order. Its actual closure required 578
acquisitions, an unchanged full source build and a supervisor correction. It
proved original regression execution while leaving successful raw internal
values unobserved. It remains necessary as a separate original route when an
internal assertion has no reviewed neutral equivalent.

Unchanged workflow adaptation preserves the exact two workflow/check pairs,
hidden fixtures, runner and config. Four genuine invocations passed under
observed local host execution. Its weak arity aggregate can hide fresh failure;
nullable controls show analogous pipeline/byte propagation losses. Separate
per-invocation exit/byte gates detect these defects. This route is suitable for
source workflow families with externally observable status/files after reviewed
completion and configuration accounting. Raw topic emissions and actual cache
correctness need additional tests.

Manual neutral cases supply reviewed typed and scalar boundaries and explicit
document additions. All 29 JVM parser scalars, 28 bounded CLI diagnostic
scalars, nine Mix values, four gap cases and separately authored strengthening
have Nextflow evidence. Their 16 new launches reused compiled resources and
completed in a 41.407-second candidate-execution interval. Observer design,
source arguments and independent result review cost more than that launch
interval. The CLI-P8 and internal-helper limits show why this route needs
family-specific decisions rather than an automatic universal translation.

The user's proposed independent suite for both Nextflow and future wr is
supported as the next bounded research route. Retain independent source and
document expectations, exact original-to-neutral relationships, complete
values/types/completion and original/stronger results as separate claims. A
later root-authorized harness correction can make each invocation's failure
visible while preserving original source snapshots and their original verdicts.
Gap tests must expand documented requirements, including families absent from
this pilot, without silently reducing the all-upstream/internal objective. This
is a research recommendation, not an authorized production architecture.

Root should route these owned work items after accepted independent review:

- Proven-family expansion owner: next research implementor appointed by root.
  Add one explicitly bounded family at a time using the accepted typed Mix,
  parser-scalar or status/file boundaries. Gate each family on complete original
  assertion accounting, independent expectations, genuine original attempt,
  reviewed observer, exact completion and paired loss controls.

- Focused internal-observer owner: root-appointed prerequisite/observer
  researcher. Capture successful raw native parser/Mix values and normalization
  without changing native outcomes; independently review instrumentation before
  execution. Compare genuine captures with candidates. Preserve all seven helper
  obligations, MockSession/lifecycle/error identities and unresolved JVM/AST
  assertions until reviewed equivalence or an explicit retained original-only
  route exists.

- Unsupported-mapping owner: root-appointed parser researcher. Stop awarding
  CLI-P8 count equivalence from empty diagnostics. Research a parser boundary
  that observes its zero count before required runtime parameters; preserve
  original P8 input and runtime cause. No arbitrary exit-to-count mapping.

- Document-gap owner: root-appointed documentation/contract researcher. Execute
  the separate chained string D-MIX example/completion; extend the four gap
  cases to reviewed ranges, absent-arity behavior, type/default/name
  requirements and topic raw-value/isolation/feedback contracts. Independently
  freeze expectations before observing Nextflow. Whole-suite absence remains
  unestablished and cannot replace document-derived gap justification.

- Document-closure owner: root-appointed source/document reviewer. Review
  process-multiple-input-files, syntax-workflow-typed, migrating-static-types,
  process-typed-topics and the unreviewed strict-syntax remainder against the
  pinned source. Typed-workflow/topic policy remains a later root decision.

- Portability owner: root. Resolve P01 before a fresh-checkout validator is
  reused; retain executable-mode and historical archive/filesystem evidence.
  Also require portable evidence paths, capture packaging and an independently
  verified cache/resource reconstruction route before calling this a reusable
  suite in a fresh checkout.

- wr comparison and final assurance owner: root. Establish an actually supported
  wr execution/observation boundary and independently review it before claiming
  any oracle or translation result. Preserve the eventual all-upstream/internal
  assertions plus document-gap objective and define its complete denominator
  later. Exact architecture and JVM/typed-language policy remain open until this
  evidence is assessed.

The core six-phase assurance specification currently has 49 scripted UATs; that
is not the full Nextflow-language denominator. Core Item 2.1 F11 remains
deferred and Item 2.2 held until root routes the evidence and later spec-writer
revision. This report changes neither. Production acceptance cannot be inferred
from the five research UATs or from successful selected Nextflow executions.

## Preservation, reproducibility and author completion

The current working research manifest has 23 fields and SHA-256
`724e4a56c4a3143f14e886de16acbce9a12d7ada1a3e3416340ea3717bf8bb2f`. All
seventeen frozen fields, 249 other sealed artifacts and the archived 22-field
contribution history pass composed comparisons. The unchanged historical
whole-manifest gates remain historical; no current PASS is claimed from a gate
whose fixed hash predates an authorized extension.

A new current baseline checks 31,868 paths. Of the earlier D01 map's 30,962
bindings, 30,960 retain exact historical bytes/modes. Two current differences
are root-owned Phase 3 markers and execution-status transitions. [Phase 3 root
delivery][root3] and [Phase 4 start][root4] bind those transitions and current
stage clocks. Their historical bytes and snapshots remain unchanged. The report
checker composes these explicit transitions instead of loosening old
preservation gates. The current manifest itself is unchanged by this author.

Run this passive report reconciliation from /home/ubuntu/wr:

```bash
timeout 60s python3 .tmp/agent/nextflow-conformance/research-pilot/report04/reconcile.py
```

It reads current bytes, typed expected values, every selected identity/facet
relationship, acquired hashes, completion/deadline records and author report
mechanics. It executes no original, observer, control, build, acquisition or wr
subject. [Author evidence][evidence] retains exact input/output hashes,
reconciliation data, effort records, command streams and actual completion.
Python syntax and ASCII/80-column/heading/whitespace/link mechanics are the
applicable prose/data gates. Ruff, Pyright, nf-test and nf-core were unavailable
in accepted evidence and no such pass is claimed. No supported behavior changed,
so a new behavioral test is unnecessary.

Most launch/checker scripts and raw captures remain in ignored .tmp research
scratch, including local absolute execution paths. The published reports bind
verified local evidence; they do not package every raw capture for a new
checkout. Resource archives/binaries remain cache inputs rather than normal Git
files. Evidence packaging, portable paths and cache reconstruction need
independent verification before reproducible suite delivery. This limit is
additional to P01's filesystem-mode issue.

Writes are confined to pilot-report.md, item4.1-author-01.md and report04
scratch. No source, fixture, result, prior report, frozen manifest, working
manifest, root status, marker, core, lock or bootstrap artifact was edited. All
owned commands complete before actual FINAL handback; no child agent, background
job, live process, outstanding wait or retained running workload belongs to this
author. Independent report review remains root's next gate.

## Appendix: frozen original-to-document facet decisions

These are the accepted historical detection relationships, retained with exact
subject IDs and reviewer rationales from inventory.json. Phase 3 gap and
strengthening measurements above are separate results. Each facet's complete
typed requirement and source byte/line identity is available in the index.

D-STRICT-DECLARATIONS (partial; DOC-STRICT lines 80-89). Subjects: P8-COUNT. P8
accepts params plus an empty entry workflow; it does not exercise every listed
declaration kind.

D-STRICT-SNIPPET (supporting-form-only; DOC-STRICT lines 91-103). Subjects: M1,
M2, M3. The selected Mix snippets contain statements only and use the
last-result helper. Values do not independently assert implicit-workflow
identity.

D-STRICT-MIX-REJECT (concrete-rejection; DOC-STRICT lines 105-119). Subjects:
P6-COUNT, P6-MESSAGE. Count one plus the original contains predicate rejects
this concrete top-level println/workflow mixture. P6 locations are
source-specific internal checks.

D-STRICT-REASON (not-asserted; DOC-STRICT lines 121-123). Subjects: none. No
selected test includes a module and observes whether top-level statements run.

D-TYPED-PARSER (not-asserted; DOC-TYPED lines 5-13). Subjects: none. Typed
workflow execution is outside this group; v2 is a selected environment
obligation.

D-TYPED-FLAG (not-asserted; DOC-TYPED lines 15-15). Subjects: none. No selected
runtime fixture enables typed workflows; no typing flag is added.

D-PARAMS-OUTPUT-NO-TYPING (partial; DOC-TYPED lines 15-15). Subjects: P8-COUNT.
P8 can support params acceptance with typing disabled. No output-block case is
selected. P7 entry-workflow rejection is source-only, not specified by this
span.

D-TYPED-SYNTAX-CLOSURE (unresolved; DOC-TYPED lines 17-17). Subjects: none. The
outgoing syntax and migration links remain unreviewed.

D-MIX-RETURN (indirect-only; DOC-MIX lines 830-832). Subjects: M1, M2, M3.
Collection values after helper normalization do not independently preserve the
returned channel type; helper/observer equivalence remains pending.

D-MIX-STRING-INPUTS (document-case; DOC-MIX-NF lines 1-5). Subjects:
D-MIX-EXAMPLE. Document inputs are strings. The first three M1 inputs are
integers; these are distinct cases.

D-MIX-ITEMS (partial-original-and-stronger; DOC-MIX lines 832-850). Subjects:
M1-MEMBER-1, M1-MEMBER-2, M1-MEMBER-3, M1-MEMBER-4, M1-MEMBER-5, M1-MEMBER-6,
M1-NOT-C, S-MIX, D-MIX-EXAMPLE. Original M1 rejects missing required values and
c but permits duplicates and other extras. Exact complete multiset claims detect
those losses under separate document/strengthened origins.

D-MIX-ORDER (order-insensitive-comparators; DOC-MIX lines 842-850). Subjects:
M1, M2-SORTED, M3-SORTED, S-MIX, D-MIX-EXAMPLE. Membership, sorted equality and
multiset equality accept permutations. This is comparator compatibility, not
runtime proof of every schedule.

D-MIX-OUTPUT-BYTES (static-example-only; DOC-MIX-OUT lines 1-6). Subjects: none.
The complete mix.out has no final LF. Its illustrative order and terminal bytes
are not an engine-output oracle.

D-PATH-IN-ARGUMENT (supporting-form-only; DOC-PATH-IN lines 159-159). Subjects:
ARITY-FRESH-CHECK, ARITY-RESUME-CHECK. The arity source exercises string
aliases. Its status predicates do not assert identifier binding or staged
filename contents.

D-PATH-IN-ARITY-FORMS (valid-example-only; DOC-PATH-IN lines 163-166). Subjects:
ARITY-FRESH-CHECK, ARITY-RESUME-CHECK, S-ARITY-FRESH-EXIT, S-ARITY-RESUME-EXIT.
Source counts 1, 2 and 1..* exercise valid examples. No arbitrary range domain
is proved.

D-PATH-IN-ARITY-FAIL (gap-only; DOC-PATH-IN lines 166-166). Subjects: G-IN.
Valid-count zero exits cannot detect missing invalid-count enforcement. G-IN
requires one existing file against declared count two and associated failure
evidence.

D-PATH-IN-NAME (not-asserted; DOC-PATH-IN lines 168-172). Subjects: none. No
selected predicate compares name or stageAs behavior.

D-ENV-INPUT (not-asserted; DOC-PATH-IN lines 174-176). Subjects: none. No
selected env-input string/export observation exists.

D-PATH-IN-COLLECTION-CLOSURE (unresolved; DOC-PATH-IN lines 155-157). Subjects:
none. The multiple-input-files link is not separately reviewed; G-IN supplies a
single file.

D-PATH-OUT-PATTERN (indirect-valid-example; DOC-PATH-OUT lines 208-208).
Subjects: ARITY-FRESH-CHECK, ARITY-RESUME-CHECK. Exact and wildcard output
patterns feed matching path inputs. Exit status cannot establish their contents
or all pattern rules.

D-PATH-OUT-ARITY-FORMS (valid-example-only; DOC-PATH-OUT lines 212-215).
Subjects: ARITY-FRESH-CHECK, ARITY-RESUME-CHECK, S-ARITY-FRESH-EXIT,
S-ARITY-RESUME-EXIT. Valid counts 1, 2 and 1..* are source examples; the
predicates do not quantify every number/range.

D-PATH-OUT-ARITY-FAIL (gap-only; DOC-PATH-OUT lines 215-215). Subjects: G-OUT.
G-OUT produces exactly one one.txt with declared count two. The observer must
associate an output-count violation, not any nonzero exit.

D-PATH-OUT-ONE-SHAPE (gap-only; DOC-PATH-OUT lines 217-217). Subjects:
G-SHAPE-FILE, G-SHAPE-FILE-EXIT. Arity one must emit a file value before
display. Original arity status checks cannot detect scalar/list confusion.

D-PATH-OUT-OTHER-SHAPE (gap-only; DOC-PATH-OUT lines 217-217). Subjects:
G-SHAPE-LIST, G-SHAPE-LIST-EXIT. Arity 1..* with one produced file must emit a
one-element file list. An identical printed basename is insufficient.

D-PATH-OUT-UNSET (not-asserted; DOC-PATH-OUT lines 219-221). Subjects: none. All
selected shape contracts specify arity; no absent-arity mixed-shape behavior is
observed.

D-PATH-UNSPECIFIED-DEFAULTS (bounded-absence-only; DOC-PATH-IN lines 159-177).
Subjects: none. No runtime default value is inferred. Input arity defaults and
output followLinks defaults fall outside the frozen requirements.

D-TOPIC-SIGNATURE (valid-input-only; DOC-TOPIC lines 350-352). Subjects:
TOPIC-FRESH-CHECK, TOPIC-RESUME-CHECK. The source supplies a valid String topic
name. File comparisons do not prove Channel class identity or rejection of other
name types.

D-TOPIC-MATCHING (partial; DOC-TOPIC lines 352-352). Subjects:
TOPIC-FRESH-CHECK, TOPIC-RESUME-CHECK. Missing foo or bar version values changes
final bytes. The case has one matching topic and does not test isolation from
unmatched names.

D-TOPIC-TYPED-FORM (not-asserted; DOC-TOPIC lines 354-372). Subjects: none.
Typed topic section and >> examples remain outside selected runtime work.

D-TOPIC-LEGACY-FORM (supporting-form-only; DOC-TOPIC lines 374-383). Subjects:
TOPIC-FRESH-CHECK, TOPIC-RESUME-CHECK. Original stdout topic outputs exercise
the legacy topic option. The document path-file form is a distinct example.

D-TOPIC-FACTORY (partial; DOC-TOPIC lines 385-393). Subjects: TOPIC-FRESH-CHECK,
TOPIC-RESUME-CHECK, S-TOPIC-FRESH-BYTES, S-TOPIC-RESUME-BYTES. unique and sorted
collectFile transform raw emissions. Exact final bytes detect missing distinct
versions but cannot prove every sent item, multiplicity or original channel
order.

D-TOPIC-VERSION (not-asserted; DOC-TOPIC lines 343-348). Subjects: none.
Historical version availability and preview-flag conditions are frozen
documentation, not runtime tests.

D-TOPIC-FEEDBACK (not-asserted; DOC-TOPIC lines 395-397). Subjects: none. No
consumer-emitter feedback cycle is selected.

D-TOPIC-TYPED-CLOSURE (unresolved; DOC-TOPIC lines 399-399). Subjects: none. The
typed topic-output reference remains unreviewed.

[charter]:
  charter.md

[phase4]:
  nextflow-pilot-phase4.md

[handoff-review]:
  contracts-review.md

[original-review]:
  original-results-review-01.md

[joint-review]:
  phase3-results-review-01.md

[inventory]:
  inventory.json

[expectations]:
  expectations.json

[prerequisites]:
  prerequisites.json

[prereq-review]:
  prerequisites-review-01.md

[resources]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites01/resource-closure.json

[originals]:
  original-results.json

[instrumentation]:
  instrumentation-review-01.md

[mapping-preflight]:
  mapping-preflight-review-01.md

[projections]:
  projection-results.json

[controls]:
  control-results.json

[prereq-author]:
  item2.1-author-01.md

[original-author]:
  item2.2-author-01.md

[control-author]:
  item3.2-author-01.md

[index]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/report04/reconciliation.json

[clock]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/execution-clock.json

[effort]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/report04/effort-reconciliation.json

[end]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/report04/completion.json

[evidence]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/report04/

[root3]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/root-phase3-delivery.json

[root4]:
  ../../../.tmp/agent/nextflow-conformance/research-pilot/root-phase4-start.json
