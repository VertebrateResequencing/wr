# Nextflow research report review 01

FAIL on 2026-10-08. One P2 factual finding concerns the effective shell
harness contract. The report correctly leaves exhaustive language coverage,
validated translations and runtime portability unestablished. Correct the
fresh/resume pass claims before accepting the research synthesis. A bounded
executable research pilot should then precede the exact assurance approach
and any dependent specification revision.

Review owner: `/root/nextflow_research_report_review`. Queue owner: `/root`.
Branch: `nextflowdsl`; worktree: `/home/ubuntu/wr`. Target: Nextflow 26.04.6,
commit `232b60569865e9a4577e48c1955409238359d6ca`, strict parser v2.

## F1: Distinguish shell check expressions from enforced pass conditions

Priority P2. Report lines 122-126 say the arity shell checks require successful
fresh and resumed runs. Lines 155-156 repeat the fresh/resume success contract.
The literal checks exist, but the upstream aggregate result does not enforce
both. This also affects the nullable-path claim of two required exact stdout
comparisons. Its pipeline-status limitation is correctly identified, but
early comparison failures can also disappear from the final script status.

The pinned [runner][runner] invokes `bash -ex .checks` at line 49 and checks
that subprocess's final status at lines 56-67. Both the [arity checks][arity]
and [nullable checks][nullable] execute `set +e` at line 1. This disables the
initial `-e`. Arity's fresh invocation failure makes its line 8 conditional
and `false` fail, but execution continues. A successful resumed invocation
leaves line 16 successful and the script exits zero. Nullable's first failed
`cmp` at line 9 likewise continues. A successful final `cmp` at line 18
returns zero even after earlier failures. Without pipefail, its pipeline
status checks additionally observe `tee` rather than the controlled subject.

Four isolated controls preserve the original `.checks` bytes and the
runner's `bash -ex` launch form. They substitute explicitly controlled shell
subjects for `NXF_RUN`; they execute no Nextflow or wr program. Expected
nullable bytes are copied unchanged from the pinned fixture.

| Control subject behavior | Original checks exit |
| --- | ---: |
| Arity fresh exits 1, resume exits 0 | 0 |
| Arity fresh exits 0, resume exits 1 | 1 |
| Nullable fresh wrong bytes, resume expected bytes, both exit 1 | 0 |
| Nullable fresh expected bytes, resume wrong bytes, both exit 0 | 1 |

The first and third results prove pass propagation defects in the shell
contract. The opposite controls demonstrate that final checks still affect
the aggregate status. Exact copies, subject scripts, stdout, Bash traces,
hashes and results are in [review evidence][evidence]. These controls prove
shell behavior only. They establish no defect in Nextflow's language or
runtime and no whole-suite missing-test claim.

Correction targets:

- Report lines 122-126 and 155-156: say that arity contains two status
  expressions, while an upstream aggregate pass can hide a failed fresh run.
- Report lines 288-319 and 321-335: require a reviewed distinction between
  preserved original aggregate semantics and added per-invocation gates.
  Retain original inputs, both invocations and every check expression.
- `test_translation.md` lines 27-32, 44-49 and 256-287: distinguish observed
  fresh/resume check results from their propagation into the original pass.
  Enforcing each status and byte comparison is added strength, alongside
  the already proposed independent engine-exit assertion.
- `requirement_coverage.md` lines 136-141 and 183-186: qualify success evidence
  by the effective harness outcome. A source predicate is not adequate
  failure-detection evidence when later success can swallow its failure.

Add the corresponding harness control to the pilot's assertion-adequacy
gate. A transformed runner can enforce every intended check, but it must
identify that stricter pass condition as a new contract. Preserving literal
check expressions alone cannot justify an equivalence or adequacy claim.

## Independently supported conclusions

The report and all three notes were read in full. The pinned source passages
were checked directly, rather than accepting the notes' summaries or search
matches as semantic proof. Source identity results are in [evidence][evidence].

- All 23 coverage-manifest files match the lock's SHA-256, byte count and
  Git blob ID. The translation manifest's 41 files, ten original pilot spans
  and ten added spans match their source identities. The requirement record
  has 22 matching spans across 15 files, totaling 22,160 bytes. Independent
  enumeration confirms directory counts 12, 340 and 309, totaling 661 files.
  These checks establish identity and counts, not language coverage.
- `docs/strict-syntax.md:5,13,21` names the language specification, describes
  the strict DSL2 subset and makes v2 the 26.04 default. The accepted ADR's
  lines 155 and 315 declare syntax-description completeness. Neither cited
  passage certifies exhaustive tests. Linked typed semantics and separate
  configuration grammar support the report's scope warning.
- `ScriptParser.g4:97-104` accepts declaration/statement alternatives;
  `ScriptAstBuilder.java:236-240` separately rejects mixed top-level forms.
  Grammar alternative counts therefore cannot discharge that restriction.
- All three Mix methods match the report. The first has six membership
  predicates and one exclusion; its two analytical witnesses satisfy those
  predicates while adding an extra item. The other methods compare sorted
  finite lists. The witness refutes the first predicate group's conservation
  strength, without proving suitewide absence or an executed mutant result.
- Parser tests retain five sequential invalid inputs, a mixed-top-level
  rejection and opposite params outcomes. The parser is `@Shared` and created
  in `setupSpec`. `TestUtils:49-52,86-101` supplies indentation normalization,
  `main.nf`, analysis, syntax-error filtering and location sorting.
- `Dsl2Spec:33-37` resets shared Nextflow state. `ScriptHelper:161-181` runs
  the MockSession and normalizes results. Its lines 340-348 return script
  text and zero status for scriptlets, or call non-scriptlet code directly.
  `ScriptLoaderV2:98-99` captures the last result for testing. A real shell
  wrapper therefore needs a separate reviewed contract.
- MultiMap's selected method has three outputs, nine ordered values and
  three STOP observations. CPU has five table rows with three properties
  each and two separate internal resource-error cases. Those facts do not
  imply that a public observer preserves JVM identity or scheduler requests.
- Topic checks retain two byte comparisons and inherit the runner's `-e`;
  their expected bytes contain both terminal newlines. Subworkflow checks
  assert eight process-name counts and explicitly enable `-e`. Nullable
  enables types, optional file output and `Path?` staging. Its exact expected
  bytes are `empty input\n\n`. Its v2-only ignore-list disposition is correct.
- JaCoCo generation, closure-class exclusion, CI upload paths, parser modes,
  conditional skips and sorted documentation comparisons match the pinned
  build and shell sources. Configuration is not execution evidence.

Primary web checks also support the bounded method claims. [Spock 2.4 data
semantics][spock-data] describe row iterations, iteration setup/cleanup,
shared fields and external/generated providers. [Spock interactions][spock]
retain cardinality and constraints; the [pinned transformation][transform]
runs at semantic analysis. [JaCoCo counters][jacoco] measure implementation
execution, while [PIT][pit] checks selected mutations. [NASA traceability
guidance][nasa] supplies a requirements/verification method, not a Nextflow
certification rule. Adjacent language-server build/version boundaries and
[current nf-test training][training] were verified; they remain separate
version/application evidence. GNU Bash web manual retrieval timed out; the
shell finding has direct controlled execution evidence instead.

The retained search logs state search scopes and retrieval failures. Neither
the report nor the notes derives semantic absence from a keyword miss.
They disclose missing release-commit CI results, numeric coverage, a complete
normative closure and a requirement-to-assertion matrix. These are appropriate
limits of the completed source research, not reasons to invent a coverage
percentage or require an unbounded audit before any pilot.

## Minimal next research contracts

The report's proposed direction is sufficient after F1 is corrected. Run a
bounded research pilot before choosing the exact importer, observer or wr
runtime architecture. Full-language exhaustiveness remains a separate later
claim. A pilot can finish with measured unresolved mappings or failed
adaptations; it need not finish with every candidate translated.

Before execution, record the following in a short reviewed pilot charter:

1. Name the finite original methods, subcases, rows, workflow invocations and
   document ranges. Start with the five-input parser method, all three Mix
   methods, arity and topic. Retain mixed-top-level and params controls.
   Freeze v2, typing flags and explicit document links for this subset;
   unresolved linked requirements remain visible. Whole-language closure
   reconstruction is not a prerequisite for bounded results.
2. Record exact source/fixture hashes, decoded literals, normalization,
   expected values, all source predicates and effective original pass rules.
   Preserve parser sharing/setup, last-result observation and mock execution.
   Keep added conservation, exit, byte and failure-propagation gates separate.
3. Resolve the actual selected dependency closure. A runtime distribution
   suitable for bootstrap NF cases does not establish a Spock/nf-lang harness.
   Record JVM, Groovy 4.0.31, Spock 2.4, test fixtures, transitive artifacts,
   parser selection, working directories, config discovery, shell tools,
   environment conditions, containers and writable fresh/resume state.
   Prerequisite failure produces incomplete evidence, not a synthetic pass.
4. Execute original cases first against their preserved contracts. Capture
   each check result and aggregate status separately. Validate a manual or
   bounded transformed case against the pinned original observations and
   reviewed expectations. Use explicit deadlines and preserve the Mix and
   MultiMap completion/timeout obligations. No general importer is needed.
5. Review source-to-observation changes and test accounting loss with omitted
   subcases, predicates, fixtures, expected newlines and resume invocations.
   Add the F1 controls to distinguish check presence from enforced failure.
   Count original-only, partial, unmapped, pending and strengthened contracts
   separately. Disagreement or an unsupported observer is a research result.
6. Report measured preservation, prerequisites, adaptations, unresolved forms
   and review effort before a route decision. wr differential runs begin only
   when a supported boundary exists. The pilot can return useful pinned-engine
   and transformation evidence while wr execution remains pending.

The remaining candidates have distinct dependencies and can follow that
first group. MultiMap needs per-output association, FIFO reads and terminal
observation. CPU needs closure context, declaration-presence mapping and
internal exception provenance; scheduler evidence is a later public mapping.
Named subworkflows need task identity/cache instrumentation. Nullable paths
need retained typing, optional-file/staging behavior and reviewed shell gates.
The map null-mode control requires separately recorded typing conditions.
Pending typed/runtime policy must not silently turn these cases into passes
or remove them from their original denominator.

## Latest candidate and decision frontier

The latest user proposal is an independently authored engine-neutral suite
that preserves upstream coverage, corrects identified weaknesses, runs
against real Nextflow and future wr, and then adds documented-language gaps.
The report's combined and manual-first routes accommodate this candidate,
but the synthesis should now state it explicitly as a route to investigate.
The new request strengthens the desired candidate; it does not establish
that all upstream assertions already have engine-neutral equivalents.

Keep three provenance groups in that investigation: preserved upstream
behavioral contracts, separately authored corrections/strengthening, and
independently documented requirements missing from the selected upstream
contracts. An independently reviewed expectation feeds two engine adapters.
Nextflow's observed result cannot automatically define expected truth,
especially when a newly strengthened check reveals a possible upstream
defect or documentation disagreement. Record that disagreement for review
rather than weaken the expectation or silently update the baseline.

The phrase "everything upstream covers" needs a checkable denominator. A
complete upstream-unit inventory can retain every internal assertion, while
the shared engine-neutral suite covers reviewed behavioral equivalents.
JVM identities, AST shapes and mock interactions need a reviewed behavioral
equivalence argument or an explicit unresolved/internal disposition. A
public output match alone cannot certify those original assertions preserved.
If the user intends literal preservation of all internal assertions against
both engines, the pilot must test that feasibility before advertising it.
Do not silently replace that requested scope with a portable subset.

Use the pilot to author a few independent neutral contracts manually before
choosing automation. Validate their decoded inputs, observers and expectations
against the original harness and real pinned Nextflow. Add the F1 per-check
failure gates and named Mix conservation contract with separate provenance.
Keep internal CPU and parser-observer questions visible. This tests the
user's candidate without building a general importer or future wr runtime.

The user decision frontier is the bounded pilot's scope and resource needs,
followed by its measured preservation, strengthening and adaptation results.
Choosing an exact assurance design or invoking spec revision before those
results would bypass the report's own evidence gate. Research completion
does not resolve Item 2.1 F11, authorize Item 2.2 or alter accepted Phase 1.

## Title and completion

The H1 has seven words and differs from the chosen report filename title.
This is a consistency note, not a semantic finding. Unslop requires one H1,
which the report supplies. The coordinator's research title rule applies
3-6 words to the chosen filename title, which already complies. Its report
writer guidance asks for an approximately six-word active title. Aligning
the H1 with `Nextflow test coverage research` would remove the discrepancy;
the approximate title guidance alone does not determine this FAIL.

This reviewer wrote only this review and its owned scratch directory. All
controls and identity checks completed; no owned process or delegated work
remains live. No Nextflow/wr execution, DSL execution, build, dependency
download, product change, spec change, commit or push occurred.

[evidence]:
  ../../../.tmp/agent/nextflow-conformance/research-report-review01/results.json
[runner]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh#L38-L67
[arity]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/process-arity.nf/.checks
[nullable]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.checks
[spock-data]: https://spockframework.org/spock/docs/2.4/data_driven_testing.html
[spock]: https://spockframework.org/spock/docs/2.4/interaction_based_testing.html
[transform]: https://github.com/spockframework/spock/blob/spock-2.4/spock-core/src/main/java/org/spockframework/compiler/SpockTransform.java
[jacoco]: https://www.jacoco.org/jacoco/trunk/doc/counters.html
[pit]: https://pitest.org/
[nasa]: https://swehb.nasa.gov/spaces/SWEHBVD/pages/102695427/SWE-052%2B-%2BBidirectional%2BTraceability
[training]: https://training.seqera.io/latest/side_quests/nf_test/
