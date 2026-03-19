# Feature: Native Nextflow DSL Parser and wr Integration

Add a `nextflowdsl` library package and a `cmd` sub-command `wr nextflow` to
this repo that lets users easily run Nextflow DSL 2 workflows without using
Nextflow at all — only using wr.

## Background

There was (it may no longer work with the latest Nextflow) a Nextflow plugin
that used wr as the backend executor; see the docs at
https://workflow-runner.readthedocs.io/en/latest/integrations/nextflow.html. But
this still involved using Nextflow with various associated foibles, including the
creation of lots of little files on disk.

## Requirements

Instead, we want to be able to natively parse (pure Go) Nextflow DSL 2 along
with Nextflow config files and translate
these into something that ultimately adds commands with appropriate resource
reservations, identifiers, limits and dependencies etc. to wr's normal queue.
Without the use of any intermediate files on disk.

For dynamic workflows where the full DAG isn't apparent from the start, we need
to somehow cope with this; figure out the simplest way using existing wr
features.

Use wr's container support
(https://workflow-runner.readthedocs.io/en/latest/basics/add.html#containers) for
dealing with Nextflow workflows that use containers in their steps.

A particular run (set of inputs and other config) of a particular workflow should
be manipulable in one go with standard wr sub-commands, i.e. all commands should
share a common `rep_grp` prefix that is at least partially human-understandable
(i.e. include workflow name/path), and each step should include the step name in
the `rep_grp`, and every command should have a good `req_grp` (see
https://workflow-runner.readthedocs.io/en/latest/basics/add.html#resource-usage).

It must not be possible for an in-progress Nextflow workflow state (including
dynamic ones) to be lost if the wr manager is hard-killed. Recovery should be
reliable and automatic with a minimum of wastage on repeating things already
done.

## Notes

- Only DSL 2 is supported. DSL 1 was removed from Nextflow itself in
  v22.12 (late 2022), so all actively maintained workflows are DSL 2.
- Dynamic workflows (where the full DAG is not known upfront) are handled using
  wr's live dependency feature: child jobs auto-run when new `dep_grp` members
  are added, so the parser can add jobs incrementally as upstream steps complete.
- Workflow execution state relies entirely on wr's existing job persistence in
  its database. No separate state file or store is needed; crash recovery is
  implicit from wr's job state.
- Nextflow process resource declarations (cpus, memory, time) are parsed and
  used as initial hints with `override=0`, allowing wr's resource learning to
  refine them over time.
- The rep_grp naming convention uses hierarchical dot-separated identifiers:
  `nf.{workflow-name}.{run-id}.{process-name}`. The req_grp follows a similar
  pattern incorporating the process name so wr can learn resources per process
  type.
- The `wr nextflow` command takes a workflow file path as positional argument,
  plus optional `--config` and `--params-file` flags:
  `wr nextflow run workflow.nf --config nextflow.config --params-file params.json`.
- Run IDs are auto-generated (short hash-based) by default, with an optional
  `--run-id` flag for user override.
- For dynamic workflows, the cmd-layer client listens for job completions and
  calls the library to parse and add next-stage jobs incrementally. This avoids
  needing a separate controller process.
- Sub-commands are minimal: `wr nextflow run` (submit and optionally monitor) and
  `wr nextflow status` (show workflow-level progress). All other operations
  (kill, retry, remove) use standard wr sub-commands with the workflow's
  `rep_grp` prefix.
- Nextflow `publishDir` directives are honoured: outputs go where the workflow
  specifies.
- When a Nextflow process specifies a `container` directive, use wr's native
  container support (`with_docker` or `with_singularity`) to wrap the command.
  The container runtime choice (docker vs singularity) is a global config option.
- The library parses all actionable Nextflow process directives: resource
  directives (cpus, memory, time, disk), `env` (mapped to wr `--env`),
  `maxForks` (mapped to wr `limit_grps`), `errorStrategy`/`maxRetries` (mapped
  to wr retries), and `container` (mapped to wr container support).
- Nextflow `publishDir` is implemented via wr `on_success` behaviours with a
  `run` action that copies outputs to the specified publish directory.
- The library exposes a callback interface for dynamic workflow progression: the
  cmd layer implements the callback to receive job completion events and calls
  back into the library to parse and add next-stage jobs.
- Container runtime is selected via a `--container-runtime` flag on
  `wr nextflow run` (accepts `docker` or `singularity`; defaults to
  `singularity`).
- `wr nextflow run` submits all statically-known jobs and returns immediately.
  For dynamic workflows, a `--follow` flag causes it to block, listen for job
  completions, and add next-stage jobs incrementally until the workflow finishes.
- Nextflow `errorStrategy` mapping: `retry` maps to wr retries with
  `maxRetries`; `ignore` maps to retries=0 plus `on_failure` remove behaviour;
  `terminate` maps to retries=0. Unsupported strategies produce a warning and
  fall back to `terminate` semantics.
- `--params-file` accepts JSON or YAML (auto-detected by extension or content).
  Parameters are injected via string substitution into DSL `params.*`
  references.
- DSL 2 feature scope: core process definitions, subworkflows, and module
  imports are supported initially. Unsupported channel operators are rejected
  with clear error messages.
- `publishDir` follows Nextflow semantics: applies to all output channels from a
  process by default; per-channel patterns are supported.
- Dynamic workflow recovery: on re-run with `--follow`, the parser detects which
  jobs already exist (by rep_grp/dep_grp) and only adds missing ones, avoiding
  repeat work.
- Nextflow `-profile` support: `wr nextflow run` accepts a `-profile` flag for
  selecting execution profiles from config files.
- Dynamic resource closures (Groovy closures in directives like
  `cpus { task.input.size() < 10 ? 1 : 4 }`) produce a warning and fall back to
  wr defaults with `override=0` so wr's resource learning kicks in.
- Subworkflow processes are inlined into the parent rep_grp hierarchy with the
  subworkflow name as an intermediate level:
  `nf.workflow.run1.subwf_name.process_name`.
- Module imports resolve via local filesystem relative paths and remote
  sources (GitHub repositories, Nextflow registries). Remote modules are
  fetched (e.g. cloned/downloaded) on demand and cached locally. This is
  required from the start so that standard examples like
  `nextflow-io/hello` work out of the box.
- Core channel operators are supported: `map`, `flatMap`, `filter`, `collect`,
  `groupTuple`, `join`, `mix`, `first`, `last`, `take`. Unsupported operators
  are rejected with clear error messages.
- For dynamic output discovery, the completion callback provides the completed
  job's rep_grp and output paths (from publishDir or cwd). The parser uses
  these to resolve downstream input references.
- Groovy expression scope: literals, variable references (`params.*`, `task.*`),
  simple string interpolation, and basic arithmetic are supported. More complex
  Groovy expressions produce a warning and fall back to defaults or require user
  overrides.
- Remote module resolution initially supports GitHub `{owner}/{repo}` format
  and nf-core, but the internal interface must be pluggable so other registries
  (Nextflow Tower, arbitrary git URLs) can be added later without
  redesign.
- Dynamic workflow monitoring uses fixed-interval polling: the `--follow` mode
  polls the jobqueue server at a configurable interval for completed jobs in
  the workflow's rep_grp, then triggers next-stage parsing.
- Parameter substitution uses simple regex replacement of `params.KEY` → value.
  Nested params use dot-separated keys (e.g. `params.input.file`). No
  Groovy-style computed values. Missing `params.X` references that are not
  provided in the params file produce an error.
- If a subworkflow name collides with a process name leading to identical
  rep_grps, the parser rejects the workflow with a clear error message.
- `--follow` is idempotent: re-running with the same run-id skips
  already-submitted and already-complete jobs. This is the mechanism for
  resuming partially failed workflows.
- `publishDir` output handling is parser-driven: the parser generates
  `on_success` behaviours attached to each job that copy outputs to the
  specified publish directory. No separate output discovery step is needed.
- Remote modules are cached in a user-wide directory
  (`~/.wr/nextflow_modules/`) to avoid per-project duplication.
- When a Nextflow process has no resource directives (cpus, memory, time),
  conservative minimums (1 CPU, 128 MB RAM, 1 hour) are used with
  `override=0` so wr's resource learning refines them over time.
- When a complex Groovy closure (e.g. a dynamic resource expression) cannot
  be parsed, the parser warns and falls back to whatever static directives
  are declared on the same process (if any), with `override=0` to allow wr
  resource learning.
- Resume recovery regenerates the full DAG and re-adds all missing jobs.
  Jobs that already exist in wr (by rep_grp/dep_grp match) are skipped, so
  only truly missing intermediate jobs are re-submitted.
- The `--follow` polling interval defaults to 5 seconds and is configurable
  via a `--poll-interval` flag.
- Nested params in JSON/YAML are resolved by splitting on `.` and performing
  sequential map lookups (e.g. `params.input.file` → `{"input":{"file":"x"}}`
  traverses input → file).
- `maxForks` is mapped to a wr limit group named after the process name
  (e.g. a process `align` with `maxForks 5` creates limit group `align`
  with limit 5).
- Remote module cache is structured as
  `~/.wr/nextflow_modules/{owner}/{repo}/{ref}/{branch}`, with one directory
  per ref/branch combination.
- Resumption detection queries wr for all jobs matching the workflow's
  rep_grp prefix, compares them against the parsed DAG, and only adds jobs
  that are missing.
- The dynamic workflow completion callback receives minimal info: the
  completed job's rep_grp, output paths, and exit code.
- The module resolver plugin interface is simple: a `Resolve(spec string)`
  method that returns a local filesystem path and an error.
- `publishDir` relative paths are resolved relative to the workflow file's
  directory.
- Groovy parse-failure warnings are written to stderr with the unparsed
  expression text included for user debugging.
- `--follow` determines workflow completion when all jobs matching the
  workflow's rep_grp prefix are in a terminal state (complete, buried, or
  deleted).
- Channel data between processes is passed via file paths in working
  directories: upstream outputs are written to the job's working directory,
  and downstream processes read from the upstream job's output path.
- Output file discovery for `publishDir` uses the parsed Nextflow output
  block declarations to know expected filenames and glob patterns.
- Container images (e.g. `container "ubuntu:22.04"`) are handled by wr's
  existing `--with_docker` / `--with_singularity` support. wr does not
  pull images itself; it wraps the command in a `docker run` or
  `singularity shell` invocation, and those runtimes pull missing images
  automatically. No additional container-pulling functionality is needed
  in wr or in this spec.
