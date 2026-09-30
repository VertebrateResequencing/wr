# Nextflow runtime implementation handoff

Status: INCOMPLETE. Acquisition tests pass, but the acquired environment-tool
closure omits shared-library files. Runtime acceptance and Item 1.2 remain
open. Continue in a fresh approved closure-correction context, then obtain
independent runtime review. No production candidate was acquired.

## Implemented boundary and checks

The distribution remains one executable, byte-identical opaque file. Pinned
and locked acquisition preserve executable permissions. Java is copied and
hashed before running the copied Java executable for its version. Its
inventory records regular files, symlinks and modes. Local Java/tool records
carry digest origins and observed source, resolved target and snapshot paths.
Three actual POMs have matching coordinates and metadata roles. The complete
source archive retains the pinned build provenance. No runtime members or
invented bundled JAR records are written.

The public A1_06 red failed on the official distribution's repeated
`META-INF/groovy-release-info.properties` entry. The final focused command
passes 32 tests, including all seven A1 UATs. Stock validation passes eleven
schemas and 1,243 baseline cases. Independent stock validation also accepts
the retained fixture lock. Focused lint reports zero findings with unchanged
analyzers; cleanorder passes. No model, schema, module or lint-config edit was
made. The manifest records commands, logs, exits and file hashes.

A traced A1_05-07 run passes. Its only child executable calls are five Java
`-version` probes during acquisition; it starts no Nextflow process. Offline
mutation checks observe zero requests. A1_05 includes actual Java content,
inventory and mode mutations. A1_07 covers prefix, payload, packaging and
replacement-lock-hash mutations.

## Unresolved execution input

Running the acquired distribution by absolute path with acquired Java and
PATH restricted to the acquired tools fails in the existing network-denied
container. The copied awk requires these missing companions:

- `/usr/lib/x86_64-linux-gnu/libsigsegv.so.2.0.7`
- `/usr/lib/x86_64-linux-gnu/libreadline.so.8.2`
- `/usr/lib/x86_64-linux-gnu/libmpfr.so.6.2.1`

The host tool inventory has twelve distinct library files. Its complete
paths, resolved targets, hashes, sizes, modes and ldd results are retained in
`nextflow-tool-libraries.json`; the container probe is separate. Startup exits
1 before Java launch. Neither startup nor execution closure passes.

The caller confirmed that required shared libraries are environment-tool
components under the existing actual-execution-input contract. The next
context may record observed companion files with that role and explicit
dependency edges, without a new role or schema. Preserve original modes and
bytes, enforce regular-file/path checks, and prove the acquired files work
offline on the recorded OS/architecture. Do not infer portability or a
resolved package inventory. No companion acquisition has been implemented.

## Review and continuation inputs

All runtime scratch evidence is under
`.tmp/agent/nextflow-conformance/acquisition-part2/`. Initial source/test
snapshots and the 39-file preservation inventory remain there. The complete
correction is `nextflow-runtime.diff`; `nextflow-runtime-spans.json` maps every
changed complete function/test to initial or supplemental input.

The measured complete changed functions/tests total 26,813 bytes; additional
function input is 19,343 bytes, including two changed existing tests outside
the initial spans. The complete diff is 37,413 bytes. Accepted correction
reads total 18,297 bytes; prior other reads were estimated at 9,000 bytes.
These alone total 84,053 bytes against the 88,000-byte supplemental estimate,
before remaining read/evidence inputs. Companion implementation, tests and
its expanded diff require a fresh approved allocation. Measurements are byte
estimates, not a claim that the model context overflowed.

The retained fixture cache and corpus are under `nextflow-retained/`. Its
lock has 267 artifacts, 454 Java inventory entries and 205 Java symlinks.
`nextflow-fixture-audit.json` binds the lock identity and schema result;
`nextflow-artifact-inventory.json` rehashes every recorded artifact. This is
fixture evidence, not production candidate acceptance.

After closure correction and independent runtime PASS, separately approve
production acquisition, exact A2 selectors/includes, independent candidate
rehashing and candidate offline validation. No E1, wr, phase or Item 1.2 pass,
commit or push is claimed.
