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
	"iter"
	"strings"

	"vimagination.zapto.org/parser"
)

// Requirements represents the AST of a parsed bsub requirements string.
type Requirements struct {
	Clauses clauses
}

func (r *Requirements) clauses() iter.Seq2[string, *logic] { //nolint:gocognit,gocyclo
	return func(yield func(string, *logic) bool) {
		for n := range r.Clauses {
			if n == 0 && r.Clauses[n].Condition == nil {
				if !yield("select", &r.Clauses[n].Logic) {
					return
				}
			}

			if r.Clauses[n].Condition == nil {
				continue
			}

			p := r.Clauses[n].Logic.Binary.Call.Primary

			if p.Name == nil {
				continue
			}

			if !yield(p.Name.Data, r.Clauses[n].Condition) {
				return
			}
		}
	}
}

// ReplaceMemoryAndHosts replaces `select[mem]` and `rusage[mem]` values with
// the given memory amount, and replaces `span[hosts]` values with the given
// hosts value.
func (r *Requirements) ReplaceMemoryAndHosts(memory, hosts string) {
	for section, logic := range r.clauses() {
		switch section {
		case "select":
			logic.replace("mem", binaryGreaterThan, memory)
		case "rusage":
			logic.replace("mem", binaryEquals, memory)
		case "span":
			logic.replace("hosts", binaryEquals, hosts)
		}
	}
}

func (r *Requirements) parse(p *parser.Parser) error {
	p.AcceptRun(tokenWhitespace)

	for p.Peek().Type >= 0 {
		var c clause

		if err := c.parse(p); err != nil {
			return fmt.Errorf("top: %w", err)
		}

		r.Clauses = append(r.Clauses, c)

		p.AcceptRun(tokenWhitespace)
	}

	return nil
}

func (r *Requirements) toString(sb *strings.Builder) {
	if len(r.Clauses) == 0 {
		return
	}

	r.Clauses[0].toString(sb)

	for _, c := range r.Clauses[1:] {
		sb.WriteString(" ")
		c.toString(sb)
	}
}

// String stringifies the parsed Requirements into a consistent format.
func (r *Requirements) String() string {
	var sb strings.Builder

	r.toString(&sb)

	return sb.String()
}
