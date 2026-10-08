# Independent suite foundation feature review 01

FAIL. F3_03's fresh-checkout acceptance contract requires real HTTPS acquisition
tests while requiring zero network requests. This blocks a truthful completion
of the specified 69-UAT gate. One actionable finding remains.

Owner: `/root/nextflow_suite_feature_review01`. Worktree: `/home/ubuntu/wr`.
Branch: `nextflowdsl`. Date: 2026-10-08. Revision owner: `/root`.
This independent feature review applies spec-reviewer, agent-conduct,
go-conventions, testing-principles and writing-for-agents. It changes no spec,
plan, implementation, existing evidence or acceptance status.

## Finding F01: Make offline reconstruction compatible with real HTTPS UATs

Priority: P1. Locations: `spec.md:2095`, `spec.md:2125` and `spec.md:2133`.
Preserved obligation: `spec.md:880`, A1_01.
Retained implementation evidence: `nextflowconformance/source_test.go:71`
and `nextflowconformance/source_test.go:481`.

F3's reconstruction launches all 68 bindings other than F3_03 under an
enforced network-denied sandbox. Its acceptance explicitly requires network
requests to be zero. Those 68 include unchanged A1_01, which acquires three
declared blobs from a real local HTTPS fixture before stopping the server and
validating offline. The retained test creates `httptest.NewTLSServer` and
asserts three actual requests for acquisition. It separately asserts zero
requests for the subsequent offline validation. A1_02 also exercises actual
HTTPS 503 responses; denying its transport produces a different failure
experiment.

Under the stated zero-request sandbox, the required successful acquisition
cannot happen. Permitting its fixture requests violates F3_03's literal
zero-request criterion. Substituting preloaded blobs, mock transports, skipped
bindings or historical passes would weaken the retained A1 obligations and
the new fresh-checkout claim. The spec defines no loopback-fixture exception
or separately composed acquisition-test environment.

Revise the F3 contract to distinguish controlled local fixture traffic from
denied dependency and engine network access. For example, execute fresh-root
acquisition UATs against independently bound local HTTPS fixtures with recorded
requests, while enforcing and recording zero external access and strict
network denial for genuine Nextflow/build operations. Alternatively, compose
fresh-checkout acquisition-UAT results from a separate controlled sandbox with
the strict offline reconstruction results. Keep all 69 bindings current,
retain the original 49 obligations, deny old source/cache access, and state
exactly which requests and enforcement receipts each gate checks. Add an
explicit acceptance subcase proving permitted fixture requests cannot supply
an undeclared engine or build dependency.

## Coverage assessment

The complete prompt, spec, accepted pilot report/review, clarification and
author report were read. The author summary was checked against actual frozen
inventory/expectation records, pinned source excerpts and retained CLI/tests.
No separate blocking coverage gap was established in these areas:

- B3, suite records and final gates distinguish upstream predicates, helpers,
  shared state, ordering, invocations, fixtures and completion from documented
  facets. Full upstream/internal and documented-language completion remain
  required later work. Native-only and reviewed unresolved dispositions do
  not discharge that future denominator.
- C3 and E3 preserve integer/string, file/list and multiset distinctions;
  parser scalar provenance; P2's literal backslash+n; P6's substring check;
  associated count errors; and separate collection/engine/supervisor receipts.
  Reviewed shared truth precedes both engine routes. The unavailable wr route
  earns zero executions or passes.
- Native originals, neutral results, strengthening, document gaps, analytical
  controls and artifact replay retain separate claims. CLI-P8's runtime cause,
  the unexecuted string Mix example, all seven helper/internal obligations
  and all five document closure links remain unfinished dependencies.
- F3 addresses published tools, portable paths, historical full permissions
  versus Git executable identity, dependency cache reconstruction, raw replay
  and actual new attempts. Its explicit exclusion of the outer F3_03 prevents
  direct recursive execution, but does not resolve F01's networking conflict.
- Original source/runtime/selection acceptance stays historical. The source
  lock, 160 selected files and eighteen batches are preserved; additional
  native dependencies require a reviewed extension. Phase 2 reconciliation
  and F11's eleven discriminating malformed/valid UTF-8 pairs remain gates.
  Both product decisions stay unresolved rather than becoming exclusions.
- F1 retains durable file-producing expansion, immutable submission, crash
  boundaries, containers, resources, grouping, output access, intermediate
  file policy and unsupported-feature diagnostics as future wr requirements.

The architecture keeps the production wr dependency boundary pure Go and
uses the existing public CLI entry point for private domain operations. The
current CLI implements acquisition and validation only; neither schema
fixtures nor this review demonstrate the future semantic ledger or suite.

## Independent checks and input identities

The [owned passive checker][checker] completed. Its [results][checks] verify
49 original acceptance bodies byte-for-byte against the archived spec,
exactly twenty added IDs and 69 total, and all eleven archived authority
byte counts/hashes. It imports no author checker or execution gate.

Direct machine-record checks confirm six methods, eleven units, 42 original
predicates, ten helpers, seven helper-observation obligations, four invocations,
fifteen completions, 83 fixtures, 34 document facets and 28 separate expected
records. Original parser/Mix sources confirm shared-parser order, TestUtils
filter/map/sort, the five-second Mix timeout and comparator strength. The
actual Bash checks retain the documented fresh/resume aggregation and tee
boundaries. Retained A1_01 source confirms the network conflict directly.

The primary reviewed input SHA-256 hashes are:

```tsv
Input	SHA-256
spec.md	d6f6b495d729671f8c55d4c0b02d04149b1070390f0852d44081d3bfbea9b732
prompt.md	a51411aa3c4e8828abf7658c2e846ed043b2cf36c8b21473a13f19803c51146c
research-pilot/pilot-report.md	c1233ec605d0bca3308c32a3f4623c10c536e2cb209dbea4e9751912ed545f2e
research-pilot/pilot-report-review.md	c429e8a40ce77a01d74ae39d1c2e6141c38a5ff0a195b7d0c7714ba22ac51e04
reviews/nextflow-independent-suite-clarification-01.md	8e2da43c7eb3f69289ae64d0280fe0d281a13d23c0e3778857f860df853fe2c0
reviews/nextflow-independent-suite-author-01.md	126b3c0bc0dd663aaf0be31351ba63ee7f8d1bf0bfa2f5a6b7e463ebfab19750
```

The results retain additional exact bindings for frozen pilot contracts,
inventory, expectations and manifest, plus retained CLI, source tests, lock
and batches. Historical manifest generations were verified without changing
their hashes. Current progress is an owner-controlled orchestration generation
and is not used as proof of spec mutation or acceptance.

These checks award no runtime, acquisition, schema, F11, fresh-checkout,
oracle or wr PASS. No engine, build, acquisition or behavioral test was
launched. No new test is needed for this report-only change. An initial
owned checker syntax error was corrected before its completed passive run;
it changed no input or finding.

## Completion

Only this new report and owned feature-review01 scratch were written. All
reviewed input identities remain exact at completion. Report mechanics and
`git diff --check` pass. No commit, push, nested agent or background workload
was started. All owned commands completed; no live process, tool session,
job, child or outstanding wait remains. Root owns routing F01 for author
correction and a fresh independent review.

[checker]:
  ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review01/check.py
[checks]:
  ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review01/checks.json
