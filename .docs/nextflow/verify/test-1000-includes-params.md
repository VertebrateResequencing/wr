# Test: Includes & Params

**Spec files:** nf-1000-includes-params.md
**Impl files:** impl-01-parse.md, impl-10-modules-params.md

## Task

For each feature ID in nf-1000-includes-params.md, determine its classification.

### Checklist

1. Check parseWorkflow (parse.go) for include/params parsing
2. Check module_load.go for include resolution
3. Check params.go for param loading/merging
4. Classify per 00-instructions.md criteria

### Features to classify

- INCL-include, INCL-selective, INCL-alias, INCL-multiple,
  INCL-relative-path, INCL-plugin, INCL-remote, INCL-addParams
- PARAM-block, PARAM-typed-block, PARAM-dot-assign, PARAM-cli-override,
  PARAM-file-json, PARAM-file-yaml, PARAM-nested
- SYN-enum, SYN-record

### Output format

```
INCL-include: SUPPORTED | reason
...
```
