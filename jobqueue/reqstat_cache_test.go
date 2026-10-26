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
	"math"
	"os"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// reqStatTestGroup is the ReqGroup most of these tests learn values for.
	reqStatTestGroup = "rg"

	// reqStatTestPrefixedGroup is a ReqGroup whose name starts with
	// reqStatTestGroup's.
	reqStatTestPrefixedGroup = "rgz"

	// reqStatTestOtherGroup is a ReqGroup unrelated to reqStatTestGroup.
	reqStatTestOtherGroup = "other"

	// reqStatTestPromptly bounds a cache call that must not wait for a load.
	reqStatTestPromptly = 100 * time.Millisecond

	// reqStatTestRACIdleWait bounds the wait for the ready-added callback to
	// finish.
	reqStatTestRACIdleWait = 10 * time.Second
)

var errReqStatTestLoad = errors.New("injected load failure")

// reqStatLoads counts the loads of learned requirements per ReqGroup through
// reqStatLoadHook, makes the loads of the ReqGroups in failing fail, and calls
// onLoad, if set, during each load.
type reqStatLoads struct {
	mu      sync.Mutex
	counts  map[string]int
	failing map[string]bool
	onLoad  func(reqGroup string)
}

// countReqStatLoads installs a reqStatLoads as reqStatLoadHook. Call it before
// starting the server, which removes it once stopped.
func countReqStatLoads() *reqStatLoads {
	loads := &reqStatLoads{counts: make(map[string]int), failing: make(map[string]bool)}
	reqStatLoadHook = loads.hook

	return loads
}

func (l *reqStatLoads) hook(reqGroup string) error {
	l.mu.Lock()
	l.counts[reqGroup]++
	failing, onLoad := l.failing[reqGroup], l.onLoad
	l.mu.Unlock()

	if onLoad != nil {
		onLoad(reqGroup)
	}

	if failing {
		return errReqStatTestLoad
	}

	return nil
}

func (l *reqStatLoads) setOnLoad(onLoad func(reqGroup string)) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.onLoad = onLoad
}

// count returns how many loads of reqGroup have started since the last reset.
func (l *reqStatLoads) count(reqGroup string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.counts[reqGroup]
}

func (l *reqStatLoads) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.counts = make(map[string]int)
}

func (l *reqStatLoads) setFailing(reqGroup string, failing bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.failing[reqGroup] = failing
}

// TestReqStatCache covers spec B1: learned requirements are cached per
// ReqGroup and invalidated only when a committed stat write creates a new key.
func TestReqStatCache(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a server with no learned stats", t, func() {
		loads := countReqStatLoads()
		rt := newReqStatTest(ctx, t)
		server, db, jq := rt.server, rt.server.db, rt.jq

		Convey("a ReqGroup with no stats is not cached until loaded, then loads as zero", func() {
			rec, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(rec, ShouldBeNil)
			So(current, ShouldBeFalse)

			rec = db.refreshReqGroupRecs(reqStatTestGroup)
			So(loads.count(reqStatTestGroup), ShouldEqual, 1)
			So(rec, ShouldNotBeNil)
			So(rec.RAM, ShouldEqual, 0)
			So(rec.Disk, ShouldEqual, 0)
			So(rec.Time, ShouldEqual, 0)
			So(rec.DiskSet, ShouldBeTrue)

			cached, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(cached, ShouldEqual, rec)
			So(current, ShouldBeTrue)
		})

		Convey("a new stat key makes the loaded value not current, and the next refresh reloads it", func() {
			old := db.refreshReqGroupRecs(reqStatTestGroup)

			loads.reset()

			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)

			cached, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(cached, ShouldEqual, old)
			So(cached.RAM, ShouldEqual, 0)
			So(current, ShouldBeFalse)

			rec := db.refreshReqGroupRecs(reqStatTestGroup)
			So(loads.count(reqStatTestGroup), ShouldEqual, 1)
			So(rec.RAM, ShouldEqual, 300)
		})

		Convey("rewriting an existing stat key invalidates nothing", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
			db.refreshReqGroupRecs(reqStatTestGroup)
			loads.reset()

			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)

			cached, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(cached.RAM, ShouldEqual, 300)
			So(current, ShouldBeTrue)

			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)
			So(loads.count(reqStatTestGroup), ShouldEqual, 0)
		})

		Convey("archiving a job with a new peak RAM makes its ReqGroup not current by the time the archive returns", func() {
			seedReqGroupStats(server, bucketJobDisk, reqStatTestGroup, 0)
			seedReqGroupStats(server, bucketJobSecs, reqStatTestGroup, reqStatTestRuntimes()...)

			job := rt.reserveJob(0)

			db.refreshReqGroupRecs(reqStatTestGroup)

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)

			So(jq.Archive(job, &JobEndState{Exited: true, Exitcode: 0, PeakRAM: 700, EndTime: time.Now()}), ShouldBeNil)

			_, current = db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeFalse)
			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 700)
		})

		Convey("releasing a job for RAM makes its ReqGroup not current by the time the release returns", func() {
			job := rt.reserveJob(1)

			db.refreshReqGroupRecs(reqStatTestGroup)

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)

			So(jq.Release(job, &JobEndState{Exited: true, Exitcode: 1, PeakRAM: 900, EndTime: time.Now()},
				FailReasonRAM), ShouldBeNil)

			_, current = db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeFalse)
			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 900)
		})

		Convey("a load computes the RAC's learned requirements", func() {
			ram := make([]int, 0, 20)
			for v := 100; v <= 2000; v += 100 {
				ram = append(ram, v)
			}

			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, ram...)
			seedReqGroupStats(server, bucketJobDisk, reqStatTestGroup, 1500, 2500)
			seedReqGroupStats(server, bucketJobSecs, reqStatTestGroup, 30, 90)

			rec := db.refreshReqGroupRecs(reqStatTestGroup)

			recm, err := db.recommendedReqGroupMemory(reqStatTestGroup)
			So(err, ShouldBeNil)
			So(rec.RAM, ShouldEqual, recm)
			So(rec.RAM, ShouldEqual, 1500)

			recd, err := db.recommendedReqGroupDisk(reqStatTestGroup)
			So(err, ShouldBeNil)
			So(recd, ShouldEqual, 2500)
			So(rec.Disk, ShouldEqual, int(math.Ceil(float64(recd)/float64(mbPerGB))))
			So(rec.Disk, ShouldEqual, 3)
			So(rec.DiskSet, ShouldBeTrue)

			recs, err := db.recommendedReqGroupTime(reqStatTestGroup)
			So(err, ShouldBeNil)
			So(rec.Time, ShouldEqual, time.Duration(recs)*time.Second)
			So(rec.Time, ShouldEqual, 90*time.Second)
		})

		Convey("a failed load keeps the previous value, not current, until a load succeeds", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
			db.refreshReqGroupRecs(reqStatTestGroup)
			db.reqStats.invalidate(reqStatTestGroup)

			loads.setFailing(reqStatTestGroup, true)
			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)

			cached, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(cached.RAM, ShouldEqual, 300)
			So(current, ShouldBeFalse)

			loads.setFailing(reqStatTestGroup, false)
			loads.reset()
			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)
			So(loads.count(reqStatTestGroup), ShouldEqual, 1)

			_, current = db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)
		})

		Convey("a restarted server starts with nothing cached", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)

			disconnect(jq)
			server.Stop(ctx, true)

			serverConfig := rt.serverConfig
			serverConfig.dontWipeDevDB = true
			restarted, _, _, err := serve(ctx, serverConfig)
			So(err, ShouldBeNil)

			defer restarted.Stop(ctx, true)

			So(waitUntilRecovered(restarted), ShouldBeTrue)

			rec, current := restarted.db.cachedReqGroupRecs(reqStatTestGroup)
			So(rec, ShouldBeNil)
			So(current, ShouldBeFalse)

			So(restarted.db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)
		})

		Convey("a ReqGroup's values exclude those of a ReqGroup whose name it prefixes", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
			seedReqGroupStats(server, bucketJobRAM, reqStatTestPrefixedGroup, 5000)

			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)

			recm, err := db.recommendedReqGroupMemory(reqStatTestGroup)
			So(err, ShouldBeNil)
			So(recm, ShouldEqual, 300)

			So(db.refreshReqGroupRecs(reqStatTestPrefixedGroup).RAM, ShouldEqual, 5000)

			recm, err = db.recommendedReqGroupMemory(reqStatTestPrefixedGroup)
			So(err, ShouldBeNil)
			So(recm, ShouldEqual, 5000)
		})

		Convey("a new stat key of a ReqGroup whose name another prefixes leaves the other current", func() {
			db.refreshReqGroupRecs(reqStatTestGroup)
			db.refreshReqGroupRecs(reqStatTestPrefixedGroup)

			seedReqGroupStats(server, bucketJobRAM, reqStatTestPrefixedGroup, 800)

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)

			_, current = db.cachedReqGroupRecs(reqStatTestPrefixedGroup)
			So(current, ShouldBeFalse)
		})

		Convey("an invalidation during a load leaves the loaded value not current", func() {
			loads.setOnLoad(func(reqGroup string) { db.reqStats.invalidate(reqGroup) })

			db.refreshReqGroupRecs(reqStatTestGroup)

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeFalse)

			loads.setOnLoad(nil)
			db.refreshReqGroupRecs(reqStatTestGroup)

			_, current = db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)
		})

		Convey("each stat write that creates the only new key makes its ReqGroup not current", func() {
			aboveRAM := rt.standardReqs.RAM + 1000

			for _, tc := range []struct {
				name   string
				seed   func()
				report func(job *Job) error
			}{
				{
					name: "archive with a new peak disk",
					seed: func() {
						seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 100)
						seedReqGroupStats(server, bucketJobSecs, reqStatTestGroup, reqStatTestRuntimes()...)
					},
					report: func(job *Job) error {
						return jq.Archive(job, &JobEndState{Exited: true, PeakRAM: 100, PeakDisk: 2048, EndTime: time.Now()})
					},
				},
				{
					name: "archive with a new runtime",
					seed: func() {
						seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 100)
						seedReqGroupStats(server, bucketJobDisk, reqStatTestGroup, 0)
					},
					report: func(job *Job) error {
						return jq.Archive(job, &JobEndState{Exited: true, PeakRAM: 100, EndTime: time.Now()})
					},
				},
				{
					name: "release for disk",
					seed: func() {},
					report: func(job *Job) error {
						return jq.Release(job, &JobEndState{Exited: true, Exitcode: 1, PeakDisk: 2048, EndTime: time.Now()},
							FailReasonDisk)
					},
				},
				{
					name: "release for time",
					seed: func() {},
					report: func(job *Job) error {
						return jq.Release(job, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()}, FailReasonTime)
					},
				},
				{
					name: "release for another reason with a peak RAM above the job's",
					seed: func() {},
					report: func(job *Job) error {
						return jq.Release(job, &JobEndState{Exited: true, Exitcode: 1, PeakRAM: aboveRAM, EndTime: time.Now()},
							FailReasonExit)
					},
				},
			} {
				Convey(tc.name, func() {
					tc.seed()

					job := rt.reserveJob(1)

					db.refreshReqGroupRecs(reqStatTestGroup)

					_, current := db.cachedReqGroupRecs(reqStatTestGroup)
					So(current, ShouldBeTrue)

					So(tc.report(job), ShouldBeNil)

					_, current = db.cachedReqGroupRecs(reqStatTestGroup)
					So(current, ShouldBeFalse)
				})
			}
		})

		Convey("a load during an uncommitted new stat key is not current once it commits", func() {
			db.refreshReqGroupRecs(reqStatTestGroup)

			err := db.bolt.Update(func(tx *bolt.Tx) error {
				if err := db.reqStats.putStat(tx, bucketJobRAM, reqStatTestGroup, 300); err != nil {
					return err
				}

				done := make(chan *jqs.Requirements)

				go func() { done <- db.refreshReqGroupRecs(reqStatTestGroup) }()

				So((<-done).RAM, ShouldEqual, 0)

				return nil
			})
			So(err, ShouldBeNil)

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeFalse)
			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)
		})

		Convey("warming loads each distinct ReqGroup of the jobs once, even when a load fails", func() {
			loads.setFailing(reqStatTestGroup, true)

			db.warmReqGroupRecs([]*Job{
				{ReqGroup: reqStatTestGroup}, {ReqGroup: reqStatTestPrefixedGroup}, {ReqGroup: reqStatTestGroup},
			})

			So(loads.count(reqStatTestGroup), ShouldEqual, 1)
			So(loads.count(reqStatTestPrefixedGroup), ShouldEqual, 1)

			_, current := db.cachedReqGroupRecs(reqStatTestPrefixedGroup)
			So(current, ShouldBeTrue)
		})
	})
}

// newReqStatTest starts a reqStatTest, stopped when the current Convey leaf
// ends, after which reqStatLoadHook is removed. Each configure is applied to
// its ServerConfig before it starts.
func newReqStatTest(ctx context.Context, t *testing.T, configure ...func(*ServerConfig)) *reqStatTest {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.Timings.ReleaseDelayMin = time.Hour

	for _, c := range configure {
		c(&serverConfig)
	}

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
	So(err, ShouldBeNil)

	Reset(func() {
		disconnect(jq)
		server.Stop(ctx, true)

		reqStatLoadHook = nil
	})

	return &reqStatTest{server: server, serverConfig: serverConfig, jq: jq, standardReqs: standardReqs}
}

// seedReqGroupStats puts each of vals in bucket for reqGroup with the
// production stat write, in one transaction.
func seedReqGroupStats(server *Server, bucket []byte, reqGroup string, vals ...int) {
	err := server.db.bolt.Update(func(tx *bolt.Tx) error {
		for _, val := range vals {
			if err := server.db.reqStats.putStat(tx, bucket, reqGroup, val); err != nil {
				return err
			}
		}

		return nil
	})
	So(err, ShouldBeNil)
}

// reqStatTestRuntimes is every runtime, in seconds, up to an hour: seeded, it
// leaves a test's own short run no new runtime key to write.
func reqStatTestRuntimes() []int {
	secs := make([]int, 0, 3600)
	for s := 1; s <= 3600; s++ {
		secs = append(secs, s)
	}

	return secs
}

// TestReqStatRefreshLimit covers spec B2: each ReqGroup is loaded at most once
// per ReqStatRefreshInterval, by one load at a time, without blocking readers.
func TestReqStatRefreshLimit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("ReqStatRefreshInterval defaults to 1s, a negative one means no limit and a positive one is kept", t, func() {
		So(ServerTimings{}.withDefaults().ReqStatRefreshInterval, ShouldEqual, time.Second)
		So(ServerTimings{ReqStatRefreshInterval: -1}.withDefaults().ReqStatRefreshInterval, ShouldEqual, 0)
		So(ServerTimings{ReqStatRefreshInterval: 2 * time.Second}.withDefaults().ReqStatRefreshInterval,
			ShouldEqual, 2*time.Second)
	})

	Convey("Given a server with a 2s refresh interval and a ReqGroup just loaded at RAM 300", t, func() {
		loads := countReqStatLoads()
		rt := newReqStatTest(ctx, t, func(c *ServerConfig) { c.Timings.ReqStatRefreshInterval = 2 * time.Second })
		server, db := rt.server, rt.server.db

		seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
		So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 300)

		loadedBy := time.Now()

		loads.reset()

		Convey("new stat keys are not loaded until 2s after the last load started, then loaded once", func() {
			seedAndRefreshReqStatRAMs(db, server, func(rec *jqs.Requirements) {
				So(rec.RAM, ShouldEqual, 300)
			})
			So(time.Since(loadedBy), ShouldBeLessThan, 2*time.Second)
			So(loads.count(reqStatTestGroup), ShouldEqual, 0)

			time.Sleep(time.Until(loadedBy.Add(2 * time.Second)))

			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 700)
			So(loads.count(reqStatTestGroup), ShouldEqual, 1)

			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 700)
			So(loads.count(reqStatTestGroup), ShouldEqual, 1)
		})
	})

	Convey("Given a server built by jobqueueTestInit, whose refresh interval is -1", t, func() {
		loads := countReqStatLoads()
		rt := newReqStatTest(ctx, t)
		server, db := rt.server, rt.server.db

		Convey("every refresh after a new stat key loads", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
			db.refreshReqGroupRecs(reqStatTestGroup)
			loads.reset()

			var last *jqs.Requirements

			seedAndRefreshReqStatRAMs(db, server, func(rec *jqs.Requirements) { last = rec })
			So(loads.count(reqStatTestGroup), ShouldEqual, 4)
			So(last.RAM, ShouldEqual, 700)
		})

		Convey("20 concurrent background refreshes of a ReqGroup that is not current make one load", func() {
			_, release := blockReqStatLoads(loads, reqStatTestGroup)
			defer release()

			var callers sync.WaitGroup

			for range 20 {
				callers.Go(func() { db.refreshReqGroupRecsLater(reqStatTestGroup) })
			}

			returned := waitsWithin(callers.Wait, reqStatTestRACIdleWait)

			release()
			So(returned, ShouldBeTrue)

			db.reqStats.wg.Wait()

			So(loads.count(reqStatTestGroup), ShouldEqual, 1)

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)
		})

		Convey("a background refresh of a current ReqGroup starts nothing", func() {
			db.refreshReqGroupRecs(reqStatTestGroup)
			loads.reset()

			db.refreshReqGroupRecsLater(reqStatTestGroup)

			So(waitsWithin(db.reqStats.wg.Wait, reqStatTestPromptly), ShouldBeTrue)
			So(loads.count(reqStatTestGroup), ShouldEqual, 0)
		})

		Convey("Stop waits for a background load, and none starts once the database has closed", func() {
			started, release := blockReqStatLoads(loads, reqStatTestGroup)
			defer release()

			db.refreshReqGroupRecsLater(reqStatTestGroup)
			<-started

			disconnect(rt.jq)

			stopped := make(chan struct{})

			go func() {
				server.Stop(ctx, true)
				close(stopped)
			}()

			select {
			case <-stopped:
				So("Stop returned during the load", ShouldBeEmpty)
			case <-time.After(200 * time.Millisecond):
			}

			release()

			select {
			case <-stopped:
			case <-time.After(time.Second):
				So("Stop did not return within 1s of the load finishing", ShouldBeEmpty)
			}

			_, current := db.cachedReqGroupRecs(reqStatTestGroup)
			So(current, ShouldBeTrue)

			db.reqStats.invalidate(reqStatTestGroup)
			loads.reset()

			db.refreshReqGroupRecsLater(reqStatTestGroup)
			db.reqStats.wg.Wait()

			So(loads.count(reqStatTestGroup), ShouldEqual, 0)
		})

		Convey("a load in progress blocks no reader of the cache", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 300)
			db.refreshReqGroupRecs(reqStatTestGroup)
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 400)

			started, release := blockReqStatLoads(loads, reqStatTestGroup)
			defer release()

			db.refreshReqGroupRecsLater(reqStatTestGroup)
			<-started

			var (
				cached  *jqs.Requirements
				current bool
			)

			So(waitsWithin(func() { cached, current = db.cachedReqGroupRecs(reqStatTestGroup) },
				reqStatTestPromptly), ShouldBeTrue)
			So(cached.RAM, ShouldEqual, 300)
			So(current, ShouldBeFalse)

			var refreshed *jqs.Requirements

			So(waitsWithin(func() { refreshed = db.refreshReqGroupRecs(reqStatTestGroup) },
				reqStatTestPromptly), ShouldBeTrue)
			So(refreshed.RAM, ShouldEqual, 300)

			So(waitsWithin(func() { db.refreshReqGroupRecs(reqStatTestOtherGroup) }, reqStatTestPromptly), ShouldBeTrue)

			release()
			db.reqStats.wg.Wait()

			So(db.refreshReqGroupRecs(reqStatTestGroup).RAM, ShouldEqual, 400)
		})
	})
}

// seedAndRefreshReqStatRAMs seeds RAM 400, 500, 600 and 700 for
// reqStatTestGroup 40ms apart, calling refreshReqGroupRecs after each and
// passing its result to check.
func seedAndRefreshReqStatRAMs(db *db, server *Server, check func(rec *jqs.Requirements)) {
	for i, ram := range []int{400, 500, 600, 700} {
		if i > 0 {
			time.Sleep(40 * time.Millisecond)
		}

		seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, ram)
		check(db.refreshReqGroupRecs(reqStatTestGroup))
	}
}

// waitsWithin reports whether f returns within timeout. f keeps running in the
// background if it does not.
func waitsWithin(f func(), timeout time.Duration) bool {
	done := make(chan struct{})

	go func() {
		f()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// blockReqStatLoads makes loads of reqGroup block until release is called,
// which is safe to call more than once. started receives once a blocked load
// has begun.
func blockReqStatLoads(loads *reqStatLoads, reqGroup string) (<-chan struct{}, func()) {
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})

	loads.setOnLoad(func(rg string) {
		if rg != reqGroup {
			return
		}

		select {
		case started <- struct{}{}:
		default:
		}

		<-unblock
	})

	var once sync.Once

	return started, func() { once.Do(func() { close(unblock) }) }
}

// TestReqStatCacheRAC covers spec B3: the ready-added callback reads learned
// requirements through the cache, so it loads a ReqGroup only when its values
// may have changed.
func TestReqStatCacheRAC(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a paused server with 100 ready jobs of one ReqGroup", t, func() {
		const (
			jobCount   = 100
			addedRAM   = 10
			learnedRAM = 300
		)

		loads := countReqStatLoads()
		rt := newReqStatTest(ctx, t)
		server := rt.server

		// pause before adding, as TestReliable4RacBoundedBySchedulable does, so
		// the server's own racs return before loading anything.
		paused, err := server.Pause()
		So(err, ShouldBeNil)
		So(paused, ShouldBeTrue)

		jobs := make([]*Job, 0, jobCount)
		for i := range jobCount {
			jobs = append(jobs, &Job{
				Cmd: fmt.Sprintf("%s rac %d", restFormTrue, i), Cwd: testCwd, RepGroup: reqStatTestGroup,
				ReqGroup:     reqStatTestGroup,
				Requirements: &jqs.Requirements{RAM: addedRAM, Time: 10 * time.Second, Cores: 1},
			})
		}

		added, _, err := rt.jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(added, ShouldEqual, jobCount)

		So(pollUntil(func() bool {
			return server.racIdle() && len(server.q.AllItems()) == jobCount
		}), ShouldBeTrue)

		allitemdata := itemData(server.q.AllItems())

		loads.reset()

		Convey("two scheduling passes with no new stats load the ReqGroup at most once", func() {
			server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")
			server.buildSchedulerGroups(ctx, server.q, allitemdata, "true")

			So(loads.count(reqStatTestGroup), ShouldBeLessThanOrEqualTo, 1)
		})

		Convey("a new stat between two passes is loaded once by the second and applied to every job", func() {
			server.buildSchedulerGroups(ctx, server.q, allitemdata, "")
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, learnedRAM)
			loads.reset()

			server.buildSchedulerGroups(ctx, server.q, allitemdata, "")

			So(loads.count(reqStatTestGroup), ShouldEqual, 1)

			learned := 0

			for _, item := range server.q.AllItems() {
				job, ok := item.Data().(*Job)
				if !ok {
					continue
				}

				job.RLock()
				ram := job.Requirements.RAM
				job.RUnlock()

				if ram == learnedRAM {
					learned++
				}
			}

			So(learned, ShouldEqual, jobCount)
		})

		Convey("a new stat committed during a pass's load leaves every job of the ReqGroup one value that pass", func() {
			seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, learnedRAM)

			var seedOnce sync.Once

			loads.setOnLoad(func(reqGroup string) {
				if reqGroup != reqStatTestGroup {
					return
				}

				seedOnce.Do(func() { seedReqGroupStats(server, bucketJobRAM, reqStatTestGroup, 5000) })
			})

			server.buildSchedulerGroups(ctx, server.q, allitemdata, "")
			loads.setOnLoad(nil)

			So(loads.count(reqStatTestGroup), ShouldEqual, 1)

			rams := make(map[int]bool)

			for _, item := range server.q.AllItems() {
				job, ok := item.Data().(*Job)
				if !ok {
					continue
				}

				job.RLock()
				rams[job.Requirements.RAM] = true
				job.RUnlock()
			}

			So(len(rams), ShouldEqual, 1)
		})
	})
}

// reqStatTest is a durability-config server on its own database, with a
// connected client.
type reqStatTest struct {
	server       *Server
	serverConfig ServerConfig
	jq           *Client
	standardReqs *jqs.Requirements
}

// reserveJob adds one job of reqStatTestGroup, reserves it, reports
// it started and waits until no ready-added callback is pending or running, so
// none can load reqStatTestGroup after this returns.
func (r *reqStatTest) reserveJob(retries uint8) *Job {
	jq := r.jq

	inserts, _, err := jq.Add([]*Job{{
		Cmd: restFormTrue + " reqstat", Cwd: testCwd, RepGroup: reqStatTestGroup,
		ReqGroup: reqStatTestGroup, Retries: retries, Requirements: r.standardReqs,
	}}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	job, err := jq.Reserve(5 * time.Second)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)
	So(jq.Started(job, os.Getpid()), ShouldBeNil)

	So(pollUntilFor(reqStatTestRACIdleWait, r.server.racIdle), ShouldBeTrue)

	return job
}
