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
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	lqaInteractive = "interactive"
	lqaNormal      = "normal"
	lqaAvoid       = "scheduler_queues_avoid"
)

func TestLSFQueuesAvoidIgnoresEmptyElements(t *testing.T) {
	Convey("Given an lsf scheduler with an interactive and a normal queue", t, func() {
		s := &lsf{
			sortedqs: []string{lqaInteractive, lqaNormal},
			queues: map[string]map[string]int{
				lqaInteractive: {},
				lqaNormal:      {},
			},
		}

		determine := func(other map[string]string) (string, error) {
			return s.determineQueue(&Requirements{RAM: 1, Time: time.Hour, Cores: 1, Other: other})
		}

		Convey("a trailing comma in scheduler_queues_avoid avoids only the named queue", func() {
			queue, err := determine(map[string]string{lqaAvoid: lqaInteractive + ","})
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqaNormal)
		})

		Convey("an empty or whitespace element between names avoids nothing more", func() {
			queue, err := determine(map[string]string{lqaAvoid: lqaInteractive + ",, ,"})
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqaNormal)
		})

		Convey("an empty name to avoid matches no queue", func() {
			So(queueShouldBeAvoided(lqaNormal, []string{lqaInteractive, ""}), ShouldBeFalse)
			So(queueShouldBeAvoided(lqaInteractive, []string{lqaInteractive, ""}), ShouldBeTrue)
		})

		Convey("a trailing comma in a scheduler_queue list never picks an empty queue name", func() {
			queue, err := determine(map[string]string{
				"scheduler_queue": lqaInteractive + ",",
				lqaAvoid:          lqaInteractive,
			})
			So(queue, ShouldEqual, "")
			So(err, ShouldNotBeNil)
		})
	})
}
