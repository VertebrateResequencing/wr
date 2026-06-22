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

type call struct {
	Primary primary
	Call    *logic
}

func (c *call) parse(p *parser.Parser) error {
	if err := c.Primary.parse(p); err != nil {
		return fmt.Errorf("call: %w", err)
	}

	p.AcceptRun(tokenWhitespace)

	if p.AcceptToken(parser.Token{Type: tokenOperator, Data: "("}) { //nolint:nestif
		c.Call = new(logic)

		if err := c.Call.parse(p); err != nil {
			return fmt.Errorf("call: %w", err)
		}

		p.AcceptRun(tokenWhitespace)

		if !p.AcceptToken(parser.Token{Type: tokenOperator, Data: ")"}) {
			return fmt.Errorf("call: %w", errMissingClosingParen)
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

func (c *call) replace(key string, op binaryOperator, value string) {
	if c.Primary.Parens != nil {
		c.Primary.Parens.replace(key, op, value)
	} else if c.Primary.Braces != nil {
		c.Primary.Braces.replace(key, op, value)
	}

	if c.Call != nil {
		c.Call.replace(key, op, value)
	}
}
