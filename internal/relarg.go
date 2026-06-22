/*******************************************************************************
 * Copyright (c) 2025 Genome Research Ltd.
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

package internal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/shlex"
)

const (
	equalSplitParts = 2
)

// GetFilesInDir returns a map of all the files in the given directory, with
// their absolute paths as keys and true as values. It returns nil if the
// directory does not exist or cannot be read. It actually includes all
// directory entries, even subdirectories, because we care about those being
// relative too.
func GetFilesInDir(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	filesInDir := make(map[string]bool, len(entries))

	for _, entry := range entries {
		filesInDir[filepath.Join(dir, entry.Name())] = true
	}

	return filesInDir
}

// CmdlineHasRelativePaths checks if the given command line has any arguments
// that look like relative references to the given files in the given dir. You
// should use GetFilesInDir() for the filesInDir arg.
//
// NB: there may be both false negatives and false positives, so do not rely on
// this 100%.
func CmdlineHasRelativePaths(filesInDir map[string]bool, dir, cmdline string) bool {
	args, err := shlex.Split(cmdline)
	if err != nil {
		return false
	}

	for i, arg := range args {
		if i == 0 && isExe(arg) {
			continue
		}

		if argIsRelativeGlob(filesInDir, arg) {
			return true
		}

		if argIsARelativePath(filesInDir, dir, arg) {
			return true
		}
	}

	return false
}

func isExe(arg string) bool {
	exe, _ := exec.LookPath(arg) //nolint:errcheck

	return exe != ""
}

func argIsRelativeGlob(filesInDir map[string]bool, arg string) bool {
	arg = strings.TrimPrefix(arg, "./")
	arg = strings.TrimSuffix(arg, "/")
	arg = strings.TrimSuffix(arg, "/*")

	if arg == "" {
		return false
	}

	for absPath := range filesInDir {
		basename := filepath.Base(absPath)

		matched, err := filepath.Match(arg, basename)
		if err != nil {
			return false
		}

		if matched {
			return true
		}
	}

	return false
}

// argIsARelativePath checks if the given fragment that came from a command line
// argument is one of the given actual absolute file paths in the given
// directory.
//
// NB: use github.com/google/shlex to split your command line into arguments,
// not github.com/mattn/go-shellwords, as the latter stops at ; and && etc.
func argIsARelativePath(filesInDir map[string]bool, dir, arg string) bool {
	if fileInDir(filesInDir, dir, arg) {
		return true
	}

	arg = cleanArg(arg)
	if arg == "" {
		return false
	}

	return fileInDir(filesInDir, dir, arg)
}

func fileInDir(filesInDir map[string]bool, dir, arg string) bool {
	path := filepath.Join(dir, arg)

	return filesInDir[path]
}

func cleanArg(arg string) string {
	arg = strings.TrimSuffix(arg, ")")
	arg = strings.TrimSuffix(arg, ";")

	parts := strings.Split(arg, "=")
	if len(parts) == equalSplitParts {
		arg = parts[1]
	}

	return arg
}
