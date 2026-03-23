# Test: Directives — Scheduler

**Spec files:** nf-1440-dir-scheduler.md
**Impl files:** impl-05-translate-directives.md, impl-07-config.md

## Task

For each feature ID in nf-1440-dir-scheduler.md, determine its classification.

### Checklist

1. Check buildRequirements (translate.go) for queue/clusterOptions
2. Check resolveAcceleratorOptions/resolveArchOptions for GPU/arch
3. Check ProcessDefaults for config fields
4. Classify per 00-instructions.md criteria

### Features to classify

- DIR-queue, DIR-clusterOptions
- DIR-accelerator, DIR-accelerator-count, DIR-accelerator-type
- DIR-arch, DIR-arch-name, DIR-arch-target
- DIR-label, DIR-tag

### Output format

```
DIR-queue: SUPPORTED | reason
...
```
