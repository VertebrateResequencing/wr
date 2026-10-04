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
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/VertebrateResequencing/wr/internal/replyproxy"
	"github.com/VertebrateResequencing/wr/jobqueue"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const lsfPendingState = "PEND"

// TestLSFBsubResentAdd checks that wr lsf bsub reports a job as submitted when
// its add was sent again after the connection dropped and the first copy had
// already queued the job, while a genuine duplicate is still not submitted.
func TestLSFBsubResentAdd(t *testing.T) {
	Convey("wr lsf bsub's submission", t, func() {
		withQueueCommandTestServer(t, func(_ *jobqueue.Client, reqs *jqs.Requirements, serverConfig jobqueue.ServerConfig) {
			proxy := replyproxy.Start(t, "localhost:"+serverConfig.Port)

			jq, err := jobqueue.ConnectWithTokenFile(proxy.Addr(), serverConfig.CAFile,
				serverConfig.CertDomain, serverConfig.TokenFile, testConnectTimeout)
			So(err, ShouldBeNil)

			defer func() {
				So(jq.Disconnect(), ShouldBeNil)
			}()

			job := newQueueCommandJob("echo lsf bsub resent", "bsub", reqs)
			job.BsubMode = "development"

			Convey("is reported submitted when its add is resent after the manager queued it", func() {
				proxy.Armed.Store(true)

				code, output := runSubmitBsubJobForTest(jq, job)

				So(proxy.Dropped.Load(), ShouldEqual, 1)
				So(code, ShouldEqual, 0)

				stored, errg := jq.GetByEssence(job.ToEssense(), false, false)
				So(errg, ShouldBeNil)
				So(stored, ShouldNotBeNil)
				So(stored.BsubID, ShouldBeGreaterThan, 0)
				So(output, ShouldEqual,
					fmt.Sprintf("Job <%d> is submitted to default queue <wr>.\n", stored.BsubID))
			})

			Convey("of a job already queued, without a resend, is refused as a duplicate", func() {
				code, _ := runSubmitBsubJobForTest(jq, job)
				So(code, ShouldEqual, 0)

				code, output := runSubmitBsubJobForTest(jq, job)

				So(proxy.Dropped.Load(), ShouldEqual, 0)
				So(code, ShouldEqual, lsfNoCommandExitCode)
				So(output, ShouldEqual, "Duplicate command specified. Job not submitted.\n")
			})
		})
	})
}

// runSubmitBsubJobForTest runs submitBsubJob, returning the exit code it asked
// for (0 if none) and what it printed.
func runSubmitBsubJobForTest(jq *jobqueue.Client, job *jobqueue.Job) (int, string) {
	originalCmdExit := cmdExit
	cmdExit = func(code int) {
		panic(commandExitPanic{code: code})
	}

	defer func() {
		cmdExit = originalCmdExit
	}()

	var code int

	output := captureStdout(func() {
		code = recoverCommandExit(func() { submitBsubJob(jq, job) })
	})

	return code, output
}

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
