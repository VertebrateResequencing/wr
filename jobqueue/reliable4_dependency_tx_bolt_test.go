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
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// This file holds the dependency tests that count bbolt transactions, so are
// deleted with bbolt (spec F5).

// TestReliable4DependencyFreeTxCost proves that resolving the dependencies of a
// job that has none costs no bolt read transaction at all, and still returns
// nothing to depend on. incompleteJobKeys used to ask depGroupsEverSeen about an
// empty list of dep groups, and that opened a read transaction regardless.
func TestReliable4DependencyFreeTxCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database holding live jobs in dep groups", t, func() {
		fixture := newDepTxFixture(t, ctx)

		defer func() {
			So(fixture.db.close(ctx), ShouldBeNil)
		}()

		Convey("Resolving jobs with no dependencies opens no read transaction", func() {
			var deps Dependencies

			before := fixture.db.bolt.Stats().TxN
			unexpected := 0

			for range depTxResolutions {
				keys, waiting, err := deps.dependencyKeys(fixture.db, fixture.groups)
				if err != nil || len(keys) != 0 || len(waiting) != 0 {
					unexpected++
				}
			}

			So(unexpected, ShouldEqual, 0)
			So(fixture.db.bolt.Stats().TxN-before, ShouldEqual, 0)
		})

		Convey("Resolving jobs with only an essence dependency costs one transaction each", func() {
			deps := Dependencies{NewEssenceDependency(depTxLiveCmd, "")}

			before := fixture.db.bolt.Stats().TxN
			unexpected := 0

			for range depTxResolutions {
				keys, _, err := deps.dependencyKeys(fixture.db, fixture.groups)
				if err != nil || len(keys) != 1 {
					unexpected++
				}
			}

			So(unexpected, ShouldEqual, 0)
			So(fixture.db.bolt.Stats().TxN-before, ShouldEqual, depTxResolutions)
		})
	})
}

// TestReliable4RecoveryDependencyPass proves that resolving a whole recovery's
// worth of jobs costs one bolt read transaction per chunk of jobs rather than
// one or more per job, resolves each job to exactly what per-job resolution
// resolves it to whether the jobs fit in one chunk or span many, and still stops
// promptly when the recovery's context is cancelled without leaving a
// transaction open.
func TestReliable4RecoveryDependencyPass(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database of live jobs and a recovery's worth of jobs to resolve", t, func() {
		fixture := newDepTxFixture(t, ctx)

		defer func() {
			So(fixture.db.close(ctx), ShouldBeNil)
		}()

		jobs := depTxRecoveryJobs(depTxResolutions)

		Convey("The pass costs a transaction per chunk where per-job resolution costs one per reading job", func() {
			beforePerJob := fixture.db.bolt.Stats().TxN
			failed := 0

			for _, job := range jobs {
				if _, _, err := job.Dependencies.dependencyKeys(fixture.db, fixture.groups); err != nil {
					failed++
				}
			}

			perJobTx := fixture.db.bolt.Stats().TxN - beforePerJob

			beforePass := fixture.db.bolt.Stats().TxN
			resolved, err := fixture.db.resolveDependencies(ctx, jobs, fixture.groups)
			passTx := fixture.db.bolt.Stats().TxN - beforePass

			So(failed, ShouldEqual, 0)
			So(err, ShouldBeNil)
			So(resolved, ShouldHaveLength, len(jobs))

			// one transaction for each job that has to ask the database
			// something, and none at all for the rest: a dep group with a live
			// member is answered from memory, so a dependency on one costs no
			// database read however many members the group has.
			So(perJobTx, ShouldEqual, depTxReadingJobs)
			So(depTxReadingJobs+depTxReadFreeJobs, ShouldEqual, len(jobs))

			So(passTx, ShouldEqual, depTxWantChunks(len(jobs)))
		})

		Convey("At the production chunk size the pass costs exactly one transaction per 1,000 jobs", func() {
			So(dependencyResolutionChunkSize, ShouldEqual, depTxProdChunkSize)

			many := depTxRecoveryJobs(depTxProdPassJobs)
			So(depTxWantChunks(len(many)), ShouldEqual, depTxProdPassChunks)

			before := fixture.db.bolt.Stats().TxN
			resolved, err := fixture.db.resolveDependencies(ctx, many, fixture.groups)
			passTx := fixture.db.bolt.Stats().TxN - before

			So(err, ShouldBeNil)
			So(resolved, ShouldHaveLength, len(many))

			// exactly ceil(N/1000), not a range. A bound loose enough to allow one
			// transaction per essence dependency or one per named dep group would
			// still be satisfied by a regression to per-job reads inside a chunk,
			// which is the cost this pass exists to remove.
			So(passTx, ShouldEqual, depTxProdPassChunks)
		})

		Convey("Jobs spanning many chunks cost exactly one transaction per chunk", func() {
			defer setDependencyResolutionChunkSize(depTxChunkSize)()

			wantChunks := depTxWantChunks(len(jobs))

			before := fixture.db.bolt.Stats().TxN
			resolved, err := fixture.db.resolveDependencies(ctx, jobs, fixture.groups)
			passTx := fixture.db.bolt.Stats().TxN - before

			So(err, ShouldBeNil)
			So(resolved, ShouldHaveLength, len(jobs))

			// exactly, not at most: a single transaction for the whole pass would
			// be fewer, but it would hold bolt's mmaplock for the whole pass and
			// so stall every write to the database for that long, which is the
			// production symptom this pass must not create.
			So(passTx, ShouldEqual, wantChunks)

			// the jobs really do span more than one chunk, and a chunk each is
			// still far below a transaction per job.
			So(wantChunks, ShouldBeGreaterThan, 1)
			So(wantChunks, ShouldBeLessThan, len(jobs))
		})

		// dependencyResolutionChunkSize is a var so that the tests above can lower
		// it, and slices.Chunk panics outright on a chunk size below 1. The floor
		// in resolveDependencies is what keeps a zero or negative one from taking
		// the manager down in the middle of prior-state recovery, and without it
		// this block panics rather than failing an assertion
		// (.docs/bugfixes/260825-3.md item 4).
		Convey("A chunk size below one is floored to one rather than panicking", func() {
			floored, cost := 0, 0

			for _, size := range []int{0, -1} {
				func() {
					defer setDependencyResolutionChunkSize(size)()

					before := fixture.db.bolt.Stats().TxN
					resolved, err := fixture.db.resolveDependencies(ctx, jobs, fixture.groups)

					if err == nil && len(resolved) == len(jobs) {
						floored++
					}

					// floored to one, so a transaction per job: the smallest
					// chunking the pass can do, never a pass-wide transaction.
					if fixture.db.bolt.Stats().TxN-before == len(jobs) {
						cost++
					}
				}()
			}

			So(floored, ShouldEqual, 2)
			So(cost, ShouldEqual, 2)
		})

		Convey("Chunking resolves every job exactly as per-job resolution does too", func() {
			defer setDependencyResolutionChunkSize(depTxChunkSize)()

			So(depTxWantChunks(len(jobs)), ShouldBeGreaterThan, 1)

			depTxSoResolvesAsPerJob(ctx, fixture, jobs)
		})

		Convey("A cancelled pass leaves no read transaction open", func() {
			cancelled, cancel := context.WithCancel(ctx)
			cancel()

			_, err := fixture.db.resolveDependencies(cancelled, jobs, fixture.groups)
			So(errors.Is(err, context.Canceled), ShouldBeTrue)

			// a read transaction left open would hold bolt's mmaplock and stall
			// every write to the database.
			So(fixture.db.bolt.Stats().OpenTxN, ShouldEqual, 0)
		})

		Convey("A cancelled context stops at the chunk it is in, opening no more", func() {
			defer setDependencyResolutionChunkSize(1)()

			cancelled, cancel := context.WithCancel(ctx)
			cancel()

			before := fixture.db.bolt.Stats().TxN
			resolved, err := fixture.db.resolveDependencies(cancelled, jobs, fixture.groups)
			passTx := fixture.db.bolt.Stats().TxN - before

			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(resolved, ShouldBeNil)

			// one chunk per job at this chunk size, so a pass that carried on
			// would open len(jobs) transactions.
			So(passTx, ShouldBeLessThanOrEqualTo, 1)
			So(len(jobs), ShouldBeGreaterThan, 1)
		})
	})
}

// depTxWantChunks is how many bolt read transactions a pass over count jobs is
// allowed to cost: one per chunk of dependencyResolutionChunkSize jobs.
func depTxWantChunks(count int) int {
	size := dependencyResolutionChunkSize

	return (count + size - 1) / size
}

// TestDepGranularityLiveGroupTxCost proves that a dep group with a live member
// is answered from memory, opening no bbolt read transaction.
func TestDepGranularityLiveGroupTxCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a database and the per-group state its live jobs represent", t, func() {
		fixture := newDepTxFixture(t, ctx)

		defer func() {
			So(fixture.db.close(ctx), ShouldBeNil)
		}()

		Convey("Resolving a dep group with live members opens no read transaction", func() {
			before := fixture.db.bolt.Stats().TxN

			deps, _ := fixture.soResolves(Dependencies{NewDepGroupDependency(depTxLiveGroup)})
			So(deps, ShouldResemble, []string{depGroupDependencyKey(depTxLiveGroup)})
			So(fixture.db.bolt.Stats().TxN-before, ShouldEqual, 0)
		})
	})
}

// TestReliable4RecoveryDependencyTxCost proves that recovering dependent jobs
// on a DB-preserving restart no longer costs a bbolt read transaction per job.
// TestReliable4RecoveryDependencyState covers what that recovery restores.
func TestReliable4RecoveryDependencyTxCost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped server whose database holds ready, dependent and waiting jobs", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := jobqueueTestInit(true)

		beforeRestart := depTxStoppedServer(ctx, config, serverConfig, addr, standardReqs, clientConnectTime)

		serverConfig.dontWipeDevDB = true
		server, _, release := pausedRecoveringFixtureServer(ctx, serverConfig)

		defer func() {
			release()
			server.Stop(ctx, true)
		}()

		beforeTx := server.boltDB().bolt.Stats().TxN

		release()
		So(waitUntilRecovered(server), ShouldBeTrue)

		Convey("Recovery costs fewer read transactions than there are jobs", func() {
			So(server.boltDB().bolt.Stats().TxN-beforeTx, ShouldBeLessThan, len(beforeRestart))
		})
	})
}
