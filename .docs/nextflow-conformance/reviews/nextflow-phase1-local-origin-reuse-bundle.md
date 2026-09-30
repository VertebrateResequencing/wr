# Local-origin locked reuse input bundle

APPROVED on 2026-09-28 after independent model PASS and input remeasurement.
Use fresh implementation and review. This supersedes the oversized reuse
bundle; do not reread it. Item 1.1 requires both split PASS verdicts.

## Contract and ownership

The accepted model permits `local:sha256:<64 lowercase hex digits>` only
for Java/environment-tool regular-file snapshots, file packaging, null
coordinate, and a digest equal to file.sha256. Lock/tree and other artifact
origins retain strict HTTPS.

Change locked acquisition to retain and rehash the existing safe snapshot
at file.path, without fetching or copying it. Check actual length/hash and
regular-file status; preserve path, bytes, mode, and runtime edges. Reject
missing/altered/non-regular files, escaped paths, final symlinks, symlink
ancestors, and unsafe cache boundaries. Preserve candidate-input collision
protection and failure atomicity. HTTPS objects keep their bounded fetch
path. The local origin must never reach the fetcher.

Own acquireLockedArtifact, required helpers, public source tests and
fixtures. Change verification/publication callers only for demonstrated
failures. Model changes need reapproval. Actual Java/tools copying/closure,
opaque runtime, downloaded permissions, POMs, A1_05-07, actual candidate
and selectors remain part 2 work.

Evidence must record observed source path, resolved target, snapshot path,
hash, bytes, and mode separately from the content identity. No invented
upstream URL. Fixtures prove local snapshot reuse, not actual Java execution;
part 2 must capture provenance for the actual Java/tools it acquires.

## Initial reads and budget

Paths are relative to `/home/ubuntu/wr`. Read full SKILL.md files under
`/home/ubuntu/.agents/skills/` for agent-conduct, go-conventions,
implementation-principles, testing-principles, writing-for-agents, unslop,
prose-principles, and final-response. Add go-implementor, or go-reviewer plus
code-smells. Totals are 36,148 implementor / 40,386 reviewer bytes. Check
applicable AGENTS; none were present at approval.

| File | Inclusive baseline lines | Bytes |
| --- | --- | ---: |
| `.docs/nextflow-conformance/spec.md` | 110-122 | 863 |
| `nextflowconformance/source.go` | 115-158,313-398,1259-1314 | 4,469 |
| `nextflowconformance/cli.go` | 201-274 | 1,787 |
| `nextflowconformance/source_test.go` | See below | 7,381 |

Source spans cover publication/input protection, file verification,
safeLocal, locked acquisition and saving. CLI spans cover path containment.
Test spans: `55-150,273-317,389-425,692-734`, for fixtures, save/run/accept,
snapshots, and candidate protection. Relocate by function after edits.

Sources total 14,500 bytes; with skills, 50,648 implementor / 54,886 reviewer.
Add this report. Four bytes/token is an estimate. Each role has a
55,000-token working cap: 20,000 initial reads including this report;
10,000 supplemental reads/growth/diff; 5,000 command output; 15,000
reasoning/edits; 5,000 harness/brief instructions. Keep 30,000 after initial
reads and instructions. Reapprove before projected consumption exceeds a
category. The earlier 32,768-context claim was unsupported and is withdrawn.
Observed model reads were about 14,470 tokens plus 1,607 supplemental;
expected changed functions/tests/cases need 5,000-7,000 more. This allocation
covers those inputs below the roughly 100,000-token ceiling; remeasure reuse
inputs after model PASS.

Read model verdict and origin changes within the 10,000 growth cap. Prior
reviews cover unchanged behavior; select failing cases/callers as needed.

## Proof and review inputs

Write red/green GoConvey tests at the public acquisition/validation boundary:

- Both local roles succeed with runtime edges. Read the candidate and
  actual retained files to verify identity, path, size, mode, and closure.
  Assert zero local fetches and expected HTTPS requests. Stop the fixture
  server and prove offline validation. Label inert Java fixtures accurately.
- Public CLI rejects a syntactically valid unequal origin/file digest.
  Cover missing, content/length altered, non-regular, escaped, and symlinked
  paths, including ancestors/cache boundary. Capture codes, JSON, stderr,
  exit, and request counts. Check old lock/cache snapshots and modes after
  failure, including candidate collision with a retained local file.
- Preserve A1_01-04 and all existing security regressions. Run full focused
  package/CLI tests and stock schema checks. Rehash actual local files
  independently in review and verify the observed provenance evidence.

Use Go 1.27.1, CGO_ENABLED=1, netgo, count=1; timeout 10m for Go/lint and
timeout 2m for schemas. Lint uses existing `.tmp/agent/bin/golangci-lint`
under GOTOOLCHAIN=go1.26.3 with unchanged analyzers. Clean owned changed
code; add no findings to the fourteen deferred source-function diagnostics.
Keep logs on disk; print summaries. Report missing prerequisites. Unrelated
wr tests and system installs are outside scope.

Before review, remeasure all changed/new functions, tests, cases and evidence,
including untracked files. Replace overlapping base spans and inspect the
complete diff mechanically. Model handoff, additions, extra reads and
evidence share 10,000 tokens. If insufficient, report a measured blocker
before dispatch. Return changed spans, red/green results, provenance,
regression/lint counts and PASS/FAIL.
