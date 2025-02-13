package scheduler

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"vimagination.zapto.org/parser"
)

func TestBsubTokeniser(t *testing.T) {
	Convey("", t, func() {
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
					{Type: TokenWhitespace, Data: "    \n\r\t   "},
					{Type: parser.TokenDone},
				},
			},
			{
				"abc def",
				[]parser.Token{
					{Type: TokenWord, Data: "abc"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenWord, Data: "def"},
					{Type: parser.TokenDone},
				},
			},
			{
				"123 0.98765",
				[]parser.Token{
					{Type: TokenNumber, Data: "123"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenNumber, Data: "0.98765"},
					{Type: parser.TokenDone},
				},
			},
			{
				`"abc"'abc'"a\n\t\"a""abc`,
				[]parser.Token{
					{Type: TokenString, Data: `"abc"`},
					{Type: TokenString, Data: "'abc'"},
					{Type: TokenString, Data: `"a\n\t\"a"`},
					{Type: parser.TokenError, Data: "unexpected EOF"},
				},
			},
			{
				`= == != < <= > >= && || : [ ] ( )`,
				[]parser.Token{
					{Type: TokenOperator, Data: "="},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "=="},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "!="},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "<"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "<="},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: ">"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: ">="},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "&&"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "||"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: ":"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "["},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "]"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: "("},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenOperator, Data: ")"},
					{Type: parser.TokenDone},
				},
			},
			{
				`[)`,
				[]parser.Token{
					{Type: TokenOperator, Data: "["},
					{Type: parser.TokenError, Data: "bad"},
				},
			},
			{
				`(]`,
				[]parser.Token{
					{Type: TokenOperator, Data: "("},
					{Type: parser.TokenError, Data: "bad"},
				},
			},
			{
				"rusage[mem=(50 10):duration=(10):decay=(0)]",
				[]parser.Token{
					{Type: TokenWord, Data: "rusage"},
					{Type: TokenOperator, Data: "["},
					{Type: TokenWord, Data: "mem"},
					{Type: TokenOperator, Data: "="},
					{Type: TokenOperator, Data: "("},
					{Type: TokenNumber, Data: "50"},
					{Type: TokenWhitespace, Data: " "},
					{Type: TokenNumber, Data: "10"},
					{Type: TokenOperator, Data: ")"},
					{Type: TokenOperator, Data: ":"},
					{Type: TokenWord, Data: "duration"},
					{Type: TokenOperator, Data: "="},
					{Type: TokenOperator, Data: "("},
					{Type: TokenNumber, Data: "10"},
					{Type: TokenOperator, Data: ")"},
					{Type: TokenOperator, Data: ":"},
					{Type: TokenWord, Data: "decay"},
					{Type: TokenOperator, Data: "="},
					{Type: TokenOperator, Data: "("},
					{Type: TokenNumber, Data: "0"},
					{Type: TokenOperator, Data: ")"},
					{Type: TokenOperator, Data: "]"},
					{Type: parser.TokenDone},
				},
			},
		} {
			p := parser.NewStringTokeniser(test.Input)
			p.TokeniserState(new(state).main)

			pos := 0

			for {
				tk, _ := p.GetToken()

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
	Convey("", t, func() {
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
				Input:  "1*{span[gtile=!] rusage[ngpus_physical=2:gmem=1G]} + 4*{span[ptile=1] rusage[ngpus_physical=1:gmem=10G]}",
				Output: "1*{span[gtile=!] rusage[ngpus_physical=2:gmem=1G]} + 4*{span[ptile=1] rusage[ngpus_physical=1:gmem=10G]}1",
			},
		} {
			tk := parser.NewStringTokeniser(test.Input)
			tk.TokeniserState(new(state).main)
			p := parser.New(tk)

			var top Top

			err := top.parse(&p)
			So(err, ShouldBeNil)

			var sb strings.Builder

			err = top.print(&sb)
			So(err, ShouldBeNil)
			So(sb.String(), ShouldEqual, test.Output)
		}
	})
}
