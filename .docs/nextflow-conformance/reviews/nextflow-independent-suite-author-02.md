# Independent suite foundation author correction 02

F01 is corrected in [spec.md][spec]. F3 now composes controlled real-HTTPS
fixture tests with genuinely offline reconstruction. All 49 original
acceptance bodies and all 69 IDs remain unchanged. Only F3's reconstruction
contract and F3_03's acceptance body change from the reviewed author01
generation. This correction awards no implementation or runtime PASS.

Owner: `/root/nextflow_suite_spec_author02`. Worktree: `/home/ubuntu/wr`.
Branch: `nextflowdsl`. Revision owner: `/root`. Date: 2026-10-08.
Only the active spec, this new report and owned `author02` scratch were
written. No prompt, plan, implementation, checklist, lock, pilot, historical
report, archive, commit or push was changed.

## Finding and evidence

The actual [feature review 01][feature] identified one P1 feasibility
conflict. Author01 placed every nonrecursive binding under total network
denial while requiring zero network requests. Retained A1_01 acquires three
blobs from `httptest.NewTLSServer`, asserts three actual HTTPS requests,
stops the server, then asserts zero requests during offline validation.
A1_02 exercises actual HTTP 503 responses with two requests for the
all-failed subcase and four for each single-failed subcase. Its successful
setup acquisition is separately counted. These assertions remain required;
mocked transport or preloaded acquisition output cannot replace them.

Research read the prompt, actual feature feedback, author01 report and
preservation checks, relevant record/runner/offline contracts, and retained
acquisition tests. Applied spec-author, agent-conduct, go-conventions,
testing-principles, unslop, prose-principles and writing-for-agents.

## Corrected execution boundary

The simplest route separates fixture execution from genuine build/engine
execution while preserving C2's actual Go discovery and execution events.

- The offline lane restores the reviewed closure, compiles genuine Go/JVM
  sources and runs all required genuine engine cases and controls. It
  executes the 61 child bindings other than A1_01-A1_07 and F3_03. All
  networking, including loopback, DNS and fixture endpoints, is denied.
- The fixture lane executes the seven A1 bindings against real HTTPS
  servers. Its private network namespace permits only declared test-owned
  loopback fixture traffic, with no external route, DNS, proxy or host
  services. Listener identities, method/path, status, body hash and actual
  request counts are retained per UAT/subcase. Unexpected delivered
  requests fail the gate. Existing zero-request validation/preflight
  assertions remain distinct from setup/acquisition counts.
- Go drivers and compilers remain offline. A fixed published `go test`
  `-exec` supervisor launches only the compiled A1 test executable in the
  fixture lane. Hashes, actual argv, Go JSON events and separate process
  identities bind this execution. Fixture inputs are read-only; writable
  corpus/cache/home/temp roots are isolated from genuine build/engine
  inputs. Only completion evidence returns to the outer supervisor.

Both lanes deny old-workspace source/evidence, ignored scratch and ambient
caches. Enforcement receipts distinguish allowed delivered fixture traffic,
denied connection/read probes, zero delivered external requests, zero
offline-lane requests and zero old-source/cache reads. Missing enforcement
fails `E_OFFLINE_UNAVAILABLE`; copied success status cannot supply proof.
The outer F3_03 composes seven current fixture results, 61 current offline
results and its own reconstruction proof without recursive suite execution.

## Discriminating acceptance subcase

F3_03 also removes the resolved Spock resource from a separately restored
offline cache. A declared fixture serves the exact missing bytes at
`/F3_UNDECLARED_SPOCK`. One actual fixture-lane GET returns 200 and the
expected hash, with output confined to that lane. The offline sandbox's
connection probes to this listener and an external address, and read probes
to fixture output and the old cache, are denied without delivered requests
or bytes. Genuine native-build preflight returns 2 with
`E_DEPENDENCY_MISSING`, names Spock, starts no affected build/engine and
awards zero passes. Restoring the resource solely from the reviewed bundle
restores the genuine offline build/engine pass.

This paired control distinguishes a usable acquisition fixture from an
undeclared dependency channel. Fixture reachability cannot discharge the
offline build closure or replace genuine engine execution.

## Preservation and checks

The owned passive checker `check.py` completed with PASS in `checks.json`.
It verifies all 49 original acceptance bodies byte-for-byte against the
pre-revision archive, all 69 IDs against author01, and only F3_03's body
changed since feature review 01. Every spec byte before F3 and after F3 is
unchanged. All eleven original archived authorities retain their recorded
byte counts/hashes; all thirteen reviewed non-spec input identities remain
exact. The first author generation retains SHA-256
`d6f6b495d729671f8c55d4c0b02d04149b1070390f0852d44081d3bfbea9b732`.
`inputs.json` also records the retained manifests and prior reports.

Spec and report mechanics pass ASCII, 80-column prose, named fences,
heading levels, whitespace and local references. `git diff --check` passes
for the spec and report. These are passive preservation/mechanics checks,
not behavioral acceptance. No new behavioral test is appropriate for this
document correction; F3_03 specifies the future executable control.

Native originals, neutral mappings, strengthening, document gaps, raw
artifact replay and actual executions retain separate claims. The finite
proven-family denominator, full future upstream/document obligations,
CLI-P8, unexecuted string Mix example, seven internal/helper obligations,
five document closure links, F11 and Phase 2 reconciliation remain as
written. Both product decisions remain unresolved. Current wr passes stay
zero. No research acceptance or historical hash was rewritten.

## Completion

The author correction is complete with no authoring blocker. Independent
feature review remains root-owned; this report does not grant its verdict
or advance a consecutive pass count. `completion.json` records final
spec/report/check identities without a report self-hash.
All owned commands completed. No engine, build, acquisition, behavioral
test, nested agent or background workload was launched; no live process,
tool session, job, child or outstanding wait remains.

Owned checker, results, input and completion bindings reside under:

```text
.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/author02/
```

[spec]: ../spec.md
[feature]: nextflow-independent-suite-feature-01.md
