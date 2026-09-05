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

// Untagged behavioural regression tests for reliable4 ITEM B: `wr status -i
// <substr> -z -l 1` must not decode a RepGroup's entire archived history before
// throwing all but one of the decoded jobs away. Production's history is ~2.15M
// complete records, and decoding it took the manager's heap from 0.35GB to
// 12.1GB - an excursion that FINDING 1's fix (5c75a15) closed for the control
// paths but left wide open for the one caller that legitimately wants history.
//
// Two things are pinned here, and they matter equally:
//
//  1. the number of FULL archived decodes (db.archivedDecodes, the INERT counter
//     5c75a15 added) is O(limit), not O(history), across the WHOLE multi-RepGroup
//     -z loop rather than per RepGroup; and
//  2. the jobs `wr status` returns are byte-for-byte what it returned before -
//     the same jobs, in the same order, carrying the same Similar counts - since
//     this is a "stop materialising what you were always going to discard" fix
//     and not a semantics change.
//
// The archived history is seeded so that the RTK cursor order is the REVERSE of
// the start-time order limitJobs presents jobs in: a pushdown that simply stopped
// at the first `limit` records the cursor produced would return the newest jobs
// instead of the oldest, and every ordering assertion below would fail.
//
// Every block asserts what the request RETURNED before it asserts how much it
// cost, because GoConvey halts a block at its first failure: with that order the
// unchanged-answer assertions are the ones that run (and pass) on the pre-fix
// tree, so they are a genuine record of the old behaviour and not just of the
// new.

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/internal"
	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

const (
	statusLimitSubStr   = "reliable4-status-limit"
	statusLimitRepGroup = statusLimitSubStr + "-a"
	statusLimitOtherRG  = statusLimitSubStr + "-b"
	statusLimitReqGroup = "reliable4-status-limit"

	// statusLimitArchived and statusLimitOtherArchived are the two RepGroups'
	// archived history sizes. They differ so that a count taken from the wrong
	// RepGroup cannot pass, and they are big enough that an O(history) decode is
	// unmistakable next to an O(limit) one.
	statusLimitArchived      = 5000
	statusLimitOtherArchived = 3000
	statusLimitTotal         = statusLimitArchived + statusLimitOtherArchived

	// statusLimitLive is how many live jobs the mixed-state case adds; they land
	// in their own limitJobs group, so they must not disturb the complete one.
	statusLimitLive = 3
)

// TestReliable4StatusLimitPushdown pins that a limited `wr status` request decodes
// O(limit) archived jobs rather than the whole history, and returns exactly what
// it returned when it decoded all of them.
func TestReliable4StatusLimitPushdown(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager with a large archived history in two report groups", t, func() {
		config, serverConfig, addr, reqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true
		seedStatusLimitHistory(ctx, serverConfig)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		decodes := func() uint64 {
			return server.db.archivedDecodes.Load()
		}

		// the reference: the unlimited fetch this fix deliberately leaves alone,
		// which is where "what wr status returned before" comes from. With no live
		// jobs it is exactly the RepGroup's archived jobs, oldest-started first.
		reference, refSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
			RepGroupMatchExact, limitJobsOptions{}))
		So(refSrerr, ShouldBeEmpty)
		So(len(reference), ShouldEqual, statusLimitArchived)
		So(reference[0].Cmd, ShouldEqual, statusLimitCmd(statusLimitRepGroup, statusLimitArchived-1))

		zReference, zSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitSubStr,
			RepGroupMatchSubStr, limitJobsOptions{}))
		So(zSrerr, ShouldBeEmpty)
		So(len(zReference), ShouldEqual, statusLimitTotal)

		Convey("a limit of 1 on one report group decodes 1 archived job, not all of them", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1}))
			used := decodes() - before

			t.Logf("limit 1 over %d archived: %d jobs, %d archived decodes", statusLimitArchived, len(jobs), used)

			So(srerr, ShouldBeEmpty)

			// the answer is unchanged: the same single job the unlimited fetch puts
			// first, standing in for all the others.
			So(len(jobs), ShouldEqual, 1)
			So(statusLimitIdentity(jobs[0]), ShouldEqual, statusLimitIdentityWithSimilar(reference[0],
				statusLimitArchived-1))

			// and the fix: the work is bounded by the limit, not by the history.
			So(used, ShouldEqual, 1)
		})

		Convey("a limit of 1 with an offset decodes offset+1 archived jobs and pages identically", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1, Offset: 3}))
			used := decodes() - before

			t.Logf("limit 1 offset 3 over %d archived: %d jobs, %d archived decodes",
				statusLimitArchived, len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, 1)

			// the group keeps its first offset+limit members and counts the rest on
			// the last of them, then the offset drops the first three: the answer is
			// the 4th oldest job, carrying the 4996 it stands in for.
			So(statusLimitIdentity(jobs[0]), ShouldEqual, statusLimitIdentityWithSimilar(reference[3],
				statusLimitArchived-4))
			So(used, ShouldEqual, 4)
		})

		Convey("a limit of 2 with an offset of 1 pages identically", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 2, Offset: 1}))
			used := decodes() - before

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, 2)
			So(statusLimitIdentity(jobs[0]), ShouldEqual, statusLimitIdentityWithSimilar(reference[1], 0))
			So(statusLimitIdentity(jobs[1]), ShouldEqual, statusLimitIdentityWithSimilar(reference[2],
				statusLimitArchived-3))
			So(used, ShouldEqual, 3)
		})

		Convey("the -z substring shape spends its limit ACROSS report groups, not per group", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitSubStr,
				RepGroupMatchSubStr, limitJobsOptions{Limit: 1}))
			used := decodes() - before

			t.Logf("-z limit 1 over %d archived in 2 report groups: %d jobs, %d archived decodes",
				statusLimitTotal, len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, 1)
			So(statusLimitIdentity(jobs[0]), ShouldEqual, statusLimitIdentityWithSimilar(zReference[0],
				statusLimitTotal-1))

			// 1, not 2: a budget applied per RepGroup would decode one from each.
			So(used, ShouldEqual, 1)
		})

		Convey("a -z limit that spans the report groups returns and counts both", func() {
			limit := statusLimitArchived + 1

			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitSubStr,
				RepGroupMatchSubStr, limitJobsOptions{Limit: limit}))
			used := decodes() - before

			t.Logf("-z limit %d over %d archived in 2 report groups: %d jobs, %d archived decodes",
				limit, statusLimitTotal, len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, limit)
			So(statusLimitIdentity(jobs[0]), ShouldEqual, statusLimitIdentityWithSimilar(zReference[0], 0))
			So(statusLimitIdentity(jobs[limit-1]), ShouldEqual,
				statusLimitIdentityWithSimilar(zReference[limit-1], statusLimitTotal-limit))

			// the whole of the first group plus one job from the second, and nothing
			// more: a per-RepGroup budget would have decoded all 8000.
			So(used, ShouldEqual, limit)
		})

		Convey("the whole `wr status -i <substr> -z -l 1` client request costs one decode", func() {
			jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			before := decodes()
			jobs, errg := jq.GetByRepGroup(statusLimitSubStr, true, 1, "", false, false)
			used := decodes() - before

			t.Logf("client -z limit 1: %d jobs, %d archived decodes", len(jobs), used)

			So(errg, ShouldBeNil)
			So(len(jobs), ShouldEqual, 1)
			So(jobs[0].Cmd, ShouldEqual, statusLimitCmd(statusLimitRepGroup, statusLimitArchived-1))
			So(jobs[0].Similar, ShouldEqual, statusLimitTotal-1)
			So(used, ShouldEqual, 1)
		})

		Convey("live jobs in the same report group get their own group and their own limit", func() {
			jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			addStatusLimitLiveJobs(jq, reqs)

			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1}))
			used := decodes() - before

			t.Logf("limit 1 with %d live jobs: %d jobs, %d archived decodes", statusLimitLive, len(jobs), used)

			So(srerr, ShouldBeEmpty)

			// one representative of each group: the archived jobs and the ready ones.
			So(len(jobs), ShouldEqual, 2)

			complete, ready := statusLimitSplitByState(jobs)
			So(len(complete), ShouldEqual, 1)
			So(len(ready), ShouldEqual, 1)
			So(statusLimitIdentity(complete[0]), ShouldEqual, statusLimitIdentityWithSimilar(reference[0],
				statusLimitArchived-1))
			So(ready[0].Similar, ShouldEqual, statusLimitLive-1)
			So(used, ShouldEqual, 1)
		})

		Convey("an unlimited request still decodes and returns the whole history", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{}))
			used := decodes() - before

			So(srerr, ShouldBeEmpty)
			So(used, ShouldEqual, statusLimitArchived)
			So(len(jobs), ShouldEqual, statusLimitArchived)
			So(statusLimitIdentity(jobs[0]), ShouldEqual, statusLimitIdentity(reference[0]))
			So(statusLimitIdentity(jobs[len(jobs)-1]), ShouldEqual, statusLimitIdentity(reference[len(reference)-1]))
		})

		Convey("a fail-reason filter is pushed down as well, so an unmatchable one costs nothing", func() {
			// matchesFailureFilter reads only FailReason and Exitcode, and both are
			// facets, so archivedJobGrouper can decide it per record exactly as
			// jobMatchesFilters does. markJobComplete clears FailReason, so none of
			// these archived records can match - which is why the pre-pass discards
			// all 5000 of them without decoding one, rather than why it must not try.
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1, FailReason: FailReasonExit, ExitCode: 1}))
			used := decodes() - before

			t.Logf("fail-reason filter over %d archived: %d jobs, %d archived decodes",
				statusLimitArchived, len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, 0)
			So(used, ShouldEqual, 0)
		})

		Convey("a waiting-for-dep-groups filter is NOT decidable from the facets, so it is refused", func() {
			// matchesWaitingForDepGroupsFilter reads Job.WaitingForDepGroups, a
			// variable-length []string archivedJobFacets deliberately does not carry
			// (decoding one per record is the allocation this fix exists to remove),
			// and nothing clears it when a job is archived - so it cannot be answered
			// from the facets NOR assumed away, and the limit must not be pushed down.
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1, WaitingForDepGroups: true}))
			used := decodes() - before

			t.Logf("waiting-for-dep-groups filter over %d archived: %d jobs, %d archived decodes",
				statusLimitArchived, len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, 0)
			So(used, ShouldEqual, statusLimitArchived)
		})

		Convey("a state filter that cannot match an archived job still costs no decode", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1, State: JobStateIncomplete}))
			So(srerr, ShouldBeEmpty)
			So(decodes()-before, ShouldEqual, 0)
			So(len(jobs), ShouldEqual, 0)
		})
	})
}

// seedStatusLimitHistory creates config's DB pre-populated with the two report
// groups' archived histories, bulk-inserted into the buckets archiveJobTx writes
// and retrieveCompleteJobsByRepGroup reads.
//
// The ith job of a group is keyed so that it sorts ith in the RTK cursor, but is
// given the (count-i)th start time, so cursor order is the exact REVERSE of the
// start-time order limitJobs uses. A pushdown that took the cursor's first
// `limit` records rather than the oldest-started ones therefore returns visibly
// wrong jobs.
func seedStatusLimitHistory(ctx context.Context, config ServerConfig) {
	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		for _, group := range []struct {
			repGroup string
			count    int
		}{{statusLimitRepGroup, statusLimitArchived}, {statusLimitOtherRG, statusLimitOtherArchived}} {
			if errs := seedStatusLimitRepGroup(tx, testDB, group.repGroup, group.count, base); errs != nil {
				return errs
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)
}

func seedStatusLimitRepGroup(tx *bolt.Tx, testDB *db, repGroup string, count int, base time.Time) error {
	completeBucket := tx.Bucket(bucketJobsComplete)
	lookupBucket := tx.Bucket(bucketRTK)

	if err := tx.Bucket(bucketRGs).Put([]byte(repGroup), nil); err != nil {
		return err
	}

	if err := updateRGEndTime(tx.Bucket(bucketRGEndTime), &Job{RepGroup: repGroup, EndTime: base}); err != nil {
		return err
	}

	for i := range count {
		var encoded []byte
		if err := codec.NewEncoderBytes(&encoded, testDB.ch).
			Encode(statusLimitArchivedJob(repGroup, i, count, base)); err != nil {
			return err
		}

		key := fmt.Appendf(nil, "%s-key-%06d", repGroup, i)

		if err := completeBucket.Put(key, encoded); err != nil {
			return err
		}

		if err := lookupBucket.Put(testDB.generateLookupKey(repGroup, key), nil); err != nil {
			return err
		}
	}

	return nil
}

// statusLimitArchivedJob returns the ith of count archived jobs for repGroup,
// started (count-i) minutes after base so that key order reverses time order, and
// ended (count+i) minutes after it.
//
// That makes each job's DURATION vary with i, so that end-time order is the exact
// reverse of start-time order. It has to be: startedBefore falls back to EndTime
// when two jobs share a StartTime, so if every record had the same duration the
// two orders would agree, and a bounded fetch that read no StartTime at all would
// still come back with the right jobs off that tie-break alone. The codec has
// ErrorIfNoField false, so a renamed StartTime facet decodes silently as the zero
// time - which is precisely the "every record shares a StartTime" case. With the
// orders opposed, that mutation returns the newest jobs where the oldest are
// expected, and the identity assertions above catch it.
func statusLimitArchivedJob(repGroup string, i, count int, base time.Time) *Job {
	start := base.Add(time.Duration(count-i) * time.Minute)
	end := base.Add(time.Duration(count+i)*time.Minute + time.Second)

	return &Job{
		Cmd:          statusLimitCmd(repGroup, i),
		Cwd:          testCwd,
		ReqGroup:     statusLimitReqGroup,
		RepGroup:     repGroup,
		Requirements: &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1},
		State:        JobStateComplete,
		Exited:       true,
		Exitcode:     0,
		StartTime:    start,
		EndTime:      end,
		PeakRAM:      10,
		PeakDisk:     1,
		CPUtime:      time.Second,
	}
}

// statusLimitOpts returns the repGroupOptions the `wr status` request shapes
// produce for the given report group selector and limits.
func statusLimitOpts(repGroup string, match RepGroupMatch, limits limitJobsOptions) repGroupOptions {
	return repGroupOptions{
		RepGroup:         repGroup,
		Match:            match,
		IncludeComplete:  true,
		limitJobsOptions: limits,
	}
}

// statusLimitCmd returns the Cmd of the ith seeded archived job of repGroup.
func statusLimitCmd(repGroup string, i int) string {
	return "echo " + repGroup + " " + strconv.Itoa(i)
}

// statusLimitIdentity renders the fields of job that `wr status` shows, so two
// jobs can be compared for equality in one assertion whose failure message names
// what actually differs.
func statusLimitIdentity(job *Job) string {
	return statusLimitIdentityWithSimilar(job, job.Similar)
}

// statusLimitIdentityWithSimilar renders job's identity as if it carried the
// given Similar count, so an unlimited-fetch job can be used as the expected
// value for a limited fetch's representative.
func statusLimitIdentityWithSimilar(job *Job, similar int) string {
	return fmt.Sprintf("cmd=%s repgroup=%s state=%s exitcode=%d failreason=%s start=%s end=%s similar=%d",
		job.Cmd, job.RepGroup, job.State, job.Exitcode, job.FailReason,
		job.StartTime.UTC().Format(time.RFC3339Nano), job.EndTime.UTC().Format(time.RFC3339Nano), similar)
}

// addStatusLimitLiveJobs adds statusLimitLive ready jobs to the main report
// group.
func addStatusLimitLiveJobs(jq *Client, reqs *jqs.Requirements) {
	live := make([]*Job, 0, statusLimitLive)
	for i := range statusLimitLive {
		live = append(live, &Job{
			Cmd:          "echo status limit live " + strconv.Itoa(i),
			Cwd:          testCwd,
			ReqGroup:     statusLimitReqGroup,
			Requirements: reqs,
			RepGroup:     statusLimitRepGroup,
		})
	}

	added, existed, err := jq.Add(live, envVars, true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, statusLimitLive)
	So(existed, ShouldEqual, 0)
}

// statusLimitSplitByState splits jobs into the complete ones and the ready ones.
func statusLimitSplitByState(jobs []*Job) (complete, ready []*Job) {
	for _, job := range jobs {
		if job.State == JobStateComplete {
			complete = append(complete, job)
		} else {
			ready = append(ready, job)
		}
	}

	return complete, ready
}

// TestReliable4StatusLimitMixedHistory pins the pushdown's other half: it must not
// assume that a RepGroup's archived records are all interchangeable. limitJobs'
// limit is per GROUP, so a history holding records of more than one group needs a
// budget per group (a single shared one would drop whole groups from the answer),
// and records the state filter discards must be neither decoded nor counted.
//
// The mixed shapes seeded here - an archived record with a non-zero exit code, one
// with a buried state, one whose stored RepGroup is a different group it was later
// re-run under - are deliberately shapes today's markJobComplete cannot produce.
// They are the invariant the pushdown would silently depend on if it assumed one
// group, which is exactly why they are seeded rather than assumed away.
func TestReliable4StatusLimitMixedHistory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager whose archived history holds more than one job group", t, func() {
		_, serverConfig, _, _, _ := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true
		seedMixedHistory(ctx, serverConfig)

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		decodes := func() uint64 {
			return server.db.archivedDecodes.Load()
		}

		reference, refSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(mixedRepGroup,
			RepGroupMatchExact, limitJobsOptions{}))
		So(refSrerr, ShouldBeEmpty)
		So(len(reference), ShouldEqual, mixedTotal)

		Convey("a limit of 1 returns one representative of every group, and counts the rest", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(mixedRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1}))
			used := decodes() - before

			t.Logf("mixed history, limit 1: %d jobs, %d archived decodes", len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, mixedGroups)

			// each group's oldest job, standing in for the rest of ITS group: a single
			// shared budget would have decoded only the oldest job overall and lost
			// every other group entirely.
			So(mixedIdentities(jobs), ShouldEqual, mixedExpectation(reference, map[string]int{
				mixedCmd(mixedOrdinary - 1):                             mixedOrdinary - 1,
				mixedCmd(mixedOrdinary + mixedExited - 1):               mixedExited - 1,
				mixedCmd(mixedOrdinary + mixedExited + mixedBuried - 1): mixedBuried - 1,
				mixedCmd(mixedTotal - 1):                                mixedRerun - 1,
			}))

			// and one decode per group, not one per record.
			So(used, ShouldEqual, mixedGroups)
		})

		Convey("a complete state filter neither returns nor pays for the buried records", func() {
			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(mixedRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1, State: JobStateComplete}))
			used := decodes() - before

			t.Logf("mixed history, limit 1, state complete: %d jobs, %d archived decodes", len(jobs), used)

			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, mixedGroups-1)
			So(mixedIdentities(jobs), ShouldEqual, mixedExpectation(reference, map[string]int{
				mixedCmd(mixedOrdinary - 1):               mixedOrdinary - 1,
				mixedCmd(mixedOrdinary + mixedExited - 1): mixedExited - 1,
				mixedCmd(mixedTotal - 1):                  mixedRerun - 1,
			}))

			// the buried group is not decoded at all: a record the caller's state
			// filter discards must not cost a decode, and must not be counted into
			// anyone's Similar either.
			So(used, ShouldEqual, mixedGroups-1)
		})

		Convey("the web UI's failed-job drill-down is bounded too, and answers identically", func() {
			// sendJobDetails (jobqueue/serverWebI.go) is the ONLY caller that sets a
			// FailReason, and it sets a Limit with it, so before this it was the one
			// remaining way to reach FINDING 1's 12.1GB excursion from the web UI.
			// Here the filter matches two of the four groups, so unlike the
			// unmatchable case it proves the pushdown RETURNS the right jobs, not
			// merely that it returns none of them cheaply.
			drill := func(limit, offset int) limitJobsOptions {
				return limitJobsOptions{
					Limit: limit, Offset: offset, FailReason: mixedFailReason, ExitCode: mixedExitCode,
					GetStd: true, GetEnv: true,
				}
			}

			unbounded, unbSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(mixedRepGroup,
				RepGroupMatchExact, drill(0, 0)))

			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(mixedRepGroup,
				RepGroupMatchExact, drill(1, 0)))
			used := decodes() - before

			pagedBefore := decodes()
			paged, pagedSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(mixedRepGroup,
				RepGroupMatchExact, drill(1, 1)))
			pagedUsed := decodes() - pagedBefore

			t.Logf("failed-job drill-down over %d archived: %d unbounded, %d limited (%d decodes), "+
				"%d at offset 1 (%d decodes)", mixedTotal, len(unbounded), len(jobs), used, len(paged), pagedUsed)

			So(unbSrerr, ShouldBeEmpty)
			So(srerr, ShouldBeEmpty)
			So(pagedSrerr, ShouldBeEmpty)

			// the drill-down's own answer is unchanged: the oldest member of each
			// matching group, standing in for the rest of ITS group.
			So(len(unbounded), ShouldEqual, mixedExited+mixedBuried)
			So(mixedIdentities(jobs), ShouldEqual, mixedExpectation(unbounded, map[string]int{
				mixedCmd(mixedOrdinary + mixedExited - 1):               mixedExited - 1,
				mixedCmd(mixedOrdinary + mixedExited + mixedBuried - 1): mixedBuried - 1,
			}))
			So(mixedIdentities(paged), ShouldEqual, mixedExpectation(unbounded, map[string]int{
				mixedCmd(mixedOrdinary + mixedExited - 2):               mixedExited - 2,
				mixedCmd(mixedOrdinary + mixedExited + mixedBuried - 2): mixedBuried - 2,
			}))

			// and the mixedOrdinary records that cannot match cost nothing at all:
			// one decode per matching group, and offset+limit of them when paging.
			So(used, ShouldEqual, 2)
			So(pagedUsed, ShouldEqual, 4)
		})
	})
}

const (
	mixedRepGroup = "reliable4-status-limit-mixed"

	// the archived record shapes seeded by seedMixedHistory, in key order: ordinary
	// complete ones, complete ones with a non-zero exit code and a fail reason,
	// buried ones, and finally one stored under a different RepGroup because it was
	// re-run. Each shape is its own limitJobs group.
	mixedOrdinary = 200
	mixedExited   = 3
	mixedBuried   = 3
	mixedRerun    = 1
	mixedTotal    = mixedOrdinary + mixedExited + mixedBuried + mixedRerun
	mixedGroups   = 4

	mixedExitCode   = 5
	mixedFailReason = "mixed history fail reason"
	mixedRerunGroup = mixedRepGroup + "-elsewhere"
)

// mixedIdentities renders jobs as one comparable string so that a single
// assertion covers which jobs came back and what each of them stands in for.
// collectJobsFromGroups flattens a map, so the order groups come back in is
// deliberately not asserted: the identities are sorted.
func mixedIdentities(jobs []*Job) string {
	identities := make([]string, 0, len(jobs))
	for _, job := range jobs {
		identities = append(identities, statusLimitIdentity(job))
	}

	sort.Strings(identities)

	return strings.Join(identities, "\n")
}

// mixedExpectation renders the jobs of reference (the unlimited fetch) named by
// wantSimilar's Cmds, each carrying the Similar count it must stand in for, in the
// same form as mixedIdentities.
func mixedExpectation(reference []*Job, wantSimilar map[string]int) string {
	identities := make([]string, 0, len(wantSimilar))

	for _, job := range reference {
		similar, wanted := wantSimilar[job.Cmd]
		if !wanted {
			continue
		}

		identities = append(identities, statusLimitIdentityWithSimilar(job, similar))
	}

	So(len(identities), ShouldEqual, len(wantSimilar))
	sort.Strings(identities)

	return strings.Join(identities, "\n")
}

// mixedCmd returns the Cmd of the ith seeded mixed-history job.
func mixedCmd(i int) string {
	return "echo mixed " + strconv.Itoa(i)
}

// seedMixedHistory creates config's DB pre-populated with mixedRepGroup's
// deliberately heterogeneous archived history, keyed so that cursor order is the
// reverse of start-time order.
func seedMixedHistory(ctx context.Context, config ServerConfig) {
	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		completeBucket := tx.Bucket(bucketJobsComplete)
		lookupBucket := tx.Bucket(bucketRTK)

		if errp := tx.Bucket(bucketRGs).Put([]byte(mixedRepGroup), nil); errp != nil {
			return errp
		}

		if errp := updateRGEndTime(tx.Bucket(bucketRGEndTime),
			&Job{RepGroup: mixedRepGroup, EndTime: base}); errp != nil {
			return errp
		}

		for i := range mixedTotal {
			var encoded []byte
			if errp := codec.NewEncoderBytes(&encoded, testDB.ch).Encode(mixedArchivedJob(i, base)); errp != nil {
				return errp
			}

			key := fmt.Appendf(nil, "%s-key-%06d", mixedRepGroup, i)

			if errp := completeBucket.Put(key, encoded); errp != nil {
				return errp
			}

			if errp := lookupBucket.Put(testDB.generateLookupKey(mixedRepGroup, key), nil); errp != nil {
				return errp
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)
}

// mixedArchivedJob returns the ith of mixedTotal archived jobs, whose shape
// depends on where i falls in the run of each group.
func mixedArchivedJob(i int, base time.Time) *Job {
	job := statusLimitArchivedJob(mixedRepGroup, i, mixedTotal, base)
	job.Cmd = mixedCmd(i)

	switch {
	case i < mixedOrdinary:
	case i < mixedOrdinary+mixedExited:
		job.Exitcode = mixedExitCode
		job.FailReason = mixedFailReason
	case i < mixedOrdinary+mixedExited+mixedBuried:
		job.State = JobStateBuried
		job.Exitcode = mixedExitCode
		job.FailReason = mixedFailReason
	default:
		job.Exitcode = mixedExitCode + 1
		job.RepGroup = mixedRerunGroup
	}

	return job
}

// statusLimitFacets is how many fields archivedJobFacets has. It is asserted so
// that adding a facet without extending statusLimitFacetIdentity - which renders
// them all by hand, and is what proves the codec matched them by NAME - fails
// loudly instead of leaving the new one unpinned.
const statusLimitFacets = 6

// TestReliable4StatusLimitFacets pins archivedJobFacets against Job directly,
// because nothing else can.
//
// The cheap partial decode the pushdown rests on works only because the codec
// encodes a Job as a map keyed by its exported field NAMES, so a struct naming a
// subset of them picks exactly those out and structurally skips the rest. The
// handle has ErrorIfNoField false, so a facet whose name or type drifts from
// Job's does not fail: it silently decodes as a zero value, and the request
// quietly starts answering with the wrong jobs - a zero StartTime, for instance,
// makes every archived record look equally old. A behavioural test only catches
// the facets it happens to order or filter by, so all of them are asserted here.
func TestReliable4StatusLimitFacets(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Every archivedJobFacets field names a Job field of the same type", t, func() {
		facetType := reflect.TypeOf((*archivedJobFacets)(nil)).Elem()
		jobType := reflect.TypeOf((*Job)(nil)).Elem()
		mismatched := make([]string, 0, facetType.NumField())

		for i := range facetType.NumField() {
			facet := facetType.Field(i)

			jobField, exists := jobType.FieldByName(facet.Name)
			if !exists || jobField.Type != facet.Type {
				mismatched = append(mismatched, facet.Name)
			}
		}

		So(strings.Join(mismatched, ","), ShouldBeEmpty)
		So(facetType.NumField(), ShouldEqual, statusLimitFacets)
	})

	Convey("A Job the real codec handle encoded decodes into archivedJobFacets by name", t, func() {
		// the name check above cannot see a change to how the codec MATCHES those
		// names (a StructToArray/toarray handle option, or a codec: tag on a Job
		// field), which would break the facets just as silently, so the real handle
		// is exercised on a Job carrying a distinct non-zero value in every facet.
		tmp := t.TempDir()
		testDB, _, err := initDB(ctx, filepath.Join(tmp, "db"), filepath.Join(tmp, "db.bk"),
			internal.Development, false, false)
		So(err, ShouldBeNil)

		defer func() {
			So(testDB.close(ctx), ShouldBeNil)
		}()

		want := archivedJobFacets{
			StartTime:  time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
			EndTime:    time.Date(2026, 8, 19, 13, 30, 0, 0, time.UTC),
			State:      JobStateBuried,
			Exitcode:   17,
			FailReason: "facets round trip",
			Lost:       true,
		}

		var encoded []byte

		err = codec.NewEncoderBytes(&encoded, testDB.ch).Encode(&Job{
			Cmd: "echo facets", Cwd: testCwd, ReqGroup: statusLimitReqGroup,
			RepGroup: statusLimitRepGroup, Requirements: &jqs.Requirements{RAM: 10, Time: time.Second, Cores: 1},
			Exited: true, StartTime: want.StartTime, EndTime: want.EndTime, State: want.State,
			Exitcode: want.Exitcode, FailReason: want.FailReason, Lost: want.Lost,
		})
		So(err, ShouldBeNil)

		var got archivedJobFacets

		So(codec.NewDecoderBytes(encoded, testDB.ch).Decode(&got), ShouldBeNil)
		So(statusLimitFacetIdentity(got), ShouldEqual, statusLimitFacetIdentity(want))
	})
}

// statusLimitFacetIdentity renders every archivedJobFacets field, so one
// assertion covers them all and names whichever one did not survive a round trip.
func statusLimitFacetIdentity(facets archivedJobFacets) string {
	return fmt.Sprintf("start=%s end=%s state=%s exitcode=%d failreason=%s lost=%t",
		facets.StartTime.UTC().Format(time.RFC3339Nano), facets.EndTime.UTC().Format(time.RFC3339Nano),
		facets.State, facets.Exitcode, facets.FailReason, facets.Lost)
}
