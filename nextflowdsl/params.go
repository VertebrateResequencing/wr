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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var interpolatedParamsRE = regexp.MustCompile(`\$\{(params(?:\.[A-Za-z_][A-Za-z0-9_]*)+)\}`)

var bareParamsRE = regexp.MustCompile(`(^|[^[:alnum:]_])(params(?:\.[A-Za-z_][A-Za-z0-9_]*)+)`)

// MergeParams merges config params, file params, and CLI params.
func MergeParams(sources ...map[string]any) map[string]any {
	merged := make(map[string]any)

	for _, source := range sources {
		for key, value := range source {
			merged[key] = mergeParamValue(merged[key], value)
		}
	}

	return merged
}

func mergeParamValue(existing, incoming any) any {
	existingMap, existingOK := existing.(map[string]any)
	incomingMap, incomingOK := incoming.(map[string]any)
	if !existingOK || !incomingOK {
		return incoming
	}

	merged := make(map[string]any, len(existingMap))
	for key, value := range existingMap {
		merged[key] = normalizeParamValue(value)
	}
	for key, value := range incomingMap {
		merged[key] = mergeParamValue(merged[key], value)
	}

	return merged
}

// LoadParams reads a JSON or YAML params file.
func LoadParams(path string) (map[string]any, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parser := detectParamsFormat(path, content)
	params, err := parser(content)
	if err != nil {
		return nil, fmt.Errorf("load params %s: %w", path, err)
	}

	return params, nil
}

func detectParamsFormat(path string, content []byte) func([]byte) (map[string]any, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return parseJSONParams
	case ".yaml", ".yml":
		return parseYAMLParams
	default:
		trimmed := bytes.TrimSpace(content)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return parseJSONParams
		}

		return parseYAMLParams
	}
}

// SubstituteParams replaces params.KEY references in s with values from params.
func SubstituteParams(s string, params map[string]any) (string, error) {
	interpolatedMatches := interpolatedParamsRE.FindAllStringSubmatchIndex(s, -1)
	if len(interpolatedMatches) == 0 {
		return replaceParamsMatches(s, bareParamsRE, params, func(match string, groups []string) string {
			return groups[1]
		})
	}

	var builder strings.Builder
	last := 0

	for _, match := range interpolatedMatches {
		prefix, err := replaceParamsMatches(s[last:match[0]], bareParamsRE, params, func(match string, groups []string) string {
			return groups[1]
		})
		if err != nil {
			return "", err
		}

		builder.WriteString(prefix)

		value, err := resolveParamReference(s[match[2]:match[3]], params)
		if err != nil {
			return "", err
		}

		builder.WriteString(fmt.Sprint(value))
		last = match[1]
	}

	suffix, err := replaceParamsMatches(s[last:], bareParamsRE, params, func(match string, groups []string) string {
		return groups[1]
	})
	if err != nil {
		return "", err
	}

	builder.WriteString(suffix)

	return builder.String(), nil
}

func replaceParamsMatches(s string, pattern *regexp.Regexp, params map[string]any, reference func(string, []string) string) (string, error) {
	matches := pattern.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}

	var builder strings.Builder
	last := 0

	for _, match := range matches {
		builder.WriteString(s[last:match[0]])

		groups := make([]string, 0, (len(match)-2)/2)
		for index := 2; index < len(match); index += 2 {
			groups = append(groups, s[match[index]:match[index+1]])
		}

		ref := reference(s[match[0]:match[1]], groups)
		value, err := resolveParamReference(ref, params)
		if err != nil {
			return "", err
		}

		if len(groups) > 1 {
			builder.WriteString(groups[0])
		}
		builder.WriteString(fmt.Sprint(value))
		last = match[1]
	}

	builder.WriteString(s[last:])

	return builder.String(), nil
}

func resolveParamReference(reference string, params map[string]any) (any, error) {
	path, ok := strings.CutPrefix(reference, "params.")
	if !ok {
		return nil, fmt.Errorf("invalid params reference %q", reference)
	}

	current := any(params)
	for _, part := range strings.Split(path, ".") {
		currentMap, mapOK := current.(map[string]any)
		if !mapOK {
			return nil, fmt.Errorf("missing parameter %s", reference)
		}

		current, mapOK = currentMap[part]
		if !mapOK {
			return nil, fmt.Errorf("missing parameter %s", reference)
		}
	}

	return current, nil
}

func parseJSONParams(content []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()

	var params map[string]any
	if err := decoder.Decode(&params); err != nil {
		return nil, err
	}

	return normalizeParamsMap(params), nil
}

func parseYAMLParams(content []byte) (map[string]any, error) {
	var params map[string]any
	if err := yaml.Unmarshal(content, &params); err != nil {
		return nil, err
	}

	return normalizeParamsMap(params), nil
}

func normalizeParamsMap(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}

	normalized := make(map[string]any, len(params))
	for key, value := range params {
		normalized[key] = normalizeParamValue(value)
	}

	return normalized
}

func normalizeParamValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return normalizeParamsMap(typed)
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeParamValue(item)
		}

		return normalized
	case json.Number:
		if intValue, err := strconv.Atoi(typed.String()); err == nil {
			return intValue
		}

		floatValue, err := typed.Float64()
		if err != nil {
			return typed.String()
		}

		return floatValue
	default:
		return typed
	}
}
