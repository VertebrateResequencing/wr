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
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var groovyInterpolationPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

var errUnsupportedClosure = errors.New("unsupported closure")

const workflowEnumValuesKey = "__nextflowdsl_enum_values"

func looksLikeEnumExprPath(root, path string) bool {
	if root == "" || path == "" || strings.Contains(path, ".") || path != strings.ToUpper(path) {
		return false
	}

	first := root[0]

	return first >= 'A' && first <= 'Z'
}

func evalStringConstructor(args []any) (any, bool, error) {
	if len(args) != 1 {
		return nil, false, nil
	}

	return fmt.Sprint(args[0]), true, nil
}

func evalDateConstructor(args []any) (any, bool, error) {
	if len(args) != 0 {
		return nil, false, nil
	}

	return time.Now().UTC().Format(time.RFC3339Nano), true, nil
}

func evalBigDecimalConstructor(args []any) (any, bool, error) {
	if len(args) != 1 {
		return nil, false, nil
	}

	value, err := strconv.ParseFloat(fmt.Sprint(args[0]), 64)
	if err != nil {
		return nil, false, err
	}

	return value, true, nil
}

func evalBigIntegerConstructor(args []any) (any, bool, error) {
	if len(args) != 1 {
		return nil, false, nil
	}

	value, err := strconv.ParseInt(fmt.Sprint(args[0]), 10, 64)
	if err != nil {
		return nil, false, err
	}

	return value, true, nil
}

func evalArrayListConstructor(args []any) (any, bool, error) {
	if len(args) != 1 {
		return nil, false, nil
	}

	value := reflect.ValueOf(args[0])
	if !value.IsValid() {
		return []any(nil), true, nil
	}

	kind := value.Kind()
	if kind != reflect.Slice && kind != reflect.Array {
		return nil, false, nil
	}

	items := make([]any, 0, value.Len())
	for index := range value.Len() {
		items = append(items, value.Index(index).Interface())
	}

	return items, true, nil
}

func evalMapConstructor(args []any) (any, bool, error) {
	if len(args) != 1 {
		return nil, false, nil
	}

	value := reflect.ValueOf(args[0])
	if !value.IsValid() {
		return map[string]any{}, true, nil
	}

	if value.Kind() != reflect.Map {
		return nil, false, nil
	}

	copyMap := make(map[string]any, value.Len())
	for _, key := range value.MapKeys() {
		copyMap[fmt.Sprint(key.Interface())] = value.MapIndex(key).Interface()
	}

	return copyMap, true, nil
}

func evalRandomConstructor(args []any) (any, bool, error) {
	if len(args) != 0 {
		return nil, false, nil
	}

	return int64(0), true, nil
}

type evalExprStmt struct {
	expr Expr
}

type evalAssignStmt struct {
	name string
	expr Expr
}

type evalAugAssignStmt struct {
	name string
	op   string
	expr Expr
}

type evalReturnStmt struct {
	expr Expr
}

type evalBreakStmt struct{}

type evalAssertStmt struct {
	expr    Expr
	message Expr
}

type evalThrowStmt struct {
	expr Expr
}

type evalIfStmt struct {
	cond     Expr
	thenBody []any
	elseBody []any
}

type evalForStmt struct {
	varName    string
	collection Expr
	body       []any
}

type evalCatchStmt struct {
	typeName string
	varName  string
	body     []any
}

type evalTryStmt struct {
	tryBody      []any
	catchClauses []evalCatchStmt
	finallyBody  []any
}

type evalSwitchCase struct {
	expr Expr
	body []any
}

type evalSwitchStmt struct {
	expr        Expr
	cases       []evalSwitchCase
	defaultBody []any
}

type evalStatementResult struct {
	value    any
	valueSet bool
	returned bool
	broke    bool
}

func resultWithValue(value any) evalStatementResult {
	return evalStatementResult{value: value, valueSet: true}
}

func evalStatementBlock(stmts []any, scope map[string]any) (evalStatementResult, error) {
	result := evalStatementResult{}

	for _, stmt := range stmts {
		stmtResult, err := evalStatement(stmt, scope)
		if err != nil {
			return result, err
		}

		if stmtResult.valueSet {
			result.value = stmtResult.value
			result.valueSet = true
		}

		if stmtResult.returned || stmtResult.broke {
			result.returned = stmtResult.returned
			result.broke = stmtResult.broke

			return result, nil
		}
	}

	return result, nil
}

func evalStatement(stmt any, scope map[string]any) (evalStatementResult, error) {
	switch typed := stmt.(type) {
	case evalExprStmt:
		value, err := EvalExpr(typed.expr, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		if expr, ok := typed.expr.(BinaryExpr); ok && expr.Op == "<<" {
			if receiver, ok := expr.Left.(VarExpr); ok && receiver.Path == "" {
				scope[receiver.Root] = cloneChannelValue(value)
			}
		}

		return resultWithValue(value), nil
	case evalAssignStmt:
		value, err := EvalExpr(typed.expr, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		scope[typed.name] = cloneChannelValue(value)

		return resultWithValue(value), nil
	case evalAugAssignStmt:
		current, ok := scope[typed.name]
		if !ok {
			return evalStatementResult{}, fmt.Errorf("unknown variable %q", typed.name)
		}

		right, err := EvalExpr(typed.expr, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		value, err := evalAugAssignValue(current, right, typed.op)
		if err != nil {
			return evalStatementResult{}, err
		}

		scope[typed.name] = cloneChannelValue(value)

		return resultWithValue(value), nil
	case evalReturnStmt:
		value, err := EvalExpr(typed.expr, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		return evalStatementResult{value: value, valueSet: true, returned: true}, nil
	case evalBreakStmt:
		return evalStatementResult{broke: true}, nil
	case evalAssertStmt:
		if err := evalAssertStatement(typed.expr, typed.message, scope); err != nil {
			return evalStatementResult{}, err
		}

		return evalStatementResult{}, nil
	case evalThrowStmt:
		return evalStatementResult{}, evalThrowStatement(typed.expr, scope)
	case evalIfStmt:
		condition, err := EvalExpr(typed.cond, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		if isTruthy(condition) {
			return evalStatementBlock(typed.thenBody, scope)
		}

		if len(typed.elseBody) == 0 {
			return evalStatementResult{}, nil
		}

		return evalStatementBlock(typed.elseBody, scope)
	case evalForStmt:
		collection, err := EvalExpr(typed.collection, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		items, err := iterValues(collection)
		if err != nil {
			return evalStatementResult{}, err
		}

		result := evalStatementResult{}

		for _, item := range items {
			scope[typed.varName] = cloneChannelValue(item)

			stmtResult, err := evalStatementBlock(typed.body, scope)
			if err != nil {
				return evalStatementResult{}, err
			}

			if stmtResult.valueSet {
				result.value = stmtResult.value
				result.valueSet = true
			}

			if stmtResult.returned {
				result.returned = true

				return result, nil
			}

			if stmtResult.broke {
				return result, nil
			}
		}

		return result, nil
	case evalTryStmt:
		result, execErr := evalStatementBlock(typed.tryBody, scope)
		if execErr != nil {
			handled := false

			for _, clause := range typed.catchClauses {
				if !matchesCatchClause(clause.typeName, execErr) {
					continue
				}

				var previous any

				hadPrevious := false
				if clause.varName != "" {
					previous, hadPrevious = scope[clause.varName]
					scope[clause.varName] = execErr
				}

				result, execErr = evalStatementBlock(clause.body, scope)
				if clause.varName != "" {
					if hadPrevious {
						scope[clause.varName] = previous
					} else {
						delete(scope, clause.varName)
					}
				}

				handled = true

				break
			}

			if !handled {
				if len(typed.finallyBody) > 0 {
					if _, finallyErr := evalStatementBlock(typed.finallyBody, scope); finallyErr != nil {
						return evalStatementResult{}, finallyErr
					}
				}

				return evalStatementResult{}, execErr
			}
		}

		if len(typed.finallyBody) > 0 {
			finallyResult, err := evalStatementBlock(typed.finallyBody, scope)
			if err != nil {
				return evalStatementResult{}, err
			}

			if finallyResult.returned || finallyResult.broke {
				return finallyResult, nil
			}

			if finallyResult.valueSet && !result.returned {
				result.value = finallyResult.value
				result.valueSet = true
			}
		}

		return result, nil
	case evalSwitchStmt:
		target, err := EvalExpr(typed.expr, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		for _, switchCase := range typed.cases {
			matched, matchErr := matchesSwitchCase(target, switchCase.expr, scope)
			if matchErr != nil {
				return evalStatementResult{}, matchErr
			}

			if !matched {
				continue
			}

			result, evalErr := evalStatementBlock(switchCase.body, scope)
			if evalErr != nil {
				return evalStatementResult{}, evalErr
			}

			result.broke = false

			return result, nil
		}

		if len(typed.defaultBody) == 0 {
			return evalStatementResult{}, nil
		}

		result, err := evalStatementBlock(typed.defaultBody, scope)
		if err != nil {
			return evalStatementResult{}, err
		}

		result.broke = false

		return result, nil
	default:
		return evalStatementResult{}, fmt.Errorf("unsupported statement %T", stmt)
	}
}

func evalStatementBody(body string, scope map[string]any) (any, error) {
	if scope == nil {
		scope = make(map[string]any)
	}

	stmts, err := parseEvalStatements(body)
	if err != nil {
		return nil, err
	}

	result, err := evalStatementBlock(stmts, scope)
	if err != nil {
		var thrown *evalThrownError
		if errors.As(err, &thrown) {
			return nil, nil
		}

		return nil, err
	}

	if !result.valueSet {
		return nil, nil
	}

	return result.value, nil
}

func parseEvalStatements(body string) ([]any, error) {
	tokens, err := lex(body)
	if err != nil {
		return nil, err
	}

	return parseEvalStatementsFromTokens(tokens)
}

func parseEvalStatementsFromTokens(tokens []token) ([]any, error) {
	parser := newEvalStatementParser(tokens)

	return parser.parseStatements()
}

func newEvalStatementParser(tokens []token) *evalStatementParser {
	trimmed := make([]token, 0, len(tokens))
	for _, tok := range tokens {
		if tok.typ == tokenEOF {
			continue
		}

		trimmed = append(trimmed, tok)
	}

	return &evalStatementParser{tokens: trimmed}
}

type evalThrownError struct {
	className string
	message   string
}

func (e *evalThrownError) Error() string {
	return e.message
}

func evalThrowStatement(expr Expr, vars map[string]any) error {
	thrown := &evalThrownError{className: "Exception", message: "Exception"}

	if newExpr, ok := expr.(NewExpr); ok {
		thrown.className = shortConstructorName(newExpr.ClassName)
		if len(newExpr.Args) > 0 {
			message, err := EvalExpr(newExpr.Args[0], vars)
			if err == nil && message != nil {
				thrown.message = fmt.Sprint(message)
			} else {
				thrown.message = renderExpr(newExpr.Args[0])
			}
		} else {
			thrown.message = thrown.className
		}

		warnf("nextflowdsl: warning: %s\n", thrown.message)

		return thrown
	}

	value, err := EvalExpr(expr, vars)
	if err == nil && value != nil {
		thrown.message = fmt.Sprint(value)
	}

	warnf("nextflowdsl: warning: %s\n", thrown.message)

	return thrown
}

func shortConstructorName(className string) string {
	index := strings.LastIndex(className, ".")
	if index == -1 {
		return className
	}

	return className[index+1:]
}

// EvalExpr evaluates a simple Groovy expression given a context of variable
// bindings. Returns the result or an error.
func EvalExpr(expr any, vars map[string]any) (any, error) {
	switch value := expr.(type) {
	case IntExpr:
		return value.Value, nil
	case StringExpr:
		return interpolateGroovyString(value.Value, vars)
	case SlashyStringExpr:
		return value.Value, nil
	case ParamsExpr:
		return resolveExprPath("params", value.Path, vars)
	case VarExpr:
		return resolveExprPath(value.Root, value.Path, vars)
	case BinaryExpr:
		return evalBinaryExpr(value, vars)
	case InExpr:
		return evalInExpr(value, vars)
	case RegexExpr:
		return evalRegexExpr(value, vars)
	case RangeExpr:
		return evalRangeExpr(value, vars)
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
	case SpreadExpr:
		return evalSpreadExpr(value, vars)
	case MethodCallExpr:
		return evalMethodCallExpr(value, vars)
	case NewExpr:
		return evalNewExpr(value, vars)
	case MultiAssignExpr:
		return evalMultiAssignExpr(value, vars)
	case ClosureExpr:
		return value, nil
	case UnsupportedExpr:
		return nil, fmt.Errorf("unsupported expression %q", value.Text)
	case BoolExpr:
		return value.Value, nil
	case nil:
		return nil, errors.New("unsupported expression <nil>")
	default:
		return nil, fmt.Errorf("unsupported expression %T", expr)
	}
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

func resolveExprPath(root, path string, vars map[string]any) (any, error) {
	if vars == nil {
		return nil, fmt.Errorf("unknown variable %q", root)
	}

	current, ok := vars[root]
	if !ok {
		if value, ok := resolveEnumExprPath(root, path, vars); ok {
			return value, nil
		}

		if root == "params" {
			return nil, nil
		}

		return nil, fmt.Errorf("unknown variable %q", root)
	}

	if path == "" {
		return current, nil
	}

	for _, part := range strings.Split(path, ".") {
		var err error

		current, err = lookupVariablePart(current, part)
		if err != nil {
			if root == "params" {
				return nil, nil
			}

			return nil, fmt.Errorf("unknown variable %q", root+"."+path)
		}
	}

	return current, nil
}

func resolveEnumExprPath(root, path string, vars map[string]any) (any, bool) {
	if path == "" || vars == nil {
		return nil, false
	}

	rawValues, ok := vars[workflowEnumValuesKey]
	if !ok {
		return nil, false
	}

	enumValues, ok := rawValues.(map[string]map[string]struct{})
	if !ok {
		return nil, false
	}

	values, ok := enumValues[root]
	if !ok || strings.Contains(path, ".") {
		return nil, false
	}

	if _, ok = values[path]; !ok {
		return nil, false
	}

	return path, true
}

func lookupVariablePart(current any, part string) (any, error) {
	switch typed := current.(type) {
	case map[string]any:
		value, ok := typed[part]
		if !ok {
			return nil, fmt.Errorf("missing key %q", part)
		}

		return value, nil
	case orderedMap:
		value, ok := typed.values[part]
		if !ok {
			return nil, fmt.Errorf("missing key %q", part)
		}

		return value, nil
	}

	refValue := reflect.ValueOf(current)
	if !refValue.IsValid() {
		return nil, errors.New("invalid value")
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
	case "instanceof", "!instanceof":
		return evalInstanceofExpr(expr, vars)
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
	case "<=>":
		return compareSpaceshipOperands(left, right)
	}

	if expr.Op == "<<" {
		if list, ok := left.([]any); ok {
			updated := cloneChannelSlice(list)
			updated = append(updated, cloneChannelValue(right))

			return updated, nil
		}
	}

	if result, ok, compareErr := compareOrderedOperands(left, right, expr.Op); ok || compareErr != nil {
		return result, compareErr
	}

	leftInt, err := requireIntegerOperand(left, expr.Op)
	if err != nil {
		return nil, fmt.Errorf("unsupported arithmetic operand %T", left)
	}

	rightInt, err := requireIntegerOperand(right, expr.Op)
	if err != nil {
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
			return nil, errors.New("division by zero")
		}

		return leftInt / rightInt, nil
	case "%":
		if rightInt == 0 {
			return nil, errors.New("division by zero")
		}

		return leftInt % rightInt, nil
	case "**":
		return powInt(leftInt, rightInt)
	case "&":
		return leftInt & rightInt, nil
	case "^":
		return leftInt ^ rightInt, nil
	case "|":
		return leftInt | rightInt, nil
	case "<<":
		if rightInt < 0 {
			return nil, errors.New("negative shift count")
		}

		return leftInt << uint(rightInt), nil
	case ">>":
		if rightInt < 0 {
			return nil, errors.New("negative shift count")
		}

		return leftInt >> uint(rightInt), nil
	case ">>>":
		if rightInt < 0 {
			return nil, errors.New("negative shift count")
		}

		//nolint:gosec // Unsigned right shift matches Groovy semantics for evaluator arithmetic.
		return int(uint32(leftInt) >> uint(rightInt)), nil
	case ">":
		return leftInt > rightInt, nil
	case "<":
		return leftInt < rightInt, nil
	default:
		return nil, fmt.Errorf("unsupported operator %q", expr.Op)
	}
}

func evalBoolOperand(value any, operator string) (bool, error) {
	boolValue, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("unsupported logical operand %T for %q", value, operator)
	}

	return boolValue, nil
}

func evalInstanceofExpr(expr BinaryExpr, vars map[string]any) (any, error) {
	left, err := EvalExpr(expr.Left, vars)
	if err != nil {
		return nil, err
	}

	typeName, err := groovyTypeName(expr.Right)
	if err != nil {
		return nil, err
	}

	matched := matchesGroovyType(left, typeName)
	if expr.Op == "!instanceof" {
		return !matched, nil
	}

	return matched, nil
}

func groovyTypeName(expr Expr) (string, error) {
	switch typed := expr.(type) {
	case VarExpr:
		if typed.Path != "" {
			return "", fmt.Errorf("unsupported instanceof type %q", typed.Root+"."+typed.Path)
		}

		return typed.Root, nil
	case StringExpr:
		return typed.Value, nil
	default:
		return "", fmt.Errorf("unsupported instanceof type %T", expr)
	}
}

func matchesGroovyType(value any, typeName string) bool {
	switch typeName {
	case "String":
		_, ok := value.(string)

		return ok
	case "Integer":
		switch value.(type) {
		case int, int64:
			return true
		default:
			return false
		}
	case "List":
		if _, ok := value.([]any); ok {
			return true
		}

		refValue := reflect.ValueOf(value)

		return refValue.IsValid() && (refValue.Kind() == reflect.Array || refValue.Kind() == reflect.Slice)
	case "Map":
		if _, ok := value.(map[string]any); ok {
			return true
		}

		if _, ok := value.(orderedMap); ok {
			return true
		}

		refValue := reflect.ValueOf(value)

		return refValue.IsValid() && refValue.Kind() == reflect.Map && refValue.Type().Key().Kind() == reflect.String
	case "Boolean":
		_, ok := value.(bool)

		return ok
	default:
		return false
	}
}

func compareSpaceshipOperands(left, right any) (any, error) {
	switch leftValue := left.(type) {
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return nil, fmt.Errorf("unsupported comparison operand %T", right)
		}

		return compareOrdering(leftValue, rightValue), nil
	default:
		leftInt, err := requireIntegerOperand(left, "<=>")
		if err != nil {
			return nil, fmt.Errorf("unsupported comparison operand %T", left)
		}

		rightInt, err := requireIntegerOperand(right, "<=>")
		if err != nil {
			return nil, fmt.Errorf("unsupported comparison operand %T", right)
		}

		return compareOrdering(leftInt, rightInt), nil
	}
}

func compareOrdering[T ~int | ~string](left, right T) int {
	if left < right {
		return -1
	}

	if left > right {
		return 1
	}

	return 0
}

func requireIntegerOperand(value any, operator string) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint:
		//nolint:gosec // The result is represented as int throughout the evaluator API.
		return int(typed), nil
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		//nolint:gosec // The result is represented as int throughout the evaluator API.
		return int(typed), nil
	default:
		return 0, fmt.Errorf("unsupported arithmetic operand %T for %q", value, operator)
	}
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

func powInt(base, exponent int) (any, error) {
	if exponent < 0 {
		return nil, fmt.Errorf("unsupported negative exponent %d", exponent)
	}

	result := 1

	for exponent > 0 {
		if exponent%2 == 1 {
			result *= base
		}

		base *= base
		exponent /= 2
	}

	return result, nil
}

func evalInExpr(expr InExpr, vars map[string]any) (any, error) {
	left, err := EvalExpr(expr.Left, vars)
	if err != nil {
		return nil, err
	}

	right, err := EvalExpr(expr.Right, vars)
	if err != nil {
		return nil, err
	}

	contains, err := containsValue(right, left)
	if err != nil {
		return nil, err
	}

	if expr.Negated {
		return !contains, nil
	}

	return contains, nil
}

func containsValue(container, needle any) (bool, error) {
	switch typed := container.(type) {
	case []any:
		for _, candidate := range typed {
			if reflect.DeepEqual(candidate, needle) {
				return true, nil
			}
		}

		return false, nil
	case map[string]any:
		key, ok := needle.(string)
		if !ok {
			return false, fmt.Errorf("unsupported membership operand %T", needle)
		}

		_, exists := typed[key]

		return exists, nil
	case orderedMap:
		key, ok := needle.(string)
		if !ok {
			return false, fmt.Errorf("unsupported membership operand %T", needle)
		}

		_, exists := typed.values[key]

		return exists, nil
	case string:
		value, ok := needle.(string)
		if !ok {
			return false, fmt.Errorf("unsupported membership operand %T", needle)
		}

		return strings.Contains(typed, value), nil
	}

	refValue := reflect.ValueOf(container)
	if !refValue.IsValid() {
		return false, errors.New("unsupported membership target <nil>")
	}

	if refValue.Kind() != reflect.Array && refValue.Kind() != reflect.Slice {
		return false, fmt.Errorf("unsupported membership target %T", container)
	}

	for index := range refValue.Len() {
		if reflect.DeepEqual(refValue.Index(index).Interface(), needle) {
			return true, nil
		}
	}

	return false, nil
}

func evalRegexExpr(expr RegexExpr, vars map[string]any) (any, error) {
	left, err := EvalExpr(expr.Left, vars)
	if err != nil {
		return nil, err
	}

	leftString, ok := left.(string)
	if !ok {
		return nil, fmt.Errorf("unsupported regex operand %T", left)
	}

	right, err := EvalExpr(expr.Right, vars)
	if err != nil {
		return nil, err
	}

	pattern, ok := right.(string)
	if !ok {
		return nil, fmt.Errorf("unsupported regex pattern %T", right)
	}

	if expr.Full {
		pattern = "^(?:" + pattern + ")$"
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	return re.MatchString(leftString), nil
}

func evalRangeExpr(expr RangeExpr, vars map[string]any) (any, error) {
	startValue, err := EvalExpr(expr.Start, vars)
	if err != nil {
		return nil, err
	}

	start, err := requireIntegerOperand(startValue, "..")
	if err != nil {
		return nil, err
	}

	endValue, err := EvalExpr(expr.End, vars)
	if err != nil {
		return nil, err
	}

	end, err := requireIntegerOperand(endValue, "..")
	if err != nil {
		return nil, err
	}

	step := 1

	limit := end
	if start > end {
		step = -1
	}

	if expr.Exclusive {
		limit -= step
	}

	if (step > 0 && start > limit) || (step < 0 && start < limit) {
		return []any{}, nil
	}

	values := make([]any, 0, absInt(limit-start)+1)
	for current := start; ; current += step {
		values = append(values, current)
		if current == limit {
			break
		}
	}

	return values, nil
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}

	return value
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
	case "-":
		value, err := requireIntegerOperand(operand, expr.Op)
		if err != nil {
			return nil, fmt.Errorf("unsupported unary operand %T", operand)
		}

		return -value, nil
	case "~":
		value, err := requireIntegerOperand(operand, expr.Op)
		if err != nil {
			return nil, fmt.Errorf("unsupported unary operand %T", operand)
		}

		return ^value, nil
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
	case orderedMap:
		key, ok := index.(string)
		if !ok {
			return nil, fmt.Errorf("unsupported map index %T", index)
		}

		value, exists := typed.values[key]
		if !exists {
			return nil, fmt.Errorf("missing key %q", key)
		}

		return value, nil
	}

	refValue := reflect.ValueOf(receiver)
	if !refValue.IsValid() {
		return nil, errors.New("unsupported index target <nil>")
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

	return resolvePropertyPath(receiver, expr.Property, true)
}

func resolvePropertyPath(current any, property string, allowNil bool) (any, error) {
	for _, part := range strings.Split(property, ".") {
		if current == nil {
			if allowNil {
				return nil, nil
			}

			return nil, errors.New("invalid value")
		}

		var err error

		current, err = lookupVariablePart(current, part)
		if err != nil {
			return nil, err
		}
	}

	return current, nil
}

func evalSpreadExpr(expr SpreadExpr, vars map[string]any) (any, error) {
	receiver, err := EvalExpr(expr.Receiver, vars)
	if err != nil {
		return nil, err
	}

	refValue := reflect.ValueOf(receiver)
	if !refValue.IsValid() {
		return nil, errors.New("unsupported spread receiver <nil>")
	}

	if refValue.Kind() != reflect.Array && refValue.Kind() != reflect.Slice {
		return nil, fmt.Errorf("unsupported spread receiver %T", receiver)
	}

	values := make([]any, 0, refValue.Len())
	for index := range refValue.Len() {
		item := refValue.Index(index).Interface()
		if item == nil {
			values = append(values, nil)

			continue
		}

		value, err := resolvePropertyPath(item, expr.Property, false)
		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	return values, nil
}

func evalMethodCallExpr(expr MethodCallExpr, vars map[string]any) (any, error) {
	if receiver, ok := expr.Receiver.(VarExpr); ok && receiver.Path == "" {
		if vars == nil || vars[receiver.Root] == nil {
			args, err := evalExprArgs(expr.Args, vars)
			if err != nil {
				return nil, err
			}

			if value, handled, err := evalStaticMethodCall(receiver.Root, expr.Method, args); handled || err != nil {
				return value, err
			}
		}
	}

	receiver, err := EvalExpr(expr.Receiver, vars)
	if err != nil {
		return nil, err
	}

	switch typed := receiver.(type) {
	case string:
		args, err := evalExprArgs(expr.Args, vars)
		if err != nil {
			return nil, err
		}

		return evalStringMethodCall(typed, expr.Method, args)
	case map[string]any:
		args, err := evalExprArgs(expr.Args, vars)
		if err != nil {
			return nil, err
		}

		return evalMapMethodCall(typed, expr.Method, args, expr, vars)
	case orderedMap:
		args, err := evalExprArgs(expr.Args, vars)
		if err != nil {
			return nil, err
		}

		return evalMapMethodCall(typed, expr.Method, args, expr, vars)
	case int, int64, float64:
		args, err := evalExprArgs(expr.Args, vars)
		if err != nil {
			return nil, err
		}

		return evalNumberMethodCall(typed, expr.Method, args)
	case []any:
		return evalListMethodCallExpr(expr, typed, vars)
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

func evalStaticMethodCall(className, method string, args []any) (any, bool, error) {
	switch className {
	case "Integer":
		switch method {
		case "parseInt":
			if err := requireMethodArgCount(method, args, 1); err != nil {
				return nil, true, err
			}

			switch typed := args[0].(type) {
			case string:
				value, err := strconv.Atoi(typed)

				return value, true, err
			case int:
				return typed, true, nil
			default:
				value, err := strconv.Atoi(fmt.Sprint(typed))

				return value, true, err
			}
		}
	}

	return nil, false, nil
}

func requireMethodArgCount(method string, args []any, counts ...int) error {
	for _, count := range counts {
		if len(args) == count {
			return nil
		}
	}

	return fmt.Errorf("unsupported %s() arity %d", method, len(args))
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
	case "replaceAll":
		if err := requireMethodArgCount(method, args, 2); err != nil {
			return nil, err
		}

		pattern, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		replacement, err := requireStringArg(method, args[1])
		if err != nil {
			return nil, err
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}

		return re.ReplaceAllString(receiver, replacement), nil
	case "replaceFirst":
		if err := requireMethodArgCount(method, args, 2); err != nil {
			return nil, err
		}

		pattern, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		replacement, err := requireStringArg(method, args[1])
		if err != nil {
			return nil, err
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}

		match := re.FindStringSubmatchIndex(receiver)
		if match == nil {
			return receiver, nil
		}

		replaced := re.ExpandString(nil, replacement, receiver, match)

		return receiver[:match[0]] + string(replaced) + receiver[match[1]:], nil
	case "matches":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		pattern, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}

		loc := re.FindStringIndex(receiver)

		return loc != nil && loc[0] == 0 && loc[1] == len(receiver), nil
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
	case "tokenize":
		if err := requireMethodArgCount(method, args, 0, 1); err != nil {
			return nil, err
		}

		delimiters := " \t\r\n"

		if len(args) == 1 {
			parsedDelimiters, err := requireStringArg(method, args[0])
			if err != nil {
				return nil, err
			}

			delimiters = parsedDelimiters
		}

		parts := strings.FieldsFunc(receiver, func(r rune) bool {
			return strings.ContainsRune(delimiters, r)
		})

		values := make([]any, 0, len(parts))
		for _, part := range parts {
			values = append(values, part)
		}

		return values, nil
	case "multiply":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		count, err := requireIntArg(method, args[0])
		if err != nil {
			return nil, err
		}

		if count < 0 {
			return nil, errors.New("multiply count must be non-negative")
		}

		return strings.Repeat(receiver, count), nil
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
			return nil, errors.New("substring indices out of range")
		}

		return receiver[start:end], nil
	case "padLeft":
		return padStringMethod(receiver, method, args, true)
	case "padRight":
		return padStringMethod(receiver, method, args, false)
	case "capitalise":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return capitalizeString(receiver), nil
	case "uncapitalize":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return uncapitalizeString(receiver), nil
	case "isNumber":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return isNumericString(receiver), nil
	case "isInteger", "isLong":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return isIntegerString(receiver), nil
	case "isDouble", "isBigDecimal":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return isFloatString(receiver), nil
	case "toBoolean":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strings.EqualFold(strings.TrimSpace(receiver), "true"), nil
	case "stripIndent":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return stripIndent(receiver), nil
	case "readLines":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		lines := splitStringLines(receiver)

		values := make([]any, 0, len(lines))
		for _, line := range lines {
			values = append(values, line)
		}

		return values, nil
	case "count":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		needle, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		return strings.Count(receiver, needle), nil
	case "toLong":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strconv.ParseInt(strings.TrimSpace(receiver), 10, 64)
	case "toDouble", "toBigDecimal":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return strconv.ParseFloat(strings.TrimSpace(receiver), 64)
	case "eachLine":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: receiver + ".eachLine(...)"}, nil
		}

		lines := splitStringLines(receiver)

		results := make([]any, 0, len(lines))
		for _, line := range lines {
			result, err := evalSimpleClosure(closure, line, nil)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: receiver + ".eachLine(...)"}, nil
				}

				return nil, err
			}

			results = append(results, result)
		}

		return results, nil
	case "execute":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		text := receiver + ".execute()"
		fmt.Fprintf(os.Stderr, "nextflowdsl: %s is not supported during Groovy evaluation\n", text)

		return UnsupportedExpr{Text: text}, nil
	default:
		return nil, fmt.Errorf("unsupported string method %q", method)
	}
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

func padStringMethod(receiver, method string, args []any, left bool) (any, error) {
	if err := requireMethodArgCount(method, args, 1, 2); err != nil {
		return nil, err
	}

	width, err := requireIntArg(method, args[0])
	if err != nil {
		return nil, err
	}

	pad := " "
	if len(args) == 2 {
		pad, err = requireStringArg(method, args[1])
		if err != nil {
			return nil, err
		}

		if pad == "" {
			return nil, fmt.Errorf("%s() padding must not be empty", method)
		}
	}

	if width <= len(receiver) {
		return receiver, nil
	}

	padding := strings.Repeat(string([]rune(pad)[0]), width-len(receiver))
	if left {
		return padding + receiver, nil
	}

	return receiver + padding, nil
}

func capitalizeString(value string) string {
	if value == "" {
		return ""
	}

	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])

	return string(runes)
}

func uncapitalizeString(value string) string {
	if value == "" {
		return ""
	}

	runes := []rune(value)
	runes[0] = unicode.ToLower(runes[0])

	return string(runes)
}

func isNumericString(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}

	_, err := strconv.ParseFloat(trimmed, 64)

	return err == nil
}

func isIntegerString(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}

	_, err := strconv.ParseInt(trimmed, 10, 64)

	return err == nil
}

func isFloatString(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}

	_, err := strconv.ParseFloat(trimmed, 64)

	return err == nil
}

func stripIndent(value string) string {
	lines := strings.Split(value, "\n")
	indent := -1

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		width := leadingIndentWidth(line)
		if indent == -1 || width < indent {
			indent = width
		}
	}

	if indent <= 0 {
		return value
	}

	stripped := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			stripped = append(stripped, "")

			continue
		}

		if indent > len(line) {
			stripped = append(stripped, "")

			continue
		}

		stripped = append(stripped, line[indent:])
	}

	return strings.Join(stripped, "\n")
}

func leadingIndentWidth(line string) int {
	width := 0

	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}

		width += len(string(r))
	}

	return width
}

func splitStringLines(value string) []string {
	parts := strings.Split(value, "\n")
	for index, line := range parts {
		parts[index] = strings.TrimSuffix(line, "\r")
	}

	return parts
}

func evalMapMethodCall(receiver any, method string, args []any, expr MethodCallExpr, vars map[string]any) (any, error) {
	entries, values, ordered, ok := mapReceiverParts(receiver)
	if !ok {
		return nil, fmt.Errorf("unsupported map receiver %T", receiver)
	}

	switch method {
	case "findAll":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		filtered := make([]mapEntry, 0, len(entries))
		for _, entry := range entries {
			matched, err := evalMapPredicateClosure(closure, entry.key, entry.value, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if matched {
				filtered = append(filtered, mapEntry{key: entry.key, value: entry.value})
			}
		}

		return buildMapResult(filtered, ordered), nil
	case "find":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		for _, entry := range entries {
			matched, err := evalMapPredicateClosure(closure, entry.key, entry.value, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if matched {
				return map[string]any{"key": entry.key, "value": cloneChannelValue(entry.value)}, nil
			}
		}

		return nil, nil
	case "any":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		for _, entry := range entries {
			matched, err := evalMapPredicateClosure(closure, entry.key, entry.value, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if matched {
				return true, nil
			}
		}

		return false, nil
	case "every":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		for _, entry := range entries {
			matched, err := evalMapPredicateClosure(closure, entry.key, entry.value, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if !matched {
				return false, nil
			}
		}

		return true, nil
	case "groupBy":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		grouped := make(map[any]any)

		for _, entry := range entries {
			key, err := evalSimpleClosure(closure, []any{entry.key, entry.value}, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			bucket, ok := grouped[key].(map[string]any)
			if !ok {
				bucket = make(map[string]any)
			}

			bucket[entry.key] = cloneChannelValue(entry.value)
			grouped[key] = bucket
		}

		return grouped, nil
	case "collectEntries":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		collected := make([]mapEntry, 0, len(entries))
		for _, entry := range entries {
			value, err := evalSimpleClosure(closure, []any{entry.key, entry.value}, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			key, entryValue, ok := collectEntryPair(value)
			if !ok {
				return nil, fmt.Errorf("unsupported %s() result %T", method, value)
			}

			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported %s() key %T", method, key)
			}

			collected = append(collected, mapEntry{key: keyString, value: entryValue})
		}

		return buildMapResult(collected, ordered), nil
	case "plus":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		otherEntries, otherValues, _, ok := mapReceiverParts(args[0])
		if !ok {
			return nil, fmt.Errorf("unsupported %s() argument %T", method, args[0])
		}

		if !ordered {
			merged := cloneStringAnyMap(values)
			for key, value := range otherValues {
				merged[key] = cloneChannelValue(value)
			}

			return merged, nil
		}

		merged := append([]mapEntry{}, entries...)

		entryIndex := make(map[string]int, len(merged))
		for index, entry := range merged {
			entryIndex[entry.key] = index
		}

		for _, entry := range otherEntries {
			if index, exists := entryIndex[entry.key]; exists {
				merged[index] = mapEntry{key: entry.key, value: entry.value}

				continue
			}

			entryIndex[entry.key] = len(merged)
			merged = append(merged, mapEntry{key: entry.key, value: entry.value})
		}

		return buildMapResult(merged, true), nil
	case "minus":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		keys, err := mapMethodKeysArg(method, args[0])
		if err != nil {
			return nil, err
		}

		if !ordered {
			filtered := cloneStringAnyMap(values)
			for _, key := range keys {
				delete(filtered, key)
			}

			return filtered, nil
		}

		remove := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			remove[key] = struct{}{}
		}

		filtered := make([]mapEntry, 0, len(entries))
		for _, entry := range entries {
			if _, exists := remove[entry.key]; exists {
				continue
			}

			filtered = append(filtered, mapEntry{key: entry.key, value: entry.value})
		}

		return buildMapResult(filtered, true), nil
	case "sort":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		var sortErr error

		sorted := append([]mapEntry(nil), entries...)
		sort.SliceStable(sorted, func(i, j int) bool {
			less, err := evalMapSortLess(closure, sorted[i], sorted[j], vars)
			if err != nil {
				sortErr = err

				return false
			}

			return less
		})

		if sortErr != nil {
			if errors.Is(sortErr, errUnsupportedClosure) {
				return UnsupportedExpr{Text: renderExpr(expr)}, nil
			}

			return nil, sortErr
		}

		for index := 1; index < len(sorted); index++ {
			if _, err := evalMapSortLess(closure, sorted[index-1], sorted[index], vars); err != nil {
				return nil, err
			}
		}

		return buildMapResult(sorted, true), nil
	case "inject":
		if err := requireMethodArgCount(method, args, 2); err != nil {
			return nil, err
		}

		closure, ok := args[1].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		accumulator := args[0]
		for _, entry := range entries {
			value, err := evalSimpleClosure(closure, []any{accumulator, entry.key, entry.value}, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			accumulator = value
		}

		return accumulator, nil
	case "size":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return len(values), nil
	case "isEmpty":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		return len(values) == 0, nil
	case "keySet":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		keys := make([]any, 0, len(entries))
		for _, entry := range entries {
			keys = append(keys, entry.key)
		}

		return keys, nil
	case "values":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		values := make([]any, 0, len(entries))
		for _, entry := range entries {
			values = append(values, cloneChannelValue(entry.value))
		}

		return values, nil
	case "containsKey":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		key, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		_, ok := values[key]

		return ok, nil
	case "containsValue":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if reflect.DeepEqual(entry.value, args[0]) {
				return true, nil
			}
		}

		return false, nil
	case "subMap":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		keys, err := mapMethodKeysArg(method, args[0])
		if err != nil {
			return nil, err
		}

		result := make([]mapEntry, 0, len(keys))
		for _, key := range keys {
			value, ok := values[key]
			if ok {
				result = append(result, mapEntry{key: key, value: value})
			}
		}

		return buildMapResult(result, ordered), nil
	case "collect":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		collected := make([]any, 0, len(entries))
		for _, entry := range entries {
			value, err := evalSimpleClosure(closure, []any{entry.key, entry.value}, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			collected = append(collected, value)
		}

		return collected, nil
	case "each":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		closure, ok := args[0].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		for _, entry := range entries {
			_, err := evalSimpleClosure(closure, []any{entry.key, entry.value}, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}
		}

		return buildMapResult(entries, ordered), nil
	case "getOrDefault":
		if err := requireMethodArgCount(method, args, 2); err != nil {
			return nil, err
		}

		key, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, err
		}

		value, ok := values[key]
		if ok {
			return cloneChannelValue(value), nil
		}

		return cloneChannelValue(args[1]), nil
	default:
		return nil, fmt.Errorf("unsupported map method %q", method)
	}
}

func mapReceiverParts(receiver any) ([]mapEntry, map[string]any, bool, bool) {
	switch typed := receiver.(type) {
	case map[string]any:
		return sortedMapEntries(typed), typed, false, true
	case orderedMap:
		return append([]mapEntry{}, typed.entries...), typed.values, true, true
	default:
		return nil, nil, false, false
	}
}

func sortedMapEntries(receiver map[string]any) []mapEntry {
	keys := make([]string, 0, len(receiver))
	for key := range receiver {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	entries := make([]mapEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, mapEntry{key: key, value: receiver[key]})
	}

	return entries
}

func evalMapPredicateClosure(closure ClosureExpr, key string, value any, vars map[string]any) (bool, error) {
	result, err := evalSimpleClosure(closure, []any{key, value}, vars)
	if err != nil {
		return false, err
	}

	return isTruthy(result), nil
}

func buildMapResult(entries []mapEntry, ordered bool) any {
	if ordered {
		return newOrderedMap(entries)
	}

	return mapFromEntries(entries)
}

func newOrderedMap(entries []mapEntry) orderedMap {
	clonedEntries := make([]mapEntry, 0, len(entries))

	values := make(map[string]any, len(entries))
	for _, entry := range entries {
		clonedValue := cloneChannelValue(entry.value)
		clonedEntries = append(clonedEntries, mapEntry{key: entry.key, value: clonedValue})
		values[entry.key] = clonedValue
	}

	return orderedMap{values: values, entries: clonedEntries}
}

func mapFromEntries(entries []mapEntry) map[string]any {
	values := make(map[string]any, len(entries))
	for _, entry := range entries {
		values[entry.key] = cloneChannelValue(entry.value)
	}

	return values
}

func collectEntryPair(value any) (any, any, bool) {
	if pair, ok := value.([]any); ok && len(pair) >= 2 {
		return pair[0], pair[1], true
	}

	if pair, ok := value.(map[string]any); ok && len(pair) == 1 {
		for key, entryValue := range pair {
			return key, entryValue, true
		}
	}

	if pair, ok := value.(map[any]any); ok && len(pair) == 1 {
		for key, entryValue := range pair {
			return key, entryValue, true
		}
	}

	return nil, nil, false
}

func cloneStringAnyMap(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneChannelValue(value)
	}

	return cloned
}

func mapMethodKeysArg(method string, value any) ([]string, error) {
	switch typed := value.(type) {
	case []any:
		keys := make([]string, 0, len(typed))
		for _, entry := range typed {
			key, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported %s() key %T", method, entry)
			}

			keys = append(keys, key)
		}

		return keys, nil
	case []string:
		return append([]string{}, typed...), nil
	default:
		return nil, fmt.Errorf("unsupported %s() argument %T", method, value)
	}
}

func evalMapSortLess(closure ClosureExpr, left, right mapEntry, vars map[string]any) (bool, error) {
	result, err := evalSimpleClosure(closure, []any{
		map[string]any{"key": left.key, "value": left.value},
		map[string]any{"key": right.key, "value": right.value},
	}, vars)
	if err != nil {
		return false, err
	}

	switch typed := result.(type) {
	case int:
		return typed < 0, nil
	case bool:
		return typed, nil
	default:
		return false, fmt.Errorf("unsupported sort() result %T", result)
	}
}

func evalNumberMethodCall(receiver any, method string, args []any) (any, error) {
	switch method {
	case "abs":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		switch typed := receiver.(type) {
		case int:
			return absInt(typed), nil
		case int64:
			if typed < 0 {
				return -typed, nil
			}

			return typed, nil
		case float64:
			return math.Abs(typed), nil
		}
	case "round":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		switch typed := receiver.(type) {
		case int:
			return typed, nil
		case int64:
			return typed, nil
		case float64:
			return int(math.Round(typed)), nil
		}
	case "intdiv":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, err
		}

		divisor, err := requireNumberInt64Arg(method, args[0])
		if err != nil {
			return nil, err
		}

		if divisor == 0 {
			return nil, errors.New("division by zero")
		}

		switch typed := receiver.(type) {
		case int:
			return int(int64(typed) / divisor), nil
		case int64:
			return typed / divisor, nil
		case float64:
			return int(int64(typed) / divisor), nil
		}
	case "toInteger":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		switch typed := receiver.(type) {
		case int:
			return typed, nil
		case int64:
			return int(typed), nil
		case float64:
			return int(typed), nil
		}
	case "toLong":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		switch typed := receiver.(type) {
		case int:
			return int64(typed), nil
		case int64:
			return typed, nil
		case float64:
			return int64(typed), nil
		}
	case "toDouble", "toBigDecimal":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, err
		}

		switch typed := receiver.(type) {
		case int:
			return float64(typed), nil
		case int64:
			return float64(typed), nil
		case float64:
			return typed, nil
		}
	}

	return nil, fmt.Errorf("unsupported number method %q on %T", method, receiver)
}

func requireNumberInt64Arg(method string, value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("unsupported %s() argument %T", method, value)
	}
}

func evalListMethodCallExpr(expr MethodCallExpr, receiver []any, vars map[string]any) (any, error) {
	switch expr.Method {
	case "collect":
		return evalListCollectMethod(expr, receiver, vars)
	case "any", "every", "findAll", "find":
		return evalListClosurePredicateMethod(expr, receiver, vars)
	case "inject", "groupBy", "countBy", "count", "collectMany", "collectEntries",
		"reverseEach", "eachWithIndex", "sum", "max", "min", "spread":
		return evalListClosureMethod(expr, receiver, vars)
	case "asType":
		if err := requireMethodExprArgCount(expr.Method, expr.Args, 1); err != nil {
			return nil, err
		}

		typeName, err := groovyTypeName(expr.Args[0])
		if err != nil {
			warnf("nextflowdsl: unsupported list asType(%s)\n", renderExpr(expr.Args[0]))

			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		switch shortConstructorName(typeName) {
		case "Set", "HashSet", "LinkedHashSet":
			return evalListAsSet(receiver), nil
		default:
			warnf("nextflowdsl: unsupported list asType(%s)\n", typeName)

			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}
	}

	args, err := evalExprArgs(expr.Args, vars)
	if err != nil {
		return nil, err
	}

	result, updated, mutated, err := evalListMethodCall(receiver, expr.Method, args)
	if err != nil {
		return nil, err
	}

	if mutated {
		assignListMethodReceiver(expr.Receiver, vars, updated)
	}

	return result, nil
}

func evalListCollectMethod(expr MethodCallExpr, receiver []any, vars map[string]any) (any, error) {
	if err := requireMethodExprArgCount(expr.Method, expr.Args, 1); err != nil {
		return nil, err
	}

	closure, ok := expr.Args[0].(ClosureExpr)
	if !ok {
		return UnsupportedExpr{Text: renderExpr(expr)}, nil
	}

	collected := make([]any, 0, len(receiver))
	for _, item := range receiver {
		value, err := evalSimpleClosure(closure, item, vars)
		if err != nil {
			if errors.Is(err, errUnsupportedClosure) {
				return UnsupportedExpr{Text: renderExpr(expr)}, nil
			}

			return nil, err
		}

		collected = append(collected, value)
	}

	return collected, nil
}

func requireMethodExprArgCount(method string, args []Expr, counts ...int) error {
	for _, count := range counts {
		if len(args) == count {
			return nil
		}
	}

	return fmt.Errorf("unsupported %s() arity %d", method, len(args))
}

func evalListClosurePredicateMethod(expr MethodCallExpr, receiver []any, vars map[string]any) (any, error) {
	if err := requireMethodExprArgCount(expr.Method, expr.Args, 1); err != nil {
		return nil, err
	}

	closure, ok := expr.Args[0].(ClosureExpr)
	if !ok {
		return UnsupportedExpr{Text: renderExpr(expr)}, nil
	}

	switch expr.Method {
	case "any":
		for _, item := range receiver {
			matched, err := evalListClosurePredicate(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if matched {
				return true, nil
			}
		}

		return false, nil
	case "every":
		for _, item := range receiver {
			matched, err := evalListClosurePredicate(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if !matched {
				return false, nil
			}
		}

		return true, nil
	case "findAll":
		matchedItems := make([]any, 0, len(receiver))
		for _, item := range receiver {
			matched, err := evalListClosurePredicate(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if matched {
				matchedItems = append(matchedItems, cloneChannelValue(item))
			}
		}

		return matchedItems, nil
	case "find":
		for _, item := range receiver {
			matched, err := evalListClosurePredicate(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			if matched {
				return cloneChannelValue(item), nil
			}
		}

		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported list closure method %q", expr.Method)
	}
}

func evalListClosurePredicate(closure ClosureExpr, item any, vars map[string]any) (bool, error) {
	result, err := evalSimpleClosure(closure, item, vars)
	if err != nil {
		return false, err
	}

	return isTruthy(result), nil
}

func evalListClosureMethod(expr MethodCallExpr, receiver []any, vars map[string]any) (any, error) {
	switch expr.Method {
	case "inject":
		if err := requireMethodExprArgCount(expr.Method, expr.Args, 2); err != nil {
			return nil, err
		}

		initial, err := EvalExpr(expr.Args[0], vars)
		if err != nil {
			return nil, err
		}

		closure, ok := expr.Args[1].(ClosureExpr)
		if !ok {
			return UnsupportedExpr{Text: renderExpr(expr)}, nil
		}

		accumulator := initial
		for _, item := range receiver {
			accumulator, err = evalSimpleClosure(closure, []any{accumulator, item}, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}
		}

		return accumulator, nil
	case "groupBy":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		grouped := make(map[any]any)

		for _, item := range receiver {
			key, err := evalSimpleClosure(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			current := []any{}
			if existing, ok := grouped[key].([]any); ok {
				current = existing
			}

			current = append(current, cloneChannelValue(item))
			grouped[key] = current
		}

		return grouped, nil
	case "countBy":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		counted := make(map[any]any)

		for _, item := range receiver {
			key, err := evalSimpleClosure(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			count := 0
			if existing, ok := counted[key].(int); ok {
				count = existing
			}

			counted[key] = count + 1
		}

		return counted, nil
	case "count":
		if err := requireMethodExprArgCount(expr.Method, expr.Args, 1); err != nil {
			return nil, err
		}

		if closure, ok := expr.Args[0].(ClosureExpr); ok {
			count := 0

			for _, item := range receiver {
				matched, err := evalListClosurePredicate(closure, item, vars)
				if err != nil {
					if errors.Is(err, errUnsupportedClosure) {
						return UnsupportedExpr{Text: renderExpr(expr)}, nil
					}

					return nil, err
				}

				if matched {
					count++
				}
			}

			return count, nil
		}

		value, err := EvalExpr(expr.Args[0], vars)
		if err != nil {
			return nil, err
		}

		count := 0

		for _, item := range receiver {
			if reflect.DeepEqual(item, value) {
				count++
			}
		}

		return count, nil
	case "collectMany":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		collected := make([]any, 0)

		for _, item := range receiver {
			value, err := evalSimpleClosure(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			collected = append(collected, flattenOneLevel(value)...)
		}

		return collected, nil
	case "collectEntries":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		entries := make(map[any]any)

		for _, item := range receiver {
			value, err := evalSimpleClosure(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			key, entryValue, ok := collectEntryPair(value)
			if !ok {
				return nil, fmt.Errorf("unsupported collectEntries() value %T", value)
			}

			entries[key] = entryValue
		}

		return entries, nil
	case "reverseEach":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		sharedScope := cloneEvalVars(vars)
		for index := len(receiver) - 1; index >= 0; index-- {
			_, err := evalSharedClosure(closure, receiver[index], sharedScope)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}
		}

		return cloneChannelSlice(receiver), nil
	case "eachWithIndex":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		sharedScope := cloneEvalVars(vars)
		for index, item := range receiver {
			_, err := evalSharedClosure(closure, []any{item, index}, sharedScope)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}
		}

		return cloneChannelSlice(receiver), nil
	case "sum":
		if err := requireMethodExprArgCount(expr.Method, expr.Args, 0, 1); err != nil {
			return nil, err
		}

		total := 0

		if len(expr.Args) == 0 {
			for _, item := range receiver {
				value, err := requireIntegerOperand(item, expr.Method)
				if err != nil {
					return nil, err
				}

				total += value
			}

			return total, nil
		}

		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		for _, item := range receiver {
			value, err := evalSimpleClosure(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			intValue, err := requireIntegerOperand(value, expr.Method)
			if err != nil {
				return nil, err
			}

			total += intValue
		}

		return total, nil
	case "max", "min":
		if err := requireMethodExprArgCount(expr.Method, expr.Args, 0, 1); err != nil {
			return nil, err
		}

		if len(receiver) == 0 {
			return nil, nil
		}

		best := cloneChannelValue(receiver[0])
		bestScore := receiver[0]

		if len(expr.Args) == 1 {
			closure, err := requireListClosureArg(expr)
			if err != nil {
				return nil, err
			}

			value, err := evalSimpleClosure(closure, receiver[0], vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			bestScore = value
			for _, item := range receiver[1:] {
				value, err := evalSimpleClosure(closure, item, vars)
				if err != nil {
					if errors.Is(err, errUnsupportedClosure) {
						return UnsupportedExpr{Text: renderExpr(expr)}, nil
					}

					return nil, err
				}

				better, err := chooseListExtremum(expr.Method, value, bestScore)
				if err != nil {
					return nil, err
				}

				if better {
					best = cloneChannelValue(item)
					bestScore = value
				}
			}

			return best, nil
		}

		for _, item := range receiver[1:] {
			better, err := chooseListExtremum(expr.Method, item, bestScore)
			if err != nil {
				return nil, err
			}

			if better {
				best = cloneChannelValue(item)
				bestScore = item
			}
		}

		return best, nil
	case "spread":
		closure, err := requireListClosureArg(expr)
		if err != nil {
			return nil, err
		}

		collected := make([]any, 0, len(receiver))
		for _, item := range receiver {
			value, err := evalSimpleClosure(closure, item, vars)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					return UnsupportedExpr{Text: renderExpr(expr)}, nil
				}

				return nil, err
			}

			collected = append(collected, value)
		}

		return collected, nil
	default:
		return nil, fmt.Errorf("unsupported list closure method %q", expr.Method)
	}
}

func requireListClosureArg(expr MethodCallExpr) (ClosureExpr, error) {
	if err := requireMethodExprArgCount(expr.Method, expr.Args, 1); err != nil {
		return ClosureExpr{}, err
	}

	closure, ok := expr.Args[0].(ClosureExpr)
	if !ok {
		return ClosureExpr{}, fmt.Errorf("unsupported %s() argument %T", expr.Method, expr.Args[0])
	}

	return closure, nil
}

func flattenOneLevel(value any) []any {
	if slice, ok := value.([]any); ok {
		return cloneChannelSlice(slice)
	}

	refValue := reflect.ValueOf(value)
	if !refValue.IsValid() || (refValue.Kind() != reflect.Array && refValue.Kind() != reflect.Slice) {
		return []any{value}
	}

	flattened := make([]any, 0, refValue.Len())
	for index := range refValue.Len() {
		flattened = append(flattened, refValue.Index(index).Interface())
	}

	return flattened
}

func cloneEvalVars(vars map[string]any) map[string]any {
	if len(vars) == 0 {
		return map[string]any{}
	}

	cloned := make(map[string]any, len(vars))
	for key, value := range vars {
		cloned[key] = value
	}

	return cloned
}

func chooseListExtremum(method string, candidate, current any) (bool, error) {
	ordered, ok, err := compareOrderedOperands(candidate, current, ">")
	if err != nil {
		return false, err
	}

	if !ok {
		return false, fmt.Errorf("unsupported %s() operand %T", method, candidate)
	}

	better, ok := ordered.(bool)
	if !ok {
		return false, fmt.Errorf("unsupported %s() operand %T", method, candidate)
	}

	if method == "min" {
		ordered, _, err = compareOrderedOperands(candidate, current, "<")
		if err != nil {
			return false, err
		}

		better, ok = ordered.(bool)
		if !ok {
			return false, fmt.Errorf("unsupported %s() operand %T", method, candidate)
		}
	}

	return better, nil
}

func evalListAsSet(receiver []any) []any {
	set := make([]any, 0, len(receiver))
	for _, item := range receiver {
		if containsComparableValue(set, item) {
			continue
		}

		set = append(set, cloneChannelValue(item))
	}

	return set
}

func containsComparableValue(values []any, candidate any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, candidate) {
			return true
		}
	}

	return false
}

func evalListMethodCall(receiver []any, method string, args []any) (any, []any, bool, error) {
	switch method {
	case "size":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		return len(receiver), nil, false, nil
	case "isEmpty":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		return len(receiver) == 0, nil, false, nil
	case "first":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		if len(receiver) == 0 {
			return nil, nil, false, errors.New("first() on empty list")
		}

		return receiver[0], nil, false, nil
	case "last":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		if len(receiver) == 0 {
			return nil, nil, false, errors.New("last() on empty list")
		}

		return receiver[len(receiver)-1], nil, false, nil
	case "flatten":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		flattened := make([]any, 0, len(receiver))
		for _, value := range receiver {
			flattened = append(flattened, flattenSliceValue(value)...)
		}

		return flattened, nil, false, nil
	case "join":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		separator, err := requireStringArg(method, args[0])
		if err != nil {
			return nil, nil, false, err
		}

		parts := make([]string, 0, len(receiver))
		for _, item := range receiver {
			parts = append(parts, fmt.Sprint(item))
		}

		return strings.Join(parts, separator), nil, false, nil
	case "unique":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		unique := make([]any, 0, len(receiver))
		for _, item := range receiver {
			if containsComparableValue(unique, item) {
				continue
			}

			unique = append(unique, cloneChannelValue(item))
		}

		return unique, nil, false, nil
	case "sort":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		sorted := cloneChannelSlice(receiver)

		var sortErr error

		sort.Slice(sorted, func(i, j int) bool {
			if sortErr != nil {
				return false
			}

			less, err := lessSortableValue(sorted[i], sorted[j])
			if err != nil {
				sortErr = err

				return false
			}

			return less
		})

		if sortErr != nil {
			return nil, nil, false, sortErr
		}

		for index := 1; index < len(sorted); index++ {
			if _, err := lessSortableValue(sorted[index-1], sorted[index]); err != nil {
				return nil, nil, false, err
			}
		}

		return sorted, nil, false, nil
	case "plus":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		other, ok := args[0].([]any)
		if !ok {
			return nil, nil, false, fmt.Errorf("unsupported %s() argument %T", method, args[0])
		}

		combined := make([]any, 0, len(receiver)+len(other))
		combined = append(combined, cloneChannelSlice(receiver)...)
		combined = append(combined, cloneChannelSlice(other)...)

		return combined, nil, false, nil
	case "minus":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		other, ok := args[0].([]any)
		if !ok {
			return nil, nil, false, fmt.Errorf("unsupported %s() argument %T", method, args[0])
		}

		filtered := make([]any, 0, len(receiver))
		for _, item := range receiver {
			if containsComparableValue(other, item) {
				continue
			}

			filtered = append(filtered, cloneChannelValue(item))
		}

		return filtered, nil, false, nil
	case "take":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		count, err := requireIntArg(method, args[0])
		if err != nil {
			return nil, nil, false, err
		}

		if count < 0 {
			return nil, nil, false, errors.New("take count must be non-negative")
		}

		if count > len(receiver) {
			count = len(receiver)
		}

		return cloneChannelSlice(receiver[:count]), nil, false, nil
	case "drop":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		count, err := requireIntArg(method, args[0])
		if err != nil {
			return nil, nil, false, err
		}

		if count < 0 {
			return nil, nil, false, errors.New("drop count must be non-negative")
		}

		if count > len(receiver) {
			count = len(receiver)
		}

		return cloneChannelSlice(receiver[count:]), nil, false, nil
	case "withIndex", "indexed":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		indexed := make([]any, 0, len(receiver))
		for index, item := range receiver {
			indexed = append(indexed, []any{cloneChannelValue(item), index})
		}

		return indexed, nil, false, nil
	case "transpose":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		return transposeList(receiver)
	case "head":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		if len(receiver) == 0 {
			return nil, nil, false, nil
		}

		return cloneChannelValue(receiver[0]), nil, false, nil
	case "tail":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		if len(receiver) <= 1 {
			return []any{}, nil, false, nil
		}

		return cloneChannelSlice(receiver[1:]), nil, false, nil
	case "init":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		if len(receiver) == 0 {
			return []any{}, nil, false, nil
		}

		return cloneChannelSlice(receiver[:len(receiver)-1]), nil, false, nil
	case "pop":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		if len(receiver) == 0 {
			return nil, []any{}, true, nil
		}

		updated := cloneChannelSlice(receiver[:len(receiver)-1])

		return cloneChannelValue(receiver[len(receiver)-1]), updated, true, nil
	case "push", "add":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		updated := append(cloneChannelSlice(receiver), cloneChannelValue(args[0]))

		return updated, updated, true, nil
	case "addAll":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		other, ok := args[0].([]any)
		if !ok {
			return nil, nil, false, fmt.Errorf("unsupported %s() argument %T", method, args[0])
		}

		updated := cloneChannelSlice(receiver)
		updated = append(updated, cloneChannelSlice(other)...)

		return updated, updated, true, nil
	case "remove":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		index, err := requireIntArg(method, args[0])
		if err != nil {
			return nil, nil, false, err
		}

		if index < 0 || index >= len(receiver) {
			return nil, nil, false, fmt.Errorf("remove index %d out of range", index)
		}

		updated := make([]any, 0, len(receiver)-1)
		updated = append(updated, cloneChannelSlice(receiver[:index])...)
		updated = append(updated, cloneChannelSlice(receiver[index+1:])...)

		return cloneChannelValue(receiver[index]), updated, true, nil
	case "contains":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		return containsComparableValue(receiver, args[0]), nil, false, nil
	case "intersect":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		other, ok := args[0].([]any)
		if !ok {
			return nil, nil, false, fmt.Errorf("unsupported %s() argument %T", method, args[0])
		}

		intersection := make([]any, 0)
		for _, item := range receiver {
			if containsComparableValue(other, item) && !containsComparableValue(intersection, item) {
				intersection = append(intersection, cloneChannelValue(item))
			}
		}

		return intersection, nil, false, nil
	case "disjoint":
		if err := requireMethodArgCount(method, args, 1); err != nil {
			return nil, nil, false, err
		}

		other, ok := args[0].([]any)
		if !ok {
			return nil, nil, false, fmt.Errorf("unsupported %s() argument %T", method, args[0])
		}

		for _, item := range receiver {
			if containsComparableValue(other, item) {
				return false, nil, false, nil
			}
		}

		return true, nil, false, nil
	case "toSet":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		return evalListAsSet(receiver), nil, false, nil
	case "reverse":
		if err := requireMethodArgCount(method, args, 0); err != nil {
			return nil, nil, false, err
		}

		reversed := cloneChannelSlice(receiver)
		for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
			reversed[left], reversed[right] = reversed[right], reversed[left]
		}

		return reversed, nil, false, nil
	case "collect":
		return UnsupportedExpr{Text: renderExpr(MethodCallExpr{
			Receiver: UnsupportedExpr{Text: renderValueForUnsupported(receiver)},
			Method:   method,
			Args:     renderArgsForUnsupported(args),
		})}, nil, false, nil
	default:
		return nil, nil, false, fmt.Errorf("unsupported list method %q", method)
	}
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

func lessSortableValue(left, right any) (bool, error) {
	switch leftValue := left.(type) {
	case int:
		rightValue, ok := right.(int)
		if !ok {
			return false, fmt.Errorf("unsupported sort operand %T", right)
		}

		return leftValue < rightValue, nil
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return false, fmt.Errorf("unsupported sort operand %T", right)
		}

		return leftValue < rightValue, nil
	default:
		return false, fmt.Errorf("unsupported sort operand %T", left)
	}
}

func transposeList(receiver []any) (any, []any, bool, error) {
	if len(receiver) == 0 {
		return []any{}, nil, false, nil
	}

	rows := make([][]any, 0, len(receiver))
	width := -1

	for _, item := range receiver {
		row, ok := closureTupleValues(item)
		if !ok {
			return nil, nil, false, fmt.Errorf("unsupported transpose() item %T", item)
		}

		if width == -1 {
			width = len(row)
		} else if len(row) != width {
			return nil, nil, false, errors.New("transpose() requires rows of equal length")
		}

		rows = append(rows, row)
	}

	transposed := make([]any, 0, width)
	for column := range width {
		rowValues := make([]any, 0, len(rows))
		for _, row := range rows {
			rowValues = append(rowValues, cloneChannelValue(row[column]))
		}

		transposed = append(transposed, rowValues)
	}

	return transposed, nil, false, nil
}

func closureTupleValues(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return cloneChannelSlice(typed), true
	case []string:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, item)
		}

		return values, true
	}

	refValue := reflect.ValueOf(value)
	if !refValue.IsValid() {
		return nil, false
	}

	if refValue.Kind() != reflect.Array && refValue.Kind() != reflect.Slice {
		return nil, false
	}

	values := make([]any, 0, refValue.Len())
	for index := range refValue.Len() {
		values = append(values, refValue.Index(index).Interface())
	}

	return values, true
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

func renderArgsForUnsupported(args []any) []Expr {
	exprs := make([]Expr, 0, len(args))
	for _, arg := range args {
		exprs = append(exprs, UnsupportedExpr{Text: renderValueForUnsupported(arg)})
	}

	return exprs
}

func assignListMethodReceiver(receiver Expr, vars map[string]any, updated []any) {
	if vars == nil {
		return
	}

	typed, ok := receiver.(VarExpr)
	if !ok || typed.Path != "" {
		return
	}

	vars[typed.Root] = cloneChannelSlice(updated)
}

func evalNewExpr(expr NewExpr, vars map[string]any) (any, error) {
	args, err := evalExprArgs(expr.Args, vars)
	if err != nil {
		return nil, err
	}

	switch shortConstructorName(expr.ClassName) {
	case "File", "Path":
		return evalPathConstructor(args)
	case "URL":
		return evalSupportedConstructor(expr, args, evalStringConstructor)
	case "Date":
		return evalSupportedConstructor(expr, args, evalDateConstructor)
	case "BigDecimal":
		return evalSupportedConstructor(expr, args, evalBigDecimalConstructor)
	case "BigInteger":
		return evalSupportedConstructor(expr, args, evalBigIntegerConstructor)
	case "ArrayList":
		return evalSupportedConstructor(expr, args, evalArrayListConstructor)
	case "HashMap", "LinkedHashMap":
		return evalSupportedConstructor(expr, args, evalMapConstructor)
	case "Random":
		return evalSupportedConstructor(expr, args, evalRandomConstructor)
	default:
		return UnsupportedExpr{Text: renderNewExpr(expr)}, nil
	}
}

func evalPathConstructor(args []any) (any, error) {
	if len(args) == 0 {
		return "", nil
	}

	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}

	if len(parts) == 1 {
		return filepath.Clean(parts[0]), nil
	}

	return filepath.Clean(filepath.Join(parts...)), nil
}

func evalSupportedConstructor(expr NewExpr, args []any, evaluator func([]any) (any, bool, error)) (any, error) {
	value, ok, err := evaluator(args)
	if err != nil {
		return nil, err
	}

	if ok {
		return value, nil
	}

	return UnsupportedExpr{Text: renderNewExpr(expr)}, nil
}

func renderNewExpr(expr NewExpr) string {
	args := make([]string, 0, len(expr.Args))
	for _, arg := range expr.Args {
		args = append(args, renderExpr(arg))
	}

	return "new " + expr.ClassName + "(" + strings.Join(args, ", ") + ")"
}

func evalMultiAssignExpr(expr MultiAssignExpr, vars map[string]any) (any, error) {
	value, err := EvalExpr(expr.Value, vars)
	if err != nil {
		return nil, err
	}

	values, ok := closureTupleValues(value)
	if !ok {
		return nil, fmt.Errorf("unsupported multi-assignment value %T", value)
	}

	if vars == nil {
		vars = map[string]any{}
	}

	for index, name := range expr.Names {
		if index < len(values) {
			vars[name] = cloneChannelValue(values[index])

			continue
		}

		vars[name] = nil
	}

	return values, nil
}

func evalSimpleClosure(closure ClosureExpr, value any, vars map[string]any) (any, error) {
	body := strings.TrimSpace(closure.Body)
	if body == "" {
		return cloneChannelValue(value), nil
	}

	scope, err := bindClosureVars(vars, closure, value)
	if err != nil {
		return nil, err
	}

	resolved, err := evalStatementBody(body, scope)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unsupported ") {
			return nil, fmt.Errorf("%w: %s", errUnsupportedClosure, body)
		}

		return nil, err
	}

	if unsupported, ok := resolved.(UnsupportedExpr); ok {
		return nil, fmt.Errorf("%w: %s", errUnsupportedClosure, unsupported.Text)
	}

	return resolved, nil
}

func bindClosureVars(vars map[string]any, closure ClosureExpr, value any) (map[string]any, error) {
	scope := cloneEvalVars(vars)
	if len(closure.Params) == 0 {
		scope["it"] = cloneChannelValue(value)

		return scope, nil
	}

	if len(closure.Params) == 1 {
		scope[closure.Params[0]] = cloneChannelValue(value)

		return scope, nil
	}

	values, ok := closureTupleValues(value)
	if !ok {
		return nil, fmt.Errorf("closure expects %d parameters but item is %T", len(closure.Params), value)
	}

	if len(values) < len(closure.Params) {
		return nil, fmt.Errorf("closure expects %d parameters but item has %d values", len(closure.Params), len(values))
	}

	for index, name := range closure.Params {
		scope[name] = cloneChannelValue(values[index])
	}

	return scope, nil
}

func evalSharedClosure(closure ClosureExpr, value any, scope map[string]any) (any, error) {
	body := strings.TrimSpace(closure.Body)
	if body == "" {
		return cloneChannelValue(value), nil
	}

	tupleValues, hasTuple := closureTupleValues(value)

	type previousValue struct {
		value any
		set   bool
	}

	previous := make(map[string]previousValue)
	bind := func(name string, value any) {
		previous[name] = previousValue{value: scope[name], set: previous[name].set || hasKey(scope, name)}
		scope[name] = cloneChannelValue(value)
	}

	if len(closure.Params) == 0 {
		bind("it", value)
	} else if len(closure.Params) == 1 {
		bind(closure.Params[0], value)
	} else {
		if !hasTuple {
			return nil, fmt.Errorf("closure expects %d parameters but item is %T", len(closure.Params), value)
		}

		if len(tupleValues) < len(closure.Params) {
			return nil, fmt.Errorf("closure expects %d parameters but item has %d values", len(closure.Params), len(tupleValues))
		}

		for index, name := range closure.Params {
			bind(name, tupleValues[index])
		}
	}

	defer func() {
		for name, prior := range previous {
			if prior.set {
				scope[name] = prior.value
			} else {
				delete(scope, name)
			}
		}
	}()

	resolved, err := evalStatementBody(body, scope)
	if err != nil {
		if strings.HasPrefix(err.Error(), "unsupported ") {
			return nil, fmt.Errorf("%w: %s", errUnsupportedClosure, body)
		}

		return nil, err
	}

	if unsupported, ok := resolved.(UnsupportedExpr); ok {
		return nil, fmt.Errorf("%w: %s", errUnsupportedClosure, unsupported.Text)
	}

	return resolved, nil
}

func hasKey(values map[string]any, key string) bool {
	if values == nil {
		return false
	}

	_, ok := values[key]

	return ok
}

type evalStatementParser struct {
	tokens []token
	pos    int
}

func (p *evalStatementParser) parseStatements() ([]any, error) {
	stmts := make([]any, 0)

	for {
		p.skipSeparators()

		if p.atEnd() {
			return stmts, nil
		}

		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}

		stmts = append(stmts, stmt)
	}
}

func (p *evalStatementParser) parseStatement() (any, error) {
	current := p.current()
	if current.typ == tokenIdent {
		switch current.lit {
		case "return":
			return p.parseReturnStmt()
		case "if":
			return p.parseIfStmt()
		case "for":
			return p.parseForStmt()
		case "try":
			return p.parseTryStmt()
		case "switch":
			return p.parseSwitchStmt()
		case "break":
			p.pos++

			return evalBreakStmt{}, nil
		case "assert":
			return p.parseAssertStmt()
		case "throw":
			return p.parseThrowStmt()
		case "def":
			return p.parseAssignmentStmt(true)
		}
	}

	if p.startsAssignment() {
		return p.parseAssignmentStmt(false)
	}

	exprTokens := trimDeclarationTokens(p.readStatementExprTokens())
	if len(exprTokens) == 0 {
		return nil, errors.New("expected expression")
	}

	expr, err := parseExprTokens(exprTokens)
	if err != nil {
		return nil, err
	}

	return evalExprStmt{expr: expr}, nil
}

func (p *evalStatementParser) parseReturnStmt() (any, error) {
	p.pos++

	exprTokens := trimDeclarationTokens(p.readStatementExprTokens())
	if len(exprTokens) == 0 {
		return evalReturnStmt{expr: NullExpr{}}, nil
	}

	expr, err := parseExprTokens(exprTokens)
	if err != nil {
		return nil, err
	}

	return evalReturnStmt{expr: expr}, nil
}

func (p *evalStatementParser) parseAssertStmt() (any, error) {
	p.pos++

	exprTokens := trimDeclarationTokens(p.readStatementExprTokens())
	if len(exprTokens) == 0 {
		return nil, errors.New("expected assertion expression")
	}

	assertTokens, messageTokens := splitTokensOnTopLevelColon(exprTokens)

	expr, err := parseExprTokens(assertTokens)
	if err != nil {
		return nil, err
	}

	var message Expr
	if len(messageTokens) > 0 {
		message, err = parseExprTokens(messageTokens)
		if err != nil {
			return nil, err
		}
	}

	return evalAssertStmt{expr: expr, message: message}, nil
}

func splitTokensOnTopLevelColon(tokens []token) ([]token, []token) {
	parenDepth := 0
	braceDepth := 0
	bracketDepth := 0

	for index, tok := range tokens {
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
		case tokenSymbol:
			switch tok.lit {
			case "[":
				bracketDepth++
			case "]":
				if bracketDepth > 0 {
					bracketDepth--
				}
			}
		case tokenColon:
			if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
				return trimDeclarationTokens(tokens[:index]), trimDeclarationTokens(tokens[index+1:])
			}
		}
	}

	return trimDeclarationTokens(tokens), nil
}

func (p *evalStatementParser) parseThrowStmt() (any, error) {
	p.pos++

	exprTokens := trimDeclarationTokens(p.readStatementExprTokens())
	if len(exprTokens) == 0 {
		return nil, errors.New("expected throw expression")
	}

	expr, err := parseExprTokens(exprTokens)
	if err != nil {
		return nil, err
	}

	return evalThrowStmt{expr: expr}, nil
}

func (p *evalStatementParser) parseIfStmt() (any, error) {
	p.pos++

	cond, err := p.parseParenExpr()
	if err != nil {
		return nil, err
	}

	thenBody, err := p.parseSingleOrBlock()
	if err != nil {
		return nil, err
	}

	p.skipSeparators()

	elseBody := []any(nil)

	if !p.atEnd() && p.current().typ == tokenIdent && p.current().lit == "else" {
		p.pos++
		p.skipSeparators()

		if !p.atEnd() && p.current().typ == tokenIdent && p.current().lit == "if" {
			elseStmt, parseErr := p.parseIfStmt()
			if parseErr != nil {
				return nil, parseErr
			}

			elseBody = []any{elseStmt}
		} else {
			elseBody, err = p.parseSingleOrBlock()
			if err != nil {
				return nil, err
			}
		}
	}

	return evalIfStmt{cond: cond, thenBody: thenBody, elseBody: elseBody}, nil
}

func (p *evalStatementParser) parseForStmt() (any, error) {
	p.pos++

	loopTokens, err := p.readWrappedTokens(tokenLParen, tokenRParen)
	if err != nil {
		return nil, err
	}

	inIndex := -1
	parenDepth := 0
	braceDepth := 0
	bracketDepth := 0

	for index, tok := range loopTokens {
		switch tok.typ {
		case tokenLParen:
			parenDepth++
		case tokenRParen:
			parenDepth--
		case tokenLBrace:
			braceDepth++
		case tokenRBrace:
			braceDepth--
		case tokenSymbol:
			switch tok.lit {
			case "[":
				bracketDepth++
			case "]":
				bracketDepth--
			}
		}

		if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 && tok.typ == tokenIdent && tok.lit == "in" {
			inIndex = index

			break
		}
	}

	if inIndex <= 0 || inIndex >= len(loopTokens)-1 {
		return nil, fmt.Errorf("unsupported expression %q", expressionText(loopTokens))
	}

	leftTokens := trimDeclarationTokens(loopTokens[:inIndex])
	if len(leftTokens) == 0 {
		return nil, errors.New("expected loop variable")
	}

	if leftTokens[0].typ == tokenIdent && leftTokens[0].lit == "def" {
		leftTokens = trimDeclarationTokens(leftTokens[1:])
	}

	if len(leftTokens) != 1 || leftTokens[0].typ != tokenIdent {
		return nil, fmt.Errorf("unsupported expression %q", expressionText(loopTokens))
	}

	collection, err := parseExprTokens(trimDeclarationTokens(loopTokens[inIndex+1:]))
	if err != nil {
		return nil, err
	}

	body, err := p.parseSingleOrBlock()
	if err != nil {
		return nil, err
	}

	return evalForStmt{varName: leftTokens[0].lit, collection: collection, body: body}, nil
}

func (p *evalStatementParser) parseTryStmt() (any, error) {
	p.pos++

	tryBody, err := p.parseSingleOrBlock()
	if err != nil {
		return nil, err
	}

	p.skipSeparators()

	catchClauses := make([]evalCatchStmt, 0)

	for !p.atEnd() && p.current().typ == tokenIdent && p.current().lit == "catch" {
		p.pos++

		catchTokens, readErr := p.readWrappedTokens(tokenLParen, tokenRParen)
		if readErr != nil {
			return nil, readErr
		}

		trimmed := trimDeclarationTokens(catchTokens)
		if len(trimmed) == 0 {
			return nil, errors.New("expected catch clause")
		}

		varName := ""
		typeName := ""

		if len(trimmed) == 1 {
			typeName = trimmed[0].lit
		} else {
			last := trimmed[len(trimmed)-1]
			if last.typ == tokenIdent {
				varName = last.lit
				typeName = expressionText(trimmed[:len(trimmed)-1])
			} else {
				typeName = expressionText(trimmed)
			}
		}

		body, parseErr := p.parseSingleOrBlock()
		if parseErr != nil {
			return nil, parseErr
		}

		catchClauses = append(catchClauses, evalCatchStmt{typeName: typeName, varName: varName, body: body})

		p.skipSeparators()
	}

	finallyBody := []any(nil)

	if !p.atEnd() && p.current().typ == tokenIdent && p.current().lit == "finally" {
		p.pos++

		finallyBody, err = p.parseSingleOrBlock()
		if err != nil {
			return nil, err
		}
	}

	return evalTryStmt{tryBody: tryBody, catchClauses: catchClauses, finallyBody: finallyBody}, nil
}

func (p *evalStatementParser) parseSwitchStmt() (any, error) {
	p.pos++

	expr, err := p.parseParenExpr()
	if err != nil {
		return nil, err
	}

	bodyTokens, err := p.readWrappedTokens(tokenLBrace, tokenRBrace)
	if err != nil {
		return nil, err
	}

	stmt := evalSwitchStmt{expr: expr, cases: make([]evalSwitchCase, 0)}

	index := 0
	for index < len(bodyTokens) {
		for index < len(bodyTokens) && (bodyTokens[index].typ == tokenNewline || bodyTokens[index].typ == tokenSemicolon) {
			index++
		}

		if index >= len(bodyTokens) {
			break
		}

		label := bodyTokens[index]
		if label.typ != tokenIdent || (label.lit != "case" && label.lit != "default") {
			return nil, fmt.Errorf("unsupported switch clause %q", label.lit)
		}

		index++

		var caseExpr Expr

		if label.lit == "case" {
			clauseStart := index
			parenDepth := 0
			braceDepth := 0
			bracketDepth := 0

			for index < len(bodyTokens) {
				current := bodyTokens[index]
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

				if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 && current.typ == tokenColon {
					break
				}

				index++
			}

			if index >= len(bodyTokens) || bodyTokens[index].typ != tokenColon {
				return nil, errors.New("expected : after switch case")
			}

			caseExpr, err = parseExprTokens(trimDeclarationTokens(bodyTokens[clauseStart:index]))
			if err != nil {
				return nil, err
			}
		}

		if index >= len(bodyTokens) || bodyTokens[index].typ != tokenColon {
			return nil, errors.New("expected : after switch clause")
		}

		index++

		branchStart := index
		parenDepth := 0
		braceDepth := 0
		bracketDepth := 0

		for index < len(bodyTokens) {
			current := bodyTokens[index]
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

			if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 &&
				current.typ == tokenIdent && (current.lit == "case" || current.lit == "default") {
				break
			}

			index++
		}

		branchBody, err := parseEvalStatementsFromTokens(bodyTokens[branchStart:index])
		if err != nil {
			return nil, err
		}

		if label.lit == "default" {
			stmt.defaultBody = branchBody
		} else {
			stmt.cases = append(stmt.cases, evalSwitchCase{expr: caseExpr, body: branchBody})
		}
	}

	return stmt, nil
}

func (p *evalStatementParser) parseAssignmentStmt(declare bool) (any, error) {
	if declare {
		p.pos++
	}

	if p.atEnd() || p.current().typ != tokenIdent {
		return nil, errors.New("expected assignment target")
	}

	name := p.current().lit
	p.pos++

	if p.atEnd() {
		return nil, errors.New("expected assignment operator")
	}

	if p.current().typ == tokenAssign {
		p.pos++

		exprTokens := trimDeclarationTokens(p.readStatementExprTokens())
		if len(exprTokens) == 0 {
			return nil, errors.New("expected expression")
		}

		expr, err := parseExprTokens(exprTokens)
		if err != nil {
			return nil, err
		}

		return evalAssignStmt{name: name, expr: expr}, nil
	}

	if p.current().typ == tokenSymbol && p.peek().typ == tokenAssign {
		op := p.current().lit
		p.pos += 2

		exprTokens := trimDeclarationTokens(p.readStatementExprTokens())
		if len(exprTokens) == 0 {
			return nil, errors.New("expected expression")
		}

		expr, err := parseExprTokens(exprTokens)
		if err != nil {
			return nil, err
		}

		return evalAugAssignStmt{name: name, op: op, expr: expr}, nil
	}

	return nil, errors.New("expected assignment operator")
}

func (p *evalStatementParser) parseSingleOrBlock() ([]any, error) {
	p.skipSeparators()

	if p.atEnd() {
		return nil, errors.New("expected statement")
	}

	if p.current().typ == tokenLBrace {
		bodyTokens, err := p.readWrappedTokens(tokenLBrace, tokenRBrace)
		if err != nil {
			return nil, err
		}

		return parseEvalStatementsFromTokens(bodyTokens)
	}

	stmt, err := p.parseStatement()
	if err != nil {
		return nil, err
	}

	return []any{stmt}, nil
}

func (p *evalStatementParser) parseParenExpr() (Expr, error) {
	tokens, err := p.readWrappedTokens(tokenLParen, tokenRParen)
	if err != nil {
		return nil, err
	}

	return parseExprTokens(trimDeclarationTokens(tokens))
}

func (p *evalStatementParser) readWrappedTokens(open, closeType tokenType) ([]token, error) {
	if p.atEnd() || p.current().typ != open {
		return nil, fmt.Errorf("expected %s", p.current().lit)
	}

	depth := 0

	start := p.pos + 1
	for p.pos < len(p.tokens) {
		current := p.tokens[p.pos]
		switch current.typ {
		case open:
			depth++
		case closeType:
			depth--
			if depth == 0 {
				inner := append([]token{}, p.tokens[start:p.pos]...)
				p.pos++

				return inner, nil
			}
		}

		p.pos++
	}

	return nil, errors.New("unterminated block")
}

func (p *evalStatementParser) readStatementExprTokens() []token {
	start := p.pos
	parenDepth := 0
	braceDepth := 0
	bracketDepth := 0

	for p.pos < len(p.tokens) {
		current := p.tokens[p.pos]
		if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 &&
			(current.typ == tokenNewline || current.typ == tokenSemicolon) {
			break
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

		p.pos++
	}

	return append([]token{}, p.tokens[start:p.pos]...)
}

func (p *evalStatementParser) startsAssignment() bool {
	if p.atEnd() || p.current().typ != tokenIdent {
		return false
	}

	if p.peek().typ == tokenAssign {
		return true
	}

	return p.peek().typ == tokenSymbol && p.peekN(2).typ == tokenAssign
}

func (p *evalStatementParser) skipSeparators() {
	for !p.atEnd() && (p.current().typ == tokenNewline || p.current().typ == tokenSemicolon) {
		p.pos++
	}
}

func (p *evalStatementParser) atEnd() bool {
	return p.pos >= len(p.tokens)
}

func (p *evalStatementParser) current() token {
	if p.atEnd() {
		return token{typ: tokenEOF}
	}

	return p.tokens[p.pos]
}

func (p *evalStatementParser) peek() token {
	return p.peekN(1)
}

func (p *evalStatementParser) peekN(offset int) token {
	index := p.pos + offset
	if index >= len(p.tokens) {
		return token{typ: tokenEOF}
	}

	return p.tokens[index]
}

type mapEntry struct {
	key   string
	value any
}

type orderedMap struct {
	values  map[string]any
	entries []mapEntry
}

func evalAssertStatement(expr Expr, message Expr, vars map[string]any) error {
	value, err := EvalExpr(expr, vars)
	if err != nil {
		return nil
	}

	if isTruthy(value) {
		return nil
	}

	messageText := "Assertion failed"

	if message != nil {
		resolved, messageErr := EvalExpr(message, vars)
		if messageErr == nil && resolved != nil {
			messageText = fmt.Sprint(resolved)
		}
	}

	level := "warning"
	if isCompileTimeConstantExpr(expr) {
		level = "error"
	}

	warnf("nextflowdsl: %s: %s\n", level, messageText)

	return nil
}

func isCompileTimeConstantExpr(expr Expr) bool {
	switch typed := expr.(type) {
	case IntExpr, StringExpr, SlashyStringExpr, BoolExpr, NullExpr:
		return true
	case ParamsExpr:
		return true
	case UnaryExpr:
		return isCompileTimeConstantExpr(typed.Operand)
	case BinaryExpr:
		return isCompileTimeConstantExpr(typed.Left) && isCompileTimeConstantExpr(typed.Right)
	case InExpr:
		return isCompileTimeConstantExpr(typed.Left) && isCompileTimeConstantExpr(typed.Right)
	case RegexExpr:
		return isCompileTimeConstantExpr(typed.Left) && isCompileTimeConstantExpr(typed.Right)
	case TernaryExpr:
		return isCompileTimeConstantExpr(typed.Cond) &&
			isCompileTimeConstantExpr(typed.True) &&
			isCompileTimeConstantExpr(typed.False)
	case CastExpr:
		return isCompileTimeConstantExpr(typed.Operand)
	case ListExpr:
		for _, element := range typed.Elements {
			if !isCompileTimeConstantExpr(element) {
				return false
			}
		}

		return true
	case MapExpr:
		for _, key := range typed.Keys {
			if !isCompileTimeConstantExpr(key) {
				return false
			}
		}

		for _, value := range typed.Values {
			if !isCompileTimeConstantExpr(value) {
				return false
			}
		}

		return true
	case IndexExpr:
		return isCompileTimeConstantExpr(typed.Receiver) && isCompileTimeConstantExpr(typed.Index)
	default:
		return false
	}
}

func bindWorkflowEnumValues(vars map[string]any, wf *Workflow) map[string]any {
	if wf == nil || len(wf.Enums) == 0 {
		return vars
	}

	bound := cloneEvalVars(vars)

	enumValues := make(map[string]map[string]struct{}, len(wf.Enums))
	for _, enumDef := range wf.Enums {
		if enumDef == nil {
			continue
		}

		values := make(map[string]struct{}, len(enumDef.Values))
		for _, value := range enumDef.Values {
			values[value] = struct{}{}
		}

		enumValues[enumDef.Name] = values
	}

	bound[workflowEnumValuesKey] = enumValues

	return bound
}

func evalSimpleFuncDef(funcDef *FuncDef, args []any, vars map[string]any) (any, error) {
	if funcDef == nil {
		return nil, errors.New("nil function")
	}

	if len(args) != len(funcDef.Params) {
		return nil, fmt.Errorf("function %q expects %d arguments, got %d", funcDef.Name, len(funcDef.Params), len(args))
	}

	scope := cloneEvalVars(vars)
	for index, name := range funcDef.Params {
		scope[name] = cloneChannelValue(args[index])
	}

	return evalStatementBody(funcDef.Body, scope)
}

func evalAugAssignValue(current, right any, op string) (any, error) {
	if op == "+" {
		if list, ok := current.([]any); ok {
			updated := cloneChannelSlice(list)
			if values, ok := right.([]any); ok {
				return append(updated, cloneChannelSlice(values)...), nil
			}

			return append(updated, cloneChannelValue(right)), nil
		}
	}

	return evalBinaryExpr(BinaryExpr{Left: renderValueAsExpr(current), Op: op, Right: renderValueAsExpr(right)}, nil)
}

func renderValueAsExpr(value any) Expr {
	switch typed := value.(type) {
	case int:
		return IntExpr{Value: typed}
	case string:
		return StringExpr{Value: typed}
	case bool:
		return BoolExpr{Value: typed}
	case nil:
		return NullExpr{}
	case []any:
		elements := make([]Expr, 0, len(typed))
		for _, item := range typed {
			elements = append(elements, renderValueAsExpr(item))
		}

		return ListExpr{Elements: elements}
	default:
		return UnsupportedExpr{Text: renderValueForUnsupported(value)}
	}
}

func iterValues(collection any) ([]any, error) {
	if values, ok := closureTupleValues(collection); ok {
		return values, nil
	}

	return nil, fmt.Errorf("unsupported loop collection %T", collection)
}

func matchesSwitchCase(target any, expr Expr, scope map[string]any) (bool, error) {
	if unary, ok := expr.(UnaryExpr); ok && unary.Op == "~" {
		patternValue, err := EvalExpr(unary.Operand, scope)
		if err != nil {
			return false, err
		}

		pattern, ok := patternValue.(string)
		if !ok {
			return false, fmt.Errorf("unsupported regex pattern %T", patternValue)
		}

		targetString, ok := target.(string)
		if !ok {
			return false, nil
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, err
		}

		return re.MatchString(targetString), nil
	}

	value, err := EvalExpr(expr, scope)
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(target, value), nil
}

func matchesCatchClause(typeName string, err error) bool {
	if err == nil {
		return false
	}

	switch typeName {
	case "", "Exception", "RuntimeException", "Throwable":
		return true
	default:
		return strings.HasSuffix(typeName, "Exception")
	}
}
