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

// Untagged behavioural regression tests for reliable4 FINDING 1: a control
// request that can only ever act on LIVE jobs must not make the manager
// cursor-scan and codec-decode the entire archived history of every matching
// RepGroup. On the production DB two such requests (from `wr resume -i portal
// -z`) ran CPU-bound for 12+ minutes after the client had already given up, took
// the manager heap from 348MB to 12,143MB, and left the operator with no way to
// un-suspend the queue at all.
//
// Two independent defects composed to cause it, and both are pinned here:
//
//  1. the CLI (cmd/suspend.go getSelectedJobs) only sent a state filter with -a,
//     so `wr suspend -i <rg>` / `wr resume -i <rg>` asked for every state; and
//  2. getJobsByRepGroup fetched complete jobs "whenever State happens to be
//     empty", so any caller that simply did not set a state - the REST job
//     modification target among them - paid for the whole history.
//
// The cost driver is decodeArchivedJob, counted per-server by db.archivedDecodes
// (INERT observability), so these tests assert on decode counts rather than on
// wall-clock, which is not trustworthy on a shared farm node. The companion
// assertions guard the opposite direction: `wr status` and the REST status
// endpoint must still return archived jobs.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	historyScanRepGroup      = "reliable4-history-scan-main"
	historyScanOtherRepGroup = "reliable4-history-scan-other"
	historyScanSubStr        = "reliable4-history-scan"
	historyScanReqGroup      = "reliable4-history-scan"

	// historyScanArchived is the per-RepGroup archived history size. It is big
	// enough that the pre-fix decode count is unmistakable (and its cost visible in
	// the logged timings) while staying fast to seed, since the history is
	// bulk-inserted rather than run through the queue.
	historyScanArchived = 5000

	// historyScanLive is how many live jobs each control request must find, and is
	// deliberately tiny next to historyScanArchived: the whole point is that the
	// request's cost tracks the live count, not the history size.
	historyScanLive = 3

	// the two history sizes whose 10x difference must not show up in the cost of a
	// suspend/resume-shaped request, and the bounds that judge its latency: the
	// floor and slack absorb the RPC round trip and a loaded machine's jitter,
	// while still rejecting a history-proportional request.
	historyScanSmallHistory = 500
	historyScanLargeHistory = 5000
	historyScanScaleLimit   = 4
	historyScanElapsedFloor = 5 * time.Millisecond
	historyScanElapsedSlack = 25 * time.Millisecond
	historyScanTimingRuns   = 3
)

// TestReliable4ControlPathsSkipArchivedHistory pins that the request shapes
// `wr suspend`/`wr resume` produce decode zero archived jobs, that the REST job
// modification path (which never wanted history either) does the same, and that
// the paths that legitimately want history still get it.
func TestReliable4ControlPathsSkipArchivedHistory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager whose report groups' archived history dwarfs their live jobs", t, func() {
		config, serverConfig, addr, reqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true
		seedArchivedRepGroupHistory(ctx, serverConfig, historyScanArchived,
			historyScanRepGroup, historyScanOtherRepGroup)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		defer disconnect(jq)

		addHistoryScanLiveJobs(jq, reqs, historyScanRepGroup)

		decodes := func() uint64 {
			return server.db.archivedDecodes.Load()
		}

		Convey("the live-only request shape suspend/resume send costs no archived decode", func() {
			liveBefore := decodes()
			liveStart := time.Now()
			liveJobs, errl := jq.GetByRepGroup(historyScanRepGroup, false, 0, JobStateIncomplete, false, false)
			liveElapsed := time.Since(liveStart)
			liveDecodes := decodes() - liveBefore

			statusBefore := decodes()
			statusStart := time.Now()
			statusJobs, errs := jq.GetByRepGroup(historyScanRepGroup, false, 0, "", false, false)
			statusElapsed := time.Since(statusStart)
			statusDecodes := decodes() - statusBefore

			t.Logf("live-only shape: %d jobs, %d archived decodes in %s; "+
				"status shape: %d jobs, %d archived decodes in %s",
				len(liveJobs), liveDecodes, liveElapsed, len(statusJobs), statusDecodes, statusElapsed)

			So(errl, ShouldBeNil)
			So(errs, ShouldBeNil)

			// the fix: the live-only shape returns every live job and decodes nothing.
			So(liveDecodes, ShouldEqual, 0)
			So(len(liveJobs), ShouldEqual, historyScanLive)

			// the guard against fixing this in the wrong direction: the status shape
			// still returns (and so still decodes) the whole history.
			So(statusDecodes, ShouldEqual, historyScanArchived)
			So(len(statusJobs), ShouldEqual, historyScanArchived+historyScanLive)

			// cost tracks the live count, not the history size.
			So(liveElapsed, ShouldBeLessThan, statusElapsed)
		})

		Convey("the -z substring shape costs no archived decode either", func() {
			liveBefore := decodes()
			liveJobs, errl := jq.GetByRepGroup(historyScanSubStr, true, 0, JobStateIncomplete, false, false)
			liveDecodes := decodes() - liveBefore

			statusBefore := decodes()
			statusJobs, errs := jq.GetByRepGroup(historyScanSubStr, true, 0, "", false, false)
			statusDecodes := decodes() - statusBefore

			t.Logf("substring live-only shape: %d jobs, %d archived decodes; "+
				"substring status shape: %d jobs, %d archived decodes",
				len(liveJobs), liveDecodes, len(statusJobs), statusDecodes)

			So(errl, ShouldBeNil)
			So(errs, ShouldBeNil)
			So(liveDecodes, ShouldEqual, 0)
			So(len(liveJobs), ShouldEqual, historyScanLive)
			So(statusDecodes, ShouldEqual, 2*historyScanArchived)
			So(len(statusJobs), ShouldEqual, 2*historyScanArchived+historyScanLive)
		})

		Convey("an explicit non-complete state filter still costs no archived decode", func() {
			before := decodes()
			jobs, errg := jq.GetByRepGroup(historyScanRepGroup, false, 0, JobStateReady, false, false)
			So(errg, ShouldBeNil)
			So(decodes()-before, ShouldEqual, 0)
			So(len(jobs), ShouldEqual, historyScanLive)
		})

		Convey("an explicit complete state filter still returns the history", func() {
			before := decodes()
			jobs, errg := jq.GetByRepGroup(historyScanRepGroup, false, 0, JobStateComplete, false, false)
			So(errg, ShouldBeNil)
			So(decodes()-before, ShouldEqual, historyScanArchived)
			So(len(jobs), ShouldEqual, historyScanArchived)
		})

		Convey("suspending and resuming the live jobs by report group works and stays history-free", func() {
			before := decodes()

			selected, errg := jq.GetByRepGroup(historyScanRepGroup, false, 0, JobStateIncomplete, false, false)
			So(errg, ShouldBeNil)
			So(len(selected), ShouldEqual, historyScanLive)

			essences := make([]*JobEssence, 0, len(selected))
			for _, job := range selected {
				essences = append(essences, job.ToEssense())
			}

			suspended, errs := jq.Suspend(essences)
			So(errs, ShouldBeNil)
			So(suspended, ShouldEqual, historyScanLive)

			resumeSelected, errg2 := jq.GetByRepGroup(historyScanRepGroup, false, 0, JobStateIncomplete, false, false)
			So(errg2, ShouldBeNil)
			So(len(resumeSelected), ShouldEqual, historyScanLive)

			resumeEssences := make([]*JobEssence, 0, len(resumeSelected))
			for _, job := range resumeSelected {
				resumeEssences = append(resumeEssences, job.ToEssense())
			}

			resumed, errr := jq.Resume(resumeEssences)
			So(errr, ShouldBeNil)
			So(resumed, ShouldEqual, historyScanLive)

			So(decodes()-before, ShouldEqual, 0)
		})

		Convey("the REST job modification path costs no archived decode", func() {
			handler := restJobs(ctx, server)
			bearer := "Bearer " + string(token)

			before := decodes()
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodPatch,
				restJobsEndpoint+historyScanRepGroup, strings.NewReader(`{"priority":9}`))
			r.Header.Set("Authorization", bearer)
			r.Header.Set("Content-Type", "application/json")
			handler(w, r)

			resp := w.Result()
			defer resp.Body.Close()

			var modified JobModifyResponse
			So(json.NewDecoder(resp.Body).Decode(&modified), ShouldBeNil)
			So(resp.StatusCode, ShouldEqual, http.StatusOK)
			So(len(modified.Modified), ShouldEqual, historyScanLive)
			So(decodes()-before, ShouldEqual, 0)

			Convey("while the REST status path still returns the archived jobs", func() {
				statusBefore := decodes()
				sw := httptest.NewRecorder()
				sr := httptest.NewRequestWithContext(ctx, http.MethodGet,
					restJobsEndpoint+historyScanRepGroup, nil)
				sr.Header.Set("Authorization", bearer)
				handler(sw, sr)

				statusResp := sw.Result()
				defer statusResp.Body.Close()

				var statuses []JStatus
				So(json.NewDecoder(statusResp.Body).Decode(&statuses), ShouldBeNil)
				So(statusResp.StatusCode, ShouldEqual, http.StatusOK)
				So(len(statuses), ShouldEqual, historyScanArchived+historyScanLive)
				So(decodes()-statusBefore, ShouldEqual, historyScanArchived)
			})
		})

		Convey("a REST modification of a history-only report group conflicts without a scan", func() {
			// historyScanOtherRepGroup has archived jobs but no live ones. The spec
			// (.docs/issue-197/spec.md) says a target resolving only to non-editable
			// states is 409, and reserves 404 for a target resolving to no queued or
			// complete job and no RepGroup - so this must stay 409 even though the
			// modification path no longer fetches the history to find that out (it asks
			// bucketRGEndTime, which archiveJob always writes, instead).
			handler := restJobs(ctx, server)

			before := decodes()
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodPatch,
				restJobsEndpoint+historyScanOtherRepGroup, strings.NewReader(`{"priority":9}`))
			r.Header.Set("Authorization", "Bearer "+string(token))
			r.Header.Set("Content-Type", "application/json")
			handler(w, r)

			resp := w.Result()
			defer resp.Body.Close()

			body, errb := io.ReadAll(resp.Body)
			So(errb, ShouldBeNil)
			So(resp.StatusCode, ShouldEqual, http.StatusConflict)
			So(string(body), ShouldEqual, errRESTModifyNoEditable.Error()+"\n")
			So(decodes()-before, ShouldEqual, 0)

			Convey("while a report group with no jobs at all is still not found", func() {
				unknownBefore := decodes()
				uw := httptest.NewRecorder()
				ur := httptest.NewRequestWithContext(ctx, http.MethodPatch,
					restJobsEndpoint+"reliable4-history-scan-never-existed", strings.NewReader(`{"priority":9}`))
				ur.Header.Set("Authorization", "Bearer "+string(token))
				ur.Header.Set("Content-Type", "application/json")
				handler(uw, ur)

				unknownResp := uw.Result()
				defer unknownResp.Body.Close()

				unknownBody, erru := io.ReadAll(unknownResp.Body)
				So(erru, ShouldBeNil)
				So(unknownResp.StatusCode, ShouldEqual, http.StatusNotFound)
				So(string(unknownBody), ShouldEqual, errRESTModifyNotFound.Error()+"\n")
				So(decodes()-unknownBefore, ShouldEqual, 0)
			})
		})
	})
}

// TestReliable4ControlPathCostDoesNotScaleWithHistory pins the other half of the
// FINDING 1 invariant: the cost of a suspend/resume-shaped request must track the
// LIVE job count, not the archived history size. It compares a 10x history
// difference; pre-fix both the decode count and the latency were proportional to
// the history size.
func TestReliable4ControlPathCostDoesNotScaleWithHistory(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A suspend/resume-shaped request costs the same on a 10x bigger history", t, func() {
		smallDecodes, smallElapsed := measureHistoryScanRequest(ctx, t, historyScanSmallHistory)
		largeDecodes, largeElapsed := measureHistoryScanRequest(ctx, t, historyScanLargeHistory)

		t.Logf("live-only shape with %d archived: %d decodes, %s; with %d archived: %d decodes, %s",
			historyScanSmallHistory, smallDecodes, smallElapsed,
			historyScanLargeHistory, largeDecodes, largeElapsed)

		// the deterministic invariant: no archived job is decoded at either size.
		So(smallDecodes, ShouldEqual, 0)
		So(largeDecodes, ShouldEqual, 0)

		// and the latency it drove does not scale either. The floor and the slack
		// absorb the RPC round trip and this being a shared, loaded machine, while
		// still failing a history-proportional request (which at these sizes was
		// ~9ms vs ~90ms).
		limit := historyScanScaleLimit*max(smallElapsed, historyScanElapsedFloor) + historyScanElapsedSlack
		So(largeElapsed, ShouldBeLessThan, limit)
	})
}

// measureHistoryScanRequest starts a manager whose DB holds the given number of
// archived jobs in one RepGroup plus historyScanLive live jobs in it, then issues the
// request shape `wr suspend -i <rg>` / `wr resume -i <rg>` produce, returning how
// many archived jobs the manager decoded for it and the best of
// historyScanTimingRuns latencies (the best, so a scheduling hiccup on a loaded
// machine cannot masquerade as a scan).
func measureHistoryScanRequest(ctx context.Context, t *testing.T, archived int) (uint64, time.Duration) {
	t.Helper()

	config, serverConfig, addr, reqs, clientConnectTime := jobqueueTestInit(true)
	serverConfig.dontWipeDevDB = true
	seedArchivedRepGroupHistory(ctx, serverConfig, archived, historyScanRepGroup)

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	So(waitUntilRecovered(server), ShouldBeTrue)

	jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
	So(errc, ShouldBeNil)

	defer disconnect(jq)

	addHistoryScanLiveJobs(jq, reqs, historyScanRepGroup)

	before := server.db.archivedDecodes.Load()
	best := time.Duration(0)

	for run := range historyScanTimingRuns {
		start := time.Now()
		jobs, errg := jq.GetByRepGroup(historyScanRepGroup, false, 0, JobStateIncomplete, false, false)
		elapsed := time.Since(start)

		So(errg, ShouldBeNil)
		So(len(jobs), ShouldEqual, historyScanLive)

		if run == 0 || elapsed < best {
			best = elapsed
		}
	}

	return server.db.archivedDecodes.Load() - before, best
}

// seedArchivedRepGroupHistory creates config's DB pre-populated with count
// archived jobs in each of the given RepGroups. The entries are bulk-inserted
// (complete bucket + RTK lookup index + rep-groups bucket + the RepGroup end
// time, exactly what archiveJobTx writes and what retrieveCompleteJobsByRepGroup
// and repGroupHasHistory read) so that seeding thousands of them is fast.
func seedArchivedRepGroupHistory(ctx context.Context, config ServerConfig, count int, repGroups ...string) {
	testDB, _, err := initDB(ctx, config.DBFile, config.DBFileBackup, internal.Development, false, false)
	So(err, ShouldBeNil)

	endTime := time.Now()
	archived := testDBArchivedJob("echo history scan archived", repGroups[0], endTime)

	var encoded []byte
	So(codec.NewEncoderBytes(&encoded, testDB.ch).Encode(archived), ShouldBeNil)

	err = testDB.bolt.Update(func(tx *bolt.Tx) error {
		completeBucket := tx.Bucket(bucketJobsComplete)
		lookupBucket := tx.Bucket(bucketRTK)
		repGroupsBucket := tx.Bucket(bucketRGs)
		endTimeBucket := tx.Bucket(bucketRGEndTime)

		for _, repGroup := range repGroups {
			if errp := repGroupsBucket.Put([]byte(repGroup), nil); errp != nil {
				return errp
			}

			if errp := updateRGEndTime(endTimeBucket, &Job{RepGroup: repGroup, EndTime: endTime}); errp != nil {
				return errp
			}

			for i := range count {
				key := []byte(repGroup + "-key-" + strconv.Itoa(i))

				if errp := completeBucket.Put(key, encoded); errp != nil {
					return errp
				}

				if errp := lookupBucket.Put(testDB.generateLookupKey(repGroup, key), nil); errp != nil {
					return errp
				}
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
	So(testDB.close(ctx), ShouldBeNil)
}

// addHistoryScanLiveJobs adds historyScanLive ready jobs in repGroup.
func addHistoryScanLiveJobs(jq *Client, reqs *jqs.Requirements, repGroup string) {
	live := make([]*Job, 0, historyScanLive)
	for i := range historyScanLive {
		live = append(live, &Job{
			Cmd:          "echo history scan live " + strconv.Itoa(i),
			Cwd:          testCwd,
			ReqGroup:     historyScanReqGroup,
			Requirements: reqs,
			RepGroup:     repGroup,
		})
	}

	added, existed, err := jq.Add(live, envVars, true)
	So(err, ShouldBeNil)
	So(added, ShouldEqual, historyScanLive)
	So(existed, ShouldEqual, 0)
}
