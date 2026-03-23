# Directives — Environment

**Source:** https://nextflow.io/docs/latest/reference/process.html#directives

## Features

### DIR-module
`module` — load Environment Modules before task execution.
```groovy
module 'ncbi-blast/2.2.27'
module 'ncbi-blast/2.2.27:t_coffee/10.0'  // multiple, colon-separated
```
Can be specified multiple times. Generates `module load X` lines.

### DIR-conda
`conda` — install/activate Conda environment.
```groovy
conda 'bwa=0.7.15'
conda 'bioconda::bwa=0.7.15 samtools=1.15'
conda '/path/to/environment.yml'
conda '/path/to/existing/env'
```

### DIR-spack
`spack` — install/activate Spack environment.
```groovy
spack 'bwa@0.7.15'
spack 'bwa@0.7.15 samtools@1.15'
```

### DIR-env
`env` — set environment variables for the task.
```groovy
env FOO: 'bar', BAZ: 'qux'
```

### DIR-beforeScript
`beforeScript` — Bash snippet run BEFORE the main script.
```groovy
beforeScript 'source /cluster/bin/setup'
```
Runs outside container for non-native executors.

### DIR-afterScript
`afterScript` — Bash snippet run AFTER the main script.
```groovy
afterScript 'cleanup /tmp/work'
```
Always runs in host environment (outside container).

### DIR-shell
`shell` directive — custom shell command for process scripts.
```groovy
shell '/bin/bash', '-euo', 'pipefail'
```
Default: `/bin/bash -ue`.
