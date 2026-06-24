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

package cmd

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const lsfPendingState = "PEND"

func TestLSFBjobsShowsSuspendedAsPending(t *testing.T) {
	Convey("wr lsf bjobs shows suspended bsub-mode jobs as pending", t, func() {
		withQueueCommandTestServer(t, func(jq *jobqueue.Client, reqs *jqs.Requirements, _ jobqueue.ServerConfig) {
			job := newQueueCommandJob("echo lsf suspended", "rg-lsf-suspended", reqs)
			job.BsubID = 207
			addQueueCommandJobs(jq, job)

			changed, err := jq.Suspend([]*jobqueue.JobEssence{job.ToEssense()})
			So(err, ShouldBeNil)
			So(changed, ShouldEqual, 1)

			output := runLSFBjobsForTest(t, "-o", "JOBID STAT")
			lines := nonEmptyStatusLines(output)

			So(lines, ShouldHaveLength, 2)
			So(lines[0], ShouldEqual, "JOBID STAT")
			So(strings.Fields(lines[1]), ShouldResemble, []string{"207", lsfPendingState})
		})
	})
}

func runLSFBjobsForTest(t *testing.T, args ...string) string {
	t.Helper()

	resetLSFBjobsForTest(t)
	So(lsfBjobsCmd.ParseFlags(args), ShouldBeNil)

	reader, writer, err := os.Pipe()
	So(err, ShouldBeNil)

	defer reader.Close()

	originalStdout := os.Stdout

	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	lsfBjobsCmd.Run(lsfBjobsCmd, nil)

	So(writer.Close(), ShouldBeNil)

	output, err := io.ReadAll(reader)
	So(err, ShouldBeNil)

	return string(output)
}

func resetLSFBjobsForTest(t *testing.T) {
	t.Helper()

	lsfFormat = ""
	lsfQueue = "wr"
	lsfNoHeader = false

	for _, flag := range []struct {
		name  string
		value string
	}{
		{"output", ""},
		{"queue", "wr"},
	} {
		So(lsfBjobsCmd.Flags().Set(flag.name, flag.value), ShouldBeNil)
	}
}
