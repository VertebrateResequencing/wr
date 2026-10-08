# Phase 2: Resolve prerequisites and attempt genuine originals

Ref: [charter.md](charter.md) sections A1-A3, C1, C3 R-UAT-02 and
Experiment order and deadlines stage 2.

## Instructions

Use the `orchestrator` skill with `nextflow-implementor` and
`nextflow-reviewer`, under `/home/ubuntu/.agents/skills/`. Apply
[Phase 1 instructions](nextflow-pilot-phase1.md#instructions) and start only
after Item 1.3 is reviewed with an accepted hashed handoff. This stage has
two active hours within the eight-hour execution cap. Items are sequential;
keep one heavy workload active. Executable projections begin in Phase 3.

## Items

### Item 2.1: C1 - Measure actual prerequisite closure

charter.md section: C1.

Inspect the pinned selected build, fixture dependencies, generated test
classes, JVM arguments and compiler/test launch semantics. The verified
distribution's missing Spock/JUnit Platform, test classes and ScriptHelper
remain genuine gaps until resolved. Record actual transitive versions and
closure rather than treating runtime POMs or starting pins as sufficient.
Reuse hash-verified resources; acquire only required research resources in
the separate cache with isolated build output and Gradle caches.

Write `prerequisites.json` and update `research-manifest.json` with resource
hashes, tools, origins, dependency edges and exact available/unavailable
causes. Preserve Phase 1 handoff hashes. Bound each acquisition at five
minutes and the selected build at 30 minutes. Terminate process trees on
deadline and hash-link attempted commands, cwd, environment, times, deadline,
exit/signal, diagnostics and completion. Account for every affected unit.
This is prerequisite coverage for the one R-UAT-02 test, not an execution
pass. Item 2.2 depends on this reviewed closure or captured failure record.

- [ ] implemented
- [ ] reviewed

### Item 2.2: A1-A3, C1 - Attempt all scheduled originals

charter.md sections: A1-A3, C1, C3 R-UAT-02.

Run available selected Spock methods through the genuine harness with
original assertions, shared state and source order. Record how selection
preserves those properties; review diagnostic instrumentation before use.
Retain Mix's five-second timeout and bound each Spock launch at five minutes.
Run the genuine selected CLI runner/checks in a disposable copied layout
with hidden fixtures, config discovery, ignore rules and isolated cleanup.
Bound each fresh/resume CLI invocation at two minutes. Preserve v2 and
selected flags; add no typing or topic-preview flags.

Write `original-results.json` with hash-linked captures for every scheduled
unit. Record actual Java, TEST_JDK, NXF command, WITH_DOCKER, tools, config
files and effective executor/container mode. Capture original per-predicate
and aggregate outcomes, exceptions, values/files, logs and completion with
the charter's command/environment/time/exit metadata. Retain failures, skips,
timeouts and non-completion; unavailable units name their dependency,
fixture or deadline cause. A missing prerequisite stops its affected runtime
mapping; data/control work can continue without original/oracle/translation
passes. Candidate enumeration cannot satisfy the one R-UAT-02 test.

Phase 3 requires this reviewed attempt/accounting record. A measured original
failure permits research to proceed with its affected mappings unavailable;
it never becomes a successful original execution verdict.

- [ ] implemented
- [ ] reviewed
