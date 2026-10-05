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
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/jobqueue"
	. "github.com/smartystreets/goconvey/convey"
)

// jobKinds are the psimjob.sh kinds prodsim's actors add.
var jobKinds = []string{ //nolint:gochecknoglobals // a test's fixed list
	"put", "fofnput", "walk", "combine", "tidy", "build", "publish", "portal_dedupe", "portal_compress", "pipeline",
}

const (
	testPortalJobs   = 10
	cmdKindField     = 1
	cmdMemField      = 4
	kindsPollTimeout = 30 * time.Second
)

// runJobAdders runs every actor that adds psimjob.sh jobs until the run ends,
// with a small portal burst.
func runJobAdders(s *sim, ctx context.Context) {
	s.cfg.portalJobs = testPortalJobs
	s.cfg.portalEvery = defaultPortalEvery

	var wg sync.WaitGroup

	for _, actor := range []func(*sim, context.Context){
		(*sim).ibackupServer, (*sim).fofnWatcher, (*sim).wrstatMulti, (*sim).wrstatUI, (*sim).portal, (*sim).waiter,
	} {
		wg.Go(func() { actor(s, ctx) })
	}

	wg.Wait()
}

func TestEveryJobHoldsLessMemoryThanItRequests(t *testing.T) {
	Convey("Given every job-adding actor running against a manager", t, func() {
		r := startOutageRun(t, testWrstatSimMinute, 0.1, runJobAdders)
		defer r.cleanup()

		jq := r.s.newJQ(context.Background(), "test")
		So(jq, ShouldNotBeNil)

		defer disconnect(jq)

		jobs := waitForKinds(jq)

		Convey("each kind's command holds less memory than the job's RAM requirement, so LSF never kills it", func() {
			seen := make(map[string]bool)

			for _, job := range jobs {
				f := strings.Fields(job.Cmd)
				So(len(f), ShouldBeGreaterThan, cmdMemField)

				held, err := strconv.Atoi(f[cmdMemField])
				So(err, ShouldBeNil)
				So(held, ShouldBeLessThan, job.Requirements.RAM)

				seen[f[cmdKindField]] = true
			}

			for _, kind := range jobKinds {
				So(seen, ShouldContainKey, kind)
			}
		})
	})
}

// waitForKinds waits for the manager to hold jobs of every jobKinds kind,
// returning its incomplete jobs.
func waitForKinds(jq *jobqueue.Client) []*jobqueue.Job {
	var jobs []*jobqueue.Job

	for deadline := time.Now().Add(kindsPollTimeout); time.Now().Before(deadline); time.Sleep(testPoll) {
		var err error

		jobs, err = jq.GetIncomplete(0, "", false, false)
		if err == nil && hasEveryKind(jobs) {
			break
		}
	}

	return jobs
}

// hasEveryKind says if jobs include every jobKinds kind.
func hasEveryKind(jobs []*jobqueue.Job) bool {
	kinds := make(map[string]bool)

	for _, job := range jobs {
		if f := strings.Fields(job.Cmd); len(f) > cmdKindField {
			kinds[f[cmdKindField]] = true
		}
	}

	for _, kind := range jobKinds {
		if !kinds[kind] {
			return false
		}
	}

	return true
}
