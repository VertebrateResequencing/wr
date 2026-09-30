# Phase 1 full runtime review 01

Verdict: FAIL on 2026-09-30. Required fixes are executable-mode preflight,
final lint failures, and an inherited acquisition mode-preservation defect.
Item 1.2 remains open.

## Findings

1. **P1: Offline preflight accepts unusable acquired executables.**
   `nextflowconformance/source.go:333` checks execute permissions only for
   runtime and launcher roles. Environment tools and acquired interpreters
   pass after their execute bits are removed. On the fresh 282-artifact
   fixture, changing acquired `bash` from 0755 to 0644 leaves `validate` at
   exit 0, `complete:true`, and `verified_artifacts:282`. The same acquired
   inputs then fail offline startup with exit 126 and
   `/usr/bin/env: 'bash': Permission denied`. Acquired `which` also passes
   validation after chmod 0644. Restoring both original modes restores the
   baseline. The reuse path calls this same verification function.

   Enforce executable requirements for the acquired tools, interpreters,
   and loaders during offline preflight and local reuse. Preserve the
   distinction between executable inputs and nonexecutable shared libraries.
   Add a behavioral regression that changes permissions without changing
   bytes and requires failure before Nextflow starts. Do not rely solely on
   successful acquisition or on the unverified provenance sidecar.

   Reproduction: run `mode-probe.py` in the evidence directory below. Its
   `mode-audit.json` records baseline, mutated validation, failed startup,
   and restored validation commands and results.

2. **P2: Final source fails the required unchanged lint gate.**
   `nextflowconformance/source_companions.go:203` and
   `nextflowconformance/source_test.go:1098` each trigger `wsl_v5`
   `leading-whitespace`. The independent lint command exits 1 with two
   findings. The implementation manifest records a successful lint command
   using `--fix` and a separate cleanorder operation. That earlier lint
   result does not establish that the final formatted bytes pass lint.
   Remove the two blank lines and run lint without autofix after the final
   formatting operation. Retain that result bound to the final source hashes.

3. **P2: Restrictive umask breaks required source mode preservation.**
   `nextflowconformance/source.go:929`, called by `unpackRegular` at line
   1032, uses `os.WriteFile` without restoring the requested permissions.
   Under umask 077, pinned 0644 files become 0600. The existing A1_06 test
   fails acquisition with `E_SOURCE_HASH` for
   `.claude/commands/speckit.analyze.md`, reporting `source mode differs`.
   This behavior exists in the retained initial implementation; it was not
   introduced by the runtime correction. It remains a required Phase 1
   follow-up because the specification requires original modes. Preserve
   requested source and Java regular-file permissions independently of the
   caller's umask, and test acquisition under a restrictive umask.

   Red command, from the repository root:

   ```bash
   umask 077
   timeout 3m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 TMPDIR=/home/ubuntu/wr/.tmp/agent/nextflow-conformance/runtime-review-01 go test -tags netgo -count=1 -v ./nextflowconformance -run '^TestUAT_A1_06$'
   ```

   Exit 1; full output is `umask-test.log` in the evidence directory.

## Independent verification

The review covered the complete current `source.go`, `source_test.go`, and
`source_companions.go`, all 52,988 bytes of the complete runtime diff, the
approved contract/reference spans, and the acquired inputs. Reconstructing
that diff from the retained initial snapshots produces identical bytes.
Every current source/evidence hash in the closure manifest matches. The
approved reference spans also retain their recorded hashes. Source growth
plus the complete diff is 67,995 bytes, within the approved 76,000-byte
allocation. No source, schema, module, lint configuration, phase, or progress
file was changed during this review.

The unmodified regression run passes 34 tests across the focused package
and CLI, including all seven A1 UATs. It uses Go 1.27.1, CGO_ENABLED=1,
`-tags netgo`, and `-count=1`. Stock validation passes all 11 schemas and
1,243 cases. The independently retained acquired lock also passes its stock
schema. Lint uses the existing binary, Go 1.26.3, and unchanged configuration;
its failure is finding 2. No autofix ran during this review.

The fresh fixture contains 282 artifacts: one launcher, one opaque runtime,
249 Java files, 27 environment tools including 15 companions, three POMs,
and one source archive. All artifact bytes, hashes, regular-file types,
original modes, local origins, and 277 provenance observations match their
actual inputs. The Java inventory contains 454 entries. Independent ELF
and script checks confirm the recorded edges for 15 executable/native input
observations, including `which`'s shell interpreter. The dependency graph
is acyclic and its runtime closure contains exactly the execution roles.

The distribution retains all 42,355,106 bytes and the pinned digest. ZIP
inspection finds 24,898 entries, 1,660 repeated names, and no nested JARs.
The production acquisition code performs no distribution extraction.
Independent XML reads confirm the three POM coordinates. The full source
inventory retains `packing.gradle`, `modules/nextflow/build.gradle`, and
`modules/nf-lang/build.gradle` with their recorded bytes and hashes.

Independent startup and traced startup both return Nextflow 26.04.6 with
exit 0. They use the fresh acquired distribution, Java, tools, and companion
snapshots in the existing image with Docker network disabled, a read-only
root and input mounts, and an empty ephemeral user home. No internet
connect call appears in the trace. All 23 successfully opened shared-library
paths were read inside that container and compared byte-for-byte with
acquired snapshots. The trace and library audit are retained.

The kernel, VDSO, image symlink layout, OS configuration, virtual filesystem
inputs, and temporary JVM performance files remain platform supplied. The
complete observed paths appear in `trace-audit.json`. They include
`/etc/ld.so.cache`, NSS/passwd configuration, `/proc`, and `/sys` inputs.
The pre-existing tracer and its extra libraries are separately identified
as harness inputs. These checks prove startup on recorded Linux/amd64;
they do not claim platform portability or a resolved Maven package graph.

## Evidence and remaining boundary

Fresh evidence directory, relative to the repository:
`.tmp/agent/nextflow-conformance/runtime-review-01/`.
The compact binding manifest is
[manifest](../evidence/nextflow-phase1-runtime-review-01-manifest.json).
It records exact commands, verdicts, source identities, fixture identity,
probe scripts, complete logs, and their hashes. Earlier evidence is intact.

The three findings require correction and fresh independent review before
this runtime boundary can pass. Production candidate acquisition and exact
A2 selector acceptance remain the next separate boundary. This review
awards no E1, wr execution, Item 1.2, or Phase 1 completion. No commit or
push was made. No additional design-smell finding blocks this review.

## Approved correction input

APPROVED for a fresh implementor to correct findings 1-3. This approval
covers input size and scope only. It awards no runtime acceptance.

The measured input manifest is
[correction inputs](../evidence/nextflow-phase1-runtime-correction-bundle-01.json).
It includes the three complete current source/test files once, the existing
approved reference/contract/skill spans, this report, all three independent
startup/trace/mode probe scripts, compact result summaries, and the lint
and umask red logs. The implementor substitutes `go-implementor` for
`go-reviewer` and can omit `code-smells`; the manifest conservatively counts
the larger reviewer skill baseline. Read large locks, inventories, command
arrays, and traces through compact programmatic summaries. Their original
paths and hashes remain bound by the review evidence manifest.

The current files already contain the entire runtime implementation. The
correction implementor need not reread the old runtime diff. A fresh final
review must cover the whole final runtime state, including its complete
regenerated diff and added files, rather than only these three corrections.

Allow 64,000 estimated tokens for initial inputs, manifest, and briefing;
16,000 for growth, the final complete diff, and extra dependency reads;
3,000 for command results; and 15,000 for reasoning, edits, and handoff.
Total: 98,000 estimated tokens, using four bytes per token. Remeasure actual
growth and full final diff before dispatching review. Rebalance or split
at an acceptance boundary if required inputs exceed the total estimate.
These estimates are context planning, not a claimed tokenizer measurement.

Keep the accepted closed model/schema and module/lint configuration.
Executable-mode validation must have an authoritative basis; simply reading
an unbound mutable provenance sidecar would not establish it. If a correct
solution needs new closed-record fields or constraints, stop that part and
request a separately measured model/schema correction boundary. Do not
silently broaden those contracts. Preserve transactional failure handling,
original modes, safe local reuse, and all A1 behavior. Run the restrictive
umask regression and offline mode mutations, then existing tests/schemas,
then unchanged lint without autofix after the final formatter. Bind final
source hashes and measured full diff for fresh independent review.
