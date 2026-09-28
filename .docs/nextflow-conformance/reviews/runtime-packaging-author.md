# Runtime packaging correction

## Finding and scope

The official Nextflow 26.04.6 distribution matches the accepted target hash
but violates the old spec's generic duplicate-member rejection. It is a
shell launcher plus a shaded JAR, with zero nested JAR members. Requiring
separate parser JARs would describe a different runtime layout.

This correction changes `spec.md` only, plus this evidence report. Target
version, parser v2, source commit, artifact hashes, Java 21, seven real
oracle cases, network denial, and zero wr-runtime passes remain required.
No code, phase file, prompt, or progress file was edited by this author.
No commit or push was made.

## Measured artifact evidence

Downloads and inspection results are retained under
`.tmp/agent/runtime-packaging/`; `inspection.json` records file hashes,
byte counts, ZIP directory observations, and verified Git blob identities.
Each download used HTTPS, a ten-second connection timeout, a sixty-second
total timeout, one attempt, and an explicit size limit. No runtime,
package manager, Gradle resolver, or broad oracle was invoked.

The distribution is 42,355,106 bytes. Its first ZIP local-file header starts
at byte 17,247, after the embedded shell launcher. The directory has 24,898
entries, 23,238 distinct names, and 1,629 names occurring more than once.
`META-INF/groovy-release-info.properties` occurs six times. Duplicate names
also include class files. There are zero names ending in `.jar`.

The manifest selects `nextflow.cli.Launcher`. Entries include
`nextflow/script/parser/ScriptParser.class`, ANTLR runtime classes, PF4J
classes, and Groovy classes. Embedded Maven properties identify
`me.sunlan:antlr4:4.13.2.6`, `me.sunlan:antlr4-runtime:4.13.2.6`, and
`org.pf4j:pf4j:3.14.1`. These observations establish packaging, not a complete
Maven build-resolution inventory or a successful oracle run.

Python's ZIP reader rejected one duplicate metadata read as overlapping
entries. Directory enumeration and selected unique metadata reads succeeded.
No archive member was extracted into the filesystem. This reinforces the
need to retain exact runtime bytes rather than impose extraction semantics
or reconstruct the JAR. Java execution remains an E1 gate, not a research
claim.

```tsv
Artifact	Bytes	SHA-256	Source key
antlr4.pom	11092	1ba04a4da8e5a2acf0ac6eece4df82280bcd6a91f44707bb0b4b9c8d73f8da32	antlr
build.gradle	19121	1cdd9f6ff6d6f2527905a9c5a4d13177da98118f32109e8f33c38b8bd694f269	build
groovy.pom	24114	ef7fc8e69aeeb2c1d4ed4df1bcdb5fd82ecfacf9fd7574bee0f89b9984d74bda	groovy
nextflow	17246	61a755edbed743cfbb568f3a6c67af68481a2f6a4d6dffcc4295e51318968281	launcher
nextflow-26.04.6-dist	42355106	182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c	dist
nextflow.gradle	4375	b37eb7f92676f34f2df9f1d4e1116f8cffd4d53a4019911d43dee1b7ac5a4f9f	nextflow-build
nf-lang.gradle	1067	a6a75a69da8fdf5d2612fcb0c39f78bd6a9feda32761bf895ef5b4ef231d7d01	lang
packing.gradle	12947	ec95da76448e62274afece948a883c9eb3589ae3f2c9ba6c05ceb7e9a8a2d34e	packing
pf4j.pom	7555	95c5843942717af5cdfe5188b98dec0034439e80978be0ef67637ea684154818	pf4j
```

## Upstream construction and dependency data

At the pinned source commit, `packing.gradle`'s `packDist` writes the
launcher with `NXF_PACK=dist`, then appends the `shadowJar` output.
`modules/nextflow/build.gradle` uses Shadow 9.3.1 and combines
`runtimeClasspath` with `lineageImplementation`; it merges service and
Groovy extension metadata. Root `build.gradle` sets JAR duplicate handling
to `INCLUDE`. These build-file bytes match the Git blob IDs in the pinned
commit's complete, non-truncated recursive tree response.

The embedded launcher resolves its own path as `NXF_BIN` in dist mode and
invokes Java with that file. The separate release launcher defaults to
`NXF_PACK=one` and can download another artifact if invoked without a
prepared runtime. The corrected contract runs the distribution's embedded
launcher and retains the separate launcher only as acquired provenance.

The three declared nf-lang POMs were downloaded and hashed. ANTLR declares
ANTLR runtime and annotation dependencies at the project version; PF4J uses
parent-defined versions for some dependencies; Groovy declares optional
runtime dependencies. A fresh POM traversal or the declarations alone would
not establish the dependency resolution used to create the released shaded
bytes. The acquired whole distribution covers all embedded classes,
including transitive dependencies, without inventing standalone JAR hashes.

## Changed contracts

- Acquired runtime bytes are one `opaque-dist` artifact. Validate the fixed
  target hash and byte count; never extract, deduplicate, or repackage it.
- `artifacts` gains `packaging` and nullable Maven `coordinate` fields.
  Dependency POMs have role `dependency-metadata`; runtime dependency edges
  name actual external execution files, including Java and required tools.
- Extracted archives still reject unsafe paths, symlinks, and duplicate
  destinations transactionally. The exception is no member extraction for
  the exact opaque runtime, not collision suppression during extraction.
- A1_04 explicitly rejects duplicate extracted destinations. A1_05 removes
  the actual distribution instead of an imaginary parser JAR. New A1_06
  requires acquiring the real distribution unchanged with its repeated
  names. New A1_07 rejects prefix/payload mutations and target-hash or
  packaging-label bypasses before execution, with zero network requests.
- D2_01 includes distribution changes in stale-evidence checks. E1 runs the
  embedded launcher and retains enforced offline execution of all seven
  cases. Acquisition success alone does not satisfy that execution gate.

## Affected phases and validation

The spec now has exactly 49 unique numbered acceptance IDs. Its C2 mapping
rule and final gate name that total. Existing IDs retain their meaning except
for the packaging corrections above.

- Phase 1 needs the new artifact fields, actual execution closure,
  distinction between extraction and opaque verification, revised A1_04/05,
  and new A1_06/07. Its five-test count must become seven.
- Phase 3's import, review, and execution-boundary totals must become 49;
  include A1_06/07 in the ledger and generated test bindings.
- Phase 4 must bind freshness to whole distribution bytes and the actual
  external execution closure.
- Phase 5 must invoke the distribution's embedded launcher and retain all
  existing E1 proof obligations.
- Phase 6's final acceptance-ID total must become 49. Final completion still
  needs all oracle cases, adversarial mutations, reviews, and current hashes.

Checks passed for ASCII, 80-column prose, whitespace, fenced block languages,
49 unique acceptance IDs, and `git diff --check` on the spec. Independent
review and affected phase updates remain with the caller. No acquisition CLI
success or Nextflow execution is claimed by this report.

## Source URLs

All GitHub source URLs below use the accepted full commit. Maven artifacts
use versioned repository paths and the measured hashes above.

```tsv
Key	URL	Role	Revision
launcher	https://github.com/nextflow-io/nextflow/releases/download/v26.04.6/nextflow	release asset	26.04.6
dist	https://github.com/nextflow-io/nextflow/releases/download/v26.04.6/nextflow-26.04.6-dist	release asset	26.04.6
build	https://raw.githubusercontent.com/nextflow-io/nextflow/232b60569865e9a4577e48c1955409238359d6ca/build.gradle	source	232b60569865e9a4577e48c1955409238359d6ca
packing	https://raw.githubusercontent.com/nextflow-io/nextflow/232b60569865e9a4577e48c1955409238359d6ca/packing.gradle	source	232b60569865e9a4577e48c1955409238359d6ca
lang	https://raw.githubusercontent.com/nextflow-io/nextflow/232b60569865e9a4577e48c1955409238359d6ca/modules/nf-lang/build.gradle	source	232b60569865e9a4577e48c1955409238359d6ca
nextflow-build	https://raw.githubusercontent.com/nextflow-io/nextflow/232b60569865e9a4577e48c1955409238359d6ca/modules/nextflow/build.gradle	source	232b60569865e9a4577e48c1955409238359d6ca
antlr	https://repo.maven.apache.org/maven2/me/sunlan/antlr4/4.13.2.6/antlr4-4.13.2.6.pom	POM	4.13.2.6
groovy	https://repo.maven.apache.org/maven2/org/apache/groovy/groovy/4.0.31/groovy-4.0.31.pom	POM	4.0.31
pf4j	https://repo.maven.apache.org/maven2/org/pf4j/pf4j/3.14.1/pf4j-3.14.1.pom	POM	3.14.1
```
