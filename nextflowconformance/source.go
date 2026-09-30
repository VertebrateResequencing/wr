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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // The pinned Git repository uses SHA-1 object IDs.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	nextflowRuntimeHashCode = "E_RUNTIME_HASH"
	sourceHashCode          = "E_SOURCE_HASH"
	pinnedVersion           = "26.04.6"
	pinnedTag               = "38ce286fe70b44a5907cf1f5b0b8fb13bd836721"
	pinnedCommit            = "232b60569865e9a4577e48c1955409238359d6ca"
	pinnedTree              = "1b9ece615807c1e8feef1d38ccac124eb2eab073"
	launcherHash            = "61a755edbed743cfbb568f3a6c67af68481a2f6a4d6dffcc4295e51318968281"
	distributionHash        = "182a63c74074e2dc7956ffa3c8cd59de952ed2c44394e21faf5e1736b945444c"
	acquisitionDeadline     = 15 * time.Minute
	requestDeadline         = 60 * time.Second
	connectionDeadline      = 10 * time.Second
	privateDirectory        = os.FileMode(0700)
	privateFile             = os.FileMode(0600)
	sourceRegular           = os.FileMode(0644)
	sourceExecutable        = os.FileMode(0755)
	gitTreeKind             = "tree"
	gitCommitKind           = "commit"
	objectLimit             = int64(256 << 20)
	sourceLimit             = int64(512 << 20)
	entryLimit              = 50000
	selectedLimit           = int64(8 << 20)
)

type acquisitionOptions struct {
	client    *http.Client
	baseURL   string
	maxObject int64
}

func acquire(ctx context.Context, root, cache, candidate, javaHome string,
	options acquisitionOptions) (*lockRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, acquisitionDeadline)
	defer cancel()

	if err := os.MkdirAll(cache, privateDirectory); err != nil {
		return nil, err
	}

	stage, err := os.MkdirTemp(cache, "nextflow-generation-")
	if err != nil {
		return nil, err
	}

	lock, err := acquireGeneration(ctx, root, cache, stage, candidate, javaHome, options)
	if err == nil {
		err = publishGeneration(ctx, cache, candidate, lock)
	}

	if err != nil {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}

		return nil, err
	}

	return lock, nil
}

func publishGeneration(ctx context.Context, cache, candidate string, lock *lockRecord) error {
	if err := protectCandidateInputs(cache, candidate, lock); err != nil {
		return err
	}

	if err := offlinePreflight(cache, lock); err != nil {
		return err
	}

	data, err := json.Marshal(lock)
	if err != nil {
		return err
	}

	if _, err = decodeRecord(kindLock, append(data, '\n')); err != nil {
		return err
	}

	if err = checkCancellation(ctx); err != nil {
		return err
	}

	return writeJSONAtomic(candidate, lock)
}

func protectCandidateInputs(cache, candidate string, lock *lockRecord) error {
	inputs := []string{lock.Environment.JavaHome}
	for _, item := range lock.Artefacts {
		inputs = append(inputs, item.File.Path)
		if item.Packaging == packExtracted {
			inputs = append(inputs, item.File.Path+"-tree")
		}
	}

	for _, input := range inputs {
		name := filepath.Join(cache, filepath.FromSlash(input))
		if isWithin(name, candidate) || isWithin(candidate, name) {
			return failure("E_SOURCE_PATH", candidate, "candidate would alter acquired inputs")
		}
	}

	return nil
}

func failure(code, location, message string) error {
	return &sourceError{Code: code, Path: location, Err: fmt.Errorf("%w: %s", errRecord, message)}
}

func offlinePreflight(cache string, lock *lockRecord) error {
	if lock.Revision != pinnedCommit {
		return failure("E_TARGET_IDENTITY", "revision", "release commit differs")
	}

	if err := verifyPinnedTree(lock.Tree); err != nil {
		return err
	}

	if err := verifyTreeAccounting(lock); err != nil {
		return err
	}

	if err := verifyArtefacts(cache, lock.Artefacts); err != nil {
		return err
	}

	if err := verifySources(cache, lock); err != nil {
		return err
	}

	return verifyJava(cache, lock.Environment)
}

func verifyPinnedTree(tree treeRecord) error {
	if tree.SHA != pinnedTree {
		return failure("E_TARGET_IDENTITY", gitTreeKind, "source tree differs from pinned commit")
	}

	return verifyTree(tree)
}

func verifyTree(tree treeRecord) error {
	if tree.Truncated || len(tree.Entries) == 0 || len(tree.Entries) > entryLimit {
		return failure("E_TREE_INCOMPLETE", gitTreeKind, "truncated, oversized or empty tree")
	}

	directories, hashes, err := indexTree(tree)
	if err != nil {
		return err
	}

	for directory := range hashes {
		if err := verifyDirectory(directory, directories[directory], hashes); err != nil {
			return err
		}
	}

	return nil
}

func indexTree(tree treeRecord) (map[string][]treeEntry, map[string]string, error) {
	directories := map[string][]treeEntry{}
	hashes := map[string]string{".": tree.SHA}

	seen := map[string]bool{}
	for _, entry := range tree.Entries {
		if !safeRelative(entry.Path) || seen[entry.Path] {
			return nil, nil, failure("E_SOURCE_PATH", entry.Path, "unsafe or duplicate tree path")
		}

		seen[entry.Path] = true
		parent := path.Dir(entry.Path)

		directories[parent] = append(directories[parent], entry)
		if entry.Type == gitTreeKind {
			hashes[entry.Path] = entry.SHA
		}
	}

	for parent := range directories {
		if hashes[parent] == "" {
			return nil, nil, failure("E_TREE_INCOMPLETE", parent, "missing parent directory")
		}
	}

	return directories, hashes, nil
}

func verifyDirectory(directory string, entries []treeEntry, hashes map[string]string) error {
	slices.SortFunc(entries, func(a, b treeEntry) int { return strings.Compare(gitSortName(a), gitSortName(b)) })

	var content bytes.Buffer

	for _, entry := range entries {
		sha, err := hex.DecodeString(entry.SHA)
		if err != nil || len(sha) != sha1.Size {
			return failure("E_TREE_INCOMPLETE", entry.Path, "invalid Git ID")
		}

		mode := strings.TrimLeft(entry.Mode, "0")
		content.WriteString(mode + " " + path.Base(entry.Path))
		content.WriteByte(0)
		content.Write(sha)
	}

	if gitHash(gitTreeKind, content.Bytes()) != hashes[directory] {
		return failure("E_TREE_INCOMPLETE", directory, "Git tree object hash differs")
	}

	return nil
}

func gitSortName(entry treeEntry) string {
	name := path.Base(entry.Path)
	if entry.Type == gitTreeKind {
		name += "/"
	}

	return name
}

func gitHash(kind string, data []byte) string {
	sum := sha1.New() //nolint:gosec // Git object identity, with SHA-256 checked independently.
	sum.Write([]byte(kind + " " + strconv.Itoa(len(data)) + "\x00"))
	sum.Write(data)

	return hex.EncodeToString(sum.Sum(nil))
}

func verifyTreeAccounting(lock *lockRecord) error {
	entries := treeBlobs(lock.Tree)
	if len(entries) != len(lock.Files) {
		return failure("E_TREE_INCOMPLETE", "files", "file inventory does not cover immutable tree")
	}

	for _, file := range lock.Files {
		entry, exists := entries[file.Path]
		if !exists || entry.SHA != file.Blob || entry.Mode != file.Mode {
			return failure("E_TREE_INCOMPLETE", file.Path, "file identity differs from tree")
		}

		delete(entries, file.Path)
	}

	return nil
}

func treeBlobs(tree treeRecord) map[string]treeEntry {
	entries := map[string]treeEntry{}

	for _, entry := range tree.Entries {
		if entry.Type == "blob" {
			entries[entry.Path] = entry
		}
	}

	return entries
}

func verifyArtefacts(cache string, items []artefact) error {
	for _, item := range items {
		if err := nextflowVerifyArtifact(cache, item); err != nil {
			return err
		}
	}

	return nextflowVerifyInterpreters(cache, items)
}

func nextflowVerifyArtifact(cache string, item artefact) error {
	code, missing := sourceHashCode, "E_SOURCE_MISSING"
	if item.Role != roleSource {
		code, missing = nextflowRuntimeHashCode, "E_RUNTIME_MISSING"
	}

	if err := verifyFile(cache, item.File, code, missing); err != nil {
		return err
	}

	tool := item.Role == roleEnvironment && strings.HasPrefix(item.ID, "NEXTFLOW_TOOL_")
	if item.Role == roleRuntime || item.Role == roleLauncher || tool {
		return nextflowExecutable(filepath.Join(cache, item.File.Path))
	}

	return nil
}

func verifyFile(root string, file fileRef, code, missing string) error {
	name, err := safeLocal(root, file.Path)
	if err != nil {
		return err
	}

	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return failure(missing, file.Path, "file missing")
	}

	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return failure("E_SOURCE_PATH", file.Path, "regular file required")
	}

	if info.Size() != file.Bytes {
		return failure(code, file.Path, "byte count differs")
	}

	return verifyFileHash(name, file.SHA256, code)
}

func safeLocal(root, relative string) (string, error) {
	if !safeRelative(relative) {
		return "", failure("E_SOURCE_PATH", relative, "path escapes root")
	}

	current := root
	for component := range strings.SplitSeq(relative, "/") {
		current = filepath.Join(current, component)

		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return "", err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return "", failure("E_SOURCE_PATH", relative, "symlink in local path")
		}
	}

	return current, nil
}

func verifyFileHash(name, expected, code string) error {
	input, err := os.Open(name)
	if err != nil {
		return err
	}
	defer input.Close()

	sum := sha256.New()
	if _, err = io.Copy(sum, input); err != nil {
		return err
	}

	if hex.EncodeToString(sum.Sum(nil)) != expected {
		return failure(code, name, "content differs")
	}

	return nil
}

func nextflowExecutable(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return failure("E_RUNTIME_MISSING", name, "executable missing")
	}

	if !info.Mode().IsRegular() {
		return failure("E_SOURCE_PATH", name, "regular executable required")
	}

	if info.Mode().Perm()&0111 == 0 {
		return failure(nextflowRuntimeHashCode, name, "executable permission missing")
	}

	return nil
}

func verifySources(cache string, lock *lockRecord) error {
	root, err := sourceArchiveRoot(cache, lock.Artefacts)
	if err != nil {
		return err
	}

	for _, file := range lock.Files {
		if err := verifySourceFile(root, file); err != nil {
			return err
		}
	}

	return checkSourceInventory(root, lock.Files)
}

func sourceArchiveRoot(cache string, items []artefact) (string, error) {
	for _, item := range items {
		if item.Role == roleSource && item.Packaging == packExtracted {
			return safeLocal(cache, item.File.Path+"-tree")
		}
	}

	return "", failure("E_TREE_INCOMPLETE", roleSource, "source tree archive required")
}

func verifySourceFile(root string, file sourceFile) error {
	data, err := sourceFileBytes(root, file.Path, file.Mode)
	if errors.Is(err, os.ErrNotExist) {
		return failure("E_SOURCE_MISSING", file.Path, "source file missing")
	}

	if err != nil {
		return err
	}

	if hashBytes(data) != file.SHA256 || int64(len(data)) != file.Bytes || gitHash("blob", data) != file.Blob {
		return failure(sourceHashCode, file.Path, "source content differs")
	}

	return verifySelectedSize(file)
}

func sourceFileBytes(root, relative, mode string) ([]byte, error) {
	if err := rejectSymlinkAncestors(filepath.Join(root, filepath.FromSlash(path.Dir(relative)))); err != nil {
		return nil, err
	}

	name := filepath.Join(root, filepath.FromSlash(relative))
	if mode == "120000" {
		return sourceSymlinkBytes(root, relative)
	}

	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}

	expected := sourceRegular
	if mode == "100755" {
		expected = sourceExecutable
	}

	if info.Mode().Perm() != expected {
		return nil, failure(sourceHashCode, relative, "source mode differs")
	}

	return sourceBytes(name, mode)
}

func sourceSymlinkBytes(root, relative string) ([]byte, error) {
	target, err := os.Readlink(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}

	if err := safeSymlink(relative, target); err != nil {
		return nil, err
	}

	return []byte(target), nil
}

func safeSymlink(name, target string) error {
	resolved := path.Clean(path.Join(path.Dir(name), target))

	contained := resolved == "." || safeRelative(resolved)
	if !contained || target == "" || path.IsAbs(target) || strings.ContainsAny(target, "\\\x00") {
		return failure("E_SOURCE_PATH", name, "unsafe symlink")
	}

	return nil
}

func sourceBytes(name, mode string) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}

	if mode == "120000" {
		if info.Mode()&os.ModeSymlink == 0 {
			return nil, failure("E_SOURCE_PATH", name, "expected symlink")
		}

		target, err := os.Readlink(name)

		return []byte(target), err
	}

	if !info.Mode().IsRegular() {
		return nil, failure("E_SOURCE_PATH", name, "expected regular file")
	}

	return os.ReadFile(name)
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func verifySelectedSize(file sourceFile) error {
	if file.State == sourceSelected && file.Bytes > selectedLimit {
		return failure("E_SOURCE_LIMIT", file.Path, "selected text limit")
	}

	return nil
}

func checkSourceInventory(root string, files []sourceFile) error {
	expected := map[string]bool{}
	for _, file := range files {
		expected[file.Path] = true
	}

	return filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}

		if !expected[filepath.ToSlash(relative)] {
			return failure("E_TREE_INCOMPLETE", relative, "archive file absent from immutable tree")
		}

		return nil
	})
}

func verifyJava(cache string, env environment) error {
	root, err := safeLocal(cache, env.JavaHome)
	if err != nil {
		return err
	}

	for _, item := range env.JavaFiles {
		if err = nextflowVerifyJavaFile(root, item); err != nil {
			return err
		}
	}

	return verifyJavaInventory(root, env.JavaFiles)
}

func nextflowVerifyJavaFile(root string, item javaFile) error {
	name := filepath.Join(root, filepath.FromSlash(item.File.Path))
	if err := rejectSymlinkAncestors(filepath.Dir(name)); err != nil {
		return err
	}

	data, err := sourceBytes(name, javaMode(item))
	if errors.Is(err, os.ErrNotExist) {
		return failure("E_RUNTIME_MISSING", item.File.Path, "Java file missing")
	}

	if err != nil {
		return err
	}

	if hashBytes(data) != item.File.SHA256 || int64(len(data)) != item.File.Bytes {
		return failure(nextflowRuntimeHashCode, item.File.Path, "Java content differs")
	}

	return nextflowVerifyJavaMode(root, item)
}

func javaMode(file javaFile) string {
	if file.Link != nil {
		return "120000"
	}

	return "100644"
}

func nextflowVerifyJavaMode(root string, item javaFile) error {
	name := filepath.Join(root, filepath.FromSlash(item.File.Path))

	info, err := os.Lstat(name)
	if err != nil {
		return err
	}

	if uint32(info.Mode().Perm()) != item.Mode {
		return failure(nextflowRuntimeHashCode, item.File.Path, "Java mode differs")
	}

	if item.Link != nil {
		return nextflowJavaLink(root, item.File.Path, *item.Link)
	}

	return nil
}

func nextflowJavaLink(root, relative, target string) error {
	if err := safeSymlink(filepath.ToSlash(relative), target); err != nil {
		return err
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return err
	}

	if !isWithin(root, resolved) {
		return failure("E_SOURCE_PATH", relative, "Java symlink escapes home")
	}

	return nil
}

func verifyJavaInventory(root string, files []javaFile) error {
	expected := map[string]bool{}
	for _, file := range files {
		expected[file.File.Path] = true
	}

	return filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}

		if !expected[filepath.ToSlash(relative)] {
			return failure(nextflowRuntimeHashCode, relative, "unexpected file in Java tree")
		}

		return nil
	})
}

func writeJSONAtomic(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	if err = os.MkdirAll(filepath.Dir(name), privateDirectory); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(name), ".candidate-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())

	_, writeErr := temporary.Write(data)
	closeErr := temporary.Close()

	if writeErr != nil {
		return writeErr
	}

	if closeErr != nil {
		return closeErr
	}

	return os.Rename(temporary.Name(), name)
}

func acquireGeneration(ctx context.Context, root, cache, stage, candidate, javaHome string,
	options acquisitionOptions) (*lockRecord, error) {
	old, err := readLock(root)
	if errors.Is(err, os.ErrNotExist) {
		return acquirePinned(ctx, cache, stage, javaHome, options)
	}

	if err != nil {
		return nil, err
	}

	if err := protectCandidateInputs(cache, candidate, old); err != nil {
		return nil, err
	}

	return acquireLocked(ctx, cache, stage, old, options)
}

func readLock(root string) (*lockRecord, error) {
	data, err := os.ReadFile(filepath.Join(root, "sources.lock.json"))
	if err != nil {
		return nil, err
	}

	value, err := decodeRecord("sources.lock", data)
	if err != nil {
		return nil, err
	}

	lock, ok := value.(*lockRecord)
	if !ok {
		return nil, errRecord
	}

	return lock, nil
}

func acquirePinned(ctx context.Context, cache, stage, javaHome string,
	options acquisitionOptions) (*lockRecord, error) {
	tree, err := acquireIdentities(ctx, options)
	if err != nil {
		return nil, err
	}

	lock := &lockRecord{Schema: 1, ID: "NEXTFLOW_26_04_6",
		Origin: "https://github.com/nextflow-io/nextflow", Revision: pinnedCommit,
		Tree: *tree, Files: []sourceFile{}, Artefacts: []artefact{}}
	if err = acquireSource(ctx, cache, stage, lock, options); err != nil {
		return nil, err
	}

	if err = acquireRuntime(ctx, cache, stage, lock, options); err != nil {
		return nil, err
	}

	if err = nextflowAcquireEnvironment(ctx, cache, stage, javaHome, lock, options); err != nil {
		return nil, err
	}

	slices.SortFunc(lock.Artefacts, func(a, b artefact) int { return strings.Compare(a.ID, b.ID) })

	return lock, nil
}

func acquireIdentities(ctx context.Context, options acquisitionOptions) (*treeRecord, error) {
	if err := acquireReleaseIdentity(ctx, options); err != nil {
		return nil, err
	}

	if err := acquireTagIdentity(ctx, options); err != nil {
		return nil, err
	}

	if err := acquireCommitIdentity(ctx, options); err != nil {
		return nil, err
	}

	return acquireTreeIdentity(ctx, options)
}

func acquireReleaseIdentity(ctx context.Context, options acquisitionOptions) error {
	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := fetchJSON(ctx, options, sourceURL(options, "/releases/tags/v"+pinnedVersion), &release); err != nil {
		return err
	}

	if release.TagName != "v"+pinnedVersion {
		return failure("E_TARGET_IDENTITY", "release", "wrong version")
	}

	return nil
}

func acquireTagIdentity(ctx context.Context, options acquisitionOptions) error {
	var tag struct {
		SHA    string `json:"sha"`
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := fetchJSON(ctx, options, sourceURL(options, "/git/tags/"+pinnedTag), &tag); err != nil {
		return err
	}

	if tag.SHA != pinnedTag || tag.Object.SHA != pinnedCommit || tag.Object.Type != gitCommitKind {
		return failure("E_TARGET_IDENTITY", "tag", "wrong tag or commit")
	}

	return nil
}

func acquireCommitIdentity(ctx context.Context, options acquisitionOptions) error {
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := fetchJSON(ctx, options, sourceURL(options, "/git/commits/"+pinnedCommit), &commit); err != nil {
		return err
	}

	if commit.SHA != pinnedCommit || commit.Tree.SHA != pinnedTree {
		return failure("E_TARGET_IDENTITY", gitCommitKind, "wrong commit or tree")
	}

	return nil
}

func acquireTreeIdentity(ctx context.Context, options acquisitionOptions) (*treeRecord, error) {
	var tree struct {
		SHA       string      `json:"sha"`
		Truncated bool        `json:"truncated"`
		Tree      []treeEntry `json:"tree"`
	}

	origin := sourceURL(options, "/git/trees/"+pinnedTree+"?recursive=1")
	if err := fetchJSON(ctx, options, origin, &tree); err != nil {
		return nil, err
	}

	result := &treeRecord{SHA: tree.SHA, Origin: origin, Truncated: tree.Truncated, Entries: tree.Tree}
	if err := verifyPinnedTree(*result); err != nil {
		return nil, err
	}

	return result, nil
}

func acquireSource(ctx context.Context, cache, stage string, lock *lockRecord, options acquisitionOptions) error {
	origin := "https://codeload.github.com/nextflow-io/nextflow/tar.gz/" + pinnedCommit

	data, err := fetch(ctx, options, origin)
	if err != nil {
		return err
	}

	item, err := saveArtifact(cache, stage, "SOURCE", roleSource, origin, "source.tar.gz", data)
	if err != nil {
		return err
	}

	lock.Artefacts = append(lock.Artefacts, item)

	sourceRoot := filepath.Join(cache, item.File.Path+"-tree")
	if err = unpackSources(data, sourceRoot); err != nil {
		return err
	}

	lock.Files, err = measureSources(sourceRoot, lock.Tree)

	return err
}

func saveArtifact(cache, stage, id, role, origin, name string, data []byte) (artefact, error) {
	absolute := filepath.Join(stage, name)
	if err := writeFile(absolute, data, privateFile); err != nil {
		return artefact{}, err
	}

	relative, err := filepath.Rel(cache, absolute)
	if err != nil {
		return artefact{}, err
	}

	return artefact{ID: id, Role: role, Origin: origin,
		File:      fileRef{Path: filepath.ToSlash(relative), SHA256: hashBytes(data), Bytes: int64(len(data))},
		Packaging: nextflowPackaging(role), Dependencies: []string{}}, nil
}

func writeFile(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), privateDirectory); err != nil {
		return err
	}

	if err := os.WriteFile(name, data, mode); err != nil { //nolint:gosec // Callers validate paths before writing.
		return err
	}

	return os.Chmod(name, mode) // Preserve requested permissions independently of the caller's umask.
}

func nextflowPackaging(role string) string {
	switch role {
	case roleSource:
		return packExtracted
	case roleRuntime:
		return packOpaque
	default:
		return packFile
	}
}

func unpackSources(data []byte, destination string) error {
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer compressed.Close()

	limited := &io.LimitedReader{R: compressed, N: sourceLimit + 1}

	return unpackSourceStream(limited, destination)
}

func unpackSourceStream(limited *io.LimitedReader, destination string) error {
	reader := tar.NewReader(limited)
	state := archiveState{seen: map[string]bool{}}

	for {
		header, err := reader.Next()

		if limited.N == 0 {
			return failure("E_SOURCE_LIMIT", destination, "unpacked size limit")
		}

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if err = unpackNextMember(reader, destination, &state, header); err != nil {
			return err
		}
	}
}

func unpackNextMember(reader io.Reader, destination string, state *archiveState, header *tar.Header) error {
	name, err := state.member(header)
	if err != nil {
		return err
	}

	if header.Typeflag == tar.TypeSymlink {
		if err := safeSymlink(name, header.Linkname); err != nil {
			return err
		}
	}

	if name == "" {
		return nil
	}

	return unpackMember(reader, destination, name, header)
}

func unpackMember(reader io.Reader, root, name string, header *tar.Header) error {
	destination, err := safeLocal(root, name)
	if err != nil {
		return err
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(destination, privateDirectory)
	case tar.TypeReg:
		return unpackRegular(reader, destination, header)
	case tar.TypeSymlink:
		if err = os.MkdirAll(filepath.Dir(destination), privateDirectory); err != nil {
			return err
		}

		return os.Symlink(header.Linkname, destination)
	default:
		return failure("E_SOURCE_PATH", name, "unsupported archive member type")
	}
}

func unpackRegular(reader io.Reader, destination string, header *tar.Header) error {
	data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
	if err != nil {
		return err
	}

	mode := sourceRegular
	if header.Mode&0111 != 0 {
		mode = sourceExecutable
	}

	return writeFile(destination, data, mode)
}

func measureSources(root string, tree treeRecord) ([]sourceFile, error) {
	files := make([]sourceFile, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		if entry.Type == gitTreeKind {
			continue
		}

		file, err := measureSource(root, entry)
		if err != nil {
			return nil, err
		}

		files = append(files, file)
	}

	if err := checkSourceInventory(root, files); err != nil {
		return nil, err
	}

	slices.SortFunc(files, func(a, b sourceFile) int { return strings.Compare(a.Path, b.Path) })

	return files, nil
}

func measureSource(root string, entry treeEntry) (sourceFile, error) {
	data, err := sourceFileBytes(root, entry.Path, entry.Mode)
	if err != nil {
		return sourceFile{}, err
	}

	if gitHash("blob", data) != entry.SHA {
		return sourceFile{}, failure(sourceHashCode, entry.Path, "Git blob mismatch")
	}

	state := "unreviewed"
	if slices.Contains(selectedPaths(), entry.Path) {
		state = sourceSelected
	}

	file := sourceFile{Path: entry.Path, Blob: entry.SHA, Mode: entry.Mode,
		SHA256: hashBytes(data), Bytes: int64(len(data)), State: state}
	if file.State == sourceSelected && file.Bytes > selectedLimit {
		return sourceFile{}, failure("E_SOURCE_LIMIT", entry.Path, "selected text limit")
	}

	return file, nil
}

func selectedPaths() []string {
	return []string{
		"docs/reference/syntax.md", "docs/reference/process.md", "docs/reference/operator.md",
		"docs/strict-syntax.md", "docs/migrations/26-04.md",
		"modules/nf-lang/src/main/antlr/ScriptParser.g4", "modules/nf-lang/src/main/antlr/ScriptLexer.g4",
		"modules/nf-lang/build.gradle", "modules/nextflow/build.gradle",
		"modules/nf-lang/src/test/groovy/nextflow/script/parser/ScriptAstBuilderTest.groovy",
		"modules/nextflow/src/test/groovy/nextflow/extension/MixOpTest.groovy",
		"modules/nextflow/src/test/groovy/nextflow/processor/TaskConfigTest.groovy",
	}
}

func acquireRuntime(ctx context.Context, cache, stage string, lock *lockRecord, options acquisitionOptions) error {
	objects := []struct{ id, role, name, hash string }{
		{"LAUNCHER", roleLauncher, "nextflow", launcherHash},
		{"RUNTIME", roleRuntime, "nextflow-" + pinnedVersion + "-dist", distributionHash},
	}
	for _, object := range objects {
		origin := "https://github.com/nextflow-io/nextflow/releases/download/v" + pinnedVersion + "/" + object.name

		data, err := fetch(ctx, options, origin)
		if err != nil {
			return err
		}

		if hashBytes(data) != object.hash {
			return failure(sourceHashCode, object.name, "release asset digest mismatch")
		}

		item, err := saveArtifact(cache, stage, object.id, object.role, origin, object.name, data)
		if err != nil {
			return err
		}

		if err = nextflowArtifactMode(filepath.Join(cache, item.File.Path), item.Role); err != nil {
			return err
		}

		lock.Artefacts = append(lock.Artefacts, item)
	}

	return nil
}

func nextflowArtifactMode(name, role string) error {
	if role == roleRuntime || role == roleLauncher {
		return os.Chmod(name, sourceExecutable)
	}

	return nil
}

func nextflowAcquireEnvironment(ctx context.Context, cache, stage, javaHome string,
	lock *lockRecord, options acquisitionOptions) error {
	var err error

	lock.Environment, err = acquireJava(ctx, cache, stage, javaHome)
	if err != nil {
		return err
	}

	if err = nextflowExecutionInputs(ctx, cache, stage, javaHome, lock); err != nil {
		return err
	}

	if err = acquireDependencies(ctx, cache, stage, lock, options); err != nil {
		return err
	}

	return nil
}

func acquireJava(ctx context.Context, cache, stage, home string) (environment, error) {
	absolute, err := nextflowJavaHome(home)
	if err != nil {
		return environment{}, err
	}

	destination := filepath.Join(stage, javaDirectory)

	files, err := copyJava(absolute, destination)
	if err != nil {
		return environment{}, err
	}

	version, err := nextflowJavaVersion(ctx, destination)
	if err != nil {
		return environment{}, err
	}

	relative, err := filepath.Rel(cache, destination)
	if err != nil {
		return environment{}, err
	}

	return nextflowJavaEnvironment(filepath.ToSlash(relative), version, files), nil
}

func nextflowJavaHome(home string) (string, error) {
	if home == "" {
		return "", failure("E_RUNTIME_MISSING", "java-home", "existing Java 21 home required")
	}

	absolute, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}

	if err = rejectSymlinkAncestors(absolute); err != nil {
		return "", err
	}

	return absolute, nil
}

func copyJava(root, destination string) ([]javaFile, error) {
	files := []javaFile{}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(destination, relative), privateDirectory)
		}

		item, err := copyJavaFile(root, destination, name, relative)
		if err != nil {
			return err
		}

		files = append(files, item)

		return nil
	})

	return files, err
}

func copyJavaFile(root, destination, name, relative string) (javaFile, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return javaFile{}, err
	}

	if info.Mode().IsRegular() {
		return nextflowCopyJavaRegular(destination, name, relative, info.Mode())
	}

	target, err := os.Readlink(name)
	if err != nil {
		return javaFile{}, err
	}

	if err = nextflowCopyJavaLink(root, destination, relative, target); err != nil {
		return javaFile{}, err
	}

	return javaFile{Mode: uint32(info.Mode().Perm()), Link: &target,
		File: fileRef{Path: filepath.ToSlash(relative), SHA256: hashBytes([]byte(target)), Bytes: int64(len(target))}}, nil
}

func nextflowCopyJavaRegular(destination, name, relative string, mode os.FileMode) (javaFile, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return javaFile{}, err
	}

	if err = writeFile(filepath.Join(destination, relative), data, mode.Perm()); err != nil {
		return javaFile{}, err
	}

	return javaFile{Mode: uint32(mode.Perm()),
		File: fileRef{Path: filepath.ToSlash(relative), SHA256: hashBytes(data), Bytes: int64(len(data))}}, nil
}

func nextflowCopyJavaLink(root, destination, relative, target string) error {
	if err := nextflowJavaLink(root, relative, target); err != nil {
		return err
	}

	return os.Symlink(target, filepath.Join(destination, relative))
}

func nextflowJavaVersion(ctx context.Context, home string) (string, error) {
	executable, err := safeLocal(home, "bin/java")
	if err != nil {
		return "", err
	}

	if err = nextflowExecutable(executable); err != nil {
		return "", err
	}

	command := exec.CommandContext(ctx, executable, "-version")
	command.Env = []string{"HOME=" + home, "LANG=C", "TZ=UTC"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second

	version, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}

	if !bytes.Contains(version, []byte(`version "21.`)) {
		return "", failure("E_RUNTIME_MISSING", home, "Java 21 required")
	}

	return strings.TrimSpace(string(version)), nil
}

func nextflowJavaEnvironment(relative, version string, files []javaFile) environment {
	return environment{JavaHome: filepath.ToSlash(relative), JavaVersion: version,
		OS: runtime.GOOS, Arch: runtime.GOARCH, JavaFiles: files,
		Variables: []envValue{{Key: "COLUMNS", Value: "80"}, {Key: "NXF_DISABLE_CHECK_LATEST", Value: "true"},
			{Key: "NXF_OFFLINE", Value: "true"}, {Key: "NXF_PLUGINS_DEFAULT", Value: ""},
			{Key: "NXF_SYNTAX_PARSER", Value: "v2"}, {Key: "NXF_VER", Value: pinnedVersion}, {Key: "TERM", Value: "dumb"}},
		Dependencies: []coordinate{}}
}

// nextflowExecutionInputs records physical files; Java symlinks remain in its tree inventory.
func nextflowExecutionInputs(ctx context.Context, cache, stage, home string, lock *lockRecord) error {
	observations, err := nextflowJavaArtifacts(cache, home, lock)
	if err != nil {
		return err
	}

	tools := []string{"env", "bash", "sh", "dirname", "realpath", "which",
		"sed", "awk", "mkdir", "cat", "md5sum", "cut"}
	for _, name := range tools {
		item, observation, toolErr := nextflowAcquireTool(cache, stage, name)
		if toolErr != nil {
			return toolErr
		}

		lock.Artefacts = append(lock.Artefacts, item)
		observations = append(observations, observation)
	}

	observations, err = nextflowAcquireCompanions(ctx, cache, stage, lock, observations)
	if err != nil {
		return err
	}

	nextflowExecutionEdges(lock)

	return writeJSONAtomic(filepath.Join(stage, "nextflow-local-provenance.json"), observations)
}

func nextflowJavaArtifacts(cache, home string, lock *lockRecord) ([]nextflowLocalObservation, error) {
	observations := []nextflowLocalObservation{}

	for _, file := range lock.Environment.JavaFiles {
		if file.Link != nil {
			continue
		}

		item := artefact{ID: "NEXTFLOW_JAVA_" + strings.ToUpper(hashBytes([]byte(file.File.Path))), Role: roleJava,
			Origin: localOriginPrefix + file.File.SHA256, Packaging: packFile, File: file.File, Dependencies: []string{}}
		item.File.Path = path.Join(lock.Environment.JavaHome, file.File.Path)
		lock.Artefacts = append(lock.Artefacts, item)

		observation, err := nextflowObserveLocal(filepath.Join(home, file.File.Path), cache, item)
		if err != nil {
			return nil, err
		}

		observations = append(observations, observation)
	}

	return observations, nil
}

func nextflowObserveLocal(source, cache string, item artefact) (nextflowLocalObservation, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return nextflowLocalObservation{}, err
	}

	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nextflowLocalObservation{}, err
	}

	snapshot := filepath.Join(cache, item.File.Path)

	info, err := os.Stat(snapshot)
	if err != nil {
		return nextflowLocalObservation{}, err
	}

	return nextflowLocalObservation{Source: absolute, Resolved: resolved, Snapshot: snapshot,
		SHA256: item.File.SHA256, Bytes: item.File.Bytes, Mode: uint32(info.Mode().Perm())}, nil
}

func nextflowAcquireTool(cache, stage, name string) (artefact, nextflowLocalObservation, error) {
	source, resolved, err := nextflowToolSource(name)
	if err != nil {
		return artefact{}, nextflowLocalObservation{}, err
	}

	return nextflowAcquireLocal(cache, stage, source, resolved, "NEXTFLOW_TOOL_"+strings.ToUpper(name),
		"nextflow-tools/"+name)
}

func nextflowToolSource(name string) (string, string, error) {
	source, err := exec.LookPath(name)
	if err != nil {
		return "", "", failure("E_RUNTIME_MISSING", name, "required tool missing")
	}

	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", err
	}

	if err = nextflowExecutable(resolved); err != nil {
		return "", "", err
	}

	return source, resolved, nil
}

func nextflowAcquireLocal(cache, stage, source, resolved, id, name string) (artefact, nextflowLocalObservation, error) {
	info, err := os.Lstat(resolved)
	if err != nil {
		return artefact{}, nextflowLocalObservation{}, err
	}

	if !info.Mode().IsRegular() {
		return artefact{}, nextflowLocalObservation{}, failure("E_SOURCE_PATH", source, "regular local input required")
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return artefact{}, nextflowLocalObservation{}, err
	}

	item, err := saveArtifact(cache, stage, id, roleEnvironment, localOriginPrefix+hashBytes(data), name, data)
	if err != nil {
		return artefact{}, nextflowLocalObservation{}, err
	}

	if err = os.Chmod(filepath.Join(cache, item.File.Path), info.Mode().Perm()); err != nil {
		return artefact{}, nextflowLocalObservation{}, err
	}

	observation, err := nextflowObserveLocal(source, cache, item)

	return item, observation, err
}

func nextflowExecutionEdges(lock *lockRecord) {
	dependencies := []string{}

	for _, item := range lock.Artefacts {
		if item.Role == roleJava || item.Role == roleEnvironment {
			dependencies = append(dependencies, item.ID)
		}
	}

	slices.Sort(dependencies)

	for index := range lock.Artefacts {
		if lock.Artefacts[index].Role == roleRuntime {
			lock.Artefacts[index].Dependencies = dependencies
		}
	}
}

func acquireDependencies(ctx context.Context, cache, stage string, lock *lockRecord, options acquisitionOptions) error {
	dependencies := []coordinate{
		{ID: "ANTLR", Group: "me.sunlan", Name: "antlr4", Version: "4.13.2.6"},
		{ID: "GROOVY", Group: "org.apache.groovy", Name: "groovy", Version: "4.0.31"},
		{ID: "PF4J", Group: "org.pf4j", Name: "pf4j", Version: "3.14.1"},
	}
	for _, dependency := range dependencies {
		origin := "https://repo.maven.apache.org/maven2/" + strings.ReplaceAll(dependency.Group, ".", "/") + "/" +
			dependency.Name + "/" + dependency.Version + "/" + dependency.Name + "-" + dependency.Version + ".pom"

		data, err := fetch(ctx, options, origin)
		if err != nil {
			return err
		}

		item, err := saveArtifact(cache, stage, "POM_"+dependency.ID, roleMetadata, origin,
			"poms/"+dependency.ID+".pom", data)
		if err != nil {
			return err
		}

		coordinate := dependency.Group + ":" + dependency.Name + ":" + dependency.Version
		item.Coordinate = &coordinate
		lock.Artefacts = append(lock.Artefacts, item)
		dependency.POMID = item.ID
		lock.Environment.Dependencies = append(lock.Environment.Dependencies, dependency)
	}

	return nil
}

func acquireLocked(ctx context.Context, cache, stage string, old *lockRecord,
	options acquisitionOptions) (*lockRecord, error) {
	clone := *old

	clone.Artefacts = slices.Clone(old.Artefacts)
	for i, item := range old.Artefacts {
		acquired, err := acquireLockedArtifact(ctx, cache, stage, item, options)
		if err != nil {
			return nil, err
		}

		clone.Artefacts[i] = acquired
	}
	// Reviewed locks reuse their Java snapshot, which is rehashed before publication.
	if err := verifyJava(cache, old.Environment); err != nil {
		return nil, err
	}

	return &clone, nil
}

func acquireLockedArtifact(ctx context.Context, cache, stage string, item artefact,
	options acquisitionOptions) (artefact, error) {
	if strings.HasPrefix(item.Origin, localOriginPrefix) {
		if err := verifyArtefacts(cache, []artefact{item}); err != nil {
			return artefact{}, err
		}

		return item, nil
	}

	data, err := fetch(ctx, options, item.Origin)
	if err != nil {
		return artefact{}, err
	}

	if hashBytes(data) != item.File.SHA256 || int64(len(data)) != item.File.Bytes {
		return artefact{}, failure(sourceHashCode, item.File.Path, "download differs from reviewed lock")
	}

	return saveLockedArtifact(cache, stage, item, data)
}

func saveLockedArtifact(cache, stage string, item artefact, data []byte) (artefact, error) {
	name := filepath.Join(stage, "artefacts", item.ID, path.Base(item.File.Path))
	if err := writeFile(name, data, privateFile); err != nil {
		return artefact{}, err
	}

	relative, err := filepath.Rel(cache, name)
	if err != nil {
		return artefact{}, err
	}

	item.File.Path = filepath.ToSlash(relative)
	if err = nextflowArtifactMode(name, item.Role); err != nil {
		return artefact{}, err
	}

	if item.Packaging == packExtracted {
		if err = unpackSources(data, name+"-tree"); err != nil {
			return artefact{}, err
		}
	}

	return item, nil
}

func sourceURL(options acquisitionOptions, suffix string) string {
	if options.baseURL != "" {
		return options.baseURL + suffix
	}

	return "https://api.github.com/repos/nextflow-io/nextflow" + suffix
}

func fetchJSON(ctx context.Context, options acquisitionOptions, origin string, destination any) error {
	data, err := fetch(ctx, options, origin)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(data, destination); err != nil {
		return &sourceError{Code: "E_FETCH", Path: origin, Err: err}
	}

	return nil
}

func fetch(ctx context.Context, options acquisitionOptions, origin string) ([]byte, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, failure("E_SOURCE_PATH", origin, "HTTPS URL required")
	}

	if options.client == nil {
		options.client = acquisitionClient()
	}

	limit := options.maxObject
	if limit == 0 {
		limit = objectLimit
	}

	return fetchAttempts(ctx, options.client, origin, min(limit, objectLimit))
}

func acquisitionClient() *http.Client {
	return &http.Client{Timeout: requestDeadline, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: connectionDeadline}).DialContext,
		TLSHandshakeTimeout: connectionDeadline}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 10 {
			return failure("E_FETCH", req.URL.String(), "unsafe redirect")
		}

		return nil
	}}
}

func fetchAttempts(ctx context.Context, client *http.Client, origin string, limit int64) ([]byte, error) {
	var last error

	for range 2 {
		data, err := fetchOnce(ctx, client, origin, limit)
		if err == nil {
			return data, nil
		}

		last = err

		var detail *sourceError
		if errors.As(err, &detail) && detail.Code == "E_SOURCE_LIMIT" {
			return nil, err
		}

		if ctx.Err() != nil {
			break
		}
	}

	return nil, &sourceError{Code: "E_FETCH", Path: origin, Err: last}
}

func fetchOnce(ctx context.Context, client *http.Client, origin string, limit int64) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()

	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, origin, nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", errRecord, response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, failure("E_SOURCE_LIMIT", origin, "object exceeds size limit")
	}

	return data, nil
}

type archiveState struct {
	seen  map[string]bool
	count int
	bytes int64
}

func (state *archiveState) member(header *tar.Header) (string, error) {
	state.count++

	state.bytes += header.Size
	if state.count > entryLimit || header.Size < 0 || state.bytes > sourceLimit {
		return "", failure("E_SOURCE_LIMIT", header.Name, "archive entry or unpacked size limit")
	}
	// Global PAX metadata describes the archive and has no filesystem member.
	if header.Typeflag == tar.TypeXGlobalHeader {
		return "", nil
	}

	name, err := archivePath(header.Name)
	if err != nil {
		return "", err
	}

	if state.seen[name] {
		return "", failure("E_SOURCE_PATH", header.Name, "duplicate archive destination")
	}

	state.seen[name] = true

	return name, nil
}

func archivePath(name string) (string, error) {
	trimmed := strings.TrimSuffix(name, "/")
	if !safeRelative(trimmed) {
		return "", failure("E_SOURCE_PATH", name, "unsafe archive path")
	}

	_, relative, found := strings.Cut(trimmed, "/")
	if !found {
		return "", nil
	}

	return relative, nil
}

type sourceError struct {
	Code string
	Path string
	Err  error
}

func (e *sourceError) Error() string { return e.Code + ": " + e.Path + ": " + e.Err.Error() }

func (e *sourceError) Unwrap() error { return e.Err }

type nextflowLocalObservation struct {
	Source   string `json:"source"`
	Resolved string `json:"resolved"`
	Snapshot string `json:"snapshot"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Mode     uint32 `json:"mode"`
}
