# Controls observer preflight review 01

INTERIM: PASS for the six frozen F1 harness subjects with the proposed tee
observer. This accepts capture design before the author rerun. It awards
no Item 3.2 or R-UAT-04 result.

Owner: `/root/nextflow_pilot_joint_review03`; queue owner: `/root`.
Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`.
Stage deadline: `2026-10-08T16:46:14.013942Z`.

## Frozen bindings and source decision

The proposal SHA-256 is
`f399c193a1927720ca7c704af12624bde8065acf07efde6f68bdb23bd3db0cd0`.
All six script hashes match the root-frozen proposal. All six scripts parse
under Python 3.12.3. The original arity and nullable checks are copied
without rewriting; nullable expected bytes remain the frozen 13 bytes.

The PATH-local wrapper runs actual `/usr/bin/tee` with original arguments
and inherited stdin/stdout/stderr. It waits for actual tee completion,
then copies completed .stdout before its own pipeline process terminates.
The sequential Bash harness therefore cannot launch resume tee until the
fresh snapshot is retained. Fresh/resume snapshot and command records have
distinct names. No stream decoding or filename rendering replaces bytes.

Actual tee status zero permits a snapshot; numeric failure retains its
status and no successful snapshot. A signaled actual tee produces a signal
record, then the wrapper restores that signal's default action and signals
itself. Outer supervision remains responsible for all owned processes.

The controlled engine writes subject bytes and exits with the frozen 0/1
status. Its wrapper observes the actual kernel exit separately. This
acceptance is limited to the six frozen numeric-status subjects; no general
controlled-engine signal propagation claim is made. Nullable's original
pipeline observes actual tee status with pipefail absent. Stronger gates
use separate kernel exits and actual tee snapshots. BASH_ENV and SHELLOPTS
are removed before bash -ex .checks; set +e remains in unchanged checks.

The copied supervisor changes only output base, deadline, environment
selection and provenance, removing unused JVM environment injection.
Birth identity, descendant discovery, subreaper, pidfd signal ownership,
TERM/KILL bounds and completion are unchanged from the accepted correction.
Each F1 command uses the 10-second bound capped by the stage deadline.

The data gate was read independently. Identity sets, exact completed
original overlays, capture bindings, typed expectation fields and all
83 fixture bytes/modes remain required. Those controls still need measured
results and final joint review. They award no runtime mutation execution.

## Independent functional evidence

Four light synthetic checks ran in owned tee-preflight scratch:

- Actual tee versus wrapper with NUL, non-UTF-8 and newline bytes produced
  identical stdout, exact .stdout bytes and exit zero. The retained fresh
  snapshot equals the actual file bytes.
- Actual tee versus wrapper with .stdout and /dev/full arguments produced
  the same numeric failure exit and exact passthrough stdout/file bytes.
  The wrapper retained no successful snapshot.
- A blocked actual tee child received SIGTERM through its owned pidfd.
  The wrapper recorded signal 15 and itself terminated by signal 15.
  Both owned processes stopped; the wrapper did not convert it into 143.
- A subsequent successful resume invocation retained its new bytes while
  preserving the earlier fresh snapshot. Fresh and resume records differ.

[Functional evidence][evidence] retains actual commands, tee command records,
bytes/hashes, exits/signals and stopped-state observations. Synthetic
subjects prove the instrumentation boundary, not any F1 acceptance status.
The author's failed nullable race attempt must remain retained and labeled
failed; the new observer does not erase it or award that attempt success.

## Completion and remaining gates

The functional decision clock is `2026-10-08T15:02:46.294557Z`.
Review is preflight round 1. All four synthetic checks and tool calls
finished. No owned process, background job, child agent or wait remains
live. No Nextflow, Gradle, Spock or wr execution was launched. Writes are
confined to this report and owned joint-review03 scratch. Historical
mapping preflight and plan reviews remain unchanged. Final acceptance
requires all six actual F1 subjects, every applicable paired loss control,
M1 witnesses, completed author FINAL and manifest integration.

[evidence]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/joint-review03/
