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
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

func TestRepGroupStatusCountsDoNotDecodeCompleteJobs(t *testing.T) {
	Convey("Given complete-job lookup keys with an undecodable archived payload", t, func() {
		ctx := context.Background()
		tmpDir := t.TempDir()

		db, _, err := initDB(ctx, filepath.Join(tmpDir, "db"), filepath.Join(tmpDir, "db_bk"),
			internal.Development, false, false)

		So(err, ShouldBeNil)
		defer func() {
			So(db.close(ctx), ShouldBeNil)
		}()

		repGroup := "fast-count"
		completeKey := []byte("complete-key")
		rerunKey := []byte("rerun-key")

		err = db.bolt.Update(func(tx *bolt.Tx) error {
			if errp := tx.Bucket(bucketRGs).Put([]byte(repGroup), nil); errp != nil {
				return errp
			}

			if errp := tx.Bucket(bucketRTK).Put(db.generateLookupKey(repGroup, completeKey), nil); errp != nil {
				return errp
			}

			if errp := tx.Bucket(bucketRTK).Put(db.generateLookupKey(repGroup, rerunKey), nil); errp != nil {
				return errp
			}

			if errp := tx.Bucket(bucketJobsComplete).Put(completeKey, []byte("not-a-job")); errp != nil {
				return errp
			}

			if errp := tx.Bucket(bucketJobsComplete).Put(rerunKey, []byte("not-a-job")); errp != nil {
				return errp
			}

			return tx.Bucket(bucketJobsLive).Put(rerunKey, []byte("live-rerun"))
		})
		So(err, ShouldBeNil)

		Convey("count-only status uses the lookup keys without decoding full jobs", func() {
			summary, errc := db.retrieveCompleteJobStatusByRepGroup(repGroup, false)
			So(errc, ShouldBeNil)
			So(summary.Counts[JobStateComplete], ShouldEqual, 1)
		})

		Convey("lazy startup seeding counts archived keys that have just become live", func() {
			counts, errc := db.retrieveCompleteJobCountsByRepGroups([]string{repGroup})
			So(errc, ShouldBeNil)
			So(counts, ShouldResemble, map[string]int{repGroup: 2})
		})

		Convey("lazy startup seeding reports a missing RepGroup lookup bucket", func() {
			assertLazyStatusSeedMissingBucket(db, repGroup, bucketRTK)
		})

		Convey("lazy startup seeding reports a missing completed-jobs bucket", func() {
			assertLazyStatusSeedMissingBucket(db, repGroup, bucketJobsComplete)
		})

		Convey("recovery reports a missing reverse lookup bucket", func() {
			assertRecoveryStatusSeedMissingBucket(db, repGroup, bucketJobLookupEntries)
		})

		Convey("recovery reports a missing completed-jobs bucket", func() {
			assertRecoveryStatusSeedMissingBucket(db, repGroup, bucketJobsComplete)
		})

		Convey("summary detail mode still decodes only when compact job stats are requested", func() {
			_, errd := db.retrieveCompleteJobStatusByRepGroup(repGroup, true)
			So(errd, ShouldNotBeNil)
		})
	})
}

func TestClientRepGroupStatusCounts(t *testing.T) {
	Convey("Given a server with live and complete jobs in matching report groups", t, func() {
		ctx := context.Background()
		_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		complete := &Job{
			Cmd:          "echo complete status count",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "status-count-a",
		}
		ready := &Job{
			Cmd:          "echo ready status count",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "status-count-a",
		}
		other := &Job{
			Cmd:          "echo other status count",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     "status-count-b",
		}

		added, existed, err := jq.Add([]*Job{complete, ready, other}, envVars, false)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 3)
		So(existed, ShouldEqual, 0)

		item, err := server.q.Get(complete.Key())
		So(err, ShouldBeNil)

		archived, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		archived.StartTime = time.Now().Add(-2 * time.Second)
		archived.EndTime = time.Now().Add(-1 * time.Second)
		archived.State = JobStateComplete
		archived.Exited = true
		archived.PeakRAM = 10
		archived.PeakDisk = 1
		archived.CPUtime = time.Second

		So(server.q.Remove(ctx, complete.Key()), ShouldBeNil)
		So(server.db.archiveJob(ctx, complete.Key(), archived), ShouldBeNil)

		summaries, err := jq.GetStatusByRepGroupMatch("status-count", RepGroupMatchPrefix, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries["status-count-a"].Counts[JobStateComplete], ShouldEqual, 1)
		So(summaries["status-count-a"].Counts[JobStateReady], ShouldEqual, 1)
		So(summaries["status-count-b"].Counts[JobStateReady], ShouldEqual, 1)

		countOnly := NewRepGroupStatus()
		for _, summary := range summaries {
			countOnly.Merge(summary)
		}

		So(countOnly.Counts[JobStateComplete], ShouldEqual, 1)
		So(countOnly.Counts[JobStateReady], ShouldEqual, 2)

		filtered, err := jq.GetStatusByRepGroupMatch("status-count", RepGroupMatchPrefix,
			[]JobState{JobStateReady}, true, false)
		So(err, ShouldBeNil)
		So(filtered["status-count-a"].Counts[JobStateComplete], ShouldEqual, 0)
		So(filtered["status-count-a"].Counts[JobStateReady], ShouldEqual, 1)
		So(filtered["status-count-b"].Counts[JobStateReady], ShouldEqual, 1)

		detailed, err := jq.GetStatusByRepGroupMatch("status-count-a", RepGroupMatchExact, nil, true, true)
		So(err, ShouldBeNil)
		So(detailed["status-count-a"].Memory.NumDataValues(), ShouldEqual, uint(1))
		So(detailed["status-count-a"].StartTime.IsZero(), ShouldBeFalse)
		So(detailed["status-count-a"].EndTime.IsZero(), ShouldBeFalse)
	})
}

func TestClientRepGroupStatusCountsIncludeSuspended(t *testing.T) {
	Convey("Given a report group with ready, suspended, and complete jobs", t, func() {
		ctx := context.Background()
		_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		repGroup := "rg-api"
		ready := &Job{
			Cmd:          "echo api status ready",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}
		suspended := &Job{
			Cmd:          "echo api status suspended",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}
		complete := &Job{
			Cmd:          "echo api status complete",
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: standardReqs,
			RepGroup:     repGroup,
		}

		added, existed, err := jq.Add([]*Job{ready, suspended, complete}, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 3)
		So(existed, ShouldEqual, 0)

		changed, err := jq.Suspend([]*JobEssence{suspended.ToEssense()})
		So(err, ShouldBeNil)
		So(changed, ShouldEqual, 1)

		item, err := server.q.Get(complete.Key())
		So(err, ShouldBeNil)

		archived, ok := item.Data().(*Job)
		So(ok, ShouldBeTrue)

		archived.StartTime = time.Now().Add(-2 * time.Second)
		archived.EndTime = time.Now().Add(-1 * time.Second)
		archived.State = JobStateComplete
		archived.Exited = true

		So(server.q.Remove(ctx, complete.Key()), ShouldBeNil)
		So(server.db.archiveJob(ctx, complete.Key(), archived), ShouldBeNil)

		summaries, err := jq.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact, nil, true, false)
		So(err, ShouldBeNil)
		So(summaries[repGroup].Counts[JobStateReady], ShouldEqual, 1)
		So(summaries[repGroup].Counts[JobStateSuspended], ShouldEqual, 1)
		So(summaries[repGroup].Counts[JobStateComplete], ShouldEqual, 1)

		filtered, err := jq.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact,
			[]JobState{JobStateSuspended}, false, false)
		So(err, ShouldBeNil)
		So(filtered[repGroup].Counts[JobStateSuspended], ShouldEqual, 1)
		So(filtered[repGroup].Counts[JobStateReady], ShouldEqual, 0)
		So(filtered[repGroup].Counts[JobStateComplete], ShouldEqual, 0)
	})
}

func assertLazyStatusSeedMissingBucket(db *db, repGroup string, bucket []byte) {
	err := db.bolt.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket(bucket)
	})
	So(err, ShouldBeNil)

	_, err = db.retrieveCompleteJobCountsByRepGroups([]string{repGroup})
	assertBoltBucketError(err, bucket)

	server := &Server{db: db, statusState: newStatusState()}
	err = server.seedStatusStateForItemDefs([]*queue.ItemDef{{
		Data: &Job{RepGroup: repGroup},
	}})
	assertJobqueueDBBucketError(err, bucket)
}

func assertRecoveryStatusSeedMissingBucket(db *db, repGroup string, bucket []byte) {
	err := db.bolt.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket(bucket)
	})
	So(err, ShouldBeNil)

	server := &Server{db: db, statusState: newStatusState()}
	err = server.recoverPriorJobs(context.Background(), ServerConfig{}, []*Job{{
		Cmd: "echo recover status", RepGroup: repGroup,
	}})
	assertJobqueueDBBucketError(err, bucket)
}

func assertJobqueueDBBucketError(err error, bucket []byte) {
	var jqErr Error

	So(errors.As(err, &jqErr), ShouldBeTrue)
	So(jqErr.Err, ShouldEqual, ErrDBError)
	assertBoltBucketError(err, bucket)
}

func assertBoltBucketError(err error, bucket []byte) {
	So(errors.Is(err, berrors.ErrBucketNotFound), ShouldBeTrue)
	So(err, ShouldNotBeNil)

	if err != nil {
		So(err.Error(), ShouldContainSubstring, string(bucket))
	}
}

func TestAddBulkDependentJobsRequeuesPersistedLiveOrphans(t *testing.T) {
	if runnermode || servermode {
		return
	}

	Convey("Given a few jobs persisted before they reached the live queue", t, func() {
		ctx := context.Background()
		_, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(false)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		const (
			totalJobs    = 15000
			orphanJobs   = 5
			repGroup     = "bigmod"
			missingGroup = "neverappears"
		)

		jobs := makeBulkDependentStatusCountJobs(totalJobs, repGroup, missingGroup, standardReqs)
		_, _, _, err = server.db.storeNewJobs(ctx, jobs[:orphanJobs], true)
		So(err, ShouldBeNil)

		if clientConnectTime < 30*time.Second {
			clientConnectTime = 30 * time.Second
		}

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		added, existed, err := jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, totalJobs)
		So(existed, ShouldEqual, 0)
		assertRepGroupDependentCount(jq, repGroup, totalJobs)

		added, existed, err = jq.Add(jobs, envVars, true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, 0)
		So(existed, ShouldEqual, totalJobs)
		assertRepGroupDependentCount(jq, repGroup, totalJobs)
	})
}

func makeBulkDependentStatusCountJobs(
	total int,
	repGroup string,
	missingGroup string,
	reqs *jqs.Requirements,
) []*Job {
	jobs := make([]*Job, 0, total)

	for i := range total {
		jobs = append(jobs, &Job{
			Cmd:          fmt.Sprintf("echo %d", i+1),
			Cwd:          testCwd,
			ReqGroup:     reqGroupFake,
			Requirements: reqs,
			RepGroup:     repGroup,
			Dependencies: Dependencies{NewDepGroupDependency(missingGroup)},
		})
	}

	return jobs
}

func assertRepGroupDependentCount(jq *Client, repGroup string, expected int) {
	summaries, err := jq.GetStatusByRepGroupMatch(repGroup, RepGroupMatchExact, nil, false, false)
	So(err, ShouldBeNil)
	So(summaries[repGroup].Counts[JobStateDependent], ShouldEqual, expected)
}
