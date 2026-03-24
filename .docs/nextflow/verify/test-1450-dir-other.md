# Test: Directives: Other

**Spec files:** nf-1450-dir-other.md
**Impl files:** impl-05-translate-directives.md

## Task

For each feature ID in nf-1450-dir-other.md, determine its classification.

### Checklist

1. Read nf-1450-dir-other.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- DIR-array, DIR-debug, DIR-echo, DIR-ext, DIR-label
  DIR-secret, DIR-shell, DIR-tag

### Output format

```
DIR-array: SUPPORTED | reason
...
```

## Results

- DIR-array: GAP — `parseDirective` stores `array` in `proc.Directives` and emits `warnStoredDirective`, but `translate.go` has no consumer for `"array"`, so job-array semantics are parsed then ignored.
- DIR-debug: GAP — `parseDirective` stores `debug` with `warnStoredDirective`, but no translation path uses it; `resolveDirectiveBool` exists, yet only `fair` is wired through `applyFairPriority`, so debug output behaviour is not applied.
- DIR-echo: GAP — `parseDirective` stores `echo` with `warnStoredDirective`, but `translate.go` never reads `proc.Directives["echo"]`, so the directive has no effect on job execution or logging.
- DIR-ext: SUPPORTED — `extDirectiveSourceMap`, `mergeExtValues`, and `resolveExtDirectiveWithValues` evaluate and merge the `ext` map, and `interpolateKnownScriptVars` resolves `${task.ext.*}` in scripts, which gives translated jobs access to `task.ext` values.
- DIR-label: SUPPORTED — `parseDirective` appends labels to `proc.Labels`, and `selectorMatchesProcess` plus `processWithConfigDefaults` use those labels to apply `withLabel` config selectors to the translated process, matching the practical effect of Nextflow labels.
- DIR-secret: GAP — `parseDirective` stores `secret` with `warnStoredDirective`, but there is no translation or environment-injection path for `proc.Directives["secret"]`, so secrets are silently ignored.
- DIR-shell: SUPPORTED — `resolveShellDirective` accepts a string or list of strings for the `shell` directive, and `applyEnv` exports the resolved command as `RunnerExecShell`, so translated jobs carry an explicit shell override.
- DIR-tag: GAP — `parseDirective` stores the tag text in `proc.Tag`, and `applyProcessDefaults` can resolve default config tags into that field, but no later translation step consumes `proc.Tag`, so it does not affect generated jobs.
