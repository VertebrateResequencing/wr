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
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/ugorji/go/codec"
	bolt "go.etcd.io/bbolt"
)

// This file holds the reservation durability tests that hold bbolt's writer or
// write bbolt records directly, so are deleted with bbolt (spec F5).

// TestReserveDurabilityStalledWrite proves a reservation whose write cannot reach
// disk is still answered within ReserveWriteWait, well inside the client's request
// timeout, so the runner gets the job instead of timing out and leaving it
// reserved to nobody; and that the job then runs once.
func TestReserveDurabilityStalledWrite(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		bound  = 300 * time.Millisecond
		margin = 2 * time.Second

		// how long the write is stalled: long past bound+margin, so a manager
		// that waits for the commit answers too late.
		stall = 5 * time.Second
	)

	ctx := context.Background()

	Convey("Given a manager whose reservation write is stalled past ReserveWriteWait", t, func() {
		config, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ReserveWriteWait = bound

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		marker := filepath.Join(t.TempDir(), "runs")

		inserts, _, err := jq.Add([]*Job{{
			Cmd: "echo run >> " + marker, Cwd: testCwd, RepGroup: reserveDurabilityRepGroup,
			ReqGroup: reserveDurabilityRepGroup, Requirements: standardReqs,
		}}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 1)

		logs := clog.ToBufferAtLevel("warn")

		defer clog.ToDefault()

		// holding bolt's single write transaction stalls every best-effort drain.
		holdTx, err := server.boltDB().bolt.Begin(true)
		So(err, ShouldBeNil)

		released := make(chan struct{})
		timer := time.AfterFunc(stall, func() {
			_ = holdTx.Rollback() //nolint:errcheck // releasing the stall

			close(released)
		})

		start := time.Now()
		reserved, errr := jq.Reserve(2 * time.Second)
		elapsed := time.Since(start)

		if timer.Stop() {
			So(holdTx.Rollback(), ShouldBeNil)
		} else {
			soReleasedWithin(released, stall)
		}

		So(errr, ShouldBeNil)
		So(reserved, ShouldNotBeNil)
		So(elapsed, ShouldBeGreaterThanOrEqualTo, bound)
		So(elapsed, ShouldBeLessThan, bound+margin)
		So(strings.Contains(logs.String(), "reservation not yet recorded on disk"), ShouldBeTrue)

		So(jq.Execute(ctx, reserved, config.RunnerExecShell), ShouldBeNil)
		So(runCount(marker), ShouldEqual, 1)

		again, errr := jq.Reserve(200 * time.Millisecond)
		So(errr, ShouldBeNil)
		So(again, ShouldBeNil)
	})
}

// TestBestEffortDrainKeepsArrivalOrder proves that when one drain holds a job's
// exit ops, full change and run state, recovery sees whichever arrived last, as
// it did when every write was a full record, and that an exit op whose live
// write is superseded still stores its std. Before, changes were always written
// first, so a release queued before the next reservation overwrote it, and the
// job recovered to ready. A reservation or start is queued either as a full
// change or as a run state; a release is an exit op; a kick and a suspend are
// full changes.
func TestBestEffortDrainKeepsArrivalOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, tc := range drainOrderCases() {
		t.Run(tc.name, func(t *testing.T) {
			database := openReliable4WriteStormDB(t, ctx)
			defer func() { _ = database.close(ctx) }()

			job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]
			key := job.Key()
			ops := newDrainOrderOps(database, job)

			if len(tc.prior) > 0 {
				ops.queue(t, tc.prior)
				database.drainBestEffort(ctx)

				if runStateBucketValue(t, database, bucketJobRunState, key) == nil {
					t.Fatal("the earlier drain committed no run-state record")
				}
			}

			if tc.archived {
				deleteDrainOrderLiveRecord(t, database, key)
			}

			ops.queue(t, tc.order)
			database.drainBestEffort(ctx)

			tc.check(t, database, ops, job)
		})
	}
}

// drainOrderCase is one TestBestEffortDrainKeepsArrivalOrder case: ops
// committed by an earlier drain, whether the job's live record is then deleted,
// and the ops queued for the one drain under test, in arrival order.
type drainOrderCase struct {
	name     string
	prior    []string
	archived bool
	order    []string
	want     drainOrderWant
}

// drainOrderWant is what a drainOrderCase must leave: the recovered job's State,
// whether a run-state record exists, and any further check of the recovered job.
type drainOrderWant struct {
	state    JobState
	runState bool
	more     func(t *testing.T, ops *drainOrderOps, recovered *Job)
}

// The ops of TestBestEffortDrainKeepsArrivalOrder.
const (
	drainRelease         = "release"
	drainReservation     = "reservation"
	drainFullReservation = "full reservation"
	drainKick            = "kick"
	drainStart           = "start"
	drainSuspend         = "suspend"
)

// drainOrderStdE is the stderr a drainRelease stores.
const drainOrderStdE = "failed run's stderr"

// drainOrderCases returns the cases of TestBestEffortDrainKeepsArrivalOrder:
// each is a sequence of releases, reservations, kicks, starts or full changes
// of one job, with what recovery must read after a crash.
func drainOrderCases() []drainOrderCase {
	reservedExitcode := func(t *testing.T, _ *drainOrderOps, recovered *Job) {
		t.Helper()

		if recovered.Exitcode != -1 {
			t.Errorf("recovered Exitcode is %d, want -1", recovered.Exitcode)
		}
	}

	kicked := func(t *testing.T, _ *drainOrderOps, recovered *Job) {
		t.Helper()

		if recovered.UntilBuried != 3 {
			t.Errorf("recovered UntilBuried is %d, want 3", recovered.UntilBuried)
		}
	}

	return []drainOrderCase{
		{name: "a release then a reservation leaves the reservation",
			order: []string{drainRelease, drainReservation},
			want: drainOrderWant{state: JobStateReserved, runState: true,
				more: func(t *testing.T, ops *drainOrderOps, recovered *Job) {
					t.Helper()
					reservedExitcode(t, ops, recovered)

					live := runStateBucketValue(t, ops.database, bucketJobsLive, recovered.Key())
					if !bytes.Equal(live, ops.releaseEncoded) {
						t.Error("the live record is not the release's encoding")
					}
				}}},
		{name: "a reservation then a release leaves the release",
			order: []string{drainReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a release, a reservation and a release leaves the last release",
			order: []string{drainRelease, drainReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a kick then a reservation leaves the reservation over the kick",
			order: []string{drainKick, drainReservation},
			want: drainOrderWant{state: JobStateReserved, runState: true,
				more: func(t *testing.T, ops *drainOrderOps, recovered *Job) {
					t.Helper()
					kicked(t, ops, recovered)

					live := runStateBucketValue(t, ops.database, bucketJobsLive, recovered.Key())
					if !bytes.Equal(live, ops.kickEncoded) {
						t.Error("the live record is not the kick's encoding")
					}
				}}},
		{name: "a reservation then a kick leaves the kick",
			order: []string{drainReservation, drainKick},
			want:  drainOrderWant{state: JobStateReady, more: kicked}},
		{name: "a reservation then a start leaves the start",
			order: []string{drainReservation, drainStart},
			want: drainOrderWant{state: JobStateRunning, runState: true,
				more: func(t *testing.T, _ *drainOrderOps, recovered *Job) {
					t.Helper()

					if recovered.Pid != 7 {
						t.Errorf("recovered Pid is %d, want 7", recovered.Pid)
					}
				}}},
		{name: "a full change after a committed run state supersedes it",
			prior: []string{drainReservation}, order: []string{drainSuspend},
			want: drainOrderWant{state: JobStateSuspended}},
		{name: "a release after a committed run state supersedes it",
			prior: []string{drainReservation}, order: []string{drainRelease},
			want: drainOrderWant{state: JobStateDelayed}},
		{name: "a reservation of an archived job writes nothing",
			archived: true, order: []string{drainReservation}},
		{name: "a kick then a release leaves the release",
			order: []string{drainKick, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a release then a kick leaves the kick",
			order: []string{drainRelease, drainKick},
			want:  drainOrderWant{state: JobStateReady, more: kicked}},
		{name: "a release then a full reservation leaves the reservation",
			order: []string{drainRelease, drainFullReservation},
			want:  drainOrderWant{state: JobStateReserved}},
		{name: "a full reservation then a release leaves the release",
			order: []string{drainFullReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
		{name: "a release, a full reservation and a release leaves the last release",
			order: []string{drainRelease, drainFullReservation, drainRelease},
			want:  drainOrderWant{state: JobStateDelayed}},
	}
}

// check asserts what the drains of tc left for job: its run-state record,
// stored stderr and recovered state, or for an archived job that neither of its
// records exists. The run-state record is checked before recovery, which drops
// a stale one.
func (tc drainOrderCase) check(t *testing.T, database *db, ops *drainOrderOps, job *Job) {
	t.Helper()

	key := job.Key()
	runState := runStateBucketValue(t, database, bucketJobRunState, key)

	if tc.archived {
		if live := runStateBucketValue(t, database, bucketJobsLive, key); live != nil || runState != nil {
			t.Errorf("an archived job has a live record (%t) or a run-state record (%t)", live != nil, runState != nil)
		}

		return
	}

	if got := runState != nil; got != tc.want.runState {
		t.Errorf("run-state record present is %t, want %t", got, tc.want.runState)
	}

	if slices.Contains(tc.order, drainRelease) {
		if got := storedStdE(t, database, key); string(got) != drainOrderStdE {
			t.Errorf("stored stderr is %q, want %q", got, drainOrderStdE)
		}
	}

	if got := storedLiveJobState(t, database, key); got != tc.want.state {
		t.Errorf("stored live state is %q, want %q", got, tc.want.state)
	}

	recovered := recoveredJob(t, database, key)
	if recovered.State != tc.want.state {
		t.Errorf("recovered state is %q, want %q", recovered.State, tc.want.state)
	}

	if tc.want.more != nil {
		tc.want.more(t, ops, recovered)
	}
}

// deleteDrainOrderLiveRecord deletes key's live record, as an archive does.
func deleteDrainOrderLiveRecord(t *testing.T, database *db, key string) {
	t.Helper()

	if err := database.bolt.Update(func(tx *bolt.Tx) error {
		return deleteLiveRecord(tx, []byte(key))
	}); err != nil {
		t.Fatalf("could not delete the live record: %v", err)
	}
}

// drainOrderOps queues TestBestEffortDrainKeepsArrivalOrder's ops of one job,
// each encoding the job as it is at that moment, without kicking the writer.
type drainOrderOps struct {
	database       *db
	job            *Job
	kickEncoded    []byte
	releaseEncoded []byte
}

// newDrainOrderOps returns a drainOrderOps for job in database.
func newDrainOrderOps(database *db, job *Job) *drainOrderOps {
	return &drainOrderOps{database: database, job: job}
}

// queue queues each of order's ops in turn.
func (o *drainOrderOps) queue(t *testing.T, order []string) {
	t.Helper()

	job := o.job

	for _, op := range order {
		switch op {
		case drainRelease:
			job.State = JobStateDelayed
			job.Exitcode = 1
			queueUnkickedBestEffortExit(t, o.database, job, []byte(drainOrderStdE))
			o.releaseEncoded = o.encode(t)
		case drainReservation, drainFullReservation:
			job.State = JobStateReserved
			job.Exitcode = -1

			o.queueReservation(t, op == drainFullReservation)
		case drainKick:
			job.State = JobStateReady
			job.UntilBuried = 3
			queueUnkickedBestEffortChange(t, o.database, job)
			o.kickEncoded = o.encode(t)
		case drainStart:
			job.State = JobStateRunning
			job.Pid = 7
			queueUnkickedBestEffortRunState(t, o.database, job)
		case drainSuspend:
			job.State = JobStateSuspended
			queueUnkickedBestEffortChange(t, o.database, job)
		default:
			t.Fatalf("unknown op %q", op)
		}
	}
}

// queueReservation queues the job's reservation as a full change if full, or
// else as a run state.
func (o *drainOrderOps) queueReservation(t *testing.T, full bool) {
	t.Helper()

	if full {
		queueUnkickedBestEffortChange(t, o.database, o.job)

		return
	}

	queueUnkickedBestEffortRunState(t, o.database, o.job)
}

// encode returns the job encoded as queueUnkickedBestEffortChange encodes it.
func (o *drainOrderOps) encode(t *testing.T) []byte {
	t.Helper()

	var encoded []byte

	if err := codec.NewEncoderBytes(&encoded, o.database.ch).Encode(o.job); err != nil {
		t.Fatalf("could not encode the job: %v", err)
	}

	return encoded
}

// TestBestEffortChangeKeepsEncodeOrder proves that of two concurrent writes of
// one job, the one that encoded the newer state is what recovery sees, whether
// the newer write, the reservation, is a full record or a run state, and when
// the older write is the run state and a full change overtakes it. A kick that
// encoded the job as ready, and was then overtaken by the job's reservation,
// used to queue after that reservation and win the coalescing. The
// reservation's waiter was told its write had committed while the ready record
// replaced it, so a crash before Started recovered the job to run a second time.
func TestBestEffortChangeKeepsEncodeOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	kick := encodeOrderWrite{write: (*db).updateJobAfterChangeDurable}
	reserve := func(write func(database *db, job *Job) error) encodeOrderWrite {
		return encodeOrderWrite{change: func(job *Job) { job.State = JobStateReserved }, write: write}
	}

	for _, tc := range []struct {
		name         string
		older, newer encodeOrderWrite
	}{
		{"a full reservation", kick, reserve((*db).updateJobAfterChangeDurable)},
		{"a run-state reservation", kick, reserve((*db).updateJobRunStateDurable)},
		{"a run-state reservation with a bounded wait", kick, reserve(func(database *db, job *Job) error {
			return database.updateJobRunStateDurableWithin(job, serverReserveWriteWait())
		})},
		{"a full change overtaking a run-state reservation", reserve((*db).updateJobRunStateDurable),
			encodeOrderWrite{change: func(job *Job) {
				job.State = JobStateReady
				job.UntilBuried = 3
			}, write: (*db).updateJobAfterChangeDurable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testBestEffortChangeKeepsEncodeOrder(t, tc.older, tc.newer)
		})
	}
}

// encodeOrderWrite is one of TestBestEffortChangeKeepsEncodeOrder's writes: a
// change made to the job under its lock, if any, then the write of it.
type encodeOrderWrite struct {
	change func(job *Job)
	write  func(database *db, job *Job) error
}

// apply makes w's change to job, then writes it.
func (w encodeOrderWrite) apply(database *db, job *Job) error {
	if w.change != nil {
		job.Lock()
		w.change(job)
		job.Unlock()
	}

	return w.write(database, job)
}

// testBestEffortChangeKeepsEncodeOrder is TestBestEffortChangeKeepsEncodeOrder
// with older paused between encoding the job and queueing its write while newer
// changes and writes the job.
func testBestEffortChangeKeepsEncodeOrder(t *testing.T, older, newer encodeOrderWrite) {
	t.Helper()

	ctx := context.Background()

	database := openReliable4WriteStormDB(t, ctx)
	defer func() { _ = database.close(ctx) }()

	job := reliable4WSSeedLiveJobs(t, ctx, database, 1)[0]

	var paused atomic.Bool

	encoded, resume := make(chan struct{}), make(chan struct{})

	jobChangeEncodedHook = func() {
		if paused.CompareAndSwap(false, true) {
			close(encoded)
			<-resume
		}
	}
	defer func() { jobChangeEncodedHook = nil }()

	olderErr, newerErr := make(chan error, 1), make(chan error, 1)

	go func() { olderErr <- older.apply(database, job) }()

	<-encoded

	go func() { newerErr <- newer.apply(database, job) }()

	// if writes queue in the order they encoded, the newer write cannot finish
	// while the older is paused, so this only waits long enough for it to try.
	var errs []error

	select {
	case err := <-newerErr:
		t.Log("the newer write committed while the older was still unqueued")

		errs = append(errs, err)
	case <-time.After(time.Second):
	}

	close(resume)

	errs = append(errs, <-olderErr)
	if len(errs) == 1 {
		errs = append(errs, <-newerErr)
	}

	for _, err := range errs {
		if err != nil {
			t.Fatalf("durable write failed: %v", err)
		}
	}

	job.RLock()
	wantState, wantUntilBuried := job.State, job.UntilBuried
	job.RUnlock()

	if got := storedLiveJobState(t, database, job.Key()); got != wantState {
		t.Errorf("live record state is %q, want %q", got, wantState)
	}

	recovered := recoveredJob(t, database, job.Key())
	if recovered.State != wantState {
		t.Errorf("recovered state is %q, want %q", recovered.State, wantState)
	}

	if recovered.UntilBuried != wantUntilBuried {
		t.Errorf("recovered UntilBuried is %d, want %d", recovered.UntilBuried, wantUntilBuried)
	}
}

// storedStdE returns the stderr stored for key.
func storedStdE(t *testing.T, database *db, key string) []byte {
	t.Helper()

	var stde []byte

	err := database.bolt.View(func(tx *bolt.Tx) error {
		stde = bytes.Clone(tx.Bucket(bucketStdE).Get([]byte(key)))

		return nil
	})
	if err != nil {
		t.Fatalf("could not read the job's stderr: %v", err)
	}

	return stde
}

// queueUnkickedBestEffortExit queues job's exit op exactly as updateJobAfterExit
// does, but WITHOUT kicking the writer goroutine, so the caller's own
// drainBestEffort picks it up in the same batch as anything else queued so.
func queueUnkickedBestEffortExit(t *testing.T, database *db, job *Job, stde []byte) {
	t.Helper()

	database.Lock()
	defer database.Unlock()

	job.RLock()
	exit, err := database.snapshotJobExitLocked(job, nil, stde, false)
	job.RUnlock()

	if err != nil {
		t.Fatalf("could not snapshot the job's exit: %s", err)
	}

	database.updatingAfterJobExit.Add(1)

	database.wgMutex.Lock()
	defer database.wgMutex.Unlock()

	database.beMu.Lock()
	defer database.beMu.Unlock()

	database.enqueueExitLocked(exit, nil)
}

// reserveNotDurableLine is one logged reserveNotDurableLogMsg line's key and
// total.
type reserveNotDurableLine struct {
	key   string
	total string
}

// reserveNotDurableLines returns, in log order, the key and total of every
// reserveNotDurableLogMsg line logged at info level in a logfmt log.
func reserveNotDurableLines(log string) []reserveNotDurableLine {
	var lines []reserveNotDurableLine

	for line := range strings.Lines(log) {
		if !strings.Contains(line, `msg="`+reserveNotDurableLogMsg+`"`) || !strings.Contains(line, "lvl=info ") {
			continue
		}

		var found reserveNotDurableLine

		for field := range strings.FieldsSeq(line) {
			if v, ok := strings.CutPrefix(field, "key="); ok {
				found.key = v
			} else if v, ok := strings.CutPrefix(field, "total="); ok {
				found.total = v
			}
		}

		lines = append(lines, found)
	}

	return lines
}

// reservesNotDurable is database's count of non-durable hand-outs.
func reservesNotDurable(database *db) uint64 {
	database.rndMu.Lock()
	defer database.rndMu.Unlock()

	return database.reservesNotDurable
}

// TestReserveNotDurableLogged proves every reservation handed out before it was
// durable is logged once, with its key and a gap-free running total, to the
// manager's info-level log file, and that durable ones are not.
func TestReserveNotDurableLogged(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager logging as setupManagerLogging sets it up, with reservation writes stalled", t, func() {
		const stall = 5 * time.Second

		logPath := filepath.Join(t.TempDir(), "log")
		handlers, err := clog.CreateFileHandlersAtLevels(logPath, "info", "warn")
		So(err, ShouldBeNil)

		clog.AddHandler(handlers[0])

		defer clog.ToDefault()

		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)
		serverConfig.Timings.ReserveWriteWait = 300 * time.Millisecond

		server, _, token, err := serve(clog.ContextWithLogHandler(ctx, handlers[1]), serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		inserts, _, err := jq.Add([]*Job{
			{Cmd: "true A", Cwd: testCwd, RepGroup: reserveDurabilityRepGroup,
				ReqGroup: reserveDurabilityRepGroup, Requirements: standardReqs},
			{Cmd: "true B", Cwd: testCwd, RepGroup: reserveDurabilityRepGroup,
				ReqGroup: reserveDurabilityRepGroup, Requirements: standardReqs},
		}, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 2)

		holdTx, err := server.boltDB().bolt.Begin(true)
		So(err, ShouldBeNil)

		released := make(chan struct{})
		timer := time.AfterFunc(stall, func() {
			_ = holdTx.Rollback() //nolint:errcheck // releasing the stall

			close(released)
		})

		jobA, errA := jq.Reserve(2 * time.Second)
		jobB, errB := jq.Reserve(2 * time.Second)

		if timer.Stop() {
			So(holdTx.Rollback(), ShouldBeNil)
		} else {
			soReleasedWithin(released, stall)
		}

		So(errA, ShouldBeNil)
		So(jobA, ShouldNotBeNil)
		So(errB, ShouldBeNil)
		So(jobB, ShouldNotBeNil)

		content, err := os.ReadFile(logPath)
		So(err, ShouldBeNil)

		So(reserveNotDurableLines(string(content)), ShouldResemble, []reserveNotDurableLine{
			{key: jobA.Key(), total: "1"},
			{key: jobB.Key(), total: "2"},
		})
		So(string(content), ShouldContainSubstring, "reservation not yet recorded on disk")
	})

	Convey("Given a manager whose reservation writes commit promptly", t, func() {
		_, serverConfig, addr, standardReqs, clientConnectTime := startDurabilityConfig(t)

		server, _, token, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		jq, err := Connect(addr, serverConfig.CAFile, serverConfig.CertDomain, token, clientConnectTime)
		So(err, ShouldBeNil)

		defer disconnect(jq)

		jobs := make([]*Job, 3)
		for i := range jobs {
			jobs[i] = &Job{
				Cmd: fmt.Sprintf("true %d", i), Cwd: testCwd, RepGroup: reserveDurabilityRepGroup,
				ReqGroup: reserveDurabilityRepGroup, Requirements: standardReqs,
			}
		}

		inserts, _, err := jq.Add(jobs, os.Environ(), true)
		So(err, ShouldBeNil)
		So(inserts, ShouldEqual, 3)

		logs := clog.ToBufferAtLevel("info")

		defer clog.ToDefault()

		reserved := 0

		for range jobs {
			job, errr := jq.Reserve(2 * time.Second)
			if errr == nil && job != nil {
				reserved++
			}
		}

		So(reserved, ShouldEqual, 3)
		So(reserveNotDurableLines(logs.String()), ShouldBeEmpty)
		So(reservesNotDurable(server.boltDB()), ShouldEqual, 0)
	})

	Convey("Given a manager whose database is closed", t, func() {
		_, serverConfig, _, _, _ := startDurabilityConfig(t)

		server, _, _, err := serve(ctx, serverConfig)
		So(err, ShouldBeNil)

		defer server.Stop(ctx, true)

		logs := clog.ToBufferAtLevel("info")

		defer clog.ToDefault()

		setClosed := func(closed bool) {
			server.boltDB().Lock()
			server.boltDB().closed = closed
			server.boltDB().Unlock()
		}

		setClosed(true)

		job := &Job{Cmd: "true A", Cwd: testCwd, RepGroup: reserveDurabilityRepGroup}
		server.persistReservation(ctx, job)

		setClosed(false)

		So(reserveNotDurableLines(logs.String()), ShouldResemble, []reserveNotDurableLine{
			{key: job.Key(), total: "1"},
		})
	})

	Convey("Given logs captured at info, concurrent non-durable hand-outs log a gap-free total in order", t, func() {
		const (
			goroutines = 50
			perKey     = 20
		)

		logs := clog.ToBufferAtLevel("info")

		defer clog.ToDefault()

		database := &db{}

		var wg sync.WaitGroup

		for g := range goroutines {
			wg.Go(func() {
				for range perKey {
					database.logReserveNotDurable(fmt.Sprintf("key%d", g))
				}
			})
		}

		wg.Wait()

		lines := reserveNotDurableLines(logs.String())
		So(len(lines), ShouldEqual, goroutines*perKey)

		outOfOrder := 0
		perKeySeen := make(map[string]int)

		for i, line := range lines {
			if line.total != strconv.Itoa(i+1) {
				outOfOrder++
			}

			perKeySeen[line.key]++
		}

		So(outOfOrder, ShouldEqual, 0)
		So(len(perKeySeen), ShouldEqual, goroutines)
		So(slices.Max(slices.Collect(maps.Values(perKeySeen))), ShouldEqual, perKey)
		So(slices.Min(slices.Collect(maps.Values(perKeySeen))), ShouldEqual, perKey)
	})
}

// soReleasedWithin waits for released, which a stall's timer closes once it has
// rolled the held transaction back, failing if that takes longer than the stall
// itself: the timer has already fired when this is called, so the rollback is
// under way.
func soReleasedWithin(released <-chan struct{}, stall time.Duration) {
	select {
	case <-released:
	case <-time.After(stall):
		So("the held transaction was not released", ShouldBeBlank)
	}
}
