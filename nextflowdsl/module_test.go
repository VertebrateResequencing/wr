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
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

type ModuleResolverFunc func(spec string) (string, error)

func (f ModuleResolverFunc) Resolve(spec string) (string, error) {
	return f(spec)
}

func TestGitHubResolver(t *testing.T) {
	Convey("NewGitHubResolver handles C2 remote module resolution", t, func() {
		cacheDir := t.TempDir()

		originalRunner := githubResolverRunGit
		Reset(func() {
			githubResolverRunGit = originalRunner
		})

		Convey("empty cache triggers a clone and returns a cached module path containing .nf files", func() {
			cloneCalls := 0
			githubResolverRunGit = func(dir string, args ...string) error {
				cloneCalls++
				So(dir, ShouldEqual, cacheDir)
				So(args, ShouldResemble, []string{"clone", "--depth", "1", "https://github.com/nextflow-io/hello.git", filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision)})
				So(os.MkdirAll(filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision), 0o755), ShouldBeNil)

				return os.WriteFile(filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision, "main.nf"), []byte("workflow {}\n"), 0o644)
			}

			path, err := NewGitHubResolver(cacheDir).Resolve("nextflow-io/hello")

			So(err, ShouldBeNil)
			So(cloneCalls, ShouldEqual, 1)
			So(path, ShouldEqual, filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision))
			matches, globErr := filepath.Glob(filepath.Join(path, "*.nf"))
			So(globErr, ShouldBeNil)
			So(matches, ShouldNotBeEmpty)
		})

		Convey("missing cache roots are created before invoking git", func() {
			cacheDir := filepath.Join(t.TempDir(), "nested", "cache")
			cloneCalls := 0
			githubResolverRunGit = func(dir string, args ...string) error {
				cloneCalls++

				info, err := os.Stat(dir)
				So(err, ShouldBeNil)
				So(info.IsDir(), ShouldBeTrue)
				So(dir, ShouldEqual, cacheDir)

				cachePath := filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision)
				So(os.MkdirAll(cachePath, 0o755), ShouldBeNil)

				return os.WriteFile(filepath.Join(cachePath, "main.nf"), []byte("workflow {}\n"), 0o644)
			}

			path, err := NewGitHubResolver(cacheDir).Resolve("nextflow-io/hello")

			So(err, ShouldBeNil)
			So(cloneCalls, ShouldEqual, 1)
			So(path, ShouldEqual, filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision))
		})

		Convey("populated cache avoids a second network fetch and returns the same path", func() {
			modulePath := filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision)
			So(os.MkdirAll(modulePath, 0o755), ShouldBeNil)
			So(os.WriteFile(filepath.Join(modulePath, "cached.nf"), []byte("workflow {}\n"), 0o644), ShouldBeNil)

			cloneCalls := 0
			githubResolverRunGit = func(string, ...string) error {
				cloneCalls++
				return nil
			}

			path, err := NewGitHubResolver(cacheDir).Resolve("nextflow-io/hello")

			So(err, ShouldBeNil)
			So(cloneCalls, ShouldEqual, 0)
			So(path, ShouldEqual, modulePath)
		})

		Convey("clone failures are reported as fetch failures", func() {
			githubResolverRunGit = func(string, ...string) error {
				return fmt.Errorf("repository not found")
			}

			_, err := NewGitHubResolver(cacheDir).Resolve("nonexistent/repo999")

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "fetch failure")
			So(err.Error(), ShouldContainSubstring, "nonexistent/repo999")
		})

		Convey("explicit revisions are included in the cache path and clone arguments", func() {
			cloneCalls := 0
			githubResolverRunGit = func(dir string, args ...string) error {
				cloneCalls++
				So(dir, ShouldEqual, cacheDir)
				So(args, ShouldResemble, []string{"clone", "--depth", "1", "--branch", "main", "https://github.com/owner/repo.git", filepath.Join(cacheDir, "owner", "repo", "main")})
				So(os.MkdirAll(filepath.Join(cacheDir, "owner", "repo", "main"), 0o755), ShouldBeNil)

				return os.WriteFile(filepath.Join(cacheDir, "owner", "repo", "main", "module.nf"), []byte("workflow {}\n"), 0o644)
			}

			path, err := NewGitHubResolver(cacheDir).Resolve("owner/repo@main")

			So(err, ShouldBeNil)
			So(cloneCalls, ShouldEqual, 1)
			So(path, ShouldEqual, filepath.Join(cacheDir, "owner", "repo", "main"))
			So(strings.Contains(path, string(filepath.Separator)+"main"), ShouldBeTrue)
		})

		Convey("GitHub workflow URLs normalize to owner/repo cache keys", func() {
			cloneCalls := 0
			githubResolverRunGit = func(dir string, args ...string) error {
				cloneCalls++
				So(dir, ShouldEqual, cacheDir)
				So(args, ShouldResemble, []string{"clone", "--depth", "1", "https://github.com/nextflow-io/hello.git", filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision)})
				So(os.MkdirAll(filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision), 0o755), ShouldBeNil)

				return os.WriteFile(filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision, "main.nf"), []byte("workflow {}\n"), 0o644)
			}

			path, err := NewGitHubResolver(cacheDir).Resolve("https://github.com/nextflow-io/hello")

			So(err, ShouldBeNil)
			So(cloneCalls, ShouldEqual, 1)
			So(path, ShouldEqual, filepath.Join(cacheDir, "nextflow-io", "hello", defaultGitHubModuleRevision))
		})
	})
}

func TestLocalResolver(t *testing.T) {
	Convey("NewLocalResolver handles C1 local module resolution", t, func() {
		basePath := t.TempDir()
		resolver := NewLocalResolver(basePath)

		Convey("relative specs resolve against the base path", func() {
			modulePath := filepath.Join(basePath, "lib", "foo.nf")
			err := os.MkdirAll(filepath.Dir(modulePath), 0o755)
			So(err, ShouldBeNil)

			err = os.WriteFile(modulePath, []byte("process foo {}\n"), 0o644)
			So(err, ShouldBeNil)

			resolvedPath, err := resolver.Resolve("./lib/foo.nf")

			So(err, ShouldBeNil)
			So(resolvedPath, ShouldEqual, modulePath)
		})

		Convey("parent-directory relative specs resolve against the base path", func() {
			parentDir := t.TempDir()
			resolver := NewLocalResolver(filepath.Join(parentDir, "workflows", "subdir"))
			modulePath := filepath.Join(parentDir, "modules", "foo.nf")
			err := os.MkdirAll(filepath.Dir(modulePath), 0o755)
			So(err, ShouldBeNil)

			err = os.WriteFile(modulePath, []byte("process foo {}\n"), 0o644)
			So(err, ShouldBeNil)

			resolvedPath, err := resolver.Resolve("../../modules/foo.nf")

			So(err, ShouldBeNil)
			So(resolvedPath, ShouldEqual, modulePath)
		})

		Convey("absolute specs resolve to the absolute path", func() {
			absoluteDir := t.TempDir()
			absolutePath := filepath.Join(absoluteDir, "foo.nf")
			err := os.WriteFile(absolutePath, []byte("workflow {}\n"), 0o644)
			So(err, ShouldBeNil)

			resolvedPath, err := resolver.Resolve(absolutePath)

			So(err, ShouldBeNil)
			So(resolvedPath, ShouldEqual, absolutePath)
		})

		Convey("missing local files return an error with the missing path", func() {
			missingPath := filepath.Join(basePath, "missing.nf")

			resolvedPath, err := resolver.Resolve("./missing.nf")

			So(err, ShouldNotBeNil)
			So(resolvedPath, ShouldBeBlank)
			So(err.Error(), ShouldContainSubstring, missingPath)
		})
	})
}

func TestChainResolver(t *testing.T) {
	Convey("NewChainResolver handles C3 chained module resolution", t, func() {
		Convey("the first resolver handles a matching local spec without falling back", func() {
			localCalls := 0
			githubCalls := 0

			resolver := NewChainResolver(
				ModuleResolverFunc(func(spec string) (string, error) {
					localCalls++
					So(spec, ShouldEqual, "./local.nf")

					return "/work/local.nf", nil
				}),
				ModuleResolverFunc(func(string) (string, error) {
					githubCalls++

					return "", nil
				}),
			)

			path, err := resolver.Resolve("./local.nf")

			So(err, ShouldBeNil)
			So(path, ShouldEqual, "/work/local.nf")
			So(localCalls, ShouldEqual, 1)
			So(githubCalls, ShouldEqual, 0)
		})

		Convey("later resolvers handle a spec after earlier resolvers return an error", func() {
			localCalls := 0
			githubCalls := 0

			resolver := NewChainResolver(
				ModuleResolverFunc(func(spec string) (string, error) {
					localCalls++
					So(spec, ShouldEqual, "owner/repo")

					return "", errors.New("unsupported local module spec")
				}),
				ModuleResolverFunc(func(spec string) (string, error) {
					githubCalls++
					So(spec, ShouldEqual, "owner/repo")

					return "/cache/owner/repo/HEAD", nil
				}),
			)

			path, err := resolver.Resolve("owner/repo")

			So(err, ShouldBeNil)
			So(path, ShouldEqual, "/cache/owner/repo/HEAD")
			So(localCalls, ShouldEqual, 1)
			So(githubCalls, ShouldEqual, 1)
		})
	})
}
