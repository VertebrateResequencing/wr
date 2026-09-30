# Acquisition part 1 independent review

Verdict: FAIL for the A1_01 through A1_04 split. Two public CLI probes
violate the publication and archive safety contracts. The existing 25 tests
pass, but they omit these cases. Item 1.2 and Phase 1 remain incomplete.
Reviewed on 2026-09-28 at `6121575cb42a3342f483618d71831cafe2e209a3`.

## Blocking findings

### R1: Candidate output can overwrite a verified cache input

Location: `nextflowconformance/source.go:115-133`, specifically the final
`writeJSONAtomic(candidate, lock)` at line 133. The candidate checks in
`nextflowconformance/cli.go:239-250` allow any nonsymlink path inside the
cache, including an input required by the acquired lock.

With a valid three-blob HTTPS fixture, set `--lock-candidate` to
`<cache>/java/release`. Acquisition returns 0, `complete:true`, and three
verified artifacts. Publication replaces that already verified Java file
with candidate JSON. Installing that candidate in the fixture corpus and
running offline `validate` immediately returns 2 with `E_RUNTIME_HASH`.
No Java or Nextflow process runs in this fixture.

The prepublication checks therefore do not establish that the published
candidate still references valid inputs. Reject candidate destinations that
overlap acquired or reused input files and trees before writing. Preserve
those inputs and any prior candidate when rejecting the request. Add a
public-boundary regression for a candidate that aliases a cache input.

### R2: An unsafe archive root symlink bypasses member validation

Location: `nextflowconformance/source.go:1002-1003`. `archivePath` returns an
empty relative name for the top-level member at lines 1428-1430.
`unpackNextMember` returns before the symlink check at lines 1020-1022.

Replace only the pinned source archive's top-level directory header with a
symlink to `../../nextflow-review-outside`. Keep all 2,856 pinned blobs and
all 3,584 tree entries unchanged, and declare the mutated archive's actual
hash and byte count in the fixture lock. Acquisition returns 0 with
`complete:true` and replaces the prior candidate. A1_04 requires exit 2,
`E_SOURCE_PATH`, and preservation of the prior candidate/cache. The probe
writes no outside file; the defect is acceptance of the prohibited member.

Validate symlink targets before omitting root entries. This does not impose
a new requirement to reject safe top-level files or a particular archive
root name. Add a regression with the complete valid pinned tree so that a
later missing-file failure cannot conceal the skipped safety check.

## Exact reproduction

Run from `/home/ubuntu/wr` with the explicitly provisioned test inputs.
The probe uses the built public CLI and its default acquisition HTTP client.
A temporary trusted certificate supplies an independent local HTTPS server.
It changes no production code, target constants, schemas, or decoder rules.

```bash
timeout 2m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go build -tags netgo -o .tmp/agent/nextflow-conformance/acquisition/review-part1/wr-nextflow-conformance ./cmd/wr-nextflow-conformance
timeout 4m python3 .docs/nextflow-conformance/evidence/phase1-acquisition-part1-review-01-probes.txt
```

The probe exits 1 after recording exactly two contract violations,
`candidate-alias-acquire` and `unsafe-root-symlink-acquire`. Each result
retains its exact CLI argument vector, stdout JSON, stderr, exit code,
request paths, input corpus, and complete cache tree. The final run is
`nextflow-probes-6kwpnkpj` beneath
`.tmp/agent/nextflow-conformance/acquisition/review-part1/`.

The [manifest](../evidence/phase1-acquisition-part1-review-01-manifest.json)
binds the source snapshot, commands, logs, probe, and retained artifact
inventories. Earlier probe runs remain on disk. The first had an invalid
review fixture containing API-only tree fields and is not defect evidence.

## Passing checks and limits

- All 25 focused tests pass with Go 1.27.1, `CGO_ENABLED=1`, `-tags netgo`,
  and `-count=1`, including the actual 256 MiB plus one-byte HTTPS response.
- Stock Draft 2020-12 validation passes eleven schemas and 1,190 mutations.
- Lint reports exactly fourteen deferred findings in the six named part 2
  functions, with zero findings in part 1 functions or `source_test.go`.
  Analyzer configuration is unchanged. This is not a package-wide pass.
- Independent public CLI acquisition verifies three blobs. With its HTTPS
  server stopped, offline validation returns 0 and makes no requests.
  A warm-cache HTTP 503 failure returns `E_FETCH`, reports zero verified
  artifacts, makes two attempts, and preserves the full cache snapshot.
  Replacing a source file with a FIFO returns `E_SOURCE_PATH` without a hang.
- Independent hashing verifies all three provisioned inputs, every source
  blob, and every directory object, including the pinned root. All 135
  checked handoff input, command-log, and captured CLI bindings match.
- Of review 05's 33 source, test, fixture, and schema inputs, only
  `source.go` and `source_test.go` changed. Reviewed model, CLI, schemas,
  module files, and linter configuration retain their recorded hashes.

The retained red evidence supports the POM filename, source mode, and
pinned-tree fixes. Fetch-all-failed already passed after fixture repair.
The discarded six-request and safe-root diagnostic assertions establish no
additional contract. Helper extraction follows existing responsibilities;
no separate blocking code-smell finding was identified.

The input estimate was approved before substantive review at approximately
50,234 tokens, with bounded supplemental reads and a 90,000-token working
budget. Generated schema cases and full artifact trees were inspected by
scripts rather than printed into context.

A1_05 through A1_07, actual Java and runtime closure, real POM provenance,
actual candidate acquisition, A2 selectors, and remaining lint belong to
part 2. No actual candidate, accepted lock, phase, oracle, or wr pass is
awarded. The reviewer changed only this review and its evidence. No
production, specification, fixture, checkbox, commit, or push was changed.
