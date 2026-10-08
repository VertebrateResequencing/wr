# Item 2.1 independent prerequisite review 01

Item 2.1: FAIL for PREREQ-F1. The recorded source, resource, generated-class
and launch closure passes independent integrity checks. The command supervisor
can retain a live owned descendant after timeout while recording completion.
Correct and independently review that supervision before Item 2.2 launches.
This finding is distinct from the charter's F1 Bash propagation controls.

Owner: `/root/nextflow_pilot_feature_review02`; queue owner: `/root`.
Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`. Review round 1 covers
actual Item 2.1 artifacts against [Phase 2][phase2] and [charter][charter]
C1/C3. Target Nextflow 26.04.6, commit
`232b60569865e9a4577e48c1955409238359d6ca`. Accepted R-UAT-01 handoff history
remains unchanged. No selected original or R-UAT-02 execution pass is awarded.

## PREREQ-F1: Detached owned children survive timeout cleanup

[capture.py][capture], lines 78-99, sends tracked descendants SIGTERM but
sends SIGKILL only to the original parent's process group. A detached child
belongs to a different group. If it ignores SIGTERM, the wrapper leaves it
alive, then unconditionally records `completed=true` at lines 102-104.
Item 2.1 and the charter require process-tree termination on deadline.
Parent completion alone does not meet that requirement.

Two one-second, supervisor-only subjects used an exact copy of the reviewed
supervisor with only its output BASE relocated into owned review scratch.
No DSL, original CLI, observer, gap or F1 Bash control ran. The detached child
that ignored SIGTERM was captured in `descendant_pids`, PID 1622686. After
the wrapper returned zero, its record reported `timed_out=true` and
`completed=true`, while `/proc` reported that child alive in state `S`.
Its process group was 1622686; the parent's group was 1622685.

The paired detached child honoring SIGTERM was absent after the same timeout
path. This rules out a failure to create, identify or signal the subject.
The reviewer killed the surviving owned child with SIGKILL and verified it
absent. All subject parents, wrappers and children have stopped.
[Raw commands, logs, captures and results][supervision] retain the proof.

The correction must escalate termination for surviving tracked descendants,
including those outside the initial process group, and verify no owned live
process remains before claiming cleanup completion. Retain timeout and
cleanup-failure evidence. Preserve the existing successful capture history;
it is not evidence that this unexercised timeout path works. No implementation
was changed during this review.

## Accepted closure evidence

The unmodified passive author gate passes. Independent checks additionally
compare actual files with the pinned source, resolved graph and raw captures.
The [check results][checks] establish:

- All 17 frozen manifest fields retain their exact JSON values/types. All
  249 other sealed artifacts retain bytes, SHA-256 and recorded local modes.
  The working manifest adds only the bounded prerequisite field. P01's
  historical permission portability limitation remains root-owned.
- All 2,856 copied source files match the pinned cache and Git blobs.
  The genuine original Gradle build compiled both selected specs, original
  test fixtures and generated production/parser dependencies. Its actual
  capture and logs show 93.196 seconds, BUILD SUCCESSFUL and 21 executed
  tasks. No selected Gradle Test task or test-result directory exists.
- All 578 acquired resources match their recorded bytes/hashes. The 575
  Gradle cache resource origins have exact path-to-URL evidence in the frozen
  URL cache. Eighteen paths have multiple legitimate cached URL entries;
  each recorded origin is among those entries. Generated/copied artifacts
  remain separate from acquisitions. The three production resources and
  all 454 JDK file/link identities match their recorded provenance.
- Both module graphs retain actual selected/requested dependency edges and
  resolved artifacts. JUnit Platform launcher/engine/commons are 1.14.1;
  Spock's JUnit 5.14.1 BOM and conflict resolution explain the override of
  requested launcher 1.10.5. Project/compiler Groovy is 4.0.31, with Spock
  2.4-groovy-4.0, ByteBuddy 1.14.17, Objenesis 3.4 and Jimfs 1.2. Original
  JaCoCo launch/report dependencies are 0.8.14 with ASM 9.9.
- Eleven required generated classes and 2,276 runtime/compiler file records
  match actual bytes. Selected spec classes, TestUtils, ScriptHelper,
  Dsl2Spec, BaseSpec and all five original Mock classes are present. Genuine
  NO-SOURCE directories remain distinguished from missing required classes.
- The ten-file CLI template matches the original workflows, hidden checks,
  topic fixture, runner, config and ignore files, retaining the runner's
  executable mode. Recorded required tool hashes/executability match actual
  tools. Original config discovery and effective executor/container remain
  Item 2.2 observations; the declared container is insufficient evidence.
- All fifteen main command captures match their retained records and logs,
  completed successfully within command and stage deadlines, and account
  for all fifteen available units. These actual successful attempts do not
  exercise PREREQ-F1's failing timeout branch.

No rebuild or resource acquisition was needed for this review. Compilation
establishes prerequisite availability, rather than original behavior.

## Native launch route decision

Accept the unexecuted native Gradle selection, isolation and reporting route
in [launch-plan.json][launch] and [launch.init.gradle][init]. It uses genuine
JUnit/Spock and selects three features in one launch per original spec.
Passive compiled metadata preserves selected ordinals 0, 1, 2, shared parser
state and Mix's five-second timeout. Genuine Spock source confirms ordinal
ordering, shared setup and default disabled order optimization/parallelism.

The init script changes isolated writable home/temp paths and native logging;
it replaces no source, assertion, helper or execution method. Recorded JVM
opens, JDK 21 launcher and original heap settings remain intact. Actual test
classpath inspection finds no SpockConfig.groovy; isolated homes have none.
The retained Spock sources match their acquired source JAR.

Decline executing this route through the current supervisor until PREREQ-F1
is corrected and reviewed. Native XML and logs preserve feature outcomes and
original failure conditions/exception values. They do not observe every
successful internal value or subcase completion. Additional instrumentation
still requires independent review before use. Actual feature discovery,
execution order, normalization and original predicates remain Item 2.2 work.

## Hash bindings, effort and completion

Reviewed charter SHA-256:
`a4c17ffca162071ec8825785e39202b209fe0cb54c0cc4ef3ece4d82a0dee67f`

Reviewed prerequisites SHA-256:
`0811af15f92f19bfc416ac6f8bfdadccc8517c3fa4a82d05c2c382ecdee61f7e`

Reviewed working manifest SHA-256:
`cb4ce476a157d79c87333a8441afb5c1941fe3edd969117928cde7eb8baa1886`

Reviewed author report SHA-256:
`ec77d6e3cd851feda75b3a8903d9801d32861e357eb68f893e7d931c9d6e590d`

Reviewed supervisor SHA-256:
`607c69469aa4a46bc784190b123666911936b94a65f435fd1e98889dfb686071`

Independent checks SHA-256:
`1ae53bd86f2a79a0baf3d505a0dc28298f53e628c3f31769556c1b92ec8c1b49`

Supervisor proof SHA-256:
`b8e21c8f0d01953a5ba00e472cc0244d0ce18a1177e64c1bb959d7376e046e5a`

The recorded review interval starts at 2026-10-08T13:27:29.321364Z and reaches
the substantive decision at 13:33:22.564161Z, 353.243 seconds or 5.887 minutes.
Initial instruction/data reading preceded that clock and was not separately
timed. No full-duration or individual-contract estimate is invented. The
stage deadline is 14:58:40.671383Z. Final artifact preparation and cleanup
verification after the decision are recorded in owned completion evidence.

All 500 protected baseline files retain their hashes. Writes are confined to
this review and owned scratch. Python syntax and report mechanics pass;
no lint, strict typing, nf-test or nf-core gate is claimed. No selected test,
original CLI, instrumentation, gap, F1 Bash control, build, download, core or
production change, commit, push or child agent occurred. All owned checks
and supervisor subjects completed; no process, tool wait or job remains live.
Root owns correction routing, stage clocks, checkboxes and delivery.

[phase2]: nextflow-pilot-phase2.md
[charter]: charter.md
[capture]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites01/capture.py
[checks]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites-review01/checks.json
[supervision]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites-review01/supervision/
[launch]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites01/launch-plan.json
[init]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites01/launch.init.gradle
