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
	"errors"
	"testing"

	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

func TestServerClosedQueueErrors(t *testing.T) {
	Convey("Closed queue errors include queue context", t, func() {
		s := &Server{}

		killed, err := s.killJob(context.Background(), "job-1")

		So(killed, ShouldBeFalse)
		So(err, ShouldNotBeNil)

		var qerr queue.Error
		So(errors.As(err, &qerr), ShouldBeTrue)
		So(qerr.Queue, ShouldEqual, serverQueueName)
		So(qerr.Op, ShouldEqual, "Get")
		So(qerr.Item, ShouldEqual, "job-1")
		So(errors.Is(qerr.Err, queue.ErrQueueClosed), ShouldBeTrue)
		So(err.Error(), ShouldEqual, "queue("+serverQueueName+") Get(job-1): queue closed")
	})
}
