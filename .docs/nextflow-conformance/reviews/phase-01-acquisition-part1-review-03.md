# Acquisition part 1 root-containment review

Verdict: PASS for A1_01 through A1_04 on 2026-09-28. R3 is fixed, and the
R1 candidate-input and R2 unsafe-root protections remain intact. No blocking
correctness or code-smell finding remains in this split. Item 1.2 and Phase 1
remain incomplete pending part 2.

## Correction and boundaries

The complete correction against the retained pre-R3 source changes
`nextflowconformance/source.go:481-489` and the existing root-header test at
`source_test.go:768-880`. `safeSymlink` accepts a resolved destination of
`.` while retaining explicit empty, absolute, backslash, NUL and parent-escape
rejection. `safeRelative` at `model.go:1244-1254` remains byte-identical and
continues rejecting `.` as an ordinary record path. The existing decoder
and stock-schema `path-dot` mutation passes its rejection assertion.

The independent public CLI probe accepts root targets `.`, `./`, `safe/..`
and `safe/../child`; each acquisition reports three verified artifacts and
each candidate validates. Its absolute and parent-escape controls return 2
with `E_SOURCE_PATH` and preserve the previous candidate and cache. The
expanded Go regression also covers safe child and regular root headers,
plus backslash and empty links, for ten layouts. Root-header validation still
runs before omission at `source.go:1031-1041`. Archive root names gain no
new restriction.

The same predicate serves extracted member links and source-link validation.
Root equality is the containment boundary in both callers. The correction
uses the existing helper without a new API, dependency or abstraction.
Every public archive mutation retains all 2,856 pinned blobs and all 3,584
tree entries. The probe independently verifies blob identities, all Git
directory objects and the pinned root; it compares each mutated archive's
complete blob contents against the original.

Candidate protection remains at `source.go:140-159`, called against both
the previous lock and the new generation before publication. Public probes
reject existing/new Java and source paths, artifact paths, input ancestors,
normalized aliases and symlinked output paths. The complete cache and corpus
are preserved on rejection. Distinct, replacement and sibling candidates
succeed. Atomic replacement of a hardlinked candidate preserves its input
artifact, and the resulting candidate validates.

## Independent checks

The [manifest][manifest] records exact command arguments, exits, logs,
source and evidence hashes, and aggregate artifact-tree digests. All commands
ran from `/home/ubuntu/wr` on `nextflowdsl` at
`6121575cb42a3342f483618d71831cafe2e209a3`.

- Go 1.27.1 with `CGO_ENABLED=1`, `-tags netgo` and `-count=1` passes all
  27 focused tests in `./nextflowconformance` and
  `./cmd/wr-nextflow-conformance`, with no failures or skips. The CLI builds.
- Stock Draft 2020-12 validation passes eleven schemas and 1,190 mutations.
- The unchanged review 02 probe passes 29 CLI calls with the fresh binary,
  a new scratch directory and the retained pre-R1/R2 comparison binary.
  The unchanged fix 01 probe passes 23 calls in another new directory.
- Final offline validation succeeds after each probe's HTTPS fixture stops.
  The fix 01 probe also proves transactional warm-cache HTTP 503 failure
  and rejects a FIFO source without hanging. The focused suite exercises
  the actual 256 MiB plus one-byte object limit.
- Lint v2.12.2 under `GOTOOLCHAIN=go1.26.3` exits 1 with exactly fourteen
  deferred findings and zero owned findings. Its log matches fix 02 byte
  for byte. The six deferred functions remain byte-identical:
  `acquirePinned`, `acquireJava`, `copyJavaFile`, `acquireRuntime`,
  `unpackRuntime` and `acquireDependencies`. This is not a package-wide
  lint pass.

The red evidence is bound to the retained pre-R3 binary and original source,
which match review 02. It records exactly the three safe-root failures;
the extended Go regression also fails for those three layouts. Historical
binaries and evidence were preserved. The fresh corrected binary matches
fix 02's corrected binary byte for byte.

All 46 fix 02 file bindings match. Of review 02's 42 bound inputs, only the
two reviewed Go files changed. The 171-file preservation audit identifies
only those changes and the parent's authorized progress update since fix 02
started. Every frozen input remains unchanged during this review. All five
historical/current probe inventories were independently rehashed, including
130 captured CLI invocations. Full inventories stay on disk; the manifest
contains their counts and digests.

[manifest]: ../evidence/phase1-acquisition-part1-review-03-manifest.json

## Scope and handoff

The existing acquisition bundle's part 1 split was approved before
substantive review, with a 90,000-token working budget and roughly
100,000-token ceiling. Complete correction diffs and affected callers were
read; unchanged model, CLI, schema, module and fixture coverage relies on
verified prior-review hashes. Generated corpora and artifact inventories
were inspected by scripts rather than dumped into context.

A1_05 through A1_07, actual Java/runtime closure, actual candidate acquisition
and acceptance, A2 selectors and the remaining lint findings belong to
part 2. No phase, oracle, workflow or wr pass is awarded. Full unrelated wr
tests were not run. This review writes only its report, manifest and scratch
evidence; no source, spec, fixture, module, lint configuration, checklist,
commit or push was changed.
