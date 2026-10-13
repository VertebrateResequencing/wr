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

// This file covers .docs/nohold/spec.md's Sections C to E: a job's
// requirements are made correct when it fails and when it becomes ready, so
// that no reserve can take it at a stale size.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
	bolt "go.etcd.io/bbolt"
)

const (
	// noHoldPeakRAM is the peak RAM of a failed run in these tests; a RAM
	// failure with it is bumped to noHoldBumpedRAM (max(150*2, 150+1000),
	// rounded up to 100).
	noHoldPeakRAM   = 150
	noHoldBumpedRAM = 1200

	noHoldReserveWait = 5 * time.Second
	noHoldLostWait    = 20 * time.Second
)

// statusPageOverLimitRE matches a status page binding that shows a value and
// highlights it when over its limit, capturing the shown value and the
// highlight's condition.
var statusPageOverLimitRE = regexp.MustCompile(`data-bind="text: ([^"]*?), css: \{ 'over-limit': ([^"]*?) \}"`)

// errModifyNeverReturned is what C3 test 10 records when Modify does not
// return in time.
var errModifyNeverReturned = errors.New("modify never returned")

// reserveReturnsWhileGated starts a reserve and reports whether it returned
// (and with what job key) within a second, while the caller keeps a callback
// gated.
//
//nolint:unused // spec E1's tests (phase 5) use it.
func reserveReturnsWhileGated(runner *Client) (bool, string, chan struct{}) {
	type outcome struct {
		job *Job
	}

	res := make(chan outcome, 1)
	done := make(chan struct{})

	go func() {
		job, _ := runner.Reserve(50 * time.Millisecond) //nolint:errcheck
		res <- outcome{job}

		close(done)
	}()

	select {
	case o := <-res:
		key := ""
		if o.job != nil {
			key = o.job.Key()
		}

		return true, key, done
	case <-time.After(time.Second):
		return false, "", done
	}
}

// noHoldLostRuns gives a 1s TTR, so a run its runner does not touch is lost
// within a second or two.
func noHoldLostRuns(c *ServerConfig) {
	c.Timings.ItemTTR = time.Second
}

// noHoldSlowRetry sets ReleaseDelayMin to 2s, so a released job's first retry
// delay is 2 to 4s.
func noHoldSlowRetry(c *ServerConfig) {
	c.Timings.ReleaseDelayMin = 2 * time.Second
}

// racGates holds each ready-added callback a test routes through it at its own
// gate until the test opens that gate. Callbacks beyond the gates it was made
// with pass straight through.
type racGates struct {
	mu     sync.Mutex
	calls  int
	called []chan struct{}
	gates  []chan struct{}
}

func newRACGates(n int) *racGates {
	g := &racGates{}
	for range n {
		g.called = append(g.called, make(chan struct{}))
		g.gates = append(g.gates, make(chan struct{}))
	}

	return g
}

func (g *racGates) enter() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	n := g.calls
	g.calls++

	if n < len(g.called) {
		close(g.called[n])
	}

	return n
}

func (g *racGates) wait(n int) {
	if n < len(g.gates) {
		<-g.gates[n]
	}
}

func (g *racGates) open(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	select {
	case <-g.gates[n]:
	default:
		close(g.gates[n])
	}
}

func (g *racGates) openAll() {
	for i := range g.gates {
		g.open(i)
	}
}

// noHoldTest is a durability-config manager on its own database, with a user
// client that adds and inspects jobs and a runner client that reserves them and
// reports how they ended.
type noHoldTest struct {
	t       *testing.T
	server  *Server
	config  ServerConfig
	jq      *Client
	runner  *Client
	reqs    *jqs.Requirements
	gates   *racGates
	connect func() *Client
	addr    string
	connTO  time.Duration
}

// newNoHoldTest starts a noHoldTest with spec Section C's defaults (ItemTTR 1m,
// no learned-value refresh limit), each configure applied after them. It is
// stopped when the current Convey leaf ends, after opening any RAC gate.
func newNoHoldTest(ctx context.Context, t *testing.T, configure ...func(*ServerConfig)) *noHoldTest {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReqStatRefreshInterval = -1

	for _, c := range configure {
		c(&serverConfig)
	}

	n := &noHoldTest{t: t, config: serverConfig, reqs: standardReqs, addr: addr, connTO: clientConnectTime}
	n.start(ctx)

	Reset(func() {
		if n.gates != nil {
			n.gates.openAll()
		}

		disconnect(n.runner)
		disconnect(n.jq)
		n.server.Stop(ctx, true)
	})

	return n
}

// start starts a manager on n's config and connects n's user and runner
// clients to it.
func (n *noHoldTest) start(ctx context.Context) {
	server, _, token, err := serve(ctx, n.config)
	So(err, ShouldBeNil)

	n.server = server
	n.connect = func() *Client {
		client, errc := Connect(n.addr, n.config.CAFile, n.config.CertDomain, token, n.connTO)
		So(errc, ShouldBeNil)

		return client
	}

	n.jq = n.connect()
	n.runner = n.connect()
}

// restart stops the manager and starts a new one on its database, returning
// once the new one has recovered.
func (n *noHoldTest) restart(ctx context.Context) {
	disconnect(n.runner)
	disconnect(n.jq)
	n.server.Stop(ctx, true)

	n.config.dontWipeDevDB = true
	n.start(ctx)

	So(waitUntilRecovered(n.server), ShouldBeTrue)
}

// addJob adds a job with standardReqs and a ReqGroup of its own, named after
// name, and returns it.
func (n *noHoldTest) addJob(name string, retries, override uint8) *Job {
	job := &Job{
		Cmd: restFormTrue + " " + name, Cwd: testCwd, RepGroup: "nohold-" + name,
		ReqGroup: "nohold-" + name, Requirements: n.reqs.Clone(), Retries: retries, Override: override,
	}

	inserts, _, err := n.jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	return job
}

// gateRAC waits until no ready-added callback is running or pending, then
// holds every later one at a gate that is only opened when the test ends.
func (n *noHoldTest) gateRAC(ctx context.Context) {
	So(pollUntil(n.server.racIdle), ShouldBeTrue)

	n.gates = newRACGates(1)

	n.server.q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
		i := n.gates.enter()
		n.gates.wait(i)
		n.server.readyAddedCallback(ctx, n.server.q, allitemdata)
	})
}

// reserveStarted has the runner reserve job (from the given scheduler group,
// or any job if group is "") and report it started with pid.
func (n *noHoldTest) reserveStarted(job *Job, group string, pid int) *Job {
	var (
		reserved *Job
		err      error
	)

	if group == "" {
		reserved, err = n.runner.Reserve(noHoldReserveWait)
	} else {
		reserved, err = n.runner.ReserveScheduled(noHoldReserveWait, group)
	}

	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, job.Key())
	So(n.runner.Started(reserved, pid), ShouldBeNil)

	return reserved
}

// failForRAM reserves job and reports it started, then releases it for using
// noHoldPeakRAM, returning the runner's copy of the job.
func (n *noHoldTest) failForRAM(job *Job) *Job {
	return n.failRun(job, FailReasonRAM, noHoldPeakRAM)
}

// failRun reserves job and reports it started, then releases it for
// failReason with the given peak RAM, returning the runner's copy of the job.
func (n *noHoldTest) failRun(job *Job, failReason string, peakRAM int) *Job {
	reserved := n.reserveStarted(job, "", os.Getpid())

	So(n.runner.Release(reserved, &JobEndState{
		Exited: true, Exitcode: 1, PeakRAM: peakRAM, EndTime: time.Now(),
	}, failReason), ShouldBeNil)

	return reserved
}

// get returns the manager's job as a client sees it.
func (n *noHoldTest) get(job *Job) *Job {
	got, err := n.jq.GetByEssence(&JobEssence{JobKey: job.Key()}, false, false)
	So(err, ShouldBeNil)
	So(got, ShouldNotBeNil)

	return got
}

// serverJob returns the manager's own job and its queue item.
func (n *noHoldTest) serverJob(key string) (*Job, *queue.Item) {
	item, err := n.server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	return job, item
}

// itemState returns the state of key's queue item, or "" if it has none.
func (n *noHoldTest) itemState(key string) queue.ItemState {
	item, err := n.server.q.Get(key)
	if err != nil || item == nil {
		return ""
	}

	return item.Stats().State
}

// loseRun has the runner reserve job (from group) and report it started with
// a dead pid, then waits for the manager to find the run lost, confirm it dead
// and release it itself. It returns the runner's copy of the job.
func (n *noHoldTest) loseRun(job *Job, group string) *Job {
	n.server.SetLostJobCheckTimeout(2 * time.Second)
	n.server.SetLostJobCheckRetryTime(200 * time.Millisecond)

	owned := n.reserveStarted(job, group, definitelyDeadPid(n.t))
	setServerJobRunnerPid(n.server, job.Key(), definitelyDeadPid(n.t))

	So(waitForJobLost(n.server, job.Key(), noHoldLostWait), ShouldBeTrue)
	So(pollUntilFor(noHoldLostWait, func() bool {
		_, _, failReason, ok := serverJobState(n.server, job.Key())
		state := n.itemState(job.Key())

		return ok && failReason == FailReasonLost &&
			(state == queue.ItemStateDelay || state == queue.ItemStateReady)
	}), ShouldBeTrue)

	return owned
}

// modifyReqs has the user modify job's requirements to reqs.
func (n *noHoldTest) modifyReqs(job *Job, reqs *jqs.Requirements) {
	jm := NewJobModifer()
	jm.SetRequirements(reqs)

	n.modify(job, jm)
}

// modify has the user apply jm to job.
func (n *noHoldTest) modify(job *Job, jm *JobModifier) {
	modified, err := n.jq.Modify([]*JobEssence{{JobKey: job.Key()}}, jm)
	So(err, ShouldBeNil)
	So(len(modified), ShouldEqual, 1)
}

// restModifyReqs modifies job's requirements to reqs through the REST API's
// modify path.
func (n *noHoldTest) restModifyReqs(ctx context.Context, job *Job, reqs *jqs.Requirements) {
	jm := NewJobModifer()
	jm.SetRequirements(reqs)

	modified, err := n.server.modifyJobsByKeys(ctx, []string{job.Key()}, jm)
	So(err, ShouldBeNil)
	So(len(modified), ShouldEqual, 1)
}

// kickAndReserve kicks the buried job and has the runner reserve it,
// returning the runner's copy.
func (n *noHoldTest) kickAndReserve(job *Job) *Job {
	kicked, err := n.jq.Kick([]*JobEssence{{JobKey: job.Key()}})
	So(err, ShouldBeNil)
	So(kicked, ShouldEqual, 1)

	return n.reserve(job, noHoldReserveWait)
}

// reserve has the runner reserve job within wait, returning the runner's copy.
func (n *noHoldTest) reserve(job *Job, wait time.Duration) *Job {
	reserved, err := n.runner.Reserve(wait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, job.Key())

	return reserved
}

// waitReady waits until key's queue item is ready.
func (n *noHoldTest) waitReady(key string) {
	So(pollUntil(func() bool { return n.itemState(key) == queue.ItemStateReady }), ShouldBeTrue)
}

// seedLowLearning seeds RAM stats 1 to 40 for name's ReqGroup, so that the
// job's run reserves a learned 100 and, with its own stat of 150 added, the
// learned RAM is still 100, which does not cover a peak of 150.
func (n *noHoldTest) seedLowLearning(name string) {
	vals := make([]int, 0, 40)
	for v := range 40 {
		vals = append(vals, v+1)
	}

	seedReqGroupStats(n.server, bucketJobRAM, "nohold-"+name, vals...)
}

// serverJobRAM returns the RAM of the manager's own job for key.
func (n *noHoldTest) serverJobRAM(key string) int {
	sjob, _ := n.serverJob(key)

	sjob.RLock()
	defer sjob.RUnlock()

	return sjob.Requirements.RAM
}

// TestNoHoldFailTimeBumps covers spec C1 tests 1 to 7: a release or bury
// applies the failure's increase as it moves the job's item out of Run.
func TestNoHoldFailTimeBumps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job with 3 retries that fails for RAM with the RAC gated and a 2s release delay", t, func() {
		n := newNoHoldTest(ctx, t, noHoldSlowRetry)
		job := n.addJob("c1-1", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		owned := n.failForRAM(job)

		Convey("it is at once delayed at the bumped RAM, and a reserve takes it at that RAM", func() {
			got := n.get(job)
			So(got.State, ShouldEqual, JobStateDelayed)
			So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
			So(got.FailedRunReqs, ShouldNotBeNil)
			So(got.FailedRunReqs.RAM, ShouldEqual, 10)

			other := n.connect()
			defer disconnect(other)

			reserved, err := other.Reserve(8 * time.Second)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, job.Key())
			So(reserved.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		})

		Convey("its stored record has the bumped RAM once the release has returned", func() {
			So(storedLiveJob(t, n.server.db, job.Key()).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		})

		Convey("the runner re-sending the same release changes neither its RAM nor its retries", func() {
			So(n.runner.Release(owned, &JobEndState{
				Exited: true, Exitcode: 1, PeakRAM: noHoldPeakRAM, EndTime: time.Now(),
			}, FailReasonRAM), ShouldBeNil)

			got := n.get(job)
			So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
			So(got.UntilBuried, ShouldEqual, 3)
		})
	})

	Convey("Given a job with 3 retries whose release has no delay, its item enters ready already at the "+
		"bumped RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c1-1-zero-delay", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		reserved := n.reserveStarted(job, "", os.Getpid())
		So(n.server.q.SetDelay(job.Key(), 0), ShouldBeNil)

		// the queue calls its ready hook under its lock, as the item is pushed
		// into ready, so this sees what any reserve could first see.
		var (
			mu        sync.Mutex
			readyRAMs []int
		)

		n.server.q.SetReadyHook(func(data any, reserveGroup string) string {
			if sjob, ok := data.(*Job); ok {
				sjob.RLock()
				ram := sjob.Requirements.RAM
				sjob.RUnlock()

				mu.Lock()

				readyRAMs = append(readyRAMs, ram)
				mu.Unlock()
			}

			return reserveGroup
		})

		So(n.runner.Release(reserved, &JobEndState{
			Exited: true, Exitcode: 1, PeakRAM: noHoldPeakRAM, EndTime: time.Now(),
		}, FailReasonRAM), ShouldBeNil)

		mu.Lock()
		defer mu.Unlock()

		So(readyRAMs, ShouldResemble, []int{noHoldBumpedRAM})
	})

	Convey("Given a job with no retries, when it fails for RAM, it is buried at the bumped RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c1-2", 0, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)

		got := n.get(job)
		So(got.State, ShouldEqual, JobStateBuried)
		So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
	})

	Convey("Given a job with 3 retries, when it is released for disk with PeakDisk 2048, its disk is 100", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c1-3", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		reserved := n.reserveStarted(job, "", os.Getpid())
		So(n.runner.Release(reserved, &JobEndState{
			Exited: true, Exitcode: 1, PeakDisk: 2048, EndTime: time.Now(),
		}, FailReasonDisk), ShouldBeNil)

		So(n.get(job).Requirements.Disk, ShouldEqual, 100)
	})

	Convey("Given a job with 3 retries, when it is released for time after a 10s run, its time is 1h0m10s", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c1-4", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		reserved := n.reserveStarted(job, "", os.Getpid())
		started := n.get(job).StartTime
		So(started.IsZero(), ShouldBeFalse)

		So(n.runner.Release(reserved, &JobEndState{
			Exited: true, Exitcode: 1, EndTime: started.Add(10 * time.Second),
		}, FailReasonTime), ShouldBeNil)

		So(n.get(job).Requirements.Time, ShouldEqual, time.Hour+10*time.Second)
	})

	Convey("Given a job with override 2, when it fails for RAM, its RAM stays 10", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c1-5", 3, jobOverrideAlwaysUseJobReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)

		So(n.get(job).Requirements.RAM, ShouldEqual, 10)
	})
}

// TestNoHoldOwnerReportAfterLost covers spec C1 tests 9 to 11 and 13: the
// owner's report of a run the manager already released as lost applies that
// failure's increase, keeping a user's later modification.
func TestNoHoldOwnerReportAfterLost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	time2h := 2 * time.Hour

	Convey("Given a job released as lost, its time modified to 2h and then ready, the owner's RAM report "+
		"bumps its RAM and keeps the user's time", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns)
		job := n.addJob("c1-9", 3, jobOverridePreferSystemReqs)

		owned := n.loseRun(job, "")
		n.modifyReqs(job, &jqs.Requirements{Time: time2h})
		So(pollUntil(func() bool { return n.itemState(job.Key()) == queue.ItemStateReady }), ShouldBeTrue)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)

		So(n.runner.Release(owned, ownerRAMEndState(), FailReasonRAM), ShouldBeNil)

		// the report's write carries the increase, so a restart keeps it
		So(storedLiveJob(t, n.server.db, job.Key()).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

		got := n.get(job)
		So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(got.Requirements.Time, ShouldEqual, time2h)
		So(got.FailBumpsCleared, ShouldEqual, FailBumpTime)

		other := n.connect()
		defer disconnect(other)

		reserved, err := other.Reserve(noHoldReserveWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())
		So(reserved.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(reserved.Requirements.Time, ShouldEqual, time2h)
	})

	Convey("Given the same with a runner command, the owner's RAM report puts the ready item in the bumped "+
		"RAM's group", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns, noHoldRunnerCmd(serverRC))
		job := n.addJob("c1-10", 3, jobOverridePreferSystemReqs)

		owned := n.loseRun(job, schedulerGroupString(reqForScheduler(n.reqs), nil))
		n.modifyReqs(job, &jqs.Requirements{Time: time2h})
		So(pollUntil(func() bool { return n.itemState(job.Key()) == queue.ItemStateReady }), ShouldBeTrue)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)

		So(n.runner.Release(owned, ownerRAMEndState(), FailReasonRAM), ShouldBeNil)

		got := n.get(job)
		So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

		want := schedulerGroupString(reqForScheduler(got.Requirements), nil)
		sjob, item := n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, want)
		So(sjob.getSchedulerGroup(), ShouldEqual, want)
	})

	Convey("Given a job released as lost and delayed, with its time modified to 2h, the owner's RAM bury "+
		"buries it at the bumped RAM and the user's time", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns, noHoldSlowRetry)
		job := n.addJob("c1-11", 3, jobOverridePreferSystemReqs)

		owned := n.loseRun(job, "")
		n.modifyReqs(job, &jqs.Requirements{Time: time2h})
		So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateDelay)

		So(n.runner.Bury(owned, ownerRAMEndState(), FailReasonRAM), ShouldBeNil)

		got := n.get(job)
		So(got.State, ShouldEqual, JobStateBuried)
		So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(got.Requirements.Time, ShouldEqual, time2h)
		So(got.FailBumpsCleared, ShouldEqual, FailBumpTime)
	})

	Convey("Given learned RAM 200, a job released as lost and its RAM modified to 500, the owner's RAM "+
		"report bumps its RAM, since the learned value replaced the user's under override 0", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns)
		seedReqGroupStats(n.server, bucketJobRAM, "nohold-c1-13", 200)
		job := n.addJob("c1-13", 3, jobOverridePreferSystemReqs)

		owned := n.loseRun(job, "")
		So(owned.Requirements.RAM, ShouldEqual, 200)

		n.modifyReqs(job, &jqs.Requirements{RAM: 500})
		So(pollUntil(func() bool { return n.itemState(job.Key()) == queue.ItemStateReady }), ShouldBeTrue)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)

		So(n.runner.Release(owned, ownerRAMEndState(), FailReasonRAM), ShouldBeNil)

		got := n.get(job)
		So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(got.FailBumpsCleared, ShouldEqual, FailBumpRAM)
	})
}

// ownerRAMEndState is the owner's report of a run that used noHoldPeakRAM.
func ownerRAMEndState() *JobEndState {
	return &JobEndState{Exited: true, Exitcode: 1, PeakRAM: noHoldPeakRAM}
}

// noHoldRunnerCmd sets the runner command.
func noHoldRunnerCmd(rc string) func(*ServerConfig) {
	return func(c *ServerConfig) { c.RunnerCmd = rc }
}

// TestNoHoldHighPeakBumps covers spec C2 tests 1 to 4 and 7 to 9: a non-RAM
// failure whose peak exceeded the run's RAM has its RAM bumped when it fails,
// whether it is buried or retried.
func TestNoHoldHighPeakBumps(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given low learning and a job with no retries that exceeded its RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		n.seedLowLearning("c2-1")
		job := n.addJob("c2-1", 0, jobOverridePreferSystemReqs)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)

		n.failRun(job, FailReasonExit, noHoldPeakRAM)

		Convey("it is buried at the bumped RAM, and kicked a reserve takes it at that RAM", func() {
			got := n.get(job)
			So(got.State, ShouldEqual, JobStateBuried)
			So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

			kicked, err := n.jq.Kick([]*JobEssence{{JobKey: job.Key()}})
			So(err, ShouldBeNil)
			So(kicked, ShouldEqual, 1)

			reserved, err := n.runner.Reserve(noHoldReserveWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, job.Key())
			So(reserved.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		})
	})

	Convey("Given low learning and a job with no retries that stayed within its RAM, kicked it has the "+
		"learned RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		n.seedLowLearning("c2-2")
		job := n.addJob("c2-2", 0, jobOverridePreferSystemReqs)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)

		n.failRun(job, FailReasonExit, 5)
		So(n.get(job).State, ShouldEqual, JobStateBuried)

		kicked, err := n.jq.Kick([]*JobEssence{{JobKey: job.Key()}})
		So(err, ShouldBeNil)
		So(kicked, ShouldEqual, 1)

		reserved, err := n.runner.Reserve(noHoldReserveWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())
		So(reserved.Requirements.RAM, ShouldEqual, 100)
	})

	Convey("Given low learning and a 2s release delay, a retried job that exceeded its RAM is at once "+
		"delayed at the bumped RAM, and its item still has it once ready", t, func() {
		n := newNoHoldTest(ctx, t, noHoldSlowRetry)
		n.seedLowLearning("c2-3")
		job := n.addJob("c2-3", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failRun(job, FailReasonExit, noHoldPeakRAM)

		got := n.get(job)
		So(got.State, ShouldEqual, JobStateDelayed)
		So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(got.FailedRunReqs, ShouldNotBeNil)
		So(got.FailedRunReqs.RAM, ShouldEqual, 100)

		So(pollUntil(func() bool { return n.itemState(job.Key()) == queue.ItemStateReady }), ShouldBeTrue)
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)
	})

	Convey("Given a started job released as lost by its runner, its RAM stays 10", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c2-4", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		reserved := n.reserveStarted(job, "", os.Getpid())
		So(n.runner.Release(reserved, nil, FailReasonLost), ShouldBeNil)

		So(n.get(job).Requirements.RAM, ShouldEqual, 10)
	})

	Convey("Given a retried job that exceeded its RAM, its high-peak stat is recorded although its RAM was "+
		"already bumped", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c2-7", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failRun(job, FailReasonExit, noHoldPeakRAM)
		So(n.get(job).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

		So(n.server.db.bolt.View(func(tx *bolt.Tx) error {
			key := fmt.Appendf(nil, "%s%s%20d", job.ReqGroup, dbDelimiter, noHoldPeakRAM)
			So(tx.Bucket(bucketJobRAM).Get(key), ShouldNotBeNil)

			return nil
		}), ShouldBeNil)

		learned, err := n.server.db.recommendedReqGroupMemory(job.ReqGroup)
		So(err, ShouldBeNil)
		So(learned, ShouldEqual, 200)
	})

	for _, override := range []uint8{jobOverridePreferSystemReqs, jobOverridePreferHigherReqs} {
		Convey(fmt.Sprintf("Given no prior stats, override %d and a 2s release delay, a retried job that "+
			"exceeded its RAM is delayed at the bumped RAM and then learns its own stat", override), t, func() {
			n := newNoHoldTest(ctx, t, noHoldSlowRetry)
			name := fmt.Sprintf("c2-8-override%d", override)
			job := n.addJob(name, 3, override)
			n.gateRAC(ctx)

			n.failRun(job, FailReasonExit, noHoldPeakRAM)

			got := n.get(job)
			So(got.State, ShouldEqual, JobStateDelayed)
			So(got.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

			So(pollUntil(func() bool { return n.itemState(job.Key()) == queue.ItemStateReady }), ShouldBeTrue)
			So(pollUntil(func() bool {
				n.server.db.refreshReqGroupRecs(job.ReqGroup)
				_, current := n.server.db.cachedReqGroupRecs(job.ReqGroup)

				return current
			}), ShouldBeTrue)

			sjob, _ := n.serverJob(job.Key())
			n.server.buildSchedulerGroups(ctx, n.server.q, []any{sjob}, "")

			So(n.serverJobRAM(job.Key()), ShouldEqual, 200)
		})
	}
}

// TestNoHoldStatusPageOverLimit covers the status page's over-limit highlight
// of a failed job whose requirements were raised when it failed: its usage is
// still judged against what its failed run reserved.
func TestNoHoldStatusPageOverLimit(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given buried jobs that used more RAM, time or disk than their run reserved", t, func() {
		n := newNoHoldTest(ctx, t)

		ramJob := n.addJob("status-ram", 0, jobOverridePreferSystemReqs)
		timeJob := n.addJob("status-time", 0, jobOverridePreferSystemReqs)

		n.reqs.Disk = 1
		n.reqs.DiskSet = true
		diskJob := n.addJob("status-disk", 0, jobOverridePreferSystemReqs)

		n.gateRAC(ctx)

		n.failForRAM(ramJob)

		reserved := n.reserveStarted(timeJob, "", os.Getpid())
		started := n.get(timeJob).StartTime
		So(n.runner.Release(reserved, &JobEndState{
			Exited: true, Exitcode: 1, EndTime: started.Add(time.Minute),
		}, FailReasonTime), ShouldBeNil)

		reserved = n.reserveStarted(diskJob, "", os.Getpid())
		So(n.runner.Release(reserved, &JobEndState{
			Exited: true, Exitcode: 1, PeakDisk: 2048, EndTime: time.Now(),
		}, FailReasonDisk), ShouldBeNil)

		status := func(job *Job) JStatus {
			sjob, _ := n.serverJob(job.Key())

			js, err := sjob.ToStatus()
			So(err, ShouldBeNil)

			return js
		}

		Convey("a RAM failure shows its raised RAM as expected, and its peak is still flagged over the "+
			"limit", func() {
			ram := status(ramJob)
			So(ram.ExpectedRAM, ShouldEqual, noHoldBumpedRAM)

			over := statusPageFlagsOverLimit(t, ram, "window.wrUtils.mbIEC(PeakRAM)")
			So(over, ShouldBeTrue)
		})

		Convey("a time failure shows its raised time as expected, and its walltime is still flagged over "+
			"the limit", func() {
			tm := status(timeJob)
			So(tm.ExpectedTime, ShouldBeGreaterThan, tm.Walltime)

			over := statusPageFlagsOverLimit(t, tm, "window.wrUtils.toDuration(Walltime)")
			So(over, ShouldBeTrue)
		})

		Convey("a disk failure shows its raised disk as requested, and its peak is still flagged over the "+
			"limit", func() {
			disk := status(diskJob)
			So(disk.RequestedDisk, ShouldBeGreaterThan, 2)

			over := statusPageFlagsOverLimit(t, disk, "PeakDisk === 0 ? '0GB' : window.wrUtils.mbIEC(PeakDisk)")
			So(over, ShouldBeTrue)
		})
	})

	Convey("Given a job that failed for RAM and whose retry is reserved at the raised RAM, its usage is "+
		"judged against the retry's RAM, not the failed run's", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("status-retry", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)
		retry := n.reserveStarted(job, "", os.Getpid())
		So(retry.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

		sjob, _ := n.serverJob(job.Key())

		js, err := sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.RunRAM, ShouldEqual, noHoldBumpedRAM)

		js.PeakRAM = noHoldPeakRAM
		So(statusPageFlagsOverLimit(t, js, "window.wrUtils.mbIEC(PeakRAM)"), ShouldBeFalse)
	})
}

// statusPageFlagsOverLimit evaluates, against status, the status page's
// over-limit condition of the first binding that shows text, as the browser
// would for an exited job, and says whether it holds.
func statusPageFlagsOverLimit(t *testing.T, status JStatus, text string) bool {
	t.Helper()

	repoRoot := repoRootForWebUITest(t)

	page, err := os.ReadFile(filepath.Join(repoRoot, "jobqueue/static/status.html"))
	So(err, ShouldBeNil)

	condition := ""

	for _, m := range statusPageOverLimitRE.FindAllStringSubmatch(string(page), -1) {
		if m[1] == text {
			condition = m[2]

			break
		}
	}

	So(condition, ShouldNotBeEmpty)

	statusJSON, err := json.Marshal(status)
	So(err, ShouldBeNil)

	conditionJSON, err := json.Marshal(condition)
	So(err, ShouldBeNil)

	if _, errl := exec.LookPath("node"); errl != nil {
		t.Skip("node is required to evaluate the status page's bindings")
	}

	script := filepath.Join(t.TempDir(), "over_limit.mjs")
	So(os.WriteFile(script, fmt.Appendf(nil, `const job = %s;
job.LiveWalltime = () => job.Walltime;
const f = new Function(...Object.keys(job), 'return (' + %s + ');');
process.stdout.write(f(...Object.values(job)) ? 'over' : 'within');
`, statusJSON, conditionJSON), 0600), ShouldBeNil)

	out, err := exec.CommandContext(context.Background(), "node", script).CombinedOutput()
	So(err, ShouldBeNil)

	return string(out) == "over"
}

// TestNoHoldModifyClearsBump covers spec C3: a size a user sets with wr mod is
// used when the job next runs, instead of being raised again for its last
// failure.
func TestNoHoldModifyClearsBump(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	ram500 := &jqs.Requirements{RAM: 500}

	Convey("Given a job with override 1 and no retries buried after failing for RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c3-1", 0, jobOverridePreferHigherReqs)

		n.failForRAM(job)
		So(n.get(job).State, ShouldEqual, JobStateBuried)

		Convey("when Modify sets RAM 500, it keeps 500 and a kicked reserve takes it at 500", func() {
			n.modifyReqs(job, ram500)

			got := n.get(job)
			So(got.Requirements.RAM, ShouldEqual, 500)
			So(got.FailBumpsCleared, ShouldEqual, FailBumpRAM)
			So(got.ModifiedReqs, ShouldNotBeNil)
			So(got.ModifiedReqs.RAM, ShouldEqual, 500)

			reserved := n.kickAndReserve(job)
			So(reserved.Requirements.RAM, ShouldEqual, 500)

			Convey("when it then fails for RAM with a peak of 600, its RAM is 1600 and the user's value is "+
				"forgotten", func() {
				So(n.runner.Started(reserved, os.Getpid()), ShouldBeNil)
				So(n.runner.Release(reserved, &JobEndState{
					Exited: true, Exitcode: 1, PeakRAM: 600, EndTime: time.Now(),
				}, FailReasonRAM), ShouldBeNil)

				got := n.get(job)
				So(got.Requirements.RAM, ShouldEqual, 1600)
				So(got.FailBumpsCleared, ShouldEqual, 0)
				So(got.ModifiedReqs, ShouldBeNil)
			})
		})

		Convey("when Modify sets only time 2h, a kicked reserve takes it at the bumped RAM and 2h", func() {
			n.modifyReqs(job, &jqs.Requirements{Time: 2 * time.Hour})

			reserved := n.kickAndReserve(job)
			So(reserved.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
			So(reserved.Requirements.Time, ShouldEqual, 2*time.Hour)
		})

		Convey("when Modify sets RAM 500 and the manager restarts, a kicked reserve takes it at 500", func() {
			n.modifyReqs(job, ram500)
			n.restart(ctx)

			So(n.kickAndReserve(job).Requirements.RAM, ShouldEqual, 500)
		})

		Convey("when the REST path modifies its RAM to 500, it keeps 500 and a kicked reserve takes it at "+
			"500", func() {
			n.restModifyReqs(ctx, job, ram500)

			got := n.get(job)
			So(got.Requirements.RAM, ShouldEqual, 500)
			So(got.FailBumpsCleared, ShouldEqual, FailBumpRAM)

			So(n.kickAndReserve(job).Requirements.RAM, ShouldEqual, 500)
		})
	})

	Convey("Given a 2s release delay and a job with override 1 and 3 retries delayed after failing for RAM, "+
		"when Modify sets RAM 500, a reserve takes it at 500 once its delay expires", t, func() {
		n := newNoHoldTest(ctx, t, noHoldSlowRetry)
		job := n.addJob("c3-3", 3, jobOverridePreferHigherReqs)

		n.failForRAM(job)
		So(n.get(job).State, ShouldEqual, JobStateDelayed)

		n.modifyReqs(job, ram500)

		So(n.reserve(job, 8*time.Second).Requirements.RAM, ShouldEqual, 500)
	})

	Convey("Given a runner command, the RAC gated and a ready job with no stats", t, func() {
		n := newNoHoldTest(ctx, t, noHoldRunnerCmd(serverRC))
		job := n.addJob("c3-5", 0, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		_, item := n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, schedulerGroupString(reqForScheduler(n.reqs), nil))

		want := n.reqs.Clone()
		want.RAM = 2000
		wantGroup := schedulerGroupString(reqForScheduler(want), nil)

		Convey("when Modify sets RAM 2000, its item and job are in the RAM 2000 group when Modify "+
			"returns", func() {
			n.modifyReqs(job, &jqs.Requirements{RAM: 2000})

			sjob, item := n.serverJob(job.Key())
			So(item.ReserveGroup, ShouldEqual, wantGroup)
			So(sjob.getSchedulerGroup(), ShouldEqual, wantGroup)
		})

		Convey("when the REST path modifies its RAM to 2000, its item and job are in the RAM 2000 group "+
			"when that returns", func() {
			n.restModifyReqs(ctx, job, &jqs.Requirements{RAM: 2000})

			sjob, item := n.serverJob(job.Key())
			So(item.ReserveGroup, ShouldEqual, wantGroup)
			So(sjob.getSchedulerGroup(), ShouldEqual, wantGroup)
		})
	})

	Convey("Given override 0 and a job with no retries buried after failing for RAM, when Modify sets only "+
		"override 2, a kicked reserve takes it at its original RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c3-8-ram", 0, jobOverridePreferSystemReqs)

		n.failForRAM(job)
		So(n.get(job).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

		n.modify(job, overrideModifier(jobOverrideAlwaysUseJobReqs))

		So(n.kickAndReserve(job).Requirements.RAM, ShouldEqual, 10)
	})

	Convey("Given override 0 and a job with no retries buried after failing for time after a 10s run, when "+
		"Modify sets only override 2, a kicked reserve takes it at its original time", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c3-8-time", 0, jobOverridePreferSystemReqs)

		reserved := n.reserveStarted(job, "", os.Getpid())
		started := n.get(job).StartTime
		So(n.runner.Release(reserved, &JobEndState{
			Exited: true, Exitcode: 1, EndTime: started.Add(10 * time.Second),
		}, FailReasonTime), ShouldBeNil)
		So(n.get(job).Requirements.Time, ShouldEqual, time.Hour+10*time.Second)

		n.modify(job, overrideModifier(jobOverrideAlwaysUseJobReqs))

		So(n.kickAndReserve(job).Requirements.Time, ShouldEqual, 10*time.Second)
	})

	Convey("Given override 0 and a job with no retries buried after failing for RAM, when Modify sets RAM "+
		"500, a kicked reserve takes it at the bumped RAM, since its learned 200 replaced the user's 500",
		t, func() {
			n := newNoHoldTest(ctx, t)
			job := n.addJob("c3-9", 0, jobOverridePreferSystemReqs)

			n.failForRAM(job)
			n.modifyReqs(job, ram500)

			So(n.kickAndReserve(job).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		})

	Convey("Given the RAC gated and a retried job, now ready, whose RAM a high peak bumped, when Modify "+
		"sets only override 2, its RAM is its original RAM when Modify returns", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("c3-override2-ready", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failRun(job, FailReasonExit, noHoldPeakRAM)
		n.waitReady(job.Key())
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)

		n.modify(job, overrideModifier(jobOverrideAlwaysUseJobReqs))

		So(n.serverJobRAM(job.Key()), ShouldEqual, 10)
		So(n.get(job).Requirements.RAM, ShouldEqual, 10)
	})

	Convey("Given a 500ms release delay and a job delayed after failing for RAM, when Modify changes its "+
		"command while its delay expires, Modify returns and the job is ready under its new key", t, func() {
		n := newNoHoldTest(ctx, t, func(c *ServerConfig) { c.Timings.ReleaseDelayMin = 500 * time.Millisecond })
		job := n.addJob("c3-10", 3, jobOverridePreferSystemReqs)

		n.failForRAM(job)

		_, item := n.serverJob(job.Key())
		So(item.Stats().State, ShouldEqual, queue.ItemStateDelay)

		// the delay expires, and from phase 4 the queue's ready hook tries to
		// lock the job, while modifyJob is about to check the new key.
		readyAt := item.ReadyAt()
		modifyKeyCheckHook = func() { time.Sleep(time.Until(readyAt.Add(200 * time.Millisecond))) }

		defer func() { modifyKeyCheckHook = nil }()

		jm := NewJobModifer()
		jm.SetCmd(job.Cmd + " modified")

		type outcome struct {
			modified map[string]string
			err      error
		}

		done := make(chan outcome, 1)

		go func() {
			modified, err := n.jq.Modify([]*JobEssence{{JobKey: job.Key()}}, jm)
			done <- outcome{modified, err}
		}()

		var got outcome

		select {
		case got = <-done:
		case <-time.After(5 * time.Second):
			got.err = errModifyNeverReturned
		}

		So(got.err, ShouldBeNil)
		So(len(got.modified), ShouldEqual, 1)

		newKey := ""
		for key := range got.modified {
			newKey = key
		}

		So(newKey, ShouldNotEqual, job.Key())
		n.waitReady(newKey)

		sjob, item := n.serverJob(newKey)
		So(sjob.Cmd, ShouldEqual, job.Cmd+" modified")
		So(item.ReserveGroup, ShouldEqual, "")
		So(n.serverJobRAM(newKey), ShouldEqual, noHoldBumpedRAM)
	})
}

// overrideModifier returns a modifier that sets only the override.
func overrideModifier(override uint8) *JobModifier {
	jm := NewJobModifer()
	jm.SetOverride(override)

	return jm
}
