# Requirement-to-test coverage of Nextflow's documented strict language

## What evidence could establish or refute exhaustive coverage?

### Takeaway

A complete, independently reviewed mapping can establish coverage of a
declared finite set of documented facets. Code execution percentages,
grammar coverage and a passing upstream suite cannot establish that mapping
or prove agreement on every possible program.

### Cited Findings

- NASA's traceability guidance connects requirements with verification and
  recommends unique identities, source provenance, maintenance and review.
  Relationships may be many-to-many. This is an available engineering
  method, not a Nextflow certification requirement. [NASA SWE-052][nasa]
- JaCoCo measures bytecode execution, branches, lines and related counters.
  A method can count as executed after one instruction; its branch metric
  excludes exception handling. The denominator is implementation code,
  rather than documented behaviors. [JaCoCo counters][jacoco]
- PIT seeds code changes and checks whether tests detect them. Its official
  explanation distinguishes executing code from detecting wrong behavior.
  Mutation results concern the chosen changes and tests. [PIT][pit]
- Pinned `ScriptParser.g4:96-106` permits a sequence of declarations or
  statements. `ScriptAstBuilder.java:215-241` separately rejects mixing
  declarations with ordinary statements. This concrete contextual check
  is outside simply visiting each grammar alternative. [Grammar][grammar];
  [AST builder][builder]
- The documented strict-language section contains allowed declaration
  categories, standalone snippets, implicit workflow treatment and a
  restriction on mixing declarations with statements. The migration guide
  explicitly refers to the separate language specification.
  [Pinned strict-syntax guide][strict]

### Inferences

The following is a proposed research method, not a chosen wr design.

1. Freeze the normative corpus and mode before counting. Record the pinned
   documentation, grammar, feature flags, linked reference pages, config
   language and runtime boundaries. Keep unresolved boundaries visible.
   A documentation example or referenced page must retain its provenance
   even if its test is absent. A directory selection alone does not define
   every documented-language requirement.
2. Have one author decompose original source into independently meaningful
   facets and a separate reviewer reconstruct the decomposition. Preserve
   defaults, permitted and rejected forms, overloads, boundaries, warnings,
   examples, feature modes and interactions. Review the specific reason for
   prose classified as nonrequirement; unknown text remains unaccounted.
3. Independently inventory test units and actual assertions. Map each facet
   to a test's input conditions and the predicate capable of detecting its
   violation. Review failure propagation into the effective harness result;
   a later success can swallow a failed predicate. A matching name, executed
   line or parsed construct provides a candidate link, not a semantic link.
4. Review both directions. Every required facet needs an adequate contract;
   every test assertion needs a disposition and provenance. Tests can
   introduce implementation-derived expectations absent from the reviewed
   documentation. Label those separately rather than promoting them to
   language requirements without review.
5. Keep accounting and execution separate. Publish counts for required
   facets, adequate reviewed contracts, partial contracts, no contracts,
   missing bindings, missing prerequisites and fresh execution results.
   A reviewed contract with no executable observation is incomplete runtime
   evidence. An exclusion reduces the declared scope; it cannot establish
   exhaustive coverage of the original documented language.
6. Challenge important links with named wrong behaviors. Record whether the
   mapped predicates would detect the wrong value, missing side effect,
   wrong diagnostic, missing closure or lost invocation. Actual mutation
   runs can strengthen this later. A source-level witness already refutes
   an overstatement about a particular predicate's precision.

Define `R` as the independently accepted required facets and `A(r)` as
contracts reviewed as adequate for facet `r` under their recorded modes and
input classes. Complete contract accounting requires every `r` in `R` to
have a nonempty `A(r)` and no unresolved source decomposition. A separate
execution claim requires every required executable contract to have fresh
results under the pinned conditions. Neither equation proves unlisted
interactions or all programs correct.

Refutation needs a stated claim and evidence at that claim's scope. One
unmapped required facet disproves complete accounting for the reviewed
denominator. A counterexample accepted by a mapped contract disproves that
contract's purported detection power. To assert an entire upstream suite
lacks a test, inspect its complete relevant test and helper closure; failed
keyword searches are insufficient.

### Gaps

- This research has not established the complete normative document
  closure, facet denominator or a whole-suite traceability matrix. It
  therefore supplies no whole-language coverage percentage.
- No Nextflow/wr execution, coverage instrumentation, mutation run,
  Java/Groovy build or dependency download was performed. Predicate
  analysis is not DSL execution evidence; the later isolated Bash controls
  prove harness behavior only.
- A finite mapping does not settle whether its input partitions represent
  every relevant interaction. Claims about generated domains need an
  independent property/oracle and explicit bounds, not just a link count.

## What does the bounded pinned-source examination actually show?

### Takeaway

The inspected source demonstrates subcase counting and assertion-strength
gaps in specific candidate contracts. It does not demonstrate that Nextflow's
complete upstream suite omits the corresponding behaviors.

### Cited Findings

- Research target is commit
  `232b60569865e9a4577e48c1955409238359d6ca` for Nextflow 26.04.6. Fifteen
  inspected files were independently checked against the current source
  lock for byte count, SHA-256 and Git blob ID. Twenty-two exact evidence
  spans contain 22,160 bytes. Identities, bounds and predicate witnesses
  are recorded in the local [evidence record][evidence].
- `ScriptAstBuilderTest:43-136` has five sequential input/error subcases in
  one method. Lines 138-153 contain a single mixed-top-level rejection;
  lines 155-183 have rejecting and accepting params-block subcases.
  `TestUtils` strips indentation, parses, analyzes, filters syntax errors
  and orders them by location. Those are parts of the test contract.
  [Parser tests][parser-test]; [TestUtils][testutils]
- All three `MixOpTest` methods were read. The first contains six membership
  predicates and one absent-item predicate. The remaining two compare
  sorted results against finite lists for different inputs. The file has
  a five-second timeout. [Pinned MixOpTest][mix-test]
- The pinned mix example uses string-valued numeric tokens, chained mixing
  and a value channel. Its prose permits different output orders. The first
  upstream mix input instead uses numeric values and three-way mixing.
  Preserve those type/input distinctions. [Operator section][operator];
  [Example input][mix-input]; [Example output][mix-output];
  [MixOpTest][mix-test]
- `TaskConfigTest:286-304` has five table rows and three checked properties
  per row. Lines 668-686 contain two negative-resource exception cases.
  These are internal TaskConfig assertions. The inspected process docs
  describe requested task CPUs and environment resource limits.
  [TaskConfig tests][cpu-test]; [Process documentation][process-doc]
- The arity workflow supplies valid one-file, two-file and open-range
  instances. Its `.checks` contains fresh and resume status expressions,
  starts with `set +e` and can return zero after a failed fresh expression
  followed by a successful final resume expression. The runner checks the
  subprocess's final status. The docs also require invalid-count failure
  and distinguish scalar from list output; the expressions assert neither.
  [Workflow][arity-input]; [checks][arity-check]; [runner][runner];
  [arity documentation][process-doc]
- Four pinned-byte controlled Bash cases reproduce original exits 0, 1, 0,
  1. They demonstrate swallowed arity fresh failure and nullable fresh byte
  failure, with opposite final-failure controls. Independent invocation
  exit/byte gates reject all four failing controls; paired valid subjects
  with both exits zero and exact applicable bytes pass originals and new
  gates. These are harness-control proofs only, not DSL observations or
  evidence of a Nextflow runtime defect.
  [Control inputs, traces and results][shell-proof]
- OperatorImplTest lines 203-251 exercise ordinary mapping, a value channel,
  tuple expansion and a skip token. The inspected map note distinguishes
  null emission by static-typing mode. These selected methods are not
  demonstrations of both null modes. [OperatorImpl tests][map-test];
  [Map documentation][operator]
- The pinned Gradle dependency is Spock `2.4-groovy-4.0`. Spock 2.4 documents
  one iteration per table row, iteration isolation and shared-state
  exceptions, external/generated data providers, and reporting modes that
  can aggregate iterations. [Pinned dependency][spock-dep];
  [Spock 2.4 data-driven testing][spock-data]

### Inferences

Analytical witnesses for the first mix method are
`[1,2,3,'a','b','z',1]` and `[1,2,3,'a','b','z','d']`. Both satisfy its seven
explicit predicates while differing from the six-item input multiset.
This was checked with a small pure-data predicate evaluation retained in
[predicate-witnesses.json][witnesses]. It was not a Groovy execution or an
injected mutant. A hypothetical defect conditional on the three-source
input could leave the other methods unaffected. Thus the three original
methods alone do not establish exact output conservation for that input.
Whether conservation should be a formal facet is an interpretation to review;
preserve upstream predicates and add any stronger requirement separately.

An allowed-order statement does not require a runtime to exhibit multiple
orders. A conformance observer must accept permitted permutations without
requiring nondeterminism or losing type and multiplicity information.

The mixed-top-level parser case is a correct positive link to one documented
rejection under a particular input. It does not establish every declaration
category, implicit-snippet runtime behavior or module-side-effect rationale
in the inspected section. Grammar alternative enumeration can complement
these contracts but cannot replace contextual acceptance/error assertions.

The CPU getters and resource-limit rows are useful source contracts, but
mapping them to the documentation's actual task-resource request would need
an observation at the relevant execution boundary. A successful method does
not itself show what a scheduler receives. Some rows express internal/default
contracts not stated in the inspected documentation spans; retain that
origin distinction.

Arity success-case evidence needs both a real invocation and recorded
per-check results; the original aggregate pass alone cannot establish fresh
success. Preserve both original status expressions and their aggregate rule.
Enforcing each invocation's status, and adding count-failure, scalar/list or
output-content assertions, creates separate stronger contracts. Nullable's
original pipeline statuses and byte expressions likewise need recorded
propagation; early comparison failure can disappear, and `tee` status is
not engine exit. Keep newly enforced status/byte/engine-exit gates separate.
These findings do not assert that the entire upstream suite lacks those
checks. [Original shell contracts][arity-check]; [nullable][nullable-check];
[runner][runner]; [controlled proofs][shell-proof]

Static-mode-dependent map behavior is a concrete reason to include feature
conditions in each link. Calling a facet covered because a `map` method
runs would miss that distinction. Coverage elsewhere remains unexamined.

### Gaps

- Bounds are the cited spans, all of MixOpTest and the two tiny mix example
  files. Searches outside those bounds located candidates only. They did
  not certify absence or completeness in modules, plugins, tests-v1,
  integration suites, documentation snippets or configuration tests.
- Only selected OperatorImpl and TaskConfig methods were analyzed. No
  assertion is made about all tests for null mapping, multiplicity, arity,
  CPU scheduling or strict-parser declaration combinations.
- Parser-mode execution, helper closure, conditional ignores, mock behavior,
  environment prerequisites and actual table expansion remain execution
  work. The initial assessment supplies related candidates, not a completed
  independent coverage audit.
- No newer Nextflow documentation was used as a substitute for pinned text.
  JaCoCo/PIT and NASA pages were retrieved on 2026-10-08 as method references;
  they do not describe Nextflow 26.04.6 coverage results. Spock 2.4 matches
  the inspected dependency. The earlier 2.3 search result was superseded.

## How should a bounded pilot preserve tests and audit its mapping?

### Takeaway

A defensible pilot would reconstruct both denominators independently, retain
every upstream predicate and review each proposed mapping or adaptation.
Its outcome would be evidence about these bounded forms, not a general
translator or a final wr implementation approach.

### Cited Findings

- Spock supports assertion-bearing interactions with call cardinalities and
  argument constraints, as well as ordinary state assertions. Deleting
  an interaction can lose a test obligation. [Spock interactions][spock-mock]
- Data providers may use outside data or generation, so method source alone
  may not enumerate executable iterations. Table rows, iteration data and
  state-sharing conditions need separate accounting. [Spock data][spock-data]
- Source contracts available for a small pilot include complete mix methods,
  a strict parser rejection, five CPU table rows with two error cases, and
  fresh/resume arity invocations. Their inspected identities are retained
  in [evidence.json][evidence], with links to the pinned upstream source.

### Inferences

Proposed research pilot, pending independent review:

| Pilot | Document bounds | Original test unit bounds |
| --- | --- | --- |
| Strict declarations | strict-syntax.md:78-123 | parser test:138-153 |
| Mix | operator.md:826-852 plus two snippets | all MixOpTest methods |
| CPU request/limits | process.md:602-622,1437-1474 | CPU rows plus errors |
| Arity diagnostic/control | process.md:155-177,207-223 | workflow + .checks |

The wider five-input parser method can be a decomposition control; the
two params cases can check preservation of opposite outcomes. Selected map
tests and its flag-sensitive note can be a mode-accounting control. These
controls do not automatically become evidence for unrelated pilot facets.

The pilot should answer the following questions before an importer or wr
runtime route is selected:

1. Can independent authors reconstruct every document clause, snippet,
   warning and example in the chosen ranges, including unresolved external
   references? Give facets exact source spans, hashes, mode predicates and
   explicit boundaries. Label candidate interpretation and ambiguity before
   promoting a facet to a reviewed requirement.
2. Can independent reviewers reconstruct all test children? Give each
   method, sequential input, table row, assertion, invocation variant and
   fixture a source identity. Retain hierarchy and shared setup. CPU has
   five finite rows, not one case; its three assertions per row remain
   associated. Sequential parser cases may share state, unlike isolated
   table iterations. Generated providers require their expression, original
   input closure and verified expansion; unexpanded providers remain pending.
3. Which exact original predicates support which facets? Use link records
   containing source facet, input/flag conditions, assertion identity,
   observation boundary, rationale, reviewer and adequacy verdict. Supported
   domains can be partial. For example, the first mix membership predicates
   cannot discharge a stronger conservation contract. A workflow-rejection
   case cannot discharge every declaration/snippet facet in its source page.
4. Can all assertions survive adaptation? Keep original input and assertion
   bytes plus decoded literals, helper/fixture hashes and invocation context.
   Record source-to-observation transformations separately. Preserve exact
   exception/location assertions, channel completion, JVM-type assertions,
   mock interactions and resumed invocations. A predicate with no equivalent
   supported observation remains pending or an explicitly reviewed scoped
   exclusion. It cannot silently disappear from a translated method.
5. Can added strength remain distinguishable? Preserve literal predicates
   and original aggregate pass semantics. Give per-invocation failure gates,
   independent engine exits, enforced byte checks, new properties and extra
   error/boundary cases separate provenance. Passing added gates does not
   retroactively prove upstream enforcement. Add the four F1 controls to
   assertion-adequacy review; literal predicate presence cannot establish
   failure detection when later success can swallow it. [Proof][shell-proof]
6. Can reviewers detect deliberate accounting loss? Remove one document
   clause, parser subcase, CPU row, negative assertion, expected file or
   resume invocation from isolated research copies. Alter a feature flag,
   literal type or mapping rationale. Every loss must fail the appropriate
   independent reconstruction or adequacy check. This tests the proposed
   accounting process, without changing upstream production files.
7. Can later execution validate the contract instead of inventing it? Compare
   the pinned engine's observations to preserved expectations first. Review
   any wrapper/helper replacement against the original test. When a second
   engine exists, compare both against the same independently reviewed
   expectations as well as each other. Record discrepancies with docs or
   strengthened expectations; Nextflow observations are not automatically
   expected truth. Execution should capture exits, output artifacts,
   diagnostics, completion and prerequisite status. A skip remains incomplete
   evidence.

The latest user candidate is an independently authored engine-neutral suite
covering all upstream behavioral scenarios, correcting identified weaknesses,
using the same independent expectations for real Nextflow and future wr
adapters, then filling documented-language gaps. Retain every internal
assertion in the complete upstream inventory. Review each shared behavioral
equivalence; keep unsupported JVM identity, AST shape or mock interactions
explicitly unresolved/internal rather than silently narrowing to a portable
subset. Separate upstream provenance, new strengthening and independently
documented-gap contracts. This candidate remains a hypothesis pending a
minimal executable pilot, before exact architecture or spec revision.
[Interactions][spock-mock]; [original Mix][mix-test];
[shell proof][shell-proof].

Report document facets and upstream test units in separate denominators.
For each, show total, interpreted, independently reviewed, partial, missing,
excluded and unclassified counts. Show assertion-retention and reviewed
transformation counts separately from actual oracle or differential results.
Count exclusions visibly against the original source scope; any reduced
profile claim must name its exclusions and cannot become full-language
coverage by subtraction.

A plausible successful pilot establishes independently checked source and
unit accounting, assertion preservation, adequate links for a named finite
subset, and fresh execution for those contracts when authorized. A useful
unsuccessful pilot supplies a precisely located uncovered facet, unsupported
assertion form, ambiguous rule or failed transformation. Either outcome
informs route selection without presupposing an exact design.

### Gaps

- This pilot is proposed, not executed or independently accepted. No
  adaptation, completeness percentage or universal guarantee is awarded.
- A reviewer must still judge the documentation's intended observable
  behavior, particularly implicit runtime effects and scheduler boundaries.
  Mechanical traceability cannot validate an incorrect semantic link.
- A full-language study would need additional document families, module and
  plugin suites, mode-specific tests and interaction analysis. The proposed
  sample cannot estimate their translation rate or implementation cost.

[nasa]:
  https://swehb.nasa.gov/spaces/SWEHBVD/pages/102695427/SWE-052%2B-%2BBidirectional%2BTraceability
[jacoco]: https://www.jacoco.org/jacoco/trunk/doc/counters.html
[pit]: https://pitest.org/
[spock-data]: https://spockframework.org/spock/docs/2.4/data_driven_testing.html
[spock-mock]:
  https://spockframework.org/spock/docs/2.4/interaction_based_testing.html
[grammar]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/main/antlr/ScriptParser.g4#L96-L106
[builder]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/main/java/nextflow/script/parser/ScriptAstBuilder.java#L209-L256
[strict]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/strict-syntax.md#L78-L123
[parser-test]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/test/groovy/nextflow/script/parser/ScriptAstBuilderTest.groovy#L43-L183
[testutils]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/src/testFixtures/groovy/test/TestUtils.groovy#L40-L120
[mix-test]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy#L30-L67
[operator]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/operator.md
[mix-input]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/snippets/mix.nf
[mix-output]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/snippets/mix.out
[cpu-test]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy
[process-doc]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/docs/reference/process.md
[arity-input]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/process-arity.nf
[arity-check]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/process-arity.nf/.checks
[map-test]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/src/test/groovy/nextflow/extension/OperatorImplTest.groovy#L203-L251
[spock-dep]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/build.gradle#L80-L86
[evidence]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-requirement-coverage/evidence.json
[witnesses]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-requirement-coverage/predicate-witnesses.json
[runner]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh#L38-L67
[nullable-check]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.checks
[shell-proof]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-report-fix01/evidence.json
