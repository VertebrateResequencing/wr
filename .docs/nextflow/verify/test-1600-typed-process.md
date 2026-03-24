# Test: Typed Process (Preview)

**Spec files:** nf-1600-typed-process.md
**Impl files:** impl-04-translate-jobs.md

## Task

For each feature ID in nf-1600-typed-process.md, determine its classification.

### Checklist

1. Read nf-1600-typed-process.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- PSEC-typed-env, PSEC-typed-stageAs, PSEC-typed-stageAs-2, PSEC-typed-stdin, PSEC-typed-env-1
  PSEC-typed-eval, PSEC-typed-file, PSEC-typed-file-followLinks, PSEC-typed-file-glob, PSEC-typed-file-hidden
  PSEC-typed-file-includeInputs, PSEC-typed-file-maxDepth, PSEC-typed-file-optional, PSEC-typed-file-type, PSEC-typed-files
  PSEC-typed-stdout, PSEC-typed-outputs, PSEC-inputs-and-outputs-typed, PSEC-stage-directives

### Output format

```
PSEC-typed-env: SUPPORTED | reason
...
```

## Results

- PSEC-typed-env: GAP — typed stage env setup is not translated. `parseSection` accepts a `stage:` section but immediately calls `warnUnsupportedProcessSection`, and `applyEnv` only applies directive-level `proc.Env` entries populated by `parseDirective`, not typed stage directives.
- PSEC-typed-stageAs: GAP — `stage:` content is parsed as an unsupported raw process section in `parseSection`, with no AST or translation support for `stageAs`.
- PSEC-typed-stageAs-2: GAP — same as `PSEC-typed-stageAs`; iterable `stageAs` has no parser/runtime handling beyond the ignored `stage:` section.
- PSEC-typed-stdin: GAP — typed `stdin(value)` stage behavior is not implemented because the `stage:` section is warned unsupported in `parseSection` and never influences `buildCommandWithValues`.
- PSEC-typed-env-1: GAP — `parseDeclarationPrimary` recognises `env(...)`, but output translation never reads process environment. `staticOutputValue` only resolves declaration expressions, named bound inputs, or fallback bindings.
- PSEC-typed-eval: GAP — `eval` declarations are parsed and `evalOutputCaptureLines` appends command substitutions to the job body, but `outputValue`/`staticOutputValue` return the declaration expression or bindings, not the captured command result.
- PSEC-typed-file: GAP — `file` output declarations are routed through generic path-pattern handling in `outputPaths` and `resolveCompletedOutputValue`; wr does not enforce single-`Path` typed semantics and can return multiple matched paths.
- PSEC-typed-file-followLinks: GAP — no parser or translator support exists for a `followLinks` file option; only the primary `file(...)` argument is used and extra options are ignored by `parseDeclarationPrimary`.
- PSEC-typed-file-glob: GAP — there is no `glob` option support. wr always treats wildcard patterns via `matchCompletedOutputPaths` and `copyOutputCommands`, so typed `file.glob` semantics cannot be selected.
- PSEC-typed-file-hidden: GAP — no `hidden` option is stored in `Declaration` or handled in `translate.go`; the option is absent from parser/runtime support.
- PSEC-typed-file-includeInputs: GAP — no `includeInputs` option is represented in `Declaration` or used in translation, so this typed file option is ignored.
- PSEC-typed-file-maxDepth: GAP — no `maxDepth` option is parsed or applied anywhere in the file output translation path.
- PSEC-typed-file-optional: GAP — optionality is parsed into `Declaration.Optional`, but `translate.go` never reads that field, so optional file behavior is ignored at runtime.
- PSEC-typed-file-type: GAP — there is no `type` option handling for typed file outputs; neither the AST nor translation path carries file-type filtering semantics.
- PSEC-typed-files: GAP — there is no dedicated `files` output implementation. `files(...)` is not handled in `outputPaths`/`outputValue`, so it does not produce typed `Set<Path>` behavior.
- PSEC-typed-stdout: GAP — job commands always redirect stdout to `.nf-stdout` via `captureCommand`, but `outputValue` does not expose stdout content and instead falls back to output paths/CWD metadata.
- PSEC-typed-outputs: GAP — the `output:` section supports only partial legacy declaration behavior. `file`, `env`, `eval`, `stdout`, and typed options do not match Nextflow typed-process semantics end to end.
- PSEC-inputs-and-outputs-typed: GAP — typed process I/O metadata is not represented in the AST. `Declaration` stores `Kind`, `Name`, `Expr`, `Each`, `Optional`, and `Elements`, but no declared type information, so typed semantics are not preserved through translation.
- PSEC-stage-directives: GAP — `parseSection` explicitly marks the `stage:` process section unsupported and discards it from translation, so stage directives have no effect on generated jobs.
