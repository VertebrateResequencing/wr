# Test: Includes & Parameters

**Spec files:** nf-1000-includes-params.md
**Impl files:** impl-10-modules-params.md, impl-01-parse.md

## Task

For each feature ID in nf-1000-includes-params.md, determine its classification.

### Checklist

1. Read nf-1000-includes-params.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- INCL-include, INCL-params-block, INCL-parameter-legacy

### Output format

```
INCL-include: SUPPORTED | reason
...
```

## Results

- INCL-include: SUPPORTED — `parseWorkflow` in `parse.go` accepts top-level `include` statements, and `loadWorkflowFile` plus `selectImportedDefinitions` in `module_load.go` resolve module paths, recursively load imported workflows, and merge the selected processes/subworkflows into the resulting workflow.
- INCL-params-block: GAP — `parseTopLevelParamsBlock` in `parse.go` parses `params { ... }` entries into `Workflow.ParamBlock`, and `module_load.go` preserves that block across imports, but the cited runtime params code in `params.go` only loads external JSON/YAML params and substitutes `params.*` references; it does not apply `ParamBlock` defaults/types to the runtime params map.
- INCL-parameter-legacy: GAP — `parseTopLevelParamAssignment` in `parse.go` parses legacy `params.foo = ...` assignments into `Workflow.ParamBlock`, but as with the params block feature there is no cited code path in `params.go` that consumes those declarations when building runtime parameters, so the legacy defaults are parsed and stored rather than taking Nextflow-equivalent effect.
