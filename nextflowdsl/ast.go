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

// WorkflowBlock is the body of a workflow block.
type WorkflowBlock struct {
	Calls []*Call
	Take  []string
	Emit  []*WFEmit
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

// Workflow is the top-level AST for a parsed .nf file.
type Workflow struct {
	Name      string
	Processes []*Process
	SubWFs    []*SubWorkflow
	Imports   []*Import
	EntryWF   *WorkflowBlock
	Functions []*FuncDef
}

// ChanRef stores a named channel reference.
type ChanRef struct {
	Name string
}

func (ChanRef) chanExpr() {}

// ChannelFactory stores a Channel.* factory call.
type ChannelFactory struct {
	Name string
	Args []Expr
}

func (ChannelFactory) chanExpr() {}

// ChannelOperator stores a supported channel operator invocation.
type ChannelOperator struct {
	Name     string
	Args     []Expr
	Channels []ChanExpr
	Closure  string
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

// UnsupportedExpr stores an expression that cannot be evaluated.
type UnsupportedExpr struct {
	Text string
}

func (UnsupportedExpr) expr() {}
