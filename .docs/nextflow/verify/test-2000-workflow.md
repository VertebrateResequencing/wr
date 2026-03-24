# Test: Workflow

**Spec files:** nf-2000-workflow.md
**Impl files:** impl-04-translate-jobs.md, impl-01-parse.md

## Task

For each feature ID in nf-2000-workflow.md, determine its classification.

### Checklist

1. Read nf-2000-workflow.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- WF-workflow, WF-output-block, WF-take, WF-main, WF-emit
  WF-publish, WF-onComplete, WF-onError, WF-pipe, WF-and
  WF-recurse, WF-until, WF-times, WF-out-directory, WF-out-mode
  WF-out-overwrite, WF-out-path, WF-out-index, WF-out-index-header, WF-out-index-path
  WF-out-index-sep, WF-out-enabled, WF-out-tags, WF-out-contentType, WF-out-label
  WF-out-ignoreErrors, WF-out-storageClass, WF-out-copyAttributes

### Output format

```
WF-workflow: SUPPORTED | reason
...
```

## Results

- WF-workflow: SUPPORTED — `parseWorkflow` in `nextflowdsl/parse.go` builds `EntryWF`/`SubWFs`, and `Translate` plus `translateBlock`/`translateCalls` in `nextflowdsl/translate.go` translate entry and named workflow blocks into jobs.
- WF-output-block: GAP — `parseTopLevelOutputBlock` stores the raw block, but `ParseOutputBlock`/`parseOutputTarget` in `nextflowdsl/translate.go` only keep targets with a static `path` and optional `index.path`; unsupported properties are consumed and ignored, and non-static targets are skipped.
- WF-take: SUPPORTED — `parseWorkflowTakeLine` records `take:` inputs and `bindSubworkflowInputs` wires call arguments into scoped subworkflow inputs.
- WF-main: SUPPORTED — `parseWorkflowBlock` treats `main` as the executable section and `translateBlock` translates its calls and conditionals.
- WF-emit: SUPPORTED — `parseWorkflowEmitLine` records workflow emits, and `bindSubworkflowOutputs` turns them into synthetic emitted outputs for downstream references.
- WF-publish: SUPPORTED — `parseWorkflowPublishLine` records `publish:` assignments, and `translateWorkflowPublishes` attaches publish copy behaviours and optional index jobs from those assignments.
- WF-onComplete: GAP — top-level `workflow.onComplete { ... }` is parsed by `parseWorkflowLifecycleHandler` but discarded there; only block-scoped `onComplete:` stored on `WorkflowBlock` is translated later by `translateLifecycleHooks`.
- WF-onError: GAP — top-level `workflow.onError { ... }` is parsed by `parseWorkflowLifecycleHandler` but discarded there; only block-scoped `onError:` stored on `WorkflowBlock` is translated by `translateLifecycleHooks`.
- WF-pipe: SUPPORTED — `parseChanExpr` builds a `PipeExpr`, and `desugarWorkflowPipe` rewrites `a | b | c` into sequential bare-identifier calls that `translateCalls` can run for processes or subworkflows.
- WF-and: PARSE_ERROR — `&` is parsed as a generic bitwise operator by `parseBitwiseAndExprTokens`, but `parseWorkflowStatement` only accepts direct calls or `PipeExpr`, so `desugarWorkflowPipe` fails with `workflow statements must be calls or channel pipelines`.
- WF-recurse: PARSE_ERROR — `parseChannelOperators` only accepts operators listed in `supportedChannelOperators`, and `recurse` is not listed, so `.recurse(...)` is rejected as an unsupported operator.
- WF-until: GAP — `supportedChannelOperators` includes `until`, so it can parse as a generic channel operator, but there is no recursion-specific AST or translation path in `translate.go`; workflow translation remains linear through `desugarWorkflowPipe`/`translateCalls`, so recursion termination semantics are not implemented.
- WF-times: PARSE_ERROR — `times` is not in `supportedChannelOperators`, so `.times(...)` is rejected by `parseChannelOperators` as an unsupported operator.
- WF-out-directory: GAP — `parseOutputTarget` ignores unknown output-target properties unless they are `path` or `index`, so `directory` is parsed and discarded.
- WF-out-mode: GAP — `parseOutputTarget` ignores `mode`, and `buildWorkflowPublishCommand` always emits copy commands via `publishDirActionCommand("copy", ...)`.
- WF-out-overwrite: GAP — `overwrite` is consumed by `parseOutputTarget` but never stored or used during publish translation.
- WF-out-path: GAP — `parseOutputTarget` requires `path`, but `parseOutputStaticPath` only accepts a static string; non-static or closure-valued paths cause the target to be skipped.
- WF-out-index: GAP — `parseOutputTarget` recognizes `index { ... }`, but only `index.path` is used; other `index` sub-directives do not affect translation.
- WF-out-index-header: GAP — `parseOutputIndexPath` only looks for `path`, so `header` is ignored.
- WF-out-index-path: GAP — `parseOutputIndexPath` supports only a static string path through `parseOutputStaticPath`; non-static values skip the target.
- WF-out-index-sep: GAP — `parseOutputIndexPath` ignores `sep`, and `buildWorkflowPublishIndexCommand` always writes tab-separated lines.
- WF-out-enabled: GAP — `enabled` is consumed by `parseOutputTarget` and has no effect on publish translation.
- WF-out-tags: GAP — `tags` is consumed by `parseOutputTarget` and never used.
- WF-out-contentType: GAP — `contentType` is consumed by `parseOutputTarget` and never used.
- WF-out-label: GAP — `label` is consumed by `parseOutputTarget` and never used.
- WF-out-ignoreErrors: GAP — `ignoreErrors` is consumed by `parseOutputTarget` and never used.
- WF-out-storageClass: GAP — `storageClass` is consumed by `parseOutputTarget` and never used.
- WF-out-copyAttributes: GAP — `copyAttributes` is consumed by `parseOutputTarget` and never used.
