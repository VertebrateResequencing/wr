# Phase 1: Core parsing

Ref: [spec.md](spec.md) sections A1, A2, A3, A4

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Item 1.1: A1 - Parse process definitions

spec.md section: A1

Implement the lexer and recursive-descent parser foundation in
`nextflowdsl/parse.go` with
`Parse(r io.Reader) (*Workflow, error)`. Define AST node types
in `nextflowdsl/ast.go` (`Workflow`, `Process`, `Expr`,
`Declaration`, `PublishDir`). Handle process blocks with
directives (`cpus`, `memory`, `time`, `disk`, `container`,
`maxForks`, `errorStrategy`, `maxRetries`, `publishDir`, `env`),
input/output sections, and script bodies. Reject legacy DSL 1
syntax with a clear error. Ignore unrecognised directives with a
warning. Covering all 13 acceptance tests from A1.

- [ ] implemented
- [x] implemented
- [x] reviewed

### Item 1.2: A2 - Parse workflow blocks

spec.md section: A2

Extend the parser in `nextflowdsl/parse.go` to handle named and
unnamed workflow blocks (`WorkflowBlock`, `SubWorkflow`, `Call`,
`ChanExpr`). Parse process invocations with channel arguments,
pipe operator, and channel variable assignments. Covering all 4
acceptance tests from A2.

Depends on Item 1.1 for the parser foundation and AST types.

- [ ] implemented
- [x] implemented
- [x] reviewed

### Item 1.3: A3 - Parse channel factories and operators

spec.md section: A3

Extend the parser in `nextflowdsl/parse.go` to handle channel
factory calls (`Channel.of`, `Channel.fromPath`,
`Channel.fromFilePairs`, `Channel.value`, `Channel.empty`) and
operator chains (`map`, `flatMap`, `filter`, `collect`,
`groupTuple`, `join`, `mix`, `first`, `last`, `take`). Return
errors for unsupported operators. Covering all 13 acceptance
tests from A3.

Depends on Item 1.2 for the workflow/channel expression AST
types.

- [ ] implemented
- [x] implemented
- [x] reviewed

### Item 1.4: A4 - Parse import statements

spec.md section: A4

Extend the parser in `nextflowdsl/parse.go` to handle `include`
statements with single/multiple names, aliases, and local/remote
sources. Populate `Import` AST nodes. Covering all 4 acceptance
tests from A4.

Depends on Item 1.1 for the parser foundation.

- [ ] implemented
- [x] implemented
- [x] reviewed
