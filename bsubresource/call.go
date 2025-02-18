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
