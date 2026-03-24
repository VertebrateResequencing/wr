# Parse Errors

Nextflow features that cause parse errors when encountered. These need parser fixes before behaviour can be implemented. Each entry describes where the parser fails.

**Total: 43 features**

## Fix strategy summary

| Strategy | Features | Fix |
|----------|----------|-----|
| Add placeholder value to `defaultDirectiveTask()` | 7 | BV-task-hash, BV-task-index, BV-task-name, BV-task-previousException, BV-task-previousTrace, BV-task-process, BV-task-workDir |
| Add scope to `skippedTopLevelConfigScopes` | 15 | CFG-aws, CFG-azure, CFG-charliecloud, CFG-fusion, CFG-google, CFG-k8s, CFG-lineage, CFG-mail, CFG-nextflow, CFG-podman, CFG-sarus, CFG-seqera, CFG-shifter, CFG-spack, CFG-workflow |
| Add `nextflow` to `skippedTopLevelConfigScopes` | 8 | CFG-enable-configProcessNamesValidation, CFG-enable-dsl, CFG-enable-moduleBinaries, CFG-enable-strict, CFG-preview-output, CFG-preview-recursion, CFG-preview-topic, CFG-preview-types |
| Skip nested blocks in `parseExecutorBlock` | 2 | CFG-executor-specific-configuration, CFG-executor-specific-defaults |
| Handle bare assignments in config parser | 1 | CFG-unscoped-options |
| Add qualifier to `applyDeclarationQualifier` / `applyTupleElementQualifier` | 2 | INP-path-name, INP-path-stageAs |
| Skip legacy DSL1 combined input/output syntax | 1 | PSEC-inputs-and-outputs-legacy |
| Parse `def (a, b) = expr` in statement parser | 1 | STMT-multi-assignment |
| Skip deprecated `addParams`/`params` after include source | 1 | SYN-deprecated-addParams |
| Parse deprecated loop constructs with warning | 2 | SYN-deprecated-for-loop, SYN-deprecated-while-loop |
| Handle `&` (parallel) in workflow statement desugaring | 1 | WF-and |
| Add to `supportedChannelOperators` | 2 | WF-recurse, WF-times |

## Add placeholder value to `defaultDirectiveTask()` (7 features)

**Fix:** In `nextflowdsl/translate.go`, add the missing key to the map returned by `defaultDirectiveTask()` with a sensible zero/placeholder value (empty string for strings, 0 for ints). This prevents "unknown variable" errors when dynamic directives reference the property.

### BV-task-hash
`task.hash`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `hash`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.hash"`.

### BV-task-index
`task.index`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `index`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.index"`.

### BV-task-name
`task.name`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `name`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.name"`.

### BV-task-previousException
`task.previousException`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `previousException`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.previousException"`.

### BV-task-previousTrace
`task.previousTrace`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `previousTrace`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.previousTrace"`.

### BV-task-process
`task.process`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `process`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.process"`.

### BV-task-workDir
`task.workDir`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` does not provide `workDir`, and missing `task.*` lookups fail in `resolveExprPath`/`lookupVariablePart` in `nextflowdsl/groovy.go` with `unknown variable "task.workDir"`.

## Add scope to `skippedTopLevelConfigScopes` (15 features)

**Fix:** In `nextflowdsl/config.go`, add the scope name to the `skippedTopLevelConfigScopes` map so the config parser silently skips it instead of returning "unsupported config section".

### CFG-aws
`aws`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `aws` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-azure
`azure`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `azure` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-charliecloud
`charliecloud`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `configParser.parse` does not dispatch `charliecloud`, so the block fails as an unsupported top-level config section.

### CFG-fusion
`fusion`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `fusion` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-google
`google`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `google` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-k8s
`k8s`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `k8s` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-lineage
`lineage`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `lineage` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-mail
`mail`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `mail` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-nextflow
`nextflow`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `nextflow` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-podman
`podman`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `configParser.parse` does not dispatch `podman`, so the block fails as an unsupported top-level config section.

### CFG-sarus
`sarus`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `configParser.parse` does not dispatch `sarus`, so the block fails as an unsupported top-level config section.

### CFG-seqera
`seqera`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `seqera` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

### CFG-shifter
`shifter`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `configParser.parse` does not dispatch `shifter`, so the block fails as an unsupported top-level config section.

### CFG-spack
`spack`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `spack` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`; only process-level `spack` assignments are parsed by `parseProcessAssignment`.

### CFG-workflow
`workflow`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** top-level `workflow` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.

## Add `nextflow` to `skippedTopLevelConfigScopes` (8 features)

**Fix:** In `nextflowdsl/config.go`, add `"nextflow"` to the `skippedTopLevelConfigScopes` map. This lets the config parser skip the entire `nextflow { ... }` block (which contains `enable.*` and `preview.*` feature flags) instead of erroring on it.

### CFG-enable-configProcessNamesValidation
`nextflow.enable.configProcessNamesValidation`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** `configParser.parse` in `nextflowdsl/config.go` only accepts top-level scopes `apptainer`, `docker`, `singularity`, `env`, `executor`, `params`, `process`, and `profiles`; a top-level `nextflow` scope falls through to `unsupported config section`. `skipUnknownTopLevelConfigScope` also does not allowlist `nextflow`.

### CFG-enable-dsl
`nextflow.enable.dsl`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** `configParser.parse` in `nextflowdsl/config.go` does not handle a top-level `nextflow` scope, so `nextflow.enable.dsl` cannot be parsed. `skipUnknownTopLevelConfigScope` only skips a fixed set of unrelated scopes and excludes `nextflow`.

### CFG-enable-moduleBinaries
`nextflow.enable.moduleBinaries`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** `configParser.parse` in `nextflowdsl/config.go` rejects unsupported top-level section `nextflow`, so `nextflow.enable.moduleBinaries` cannot parse.

### CFG-enable-strict
`nextflow.enable.strict`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** `configParser.parse` in `nextflowdsl/config.go` has no `nextflow` branch and returns `unsupported config section` for that scope; `skipUnknownTopLevelConfigScope` does not skip it.

### CFG-preview-output
`nextflow.preview.output`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** `nextflow.preview.output` requires a top-level `nextflow` scope, but `configParser.parse` in `nextflowdsl/config.go` rejects that scope and `skippedTopLevelConfigScopes` does not include it.

### CFG-preview-recursion
`nextflow.preview.recursion`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, so `nextflow.preview.recursion` cannot be read.

### CFG-preview-topic
`nextflow.preview.topic`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, and `skipUnknownTopLevelConfigScope` only skips the allowlisted scopes in `skippedTopLevelConfigScopes`.

### CFG-preview-types
`nextflow.preview.types`
Reference: https://nextflow.io/docs/latest/reference/feature-flags.html

**Current behaviour:** same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, so `nextflow.preview.types` cannot parse.

## Skip nested blocks in `parseExecutorBlock` (2 features)

**Fix:** In `nextflowdsl/config.go`, modify `parseExecutorBlock` to detect when a value token is `{` (start of nested block) and skip it with `skipNamedBlock` or brace-counting, rather than only accepting flat assignments.

### CFG-executor-specific-configuration
`Executor-specific configuration`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseExecutorBlock` only accepts flat assignments inside `executor { ... }`; executor-specific nested blocks/default sections are not parsed.

### CFG-executor-specific-defaults
`Executor-specific defaults`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseExecutorBlock` only accepts flat assignments inside `executor { ... }`; executor-specific nested blocks/default sections are not parsed.

## Handle bare assignments in config parser (1 feature)

**Fix:** In `nextflowdsl/config.go`, modify `configParser.parse` to detect and skip unrecognised top-level assignments (e.g. `cleanup = true`) instead of erroring. These are bare config options outside any scope.

### CFG-unscoped-options
`Unscoped options`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `configParser.parse` only accepts named top-level scopes (`apptainer`, `docker`, `singularity`, `env`, `executor`, `params`, `process`, `profiles`) and otherwise returns `unsupported config section`.

## Add qualifier to `applyDeclarationQualifier` / `applyTupleElementQualifier` (2 features)

**Fix:** In `nextflowdsl/parse.go`, add a case for the qualifier name in both `applyDeclarationQualifier` and `applyTupleElementQualifier` switch statements. The qualifier value is a string expression that should be stored on the Declaration/TupleElement (may need a new field).

### INP-path-name
`path.name`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `name:` is not an accepted qualifier in either `applyDeclarationQualifier` or `applyTupleElementQualifier`.

### INP-path-stageAs
`path.stageAs`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `stageAs:` is not an accepted qualifier in either `applyDeclarationQualifier` or `applyTupleElementQualifier`.

## Skip legacy DSL1 combined input/output syntax (1 feature)

**Fix:** This is DSL1 syntax which Nextflow itself has removed. Rather than implementing it, modify the error message in `parseDeclarationLine` to be more descriptive, or silently skip DSL1 blocks with a warning. The goal is to not crash when encountering legacy pipelines.

### PSEC-inputs-and-outputs-legacy
`Inputs and outputs (legacy)`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseSection` only accepts separate `input:` and `output:` blocks, and `parseDeclarationLine` explicitly rejects DSL1 `into` syntax with `DSL 1 syntax is not supported`, so legacy combined input/output syntax is not accepted.

## Parse `def (a, b) = expr` in statement parser (1 feature)

**Fix:** In `nextflowdsl/parse.go`, modify `parseAssignmentStmt` to accept `def` followed by `(` as a multi-assignment. The evaluator `evalMultiAssignExpr` already exists — the parser just needs to produce the right AST node.

### STMT-multi-assignment
`Multi-assignment: def (a, b) = [1, 2]`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** the statement parser does not accept `def (a, b) = ...`; `parseAssignmentStmt` requires a single identifier after `def`, so this form fails before `evalMultiAssignExpr` can be used.

## Skip deprecated `addParams`/`params` after include source (1 feature)

**Fix:** In `nextflowdsl/parse.go`, after `parseImport` reads the `from` source string, check if the next token is an identifier matching `addParams` or `params`, and if so, skip it plus any following parenthesised/map argument. Emit a deprecation warning.

### SYN-deprecated-addParams
`addParams and params clauses of include declarations`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseImport` in `nextflowdsl/parse.go` only parses `include { ... } from 'module'` and then returns; deprecated trailing `addParams`/`params` include clauses are not handled, so leftover tokens fail top-level parsing.

## Parse deprecated loop constructs with warning (2 features)

**Fix:** In `nextflowdsl/parse.go`, add support for `for`/`while` in `parseWorkflowStatement` or the statement parser. These are deprecated in Nextflow strict mode. Parse the loop structure (condition + body), emit a deprecation warning, and either evaluate the body or skip it gracefully.

### SYN-deprecated-for-loop
`for loop — removed in strict syntax`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** there is no loop parser. Workflow `main` handling in `parseWorkflowBlock` only special-cases `if`, assignments, and call/pipeline statements, and `parseWorkflowStatement` has no `for` support, so `for` loop syntax is rejected.

### SYN-deprecated-while-loop
`while loop — removed in strict syntax`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** there is no `while` statement parser. Workflow `main` handling in `parseWorkflowBlock` and `parseWorkflowStatement` does not admit loop constructs, so `while` loop syntax is rejected.

## Handle `&` (parallel) in workflow statement desugaring (1 feature)

**Fix:** In `nextflowdsl/parse.go`, modify `desugarWorkflowPipe` or `parseWorkflowStatement` to recognise `BitwiseAndExpr` as a parallel fork. In Nextflow, `A & B` means run A and B in parallel on the same input. This needs to produce two separate `Call` entries with the same input channel.

### WF-and
`and (&) operator for parallel process/workflow calls`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `&` is parsed as a generic bitwise operator by `parseBitwiseAndExprTokens`, but `parseWorkflowStatement` only accepts direct calls or `PipeExpr`, so `desugarWorkflowPipe` fails with `workflow statements must be calls or channel pipelines`.

## Add to `supportedChannelOperators` (2 features)

**Fix:** In `nextflowdsl/parse.go`, add the operator name to the `supportedChannelOperators` map so it parses as a channel operator call instead of being rejected.

### WF-recurse
`.recurse() method for process/workflow recursion`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseChannelOperators` only accepts operators listed in `supportedChannelOperators`, and `recurse` is not listed, so `.recurse(...)` is rejected as an unsupported operator.

### WF-times
`.times() method — fixed-count recursion`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `times` is not in `supportedChannelOperators`, so `.times(...)` is rejected by `parseChannelOperators` as an unsupported operator.
