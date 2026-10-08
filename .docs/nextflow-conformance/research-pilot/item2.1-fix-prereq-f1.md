# Item 2.1 PREREQ-F1 correction author record

PREREQ-F1 is corrected in a new supervisor version. The red reproduction,
paired green regressions and passive preservation gate passed their required
checks. Independent correction review remains pending. No original, DSL,
observer, oracle, control runtime or R-UAT-02 pass is awarded.

## Root cause and correction

The old supervisor sent tracked descendants TERM but escalated KILL only
against the parent's process group. Its detached child could survive timeout
while the wrapper recorded completed=true. The old source and successful
capture history remain unchanged; the correction is separate owned scratch.

The corrected wrapper adopts owned orphans using Linux child subreaper mode,
validates current ancestry and birth ticks, and retains stable pidfds. It
signals these handles, including descendants outside the parent's group,
rather than reusable numeric PIDs or groups. Current birth checks reject
stale identities. Normal parent exit uses the same cleanup as timeout.

TERM receives 0.5 seconds, then surviving or newly adopted descendants
receive KILL with a one-second bounded verification interval. Parent status,
reaped child statuses, signals/errors, final process states and remaining
live identities are captured. Completion requires parent status and zero
owned live survivors. Failure remains completed=false with wrapper exit 2;
timeout remains a distinct result. Zombie states are stopped, not absent.

The existing command/cwd/environment/limit/log capture format is retained.
The original JAVA_HOME and prerequisites01 Gradle cache remain the launch
resources. Effective command deadlines cannot exceed the existing stage
end. No native launch-plan/init selection or resource closure was altered.

## Red, paired and green evidence

The exact historical supervisor was copied with only BASE relocated. The
isolated red command exits 1 because its tracked detached SIGTERM-ignoring
child remains alive with completed=true. The paired honoring child stops.
Test cleanup uses the survivor's actual birth identity and stable handle;
its final observed zombie state is retained rather than reported absent.
The original reviewer proof and raw supervisor copy remain unchanged.

Four corrected subjects pass with complete actual command and capture
metadata. The detached timeout child ignores TERM and requires KILL. Its
paired honoring child stops under TERM alone. Ordinary completion stops
without signals. A detached ignoring child left by normal parent exit also
requires KILL and stops. Every green capture has zero owned live survivors.
Additional assertions reject stale birth identity signals and a non-owned
ancestor. Green process identities include parent and detached child births,
groups, signal attempts and actual final states.

The regression source and results are in correction scratch as
`test_supervision.py`, `red-results.json`, `green-results.json`, each subject's
command/JSON/stdout/stderr and wrapper stdout/stderr. `red.stdout` and
`green.stdout` retain semantic decisions. These are Python supervisor-only
subjects; the charter's F1 Bash controls and DSL remain unexecuted.

## History preservation and current route

`prerequisites-supervision-fix.json` binds the correction, old working
manifest, old supervisor, prerequisite/author/review history and accepted
launch-plan/init. Its current route references the new supervisor, retains
capture metadata and requires fresh independent review before Item 2.2.
The existing fifteen successful captures remain unchanged actual history;
no retrospective validation of their old timeout branch is claimed.

Working research-manifest gains only
`phase2_prerequisites_supervision_correction`. Every existing field, including
phase2_prerequisites, remains exact canonical JSON. The frozen manifest,
historical seal, seventeen frozen fields and 249 sealed artifacts are intact.
No old report, launch file, supervisor, capture or review record is rewritten.

The passive `verify_correction.py` validates that allowed extension, historical
bindings and actual accepted closure. It checks 2,856 source copies, 2,276
runtime/compiler files, 578 acquired resources, three reused resources,
454 JDK file/link records and ten CLI template files. The gate also binds
new captures, verifies their metadata and checks each retained birth identity
for remaining live work. It changes no file. The historical author gate
remains unchanged; its old one-extension assumption is not used to reject
this reviewed correction path or to disguise history.

Scratch `review-read-plan.md` covers all corrected behavior and new binding
fields. `before.json` records 1,308 old/protected artifacts. The working
manifest's old bytes are separately archived and checked; all other bound
files retain actual hashes, sizes and modes.

## Hash bindings

corrected supervisor:

```text
de625c1996b536afe6ca381d0978326f8e94690c7957923a4590f638430b3894
```

correction sidecar:

```text
a5432e03bdfb30da74cac0901e381ecb41e7316f2f7483aa1ffdeedb95f77ebb
```

extended working manifest:

```text
d834b2fec5bf89a32c8e99c085932d48f752501fe3fc747ee35b5a80431cd3a8
```

red results:

```text
7c7676cf52726228f04f05240dde90a91784ab20500e9b4c6f5218e9ed773578
```

green results:

```text
f4918cfa86ce63f25d1b8551d2e5b98ff5862b30f35f33d376e0c878654fc79f
```

passive corrective gate:

```text
81c95139620407a7cdba5e1d4663c984c2ecdc5aa61c12fd2c8b29313116ebd1
```

## Quality, clocks and completion

Owned Python scripts parse with Python 3.12.3 and all functions have declared
parameter/return annotations. Ruff and pyright are unavailable and were not
run; no lint or strict-type pass is claimed. nf-test, nf-core and Go checks
do not verify this bounded scratch supervision correction.

Actual correction start clock was 2026-10-08T13:37:33+00:00.
Actual final author decision clock was 2026-10-08T13:51:07.066741+00:00.
Measured elapsed effort was 814.067 seconds, or 13.568 minutes.
The stage deadline remains 14:58:40.671383 UTC. No overlapping child effort
or invented per-subject allocation is added. Final passive gate and cleanup
verification clocks are retained in scratch completion evidence.

All owned commands and waits completed. No owned live parent or descendant,
child agent, tool wait or job remains. No selected original Spock/CLI test,
observer, gap, F1 Bash control, rebuild, acquisition, product/core/lock/batch
change, commit or push occurred. Root owns independent review, launch gates,
queue/status transitions and subsequent genuine original attempts.
