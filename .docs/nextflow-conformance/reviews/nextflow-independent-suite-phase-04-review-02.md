# Independent suite Phase 4 review 02

Verdict: PASS.

Reviewer: `/root/nextflow_suite_phase04_review02`. Workflow owner: `/root`.
Branch: `nextflowdsl`. Worktree: `/home/ubuntu/wr`. Review date: 2026-10-08.

This is a fresh independent plan review against the accepted spec and current
Phase 3. No Phase 4 edits were needed. It grants no implementation, build,
engine execution or whole-target acceptance. All item marks remain unchecked.

## Reviewed identities

SHA-256 inputs and unchanged output:

- Accepted `spec.md`:
  `57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c`.
- Current clean-reviewed `phase3.md`:
  `4f9a9f7eed551dadd4e800941b8fc118c77a73a764c3cf3c7db123c1f435c560`.
- Before and after `phase4.md`:
  `862c22c6977ae4fd2e1ad6f0cd98bd8b0f92e938f8bc7052f388be6c1b4d1ba7`.

Read agent-conduct, completion/liveness, phase-reviewer, unslop,
writing-for-agents, prose-principles and final-response. Compared every D story
and subcase, the independent-suite records/routes architecture, Implementation
Order, current Item 3.6, prior Phase 4 review and retained public CLI dispatch.

## Acceptance and prerequisite findings

All twelve D IDs have one ownership row and the spec's test file. Items 4.1-4.5
are continuous and sequential. D1 records execution first, D2 establishes
freshness next, and D3's full supervision gate precedes native readiness. Item
4.5 owns the complete D3_01/D3_02 acceptance; earlier items provide reviewed
prerequisites without duplicate test bindings.

- D1_01 owns the real run/pass/package-pass fixture, successful exit,
  executed 1/passed 1 and verified event-log/manifest hashes.
- D1_02 owns independent skip, fail, one-second timeout, no-match and missing
  observation fixtures, their five diagnostics, exit 1, zero passes and child
  cleanup.
- D1_03 owns malformed/truncated events, unmatched pass, duplicate terminal
  events and a passing test in a failing package, with the required exit and
  diagnostic distinctions. D1_04 owns interruption before publication,
  E_ATTEMPT_INCOMPLETE and rejection of substitution from different inputs.
- D2_01 owns unchanged verification and all seven independent stale-input
  changes, including whole-distribution identity and dirty code without a HEAD
  change. D2_02 owns deleted logs, changed artifacts, exit 2 diagnostics and
  rejection of forged manifest status.
- D2_03 owns the controlled input-change barrier and zero current passes.
  D2_04 owns newer-failure precedence despite timestamp manipulation. D2_05
  owns generated-view mismatch without changing runtime freshness or adding
  execution.
- D3_03 owns detached TERM-ignoring descendants, termination-time children,
  one-second timeout, KILL/wait/reap, receipts for every owned stopped child,
  zero passes and rejection of fabricated stopped summaries. Its cleanup
  boundary protects unrelated processes and leaves unknown states incomplete.
- D3_01 owns genuine native XML/order/assertion execution and separate neutral
  typed evidence, retaining distinct claims. Feature, resume and terminal
  deletion independently produce E_NATIVE_INCOMPLETE with the original ID.
  Session assertions, MockSession and exit cannot establish neutral internal
  identity, lifecycle or raw-original equality.
- D3_02 owns all five independent observer/mapping/resource/recipe/order
  mutations, stale evidence with zero current passes, deleted callback/XML
  evidence, replay with zero executed/new engine passes and newer genuine
  failure precedence. Partial/unresolved mappings stay incomplete.

Item 4.4 requires current exact-byte readiness approval before builds or engine
launches. The queue owner assigns named compile/native and neutral implementors
and independent source, observer, dependency, selector, launch and actual-result
reviewers. Acquired closure and historical pilot results prove no new build.
Actual offline compile receipts/output digests, genuine native smoke and
independent build/result acceptance precede closure. The actual neutral smoke
uses Item 3.6's published workflow/config/observer and narrow launch primitive
under Item 4.3 supervision; stale readiness requires renewed input review,
execution and result review. An intact actual CLI fresh/resume pair supplies
the resume-removal prerequisite.

Item 4.5 explicitly requires fresh native, neutral and CLI pair attempts after
its D2-bound code/input changes. It reaccepts current readiness and rebuilds
changed compiled inputs. It independently reviews each new actual result. Smoke
is readiness evidence, not new result credit. Result-review bytes and raw
artifacts are retained in evidence closure without becoming extraction inputs
or creating hash cycles. The unavailable wr route retains null binding and no
observer, returns E_ADAPTER_UNAVAILABLE, creates no observation and awards zero
wr passes.

The phase preserves complete imported subcases for all 69 foundation IDs,
including the original 49 and twenty additions. Measured input manifests,
roughly 100k total context ceilings, fresh contexts, independent input review
and changed-input reapproval remain required. Fixed selections and completion
denominators cannot shrink. Native originals retain parser shared-instance and
TestUtils ordering, Mix reset/MockSession/five-second feature deadlines, and CLI
aggregates.

D1 retains ordinary 1-180-second deadlines and its twenty-minute suite ceiling.
D3 records separate native 300, neutral/CLI 120, original CLI runner 540 and
offline recipe 1,800-second limits. Offline builds and readiness smoke run
separately before the focused thirty-minute UAT gate. Only later E3/F3 bindings
may declare 3,600 seconds and a one-hour suite ceiling. Missing prerequisites,
unenforceable isolation and unknown cleanup keep affected work incomplete.

## Checks and preservation

Reviewer-owned receipts are under
`.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/`,
in `phase-review04-02/`:

- `checks.json` records document/ownership checks and exact input/output hashes.
- `document-check.txt` records PASS for ASCII, 80-column prose, one h1,
  continuous headings, labelled fences, resolved links, no placeholders,
  trailing whitespace or repeated blank lines, continuous item numbering and
  one unchecked implemented/reviewed pair per item.
- `protected-before.json` and `protected-check.json` verify 358 protected spec,
  other-plan, code, source-selection, batch and frozen-pilot files unchanged.
- `phase4-review.diff` is empty; its SHA-256 is
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- `diff-check.txt` records the bounded Git diff check for this plan and report.
- `report-checks.json` records document mechanics for this report.

Spec counting confirms 69 unique obligations, original 49 plus twenty additions.
Phase 4 has twelve exact D ownership rows, five unchecked implemented marks,
five unchecked reviewed marks and zero checked marks. The protected identities
also match the prior review's 358-file snapshot; preservation includes all 160
selections and eighteen bootstrap batches.

Retained public CLI dispatch implements acquire/validate only; future operations
still return E_PREREQUISITE. No future command, UAT, compile, engine launch or
fixture execution was run or credited. Checks were synchronous bounded document
operations. Writes are confined to this report and reviewer-owned scratch.
No commits, pushes, installs, nested agents or background jobs were created.
No unresolved plan findings and no owned live work remain.
