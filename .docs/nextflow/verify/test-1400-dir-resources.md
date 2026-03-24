# Test: Directives: Resources

**Spec files:** nf-1400-dir-resources.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1400-dir-resources.md, determine its classification.

### Checklist

1. Read nf-1400-dir-resources.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-accelerator, DIR-cpus, DIR-disk, DIR-machineType, DIR-memory
  DIR-resourceLabels, DIR-resourceLimits, DIR-time

### Output format

```
DIR-accelerator: SUPPORTED | reason
...
```

## Results

- DIR-accelerator: GAP — `resolveAcceleratorOptions` in `nextflowdsl/translate.go` parses the directive, but only applies it for `lsf` scheduling and treats accelerator `type` as informational only, so behaviour differs from Nextflow's broader resource semantics.
- DIR-cpus: SUPPORTED — `buildRequirements` in `nextflowdsl/translate.go` resolves `cpus` with `resolveDirectiveInt` and writes it to `scheduler.Requirements.Cores`.
- DIR-disk: SUPPORTED — `diskRE` in `nextflowdsl/parse.go` normalises disk strings to GB integers, and `buildRequirements` in `nextflowdsl/translate.go` writes the value to `scheduler.Requirements.Disk`.
- DIR-machineType: GAP — `parseProcessDirective` in `nextflowdsl/parse.go` accepts and stores `machineType`, but there is no corresponding consumer in the directive resolution or job requirement/application paths in `nextflowdsl/translate.go`, so it is parsed and then ignored.
- DIR-memory: SUPPORTED — `memoryRE` in `nextflowdsl/parse.go` normalises memory strings, and `buildRequirements` in `nextflowdsl/translate.go` writes the resolved value to `scheduler.Requirements.RAM`.
- DIR-resourceLabels: GAP — `parseProcessDirective` in `nextflowdsl/parse.go` accepts and stores `resourceLabels`, but no code in `nextflowdsl/translate.go` applies those labels to scheduler requirements or jobs.
- DIR-resourceLimits: GAP — `parseProcessDirective` in `nextflowdsl/parse.go` accepts and stores `resourceLimits`, but no code in `nextflowdsl/translate.go` consumes it when building requirements or jobs.
- DIR-time: SUPPORTED — `timeRE` in `nextflowdsl/parse.go` normalises duration strings to minutes, and `buildRequirements` in `nextflowdsl/translate.go` converts that to `scheduler.Requirements.Time`.
