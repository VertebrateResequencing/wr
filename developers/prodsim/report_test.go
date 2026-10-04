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
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestReportShowsCallsBlockedByAnOutage(t *testing.T) {
	Convey("Given a run whose calls rode out a manager outage instead of failing", t, func() {
		dir := t.TempDir()

		// two quick finds, then a find blocked for 5 minutes through an outage
		// (success, as a client riding out outages reports it), and one that
		// failed after the 2 minute Timeout (as before clients rode them out),
		// plus a submit_and_wait, which waits for its jobs by design.
		calls := "t_s\tactor\top\tms\tn\terr\n" +
			"10.0\tfofn\tfind\t40\t3\t\n" +
			"20.0\tfofn\tfind\t60\t3\t\n" +
			"400.0\tfofn\tfind\t300000\t3\t\n" +
			"500.0\tfofn\tfind\t120000\t0\tsend time out\n" +
			"600.0\tibserver\tadd_put\t50\t10\t\n" +
			"700.0\twaiter\tsubmit_and_wait\t600000\t2\t\n"
		So(os.WriteFile(filepath.Join(dir, "calls.tsv"), []byte(calls), filePerm), ShouldBeNil)
		So(os.WriteFile(filepath.Join(dir, "samples.tsv"), []byte(sampleHeader+"\n"), filePerm), ShouldBeNil)

		var out strings.Builder
		So(report(&out, dir), ShouldBeNil)

		Convey("the report counts the calls that took at least the client Timeout, and the time spent in them", func() {
			header := reportRow(out.String(), "actor/op")
			So(header, ShouldNotBeNil)
			So(header[3], ShouldEqual, "slow")
			So(header[4], ShouldEqual, "slow_s")

			find := reportRow(out.String(), "fofn/find")
			So(find, ShouldNotBeNil)
			So(find[1], ShouldEqual, "4")
			So(find[2], ShouldEqual, "1")
			So(find[3], ShouldEqual, "2")
			So(find[4], ShouldEqual, "420")

			add := reportRow(out.String(), "ibserver/add_put")
			So(add, ShouldNotBeNil)
			So(add[3], ShouldEqual, "0")
			So(add[4], ShouldEqual, "0")

			wait := reportRow(out.String(), "waiter/submit_and_wait")
			So(wait, ShouldNotBeNil)
			So(wait[3], ShouldEqual, "0")
			So(wait[4], ShouldEqual, "0")
		})
	})
}

// reportRow returns the whitespace-separated fields of the report line whose
// first field is first, or nil.
func reportRow(report, first string) []string {
	for line := range strings.SplitSeq(report, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == first {
			return f
		}
	}

	return nil
}
