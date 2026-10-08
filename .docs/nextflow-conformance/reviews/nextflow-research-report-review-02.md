# Nextflow research report review 02

PASS on 2026-10-08. Accept the corrected research synthesis and its evidence
as a basis for a bounded executable research pilot. No remaining actionable
finding was identified. The pilot is an adequate next research step before
choosing the exact assurance approach. This verdict certifies neither an
engine-neutral architecture nor exhaustive coverage or translation.

Review owner: `/root/nextflow_research_report_review02`. Queue owner: `/root`.
Branch: `nextflowdsl`; worktree: `/home/ubuntu/wr`. Target: Nextflow 26.04.6,
commit `232b60569865e9a4577e48c1955409238359d6ca`, strict parser v2.

## Accepted claim and decision boundary

The [report][report] answers the research question at the evidence's scope.
Upstream declares a language specification, but this study establishes no
complete requirement-to-assertion matrix, release-commit execution results,
semantic coverage percentage or whole-suite translation rate. The pinned
strict guide and accepted ADR support the specification claim; neither
certifies exhaustive tests. Linked semantics, configuration grammar, typed
feature flags and Groovy extension mechanisms justify keeping language and
runtime boundaries explicit. JaCoCo configuration and CI lanes establish
intended checks, rather than successful release-commit execution.

The report names the latest candidate as an independently authored
engine-neutral suite covering all upstream behavioral scenarios, correcting
identified weaknesses, applying the same independently reviewed expectations
to real Nextflow and future wr, then filling documented-language gaps.
It retains upstream, strengthening and documented-gap provenance separately.
Every internal assertion remains in the original inventory obligation.
JVM identity, AST shape and mock interactions need reviewed equivalence or
an explicit unresolved/internal disposition. An output match cannot
silently discharge those assertions. A portable subset cannot replace the
requested scope. Observed Nextflow output is evidence to compare with
reviewed expectations; disagreements remain review subjects.

These distinctions preserve the user's objective while keeping the candidate
a hypothesis. Research acceptance does not authorize specification revision
or further product implementation. Item 2.1 F11 remains an assertion-strength
finding: production rejects the malformed UTF-8 inputs, while a guard-removal
fault escapes the existing tests. This review neither resolves F11 nor
changes its deferred status or authorizes Item 2.2.
([Current Item 2.1 review][schema-review])

## F1 is corrected and independently reproduced

The effective contract follows the pinned runner and checks. The runner
launches `bash -ex .checks` and reads the final subprocess status. Arity and
nullable both execute `set +e`. Arity retains two literal status expressions,
but a failed fresh expression can precede a successful resume expression
and aggregate zero. Nullable retains two pipeline-status and two byte
expressions; its fresh comparison failure can disappear before a successful
final comparison. Without pipefail, pipeline status observes `tee`.
Topic retains two enforced byte comparisons under inherited errexit.
([Runner][runner], [arity checks][arity], [nullable checks][nullable],
[topic checks][topic])

Fresh independently authored Bash subjects ran byte-identical pinned checks
under the original launch form. Nullable expected bytes were copied directly
and verified as `empty input\n\n`, including both terminal newlines.
All subjects and original-check subprocesses completed within ten-second
deadlines. Independent invocation-exit and applicable byte gates produced
these results:

| Controlled behavior | Original exit | Added gates |
| --- | ---: | --- |
| Arity fresh 1, resume 0 | 0 | Reject |
| Arity fresh 0, resume 1 | 1 | Reject |
| Nullable fresh wrong/resume expected bytes, both exits 1 | 0 | Reject |
| Nullable fresh expected/resume wrong bytes, both exits 0 | 1 | Reject |
| Arity both exits 0 | 0 | Accept |
| Nullable expected bytes twice, both exits 0 | 0 | Accept |

[Independent subjects, traces and results][evidence] prove shell-harness
behavior only. No Nextflow or wr program ran. The valid subjects show that
the added gates accept their stated success conditions; the bad subjects
show the distinction between original aggregate outcomes and stronger gates.

The correction reaches the report and both affected notes. They preserve
literal expressions and original aggregate semantics, require per-check
results and propagation evidence, and label enforced invocation/status/byte
checks and independent engine exits as newly authored contracts. The four
F1 controls now enter assertion-adequacy review. The report explicitly
corrects historical P06 and supplements P09's pipeline warning. The original
assessment matches its pre-fix baseline hash and remains historical evidence.
([Translation note][translation], [requirement note][requirements],
[historical assessment][assessment])

## Evidence supports the remaining synthesis

The report, all three notes, prior review and fix record were read in full.
Primary pinned passages were read directly; manifest success and keyword
matches were not substituted for semantic review. Independent checks found:

- All 23 coverage-manifest records match locked SHA-256, byte count and Git
  blob identity. All 41 translation files, ten original spans and ten added
  spans match; all 22 requirement spans match. Byte spans also match their
  declared line ranges. These establish provenance rather than coverage.
- Independent directory enumeration and identity checks confirm 12, 340
  and 309 files, totaling 661. Recursive NF counts are 115 integration,
  91 legacy and 97 documentation-snippet files. None is an executed-case
  or semantic coverage count.
- The grammar admits declaration/statement alternatives, while the AST
  builder rejects mixed top-level forms. The five-input parser method has
  twenty diagnostic predicates; params has opposite outcomes. Shared parser
  state, literal decoding, indentation normalization, `main.nf`, analysis,
  error filtering and location sorting remain preservation obligations.
- All three Mix methods match the report. Fresh pure-data evaluation confirms
  that both seven-item witnesses satisfy the first method's seven predicates.
  The other two use sorted-list equality for different inputs. This proves
  a named predicate-strength limit, without proving whole-suite absence or
  an executed Groovy mutation. Permitted ordering does not require exhibited
  nondeterminism, and the documentation's numeric tokens are strings.
- ScriptHelper's mock scriptlets return script text with zero status;
  Dsl2Spec resets shared state and the v2 loader captures the last result.
  CLI replacement needs review of this observation change. MultiMap's
  ordered values, output association, STOPs and JVM identity assertions remain
  distinct obligations. CPU has five rows with three predicates each and
  two internal resource-error cases, without scheduler-boundary proof.
- Arity's documentation adds invalid-count and scalar/list requirements.
  Topic's expected bytes, named-process fresh/cache log counts, hello's
  instrumentation, nullable typing/staging and map's typing-dependent null
  behavior support the report's varied observer and mode concerns.

Primary web checks support the bounded external-method claims. Spock 2.4
specifies row iterations, setup/cleanup, shared-state exceptions and external
providers; its interaction contract and semantic-analysis transformation
support the extraction caveats. JaCoCo counters and PIT distinguish code
execution from fault detection. NASA provides a traceability method rather
than Nextflow certification. The language-server v26.04.0 build and tagged
README confirm separate patch policy and dependencies. Current nf-test docs
and training support the adjacent-framework discussion; they remain mutable
context requiring their own applicability review.
([Spock data][spock-data], [interactions][spock-interactions],
[transformation][spock-transform], [JaCoCo][jacoco], [PIT][pit],
[NASA][nasa], [language-server build][lsp-build], [release policy][lsp-policy],
[nf-test docs][nf-test], [training][training])

The retained search logs disclose bounded queries, failed retrievals,
unavailable release-commit results and incomplete external inventory. The
notes derive no semantic absence from missing keyword matches. Their bounds
are adequate for this researched synthesis, with the missing normative
closure and full assertion mapping still explicit.
([Coverage search log][coverage-search], [translation search log][search])

## The bounded pilot is adequate next research

Accept the proposed first group as the next investigation: the five-input
parser method, all three Mix methods, fresh/resume arity and exact topic-file
comparisons, with mixed-top-level and opposite params controls. A short
independently reviewed charter must fix original unit/document ranges, v2,
typing flags, unresolved document links and preservation obligations before
execution. Full-language closure is a later full-language prerequisite,
rather than a prerequisite for this finite pilot.

Manual neutral contracts can test the candidate before a general importer.
Resolve the selected original dependency/fixture closure, retain decoded
inputs and all original predicates/pass rules, then execute originals first.
Review wrappers, observers and strengthened expectations independently.
Capture per-check and aggregate results, diagnostics, artifacts, completion,
prerequisite status and fresh/resume variants under explicit deadlines.
Retain original failures and documentation disagreements. The added F1 gates
and any Mix conservation requirement keep separate provenance.

Loss controls must detect omitted subcases, rows, predicates, terminal
observations, fixtures, expected newlines and resume invocations. The later
MultiMap, CPU, subworkflow, nullable and map controls expose remaining
observer/policy questions. Their unresolved status stays visible against
original denominators. Pilot results must report preservation, adaptation,
dependencies, disagreements and review effort, including failed mappings.
A future wr adapter uses the same reviewed expectations when its supported
boundary exists; pending wr execution remains incomplete evidence.

The report supplies these gates without claiming the pilot already ran or
choosing its exact architecture. Measured bounded results can inform the
later assurance approach. The present PASS accepts that research direction,
with all-upstream coverage and documented gaps still in the eventual scope.

## Document and completion checks

The report H1 now matches `Nextflow test coverage research`. The report,
three notes and this review pass checks for one H1, heading order, 80-column
prose, named fences, no placeholders, trailing whitespace, consecutive blank
lines or prohibited typography. Reference citations and local targets
resolve. Pinned citation files/directories and line bounds match retained
locked source. URL destinations and table rows are exempt from prose width.

Only this review and owned scratch were written. Every pre-review baseline
file under `.docs/nextflow-conformance/` and `nextflowconformance/` retains
its hash. All owned checks completed; no tool session, process or delegated
work remains live. No DSL execution, build, dependency download, production,
specification, phase or status change, commit or push occurred.

[report]: </home/ubuntu/wr/.docs/nextflow-conformance/reports/Nextflow test coverage research.md>
[translation]: </home/ubuntu/wr/.docs/nextflow-conformance/research_notes/Nextflow test coverage research/test_translation.md>
[requirements]: </home/ubuntu/wr/.docs/nextflow-conformance/research_notes/Nextflow test coverage research/requirement_coverage.md>
[assessment]: /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-upstream-test-assessment-01.md
[schema-review]: /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-phase2-schema-review-06.md
[evidence]: /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-report-review02/results.json
[runner]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh#L38-L67
[arity]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/process-arity.nf/.checks
[nullable]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.checks
[topic]: https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/topic-channel.nf/.checks
[spock-data]: https://spockframework.org/spock/docs/2.4/data_driven_testing.html
[spock-interactions]: https://spockframework.org/spock/docs/2.4/interaction_based_testing.html
[spock-transform]: https://github.com/spockframework/spock/blob/spock-2.4/spock-core/src/main/java/org/spockframework/compiler/SpockTransform.java
[jacoco]: https://www.jacoco.org/jacoco/trunk/doc/counters.html
[pit]: https://pitest.org/
[nasa]: https://swehb.nasa.gov/spaces/SWEHBVD/pages/102695427/SWE-052%2B-%2BBidirectional%2BTraceability
[lsp-build]: https://github.com/nextflow-io/language-server/blob/v26.04.0/build.gradle#L34-L56
[lsp-policy]: https://github.com/nextflow-io/language-server/blob/v26.04.0/README.md#releasing
[nf-test]: https://github.com/askimed/nf-test/blob/main/docs/index.md
[training]: https://training.seqera.io/latest/side_quests/nf_test/
[coverage-search]: /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-coverage-evidence/search-log.json
[search]: /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-test-translation/search-log.json
