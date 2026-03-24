# Future Features

Nextflow features not currently implemented but feasible to add. Each entry describes what would need to change. These are candidates for new implementation tasks.

**Total: 205 features**

## Built-in Variables, Functions and Namespaces
Source: https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

### BV-baseDir
`baseDir: Path`

`EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `baseDir` built-in.

### BV-branchCriteria
`branchCriteria( criteria: Closure ) -> Closure`

bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `branchCriteria(...)` built-in.

### BV-env
`env( name: String ) -> String`

bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `env(...)` built-in.

### BV-error
`error( message: String = null )`

bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `error(...)` built-in.

### BV-exit
`exit( exitCode: int = 0, message: String = null )`

bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `exit(...)` built-in.

### BV-file
`file( filePattern: String, [options] ) -> Path`

there is no `file(...)` built-in in `evalMethodCallExpr`; `evalPathConstructor` only covers `new File(...)`/`new Path(...)`.

### BV-file-checkIfExists
`file.checkIfExists: boolean`

`file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-followLinks
`file.followLinks: boolean`

`file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-glob
`file.glob: boolean`

`file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-hidden
`file.hidden: boolean`

`file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-maxDepth
`file.maxDepth: int`

`file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-file-type
`file.type: String`

`file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.

### BV-files
`files( filePattern: String, [options] ) -> Iterable<Path>`

there is no `files(...)` built-in in `evalMethodCallExpr`; `evalPathConstructor` only covers constructors.

### BV-groupKey
`groupKey( key, size: int ) -> GroupKey`

bare calls are routed through `evalMethodCallExpr`, but there is no `groupKey(...)` built-in implementation.

### BV-launchDir
`launchDir: Path`

`EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `launchDir` built-in.

### BV-log-error
`error( message: String )`

`evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.error(...)` namespace receiver is implemented.

### BV-log-info
`info( message: String )`

`evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.info(...)` namespace receiver is implemented.

### BV-log-warn
`warn( message: String )`

`evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.warn(...)` namespace receiver is implemented.

### BV-moduleDir
`moduleDir: Path`

`EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `moduleDir` built-in.

### BV-multiMapCriteria
`multiMapCriteria( criteria: Closure ) -> Closure`

bare calls are routed through `evalMethodCallExpr`, but there is no `multiMapCriteria(...)` built-in implementation.

### BV-nextflow-build
`build: int`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.

### BV-nextflow-timestamp
`timestamp: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.

### BV-nextflow-version
`version: VersionNumber`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.

### BV-print
`print( value )`

bare calls are routed through `evalMethodCallExpr`, but there is no `print(...)` built-in implementation.

### BV-printf
`printf( format: String, values... )`

bare calls are routed through `evalMethodCallExpr`, but there is no `printf(...)` built-in implementation.

### BV-println
`println( value )`

bare calls are routed through `evalMethodCallExpr`, but there is no `println(...)` built-in implementation.

### BV-projectDir
`projectDir: Path`

`EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `projectDir` built-in.

### BV-record
`record( [options] ) -> Record`

bare calls are routed through `evalMethodCallExpr`, but there is no `record(...)` built-in implementation.

### BV-secrets
`secrets: Map<String,String>`

`EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `secrets` built-in.

### BV-sendMail
`sendMail( [options] )`

bare calls are routed through `evalMethodCallExpr`, but there is no `sendMail(...)` built-in implementation.

### BV-sleep
`sleep( milliseconds: long )`

bare calls are routed through `evalMethodCallExpr`, but there is no `sleep(...)` built-in implementation.

### BV-tuple
`tuple( args... ) -> Tuple`

bare calls are routed through `evalMethodCallExpr`, but there is no `tuple(...)` built-in implementation; the internal `closureTupleValues` helper is unrelated.

### BV-workDir
`workDir: Path`

`EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `workDir` built-in.

### BV-workflow-commandLine
`commandLine: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-commitId
`commitId: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-complete
`complete: OffsetDateTime`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-configFiles
`configFiles: List<Path>`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-container
`container: String | Map<String,String>`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-containerEngine
`containerEngine: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-duration
`duration: Duration`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-errorMessage
`errorMessage: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-errorReport
`errorReport: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-exitStatus
`exitStatus: int`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-failOnIgnore
`failOnIgnore: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-fusion
`fusion`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-fusion-enabled
`fusion.enabled: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-fusion-version
`fusion.version: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-homeDir
`homeDir: Path`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-launchDir
`launchDir: Path`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-manifest
`manifest`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-onComplete
`onComplete( action: Closure )`

`evalMethodCallExpr` has no implementation for workflow lifecycle hook methods such as `workflow.onComplete(...)`.

### BV-workflow-onError
`onError( action: Closure )`

`evalMethodCallExpr` has no implementation for workflow lifecycle hook methods such as `workflow.onError(...)`.

### BV-workflow-outputDir
`outputDir: Path`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-preview
`preview: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-profile
`profile: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-projectDir
`projectDir: Path`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-repository
`repository: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-resume
`resume: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-revision
`revision: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-runName
`runName: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-scriptFile
`scriptFile: Path`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-scriptId
`scriptId: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-scriptName
`scriptName: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-sessionId
`sessionId: UUID`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-start
`start: OffsetDateTime`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-stubRun
`stubRun: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-success
`success: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-userName
`userName: String`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-wave
`wave`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-wave-enabled
`wave.enabled: boolean`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

### BV-workflow-workDir
`workDir: Path`

`resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.

## Directives: Scheduler
Source: https://nextflow.io/docs/latest/reference/process.html

### DIR-pod
`pod`

there is no `pod` handling in the directive application list or in `buildRequirements` in `nextflowdsl/translate.go`; this could theoretically be mapped for Kubernetes execution, but it would require new translation logic.

## Groovy Methods & Imports
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### METH-bag-plus
`+ : (Bag<E>, Bag<E>) -> Bag<E>`

### METH-duration-getDays
`getDays() — alias for toDays()`

### METH-duration-getHours
`getHours() — alias for toHours()`

### METH-duration-getMillis
`getMillis() — alias for toMillis()`

### METH-duration-getMinutes
`getMinutes() — alias for toMinutes()`

### METH-duration-getSeconds
`getSeconds() — alias for toSeconds()`

### METH-duration-toDays
`toDays() -> Integer`

### METH-duration-toHours
`toHours() -> Integer`

### METH-duration-toMillis
`toMillis() -> Integer`

### METH-duration-toMinutes
`toMinutes() -> Integer`

### METH-duration-toSeconds
`toSeconds() -> Integer`

### METH-iterable-each
`each( action: (E) -> () )`

### METH-iterable-toList
`toList() -> List<E>`

### METH-iterable-toSorted
`toSorted() -> List<E>`

### METH-iterable-toSorted-1
`toSorted( comparator: (E) -> R ) -> List<E>`

### METH-iterable-toSorted-2
`toSorted( comparator: (E,E) -> Integer ) -> List<E>`

### METH-iterable-toUnique
`toUnique() -> Iterable<E>`

### METH-iterable-toUnique-1
`toUnique( comparator: (E) -> R ) -> Iterable<E>`

### METH-iterable-toUnique-2
`toUnique( comparator: (E,E) -> Integer ) -> Iterable<E>`

### METH-list-collate
`collate( size: Integer, keepRemainder: Boolean = true ) -> List<List<E>>`

### METH-list-collate-3
`collate( size: Integer, step: Integer, keepRemainder: Boolean = true ) -> List<List<E>>`

### METH-list-getIndices
`getIndices() -> List<Integer>`

### METH-list-indexOf
`indexOf( value: E ) -> Integer`

### METH-list-multiply
`* : (List<E>, Integer) -> List<E>`

### METH-list-plus
`+ : (List<E>, List<E>) -> List<E>`

### METH-list-subList
`subList( fromIndex: Integer, toIndex: Integer ) -> List<E>`

### METH-list-takeWhile
`takeWhile( condition: (E) -> Boolean ) -> List<E>`

### METH-map-entrySet
`entrySet() -> Set<(K,V)>`

### METH-map-plus
`+ : (Map<K,V>, Map<K,V>) -> Map<K,V>`

### METH-memoryunit-getBytes
`getBytes() — alias for toBytes()`

### METH-memoryunit-getGiga
`getGiga() — alias for toGiga()`

### METH-memoryunit-getKilo
`getKilo() — alias for toKilo()`

### METH-memoryunit-getMega
`getMega() — alias for toMega()`

### METH-memoryunit-toBytes
`toBytes() -> Integer`

### METH-memoryunit-toGiga
`toGiga() -> Integer`

### METH-memoryunit-toKilo
`toKilo() -> Integer`

### METH-memoryunit-toMega
`toMega() -> Integer`

### METH-memoryunit-toUnit
`toUnit( unit: String ) -> Integer`

### METH-path-append
`append( text: String )`

### METH-path-baseName
`baseName: String`

### METH-path-copyTo
`copyTo( target: Path )`

### METH-path-countFasta
`countFasta() -> Integer`

### METH-path-countFastq
`countFastq() -> Integer`

### METH-path-countJson
`countJson() -> Integer`

### METH-path-countLines
`countLines() -> Integer`

### METH-path-delete
`delete() -> Boolean`

### METH-path-deleteDir
`deleteDir() -> Boolean`

### METH-path-divide
`/ : (Path, String) -> Path`

### METH-path-eachFile
`eachFile( action: (Path) -> () )`

### METH-path-eachFileRecurse
`eachFileRecurse( action: (Path) -> () )`

### METH-path-exists
`exists() -> Boolean`

### METH-path-extension
`extension: String`

### METH-path-getPermissions
`getPermissions() -> String`

### METH-path-getText
`getText() -> String`

### METH-path-isDirectory
`isDirectory() -> Boolean`

### METH-path-isEmpty
`isEmpty() -> Boolean`

### METH-path-isFile
`isFile() -> Boolean`

### METH-path-isHidden
`isHidden() -> Boolean`

### METH-path-isLink
`isLink() -> Boolean`

### METH-path-lastModified
`lastModified() -> Integer`

### METH-path-leftShift
`<< : (Path, String)`

### METH-path-listDirectory
`listDirectory() -> Iterable<Path>`

### METH-path-listFiles
`listFiles() -> Iterable<Path>`

### METH-path-mkdir
`mkdir() -> Boolean`

### METH-path-mkdirs
`mkdirs() -> Boolean`

### METH-path-mklink
`mklink( linkName: String, [options] ) -> Path`

### METH-path-mklink-hard
`mklink.hard: Boolean`

### METH-path-mklink-overwrite
`mklink.overwrite: Boolean`

### METH-path-moveTo
`moveTo( target: Path )`

### METH-path-name
`name: String`

### METH-path-parent
`parent: Path`

### METH-path-relativize
`relativize(other: Path) -> Path`

### METH-path-renameTo
`renameTo( target: String ) -> Boolean`

### METH-path-resolve
`resolve(other: String) -> Path`

### METH-path-resolveSibling
`resolveSibling(other: String) -> Path`

### METH-path-scheme
`scheme: String`

### METH-path-setPermissions
`setPermissions( permissions: String ) -> Boolean`

### METH-path-setPermissions-3
`setPermissions( owner: Integer, group: Integer, other: Integer ) -> Boolean`

### METH-path-setText
`setText( text: String )`

### METH-path-simpleName
`simpleName: String`

### METH-path-size
`size() -> Integer`

### METH-path-splitCsv
`splitCsv() -> List<?>`

### METH-path-splitFasta
`splitFasta() -> List<?>`

### METH-path-splitFastq
`splitFastq() -> List<?>`

### METH-path-splitJson
`splitJson() -> List<?>`

### METH-path-splitText
`splitText() -> List<String>`

### METH-path-toUriString
`toUriString() -> String`

### METH-path-withReader
`withReader( action: (BufferedReader) -> () )`

### METH-path-write
`write( text: String )`

### METH-record-plus
`+ : (Record, Record) -> Record`

### METH-set-minus
`- : (Set<E>, Iterable<E>) -> Set<E>`

### METH-set-plus
`+ : (Set<E>, Iterable<E>) -> Set<E>`

### METH-string-bitwiseNegate
`~ : (String) -> Pattern`

### METH-string-find
`=~ : (String, String) -> Matcher`

### METH-string-getAt
`[] : (String, Integer) -> char`

### METH-string-indexOf
`indexOf( str: String ) -> Integer`

### METH-string-indexOf-2
`indexOf( str: String, fromIndex: Integer ) -> Integer`

### METH-string-isBlank
`isBlank() -> Boolean`

### METH-string-isEmpty
`isEmpty() -> Boolean`

### METH-string-isFloat
`isFloat() -> Boolean`

### METH-string-lastIndexOf
`lastIndexOf( str: String ) -> Integer`

### METH-string-lastIndexOf-2
`lastIndexOf( str: String, fromIndex: Integer ) -> Integer`

### METH-string-length
`length() -> Integer`

### METH-string-match
`==~ : (String, String) -> Boolean`

### METH-string-md5
`md5() -> String`

### METH-string-multiply
`* : (String, Integer) -> String`

### METH-string-plus
`+ : (String, String) -> String`

### METH-string-sha256
`sha256() -> String`

### METH-string-strip
`strip() -> String`

### METH-string-stripLeading
`stripLeading() -> String`

### METH-string-stripTrailing
`stripTrailing() -> String`

### METH-string-toFloat
`toFloat() -> Float`

### METH-value-flatMap
`flatMap( transform: (V) -> Iterable<R> ) -> Channel<R>`

### METH-value-map
`map( transform: (V) -> R ) -> Value<R>`

### METH-value-subscribe
`subscribe( action: (V) -> () )`

### METH-value-view
`view() -> Value<V>`

### METH-value-view-1
`view( transform: (V) -> String ) -> Value<V>`

### METH-versionnumber-getMajor
`getMajor() -> String`

### METH-versionnumber-getMinor
`getMinor() -> String`

### METH-versionnumber-getPatch
`getPatch() -> String`

### METH-versionnumber-matches
`matches( condition: String ) -> Boolean`

## Syntax Basics
Source: https://nextflow.io/docs/latest/reference/syntax.html

### SYN-function-call
`Function call`

`parseMethodCallExprTokens` in `nextflowdsl/parse.go` requires `receiver.method(...)` or `receiver.method { ... }`; bare `foo()` calls are not parsed into a callable form, and `EvalExpr` in `nextflowdsl/groovy.go` has no top-level function invocation case.

## Standard Library Types
Source: https://nextflow.io/docs/latest/reference/stdlib-types.html

### TYPE-bag
`Bag<E>`

`evalNewExpr`, `evalCastExpr`, and `matchesGroovyType` have no `Bag` handling; adding a bag type would require new constructor/type/method support rather than using an existing evaluator type.

### TYPE-channel
`Channel<E>`

there is no dedicated `Channel` support in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`; `evalMethodCallExpr` only dispatches concrete receivers like `[]any`, maps, strings, and numbers.

### TYPE-duration
`Duration`

no `Duration` constructor, cast, static helper, or type check exists in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`.

### TYPE-filesystem-attributes
`Filesystem attributes`

`resolvePropertyPath` and `lookupVariablePart` only resolve map-like/object-like lookups, and there is no implementation for path attributes such as file name, extension, or size.

### TYPE-filesystem-operations
`Filesystem operations`

there is no evaluator support for path operations such as existence checks, deletion, directory creation, or listing; `evalStringMethodCall` contains no such methods.

### TYPE-memoryunit
`MemoryUnit`

no `MemoryUnit` handling appears in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, or `matchesGroovyType`.

### TYPE-record
`Record`

`Record` is not implemented in `evalNewExpr`, `evalCastExpr`, `matchesGroovyType`, or receiver dispatch.

### TYPE-splitting-files
`Splitting files`

the evaluator only implements plain string splitting (`split`, `tokenize`) in `evalStringMethodCall`; there are no file-based `split*` helpers.

### TYPE-value
`Value<V>`

there is no dedicated `Value` type support in `evalNewExpr`, `evalCastExpr`, `evalStaticMethodCall`, `matchesGroovyType`, or receiver dispatch.

### TYPE-versionnumber
`VersionNumber`

no `VersionNumber` constructor, cast, static helper, or type check exists in the cited evaluator implementation.

### TYPE-writing
`Writing`

there are no write or append operations for path/string receivers in `evalStringMethodCall` or elsewhere in the cited evaluator code.
