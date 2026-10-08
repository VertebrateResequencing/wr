# Independent suite foundation author correction 03

F02 is addressed in [spec.md][spec]. F3 permits bounded owned Gradle IPC
while preserving zero delivered dependency/acquisition/engine requests.
All 49 original acceptance bodies and all 69 IDs remain. Only F3's contract
and F3_03's acceptance body change from author02. This document revision
awards no implementation, runtime or fresh-checkout acceptance.

Owner: `/root/nextflow_suite_spec_author03`. Worktree: `/home/ubuntu/wr`.
Branch: `nextflowdsl`. Revision owner: `/root`. Date: 2026-10-08.
Applied spec-author, agent-conduct, go-conventions, testing-principles,
unslop, prose-principles and writing-for-agents. Only the spec, this report
and owned `author03` scratch were written. No commit or push was made.

## Finding and measured inputs

The actual [feature review 02][feature] found that blanket loopback denial
prevents the genuine Gradle 9.3.1 build/native route. The retained build log
records a forked single-use daemon despite `--no-daemon`, with
`-Dorg.gradle.jvmargs=-Xmx2g`. Its launch recipes also retain `--offline` and
`--max-workers=1`. Existing JAR disassembly and pinned Gradle sources prove
TCP messaging, loopback address selection and UDP cache-lock sockets.

Additional passive research retained the pinned outgoing connector and
lock payload/type sources. TCP uses the literal `Gradle Magic` preamble;
lock messages contain version 1, an eight-byte big-endian lock ID and one
type byte. Actual retained JAR disassembly confirms daemon address/registry
publication and worker address serialization. An initial optional worker
serializer lookup used the wrong package; the actual messaging package was
located and disassembled. Two initial upstream daemon-source URL guesses
returned 404; the daemon findings use actual retained bytecode instead.

The current Linux 6.8 x86_64 environment has Docker server 29.1.3. A bounded
`bwrap` probe failed to configure loopback with `Operation not permitted`.
Docker's [none network driver][none] supplies isolated loopback. Linux
[ptrace][ptrace] supports supervised descendant execution. These facts
support the specified route; no container, sandboxed native build or new
supervisor was launched or proved by this author correction.

## Corrected executable boundary

- Each lane uses an isolated Docker container with `--network none`,
  separate PID/mount/network namespaces, no added capabilities and a
  published seccomp profile permitting parent-child tracing. Read-only
  inputs, isolated writable roots and recorded kernel mounts exclude old
  roots, ambient caches, host services and fixture/build input transfer.
  The OCI root filesystem must be generated offline from locked bundle
  inputs; ambient image tags cannot supply it. Docker/kernel/OCI runtime
  identities are explicit host control prerequisites.
- A fixed published supervisor traces all descendants and socket I/O.
  Before delivery it matches actual socket ownership and current Gradle
  role to the daemon registry or worker launch address. TCP admission also
  checks the pinned connector preamble. UDP admission binds owned lock
  listeners and current cache lock records to the fixed ten-byte protocol.
  Java identity, loopback address or a protocol prefix alone grants no IPC.
  Denials return `EACCES` with independently retained decision receipts.
- Genuine unchanged selected native specs/helpers retain the measured
  Gradle recipe and require fresh compilation/native attempts under this
  enforcement. Allowed TCP flows and UDP binds/datagrams are separate from
  zero delivered dependency/acquisition/application requests, external
  traffic/DNS and old-root/cache reads. All descendants and containers
  must have stopped/reaped/removed receipts, including failure and timeout.
  Missing capability, socket coverage or cleanup leaves proof incomplete.
- The existing seven A1 real-HTTPS fixture bindings, C2 driver/discovery,
  isolated fixture outputs and all original acquisition assertions remain
  unchanged. The outer F3_03 still composes seven fixture children, 61
  offline children and its own proof without recursive suite execution.

## Discriminating controls

The missing-Spock control remains intact: a real fixture GET proves the
exact resource is available while offline fixture/external/read probes
fail. Genuine native-build preflight returns 2 with `E_DEPENDENCY_MISSING`,
starts no affected build and awards zero passes. Only the reviewed bundle
restores the resource and permits a new genuine build/native pass.

An added same-container control retains a usable undeclared HTTP repository
with the exact Spock bytes. One supervisor-only readiness GET returns 200
and the resource hash; its bytes cannot enter build roots. That diagnostic
permission is revoked before the genuine recipe runs. With the closure
restored, a separately reviewed control init script probes the repository
from the actual Gradle daemon and requires the supervisor's denial receipt.
The unchanged selected native features then run with their legitimate IPC.
The server receives zero build/dependency requests. An absent service,
namespace-only policy or Java-executable-only allowance cannot supply this
result. Readiness/control traffic remains distinct from tool IPC and actual
acquisition. The diagnostic server and all descendants must stop.

## Preservation and checks

The owned passive checker verifies original acceptance bodies byte-for-byte
against the exact archived pre-revision authority. All twenty added IDs and
69 total IDs remain; only F3_03's body differs from author02. Spec bytes
before F3 and after F3 retain all native/neutral/document/upstream
distinctions, six-phase order, original source/runtime/selection acceptance
and named unfinished dependencies. The seven-A1 fixture paragraph and
acquisition-count contract remain byte-identical. Eleven archived
authorities and the reviewed non-spec inputs retain their hashes.

ASCII, 80-column prose, fences, headings, references and whitespace pass;
`git diff --check` passes. These are passive checks, not behavioral tests.
No new behavioral test is appropriate for this prose correction; F3_03
defines the implementation's required discriminating subcases.

Owned research receipts, checker, results and final completion identities
reside under:

```text
.tmp/agent/nextflow-conformance/independent-suite-revision-2026-10-08/author03/
```

## Completion

Authoring has no remaining blocker. The OCI reconstruction, supervisor and
actual enforced build/native proof remain implementation obligations;
their future declarations are not current acceptance. Root owns fresh
independent review and the reset pass count. Historical review evidence,
locks, pilot records, plans, progress and implementation were not edited.
All owned bounded commands completed. No engine, build, behavioral test,
container, nested agent or background workload was started. No live owned
process, tool session, job, child or outstanding wait remains.

[spec]: ../spec.md
[feature]: nextflow-independent-suite-feature-02.md
[none]: https://docs.docker.com/engine/network/drivers/none/
[ptrace]: https://man7.org/linux/man-pages/man2/ptrace.2.html
