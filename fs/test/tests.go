/*******************************************************************************
 * Copyright (c) 2021 Genome Research Ltd.
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

package test

// this file implements utility routines related to testing.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// readAndRestoreError records an error for restoring the *os.file handle.
type readAndRestoreError struct{}

// Error returns an error when restoring an already closed handle.
func (r *readAndRestoreError) Error() string {
	return "ReadAndRestore from closed MockStdInErr"
}

// MockStdErr represents a mock implementation of STDERR.
type MockStdErr struct {
	origStderr   *os.File
	stderrReader *os.File
	outCh        chan []byte
}

// MockStdIn represents a mock implementation of STDIN.
type MockStdIn struct {
	origStdin   *os.File
	stdinWriter *os.File
}

// NewMockStdErr creates a new MockStdErr and starts capturing the STDERR. Be
// sure to call GetAndRestoreStdErr() after you've done writing to STDERR, or
// RestoreStdErr() if this returns an error.
func NewMockStdErr() (*MockStdErr, error) {
	origStderr, stderrReader, outCh, err := mockStdErrRW()

	return &MockStdErr{
		origStderr:   origStderr,
		stderrReader: stderrReader,
		outCh:        outCh,
	}, err
}

// mockStdErrRW mocks STDERR and starts capturing it.
func mockStdErrRW() (*os.File, *os.File, chan []byte, error) {
	stderrReader, stderrWriter, err := os.Pipe()

	origStderr := os.Stderr
	os.Stderr = stderrWriter

	outCh := make(chan []byte)

	go func() {
		var b bytes.Buffer
		if _, errc := io.Copy(&b, stderrReader); errc != nil {
			outCh <- []byte(errc.Error())

			return
		}

		bytes := b.Bytes()
		outCh <- bytes
	}()

	return origStderr, stderrReader, outCh, err
}

// GetAndRestoreStdErr stops capturing the STDERR and returns already captured
// STDERR.
func (se *MockStdErr) GetAndRestoreStdErr() (string, error) {
	if se.stderrReader == nil {
		return "", &readAndRestoreError{}
	}

	os.Stderr.Close()

	out := <-se.outCh

	se.RestoreStdErr()

	return string(out), nil
}

// RestoreStdErr restores the STDERR to its original value.
func (se *MockStdErr) RestoreStdErr() {
	os.Stderr = se.origStderr

	if se.stderrReader != nil {
		se.stderrReader.Close()
		se.stderrReader = nil
	}
}

// NewMockStdIn creates a new MockStdIn. Be sure to call RestoreStdIn() after
// you've done reading from STDIN, or if this returns an error.
func NewMockStdIn() (*MockStdIn, error) {
	origStdin, stdinWriter, err := mockStdInRW()

	return &MockStdIn{
		origStdin:   origStdin,
		stdinWriter: stdinWriter,
	}, err
}

// mockStdInRW writes the given value to a replaced STDIN.
func mockStdInRW() (*os.File, *os.File, error) {
	stdinReader, stdinWriter, err := os.Pipe()

	origStdin := os.Stdin
	os.Stdin = stdinReader

	return origStdin, stdinWriter, err
}

// WriteString writes the given string to our mock STDIN.
func (si *MockStdIn) WriteString(stdinText string) error {
	_, err := si.stdinWriter.WriteString(stdinText + "\n")

	return err
}

// RestoreStdIn restores the STDIN to its original value.
func (si *MockStdIn) RestoreStdIn() {
	os.Stdin = si.origStdin

	if si.stdinWriter != nil {
		si.stdinWriter.Close()
		si.stdinWriter = nil
	}
}

// FilePathInTempDir creates a new temporary directory and returns the absolute
// path to a file called basename in that directory (without actually creating
// the file).
func FilePathInTempDir(t *testing.T, basename string) string {
	t.Helper()
	tmpdir := t.TempDir()

	return filepath.Join(tmpdir, basename)
}
