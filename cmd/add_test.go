// Copyright © 2016-2021,2024,2025 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

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
