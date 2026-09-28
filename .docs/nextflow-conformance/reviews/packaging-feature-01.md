# Packaging feature review 01

Verdict: PASS.

The amended spec covers the accepted foundation requirements and corrects
the runtime packaging assumption without weakening source extraction,
target identity, evidence freshness, or the real oracle gate. No blocking
feature-coverage finding remains.

## Reviewed inputs

Reviewed `spec.md` against `prompt.md` using `spec-reviewer`,
`go-conventions`, `implementation-principles`, and `testing-principles`.
The author report supplied factual leads only. Phase plans were outside this
review because their packaging updates are still pending.

```tsv
Input	SHA-256
spec.md	553c985cdd244a69ef3d76f35aa9658c1c3cfd017741c146f02d4f73b50ba7be
prompt.md	e4a50b70971fa0e129c265aae32f2dabab090916cad53494f723d65135c6db1b
reviews/runtime-packaging-author.md	cd9071e08d519a0a146b03fa9b1ab5beccf307cf17ab9176dd44b235b0b5a532
```

## Independent artifact checks

Read the retained distribution directly with Python's SHA-256 and ZIP
directory readers under a 20-second timeout. It is 42,355,106 bytes with
SHA-256
`182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c`.
The first ZIP local header begins at byte 17,247. Its directory contains
24,898 entries, 23,238 unique names, 1,629 repeated names, and zero nested
JAR names. The manifest names `nextflow.cli.Launcher`.

Recomputed the Git blob IDs of root `build.gradle`, `packing.gradle`,
`modules/nf-lang/build.gradle`, and `modules/nextflow/build.gradle`.
All four match the retained non-truncated tree response. Read the build
instructions and embedded launcher directly. They confirm concatenation of
the launcher and shaded JAR, included duplicate names, both runtime and
lineage configurations, and Java execution of the distribution itself.

These checks establish packaging and agreement with the retained tree
listing. They do not establish a completed acquisition transaction, a
resolved Maven build graph, or successful Java execution. No oracle was run
for this review.

## Coverage findings

- A1_06 requires the actual repeated-name distribution to survive acquisition
  unchanged, with no extracted members or invented bundled JAR artifacts.
  The artifact model records actual external execution inputs. POMs are
  hashed provenance and cannot stand in for resolved runtime dependencies.
- A1_04 retains transactional rejection of duplicate extraction destinations,
  traversal, absolute paths, unsafe symlinks, and size-limit violations.
  Opaque handling never authorizes member extraction with relaxed checks.
- A1_05 and A1_07 require pre-execution offline rejection of missing runtime
  bytes, changed Java inputs, launcher-prefix and payload mutations, and
  target-identity bypasses using changed hashes or packaging labels.
- D2 binds evidence to distribution bytes, Java, actual tools, effective
  environment, source, tests, and corpus. E1 still requires seven real
  oracle cases under enforced network denial. Closure sufficiency is limited
  to those cases; acquisition alone cannot satisfy E1.
- A2 and B1 retain byte-complete extraction, independent semantic review,
  defaults, overloads, options, examples, warnings, and grammar alternatives.
  B2 keeps typed and JVM/plugin policy decisions visible and unresolved.
- C1, C2, D1, and D2 retain concrete observable expectations, exact test
  discovery, actual execution events, stale-evidence rejection, generated
  views, and evidence-kind checks. The numbered acceptance IDs are exactly
  49 and all are unique, including A1_06 and A1_07.
- E1 retains ordering, task, file, and diagnostic observations. E2 retains
  all 18 accounting mutations and three semantic observer mutations with
  intended diagnostic checks. Neither fixtures nor parsing diagnostics can
  award wr runtime passes.
- F1 preserves all nine wr requirements, durable dynamic execution and
  crash boundaries, immutable submissions, container/resource/grouping and
  output requirements, and the gate before broad operator implementation.
  F2 preserves bounded handoffs, independent input-bundle review, durable
  evidence links, unresolved decisions, and generated completion checklists.
- The public Go package, existing standalone CLI pattern, standard library,
  GoConvey bindings, cancellation, and observable-boundary tests fit the
  supplied conventions. Runtime implementation remains a later milestone.

The final gate still distinguishes foundation completion from incomplete
target inventory and unavailable wr runtime support. Pending phase updates
must follow the amended spec before implementation resumes.
