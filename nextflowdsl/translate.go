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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	"github.com/VertebrateResequencing/wr/jobqueue/scheduler"
)

const (
	defaultCPUs   = 1
	defaultMemory = 128
	defaultTime   = 60
	defaultDisk   = 1
	nfStdoutFile  = ".nf-stdout"
	nfStderrFile  = ".nf-stderr"
)

const (
	selectorSpecificityLabel = iota + 1
	selectorSpecificityName
)

var shellSectionInterpolationPattern = regexp.MustCompile(`!\{([^}]+)\}`)

var acceleratorDirectiveTextPattern = regexp.MustCompile(
	`^\s*(\d+)\s*(?:,\s*type\s*:\s*(?:['"]([^'"]+)['"]|([^,\s]+)))?\s*$`,
)

// TranslateConfig controls how the AST is translated to wr jobs.
type TranslateConfig struct {
	RunID            string
	WorkflowName     string
	WorkflowPath     string
	Cwd              string
	Scheduler        string
	StubRun          bool
	ContainerRuntime string
	Params           map[string]any
	Profile          string
}

// TranslatePending resolves a pending stage into concrete jobs from completed upstream jobs.
func TranslatePending(pending *PendingStage, completed []CompletedJob, tc TranslateConfig) ([]*jobqueue.Job, error) {
	if pending == nil || pending.Process == nil {
		return nil, errors.New("pending stage is nil")
	}

	if pending.call == nil {
		return nil, errors.New("pending stage has no call context")
	}

	params := cloneParams(pending.params)
	if tc.Params != nil {
		params = MergeParams(params, tc.Params)
	}

	completedByRepGrp := make(map[string]CompletedJob, len(completed))

	completedItemsByRepGrp := make(map[string][]CompletedJob, len(completed))
	for _, job := range completed {
		completedItemsByRepGrp[job.RepGrp] = append(completedItemsByRepGrp[job.RepGrp], CompletedJob{
			RepGrp:      job.RepGrp,
			OutputPaths: cloneStrings(job.OutputPaths),
			DepGroups:   cloneStrings(job.DepGroups),
			ExitCode:    job.ExitCode,
		})

		existing, ok := completedByRepGrp[job.RepGrp]
		if !ok {
			completedByRepGrp[job.RepGrp] = CompletedJob{
				RepGrp:      job.RepGrp,
				OutputPaths: cloneStrings(job.OutputPaths),
				DepGroups:   cloneStrings(job.DepGroups),
				ExitCode:    job.ExitCode,
			}

			continue
		}

		existing.OutputPaths = appendUniqueStrings(existing.OutputPaths, job.OutputPaths)

		existing.DepGroups = appendUniqueStrings(existing.DepGroups, job.DepGroups)
		if existing.ExitCode == 0 && job.ExitCode != 0 {
			existing.ExitCode = job.ExitCode
		}

		completedByRepGrp[job.RepGrp] = existing
	}

	for _, repGrp := range pending.awaitRepGrps {
		job, ok := completedByRepGrp[repGrp]
		if !ok {
			return nil, fmt.Errorf("missing completed job for %q", repGrp)
		}

		if job.ExitCode != 0 {
			return nil, fmt.Errorf("completed job %q exited with code %d", repGrp, job.ExitCode)
		}
	}

	translated := cloneTranslatedCalls(pending.translated)
	for name, stage := range translated {
		job, ok := completedByRepGrp[stage.repGroup]
		if !ok {
			continue
		}

		stage.outputPaths = cloneStrings(job.OutputPaths)

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		matches := completedItemsByRepGrp[stage.repGroup]
		templates := cloneChannelItems(stage.items)
		emitTemplates := cloneEmittedOutputs(stage.emitOutputs)
		stage.items = make([]channelItem, 0, len(matches))
		stage.emitOutputs = nil

		for index, match := range matches {
			itemDeps := cloneStrings(deps)
			if len(match.DepGroups) > 0 {
				itemDeps = cloneStrings(match.DepGroups)
			}

			if len(deps) == len(matches) {
				itemDeps = []string{deps[index]}
			}

			itemValue := channelItemValue(match.OutputPaths)

			if len(templates) > 0 {
				templateIndex := index
				if templateIndex >= len(templates) {
					templateIndex = len(templates) - 1
				}

				itemValue = resolveCompletedOutputValue(templates[templateIndex].value, match.OutputPaths)
			}

			stage.items = append(stage.items, channelItem{
				value:     itemValue,
				depGroups: itemDeps,
			})

			for label, emitted := range emitTemplates {
				emittedPaths := matchCompletedOutputPathsForPatterns(emitted.outputPaths, match.OutputPaths)
				emittedValue := channelItemValue(emittedPaths)

				if len(emitted.items) > 0 {
					templateIndex := index
					if templateIndex >= len(emitted.items) {
						templateIndex = len(emitted.items) - 1
					}

					emittedValue = resolveCompletedOutputValue(emitted.items[templateIndex].value, emittedPaths)
				}

				if stage.emitOutputs == nil {
					stage.emitOutputs = map[string]emittedOutput{}
				}

				labelOutput := stage.emitOutputs[label]
				labelOutput.outputPaths = appendUniqueStrings(labelOutput.outputPaths, emittedPaths)
				labelOutput.items = append(labelOutput.items, channelItem{value: emittedValue, depGroups: itemDeps})
				stage.emitOutputs[label] = labelOutput
			}
		}

		stage.pending = false
		translated[name] = stage
	}

	bindingSets, err := resolveBindings(pending.Process, pending.call, pending.scope, translated, tc.Cwd)
	if err != nil {
		return nil, err
	}

	if len(bindingSets) == 0 {
		return nil, nil
	}

	bindingSets, err = filterWhenBindingSets(pending.Process, bindingSets, params)
	if err != nil {
		return nil, err
	}

	if len(bindingSets) == 0 {
		return nil, nil
	}

	jobs, _, err := translateProcessBindingSets(
		pending.Process,
		bindingSets,
		pending.scope,
		cloneDefaults(pending.defaults),
		cloneSelectors(pending.selectors),
		params,
		tc,
	)
	if err != nil {
		return nil, err
	}

	return jobs, nil
}

func cloneParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}

	clone := make(map[string]any, len(params))
	for key, value := range params {
		clone[key] = value
	}

	return clone
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	clone := make([]string, len(values))
	copy(clone, values)

	return clone
}

func appendUniqueStrings(existing []string, values []string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		seen[value] = struct{}{}
	}

	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}

		existing = append(existing, value)
		seen[value] = struct{}{}
	}

	return existing
}

func cloneTranslatedCalls(translated map[string]translatedCall) map[string]translatedCall {
	if len(translated) == 0 {
		return nil
	}

	clone := make(map[string]translatedCall, len(translated))
	for key, value := range translated {
		clone[key] = cloneTranslatedCall(value)
	}

	return clone
}

func cloneTranslatedCall(value translatedCall) translatedCall {
	value.depGroups = cloneStrings(value.depGroups)
	value.repGroups = cloneStrings(value.repGroups)
	value.outputPaths = cloneStrings(value.outputPaths)
	value.emitOutputs = cloneEmittedOutputs(value.emitOutputs)
	value.items = cloneChannelItems(value.items)

	return value
}

func cloneEmittedOutputs(outputs map[string]emittedOutput) map[string]emittedOutput {
	if len(outputs) == 0 {
		return nil
	}

	clone := make(map[string]emittedOutput, len(outputs))
	for label, output := range outputs {
		clone[label] = emittedOutput{
			outputPaths: cloneStrings(output.outputPaths),
			items:       cloneChannelItems(output.items),
		}
	}

	return clone
}

func channelItemValue(paths []string) any {
	if len(paths) == 0 {
		return ""
	}

	if len(paths) == 1 {
		return paths[0]
	}

	return cloneStrings(paths)
}

func resolveCompletedOutputValue(template any, completedPaths []string) any {
	switch value := template.(type) {
	case []any:
		resolved := make([]any, len(value))
		for index, element := range value {
			resolved[index] = resolveCompletedOutputValue(element, completedPaths)
		}

		return resolved
	case []string:
		return channelItemValue(completedPaths)
	case string:
		matched := matchCompletedOutputPaths(value, completedPaths)
		if len(matched) == 0 {
			return value
		}

		return channelItemValue(matched)
	default:
		return cloneChannelValue(value)
	}
}

func matchCompletedOutputPaths(pattern string, completedPaths []string) []string {
	cleanPattern := filepath.Clean(pattern)
	hasGlob := strings.ContainsAny(cleanPattern, "*?[")
	basePattern := filepath.Base(cleanPattern)
	baseHasGlob := strings.ContainsAny(basePattern, "*?[")
	patternHasDir := cleanPattern != basePattern
	directMatches := make([]string, 0, len(completedPaths))
	fallbackMatches := make([]string, 0, len(completedPaths))

	for _, candidate := range completedPaths {
		cleanCandidate := filepath.Clean(candidate)

		baseCandidate := filepath.Base(cleanCandidate)
		if cleanCandidate == cleanPattern {
			directMatches = append(directMatches, candidate)

			continue
		}

		if hasGlob {
			ok, err := filepath.Match(cleanPattern, cleanCandidate)
			if err == nil && ok {
				directMatches = append(directMatches, candidate)

				continue
			}
		}

		if patternHasDir && relativePatternMatches(cleanPattern, cleanCandidate, hasGlob) {
			directMatches = append(directMatches, candidate)

			continue
		}

		if baseCandidate == basePattern {
			fallbackMatches = append(fallbackMatches, candidate)

			continue
		}

		if !baseHasGlob {
			continue
		}

		ok, err := filepath.Match(basePattern, baseCandidate)
		if err == nil && ok {
			fallbackMatches = append(fallbackMatches, candidate)
		}
	}

	if len(directMatches) > 0 {
		return directMatches
	}

	if !patternHasDir || baseHasGlob || len(fallbackMatches) == 1 {
		return fallbackMatches
	}

	return nil
}

func relativePatternMatches(pattern, candidate string, hasGlob bool) bool {
	if filepath.IsAbs(pattern) {
		return false
	}

	trimmed := strings.TrimPrefix(candidate, string(filepath.Separator))
	if trimmed == candidate {
		return false
	}

	parts := strings.Split(trimmed, string(filepath.Separator))
	for index := range parts {
		suffix := filepath.Join(parts[index:]...)
		if !hasGlob {
			if suffix == pattern {
				return true
			}

			continue
		}

		ok, err := filepath.Match(pattern, suffix)
		if err == nil && ok {
			return true
		}
	}

	return false
}

func matchCompletedOutputPathsForPatterns(patterns []string, completedPaths []string) []string {
	if len(patterns) == 0 || len(completedPaths) == 0 {
		return nil
	}

	matched := []string{}
	for _, pattern := range patterns {
		matched = appendUniqueStrings(matched, matchCompletedOutputPaths(pattern, completedPaths))
	}

	return matched
}

func resolveBindings(proc *Process, call *Call, scope []string, translated map[string]translatedCall, cwd string) ([]bindingSet, error) {
	if call == nil || len(call.Args) == 0 {
		return []bindingSet{{regularIx: 0}}, nil
	}

	plans := []bindingSet{{regularIx: 0}}
	eachArgs := make([]struct {
		decl     *Declaration
		resolved resolvedArg
	}, 0, len(call.Args))

	for index, arg := range call.Args {
		decl := inputDeclarationForArg(proc, index)

		resolved, err := resolveBindingArg(arg, scope, translated, cwd)
		if err != nil {
			return nil, err
		}

		if resolved.fanout && len(resolved.items) == 0 {
			return nil, nil
		}

		if decl != nil && decl.Each {
			eachArgs = append(eachArgs, struct {
				decl     *Declaration
				resolved resolvedArg
			}{decl: decl, resolved: resolved})

			continue
		}

		if resolved.fanout && len(resolved.items) > 1 {
			switch {
			case len(plans) == 1:
				expanded := make([]bindingSet, len(resolved.items))
				for itemIndex, item := range resolved.items {
					bindings, bindErr := bindingsForInputDeclaration(decl, item)
					if bindErr != nil {
						return nil, bindErr
					}

					expanded[itemIndex] = bindingSet{
						bindings:  append(cloneStrings(plans[0].bindings), bindings...),
						values:    append(cloneChannelSlice(plans[0].values), cloneChannelSlice(item.values)...),
						depGroups: cloneStrings(plans[0].depGroups),
						regularIx: itemIndex,
					}
					expanded[itemIndex].depGroups = appendUniqueStrings(expanded[itemIndex].depGroups, item.depGroups)
				}

				plans = expanded
			case len(plans) == len(resolved.items):
				for itemIndex, item := range resolved.items {
					bindings, bindErr := bindingsForInputDeclaration(decl, item)
					if bindErr != nil {
						return nil, bindErr
					}

					plans[itemIndex].bindings = append(plans[itemIndex].bindings, bindings...)
					plans[itemIndex].values = append(plans[itemIndex].values, cloneChannelSlice(item.values)...)
					plans[itemIndex].depGroups = appendUniqueStrings(plans[itemIndex].depGroups, item.depGroups)
				}
			default:
				return nil, fmt.Errorf("channel cardinality mismatch for process %q", call.Target)
			}

			continue
		}

		binding := ""
		values := []any{}
		depGroups := []string{}
		bindings := []string{binding}

		if len(resolved.items) > 0 {
			var bindErr error

			bindings, bindErr = bindingsForInputDeclaration(decl, resolved.items[0])
			if bindErr != nil {
				return nil, bindErr
			}

			if len(bindings) > 0 {
				binding = bindings[0]
			}

			values = cloneChannelSlice(resolved.items[0].values)
			depGroups = resolved.items[0].depGroups
		}

		for itemIndex := range plans {
			if len(bindings) == 0 {
				plans[itemIndex].bindings = append(plans[itemIndex].bindings, binding)
				plans[itemIndex].values = append(plans[itemIndex].values, cloneChannelSlice(values)...)

				continue
			}

			plans[itemIndex].bindings = append(plans[itemIndex].bindings, bindings...)
			plans[itemIndex].values = append(plans[itemIndex].values, cloneChannelSlice(values)...)
			plans[itemIndex].depGroups = appendUniqueStrings(plans[itemIndex].depGroups, depGroups)
		}
	}

	if len(plans) == 0 {
		return nil, nil
	}

	for _, eachArg := range eachArgs {
		if eachArg.resolved.fanout && len(eachArg.resolved.items) == 0 {
			return nil, nil
		}

		expanded := make([]bindingSet, 0, len(plans)*maxInt(1, len(eachArg.resolved.items)))

		items := eachArg.resolved.items
		if len(items) == 0 {
			items = []bindingSet{{}}
		}

		for _, plan := range plans {
			for _, item := range items {
				bindings, bindErr := bindingsForInputDeclaration(eachArg.decl, item)
				if bindErr != nil {
					return nil, bindErr
				}

				nextPlan := bindingSet{
					bindings:  append(cloneStrings(plan.bindings), bindings...),
					values:    append(cloneChannelSlice(plan.values), cloneChannelSlice(item.values)...),
					depGroups: cloneStrings(plan.depGroups),
					regularIx: plan.regularIx,
					hasEach:   true,
				}
				nextPlan.depGroups = appendUniqueStrings(nextPlan.depGroups, item.depGroups)
				expanded = append(expanded, nextPlan)
			}
		}

		plans = expanded
	}

	eachIndexByRegular := map[int]int{}

	for index := range plans {
		if !plans[index].hasEach {
			continue
		}

		plans[index].eachIx = eachIndexByRegular[plans[index].regularIx]
		eachIndexByRegular[plans[index].regularIx]++
	}

	return plans, nil
}

func inputDeclarationForArg(proc *Process, index int) *Declaration {
	if proc == nil || index < 0 || index >= len(proc.Input) {
		return nil
	}

	return proc.Input[index]
}

func resolveBindingArg(arg ChanExpr, scope []string, translated map[string]translatedCall, cwd string) (resolvedArg, error) {
	switch arg.(type) {
	case ChanRef, NamedChannelRef, ChannelFactory, ChannelChain, PipeExpr:
		items, err := resolveTranslatedChannelItems(arg, scope, translated, cwd)
		if err == nil {
			resolvedItems := make([]bindingSet, 0, len(items))
			for _, item := range items {
				resolvedItems = append(resolvedItems, bindingSet{
					bindings:  []string{itemBinding(item.value)},
					values:    []any{cloneChannelValue(item.value)},
					depGroups: cloneStrings(item.depGroups),
				})
			}

			return resolvedArg{items: resolvedItems, fanout: true}, nil
		}
	}

	paths, deps, err := resolveArg(arg, scope, translated)
	if err != nil {
		return resolvedArg{}, err
	}

	if len(paths) > 1 && len(deps) > 1 {
		items := make([]bindingSet, 0, len(paths))
		for _, path := range paths {
			items = append(items, bindingSet{bindings: []string{path}, values: []any{path}, depGroups: cloneStrings(deps)})
		}

		return resolvedArg{items: items, fanout: true}, nil
	}

	var value any

	switch len(paths) {
	case 0:
		value = ""
	case 1:
		value = paths[0]
	default:
		value = cloneStrings(paths)
	}

	return resolvedArg{items: []bindingSet{{
		bindings:  []string{strings.Join(paths, " ")},
		values:    []any{value},
		depGroups: deps,
	}}}, nil
}

func resolveTranslatedChannelItems(
	arg ChanExpr,
	scope []string,
	translated map[string]translatedCall,
	cwd string,
) ([]channelItem, error) {
	return resolveChannelItems(arg, cwd, func(ref ChanRef) ([]channelItem, error) {
		stage, ok := resolveTranslatedOutput(ref.Name, scope, translated)
		if !ok {
			return nil, fmt.Errorf("unknown upstream reference %q", ref.Name)
		}

		if stage.skipped {
			return nil, nil
		}

		if len(stage.items) > 0 {
			return cloneChannelItems(stage.items), nil
		}

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		return []channelItem{{
			value:     channelItemValue(stage.outputPaths),
			depGroups: deps,
		}}, nil
	})
}

func resolveTranslatedOutput(name string, scope []string, translated map[string]translatedCall) (translatedCall, bool) {
	stageName, emitLabel, hasEmitLabel := emitOutputReference(name)

	lookupName := name
	if hasEmitLabel {
		lookupName = stageName
	}

	stage, ok := resolveTranslatedRef(lookupName, scope, translated)
	if !ok {
		return translatedCall{}, false
	}

	if !hasEmitLabel {
		return stage, true
	}

	emitted, ok := stage.emitOutputs[emitLabel]
	if !ok {
		warnf("nextflowdsl: emit label %q not found for %s; falling back to full outputs\n", emitLabel, name)

		return stage, true
	}

	stage.outputPaths = cloneStrings(emitted.outputPaths)
	stage.items = cloneChannelItems(emitted.items)

	return stage, true
}

func emitOutputReference(name string) (string, string, bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 || parts[len(parts)-2] != "out" {
		return "", "", false
	}

	stageName := strings.Join(parts[:len(parts)-2], ".")

	emitLabel := parts[len(parts)-1]
	if stageName == "" || emitLabel == "" {
		return "", "", false
	}

	return stageName, emitLabel, true
}

func resolveTranslatedRef(name string, scope []string, translated map[string]translatedCall) (translatedCall, bool) {
	parts := strings.Split(name, ".")
	if len(scope) > 0 {
		for prefixLen := len(parts); prefixLen >= 1; prefixLen-- {
			key := scopedTargetKey(scope, strings.Join(parts[:prefixLen], "."))
			if stage, ok := translated[key]; ok {
				return stage, true
			}
		}
	}

	for prefixLen := len(parts); prefixLen >= 1; prefixLen-- {
		key := strings.Join(parts[:prefixLen], ".")
		if stage, ok := translated[key]; ok {
			return stage, true
		}
	}

	return translatedCall{}, false
}

func scopedTargetKey(scope []string, target string) string {
	if len(scope) == 0 {
		return target
	}

	parts := append(append([]string{}, scope...), target)

	return strings.Join(parts, ".")
}

func warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

func itemBinding(item any) string {
	switch value := item.(type) {
	case []string:
		return strings.Join(value, " ")
	case []any:
		parts := make([]string, 0, len(value))
		for _, part := range value {
			parts = append(parts, itemBinding(part))
		}

		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(value)
	}
}

func resolveArg(arg ChanExpr, scope []string, translated map[string]translatedCall) ([]string, []string, error) {
	switch expr := arg.(type) {
	case ChanRef:
		stage, ok := resolveTranslatedOutput(expr.Name, scope, translated)
		if !ok {
			return nil, nil, fmt.Errorf("unknown upstream reference %q", expr.Name)
		}

		if stage.skipped {
			return nil, nil, nil
		}

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		return cloneStrings(stage.outputPaths), deps, nil
	case NamedChannelRef:
		return resolveArg(expr.Source, scope, translated)
	case ChannelChain:
		return resolveArg(expr.Source, scope, translated)
	case PipeExpr:
		paths := []string{}
		deps := []string{}
		seenPaths := map[string]struct{}{}
		seenDeps := map[string]struct{}{}

		for _, stage := range expr.Stages {
			stagePaths, stageDeps, err := resolveArg(stage, scope, translated)
			if err != nil {
				if _, ok := stage.(ChanRef); ok {
					continue
				}

				return nil, nil, err
			}

			for _, path := range stagePaths {
				if _, ok := seenPaths[path]; ok {
					continue
				}

				paths = append(paths, path)
				seenPaths[path] = struct{}{}
			}

			for _, dep := range stageDeps {
				if _, ok := seenDeps[dep]; ok {
					continue
				}

				deps = append(deps, dep)
				seenDeps[dep] = struct{}{}
			}
		}

		return paths, deps, nil
	default:
		return nil, nil, nil
	}
}

func bindingsForInputDeclaration(decl *Declaration, resolved bindingSet) ([]string, error) {
	if decl == nil || decl.Kind != "tuple" {
		if len(resolved.bindings) == 0 {
			return []string{""}, nil
		}

		return cloneStrings(resolved.bindings), nil
	}

	values := []any{}
	if len(resolved.values) > 0 {
		values = channelTuple(resolved.values[0])
	}

	if len(values) == 0 && len(resolved.bindings) > 0 {
		values = channelTuple(resolved.bindings[0])
	}

	if len(values) != len(decl.Elements) {
		return nil, fmt.Errorf("tuple input expected %d values, got %d", len(decl.Elements), len(values))
	}

	bindings := make([]string, 0, len(values))
	for _, value := range values {
		bindings = append(bindings, itemBinding(value))
	}

	return bindings, nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}

	return right
}

func filterWhenBindingSets(proc *Process, bindingSets []bindingSet, params map[string]any) ([]bindingSet, error) {
	if proc == nil || strings.TrimSpace(proc.When) == "" || len(bindingSets) == 0 {
		return bindingSets, nil
	}

	filtered := make([]bindingSet, 0, len(bindingSets))
	for _, bindingSet := range bindingSets {
		bindings := outputVarsWithValues(proc, bindingSet.bindings, bindingSet.values, nil)
		delete(bindings, "params")

		allowed, err := EvalWhenGuard(proc.When, bindings, params)
		if err != nil {
			return nil, err
		}

		if allowed {
			filtered = append(filtered, bindingSet)
		}
	}

	return filtered, nil
}

func outputVarsWithValues(proc *Process, bindings []string, values []any, params map[string]any) map[string]any {
	vars := make(map[string]any)
	if len(params) > 0 {
		vars["params"] = params
	}

	inputs := flattenedInputDeclarations(proc)
	for index, binding := range bindings {
		if index < len(inputs) && inputs[index] != nil && inputs[index].Name != "" {
			if index < len(values) {
				vars[inputs[index].Name] = cloneChannelValue(values[index])

				continue
			}

			vars[inputs[index].Name] = binding
		}
	}

	return vars
}

func flattenedInputDeclarations(proc *Process) []*Declaration {
	if proc == nil || len(proc.Input) == 0 {
		return nil
	}

	flat := make([]*Declaration, 0, len(proc.Input))
	for _, decl := range proc.Input {
		if decl == nil {
			flat = append(flat, nil)

			continue
		}

		if decl.Kind != "tuple" || len(decl.Elements) == 0 {
			flat = append(flat, decl)

			continue
		}

		for _, element := range decl.Elements {
			if element == nil {
				flat = append(flat, nil)

				continue
			}

			flat = append(flat, declarationFromTupleElement(element))
		}
	}

	return flat
}

// EvalWhenGuard evaluates a process when: expression with the given input
// bindings and params.
func EvalWhenGuard(whenExpr string, bindings map[string]any, params map[string]any) (bool, error) {
	if strings.TrimSpace(whenExpr) == "" {
		return true, nil
	}

	vars := cloneEvalVars(bindings)
	if vars == nil {
		vars = map[string]any{}
	}

	if params != nil {
		vars["params"] = params
	}

	expr, err := parseExprText(whenExpr)
	if err != nil {
		return false, err
	}

	value, err := EvalExpr(expr, vars)
	if err != nil {
		return false, err
	}

	return isTruthy(value), nil
}

func parseExprText(text string) (Expr, error) {
	tokens, err := lex(text)
	if err != nil {
		return nil, err
	}

	exprTokens := make([]token, 0, len(tokens))
	for _, tok := range tokens {
		if tok.typ == tokenEOF || tok.typ == tokenNewline {
			continue
		}

		exprTokens = append(exprTokens, tok)
	}

	if len(exprTokens) == 0 {
		return nil, errors.New("empty expression")
	}

	return parseExprTokens(exprTokens)
}

func translateProcessBindingSets(
	proc *Process,
	bindingSets []bindingSet,
	scope []string,
	defaults *ProcessDefaults,
	selectors []*ProcessSelector,
	params map[string]any,
	tc TranslateConfig,
) ([]*jobqueue.Job, translatedCall, error) {
	if len(bindingSets) == 0 {
		return nil, translatedCall{}, nil
	}

	jobs := make([]*jobqueue.Job, 0, len(bindingSets))
	stage := translatedCall{
		depGroups: make([]string, 0, len(bindingSets)),
		items:     make([]channelItem, 0, len(bindingSets)),
	}
	repGroup := scopedRepGroup(tc.WorkflowName, tc.RunID, scope, proc.Name)
	reqGroup := "nf." + proc.Name
	indexed := len(bindingSets) > 1
	stage.baseCwd = deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name)

	effectiveProc, err := processWithConfigDefaults(proc, defaults, selectors, params)
	if err != nil {
		return nil, translatedCall{}, err
	}

	procForCommand := effectiveProc
	if tc.StubRun && proc != nil && proc.Stub != "" {
		stubProc := *effectiveProc
		stubProc.Script = proc.Stub
		procForCommand = &stubProc
	}

	for index, bindingSet := range bindingSets {
		cwd := deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name)

		depGroup := scopedDepGroup(tc.RunID, scope, proc.Name)
		if bindingSet.hasEach {
			cwd = deterministicEachCwd(tc.Cwd, tc.RunID, scope, proc.Name, bindingSet.regularIx, bindingSet.eachIx)
		} else if indexed {
			cwd = deterministicIndexedCwd(tc.Cwd, tc.RunID, scope, proc.Name, index)
			depGroup = scopedIndexedDepGroup(tc.RunID, scope, proc.Name, index)
		}

		job := &jobqueue.Job{
			Cwd:          cwd,
			CwdMatters:   true,
			RepGroup:     repGroup,
			ReqGroup:     reqGroup,
			DepGroups:    []string{depGroup},
			Dependencies: depGroupsToDependencies(bindingSet.depGroups),
			Override:     0,
		}

		requirements, reqErr := buildRequirements(proc, effectiveProc, defaults, selectors, params, tc.Scheduler)
		if reqErr != nil {
			return nil, translatedCall{}, reqErr
		}

		job.Requirements = requirements

		if err = applyContainer(job, effectiveProc, tc.ContainerRuntime, params); err != nil {
			return nil, translatedCall{}, err
		}

		applyMaxForks(job, effectiveProc)
		applyFairPriority(job, effectiveProc, index, params)

		if err = applyFinishStrategyLimitGroup(job, effectiveProc, params, tc.RunID); err != nil {
			return nil, translatedCall{}, err
		}

		if err = applyErrorStrategy(job, effectiveProc, params); err != nil {
			return nil, translatedCall{}, err
		}

		if err = applyEnv(job, effectiveProc, params); err != nil {
			return nil, translatedCall{}, err
		}

		cmd, cmdErr := buildCommandWithValues(procForCommand, bindingSet.bindings, bindingSet.values, params, cwd, tc.Cwd)
		if cmdErr != nil {
			return nil, translatedCall{}, cmdErr
		}

		job.Cmd = cmd
		applyCaptureCleanupBehaviour(job)

		if err = applyPublishDirBehaviours(job, effectiveProc, bindingSet.bindings, params, tc, cwd); err != nil {
			return nil, translatedCall{}, err
		}

		jobs = append(jobs, job)

		if stage.depGroup == "" {
			stage.depGroup = depGroup
		}

		stage.depGroups = appendUniqueStrings(stage.depGroups, []string{depGroup})
		paths := outputPaths(effectiveProc, bindingSet.bindings, params, cwd)
		stage.outputPaths = append(stage.outputPaths, paths...)
		stage.items = append(stage.items, channelItem{
			value:     outputValue(effectiveProc, bindingSet.bindings, params, cwd, paths),
			depGroups: []string{depGroup},
		})
		stage.emitOutputs = mergeEmittedOutputs(
			stage.emitOutputs,
			emitOutputsForProcess(effectiveProc, bindingSet.bindings, params, cwd, depGroup),
		)
	}

	maxErrorsJob, err := newProcessMaxErrorsJob(proc, repGroup, scope, params, tc, len(jobs))
	if err != nil {
		return nil, translatedCall{}, err
	}

	if maxErrorsJob != nil {
		jobs = append(jobs, maxErrorsJob)
	}

	return jobs, stage, nil
}

func scopedRepGroup(workflowName, runID string, scope []string, processName string) string {
	parts := []string{"nf", repGroupToken(workflowName), repGroupToken(runID)}
	parts = append(parts, scope...)
	parts = append(parts, processName)

	return strings.Join(parts, ".")
}

func repGroupToken(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), ".", "%2E")
}

func deterministicCwd(base, runID string, scope []string, processName string) string {
	parts := []string{base, "nf-work", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName)

	return filepath.Clean(filepath.Join(parts...))
}

func processWithConfigDefaults(proc *Process, defaults *ProcessDefaults, selectors []*ProcessSelector, params map[string]any) (*Process, error) {
	if proc == nil {
		return nil, nil
	}

	cloned := *proc
	cloned.Directives = cloneDirectiveMap(proc.Directives)
	cloned.PublishDir = clonePublishDirs(proc.PublishDir)
	cloned.Env = cloneStringMap(proc.Env)

	if err := applyProcessDefaults(&cloned, defaults, params, false); err != nil {
		return nil, err
	}

	matches := make([]selectorMatch, 0, len(selectors))
	for _, selector := range selectors {
		collectSelectorMatches(proc, selector, true, &matches)
	}

	for _, specificity := range []int{selectorSpecificityLabel, selectorSpecificityName} {
		for _, match := range matches {
			if match.specificity != specificity {
				continue
			}

			if err := applyProcessDefaults(&cloned, match.settings, params, true); err != nil {
				return nil, err
			}
		}
	}

	return &cloned, nil
}

func cloneDirectiveMap(directives map[string]any) map[string]any {
	if len(directives) == 0 {
		return map[string]any{}
	}

	cloned := make(map[string]any, len(directives))
	for key, value := range directives {
		cloned[key] = value
	}

	return cloned
}

func clonePublishDirs(publishDirs []*PublishDir) []*PublishDir {
	if len(publishDirs) == 0 {
		return nil
	}

	cloned := make([]*PublishDir, 0, len(publishDirs))
	for _, publishDir := range publishDirs {
		if publishDir == nil {
			cloned = append(cloned, nil)

			continue
		}

		copyPublishDir := *publishDir
		cloned = append(cloned, &copyPublishDir)
	}

	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

func applyProcessDefaults(proc *Process, defaults *ProcessDefaults, params map[string]any, override bool) error {
	if proc == nil || defaults == nil {
		return nil
	}

	if proc.Directives == nil {
		proc.Directives = map[string]any{}
	}

	applyDirective := func(name string, value any) {
		if value == nil {
			return
		}

		if !override {
			if _, ok := proc.Directives[name]; ok {
				return
			}
		}

		proc.Directives[name] = value
	}

	if defaults.Cpus != 0 {
		if _, ok := proc.Directives["cpus"]; !ok {
			proc.Directives["cpus"] = defaults.Cpus
		}
	}

	if defaults.Memory != 0 {
		if _, ok := proc.Directives["memory"]; !ok {
			proc.Directives["memory"] = defaults.Memory
		}
	}

	if defaults.Time != 0 {
		if _, ok := proc.Directives["time"]; !ok {
			proc.Directives["time"] = defaults.Time
		}
	}

	if defaults.Disk != 0 {
		if _, ok := proc.Directives["disk"]; !ok {
			proc.Directives["disk"] = defaults.Disk
		}
	}

	if defaults.Container != "" && proc.Container == "" && proc.Directives["container"] == nil {
		proc.Container = defaults.Container
		applyDirective("container", defaults.Container)
	}

	if defaults.ErrorStrategy != nil && (override || (proc.ErrorStrat == "" && proc.Directives["errorStrategy"] == nil)) {
		proc.Directives["errorStrategy"] = defaults.ErrorStrategy

		resolved, err := resolveDirectiveString(
			"errorStrategy",
			defaults.ErrorStrategy,
			params,
			proc.ErrorStrat,
			defaultDirectiveTask(),
		)
		if err != nil {
			return err
		}

		proc.ErrorStrat = resolved
	}

	if defaults.MaxRetries != nil && (override || proc.MaxRetries == 0) {
		resolved, err := resolveDirectiveInt(
			"maxRetries",
			defaults.MaxRetries,
			params,
			proc.MaxRetries,
			defaultDirectiveTask(),
		)
		if err != nil {
			return err
		}

		proc.MaxRetries = resolved
	}

	if defaults.MaxForks != nil && (override || proc.MaxForks == 0) {
		resolved, err := resolveDirectiveInt("maxForks", defaults.MaxForks, params, proc.MaxForks, defaultDirectiveTask())
		if err != nil {
			return err
		}

		proc.MaxForks = resolved
	}

	if len(defaults.PublishDir) > 0 && (override || len(proc.PublishDir) == 0) {
		proc.PublishDir = clonePublishDirs(defaults.PublishDir)
	}

	if len(defaults.Ext) > 0 {
		current, err := extDirectiveSourceMap(proc.Directives["ext"])
		if err != nil {
			return err
		}

		if override {
			proc.Directives["ext"] = mergeExtValues(current, defaults.Ext)
		} else {
			proc.Directives["ext"] = mergeExtValues(current, defaults.Ext)
		}
	}

	applyDirective("queue", defaults.Queue)
	applyDirective("clusterOptions", defaults.ClusterOptions)
	applyDirective("containerOptions", defaults.ContainerOptions)
	applyDirective("accelerator", defaults.Accelerator)
	applyDirective("arch", defaults.Arch)
	applyDirective("scratch", defaults.Scratch)
	applyDirective("storeDir", defaults.StoreDir)
	applyDirective("conda", defaults.Conda)
	applyDirective("spack", defaults.Spack)
	applyDirective("fair", defaults.Fair)

	if defaults.Shell != nil && (override || strings.TrimSpace(proc.Shell) == "") {
		resolved, err := resolveShellDirective(defaults.Shell, params)
		if err != nil {
			return err
		}

		if resolved != "" {
			proc.Shell = resolved
		}
	}

	if defaults.BeforeScript != nil && (override || proc.BeforeScript == "") {
		resolved, err := resolveDirectiveString(
			"beforeScript",
			defaults.BeforeScript,
			params,
			proc.BeforeScript,
			defaultDirectiveTask(),
		)
		if err != nil {
			return err
		}

		proc.BeforeScript = resolved
	}

	if defaults.AfterScript != nil && (override || proc.AfterScript == "") {
		resolved, err := resolveDirectiveString(
			"afterScript",
			defaults.AfterScript,
			params,
			proc.AfterScript,
			defaultDirectiveTask(),
		)
		if err != nil {
			return err
		}

		proc.AfterScript = resolved
	}

	if defaults.Module != nil && (override || proc.Module == "") {
		resolved, err := resolveDirectiveString("module", defaults.Module, params, proc.Module, defaultDirectiveTask())
		if err != nil {
			return err
		}

		proc.Module = resolved
	}

	if defaults.Cache != nil && (override || proc.Cache == "") {
		resolved, err := resolveDirectiveString("cache", defaults.Cache, params, proc.Cache, defaultDirectiveTask())
		if err != nil {
			return err
		}

		proc.Cache = resolved
	}

	if defaults.Tag != nil && (override || proc.Tag == "") {
		resolved, err := resolveDirectiveString("tag", defaults.Tag, params, proc.Tag, defaultDirectiveTask())
		if err != nil {
			return err
		}

		proc.Tag = resolved
	}

	if len(defaults.Directives) > 0 {
		for key, value := range defaults.Directives {
			applyDirective(key, value)
		}
	}

	if len(defaults.Env) > 0 {
		if proc.Env == nil {
			proc.Env = map[string]string{}
		}

		for key, value := range defaults.Env {
			if _, ok := proc.Env[key]; ok {
				continue
			}

			proc.Env[key] = value
		}
	}

	return nil
}

func resolveDirectiveString(
	name string,
	expr any,
	params map[string]any,
	fallback string,
	task map[string]any,
) (string, error) {
	if expr == nil {
		return fallback, nil
	}

	value, fallbackUsed, err := resolveDirectiveValue(name, expr, params, task)
	if err != nil {
		return "", err
	}

	if fallbackUsed {
		return fallback, nil
	}

	stringValue, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s directive must evaluate to a string", name)
	}

	return stringValue, nil
}

func resolveDirectiveValue(name string, expr any, params map[string]any, task map[string]any) (any, bool, error) {
	if expr == nil {
		return nil, false, nil
	}

	if unsupported, ok := expr.(UnsupportedExpr); ok {
		warnf("nextflowdsl: falling back for %s directive with unsupported expression %q\n", name, unsupported.Text)

		return nil, true, nil
	}

	value, err := evalDirectiveExpr(expr, params, task)
	if err != nil {
		return nil, false, err
	}

	if unsupported, ok := value.(UnsupportedExpr); ok {
		warnf("nextflowdsl: falling back for %s directive with unsupported expression %q\n", name, unsupported.Text)

		return nil, true, nil
	}

	return value, false, nil
}

func evalDirectiveExpr(expr any, params map[string]any, task map[string]any) (any, error) {
	switch typed := expr.(type) {
	case nil:
		return nil, nil
	case string, int, bool, float64, []any, []string, map[string]any:
		return typed, nil
	}

	if closure, ok := expr.(ClosureExpr); ok {
		if len(closure.Params) != 0 {
			return UnsupportedExpr{Text: renderExpr(closure)}, nil
		}

		return evalStatementBody(closure.Body, cloneEvalVars(exprVarsWithTask(params, task)))
	}

	return EvalExpr(expr, exprVarsWithTask(params, task))
}

func exprVarsWithTask(params map[string]any, task map[string]any) map[string]any {
	if params == nil && task == nil {
		return nil
	}

	vars := make(map[string]any, 2)
	if params != nil {
		vars["params"] = params
	}

	if task != nil {
		vars["task"] = task
	}

	return vars
}

func defaultDirectiveTask() map[string]any {
	return map[string]any{
		"attempt":           1,
		"cpus":              1,
		"memory":            0,
		"exitStatus":        0,
		"hash":              "",
		"index":             0,
		"name":              "",
		"previousException": "",
		"previousTrace":     "",
		"process":           "",
		"workDir":           "",
	}
}

func resolveDirectiveInt(name string, expr any, params map[string]any, fallback int, task map[string]any) (int, error) {
	if expr == nil {
		return fallback, nil
	}

	value, fallbackUsed, err := resolveDirectiveValue(name, expr, params, task)
	if err != nil {
		return 0, err
	}

	if fallbackUsed {
		return fallback, nil
	}

	intValue, ok := value.(int)
	if !ok {
		return 0, fmt.Errorf("%s directive must evaluate to an integer", name)
	}

	return intValue, nil
}

func extDirectiveSourceMap(raw any) (map[string]any, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return cloneExtMap(typed), nil
	case MapExpr:
		values := make(map[string]any, len(typed.Keys))
		for index, keyExpr := range typed.Keys {
			keyValue, err := EvalExpr(keyExpr, nil)
			if err != nil {
				return nil, err
			}

			key, ok := keyValue.(string)
			if !ok {
				return nil, errors.New("ext directive keys must evaluate to strings")
			}

			values[key] = typed.Values[index]
		}

		return values, nil
	default:
		return nil, errors.New("ext directive must evaluate to a map")
	}
}

func cloneExtMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneExtValue(value)
	}

	return cloned
}

func cloneExtValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneExtMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneExtValue(item)
		}

		return cloned
	case []string:
		return cloneStrings(typed)
	default:
		return typed
	}
}

func mergeExtValues(base map[string]any, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	merged := cloneExtMap(base)
	if merged == nil {
		merged = map[string]any{}
	}

	for key, value := range override {
		merged[key] = cloneExtValue(value)
	}

	return merged
}

func resolveShellDirective(expr any, params map[string]any) (string, error) {
	value, fallbackUsed, err := resolveDirectiveValue("shell", expr, params, defaultDirectiveTask())
	if err != nil {
		return "", err
	}

	if fallbackUsed || value == nil {
		return "", nil
	}

	switch typed := value.(type) {
	case string:
		return typed, nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			stringPart, ok := part.(string)
			if !ok {
				return "", errors.New("shell directive list entries must evaluate to strings")
			}

			parts = append(parts, stringPart)
		}

		return strings.Join(parts, " "), nil
	default:
		return "", errors.New("shell directive must evaluate to a string or list of strings")
	}
}

func collectSelectorMatches(proc *Process, selector *ProcessSelector, matched bool, matches *[]selectorMatch) {
	if !matched || proc == nil || selector == nil {
		return
	}

	if !selectorMatchesProcess(selector, proc) {
		return
	}

	if selector.Settings != nil {
		*matches = append(*matches, selectorMatch{
			specificity: selectorSpecificity(selector.Kind),
			settings:    selector.Settings,
		})
	}

	collectSelectorMatches(proc, selector.Inner, true, matches)
}

func selectorMatchesProcess(selector *ProcessSelector, proc *Process) bool {
	if selector == nil || proc == nil {
		return false
	}

	switch selector.Kind {
	case "withLabel":
		for _, label := range proc.Labels {
			if selectorPatternMatches(selector.Pattern, label) {
				return true
			}
		}

		return false
	case "withName":
		return selectorPatternMatches(selector.Pattern, proc.Name)
	default:
		return false
	}
}

func selectorPatternMatches(pattern, candidate string) bool {
	if strings.HasPrefix(pattern, "~") {
		matched, err := regexp.MatchString(pattern[1:], candidate)

		return err == nil && matched
	}

	matched, err := filepath.Match(pattern, candidate)

	return err == nil && matched
}

func selectorSpecificity(kind string) int {
	if kind == "withName" {
		return selectorSpecificityName
	}

	return selectorSpecificityLabel
}

func scopedDepGroup(runID string, scope []string, processName string) string {
	parts := []string{"nf", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName)

	return strings.Join(parts, ".")
}

func deterministicEachCwd(base, runID string, scope []string, processName string, regularIndex, eachIndex int) string {
	parts := []string{base, "nf-work", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName, fmt.Sprintf("%d_%d", regularIndex, eachIndex))

	return filepath.Clean(filepath.Join(parts...))
}

func deterministicIndexedCwd(base, runID string, scope []string, processName string, itemIndex int) string {
	parts := []string{base, "nf-work", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName, strconv.Itoa(itemIndex))

	return filepath.Clean(filepath.Join(parts...))
}

func scopedIndexedDepGroup(runID string, scope []string, processName string, itemIndex int) string {
	parts := []string{"nf", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName, strconv.Itoa(itemIndex))

	return strings.Join(parts, ".")
}

func depGroupsToDependencies(depGroups []string) jobqueue.Dependencies {
	if len(depGroups) == 0 {
		return nil
	}

	dependencies := make(jobqueue.Dependencies, 0, len(depGroups))
	for _, depGroup := range depGroups {
		dependencies = append(dependencies, jobqueue.NewDepGroupDependency(depGroup))
	}

	return dependencies
}

func buildRequirements(proc *Process, effectiveProc *Process, defaults *ProcessDefaults, selectors []*ProcessSelector, params map[string]any, schedulerName string) (*scheduler.Requirements, error) {
	req := &scheduler.Requirements{}
	task := defaultDirectiveTask()

	legacyDefaults := MatchSelectors(proc, defaults, selectors)
	if effectiveProc == nil {
		effectiveProc = proc
	}

	cpus, err := resolveDirectiveInt(
		"cpus",
		proc.Directives["cpus"],
		params,
		intDefault(legacyDefaults.Cpus, defaultCPUs),
		task,
	)
	if err != nil {
		return nil, err
	}

	task["cpus"] = cpus

	memory, err := resolveDirectiveInt(
		"memory",
		proc.Directives["memory"],
		params,
		intDefault(legacyDefaults.Memory, defaultMemory),
		task,
	)
	if err != nil {
		return nil, err
	}

	task["memory"] = memory

	timeMinutes, err := resolveDirectiveInt(
		"time",
		proc.Directives["time"],
		params,
		intDefault(legacyDefaults.Time, defaultTime),
		task,
	)
	if err != nil {
		return nil, err
	}

	disk, err := resolveDirectiveInt(
		"disk",
		proc.Directives["disk"],
		params,
		intDefault(legacyDefaults.Disk, defaultDisk),
		task,
	)
	if err != nil {
		return nil, err
	}

	req.Cores = float64(cpus)
	req.CoresSet = true
	req.RAM = memory
	req.Time = time.Duration(timeMinutes) * time.Minute
	req.Disk = disk
	req.DiskSet = true

	queue, err := resolveDirectiveString("queue", effectiveProc.Directives["queue"], params, "", task)
	if err != nil {
		return nil, err
	}

	if queue != "" {
		if req.Other == nil {
			req.Other = make(map[string]string)
		}

		req.Other["scheduler_queue"] = queue
		req.OtherSet = true
	}

	clusterOptions, err := resolveDirectiveString(
		"clusterOptions",
		effectiveProc.Directives["clusterOptions"],
		params,
		"",
		task,
	)
	if err != nil {
		return nil, err
	}

	if clusterOptions != "" {
		if req.Other == nil {
			req.Other = make(map[string]string)
		}

		req.Other["scheduler_misc"] = clusterOptions
		req.OtherSet = true
	}

	acceleratorOptions, err := resolveAcceleratorOptions(effectiveProc, params, schedulerName)
	if err != nil {
		return nil, err
	}

	req.Other = appendSchedulerRequirement(req.Other, "scheduler_misc", acceleratorOptions)

	archOptions, err := resolveArchOptions(effectiveProc, params, schedulerName)
	if err != nil {
		return nil, err
	}

	req.Other = appendSchedulerRequirement(req.Other, "scheduler_misc", archOptions)
	if len(req.Other) > 0 {
		req.OtherSet = true
	}

	return req, nil
}

// MatchSelectors returns merged process defaults by applying matching selectors
// in specificity order before process-level directives are evaluated.
func MatchSelectors(proc *Process, base *ProcessDefaults, selectors []*ProcessSelector) *ProcessDefaults {
	merged := cloneDefaults(base)
	if proc == nil || len(selectors) == 0 {
		return merged
	}

	matches := make([]selectorMatch, 0, len(selectors))
	for _, selector := range selectors {
		collectSelectorMatches(proc, selector, true, &matches)
	}

	for _, specificity := range []int{selectorSpecificityLabel, selectorSpecificityName} {
		for _, match := range matches {
			if match.specificity != specificity {
				continue
			}

			merged = mergeDefaults(merged, match.settings)
		}
	}

	return merged
}

func cloneDefaults(defaults *ProcessDefaults) *ProcessDefaults {
	if defaults == nil {
		return &ProcessDefaults{}
	}

	clone := &ProcessDefaults{
		Cpus:             defaults.Cpus,
		Memory:           defaults.Memory,
		Time:             defaults.Time,
		Disk:             defaults.Disk,
		Container:        defaults.Container,
		ErrorStrategy:    cloneExtValue(defaults.ErrorStrategy),
		MaxRetries:       cloneExtValue(defaults.MaxRetries),
		MaxForks:         cloneExtValue(defaults.MaxForks),
		PublishDir:       clonePublishDirs(defaults.PublishDir),
		Queue:            cloneExtValue(defaults.Queue),
		ClusterOptions:   cloneExtValue(defaults.ClusterOptions),
		Ext:              cloneExtMap(defaults.Ext),
		ContainerOptions: cloneExtValue(defaults.ContainerOptions),
		Accelerator:      cloneExtValue(defaults.Accelerator),
		Arch:             cloneExtValue(defaults.Arch),
		Shell:            cloneExtValue(defaults.Shell),
		BeforeScript:     cloneExtValue(defaults.BeforeScript),
		AfterScript:      cloneExtValue(defaults.AfterScript),
		Cache:            cloneExtValue(defaults.Cache),
		Scratch:          cloneExtValue(defaults.Scratch),
		StoreDir:         cloneExtValue(defaults.StoreDir),
		Module:           cloneExtValue(defaults.Module),
		Conda:            cloneExtValue(defaults.Conda),
		Spack:            cloneExtValue(defaults.Spack),
		Fair:             cloneExtValue(defaults.Fair),
		Tag:              cloneExtValue(defaults.Tag),
	}
	if len(defaults.Directives) > 0 {
		clone.Directives = make(map[string]any, len(defaults.Directives))
		for key, value := range defaults.Directives {
			clone.Directives[key] = cloneExtValue(value)
		}
	}

	if len(defaults.Env) > 0 {
		clone.Env = make(map[string]string, len(defaults.Env))
		for key, value := range defaults.Env {
			clone.Env[key] = value
		}
	}

	return clone
}

func mergeDefaults(base *ProcessDefaults, override *ProcessDefaults) *ProcessDefaults {
	merged := cloneDefaults(base)
	if override == nil {
		return merged
	}

	if override.Cpus != 0 {
		merged.Cpus = override.Cpus
	}

	if override.Memory != 0 {
		merged.Memory = override.Memory
	}

	if override.Time != 0 {
		merged.Time = override.Time
	}

	if override.Disk != 0 {
		merged.Disk = override.Disk
	}

	if override.Container != "" {
		merged.Container = override.Container
	}

	if override.ErrorStrategy != nil {
		merged.ErrorStrategy = cloneExtValue(override.ErrorStrategy)
	}

	if override.MaxRetries != nil {
		merged.MaxRetries = cloneExtValue(override.MaxRetries)
	}

	if override.MaxForks != nil {
		merged.MaxForks = cloneExtValue(override.MaxForks)
	}

	if len(override.PublishDir) > 0 {
		merged.PublishDir = clonePublishDirs(override.PublishDir)
	}

	if override.Queue != nil {
		merged.Queue = cloneExtValue(override.Queue)
	}

	if override.ClusterOptions != nil {
		merged.ClusterOptions = cloneExtValue(override.ClusterOptions)
	}

	if len(override.Ext) > 0 {
		merged.Ext = mergeExtValues(merged.Ext, override.Ext)
	}

	if override.ContainerOptions != nil {
		merged.ContainerOptions = cloneExtValue(override.ContainerOptions)
	}

	if override.Accelerator != nil {
		merged.Accelerator = cloneExtValue(override.Accelerator)
	}

	if override.Arch != nil {
		merged.Arch = cloneExtValue(override.Arch)
	}

	if override.Shell != nil {
		merged.Shell = cloneExtValue(override.Shell)
	}

	if override.BeforeScript != nil {
		merged.BeforeScript = cloneExtValue(override.BeforeScript)
	}

	if override.AfterScript != nil {
		merged.AfterScript = cloneExtValue(override.AfterScript)
	}

	if override.Cache != nil {
		merged.Cache = cloneExtValue(override.Cache)
	}

	if override.Scratch != nil {
		merged.Scratch = cloneExtValue(override.Scratch)
	}

	if override.StoreDir != nil {
		merged.StoreDir = cloneExtValue(override.StoreDir)
	}

	if override.Module != nil {
		merged.Module = cloneExtValue(override.Module)
	}

	if override.Conda != nil {
		merged.Conda = cloneExtValue(override.Conda)
	}

	if override.Spack != nil {
		merged.Spack = cloneExtValue(override.Spack)
	}

	if override.Fair != nil {
		merged.Fair = cloneExtValue(override.Fair)
	}

	if override.Tag != nil {
		merged.Tag = cloneExtValue(override.Tag)
	}

	if len(override.Directives) > 0 {
		if merged.Directives == nil {
			merged.Directives = make(map[string]any, len(override.Directives))
		}

		for key, value := range override.Directives {
			merged.Directives[key] = cloneExtValue(value)
		}
	}

	if len(override.Env) > 0 {
		if merged.Env == nil {
			merged.Env = make(map[string]string, len(override.Env))
		}

		for key, value := range override.Env {
			merged.Env[key] = value
		}
	}

	return merged
}

func intDefault(value, fallback int) int {
	if value != 0 {
		return value
	}

	return fallback
}

func resolveAcceleratorOptions(proc *Process, params map[string]any, schedulerName string) (string, error) {
	count, acceleratorType, present, err := resolveAcceleratorDirective(proc.Directives["accelerator"], params)
	if err != nil {
		return "", err
	}

	if !present {
		return "", nil
	}

	if schedulerName != "lsf" {
		warnf("nextflowdsl: accelerator directive is only applied for lsf scheduling\n")

		return "", nil
	}

	if acceleratorType != "" {
		warnf("nextflowdsl: accelerator type %q is informational only for lsf scheduling\n", acceleratorType)
	}

	if count <= 0 {
		return "", nil
	}

	return fmt.Sprintf(`-R "select[ngpus>0] rusage[ngpus_physical=%d]"`, count), nil
}

func resolveAcceleratorDirective(expr any, params map[string]any) (int, string, bool, error) {
	if expr == nil {
		return 0, "", false, nil
	}

	if unsupported, ok := expr.(UnsupportedExpr); ok {
		count, acceleratorType, err := parseAcceleratorDirectiveText(unsupported.Text)
		if err != nil {
			warnf("nextflowdsl: falling back for accelerator directive with unsupported expression %q\n", unsupported.Text)

			return 0, "", false, nil
		}

		return count, acceleratorType, true, nil
	}

	value, fallbackUsed, err := resolveDirectiveValue("accelerator", expr, params, defaultDirectiveTask())
	if err != nil {
		return 0, "", false, err
	}

	if fallbackUsed || value == nil {
		return 0, "", false, nil
	}

	count, acceleratorType, err := acceleratorDirectiveParts(value)
	if err != nil {
		return 0, "", false, err
	}

	return count, acceleratorType, true, nil
}

func parseAcceleratorDirectiveText(text string) (int, string, error) {
	matches := acceleratorDirectiveTextPattern.FindStringSubmatch(text)
	if matches == nil {
		return 0, "", fmt.Errorf("unsupported accelerator directive %q", text)
	}

	count, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, "", errors.New("accelerator directive must evaluate to an integer")
	}

	acceleratorType := matches[2]
	if acceleratorType == "" {
		acceleratorType = matches[3]
	}

	return count, acceleratorType, nil
}

func acceleratorDirectiveParts(value any) (int, string, error) {
	switch typed := value.(type) {
	case int:
		return typed, "", nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, "", errors.New("accelerator directive must evaluate to an integer")
		}

		return int(typed), "", nil
	case []any:
		if len(typed) == 0 {
			return 0, "", nil
		}

		count, acceleratorType, err := acceleratorDirectiveParts(typed[0])
		if err != nil {
			return 0, "", err
		}

		for _, element := range typed[1:] {
			typeName, err := acceleratorDirectiveType(element)
			if err != nil {
				return 0, "", err
			}

			if typeName != "" {
				acceleratorType = typeName
			}
		}

		return count, acceleratorType, nil
	case map[string]any:
		countValue, hasCount := typed["count"]
		if !hasCount {
			countValue = typed["value"]
		}

		if !hasCount && countValue == nil {
			countValue = typed["num"]
		}

		count, _, err := acceleratorDirectiveParts(countValue)
		if err != nil {
			return 0, "", err
		}

		acceleratorType, err := acceleratorDirectiveType(typed)
		if err != nil {
			return 0, "", err
		}

		return count, acceleratorType, nil
	default:
		return 0, "", errors.New("accelerator directive must evaluate to an integer")
	}
}

func acceleratorDirectiveType(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case map[string]any:
		typeValue, ok := typed["type"]
		if !ok || typeValue == nil {
			return "", nil
		}

		typeName, ok := typeValue.(string)
		if !ok {
			return "", errors.New("accelerator type must evaluate to a string")
		}

		return typeName, nil
	default:
		return "", nil
	}
}

func appendSchedulerRequirement(other map[string]string, key, option string) map[string]string {
	if strings.TrimSpace(option) == "" {
		return other
	}

	if other == nil {
		other = make(map[string]string)
	}

	if existing := strings.TrimSpace(other[key]); existing != "" {
		other[key] = existing + " " + option
	} else {
		other[key] = option
	}

	return other
}

func resolveArchOptions(proc *Process, params map[string]any, schedulerName string) (string, error) {
	value, err := resolveDirectiveString("arch", proc.Directives["arch"], params, "", defaultDirectiveTask())
	if err != nil {
		return "", err
	}

	if value == "" {
		return "", nil
	}

	if schedulerName != "lsf" {
		warnf("nextflowdsl: arch directive is only applied for lsf scheduling\n")

		return "", nil
	}

	switch value {
	case "linux/x86_64":
		return `-R "select[type==X86_64]"`, nil
	case "linux/aarch64":
		return `-R "select[type==AARCH64]"`, nil
	default:
		return "", nil
	}
}

func applyContainer(job *jobqueue.Job, proc *Process, runtime string, params map[string]any) error {
	container := proc.Container

	resolved, err := resolveDirectiveString(
		"container",
		proc.Directives["container"],
		params,
		container,
		defaultDirectiveTask(),
	)
	if err != nil {
		return err
	}

	container = resolved
	if container == "" {
		return nil
	}

	containerOptions, err := resolveDirectiveString(
		"containerOptions",
		proc.Directives["containerOptions"],
		params,
		"",
		defaultDirectiveTask(),
	)
	if err != nil {
		return err
	}

	if containerOptions != "" {
		container = strings.TrimSpace(containerOptions + " " + container)
	}

	switch runtime {
	case "docker":
		job.WithDocker = container
	case "singularity", "apptainer":
		job.WithSingularity = container
	}

	return nil
}

func applyMaxForks(job *jobqueue.Job, proc *Process) {
	if proc.MaxForks > 0 {
		appendLimitGroup(job, fmt.Sprintf("%s:%d", proc.Name, proc.MaxForks))
	}
}

func appendLimitGroup(job *jobqueue.Job, limitGroup string) {
	if limitGroup == "" || slices.Contains(job.LimitGroups, limitGroup) {
		return
	}

	job.LimitGroups = append(job.LimitGroups, limitGroup)
}

func applyFairPriority(job *jobqueue.Job, proc *Process, index int, params map[string]any) {
	fair, err := resolveDirectiveBool("fair", proc.Directives["fair"], params, false, defaultDirectiveTask())
	if err != nil || !fair {
		return
	}

	clampedIndex := index
	if clampedIndex > 254 {
		clampedIndex = 254
	}

	//nolint:gosec // clampedIndex is bounded above, so the computed priority always fits in uint8.
	job.Priority = uint8(255 - clampedIndex)
}

func resolveDirectiveBool(
	name string,
	expr any,
	params map[string]any,
	fallback bool,
	task map[string]any,
) (bool, error) {
	if expr == nil {
		return fallback, nil
	}

	value, fallbackUsed, err := resolveDirectiveValue(name, expr, params, task)
	if err != nil {
		return false, err
	}

	if fallbackUsed {
		return fallback, nil
	}

	boolValue, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s directive must evaluate to a boolean", name)
	}

	return boolValue, nil
}

func applyFinishStrategyLimitGroup(job *jobqueue.Job, proc *Process, params map[string]any, runID string) error {
	errorStrategy, err := resolveErrorStrategy(proc, params)
	if err != nil {
		return err
	}

	if errorStrategy == "finish" {
		appendLimitGroup(job, finishStrategyLimitGroup(proc.Name, runID))
	}

	return nil
}

func resolveErrorStrategy(proc *Process, params map[string]any) (string, error) {
	return resolveDirectiveString(
		"errorStrategy",
		proc.Directives["errorStrategy"],
		params,
		proc.ErrorStrat,
		defaultDirectiveTask(),
	)
}

func finishStrategyLimitGroup(processName, runID string) string {
	return fmt.Sprintf("nf-finish-%s-%s", repGroupToken(processName), repGroupToken(runID))
}

func applyErrorStrategy(job *jobqueue.Job, proc *Process, params map[string]any) error {
	errorStrategy, err := resolveErrorStrategy(proc, params)
	if err != nil {
		return err
	}

	switch errorStrategy {
	case "", "finish", "terminate":
		job.Retries = 0
	case "retry":
		//nolint:gosec // MaxRetries is validated by directive parsing before being stored on the process.
		job.Retries = uint8(proc.MaxRetries)
	case "ignore":
		job.Retries = 0
		job.Behaviours = append(job.Behaviours, &jobqueue.Behaviour{When: jobqueue.OnFailure, Do: jobqueue.Remove})
	default:
		warnf("nextflowdsl: unsupported errorStrategy %q, using terminate semantics\n", errorStrategy)

		job.Retries = 0
	}

	return nil
}

func applyEnv(job *jobqueue.Job, proc *Process, params map[string]any) error {
	env := make([]string, 0, len(proc.Env)+1)

	positions := make(map[string]int, len(proc.Env)+1)
	for key, value := range proc.Env {
		if index, ok := positions[key]; ok {
			env[index] = key + "=" + value

			continue
		}

		positions[key] = len(env)
		env = append(env, key+"="+value)
	}

	shellCommand, err := resolveShellDirective(proc.Directives["shell"], params)
	if err != nil {
		return err
	}

	if shellCommand != "" {
		if index, ok := positions["RunnerExecShell"]; ok {
			env[index] = "RunnerExecShell=" + shellCommand
		} else {
			env = append(env, "RunnerExecShell="+shellCommand)
		}
	}

	if len(env) == 0 {
		return nil
	}

	return job.EnvAddOverride(env)
}

func buildCommandWithValues(proc *Process, bindings []string, values []any, params map[string]any, cwd string, launchCwd string) (string, error) {
	body, err := buildCommandBody(proc, bindings, values, params)
	if err != nil {
		return "", err
	}

	executionCommand := captureCommand(body, nfStdoutFile, nfStderrFile)

	scratchEnabled, scratchPath, err := resolveScratchDirective(proc, params)
	if err != nil {
		return "", err
	}

	if scratchEnabled {
		executionCommand = wrapScratchCommand(body, proc, bindings, params, scratchPath)
	}

	executionCommand, err = prependEnvironmentDirectives(executionCommand, proc, params)
	if err != nil {
		return "", err
	}

	storeDir, storeDirEnabled, err := resolveStoreDirDirective(proc, params, launchCwd)
	if err != nil {
		return "", err
	}

	if storeDirEnabled {
		return wrapStoreDirCommand(executionCommand, proc, bindings, params, cwd, storeDir), nil
	}

	return executionCommand, nil
}

func buildCommandBody(proc *Process, bindings []string, values []any, params map[string]any) (string, error) {
	script, err := renderScript(proc, bindings, values, params)
	if err != nil {
		return "", err
	}

	bodyParts := make([]string, 0, 3)
	if proc.BeforeScript != "" {
		bodyParts = append(bodyParts, proc.BeforeScript)
	}

	bodyParts = append(bodyParts, script)
	if proc.AfterScript != "" {
		bodyParts = append(bodyParts, proc.AfterScript)
	}

	evalLines, err := evalOutputCaptureLines(proc, bindings, params)
	if err != nil {
		return "", err
	}

	bodyParts = append(bodyParts, evalLines...)
	body := strings.Join(bodyParts, "\n")

	inputs := flattenedInputDeclarations(proc)

	prefixes := make([]string, 0, len(bindings))
	for index, binding := range bindings {
		if binding == "" {
			continue
		}

		name := fmt.Sprintf("NF_INPUT_%d", index+1)
		if index < len(inputs) && inputs[index] != nil && inputs[index].Name != "" {
			name = inputs[index].Name
		}

		prefixes = append(prefixes, fmt.Sprintf("export %s=%s", name, shellQuote(binding)))
	}

	commandParts := moduleLoadLines(proc.Module)
	commandParts = append(commandParts, prefixes...)

	commandParts = append(commandParts, body)
	if len(commandParts) > 1 {
		body = strings.Join(commandParts, "\n")
	}

	body = strings.TrimRight(body, " \t\r")
	if strings.TrimSpace(body) == "" {
		body = ":"
	}

	return body, nil
}

func renderScript(proc *Process, bindings []string, values []any, params map[string]any) (string, error) {
	if strings.TrimSpace(proc.Shell) != "" {
		return renderShellSection(proc, bindings, params)
	}

	script, err := SubstituteParams(proc.Script, params)
	if err != nil {
		return "", err
	}

	extValues, err := resolveExtDirectiveWithValues(proc, nil, bindings, values, params)
	if err != nil {
		return "", err
	}

	return interpolateKnownScriptVars(script, outputVarsWithValues(proc, bindings, values, params), extValues)
}

func renderShellSection(proc *Process, bindings []string, params map[string]any) (string, error) {
	vars := outputVars(proc, bindings, params)

	section := strings.TrimSpace(proc.Shell)
	if section == "" {
		return "", nil
	}

	if strings.HasPrefix(section, "[") {
		return renderShellSectionList(section, vars)
	}

	interpolated, err := interpolateShellSection(section, vars)
	if err != nil {
		return "", err
	}

	return strings.Join([]string{"set -euo pipefail", interpolated}, "\n"), nil
}

func outputVars(proc *Process, bindings []string, params map[string]any) map[string]any {
	return outputVarsWithValues(proc, bindings, nil, params)
}

func renderShellSectionList(section string, vars map[string]any) (string, error) {
	expr, err := parseShellSectionExpr(section)
	if err != nil {
		return "", err
	}

	resolved, err := EvalExpr(expr, vars)
	if err != nil {
		return "", err
	}

	parts, ok := resolved.([]any)
	if !ok {
		return "", errors.New("shell section list must evaluate to a list")
	}

	if len(parts) == 0 {
		return "", errors.New("shell section list must not be empty")
	}

	stringParts := make([]string, 0, len(parts))
	for _, part := range parts {
		stringPart, ok := part.(string)
		if !ok {
			return "", errors.New("shell section list entries must evaluate to strings")
		}

		stringParts = append(stringParts, stringPart)
	}

	scriptBody, err := interpolateShellSection(stringParts[len(stringParts)-1], vars)
	if err != nil {
		return "", err
	}

	command := append([]string{}, stringParts[:len(stringParts)-1]...)
	command = append(command, "-c", shellQuote(scriptBody))

	return strings.Join(command, " "), nil
}

func parseShellSectionExpr(section string) (Expr, error) {
	tokens, err := lex(section)
	if err != nil {
		return nil, err
	}

	trimmed := trimDeclarationTokens(tokens)
	if len(trimmed) > 0 && trimmed[len(trimmed)-1].typ == tokenEOF {
		trimmed = trimmed[:len(trimmed)-1]
	}

	if len(trimmed) == 0 {
		return nil, errors.New("expected shell section expression")
	}

	return parseExprTokens(trimmed)
}

// interpolateShellSection replaces !{expr} with evaluated values, leaving
// ${...} sequences untouched.
func interpolateShellSection(shell string, vars map[string]any) (string, error) {
	matches := shellSectionInterpolationPattern.FindAllStringSubmatchIndex(shell, -1)
	if len(matches) == 0 {
		return shell, nil
	}

	var builder strings.Builder

	last := 0

	for _, match := range matches {
		builder.WriteString(shell[last:match[0]])

		exprText := strings.TrimSpace(shell[match[2]:match[3]])

		resolved, err := evalShellInterpolationExpr(exprText, vars)
		if err != nil {
			return "", err
		}

		builder.WriteString(fmt.Sprint(resolved))

		last = match[1]
	}

	builder.WriteString(shell[last:])

	return builder.String(), nil
}

func evalShellInterpolationExpr(exprText string, vars map[string]any) (any, error) {
	expr, err := parseShellSectionExpr(exprText)
	if err != nil {
		return nil, err
	}

	resolved, err := EvalExpr(expr, vars)
	if err != nil {
		return nil, err
	}

	if unsupported, ok := resolved.(UnsupportedExpr); ok {
		return nil, fmt.Errorf("unsupported shell interpolation %q", unsupported.Text)
	}

	return resolved, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func resolveExtDirectiveWithValues(proc *Process, defaults *ProcessDefaults, bindings []string, values []any, params map[string]any) (map[string]any, error) {
	if proc == nil {
		return nil, nil
	}

	merged, err := extDirectiveSourceMap(proc.Directives["ext"])
	if err != nil {
		return nil, err
	}

	if defaults != nil {
		merged = mergeExtValues(merged, defaults.Ext)
	}

	if len(merged) == 0 {
		return nil, nil
	}

	vars := outputVarsWithValues(proc, bindings, values, params)

	resolved := make(map[string]any, len(merged))
	for key, value := range merged {
		resolvedValue, err := resolveExtValue(value, vars)
		if err != nil {
			return nil, err
		}

		if unsupported, ok := resolvedValue.(UnsupportedExpr); ok {
			warnf("nextflowdsl: falling back for ext.%s with unsupported expression %q\n", key, unsupported.Text)

			continue
		}

		resolved[key] = resolvedValue
	}

	return resolved, nil
}

func resolveExtValue(value any, vars map[string]any) (any, error) {
	switch typed := value.(type) {
	case ClosureExpr:
		if len(typed.Params) != 0 {
			return UnsupportedExpr{Text: renderExpr(typed)}, nil
		}

		return evalStatementBody(typed.Body, cloneEvalVars(vars))
	case Expr:
		return EvalExpr(typed, vars)
	case map[string]any:
		resolved := make(map[string]any, len(typed))
		for key, item := range typed {
			value, err := resolveExtValue(item, vars)
			if err != nil {
				return nil, err
			}

			resolved[key] = value
		}

		return resolved, nil
	case []any:
		resolved := make([]any, len(typed))
		for index, item := range typed {
			value, err := resolveExtValue(item, vars)
			if err != nil {
				return nil, err
			}

			resolved[index] = value
		}

		return resolved, nil
	default:
		return cloneExtValue(typed), nil
	}
}

func interpolateKnownScriptVars(script string, vars map[string]any, extValues map[string]any) (string, error) {
	if len(vars) == 0 && len(extValues) == 0 && !strings.Contains(script, "${task.ext.") {
		return script, nil
	}

	matches := groovyInterpolationPattern.FindAllStringSubmatchIndex(script, -1)
	if len(matches) == 0 {
		return script, nil
	}

	var builder strings.Builder

	last := 0

	for _, match := range matches {
		builder.WriteString(script[last:match[0]])

		exprText := strings.TrimSpace(script[match[2]:match[3]])
		if strings.HasPrefix(exprText, "task.ext.") {
			builder.WriteString(taskExtInterpolationValue(exprText, extValues))

			last = match[1]

			continue
		}

		root, _, _ := strings.Cut(exprText, ".")
		if _, ok := vars[root]; !ok {
			builder.WriteString(script[match[0]:match[1]])
			last = match[1]

			continue
		}

		resolved, err := resolveInterpolation(exprText, vars)
		if err != nil {
			return "", err
		}

		builder.WriteString(fmt.Sprint(resolved))

		last = match[1]
	}

	builder.WriteString(script[last:])

	return builder.String(), nil
}

func taskExtInterpolationValue(exprText string, extValues map[string]any) string {
	if len(extValues) == 0 {
		return ""
	}

	path, ok := strings.CutPrefix(exprText, "task.ext.")
	if !ok || path == "" {
		return ""
	}

	current := any(extValues)
	for _, part := range strings.Split(path, ".") {
		next, ok := current.(map[string]any)
		if !ok {
			return ""
		}

		value, ok := next[part]
		if !ok || value == nil {
			return ""
		}

		current = value
	}

	return fmt.Sprint(current)
}

func evalOutputCaptureLines(proc *Process, bindings []string, params map[string]any) ([]string, error) {
	if proc == nil || len(proc.Output) == 0 {
		return nil, nil
	}

	vars := outputVars(proc, bindings, params)
	lines := make([]string, 0, len(proc.Output))
	evalIndex := 0

	for _, decl := range proc.Output {
		if decl == nil || decl.Kind != "eval" {
			continue
		}

		command, err := evalOutputCommand(decl, vars, evalIndex)
		if err != nil {
			return nil, err
		}

		lines = append(lines, fmt.Sprintf("__nf_eval_%d=$(%s)", evalIndex, command))
		evalIndex++
	}

	return lines, nil
}

func evalOutputCommand(decl *Declaration, vars map[string]any, index int) (string, error) {
	if decl == nil || decl.Expr == nil {
		return "", fmt.Errorf("eval output %d requires an expression", index)
	}

	resolved, err := EvalExpr(decl.Expr, vars)
	if err != nil {
		return "", fmt.Errorf("resolve eval output %d: %w", index, err)
	}

	command, ok := resolved.(string)
	if !ok {
		return "", fmt.Errorf("eval output %d must resolve to a string", index)
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("eval output %d must not be empty", index)
	}

	return command, nil
}

func moduleLoadLines(moduleDirective string) []string {
	if strings.TrimSpace(moduleDirective) == "" {
		return nil
	}

	modules := strings.Split(moduleDirective, ":")

	lines := make([]string, 0, len(modules))
	for _, module := range modules {
		module = strings.TrimSpace(module)
		if module == "" {
			continue
		}

		lines = append(lines, "module load "+module)
	}

	return lines
}

func captureCommand(body, stdoutPath, stderrPath string) string {
	if strings.HasSuffix(body, "\n") {
		return fmt.Sprintf("{ %s} > %s 2> %s", body, stdoutPath, stderrPath)
	}

	return fmt.Sprintf("{ %s; } > %s 2> %s", body, stdoutPath, stderrPath)
}

func resolveScratchDirective(proc *Process, params map[string]any) (bool, string, error) {
	if proc == nil {
		return false, "", nil
	}

	value, fallbackUsed, err := resolveDirectiveValue("scratch", proc.Directives["scratch"], params, nil)
	if err != nil {
		return false, "", err
	}

	if fallbackUsed || value == nil {
		return false, "", nil
	}

	switch resolved := value.(type) {
	case bool:
		return resolved, "", nil
	case string:
		resolved = strings.TrimSpace(resolved)

		return resolved != "", resolved, nil
	default:
		return false, "", errors.New("scratch directive must evaluate to a boolean or string")
	}
}

func wrapScratchCommand(
	body string,
	proc *Process,
	bindings []string,
	params map[string]any,
	scratchPath string,
) string {
	patterns := outputPatterns(proc, bindings, params)
	assignScratch := "nf_scratch_dir=$(mktemp -d)"
	cleanup := "rm -rf -- \"$nf_scratch_dir\""

	if scratchPath != "" {
		assignScratch = "nf_scratch_dir=" + shellQuote(filepath.Clean(scratchPath))
		cleanup = ":"
	}

	copyBack := copyOutputCommands(patterns, "$nf_scratch_dir", "$nf_orig_dir", true, true)
	if len(copyBack) == 0 {
		copyBack = []string{":"}
	}

	lines := []string{
		"nf_orig_dir=$PWD",
		assignScratch,
		"mkdir -p \"$nf_scratch_dir\"",
		fmt.Sprintf(
			"( cd \"$nf_scratch_dir\" || exit 1\n%s )",
			captureCommand(body, "\"$nf_orig_dir\"/"+nfStdoutFile, "\"$nf_orig_dir\"/"+nfStderrFile),
		),
		"nf_status=$?",
		"if [ \"$nf_status\" -eq 0 ]; then",
	}
	lines = append(lines, copyBack...)
	lines = append(lines,
		"fi",
		cleanup,
		"(exit \"$nf_status\")",
	)

	return strings.Join(lines, "\n")
}

func outputPatterns(proc *Process, bindings []string, params map[string]any) []string {
	if proc == nil || len(proc.Output) == 0 {
		return nil
	}

	vars := outputVars(proc, bindings, params)
	patterns := []string{}

	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		switch decl.Kind {
		case "path", "file":
			pattern, ok := resolvedOutputPatternString(decl.Expr, decl.Name, vars)
			if ok {
				patterns = appendUniqueStrings(patterns, []string{pattern})
			}
		case "tuple":
			for _, element := range decl.Elements {
				if element == nil || (element.Kind != "path" && element.Kind != "file") {
					continue
				}

				pattern, ok := resolvedOutputPatternString(element.Expr, element.Name, vars)
				if ok {
					patterns = appendUniqueStrings(patterns, []string{pattern})
				}
			}
		}
	}

	return patterns
}

func resolvedOutputPatternString(expr Expr, name string, vars map[string]any) (string, bool) {
	pattern := ""

	if expr != nil {
		value, err := EvalExpr(expr, vars)
		if err == nil {
			pattern = fmt.Sprint(value)
		} else if stringExpr, ok := expr.(StringExpr); ok {
			pattern = stringExpr.Value
		}
	}

	if pattern == "" && name != "" {
		if value, ok := vars[name]; ok {
			pattern = fmt.Sprint(value)
		} else {
			pattern = name
		}
	}

	if pattern == "" {
		return "", false
	}

	return filepath.Clean(pattern), true
}

func copyOutputCommands(patterns []string, sourceBase, targetDir string, dynamicSource, dynamicTarget bool) []string {
	commands := make([]string, 0, len(patterns)*2)
	for _, pattern := range patterns {
		sourcePattern := shellPatternFromBase(sourceBase, pattern)

		target := shellQuote(targetDir)
		if dynamicTarget {
			target = fmt.Sprintf("\"%s\"", targetDir)
		}

		sourceCheck := shellQuote(sourcePattern)

		sourceCopy := shellQuote(sourcePattern)
		if dynamicSource {
			sourceCheck = fmt.Sprintf("\"%s\"", sourcePattern)
			sourceCopy = fmt.Sprintf("\"%s\"", sourcePattern)
		}

		if strings.ContainsAny(pattern, "*?[") {
			commands = append(commands, fmt.Sprintf(
				"if ( set -- %s; [ -e \"$1\" ] || [ -L \"$1\" ] ); then for path in %s; do cp -rf -- \"$path\" %s; done; fi",
				sourcePattern,
				sourcePattern,
				target,
			))

			continue
		}

		commands = append(commands, fmt.Sprintf(
			"if [ -e %s ] || [ -L %s ]; then cp -rf -- %s %s; fi",
			sourceCheck,
			sourceCheck,
			sourceCopy,
			target,
		))
	}

	return commands
}

func shellPatternFromBase(base, pattern string) string {
	cleanPattern := filepath.Clean(pattern)
	if filepath.IsAbs(cleanPattern) {
		return cleanPattern
	}

	return filepath.Join(base, cleanPattern)
}

func prependEnvironmentDirectives(command string, proc *Process, params map[string]any) (string, error) {
	if proc == nil {
		return command, nil
	}

	prefixes := make([]string, 0, 2)

	condaEnv, err := resolveDirectiveString("conda", proc.Directives["conda"], params, "", nil)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(condaEnv) != "" {
		prefixes = append(prefixes, "conda activate "+condaEnv)
	}

	spackPkg, err := resolveDirectiveString("spack", proc.Directives["spack"], params, "", nil)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(spackPkg) != "" {
		prefixes = append(prefixes, "spack load "+spackPkg)
	}

	if len(prefixes) == 0 {
		return command, nil
	}

	prefixes = append(prefixes, command)

	return strings.Join(prefixes, " && "), nil
}

func resolveStoreDirDirective(proc *Process, params map[string]any, launchCwd string) (string, bool, error) {
	if proc == nil {
		return "", false, nil
	}

	storeDir, err := resolveDirectiveString("storeDir", proc.Directives["storeDir"], params, "", nil)
	if err != nil {
		return "", false, err
	}

	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return "", false, nil
	}

	if filepath.IsAbs(storeDir) {
		return filepath.Clean(storeDir), true, nil
	}

	return filepath.Clean(filepath.Join(launchCwd, storeDir)), true, nil
}

func wrapStoreDirCommand(command string, proc *Process, bindings []string, params map[string]any, cwd, storeDir string) string {
	patterns := outputPatterns(proc, bindings, params)

	existenceCheck := outputExistenceCondition(patterns, storeDir)
	if existenceCheck == "" {
		existenceCheck = "false"
	}

	copyFromStore := copyOutputCommands(patterns, storeDir, ensureTrailingSeparator(cwd), false, false)
	if len(copyFromStore) == 0 {
		copyFromStore = []string{":"}
	}

	copyToStore := copyOutputCommands(patterns, cwd, ensureTrailingSeparator(storeDir), false, false)
	if len(copyToStore) == 0 {
		copyToStore = []string{":"}
	}

	lines := []string{
		"mkdir -p " + shellQuote(ensureTrailingSeparator(storeDir)),
		fmt.Sprintf("if %s; then", existenceCheck),
	}
	lines = append(lines, copyFromStore...)
	lines = append(lines,
		"else",
		command,
		"nf_status=$?",
		"if [ \"$nf_status\" -eq 0 ]; then",
	)
	lines = append(lines, copyToStore...)
	lines = append(lines,
		"fi",
		"(exit \"$nf_status\")",
		"fi",
	)

	return strings.Join(lines, "\n")
}

func outputExistenceCondition(patterns []string, storeDir string) string {
	checks := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		storePattern := shellPatternFromBase(storeDir, pattern)
		if strings.ContainsAny(pattern, "*?[") {
			checks = append(checks, fmt.Sprintf("( set -- %s; [ -e \"$1\" ] || [ -L \"$1\" ] )", storePattern))

			continue
		}

		checks = append(checks, fmt.Sprintf("[ -e %s ]", shellQuote(storePattern)))
	}

	return strings.Join(checks, " && ")
}

func ensureTrailingSeparator(path string) string {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}

	return path + string(os.PathSeparator)
}

func applyCaptureCleanupBehaviour(job *jobqueue.Job) {
	job.Behaviours = append(job.Behaviours, &jobqueue.Behaviour{
		When: jobqueue.OnExit,
		Do:   jobqueue.Run,
		Arg: fmt.Sprintf(
			`for f in %s %s; do if [ -f "$f" ] && ! grep -qP '\S' "$f"; then rm -f "$f"; fi; done`,
			nfStdoutFile,
			nfStderrFile,
		),
	})
}

func outputPaths(proc *Process, bindings []string, params map[string]any, cwd string) []string {
	vars := outputVars(proc, bindings, params)
	paths := []string{}

	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		switch decl.Kind {
		case "path", "file":
			pattern, ok := resolvedOutputPattern(decl.Expr, decl.Name, vars, cwd)
			if ok {
				paths = append(paths, pattern)
			}
		case "tuple":
			for _, element := range decl.Elements {
				if element == nil || (element.Kind != "path" && element.Kind != "file") {
					continue
				}

				pattern, ok := resolvedOutputPattern(element.Expr, element.Name, vars, cwd)
				if ok {
					paths = append(paths, pattern)
				}
			}
		}
	}

	if len(paths) > 0 {
		return paths
	}

	return []string{cwd + string(os.PathSeparator)}
}

func resolvedOutputPattern(expr Expr, name string, vars map[string]any, cwd string) (string, bool) {
	pattern := ""

	if expr != nil {
		value, err := EvalExpr(expr, vars)
		if err == nil {
			pattern = fmt.Sprint(value)
		} else if stringExpr, ok := expr.(StringExpr); ok {
			pattern = stringExpr.Value
		}
	}

	if pattern == "" && name != "" {
		if value, ok := vars[name]; ok {
			pattern = fmt.Sprint(value)
		} else {
			pattern = name
		}
	}

	if pattern == "" {
		return "", false
	}

	cleanPath := filepath.Clean(pattern)
	if filepath.IsAbs(cleanPath) {
		return cleanPath, true
	}

	return filepath.Join(cwd, cleanPath), true
}

func outputValue(proc *Process, bindings []string, params map[string]any, cwd string, fallbackPaths []string) any {
	if proc == nil || len(proc.Output) == 0 {
		return channelItemValue(fallbackPaths)
	}

	values := make([]any, 0, len(proc.Output))
	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		if decl.Kind == "tuple" {
			if value, ok := tupleOutputValue(proc, decl, bindings, params, cwd); ok {
				values = append(values, value)
			}

			continue
		}

		if decl.Kind == "path" || decl.Kind == "file" {
			continue
		}

		if value, ok := staticOutputValue(proc, decl, bindings, params); ok {
			values = append(values, value)
		}
	}

	if len(values) == 0 {
		return channelItemValue(fallbackPaths)
	}

	if len(values) == 1 {
		return values[0]
	}

	return values
}

func tupleOutputValue(proc *Process, decl *Declaration, bindings []string, params map[string]any, cwd string) (any, bool) {
	if decl == nil || decl.Kind != "tuple" || len(decl.Elements) == 0 {
		return nil, false
	}

	values := make([]any, 0, len(decl.Elements))
	vars := outputVars(proc, bindings, params)

	for _, element := range decl.Elements {
		if element == nil {
			continue
		}

		switch element.Kind {
		case "path", "file":
			pattern, ok := resolvedOutputPattern(element.Expr, element.Name, vars, cwd)
			if !ok {
				return nil, false
			}

			values = append(values, pattern)
		default:
			value, ok := staticOutputValue(proc, declarationFromTupleElement(element), bindings, params)
			if !ok {
				return nil, false
			}

			values = append(values, value)
		}
	}

	if len(values) == 0 {
		return nil, false
	}

	return values, true
}

func staticOutputValue(proc *Process, decl *Declaration, bindings []string, params map[string]any) (any, bool) {
	vars := outputVars(proc, bindings, params)

	if decl.Expr != nil {
		value, err := EvalExpr(decl.Expr, vars)
		if err == nil {
			return value, true
		}
	}

	if decl.Name != "" {
		if value, ok := vars[decl.Name]; ok {
			return value, true
		}
	}

	if len(bindings) == 1 {
		return bindings[0], true
	}

	if decl.Kind == "val" {
		return "", true
	}

	return nil, false
}

func mergeEmittedOutputs(
	existing map[string]emittedOutput,
	incoming map[string]emittedOutput,
) map[string]emittedOutput {
	if len(incoming) == 0 {
		return existing
	}

	if existing == nil {
		existing = map[string]emittedOutput{}
	}

	for label, output := range incoming {
		current := existing[label]
		current.outputPaths = appendUniqueStrings(current.outputPaths, output.outputPaths)
		current.items = append(current.items, cloneChannelItems(output.items)...)
		existing[label] = current
	}

	return existing
}

func emitOutputsForProcess(proc *Process, bindings []string, params map[string]any, cwd, depGroup string) map[string]emittedOutput {
	if proc == nil || len(proc.Output) == 0 {
		return nil
	}

	outputs := map[string]emittedOutput{}

	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		addEmittedOutput(outputs, decl.Emit, proc, decl, bindings, params, cwd, depGroup)

		if decl.Kind != "tuple" {
			continue
		}

		for _, element := range decl.Elements {
			if element == nil || element.Emit == "" {
				continue
			}

			addEmittedOutput(outputs, element.Emit, proc, declarationFromTupleElement(element), bindings, params, cwd, depGroup)
		}
	}

	if len(outputs) == 0 {
		return nil
	}

	return outputs
}

func addEmittedOutput(
	outputs map[string]emittedOutput,
	label string,
	proc *Process,
	decl *Declaration,
	bindings []string,
	params map[string]any,
	cwd string,
	depGroup string,
) {
	if label == "" || decl == nil {
		return
	}

	current := outputs[label]
	paths := outputPathsForDeclaration(proc, decl, bindings, params, cwd)

	current.outputPaths = appendUniqueStrings(current.outputPaths, paths)
	if value, ok := outputValueForDeclaration(proc, decl, bindings, params, cwd, paths); ok {
		current.items = append(current.items, channelItem{value: value, depGroups: []string{depGroup}})
	}

	outputs[label] = current
}

func outputPathsForDeclaration(proc *Process, decl *Declaration, bindings []string, params map[string]any, cwd string) []string {
	if decl == nil {
		return nil
	}

	vars := outputVars(proc, bindings, params)
	paths := []string{}

	switch decl.Kind {
	case "path", "file":
		pattern, ok := resolvedOutputPattern(decl.Expr, decl.Name, vars, cwd)
		if ok {
			return []string{pattern}
		}
	case "tuple":
		for _, element := range decl.Elements {
			if element == nil || (element.Kind != "path" && element.Kind != "file") {
				continue
			}

			pattern, ok := resolvedOutputPattern(element.Expr, element.Name, vars, cwd)
			if ok {
				paths = append(paths, pattern)
			}
		}
	}

	return paths
}

func outputValueForDeclaration(
	proc *Process,
	decl *Declaration,
	bindings []string,
	params map[string]any,
	cwd string,
	fallbackPaths []string,
) (any, bool) {
	if decl == nil {
		return nil, false
	}

	switch decl.Kind {
	case "tuple":
		return tupleOutputValue(proc, decl, bindings, params, cwd)
	case "path", "file":
		if len(fallbackPaths) == 0 {
			return nil, false
		}

		return channelItemValue(fallbackPaths), true
	default:
		return staticOutputValue(proc, decl, bindings, params)
	}
}

func cloneSelectors(selectors []*ProcessSelector) []*ProcessSelector {
	if len(selectors) == 0 {
		return nil
	}

	clone := make([]*ProcessSelector, len(selectors))
	copy(clone, selectors)

	return clone
}

func mergeTranslateParams(wf *Workflow, cfg *Config, tc TranslateConfig) (map[string]any, error) {
	var sources []map[string]any

	workflowDefaults, err := workflowParamDefaults(wf)
	if err != nil {
		return nil, err
	}

	if len(workflowDefaults) > 0 {
		sources = append(sources, workflowDefaults)
	}

	if cfg != nil && len(cfg.Params) > 0 {
		sources = append(sources, cfg.Params)
	}

	if cfg != nil && tc.Profile != "" && cfg.Profiles != nil {
		if profile, ok := cfg.Profiles[tc.Profile]; ok && len(profile.Params) > 0 {
			sources = append(sources, profile.Params)
		}
	}

	if tc.Params != nil {
		sources = append(sources, tc.Params)
	}

	if len(sources) == 0 {
		return nil, nil
	}

	return MergeParams(sources...), nil
}

func workflowParamDefaults(wf *Workflow) (map[string]any, error) {
	if wf == nil || len(wf.ParamBlock) == 0 {
		return nil, nil
	}

	defaults := map[string]any{}

	for _, decl := range wf.ParamBlock {
		if decl == nil || decl.Default == nil {
			continue
		}

		value, err := EvalExpr(decl.Default, exprVarsWithTask(defaults, nil))
		if err != nil {
			fallback, ok := workflowParamDefaultFallback(decl.Default)
			if !ok {
				return nil, fmt.Errorf("evaluate workflow param default %q: %w", decl.Name, err)
			}

			warnf(
				"nextflowdsl: unable to evaluate workflow param default %q; using expression-text fallback %q\n",
				decl.Name,
				fallback,
			)
			value = fallback
		}

		if unsupported, ok := value.(UnsupportedExpr); ok {
			warnf(
				"nextflowdsl: unable to evaluate workflow param default %q; using expression-text fallback %q\n",
				decl.Name,
				unsupported.Text,
			)
			value = unsupported.Text
		}

		defaults = MergeParams(defaults, paramValueAtPath(decl.Name, normalizeParamValue(value)))
	}

	if len(defaults) == 0 {
		return nil, nil
	}

	return defaults, nil
}

func workflowParamDefaultFallback(expr Expr) (string, bool) {
	switch typed := expr.(type) {
	case NewExpr:
		return renderNewExpr(typed), true
	default:
		return "", false
	}
}

func paramValueAtPath(path string, value any) map[string]any {
	parts := strings.Split(path, ".")
	root := map[string]any{}
	current := root

	for index, part := range parts {
		if index == len(parts)-1 {
			current[part] = value

			break
		}

		next := map[string]any{}
		current[part] = next
		current = next
	}

	return root
}

func translateBlock(
	block *WorkflowBlock,
	scope []string,
	processes map[string]*Process,
	subworkflows map[string]*SubWorkflow,
	translated map[string]translatedCall,
	outputTargets map[string]*OutputTarget,
	defaults *ProcessDefaults,
	selectors []*ProcessSelector,
	params map[string]any,
	tc TranslateConfig,
	result *TranslateResult,
) error {
	if block == nil {
		return nil
	}

	if err := translateCalls(
		block.Calls,
		scope,
		processes,
		subworkflows,
		translated,
		outputTargets,
		defaults,
		selectors,
		params,
		tc,
		result,
	); err != nil {
		return err
	}

	for index, ifBlock := range block.Conditions {
		if err := translateConditionalBlock(
			ifBlock,
			index,
			scope,
			processes,
			subworkflows,
			translated,
			outputTargets,
			defaults,
			selectors,
			params,
			tc,
			result,
		); err != nil {
			return err
		}
	}

	if err := translateWorkflowPublishes(block, scope, translated, outputTargets, params, tc, result); err != nil {
		return err
	}

	if err := translateLifecycleHooks(block, scope, translated, params, tc, result); err != nil {
		return err
	}

	return nil
}

func translateConditionalBlock(
	ifBlock *IfBlock,
	index int,
	scope []string,
	processes map[string]*Process,
	subworkflows map[string]*SubWorkflow,
	translated map[string]translatedCall,
	outputTargets map[string]*OutputTarget,
	defaults *ProcessDefaults,
	selectors []*ProcessSelector,
	params map[string]any,
	tc TranslateConfig,
	result *TranslateResult,
) error {
	if ifBlock == nil {
		return nil
	}

	selected, err := selectWorkflowConditionBranch(ifBlock, index, params)
	if err == nil {
		if selected == nil || len(selected.calls) == 0 {
			return nil
		}

		return translateCalls(
			selected.calls,
			scope,
			processes,
			subworkflows,
			translated,
			outputTargets,
			defaults,
			selectors,
			params,
			tc,
			result,
		)
	}

	warnf("nextflowdsl: unable to evaluate workflow condition %q, translating all branches\n", ifBlock.Condition)

	for _, branch := range workflowConditionBranches(ifBlock, index) {
		if len(branch.calls) == 0 {
			continue
		}

		branchScope := append(append([]string{}, scope...), branch.scopeName)
		if err := translateCalls(
			branch.calls,
			branchScope,
			processes,
			subworkflows,
			translated,
			outputTargets,
			defaults,
			selectors,
			params,
			tc,
			result,
		); err != nil {
			return err
		}
	}

	return nil
}

func selectWorkflowConditionBranch(
	ifBlock *IfBlock,
	index int,
	params map[string]any,
) (*workflowConditionBranch, error) {
	for _, branch := range workflowConditionBranches(ifBlock, index) {
		if branch.condition == "" {
			selected := branch

			return &selected, nil
		}

		matched, err := evalWorkflowCondition(branch.condition, params)
		if err != nil {
			return nil, err
		}

		if matched {
			selected := branch

			return &selected, nil
		}
	}

	return nil, nil
}

func workflowConditionBranches(ifBlock *IfBlock, index int) []workflowConditionBranch {
	if ifBlock == nil {
		return nil
	}

	branches := []workflowConditionBranch{{
		scopeName: fmt.Sprintf("if_%d", index),
		condition: ifBlock.Condition,
		calls:     ifBlock.Body,
	}}

	for elseIfIndex, elseIf := range ifBlock.ElseIf {
		if elseIf == nil {
			continue
		}

		branches = append(branches, workflowConditionBranch{
			scopeName: fmt.Sprintf("elseif_%d_%d", index, elseIfIndex),
			condition: elseIf.Condition,
			calls:     elseIf.Body,
		})
	}

	branches = append(branches, workflowConditionBranch{
		scopeName: fmt.Sprintf("else_%d", index),
		calls:     ifBlock.ElseBody,
	})

	return branches
}

func evalWorkflowCondition(condition string, params map[string]any) (bool, error) {
	expr, err := parseExprText(condition)
	if err != nil {
		return false, err
	}

	value, err := EvalExpr(expr, exprVarsWithTask(params, nil))
	if err != nil {
		return false, err
	}

	return isTruthy(value), nil
}

func translateCalls(
	calls []*Call,
	scope []string,
	processes map[string]*Process,
	subworkflows map[string]*SubWorkflow,
	translated map[string]translatedCall,
	outputTargets map[string]*OutputTarget,
	defaults *ProcessDefaults,
	selectors []*ProcessSelector,
	params map[string]any,
	tc TranslateConfig,
	result *TranslateResult,
) error {
	for _, call := range calls {
		if call == nil {
			continue
		}

		if proc, ok := processes[call.Target]; ok {
			awaitDepGrps, awaitRepGrps, err := detectPendingInputs(call, scope, translated)
			if err != nil {
				return err
			}

			if len(awaitDepGrps) > 0 || len(awaitRepGrps) > 0 || strings.TrimSpace(proc.When) != "" {
				repGroup := scopedRepGroup(tc.WorkflowName, tc.RunID, scope, proc.Name)
				depGroup := scopedDepGroup(tc.RunID, scope, proc.Name)
				placeholderPaths := outputPaths(proc, nil, params, deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name))
				placeholderCwd := deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name)
				translated[scopedTargetKey(scope, call.Target)] = translatedCall{
					depGroup:    depGroup,
					depGroups:   []string{depGroup},
					repGroup:    repGroup,
					baseCwd:     placeholderCwd,
					outputPaths: placeholderPaths,
					emitOutputs: emitOutputsForProcess(proc, nil, params, placeholderCwd, depGroup),
					items: []channelItem{{
						value:     outputValue(proc, nil, params, placeholderCwd, placeholderPaths),
						depGroups: []string{depGroup},
					}},
					dynamicOutput: hasDynamicOutputs(proc),
					pending:       true,
				}
				result.Pending = append(result.Pending, &PendingStage{
					Process:      proc,
					AwaitDepGrps: awaitDepGrps,
					call:         call,
					scope:        append([]string{}, scope...),
					defaults:     cloneDefaults(defaults),
					selectors:    cloneSelectors(selectors),
					params:       cloneParams(params),
					translated:   cloneTranslatedCalls(translated),
					awaitRepGrps: cloneStrings(awaitRepGrps),
				})

				continue
			}

			jobs, stage, err := translateProcessCall(proc, call, scope, translated, defaults, selectors, params, tc)
			if err != nil {
				return err
			}

			if len(jobs) == 0 {
				continue
			}

			stage.repGroup = jobs[0].RepGroup
			stage.dynamicOutput = hasDynamicOutputsForStage(proc, stage.outputPaths)
			translated[scopedTargetKey(scope, call.Target)] = stage

			result.Jobs = append(result.Jobs, jobs...)

			continue
		}

		subwf, ok := subworkflows[call.Target]
		if !ok {
			return fmt.Errorf("unknown process or subworkflow %q", call.Target)
		}

		nextScope := append(append([]string{}, scope...), call.Target)
		if err := bindSubworkflowInputs(call, subwf, scope, nextScope, translated, tc.Cwd); err != nil {
			return err
		}

		if err := translateBlock(
			subwf.Body,
			nextScope,
			processes,
			subworkflows,
			translated,
			outputTargets,
			defaults,
			selectors,
			params,
			tc,
			result,
		); err != nil {
			return err
		}

		if err := bindSubworkflowOutputs(call, subwf, nextScope, translated, tc.Cwd); err != nil {
			return err
		}
	}

	return nil
}

func detectPendingInputs(call *Call, scope []string, translated map[string]translatedCall) ([]string, []string, error) {
	if call == nil || len(call.Args) == 0 {
		return nil, nil, nil
	}

	awaitDepGrps := []string{}
	awaitRepGrps := []string{}
	seenDepGrps := make(map[string]struct{})
	seenRepGrps := make(map[string]struct{})

	for _, arg := range call.Args {
		depGrps, repGrps, err := detectPendingArg(arg, scope, translated)
		if err != nil {
			return nil, nil, err
		}

		for _, depGrp := range depGrps {
			if _, seen := seenDepGrps[depGrp]; seen {
				continue
			}

			awaitDepGrps = append(awaitDepGrps, depGrp)
			seenDepGrps[depGrp] = struct{}{}
		}

		for _, repGrp := range repGrps {
			if _, seen := seenRepGrps[repGrp]; seen {
				continue
			}

			awaitRepGrps = append(awaitRepGrps, repGrp)
			seenRepGrps[repGrp] = struct{}{}
		}
	}

	if len(awaitDepGrps) == 0 && len(awaitRepGrps) == 0 {
		return nil, nil, nil
	}

	return awaitDepGrps, awaitRepGrps, nil
}

func detectPendingArg(arg ChanExpr, scope []string, translated map[string]translatedCall) ([]string, []string, error) {
	switch expr := arg.(type) {
	case ChanRef:
		stage, ok := resolveTranslatedOutput(expr.Name, scope, translated)
		if !ok {
			return nil, nil, fmt.Errorf("unknown upstream reference %q", expr.Name)
		}

		if stage.skipped {
			return nil, nil, nil
		}

		if !stage.dynamicOutput && !stage.pending {
			return nil, nil, nil
		}

		if stage.pending {
			return nil, []string{stage.repGroup}, nil
		}

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		return deps, []string{stage.repGroup}, nil
	case NamedChannelRef:
		return detectPendingArg(expr.Source, scope, translated)
	case ChannelChain:
		if channelChainWaitsForAllItems(expr) {
			repGrps, err := translatedChannelExprRepGroups(expr, scope, translated)
			if err != nil {
				return nil, nil, err
			}

			if len(repGrps) > 0 {
				return nil, repGrps, nil
			}
		}

		depGrps, repGrps, err := detectPendingArg(expr.Source, scope, translated)
		if err != nil {
			return nil, nil, err
		}

		for _, operator := range expr.Operators {
			for _, channel := range operator.Channels {
				channelDepGrps, channelRepGrps, channelErr := detectPendingArg(channel, scope, translated)
				if channelErr != nil {
					return nil, nil, channelErr
				}

				depGrps = appendUniqueStrings(depGrps, channelDepGrps)
				repGrps = appendUniqueStrings(repGrps, channelRepGrps)
			}
		}

		return depGrps, repGrps, nil
	case PipeExpr:
		depGrps := []string{}
		repGrps := []string{}
		seenDepGrps := make(map[string]struct{})
		seenRepGrps := make(map[string]struct{})

		for _, stage := range expr.Stages {
			stageDepGrps, stageRepGrps, err := detectPendingArg(stage, scope, translated)
			if err != nil {
				if _, ok := stage.(ChanRef); ok {
					continue
				}

				return nil, nil, err
			}

			for _, depGrp := range stageDepGrps {
				if _, seen := seenDepGrps[depGrp]; seen {
					continue
				}

				depGrps = append(depGrps, depGrp)
				seenDepGrps[depGrp] = struct{}{}
			}

			for _, repGrp := range stageRepGrps {
				if _, seen := seenRepGrps[repGrp]; seen {
					continue
				}

				repGrps = append(repGrps, repGrp)
				seenRepGrps[repGrp] = struct{}{}
			}
		}

		return depGrps, repGrps, nil
	default:
		return nil, nil, nil
	}
}

func channelChainWaitsForAllItems(expr ChannelChain) bool {
	for _, operator := range expr.Operators {
		if operator.Name == "randomSample" {
			return true
		}
	}

	return false
}

func translatedChannelExprRepGroups(arg ChanExpr, scope []string, translated map[string]translatedCall) ([]string, error) {
	switch expr := arg.(type) {
	case ChanRef:
		stage, ok := resolveTranslatedOutput(expr.Name, scope, translated)
		if !ok {
			return nil, fmt.Errorf("unknown upstream reference %q", expr.Name)
		}

		return translatedStageRepGroups(stage), nil
	case NamedChannelRef:
		return translatedChannelExprRepGroups(expr.Source, scope, translated)
	case ChannelChain:
		repGrps, err := translatedChannelExprRepGroups(expr.Source, scope, translated)
		if err != nil {
			return nil, err
		}

		for _, operator := range expr.Operators {
			for _, channel := range operator.Channels {
				channelRepGrps, channelErr := translatedChannelExprRepGroups(channel, scope, translated)
				if channelErr != nil {
					return nil, channelErr
				}

				repGrps = appendUniqueStrings(repGrps, channelRepGrps)
			}
		}

		return repGrps, nil
	case PipeExpr:
		repGrps := []string{}

		for _, stage := range expr.Stages {
			stageRepGrps, err := translatedChannelExprRepGroups(stage, scope, translated)
			if err != nil {
				if _, ok := stage.(ChanRef); ok {
					continue
				}

				return nil, err
			}

			repGrps = appendUniqueStrings(repGrps, stageRepGrps)
		}

		return repGrps, nil
	default:
		return nil, nil
	}
}

func translatedStageRepGroups(stage translatedCall) []string {
	repGroups := cloneStrings(stage.repGroups)
	if len(repGroups) == 0 && stage.repGroup != "" {
		repGroups = []string{stage.repGroup}
	}

	return repGroups
}

func hasDynamicOutputs(proc *Process) bool {
	if proc == nil {
		return false
	}

	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		if decl.Kind == "path" || decl.Kind == "file" {
			return true
		}
	}

	return false
}

func translateProcessCall(
	proc *Process,
	call *Call,
	scope []string,
	translated map[string]translatedCall,
	defaults *ProcessDefaults,
	selectors []*ProcessSelector,
	params map[string]any,
	tc TranslateConfig,
) ([]*jobqueue.Job, translatedCall, error) {
	bindingSets, err := resolveBindings(proc, call, scope, translated, tc.Cwd)
	if err != nil {
		return nil, translatedCall{}, err
	}

	return translateProcessBindingSets(proc, bindingSets, scope, defaults, selectors, params, tc)
}

func hasDynamicOutputsForStage(proc *Process, outputPatterns []string) bool {
	if proc == nil {
		return false
	}

	hasTuplePaths := false

	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		if decl.Kind == "path" || decl.Kind == "file" {
			return true
		}

		if decl.Kind != "tuple" {
			continue
		}

		for _, element := range decl.Elements {
			if element != nil && (element.Kind == "path" || element.Kind == "file") {
				hasTuplePaths = true

				break
			}
		}
	}

	if !hasTuplePaths {
		return false
	}

	for _, pattern := range outputPatterns {
		if strings.ContainsAny(pattern, "*?[") || strings.Contains(pattern, "${") {
			return true
		}
	}

	return false
}

func bindSubworkflowInputs(
	call *Call,
	subwf *SubWorkflow,
	scope []string,
	nextScope []string,
	translated map[string]translatedCall,
	cwd string,
) error {
	if subwf == nil || subwf.Body == nil || len(subwf.Body.Take) == 0 {
		if len(call.Args) == 0 {
			return nil
		}

		return fmt.Errorf("subworkflow %q does not declare take inputs", call.Target)
	}

	if len(call.Args) != len(subwf.Body.Take) {
		return fmt.Errorf("subworkflow %q expects %d input(s), got %d", call.Target, len(subwf.Body.Take), len(call.Args))
	}

	for index, takeName := range subwf.Body.Take {
		stage, err := translatedCallForSubworkflowArg(call.Args[index], scope, translated, cwd)
		if err != nil {
			return fmt.Errorf("subworkflow %q input %q: %w", call.Target, takeName, err)
		}

		translated[scopedTargetKey(nextScope, takeName)] = stage
	}

	return nil
}

func translatedCallForSubworkflowArg(arg ChanExpr, scope []string, translated map[string]translatedCall, cwd string) (translatedCall, error) {
	if ref, ok := arg.(ChanRef); ok {
		stage, found := resolveTranslatedOutput(ref.Name, scope, translated)
		if !found {
			return translatedCall{}, fmt.Errorf("unknown upstream reference %q", ref.Name)
		}

		return cloneTranslatedCall(stage), nil
	}

	items, err := resolveTranslatedChannelItems(arg, scope, translated, cwd)
	if err == nil {
		stage := translatedCall{items: cloneChannelItems(items)}
		for _, item := range items {
			stage.depGroups = appendUniqueStrings(stage.depGroups, item.depGroups)
		}

		return stage, nil
	}

	paths, depGroups, resolveErr := resolveArg(arg, scope, translated)
	if resolveErr != nil {
		return translatedCall{}, resolveErr
	}

	stage := translatedCall{
		depGroups:   cloneStrings(depGroups),
		outputPaths: cloneStrings(paths),
	}
	if len(paths) > 0 || len(depGroups) > 0 {
		stage.items = []channelItem{{value: channelItemValue(paths), depGroups: cloneStrings(depGroups)}}
	}

	return stage, nil
}

func bindSubworkflowOutputs(
	call *Call,
	subwf *SubWorkflow,
	nextScope []string,
	translated map[string]translatedCall,
	cwd string,
) error {
	if subwf == nil || subwf.Body == nil || len(subwf.Body.Emit) == 0 {
		return nil
	}

	synthetic := translatedCall{
		emitOutputs: map[string]emittedOutput{},
		repGroups:   workflowBlockStageRepGroups(subwf.Body, nextScope, translated),
	}
	for _, emit := range subwf.Body.Emit {
		if emit == nil {
			continue
		}

		referenceText := emit.Expr
		if referenceText == "" {
			referenceText = emit.Name
		}

		expr, err := parseChanExprText(referenceText)
		if err != nil {
			return fmt.Errorf("subworkflow %q emit %q: %w", call.Target, emit.Name, err)
		}

		stage, err := translatedCallForSubworkflowArg(expr, nextScope, translated, cwd)
		if err != nil {
			return fmt.Errorf("subworkflow %q emit %q: %w", call.Target, emit.Name, err)
		}

		labelOutput := emittedOutput{
			outputPaths: cloneStrings(stage.outputPaths),
			items:       cloneChannelItems(stage.items),
		}
		synthetic.emitOutputs[emit.Name] = labelOutput
		synthetic.outputPaths = appendUniqueStrings(synthetic.outputPaths, labelOutput.outputPaths)
		synthetic.items = append(synthetic.items, cloneChannelItems(stage.items)...)

		synthetic.depGroups = appendUniqueStrings(synthetic.depGroups, stage.depGroups)
		if synthetic.depGroup == "" {
			synthetic.depGroup = stage.depGroup
		}

		if synthetic.repGroup == "" {
			synthetic.repGroup = stage.repGroup
		}

		synthetic.dynamicOutput = synthetic.dynamicOutput || stage.dynamicOutput
		synthetic.pending = synthetic.pending || stage.pending
	}

	if len(synthetic.emitOutputs) == 0 {
		return nil
	}

	translated[scopedTargetKey(nextScope[:len(nextScope)-1], call.Target)] = synthetic

	return nil
}

func workflowBlockStageRepGroups(block *WorkflowBlock, scope []string, translated map[string]translatedCall) []string {
	if block == nil {
		return nil
	}

	repGroups := []string{}
	for _, call := range block.Calls {
		repGroups = appendUniqueStrings(repGroups, translatedCallRepGroups(scopedTargetKey(scope, call.Target), translated))
	}

	for index, ifBlock := range block.Conditions {
		repGroups = appendUniqueStrings(repGroups, workflowConditionStageRepGroups(ifBlock, index, scope, translated))
	}

	return repGroups
}

func translatedCallRepGroups(key string, translated map[string]translatedCall) []string {
	stage, ok := translated[key]
	if !ok {
		return nil
	}

	return translatedStageRepGroups(stage)
}

func workflowConditionStageRepGroups(
	ifBlock *IfBlock,
	index int,
	scope []string,
	translated map[string]translatedCall,
) []string {
	branches := workflowConditionBranches(ifBlock, index)
	repGroups := []string{}

	for _, branch := range branches {
		branchScope := append(append([]string{}, scope...), branch.scopeName)
		for _, call := range branch.calls {
			repGroups = appendUniqueStrings(
				repGroups,
				translatedCallRepGroups(scopedTargetKey(scope, call.Target), translated),
			)
			repGroups = appendUniqueStrings(
				repGroups,
				translatedCallRepGroups(scopedTargetKey(branchScope, call.Target), translated),
			)
		}
	}

	return repGroups
}

func parseChanExprText(text string) (ChanExpr, error) {
	tokens, err := lex(text)
	if err != nil {
		return nil, err
	}

	parser := newParser(tokens, text)

	expr, err := parser.parseChanExpr(tokenEOF)
	if err != nil {
		return nil, err
	}

	if parser.current().typ != tokenEOF {
		return nil, fmt.Errorf("line %d: unexpected token %q", parser.current().line, parser.current().lit)
	}

	return expr, nil
}

func translateWorkflowPublishes(
	block *WorkflowBlock,
	scope []string,
	translated map[string]translatedCall,
	outputTargets map[string]*OutputTarget,
	params map[string]any,
	tc TranslateConfig,
	result *TranslateResult,
) error {
	if block == nil || len(block.Publish) == 0 {
		return nil
	}

	for _, publish := range block.Publish {
		if publish == nil {
			continue
		}

		target, ok := outputTargets[publish.Target]
		if !ok || target == nil {
			warnf("nextflowdsl: publish target %q not found in output block; skipping publish\n", publish.Target)

			continue
		}

		expr, err := parseChanExprText(publish.Source)
		if err != nil {
			return fmt.Errorf("workflow publish %q: %w", publish.Target, err)
		}

		stage, err := translatedCallForSubworkflowArg(expr, scope, translated, tc.Cwd)
		if err != nil {
			return fmt.Errorf("workflow publish %q: %w", publish.Target, err)
		}

		entries, err := attachWorkflowPublishBehaviours(result.Jobs, stage, target, params, tc)
		if err != nil {
			return fmt.Errorf("workflow publish %q: %w", publish.Target, err)
		}

		if target.IndexPath == "" {
			continue
		}

		job, err := newWorkflowPublishIndexJob(publish.Target, target.IndexPath, entries, scope, params, tc)
		if err != nil {
			return fmt.Errorf("workflow publish %q index: %w", publish.Target, err)
		}

		if job != nil {
			result.Jobs = append(result.Jobs, job)
		}
	}

	return nil
}

func attachWorkflowPublishBehaviours(
	jobs []*jobqueue.Job,
	stage translatedCall,
	target *OutputTarget,
	params map[string]any,
	tc TranslateConfig,
) ([]workflowPublishIndexEntry, error) {
	resolvedTarget, err := resolvePublishDirTarget(target.Path, params, tc)
	if err != nil {
		return nil, err
	}

	targetDir := ensureTrailingSeparator(resolvedTarget)

	jobsByDepGroup := make(map[string]*jobqueue.Job, len(jobs))
	for _, job := range jobs {
		if job == nil {
			continue
		}

		for _, depGroup := range job.DepGroups {
			jobsByDepGroup[depGroup] = job
		}
	}

	entries := []workflowPublishIndexEntry{}

	items := sourceItemsForWorkflowPublish(stage)
	if len(items) == 0 {
		items = []channelItem{{
			value:     cloneStrings(stage.outputPaths),
			depGroups: workflowPublishStageDepGroups(stage),
		}}
	}

	for _, item := range items {
		sources := workflowPublishSourcesForItem(item, stage)
		if len(sources) == 0 {
			continue
		}

		command := buildWorkflowPublishCommand(sources, targetDir)

		depGroups := cloneStrings(item.depGroups)
		if len(depGroups) == 0 {
			depGroups = workflowPublishStageDepGroups(stage)
		}

		for _, depGroup := range depGroups {
			job, ok := jobsByDepGroup[depGroup]
			if !ok {
				continue
			}

			job.Behaviours = append(job.Behaviours, &jobqueue.Behaviour{
				When: jobqueue.OnSuccess,
				Do:   jobqueue.Run,
				Arg:  command,
			})

			for _, source := range sources {
				entries = append(entries, workflowPublishIndexEntry{
					depGroup:    depGroup,
					source:      source,
					destination: filepath.Join(targetDir, filepath.Base(filepath.Clean(source))),
				})
			}

			break
		}
	}

	return entries, nil
}

func sourceItemsForWorkflowPublish(stage translatedCall) []channelItem {
	if len(stage.items) == 0 {
		return nil
	}

	return cloneChannelItems(stage.items)
}

func workflowPublishStageDepGroups(stage translatedCall) []string {
	depGroups := cloneStrings(stage.depGroups)
	if len(depGroups) == 0 && stage.depGroup != "" {
		depGroups = []string{stage.depGroup}
	}

	return depGroups
}

func workflowPublishSourcesForItem(item channelItem, stage translatedCall) []string {
	sources := collectWorkflowPublishSources(item.value, stage.outputPaths)
	if len(sources) > 0 {
		return sources
	}

	return cloneStrings(stage.outputPaths)
}

func collectWorkflowPublishSources(value any, candidates []string) []string {
	sources := []string{}

	switch typed := value.(type) {
	case string:
		sources = appendUniqueStrings(sources, matchCompletedOutputPaths(typed, candidates))
	case []string:
		for _, item := range typed {
			sources = appendUniqueStrings(sources, collectWorkflowPublishSources(item, candidates))
		}
	case []any:
		for _, item := range typed {
			sources = appendUniqueStrings(sources, collectWorkflowPublishSources(item, candidates))
		}
	}

	return sources
}

func buildWorkflowPublishCommand(sources []string, targetDir string) string {
	commands := make([]string, 0, len(sources)+1)

	commands = append(commands, "mkdir -p "+shellQuote(targetDir))
	for _, source := range sources {
		commands = append(commands, publishDirActionCommand("copy", shellQuote(source), targetDir))
	}

	return strings.Join(commands, " && ")
}

func publishDirActionCommand(mode, source, target string) string {
	switch mode {
	case "link":
		return fmt.Sprintf("ln -sfn -- %s %s", source, shellQuote(target))
	case "move":
		return fmt.Sprintf("mv -f -- %s %s", source, shellQuote(target))
	default:
		return fmt.Sprintf("cp -rf -- %s %s", source, shellQuote(target))
	}
}

func newWorkflowPublishIndexJob(
	targetName string,
	indexPath string,
	entries []workflowPublishIndexEntry,
	scope []string,
	params map[string]any,
	tc TranslateConfig,
) (*jobqueue.Job, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	depGroups := []string{}
	for _, entry := range entries {
		depGroups = appendUniqueStrings(depGroups, []string{entry.depGroup})
	}

	indexTarget, err := resolvePublishDirTarget(indexPath, params, tc)
	if err != nil {
		return nil, err
	}

	cmd := buildWorkflowPublishIndexCommand(indexTarget, entries)
	jobName := fmt.Sprintf("publish_%s_index", targetName)
	job := &jobqueue.Job{
		Cmd:          wrapWorkflowHookCommand(cmd),
		Cwd:          deterministicCwd(tc.Cwd, tc.RunID, scope, jobName),
		CwdMatters:   true,
		RepGroup:     scopedRepGroup(tc.WorkflowName, tc.RunID, scope, jobName),
		ReqGroup:     "nf.workflow.publish",
		DepGroups:    []string{scopedDepGroup(tc.RunID, scope, jobName)},
		Dependencies: depGroupsToDependencies(depGroups),
		Requirements: defaultWorkflowHookRequirements(),
		Override:     0,
	}
	applyCaptureCleanupBehaviour(job)

	return job, nil
}

func buildWorkflowPublishIndexCommand(indexTarget string, entries []workflowPublishIndexEntry) string {
	commands := []string{"mkdir -p " + shellQuote(filepath.Dir(indexTarget)), ": > " + shellQuote(indexTarget)}
	for _, entry := range entries {
		commands = append(commands, fmt.Sprintf(
			"printf '%s\t%s\\n' %s %s >> %s",
			"%s",
			"%s",
			shellQuote(entry.source),
			shellQuote(entry.destination),
			shellQuote(indexTarget),
		))
	}

	return strings.Join(commands, "\n")
}

func wrapWorkflowHookCommand(body string) string {
	body = strings.TrimRight(strings.TrimSpace(body), " \t\r")
	if body == "" {
		body = ":"
	}

	if strings.HasSuffix(body, "\n") {
		return fmt.Sprintf("{ %s} > %s 2> %s", body, nfStdoutFile, nfStderrFile)
	}

	return fmt.Sprintf("{ %s; } > %s 2> %s", body, nfStdoutFile, nfStderrFile)
}

func defaultWorkflowHookRequirements() *scheduler.Requirements {
	return &scheduler.Requirements{
		Cores:    defaultCPUs,
		CoresSet: true,
		RAM:      defaultMemory,
		Time:     time.Duration(defaultTime) * time.Minute,
		Disk:     defaultDisk,
		DiskSet:  true,
	}
}

func translateLifecycleHooks(
	block *WorkflowBlock,
	scope []string,
	translated map[string]translatedCall,
	params map[string]any,
	tc TranslateConfig,
	result *TranslateResult,
) error {
	if block == nil {
		return nil
	}

	stageDepGroups := workflowBlockStageDepGroups(block, scope, translated)
	stageRepGroups := workflowBlockStageRepGroups(block, scope, translated)

	if strings.TrimSpace(block.OnComplete) != "" {
		job, err := newWorkflowOnCompleteJob(block.OnComplete, stageDepGroups, scope, params, tc)
		if err != nil {
			return err
		}

		result.Jobs = append(result.Jobs, job)
	}

	if strings.TrimSpace(block.OnError) != "" {
		job, err := newWorkflowOnErrorJob(block.OnError, stageRepGroups, scope, params, tc)
		if err != nil {
			return err
		}

		result.Jobs = append(result.Jobs, job)
	}

	return nil
}

func workflowBlockStageDepGroups(block *WorkflowBlock, scope []string, translated map[string]translatedCall) []string {
	if block == nil {
		return nil
	}

	depGroups := []string{}
	for _, call := range block.Calls {
		depGroups = appendUniqueStrings(depGroups, translatedCallDepGroups(scopedTargetKey(scope, call.Target), translated))
	}

	for index, ifBlock := range block.Conditions {
		depGroups = appendUniqueStrings(depGroups, workflowConditionStageDepGroups(ifBlock, index, scope, translated))
	}

	return depGroups
}

func translatedCallDepGroups(key string, translated map[string]translatedCall) []string {
	stage, ok := translated[key]
	if !ok {
		return nil
	}

	depGroups := cloneStrings(stage.depGroups)
	if len(depGroups) == 0 && stage.depGroup != "" {
		depGroups = []string{stage.depGroup}
	}

	return depGroups
}

func workflowConditionStageDepGroups(
	ifBlock *IfBlock,
	index int,
	scope []string,
	translated map[string]translatedCall,
) []string {
	branches := workflowConditionBranches(ifBlock, index)
	depGroups := []string{}

	for _, branch := range branches {
		branchScope := append(append([]string{}, scope...), branch.scopeName)
		for _, call := range branch.calls {
			depGroups = appendUniqueStrings(
				depGroups,
				translatedCallDepGroups(scopedTargetKey(scope, call.Target), translated),
			)
			depGroups = appendUniqueStrings(
				depGroups,
				translatedCallDepGroups(scopedTargetKey(branchScope, call.Target), translated),
			)
		}
	}

	return depGroups
}

func newWorkflowOnCompleteJob(
	rawBody string,
	stageDepGroups []string,
	scope []string,
	params map[string]any,
	tc TranslateConfig,
) (*jobqueue.Job, error) {
	body, err := translateWorkflowHookBody(rawBody, params)
	if err != nil {
		return nil, err
	}

	job := &jobqueue.Job{
		Cmd:          wrapWorkflowHookCommand(body),
		Cwd:          deterministicCwd(tc.Cwd, tc.RunID, scope, "onComplete"),
		CwdMatters:   true,
		RepGroup:     scopedRepGroup(tc.WorkflowName, tc.RunID, scope, "onComplete"),
		ReqGroup:     "nf.workflow.onComplete",
		DepGroups:    []string{scopedDepGroup(tc.RunID, scope, "onComplete")},
		Dependencies: depGroupsToDependencies(stageDepGroups),
		Requirements: defaultWorkflowHookRequirements(),
		Override:     0,
	}
	applyCaptureCleanupBehaviour(job)

	return job, nil
}

func translateWorkflowHookBody(body string, params map[string]any) (string, error) {
	resolved, err := SubstituteParams(strings.TrimSpace(body), params)
	if err != nil {
		return "", err
	}

	if resolved == "" {
		return ":", nil
	}

	lines := strings.Split(resolved, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "println ") {
			lines[index] = strings.Replace(line, "println ", "echo ", 1)

			continue
		}

		lines[index] = strings.TrimRight(line, " \t\r")
	}

	translatedBody := strings.TrimSpace(strings.Join(lines, "\n"))
	if translatedBody == "" {
		return ":", nil
	}

	return translatedBody, nil
}

func newWorkflowOnErrorJob(
	rawBody string,
	stageRepGroups []string,
	scope []string,
	params map[string]any,
	tc TranslateConfig,
) (*jobqueue.Job, error) {
	body, err := translateWorkflowHookBody(rawBody, params)
	if err != nil {
		return nil, err
	}

	cmd := buildWorkflowOnErrorCommand(body, stageRepGroups, scope, tc)
	job := &jobqueue.Job{
		Cmd:          cmd,
		Cwd:          deterministicCwd(tc.Cwd, tc.RunID, scope, "onError"),
		CwdMatters:   true,
		RepGroup:     scopedRepGroup(tc.WorkflowName, tc.RunID, scope, "onError"),
		ReqGroup:     "nf.workflow.onError",
		DepGroups:    []string{scopedDepGroup(tc.RunID, scope, "onError")},
		Requirements: defaultWorkflowHookRequirements(),
		Override:     0,
	}
	applyCaptureCleanupBehaviour(job)

	return job, nil
}

func buildWorkflowOnErrorCommand(body string, stageRepGroups []string, scope []string, tc TranslateConfig) string {
	helperPath := ".wr-onerror-poll.sh"
	helper := buildWorkflowOnErrorHelperScript(body, stageRepGroups, helperPath, scope, tc)
	setup := strings.Join([]string{
		"cat <<'WR_NF_ONERROR' > " + helperPath,
		helper,
		"WR_NF_ONERROR",
		"chmod +x " + helperPath,
		"bash ./" + helperPath,
	}, "\n")

	return wrapWorkflowHookCommand(setup)
}

func buildWorkflowOnErrorHelperScript(body string, stageRepGroups []string, helperPath string, scope []string, tc TranslateConfig) string {
	jobCwd := deterministicCwd(tc.Cwd, tc.RunID, scope, "onError")
	repGroup := scopedRepGroup(tc.WorkflowName, tc.RunID, scope, "onError")
	reqGroup := "nf.workflow.onError"
	statusCmd := workflowHookStatusCommand("", stageRepGroups)
	resubmitAdd := fmt.Sprintf(
		"wr add --cwd %s --cwd_matters --rep_grp %s --req_grp %s --limit_grps \"$next_limit\" %s",
		shellQuote(jobCwd),
		shellQuote(repGroup),
		shellQuote(reqGroup),
		shellQuote("bash ./"+helperPath),
	)
	resubmitCmd := strings.Join([]string{
		`next_limit=$(date -d '+1 minute' '+datetime < %Y-%m-%d %H:%M:%S')`,
		resubmitAdd,
	}, "\n        ")
	failureCountCmd := strings.Join([]string{
		`failure_count=$(printf '%s\n' "$status_output" | awk -F '\t' `,
		`'$2 == "buried" || $2 == "lost" {count++} END {print count+0}')`,
	}, "")
	activeCountCmd := strings.Join([]string{
		`active_count=$(printf '%s\n' "$status_output" | awk -F '\t' `,
		`'$2 != "" && $2 != "buried" && $2 != "complete" && $2 != "lost" {count++} END {print count+0}')`,
	}, "")

	bodyLines := indentLines(body, "    ")
	if len(bodyLines) == 0 {
		bodyLines = []string{"    :"}
	}

	lines := []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("status_output=$( %s )", statusCmd),
		failureCountCmd,
		activeCountCmd,
		`if [ "$failure_count" -gt 0 ]; then`,
	}
	lines = append(lines, bodyLines...)
	lines = append(lines,
		"else",
		`    if [ "$active_count" -gt 0 ]; then`,
		"        "+resubmitCmd,
		"    fi",
		"fi",
	)

	return strings.Join(lines, "\n")
}

func workflowHookStatusCommand(flag string, stageRepGroups []string) string {
	commands := make([]string, 0, len(stageRepGroups))
	for _, repGroup := range stageRepGroups {
		cmd := "wr status"
		if flag != "" {
			cmd += " " + flag
		}

		commands = append(commands, fmt.Sprintf("%s -i %s -o plain 2>/dev/null || true", cmd, shellQuote(repGroup)))
	}

	if len(commands) == 0 {
		return "printf ''"
	}

	return fmt.Sprintf("{\n%s\n}", strings.Join(commands, "\n"))
}

func indentLines(body string, indent string) []string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil
	}

	lines := strings.Split(trimmed, "\n")

	indented := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			indented = append(indented, indent)

			continue
		}

		indented = append(indented, indent+strings.TrimSpace(line))
	}

	return indented
}

func applyPublishDirBehaviours(
	job *jobqueue.Job,
	proc *Process,
	bindings []string,
	params map[string]any,
	tc TranslateConfig,
	cwd string,
) error {
	if len(proc.PublishDir) == 0 {
		return nil
	}

	for _, directive := range proc.PublishDir {
		if directive == nil {
			continue
		}

		command, err := buildPublishDirCommand(proc, directive, bindings, params, tc, cwd)
		if err != nil {
			return err
		}

		if command == "" {
			continue
		}

		job.Behaviours = append(job.Behaviours, &jobqueue.Behaviour{
			When: jobqueue.OnSuccess,
			Do:   jobqueue.Run,
			Arg:  command,
		})
	}

	return nil
}

func buildPublishDirCommand(
	proc *Process,
	directive *PublishDir,
	bindings []string,
	params map[string]any,
	tc TranslateConfig,
	cwd string,
) (string, error) {
	target, err := resolvePublishDirTarget(directive.Path, params, tc)
	if err != nil {
		return "", err
	}

	target = ensureTrailingSeparator(target)
	mkdir := "mkdir -p " + shellQuote(target)

	if directive.Pattern != "" {
		return strings.Join([]string{
			mkdir,
			"shopt -s nullglob",
			fmt.Sprintf(
				"for path in %s; do %s; done",
				directive.Pattern,
				publishDirActionCommand(directive.Mode, "$path", target),
			),
		}, " && "), nil
	}

	sources := publishDirSources(proc, bindings, params, cwd)
	if len(sources) == 0 {
		return mkdir, nil
	}

	commands := make([]string, 0, len(sources)+1)

	commands = append(commands, mkdir)
	for _, source := range sources {
		commands = append(commands, publishDirActionCommand(directive.Mode, shellQuote(source), target))
	}

	return strings.Join(commands, " && "), nil
}

func publishDirSources(proc *Process, bindings []string, params map[string]any, cwd string) []string {
	if len(proc.Output) == 0 {
		return nil
	}

	vars := outputVars(proc, bindings, params)

	sources := make([]string, 0, len(proc.Output))
	for _, decl := range proc.Output {
		if decl == nil {
			continue
		}

		switch decl.Kind {
		case "path", "file":
			pattern, ok := resolvedOutputPattern(decl.Expr, decl.Name, vars, cwd)
			if !ok || pattern == "" {
				continue
			}

			sources = appendUniqueStrings(sources, []string{pattern})
		case "tuple":
			for _, element := range decl.Elements {
				if element == nil || (element.Kind != "path" && element.Kind != "file") {
					continue
				}

				pattern, ok := resolvedOutputPattern(element.Expr, element.Name, vars, cwd)
				if !ok || pattern == "" {
					continue
				}

				sources = appendUniqueStrings(sources, []string{pattern})
			}
		}
	}

	return sources
}

func resolvePublishDirTarget(path string, params map[string]any, tc TranslateConfig) (string, error) {
	resolved, err := SubstituteParams(path, params)
	if err != nil {
		return "", err
	}

	if filepath.IsAbs(resolved) {
		return filepath.Clean(resolved), nil
	}

	base := tc.Cwd
	if tc.WorkflowPath != "" {
		base = filepath.Dir(tc.WorkflowPath)
	}

	return filepath.Clean(filepath.Join(base, resolved)), nil
}

func newProcessMaxErrorsJob(
	proc *Process,
	repGroup string,
	scope []string,
	params map[string]any,
	tc TranslateConfig,
	totalJobs int,
) (*jobqueue.Job, error) {
	if proc == nil || totalJobs == 0 {
		return nil, nil
	}

	maxErrors, err := resolveDirectiveInt("maxErrors", proc.Directives["maxErrors"], params, 0, defaultDirectiveTask())
	if err != nil {
		return nil, err
	}

	if maxErrors <= 0 {
		return nil, nil
	}

	helperScope := append(append([]string{}, scope...), proc.Name)
	cmd := buildProcessMaxErrorsCommand(repGroup, proc.Name, maxErrors, helperScope, tc)
	job := &jobqueue.Job{
		Cmd:          cmd,
		Cwd:          deterministicCwd(tc.Cwd, tc.RunID, helperScope, "maxErrors"),
		CwdMatters:   true,
		RepGroup:     scopedRepGroup(tc.WorkflowName, tc.RunID, helperScope, "maxErrors"),
		ReqGroup:     fmt.Sprintf("nf.%s.maxErrors", proc.Name),
		DepGroups:    []string{scopedDepGroup(tc.RunID, helperScope, "maxErrors")},
		Requirements: defaultWorkflowHookRequirements(),
		Override:     0,
	}
	applyCaptureCleanupBehaviour(job)

	return job, nil
}

func buildProcessMaxErrorsCommand(
	processRepGroup string,
	processName string,
	maxErrors int,
	scope []string,
	tc TranslateConfig,
) string {
	helperPath := ".wr-maxerrors-poll.sh"
	helper := buildProcessMaxErrorsHelperScript(processRepGroup, processName, maxErrors, helperPath, scope, tc)
	setup := strings.Join([]string{
		"cat <<'WR_NF_MAXERRORS' > " + helperPath,
		helper,
		"WR_NF_MAXERRORS",
		"chmod +x " + helperPath,
		"bash ./" + helperPath,
	}, "\n")

	return wrapWorkflowHookCommand(setup)
}

func buildProcessMaxErrorsHelperScript(processRepGroup, processName string, maxErrors int, helperPath string, scope []string, tc TranslateConfig) string {
	jobCwd := deterministicCwd(tc.Cwd, tc.RunID, scope, "maxErrors")
	repGroup := scopedRepGroup(tc.WorkflowName, tc.RunID, scope, "maxErrors")
	reqGroup := fmt.Sprintf("nf.%s.maxErrors", processName)
	statusCmd := fmt.Sprintf("wr status -i %s -o plain 2>/dev/null || true", shellQuote(processRepGroup))
	resubmitAdd := fmt.Sprintf(
		"wr add --cwd %s --cwd_matters --rep_grp %s --req_grp %s --limit_grps \"$next_limit\" %s",
		shellQuote(jobCwd),
		shellQuote(repGroup),
		shellQuote(reqGroup),
		shellQuote("bash ./"+helperPath),
	)
	resubmitCmd := strings.Join([]string{
		`next_limit=$(date -d '+1 minute' '+datetime < %Y-%m-%d %H:%M:%S')`,
		resubmitAdd,
	}, "\n        ")
	activeCountCmd := strings.Join([]string{
		`active_count=$(printf '%s\n' "$status_output" | awk -F '\t' `,
		`'$2 != "" && $2 != "buried" && $2 != "complete" && $2 != "lost" {count++} END {print count+0}')`,
	}, "")

	lines := []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		fmt.Sprintf("status_output=$( %s )", statusCmd),
		`buried_count=$(printf '%s\n' "$status_output" | awk -F '\t' '$2 == "buried" {count++} END {print count+0}')`,
		activeCountCmd,
		fmt.Sprintf(`if [ "$buried_count" -gt %d ]; then`, maxErrors),
		`    while IFS=$'\t' read -r job_id state; do`,
		`        case "$state" in`,
		`            running)`,
		`                wr kill -i "$job_id" -y 2>/dev/null || true`,
		`                ;;`,
		`            lost)`,
		`                wr kill --confirmdead -i "$job_id" -y 2>/dev/null || true`,
		`                ;;`,
		`            delayed|dependent|ready|reserved)`,
		`                wr remove -i "$job_id" -y 2>/dev/null || true`,
		`                ;;`,
		`        esac`,
		`    done <<< "$status_output"`,
		`    if [ "$active_count" -gt 0 ]; then`,
		"        " + resubmitCmd,
		`    fi`,
		`elif [ "$active_count" -gt 0 ]; then`,
		"    " + resubmitCmd,
		"fi",
	}

	return strings.Join(lines, "\n")
}

type workflowPublishIndexEntry struct {
	depGroup    string
	source      string
	destination string
}

// OutputTarget represents a parsed target from the output {} block.
type OutputTarget struct {
	Name      string
	Path      string
	IndexPath string
}

func parseOutputTarget(name string, tokens []token) (*OutputTarget, error) {
	target := &OutputTarget{Name: name}

	for pos := 0; pos < len(tokens); {
		pos = skipOutputBlockSeparators(tokens, pos)
		if pos >= len(tokens) {
			break
		}

		if tokens[pos].typ != tokenIdent {
			return nil, fmt.Errorf("expected property name in output target %q, found %q", name, tokens[pos].lit)
		}

		property := tokens[pos].lit
		pos++
		pos = skipOutputBlockSeparators(tokens, pos)

		switch property {
		case "path":
			valueTokens, next := consumeOutputBlockValue(tokens, pos)

			path, supported, err := parseOutputStaticPath(name, "path", valueTokens)
			if err != nil {
				return nil, err
			}

			if !supported {
				return nil, nil
			}

			target.Path = path
			pos = next
		case "index":
			if pos >= len(tokens) || tokens[pos].typ != tokenLBrace {
				return nil, fmt.Errorf("expected block body for output target %q index", name)
			}

			end, err := findOutputBlockClosingBrace(tokens, pos)
			if err != nil {
				return nil, err
			}

			indexPath, supported, err := parseOutputIndexPath(name, tokens[pos+1:end])
			if err != nil {
				return nil, err
			}

			if !supported {
				return nil, nil
			}

			target.IndexPath = indexPath
			pos = end + 1
		default:
			if pos < len(tokens) && tokens[pos].typ == tokenLBrace {
				end, err := findOutputBlockClosingBrace(tokens, pos)
				if err != nil {
					return nil, err
				}

				pos = end + 1

				continue
			}

			_, pos = consumeOutputBlockValue(tokens, pos)
		}
	}

	if target.Path == "" {
		return nil, nil
	}

	return target, nil
}

func parseOutputTargetMap(rawOutputBlock string) (map[string]*OutputTarget, error) {
	targets, err := ParseOutputBlock(rawOutputBlock)
	if err != nil {
		return nil, err
	}

	if len(targets) == 0 {
		return nil, nil
	}

	indexed := make(map[string]*OutputTarget, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}

		indexed[target.Name] = target
	}

	return indexed, nil
}

// ParseOutputBlock extracts named targets from a raw output block body string.
func ParseOutputBlock(body string) ([]*OutputTarget, error) {
	targets := make([]*OutputTarget, 0)
	if strings.TrimSpace(body) == "" {
		return targets, nil
	}

	rawTokens, err := lex(body)
	if err != nil {
		return nil, err
	}

	tokens := make([]token, 0, len(rawTokens))
	for _, tok := range rawTokens {
		if tok.typ == tokenEOF {
			continue
		}

		tokens = append(tokens, tok)
	}

	for pos := 0; pos < len(tokens); {
		pos = skipOutputBlockSeparators(tokens, pos)
		if pos >= len(tokens) {
			break
		}

		if tokens[pos].typ != tokenIdent {
			return nil, fmt.Errorf("expected output target name, found %q", tokens[pos].lit)
		}

		name := tokens[pos].lit
		pos++

		pos = skipOutputBlockSeparators(tokens, pos)
		if pos >= len(tokens) || tokens[pos].typ != tokenLBrace {
			return nil, fmt.Errorf("expected block body for output target %q", name)
		}

		end, err := findOutputBlockClosingBrace(tokens, pos)
		if err != nil {
			return nil, err
		}

		target, err := parseOutputTarget(name, tokens[pos+1:end])
		if err != nil {
			return nil, err
		}

		if target != nil {
			targets = append(targets, target)
		}

		pos = end + 1
	}

	return targets, nil
}

func skipOutputBlockSeparators(tokens []token, pos int) int {
	for pos < len(tokens) {
		typ := tokens[pos].typ
		if typ != tokenNewline && typ != tokenSemicolon {
			break
		}

		pos++
	}

	return pos
}

func findOutputBlockClosingBrace(tokens []token, start int) (int, error) {
	depth := 0

	for index := start; index < len(tokens); index++ {
		switch tokens[index].typ {
		case tokenLBrace:
			depth++
		case tokenRBrace:
			depth--
			if depth == 0 {
				return index, nil
			}
		}
	}

	return 0, errors.New("unterminated output block")
}

type emittedOutput struct {
	outputPaths []string
	items       []channelItem
}

type translatedCall struct {
	depGroup      string
	depGroups     []string
	repGroup      string
	repGroups     []string
	baseCwd       string
	outputPaths   []string
	emitOutputs   map[string]emittedOutput
	items         []channelItem
	dynamicOutput bool
	pending       bool
	skipped       bool
}

func translatedCallForRepGroup(translated map[string]translatedCall, repGrp string) (translatedCall, bool) {
	for _, stage := range translated {
		if stage.repGroup == repGrp {
			return stage, true
		}
	}

	return translatedCall{}, false
}

func completedOutputPathsForJob(pending *PendingStage, repGrp string, job *jobqueue.Job) ([]string, error) {
	stage, ok := translatedCallForRepGroup(pending.translated, repGrp)
	if !ok {
		return nil, fmt.Errorf("missing translated stage for rep group %q", repGrp)
	}

	patterns := outputPatternsForCwd(stage.outputPaths, stage.baseCwd, job.Cwd)
	if len(patterns) == 0 {
		return nil, fmt.Errorf("missing output patterns for rep group %q in %q", repGrp, job.Cwd)
	}

	resolved := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		matched, err := expandCompletedOutputPattern(pattern)
		if err != nil {
			return nil, err
		}

		resolved = append(resolved, matched...)
	}

	if len(resolved) == 0 {
		return nil, fmt.Errorf("no completed output paths found for rep group %q in %q", repGrp, job.Cwd)
	}

	return resolved, nil
}

func outputPatternsForCwd(patterns []string, baseCwd, cwd string) []string {
	filtered := make([]string, 0, len(patterns))
	absolute := make([]string, 0, len(patterns))
	cleanCwd := filepath.Clean(cwd)
	cleanBaseCwd := filepath.Clean(baseCwd)

	for _, pattern := range patterns {
		cleanPattern := filepath.Clean(pattern)
		if cleanPattern == cleanCwd || strings.HasPrefix(cleanPattern, cleanCwd+string(os.PathSeparator)) {
			filtered = append(filtered, pattern)

			continue
		}

		if filepath.IsAbs(cleanPattern) {
			absolute = append(absolute, pattern)
		}
	}

	if len(filtered) == 0 {
		if cleanBaseCwd != "." && cleanBaseCwd != "" && cleanBaseCwd != cleanCwd {
			remapped := remapOutputPatterns(patterns, cleanBaseCwd, cleanCwd)
			if len(remapped) > 0 {
				return remapped
			}
		}

		return absolute
	}

	return filtered
}

func remapOutputPatterns(patterns []string, fromCwd, toCwd string) []string {
	remapped := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		cleanPattern := filepath.Clean(pattern)
		if !strings.HasPrefix(cleanPattern, fromCwd+string(os.PathSeparator)) {
			continue
		}

		relPath, err := filepath.Rel(fromCwd, cleanPattern)
		if err != nil {
			continue
		}

		remapped = append(remapped, filepath.Join(toCwd, relPath))
	}

	return remapped
}

func expandCompletedOutputPattern(pattern string) ([]string, error) {
	if strings.ContainsAny(pattern, "*?[") {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("expand output pattern %q: %w", pattern, err)
		}

		if len(matches) == 0 {
			return nil, nil
		}

		sort.Strings(matches)

		return matches, nil
	}

	if _, err := os.Stat(pattern); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("stat output path %q: %w", pattern, err)
	}

	return []string{pattern}, nil
}

// PendingStage describes a stage whose jobs will be created later.
type PendingStage struct {
	Process      *Process
	AwaitDepGrps []string

	call         *Call
	scope        []string
	defaults     *ProcessDefaults
	selectors    []*ProcessSelector
	params       map[string]any
	translated   map[string]translatedCall
	awaitRepGrps []string
}

// CompletedJobsForPending builds completed upstream job records for a pending
// stage from concrete wr jobs that have already finished successfully.
func CompletedJobsForPending(pending *PendingStage, jobs []*jobqueue.Job, incompleteJobs []*jobqueue.Job) ([]CompletedJob, bool, error) {
	if pending == nil {
		return nil, false, errors.New("pending stage is nil")
	}

	jobsByRepGrp := make(map[string][]*jobqueue.Job)
	jobsByDepGrp := make(map[string]*jobqueue.Job)
	incompleteByRepGrp := make(map[string]struct{})

	for _, job := range jobs {
		if job == nil {
			continue
		}

		jobsByRepGrp[job.RepGroup] = append(jobsByRepGrp[job.RepGroup], job)
		for _, depGrp := range job.DepGroups {
			jobsByDepGrp[depGrp] = job
		}
	}

	for _, job := range incompleteJobs {
		if job == nil {
			continue
		}

		incompleteByRepGrp[job.RepGroup] = struct{}{}
	}

	for _, depGrp := range pending.AwaitDepGrps {
		if _, ok := jobsByDepGrp[depGrp]; !ok {
			return nil, false, nil
		}
	}

	for _, repGrp := range pending.awaitRepGrps {
		if _, ok := incompleteByRepGrp[repGrp]; ok {
			return nil, false, nil
		}
	}

	completed := make([]CompletedJob, 0, len(jobs))

	for _, repGrp := range pending.awaitRepGrps {
		matchedJobs := jobsByRepGrp[repGrp]
		if len(matchedJobs) == 0 {
			return nil, false, nil
		}

		sort.Slice(matchedJobs, func(i, j int) bool {
			leftParent, leftIndexes, leftIndexed := indexedCwdOrder(matchedJobs[i].Cwd)

			rightParent, rightIndexes, rightIndexed := indexedCwdOrder(matchedJobs[j].Cwd)
			if leftIndexed && rightIndexed && leftParent == rightParent {
				if cmp := compareIndexedSuffixes(leftIndexes, rightIndexes); cmp != 0 {
					return cmp < 0
				}
			}

			if matchedJobs[i].Cwd != matchedJobs[j].Cwd {
				return matchedJobs[i].Cwd < matchedJobs[j].Cwd
			}

			return matchedJobs[i].Cmd < matchedJobs[j].Cmd
		})

		for _, job := range matchedJobs {
			outputPaths, err := completedOutputPathsForJob(pending, repGrp, job)
			if err != nil {
				return nil, false, err
			}

			completed = append(completed, CompletedJob{
				RepGrp:      repGrp,
				OutputPaths: outputPaths,
				DepGroups:   cloneStrings(job.DepGroups),
				ExitCode:    job.Exitcode,
			})
		}
	}

	return completed, true, nil
}

func indexedCwdOrder(cwd string) (string, []int, bool) {
	cleaned := filepath.Clean(cwd)
	parts := strings.Split(filepath.Base(cleaned), "_")

	indexes := make([]int, 0, len(parts))
	for _, part := range parts {
		index, err := strconv.Atoi(part)
		if err != nil {
			return "", nil, false
		}

		indexes = append(indexes, index)
	}

	return filepath.Dir(cleaned), indexes, true
}

func compareIndexedSuffixes(left, right []int) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}

	for index := range limit {
		if left[index] < right[index] {
			return -1
		}

		if left[index] > right[index] {
			return 1
		}
	}

	if len(left) < len(right) {
		return -1
	}

	if len(left) > len(right) {
		return 1
	}

	return 0
}

// MarkPendingStageSkipped records that a pending stage resolved to an empty
// channel and updates downstream pending stages so they no longer wait on it.
func MarkPendingStageSkipped(skipped *PendingStage, downstream []*PendingStage) error {
	if skipped == nil || skipped.call == nil {
		return errors.New("pending stage is nil")
	}

	targetKey := scopedTargetKey(skipped.scope, skipped.call.Target)

	stage, ok := skipped.translated[targetKey]
	if !ok {
		return fmt.Errorf("pending stage %q has no translated placeholder", skipped.call.Target)
	}

	for _, pending := range downstream {
		if pending == nil {
			continue
		}

		pending.awaitRepGrps = removeStringValues(pending.awaitRepGrps, stage.repGroup)

		existing, ok := pending.translated[targetKey]
		if !ok {
			continue
		}

		existing.depGroup = ""
		existing.depGroups = nil
		existing.outputPaths = nil
		existing.emitOutputs = nil
		existing.items = nil
		existing.dynamicOutput = false
		existing.pending = false
		existing.skipped = true
		pending.translated[targetKey] = existing
	}

	return nil
}

func removeStringValues(values []string, remove string) []string {
	if len(values) == 0 || remove == "" {
		return values
	}

	filtered := values[:0]
	for _, value := range values {
		if value == remove {
			continue
		}

		filtered = append(filtered, value)
	}

	return filtered
}

// TranslateResult holds the output of Translate.
type TranslateResult struct {
	Jobs    []*jobqueue.Job
	Pending []*PendingStage
}

// Translate converts a parsed Workflow and config into wr jobs.
func Translate(wf *Workflow, cfg *Config, tc TranslateConfig) (*TranslateResult, error) {
	if wf == nil {
		return nil, errors.New("workflow is nil")
	}

	if err := validateTranslateProfile(cfg, tc.Profile); err != nil {
		return nil, err
	}

	result := &TranslateResult{}
	if wf.EntryWF == nil {
		return result, nil
	}

	processes, err := processIndex(wf.Processes)
	if err != nil {
		return nil, err
	}

	subworkflows, err := subWorkflowIndex(wf.SubWFs)
	if err != nil {
		return nil, err
	}

	if err = validateScopedRepGroups(processes, subworkflows); err != nil {
		return nil, err
	}

	params, err := mergeTranslateParams(wf, cfg, tc)
	if err != nil {
		return nil, err
	}

	defaults := effectiveDefaults(cfg, tc.Profile)
	selectors := effectiveSelectors(cfg, tc.Profile)

	outputTargets, err := parseOutputTargetMap(wf.OutputBlock)
	if err != nil {
		return nil, err
	}

	translated := make(map[string]translatedCall, len(wf.EntryWF.Calls))

	if err = translateBlock(
		wf.EntryWF,
		nil,
		processes,
		subworkflows,
		translated,
		outputTargets,
		defaults,
		selectors,
		params,
		tc,
		result,
	); err != nil {
		return nil, err
	}

	return result, nil
}

// CompletedJob holds completed upstream job information.
type CompletedJob struct {
	RepGrp      string
	OutputPaths []string
	DepGroups   []string
	ExitCode    int
}

type bindingSet struct {
	bindings  []string
	values    []any
	depGroups []string
	regularIx int
	eachIx    int
	hasEach   bool
}

type resolvedArg struct {
	items  []bindingSet
	fanout bool
}

type selectorMatch struct {
	specificity int
	settings    *ProcessDefaults
}

type workflowConditionBranch struct {
	scopeName string
	condition string
	calls     []*Call
}

func parseOutputIndexPath(name string, tokens []token) (string, bool, error) {
	for pos := 0; pos < len(tokens); {
		pos = skipOutputBlockSeparators(tokens, pos)
		if pos >= len(tokens) {
			break
		}

		if tokens[pos].typ != tokenIdent {
			return "", false, fmt.Errorf("expected property name in output target %q index block, found %q", name, tokens[pos].lit)
		}

		property := tokens[pos].lit
		pos++
		pos = skipOutputBlockSeparators(tokens, pos)

		if property == "path" {
			valueTokens, _ := consumeOutputBlockValue(tokens, pos)

			return parseOutputStaticPath(name, "index path", valueTokens)
		}

		if pos < len(tokens) && tokens[pos].typ == tokenLBrace {
			end, err := findOutputBlockClosingBrace(tokens, pos)
			if err != nil {
				return "", false, err
			}

			pos = end + 1

			continue
		}

		_, pos = consumeOutputBlockValue(tokens, pos)
	}

	return "", true, nil
}

func consumeOutputBlockValue(tokens []token, start int) ([]token, int) {
	parenDepth := 0
	bracketDepth := 0
	braceDepth := 0
	end := start

loop:
	for end < len(tokens) {
		current := tokens[end]
		if (current.typ == tokenNewline || current.typ == tokenSemicolon) && parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
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
			if braceDepth == 0 && parenDepth == 0 && bracketDepth == 0 {
				break loop
			}

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

		end++
	}

	return trimDeclarationTokens(tokens[start:end]), skipOutputBlockSeparators(tokens, end)
}

func parseOutputStaticPath(name, property string, tokens []token) (string, bool, error) {
	trimmed := trimDeclarationTokens(tokens)
	if len(trimmed) == 0 {
		return "", false, fmt.Errorf("expected %s value for output target %q", property, name)
	}

	expr, err := parseExprTokens(trimmed)
	if err != nil {
		return "", false, err
	}

	switch value := expr.(type) {
	case StringExpr:
		return value.Value, true, nil
	case ClosureExpr:
		warnf("nextflowdsl: output target %q has closure-valued %s %q; skipping target\n", name, property, renderExpr(value))
	default:
		warnf("nextflowdsl: output target %q has non-static %s %q; skipping target\n", name, property, renderExpr(value))
	}

	return "", false, nil
}

func processWithMergedExt(proc *Process, defaults *ProcessDefaults) (*Process, error) {
	if proc == nil {
		return nil, nil
	}

	merged, err := extDirectiveSourceMap(proc.Directives["ext"])
	if err != nil {
		return nil, err
	}

	merged = mergeExtValues(merged, defaults.Ext)
	if len(merged) == 0 {
		return proc, nil
	}

	cloned := *proc
	cloned.Directives = cloneDirectiveMap(proc.Directives)
	cloned.Directives["ext"] = merged

	return &cloned, nil
}

func resolveExtDirective(proc *Process, defaults *ProcessDefaults, bindings []string, params map[string]any) (map[string]any, error) {
	return resolveExtDirectiveWithValues(proc, defaults, bindings, nil, params)
}

func workflowPollingLimitGroup(t time.Time) string {
	return "datetime < " + t.Format(time.DateTime)
}

func scopedRepGroupPrefix(workflowName, runID string, scope []string) string {
	parts := []string{"nf", repGroupToken(workflowName), repGroupToken(runID)}
	parts = append(parts, scope...)

	return strings.Join(parts, ".")
}

func validateTranslateProfile(cfg *Config, profileName string) error {
	if cfg == nil || profileName == "" {
		return nil
	}

	if cfg.Profiles != nil {
		if _, ok := cfg.Profiles[profileName]; ok {
			return nil
		}
	}

	available := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		available = append(available, name)
	}

	sort.Strings(available)

	if len(available) == 0 {
		return fmt.Errorf("unknown config profile %q: config does not define any profiles", profileName)
	}

	return fmt.Errorf("unknown config profile %q; available profiles: %s", profileName, strings.Join(available, ", "))
}

func buildCommand(
	proc *Process,
	bindings []string,
	params map[string]any,
	cwd string,
	launchCwd string,
) (string, error) {
	return buildCommandWithValues(proc, bindings, nil, params, cwd, launchCwd)
}

func indexedCwd(cwd string) (string, int, bool) {
	parent, indexes, ok := indexedCwdOrder(cwd)
	if !ok || len(indexes) != 1 {
		return "", 0, false
	}

	return parent, indexes[0], true
}

func processIndex(processes []*Process) (map[string]*Process, error) {
	indexed := make(map[string]*Process, len(processes))
	for _, proc := range processes {
		if proc == nil {
			continue
		}

		if _, exists := indexed[proc.Name]; exists {
			return nil, fmt.Errorf("duplicate process %q", proc.Name)
		}

		indexed[proc.Name] = proc
	}

	return indexed, nil
}

func subWorkflowIndex(subworkflows []*SubWorkflow) (map[string]*SubWorkflow, error) {
	indexed := make(map[string]*SubWorkflow, len(subworkflows))
	for _, subworkflow := range subworkflows {
		if subworkflow == nil {
			continue
		}

		if _, exists := indexed[subworkflow.Name]; exists {
			return nil, fmt.Errorf("duplicate subworkflow %q", subworkflow.Name)
		}

		indexed[subworkflow.Name] = subworkflow
	}

	return indexed, nil
}

func validateScopedRepGroups(processes map[string]*Process, subworkflows map[string]*SubWorkflow) error {
	for name := range subworkflows {
		if _, exists := processes[name]; exists {
			return fmt.Errorf("rep_grp collision: subworkflow %q collides with process %q", name, name)
		}
	}

	return nil
}

func effectiveDefaults(cfg *Config, profileName string) *ProcessDefaults {
	defaults := cloneDefaults(nil)

	if cfg != nil {
		if len(cfg.Env) > 0 {
			defaults = mergeDefaults(defaults, &ProcessDefaults{Env: cfg.Env})
		}

		defaults = mergeDefaults(defaults, cfg.Process)
		if profileName != "" && cfg.Profiles != nil {
			if profile, ok := cfg.Profiles[profileName]; ok {
				defaults = mergeDefaults(defaults, profile.Process)
			}
		}
	}

	return defaults
}

func effectiveSelectors(cfg *Config, profileName string) []*ProcessSelector {
	if cfg == nil {
		return nil
	}

	selectors := cloneSelectors(cfg.Selectors)
	if profileName == "" || cfg.Profiles == nil {
		return selectors
	}

	profile, ok := cfg.Profiles[profileName]
	if !ok || len(profile.Selectors) == 0 {
		return selectors
	}

	return append(selectors, cloneSelectors(profile.Selectors)...)
}
