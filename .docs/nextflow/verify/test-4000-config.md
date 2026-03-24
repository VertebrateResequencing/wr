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

## Results

- CFG-unscoped-options: PARSE_ERROR — `configParser.parse` only accepts named top-level scopes (`apptainer`, `docker`, `singularity`, `env`, `executor`, `params`, `process`, `profiles`) and otherwise returns `unsupported config section`.
- CFG-apptainer: GAP — `parseContainerScope` supports only `enabled = <bool>` and, when true, only records `Config.ContainerEngine = "apptainer"`; other apptainer settings are rejected.
- CFG-aws: PARSE_ERROR — top-level `aws` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-azure: PARSE_ERROR — top-level `azure` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-charliecloud: PARSE_ERROR — `configParser.parse` does not dispatch `charliecloud`, so the block fails as an unsupported top-level config section.
- CFG-conda: GAP — `skippedTopLevelConfigScopes` includes `conda`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-dag: GAP — `skippedTopLevelConfigScopes` includes `dag`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-docker: GAP — `parseContainerScope` supports only `enabled = <bool>` and, when true, only records `Config.ContainerEngine = "docker"`; other docker settings are rejected.
- CFG-env: GAP — `parseTopLevelEnvBlock` / `parseEnvBlock` only parse a string map into `Config.Env`; in the inspected code this is stored but no Nextflow-equivalent runtime behaviour is implemented here.
- CFG-executor: GAP — `parseExecutorBlock` parses flat key/value settings into `Config.Executor`, but the inspected code only stores them and does not implement Nextflow executor semantics here.
- CFG-executor-specific-defaults: PARSE_ERROR — `parseExecutorBlock` only accepts flat assignments inside `executor { ... }`; executor-specific nested blocks/default sections are not parsed.
- CFG-executor-specific-configuration: PARSE_ERROR — `parseExecutorBlock` only accepts flat assignments inside `executor { ... }`; executor-specific nested blocks/default sections are not parsed.
- CFG-fusion: PARSE_ERROR — top-level `fusion` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-google: PARSE_ERROR — top-level `google` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-k8s: PARSE_ERROR — top-level `k8s` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-lineage: PARSE_ERROR — top-level `lineage` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-mail: PARSE_ERROR — top-level `mail` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-manifest: GAP — `skippedTopLevelConfigScopes` includes `manifest`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-nextflow: PARSE_ERROR — top-level `nextflow` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-notification: GAP — `skippedTopLevelConfigScopes` includes `notification`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-podman: PARSE_ERROR — `configParser.parse` does not dispatch `podman`, so the block fails as an unsupported top-level config section.
- CFG-report: GAP — `skippedTopLevelConfigScopes` includes `report`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-sarus: PARSE_ERROR — `configParser.parse` does not dispatch `sarus`, so the block fails as an unsupported top-level config section.
- CFG-seqera: PARSE_ERROR — top-level `seqera` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
- CFG-shifter: PARSE_ERROR — `configParser.parse` does not dispatch `shifter`, so the block fails as an unsupported top-level config section.
- CFG-singularity: GAP — `parseContainerScope` supports only `enabled = <bool>` and, when true, only records `Config.ContainerEngine = "singularity"`; other singularity settings are rejected.
- CFG-spack: PARSE_ERROR — top-level `spack` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`; only process-level `spack` assignments are parsed by `parseProcessAssignment`.
- CFG-timeline: GAP — `skippedTopLevelConfigScopes` includes `timeline`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-tower: GAP — `skippedTopLevelConfigScopes` includes `tower`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-trace: GAP — `skippedTopLevelConfigScopes` includes `trace`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-wave: GAP — `skippedTopLevelConfigScopes` includes `wave`, and `skipUnknownTopLevelConfigScope` skips the whole block with a warning, so it parses but is ignored.
- CFG-workflow: PARSE_ERROR — top-level `workflow` is neither handled by `configParser.parse` nor listed in `skippedTopLevelConfigScopes`.
