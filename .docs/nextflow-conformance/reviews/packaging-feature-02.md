# Packaging feature review 02

Verdict: PASS

The amended spec covers the accepted prompt and the runtime packaging
correction. No blocking feature-coverage finding remains. This is an
independent review of the spec and retained artifact evidence; no prior
feature verdict was read.

## Reviewed inputs

SHA-256 identities at review time:

```tsv
Input	SHA-256
prompt.md	e4a50b70971fa0e129c265aae32f2dabab090916cad53494f723d65135c6db1b
spec.md	553c985cdd244a69ef3d76f35aa9658c1c3cfd017741c146f02d4f73b50ba7be
reviews/runtime-packaging-author.md	cd9071e08d519a0a146b03fa9b1ab5beccf307cf17ab9176dd44b235b0b5a532
nextflow-26.04.6-dist	182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c
```

Review followed spec-reviewer, go-conventions, implementation-principles,
and testing-principles. The evidence directory was
`.tmp/agent/runtime-packaging/`. All 11 files listed in its inspection record
matched their recorded byte counts and SHA-256 hashes on independent reads.

## Packaging coverage

- Independent ZIP directory inspection found 24,898 entries, 23,238 distinct
  names, 1,629 repeated names, 1,596 repeated class names, and zero nested
  JARs. The unchanged distribution has 42,355,106 bytes, with its first ZIP
  local header at offset 17,247. Its manifest names
  `nextflow.cli.Launcher`. These observations support the opaque-artifact
  contract without requiring member extraction.
- The four retained Gradle files match the Git blob identities in the
  retained tree listing. Their construction rules corroborate launcher
  concatenation and shading of `runtimeClasspath` and
  `lineageImplementation`. The spec preserves those files as provenance.
- Acquisition and A1_06 require the actual whole distribution unchanged.
  Bundled classes are covered by that hash. The artifact schema records
  packaging, nullable Maven coordinates, provenance POMs, and external
  execution dependencies without inventing nested or separate bundled JARs.
- A1_04 retains transactional rejection of traversal, absolute paths,
  duplicate destinations, and unsafe symlinks for extracted archives.
  The opaque exception permits no extraction or collision resolution.
- A1_07 independently changes prefix and payload bytes and requires
  `E_RUNTIME_HASH` before execution with zero network requests. Packaging
  relabeling and replacing the lock hash cannot bypass target identity;
  those cases require `E_TARGET_IDENTITY`. Exact byte count and rejection
  of non-regular executable inputs remain explicit.
- A1_05 checks the real distribution and Java tree. D2 binds freshness to
  distribution and external execution inputs. E1 invokes the distribution's
  embedded launcher, uses isolated homes, and requires enforced network
  denial. Missing dependencies fail instead of triggering a substituted
  launcher download. POM provenance is not presented as a resolved Maven
  graph or proof that the runtime closure executes successfully.

## Original requirement coverage

- A1 pins source, parser, runtime, dependencies, and environment. A2 retains
  complete selected bytes and meaningful source units, including includes,
  grammar alternatives, warnings, overloads, and unclassified content.
- B1 requires independent source-based review and facet-level links. B2
  retains typed syntax and JVM/plugin policy as unresolved decisions with
  affected requirements, rather than historical exclusions.
- C1 defines concrete observation contracts and constrained normalization.
  C2 binds UATs to discoverable active Go tests. D1 rejects missing, skipped,
  failed, timed-out, or malformed execution evidence. D2 invalidates stale
  inputs and rechecks raw evidence and artifacts.
- E1 requires seven actual oracle cases, including ordered fair emission
  after reversed task completion and specific zero-value error contracts.
  E2 requires 18 accounting controls and three semantic observer mutations.
  Fixtures cannot establish wr execution or replace missing prerequisites.
- F1 retains all nine wr runtime requirements and the durable dynamic slice
  before broad operator implementation. F2 generates bounded handoffs and
  checklists from current evidence while preserving unresolved work.
- Architecture keeps the developer CLI separate from production wr, places
  domain logic in the public Go package, uses existing GoConvey and standard
  library capabilities, and requires observable tests with real CLI/oracle
  integration. Runtime conformance is explicitly outside this foundation.

An independent numbered-ID count found exactly 49 unique acceptance tests:
A1 7, A2 4, B1 5, B2 3, C1 4, C2 4, D1 4, D2 5, E1 4, E2 3, F1 3, F2 3.
Both C2's mapping rule and the final foundation gate name 49. A1_06 and
A1_07 add concrete positive and negative packaging assertions.

## Review limits

No acquisition CLI, Java runtime, or oracle workflow was executed by this
review. Successful offline execution remains an implementation acceptance
gate. Retained build-file identities were compared with the supplied tree
listing; this review did not independently fetch the upstream Git objects.

Phase files are awaiting the caller's update and are outside this spec
verdict. The spec was not edited. No commit or push was made.
