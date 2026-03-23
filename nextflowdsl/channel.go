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
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

var unsupportedCardinalityOperators = map[string]struct{}{
	"reduce": {},
}

var warningOnlyChannelOperators = map[string]struct{}{
	"randomSample": {},
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

func selectNamedChannelItems(items []channelItem, label string) []channelItem {
	selected := make([]channelItem, 0, len(items))
	for _, item := range items {
		for _, depGroup := range item.depGroups {
			if depGroup == label || strings.HasSuffix(depGroup, "."+label) {
				selected = append(selected, cloneChannelItem(item))
				break
			}
		}
	}

	return selected
}

func warnDeprecatedChannelFactory(name string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: deprecated channel factory %q resolved as Channel.of for compatibility\n", name)
}

func warnUntranslatableChannelFactory(name string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: channel factory %q cannot be translated at compile time and will resolve to an empty channel\n", name)
}

func resolveBranchChannelItems(items []channelItem, operator ChannelOperator) (namedChannelResult, error) {
	entries, err := parseNamedClosureEntries(operator)
	if err != nil {
		return namedChannelResult{}, err
	}

	result := namedChannelResult{labels: make([]string, 0, len(entries)), items: make(map[string][]channelItem, len(entries))}
	for _, entry := range entries {
		result.labels = append(result.labels, entry.label)
	}

	for _, item := range items {
		scope, err := bindOperatorClosureVars(operator, item.value)
		if err != nil {
			return namedChannelResult{}, err
		}

		matchedLabel := ""
		fallbackLabel := ""
		for _, entry := range entries {
			if entry.isFallback {
				fallbackLabel = entry.label
				continue
			}

			matched, evalErr := EvalExpr(entry.expr, scope)
			if evalErr != nil {
				return namedChannelResult{}, evalErr
			}
			if isTruthy(matched) {
				matchedLabel = entry.label
				break
			}
		}

		if matchedLabel == "" {
			matchedLabel = fallbackLabel
		}
		if matchedLabel == "" {
			continue
		}

		result.items[matchedLabel] = append(result.items[matchedLabel], channelItem{
			value:     cloneChannelValue(item.value),
			depGroups: labelScopedDepGroups(item.depGroups, matchedLabel),
		})
	}

	return result, nil
}

func parseNamedClosureEntries(operator ChannelOperator) ([]namedClosureEntry, error) {
	body := strings.TrimSpace(operator.ClosureExprOrText())
	if body == "" {
		return nil, fmt.Errorf("%s expects a closure body", operator.Name)
	}

	tokens, err := lex(body)
	if err != nil {
		return nil, err
	}

	tokens = trimDeclarationTokens(tokens)
	paramsTokens, bodyTokens, hasArrow := splitClosureArrowTokens(tokens)
	if hasArrow {
		if len(trimDeclarationTokens(paramsTokens)) > 0 {
			bodyTokens = trimDeclarationTokens(bodyTokens)
		}
	} else {
		bodyTokens = tokens
	}

	entries := make([]namedClosureEntry, 0)
	index := 0
	for index < len(bodyTokens) {
		for index < len(bodyTokens) && (bodyTokens[index].typ == tokenNewline || bodyTokens[index].typ == tokenSemicolon || bodyTokens[index].typ == tokenEOF) {
			index++
		}
		if index >= len(bodyTokens) {
			break
		}
		if bodyTokens[index].typ != tokenIdent {
			return nil, fmt.Errorf("%s expects named closure entries", operator.Name)
		}

		label := bodyTokens[index].lit
		index++
		if index >= len(bodyTokens) || bodyTokens[index].typ != tokenColon {
			return nil, fmt.Errorf("%s expected ':' after %q", operator.Name, label)
		}
		index++

		exprStart := index
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

			if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 && (current.typ == tokenNewline || current.typ == tokenSemicolon || current.typ == tokenEOF) {
				break
			}
			index++
		}

		exprTokens := trimDeclarationTokens(bodyTokens[exprStart:index])
		if len(exprTokens) == 0 {
			return nil, fmt.Errorf("%s expected expression for %q", operator.Name, label)
		}

		expr, parseErr := parseExprTokens(exprTokens)
		if parseErr != nil {
			return nil, parseErr
		}

		entries = append(entries, namedClosureEntry{label: label, expr: expr, isFallback: label == "default"})
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("%s expects at least one named closure entry", operator.Name)
	}

	return entries, nil
}

func bindOperatorClosureVars(operator ChannelOperator, value any) (map[string]any, error) {
	if operator.ClosureExpr != nil {
		return bindClosureVars(nil, *operator.ClosureExpr, value)
	}

	return bindClosureVars(nil, ClosureExpr{Body: operator.Closure}, value)
}

func labelScopedDepGroups(depGroups []string, label string) []string {
	if len(depGroups) == 0 {
		return []string{label}
	}

	labelled := make([]string, 0, len(depGroups))
	for _, depGroup := range depGroups {
		labelled = append(labelled, depGroup+"."+label)
	}

	return labelled
}

func warnUnsupportedChannelClosure(closure string) {
	_, _ = fmt.Fprintf(os.Stderr, "nextflowdsl: closure %q could not be evaluated at compile time and will be treated as a pass-through\n", closure)
}

func resolveMultiMapChannelItems(items []channelItem, operator ChannelOperator) (namedChannelResult, error) {
	entries, err := parseNamedClosureEntries(operator)
	if err != nil {
		return namedChannelResult{}, err
	}

	result := namedChannelResult{labels: make([]string, 0, len(entries)), items: make(map[string][]channelItem, len(entries))}
	for _, entry := range entries {
		result.labels = append(result.labels, entry.label)
	}

	for _, item := range items {
		scope, err := bindOperatorClosureVars(operator, item.value)
		if err != nil {
			return namedChannelResult{}, err
		}

		for _, entry := range entries {
			if entry.isFallback {
				continue
			}

			value, evalErr := EvalExpr(entry.expr, scope)
			if evalErr != nil {
				return namedChannelResult{}, evalErr
			}

			result.items[entry.label] = append(result.items[entry.label], channelItem{
				value:     cloneChannelValue(value),
				depGroups: labelScopedDepGroups(item.depGroups, entry.label),
			})
		}
	}

	return result, nil
}

func resolveOperatorByIndex(operator ChannelOperator) (*int, error) {
	for _, arg := range operator.Args {
		value, err := EvalExpr(arg, nil)
		if err != nil {
			return nil, err
		}

		switch typed := value.(type) {
		case int:
			index := typed
			return &index, nil
		case map[string]any:
			raw, ok := typed["by"]
			if !ok {
				return nil, fmt.Errorf("%s expects a by argument", operator.Name)
			}

			index, ok := raw.(int)
			if !ok {
				return nil, fmt.Errorf("%s expects by to be an integer", operator.Name)
			}

			return &index, nil
		default:
			return nil, fmt.Errorf("%s expects by to be an integer", operator.Name)
		}
	}

	return nil, nil
}

func reduceOperatorSeedAndClosure(operator ChannelOperator) (any, bool, ClosureExpr, error) {
	switch len(operator.Args) {
	case 0:
		if operator.ClosureExpr != nil {
			return nil, false, *operator.ClosureExpr, nil
		}
		closure := strings.TrimSpace(operator.Closure)
		if closure == "" {
			return nil, false, ClosureExpr{}, fmt.Errorf("reduce expects a closure")
		}

		return nil, false, ClosureExpr{Body: closure}, nil
	case 1:
		if closure, ok := operator.Args[0].(ClosureExpr); ok {
			return nil, false, closure, nil
		}

		return nil, false, ClosureExpr{}, fmt.Errorf("reduce expects a closure")
	case 2:
		seed, err := EvalExpr(operator.Args[0], nil)
		if err != nil {
			return nil, false, ClosureExpr{}, err
		}

		closure, ok := operator.Args[1].(ClosureExpr)
		if !ok {
			return nil, false, ClosureExpr{}, fmt.Errorf("reduce expects a closure as the second argument")
		}

		return seed, true, closure, nil
	default:
		return nil, false, ClosureExpr{}, fmt.Errorf("reduce expects at most 2 arguments, got %d", len(operator.Args))
	}
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

func flattenNamedChannelItems(result namedChannelResult) []channelItem {
	flattened := []channelItem{}
	for _, label := range result.labels {
		flattened = append(flattened, cloneChannelItems(result.items[label])...)
	}

	return flattened
}

func collectFileChannelItems(items []channelItem, operator ChannelOperator, cwd string) ([]channelItem, error) {
	options, err := resolveOperatorOptions(operator)
	if err != nil {
		return nil, err
	}

	name := "collect.out"
	if raw, ok := options["name"]; ok {
		name, err = stringOption(raw, "name")
		if err != nil {
			return nil, err
		}
	}

	outputPath := name
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(cwd, outputPath)
	}

	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, itemBinding(item.value))
	}

	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}

	if err := os.WriteFile(outputPath, []byte(content), 0o600); err != nil {
		return nil, err
	}

	return []channelItem{{value: filepath.Clean(outputPath), depGroups: unionChannelDepGroups(items)}}, nil
}

func resolveOperatorOptions(operator ChannelOperator) (map[string]any, error) {
	if len(operator.Args) == 0 {
		return nil, nil
	}
	if len(operator.Args) != 1 {
		return nil, fmt.Errorf("%s expects at most one argument", operator.Name)
	}

	value, err := EvalExpr(operator.Args[0], nil)
	if err != nil {
		return nil, err
	}
	if options, ok := value.(map[string]any); ok {
		return options, nil
	}

	return map[string]any{"by": value}, nil
}

func stringOption(value any, name string) (string, error) {
	typed, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("expects %q to be a string", name)
	}

	return typed, nil
}

func combineChannelItems(left, right []channelItem, byIndex *int) ([]channelItem, error) {
	combined := make([]channelItem, 0, len(left)*len(right))
	for _, leftItem := range left {
		var leftKey any
		if byIndex != nil {
			key, err := resolveChannelKey(leftItem.value, *byIndex)
			if err != nil {
				return nil, err
			}

			leftKey = key
		}

		for _, rightItem := range right {
			if byIndex != nil {
				rightKey, err := resolveChannelKey(rightItem.value, *byIndex)
				if err != nil {
					return nil, err
				}
				if !reflect.DeepEqual(leftKey, rightKey) {
					continue
				}
			}

			combined = append(combined, channelItem{
				value:     combineChannelValues(leftItem.value, rightItem.value, byIndex),
				depGroups: appendUniqueStrings(cloneStrings(leftItem.depGroups), rightItem.depGroups),
			})
		}
	}

	return combined, nil
}

func resolveChannelKey(value any, byIndex int) (any, error) {
	tuple := channelTuple(value)
	if byIndex < 0 || byIndex >= len(tuple) {
		return nil, fmt.Errorf("by index %d out of range for %T", byIndex, value)
	}

	return cloneChannelValue(tuple[byIndex]), nil
}

func combineChannelValues(left, right any, byIndex *int) any {
	leftTuple := channelTuple(left)
	rightTuple := channelTuple(right)

	combined := make([]any, 0, len(leftTuple)+len(rightTuple))
	combined = append(combined, cloneChannelSlice(leftTuple)...)
	if byIndex == nil {
		combined = append(combined, cloneChannelSlice(rightTuple)...)
		return combined
	}

	combined = append(combined, removeChannelTupleIndex(rightTuple, *byIndex)...)

	return combined
}

func removeChannelTupleIndex(values []any, index int) []any {
	trimmed := make([]any, 0, len(values))
	for valueIndex, value := range values {
		if valueIndex == index {
			continue
		}

		trimmed = append(trimmed, cloneChannelValue(value))
	}

	return trimmed
}

func concatChannelItems(channels ...[]channelItem) []channelItem {
	concatenated := []channelItem{}
	for _, channel := range channels {
		concatenated = append(concatenated, cloneChannelItems(channel)...)
	}

	return concatenated
}

func uniqueChannelItems(items []channelItem) []channelItem {
	unique := make([]channelItem, 0, len(items))
	seen := make([]any, 0, len(items))
	for _, item := range items {
		if containsComparableValue(seen, item.value) {
			continue
		}

		seen = append(seen, cloneChannelValue(item.value))
		unique = append(unique, cloneChannelItem(item))
	}

	return unique
}

func distinctChannelItems(items []channelItem) []channelItem {
	distinct := make([]channelItem, 0, len(items))
	var previous any
	havePrevious := false
	for _, item := range items {
		if havePrevious && reflect.DeepEqual(previous, item.value) {
			continue
		}

		distinct = append(distinct, cloneChannelItem(item))
		previous = cloneChannelValue(item.value)
		havePrevious = true
	}

	return distinct
}

func sortedChannelValues(items []channelItem) ([]any, error) {
	sorted := collectChannelValues(items)
	sort.Slice(sorted, func(i, j int) bool {
		less, _ := lessSortableValue(sorted[i], sorted[j])
		return less
	})
	for index := 1; index < len(sorted); index++ {
		if _, err := lessSortableValue(sorted[index-1], sorted[index]); err != nil {
			return nil, err
		}
	}

	return sorted, nil
}

func countChannelItems(items []channelItem, operator ChannelOperator) ([]channelItem, error) {
	filterArg, hasFilter, err := channelOperatorFilterArg(operator)
	if err != nil {
		return nil, err
	}
	if !hasFilter {
		return []channelItem{{value: len(items), depGroups: unionChannelDepGroups(items)}}, nil
	}

	matched := 0
	for _, item := range items {
		keep, err := channelOperatorMatches(filterArg, item.value)
		if err != nil {
			return nil, err
		}
		if keep {
			matched++
		}
	}

	return []channelItem{{value: matched, depGroups: unionChannelDepGroups(items)}}, nil
}

func channelOperatorFilterArg(operator ChannelOperator) (Expr, bool, error) {
	switch {
	case len(operator.Args) > 1:
		return nil, false, fmt.Errorf("count expects at most 1 argument, got %d", len(operator.Args))
	case len(operator.Args) == 1:
		return operator.Args[0], true, nil
	case operator.ClosureExpr != nil:
		return *operator.ClosureExpr, true, nil
	case strings.TrimSpace(operator.Closure) != "":
		return ClosureExpr{Body: operator.Closure}, true, nil
	default:
		return nil, false, nil
	}
}

func channelOperatorMatches(filter Expr, value any) (bool, error) {
	if closure, ok := filter.(ClosureExpr); ok {
		resolved, err := evalSimpleClosure(closure, value, nil)
		if err != nil {
			return false, err
		}

		return isTruthy(resolved), nil
	}

	resolved, err := EvalExpr(filter, nil)
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(value, resolved), nil
}

func transposeChannelItems(items []channelItem, byIndex *int) ([]channelItem, error) {
	transposed := []channelItem{}
	for _, item := range items {
		rows, err := transposeChannelItem(item, byIndex)
		if err != nil {
			return nil, err
		}

		transposed = append(transposed, rows...)
	}

	return transposed, nil
}

func transposeChannelItem(item channelItem, byIndex *int) ([]channelItem, error) {
	tuple := channelTuple(item.value)
	indices := []int{}
	if byIndex != nil {
		indices = append(indices, *byIndex)
	} else {
		for index, value := range tuple {
			switch value.(type) {
			case []any, []string:
				indices = append(indices, index)
			}
		}
	}

	if len(indices) == 0 {
		return []channelItem{cloneChannelItem(item)}, nil
	}

	rows := [][]any{cloneChannelSlice(tuple)}
	for _, index := range indices {
		expanded := [][]any{}
		for _, row := range rows {
			if index < 0 || index >= len(row) {
				return nil, fmt.Errorf("transpose index %d out of range", index)
			}

			values := flattenChannelValues(row[index])
			if len(values) == 0 {
				continue
			}

			for _, value := range values {
				candidate := cloneChannelSlice(row)
				candidate[index] = cloneChannelValue(value)
				expanded = append(expanded, candidate)
			}
		}
		rows = expanded
	}

	result := make([]channelItem, 0, len(rows))
	for _, row := range rows {
		result = append(result, channelItem{value: row, depGroups: cloneStrings(item.depGroups)})
	}

	return result, nil
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

func splitCSVChannelItems(items []channelItem, operator ChannelOperator) ([]channelItem, error) {
	options, err := resolveOperatorOptions(operator)
	if err != nil {
		return nil, err
	}

	header := false
	if raw, ok := options["header"]; ok {
		header, err = boolOption(raw, "header")
		if err != nil {
			return nil, err
		}
	}

	by := 1
	if raw, ok := options["by"]; ok {
		by, err = intOption(raw, "by")
		if err != nil {
			return nil, err
		}
	}

	result := []channelItem{}
	for _, item := range items {
		paths, err := channelItemPaths(item.value)
		if err != nil {
			return nil, err
		}

		for _, path := range paths {
			rows, err := splitCSVFile(path, header, by)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				result = append(result, channelItem{value: row, depGroups: cloneStrings(item.depGroups)})
			}
		}
	}

	return result, nil
}

func boolOption(value any, name string) (bool, error) {
	typed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("expects %q to be a boolean", name)
	}

	return typed, nil
}

func intOption(value any, name string) (int, error) {
	typed, ok := value.(int)
	if !ok {
		return 0, fmt.Errorf("expects %q to be an integer", name)
	}
	if typed <= 0 {
		return 0, fmt.Errorf("expects %q to be positive", name)
	}

	return typed, nil
}

func channelItemPaths(value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return []string{typed}, nil
	case []string:
		return cloneStrings(typed), nil
	case []any:
		paths := make([]string, 0, len(typed))
		for _, element := range typed {
			path, ok := element.(string)
			if !ok {
				return nil, fmt.Errorf("expected file path item, got %T", element)
			}
			paths = append(paths, path)
		}

		return paths, nil
	default:
		return nil, fmt.Errorf("expected file path item, got %T", value)
	}
}

func splitCSVFile(path string, header bool, by int) ([]any, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}

	data := records
	headers := []string{}
	if header {
		headers = cloneStrings(records[0])
		data = records[1:]
	}

	rows := make([]any, 0, len(data))
	for _, record := range data {
		if header {
			row := make(map[string]any, len(headers))
			for index, name := range headers {
				if index < len(record) {
					row[name] = record[index]
				} else {
					row[name] = ""
				}
			}
			rows = append(rows, row)
			continue
		}

		values := make([]any, 0, len(record))
		for _, field := range record {
			values = append(values, field)
		}
		rows = append(rows, values)
	}

	return chunkSplitValues(rows, by), nil
}

func chunkSplitValues(values []any, by int) []any {
	if by <= 1 {
		return values
	}

	chunked := make([]any, 0, (len(values)+by-1)/by)
	for start := 0; start < len(values); start += by {
		end := start + by
		if end > len(values) {
			end = len(values)
		}
		chunk := make([]any, 0, end-start)
		for _, value := range values[start:end] {
			chunk = append(chunk, cloneJSONValue(value))
		}
		chunked = append(chunked, chunk)
	}

	return chunked
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case []any:
		cloned := make([]any, 0, len(typed))
		for _, element := range typed {
			cloned = append(cloned, cloneJSONValue(element))
		}

		return cloned
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, element := range typed {
			cloned[key] = cloneJSONValue(element)
		}

		return cloned
	default:
		return typed
	}
}

func splitFASTAChannelItems(items []channelItem, operator ChannelOperator) ([]channelItem, error) {
	options, err := resolveOperatorOptions(operator)
	if err != nil {
		return nil, err
	}

	by := 1
	if raw, ok := options["by"]; ok {
		by, err = intOption(raw, "by")
		if err != nil {
			return nil, err
		}
	}

	result := []channelItem{}
	for _, item := range items {
		paths, err := channelItemPaths(item.value)
		if err != nil {
			return nil, err
		}

		for _, path := range paths {
			chunks, err := splitFASTAFile(path, by)
			if err != nil {
				return nil, err
			}
			for _, chunk := range chunks {
				result = append(result, channelItem{value: chunk, depGroups: cloneStrings(item.depGroups)})
			}
		}
	}

	return result, nil
}

func splitFASTAFile(path string, by int) ([]string, error) {
	records, err := splitStructuredRecords(path, func(line string) bool {
		return strings.HasPrefix(line, ">")
	})
	if err != nil {
		return nil, err
	}

	return chunkStrings(records, by), nil
}

func splitStructuredRecords(path string, startsRecord func(string) bool) ([]string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	records := []string{}
	current := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		if startsRecord(line) && len(current) > 0 {
			records = append(records, strings.Join(current, "\n"))
			current = nil
		}
		current = append(current, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(current) > 0 {
		records = append(records, strings.Join(current, "\n"))
	}

	return records, nil
}

func chunkStrings(values []string, by int) []string {
	if len(values) == 0 {
		return nil
	}

	chunks := make([]string, 0, (len(values)+by-1)/by)
	for start := 0; start < len(values); start += by {
		end := start + by
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, strings.Join(values[start:end], "\n"))
	}

	return chunks
}

func splitFASTQChannelItems(items []channelItem, operator ChannelOperator) ([]channelItem, error) {
	options, err := resolveOperatorOptions(operator)
	if err != nil {
		return nil, err
	}

	by := 1
	if raw, ok := options["by"]; ok {
		by, err = intOption(raw, "by")
		if err != nil {
			return nil, err
		}
	}

	pairedEnd := false
	if raw, ok := options["pe"]; ok {
		pairedEnd, err = boolOption(raw, "pe")
		if err != nil {
			return nil, err
		}
	}

	result := []channelItem{}
	for _, item := range items {
		chunks, err := splitFASTQValue(item.value, by, pairedEnd)
		if err != nil {
			return nil, err
		}
		for _, chunk := range chunks {
			result = append(result, channelItem{value: chunk, depGroups: cloneStrings(item.depGroups)})
		}
	}

	return result, nil
}

func splitFASTQValue(value any, by int, pairedEnd bool) ([]any, error) {
	paths, err := channelItemPaths(value)
	if err != nil {
		return nil, err
	}

	if pairedEnd {
		if len(paths) != 2 {
			return nil, fmt.Errorf("splitFastq with pe:true expects exactly 2 files")
		}

		left, err := splitFASTQFile(paths[0])
		if err != nil {
			return nil, err
		}
		right, err := splitFASTQFile(paths[1])
		if err != nil {
			return nil, err
		}
		if len(left) != len(right) {
			return nil, fmt.Errorf("splitFastq paired files contain different read counts")
		}

		pairs := make([]any, 0, len(left))
		for index := range left {
			pairs = append(pairs, []any{left[index], right[index]})
		}

		return chunkSplitValues(pairs, by), nil
	}

	result := []any{}
	for _, path := range paths {
		records, err := splitFASTQFile(path)
		if err != nil {
			return nil, err
		}
		for _, chunk := range chunkStrings(records, by) {
			result = append(result, chunk)
		}
	}

	return result, nil
}

func splitFASTQFile(path string) ([]string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	records := []string{}
	for {
		lines := make([]string, 0, 4)
		for range 4 {
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			line = strings.TrimRight(line, "\r\n")
			if line != "" || !errors.Is(err, io.EOF) {
				lines = append(lines, line)
			}
			if errors.Is(err, io.EOF) {
				break
			}
		}
		if len(lines) == 0 {
			break
		}
		if len(lines) != 4 {
			return nil, fmt.Errorf("invalid FASTQ record in %s", path)
		}
		records = append(records, strings.Join(lines, "\n"))
	}

	return records, nil
}

func splitJSONChannelItems(items []channelItem, operator ChannelOperator) ([]channelItem, error) {
	options, err := resolveOperatorOptions(operator)
	if err != nil {
		return nil, err
	}

	valuePath := ""
	if raw, ok := options["path"]; ok {
		valuePath, err = stringOption(raw, "path")
		if err != nil {
			return nil, err
		}
	}

	result := []channelItem{}
	for _, item := range items {
		paths, err := channelItemPaths(item.value)
		if err != nil {
			return nil, err
		}

		for _, path := range paths {
			values, err := splitJSONFile(path, valuePath)
			if err != nil {
				return nil, err
			}
			for _, value := range values {
				result = append(result, channelItem{value: value, depGroups: cloneStrings(item.depGroups)})
			}
		}
	}

	return result, nil
}

func splitJSONFile(path, valuePath string) ([]any, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}

	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}

	selected, err := selectJSONPath(decoded, valuePath)
	if err != nil {
		return nil, err
	}

	if values, ok := selected.([]any); ok {
		result := make([]any, 0, len(values))
		for _, value := range values {
			result = append(result, cloneJSONValue(value))
		}

		return result, nil
	}

	return []any{cloneJSONValue(selected)}, nil
}

func selectJSONPath(value any, valuePath string) (any, error) {
	if valuePath == "" {
		return value, nil
	}

	current := value
	for _, part := range strings.Split(valuePath, ".") {
		if part == "" {
			continue
		}

		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("json path %q not found", valuePath)
		}

		next, exists := object[part]
		if !exists {
			return nil, fmt.Errorf("json path %q not found", valuePath)
		}
		current = next
	}

	return current, nil
}

func splitTextChannelItems(items []channelItem, operator ChannelOperator) ([]channelItem, error) {
	options, err := resolveOperatorOptions(operator)
	if err != nil {
		return nil, err
	}

	by := 1
	if raw, ok := options["by"]; ok {
		by, err = intOption(raw, "by")
		if err != nil {
			return nil, err
		}
	}

	result := []channelItem{}
	for _, item := range items {
		paths, err := channelItemPaths(item.value)
		if err != nil {
			return nil, err
		}

		for _, path := range paths {
			chunks, err := splitTextFile(path, by)
			if err != nil {
				return nil, err
			}
			for _, chunk := range chunks {
				result = append(result, channelItem{value: chunk, depGroups: cloneStrings(item.depGroups)})
			}
		}
	}

	return result, nil
}

func splitTextFile(path string, by int) ([]string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	chunks := make([]string, 0, (len(lines)+by-1)/by)
	for start := 0; start < len(lines); start += by {
		end := start + by
		if end > len(lines) {
			end = len(lines)
		}
		chunks = append(chunks, strings.Join(lines[start:end], "\n"))
	}

	return chunks, nil
}

type namedChannelResult struct {
	labels []string
	items  map[string][]channelItem
}

type namedClosureEntry struct {
	label      string
	expr       Expr
	isFallback bool
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
	case NamedChannelRef:
		items, err := resolveChannelItems(expr.Source, cwd, resolver)
		if err != nil {
			return nil, err
		}

		return selectNamedChannelItems(items, expr.Label), nil
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
	case "branch":
		result, err := resolveBranchChannelItems(items, operator)
		if err != nil {
			return nil, err
		}

		return flattenNamedChannelItems(result), nil
	case "collect":
		return []channelItem{{value: collectChannelValues(items), depGroups: unionChannelDepGroups(items)}}, nil
	case "collectFile":
		return collectFileChannelItems(items, operator, cwd)
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
	case "multiMap":
		result, err := resolveMultiMapChannelItems(items, operator)
		if err != nil {
			return nil, err
		}

		return flattenNamedChannelItems(result), nil
	case "combine":
		combined := cloneChannelItems(items)
		byIndex, err := resolveOperatorByIndex(operator)
		if err != nil {
			return nil, err
		}
		for _, other := range operator.Channels {
			resolved, err := resolveChannelItems(other, cwd, resolver)
			if err != nil {
				return nil, err
			}

			combined, err = combineChannelItems(combined, resolved, byIndex)
			if err != nil {
				return nil, err
			}
		}

		return combined, nil
	case "concat":
		concatenated := cloneChannelItems(items)
		for _, other := range operator.Channels {
			resolved, err := resolveChannelItems(other, cwd, resolver)
			if err != nil {
				return nil, err
			}

			concatenated = concatChannelItems(concatenated, resolved)
		}

		return concatenated, nil
	case "flatten":
		flattened := []channelItem{}
		for _, item := range items {
			for _, value := range flattenSliceValue(item.value) {
				flattened = append(flattened, channelItem{value: value, depGroups: cloneStrings(item.depGroups)})
			}
		}

		return flattened, nil
	case "unique":
		return uniqueChannelItems(items), nil
	case "distinct":
		return distinctChannelItems(items), nil
	case "ifEmpty":
		if len(items) != 0 {
			return cloneChannelItems(items), nil
		}
		if len(operator.Args) != 1 {
			return nil, fmt.Errorf("ifEmpty expects 1 argument, got %d", len(operator.Args))
		}

		value, err := EvalExpr(operator.Args[0], nil)
		if err != nil {
			return nil, err
		}

		return []channelItem{{value: value}}, nil
	case "toList":
		return []channelItem{{value: collectChannelValues(items), depGroups: unionChannelDepGroups(items)}}, nil
	case "toSortedList":
		sorted, err := sortedChannelValues(items)
		if err != nil {
			return nil, err
		}

		return []channelItem{{value: sorted, depGroups: unionChannelDepGroups(items)}}, nil
	case "count":
		return countChannelItems(items, operator)
	case "reduce":
		seed, hasSeed, closure, err := reduceOperatorSeedAndClosure(operator)
		if err != nil {
			return nil, err
		}
		if hasSeed {
			items = append([]channelItem{{value: seed}}, items...)
		}

		return reduceChannelItems(items, func(current, candidate any) (any, error) {
			return evalChannelClosure(ChannelOperator{ClosureExpr: &closure}, []any{current, candidate})
		})
	case "transpose":
		byIndex, err := resolveOperatorByIndex(operator)
		if err != nil {
			return nil, err
		}

		return transposeChannelItems(items, byIndex)
	case "toLong", "toFloat", "toDouble":
		return cloneChannelItems(items), nil
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
	case "splitCsv":
		return splitCSVChannelItems(items, operator)
	case "splitFasta":
		return splitFASTAChannelItems(items, operator)
	case "splitFastq":
		return splitFASTQChannelItems(items, operator)
	case "splitJson":
		return splitJSONChannelItems(items, operator)
	case "splitText":
		return splitTextChannelItems(items, operator)
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
	case orderedMap:
		return newOrderedMap(typed.entries)
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
