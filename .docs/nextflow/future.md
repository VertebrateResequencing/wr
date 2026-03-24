# Future Features

Nextflow features not currently implemented but feasible to add. Each entry describes what would need to change. These are candidates for new implementation tasks.

**Total: 205 features**

## Built-in Variables, Functions and Namespaces
Source: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

### BV-baseDir
`baseDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `baseDir` built-in.

### BV-branchCriteria
`branchCriteria( criteria: Closure ) -> Closure`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `branchCriteria(...)` built-in.

### BV-env
`env( name: String ) -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `env(...)` built-in.

### BV-error
`error( message: String = null )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `error(...)` built-in.

### BV-exit
`exit( exitCode: int = 0, message: String = null )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `exit(...)` built-in.

### BV-file
`file( filePattern: String, [options] ) -> Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** there is no `file(...)` built-in in `evalMethodCallExpr`; `evalPathConstructor` only covers `new File(...)`/`new Path(...)`.

### BV-file-checkIfExists
`file.checkIfExists: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-followLinks
`file.followLinks: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-glob
`file.glob: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-hidden
`file.hidden: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-maxDepth
`file.maxDepth: int`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-type
`file.type: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-files
`files( filePattern: String, [options] ) -> Iterable<Path>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** there is no `files(...)` built-in in `evalMethodCallExpr`; `evalPathConstructor` only covers constructors.

### BV-groupKey
`groupKey( key, size: int ) -> GroupKey`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `groupKey(...)` built-in implementation.

### BV-launchDir
`launchDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `launchDir` built-in.

### BV-log-error
`error( message: String )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.error(...)` namespace receiver is implemented.

### BV-log-info
`info( message: String )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.info(...)` namespace receiver is implemented.

### BV-log-warn
`warn( message: String )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.warn(...)` namespace receiver is implemented.

### BV-moduleDir
`moduleDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `moduleDir` built-in.

### BV-multiMapCriteria
`multiMapCriteria( criteria: Closure ) -> Closure`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `multiMapCriteria(...)` built-in implementation.

### BV-nextflow-build
`build: int`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.

### BV-nextflow-timestamp
`timestamp: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.

### BV-nextflow-version
`version: VersionNumber`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.

### BV-print
`print( value )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `print(...)` built-in implementation.

### BV-printf
`printf( format: String, values... )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `printf(...)` built-in implementation.

### BV-println
`println( value )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `println(...)` built-in implementation.

### BV-projectDir
`projectDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `projectDir` built-in.

### BV-record
`record( [options] ) -> Record`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `record(...)` built-in implementation.

### BV-secrets
`secrets: Map<String,String>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `secrets` built-in.

### BV-sendMail
`sendMail( [options] )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `sendMail(...)` built-in implementation.

### BV-sleep
`sleep( milliseconds: long )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `sleep(...)` built-in implementation.

### BV-tuple
`tuple( args... ) -> Tuple`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** bare calls are routed through `evalMethodCallExpr`, but there is no `tuple(...)` built-in implementation; the internal `closureTupleValues` helper is unrelated.

### BV-workDir
`workDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `workDir` built-in.

### BV-workflow-commandLine
`commandLine: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-commitId
`commitId: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-complete
`complete: OffsetDateTime`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-configFiles
`configFiles: List<Path>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-container
`container: String | Map<String,String>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-containerEngine
`containerEngine: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-duration
`duration: Duration`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-errorMessage
`errorMessage: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-errorReport
`errorReport: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-exitStatus
`exitStatus: int`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-failOnIgnore
`failOnIgnore: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-fusion
`fusion`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-fusion-enabled
`fusion.enabled: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-fusion-version
`fusion.version: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-homeDir
`homeDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-launchDir
`launchDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-manifest
`manifest`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-onComplete
`onComplete( action: Closure )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `evalMethodCallExpr` has no implementation for workflow lifecycle hook methods such as `workflow.onComplete(...)`.

### BV-workflow-onError
`onError( action: Closure )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `evalMethodCallExpr` has no implementation for workflow lifecycle hook methods such as `workflow.onError(...)`.

### BV-workflow-outputDir
`outputDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-preview
`preview: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-profile
`profile: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-projectDir
`projectDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-repository
`repository: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-resume
`resume: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-revision
`revision: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-runName
`runName: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-scriptFile
`scriptFile: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-scriptId
`scriptId: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-scriptName
`scriptName: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-sessionId
`sessionId: UUID`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-start
`start: OffsetDateTime`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-stubRun
`stubRun: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-success
`success: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-userName
`userName: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-wave
`wave`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-wave-enabled
`wave.enabled: boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-workDir
`workDir: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

**Current behaviour:** `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

## Directives: Scheduler
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-pod
`pod`
Reference: https://nextflow.io/docs/latest/reference/process.html

**Current behaviour:** there is no `pod` handling in the directive application list or in `buildRequirements` in `nextflowdsl/translate.go`; this could theoretically be mapped for Kubernetes execution, but it would require new translation logic.

## Groovy Methods & Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-bag-plus
`+ : (Bag<E>, Bag<E>) -> Bag<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-getDays
`getDays() — alias for toDays()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-getHours
`getHours() — alias for toHours()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-getMillis
`getMillis() — alias for toMillis()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-getMinutes
`getMinutes() — alias for toMinutes()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-getSeconds
`getSeconds() — alias for toSeconds()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-toDays
`toDays() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-toHours
`toHours() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-toMillis
`toMillis() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-toMinutes
`toMinutes() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-duration-toSeconds
`toSeconds() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-each
`each( action: (E) -> () )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toList
`toList() -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toSorted
`toSorted() -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toSorted-1
`toSorted( comparator: (E) -> R ) -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toSorted-2
`toSorted( comparator: (E,E) -> Integer ) -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toUnique
`toUnique() -> Iterable<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toUnique-1
`toUnique( comparator: (E) -> R ) -> Iterable<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-iterable-toUnique-2
`toUnique( comparator: (E,E) -> Integer ) -> Iterable<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-collate
`collate( size: Integer, keepRemainder: Boolean = true ) -> List<List<E>>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-collate-3
`collate( size: Integer, step: Integer, keepRemainder: Boolean = true ) -> List<List<E>>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-getIndices
`getIndices() -> List<Integer>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-indexOf
`indexOf( value: E ) -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-multiply
`* : (List<E>, Integer) -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-plus
`+ : (List<E>, List<E>) -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-subList
`subList( fromIndex: Integer, toIndex: Integer ) -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-list-takeWhile
`takeWhile( condition: (E) -> Boolean ) -> List<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-map-entrySet
`entrySet() -> Set<(K,V)>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-map-plus
`+ : (Map<K,V>, Map<K,V>) -> Map<K,V>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-getBytes
`getBytes() — alias for toBytes()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-getGiga
`getGiga() — alias for toGiga()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-getKilo
`getKilo() — alias for toKilo()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-getMega
`getMega() — alias for toMega()`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-toBytes
`toBytes() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-toGiga
`toGiga() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-toKilo
`toKilo() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-toMega
`toMega() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-memoryunit-toUnit
`toUnit( unit: String ) -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-append
`append( text: String )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-baseName
`baseName: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-copyTo
`copyTo( target: Path )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-countFasta
`countFasta() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-countFastq
`countFastq() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-countJson
`countJson() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-countLines
`countLines() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-delete
`delete() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-deleteDir
`deleteDir() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-divide
`/ : (Path, String) -> Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-eachFile
`eachFile( action: (Path) -> () )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-eachFileRecurse
`eachFileRecurse( action: (Path) -> () )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-exists
`exists() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-extension
`extension: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-getPermissions
`getPermissions() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-getText
`getText() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-isDirectory
`isDirectory() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-isEmpty
`isEmpty() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-isFile
`isFile() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-isHidden
`isHidden() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-isLink
`isLink() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-lastModified
`lastModified() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-leftShift
`<< : (Path, String)`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-listDirectory
`listDirectory() -> Iterable<Path>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-listFiles
`listFiles() -> Iterable<Path>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-mkdir
`mkdir() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-mkdirs
`mkdirs() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-mklink
`mklink( linkName: String, [options] ) -> Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-mklink-hard
`mklink.hard: Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-mklink-overwrite
`mklink.overwrite: Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-moveTo
`moveTo( target: Path )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-name
`name: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-parent
`parent: Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-relativize
`relativize(other: Path) -> Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-renameTo
`renameTo( target: String ) -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-resolve
`resolve(other: String) -> Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-resolveSibling
`resolveSibling(other: String) -> Path`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-scheme
`scheme: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-setPermissions
`setPermissions( permissions: String ) -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-setPermissions-3
`setPermissions( owner: Integer, group: Integer, other: Integer ) -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-setText
`setText( text: String )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-simpleName
`simpleName: String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-size
`size() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-splitCsv
`splitCsv() -> List<?>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-splitFasta
`splitFasta() -> List<?>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-splitFastq
`splitFastq() -> List<?>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-splitJson
`splitJson() -> List<?>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-splitText
`splitText() -> List<String>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-toUriString
`toUriString() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-withReader
`withReader( action: (BufferedReader) -> () )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-path-write
`write( text: String )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-record-plus
`+ : (Record, Record) -> Record`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-set-minus
`- : (Set<E>, Iterable<E>) -> Set<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-set-plus
`+ : (Set<E>, Iterable<E>) -> Set<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-bitwiseNegate
`~ : (String) -> Pattern`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-find
`=~ : (String, String) -> Matcher`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-getAt
`[] : (String, Integer) -> char`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-indexOf
`indexOf( str: String ) -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-indexOf-2
`indexOf( str: String, fromIndex: Integer ) -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-isBlank
`isBlank() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-isEmpty
`isEmpty() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-isFloat
`isFloat() -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-lastIndexOf
`lastIndexOf( str: String ) -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-lastIndexOf-2
`lastIndexOf( str: String, fromIndex: Integer ) -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-length
`length() -> Integer`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-match
`==~ : (String, String) -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-md5
`md5() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-multiply
`* : (String, Integer) -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-plus
`+ : (String, String) -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-sha256
`sha256() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-strip
`strip() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-stripLeading
`stripLeading() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-stripTrailing
`stripTrailing() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-string-toFloat
`toFloat() -> Float`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-value-flatMap
`flatMap( transform: (V) -> Iterable<R> ) -> Channel<R>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-value-map
`map( transform: (V) -> R ) -> Value<R>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-value-subscribe
`subscribe( action: (V) -> () )`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-value-view
`view() -> Value<V>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-value-view-1
`view( transform: (V) -> String ) -> Value<V>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-versionnumber-getMajor
`getMajor() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-versionnumber-getMinor
`getMinor() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-versionnumber-getPatch
`getPatch() -> String`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-versionnumber-matches
`matches( condition: String ) -> Boolean`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

## Syntax Basics
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-function-call
`Function call`
Reference: https://nextflow.io/docs/latest/reference/syntax.html

**Current behaviour:** `parseMethodCallExprTokens` in `nextflowdsl/parse.go` requires `receiver.method(...)` or `receiver.method { ... }`; bare `foo()` calls are not parsed into a callable form, and `EvalExpr` in `nextflowdsl/groovy.go` has no top-level function invocation case.

## Standard Library Types
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### TYPE-bag
`Bag<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** `evalNewExpr`, `evalCastExpr`, and `matchesGroovyType` have no `Bag` handling; adding a bag type would require new constructor/type/method support rather than using an existing evaluator type.

### TYPE-channel
`Channel<E>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** there is no dedicated `Channel` support in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`; `evalMethodCallExpr` only dispatches concrete receivers like `[]any`, maps, strings, and numbers.

### TYPE-duration
`Duration`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** no `Duration` constructor, cast, static helper, or type check exists in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`.

### TYPE-filesystem-attributes
`Filesystem attributes`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** `resolvePropertyPath` and `lookupVariablePart` only resolve map-like/object-like lookups, and there is no implementation for path attributes such as file name, extension, or size.

### TYPE-filesystem-operations
`Filesystem operations`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** there is no evaluator support for path operations such as existence checks, deletion, directory creation, or listing; `evalStringMethodCall` contains no such methods.

### TYPE-memoryunit
`MemoryUnit`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** no `MemoryUnit` handling appears in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`.

### TYPE-record
`Record`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** `Record` is not implemented in `evalNewExpr`, `evalCastExpr`, `matchesGroovyType`, or receiver dispatch.

### TYPE-splitting-files
`Splitting files`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** the evaluator only implements plain string splitting (`split`, `tokenize`) in `evalStringMethodCall`; there are no file-based `split*` helpers.

### TYPE-value
`Value<V>`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** there is no dedicated `Value` type support in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, `matchesGroovyType`, or receiver dispatch.

### TYPE-versionnumber
`VersionNumber`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** no `VersionNumber` constructor, cast, static helper, or type check exists in the cited evaluator implementation.

### TYPE-writing
`Writing`
Reference: https://nextflow.io/docs/latest/reference/stdlib-types.html

**Current behaviour:** there are no write or append operations for path/string receivers in `evalStringMethodCall` or elsewhere in the cited evaluator code.
