# Independent suite Phase 4 review 03

Verdict: PASS. No findings or plan edits.

Reviewer: `/root/nextflow_suite_phase04_review03`.
Workflow owner: `/root`. Branch: `nextflowdsl`.
Worktree: `/home/ubuntu/wr`. Review date: 2026-10-08.

This fresh review accepts the corrected Phase 4 plan against the accepted spec
and current clean-reviewed Phase 3. It grants no implementation, build, engine
execution or runtime acceptance. All five item pairs remain unchecked.

## Reviewed identities

SHA-256:

- Accepted `spec.md`:
  `57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c`.
- Current clean-reviewed `phase3.md`:
  `f4504f69826501d4dabdfd67930a9bf529db07f77b0b1aa465061784d3ad684e`.
- Input and output `phase4.md`:
  `8b9ad5ef415f8dd2662894ea29b0b978f0d359f666c85b893ad8bf78372c30b9`.

Read agent-conduct, completion/liveness, phase-reviewer, unslop,
writing-for-agents, prose-principles and final-response. Compared the spec's
architecture, independent-suite records/routes, bounded accounting, public
result contract, all D stories and Implementation Order. Read current Phase 3,
its clean review, prior Phase 4 review and the bounded deadline correction.
Rechecked retained CLI dispatch and local Go timeout documentation.

## Whole acceptance tests and ownership

The spec has 69 unique numbered obligations, the original 49 plus exactly
twenty B3/C3/D3/E3/F3 additions. Phase tables assign every obligation once.
Phase 4 preserves all twelve D IDs, whole imported subcases and test files.
Items 4.1-4.5 are continuous and sequential; the ownership table closes each
whole acceptance test after its prerequisite review.

- D1_01 requires actual run/pass/package-pass events, exit 0, executed 1,
  passed 1 and verified raw-log/manifest hashes.
- D1_02 separately executes skip, fail, one-second timeout, exit-0/no-match
  and missing-observation subjects. Exit 1 diagnostics are E_TEST_SKIPPED,
  E_TEST_FAILED, E_TEST_TIMEOUT, E_TEST_NOT_RUN and E_OBSERVATION_MISSING.
  Every subcase awards zero passes; timeout leaves no owned child alive.
- D1_03 sends malformed/truncated JSON, unmatched passes and duplicate
  terminal events through the recorder boundary, returning 2/E_TEST_EVENTS.
  A passing test in a failing package returns 1/E_TEST_FAILED. D1_04 owns
  interrupted publication, 1/E_ATTEMPT_INCOMPLETE and rejection of old
  evidence from different inputs.
- D2_01 first verifies an unchanged completed fixture, then independently
  changes dirty implementation bytes, corpus block, tests, expected bytes,
  normalization, build tags and the whole distribution. Each produces
  E_EVIDENCE_STALE and zero passes. D2_02 separately deletes raw logs and
  alters artifacts, producing 2/E_EVIDENCE_MISSING or 2/E_ARTIFACT_HASH;
  forged passed status cannot change either outcome.
- D2_03 controls a mutation during execution with a barrier and requires
  1/E_INPUT_CHANGED with no current pass. D2_04 selects the newer failure
  with identical hashes despite an edited older timestamp. D2_05 keeps a
  generated Markdown edit outside runtime freshness while render check fails;
  rerendering restores the view without adding execution.
- D3_03 owns detached TERM-ignoring descendants and a child created during
  termination. At one second it kills/reaps every owned child, returns
  1/E_TEST_TIMEOUT and zero passes, and retains each stopped receipt.
  A fabricated stopped summary returns E_OBSERVATION_INCOMPLETE. Unknown
  cleanup stays incomplete; unrelated processes remain untouched.
- D3_01 requires separate genuine native and neutral attempts. Native XML
  retains exact selection order, original checks/helpers and stopped
  supervision; neutral typed receipts support only preserved observables.
  Feature, resume invocation and terminal receipt removal independently
  return 1/E_NATIVE_INCOMPLETE and name the original ID.
- D3_02 independently mutates observer code, mapping source argument,
  extension resource, build recipe and native order. Affected old attempts
  become E_EVIDENCE_STALE with zero current passes. Missing callback/XML
  bytes return E_EVIDENCE_MISSING. Identical artifact replay has zero executed
  and zero new engine passes; a newer genuine failure still wins.

D1 additionally requires exact selected-subtest completion, 16 MiB log limits,
E_OUTPUT_LIMIT termination, cancellation propagation and atomic publication
after logs close and hash. D2 hashes before/after inputs, current bound absence,
dirty/untracked code, active dependencies, retained extraction generations,
tools and effective allowlisted environments. Only fixed generated output
classes are excluded. Verification derives status again from raw evidence and
uses monotonic sequence for newest completed exact-input selection.

## Prerequisites, review owners and claim boundaries

Current Item 3.6 supplies its actual reviewed Mix smoke and narrow launch
primitive before phase entry. Item 4.3 then extends and proves full descendant
supervision before Item 4.4 compiles or launches genuine originals. Historical
PREREQ-F1 evidence stays unchanged and cannot approve corrected cleanup.

Item 4.4 requires the queue owner to name compile/native and neutral launch
implementors plus independent source, observer, dependency, selector, launch
and result reviewers before readiness work. General item handoffs also require
named implementor/input/code reviewers, measured manifests, fresh contexts,
roughly 100k total context ceilings and changed-input reapproval.

Current source/helper/fixture/expected, observer/mapping, actual executable
closure/recipe, exact ordered selector/suite-lock and launch/isolation/cleanup
reviews precede compilation or engine execution. Actual offline compilation
retains successful receipts and output identities distinct from acquired
resources. Genuine native smoke and independent build/result acceptance are
required. Neutral readiness uses published Mix workflow/config/observer under
Item 4.3 supervision, with current exact-byte readiness reviews and actual
accepted results. Stale readiness needs renewed execution and result review.
Missing resources produce E_DEPENDENCY_MISSING before build or engine launch.

Item 4.5 owns the complete D3_01/D3_02 tests after Item 4.4 review. Code and
input changes require fresh genuine native and separate neutral attempts under
current D2 hashes, reaccepted readiness and rebuilt changed compiled inputs.
Each actual result needs independent acceptance. The intact actual original
CLI fresh/resume pair reruns before the resume-loss subcase. Earlier accepted
smokes establish readiness and add no new result credit.

Native XML establishes original predicate execution, without invented neutral
values or successful assertions' raw values. Session assertions, MockSession
and engine exit establish no neutral internal identity, lifecycle or raw
original/candidate equality. Such equality requires both genuine captures and
accepted instrumentation review. Partial/unresolved mappings remain incomplete.
The unavailable wr route retains null binding/no observer, returns
1/E_ADAPTER_UNAVAILABLE, creates no observation and awards zero wr passes.
Replay cannot supersede or revive a genuine attempt. Raw artifacts and accepted
result-review bytes remain in evidence closure without extraction-input cycles.

## Deadline and preservation checks

D1 retains 1-180-second ordinary deadlines and a twenty-minute suite ceiling.
D3 retains native 300, neutral/CLI 120, original CLI runner 540 and offline
recipe 1,800-second limits, plus original five-second Mix feature timeouts.
Only E3/F3 integration bindings may declare 3,600 seconds and a one-hour suite
ceiling. Offline builds and readiness smokes run separately before focused UATs.

The corrected focused command sets outer timeout 30m and Go -timeout=30m.
Local go help testflag confirms Go otherwise uses ten minutes per test binary;
the outer timeout bounds total compilation/execution. Reviewed splits must set
explicit Go bounds within the thirty-minute outer bound and omit no D ID.
The exact correction matches the recorded timeout/split changes; item bodies,
ownership and dependencies remain byte-identical to the prior clean plan.

Owned receipts are under
`.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/`,
in `phase-review04-03/`:

- `phase4.before.md` and empty `phase4-review.diff` retain unchanged plan bytes.
  The empty diff SHA-256 is
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- `scope-before.json` and `scope-check.json` compare 1,314 existing repository
  files and permit only this new review report.
- `protected-before.json` and `protected-check.json` compare 358 protected
  spec, other-plan, code, source-lock/batch and frozen-pilot files.
- `go-timeout-contract.txt` retains actual local Go documentation.
- `checks.json` records identities, all 69 ownership rows, twelve D owners,
  exact correction, lock counts and plan/report document mechanics.
- `document-check.txt` and `diff-check.txt` retain document/whitespace verdicts.

Checks pass for ASCII, 80-column prose, one h1, heading levels, labelled fences,
resolved links, whitespace, placeholders, continuous items and unchecked pairs.
The 2,856-file lock, 160 selections, eighteen batches, frozen pilot, spec, other
plans and code are unchanged. The plan has five unchecked implemented marks,
five unchecked reviewed marks and zero checked marks.

Retained CLI dispatch implements acquire/validate only; recognized later-phase
commands return E_PREREQUISITE. Planned route commands still require
implementation.
No planned command, UAT, build, engine or runtime gate was run or credited.
Writes are limited to this report and owned scratch. No commits, pushes,
installs, nested agents or background jobs were created. No unresolved plan
findings or owned live work remain.
