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
	"reflect"
	"regexp"
	"strings"
)

var groovyInterpolationPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

func resolveInterpolation(exprText string, vars map[string]any) (any, error) {
	parts := strings.Split(exprText, ".")
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("unsupported interpolation %q", exprText)
	}

	path := ""
	if len(parts) > 1 {
		path = strings.Join(parts[1:], ".")
	}

	return resolveExprPath(parts[0], path, vars)
}

func interpolateGroovyString(value string, vars map[string]any) (string, error) {
	matches := groovyInterpolationPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}

	var builder strings.Builder
	last := 0

	for _, match := range matches {
		builder.WriteString(value[last:match[0]])

		exprText := strings.TrimSpace(value[match[2]:match[3]])
		resolved, err := resolveInterpolation(exprText, vars)
		if err != nil {
			return "", err
		}

		builder.WriteString(fmt.Sprint(resolved))
		last = match[1]
	}

	builder.WriteString(value[last:])

	return builder.String(), nil
}

func resolveExprPath(root, path string, vars map[string]any) (any, error) {
	if vars == nil {
		return nil, fmt.Errorf("unknown variable %q", root)
	}

	current, ok := vars[root]
	if !ok {
		return nil, fmt.Errorf("unknown variable %q", root)
	}

	if path == "" {
		return current, nil
	}

	for _, part := range strings.Split(path, ".") {
		var err error
		current, err = lookupVariablePart(current, part)
		if err != nil {
			return nil, fmt.Errorf("unknown variable %q", root+"."+path)
		}
	}

	return current, nil
}

func lookupVariablePart(current any, part string) (any, error) {
	switch typed := current.(type) {
	case map[string]any:
		value, ok := typed[part]
		if !ok {
			return nil, fmt.Errorf("missing key %q", part)
		}
		return value, nil
	}

	refValue := reflect.ValueOf(current)
	if !refValue.IsValid() {
		return nil, fmt.Errorf("invalid value")
	}

	if refValue.Kind() == reflect.Map && refValue.Type().Key().Kind() == reflect.String {
		result := refValue.MapIndex(reflect.ValueOf(part))
		if !result.IsValid() {
			return nil, fmt.Errorf("missing key %q", part)
		}
		return result.Interface(), nil
	}

	return nil, fmt.Errorf("unsupported lookup target %T", current)
}

func evalBinaryExpr(expr BinaryExpr, vars map[string]any) (any, error) {
	left, err := EvalExpr(expr.Left, vars)
	if err != nil {
		return nil, err
	}

	right, err := EvalExpr(expr.Right, vars)
	if err != nil {
		return nil, err
	}

	leftInt, ok := left.(int)
	if !ok {
		return nil, fmt.Errorf("unsupported arithmetic operand %T", left)
	}

	rightInt, ok := right.(int)
	if !ok {
		return nil, fmt.Errorf("unsupported arithmetic operand %T", right)
	}

	switch expr.Op {
	case "+":
		return leftInt + rightInt, nil
	case "-":
		return leftInt - rightInt, nil
	case "*":
		return leftInt * rightInt, nil
	case "/":
		if rightInt == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		return leftInt / rightInt, nil
	case ">":
		return leftInt > rightInt, nil
	case "<":
		return leftInt < rightInt, nil
	default:
		return nil, fmt.Errorf("unsupported operator %q", expr.Op)
	}
}

// EvalExpr evaluates a simple Groovy expression given a context of variable
// bindings. Returns the result or an error.
func EvalExpr(expr Expr, vars map[string]any) (any, error) {
	switch value := expr.(type) {
	case IntExpr:
		return value.Value, nil
	case StringExpr:
		return interpolateGroovyString(value.Value, vars)
	case ParamsExpr:
		return resolveExprPath("params", value.Path, vars)
	case VarExpr:
		return resolveExprPath(value.Root, value.Path, vars)
	case BinaryExpr:
		return evalBinaryExpr(value, vars)
	case UnsupportedExpr:
		return nil, fmt.Errorf("unsupported expression %q", value.Text)
	case BoolExpr:
		return value.Value, nil
	case nil:
		return nil, fmt.Errorf("unsupported expression <nil>")
	default:
		return nil, fmt.Errorf("unsupported expression %T", expr)
	}
}
