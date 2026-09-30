# Phase 1 orchestration handoff

Status: INCOMPLETE on branch `nextflowdsl`, HEAD `6121575c`. No commit or
push was made. All implementation and review agents have stopped writing.
This handoff transfers phase1.md and progress.md ownership back to the
caller. Resume in a fresh orchestration context to keep input bounded.

## Accepted work

Item 1.1 is implemented and reviewed. The original schema review 05 remains
valid for its boundary. A necessary local-origin correction subsequently
passed independent model and locked-reuse reviews:

- `reviews/nextflow-phase1-local-origin-model-review-01.md`.
- `reviews/nextflow-phase1-local-origin-reuse-review-01.md`.

Those paths are relative to `.docs/nextflow-conformance/`. Local origins
are `local:sha256:<digest>`, allowed only for Java and environment-tool
regular-file snapshots with file packaging, null coordinate and a matching
file digest. Lock/tree/network origins remain strict HTTPS. Stock schemas
check syntax and roles; the decoder checks digest equality. Locked acquire
rehashes and retains safe snapshots without fetching or copying. Observed
source and resolved paths remain separate provenance.

Both correction reviews passed the focused regressions and all eleven
schemas with 1,243 cases. The model review passed 112 independent probes;
reuse passed 24 safety failures and six additional atomicity/offline probes.
Part 1 A1_01-04 acceptance and its path/collision/root-link fixes remain
binding. Historical evidence is unchanged.

## Current runtime work

Read `evidence/nextflow-phase1-runtime-incomplete.md` and its manifest first.
They describe implemented opaque acquisition, real Java inventory/version,
local tools, three POMs, provenance, A1_05-07 and lint corrections. This
runtime change has not received independent review or closure acceptance.
Item 1.2 remains unchecked.

The latest run passes 32 focused test functions, all seven A1 UATs, eleven
schemas and 1,243 cases. A traced A1_05-07 run contains five acquisition Java
version probes and no Nextflow process. The orchestrator rehashed every
manifest source/evidence entry without mismatch and counted all 32 passing
test events and no failing events from the retained JSONL log. It separately
ran unchanged lint without autofix; exit 0, zero findings. The log is
`.tmp/agent/nextflow-conformance/acquisition-part2/nextflow-orchestrator-lint.log`.

Offline startup of the acquired distribution, acquired Java and acquired
tools fails before Java launch. The copied awk needs shared libraries absent
from the existing image. The first actual error names `libsigsegv.so.2`;
image inventory also lacks its required `libreadline.so.8` and `libmpfr.so.6`.
The complete host-tool inventory has twelve companion-library files.

The caller authorized recording observed companion files as environment-tool
components with explicit dependency edges. This follows the existing actual
execution-input contract at spec.md:110-129 and 197-207; no new role or schema
was authorized. Preserve observed paths, resolved targets, exact bytes,
modes, hashes and safe regular-file snapshots. Prove the acquired files
work offline on the recorded OS/architecture. No package installation or
invented upstream provenance is allowed. Companion acquisition is not yet
implemented, and no startup/closure pass is claimed.

## Next bounded work

1. Obtain fresh reviewer approval for a closure-correction input bundle.
   The prior runtime context stopped at its allocation before adding the
   twelve companions. Do not mistake a displayed generation budget for a
   measured context limit; the earlier 32,768-context assertion was false.
   Use actual source/evidence bytes, complete changed spans and diff sizes.
2. Delegate closure correction to a fresh Go implementor. Use the failed
   actual startup as red evidence and retain all existing regression gates.
   Keep the reviewed model/schema unchanged unless a demonstrated defect
   requires a separately bounded correction. Stop on a real loader/ABI or
   specification blocker rather than hiding it.
3. Obtain a fresh independent review of the entire runtime split, including
   its existing unreviewed acquisition changes and companion correction.
   Verify real files, provenance, modes, edges and offline execution inputs.
4. After runtime PASS, approve and implement the actual production candidate
   and selector split. Acquire through the real CLI, resolve/hash every exact
   A2 selector/include, independently rehash all candidate inputs and prove
   offline validation. Keep the candidate distinct from the reviewed lock
   until independent acceptance.
5. Only after all obligations pass, check Item 1.2, check prescribed project
   verify-skill location, run final phase CLI/schema/lint gates, inspect the
   complete targeted staged diff and commit `Implement phase 1`. No pushes.
   Phases 2-6 and final global PR reviews remain the caller's later work.

## Precise continuation inputs

Runtime scratch root is
`.tmp/agent/nextflow-conformance/acquisition-part2/`:

- `nextflow-runtime-spans.json`: complete changed functions/tests total
  26,813 bytes, additional types 331 bytes, allocation mapping.
- `nextflow-runtime.diff`: complete 37,413-byte correction against retained
  initial source/test snapshots, including untracked source files.
- `nextflow-tool-libraries.json`: twelve observed files and ldd evidence.
- `nextflow-image-library-probe.json` and `nextflow-startup-command.json`:
  container prerequisites and exact failed invocation; stdout/stderr/exit
  files retain the actual failure.
- `nextflow-retained/`: fixture corpus/cache; lock has 267 artifacts,
  454 Java inventory entries and 205 Java symlinks.
- `nextflow-artifact-inventory.json` and `nextflow-fixture-audit.json`:
  artifact rehashing and stock-schema evidence. This is fixture acquisition,
  not actual production candidate acceptance.
- `nextflow-full.jsonl`, `nextflow-stock.log`, `nextflow-execve.log` and
  the incomplete manifest: complete test, schema, process and command proof.

Current source.go is 44,497 bytes with SHA-256
`af44f785478bed7e1623ffe339be85801d6655cd1216c7c1cc58434c941fcd5a`.
Current source_test.go is 35,210 bytes with SHA-256
`abb9460e8497fd2c7cb863f1fb4478fc0a1efbd43223e851b3cface9e45d6f9e`.

Stable actual input files remain under
`.tmp/nextflow-conformance/test-inputs/`; rehash against the explicit
`evidence/phase1-acquisition-part1-provision.txt` identities. Existing Java
is `.tmp/agent/java21/jdk-21.0.12.1+1`. The corrected historical prerequisite
probe used existing Docker image
`sha256:513c074113a871b51a8d16ab445c88779d6452d937a164fb5cc479f32668a41d`.
That earlier startup used image tools and does not prove acquired-tool
closure. The runtime-incomplete manifest binds the newer failing probe.

Use Go 1.27.1, CGO_ENABLED=1, netgo and count=1 for focused tests. Lint uses
existing `.tmp/agent/bin/golangci-lint` under GOTOOLCHAIN=go1.26.3 with the
unchanged analyzers/configuration. Full unrelated wr tests are outside this
phase. These assurance-tooling UATs make no wr DSL2 execution claim.
