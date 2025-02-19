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

// Package bsub parsers bsub resource strings for easy manipulation.
package bsubresource

import (
	"vimagination.zapto.org/parser"
)

// ParseBsubR parses a passed bsub `-R` requirements string into AST to it can
// be modified and reformatted.
func ParseBsubR(r string) (*Requirements, error) {
	tk := parser.NewStringTokeniser(r)
	tk.TokeniserState(new(state).main)
	p := parser.New(tk)

	var t Requirements

	return &t, t.parse(&p)
}
