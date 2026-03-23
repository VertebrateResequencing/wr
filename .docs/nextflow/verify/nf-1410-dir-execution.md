# Directives — Execution

**Source:** https://nextflow.io/docs/latest/reference/process.html#directives

## Features

### DIR-container
`container` — Docker/Singularity container image for the task.
```groovy
container 'ubuntu:22.04'
```
Requires container runtime. Ignored for `exec:` (native) processes.

### DIR-errorStrategy
`errorStrategy` — how to handle task failure.
Values: `'terminate'` (default), `'finish'`, `'ignore'`, `'retry'`.
```groovy
errorStrategy 'retry'
```
- `terminate`: stop immediately, kill pending
- `finish`: wait for running tasks, then stop
- `ignore`: skip failed task, continue pipeline
- `retry`: re-submit the failed task

Can be dynamic: `errorStrategy { task.exitStatus == 137 ? 'retry' : 'terminate' }`.

### DIR-maxRetries
`maxRetries` — max times a single task instance can be re-submitted on failure.
Default: 1 when `errorStrategy 'retry'`.
```groovy
maxRetries 3
```

### DIR-maxForks
`maxForks` — max concurrent instances of this process.
```groovy
maxForks 1  // sequential execution
```
Default: number of CPUs minus 1.

### DIR-maxErrors
`maxErrors` — max total errors across ALL instances of this process before
stopping retries. Different from `maxRetries` (per-instance).
```groovy
maxErrors 5
```

### DIR-executor
`executor` — which execution backend to use.
Values: `'local'`, `'slurm'`, `'lsf'`, `'sge'`, `'pbs'`, `'pbspro'`,
`'awsbatch'`, `'azurebatch'`, `'k8s'`, `'condor'`, `'moab'`, `'nqsii'`.
```groovy
executor 'slurm'
```
Overrides global `process.executor` config.
