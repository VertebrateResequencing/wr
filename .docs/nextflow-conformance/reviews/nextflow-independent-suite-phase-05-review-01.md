# Independent suite Phase 5 review 01

Verdict: FIXED.

Reviewer: `/root/nextflow_suite_phase05_review01`. Workflow owner: `/root`.
Branch: `nextflowdsl`. Worktree: `/home/ubuntu/wr`. Review date: 2026-10-08.

This review fixes plan defects against the accepted spec and current Phase 4
and Item 3.6. It awards no implementation, engine execution, foundation or
runtime acceptance. All eleven item pairs remain unchecked.

## Reviewed identities

SHA-256 inputs and output:

- Accepted `spec.md`:
  `57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c`.
- Current clean-reviewed `phase4.md`:
  `862c22c6977ae4fd2e1ad6f0cd98bd8b0f92e938f8bc7052f388be6c1b4d1ba7`.
- Current `phase3.md`, including Item 3.6:
  `4f9a9f7eed551dadd4e800941b8fc118c77a73a764c3cf3c7db123c1f435c560`.
- Before `phase5.md`:
  `7401fdb72a1e102670dae6105d35c68644575920a71964f71b751efe8766ef55`.
- After `phase5.md`:
  `745f468ecf651ec39f6787cf3c132b988d7c948fc5d812025f1abda499fe6dc5`.
- Exact reviewer diff:
  `a424565e18cb3b13c2dd12bf245d160245842ea671bc221c1109d1d476416454`.

Read agent-conduct, completion/liveness, phase-reviewer, unslop,
writing-for-agents, prose-principles and final-response. Compared E1/E2/E3 and
all their subcases, D3, independent-suite architecture, bounded accounting,
Implementation Order, current prerequisite plans, frozen family contracts and
inventory, and retained public CLI dispatch.

## Fixed findings

1. E1 diagnostic approval could derive expected literals from actual output.
   Item 5.1 now requires authoring import/missing-output literals from pinned
   error/formatter source and independent acceptance before execution.
   Unapproved cases remain incomplete; actual disagreements need source-based
   resolution while raw observations and independent truth stay unchanged.
2. E1_02 omitted its negative subcase. Item 5.1 now requires removing downstream
   emission evidence to leave the UAT incomplete despite matching priority and
   task counts.
3. E1_03 omitted explicit bootstrap verification and the fixture-executable
   counterexample. Item 5.1 now runs bootstrap verification, separately checks
   wr-runtime exit 1/E_ADAPTER_UNAVAILABLE/zero passes, and proves a subject
   printing expected values cannot change that result.
4. E2_02 named only an invalid/surviving outcome. Item 5.2 now identifies the
   actual unrelated-error or unchanged-input mutation that must cause it and
   retains foundation exit 1/E_MUTATION_NOT_KILLED.
5. E1/E3 readiness roles were incomplete and could be assigned after readiness
   work. Instructions and Item 5.3 now assign independent source, observer,
   mapping, expectation, dependency, selector, launch and actual-result owners
   before readiness work. Complete current input/recipe/isolation/cleanup
   approval precedes build or launch. Item 5.4 now builds and independently
   accepts actual output digests before engine launches. The D3 per-command
   deadlines and all 69 unchanged foundation IDs are explicit.
6. Sequential code changes could stale earlier genuine D2-bound captures while
   closure silently reused them. Item 5.8 now reapproves readiness, rebuilds
   affected inputs, reruns all required routes and independently accepts their
   actual results after its code changes. Final closure repeats fresh E1 and
   complete E3 executions plus current E2/E3 controls after Item 5.11, rechecks
   input keys, and repeats affected work after later changes. Stale captures
   receive zero credit; result reviews stay in evidence closure without cycles.
7. E3_02 listed the 155 losses but its removal instruction omitted units and
   resume invocations. Item 5.9 now names both explicitly while preserving the
   fixed manifest and all intact counterparts.
8. Final receipts treated analytical witnesses as supervised launches. Closure
   now requires stopped receipts for actual launches/subjects and independent
   evaluation receipts for analytical witnesses. Item 5.7 now states that actual
   observations are reviewed against frozen expectations.

## Acceptance and sequencing

All eleven E IDs have one owner and the spec's test file. Items 5.1-5.11 are
continuous and sequential. Items 5.4-5.7 supply components; Item 5.8 alone owns
E3_01. Items 5.9-5.11 own E3_02/E3_03/E3_04 respectively. All 69 unique
foundation obligations retain one owner and source provenance across six plans,
including the original 49 and twenty additions.

- E1 retains all seven actual pinned offline cases, parser v2/static typing off,
  two-task local execution, network-denial proof, source/distribution hashes,
  exact error contracts, no malformed/extra value filtering, missing-output
  script exit zero and fair raw B,A completion versus A,B emission. E1_04
  retains contradiction, raw observations and an unresolved disagreement
  candidate without approval or expected-data edits.
- E2 retains eighteen independent one-change accounting subjects with clean
  baselines, declared exit/diagnostic and 18/18/0/0 accounting. Its three null,
  duplicate and fair-order observer subjects fail E_EXPECTATION; normalization
  cannot hide them and no mutated wr implementation claim follows.
- E3 retains exact literal selectors, six unchanged native features, four
  fresh/resume CLI invocations, 42 original predicates and fifteen completions.
  Native builds execute current published sources/recipes and preserve parser
  shared-instance/TestUtils ordering, Mix reset/MockSession/five-second timeouts
  and original Bash aggregates.
- E3 neutral evidence retains fifteen primary units, eight separate CLI parser
  probes and six stronger exit/byte checks on four existing CLI invocations.
  Required checks include 29 JVM and 28 CLI-P1-P7 scalars, P6 substring/P2
  backslash+n, nine typed Mix predicates, exact S-MIX plus collection
  completion,
  exact topic bytes/final LF, associated G-IN/G-OUT diagnostics, G-OUT script
  exit/file bytes and both outer-item file/List<file> shapes.
- E3_02 retains all 155 intact/loss pairs: 11 unit, 42 predicate, 83 fixture,
  two resume, fifteen completion and two final-LF losses. Every intact copy
  passes before the intended missing-ID/hash/observation rejection; required
  counts remain fixed and unrelated failure is invalid.
- E3_03 retains six ordered Bash subjects and aggregate exits 0,1,0,1,0,0,
  precisely four stronger rejections/two acceptances, exact nullable bytes,
  invocation/pipeline/tee receipts and three analytical M1 witnesses. Neither
  subject class awards a Nextflow or nullable DSL pass.
- E3_04 retains five separate intact/faulty typed subjects: S-MIX integer
  stringification, ORACLE_MIX duplicate loss, completed-value prefix without
  collection termination, unrelated-process G-IN error and bare-file collapse
  of G-SHAPE-LIST. All five must fail their intended diagnostic with zero
  survived/invalid; these supplement rather than replace E2's three subjects.

Source/observer/expected/dependency/selection/launch acceptance precedes genuine
builds and engines; independent actual-result review follows each execution.
Current Phase 4 and Item 3.6 supply launch/supervision prerequisites, not E1/E3
result credit. Full input manifests, roughly 100k context bounds, fresh
contexts,
changed-input reapproval and coherent splits preserve the single acceptance
owner. Missing resources, unsupported isolation or unknown cleanup leave work
incomplete. Actual compile/run success cannot be replaced by acquired bytes,
historical pilot logs, old generated classes or supplied fixture output.

Accounting, native, neutral, strengthened, document-gap and replay denominators
stay separate. CLI-P8 preserves its actual required-greeting CLI failure beside
JVM count zero. String Mix, seven helper/internal obligations, five document
closure links, full target inventory and both product decisions stay pending.
wr remains pure Go with no adapter, no observation and zero runtime passes.
F3 fresh-checkout proof remains Phase 6 work.

## Checks and preservation

Reviewer-owned receipts are in
`.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/`,
under `phase-review05-01/`:

- `checks.json` records exact before/after hashes, all 69 ownership rows,
  eleven phase owners and document checks.
- `document-check.txt` records plan/report ASCII, 80-column prose, one h1,
  heading levels, labelled fences, resolved links, no placeholders, whitespace
  and blank-line checks, continuous items and unchecked pairs.
- `protected-before.json` and `protected-check.json` verify 358 protected spec,
  other-plan, package/CLI code, source-lock/batch and frozen-pilot files.
- `phase5.before.md` and `phase5-review.diff` preserve exact reviewed input and
  fixes. `diff-check.txt` records the bounded Git diff check.

The unchanged source lock contains 2,856 files and all 160 selections; all
18 bootstrap batches remain unchanged. The global ownership comparison passes
for 69 unique IDs/test files/items, 49 retained originals plus twenty additions.
All eleven Phase 5 implemented/reviewed pairs remain unchecked.

Retained public CLI dispatch implements acquire/validate only; later commands
still return E_PREREQUISITE. No future command, UAT, build, engine or runtime
result was executed or credited. This worker wrote only phase5.md, this report
and its own scratch. No commits, pushes, installs, nested agents or background
jobs were created. No unresolved plan findings or owned live work remain.
