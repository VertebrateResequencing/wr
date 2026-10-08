# Mapping observer preflight review 01

INTERIM: PASS for execution of the frozen candidate observers below. This
accepts observer design and capture supervision. It awards no measured
mapping, equivalence, oracle, Item 3.1, Item 3.2 or R-UAT verdict.

Owner: `/root/nextflow_pilot_joint_review03`; queue owner: `/root`.
Worktree: `/home/ubuntu/wr`; branch: `nextflowdsl`.
Deadline: `2026-10-08T16:46:14.013942Z`.

## Frozen inputs and decisions

The root-supplied proposal SHA-256 is
`d3bbc775e36dd52e3f5b3c36af4390e81d78f241f4e9e50004eea5a4fc785ea6`.
Its bindings SHA-256 is
`d9b0241a6b41cb796853344162d5fa707b00ce5c1c07353c05577bb8ff492e8f`.
All 34 bound files independently match their recorded SHA-256 and size.
[Independent checks][checks] retain those identities and semantic results.

- Parser JVM observer: PASS. One ScriptParser is constructed before P1-P8,
  preserving source order and shared parser lifetime. Each decoded input
  reaches the unmodified TestUtils.check(parser, contents), preserving
  stripIndent, main.nf, parse/analyze, SyntaxErrorMessage cause filtering
  and line/column sorting. Count, line, column and original message retain
  separate values. Groovy properties resolve the original getter fields.
  Exceptions prevent the terminal completed record. Actual normalization
  bytes are candidate observations; source-authored candidates stay frozen.
- CLI P1-P8 raw diagnostic probes: PASS for capture only. All eight inputs
  match the source-authored normalized bytes. Count, location and message
  mappings remain unresolved until actual normalization, raw diagnostics
  and the source formatter are reviewed. Exit alone earns no parser pass.
- M1-M3 typed observers: PASS. The selected channel expressions, variadic
  mix and collect remain intact inside an entry workflow. The observer
  follows collection termination and emits Integer versus String values,
  multiplicity and list count. Engine exit and supervisor completion are
  required separately. Entry-workflow CLI execution does not establish
  original MockSession, last-result or ScriptHelper lifecycle equivalence.
- G-IN and G-OUT raw failure observers: PASS for capture. Both main.nf
  files equal accepted fixtures byte for byte. The existing input retains
  frozen bytes. Final recognition must observe the specific arity failure,
  process, affected input/output and declared 2 versus actual 1 together.
  Parse, missing-tool, missing-fixture or unrelated failure cannot pass.
  The proposal contains no executable recognition evaluator; its later
  implementation and actual diagnostics require review before a gap pass.
- G-SHAPE-FILE and G-SHAPE-LIST: PASS. The independent byte comparison
  permits only appending toList().view to each process channel. Original
  process and shell bytes remain intact. The outer collected channel list
  preserves each inner Path or List<Path>; typed observation checks Path
  before List before Integer before String. File basename, byte count and
  SHA-256 come from the actual file. Unknown runtime types fail. Final
  outcomes require successful invocation and completed supervision.
- Capture supervisor: PASS. The exact diff against the accepted Phase 2
  correction changes only owned output base, stage deadline, recorded
  environment keys and provenance label. Ownership discovery, subreaper,
  birth identities, pidfd signaling, TERM/KILL bounds and cleanup remain
  literal. Python syntax passes. No new timeout behavior is inferred from
  this source comparison; the accepted correction supplies its proof.

## Bounds and provenance

The 16 finite launch records each specify 120 seconds and owned scratch
cwd. The supervisor caps requested deadlines at the stage deadline. Root
retains serialization authority and the author retains the sole heavy slot.
The JVM parser classpath reuses actual resolved Phase 2 jars and test
classes. Its two absent Java-test/resource directories are the recorded
NO-SOURCE entries, not missing required fixture classes. No build, download
or new dependency is proposed. Typing and topic-preview flags are absent.
The relevant inherited typing/strict environment keys were unset when
independently checked; actual issued environment must still be captured.

The frozen source, expectations and accepted genuine original record were
read separately from this proposal. The original 38 Spock assertions are
assertion-proved; their successful raw diagnostic lists and Mix values
remain unobserved. These candidate launches do not rewrite that historical
boundary. Shared-parser identity observed here is candidate internal
information, not historical original identity or an engine-neutral claim.

Original-only, partial, unresolved/internal, strengthened, documented-gap,
oracle and pending-wr dispositions must remain separate in the final
record. All runtime/equivalence verdicts await actual author FINAL records,
manifest integration and the joint result review. No wr execution pass can
be awarded. P01 remains visible: 79 sealed non-executable records retain
local 0o664 evidence; portable Git identity cannot preserve group-write.

## Completion and review evidence

This is preflight round 1. The owned authority record retains the initial
review clock and immutable authority hashes. The preflight completion record
binds this report and independent checks with the actual decision clock.
The preflight source and semantic checks were read-only. No Nextflow,
Gradle, JVM candidate or Bash subject was launched by this reviewer. No
child agent, commit, push or production change occurred. Writes are limited
to this new report and owned joint-review03 scratch. All owned tool calls
finished; no owned background process, workload or wait remains live.
The reviewer remains assigned for final joint review.

[checks]:
 ../../../.tmp/agent/nextflow-conformance/research-pilot/joint-review03/
