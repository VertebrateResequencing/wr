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
		return fmt.Errorf("binary: %w", err)
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
			return fmt.Errorf("binary: %w", err)
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
