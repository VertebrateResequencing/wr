# Local-origin locked reuse implementation

IMPLEMENTED for independent review. Locked acquisition rehashes accepted
local Java and environment-tool snapshots at their existing paths. The
artifact verifier checks path safety, regular-file status, size, and digest.
The returned artifact retains its path, origin, and dependency edges. HTTPS
acquisition keeps its existing bounded fetch and staging path.

The production correction adds eight lines to `acquireLockedArtifact`.
The public CLI already rejects unsafe cache boundaries and candidate input
collisions. Its validation and publication callers required no changes.
The existing snapshot test helper now records symlink targets without
following them, so failure checks can compare unsafe test trees safely.

## Verification

| Check | Exit | Result |
| --- | ---: | --- |
| Final red replay against initial source | 1 | Local fetch defect reproduced |
| Full focused package and CLI tests | 0 | 30 test functions pass |
| Stock schema checks | 0 | 11 schemas and 1,243 cases pass |
| Persistent acquisition and offline probe | 0 | Both local roles retained |
| Final lint | 1 | Exactly 14 baseline diagnostics, zero owned |
| Complete diff reverse-apply check | 0 | All three source files covered |
| Artifact and protected-file audit | 0 | Hashes, modes, model unchanged |

Go commands used Go 1.27.1, CGO_ENABLED=1, netgo, count=1, and a ten-minute
bound. Lint used the existing binary and Go 1.26.3. Stock checks had a
two-minute bound. A1_01-04 and every existing package regression passed.
The CLI package has no test files; the package tests call its CLI boundary.

The success case reads the published candidate and actual retained files.
It checks paths, hashes, lengths, mode 0751, file identity, dependency edges,
three HTTPS requests, and zero local transport requests. Validation passes
after the fixture server stops. These are inert Java/tool fixture files.
No Java or environment tool was executed.

The 24 failure cases cover both roles across missing files, changed contents,
changed lengths, directories, final symlinks, symlink ancestors, escaped
paths, absolute paths, unequal origin/file digests, symlink cache roots,
symlink cache ancestors, and candidate collisions. CLI output, stderr,
exit, error codes, request counts, corpus/cache contents and modes are
captured. Digest mismatch returns E_INPUT before requests. Candidate
collisions also fail before requests.

The first red exposed an unsorted test dependency list. After fixing that
fixture, red reproduced the intended HTTPS rejection. Two initial green
attempts expected three requests for a candidate collision; the existing
input guard correctly rejected it before fetching. Final tests assert zero.
All logs and validation commands, including these failed attempts, remain
under `.tmp/agent/nextflow-conformance/local-origin-reuse/`.

## Review inputs

The [manifest][manifest] records source and evidence hashes. In the evidence
directory, `changed-functions.json` gives every complete function span;
`changed-functions.txt` contains all seven functions, including both complete
tests and their 25 cases. `correction.diff` includes untracked source files
and compares the initial snapshots with the final source. It is 10,464 bytes.

`provenance.json` separately records observed source, resolved target,
snapshot path, origin identity, independently measured hash, length, mode,
and inode. The two retained files and candidate are under `retained/`.
The probe overlay only replaces the fixture's temporary root allocation
with persistent repository directories. Acquisition and validation use the
same implementation and success test. `evidence-fixture.diff` shows the
complete overlay change without rereading the unchanged fixture code.

`audit.py` checks those actual files, the exact lint baseline, and all seven
accepted model files. `commands.json` contains validation commands, exits,
log paths, and hashes. Full logs need not be initial review inputs.

Actual Java/tool acquisition, opaque runtime handling, POMs, A1_05-07,
actual candidate/selectors, and completion of phase 1 remain with part 2.
No model, schema, specification, production wr, module, or lint configuration
changes were made. No commits or pushes were made.

[manifest]: ../evidence/nextflow-phase1-local-origin-reuse-manifest.json
