# Test: Directives: Environment

**Spec files:** nf-1420-dir-environment.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1420-dir-environment.md, determine its classification.

### Checklist

1. Read nf-1420-dir-environment.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-afterScript, DIR-beforeScript, DIR-conda, DIR-container, DIR-containerOptions
  DIR-module, DIR-spack

### Output format

```
DIR-afterScript: SUPPORTED | reason
...
```

## Results

- DIR-afterScript: GAP — `buildCommandBody` in `nextflowdsl/translate.go` appends `proc.AfterScript` after the main script, so it runs, but only as part of the same command body; there is no separate outside-container execution matching Nextflow `afterScript` semantics.
- DIR-beforeScript: GAP — `buildCommandBody` in `nextflowdsl/translate.go` prepends `proc.BeforeScript` before the main script, but it is executed inside the same generated command body rather than with Nextflow's distinct pre-task/container-aware semantics.
- DIR-conda: GAP — `prependEnvironmentDirectives` in `nextflowdsl/translate.go` only prefixes `conda activate <value>`; it does not create or resolve Conda environments from package specs/files the way Nextflow does.
- DIR-container: SUPPORTED — `applyContainer` in `nextflowdsl/translate.go` resolves the `container` directive and assigns it to `job.WithDocker` or `job.WithSingularity`, so the task is configured to run with the selected container image.
- DIR-containerOptions: SUPPORTED — `applyContainer` in `nextflowdsl/translate.go` resolves `containerOptions` and prepends the raw options string to the container spec before setting `job.WithDocker`/`job.WithSingularity`, preserving runtime options for execution.
- DIR-module: SUPPORTED — `moduleLoadLines` and `buildCommandBody` in `nextflowdsl/translate.go` turn the directive into `module load ...` commands inserted before the task body, including colon-separated module lists.
- DIR-spack: GAP — `prependEnvironmentDirectives` in `nextflowdsl/translate.go` only prefixes `spack load <value>`; it does not implement Nextflow's broader Spack environment/package resolution behaviour.
