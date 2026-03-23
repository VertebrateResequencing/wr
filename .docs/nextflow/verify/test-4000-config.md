# Test: Configuration

**Spec files:** nf-4000-config.md
**Impl files:** impl-07-config.md

## Task

For each feature ID in nf-4000-config.md, determine its classification.

### Checklist

1. Check config.go parse() top-level scope handling
2. Check skippedTopLevelConfigScopes map
3. Check parseContainerScope, parseProcessBlock, parseProfilesBlock, etc.
4. Classify per 00-instructions.md criteria

### Features to classify

- CFG-params, CFG-process, CFG-env, CFG-executor, CFG-profiles, CFG-selectors
- CFG-docker, CFG-singularity, CFG-apptainer, CFG-charliecloud,
  CFG-podman, CFG-sarus, CFG-shifter
- CFG-conda, CFG-dag, CFG-manifest, CFG-notification, CFG-report,
  CFG-timeline, CFG-tower, CFG-trace, CFG-wave, CFG-weblog
- CFG-aws, CFG-azure, CFG-google, CFG-k8s, CFG-mail, CFG-seqera,
  CFG-fusion, CFG-lineage, CFG-spack
- CFG-nextflow, CFG-workflow-scope, CFG-feature-flags

### Output format

```
CFG-params: SUPPORTED | reason
...
```
