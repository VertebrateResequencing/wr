# Acquisition part 1 root-symlink correction

IMPLEMENTED for R3 on 2026-09-28, pending independent review. This correction
covers A1_01 through A1_04 only. Item 1.2 and Phase 1 remain incomplete.

The [manifest](phase1-acquisition-part1-fix-02-manifest.json) binds source
snapshots, binaries, commands, exits, logs, input hashes and artifact trees.
Both historical public probes were reused unchanged. Their HTTPS fixtures
exercise the built CLI and its default TLS client with explicitly
provisioned inputs. No test input was downloaded and no test was skipped.

## Correction

`safeSymlink` now treats a resolved destination of `.` as contained within
the extraction root. It still rejects absolute targets, parent escapes,
backslashes and NUL bytes. Explicit empty-target rejection preserves the
previous root-header check when `.` becomes an allowed destination.
`safeRelative` and closed record schemas remain unchanged.

The existing root-header regression now covers ten layouts. `.`, `./`,
`safe/..`, `safe/../child`, a safe child link and a regular root header
acquire successfully and validate after the fixture server stops. Escaping,
absolute, backslash and empty links return `E_SOURCE_PATH` and preserve the
previous candidate and cache. Each archive retains the complete pinned tree.

Only `nextflowconformance/source.go` and `source_test.go` changed in the
implementation. R1 candidate-input overlap checks and R2 root-header
validation remain in place. No archive root-name restriction was added.

## Red and green

Scratch evidence is retained beneath:

```text
.tmp/agent/nextflow-conformance/acquisition/fix-part1-02/
```

Before editing source or tests, the original source was copied and built to
`original/wr-nextflow-conformance`. The unchanged [review 02 probe][review]
returned 1 after 26 CLI invocations, with exactly three violations:
`root-dot-current`, `root-dot-slash-current` and
`root-normalized-dot-current`. All returned 2 with `E_SOURCE_PATH` and zero
verified artifacts. The retained pre-R1/R2 binary accepted and validated
the same three layouts, proving the regression against unchanged blobs.

The extended Go regression then failed for those same three safe layouts
against the original production source. All other controls passed. The
corrected production source passes that regression and the full suite.

The corrected binary passes the unchanged review 02 probe with 29 CLI
invocations and zero violations. The three additional calls validate the
newly successful acquisitions. Each reports three verified artifacts; every
candidate validates. The probe independently checks all 2,856 source blobs
and 3,584 tree entries. Its absolute and parent-escape controls still fail
with `E_SOURCE_PATH`, preserving the cache and previous candidate.

The unchanged [fix 01 probe][fix] also passes all 23 invocations. This retains
candidate overlap rejection, distinct/replacement/sibling candidate success,
safe regular root-header support, failure preservation and validation after
HTTPS stops. Full CLI arguments, JSON, stderr, exits, archives and inventories
remain in each probe's scratch directory. All three inventories were
rehashed in full; their compact digests are in the manifest.

[review]: phase1-acquisition-part1-review-02-probes.txt
[fix]: phase1-acquisition-part1-fix-01-probes.txt

## Final checks

Commands ran from `/home/ubuntu/wr` on branch `nextflowdsl`. The manifest
retains the exact red, build, formatter, probe and audit commands as well.

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -v
timeout 30s python3 nextflowconformance/testdata/check_schemas.py
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
timeout 4m python3 .docs/nextflow-conformance/evidence/phase1-acquisition-part1-review-02-probes.txt --binary .tmp/agent/nextflow-conformance/acquisition/fix-part1-02/corrected/wr-nextflow-conformance --scratch .tmp/agent/nextflow-conformance/acquisition/fix-part1-02/green-review02 --original-binary .tmp/agent/nextflow-conformance/acquisition/fix-part1-01/red/wr-nextflow-conformance
timeout 4m python3 .docs/nextflow-conformance/evidence/phase1-acquisition-part1-fix-01-probes.txt --binary .tmp/agent/nextflow-conformance/acquisition/fix-part1-02/corrected/wr-nextflow-conformance --scratch .tmp/agent/nextflow-conformance/acquisition/fix-part1-02/green-fix01
```

- Focused tests exit 0 with all 27 tests passing. The CLI builds and has no
  separate tests. Stock validation passes 11 schemas and 1,190 mutations.
- Both public probes exit 0 against the corrected binary. The original
  replay and the extended Go regression each exit 1 before the correction.
- Lint v2.12.2 exits 1 with exactly 14 deferred findings and zero owned
  findings. `acquirePinned`, `acquireJava`, `copyJavaFile`, `acquireRuntime`,
  `unpackRuntime` and `acquireDependencies` remain byte-identical. Analyzer
  configuration is unchanged. This is not a package-wide lint pass.
- `cleanorder -min-diff` passes for both changed Go files. Lint autofix added
  one blank line in `safeSymlink` and did not alter deferred functions.
- The preservation audit checks 171 file bindings. Only the two intended Go
  files differ. Historical evidence, binaries, provisioned inputs, model,
  CLI, schemas, fixtures, module files, linter configuration, phase and
  progress files retain their original hashes.

A1_05 through A1_07, actual Java/runtime closure, acquisition and acceptance
of a real candidate, A2 selectors and remaining lint belong to part 2. No
Java, Nextflow or wr execution is claimed. Full unrelated wr tests were not
run. No commit, push, phase edit or progress edit was made.
