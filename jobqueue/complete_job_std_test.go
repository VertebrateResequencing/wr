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

package jobqueue

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	completeStdOutMarker = "COMPLETEJOBSTDOUTMARKER"
	completeStdErrMarker = "COMPLETEJOBSTDERRMARKER"
	completeStdLiveTail  = "COMPLETEJOBLIVETAILMARKER\n"
)

func TestCompleteJobKeepsNoStd(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a server with a connected client", t, func() {
		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		addAndReserve := func(cmd, repGroup string) *Job {
			inserts, _, erra := jq.Add([]*Job{{
				Cmd: cmd, Cwd: testCwd, RepGroup: repGroup, ReqGroup: repGroup,
				Requirements: standardReqs,
			}}, os.Environ(), true)
			So(erra, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			job, errr := jq.Reserve(2 * time.Second)
			So(errr, ShouldBeNil)
			So(job, ShouldNotBeNil)
			So(job.RepGroup, ShouldEqual, repGroup)

			return job
		}

		archivedRecord := func(key string) *Job {
			var record *Job

			errv := server.db.bolt.View(func(tx *bolt.Tx) error {
				encoded := tx.Bucket(bucketJobsComplete).Get([]byte(key))
				if encoded == nil {
					return nil
				}

				decoded, errd := server.db.decodeJob(encoded)
				record = decoded

				return errd
			})
			So(errv, ShouldBeNil)
			So(record, ShouldNotBeNil)

			return record
		}

		// the decompressed streams are asserted first only so a failure names the
		// output that was kept, rather than printing its compressed bytes.
		soRecordHasNoStd := func(record *Job) {
			stdout, errs := record.StdOut()
			So(errs, ShouldBeNil)
			So(stdout, ShouldBeEmpty)

			stderr, errs := record.StdErr()
			So(errs, ShouldBeNil)
			So(stderr, ShouldBeEmpty)

			So(record.StdOutC, ShouldBeEmpty)
			So(record.StdErrC, ShouldBeEmpty)
		}

		echoCmd := "echo " + completeStdOutMarker + "; echo " + completeStdErrMarker + " >&2"

		Convey("a successful job's archived record has no output, while a failed job's output is still stored", func() {
			okJob := addAndReserve(echoCmd, "complete-std-ok")
			So(jq.Execute(ctx, okJob, config.RunnerExecShell), ShouldBeNil)

			failJob := addAndReserve(echoCmd+"; false", "complete-std-fail")
			So(jq.Execute(ctx, failJob, config.RunnerExecShell), ShouldNotBeNil)

			record := archivedRecord(okJob.Key())
			So(record.Exited, ShouldBeTrue)
			So(record.Exitcode, ShouldEqual, 0)
			soRecordHasNoStd(record)

			failed, errg := jq.GetByEssence(failJob.ToEssense(), true, false)
			So(errg, ShouldBeNil)
			So(failed, ShouldNotBeNil)
			So(failed.Exitcode, ShouldEqual, 1)

			stdout, errs := failed.StdOut()
			So(errs, ShouldBeNil)
			So(stdout, ShouldEqual, completeStdOutMarker)

			stderr, errs := failed.StdErr()
			So(errs, ShouldBeNil)
			So(stderr, ShouldEqual, completeStdErrMarker)
		})

		Convey("a successful job's archived record keeps neither its final output nor its live tail", func() {
			job := addAndReserve("echo complete std live tail", "complete-std-live")
			So(jq.Started(job, os.Getpid()), ShouldBeNil)

			job.StdOutC = compressStd([]byte(completeStdLiveTail))
			job.StdErrC = compressStd([]byte(completeStdLiveTail))

			killCalled, errt := jq.Touch(job)
			So(errt, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			item, errq := server.q.Get(job.Key())
			So(errq, ShouldBeNil)

			live, ok := item.Data().(*Job)
			So(ok, ShouldBeTrue)

			liveOut, errs := live.StdOut()
			So(errs, ShouldBeNil)
			So(liveOut, ShouldEqual, completeStdLiveTail)

			So(jq.Archive(job, &JobEndState{
				Exited:   true,
				Exitcode: 0,
				EndTime:  time.Now(),
				Stdout:   compressStd([]byte(completeStdOutMarker)),
				Stderr:   compressStd([]byte(completeStdErrMarker)),
			}), ShouldBeNil)

			record := archivedRecord(job.Key())
			So(record.Exitcode, ShouldEqual, 0)
			soRecordHasNoStd(record)
		})
	})
}
