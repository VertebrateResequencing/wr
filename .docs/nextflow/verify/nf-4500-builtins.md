# Built-in Variables and Global Functions

**Source:** https://nextflow.io/docs/latest/reference/stdlib.html,
https://nextflow.io/docs/latest/script.html

## Built-in Variables

### BV-baseDir
`baseDir` — alias for `projectDir`.

### BV-projectDir
`projectDir` — directory of the main script.

### BV-launchDir
`launchDir` — directory where `nextflow run` was invoked.

### BV-moduleDir
`moduleDir` — directory of the current module script.

### BV-workDir
`workDir` — pipeline work directory (default: `./work`).

## Workflow Object

### BV-workflow-projectDir
`workflow.projectDir` — same as `projectDir`.

### BV-workflow-launchDir
`workflow.launchDir` — same as `launchDir`.

### BV-workflow-workDir
`workflow.workDir` — same as `workDir`.

### BV-workflow-profile
`workflow.profile` — active profile name(s).

### BV-workflow-configFiles
`workflow.configFiles` — list of config files used.

### BV-workflow-runName
`workflow.runName` — unique run name.

### BV-workflow-sessionId
`workflow.sessionId` — unique session ID.

### BV-workflow-resume
`workflow.resume` — true if pipeline was resumed.

### BV-workflow-revision
`workflow.revision` — pipeline Git revision.

### BV-workflow-commitId
`workflow.commitId` — pipeline Git commit ID.

### BV-workflow-repository
`workflow.repository` — pipeline Git URL.

### BV-workflow-scriptName
`workflow.scriptName` — main script filename.

### BV-workflow-scriptFile
`workflow.scriptFile` — main script absolute path.

### BV-workflow-start
`workflow.start` — pipeline start timestamp.

### BV-workflow-complete
`workflow.complete` — pipeline completion timestamp (onComplete only).

### BV-workflow-success
`workflow.success` — true if pipeline completed without errors (onComplete only).

### BV-workflow-failOnIgnore
`workflow.failOnIgnore` — config setting for fail-on-ignore.

## Nextflow Object

### BV-nextflow-version
`nextflow.version` — Nextflow version string.

### BV-nextflow-build
`nextflow.build` — build number.

### BV-nextflow-timestamp
`nextflow.timestamp` — build timestamp.

## Log Namespace

### BV-log-info
`log.info(message)` — log informational message.

### BV-log-warn
`log.warn(message)` — log warning message.

### BV-log-error
`log.error(message)` — log error message.

## Global Functions

### GF-file
`file(path)` — create a Path object. `files(pattern)` — list matching files.

### GF-groupKey
`groupKey(key, size)` — create group key for `groupTuple` early emission.

### GF-branchCriteria
`branchCriteria { ... }` — create reusable `branch` criteria.

### GF-multiMapCriteria
`multiMapCriteria { ... }` — create reusable `multiMap` criteria.

### GF-error
`error(message)` — throw an error and abort the pipeline.

### GF-exit
`exit(code)` — **deprecated** alias for `error`.

### GF-println
`print(value)` / `println(value)` / `printf(format, args...)`.

### GF-tuple
`tuple(a, b, ...)` — create a tuple (list).

### GF-env
`env(name)` — read environment variable.

### GF-sleep
`sleep(milliseconds)` — pause execution.

### GF-sendMail
`sendMail(to: ..., subject: ..., body: ...)` — send email notification.
