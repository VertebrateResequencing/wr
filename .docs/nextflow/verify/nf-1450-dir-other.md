# Directives — Other

**Source:** https://nextflow.io/docs/latest/reference/process.html#directives

## Features

### DIR-cache
`cache` — control caching/resume behaviour.
Values: `true` (default), `false`, `'deep'`, `'lenient'`.
```groovy
cache false       // disable caching
cache 'deep'      // hash file contents not metadata
cache 'lenient'   // name+size only (workaround for NFS timestamp issues)
```

### DIR-fair
`fair` — guarantee outputs are emitted in input order.
```groovy
fair true
```

### DIR-ext
`ext` — namespace for user-defined custom directive values.
```groovy
ext version: '2.5.3', args: '--alpha --beta'
```
Accessible in script as `task.ext.version`, `task.ext.args`.
Commonly configured per-process via config selectors.

### DIR-scratch
`scratch` — execute in a local temporary directory.
```groovy
scratch true                  // use $TMPDIR or mktemp
scratch '$MY_SCRATCH'         // use specific env var
scratch '/local/tmp'          // use specific path
scratch 'ram-disk'            // use /dev/shm
```
Only declared outputs are copied back to working directory.

### DIR-storeDir
`storeDir` — permanent cache directory for process outputs.
```groovy
storeDir '/db/genomes'
```
Process is skipped if outputs already exist in storeDir. Completed outputs
are moved to storeDir.

### DIR-stageInMode
`stageInMode` — how input files are staged into task directory.
Values: `'symlink'` (default), `'rellink'`, `'link'`, `'copy'`.

### DIR-stageOutMode
`stageOutMode` — how output files are staged from scratch to work dir.
Values: `'copy'`, `'move'`, `'rsync'`, `'fcp'`, `'rclone'`.

### DIR-containerOptions
`containerOptions` — extra options passed to the container engine.
```groovy
containerOptions '--volume /data/db:/db'
```

### DIR-secret
`secret` — make a named secret available as env var.
```groovy
secret 'MY_ACCESS_KEY'
```
Only for local/grid executors. Must escape in script: `\$MY_ACCESS_KEY`.

### DIR-penv
`penv` — parallel environment for SGE executor.
```groovy
penv 'smp'
```

### DIR-pod
`pod` — Kubernetes pod settings (env vars, secrets, config maps, volumes, etc.).
```groovy
pod env: 'MESSAGE', value: 'hello'
```
Many sub-options: affinity, annotation, automountServiceAccountToken, config,
csi, emptyDir, env (value/config/secret/fieldPath), hostPath, imagePullPolicy,
imagePullSecret, label, nodeSelector, priorityClassName, privileged, runAsUser,
runtimeClassName, schedulerName, secret, securityContext, toleration,
ttlSecondsAfterFinished, volumeClaim.

### DIR-array
`array` — submit tasks as job arrays. Experimental.
```groovy
array 100
```

### DIR-machineType
`machineType` — Google Cloud / Azure machine type.
```groovy
machineType 'n1-highmem-8'
```

### DIR-maxSubmitAwait
`maxSubmitAwait` — max time a task can wait in queue before failing.
```groovy
maxSubmitAwait 10.m
```

### DIR-resourceLabels
`resourceLabels` — custom labels applied to computing resources.
```groovy
resourceLabels region: 'us-east-1', user: 'jdoe'
```
For AWS Batch, Azure Batch, Google Batch, Kubernetes.

### DIR-resourceLimits
`resourceLimits` — cap resource requests to environment limits.
```groovy
resourceLimits cpus: 24, memory: 768.GB, time: 72.h
```
Limits apply to cpus, disk, memory, time.
