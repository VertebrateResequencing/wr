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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
)

const (
	// racLearnedJobs is the limit-blocked ready backlog
	// BenchmarkRACLearnedChange cycles over: about the 60,000 of item 6.3's
	// add-storm-lsf fixture.
	racLearnedJobs = 60000

	// racLearnedReqGroup is the ReqGroup of its jobs.
	racLearnedReqGroup = "rac-learned"

	// racLearnedUserRAM is its jobs' own RAM, and racLearnedRAMs the learned
	// RAMs its ops alternate between.
	racLearnedUserRAM = 600

	// racLearnedRAMChange and racLearnedOtherChange name the variants of
	// BenchmarkRACLearnedChange.
	racLearnedRAMChange   = "ram"
	racLearnedOtherChange = "other"
)

var racLearnedRAMs = [2]int{200, 300} //nolint:gochecknoglobals

// BenchmarkRACLearnedChange records the cost of a rac cycle over racLearnedJobs
// ready jobs blocked by their full limit group. In change=ram, the learned RAM
// of their ReqGroup has changed since they were prepared, so every one of them
// is recomputed and moved to a new reserve group. In change=other, only some
// other ReqGroup's learned values have changed, so every one of them is
// compared and none recomputed. In change=none nothing has changed. One op is
// one such cycle. It reports max-qlock-ns, the longest a reader of the queue
// waited for its lock during the op, which is what a reserve would have
// waited.
func BenchmarkRACLearnedChange(b *testing.B) {
	ctx := context.Background()
	s, q, data := racLearnedFixture(b)
	ramIndex := 0

	for _, change := range []string{racLearnedRAMChange, racLearnedOtherChange, "none"} {
		b.Run("change="+change, func(b *testing.B) {
			b.ReportAllocs()

			var maxWait time.Duration

			for range b.N {
				switch change {
				case racLearnedRAMChange:
					ramIndex = (ramIndex + 1) % 2
					racLearnedSetRAM(s, racLearnedRAMs[ramIndex])
				case racLearnedOtherChange:
					racLearnedOtherChanged(s)
				}

				wait := racQueueLockWaitDuring(q, func() {
					s.buildSchedulerGroups(ctx, q, data, racLimitRunnerCmd)
				})
				maxWait = max(maxWait, wait)
			}

			b.StopTimer()

			if got := racLearnedFirstRAM(data); change == racLearnedRAMChange && got != racLearnedRAMs[ramIndex] {
				b.Fatalf("jobs at RAM %d, not %d", got, racLearnedRAMs[ramIndex])
			}

			b.ReportMetric(float64(maxWait.Nanoseconds()), "max-qlock-ns")
		})
	}
}

// racLearnedFixture returns a server whose limit group has a limit of 0, its
// queue holding racLearnedJobs ready jobs of that limit group, prepared with no
// learned values, and their data as a cycle is given it.
func racLearnedFixture(b *testing.B) (*Server, *queue.Queue, []any) {
	b.Helper()

	ctx := context.Background()
	q := queue.New(ctx, "rac-learned-bench")
	s := &Server{
		q:  q,
		db: racNoStatsDB(racLearnedReqGroup),
		limiter: limiter.New(func(context.Context, string) *limiter.GroupData {
			return limiter.NewCountGroupData(0)
		}),
	}

	defs := make([]*queue.ItemDef, 0, racLearnedJobs)
	data := make([]any, 0, racLearnedJobs)

	for i := range racLearnedJobs {
		job := &Job{
			Cmd: fmt.Sprintf("echo rac-learned %d", i), Cwd: testCwd,
			ReqGroup: racLearnedReqGroup, RepGroup: racLearnedReqGroup,
			Requirements: &jqs.Requirements{RAM: racLearnedUserRAM, Time: time.Minute, Cores: 1},
			LimitGroups:  []string{"rac-learned-lg"},
		}

		job.Lock()
		updateJobRequirementsForRetry(job, job.Override, nil)
		job.schedulerGroup = job.derivedLocked().group
		job.Unlock()

		defs = append(defs, &queue.ItemDef{
			Key: job.Key(), ReserveGroup: job.schedulerGroup, Data: job, TTR: time.Hour,
		})
		data = append(data, job)
	}

	if _, _, err := q.AddMany(ctx, defs); err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if err := q.Destroy(); err != nil {
			b.Fatal(err)
		}
	})

	return s, q, data
}

// racLearnedSetRAM makes ram the current learned RAM of racLearnedReqGroup.
func racLearnedSetRAM(s *Server, ram int) {
	c := s.db.reqStats

	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[racLearnedReqGroup] = &reqStatEntry{rec: &jqs.Requirements{RAM: ram, DiskSet: true}, loaded: true}
	c.changes.Add(1)
}

// racLearnedOtherChanged records a change to some other ReqGroup's learned
// values, as a load that changes them does.
func racLearnedOtherChanged(s *Server) {
	s.db.reqStats.changes.Add(1)
}

// racQueueLockWaitDuring runs f while another goroutine reads q's state over
// and over, and returns the longest one of those reads waited.
func racQueueLockWaitDuring(q *queue.Queue, f func()) time.Duration {
	var (
		stop    atomic.Bool
		maxWait time.Duration
		wg      sync.WaitGroup
	)

	wg.Go(func() {
		for !stop.Load() {
			start := time.Now()

			q.HasReady("")

			maxWait = max(maxWait, time.Since(start))
		}
	})

	f()
	stop.Store(true)
	wg.Wait()

	return maxWait
}

// racLearnedFirstRAM returns the RAM of the first job of data.
func racLearnedFirstRAM(data []any) int {
	job, ok := data[0].(*Job)
	if !ok {
		return 0
	}

	job.RLock()
	defer job.RUnlock()

	return job.Requirements.RAM
}
