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

// Spec D3 tests 2 to 6: a job that became ready at an unlearned size picks up
// the size a later completion of its ReqGroup teaches, through the RAC, even
// while it is blocked behind its full limit group (item 6.3b of
// .docs/nohold/phase6.md). Learning comes from real archives, and a RAC is
// known to have run by counting the callbacks that finished.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	learnChangeUserRAM    = 600 // the jobs' own RAM, what they get unlearned
	learnChangePeakRAM    = 150 // a completion's peak; learned as 200
	learnChangeLearnedRAM = 200
	learnChangeHigherPeak = 250 // with 150, learned as 300
	learnChangeHigherRAM  = 300
	learnChangeModRAM     = 400  // a user's wr mod
	learnChangeBumpedRAM  = 1200 // a RAM failure with peak 150
	learnChangeReqGroup   = "nohold-learnchange"
	learnChangeNoStats    = "nohold-learnchange-none"
	learnChangeLimit      = "nohold-lc"
	learnChangeLimitOne   = learnChangeLimit + ":1"
)

const (
	// stampRaceTrials is how many times TestNoHoldLearnedStampRace sets up its
	// race. Each trial refreshes its two ReqGroups in map order, and only one
	// order meets the race, so a stamp read after the refresh fails about half
	// of them.
	stampRaceTrials = 20

	stampRaceRAM        = 100
	stampRaceLearnedRAM = 500
	stampRaceLimit      = "nohold-stamprace:0"
	stampRaceJobs       = 2
	stampRaceWait       = 5 * time.Second
)

const (
	reqGroupModFrom  = "nohold-reqgroupmod-from"
	reqGroupModTo    = "nohold-reqgroupmod-to"
	reqGroupModLimit = "nohold-reqgroupmod:0"
)

type learnChangeTest struct {
	server   *Server
	jq       *Client
	runner   *Client
	reqs     *jqs.Requirements
	finished atomic.Int64
	gated    atomic.Pointer[chan struct{}]
	entered  atomic.Pointer[chan struct{}]
}

// newLearnChangeTest starts a manager with runner command serverRC and no
// learned-value refresh limit, each configure applied after that, whose RAC is
// wrapped to count finished callbacks and to be held by gate. It is stopped
// when the current Convey leaf ends.
func newLearnChangeTest(ctx context.Context, t *testing.T, configure ...func(*ServerConfig)) *learnChangeTest {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReqStatRefreshInterval = -1
	serverConfig.RunnerCmd = serverRC

	for _, c := range configure {
		c(&serverConfig)
	}

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	lc := &learnChangeTest{server: server, reqs: standardReqs.Clone()}
	lc.reqs.RAM = learnChangeUserRAM

	q := server.q
	q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
		if gate := lc.gated.Load(); gate != nil {
			if entered := lc.entered.Swap(nil); entered != nil {
				close(*entered)
			}

			<-*gate
		}

		server.readyAddedCallback(ctx, q, allitemdata)
		lc.finished.Add(1)
	})

	connect := func() *Client {
		client, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		return client
	}

	lc.jq = connect()
	lc.runner = connect()

	Reset(func() {
		lc.openGate()
		disconnect(lc.runner)
		disconnect(lc.jq)
		server.Stop(ctx, true)
	})

	return lc
}

// gate holds every later RAC until openGate, once no RAC is running, and
// returns a channel closed once a held RAC has its snapshot of the ready jobs.
func (lc *learnChangeTest) gate(ctx context.Context) <-chan struct{} {
	lc.racAfter(ctx)

	entered := make(chan struct{})
	lc.entered.Store(&entered)

	gate := make(chan struct{})
	lc.gated.Store(&gate)

	return entered
}

func (lc *learnChangeTest) openGate() {
	if gate := lc.gated.Swap(nil); gate != nil {
		close(*gate)
	}
}

// job returns a job of the test's ReqGroup with the given override and limit
// groups.
func (lc *learnChangeTest) job(name string, override uint8, limitGroups ...string) *Job {
	return lc.jobOf(learnChangeReqGroup, name, override, limitGroups...)
}

func (lc *learnChangeTest) jobOf(reqGroup, name string, override uint8, limitGroups ...string) *Job {
	return &Job{
		Cmd: restFormTrue + " " + name, Cwd: testCwd, RepGroup: reqGroup,
		ReqGroup: reqGroup, Requirements: lc.reqs.Clone(),
		Override: override, LimitGroups: limitGroups, Retries: 3,
	}
}

func (lc *learnChangeTest) add(jobs ...*Job) {
	inserts, _, err := lc.jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(jobs))
}

func (lc *learnChangeTest) group(ram int, limitGroups ...string) string {
	req := lc.reqs.Clone()
	req.RAM = ram

	return schedulerGroupString(reqForScheduler(req), limitGroups)
}

// serverJob returns the manager's RAM and scheduler group for key, and its
// item's reserve group.
func (lc *learnChangeTest) serverJob(key string) (int, string, string) {
	item, err := lc.server.q.Get(key)
	So(err, ShouldBeNil)

	sjob, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	sjob.RLock()
	ram := sjob.Requirements.RAM
	sjob.RUnlock()

	return ram, sjob.getSchedulerGroup(), item.ReserveGroup
}

// reserveStarted reserves from group and reports it started.
func (lc *learnChangeTest) reserveStarted(group string) *Job {
	reserved, err := lc.runner.ReserveScheduled(5*time.Second, group)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(lc.runner.Started(reserved, os.Getpid()), ShouldBeNil)

	return reserved
}

func (lc *learnChangeTest) archive(job *Job, peakRAM int) {
	So(lc.runner.Archive(job, &JobEndState{
		Exited: true, Exitcode: 0, PeakRAM: peakRAM, EndTime: time.Now(),
	}), ShouldBeNil)
}

// learn reserves a job of group and archives it with peakRAM, so its ReqGroup
// learns from it.
func (lc *learnChangeTest) learn(group string, peakRAM int) {
	lc.archive(lc.reserveStarted(group), peakRAM)
}

// racAfter triggers a RAC and waits until one that started after the call has
// finished: the count of finished callbacks has grown by 2 (one may have been
// running already and finished without seeing the change).
func (lc *learnChangeTest) racAfter(ctx context.Context) {
	before := lc.finished.Load()

	lc.server.q.TriggerReadyAddedCallback(ctx)

	So(pollUntilFor(5*time.Second, func() bool {
		if lc.finished.Load() >= before+2 {
			return true
		}

		lc.server.q.TriggerReadyAddedCallback(ctx)

		return false
	}), ShouldBeTrue)
}

func (lc *learnChangeTest) other(reserved, a, b *Job) *Job {
	if reserved.Key() == a.Key() {
		return b
	}

	return a
}

// derivations returns how often the manager's job for key has derived its
// scheduler group.
func (lc *learnChangeTest) derivations(key string) uint32 {
	item, err := lc.server.q.Get(key)
	So(err, ShouldBeNil)

	sjob, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	sjob.RLock()
	defer sjob.RUnlock()

	return sjob.derivations
}

// TestNoHoldLearningAfterReady covers spec D3 tests 2 to 6.
func TestNoHoldLearningAfterReady(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner command and jobs of one ReqGroup ready at an unlearned 600", t, func() {
		lc := newLearnChangeTest(ctx, t)
		unlearned, learned := lc.group(learnChangeUserRAM), lc.group(learnChangeLearnedRAM)
		blockedOld := lc.group(learnChangeUserRAM, learnChangeLimit)
		blockedNew := lc.group(learnChangeLearnedRAM, learnChangeLimit)

		Convey("once one completes with peak 150, a RAC moves the other to the learned 200 and its group", func() {
			a, b := lc.job("a", jobOverridePreferSystemReqs), lc.job("b", jobOverridePreferSystemReqs)
			lc.add(a, b)

			reserved := lc.reserveStarted(unlearned)
			So(reserved.Requirements.RAM, ShouldEqual, learnChangeUserRAM)
			waiting := lc.other(reserved, a, b)

			lc.archive(reserved, learnChangePeakRAM)
			lc.racAfter(ctx)

			ram, group, _ := lc.serverJob(waiting.Key())
			So(ram, ShouldEqual, learnChangeLearnedRAM)
			So(group, ShouldEqual, learned)

			got := lc.reserveStarted(learned)
			So(got.Key(), ShouldEqual, waiting.Key())
			So(got.Requirements.RAM, ShouldEqual, learnChangeLearnedRAM)
		})

		Convey("with a job holding a limit group's only slot", func() {
			a, b := lc.job("a", jobOverridePreferSystemReqs, learnChangeLimitOne),
				lc.job("b", jobOverridePreferSystemReqs, learnChangeLimitOne)
			lc.add(a, b)

			holder := lc.reserveStarted(blockedOld)
			blocked := lc.other(holder, a, b)

			noStats := lc.jobOf(learnChangeNoStats, "c", jobOverridePreferSystemReqs, learnChangeLimitOne)
			learner := lc.job("learner", jobOverridePreferSystemReqs)
			lc.add(noStats, learner)

			Convey("(red) a RAC after learning moves the blocked job to the learned 200 and its group, "+
				"and a runner of that group then reserves it at 200", func() {
				regroupedBefore := lc.server.racRegrouped.Load()

				lc.learn(unlearned, learnChangePeakRAM)
				lc.racAfter(ctx)

				ram, group, reserveGroup := lc.serverJob(blocked.Key())
				So(ram, ShouldEqual, learnChangeLearnedRAM)
				So(group, ShouldEqual, blockedNew)
				So(reserveGroup, ShouldEqual, blockedNew)

				// the job of a ReqGroup with no stats is left as it was
				So(lc.server.racRegrouped.Load()-regroupedBefore, ShouldEqual, 1)

				ram, group, reserveGroup = lc.serverJob(noStats.Key())
				So(ram, ShouldEqual, learnChangeUserRAM)
				So(group, ShouldEqual, blockedOld)
				So(reserveGroup, ShouldEqual, blockedOld)

				Convey("further RACs with nothing newly learned recompute and regroup nothing", func() {
					regrouped := lc.server.racRegrouped.Load()
					derivations := lc.derivations(blocked.Key())

					lc.racAfter(ctx)
					lc.racAfter(ctx)

					So(lc.server.racRegrouped.Load(), ShouldEqual, regrouped)
					So(lc.server.racScanWork.Load(), ShouldEqual, 0)
					So(lc.derivations(blocked.Key()), ShouldEqual, derivations)
				})

				Convey("once the slot holder completes, a runner of the learned group reserves it at 200", func() {
					lc.archive(holder, learnChangePeakRAM)

					got := lc.reserveStarted(blockedNew)
					So(got.Key(), ShouldEqual, blocked.Key())
					So(got.Requirements.RAM, ShouldEqual, learnChangeLearnedRAM)
				})
			})

			Convey("a blocked job reserved after a RAC's snapshot is not resized or regrouped by that RAC", func() {
				entered := lc.gate(ctx)

				lc.learn(unlearned, learnChangePeakRAM)
				lc.server.q.TriggerReadyAddedCallback(ctx)

				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					So("ready-added callback was not held", ShouldBeEmpty)
				}

				lc.archive(holder, learnChangePeakRAM)

				got := lc.reserveStarted(blockedOld)
				So(got.Key(), ShouldEqual, blocked.Key())
				So(got.Requirements.RAM, ShouldEqual, learnChangeUserRAM)

				lc.openGate()
				So(pollUntil(lc.server.racIdle), ShouldBeTrue)

				ram, group, _ := lc.serverJob(blocked.Key())
				So(ram, ShouldEqual, learnChangeUserRAM)
				So(group, ShouldEqual, blockedOld)
			})
		})

		Convey("a blocked job's wr mod size is kept under override 1 when learning changes", func() {
			holder := lc.job("holder", jobOverridePreferSystemReqs, learnChangeLimitOne)
			lc.add(holder)
			lc.reserveStarted(blockedOld)

			modded := lc.job("modded", jobOverridePreferHigherReqs, learnChangeLimitOne)
			learner := lc.job("learner", jobOverridePreferSystemReqs)
			lc.add(modded, learner)

			jm := NewJobModifer()
			jm.SetRequirements(&jqs.Requirements{RAM: learnChangeModRAM})
			mods, err := lc.jq.Modify([]*JobEssence{{JobKey: modded.Key()}}, jm)
			So(err, ShouldBeNil)
			So(len(mods), ShouldEqual, 1)

			modGroup := lc.group(learnChangeModRAM, learnChangeLimit)
			_, _, reserveGroup := lc.serverJob(modded.Key())
			So(reserveGroup, ShouldEqual, modGroup)

			lc.learn(unlearned, learnChangePeakRAM)
			lc.racAfter(ctx)

			ram, group, reserveGroup := lc.serverJob(modded.Key())
			So(ram, ShouldEqual, learnChangeModRAM)
			So(group, ShouldEqual, modGroup)
			So(reserveGroup, ShouldEqual, modGroup)
		})
	})

	Convey("Given a blocked job whose RAM a failure raised, when learning changes, the RAC keeps the increase", t, func() {
		lc := newLearnChangeTest(ctx, t, noHoldSlowRetry)
		bumpedGroup := lc.group(learnChangeBumpedRAM, learnChangeLimit)

		failed := lc.job("failed", jobOverridePreferSystemReqs, learnChangeLimitOne)
		lc.add(failed)

		run := lc.reserveStarted(lc.group(learnChangeUserRAM, learnChangeLimit))
		So(lc.runner.Release(run, &JobEndState{
			Exited: true, Exitcode: 1, PeakRAM: learnChangePeakRAM, EndTime: time.Now(),
		}, FailReasonRAM), ShouldBeNil)

		// the failure taught the ReqGroup 200, so the holder is of a ReqGroup
		// with no stats, to keep the unlearned group
		holder := lc.jobOf(learnChangeNoStats, "holder", jobOverridePreferSystemReqs, learnChangeLimitOne)
		learner := lc.job("learner", jobOverridePreferSystemReqs)
		lc.add(holder, learner)

		got := lc.reserveStarted(lc.group(learnChangeUserRAM, learnChangeLimit))
		So(got.Key(), ShouldEqual, holder.Key())

		So(pollUntilFor(10*time.Second, func() bool {
			item, err := lc.server.q.Get(failed.Key())

			return err == nil && item.Stats().State == queue.ItemStateReady
		}), ShouldBeTrue)

		lc.learn(lc.group(learnChangeLearnedRAM), learnChangeHigherPeak)
		lc.racAfter(ctx)

		ram, group, reserveGroup := lc.serverJob(failed.Key())
		So(ram, ShouldEqual, learnChangeBumpedRAM)
		So(group, ShouldEqual, bumpedGroup)
		So(reserveGroup, ShouldEqual, bumpedGroup)
	})
}

// TestNoHoldLearnedReqGroupModified covers spec D3 test 8: a wr mod that moves
// a limit-blocked job to a ReqGroup no blocked job had, whose learned values
// round the same, must not leave the job marked current, so a later change to
// that ReqGroup's learned values still resizes it.
func TestNoHoldLearnedReqGroupModified(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a blocked job found current, modified to a ReqGroup with the same (no) learned values", t, func() {
		lc := newLearnChangeTest(ctx, t)

		job := lc.jobOf(reqGroupModFrom, "modded", jobOverridePreferSystemReqs, reqGroupModLimit)
		lc.add(job)
		lc.racAfter(ctx)

		jm := NewJobModifer()
		jm.SetReqGroup(reqGroupModTo)
		mods, err := lc.jq.Modify([]*JobEssence{{JobKey: job.Key()}}, jm)
		So(err, ShouldBeNil)
		So(len(mods), ShouldEqual, 1)

		So(pollUntil(func() bool {
			lc.server.db.refreshReqGroupRecs(reqGroupModTo)
			_, current := lc.server.db.cachedReqGroupRecs(reqGroupModTo)

			return current
		}), ShouldBeTrue)

		lc.racAfter(ctx)

		Convey("when that ReqGroup then learns a new RAM, a RAC resizes the job", func() {
			seedReqGroupStats(lc.server, bucketJobRAM, reqGroupModTo, stampRaceLearnedRAM)

			lc.racAfter(ctx)
			lc.racAfter(ctx)

			ram, _, _ := lc.serverJob(job.Key())
			So(ram, ShouldEqual, stampRaceLearnedRAM)
		})
	})
}

// stampRaceTrial is the ReqGroups of one trial of TestNoHoldLearnedStampRace,
// and the channel its first ReqGroup's held load waits on.
type stampRaceTrial struct {
	held, other string
	release     chan struct{}
	once        sync.Once
	server      *Server
}

// load is the trial's part of reqStatLoadHook: a load of held waits until a
// load of other starts, which lets it go and waits for it to finish, so held's
// learned values change while a RAC refreshes other.
func (tr *stampRaceTrial) load(reqGroup string) {
	switch reqGroup {
	case tr.held:
		<-tr.release
	case tr.other:
		tr.once.Do(func() { close(tr.release) })

		pollUntilFor(stampRaceWait, func() bool {
			_, current := tr.server.db.cachedReqGroupRecs(tr.held)

			return current
		})
	}
}

// TestNoHoldLearnedStampRace covers spec D3 test 7: a background load that
// changes a ReqGroup's learned values while a RAC refreshes the blocked
// ReqGroups must not leave that ReqGroup's blocked jobs marked current at
// their old size.
func TestNoHoldLearnedStampRace(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given limit-blocked jobs of two ReqGroups and a load of one held until the other's starts", t, func() {
		var trial atomic.Pointer[stampRaceTrial]

		reqStatLoadHook = func(reqGroup string) error {
			if tr := trial.Load(); tr != nil {
				tr.load(reqGroup)
			}

			return nil
		}

		_, serverConfig, addr, _, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ReqStatRefreshInterval = -1

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		paused, err := server.Pause()
		So(err, ShouldBeNil)
		So(paused, ShouldBeTrue)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		Reset(func() {
			disconnect(jq)
			server.Stop(ctx, true)

			reqStatLoadHook = nil
		})

		Convey("RACs after the load resize every blocked job, whichever ReqGroup they refresh first", func() {
			stuck := 0

			for i := range stampRaceTrials {
				tr := &stampRaceTrial{
					held: fmt.Sprintf("nohold-stamprace-held%d", i), other: fmt.Sprintf("nohold-stamprace-other%d", i),
					release: make(chan struct{}), server: server,
				}

				jobs := racBoundJobs(tr.held, stampRaceRAM, 0, stampRaceLimit, stampRaceJobs)
				jobs = append(jobs, racBoundJobs(tr.other, stampRaceRAM, 0, stampRaceLimit, stampRaceJobs)...)

				added, _, errAdd := jq.Add(jobs, envVars, true)
				So(errAdd, ShouldBeNil)
				So(added, ShouldEqual, len(jobs))

				data := make([]any, 0, len(jobs))
				for _, job := range jobs {
					item, errg := server.q.Get(job.Key())
					So(errg, ShouldBeNil)

					data = append(data, item.Data())
				}

				// a cycle with nothing new finds the jobs current
				server.buildSchedulerGroups(ctx, server.q, data, "true")

				seedReqGroupStats(server, bucketJobRAM, tr.held, stampRaceLearnedRAM)
				seedReqGroupStats(server, bucketJobRAM, tr.other, stampRaceLearnedRAM)

				trial.Store(tr)
				server.db.refreshReqGroupRecsLater(tr.held)

				for range 3 {
					server.buildSchedulerGroups(ctx, server.q, data, "true")
				}

				trial.Store(nil)

				for _, d := range data[:stampRaceJobs] {
					job, ok := d.(*Job)
					So(ok, ShouldBeTrue)

					job.RLock()
					ram := job.Requirements.RAM
					job.RUnlock()

					if ram != stampRaceLearnedRAM {
						stuck++

						break
					}
				}
			}

			So(stuck, ShouldEqual, 0)
		})
	})
}
