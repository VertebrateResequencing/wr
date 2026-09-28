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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	lqdQueueKey = "scheduler_queue"
	lqdAvoidKey = "scheduler_queues_avoid"
	lqdNormal   = "normal"
	lqdLong     = "long"
	lqdYesterq  = "yesterday"
	lqdInteract = "interactive"
)

func TestLSFManagerQueueDefaults(t *testing.T) {
	Convey("Given an lsf scheduler with manager default queue settings", t, func() {
		s := &lsf{
			config:   &ConfigLSF{},
			sortedqs: []string{lqdInteract, lqdNormal, lqdLong, lqdYesterq},
			queues: map[string]map[string]int{
				lqdInteract: {memlimitKey: 0, runlimitKey: 0},
				lqdNormal:   {memlimitKey: 0, runlimitKey: 12 * 60 * 60},
				lqdLong:     {memlimitKey: 0, runlimitKey: 0},
				lqdYesterq:  {memlimitKey: 0, runlimitKey: 0},
			},
		}

		determine := func(other map[string]string) (string, error) {
			return s.determineQueue(&Requirements{RAM: 1, Time: time.Hour, Cores: 1, Other: other})
		}

		Convey("with neither a job queue nor a default, the automatic choice is kept", func() {
			queue, err := determine(nil)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdInteract)
		})

		Convey("a default queue is used by a job that names no queue", func() {
			s.config.Queue = lqdYesterq

			queue, err := determine(nil)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdYesterq)

			Convey("but a job's own queue overrides it", func() {
				queue, err = determine(map[string]string{lqdQueueKey: lqdLong})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdLong)
			})

			Convey("and a job queue of only empty or whitespace elements counts as unset", func() {
				queue, err = determine(map[string]string{lqdQueueKey: " "})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdYesterq)

				queue, err = determine(map[string]string{lqdQueueKey: ", ,"})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdYesterq)
			})
		})

		Convey("a default queue list is picked amongst, skipping empty elements and unsuitable queues", func() {
			s.config.Queue = "," + lqdNormal + ", ," + lqdLong + ","

			queue, err := determine(nil)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdNormal)

			queue, err = s.determineQueue(&Requirements{RAM: 1, Time: 24 * time.Hour, Cores: 1})
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdLong)
		})

		Convey("spaces around queue list elements are ignored", func() {
			s.config.Queue = lqdNormal + " , " + lqdLong

			queue, err := s.determineQueue(&Requirements{RAM: 1, Time: 24 * time.Hour, Cores: 1})
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdLong)

			queue, err = determine(map[string]string{lqdQueueKey: " " + lqdNormal + ",  " + lqdLong + " "})
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdNormal)

			Convey("including a single queue", func() {
				queue, err = determine(map[string]string{lqdQueueKey: " " + lqdLong + " "})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdLong)

				s.config.Queue = " " + lqdYesterq + " "

				queue, err = determine(nil)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdYesterq)
			})
		})

		Convey("spaces around queues to avoid elements are ignored", func() {
			s.config.QueuesAvoid = lqdNormal + ", " + lqdInteract

			queue, err := determine(nil)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdLong)

			queue, err = determine(map[string]string{lqdAvoidKey: " " + lqdInteract + " , " + lqdNormal + " "})
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdLong)
		})

		Convey("a default queue of only empty or whitespace elements counts as unset", func() {
			s.config.Queue = " , "

			queue, err := determine(nil)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdInteract)
		})

		Convey("default queues to avoid apply to a job with none of its own", func() {
			s.config.QueuesAvoid = lqdInteract + ",,"

			queue, err := determine(nil)
			So(err, ShouldBeNil)
			So(queue, ShouldEqual, lqdNormal)

			Convey("but a job's own queues to avoid replace them, rather than merge", func() {
				queue, err = determine(map[string]string{lqdAvoidKey: lqdNormal})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdInteract)
			})

			Convey("and a job's queues to avoid of only empty elements counts as unset", func() {
				queue, err = determine(map[string]string{lqdAvoidKey: " ,"})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdNormal)
			})

			Convey("but not a job's own single queue, which is always used", func() {
				queue, err = determine(map[string]string{lqdQueueKey: lqdInteract})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdInteract)
			})

			Convey("nor a default single queue", func() {
				s.config.Queue = lqdInteract

				queue, err = determine(nil)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdInteract)
			})

			Convey("and they also filter a job's own queue list", func() {
				queue, err = determine(map[string]string{lqdQueueKey: lqdInteract + "," + lqdNormal})
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdNormal)
			})

			Convey("and they also filter a default queue list", func() {
				s.config.Queue = lqdInteract + "," + lqdLong

				queue, err = determine(nil)
				So(err, ShouldBeNil)
				So(queue, ShouldEqual, lqdLong)
			})
		})

		Convey("default queues to avoid can make a job impossible to schedule", func() {
			s.config.QueuesAvoid = strings.Join([]string{lqdInteract, lqdNormal, lqdLong, lqdYesterq}, ",")

			_, err := determine(nil)
			So(err, ShouldNotBeNil)
		})
	})

	Convey("An lsf scheduler with no config still picks queues automatically", t, func() {
		s := &lsf{sortedqs: []string{lqdNormal}, queues: map[string]map[string]int{lqdNormal: {}}}

		queue, err := s.determineQueue(&Requirements{RAM: 1, Time: time.Hour, Cores: 1})
		So(err, ShouldBeNil)
		So(queue, ShouldEqual, lqdNormal)
	})
}

func TestLSFManagerDefaultQueueReachesBsub(t *testing.T) {
	ctx := context.Background()

	Convey("Given an lsf scheduler with fake LSF exes and a manager default queue", t, func() {
		dir := t.TempDir()
		s := newFakeLSFScheduler(t, dir, filepath.Join(dir, "jargs"), fakeLSFDelays{})
		s.sortedqs = []string{lqdNormal, lqdYesterq}
		s.queues = map[string]map[string]int{lqdNormal: {}, lqdYesterq: {}}
		s.config.Queue = lqdYesterq

		qArgsFile := filepath.Join(dir, "qargs")
		writeFakeExe(t, s.bsubExe, `#!/bin/bash
capture=0
for a in "$@"; do
  if [ "$capture" = "1" ]; then echo "$a" >> `+qArgsFile+`; capture=0; fi
  if [ "$a" = "-q" ]; then capture=1; fi
done
echo "Job <321>"
exit 0
`)

		readQueues := func() []string {
			content, err := os.ReadFile(qArgsFile)
			So(err, ShouldBeNil)

			return strings.Fields(string(content))
		}

		Convey("a job with no queue of its own is submitted to the default queue", func() {
			err := s.schedule(ctx, "false", &Requirements{RAM: 100, Time: time.Minute, Cores: 1}, 0, 1)
			So(err, ShouldBeNil)
			So(readQueues(), ShouldResemble, []string{lqdYesterq})
		})

		Convey("a job with its own queue is submitted to that queue", func() {
			err := s.schedule(ctx, "false", &Requirements{RAM: 100, Time: time.Minute, Cores: 1,
				Other: map[string]string{lqdQueueKey: lqdNormal}}, 0, 1)
			So(err, ShouldBeNil)
			So(readQueues(), ShouldResemble, []string{lqdNormal})
		})
	})
}
