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

package scheduler

import (
	"context"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

type processStatusScheduler struct {
	mock
	host Host
}

func (s *processStatusScheduler) getHost(_ string) (Host, bool) {
	return s.host, s.host != nil
}

type processStatusHost struct {
	stdout string
	err    error
}

func (h *processStatusHost) RunCmd(_ context.Context, _ string, _ bool) (string, string, error) {
	return h.stdout, "", h.err
}

func TestProcessNotRunningOnHostUsesProcessState(t *testing.T) {
	Convey("ProcessNotRunningOnHost treats absent processes as not running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: ""}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeTrue)
	})

	Convey("ProcessNotRunningOnHost treats zombie processes as not running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "Z+\n"}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeTrue)
	})

	Convey("ProcessNotRunningOnHost treats sleeping processes as still running", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{stdout: "S\n"}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeFalse)
	})

	Convey("ProcessNotRunningOnHost treats host command failures as inconclusive", t, func() {
		s := &Scheduler{impl: &processStatusScheduler{host: &processStatusHost{err: context.Canceled}}}

		So(s.ProcessNotRunningOnHost(context.Background(), 42, "host"), ShouldBeFalse)
	})
}
