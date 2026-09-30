# Item 1.2 acquisition part 2 input bundle

Verdict: APPROVED for input size and scope on 2026-09-28. Use one fresh
implementor and a separate fresh independent reviewer. This review awards
no implementation, test, acquisition, candidate, or phase pass. Part 1
passed `phase-01-acquisition-part1-review-03.md`; Item 1.1 remains closed.

## Scope and budget

Complete A1_05 through A1_07, actual Java and external execution inputs,
opaque runtime acquisition, dependency provenance, an actual candidate,
exact A2 selector preparation, offline validation, and remaining acquisition
lint. Preserve the accepted A1_01 through A1_04 behavior and schema contract.
A2 extraction and semantic review, E1 workflows, and wr execution remain
later work.

Each implementation or review context has this total budget:

| Use | Token cap |
| --- | ---: |
| Initial spans, role skills, instructions, and this bundle | 48,000 |
| Supplemental reads, changed-source growth, and final diff | 10,000 |
| Command output and evidence summaries | 6,000 |
| Reasoning, edits, and final handoff | 26,000 |
| Total | 90,000 |

The listed file spans and ten applicable skill files total about 160 KB,
or 40,000 tokens at four bytes per token. This is a byte-based estimate,
not measured model usage. The initial allowance includes headroom for this
bundle and instructions. Read only the assigned role skill; the estimate
uses the larger reviewer skill and includes reviewer-only code smells.
The roughly 100,000-token ceiling is not a target. Split before the next
required read would exceed an allowance or the projected 90,000-token total.

Current `source.go` is 38,470 bytes and `source_test.go` is 27,574 bytes.
Before review dispatch, remeasure the final changed functions, added tests,
diff, and implementation evidence. All new or changed functions and tests
must enter the review input. Their growth shares the supplemental cap;
approval of these current spans does not preapprove an expanded change.
Retain at least 30,000 tokens after initial reading for further evidence,
reasoning, and the verdict. Use the split below if that reserve cannot hold.

## Initial reads

Paths are relative to `/home/ubuntu/wr`. These are current inclusive line
spans. Relocate by function or heading after edits; remeasure moved inputs.

Read these files from `/home/ubuntu/.agents/skills/`:

- `agent-conduct/SKILL.md` and the assigned `go-implementor/SKILL.md` or
  `go-reviewer/SKILL.md`.
- `implementation-principles/SKILL.md`, `testing-principles/SKILL.md`, and
  `go-conventions/SKILL.md`; reviewers read `code-smells/SKILL.md`.
- `writing-for-agents/SKILL.md`, `unslop/SKILL.md`,
  `prose-principles/SKILL.md`, and `final-response/SKILL.md`.

Read these contract and status spans under `.docs/nextflow-conformance/`:

- `spec.md:23-444,1034-1054`: Architecture, A1, A2 selection contract, and
  Implementation Order.
- `phase1.md:1-39,87-148`: Instructions, Item 1.2, and exit conditions.
- `reviews/phase-01-acquisition-bundle.md:1-10,170-191`: prior approval and
  the accepted split boundary.
- `evidence/phase1-acquisition-part1.md:33-68,141-168`: provisioned inputs
  and part 2 handoff. Review 03 supersedes its pending-review status.
- `evidence/phase1-acquisition-part1-fix-02.md:78-98`: preserved checks and
  deferred functions. Review 03 supersedes its pending-review status.
- `reviews/phase-01-acquisition-part1-review-03.md:1-7,34-65,83-98`:
  accepted boundary, candidate protection, baseline checks, and handoff.
- `reviews/runtime-packaging-author.md:16-81,124-140`: measured packaging,
  dependency provenance, and immutable URLs.
- `evidence/phase1-acquisition-part1-provision.txt:1-47`: explicit fixture
  provisioning and exact expected byte identities. Read without executing.

Read these implementation spans:

- `nextflowconformance/source.go:1-186,313-398,441-514,557-618,651-854,`
  `947-987,1080-1353,1467-1475`. These cover constants, transaction and
  preflight entry points, physical file and Java checks, source-byte helpers,
  pinned/locked callers, artifact saving, selection, and six owned functions.
- The owned functions are `acquirePinned:688-716`, `acquireJava:717-766`,
  `copyJavaFile:796-846`, `acquireRuntime:1140-1175`,
  `unpackRuntime:1176-1225`, and `acquireDependencies:1226-1258`.
- `nextflowconformance/source_test.go:1-317,636-671`: shared fixture,
  explicit input loader, CLI helpers, and existing A1_05. The fixture Java
  is labelled inert; that pass is not actual Java/runtime proof.
- `nextflowconformance/model.go:43-99,138-183,195-437,850-882,1244-1255`:
  constants, file/target/tree records, artifact/environment/coordinate
  records and checks, closure, decoder entry, and path rule.
- `nextflowconformance/cli.go:61-371,799-827,865-876,1064-1101`:
  invocation, output protection, acquisition/validation callers, identity,
  offline checks, result fields, and public entry point.
- `nextflowconformance/schema.go:323-346,424-437`: artifact and coordinate
  conditions. Item 1.1 owns the remaining closed-schema implementation.
- `cmd/wr-nextflow-conformance/main.go:1-44`, `go.mod:1-50`, and
  `.golangci.yml:1-169`: entry point and unchanged quality gates.

Read `.tmp/agent/runtime-packaging/inspection.json` as a small metadata
record. Under `.tmp/agent/nextflow-conformance/runtime-prerequisite/`, read
only `executable-command.json`, `executable-exit.txt`,
`executable-stdout.txt`, and `executable-stderr.txt` initially. The corrected
absolute-path executable probe reports 26.04.6 with exit 0 under network
denial. It proves startup with an existing image, not E1 or the acquired
candidate's execution closure. The earlier Bash invocation of the download
was invalid and establishes no curl/wget requirement.

## Supplemental read limits

All extra reads share 10,000 tokens, with at most 2,000 tokens per read.
Read an unchanged part 1 function or fixture case only when a changed caller
or failing regression needs it. Relevant first pointers are
`source.go:855-946,1354-1420` for identity acquisition and HTTPS bounds,
and `model_test.go:176-199,249-296,482-507,619-652` for artifact tests and
corpus helpers. Locate other direct dependencies with bounded searches.

The current focused baseline is 27 passing tests, eleven schemas, and 1,190
schema mutations. Fourteen lint findings belong only to the six owned
functions. Obtain log paths from the review 03 manifest by script and print
diagnostic headers and totals only. This size review did not rerun them.

Inspect candidate locks, trees, Java inventories, manifests, schema records,
and acquired directories with scripts. Keep full results on disk; print
counts, hashes, paths of failing records, and diagnostics. Read individual
record fields needed to construct valid fixtures. A required schema change
needs a separately bounded review; preserve the closed constraints.

For selectors, inspect only the twelve A2 paths, recursively referenced local
include files, `packing.gradle`, and root `build.gradle`. Locate boundaries
with scripts and print small snippets around matches. Record original-byte
start/end offsets, hashes, match counts, include targets, and resolution
results. Include all process/operator regions, imports, the migration HTML
heading, seven grammar rules with alternatives, every MixOp test method, and
both selected Gradle dependency regions. Missing, ambiguous, cyclic, or
unresolved required selectors/includes prevent candidate acceptance. This
preparation does not implement A2 extraction or award semantic review.

## Prerequisites and completion criteria

Rehash retained inputs before use. Stable fixtures are under
`.tmp/nextflow-conformance/test-inputs/`; their exact expected hashes and
sizes are in the provisioning script and part 1 evidence. Use its explicit
pinned-tree API response. The retained research `tree.json` names the commit
in its envelope and is not the tree-lock identity. Existing Java 21 is
`.tmp/agent/java21/jdk-21.0.12.1+1`. No system installs are authorized.

Part 2 is complete only when implementation and independent review establish
all these conditions:

1. The actual runtime remains one opaque 42,355,106-byte file with SHA-256
   `182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c`.
   Acquisition retains its prefix, shaded payload, and repeated ZIP names;
   it writes no members and invents no bundled JAR records. Pinned and locked
   acquisition retain executable permissions and path containment.
2. The candidate records the real Java tree, regular files and symlink
   targets, verified Java executable/version, OS/architecture, required
   environment tools, and any actual external JAR. Runtime edges name
   actual execution files. Hash the files physically present; Java-only
   source/archive fixtures do not establish a production closure.
3. All three pinned coordinates have hashed POMs with
   `dependency-metadata` role, `file` packaging, matching coordinates, and
   no execution dependency claim. Retain the separate launcher and pinned
   build files as provenance. POMs do not establish a resolved Maven graph.
4. Meaningful red and green commands cover the remaining behavior. All
   seven `TestUAT_A1_01` through `TestUAT_A1_07` pass at their public boundary.
   A1_05 and A1_07 prove required diagnostic codes, zero network requests,
   and no Nextflow process for missing runtime, altered Java, prefix and
   payload mutations. Packaging-label and replacement-lock-hash attempts
   fail with `E_TARGET_IDENTITY`. A1_06 uses actual pinned bytes and passes
   offline validation. Preserve part 1's three-blob fixture and regressions.
5. The real acquire CLI succeeds and emits its candidate through the
   reviewed decoder and schema. Preserve previous locks/cache on failure.
   Capture exact arguments, JSON, stderr, exit, candidate path/hash, file
   identities, and all exact selector/include results. Offline validation
   succeeds with acquisition fixtures stopped; only production `acquire`
   accesses the network. Keep the candidate separate from the accepted
   checked-in lock until independent review rehashes and accepts it.
6. All focused package/CLI tests and stock schema checks pass. Focused lint
   has zero findings with unchanged analyzers. Use Go 1.27.1,
   `CGO_ENABLED=1`, `-tags netgo`, and `-count=1` for tests. Use existing
   `.tmp/agent/bin/golangci-lint` v2.12.2 under `GOTOOLCHAIN=go1.26.3`.
   Follow phase 1's bounded exit commands; keep full logs on disk. Full
   unrelated wr tests remain outside scope. New artifact and evidence names
   identify Nextflow.

The reviewer independently rehashes the candidate's actual acquired bytes
and checks selectors, closure, and preserved regressions. A missing real
prerequisite leaves the affected acceptance incomplete. Startup evidence
and successful acquisition award no workflow, oracle, or wr pass.

## Split fallback

If the cap cannot hold, use sequential fresh implementation and independent
review contexts at these boundaries. Reapprove each narrower input bundle:

1. Runtime acquisition and preflight: A1_05 through A1_07, real Java/tools,
   POM/provenance records, opaque bytes, executable paths, identity mutations,
   and remaining lint. Run the full focused regressions and schema checks.
2. Actual candidate and selector acceptance: run real production acquire,
   resolve/hash every A2 selector/include, prove offline validation, and
   independently rehash candidate artifacts. Read the accepted runtime diff
   and evidence plus selector/provenance spans; preserve all seven UATs.

The first split cannot close Item 1.2 or Phase 1. Their completion requires
the actual candidate, selector evidence, and final independent acceptance.
This bundle review writes only this report and makes no commit or push.
