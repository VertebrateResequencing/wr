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

// This file covers spec C3: each transition appends one state record, and no
// job is handed to a runner before its reservation is durable, even during a
// filesystem stall.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	jqs "github.com/VertebrateResequencing/wr/jobqueue/scheduler"
	"github.com/VertebrateResequencing/wr/jobqueue/wal"
	"github.com/VertebrateResequencing/wr/queue"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// reserveTestRepGroup names the jobs these tests add.
	reserveTestRepGroup = "store_reserve"

	// reserveTestRequestTimeout is C3 test 2's ClientMinRequestTimeout, which
	// the spec says makes reserveDurableWait() reserveTestBound.
	reserveTestRequestTimeout = 800 * time.Millisecond
	reserveTestBound          = 200 * time.Millisecond

	// reserveTestHold is how long C3's stalls hold syncs (or bbolt's drain),
	// and reserveTestAnswered how soon a runner must be answered despite one.
	reserveTestHold     = 2 * time.Second
	reserveTestAnswered = time.Second

	// reserveTestWait bounds the waits for something these tests expect to
	// happen promptly, on a loaded machine.
	reserveTestWait = 10 * time.Second
)

// abandonTestLiveStd is the stdout and stderr tails failFirstAttempt's attempt
// reports in a touch while it runs.
var abandonTestLiveStd = [2]string{"live out", "live err"} //nolint:gochecknoglobals

// abandonTestPid is the pid failFirstAttempt's attempt reports, not the test's
// own, which the runner that next reserves the job reports as its pid.
const abandonTestPid = 99999

// TestStoreReserveStateSeqOrder covers spec C3 acceptance test 5: of a key's
// state records, the one with the higher seq wins, whatever order they were
// appended in.
func TestStoreReserveStateSeqOrder(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const buriedSeq, readySeq = 7, 6

	ctx := context.Background()

	Convey("Given a log store holding a job", t, func() {
		s, _ := openTestLogStore(t, ctx)

		job := testDBJob("echo store reserve seq order", reserveTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)
		So(s.seq.Load(), ShouldBeLessThan, readySeq)

		k, err := parseLogKey(job.Key())
		So(err, ShouldBeNil)

		Convey("when state records of it with seq 7, buried, then seq 6, ready, are appended, it is buried "+
			"once reopened", func() {
			for _, rec := range []struct {
				seq   uint64
				state JobState
			}{{buriedSeq, JobStateBuried}, {readySeq, JobStateReady}} {
				job.State = rec.state
				rs := newLogRunState(job)

				var f logFrames

				s.addStateAt(&f, logRecState, rec.seq, k, &rs)
				So(s.writeAndWait(&f), ShouldBeNil)
			}

			s.seq.Store(buriedSeq)

			reopened := reopenLogStore(ctx, s)

			recovered, live, err := reopened.liveJob(job.Key())
			So(err, ShouldBeNil)
			So(live, ShouldBeTrue)
			So(recovered.State, ShouldEqual, JobStateBuried)
		})
	})
}

// reserveTestManager is a manager these tests run, with a client of it.
type reserveTestManager struct {
	server       *Server
	serverConfig ServerConfig
	jq           *Client
	addr         string
	token        []byte
	connectTime  time.Duration
	reqs         *jqs.Requirements
	stopped      bool
}

// startReserveTestManager starts a manager on a store of kind in a new
// directory, after configure has had its config, and connects a client to it.
// Both are stopped when the Convey block ends.
func startReserveTestManager(t *testing.T, ctx context.Context, kind storeKind,
	configure func(*ServerConfig),
) *reserveTestManager {
	t.Helper()

	return startReserveTestManagerThen(t, ctx, kind, configure, nil)
}

// startReserveTestManagerThen is startReserveTestManager, also calling
// afterStop, if not nil, when the Convey block ends: once the manager has
// stopped, or once the start has failed.
func startReserveTestManagerThen(t *testing.T, ctx context.Context, kind storeKind,
	configure func(*ServerConfig), afterStop func(),
) *reserveTestManager {
	t.Helper()

	var m *reserveTestManager

	Reset(func() {
		if m != nil {
			m.stop(ctx)
		}

		if afterStop != nil {
			afterStop()
		}
	})

	_, serverConfig, addr, reqs, connectTime := startDurabilityConfig(t)
	serverConfig.storeKind = kind
	serverConfig.dontWipeDevDB = true

	if configure != nil {
		configure(&serverConfig)
	}

	server, _, token, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	m = &reserveTestManager{
		server: server, serverConfig: serverConfig, addr: addr, token: token, connectTime: connectTime,
		reqs: reqs,
	}

	So(waitUntilRecovered(server), ShouldBeTrue)

	m.jq = m.connect()

	return m
}

// startAbandonTestManager starts a manager on a store of the given kind whose
// failed jobs are ready again at once, and returns it with a function that
// holds the store's writes for a duration, returning when they are released.
func startAbandonTestManager(t *testing.T, ctx context.Context, kind storeKind) (*reserveTestManager,
	func(time.Duration) time.Time,
) {
	t.Helper()

	configure := func(c *ServerConfig) { c.Timings.ReleaseDelayMin = time.Millisecond }

	if kind == storeKindLog {
		pauseLogFolder()

		syncs := &reserveTestSyncs{}
		syncs.install()

		return startReserveTestManager(t, ctx, kind, configure), syncs.holdFor
	}

	drains := &boltDrainHold{}
	drains.install()

	m := startReserveTestManagerThen(t, ctx, kind, configure, drains.uninstall)

	return m, func(d time.Duration) time.Time {
		drains.holdFor(d)

		return time.Now().Add(d)
	}
}

// connect returns a new client of the manager, disconnected when the Convey
// block ends.
func (m *reserveTestManager) connect() *Client {
	jq, err := Connect(m.addr, m.serverConfig.CAFile, m.serverConfig.CertDomain, m.token, m.connectTime)
	So(err, ShouldBeNil)

	Reset(func() { disconnect(jq) })

	return jq
}

// stop stops the manager, if it has not been stopped already.
func (m *reserveTestManager) stop(ctx context.Context) {
	if m.stopped {
		return
	}

	m.stopped = true

	if m.jq != nil {
		disconnect(m.jq)
	}

	m.server.Stop(ctx, true)
}

// restart stops the manager and starts it again on its store, reconnecting its
// client.
func (m *reserveTestManager) restart(ctx context.Context) {
	m.stop(ctx)

	server, _, token, err := serve(ctx, m.serverConfig)
	So(err, ShouldBeNil)

	m.server, m.token, m.stopped = server, token, false
	So(waitUntilRecovered(server), ShouldBeTrue)

	m.jq = m.connect()
}

// add adds one job named by suffix, returning its key.
func (m *reserveTestManager) add(suffix string, limitGroups ...string) string {
	job := &Job{
		Cmd: "echo store reserve " + suffix, Cwd: testCwd, RepGroup: reserveTestRepGroup,
		ReqGroup: reserveTestRepGroup, Requirements: m.reqs, Retries: 3, LimitGroups: limitGroups,
	}

	inserts, _, err := m.jq.Add([]*Job{job}, os.Environ(), true)
	So(err, ShouldBeNil)
	So(inserts, ShouldEqual, 1)

	return job.Key()
}

// logStore returns the manager's log store.
func (m *reserveTestManager) logStore() *logStore {
	s, ok := m.server.db.(*logStore)
	So(ok, ShouldBeTrue)

	return s
}

// queued returns the item of the keyed job in the manager's queue, and its job.
func (m *reserveTestManager) queued(key string) (*queue.Item, *Job) {
	item, err := m.server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	return item, job
}

// TestStoreReserveHealthy covers spec C3 acceptance test 1: a runner reserving
// from a healthy log store gets the job, and a crash right after leaves it
// reserved to that runner. On either store, its "reservation wait" line counts
// it as durable, and its wait as at least its store's write took.
func TestStoreReserveHealthy(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a manager on a healthy log store holding a ready job, whose syncs each take 200ms", t, func() {
		pauseLogFolder()
		reserveTestReportAtStop()

		syncs := &reserveTestSyncs{}
		syncs.install()
		syncs.set(reserveTestBound, 0)

		logPath, logCtx := managerLogContext(t, ctx)
		m := startReserveTestManager(t, logCtx, storeKindLog, nil)
		key := m.add("healthy")

		Convey("a runner reserving it gets the job, which a crash then leaves reserved by that runner, and "+
			"the next reservation wait line counts it as durable", func() {
			reserved, err := m.jq.Reserve(reserveTestWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, key)

			crashed := durableLogCopy(t, m.logStore())

			recovered, live := reopenedLogJob(ctx, crashed, key)
			So(live, ShouldBeTrue)
			So(recovered.State, ShouldEqual, JobStateReserved)
			So(recovered.ReservedBy, ShouldEqual, m.jq.clientid)

			m.stop(ctx)
			reserveTestHandedOutLine(t, logPath, reserveTestBound)
		})
	})

	Convey("Given a manager on a healthy bbolt store holding a ready job, whose drains each take 200ms", t, func() {
		reserveTestReportAtStop()

		hold := &boltDrainHold{}
		hold.install()

		logPath, logCtx := managerLogContext(t, ctx)
		m := startReserveTestManager(t, logCtx, storeKindBolt, nil)

		Reset(hold.uninstall)

		key := m.add("healthy bolt")

		Convey("a runner reserving it gets the job, and the next reservation wait line counts it as "+
			"durable", func() {
			hold.delayEach(reserveTestBound)

			reserved, err := m.jq.Reserve(reserveTestWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)
			So(reserved.Key(), ShouldEqual, key)

			m.stop(ctx)
			reserveTestHandedOutLine(t, logPath, reserveTestBound)
		})
	})
}

// reserveTestReportAtStop makes managers started from now until the Convey
// block ends log their wait lines only when they stop.
func reserveTestReportAtStop() {
	productionInterval := archiveFoldReportInterval
	archiveFoldReportInterval = time.Hour

	Reset(func() { archiveFoldReportInterval = productionInterval })
}

// durableLogCopy copies what is durable of the log store s's directory to a
// new directory, as a crash would leave it, and returns that directory: every
// wal file up to the writer's durable end, cut there, and its other files.
func durableLogCopy(t *testing.T, s *logStore) string {
	t.Helper()

	file, end := s.w.Durable()
	dst := filepath.Join(t.TempDir(), "crashed")
	So(os.MkdirAll(dst, logStoreDirPerm), ShouldBeNil)

	walFiles, err := walFileNumbers(s.dir)
	So(err, ShouldBeNil)

	isWal := make(map[string]uint32, len(walFiles))
	for _, n := range walFiles {
		isWal[filepath.Base(walFilePath(s.dir, n))] = n
	}

	entries, err := os.ReadDir(s.dir)
	So(err, ShouldBeNil)

	for _, entry := range entries {
		n, wal := isWal[entry.Name()]
		if !entry.Type().IsRegular() || (wal && n > file) {
			continue
		}

		copied := filepath.Join(dst, entry.Name())
		So(copyCrashFile(filepath.Join(s.dir, entry.Name()), copied), ShouldBeNil)

		if wal && n == file {
			So(os.Truncate(copied, end), ShouldBeNil)
		}
	}

	return dst
}

// reopenedLogJob returns the keyed job as the log store in dir, opened anew,
// recovers it, and whether it is live.
func reopenedLogJob(ctx context.Context, dir, key string) (*Job, bool) {
	s := openLogStoreIn(ctx, dir)

	defer func() { So(s.close(ctx), ShouldBeNil) }()

	job, live, err := s.liveJob(key)
	So(err, ShouldBeNil)

	return job, live
}

// reserveTestHandedOutLine checks that the manager log at logPath has one
// "reservation wait" line, for one reservation handed out once durable, which
// waited at least waited.
func reserveTestHandedOutLine(t *testing.T, logPath string, waited time.Duration) {
	t.Helper()

	lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
	So(lines, ShouldHaveLength, 1)
	So(storeStatsN(t, lines), ShouldEqual, 1)
	So(storeStatsSum(t, lines, "notDurable"), ShouldEqual, 0)
	So(storeStatsSum(t, lines, "abandoned"), ShouldEqual, 0)
	So(storeStatsDuration(t, lines[0], "max"), ShouldBeGreaterThanOrEqualTo, waited)
}

// TestStoreReserveStalled covers spec C3 acceptance tests 2, 3 and 6: a
// reservation whose record is not durable within reserveDurableWait is
// abandoned, its job ready again with nothing spent, recorded after its
// reservation, and counted as abandoned; one handed out regardless is counted
// as notDurable.
func TestStoreReserveStalled(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store manager whose reservations wait at most 200ms, holding a ready job", t, func() {
		pauseLogFolder()
		reserveTestReportAtStop()

		syncs := &reserveTestSyncs{}
		syncs.install()

		logPath, logCtx := managerLogContext(t, ctx)
		m := startReserveTestManager(t, logCtx, storeKindLog, nil)

		Reset(setClientMinRequestTimeout(reserveTestRequestTimeout))

		So(reserveDurableWait(), ShouldEqual, reserveTestBound)

		key := m.add("stalled")
		dir := m.serverConfig.DBFile

		reserveHeld := func() (*Job, time.Duration, time.Time) {
			released := syncs.holdFor(reserveTestHold)
			reserving := time.Now()

			reserved, err := m.jq.Reserve(reserveTestBound / 2)
			So(err, ShouldBeNil)

			return reserved, time.Since(reserving), released
		}

		stopAfter := func(released time.Time) {
			time.Sleep(time.Until(released))
			m.stop(ctx)
		}

		Convey("when a runner reserves while every sync is held for 2s", func() {
			reserved, took, released := reserveHeld()

			Convey("it gets no job within 1s, and the job is ready, with no attempt spent, also once "+
				"the hold has ended and the store is reopened", func() {
				So(reserved, ShouldBeNil)
				So(took, ShouldBeGreaterThanOrEqualTo, reserveTestBound)
				So(took, ShouldBeLessThan, reserveTestAnswered)

				item, job := m.queued(key)
				So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

				job.RLock()
				attempts := job.Attempts
				job.RUnlock()

				So(attempts, ShouldEqual, 0)

				stopAfter(released)

				recovered, live := reopenedLogJob(ctx, dir, key)
				So(live, ShouldBeTrue)
				So(recovered.State, ShouldEqual, JobStateReady)
				So(recovered.Attempts, ShouldEqual, 0)
			})

			Convey("the wal holds its reserve state record and then, with a higher seq, its release", func() {
				stopAfter(released)

				records := stateRecordsOf(dir, key)

				reserve, release := -1, -1

				for i, r := range records {
					switch recordedRunState(r).State {
					case JobStateReserved:
						reserve = i
					case JobStateReady:
						if reserve >= 0 && release < 0 {
							release = i
						}
					default:
					}
				}

				So(reserve, ShouldBeGreaterThanOrEqualTo, 0)
				So(release, ShouldBeGreaterThan, reserve)
				So(records[release].seq, ShouldBeGreaterThan, records[reserve].seq)
			})

			Convey("the next reservation wait line counts it as abandoned, not notDurable", func() {
				stopAfter(released)

				lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
				So(storeStatsN(t, lines), ShouldEqual, 1)
				So(storeStatsSum(t, lines, "notDurable"), ShouldEqual, 0)
				So(storeStatsSum(t, lines, "abandoned"), ShouldEqual, 1)
				So(storeStatsDuration(t, lines[0], "max"), ShouldBeGreaterThanOrEqualTo, reserveTestBound)
			})
		})

		Convey("when a runner reserves while every sync is held for 2s and the manager does not wait for the "+
			"reservation, it gets the job and the next reservation wait line counts it as notDurable", func() {
			persistReservationNoWait.Store(true)

			reserved, _, released := reserveHeld()

			persistReservationNoWait.Store(false)

			So(reserved, ShouldNotBeNil)

			stopAfter(released)

			lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
			So(storeStatsN(t, lines), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "notDurable"), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "abandoned"), ShouldEqual, 0)
		})
	})
}

// stateRecordsOf returns the state records of the keyed job in the wal files of
// the log store directory dir, in log order.
func stateRecordsOf(dir, key string) []logRecord {
	k, err := parseLogKey(key)
	So(err, ShouldBeNil)

	walFiles, err := walFileNumbers(dir)
	So(err, ShouldBeNil)

	var records []logRecord

	for _, n := range walFiles {
		data, errr := os.ReadFile(walFilePath(dir, n))
		So(errr, ShouldBeNil)

		header := true

		_, errs := wal.Scan(bytes.NewReader(data), func(_ wal.Ref, rec []byte) error {
			if header {
				header = false

				return nil
			}

			r, errp := parseLogRecord(bytes.Clone(rec))
			if errp == nil && r.typ == logRecState && r.key == k {
				records = append(records, r)
			}

			return errp
		})
		So(errs, ShouldBeNil)
	}

	return records
}

// recordedRunState decodes a state record's run state.
func recordedRunState(r logRecord) logRunState {
	var rs logRunState
	So(decodeFlatRunState(r.body, &rs), ShouldBeNil)

	return rs
}

// TestStoreReserveAbandonKeepsLastAttempt covers spec C3's step 4: a job that
// failed, whose next reservation is abandoned, keeps the details of the attempt
// that failed, as wr status gets them, also after a restart, on either store.
func TestStoreReserveAbandonKeepsLastAttempt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	for _, kind := range []storeKind{storeKindLog, storeKindBolt} {
		Convey("Given a manager on a "+storeKindName(kind)+" store holding a job whose first attempt failed", t,
			func() {
				m, hold := startAbandonTestManager(t, ctx, kind)

				Reset(setClientMinRequestTimeout(reserveTestRequestTimeout))

				key := m.add("last attempt")
				failed := failFirstAttempt(m, key)

				Convey("when its next reservation is abandoned, it keeps that attempt's details, also after a "+
					"restart", func() {
					released := hold(reserveTestHold)

					reserved, errr := m.jq.Reserve(reserveTestBound / 2)
					So(errr, ShouldBeNil)
					So(reserved, ShouldBeNil)

					item, _ := m.queued(key)
					So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

					So(lastAttemptOf(m.jq, key), ShouldResemble, failed)
					So(managerLiveStd(m, key), ShouldResemble, abandonTestLiveStd)

					time.Sleep(time.Until(released))
					m.restart(ctx)

					So(lastAttemptOf(m.jq, key), ShouldResemble, failed)
					So(managerLiveStd(m, key), ShouldResemble, abandonTestLiveStd)
				})
			})
	}
}

// failFirstAttempt reserves, starts and fails the keyed job of m in a
// working directory of its own, after a touch reporting abandonTestLiveStd,
// waits for it to be ready again, and returns that attempt's details, each of
// them set.
func failFirstAttempt(m *reserveTestManager, key string) lastAttempt {
	first, err := m.jq.Reserve(reserveTestWait)
	So(err, ShouldBeNil)
	So(first, ShouldNotBeNil)
	So(m.jq.Started(first, abandonTestPid), ShouldBeNil)

	stderr, err := compress([]byte("boom"))
	So(err, ShouldBeNil)
	stdout, err := compress([]byte("out"))
	So(err, ShouldBeNil)

	first.StdOutC, err = compress([]byte(abandonTestLiveStd[0]))
	So(err, ShouldBeNil)
	first.StdErrC, err = compress([]byte(abandonTestLiveStd[1]))
	So(err, ShouldBeNil)

	_, err = m.jq.Touch(first)
	So(err, ShouldBeNil)

	cwd := filepath.Join(testCwd, "abandon-test-cwd")
	So(m.jq.Release(first, &JobEndState{
		Cwd: cwd, Exited: true, Exitcode: 3, PeakRAM: 42, PeakDisk: 7, CPUtime: time.Second,
		EndTime: time.Now(), Stdout: stdout, Stderr: stderr,
	}, "it failed"), ShouldBeNil)

	item, sjob := m.queued(key)

	So(pollUntilFor(reserveTestWait, func() bool {
		return item.Stats().State == queue.ItemStateReady
	}), ShouldBeTrue)

	// the attempt ran on another host than the runner that next reserves it,
	// so the reservation's own host is not mistaken for it, and gets the host
	// ID a cloud scheduler would have given it, as the local scheduler gives
	// none.
	sjob.Lock()
	sjob.Host, sjob.HostID = "abandon-test-host", "abandon-test-host-id"
	sjob.Unlock()

	failed := lastAttemptOf(m.jq, key)
	So(failed.Exitcode, ShouldEqual, 3)
	So(failed.FailReason, ShouldEqual, "it failed")
	So(failed.Pid, ShouldEqual, abandonTestPid)
	So(failed.Host, ShouldEqual, "abandon-test-host")
	So(failed.HostID, ShouldEqual, "abandon-test-host-id")
	So(failed.ActualCwd, ShouldEqual, cwd)
	So(failed.StdOut, ShouldEqual, "out")
	So(failed.StdErr, ShouldEqual, "boom")
	So(managerLiveStd(m, key), ShouldResemble, abandonTestLiveStd)

	return failed
}

// lastAttemptOf returns the lastAttempt of the keyed job, as jq gets it with
// its std.
func lastAttemptOf(jq *Client, key string) lastAttempt {
	job, err := jq.GetByEssence(&JobEssence{JobKey: key}, true, false)
	So(err, ShouldBeNil)
	So(job, ShouldNotBeNil)

	stdout, err := job.StdOut()
	So(err, ShouldBeNil)

	stderr, err := job.StdErr()
	So(err, ShouldBeNil)

	return lastAttempt{
		Exited: job.Exited, Exitcode: job.Exitcode, FailReason: job.FailReason,
		Host: job.Host, HostID: job.HostID, HostIP: job.HostIP, Pid: job.Pid, ActualCwd: job.ActualCwd,
		StartTime: job.StartTime.UTC(), EndTime: job.EndTime.UTC(),
		PeakRAM: job.PeakRAM, PeakDisk: job.PeakDisk, CPUtime: job.CPUtime, StdOut: stdout, StdErr: stderr,
	}
}

// managerLiveStd returns the std tails the manager's own copy of the keyed job
// holds, which a runner's touches set while it runs and its state records
// carry; wr status reads a ready job's std from the store instead.
func managerLiveStd(m *reserveTestManager, key string) [2]string {
	_, sjob := m.queued(key)

	sjob.RLock()
	defer sjob.RUnlock()

	stdout, err := compressedStdString(sjob.StdOutC)
	So(err, ShouldBeNil)

	stderr, err := compressedStdString(sjob.StdErrC)
	So(err, ShouldBeNil)

	return [2]string{stdout, stderr}
}

// TestStoreReserveClosing covers C3's errDBClosed case: a reservation whose
// record cannot be written because the store has closed under it is not handed
// out, and is not undone either, the manager being on its way out.
func TestStoreReserveClosing(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store manager holding a ready job, whose store closes as a reservation of it is "+
		"made", t, func() {
		reserveTestReportAtStop()

		logPath, logCtx := managerLogContext(t, ctx)
		m := startReserveTestManager(t, logCtx, storeKindLog, nil)
		key := m.add("closing")

		var closeErr error

		// it runs on the manager's goroutine, where So cannot.
		reservationQueuedHook = func(string) {
			reservationQueuedHook = nil
			closeErr = m.server.db.close(ctx)
		}

		Reset(func() { reservationQueuedHook = nil })

		Convey("a runner reserving it gets no job, and the job's item stays in the run sub-queue, counted as "+
			"neither notDurable nor abandoned", func() {
			reserved, err := m.jq.Reserve(reserveTestWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldBeNil)
			So(closeErr, ShouldBeNil)

			item, _ := m.queued(key)
			So(item.Stats().State, ShouldEqual, queue.ItemStateRun)

			m.stop(ctx)

			lines := logLinesContaining(readLogFile(t, logPath), reservationWaitLogMatch)
			So(lines, ShouldHaveLength, 1)
			So(storeStatsN(t, lines), ShouldEqual, 1)
			So(storeStatsSum(t, lines, "notDurable"), ShouldEqual, 0)
			So(storeStatsSum(t, lines, "abandoned"), ShouldEqual, 0)
		})
	})
}

// TestStoreReserveTTRFromHandOut covers spec C3 acceptance test 4: a
// reservation's TTR runs from its hand-out, not from when the queue reserved
// it, so time spent making it durable does not make its job lost.
func TestStoreReserveTTRFromHandOut(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		ttr   = 300 * time.Millisecond
		delay = 150 * time.Millisecond
		touch = 250 * time.Millisecond
	)

	ctx := context.Background()

	Convey("Given a log store manager with a 300ms TTR whose syncs each take 150ms, holding a ready job", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()
		syncs.set(delay, 0)

		m := startReserveTestManager(t, ctx, storeKindLog, func(c *ServerConfig) { c.Timings.ItemTTR = ttr })
		key := m.add("ttr")

		Convey("a reserved job touched 250ms after its hand-out is not marked lost", func() {
			reserved, err := m.jq.Reserve(reserveTestWait)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			time.Sleep(touch)

			killCalled, err := m.jq.Touch(reserved)
			So(err, ShouldBeNil)
			So(killCalled, ShouldBeFalse)

			// a touch recovers a lost job, but leaves its FailReason saying it
			// was lost.
			_, lost, failReason, ok := serverJobState(m.server, key)
			So(ok, ShouldBeTrue)
			So(lost, ShouldBeFalse)
			So(failReason, ShouldNotEqual, FailReasonLost)
		})
	})
}

// TestStoreReserveFailedSync covers spec C3 acceptance test 7: a reservation
// whose record fails is undone as a release would undo it, giving back what it
// took, and the job can be reserved again.
func TestStoreReserveFailedSync(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const limitGroup = "g"

	ctx := context.Background()

	Convey("Given a log store manager holding a job in limit group g of limit 1", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		m := startReserveTestManager(t, ctx, storeKindLog, nil)
		// a manager reserves by scheduler group, and so by limit group, only
		// with a runner command.
		m.server.setRC(serverRC)

		key := m.add("failed sync", limitGroup+":1")
		group := schedulerGroupString(reqForScheduler(m.reqs), []string{limitGroup})

		So(m.server.limiter.GetRemainingCapacity(ctx, []string{limitGroup}), ShouldEqual, 1)

		feed := m.server.statusCaster.Join()
		Reset(feed.Close)

		Convey("when a runner reserves with its scheduler group and the reservation's sync fails with EIO", func() {
			// as a wr runner, whose reservation the manager holds for it.
			m.jq.SetReserveAsRunner(true)
			syncs.set(0, 1)

			reserved, err := m.jq.ReserveScheduled(reserveTestWait, group)
			So(err, ShouldBeNil)

			Convey("it gets no job, and the job is ready with nothing of the reservation left, its limit "+
				"group's usage and runner hold given back and its move back to ready on the status feed; "+
				"a second reserve gets it", func() {
				So(reserved, ShouldBeNil)

				item, job := m.queued(key)
				So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

				job.RLock()
				attempts, state, reservedBy, handingOut := job.Attempts, job.State, job.ReservedBy, job.handingOut
				host, pid := job.Host, job.Pid
				job.RUnlock()

				So(attempts, ShouldEqual, 0)
				So(state, ShouldEqual, JobStateReady)
				So(reservedBy.IsNil(), ShouldBeTrue)
				So(host, ShouldBeEmpty)
				So(pid, ShouldEqual, 0)
				So(handingOut, ShouldBeFalse)
				So(m.server.limiter.GetRemainingCapacity(ctx, []string{limitGroup}), ShouldEqual, 1)

				_, held := m.server.runnerHolds.byClient.Load(m.jq.clientid)
				So(held, ShouldBeFalse)
				So(reserveTestFeedHas(feed, JobStateRunning, JobStateReady), ShouldBeTrue)

				again, err := m.jq.ReserveScheduled(reserveTestWait, group)
				So(err, ShouldBeNil)
				So(again, ShouldNotBeNil)
				So(again.Key(), ShouldEqual, key)
			})
		})
	})
}

// reserveTestFeedHas reports whether feed delivers, within reserveTestWait, a
// status count delta of one reserveTestRepGroup job from one state to another.
func reserveTestFeedHas(feed *casterMember, from, to JobState) bool {
	deadline := time.After(reserveTestWait)

	for {
		select {
		case msg := <-feed.In:
			if reserveTestDelta(msg, from, to) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// reserveTestDelta reports whether msg is a status count delta of one
// reserveTestRepGroup job from one state to another.
func reserveTestDelta(msg any, from, to JobState) bool {
	delta, ok := msg.(*jstateCount)

	return ok && delta.RepGroup == reserveTestRepGroup && delta.FromState == from && delta.ToState == to &&
		delta.Count == 1
}

// TestStoreReserveLimitGroupsGivenBack covers the limit-group step of spec C3's
// undo, and a release's: the limit groups a run took are given back even when
// the job's item is reserved again before that happens, and the new run's are
// left for it to give back.
func TestStoreReserveLimitGroupsGivenBack(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		limitGroup = "g"
		limit      = 2
	)

	ctx := context.Background()

	Convey("Given a log store manager holding a job in limit group g of limit 2, and a second runner", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		m := startReserveTestManager(t, ctx, storeKindLog, nil)
		m.server.setRC(serverRC)

		key := m.add("limit given back", limitGroup+":"+strconv.Itoa(limit))
		group := schedulerGroupString(reqForScheduler(m.reqs), []string{limitGroup})
		other := m.connect()

		capacity := func() int {
			return m.server.limiter.GetRemainingCapacity(ctx, []string{limitGroup})
		}

		// gotAgain checks that the second runner's reserve got the job.
		gotAgain := func(again *Job, err error) {
			So(err, ShouldBeNil)
			So(again, ShouldNotBeNil)
			So(again.Key(), ShouldEqual, key)
		}

		// releaseAgain has the second runner release the job, after which the
		// whole limit must be free.
		releaseAgain := func(again *Job) {
			So(capacity(), ShouldEqual, limit-1)
			So(other.Release(again, &JobEndState{}, "limit given back"), ShouldBeNil)
			So(pollUntilFor(reserveTestWait, func() bool { return capacity() == limit }), ShouldBeTrue)
		}

		Convey("when a reservation whose sync fails is abandoned, and the second runner reserves the job "+
			"before the abandon gives back its limit groups, the whole limit is free once that runner "+
			"releases it", func() {
			var (
				again    *Job
				againErr error
			)

			// it runs on the manager's goroutine, where So cannot.
			reservationAbandonedHook = func(string) {
				reservationAbandonedHook = nil
				again, againErr = other.ReserveScheduled(reserveTestWait, group)
			}

			Reset(func() { reservationAbandonedHook = nil })

			syncs.set(0, 1)

			reserved, err := m.jq.ReserveScheduled(reserveTestWait, group)
			So(err, ShouldBeNil)
			So(reserved, ShouldBeNil)
			gotAgain(again, againErr)

			releaseAgain(again)
		})

		Convey("when a release moves the job's item to ready, and the second runner reserves the job "+
			"before the release gives back its limit groups, the whole limit is free once that runner "+
			"releases it", func() {
			reserved, err := m.jq.ReserveScheduled(reserveTestWait, group)
			So(err, ShouldBeNil)
			So(reserved, ShouldNotBeNil)

			item, job := m.queued(key)
			So(m.server.q.SetDelay(key, 0), ShouldBeNil)

			// the steps releaseJob takes, with the second reservation landing
			// between its queue change and its finalisation.
			rep := lostJobReleaseReport()
			snap := releaseJobSnapshot(job, item, &rep)

			outcome, err := m.server.applyReleaseQueueChange(ctx, m.server.q, item, snap, job, nil)
			So(err, ShouldBeNil)
			So(outcome, ShouldEqual, releaseMoved)

			again, err := other.ReserveScheduled(reserveTestWait, group)
			gotAgain(again, err)

			So(m.server.finalizeReleasedJob(ctx, job, rep, outcome), ShouldBeNil)

			releaseAgain(again)
		})
	})
}

// TestStoreReserveReleaseLost covers spec C3 acceptance test 8: a crash after
// an abandoned reservation became durable, but before its release did, leaves
// the job reserved by a runner that never had it, which a manager then treats
// as any reserved job whose runner vanished, as it does on bbolt.
func TestStoreReserveReleaseLost(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store manager whose reservations wait at most 200ms, holding a ready job", t, func() {
		pauseLogFolder()

		var (
			armed     atomic.Bool
			syncsSeen atomic.Int32
			holdUntil atomic.Int64
			unblock   = make(chan struct{})
			unblocked sync.Once
		)

		release := func() { unblocked.Do(func() { close(unblock) }) }
		Reset(release)

		// once armed, the first sync, the reservation's, is held for 2s, and every
		// later one, its release's among them, until the test lets them go.
		Reset(wal.SetTestSyncHook(func(string) error {
			if !armed.Load() {
				return nil
			}

			if syncsSeen.Add(1) == 1 {
				time.Sleep(time.Until(time.Unix(0, holdUntil.Load())))
			} else {
				<-unblock
			}

			return nil
		}))

		m := startReserveTestManager(t, ctx, storeKindLog, nil)

		Reset(setClientMinRequestTimeout(reserveTestRequestTimeout))

		key := m.add("release lost")

		Convey("when a runner's reservation of it times out and becomes durable, but its release never "+
			"does", func() {
			holdUntil.Store(time.Now().Add(reserveTestHold).UnixNano())
			armed.Store(true)

			reserved, err := m.jq.Reserve(reserveTestBound / 2)
			So(err, ShouldBeNil)
			So(reserved, ShouldBeNil)

			s := m.logStore()

			So(pollUntilFor(reserveTestWait, func() bool {
				job, live, errl := s.liveJob(key)

				return errl == nil && live && job.State == JobStateReserved
			}), ShouldBeTrue)

			crashed := durableLogCopy(t, s)
			client := m.jq.clientid

			release()
			m.stop(ctx)

			Convey("the job recovers reserved by that runner, with no start record, and a manager on it treats "+
				"it as a reserved job whose runner vanished, as one on bbolt does", func() {
				recovered, live := reopenedLogJob(ctx, crashed, key)
				So(live, ShouldBeTrue)
				So(recovered.State, ShouldEqual, JobStateReserved)
				So(recovered.ReservedBy, ShouldEqual, client)

				for _, r := range stateRecordsOf(crashed, key) {
					rs := recordedRunState(r)
					So(rs.State, ShouldNotEqual, JobStateRunning)
					So(rs.StartTime.IsZero(), ShouldBeTrue)
				}

				boltDir := t.TempDir()
				boltStore := openStoreIn(ctx, storeKindBolt, boltDir)
				_, err = boltStore.storeNewJobsGuarded(ctx, []*Job{recovered}, false, nil)
				So(err, ShouldBeNil)
				So(boltStore.updateJobRunStateDurable(recovered), ShouldBeNil)
				So(boltStore.close(ctx), ShouldBeNil)

				onLog := reserveTestVanishedRunner(ctx, m.serverConfig, storeKindLog, crashed, key)
				onBolt := reserveTestVanishedRunner(ctx, m.serverConfig, storeKindBolt,
					filepath.Join(boltDir, "queue.db"), key)

				So(onLog, ShouldResemble, reserveTestRecovered{inRun: true, reservedBy: client.String(), lost: true})
				So(onBolt, ShouldResemble, onLog)
			})
		})
	})
}

// reserveTestVanishedRunner starts a manager configured as serverConfig, but
// with a short TTR, on the store of kind at dbFile, and returns what it made of
// the keyed job's reservation.
func reserveTestVanishedRunner(ctx context.Context, serverConfig ServerConfig, kind storeKind, dbFile,
	key string,
) reserveTestRecovered {
	const ttr = 300 * time.Millisecond

	serverConfig.storeKind = kind
	serverConfig.dontWipeDevDB = true
	serverConfig.DBFile = dbFile
	serverConfig.Timings.ItemTTR = ttr

	server, _, _, err := serve(ctx, serverConfig)
	So(err, ShouldBeNil)

	defer server.Stop(ctx, true)

	So(waitUntilRecovered(server), ShouldBeTrue)

	var got reserveTestRecovered

	inRun, _, _, ok := serverJobState(server, key)
	So(ok, ShouldBeTrue)

	got.inRun = inRun

	item, err := server.q.Get(key)
	So(err, ShouldBeNil)

	job, ok := item.Data().(*Job)
	So(ok, ShouldBeTrue)

	job.RLock()
	got.reservedBy = job.ReservedBy.String()
	job.RUnlock()

	got.lost = waitForJobLost(server, key, reserveTestWait)

	return got
}

// TestStoreReserveWriteFailures covers spec C3 acceptance test 9: while every
// sync fails, adds fail without stopping the manager answering, the failures
// are logged once, and once syncs work again so do adds.
func TestStoreReserveWriteFailures(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		failedAdds  = 3
		failedAddIn = 2 * time.Second
	)

	ctx := context.Background()

	Convey("Given a log store manager whose syncs all fail with EIO", t, func() {
		Reset(wal.SetTestRetryCap(reserveTestBound))

		syncs := &reserveTestSyncs{}
		syncs.install()

		logPath, logCtx := managerLogContext(t, ctx)
		m := startReserveTestManager(t, logCtx, storeKindLog, nil)

		syncs.set(0, -1)

		Convey("3 adds made within 2s each fail, the manager still answers a status request, and the failures "+
			"are logged in one line; once syncs work again, an add succeeds and is kept", func() {
			adding := time.Now()

			for i := range failedAdds {
				_, _, err := m.jq.Add([]*Job{{
					Cmd: "echo store reserve failing " + strconv.Itoa(i), Cwd: testCwd, RepGroup: reserveTestRepGroup,
					ReqGroup: reserveTestRepGroup, Requirements: m.reqs,
				}}, os.Environ(), true)
				So(err, ShouldNotBeNil)
			}

			So(time.Since(adding), ShouldBeLessThan, failedAddIn)

			_, err := m.jq.GetByRepGroup(reserveTestRepGroup, false, 0, "", false, false)
			So(err, ShouldBeNil)

			lines := logLinesContaining(readLogFile(t, logPath), `msg="`+walWriteFailedLogMsg+`"`)
			So(lines, ShouldHaveLength, 1)

			failures, err := strconv.Atoi(logLineValue(lines[0], "failures"))
			So(err, ShouldBeNil)
			So(failures, ShouldBeGreaterThanOrEqualTo, 1)

			syncs.set(0, 0)

			key := m.add("after failures")
			m.stop(ctx)

			_, live := reopenedLogJob(ctx, m.serverConfig.DBFile, key)
			So(live, ShouldBeTrue)
		})
	})
}

// TestStoreReserveBolt covers spec C3 acceptance tests 10 and 11: bbolt's
// reservation is done only once the drain that covers it is, a manager on it
// abandons a reservation held past its bound, and a store closed under a held
// drain leaves its reservation done and nothing running.
func TestStoreReserveBolt(t *testing.T) {
	if runnermode || servermode {
		return
	}

	const (
		shortHold  = 300 * time.Millisecond
		shortWait  = 50 * time.Millisecond
		longWait   = time.Second
		goroutines = time.Second
	)

	ctx := context.Background()

	Convey("Given a bbolt store whose best-effort drains can be held, holding a job", t, func() {
		// the store's own goroutines end when it closes, so only a count from
		// before it opened can show one the reservation left behind.
		before := runtime.NumGoroutine()

		hold := &boltDrainHold{}
		hold.install()

		s := openStoreIn(ctx, storeKindBolt, t.TempDir())
		closed := false

		Reset(func() {
			if !closed {
				So(s.close(ctx), ShouldBeNil)
			}

			hold.uninstall()
		})

		job := testDBJob("echo store reserve bolt", reserveTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		job.State = JobStateReserved

		Convey("when its drain is held for 300ms, a reservation is not done within 50ms but is within 1s", func() {
			hold.holdFor(shortHold)

			commit, err := s.reserveRunState(job)
			So(err, ShouldBeNil)

			done, err := commit.WaitWithin(shortWait)
			So(err, ShouldBeNil)
			So(done, ShouldBeFalse)

			done, err = commit.WaitWithin(longWait)
			So(err, ShouldBeNil)
			So(done, ShouldBeTrue)
		})

		Convey("when its drain is held for 2s and it is closed after a reservation is not done within its "+
			"bound, close returns nil once the hold ends, the reservation is done, and no goroutine is left", func() {
			hold.holdFor(reserveTestHold)

			commit, err := s.reserveRunState(job)
			So(err, ShouldBeNil)

			done, err := commit.WaitWithin(reserveTestBound)
			So(err, ShouldBeNil)
			So(done, ShouldBeFalse)

			closed = true

			So(s.close(ctx), ShouldBeNil)

			done, err = commit.WaitWithin(0)
			So(err, ShouldBeNil)
			So(done, ShouldBeTrue)

			So(pollUntilFor(goroutines, func() bool { return runtime.NumGoroutine() <= before }), ShouldBeTrue)
		})
	})

	Convey("Given a bbolt manager whose reservations wait at most 200ms and whose drains are held for 2s, "+
		"holding a ready job", t, func() {
		hold := &boltDrainHold{}
		hold.install()

		m := startReserveTestManager(t, ctx, storeKindBolt, nil)

		Reset(hold.uninstall)
		Reset(setClientMinRequestTimeout(reserveTestRequestTimeout))

		key := m.add("bolt held")

		Convey("a runner reserving it gets no job, and the job is ready, with no runner also once the hold has "+
			"ended and the manager is restarted", func() {
			hold.holdFor(reserveTestHold)

			reserved, err := m.jq.Reserve(reserveTestBound / 2)
			So(err, ShouldBeNil)
			So(reserved, ShouldBeNil)

			item, _ := m.queued(key)
			So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

			hold.holdFor(0)
			m.restart(ctx)

			item, job := m.queued(key)
			So(item.Stats().State, ShouldEqual, queue.ItemStateReady)

			job.RLock()
			state, reservedBy := job.State, job.ReservedBy
			job.RUnlock()

			So(state, ShouldEqual, JobStateReady)
			So(reservedBy.IsNil(), ShouldBeTrue)
		})
	})
}

// lastAttempt is what wr status shows of a job's last attempt.
type lastAttempt struct {
	Exited               bool
	Exitcode             int
	FailReason           string
	Host, HostID, HostIP string
	Pid                  int
	ActualCwd            string
	StartTime, EndTime   time.Time
	PeakRAM              int
	PeakDisk             int64
	CPUtime              time.Duration
	StdOut, StdErr       string
}

// reserveTestSyncs is a wal sync hook a test controls: it can hold syncs until
// a time, delay each, and fail some with EIO.
type reserveTestSyncs struct {
	mu       sync.Mutex
	until    time.Time
	delay    time.Duration
	failures int // how many more syncs fail; -1 for every one
}

// install makes s the sync hook of the log writers made from now until the
// Convey block ends.
func (s *reserveTestSyncs) install() {
	Reset(wal.SetTestSyncHook(s.sync))
}

// sync is the hook.
func (s *reserveTestSyncs) sync(string) error {
	s.mu.Lock()
	until, delay := s.until, s.delay

	fail := s.failures != 0
	if s.failures > 0 {
		s.failures--
	}
	s.mu.Unlock()

	time.Sleep(max(time.Until(until), delay))

	if fail {
		return syscall.EIO
	}

	return nil
}

// holdFor holds every sync made from now until d has passed, returning when
// that will be.
func (s *reserveTestSyncs) holdFor(d time.Duration) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.until = time.Now().Add(d)

	return s.until
}

// set sets s's delay and failures.
func (s *reserveTestSyncs) set(delay time.Duration, failures int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.delay, s.failures = delay, failures
}

// TestStoreReserveRevivingBounded covers C3's bound on a reservation of a key
// another write is making live (item 1e.1): reserveRunState does not wait for
// that write, so the reservation's whole wait for durability is its bound, and
// its record is still made once that write has finished. A change queued
// meanwhile, such as an abandoned reservation's, likewise does not wait, and is
// still recorded.
func TestStoreReserveRevivingBounded(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	Convey("Given a log store holding a job that a put-back held by a 2s sync stall is making live", t, func() {
		syncs := &reserveTestSyncs{}
		syncs.install()

		s, dir := openTestLogStore(t, ctx)

		job := testDBJob("echo store reserve reviving", reserveTestRepGroup)
		_, err := s.storeNewJobsGuarded(ctx, []*Job{job}, false, nil)
		So(err, ShouldBeNil)

		released := syncs.holdFor(reserveTestHold)
		putBack := make(chan error, 1)

		go func() { putBack <- s.storeLiveForRerun([]*Job{job}) }()

		awaitReviving(s, job.Key())

		Convey("a reservation of it returns at once and is not durable within its bound, then is once the "+
			"put-back has finished", func() {
			job.Lock()
			job.State = JobStateReserved
			job.Unlock()

			reserving := time.Now()

			commit, err := s.reserveRunState(job)
			So(err, ShouldBeNil)

			done, err := commit.WaitWithin(reserveTestBound)
			So(err, ShouldBeNil)
			So(done, ShouldBeFalse)
			So(time.Since(reserving), ShouldBeLessThan, reserveTestAnswered)

			done, err = commit.WaitWithin(time.Until(released) + reserveTestWait)
			So(err, ShouldBeNil)
			So(done, ShouldBeTrue)
			So(<-putBack, ShouldBeNil)

			recovered, live, err := s.liveJob(job.Key())
			So(err, ShouldBeNil)
			So(live, ShouldBeTrue)
			So(recovered.State, ShouldEqual, JobStateReserved)
		})

		Convey("a change of it queued meanwhile returns at once, and is recorded once the put-back has "+
			"finished, with a higher seq than the put-back's, so a reopen recovers it", func() {
			changing := time.Now()

			s.queueJobChangeAhead(ctx, job, nil, func(j *Job) { j.State = JobStateBuried })
			So(time.Since(changing), ShouldBeLessThan, reserveTestAnswered)
			So(<-putBack, ShouldBeNil)

			So(pollUntilFor(time.Until(released)+reserveTestWait, func() bool {
				changed, live, errl := s.liveJob(job.Key())

				return errl == nil && live && changed.State == JobStateBuried
			}), ShouldBeTrue)

			reopened := reopenLogStore(ctx, s)

			recovered, live, err := reopened.liveJob(job.Key())
			So(err, ShouldBeNil)
			So(live, ShouldBeTrue)
			So(recovered.State, ShouldEqual, JobStateBuried)

			records := stateRecordsOf(dir, job.Key())
			So(len(records), ShouldBeGreaterThanOrEqualTo, 2)

			last := records[len(records)-1]
			So(recordedRunState(last).State, ShouldEqual, JobStateBuried)

			for _, r := range records[:len(records)-1] {
				So(r.seq, ShouldBeLessThan, last.seq)
			}
		})
	})
}

// reserveTestRecovered is what a manager made of a recovered reservation:
// whether its item was in the Run sub-queue, by whom it was reserved, and
// whether it was marked lost once its TTR passed.
type reserveTestRecovered struct {
	inRun      bool
	reservedBy string
	lost       bool
}

// boltDrainHold holds bbolt's best-effort drains, through
// bestEffortSwappedHook, until a time, or delays each.
type boltDrainHold struct {
	until atomic.Int64
	delay atomic.Int64
}

// install makes h hold the drains of bbolt stores until the Convey block
// ends. Call it before any store is opened, and only once every store the
// test opens is closed is the hook removed, as Reset runs in order.
func (h *boltDrainHold) install() {
	bestEffortSwappedHook = h.wait
}

// uninstall removes the hook; call it once every store is closed.
func (h *boltDrainHold) uninstall() {
	bestEffortSwappedHook = nil
}

// wait is the hook. A drain's hook runs once it has taken the writes queued
// for it, so a delay holds every write it covers for at least that long.
func (h *boltDrainHold) wait() {
	time.Sleep(max(time.Until(time.Unix(0, h.until.Load())), time.Duration(h.delay.Load())))
}

// delayEach delays every drain from now on by d.
func (h *boltDrainHold) delayEach(d time.Duration) {
	h.delay.Store(int64(d))
}

// holdFor holds every drain from now until d has passed.
func (h *boltDrainHold) holdFor(d time.Duration) {
	h.until.Store(time.Now().Add(d).UnixNano())
}
