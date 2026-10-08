# Nextflow research report fix 01

Completed on 2026-10-08 for review, with research acceptance pending.
Owner: `/root/nextflow_research_report_fix01`; queue owner: `/root`.
Branch: `nextflowdsl`; worktree: `/home/ubuntu/wr`. Target: Nextflow 26.04.6,
commit `232b60569865e9a4577e48c1955409238359d6ca`, strict parser v2.

## F1 distinguishes predicates from effective shell passes

The synthesis and two notes now distinguish literal check expressions,
effective original aggregate status and deliberately stronger per-invocation
gates. The original runner launches `bash -ex .checks` and records its final
status. Arity and nullable each disable inherited errexit with `set +e`.
Arity's failed fresh status expression can precede successful resume and
aggregate exit zero. Nullable's first byte-comparison failure can precede a
successful final comparison and aggregate exit zero; without pipefail,
its pipeline-status expression also observes `tee`, not engine exit.
These conclusions follow direct inspection of pinned runner/check bytes and
isolated controls, rather than a completed research summary.
([Runner][runner], [arity][arity], [nullable][nullable])

The literal expressions remain `[[ $? == 0 ]] || false` and, for nullable,
`cmp .expected .stdout || false`. Both invocations and every expression
retain their own preservation obligation. Independently capturing engine
exit and enforcing every invocation/status/byte check are added contracts.
The pilot now requires separate check results and original aggregate status,
plus the F1 failure-propagation controls in assertion-adequacy review.

The immutable initial assessment's P06 fresh/resume success statement now
has an explicit contextual correction in the synthesis. P09's pipeline
warning remains correct, but its omission of swallowed early comparison
failure is also identified there. Historical assessment bytes remain
unchanged; its residual claim does not govern the corrected synthesis.
([Initial assessment][assessment], [review F1][review])

## Controlled proofs reproduce failure swallowing and accept valid subjects

All controls use byte-identical pinned `.checks`, copied nullable expected
bytes and the runner's `bash -ex .checks` launch form. `NXF_RUN` names an
explicit controlled Bash subject. All subprocesses completed within their
ten-second deadlines. The deliberately stronger gate evaluation independently
captures each subject exit and compares applicable stdout bytes for fresh
and resume. This is harness-control proof only, not DSL observation or a
validated translation. No Nextflow or wr program ran.
([Proof script and retained results][evidence])

| Controlled subject | Original exit | Stronger gates |
| --- | ---: | --- |
| Arity fresh 1, resume 0 | 0 | Reject |
| Arity fresh 0, resume 1 | 1 | Reject |
| Nullable fresh wrong/resume expected bytes, both exits 1 | 0 | Reject |
| Nullable fresh expected/resume wrong bytes, both exits 0 | 1 | Reject |
| Arity fresh 0, resume 0 | 0 | Accept |
| Nullable expected bytes twice, both exits 0 | 0 | Accept |

The first four original outcomes match the reviewer controls. The paired
valid subjects establish that the new gates accept their intended success
conditions. Nullable expected bytes are exactly `empty input\n\n`, including
two terminal newlines. Inputs, traces, output bytes, original checks and
per-invocation gate results are retained in owned scratch.

## The latest candidate remains a hypothesis for an executable pilot

The report explicitly names the user's candidate: an independently authored
engine-neutral suite covering all upstream behavioral scenarios, correcting
identified weaknesses, using the same independently reviewed expectations
for real Nextflow and future wr adapters, then filling documented-language
gaps. It separates upstream behavioral provenance, new strengthening and
independently documented-gap contracts.

Every internal assertion remains in the upstream inventory obligation. Shared
behavioral equivalence needs review; JVM identity, AST shape and mock
interactions require an equivalence argument or explicit unresolved/internal
disposition. A portable subset cannot silently replace the requested scope.
Nextflow observations do not automatically define expected truth. Conflicts
with independently reviewed expectations or documentation remain recorded
for review.

The next evidence gate is a short reviewed charter and a minimal executable
pilot of manually authored neutral contracts. A complete language closure is
needed for a later full-language claim; selected document ranges and visible
unresolved links suffice for bounded pilot results. Original cases run first,
with actual dependency/fixture closure, modes, explicit deadlines and retained
completion obligations. The report chooses no general importer, whole-language
coverage claim, typed-language/JVM policy or exact runtime architecture.
It does not revise the specification, resolve Item 2.1 F11 or authorize 2.2.

## Identity, preservation and document checks

Before edits, owned scratch retained complete baseline copies of all 220
files then present under `.docs/nextflow-conformance/` and
`nextflowconformance/`, plus SHA-256 and byte counts for 972 tracked or
branch-owned files. The only changed baseline files are the synthesis,
`test_translation.md` and `requirement_coverage.md`. This fix record is the
only new branch-owned document outside owned scratch. Coverage evidence,
the initial assessment, production, spec, phases and metadata retain their
baseline hashes. Exact diffs, final hashes, preservation results and citation
checks are retained in [validation results][validation].

Independent source identity and span checks passed for all 23 coverage
manifest records, 61 translation records comprising 41 files and ten each
original/added spans, and 22 requirement spans. Six direct shell source and
fixture identities match locked SHA-256, byte counts and Git blob IDs.
These checks prove source identity, not semantic coverage.
([Source and control evidence][evidence])

All four edited or created documents passed Markdown and citation checks:
one H1, sequential heading levels, 80-column prose,
no trailing whitespace or consecutive blank lines, named fence languages,
no placeholders and resolving reference citations/local targets. URL/path
reference destinations and table rows are exempt from prose wrapping.
The synthesis H1 now matches `Nextflow test coverage research`. All pinned
GitHub citations in the changed documents resolve to retained source files
or directories; cited file hashes and line bounds are checked against the
lock. Citation identity checks establish provenance, not acceptance of an
assertion's semantics.

No build, download, Nextflow/wr runtime, DSL execution, production/spec/phase
change, commit or push occurred. No owned tool session, process or delegated
work remains live. Independent review must judge the corrected synthesis;
this fix record does not mark the research accepted.

[runner]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/run.sh#L38-L67
[arity]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/process-arity.nf/.checks
[nullable]:
  https://github.com/nextflow-io/nextflow/blob/232b60569865e9a4577e48c1955409238359d6ca/tests/checks/nullable-path.nf/.checks
[assessment]:
  /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-upstream-test-assessment-01.md
[review]:
  /home/ubuntu/wr/.docs/nextflow-conformance/reviews/nextflow-research-report-review-01.md
[evidence]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-report-fix01/evidence.json
[validation]:
  /home/ubuntu/wr/.tmp/agent/nextflow-conformance/research-report-fix01/validation.json
