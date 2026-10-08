# Independent suite foundation feature review 02

FAIL. The corrected HTTPS fixture lane preserves A1's actual requests, but
F3 still forbids the local IPC used by its genuine Gradle build/native-test
route. One P1 feasibility finding remains. No other blocking prompt-coverage
gap was established.

Owner: `/root/nextflow_suite_feature_review02`. Worktree: `/home/ubuntu/wr`.
Branch: `nextflowdsl`. Revision owner: `/root`. Date: 2026-10-08.
This independent review applies spec-reviewer, agent-conduct,
go-conventions, testing-principles and writing-for-agents. Report mechanics
apply unslop and prose-principles. No spec, implementation, prior review,
historical authority or acceptance status was changed.

## F02: Permit bounded tool IPC or specify a proven compatible native route

Priority: P1. Locations: `spec.md:2096`, `spec.md:2146`, `spec.md:2181`.
Related obligations: `spec.md:2059`, `spec.md:2069`, `spec.md:2174`.
Retained evidence: `research-pilot/prerequisites.json:485` and the genuine
`prerequisites01/full-gradle-build.stdout:1` retained in research scratch.
The exact launch selections are in `prerequisites01/launch-plan.json`.

The offline lane denies all traffic, explicitly including loopback, while
requiring genuine JVM compilation and execution of the unchanged selected
Spock specifications. The accepted pilot's actual compilation and native
launch recipes use Gradle 9.3.1 with `--no-daemon`, `--offline` and
`-Dorg.gradle.jvmargs=-Xmx2g`. Its actual build log records a single-use
daemon being forked. `--no-daemon` therefore did not remove local process
communication from this measured route.

This is a tool requirement, not an assumed dependency download. The pinned
[Gradle worker builder][worker] creates a messaging connection and passes
its local address to the worker. The pinned [TCP connector][tcp] opens and
accepts a TCP socket; [address selection][address] chooses loopback. Gradle
[cache lock communication][locks] also uses datagram sockets. The official
[daemon documentation][daemon] describes client/daemon local socket
communication and single-use daemons when JVM settings differ. Independent
disassembly of the actual retained Gradle JARs confirms the TCP accept,
worker messaging and datagram operations in those acquired binaries.

Denying that loopback traffic prevents this genuine native tooling route
from completing. Allowing it violates the literal all-traffic boundary.
Moving the build/native tests to the fixture lane violates its separate
ban on genuine build/engine execution. A compiler flag, offline dependency
cache or `--no-daemon` does not itself supply a compatible replacement.
The spec does not identify a reviewed socket-free native recipe preserving
the actual selection, helpers, configuration and completion requirements.

Revise the offline boundary to allow only independently reviewed tool IPC,
with owned process/listener identities and recorded endpoints/protocols,
while denying dependency acquisition, DNS, fixture endpoints, host services
and external traffic. Count permitted IPC separately from acquisition and
engine/dependency requests. Retain the missing-Spock negative control and
prove that an undeclared HTTP listener or dependency repository cannot use
the IPC allowance. Require current genuine build/native attempts and
stopped descendants under this enforcement. An alternative is an explicit,
independently reviewed socket-free build/native launch recipe with proof
that it preserves the original obligations. Keep all 69 bindings and both
current lanes' source/cache isolation requirements.

This finding concerns the specified executable contract. No Gradle build,
Spock test or sandboxed runtime was launched for this review. Source and
retained-bytecode inspection establish the mechanism; they award no new
runtime or fresh-checkout acceptance.

## Correction 02 and retained feature coverage

The complete current prompt/spec, accepted pilot report/review,
clarification and author01/02 reports were read. Frozen machine records,
original parser/Mix/helpers, retained acquisition tests and actual build
artifacts were checked independently of author checkers.

- F01's A1 conflict is corrected. The seven A1 bindings are foundation
  fixtures with actual HTTPS endpoints; genuine build/engine operations
  remain in the other lane. A1_01 retains three real acquisition requests
  and zero subsequent validation requests. A1_02 retains actual 503
  responses, two all-failed requests and four single-failed requests, with
  setup acquisition separate. A1_06's Java-version probe is tool identity
  inspection, not a native specification build or Nextflow execution.
- The fixed `go test -exec` supervisor retains C2's Go driver, active source
  discovery, anchored selector, JSON events and distinct test executable.
  Separate writable fixture roots cannot supply genuine dependency/build
  inputs. The missing-Spock subcase demonstrates actual fixture byte
  availability while requiring the offline preflight to fail. Tool IPC
  remains the uncovered boundary in F02.
- B3 preserves selected assertions, helper/internal obligations, state,
  order, invocations, fixtures and completions. Native successes and
  reviewed unresolved dispositions cannot discharge full neutral coverage.
  All-upstream/internal and documented-language inventories retain later
  denominators and completion dependencies.
- C3/E3 preserve source-authored truth before engine observation, typed
  integer/string and file/list distinctions, multiplicity, parser order,
  literal backslash+n, substring strength and associated count errors.
  Collection, engine exit and supervisor completion remain independent.
  Native originals, neutral results, strengthening, document gaps, controls
  and replay retain separate provenance and execution claims.
- CLI-P8's runtime/parser mismatch, the unexecuted chained string Mix
  example/completion, all seven internal/helper obligations and all five
  document closure links remain named unfinished work. Numeric Mix,
  successful native helpers or resolved link locations supply no missing
  pass. Both product decisions remain unresolved, with affected semantics
  retained. Current wr execution counts remain zero.
- F3 retains published sources, dependency/cache reconstruction, portable
  paths, unchanged historical full modes, Git executable identity and new
  genuine attempts. The outer F3_03 plus seven fixture and 61 offline child
  results avoid direct suite recursion. Artifact replay creates no new
  execution. Original lock/runtime/160 selections/eighteen batches stay
  historical; native dependencies require a reviewed extension.
- Phase 2 reconciliation and F11's eleven malformed/valid UTF-8 pairs plus
  discriminating guard-removal control remain prerequisites. F1 retains
  immutable submission, durable expansion, crash boundaries, containers,
  resources, grouping, output access, intermediate-file policy and precise
  unsupported-semantics errors for future wr work. Broad operator batches
  still depend on the durable dynamic slice.

The architecture retains the pure-Go production boundary, existing public
developer entry point and private domain operations. Current acquisition
and schema code supplies no production semantic ledger or engine suite.
No whole-language or universal translation claim follows from this review.

## Independent preservation checks and identities

The [owned passive checker][checker] completed with [PASS results][checks].
It verifies all 49 original acceptance bodies against the exact archived
pre-revision spec, twenty precise added IDs and 69 total. All author01 IDs
remain; F3_03 is the only changed acceptance body. Eleven archived
authorities retain their recorded byte counts and hashes. Current
owner-controlled progress/plan generations are not spec approvals.

Direct machine checks confirm six methods, eleven units, ten helpers,
seven helper observations, 42 predicates, fifteen completions, four
invocations, 83 fixture identities and 34 documented facets. The actual
source lock has 2,856 files with 160 selected, and eighteen batches remain.
Source reads confirm shared parser sequencing, TestUtils filtering/sorting,
Mix membership versus sorted equality, reset/lifecycle/error helpers and
the five-second native timeout. Passive checks award no runtime PASS.

Primary reviewed SHA-256 identities are:

```tsv
Input	SHA-256
spec.md	2c6ce413c41c856d903c6946112e8b515e366f08ff9edb5498b85c1b7b77ea3e
prompt.md	a51411aa3c4e8828abf7658c2e846ed043b2cf36c8b21473a13f19803c51146c
research-pilot/pilot-report.md	c1233ec605d0bca3308c32a3f4623c10c536e2cb209dbea4e9751912ed545f2e
research-pilot/pilot-report-review.md	c429e8a40ce77a01d74ae39d1c2e6141c38a5ff0a195b7d0c7714ba22ac51e04
reviews/nextflow-independent-suite-author-01.md	126b3c0bc0dd663aaf0be31351ba63ee7f8d1bf0bfa2f5a6b7e463ebfab19750
reviews/nextflow-independent-suite-author-02.md	a26c297f4a01ad4403ff71b6c2db31d7c6115032c6f47ba9e2f288c1c15e3513
reviews/nextflow-independent-suite-clarification-01.md	8e2da43c7eb3f69289ae64d0280fe0d281a13d23c0e3778857f860df853fe2c0
```

Checks retain exact frozen contract/inventory/expectation, prior FAIL
report, original-result, actual recipe/log and Gradle JAR identities.
Primary Gradle source copies, URL/hash receipts and disassembly outputs
are retained in owned scratch. Initial source-path lookups returned 404;
the versioned repository tree supplied the actual paths. One checker used
an incorrect source-selection field name; it was corrected to the actual
`state` field before the completed passive run. Neither changed an input.

## Completion

Only this new report and owned feature-review02 scratch were written.
All owned commands completed, including the bounded JAR disassembly and
consumed tool session. No dependency acquisition, native build, engine,
behavioral test, nested agent, commit or push was started. No live process, tool
session, background job, child or outstanding wait remains. Root owns
F02 correction and the next fresh independent review.

[worker]: https://raw.githubusercontent.com/gradle/gradle/v9.3.1/subprojects/core/src/main/java/org/gradle/process/internal/worker/DefaultWorkerProcessBuilder.java
[tcp]: https://raw.githubusercontent.com/gradle/gradle/v9.3.1/platforms/core-runtime/messaging/src/main/java/org/gradle/internal/remote/internal/inet/TcpIncomingConnector.java
[address]: https://raw.githubusercontent.com/gradle/gradle/v9.3.1/platforms/core-runtime/messaging/src/main/java/org/gradle/internal/remote/internal/inet/InetAddressFactory.java
[locks]: https://raw.githubusercontent.com/gradle/gradle/v9.3.1/platforms/core-execution/persistent-cache/src/main/java/org/gradle/cache/internal/locklistener/DefaultFileLockCommunicator.java
[daemon]: https://docs.gradle.org/current/userguide/gradle_daemon.html
[checker]: ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review02/check.py
[checks]: ../../../.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/feature-review02/checks.json
