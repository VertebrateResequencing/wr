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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// TestReleaseDurabilityResendWaitsForWrite proves that a re-sent release or
// bury, which the manager has already applied, is not acknowledged while the
// bbolt write lock holds the job's state off disk.
func TestReleaseDurabilityResendWaitsForWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job a runner has released after a failed attempt", t, func() {
		server, jq, job := startedJobForResend(ctx, t, releaseResendRetries)

		defer server.Stop(ctx, true)
		defer disconnect(jq)

		So(jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent release is not acknowledged while writes are held off disk", func() {
			_, acked := reportWithWriteHeld(server, func() error {
				return jq.releaseAfterAttempt(job, failedAttempt(), FailReasonExit)
			})

			So(acked, ShouldBeFalse)

			jobs, err := jq.GetByRepGroup(releaseDurabilityRepGroup, false, 0, "", false, false)
			So(err, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].UntilBuried, ShouldEqual, releaseResendRetries)
		})
	})

	Convey("Given a job a runner has buried", t, func() {
		server, jq, job := startedJobForResend(ctx, t, releaseDurabilityRetries)

		defer server.Stop(ctx, true)
		defer disconnect(jq)

		So(jq.Bury(job, failedAttempt(), FailReasonExit), ShouldBeNil)

		Convey("a re-sent bury is acknowledged, but not while writes are held off disk", func() {
			_, acked := reportWithWriteHeld(server, func() error {
				return jq.Bury(job, failedAttempt(), FailReasonExit)
			})

			So(acked, ShouldBeFalse)
		})
	})
}
