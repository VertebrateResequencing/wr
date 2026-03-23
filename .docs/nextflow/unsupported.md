# Nextflow Features — Unsupported in wr

These Nextflow features are either impossible to support in wr's execution
model, inapplicable to wr's architecture, or deprecated by Nextflow itself.

## Process Sections

### `exec:` section

Runs a Groovy closure instead of a shell script. The process body is pure
Groovy code executed in the JVM. Example: `exec: println "Hello ${x}"`.
Very rare in production — only works with the local executor and is mainly
for toy examples. wr runs shell commands, not Groovy code. Supporting
`exec:` would require a full Groovy interpreter or transpilation of
arbitrary Groovy to shell, which is infeasible in a pure-Go implementation.

### `stage:` section (typed process syntax)

`stage:` is part of the new typed process syntax introduced experimentally
in Nextflow 25.10+. It declares staged file inputs/outputs with type
annotations. The section body is parsed and consumed but discarded with a
warning. Fully typed process definitions (including `stage:`, `topic:`,
and typed `input:`/`output:` sections) are not supported — the existing
untyped declaration model covers all production usage.

## Process Directives

### `array`

Enables job array submission on HPC schedulers (e.g. `bsub -J "name[1-100]"`).
Groups N identical jobs into one scheduler submission for efficiency. wr
already submits all jobs in appropriate LSF job arrays automatically, so
this directive is redundant.

### `debug` / `echo`

When true, forwards process stdout to the Nextflow console output. `echo`
is the deprecated name. wr already captures stdout/stderr per job
(`.nf-stdout`/`.nf-stderr` files). There is no live console to forward
to — wr is a batch scheduler, not an interactive runner. Captured output
is accessible via `wr status`.

### `executor`

Overrides the executor for a specific process. Example:
`executor 'local'` to run one process locally while the rest use
LSF/Slurm. wr uses a single scheduler backend per run. Per-process
executor switching is not supported. wr auto-solves lightweight jobs and
they will run quickly and efficiently via scheduler (LSF) submission.

### `machineType`

Specifies cloud VM instance type. Example: `machineType 'n1-standard-8'`
for Google Cloud. wr's cloud scheduler (OpenStack) uses its own
instance-type selection logic based on resource requirements. Direct
machine type specification only works for specific cloud providers, and
wr's abstraction layer handles this differently.

### `maxSubmitAwait`

Maximum time a job waits in the scheduler queue before being considered
failed. Example: `maxSubmitAwait '1h'`. wr does not have a built-in queue
wait timeout. Jobs stay queued until resources are available.

### `penv`

SGE (Sun Grid Engine) parallel environment name. Example: `penv 'smp'`.
wr does not have SGE as a scheduler backend. SGE-specific configuration
has no mapping.

### `pod`

Kubernetes pod configuration — labels, annotations, node selectors,
security contexts, etc. Example: `pod [label: 'app', value: 'genomics']`.
wr's Kubernetes scheduler has its own pod configuration model. Mapping
arbitrary Nextflow pod directives to wr's Kubernetes client would require
a comprehensive translation layer for every pod spec field.

### `resourceLabels`

Custom metadata labels attached to cloud resources for cost
tracking/management. Example: `resourceLabels project: 'genomics'`.
wr's cloud provisioning does not propagate custom labels to VMs.

### `resourceLimits`

Caps on resources (cpus, memory, time, disk) that override any higher
requests. Example: `resourceLimits cpus: 64, memory: '256.GB'`. wr does
not have per-job resource ceilings. The scheduler enforces cluster-wide
limits but does not clamp individual job requests.

### `secret`

Exposes secrets (passwords, API keys) as environment variables, sourced
from a secure store rather than plaintext config. Example:
`secret 'MY_TOKEN'`. wr has no secret management system. Secrets are
environment variables in wr, which the user sets externally.

### `stageInMode` / `stageOutMode`

Controls how input files are staged into the working directory (symlink,
link, copy, rellink) and how outputs are staged out (move, copy, rsync).
wr jobs run in their CWD with files accessed directly via paths. There is
no staging layer — inputs are referenced by absolute path in the command,
outputs are produced in-place.

## Task Properties

### `task.previousException` / `task.previousTrace`

`task.previousException` returns the exception from the previous failed
attempt; `task.previousTrace` returns the trace record. Both are only
available when `task.attempt > 1` (retried tasks). New in Nextflow 24.10.
wr does not track per-attempt exception or trace data — retries are
handled by wr's scheduler, not by the translator.

## Process Output Options

### `topic: <name>`

The `topic:` generic output option sends a process output to a named
topic channel (pub/sub pattern). Example:
`output: val('hello'), topic: my_topic`. This is part of Nextflow's
topic-based channel system alongside `Channel.topic()`. wr does not
implement topic channels — see `future.md` for `Channel.topic`.

## Channel Factories

### `Channel.fromLineage`

Queries Nextflow lineage/provenance metadata from a local store to
recreate channels from previous runs. Part of Nextflow's data lineage
feature. Very rare — brand new feature (v25+), experimental. Requires
Nextflow's lineage store, which wr does not create or maintain.

## Channel Operators

### `subscribe`

Attaches a callback closure to execute for each item emitted by a channel.
It is a terminal operator — does not produce output, just side-effects.
Very rare in production. wr does not have a callback mechanism. Jobs are
the unit of execution, and there is no way to run arbitrary Groovy
closures as side-effects when jobs complete.

### `until`

Emits items from a channel until a condition is met, then stops. Example:
`ch.until { it == 'DONE' }`. Very rare. Requires evaluating a Groovy
closure against each item at runtime to decide whether to continue. At
compile time, we do not know when to stop creating downstream jobs.

### Deprecated Operators

The following operators are deprecated by Nextflow itself. Modern
equivalents (e.g. `splitFasta | count`, `combine`) are supported.

- `countFasta` — use `splitFasta | count`
- `countFastq` — use `splitFastq | count`
- `countJson` — use `splitJson | count`
- `countLines` — use `splitText | count`
- `merge` — use `combine`

## Config Scopes

The following config scopes are Nextflow platform features — visualisation,
reporting, cloud platform integration, notifications. wr has its own
monitoring (web UI, status commands) and does not need Nextflow's reporting
infrastructure. None affect job creation or execution semantics.

- `aws` / `azure` / `google` — cloud provider executor and storage settings (wr uses OpenStack)
- `charliecloud` / `podman` / `sarus` / `shifter` — alternative container engines (wr supports Docker, Singularity, Apptainer)
- `conda` — Conda environment management settings
- `dag` — DAG visualisation output settings
- `fusion` — Fusion FUSE file system for cloud object storage
- `k8s` — Kubernetes execution settings (wr has its own K8s scheduler)
- `lineage` — data lineage/provenance metadata store
- `mail` — SMTP mail server for email notifications
- `manifest` — Pipeline metadata (name, version, author, description)
- `nextflow` — Nextflow runtime retry policy internals
- `notification` — Email/webhook notifications on completion/error
- `report` — HTML execution report settings
- `seqera` — Seqera Platform compute environment and scheduler
- `spack` — Spack package manager environment (directive is supported)
- `timeline` — Timeline HTML report settings
- `tower` / `wave` — Seqera Platform and Wave container service integration
- `trace` — Execution trace CSV settings
- `weblog` — HTTP webhook for real-time execution logging

## Container Config Settings

### `docker` / `singularity` / `apptainer` settings beyond `enabled`

Only the `enabled` flag is parsed in container config scopes. Settings
like `envWhitelist`, `registry`, `mountFlags`, `sudo`, `temp`,
`fixOwnership`, `remove`, `legacy`, and `writableDockerfile` cause parse
errors. wr manages container execution through its own container flags
and does not need these settings. `runOptions` is also not yet parsed
but could be useful — see `gaps.md`.

## publishDir Options (Cloud-Only)

### `contentType` / `storageClass` / `tags`

These publishDir options are experimental and only supported for S3
object storage in Nextflow. `contentType` sets the MIME type,
`storageClass` sets the storage tier, and `tags` attach metadata. wr
does not publish to S3 buckets, so these have no mapping.

## Include Clauses

### `addParams` / `params` on includes

`include { FOO } from './module' addParams(x: 1)` and the `params`
include clause are deprecated by Nextflow itself (since DSL2). These
clauses are not recognised by the parser and will cause errors. Pipeline
parameters should be defined in `params {}` blocks or config files
instead.

## Type Definitions

### Enum and Record Types

`enum Day { MON, TUE }` and `record FastqPair { id: String; fastq_1: Path }`
are typed definitions for structured data and type safety (Nextflow 26.04+).
These are parsed and stored, and enum values resolve in expressions, but
records and enums do not affect job creation — they are type annotations the
JVM uses for validation that wr's Go translator does not need.

## Include Sources

### `plugin/nf-*` includes

`include { FOO } from 'plugin/nf-hello'` loads processes/functions from
Nextflow JVM plugins. JVM plugins require the Nextflow runtime to load and
execute. The Go parser cannot load Java bytecode. The include is accepted
and a warning is emitted.

## Expression Evaluation

### Complex/unevaluable closures

Closures using constructs beyond what the Groovy evaluator handles —
external method calls, file I/O, complex logic chains. The Go evaluator
handles a Groovy subset. Closures referencing undefined functions,
performing I/O, or using unsupported constructs cannot be evaluated. Items
pass through unchanged with a warning.

### `do-while` loops

Not implemented in the Groovy evaluator. The `do { ... } while (cond)`
construct does not appear in the Nextflow syntax reference and is
extremely rare in Nextflow scripts. `for (x in collection)` covers
virtually all iteration needs. `while` loops (without `do`) are tracked
in `gaps.md`.
