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

type clause struct {
	Logic     logic
	Condition *logic
}

func (c *clause) parse(p *parser.Parser) error {
	if err := c.Logic.parse(p); err != nil {
		return fmt.Errorf("clause: %w", err)
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
		return fmt.Errorf("clause: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if !p.AcceptToken(parser.Token{Type: tokenOperator, Data: "]"}) {
		return fmt.Errorf("clause: %w", errMissingClosingBracket)
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

func (c *clause) replace(key string, op binaryOperator, value string) {
	c.Logic.replace(key, op, value)

	if c.Condition != nil {
		c.Condition.replace(key, op, value)
	}
}

type clauses []clause

func (c clauses) replace(key string, op binaryOperator, value string) {
	for n := range c {
		c[n].replace(key, op, value)
	}
}
