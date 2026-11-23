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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestUnderReserveLoadMetrics(t *testing.T) {
	us := func(n int) time.Duration { return time.Duration(n) * time.Microsecond }

	Convey("Given 2 ops' reserve latencies, some exactly at a threshold or at 1.1 times one, some just past", t, func() {
		latencies := []time.Duration{
			us(500), us(1000), us(1250), us(1375), us(1400), us(2200), us(2300), us(5000), us(5600),
			us(13200), us(13300), us(64000), us(70400), us(71000),
		}

		metrics := underReserveLoadMetrics(2, 300*time.Millisecond, 3*time.Millisecond, latencies)

		Convey("it reports each figure per op, each count holding only the reserves strictly over T, "+
			"and its -tol count only those strictly over 1.1T", func() {
			So(metrics, ShouldResemble, []underReserveLoadMetric{
				{150e6, "ns/op"},
				{7, "reserves/op"},
				{2300, "reserve-p50-us"},
				{71000, "reserve-p99-us"},
				{71000, "reserve-max-us"},
				{6, slowReservesUnit},
				{(1250 + 1375 + 1400 + 2200 + 2300 + 5000 + 5600 + 13200 + 13300 + 64000 + 70400 + 71000) / 2.0,
					slowReserveWaitUnit},
				{5.5, "reserves-over-1.25ms/op"},
				{5, "reserves-over-1.25ms-tol/op"},
				{4.5, "reserves-over-1.5ms/op"},
				{4.5, "reserves-over-1.5ms-tol/op"},
				{4.5, "reserves-over-2ms/op"},
				{4, "reserves-over-2ms-tol/op"},
				{3.5, "reserves-over-2.5ms/op"},
				{3.5, "reserves-over-2.5ms-tol/op"},
				{3.5, "reserves-over-3ms/op"},
				{3.5, "reserves-over-3ms-tol/op"},
				{3.5, "reserves-over-4ms/op"},
				{3.5, "reserves-over-4ms-tol/op"},
				{3, "reserves-over-5ms/op"},
				{3, "reserves-over-5ms-tol/op"},
				{2.5, "reserves-over-6ms/op"},
				{2.5, "reserves-over-6ms-tol/op"},
				{2.5, "reserves-over-8ms/op"},
				{2.5, "reserves-over-8ms-tol/op"},
				{2.5, "reserves-over-10ms/op"},
				{2.5, "reserves-over-10ms-tol/op"},
				{2.5, "reserves-over-12ms/op"},
				{2, "reserves-over-12ms-tol/op"},
				{1.5, "reserves-over-16ms/op"},
				{1.5, "reserves-over-16ms-tol/op"},
				{1.5, "reserves-over-20ms/op"},
				{1.5, "reserves-over-20ms-tol/op"},
				{1.5, "reserves-over-24ms/op"},
				{1.5, "reserves-over-24ms-tol/op"},
				{1.5, "reserves-over-32ms/op"},
				{1.5, "reserves-over-32ms-tol/op"},
				{1.5, "reserves-over-40ms/op"},
				{1.5, "reserves-over-40ms-tol/op"},
				{1.5, "reserves-over-48ms/op"},
				{1.5, "reserves-over-48ms-tol/op"},
				{1, "reserves-over-64ms/op"},
				{0.5, "reserves-over-64ms-tol/op"},
				{5000, slowReserveP50Unit},
				{1500, "max-qlock-us"},
			})
		})
	})

	Convey("Given no reserve past the slow threshold, every slow and tail figure is 0", t, func() {
		metrics := underReserveLoadMetrics(1, time.Millisecond, 0, []time.Duration{time.Microsecond, time.Millisecond})

		So(metrics[5:len(metrics)-1], ShouldHaveLength, 3+2*18)

		for _, m := range metrics[5 : len(metrics)-1] {
			So(m.value, ShouldEqual, 0)
		}
	})
}
