# Independent suite Phase 5 review 02

Verdict: FIXED. A fresh unchanged-plan review is still required.

Reviewer: `/root/nextflow_suite_phase05_review02`. Workflow owner: `/root`.
Branch: `nextflowdsl`. Worktree: `/home/ubuntu/wr`. Review date: 2026-10-08.

This review awards plan correctness only. It marks no item implemented or
reviewed and awards no foundation, engine or wr runtime acceptance.

## Reviewed identities

SHA-256 inputs and output:

- Accepted `spec.md`:
  `57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c`.
- Clean-reviewed `phase4.md`:
  `862c22c6977ae4fd2e1ad6f0cd98bd8b0f92e938f8bc7052f388be6c1b4d1ba7`.
- Current `phase3.md`, including Item 3.6:
  `4f9a9f7eed551dadd4e800941b8fc118c77a73a764c3cf3c7db123c1f435c560`.
- Before `phase5.md`:
  `745f468ecf651ec39f6787cf3c132b988d7c948fc5d812025f1abda499fe6dc5`.
- After `phase5.md`:
  `9d8cf993d27756bfc9c6680736d8d1003064c3b3c3fb90888d6ee85730893b67`.
- Exact reviewer diff:
  `f32ab6138f7c21b06ceb73e8af757c4a09dc3c6f3ac1e0e7f1080c11bf13ab25`.

Read agent-conduct, completion/liveness, phase-reviewer, unslop,
writing-for-agents, prose-principles and final-response. Independently compared
all E1/E2/E3 subcases with the plan, accepted architecture and implementation
order, current Phase 4 and Item 3.6, frozen contracts and actual pilot control
denominators. Read review 01 and checked its corrections against current bytes.

## Fixed finding

The focused command used an outer 60-minute timeout but omitted Go's test
timeout. Local `go help testflag` states that the default is ten minutes and
expiry panics the test binary. That command could abort valid E3 integration
work before its explicitly allowed 3,600-second bound. The focused command now
sets `-timeout=60m`. Split invocations must also set their reviewed Go timeout
within the existing one-hour suite ceiling. The fixed D3 recipe, native, neutral
and original-runner deadlines remain explicit.

The same default mismatch is present in Phase 3's outer 15-minute command,
Phase 4's outer 30-minute command, and Phase 6's outer 60/20-minute commands.
Those files are outside this worker's write scope and remain unchanged. The
workflow owner received the evidence for separate correction and review.

## Acceptance and execution ordering

All eleven E IDs have exactly one acceptance owner and the spec's test file.
Items 5.1-5.11 are continuous and sequential; Items 5.4-5.7 provide E3_01
components, while Item 5.8 owns its complete acceptance. The six plans retain
all 69 unique IDs, original 49 plus twenty additions, with valid owner items
and unchanged test-file bindings. Each owner closes all spec subcases.

- E1 retains seven genuine pinned offline cases, separate source-derived import
  and missing-output diagnostic approval before execution, zero error-case
  values and missing-output script exit zero. Fair completion B,A and downstream
  emission A,B are distinct raw proofs. Removing downstream evidence makes
  E1_02 incomplete. E1_03 explicitly checks bootstrap reporting, wr-runtime
  exit 1/E_ADAPTER_UNAVAILABLE/zero passes and the expected-printing fixture
  counterexample. E1_04 preserves contradictory truth and an unresolved
  disagreement candidate without approving it.
- E2 retains eighteen independent one-change subjects with intact preflight,
  declared command/exit/diagnostic and 18 killed, zero survived/invalid. E2_02
  independently introduces an unrelated error or unchanged input and requires
  foundation exit 1/E_MUTATION_NOT_KILLED. The three null, duplicate and sorted
  fair observer controls remain separate and cannot be concealed by changed
  normalization or represented as wr implementation mutations.
- E3 retains six exact native feature selectors and four original fresh/resume
  invocations, 42 original predicates and fifteen original completions. Parser
  shared-instance/TestUtils order, Mix inherited reset/MockSession/helper/config
  closure and five-second feature deadlines, and original Bash aggregates stay
  intact. Selected unavailable originals remain incomplete.
- Neutral proof retains fifteen primary units, eight JVM parser, three Mix and
  four gaps; eight CLI parser probes have their own denominator. Checks retain
  29 JVM and 28 CLI-P1-P7 scalars, all eight authored normalization fixtures,
  P6 substring/P2 backslash+n semantics, nine typed Mix predicates and exact
  S-MIX collection completion. Four existing CLI invocations provide six
  stronger exit/byte contracts, including both exact topic files and final LF.
  Gap controls require associated count diagnostics, G-OUT script exit/file
  bytes and distinct file/List<file> shapes before rendering.
- E3_02 preserves 155 intact/loss pairs: eleven units, 42 predicates, 83
  fixtures, two resumes, fifteen completions and two final-LF losses. Each
  intact counterpart passes first; only the intended missing-ID/hash/receipt
  failure counts as a rejected loss. Required counts stay fixed.
- E3_03 preserves six Bash subjects with original exits 0,1,0,1,0,0, four
  stronger rejections/two acceptances, exact nullable bytes and invocation/
  pipeline/tee/supervision receipts. Its three analytical M1 witnesses retain
  their two extra-value failures and valid permutation. Tool failure is invalid
  with E_MUTATION_NOT_KILLED; these subjects supply no Nextflow/nullable pass.
- E3_04 retains five separate typed faults and intact baselines: integer
  stringification, ORACLE_MIX duplicate loss, complete S-MIX values without
  collection termination, unrelated-process G-IN error and G-SHAPE-LIST bare
  file collapse. Their intended failures require zero survived/invalid and
  supplement E2's three semantic subjects.

Named readiness authors/implementors and independent source, observer/mapping,
expectation, dependency, selector, launch and result reviewers are assigned
before readiness work. Complete current input/recipe/isolation/cleanup review
precedes builds and launches; independent actual build-output approval precedes
dependent engine launches. Actual engine-result acceptance follows execution.
Historical classes or pilot captures supply no new execution credit.

Item 5.8 and final Item 5.11 closure require readiness approval, affected
rebuilds, fresh E1/E3 native/neutral/CLI execution under final D2 input keys and
current controls. Later bound-input changes require affected reruns and result
reacceptance. Result reviews remain evidence closure without expected-truth or
extraction cycles. Full measured manifests, fresh contexts and coherent splits
preserve whole-subcase owners and the roughly 100k context ceiling.

CLI-P8 retains its genuine failed mapping separately from JVM count zero.
String Mix, seven internal obligations, five document closure links, full target
inventory and both product decisions remain pending. wr retains no adapter,
observation or runtime pass. F3 fresh-checkout proof remains Phase 6 work.

## Checks and preservation

Receipts are under
`.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/`,
in `phase-review05-02/`:

- `phase5.before.md` and `phase5-review.diff` retain the exact input and fix.
- `go-timeout-contract.txt` retains the local timeout/default contract.
- `checks.json` records hashes, all 69 ownership rows and document checks.
- `protected-before.json` and `protected-check.json` verify 358 protected spec,
  other-plan, source-lock/batch, package/CLI code and frozen-pilot files.
- `prior-protected-check.json` compares those inputs with review 01's snapshot.
- `document-check.txt` records ASCII, 80-column prose, headings, labelled
  fences, resolved links, whitespace, placeholders, continuous items and
  unchecked pairs.
- `diff-check.txt` records the bounded Git diff check.

The lock retains 2,856 files and 160 selections; all eighteen bootstrap batches
and frozen pilot files are unchanged. Eleven implemented/reviewed pairs remain
unchecked. Public CLI dispatch still implements only acquire/validate; planned
commands return E_PREREQUISITE. No future command, build, engine or runtime gate
was executed. Writes are limited to phase5.md, this report and owned scratch.
No commits, pushes, installs, nested agents or background jobs were created.
No unresolved Phase 5 plan findings or owned live work remain; the other-plan
timeout finding is routed to the owner, and fresh unchanged review is pending.
