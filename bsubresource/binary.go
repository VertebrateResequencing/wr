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

func (b *binary) replace(key string, op binaryOperator, value string) {
	if b.Binary == nil {
		b.Call.replace(key, op, value)

		return
	}

	if b.Call.Call != nil || b.Call.Primary.Name == nil || b.Call.Primary.Name.Data != key {
		return
	}

	b.Operator = op
	b.Binary = &binary{
		Call: call{
			Primary: primary{
				Name: &parser.Token{Type: tokenWord, Data: value},
			},
		},
	}
}
