/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
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

package nextflowdsl

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var memoryRE = regexp.MustCompile(`(?i)^([0-9]+)\s*(gb|g|mb|m)$`)

var diskRE = regexp.MustCompile(`(?i)^([0-9]+)\s*(gb|g)$`)

var timeRE = regexp.MustCompile(`(?i)^([0-9]+)(?:\s*\.\s*|\s+)?(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)

var supportedChannelOperators = map[string]struct{}{
	"buffer":       {},
	"branch":       {},
	"collate":      {},
	"collect":      {},
	"collectFile":  {},
	"combine":      {},
	"concat":       {},
	"count":        {},
	"countFasta":   {},
	"countFastq":   {},
	"countJson":    {},
	"countLines":   {},
	"cross":        {},
	"distinct":     {},
	"dump":         {},
	"filter":       {},
	"first":        {},
	"flatMap":      {},
	"flatten":      {},
	"groupTuple":   {},
	"ifEmpty":      {},
	"join":         {},
	"last":         {},
	"map":          {},
	"max":          {},
	"merge":        {},
	"min":          {},
	"mix":          {},
	"multiMap":     {},
	"randomSample": {},
	"reduce":       {},
	"set":          {},
	"splitCsv":     {},
	"splitFasta":   {},
	"splitFastq":   {},
	"splitJson":    {},
	"splitText":    {},
	"subscribe":    {},
	"sum":          {},
	"tap":          {},
	"take":         {},
	"toInteger":    {},
	"toList":       {},
	"toSortedList": {},
	"transpose":    {},
	"until":        {},
	"unique":       {},
	"view":         {},
}

var supportedChannelFactories = map[string]struct{}{
	"empty":         {},
	"from":          {},
	"fromFilePairs": {},
	"fromLineage":   {},
	"fromList":      {},
	"fromPath":      {},
	"fromSRA":       {},
	"interval":      {},
	"of":            {},
	"topic":         {},
	"value":         {},
	"watchPath":     {},
}

type tokenType int

const (
	tokenEOF tokenType = iota
	tokenIdent
	tokenInt
	tokenString
	tokenLBrace
	tokenRBrace
	tokenLParen
	tokenRParen
	tokenColon
	tokenComma
	tokenDot
	tokenSemicolon
	tokenAssign
	tokenPipe
	tokenSymbol
	tokenNewline
)

type token struct {
	typ   tokenType
	lit   string
	line  int
	col   int
	start int
	end   int
}

func lex(input string) ([]token, error) {
	l := &lexer{input: []rune(input), line: 1, col: 1}
	tokens := make([]token, 0, len(input)/2)

	for {
		r := l.peek()
		switch {
		case r == 0:
			tokens = append(tokens, token{typ: tokenEOF, line: l.line, col: l.col})
			return tokens, nil
		case r == ' ' || r == '\t' || r == '\r':
			l.next()
		case r == '\n':
			line, col, start := l.line, l.col, l.pos
			l.next()
			tokens = append(tokens, token{typ: tokenNewline, lit: "\n", line: line, col: col, start: start, end: l.pos})
		case r == '/' && l.peekN(1) == '/':
			for l.peek() != 0 && l.peek() != '\n' {
				l.next()
			}
		case r == '/' && l.peekN(1) == '*':
			if err := l.skipBlockComment(); err != nil {
				return nil, err
			}
		case r == '#' && !l.lineHasContent:
			for l.peek() != 0 && l.peek() != '\n' {
				l.next()
			}
		case r == '{':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenLBrace, lit: "{", line: line, col: col, start: start, end: l.pos})
		case r == '}':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenRBrace, lit: "}", line: line, col: col, start: start, end: l.pos})
		case r == '(':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenLParen, lit: "(", line: line, col: col, start: start, end: l.pos})
		case r == ')':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenRParen, lit: ")", line: line, col: col, start: start, end: l.pos})
		case r == ':':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenColon, lit: ":", line: line, col: col, start: start, end: l.pos})
		case r == ',':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenComma, lit: ",", line: line, col: col, start: start, end: l.pos})
		case r == '.':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenDot, lit: ".", line: line, col: col, start: start, end: l.pos})
		case r == ';':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenSemicolon, lit: ";", line: line, col: col, start: start, end: l.pos})
		case r == '!' && l.peekN(1) == '=':
			start, line, col := l.pos, l.line, l.col
			l.next()
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: "!=", line: line, col: col, start: start, end: l.pos})
		case r == '=' && l.peekN(1) == '=':
			start, line, col := l.pos, l.line, l.col
			l.next()
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: "==", line: line, col: col, start: start, end: l.pos})
		case r == '=':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenAssign, lit: "=", line: line, col: col, start: start, end: l.pos})
		case r == '&' && l.peekN(1) == '&':
			start, line, col := l.pos, l.line, l.col
			l.next()
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: "&&", line: line, col: col, start: start, end: l.pos})
		case r == '|' && l.peekN(1) == '|':
			start, line, col := l.pos, l.line, l.col
			l.next()
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: "||", line: line, col: col, start: start, end: l.pos})
		case r == '|':
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenPipe, lit: "|", line: line, col: col, start: start, end: l.pos})
		case r == '>' && l.peekN(1) == '=':
			start, line, col := l.pos, l.line, l.col
			l.next()
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: ">=", line: line, col: col, start: start, end: l.pos})
		case r == '<' && l.peekN(1) == '=':
			start, line, col := l.pos, l.line, l.col
			l.next()
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: "<=", line: line, col: col, start: start, end: l.pos})
		case isClosureSymbol(r):
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: string(r), line: line, col: col, start: start, end: l.pos})
		case r == '\'' || r == '"':
			stringTok, err := l.readString()
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, stringTok)
		case unicode.IsDigit(r):
			tokens = append(tokens, l.readInt())
		case isIdentStart(r):
			tokens = append(tokens, l.readIdent())
		default:
			start, line, col := l.pos, l.line, l.col
			l.next()
			tokens = append(tokens, token{typ: tokenSymbol, lit: string(r), line: line, col: col, start: start, end: l.pos})
		}
	}
}

func isClosureSymbol(r rune) bool {
	switch r {
	case '*', '+', '-', '/', '<', '>', '?', '!', '&', '$', '%', '[', ']', '^', '~', '@', '\\':
		return true
	default:
		return false
	}
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func warnUnsupportedOutputQualifier(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: unsupported output qualifier %q at line %d will not be translated\n", name.lit, name.line)
}

func parseTernaryExprTokens(tokens []token) (Expr, error) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0

	for index, current := range tokens {
		switch current.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			parenDepth--
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			braceDepth--
		case tokenSymbol:
			switch current.lit {
			case "[":
				bracketDepth++
			case "]":
				bracketDepth--
			}
		}

		if parenDepth != 0 || bracketDepth != 0 || braceDepth != 0 || current.typ != tokenSymbol || current.lit != "?" || isNullSafeQuestion(tokens, index) {
			continue
		}

		if index+1 < len(tokens) && tokens[index+1].typ == tokenColon {
			trueTokens := tokens[:index]
			falseTokens := tokens[index+2:]
			if len(trueTokens) == 0 || len(falseTokens) == 0 {
				return nil, fmt.Errorf("unsupported expression %q", expressionText(tokens))
			}

			trueExpr, err := parseLogicalOrExprTokens(trueTokens)
			if err != nil {
				return nil, err
			}

			falseExpr, err := parseExprTokens(falseTokens)
			if err != nil {
				return nil, err
			}

			return TernaryExpr{True: trueExpr, False: falseExpr}, nil
		}

		colonIndex := findTernaryColon(tokens, index+1)
		if colonIndex == -1 {
			return nil, fmt.Errorf("unsupported expression %q", expressionText(tokens))
		}

		condTokens := tokens[:index]
		trueTokens := tokens[index+1 : colonIndex]
		falseTokens := tokens[colonIndex+1:]
		if len(condTokens) == 0 || len(trueTokens) == 0 || len(falseTokens) == 0 {
			return nil, fmt.Errorf("unsupported expression %q", expressionText(tokens))
		}

		condExpr, err := parseLogicalOrExprTokens(condTokens)
		if err != nil {
			return nil, err
		}

		trueExpr, err := parseExprTokens(trueTokens)
		if err != nil {
			return nil, err
		}

		falseExpr, err := parseExprTokens(falseTokens)
		if err != nil {
			return nil, err
		}

		return TernaryExpr{Cond: condExpr, True: trueExpr, False: falseExpr}, nil
	}

	return nil, nil
}

func splitClosureArrowTokens(tokens []token) ([]token, []token, bool) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0

	for index := 0; index < len(tokens)-1; index++ {
		current := tokens[index]
		switch current.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			if parenDepth > 0 {
				parenDepth--
			}
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			if braceDepth > 0 {
				braceDepth--
			}
		case tokenSymbol:
			switch current.lit {
			case "[":
				bracketDepth++
			case "]":
				if bracketDepth > 0 {
					bracketDepth--
				}
			}
		}

		if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && current.typ == tokenSymbol && current.lit == "-" && tokens[index+1].typ == tokenSymbol && tokens[index+1].lit == ">" {
			return tokens[:index], tokens[index+2:], true
		}
	}

	return nil, tokens, false
}

func parseComparisonExprTokens(tokens []token) (Expr, error) {
	if expr, err := parseBinaryExprTokens(tokens, []string{">", "<", ">=", "<="}, parseAdditiveExprTokens); expr != nil || err != nil {
		return expr, err
	}

	return parseAdditiveExprTokens(tokens)
}

func findTrailingBraceStart(tokens []token) (int, bool) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0

	for index := len(tokens) - 1; index >= 0; index-- {
		current := tokens[index]
		switch current.typ {
		case tokenRParen:
			parenDepth++
		case tokenLParen:
			parenDepth--
		case tokenLBrace:
			braceDepth--
			if braceDepth == 0 && parenDepth == 0 && bracketDepth == 0 {
				return index, true
			}
		case tokenRBrace:
			braceDepth++
		case tokenSymbol:
			switch current.lit {
			case "]":
				bracketDepth++
			case "[":
				bracketDepth--
			}
		}
	}

	return 0, false
}

func parseClosureExpr(tokens []token) (ClosureExpr, error) {
	if len(tokens) < 2 || tokens[0].typ != tokenLBrace || tokens[len(tokens)-1].typ != tokenRBrace {
		return ClosureExpr{}, fmt.Errorf("unsupported expression %q", expressionText(tokens))
	}

	return parseClosureInnerTokens(tokens[1 : len(tokens)-1])
}

func parseClosureInnerTokens(tokens []token) (ClosureExpr, error) {
	trimmed := trimDeclarationTokens(tokens)
	paramsTokens, bodyTokens, hasArrow := splitClosureArrowTokens(trimmed)
	if !hasArrow {
		bodyTokens = trimmed
	}

	bodyTokens = trimDeclarationTokens(bodyTokens)
	if len(bodyTokens) == 0 {
		return ClosureExpr{}, fmt.Errorf("expected closure body")
	}

	params := []string{}
	if hasArrow {
		paramSegments := splitTopLevelCommaSegments(trimDeclarationTokens(paramsTokens))
		for _, segment := range paramSegments {
			trimmedSegment := trimDeclarationTokens(segment)
			if len(trimmedSegment) == 0 {
				continue
			}

			if len(trimmedSegment) != 1 || trimmedSegment[0].typ != tokenIdent {
				return ClosureExpr{}, fmt.Errorf("unsupported closure parameters %q", expressionText(paramsTokens))
			}

			params = append(params, trimmedSegment[0].lit)
		}
	}

	return ClosureExpr{Params: params, Body: strings.TrimSpace(renderClosureTokens(bodyTokens))}, nil
}

func isParenthesisedExpr(tokens []token) bool {
	if len(tokens) < 2 || tokens[0].typ != tokenLParen || tokens[len(tokens)-1].typ != tokenRParen {
		return false
	}

	depth := 0
	for index, tok := range tokens {
		switch tok.typ {
		case tokenLParen:
			depth++
		case tokenRParen:
			depth--
			if depth == 0 && index != len(tokens)-1 {
				return false
			}
		}
	}

	return depth == 0
}

func parseNullSafeExprTokens(tokens []token) (Expr, bool, error) {
	parenDepth := 0
	bracketDepth := 0

	for index := len(tokens) - 3; index >= 1; index-- {
		current := tokens[index]
		switch current.typ {
		case tokenRParen:
			parenDepth++
		case tokenLParen:
			parenDepth--
		case tokenSymbol:
			switch current.lit {
			case "]":
				bracketDepth++
			case "[":
				bracketDepth--
			}
		}

		if parenDepth != 0 || bracketDepth != 0 || current.typ != tokenSymbol || current.lit != "?" {
			continue
		}

		if tokens[index+1].typ != tokenDot {
			continue
		}

		property, ok := parsePropertyPathTokens(tokens[index+2:])
		if !ok || index == 0 {
			return nil, true, fmt.Errorf("unsupported expression %q", expressionText(tokens))
		}

		receiver, err := parseExprTokens(tokens[:index])
		if err != nil {
			return nil, true, err
		}

		return NullSafeExpr{Receiver: receiver, Property: property}, true, nil
	}

	return nil, false, nil
}

func parseMethodCallExprTokens(tokens []token) (Expr, bool, error) {
	if closureArg, remaining, ok, err := parseTrailingClosureArgTokens(tokens); ok || err != nil {
		if err != nil {
			return nil, true, err
		}

		return parseMethodCallExprTokensWithTrailingArgs(remaining, []Expr{closureArg}, true)
	}

	return parseMethodCallExprTokensWithTrailingArgs(tokens, nil, false)
}

func parseTrailingClosureArgTokens(tokens []token) (Expr, []token, bool, error) {
	if len(tokens) == 0 || tokens[len(tokens)-1].typ != tokenRBrace {
		return nil, nil, false, nil
	}

	start, ok := findTrailingBraceStart(tokens)
	if !ok {
		return nil, nil, false, nil
	}

	remaining := trimDeclarationTokens(tokens[:start])
	if len(remaining) == 0 {
		return nil, nil, true, fmt.Errorf("unsupported expression %q", expressionText(tokens))
	}

	closureExpr, err := parseClosureExpr(tokens[start:])
	if err != nil {
		return nil, nil, true, err
	}

	return closureExpr, remaining, true, nil
}

func parseMethodCallExprTokensWithTrailingArgs(tokens []token, trailingArgs []Expr, allowBareMethod bool) (Expr, bool, error) {
	if len(tokens) < 5 || tokens[len(tokens)-1].typ != tokenRParen {
		if !allowBareMethod || len(tokens) < 3 || tokens[len(tokens)-1].typ != tokenIdent || tokens[len(tokens)-2].typ != tokenDot {
			return nil, false, nil
		}

		receiverTokens := tokens[:len(tokens)-2]
		if len(receiverTokens) == 0 {
			return nil, true, fmt.Errorf("unsupported expression %q", expressionText(tokens))
		}

		receiver, err := parseExprTokens(receiverTokens)
		if err != nil {
			return nil, true, err
		}

		return MethodCallExpr{Receiver: receiver, Method: tokens[len(tokens)-1].lit, Args: trailingArgs}, true, nil
	}

	start, ok := findTrailingParenStart(tokens)
	if !ok || start < 2 {
		return nil, false, nil
	}

	if tokens[start-1].typ != tokenIdent || tokens[start-2].typ != tokenDot {
		return nil, false, nil
	}

	receiverTokens := tokens[:start-2]
	if len(receiverTokens) == 0 {
		return nil, true, fmt.Errorf("unsupported expression %q", expressionText(tokens))
	}

	receiver, err := parseExprTokens(receiverTokens)
	if err != nil {
		return nil, true, err
	}

	args := make([]Expr, 0)
	argTokens := trimDeclarationTokens(tokens[start+1 : len(tokens)-1])
	if len(argTokens) > 0 {
		segments := splitTopLevelCommaSegments(argTokens)
		args = make([]Expr, 0, len(segments))
		for _, segment := range segments {
			trimmed := trimDeclarationTokens(segment)
			if len(trimmed) == 0 {
				return nil, true, fmt.Errorf("unsupported expression %q", expressionText(tokens))
			}

			arg, parseErr := parseExprTokens(trimmed)
			if parseErr != nil {
				return nil, true, parseErr
			}

			args = append(args, arg)
		}
	}

	args = append(args, trailingArgs...)

	return MethodCallExpr{Receiver: receiver, Method: tokens[start-1].lit, Args: args}, true, nil
}

func parseIndexExprTokens(tokens []token) (Expr, bool, error) {
	if len(tokens) < 4 || tokens[len(tokens)-1].typ != tokenSymbol || tokens[len(tokens)-1].lit != "]" {
		return nil, false, nil
	}

	start, ok := findTrailingBracketStart(tokens)
	if !ok || start == 0 {
		return nil, false, nil
	}

	receiver, err := parseExprTokens(tokens[:start])
	if err != nil {
		return nil, true, err
	}

	indexTokens := tokens[start+1 : len(tokens)-1]
	if len(indexTokens) == 0 {
		return nil, true, fmt.Errorf("unsupported expression %q", expressionText(tokens))
	}

	indexExpr, err := parseExprTokens(indexTokens)
	if err != nil {
		return nil, true, err
	}

	return IndexExpr{Receiver: receiver, Index: indexExpr}, true, nil
}

func findTrailingBracketStart(tokens []token) (int, bool) {
	parenDepth := 0
	bracketDepth := 0

	for index := len(tokens) - 1; index >= 0; index-- {
		current := tokens[index]
		switch current.typ {
		case tokenRParen:
			parenDepth++
		case tokenLParen:
			parenDepth--
		case tokenSymbol:
			switch current.lit {
			case "]":
				bracketDepth++
			case "[":
				bracketDepth--
				if bracketDepth == 0 && parenDepth == 0 {
					return index, true
				}
			}
		}
	}

	return 0, false
}

func parseCollectionExprTokens(tokens []token) (Expr, bool, error) {
	if len(tokens) < 2 || tokens[0].typ != tokenSymbol || tokens[0].lit != "[" || tokens[len(tokens)-1].typ != tokenSymbol || tokens[len(tokens)-1].lit != "]" {
		return nil, false, nil
	}

	inner := tokens[1 : len(tokens)-1]
	if len(inner) == 0 {
		return ListExpr{}, true, nil
	}

	if len(inner) == 1 && inner[0].typ == tokenColon {
		return MapExpr{}, true, nil
	}

	segments := splitTopLevelCommaSegments(inner)
	isMap := true
	for _, segment := range segments {
		trimmed := trimDeclarationTokens(segment)
		if len(trimmed) == 0 {
			return nil, true, fmt.Errorf("expected expression")
		}

		if _, _, ok := splitTopLevelColonTokens(trimmed); !ok {
			isMap = false
			break
		}
	}

	if !isMap {
		elements := make([]Expr, 0, len(segments))
		for _, segment := range segments {
			trimmed := trimDeclarationTokens(segment)
			element, err := parseExprTokens(trimmed)
			if err != nil {
				return nil, true, err
			}

			elements = append(elements, element)
		}

		return ListExpr{Elements: elements}, true, nil
	}

	keys := make([]Expr, 0, len(segments))
	values := make([]Expr, 0, len(segments))
	for _, segment := range segments {
		trimmed := trimDeclarationTokens(segment)
		keyTokens, valueTokens, ok := splitTopLevelColonTokens(trimmed)
		if !ok || len(keyTokens) == 0 || len(valueTokens) == 0 {
			return nil, true, fmt.Errorf("unsupported expression %q", expressionText(trimmed))
		}

		keyExpr, err := parseMapKeyExpr(keyTokens)
		if err != nil {
			return nil, true, err
		}

		valueExpr, err := parseExprTokens(valueTokens)
		if err != nil {
			return nil, true, err
		}

		keys = append(keys, keyExpr)
		values = append(values, valueExpr)
	}

	return MapExpr{Keys: keys, Values: values}, true, nil
}

func splitTopLevelColonTokens(tokens []token) ([]token, []token, bool) {
	parenDepth := 0
	bracketDepth := 0

	for index, current := range tokens {
		switch current.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			if parenDepth > 0 {
				parenDepth--
			}
		case tokenSymbol:
			switch current.lit {
			case "[":
				bracketDepth++
			case "]":
				if bracketDepth > 0 {
					bracketDepth--
				}
			}
		case tokenColon:
			if parenDepth == 0 && bracketDepth == 0 {
				return tokens[:index], tokens[index+1:], true
			}
		}
	}

	return nil, nil, false
}

func parseParamsPath(tokens []token) (string, bool) {
	if len(tokens) < 3 || tokens[0].typ != tokenIdent || tokens[0].lit != "params" {
		return "", false
	}

	parts := make([]string, 0, len(tokens)/2)
	for index := 1; index < len(tokens); index += 2 {
		if tokens[index].typ != tokenDot {
			return "", false
		}
		if index+1 >= len(tokens) || tokens[index+1].typ != tokenIdent {
			return "", false
		}
		parts = append(parts, tokens[index+1].lit)
	}

	return strings.Join(parts, "."), true
}

func parseTupleDeclaration(tokens []token) (*Declaration, error) {
	if len(tokens) == 1 {
		return nil, fmt.Errorf("line %d: tuple requires at least one element", tokens[0].line)
	}

	elementSegments := splitTopLevelCommaSegments(tokens[1:])
	elements := make([]*TupleElement, 0, len(elementSegments))
	for _, segment := range elementSegments {
		trimmed := trimDeclarationTokens(segment)
		if len(trimmed) == 0 {
			continue
		}

		element, err := parseTupleElement(trimmed)
		if err != nil {
			return nil, err
		}
		elements = append(elements, element)
	}

	if len(elements) == 0 {
		return nil, fmt.Errorf("line %d: tuple requires at least one element", tokens[0].line)
	}

	return &Declaration{Kind: "tuple", Elements: elements}, nil
}

func parseDeclarationNameArg(tokens []token) (string, error) {
	if len(tokens) == 0 {
		return "", fmt.Errorf("expected name")
	}

	if len(tokens) == 1 && (tokens[0].typ == tokenIdent || tokens[0].typ == tokenString) {
		return tokens[0].lit, nil
	}

	return expressionText(tokens), nil
}

func joinDeclarationSegments(segments [][]token) []token {
	joined := make([]token, 0)
	for index, segment := range segments {
		trimmed := trimDeclarationTokens(segment)
		if len(trimmed) == 0 {
			continue
		}
		if len(joined) != 0 && index > 0 {
			joined = append(joined, token{typ: tokenComma, lit: ",", line: trimmed[0].line, col: trimmed[0].col})
		}
		joined = append(joined, trimmed...)
	}

	return joined
}

func parseTupleElement(tokens []token) (*TupleElement, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expected tuple element")
	}

	element := &TupleElement{Kind: tokens[0].lit, Raw: joinTokens(tokens)}
	if len(tokens) == 1 && (element.Kind == "stdout" || element.Kind == "stdin") {
		return element, nil
	}

	if isCallLikeDeclaration(tokens) {
		segments := splitTopLevelCommaSegments(tokens[2 : len(tokens)-1])
		if len(segments) == 0 {
			return nil, fmt.Errorf("line %d: %s requires an argument", tokens[0].line, element.Kind)
		}

		primary := trimDeclarationTokens(segments[0])
		if len(primary) == 0 {
			return nil, fmt.Errorf("line %d: %s requires an argument", tokens[0].line, element.Kind)
		}

		if err := applyTupleElementPrimary(element, primary); err != nil {
			return nil, wrapLineError(tokens[0].line, err)
		}

		for _, segment := range segments[1:] {
			trimmed := trimDeclarationTokens(segment)
			if len(trimmed) == 0 {
				continue
			}
			if err := applyTupleElementQualifier(element, trimmed); err != nil {
				return nil, err
			}
		}

		return element, nil
	}

	if len(tokens) == 2 && tokens[1].typ == tokenIdent {
		element.Name = tokens[1].lit
		return element, nil
	}

	if len(tokens) > 1 {
		expr, err := parseExprTokens(tokens[1:])
		if err != nil {
			return nil, wrapLineError(tokens[0].line, err)
		}
		element.Expr = expr
	}

	return element, nil
}

func isCallLikeDeclaration(tokens []token) bool {
	return len(tokens) >= 4 && tokens[1].typ == tokenLParen && tokens[len(tokens)-1].typ == tokenRParen
}

func splitTopLevelCommaSegments(tokens []token) [][]token {
	segments := make([][]token, 0, 1)
	segmentStart := 0
	parenDepth := 0
	bracketDepth := 0

	for index, tok := range tokens {
		switch tok.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			if parenDepth > 0 {
				parenDepth--
			}
		case tokenSymbol:
			switch tok.lit {
			case "[":
				bracketDepth++
			case "]":
				if bracketDepth > 0 {
					bracketDepth--
				}
			}
		case tokenComma:
			if parenDepth == 0 && bracketDepth == 0 {
				segments = append(segments, tokens[segmentStart:index])
				segmentStart = index + 1
			}
		}
	}

	return append(segments, tokens[segmentStart:])
}

func trimDeclarationTokens(tokens []token) []token {
	start := 0
	for start < len(tokens) && tokens[start].typ == tokenNewline {
		start++
	}
	end := len(tokens)
	for end > start && tokens[end-1].typ == tokenNewline {
		end--
	}

	return tokens[start:end]
}

func applyTupleElementPrimary(element *TupleElement, tokens []token) error {
	if element.Kind == "env" {
		name, err := parseDeclarationNameArg(tokens)
		if err != nil {
			return err
		}
		element.Name = name
		return nil
	}

	if len(tokens) == 1 && tokens[0].typ == tokenIdent {
		element.Name = tokens[0].lit
		return nil
	}

	expr, err := parseExprTokens(tokens)
	if err != nil {
		return err
	}
	element.Expr = expr

	return nil
}

func applyTupleElementQualifier(element *TupleElement, tokens []token) error {
	if !isDeclarationQualifierTokens(tokens) {
		return fmt.Errorf("line %d: unsupported tuple element qualifier %q", tokens[0].line, joinTokens(tokens))
	}

	name := tokens[0]
	valueTokens := tokens[2:]
	if len(valueTokens) == 0 {
		return fmt.Errorf("line %d: qualifier %q requires a value", name.line, name.lit)
	}

	switch name.lit {
	case "arity":
		_, err := parseExprTokens(valueTokens)
		if err != nil {
			return wrapLineError(name.line, err)
		}
	case "emit":
		value, err := parseExprTokens(valueTokens)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		switch emitValue := value.(type) {
		case StringExpr:
			element.Emit = emitValue.Value
		case VarExpr:
			if emitValue.Path != "" {
				return fmt.Errorf("line %d: emit qualifier expects a string or identifier", name.line)
			}
			element.Emit = emitValue.Root
		default:
			return fmt.Errorf("line %d: emit qualifier expects a string or identifier", name.line)
		}
	default:
		return fmt.Errorf("line %d: unsupported tuple element qualifier %q", name.line, name.lit)
	}

	return nil
}

func isDeclarationQualifierTokens(tokens []token) bool {
	return len(tokens) >= 2 && tokens[0].typ == tokenIdent && tokens[1].typ == tokenColon
}

func parsePropertyPathTokens(tokens []token) (string, bool) {
	if len(tokens) == 0 || tokens[0].typ != tokenIdent {
		return "", false
	}

	parts := []string{tokens[0].lit}
	for index := 1; index < len(tokens); index += 2 {
		if tokens[index].typ != tokenDot {
			return "", false
		}

		if index+1 >= len(tokens) || tokens[index+1].typ != tokenIdent {
			return "", false
		}

		parts = append(parts, tokens[index+1].lit)
	}

	return strings.Join(parts, "."), true
}

func parseLogicalOrExprTokens(tokens []token) (Expr, error) {
	if expr, err := parseBinaryExprTokens(tokens, []string{"||"}, parseLogicalAndExprTokens); expr != nil || err != nil {
		return expr, err
	}

	return parseLogicalAndExprTokens(tokens)
}

func parseEqualityExprTokens(tokens []token) (Expr, error) {
	if expr, err := parseBinaryExprTokens(tokens, []string{"==", "!="}, parseComparisonExprTokens); expr != nil || err != nil {
		return expr, err
	}

	return parseComparisonExprTokens(tokens)
}

func parseAdditiveExprTokens(tokens []token) (Expr, error) {
	if expr, err := parseBinaryExprTokens(tokens, []string{"+", "-"}, parseMultiplicativeExprTokens); expr != nil || err != nil {
		return expr, err
	}

	return parseMultiplicativeExprTokens(tokens)
}

func parseLogicalAndExprTokens(tokens []token) (Expr, error) {
	if expr, err := parseBinaryExprTokens(tokens, []string{"&&"}, parseEqualityExprTokens); expr != nil || err != nil {
		return expr, err
	}

	return parseEqualityExprTokens(tokens)
}

func parseMultiplicativeExprTokens(tokens []token) (Expr, error) {
	if expr, err := parseBinaryExprTokens(tokens, []string{"*", "/"}, parseUnaryExprTokens); expr != nil || err != nil {
		return expr, err
	}

	return parseUnaryExprTokens(tokens)
}

func parseUnaryExprTokens(tokens []token) (Expr, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expected expression")
	}

	if tokens[0].typ == tokenSymbol && tokens[0].lit == "!" {
		operand, err := parseUnaryExprTokens(tokens[1:])
		if err != nil {
			return nil, err
		}

		return UnaryExpr{Op: "!", Operand: operand}, nil
	}

	if expr, err := parseCastExprTokens(tokens); expr != nil || err != nil {
		return expr, err
	}

	return parsePrimaryExprTokens(tokens)
}

func parseCastExprTokens(tokens []token) (Expr, error) {
	depth := 0

	for index := len(tokens) - 2; index >= 1; index-- {
		current := tokens[index]
		switch current.typ {
		case tokenRParen:
			depth++
		case tokenLParen:
			depth--
		}

		if depth != 0 || current.typ != tokenIdent || current.lit != "as" {
			continue
		}

		operandTokens := tokens[:index]
		typeTokens := tokens[index+1:]
		if len(operandTokens) == 0 || len(typeTokens) == 0 {
			return nil, fmt.Errorf("unsupported expression %q", expressionText(tokens))
		}

		operand, err := parseUnaryExprTokens(operandTokens)
		if err != nil {
			return nil, err
		}

		return CastExpr{Operand: operand, TypeName: expressionText(typeTokens)}, nil
	}

	return nil, nil
}

func parsePrimaryExprTokens(tokens []token) (Expr, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expected expression")
	}

	if len(tokens) >= 2 && tokens[0].typ == tokenLBrace && tokens[len(tokens)-1].typ == tokenRBrace {
		closureExpr, err := parseClosureExpr(tokens)
		if err == nil {
			return closureExpr, nil
		}
	}

	if isParenthesisedExpr(tokens) {
		return parseExprTokens(tokens[1 : len(tokens)-1])
	}

	if expr, ok, err := parseNullSafeExprTokens(tokens); ok || err != nil {
		return expr, err
	}

	if expr, ok, err := parseMethodCallExprTokens(tokens); ok || err != nil {
		return expr, err
	}

	if expr, ok, err := parseIndexExprTokens(tokens); ok || err != nil {
		return expr, err
	}

	if expr, ok, err := parseCollectionExprTokens(tokens); ok || err != nil {
		return expr, err
	}

	if len(tokens) == 1 && tokens[0].typ == tokenIdent {
		switch tokens[0].lit {
		case "true":
			return BoolExpr{Value: true}, nil
		case "false":
			return BoolExpr{Value: false}, nil
		case "null":
			return NullExpr{}, nil
		}
	}

	if paramsPath, ok := parseParamsPath(tokens); ok {
		return ParamsExpr{Path: paramsPath}, nil
	}

	if variable, ok := parseVarExprTokens(tokens); ok {
		return variable, nil
	}

	if len(tokens) != 1 {
		return UnsupportedExpr{Text: expressionText(tokens)}, nil
	}

	tok := tokens[0]
	switch tok.typ {
	case tokenInt:
		value, err := strconv.Atoi(tok.lit)
		if err != nil {
			return nil, err
		}
		return IntExpr{Value: value}, nil
	case tokenString:
		return StringExpr{Value: tok.lit}, nil
	case tokenIdent:
		switch tok.lit {
		case "true":
			return BoolExpr{Value: true}, nil
		case "false":
			return BoolExpr{Value: false}, nil
		case "null":
			return NullExpr{}, nil
		default:
			return StringExpr{Value: tok.lit}, nil
		}
	default:
		return nil, fmt.Errorf("unsupported expression %q", joinTokens(tokens))
	}
}

func parseMapKeyExpr(tokens []token) (Expr, error) {
	trimmed := trimDeclarationTokens(tokens)
	if len(trimmed) == 1 && trimmed[0].typ == tokenIdent {
		return StringExpr{Value: trimmed[0].lit}, nil
	}

	return parseExprTokens(trimmed)
}

func findTrailingParenStart(tokens []token) (int, bool) {
	parenDepth := 0
	bracketDepth := 0

	for index := len(tokens) - 1; index >= 0; index-- {
		current := tokens[index]
		switch current.typ {
		case tokenRParen:
			parenDepth++
		case tokenLParen:
			parenDepth--
			if parenDepth == 0 && bracketDepth == 0 {
				return index, true
			}
		case tokenSymbol:
			switch current.lit {
			case "]":
				bracketDepth++
			case "[":
				bracketDepth--
			}
		}
	}

	return 0, false
}

func findTernaryColon(tokens []token, start int) int {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	ternaryDepth := 0

	for index := start; index < len(tokens); index++ {
		current := tokens[index]
		switch current.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			parenDepth--
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			braceDepth--
		case tokenSymbol:
			switch current.lit {
			case "[":
				bracketDepth++
			case "]":
				bracketDepth--
			case "?":
				if !isNullSafeQuestion(tokens, index) {
					ternaryDepth++
				}
			}
		case tokenColon:
			if parenDepth != 0 || bracketDepth != 0 || braceDepth != 0 {
				continue
			}

			if ternaryDepth == 0 {
				return index
			}

			ternaryDepth--
		}
	}

	return -1
}

func isNullSafeQuestion(tokens []token, index int) bool {
	return index+1 < len(tokens) && tokens[index].typ == tokenSymbol && tokens[index].lit == "?" && tokens[index+1].typ == tokenDot
}

type lexer struct {
	input          []rune
	pos            int
	line           int
	col            int
	lineHasContent bool
}

func (l *lexer) peek() rune {
	if l.pos >= len(l.input) {
		return 0
	}

	return l.input[l.pos]
}

func (l *lexer) peekN(offset int) rune {
	index := l.pos + offset
	if index >= len(l.input) {
		return 0
	}

	return l.input[index]
}

func (l *lexer) next() rune {
	if l.pos >= len(l.input) {
		return 0
	}

	r := l.input[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
		l.lineHasContent = false
	} else {
		l.col++
		if r != ' ' && r != '\t' && r != '\r' {
			l.lineHasContent = true
		}
	}

	return r
}

func (l *lexer) skipBlockComment() error {
	line := l.line

	l.next()
	l.next()

	for {
		r := l.peek()
		if r == 0 {
			return fmt.Errorf("line %d: unterminated comment", line)
		}

		if r == '*' && l.peekN(1) == '/' {
			l.next()
			l.next()
			return nil
		}

		l.next()
	}
}

func (l *lexer) readString() (token, error) {
	quote := l.peek()
	line, col, start := l.line, l.col, l.pos
	triple := l.peekN(1) == quote && l.peekN(2) == quote
	if triple {
		l.next()
		l.next()
		l.next()
	} else {
		l.next()
	}

	var builder strings.Builder
	for {
		r := l.peek()
		if r == 0 {
			return token{}, fmt.Errorf("line %d: unterminated string literal", line)
		}

		if triple {
			if r == quote && l.peekN(1) == quote && l.peekN(2) == quote {
				l.next()
				l.next()
				l.next()
				break
			}
		} else if r == quote {
			l.next()
			break
		}

		if r == '\\' && !triple {
			l.next()
			escaped := l.peek()
			if escaped == 0 {
				return token{}, fmt.Errorf("line %d: unterminated escape sequence", l.line)
			}
			builder.WriteRune(unescapeRune(escaped))
			l.next()
			continue
		}

		builder.WriteRune(l.next())
	}

	return token{typ: tokenString, lit: builder.String(), line: line, col: col, start: start, end: l.pos}, nil
}

func unescapeRune(r rune) rune {
	switch r {
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	default:
		return r
	}
}

func (l *lexer) readInt() token {
	line, col, start := l.line, l.col, l.pos
	for unicode.IsDigit(l.peek()) {
		l.next()
	}

	return token{typ: tokenInt, lit: string(l.input[start:l.pos]), line: line, col: col, start: start, end: l.pos}
}

func (l *lexer) readIdent() token {
	line, col, start := l.line, l.col, l.pos
	for isIdentPart(l.peek()) {
		l.next()
	}

	return token{typ: tokenIdent, lit: string(l.input[start:l.pos]), line: line, col: col, start: start, end: l.pos}
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || unicode.IsDigit(r)
}

type parser struct {
	tokens      []token
	pos         int
	source      []rune
	assignments map[string]ChanExpr
	localScopes []map[string]ChanExpr
}

func newParser(tokens []token, source string) *parser {
	return &parser{tokens: tokens, source: []rune(source), assignments: make(map[string]ChanExpr)}
}

func (p *parser) parseWorkflow() (*Workflow, error) {
	wf := &Workflow{Processes: []*Process{}, SubWFs: []*SubWorkflow{}, Imports: []*Import{}, Functions: []*FuncDef{}}

	for {
		p.skipNewlines()
		current := p.current()
		if current.typ == tokenEOF {
			return wf, nil
		}

		if current.typ == tokenIdent && current.lit == "include" {
			importNode, err := p.parseImport()
			if err != nil {
				return nil, err
			}
			wf.Imports = append(wf.Imports, importNode)
			continue
		}

		if current.typ == tokenIdent && current.lit == "def" {
			funcDef, err := p.parseFunctionDef()
			if err != nil {
				return nil, err
			}
			wf.Functions = append(wf.Functions, funcDef)
			continue
		}

		if current.typ == tokenIdent && current.lit == "process" {
			proc, err := p.parseProcess()
			if err != nil {
				return nil, err
			}
			wf.Processes = append(wf.Processes, proc)
			continue
		}

		if current.typ == tokenIdent && current.lit == "workflow" {
			if p.isWorkflowLifecycleHandlerStart() {
				if err := p.parseWorkflowLifecycleHandler(); err != nil {
					return nil, err
				}
				continue
			}

			name, block, err := p.parseWorkflowBlockDecl()
			if err != nil {
				return nil, err
			}
			if name == "" {
				wf.EntryWF = block
			} else {
				wf.SubWFs = append(wf.SubWFs, &SubWorkflow{Name: name, Body: block})
			}
			continue
		}

		if p.isTopLevelOutputBlockStart() {
			if err := p.parseTopLevelOutputBlock(); err != nil {
				return nil, err
			}
			continue
		}

		if p.skipFeatureFlagAssignment() {
			continue
		}

		if current.typ == tokenIdent && p.peek().typ == tokenAssign {
			if err := p.parseChannelAssignment(tokenNewline, tokenEOF); err != nil {
				return nil, err
			}
			continue
		}

		return nil, fmt.Errorf("line %d: unexpected token %q", current.line, current.lit)
	}
}

func (p *parser) isTopLevelOutputBlockStart() bool {
	current := p.current()
	if current.typ != tokenIdent || current.lit != "output" {
		return false
	}

	for offset := 1; ; offset++ {
		next := p.peekN(offset)
		switch next.typ {
		case tokenNewline:
			continue
		case tokenLBrace:
			return true
		default:
			return false
		}
	}
}

func (p *parser) parseFunctionDef() (*FuncDef, error) {
	if _, err := p.expectIdent("def"); err != nil {
		return nil, err
	}

	name, err := p.expectType(tokenIdent, "function name")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLParen, "("); err != nil {
		return nil, err
	}

	params := make([]string, 0)
	for {
		p.skipNewlines()
		current := p.current()
		if current.typ == tokenRParen {
			p.pos++
			break
		}

		param, err := p.expectType(tokenIdent, "function parameter")
		if err != nil {
			return nil, err
		}
		params = append(params, param.lit)

		p.skipNewlines()
		switch p.current().typ {
		case tokenComma:
			p.pos++
		case tokenRParen:
			p.pos++
			goto parseBody
		default:
			return nil, fmt.Errorf("line %d: expected , or )", p.current().line)
		}
	}

parseBody:
	p.skipNewlines()
	openBrace, err := p.expectType(tokenLBrace, "{")
	if err != nil {
		return nil, err
	}

	body, err := p.parseRawBraceBody(openBrace, fmt.Sprintf("function %q", name.lit))
	if err != nil {
		return nil, err
	}

	return &FuncDef{Name: name.lit, Params: params, Body: body}, nil
}

func (p *parser) parseTopLevelOutputBlock() error {
	if _, err := p.expectIdent("output"); err != nil {
		return err
	}

	p.skipNewlines()
	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return err
	}

	depth := 1
	for depth > 0 {
		current := p.current()
		if current.typ == tokenEOF {
			line := p.previous().line
			if line == 0 {
				line = current.line
			}

			return fmt.Errorf("line %d: expected } to close output block", line)
		}

		switch current.typ {
		case tokenLBrace:
			depth++
		case tokenRBrace:
			depth--
		}

		p.pos++
	}

	return nil
}

func (p *parser) skipFeatureFlagAssignment() bool {
	if !p.isFeatureFlagAssignmentStart() {
		return false
	}

	for {
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenNewline {
			break
		}
		p.pos++
	}

	if p.current().typ == tokenNewline {
		p.pos++
	}

	return true
}

func (p *parser) isWorkflowLifecycleHandlerStart() bool {
	if p.current().typ != tokenIdent || p.current().lit != "workflow" {
		return false
	}

	if p.peek().typ != tokenDot || p.peekN(2).typ != tokenIdent {
		return false
	}

	switch p.peekN(2).lit {
	case "onComplete", "onError":
		return true
	default:
		return false
	}
}

func (p *parser) parseWorkflowLifecycleHandler() error {
	if _, err := p.expectIdent("workflow"); err != nil {
		return err
	}

	if _, err := p.expectType(tokenDot, "."); err != nil {
		return err
	}

	handler, err := p.expectType(tokenIdent, "workflow lifecycle handler")
	if err != nil {
		return err
	}

	if handler.lit != "onComplete" && handler.lit != "onError" {
		return fmt.Errorf("line %d: unsupported workflow lifecycle handler %q", handler.line, handler.lit)
	}

	p.skipNewlines()
	openBrace, err := p.expectType(tokenLBrace, "{")
	if err != nil {
		return err
	}

	_, err = p.parseRawBraceBody(openBrace, fmt.Sprintf("workflow.%s handler", handler.lit))

	return err
}

func (p *parser) isFeatureFlagAssignmentStart() bool {
	if p.current().typ != tokenIdent || p.current().lit != "nextflow" {
		return false
	}

	index := p.pos + 1
	if index+3 >= len(p.tokens) {
		return false
	}

	if p.tokens[index].typ != tokenDot || p.tokens[index+1].typ != tokenIdent {
		return false
	}

	section := p.tokens[index+1].lit
	if section != "enable" && section != "preview" {
		return false
	}

	index += 2
	segments := 0
	for index+1 < len(p.tokens) && p.tokens[index].typ == tokenDot && p.tokens[index+1].typ == tokenIdent {
		segments++
		index += 2
	}

	if segments == 0 || index >= len(p.tokens) {
		return false
	}

	return p.tokens[index].typ == tokenAssign
}

func (p *parser) parseImport() (*Import, error) {
	if _, err := p.expectIdent("include"); err != nil {
		return nil, err
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	importNode := &Import{Names: []string{}, Alias: make(map[string]string)}
	for {
		p.skipNewlines()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, fmt.Errorf("line %d: expected } to close include", current.line)
		case tokenRBrace:
			if len(importNode.Names) == 0 {
				return nil, fmt.Errorf("line %d: expected import name", current.line)
			}
			p.pos++
			goto parseSource
		case tokenIdent:
			name := current.lit
			importNode.Names = append(importNode.Names, name)
			p.pos++

			if p.current().typ == tokenIdent && p.current().lit == "as" {
				p.pos++
				alias, err := p.expectType(tokenIdent, "import alias")
				if err != nil {
					return nil, err
				}
				importNode.Alias[name] = alias.lit
			}

			p.skipNewlines()
			switch p.current().typ {
			case tokenSemicolon:
				p.pos++
			case tokenRBrace:
			default:
				return nil, fmt.Errorf("line %d: expected ; or } in include", p.current().line)
			}
		default:
			return nil, fmt.Errorf("line %d: expected import name", current.line)
		}
	}

parseSource:
	p.skipNewlines()
	if p.current().typ != tokenIdent || p.current().lit != "from" {
		return nil, fmt.Errorf("line %d: missing module source", p.current().line)
	}
	p.pos++
	p.skipNewlines()

	if p.current().typ != tokenString {
		return nil, fmt.Errorf("line %d: missing module source", p.current().line)
	}

	importNode.Source = p.current().lit
	p.pos++

	return importNode, nil
}

func (p *parser) parseWorkflowBlockDecl() (string, *WorkflowBlock, error) {
	if _, err := p.expectIdent("workflow"); err != nil {
		return "", nil, err
	}

	p.skipNewlines()
	name := ""
	if p.current().typ == tokenIdent {
		name = p.current().lit
		p.pos++
	}

	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return "", nil, err
	}

	block, err := p.parseWorkflowBlock()
	if err != nil {
		return "", nil, err
	}

	return name, block, nil
}

func (p *parser) parseWorkflowBlock() (*WorkflowBlock, error) {
	p.localScopes = append(p.localScopes, make(map[string]ChanExpr))
	defer func() {
		p.localScopes = p.localScopes[:len(p.localScopes)-1]
	}()

	block := &WorkflowBlock{Calls: []*Call{}, Take: []string{}, Emit: []*WFEmit{}, Conditions: []*IfBlock{}}
	section := "main"

	for {
		p.skipWorkflowSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			line := p.previous().line
			if line == 0 {
				line = current.line
			}
			return nil, fmt.Errorf("line %d: expected } to close workflow", line)
		case tokenRBrace:
			p.pos++
			return block, nil
		}

		if current.typ == tokenIdent && p.peek().typ == tokenColon {
			if !isWorkflowBlockSection(current.lit) {
				if section == "publish" {
					p.readWorkflowPublishLineTokens()
					continue
				}

				return nil, fmt.Errorf("line %d: unsupported workflow section %q", current.line, current.lit)
			}

			section = current.lit
			p.pos += 2
			p.skipWorkflowSeparators()
			continue
		}

		switch section {
		case "take":
			take, err := p.parseWorkflowTakeLine()
			if err != nil {
				return nil, err
			}
			block.Take = append(block.Take, take...)
		case "emit":
			emit, err := p.parseWorkflowEmitLine()
			if err != nil {
				return nil, err
			}
			block.Emit = append(block.Emit, emit)
		case "publish":
			p.readWorkflowPublishLineTokens()
		case "main":
			if current.typ == tokenIdent && current.lit == "if" {
				condition, err := p.parseWorkflowIfBlock()
				if err != nil {
					return nil, err
				}
				block.Conditions = append(block.Conditions, condition)
				continue
			}

			if current.typ == tokenIdent && p.peek().typ == tokenAssign {
				if err := p.parseChannelAssignment(tokenNewline, tokenSemicolon, tokenRBrace, tokenEOF); err != nil {
					return nil, err
				}
				continue
			}

			calls, err := p.parseWorkflowStatement()
			if err != nil {
				return nil, err
			}
			block.Calls = append(block.Calls, calls...)
		default:
			return nil, fmt.Errorf("line %d: unsupported workflow section %q", current.line, section)
		}
	}
}

func isWorkflowBlockSection(name string) bool {
	switch name {
	case "take", "main", "emit", "publish":
		return true
	default:
		return false
	}
}

func (p *parser) parseWorkflowTakeLine() ([]string, error) {
	lineTokens := p.readLineTokens()
	if len(lineTokens) == 0 {
		return nil, fmt.Errorf("line %d: expected workflow take declaration", p.current().line)
	}

	segments := splitTopLevelCommaSegments(lineTokens)
	take := make([]string, 0, len(segments))
	for _, segment := range segments {
		trimmed := trimDeclarationTokens(segment)
		if len(trimmed) == 0 {
			continue
		}

		take = append(take, p.rawTokenText(trimmed))
	}

	if len(take) == 0 {
		return nil, fmt.Errorf("line %d: expected workflow take declaration", lineTokens[0].line)
	}

	return take, nil
}

func (p *parser) parseWorkflowEmitLine() (*WFEmit, error) {
	lineTokens := p.readLineTokens()
	if len(lineTokens) == 0 {
		return nil, fmt.Errorf("line %d: expected workflow emit declaration", p.current().line)
	}

	assignIndex := -1
	parenDepth := 0
	braceDepth := 0
	for index, tok := range lineTokens {
		switch tok.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			if parenDepth > 0 {
				parenDepth--
			}
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			if braceDepth > 0 {
				braceDepth--
			}
		case tokenAssign:
			if parenDepth == 0 && braceDepth == 0 {
				assignIndex = index
				goto parseAssign
			}
		}
	}

parseAssign:

	if assignIndex == -1 {
		trimmed := trimDeclarationTokens(lineTokens)
		if len(trimmed) == 0 {
			return nil, fmt.Errorf("line %d: expected workflow emit declaration", lineTokens[0].line)
		}

		return &WFEmit{Name: p.rawTokenText(trimmed)}, nil
	}

	nameTokens := trimDeclarationTokens(lineTokens[:assignIndex])
	exprTokens := trimDeclarationTokens(lineTokens[assignIndex+1:])
	if len(nameTokens) == 0 || len(exprTokens) == 0 {
		return nil, fmt.Errorf("line %d: expected workflow emit declaration", lineTokens[0].line)
	}

	return &WFEmit{Name: p.rawTokenText(nameTokens), Expr: p.rawTokenText(exprTokens)}, nil
}

func (p *parser) parseWorkflowStatement() ([]*Call, error) {
	if p.current().typ == tokenIdent && p.peek().typ == tokenLParen {
		call, err := p.parseCall()
		if err != nil {
			return nil, err
		}

		return []*Call{call}, nil
	}

	expr, err := p.parseChanExpr(tokenNewline, tokenSemicolon, tokenRBrace, tokenEOF)
	if err != nil {
		return nil, err
	}

	return desugarWorkflowPipe(expr)
}

func desugarWorkflowPipe(expr ChanExpr) ([]*Call, error) {
	pipe, ok := expr.(PipeExpr)
	if !ok {
		return nil, fmt.Errorf("workflow statements must be calls or channel pipelines")
	}
	if len(pipe.Stages) < 2 {
		return nil, fmt.Errorf("workflow pipelines require at least one process stage")
	}

	input := pipe.Stages[0]
	calls := make([]*Call, 0, len(pipe.Stages)-1)
	for stageIndex, stage := range pipe.Stages[1:] {
		target, ok := stage.(ChanRef)
		if !ok {
			return nil, fmt.Errorf("workflow pipe stage %d must be a bare identifier", stageIndex+2)
		}

		if target.Name == "view" && stageIndex == len(pipe.Stages)-2 {
			break
		}

		call := &Call{Target: target.Name, Args: []ChanExpr{input}}
		calls = append(calls, call)
		input = ChanRef{Name: target.Name + ".out"}
	}

	if len(calls) == 0 {
		return nil, fmt.Errorf("workflow pipelines require at least one runnable process stage")
	}

	return calls, nil
}

func (p *parser) parseWorkflowIfBlock() (*IfBlock, error) {
	condition, body, err := p.parseWorkflowIfClause()
	if err != nil {
		return nil, err
	}

	ifBlock := &IfBlock{Condition: condition, Body: body, ElseIf: []*IfBlock{}, ElseBody: []*Call{}}

	for {
		p.skipWorkflowSeparators()
		if p.current().typ != tokenIdent || p.current().lit != "else" {
			return ifBlock, nil
		}

		p.pos++
		p.skipWorkflowSeparators()
		if p.current().typ == tokenIdent && p.current().lit == "if" {
			elseIfCondition, elseIfBody, parseErr := p.parseWorkflowIfClause()
			if parseErr != nil {
				return nil, parseErr
			}

			ifBlock.ElseIf = append(ifBlock.ElseIf, &IfBlock{Condition: elseIfCondition, Body: elseIfBody, ElseIf: []*IfBlock{}, ElseBody: []*Call{}})
			continue
		}

		elseBody, parseErr := p.parseWorkflowBranchBody()
		if parseErr != nil {
			return nil, parseErr
		}

		ifBlock.ElseBody = elseBody
		return ifBlock, nil
	}
}

func (p *parser) parseWorkflowIfClause() (string, []*Call, error) {
	ifTok, err := p.expectIdent("if")
	if err != nil {
		return "", nil, err
	}

	p.skipWorkflowSeparators()
	if _, err = p.expectType(tokenLParen, "("); err != nil {
		return "", nil, err
	}

	conditionTokens, err := p.readExprTokens(tokenRParen)
	if err != nil {
		return "", nil, err
	}

	if _, err = p.expectType(tokenRParen, ")"); err != nil {
		return "", nil, err
	}

	condition := p.rawTokenText(conditionTokens)
	if condition == "" {
		return "", nil, fmt.Errorf("line %d: expected workflow if condition", ifTok.line)
	}

	p.skipWorkflowSeparators()
	body, err := p.parseWorkflowBranchBody()
	if err != nil {
		return "", nil, err
	}

	return condition, body, nil
}

func (p *parser) parseWorkflowBranchBody() ([]*Call, error) {
	openBrace, err := p.expectType(tokenLBrace, "{")
	if err != nil {
		return nil, err
	}

	calls := []*Call{}
	for {
		p.skipWorkflowSeparators()
		current := p.current()

		switch current.typ {
		case tokenEOF:
			return nil, fmt.Errorf("line %d: expected } to close workflow conditional", openBrace.line)
		case tokenRBrace:
			p.pos++
			return calls, nil
		}

		if current.typ == tokenIdent && current.lit == "if" {
			return nil, fmt.Errorf("line %d: nested workflow conditionals are not supported", current.line)
		}

		if current.typ == tokenIdent && p.peek().typ == tokenAssign {
			if err := p.parseChannelAssignment(tokenNewline, tokenSemicolon, tokenRBrace, tokenEOF); err != nil {
				return nil, err
			}
			continue
		}

		statementCalls, err := p.parseWorkflowStatement()
		if err != nil {
			return nil, err
		}
		calls = append(calls, statementCalls...)
	}
}

func (p *parser) parseCall() (*Call, error) {
	target, err := p.expectType(tokenIdent, "call target")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLParen, "("); err != nil {
		return nil, err
	}

	call := &Call{Target: target.lit}
	for {
		if p.current().typ == tokenRParen {
			p.pos++
			return call, nil
		}

		arg, err := p.parseChanExpr(tokenComma, tokenRParen)
		if err != nil {
			return nil, err
		}
		call.Args = append(call.Args, arg)

		switch p.current().typ {
		case tokenComma:
			p.pos++
		case tokenRParen:
			p.pos++
			return call, nil
		default:
			return nil, fmt.Errorf("line %d: expected , or )", p.current().line)
		}
	}
}

func (p *parser) parseChannelAssignment(terminators ...tokenType) error {
	name, err := p.expectType(tokenIdent, "channel variable name")
	if err != nil {
		return err
	}

	if _, err = p.expectType(tokenAssign, "="); err != nil {
		return err
	}

	value, err := p.parseChanExpr(terminators...)
	if err != nil {
		return err
	}
	if p.current().typ == tokenNewline || p.current().typ == tokenSemicolon {
		p.pos++
	}

	if scope := p.currentLocalScope(); scope != nil {
		scope[name.lit] = value
		return nil
	}

	p.assignments[name.lit] = value

	return nil
}

func (p *parser) parseChanExpr(terminators ...tokenType) (ChanExpr, error) {
	stage, err := p.parseChanStage(terminators...)
	if err != nil {
		return nil, err
	}

	stages := []ChanExpr{stage}
	for p.current().typ == tokenPipe {
		p.pos++
		nextStage, err := p.parseChanStage(terminators...)
		if err != nil {
			return nil, err
		}
		stages = append(stages, nextStage)
	}

	if len(stages) == 1 {
		return stages[0], nil
	}

	return PipeExpr{Stages: stages}, nil
}

func (p *parser) parseChanStage(terminators ...tokenType) (ChanExpr, error) {
	current := p.current()
	if hasTokenType(terminators, current.typ) {
		return nil, fmt.Errorf("line %d: expected channel expression", current.line)
	}

	if current.typ == tokenIdent && current.lit == "set" && p.peek().typ == tokenLBrace {
		return nil, fmt.Errorf("line %d: DSL1-only construct: set { ... } channel assignment is not supported in DSL2", current.line)
	}

	var base ChanExpr
	if current.typ == tokenIdent && current.lit == "Channel" && p.peek().typ == tokenDot {
		factory, err := p.parseChannelFactory()
		if err != nil {
			return nil, err
		}
		base = factory
	} else {
		if current.typ != tokenIdent {
			return nil, fmt.Errorf("line %d: expected channel expression", current.line)
		}

		name := p.parseChannelRefName()
		if value, ok := p.resolveAssignment(name); ok {
			base = value
		} else {
			base = ChanRef{Name: name}
		}
	}

	return p.parseChannelOperators(base)
}

func hasTokenType(types []tokenType, target tokenType) bool {
	for _, current := range types {
		if current == target {
			return true
		}
	}

	return false
}

func (p *parser) currentLocalScope() map[string]ChanExpr {
	if len(p.localScopes) == 0 {
		return nil
	}

	return p.localScopes[len(p.localScopes)-1]
}

func (p *parser) resolveAssignment(name string) (ChanExpr, bool) {
	if scope := p.currentLocalScope(); scope != nil {
		if value, ok := scope[name]; ok {
			return value, true
		}
	}

	value, ok := p.assignments[name]

	return value, ok
}

func (p *parser) parseChannelFactory() (ChanExpr, error) {
	if _, err := p.expectIdent("Channel"); err != nil {
		return nil, err
	}
	if _, err := p.expectType(tokenDot, "."); err != nil {
		return nil, err
	}

	name, err := p.expectType(tokenIdent, "channel factory name")
	if err != nil {
		return nil, err
	}
	if name.lit == "create" {
		return nil, fmt.Errorf("line %d: DSL1-only construct: Channel.create() is not supported in DSL2", name.line)
	}
	if _, ok := supportedChannelFactories[name.lit]; !ok {
		return nil, fmt.Errorf("line %d: unsupported factory: %s", name.line, name.lit)
	}
	if _, err = p.expectType(tokenLParen, "("); err != nil {
		return nil, err
	}

	factory := ChannelFactory{Name: name.lit}
	for {
		if p.current().typ == tokenRParen {
			p.pos++
			return factory, nil
		}

		argTokens, err := p.readExprTokens(tokenComma, tokenRParen)
		if err != nil {
			return nil, err
		}
		expr, err := parseExprTokens(argTokens)
		if err != nil {
			return nil, wrapLineError(argTokens[0].line, err)
		}
		factory.Args = append(factory.Args, expr)

		switch p.current().typ {
		case tokenComma:
			p.pos++
		case tokenRParen:
			p.pos++
			return factory, nil
		default:
			return nil, fmt.Errorf("line %d: expected , or )", p.current().line)
		}
	}
}

func parseExprTokens(tokens []token) (Expr, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expected expression")
	}

	if expr, err := parseTernaryExprTokens(tokens); expr != nil || err != nil {
		return expr, err
	}

	return parseLogicalOrExprTokens(tokens)
}

func parseBinaryExprTokens(tokens []token, operators []string, operandParser func([]token) (Expr, error)) (Expr, error) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	for index := len(tokens) - 1; index >= 0; index-- {
		current := tokens[index]
		switch current.typ {
		case tokenRParen:
			parenDepth++
		case tokenLParen:
			parenDepth--
		case tokenLBrace:
			braceDepth--
		case tokenRBrace:
			braceDepth++
		case tokenSymbol:
			switch current.lit {
			case "]":
				bracketDepth++
			case "[":
				bracketDepth--
			}
		}

		if parenDepth != 0 || bracketDepth != 0 || braceDepth != 0 || current.typ != tokenSymbol || !stringInSlice(current.lit, operators) {
			continue
		}

		leftTokens := tokens[:index]
		rightTokens := tokens[index+1:]
		if len(leftTokens) == 0 || len(rightTokens) == 0 {
			return nil, fmt.Errorf("unsupported expression %q", expressionText(tokens))
		}

		leftExpr, err := operandParser(leftTokens)
		if err != nil {
			return nil, err
		}

		rightExpr, err := operandParser(rightTokens)
		if err != nil {
			return nil, err
		}

		return BinaryExpr{Left: leftExpr, Op: current.lit, Right: rightExpr}, nil
	}

	return nil, nil
}

func parseVarExprTokens(tokens []token) (Expr, bool) {
	if len(tokens) == 0 || tokens[0].typ != tokenIdent {
		return nil, false
	}

	parts := []string{tokens[0].lit}
	for index := 1; index < len(tokens); index += 2 {
		if tokens[index].typ != tokenDot {
			return nil, false
		}
		if index+1 >= len(tokens) || tokens[index+1].typ != tokenIdent {
			return nil, false
		}
		parts = append(parts, tokens[index+1].lit)
	}

	if len(parts) == 1 {
		return VarExpr{Root: parts[0]}, true
	}

	return VarExpr{Root: parts[0], Path: strings.Join(parts[1:], ".")}, true
}

func expressionText(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	var builder strings.Builder
	for index, tok := range tokens {
		if index > 0 && needsSpaceBetween(tokens[index-1], tok) {
			builder.WriteByte(' ')
		}
		builder.WriteString(tok.lit)
	}

	return strings.TrimSpace(builder.String())
}

func needsSpaceBetween(prev, current token) bool {
	if prev.typ == tokenDot || current.typ == tokenDot {
		return false
	}

	if current.typ == tokenLParen {
		return false
	}

	if prev.typ == tokenLParen || current.typ == tokenRParen {
		return false
	}

	if prev.typ == tokenSymbol || current.typ == tokenSymbol {
		return true
	}

	if current.typ == tokenComma || current.typ == tokenColon || current.typ == tokenSemicolon {
		return false
	}

	if prev.typ == tokenComma || prev.typ == tokenColon || prev.typ == tokenSemicolon {
		return true
	}

	return true
}

func stringInSlice(target string, values []string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func (p *parser) parseChannelRefName() string {
	parts := []string{p.current().lit}
	p.pos++

	for p.current().typ == tokenDot {
		if p.peek().typ != tokenIdent || p.peekN(2).typ == tokenLParen || p.peekN(2).typ == tokenLBrace {
			break
		}

		p.pos++
		parts = append(parts, p.current().lit)
		p.pos++
	}

	return strings.Join(parts, ".")
}

func (p *parser) parseChannelOperators(base ChanExpr) (ChanExpr, error) {
	operators := []ChannelOperator{}

	for p.current().typ == tokenDot && p.peek().typ == tokenIdent {
		if p.peekN(2).typ != tokenLParen && p.peekN(2).typ != tokenLBrace {
			break
		}

		p.pos++
		name := p.current()
		p.pos++

		if _, ok := supportedChannelOperators[name.lit]; !ok {
			return nil, fmt.Errorf("line %d: unsupported operator: %s", name.line, name.lit)
		}

		operator, err := p.parseChannelOperator(name)
		if err != nil {
			return nil, err
		}
		operators = append(operators, operator)
	}

	if len(operators) == 0 {
		return base, nil
	}

	return ChannelChain{Source: base, Operators: operators}, nil
}

func (p *parser) parseChannelOperator(name token) (ChannelOperator, error) {
	operator := ChannelOperator{Name: name.lit}

	switch p.current().typ {
	case tokenLBrace:
		closureExpr, closure, err := p.parseClosureBody()
		if err != nil {
			return ChannelOperator{}, err
		}
		operator.Closure = closure
		operator.ClosureExpr = &closureExpr
		if isDeprecatedChannelOperator(name.lit) {
			warnDeprecatedParsedChannelOperator(name)
		}
		return operator, nil
	case tokenLParen:
		channels, args, err := p.parseChannelOperatorArgs(name)
		if err != nil {
			return ChannelOperator{}, err
		}
		operator.Channels = channels
		operator.Args = args
		if isDeprecatedChannelOperator(name.lit) {
			warnDeprecatedParsedChannelOperator(name)
		}
		return operator, nil
	default:
		return ChannelOperator{}, fmt.Errorf("line %d: expected operator invocation", p.current().line)
	}
}

func isDeprecatedChannelOperator(name string) bool {
	switch name {
	case "countFasta", "countFastq", "countJson", "countLines", "merge", "toInteger":
		return true
	default:
		return false
	}
}

func warnDeprecatedParsedChannelOperator(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: deprecated channel operator %q at line %d parsed for compatibility\n", name.lit, name.line)
}

func (p *parser) parseChannelOperatorArgs(name token) ([]ChanExpr, []Expr, error) {
	if _, err := p.expectType(tokenLParen, "("); err != nil {
		return nil, nil, err
	}

	if p.current().typ == tokenRParen {
		p.pos++
		return nil, nil, nil
	}

	switch name.lit {
	case "combine", "concat", "cross", "join", "merge", "mix", "tap":
		channels := []ChanExpr{}
		for {
			channel, err := p.parseChanExpr(tokenComma, tokenRParen)
			if err != nil {
				return nil, nil, err
			}
			channels = append(channels, channel)

			switch p.current().typ {
			case tokenComma:
				p.pos++
			case tokenRParen:
				p.pos++
				return channels, nil, nil
			default:
				return nil, nil, fmt.Errorf("line %d: expected , or )", p.current().line)
			}
		}
	default:
		args := []Expr{}
		for {
			argTokens, err := p.readExprTokens(tokenComma, tokenRParen)
			if err != nil {
				return nil, nil, err
			}
			arg, err := parseExprTokens(argTokens)
			if err != nil {
				return nil, nil, wrapLineError(argTokens[0].line, err)
			}
			args = append(args, arg)

			switch p.current().typ {
			case tokenComma:
				p.pos++
			case tokenRParen:
				p.pos++
				return nil, args, nil
			default:
				return nil, nil, fmt.Errorf("line %d: expected , or )", p.current().line)
			}
		}
	}
}

func (p *parser) parseClosureBody() (ClosureExpr, string, error) {
	if _, err := p.expectType(tokenLBrace, "{"); err != nil {
		return ClosureExpr{}, "", err
	}

	depth := 1
	tokens := []token{}
	for depth > 0 {
		current := p.current()
		if current.typ == tokenEOF {
			return ClosureExpr{}, "", fmt.Errorf("line %d: unterminated closure", current.line)
		}

		switch current.typ {
		case tokenLBrace:
			depth++
			tokens = append(tokens, current)
		case tokenRBrace:
			depth--
			if depth > 0 {
				tokens = append(tokens, current)
			}
		default:
			tokens = append(tokens, current)
		}

		p.pos++
	}

	closureExpr, err := parseClosureInnerTokens(tokens)
	if err != nil {
		return ClosureExpr{}, "", err
	}

	return closureExpr, strings.TrimSpace(renderClosureTokens(tokens)), nil
}

func joinTokens(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		switch tok.typ {
		case tokenLBrace, tokenRBrace, tokenLParen, tokenRParen, tokenColon, tokenComma, tokenDot, tokenSemicolon, tokenAssign, tokenPipe:
			parts = append(parts, tok.lit)
		default:
			parts = append(parts, tok.lit)
		}
	}

	return strings.Join(parts, " ")
}

func renderClosureTokens(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	var builder strings.Builder
	for index, tok := range tokens {
		if index > 0 && needsSpaceBetween(tokens[index-1], tok) {
			builder.WriteByte(' ')
		}

		builder.WriteString(renderClosureToken(tok))
	}

	return builder.String()
}

func renderClosureToken(tok token) string {
	if tok.typ != tokenString {
		return tok.lit
	}

	return "'" + strings.ReplaceAll(tok.lit, "'", "\\'") + "'"
}

func (p *parser) parseDottedIdent() string {
	parts := []string{p.current().lit}
	p.pos++

	for p.current().typ == tokenDot {
		p.pos++
		if p.current().typ != tokenIdent {
			break
		}
		parts = append(parts, p.current().lit)
		p.pos++
	}

	return strings.Join(parts, ".")
}

func (p *parser) readExprTokens(terminators ...tokenType) ([]token, error) {
	start := p.current()
	tokens := []token{}
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0

	for {
		current := p.current()
		if current.typ == tokenEOF {
			if len(tokens) == 0 {
				return nil, fmt.Errorf("line %d: expected expression", start.line)
			}
			return tokens, nil
		}

		if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 && hasTokenType(terminators, current.typ) {
			if len(tokens) == 0 {
				return nil, fmt.Errorf("line %d: expected expression", current.line)
			}
			return tokens, nil
		}

		switch current.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			if parenDepth > 0 {
				parenDepth--
			}
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			if braceDepth > 0 {
				braceDepth--
			}
		case tokenSymbol:
			switch current.lit {
			case "[":
				bracketDepth++
			case "]":
				if bracketDepth > 0 {
					bracketDepth--
				}
			}
		}

		tokens = append(tokens, current)
		p.pos++
	}
}

func (p *parser) parseProcess() (*Process, error) {
	if _, err := p.expectIdent("process"); err != nil {
		return nil, err
	}

	name, err := p.expectType(tokenIdent, "process name")
	if err != nil {
		return nil, err
	}

	if _, err = p.expectType(tokenLBrace, "{"); err != nil {
		return nil, err
	}

	proc := &Process{
		Name:       name.lit,
		Labels:     []string{},
		Directives: make(map[string]any),
		PublishDir: []*PublishDir{},
		Env:        make(map[string]string),
	}

	for {
		p.skipNewlines()
		current := p.current()
		switch current.typ {
		case tokenEOF:
			line := p.previous().line
			if line == 0 {
				line = current.line
			}
			return nil, fmt.Errorf("line %d: expected } to close process %q", line, proc.Name)
		case tokenRBrace:
			p.pos++
			return proc, nil
		case tokenIdent:
			label := current
			p.pos++
			if p.current().typ == tokenColon {
				p.pos++
				if err := p.parseSection(proc, label); err != nil {
					return nil, err
				}
				continue
			}

			if err := p.parseDirective(proc, label); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("line %d: unexpected token %q in process %q", current.line, current.lit, proc.Name)
		}
	}
}

func (p *parser) parseSection(proc *Process, label token) error {
	switch label.lit {
	case "input":
		decls, err := p.parseDeclarations(label.lit)
		if err != nil {
			return err
		}
		proc.Input = append(proc.Input, decls...)
	case "output":
		decls, err := p.parseDeclarations(label.lit)
		if err != nil {
			return err
		}
		proc.Output = append(proc.Output, decls...)
	case "script":
		script, err := p.parseScript()
		if err != nil {
			return err
		}
		proc.Script = script
	case "stub":
		stub, err := p.parseRawSectionBody()
		if err != nil {
			return err
		}
		proc.Stub = stub
		warnUnsupportedProcessSection(label)
	case "exec":
		execBody, err := p.parseRawSectionBody()
		if err != nil {
			return err
		}
		proc.Exec = execBody
		warnUnsupportedProcessSection(label)
	case "shell":
		shell, err := p.parseRawSectionBody()
		if err != nil {
			return err
		}
		proc.Shell = shell
		warnUnsupportedProcessSection(label)
	case "when":
		when, err := p.parseRawSectionBody()
		if err != nil {
			return err
		}
		proc.When = when
		warnUnsupportedProcessSection(label)
	default:
		return fmt.Errorf("line %d: unsupported section %q", label.line, label.lit)
	}

	return nil
}

func warnUnsupportedProcessSection(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: unsupported process section %q at line %d will not be translated\n", name.lit, name.line)
}

func (p *parser) parseDeclarations(section string) ([]*Declaration, error) {
	decls := []*Declaration{}
	for {
		p.skipNewlines()
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenRBrace {
			return decls, nil
		}

		if current.typ == tokenIdent && p.peek().typ == tokenColon {
			return decls, nil
		}

		decl, err := p.parseDeclarationLine(section)
		if err != nil {
			return nil, err
		}
		decls = append(decls, decl)
	}
}

func (p *parser) parseDeclarationLine(section string) (*Declaration, error) {
	lineTokens := p.readLineTokens()
	if len(lineTokens) == 0 {
		return nil, fmt.Errorf("line %d: expected declaration", p.current().line)
	}

	for _, tok := range lineTokens {
		if tok.typ == tokenIdent && tok.lit == "into" {
			return nil, fmt.Errorf("line %d: DSL 1 syntax is not supported", tok.line)
		}
	}

	primaryTokens, qualifierTokens, err := splitDeclarationLineTokens(lineTokens)
	if err != nil {
		return nil, err
	}

	decl, err := parseDeclarationPrimary(primaryTokens)
	if err != nil {
		return nil, err
	}
	decl.Raw = joinTokens(lineTokens)

	for _, qualifier := range qualifierTokens {
		if err = applyDeclarationQualifier(decl, qualifier, section); err != nil {
			return nil, err
		}
	}

	if section == "output" && decl.Kind == "eval" {
		warnUnsupportedOutputType(primaryTokens[0])
	}

	return decl, nil
}

func splitDeclarationLineTokens(lineTokens []token) ([]token, [][]token, error) {
	segments := splitTopLevelCommaSegments(lineTokens)

	primary := trimDeclarationTokens(segments[0])
	if len(primary) == 0 {
		return nil, nil, fmt.Errorf("line %d: expected declaration", lineTokens[0].line)
	}

	if primary[0].typ == tokenIdent && primary[0].lit == "tuple" {
		primarySegments := make([][]token, 0, len(segments))
		primarySegments = append(primarySegments, segments[0])
		qualifiers := make([][]token, 0, len(segments)-1)

		for _, segment := range segments[1:] {
			trimmed := trimDeclarationTokens(segment)
			if len(trimmed) == 0 {
				continue
			}

			if isDeclarationQualifierTokens(trimmed) {
				qualifiers = append(qualifiers, trimmed)
				continue
			}

			if len(qualifiers) != 0 {
				return nil, nil, fmt.Errorf("line %d: unsupported declaration qualifier %q", trimmed[0].line, joinTokens(trimmed))
			}

			primarySegments = append(primarySegments, segment)
		}

		return joinDeclarationSegments(primarySegments), qualifiers, nil
	}

	qualifiers := make([][]token, 0, len(segments)-1)
	for _, segment := range segments[1:] {
		trimmed := trimDeclarationTokens(segment)
		if len(trimmed) == 0 {
			continue
		}
		qualifiers = append(qualifiers, trimmed)
	}

	return primary, qualifiers, nil
}

func parseDeclarationPrimary(tokens []token) (*Declaration, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expected declaration")
	}

	if tokens[0].typ == tokenIdent && tokens[0].lit == "tuple" {
		return parseTupleDeclaration(tokens)
	}

	decl := &Declaration{Kind: tokens[0].lit}
	if len(tokens) == 1 && (decl.Kind == "stdout" || decl.Kind == "stdin") {
		return decl, nil
	}

	if decl.Kind == "env" && isCallLikeDeclaration(tokens) {
		name, err := parseDeclarationNameArg(tokens[2 : len(tokens)-1])
		if err != nil {
			return nil, wrapLineError(tokens[0].line, err)
		}
		decl.Name = name
		return decl, nil
	}

	if decl.Kind == "eval" && isCallLikeDeclaration(tokens) {
		if len(tokens) == 3 {
			return nil, fmt.Errorf("line %d: eval requires an argument", tokens[0].line)
		}
		expr, err := parseExprTokens(tokens[2 : len(tokens)-1])
		if err != nil {
			return nil, wrapLineError(tokens[0].line, err)
		}
		decl.Expr = expr
		return decl, nil
	}

	if len(tokens) == 2 && tokens[1].typ == tokenIdent {
		decl.Name = tokens[1].lit
		return decl, nil
	}

	if len(tokens) > 1 {
		expr, err := parseExprTokens(tokens[1:])
		if err != nil {
			return nil, wrapLineError(tokens[0].line, err)
		}
		decl.Expr = expr
	}

	return decl, nil
}

func applyDeclarationQualifier(decl *Declaration, tokens []token, section string) error {
	if len(tokens) < 3 || tokens[0].typ != tokenIdent || tokens[1].typ != tokenColon {
		return fmt.Errorf("line %d: unsupported declaration qualifier %q", tokens[0].line, joinTokens(tokens))
	}

	name := tokens[0]
	valueTokens := tokens[2:]
	if len(valueTokens) == 0 {
		return fmt.Errorf("line %d: qualifier %q requires a value", name.line, name.lit)
	}

	switch name.lit {
	case "emit":
		value, err := parseExprTokens(valueTokens)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		switch emitValue := value.(type) {
		case StringExpr:
			decl.Emit = emitValue.Value
		case VarExpr:
			if emitValue.Path != "" {
				return fmt.Errorf("line %d: emit qualifier expects a string or identifier", name.line)
			}
			decl.Emit = emitValue.Root
		default:
			return fmt.Errorf("line %d: emit qualifier expects a string or identifier", name.line)
		}
	case "optional":
		if len(valueTokens) == 1 && valueTokens[0].typ == tokenIdent {
			switch valueTokens[0].lit {
			case "true":
				decl.Optional = true
				return nil
			case "false":
				decl.Optional = false
				return nil
			}
		}
		value, err := parseExprTokens(valueTokens)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		boolValue, ok := value.(BoolExpr)
		if !ok {
			return fmt.Errorf("line %d: optional qualifier expects a boolean", name.line)
		}
		decl.Optional = boolValue.Value
	case "topic":
		if _, err := parseExprTokens(valueTokens); err != nil {
			return wrapLineError(name.line, err)
		}
		if section == "output" {
			warnUnsupportedOutputQualifier(name)
		}
	default:
		return fmt.Errorf("line %d: unsupported declaration qualifier %q", name.line, name.lit)
	}

	return nil
}

func warnUnsupportedOutputType(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: unsupported output type %q at line %d will not be translated\n", name.lit, name.line)
}

func (p *parser) parseScript() (string, error) {
	body, err := p.parseRawSectionBody()
	if err != nil {
		return "", err
	}

	if body == "" {
		return "", fmt.Errorf("line %d: expected script string", p.current().line)
	}

	return body, nil
}

func (p *parser) parseRawSectionBody() (string, error) {
	p.skipNewlines()
	current := p.current()
	if current.typ == tokenString {
		p.pos++
		p.discardLineRemainder()
		return current.lit, nil
	}

	lineTokens := p.readRawLineTokens()
	if len(lineTokens) == 0 {
		return "", fmt.Errorf("line %d: expected section body", current.line)
	}

	return p.rawTokenText(lineTokens), nil
}

func (p *parser) parseRawBraceBody(openBrace token, description string) (string, error) {
	start := openBrace.end
	depth := 1

	for {
		current := p.current()
		switch current.typ {
		case tokenEOF:
			return "", fmt.Errorf("line %d: expected } to close %s", openBrace.line, description)
		case tokenLBrace:
			depth++
		case tokenRBrace:
			depth--
			if depth == 0 {
				end := current.start
				p.pos++
				if start < 0 || end < start || end > len(p.source) {
					return "", nil
				}

				return strings.TrimSpace(string(p.source[start:end])), nil
			}
		}

		p.pos++
	}
}

func (p *parser) rawTokenText(tokens []token) string {
	if len(tokens) == 0 {
		return ""
	}

	start := tokens[0].start
	end := tokens[len(tokens)-1].end
	if start < 0 || end < start || end > len(p.source) {
		return joinTokens(tokens)
	}

	return strings.TrimSpace(string(p.source[start:end]))
}

func (p *parser) parseDirective(proc *Process, name token) error {
	args := p.readLineTokens()

	switch name.lit {
	case "cpus", "memory", "time", "disk":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Directives[name.lit] = expr
	case "label":
		value, err := parseDirectiveStringValue(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Labels = append(proc.Labels, value)
	case "container":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		strExpr, ok := expr.(StringExpr)
		if !ok {
			return fmt.Errorf("line %d: container expects a string literal", name.line)
		}
		proc.Container = strExpr.Value
	case "tag":
		value, err := parseDirectiveText(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Tag = value
	case "beforeScript":
		value, err := parseDirectiveStringValue(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.BeforeScript = value
	case "afterScript":
		value, err := parseDirectiveStringValue(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.AfterScript = value
	case "module":
		value, err := parseDirectiveStringValue(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Module = value
	case "cache":
		value, err := parseDirectiveStringValue(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Cache = value
		warnStoredDirective(name)
	case "maxForks":
		value, err := parseIntValue(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.MaxForks = value
	case "errorStrategy":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		strExpr, ok := expr.(StringExpr)
		if !ok {
			return fmt.Errorf("line %d: errorStrategy expects a string literal", name.line)
		}
		proc.ErrorStrat = strExpr.Value
	case "maxRetries":
		value, err := parseIntValue(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.MaxRetries = value
	case "publishDir":
		publishDir, err := parsePublishDir(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.PublishDir = append(proc.PublishDir, publishDir)
	case "env":
		key, value, err := parseEnv(args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Env[key] = value
	case "scratch", "storeDir", "queue", "clusterOptions", "executor", "debug", "secret":
		expr, err := parseDirectiveExpr(name.lit, args)
		if err != nil {
			return wrapLineError(name.line, err)
		}
		proc.Directives[name.lit] = expr
		warnStoredDirective(name)
	default:
		warnUnsupportedDirective(name)
	}

	return nil
}

func parseDirectiveExpr(name string, args []token) (Expr, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s requires an argument", name)
	}

	expr, err := parseExprTokens(args)
	if err != nil {
		return nil, err
	}

	if stringExpr, ok := expr.(StringExpr); ok {
		switch name {
		case "memory":
			value, err := parseMemoryValue(stringExpr.Value)
			if err != nil {
				return nil, err
			}
			return IntExpr{Value: value}, nil
		case "time":
			value, err := parseTimeValue(stringExpr.Value)
			if err != nil {
				return nil, err
			}
			return IntExpr{Value: value}, nil
		case "disk":
			value, err := parseDiskValue(stringExpr.Value)
			if err != nil {
				return nil, err
			}
			return IntExpr{Value: value}, nil
		}
	}

	return expr, nil
}

func wrapLineError(line int, err error) error {
	if err == nil {
		return nil
	}

	if strings.HasPrefix(err.Error(), "line ") {
		return err
	}

	return fmt.Errorf("line %d: %w", line, err)
}

func parseDirectiveStringValue(name string, args []token) (string, error) {
	expr, err := parseDirectiveExpr(name, args)
	if err != nil {
		return "", err
	}

	stringExpr, ok := expr.(StringExpr)
	if !ok {
		return "", fmt.Errorf("%s expects a string literal", name)
	}

	return stringExpr.Value, nil
}

func parseDirectiveText(name string, args []token) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("%s requires an argument", name)
	}

	if _, err := parseDirectiveExpr(name, args); err != nil {
		return "", err
	}

	return expressionText(args), nil
}

func warnStoredDirective(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: unsupported directive %q at line %d stored without translation support\n", name.lit, name.line)
}

func parseIntValue(tokens []token) (int, error) {
	expr, err := parseExprTokens(tokens)
	if err != nil {
		return 0, err
	}

	intExpr, ok := expr.(IntExpr)
	if !ok {
		return 0, fmt.Errorf("expected integer value")
	}

	return intExpr.Value, nil
}

func parsePublishDir(tokens []token) (*PublishDir, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("publishDir requires a path")
	}

	pathExpr, err := parseExprTokens([]token{tokens[0]})
	if err != nil {
		return nil, err
	}

	pathValue, ok := pathExpr.(StringExpr)
	if !ok {
		return nil, fmt.Errorf("publishDir path must be a string literal")
	}

	publishDir := &PublishDir{Path: pathValue.Value, Mode: "copy"}
	index := 1
	for index < len(tokens) {
		if tokens[index].typ == tokenComma {
			index++
		}
		if index >= len(tokens) {
			break
		}

		if index+2 >= len(tokens) || tokens[index].typ != tokenIdent || tokens[index+1].typ != tokenColon {
			return nil, fmt.Errorf("unsupported publishDir option %q", joinTokens(tokens[index:]))
		}

		key := tokens[index].lit
		valueExpr, err := parseExprTokens([]token{tokens[index+2]})
		if err != nil {
			return nil, err
		}

		valueString, ok := valueExpr.(StringExpr)
		if !ok {
			return nil, fmt.Errorf("publishDir option %q must be a string literal", key)
		}

		switch key {
		case "pattern":
			publishDir.Pattern = valueString.Value
		case "mode":
			publishDir.Mode = valueString.Value
		default:
			return nil, fmt.Errorf("unsupported publishDir option %q", key)
		}

		index += 3
	}

	return publishDir, nil
}

func parseEnv(tokens []token) (string, string, error) {
	if len(tokens) != 3 || tokens[0].typ != tokenIdent || tokens[1].typ != tokenColon {
		return "", "", fmt.Errorf("env expects KEY: 'value'")
	}

	valueExpr, err := parseExprTokens([]token{tokens[2]})
	if err != nil {
		return "", "", err
	}

	valueString, ok := valueExpr.(StringExpr)
	if !ok {
		return "", "", fmt.Errorf("env value must be a string literal")
	}

	return tokens[0].lit, valueString.Value, nil
}

func warnUnsupportedDirective(name token) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: ignoring unsupported directive %q at line %d\n", name.lit, name.line)
}

func (p *parser) skipNewlines() {
	for p.current().typ == tokenNewline {
		p.pos++
	}
}

func (p *parser) skipWorkflowSeparators() {
	for {
		switch p.current().typ {
		case tokenNewline, tokenSemicolon:
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) readLineTokens() []token {
	lineTokens := []token{}
	for {
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenNewline || current.typ == tokenRBrace {
			break
		}
		lineTokens = append(lineTokens, current)
		p.pos++
	}
	if p.current().typ == tokenNewline {
		p.pos++
	}

	return lineTokens
}

func (p *parser) readWorkflowPublishLineTokens() []token {
	lineTokens := []token{}
	braceDepth := 0
	parenDepth := 0

	for {
		current := p.current()
		if current.typ == tokenEOF {
			break
		}
		if braceDepth == 0 && parenDepth == 0 && (current.typ == tokenNewline || current.typ == tokenRBrace) {
			break
		}

		lineTokens = append(lineTokens, current)
		p.pos++

		switch current.typ {
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			if braceDepth > 0 {
				braceDepth--
			}
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			if parenDepth > 0 {
				parenDepth--
			}
		}
	}
	if p.current().typ == tokenNewline {
		p.pos++
	}

	return lineTokens
}

func (p *parser) readRawLineTokens() []token {
	lineTokens := []token{}
	for {
		current := p.current()
		if current.typ == tokenEOF || current.typ == tokenNewline {
			break
		}
		lineTokens = append(lineTokens, current)
		p.pos++
	}
	if p.current().typ == tokenNewline {
		p.pos++
	}

	return lineTokens
}

func (p *parser) discardLineRemainder() {
	for p.current().typ != tokenEOF && p.current().typ != tokenNewline && p.current().typ != tokenRBrace {
		p.pos++
	}
	if p.current().typ == tokenNewline {
		p.pos++
	}
}

func (p *parser) expectType(expected tokenType, description string) (token, error) {
	current := p.current()
	if current.typ != expected {
		return token{}, fmt.Errorf("line %d: expected %s", current.line, description)
	}
	p.pos++

	return current, nil
}

func (p *parser) expectIdent(expected string) (token, error) {
	current := p.current()
	if current.typ != tokenIdent || current.lit != expected {
		return token{}, fmt.Errorf("line %d: expected %q", current.line, expected)
	}
	p.pos++

	return current, nil
}

func (p *parser) current() token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos]
}

func (p *parser) peek() token {
	if p.pos+1 >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos+1]
}

func (p *parser) peekN(offset int) token {
	if p.pos+offset >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}

	return p.tokens[p.pos+offset]
}

func (p *parser) previous() token {
	if p.pos == 0 {
		return token{}
	}

	return p.tokens[p.pos-1]
}

// Parse parses a Nextflow DSL 2 file and returns the AST.
func Parse(r io.Reader) (*Workflow, error) {
	input, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	tokens, err := lex(string(input))
	if err != nil {
		return nil, err
	}

	return newParser(tokens, string(input)).parseWorkflow()
}

func parseMemoryValue(value string) (int, error) {
	matches := memoryRE.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("unsupported memory value %q", value)
	}

	amount, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, err
	}

	unit := strings.ToLower(matches[2])
	if unit == "gb" || unit == "g" {
		return amount * 1024, nil
	}

	return amount, nil
}

func parseDiskValue(value string) (int, error) {
	matches := diskRE.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("unsupported disk value %q", value)
	}

	return strconv.Atoi(matches[1])
}

func parseTimeValue(value string) (int, error) {
	matches := timeRE.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return 0, fmt.Errorf("unsupported time value %q", value)
	}

	amount, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, err
	}

	switch strings.ToLower(matches[2]) {
	case "m", "min", "mins", "minute", "minutes":
		return amount, nil
	case "h", "hr", "hrs", "hour", "hours":
		return amount * 60, nil
	case "d", "day", "days":
		return amount * 24 * 60, nil
	default:
		return 0, fmt.Errorf("unsupported time unit %q", matches[2])
	}
}
