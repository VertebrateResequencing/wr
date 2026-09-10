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

package file

// this file implements utility routines related to files.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	fp "github.com/VertebrateResequencing/wr/fs/filepath"
)

// maxFirstLineBytes is the longest first line GetFirstLine will return. It is
// far more than the 64 hexadecimal characters of the container ids that
// GetFirstLine exists to read, and little enough that a file which merely
// happens to match a cidfile glob never gets read into memory.
const maxFirstLineBytes = 4096

// ErrLineTooLong is wrapped by the error GetFirstLine returns when a file's
// first line is longer than maxFirstLineBytes.
var ErrLineTooLong = errors.New("first line longer than " + strconv.Itoa(maxFirstLineBytes) + " bytes")

// ErrEmptyPath is wrapped by the error GetFirstLine and ToString return when
// given an empty path.
var ErrEmptyPath = errors.New("path is empty")

// PathReadError records a path read error.
type PathReadError struct {
	path string
	Err  error
}

// Error returns an error related to a path that could not be read.
func (p *PathReadError) Error() string {
	return fmt.Sprintf("path [%s] could not be read: %s", p.path, p.Err)
}

// Unwrap returns the error that stopped the path being read.
func (p *PathReadError) Unwrap() error {
	return p.Err
}

// GetFirstLine returns the first line, excluding its newline, of the file at
// the given absolute or tilde path.
//
// At most maxFirstLineBytes are read, so that a large file which is not the
// short id file this is for does not get read into memory; if the first line
// is longer than that, the returned error wraps ErrLineTooLong.
func GetFirstLine(filename string) (string, error) {
	if filename == "" {
		return "", &PathReadError{"", ErrEmptyPath}
	}

	absPath := fp.TildaToHome(filename)

	f, err := os.Open(filepath.Clean(absPath))
	if err != nil {
		return "", &PathReadError{absPath, err}
	}

	defer f.Close()

	line, err := firstLine(f)
	if err != nil {
		return "", &PathReadError{absPath, err}
	}

	return line, nil
}

// firstLine returns the content of r up to its first newline, reading no more
// than maxFirstLineBytes+1 bytes. If r gives maxFirstLineBytes bytes without a
// newline and still has more, ErrLineTooLong is returned.
func firstLine(r io.Reader) (string, error) {
	buf := make([]byte, maxFirstLineBytes+1)

	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}

	if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
		return string(buf[:i]), nil
	}

	if n > maxFirstLineBytes {
		return "", ErrLineTooLong
	}

	return string(buf[:n]), nil
}

// ToString takes the path to a file and returns its contents as a string. If
// path begins with a tilde, TildaToHome() is used to first convert the path to
// an absolute path, in order to find the file.
func ToString(path string) (string, error) {
	if path == "" {
		return "", &PathReadError{"", ErrEmptyPath}
	}

	absPath := fp.TildaToHome(path)

	contents, err := os.ReadFile(absPath)
	if err != nil {
		return "", &PathReadError{absPath, err}
	}

	return string(contents), nil
}
