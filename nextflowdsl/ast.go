/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package nextflowdsl

// Expr is a minimal Groovy expression node.
type Expr interface{ expr() }

// TupleElement is one element within a tuple declaration.
type TupleElement struct {
	Kind string
	Name string
	Expr Expr
	Raw  string
	Emit string
}

// Declaration is a parsed input or output declaration.
type Declaration struct {
	Kind     string
	Name     string
	Expr     Expr
	Raw      string
	Emit     string
	Each     bool
	Optional bool
	Elements []*TupleElement
}

// PublishDir holds a parsed publishDir directive.
type PublishDir struct {
	Path    string
	Pattern string
	Mode    string
}

// Process is a Nextflow process definition.
type Process struct {
	Name         string
	Labels       []string
	Tag          string
	BeforeScript string
	AfterScript  string
	Module       string
	Cache        string
	Directives   map[string]any
	Input        []*Declaration
	Output       []*Declaration
	Script       string
	Stub         string
	Exec         string
	Shell        string
	When         string
	Container    string
	PublishDir   []*PublishDir
	ErrorStrat   string
	MaxRetries   int
	MaxForks     int
	Env          map[string]string
}

// ChanExpr is a channel expression used in workflow invocations.
type ChanExpr interface{ chanExpr() }

// Call represents a process or subworkflow invocation.
type Call struct {
	Target string
	Args   []ChanExpr
}

// WFEmit represents one workflow emit declaration.
type WFEmit struct {
	Name string
	Expr string
}

// WFPublish represents one workflow publish assignment.
type WFPublish struct {
	Target string
	Source string
}

// IfBlock represents an if/else if/else in a workflow block.
type IfBlock struct {
	Condition string
	Body      []*Call
	ElseIf    []*IfBlock
	ElseBody  []*Call
}

// WorkflowBlock is the body of a workflow block.
type WorkflowBlock struct {
	Calls      []*Call
	Take       []string
	Emit       []*WFEmit
	Publish    []*WFPublish
	OnComplete string
	OnError    string
	Conditions []*IfBlock
}

// SubWorkflow is a named workflow block calling processes or other workflows.
type SubWorkflow struct {
	Name string
	Body *WorkflowBlock
}

// Import describes a module import.
type Import struct {
	Names  []string
	Source string
	Alias  map[string]string
}

// FuncDef stores a parsed top-level function definition.
type FuncDef struct {
	Name   string
	Params []string
	Body   string
}

// EnumDef stores a parsed top-level enum definition.
type EnumDef struct {
	Name   string
	Values []string
}

// ParamDecl stores a typed parameter from a params block.
type ParamDecl struct {
	Name    string
	Type    string
	Default Expr
}

// RecordField stores one field in a parsed top-level record definition.
type RecordField struct {
	Name    string
	Type    string
	Default Expr
}

// RecordDef stores a parsed top-level record definition.
type RecordDef struct {
	Name   string
	Fields []*RecordField
}

// Workflow is the top-level AST for a parsed .nf file.
type Workflow struct {
	Name        string
	Processes   []*Process
	SubWFs      []*SubWorkflow
	Imports     []*Import
	EntryWF     *WorkflowBlock
	Functions   []*FuncDef
	Enums       []*EnumDef
	OutputBlock string
	ParamBlock  []*ParamDecl
	Records     []*RecordDef
}

// ChanRef stores a named channel reference.
type ChanRef struct {
	Name string
}

func (ChanRef) chanExpr() {}

// NamedChannelRef stores a label-specific selection from a named channel result.
type NamedChannelRef struct {
	Source ChanExpr
	Label  string
}

func (NamedChannelRef) chanExpr() {}

// ChannelFactory stores a Channel.* factory call.
type ChannelFactory struct {
	Name string
	Args []Expr
}

func (ChannelFactory) chanExpr() {}

// ClosureExpr stores a parsed closure with parameters.
type ClosureExpr struct {
	Params []string
	Body   string
}

func (ClosureExpr) expr() {}

// AssertStmt stores an assert statement.
type AssertStmt struct {
	Expr    Expr
	Message Expr
}

// ThrowStmt stores a throw statement.
type ThrowStmt struct {
	Expr Expr
}

// CatchClause stores one catch block in a try statement.
type CatchClause struct {
	TypeName string
	VarName  string
	Body     string
}

// TryCatchStmt stores a try/catch/finally statement.
type TryCatchStmt struct {
	TryBody      string
	CatchClauses []CatchClause
	FinallyBody  string
}

// ReturnStmt stores a return statement.
type ReturnStmt struct {
	Expr Expr
}

// ForStmt stores a for-in loop.
type ForStmt struct {
	VarName    string
	Collection Expr
	Body       string
}

// WhileStmt stores a while loop.
type WhileStmt struct {
	Condition Expr
	Body      string
}

// SwitchStmt stores a switch statement.
type SwitchStmt struct {
	Expr string
	Body string
}

// ChannelOperator stores a supported channel operator invocation.
type ChannelOperator struct {
	Name        string
	Args        []Expr
	Channels    []ChanExpr
	Closure     string
	ClosureExpr *ClosureExpr
}

// ChannelChain stores a channel expression with chained operators.
type ChannelChain struct {
	Source    ChanExpr
	Operators []ChannelOperator
}

func (ChannelChain) chanExpr() {}

// PipeExpr stores a channel pipeline connected with |.
type PipeExpr struct {
	Stages []ChanExpr
}

func (PipeExpr) chanExpr() {}

// IntExpr stores an integer literal or a parsed unit value.
type IntExpr struct {
	Value int
}

func (IntExpr) expr() {}

// StringExpr stores a string literal.
type StringExpr struct {
	Value string
}

func (StringExpr) expr() {}

// SlashyStringExpr stores a slashy string literal.
type SlashyStringExpr struct {
	Value string
}

func (SlashyStringExpr) expr() {}

// ParamsExpr stores a params.* reference.
type ParamsExpr struct {
	Path string
}

func (ParamsExpr) expr() {}

// BoolExpr stores a boolean literal.
type BoolExpr struct {
	Value bool
}

func (BoolExpr) expr() {}

// VarExpr stores a variable reference, optionally with a dotted path.
type VarExpr struct {
	Root string
	Path string
}

func (VarExpr) expr() {}

// BinaryExpr stores a basic arithmetic expression.
type BinaryExpr struct {
	Left  Expr
	Op    string
	Right Expr
}

func (BinaryExpr) expr() {}

// InExpr stores an in or !in membership test.
type InExpr struct {
	Left    Expr
	Right   Expr
	Negated bool
}

func (InExpr) expr() {}

// RegexExpr stores a =~ or ==~ regex match expression.
type RegexExpr struct {
	Left  Expr
	Right Expr
	Full  bool
}

func (RegexExpr) expr() {}

// RangeExpr stores a Groovy range expression.
type RangeExpr struct {
	Start     Expr
	End       Expr
	Exclusive bool
}

func (RangeExpr) expr() {}

// TernaryExpr stores a ternary or elvis expression.
type TernaryExpr struct {
	Cond  Expr
	True  Expr
	False Expr
}

func (TernaryExpr) expr() {}

// UnaryExpr stores a unary operator expression.
type UnaryExpr struct {
	Op      string
	Operand Expr
}

func (UnaryExpr) expr() {}

// MethodCallExpr stores a method call on a receiver.
type MethodCallExpr struct {
	Receiver Expr
	Method   string
	Args     []Expr
}

func (MethodCallExpr) expr() {}

// NewExpr stores a constructor call.
type NewExpr struct {
	ClassName string
	Args      []Expr
}

func (NewExpr) expr() {}

// SpreadExpr stores a spread-dot property access.
type SpreadExpr struct {
	Receiver Expr
	Property string
}

func (SpreadExpr) expr() {}

// IndexExpr stores subscript access.
type IndexExpr struct {
	Receiver Expr
	Index    Expr
}

func (IndexExpr) expr() {}

// ListExpr stores a list literal.
type ListExpr struct {
	Elements []Expr
}

func (ListExpr) expr() {}

// MapExpr stores a map literal.
type MapExpr struct {
	Keys   []Expr
	Values []Expr
}

func (MapExpr) expr() {}

// NullExpr stores a null literal.
type NullExpr struct{}

func (NullExpr) expr() {}

// NullSafeExpr stores a null-safe property lookup.
type NullSafeExpr struct {
	Receiver Expr
	Property string
}

func (NullSafeExpr) expr() {}

// CastExpr stores a postfix type cast.
type CastExpr struct {
	Operand  Expr
	TypeName string
}

func (CastExpr) expr() {}

// MultiAssignExpr stores a multi-variable assignment.
type MultiAssignExpr struct {
	Names []string
	Value Expr
}

func (MultiAssignExpr) expr() {}

// UnsupportedExpr stores an expression that cannot be evaluated.
type UnsupportedExpr struct {
	Text string
}

func (UnsupportedExpr) expr() {}
