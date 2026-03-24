# Parse Errors

Nextflow features that cause parse errors when encountered. These need parser fixes before behaviour can be implemented. Each entry describes where the parser fails.

**Total: 43 features**

## Dynamic Directives & Task Properties
Source: https://nextflow.io/docs/latest/reference/process.html

### BV-task-hash
`task.hash`

`defaultDirectiveTask()` does not provide `hash`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.hash"`.

### BV-task-index
`task.index`

`defaultDirectiveTask()` does not provide `index`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.index"`.

### BV-task-name
`task.name`

`defaultDirectiveTask()` does not provide `name`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.name"`.

### BV-task-previousException
`task.previousException`

`defaultDirectiveTask()` does not provide `previousException`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.previousException"`.

### BV-task-previousTrace
`task.previousTrace`

`defaultDirectiveTask()` does not provide `previousTrace`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.previousTrace"`.

### BV-task-process
`task.process`

`defaultDirectiveTask()` does not provide `process`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.process"`.

### BV-task-workDir
`task.workDir`

`defaultDirectiveTask()` does not provide `workDir`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.workDir"`.

## Configuration
Source: https://nextflow.io/docs/latest/reference/config.html

### CFG-aws
`aws`

top-level `aws` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-azure
`azure`

top-level `azure` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-charliecloud
`charliecloud`

`configParser.parse` does not dispatch `charliecloud`, so the block fails as an unsupported top-level config section.

### CFG-executor-specific-configuration
`Executor-specific configuration`

`parseExecutorBlock` only accepts flat assignments inside `executor { ... }`; executor-specific nested blocks/default sections are not parsed.

### CFG-executor-specific-defaults
`Executor-specific defaults`

`parseExecutorBlock` only accepts flat assignments inside `executor { ... }`; executor-specific nested blocks/default sections are not parsed.

### CFG-fusion
`fusion`

top-level `fusion` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-google
`google`

top-level `google` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-k8s
`k8s`

top-level `k8s` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-lineage
`lineage`

top-level `lineage` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-mail
`mail`

top-level `mail` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-nextflow
`nextflow`

top-level `nextflow` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-podman
`podman`

`configParser.parse` does not dispatch `podman`, so the block fails as an unsupported top-level config section.

### CFG-sarus
`sarus`

`configParser.parse` does not dispatch `sarus`, so the block fails as an unsupported top-level config section.

### CFG-seqera
`seqera`

top-level `seqera` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-shifter
`shifter`

`configParser.parse` does not dispatch `shifter`, so the block fails as an unsupported top-level config section.

### CFG-spack
`spack`

top-level `spack` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`; only process-level `spack` assignments are parsed by `parseProcessAssignment`.

### CFG-unscoped-options
`Unscoped options`

`configParser.parse` only accepts named top-level scopes (`apptainer`, `docker`, `singularity`, `env`, `executor`, `params`, `process`, `profiles`) and otherwise returns `unsupported config section`.

### CFG-workflow
`workflow`

top-level `workflow` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

## Feature Flags
Source: https://nextflow.io/docs/latest/reference/feature-flags.html

### CFG-enable-configProcessNamesValidation
`nextflow.enable.configProcessNamesValidation`

`configParser.parse` in `nextflowdsl/config.go` only accepts top-level scopes `apptainer`, `docker`, `singularity`, `env`, `executor`, `params`, `process`, and `profiles`; a top-level `nextflow` scope falls through to `unsupported config section`. `skipUnknownTopLevelConfigScope` also does not allowlist `nextflow`.

### CFG-enable-dsl
`nextflow.enable.dsl`

`configParser.parse` in `nextflowdsl/config.go` does not handle a top-level `nextflow` scope, so `nextflow.enable.dsl` cannot be parsed. `skipUnknownTopLevelConfigScope` only skips a fixed set of unrelated scopes and excludes `nextflow`.

### CFG-enable-moduleBinaries
`nextflow.enable.moduleBinaries`

`configParser.parse` in `nextflowdsl/config.go` rejects unsupported top-level section `nextflow`, so `nextflow.enable.moduleBinaries` cannot parse.

### CFG-enable-strict
`nextflow.enable.strict`

`configParser.parse` in `nextflowdsl/config.go` has no `nextflow` branch and returns `unsupported config section` for that scope; `skipUnknownTopLevelConfigScope` does not skip it.

### CFG-preview-output
`nextflow.preview.output`

`nextflow.preview.output` requires a top-level `nextflow` scope, but `configParser.parse` in `nextflowdsl/config.go` rejects that scope and `skippedTopLevelConfigScopes` does not include it.

### CFG-preview-recursion
`nextflow.preview.recursion`

same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, so `nextflow.preview.recursion` cannot be read.

### CFG-preview-topic
`nextflow.preview.topic`

same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, and `skipUnknownTopLevelConfigScope` only skips the allowlisted scopes in `skippedTopLevelConfigScopes`.

### CFG-preview-types
`nextflow.preview.types`

same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, so `nextflow.preview.types` cannot parse.

## Input Qualifiers & Options
Source: https://nextflow.io/docs/latest/reference/process.html

### INP-path-name
`path.name`

`name:` is not an accepted qualifier in either `applyDeclarationQualifier` or `applyTupleElementQualifier`.

### INP-path-stageAs
`path.stageAs`

`stageAs:` is not an accepted qualifier in either `applyDeclarationQualifier` or `applyTupleElementQualifier`.

## Process Sections
Source: https://nextflow.io/docs/latest/reference/process.html

### PSEC-inputs-and-outputs-legacy
`Inputs and outputs (legacy)`

`parseSection` only accepts separate `input:` and `output:` blocks, and `parseDeclarationLine` explicitly rejects DSL1 `into` syntax with `DSL 1 syntax is not supported`, so legacy combined input/output syntax is not accepted.

## Statements
Source: https://nextflow.io/docs/latest/reference/syntax.html

### STMT-multi-assignment
`Multi-assignment: def (a, b) = [1, 2]`

the statement parser does not accept `def (a, b) = ...`; `parseAssignmentStmt` requires a single identifier after `def`, so this form fails before `evalMultiAssignExpr` can be used.

## Deprecations
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-deprecated-addParams
`addParams and params clauses of include declarations`

`parseImport` in `nextflowdsl/parse.go` only parses `include { ... } from 'module'` and then returns; deprecated trailing `addParams`/`params` include clauses are not handled, so leftover tokens fail top-level parsing.

### SYN-deprecated-for-loop
`for loop — removed in strict syntax`

there is no loop parser. Workflow `main` handling in `parseWorkflowBlock` only special-cases `if`, assignments, and call/pipeline statements, and `parseWorkflowStatement` has no `for` support, so `for` loop syntax is rejected.

### SYN-deprecated-while-loop
`while loop — removed in strict syntax`

there is no `while` statement parser. Workflow `main` handling in `parseWorkflowBlock` and `parseWorkflowStatement` does not admit loop constructs, so `while` loop syntax is rejected.

## Workflow
Source: https://nextflow.io/docs/latest/reference/syntax.html

### WF-and
`and (&) operator for parallel process/workflow calls`

`&` is parsed as a generic bitwise operator by `parseBitwiseAndExprTokens`, but `parseWorkflowStatement` only accepts direct calls or `PipeExpr`, so `desugarWorkflowPipe` fails with `workflow statements must be calls or channel pipelines`.

### WF-recurse
`.recurse() method for process/workflow recursion`

`parseChannelOperators` only accepts operators listed in `supportedChannelOperators`, and `recurse` is not listed, so `.recurse(...)` is rejected as an unsupported operator.

### WF-times
`.times() method — fixed-count recursion`

`times` is not in `supportedChannelOperators`, so `.times(...)` is rejected by `parseChannelOperators` as an unsupported operator.
