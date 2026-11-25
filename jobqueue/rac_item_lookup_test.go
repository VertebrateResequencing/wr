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
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/limiter"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// racLookupJobs is how many ready jobs TestRACCycleLooksUpItemsInChunks
	// cycles over.
	racLookupJobs = 4000

	// racLookupHold is how long that test's writer holds the queue's write
	// lock each time it takes it.
	racLookupHold = time.Millisecond

	racLookupReqGroup = "rac-lookup"

	// racLookupTakenGroup is the reserve group of the fixture's jobs that are
	// reserved after a cycle's snapshot.
	racLookupTakenGroup = "taken"

	racLookupRAM        = 100
	racLookupLearnedRAM = 2000

	// racLookupLimitGroup is the limit group, with no capacity, of the jobs
	// racLookupBehindBacklog picks.
	racLookupLimitGroup = "rac-lookup-full"
)

// TestRACCycleLooksUpItemsInChunks covers a rac cycle over many ready jobs
// while reserves keep taking the queue's write lock. A cycle that recomputes a
// job's requirements first looks up the job's queue item; looked up with one
// Get per job, the cycle took the queue's read lock once per job, so it queued
// behind a waiting reserve once per job, and every reserve made meanwhile
// queued behind it in turn, doubling to quadrupling the median reserve wait
// for as long as the cycle ran (bugfix 261119 item L).
func TestRACCycleLooksUpItemsInChunks(t *testing.T) {
	ctx := context.Background()

	for _, rc := range []string{racLimitRunnerCmd, ""} {
		Convey(fmt.Sprintf("Given a queue of ready jobs whose requirements a rac cycle recomputes (rc %q)", rc),
			t, func() {
				s, q, data, blocker := racLookupFixture(ctx, &jqs.Requirements{DiskSet: true})

				// the writer stands in for a stream of reserves: it holds the
				// queue's write lock for racLookupHold each time, taking it again
				// as soon as it lets it go.
				stop, stopped := make(chan struct{}), make(chan struct{})
				holding := make(chan struct{}, 1)

				go func() {
					defer close(stopped)

					for !isClosed(stop) {
						_, err := q.RegroupReady([]string{blocker}, func(_ any, group string) string {
							select {
							case holding <- struct{}{}:
							default:
							}

							time.Sleep(racLookupHold)

							return group
						})
						if err != nil {
							return
						}
					}
				}()

				Reset(func() {
					close(stop)
					<-stopped
					So(q.Destroy(), ShouldBeNil)
				})

				select {
				case <-holding:
				case <-time.After(10 * time.Second):
					So("the writer never took the queue's lock", ShouldBeEmpty)
				}

				Convey("the cycle waits for the writer a few times, not once per job", func() {
					start := time.Now()

					s.buildSchedulerGroups(ctx, q, data, rc)

					took := time.Since(start)

					// waiting once per job takes at least racLookupJobs holds.
					So(took, ShouldBeLessThan, racLookupJobs*racLookupHold/2)
				})
			})
	}
}

// TestRACCycleRecomputesOnlyJobsStillReady covers the item each job's
// recompute checks: a rac cycle recomputes the requirements of the jobs whose
// items are still ready, and leaves those a reserve has taken since its
// snapshot as they were reserved, each judged by its own item.
func TestRACCycleRecomputesOnlyJobsStillReady(t *testing.T) {
	ctx := context.Background()

	for _, rc := range []string{racLimitRunnerCmd, ""} {
		Convey(fmt.Sprintf("Given ready jobs, every third one reserved after a rac cycle's snapshot (rc %q)", rc),
			t, func() {
				s, q, data, _ := racLookupFixture(ctx, &jqs.Requirements{RAM: racLookupLearnedRAM, DiskSet: true})

				Reset(func() { So(q.Destroy(), ShouldBeNil) })

				taken := 0

				for {
					if _, err := q.Reserve(racLookupTakenGroup, 0); err != nil {
						break
					}

					taken++
				}

				So(taken, ShouldEqual, racLookupJobs/3)

				Convey("the cycle gives the learned RAM to the jobs still ready, and only to them", func() {
					s.buildSchedulerGroups(ctx, q, data, rc)

					wrong := 0

					for i, d := range data {
						job := d.(*Job) //nolint:forcetypeassert,errcheck

						want := racLookupLearnedRAM
						if racLookupTaken(i) {
							want = racLookupRAM
						}

						job.RLock()

						if job.Requirements.RAM != want {
							wrong++
						}

						job.RUnlock()
					}

					So(wrong, ShouldEqual, 0)
				})
			})
	}
}

// TestRACCycleChecksTheItemOfAJobModifiedSinceItsSnapshot covers a job whose
// key a `wr mod` changed after a rac cycle's snapshot, and that a reserve then
// took: the cycle must check the job's own item, which moved to the new key,
// and so leave the running job at the size it was reserved at. Looked up by
// the snapshot's key, it found no item, or another job's item re-added under
// that key, and recomputed the running job to the learned size.
func TestRACCycleChecksTheItemOfAJobModifiedSinceItsSnapshot(t *testing.T) {
	ctx := context.Background()

	for _, readdOldKey := range []bool{false, true} {
		Convey(fmt.Sprintf("Given a rac cycle's snapshot of ready jobs, the first of which is then modified "+
			"to a new key and reserved (old key re-added by another job: %v)", readdOldKey), t, func() {
			s, q, data, _ := racLookupFixture(ctx, &jqs.Requirements{RAM: racLookupLearnedRAM, DiskSet: true})

			Reset(func() { So(q.Destroy(), ShouldBeNil) })

			candidates := s.snapshotReadyJobs(data)
			job := candidates[0].job
			oldKey := job.Key()

			job.Lock()
			job.Cmd = "echo rac-lookup modified"
			job.invalidateDerivedLocked()
			job.Unlock()

			newKey := job.Key()
			So(newKey, ShouldNotEqual, oldKey)
			So(q.ChangeKey(oldKey, newKey), ShouldBeNil)

			if readdOldKey {
				other := &Job{
					Cmd: "echo rac-lookup 0", Cwd: testCwd,
					ReqGroup: racLookupReqGroup, RepGroup: racLookupReqGroup,
					Requirements: &jqs.Requirements{RAM: racLookupRAM, Time: time.Minute, Cores: 1, Disk: 1},
				}
				So(other.Key(), ShouldEqual, oldKey)

				_, err := q.Add(ctx, oldKey, "elsewhere", other, 0, 0, time.Hour, queue.SubQueueReady)
				So(err, ShouldBeNil)
			}

			So(q.SetReserveGroup(newKey, "modified"), ShouldBeNil)

			item, err := q.Reserve("modified", 0)
			So(err, ShouldBeNil)
			So(item.Key, ShouldEqual, newKey)

			Convey("the cycle leaves the running job's requirements as they were", func() {
				s.scheduleReadyJobsByPriority(ctx, q, make(map[string]*sgroup), candidates, racLimitRunnerCmd,
					make(map[string]*jqs.Requirements))

				job.RLock()
				ram := job.Requirements.RAM
				job.RUnlock()

				So(ram, ShouldEqual, racLookupRAM)

				second := candidates[1].job
				second.RLock()
				ram = second.Requirements.RAM
				second.RUnlock()

				So(ram, ShouldEqual, racLookupLearnedRAM)
			})
		})
	}
}

// TestRACCycleAfterQueueDestroyed covers a rac cycle that runs once its queue
// has been destroyed, as one can while the manager shuts down: it looks up no
// items, so it recomputes each job as for a job that left the queue, and does
// not panic.
func TestRACCycleAfterQueueDestroyed(t *testing.T) {
	ctx := context.Background()

	for _, rc := range []string{racLimitRunnerCmd, ""} {
		Convey(fmt.Sprintf("Given a destroyed queue of ready jobs (rc %q)", rc), t, func() {
			s, q, data, _ := racLookupFixture(ctx, &jqs.Requirements{RAM: racLookupLearnedRAM, DiskSet: true})
			So(q.Destroy(), ShouldBeNil)

			Convey("a rac cycle over them recomputes them without panicking", func() {
				So(func() { s.buildSchedulerGroups(ctx, q, data, rc) }, ShouldNotPanic)

				job := data[0].(*Job) //nolint:forcetypeassert,errcheck
				job.RLock()
				ram := job.Requirements.RAM
				job.RUnlock()

				So(ram, ShouldEqual, racLookupLearnedRAM)
			})
		})
	}
}

// TestRACCycleItemsAfterQueueDestroyedMidCycle covers a queue destroyed while
// a rac cycle is between two chunks of its lookups, as it can be while the
// manager shuts down, since Destroy does not wait for a running cycle: every
// candidate past the chunk looked up before the queue closed has no item, and
// is not judged by the item of a candidate in that earlier chunk.
func TestRACCycleItemsAfterQueueDestroyedMidCycle(t *testing.T) {
	ctx := context.Background()

	Convey("Given a rac cycle's items, the first chunk looked up before its queue is destroyed", t, func() {
		s, q, data, _ := racLookupFixture(ctx, &jqs.Requirements{RAM: racLookupLearnedRAM, DiskSet: true})

		destroyed := false

		Reset(func() {
			if !destroyed {
				So(q.Destroy(), ShouldBeNil)
			}
		})

		candidates := s.snapshotReadyJobs(data)
		items := newRACReadyItems(q, candidates)

		So(items.item(0), ShouldNotBeNil)
		So(q.Destroy(), ShouldBeNil)

		destroyed = true

		Convey("every candidate past the first chunk has no item", func() {
			found := 0

			for i := racItemChunk; i < len(candidates); i++ {
				if items.item(i) != nil {
					found++
				}
			}

			So(len(candidates), ShouldBeGreaterThan, racItemChunk)
			So(found, ShouldEqual, 0)
		})
	})
}

// TestRACCycleJudgesJobsPastALimitBlockedBacklogByTheirOwnItems covers a rac
// cycle that skips more than a chunk of limit-blocked jobs at a time, as it
// does over a large limit-blocked backlog: each schedulable job after the
// skip must still be judged by its own queue item, so the jobs a reserve took
// after the cycle's snapshot keep the size they were reserved at, and the jobs
// still ready get the learned size. Judged by another job's item, a reserved
// job would be recomputed while it runs, or a ready one left unrecomputed.
func TestRACCycleJudgesJobsPastALimitBlockedBacklogByTheirOwnItems(t *testing.T) {
	ctx := context.Background()

	Convey("Given ready jobs behind limit-blocked backlogs longer than a lookup chunk, every third one "+
		"reserved after a rac cycle's snapshot", t, func() {
		s, q, data, _ := racLookupFixtureBlocking(ctx, &jqs.Requirements{RAM: racLookupLearnedRAM, DiskSet: true},
			racLookupBehindBacklog)

		Reset(func() { So(q.Destroy(), ShouldBeNil) })

		taken := 0

		for {
			if _, err := q.Reserve(racLookupTakenGroup, 0); err != nil {
				break
			}

			taken++
		}

		So(taken, ShouldEqual, racLookupJobs/3)

		Convey("the cycle gives the learned RAM to the jobs still ready, and only to them", func() {
			groups := s.buildSchedulerGroups(ctx, q, data, racLimitRunnerCmd)

			skipped, blocked := 0, 0

			for _, g := range groups {
				skipped += g.skipped
			}

			for i := range racLookupJobs {
				if racLookupBehindBacklog(i) {
					blocked++
				}
			}

			So(blocked, ShouldBeGreaterThan, 2*racItemChunk)
			So(skipped, ShouldEqual, blocked)

			wrong := 0

			for i, d := range data {
				job := d.(*Job) //nolint:forcetypeassert,errcheck

				want := racLookupLearnedRAM
				if racLookupTaken(i) {
					want = racLookupRAM
				}

				job.RLock()

				if job.Requirements.RAM != want {
					wrong++
				}

				job.RUnlock()
			}

			So(wrong, ShouldEqual, 0)
		})
	})
}

// TestRACCycleItemsInAnyOrder covers the items a rac cycle's racReadyItems
// gives when asked for its candidates out of order: skipping ahead past the
// chunk looked up, within it, at the chunks' exact boundaries, and backwards.
// Each must be the candidate's own item, whatever was asked for before.
func TestRACCycleItemsInAnyOrder(t *testing.T) {
	ctx := context.Background()

	last := racLookupJobs - 1
	orders := [][]int{
		{0, 1500, 1501, 2600, last},
		{1500, 1499, 2600, 0, last, 1},
		{0, racItemChunk - 1, racItemChunk, 2*racItemChunk - 1, 2 * racItemChunk, 3*racItemChunk - 1, 3 * racItemChunk},
		{racItemChunk - 1, racItemChunk, 2*racItemChunk - 1, 2 * racItemChunk},
		{last, 2 * racItemChunk, 2*racItemChunk - 1, racItemChunk, racItemChunk - 1, 0, 1},
		{5, 5, 4, 6, 1030, 1029, 5},
	}

	Convey("Given a rac cycle's candidates and their queue items", t, func() {
		s, q, data, _ := racLookupFixture(ctx, &jqs.Requirements{RAM: racLookupLearnedRAM, DiskSet: true})

		Reset(func() { So(q.Destroy(), ShouldBeNil) })

		candidates := s.snapshotReadyJobs(data)
		So(len(candidates), ShouldBeGreaterThan, 3*racItemChunk)

		own := make([]*queue.Item, len(candidates))
		failed := 0

		for i, candidate := range candidates {
			item, err := q.Get(candidate.job.Key())
			if err != nil {
				failed++
			}

			own[i] = item
		}

		So(failed, ShouldEqual, 0)

		for _, order := range orders {
			Convey(fmt.Sprintf("each candidate asked for in the order %v gets its own item", order), func() {
				items := newRACReadyItems(q, candidates)

				for _, i := range order {
					So(items.item(i), ShouldPointTo, own[i])
				}
			})
		}

		Convey("walking every candidate allocates no more than walking the first chunk, as each chunk "+
			"reuses the slices of the one before", func() {
			walk := func(n int) float64 {
				return testing.AllocsPerRun(5, func() {
					items := newRACReadyItems(q, candidates)

					for i := range n {
						items.item(i)
					}
				})
			}

			So(walk(len(candidates)), ShouldBeLessThanOrEqualTo, walk(racItemChunk))
		})

		Convey("one racReadyItems asked for every order in turn gives each candidate its own item", func() {
			items := newRACReadyItems(q, candidates)
			wrong := 0

			for _, order := range orders {
				for _, i := range order {
					if items.item(i) != own[i] {
						wrong++
					}
				}
			}

			So(wrong, ShouldEqual, 0)
		})
	})
}

// racLookupFixture returns a server and its queue holding racLookupJobs ready
// jobs already in the scheduler group their requirements call for, with rec as
// the learned recommendation for their ReqGroup so a cycle recomputes each one,
// the jobs as the ready item data a cycle is given, and the key of one more
// ready item, not among them, for a writer to hold the queue's lock with. The
// jobs racLookupTaken picks have their items in reserve group
// racLookupTakenGroup.
func racLookupFixture(ctx context.Context, rec *jqs.Requirements) (*Server, *queue.Queue, []any, string) {
	return racLookupFixtureBlocking(ctx, rec, func(int) bool { return false })
}

// racLookupFixtureBlocking is racLookupFixture, but the jobs blocked picks are
// in limit group racLookupLimitGroup, which has no capacity, so a cycle with a
// runner command skips them as limit-blocked.
func racLookupFixtureBlocking(ctx context.Context, rec *jqs.Requirements,
	blocked func(int) bool) (*Server, *queue.Queue, []any, string) {
	cache := newReqStatCache()
	cache.entries[racLookupReqGroup] = &reqStatEntry{rec: rec, loaded: true}

	q := queue.New(ctx, "rac-lookup")
	s := &Server{
		q:  q,
		db: &db{reqStats: cache},
		limiter: limiter.New(func(_ context.Context, name string) *limiter.GroupData {
			if name == racLookupLimitGroup {
				return limiter.NewCountGroupData(0)
			}

			return nil
		}),
		previouslyScheduledGroups: make(map[string]*sgroup),
	}

	defs := make([]*queue.ItemDef, 0, racLookupJobs+1)
	data := make([]any, 0, racLookupJobs)

	for i := range racLookupJobs + 1 {
		job := &Job{
			Cmd: fmt.Sprintf("echo rac-lookup %d", i), Cwd: testCwd,
			ReqGroup: racLookupReqGroup, RepGroup: racLookupReqGroup,
			Requirements: &jqs.Requirements{RAM: racLookupRAM, Time: time.Minute, Cores: 1, Disk: 1},
		}

		if i < racLookupJobs && blocked(i) {
			job.LimitGroups = []string{racLookupLimitGroup}
		}

		group := job.schedulerGroupSnapshot().group
		job.setSchedulerGroup(group)

		reserveGroup := group
		if racLookupTaken(i) && i < racLookupJobs {
			reserveGroup = racLookupTakenGroup
		}

		defs = append(defs, &queue.ItemDef{Key: job.Key(), ReserveGroup: reserveGroup, Data: job, TTR: time.Hour})

		if i < racLookupJobs {
			data = append(data, job)
		}
	}

	_, _, err := q.AddMany(ctx, defs)
	So(err, ShouldBeNil)

	return s, q, data, defs[racLookupJobs].Key
}

// racLookupBehindBacklog reports whether racLookupFixtureBlocking, given it,
// puts the ith job behind no capacity: the first 1100 jobs and the 1200 from
// 1400, so a cycle skips more than a chunk of them before its first
// schedulable job and again before job 2600. Neither skip is a whole number of
// racLookupTaken's period, so a cycle that judged a job by the item of a job
// any number of skipped jobs before would judge some jobs wrongly.
func racLookupBehindBacklog(i int) bool {
	return i < 1100 || (i >= 1400 && i < 2600)
}

// racLookupTaken reports whether racLookupFixture puts the ith job's item in
// reserve group racLookupTakenGroup: every third job, starting with the second.
// The pattern must not repeat every racItemChunk jobs, so that a cycle that
// checked the item of the job a chunk, or any whole number of chunks, before
// would judge some jobs wrongly.
func racLookupTaken(i int) bool {
	return i%3 == 1
}
