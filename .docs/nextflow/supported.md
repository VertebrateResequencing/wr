# Supported Features

Nextflow features that are fully implemented in wr's nextflowdsl package. These parse correctly and produce correct behaviour.

**Total: 134 features**

## Built-in Variables, Functions and Namespaces
Source: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

### BV-params
`params`

`EvalExpr` has a dedicated `ParamsExpr` path and `resolveExprPath` gives `params.*` nil-on-missing semantics instead of erroring.

## Channel Factories
Source: https://nextflow.io/docs/latest/reference/channel.html

### CF-empty
`empty`

`resolveChannelFactoryItems` handles `empty` by requiring zero args and returning no items, which matches an empty channel.

### CF-from
`from`

`resolveChannelFactoryItems` maps deprecated `from` to `resolveChannelLiteralItems`, so each argument is emitted as a channel item with only a compatibility warning.

### CF-fromList
`fromList`

`resolveChannelFromListItems` evaluates the single list argument and emits each element as a separate channel item via `flattenChannelValues`.

### CF-of
`of`

`resolveChannelFactoryItems` dispatches `of` to `resolveChannelLiteralItems`, which evaluates each argument and emits it as a channel item.

### CF-value
`value`

`resolveChannelFactoryItems` evaluates the single argument for `value` and wraps it in one `channelItem`, matching a single-value channel.

## Operators: Forking
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-branch
`branch`

`applyChannelOperator` dispatches `branch` to `resolveBranchChannelItems`, which evaluates named entries in order, applies `default` fallback, and labels emitted items; downstream label selection is implemented by `NamedChannelRef` plus `selectNamedChannelItems` in `channel.go`.

### CO-multiMap
`multiMap`

`applyChannelOperator` dispatches `multiMap` to `resolveMultiMapChannelItems`, which evaluates every named mapping for each item and labels results so `NamedChannelRef` can select each forked output.

## Operators: Combining
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-concat
`concat`

`applyChannelOperator` resolves each extra channel and `concatChannelItems` appends them in sequence, matching `concat` semantics.

### CO-mix
`mix`

`applyChannelOperator` for `mix` resolves each input channel and appends all items into one output stream, preserving the operator’s practical effect.

## Operators: Filtering
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-first
`first`

`applyChannelOperator` returns `items[:1]` for `first`, matching the basic first-item behavior and returning no items for an empty channel.

### CO-last
`last`

`applyChannelOperator` returns the final item with `items[len(items)-1:]` for `last`, matching the basic last-item behavior and returning no items for an empty channel.

### CO-randomSample
`randomSample`

`randomSampleChannelItems` implements sampling without replacement, supports an optional integer seed via `randomSampleOperatorArgs`, and preserves selected items in source order after sampling.

### CO-take
`take`

`applyChannelOperator` evaluates a single integer argument for `take` and returns the first `n` items, clamping to channel length and yielding no items for non-positive counts.

## Operators: Transforming
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-flatten
`flatten`

`applyChannelOperator` calls `flattenSliceValue`, and `flattenSliceValue` in `nextflowdsl/groovy.go` recursively flattens nested slices/arrays, which matches the core flatten behaviour.

### CO-toList
`toList`

`applyChannelOperator` emits a single item containing `collectChannelValues(items)` in `nextflowdsl/channel.go`, matching the basic `toList` behaviour.

## Operators: Splitting
Source: https://nextflow.io/docs/latest/reference/operator.html

### CO-splitCsv
`splitCsv`

`applyChannelOperator` dispatches `splitCsv` to `splitCSVChannelItems`, and `splitCSVFile` reads CSV files and emits rows, with `header` and `by` options implemented.

### CO-splitFasta
`splitFasta`

`applyChannelOperator` dispatches `splitFasta` to `splitFASTAChannelItems`, and `splitFASTAFile` splits FASTA input on `>` record boundaries with `by` chunking support.

### CO-splitFastq
`splitFastq`

`applyChannelOperator` dispatches `splitFastq` to `splitFASTQChannelItems`, and `splitFASTQValue`/`splitFASTQFile` split FASTQ records with `by` chunking and `pe:true` paired-end handling.

### CO-splitJson
`splitJson`

`applyChannelOperator` dispatches `splitJson` to `splitJSONChannelItems`, and `splitJSONFile` decodes JSON and emits either array elements or the selected `path` value.

### CO-splitText
`splitText`

`applyChannelOperator` dispatches `splitText` to `splitTextChannelItems`, and `splitTextFile` emits line-based chunks with `by` support.

## Directives: Environment
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-container
`container`

`applyContainer` in `nextflowdsl/translate.go` resolves the `container` directive and assigns it to `job.WithDocker` or `job.WithSingularity`, so the task is configured to run with the selected container image.

### DIR-containerOptions
`containerOptions`

`applyContainer` in `nextflowdsl/translate.go` resolves `containerOptions` and prepends the raw options string to the container spec before setting `job.WithDocker`/`job.WithSingularity`, preserving runtime options for execution.

### DIR-module
`module`

`moduleLoadLines` and `buildCommandBody` in `nextflowdsl/translate.go` turn the directive into `module load ...` commands inserted before the task body, including colon-separated module lists.

## Directives: Resources
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-cpus
`cpus`

`buildRequirements` in `nextflowdsl/translate.go` resolves `cpus` with `resolveDirectiveInt` and writes it to `scheduler.Requirements.Cores`.

### DIR-disk
`disk`

`diskRE` in `nextflowdsl/parse.go` normalises disk strings to GB integers, and `buildRequirements` in `nextflowdsl/translate.go` writes the value to `scheduler.Requirements.Disk`.

### DIR-memory
`memory`

`memoryRE` in `nextflowdsl/parse.go` normalises memory strings, and `buildRequirements` in `nextflowdsl/translate.go` writes the resolved value to `scheduler.Requirements.RAM`.

### DIR-time
`time`

`timeRE` in `nextflowdsl/parse.go` normalises duration strings to minutes, and `buildRequirements` in `nextflowdsl/translate.go` converts that to `scheduler.Requirements.Time`.

## Directives: Other
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-ext
`ext`

`extDirectiveSourceMap`, `mergeExtValues`, and `resolveExtDirectiveWithValues` evaluate and merge the `ext` map, and `interpolateKnownScriptVars` resolves `${task.ext.*}` in scripts, which gives translated jobs access to `task.ext` values.

### DIR-label
`label`

`parseDirective` appends labels to `proc.Labels`, and `selectorMatchesProcess` plus `processWithConfigDefaults` use those labels to apply `withLabel` config selectors to the translated process, matching the practical effect of Nextflow labels.

### DIR-shell
`shell`

`resolveShellDirective` accepts a string or list of strings for the `shell` directive, and `applyEnv` exports the resolved command as `RunnerExecShell`, so translated jobs carry an explicit shell override.

## Directives: Execution
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-maxErrors
`maxErrors`

`newProcessMaxErrorsJob` in `nextflowdsl/translate.go` resolves `maxErrors`, adds a helper job, and that helper polls `wr status`, then kills or removes remaining jobs once the buried-count exceeds the configured limit.

### DIR-maxForks
`maxForks`

`applyMaxForks` in `nextflowdsl/translate.go` appends a process-specific limit group `name:maxForks` to each job during translation, which is the runtime control used for concurrent job limiting.

### DIR-maxRetries
`maxRetries`

`parseDirective` stores `maxRetries` on the process in `nextflowdsl/parse.go`, and `applyErrorStrategy` uses `proc.MaxRetries` when `errorStrategy` resolves to `retry`.

### DIR-storeDir
`storeDir`

`resolveStoreDirDirective` and `wrapStoreDirCommand` in `nextflowdsl/translate.go` resolve the cache directory, skip execution when declared outputs already exist there, copy cached outputs back into the work directory, and copy fresh outputs into the store after a successful run.

## Directives: Scheduler
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-queue
`queue`

`buildRequirements` resolves `queue` with `resolveDirectiveString` and stores it as `req.Other["scheduler_queue"]`, which gives wr a scheduler queue selection matching the directive's practical effect.

## Includes & Parameters
Source: https://nextflow.io/docs/latest/reference/syntax.html

### INCL-include
`Include`

`parseWorkflow` in `parse.go` accepts top-level `include` statements, and `loadWorkflowFile` plus `selectImportedDefinitions` in `module_load.go` resolve module paths, recursively load imported workflows, and merge the selected processes/subworkflows into the resulting workflow.

## Input Qualifiers & Options
Source: https://nextflow.io/docs/latest/reference/process.html

### INP-env
`env( name )`

`parseDeclarationPrimary` captures the env variable name for `env(...)`, and `buildCommandBody` exports the bound value under that name.

### INP-tuple
`tuple( arg1, arg2, ... )`

`parseTupleDeclaration`, `bindingsForInputDeclaration`, and `flattenedInputDeclarations` bind tuple items element-by-element and expose named elements to the generated job.

### INP-val
`val( identifier )`

`resolveBindings` and `bindingsForInputDeclaration` pass non-tuple inputs through unchanged, and `buildCommandBody` exports named inputs so `val(x)` is available to the job as `x`.

## Groovy Methods & Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-bag-membership
`in, !in : (E, Bag<E>) -> Boolean`

### METH-iterable-any
`any( condition: (E) -> Boolean ) -> Boolean`

### METH-iterable-collect
`collect( transform: (E) -> R ) -> Iterable<R>`

### METH-iterable-collectMany
`collectMany( transform: (E) -> Iterable<R> ) -> Iterable<R>`

### METH-iterable-contains
`contains( value: E ) -> Boolean`

### METH-iterable-every
`every( condition: (E) -> Boolean ) -> Boolean`

### METH-iterable-findAll
`findAll( condition: (E) -> Boolean ) -> Iterable<E>`

### METH-iterable-groupBy
`groupBy( transform: (E) -> K ) -> Map<K,Iterable<E>>`

### METH-iterable-inject-3
`inject( initialValue: R, accumulator: (R,E) -> R ) -> R`

### METH-iterable-isEmpty
`isEmpty() -> Boolean`

### METH-iterable-max
`max() -> E`

### METH-iterable-max-1
`max( comparator: (E) -> R ) -> E`

### METH-iterable-min
`min() -> E`

### METH-iterable-min-1
`min( comparator: (E) -> R ) -> E`

### METH-iterable-size
`size() -> Integer`

### METH-iterable-sum
`sum() -> E`

### METH-iterable-sum-1
`sum( transform: (E) -> R ) -> R`

### METH-iterable-toSet
`toSet() -> Set<E>`

### METH-list-find
`find( condition: (E) -> Boolean ) -> E`

### METH-list-first
`first() -> E`

### METH-list-getAt
`[] : (List<E>, Integer) -> E`

### METH-list-head
`head() -> E`

### METH-list-init
`init() -> List<E>`

### METH-list-last
`last() -> E`

### METH-list-membership
`in, !in : (E, List<E>) -> Boolean`

### METH-list-reverse
`reverse() -> List<E>`

### METH-list-tail
`tail() -> List<E>`

### METH-list-take
`take( n: Integer ) -> List<E>`

### METH-list-withIndex
`withIndex() -> List<(E,Integer)>`

### METH-map-any
`any( condition: (K,V) -> Boolean ) -> Boolean`

### METH-map-containsKey
`containsKey( key: K ) -> Boolean`

### METH-map-containsValue
`containsValue( value: V ) -> Boolean`

### METH-map-each
`each( action: (K,V) -> () )`

### METH-map-every
`every( condition: (K,V) -> Boolean ) -> Boolean`

### METH-map-getAt
`[] : (Map<K,V>, K) -> V`

### METH-map-isEmpty
`isEmpty() -> Boolean`

### METH-map-keySet
`keySet() -> Set<K>`

### METH-map-membership
`in, !in : (K, Map<K,V>) -> Boolean`

### METH-map-size
`size() -> Integer`

### METH-map-subMap
`subMap( keys: Iterable<K> ) -> Map<K,V>`

### METH-map-values
`values() -> Bag<V>`

### METH-record-subMap
`subMap( keys: Iterable<String> ) -> Record`

### METH-set-intersect
`intersect( right: Iterable<E> ) -> Set<E>`

### METH-set-membership
`in, !in : (E, Set<E>) -> Boolean`

### METH-string-contains
`contains( str: String ) -> Boolean`

### METH-string-endsWith
`endsWith( suffix: String ) -> Boolean`

### METH-string-isDouble
`isDouble() -> Boolean`

### METH-string-isInteger
`isInteger() -> Boolean`

### METH-string-isLong
`isLong() -> Boolean`

### METH-string-replace
`replace( target: String, replacement: String ) -> String`

### METH-string-replaceAll
`replaceAll( regex: String, replacement: String ) -> String`

### METH-string-replaceFirst
`replaceFirst( regex: String, replacement: String ) -> String`

### METH-string-startsWith
`startsWith( prefix: String ) -> Boolean`

### METH-string-stripIndent
`stripIndent() -> String`

### METH-string-substring
`substring( beginIndex: Integer ) -> String`

### METH-string-substring-2
`substring( beginIndex: Integer, endIndex: Integer ) -> String`

### METH-string-toBoolean
`toBoolean() -> Boolean`

### METH-string-toDouble
`toDouble() -> Float`

### METH-string-toInteger
`toInteger() -> Integer`

### METH-string-toLong
`toLong() -> Integer`

### METH-string-toLowerCase
`toLowerCase() -> String`

### METH-string-toUpperCase
`toUpperCase() -> String`

### METH-string-tokenize
`tokenize( delimiters: String ) -> List<String>`

### METH-tuple-getAt
`[] : (Tuple, Integer) -> ?`

## Output Qualifiers & Options
Source: https://nextflow.io/docs/latest/reference/process.html

### OUT-file
`file( pattern )`

`parseDeclarationPrimary` in `nextflowdsl/parse.go` accepts `file(...)`, and `outputPatterns`, `outputPaths`, `outputValue`, and `completedOutputPathsForJob` in `nextflowdsl/translate.go` handle `file` exactly like `path`, including glob expansion to concrete completed files.

### OUT-path
`path( pattern, [options] )`

`parseDeclarationPrimary` accepts `path(...)`, and `outputPatterns`, `outputPaths`, `resolveCompletedOutputValue`, and `completedOutputPathsForJob` in `nextflowdsl/translate.go` carry path patterns through translation and resolve them to concrete output paths after job completion.

## Process Sections
Source: https://nextflow.io/docs/latest/reference/process.html

### PSEC-script
`script: section of a process definition`

`parseSection` stores `script`, and `buildCommandWithValues` -> `buildCommandBody` -> `renderScript` turns it into the job command with input binding and process environment handling.

### PSEC-stub
`stub: section of a process definition`

`parseSection` stores `stub`, and `translateProcessBindingSets` swaps `procForCommand.Script` to `proc.Stub` when `tc.StubRun` is enabled, which implements stub-run behavior.

## Statements
Source: https://nextflow.io/docs/latest/reference/syntax.html

### STMT-assignment
`Assignment`

`parseAssignmentStmt` in `nextflowdsl/groovy.go` parses `def x = ...`, `x = ...`, and augmented assignment for bare identifiers, and `evalStatement` executes them via `evalAssignStmt` and `evalAugAssignStmt` by updating the evaluation scope.

### STMT-expression-statement
`Expression statement`

`parseStatement` falls back to parsing an expression statement, and `evalStatement` executes `evalExprStmt`, returning the expression value as the current block result.

### STMT-ifelse
`if/else`

`parseIfStmt` parses `if`/`else if`/`else`, and `evalStatement` executes `evalIfStmt` using Groovy-style truthiness via `isTruthy`.

### STMT-return
`return`

`parseReturnStmt` parses `return`, and `evalStatement` marks `evalReturnStmt` as returned so `evalStatementBlock` exits early with the return value.

## Types, Literals and Operators
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-boolean
`Boolean`

`parsePrimaryExprTokens` maps `true` and `false` to `BoolExpr`, and `EvalExpr` returns native booleans in `nextflowdsl/groovy.go`.

### SYN-list
`List`

`parseCollectionExprTokens` in `nextflowdsl/parse.go` parses `[]` and comma-separated list literals into `ListExpr`, and `evalListExpr` in `nextflowdsl/groovy.go` evaluates elements into a Go slice.

### SYN-null
`Null`

`parsePrimaryExprTokens` maps `null` to `NullExpr`, and `EvalExpr` returns `nil`; `isTruthy` also treats nil as false in `nextflowdsl/groovy.go`.

### SYN-parentheses
`Parentheses`

`isParenthesisedExpr` in `nextflowdsl/parse.go` detects surrounding parentheses and `parsePrimaryExprTokens` reparses the inner expression, so explicit grouping is supported.

### SYN-ternary-expression
`Ternary expression`

`parseTernaryExprTokens` in `nextflowdsl/parse.go` parses both `cond ? a : b` and Elvis-style `a ?: b`, and `evalTernaryExpr` in `nextflowdsl/groovy.go` evaluates them using `isTruthy`.

### SYN-variable
`Variable`

`parseVarExprTokens` in `nextflowdsl/parse.go` parses identifiers and dotted paths, and `EvalExpr`/`resolveExprPath` in `nextflowdsl/groovy.go` resolve `VarExpr` and `ParamsExpr` values from the evaluation context.

## Syntax Basics
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-closure
`Closure`

`parseClosureExpr` in `nextflowdsl/parse.go` parses closure literals, and `EvalExpr` in `nextflowdsl/groovy.go` preserves `ClosureExpr` values for supported consumers.

### SYN-comments
`Comments`

`lex` in `nextflowdsl/parse.go` skips `//` line comments and `/* */` block comments during tokenization.

### SYN-shebang
`Shebang`

`lex` in `nextflowdsl/parse.go` skips a `#` line when `lineHasContent` is false, so a shebang-style first line is ignored by the parser.

## Deprecations
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-deprecated-shell-section
`shell: section of a process definition`

`parseSection` stores `shell:` into `proc.Shell` in `nextflowdsl/parse.go`, and `renderScript` dispatches to `renderShellSection` in `nextflowdsl/translate.go`, which interpolates `!{...}` while preserving shell `${...}` syntax.

### SYN-deprecated-when-section
`when: section of a process definition`

`parseSection` stores `when:` into `proc.When` in `nextflowdsl/parse.go`, and `filterWhenBindingSets` with `EvalWhenGuard` in `nextflowdsl/translate.go` evaluates the guard before creating jobs.

## Standard Library Types
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### TYPE-boolean
`Boolean`

booleans evaluate directly through `EvalExpr`, logical operators are handled in `evalBinaryExpr`, truthiness is implemented in `isTruthy`, and `matchesGroovyType` recognizes `Boolean`.

### TYPE-integer
`Integer`

integer arithmetic and ordered comparisons are implemented in `evalBinaryExpr`, casts in `evalCastExpr`, `Integer.parseInt` in `evalStaticMethodCall`, and numeric helpers in `evalNumberMethodCall`.

### TYPE-list
`List<E>`

list literals are built by `evalListExpr`, indexing is implemented in `evalIndexExpr`, list methods are handled by `evalListMethodCallExpr` and `evalListMethodCall`, and `matchesGroovyType` recognizes `List`.

### TYPE-map
`Map<K,V>`

map literals are built by `evalMapExpr`, indexing is implemented in `evalIndexExpr`, map methods are handled by `evalMapMethodCall`, and `matchesGroovyType` recognizes `Map`.

### TYPE-string
`String`

string interpolation is implemented in `interpolateGroovyString`, string methods in `evalStringMethodCall`, casts in `evalCastExpr`, and `matchesGroovyType` recognizes `String`.

## Workflow
Source: https://nextflow.io/docs/latest/reference/syntax.html

### WF-emit
`emit: section of a workflow`

`parseWorkflowEmitLine` records workflow emits, and `bindSubworkflowOutputs` turns them into synthetic emitted outputs for downstream references.

### WF-main
`main: section of a workflow`

`parseWorkflowBlock` treats `main` as the executable section and `translateBlock` translates its calls and conditionals.

### WF-pipe
`pipe (|) operator for chaining processes/workflows`

`parseChanExpr` builds a `PipeExpr`, and `desugarWorkflowPipe` rewrites `a | b | c` into sequential bare-identifier calls that `translateCalls` can run for processes or subworkflows.

### WF-publish
`publish: section of a workflow`

`parseWorkflowPublishLine` records `publish:` assignments, and `translateWorkflowPublishes` attaches publish copy behaviours and optional index jobs from those assignments.

### WF-take
`take: section of a workflow`

`parseWorkflowTakeLine` records `take:` inputs and `bindSubworkflowInputs` wires call arguments into scoped subworkflow inputs.

### WF-workflow
`Workflow`

`parseWorkflow` in `nextflowdsl/parse.go` builds `EntryWF`/`SubWFs`, and `Translate` plus `translateBlock`/`translateCalls` in `nextflowdsl/translate.go` translate entry and named workflow blocks into jobs.
