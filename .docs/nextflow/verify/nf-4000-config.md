# Configuration Scopes

**Source:** https://nextflow.io/docs/latest/reference/config.html

## Core Scopes

### CFG-unscoped
Unscoped options (top-level, outside any scope):
- `bucketDir` — remote work directory for hybrid workflows
- `cleanup` — delete work files on success (default: `false`)
- `outputDir` — pipeline output directory
- `resume` — enable cached task re-use
- `workDir` — pipeline work directory

### CFG-params
`params` scope — pipeline parameters.
```groovy
params {
    input = '/data/reads.fq'
    threads = 4
}
```

### CFG-process
`process` scope — default directives for all processes.
```groovy
process {
    cpus = 2
    memory = '4 GB'
    withLabel: 'big_mem' { memory = '32 GB' }
}
```

### CFG-env
`env` scope — set environment variables for all tasks.
```groovy
env { PATH = '/custom/bin:$PATH' }
```

### CFG-executor
`executor` scope — executor-specific settings.
```groovy
executor {
    name = 'slurm'
    queueSize = 100
    submitRateLimit = '10 sec'
}
```

### CFG-profiles
`profiles` — named configuration profiles selectable with `-profile`.
```groovy
profiles {
    standard { ... }
    docker { docker.enabled = true }
}
```

### CFG-selectors
Process selectors in config:
- `withLabel: '<pattern>'` — match processes by label
- `withName: '<pattern>'` — match processes by name
Patterns support `*` and `?` wildcards, also `!` negation.

## Container Scopes

### CFG-docker
`docker` scope — Docker settings: `enabled`, `runOptions`, `temp`, etc.

### CFG-singularity
`singularity` scope — Singularity settings: `enabled`, `runOptions`, etc.

### CFG-apptainer
`apptainer` scope — Apptainer settings (fork of Singularity).

### CFG-charliecloud
`charliecloud` scope — Charliecloud settings.

### CFG-podman
`podman` scope — Podman settings.

### CFG-sarus
`sarus` scope — Sarus settings.

### CFG-shifter
`shifter` scope — Shifter settings.

## Skippable Scopes (typically irrelevant to wr translation)

### CFG-conda
`conda` scope — global Conda settings.

### CFG-dag
`dag` scope — DAG visualisation settings.

### CFG-manifest
`manifest` scope — pipeline metadata (name, version, description, etc.).

### CFG-notification
`notification` scope — email notifications on completion.

### CFG-report
`report` scope — HTML execution report settings.

### CFG-timeline
`timeline` scope — timeline report settings.

### CFG-tower
`tower` scope — Nextflow Tower/Seqera Platform integration.

### CFG-trace
`trace` scope — trace report settings (CSV of task metrics).

### CFG-wave
`wave` scope — Wave container service settings.

### CFG-weblog
`weblog` scope — webhook logging settings.

## Cloud/Platform Scopes

### CFG-aws
`aws` scope — AWS-specific settings (Batch, S3, etc.).

### CFG-azure
`azure` scope — Azure Batch settings.

### CFG-google
`google` scope — Google Cloud Batch/Life Sciences settings.

### CFG-k8s
`k8s` scope — Kubernetes settings.

### CFG-mail
`mail` scope — SMTP mail settings.

### CFG-seqera
`seqera` scope — Seqera Platform settings.

### CFG-fusion
`fusion` scope — Fusion file system settings.

### CFG-lineage
`lineage` scope — data lineage settings.

### CFG-spack
`spack` scope — global Spack settings.

## Special Scopes

### CFG-nextflow
`nextflow` scope — Nextflow engine settings.

### CFG-workflow-scope
`workflow` scope in config — workflow-level settings.

### CFG-feature-flags
Feature flags via `nextflow.enable.*` and `nextflow.preview.*`:
- `nextflow.enable.configProcessNamesValidation` — warn on unmatched withName selectors (deprecated)
- `nextflow.enable.dsl` — DSL version (deprecated)
- `nextflow.enable.moduleBinaries` — enable modules with binary scripts
- `nextflow.enable.strict` — strict mode (deprecated)
- `nextflow.preview.output` — workflow outputs (deprecated, now stable)
- `nextflow.preview.recursion` — process/workflow recursion
- `nextflow.preview.topic` — topic channels (deprecated, now stable)
- `nextflow.preview.types` — typed processes
