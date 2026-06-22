/*******************************************************************************
 * Copyright (c) 2025 Genome Research Ltd.
 *
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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

package bsubresource

import (
	"errors"
	"io"

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
