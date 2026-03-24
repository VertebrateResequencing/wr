# Test: Feature Flags

**Spec files:** nf-4100-feature-flags.md
**Impl files:** impl-07-config.md

## Task

For each feature ID in nf-4100-feature-flags.md, determine its classification.

### Checklist

1. Read nf-4100-feature-flags.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- CFG-enable-configProcessNamesValidation, CFG-enable-dsl, CFG-enable-moduleBinaries, CFG-enable-strict, CFG-preview-output
  CFG-preview-recursion, CFG-preview-topic, CFG-preview-types

### Output format

```
CFG-enable-configProcessNamesValidation: SUPPORTED | reason
...
```

## Results

- CFG-enable-configProcessNamesValidation: PARSE_ERROR — `configParser.parse` in `nextflowdsl/config.go` only accepts top-level scopes `apptainer`, `docker`, `singularity`, `env`, `executor`, `params`, `process`, and `profiles`; a top-level `nextflow` scope falls through to `unsupported config section`. `skipUnknownTopLevelConfigScope` also does not allowlist `nextflow`.
- CFG-enable-dsl: PARSE_ERROR — `configParser.parse` in `nextflowdsl/config.go` does not handle a top-level `nextflow` scope, so `nextflow.enable.dsl` cannot be parsed. `skipUnknownTopLevelConfigScope` only skips a fixed set of unrelated scopes and excludes `nextflow`.
- CFG-enable-moduleBinaries: PARSE_ERROR — `configParser.parse` in `nextflowdsl/config.go` rejects unsupported top-level section `nextflow`, so `nextflow.enable.moduleBinaries` cannot parse.
- CFG-enable-strict: PARSE_ERROR — `configParser.parse` in `nextflowdsl/config.go` has no `nextflow` branch and returns `unsupported config section` for that scope; `skipUnknownTopLevelConfigScope` does not skip it.
- CFG-preview-output: PARSE_ERROR — `nextflow.preview.output` requires a top-level `nextflow` scope, but `configParser.parse` in `nextflowdsl/config.go` rejects that scope and `skippedTopLevelConfigScopes` does not include it.
- CFG-preview-recursion: PARSE_ERROR — same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, so `nextflow.preview.recursion` cannot be read.
- CFG-preview-topic: PARSE_ERROR — same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, and `skipUnknownTopLevelConfigScope` only skips the allowlisted scopes in `skippedTopLevelConfigScopes`.
- CFG-preview-types: PARSE_ERROR — same parser limitation as above: `configParser.parse` rejects top-level `nextflow`, so `nextflow.preview.types` cannot parse.
