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
	"bytes"
	"context"
	"debug/elf"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const nextflowCompanionMissingCode = "E_RUNTIME_MISSING"

const nextflowCompanionPathCode = "E_SOURCE_PATH"

const nextflowShebangLimit = 256

const nextflowInterpreterLimit = 4096

type nextflowCompanions struct {
	cache, stage string
	lock         *lockRecord
	observations []nextflowLocalObservation
	resolved     map[string]int
}

func (collector *nextflowCompanions) index() {
	snapshots := map[string]int{}
	for index, item := range collector.lock.Artefacts {
		snapshots[filepath.Join(collector.cache, item.File.Path)] = index
	}

	for _, observation := range collector.observations {
		collector.resolved[observation.Resolved] = snapshots[observation.Snapshot]
	}
}

func (collector *nextflowCompanions) dependencies(ctx context.Context, index int, source string) error {
	inputs, script, err := nextflowCompanionPaths(ctx, source)
	if err != nil {
		return err
	}

	for _, input := range inputs {
		dependency, added, err := collector.acquireInput(input, script)
		if err != nil {
			return err
		}

		item := &collector.lock.Artefacts[index]

		item.Dependencies = append(item.Dependencies, collector.lock.Artefacts[dependency].ID)

		if !script || !added {
			continue
		}

		if err := collector.dependencies(ctx, dependency, input); err != nil {
			return err
		}
	}

	slices.Sort(collector.lock.Artefacts[index].Dependencies)
	collector.lock.Artefacts[index].Dependencies = slices.Compact(collector.lock.Artefacts[index].Dependencies)

	return nil
}

func nextflowCompanionPaths(ctx context.Context, source string) ([]string, bool, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	prefix, err := io.ReadAll(io.LimitReader(file, nextflowShebangLimit))
	if err != nil {
		return nil, false, err
	}

	if bytes.HasPrefix(prefix, []byte("#!")) {
		inputs, scriptErr := nextflowInterpreter(prefix, source)

		return inputs, true, scriptErr
	}

	inputs, err := nextflowELFCompanions(ctx, source)

	return inputs, false, err
}

func (collector *nextflowCompanions) acquire(source string) (int, bool, error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return 0, false, err
	}

	if index, found := collector.resolved[resolved]; found {
		return index, false, collector.observe(source, index)
	}

	id := "NEXTFLOW_COMPANION_" + strings.ToUpper(hashBytes([]byte(resolved)))
	name := filepath.Join("nextflow-companions", id, filepath.Base(source))

	item, observation, err := nextflowAcquireLocal(collector.cache, collector.stage, source, resolved, id, name)
	if err != nil {
		return 0, false, err
	}

	index := len(collector.lock.Artefacts)
	collector.lock.Artefacts = append(collector.lock.Artefacts, item)
	collector.observations = append(collector.observations, observation)
	collector.resolved[resolved] = index

	return index, true, nil
}

func (collector *nextflowCompanions) observe(source string, index int) error {
	observation, err := nextflowObserveLocal(source, collector.cache, collector.lock.Artefacts[index])
	if err != nil {
		return err
	}

	if !slices.Contains(collector.observations, observation) {
		collector.observations = append(collector.observations, observation)
	}

	return nil
}

func (collector *nextflowCompanions) acquireInput(source string, executable bool) (int, bool, error) {
	index, added, err := collector.acquire(source)
	if err != nil || !executable {
		return index, added, err
	}

	return index, added, nextflowExecutable(filepath.Join(collector.cache, collector.lock.Artefacts[index].File.Path))
}

func (collector *nextflowCompanions) launchers(ctx context.Context) error {
	for index, item := range collector.lock.Artefacts {
		if item.Role != roleRuntime && item.Role != roleLauncher {
			continue
		}

		if err := collector.dependencies(ctx, index, filepath.Join(collector.cache, item.File.Path)); err != nil {
			return err
		}
	}

	return nil
}

// Companions are observed on this host; this is not a portable package resolver.
// ldd lists transitive ELF inputs. Edges from each executable retain that list;
// companions are leaves, avoiding loader/libc cycles in the artefact DAG.
func nextflowAcquireCompanions(ctx context.Context, cache, stage string, lock *lockRecord,
	observations []nextflowLocalObservation) ([]nextflowLocalObservation, error) {
	collector := nextflowCompanions{cache: cache, stage: stage, lock: lock, observations: observations,
		resolved: map[string]int{}}
	collector.index()

	if err := collector.launchers(ctx); err != nil {
		return nil, err
	}

	for _, observation := range observations {
		index := collector.resolved[observation.Resolved]

		item := lock.Artefacts[index]

		java := strings.HasSuffix(item.File.Path, "/bin/java") || strings.HasSuffix(item.File.Path, "/lib/server/libjvm.so")
		if item.Role != roleEnvironment && !java {
			continue
		}

		if err := collector.dependencies(ctx, index, observation.Resolved); err != nil {
			return nil, err
		}
	}

	return collector.observations, nil
}

func nextflowInterpreter(prefix []byte, source string) ([]string, error) {
	inputs, err := nextflowInterpreterNames(prefix, source)
	if err != nil || len(inputs) != 2 {
		return inputs, err
	}

	command, _, err := nextflowToolSource(inputs[1])
	if err != nil {
		return nil, err
	}

	return []string{inputs[0], command}, nil
}

// Read only verified snapshots: offline checks must not resolve host PATH or use
// the unbound provenance sidecar. Dependency snapshots retain input basenames.
func nextflowVerifyInterpreters(cache string, items []artefact) error {
	byID := make(map[string]artefact, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}

	for _, item := range items {
		if len(item.Dependencies) == 0 {
			continue
		}

		if err := nextflowVerifyInterpreterEdges(cache, item, byID); err != nil {
			return err
		}
	}

	return nil
}

func nextflowVerifyInterpreterEdges(cache string, item artefact, byID map[string]artefact) error {
	inputs, err := nextflowReadInterpreters(filepath.Join(cache, item.File.Path))
	if err != nil {
		return err
	}

	for _, id := range item.Dependencies {
		dependency, found := byID[id]

		matches := slices.ContainsFunc(inputs, func(input string) bool {
			return filepath.Base(input) == filepath.Base(dependency.File.Path)
		})
		if !found || !matches {
			continue
		}

		if err := nextflowExecutable(filepath.Join(cache, dependency.File.Path)); err != nil {
			return err
		}
	}

	return nil
}

func nextflowReadInterpreters(source string) ([]string, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	prefix, err := io.ReadAll(io.LimitReader(file, nextflowShebangLimit))
	if err != nil {
		return nil, err
	}

	if bytes.HasPrefix(prefix, []byte("#!")) {
		return nextflowInterpreterNames(prefix, source)
	}

	if !bytes.HasPrefix(prefix, []byte(elf.ELFMAG)) {
		return []string{}, nil
	}

	native, err := elf.NewFile(file)
	if err != nil {
		return nil, err
	}

	return nextflowELFInterpreter(native)
}

func nextflowInterpreterNames(prefix []byte, source string) ([]string, error) {
	line, _, _ := strings.Cut(string(prefix), "\n")

	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 || !filepath.IsAbs(fields[0]) {
		return nil, failure(nextflowCompanionMissingCode, source, "absolute script interpreter required")
	}

	if filepath.Base(fields[0]) != "env" {
		return fields[:1], nil
	}

	if len(fields) != 2 || strings.HasPrefix(fields[1], "-") {
		return nil, failure(nextflowCompanionMissingCode, source, "unsupported env interpreter arguments")
	}

	return fields, nil
}

func nextflowELFCompanions(ctx context.Context, source string) ([]string, error) {
	file, err := elf.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	libraries, err := file.ImportedLibraries()
	if err != nil {
		return nil, err
	}

	if len(libraries) == 0 {
		return nextflowELFInterpreter(file)
	}

	return nextflowLdd(ctx, source)
}

func nextflowELFInterpreter(file *elf.File) ([]string, error) {
	for _, program := range file.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}

		data, err := io.ReadAll(io.LimitReader(program.Open(), nextflowInterpreterLimit))
		if err != nil {
			return nil, err
		}

		interpreter := strings.TrimRight(string(data), "\x00")
		if !filepath.IsAbs(interpreter) {
			return nil, failure(nextflowCompanionMissingCode, interpreter, "absolute ELF interpreter required")
		}

		return []string{interpreter}, nil
	}

	return []string{}, nil
}

func nextflowLdd(ctx context.Context, source string) ([]string, error) {
	probe, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()

	command := exec.CommandContext(probe, "ldd", source)
	command.Env = []string{"LANG=C", "LC_ALL=C", "PATH=" + os.Getenv("PATH")}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second

	output, err := command.CombinedOutput()
	if err != nil {
		return nil, failure(nextflowCompanionMissingCode, source, "ldd failed: "+string(output))
	}

	return nextflowLddPaths(source, string(output))
}

func nextflowLddPaths(source, output string) ([]string, error) {
	inputs := []string{}

	for line := range strings.SplitSeq(output, "\n") {
		candidate, err := nextflowLddPath(source, line)
		if err != nil {
			return nil, err
		}

		if candidate == "" {
			continue
		}

		inputs = append(inputs, filepath.Clean(candidate))
	}

	if len(inputs) == 0 {
		return nil, failure(nextflowCompanionMissingCode, source, "empty ELF dependency inventory")
	}

	return inputs, nil
}

func nextflowLddPath(source, line string) (string, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "linux-vdso.") {
		return "", nil
	}

	candidate := fields[0]
	if len(fields) >= 3 && fields[1] == "=>" {
		candidate = fields[2]
	}

	if !filepath.IsAbs(candidate) {
		return "", failure(nextflowCompanionMissingCode, source, "unresolved ELF input: "+line)
	}

	return candidate, nil
}
