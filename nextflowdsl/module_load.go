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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LoadWorkflowFile parses a workflow file and resolves any imported modules
// into a single merged AST ready for translation.
func LoadWorkflowFile(path string, remoteResolver ModuleResolver) (*Workflow, error) {
	resolvedPath, _, err := ResolveWorkflowPath(path, remoteResolver)
	if err != nil {
		return nil, err
	}

	loaded := make(map[string]*Workflow)
	loading := make(map[string]struct{})

	return loadWorkflowFile(resolvedPath, remoteResolver, loaded, loading)
}

// ResolveWorkflowPath resolves a top-level workflow identifier to a local
// workflow entry file, using the remote resolver when the workflow is not
// present locally.
func ResolveWorkflowPath(
	path string,
	remoteResolver ModuleResolver,
) (resolvedPath string, resolvedRemotely bool, err error) {
	localPath, localErr := resolveLocalWorkflowPath(path)
	if localErr == nil {
		return localPath, false, nil
	}

	if remoteResolver == nil {
		return "", false, fmt.Errorf("resolve workflow path %s: %w", path, localErr)
	}

	remotePath, remoteErr := remoteResolver.Resolve(path)
	if remoteErr != nil {
		if looksLikeRemoteWorkflowSpec(path) {
			return "", false, fmt.Errorf("resolve workflow path %s: %w", path, remoteErr)
		}

		return "", false, fmt.Errorf("resolve workflow path %s: %w", path, localErr)
	}

	entryPath, err := workflowEntryPath(remotePath)
	if err != nil {
		return "", false, fmt.Errorf("resolve workflow path %s: %w", path, err)
	}

	return entryPath, true, nil
}

func resolveLocalWorkflowPath(path string) (string, error) {
	resolvedPath := path
	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve workflow path %s: %w", path, err)
		}

		resolvedPath = absPath
	}

	return workflowEntryPath(resolvedPath)
}

func looksLikeRemoteWorkflowSpec(path string) bool {
	spec, _, _ := strings.Cut(strings.TrimSpace(path), "@")
	if spec == "" {
		return false
	}

	if strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "http://") {
		return true
	}

	parts := strings.Split(strings.Trim(spec, "/"), "/")

	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && filepath.Ext(parts[1]) == ""
}

func workflowEntryPath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("workflow path %q does not exist", path)
		}

		return "", fmt.Errorf("stat workflow path %q: %w", path, err)
	}

	if !info.IsDir() {
		return path, nil
	}

	entryPath := filepath.Join(path, "main.nf")

	entryInfo, err := os.Stat(entryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("workflow directory %q does not contain main.nf", path)
		}

		return "", fmt.Errorf("stat workflow entry %q: %w", entryPath, err)
	}

	if entryInfo.IsDir() {
		return "", fmt.Errorf("workflow entry %q is a directory", entryPath)
	}

	return entryPath, nil
}

func cloneAliasMap(alias map[string]string) map[string]string {
	if len(alias) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(alias))
	for key, value := range alias {
		cloned[key] = value
	}

	return cloned
}

func moduleWorkflowFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat module path %q: %w", path, err)
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	files := []string{}

	walkErr := filepath.WalkDir(path, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		if filepath.Ext(currentPath) == ".nf" {
			files = append(files, currentPath)
		}

		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan module path %q: %w", path, walkErr)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("module path %q did not contain any .nf files", path)
	}

	sort.Strings(files)

	return files, nil
}

func loadWorkflowFile(path string, remoteResolver ModuleResolver, loaded map[string]*Workflow, loading map[string]struct{}) (*Workflow, error) {
	if wf, ok := loaded[path]; ok {
		return cloneWorkflow(wf), nil
	}

	if _, ok := loading[path]; ok {
		return nil, fmt.Errorf("workflow import cycle detected at %q", path)
	}

	loading[path] = struct{}{}
	defer delete(loading, path)

	workflowFile, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open workflow %s: %w", path, err)
	}

	defer func() {
		_ = workflowFile.Close()
	}()

	wf, err := Parse(workflowFile)
	if err != nil {
		return nil, err
	}

	merged := cloneWorkflow(wf)
	if len(wf.Imports) == 0 {
		loaded[path] = cloneWorkflow(merged)

		return merged, nil
	}

	for _, importNode := range wf.Imports {
		moduleWF, err := loadImportedWorkflow(path, importNode, remoteResolver, loaded, loading)
		if err != nil {
			return nil, err
		}

		selected, err := selectImportedDefinitions(moduleWF, importNode)
		if err != nil {
			return nil, err
		}

		merged.Processes = append(merged.Processes, selected.Processes...)
		merged.SubWFs = append(merged.SubWFs, selected.SubWFs...)
		merged.Enums = append(merged.Enums, selected.Enums...)
		merged.Records = append(merged.Records, cloneRecordDefs(selected.Records)...)
		merged.ParamBlock = append(merged.ParamBlock, cloneParamDecls(selected.ParamBlock)...)
		merged.OutputBlock = mergeOutputBlocks(merged.OutputBlock, selected.OutputBlock)
	}

	loaded[path] = cloneWorkflow(merged)

	return merged, nil
}

func cloneWorkflow(wf *Workflow) *Workflow {
	if wf == nil {
		return nil
	}

	imports := make([]*Import, 0, len(wf.Imports))
	for _, importNode := range wf.Imports {
		if importNode == nil {
			continue
		}

		imports = append(imports, &Import{
			Names:  append([]string{}, importNode.Names...),
			Source: importNode.Source,
			Alias:  cloneAliasMap(importNode.Alias),
		})
	}

	return &Workflow{
		Name:        wf.Name,
		Processes:   cloneProcesses(wf.Processes),
		SubWFs:      cloneSubWorkflows(wf.SubWFs, nil),
		Imports:     imports,
		EntryWF:     cloneWorkflowBlock(wf.EntryWF, nil),
		Functions:   cloneFuncDefs(wf.Functions),
		Enums:       cloneEnumDefs(wf.Enums),
		OutputBlock: wf.OutputBlock,
		ParamBlock:  cloneParamDecls(wf.ParamBlock),
		Records:     cloneRecordDefs(wf.Records),
	}
}

func loadImportedWorkflow(
	parentPath string,
	importNode *Import,
	remoteResolver ModuleResolver,
	loaded map[string]*Workflow,
	loading map[string]struct{},
) (*Workflow, error) {
	resolver := NewChainResolver(NewLocalResolver(filepath.Dir(parentPath)), remoteResolver)

	resolvedPath, err := resolver.Resolve(importNode.Source)
	if err != nil {
		return nil, err
	}

	moduleFiles, err := moduleWorkflowFiles(resolvedPath)
	if err != nil {
		return nil, err
	}

	moduleWF := &Workflow{}

	for _, moduleFile := range moduleFiles {
		loadedWorkflow, err := loadWorkflowFile(moduleFile, remoteResolver, loaded, loading)
		if err != nil {
			return nil, err
		}

		moduleWF.Processes = append(moduleWF.Processes, cloneProcesses(loadedWorkflow.Processes)...)
		moduleWF.SubWFs = append(moduleWF.SubWFs, cloneSubWorkflows(loadedWorkflow.SubWFs, nil)...)
		moduleWF.Enums = append(moduleWF.Enums, cloneEnumDefs(loadedWorkflow.Enums)...)
		moduleWF.Records = append(moduleWF.Records, cloneRecordDefs(loadedWorkflow.Records)...)
		moduleWF.ParamBlock = append(moduleWF.ParamBlock, cloneParamDecls(loadedWorkflow.ParamBlock)...)
		moduleWF.OutputBlock = mergeOutputBlocks(moduleWF.OutputBlock, loadedWorkflow.OutputBlock)
	}

	return moduleWF, nil
}

func selectImportedDefinitions(moduleWF *Workflow, importNode *Import) (*Workflow, error) {
	processes, err := processIndex(moduleWF.Processes)
	if err != nil {
		return nil, err
	}

	subworkflows, err := subWorkflowIndex(moduleWF.SubWFs)
	if err != nil {
		return nil, err
	}

	aliasByOriginal := importNode.Alias
	requested := make(map[string]struct{}, len(importNode.Names))
	queue := append([]string{}, importNode.Names...)
	selectedProcesses := []*Process{}
	selectedSubWFs := []*SubWorkflow{}
	selectedNames := make(map[string]struct{})
	visited := make(map[string]struct{}, len(queue))

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		if _, ok := visited[name]; ok {
			continue
		}

		visited[name] = struct{}{}
		if _, ok := requested[name]; !ok {
			requested[name] = struct{}{}
		}

		if proc, ok := processes[name]; ok {
			finalName := importedName(name, importNode)
			if _, exists := selectedNames[finalName]; !exists {
				selectedProcesses = append(selectedProcesses, cloneProcess(proc, finalName))
				selectedNames[finalName] = struct{}{}
			}

			continue
		}

		subwf, ok := subworkflows[name]
		if !ok {
			return nil, fmt.Errorf("module %q does not export %q", importNode.Source, name)
		}

		queue = append(queue, workflowDependencyTargets(subwf.Body, processes, subworkflows)...)

		finalName := importedName(name, importNode)
		if _, exists := selectedNames[finalName]; exists {
			continue
		}

		selectedSubWFs = append(selectedSubWFs, cloneSubWorkflow(subwf, finalName, aliasByOriginal))
		selectedNames[finalName] = struct{}{}
	}

	return &Workflow{
		Processes:   selectedProcesses,
		SubWFs:      selectedSubWFs,
		Functions:   cloneFuncDefs(moduleWF.Functions),
		Enums:       cloneEnumDefs(moduleWF.Enums),
		OutputBlock: moduleWF.OutputBlock,
		ParamBlock:  cloneParamDecls(moduleWF.ParamBlock),
		Records:     cloneRecordDefs(moduleWF.Records),
	}, nil
}

func importedName(name string, importNode *Import) string {
	if importNode != nil && importNode.Alias != nil {
		if alias, ok := importNode.Alias[name]; ok {
			return alias
		}
	}

	return name
}

func cloneProcesses(processes []*Process) []*Process {
	cloned := make([]*Process, 0, len(processes))
	for _, proc := range processes {
		if proc == nil {
			continue
		}

		cloned = append(cloned, cloneProcess(proc, proc.Name))
	}

	return cloned
}

func cloneProcess(proc *Process, name string) *Process {
	if proc == nil {
		return nil
	}

	directives := make(map[string]any, len(proc.Directives))
	for key, value := range proc.Directives {
		directives[key] = value
	}

	publishDirs := make([]*PublishDir, 0, len(proc.PublishDir))
	for _, publishDir := range proc.PublishDir {
		if publishDir == nil {
			continue
		}

		publishDirs = append(publishDirs, &PublishDir{
			Path:    publishDir.Path,
			Pattern: publishDir.Pattern,
			Mode:    publishDir.Mode,
		})
	}

	return &Process{
		Name:         name,
		Labels:       append([]string{}, proc.Labels...),
		Tag:          proc.Tag,
		BeforeScript: proc.BeforeScript,
		AfterScript:  proc.AfterScript,
		Module:       proc.Module,
		Cache:        proc.Cache,
		Directives:   directives,
		Input:        cloneDeclarations(proc.Input),
		Output:       cloneDeclarations(proc.Output),
		Script:       proc.Script,
		Stub:         proc.Stub,
		Exec:         proc.Exec,
		Shell:        proc.Shell,
		When:         proc.When,
		Container:    proc.Container,
		PublishDir:   publishDirs,
		ErrorStrat:   proc.ErrorStrat,
		MaxRetries:   proc.MaxRetries,
		MaxForks:     proc.MaxForks,
		Env:          cloneEnv(proc.Env),
	}
}

func cloneDeclarations(declarations []*Declaration) []*Declaration {
	cloned := make([]*Declaration, 0, len(declarations))
	for _, declaration := range declarations {
		if declaration == nil {
			continue
		}

		cloned = append(cloned, &Declaration{
			Kind:     declaration.Kind,
			Name:     declaration.Name,
			Expr:     declaration.Expr,
			Raw:      declaration.Raw,
			Emit:     declaration.Emit,
			Each:     declaration.Each,
			Optional: declaration.Optional,
			Elements: cloneTupleElements(declaration.Elements),
		})
	}

	return cloned
}

func cloneTupleElements(elements []*TupleElement) []*TupleElement {
	cloned := make([]*TupleElement, 0, len(elements))
	for _, element := range elements {
		if element == nil {
			continue
		}

		cloned = append(cloned, &TupleElement{
			Kind: element.Kind,
			Name: element.Name,
			Expr: element.Expr,
			Raw:  element.Raw,
			Emit: element.Emit,
		})
	}

	return cloned
}

func cloneEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(env))
	for key, value := range env {
		cloned[key] = value
	}

	return cloned
}

func workflowDependencyTargets(block *WorkflowBlock, processes map[string]*Process, subworkflows map[string]*SubWorkflow) []string {
	if block == nil {
		return nil
	}

	targets := make([]string, 0, len(block.Calls))
	appendTargets := func(calls []*Call) {
		for _, call := range calls {
			if call == nil {
				continue
			}

			if _, ok := processes[call.Target]; ok {
				targets = append(targets, call.Target)

				continue
			}

			if _, ok := subworkflows[call.Target]; ok {
				targets = append(targets, call.Target)
			}
		}
	}

	appendTargets(block.Calls)

	for _, condition := range block.Conditions {
		targets = append(targets, ifBlockDependencyTargets(condition, processes, subworkflows)...)
	}

	return targets
}

func ifBlockDependencyTargets(block *IfBlock, processes map[string]*Process, subworkflows map[string]*SubWorkflow) []string {
	if block == nil {
		return nil
	}

	targets := make([]string, 0, len(block.Body)+len(block.ElseBody))
	appendTargets := func(calls []*Call) {
		for _, call := range calls {
			if call == nil {
				continue
			}

			if _, ok := processes[call.Target]; ok {
				targets = append(targets, call.Target)

				continue
			}

			if _, ok := subworkflows[call.Target]; ok {
				targets = append(targets, call.Target)
			}
		}
	}

	appendTargets(block.Body)
	appendTargets(block.ElseBody)

	for _, elseIf := range block.ElseIf {
		targets = append(targets, ifBlockDependencyTargets(elseIf, processes, subworkflows)...)
	}

	return targets
}

func cloneSubWorkflows(subworkflows []*SubWorkflow, renameTargets map[string]string) []*SubWorkflow {
	cloned := make([]*SubWorkflow, 0, len(subworkflows))
	for _, subwf := range subworkflows {
		if subwf == nil {
			continue
		}

		cloned = append(cloned, cloneSubWorkflow(subwf, subwf.Name, renameTargets))
	}

	return cloned
}

func cloneSubWorkflow(subwf *SubWorkflow, name string, renameTargets map[string]string) *SubWorkflow {
	if subwf == nil {
		return nil
	}

	return &SubWorkflow{Name: name, Body: cloneWorkflowBlock(subwf.Body, renameTargets)}
}

func cloneWorkflowBlock(block *WorkflowBlock, renameTargets map[string]string) *WorkflowBlock {
	if block == nil {
		return nil
	}

	clonedCalls := make([]*Call, 0, len(block.Calls))
	clonedTake := append([]string{}, block.Take...)

	clonedEmit := make([]*WFEmit, 0, len(block.Emit))
	for _, call := range block.Calls {
		if call == nil {
			continue
		}

		target := call.Target
		if renameTargets != nil {
			if renamed, ok := renameTargets[target]; ok {
				target = renamed
			}
		}

		clonedCalls = append(clonedCalls, &Call{Target: target, Args: cloneChanExprs(call.Args, renameTargets)})
	}

	for _, emit := range block.Emit {
		if emit == nil {
			continue
		}

		clonedEmit = append(clonedEmit, &WFEmit{Name: emit.Name, Expr: cloneWorkflowEmitExpr(emit.Expr, renameTargets)})
	}

	clonedPublish := make([]*WFPublish, 0, len(block.Publish))
	for _, publish := range block.Publish {
		if publish == nil {
			continue
		}

		clonedPublish = append(clonedPublish, &WFPublish{
			Target: publish.Target,
			Source: cloneWorkflowEmitExpr(publish.Source, renameTargets),
		})
	}

	return &WorkflowBlock{
		Calls:      clonedCalls,
		Take:       clonedTake,
		Emit:       clonedEmit,
		Publish:    clonedPublish,
		OnComplete: block.OnComplete,
		OnError:    block.OnError,
		Conditions: cloneIfBlocks(block.Conditions, renameTargets),
	}
}

func cloneFuncDefs(funcDefs []*FuncDef) []*FuncDef {
	cloned := make([]*FuncDef, 0, len(funcDefs))
	for _, funcDef := range funcDefs {
		if funcDef == nil {
			continue
		}

		cloned = append(cloned, &FuncDef{
			Name:   funcDef.Name,
			Params: append([]string{}, funcDef.Params...),
			Body:   funcDef.Body,
		})
	}

	return cloned
}

func cloneEnumDefs(enumDefs []*EnumDef) []*EnumDef {
	cloned := make([]*EnumDef, 0, len(enumDefs))
	for _, enumDef := range enumDefs {
		if enumDef == nil {
			continue
		}

		cloned = append(cloned, &EnumDef{Name: enumDef.Name, Values: append([]string{}, enumDef.Values...)})
	}

	return cloned
}

func renderChanExpr(expression ChanExpr) string {
	switch value := expression.(type) {
	case ChanRef:
		return value.Name
	case NamedChannelRef:
		return renderChanExpr(value.Source) + "." + value.Label
	case ChannelFactory:
		args := make([]string, 0, len(value.Args))
		for _, arg := range value.Args {
			args = append(args, renderExpr(arg))
		}

		return fmt.Sprintf("Channel.%s(%s)", value.Name, strings.Join(args, ", "))
	case ChannelChain:
		var builder strings.Builder
		builder.WriteString(renderChanExpr(value.Source))

		for _, operator := range value.Operators {
			builder.WriteByte('.')
			builder.WriteString(operator.Name)

			switch {
			case operator.Closure != "":
				builder.WriteString(" {")

				if operator.Closure != "" {
					builder.WriteByte(' ')
					builder.WriteString(operator.Closure)
					builder.WriteByte(' ')
				}

				builder.WriteByte('}')
			case len(operator.Channels) > 0:
				parts := make([]string, 0, len(operator.Channels))
				for _, channel := range operator.Channels {
					parts = append(parts, renderChanExpr(channel))
				}

				builder.WriteByte('(')
				builder.WriteString(strings.Join(parts, ", "))
				builder.WriteByte(')')
			default:
				parts := make([]string, 0, len(operator.Args))
				for _, arg := range operator.Args {
					parts = append(parts, renderExpr(arg))
				}

				builder.WriteByte('(')
				builder.WriteString(strings.Join(parts, ", "))
				builder.WriteByte(')')
			}
		}

		return builder.String()
	case PipeExpr:
		parts := make([]string, 0, len(value.Stages))
		for _, stage := range value.Stages {
			parts = append(parts, renderChanExpr(stage))
		}

		return strings.Join(parts, " | ")
	default:
		return ""
	}
}

func renderExpr(expression Expr) string {
	switch value := expression.(type) {
	case IntExpr:
		return strconv.Itoa(value.Value)
	case StringExpr:
		return strconv.Quote(value.Value)
	case ParamsExpr:
		return "params." + value.Path
	case BoolExpr:
		if value.Value {
			return "true"
		}

		return "false"
	case VarExpr:
		if value.Path == "" {
			return value.Root
		}

		return value.Root + "." + value.Path
	case UnaryExpr:
		return value.Op + renderExpr(value.Operand)
	case BinaryExpr:
		return renderExpr(value.Left) + " " + value.Op + " " + renderExpr(value.Right)
	case TernaryExpr:
		if value.Cond == nil {
			return renderExpr(value.True) + " ?: " + renderExpr(value.False)
		}

		return renderExpr(value.Cond) + " ? " + renderExpr(value.True) + " : " + renderExpr(value.False)
	case MethodCallExpr:
		parts := make([]string, 0, len(value.Args))
		for _, arg := range value.Args {
			parts = append(parts, renderExpr(arg))
		}

		return renderExpr(value.Receiver) + "." + value.Method + "(" + strings.Join(parts, ", ") + ")"
	case IndexExpr:
		return renderExpr(value.Receiver) + "[" + renderExpr(value.Index) + "]"
	case ListExpr:
		parts := make([]string, 0, len(value.Elements))
		for _, element := range value.Elements {
			parts = append(parts, renderExpr(element))
		}

		return "[" + strings.Join(parts, ", ") + "]"
	case MapExpr:
		parts := make([]string, 0, len(value.Keys))
		for index, key := range value.Keys {
			parts = append(parts, renderExpr(key)+": "+renderExpr(value.Values[index]))
		}

		return "[" + strings.Join(parts, ", ") + "]"
	case ClosureExpr:
		if len(value.Params) == 0 {
			return "{ " + value.Body + " }"
		}

		return "{ " + strings.Join(value.Params, ", ") + " -> " + value.Body + " }"
	case CastExpr:
		return renderExpr(value.Operand) + " as " + value.TypeName
	case UnsupportedExpr:
		return value.Text
	default:
		return ""
	}
}

func cloneWorkflowEmitExpr(expression string, renameTargets map[string]string) string {
	if expression == "" || len(renameTargets) == 0 {
		return expression
	}

	parsed, err := parseChanExprText(expression)
	if err != nil {
		return expression
	}

	return renderChanExpr(cloneChanExpr(parsed, renameTargets))
}

func cloneChanExpr(expression ChanExpr, renameTargets map[string]string) ChanExpr {
	switch value := expression.(type) {
	case ChanRef:
		name := value.Name

		for _, original := range sortedRenameTargets(renameTargets) {
			renamed := renameTargets[original]

			prefix := original + "."
			if name == original {
				name = renamed

				break
			}

			if len(name) > len(prefix) && name[:len(prefix)] == prefix {
				name = renamed + name[len(original):]

				break
			}
		}

		return ChanRef{Name: name}
	case NamedChannelRef:
		return NamedChannelRef{Source: cloneChanExpr(value.Source, renameTargets), Label: value.Label}
	case ChannelFactory:
		return ChannelFactory{Name: value.Name, Args: append([]Expr{}, value.Args...)}
	case ChannelChain:
		operators := make([]ChannelOperator, 0, len(value.Operators))
		for _, operator := range value.Operators {
			var closureExpr *ClosureExpr

			if operator.ClosureExpr != nil {
				clonedClosure := *operator.ClosureExpr
				clonedClosure.Params = append([]string{}, operator.ClosureExpr.Params...)
				closureExpr = &clonedClosure
			}

			operators = append(operators, ChannelOperator{
				Name:        operator.Name,
				Args:        append([]Expr{}, operator.Args...),
				Channels:    cloneChanExprs(operator.Channels, renameTargets),
				Closure:     operator.Closure,
				ClosureExpr: closureExpr,
			})
		}

		return ChannelChain{Source: cloneChanExpr(value.Source, renameTargets), Operators: operators}
	case PipeExpr:
		return PipeExpr{Stages: cloneChanExprs(value.Stages, renameTargets)}
	default:
		return expression
	}
}

func sortedRenameTargets(renameTargets map[string]string) []string {
	keys := make([]string, 0, len(renameTargets))
	for key := range renameTargets {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}

		return keys[i] < keys[j]
	})

	return keys
}

func cloneChanExprs(expressions []ChanExpr, renameTargets map[string]string) []ChanExpr {
	cloned := make([]ChanExpr, 0, len(expressions))
	for _, expression := range expressions {
		cloned = append(cloned, cloneChanExpr(expression, renameTargets))
	}

	return cloned
}

func cloneIfBlocks(blocks []*IfBlock, renameTargets map[string]string) []*IfBlock {
	if len(blocks) == 0 {
		return nil
	}

	cloned := make([]*IfBlock, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}

		cloned = append(cloned, cloneIfBlock(block, renameTargets))
	}

	return cloned
}

func cloneIfBlock(block *IfBlock, renameTargets map[string]string) *IfBlock {
	if block == nil {
		return nil
	}

	cloneCalls := func(calls []*Call) []*Call {
		cloned := make([]*Call, 0, len(calls))
		for _, call := range calls {
			if call == nil {
				continue
			}

			target := call.Target
			if renameTargets != nil {
				if renamed, ok := renameTargets[target]; ok {
					target = renamed
				}
			}

			cloned = append(cloned, &Call{Target: target, Args: cloneChanExprs(call.Args, renameTargets)})
		}

		return cloned
	}

	return &IfBlock{
		Condition: block.Condition,
		Body:      cloneCalls(block.Body),
		ElseIf:    cloneIfBlocks(block.ElseIf, renameTargets),
		ElseBody:  cloneCalls(block.ElseBody),
	}
}

func cloneParamDecls(paramDecls []*ParamDecl) []*ParamDecl {
	cloned := make([]*ParamDecl, 0, len(paramDecls))
	for _, paramDecl := range paramDecls {
		if paramDecl == nil {
			continue
		}

		cloned = append(cloned, &ParamDecl{Name: paramDecl.Name, Type: paramDecl.Type, Default: paramDecl.Default})
	}

	return cloned
}

func cloneRecordDefs(recordDefs []*RecordDef) []*RecordDef {
	cloned := make([]*RecordDef, 0, len(recordDefs))
	for _, recordDef := range recordDefs {
		if recordDef == nil {
			continue
		}

		fields := make([]*RecordField, 0, len(recordDef.Fields))
		for _, field := range recordDef.Fields {
			if field == nil {
				continue
			}

			fields = append(fields, &RecordField{Name: field.Name, Type: field.Type, Default: field.Default})
		}

		cloned = append(cloned, &RecordDef{Name: recordDef.Name, Fields: fields})
	}

	return cloned
}

func mergeOutputBlocks(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)

	switch {
	case existing == "":
		return incoming
	case incoming == "":
		return existing
	default:
		return existing + "\n" + incoming
	}
}
