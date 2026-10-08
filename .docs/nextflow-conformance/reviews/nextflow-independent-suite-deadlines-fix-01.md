# Independent suite Phase 3 and Phase 4 deadline correction

Verdict: FIXED. Fresh independent review of each changed plan is pending.

Worker: `/root/nextflow_suite_deadline_plans_fix01`. Workflow owner: `/root`.
Branch: `nextflowdsl`. Worktree: `/home/ubuntu/wr`. Date: 2026-10-08.

This record reports the correction, not independent plan approval. It marks
no item implemented or reviewed and awards no execution or runtime acceptance.

## Identities

SHA-256 inputs and outputs:

- Immutable accepted `spec.md`:
  `57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c`.
- Before `phase3.md`:
  `4f9a9f7eed551dadd4e800941b8fc118c77a73a764c3cf3c7db123c1f435c560`.
- After `phase3.md`:
  `f4504f69826501d4dabdfd67930a9bf529db07f77b0b1aa465061784d3ad684e`.
- Exact Phase 3 correction diff:
  `e965a756db638f0c00be48ba0ed419b849de657c364c903bfdb931721a9e2468`.
- Before `phase4.md`:
  `862c22c6977ae4fd2e1ad6f0cd98bd8b0f92e938f8bc7052f388be6c1b4d1ba7`.
- After `phase4.md`:
  `8b9ad5ef415f8dd2662894ea29b0b978f0d359f666c85b893ad8bf78372c30b9`.
- Exact Phase 4 correction diff:
  `e0889de5d18f6e957c9f2dc4e51db44899bc321a0dabd78c5d1a3de560f1f34c`.
- Unchanged corrected `phase5.md` used as the command model:
  `9d8cf993d27756bfc9c6680736d8d1003064c3b3c3fb90888d6ee85730893b67`.

## Correction

The confirmed finding in
[Phase 5 review 02](nextflow-independent-suite-phase-05-review-02.md)
identified the omitted Go timeout in Phase 3 and Phase 4. Local
`go help testflag` states that Go panics a test binary after its timeout and
uses ten minutes by default. An outer fifteen- or thirty-minute `timeout`
command leaves that ten-minute default active.

Phase 3's focused command now sets `-timeout=15m`; Phase 4's sets
`-timeout=30m`. Both retain their existing outer bound and complete C/D UAT
selector. Each exit section now requires reviewed split bounds, an explicit
Go timeout within the same outer bound and every acceptance ID. Only those
two command options and split instructions changed.

All item bodies, acceptance ownership and dependencies remain byte-identical.
Phase 3 retains current genuine C3 readiness and actual observation obligations.
Phase 4 retains current native/neutral readiness, separate compile receipts,
D1's 1-180-second deadlines and twenty-minute suite ceiling, D3's distinct
native/neutral/CLI/build bounds and stopped-descendant supervision. The change
adds no build, execution, review or runtime credit.

## Checks and preservation

Receipts are under
`.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/`,
in `deadlines-fix01/`:

- `phase3.before.md` and `phase4.before.md` retain exact inputs.
- `phase3.diff`, `phase4.diff` and `identities.json` retain exact changes
  and before/after/diff hashes.
- `go-timeout-contract.txt` retains the local Go timeout/default contract.
- `checks.json` records document, exact-edit and ownership reconciliation.
- `protected-before.json` and `protected-check.json` verify 357 protected
  spec, other-plan, source-lock/batch, code and frozen-pilot files.
- `phase1-items-exit.before.bin` and the protected check retain and compare
  historical Phase 1 Items and Exit bytes.
- `document-check.txt` records ASCII, prose width, headings, labelled fences,
  links, whitespace, placeholders, continuous items and unchecked pairs.
- `diff-check.txt` records the bounded Git diff check.

The checks pass. All 69 unique ownership rows, original 49 plus twenty
additions, remain unchanged; C and D each retain twelve UAT owners. The lock
retains 2,856 files and 160 selections; all eighteen bootstrap batches and
frozen pilot files remain unchanged. All Phase 3 and Phase 4 item pairs remain
unchecked. Other plans, the spec and code remain unchanged by this worker.

Writes are limited to phase3.md, phase4.md, this report and owned scratch.
No planned command, test, build, engine or runtime gate was executed. No
commits, pushes, installs, nested agents or background jobs were created.
No owned live work remains. The workflow owner must arrange fresh independent
reviews before accepting either changed plan.
