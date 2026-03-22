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
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var unsupportedCardinalityOperators = map[string]struct{}{
	"branch":       {},
	"collectFile":  {},
	"combine":      {},
	"concat":       {},
	"count":        {},
	"distinct":     {},
	"flatten":      {},
	"ifEmpty":      {},
	"multiMap":     {},
	"reduce":       {},
	"splitCsv":     {},
	"splitFasta":   {},
	"splitFastq":   {},
	"toList":       {},
	"toSortedList": {},
	"transpose":    {},
	"unique":       {},
}

var warningOnlyChannelOperators = map[string]struct{}{
	"randomSample": {},
	"splitJson":    {},
	"splitText":    {},
	"subscribe":    {},
	"until":        {},
}

var deprecatedChannelOperators = map[string]struct{}{
	"countFasta": {},
	"countFastq": {},
	"countJson":  {},
	"countLines": {},
	"merge":      {},
	"toInteger":  {},
}

type channelItem struct {
	value     any
	depGroups []string
}

func resolveChannelLiteralItems(args []Expr) ([]channelItem, error) {
	items := make([]channelItem, 0, len(args))
	for _, arg := range args {
		value, err := EvalExpr(arg, nil)
		if err != nil {
			return nil, err
		}
		items = append(items, channelItem{value: value})
	}

	return items, nil
}

func resolveChannelFromListItems(args []Expr) ([]channelItem, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("Channel.fromList expects 1 argument, got %d", len(args))
	}

	value, err := EvalExpr(args[0], nil)
	if err != nil {
		return nil, err
	}

	values := flattenChannelValues(value)
	items := make([]channelItem, 0, len(values))
	for _, item := range values {
		items = append(items, channelItem{value: item})
	}

	return items, nil
}

func warnDeprecatedChannelFactory(name string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: deprecated channel factory %q resolved as Channel.of for compatibility\n", name)
}

func warnUntranslatableChannelFactory(name string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: channel factory %q cannot be translated at compile time and will resolve to an empty channel\n", name)
}

func warnUnsupportedChannelClosure(closure string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: closure %q could not be evaluated at compile time and will be treated as a pass-through\n", closure)
}

func resolveChunkSize(args []Expr, operatorName, namedArg string) (int, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("%s expects 1 argument, got %d", operatorName, len(args))
	}

	value, err := EvalExpr(args[0], nil)
	if err != nil {
		return 0, err
	}

	size, err := coerceChunkSize(value, namedArg)
	if err != nil {
		return 0, fmt.Errorf("%s %w", operatorName, err)
	}
	if size <= 0 {
		return 0, fmt.Errorf("%s expects a positive chunk size", operatorName)
	}

	return size, nil
}

func coerceChunkSize(value any, namedArg string) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case map[string]any:
		raw, ok := typed[namedArg]
		if !ok {
			return 0, fmt.Errorf("expects %q to be set", namedArg)
		}

		size, ok := raw.(int)
		if !ok {
			return 0, fmt.Errorf("expects %q to be an integer", namedArg)
		}

		return size, nil
	default:
		return 0, fmt.Errorf("expects an integer chunk size")
	}
}

func warnDeprecatedChannelOperator(name string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: deprecated channel operator %q resolved as a pass-through for compatibility\n", name)
}

func warnUnsupportedCardinalityOperator(name string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: operator %q may affect job cardinality in unsupported ways and will be treated as a pass-through\n", name)
}

func crossChannelItems(left, right []channelItem) []channelItem {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}

	crossed := make([]channelItem, 0, len(left)*len(right))
	for _, leftItem := range left {
		for _, rightItem := range right {
			crossed = append(crossed, channelItem{
				value:     []any{cloneChannelValue(leftItem.value), cloneChannelValue(rightItem.value)},
				depGroups: appendUniqueStrings(cloneStrings(leftItem.depGroups), rightItem.depGroups),
			})
		}
	}

	return crossed
}

func chunkChannelItems(items []channelItem, size int) []channelItem {
	if len(items) == 0 {
		return nil
	}

	chunked := make([]channelItem, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}

		values := make([]any, 0, end-start)
		deps := []string{}
		for _, item := range items[start:end] {
			values = append(values, cloneChannelValue(item.value))
			deps = appendUniqueStrings(deps, item.depGroups)
		}

		chunked = append(chunked, channelItem{value: values, depGroups: deps})
	}

	return chunked
}

func reduceChannelItems(items []channelItem, pick func(current, candidate any) (any, error)) ([]channelItem, error) {
	if len(items) == 0 {
		return nil, nil
	}

	reduced := cloneChannelValue(items[0].value)
	for _, item := range items[1:] {
		var err error
		reduced, err = pick(reduced, item.value)
		if err != nil {
			return nil, err
		}
	}

	return []channelItem{{value: reduced, depGroups: unionChannelDepGroups(items)}}, nil
}

func sumChannelItems(items []channelItem) ([]channelItem, error) {
	if len(items) == 0 {
		return nil, nil
	}

	total := 0
	for _, item := range items {
		value, ok := item.value.(int)
		if !ok {
			return nil, fmt.Errorf("sum expects integer channel items")
		}

		total += value
	}

	return []channelItem{{value: total, depGroups: unionChannelDepGroups(items)}}, nil
}

type channelResolver func(ChanRef) ([]channelItem, error)

// ResolveChannel evaluates a ChanExpr and returns the resulting channel items.
func ResolveChannel(ce ChanExpr, cwd string) ([]any, error) {
	items, err := resolveChannelItems(ce, cwd, nil)
	if err != nil {
		return nil, err
	}

	values := make([]any, 0, len(items))
	for _, item := range items {
		values = append(values, item.value)
	}

	return values, nil
}

func resolveChannelItems(ce ChanExpr, cwd string, resolver channelResolver) ([]channelItem, error) {
	switch expr := ce.(type) {
	case ChanRef:
		if resolver == nil {
			return nil, fmt.Errorf("channel reference %q cannot be resolved at translate time", expr.Name)
		}

		return resolver(expr)
	case ChannelFactory:
		return resolveChannelFactoryItems(expr, cwd)
	case ChannelChain:
		items, err := resolveChannelItems(expr.Source, cwd, resolver)
		if err != nil {
			return nil, err
		}

		for _, operator := range expr.Operators {
			items, err = applyChannelOperator(items, operator, cwd, resolver)
			if err != nil {
				return nil, err
			}
		}

		return items, nil
	case PipeExpr:
		if len(expr.Stages) != 1 {
			return nil, fmt.Errorf("pipe expressions require D6 translation support")
		}

		return resolveChannelItems(expr.Stages[0], cwd, resolver)
	default:
		return nil, fmt.Errorf("channel expression %T cannot be resolved at translate time", ce)
	}
}

func resolveChannelFactoryItems(factory ChannelFactory, cwd string) ([]channelItem, error) {
	switch factory.Name {
	case "of":
		return resolveChannelLiteralItems(factory.Args)
	case "from":
		warnDeprecatedChannelFactory(factory.Name)

		return resolveChannelLiteralItems(factory.Args)
	case "fromList":
		return resolveChannelFromListItems(factory.Args)
	case "value":
		if len(factory.Args) != 1 {
			return nil, fmt.Errorf("Channel.value expects 1 argument, got %d", len(factory.Args))
		}

		value, err := EvalExpr(factory.Args[0], nil)
		if err != nil {
			return nil, err
		}

		return []channelItem{{value: value}}, nil
	case "empty":
		if len(factory.Args) != 0 {
			return nil, fmt.Errorf("Channel.empty expects 0 arguments, got %d", len(factory.Args))
		}

		return nil, nil
	case "fromPath":
		pattern, err := resolveChannelPattern(factory.Args, cwd, "Channel.fromPath")
		if err != nil {
			return nil, err
		}

		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}

		items := make([]channelItem, 0, len(matches))
		for _, match := range matches {
			items = append(items, channelItem{value: filepath.Clean(match)})
		}

		return items, nil
	case "fromFilePairs":
		pattern, err := resolveChannelPattern(factory.Args, cwd, "Channel.fromFilePairs")
		if err != nil {
			return nil, err
		}

		values, err := resolveFilePairs(pattern)
		if err != nil {
			return nil, err
		}

		items := make([]channelItem, 0, len(values))
		for _, value := range values {
			items = append(items, channelItem{value: value})
		}

		return items, nil
	case "fromLineage", "fromSRA", "interval", "topic", "watchPath":
		warnUntranslatableChannelFactory(factory.Name)

		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported channel factory %q", factory.Name)
	}
}

func applyChannelOperator(items []channelItem, operator ChannelOperator, cwd string, resolver channelResolver) ([]channelItem, error) {
	switch operator.Name {
	case "collect":
		return []channelItem{{value: collectChannelValues(items), depGroups: unionChannelDepGroups(items)}}, nil
	case "first":
		if len(items) == 0 {
			return nil, nil
		}

		return cloneChannelItems(items[:1]), nil
	case "last":
		if len(items) == 0 {
			return nil, nil
		}

		return cloneChannelItems(items[len(items)-1:]), nil
	case "take":
		if len(operator.Args) != 1 {
			return nil, fmt.Errorf("take expects 1 argument, got %d", len(operator.Args))
		}

		count, err := EvalExpr(operator.Args[0], nil)
		if err != nil {
			return nil, err
		}

		limit, ok := count.(int)
		if !ok {
			return nil, fmt.Errorf("take expects an integer argument")
		}
		if limit <= 0 {
			return nil, nil
		}
		if limit > len(items) {
			limit = len(items)
		}

		return cloneChannelItems(items[:limit]), nil
	case "filter":
		filtered := make([]channelItem, 0, len(items))
		for _, item := range items {
			keep, err := evalChannelClosureBool(operator, item.value)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					warnUnsupportedChannelClosure(strings.TrimSpace(operator.ClosureExprOrText()))
					return cloneChannelItems(items), nil
				}

				return nil, err
			}
			if keep {
				filtered = append(filtered, cloneChannelItem(item))
			}
		}

		return filtered, nil
	case "map":
		mapped := make([]channelItem, 0, len(items))
		for _, item := range items {
			value, err := evalChannelClosure(operator, item.value)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					warnUnsupportedChannelClosure(strings.TrimSpace(operator.ClosureExprOrText()))
					return cloneChannelItems(items), nil
				}

				return nil, err
			}
			mapped = append(mapped, channelItem{value: value, depGroups: cloneStrings(item.depGroups)})
		}

		return mapped, nil
	case "flatMap":
		flattened := []channelItem{}
		for _, item := range items {
			value, err := evalChannelClosure(operator, item.value)
			if err != nil {
				if errors.Is(err, errUnsupportedClosure) {
					warnUnsupportedChannelClosure(strings.TrimSpace(operator.ClosureExprOrText()))
					return cloneChannelItems(items), nil
				}

				return nil, err
			}

			for _, mapped := range flattenChannelValues(value) {
				flattened = append(flattened, channelItem{value: mapped, depGroups: cloneStrings(item.depGroups)})
			}
		}

		return flattened, nil
	case "mix":
		mixed := cloneChannelItems(items)
		for _, other := range operator.Channels {
			resolved, err := resolveChannelItems(other, cwd, resolver)
			if err != nil {
				return nil, err
			}
			mixed = append(mixed, cloneChannelItems(resolved)...)
		}

		return mixed, nil
	case "cross":
		crossed := cloneChannelItems(items)
		for _, other := range operator.Channels {
			resolved, err := resolveChannelItems(other, cwd, resolver)
			if err != nil {
				return nil, err
			}
			crossed = crossChannelItems(crossed, resolved)
		}

		return crossed, nil
	case "join":
		joined := cloneChannelItems(items)
		for _, other := range operator.Channels {
			resolved, err := resolveChannelItems(other, cwd, resolver)
			if err != nil {
				return nil, err
			}
			joined = joinChannelItems(joined, resolved)
		}

		return joined, nil
	case "groupTuple":
		return groupTupleItems(items), nil
	case "buffer":
		size, err := resolveChunkSize(operator.Args, "buffer", "size")
		if err != nil {
			return nil, err
		}

		return chunkChannelItems(items, size), nil
	case "collate":
		size, err := resolveChunkSize(operator.Args, "collate", "size")
		if err != nil {
			return nil, err
		}

		return chunkChannelItems(items, size), nil
	case "min":
		return reduceChannelItems(items, minChannelValue)
	case "max":
		return reduceChannelItems(items, maxChannelValue)
	case "sum":
		return sumChannelItems(items)
	case "dump", "set", "tap", "view":
		return cloneChannelItems(items), nil
	default:
		if _, ok := deprecatedChannelOperators[operator.Name]; ok {
			warnDeprecatedChannelOperator(operator.Name)
			return cloneChannelItems(items), nil
		}

		if _, ok := warningOnlyChannelOperators[operator.Name]; ok {
			warnUnsupportedCardinalityOperator(operator.Name)
			return cloneChannelItems(items), nil
		}

		if _, ok := unsupportedCardinalityOperators[operator.Name]; ok {
			warnUnsupportedCardinalityOperator(operator.Name)
			return cloneChannelItems(items), nil
		}

		return nil, fmt.Errorf("unsupported channel operator %q", operator.Name)
	}
}

func collectChannelValues(items []channelItem) []any {
	values := make([]any, 0, len(items))
	for _, item := range items {
		values = append(values, cloneChannelValue(item.value))
	}

	return values
}

func unionChannelDepGroups(items []channelItem) []string {
	deps := []string{}
	for _, item := range items {
		deps = appendUniqueStrings(deps, item.depGroups)
	}

	return deps
}

func evalChannelClosureBool(operator ChannelOperator, value any) (bool, error) {
	resolved, err := evalChannelClosure(operator, value)
	if err != nil {
		return false, err
	}

	return isTruthy(resolved), nil
}

func evalChannelClosure(operator ChannelOperator, value any) (any, error) {
	if operator.ClosureExpr != nil {
		return evalSimpleClosure(*operator.ClosureExpr, value, nil)
	}

	closure := strings.TrimSpace(operator.Closure)
	if closure == "" {
		return value, nil
	}

	return evalSimpleClosure(ClosureExpr{Body: closure}, value, nil)
}

func flattenChannelValues(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		flattened := make([]any, 0, len(typed))
		for _, item := range typed {
			flattened = append(flattened, cloneChannelValue(item))
		}

		return flattened
	case []string:
		flattened := make([]any, 0, len(typed))
		for _, item := range typed {
			flattened = append(flattened, item)
		}

		return flattened
	default:
		return []any{typed}
	}
}

func joinChannelItems(left, right []channelItem) []channelItem {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}

	rightByKey := make(map[string][]channelItem)
	for _, item := range right {
		key := channelItemKey(item)
		rightByKey[key] = append(rightByKey[key], cloneChannelItem(item))
	}

	joined := []channelItem{}
	for _, item := range left {
		matches := rightByKey[channelItemKey(item)]
		for _, match := range matches {
			joined = append(joined, channelItem{
				value:     joinChannelValues(item.value, match.value),
				depGroups: appendUniqueStrings(cloneStrings(item.depGroups), match.depGroups),
			})
		}
	}

	return joined
}

func groupTupleItems(items []channelItem) []channelItem {
	if len(items) == 0 {
		return nil
	}

	type groupedTuple struct {
		rows      [][]any
		depGroups []string
	}

	grouped := make(map[string]*groupedTuple)
	keys := []string{}
	for _, item := range items {
		row := channelTuple(item.value)
		key := fmt.Sprint(row[0])
		entry, ok := grouped[key]
		if !ok {
			entry = &groupedTuple{}
			grouped[key] = entry
			keys = append(keys, key)
		}
		entry.rows = append(entry.rows, row)
		entry.depGroups = appendUniqueStrings(entry.depGroups, item.depGroups)
	}

	result := make([]channelItem, 0, len(keys))
	for _, key := range keys {
		entry := grouped[key]
		columns := make([][]any, 0)
		for _, row := range entry.rows {
			for index := 1; index < len(row); index++ {
				for len(columns) < index {
					columns = append(columns, []any{})
				}
				columns[index-1] = append(columns[index-1], cloneChannelValue(row[index]))
			}
		}

		groupedValue := []any{cloneChannelValue(entry.rows[0][0])}
		for _, column := range columns {
			groupedValue = append(groupedValue, column)
		}

		result = append(result, channelItem{value: groupedValue, depGroups: cloneStrings(entry.depGroups)})
	}

	return result
}

func channelItemKey(item channelItem) string {
	row := channelTuple(item.value)
	if len(row) == 0 {
		return ""
	}

	return fmt.Sprint(row[0])
}

func joinChannelValues(left, right any) any {
	leftTuple := channelTuple(left)
	rightTuple := channelTuple(right)
	if len(rightTuple) == 0 {
		return leftTuple
	}

	joined := make([]any, 0, len(leftTuple)+len(rightTuple))
	joined = append(joined, cloneChannelSlice(leftTuple)...)
	if len(leftTuple) > 0 && len(rightTuple) > 0 && fmt.Sprint(leftTuple[0]) == fmt.Sprint(rightTuple[0]) {
		joined = append(joined, cloneChannelSlice(rightTuple[1:])...)
	} else {
		joined = append(joined, cloneChannelSlice(rightTuple)...)
	}

	return joined
}

func channelTuple(value any) []any {
	switch typed := value.(type) {
	case []any:
		return cloneChannelSlice(typed)
	case []string:
		row := make([]any, 0, len(typed))
		for _, item := range typed {
			row = append(row, item)
		}

		return row
	default:
		return []any{cloneChannelValue(typed)}
	}
}

func cloneChannelItems(items []channelItem) []channelItem {
	if len(items) == 0 {
		return nil
	}

	cloned := make([]channelItem, len(items))
	for index, item := range items {
		cloned[index] = cloneChannelItem(item)
	}

	return cloned
}

func cloneChannelItem(item channelItem) channelItem {
	return channelItem{value: cloneChannelValue(item.value), depGroups: cloneStrings(item.depGroups)}
}

func cloneChannelValue(value any) any {
	switch typed := value.(type) {
	case []any:
		return cloneChannelSlice(typed)
	case []string:
		return cloneStrings(typed)
	default:
		return typed
	}
}

func cloneChannelSlice(values []any) []any {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]any, len(values))
	for index, value := range values {
		cloned[index] = cloneChannelValue(value)
	}

	return cloned
}

func resolveChannelPattern(args []Expr, cwd, name string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s expects 1 argument, got %d", name, len(args))
	}

	value, err := EvalExpr(args[0], nil)
	if err != nil {
		return "", err
	}

	pattern, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s expects a string glob", name)
	}

	if filepath.IsAbs(pattern) {
		return filepath.Clean(pattern), nil
	}

	return filepath.Join(cwd, pattern), nil
}

func resolveFilePairs(pattern string) ([]any, error) {
	expanded := expandBracePatterns(pattern)
	grouped := make(map[string][]string)
	keys := make([]string, 0)
	seenPaths := make(map[string]struct{})

	for _, candidate := range expanded {
		matches, err := filepath.Glob(candidate)
		if err != nil {
			return nil, err
		}

		for _, match := range matches {
			cleaned := filepath.Clean(match)
			if _, seen := seenPaths[cleaned]; seen {
				continue
			}
			seenPaths[cleaned] = struct{}{}

			key := filePairGroupKey(cleaned)
			if _, ok := grouped[key]; !ok {
				keys = append(keys, key)
			}
			grouped[key] = append(grouped[key], cleaned)
		}
	}

	sort.Strings(keys)
	items := make([]any, 0, len(keys))
	for _, key := range keys {
		matches := grouped[key]
		sort.Strings(matches)
		items = append(items, cloneStrings(matches))
	}

	return items, nil
}

func expandBracePatterns(pattern string) []string {
	start := strings.IndexByte(pattern, '{')
	if start == -1 {
		return []string{pattern}
	}

	end := strings.IndexByte(pattern[start:], '}')
	if end == -1 {
		return []string{pattern}
	}
	end += start

	prefix := pattern[:start]
	suffix := pattern[end+1:]
	parts := strings.Split(pattern[start+1:end], ",")
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		patterns = append(patterns, prefix+strings.TrimSpace(part)+suffix)
	}

	return patterns
}

func filePairGroupKey(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if index := strings.LastIndex(base, "_"); index >= 0 {
		base = base[:index]
	}

	return filepath.Join(filepath.Dir(path), base)
}

func minChannelValue(current, candidate any) (any, error) {
	less, err := channelValueLess(candidate, current)
	if err != nil {
		return nil, err
	}
	if less {
		return cloneChannelValue(candidate), nil
	}

	return cloneChannelValue(current), nil
}

func maxChannelValue(current, candidate any) (any, error) {
	less, err := channelValueLess(current, candidate)
	if err != nil {
		return nil, err
	}
	if less {
		return cloneChannelValue(candidate), nil
	}

	return cloneChannelValue(current), nil
}

func channelValueLess(left, right any) (bool, error) {
	switch leftValue := left.(type) {
	case int:
		rightValue, ok := right.(int)
		if !ok {
			return false, fmt.Errorf("cannot compare %T and %T", left, right)
		}

		return leftValue < rightValue, nil
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return false, fmt.Errorf("cannot compare %T and %T", left, right)
		}

		return leftValue < rightValue, nil
	default:
		return false, fmt.Errorf("unsupported comparable channel item %T", left)
	}
}

func (operator ChannelOperator) ClosureExprOrText() string {
	if operator.ClosureExpr != nil {
		return operator.ClosureExpr.Body
	}

	return operator.Closure
}
