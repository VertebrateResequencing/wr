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
