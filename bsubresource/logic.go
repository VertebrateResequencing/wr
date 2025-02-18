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

func (l *logic) replace(key string, op binaryOperator, value string) {
	l.Binary.replace(key, op, value)

	if l.Ext != nil {
		l.Ext.replace(key, op, value)
	}
}
