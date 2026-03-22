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
func ResolveWorkflowPath(path string, remoteResolver ModuleResolver) (resolvedPath string, resolvedRemotely bool, err error) {
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
	}

	loaded[path] = cloneWorkflow(merged)

	return merged, nil
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
	}

	return moduleWF, nil
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

		for _, call := range subwf.Body.Calls {
			if call == nil {
				continue
			}
			if _, ok := processes[call.Target]; ok {
				queue = append(queue, call.Target)
				continue
			}
			if _, ok := subworkflows[call.Target]; ok {
				queue = append(queue, call.Target)
			}
		}

		finalName := importedName(name, importNode)
		if _, exists := selectedNames[finalName]; exists {
			continue
		}

		selectedSubWFs = append(selectedSubWFs, cloneSubWorkflow(subwf, finalName, aliasByOriginal))
		selectedNames[finalName] = struct{}{}
	}

	return &Workflow{Processes: selectedProcesses, SubWFs: selectedSubWFs, Functions: cloneFuncDefs(moduleWF.Functions)}, nil
}

func importedName(name string, importNode *Import) string {
	if importNode != nil && importNode.Alias != nil {
		if alias, ok := importNode.Alias[name]; ok {
			return alias
		}
	}

	return name
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
		imports = append(imports, &Import{Names: append([]string{}, importNode.Names...), Source: importNode.Source, Alias: cloneAliasMap(importNode.Alias)})
	}

	return &Workflow{
		Name:      wf.Name,
		Processes: cloneProcesses(wf.Processes),
		SubWFs:    cloneSubWorkflows(wf.SubWFs, nil),
		Imports:   imports,
		EntryWF:   cloneWorkflowBlock(wf.EntryWF, nil),
		Functions: cloneFuncDefs(wf.Functions),
	}
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
		publishDirs = append(publishDirs, &PublishDir{Path: publishDir.Path, Pattern: publishDir.Pattern, Mode: publishDir.Mode})
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

	return &WorkflowBlock{Calls: clonedCalls, Take: clonedTake, Emit: clonedEmit}
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

func cloneFuncDefs(funcDefs []*FuncDef) []*FuncDef {
	cloned := make([]*FuncDef, 0, len(funcDefs))
	for _, funcDef := range funcDefs {
		if funcDef == nil {
			continue
		}

		cloned = append(cloned, &FuncDef{Name: funcDef.Name, Params: append([]string{}, funcDef.Params...), Body: funcDef.Body})
	}

	return cloned
}

func cloneChanExprs(expressions []ChanExpr, renameTargets map[string]string) []ChanExpr {
	cloned := make([]ChanExpr, 0, len(expressions))
	for _, expression := range expressions {
		cloned = append(cloned, cloneChanExpr(expression, renameTargets))
	}

	return cloned
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
	case ChannelFactory:
		return ChannelFactory{Name: value.Name, Args: append([]Expr{}, value.Args...)}
	case ChannelChain:
		operators := make([]ChannelOperator, 0, len(value.Operators))
		for _, operator := range value.Operators {
			operators = append(operators, ChannelOperator{
				Name:     operator.Name,
				Args:     append([]Expr{}, operator.Args...),
				Channels: cloneChanExprs(operator.Channels, renameTargets),
				Closure:  operator.Closure,
			})
		}

		return ChannelChain{Source: cloneChanExpr(value.Source, renameTargets), Operators: operators}
	case PipeExpr:
		return PipeExpr{Stages: cloneChanExprs(value.Stages, renameTargets)}
	default:
		return expression
	}
}

func renderChanExpr(expression ChanExpr) string {
	switch value := expression.(type) {
	case ChanRef:
		return value.Name
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
	case CastExpr:
		return renderExpr(value.Operand) + " as " + value.TypeName
	case UnsupportedExpr:
		return value.Text
	default:
		return ""
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
