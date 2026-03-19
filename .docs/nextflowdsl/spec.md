# Nextflow DSL 2 Parser and wr Integration Specification

## Overview

A pure-Go `nextflowdsl` library package parses Nextflow DSL 2 workflow
files (`.nf`) and config files, translating them into `jobqueue.Job`
slices that wr executes with proper dependencies, resource hints,
container wrapping, and output publishing. A `cmd/nextflow.go` file adds
`wr nextflow run` and `wr nextflow status` sub-commands.

DSL 2 only (DSL 1 was removed from Nextflow in v22.12). No intermediate files on
disk. No separate state store -- crash recovery relies entirely on wr's
existing job persistence. Dynamic workflows use wr's live dependency
feature plus `--follow` polling to add jobs incrementally as upstream
steps complete.

## Architecture

### Packages and files

```text
nextflowdsl/           # new public package
  doc.go               # package doc
  parse.go             # lexer + recursive-descent parser
  parse_test.go
  ast.go               # AST node types
  config.go            # Nextflow config parser
  config_test.go
  params.go            # parameter substitution
  params_test.go
  groovy.go            # minimal Groovy expression evaluator
  groovy_test.go
  translate.go         # AST -> []jobqueue.Job
  translate_test.go
  channel.go           # channel/operator runtime types
  channel_test.go
  module.go            # module resolver interface + impls
  module_test.go
cmd/
  nextflow.go          # wr nextflow {run,status} cobra commands
```

### Key types

```go
// nextflowdsl/ast.go

// Workflow is the top-level AST for a parsed .nf file.
type Workflow struct {
    Name       string
    Processes  []*Process
    SubWFs     []*SubWorkflow
    Imports    []*Import
    EntryWF    *WorkflowBlock  // entry workflow block
}

// Process is a Nextflow process definition.
type Process struct {
    Name       string
    Directives map[string]Expr    // cpus, memory, time, etc.
    Input      []*Declaration
    Output     []*Declaration
    Script     string             // shell script body
    Container  string             // container image, empty if none
    PublishDir []*PublishDir
    ErrorStrat string             // retry|ignore|terminate
    MaxRetries int
    MaxForks   int                // 0 = unlimited
    Env        map[string]string
}

// SubWorkflow is a named workflow block calling processes/sub-wfs.
type SubWorkflow struct {
    Name  string
    Body  *WorkflowBlock
}

// WorkflowBlock is the body of a workflow or subworkflow -- a
// sequence of process/operator invocations wired by channels.
type WorkflowBlock struct {
    Calls []*Call   // ordered process/subworkflow invocations
}

// Call represents a process or subworkflow invocation inside a
// workflow block, including its input channel expressions.
type Call struct {
    Target string     // process or subworkflow name
    Args   []ChanExpr // input channel expressions
}

// Import describes a module import (local or remote).
type Import struct {
    Names  []string // imported process/subworkflow names
    Source string   // path or owner/repo
    Alias  map[string]string // original->alias renames
}

// PublishDir holds a parsed publishDir directive.
type PublishDir struct {
    Path    string // target directory (may contain params)
    Pattern string // glob pattern, empty = all outputs
    Mode    string // copy|move|link (copy default)
}

// ChanExpr represents a channel expression:
// factory call, operator chain, or named reference.
type ChanExpr interface{ chanExpr() }

// Expr is a minimal Groovy expression node.
type Expr interface{ expr() }
```

```go
// nextflowdsl/translate.go

// TranslateConfig controls how the AST is translated to wr jobs.
type TranslateConfig struct {
    RunID            string   // unique run identifier
    WorkflowName     string   // human-readable workflow name
    Cwd              string   // base working directory
    ContainerRuntime string   // "docker" or "singularity"
    Params           map[string]any // resolved parameters
    Profile          string   // config profile name
}

// TranslateResult holds the output of Translate().
type TranslateResult struct {
    Jobs  []*jobqueue.Job
    // Pending is non-empty when some jobs cannot be created
    // until upstream outputs are known (dynamic workflows).
    Pending []*PendingStage
}

// PendingStage describes a stage whose jobs will be created
// after the listed dep_grps complete.
type PendingStage struct {
    Process    *Process
    AwaitDepGrps []string // dep_grps to wait for
}

// Translate converts a parsed Workflow + config into wr jobs.
func Translate(
    wf *Workflow,
    cfg *Config,
    tc TranslateConfig,
) (*TranslateResult, error)

// CompletedJob holds info about a completed upstream job,
// used to resolve pending stages into concrete jobs.
type CompletedJob struct {
    RepGrp      string
    OutputPaths []string
    ExitCode    int
}

// TranslatePending resolves a pending stage into concrete
// jobs given completed upstream job info.
func TranslatePending(
    pending *PendingStage,
    completed []CompletedJob,
    tc TranslateConfig,
) ([]*jobqueue.Job, error)
```

```go
// nextflowdsl/module.go

// ModuleResolver fetches module source and returns a local path.
type ModuleResolver interface {
    Resolve(spec string) (localPath string, err error)
}

// NewGitHubResolver returns a resolver for {owner}/{repo} specs.
// Modules are cached under ~/.wr/nextflow_modules/.
func NewGitHubResolver(cacheDir string) ModuleResolver

// NewLocalResolver returns a resolver for relative/absolute paths.
func NewLocalResolver(basePath string) ModuleResolver

// NewChainResolver tries resolvers in order.
func NewChainResolver(resolvers ...ModuleResolver) ModuleResolver
```

```go
// nextflowdsl/config.go

// Config holds parsed Nextflow configuration.
type Config struct {
    Params   map[string]any
    Profiles map[string]*Profile
    Process  *ProcessDefaults // default directives
}

// Profile holds profile-scoped config overrides.
type Profile struct {
    Process *ProcessDefaults
    Params  map[string]any
}

// ProcessDefaults holds default values for process directives.
type ProcessDefaults struct {
    Cpus      int
    Memory    int    // MB
    Time      int    // minutes
    Disk      int    // GB
    Container string
    Env       map[string]string
}

// ParseConfig parses a nextflow.config file.
func ParseConfig(r io.Reader) (*Config, error)
```

### Naming conventions

- **rep_grp**: `nf.<workflow-name>.<run-id>.<process-name>`.
  Subworkflows insert an intermediate level:
  `nf.<workflow-name>.<run-id>.<subwf>.<process-name>`.
- **req_grp**: `nf.<process-name>` (wr learns resources per process
  type).
- **dep_grp**: `nf.<run-id>.<process-name>` for top-level;
  `nf.<run-id>.<subwf>.<process-name>` inside subworkflows.
- **limit_grp**: `<process-name>:<maxForks>` when `maxForks` is
  set (e.g. process `align` with `maxForks 5` -> `align:5`).

### Resource mapping

| Nextflow directive | wr field | Notes |
|-|-|-|
| `cpus N` | `Requirements.Cores` | float64(N) |
| `memory 'X GB'` | `Requirements.RAM` | MB int |
| `time 'X h'` | `Requirements.Time` | `time.Duration` |
| `disk 'X GB'` | `Requirements.Disk` | GB int |
| `container 'img'` | `WithDocker`/`WithSingularity` | per runtime |
| `maxForks N` | `LimitGroups` | `["name:N"]` |
| `errorStrategy 'retry'` | `Retries` | `uint8(maxRetries)` |
| `errorStrategy 'ignore'` | `Retries=0`, on_failure Remove | |
| `errorStrategy 'terminate'` | `Retries=0` | default |
| `env K=V` | `EnvOverride` | compressed |
| `publishDir path` | `Behaviours` OnSuccess Run | copy cmd |

All resources use `Override=0` so wr's learning refines them.

When no resource directives exist: 1 CPU, 128 MB RAM, 1 hour, 1 GB
disk, `Override=0`.

### Error handling

- Missing required `params.*` reference: return error from `Translate`.
- Unsupported channel operator: return error from `Parse` naming the
  operator.
- Unparseable Groovy closure in directive: warn to stderr, fall back to
  static directive or defaults with `Override=0`.
- Unsupported `errorStrategy` value: warn to stderr, use `terminate`.
- rep_grp collision (subworkflow name == process name): return error
  from `Translate`.
- Remote module fetch failure: return error from `Resolve`.
- Unrecognised process directive (e.g. `scratch`, `storeDir`):
  warn to stderr, ignore.

### Channel data flow (static DAGs)

When `CwdMatters` is false, wr creates working directories via
`os.MkdirTemp` (in `mkHashedDir`), adding a random suffix that
cannot be predicted at translate time. The translator therefore
sets `CwdMatters=true` with deterministic per-process working
directories:

```text
{TranslateConfig.Cwd}/nf-work/{runId}/{processName}/
```

Subworkflow processes include the subworkflow name:

```text
{TranslateConfig.Cwd}/nf-work/{runId}/{subwf}/{processName}/
```

Since all paths are known at translate time, the translator wires
downstream `Cmd` strings with absolute references to upstream
output locations. For example, if process A outputs `out.txt`,
process B's `Cmd` references
`{Cwd}/nf-work/{runId}/A/out.txt` directly. wr's runner
automatically creates the Cwd directory (via `os.MkdirAll`) and
sets `cmd.Dir` to it for `CwdMatters=true` jobs, so no
`mkdir`/`cd` prefix is needed in the job Cmd.

### Channel data flow (dynamic workflows)

When outputs are dynamic (`path`/`file` declarations -- see D4),
the actual files produced are only known after the upstream job
completes. The `--follow` loop reads files from the known
deterministic working directory and calls `TranslatePending` to
create next-stage jobs. `CompletedJob.OutputPaths` lists files
found in the upstream working directory matching its output
declarations.

---

## A. Parsing

### A1: Parse process definitions

As a developer, I want to parse Nextflow DSL 2 process blocks, so
that each process's script, directives, inputs, and outputs are
available as AST nodes.

Handles: `process NAME { ... }` blocks with `input:`, `output:`,
`script:`, and directive sections. String literals, integer
literals, memory/time units (`'4 GB'`, `'2.h'`), and `params.*`
references are parsed as `Expr` nodes.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

```go
// Parse parses a Nextflow DSL 2 file and returns the AST.
func Parse(r io.Reader) (*Workflow, error)
```

**Acceptance tests:**

1. Given `process foo { input: val x\n output: path 'out.txt'\n
   script: 'echo hello' }`, when parsed, then result has 1 process
   named `"foo"` with 1 input decl, 1 output decl, and script
   `"echo hello"`.
2. Given a process with directives `cpus 4`, `memory '8 GB'`,
   `time '2.h'`, when parsed, then `Directives["cpus"]` evaluates
   to int 4, `Directives["memory"]` to 8192 (MB),
   `Directives["time"]` to 120 (minutes).
3. Given a process with `container 'ubuntu:22.04'`, when parsed,
   then `Container` == `"ubuntu:22.04"`.
4. Given a process with `maxForks 5`, when parsed, then `MaxForks`
   == 5.
5. Given a process with `errorStrategy 'retry'` and `maxRetries 3`,
   when parsed, then `ErrorStrat` == `"retry"` and `MaxRetries` ==
   3.
6. Given a process with `publishDir '/results'`, when parsed, then
   `PublishDir[0].Path` == `"/results"`.
7. Given a process with `publishDir '/results', pattern: '*.bam'`,
   when parsed, then `PublishDir[0].Pattern` == `"*.bam"`.
8. Given a process with `env MY_VAR: 'value'`, when parsed, then
   `Env["MY_VAR"]` == `"value"`.
9. Given input with no process blocks, when parsed, then result has
   0 processes and no error.
10. Given syntactically invalid DSL (unclosed brace), when parsed,
    then error contains the line number.
11. Given a process with `disk '10 GB'`, when parsed, then
    `Directives["disk"]` evaluates to int 10.
12. Given a legacy DSL 1 file (uses `into` channel assignment,
    e.g. `process foo { output: stdout into result }`), when
    parsed, then error mentions DSL 1 is not supported.
13. Given a process with `scratch true`, when parsed, then parse
    succeeds without error and `Directives` map does not contain
    key `"scratch"`.

### A2: Parse workflow blocks

As a developer, I want to parse workflow blocks that wire processes
via channels, so that the execution DAG can be derived.

Handles: named `workflow NAME { ... }` blocks and the unnamed entry
workflow. Process calls with channel arguments, pipe operator
(`|`), and channel variable assignments.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `workflow { foo(ch) ; bar(foo.out) }`, when parsed, then
   `EntryWF.Calls` has 2 entries: `foo` and `bar`.
2. Given `workflow named { foo(x) }`, when parsed, then
   `SubWFs[0].Name` == `"named"` and its body has 1 call.
3. Given `ch = Channel.of(1,2,3)\nworkflow { foo(ch) }`, when
   parsed, then `foo`'s first arg is a `ChanExpr` referencing the
   `Channel.of` factory.
4. Given `workflow { foo(Channel.fromPath('/data/*.fq')) }`, when
   parsed, then `foo`'s arg is a `Channel.fromPath` factory call
   with glob `"/data/*.fq"`.

### A3: Parse channel factories and operators

As a developer, I want to parse channel factory calls and operator
chains, so that data flow between processes is fully represented.

Supported factories: `Channel.of()`, `Channel.fromPath()`,
`Channel.fromFilePairs()`, `Channel.value()`, `Channel.empty()`.

Supported operators: `map`, `flatMap`, `filter`, `collect`,
`groupTuple`, `join`, `mix`, `first`, `last`, `take`.

Unsupported operators produce an error naming the operator.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `Channel.of(1,2,3).map { it * 2 }`, when parsed, then
   the ChanExpr is a factory `of` with 3 args chained to a `map`
   operator.
2. Given `Channel.fromFilePairs('/data/*_{1,2}.fq')`, when parsed,
   then factory is `fromFilePairs` with glob arg.
3. Given `Channel.empty()`, when parsed, then factory is `empty`
   with 0 args.
4. Given `Channel.value('hello')`, when parsed, then factory is
   `value` with string arg `"hello"`.
5. Given `ch.filter { it > 5 }.collect()`, when parsed, then chain
   has 2 operators: `filter` and `collect`.
6. Given `ch.groupTuple()`, when parsed, then operator is
   `groupTuple`.
7. Given `ch.join(other)`, when parsed, then operator is `join`
   with 1 channel arg.
8. Given `ch.mix(a, b)`, when parsed, then operator is `mix` with
   2 channel args.
9. Given `ch.first()`, when parsed, then operator is `first`.
10. Given `ch.flatMap { it.split(',') }`, when parsed, then
    operator is `flatMap`.
11. Given `ch.last()`, when parsed, then operator is `last`.
12. Given `ch.take(3)`, when parsed, then operator is `take` with
    int arg 3.
13. Given `ch.buffer(size: 3)`, when parsed, then error message
    contains `"unsupported operator: buffer"`.

### A4: Parse import statements

As a developer, I want to parse `include` statements for module
imports, so that remote and local modules can be resolved.

Handles: `include { NAME } from './path'` and
`include { NAME as ALIAS } from 'owner/repo'`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/parse.go`
**Test file:** `nextflowdsl/parse_test.go`

**Acceptance tests:**

1. Given `include { foo } from './modules/foo'`, when parsed, then
   `Imports[0].Names` == `["foo"]`, `Source` == `"./modules/foo"`.
2. Given `include { foo ; bar } from './lib'`, when parsed, then
   `Names` == `["foo", "bar"]`.
3. Given `include { foo as myFoo } from 'nf-core/modules'`, when
   parsed, then `Alias["foo"]` == `"myFoo"`, `Source` ==
   `"nf-core/modules"`.
4. Given `include { foo } from` (missing source), when parsed, then
   error mentions missing module source.

---

## B. Configuration and Parameters

### B1: Parse Nextflow config files

As a developer, I want to parse `nextflow.config` files, so that
profiles, default process directives, and params are available.

Handles: `params { }`, `process { }`, `profiles { }` blocks.
Profile selection merges profile-scoped values over defaults.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/config.go`
**Test file:** `nextflowdsl/config_test.go`

```go
func ParseConfig(r io.Reader) (*Config, error)
```

**Acceptance tests:**

1. Given config `params { input = '/data' ; output = '/out' }`,
   when parsed, then `Config.Params["input"]` == `"/data"`.
2. Given config with `process { cpus = 2 ; memory = '4 GB' }`,
   when parsed, then `ProcessDefaults.Cpus` == 2,
   `ProcessDefaults.Memory` == 4096.
3. Given config with `profiles { test { params { input = '/test' }
   } }`, when parsed with profile `"test"`, then
   `Profiles["test"].Params["input"]` == `"/test"`.
4. Given config with `process { container = 'ubuntu:22.04' }`,
   when parsed, then `ProcessDefaults.Container` ==
   `"ubuntu:22.04"`.
5. Given empty config, when parsed, then `Config` has zero-value
   fields and no error.
6. Given syntactically invalid config (unmatched brace), when
   parsed, then error contains line number.

### B2: Parameter substitution

As a developer, I want `params.*` references in DSL files to be
replaced with values from config + params-file + CLI, so that
workflows are parameterised.

Params-file is JSON or YAML (auto-detected by extension: `.json`
or `.yml`/`.yaml`; files with ambiguous extensions are
content-sniffed). Nested
params use dot-separated keys resolved by sequential map lookup.
Missing referenced params produce an error.

Both `${params.KEY}` (interpolated) and bare `params.KEY` forms
are recognised and substituted.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/params.go`
**Test file:** `nextflowdsl/params_test.go`

```go
// LoadParams reads a JSON or YAML params file.
func LoadParams(path string) (map[string]any, error)

// MergeParams merges config params, file params, and CLI params
// (later sources override earlier). CLI params come from the
// --param / -P flag (repeatable, KEY=VALUE format).
func MergeParams(sources ...map[string]any) map[string]any

// SubstituteParams replaces params.KEY references in s with
// values from params. Returns error if a referenced param is
// missing.
func SubstituteParams(s string, params map[string]any) (string, error)
```

**Acceptance tests:**

1. Given params `{"input": "/data"}` and string
   `"cat ${params.input}/file.txt"`, when substituted, then result
   is `"cat /data/file.txt"`.
2. Given params `{"input":{"file":"x.fq"}}` and string
   `"${params.input.file}"`, when substituted, then result is
   `"x.fq"`.
3. Given params `{"a":"1"}` and string `"${params.missing}"`, when
   substituted, then error contains `"params.missing"`.
4. Given file `params.json` containing `{"x":1}`, when loaded, then
   result is `map["x"] = 1`.
5. Given file `params.yaml` containing `x: 1`, when loaded, then
   result is `map["x"] = 1`.
6. Given config params `{"a":"1"}` and file params `{"a":"2"}`,
   when merged, then `"a"` == `"2"` (file overrides config).
7. Given params `{"input": "/data"}` and string
   `"cat params.input/file.txt"` (bare form, no braces), when
   substituted, then result is `"cat /data/file.txt"`.
8. Given config params `{"input":"/cfg"}`, file params
   `{"input":"/file"}`, and CLI params `{"input":"/cli"}`,
   when merged in order (config, file, CLI), then
   `"input"` == `"/cli"` (CLI overrides file overrides config).

### B3: Minimal Groovy expression evaluator

As a developer, I want simple Groovy expressions in directives
evaluated, so that literal values, `params.*`, `task.*`,
arithmetic, and string interpolation work.

Complex closures that cannot be parsed produce a warning on stderr
and fall back to defaults with `Override=0`.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/groovy.go`
**Test file:** `nextflowdsl/groovy_test.go`

```go
// EvalExpr evaluates a simple Groovy expression given a context
// of variable bindings. Returns the result or an error.
func EvalExpr(expr Expr, vars map[string]any) (any, error)
```

**Acceptance tests:**

1. Given integer literal `4`, when evaluated, then result is
   `int(4)`.
2. Given `params.cpus` with vars `{"params":{"cpus":8}}`, when
   evaluated, then result is `int(8)`.
3. Given `"hello ${name}"` with vars `{"name":"world"}`, when
   evaluated, then result is `"hello world"`.
4. Given `2 + 3`, when evaluated, then result is `int(5)`.
5. Given `task.cpus * 2` with vars `{"task":{"cpus":4}}`, when
   evaluated, then result is `int(8)`.
6. Given a complex closure `{ task.input.size() < 10 ? 1 : 4 }`,
   when evaluated, then error is returned (non-nil) indicating
   unsupported expression.
7. Given a complex closure `{ task.input.size() < 10 ? 1 : 4 }`,
   when evaluated, then the error message contains the original
   expression text `"task.input.size() < 10 ? 1 : 4"`.

---

## C. Module Resolution

### C1: Local module resolution

As a developer, I want `include` paths starting with `./` or `/`
resolved against the workflow file's directory, so that local
modules work.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/module.go`
**Test file:** `nextflowdsl/module_test.go`

**Acceptance tests:**

1. Given base path `/work` and spec `./lib/foo.nf`, when resolved,
   then local path is `/work/lib/foo.nf`.
2. Given base path `/work` and spec `/abs/foo.nf`, when resolved,
   then local path is `/abs/foo.nf`.
3. Given spec `./missing.nf` and file does not exist, when
   resolved, then error contains the missing path.

### C2: Remote module resolution (GitHub)

As a developer, I want `include` sources like `owner/repo` fetched
from GitHub and cached in `~/.wr/nextflow_modules/`, so that
standard nf-core modules work out of the box.

Cache structure:
`~/.wr/nextflow_modules/{owner}/{repo}/{revision}/`, where
`{revision}` is a git ref, branch name, or tag (e.g. `main`,
`v1.0`).

Uses `git clone --depth 1` (or archive download). The interface is
pluggable for future registries.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/module.go`
**Test file:** `nextflowdsl/module_test.go`

**Acceptance tests:**

1. Given spec `nextflow-io/hello` and empty cache, when resolved,
   then local path exists under
   `~/.wr/nextflow_modules/nextflow-io/hello/` and contains at
   least one `.nf` file.
2. Given spec `nextflow-io/hello` and cache already populated, when
   resolved, then no network fetch occurs (cache hit) and the same
   path is returned.
3. Given spec `nonexistent/repo999`, when resolved, then error
   mentions fetch failure.
4. Given spec `owner/repo` with explicit revision `main`, when
   resolved, then cache path includes `main`.

### C3: Chain resolver

As a developer, I want a chain resolver that tries local then
remote, so that `include` statements resolve transparently.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/module.go`
**Test file:** `nextflowdsl/module_test.go`

**Acceptance tests:**

1. Given chain [local, github] and a spec `./local.nf` that exists
   locally, when resolved, then local resolver handles it.
2. Given chain [local, github] and a spec `owner/repo`, when
   resolved, then github resolver handles it (local returns error).

---

## D. Translation to wr Jobs

### D1: Static DAG translation

As a developer, I want `Translate()` to convert a parsed workflow
into `[]jobqueue.Job` with correct dep_grps, deps, rep_grp,
req_grp, and resources, so that wr executes the workflow.

Each process invocation becomes one or more jobs. Upstream ->
downstream edges become dep_grp dependencies. CwdMatters is true
with deterministic per-process working directories (see Channel
data flow above) so downstream jobs can reference upstream
output paths at translate time.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given workflow `A | B` (A feeds B), when translated with
   run-id `"r1"`, workflow-name `"mywf"`, and Cwd `"/work"`, then
   result has 2 jobs. Job A: `DepGroups` contains `"nf.r1.A"`,
   `RepGroup` == `"nf.mywf.r1.A"`, `ReqGroup` == `"nf.A"`,
   `CwdMatters` == true, `Cwd` == `"/work/nf-work/r1/A"`. Job B:
   `Dependencies` contains dep on `"nf.r1.A"`, `RepGroup` ==
   `"nf.mywf.r1.B"`, `CwdMatters` == true, `Cwd` ==
   `"/work/nf-work/r1/B"`, and `Cmd` contains the string
   `"/work/nf-work/r1/A/"` (reference to A's output directory).
2. Given process with `cpus 4`, `memory '8 GB'`, `time '2.h'`,
   when translated, then job `Requirements` has `Cores` == 4.0,
   `RAM` == 8192, `Time` == 2h, `Override` == 0.
3. Given process with no resource directives, when translated, then
   job `Requirements` has `Cores` == 1, `RAM` == 128, `Time` ==
   1h, `Disk` == 1, `Override` == 0.
4. Given process with `container 'ubuntu:22.04'` and
   `ContainerRuntime` == `"singularity"`, when translated, then job
   `WithSingularity` == `"ubuntu:22.04"`, `WithDocker` == `""`.
5. Given process with `container 'ubuntu:22.04'` and
   `ContainerRuntime` == `"docker"`, when translated, then job
   `WithDocker` == `"ubuntu:22.04"`.
6. Given process with no `container` directive, when translated,
   then both `WithDocker` and `WithSingularity` are empty.
7. Given process named `proc` with `maxForks 5`, when translated,
   then job `LimitGroups` contains `"proc:5"`.
8. Given process with `errorStrategy 'retry'` and `maxRetries 3`,
   when translated, then job `Retries` == 3.
9. Given process with `errorStrategy 'ignore'`, when translated,
   then job `Retries` == 0 and `Behaviours` includes an OnFailure
   Remove action.
10. Given process with `errorStrategy 'terminate'`, when translated,
    then job `Retries` == 0 and no special failure behaviour.
11. Given process with unsupported `errorStrategy 'finish'`, when
    translated, then job `Retries` == 0 (terminate semantics) and
    a warning is emitted.
12. Given workflow with 3 sequential processes A -> B -> C, when
    translated, then B depends on A's dep_grp and C depends on B's
    dep_grp.
13. Given process with `env MY_VAR: 'hello'`, when translated, then
    job `EnvOverride` (when decompressed) contains `"MY_VAR=hello"`.
14. Given process with `disk '10 GB'`, when translated, then job
    `Requirements.Disk` == 10.
15. Given a process with no directives and a Config with
    `ProcessDefaults.Cpus` == 2, when translated, then job
    `Requirements.Cores` == 2 (not the hardcoded default of 1).
16. Given a process whose Script contains `${params.input}` and
    `TranslateConfig.Params` has `{"input":"/data"}`, when
    translated, then the job `Cmd` contains `/data` in place of
    the params reference.
17. Given a process with `cpus { task.input.size() < 10 ? 1 : 4 }`
    (unparseable closure) and `memory '8 GB'` (static), when
    translated, then `Requirements.Cores` == 1 (default fallback),
    `Requirements.RAM` == 8192 (static directive preserved),
    `Override` == 0, and a warning is emitted to stderr containing
    the unparsed closure text.
18. Given a Config with `ProcessDefaults.Cpus` == 2 and a selected
    profile whose `Process.Cpus` == 8, and a process with no
    directives, when translated, then `Requirements.Cores` == 8
    (profile overrides global defaults).
19. Given diamond DAG where process C takes input from both A and
    B (`A -> C`, `B -> C`), when translated with run-id `"r1"`
    and Cwd `"/work"`, then C's `Dependencies` contains dep_grps
    `"nf.r1.A"` and `"nf.r1.B"`, and C has `Cwd` ==
    `"/work/nf-work/r1/C"`.

### D2: publishDir translation

As a developer, I want `publishDir` directives translated to
`on_success` behaviours that copy outputs to the publish directory,
so that workflow results land where specified.

Relative publishDir paths are resolved relative to the workflow
file's directory.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given process with `publishDir '/results'` and output
   `path 'out.txt'`, when translated, then job `Behaviours`
   contains an OnSuccess Run behaviour whose command copies
   `out.txt` to `/results/`.
2. Given process with `publishDir '/results', pattern: '*.bam'`,
   when translated, then the copy command uses glob `*.bam`.
3. Given process with `publishDir 'results'` (relative) and
   workflow file at `/work/main.nf`, when translated, then copy
   target is `/work/results/`.
4. Given process with no publishDir, when translated, then no
   OnSuccess Run behaviour is present.
5. Given process with 2 publishDir directives (different paths),
   when translated, then 2 OnSuccess Run behaviours are present.
6. Given process with `publishDir '/results', mode: 'link'` and
   output `path 'out.txt'`, when translated, then the OnSuccess
   Run behaviour command uses `ln` (not `cp`) to link `out.txt`
   into `/results/`.
7. Given process with `publishDir '/results', mode: 'move'` and
   output `path 'out.txt'`, when translated, then the OnSuccess
   Run behaviour command uses `mv` (not `cp`) to move `out.txt`
   into `/results/`.
8. Given process with `publishDir '${params.outdir}'` and
   `TranslateConfig.Params` has `{"outdir":"/results"}`, when
   translated, then the OnSuccess copy target is `/results/`.

### D3: Subworkflow translation

As a developer, I want subworkflow processes inlined with the
subworkflow name in the rep_grp hierarchy, so that status reporting
distinguishes them.

rep_grp: `nf.<workflow>.<run-id>.<subwf>.<process>`.

If a subworkflow name collides with a process name causing
identical rep_grps, Translate returns an error.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given subworkflow `prep` containing process `trim`, when
   translated with workflow `"mywf"`, run-id `"r1"`, and
   Cwd `"/work"`, then trim's
   `RepGroup` == `"nf.mywf.r1.prep.trim"`,
   `DepGroups` contains `"nf.r1.prep.trim"`, and
   `Cwd` == `"/work/nf-work/r1/prep/trim"`.
2. Given subworkflow `foo` and top-level process `foo`, when
   translated, then error mentions rep_grp collision.
3. Given two subworkflows `align` and `qc`, each containing a
   process named `sort`, when translated with run-id `"r1"` and
   Cwd `"/work"`, then `align.sort` gets
   `DepGroups` containing `"nf.r1.align.sort"` and
   `Cwd` == `"/work/nf-work/r1/align/sort"`, and `qc.sort` gets
   `DepGroups` containing `"nf.r1.qc.sort"` and
   `Cwd` == `"/work/nf-work/r1/qc/sort"`. A downstream process
   in `qc` depending on `align.sort` has `Dependencies`
   containing dep on `"nf.r1.align.sort"`.

### D4: Dynamic workflow detection

As a developer, I want `Translate()` to identify stages that depend
on runtime-determined outputs, so that the `--follow` loop knows
which stages to defer.

**Classification rule:** all `path` and `file` output
declarations are dynamic (pending) because the actual files
produced may differ from declarations (globs, optional outputs,
runtime-generated names). Only `val` outputs are static. A
stage is "pending" when any of its input channels derive from
an upstream process's `path`/`file` output.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/translate.go`
**Test file:** `nextflowdsl/translate_test.go`

**Acceptance tests:**

1. Given workflow `A | B` where A's output is `path '*.txt'`
   (dynamic glob), when translated, then `Pending` has 1 entry
   for process B awaiting A's dep_grp.
2. Given workflow `A | B` where A's output is `val x` (static),
   when translated, then `Pending` is empty and both jobs are in
   `Jobs`.
3. Given a pending stage for process B, when `TranslatePending`
   is called with `[]CompletedJob{{RepGrp: "nf.mywf.r1.A",
   OutputPaths: []string{"/tmp/out.txt"}, ExitCode: 0}}`,
   then result contains concrete jobs for B with Cmd
   referencing `/tmp/out.txt`.
4. Given workflow `A | B` where A's output is
   `path 'result.txt'` (literal filename, not a glob), when
   translated, then `Pending` has 1 entry for process B
   (all `path`/`file` outputs are dynamic per the
   classification rule).

### D5: Channel factory resolution

As a developer, I want channel factories resolved at translate
time to determine how many data items they produce, so that the
correct number of jobs is created per process.

Supported factories and resolution:
- `Channel.of(a, b, c)` -- N items from literal args.
- `Channel.value(x)` -- 1 item (single value).
- `Channel.empty()` -- 0 items.
- `Channel.fromPath(glob)` -- N items from filesystem glob.
- `Channel.fromFilePairs(glob)` -- N pairs from filesystem glob.

When a channel resolves to N > 1 items, the consuming process
gets N jobs. Each job has:
- `Cwd`: `{Cwd}/nf-work/{runId}/{process}/{itemIdx}/`
- `DepGroups`: `["nf.{runId}.{process}.{itemIdx}"]`
- `RepGroup`: `nf.{workflow}.{runId}.{process}` (shared)
- `Cmd` containing the item's value or file path.

When N == 1, the standard (unindexed) convention from D1 applies.
When N == 0, no jobs are created for the consuming process.

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/channel_test.go`

```go
// ResolveChannel evaluates a ChanExpr (factory + operator chain)
// and returns the resulting data items. File-based factories
// glob the filesystem at translate time.
func ResolveChannel(ce ChanExpr, cwd string) ([]any, error)
```

**Acceptance tests:**

1. Given `Channel.of(1,2,3)` feeding process `foo`, when
   translated with run-id `"r1"`, workflow `"wf"`, and Cwd
   `"/work"`, then 3 jobs created for `foo`: each has
   `RepGroup` == `"nf.wf.r1.foo"`, `Cwd` values
   `"/work/nf-work/r1/foo/0"`, `.../1`, `.../2`, and
   `DepGroups` containing `"nf.r1.foo.0"`, `.1`, `.2`
   respectively. Each job's `Cmd` contains its input value
   (`"1"`, `"2"`, or `"3"`).
2. Given `Channel.fromPath('/data/*.fq')` matching files
   `a.fq`, `b.fq`, `c.fq` in `/data/`, when translated, then
   3 jobs created for the consuming process, each with a
   different file path (`/data/a.fq`, etc.) in `Cmd`.
3. Given `Channel.fromFilePairs('/data/*_{1,2}.fq')` matching
   pairs `(sample1_1.fq, sample1_2.fq)` and
   `(sample2_1.fq, sample2_2.fq)`, when translated, then 2
   jobs created.
4. Given `Channel.value('x')` feeding process `foo`, when
   translated, then 1 job created for `foo` with `Cmd`
   containing `"x"`.
5. Given `Channel.empty()` feeding process `foo`, when
   translated, then 0 jobs created for `foo`.
6. Given `Channel.fromPath('/data/*.fq')` matching 0 files,
   when translated, then 0 jobs created (empty channel).

### D6: Channel operator effects on translation

As a developer, I want channel operators to transform item
cardinality during translation, so that fan-in, fan-out, and
filtering produce the correct number of downstream jobs.

Operators modify the resolved item list before job creation:
- `collect()` -- N items -> 1 list. Downstream gets 1 job
  whose `Dependencies` contains all N upstream dep_grps.
- `first()` -- N items -> 1 (first item).
- `last()` -- N items -> 1 (last item).
- `take(K)` -- N items -> min(K, N) items.
- `filter { pred }` -- N items -> M items (M <= N).
- `map { transform }` -- N items -> N items (1:1).
- `flatMap { transform }` -- N items -> M items (M >= 0).
- `mix(other)` -- merges channels (N + M items).
- `join(other)` -- combines by key (K matching pairs).
- `groupTuple()` -- groups by first element (K groups).

**Package:** `nextflowdsl/`
**File:** `nextflowdsl/channel.go`
**Test file:** `nextflowdsl/channel_test.go`

**Acceptance tests:**

1. Given 3-element channel (from `Channel.of(1,2,3)`) ->
   `collect()` -> process `merge`, when translated with
   run-id `"r1"`, then 1 `merge` job whose `Dependencies`
   contains dep_grps `"nf.r1.upstream.0"`, `.1`, `.2` (all
   3 upstream dep_grps).
2. Given 3-element channel -> `first()` -> process `peek`,
   when translated, then 1 `peek` job (first item only).
3. Given 6-element channel with keys
   `[(a,1),(a,2),(a,3),(b,4),(b,5),(b,6)]` ->
   `groupTuple()`, when translated, then 2 downstream jobs
   (one per group key `a` and `b`).
4. Given 5-element channel `[1,2,3,4,5]` ->
   `filter { it > 3 }` -> process `foo`, when translated,
   then 2 jobs for `foo` (items 4 and 5).
5. Given `ch1` (2 elements) `.mix(ch2)` (3 elements) ->
   process `bar`, when translated, then 5 jobs for `bar`.
6. Given diamond DAG where process C depends on both A.out
   and B.out via `a_out.mix(b_out)` (1 element each), when
   translated, then 2 jobs for C with distinct deps: one
   depending on A's dep_grp, one on B's dep_grp.

---

## E. Command Layer

### E1: `wr nextflow run` sub-command

As a user, I want `wr nextflow run workflow.nf` to parse and submit
all statically-known jobs to wr, so that my Nextflow workflow runs
on wr.

Flags:
- `--config` / `-c`: path to nextflow.config
- `--params-file` / `-p`: path to params JSON/YAML
- `--param` / `-P`: repeatable, `KEY=VALUE` format; overrides
  config and file params (like Nextflow's `-params.KEY=VALUE`)
- `--run-id`: override auto-generated run ID
- `--container-runtime`: `docker` or `singularity` (default
  `singularity`)
- `--follow` / `-f`: block and poll for dynamic workflow
  progression
- `--poll-interval`: duration (default 5s)
- `--profile`: select config profile

Positional arg: workflow file path.

Auto-generates run-id as short hash if not provided.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** (tested via integration; library logic tested in
`nextflowdsl/`)

```go
// cmd/nextflow.go -- cobra commands
// nextflowCmd: parent "wr nextflow"
// nextflowRunCmd: "wr nextflow run"
// nextflowStatusCmd: "wr nextflow status"
```

**Acceptance tests:**

1. Given a valid `workflow.nf` with 2 processes A -> B, when
   `wr nextflow run workflow.nf` is invoked (no `--run-id`), then
   jobs are added to wr with correct rep_grps and deps, the
   auto-generated run-id in each rep_grp is a lowercase hex string
   of at least 8 characters matching `[0-9a-f]{8,}`, and the
   command exits 0.
2. Given `--run-id myrun`, when invoked, then all jobs have
   `"myrun"` in their rep_grp.
3. Given `--config nf.config` with params, when invoked, then
   params from config are applied to job commands.
4. Given `--params-file params.json`, when invoked, then file
   params override config params.
5. Given `--container-runtime docker`, when invoked, then jobs with
   container directives use `WithDocker`.
6. Given `--profile test` with config containing a test profile,
   when invoked, then profile settings are applied.
7. Given a workflow file that does not exist, when invoked, then
   error message names the missing file and exit code is non-zero.
8. Given a workflow with syntax errors, when invoked, then parse
   error with line number is printed and exit code is non-zero.
9. Given `--param input=/override` with config params
   `{"input":"/cfg"}` and file params `{"input":"/file"}`,
   when invoked, then the job Cmd uses `/override` (CLI --param
   wins over both config and file params).

### E2: `wr nextflow run --follow` dynamic polling

As a user, I want `--follow` to block, poll for completed jobs, and
submit next-stage jobs until the workflow finishes, so that dynamic
workflows complete without manual intervention.

Polls jobqueue server at `--poll-interval` for completed jobs in
the workflow's rep_grp prefix. On completion, builds
`[]CompletedJob` from the finished jobs and calls
`TranslatePending` for each pending stage to create next-stage
jobs. Exits when all jobs matching the rep_grp prefix are in
terminal state (complete, buried, or deleted). Exits non-zero
if any job is buried, signalling failure; the user then resumes
via `wr nextflow run --follow --run-id <id>` (see E3).

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** (tested via integration)

**Acceptance tests:**

1. Given a dynamic workflow A -> B (B is pending), when `--follow`
   is used and A completes, then `TranslatePending` is called with
   A's `CompletedJob` and B's concrete jobs are added to wr.
2. Given `--follow` and all jobs complete, then the command exits 0.
3. Given `--follow` and a job is buried (unrecoverable), then the
   command exits non-zero after all other jobs reach terminal state.
4. Given `--follow --poll-interval 1s`, then polling occurs at 1s
   intervals (not 5s default).

### E3: `wr nextflow run` resume/recovery

As a user, I want re-running `wr nextflow run --follow` with the
same `--run-id` to skip already-submitted and already-complete
jobs, so that I can resume a partially failed workflow.

The command regenerates the full DAG, queries wr for existing jobs
by rep_grp prefix, and only adds jobs that are missing. Buried
jobs are automatically removed and re-added so recovery is fully
automatic.

Resume behaviour per wr job state:

| Job state | Resume action |
|-|-|
| complete | skip (already done) |
| running | skip (still executing) |
| dependent | skip (still waiting) |
| buried | remove from wr, then re-add |
| deleted | re-add |

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** (tested via integration)

**Acceptance tests:**

1. Given run-id `"r1"` with A complete and B not yet submitted,
   when `wr nextflow run --follow --run-id r1` is invoked, then A
   is not re-added (already exists) and B is added.
2. Given run-id `"r1"` with all jobs complete, when invoked, then
   0 jobs are added and exit is 0.
3. Given run-id `"r1"` with B in buried state, when resume is
   invoked, then B is first removed from wr and then re-added
   as a fresh job (not skipped), and A (complete) is skipped.
4. Given run-id `"r1"` with A complete and B deleted, when
   resume is invoked, then B is re-added and A is skipped.

### E4: `wr nextflow status` sub-command

As a user, I want `wr nextflow status` to show workflow-level
progress, so that I can monitor running Nextflow workflows.

Shows per-process counts of pending/running/complete/buried jobs
for all workflows or a specific run-id.

Flags:
- `--run-id` / `-r`: filter to specific run
- `--workflow` / `-w`: filter by workflow name

Output format: table with columns: Process, Pending, Running,
Complete, Buried, Total.

**Package:** `cmd/`
**File:** `cmd/nextflow.go`
**Test file:** (tested via integration)

**Acceptance tests:**

1. Given 3 jobs with rep_grp prefix `"nf.mywf.r1"` in states
   dependent, running, complete, when `wr nextflow status -r r1`
   is invoked, then output shows correct counts per process.
2. Given no jobs matching the run-id, when invoked, then output
   says "no jobs found" and exit is 0.
3. Given jobs from 2 workflows `"wf1"` and `"wf2"` with rep_grps
   `"nf.wf1.r1.*"` and `"nf.wf2.r2.*"`, when
   `wr nextflow status -w wf1` is invoked, then output shows
   only processes from `wf1`.

---

## Implementation Order

### Phase 1: Core parsing (A1, A2, A3, A4)

Sequential. Lexer/parser foundation. All four stories build on the
same parser. Test with in-memory strings; no wr dependency.

### Phase 2: Config, params, Groovy (B1, B2, B3)

Sequential within phase; not parallelisable with other phases
(depends on Phase 1 `Expr` types). Config parser reuses
lexer patterns. Params and Groovy evaluator are independent
of each other but both depend on `Expr` from Phase 1.

### Phase 3: Module resolution (C1, C2, C3)

C1 and C2 are independent (can be parallel). C3 depends on both.
C2 tests use `t.TempDir()` as cache dir; the real-network test
(GitHub clone) should be skipped in CI via a short timeout or
mock.

### Phase 4: Translation (D1, D2, D3, D4, D5, D6)

Sequential. D1 is the core; D2-D4 build on it. D5 (channel
factory resolution) and D6 (operator cardinality) depend on D1
and on the AST channel types from Phase 1. D5 and D6 can be
parallel with D2-D4. Depends on Phases 1-3 for AST, config,
and module resolution.

### Phase 5: Command layer (E1, E2, E3, E4)

Sequential. E1 first (basic run), E2 (follow), E3 (resume), E4
(status). Depends on Phase 4 for `Translate()`. E1 can be started
once D1 is complete. E2-E4 depend on E1.

---

## Appendix: Key Decisions

1. **DSL 2 only.** DSL 1 was removed from Nextflow in v22.12;
   parser rejects legacy DSL 1 syntax with a clear error.
2. **No intermediate files.** Channel data flows via file paths in
   working directories, not serialised channel state.
3. **Override=0 always.** wr's resource learning is the primary
   mechanism; Nextflow directives are initial hints.
4. **Live deps for dynamic workflows.** wr's existing dep_grp
   mechanism handles jobs added after their dependents.
5. **Testing.** GoConvey for all unit tests per go-conventions.
   Remote module tests (C2 acceptance test 1) may use a mock HTTP
   server or be gated by an env var for CI. Integration tests for
   cmd layer require a running wr manager.
6. **Error policy.** Parse/translate errors are fatal (returned).
   Groovy closure failures and unsupported errorStrategy values
   are warnings (stderr) with safe fallbacks.
7. **Limit group format.** Uses wr's `name:count` convention
   with just the process name (e.g. `"align:5"`), which wr
   parses automatically.
8. **publishDir via Run behaviour.** The `on_success` behaviour
   runs a shell `cp` (or `mv`/`ln` per mode) command in the job's
   deterministic working directory, matching Nextflow semantics.
9. **Container images are not pulled by this code.** wr's
   `WithDocker`/`WithSingularity` fields cause `docker run` /
   `singularity shell` invocations that pull missing images
   automatically.
10. **Module cache directory.** `~/.wr/nextflow_modules/` to avoid
    per-project duplication. Structured as
    `{owner}/{repo}/{revision}/` where `{revision}` is a single
    path component covering git refs, branches, and tags (e.g.
    `main`, `v1.0`). An alternative would be 4 levels
    (`{owner}/{repo}/{ref}/{branch}`), but since branches and
    tags are both git refs, a single `{revision}` level avoids
    redundancy and simplifies cache lookups.
11. **CwdMatters=true for deterministic paths.** wr's
    `CwdMatters=false` mode uses `os.MkdirTemp` (with a random
    suffix) to create working directories, making `ActualCwd`
    unpredictable at translate time. Setting `CwdMatters=true`
    with deterministic paths
    (`{Cwd}/nf-work/{runId}/{processName}/` for top-level;
    `{Cwd}/nf-work/{runId}/{subwf}/{processName}/` for
    subworkflow processes) lets the translator wire downstream
    job Cmd strings with absolute references to upstream output
    locations without requiring runtime discovery. wr's runner
    creates the Cwd via `os.MkdirAll` and sets `cmd.Dir`
    automatically, so the job Cmd needs no `mkdir`/`cd` prefix.
12. **All path/file outputs are dynamic.** Even a literal
    `path 'result.txt'` is treated as dynamic (pending) because
    the actual file produced may differ from the declaration
    (optional outputs, runtime-generated content). Only `val`
    outputs are static.
