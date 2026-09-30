# Phase 1 full runtime review 02

Verdict: PASS for the runtime split on 2026-09-30. All three findings from
review 01 are corrected. Production candidate acquisition and exact A2
selector acceptance remain open. This verdict awards no Item 1.2, Phase 1,
E1, or wr execution pass.

## Scope and identity

The review covered all 100,024 bytes of the current `source.go`,
`source_test.go`, and `source_companions.go`, and the complete 58,602-byte
runtime diff. Reconstructing the diff from the retained initial snapshots
produced identical bytes. All 79 current source/evidence entries in the
correction manifest and all 11 reference/contract files retain their
recorded hashes. Growth plus complete diff is 63,912 bytes, within the
approved 64,000-byte allocation. No duplicate changed-function extracts
were needed.

The acquisition, transaction, Java inventory, local reuse, opaque packaging,
companion collection, and offline validation paths were reviewed together.
The closed model/schema, module versions, and lint configuration remain
unchanged. No additional correctness or blocking design-smell finding was
verified.

## Corrected findings

1. Offline validation checks acquired tools' execute permissions. It reads
   script shebangs and ELF interpreter headers from verified snapshots and
   checks matching dependency snapshots. It does not use host PATH or the
   mutable provenance sidecar for this decision. The final preflight also
   checks these requirements before publishing a reused generation.
2. `writeFile` restores the requested permission bits after writing. Source
   and Java regular-file modes therefore survive umask 077.
3. Final source passes the existing lint command without autofix, under
   Go 1.26.3 and the unchanged configuration. Source hashes still match the
   post-formatting correction manifest.

An independent Go overlay substituted the two pre-correction production
files while retaining the current tests. Both new regression tests failed
for the expected behavior. Old offline validation and old locked reuse
accepted chmod 0644 on bash, which, a custom script interpreter, and the
dynamic loader. Old acquisition under umask 077 failed with `E_SOURCE_HASH`
and `source mode differs`. The current full test run passes both tests.
These tests remove the provenance sidecar and check transactional failure,
zero execution, and zero requests during offline validation.

A separate current-CLI probe mutated the fresh retained snapshots without
changing their bytes. Bash, which, sh, and the loader each fail validation
with exit 2 and `E_RUNTIME_HASH`. Bash startup fails with exit 126. Libc at
0644 remains valid, and startup succeeds. Restoring all modes restores
successful validation. The regression suite also checks locked reuse with
the custom interpreter and the nonexecuting libc control.

## Independent evidence

All 36 focused tests pass with Go 1.27.1, `CGO_ENABLED=1`, `-tags netgo`,
and `-count=1`, including all seven A1 UATs. The CLI package builds and has
no test files. Stock validation passes all 11 schemas and 1,243 cases. The
fresh acquired lock independently passes its stock schema. The retained
A1_06 run under shell umask 077 also passes.

The fresh fixture contains 282 artifacts: one launcher, one opaque runtime,
249 Java files, 27 environment tools including 15 companions, three POMs,
and one source archive. Every artifact's bytes, hash, size and regular-file
type match. All 277 local observations match source/resolved paths, original
modes and snapshot bytes. The Java inventory has 454 entries. All 2,856
source files match SHA-256, Git blob identity, byte count and original mode;
the extracted inventory has no extra files.

The distribution retains its pinned digest and all 42,355,106 bytes. ZIP
inspection finds 24,898 entries, 1,660 repeated names and no nested JARs.
Acquisition keeps the distribution opaque. XML inspection confirms all
three POM coordinates. The acquired source retains the three relevant
Gradle build files with their original hashes. The runtime closure is
acyclic and contains exactly the recorded execution roles. Independent
script/ELF dependency checks match all 15 probed observations and edges.

Fresh startup and traced startup both report Nextflow 26.04.6 and exit 0.
The existing Linux/amd64 image runs with network disabled, read-only input
mounts and root filesystem, and an empty ephemeral home. The distribution,
Java, tools, interpreters and companion files come from the fresh acquired
snapshots. Every one of the 23 successfully opened shared-library paths was
read inside the container and compared with its acquired bytes. The trace
records no internet connection attempt.

The kernel, VDSO, image symlink layout, OS configuration, virtual filesystems
and temporary JVM performance files remain platform supplied. The trace
audit lists these paths, including loader cache, NSS/passwd, `/proc` and
`/sys` inputs. The existing tracer and its extra libraries are identified
separately as harness inputs. This proves startup on the recorded platform;
it does not establish portability, E1 workflow behavior or a Maven graph.

## Evidence and next bundle

Fresh logs, commands, probe scripts, acquired fixtures and audits are under
`.tmp/agent/nextflow-conformance/runtime-review-02/`. The
[compact manifest](../evidence/nextflow-phase1-runtime-review-02-manifest.json)
binds their hashes, source identities and exact command results. Historical
evidence remains intact. This review made no production edits, phase or
progress updates, commits, or pushes.

The next candidate-bundle sizing reviewer needs these minimal inputs:

- This verdict and its compact manifest, plus accepted model/reuse verdicts
  if the proposed work touches those contracts. Unchanged runtime sources
  can remain hash-bound references.
- `spec.md:70-143`, `spec.md:192-218`, `spec.md:261-366`, and
  `spec.md:367-440`, plus Item 1.2 and phase exit conditions. These define
  acquisition, candidate acceptance, all exact A2 selectors and recursive
  includes.
- The proposed real-CLI acquisition and offline-validation commands, input
  Java identity, output paths, and a measured list of any changed scripts
  or source functions. Keep the candidate distinct from the reviewed lock.
- A measured inventory of the actual pinned selector source files and
  recursively included files, with exact proposed read spans and hashes.
  Account for every selector's nested content, all named grammar
  alternatives, every MixOp test method, and both Gradle dependency regions.
- A proof plan for independent candidate/schema/byte/mode/provenance
  verification, exact selector resolution, ambiguous or missing selector
  failures, offline validation, and required regression gates. Size the
  final acceptance review using the actual candidate and selector evidence.

This report identifies the next inputs; it does not author or approve the
production candidate bundle or accept a lock.
