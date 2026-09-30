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

package nextflowconformance

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"unicode"
)

const (
	schemaAnyOf      = "anyOf"
	schemaAllOf      = "allOf"
	schemaTypeKey    = "type"
	schemaString     = "string"
	schemaInteger    = "integer"
	schemaProperties = "properties"
	schemaConst      = "const"
	schemaPattern    = "pattern"
	schemaEnum       = "enum"
	schemaThen       = "then"
	schemaElse       = "else"
	schemaIf         = "if"
	schemaNull       = "null"
	schemaMinimum    = "minimum"
	schemaMaximum    = "maximum"
	schemaItems      = "items"
	schemaMinItems   = "minItems"
	schemaMinLength  = "minLength"
	schemaUnique     = "uniqueItems"
	schemaNot        = "not"
)

func recordSchema(kind string) ([]byte, error) {
	record := newRecord(kind)
	if record == nil {
		return nil, fmt.Errorf("%w: unknown schema %s", errRecord, kind)
	}

	schema := schemaType(reflect.TypeOf(record).Elem())
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	schema["$id"] = "urn:wr:nextflow-conformance:schema:1:" + kind + ".json"
	schema["title"] = kind + " version 1"
	// Stock JSON Schema checks local shape. These annotations describe additional
	// decoder/corpus checks; annotations do not enforce ordering or relationships.
	schema["x-wr-record-rules"] = recordConstraints(kind)
	data, err := json.MarshalIndent(schema, "", "  ")

	return append(data, '\n'), err
}

func schemaType(typ reflect.Type) map[string]any {
	if typ.Kind() == reflect.Pointer {
		return map[string]any{schemaAnyOf: []any{schemaType(typ.Elem()), map[string]any{schemaTypeKey: schemaNull}}}
	}

	switch typ.Kind() {
	case reflect.Struct:
		return schemaObject(typ)
	case reflect.Slice:
		return map[string]any{schemaTypeKey: "array", schemaItems: schemaType(typ.Elem())}
	case reflect.String:
		return map[string]any{schemaTypeKey: schemaString}
	case reflect.Bool:
		return map[string]any{schemaTypeKey: "boolean"}
	case reflect.Uint32:
		return map[string]any{schemaTypeKey: schemaInteger, schemaMinimum: 0, schemaMaximum: uint64(math.MaxUint32)}
	default:
		return map[string]any{schemaTypeKey: schemaInteger, schemaMinimum: int64(math.MinInt64),
			schemaMaximum: int64(math.MaxInt64)}
	}
}

func schemaObject(typ reflect.Type) map[string]any {
	properties := map[string]any{}

	required := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := field.Tag.Get("json")
		child := schemaType(field.Type)
		schemaRules(child, field.Tag.Get("rule"))

		if reference := field.Tag.Get("ref"); reference != "" {
			child["x-wr-reference"] = reference
		}

		properties[name] = child
		required = append(required, name)
	}

	schema := map[string]any{schemaTypeKey: "object", schemaProperties: properties, "required": required,
		"additionalProperties": false}
	if conditions := schemaConditions(typ); len(conditions) > 0 {
		schema[schemaAllOf] = conditions
	}

	return schema
}

func schemaRules(schema map[string]any, rules string) {
	if rules == "" {
		return
	}

	if branches, ok := schema[schemaAnyOf].([]any); ok {
		if value, isObject := branches[0].(map[string]any); isObject {
			schemaRules(value, rules)
		}

		return
	}

	schema["x-wr-field-rules"] = strings.Split(rules, ",")
	for rule := range strings.SplitSeq(rules, ",") {
		applySchemaRule(schema, rule)
	}
}

func applySchemaRule(schema map[string]any, rule string) {
	if pattern, ok := rulePatterns()[rule]; ok {
		schema[schemaPattern] = schemaFullPattern(pattern)

		return
	}

	if values, ok := ruleEnums()[rule]; ok {
		schema[schemaEnum] = values

		return
	}

	if strings.HasPrefix(rule, "enum:") {
		schema[schemaEnum] = strings.Split(strings.TrimPrefix(rule, "enum:"), "|")

		return
	}

	if values, exists := scalarSchemaRules()[rule]; exists {
		for key, value := range values {
			schema[key] = value
		}

		return
	}

	applyArraySchemaRule(schema, rule)
}

func schemaConditions(typ reflect.Type) []any {
	conditions := map[reflect.Type][]any{
		reflect.TypeFor[uatRecord](): {conditionalNulls("readiness", readinessDraft,
			[]string{"fixture", "expected", "binding", reviewField}, true)},
		reflect.TypeFor[decisionRecord](): {conditionalNulls("state", decisionUnresolved,
			[]string{"resolution", reviewField}, true)},
		reflect.TypeFor[obligationRecord](): {conditionalNulls("disposition", dispositionNonrequirement,
			[]string{"rationale", reviewField}, false)},
		reflect.TypeFor[sourceFile](): {conditionalNulls("state", sourceNonsemantic,
			[]string{"rationale", reviewField}, false)},
		reflect.TypeFor[requirementRecord](): {when("scope", scopeExcluded, property("decision_ids",
			map[string]any{schemaMinItems: 1}))},
		reflect.TypeFor[includeRef](): {map[string]any{"oneOf": []any{
			property("block_id", map[string]any{schemaTypeKey: schemaString}), property("external_id",
				map[string]any{schemaTypeKey: schemaString}),
		}}},
		reflect.TypeFor[observation]():  observationConditions(),
		reflect.TypeFor[artefact]():     artefactConditions(),
		reflect.TypeFor[batchCommand](): batchConditions(),
	}

	return conditions[typ]
}

func conditionalNulls(discriminant, state string, fields []string, nullable bool) map[string]any {
	properties := map[string]any{}
	opposite := map[string]any{}

	for _, name := range fields {
		null := map[string]any{schemaTypeKey: schemaNull}
		nonnull := map[string]any{schemaNot: null}

		properties[name] = nonnull
		if nullable {
			properties[name] = null
			opposite[name] = nonnull
		}
	}

	result := when(discriminant, state, map[string]any{schemaProperties: properties})
	if nullable {
		result[schemaElse] = map[string]any{schemaProperties: opposite}
	}

	return result
}

func recordConstraints(kind string) []string {
	common := []string{
		"decodeRecord: UTF-8; final newline; no duplicate keys; no trailing value",
		"loadRecordFile/checkNested: sorted unique IDs and unique paths, environment keys and result UAT IDs",
		"checkRules: sorted unique identifier/text/index arrays; integer JSON lexical form",
		"checkReferences: typed references resolve within the same target revision",
		"checkSourceSpans: locked source file, matching hash, ordered span within byte bounds",
	}
	extra := map[string][]string{
		kindTarget: {"corpusIdentity: exactly one target and lock matching pinned version/tag/commit/tree"},
		kindLock: {"checkLock: one opaque runtime; actual acyclic dependencies; matching coordinate metadata",
			"checkArtefactOrigin: local origin digest equals file.sha256 (decoder relational check, not stock schema)",
			"checkSourceReviews: nonsemantic sources require accepted independent review"},
		kindReviews: {"reviewRecord.validate: author and reviewer differ",
			"reviewRecord.checkRelations: reviewed source files match locked path, hash and byte count"},
		kindDecisions:    {"checkRecordRelations: resolved decision requires accepted review"},
		kindRequirements: {"checkExclusion: resolved decision names affected requirement and accepted review"},
		kindUATs: {"checkUAT: cases use declared facets",
			"checkUATRelations: ready binding matches UAT and evidence kind"},
		kindBindings: {"checkBindingRelation: evidence kind matches referenced UAT"},
		kindBlocks:   {"checkBlockIncludes: include resolves relative to source and matches locked destination"},
		kindAttempts: {"attemptRecord.validate: ended at or after started",
			"loadAttempts: filename matches ID; D2 derives results later"},
	}

	return slices.Concat(common, extra[kind])
}

func descriptionStandInPattern() string {
	space := "[" + descriptionSpaceCharacters + "]"

	return "(^" + space + "*|[.!?:]" + space + "+|`[.!?:]?" + space + "*)" +
		caseInsensitive("TODO: describe expected behaviour.") + "(" + space + "|$)"
}

func placeholderPattern() string {
	words := []string{caseInsensitive("TODO"), caseInsensitive("TBD"), caseInsensitive("placeholder")}

	space := "[" + descriptionSpaceCharacters + "]"

	return "^" + space + "*(?:" + strings.Join(words, "|") + ")" + space + "*$"
}

func schemaDescriptionStandInPattern() string {
	space := "[" + descriptionSpaceCharacters + "]"
	quoted := "(?:" + descriptionQuotedPattern() + ")"
	// The lookahead prevents backtracking into a quoted span. Go masks those same
	// spans before applying the boundary guard because RE2 has no lookahead.
	prefix := "(?:" + quoted + "|(?!" + quoted + `)[\s\S])*`
	heading := "`(?:[^`\\\\]|\\\\[\\s\\S])*`[.!?:]?" + space + "*"
	unmatched := "(?!" + quoted + ")`[.!?:]?" + space + "*"

	return "^(?:" + space + "*|" + prefix + "(?:[.!?:]" + space + "+|" + heading + "|" + unmatched + "))" +
		caseInsensitive("TODO: describe expected behaviour.") + "(" + space + "|$)"
}

func descriptionQuotedPattern() string {
	// Single quotes need an opening boundary so contractions are ordinary prose.
	// Complete pairs alone are literals; an unmatched delimiter grants no exemption.
	boundary := "(^|[" + descriptionSpaceCharacters + `!#$%&()*+,\-./:;<=>?@\[\]^_{|}~])`

	return `\\[\s\S]|"(?:[^"\\]|\\[\s\S])*"|` + "`(?:[^`\\\\]|\\\\[\\s\\S])*`|" +
		boundary + `'(?:[^'\\]|\\[\s\S])*'`
}

func caseInsensitive(text string) string {
	var pattern strings.Builder

	for _, char := range text {
		switch {
		case unicode.IsLetter(char):
			letter := unicode.ToUpper(char)
			pattern.WriteString("[" + string(letter))
			// Explicit folds preserve Go's case-insensitive matching in stock schemas.
			for folded := unicode.SimpleFold(letter); folded != letter; folded = unicode.SimpleFold(folded) {
				pattern.WriteRune(folded)
			}

			pattern.WriteByte(']')
		case char == '.':
			pattern.WriteString(`\.`)
		default:
			pattern.WriteRune(char)
		}
	}

	return pattern.String()
}

func observationConditions() []any {
	paths := map[string]any{schemaEnum: []string{packFile, "json"}}
	condition := map[string]any{
		schemaIf: property("kind", paths), schemaThen: property("path", map[string]any{schemaTypeKey: schemaString}),
		schemaElse: property("path", map[string]any{schemaTypeKey: schemaNull}),
	}

	return []any{condition, when("operator", "sha256", property("value",
		map[string]any{schemaPattern: schemaFullPattern(rulePatterns()["hash"])}))}
}

func artefactConditions() []any {
	metadata := map[string]any{schemaProperties: map[string]any{
		"packaging": map[string]any{schemaConst: packFile}, packFile: property("path",
			map[string]any{schemaPattern: schemaFullPattern(`\.pom$`)}),
		"coordinate": map[string]any{schemaTypeKey: schemaString}, "dependencies": map[string]any{"maxItems": 0},
	}}
	runtime := map[string]any{schemaProperties: map[string]any{
		"packaging": map[string]any{schemaConst: packOpaque},
		packFile: map[string]any{schemaProperties: map[string]any{
			"sha256": map[string]any{schemaConst: distributionHash},
			"bytes":  map[string]any{schemaConst: distributionBytes},
		}},
	}}

	return []any{
		when("role", roleMetadata, metadata), when("role", roleRuntime, runtime),
		when("packaging", packOpaque, property("role", map[string]any{schemaConst: roleRuntime})),
		map[string]any{schemaIf: property(packFile, property("path",
			map[string]any{schemaPattern: schemaFullPattern(`\.pom$`)})),
			schemaThen: property("role", map[string]any{schemaConst: roleMetadata})},
		coordinateCondition(),
		map[string]any{schemaIf: property("origin", map[string]any{schemaPattern: schemaFullPattern(localOriginPattern)}),
			schemaThen: map[string]any{schemaProperties: map[string]any{
				roleField:      map[string]any{schemaEnum: []string{roleJava, roleEnvironment}},
				packagingField: map[string]any{schemaConst: packFile}, "coordinate": map[string]any{schemaTypeKey: schemaNull},
			}}},
	}
}

func batchConditions() []any {
	suites := map[string]any{
		schemaIf: property("command", map[string]any{schemaEnum: []string{commandDiscover, commandRun,
			commandVerify}}),
		schemaThen: property(suiteField, map[string]any{schemaTypeKey: schemaString}),
		schemaElse: property(suiteField, map[string]any{schemaTypeKey: schemaNull}),
	}
	checks := map[string]any{schemaIf: property("check", map[string]any{schemaConst: true}),
		schemaThen: property("command", map[string]any{schemaEnum: []string{commandExtract, commandRender}})}

	return []any{suites, checks}
}

func scalarSchemaRules() map[string]map[string]any {
	return map[string]map[string]any{
		textRule: textSchema(), "one": {schemaConst: 1}, "positive": {schemaMinimum: 1},
		"nonnegative":  {schemaMinimum: 0},
		"path":         {schemaMinLength: 1, schemaPattern: pathPattern()},
		"include-path": {schemaMinLength: 1, schemaPattern: includePathPattern()},
		httpsRule:      {schemaPattern: schemaFullPattern(httpsPattern())},
		artefactOriginRule: {"anyOf": []any{
			map[string]any{schemaPattern: schemaFullPattern(httpsPattern())},
			map[string]any{schemaPattern: schemaFullPattern(localOriginPattern)},
		}},
		"time": {schemaPattern: schemaFullPattern(timePattern())},
		"package": {schemaAllOf: []any{
			map[string]any{schemaPattern: `^github\.com/VertebrateResequencing/wr(?:/|(?![\s\S]))`},
			map[string]any{schemaPattern: pathPattern()},
		}},
	}
}

func applyArraySchemaRule(schema map[string]any, rule string) {
	switch rule {
	case "nonempty":
		schema[schemaMinItems] = 1
	case "ids":
		schema[schemaUnique] = true
		schema[schemaItems] = map[string]any{schemaTypeKey: schemaString,
			schemaPattern: schemaFullPattern(rulePatterns()["id"])}
	case "texts":
		schema[schemaUnique] = true
		schema[schemaItems] = textSchema()
	case "indexes":
		schema[schemaUnique] = true
		schema[schemaItems] = map[string]any{schemaTypeKey: schemaInteger, schemaMinimum: 0,
			schemaMaximum: int64(math.MaxInt64)}
	default:
	}
}

func textSchema() map[string]any {
	return map[string]any{
		schemaMinLength: 1, schemaPattern: "[^" + descriptionSpaceCharacters + "]",
		schemaNot: map[string]any{schemaAnyOf: []any{
			map[string]any{schemaPattern: placeholderPattern()},
			map[string]any{schemaPattern: schemaDescriptionStandInPattern()},
		}},
	}
}

func pathPattern() string {
	segment := pathSegmentPattern()

	return schemaFullPattern(`^` + segment + `(?:/` + segment + `)*$`)
}

func includePathPattern() string {
	segment := pathSegmentPattern()

	return schemaFullPattern(`^(?:\.\./)*(?:\.\.|` + segment + `(?:/` + segment + `)*)$`)
}

func pathSegmentPattern() string {
	return `(?!\.{1,2}(?:/|(?![\s\S])))[^/\\\u0000]+`
}

func when(field, value string, then map[string]any) map[string]any {
	return map[string]any{schemaIf: property(field, map[string]any{schemaConst: value}), schemaThen: then}
}

func coordinateCondition() map[string]any {
	maven := map[string]any{schemaAnyOf: []any{
		property("role", map[string]any{schemaConst: roleMetadata}),
		map[string]any{schemaProperties: map[string]any{
			"role": map[string]any{schemaConst: roleSource}, packFile: property("path",
				map[string]any{schemaPattern: schemaFullPattern(`-sources\.jar$`)}),
		}},
	}}

	return map[string]any{schemaIf: maven, schemaThen: property("coordinate",
		map[string]any{schemaTypeKey: schemaString}),
		schemaElse: property("coordinate", map[string]any{schemaTypeKey: schemaNull})}
}

func property(name string, constraint map[string]any) map[string]any {
	return map[string]any{schemaProperties: map[string]any{name: constraint}}
}

func schemaFullPattern(pattern string) string {
	// Unlike Go, ECMAScript and Python allow $ immediately before a final newline.
	return strings.TrimSuffix(pattern, "$") + `(?![\s\S])`
}
