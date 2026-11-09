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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// testStoreFixture is a log store, as a gzipped tar of its files, holding
	// one live psimjob reserved on host h1 as pid 7 and one complete psimjob
	// that exited 0. testGolden is what dbstart printed, at 338eaed6 when it
	// read bbolt, for a bbolt database holding the same two jobs. Both were
	// made by jobqueue's TestWriteDBStartFixture (dbstart_fixture_test.go).
	testStoreFixture = "testdata/store.tar.gz"
	testGolden       = "testdata/golden.tsv"

	testDirPerm  = 0o700
	testFilePerm = 0o600
)

func TestUsage(t *testing.T) {
	Convey("dbstart prints usage and exits 2", t, func() {
		var out, errOut bytes.Buffer

		for _, args := range [][]string{{}, {"a", "b"}, {"-schema", "a"}} {
			So(runArgs(args, &out, &errOut), ShouldEqual, exitUsage)
		}

		So(out.String(), ShouldBeEmpty)
		So(errOut.String(), ShouldEqual, strings.Repeat("usage: dbstart <db dir>\n", 3))
	})
}

func TestPrintJobKinds(t *testing.T) {
	Convey("printJob prints a relbury.sh job as kind relb, named by its first argument, and skips a job of "+
		"any other command", t, func() {
		relb := &jobqueue.Job{Cmd: ": relb t1.rel.4; h=$(hostname -s); exit 3", Cwd: "/x",
			State: jobqueue.JobStateBuried, Exitcode: 3, Attempts: 2, Host: "h3", Pid: 11}
		other := &jobqueue.Job{Cmd: "echo not a soak job", Cwd: "/x", State: jobqueue.JobStateComplete}

		var out bytes.Buffer

		printJob(&out, "jobslive", relb)
		printJob(&out, "jobscomplete", other)

		So(out.String(), ShouldEqual, "jobslive\t"+relb.Key()+"\trelb\tt1.rel.4\tburied\t3\t2\th3\t11\t0\t0\n")
	})
}

func TestRunPrintsBboltColumns(t *testing.T) {
	Convey("Given a store with one live psimjob (reserved, host h1, pid 7) and one complete psimjob (exit 0)",
		t, func() {
			dbDir := extractTestStore(t)

			golden, err := os.ReadFile(testGolden)
			So(err, ShouldBeNil)

			Convey("dbstart prints exactly the two lines the bbolt dbstart printed for the same jobs", func() {
				var out, errOut bytes.Buffer

				So(runArgs([]string{dbDir}, &out, &errOut), ShouldEqual, 0)
				So(errOut.String(), ShouldBeEmpty)
				So(strings.Count(string(golden), "\n"), ShouldEqual, 2)
				So(out.String(), ShouldEqual, string(golden))
			})
		})

	Convey("dbstart of a path with no store reports the error and exits 1", t, func() {
		var out, errOut bytes.Buffer

		So(runArgs([]string{filepath.Join(t.TempDir(), "absent")}, &out, &errOut), ShouldEqual, 1)
		So(out.String(), ShouldBeEmpty)
		So(errOut.String(), ShouldStartWith, "dbstart: ")
	})
}

// extractTestStore extracts testStoreFixture into a new store directory and
// returns its path.
func extractTestStore(t *testing.T) string {
	t.Helper()

	dbDir := filepath.Join(t.TempDir(), "db")
	So(os.Mkdir(dbDir, testDirPerm), ShouldBeNil)

	f, err := os.Open(testStoreFixture)
	So(err, ShouldBeNil)

	defer f.Close()

	gz, err := gzip.NewReader(f)
	So(err, ShouldBeNil)

	tr := tar.NewReader(gz)

	for {
		hdr, errn := tr.Next()
		if errn == io.EOF {
			break
		}

		So(errn, ShouldBeNil)

		content, errr := io.ReadAll(tr)
		So(errr, ShouldBeNil)
		So(os.WriteFile(filepath.Join(dbDir, filepath.Base(hdr.Name)), content, testFilePerm), ShouldBeNil)
	}

	return dbDir
}
