# Acquisition part 1 correction review

Verdict: FAIL for corrected A1_01 through A1_04 on 2026-09-28. R1 is fixed,
and the reported unsafe-root R2 cases are rejected. The R2 correction also
rejects safe root links that the previous implementation accepted. Item 1.2
and Phase 1 remain incomplete.

## Blocking finding

### R3: Root symlinks resolving to the extraction root are rejected

Location: `nextflowconformance/source.go:1029-1031`, calling
`safeSymlink` at `source.go:481-484` with an empty member name.

Replace only the pinned archive's top-level directory header with a symlink
to `.`, `./`, or `safe/..`. Keep all 2,856 source blobs and 3,584 tree entries
unchanged, and declare the modified archive's actual hash and size. The
freshly built public CLI returns 2 with `E_SOURCE_PATH` for each case. The
retained pre-correction binary returns 0, reports three verified artifacts,
and offline-validates each resulting candidate. The probe independently
compares every original source blob with every mutated archive's contents.

These targets resolve to the extraction root, not outside it. The new call
passes `name == ""`; `path.Clean(path.Join(path.Dir(name), target))` becomes
`.`. `safeRelative` rejects `.`, because that predicate validates record
paths rather than symlink containment. Reusing it here rejects a valid
boundary. Archive layout is not source identity, and the correction must
preserve safe root layouts while rejecting links that escape.

Allow a root link's resolved destination to equal the extraction root.
Retain rejection of absolute targets, parent escapes, and other unsafe link
syntax. Add public-boundary regressions for `.` and `./` using the complete
pinned tree. The retained `safe/..` case also exercises normalization.
The independent absolute and parent-escape controls already return
`E_SOURCE_PATH`; `safe/../child` still acquires and validates successfully.
All three rejected safe cases preserve the previous candidate and cache.

No additional blocking code-smell finding was identified. The candidate
input helper centralizes the previous/new-lock check without an additional
abstraction. The root regression is a correctness issue in the shared
path predicate's use.

## Reproduction and evidence

Run from `/home/ubuntu/wr` with the provisioned test inputs. The probe uses
an independent trusted HTTPS server and the public binary's default client.
It neither downloads test inputs nor modifies production code or records.

```bash
timeout 2m env GOTOOLCHAIN=go1.27.1 CGO_ENABLED=1 go build -tags netgo -o .tmp/agent/nextflow-conformance/acquisition/review-part1-02/wr-nextflow-conformance ./cmd/wr-nextflow-conformance
timeout 4m python3 .docs/nextflow-conformance/evidence/phase1-acquisition-part1-review-02-probes.txt --binary .tmp/agent/nextflow-conformance/acquisition/review-part1-02/wr-nextflow-conformance --scratch .tmp/agent/nextflow-conformance/acquisition/review-part1-02/boundary --original-binary .tmp/agent/nextflow-conformance/acquisition/fix-part1-01/red/wr-nextflow-conformance
```

The probe exits 1 after 26 CLI invocations. Its three contract violations
are `root-dot-current`, `root-dot-slash-current`, and
`root-normalized-dot-current`. Full argument vectors, stdout, stderr, exits,
archives, corpus records, cache trees, and inventories remain in
`review-part1-02/boundary/nextflow-probes-cs68xspg` beneath
`.tmp/agent/nextflow-conformance/acquisition/`. Each rerun creates a new
scratch directory. The [manifest][manifest] binds exact commands, logs,
source snapshots, probe bytes, input audits, and bounded artifact digests.

[manifest]: ../evidence/phase1-acquisition-part1-review-02-manifest.json

## Passing checks and limits

- All 27 focused Go tests pass with Go 1.27.1, `CGO_ENABLED=1`, `-tags netgo`,
  and `-count=1`. The CLI builds. Stock validation passes all eleven schemas
  and 1,190 independent mutations.
- The implementor's expanded public probe passes all 23 invocations using
  the fresh binary and a new scratch directory. Both historical blockers
  are rejected with full preservation. Distinct/replacement/sibling
  candidates and its safe root-link/regular-file controls pass.
- Independent candidate probes reject the cache root, Java root, source
  tree root, artifact parent, normalized Java-file alias, and symlinked
  output paths. They preserve the full cache and corpus. Atomic replacement
  of a hardlinked candidate preserves the original artifact and produces a
  candidate that validates. Final validation succeeds after HTTPS stops.
- Lint exits 1 with exactly fourteen deferred findings in six byte-identical
  part 2 functions: `acquirePinned`, `acquireJava`, `copyJavaFile`,
  `acquireRuntime`, `unpackRuntime`, and `acquireDependencies`. There are zero
  owned findings. Analyzer configuration is unchanged; this is not a
  package-wide lint pass.
- Of the previous review's 42 bound inputs, only `source.go` and
  `source_test.go` changed. All 38 correction-manifest file bindings match.
  The complete correction diff and affected callers/tests were reviewed;
  earlier reviewed model, CLI, schema, module, and fixture rules retain
  their recorded hashes. Historical evidence was preserved.

The bounded correction bundle was approved before substantive review under
the 90,000-token working budget. Large records and artifact trees were
checked with scripts, not loaded as review context. This review changes only
its report, new probe, manifest, and scratch evidence. No source, spec,
fixture, module, linter, phase checkbox, commit, or push was changed.

A1_05 through A1_07, real Java/runtime closure, actual acquisition, candidate
acceptance, A2 selectors, and remaining lint stay with part 2. No phase,
oracle, or wr pass is awarded. Full unrelated wr tests were not run.
