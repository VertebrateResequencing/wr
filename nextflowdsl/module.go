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
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultGitHubModuleRevision = "HEAD"

var githubResolverRunGit = func(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}

		return fmt.Errorf("%w: %s", err, message)
	}

	return nil
}

// SetGitHubResolverRunGitForTesting swaps the low-level git runner used by the
// GitHub resolver and returns a restore function for tests.
func SetGitHubResolverRunGitForTesting(runGit func(dir string, args ...string) error) func() {
	previous := githubResolverRunGit
	githubResolverRunGit = runGit

	return func() {
		githubResolverRunGit = previous
	}
}

// ModuleResolver fetches module source and returns a local path.
type ModuleResolver interface {
	Resolve(spec string) (localPath string, err error)
}

// NewGitHubResolver returns a resolver for owner/repo GitHub module specs.
func NewGitHubResolver(cacheDir string) ModuleResolver {
	if cacheDir == "" {
		cacheDir = defaultGitHubModuleCacheDir()
	}

	return githubResolver{cacheDir: cacheDir}
}

// NewLocalResolver returns a resolver for relative and absolute module paths.
func NewLocalResolver(basePath string) ModuleResolver {
	return localResolver{basePath: basePath}
}

// NewChainResolver returns a resolver that tries each resolver in order.
func NewChainResolver(resolvers ...ModuleResolver) ModuleResolver {
	return chainResolver{resolvers: resolvers}
}

type localResolver struct {
	basePath string
}

func (r localResolver) Resolve(spec string) (string, error) {
	var resolvedPath string

	switch {
	case filepath.IsAbs(spec):
		resolvedPath = filepath.Clean(spec)
	case spec == "." || spec == ".." || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../"):
		resolvedPath = filepath.Join(r.basePath, filepath.FromSlash(spec))
	default:
		return "", fmt.Errorf("unsupported local module spec %q", spec)
	}

	if _, err := os.Stat(resolvedPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("module path %q does not exist", resolvedPath)
		}

		return "", fmt.Errorf("stat module path %q: %w", resolvedPath, err)
	}

	return resolvedPath, nil
}

type chainResolver struct {
	resolvers []ModuleResolver
}

func (r chainResolver) Resolve(spec string) (string, error) {
	var errs []error

	for _, resolver := range r.resolvers {
		if resolver == nil {
			continue
		}

		localPath, err := resolver.Resolve(spec)
		if err == nil {
			return localPath, nil
		}

		errs = append(errs, err)
	}

	if len(errs) == 0 {
		return "", fmt.Errorf("could not resolve module spec %q: no resolvers configured", spec)
	}

	return "", fmt.Errorf("could not resolve module spec %q: %w", spec, errors.Join(errs...))
}

type githubResolver struct {
	cacheDir string
}

func (r githubResolver) Resolve(spec string) (string, error) {
	owner, repo, revision, explicitRevision, err := parseGitHubModuleSpec(spec)
	if err != nil {
		return "", err
	}

	cachePath := filepath.Join(r.cacheDir, owner, repo, revision)
	if ok, hasFilesErr := hasNextflowModuleFiles(cachePath); hasFilesErr != nil {
		return "", hasFilesErr
	} else if ok && explicitRevision {
		return cachePath, nil
	} else if ok {
		if pullErr := githubResolverRunGit(cachePath, "pull", "--ff-only"); pullErr == nil {
			return cachePath, nil
		}
	}

	if removeErr := os.RemoveAll(cachePath); removeErr != nil {
		return "", fmt.Errorf("clear module cache %q: %w", cachePath, removeErr)
	}

	if mkdirErr := os.MkdirAll(cachePath, 0o755); mkdirErr != nil {
		return "", fmt.Errorf("create module cache %q: %w", cachePath, mkdirErr)
	}

	if mkdirErr := os.MkdirAll(r.cacheDir, 0o755); mkdirErr != nil {
		return "", fmt.Errorf("create module cache root %q: %w", r.cacheDir, mkdirErr)
	}

	args := []string{"clone", "--depth", "1", githubModuleURL(owner, repo), cachePath}
	if explicitRevision {
		args = []string{"clone", githubModuleURL(owner, repo), cachePath}
	}

	if fetchErr := githubResolverRunGit(r.cacheDir, args...); fetchErr != nil {
		return "", fmt.Errorf("fetch failure for %q: %w", spec, fetchErr)
	}

	if explicitRevision {
		if checkoutErr := githubResolverRunGit(cachePath, "checkout", revision); checkoutErr != nil {
			return "", fmt.Errorf("fetch failure for %q: %w", spec, checkoutErr)
		}
	}

	hasFiles, err := hasNextflowModuleFiles(cachePath)
	if err != nil {
		return "", err
	}

	if !hasFiles {
		return "", fmt.Errorf("fetch failure for %q: cloned module did not contain any .nf files", spec)
	}

	return cachePath, nil
}

func parseGitHubModuleSpec(spec string) (owner, repo, revision string, explicitRevision bool, err error) {
	repoSpec, revisionSpec, hasRevision := strings.Cut(spec, "@")

	repoSpec, err = normalizeGitHubRepoSpec(repoSpec)
	if err != nil {
		return "", "", "", false, err
	}

	parts := strings.Split(repoSpec, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false, fmt.Errorf("unsupported GitHub module spec %q", spec)
	}

	if !isValidGitHubRepoPathPart(parts[0]) || !isValidGitHubRepoPathPart(parts[1]) {
		return "", "", "", false, fmt.Errorf("unsupported GitHub module spec %q", spec)
	}

	if hasRevision {
		if revisionSpec == "" {
			return "", "", "", false, fmt.Errorf("unsupported GitHub module spec %q", spec)
		}

		if strings.ContainsRune(revisionSpec, filepath.Separator) || strings.Contains(revisionSpec, "/") {
			return "", "", "", false, fmt.Errorf("unsupported GitHub revision %q", revisionSpec)
		}

		return parts[0], parts[1], revisionSpec, true, nil
	}

	return parts[0], parts[1], defaultGitHubModuleRevision, false, nil
}

func isValidGitHubRepoPathPart(part string) bool {
	return part != "." && part != ".."
}

func hasNextflowModuleFiles(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("stat module cache %q: %w", path, err)
	}

	if !info.IsDir() {
		return false, fmt.Errorf("module cache %q is not a directory", path)
	}

	var found bool

	walkErr := filepath.WalkDir(path, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		if filepath.Ext(currentPath) == ".nf" {
			found = true

			return filepath.SkipAll
		}

		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return false, fmt.Errorf("scan module cache %q: %w", path, walkErr)
	}

	return found, nil
}

func githubModuleURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}

func normalizeGitHubRepoSpec(spec string) (string, error) {
	if !strings.HasPrefix(spec, "https://") && !strings.HasPrefix(spec, "http://") {
		return spec, nil
	}

	parsed, err := url.Parse(spec)
	if err != nil {
		return "", fmt.Errorf("unsupported GitHub module spec %q", spec)
	}

	if !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {

		return "", fmt.Errorf("unsupported GitHub module spec %q", spec)
	}

	trimmedPath := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")

	parts := strings.Split(trimmedPath, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("unsupported GitHub module spec %q", spec)
	}

	return parts[0] + "/" + parts[1], nil
}

func defaultGitHubModuleCacheDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".wr", "nextflow_modules")
	}

	return filepath.Join(homeDir, ".wr", "nextflow_modules")
}
