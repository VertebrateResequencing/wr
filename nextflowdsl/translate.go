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
	"net/url"
	"os"
	"path/filepath"
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

// TranslateConfig controls how the AST is translated to wr jobs.
type TranslateConfig struct {
	RunID            string
	WorkflowName     string
	WorkflowPath     string
	Cwd              string
	ContainerRuntime string
	Params           map[string]any
	Profile          string
}

func applyCaptureCleanupBehaviour(job *jobqueue.Job) {
	job.Behaviours = append(job.Behaviours, &jobqueue.Behaviour{
		When: jobqueue.OnExit,
		Do:   jobqueue.Run,
		Arg:  fmt.Sprintf(`for f in %s %s; do if [ -f "$f" ] && ! grep -qP '\S' "$f"; then rm -f "$f"; fi; done`, nfStdoutFile, nfStderrFile),
	})
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

	patterns := outputPatternsForCwd(stage.outputPaths, job.Cwd)
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

func outputPatternsForCwd(patterns []string, cwd string) []string {
	filtered := make([]string, 0, len(patterns))
	cleanCwd := filepath.Clean(cwd)

	for _, pattern := range patterns {
		cleanPattern := filepath.Clean(pattern)
		if cleanPattern == cleanCwd || strings.HasPrefix(cleanPattern, cleanCwd+string(os.PathSeparator)) {
			filtered = append(filtered, pattern)
		}
	}

	return filtered
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
	params       map[string]any
	translated   map[string]translatedCall
	awaitRepGrps []string
}

// CompletedJobsForPending builds completed upstream job records for a pending
// stage from concrete wr jobs that have already finished successfully.
func CompletedJobsForPending(pending *PendingStage, jobs []*jobqueue.Job) ([]CompletedJob, bool, error) {
	if pending == nil {
		return nil, false, fmt.Errorf("pending stage is nil")
	}

	jobsByRepGrp := make(map[string][]*jobqueue.Job)
	jobsByDepGrp := make(map[string]*jobqueue.Job)

	for _, job := range jobs {
		if job == nil {
			continue
		}

		jobsByRepGrp[job.RepGroup] = append(jobsByRepGrp[job.RepGroup], job)
		for _, depGrp := range job.DepGroups {
			jobsByDepGrp[depGrp] = job
		}
	}

	for _, depGrp := range pending.AwaitDepGrps {
		if _, ok := jobsByDepGrp[depGrp]; !ok {
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
				ExitCode:    job.Exitcode,
			})
		}
	}

	return completed, true, nil
}

// TranslateResult holds the output of Translate.
type TranslateResult struct {
	Jobs    []*jobqueue.Job
	Pending []*PendingStage
}

// CompletedJob holds completed upstream job information.
type CompletedJob struct {
	RepGrp      string
	OutputPaths []string
	ExitCode    int
}

type translatedCall struct {
	depGroup      string
	depGroups     []string
	repGroup      string
	outputPaths   []string
	items         []channelItem
	dynamicOutput bool
	pending       bool
}

type bindingSet struct {
	bindings  []string
	depGroups []string
}

type resolvedArg struct {
	items  []bindingSet
	fanout bool
}

func mergeTranslateParams(cfg *Config, tc TranslateConfig) map[string]any {
	var sources []map[string]any
	if cfg != nil && len(cfg.Params) > 0 {
		sources = append(sources, cfg.Params)
	}
	if cfg != nil && tc.Profile != "" && cfg.Profiles != nil {
		if profile, ok := cfg.Profiles[tc.Profile]; ok && len(profile.Params) > 0 {
			sources = append(sources, profile.Params)
		}
	}
	if len(tc.Params) > 0 {
		sources = append(sources, tc.Params)
	}
	if len(sources) == 0 {
		return nil
	}

	return MergeParams(sources...)
}

// Translate converts a parsed Workflow and config into wr jobs.
func Translate(wf *Workflow, cfg *Config, tc TranslateConfig) (*TranslateResult, error) {
	if wf == nil {
		return nil, fmt.Errorf("workflow is nil")
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

	params := mergeTranslateParams(cfg, tc)
	defaults := effectiveDefaults(cfg, tc.Profile)
	translated := make(map[string]translatedCall, len(wf.EntryWF.Calls))

	if err = translateBlock(wf.EntryWF, nil, processes, subworkflows, translated, defaults, params, tc, result); err != nil {
		return nil, err
	}

	return result, nil
}

// TranslatePending resolves a pending stage into concrete jobs from completed upstream jobs.
func TranslatePending(pending *PendingStage, completed []CompletedJob, tc TranslateConfig) ([]*jobqueue.Job, error) {
	if pending == nil || pending.Process == nil {
		return nil, fmt.Errorf("pending stage is nil")
	}
	if pending.call == nil {
		return nil, fmt.Errorf("pending stage has no call context")
	}

	params := cloneParams(pending.params)
	if len(tc.Params) > 0 {
		params = MergeParams(params, tc.Params)
	}

	completedByRepGrp := make(map[string]CompletedJob, len(completed))
	completedItemsByRepGrp := make(map[string][]CompletedJob, len(completed))
	for _, job := range completed {
		completedItemsByRepGrp[job.RepGrp] = append(completedItemsByRepGrp[job.RepGrp], CompletedJob{
			RepGrp:      job.RepGrp,
			OutputPaths: cloneStrings(job.OutputPaths),
			ExitCode:    job.ExitCode,
		})

		existing, ok := completedByRepGrp[job.RepGrp]
		if !ok {
			completedByRepGrp[job.RepGrp] = CompletedJob{
				RepGrp:      job.RepGrp,
				OutputPaths: cloneStrings(job.OutputPaths),
				ExitCode:    job.ExitCode,
			}
			continue
		}

		existing.OutputPaths = appendUniqueStrings(existing.OutputPaths, job.OutputPaths)
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
		stage.items = make([]channelItem, 0, len(matches))
		for index, match := range matches {
			itemDeps := cloneStrings(deps)
			if len(deps) == len(matches) {
				itemDeps = []string{deps[index]}
			}

			stage.items = append(stage.items, channelItem{
				value:     channelItemValue(match.OutputPaths),
				depGroups: itemDeps,
			})
		}
		stage.pending = false
		translated[name] = stage
	}

	jobs, _, err := translateProcessCall(pending.Process, pending.call, pending.scope, translated, cloneDefaults(pending.defaults), params, tc)
	if err != nil {
		return nil, err
	}

	return jobs, nil
}

func translateBlock(
	block *WorkflowBlock,
	scope []string,
	processes map[string]*Process,
	subworkflows map[string]*SubWorkflow,
	translated map[string]translatedCall,
	defaults *ProcessDefaults,
	params map[string]any,
	tc TranslateConfig,
	result *TranslateResult,
) error {
	if block == nil {
		return nil
	}

	for _, call := range block.Calls {
		if call == nil {
			continue
		}

		if proc, ok := processes[call.Target]; ok {
			awaitDepGrps, awaitRepGrps, err := detectPendingInputs(call, scope, translated)
			if err != nil {
				return err
			}
			if len(awaitDepGrps) > 0 {
				depGroup := scopedDepGroup(tc.RunID, scope, proc.Name)
				translated[scopedTargetKey(scope, call.Target)] = translatedCall{
					depGroup:      depGroup,
					depGroups:     []string{depGroup},
					repGroup:      scopedRepGroup(tc.WorkflowName, tc.RunID, scope, proc.Name),
					outputPaths:   outputPaths(proc, deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name)),
					items:         []channelItem{{value: channelItemValue(outputPaths(proc, deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name))), depGroups: []string{depGroup}}},
					dynamicOutput: hasDynamicOutputs(proc),
					pending:       true,
				}
				result.Pending = append(result.Pending, &PendingStage{
					Process:      proc,
					AwaitDepGrps: awaitDepGrps,
					call:         call,
					scope:        append([]string{}, scope...),
					defaults:     cloneDefaults(defaults),
					params:       cloneParams(params),
					translated:   cloneTranslatedCalls(translated),
					awaitRepGrps: cloneStrings(awaitRepGrps),
				})

				continue
			}

			jobs, stage, err := translateProcessCall(proc, call, scope, translated, defaults, params, tc)
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				continue
			}

			stage.repGroup = jobs[0].RepGroup
			stage.dynamicOutput = hasDynamicOutputs(proc)
			translated[scopedTargetKey(scope, call.Target)] = stage
			result.Jobs = append(result.Jobs, jobs...)

			continue
		}

		subwf, ok := subworkflows[call.Target]
		if !ok {
			return fmt.Errorf("unknown process or subworkflow %q", call.Target)
		}

		nextScope := append(append([]string{}, scope...), call.Target)
		if err := translateBlock(subwf.Body, nextScope, processes, subworkflows, translated, defaults, params, tc, result); err != nil {
			return err
		}
	}

	return nil
}

func translateProcessCall(
	proc *Process,
	call *Call,
	scope []string,
	translated map[string]translatedCall,
	defaults *ProcessDefaults,
	params map[string]any,
	tc TranslateConfig,
) ([]*jobqueue.Job, translatedCall, error) {
	bindingSets, err := resolveBindings(call, scope, translated, tc.Cwd)
	if err != nil {
		return nil, translatedCall{}, err
	}
	if len(bindingSets) == 0 {
		return nil, translatedCall{}, nil
	}

	jobs := make([]*jobqueue.Job, 0, len(bindingSets))
	stage := translatedCall{depGroups: make([]string, 0, len(bindingSets)), items: make([]channelItem, 0, len(bindingSets))}
	repGroup := scopedRepGroup(tc.WorkflowName, tc.RunID, scope, proc.Name)
	reqGroup := fmt.Sprintf("nf.%s", proc.Name)
	indexed := len(bindingSets) > 1

	for index, bindingSet := range bindingSets {
		cwd := deterministicCwd(tc.Cwd, tc.RunID, scope, proc.Name)
		depGroup := scopedDepGroup(tc.RunID, scope, proc.Name)
		if indexed {
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

		requirements, reqErr := buildRequirements(proc, defaults, params)
		if reqErr != nil {
			return nil, translatedCall{}, reqErr
		}
		job.Requirements = requirements

		applyContainer(job, proc, defaults, tc.ContainerRuntime)
		applyMaxForks(job, proc)
		applyErrorStrategy(job, proc)
		if err = applyEnv(job, proc, defaults); err != nil {
			return nil, translatedCall{}, err
		}

		cmd, cmdErr := buildCommand(proc, bindingSet.bindings, params)
		if cmdErr != nil {
			return nil, translatedCall{}, cmdErr
		}
		job.Cmd = cmd
		applyCaptureCleanupBehaviour(job)

		if err = applyPublishDirBehaviours(job, proc, params, tc); err != nil {
			return nil, translatedCall{}, err
		}

		jobs = append(jobs, job)
		if stage.depGroup == "" {
			stage.depGroup = depGroup
		}
		stage.depGroups = append(stage.depGroups, depGroup)
		paths := outputPaths(proc, cwd)
		stage.outputPaths = append(stage.outputPaths, paths...)
		stage.items = append(stage.items, channelItem{value: outputValue(proc, bindingSet.bindings, params, paths), depGroups: []string{depGroup}})
	}

	return jobs, stage, nil
}

func outputValue(proc *Process, bindings []string, params map[string]any, fallbackPaths []string) any {
	if proc == nil || len(proc.Output) == 0 {
		return channelItemValue(fallbackPaths)
	}

	values := make([]any, 0, len(proc.Output))
	for _, decl := range proc.Output {
		if decl == nil {
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

func outputVars(proc *Process, bindings []string, params map[string]any) map[string]any {
	vars := make(map[string]any)
	if len(params) > 0 {
		vars["params"] = params
	}
	for index, binding := range bindings {
		if index < len(proc.Input) && proc.Input[index] != nil && proc.Input[index].Name != "" {
			vars[proc.Input[index].Name] = binding
		}
	}

	return vars
}

func resolveBindings(call *Call, scope []string, translated map[string]translatedCall, cwd string) ([]bindingSet, error) {
	if call == nil || len(call.Args) == 0 {
		return []bindingSet{{}}, nil
	}

	plans := []bindingSet{{bindings: make([]string, len(call.Args))}}

	for index, arg := range call.Args {
		resolved, err := resolveBindingArg(arg, scope, translated, cwd)
		if err != nil {
			return nil, err
		}
		if resolved.fanout && len(resolved.items) == 0 {
			return nil, nil
		}

		if resolved.fanout && len(resolved.items) > 1 {
			switch {
			case len(plans) == 1:
				expanded := make([]bindingSet, len(resolved.items))
				for itemIndex, item := range resolved.items {
					expanded[itemIndex] = bindingSet{
						bindings:  cloneStrings(plans[0].bindings),
						depGroups: cloneStrings(plans[0].depGroups),
					}
					expanded[itemIndex].bindings[index] = item.bindings[0]
					expanded[itemIndex].depGroups = appendUniqueStrings(expanded[itemIndex].depGroups, item.depGroups)
				}
				plans = expanded
			case len(plans) == len(resolved.items):
				for itemIndex, item := range resolved.items {
					plans[itemIndex].bindings[index] = item.bindings[0]
					plans[itemIndex].depGroups = appendUniqueStrings(plans[itemIndex].depGroups, item.depGroups)
				}
			default:
				return nil, fmt.Errorf("channel cardinality mismatch for process %q", call.Target)
			}

			continue
		}

		binding := ""
		depGroups := []string{}
		if len(resolved.items) > 0 {
			binding = resolved.items[0].bindings[0]
			depGroups = resolved.items[0].depGroups
		}
		for itemIndex := range plans {
			plans[itemIndex].bindings[index] = binding
			plans[itemIndex].depGroups = appendUniqueStrings(plans[itemIndex].depGroups, depGroups)
		}
	}

	return plans, nil
}

func resolveBindingArg(arg ChanExpr, scope []string, translated map[string]translatedCall, cwd string) (resolvedArg, error) {
	switch arg.(type) {
	case ChanRef, ChannelFactory, ChannelChain, PipeExpr:
		items, err := resolveTranslatedChannelItems(arg, scope, translated, cwd)
		if err == nil {
			resolvedItems := make([]bindingSet, 0, len(items))
			for _, item := range items {
				resolvedItems = append(resolvedItems, bindingSet{
					bindings:  []string{itemBinding(item.value)},
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
			items = append(items, bindingSet{bindings: []string{path}, depGroups: cloneStrings(deps)})
		}

		return resolvedArg{items: items, fanout: true}, nil
	}

	return resolvedArg{items: []bindingSet{{bindings: []string{strings.Join(paths, " ")}, depGroups: deps}}}, nil
}

func resolveTranslatedChannelItems(arg ChanExpr, scope []string, translated map[string]translatedCall, cwd string) ([]channelItem, error) {
	return resolveChannelItems(arg, cwd, func(ref ChanRef) ([]channelItem, error) {
		stage, ok := resolveTranslatedRef(ref.Name, scope, translated)
		if !ok {
			return nil, fmt.Errorf("unknown upstream reference %q", ref.Name)
		}
		if len(stage.items) > 0 {
			return cloneChannelItems(stage.items), nil
		}

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		return []channelItem{{value: channelItemValue(stage.outputPaths), depGroups: deps}}, nil
	})
}

func resolveArg(arg ChanExpr, scope []string, translated map[string]translatedCall) ([]string, []string, error) {
	switch expr := arg.(type) {
	case ChanRef:
		stage, ok := resolveTranslatedRef(expr.Name, scope, translated)
		if !ok {
			return nil, nil, fmt.Errorf("unknown upstream reference %q", expr.Name)
		}

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		return cloneStrings(stage.outputPaths), deps, nil
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

func deterministicCwd(base, runID string, scope []string, processName string) string {
	parts := []string{base, "nf-work", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName)

	return filepath.Clean(filepath.Join(parts...))
}

func deterministicIndexedCwd(base, runID string, scope []string, processName string, itemIndex int) string {
	parts := []string{base, "nf-work", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName, strconv.Itoa(itemIndex))

	return filepath.Clean(filepath.Join(parts...))
}

func scopedDepGroup(runID string, scope []string, processName string) string {
	parts := []string{"nf", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName)

	return strings.Join(parts, ".")
}

func scopedIndexedDepGroup(runID string, scope []string, processName string, itemIndex int) string {
	parts := []string{"nf", runID}
	parts = append(parts, scope...)
	parts = append(parts, processName, strconv.Itoa(itemIndex))

	return strings.Join(parts, ".")
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

func buildRequirements(proc *Process, defaults *ProcessDefaults, params map[string]any) (*scheduler.Requirements, error) {
	req := &scheduler.Requirements{}

	cpus, err := resolveDirectiveInt("cpus", proc.Directives["cpus"], params, intDefault(defaults.Cpus, defaultCPUs))
	if err != nil {
		return nil, err
	}
	memory, err := resolveDirectiveInt("memory", proc.Directives["memory"], params, intDefault(defaults.Memory, defaultMemory))
	if err != nil {
		return nil, err
	}
	timeMinutes, err := resolveDirectiveInt("time", proc.Directives["time"], params, intDefault(defaults.Time, defaultTime))
	if err != nil {
		return nil, err
	}
	disk, err := resolveDirectiveInt("disk", proc.Directives["disk"], params, intDefault(defaults.Disk, defaultDisk))
	if err != nil {
		return nil, err
	}

	req.Cores = float64(cpus)
	req.CoresSet = true
	req.RAM = memory
	req.Time = time.Duration(timeMinutes) * time.Minute
	req.Disk = disk
	req.DiskSet = true

	return req, nil
}

func resolveDirectiveInt(name string, expr Expr, params map[string]any, fallback int) (int, error) {
	if expr == nil {
		return fallback, nil
	}
	if unsupported, ok := expr.(UnsupportedExpr); ok {
		warnf("nextflowdsl: falling back for %s directive with unsupported expression %q\n", name, unsupported.Text)
		return fallback, nil
	}

	value, err := EvalExpr(expr, exprVars(params))
	if err != nil {
		return 0, err
	}

	intValue, ok := value.(int)
	if !ok {
		return 0, fmt.Errorf("%s directive must evaluate to an integer", name)
	}

	return intValue, nil
}

func warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

func intDefault(value, fallback int) int {
	if value != 0 {
		return value
	}

	return fallback
}

func applyContainer(job *jobqueue.Job, proc *Process, defaults *ProcessDefaults, runtime string) {
	container := proc.Container
	if container == "" && defaults != nil {
		container = defaults.Container
	}
	if container == "" {
		return
	}

	switch runtime {
	case "docker":
		job.WithDocker = container
	case "singularity":
		job.WithSingularity = container
	}
}

func applyMaxForks(job *jobqueue.Job, proc *Process) {
	if proc.MaxForks > 0 {
		job.LimitGroups = []string{fmt.Sprintf("%s:%d", proc.Name, proc.MaxForks)}
	}
}

func applyErrorStrategy(job *jobqueue.Job, proc *Process) {
	switch proc.ErrorStrat {
	case "", "terminate":
		job.Retries = 0
	case "retry":
		job.Retries = uint8(proc.MaxRetries)
	case "ignore":
		job.Retries = 0
		job.Behaviours = append(job.Behaviours, &jobqueue.Behaviour{When: jobqueue.OnFailure, Do: jobqueue.Remove})
	default:
		warnf("nextflowdsl: unsupported errorStrategy %q, using terminate semantics\n", proc.ErrorStrat)
		job.Retries = 0
	}
}

func applyEnv(job *jobqueue.Job, proc *Process, defaults *ProcessDefaults) error {
	defaultEnvSize := 0
	if defaults != nil {
		defaultEnvSize = len(defaults.Env)
	}

	env := make([]string, 0, len(proc.Env)+defaultEnvSize)
	seen := map[string]struct{}{}
	if defaults != nil {
		for key, value := range defaults.Env {
			env = append(env, key+"="+value)
			seen[key] = struct{}{}
		}
	}
	for key, value := range proc.Env {
		if _, ok := seen[key]; ok {
			for index, entry := range env {
				if strings.HasPrefix(entry, key+"=") {
					env[index] = key + "=" + value
					break
				}
			}
			continue
		}
		env = append(env, key+"="+value)
	}
	if len(env) == 0 {
		return nil
	}

	return job.EnvAddOverride(env)
}

func buildCommand(proc *Process, bindings []string, params map[string]any) (string, error) {
	script, err := SubstituteParams(proc.Script, params)
	if err != nil {
		return "", err
	}
	body := script

	prefixes := make([]string, 0, len(bindings))
	for index, binding := range bindings {
		if binding == "" {
			continue
		}
		name := fmt.Sprintf("NF_INPUT_%d", index+1)
		if index < len(proc.Input) && proc.Input[index] != nil && proc.Input[index].Name != "" {
			name = proc.Input[index].Name
		}
		prefixes = append(prefixes, fmt.Sprintf("export %s=%s", name, shellQuote(binding)))
	}
	if len(prefixes) > 0 {
		body = strings.Join(append(prefixes, script), "\n")
	}

	return fmt.Sprintf("{ %s; } > %s 2> %s", body, nfStdoutFile, nfStderrFile), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func outputPaths(proc *Process, cwd string) []string {
	paths := []string{}
	for _, decl := range proc.Output {
		if decl == nil || (decl.Kind != "path" && decl.Kind != "file") {
			continue
		}
		if stringExpr, ok := decl.Expr.(StringExpr); ok {
			paths = append(paths, filepath.Join(cwd, filepath.Clean(stringExpr.Value)))
		}
	}
	if len(paths) > 0 {
		return paths
	}

	return []string{cwd + string(os.PathSeparator)}
}

func applyPublishDirBehaviours(job *jobqueue.Job, proc *Process, params map[string]any, tc TranslateConfig) error {
	if len(proc.PublishDir) == 0 {
		return nil
	}

	for _, directive := range proc.PublishDir {
		if directive == nil {
			continue
		}

		command, err := buildPublishDirCommand(proc, directive, params, tc)
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

func buildPublishDirCommand(proc *Process, directive *PublishDir, params map[string]any, tc TranslateConfig) (string, error) {
	target, err := resolvePublishDirTarget(directive.Path, params, tc)
	if err != nil {
		return "", err
	}

	target = ensureTrailingSeparator(target)
	mkdir := fmt.Sprintf("mkdir -p %s", shellQuote(target))

	if directive.Pattern != "" {
		return strings.Join([]string{
			mkdir,
			"shopt -s nullglob",
			fmt.Sprintf("for path in %s; do %s; done", directive.Pattern, publishDirActionCommand(directive.Mode, "$path", target)),
		}, " && "), nil
	}

	sources := publishDirSources(proc)
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

func ensureTrailingSeparator(path string) string {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}

	return path + string(os.PathSeparator)
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

func publishDirSources(proc *Process) []string {
	if len(proc.Output) == 0 {
		return nil
	}

	sources := make([]string, 0, len(proc.Output))
	for _, decl := range proc.Output {
		if decl == nil || (decl.Kind != "path" && decl.Kind != "file") {
			continue
		}

		stringExpr, ok := decl.Expr.(StringExpr)
		if !ok || stringExpr.Value == "" {
			continue
		}

		sources = append(sources, filepath.Clean(stringExpr.Value))
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
	if len(awaitDepGrps) == 0 {
		return nil, nil, nil
	}

	return awaitDepGrps, awaitRepGrps, nil
}

func detectPendingArg(arg ChanExpr, scope []string, translated map[string]translatedCall) ([]string, []string, error) {
	switch expr := arg.(type) {
	case ChanRef:
		stage, ok := resolveTranslatedRef(expr.Name, scope, translated)
		if !ok {
			return nil, nil, fmt.Errorf("unknown upstream reference %q", expr.Name)
		}
		if !stage.dynamicOutput && !stage.pending {
			return nil, nil, nil
		}

		deps := cloneStrings(stage.depGroups)
		if len(deps) == 0 && stage.depGroup != "" {
			deps = []string{stage.depGroup}
		}

		return deps, []string{stage.repGroup}, nil
	case ChannelChain:
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

func hasDynamicOutputs(proc *Process) bool {
	if proc == nil {
		return false
	}

	// D4 classifies all path/file outputs as dynamic, so only val-only edges can
	// stay fully materialized in the initial D1 translation pass.
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

func cloneTranslatedCalls(translated map[string]translatedCall) map[string]translatedCall {
	if len(translated) == 0 {
		return nil
	}

	clone := make(map[string]translatedCall, len(translated))
	for key, value := range translated {
		value.depGroups = cloneStrings(value.depGroups)
		value.outputPaths = cloneStrings(value.outputPaths)
		value.items = cloneChannelItems(value.items)
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

func channelItemValue(paths []string) any {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return paths[0]
	}

	return cloneStrings(paths)
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
		defaults = mergeDefaults(defaults, cfg.Process)
		if profileName != "" && cfg.Profiles != nil {
			if profile, ok := cfg.Profiles[profileName]; ok {
				defaults = mergeDefaults(defaults, profile.Process)
			}
		}
	}

	return defaults
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

func cloneDefaults(defaults *ProcessDefaults) *ProcessDefaults {
	if defaults == nil {
		return &ProcessDefaults{}
	}

	clone := &ProcessDefaults{
		Cpus:      defaults.Cpus,
		Memory:    defaults.Memory,
		Time:      defaults.Time,
		Disk:      defaults.Disk,
		Container: defaults.Container,
	}
	if len(defaults.Env) > 0 {
		clone.Env = make(map[string]string, len(defaults.Env))
		for key, value := range defaults.Env {
			clone.Env[key] = value
		}
	}

	return clone
}
