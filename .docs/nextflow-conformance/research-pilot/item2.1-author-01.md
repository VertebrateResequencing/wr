# Item 2.1 prerequisite author record

Item 2.1 has measured prerequisite closure available for independent review.
The unchanged pinned upstream Gradle build compiled both selected Spock specs
and the genuine parser/Mix fixtures. No selected test, original CLI, observer,
gap or control ran. R-UAT-02's original-attempt requirement remains pending.

## Build result and resource cost

Work used `/home/ubuntu/wr`, branch `nextflowdsl`, pinned Nextflow commit
`232b60569865e9a4577e48c1955409238359d6ca`. The full original multi-project
build ran in a byte-identical copied source tree with its own Gradle cache.
There was one heavy workload, one worker, a 2 GiB Gradle heap and no retained
daemon. All 2,856 copied source files still match the pinned Git tree and
original cache. No selected source, helper or assertion was rewritten.

The genuine `:nf-lang:compileTestGroovy :nextflow:compileTestGroovy` command
completed successfully in 93.196 seconds. Gradle reports 21 executed tasks
and 1 minute 32 seconds. That includes production module compilation,
ANTLR-generated parser sources, genuine testFixtures compilation and the
selected test classes. Follow-up prerequisite tasks copied applicable test
resources and resolved launch dependencies. No Gradle Test task ran.

Java is retained Temurin 21.0.12.1+1; all 454 recorded JDK files/links match
production provenance. Source/target compatibility remains Java 17. Official
Gradle 9.3.1 downloaded and matched its separately downloaded SHA-256. Its
embedded Groovy is 4.0.29; the actual project/compiler classpath uses 4.0.31.
Spock is 2.4-groovy-4.0. Actual JUnit Platform launcher, engine and commons
align to 1.14.1 through Spock's JUnit 5.14.1 BOM, overriding the originally
requested launcher 1.10.5. ByteBuddy is 1.14.17, Objenesis 3.4 and Jimfs 1.2.
The original Gradle JaCoCo launch/report dependencies resolve to 0.8.14 and
ASM 9.9. Complete selected/requested edges and artifacts are retained in the
actual Gradle resolution record.

There are 578 distinct acquired resource paths and 578 distinct SHA-256
values. They comprise 575 Gradle-resolved artifact/metadata files, the Gradle
ZIP/checksum and the official Spock source JAR needed for selection review.
Gradle's frozen URL cache records their actual acquisition origins. Generated
classes, copied sources/templates, extracted tool files and captures are
separate records, not additional acquisitions. The verified production
SOURCE, RUNTIME and LAUNCHER resources are reused unchanged. The shell-prefixed
runtime ZIP remains opaque and retains its original bytes. Scratch occupies
about 497 MiB at final verification; no system package was added.

All acquisitions completed inside their five-minute bounds. The full build
completed inside its 30-minute bound. No acquisition/build timed out or failed.
Two passive author inventory checks initially mishandled symlink-target bytes
and absent NO-SOURCE resource directories. The corrected collectors verified
the actual JDK links and selected classes; the diagnostic causes are retained
in `inspection-corrections.json`. These were author recording defects, not
engine or prerequisite failures.

## Availability, selection and limits

All eleven P/M units and all four fresh/resume CLI invocations are available
for genuine attempts. Each has an explicit availability record. Eleven named
generated classes bind ScriptAstBuilderTest, MixOpTest, TestUtils, ScriptHelper,
Dsl2Spec, BaseSpec and the original five Mock classes. Full runtime/compiler
files, source hashes and exact build captures support those records.

Passive javap inspection confirms selected feature ordinals 0, 1, 2 for each
spec, the original shared parser field and Mix's five-second timeout. Genuine
Spock source shows declaration/execution sorting and one shared-spec setup.
The ready commands select all three parser features in one class launch and
all three Mix features in another. This preserves within-method subcase
sequence and the shared parser's lifetime across selected features. Actual
engine discovery, method execution order and subcase completion still require
Item 2.2 captures; none is reported as observed execution here.

`launch-plan.json` contains exact command arrays, cwd and environment.
`launch.init.gradle` configures writable isolated home/temp paths, preserves
default disabled parallelism and enables native full Gradle reporting. It
changes no original helper or assertion. Independent review must accept this
unexecuted route before use. Standard Gradle XML captures genuine feature
outcomes, exceptions and failed-condition expressions/values. Successful
completed features entail their sequential assertions passed; XML alone does
not expose every successful internal value or subcase completion. Additional
instrumentation requires separate review before execution.

The unchanged ten-file CLI template retains both workflows, hidden checks,
topic expected bytes, all ignore files and `tests/nextflow.config`. Bash,
runner tools and process tools exist; `TERM=xterm tput sgr0` completed.
`unzip` is absent and is not needed by the selected route. The original runner
will use the verified launcher and opaque runtime through `NXF_BIN`, v2,
TEST_JDK 21 and explicitly empty WITH_DOCKER. Actual config discovery and
effective executor/container are pending runtime observations. The declared
container alone awards no container-execution claim.

## Preservation, clocks and review handoff

The working research manifest adds one `phase2_prerequisites` field. Every
one of its 17 prior fields compares equal with exact canonical JSON types to
the frozen Phase 1 checkpoint. All 249 other sealed artifacts retain their
accepted SHA-256, bytes and local modes. The frozen manifest remains
`2ac04fa4c27a17753afe39d9dcbf6489bafd5027475799099b0359787f0052d0`;
the seal remains
`fd14d9c7c7b8c8ab7f37f99b9c406434b13b21c534bc24c563f0aff1c9356957`.
Historical mode fields remain unchanged. This is local handoff evidence;
P01's fresh-checkout permission portability finding remains root-owned.

The first recorded author clock is 2026-10-08T12:59:36Z. The substantive
closure decision clock is 2026-10-08T13:16:19.680886Z, 16.728 wall minutes.
The stage began at 12:58:40.671383Z and ends at 14:58:40.671383Z. Fifteen
prerequisite command records retain actual start/end, cwd, environment,
timeout/deadline, exit/signal, diagnostics and completion. Build/export time
is inside author wall time and is not added again as separate effort. No
individual-contract review minutes are invented; fresh review has not begun.
Final verification and report preparation occur after the decision clock and
are retained in the completion evidence.

The passive prerequisite gate passed frozen-field, seal, resource, capture,
source and process checks. All scratch Python scripts parse under Python 3.12.
No ruff, pyright, nf-test, nf-core or production test pass is claimed. The
actual unchanged upstream compilation and passive byte/data checks are the
applicable gates for this bounded prerequisite item.

The [compact read plan][plan] maps all new semantic fields and exact launch
commands. [Scratch evidence][evidence] contains the full resolution and
resource indexes, generated class hashes, raw command captures, source-order
inspection and corrections. Root retains queue, checkbox, stage-clock and
status ownership. No production lock, bootstrap batch, code, core spec,
phase plan, F11, commit or push was changed. All owned commands finished;
no owned process, job, wait or child remains live at handback. Original,
projection, oracle, translation, control and wr execution passes remain zero.

[plan]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites01/review-read-plan.md
[evidence]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/prerequisites01/
