# Fix All 43 Parse Errors in nextflowdsl Package

## Background

We have a pure-Go Nextflow DSL2 parser in the `nextflowdsl/` package which
parses `.nf` workflow files and config files, translating them into
`jobqueue.Job` slices that wr executes.

The parser currently fails on 43 Nextflow features documented in
`.docs/nextflow/parse_errors.md`. Each causes a parse error that prevents
processing of any pipeline that uses the feature. This spec covers fixing
all 43 so they no longer produce parse errors.

## Design principles (from prior specs -- maintain these)

- Pure Go, no CGo dependencies beyond what's already in go.mod.
- No intermediate files written to disk.
- No separate state store -- crash recovery uses wr's existing job
  persistence.
- All changes confined to existing `nextflowdsl/` and `cmd/` packages.
- Tests use GoConvey style.
- Constructs that cannot be meaningfully translated to wr jobs should be
  parsed without error and produce clear warnings, never parse failures.

## Parse Errors to Fix (43 total)

### Group 1: Add placeholder to `defaultDirectiveTask()` (7 features)

In `nextflowdsl/translate.go`, add missing keys to the map returned by
`defaultDirectiveTask()` with sensible zero/placeholder values (empty
string for strings, 0 for ints). This prevents "unknown variable" errors
when dynamic directives reference the property.

Features: BV-task-hash, BV-task-index, BV-task-name,
BV-task-previousException, BV-task-previousTrace, BV-task-process,
BV-task-workDir.

Each currently fails in `resolveExprPath`/`lookupVariablePart` in
`nextflowdsl/groovy.go` with `unknown variable "task.<property>"`.

### Group 2: Add scope to `skippedTopLevelConfigScopes` (15 features)

In `nextflowdsl/config.go`, add scope names to the
`skippedTopLevelConfigScopes` map so the config parser silently skips
them instead of returning "unsupported config section".

Features: CFG-aws, CFG-azure, CFG-charliecloud, CFG-fusion, CFG-google,
CFG-k8s, CFG-lineage, CFG-mail, CFG-nextflow, CFG-podman, CFG-sarus,
CFG-seqera, CFG-shifter, CFG-spack, CFG-workflow.

### Group 3: Add `nextflow` to `skippedTopLevelConfigScopes` (8 features)

Same fix as Group 2 -- adding `"nextflow"` to `skippedTopLevelConfigScopes`
fixes all 8 `nextflow.enable.*` and `nextflow.preview.*` feature flags.
NOTE: `CFG-nextflow` from Group 2 and the 8 features here are all fixed
by the same single addition of `"nextflow"` to the map. The groups overlap
on this scope name.

Features: CFG-enable-configProcessNamesValidation, CFG-enable-dsl,
CFG-enable-moduleBinaries, CFG-enable-strict, CFG-preview-output,
CFG-preview-recursion, CFG-preview-topic, CFG-preview-types.

### Group 4: Skip nested blocks in `parseExecutorBlock` (2 features)

In `nextflowdsl/config.go`, modify `parseExecutorBlock` to detect when
a value token is `{` (start of nested block) and skip it with
`skipNamedBlock` or brace-counting, rather than only accepting flat
assignments.

Features: CFG-executor-specific-configuration,
CFG-executor-specific-defaults.

### Group 5: Handle bare assignments in config parser (1 feature)

In `nextflowdsl/config.go`, modify `configParser.parse` to detect and
skip unrecognised top-level assignments (e.g. `cleanup = true`) instead
of erroring.

Feature: CFG-unscoped-options.

### Group 6: Add qualifier to `applyDeclarationQualifier` /
`applyTupleElementQualifier` (2 features)

In `nextflowdsl/parse.go`, add cases for `name:` and `stageAs:` qualifiers
in both `applyDeclarationQualifier` and `applyTupleElementQualifier`
switch statements. The qualifier values are string expressions that should
be stored on the Declaration/TupleElement (may need new fields).

Features: INP-path-name, INP-path-stageAs.

### Group 7: Skip legacy DSL1 combined input/output syntax (1 feature)

DSL1 syntax which Nextflow itself has removed. Rather than implementing
it, improve the error handling so the parser does not crash when
encountering legacy pipelines. Emit a clear warning and skip the block.

Feature: PSEC-inputs-and-outputs-legacy.

### Group 8: Parse `def (a, b) = expr` in statement parser (1 feature)

In `nextflowdsl/parse.go`, modify `parseAssignmentStmt` to accept `def`
followed by `(` as a multi-assignment. The evaluator
`evalMultiAssignExpr` already exists -- the parser just needs to produce
the right AST node.

Feature: STMT-multi-assignment.

### Group 9: Skip deprecated `addParams`/`params` after include source
(1 feature)

In `nextflowdsl/parse.go`, after `parseImport` reads the `from` source
string, check if the next token is an identifier matching `addParams` or
`params`, and if so, skip it plus any following parenthesised/map
argument. Emit a deprecation warning.

Feature: SYN-deprecated-addParams.

### Group 10: Parse deprecated loop constructs with warning (2 features)

In `nextflowdsl/parse.go`, add support for `for`/`while` in
`parseWorkflowStatement` or the statement parser. Parse the loop structure
(condition + body), emit a deprecation warning, and either evaluate the
body or skip it gracefully. These are deprecated in Nextflow strict mode.

Features: SYN-deprecated-for-loop, SYN-deprecated-while-loop.

### Group 11: Handle `&` (parallel) in workflow statement desugaring
(1 feature)

In `nextflowdsl/parse.go`, modify `desugarWorkflowPipe` or
`parseWorkflowStatement` to recognise `BitwiseAndExpr` as a parallel
fork. `A & B` means run A and B in parallel on the same input. Produce
two separate `Call` entries with the same input channel.

Feature: WF-and.

### Group 12: Add to `supportedChannelOperators` (2 features)

In `nextflowdsl/parse.go`, add operator names to the
`supportedChannelOperators` map so they parse as channel operator calls
instead of being rejected.

Features: WF-recurse, WF-times.

## Notes

- `name:` and `stageAs:` qualifiers (Group 6): parse and store as simple
  string fields on Declaration/TupleElement. Not used during translation
  -- just prevents parse errors.
- Deprecated `for`/`while` loops (Group 10): skip the loop body entirely
  with a deprecation warning. Variables assigned inside are not available
  after the loop.
- `recurse` and `times` operators (Group 12): add to
  `supportedChannelOperators` as pass-through only. Parse without error
  but no runtime recursion semantics.
- `&` parallel operator (Group 11): real fork semantics -- produce two
  separate Call entries sharing the same input channel.
- All existing tests must continue to pass.
- Acceptance tests use synthetic minimal test cases.
- Constructs that cannot be meaningfully translated should parse without
  error and produce warnings, never parse failures.
- Deprecated `for`/`while` loops: skip by consuming tokens
  (brace-counting), no AST node types needed. Just consume the loop
  syntax and emit a deprecation warning.
- DSL1 legacy syntax: silently skip with a warning, do not fail.
- `&` operator: flatten BitwiseAndExpr to multiple Call entries in
  `desugarWorkflowPipe`. No new AST node type needed.
- Multi-assignment: support both `def (a, b) = expr` and
  `(a, b) = expr` forms.
- Tests: each group needs tests verifying that parsing completes without
  error AND that appropriate warnings are emitted where applicable.
  Tests grouped by fix strategy (matching the 12 groups above).
- Warning format: follow existing warning patterns in the codebase.
- Field naming: follow existing codebase conventions for struct fields.
