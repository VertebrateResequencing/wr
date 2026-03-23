# Test: Configuration

**Spec files:** nf-4000-config.md
**Impl files:** impl-07-config.md

## Task

For each feature ID in nf-4000-config.md, determine its classification.

### Checklist

1. Read nf-4000-config.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CFG-unscoped-options, CFG-apptainer, CFG-aws, CFG-azure, CFG-charliecloud
  CFG-conda, CFG-dag, CFG-docker, CFG-env, CFG-executor
  CFG-executor-specific-defaults, CFG-executor-specific-configuration, CFG-fusion, CFG-google, CFG-k8s
  CFG-lineage, CFG-mail, CFG-manifest, CFG-nextflow, CFG-notification
  CFG-podman, CFG-report, CFG-sarus, CFG-seqera, CFG-shifter
  CFG-singularity, CFG-spack, CFG-timeline, CFG-tower, CFG-trace
  CFG-wave, CFG-workflow

### Output format

```
CFG-unscoped-options: SUPPORTED | reason
...
```
