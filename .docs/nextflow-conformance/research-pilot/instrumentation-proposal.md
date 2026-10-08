# CLI original capture instrumentation proposal

Review the finite `nxf_capture.py` wrapper and its structured launch inputs
before executing any original CLI through it. The exact genuine runner,
checks, workflow, config and ignore files remain byte-identical in the copied
layout. The wrapper and structured inputs are under `originals01/` scratch.

## Preservation boundary

The original runner sets NXF_CMD to this delegate, preserving NXF_RUN's
selected `-q run ../../<workflow>` arguments and fresh/resume sequence.
The delegate executes the actual pinned launcher with precisely that argv,
current cwd and a copied current environment. Standard output/error descriptors
are inherited unchanged, so the original bash -ex checks.out stream remains
live and exact. A normal exit is returned unchanged; negative engine status
is replayed as the same signal. No diagnostic text or output value is authored.

The exact accepted corrected supervisor supplies discovery, stable pidfds,
ancestry/birth guards and bounded cleanup. Linux child subreaper setup matches
that accepted version. Each individual engine invocation has 120 seconds,
capped by the existing stage deadline. No whole-runner budget substitutes
for these invocation deadlines. The outer genuine runner is independently
supervised by the accepted corrected capture route with a 540-second cap.

Immediately after each engine stops, the wrapper snapshots actual versions.txt,
.nextflow.log and .expected when present, before original cmp and later resume
can alter files. It records command/cwd/environment, start/end/deadline,
exit/signal/timeout, cleanup and stable identities. The genuine runner and
checks.out retain literal check outcomes and aggregate separately. Cleanup
failure records completed=false and returns distinct instrumentation exit125;
it cannot become an engine or original success. It adds no assertion and
changes no fixture or engine output. Successful raw Spock values/normalization
receive no additional instrumentation or equivalence claim.

## Inputs and isolation

The source ten-file template, real launcher/distribution, Java and tool
closure are the accepted prerequisite resources. The copied runner's cleanup
is confined to original-owned cli-layout. HOME and NXF_HOME are original-owned
empty directories; TEST_JDK is21, WITH_DOCKER is empty, NXF_OFFLINE is true,
NXF_SYNTAX_PARSER is v2. No static typing, topic flag or config override is
added. Actual loaded configs and executor/container observations must come
from retained logs/tasks, not the declared tests container.

`instrumentation-inputs.json` gives exact command, cwd, environment, limits,
wrapper hash and accepted supervisor hash. The independent reviewer must
accept these actual bytes before CLI execution. Root delivers approval;
the author may continue already-accepted native Spock attempts while waiting.

Wrapper SHA-256:

```text
73053d725dec2268c1a99c1afd7ae8aa17318df89cb188f73f37ae493080ec8e
```
