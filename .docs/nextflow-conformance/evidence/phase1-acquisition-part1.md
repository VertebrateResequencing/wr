# Acquisition part 1 handoff

Status: IMPLEMENTED for the approved A1_01 through A1_04 split, awaiting
independent review. Item 1.2 and Phase 1 remain incomplete. No actual
acquisition candidate, accepted lock, Nextflow execution, or wr pass is
claimed. No shared schema, CLI, module, lint configuration, phase checkbox,
commit, or push was changed by this implementor.

## Scope and changes

The [approved bundle](../reviews/phase-01-acquisition-bundle.md) permits this
split. Changes are confined to `nextflowconformance/source.go`, its tests,
and new evidence. Runtime acquisition, Java closure, dependency provenance,
and A2 selectors belong to part 2.

Locked acquisition preserves artifact filenames, including the required
POM suffix. Before publication, the complete generated lock passes the
reviewed decoder, offline file checks, and cancellation check. Failed stages
are removed without replacing a previous candidate or cache generation.

Source verification checks Git file modes, bytes, hashes, parent paths,
tree inventory, and selected-file size. Tree verification binds the root
SHA to the pinned commit before publication and checks every
listed directory, including empty trees and missing parents. Artifact
verification rejects nonregular files and streams hashes. Extracted archives
retain traversal, absolute-path, duplicate-destination, symlink, entry, and
size checks. The opaque fixture distribution is never extracted.

The source, transaction, tree, archive, and request functions were split
where needed to meet the existing complexity limits. Analyzer settings are
unchanged. No reviewed record rule or production target identity was relaxed.

## Explicit test prerequisites

The three HTTPS fixture blobs are the actual pinned source archive, the
actual opaque distribution, and an independently authored fixture POM.
The POM has a fixture coordinate and makes no parser-dependency claim. The
Java tree contains labelled inert fixture bytes; no Java process runs in
these tests. The complete actual tree has 3,584 entries and 2,856 blobs.
Fixture construction independently hashes every source blob before use.

Inputs live in `.tmp/nextflow-conformance/test-inputs/`, outside source
control. Tests accept `NEXTFLOW_CONFORMANCE_TEST_INPUTS` as an alternate
input directory. Missing inputs fail explicitly. Tests never download or
skip unavailable inputs. A clean checkout needs explicit provisioning:

```bash
timeout 8m python3 .docs/nextflow-conformance/evidence/phase1-acquisition-part1-provision.txt
```

The [provisioning script](phase1-acquisition-part1-provision.txt) records
exact HTTPS URLs, sizes, hashes, bounded curl commands, reuse, and exits in
`nextflow-provisioning.json` under that cache. It validates reused bytes and
preserves previous files if replacement download or hash verification fails.
This is fixture provisioning, not successful production `acquire` evidence.

| Input | Bytes | SHA-256 |
| --- | ---: | --- |
| `nextflow-source.tar.gz` | 8,094,680 | `f1c889c683094cbba5eda8fab701dac48624253f39c41a8e5361729f14c6ba11` |
| `nextflow-tree.json` | 1,138,642 | `b5c8fbd548c057b40c136eccc2dce8ab252129f0f2ca21e9a2385f7d61540ea4` |
| `nextflow-26.04.6-dist` | 42,355,106 | `182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c` |

Source and tree were fetched from their immutable commit and tree URLs.
The retained research distribution was independently rehashed before reuse.
The research tree response names the commit in its envelope; the fixture
instead fetched the explicit pinned tree-object URL. No identity was changed.

## Red and green evidence

All logs are under `.tmp/agent/nextflow-conformance/acquisition/part1/`.
The [manifest](phase1-acquisition-part1-manifest.json) binds exact commands,
exits, input hashes, and retained evidence.

- `red-boundary.log` records the valid-fixture candidate rejection. The old
  code published a POM path without `.pom`, which the reviewed decoder
  rejected. Preserving the filename and decoding before publication fixes
  this integration defect.
- `red-tree-identity.log` records a self-consistent replacement tree that
  incorrectly published a candidate with exit 0. `green-tree-identity.log`
  proves the pinned root check now rejects it with `E_TARGET_IDENTITY`.
- `red-mode.log` records source mode `100644` changed to executable. The old
  offline validation returned 0; it now returns 2 with `E_SOURCE_HASH`.
- `fetch-baseline.log` proves that fetch-all-failed already returned 2 with
  `E_FETCH`, zero verified artifacts, no publication, and preserved bytes
  after obsolete fixture records were corrected. No fetch behavior fix was
  needed. An intermediate assertion demanding six requests was discarded;
  the contract permits fail-fast behavior. Its log is retained separately
  as `overstrict-fetch-red.log`, not acceptance evidence.
- The early archive-root diagnostic assertion in `red-boundary.log` was
  also discarded. A safe top-level member does not require `E_SOURCE_PATH`.
  Final tests cover the specified unsafe archive cases without imposing a
  GitHub-only root-prefix contract.

| Acceptance | Result at this boundary |
| --- | --- |
| A1_01 | PASS: three verified blobs, valid candidate, offline validation after stopping HTTPS |
| A1_02 | PASS: all failures, one failed among two successful blobs, and failure after a successful cache generation |
| A1_03 | PASS: byte, missing-file, commit, truncated-tree, accounting, mode, and extra-file mutations |
| A1_04 | PASS: traversal, absolute path, duplicate destination, escaping symlink, and size failure with preservation |
| A1_05 | Existing adapted fixture passes; complete runtime proof remains part 2 |
| A1_06 | Not implemented; actual acquisition remains part 2 |
| A1_07 | Not implemented; runtime identity mutations remain part 2 |

A separate HTTPS stream serves the production object ceiling plus one byte,
268,435,457 bytes. It returns `E_SOURCE_LIMIT` after one request and preserves
the cache. Cancellation and candidate-path safety also pass. The warm-cache
A1_02 case compares every previous file's hash, mode, and path, including its
candidate and full acquired generation. Fixture candidates are temporary
and are not an actual acquisition candidate or accepted checked-in lock.

## Final checks

Run from `/home/ubuntu/wr` at `6121575cb42a3342f483618d71831cafe2e209a3`.
Commands and results:

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -v
timeout 30s python3 nextflowconformance/testdata/check_schemas.py
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
```

- Focused tests exit 0. All 25 Go tests pass, including the sixteen reviewed
  Item 1.1 tests. The developer entry point builds and has no separate tests.
- Stock Draft 2020-12 validation exits 0 for all eleven schemas and 1,190
  independent mutations. Schema bytes and Item 1.1 implementation remain
  unchanged.
- Lint exits 1 with exactly fourteen part 2 findings, all in `acquirePinned`,
  `acquireJava`, `copyJavaFile`, `acquireRuntime`, `unpackRuntime`, and
  `acquireDependencies`. Part 1 functions and the entire test file have zero
  findings. `nextflow-lint-ownership.json` records each finding. This is not
  a package-wide lint pass. The binary is v2.12.2 with unchanged analyzers.
- `cleanorder -min-diff` exits 0 for both changed Go files. Lint autofix ran;
  the final lint command above made no edits. The preservation audit checked
  all 33 source/test/schema inputs in review 05's manifest. Only `source.go`
  and `source_test.go` changed.

`nextflow-cli/` retains each captured command's full JSON, stderr, and exit.
The fixture servers are stopped before offline validation. Test results do
not claim actual Java or Nextflow execution. Full unrelated wr tests were
not run.

## Part 2 handoff

After independent part 1 review, use a fresh implementor and reviewer under
the approved split. Preserve the source/transaction tests and closed schema
contract. Complete these remaining obligations:

1. Replace `unpackRuntime` and nested-JAR assumptions with opaque byte
   retention. Complete packaging and coordinate fields in generated records.
   Apply runtime executable permissions in both pinned and locked acquisition.
2. Hash the existing real Java 21 tree and required environment tools. Verify
   its executable and symlinks before execution. Represent only genuine
   external execution files in the runtime closure. Retain the separate
   launcher and build provenance; acquire all three specified POMs as
   `dependency-metadata`, never invented runtime JARs.
3. Complete A1_05 and add public-boundary A1_06 and A1_07. Check missing
   runtime, Java changes, shell-prefix and payload changes, packaging-label changes,
   replacement-lock-hash attempts, zero requests, and no Nextflow process.
4. Acquire the actual complete candidate with the existing Java home. Record
   the real CLI JSON, stderr, exit, candidate path/hash, measured artifact
   identities, and successful offline preflight. No actual candidate exists
   from this part. Keep it separate from an accepted checked-in lock.
5. Resolve and hash every exact A2 selector and recursive local include in
   the approved bundle. Missing or ambiguous selectors block acceptance.
   Extraction and semantic review remain A2 work; Nextflow execution remains E1.
6. Remove the fourteen remaining lint findings. Rerun all focused tests and
   the stock schema checker, then submit all seven UAT results and the actual
   candidate for independent byte rehash and review.
