# Acquisition part 1 correction evidence

IMPLEMENTED for review findings R1 and R2 on 2026-09-28. This is the
A1_01 through A1_04 correction only. Item 1.2 and Phase 1 remain incomplete;
the actual runtime, Java closure, candidate and selectors remain part 2.

The [manifest](phase1-acquisition-part1-fix-01-manifest.json) binds the
original and corrected source, binaries, commands, logs and artifact trees.
The [probe](phase1-acquisition-part1-fix-01-probes.txt) runs the built public
CLI with its default HTTP client against an independent trusted HTTPS server.
It uses the existing explicitly provisioned inputs without downloading.

## Corrections

R1 now rejects candidate paths that overlap lock artifact files, extracted
archive trees or the Java tree. The check covers the previous lock before
acquisition and the new lock before publication. It returns `E_SOURCE_PATH`,
matching the existing candidate path contract. Rejection leaves the previous
candidate, input files and complete cache inventory unchanged. New candidate
files inside an input tree also fail. Distinct candidates, candidate
replacement and sibling paths with an input directory's name prefix pass.

R2 now checks a symlink target before omitting a top-level archive header.
The regression changes only that directory header in the complete pinned
archive, retaining all 2,856 blobs and 3,584 Git tree entries. Escaping and
absolute root links fail with `E_SOURCE_PATH`. A safe root link and a safe
top-level regular file still acquire and validate successfully.

Only `nextflowconformance/source.go` and `source_test.go` changed in the
implementation. No shared CLI, model, schema, module or fixture file changed.
No commit, push, phase edit or progress edit was made by this correction.

## Red and green

All scratch evidence is beneath:

```text
.tmp/agent/nextflow-conformance/acquisition/fix-part1-01/
```

The original source and test bytes are retained as `original-source.go.txt`
and `original-source_test.go.txt`. The original public CLI was built before
any source edits. A scratch copy of the immutable reviewer probe changed
only its scratch path.

| Check | Red | Green |
| --- | --- | --- |
| Reviewer R1 | Exit 0, Java release overwritten | Exit 2, input preserved |
| Immediate R1 validation | `E_RUNTIME_HASH` | No invalid candidate published |
| Reviewer R2 | Exit 0, candidate replaced | `E_SOURCE_PATH`, cache preserved |
| Candidate regressions | Seven input overlaps accepted | All seven rejected |
| Root-link regressions | Escaping and absolute links accepted | Both rejected |
| Safe controls | Passed | Passed |

`red-review-probes.log` retains both original CLI failures and their full
JSON, stderr, exits and artifact trees. `red-regressions.log` establishes
the candidate test failures. Its root-header fixture also changed a global
PAX metadata header; that intermediate root result is not defect evidence.
After correcting the fixture to change exactly one directory header,
`red-root-regression.log` records both root-link failures against the
original production source. Its safe layouts pass.

`green-regressions.log` is an intermediate run with the same invalid PAX
fixture and is not green evidence. The final `focused.log` proves all
corrected tests pass. `green-review-probes.log` replays the original reviewer
probe with zero violations. `green-expanded-probes.log` records 23 public
CLI invocations, including eight candidate/input overlap rejections, the
escaping root-link rejection, allowed candidate replacement, safe root
layouts, warm failure preservation and offline validation.

Every rejected overlap compares full cache and corpus snapshots. The cold
Java alias probe retains the release hash
`032413f4266a50ef438a8a19fb5daefec7c52e0067daaab0086b4d780433e2e5`
before and after rejection. The root-link probe preserves the old candidate
and cache and writes no outside file. Successful baseline validation runs
after the HTTPS server stops. No Java or Nextflow process runs.

## Final checks

Commands run from `/home/ubuntu/wr` on branch `nextflowdsl`:

```bash
timeout 10m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go test -tags netgo -count=1 ./nextflowconformance ./cmd/wr-nextflow-conformance -v
timeout 30s python3 nextflowconformance/testdata/check_schemas.py
timeout 5m env GOTOOLCHAIN=go1.26.3 .tmp/agent/bin/golangci-lint run ./nextflowconformance/... ./cmd/wr-nextflow-conformance/...
timeout 4m python3 .docs/nextflow-conformance/evidence/phase1-acquisition-part1-fix-01-probes.txt --binary .tmp/agent/nextflow-conformance/acquisition/fix-part1-01/green-review/wr-nextflow-conformance --scratch .tmp/agent/nextflow-conformance/acquisition/fix-part1-01/green-expanded
```

- Focused tests exit 0 with all 27 tests passing, including the two new
  regression tests. The CLI entry point builds; it has no separate tests.
- Stock schema validation exits 0 for 11 schemas and 1,190 mutations.
- The reviewer replay and expanded public CLI probe both exit 0.
- Lint exits 1 with exactly 14 deferred findings in `acquirePinned`,
  `acquireJava`, `copyJavaFile`, `acquireRuntime`, `unpackRuntime` and
  `acquireDependencies`. Part 1 and tests have zero findings. All six
  deferred functions remain byte-identical. The final output reports an
  existing long line in `acquireRuntime` where the earlier run reported a
  repeated string; the new tests changed the package's literal counts.
- `cleanorder -min-diff` passes and is idempotent on both changed Go files.
  Lint autofix ran with unchanged analyzers and configuration. Its edit to
  deferred runtime code was restored before final tests and lint.
- The preservation audit checked both prior acquisition manifests. Only
  the two intended Go files differ. Historical evidence remains unchanged.
  The broader audit separately records the parent's concurrent progress
  update; this correction did not edit that file.

The manifest retains full commands and compact artifact digests. These
checks do not establish a real acquired candidate, an oracle pass or a wr
execution pass. Full unrelated wr tests were not run.
