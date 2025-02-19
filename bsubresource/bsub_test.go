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
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"vimagination.zapto.org/parser"
)

func TestBsubTokeniser(t *testing.T) {
	Convey("Given a bsub resource string, you can tokenise it", t, func() {
		for _, test := range [...]struct {
			Input  string
			Output []parser.Token
		}{
			{
				"",
				[]parser.Token{{Type: parser.TokenDone}},
			},
			{
				"    \n\r\t   ",
				[]parser.Token{
					{Type: tokenWhitespace, Data: "    \n\r\t   "},
					{Type: parser.TokenDone},
				},
			},
			{
				"abc def",
				[]parser.Token{
					{Type: tokenWord, Data: "abc"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenWord, Data: "def"},
					{Type: parser.TokenDone},
				},
			},
			{
				"123 0.98765",
				[]parser.Token{
					{Type: tokenNumber, Data: "123"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenNumber, Data: "0.98765"},
					{Type: parser.TokenDone},
				},
			},
			{
				`"abc"'abc'"a\n\t\"a""abc`,
				[]parser.Token{
					{Type: tokenString, Data: `"abc"`},
					{Type: tokenString, Data: "'abc'"},
					{Type: tokenString, Data: `"a\n\t\"a"`},
					{Type: parser.TokenError, Data: "unexpected EOF"},
				},
			},
			{
				`= == != < <= > >= && || : [ ] ( )`,
				[]parser.Token{
					{Type: tokenOperator, Data: "="},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "=="},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "!="},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "<"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "<="},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: ">"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: ">="},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "&&"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "||"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: ":"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "["},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "]"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: "("},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenOperator, Data: ")"},
					{Type: parser.TokenDone},
				},
			},
			{
				`[)`,
				[]parser.Token{
					{Type: tokenOperator, Data: "["},
					{Type: parser.TokenError, Data: "invalid group closing"},
				},
			},
			{
				`(]`,
				[]parser.Token{
					{Type: tokenOperator, Data: "("},
					{Type: parser.TokenError, Data: "invalid group closing"},
				},
			},
			{
				"rusage[mem=(50 10):duration=(10):decay=(0)]",
				[]parser.Token{
					{Type: tokenWord, Data: "rusage"},
					{Type: tokenOperator, Data: "["},
					{Type: tokenWord, Data: "mem"},
					{Type: tokenOperator, Data: "="},
					{Type: tokenOperator, Data: "("},
					{Type: tokenNumber, Data: "50"},
					{Type: tokenWhitespace, Data: " "},
					{Type: tokenNumber, Data: "10"},
					{Type: tokenOperator, Data: ")"},
					{Type: tokenOperator, Data: ":"},
					{Type: tokenWord, Data: "duration"},
					{Type: tokenOperator, Data: "="},
					{Type: tokenOperator, Data: "("},
					{Type: tokenNumber, Data: "10"},
					{Type: tokenOperator, Data: ")"},
					{Type: tokenOperator, Data: ":"},
					{Type: tokenWord, Data: "decay"},
					{Type: tokenOperator, Data: "="},
					{Type: tokenOperator, Data: "("},
					{Type: tokenNumber, Data: "0"},
					{Type: tokenOperator, Data: ")"},
					{Type: tokenOperator, Data: "]"},
					{Type: parser.TokenDone},
				},
			},
		} {
			p := parser.NewStringTokeniser(test.Input)
			p.TokeniserState(new(state).main)

			pos := 0

			for {
				tk, _ := p.GetToken() //nolint:errcheck

				So(tk, ShouldResemble, test.Output[pos])

				pos++

				if tk.Type < 0 {
					break
				}
			}
		}
	})
}

func TestBsubParser(t *testing.T) {
	Convey("Given a bsub resource string, you can parse and print it", t, func() {
		for _, test := range [...]struct {
			Input  string
			Output string
		}{
			{},
			{
				Input:  "avx",
				Output: "avx",
			},
			{
				Input:  " avx ",
				Output: "avx",
			},
			{
				Input:  "rhel6 || rhel7",
				Output: "rhel6 || rhel7",
			},
			{
				Input:  "rhel6||rhel7",
				Output: "rhel6 || rhel7",
			},
			{
				Input:  "swp > 15 && hpux order[ut]",
				Output: "swp > 15 && hpux order[ut]",
			},
			{
				Input:  "swp>15&&hpux order[ut]",
				Output: "swp > 15 && hpux order[ut]",
			},
			{
				Input:  "type==any order[ut] same[model] rusage[mem=1]",
				Output: "type==any order[ut] same[model] rusage[mem=1]",
			},
			{
				Input:  "type == any order[ut] same[model] rusage[mem = 1]",
				Output: "type==any order[ut] same[model] rusage[mem=1]",
			},
			{
				Input:  "select[type==any] order[ut] same[model] rusage[mem=1]",
				Output: "select[type==any] order[ut] same[model] rusage[mem=1]",
			},
			{
				Input:  "select[hname!='host06-x12']",
				Output: "select[hname!='host06-x12']",
			},
			{
				Input:  "rusage[mem=512MB:swp=1GB:tmp=1TB]",
				Output: "rusage[mem=512MB:swp=1GB:tmp=1TB]",
			},
			{
				Input:  "rusage[mem=0.5:swp=1:tmp=1024]",
				Output: "rusage[mem=0.5:swp=1:tmp=1024]",
			},
			{
				Input:  "defined(bigmem)",
				Output: "defined(bigmem)",
			},
			{
				Input:  "select[defined(verilog_lic)] rusage[verilog_lic=1]",
				Output: "select[defined(verilog_lic)] rusage[verilog_lic=1]",
			},
			{
				Input:  "rusage[mem=20,license=1:duration=2]",
				Output: "rusage[mem=20, license=1:duration=2]",
			},
			{
				Input:  "rusage[mem=20:swp=50:duration=1h, license=1:duration=2]",
				Output: "rusage[mem=20:swp=50:duration=1h, license=1:duration=2]",
			},
			{
				Input:  "rusage[mem=(50 10):duration=(10):decay=(0)]",
				Output: "rusage[mem=(50 10):duration=(10):decay=(0)]",
			},
			{
				Input:  "rusage[mem=10/host:duration=10:decay=0]",
				Output: "rusage[mem=10/host:duration=10:decay=0]",
			},
			{
				Input: "1*{span[gtile=!] rusage[ngpus_physical=2:gmem=1G]} + " +
					"4*{span[ptile=1] rusage[ngpus_physical=1:gmem=10G]}",
				Output: "1 * {span[gtile=!] rusage[ngpus_physical=2:gmem=1G]} + " +
					"4 * {span[ptile=1] rusage[ngpus_physical=1:gmem=10G]}",
			},
			{
				Input: "{ select[type==any] order[ut] same[model] rusage[mem=1] } || " +
					"{ select[type==any] order[ls] same[ostype] rusage[mem=5] }@4",
				Output: "{select[type==any] order[ut] same[model] rusage[mem=1]} || " +
					"{select[type==any] order[ls] same[ostype] rusage[mem=5]}@4",
			},
		} {
			top, err := ParseBsubR(test.Input)
			So(err, ShouldBeNil)

			var sb strings.Builder

			top.toString(&sb)
			So(sb.String(), ShouldEqual, test.Output)
		}
	})
}

func TestReplaceMemoryAndHosts(t *testing.T) {
	Convey("Given a bsub resource value, you can replace the memory and hosts values", t, func() {
		for _, test := range [...]struct {
			Input, Mem, Hosts, Output string
		}{
			{
				Input:  "select[hname!='host06-x12']",
				Output: "select[hname!='host06-x12']",
			},
			{
				Input:  "select[mem=1]",
				Mem:    "100",
				Output: "select[mem > 100]",
			},
			{
				Input:  "mem=1",
				Mem:    "100",
				Output: "mem > 100",
			},
			{
				Input:  "rusage[mem=1]",
				Mem:    "100",
				Output: "rusage[mem=100]",
			},
			{
				Input:  "span[hosts=1]",
				Hosts:  "5",
				Output: "span[hosts=5]",
			},
			{
				Input:  "select[mem=1] span[hosts=1] rusage[mem=1]",
				Mem:    "100",
				Hosts:  "5",
				Output: "select[mem > 100] span[hosts=5] rusage[mem=100]",
			},
		} {
			top, err := ParseBsubR(test.Input)
			So(err, ShouldBeNil)

			top.ReplaceMemoryAndHosts(test.Mem, test.Hosts)

			So(top.String(), ShouldEqual, test.Output)
		}
	})
}
