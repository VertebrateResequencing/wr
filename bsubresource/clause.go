/*******************************************************************************
 * Copyright (c) 2025 Genome Research Ltd.
 *
 * Author: Michael Woolnough <mw31@sanger.ac.uk>
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
