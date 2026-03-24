# Test: Process Sections

**Spec files:** nf-1100-process-sections.md
**Impl files:** impl-01-parse.md, impl-04-translate-jobs.md

## Task

For each feature ID in nf-1100-process-sections.md, determine its classification.

### Checklist

1. Read nf-1100-process-sections.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- PSEC-inputs-and-outputs-legacy, PSEC-inputs, PSEC-generic-options, PSEC-generic-emit, PSEC-generic-optional
  PSEC-generic-topic, PSEC-directives, PSEC-script, PSEC-exec, PSEC-stub

### Output format

```
PSEC-inputs-and-outputs-legacy: SUPPORTED | reason
...
```

## Results

- PSEC-inputs-and-outputs-legacy: PARSE_ERROR — `parseSection` only accepts separate `input:` and `output:` blocks, and `parseDeclarationLine` explicitly rejects DSL1 `into` syntax with `DSL 1 syntax is not supported`, so legacy combined input/output syntax is not accepted.
- PSEC-inputs: GAP — `parseSection`/`parseDeclarations` and `resolveBindings` implement normal input parsing and binding, but `stdin` is only parsed in `parseDeclarationPrimary` and is never wired into `buildCommandWithValues` or `buildCommandBody`, so input semantics are incomplete.
- PSEC-generic-options: GAP — `applyDeclarationQualifier` only recognizes `emit`, `optional`, and `topic`; among those, only output-side `emit` is translated, while `optional` and `topic` are not consumed by the translator.
- PSEC-generic-emit: GAP — `applyDeclarationQualifier` parses `emit`, and output emits are carried through `emitOutputsForProcess` plus `resolveTranslatedOutput`, but input-side `emit` is never used during binding resolution.
- PSEC-generic-optional: GAP — `applyDeclarationQualifier` stores `decl.Optional`, but the translation path does not read `Optional` in `resolveBindings`, `outputPaths`, or `outputValue`, so the qualifier is parsed and ignored.
- PSEC-generic-topic: GAP — `applyDeclarationQualifier` parses `topic`, but no translation code uses it; output `topic` additionally triggers `warnUnsupportedOutputQualifier`, so topic semantics are not implemented.
- PSEC-directives: GAP — `parseDirective` accepts many directives, and some are translated by `buildRequirements`, `applyContainer`, `applyMaxForks`, `applyFairPriority`, `applyErrorStrategy`, `applyEnv`, `resolveScratchDirective`, `prependEnvironmentDirectives`, `resolveStoreDirDirective`, and `newProcessMaxErrorsJob`, but many others are only stored with `warnStoredDirective` or warned unsupported, so directive support is partial rather than full Nextflow parity.
- PSEC-script: SUPPORTED — `parseSection` stores `script`, and `buildCommandWithValues` -> `buildCommandBody` -> `renderScript` turns it into the job command with input binding and process environment handling.
- PSEC-exec: GAP — `parseSection` stores `proc.Exec` but immediately calls `warnUnsupportedProcessSection`, and job generation uses `renderScript(proc.Script)` rather than `proc.Exec`, so `exec:` parses but is not translated.
- PSEC-stub: SUPPORTED — `parseSection` stores `stub`, and `translateProcessBindingSets` swaps `procForCommand.Script` to `proc.Stub` when `tc.StubRun` is enabled, which implements stub-run behavior.
