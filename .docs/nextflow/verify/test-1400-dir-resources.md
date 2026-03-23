# Test: Directives — Resources

**Spec files:** nf-1400-dir-resources.md
**Impl files:** impl-05-translate-directives.md, impl-07-config.md

## Task

For each feature ID in nf-1400-dir-resources.md, determine its classification.

### Checklist

1. Check buildRequirements (translate.go line 1813) for resource resolution
2. Check ProcessDefaults struct (config.go) for config support
3. Check parseProcessAssignment (config.go) for config parsing
4. Classify per 00-instructions.md criteria

### Features to classify

- DIR-cpus, DIR-memory, DIR-time, DIR-disk

### Output format

```
DIR-cpus: SUPPORTED | reason
...
```
