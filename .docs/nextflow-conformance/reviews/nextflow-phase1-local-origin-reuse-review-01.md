# Local-origin locked reuse review 01

PASS for locked local-origin snapshot reuse. No blocking findings or
code-smell suggestions. This verdict covers inert snapshot fixtures. Actual
Java/tool acquisition and execution remain part 2 work; phase 1 is incomplete.

## Contract and implementation

`nextflowconformance/source.go:1280-1300` adds eight lines to
`acquireLockedArtifact`. A local origin uses the existing artifact verifier
and returns the same artifact. The verifier checks path containment, symlink
components, regular-file status, length, and SHA-256. Returning the artifact
preserves its path, origin, and dependency edges without copying or fetching.
HTTPS artifacts retain their existing bounded fetch and staging path.

The CLI checks cache and candidate boundaries before acquisition. Candidate
input protection and publication preflight remain unchanged. The public
decoder rejects unequal local-origin and file digests before requests.
All seven accepted model/schema files match the model review hashes.

The complete correction includes untracked files and matches an independently
reconstructed diff. Its reverse-apply check passes without changing files.
The seven complete changed functions were reviewed at these current spans:

- `source.go:1280-1300`, locked acquisition.
- `source_test.go:389-431`, snapshot comparison including symlink targets.
- `source_local_test.go:53-83`, fixture; `85-91`, request observation;
  `93-143`, successful reuse; `145-189`, failure cases;
  and `191-243`, mutations.

## Independent verification

| Check | Exit | Result |
| --- | ---: | --- |
| Full focused Go package and CLI tests | 0 | 30 test functions pass |
| Stock schema checks | 0 | 11 schemas, 1,243 cases pass |
| Original acquisition source replay | 1 | Expected HTTPS rejection |
| Reviewer public CLI probes | 0 | Six cases pass |
| Unchanged lint analyzers | 1 | Exact 14 deferred diagnostics, zero owned |
| Independent audit and reverse-apply check | 0 | Source and evidence match |

Go used 1.27.1, CGO_ENABLED=1, netgo, count=1, and a ten-minute timeout.
The full suite completed in 127.261 seconds. Lint used the existing binary
with Go 1.26.3 and a ten-minute timeout, without autofix. Schema validation
used a two-minute timeout. The CLI package has no test files; package tests
invoke its public command boundary. A1_01-04 and existing security tests pass.

The successful acquisition reads the candidate and retained files. Both local
roles retain path, inode, mode 0751, hash, size, and runtime dependency edges.
The test observes three HTTPS requests and zero local requests. Validation
passes after the fixture server closes. Independent Python rehashing confirms
both retained files, their recorded provenance, candidate entries, and edges.
Their observed source and resolved target are the actual snapshot paths; the
local digest identity does not stand in for an upstream URL.

All 24 failure cases pass at the CLI boundary. They cover both roles across
missing files, altered contents and lengths, directories, final and ancestor
symlinks, escaped and absolute paths, unequal digests, cache root and ancestor
symlinks, and candidate collisions. Logs retain exit codes, JSON, stderr,
diagnostics, and request counts. Assertions compare corpus/cache contents,
modes, prior candidate, and outside paths after failure.

Four independent probes place each local role after the HTTPS artifacts,
then corrupt or remove its snapshot. All observe three HTTPS requests before
failure and prove complete staging cleanup with the old cache, candidate,
and corpus unchanged. Two further probes corrupt an accepted local snapshot
after stopping the server. Offline validation rejects each role with
E_RUNTIME_HASH, without more requests or writes.

The first reviewer probe fixture violated sorted artifact IDs and correctly
failed with E_INPUT. The corrected fixture uses later-sorting local IDs and
matching runtime edges. Both probe logs remain in the evidence directory.
The production source was unchanged throughout review.

## Evidence

The [review manifest][manifest] records commands, exits, source hashes, and
evidence hashes. Full logs, the runnable independent audit, probe additions,
and their Go overlay are under
`.tmp/agent/nextflow-conformance/local-origin-reuse-review-01/`.
The audit verifies complete diff coverage, seven function spans, retained
files and provenance, accepted model hashes, all focused results, and the
exact lint baseline. It also verifies every Nextflow source/fixture file
remained unchanged during review.

No production fixes, specification or progress edits, commits, or pushes
were made. Downloaded permissions, opaque runtime handling, POMs, actual
candidate/selectors, and A1_05-07 remain part 2 work.

[manifest]:
  ../evidence/nextflow-phase1-local-origin-reuse-review-01-manifest.json
