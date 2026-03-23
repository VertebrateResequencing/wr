# Test: Workflow Blocks

**Spec files:** nf-2000-workflow.md
**Impl files:** impl-01-parse.md, impl-04-translate-jobs.md, impl-09-ast.md

## Task

For each feature ID in nf-2000-workflow.md, determine its classification.

### Checklist

1. Check WorkflowBlock/SubWorkflow structs (ast.go) for field presence
2. Check parseWorkflow (parse.go) for parsing
3. Check translateProcessBindingSets (translate.go) for execution
4. Classify per 00-instructions.md criteria

### Features to classify

- WF-unnamed, WF-named, WF-call, WF-call-args, WF-call-out,
  WF-take, WF-emit, WF-publish, WF-onComplete, WF-onError,
  WF-conditional, WF-pipe, WF-and, WF-output-block

### Output format

```
WF-unnamed: SUPPORTED | reason
...
```
