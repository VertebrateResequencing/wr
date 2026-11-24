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

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	// speedLayout is speed.sh's layout generator, relative to this package.
	speedLayout = "../speedlayout.sh"

	// ownTest is the test the generated tree's package holds itself.
	ownTest = "TestOwn"
)

func TestSpeedLayout(t *testing.T) {
	Convey("Given a tree holding a package with a test of its own", t, func() {
		tree := t.TempDir()
		out := filepath.Join(t.TempDir(), "layouts")
		writeFile(t, filepath.Join(tree, "go.mod"), "module example.com/tree\n\ngo 1.21\n")
		writeFile(t, filepath.Join(tree, "p", "p.go"), "package p\n")
		writeFile(t, filepath.Join(tree, "p", "p_test.go"),
			"package p\n\nimport \"testing\"\n\nfunc "+ownTest+"(t *testing.T) {}\n")
		before := treeFiles(t, tree)

		Convey("layout 0 is the tree as it is: no overlay and nothing written", func() {
			overlay, err := runSpeedLayout(tree, "p", "0", out)
			So(err, ShouldBeNil)
			So(overlay, ShouldBeEmpty)

			_, err = os.Stat(out)
			So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)
			So(listTests(t, tree, ""), ShouldResemble, []string{ownTest})
		})

		Convey("layout 3 builds with 3 more Test functions, through an overlay alone", func() {
			overlay, err := runSpeedLayout(tree, "p", "3", out)
			So(err, ShouldBeNil)
			So(overlay, ShouldStartWith, out+string(os.PathSeparator))
			So(listTests(t, tree, overlay), ShouldResemble, []string{
				"TestSpeedLayoutPad1", "TestSpeedLayoutPad2", "TestSpeedLayoutPad3", ownTest,
			})
			So(treeFiles(t, tree), ShouldResemble, before)
			So(listTests(t, tree, ""), ShouldResemble, []string{ownTest})
		})

		Convey("layout 1 gives a package a layout of its own name", func() {
			writeFile(t, filepath.Join(tree, "q", "q.go"), "package other\n")
			overlay, err := runSpeedLayout(tree, "q", "1", out)
			So(err, ShouldBeNil)
			So(listTests(t, tree, overlay), ShouldResemble, []string{ownTest, "TestSpeedLayoutPad1"})
		})

		Convey("it refuses a tree that already has the generated file", func() {
			writeFile(t, filepath.Join(tree, "p", "aaa_speed_layout_test.go"), "package p\n")
			_, err := runSpeedLayout(tree, "p", "2", out)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "already exists")
		})

		Convey("it refuses a layout that is not a whole number", func() {
			_, err := runSpeedLayout(tree, "p", "-1", out)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "not a whole number")
		})
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// treeFiles returns the relative path of every file under dir.
func treeFiles(t *testing.T, dir string) []string {
	t.Helper()

	var files []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		files = append(files, rel)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return files
}

// runSpeedLayout runs the generator and returns what it printed, or an error
// holding its stderr.
func runSpeedLayout(tree, pkg, layout, out string) (string, error) {
	cmd := exec.Command(speedLayout, tree, pkg, layout, out) //nolint:noctx // A short local script.

	var stderr strings.Builder

	cmd.Stderr = &stderr

	stdout, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}

	return strings.TrimSpace(string(stdout)), nil
}

// listTests builds the tree's packages with the given overlay (none if empty)
// and returns the Test functions they hold, in the binaries' order.
func listTests(t *testing.T, tree, overlay string) []string {
	t.Helper()

	args := []string{"test", "-list", "."}
	if overlay != "" {
		args = append(args, "-overlay", overlay)
	}

	cmd := exec.Command("go", append(args, "./...")...) //nolint:noctx,gosec // A short local build of a temporary module.
	cmd.Dir = tree

	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test -list in %s: %s\n%s", tree, err, output)
	}

	var tests []string

	for line := range strings.SplitSeq(string(output), "\n") {
		if strings.HasPrefix(line, "Test") {
			tests = append(tests, line)
		}
	}

	return tests
}
