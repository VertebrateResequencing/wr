# Directives — Scheduler

**Source:** https://nextflow.io/docs/latest/reference/process.html#directives

## Features

### DIR-queue
`queue` — scheduler queue/partition name.
```groovy
queue 'long'
queue 'short,long,cn-el6'   // some executors support comma-separated
```

### DIR-clusterOptions
`clusterOptions` — native scheduler options passed verbatim.
```groovy
clusterOptions '-x 1 -y 2'
clusterOptions '-x 1', '-y 2', '--flag'   // list form
```

### DIR-accelerator
`accelerator` — request hardware accelerators (GPUs).
```groovy
accelerator 4, type: 'nvidia-tesla-k80'
accelerator request: 4
```
Options: `request:` (count), `type:` (accelerator type string).

### DIR-arch
`arch` — CPU architecture for Spack/Wave builds.
```groovy
arch 'linux/x86_64', target: 'cascadelake'
```
Allowed families: x86_64/amd64, aarch64/arm64, arm64/v7.
`target:` specifies microarchitecture (cascadelake, icelake, zen2, zen3, etc.).

### DIR-label
`label` — annotate process with mnemonic identifier(s).
```groovy
label 'big_mem'
label 'gpu'
```
Multiple labels allowed. Used for config process selectors.
Must match `[a-zA-Z][a-zA-Z0-9_]*[a-zA-Z0-9]`.

### DIR-tag
`tag` — custom label for log/trace display.
```groovy
tag "$sample_id"
```
Can use dynamic expressions.
