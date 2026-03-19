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