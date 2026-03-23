# Test: Directives — Other

**Spec files:** nf-1450-dir-other.md
**Impl files:** impl-05-translate-directives.md, impl-07-config.md

## Task

For each feature ID in nf-1450-dir-other.md, determine its classification.

### Checklist

1. Check applyProcessDefaults (translate.go) for each directive
2. Check resolveScratchDirective/wrapScratchCommand for scratch
3. Check ProcessDefaults struct for config support
4. Classify per 00-instructions.md criteria

### Features to classify

- DIR-cache, DIR-fair, DIR-ext, DIR-scratch, DIR-storeDir,
  DIR-stageInMode, DIR-stageOutMode, DIR-containerOptions,
  DIR-secret, DIR-penv, DIR-pod, DIR-array, DIR-machineType,
  DIR-maxSubmitAwait, DIR-resourceLabels, DIR-resourceLimits

### Output format

```
DIR-cache: SUPPORTED | reason
...
```
