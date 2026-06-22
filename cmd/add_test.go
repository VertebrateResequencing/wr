/*******************************************************************************
 * Copyright (c) 2016, 2018, 2026 Genome Research Ltd.
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

package cmd

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

var (
	errMissingSynchronousAddContext = errors.New("missing context")
	errUnexpectedSynchronousJobs    = errors.New("AddAndWait received unexpected jobs")
	errMissingIgnoreComplete        = errors.New("AddAndWait did not preserve ignoreComplete")
	errUnexpectedSynchronousEnv     = errors.New("AddAndWait received unexpected env vars")
)

const synchronousAddBuriedHelper = "buried"

func TestAddQueuesAvoidDefault(t *testing.T) {
	Convey("add queues_avoid default is applied when unset", t, func() {
		flag := addCmd.Flags().Lookup("queues_avoid")
		So(flag, ShouldNotBeNil)
		So(flag.Value.String(), ShouldEqual, "interactive")
		So(cmdQueuesAvoidAdd, ShouldEqual, "interactive")
	})
}

func TestSynchronousAddDoesNotUsePollingHelper(t *testing.T) {
	Convey("cmd/add.go no longer contains the sync polling helper", t, func() {
		source, err := os.ReadFile(filepath.Join("..", "cmd", "add.go"))
		So(err, ShouldBeNil)
		So(string(source), ShouldNotContainSubstring, "waitForJobCompletion")
	})
}

type synchronousAddTestClient struct {
	exitCode int
	stdout   string
	stderr   string
}

func (c synchronousAddTestClient) AddAndWait(ctx context.Context, jobs []*jobqueue.Job,
	envVars []string, ignoreComplete bool) ([]*jobqueue.Job, error) {
	if ctx == nil {
		return nil, errMissingSynchronousAddContext
	}

	if len(jobs) != 1 || jobs[0].Cmd != "sync command" {
		return nil, errUnexpectedSynchronousJobs
	}

	if !ignoreComplete {
		return nil, errMissingIgnoreComplete
	}

	if len(envVars) != 1 || envVars[0] != "SYNC_TEST=1" {
		return nil, errUnexpectedSynchronousEnv
	}

	return []*jobqueue.Job{{
		Exitcode: c.exitCode,
		StdOutC:  zlibCompress([]byte(c.stdout)),
		StdErrC:  zlibCompress([]byte(c.stderr)),
	}}, nil
}

func zlibCompress(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}

	var compressed bytes.Buffer

	writer, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		panic(err)
	}

	_, err = writer.Write(data)
	if err != nil {
		panic(err)
	}

	if err = writer.Close(); err != nil {
		panic(err)
	}

	return compressed.Bytes()
}

type synchronousAddExitCode int

func runSynchronousAddHelper(exitCode int, stdout string, stderr string, exit func(int)) {
	synchronousAddWithExit(
		synchronousAddTestClient{exitCode: exitCode, stdout: stdout, stderr: stderr},
		&jobqueue.Job{Cmd: "sync command"},
		[]string{"SYNC_TEST=1"},
		true,
		exit,
	)
}

func TestSynchronousAddPrintsStdoutAndExitsZero(t *testing.T) {
	Convey("wr add --sync prints stdout and exits zero for a successful job", t, func() {
		stdout, stderr, exitCode := runSynchronousAddInProcess(t, "success")

		So(exitCode, ShouldEqual, 0)
		So(stdout, ShouldEqual, "sync stdout\n")
		So(stderr, ShouldBeBlank)
	})
}

func TestSynchronousAddExitsWithBuriedJobExitCode(t *testing.T) {
	Convey("wr add --sync exits with the buried job exit code", t, func() {
		stdout, stderr, exitCode := runSynchronousAddInProcess(t, synchronousAddBuriedHelper)

		So(exitCode, ShouldEqual, 3)
		So(stdout, ShouldBeBlank)
		So(stderr, ShouldEqual, "sync stderr\n")
	})
}

func runSynchronousAddInProcess(t *testing.T, helper string) (string, string, int) {
	t.Helper()

	stdoutReader, stdoutWriter := synchronousAddPipe(t)
	defer stdoutReader.Close()

	stderrReader, stderrWriter := synchronousAddPipe(t)
	defer stderrReader.Close()

	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter

	exitCode := 0

	func() {
		defer func() {
			os.Stdout, os.Stderr = originalStdout, originalStderr

			So(stdoutWriter.Close(), ShouldBeNil)
			So(stderrWriter.Close(), ShouldBeNil)

			recovered := recover()
			if recovered == nil {
				return
			}

			code, ok := recovered.(synchronousAddExitCode)
			if !ok {
				panic(recovered)
			}

			exitCode = int(code)
		}()

		runSynchronousAddNamedHelper(helper, func(code int) {
			panic(synchronousAddExitCode(code))
		})
	}()

	stdout, err := io.ReadAll(stdoutReader)
	So(err, ShouldBeNil)

	stderr, err := io.ReadAll(stderrReader)
	So(err, ShouldBeNil)

	return string(stdout), string(stderr), exitCode
}

func synchronousAddPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	return reader, writer
}

func runSynchronousAddNamedHelper(helper string, exit func(int)) {
	switch helper {
	case "success":
		runSynchronousAddHelper(0, "sync stdout", "", exit)
	case synchronousAddBuriedHelper:
		runSynchronousAddHelper(3, "", "sync stderr", exit)
	}
}
