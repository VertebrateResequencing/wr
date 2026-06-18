/*******************************************************************************
 * Copyright (c) 2021 Genome Research Ltd.
 *
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 ******************************************************************************/

package filepath

// this file implements utility routines for manipulating filename paths.

import (
	"os"
	"path/filepath"
	"strings"
)

// RelToAbsPath returns the absolute path of a file given its relative path and
// the directory name.
func RelToAbsPath(path string, dir string) string {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(dir, absPath)
	}

	return absPath
}

// TildaToHome converts a path beginning with ~/ to the absolute path based in
// the current home directory. If that cannot be determined, path is returned
// unaltered.
func TildaToHome(path string) string {
	if path == "" {
		return ""
	}

	home, herr := os.UserHomeDir()
	if herr == nil && home != "" && strings.HasPrefix(path, "~/") {
		path = strings.TrimLeft(path, "~/")
		path = filepath.Join(home, path)
	}

	return path
}
