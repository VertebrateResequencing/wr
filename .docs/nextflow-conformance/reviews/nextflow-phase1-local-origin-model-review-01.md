# Local-origin model review 01

PASS for the local-origin model and schema correction. No blocking findings
or code-smell suggestions. This verdict does not cover locked reuse,
snapshot safety, public acquisition failures, or actual Java/tool execution.
Those checks remain with reuse and part 2. It does not complete phase 1.

## Contract review

`nextflowconformance/model.go:237` keeps origin a required string.
`validArtefactOrigin` at line 809 accepts the existing HTTPS grammar or the
exact lowercase local digest syntax. `checkArtefactOrigin` at line 261
restricts local origins to Java and environment-tool, file packaging, null
coordinate, and equality with `file.sha256`. Lock and tree origins retain
the HTTPS rule. Existing coordinate, pinned runtime, and closure checks
remain in place.

`nextflowconformance/schema.go:324` adds the matching role, packaging, and
coordinate condition. The scalar rule at line 366 preserves the previous
HTTPS pattern in its first alternative. The generated schema adds only that
condition, the artifact-origin union and annotation, and the explicit
decoder-only digest-equality annotation. Closed shapes and required fields
are unchanged. Stock validation intentionally cannot compare the two hashes.

All eight changed or new functions, both complete fixtures, and all 53 added
common cases were reviewed. The five forbidden roles have valid HTTPS
controls. The four decoder-only mismatch mutations independently change the
origin or file digest for each allowed role. Tests exercise record decoding
and stock validation without bypassing their validators.

## Independent verification

| Check | Result |
| --- | --- |
| Focused Go model, CLI, closure and security regressions | Exit 0 |
| Stock schemas and common mutation corpus | 11 schemas, 1,243 cases pass |
| Reviewer adversarial probes | 112 pass in decoder and stock checks |
| Baseline decoder replay | Expected exit 1 on valid local origin |
| Baseline stock replay | Rejects both allowed-role local origins |
| Focused lint | Exit 1, 14 deferred source.go findings, zero owned |
| Complete correction reverse-apply check | Exit 0 |

The reviewer probes transfer all 33 existing HTTPS origin cases to each
allowed artifact role. They also cover control and Unicode suffixes and
prefixes, URI suffixes, malformed hashes, encoded digits, uppercase
algorithms, and matching zero and mixed lowercase hexadecimal digests.
Exactly four cases are accepted by stock schema and rejected by the decoder
with the digest-specific error, as the schema annotation states.

Go checks used Go 1.27.1, CGO_ENABLED=1, netgo, count=1, and a ten-minute
timeout. Stock checks used a two-minute timeout. Lint used the existing
binary with Go 1.26.3 and a ten-minute timeout. The CLI package has no test
files; its invocation contract is exercised by the package tests.

The baseline decoder replay uses a Go overlay for the old model and schema.
It substitutes the equivalent literal for the new test's `sha256Field`
constant so the old package compiles. The test then fails because the old
HTTPS-only rule rejects the local origin. Production files were not changed.

The 14 lint findings exactly match the implementor's final lint output.
Their source file hash is unchanged. The approved bundle defers these
findings to part 2; lint is not reported as globally clean.

## Evidence

The compact [evidence manifest][e] records this review's results.
It records source hashes, command exits, logs, and review evidence hashes.
Full logs, the independent audit, probe records, and Go overlays are under
`.tmp/agent/nextflow-conformance/local-origin-model-review-01/`.

The audit verifies all 1,190 baseline cases remain unchanged and in order,
the 53 additions match the supplied review input, all case names are unique,
the other ten schemas are byte-identical, and all eight function spans and
sizes match the current files. It also checks the exact generated-schema
delta and retained module, lint, acquisition, and CLI hashes. The complete
seven-file correction applies in reverse without modifying the workspace.

No production fixes, specification edits, commits, or pushes were made.

[e]: ../evidence/nextflow-phase1-local-origin-model-review-01-manifest.json
