# Implementation Gaps

Nextflow features that are partially implemented — they parse but have incorrect or incomplete behaviour. Each entry describes what works and what doesn't.

Each gap is a potential micro-task: read the Nextflow reference (linked per entry), understand the correct behaviour, then fix the Go code cited in "Current behaviour".

**Total: 184 features**

## Dynamic Directives & Task Properties
Source: https://nextflow.io/docs/latest/reference/process.html

### BV-task-attempt
`task.attempt`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `evalDirectiveExpr` and `resolveDirectiveValue` in `nextflowdsl/translate.go` evaluate dynamic directives during translation, and `defaultDirectiveTask()` only seeds `task.attempt` with the placeholder value `1`; retries do not rebind it to the real attempt count, so semantics differ from Nextflow.

### BV-task-exitStatus
`task.exitStatus`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `defaultDirectiveTask()` in `nextflowdsl/translate.go` hard-codes `task.exitStatus` to `0`, and `resolveErrorStrategy`/`applyErrorStrategy` resolve strategy from that translation-time value rather than a real task failure status.

## Channel Factories
Source: https://nextflow.io/docs/latest/reference/channel.html

### CF-fromFilePairs
`fromFilePairs`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelFactoryItems` accepts the factory, but `resolveFilePairs` returns only sorted file-path slices per group and omits the tuple key that Nextflow `fromFilePairs` emits; extra options are also not handled.

### CF-fromLineage
`fromLineage`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelFactoryItems` sends `fromLineage` to `warnUntranslatableChannelFactory` and returns `nil, nil`, so it parses but always resolves to an empty channel.

### CF-fromPath
`fromPath`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelPattern` only accepts one string argument, and `resolveChannelFactoryItems` uses `filepath.Glob` to emit cleaned string paths; Nextflow option maps and richer path semantics are not implemented.

### CF-fromSRA
`fromSRA`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelFactoryItems` routes `fromSRA` to `warnUntranslatableChannelFactory` and returns an empty channel instead of resolving SRA inputs.

### CF-interval
`interval`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelFactoryItems` routes `interval` to `warnUntranslatableChannelFactory` and returns an empty channel, so no timed emissions occur.

### CF-topic
`topic`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelFactoryItems` routes `topic` to `warnUntranslatableChannelFactory` and returns an empty channel, so topic-based channel behaviour is absent.

### CF-watchPath
`watchPath`
Reference: https://nextflow.io/docs/latest/reference/channel.html

**Current behaviour:** `resolveChannelFactoryItems` routes `watchPath` to `warnUntranslatableChannelFactory` and returns an empty channel, so filesystem watch events are never produced.

## Configuration
Source: https://nextflow.io/docs/latest/reference/config.html

### CFG-apptainer
`apptainer`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseContainerScope` supports only `enabled = <bool>` and, when true, only records `Config.ContainerEngine = "apptainer"`; other apptainer settings are rejected.

### CFG-conda
`conda`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `conda`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-dag
`dag`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `dag`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-docker
`docker`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseContainerScope` supports only `enabled = <bool>` and, when true, only records `Config.ContainerEngine = "docker"`; other docker settings are rejected.

### CFG-env
`env`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseTopLevelEnvBlock` / `parseEnvBlock` only parse a string map into `Config.Env`; in the inspected code this is stored but no Nextflow-equivalent runtime behaviour is implemented here.

### CFG-executor
`executor`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseExecutorBlock` parses flat key/value settings into `Config.Executor`, but the inspected code only stores them and does not implement Nextflow executor semantics here.

### CFG-manifest
`manifest`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `manifest`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-notification
`notification`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `notification`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-report
`report`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `report`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-singularity
`singularity`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `parseContainerScope` supports only `enabled = <bool>` and, when true, only records `Config.ContainerEngine = "singularity"`; other singularity settings are rejected.

### CFG-timeline
`timeline`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `timeline`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-tower
`tower`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `tower`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-trace
`trace`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `trace`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

### CFG-wave
`wave`
Reference: https://nextflow.io/docs/latest/reference/config.html

**Current behaviour:** `skippedTopLevelConfigScopes` includes `wave`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.

## Operators: Transforming
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-buffer
`buffer`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` handles `buffer` only through `resolveChunkSize` and `chunkChannelItems` in `nextflowdsl/channel.go`, so only fixed-size chunking is implemented; other buffer modes/options are not.

### CO-collate
`collate`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` handles `collate` with the same `resolveChunkSize` plus `chunkChannelItems` path as `buffer`, so it supports only fixed-size chunking and not the fuller collate variants.

### CO-collect
`collect`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` implements `collect` as a plain `collectChannelValues(items)` wrapper in `nextflowdsl/channel.go` and ignores operator args/options, so only the basic collect behaviour is present.

### CO-count
`count`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `countChannelItems` in `nextflowdsl/channel.go` supports plain counting, a simple closure filter, or exact-value equality via `channelOperatorMatches`; broader matcher variants are not implemented.

### CO-flatMap
`flatMap`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` evaluates only compile-time-resolvable closures through `evalChannelClosure`, and unsupported closures become pass-through; flattening is limited to scalars, `[]any`, and `[]string` via `flattenChannelValues`.

### CO-groupTuple
`groupTuple`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `groupTupleItems` in `nextflowdsl/channel.go` always groups on tuple element `0` and collects remaining positions into lists; operator arguments/options are not read.

### CO-map
`map`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` uses `evalChannelClosure` for compile-time-evaluable closures only, and unsupported closures are downgraded to pass-through with `warnUnsupportedChannelClosure`.

### CO-max
`max`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` routes `max` to `reduceChannelItems(maxChannelValue)`, but `maxChannelValue` only compares `int` and `string` values and ignores any comparator/closure arguments.

### CO-min
`min`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` routes `min` to `reduceChannelItems(minChannelValue)`, but `minChannelValue` only compares `int` and `string` values and ignores any comparator/closure arguments.

### CO-reduce
`reduce`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `reduceOperatorSeedAndClosure` supports only the basic seed-plus-closure forms, and the reduction closure is evaluated at compile time through `evalChannelClosure`, so complex runtime Groovy reductions are not supported.

### CO-sum
`sum`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `sumChannelItems` in `nextflowdsl/channel.go` sums only `int` items and does not implement richer sum variants.

### CO-toInteger
`toInteger`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `toInteger` appears only in `deprecatedChannelOperators` in `nextflowdsl/channel.go`; the default path warns and returns items unchanged, so no integer conversion happens.

### CO-toSortedList
`toSortedList`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `sortedChannelValues` sorts collected values using `lessSortableValue`, which only supports `int` and `string`; comparator/closure variants and other comparable types are unsupported.

### CO-transpose
`transpose`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `transposeChannelItems` in `nextflowdsl/channel.go` expands list-valued tuple positions independently, producing cartesian expansion across indexed columns rather than a strict position-wise transpose.

## Operators: Combining
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-collectFile
`collectFile`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `collectFileChannelItems` in `nextflowdsl/channel.go` writes all items to a single file and only honors the `name` option; grouping/closure-driven naming and broader `collectFile` behaviors are not implemented.

### CO-combine
`combine`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` and `combineChannelItems` implement the basic cartesian product and a single integer `by` key via `resolveOperatorByIndex`, but broader `combine` variants/options are not supported.

### CO-cross
`cross`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `crossChannelItems` produces the full cartesian product of left and right items and `applyChannelOperator` ignores operator arguments, so keyed `cross` semantics are not matched.

### CO-join
`join`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `joinChannelItems` only performs a default first-element key join using `channelItemKey` and ignores join options/variants in `applyChannelOperator`.

### CO-merge
`merge`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `merge` is only listed in `deprecatedChannelOperators`; the default branch warns and returns the original items unchanged, so merged channel inputs are ignored.

## Operators: Splitting
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-countFasta
`countFasta`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` does not implement a `countFasta` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so FASTA records are not counted.

### CO-countFastq
`countFastq`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` does not implement a `countFastq` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so FASTQ records are not counted.

### CO-countJson
`countJson`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` does not implement a `countJson` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so JSON elements are not counted.

### CO-countLines
`countLines`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` does not implement a `countLines` case; the deprecated fallback in `deprecatedChannelOperators` warns and returns `cloneChannelItems(items)` unchanged, so input lines are not counted.

## Operators: Filtering
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-distinct
`distinct`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` dispatches `distinct` to `distinctChannelItems`, which only removes consecutive duplicate whole values via `reflect.DeepEqual`; parsed args/closures are ignored, so keyed/comparator variants are not implemented.

### CO-filter
`filter`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` handles `filter` only through `evalChannelClosureBool`; parsed args are ignored entirely, and unsupported closures trigger `warnUnsupportedChannelClosure` and return the original items unchanged.

### CO-unique
`unique`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` dispatches `unique` to `uniqueChannelItems`, which performs whole-value de-duplication across the full channel but ignores any parsed args or closure variants.

### CO-until
`until`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `until` is listed in `warningOnlyChannelOperators`; `applyChannelOperator` falls through to the warning-only path and returns items unchanged instead of stopping on a predicate.

## Operators: Other
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-dump
`dump`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` in `nextflowdsl/channel.go` handles `dump` via the `case "dump", "set", "tap", "view": return cloneChannelItems(items), nil` pass-through path, so it parses but does not perform any dump/output side effect.

### CO-subscribe
`subscribe`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `warningOnlyChannelOperators` includes `subscribe`, and the default path in `applyChannelOperator` warns then returns `cloneChannelItems(items)` unchanged, so it parses but does not perform subscription/callback behaviour.

### CO-view
`view`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` in `nextflowdsl/channel.go` handles `view` via the same pass-through branch as `dump`, returning items unchanged without any view/output side effect.

## Operators: Forking
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-ifEmpty
`ifEmpty`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** `applyChannelOperator` implements the non-empty and single-argument default-value cases, but only via `operator.Args`; closure-form `ifEmpty { ... }` parses as a closure operator and is not handled here, so that variant does not match Nextflow semantics.

### CO-set
`set`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** parsing accepts `.set { ... }`, but runtime handling in `applyChannelOperator` groups `set` with `dump`, `tap`, and `view` as a pure pass-through, so no named channel binding or side effect is created.

### CO-tap
`tap`
Reference: https://nextflow.io/docs/latest/reference/operator.html

**Current behaviour:** parsing accepts both closure and channel-argument forms (`parseChannelOperatorArgs` handles `tap` channels), but runtime handling in `applyChannelOperator` is pass-through only and ignores the side-channel target.

## Directives: Resources
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-accelerator
`accelerator`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `resolveAcceleratorOptions` in `nextflowdsl/translate.go` parses the directive, but only applies it for `lsf` scheduling and treats accelerator `type` as informational only, so behaviour differs from Nextflow's broader resource semantics.

### DIR-machineType
`machineType`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseProcessDirective` in `nextflowdsl/parse.go` accepts and stores `machineType`, but there is no corresponding consumer in the directive resolution or job requirement/application paths in `nextflowdsl/translate.go`, so it is parsed and then ignored.

### DIR-resourceLabels
`resourceLabels`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseProcessDirective` in `nextflowdsl/parse.go` accepts and stores `resourceLabels`, but no code in `nextflowdsl/translate.go` applies those labels to scheduler requirements or jobs.

### DIR-resourceLimits
`resourceLimits`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseProcessDirective` in `nextflowdsl/parse.go` accepts and stores `resourceLimits`, but no code in `nextflowdsl/translate.go` consumes it when building requirements or jobs.

## Directives: Environment
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-afterScript
`afterScript`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `buildCommandBody` in `nextflowdsl/translate.go` appends `proc.AfterScript` after the main script, so it runs, but only as part of the same command body; there is no separate outside-container execution matching Nextflow `afterScript` semantics.

### DIR-beforeScript
`beforeScript`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `buildCommandBody` in `nextflowdsl/translate.go` prepends `proc.BeforeScript` before the main script, but it is executed inside the same generated command body rather than with Nextflow's distinct pre-task/container-aware semantics.

### DIR-conda
`conda`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `prependEnvironmentDirectives` in `nextflowdsl/translate.go` only prefixes `conda activate <value>`; it does not create or resolve Conda environments from package specs/files the way Nextflow does.

### DIR-spack
`spack`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `prependEnvironmentDirectives` in `nextflowdsl/translate.go` only prefixes `spack load <value>`; it does not implement Nextflow's broader Spack environment/package resolution behaviour.

## Directives: Scheduler
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-arch
`arch`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `resolveArchOptions` in `nextflowdsl/translate.go` only applies `arch` for `lsf` scheduling and only recognizes the exact strings `linux/x86_64` and `linux/aarch64`; other schedulers and values are ignored, so this does not fully match Nextflow semantics.

### DIR-clusterOptions
`clusterOptions`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `buildRequirements` forwards `clusterOptions` via `resolveDirectiveString` to `req.Other["scheduler_misc"]`, but `resolveDirectiveString` only accepts values that evaluate to a single string, so non-string variants do not match Nextflow's broader directive behavior.

## Directives: Other
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-array
`array`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `array` in `proc.Directives` and emits `warnStoredDirective`, but `translate.go` has no consumer for `"array"`, so job-array semantics are parsed then ignored.

### DIR-debug
`debug`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `debug` with `warnStoredDirective`, but no translation path uses it; `resolveDirectiveBool` exists, yet only `fair` is wired through `applyFairPriority`, so debug output behaviour is not applied.

### DIR-echo
`echo`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `echo` with `warnStoredDirective`, but `translate.go` never reads `proc.Directives["echo"]`, so the directive has no effect on job execution or logging.

### DIR-secret
`secret`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `secret` with `warnStoredDirective`, but there is no translation or environment-injection path for `proc.Directives["secret"]`, so secrets are silently ignored.

### DIR-tag
`tag`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores the tag text in `proc.Tag`, and `applyProcessDefaults` can resolve default config tags into that field, but no later translation step consumes `proc.Tag`, so it does not affect generated jobs.

## Directives: Execution
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-cache
`cache`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `cache` in `proc.Cache` in `nextflowdsl/parse.go`, and defaults propagate through `resolveDirectiveString` in `nextflowdsl/translate.go`, but the translation path never applies `proc.Cache` to job creation or execution, so cache mode semantics are ignored.

### DIR-errorStrategy
`errorStrategy`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyErrorStrategy` in `nextflowdsl/translate.go` implements `retry`, `ignore`, and terminate-style handling, but `finish` is only represented by `applyFinishStrategyLimitGroup` adding a limit-group token; the cited execution path does not show full Nextflow `finish` orchestration.

### DIR-executor
`executor`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `executor` in `proc.Directives["executor"]` in `nextflowdsl/parse.go`, but there is no corresponding use in `nextflowdsl/translate.go`, so per-process executor selection is silently ignored.

### DIR-fair
`fair`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyFairPriority` in `nextflowdsl/translate.go` only converts input index into `job.Priority`; no cited code enforces Nextflow-style ordered output emission, so this is only an approximation.

### DIR-maxSubmitAwait
`maxSubmitAwait`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` stores `maxSubmitAwait` in `proc.Directives` in `nextflowdsl/parse.go`, but no cited translation code reads or applies it, so submit-wait behaviour is ignored.

### DIR-scratch
`scratch`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `resolveScratchDirective` and `wrapScratchCommand` in `nextflowdsl/translate.go` run the script in a scratch directory and copy outputs back, but the cited implementation does not stage inputs into scratch first, so behaviour differs from Nextflow scratch execution.

## Directives: Publishing
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-publishDir
`publishDir`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyPublishDirBehaviours` and `buildPublishDirCommand` in `nextflowdsl/translate.go` do add publish actions, but `parsePublishDir` in `nextflowdsl/parse.go` defaults the mode to `"copy"` and `publishDirActionCommand` only treats `link` and `move` specially, falling back to copy for other modes. That does not match Nextflow `publishDir` semantics, whose default is symlink-based publishing and which supports additional mode variants/options.

### DIR-stageInMode
`stageInMode`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirectiveExpr` in `nextflowdsl/parse.go` accepts and stores `stageInMode` in `proc.Directives`, but `warnStoredDirective` marks it as stored without translation support and there is no corresponding handling in `nextflowdsl/translate.go`, so it parses but has no effect on job staging.

### DIR-stageOutMode
`stageOutMode`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirectiveExpr` in `nextflowdsl/parse.go` accepts and stores `stageOutMode` in `proc.Directives`, but `warnStoredDirective` marks it as stored without translation support and there is no corresponding handling in `nextflowdsl/translate.go`, so it parses but has no effect on output staging.

## Includes & Parameters
Source: https://nextflow.io/docs/latest/reference/syntax.html

### INCL-parameter-legacy
`Parameter (legacy)`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseTopLevelParamAssignment` in `parse.go` parses legacy `params.foo = ...` assignments into `Workflow.ParamBlock`, but as with the params block feature there is no cited code path in `params.go` that consumes those declarations when building runtime parameters, so the legacy defaults are parsed and stored rather than taking Nextflow-equivalent effect.

### INCL-params-block
`Params block`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseTopLevelParamsBlock` in `parse.go` parses `params { ... }` entries into `Workflow.ParamBlock`, and `module_load.go` preserves that block across imports, but the cited runtime params code in `params.go` only loads external JSON/YAML params and substitutes `params.*` references; it does not apply `ParamBlock` defaults/types to the runtime params map.

## Input Qualifiers & Options
Source: https://nextflow.io/docs/latest/reference/process.html

### INP-file
`file( identifier | stageName )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** identifier inputs work because translation treats `file` like a generic non-tuple binding, but the `stageName` form is not implemented: input translation never uses `decl.Expr`, so no staging/renaming semantics are applied.

### INP-path
`path( identifier | stageName )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** identifier inputs are bound from upstream channel items in `resolveBindings`, but the `stageName` form is silently ignored because non-tuple input handling uses only tuple shape and `decl.Name`; there is no input-side staging logic.

### INP-path-arity
`path.arity`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** tuple element `arity:` is accepted by `applyTupleElementQualifier`, but the value is not stored on the AST and is never enforced by `resolveBindings` or `bindingsForInputDeclaration`; top-level `path(..., arity: ...)` would be rejected by `applyDeclarationQualifier`.

### INP-stdin
`stdin`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `stdin` parses, but translation never connects the bound value to process standard input; `buildCommandBody` only exports bindings as environment variables.

## Groovy Methods & Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-inject
`inject( accumulator: (E,E) -> E ) -> E`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-join
`join( separator: String = '' ) -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-max-2
`max( comparator: (E,E) -> Integer ) -> E`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-min-2
`min( comparator: (E,E) -> Integer ) -> E`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-eachLine
`eachLine( action: (String) -> () )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-readLines
`readLines() -> List<String>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

## Groovy & Java Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

### METH-java-io
`java.io.*`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** `evalNewExpr` supports `File`/`Path` via `evalPathConstructor`, but it reduces them to cleaned path strings rather than Java objects, and no other `java.io.*` classes are implemented.

### METH-java-lang
`java.lang.*`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** the evaluator supports a small subset (`Integer.parseInt` in `evalStaticMethodCall`, plus `Integer`/`String` handling in `matchesGroovyType` and `evalCastExpr`), but the rest of `java.lang.*` such as `Math` and `System` is not implemented.

### METH-java-math-BigDecimal
`java.math.BigDecimal`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** `evalNewExpr` recognizes `BigDecimal`, but `evalBigDecimalConstructor` converts the value with `strconv.ParseFloat`, so wr returns `float64` rather than Groovy/Java `BigDecimal` semantics.

### METH-java-math-BigInteger
`java.math.BigInteger`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** `evalNewExpr` recognizes `BigInteger`, but `evalBigIntegerConstructor` converts the value with `strconv.ParseInt`, so wr returns `int64` rather than arbitrary-precision `BigInteger` semantics.

### METH-java-net
`java.net.*`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** `evalNewExpr` recognizes `URL`, but routes it through `evalStringConstructor`, so wr treats it as a plain string and implements no other `java.net.*` types.

### METH-java-util
`java.util.*`
Reference: https://nextflow.io/docs/latest/reference/stdlib-groovy.html

**Current behaviour:** `evalNewExpr` supports only a subset (`Date`, `ArrayList`, `HashMap`, `LinkedHashMap`, `Random`), with `Random` stubbed by `evalRandomConstructor` to `int64(0)` and no broader `java.util.*` class or utility support.

## Output Qualifiers & Options
Source: https://nextflow.io/docs/latest/reference/process.html

### OUT-env
`env( name )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDeclarationPrimary` accepts `env(name)`, but `staticOutputValue` only looks up the name in `outputVarsWithValues` (inputs and `params`), so it does not capture the process environment value produced at runtime.

### OUT-eval
`eval( command )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `evalOutputCaptureLines` appends shell lines that capture command output into `__nf_eval_*`, but `outputValue` falls back to `staticOutputValue`, which evaluates the `eval(...)` expression itself and returns the command string rather than the captured command output.

### OUT-path-arity
`path.arity`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** extra `path(...)` options are silently discarded because `parseDeclarationPrimary` only uses the first comma-separated argument inside the call, and although tuple elements accept an `arity` qualifier in `applyTupleElementQualifier`, no arity value is stored in the AST or enforced anywhere in `nextflowdsl/translate.go`.

### OUT-path-followLinks
`path.followLinks`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `followLinks` is parsed only as an ignored extra `path(...)` argument because `parseDeclarationPrimary` keeps just the first inner argument, and there is no follow-links handling in `nextflowdsl/translate.go`.

### OUT-path-glob
`path.glob`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** basic glob patterns in `path('*.txt')` are supported by `outputPatterns` and `expandCompletedOutputPattern`, but the `glob` option itself is ignored because additional `path(...)` arguments are discarded in `parseDeclarationPrimary`.

### OUT-path-hidden
`path.hidden`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** the `hidden` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and no hidden-file filtering logic exists in `nextflowdsl/translate.go`.

### OUT-path-includeInputs
`path.includeInputs`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** the `includeInputs` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and translation only matches completed output paths, not declared inputs, in `completedOutputPathsForJob` and `resolveCompletedOutputValue`.

### OUT-path-maxDepth
`path.maxDepth`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** the `maxDepth` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and `expandCompletedOutputPattern` only uses `filepath.Glob`, with no recursive depth control.

### OUT-path-type
`path.type`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** the `type` option is silently ignored because `parseDeclarationPrimary` drops additional `path(...)` arguments and translation never filters outputs by file type in `nextflowdsl/translate.go`.

### OUT-stdout
`stdout`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** commands are wrapped by `captureCommand` to write stdout into `.nf-stdout`, but `outputValue` and `outputValueForDeclaration` never surface that captured stdout as the channel item, so `stdout` outputs do not behave like Nextflow stdout channels.

### OUT-tuple
`tuple( arg1, arg2, ... )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseTupleDeclaration` and `tupleOutputValue` support tuples of `path`/`file` plus static value-like elements, but tuple elements that rely on runtime captures such as `env`, `stdout`, or `eval` still go through `staticOutputValue`, so tuple output semantics are only partially implemented.

### OUT-val
`val( value )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `staticOutputValue` in `nextflowdsl/translate.go` only evaluates against `outputVarsWithValues` (named inputs plus `params`) and otherwise falls back to the single binding or `""`; it does not capture general runtime-generated values the way Nextflow `val(...)` can.

## Process Sections
Source: https://nextflow.io/docs/latest/reference/process.html

### PSEC-directives
`Directives`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDirective` accepts many directives, and some are translated by `buildRequirements`, `applyContainer`, `applyMaxForks`, `applyFairPriority`, `applyErrorStrategy`, `applyEnv`, `resolveScratchDirective`, `prependEnvironmentDirectives`, `resolveStoreDirDirective`, and `newProcessMaxErrorsJob`, but many others are only stored with `warnStoredDirective` or warned unsupported, so directive support is partial rather than full Nextflow parity.

### PSEC-exec
`exec: section of a process definition`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseSection` stores `proc.Exec` but immediately calls `warnUnsupportedProcessSection`, and job generation uses `renderScript(proc.Script)` rather than `proc.Exec`, so `exec:` parses but is not translated.

### PSEC-generic-emit
`emit: generic option for inputs/outputs`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyDeclarationQualifier` parses `emit`, and output emits are carried through `emitOutputsForProcess` plus `resolveTranslatedOutput`, but input-side `emit` is never used during binding resolution.

### PSEC-generic-optional
`optional: generic option for inputs/outputs`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyDeclarationQualifier` stores `decl.Optional`, but the translation path does not read `Optional` in `resolveBindings`, `outputPaths`, or `outputValue`, so the qualifier is parsed and ignored.

### PSEC-generic-options
`Generic options`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyDeclarationQualifier` only recognizes `emit`, `optional`, and `topic`; among those, only output-side `emit` is translated, while `optional` and `topic` are not consumed by the translator.

### PSEC-generic-topic
`topic: generic option for inputs/outputs`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `applyDeclarationQualifier` parses `topic`, but no translation code uses it; output `topic` additionally triggers `warnUnsupportedOutputQualifier`, so topic semantics are not implemented.

### PSEC-inputs
`Inputs`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseSection`/`parseDeclarations` and `resolveBindings` implement normal input parsing and binding, but `stdin` is only parsed in `parseDeclarationPrimary` and is never wired into `buildCommandWithValues` or `buildCommandBody`, so input semantics are incomplete.

## Typed Process (Preview)
Source: https://nextflow.io/docs/latest/reference/process.html

### PSEC-inputs-and-outputs-typed
`Inputs and outputs (typed)`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** typed process I/O metadata is not represented in the AST. `Declaration` stores `Kind`, `Name`, `Expr`, `Each`, `Optional`, and `Elements`, but no declared type information, so typed semantics are not preserved through translation.

### PSEC-stage-directives
`Stage directives`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseSection` explicitly marks the `stage:` process section unsupported and discards it from translation, so stage directives have no effect on generated jobs.

### PSEC-typed-env
`env( name: String, String value )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** typed stage env setup is not translated. `parseSection` accepts a `stage:` section but immediately calls `warnUnsupportedProcessSection`, and `applyEnv` only applies directive-level `proc.Env` entries populated by `parseDirective`, not typed stage directives.

### PSEC-typed-env-1
`env( name: String ) -> String`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `parseDeclarationPrimary` recognises `env(...)`, but output translation never reads process environment. `staticOutputValue` only resolves declaration expressions, named bound inputs, or fallback bindings.

### PSEC-typed-eval
`eval( command: String ) -> String`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `eval` declarations are parsed and `evalOutputCaptureLines` appends command substitutions to the job body, but `outputValue`/`staticOutputValue` return the declaration expression or bindings, not the captured command result.

### PSEC-typed-file
`file( pattern: String, [options] ) -> Path`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `file` output declarations are routed through generic path-pattern handling in `outputPaths` and `resolveCompletedOutputValue`; wr does not enforce single-`Path` typed semantics and can return multiple matched paths.

### PSEC-typed-file-followLinks
`file.followLinks: Boolean`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** no parser or translator support exists for a `followLinks` file option; only the primary `file(...)` argument is used and extra options are ignored by `parseDeclarationPrimary`.

### PSEC-typed-file-glob
`file.glob: Boolean`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** there is no `glob` option support. wr always treats wildcard patterns via `matchCompletedOutputPaths` and `copyOutputCommands`, so typed `file.glob` semantics cannot be selected.

### PSEC-typed-file-hidden
`file.hidden: Boolean`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** no `hidden` option is stored in `Declaration` or handled in `translate.go`; the option is absent from parser/runtime support.

### PSEC-typed-file-includeInputs
`file.includeInputs: Boolean`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** no `includeInputs` option is represented in `Declaration` or used in translation, so this typed file option is ignored.

### PSEC-typed-file-maxDepth
`file.maxDepth: Integer`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** no `maxDepth` option is parsed or applied anywhere in the file output translation path.

### PSEC-typed-file-optional
`file.optional: Boolean`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** optionality is parsed into `Declaration.Optional`, but `translate.go` never reads that field, so optional file behavior is ignored at runtime.

### PSEC-typed-file-type
`file.type: String`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** there is no `type` option handling for typed file outputs; neither the AST nor translation path carries file-type filtering semantics.

### PSEC-typed-files
`files( pattern: String, [options] ) -> Set<Path>`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** there is no dedicated `files` output implementation. `files(...)` is not handled in `outputPaths`/`outputValue`, so it does not produce typed `Set<Path>` behavior.

### PSEC-typed-outputs
`Outputs section of typed process`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** the `output:` section supports only partial legacy declaration behavior. `file`, `env`, `eval`, `stdout`, and typed options do not match Nextflow typed-process semantics end to end.

### PSEC-typed-stageAs
`stageAs( value: Path, filePattern: String )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** `stage:` content is parsed as an unsupported raw process section in `parseSection`, with no AST or translation support for `stageAs`.

### PSEC-typed-stageAs-2
`stageAs( value: Iterable<Path>, filePattern: String )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** same as `PSEC-typed-stageAs`; iterable `stageAs` has no parser/runtime handling beyond the ignored `stage:` section.

### PSEC-typed-stdin
`stdin( value: String )`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** typed `stdin(value)` stage behavior is not implemented because the `stage:` section is warned unsupported in `parseSection` and never influences `buildCommandWithValues`.

### PSEC-typed-stdout
`stdout() -> String`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** job commands always redirect stdout to `.nf-stdout` via `captureCommand`, but `outputValue` does not expose stdout content and instead falls back to output paths/CWD metadata.

## Statements
Source: https://nextflow.io/docs/latest/reference/syntax.html

### STMT-assert
`assert`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseAssertStmt` parses `assert`, but `evalAssertStatement` only logs a warning/error and always returns `nil`, so failed assertions do not stop evaluation or raise an exception.

### STMT-throw
`throw`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseThrowStmt` parses `throw`, but uncaught throws are swallowed by `evalStatementBody`, which converts `evalThrownError` into a `nil` result after warning instead of propagating failure.

### STMT-trycatch
`try/catch`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseTryStmt` and `evalStatement` implement `try`/`catch`/`finally`, but exception matching is only approximate in `matchesCatchClause` and uncaught `evalThrownError` is later swallowed by `evalStatementBody`, so behavior does not match real Groovy/Nextflow exception handling.

## Types, Literals and Operators
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-binary-expressions
`Binary expressions`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** the parser has precedence layers (`parseLogicalOrExprTokens`, `parseAdditiveExprTokens`, `parseMultiplicativeExprTokens`, etc.), but `evalBinaryExpr` in `nextflowdsl/groovy.go` only gives correct semantics for a limited subset (primarily bool/int comparisons and integer arithmetic). It also relies on `requireIntegerOperand`, so non-integer numeric operations differ, and repeated same-precedence expressions can leave `UnsupportedExpr` operands because `parseBinaryExprTokens` parses both sides with the lower-precedence operand parser.

### SYN-map
`Map`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseCollectionExprTokens` parses map literals, but `parseMapKeyExpr` coerces bare identifier keys to strings and `evalMapExpr` only accepts keys that evaluate to `string`, so map support is narrower than Groovy/Nextflow's general map semantics.

### SYN-number
`Number`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** the lexer only emits `tokenInt` via `readInt` in `nextflowdsl/parse.go`, `parsePrimaryExprTokens` only builds `IntExpr`, and arithmetic in `nextflowdsl/groovy.go` goes through `requireIntegerOperand`, so integer literals work but floating-point/decimal numeric literals and arithmetic do not match Nextflow/Groovy semantics.

### SYN-precedence
`Precedence`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** precedence levels are encoded in the recursive descent parser (`parseExprTokens` through `parsePrimaryExprTokens`), so mixed-precedence forms like additive vs multiplicative are structured, but associativity within the same precedence level is incomplete because `parseBinaryExprTokens` parses left and right operands with the next lower parser. Expressions like chained additions therefore do not evaluate with full Groovy/Nextflow semantics unless extra parentheses are added.

### SYN-unary-expressions
`Unary expressions`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseUnaryExprTokens` and `evalUnaryExpr` only handle `!`, `-`, and `~`; unary `+` and increment/decrement forms are not implemented, and `!` requires a real boolean through `evalBoolOperand` rather than Groovy truthiness.

## Syntax Basics
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-constructor-call
`Constructor call`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseNewExprTokens` in `nextflowdsl/parse.go` parses `new Class(...)`, but `evalNewExpr` in `nextflowdsl/groovy.go` only implements a small constructor whitelist and returns `UnsupportedExpr` for others.

### SYN-dynamic-string
`Dynamic string`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `interpolateGroovyString` and `resolveInterpolation` in `nextflowdsl/groovy.go` only resolve `${...}` using dotted variable paths via `resolveExprPath`; full Groovy interpolation semantics are not implemented.

### SYN-enum-type
`Enum type`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseEnumDef` in `nextflowdsl/parse.go` parses enums, while `bindWorkflowEnumValues` and `resolveEnumExprPath` in `nextflowdsl/groovy.go` only expose `Enum.VALUE` as plain string constants rather than full enum objects.

### SYN-feature-flag
`Feature flag`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `skipFeatureFlagAssignment` in `nextflowdsl/parse.go` explicitly discards `nextflow.enable.*` and `nextflow.preview.*` assignments, so they parse but have no effect.

### SYN-function
`Function`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseWorkflow` and `parseFunctionDef` in `nextflowdsl/parse.go` collect top-level functions, and `evalSimpleFuncDef` exists in `nextflowdsl/groovy.go`, but the inspected evaluator code exposes no reachable call/binding path for user-defined functions.

### SYN-index-expression
`Index expression`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseIndexExprTokens` in `nextflowdsl/parse.go` parses `expr[index]`, but `evalIndexExpr` in `nextflowdsl/groovy.go` only handles positive integer indexes into slices/arrays and string-key map lookups.

### SYN-multi-line-dynamic-string
`Triple-quoted dynamic string ("""...""") for multi-line interpolation`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** triple-quoted dynamic strings lex via `readString`, but interpolation is still limited by `interpolateGroovyString` and `resolveInterpolation` in `nextflowdsl/groovy.go` to `${...}` dotted-path substitutions.

### SYN-multi-line-string
`Triple-quoted string ('''...''') for multi-line literals`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `readString` in `nextflowdsl/parse.go` accepts triple-quoted literals, but `EvalExpr` in `nextflowdsl/groovy.go` still treats the resulting `StringExpr` as interpolated text rather than a distinct literal-only form.

### SYN-property-expression
`Property expression`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseVarExprTokens` and `parsePropertyPathTokens` in `nextflowdsl/parse.go` parse dotted access, but `resolveExprPath` and `resolvePropertyPath` in `nextflowdsl/groovy.go` only walk map-like values, not general Groovy object property semantics.

### SYN-record-type
`Record type`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseRecordDef` in `nextflowdsl/parse.go` stores record definitions on `Workflow.Records`, but the inspected evaluator code in `nextflowdsl/groovy.go` has no record construction or runtime field handling.

### SYN-slashy-string
`Slashy string (/pattern/) for regex patterns`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `readSlashyString` in `nextflowdsl/parse.go` tokenizes `/.../` as `SlashyStringExpr`, and `EvalExpr` in `nextflowdsl/groovy.go` returns its raw string value, which is usable as a regex-pattern string but does not implement full Groovy slashy-string semantics.

### SYN-string
`String`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `readString` in `nextflowdsl/parse.go` tokenizes both single- and double-quoted strings as `tokenString`, and `EvalExpr` in `nextflowdsl/groovy.go` always runs `StringExpr` through `interpolateGroovyString`, so literal strings are not kept distinct from dynamic strings.

### SYN-variable-declaration
`Variable declaration`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseStatement` routes `def` declarations to `parseAssignmentStmt` and `evalStatement` executes them inside statement bodies, but `parseWorkflow` treats top-level `def` only as function definitions and there is no typed declaration path here.

## Deprecations
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-deprecations
`Deprecations`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** support is partial. `parseSection` in `nextflowdsl/parse.go` accepts deprecated `when:` and `shell:` sections, and translation applies them via `filterWhenBindingSets`/`EvalWhenGuard` and `renderShellSection` in `nextflowdsl/translate.go`, but deprecated include clauses and `for`/`while` loop syntax are not parsed.

## Standard Library Types
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### TYPE-float
`Float`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** float values are partially supported because `evalNumberMethodCall` handles `float64`, but `evalBinaryExpr` arithmetic and ordered comparison paths fall back to integer-only helpers such as `requireIntegerOperand` and `compareOrderedOperands`.

### TYPE-iterable
`Iterable<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** looping works for slice/array-like values via `iterValues` and `closureTupleValues`, but there is no explicit `Iterable` type support in `matchesGroovyType`, `evalCastExpr`, or receiver dispatch.

### TYPE-operations
`Operations`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** path values only get generic string dispatch through `evalMethodCallExpr` and `evalStringMethodCall`; there is no path-specific operation layer in `resolvePropertyPath` or method dispatch.

### TYPE-path
`Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** `evalNewExpr` and `evalPathConstructor` construct cleaned path strings, but `evalMethodCallExpr` then treats the result as a plain string receiver rather than a real `Path` object.

### TYPE-reading
`Reading`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** `evalStringMethodCall` implements `readLines` and `eachLine` against the string value itself, so a path returned by `evalPathConstructor` would be split as text instead of reading file contents.

### TYPE-set
`Set<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** `evalListMethodCallExpr` supports `asType(Set|HashSet|LinkedHashSet)` and `evalListMethodCall` supports `toSet`, but both return deduplicated `[]any`, and there is no dedicated set type in `matchesGroovyType` or `evalNewExpr`.

### TYPE-tuple
`Tuple`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** tuple-like values work only as raw slice/array data via `closureTupleValues`, `transposeList`, and `evalMultiAssignExpr`; there is no dedicated `Tuple` constructor or type.

## Workflow
Source: https://nextflow.io/docs/latest/reference/syntax.html

### WF-onComplete
`workflow.onComplete handler`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** top-level `workflow.onComplete { ... }` is parsed by `parseWorkflowLifecycleHandler` but discarded there; only block-scoped `onComplete:` stored on `WorkflowBlock` is translated later by `translateLifecycleHooks`.

### WF-onError
`workflow.onError handler`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** top-level `workflow.onError { ... }` is parsed by `parseWorkflowLifecycleHandler` but discarded there; only block-scoped `onError:` stored on `WorkflowBlock` is translated by `translateLifecycleHooks`.

### WF-out-contentType
`output block: contentType directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `contentType` is consumed by `parseOutputTarget` and never used.

### WF-out-copyAttributes
`output block: copyAttributes directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `copyAttributes` is consumed by `parseOutputTarget` and never used.

### WF-out-directory
`output block: directory directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputTarget` ignores unknown output-target properties unless they are `path` or `index`, so `directory` is parsed and discarded.

### WF-out-enabled
`output block: enabled directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `enabled` is consumed by `parseOutputTarget` and has no effect on publish translation.

### WF-out-ignoreErrors
`output block: ignoreErrors directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `ignoreErrors` is consumed by `parseOutputTarget` and never used.

### WF-out-index
`output block: index directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputTarget` recognizes `index { ... }`, but only `index.path` is used; other `index` sub-directives do not affect translation.

### WF-out-index-header
`output block: index header sub-directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputIndexPath` only looks for `path`, so `header` is ignored.

### WF-out-index-path
`output block: index path sub-directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputIndexPath` supports only a static string path through `parseOutputStaticPath`; non-static values skip the target.

### WF-out-index-sep
`output block: index sep sub-directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputIndexPath` ignores `sep`, and `buildWorkflowPublishIndexCommand` always writes tab-separated lines.

### WF-out-label
`output block: label directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `label` is consumed by `parseOutputTarget` and never used.

### WF-out-mode
`output block: mode directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputTarget` ignores `mode`, and `buildWorkflowPublishCommand` always emits copy commands via `publishDirActionCommand("copy", ...)`.

### WF-out-overwrite
`output block: overwrite directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `overwrite` is consumed by `parseOutputTarget` but never stored or used during publish translation.

### WF-out-path
`output block: path directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseOutputTarget` requires `path`, but `parseOutputStaticPath` only accepts a static string; non-static or closure-valued paths cause the target to be skipped.

### WF-out-storageClass
`output block: storageClass directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `storageClass` is consumed by `parseOutputTarget` and never used.

### WF-out-tags
`output block: tags directive`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `tags` is consumed by `parseOutputTarget` and never used.

### WF-output-block
`Output block`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseTopLevelOutputBlock` stores the raw block, but `ParseOutputBlock`/`parseOutputTarget` in `nextflowdsl/translate.go` only keep targets with a static `path` and optional `index.path`; unsupported properties are consumed and ignored, and non-static targets are skipped.

### WF-until
`.until() method — recursion termination condition`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `supportedChannelOperators` includes `until`, so it can parse as a generic channel operator, but there is no recursion-specific AST or translation path in `translate.go`; workflow translation remains linear through `desugarWorkflowPipe`/`translateCalls`, so recursion termination semantics are not implemented.
