# Implementation: AST Types (ast.go)

**File:** `nextflowdsl/ast.go` (~250 lines)

## Top-Level AST

### Workflow (line 167)
Top-level parse result for a `.nf` file:
- `Name` — workflow/pipeline name
- `Processes` — `[]*Process`
- `SubWFs` — `[]*SubWorkflow`
- `Imports` — `[]*Import`
- `EntryWF` — `*WorkflowBlock` (unnamed workflow)
- `Functions` — `[]*FuncDef`
- `Enums` — `[]*EnumDef`
- `OutputBlock` — raw output block text
- `ParamBlock` — `[]*ParamDecl`
- `Records` — `[]*RecordDef`

**Coverage:** INCL-*, PARAM-*, SYN-enum, SYN-record, SYN-function, WF-*

## Process AST

### Process (line 60)
22-field struct representing a process definition:
- Name, Labels → PSEC (identity)
- Tag → DIR-tag
- BeforeScript, AfterScript → DIR-beforeScript, DIR-afterScript
- Module → DIR-module
- Cache → DIR-cache
- Directives → DIR-* (arbitrary map)
- Input, Output → `[]*Declaration` → INP-*, OUT-*
- Script, Stub, Exec, Shell → PSEC-script, PSEC-stub, PSEC-exec, PSEC-shell
- When → PSEC-when
- Container → DIR-container
- PublishDir → `[]*PublishDir` → DIR-publishDir
- ErrorStrat → DIR-errorStrategy
- MaxRetries → DIR-maxRetries
- MaxForks → DIR-maxForks
- Env → DIR-env

### Declaration (line 42)
Input/output declaration:
- Kind: val, path, env, stdin, stdout, tuple
- Name, Expr, Raw, Emit, Each, Optional
- Elements: `[]*TupleElement` (for tuples)

### TupleElement (line 33)
One element within a tuple: Kind, Name, Expr, Raw, Emit.

### PublishDir (line 55)
Path, Pattern, Mode.

## Workflow AST

### SubWorkflow (line 117)
Named workflow: Name + Body (*WorkflowBlock).
**Coverage:** WF-named

### WorkflowBlock (line 108)
- Calls → `[]*Call` → process invocations
- Take → input channel names
- Emit → `[]*WFEmit` → output declarations
- Publish → `[]*WFPublish` → publish mappings
- OnComplete, OnError → event handlers
- Conditions → `[]*IfBlock` → conditional execution

**Coverage:** WF-take, WF-emit, WF-publish, WF-onComplete, WF-onError, WF-call

### Call (line 89)
Process/subworkflow invocation: Target name + Args (`[]ChanExpr`).

### IfBlock (line 99)
Conditional: Condition + Body + ElseIf chain + ElseBody.
**Coverage:** WF-conditional

## Channel Expression AST

### ChanRef (line 183)
Named channel reference.

### NamedChannelRef (line 189)
`source.label` — selects named output from operator result.

### ChannelFactory (line 196)
`Channel.name(args)` factory call.

### ClosureExpr (line 203)
Parsed closure: Params + Body string.

## Other AST

### Import (line 122)
Module import: Names, Source path, Alias map.
**Coverage:** INCL-include, INCL-alias

### FuncDef (line 129)
Function: Name, Params, Body.
**Coverage:** SYN-function

### EnumDef (line 135) / RecordDef (line 148) / RecordField (line 143)
Enum/record definitions.
**Coverage:** SYN-enum, SYN-record

### ParamDecl (line 137)
Typed parameter: Name, Type, Default.

### Statement ASTs
AssertStmt, ThrowStmt, TryCatchStmt, CatchClause, ReturnStmt, ForStmt.
**Coverage:** STMT-assert, STMT-throw, STMT-try, STMT-return, STMT-for
