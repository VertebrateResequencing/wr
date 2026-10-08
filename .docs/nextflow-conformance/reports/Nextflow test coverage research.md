# Nextflow test coverage research

**Exhaustive upstream coverage of Nextflow 26.04.6's documented strict DSL2
language has not been established.** Upstream does publish a language
specification, and its accepted parser decision says that specification
enumerates every supported syntax construct. That statement concerns the
language description; it does not certify exhaustive tests. The inspected
suite contains useful parser, runtime, workflow and documentation tests,
but this research obtained neither a complete requirement-to-assertion
matrix nor release-commit execution and coverage results. Source inspection
supports reuse of some unchanged workflows and expected files, plus reviewed
translation of bounded Spock forms. It does not establish a whole-suite
translation rate or validate any cross-engine adaptation. The latest
candidate is an independently authored engine-neutral suite covering all
upstream behavioral scenarios, correcting identified weaknesses
and then adding documented-language gaps. Its independently reviewed
expectations would serve real Nextflow and future wr adapters. This remains
a hypothesis pending a minimal executable pilot of preservation, observers,
dependencies and review cost. This report does not accept an assurance
architecture or authorize spec revision or further implementation.
([Strict guide][strict], [accepted parser decision][adr],
[upstream CI][ci], [Mix tests][mix])

## The specification exists, but parser v2 does not define every boundary

Research date is 2026-10-08. The target is **Nextflow 26.04.6 at commit
`232b60569865e9a4577e48c1955409238359d6ca`, using parser v2**. The pinned
migration guide explicitly identifies the Syntax reference as the language
specification and describes strict DSL2 as a subset of Groovy syntax for
scripts and configuration. v2 is the default in 26.04. The accepted
architecture decision separately describes an ANTLR script grammar,
configuration grammar, AST construction, include resolution, name checking,
type checking and conversion to Groovy AST. Its claim that the specification
enumerates every supported syntax construct is an upstream statement about
descriptive completeness. Its AST-validity statement is a design claim about
error checking. Neither statement asserts exhaustive test coverage.
([Pinned strict guide][strict], [accepted parser decision][adr])

An audit restricted to the Syntax page would leave its own semantic links
unexamined. That page delegates typed workflow and process semantics to
other pages and delegates stage directives and output functions to the
process reference. Configuration also has a separate language description
and grammar. Therefore the first research decision is which pinned document
closure and modes define the claim. Grammar alternatives can help enumerate
accepted forms, but contextual restrictions and runtime behavior need their
own contracts. For example, the script grammar accepts sequences of
declarations or statements; the AST builder separately rejects mixing them.
Visiting the grammar alternatives alone would not verify that restriction.
([Syntax semantic links][syntax], [configuration guide][config-guide],
[script grammar][grammar], [AST contextual check][builder])

**Strict parser selection does not settle static typing or JVM extension
policy.** Typed processes are preview features requiring v2 and
`nextflow.enable.types = true` in each using script. Typed workflows require
the same flag, while their params and output blocks can be used without it.
The strict guide permits moving arbitrary Groovy into `lib/` or plugins, and
the parser decision explicitly retains Groovy execution. A wr profile must
eventually state how it handles strict configuration, typed features,
library calls, `lib/`, plugins and runtime integrations. This research does
not choose those policies. The decision's historical minimal-type-checking
description cannot override the pinned typed-language documentation.
([Typed processes][typed-process], [typed workflows][typed-workflow],
[Groovy preservation][preserve], [parser decision][adr])

The three completed research facets extend the earlier source assessment.
Their manifests retain locked source hashes, exact spans and retrieval
limitations. The earlier inventory verified 661 unique files across
`nf-lang/src/test/`, `nextflow/src/test/` and `tests/`; those groups contain
12, 340 and 309 files respectively. These are file identities, including
helpers and data, rather than semantic coverage or executed-case counts.
Other modules, plugins, legacy tests, documentation and external suites need
separate applicability decisions. The synthesis rechecked bounded primary
source passages needed for its conclusions. It performed no Nextflow or wr
execution, build, dependency download or mutation run.
([Pinned test directories][lang-tests], [runtime test directory][runtime-tests],
[integration directory][integration-tests];
[local inventory evidence][inventory], [coverage evidence][coverage-record],
[translation evidence][translation-record],
[requirement evidence][requirement-record])

## Suite size and code coverage cannot establish semantic exhaustiveness

The pinned build generates JaCoCo reports after tests and excludes Groovy
closure classes from report class directories. CI runs `make test` and
uploads unit-test reports; the inspected upload path does not include
JaCoCo reports. It also defines integration, parser-v2, documentation and
cloud lanes. The v2 runner explicitly selects v2 and runs `tests/`; the
legacy runner selects v1 and also runs `tests-v1/`. Ignore files, Docker,
Java and credential conditions affect which tests run. A configured lane
establishes an intended invocation, not proof that all cases ran successfully
at this release commit. Exact release-commit outcomes, skips and numeric
coverage results were not obtained.
([JaCoCo build configuration][build], [CI configuration][ci],
[validation modes][validation], [conditional integration runner][runner])

Even a recovered JaCoCo percentage would measure implementation execution.
Its branch counter covers `if` and `switch` branches but excludes exception
handling; its line counter records executed instructions associated with a
line. Those denominators do not enumerate documented behavior or judge the
strength of assertions. Mutation testing can test whether selected wrong
implementations escape selected predicates, but its result remains bounded
by the mutations, tests and environments used. Neither technique supplies
a missing requirements denominator. The documentation-snippet runner adds
another useful but limited contract: it sorts actual and expected output
before comparison and skips the comparison when no matching `.out` exists.
That procedure loses output-order evidence and does not enumerate every
example embedded in Markdown.
([JaCoCo counters][jacoco], [PIT method][pit], [snippet runner][snippets])

The clearest limitation comes from actual predicates. The first of three
MixOpTest methods checks membership of six values and absence of `c`.
Outputs `[1,2,3,'a','b','z',1]` and `[1,2,3,'a','b','z','d']` satisfy those
seven predicates. This analytical witness shows that the method cannot
establish exact multiset conservation for its input. The other two methods
compare sorted lists and preserve multiplicity for their different inputs.
No injected Groovy mutant was run, and the witness does not establish that
the complete upstream suite lacks conservation tests. The documented
permission for arbitrary mix order also means an observer must accept
permitted permutations; it must not demand that a runtime demonstrate
nondeterminism. The documentation example uses string-valued numeric tokens,
so its types should remain distinct from the numeric upstream test input.
([Complete Mix tests][mix], [documented mix behavior][mix-doc],
[mix example][mix-example]; [analytical witnesses][witnesses])

Other samples show the same distinction between exercising a construct and
asserting its documented consequences. The process-arity workflow exercises
valid file counts. Its shell script contains fresh and resume status
expressions, but an aggregate pass can hide a failed fresh invocation. The
inspected documentation also specifies invalid-count failure and
scalar-versus-list output behavior; those expressions do not assert either.
The CPU table checks five rows of internal TaskConfig getters and declaration
presence. Those predicates do not establish what an actual scheduler
receives. The map documentation distinguishes null behavior by the static
typing flag, so a method named for map cannot establish both modes without
appropriate inputs and predicates. These are **limits of named contracts**,
not findings of whole-suite absence.
([Arity workflow][arity], [arity checks][arity-checks],
[documented arity][process-doc], [CPU table][cpu],
[mode-dependent map behavior][map-doc])

To establish complete coverage of a declared finite documentation scope,
researchers need independently reviewed required facets and links to
assertions capable of detecting each facet's violation under recorded input
and mode conditions. Bidirectional traceability is an available engineering
method for preserving those relationships, rather than an upstream Nextflow
certification rule. One missing required facet would refute complete
accounting for that denominator. To claim that the entire upstream suite
lacks its test requires inspecting the complete relevant tests and helper
closure. Keyword searches locate candidates; missing matches cannot prove
semantic absence. No complete normative closure, facet denominator,
traceability matrix, formal conformance standard or exhaustive upstream test
claim was discovered in this bounded research. That is a limitation of the
evidence obtained, not proof that such evidence exists nowhere.
([Traceability guidance][nasa], [parser test example][parser],
[retained search scope and retrieval log][coverage-search])

## Reuse follows assertion boundaries rather than file extensions

Some complete `.nf` workflows and expected files are credible unchanged-input
candidates. Process arity contains two literal status expressions, whose
failure propagation must be retained separately from their presence. Topic
channels supplies two exact `versions.txt` comparisons against preserved
expected bytes. Named subworkflows instead assert four submitted-process
log counts and four cached-process counts. Hello also requests Nextflow
report, timeline, trace and DAG artifacts. A wr invocation adapter can
preserve simple workflow and byte-comparison contracts, but preserving the
other assertions requires a reviewed observation of process identity,
cache behavior and instrumentation. Dropping those checks would translate
only part of the test. Configuration discovery, environment flags,
containers, shell tools and invocation variants remain execution inputs.
The global container declaration in `tests/nextflow.config` does not alone
prove Docker was used for a particular invocation.
([Arity checks][arity-checks], [topic checks][topic-checks],
[topic expected bytes][topic-expected], [subworkflow checks][sub-checks],
[hello checks][hello-checks], [fixture config][fixture-config])

The effective shell pass rule limits that reuse claim. The runner invokes
`bash -ex .checks` and checks the subprocess's final exit status. Arity and
nullable-path both start with `set +e`, disabling the inherited `-e`.
Arity retains `[[ $? == 0 ]] || false` after each invocation, but a failed
fresh expression can be followed by a successful final resume expression
and an aggregate exit of zero. Nullable retains that expression after each
`tee` pipeline and `cmp .expected .stdout || false` after each output.
Without pipefail, its status expressions observe `tee`; a failed fresh
comparison can also be followed by a successful final comparison and exit
zero. Topic inherits `-e` and retains two enforced byte comparisons. These
are different original aggregate contracts despite similar check syntax.
([Runner launch and status][runner], [arity expressions][arity-checks],
[nullable expressions][nullable-checks], [topic comparisons][topic-checks])

Four controlled Bash subjects reproduce original aggregate exits **0, 1, 0,
1**: arity fresh failure/resume success; arity fresh success/resume failure;
nullable fresh wrong bytes/resume expected bytes with both subjects exiting
1; and nullable fresh expected bytes/resume wrong bytes with both exiting
0. The pinned `.checks` and nullable expected bytes remain unchanged, with
`empty input\n\n` decoded as two terminal newlines. Independent per-invocation
exit and applicable byte gates reject all four failing controls; paired valid
subjects with both exits zero and exact applicable bytes pass both the
original checks and new gates. This is **harness-control proof only, not DSL
observation**. It establishes shell
failure propagation, without establishing a Nextflow or wr runtime defect.
A stronger translated runner must label enforcement of every status and
byte check, plus independent engine exit capture, as newly authored gates.
([Retained controls and identities][shell-proof],
[nullable expected bytes][nullable-expected])

The immutable initial assessment's P06 statement that `.checks` asserts
fresh and resume success needs this contextual correction: both expressions
are present, but only the final expression determines aggregate status in
the controlled arity cases. Its P09 pipeline warning remains valid but
omits early byte-comparison failure swallowing. The original assessment is
historical evidence; the effective contracts above govern this synthesis.
([Initial assessment, P06 and P09][initial-assessment],
[review finding F1][report-review], [independent controls][shell-proof])

Spock operator tests often have reusable literal inputs and value predicates,
but their observer is internal. Dsl2Spec resets shared Nextflow state;
ScriptHelper constructs a MockSession, selects a loader, evaluates the
snippet, starts the network, waits and normalizes returned dataflow values.
The v2 loader captures the last statement for testing. Most importantly,
the helper's mock executor returns script text as stdout with exit status
zero for shell scriptlets; it does not launch the shell. A public CLI
wrapper therefore needs validation against the original predicate and
helper semantics. Real shell execution is a different contract from mock
script-text observation. Seeing the same final value does not discharge
an internal AST-shape, JVM-type or mock-interaction assertion.
([Dsl2Spec setup][dsl2], [script helper][helper], [v2 loader][loader],
[AST helper tests][ast-helper], [Spock interactions][spock-interactions])

Parser translation must retain the actual normalization and diagnostics.
One invalid-syntax method contains five sequential inputs, each checking
error count, line, column and message. The params method contains both a
rejection and an acceptance. TestUtils strips indentation, uses `main.nf`,
parses and analyzes, filters syntax errors and sorts locations. The parser
is shared in the original test. Changing literal decoding, parser state,
filename or normalization can change the assertions. A public CLI's rendered
diagnostic is also a different boundary from the original exception object.
([Parser subcases and shared parser][parser], [TestUtils contract][test-utils])

**Mechanical extraction is plausible for bounded forms; mechanical
equivalence is unproved.** Outer Spock files are Groovy classes, while the
Nextflow grammar parses their inner scripts. A structural host parser or
Groovy AST exporter can preserve blocks, nested strings, closures and source
spans better than regular-expression decomposition. It still needs literal
decoding, fixture scope, equality semantics, predicate recognition and
provider handling. The pinned tests use Spock 2.4, whose tables run once per
row with iteration setup/cleanup and shared-state exceptions. Providers can
also draw from external or generated data. Spock rewrites interactions and
specification ASTs, so an exporter's compiler phase and classpath matter.
Unknown structures must remain visible and unresolved if a selected corpus
is to receive a complete-translation claim. No translator prototype,
validated adaptation or whole-suite translation percentage was established.
([Pinned test dependencies][test-deps], [Spock data semantics][spock-data],
[Spock interaction semantics][spock-interactions],
[Spock compiler transformation][spock-transform])

## Several assurance routes remain credible until a pilot compares them

A workflow-first route would reuse unchanged NF and expected files with a
small invocation and artifact adapter. Its strongest evidence is the
existing byte-comparison fixtures. Its limiting cases are Nextflow-specific
logs, reports, resume semantics and environment setup. It would need separate
parser and internal-test handling. An original-harness route would retain
Spock as a pinned oracle baseline and project suitable predicates into wr's
public observations. That preserves evidence of the original behavior but
requires a resolved test dependency closure and cannot turn internal-only
assertions into wr runtime passes. These routes can coexist; neither alone
establishes documented-language coverage.
([Topic contract][topic-checks], [instrumentation contract][hello-checks],
[mock harness][helper], [test dependencies][test-deps])

An AST-export route would recognize bounded Groovy/Spock structures and emit
reviewed neutral cases. It reduces manual host-syntax parsing while adding a
pinned development JVM, compiler phase and export contract. A Go recognizer
would avoid that exporter dependency but require its own relevant Groovy
literal and structure handling. A manual-first route would extract reviewed
cases before automating repeated forms. It has less importer implementation
cost and more review work per case. The samples support investigating these
tradeoffs; they do not establish suitewide throughput, cost or an optimal
choice. An exporter dependency is also distinct from a production runtime
decision about Groovy/JVM compatibility. Choosing one does not automatically
choose the other.
([Nested parser literals][parser], [closure-bearing CPU rows][cpu],
[Spock compiler][spock-transform], [upstream runtime decision][adr])

A documentation-first route would independently reconstruct requirements
and author missing tests while using upstream cases as regression witnesses.
It adds interpretation and test-authoring work. The user's latest candidate
combines those sources in an independently authored engine-neutral suite:
cover all upstream behavioral scenarios, correct identified weaknesses,
apply the same independently reviewed expectations to real Nextflow and
future wr adapters, then cover documented-language gaps. This is a route to
investigate through manually authored neutral pilot contracts before choosing
automation. Preserve three provenance groups: upstream behavioral contracts,
new corrections or strengthening, and independently documented requirements
missing from the selected upstream contracts. The candidate remains a
hypothesis until an executable pilot measures preservation and adaptation.
([Original predicates][mix], [arity contract][arity-checks],
[documented arity requirements][process-doc], [traceability method][nasa])

"All upstream behavioral scenarios" requires a complete upstream-unit
inventory retaining every internal assertion as well as public predicates.
The shared suite can claim behavioral equivalence only after review of each
mapping. JVM identity, AST shape and mock interactions need an equivalence
argument or an explicit unresolved/internal disposition in the original
inventory. A public output match does not discharge those assertions. If
literal preservation across both engines is intended, the pilot must test
that feasibility; a portable subset cannot silently replace that scope.
Nextflow observations are evidence to compare with independently reviewed
expectations, not automatic expected truth. Record expectation, documentation
or engine disagreements for review, preserving the original result and the
strengthened contract rather than changing expectations to obtain a pass.
([Internal AST assertions][ast-helper], [mock boundary][helper],
[Spock interactions][spock-interactions], [Mix predicates][mix])

Adjacent projects offer comparison material, not a ready-made target suite.
Language-server v26.04.0 depends on nf-lang 26.04.0 and has configuration
tests involving a plugin-spec visitor. Its release policy separates runtime
and language-server patch releases. nf-test offers workflow, process,
function and snapshot assertions. Those documented checks do not establish
an importer for Nextflow's internal Spock suite. Its mutable project
documentation and current strict-parser
training need separate version pins and applicability review. Neither source
inherits the 26.04.6 lock or proves cross-engine compatibility. The attempted
language-server inventory was incomplete, and no external corpus was built
or run in this research.
([Language-server release build][lsp-build], [release policy][lsp-policy],
[configuration test][lsp-test], [nf-test project docs][nf-test],
[current strict-parser training][nf-training],
[retrieval limitations][translation-search])

## A bounded pilot should determine scope, preservation and execution gates

A short reviewed pilot charter should name the finite methods, subcases,
rows, invocations and document ranges before execution. Freeze parser v2,
selected typing flags and the linked documentation for this bounded subset;
retain unresolved linked requirements. Full-language closure reconstruction
is a later prerequisite for a full-language claim, not for bounded results.
Within those ranges, an author and independent reviewer
should reconstruct allowed and rejected forms, defaults, overloads,
boundaries, warnings, examples, feature flags and selected interactions from
exact source spans. They should retain ambiguous rules and unresolved linked
pages. A second reconstruction should inventory test files, methods,
sequential subcases, table rows, assertions, generated-provider expressions,
fixtures, setup, invocation variants and skips. The two denominators answer
different questions. A test name or grammar match supplies a candidate link;
the link's acceptance requires a rationale explaining what the predicate can
detect at its observation boundary.
([Declaration documentation][declarations], [parser contracts][parser],
[Spock row and provider semantics][spock-data], [traceability guidance][nasa])

The existing nine candidates provide a useful varied pilot rather than a
representative sample for rate estimation. Begin with the five-input parser
method, all three mix methods, fresh/resume arity and the exact topic-file
comparison. Include the mixed-top-level rejection and opposite params
outcomes as mapping and normalization controls. Then add MultiMap's three
outputs and terminal observations, five CPU rows plus two resource errors,
named subworkflow log counts, and nullable typed paths. The last case must
retain its type flag and optional/staging behavior even if the chosen wr
profile leaves it pending. The map null-mode note supplies a separate
feature-condition control. These choices expose different assertion,
observer and policy costs before generalizing extraction mechanics.
([Parser cases][parser], [Mix cases][mix], [arity][arity], [topic][topic],
[MultiMap][multi-map], [CPU rows][cpu], [resource errors][cpu-errors],
[subworkflow checks][sub-checks], [typed nullable path][nullable],
[map mode distinction][map-doc])

The preservation gate should require every original predicate and runnable
child unit to retain an identity, input bytes, helper/fixture provenance,
normalization and disposition. Retain literal original expressions and
effective aggregate pass rules separately. Capture each expression's result
and its propagation into the original aggregate status. Review transformations
separately from strengthened expectations. Exact multiset conservation,
arity-failure cases, enforcement of every invocation/status/byte check and
independent engine-exit capture remain newly authored contracts. A public value
observer must retain output association, multiplicity, asserted order and
completion. Unknown assertions, unexpanded providers and internal-only
observations remain pending or explicitly reviewed scoped exclusions; they
cannot disappear from a translated-method count. Loss controls should remove
a parser subcase, CPU row, negative predicate, terminal observation, expected
newline, fixture or resume invocation in isolated research copies. Each
omission should fail the corresponding accounting or preservation check.
Add the four F1 shell controls to the assertion-adequacy gate: preserving
expressions alone cannot establish enforced failure or equivalence. Show
original aggregate status and new per-invocation gate outcomes separately.
([Shell-control proof][shell-proof], [original mix predicates][mix],
[MultiMap completion][multi-map],
[arity checks][arity-checks], [CPU table][cpu],
[nullable shell status predicate][nullable-checks])

The execution gate is still future work. Resolve and hash the original
fixture and dependency closure, then run selected original cases first with
the pinned v2 runtime and recorded mode, configuration and prerequisites.
A bootstrap runtime distribution alone does not supply a Spock/nf-lang
harness. Record JVM, Groovy 4.0.31, Spock 2.4, transitive artifacts, fixtures,
working directories, shell tools, config discovery, containers and writable
fresh/resume state. Use explicit deadlines and retain Mix and MultiMap
completion obligations. Capture each original check and aggregate status.
Validate manually authored or bounded transformed cases against the original
observations and independently reviewed expectations;
preserve original failures rather than rewrite expectations to obtain a
pass. For mock-dependent tests, distinguish original-harness observation
from a new real-process contract. Capture diagnostics, exits, artifacts,
completion and fresh/resume variants. Once a supported wr observation exists,
compare both engines against the same independently reviewed expectations
and then compare their observations. Record discrepancies with documentation,
original contracts or new strengthened expectations rather than treating
Nextflow output as expected truth. A missing wr binding, prerequisite,
unmapped assertion or skipped case remains incomplete evidence. Agreement
between two engines cannot itself validate an incorrectly interpreted
requirement.
([Mock execution boundary][helper], [parser normalization][test-utils],
[integration invocation][runner], [typed nullable checks][nullable-checks])

Route selection should use the pilot's measured unresolved forms,
assertion-retention results, mapping disagreements, adaptation defects,
dependency needs, execution prerequisites and review effort. Keep file,
method, subcase, row, assertion, required-facet, partial-contract,
unclassified, exclusion, missing-binding, oracle and differential totals
separate. A successful small AST pilot would justify expansion to that
proven structural family. Persistent exporter or provider problems would
favor manual extraction or retained original-harness evidence for those
families. Strong unchanged-workflow preservation would favor a workflow
adapter for that corpus. Discovered requirement gaps would require added
tests whichever route is chosen. These are proposed decision gates, not
completed results or final architecture approval.

**Retain accepted acquisition work and prior schema work, with their current
review status.** The foundation's 49 tooling UATs test its acquisition,
accounting and evidence behavior; its seven bootstrap oracle cases have
static typing disabled. They neither replace a language inventory nor prove
wr execution or wr-specific durability and recovery. Current Item 2.1 review
06 remains unaccepted because F11 identifies a UTF-8 assertion-strength gap:
production rejects the inputs, but a guard-removal fault escapes the existing
tests. Research completion does not resolve F11 or authorize Item 2.2. A
later proposal should preserve useful completed work and state its additional
claim and gate, rather than treat this report as an accepted spec revision.
([Foundation contracts][wr-spec], [current Item 2.1 verdict][schema-review])

## Conclusion

wr can make a stronger assurance claim by naming its evidence level precisely.
Complete upstream inventory, complete assertion-preserving translation,
reviewed coverage of a declared documentation scope, and successful pinned
execution are separate achievements. An exclusion changes the profile's
scope; it does not discharge the excluded part of the original language.
Even all four achievements would establish the reviewed finite contracts
and input partitions, rather than correctness of every possible program.

The next research investment should reduce uncertainty about those
boundaries before it increases corpus size. A pilot that exposes a missing
facet, unusable observer or expensive harness dependency is decision evidence
as useful as a successful translation. That evidence can support a focused,
independently reviewed proposal for wr's exact assurance approach while
leaving typed-language and JVM compatibility choices explicit.

[strict]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L3-L31
[adr]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/adr/20250508-strict-syntax-parser.md
[syntax]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/syntax.md#L199-L347
[config-guide]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L649-L657
[grammar]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/main/antlr/ScriptParser.g4#L96-L106
[builder]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/main/java/nextflow/script/parser/ScriptAstBuilder.java#L209-L256
[typed-process]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/process-typed.md#L3-L41
[typed-workflow]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/workflow-typed.md#L3-L17
[preserve]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L723-L732
[lang-tests]:
  https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test
[runtime-tests]:
  https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test
[integration-tests]:
  https://github.com/nextflow-io/nextflow/tree/232b60569865e9a4577e48c1955409238359d6ca/tests
[inventory]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/upstream-test-assessment01/inventory.json
[coverage-record]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-coverage-evidence/pinned-evidence.json
[translation-record]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-test-translation/pinned-evidence.json
[requirement-record]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-requirement-coverage/evidence.json
[build]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/build.gradle#L174-L191
[ci]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/.github/workflows/build.yml
[validation]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/validation/test.sh#L48-L126
[runner]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh
[jacoco]: https://www.jacoco.org/jacoco/trunk/doc/counters.html
[pit]: https://pitest.org/
[snippets]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/snippets/test.sh#L3-L27
[mix]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy#L30-L67
[mix-doc]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/operator.md#L826-L852
[mix-example]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/snippets/mix.nf
[witnesses]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-requirement-coverage/predicate-witnesses.json
[arity]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/process-arity.nf
[arity-checks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/process-arity.nf/.checks
[process-doc]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/process.md#L155-L223
[cpu]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy#L286-L304
[map-doc]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/operator.md#L730-L732
[nasa]:
  https://swehb.nasa.gov/spaces/SWEHBVD/pages/102695427/SWE-052%2B-%2BBidirectional%2BTraceability
[parser]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test/groovy/nextflow/script/parser/ScriptAstBuilderTest.groovy#L30-L183
[coverage-search]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-coverage-evidence/search-log.json
[topic-checks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/topic-channel.nf/.checks
[topic-expected]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/topic-channel.nf/.expected
[sub-checks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/subworkflow-take.nf/.checks
[hello-checks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/hello.nf/.checks
[fixture-config]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/nextflow.config
[dsl2]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/testFixtures/groovy/test/Dsl2Spec.groovy#L31-L38
[helper]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/testFixtures/groovy/test/ScriptHelper.groovy
[loader]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/script/parser/v2/ScriptLoaderV2.groovy
[ast-helper]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test/groovy/nextflow/script/control/ScriptToGroovyHelperTest.groovy#L36-L84
[spock-interactions]:
  https://spockframework.org/spock/docs/2.4/interaction_based_testing.html
[test-utils]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/testFixtures/groovy/test/TestUtils.groovy#L49-L101
[test-deps]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/build.gradle#L78-L87
[spock-data]:
  https://spockframework.org/spock/docs/2.4/data_driven_testing.html
[spock-transform]:
  https://github.com/spockframework/spock/blob/spock-2.4/spock-core/src/main/java/org/spockframework/compiler/SpockTransform.java#L26-L71
[lsp-build]:
  https://github.com/nextflow-io/language-server/blob/v26.04.0/build.gradle#L34-L56
[lsp-policy]: https://github.com/nextflow-io/language-server#releasing
[lsp-test]:
  https://github.com/nextflow-io/language-server/blob/v26.04.0/src/test/groovy/nextflow/lsp/services/config/ConfigSpecTest.groovy
[nf-test]: https://github.com/askimed/nf-test/blob/main/docs/index.md
[nf-training]: https://training.seqera.io/latest/side_quests/nf_test/
[translation-search]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-test-translation/search-log.json
[declarations]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L78-L123
[topic]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/topic-channel.nf
[multi-map]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/extension/MultiMapOpTest.groovy#L38-L144
[cpu-errors]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy#L668-L686
[nullable]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/nullable-path.nf
[nullable-checks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.checks
[wr-spec]: /home/ubuntu/wr/.docs/nextflow-conformance/spec.md
[schema-review]:
  /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-phase2-schema-review-06.md
[shell-proof]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-report-fix01/evidence.json
[nullable-expected]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.expected
[initial-assessment]:
  /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-upstream-test-assessment-01.md
[report-review]:
  /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-research-report-review-01.md
