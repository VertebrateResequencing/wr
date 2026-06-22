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
		return fmt.Errorf("logic: %w", err)
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
			return fmt.Errorf("logic: %w", err)
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

func (l *logic) replace(key string, op binaryOperator, value string) {
	l.Binary.replace(key, op, value)

	if l.Ext != nil {
		l.Ext.replace(key, op, value)
	}
}
