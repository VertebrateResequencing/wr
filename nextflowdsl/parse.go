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

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var memoryRE = regexp.MustCompile(`(?i)^([0-9]+)\s*(gb|g|mb|m)$`)

var diskRE = regexp.MustCompile(`(?i)^([0-9]+)\s*(gb|g)$`)

var timeRE = regexp.MustCompile(`(?i)^([0-9]+)(?:\s*\.\s*|\s+)?(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)

var supportedChannelOperators = map[string]struct{}{
	"collect":    {},
	"filter":     {},
	"first":      {},
	"flatMap":    {},
	"groupTuple": {},
	"join":       {},
	"last":       {},
	"map":        {},
	"mix":        {},
	"take":       {},
}

type tokenType int

const (
	tokenEOF tokenType = iota
	tokenIdent
	tokenInt
	tokenString
	tokenLBrace
	tokenRBrace
	tokenLParen
	tokenRParen
	tokenColon
	tokenComma
	tokenDot
	tokenSemicolon
	tokenAssign
	tokenPipe
	tokenSymbol
	tokenNewline
)

type token struct {
	typ  tokenType
	lit  string
	line int
	col  int
}

func lex(input string) ([]token, error) {
	l := &lexer{input: []rune(input), line: 1, col: 1}
	tokens := make([]token, 0, len(input)/2)

	for {
		r := l.peek()
		switch {
		case r == 0:
			tokens = append(tokens, token{typ: tokenEOF, line: l.line, col: l.col})
			return tokens, nil
		case r == ' ' || r == '\t' || r == '\r':
			l.next()
		case r == '\n':
			line, col := l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenNewline, lit: "\n", line: line, col: col})
		case r == '/' && l.peekN(1) == '/':
			for l.peek() != 0 && l.peek() != '\n' {
				l.next()
			}
		case r == '/' && l.peekN(1) == '*':
			if err := l.skipBlockComment(); err != nil {
				return nil, err
			}
		case r == '#' && !l.lineHasContent:
			for l.peek() != 0 && l.peek() != '\n' {
				l.next()
			}
		case r == '{':
			tokens = append(tokens, token{typ: tokenLBrace, lit: "{", line: l.line, col: l.col})
			l.next()
		case r == '}':
			tokens = append(tokens, token{typ: tokenRBrace, lit: "}", line: l.line, col: l.col})
			l.next()
		case r == '(':
			tokens = append(tokens, token{typ: tokenLParen, lit: "(", line: l.line, col: l.col})
			l.next()
		case r == ')':
			tokens = append(tokens, token{typ: tokenRParen, lit: ")", line: l.line, col: l.col})
			l.next()
		case r == ':':
			tokens = append(tokens, token{typ: tokenColon, lit: ":", line: l.line, col: l.col})
			l.next()
		case r == ',':
			tokens = append(tokens, token{typ: tokenComma, lit: ",", line: l.line, col: l.col})
			l.next()
		case r == '.':
			tokens = append(tokens, token{typ: tokenDot, lit: ".", line: l.line, col: l.col})
			l.next()
		case r == ';':
			tokens = append(tokens, token{typ: tokenSemicolon, lit: ";", line: l.line, col: l.col})
			l.next()
		case r == '=':
			tokens = append(tokens, token{typ: tokenAssign, lit: "=", line: l.line, col: l.col})
			l.next()
		case r == '|':
			tokens = append(tokens, token{typ: tokenPipe, lit: "|", line: l.line, col: l.col})
			l.next()
		case isClosureSymbol(r):
			tokens = append(tokens, token{typ: tokenSymbol, lit: string(r), line: l.line, col: l.col})
			l.next()
		case r == '\'' || r == '"':
			stringTok, err := l.readString()
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, stringTok)
		case unicode.IsDigit(r):
			tokens = append(tokens, l.readInt())
		case isIdentStart(r):
			tokens = append(tokens, l.readIdent())
		default:
			return nil, fmt.Errorf("line %d: unexpected character %q", l.line, r)
		}
	}
}

func isClosureSymbol(r rune) bool {
	switch r {
	case '*', '+', '-', '/', '<', '>', '?':
		return true
	default:
		return false
	}
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func parseParamsPath(tokens []token) (string, bool) {
	if len(tokens) < 3 || tokens[0].typ != tokenIdent || tokens[0].lit != "params" {
		return "", false
	}

	parts := make([]string, 0, len(tokens)/2)
	for index := 1; index < len(tokens); index += 2 {
		if tokens[index].typ != tokenDot {
			return "", false
		}
		if index+1 >= len(tokens) || tokens[index+1].typ != tokenIdent {
			return "", false
		}
		parts = append(parts, tokens[index+1].lit)
	}

	return strings.Join(parts, "."), true
}

type lexer struct {
	input          []rune
	pos            int
	line           int
	col            int
	lineHasContent bool
}

func (l *lexer) peek() rune {
	if l.pos >= len(l.input) {
		return 0
	}

	return l.input[l.pos]
}

func (l *lexer) peekN(offset int) rune {
	index := l.pos + offset
	if index >= len(l.input) {
		return 0
	}

	return l.input[index]
}

func (l *lexer) next() rune {
	if l.pos >= len(l.input) {
		return 0
	}

	r := l.input[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
		l.lineHasContent = false
	} else {
		l.col++
		if r != ' ' && r != '\t' && r != '\r' {
			l.lineHasContent = true
		}
	}

	return r
}

func (l *lexer) skipBlockComment() error {
	line := l.line

	l.next()
	l.next()

	for {
		r := l.peek()
		if r == 0 {
			return fmt.Errorf("line %d: unterminated comment", line)
		}

		if r == '*' && l.peekN(1) == '/' {
			l.next()
			l.next()
			return nil
		}

		l.next()
	}
}

func (l *lexer) readString() (token, error) {
	quote := l.peek()
	line, col := l.line, l.col
	triple := l.peekN(1) == quote && l.peekN(2) == quote
	if triple {
		l.next()
		l.next()
		l.next()
	} else {
		l.next()
	}

	var builder strings.Builder
	for {
		r := l.peek()
		if r == 0 {
			return token{}, fmt.Errorf("line %d: unterminated string literal", line)
		}

		if triple {
			if r == quote && l.peekN(1) == quote && l.peekN(2) == quote {
				l.next()
				l.next()
				l.next()
				break
			}
		} else if r == quote {
			l.next()
			break
		}

		if r == '\\' && !triple {
			l.next()
			escaped := l.peek()
			if escaped == 0 {
				return token{}, fmt.Errorf("line %d: unterminated escape sequence", l.line)
			}
			builder.WriteRune(unescapeRune(escaped))
			l.next()
			continue
		}

		builder.WriteRune(l.next())
	}

	return token{typ: tokenString, lit: builder.String(), line: line, col: col}, nil
}

func unescapeRune(r rune) rune {
	switch r {
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	default:
		return r
	}
}

func (l *lexer) readInt() token {
	line, col := l.line, l.col
	start := l.pos
	for unicode.IsDigit(l.peek()) {
		l.next()
	}

	return token{typ: tokenInt, lit: string(l.input[start:l.pos]), line: line, col: col}
}

func (l *lexer) readIdent() token {
	line, col := l.line, l.col
	start := l.pos
	for isIdentPart(l.peek()) {
		l.next()
	}

	return token{typ: tokenIdent, lit: string(l.input[start:l.pos]), line: line, col: col}
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r)
}

type parser struct {
	tokens      []token
	pos         int
	assignments map[string]ChanExpr
	localScopes []map[string]ChanExpr
}

func newParser(tokens []token) *parser {
	return &parser{tokens: tokens, assignments: make(map[string]ChanExpr)}
}

func (p *parser) parseWorkflow() (*Workflow, error) {
	wf := &Workflow{Processes: []*Process{}, SubWFs: []*SubWorkflow{}, Imports: []*Import{}}

	for {
		p.skipNewlines()
		current := p.current()
		if current.typ == tokenEOF {
			return wf, nil
		}

		if current.typ == tokenIdent && current.lit == "include" {
			importNode, err := p.parseImport()
			if err != nil {
				return nil, err
			}
			wf.Imports = append(wf.Imports, importNode)
			continue
		}

		if current.typ == tokenIdent && current.lit == "process" {
			proc, err := p.parseProcess()
			if err != nil {
				return nil, err
			}
			wf.Processes = append(wf.Processes, proc)
			continue
		}

		if current.typ == tokenIdent && current.lit == "workflow" {
			name, block, err := p.parseWorkflowBlockDecl()
			if err != nil {
				return nil, err
			}
			if name == "" {
				wf.EntryWF = block
			} else {
				wf.SubWFs = append(wf.SubWFs, &SubWorkflow{Name: name, Body: block})
			}
			continue
		}

		if p.skipFeatureFlagAssignment() {
			continue
		}

		if current.typ == tokenIdent && p.peek().typ == tokenAssign {
			if err := p.parseChannelAssignment(tokenNewline, tokenEOF); err != nil {
				return nil, err
			}
			continue
		}

		return nil, fmt.Errorf("line %d: unexpected token %q", current.line, current.lit)
	}
}

func (p *parser) skipFeatureFlagAssignment() bool {
	if !p.isFeatureFlagAssignmentStart() {
		return false
	}

	for {
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenNewline {
			break
		}
		p.pos++
	}

	if p.current().typ == tokenNewline {
		p.pos++
	}

	return true
}

func (p *parser) isFeatureFlagAssignmentStart() bool {
	if p.current().typ != tokenIdent || p.current().lit != "nextflow" {
		return false
	}

	index := p.pos + 1
	if index+3 >= len(p.tokens) {
		return false
	}

	if p.tokens[index].typ != tokenDot || p.tokens[index+1].typ != tokenIdent {
		return false
	}

	section := p.tokens[index+1].lit
	if section != "enable" && section != "preview" {
		return false
	}

	index += 2
	segments := 0
	for index+1 < len(p.tokens) && p.tokens[index].typ == tokenDot && p.tokens[index+1].typ == tokenIdent {
		segments++
		index += 2
	}

	if segments == 0 || index >= len(p.tokens) {
		return false
	}

	return p.tokens[index].typ == tokenAssign
}

func (p *parser) parseImport() (*Import, error) {
	if _, err := p.expectIdent("include"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	importNode := &Import{Names: []string{}, Alias: make(map[string]string)}
	for {
		p.skipNewlines()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, fmt.Errorf("line %d: expected } to close include", current.line)
		case tokenRBrace:
			if len(importNode.Names) == 0 {
				return nil, fmt.Errorf("line %d: expected import name", current.line)
			}
			p.pos++
			goto parseSource
		case tokenIdent:
			name := current.lit
			importNode.Names = append(importNode.Names, name)
			p.pos++

			if p.current().typ == tokenIdent && p.current().lit == "as" {
				p.pos++
				alias, err := p.expectType(tokenIdent, "import alias")
				if err != nil {
					return nil, err
				}
				importNode.Alias[name] = alias.lit
			}

			p.skipNewlines()
			switch p.current().typ {
			case tokenSemicolon:
				p.pos++
			case tokenRBrace:
			default:
				return nil, fmt.Errorf("line %d: expected ; or } in include", p.current().line)
			}
		default:
			return nil, fmt.Errorf("line %d: expected import name", current.line)
		}
	}

parseSource:
	p.skipNewlines()
	if p.current().typ != tokenIdent || p.current().lit != "from" {
		return nil, fmt.Errorf("line %d: missing module source", p.current().line)
	}
	p.pos++
	p.skipNewlines()

	if p.current().typ != tokenString {
		return nil, fmt.Errorf("line %d: missing module source", p.current().line)
	}

	importNode.Source = p.current().lit
	p.pos++

	return importNode, nil
}

func (p *parser) parseWorkflowBlockDecl() (string, *WorkflowBlock, error) {
	if _, err := p.expectIdent("workflow"); err != nil {
		return "", nil, err
	}

	p.skipNewlines()
	name := ""
	if p.current().typ == tokenIdent {
		name = p.current().lit
		p.pos++
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return "", nil, err
	}

	block, err := p.parseWorkflowBlock()
	if err != nil {
		return "", nil, err
	}

	return name, block, nil
}

func (p *parser) parseWorkflowBlock() (*WorkflowBlock, error) {
	p.localScopes = append(p.localScopes, make(map[string]ChanExpr))
	defer func() {
		p.localScopes = p.localScopes[:len(p.localScopes)-1]
	}()

	block := &WorkflowBlock{Calls: []*Call{}}

	for {
		p.skipWorkflowSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			line := p.previous().line
			if line == 0 {
				line = current.line
			}
			return nil, fmt.Errorf("line %d: expected } to close workflow", line)
		case tokenRBrace:
			p.pos++
			return block, nil
		}

		if current.typ == tokenIdent && p.peek().typ == tokenAssign {
			if err := p.parseChannelAssignment(tokenNewline, tokenSemicolon, tokenRBrace, tokenEOF); err != nil {
				return nil, err
			}
			continue
		}

		calls, err := p.parseWorkflowStatement()
		if err != nil {
			return nil, err
		}
		block.Calls = append(block.Calls, calls...)
	}
}

func (p *parser) parseWorkflowStatement() ([]*Call, error) {
	if p.current().typ == tokenIdent && p.peek().typ == tokenLParen {
		call, err := p.parseCall()
		if err != nil {
			return nil, err
		}

		return []*Call{call}, nil
	}

	expr, err := p.parseChanExpr(tokenNewline, tokenSemicolon, tokenRBrace, tokenEOF)
	if err != nil {
		return nil, err
	}

	return desugarWorkflowPipe(expr)
}

func desugarWorkflowPipe(expr ChanExpr) ([]*Call, error) {
	pipe, ok := expr.(PipeExpr)
	if !ok {
		return nil, fmt.Errorf("workflow statements must be calls or channel pipelines")
	}
	if len(pipe.Stages) < 2 {
		return nil, fmt.Errorf("workflow pipelines require at least one process stage")
	}

	input := pipe.Stages[0]
	calls := make([]*Call, 0, len(pipe.Stages)-1)
	for stageIndex, stage := range pipe.Stages[1:] {
		target, ok := stage.(ChanRef)
		if !ok {
			return nil, fmt.Errorf("workflow pipe stage %d must be a bare identifier", stageIndex+2)
		}

		if target.Name == "view" && stageIndex == len(pipe.Stages)-2 {
			break
		}

		call := &Call{Target: target.Name, Args: []ChanExpr{input}}
		calls = append(calls, call)
		input = ChanRef{Name: target.Name + ".out"}
	}

	if len(calls) == 0 {
		return nil, fmt.Errorf("workflow pipelines require at least one runnable process stage")
	}

	return calls, nil
}

func (p *parser) parseCall() (*Call, error) {
	target, err := p.expectType(tokenIdent, "call target")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLParen, "("); err != nil {
		return nil, err
	}

	call := &Call{Target: target.lit}
	for {
		if p.current().typ == tokenRParen {
			p.pos++
			return call, nil
		}

		arg, err := p.parseChanExpr(tokenComma, tokenRParen)
		if err != nil {
			return nil, err
		}
		call.Args = append(call.Args, arg)

		switch p.current().typ {
		case tokenComma:
			p.pos++
		case tokenRParen:
			p.pos++
			return call, nil
		default:
			return nil, fmt.Errorf("line %d: expected , or )", p.current().line)
		}
	}
}

func (p *parser) parseChannelAssignment(terminators ...tokenType) error {
	name, err := p.expectType(tokenIdent, "channel variable name")
	if err != nil {
		return err
	}

	if _, err = p.expectType(tokenAssign, "="); err != nil {
		return err
	}

	value, err := p.parseChanExpr(terminators...)
	if err != nil {
		return err
	}
	if p.current().typ == tokenNewline || p.current().typ == tokenSemicolon {
		p.pos++
	}

	if scope := p.currentLocalScope(); scope != nil {
		scope[name.lit] = value
		return nil
	}

	p.assignments[name.lit] = value

	return nil
}

func (p *parser) parseChanExpr(terminators ...tokenType) (ChanExpr, error) {
	stage, err := p.parseChanStage(terminators...)
	if err != nil {
		return nil, err
	}

	stages := []ChanExpr{stage}
	for p.current().typ == tokenPipe {
		p.pos++
		nextStage, err := p.parseChanStage(terminators...)
		if err != nil {
			return nil, err
		}
		stages = append(stages, nextStage)
	}

	if len(stages) == 1 {
		return stages[0], nil
	}

	return PipeExpr{Stages: stages}, nil
}

func (p *parser) parseChanStage(terminators ...tokenType) (ChanExpr, error) {
	current := p.current()
	if hasTokenType(terminators, current.typ) {
		return nil, fmt.Errorf("line %d: expected channel expression", current.line)
	}

	var base ChanExpr
	if current.typ == tokenIdent && current.lit == "Channel" && p.peek().typ == tokenDot {
		factory, err := p.parseChannelFactory()
		if err != nil {
			return nil, err
		}
		base = factory
	} else {
		if current.typ != tokenIdent {
			return nil, fmt.Errorf("line %d: expected channel expression", current.line)
		}

		name := p.parseChannelRefName()
		if value, ok := p.resolveAssignment(name); ok {
			base = value
		} else {
			base = ChanRef{Name: name}
		}
	}

	return p.parseChannelOperators(base)
}

func hasTokenType(types []tokenType, target tokenType) bool {
	for _, current := range types {
		if current == target {
			return true
		}
	}

	return false
}

func (p *parser) currentLocalScope() map[string]ChanExpr {
	if len(p.localScopes) == 0 {
		return nil
	}

	return p.localScopes[len(p.localScopes)-1]
}

func (p *parser) resolveAssignment(name string) (ChanExpr, bool) {
	if scope := p.currentLocalScope(); scope != nil {
		if value, ok := scope[name]; ok {
			return value, true
		}
	}

	value, ok := p.assignments[name]

	return value, ok
}

func (p *parser) parseChannelFactory() (ChanExpr, error) {
	if _, err := p.expectIdent("Channel"); err != nil {
		return nil, err
	}
	if _, err := p.expectType(tokenDot, "."); err != nil {
		return nil, err
	}

	name, err := p.expectType(tokenIdent, "channel factory name")
	if err != nil {
		return nil, err
	}
	if _, err = p.expectType(tokenLParen, "("); err != nil {
		return nil, err
	}

	factory := ChannelFactory{Name: name.lit}
	for {
		if p.current().typ == tokenRParen {
			p.pos++
			return factory, nil
		}

		argTokens, err := p.readExprTokens(tokenComma, tokenRParen)
		if err != nil {
			return nil, err
		}
		expr, err := parseExprTokens(argTokens)
		if err != nil {
			return nil, wrapLineError(argTokens[0].line, err)
		}
		factory.Args = append(factory.Args, expr)

		switch p.current().typ {
		case tokenComma:
			p.pos++
		case tokenRParen:
			p.pos++
			return factory, nil
		default:
			return nil, fmt.Errorf("line %d: expected , or )", p.current().line)
		}
	}
}

func parseExprTokens(tokens []token) (Expr, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expected expression")
	}

	if expr, ok, err := parseBinaryExprTokens(tokens); ok || err != nil {
		return expr, err
	}

	if paramsPath, ok := parseParamsPath(tokens); ok {
		return ParamsExpr{Path: paramsPath}, nil
	}

	if variable, ok := parseVarExprTokens(tokens); ok {
		return variable, nil
	}

	if len(tokens) != 1 {
		return UnsupportedExpr{Text: expressionText(tokens)}, nil
	}

	tok := tokens[0]
	switch tok.typ {
	case tokenInt:
		value, err := strconv.Atoi(tok.lit)
		if err != nil {
			return nil, err
		}
		return IntExpr{Value: value}, nil
	case tokenString:
		return StringExpr{Value: tok.lit}, nil
	case tokenIdent:
		switch tok.lit {
		case "true":
			return BoolExpr{Value: true}, nil
		case "false":
			return BoolExpr{Value: false}, nil
		default:
			return StringExpr{Value: tok.lit}, nil
		}
	default:
		return nil, fmt.Errorf("unsupported expression %q", joinTokens(tokens))
	}
}

func parseBinaryExprTokens(tokens []token) (Expr, bool, error) {
	for _, operators := range [][]string{{">", "<"}, {"+", "-"}, {"*", "/"}} {
		depth := 0
		for index := len(tokens) - 1; index >= 0; index-- {
			current := tokens[index]
			switch current.typ {
			case tokenRParen:
				depth++
			case tokenLParen:
				depth--
			}

			if depth != 0 || current.typ != tokenSymbol || !stringInSlice(current.lit, operators) {
				continue
			}

			leftTokens := tokens[:index]
			rightTokens := tokens[index+1:]
			if len(leftTokens) == 0 || len(rightTokens) == 0 {
				return nil, false, fmt.Errorf("unsupported expression %q", expressionText(tokens))
			}

			leftExpr, err := parseExprTokens(leftTokens)
			if err != nil {
				return nil, true, err
			}

			rightExpr, err := parseExprTokens(rightTokens)
			if err != nil {
				return nil, true, err
			}

			return BinaryExpr{Left: leftExpr, Op: current.lit, Right: rightExpr}, true, nil
		}
	}

	return nil, false, nil
}

func parseVarExprTokens(tokens []token) (Expr, bool) {
	if len(tokens) == 0 || tokens[0].typ != tokenIdent {
		return nil, false
	}

	parts := []string{tokens[0].lit}
	for index := 1; index < len(tokens); index += 2 {
		if tokens[index].typ != tokenDot {
			return nil, false
		}
		if index+1 >= len(tokens) || tokens[index+1].typ != tokenIdent {
			return nil, false
		}
		parts = append(parts, tokens[index+1].lit)
	}

	if len(parts) == 1 {
		return VarExpr{Root: parts[0]}, true
	}

	return VarExpr{Root: parts[0], Path: strings.Join(parts[1:], ".")}, true
}

func expressionText(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	var builder strings.Builder
	for index, tok := range tokens {
		if index > 0 && needsSpaceBetween(tokens[index-1], tok) {
			builder.WriteByte(' ')
		}
		builder.WriteString(tok.lit)
	}

	return strings.TrimSpace(builder.String())
}

func needsSpaceBetween(prev, current token) bool {
	if prev.typ == tokenDot || current.typ == tokenDot {
		return false
	}

	if current.typ == tokenLParen {
		return false
	}

	if prev.typ == tokenLParen || current.typ == tokenRParen {
		return false
	}

	if prev.typ == tokenSymbol || current.typ == tokenSymbol {
		return true
	}

	if current.typ == tokenComma || current.typ == tokenColon || current.typ == tokenSemicolon {
		return false
	}

	if prev.typ == tokenComma || prev.typ == tokenColon || prev.typ == tokenSemicolon {
		return true
	}

	return true
}

func stringInSlice(target string, values []string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func (p *parser) parseChannelRefName() string {
	parts := []string{p.current().lit}
	p.pos++

	for p.current().typ == tokenDot {
		if p.peek().typ != tokenIdent || p.peekN(2).typ == tokenLParen || p.peekN(2).typ == tokenLBrace {
			break
		}

		p.pos++
		parts = append(parts, p.current().lit)
		p.pos++
	}

	return strings.Join(parts, ".")
}

func (p *parser) parseChannelOperators(base ChanExpr) (ChanExpr, error) {
	operators := []ChannelOperator{}

	for p.current().typ == tokenDot && p.peek().typ == tokenIdent {
		if p.peekN(2).typ != tokenLParen && p.peekN(2).typ != tokenLBrace {
			break
		}

		p.pos++
		name := p.current()
		p.pos++

		if _, ok := supportedChannelOperators[name.lit]; !ok {
			return nil, fmt.Errorf("line %d: unsupported operator: %s", name.line, name.lit)
		}

		operator, err := p.parseChannelOperator(name)
		if err != nil {
			return nil, err
		}
		operators = append(operators, operator)
	}

	if len(operators) == 0 {
		return base, nil
	}

	return ChannelChain{Source: base, Operators: operators}, nil
}

func (p *parser) parseChannelOperator(name token) (ChannelOperator, error) {
	operator := ChannelOperator{Name: name.lit}

	switch p.current().typ {
	case tokenLBrace:
		closure, err := p.parseClosureBody()
		if err != nil {
			return ChannelOperator{}, err
		}
		operator.Closure = closure
		return operator, nil
	case tokenLParen:
		channels, args, err := p.parseChannelOperatorArgs(name)
		if err != nil {
			return ChannelOperator{}, err
		}
		operator.Channels = channels
		operator.Args = args
		return operator, nil
	default:
		return ChannelOperator{}, fmt.Errorf("line %d: expected operator invocation", p.current().line)
	}
}

func (p *parser) parseChannelOperatorArgs(name token) ([]ChanExpr, []Expr, error) {
	if _, err := p.expectType(tokenLParen, "("); err != nil {
		return nil, nil, err
	}

	if p.current().typ == tokenRParen {
		p.pos++
		return nil, nil, nil
	}

	switch name.lit {
	case "join", "mix":
		channels := []ChanExpr{}
		for {
			channel, err := p.parseChanExpr(tokenComma, tokenRParen)
			if err != nil {
				return nil, nil, err
			}
			channels = append(channels, channel)

			switch p.current().typ {
			case tokenComma:
				p.pos++
			case tokenRParen:
				p.pos++
				return channels, nil, nil
			default:
				return nil, nil, fmt.Errorf("line %d: expected , or )", p.current().line)
			}
		}
	default:
		args := []Expr{}
		for {
			argTokens, err := p.readExprTokens(tokenComma, tokenRParen)
			if err != nil {
				return nil, nil, err
			}
			arg, err := parseExprTokens(argTokens)
			if err != nil {
				return nil, nil, wrapLineError(argTokens[0].line, err)
			}
			args = append(args, arg)

			switch p.current().typ {
			case tokenComma:
				p.pos++
			case tokenRParen:
				p.pos++
				return nil, args, nil
			default:
				return nil, nil, fmt.Errorf("line %d: expected , or )", p.current().line)
			}
		}
	}
}

func (p *parser) parseClosureBody() (string, error) {
	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return "", err
	}

	depth := 1
	tokens := []token{}
	for depth > 0 {
		current := p.current()
		if current.typ == tokenEOF {
			return "", fmt.Errorf("line %d: unterminated closure", current.line)
		}

		switch current.typ {
		case tokenLBrace:
			depth++
			tokens = append(tokens, current)
		case tokenRBrace:
			depth--
			if depth > 0 {
				tokens = append(tokens, current)
			}
		default:
			tokens = append(tokens, current)
		}

		p.pos++
	}

	return strings.TrimSpace(renderClosureTokens(tokens)), nil
}

func joinTokens(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		switch tok.typ {
		case tokenLBrace, tokenRBrace, tokenLParen, tokenRParen, tokenColon, tokenComma, tokenDot, tokenSemicolon, tokenAssign, tokenPipe:
			parts = append(parts, tok.lit)
		default:
			parts = append(parts, tok.lit)
		}
	}

	return strings.Join(parts, " ")
}

func renderClosureTokens(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	var builder strings.Builder
	for index, tok := range tokens {
		if index > 0 && needsSpaceBetween(tokens[index-1], tok) {
			builder.WriteByte(' ')
		}

		builder.WriteString(renderClosureToken(tok))
	}

	return builder.String()
}

func renderClosureToken(tok token) string {
	if tok.typ != tokenString {
		return tok.lit
	}

	return "'" + strings.ReplaceAll(tok.lit, "'", "\\'") + "'"
}

func (p *parser) parseDottedIdent() string {
	parts := []string{p.current().lit}
	p.pos++

	for p.current().typ == tokenDot {
		p.pos++
		if p.current().typ != tokenIdent {
			break
		}
		parts = append(parts, p.current().lit)
		p.pos++
	}

	return strings.Join(parts, ".")
}

func (p *parser) readExprTokens(terminators ...tokenType) ([]token, error) {
	start := p.current()
	tokens := []token{}
	depth := 0

	for {
		current := p.current()
		if current.typ == tokenEOF {
			if len(tokens) == 0 {
				return nil, fmt.Errorf("line %d: expected expression", start.line)
			}
			return tokens, nil
		}

		if depth == 0 && hasTokenType(terminators, current.typ) {
			if len(tokens) == 0 {
				return nil, fmt.Errorf("line %d: expected expression", current.line)
			}
			return tokens, nil
		}

		if current.typ == tokenLParen {
			depth++
		} else if current.typ == tokenRParen && depth > 0 {
			depth--
		}

		tokens = append(tokens, current)
		p.pos++
	}
}

func (p *parser) parseProcess() (*Process, error) {
	if _, err := p.expectIdent("process"); err != nil {
		return nil, err
	}

	name, err := p.expectType(tokenIdent, "process name")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	proc := &Process{
		Name:       name.lit,
		Directives: make(map[string]Expr),
		PublishDir: []*PublishDir{},
		Env:        make(map[string]string),
	}

	for {
		p.skipNewlines()
		current := p.current()
		switch current.typ {
		case tokenEOF:
			line := p.previous().line
			if line == 0 {
				line = current.line
			}
			return nil, fmt.Errorf("line %d: expected } to close process %q", line, proc.Name)
		case tokenRBrace:
			p.pos++
			return proc, nil
		case tokenIdent:
			label := current
			p.pos++
			if p.current().typ == tokenColon {
				p.pos++
				if err := p.parseSection(proc, label); err != nil {
					return nil, err
				}
				continue
			}

			if err := p.parseDirective(proc, label); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("line %d: unexpected token %q in process %q", current.line, current.lit, proc.Name)
		}
	}
}

func (p *parser) parseSection(proc *Process, label token) error {
	switch label.lit {
	case "input":
		decls, err := p.parseDeclarations()
		if err != nil {
			return err
		}
		proc.Input = append(proc.Input, decls...)
	case "output":
		decls, err := p.parseDeclarations()
		if err != nil {
			return err
		}
		proc.Output = append(proc.Output, decls...)
	case "script":
		script, err := p.parseScript()
		if err != nil {
			return err
		}
		proc.Script = script
	default:
		return fmt.Errorf("line %d: unsupported section %q", label.line, label.lit)
	}

	return nil
}

func (p *parser) parseDeclarations() ([]*Declaration, error) {
	decls := []*Declaration{}
	for {
		p.skipNewlines()
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenRBrace {
			return decls, nil
		}

		if current.typ == tokenIdent && p.peek().typ == tokenColon {
			return decls, nil
		}

		decl, err := p.parseDeclarationLine()
		if err != nil {
			return nil, err
		}
		decls = append(decls, decl)
	}
}

func (p *parser) parseDeclarationLine() (*Declaration, error) {
	lineTokens := p.readLineTokens()
	if len(lineTokens) == 0 {
		return nil, fmt.Errorf("line %d: expected declaration", p.current().line)
	}

	for _, tok := range lineTokens {
		if tok.typ == tokenIdent && tok.lit == "into" {
			return nil, fmt.Errorf("line %d: DSL 1 syntax is not supported", tok.line)
		}
	}

	decl := &Declaration{Kind: lineTokens[0].lit, Raw: joinTokens(lineTokens)}
	if len(lineTokens) == 2 && lineTokens[1].typ == tokenIdent {
		decl.Name = lineTokens[1].lit
		return decl, nil
	}

	if len(lineTokens) > 1 {
		expr, err := parseExprTokens(lineTokens[1:])
		if err != nil {
			return nil, err
		}
		decl.Expr = expr
	}

	return decl, nil
}

func (p *parser) parseScript() (string, error) {
	p.skipNewlines()
	current := p.current()
	if current.typ != tokenString {
		return "", fmt.Errorf("line %d: expected script string", current.line)
	}

	p.pos++
	p.discardLineRemainder()

	return current.lit, nil
}

func (p *parser) parseDirective(proc *Process, name token) error {
	args := p.readLineTokens()

	switch name.lit {
	case "cpus", "memory", "time", "disk":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Directives[name.lit] = expr
	case "container":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		strExpr, ok := expr.(StringExpr)
		if !ok {
			return fmt.Errorf("line %d: container expects a string literal", name.line)
		}
		proc.Container = strExpr.Value
	case "maxForks":
		value, err := parseIntValue(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.MaxForks = value
	case "errorStrategy":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		strExpr, ok := expr.(StringExpr)
		if !ok {
			return fmt.Errorf("line %d: errorStrategy expects a string literal", name.line)
		}
		proc.ErrorStrat = strExpr.Value
	case "maxRetries":
		value, err := parseIntValue(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.MaxRetries = value
	case "publishDir":
		publishDir, err := parsePublishDir(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.PublishDir = append(proc.PublishDir, publishDir)
	case "env":
		key, value, err := parseEnv(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Env[key] = value
	default:
		warnUnsupportedDirective(name)
	}

	return nil
}

func parseDirectiveExpr(name string, args []token) (Expr, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s requires an argument", name)
	}

	expr, err := parseExprTokens(args)
	if err != nil {
		return nil, err
	}

	if stringExpr, ok := expr.(StringExpr); ok {
		switch name {
		case "memory":
			value, err := parseMemoryValue(stringExpr.Value)
			if err != nil {
				return nil, err
			}
			return IntExpr{Value: value}, nil
		case "time":
			value, err := parseTimeValue(stringExpr.Value)
			if err != nil {
				return nil, err
			}
			return IntExpr{Value: value}, nil
		case "disk":
			value, err := parseDiskValue(stringExpr.Value)
			if err != nil {
				return nil, err
			}
			return IntExpr{Value: value}, nil
		}
	}

	return expr, nil
}

func wrapLineError(line int, err error) error {
	if err == nil {
		return nil
	}

	if strings.HasPrefix(err.Error(), "line ") {
		return err
	}

	return fmt.Errorf("line %d: %w", line, err)
}

func parseIntValue(tokens []token) (int, error) {
	expr, err := parseExprTokens(tokens)
	if err != nil {
		return 0, err
	}

	intExpr, ok := expr.(IntExpr)
	if !ok {
		return 0, fmt.Errorf("expected integer value")
	}

	return intExpr.Value, nil
}

func parsePublishDir(tokens []token) (*PublishDir, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("publishDir requires a path")
	}

	pathExpr, err := parseExprTokens([]token{tokens[0]})
	if err != nil {
		return nil, err
	}

	pathValue, ok := pathExpr.(StringExpr)
	if !ok {
		return nil, fmt.Errorf("publishDir path must be a string literal")
	}

	publishDir := &PublishDir{Path: pathValue.Value, Mode: "copy"}
	index := 1
	for index < len(tokens) {
		if tokens[index].typ == tokenComma {
			index++
		}
		if index >= len(tokens) {
			break
		}

		if index+2 >= len(tokens) || tokens[index].typ != tokenIdent || tokens[index+1].typ != tokenColon {
			return nil, fmt.Errorf("unsupported publishDir option %q", joinTokens(tokens[index:]))
		}

		key := tokens[index].lit
		valueExpr, err := parseExprTokens([]token{tokens[index+2]})
		if err != nil {
			return nil, err
		}

		valueString, ok := valueExpr.(StringExpr)
		if !ok {
			return nil, fmt.Errorf("publishDir option %q must be a string literal", key)
		}

		switch key {
		case "pattern":
			publishDir.Pattern = valueString.Value
		case "mode":
			publishDir.Mode = valueString.Value
		default:
			return nil, fmt.Errorf("unsupported publishDir option %q", key)
		}

		index += 3
	}

	return publishDir, nil
}

func parseEnv(tokens []token) (string, string, error) {
	if len(tokens) != 3 || tokens[0].typ != tokenIdent || tokens[1].typ != tokenColon {
		return "", "", fmt.Errorf("env expects KEY: 'value'")
	}

	valueExpr, err := parseExprTokens([]token{tokens[2]})
	if err != nil {
		return "", "", err
	}

	valueString, ok := valueExpr.(StringExpr)
	if !ok {
		return "", "", fmt.Errorf("env value must be a string literal")
	}

	return tokens[0].lit, valueString.Value, nil
}

func warnUnsupportedDirective(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: ignoring unsupported directive %q at line %d\n", name.lit, name.line)
}

func (p *parser) skipNewlines() {
	for p.current().typ == tokenNewline {
		p.pos++
	}
}

func (p *parser) skipWorkflowSeparators() {
	for {
		switch p.current().typ {
		case tokenNewline, tokenSemicolon:
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) readLineTokens() []token {
	lineTokens := []token{}
	for {
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenNewline || current.typ == tokenRBrace {
			break
		}
		lineTokens = append(lineTokens, current)
		p.pos++
	}
	if p.current().typ == tokenNewline {
		p.pos++
	}

	return lineTokens
}

func (p *parser) discardLineRemainder() {
	for p.current().typ != tokenEOF && p.current().typ != tokenNewline && p.current().typ != tokenRBrace {
		p.pos++
	}
	if p.current().typ == tokenNewline {
		p.pos++
	}
}

func (p *parser) expectType(expected tokenType, description string) (token, error) {
	current := p.current()
	if current.typ != expected {
		return token{}, fmt.Errorf("line %d: expected %s", current.line, description)
	}
	p.pos++

	return current, nil
}

func (p *parser) expectIdent(expected string) (token, error) {
	current := p.current()
	if current.typ != tokenIdent || current.lit != expected {
		return token{}, fmt.Errorf("line %d: expected %q", current.line, expected)
	}
	p.pos++

	return current, nil
}

func (p *parser) current() token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos]
}

func (p *parser) peek() token {
	if p.pos+1 >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos+1]
}

func (p *parser) peekN(offset int) token {
	if p.pos+offset >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos+offset]
}

func (p *parser) previous() token {
	if p.pos == 0 {
		return token{}
	}

	return p.tokens[p.pos-1]
}

// Parse parses a Nextflow DSL 2 file and returns the AST.
func Parse(r io.Reader) (*Workflow, error) {
	input, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	tokens, err := lex(string(input))
	if err != nil {
		return nil, err
	}

	return newParser(tokens).parseWorkflow()
}

func parseMemoryValue(value string) (int, error) {
	matches := memoryRE.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("unsupported memory value %q", value)
	}

	amount, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, err
	}

	unit := strings.ToLower(matches[2])
	if unit == "gb" || unit == "g" {
		return amount * 1024, nil
	}

	return amount, nil
}

func parseDiskValue(value string) (int, error) {
	matches := diskRE.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("unsupported disk value %q", value)
	}

	return strconv.Atoi(matches[1])
}

func parseTimeValue(value string) (int, error) {
	matches := timeRE.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("unsupported time value %q", value)
	}

	amount, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, err
	}

	switch strings.ToLower(matches[2]) {
	case "m", "min", "mins", "minute", "minutes":
		return amount, nil
	case "h", "hr", "hrs", "hour", "hours":
		return amount * 60, nil
	case "d", "day", "days":
		return amount * 24 * 60, nil
	default:
		return 0, fmt.Errorf("unsupported time unit %q", matches[2])
	}
}
