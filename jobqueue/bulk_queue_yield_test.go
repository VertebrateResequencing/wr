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

// This file covers reserves made while a kick, resume or suspend of many jobs
// runs: such a loop takes the queue's lock once per job, and must let a reserve
// waiting on the lock take it between two of its jobs, not only once the loop
// has finished or the reserve has waited long enough for Go's mutex to hand it
// over.

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// bulkYieldTargets is how many jobs each bulk op handles.
	bulkYieldTargets = 200

	// bulkYieldFillers is how many ready jobs there are to reserve.
	bulkYieldFillers = 10

	bulkYieldRepGroup = "bulk_yield"
)

// bulkYieldBury reserves and buries every ready job on s, which are those of
// keys, and returns their server-side jobs.
func bulkYieldBury(s *Server, keys []string) []*Job {
	ctx := context.Background()
	jobs := make([]*Job, 0, len(keys))

	for range keys {
		item, _ := s.reserveItem(ctx, &clientRequest{})
		So(item, ShouldNotBeNil)

		job, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)
		So(s.q.Bury(item.Key), ShouldBeNil)

		job.Lock()
		job.State = JobStateBuried
		job.Unlock()

		jobs = append(jobs, job)
	}

	return jobs
}

// bulkYieldDelay reserves every ready job on s, which are those of keys, and
// releases each into the delay sub-queue for an hour, so it stays suspendable.
func bulkYieldDelay(s *Server, keys []string) []*Job {
	ctx := context.Background()

	for range keys {
		item, _ := s.reserveItem(ctx, &clientRequest{})
		So(item, ShouldNotBeNil)
		So(s.q.SetDelay(item.Key, time.Hour), ShouldBeNil)
		So(s.q.Release(ctx, item.Key), ShouldBeNil)

		job, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		job.Lock()
		job.State = JobStateDelayed
		job.Unlock()
	}

	return nil
}

// bulkYieldSuspend suspends the ready jobs of keys on s.
func bulkYieldSuspend(s *Server, keys []string) []*Job {
	So(s.suspendJobs(context.Background(), keys), ShouldEqual, len(keys))

	return nil
}

// TestBulkQueueOpsLetReservesIn proves that a reserve begun while a kick,
// resume or suspend of many jobs runs gets its job before the op has handled
// more than one more job. It runs the op on one processor with GC off, so the
// reserve can run at all before the op ends only if the op yields between
// jobs.
func TestBulkQueueOpsLetReservesIn(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, bc := range []struct {
		name    string
		prepare func(s *Server, keys []string) []*Job
		hook    *func(key string)
		op      func(s *Server, jobs []*Job, keys []string) int
	}{
		{
			name:    "kick of buried jobs",
			prepare: bulkYieldBury,
			hook:    &kickQueuedHook,
			op:      func(s *Server, jobs []*Job, _ []string) int { return s.kickJobs(ctx, jobs) },
		},
		{
			name:    "resume of suspended jobs",
			prepare: bulkYieldSuspend,
			hook:    &resumeQueuedHook,
			op:      func(s *Server, _ []*Job, keys []string) int { return s.resumeJobs(ctx, keys) },
		},
		{
			name:    "suspend of delayed jobs",
			prepare: bulkYieldDelay,
			hook:    &suspendQueuedHook,
			op:      func(s *Server, _ []*Job, keys []string) int { return s.suspendJobs(ctx, keys) },
		},
	} {
		Convey("Given a server whose "+bc.name+" has a reserve begun after its first job", t, func() {
			_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
			serverConfig.Timings.ItemTTR = time.Hour

			s, _, token, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer s.Stop(ctx, true)

			jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
			So(err, ShouldBeNil)

			defer disconnect(jq)

			targets := bulkYieldJobs("target", bulkYieldTargets, standardReqs)
			inserts, _, err := jq.Add(targets, os.Environ(), true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, bulkYieldTargets)

			keys := make([]string, len(targets))
			for i, job := range targets {
				keys[i] = job.Key()
			}

			jobs := bc.prepare(s, keys)

			inserts, _, err = jq.Add(bulkYieldJobs("filler", bulkYieldFillers, standardReqs), os.Environ(), true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, bulkYieldFillers)

			var handled atomic.Int64

			reserved := make(chan int64, 1)

			*bc.hook = func(string) {
				if handled.Add(1) != 1 {
					return
				}

				go func() {
					item, _ := s.reserveItem(ctx, &clientRequest{})
					if item == nil {
						reserved <- -1

						return
					}

					reserved <- handled.Load()
				}()
			}

			defer func() { *bc.hook = nil }()

			procs := runtime.GOMAXPROCS(1)
			gc := debug.SetGCPercent(-1)

			n := bc.op(s, jobs, keys)

			debug.SetGCPercent(gc)
			runtime.GOMAXPROCS(procs)

			So(n, ShouldEqual, bulkYieldTargets)

			Convey("the reserve gets a job before the op has handled a third job", func() {
				So(<-reserved, ShouldBeBetweenOrEqual, 1, 2)
			})
		})
	}
}

// bulkYieldJobs returns n small jobs named after kind.
func bulkYieldJobs(kind string, n int, reqs *jqs.Requirements) []*Job {
	jobs := make([]*Job, n)
	for i := range jobs {
		jobs[i] = &Job{
			Cmd: "true " + kind + " " + strconv.Itoa(i), Cwd: testCwd, RepGroup: bulkYieldRepGroup,
			ReqGroup: bulkYieldRepGroup, Requirements: reqs, Retries: 3,
		}
	}

	return jobs
}
