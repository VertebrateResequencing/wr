# Test: Built-in Variables, Functions and Namespaces

**Spec files:** nf-4500-builtins.md
**Impl files:** impl-08-groovy.md

## Task

For each feature ID in nf-4500-builtins.md, determine its classification.

### Checklist

1. Read nf-4500-builtins.md to understand what Nextflow expects
2. Read the impl files to find where to look in Go source
3. Read the actual Go source code at the cited locations
4. Classify each feature per 00-instructions.md criteria

### Features to classify

- BV-baseDir, BV-launchDir, BV-moduleDir, BV-params, BV-projectDir
  BV-secrets, BV-workDir, BV-branchCriteria, BV-env, BV-error
  BV-exit, BV-file, BV-file-checkIfExists, BV-file-followLinks, BV-file-glob
  BV-file-hidden, BV-file-maxDepth, BV-file-type, BV-files, BV-groupKey
  BV-multiMapCriteria, BV-print, BV-printf, BV-println, BV-sendMail
  BV-sleep, BV-record, BV-tuple, BV-log-error, BV-log-info
  BV-log-warn, BV-nextflow-build, BV-nextflow-timestamp, BV-nextflow-version, BV-workflow-commandLine
  BV-workflow-commitId, BV-workflow-complete, BV-workflow-configFiles, BV-workflow-container, BV-workflow-containerEngine
  BV-workflow-duration, BV-workflow-errorMessage, BV-workflow-errorReport, BV-workflow-exitStatus, BV-workflow-failOnIgnore
  BV-workflow-fusion, BV-workflow-fusion-enabled, BV-workflow-fusion-version, BV-workflow-homeDir, BV-workflow-launchDir
  BV-workflow-manifest, BV-workflow-outputDir, BV-workflow-preview, BV-workflow-profile, BV-workflow-projectDir
  BV-workflow-repository, BV-workflow-resume, BV-workflow-revision, BV-workflow-runName, BV-workflow-scriptFile
  BV-workflow-scriptId, BV-workflow-scriptName, BV-workflow-sessionId, BV-workflow-start, BV-workflow-stubRun
  BV-workflow-success, BV-workflow-userName, BV-workflow-wave, BV-workflow-wave-enabled, BV-workflow-workDir
  BV-workflow-onComplete, BV-workflow-onError

### Output format

```
BV-baseDir: SUPPORTED | reason
...
```

## Results

- BV-baseDir: FUTURE — `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `baseDir` built-in.
- BV-launchDir: FUTURE — `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `launchDir` built-in.
- BV-moduleDir: FUTURE — `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `moduleDir` built-in.
- BV-params: SUPPORTED — `EvalExpr` has a dedicated `ParamsExpr` path and `resolveExprPath` gives `params.*` nil-on-missing semantics instead of erroring.
- BV-projectDir: FUTURE — `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `projectDir` built-in.
- BV-secrets: FUTURE — `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `secrets` built-in.
- BV-workDir: FUTURE — `EvalExpr`/`resolveExprPath` can only read pre-bound variables, and the cited `groovy.go` code does not bind a `workDir` built-in.
- BV-branchCriteria: FUTURE — bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `branchCriteria(...)` built-in.
- BV-env: FUTURE — bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `env(...)` built-in.
- BV-error: FUTURE — bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `error(...)` built-in.
- BV-exit: FUTURE — bare calls are routed through `evalMethodCallExpr`, but `evalStaticMethodCall` only implements `Integer.parseInt`; there is no `exit(...)` built-in.
- BV-file: FUTURE — there is no `file(...)` built-in in `evalMethodCallExpr`; `evalPathConstructor` only covers `new File(...)`/`new Path(...)`.
- BV-file-checkIfExists: FUTURE — `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.
- BV-file-followLinks: FUTURE — `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.
- BV-file-glob: FUTURE — `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.
- BV-file-hidden: FUTURE — `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.
- BV-file-maxDepth: FUTURE — `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.
- BV-file-type: FUTURE — `file(...)` itself is not implemented in `groovy.go`, so its option fields are not implemented either.
- BV-files: FUTURE — there is no `files(...)` built-in in `evalMethodCallExpr`; `evalPathConstructor` only covers constructors.
- BV-groupKey: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `groupKey(...)` built-in implementation.
- BV-multiMapCriteria: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `multiMapCriteria(...)` built-in implementation.
- BV-print: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `print(...)` built-in implementation.
- BV-printf: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `printf(...)` built-in implementation.
- BV-println: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `println(...)` built-in implementation.
- BV-sendMail: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `sendMail(...)` built-in implementation.
- BV-sleep: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `sleep(...)` built-in implementation.
- BV-record: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `record(...)` built-in implementation.
- BV-tuple: FUTURE — bare calls are routed through `evalMethodCallExpr`, but there is no `tuple(...)` built-in implementation; the internal `closureTupleValues` helper is unrelated.
- BV-log-error: FUTURE — `evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.error(...)` namespace receiver is implemented.
- BV-log-info: FUTURE — `evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.info(...)` namespace receiver is implemented.
- BV-log-warn: FUTURE — `evalMethodCallExpr` only dispatches receivers as string, map, list, number, or static call, and no `log.warn(...)` namespace receiver is implemented.
- BV-nextflow-build: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.
- BV-nextflow-timestamp: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.
- BV-nextflow-version: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `nextflow` map; `bindWorkflowEnumValues` only injects enum members.
- BV-workflow-commandLine: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-commitId: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-complete: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-configFiles: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-container: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-containerEngine: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-duration: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-errorMessage: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-errorReport: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-exitStatus: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-failOnIgnore: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-fusion: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-fusion-enabled: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-fusion-version: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-homeDir: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-launchDir: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-manifest: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-outputDir: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-preview: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-profile: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-projectDir: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-repository: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-resume: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-revision: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-runName: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-scriptFile: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-scriptId: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-scriptName: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-sessionId: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-start: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-stubRun: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-success: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-userName: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-wave: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-wave-enabled: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-workDir: FUTURE — `resolveExprPath` can walk pre-bound maps, but the cited code does not bind a `workflow` map with documented runtime properties.
- BV-workflow-onComplete: FUTURE — `evalMethodCallExpr` has no implementation for workflow lifecycle hook methods such as `workflow.onComplete(...)`.
- BV-workflow-onError: FUTURE — `evalMethodCallExpr` has no implementation for workflow lifecycle hook methods such as `workflow.onError(...)`.
