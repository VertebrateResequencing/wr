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

	"vimagination.zapto.org/parser"
)

const (
	whitespace = " \t\n\r"
	letter     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_-"
	digit      = "0123456789"
)

var (
	ErrBad                   = errors.New("bad")
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
		t.AcceptRun(digit)

		if t.Accept(".") {
			t.AcceptRun(digit)
		}

		t.AcceptRun(letter)

		return t.Return(TokenNumber, s.main)
	}

	c := t.Next()

	if c == '"' || c == '\'' {
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

	switch c {
	case '[':
		s.depth = append(s.depth, ']')
	case '(':
		s.depth = append(s.depth, ')')
	case '{':
		s.depth = append(s.depth, '}')
	case ']', ')', '}':
		if l := len(s.depth); l == 0 || s.depth[l-1] != c {
			return t.ReturnError(ErrBad)
		}

		s.depth = s.depth[:len(s.depth)-1]
	case '!':
		if !t.Accept("=") {
			return t.ReturnError(ErrBad)
		}
	case ':', ',', '/', '+', '-', '*':
	case '=', '>', '<':
		t.Accept("=")
	case '&', '|':
		if !t.Accept(string(c)) {
			return t.ReturnError(ErrBad)
		}
	default:
		return t.ReturnError(ErrBad)
	}

	return t.Return(TokenOperator, s.main)
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
		var (
			close string
			arr   *[]Clause
		)

		switch tk.Data {
		case "(":
			close = ")"
			arr = &py.Parens
		case "{":
			close = "}"
			arr = &py.Braces
		default:
			return fmt.Errorf("Primary: %w", ErrInvalidPrimary)
		}

		p.Next()
		p.AcceptRun(TokenWhitespace)

		for {
			var c Clause

			if err := c.parse(p); err != nil {
				return fmt.Errorf("Primary: %w", err)
			}

			*arr = append(*arr, c)

			p.AcceptRun(TokenWhitespace)

			if p.AcceptToken(parser.Token{Type: TokenOperator, Data: close}) {
				break
			}
		}
	}

	return nil
}

func (p *Primary) print(w io.Writer) error {
	if p.Name != nil {
		_, err := io.WriteString(w, p.Name.Data)

		return err
	}

	if p.Literal != nil {
		_, err := io.WriteString(w, p.Literal.Data)

		return err
	}

	if len(p.Parens) > 0 {
		if _, err := io.WriteString(w, "("); err != nil {
			return err
		}

		if err := p.Parens[0].print(w); err != nil {
			return err
		}

		for _, c := range p.Parens[1:] {
			if _, err := io.WriteString(w, " "); err != nil {
				return err
			}

			if err := c.print(w); err != nil {
				return err
			}
		}

		if _, err := io.WriteString(w, ")"); err != nil {
			return err
		}
	}

	if len(p.Braces) > 0 {
		if _, err := io.WriteString(w, "{"); err != nil {
			return err
		}

		if err := p.Braces[0].print(w); err != nil {
			return err
		}

		for _, c := range p.Braces[1:] {
			if _, err := io.WriteString(w, " "); err != nil {
				return err
			}

			if err := c.print(w); err != nil {
				return err
			}
		}

		if _, err := io.WriteString(w, "}"); err != nil {
			return err
		}
	}

	return ErrBad
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

	if p.AcceptToken(parser.Token{Type: TokenOperator, Data: "("}) {
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

func (c *Call) print(w io.Writer) error {
	if err := c.Primary.print(w); err != nil {
		return err
	}

	if c.Call != nil {
		if _, err := io.WriteString(w, "("); err != nil {
			return err
		}

		if err := c.Call.print(w); err != nil {
			return err
		}

		if _, err := io.WriteString(w, ")"); err != nil {
			return err
		}
	}

	return nil
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

func (b BinaryOperator) print(w io.Writer) error {
	var print string

	switch b {
	case BinaryEquals:
		print = "="
	case BinaryNotEquals:
		print = "!="
	case BinaryDoubleEquals:
		print = "=="
	case BinaryLessThan:
		print = " < "
	case BinaryLessThanOrEqual:
		print = " <= "
	case BinaryGreaterThan:
		print = " > "
	case BinaryGreaterThanOrEqual:
		print = " >= "
	case BinaryAdd:
		print = " + "
	case BinarySubract:
		print = " - "
	case BinaryMultiply:
		print = " + "
	default:
		return nil
	}

	_, err := io.WriteString(w, print)

	return err
}

type Binary struct {
	Call     Call
	Operator BinaryOperator
	Binary   *Call
}

func (b *Binary) parse(p *parser.Parser) error {
	if err := b.Call.parse(p); err != nil {
		return fmt.Errorf("Binary: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if tk := p.Peek(); tk.Type == TokenOperator {
		switch tk.Data {
		case "=":
			b.Operator = BinaryEquals
		case "!=":
			b.Operator = BinaryNotEquals
		case "==":
			b.Operator = BinaryDoubleEquals
		case "<":
			b.Operator = BinaryLessThan
		case "<=":
			b.Operator = BinaryLessThanOrEqual
		case ">":
			b.Operator = BinaryGreaterThan
		case ">=":
			b.Operator = BinaryGreaterThanOrEqual
		case "+":
			b.Operator = BinaryAdd
		case "-":
			b.Operator = BinarySubract
		case "*":
			b.Operator = BinaryMultiply
		default:
			return nil
		}

		p.Next()
		p.AcceptRun(TokenWhitespace)

		b.Binary = new(Call)

		if err := b.Binary.parse(p); err != nil {
			return fmt.Errorf("Binary: %w", err)
		}
	}

	return nil
}

func (b *Binary) print(w io.Writer) error {
	if err := b.Call.print(w); err != nil {
		return err
	}

	if b.Operator != BinaryNone && b.Binary != nil {
		if err := b.Operator.print(w); err != nil {
			return err
		}

		if err := b.Binary.print(w); err != nil {
			return err
		}
	}

	return nil
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

func (l LogicOperator) print(w io.Writer) error {
	var print string

	switch l {
	case LogicAnd:
		print = " && "
	case LogicOr:
		print = " || "
	case LogicColon:
		print = ":"
	case LogicComma:
		print = ", "
	case LogicSlash:
		print = "/"
	default:
		return nil
	}

	_, err := io.WriteString(w, print)

	return err
}

type Logic struct {
	Binary   Binary
	Operator LogicOperator
	Ext      *Logic
}

func (l *Logic) parse(p *parser.Parser) error {
	if err := l.Binary.parse(p); err != nil {
		return fmt.Errorf("Logic: %w", err)
	}

	p.AcceptRun(TokenWhitespace)

	if tk := p.Peek(); tk.Type == TokenOperator {
		switch tk.Data {
		case "&&":
			l.Operator = LogicAnd
		case "||":
			l.Operator = LogicOr
		case ":":
			l.Operator = LogicColon
		case ",":
			l.Operator = LogicComma
		case "/":
			l.Operator = LogicSlash
		default:
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

func (l *Logic) print(w io.Writer) error {
	if err := l.Binary.print(w); err != nil {
		return err
	}

	if l.Operator != LogicNone && l.Ext != nil {
		if err := l.Operator.print(w); err != nil {
			return err
		}

		if err := l.Ext.print(w); err != nil {
			return err
		}
	}

	return nil
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
		p.AcceptRun(TokenWhitespace)

		c.Condition = new(Logic)

		if err := c.Condition.parse(p); err != nil {
			return fmt.Errorf("Clause: %w", err)
		}

		p.AcceptRun(TokenWhitespace)

		if !p.AcceptToken(parser.Token{Type: TokenOperator, Data: "]"}) {
			return fmt.Errorf("Clause: %w", ErrMissingClosingBracket)
		}
	}

	return nil
}

func (c *Clause) print(w io.Writer) error {
	if err := c.Logic.print(w); err != nil {
		return err
	}

	if c.Condition != nil {
		if _, err := io.WriteString(w, "["); err != nil {
			return err
		}

		if err := c.Condition.print(w); err != nil {
			return err
		}

		if _, err := io.WriteString(w, "]"); err != nil {
			return err
		}
	}

	return nil
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

func (t *Top) print(w io.Writer) error {
	if len(t.Clauses) == 0 {
		return nil
	}

	if err := t.Clauses[0].print(w); err != nil {
		return err
	}

	for _, c := range t.Clauses[1:] {
		if _, err := io.WriteString(w, " "); err != nil {
			return err
		}

		if err := c.print(w); err != nil {
			return err
		}
	}

	return nil
}
