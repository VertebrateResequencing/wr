# Assertion-preserving Nextflow test reuse

## Which pinned upstream tests can run with unchanged inputs and assertions?

### Takeaway

Some complete `.nf` workflows and expected files can be reused unchanged.
The surrounding Spock or shell harness often cannot: it observes internal
objects, mock execution, Nextflow logs, or CLI features. Reuse feasibility
must be decided per assertion and fixture, not per file extension.
[Workflow inputs][arity]; [script helper][helper]; [shell checks][hello].

Research date: 2026-10-08. Target: Nextflow 26.04.6, commit
`232b60569865e9a4577e48c1955409238359d6ca`, parser v2. This extends the
[initial nine-candidate assessment][assessment] through pinned source
inspection and primary external sources. The [evidence manifest][evidence]
rechecks 41 source/fixture files against locked bytes, SHA-256, and Git blob
IDs, plus all ten original pilot spans. No runtime execution occurred.

### Cited Findings

- The integration runner supplies `$NXF_CMD -q run ../../<workflow>` to
  `.checks` scripts. A workflow with no `.checks` is run directly, making
  its success status the assertion. The strict CI lane explicitly sets
  v2 and runs `tests/`; v1 also runs `tests-v1/`. Ignore conditions remain
  part of applicability. [Integration runner][runner]; [CI modes][validation].
- The process-arity fixture contains fresh and resume exit-status
  expressions `[[ $? == 0 ]] || false`. It starts with `set +e`, so the
  runner's `bash -ex .checks` launch does not enforce the failed fresh
  expression. A successful final resume expression can yield aggregate
  exit zero. Its workflow produces/stages files with arities 1, 2, and
  `1..*`. Preserve NF bytes, both expressions and the effective original
  aggregate rule; enforcing each status is a separately strengthened
  contract. The expressions do not compare contents or prove cache reuse.
  [Workflow][arity]; [arity checks][aritychecks]; [runner][runner].
- The topic-channel fixture's `.checks` compares `versions.txt` with the
  preserved `.expected` on fresh and resumed runs. Its workflow combines
  two process topics, `unique`, and sorted `collectFile` with `storeDir`.
  Its expected bytes can remain unchanged. [Workflow][topic];
  [checks][topicchecks]; [expected file][topicexpected].
- The subworkflow fixture checks four process-name counts in
  `.nextflow.log`, then four cached-process counts after resume. The hello
  checks also inspect submitted/cached log records and request report,
  timeline, trace, and DAG files. Executing unchanged shell checks against
  wr would require those Nextflow contracts, not merely workflow values.
  [Subworkflow checks][subchecks]; [hello checks][hello].
- The nullable-path fixture enables types, uses `Path?`, `stageAs`, and an
  optional missing output. Its `.checks` contains two pipeline-status
  expressions and two `cmp .expected .stdout || false` expressions. It
  starts with `set +e`; an early failed comparison can disappear when the
  final comparison succeeds. Without pipefail, pipeline status observes
  `tee` rather than independent engine exit. Preserve every expression,
  both invocations and original aggregate semantics; separately label
  newly enforced per-invocation status, byte and engine-exit gates.
  [Typed workflow][nullable]; [shell checks][nullablechecks]; [runner][runner].
- Independent controlled Bash subjects preserve the pinned `.checks` bytes
  and runner launch form, reproducing exits 0, 1, 0, 1 for arity fresh-only
  failure, arity resume-only failure, nullable fresh wrong/resume expected
  bytes with both subjects exiting 1, and nullable resume wrong bytes with
  both exiting 0. New per-invocation exit/byte gates reject all four failing
  controls. Paired valid subjects with both exits zero and exact applicable
  bytes pass original checks and new gates.
  This is harness-control proof only, not DSL observation. Expected nullable
  bytes are exactly `empty input\n\n`. [Controls and identities][shellproof].
- `tests/nextflow.config` declares a global process container image.
  The run root, configuration discovery, environment flags, shell tools,
  and whether containers are enabled must be retained as execution inputs.
  The container declaration alone does not prove these sample invocations
  use Docker. [Fixture config][fixtureconfig]; [runner][runner].
- `Dsl2Spec.setup()` resets process, script-metadata, and global state, then
  initializes Nextflow. `runScript()` uses a MockSession, selects the script
  loader, evaluates the input, normalizes the returned result, starts the
  dataflow network, waits, destroys the session, and propagates errors.
  [Dsl2Spec][dsl2]; [helper, lines 161-181][helper].
- The helper's mock executor completes shell scriptlets by assigning script
  text to stdout and zero to exit status. It does not launch the shell.
  Non-scriptlet task code is called directly. A process test under this
  helper therefore has a different execution contract from a real CLI run.
  [Helper, lines 281-363][helper].
- ScriptLoaderV2 captures the last statement of a snippet or entry workflow
  for testing. The helper normalizes queues, values, channel outputs, and
  broadcasts into JVM dataflow objects. Returning an internal channel is
  not an ordinary CLI artifact contract. [V2 loader][loader];
  [helper normalization, lines 241-277][helper].
- The selected Mix methods assert membership plus exclusion, or sorted
  sequence equality. They do not all assert the same predicate: the first
  method permits extra values other than the excluded value, whereas the
  other two equalities preserve multiplicity. [Mix tests][mix].
- The selected MultiMap method asserts three outputs, nine ordered values,
  and three terminal sentinels. Another method in that file asserts JVM
  `DataflowVariable` identity; another captures stdout and checks substring
  membership. These are distinct assertion families.
  [MultiMap tests, lines 38-144][multimap].
- Parser tests call `TestUtils.check`, which strips indentation, names the
  script `main.nf`, parses and analyzes it, filters SyntaxErrorMessage
  causes, and sorts by line/column. The original invalid-syntax method has
  five sequential input/assertion blocks; the params-block method has a
  negative and a positive block. [Parser sample][parser]; [TestUtils][utils].
- Tests of ScriptToGroovyHelper navigate the AST and inspect generated
  reference expressions/source text. WorkflowDefTest reads ScriptMeta and
  declared input arrays. ProcessEntryHandlerTest builds mocked internal
  Session/BaseScript/ScriptMeta objects. Such assertions are not public
  workflow output expectations. [AST helper tests][asthelper];
  [workflow metadata tests][workflowmeta]; [mocked handler test][mocktest].
- The CPU table directly constructs TaskConfig, supplies closure context
  `ten: 10`, caps resources at 24, and checks two getters plus declaration
  presence across five rows. Two negative tests assert internal exception
  types and exact messages. [CPU rows][cpu]; [negative CPU cases][cpuneg].

### Inferences

- Preserve unchanged NF and expected bytes where possible. A CLI adapter
  may translate invocation and artifact lookup, but deleting log checks,
  resume invocations, resource-presence checks, or terminal observations
  would weaken the original contract. [Shell checks][subchecks];
  [MultiMap tests][multimap]; [CPU rows][cpu].
- A public replacement for an internal AST/mock assertion is a reviewed
  projection of that test. It should retain the original assertion as
  internal-only or unmapped until an equivalence argument exists. A test of
  final output alone cannot discharge an AST-shape predicate or a scheduler
  resource-request predicate. [AST tests][asthelper]; [CPU rows][cpu].
- Capturing original Spock results is useful as a baseline, especially for
  mock-dependent tests. Replaying a shell process through the real engine
  is a separate behavioral case when the original only returned script
  text. [Mock task handler][helper].

### Gaps

- None of the nine candidates has been executed in this research under the
  pinned v2 runtime or wr. Candidates are source-backed feasibility
  observations, not validated translations.
- The original Spock test dependency closure has not been resolved and
  hashed. The target build declares test fixtures including Groovy-test,
  Spock 2.4, Byte Buddy, Objenesis, and Jimfs; declaration inspection is not
  proof of an executable offline harness. [Test dependencies][testdeps].
- Internal diagnostic objects and public CLI rendering differ. The loader
  catches compilation failure and prints errors through StandardErrorListener
  for file inputs. No exact cross-engine diagnostic mapping was proved.
  [V2 loader, lines 126-143][loader].

## Can mechanical recognizers preserve the original test contract?

### Takeaway

Bounded structural recognizers are plausible for simple literal inputs and
known assertion forms. They need explicit handling of Groovy literals,
Spock blocks, row expansion, state, and helper semantics; parsing an AST
alone does not prove expectation preservation. No translator or automatic
translation percentage was established. [Parser sample][parser];
[CPU table][cpu]; [Spock data semantics][spockdata].

### Cited Findings

- The pinned Nextflow build uses Spock `2.4-groovy-4.0` and Groovy `4.0.31`
  test fixtures. Spock 2.4 executes a feature once per data-table row,
  creates a separate specification instance per iteration, and calls
  setup/cleanup per iteration. Shared/static fields remain shared.
  [Pinned dependencies][testdeps]; [Spock data documentation][spockdata].
- Spock data providers can be iterable objects, external sources, or
  generated values. Data-variable assignments are evaluated each
  iteration. Finite literal rows therefore differ from arbitrary `where`
  providers; they cannot all be expanded by splitting lines on `|`.
  [Spock Data Pipes and Data Variable Assignment][spockdata].
- Spock interactions encode invocation cardinality, target, method, and
  argument constraints. They are not ordinary boolean output predicates.
  Spock moves interactions declared in a `then` block before the preceding
  `when` block. [Spock interactions][spockinteraction].
- Spock's own 2.4 compiler parses and rewrites specification ASTs during
  semantic analysis. An extractor that compiles through this transformation
  observes altered structure, so its chosen phase and classpath matter.
  [SpockTransform source][spocktransform].
- The pinned parser sample contains triple-quoted Groovy literals with
  leading-line suppression, nested double-quoted process strings, repeated
  `when`/`then` pairs, and escaped diagnostic text. The Mix sample's
  `runScript` literals go through another helper path without TestUtils's
  explicit `stripIndent` step. [Parser sample][parser]; [Mix tests][mix];
  [TestUtils][utils]; [runScript helper][helper].
- Nextflow's `nf-lang` parses the inner Nextflow/config source. Outer test
  files are Groovy classes extending Specification/Dsl2Spec. An NF grammar
  for the inner source cannot itself parse the whole Spock host class.
  [ScriptAstBuilderTest class][parser]; [Dsl2Spec][dsl2];
  [strict syntax grammar decision][adr].

### Inferences

- Regex may help locate candidate files or lines. Using it as the authority
  for test decomposition risks confusing nested strings, closure bodies,
  fixture setup, and sequential blocks. A bounded host-language parser or
  Groovy AST exporter can retain that structure, then a small recognizer can
  accept only explicitly supported helper/assertion combinations.
  [Nested parser sample][parser]; [closure table row][cpu].
- A structural recognizer should preserve original source spans, decoded
  input bytes, predicate kind, expected values, statement order, fixture
  scope, provider expression, and all unrecognized nodes. Unknown constructs
  should produce unresolved inventory entries instead of disappear from
  counts. This is a proposed assurance condition, not an upstream guarantee.
- Literal membership, exclusion, sequence equality, sorted equality, size,
  diagnostic fields, byte comparison, and completion can be represented
  separately. JVM class identity, mock interactions, AST structure, arbitrary
  closures, and generated data need further mappings or retained internal
  dispositions. [Mix][mix]; [MultiMap][multimap]; [AST tests][asthelper];
  [Spock data providers][spockdata].
- Groovy AST extraction reduces host-syntax work but adds a pinned JVM
  development dependency and phase-sensitive export contract. A bounded Go
  parser avoids that exporter dependency but must reproduce relevant Groovy
  token/literal behavior. Manual extraction has less parser implementation
  cost but more review cost per unit. No option removes independent
  assertion review or oracle validation.

### Gaps

- No existing upstream Spock-to-Go or Spock-to-engine-neutral translator
  was discovered. This is a bounded search result, not proof none exists.
- No host parser/exporter has been prototyped here. Literal decoding,
  Groovy equality/coercion, closure context, generated-row expansion, and
  sequential shared-parser behavior remain unverified across engines.
- The Groovy 4.0.31 AstBuilder API page was inaccessible. The AST approach
  is supported by Spock's own compiler implementation; no specific
  AstBuilder API suitability claim is made. [Retrieval log][searchlog].

## Which existing corpora and pilots can test preservation before design?

### Takeaway

Use the nine existing candidates as a varied pilot, preserving each original
assertion and separating added checks. Language-server tests and nf-test
provide useful additional patterns, but their versions and contracts need
separate pins and applicability review. They do not establish a ready-made
26.04.6 cross-engine conformance suite. [Initial candidates][assessment];
[language-server release contract][lsp]; [nf-test source docs][nftestdocs].

### Cited Findings

- Language-server release v26.04.0 depends on `nf-lang:26.04.0`, Nextflow
  runtime 26.04.0 definitions, and separately versioned plugin definitions.
  Its README says Nextflow and language-server patch releases are independent.
  These are adjacent-release sources, not the locked 26.04.6 target.
  [Language-server build][lspbuild]; [release policy][lsp].
- Its v26.04.0 ConfigSpecTest has literal config snippets, negative/positive
  sequential pairs, errors/warnings with locations and messages, and a
  PluginSpecCache plus ConfigSpecVisitor in its helper. These are useful
  diagnostic-case candidates; dropping the visitor would change the test.
  [ConfigSpecTest][lsptest].
- Its LanguageServerErrorCollectorTest constructs mocked phase-aware errors
  and checks collector filtering and object identity. That is an internal
  tooling test, not a language input/output conformance case.
  [Error collector test][lsperror].
- The current tree-sitter-nextflow README says its grammar mirrors the
  official ANTLR grammar and provides ast-grep integration. This may help
  inspect inner NF code. It does not parse the Groovy/Spock host or supply
  the runtime/assertion contract. The browsed mutable `main` source is later
  context, outside the target lock. [Tree-sitter README][treesitter].
- nf-test's project docs offer pipeline/process/workflow/function checks,
  output assertions, and snapshots. It is a pipeline testing framework,
  rather than an importer of Nextflow's internal Spock tests.
  [nf-test project][nftest]; [project docs][nftestdocs].
- Current official training requires nf-test 0.9.3 or later for process
  tests with the strict parser and reports incompatibility in older
  generated harnesses. This reinforces that any adopted wrapper framework
  needs its own version pin and target validation. The training page is
  current context, not pinned 26.04.6 source evidence.
  [Official training][nftraining].

### Inferences

A future pilot should compare original-harness and transformed-case
observations against preserved expectations. The following decomposition
is grounded in the pinned sample, not a suitewide translation estimate.
[Manifest and exact spans][evidence].

| Pilot | Preserve and verify | Main dependency |
| --- | --- | --- |
| P01 | Five inputs, twenty diagnostic predicates | Shared parser and normalization |
| P02 | Invalid and valid params inputs, five predicates | Semantic analysis |
| P03 | Three methods, membership/exclusion/sorted equality | Last-result observer |
| P04 | Three outputs, nine ordered values, three STOPs | Completion observation |
| P05 | Five CPU rows and two negative cases | Resource and diagnostic mapping |
| P06 | Two status expressions; original final status | Shell and resume |
| P07 | Eight named-process log-count checks | Identity/cache instrumentation |
| P08 | Two exact expected-file comparisons | Topics, collectFile, resume |
| P09 | Typed input, byte expressions, final status | Types, optional file, staging |

The execution pilot should do the following before claiming preserved
expectations. These are proposed evidence requirements, not completed work.

1. Pin the original fixture/helper closure and execute the selected original
   cases under Nextflow 26.04.6/v2. For Spock cases, record expanded rows,
   subcases, setup order, exact returned values/diagnostics, and mock use.
   Preserve originals even if their v2 execution fails.
2. Decode each inner input with its actual Groovy literal/helper semantics.
   Record input bytes before/after normalization and original filename.
   Run transformed parser cases against the pinned parser and check all
   original count/location/message predicates, including positive cases.
3. For operator snippets, execute an observer/wrapper against the pinned
   real runtime and compare original predicates. Demonstrate per-output
   association, multiplicity, ordering only where asserted, queue/value
   behavior where mapped, and bounded completion. Record any mock-to-real
   execution change as a separate contract.
4. Run unchanged integration workflows with retained fixture discovery and
   fresh/resume variants. Retain every literal check and record each result
   separately from the original aggregate status. Compare exact expected
   bytes where present. Preserve log/report assertions or retain their
   unmapped status. Newly enforcing each invocation/status/byte check and
   independent engine exit are separate stronger gates, not original passes.
5. Validate the translator/accounting with deliberate omissions or changes:
   lose a sequential subcase, CPU row, negative predicate, terminal sentinel,
   resume invocation, fixture, or expected newline. Each must be detected
   for the missing or changed contract, not merely a generic parse error.
   Add all four F1 controls to detect swallowed fresh failures and distinguish
   original aggregate passes from new per-invocation gates. Mere expression
   retention does not prove equivalent failure propagation or adequacy.
6. Once a wr boundary exists, run both engines against the same independently
   reviewed expectations and retain separate oracle, wr and differential
   results. Record disagreements rather than deriving expected truth from
   Nextflow observations.
   A missing wr runtime binding stays unverified. Do not derive expected
   values from wr's output or award translation from a foundation test pass.

[Source assertions and helper contracts][evidence];
[original assessment's pilot boundaries][assessment].

The latest user candidate is an independently authored engine-neutral suite
covering all upstream behavioral scenarios, correcting identified weaknesses,
using the same independent expectations for real Nextflow and future wr
adapters, then covering documented-language gaps. A complete upstream-unit
inventory retains every internal assertion. Each shared behavioral equivalent
needs review; JVM identity, AST shape and mock interactions remain explicitly
unresolved/internal until a reviewed mapping exists. A portable subset must
not silently replace the requested scope. Preserve upstream, strengthening
and independently documented-gap provenance separately. This hypothesis needs
a minimal executable pilot of manually authored neutral contracts before
choosing exact architecture, automation or spec revisions.
[Internal assertions][asthelper]; [mock boundary][helper]; [original Mix][mix].

Possible routes have different costs and claim boundaries:

- Reuse NF/expected files and retain shell checks behind an invocation adapter.
  This preserves simple byte comparisons but needs reviewed mappings for
  Nextflow-specific flags, logs, and report artifacts. [Checks][hello].
- Export bounded test forms from the Groovy AST into neutral case records.
  This preserves host structure but requires literal/provider/fixture
  semantics and a development JVM export step. [Spock compiler][spocktransform].
- Extract reviewed cases manually first, then automate forms proven by the
  pilot. This limits importer risk at the cost of per-case review work.
  These source samples provide enough variation to test that tradeoff;
  they do not predict whole-suite throughput. [Pilot sources][evidence].
- Retain original Spock as the oracle baseline and project suitable
  assertions into public wr observations. This keeps original evidence but
  cannot label internal-only predicates as wr runtime passes.
  [Internal AST tests][asthelper]; [mock execution][helper].

### Gaps

- Full language-server corpus inventory was not retrieved. Directory/tree
  requests failed, but two release-tagged primary test files and the build
  file were retrieved. No language-server suite count or reuse rate is
  claimed. Exact failures and discovery queries are in [search log][searchlog].
- External repositories were browsed, not downloaded, hashed, installed,
  built, or run. Their sources do not inherit the wr target source lock.
- The pilot must still execute. This report establishes source-backed
  alternatives and dependencies only. It leaves the exact design to the
  caller's independently reviewed synthesis and later spec work.

[arity]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/process-arity.nf
[helper]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/testFixtures/groovy/test/ScriptHelper.groovy
[hello]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/hello.nf/.checks
[assessment]:
  /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-upstream-test-assessment-01.md
[evidence]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-test-translation/pinned-evidence.json
[runner]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh
[validation]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/validation/test.sh#L48-L78
[aritychecks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/process-arity.nf/.checks
[topic]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/topic-channel.nf
[topicchecks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/topic-channel.nf/.checks
[topicexpected]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/topic-channel.nf/.expected
[subchecks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/subworkflow-take.nf/.checks
[nullable]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/nullable-path.nf
[nullablechecks]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.checks
[fixtureconfig]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/nextflow.config
[dsl2]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/testFixtures/groovy/test/Dsl2Spec.groovy#L31-L38
[loader]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/main/groovy/nextflow/script/parser/v2/ScriptLoaderV2.groovy
[mix]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy#L30-L67
[multimap]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/extension/MultiMapOpTest.groovy#L38-L144
[parser]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test/groovy/nextflow/script/parser/ScriptAstBuilderTest.groovy#L30-L183
[utils]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/testFixtures/groovy/test/TestUtils.groovy#L49-L101
[asthelper]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test/groovy/nextflow/script/control/ScriptToGroovyHelperTest.groovy#L36-L84
[workflowmeta]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/script/WorkflowDefTest.groovy#L35-L78
[mocktest]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/script/ProcessEntryHandlerTest.groovy#L40-L60
[cpu]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy#L286-L304
[cpuneg]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy#L668-L686
[testdeps]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/build.gradle#L78-L87
[spockdata]: https://spockframework.org/spock/docs/2.4/data_driven_testing.html
[spockinteraction]:
  https://spockframework.org/spock/docs/2.4/interaction_based_testing.html
[spocktransform]:
  https://github.com/spockframework/spock/blob/spock-2.4/spock-core/src/main/java/org/spockframework/compiler/SpockTransform.java#L26-L71
[adr]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/adr/20250508-strict-syntax-parser.md#L45-L59
[searchlog]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-test-translation/search-log.json
[lsp]: https://github.com/nextflow-io/language-server#releasing
[lspbuild]:
  https://github.com/nextflow-io/language-server/blob/v26.04.0/build.gradle#L34-L56
[lsptest]:
  https://github.com/nextflow-io/language-server/blob/v26.04.0/src/test/groovy/nextflow/lsp/services/config/ConfigSpecTest.groovy
[lsperror]:
  https://github.com/nextflow-io/language-server/blob/v26.04.0/src/test/groovy/nextflow/lsp/compiler/LanguageServerErrorCollectorTest.groovy#L29-L59
[treesitter]:
  https://github.com/nextflow-io/tree-sitter-nextflow/blob/main/README.md
[nftest]: https://github.com/askimed/nf-test
[nftestdocs]: https://github.com/askimed/nf-test/blob/main/docs/index.md
[nftraining]: https://training.seqera.io/latest/side_quests/nf_test/
[shellproof]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-report-fix01/evidence.json
