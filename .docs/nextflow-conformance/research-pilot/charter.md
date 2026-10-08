# Nextflow test preservation research pilot charter

## Overview

Test independently authored engine-neutral candidate contracts against
a finite first group of pinned Nextflow tests. Measure assertion preservation,
observer adaptation, genuine harness prerequisites and independent review
effort before recommending a wider assurance approach. [Accepted research]
supports this experiment; it proves neither exhaustive coverage nor a chosen
architecture. [Clarification 01] records no pending user decision.

Target Nextflow 26.04.6, source commit
`232b60569865e9a4577e48c1955409238359d6ca`, with
`NXF_SYNTAX_PARSER=v2`. Preserve selected script flags: no static-typing or
topic-preview flag is added. The params controls need no typing flag.
Nullable's typed DSL execution is outside this first group; its Bash harness
controls remain required below. A future wr adapter is pending: no supported
wr DSL boundary exists and no wr execution pass can be awarded.

Keep the eventual all-upstream behavioral objective visible, including every
internal assertion and documented gaps. This group is neither a representative
rate sample nor a complete language denominator. MultiMap, CPU, subworkflow,
nullable DSL and map-mode cases remain later research, not scoped exclusions
that discharge their original assertions.

## Research artifacts and boundaries

All retained artifacts belong under this directory; executable scratch and
caches belong under `.tmp/agent/nextflow-conformance/research-pilot/`. Existing
verified cache resources may be reused. A separate `research-manifest.json`
records source-relative paths, inclusive lines, byte offsets, full/span SHA-256,
byte counts, Git blobs, modes, tools, acquisition origin and dependency edges.
Record reused and acquired resources separately and verify bytes before use.
[Source facts] and `pinned-evidence.json` in the [span directory] provide
starting provenance.

Before any executable adapter, hand off `contracts.md`, `inventory.json`,
`expectations.json` and `fixtures/` to an independent expectation/preservation
reviewer. Record review and immutable artifact hashes in `contracts-review.md`.
Each expectation has a stable ID, typed input, comparator, expected value,
observation boundary and source/document rationale. Inventory links every
original predicate, helper, fixture, invocation and child unit to its
disposition. Preserve original, strengthened and documented-gap origins as
separate claims, even when they share inputs. Author expectations from source
and documents before observations; disagreements require review, not edits
that turn observed Nextflow output into expected truth.

Use `prerequisites.json`, `original-results.json`, `mapping-review.md`,
`projection-results.json`, `control-results.json` and `pilot-report.md` for
later captures. These are research records, not new production APIs or ledger
schemas. Each result links raw captures, commands, cwd, environment, start/end
times, deadline, exit/signal, per-predicate outcome and aggregate outcome.
Hash diagnostics, values, files and logs. Capture completion explicitly.

Retain production source lock, all eighteen bootstrap batches, code, core
specification and six phase plans unchanged. Item 2.1 F11 remains deferred and
Item 2.2 held. This charter authorizes no general importer, production runtime,
specification revision, commit or push. Root owns delivery and status files.

## Section A: Frozen original contracts

### A1: Parser methods and all eight inputs

Use the `modules/nf-lang/src/test/groovy/nextflow/script/parser/` prefix for
`ScriptAstBuilderTest.groovy`. Select these exact methods and inclusive lines:

- `should report an error for invalid syntax`, 43-136: P1-P5 in source order.
- `should report an error for mixing script declarations with statements`,
  138-153: P6.
- `should report an error for params block without an entry workflow`,
  155-183: P7 rejection, then P8 acceptance.

For P1-P7, inventory four separate predicates: count, start line, start column
and original message. Count is 1 in each. Locations and message predicates:

- P1, missing argument comma: `(2,14)`, equals `Unexpected input: '3'`.
- P2, colon after debug directive: `(2,16)`, equals
  `Unexpected input: '\n'`. Here `\n` denotes the backslash and `n` characters
  in the decoded message, not a newline byte.
- P3, missing tuple comma: `(3,24)`, equals `Unexpected input: 'val'`.
- P4, missing comma before emit: `(3,40)`, equals `Unexpected input: 'emit'`.
- P5, comma after take input: `(4,6)`, equals `Unexpected input: ','`.
- P6, top-level println plus workflow: `(1,1)`, contains
  `Statements cannot be mixed with script declarations`.
- P7, params without entry workflow: `(1,1)`, equals
  `Params block cannot be defined without an entry workflow`.
- P8, same params with empty entry workflow: count equals 0 only.

There are 29 original predicates, not eight generic rejection checks. Keep
each literal source span and genuine Groovy decoding, including leading-line
suppression, escaped diagnostics, nested strings and terminal whitespace.
Retain raw and decoded bytes separately. `setupSpec()` supplies one `@Shared`
parser; preserve original method/subcase state and sequence. TestUtils lines
49-101 apply `stripIndent()`, use `main.nf`, parse and analyze, filter
`SyntaxErrorMessage` causes and sort by line then column. Retain these steps
and exception observations. CLI diagnostic projection needs reviewed mappings
for count, location and message independently; an exit alone preserves none.

### A2: All three Mix methods

Use `modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy`:

- M1, `should mix channels`, 30-48: inputs numeric `[1,2,3]`, strings
  `['a','b']` and value `'z'`. Six predicates require membership of
  `1,2,3,'a','b','z'`; the seventh excludes `'c'`.
- M2, `should mix with value channels`, 50-57: value `1`, queue `[2,3]`;
  `result.val.sort()` equals numeric `[1,2,3]`.
- M3, `should mix with two singleton`, 59-66: values `1` and `2`;
  `result.val.sort()` equals numeric `[1,2]`.

Keep nine original value predicates and class `@Timeout(5)`. Preserve literal
decoding without adding TestUtils normalization. Dsl2Spec lines 33-38 reset
task, script metadata and global state and initialize NF. ScriptHelper lines
161-181, 241-277 and 281-363 retain MockSession, last-result observation,
normalization, dataflow start/await/destroy and error propagation. The v2
ScriptLoader lines 89-100 capture the last statement. Mock scriptlets return
script text and exit zero; this does not prove real shell execution.

Keep collection completion and type/value distinctions in any observer.
M1's original predicates accept both analytical witnesses
`[1,2,3,'a','b','z',1]` and `[1,2,3,'a','b','z','d']`. Strengthened S-MIX
requires the exact six-item multiset, each value once, with any permutation
accepted. M2/M3 already preserve multiplicity through sorted equality.
Review S-MIX against the selected Mix documentation; retain its new origin.

### A3: Arity and topic, each fresh and resumed

Retain complete `tests/process-arity.nf` lines 1-34 and
`tests/topic-channel.nf` lines 1-35 plus their adjacent `.checks` files.
The genuine runner is `tests/checks/run.sh` lines 26-99. Run only these two
selected workflows in a disposable copied test layout, preserving checks,
hidden fixtures, config discovery and ignore rules. Its writable cleanup must
affect only that layout. Record actual tools, NXF command, Java, TEST_JDK,
WITH_DOCKER, config files and effective executor/container mode. The declared
container in `tests/nextflow.config` does not establish container execution.

Arity retains two literal `[[ $? == 0 ]] || false` predicates after fresh and
`-resume` invocations. `set +e` overrides inherited errexit; successful final
resume expression can hide fresh failure. Original aggregate status follows
the runner's final `bash -ex .checks` status. Preserve literal outcomes and
propagation separately. Stronger S-ARITY requires each engine exit zero.
The source exercises valid counts 1, 2 and `1..*`; it does not assert contents,
invalid counts, scalar/list behavior or actual cache reuse.

Topic retains both enforced `cmp versions.txt .expected || false` predicates,
fresh and `-resume`, under inherited errexit. The fixture is exactly 22 bytes,
`bar: 0.9.0\nfoo: 0.1.0\n`, SHA-256
`ce5e4c500ca731aa86fa5e5a3856b9bdbe3c64f685d0e51b2ba5d1886b13bceb`.
The displayed escapes denote newline bytes. Capture each invocation exit and
file separately; S-TOPIC explicitly requires both exits zero and both exact
byte comparisons. Neither comparison alone proves cache reuse.

These original denominators are six Spock methods, eleven Spock input units,
38 Spock predicates, two workflow/check pairs, four CLI invocations and four
literal CLI predicates. Nullable control checks are separate from this group.
No table rows or generated providers occur in the selected methods; record
zero rather than silently omitting these inventory dimensions.

## Section B: Independent expectations and gaps

### B1: Bounded document reconstruction

Freeze these source-relative inclusive ranges with hashes in the manifest:
`docs/strict-syntax.md` 78-123; `docs/workflow-typed.md` 3-17;
`docs/reference/operator.md` 826-852 with complete literal includes
`docs/snippets/mix.nf` and `mix.out`; `docs/reference/process.md` 155-177 and
207-223; `docs/reference/channel.md` 339-399. Record allowed/rejected forms,
types, order, counts, defaults, warnings and feature conditions independently
of the test inventory. The Mix documentation tokens are strings, unlike M1's
numeric values. Keep this distinction in expectations.

List outgoing semantic links, including process-multiple-input-files,
syntax-workflow-typed, migrating-static-types and process-typed-topics, as
unresolved unless their pinned requirements are separately reviewed. Neither
these spans nor the grammar establish full-language closure.
Predicate-to-facet links require a reviewer rationale about what violation
they detect.

### B2: Three documented-gap examples

Author separate G-IN, G-OUT and G-SHAPE contracts, not claims that the entire
upstream suite lacks their tests. G-IN supplies one existing file to a path
input requiring arity 2; G-OUT produces one file for output arity 2. Each must
fail for the specific arity count violation, with declared/actual counts and
the affected input/output associated in the capture. An unrelated parse,
missing-tool or fixture failure is not a pass. Review the diagnostic observer
without inventing a documentation promise of exact diagnostic text.

G-SHAPE produces one file with output arity 1 and with output arity `1..*` in
separate minimal cases. Expected emitted values are respectively one file and
a one-element file list. A reviewed type-preserving observation must establish
that distinction; identical printed filenames alone leave mapping unresolved.
These cases use v2 with typing disabled and retain exact authored fixture bytes.

## Section C: Execution and preservation gates

### C1: Genuine originals before executable projections

Resolve the actual selected original dependency and fixture closure. The
verified distribution lacks Spock/JUnit Platform, selected test classes and
ScriptHelper. Runtime POMs alone cannot close that gap. Inspect pinned build
and fixture dependencies, JVM arguments and compiler/test launch semantics.
Starting pins are Java toolchain 21, retained Temurin 21.0.12.1+1, Gradle
9.3.1, Groovy 4.0.31, Spock 2.4-groovy-4.0 and JUnit launcher 1.10.5.
Record actual resolved transitive versions and source-generated test/fixture
classes, not an assumed list inferred from these pins.

Acquire only genuinely required research resources into the separate cache
and hash-bind them in the research manifest; never change the production lock
or bootstrap batches. Use isolated build output and Gradle caches. Run genuine
selected Spock methods with original setup/state/assertions and the genuine
selected CLI checks first. Retain original failures, skips and non-completion.
Record how selection preserves method order and shared state. Diagnostic
instrumentation must be reviewed for preserving the original harness result.

Only after the handoff review and original attempts, investigate small manual
neutral projections and observers. Review each mapping against source,
expectations and genuine captures before awarding equivalence. Internal AST,
JVM identity or mock assertions need an equivalence argument or explicit
unresolved/internal disposition; output agreement cannot discharge them.
For a missing original prerequisite, stop the affected runtime mapping and
record its exact cause and evidence. Data-only/control work may continue but
cannot earn original execution, oracle or translation passes.

### C2: Failure propagation and loss controls

Independently reproduce the four reviewed failure-propagation (F1) subjects
using byte-identical arity/nullable `.checks` under `bash -ex .checks`.
Required original aggregate exits, in order, are 0, 1, 0, 1:

1. Arity fresh exit 1, resume exit 0.
2. Arity fresh exit 0, resume exit 1.
3. Nullable fresh wrong bytes, resume expected bytes, both engine exits 1.
4. Nullable fresh expected bytes, resume wrong bytes, both engine exits 0.

The stronger per-invocation exit/byte gates reject each with its specific
failing invocation/predicate. Paired valid arity and nullable subjects use
zero exits and exact applicable bytes; both original and stronger gates
accept. Nullable expected bytes are exactly `empty input\n\n`, 13 bytes,
SHA-256 `d4aa42007b1cbfce672a372a1a97587ffdd4102f52dc3b200314a8081206a019`.
Preserve its two pipeline-status and two byte expressions, `set +e`, tee
status without pipefail and final-expression aggregate rule. These subjects
prove harness behavior only, not Nextflow or wr DSL execution.

In isolated copies, remove each P1-P8/M1-M3 unit in turn, each of the 38 Spock
and four CLI predicates in turn, each selected fixture, each resume invocation
and each required completion record. Mutate topic's final newline and one of
nullable's two terminal newlines separately. Every loss must fail the matching
identity/accounting/byte/completion gate with the missing ID or changed hash,
not generic command failure. Intact counterparts pass those gates. Test M1's
two witnesses against original predicates and S-MIX: original accepts both,
S-MIX rejects duplicate 1 and extra 'd'; a six-value permutation passes both.

### C3: Research acceptance and UATs

- R-UAT-01: Independent review accepts the hashed handoff. All selected units,
  original predicates, state, literals, fixtures and document facets have IDs,
  provenance and dispositions; expected values are independent of observations.
- R-UAT-02: Actual prerequisite resolution and original attempts produce
  hash-linked captures for every scheduled unit. Available originals run with
  genuine harnesses; unavailable units name a specific dependency/fixture or
  deadline cause. Candidate enumeration alone does not satisfy this UAT.
- R-UAT-03: Each attempted projection/gap has a reviewed observer and measured
  outcome, or a specific failed/unresolved mapping with captured cause. Record
  original-only, partial, unresolved/internal, strengthened, documented-gap,
  oracle and pending-wr claims separately. No nonexecuted mapping passes.
- R-UAT-04: All six F1 subjects and all applicable loss controls have paired
  intact results and expected specific rejection reasons. Original and stronger
  statuses remain separate; uncontrolled infrastructure errors fail this UAT.
- R-UAT-05: Independent review of `pilot-report.md` reconciles every denominator
  and result, preserved assertions, adaptation defects, disagreements, acquired
  dependencies, time and review effort. It recommends a next research route
  with measured reasons, remaining gates and an owner for unresolved work.

## Experiment order and deadlines

Perform stages sequentially, with one heavy workload at a time. From execution
start, cap the experiment at eight active hours; record spent time by stage.

1. Within two hours, freeze provenance and independently author/review the
   handoff and document facets. No executable adapter precedes acceptance.
2. Within two hours, resolve/acquire/hash selected prerequisites and attempt
   genuine originals. Bound each acquisition at five minutes, selected build
   at 30 minutes, Spock launch at five minutes while retaining Mix's five-second
   timeout, and each CLI invocation at two minutes. Terminate process trees on
   deadline; retain timeout captures and account for all unrun units.
3. Within two hours, investigate manual projections, reviewed strengthened/gap
   expectations and paired controls. Limit observer work to this group; each
   gap/projection launch has two minutes and each Bash subject ten seconds.
4. Within two hours, reconcile results and obtain independent pilot review.
   Record review minutes and rounds by contract/mapping, not just elapsed time.

Deadline exhaustion or an actual prerequisite/mapping failure may be a measured
research result only with its specific cause, attempted command, captures and
all affected units accounted for. If required controls or review cannot finish,
report the pilot incomplete and route remaining work. Research completion is
R-UAT-01 through R-UAT-05 accounting and review, not automatic success after a
failed launch. Report each execution and preservation verdict independently.

## Exit decision

The report compares retained original harness evidence, unchanged workflow
adaptation and manual neutral contracts using measured preservation,
adaptation/prerequisite failures and author/review effort. Recommend expansion
only for a proven family, focused prerequisite/observer research where results
are unresolved, or stopping a demonstrated unsuitable mapping. Additional
documented tests remain necessary whichever route is chosen. Root routes the
reviewed recommendation to the later assurance decision; exact design,
full-language scope and JVM/typed-language policy remain open until that
evidence is assessed.

[Accepted research]: ../reviews/nextflow-research-report-review-02.md
[Clarification 01]: clarification-01.md
[Source facts]:
  ../../../.tmp/agent/nextflow-conformance/pilot-clarification01/facts.json
[span directory]:
  ../../../.tmp/agent/nextflow-conformance/research-test-translation/
