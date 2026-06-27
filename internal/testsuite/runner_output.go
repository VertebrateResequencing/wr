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

package testsuite

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	conveyJSONOpen  = ">->->OPEN-JSON->->->"
	conveyJSONClose = "<-<-<-CLOSE-JSON<-<-<"
)

type conveyScope struct {
	Title      string            `json:"Title"`
	File       string            `json:"File"`
	Line       int               `json:"Line"`
	Depth      int               `json:"Depth"`
	Assertions []conveyAssertion `json:"Assertions"`
	Output     string            `json:"Output"`
}

type conveyAssertion struct {
	File       string          `json:"File"`
	Line       int             `json:"Line"`
	Expected   string          `json:"Expected"`
	Actual     string          `json:"Actual"`
	Failure    string          `json:"Failure"`
	Error      json.RawMessage `json:"Error"`
	StackTrace string          `json:"StackTrace"`
	Skipped    bool            `json:"Skipped"`
}

func formatLaneLog(raw string) string {
	clean, blocks, ok := extractConveyJSON(raw)
	if !ok || len(blocks) == 0 {
		return raw
	}

	scopes, err := parseConveyScopes(blocks)
	if err != nil {
		return raw
	}

	report := formatConveyFailures(scopes)
	if report == "" {
		return clean
	}

	return report + clean
}

func extractConveyJSON(raw string) (string, []string, bool) {
	var clean strings.Builder

	blocks := make([]string, 0)
	remaining := raw

	for {
		start := strings.Index(remaining, conveyJSONOpen)
		if start == -1 {
			clean.WriteString(remaining)

			return clean.String(), blocks, true
		}

		clean.WriteString(remaining[:start])
		afterOpen := remaining[start+len(conveyJSONOpen):]

		before, after, ok := strings.Cut(afterOpen, conveyJSONClose)
		if !ok {
			return "", nil, false
		}

		blocks = append(blocks, before)
		remaining = after
	}
}

func parseConveyScopes(blocks []string) ([]conveyScope, error) {
	scopes := make([]conveyScope, 0)

	for _, block := range blocks {
		parsed, err := parseConveyScopeBlock(block)
		if err != nil {
			return nil, err
		}

		scopes = append(scopes, parsed...)
	}

	return scopes, nil
}

func parseConveyScopeBlock(block string) ([]conveyScope, error) {
	body := strings.TrimSpace(block)

	body = strings.TrimSuffix(body, ",")
	if body == "" {
		return nil, nil
	}

	var scopes []conveyScope
	if err := json.Unmarshal([]byte("["+body+"]"), &scopes); err != nil {
		return nil, fmt.Errorf("parse GoConvey JSON: %w", err)
	}

	return scopes, nil
}

func formatConveyFailures(scopes []conveyScope) string {
	writer := conveyFailureWriter{}
	path := make([]conveyScope, 0)

	for _, scope := range scopes {
		path = appendScopePath(path, scope)
		writer.writeAssertions(path, scope.Assertions)
	}

	return writer.String()
}

func appendScopePath(path []conveyScope, scope conveyScope) []conveyScope {
	depth := max(scope.Depth, 1)
	if len(path) >= depth {
		path = path[:depth-1]
	}

	return append(path, scope)
}

type conveyFailureWriter struct {
	out           strings.Builder
	wroteFailures bool
	wroteErrors   bool
}

func (w *conveyFailureWriter) String() string {
	return w.out.String()
}

func (w *conveyFailureWriter) writeAssertions(path []conveyScope, assertions []conveyAssertion) {
	for _, assertion := range assertions {
		w.writeFailure(path, assertion)
		w.writeError(path, assertion)
	}
}

func (w *conveyFailureWriter) writeFailure(path []conveyScope, assertion conveyAssertion) {
	if assertion.Failure == "" {
		return
	}

	if !w.wroteFailures {
		w.out.WriteString("\nFailures:\n\n")
		w.wroteFailures = true
	}

	writeConveyContext(&w.out, path)
	writeConveyAssertion(&w.out, assertion)
}

func (w *conveyFailureWriter) writeError(path []conveyScope, assertion conveyAssertion) {
	if !assertionHasError(assertion) {
		return
	}

	if !w.wroteErrors {
		w.out.WriteString("\nErrors:\n\n")
		w.wroteErrors = true
	}

	writeConveyContext(&w.out, path)
	writeConveyError(&w.out, assertion)
}

func writeConveyContext(out *strings.Builder, path []conveyScope) {
	out.WriteString("  Context:\n")

	for index, scope := range path {
		out.WriteString(strings.Repeat("  ", index+1))
		out.WriteString(scope.Title)
		out.WriteByte('\n')
	}

	out.WriteByte('\n')
}

func writeConveyAssertion(out *strings.Builder, assertion conveyAssertion) {
	out.WriteString("  * ")
	out.WriteString(assertion.File)
	out.WriteString(" \n  Line ")
	fmt.Fprint(out, assertion.Line)
	out.WriteString(":\n")
	out.WriteString(indentConveyDetail(assertion.Failure))
	out.WriteByte('\n')

	if assertion.StackTrace != "" {
		out.WriteString(indentConveyDetail(assertion.StackTrace))
		out.WriteByte('\n')
	}
}

func writeConveyError(out *strings.Builder, assertion conveyAssertion) {
	out.WriteString("  * ")
	out.WriteString(assertion.File)
	out.WriteString(" \n  Line ")
	fmt.Fprint(out, assertion.Line)
	out.WriteString(": - ")
	out.WriteString(conveyErrorString(assertion.Error))
	out.WriteString(" \n")

	if assertion.StackTrace != "" {
		out.WriteString(indentConveyDetail(assertion.StackTrace))
		out.WriteByte('\n')
	}
}

func indentConveyDetail(detail string) string {
	lines := strings.Split(detail, "\n")

	for index, line := range lines {
		lines[index] = "  " + line
	}

	return strings.Join(lines, "\n")
}

func assertionHasError(assertion conveyAssertion) bool {
	trimmed := strings.TrimSpace(string(assertion.Error))

	return trimmed != "" && trimmed != "null"
}

func conveyErrorString(raw json.RawMessage) string {
	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return message
	}

	return string(raw)
}
