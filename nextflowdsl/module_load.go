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
)

// LoadWorkflowFile parses a workflow file and resolves any imported modules
// into a single merged AST ready for translation.
func LoadWorkflowFile(path string, remoteResolver ModuleResolver) (*Workflow, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve workflow path %s: %w", path, err)
	}

	loaded := make(map[string]*Workflow)
	return loadWorkflowFile(absPath, remoteResolver, loaded)
}

func loadWorkflowFile(path string, remoteResolver ModuleResolver, loaded map[string]*Workflow) (*Workflow, error) {
	if wf, ok := loaded[path]; ok {
		return cloneWorkflow(wf), nil
	}

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
		moduleWF, err := loadImportedWorkflow(path, importNode, remoteResolver, loaded)
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

func loadImportedWorkflow(parentPath string, importNode *Import, remoteResolver ModuleResolver, loaded map[string]*Workflow) (*Workflow, error) {
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
		loadedWorkflow, err := loadWorkflowFile(moduleFile, remoteResolver, loaded)
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

	return &Workflow{Processes: selectedProcesses, SubWFs: selectedSubWFs}, nil
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

	directives := make(map[string]Expr, len(proc.Directives))
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
		Name:       name,
		Directives: directives,
		Input:      cloneDeclarations(proc.Input),
		Output:     cloneDeclarations(proc.Output),
		Script:     proc.Script,
		Container:  proc.Container,
		PublishDir: publishDirs,
		ErrorStrat: proc.ErrorStrat,
		MaxRetries: proc.MaxRetries,
		MaxForks:   proc.MaxForks,
		Env:        cloneEnv(proc.Env),
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

	return &WorkflowBlock{Calls: clonedCalls}
}

func cloneDeclarations(declarations []*Declaration) []*Declaration {
	cloned := make([]*Declaration, 0, len(declarations))
	for _, declaration := range declarations {
		if declaration == nil {
			continue
		}
		cloned = append(cloned, &Declaration{Kind: declaration.Kind, Name: declaration.Name, Expr: declaration.Expr, Raw: declaration.Raw})
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