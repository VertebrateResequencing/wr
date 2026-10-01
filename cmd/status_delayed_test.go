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
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestStatusDelayedLine(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	attempted := " (attempted at " + started.Format(shortTimeFormat) + ")"

	const delayedPrefix = "Status: delayed following a problem, prior to retrying; will become ready "

	Convey("A delayed job's status line", t, func() {
		job := &jobqueue.Job{State: jobqueue.JobStateDelayed, DelayTime: 2 * time.Minute}

		Convey("says when the manager's queue will make it ready", func() {
			job.ReadyTime = now.Add(90 * time.Second)
			job.EndTime = now.Add(-time.Hour)
			job.StartTime = started

			So(statusDelayedLine(job, now), ShouldEqual, delayedPrefix+"in 1m30s"+attempted)
		})

		Convey("never gives a negative time when that has passed", func() {
			job.ReadyTime = now.Add(-time.Second)

			So(statusDelayedLine(job, now), ShouldEqual, delayedPrefix+"imminently")
		})

		Convey("falls back to its EndTime and DelayTime from a manager that does not say", func() {
			job.EndTime = now.Add(-30 * time.Second)
			job.StartTime = started

			So(statusDelayedLine(job, now), ShouldEqual, delayedPrefix+"in 1m30s"+attempted)
		})

		Convey("never gives a saturated time when neither is known", func() {
			So(statusDelayedLine(job, now), ShouldEqual, delayedPrefix+"imminently")
		})
	})

	Convey("A buried job's status line only says when it was attempted if it started", t, func() {
		const buried = "Status: buried - you need to fix the problem and then `wr retry`"

		job := &jobqueue.Job{State: jobqueue.JobStateBuried}
		So(statusBuriedLine(job), ShouldEqual, buried)

		job.StartTime = started
		So(statusBuriedLine(job), ShouldEqual, buried+attempted)
	})
}

func TestStatusDetailsOfAJobReleasedWithNoExitState(t *testing.T) {
	Convey("wr status details of a job a runner released with no exit state says when it will be ready", t, func() {
		ctx := context.Background()
		testConfig, serverConfig, addr, reqs, server, token := startStatusTestServer(ctx, t)

		oldConfig, oldCAFile := config, caFile

		config, caFile = testConfig, testConfig.ManagerCAFile
		defer func() {
			config, caFile = oldConfig, oldCAFile
		}()

		defer server.Stop(ctx, true)

		jq, err := jobqueue.Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, testConnectTimeout)

		So(err, ShouldBeNil)
		defer func() {
			So(jq.Disconnect(), ShouldBeNil)
		}()

		repGroup := "status-delayed-no-exit"
		job := statusTestJob("echo delayed", repGroup, reqs)
		job.Retries = 3
		addStatusJobs(jq, job)

		reserved, err := jq.Reserve(50 * time.Millisecond)
		So(err, ShouldBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())

		// as a runner does when it has not enough time left to run the job
		releasedFrom := time.Now()

		So(jq.Release(reserved, nil, "not enough time to run"), ShouldBeNil)

		releasedBy := time.Now()

		details := runStatusForTest(t, "--identifier", repGroup, "--output", "details")
		So(details, ShouldNotContainSubstring, "attempted at")

		matches := regexp.MustCompile(`will become ready in (\S+)\n`).FindStringSubmatch(details)
		So(matches, ShouldHaveLength, 2)

		remaining, err := time.ParseDuration(matches[1])
		So(err, ShouldBeNil)
		So(remaining, ShouldBeGreaterThan, 0)
		// it is rounded to the second
		So(remaining, ShouldBeLessThanOrEqualTo, reserved.DelayTime+time.Second/2)

		jsonOutput := runStatusForTest(t, "--identifier", repGroup, "--output", "json")

		var statuses []jobqueue.JStatus
		So(json.Unmarshal([]byte(jsonOutput), &statuses), ShouldBeNil)
		So(statuses, ShouldHaveLength, 1)
		So(statuses[0].Ready, ShouldNotBeNil)
		So(time.Unix(0, *statuses[0].Ready), ShouldHappenOnOrBetween,
			releasedFrom.Add(reserved.DelayTime), releasedBy.Add(reserved.DelayTime))
	})
}
