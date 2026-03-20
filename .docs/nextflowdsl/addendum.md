# Nextflow DSL Spec Addendum

## 2026-03-19

### Phase 1 Item 1.1

- The architecture section defines `PublishDir.Mode` with a default of `"copy"`, but A1 and the Phase 1 item do not make that contract explicit in their acceptance coverage.
- The implementation sets the default mode to `copy` anyway so later translation work can rely on it.
- A1 acceptance coverage also does not explicitly require parsing explicit `publishDir` modes such as `move` or `link`, even though the parser now supports them.

### Phase 1 Item 1.2

- A2 does not state assignment scope semantics clearly. The implementation treats workflow-body assignments as block-local and allows fallback to top-level assignments.
- A2 does not say whether assignment statements must remain explicit in the AST or may be eagerly resolved into later call arguments. The implementation resolves them during parsing instead of introducing assignment nodes.
- The Item 1.2 text mentions pipe support and channel variable assignments, but the original A2 acceptance list does not explicitly cover workflow-body assignment handling or pipe parsing, so focused tests were added beyond the written acceptance cases.

### Phase 1 Item 1.3

- A3 lists `Channel.fromPath()` as a supported factory, but the A3 acceptance list does not include a direct factory-only acceptance case for it. Current coverage comes from adjacent workflow parsing tests.
- A3 specifies errors for unsupported operators but does not say what should happen for unsupported `Channel.*` factories. The implementation remains permissive for unknown factory names pending a clearer spec contract.
- A3 does not define the AST shape for operator chains or closure arguments, so the implementation introduced minimal internal nodes for chains and stores closure text in a normalized raw-string form.

### Phase 1 Item 1.4

- A4 does not explicitly say whether `include` statements are valid only at the top level or may also appear inside workflows. The implementation accepts them only at the top level.
- A4 examples show quoted `from` sources, but the spec does not explicitly state that the source must be a string literal. The parser currently requires a quoted source.
- A4 acceptance cases cover isolated import statements but not interleaving multiple `include` statements with `process` and `workflow` declarations, even though the parser now supports that top-level mix.

### Phase 2 Item 2.1

- B1 says profile-scoped values merge over defaults, but the Phase 2 Item 2.1 API only defines `ParseConfig`. The implementation parses and stores defaults plus profile overrides without inventing a separate profile-selection API yet.
- B1 acceptance coverage does not explicitly assert profile-scoped `process {}` overrides even though the implementation supports them.
- B1 remains intentionally narrow to block-style config parsing and does not define whether dotted assignments such as `params.input = '/data'` are in scope.

### Phase 2 Item 2.2

- B2 requires content sniffing for params files with ambiguous extensions, but the written B2 acceptance list does not include an explicit test case for that behavior.
- B2 does not define deep-merge semantics for nested maps during `MergeParams`; the implementation uses shallow top-level override, which is the minimal behavior required by the acceptance cases.
- B2 does not specify how non-string substituted values should be rendered into command strings. The implementation uses normal string formatting of the resolved value.

### Phase 2 Item 2.3

- B3 acceptance tests are written against `EvalExpr` directly and do not explicitly require parsed-input end-to-end coverage, even though the feature description implies expressions should work from real workflow/config source.
- The implementation now supports parsed workflow arithmetic and variable references end to end, and config-backed `params.*` expressions when the relevant params are already available in parse order.
- The spec does not state whether config-origin `params.*` expressions must resolve regardless of section order. The current implementation evaluates against the params visible at the point the process block is parsed.

### Phase 3 Items 3.1-3.2

- C1 names `./` and `/` module specs explicitly but does not say whether other relative forms such as `../...` are in scope. The implementation stays narrow and only accepts the forms named by the spec.
- C2 describes the cache path as `{owner}/{repo}/{revision}` but does not define the default revision name when the input is only `owner/repo`. The implementation uses `HEAD` for the default-branch cache key.
- C2 says the revision component may be a git ref, branch name, or tag, but the implementation uses `git clone --branch <revision>` for explicit revisions, which is suitable for branches and tags but not arbitrary commit SHAs. The spec should clarify whether raw SHAs are intended to be supported.

### Phase 3 Item 3.3

- C3 defines ordered fallback semantics but does not say what should happen when all resolvers fail, when the resolver list is empty, or when a `nil` resolver is present.
- The implementation skips `nil` resolvers, reports `no resolvers configured` for an empty usable chain, and returns a joined error when every resolver fails.
- `NewChainResolver` is implemented and tested directly, but later phases still need to wire it into actual module import resolution paths for end-to-end coverage.

### Phase 4 Items 4.2-4.5

- D2 requires resolving relative `publishDir` paths against the workflow file directory, but the spec’s `TranslateConfig` shape does not include a workflow-path field. The implementation added `WorkflowPath` so that behavior is expressible.
- D4 and D5 combine cleanly only if the caller invokes `TranslatePending` after all awaited dep groups are complete. The current `CompletedJob` contract does not itself prove completeness for multi-job fanout; it relies on the follow-loop calling discipline.
- D5 does not define what should happen when multiple fanout inputs on one process expand to different cardinalities. The implementation returns an error instead of inventing zip or cartesian semantics.

### Phase 4 Item 4.6

- D6 prose includes `join`, `last`, `take`, `map`, and `flatMap` as translation-relevant operators, but the written D6 acceptance list only exercises `collect`, `first`, `groupTuple`, `filter`, and `mix` end to end.
- The implementation covers those additional operators at the channel-resolution level, but the spec does not currently give them dedicated workflow-translation acceptance cases.

### Phase 5 Item 5.1

- E1 does not say whether nested `--param` overrides should preserve sibling nested keys. The implementation now uses deep merge semantics so overriding one leaf does not discard adjacent nested params.
- E1 lists `--follow` in the command surface, but Phase 5.2 is where the follow behavior is actually implemented. The current implementation exposes the flag and returns a clear not-yet-implemented error until E2 is completed.
- E1 does not define how workflow names should be derived for `rep_grp`. The implementation uses the workflow filename without its extension.

### Phase 5 Items 5.2-5.3

- E2 says the follow behavior is "tested via integration", but it does not define whether every acceptance branch must run through the Cobra command path or whether one command-path test plus narrower follow-loop tests is sufficient. The implementation now covers both: a real command-path follow test and focused follow-loop edge-case tests.
- E4 defines per-process status counts but does not specify how subworkflow-qualified process names should be rendered in the `Process` column. The current implementation reports the full process segment parsed from `rep_grp`, preserving any dotted subworkflow prefix.

### Phase 5 Item 5.4

- E3 lists a `deleted` job state in the resume matrix, but in practice the command detects that case by the job being absent from the `rep_grp`-prefix query result. The spec should say explicitly whether "deleted" means "missing from current wr state".
- E3 frames resume as re-invocation with both `--run-id` and `--follow`, but the implementation's reconciliation hook applies during general submission as well. The broader behavior is harmless and consistent, but the spec does not say whether resume semantics outside follow mode are intended.
- Real `wr` manager state can momentarily lag between an execution failure and the buried job appearing in the buried-state query. The follow loop now requires one confirmation poll before reporting terminal success so fast failures do not race past the error path, but the spec does not mention this visibility nuance.