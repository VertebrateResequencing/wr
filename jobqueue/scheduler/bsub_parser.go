// Copyright © 2025 Genome Research Limited
// Author: Michael Woolnough <mw31@sanger.ac.uk>
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package scheduler

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"vimagination.zapto.org/parser"
)

const (
	whitespace = " \t\n\r"
	letter     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_-"
	digit      = "0123456789"
)

var (
	ErrInvalidOperator       = errors.New("invalid operator")
	ErrInvalidGroupClosing   = errors.New("invalid group closing")
	ErrInvalidPrimary        = errors.New("invalid primary")
	ErrMissingClosingParen   = errors.New("missing closing paren")
	ErrMissingClosingBracket = errors.New("missing closing bracket")
	ErrMissingClosingBrace   = errors.New("missing closing brace")
)

const (
	TokenWhitespace parser.TokenType = iota
	TokenWord
	TokenNumber
	TokenString
	TokenOperator
)

type state struct {
	depth []rune
}

func (s *state) main(t *parser.Tokeniser) (parser.Token, parser.TokenFunc) {
	if t.Peek() == -1 {
		if len(s.depth) > 0 {
			return t.ReturnError(io.ErrUnexpectedEOF)
		}

		return t.Done()
	}

	if t.Accept(whitespace) {
		t.AcceptRun(whitespace)

		return t.Return(TokenWhitespace, s.main)
	}

	if t.Accept(letter) {
		t.AcceptRun(letter + digit)

		return t.Return(TokenWord, s.main)
	}

	if t.Accept(digit) {
		return s.number(t)
	}

	return s.stringOrOperator(t)
}

func (s *state) number(t *parser.Tokeniser) (parser.Token, parser.TokenFunc) {
	t.AcceptRun(digit)

	if t.Accept(".") {
		t.AcceptRun(digit)
	}

	t.AcceptRun(letter)

	return t.Return(TokenNumber, s.main)
}

func (s *state) stringOrOperator(t *parser.Tokeniser) (parser.Token, parser.TokenFunc) {
	c := t.Next()

	if c == '"' || c == '\'' {
		return s.string(t, c)
	}

	return s.operator(t, c)
}

func (s *state) operator(t *parser.Tokeniser, c rune) (parser.Token, parser.TokenFunc) { //nolint:gocyclo,cyclop
	switch c {
	case '[', '(', '{':
		s.depth = append(s.depth, inverseGrouping(c))
	case ']', ')', '}':
		if l := len(s.depth); l == 0 || s.depth[l-1] != c {
			return t.ReturnError(ErrInvalidGroupClosing)
		}

		s.depth = s.depth[:len(s.depth)-1]
	case '!':
		if !t.Accept("=") {
			return t.Return(TokenWord, s.main)
		}
	case ':', ',', '/', '+', '-', '*':
	case '=', '>', '<':
		t.Accept("=")
	case '&', '|':
		if !t.Accept(string(c)) {
			return t.ReturnError(ErrInvalidOperator)
		}
	default:
		return t.ReturnError(ErrInvalidOperator)
	}

	return t.Return(TokenOperator, s.main)
}

func inverseGrouping(c rune) rune {
	switch c {
	case '[':
		return ']'
	case '(':
		return ')'
	case '{':
		return '}'
	}

	return -1
}

func (s *state) string(t *parser.Tokeniser, c rune) (parser.Token, parser.TokenFunc) {
	for {
		switch t.ExceptRun("\\" + string(c)) {
		case '\\':
			t.Next()
			t.Next()
		case c:
			t.Next()

			return t.Return(TokenString, s.main)
		default:
			return t.ReturnError(io.ErrUnexpectedEOF)
		}
	}
}

type Primary struct {
	Name    *parser.Token
	Literal *parser.Token
	Parens  []Clause
	Braces  []Clause
}

func (py *Primary) parse(p *parser.Parser) error {
	switch tk := p.Peek(); tk.Type {
	case TokenWord:
		p.Get()
		p.Next()

		py.Name = &p.Get()[0]
	case TokenNumber, TokenString:
		p.Get()
		p.Next()

		py.Literal = &p.Get()[0]
	case TokenOperator:
		return py.parseGrouping(p, tk)
	default:
		return ErrInvalidPrimary
	}

	return nil
}

func (py *Primary) parseGrouping(p *parser.Parser, tk parser.Token) error {
	var (
		closeChar string
		arr       *[]Clause
	)

	switch tk.Data {
	case "(":
		closeChar = ")"
		arr = &py.Parens
	case "{":
		closeChar = "}"
		arr = &py.Braces
	default:
		return fmt.Errorf("Primary: %w", ErrInvalidPrimary)
	}

	p.Next()
	p.AcceptRun(TokenWhitespace)

	return parseClauses(p, closeChar, arr)
}

func parseClauses(p *parser.Parser, closeChar string, arr *[]Clause) error {
	for {
		var c Clause

		if err := c.parse(p); err != nil {
			return fmt.Errorf("Primary: %w", err)
		}

		*arr = append(*arr, c)

		p.AcceptRun(TokenWhitespace)

		if p.AcceptToken(parser.Token{Type: TokenOperator, Data: closeChar}) {
			return nil
		}
	}
}

func (py *Primary) toString(sb *strings.Builder) {
	switch {
	case py.Name != nil:
		sb.WriteString(py.Name.Data)
	case py.Literal != nil:
		sb.WriteString(py.Literal.Data)
	default:
		py.toStringClause(sb)
	}
}

func (py *Primary) toStringClause(sb *strings.Builder) {
	var (
		chars   string
		clauses []Clause
	)

	if len(py.Parens) > 0 {
		chars = "()"
		clauses = py.Parens
	} else if len(py.Braces) > 0 {
		chars = "{}"
		clauses = py.Braces
	}

	sb.WriteString(chars[:1])
	clauses[0].toString(sb)

	for _, c := range clauses[1:] {
		sb.WriteString(" ")
		c.toString(sb)
	}

	sb.WriteString(chars[1:])
}

type Call struct {
	Primary Primary
	Call    *Logic
}

func (c *Call) parse(p *parser.Parser) error {
	if err := c.Primary.parse(p); err != nil {
		return fmt.Errorf("Call: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if p.AcceptToken(parser.Token{Type: TokenOperator, Data: "("}) { //nolint:nestif
		c.Call = new(Logic)

		if err := c.Call.parse(p); err != nil {
			return fmt.Errorf("Call: %w", err)
		}

		p.AcceptRun(TokenWhitespace)

		if !p.AcceptToken(parser.Token{Type: TokenOperator, Data: ")"}) {
			return fmt.Errorf("Call: %w", ErrMissingClosingParen)
		}
	}

	return nil
}

func (c *Call) toString(sb *strings.Builder) {
	c.Primary.toString(sb)

	if c.Call != nil {
		sb.WriteString("(")
		c.Call.toString(sb)
		sb.WriteString(")")
	}
}

type BinaryOperator uint8

const (
	BinaryNone BinaryOperator = iota
	BinaryEquals
	BinaryNotEquals
	BinaryDoubleEquals
	BinaryLessThan
	BinaryLessThanOrEqual
	BinaryGreaterThan
	BinaryGreaterThanOrEqual
	BinaryAdd
	BinarySubract
	BinaryMultiply
)

func (b BinaryOperator) toString(sb *strings.Builder) { //nolint:funlen,gocyclo,cyclop
	var toWrite string

	switch b {
	case BinaryEquals:
		toWrite = "="
	case BinaryNotEquals:
		toWrite = "!="
	case BinaryDoubleEquals:
		toWrite = "=="
	case BinaryLessThan:
		toWrite = " < "
	case BinaryLessThanOrEqual:
		toWrite = " <= "
	case BinaryGreaterThan:
		toWrite = " > "
	case BinaryGreaterThanOrEqual:
		toWrite = " >= "
	case BinaryAdd:
		toWrite = " + "
	case BinarySubract:
		toWrite = " + "
	case BinaryMultiply:
		toWrite = " * "
	default:
	}

	sb.WriteString(toWrite)
}

type Binary struct {
	Call     Call
	Operator BinaryOperator
	Binary   *Binary
}

func (b *Binary) parse(p *parser.Parser) error { //nolint:dupl
	if err := b.Call.parse(p); err != nil {
		return fmt.Errorf("Binary: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if tk := p.Peek(); tk.Type == TokenOperator { //nolint:nestif
		if b.Operator = parseBinaryOperator(tk); b.Operator == BinaryNone {
			return nil
		}

		p.Next()
		p.AcceptRun(TokenWhitespace)

		b.Binary = new(Binary)

		if err := b.Binary.parse(p); err != nil {
			return fmt.Errorf("Binary: %w", err)
		}
	}

	return nil
}

func parseBinaryOperator(tk parser.Token) BinaryOperator { //nolint:funlen,gocyclo,cyclop
	switch tk.Data {
	case "=":
		return BinaryEquals
	case "!=":
		return BinaryNotEquals
	case "==":
		return BinaryDoubleEquals
	case "<":
		return BinaryLessThan
	case "<=":
		return BinaryLessThanOrEqual
	case ">":
		return BinaryGreaterThan
	case ">=":
		return BinaryGreaterThanOrEqual
	case "+":
		return BinaryAdd
	case "-":
		return BinarySubract
	case "*":
		return BinaryMultiply
	default:
		return BinaryNone
	}
}

func (b *Binary) toString(sb *strings.Builder) {
	b.Call.toString(sb)

	if b.Operator != BinaryNone && b.Binary != nil {
		b.Operator.toString(sb)
		b.Binary.toString(sb)
	}
}

type LogicOperator uint8

const (
	LogicNone LogicOperator = iota
	LogicAnd
	LogicOr
	LogicColon
	LogicComma
	LogicSlash
)

func (l LogicOperator) toString(sb *strings.Builder) {
	var toWrite string

	switch l {
	case LogicAnd:
		toWrite = " && "
	case LogicOr:
		toWrite = " || "
	case LogicColon:
		toWrite = ":"
	case LogicComma:
		toWrite = ", "
	case LogicSlash:
		toWrite = "/"
	default:
	}

	sb.WriteString(toWrite)
}

type Logic struct {
	Binary   Binary
	Operator LogicOperator
	Ext      *Logic
}

func (l *Logic) parse(p *parser.Parser) error { //nolint:dupl
	if err := l.Binary.parse(p); err != nil {
		return fmt.Errorf("Logic: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if tk := p.Peek(); tk.Type == TokenOperator { //nolint:nestif
		if l.Operator = parseLogicOperator(tk); l.Operator == LogicNone {
			return nil
		}

		p.Next()
		p.AcceptRun(TokenWhitespace)

		l.Ext = new(Logic)

		if err := l.Ext.parse(p); err != nil {
			return fmt.Errorf("Logic: %w", err)
		}
	}

	return nil
}

func parseLogicOperator(tk parser.Token) LogicOperator {
	switch tk.Data {
	case "&&":
		return LogicAnd
	case "||":
		return LogicOr
	case ":":
		return LogicColon
	case ",":
		return LogicComma
	case "/":
		return LogicSlash
	default:
		return LogicNone
	}
}

func (l *Logic) toString(sb *strings.Builder) {
	l.Binary.toString(sb)

	if l.Operator != LogicNone && l.Ext != nil {
		l.Operator.toString(sb)
		l.Ext.toString(sb)
	}
}

type Clause struct {
	Logic     Logic
	Condition *Logic
}

func (c *Clause) parse(p *parser.Parser) error {
	if err := c.Logic.parse(p); err != nil {
		return fmt.Errorf("Clause: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if p.AcceptToken(parser.Token{Type: TokenOperator, Data: "["}) {
		return c.parseCondition(p)
	}

	return nil
}

func (c *Clause) parseCondition(p *parser.Parser) error {
	p.AcceptRun(TokenWhitespace)

	c.Condition = new(Logic)

	if err := c.Condition.parse(p); err != nil {
		return fmt.Errorf("Clause: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if !p.AcceptToken(parser.Token{Type: TokenOperator, Data: "]"}) {
		return fmt.Errorf("Clause: %w", ErrMissingClosingBracket)
	}

	return nil
}

func (c *Clause) toString(sb *strings.Builder) {
	c.Logic.toString(sb)

	if c.Condition != nil {
		sb.WriteString("[")
		c.Condition.toString(sb)
		sb.WriteString("]")
	}
}

type Top struct {
	Clauses []Clause
}

func (t *Top) parse(p *parser.Parser) error {
	p.AcceptRun(TokenWhitespace)

	for p.Peek().Type >= 0 {
		var c Clause

		if err := c.parse(p); err != nil {
			return fmt.Errorf("Top: %w", err)
		}

		t.Clauses = append(t.Clauses, c)

		p.AcceptRun(TokenWhitespace)
	}

	return nil
}

func (t *Top) toString(sb *strings.Builder) {
	if len(t.Clauses) == 0 {
		return
	}

	t.Clauses[0].toString(sb)

	for _, c := range t.Clauses[1:] {
		sb.WriteString(" ")
		c.toString(sb)
	}
}
