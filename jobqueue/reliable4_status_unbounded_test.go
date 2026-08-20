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

// This file guards the residual `5b90c53` (reliable4 item B) left behind, which
// the 2026-08-20 farm-scale validation gate then measured as still reachable:
// cmd/status.go zeroes the limit for the ungrouped output formats, so
// `wr status -i <rg> -z -o plain` - and any explicit `--limit 0` - still asks for
// every matching archived record and gets the unbounded decode. On only 154,000
// records that cost 6,975 ms and 905 MB of peak RSS; on production's ~2.15M
// complete jobs it extrapolates to ~12.6 GB, i.e. the same heap excursion
// `5c75a15` closed for the control paths, one flag away.
//
// There is no result-preserving bound available for that shape: `-o plain` prints
// one line per job key and `-o summary` accumulates per-job statistics, so
// neither can be served from the limitJobs grouping the way the grouped formats
// are (`counts` folds safely, but it normally takes the decode-free getrs path).
// So the fix is a heap budget on the fetch, and a refusal that names the way out
// when it is exceeded. Everything that fits in the budget is returned exactly as
// before.

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// statusUnboundedBudgetJobs is how many of the seeded archived records the
// driven-down budget is meant to allow through before it refuses. It is small
// enough that "O(budget), not O(history)" is unmistakable against
// statusLimitArchived (5,000).
const statusUnboundedBudgetJobs = 10

// statusUnboundedSpanBudgetJobs sizes the budget for the cross-RepGroup case: it
// is MORE than the larger group's whole history on its own
// (statusLimitArchived records) and LESS than the two groups' histories together
// (statusLimitTotal). That bracket is what makes the case test what it claims -
// a budget that either group could bust alone is refused for the wrong reason,
// and a per-RepGroup budget of this size serves the request instead of refusing
// it.
const statusUnboundedSpanBudgetJobs = statusLimitArchived + statusLimitOtherArchived/2

// TestReliable4StatusUnboundedHistoryBudget pins that a `wr status` request that
// cannot push its limit down is bounded by a heap budget rather than by the size
// of the history, that it refuses with a message naming the way out instead of
// materialising it, and that everything within the budget is returned exactly as
// it was before the budget existed.
func TestReliable4StatusUnboundedHistoryBudget(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager with a large archived history in two report groups", t, func() {
		config, serverConfig, addr, reqs, clientConnectTime := jobqueueTestInit(true)
		serverConfig.dontWipeDevDB = true

		// the state-filter case below reserves a live job and buries it by hand, so
		// the shortTTR default of one second must not expire underneath it on a
		// loaded host. Nothing here depends on a TTR elapsing.
		serverConfig.Timings.ItemTTR = time.Hour

		seedStatusLimitHistory(ctx, serverConfig)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		So(waitUntilRecovered(server), ShouldBeTrue)

		decodes := func() uint64 {
			return server.db.archivedDecodes.Load()
		}

		restore := maxArchivedBytes

		defer func() {
			maxArchivedBytes = restore
		}()

		// the reference, at the shipped budget: the unbounded fetch is unchanged,
		// so this whole history is still returned, oldest-started first.
		before := decodes()
		reference, refSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
			RepGroupMatchExact, limitJobsOptions{}))
		So(refSrerr, ShouldBeEmpty)
		So(len(reference), ShouldEqual, statusLimitArchived)
		So(decodes()-before, ShouldEqual, uint64(statusLimitArchived))
		So(reference[0].Cmd, ShouldEqual, statusLimitCmd(statusLimitRepGroup, statusLimitArchived-1))

		// size the budget from the records themselves, so the test says what it
		// means whatever a Job encodes to.
		historyBytes, perJob := archivedBytesFor(server, statusLimitRepGroup)
		So(perJob, ShouldBeGreaterThan, 0)
		So(historyBytes, ShouldBeGreaterThan, perJob*statusLimitArchived/2)

		Convey("A request that cannot push its limit down is bounded by the budget, not the history", func() {
			maxArchivedBytes = perJob * statusUnboundedBudgetJobs

			before := decodes()
			jobs, srerr, qerr := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{}))
			used := decodes() - before

			// nothing at all was decoded: the history is priced from the encoded
			// records first, so a request that cannot fit costs no heap rather than
			// O(history) of it.
			So(used, ShouldEqual, uint64(0))
			So(jobs, ShouldBeEmpty)

			// and it refused in a way the operator can act on, through the half of
			// the error pair the client turns into its error.
			So(srerr, ShouldContainSubstring, "too much completed-job history")
			So(srerr, ShouldContainSubstring, "--limit")
			So(srerr, ShouldContainSubstring, "-o counts")
			So(qerr, ShouldEqual, srerr)
		})

		Convey("The budget spans the report groups a -z request loops over", func() {
			maxArchivedBytes = perJob * statusUnboundedSpanBudgetJobs

			// the control half of the claim: at this budget the larger group's
			// history fits on its own, and is served in full. So does the smaller
			// one, being smaller. Neither group can bust this budget alone, which is
			// the only way the -z refusal below can be attributed to the SPAN.
			before := decodes()
			alone, aloneSrerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{}))
			So(aloneSrerr, ShouldBeEmpty)
			So(len(alone), ShouldEqual, statusLimitArchived)
			So(decodes()-before, ShouldEqual, uint64(statusLimitArchived))

			before = decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitSubStr,
				RepGroupMatchSubStr, limitJobsOptions{}))

			// two report groups, one budget: the second cannot start afresh, so a
			// history that only exceeds the budget in TOTAL is still refused - and
			// refused before the first group's records are decoded, not after.
			So(decodes()-before, ShouldEqual, uint64(0))
			So(jobs, ShouldBeEmpty)
			So(srerr, ShouldContainSubstring, "too much completed-job history")
		})

		Convey("A history that fits the budget is returned exactly as before", func() {
			maxArchivedBytes = historyBytes

			jobs, srerr, qerr := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{}))
			So(srerr, ShouldBeEmpty)
			So(qerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, len(reference))

			mismatches := 0

			for i, job := range jobs {
				if statusLimitIdentity(job) != statusLimitIdentity(reference[i]) {
					mismatches++
				}
			}

			So(mismatches, ShouldEqual, 0)
		})

		Convey("A limited request is untouched by the budget, since its decoding is already bounded", func() {
			maxArchivedBytes = perJob

			before := decodes()
			jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitRepGroup,
				RepGroupMatchExact, limitJobsOptions{Limit: 1}))
			So(srerr, ShouldBeEmpty)
			So(len(jobs), ShouldEqual, 1)
			So(decodes()-before, ShouldEqual, uint64(1))
			So(statusLimitIdentity(jobs[0]),
				ShouldEqual, statusLimitIdentityWithSimilar(reference[0], statusLimitArchived-1))
		})

		Convey("A request filtered to a state other than complete is served, not refused", func() {
			// `wr status -i <substr> -z -o plain -b` reaches the pricing check with
			// everything the refusal looks for except the unbounded decode: Limit 0,
			// IncludeComplete, and a state filter of buried. getDBJobsByRepGroup
			// rejects every state filter but complete before the database is read at
			// all, so this shape materialises no archived record however much history
			// it matches - and pricing it would refuse a working query outright,
			// rather than bound one.
			jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			addStatusLimitLiveJobs(jq, reqs)

			reserved, errr := jq.Reserve(clientConnectTime)
			So(errr, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(jq.Bury(reserved, nil, "status history budget test"), ShouldBeNil)

			served := func(state JobState) ([]*Job, string) {
				jobs, srerr, _ := server.getJobsByRepGroup(ctx, statusLimitOpts(statusLimitSubStr,
					RepGroupMatchSubStr, limitJobsOptions{State: state}))

				return jobs, srerr
			}

			// what each filter answers at the shipped budget: the live jobs of that
			// state and nothing from the history, since none of these records is ready
			// or buried.
			readyRef, readySrerr := served(JobStateReady)
			So(readySrerr, ShouldBeEmpty)
			So(len(readyRef), ShouldEqual, statusLimitLive-1)

			buriedRef, buriedSrerr := served(JobStateBuried)
			So(buriedSrerr, ShouldBeEmpty)
			So(len(buriedRef), ShouldEqual, 1)

			// a budget of one byte: every single archived record in either group busts
			// it, so this is the most refusable history there could be.
			maxArchivedBytes = 1

			before := decodes()

			readyJobs, readyErr := served(JobStateReady)
			So(readyErr, ShouldBeEmpty)
			So(len(readyJobs), ShouldEqual, len(readyRef))

			buriedJobs, buriedErr := served(JobStateBuried)
			So(buriedErr, ShouldBeEmpty)
			So(len(buriedJobs), ShouldEqual, len(buriedRef))
			So(statusLimitIdentity(buriedJobs[0]), ShouldEqual, statusLimitIdentity(buriedRef[0]))

			// and neither of them touched the history at all, which is why refusing
			// them would be refusing nothing.
			So(decodes()-before, ShouldEqual, uint64(0))
		})

		Convey("The whole `wr status -o plain` client request is refused, not served", func() {
			maxArchivedBytes = perJob * statusUnboundedBudgetJobs

			jq, errc := Connect(addr, config.ManagerCAFile, config.ManagerCertDomain, token, clientConnectTime)
			So(errc, ShouldBeNil)

			defer disconnect(jq)

			// this is exactly what cmd/status.go sends for `-o plain`: it zeroes the
			// limit for the ungrouped output formats.
			before := decodes()
			jobs, errg := jq.GetByRepGroup(statusLimitRepGroup, false, 0, "", false, false)
			So(errg, ShouldNotBeNil)
			So(errg.Error(), ShouldContainSubstring, "too much completed-job history")
			So(errg.Error(), ShouldContainSubstring, "--limit")
			So(jobs, ShouldBeEmpty)
			So(decodes()-before, ShouldEqual, uint64(0))

			// the way out the message names really is one: the same query with a
			// limit answers, and answers with the history's own job.
			limited, errl := jq.GetByRepGroup(statusLimitRepGroup, false, 1, "", false, false)
			So(errl, ShouldBeNil)
			So(len(limited), ShouldEqual, 1)
			So(limited[0].Cmd, ShouldEqual, reference[0].Cmd)
			So(limited[0].Similar, ShouldEqual, statusLimitArchived-1)
		})

		Convey("The REST status endpoint reports the refusal as a client error, not a server fault", func() {
			maxArchivedBytes = perJob * statusUnboundedBudgetJobs

			// GET /rest/v1/jobs/<repgroup> with no limit parameter: parseRESTStatusQuery
			// defaults limit to 0, so this is the unbounded shape too.
			w := httptest.NewRecorder()
			r := httptest.NewRequestWithContext(ctx, http.MethodGet,
				restJobsEndpoint+statusLimitRepGroup, nil)
			r.Header.Set("Authorization", "Bearer "+string(token))

			before := decodes()

			restJobs(ctx, server)(w, r)

			resp := w.Result()

			defer resp.Body.Close()

			body, errb := io.ReadAll(resp.Body)
			So(errb, ShouldBeNil)

			// 4xx, not the 500 this used to be: the manager is healthy and the caller's
			// query is the thing that has to change, which is what the body tells it.
			So(resp.StatusCode, ShouldEqual, http.StatusBadRequest)
			So(string(body), ShouldContainSubstring, "too much completed-job history")
			So(string(body), ShouldContainSubstring, "--limit")
			So(decodes()-before, ShouldEqual, uint64(0))

			// and the way out works over REST as well, at the same budget.
			w = httptest.NewRecorder()
			r = httptest.NewRequestWithContext(ctx, http.MethodGet,
				restJobsEndpoint+statusLimitRepGroup+"?limit=1", nil)
			r.Header.Set("Authorization", "Bearer "+string(token))

			restJobs(ctx, server)(w, r)

			limitedResp := w.Result()

			defer limitedResp.Body.Close()

			var statuses []JStatus

			So(limitedResp.StatusCode, ShouldEqual, http.StatusOK)
			So(json.NewDecoder(limitedResp.Body).Decode(&statuses), ShouldBeNil)
			So(len(statuses), ShouldEqual, 1)
			So(statuses[0].Cmd, ShouldEqual, reference[0].Cmd)
		})
	})
}

// archivedBytesFor returns the total encoded size of repGroup's archived records
// and the mean size of one, so a test can express a byte budget in jobs.
func archivedBytesFor(server *Server, repGroup string) (int, int) {
	budget := &archivedBytesBudget{remaining: math.MaxInt}

	So(server.db.spendArchivedBytesByRepGroup(repGroup, budget), ShouldBeNil)
	So(budget.priced, ShouldBeGreaterThan, 0)

	total := math.MaxInt - budget.remaining

	return total, total / budget.priced
}
