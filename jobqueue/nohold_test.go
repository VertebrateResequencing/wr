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
	"strings"
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

	// noHoldLearnedRAM is the RAM Section D's tests seed for a job's ReqGroup
	// ("learned 300"): seeded alone, it is also the recommendation.
	noHoldLearnedRAM = 300

	// noHoldNewerLearnedRAM is the RAM spec D3's test seeds after a job was
	// prepared at noHoldLearnedRAM; seeded alongside it, it is the new
	// recommendation.
	noHoldNewerLearnedRAM = 800

	// noHoldChildCmd is appended to a test's command for a second job of its
	// ReqGroup.
	noHoldChildCmd = " child"

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

const (
	// readyPreparedReqGroup is the ReqGroup of the jobs of a
	// readyPreparedProbe, other than its RAM-failed job.
	readyPreparedReqGroup = "racprobe"

	// readyPreparedGates is how many ready-added callbacks a readyPreparedProbe
	// gates: a callback and its recall, as the probe's P2 has them, and one more.
	readyPreparedGates = 3
)

// noHoldFastRetry sets ReleaseDelayMin to 10ms, so a released job is soon ready
// again.
func noHoldFastRetry(c *ServerConfig) {
	c.Timings.ReleaseDelayMin = 10 * time.Millisecond
}

// noHoldQuickShutdown sets ShutdownRunnerWait to 1s, so stopping the manager
// does not wait out a touch interval for runners its scheduler may have been
// asked for.
func noHoldQuickShutdown(c *ServerConfig) {
	c.Timings.ShutdownRunnerWait = time.Second
}

// readyPreparedProbe is the setup of the racpending probe that spec E1's tests
// keep: a parent reserved and never finished, and a suspended child waiting on
// it, so resuming the child sends it back to dependent (P1's early release on
// the base), with learned RAM noHoldLearnedRAM for their ReqGroup.
type readyPreparedProbe struct {
	t      *testing.T
	server *Server
	jq     *Client
	runner *Client
	reqs   *jqs.Requirements
	child  *Job
	gates  *racGates
}

// newReadyPreparedProbe starts a manager with no runner command and no
// learned-value refresh limit, and sets up the probe's parent and child. It
// is stopped when the current Convey leaf ends, after opening the gates.
func newReadyPreparedProbe(ctx context.Context, t *testing.T) *readyPreparedProbe {
	t.Helper()

	_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
	serverConfig.Timings.ItemTTR = time.Minute
	serverConfig.Timings.ReqStatRefreshInterval = -1

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	p := &readyPreparedProbe{t: t, server: server, reqs: standardReqs}
	connect := func() *Client {
		client, errc := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(errc, ShouldBeNil)

		return client
	}
	p.jq, p.runner = connect(), connect()

	Reset(func() {
		if p.gates != nil {
			p.gates.openAll()
		}

		disconnect(p.runner)
		disconnect(p.jq)
		server.Stop(ctx, true)
	})

	parent := p.newJob("parent", 3)
	parent.DepGroups = []string{"racprobe-parent"}
	p.child = p.newJob("child", 3)
	p.child.Dependencies = Dependencies{NewDepGroupDependency("racprobe-parent")}
	p.add(parent, p.child)

	got, err := p.jq.Reserve(time.Second)
	So(err, ShouldBeNil)
	So(got, ShouldNotBeNil)
	So(got.Key(), ShouldEqual, parent.Key())

	suspended, err := p.jq.Suspend([]*JobEssence{{JobKey: p.child.Key()}})
	So(err, ShouldBeNil)
	So(suspended, ShouldEqual, 1)

	seedReqGroupStats(server, bucketJobRAM, readyPreparedReqGroup, noHoldLearnedRAM)

	return p
}

// newJob returns a job of the probe's ReqGroup named after name.
func (p *readyPreparedProbe) newJob(name string, retries uint8) *Job {
	return &Job{
		Cmd: restFormTrue + " " + name, Cwd: testCwd, RepGroup: readyPreparedReqGroup,
		ReqGroup: readyPreparedReqGroup, Requirements: p.reqs.Clone(), Retries: retries,
	}
}

// add has the user add jobs, all of which must be new.
func (p *readyPreparedProbe) add(jobs ...*Job) {
	inserts, _, err := p.jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(jobs))
}

// gate waits until no ready-added callback is running or queued, then holds
// each of the next readyPreparedGates at its own gate.
func (p *readyPreparedProbe) gate(ctx context.Context) {
	So(pollUntil(p.server.racIdle), ShouldBeTrue)

	p.gates = newRACGates(readyPreparedGates)
	gates, server, q := p.gates, p.server, p.server.q

	q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
		i := gates.enter()
		gates.wait(i)
		server.readyAddedCallback(ctx, q, allitemdata)
	})
}

// awaitCalled waits until the ready-added callback has reached gate n.
func (p *readyPreparedProbe) awaitCalled(n int) {
	select {
	case <-p.gates.called[n]:
	case <-time.After(noHoldReserveWait):
		p.t.Fatalf("ready-added callback %d was not called", n)
	}
}

// resumeChild resumes the child, which goes back to dependent.
func (p *readyPreparedProbe) resumeChild() {
	resumed, err := p.jq.Resume([]*JobEssence{{JobKey: p.child.Key()}})
	So(err, ShouldBeNil)
	So(resumed, ShouldEqual, 1)
}

// buryForRAM adds a job with no retries and a ReqGroup of its own (so nothing
// is learned for it), has the runner reserve it and report it failed for using
// noHoldPeakRAM, and waits until it is buried.
func (p *readyPreparedProbe) buryForRAM() *Job {
	job := p.newJob("ramfail", 0)
	job.ReqGroup = "ramfail"
	p.add(job)

	reserved, err := p.runner.Reserve(noHoldReserveWait)
	So(err, ShouldBeNil)
	So(reserved, ShouldNotBeNil)
	So(reserved.Key(), ShouldEqual, job.Key())
	So(p.runner.Started(reserved, os.Getpid()), ShouldBeNil)
	So(p.runner.Release(reserved, &JobEndState{
		Exited: true, Exitcode: 1, PeakRAM: noHoldPeakRAM, EndTime: time.Now(),
	}, FailReasonRAM), ShouldBeNil)

	So(pollUntil(func() bool {
		item, errg := p.server.q.Get(job.Key())

		return errg == nil && item.Stats().State == queue.ItemStateBury
	}), ShouldBeTrue)

	return job
}

// kick has the user kick job.
func (p *readyPreparedProbe) kick(job *Job) {
	kicked, err := p.jq.Kick([]*JobEssence{{JobKey: job.Key()}})
	So(err, ShouldBeNil)
	So(kicked, ShouldEqual, 1)
}

// reservesAtOnce asserts that a reserve made now returns want within a second,
// with the given RAM, while the ready-added callback stays gated.
func (p *readyPreparedProbe) reservesAtOnce(want *Job, ram int) {
	returned, got := reserveReturnsWhileGated(p.runner)
	So(returned, ShouldBeTrue)
	So(got, ShouldNotBeNil)
	So(got.Key(), ShouldEqual, want.Key())
	So(got.Requirements.RAM, ShouldEqual, ram)
}

// reserveReturnsWhileGated starts a reserve and reports whether it returned
// (and with what job, nil if none) within a second, while the caller keeps a
// callback gated.
func reserveReturnsWhileGated(runner *Client) (bool, *Job) {
	res := make(chan *Job, 1)

	go func() {
		job, _ := runner.Reserve(50 * time.Millisecond) //nolint:errcheck
		res <- job
	}()

	select {
	case job := <-res:
		return true, job
	case <-time.After(time.Second):
		return false, nil
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

// noHoldServerRC sets the runner command to serverRC.
func noHoldServerRC(c *ServerConfig) {
	c.RunnerCmd = serverRC
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

// TestNoHoldReadyPrepared covers spec E1 tests 1 to 6, the racpending probe
// with its assertion inverted: a reserve no longer waits for the ready-added
// callback, and the job it gets is already prepared, whatever would have
// released the old hold early.
func TestNoHoldReadyPrepared(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a reserved parent, a suspended child waiting on it and learned RAM 300", t, func() {
		p := newReadyPreparedProbe(ctx, t)
		newJob := p.newJob("new", 3)

		Convey("control: a reserve gets a new add at once, prepared, while its callback is gated", func() {
			p.gate(ctx)
			p.add(newJob)
			p.awaitCalled(0)

			p.reservesAtOnce(newJob, noHoldLearnedRAM)
		})

		Convey("P1: a resume to dependent after the add changes nothing", func() {
			p.gate(ctx)
			p.add(newJob)
			p.awaitCalled(0)
			p.resumeChild()

			p.reservesAtOnce(newJob, noHoldLearnedRAM)
		})

		Convey("P2: an add merged into a recall of a running callback is reservable at once, prepared", func() {
			p.gate(ctx)
			p.server.triggerReadyAddedCallback(ctx)
			p.awaitCalled(0)

			p.add(newJob)

			p.gates.open(0)
			p.awaitCalled(1)

			p.reservesAtOnce(newJob, noHoldLearnedRAM)
		})

		Convey("a job added while paused is prepared, not reservable until resumed, then reservable", func() {
			p.gate(ctx)

			paused, err := p.server.Pause()
			So(err, ShouldBeNil)
			So(paused, ShouldBeTrue)

			p.add(newJob)

			got, err := p.jq.GetByEssence(&JobEssence{JobKey: newJob.Key()}, false, false)
			So(err, ShouldBeNil)
			So(got, ShouldNotBeNil)
			So(got.Requirements.RAM, ShouldEqual, noHoldLearnedRAM)

			idle, err := p.runner.Reserve(10 * time.Millisecond)
			So(err, ShouldBeNil)
			So(idle, ShouldBeNil)

			resumed, err := p.server.Resume(ctx)
			So(err, ShouldBeNil)
			So(resumed, ShouldBeTrue)

			p.reservesAtOnce(newJob, noHoldLearnedRAM)
		})

		Convey("Given a job with no retries buried after failing for RAM", func() {
			ramFailed := p.buryForRAM()
			p.gate(ctx)

			Convey("a reserve gets it at the bumped RAM at once when it is kicked", func() {
				p.kick(ramFailed)
				p.awaitCalled(0)

				p.reservesAtOnce(ramFailed, noHoldBumpedRAM)
			})

			Convey("a resume to dependent after the kick changes nothing", func() {
				p.kick(ramFailed)
				p.awaitCalled(0)
				p.resumeChild()

				p.reservesAtOnce(ramFailed, noHoldBumpedRAM)
			})
		})
	})
}

// TestNoHoldRACCountsReservedOnce covers item 5.4 of the nohold phase 5 plan:
// a job a runner reserves after the ready-added callback took its ready
// snapshot, and before it counts the running jobs, is counted once for what it
// is when the callback counts, whatever its run did meanwhile.
func TestNoHoldRACCountsReservedOnce(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner command and the RAC gated after its snapshot of a ready job", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC, noHoldSlowRetry, noHoldLostRuns)
		group := n.groupFor(n.reqs.RAM)

		n.gateRAC(ctx)
		job := n.addJob("raconce", 3, 0)
		n.awaitGatedRAC()

		reserved := n.reserveStarted(job, group, os.Getpid())

		Convey("a reserve of it still running when the callback counts is counted once", func() {
			n.openGatedRAC()
			So(n.server.currentGroupCount(group), ShouldEqual, 1)
		})

		Convey("its run lost when the callback counts is counted once", func() {
			So(waitForJobLost(n.server, job.Key(), noHoldLostWait), ShouldBeTrue)
			So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateRun)

			n.openGatedRAC()
			So(n.server.currentGroupCount(group), ShouldEqual, 1)
		})

		Convey("its run archived before the callback counts is not counted", func() {
			So(n.runner.Archive(reserved, &JobEndState{Exited: true, EndTime: time.Now()}), ShouldBeNil)

			n.openGatedRAC()
			So(n.server.currentGroupCount(group), ShouldEqual, 0)
		})

		Convey("its run released to delayed before the callback counts is not counted", func() {
			So(n.runner.Release(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()},
				FailReasonExit), ShouldBeNil)
			So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateDelay)

			n.openGatedRAC()
			So(n.server.currentGroupCount(group), ShouldEqual, 0)
		})

		Convey("its run buried before the callback counts is not counted", func() {
			So(n.runner.Bury(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()},
				FailReasonExit), ShouldBeNil)
			So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateBury)

			n.openGatedRAC()
			So(n.server.currentGroupCount(group), ShouldEqual, 0)
		})
	})

	Convey("Given a runner command and the RAC gated after its snapshot of a ready job in each of three "+
		"scheduler groups, two reserved before the callback counts, each group counts its one job", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)

		rams := []int{n.reqs.RAM, noHoldLearnedRAM, noHoldNewerLearnedRAM}
		jobs := make([]*Job, 0, len(rams))

		for i, ram := range rams {
			job := n.newJob("racgroups", 0, 0)
			job.Cmd += strings.Repeat(noHoldChildCmd, i)
			job.Requirements.RAM = ram
			jobs = append(jobs, job)
		}

		n.gateRAC(ctx)
		n.add(jobs...)
		n.awaitGatedRAC()

		n.reserveStarted(jobs[0], n.groupFor(rams[0]), os.Getpid())
		n.reserveStarted(jobs[1], n.groupFor(rams[1]), os.Getpid())

		n.openGatedRAC()

		for _, ram := range rams {
			So(n.server.currentGroupCount(n.groupFor(ram)), ShouldEqual, 1)
		}
	})

	Convey("Given a runner command and the RAC gated after its snapshot of 2 ready jobs of a limit group "+
		"of 5, one reserved before the callback counts, their group counts 2", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		group := n.groupFor(n.reqs.RAM, "nohold-lc")

		job := n.newJob("raclimit", 0, 0)
		job.LimitGroups = []string{"nohold-lc:5"}
		other := n.newJob("raclimit", 0, 0)
		other.Cmd += noHoldChildCmd
		other.LimitGroups = []string{"nohold-lc"}

		n.gateRAC(ctx)
		n.add(job, other)
		n.awaitGatedRAC()

		reserved, err := n.runner.ReserveScheduled(noHoldReserveWait, group)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)

		n.openGatedRAC()
		So(n.server.currentGroupCount(group), ShouldEqual, 2)
	})

	Convey("Given a runner command, a running job an earlier callback counted as ready, and a callback "+
		"gated after its snapshot of another ready job of its group, the job released to ready and "+
		"reserved again during the callback is counted once", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC, noHoldFastRetry, noHoldQuickShutdown)
		group := n.groupFor(n.reqs.RAM)

		job := n.newJob("racearlier", 3, 0)
		job.Priority = 5
		n.add(job)
		So(pollUntil(func() bool { return n.server.racIdle() && n.server.currentGroupCount(group) == 1 }),
			ShouldBeTrue)

		reserved := n.reserveStarted(job, group, os.Getpid())

		counted := n.gateFirstRACCounting(ctx, group)

		other := n.newJob("racearlier", 0, 0)
		other.Cmd += noHoldChildCmd
		n.add(other)
		counted.awaitGated()

		So(n.runner.Release(reserved, &JobEndState{Exited: true, Exitcode: 1, EndTime: time.Now()},
			FailReasonExit), ShouldBeNil)
		n.waitReady(job.Key())
		n.reserveStarted(job, group, os.Getpid())

		So(counted.open(), ShouldEqual, 2)
	})

	Convey("Given a runner command and a callback gated after its snapshot of a ready job, the job "+
		"suspended, resumed and reserved during the callback, so leaving ready twice, is counted once",
		t, func() {
			n := newNoHoldTest(ctx, t, noHoldServerRC, noHoldQuickShutdown)
			group := n.groupFor(n.reqs.RAM)

			counted := n.gateFirstRACCounting(ctx, group)
			job := n.addJob("racleavetwice", 0, 0)

			counted.awaitGated()

			n.suspend(job)
			n.resume(job)
			n.waitReady(job.Key())
			n.reserveStarted(job, group, os.Getpid())

			So(counted.open(), ShouldEqual, 1)
		})
}

// clientStatuses returns the JStatus of job's client copies: the one the REST
// API and the web page build from getJobsByKeys, and the one `wr status -o
// json` builds from a client's GetByEssence.
func (n *noHoldTest) clientStatuses(ctx context.Context, job *Job) []JStatus {
	jobs, srerr, qerr := n.server.getJobsByKeys(ctx, []string{job.Key()}, false, false)
	So(srerr, ShouldBeEmpty)
	So(qerr, ShouldBeEmpty)
	So(len(jobs), ShouldEqual, 1)

	fromServer, err := jobs[0].ToStatus()
	So(err, ShouldBeNil)

	fromClient, err := n.get(job).ToStatus()
	So(err, ShouldBeNil)

	return []JStatus{fromServer, fromClient}
}

// assertBumpKeptWhenReservedAfterReport seeds low learning for a job named
// name, has the manager release a run of it as lost, then sends the owner's RAM
// report with report while the job is delayed. Once the manager has applied
// that report, a new runner reserves the job, after a kick if kick is set. It
// asserts that runner and the manager both have the bumped RAM.
func (n *noHoldTest) assertBumpKeptWhenReservedAfterReport(name string, kick bool, report func(owned *Job) error) {
	n.seedLowLearning(name)
	job := n.addJob(name, 3, jobOverridePreferSystemReqs)

	owned := n.loseRun(job, "")
	So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateDelay)

	reservedRAM := n.reserveOnceLostEndStateApplied(job, kick)

	So(report(owned), ShouldBeNil)

	So(<-reservedRAM, ShouldEqual, noHoldBumpedRAM)
	So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)
	So(n.get(job).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
}

// reserveOnceLostEndStateApplied has a new runner reserve job, from inside the
// manager, once an owner's report of job's lost run has been applied to it,
// kicking it first if kick is set. It sends the RAM that runner was handed, or
// -1 if the kick or reserve failed, on the returned channel. The hook is
// removed, and the runner disconnected, when the Convey ends.
func (n *noHoldTest) reserveOnceLostEndStateApplied(job *Job, kick bool) <-chan int {
	other := n.connect()
	reservedRAM := make(chan int, 1)

	var once sync.Once

	lostEndStateAppliedHook = func(key string) {
		if key != job.Key() {
			return
		}

		once.Do(func() { reservedRAM <- n.kickAndReserveRAM(other, job, kick) })
	}

	Reset(func() {
		lostEndStateAppliedHook = nil

		disconnect(other)
	})

	return reservedRAM
}

// kickAndReserveRAM kicks job if kick is set, then has runner reserve it,
// returning the RAM it was handed, or -1 if either failed.
func (n *noHoldTest) kickAndReserveRAM(runner *Client, job *Job, kick bool) int {
	if kick {
		if kicked, err := n.jq.Kick([]*JobEssence{{JobKey: job.Key()}}); err != nil || kicked != 1 {
			return -1
		}
	}

	reserved, err := runner.Reserve(noHoldReserveWait)
	if err != nil || reserved == nil || reserved.Key() != job.Key() {
		return -1
	}

	return reserved.Requirements.RAM
}

// TestNoHoldSkipReserve covers spec E2 tests 1 to 5: a runner's first reserve
// is skipped only when its group has no count and nothing ready, so a fresh
// runner gets work the RAC has not yet counted.
func TestNoHoldSkipReserve(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner command and the RAC gated, a fresh client's first reserve gets an added job "+
		"of its group at once", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.gateRAC(ctx)

		job := n.addJob("e2-1", 0, 0)

		got, took := n.firstReserve(noHoldReserveWait, n.groupFor(n.reqs.RAM))
		So(got, ShouldNotBeNil)
		So(got.Key(), ShouldEqual, job.Key())
		So(took, ShouldBeLessThan, time.Second)
	})

	Convey("Given a runner command, a fresh client's first reserve for a group with no count and nothing "+
		"ready returns nothing at once", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)

		got, took := n.firstReserve(noHoldReserveWait, "nohold-nothing")
		So(got, ShouldBeNil)
		So(took, ShouldBeLessThan, time.Second)
	})

	Convey("Given a runner command and a group whose only job the RAC counted and a client reserved, a "+
		"fresh client's first reserve waits out its timeout", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		group := n.groupFor(n.reqs.RAM)

		job := n.addJob("e2-3", 0, 0)

		So(pollUntil(func() bool { return n.server.racIdle() && n.server.currentGroupCount(group) == 1 }),
			ShouldBeTrue)
		n.reserveStarted(job, group, os.Getpid())
		So(n.server.currentGroupCount(group), ShouldEqual, 1)
		So(n.server.q.HasReady(group), ShouldBeFalse)

		wait := 300 * time.Millisecond
		got, took := n.firstReserve(wait, group)
		So(got, ShouldBeNil)
		So(took, ShouldBeGreaterThanOrEqualTo, wait)
	})

	Convey("Given no runner command, skipReserve is false for any request", t, func() {
		n := newNoHoldTest(ctx, t)

		for _, cr := range []*clientRequest{
			{SchedulerGroup: "nohold-nothing", FirstReserve: true},
			{SchedulerGroup: "nohold-nothing"},
			{FirstReserve: true},
			{},
		} {
			So(n.server.skipReserve(cr), ShouldBeFalse)
		}
	})

	Convey("Given a runner command, the RAC gated and a ready job blocked by its limit group's 1 slot in "+
		"use, a fresh client's first reserve waits out its timeout for the slot and leaves it ready", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.gateRAC(ctx)

		group := n.groupFor(n.reqs.RAM, "nohold-lb")

		first := n.newJob("e2-5", 0, 0)
		first.LimitGroups = []string{"nohold-lb:1"}
		n.add(first)
		n.reserveStarted(first, group, os.Getpid())

		second := n.newJob("e2-5", 0, 0)
		second.Cmd += noHoldChildCmd
		second.LimitGroups = []string{"nohold-lb"}
		n.add(second)
		n.waitReady(second.Key())

		wait := 500 * time.Millisecond
		got, took := n.firstReserve(wait, group)
		So(got, ShouldBeNil)
		So(took, ShouldBeGreaterThanOrEqualTo, wait)
		So(took, ShouldBeLessThan, 1500*time.Millisecond)
		So(n.itemState(second.Key()), ShouldEqual, queue.ItemStateReady)
	})
}

// queueLockProbe records, at each load of one ReqGroup's learned requirements,
// whether the queue's lock was held, as spec D4 test 1 checks it.
type queueLockProbe struct {
	mu      sync.Mutex
	server  *Server
	loads   *reqStatLoads
	group   string
	checked int
	under   int
}

// loadsAfter waits until no ready-added callback is running or pending, so
// none can load the probed ReqGroup first, invalidates it, runs step, and
// reports whether at least one load of it then started and every load started
// so far was checked.
func (p *queueLockProbe) loadsAfter(step func()) bool {
	if !pollUntil(p.server.racIdle) {
		return false
	}

	p.server.db.invalidateReqGroupRecs(p.group)
	before := p.loads.count(p.group)

	step()

	return pollUntil(func() bool {
		started := p.loads.count(p.group)

		p.mu.Lock()
		defer p.mu.Unlock()

		return started > before && p.checked == started
	})
}

// underLock returns how many checked loads held the queue's lock.
func (p *queueLockProbe) underLock() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.under
}

// TestNoHoldNoIOUnderQueueLock covers spec D4 tests 1, 2, 3 and 5: preparing an
// item as it enters ready never holds the queue's lock across a database read,
// a Server mutex or a wait on the learned-requirement cache. D4 test 4 is
// `CGO_ENABLED=1 make race`.
func TestNoHoldNoIOUnderQueueLock(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a check at each load of a ReqGroup of whether the queue's lock is held, jobs entering "+
		"ready by add, kick, resume, dependency satisfied and delay expiry load it with the lock free",
		t, func() {
			loads := countReqStatLoads()
			n := newNoHoldTest(ctx, t, noHoldSlowRetry)

			Reset(func() { reqStatLoadHook = nil })

			const name = "d4-1"

			reqGroup := "nohold-" + name
			job := func(cmd string, retries, priority uint8) *Job {
				j := n.newJob(name, retries, jobOverridePreferSystemReqs)
				j.Cmd += " " + cmd
				j.Priority = priority

				return j
			}

			buried := job("buried", 0, 0)
			n.add(buried)
			n.failRun(buried, FailReasonExit, 5)
			So(n.itemState(buried.Key()), ShouldEqual, queue.ItemStateBury)

			suspended := job("suspended", 0, 0)
			n.add(suspended)
			n.suspend(suspended)

			parent := n.newJob(name+"-parent", 0, jobOverridePreferSystemReqs)
			parent.Priority = 2
			parent.DepGroups = []string{reqGroup + "-parent"}
			n.add(parent)

			dependent := job("dependent", 0, 0)
			dependent.Dependencies = Dependencies{NewDepGroupDependency(reqGroup + "-parent")}
			n.add(dependent)
			So(n.itemState(dependent.Key()), ShouldEqual, queue.ItemStateDependent)

			delayed := job("delayed", 3, 3)

			probe := n.probeQueueLockOnLoad(loads, reqGroup)

			So(probe.loadsAfter(func() { n.add(job("added", 0, 0)) }), ShouldBeTrue)
			So(probe.loadsAfter(func() { n.kick(buried) }), ShouldBeTrue)
			So(probe.loadsAfter(func() { n.resume(suspended) }), ShouldBeTrue)
			So(probe.loadsAfter(func() {
				n.archive(parent, 5)
				n.waitReady(dependent.Key())
			}), ShouldBeTrue)

			n.add(delayed)
			n.failRun(delayed, FailReasonExit, 5)
			So(n.itemState(delayed.Key()), ShouldEqual, queue.ItemStateDelay)
			So(probe.loadsAfter(func() { n.waitReady(delayed.Key()) }), ShouldBeTrue)

			So(loads.count(reqGroup), ShouldBeGreaterThanOrEqualTo, 1)
			So(probe.underLock(), ShouldEqual, 0)
		})

	Convey("Given a runner command and a job delayed after failing for RAM in the RAM 10 group, while "+
		"racmutex is held across its delay expiry the queue stays responsive, and once released the "+
		"item is in the bumped group", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC, noHoldSlowRetry)
		job := n.addJob("d4-2", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failRAMInGroup(job, n.groupFor(10))

		_, item := n.serverJob(job.Key())
		So(item.Stats().State, ShouldEqual, queue.ItemStateDelay)
		So(item.ReserveGroup, ShouldEqual, n.groupFor(10))

		responsive, expired := n.holdRACMutexUntilReady()
		So(responsive, ShouldBeTrue)
		So(expired, ShouldBeTrue)

		_, item = n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldBumpedRAM))
	})

	Convey("Given the runner command set and then unset, an added job's item has reserve group \"\"", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.server.setRC("")
		n.gateRAC(ctx)

		job := n.addJob("d4-3", 0, jobOverridePreferSystemReqs)

		_, item := n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, "")
	})

	Convey("Given learned 300 loaded, then 800 seeded and a background load of it held, an add returns at "+
		"once at RAM 300, and once the load completes the RAC brings the job to 800", t, func() {
		held := newHeldLoad()
		Reset(held.release)

		loads := countReqStatLoads()
		n := newNoHoldTest(ctx, t, noHoldServerRC)

		Reset(func() { reqStatLoadHook = nil })
		n.gateRAC(ctx)

		job := n.newJob("d4-5", 0, jobOverridePreferSystemReqs)
		n.seedLearned("d4-5")
		n.server.db.refreshReqGroupRecs(job.ReqGroup)

		seedReqGroupStats(n.server, bucketJobRAM, job.ReqGroup, noHoldNewerLearnedRAM)
		_, current := n.server.db.cachedReqGroupRecs(job.ReqGroup)
		So(current, ShouldBeFalse)

		loads.setOnLoad(held.hold)
		n.server.db.refreshReqGroupRecsLater(job.ReqGroup)
		So(held.started(), ShouldBeTrue)

		So(addsWithin(n.jq, job, time.Second), ShouldBeTrue)
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)

		held.release()
		So(pollUntil(func() bool {
			_, current := n.server.db.cachedReqGroupRecs(job.ReqGroup)

			return current
		}), ShouldBeTrue)

		sjob, _ := n.serverJob(job.Key())
		n.server.buildSchedulerGroups(ctx, n.server.q, []any{sjob}, serverRC)
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldNewerLearnedRAM)
	})
}

func newHeldLoad() *heldLoad {
	return &heldLoad{entered: make(chan struct{}), released: make(chan struct{})}
}

// addsWithin has jq add job, which must be new, and reports whether the add
// returned without error within wait.
func addsWithin(jq *Client, job *Job, wait time.Duration) bool {
	added := make(chan bool, 1)

	go func() {
		inserts, _, err := jq.Add([]*Job{job}, os.Environ(), true)
		added <- err == nil && inserts == 1
	}()

	select {
	case ok := <-added:
		return ok
	case <-time.After(wait):
		return false
	}
}

// hookDerivations is what recordHookDerivations records at one call of the
// ready hook for its job: whether the job's derived strings were memoised as
// the hook began, and the job's derivation count before and after it.
type hookDerivations struct {
	derived bool
	before  uint32
	after   uint32
}

// TestNoHoldHookFindsGroupDerived covers the cost spec G1.3 measures: a job's
// scheduler group is derived before its item is queued, and preparing it as it
// enters ready re-derives only when that changes its requirements, so the
// queue's ready hook finds the group memoised.
func TestNoHoldHookFindsGroupDerived(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a runner command and learned 300, an added job's group is derived before its item is "+
		"queued, and the hook derives nothing more", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.seedLearned("g13-add")
		n.gateRAC(ctx)

		job := n.newJob("g13-add", 0, jobOverridePreferSystemReqs)
		calls := n.recordHookDerivations(job.Key())

		n.add(job)

		So(calls(), ShouldResemble, []hookDerivations{{derived: true, before: 1, after: 1}})

		_, item := n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldLearnedRAM))
	})

	Convey("Given a runner command and a ready job, suspending and resuming it with nothing learned in "+
		"between derives its group no more", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.seedLearned("g13-resume")
		job := n.addJob("g13-resume", 0, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		before := n.derivations(job.Key())

		n.suspend(job)
		n.resume(job)
		n.waitReady(job.Key())

		So(n.derivations(job.Key()), ShouldEqual, before)

		_, item := n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldLearnedRAM))
	})
}

// countingGate holds the first ready-added callback after its snapshot until
// opened, and reports the scheduler group's count as that callback left it,
// before any recall can change it.
type countingGate struct {
	entered  chan struct{}
	gate     chan struct{}
	finished chan int
	once     sync.Once
}

// awaitGated waits until the gated callback has its snapshot.
func (g *countingGate) awaitGated() {
	select {
	case <-g.entered:
	case <-time.After(noHoldReserveWait):
		So("ready-added callback was not called", ShouldBeEmpty)
	}
}

// open opens the gate and returns the group's count as the gated callback left
// it.
func (g *countingGate) open() int {
	g.release()

	select {
	case count := <-g.finished:
		return count
	case <-time.After(noHoldReserveWait):
		return -1
	}
}

func (g *countingGate) release() {
	g.once.Do(func() { close(g.gate) })
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

	// releaseRecovery, set while restartPaused holds recovery, lets it go.
	releaseRecovery func()
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
		if n.releaseRecovery != nil {
			n.releaseRecovery()
		}

		n.stop(ctx)
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

// stop opens any RAC gate, so no callback is left waiting at it, then
// disconnects n's clients and stops the manager.
func (n *noHoldTest) stop(ctx context.Context) {
	if n.gates != nil {
		n.gates.openAll()
		n.gates = nil
	}

	disconnect(n.runner)
	disconnect(n.jq)
	n.server.Stop(ctx, true)
}

// restart stops the manager and starts a new one on its database, returning
// once the new one has recovered.
func (n *noHoldTest) restart(ctx context.Context) {
	n.stop(ctx)

	n.config.dontWipeDevDB = true
	n.start(ctx)

	So(waitUntilRecovered(n.server), ShouldBeTrue)
}

// restartPaused stops the manager and starts a new one on its database whose
// recovery is held before it queues anything, until the returned func is
// called. That func returns once the new manager has recovered, with n's
// clients connected to it.
func (n *noHoldTest) restartPaused(ctx context.Context) func() {
	n.stop(ctx)

	n.config.dontWipeDevDB = true
	server, token, release := pausedRecoveringFixtureServer(ctx, n.config)
	n.server = server
	n.releaseRecovery = release

	return func() {
		release()
		So(waitUntilRecovered(n.server), ShouldBeTrue)

		n.connect = func() *Client {
			client, errc := Connect(n.addr, n.config.CAFile, n.config.CertDomain, token, n.connTO)
			So(errc, ShouldBeNil)

			return client
		}

		n.jq = n.connect()
		n.runner = n.connect()
	}
}

// addJob adds a job with standardReqs and a ReqGroup of its own, named after
// name, and returns it.
func (n *noHoldTest) addJob(name string, retries, override uint8) *Job {
	job := n.newJob(name, retries, override)
	n.add(job)

	return job
}

// newJob returns a job with standardReqs and a ReqGroup of its own, named
// after name, without adding it.
func (n *noHoldTest) newJob(name string, retries, override uint8) *Job {
	return &Job{
		Cmd: restFormTrue + " " + name, Cwd: testCwd, RepGroup: "nohold-" + name,
		ReqGroup: "nohold-" + name, Requirements: n.reqs.Clone(), Retries: retries, Override: override,
	}
}

// add has the user add jobs, all of which must be new.
func (n *noHoldTest) add(jobs ...*Job) {
	inserts, _, err := n.jq.Add(jobs, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, len(jobs))
}

// gateRAC waits until no ready-added callback is running or pending, then
// holds every later one at a gate that is only opened when the test ends.
func (n *noHoldTest) gateRAC(ctx context.Context) {
	So(pollUntil(n.server.racIdle), ShouldBeTrue)

	// the callback keeps this server, queue and gates, since n's are replaced
	// by a restart and the server's queue is cleared when it stops.
	gates := newRACGates(1)
	n.gates = gates
	server, q := n.server, n.server.q

	q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
		i := gates.enter()
		gates.wait(i)
		server.readyAddedCallback(ctx, q, allitemdata)
	})
}

// awaitGatedRAC waits until the ready-added callback gateRAC holds has taken its
// snapshot of the ready jobs and reached its gate.
func (n *noHoldTest) awaitGatedRAC() {
	select {
	case <-n.gates.called[0]:
	case <-time.After(noHoldReserveWait):
		n.t.Fatalf("ready-added callback was not called")
	}
}

// openGatedRAC opens the gate of the ready-added callback gateRAC holds and
// waits until no callback is running.
func (n *noHoldTest) openGatedRAC() {
	n.gates.open(0)
	So(pollUntil(n.server.racIdle), ShouldBeTrue)
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

// seedLearned seeds noHoldLearnedRAM as name's ReqGroup's only RAM stat, so its
// learned RAM is noHoldLearnedRAM ("learned 300").
func (n *noHoldTest) seedLearned(name string) {
	seedReqGroupStats(n.server, bucketJobRAM, "nohold-"+name, noHoldLearnedRAM)
}

// groupFor returns the scheduler group of standardReqs with the given RAM and
// limit groups.
func (n *noHoldTest) groupFor(ram int, limitGroups ...string) string {
	req := n.reqs.Clone()
	req.RAM = ram

	return schedulerGroupString(reqForScheduler(req), limitGroups)
}

// asOlderRecord makes the manager's job for key look as a record from a
// manager before FailedRunReqs does: its RAM back at 10 and no FailedRunReqs.
func (n *noHoldTest) asOlderRecord(key string) {
	sjob, _ := n.serverJob(key)

	sjob.Lock()
	sjob.Requirements.RAM = 10
	sjob.FailedRunReqs = nil
	sjob.Unlock()
}

// kick has the user kick job.
func (n *noHoldTest) kick(job *Job) {
	kicked, err := n.jq.Kick([]*JobEssence{{JobKey: job.Key()}})
	So(err, ShouldBeNil)
	So(kicked, ShouldEqual, 1)
}

// suspend has the user suspend job.
func (n *noHoldTest) suspend(job *Job) {
	suspended, err := n.jq.Suspend([]*JobEssence{{JobKey: job.Key()}})
	So(err, ShouldBeNil)
	So(suspended, ShouldEqual, 1)
}

// resume has the user resume job.
func (n *noHoldTest) resume(job *Job) {
	resumed, err := n.jq.Resume([]*JobEssence{{JobKey: job.Key()}})
	So(err, ShouldBeNil)
	So(resumed, ShouldEqual, 1)
}

// archive has the runner reserve job and archive it as a success with the
// given peak RAM.
func (n *noHoldTest) archive(job *Job, peakRAM int) {
	reserved := n.reserveStarted(job, "", os.Getpid())

	So(n.runner.Archive(reserved, &JobEndState{
		Exited: true, Exitcode: 0, PeakRAM: peakRAM, EndTime: time.Now(),
	}), ShouldBeNil)
}

// failRAMInGroup has the runner reserve job from the given scheduler group,
// report it started and release it for RAM with noHoldPeakRAM.
func (n *noHoldTest) failRAMInGroup(job *Job, group string) {
	reserved := n.reserveStarted(job, group, os.Getpid())

	So(n.runner.Release(reserved, &JobEndState{
		Exited: true, Exitcode: 1, PeakRAM: noHoldPeakRAM, EndTime: time.Now(),
	}, FailReasonRAM), ShouldBeNil)
}

// addCompleteDependent queues, straight into the queue, the item of a complete
// job (a rerun candidate) named after name, with " child" added to its
// command, that depends on parent, and returns its key.
func (n *noHoldTest) addCompleteDependent(ctx context.Context, name string, parent *Job) string {
	old := n.newJob(name, 0, jobOverridePreferSystemReqs)
	old.Cmd += noHoldChildCmd
	old.State = JobStateComplete

	added, _, err := n.server.q.AddMany(ctx, []*queue.ItemDef{{
		Key: old.Key(), Data: old, TTR: n.server.itemTTRDuration(), Dependencies: []string{parent.Key()},
	}})
	So(err, ShouldBeNil)
	So(added, ShouldEqual, 1)

	return old.Key()
}

// recordItemStatesOnLoad has loads record, at each load of job's ReqGroup, the
// state of job's queue item ("" if it is not queued). It reads the item in a
// goroutine and waits up to a second for it, since a load under the queue's
// lock would stop that read returning; such a wait records "timeout". It
// returns a func giving the states recorded so far.
func (n *noHoldTest) recordItemStatesOnLoad(loads *reqStatLoads, job *Job) func() []queue.ItemState {
	var (
		mu     sync.Mutex
		states []queue.ItemState
	)

	q, key := n.server.q, job.Key()

	loads.setOnLoad(func(reqGroup string) {
		if reqGroup != job.ReqGroup {
			return
		}

		got := make(chan queue.ItemState, 1)

		go func() {
			item, err := q.Get(key)
			if err != nil || item == nil {
				got <- ""

				return
			}

			got <- item.Stats().State
		}()

		var state queue.ItemState

		select {
		case state = <-got:
		case <-time.After(time.Second):
			state = "timeout"
		}

		mu.Lock()

		states = append(states, state)
		mu.Unlock()
	})

	return func() []queue.ItemState {
		mu.Lock()
		defer mu.Unlock()

		return append([]queue.ItemState(nil), states...)
	}
}

// probeQueueLockOnLoad has each load of reqGroup call the queue's Stats in a
// goroutine and wait up to a second for it, counting a wait that times out as
// a load under the queue's lock: such a load holds the lock while it waits, so
// Stats cannot return. The load does not depend on Stats, so this cannot
// deadlock. It resets loads' counts, so only probed loads are counted.
func (n *noHoldTest) probeQueueLockOnLoad(loads *reqStatLoads, reqGroup string) *queueLockProbe {
	p := &queueLockProbe{server: n.server, loads: loads, group: reqGroup}
	q := n.server.q

	loads.reset()

	loads.setOnLoad(func(group string) {
		if group != reqGroup {
			return
		}

		returned := make(chan struct{})

		go func() {
			q.Stats()
			close(returned)
		}()

		under := false

		select {
		case <-returned:
		case <-time.After(time.Second):
			under = true
		}

		p.mu.Lock()
		defer p.mu.Unlock()

		p.checked++
		if under {
			p.under++
		}
	})

	return p
}

// holdRACMutexUntilReady holds racmutex for writing until the queue's only item
// is ready, checking every 50ms that the queue's Stats returns within a second
// meanwhile. It reports whether Stats always did, and whether the item became
// ready within noHoldLostWait.
func (n *noHoldTest) holdRACMutexUntilReady() (bool, bool) {
	q := n.server.q

	n.server.racmutex.Lock()
	defer n.server.racmutex.Unlock()

	deadline := time.Now().Add(noHoldLostWait)

	for time.Now().Before(deadline) {
		stats, ok := statsWithin(q, time.Second)
		if !ok {
			return false, false
		}

		if stats.Delayed == 0 && stats.Ready == 1 {
			return true, true
		}

		time.Sleep(50 * time.Millisecond)
	}

	return true, false
}

// statsWithin returns q's Stats, and false if they did not return within wait.
func statsWithin(q *queue.Queue, wait time.Duration) (*queue.Stats, bool) {
	got := make(chan *queue.Stats, 1)

	go func() { got <- q.Stats() }()

	select {
	case stats := <-got:
		return stats, true
	case <-time.After(wait):
		return nil, false
	}
}

// recordHookDerivations wraps the queue's ready hook so that each call for
// key's job records its hookDerivations. It returns a func giving the calls
// recorded so far.
func (n *noHoldTest) recordHookDerivations(key string) func() []hookDerivations {
	var (
		mu    sync.Mutex
		calls []hookDerivations
	)

	server := n.server

	server.q.SetReadyHook(func(data any, reserveGroup string) string {
		job, ok := data.(*Job)
		if !ok {
			return server.prepareForReady(data, reserveGroup)
		}

		job.RLock()
		matches, derived, before := job.Key() == key, job.derived != nil, job.derivations
		job.RUnlock()

		group := server.prepareForReady(data, reserveGroup)

		if matches {
			job.RLock()
			after := job.derivations
			job.RUnlock()

			mu.Lock()

			calls = append(calls, hookDerivations{derived: derived, before: before, after: after})
			mu.Unlock()
		}

		return group
	})

	return func() []hookDerivations {
		mu.Lock()
		defer mu.Unlock()

		return append([]hookDerivations(nil), calls...)
	}
}

// derivations returns how many times the manager's job for key has derived its
// scheduler-group strings.
func (n *noHoldTest) derivations(key string) uint32 {
	sjob, _ := n.serverJob(key)

	sjob.RLock()
	defer sjob.RUnlock()

	return sjob.derivations
}

// firstReserve has a fresh client make its first reserve, from group with the
// given timeout, and returns the job it got (nil if none) and how long the
// reserve took.
func (n *noHoldTest) firstReserve(wait time.Duration, group string) (*Job, time.Duration) {
	fresh := n.connect()
	defer disconnect(fresh)

	start := time.Now()
	job, err := fresh.ReserveScheduled(wait, group)
	took := time.Since(start)

	So(err, ShouldBeNil)

	return job, took
}

// gateFirstRACCounting routes n's ready-added callbacks through a countingGate
// for group, after waiting until no callback is running.
func (n *noHoldTest) gateFirstRACCounting(ctx context.Context, group string) *countingGate {
	So(pollUntil(n.server.racIdle), ShouldBeTrue)

	g := &countingGate{entered: make(chan struct{}), gate: make(chan struct{}), finished: make(chan int, 1)}
	server, q := n.server, n.server.q
	calls := 0

	q.SetReadyAddedCallback(func(_ string, allitemdata []any) {
		calls++
		if calls > 1 {
			server.readyAddedCallback(ctx, q, allitemdata)

			return
		}

		close(g.entered)
		<-g.gate
		server.readyAddedCallback(ctx, q, allitemdata)

		g.finished <- server.currentGroupCount(group)
	})

	Reset(func() { g.release() })

	return g
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
		n := newNoHoldTest(ctx, t, noHoldLostRuns, noHoldServerRC)
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

	Convey("Given low learning and a job released as lost and delayed, a run reserved once the owner's RAM "+
		"report has been applied has the bumped RAM, and the manager keeps it", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns, noHoldSlowRetry)

		n.assertBumpKeptWhenReservedAfterReport("c1-applied-release", false, func(owned *Job) error {
			return n.runner.Release(owned, ownerRAMEndState(), FailReasonRAM)
		})
	})

	Convey("Given low learning and a job released as lost and delayed, a run reserved after a kick once the "+
		"owner's RAM bury has been applied has the bumped RAM, and the manager keeps it", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns, noHoldSlowRetry)

		n.assertBumpKeptWhenReservedAfterReport("c1-applied-bury", true, func(owned *Job) error {
			return n.runner.Bury(owned, ownerRAMEndState(), FailReasonRAM)
		})
	})
}

// ownerRAMEndState is the owner's report of a run that used noHoldPeakRAM.
func ownerRAMEndState() *JobEndState {
	return &JobEndState{Exited: true, Exitcode: 1, PeakRAM: noHoldPeakRAM}
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

	Convey("Given low learning, a buried job that exceeded its RAM and the RAC its kick triggers held "+
		"until a reserve has taken it at the bumped RAM, the reserved job keeps that RAM once the RAC "+
		"has run", t, func() {
		n := newNoHoldTest(ctx, t)
		n.seedLowLearning("c2-1-rac")
		job := n.addJob("c2-1-rac", 0, jobOverridePreferSystemReqs)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)

		n.failRun(job, FailReasonExit, noHoldPeakRAM)
		So(n.get(job).State, ShouldEqual, JobStateBuried)

		n.gateRAC(ctx)
		n.kick(job)
		n.awaitGatedRAC()

		reserved, err := n.runner.Reserve(noHoldReserveWait)
		So(err, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(reserved.Key(), ShouldEqual, job.Key())
		So(reserved.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)

		n.openGatedRAC()

		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)
		So(n.get(job).Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
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

	Convey("Given a job that failed for RAM and whose retry at the raised RAM was lost and recovered, the "+
		"retry failing within that RAM records no high-peak stat", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns)
		job := n.addJob("c2-lost-retry", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)
		retry := n.reserveStarted(job, "", os.Getpid())
		So(retry.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(waitForJobLost(n.server, job.Key(), noHoldLostWait), ShouldBeTrue)

		_, err := n.runner.Touch(retry)
		So(err, ShouldBeNil)

		const withinRetryRAM = 500

		So(n.runner.Release(retry, &JobEndState{
			Exited: true, Exitcode: 1, PeakRAM: withinRetryRAM, EndTime: time.Now(),
		}, FailReasonExit), ShouldBeNil)
		So(storedLiveJob(t, n.server.db, job.Key()).FailReason, ShouldEqual, FailReasonExit)

		So(n.server.db.bolt.View(func(tx *bolt.Tx) error {
			key := fmt.Appendf(nil, "%s%s%20d", job.ReqGroup, dbDelimiter, withinRetryRAM)
			So(tx.Bucket(bucketJobRAM).Get(key), ShouldBeNil)

			return nil
		}), ShouldBeNil)
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

	Convey("Given a job that failed for RAM and whose retry at the raised RAM is lost, its usage is judged "+
		"against the retry's RAM while lost and once a touch recovers it", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns)
		job := n.addJob("status-lost-retry", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)
		retry := n.reserveStarted(job, "", os.Getpid())
		So(retry.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(waitForJobLost(n.server, job.Key(), noHoldLostWait), ShouldBeTrue)

		sjob, _ := n.serverJob(job.Key())

		js, err := sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.State, ShouldEqual, JobStateLost)
		So(js.RunRAM, ShouldEqual, noHoldBumpedRAM)

		for _, copied := range n.clientStatuses(ctx, job) {
			So(copied.State, ShouldEqual, JobStateLost)
			So(copied.RunRAM, ShouldEqual, noHoldBumpedRAM)
		}

		_, err = n.runner.Touch(retry)
		So(err, ShouldBeNil)

		js, err = sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.State, ShouldEqual, JobStateRunning)
		So(js.FailReason, ShouldEqual, FailReasonLost)
		So(js.RunRAM, ShouldEqual, noHoldBumpedRAM)

		for _, copied := range n.clientStatuses(ctx, job) {
			So(copied.RunRAM, ShouldEqual, noHoldBumpedRAM)
		}

		js.PeakRAM = noHoldPeakRAM
		So(statusPageFlagsOverLimit(t, js, "window.wrUtils.mbIEC(PeakRAM)"), ShouldBeFalse)
	})

	Convey("Given a job that failed for RAM and whose retry is reserved at the raised RAM but never "+
		"started, and is lost, its usage is judged against the retry's RAM while lost and once a touch "+
		"recovers it", t, func() {
		n := newNoHoldTest(ctx, t, noHoldLostRuns)
		job := n.addJob("status-lost-reserved", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)
		retry := n.reserve(job, noHoldReserveWait)
		So(retry.Requirements.RAM, ShouldEqual, noHoldBumpedRAM)
		So(waitForJobLost(n.server, job.Key(), noHoldLostWait), ShouldBeTrue)

		sjob, _ := n.serverJob(job.Key())

		js, err := sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.FailReason, ShouldEqual, FailReasonLost)
		So(js.RunRAM, ShouldEqual, noHoldBumpedRAM)

		for _, copied := range n.clientStatuses(ctx, job) {
			So(copied.State, ShouldEqual, JobStateLost)
			So(copied.RunRAM, ShouldEqual, noHoldBumpedRAM)
		}

		_, err = n.runner.Touch(retry)
		So(err, ShouldBeNil)

		js, err = sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.State, ShouldEqual, JobStateReserved)
		So(js.FailReason, ShouldEqual, FailReasonLost)
		So(js.RunRAM, ShouldEqual, noHoldBumpedRAM)

		for _, copied := range n.clientStatuses(ctx, job) {
			So(copied.RunRAM, ShouldEqual, noHoldBumpedRAM)
		}
	})

	Convey("Given a job that failed for RAM and whose retry at the raised RAM completed, its usage is "+
		"judged against the retry's RAM", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("status-complete-retry", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)
		sjob, _ := n.serverJob(job.Key())
		n.archive(job, noHoldPeakRAM)

		js, err := sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.State, ShouldEqual, JobStateComplete)
		So(js.RunRAM, ShouldEqual, noHoldBumpedRAM)

		for _, copied := range n.clientStatuses(ctx, job) {
			So(copied.State, ShouldEqual, JobStateComplete)
			So(copied.RunRAM, ShouldEqual, noHoldBumpedRAM)
		}
	})

	Convey("Given a run released as lost after its group learned more RAM, its usage is judged against "+
		"the RAM the lost run reserved", t, func() {
		n := newNoHoldTest(ctx, t, func(c *ServerConfig) { c.Timings.ItemTTR = 3 * time.Second })
		job := n.addJob("status-lost-learned", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.server.SetLostJobCheckTimeout(2 * time.Second)
		n.server.SetLostJobCheckRetryTime(200 * time.Millisecond)

		lostRun := n.reserveStarted(job, "", definitelyDeadPid(t))
		So(lostRun.Requirements.RAM, ShouldEqual, 10)
		setServerJobRunnerPid(n.server, job.Key(), definitelyDeadPid(t))

		n.seedLearned("status-lost-learned")
		So(pollUntil(func() bool {
			n.server.db.refreshReqGroupRecs(job.ReqGroup)
			_, current := n.server.db.cachedReqGroupRecs(job.ReqGroup)

			return current
		}), ShouldBeTrue)

		So(pollUntilFor(noHoldLostWait, func() bool {
			_, _, failReason, ok := serverJobState(n.server, job.Key())
			state := n.itemState(job.Key())

			return ok && failReason == FailReasonLost &&
				(state == queue.ItemStateDelay || state == queue.ItemStateReady)
		}), ShouldBeTrue)

		sjob, _ := n.serverJob(job.Key())

		js, err := sjob.ToStatus()
		So(err, ShouldBeNil)
		So(js.ExpectedRAM, ShouldEqual, noHoldLearnedRAM)
		So(js.RunRAM, ShouldEqual, 10)
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
		n := newNoHoldTest(ctx, t, noHoldServerRC)
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

		n.modify(job, override2Modifier())

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

		n.modify(job, override2Modifier())

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

		n.modify(job, override2Modifier())

		So(n.serverJobRAM(job.Key()), ShouldEqual, 10)
		So(n.get(job).Requirements.RAM, ShouldEqual, 10)
	})

	Convey("Given a 500ms release delay and a job delayed after failing for RAM, when Modify changes its "+
		"command while its delay expires, Modify returns and the job is ready under its new key", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC,
			func(c *ServerConfig) { c.Timings.ReleaseDelayMin = 500 * time.Millisecond })
		job := n.addJob("c3-10", 3, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failRAMInGroup(job, n.groupFor(10))

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

		// the item was prepared as it became ready, with the RAC gated: it is
		// in the bumped group, not the RAM 10 group it waited in.
		sjob, item := n.serverJob(newKey)
		So(sjob.Cmd, ShouldEqual, job.Cmd+" modified")
		So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldBumpedRAM))
		So(sjob.getSchedulerGroup(), ShouldEqual, item.ReserveGroup)
		So(n.serverJobRAM(newKey), ShouldEqual, noHoldBumpedRAM)
	})

	Convey("Given no learned values (every load fails), the RAC gated and a job whose RAM a high peak "+
		"bumped", t, func() {
		const name = "c3-override2-no-learning"

		loads := countReqStatLoads()
		loads.setFailing("nohold-"+name, true)

		Convey("buried, when Modify sets only override 2, kicked its item has its original RAM", func() {
			n := newNoHoldTest(ctx, t)

			Reset(func() { reqStatLoadHook = nil })

			job := n.addJob(name, 0, jobOverridePreferSystemReqs)
			n.gateRAC(ctx)

			n.failRun(job, FailReasonExit, noHoldPeakRAM)
			So(n.get(job).State, ShouldEqual, JobStateBuried)
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)

			rec, _ := n.server.db.cachedReqGroupRecs(job.ReqGroup)
			So(rec, ShouldBeNil)

			n.modify(job, override2Modifier())
			n.kick(job)

			So(n.serverJobRAM(job.Key()), ShouldEqual, 10)
		})

		Convey("delayed, when Modify sets only override 2, its item has its original RAM once ready", func() {
			n := newNoHoldTest(ctx, t, noHoldSlowRetry)

			Reset(func() { reqStatLoadHook = nil })

			job := n.addJob(name, 1, jobOverridePreferSystemReqs)
			n.gateRAC(ctx)

			n.failRun(job, FailReasonExit, noHoldPeakRAM)
			So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateDelay)
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)

			n.modify(job, override2Modifier())

			n.waitReady(job.Key())
			So(n.serverJobRAM(job.Key()), ShouldEqual, 10)
		})
	})
}

// override2Modifier returns a modifier that sets only Override 2
// (jobOverrideAlwaysUseJobReqs).
func override2Modifier() *JobModifier {
	jm := NewJobModifer()
	jm.SetOverride(jobOverrideAlwaysUseJobReqs)

	return jm
}

// TestNoHoldReadyPrepare covers spec D1: every item is prepared by the queue's
// ready hook as it becomes ready, so its data and reserve group are already
// right before any reserve or RAC.
func TestNoHoldReadyPrepare(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a job with no retries buried after failing for RAM, with a record from an older manager, "+
		"kicked its item has the bumped RAM when Kick returns", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("d1-1", 0, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failForRAM(job)
		So(n.get(job).State, ShouldEqual, JobStateBuried)
		n.asOlderRecord(job.Key())

		n.kick(job)

		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)
	})

	Convey("Given learned 300 and no runner command, an added job's item has RAM 300 and reserve group "+
		"\"\" when Add returns", t, func() {
		n := newNoHoldTest(ctx, t)
		n.seedLearned("d1-2")
		n.gateRAC(ctx)

		job := n.addJob("d1-2", 0, jobOverridePreferSystemReqs)

		_, item := n.serverJob(job.Key())
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
		So(item.ReserveGroup, ShouldEqual, "")
	})

	Convey("Given learned 300 and a runner command, an added job's item is in the RAM 300 group, as is "+
		"the job, when Add returns", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.seedLearned("d1-3")
		n.gateRAC(ctx)

		job := n.addJob("d1-3", 0, jobOverridePreferSystemReqs)

		sjob, item := n.serverJob(job.Key())
		So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldLearnedRAM))
		So(sjob.getSchedulerGroup(), ShouldEqual, item.ReserveGroup)
	})

	Convey("Given learned 300", t, func() {
		Convey("a suspended job, resumed after 300 is learned, is ready at RAM 300", func() {
			n := newNoHoldTest(ctx, t)
			job := n.addJob("d1-4-resume", 0, jobOverridePreferSystemReqs)
			n.suspend(job)
			n.gateRAC(ctx)

			n.seedLearned("d1-4-resume")
			n.resume(job)

			n.waitReady(job.Key())
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
		})

		Convey("a dependent job is ready at RAM 300 once its parent is archived", func() {
			n := newNoHoldTest(ctx, t)
			n.seedLearned("d1-4-dep")
			parent := n.newJob("d1-4-dep", 0, jobOverridePreferSystemReqs)
			parent.DepGroups = []string{"nohold-d1-4-dep"}
			n.add(parent)

			child := n.newJob("d1-4-dep", 0, jobOverridePreferSystemReqs)
			child.Cmd += noHoldChildCmd
			child.Dependencies = Dependencies{NewDepGroupDependency("nohold-d1-4-dep")}
			n.add(child)
			n.gateRAC(ctx)

			So(n.itemState(child.Key()), ShouldEqual, queue.ItemStateDependent)
			So(n.serverJobRAM(child.Key()), ShouldEqual, 10)

			n.archive(parent, noHoldLearnedRAM)

			n.waitReady(child.Key())
			So(n.serverJobRAM(child.Key()), ShouldEqual, noHoldLearnedRAM)
		})

		Convey("a job whose record is from an older manager is ready at RAM 300 when its delay expires", func() {
			n := newNoHoldTest(ctx, t, noHoldSlowRetry)
			n.seedLearned("d1-4-delay")
			job := n.addJob("d1-4-delay", 3, jobOverridePreferSystemReqs)
			n.gateRAC(ctx)

			n.failRun(job, FailReasonExit, 5)
			So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateDelay)
			n.asOlderRecord(job.Key())

			n.waitReady(job.Key())
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
		})

		Convey("a dependent job replaced by a live rerun, added by the user, is ready at RAM 300 once its "+
			"dependency is satisfied", func() {
			n := newNoHoldTest(ctx, t)
			parent := n.newJob("d1-4-rerun-parent", 0, jobOverridePreferSystemReqs)
			parent.DepGroups = []string{"nohold-d1-4-rerun-parent"}
			n.add(parent)

			n.seedLearned("d1-4-rerun")
			childKey := n.addCompleteDependent(ctx, "d1-4-rerun", parent)
			n.gateRAC(ctx)

			rerun := n.newJob("d1-4-rerun", 0, jobOverridePreferSystemReqs)
			rerun.Cmd += noHoldChildCmd
			rerun.Dependencies = Dependencies{NewDepGroupDependency("nohold-d1-4-rerun-parent")}
			So(rerun.Key(), ShouldEqual, childKey)

			inserts, _, err := n.jq.Add([]*Job{rerun}, os.Environ(), false)
			So(err, ShouldBeNil)
			So(inserts, ShouldEqual, 1)

			sjob, _ := n.serverJob(childKey)
			sjob.RLock()
			So(sjob.State, ShouldNotEqual, JobStateComplete)
			sjob.RUnlock()
			So(n.itemState(childKey), ShouldEqual, queue.ItemStateDependent)
			So(n.serverJobRAM(childKey), ShouldEqual, 10)

			n.archive(parent, 5)

			n.waitReady(childKey)
			So(n.serverJobRAM(childKey), ShouldEqual, noHoldLearnedRAM)
		})
	})

	Convey("Given a runner command, a limit group with its 1 slot in use and learned 300, an added job of "+
		"that limit group is in the RAM 300 group with its limit group", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.seedLearned("d1-5")

		first := n.newJob("d1-5", 0, jobOverridePreferSystemReqs)
		first.LimitGroups = []string{"nohold:1"}
		n.add(first)
		So(pollUntil(n.server.racIdle), ShouldBeTrue)
		n.reserveStarted(first, n.groupFor(noHoldLearnedRAM, "nohold"), os.Getpid())
		n.gateRAC(ctx)

		second := n.newJob("d1-5", 0, jobOverridePreferSystemReqs)
		second.Cmd += " second"
		second.LimitGroups = []string{"nohold"}
		n.add(second)

		_, item := n.serverJob(second.Key())
		So(n.serverJobRAM(second.Key()), ShouldEqual, noHoldLearnedRAM)
		So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldLearnedRAM, "nohold"))
	})

	Convey("Given the RAC gated, adding 10 jobs does no RAC scan work", t, func() {
		n := newNoHoldTest(ctx, t)
		n.gateRAC(ctx)
		n.server.racScanWork.Store(0)

		jobs := make([]*Job, 0, 10)
		for i := range 10 {
			jobs = append(jobs, n.newJob(fmt.Sprintf("d1-6-%d", i), 0, jobOverridePreferSystemReqs))
		}

		n.add(jobs...)

		So(n.server.racScanWork.Load(), ShouldEqual, 0)
	})

	Convey("Given learned 300 for a ReqGroup never loaded, adding a job loads it once, before its item is "+
		"queued, and its item has RAM 300", t, func() {
		loads := countReqStatLoads()
		n := newNoHoldTest(ctx, t)

		Reset(func() { reqStatLoadHook = nil })
		n.gateRAC(ctx)

		job := n.newJob("d1-7", 0, jobOverridePreferSystemReqs)
		states := n.recordItemStatesOnLoad(loads, job)

		n.seedLearned("d1-7")
		_, current := n.server.db.cachedReqGroupRecs(job.ReqGroup)
		So(current, ShouldBeFalse)

		n.add(job)

		So(loads.count(job.ReqGroup), ShouldEqual, 1)
		So(states(), ShouldResemble, []queue.ItemState{""})
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
	})

	Convey("Given a buried job and learned 300 seeded after its bury, after a restart a kick loads its "+
		"ReqGroup once, while its item is buried, and its item has RAM 300", t, func() {
		loads := countReqStatLoads()
		n := newNoHoldTest(ctx, t)

		Reset(func() { reqStatLoadHook = nil })

		job := n.addJob("d1-8", 0, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)

		n.failRun(job, FailReasonExit, 5)
		So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateBury)
		n.seedLearned("d1-8")

		n.restart(ctx)
		n.gateRAC(ctx)

		states := n.recordItemStatesOnLoad(loads, job)
		loads.reset()

		n.kick(job)

		So(loads.count(job.ReqGroup), ShouldEqual, 1)
		So(states(), ShouldResemble, []queue.ItemState{queue.ItemStateBury})
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
	})

	Convey("Given a suspended job and learned 300 seeded after its suspend, after a restart a resume loads "+
		"its ReqGroup once, while its item is suspended, and its item has RAM 300", t, func() {
		loads := countReqStatLoads()
		n := newNoHoldTest(ctx, t)

		Reset(func() { reqStatLoadHook = nil })

		job := n.addJob("d1-9", 0, jobOverridePreferSystemReqs)
		n.suspend(job)
		n.seedLearned("d1-9")

		n.restart(ctx)
		n.gateRAC(ctx)

		states := n.recordItemStatesOnLoad(loads, job)
		loads.reset()

		n.resume(job)

		So(loads.count(job.ReqGroup), ShouldEqual, 1)
		So(states(), ShouldResemble, []queue.ItemState{queue.ItemStateSuspended})
		So(n.itemState(job.Key()), ShouldEqual, queue.ItemStateReady)
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
	})

	Convey("Given a runner command, a 2s release delay and a job with 3 retries that fails for RAM, it "+
		"waits in the RAM 10 group at the bumped RAM, and is in the bumped group once its delay expires",
		t, func() {
			n := newNoHoldTest(ctx, t, noHoldServerRC, noHoldSlowRetry)
			job := n.addJob("d1-10", 3, jobOverridePreferSystemReqs)
			n.gateRAC(ctx)

			n.failRAMInGroup(job, n.groupFor(10))

			_, item := n.serverJob(job.Key())
			So(item.Stats().State, ShouldEqual, queue.ItemStateDelay)
			So(item.ReserveGroup, ShouldEqual, n.groupFor(10))
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldBumpedRAM)

			n.waitReady(job.Key())

			sjob, item := n.serverJob(job.Key())
			So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldBumpedRAM))
			So(sjob.getSchedulerGroup(), ShouldEqual, n.groupFor(noHoldBumpedRAM))
		})
}

// TestNoHoldRecoveredReadyPrepared covers spec D2 tests 1 and 2: a ready job
// recovered by a restarted manager is prepared like any other item entering
// ready.
func TestNoHoldRecoveredReadyPrepared(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a stopped manager whose database holds a ready job at RAM 10 with learned 300", t, func() {
		n := newNoHoldTest(ctx, t)
		job := n.addJob("d2", 0, jobOverridePreferSystemReqs)
		n.gateRAC(ctx)
		So(n.serverJobRAM(job.Key()), ShouldEqual, 10)
		n.seedLearned("d2")

		recoverJobs := n.restartPaused(ctx)

		Convey("a manager given a runner command while its recovery is held queues the job in the RAM 300 "+
			"group at RAM 300", func() {
			n.server.setRC(serverRC)
			n.gateRAC(ctx)
			recoverJobs()

			sjob, item := n.serverJob(job.Key())
			So(item.Stats().State, ShouldEqual, queue.ItemStateReady)
			So(item.ReserveGroup, ShouldEqual, n.groupFor(noHoldLearnedRAM))
			So(sjob.getSchedulerGroup(), ShouldEqual, item.ReserveGroup)
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
		})

		Convey("a manager with no runner command queues the job in group \"\" at RAM 300", func() {
			n.gateRAC(ctx)
			recoverJobs()

			_, item := n.serverJob(job.Key())
			So(item.Stats().State, ShouldEqual, queue.ItemStateReady)
			So(item.ReserveGroup, ShouldEqual, "")
			So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)
		})
	})
}

// TestNoHoldRACFloor covers spec D3 test 1: the ready-added callback still
// re-prepares a long-waiting ready job from learning newer than its
// preparation, so ready-time preparation is a floor, not a freeze.
func TestNoHoldRACFloor(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("A long-waiting ready job keeps picking up newer learning", t, func() {
		n := newNoHoldTest(ctx, t, noHoldServerRC)
		n.seedLearned("d3-1")

		job := n.addJob("d3-1", 0, jobOverridePreferSystemReqs)

		// the item's ReserveGroup is read through HasReady, which takes the
		// queue's lock, since the ungated RAC may be setting it.
		learnedGroup, newerGroup := n.groupFor(noHoldLearnedRAM), n.groupFor(noHoldNewerLearnedRAM)
		So(n.server.q.HasReady(learnedGroup), ShouldBeTrue)
		So(n.serverJobRAM(job.Key()), ShouldEqual, noHoldLearnedRAM)

		seedReqGroupStats(n.server, bucketJobRAM, job.ReqGroup, noHoldNewerLearnedRAM)
		n.server.q.TriggerReadyAddedCallback(ctx)

		So(pollUntilFor(noHoldReserveWait, func() bool {
			item, err := n.server.q.Get(job.Key())
			if err != nil {
				return false
			}

			sjob, ok := item.Data().(*Job)
			if !ok {
				return false
			}

			sjob.RLock()
			ram := sjob.Requirements.RAM
			sjob.RUnlock()

			return ram == noHoldNewerLearnedRAM && sjob.getSchedulerGroup() == newerGroup &&
				n.server.q.HasReady(newerGroup) && !n.server.q.HasReady(learnedGroup)
		}), ShouldBeTrue)
	})
}

// heldLoad holds the loads it is given to hold until released.
type heldLoad struct {
	once     sync.Once
	entered  chan struct{}
	enterOne sync.Once
	released chan struct{}
}

// hold is a reqStatLoads onLoad that blocks each load until release.
func (h *heldLoad) hold(string) {
	h.enterOne.Do(func() { close(h.entered) })
	<-h.released
}

// started reports whether a load reached hold within a second.
func (h *heldLoad) started() bool {
	select {
	case <-h.entered:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// release lets every held load, and every later one, go.
func (h *heldLoad) release() {
	h.once.Do(func() { close(h.released) })
}
