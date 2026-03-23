# Test: Directives — Environment

**Spec files:** nf-1420-dir-environment.md
**Impl files:** impl-05-translate-directives.md, impl-06-translate-command.md, impl-07-config.md

## Task

For each feature ID in nf-1420-dir-environment.md, determine its classification.

### Checklist

1. Check moduleLoadLines (translate.go line 2986) for module
2. Check applyEnv (line 2502) for env
3. Check buildCommandWithValues (line 2537) for beforeScript/afterScript
4. Check resolveShellDirective (line 1678) for shell
5. Classify per 00-instructions.md criteria

### Features to classify

- DIR-module, DIR-conda, DIR-spack, DIR-env,
  DIR-beforeScript, DIR-afterScript, DIR-shell

### Output format

```
DIR-module: SUPPORTED | reason
...
```
