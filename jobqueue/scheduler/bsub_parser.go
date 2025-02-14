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
	letter     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_"
	digit      = "0123456789"
)

const (
	tokenWhitespace parser.TokenType = iota
	tokenWord
	tokenNumber
	tokenString
	tokenOperator
)

var (
	errInvalidOperator       = errors.New("invalid operator")
	errInvalidGroupClosing   = errors.New("invalid group closing")
	errInvalidPrimary        = errors.New("invalid primary")
	errMissingClosingParen   = errors.New("missing closing paren")
	errMissingClosingBracket = errors.New("missing closing bracket")
	errMissingClosingBrace   = errors.New("missing closing brace")
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

		return t.Return(tokenWhitespace, s.main)
	}

	if t.Accept(letter) {
		t.AcceptRun(letter + digit)

		return t.Return(tokenWord, s.main)
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

	return t.Return(tokenNumber, s.main)
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
			return t.ReturnError(errInvalidGroupClosing)
		}

		s.depth = s.depth[:len(s.depth)-1]
	case '!':
		if !t.Accept("=") {
			return t.Return(tokenWord, s.main)
		}
	case ':', ',', '/', '+', '*', '@':
	case '=', '>', '<':
		t.Accept("=")
	case '&', '|':
		if !t.Accept(string(c)) {
			return t.ReturnError(errInvalidOperator)
		}
	default:
		return t.ReturnError(errInvalidOperator)
	}

	return t.Return(tokenOperator, s.main)
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

			return t.Return(tokenString, s.main)
		default:
			return t.ReturnError(io.ErrUnexpectedEOF)
		}
	}
}

type primary struct {
	Name    *parser.Token
	Literal *parser.Token
	Parens  []clause
	Braces  []clause
}

func (py *primary) parse(p *parser.Parser) error {
	switch tk := p.Peek(); tk.Type {
	case tokenWord:
		p.Get()
		p.Next()

		py.Name = &p.Get()[0]
	case tokenNumber, tokenString:
		p.Get()
		p.Next()

		py.Literal = &p.Get()[0]
	case tokenOperator:
		return py.parseGrouping(p, tk)
	default:
		return errInvalidPrimary
	}

	return nil
}

func (py *primary) parseGrouping(p *parser.Parser, tk parser.Token) error {
	var (
		closeChar string
		arr       *[]clause
	)

	switch tk.Data {
	case "(":
		closeChar = ")"
		arr = &py.Parens
	case "{":
		closeChar = "}"
		arr = &py.Braces
	default:
		return fmt.Errorf("Primary: %w", errInvalidPrimary)
	}

	p.Next()
	p.AcceptRun(tokenWhitespace)

	return parseClauses(p, closeChar, arr)
}

func parseClauses(p *parser.Parser, closeChar string, arr *[]clause) error {
	for {
		var c clause

		if err := c.parse(p); err != nil {
			return fmt.Errorf("Primary: %w", err)
		}

		*arr = append(*arr, c)

		p.AcceptRun(tokenWhitespace)

		if p.AcceptToken(parser.Token{Type: tokenOperator, Data: closeChar}) {
			return nil
		}
	}
}

func (py *primary) toString(sb *strings.Builder) {
	switch {
	case py.Name != nil:
		sb.WriteString(py.Name.Data)
	case py.Literal != nil:
		sb.WriteString(py.Literal.Data)
	default:
		py.toStringClause(sb)
	}
}

func (py *primary) toStringClause(sb *strings.Builder) {
	var (
		chars   string
		clauses []clause
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

type call struct {
	Primary primary
	Call    *logic
}

func (c *call) parse(p *parser.Parser) error {
	if err := c.Primary.parse(p); err != nil {
		return fmt.Errorf("Call: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if p.AcceptToken(parser.Token{Type: tokenOperator, Data: "("}) { //nolint:nestif
		c.Call = new(logic)

		if err := c.Call.parse(p); err != nil {
			return fmt.Errorf("Call: %w", err)
		}

		p.AcceptRun(tokenWhitespace)

		if !p.AcceptToken(parser.Token{Type: tokenOperator, Data: ")"}) {
			return fmt.Errorf("Call: %w", errMissingClosingParen)
		}
	}

	return nil
}

func (c *call) toString(sb *strings.Builder) {
	c.Primary.toString(sb)

	if c.Call != nil {
		sb.WriteString("(")
		c.Call.toString(sb)
		sb.WriteString(")")
	}
}

type binaryOperator uint8

const (
	binaryNone binaryOperator = iota
	binaryEquals
	binaryNotEquals
	binaryDoubleEquals
	binaryLessThan
	binaryLessThanOrEqual
	binaryGreaterThan
	binaryGreaterThanOrEqual
	binaryAdd
	binaryMultiply
	binaryDelay
)

func (b binaryOperator) toString(sb *strings.Builder) { //nolint:funlen,gocyclo,cyclop
	var toWrite string

	switch b {
	case binaryEquals:
		toWrite = "="
	case binaryNotEquals:
		toWrite = "!="
	case binaryDoubleEquals:
		toWrite = "=="
	case binaryLessThan:
		toWrite = " < "
	case binaryLessThanOrEqual:
		toWrite = " <= "
	case binaryGreaterThan:
		toWrite = " > "
	case binaryGreaterThanOrEqual:
		toWrite = " >= "
	case binaryAdd:
		toWrite = " + "
	case binaryMultiply:
		toWrite = " * "
	case binaryDelay:
		toWrite = "@"
	default:
	}

	sb.WriteString(toWrite)
}

type binary struct {
	Call     call
	Operator binaryOperator
	Binary   *binary
}

func (b *binary) parse(p *parser.Parser) error { //nolint:dupl
	if err := b.Call.parse(p); err != nil {
		return fmt.Errorf("Binary: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if tk := p.Peek(); tk.Type == tokenOperator { //nolint:nestif
		if b.Operator = parseBinaryOperator(tk); b.Operator == binaryNone {
			return nil
		}

		p.Next()
		p.AcceptRun(tokenWhitespace)

		b.Binary = new(binary)

		if err := b.Binary.parse(p); err != nil {
			return fmt.Errorf("Binary: %w", err)
		}
	}

	return nil
}

func parseBinaryOperator(tk parser.Token) binaryOperator { //nolint:funlen,gocyclo,cyclop
	switch tk.Data {
	case "=":
		return binaryEquals
	case "!=":
		return binaryNotEquals
	case "==":
		return binaryDoubleEquals
	case "<":
		return binaryLessThan
	case "<=":
		return binaryLessThanOrEqual
	case ">":
		return binaryGreaterThan
	case ">=":
		return binaryGreaterThanOrEqual
	case "+":
		return binaryAdd
	case "*":
		return binaryMultiply
	case "@":
		return binaryDelay
	default:
		return binaryNone
	}
}

func (b *binary) toString(sb *strings.Builder) {
	b.Call.toString(sb)

	if b.Operator != binaryNone && b.Binary != nil {
		b.Operator.toString(sb)
		b.Binary.toString(sb)
	}
}

type logicOperator uint8

const (
	logicNone logicOperator = iota
	logicAnd
	logicOr
	logicColon
	logicComma
	logicSlash
)

func (l logicOperator) toString(sb *strings.Builder) {
	var toWrite string

	switch l {
	case logicAnd:
		toWrite = " && "
	case logicOr:
		toWrite = " || "
	case logicColon:
		toWrite = ":"
	case logicComma:
		toWrite = ", "
	case logicSlash:
		toWrite = "/"
	default:
	}

	sb.WriteString(toWrite)
}

type logic struct {
	Binary   binary
	Operator logicOperator
	Ext      *logic
}

func (l *logic) parse(p *parser.Parser) error { //nolint:dupl
	if err := l.Binary.parse(p); err != nil {
		return fmt.Errorf("Logic: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if tk := p.Peek(); tk.Type == tokenOperator { //nolint:nestif
		if l.Operator = parseLogicOperator(tk); l.Operator == logicNone {
			return nil
		}

		p.Next()
		p.AcceptRun(tokenWhitespace)

		l.Ext = new(logic)

		if err := l.Ext.parse(p); err != nil {
			return fmt.Errorf("Logic: %w", err)
		}
	}

	return nil
}

func parseLogicOperator(tk parser.Token) logicOperator {
	switch tk.Data {
	case "&&":
		return logicAnd
	case "||":
		return logicOr
	case ":":
		return logicColon
	case ",":
		return logicComma
	case "/":
		return logicSlash
	default:
		return logicNone
	}
}

func (l *logic) toString(sb *strings.Builder) {
	l.Binary.toString(sb)

	if l.Operator != logicNone && l.Ext != nil {
		l.Operator.toString(sb)
		l.Ext.toString(sb)
	}
}

type clause struct {
	Logic     logic
	Condition *logic
}

func (c *clause) parse(p *parser.Parser) error {
	if err := c.Logic.parse(p); err != nil {
		return fmt.Errorf("Clause: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if p.AcceptToken(parser.Token{Type: tokenOperator, Data: "["}) {
		return c.parseCondition(p)
	}

	return nil
}

func (c *clause) parseCondition(p *parser.Parser) error {
	p.AcceptRun(tokenWhitespace)

	c.Condition = new(logic)

	if err := c.Condition.parse(p); err != nil {
		return fmt.Errorf("Clause: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if !p.AcceptToken(parser.Token{Type: tokenOperator, Data: "]"}) {
		return fmt.Errorf("Clause: %w", errMissingClosingBracket)
	}

	return nil
}

func (c *clause) toString(sb *strings.Builder) {
	c.Logic.toString(sb)

	if c.Condition != nil {
		sb.WriteString("[")
		c.Condition.toString(sb)
		sb.WriteString("]")
	}
}

type top struct {
	Clauses []clause
}

func (t *top) parse(p *parser.Parser) error {
	p.AcceptRun(tokenWhitespace)

	for p.Peek().Type >= 0 {
		var c clause

		if err := c.parse(p); err != nil {
			return fmt.Errorf("Top: %w", err)
		}

		t.Clauses = append(t.Clauses, c)

		p.AcceptRun(tokenWhitespace)
	}

	return nil
}

func (t *top) toString(sb *strings.Builder) {
	if len(t.Clauses) == 0 {
		return
	}

	t.Clauses[0].toString(sb)

	for _, c := range t.Clauses[1:] {
		sb.WriteString(" ")
		c.toString(sb)
	}
}
