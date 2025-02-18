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

package bsubresource

import (
	"fmt"
	"strings"

	"vimagination.zapto.org/parser"
)

type primary struct {
	Name    *parser.Token
	Literal *parser.Token
	Parens  clauses
	Braces  clauses
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
		arr       *clauses
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

func parseClauses(p *parser.Parser, closeChar string, arr *clauses) error {
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
