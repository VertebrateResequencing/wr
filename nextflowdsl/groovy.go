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
	"strconv"
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

func evalTernaryExpr(expr TernaryExpr, vars map[string]any) (any, error) {
	if expr.Cond == nil {
		value, err := EvalExpr(expr.True, vars)
		if err != nil {
			return nil, err
		}

		if isTruthy(value) {
			return value, nil
		}

		return EvalExpr(expr.False, vars)
	}

	condition, err := EvalExpr(expr.Cond, vars)
	if err != nil {
		return nil, err
	}

	if isTruthy(condition) {
		return EvalExpr(expr.True, vars)
	}

	return EvalExpr(expr.False, vars)
}

func evalUnaryExpr(expr UnaryExpr, vars map[string]any) (any, error) {
	operand, err := EvalExpr(expr.Operand, vars)
	if err != nil {
		return nil, err
	}

	switch expr.Op {
	case "!":
		value, err := evalBoolOperand(operand, expr.Op)
		if err != nil {
			return nil, err
		}

		return !value, nil
	default:
		return nil, fmt.Errorf("unsupported unary operator %q", expr.Op)
	}
}

func evalCastExpr(expr CastExpr, vars map[string]any) (any, error) {
	value, err := EvalExpr(expr.Operand, vars)
	if err != nil {
		return nil, err
	}

	switch expr.TypeName {
	case "Integer":
		switch typed := value.(type) {
		case int:
			return typed, nil
		case string:
			return strconv.Atoi(typed)
		default:
			return strconv.Atoi(fmt.Sprintf("%v", value))
		}
	case "String":
		return fmt.Sprintf("%v", value), nil
	default:
		return UnsupportedExpr{Text: renderExpr(expr)}, nil
	}
}

func evalListExpr(expr ListExpr, vars map[string]any) (any, error) {
	values := make([]any, 0, len(expr.Elements))
	for _, element := range expr.Elements {
		value, err := EvalExpr(element, vars)
		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	return values, nil
}

func evalMapExpr(expr MapExpr, vars map[string]any) (any, error) {
	values := make(map[string]any, len(expr.Keys))
	for index, keyExpr := range expr.Keys {
		keyValue, err := EvalExpr(keyExpr, vars)
		if err != nil {
			return nil, err
		}

		key, ok := keyValue.(string)
		if !ok {
			return nil, fmt.Errorf("unsupported map key %T", keyValue)
		}

		value, err := EvalExpr(expr.Values[index], vars)
		if err != nil {
			return nil, err
		}

		values[key] = value
	}

	return values, nil
}

func evalIndexExpr(expr IndexExpr, vars map[string]any) (any, error) {
	receiver, err := EvalExpr(expr.Receiver, vars)
	if err != nil {
		return nil, err
	}

	index, err := EvalExpr(expr.Index, vars)
	if err != nil {
		return nil, err
	}

	switch typed := receiver.(type) {
	case []any:
		position, ok := index.(int)
		if !ok {
			return nil, fmt.Errorf("unsupported list index %T", index)
		}

		if position < 0 || position >= len(typed) {
			return nil, fmt.Errorf("list index %d out of range", position)
		}

		return typed[position], nil
	case map[string]any:
		key, ok := index.(string)
		if !ok {
			return nil, fmt.Errorf("unsupported map index %T", index)
		}

		value, exists := typed[key]
		if !exists {
			return nil, fmt.Errorf("missing key %q", key)
		}

		return value, nil
	}

	refValue := reflect.ValueOf(receiver)
	if !refValue.IsValid() {
		return nil, fmt.Errorf("unsupported index target <nil>")
	}

	if refValue.Kind() == reflect.Slice || refValue.Kind() == reflect.Array {
		position, ok := index.(int)
		if !ok {
			return nil, fmt.Errorf("unsupported list index %T", index)
		}

		if position < 0 || position >= refValue.Len() {
			return nil, fmt.Errorf("list index %d out of range", position)
		}

		return refValue.Index(position).Interface(), nil
	}

	if refValue.Kind() == reflect.Map && refValue.Type().Key().Kind() == reflect.String {
		key, ok := index.(string)
		if !ok {
			return nil, fmt.Errorf("unsupported map index %T", index)
		}

		result := refValue.MapIndex(reflect.ValueOf(key))
		if !result.IsValid() {
			return nil, fmt.Errorf("missing key %q", key)
		}

		return result.Interface(), nil
	}

	return nil, fmt.Errorf("unsupported index target %T", receiver)
}

func evalNullSafeExpr(expr NullSafeExpr, vars map[string]any) (any, error) {
	receiver, err := EvalExpr(expr.Receiver, vars)
	if err != nil {
		return nil, err
	}

	current := receiver
	for _, part := range strings.Split(expr.Property, ".") {
		if current == nil {
			return nil, nil
		}

		current, err = lookupVariablePart(current, part)
		if err != nil {
			return nil, err
		}
	}

	return current, nil
}

func evalMethodCallExpr(expr MethodCallExpr, vars map[string]any) (any, error) {
	receiver, err := EvalExpr(expr.Receiver, vars)
	if err != nil {
		return nil, err
	}

	if expr.Method == "collect" {
		if _, ok := receiver.([]any); ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}
	}

	args, err := evalExprArgs(expr.Args, vars)
	if err != nil {
		return nil, err
	}

	switch typed := receiver.(type) {
	case string:
		return evalStringMethodCall(typed, expr.Method, args)
	case []any:
		return evalListMethodCall(typed, expr.Method, args)
	default:
		return nil, fmt.Errorf("unsupported method receiver %T", receiver)
	}
}

func evalExprArgs(args []Expr, vars map[string]any) ([]any, error) {
	values := make([]any, 0, len(args))
	for _, arg := range args {
		value, err := EvalExpr(arg, vars)
		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	return values, nil
}

func evalStringMethodCall(receiver string, method string, args []any) (any, error) {
	switch method {
	case "trim":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strings.TrimSpace(receiver), nil
	case "size":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return len(receiver), nil
	case "toInteger":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strconv.Atoi(receiver)
	case "toLowerCase":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strings.ToLower(receiver), nil
	case "toUpperCase":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strings.ToUpper(receiver), nil
	case "contains":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		needle, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		return strings.Contains(receiver, needle), nil
	case "startsWith":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		prefix, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		return strings.HasPrefix(receiver, prefix), nil
	case "endsWith":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		suffix, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		return strings.HasSuffix(receiver, suffix), nil
	case "replace":
		if err := requireMethodArgCount(method, args, 2); err != nil {
			return nil, err
		}

		oldValue, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		newValue, err := requireStringArg(method, args[1])
		if err != nil {
			return nil, err
		}

		return strings.ReplaceAll(receiver, oldValue, newValue), nil
	case "split":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		delim, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		parts := strings.Split(receiver, delim)
		values := make([]any, 0, len(parts))
		for _, part := range parts {
			values = append(values, part)
		}

		return values, nil
	case "substring":
		if err := requireMethodArgCount(method, args, 1, 2); err != nil {
			return nil, err
		}

		start, err := requireIntArg(method, args[0])
		if err != nil {
			return nil, err
		}

		end := len(receiver)
		if len(args) == 2 {
			end, err = requireIntArg(method, args[1])
			if err != nil {
				return nil, err
			}
		}

		if start < 0 || end < start || end > len(receiver) {
			return nil, fmt.Errorf("substring indices out of range")
		}

		return receiver[start:end], nil
	default:
		return nil, fmt.Errorf("unsupported string method %q", method)
	}
}

func evalListMethodCall(receiver []any, method string, args []any) (any, error) {
	switch method {
	case "size":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return len(receiver), nil
	case "isEmpty":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return len(receiver) == 0, nil
	case "first":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}
		if len(receiver) == 0 {
			return nil, fmt.Errorf("first() on empty list")
		}

		return receiver[0], nil
	case "last":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}
		if len(receiver) == 0 {
			return nil, fmt.Errorf("last() on empty list")
		}

		return receiver[len(receiver)-1], nil
	case "flatten":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		flattened := make([]any, 0, len(receiver))
		for _, value := range receiver {
			flattened = append(flattened, flattenSliceValue(value)...)
		}

		return flattened, nil
	case "collect":
		return UnsupportedExpr{Text: renderExpr(MethodCallExpr{Receiver: UnsupportedExpr{Text: renderValueForUnsupported(receiver)}, Method: method, Args: renderArgsForUnsupported(args)})}, nil
	default:
		return nil, fmt.Errorf("unsupported list method %q", method)
	}
}

func requireMethodArgCount(method string, args []any, counts ...int) error {
	for _, count := range counts {
		if len(args) == count {
			return nil
		}
	}

	return fmt.Errorf("unsupported %s() arity %d", method, len(args))
}

func flattenSliceValue(value any) []any {
	refValue := reflect.ValueOf(value)
	if !refValue.IsValid() || (refValue.Kind() != reflect.Slice && refValue.Kind() != reflect.Array) {
		return []any{value}
	}

	flattened := make([]any, 0, refValue.Len())
	for index := range refValue.Len() {
		flattened = append(flattened, flattenSliceValue(refValue.Index(index).Interface())...)
	}

	return flattened
}

func requireStringArg(method string, value any) (string, error) {
	stringValue, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("unsupported %s() argument %T", method, value)
	}

	return stringValue, nil
}

func requireIntArg(method string, value any) (int, error) {
	intValue, ok := value.(int)
	if !ok {
		return 0, fmt.Errorf("unsupported %s() argument %T", method, value)
	}

	return intValue, nil
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
	switch expr.Op {
	case "&&":
		left, err := EvalExpr(expr.Left, vars)
		if err != nil {
			return nil, err
		}

		leftBool, err := evalBoolOperand(left, expr.Op)
		if err != nil {
			return nil, err
		}
		if !leftBool {
			return false, nil
		}

		right, err := EvalExpr(expr.Right, vars)
		if err != nil {
			return nil, err
		}

		return evalBoolOperand(right, expr.Op)
	case "||":
		left, err := EvalExpr(expr.Left, vars)
		if err != nil {
			return nil, err
		}

		leftBool, err := evalBoolOperand(left, expr.Op)
		if err != nil {
			return nil, err
		}
		if leftBool {
			return true, nil
		}

		right, err := EvalExpr(expr.Right, vars)
		if err != nil {
			return nil, err
		}

		return evalBoolOperand(right, expr.Op)
	}

	left, err := EvalExpr(expr.Left, vars)
	if err != nil {
		return nil, err
	}

	right, err := EvalExpr(expr.Right, vars)
	if err != nil {
		return nil, err
	}

	switch expr.Op {
	case "==":
		return reflect.DeepEqual(left, right), nil
	case "!=":
		return !reflect.DeepEqual(left, right), nil
	}

	if result, ok, err := compareOrderedOperands(left, right, expr.Op); ok || err != nil {
		return result, err
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
func EvalExpr(expr any, vars map[string]any) (any, error) {
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
	case TernaryExpr:
		return evalTernaryExpr(value, vars)
	case UnaryExpr:
		return evalUnaryExpr(value, vars)
	case CastExpr:
		return evalCastExpr(value, vars)
	case ListExpr:
		return evalListExpr(value, vars)
	case MapExpr:
		return evalMapExpr(value, vars)
	case IndexExpr:
		return evalIndexExpr(value, vars)
	case NullExpr:
		return nil, nil
	case NullSafeExpr:
		return evalNullSafeExpr(value, vars)
	case MethodCallExpr:
		return evalMethodCallExpr(value, vars)
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

func evalBoolOperand(value any, operator string) (bool, error) {
	boolValue, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("unsupported logical operand %T for %q", value, operator)
	}

	return boolValue, nil
}

func compareOrderedOperands(left, right any, operator string) (any, bool, error) {
	switch leftValue := left.(type) {
	case int:
		rightValue, ok := right.(int)
		if !ok {
			return nil, true, fmt.Errorf("unsupported comparison operand %T", right)
		}

		switch operator {
		case ">":
			return leftValue > rightValue, true, nil
		case "<":
			return leftValue < rightValue, true, nil
		case ">=":
			return leftValue >= rightValue, true, nil
		case "<=":
			return leftValue <= rightValue, true, nil
		}
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return nil, true, fmt.Errorf("unsupported comparison operand %T", right)
		}

		switch operator {
		case ">":
			return leftValue > rightValue, true, nil
		case "<":
			return leftValue < rightValue, true, nil
		case ">=":
			return leftValue >= rightValue, true, nil
		case "<=":
			return leftValue <= rightValue, true, nil
		}
	}

	return nil, false, nil
}

func isTruthy(value any) bool {
	if value == nil {
		return false
	}

	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	}

	refValue := reflect.ValueOf(value)
	if !refValue.IsValid() {
		return false
	}

	switch refValue.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return refValue.Len() != 0
	case reflect.Bool:
		return refValue.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return refValue.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return refValue.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return refValue.Float() != 0
	case reflect.Interface, reflect.Pointer:
		if refValue.IsNil() {
			return false
		}

		return isTruthy(refValue.Elem().Interface())
	default:
		return true
	}
}

func renderArgsForUnsupported(args []any) []Expr {
	exprs := make([]Expr, 0, len(args))
	for _, arg := range args {
		exprs = append(exprs, UnsupportedExpr{Text: renderValueForUnsupported(arg)})
	}

	return exprs
}

func renderValueForUnsupported(value any) string {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed)
	case int:
		return strconv.Itoa(typed)
	case bool:
		if typed {
			return "true"
		}

		return "false"
	case []any:
		parts := make([]string, 0, len(typed))
		for _, element := range typed {
			parts = append(parts, renderValueForUnsupported(element))
		}

		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprintf("%v", value)
	}
}
