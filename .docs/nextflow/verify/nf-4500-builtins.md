# Built-in Variables, Functions and Namespaces

**Source:** https://nextflow.io/docs/latest/reference/stdlib-namespaces.html

## Features

### BV-baseDir
`baseDir: Path` — TODO: describe expected behaviour.

### BV-launchDir
`launchDir: Path` — TODO: describe expected behaviour.

### BV-moduleDir
`moduleDir: Path` — TODO: describe expected behaviour.

### BV-params
`params` — TODO: describe expected behaviour.

### BV-projectDir
`projectDir: Path` — TODO: describe expected behaviour.

### BV-secrets
`secrets: Map<String,String>` — TODO: describe expected behaviour.

### BV-workDir
`workDir: Path` — TODO: describe expected behaviour.

### BV-branchCriteria
`branchCriteria( criteria: Closure ) -> Closure` — TODO: describe expected behaviour.

### BV-env
`env( name: String ) -> String` — TODO: describe expected behaviour.

### BV-error
`error( message: String = null )` — TODO: describe expected behaviour.

### BV-exit
`exit( exitCode: int = 0, message: String = null )` — TODO: describe expected behaviour.

### BV-file
`file( filePattern: String, [options] ) -> Path` — TODO: describe expected behaviour.

### BV-file-checkIfExists
`file.checkIfExists: boolean` — TODO: describe expected behaviour.

### BV-file-followLinks
`file.followLinks: boolean` — TODO: describe expected behaviour.

### BV-file-glob
`file.glob: boolean` — TODO: describe expected behaviour.

### BV-file-hidden
`file.hidden: boolean` — TODO: describe expected behaviour.

### BV-file-maxDepth
`file.maxDepth: int` — TODO: describe expected behaviour.

### BV-file-type
`file.type: String` — TODO: describe expected behaviour.

### BV-files
`files( filePattern: String, [options] ) -> Iterable<Path>` — TODO: describe expected behaviour.

### BV-groupKey
`groupKey( key, size: int ) -> GroupKey` — TODO: describe expected behaviour.

### BV-multiMapCriteria
`multiMapCriteria( criteria: Closure ) -> Closure` — TODO: describe expected behaviour.

### BV-print
`print( value )` — TODO: describe expected behaviour.

### BV-printf
`printf( format: String, values... )` — TODO: describe expected behaviour.

### BV-println
`println( value )` — TODO: describe expected behaviour.

### BV-sendMail
`sendMail( [options] )` — TODO: describe expected behaviour.

### BV-sleep
`sleep( milliseconds: long )` — TODO: describe expected behaviour.

### BV-record
`record( [options] ) -> Record` — TODO: describe expected behaviour.

### BV-tuple
`tuple( args... ) -> Tuple` — TODO: describe expected behaviour.

### BV-log-error
`error( message: String )` — TODO: describe expected behaviour.

### BV-log-info
`info( message: String )` — TODO: describe expected behaviour.

### BV-log-warn
`warn( message: String )` — TODO: describe expected behaviour.

### BV-nextflow-build
`build: int` — TODO: describe expected behaviour.

### BV-nextflow-timestamp
`timestamp: String` — TODO: describe expected behaviour.

### BV-nextflow-version
`version: VersionNumber` — TODO: describe expected behaviour.

### BV-workflow-commandLine
`commandLine: String` — TODO: describe expected behaviour.

### BV-workflow-commitId
`commitId: String` — TODO: describe expected behaviour.

### BV-workflow-complete
`complete: OffsetDateTime` — TODO: describe expected behaviour.

### BV-workflow-configFiles
`configFiles: List<Path>` — TODO: describe expected behaviour.

### BV-workflow-container
`container: String | Map<String,String>` — TODO: describe expected behaviour.

### BV-workflow-containerEngine
`containerEngine: String` — TODO: describe expected behaviour.

### BV-workflow-duration
`duration: Duration` — TODO: describe expected behaviour.

### BV-workflow-errorMessage
`errorMessage: String` — TODO: describe expected behaviour.

### BV-workflow-errorReport
`errorReport: String` — TODO: describe expected behaviour.

### BV-workflow-exitStatus
`exitStatus: int` — TODO: describe expected behaviour.

### BV-workflow-failOnIgnore
`failOnIgnore: boolean` — TODO: describe expected behaviour.

### BV-workflow-fusion
`fusion` — TODO: describe expected behaviour.

### BV-workflow-fusion-enabled
`fusion.enabled: boolean` — TODO: describe expected behaviour.

### BV-workflow-fusion-version
`fusion.version: String` — TODO: describe expected behaviour.

### BV-workflow-homeDir
`homeDir: Path` — TODO: describe expected behaviour.

### BV-workflow-launchDir
`launchDir: Path` — TODO: describe expected behaviour.

### BV-workflow-manifest
`manifest` — TODO: describe expected behaviour.

### BV-workflow-outputDir
`outputDir: Path` — TODO: describe expected behaviour.

### BV-workflow-preview
`preview: boolean` — TODO: describe expected behaviour.

### BV-workflow-profile
`profile: String` — TODO: describe expected behaviour.

### BV-workflow-projectDir
`projectDir: Path` — TODO: describe expected behaviour.

### BV-workflow-repository
`repository: String` — TODO: describe expected behaviour.

### BV-workflow-resume
`resume: boolean` — TODO: describe expected behaviour.

### BV-workflow-revision
`revision: String` — TODO: describe expected behaviour.

### BV-workflow-runName
`runName: String` — TODO: describe expected behaviour.

### BV-workflow-scriptFile
`scriptFile: Path` — TODO: describe expected behaviour.

### BV-workflow-scriptId
`scriptId: String` — TODO: describe expected behaviour.

### BV-workflow-scriptName
`scriptName: String` — TODO: describe expected behaviour.

### BV-workflow-sessionId
`sessionId: UUID` — TODO: describe expected behaviour.

### BV-workflow-start
`start: OffsetDateTime` — TODO: describe expected behaviour.

### BV-workflow-stubRun
`stubRun: boolean` — TODO: describe expected behaviour.

### BV-workflow-success
`success: boolean` — TODO: describe expected behaviour.

### BV-workflow-userName
`userName: String` — TODO: describe expected behaviour.

### BV-workflow-wave
`wave` — TODO: describe expected behaviour.

### BV-workflow-wave-enabled
`wave.enabled: boolean` — TODO: describe expected behaviour.

### BV-workflow-workDir
`workDir: Path` — TODO: describe expected behaviour.

### BV-workflow-onComplete
`onComplete( action: Closure )` — TODO: describe expected behaviour.

### BV-workflow-onError
`onError( action: Closure )` — TODO: describe expected behaviour.
