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

// Heap proof for archived-job retention: a manager that runs round after round
// of jobs, each round under a reserve group it never uses again (as wrstat's
// fresh datetime limit group per run does), must not keep the archived jobs in
// its heap. Before the queue cleared the slots it popped and dropped emptied
// reserve groups, every round's jobs, Cmd and all, stayed reachable for the
// manager's lifetime, as did an entry per RepGroup in the server's lookup of
// RepGroup to job keys.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	hrRounds      = 6
	hrJobsPerRnd  = 100
	hrCmdPadBytes = 40 << 10

	// hrMaxGrowth bounds how much the heap may grow between the first and the
	// last round with nothing live. Retaining the archived jobs costs at least
	// hrJobsPerRnd * hrCmdPadBytes (4MB) per round, so 20MB over the 5 rounds
	// measured.
	hrMaxGrowth = 8 << 20
)

func TestServerArchivedJobsLeaveTheHeap(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()
	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a running server", t, func() {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer server.Stop(ctx, true)

		server.setRC(serverRC)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		cwd := t.TempDir()

		Convey("Rounds of jobs, each under a new reserve group, do not grow the heap once archived", func() {
			failures := 0
			heaps := make([]uint64, 0, hrRounds)

			for round := range hrRounds {
				failures += hrRunRound(jq, cwd, round)

				So(server.q.Stats().Items, ShouldEqual, 0)

				heaps = append(heaps, hrHeapInuse())
			}

			So(failures, ShouldEqual, 0)

			var growth uint64
			if last := heaps[len(heaps)-1]; last > heaps[0] {
				growth = last - heaps[0]
			}

			t.Logf("HeapInuse after each round: %v; growth %dMB", heaps, growth>>20)

			So(growth, ShouldBeLessThan, hrMaxGrowth)
			So(hrRepGroupsTracked(server), ShouldEqual, 0)
		})
	})
}

// hrRunRound adds one round of jobs, then reserves, starts and archives every
// one of them, returning how many of those steps failed.
func hrRunRound(jq *Client, cwd string, round int) int {
	req := &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1}
	limitGroup := fmt.Sprintf("datetime<2030-01-01 00:00:%02d", round)
	group := schedulerGroupString(reqForScheduler(req), []string{limitGroup})

	inserts, _, err := jq.Add(hrRoundJobs(cwd, round, req, limitGroup), envVars, true)
	if err != nil || inserts != hrJobsPerRnd {
		return hrJobsPerRnd
	}

	failures := 0

	for range hrJobsPerRnd {
		job, errr := jq.ReserveScheduled(5*time.Second, group)
		if errr != nil || job == nil {
			failures++

			continue
		}

		if hrStartAndArchive(jq, job) != nil {
			failures++
		}
	}

	return failures
}

// hrRoundJobs returns the jobs for one round, each with a unique long command
// and the round's own req group and limit group.
func hrRoundJobs(cwd string, round int, req *jqs.Requirements, limitGroup string) []*Job {
	pad := strings.Repeat("x", hrCmdPadBytes)
	jobs := make([]*Job, 0, hrJobsPerRnd)

	for i := range hrJobsPerRnd {
		jobs = append(jobs, &Job{
			Cmd:          fmt.Sprintf("true r%d.%d # %s", round, i, pad),
			Cwd:          cwd,
			ReqGroup:     fmt.Sprintf("heapretain%d", round),
			RepGroup:     fmt.Sprintf("heapretain%d", round),
			Requirements: req,
			LimitGroups:  []string{limitGroup},
			Retries:      0,
		})
	}

	return jobs
}

// hrRepGroupsTracked returns how many RepGroups the server keeps a key list
// for.
func hrRepGroupsTracked(server *Server) int {
	server.rpl.RLock()
	defer server.rpl.RUnlock()

	return len(server.rpl.lookup)
}

// hrStartAndArchive marks a reserved job as started and then archives it as a
// success.
func hrStartAndArchive(jq *Client, job *Job) error {
	if err := jq.Started(job, os.Getpid()); err != nil {
		return err
	}

	return jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, EndTime: time.Now()})
}

// hrHeapInuse returns HeapInuse after forcing the GC.
func hrHeapInuse() uint64 {
	runtime.GC()
	runtime.GC()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	return ms.HeapInuse
}
