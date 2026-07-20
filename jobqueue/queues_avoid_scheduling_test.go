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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// TestJobCodecPreservesQueuesAvoid checks that a Job carrying a
// scheduler_queues_avoid requirement (with OtherSet left false, as
// client.determineOverrideAndReq produces) survives the BincHandle codec
// round-trip used to move jobs between client, server and db.
func TestJobCodecPreservesQueuesAvoid(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("A Job with scheduler_queues_avoid survives the codec round-trip", t, func() {
		job := &Job{
			Cmd:        "echo codec",
			Cwd:        testCwd,
			CwdMatters: true,
			ReqGroup:   "results_frontend",
			RepGroup:   "rg",
			Requirements: &jqs.Requirements{
				RAM:   300,
				Time:  time.Second,
				Cores: 1,
				Disk:  1,
				Other: map[string]string{queuesAvoidTestKey: queuesAvoidTestValue},
			},
		}

		ch := new(codec.BincHandle)

		var encoded []byte

		So(codec.NewEncoderBytes(&encoded, ch).Encode(job), ShouldBeNil)

		var decoded Job

		So(codec.NewDecoderBytes(encoded, ch).Decode(&decoded), ShouldBeNil)
		So(decoded.Requirements, ShouldNotBeNil)
		So(decoded.Requirements.Other[queuesAvoidTestKey], ShouldEqual, queuesAvoidTestValue)
	})
}

const (
	queuesAvoidTestValue = "interactive,inference"
	queuesAvoidTestKey   = "scheduler_queues_avoid"
)

// seedReqGroupRAMRecommendation writes a spread of peak-RAM stat values for the
// given reqGroup directly into the db's stat bucket, so that
// recommendedReqGroupMemory() returns a non-zero recommendation (simulating a
// manager that has "learned" resource usage for that reqGroup). Disk and time
// buckets are left empty so only the RAM recommendation is active.
func seedReqGroupRAMRecommendation(testDB *db, reqGroup string, maxRAM int) {
	err := testDB.bolt.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobRAM)

		for i := range 40 {
			val := maxRAM - (39-i)*5
			if val < 1 {
				val = 1
			}

			if e := putJobStat(b, reqGroup, val); e != nil {
				return e
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
}

// avoidJob builds a ready job in the given reqGroup that carries a
// scheduler_queues_avoid requirement, exactly as client.determineOverrideAndReq
// would produce it (Other set, OtherSet left false).
func avoidJob(cwd, reqGroup, cmd string, ram int) *Job {
	return &Job{
		Cmd:        cmd,
		Cwd:        cwd,
		CwdMatters: true,
		ReqGroup:   reqGroup,
		RepGroup:   "results_frontend_jobs",
		Requirements: &jqs.Requirements{
			RAM:   ram,
			Time:  time.Second,
			Cores: 1,
			Disk:  1,
			Other: map[string]string{queuesAvoidTestKey: queuesAvoidTestValue},
		},
		Retries: 3,
	}
}

// avoidReq returns a fresh Requirements carrying scheduler_queues_avoid, with
// OtherSet left false (as client.determineOverrideAndReq produces).
func avoidReq(ram int) *jqs.Requirements {
	return &jqs.Requirements{
		RAM:   ram,
		Time:  time.Second,
		Cores: 1,
		Other: map[string]string{queuesAvoidTestKey: queuesAvoidTestValue},
	}
}

// TestServerSchedulerQueuesAvoidEndToEnd is the faithful end-to-end check: it
// runs a real in-process server, adds jobs (over the wire, so the real codec and
// db-store paths run) whose requirements carry scheduler_queues_avoid, learns a
// resource recommendation for their reqGroup by archiving a first batch, then
// adds a second batch and asserts the requirements the server hands to the
// scheduler (previouslyScheduledGroups[group].req) still carry
// scheduler_queues_avoid after the recommendation has been learned.
func TestServerSchedulerQueuesAvoidEndToEnd(t *testing.T) {
	ctx := context.Background()

	if runnermode || servermode {
		return
	}

	config, serverConfig, addr, _, clientConnectTime := jobqueueTestInit(true)

	Convey("Given a running server with an echo runner command", t, func() {
		server, _, token, errs := serve(ctx, serverConfig)
		So(errs, ShouldBeNil)

		defer server.Stop(ctx, true)

		server.setRC(serverRC)

		jq, err := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		const (
			firstBatch     = 2
			secondBatch    = 2
			learnedPeakRAM = 100
			reqGroup       = "results_frontend"
			repGroup       = "results_frontend_jobs"
		)

		tmpdir := t.TempDir()

		preLearningGroup := schedulerGroupString(reqForScheduler(avoidReq(300)), nil)
		learnedGroup := schedulerGroupString(reqForScheduler(avoidReq(learnedPeakRAM)), nil)
		So(preLearningGroup, ShouldNotEqual, learnedGroup)

		groupReqAvoid := func(group string) (string, bool) {
			server.psgmutex.RLock()
			defer server.psgmutex.RUnlock()

			g, existed := server.previouslyScheduledGroups[group]
			if !existed {
				return "", false
			}

			g.RLock()
			defer g.RUnlock()

			return g.req.Other[queuesAvoidTestKey], true
		}

		makeJobs := func(label string, count int) []*Job {
			jobs := make([]*Job, 0, count)
			for i := range count {
				jobs = append(jobs, &Job{
					Cmd:          fmt.Sprintf("echo %s-%d", label, i),
					Cwd:          tmpdir,
					ReqGroup:     reqGroup,
					RepGroup:     repGroup,
					Requirements: avoidReq(300),
					Retries:      3,
				})
			}

			return jobs
		}

		archiveGroup := func(group string, count int) {
			for range count {
				job, reserveErr := jq.ReserveScheduled(3*time.Second, group)
				So(reserveErr, ShouldBeNil)
				So(job, ShouldNotBeNil)
				So(jq.Started(job, os.Getpid()), ShouldBeNil)
				So(jq.Archive(job, &JobEndState{
					Exited:   true,
					Exitcode: 0,
					PeakRAM:  learnedPeakRAM,
					CPUtime:  time.Second,
					EndTime:  time.Now(),
				}), ShouldBeNil)
			}
		}

		Convey("queues_avoid reaches the scheduler both before and after learning", func() {
			inserts, _, err := jq.Add(makeJobs("batch1", firstBatch), envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, firstBatch)

			// the pre-learning group must be scheduled and carry queues_avoid.
			So(waitForScheduledGroupCount(server, preLearningGroup, firstBatch), ShouldBeTrue)

			avoid, existed := groupReqAvoid(preLearningGroup)
			So(existed, ShouldBeTrue)
			So(avoid, ShouldEqual, queuesAvoidTestValue)

			// archiving the first batch teaches the manager a RAM recommendation.
			archiveGroup(preLearningGroup, firstBatch)

			recRAM, err := server.db.recommendedReqGroupMemory(reqGroup)
			So(err, ShouldBeNil)
			So(recRAM, ShouldEqual, learnedPeakRAM)

			// the second batch must now be scheduled under the learned group, and
			// that group's req must STILL carry queues_avoid.
			inserts, _, err = jq.Add(makeJobs("batch2", secondBatch), envVars, true)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, secondBatch)

			So(waitForScheduledGroupCount(server, learnedGroup, secondBatch), ShouldBeTrue)

			avoid, existed = groupReqAvoid(learnedGroup)
			So(existed, ShouldBeTrue)
			So(avoid, ShouldEqual, queuesAvoidTestValue)
		})
	})
}

// TestServerSchedulerQueuesAvoidPreserved drives the real server-side scheduler
// grouping (buildSchedulerGroups -> processReadyJob -> recommendedReqForGroup ->
// updateJobRequirementsForRetry -> schedulerGroupSnapshot -> countJobInGroup)
// and asserts that the scheduler_queues_avoid requirement survives into the
// *scheduler.Requirements of every scheduled group, both on a fresh db and once
// resource recommendations have been learned for the reqGroup.
func TestServerSchedulerQueuesAvoidPreserved(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server db and a queue", t, func() {
		tmpdir := t.TempDir()

		testDB, _, err := initDB(
			ctx,
			filepath.Join(tmpdir, "queue.db"),
			filepath.Join(tmpdir, "queue.db.bak"),
			internal.Development,
			false,
			false,
		)
		So(err, ShouldBeNil)

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		server := &Server{
			db:                        testDB,
			previouslyScheduledGroups: make(map[string]*sgroup),
			limiter:                   limiter.New(nil),
		}

		q := queue.New(ctx, "test-queues-avoid")

		defer func() {
			So(q.Destroy(), ShouldBeNil)
		}()

		const reqGroup = "results_frontend"

		runGrouping := func(iterations, jobsPer, ram int) (groupsSeen, missing, learnedRAMGroups int) {
			for iter := range iterations {
				allitemdata := make([]any, 0, jobsPer)

				for j := range jobsPer {
					job := avoidJob(tmpdir, reqGroup, fmt.Sprintf("echo job-%d-%d", iter, j), ram)
					allitemdata = append(allitemdata, job)
				}

				groups := server.buildSchedulerGroups(ctx, q, allitemdata, "runner-cmd")

				for _, g := range groups {
					groupsSeen++

					if g.req.Other[queuesAvoidTestKey] != queuesAvoidTestValue {
						missing++
					}

					if g.req.RAM != reqForScheduler(&jqs.Requirements{RAM: ram, Time: time.Second, Cores: 1, Disk: 1}).RAM {
						learnedRAMGroups++
					}
				}
			}

			return groupsSeen, missing, learnedRAMGroups
		}

		Convey("On a fresh db (no learned recommendations) queues_avoid is preserved", func() {
			recRAM, errr := testDB.recommendedReqGroupMemory(reqGroup)
			So(errr, ShouldBeNil)
			So(recRAM, ShouldEqual, 0)

			groupsSeen, missing, _ := runGrouping(300, 8, 300)
			So(groupsSeen, ShouldBeGreaterThan, 0)
			So(missing, ShouldEqual, 0)
		})

		Convey("Once recommendations are learned, queues_avoid is still preserved", func() {
			seedReqGroupRAMRecommendation(testDB, reqGroup, 800)

			recRAM, errr := testDB.recommendedReqGroupMemory(reqGroup)
			So(errr, ShouldBeNil)
			So(recRAM, ShouldBeGreaterThan, 0)

			groupsSeen, missing, learnedRAMGroups := runGrouping(300, 8, 300)
			So(groupsSeen, ShouldBeGreaterThan, 0)
			So(missing, ShouldEqual, 0)

			// prove the learned recommendation actually changed the req that
			// reached the group (so we know we exercised the "learned" path).
			So(learnedRAMGroups, ShouldEqual, groupsSeen)
		})

		Convey("With concurrent status reads of the same jobs, queues_avoid is preserved (race)", func() {
			seedReqGroupRAMRecommendation(testDB, reqGroup, 800)

			const (
				iterations = 200
				jobsPer    = 8
			)

			missing := 0
			groupsSeen := 0

			for iter := range iterations {
				jobs := make([]*Job, 0, jobsPer)
				allitemdata := make([]any, 0, jobsPer)

				for j := range jobsPer {
					job := avoidJob(tmpdir, reqGroup, fmt.Sprintf("echo cjob-%d-%d", iter, j), 300)
					jobs = append(jobs, job)
					allitemdata = append(allitemdata, job)
				}

				var wg sync.WaitGroup

				wg.Add(1)

				go func() {
					defer wg.Done()

					for _, job := range jobs {
						snap := job.schedulerGroupSnapshot()
						if snap.requirements.Other[queuesAvoidTestKey] != queuesAvoidTestValue {
							missing++
						}
					}
				}()

				groups := server.buildSchedulerGroups(ctx, q, allitemdata, "runner-cmd")
				for _, g := range groups {
					groupsSeen++

					if g.req.Other[queuesAvoidTestKey] != queuesAvoidTestValue {
						missing++
					}
				}

				wg.Wait()
			}

			So(groupsSeen, ShouldBeGreaterThan, 0)
			So(missing, ShouldEqual, 0)
		})
	})
}
