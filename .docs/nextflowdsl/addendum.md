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